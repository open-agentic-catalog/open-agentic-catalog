//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/draftfile"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestImportDraftCLIRealAPI(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	binary := buildCatalogctlProcessBinary(t, ctx)
	fault := &atomic.Int32{}
	runtime, server, archiveOrigin, _, writes := configuredDemoRuntimeWithPeerControl(t, fault)
	seed, err := executeExpandedDemo(ctx, server.URL, filepath.Join(canonicalTempDir(t), "seed.json"), archiveOrigin, false)
	if err != nil {
		t.Fatal(err)
	}
	token := clientSkillToken(t, "local-contributor")
	runAt := func(origin string, exitCode int, args ...string) ([]byte, string) {
		t.Helper()
		if len(args) < 2 || args[1] != "preview" {
			args = append(args, "--base-url", origin, "--token-file", token)
		}
		command := exec.CommandContext(ctx, binary, args...) // #nosec G204,G702 -- owned binary, loopback API and synthetic files.
		command.Env = append(catalogctlProcessEnvironment(t), "CATALOG_CONFIG_FILE=/missing/PRIVATE_SENTINEL", "CATALOG_REMOTE_TOKEN=ignored", "CATALOG_REMOTE_CA_FILE=")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if command.ProcessState == nil || command.ProcessState.ExitCode() != exitCode || (stderr.Len() == 0) != (exitCode == 0) || strings.Contains(stderr.String(), "PRIVATE_SENTINEL") || strings.Contains(stdout.String(), "local-contributor") {
			t.Fatalf("CLI: %v stdout=%s stderr=%s", err, &stdout, &stderr)
		}
		return stdout.Bytes(), stderr.String()
	}
	run := func(exitCode int, args ...string) []byte {
		t.Helper()
		out, _ := runAt(server.URL, exitCode, args...)
		return out
	}
	read := func(path string, target any) {
		t.Helper()
		response, err := requestRemote(ctx, remoteOptions{baseURL: server.URL, tokenFile: token, timeout: 20 * time.Second, maxResponseBytes: remoteMaxBody, path: path})
		if err != nil || json.Unmarshal(response.Data, target) != nil {
			t.Fatalf("read %s: %v", path, err)
		}
	}
	seen := map[catalog.Kind]bool{}
	var agent catalog.CatalogRecord
	for _, item := range seed.Items {
		if item.ReleaseID == "" || seen[item.Kind] {
			continue
		}
		seen[item.Kind] = true
		var record catalog.CatalogRecord
		read("/api/v1/catalog/resources/"+item.ReleaseID, &record)
		if item.Kind == catalog.KindAgent {
			agent = record
		}
		directory := filepath.Join(canonicalTempDir(t), "export")
		run(0, "export", "--format", "draft", "--version-revision-id", record.VersionRevision.ID, "--listing-revision-id", record.ListingRevision.ID, "--output-dir", directory)
		filename := filepath.Join(directory, "draft.json")
		raw, err := os.ReadFile(filename) // #nosec G304 -- fixed file in owned export directory.
		if err != nil {
			t.Fatal(err)
		}
		file, err := draftfile.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		file.CanonicalIdentifier = "custom:reusable-" + string(item.Kind)
		writeImportDraftTest(t, filename, file)
		var preview importBatchPreview
		if json.Unmarshal(run(0, "import", "preview", "--file", filename, "--namespace-id", record.Resource.NamespaceID, "--publisher-id", record.ListingRevision.PublisherID), &preview) != nil || !preview.LocallyValid {
			t.Fatal("four-kind offline preview failed")
		}
		state := filepath.Join(canonicalTempDir(t), "private", "state.json")
		var outcome importOutcome
		args := []string{"--file", filename, "--namespace-id", record.Resource.NamespaceID, "--publisher-id", record.ListingRevision.PublisherID, "--state-file", state}
		if json.Unmarshal(run(0, append([]string{"import", "apply"}, args...)...), &outcome) != nil || outcome.Disposition != "complete" || len(outcome.Items[0].Steps) != 4 {
			t.Fatal("four-kind shared file did not create the complete draft")
		}
		var retained struct {
			Revision catalog.VersionRevision `json:"revision"`
			Evidence catalog.EvidenceSet     `json:"evidence"`
		}
		read("/api/v1/version-revisions/"+outcome.Items[0].Steps[1].Ack.RevisionID, &retained)
		if retained.Evidence.State != catalog.EvidenceComplete || len(retained.Revision.Representations) != len(record.VersionRevision.Representations) {
			t.Fatal("shared import changed native representation count or evidence")
		}
		var complete importOutcome
		if json.Unmarshal(run(0, append([]string{"import", "resume"}, args...)...), &complete) != nil || complete.Disposition != "complete" {
			t.Fatal("completed shared import did not resume")
		}
		if item.Kind == catalog.KindPlugin {
			// Different archive bytes are rejected through normal admission.
			// The unit request assertion separately checks checksum forwarding;
			// this fixture also enforces its independent archive allowlist.
			fault.Store(2)
			file.CanonicalIdentifier = "custom:changed-reusable-plugin"
			writeImportDraftTest(t, filename, file)
			failedState := filepath.Join(canonicalTempDir(t), "private", "state.json")
			var failed importOutcome
			if json.Unmarshal(run(1, "import", "apply", "--file", filename, "--namespace-id", record.Resource.NamespaceID, "--content-only", "--state-file", failedState), &failed) != nil || len(failed.Items[0].Steps) != 2 || failed.Items[0].Steps[0].State != "acknowledged" || failed.Items[0].Steps[1].State != "failed" {
				t.Fatal("changed Plugin archive was admitted or lost partial progress")
			}
			var versions struct {
				Items []catalog.ResourceVersion `json:"items"`
			}
			read("/api/v1/resources/"+failed.Items[0].Steps[0].Ack.ID+"/versions", &versions)
			if len(versions.Items) != 0 {
				t.Fatal("checksum rejection retained a Version")
			}
			fault.Store(0)
		}
	}
	if len(seen) != 4 || agent.Resource.ID == "" {
		t.Fatal("four-kind export/import coverage incomplete")
	}
	verifyDraftReadbackRecovery(t, runtime.HTTP.Handler(), agent.Resource, runAt, read)
	// The reusable file contains only a handoff. Package bytes come from one
	// confined explicit CLI argument and retain the normal server inspection.
	packageDir := canonicalTempDir(t)
	packageFile := filepath.Join(packageDir, "draft.json")
	handoff := `{"oci":{"reference":"registry.example.com/safe-skill@sha256:` + strings.Repeat("a", 64) + `"}}`
	packaged := draftfile.File{Format: draftfile.Format, Kind: catalog.KindSkill, CanonicalIdentifier: "custom:reusable-package", Version: "1.0.0", Representations: []draftfile.Representation{{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, ProfileID: adapter.ProfileAgentSkills, Input: draftfile.Input{Mode: "skill-package", Handoff: &handoff}}}}
	writeImportDraftTest(t, packageFile, packaged)
	writeImportTestFile(t, packageDir, "safe-skill/SKILL.md", []byte("---\nname: safe-skill\ndescription: Inspect metadata.\n---\nDo not execute this example.\n"), 0600)
	writeImportTestFile(t, packageDir, "safe-skill/scripts/run.sh", []byte("#!/bin/sh\nexit 1\n"), 0700)
	missingState := filepath.Join(canonicalTempDir(t), "private", "state.json")
	before := writes.Load()
	run(1, "import", "preview", "--file", packageFile, "--namespace-id", agent.Resource.NamespaceID)
	run(1, "import", "apply", "--file", packageFile, "--namespace-id", agent.Resource.NamespaceID, "--state-file", missingState)
	if writes.Load() != before {
		t.Fatal("missing package created remote records")
	}
	if _, err := os.Stat(missingState); !os.IsNotExist(err) {
		t.Fatal("missing package created a command receipt")
	}
	var packageResult importOutcome
	raw := run(0, "import", "apply", "--file", packageFile, "--namespace-id", agent.Resource.NamespaceID, "--package-directory", "1=safe-skill", "--state-file", filepath.Join(canonicalTempDir(t), "private", "state.json"))
	if json.Unmarshal(raw, &packageResult) != nil || packageResult.Disposition != "complete" || len(packageResult.Items[0].Steps) != 2 {
		t.Fatal("separate Skill package import failed")
	}
	var retainedPackage struct {
		Revision catalog.VersionRevision `json:"revision"`
	}
	read("/api/v1/version-revisions/"+packageResult.Items[0].Steps[1].Ack.RevisionID, &retainedPackage)
	inspection := retainedPackage.Revision.Representations[0].Package
	if inspection == nil || inspection.FileCount != 2 || len(inspection.Files) != 2 {
		t.Fatal("shared package import lost server inspection")
	}
	var executable bool
	for _, file := range inspection.Files {
		if file.Path == "safe-skill/scripts/run.sh" && file.Executable {
			executable = true
		}
	}
	if !executable {
		t.Fatal("shared package import lost executable evidence")
	}

	// A scoped author can add content without create or Publisher browsing.
	// resource.edit retains its registered read/history/content companions.
	// An exact Listing ID avoids the collection lookup.
	contributor, err := runtime.Authenticator.Authenticate(ctx, "local-contributor")
	if err != nil {
		t.Fatal(err)
	}
	contributorCtx := identity.WithActor(ctx, contributor)
	longTarget, err := runtime.Catalog.CreateResource(contributorCtx, catalog.CreateResourceInput{NamespaceID: agent.Resource.NamespaceID, Kind: catalog.KindAgent, CanonicalIdentifier: "custom:" + strings.Repeat("界", 505)})
	if err != nil {
		t.Fatal(err)
	}
	longListing, err := runtime.Catalog.CreateListing(contributorCtx, catalog.CreateListingInput{NamespaceID: agent.Resource.NamespaceID, ResourceID: longTarget.ID})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := runtime.Authenticator.Authenticate(ctx, "local-admin")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := runtime.Authenticator.Authenticate(ctx, "local-reader")
	if err != nil {
		t.Fatal(err)
	}
	adminCtx := identity.WithActor(ctx, admin)
	assignments, err := runtime.Identity.ListRoleAssignments(adminCtx, identity.RoleAssignmentListRequest{MembershipID: reader.MembershipID, Limit: 100})
	if err != nil || len(assignments) != 1 {
		t.Fatal("reader fixture assignment missing", err)
	}
	if _, err = runtime.Identity.RevokeRole(adminCtx, assignments[0].ID, "Test exact draft import", "import-draft-revoke", assignments[0].Revision); err != nil {
		t.Fatal(err)
	}
	role, err := runtime.Identity.SaveRoleDefinition(adminCtx, "", 0, identity.RoleDefinitionInput{Name: "Exact draft author", Permissions: []identity.Permission{"resource.read", "resource.read_history", "resource.read_content", "resource.edit"}, Reason: "Test exact Version and Listing import"}, false, "import-draft-role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Identity.GrantAuthorizationAssignment(adminCtx, identity.AccessAssignmentInput{MembershipID: reader.MembershipID, RoleDefinitionID: role.ID, NamespaceID: agent.Resource.NamespaceID, Reason: "Test exact draft target"}, "import-draft-grant"); err != nil {
		t.Fatal(err)
	}
	token = clientSkillToken(t, "local-reader")
	for _, withListing := range []bool{false, true} {
		filename, file := importDraftTestFile(t, withListing)
		file.CanonicalIdentifier, file.Version = longTarget.CanonicalIdentifier, "shared-new-version-content"
		if withListing {
			file.Version = "shared-new-version-listing"
		}
		writeImportDraftTest(t, filename, file)
		// The target's valid 512-character identifier exceeds 768 bytes. It
		// belongs to the exact read; both real upload bodies fit this limit.
		args := []string{"--file", filename, "--resource-id", longTarget.ID, "--max-request-bytes", "768", "--state-file", filepath.Join(canonicalTempDir(t), "private", "state.json")}
		wantSteps := 1
		if withListing {
			wantSteps = 2
			args = append(args, "--publisher-id", agent.ListingRevision.PublisherID, "--listing-id", longListing.ID)
		}
		for _, action := range []string{"apply", "resume"} {
			var outcome importOutcome
			if json.Unmarshal(run(0, append([]string{"import", action}, args...)...), &outcome) != nil || outcome.Disposition != "complete" || len(outcome.Items[0].Steps) != wantSteps {
				t.Fatal("exact scoped author could not import without create/Publisher read")
			}
		}
	}
	// Creation still requires resource.create. Cross-tenant target reads also
	// fail before a command receipt is created.
	filename, file := importDraftTestFile(t, true)
	file.CanonicalIdentifier, file.Version = agent.Resource.CanonicalIdentifier, "must-not-create"
	writeImportDraftTest(t, filename, file)
	for _, otherTenant := range []bool{false, true} {
		if otherTenant {
			token = clientSkillToken(t, "other-tenant-admin")
		}
		state := filepath.Join(canonicalTempDir(t), "private", "state.json")
		args := []string{"import", "apply", "--file", filename, "--publisher-id", agent.ListingRevision.PublisherID, "--state-file", state}
		if otherTenant {
			args = append(args, "--resource-id", agent.Resource.ID)
		} else {
			args = append(args, "--namespace-id", agent.Resource.NamespaceID)
		}
		run(1, args...)
		if _, err = os.Stat(state); !os.IsNotExist(err) {
			t.Fatal("denied import created a command receipt")
		}
	}
}

// Lose only the final revision read, after the real API acknowledged the Version.
func verifyDraftReadbackRecovery(t *testing.T, api http.Handler, resource catalog.Resource, runAt func(string, int, ...string) ([]byte, string), read func(string, any)) {
	t.Helper()
	filename, file := importDraftTestFile(t, false)
	file.CanonicalIdentifier, file.Version = resource.CanonicalIdentifier, "readback-recovery"
	writeImportDraftTest(t, filename, file)
	type versionResult struct {
		Version  catalog.ResourceVersion `json:"version"`
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	var created atomic.Pointer[versionResult]
	var revisionReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		original := created.Load()
		create := r.Method == http.MethodPost && r.URL.Path == "/api/v1/resources/"+resource.ID+"/versions"
		verify := r.Method == http.MethodGet && original != nil && r.URL.Path == "/api/v1/version-revisions/"+original.Revision.ID
		if !create && !verify {
			if r.Method != http.MethodGet {
				t.Errorf("unexpected authoring request: %s %s", r.Method, r.URL.Path)
			}
			api.ServeHTTP(w, r)
			return
		}
		reply := httptest.NewRecorder()
		api.ServeHTTP(reply, r)
		var result versionResult
		if err := json.Unmarshal(reply.Body.Bytes(), &result); err != nil {
			t.Errorf("decode Catalog response: %v", err)
			http.Error(w, "invalid fixture response", http.StatusInternalServerError)
			return
		}
		if create {
			if reply.Code != http.StatusCreated || result.Version.ResourceID != resource.ID || result.Version.Version != file.Version {
				t.Error("invalid Version acknowledgement from the selected Resource")
			}
			if result.Version.ID == "" || result.Revision.ResourceVersionID != result.Version.ID || result.Revision.ID == "" || result.Evidence.ID == "" || result.Revision.ContentSetDigest == "" || result.Evidence.State != catalog.EvidenceComplete {
				t.Error("created Version is missing its revision or validation evidence")
			}
			if !created.CompareAndSwap(nil, &result) {
				t.Error("repeated Version create")
			}
		} else {
			if reply.Code != http.StatusOK || !reflect.DeepEqual(result.Revision, original.Revision) || !reflect.DeepEqual(result.Evidence, original.Evidence) {
				t.Error("original revision read did not match the acknowledged revision and evidence")
			}
			if revisionReads.Add(1) == 1 {
				w.Header().Set("Content-Type", "application/problem+json")
				w.Header().Set("X-Request-ID", "fixture-read-outage")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"type":"about:blank","title":"Service Unavailable","status":503,"code":"DEPENDENCY_UNAVAILABLE","detail":"temporary read outage","requestId":"fixture-read-outage"}`))
				return
			}
		}
		for key, values := range reply.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(reply.Code)
		_, _ = w.Write(reply.Body.Bytes())
	}))
	defer server.Close()
	state := filepath.Join(canonicalTempDir(t), "private", "state.json")
	args := []string{"--file", filename, "--resource-id", resource.ID, "--state-file", state}
	raw, diagnostic := runAt(server.URL, 1, append([]string{"import", "apply"}, args...)...)
	var outcome importOutcome
	if revisionReads.Load() != 1 || !strings.Contains(diagnostic, "exact retained draft reads failed or disagree") || json.Unmarshal(raw, &outcome) != nil || outcome.Disposition != "uncertain" || len(outcome.Items) != 1 || outcome.Items[0].Disposition != "uncertain" {
		t.Fatalf("failed final read: report=%s diagnostic=%s", raw, diagnostic)
	}
	before, err := os.ReadFile(state) // #nosec G304 -- exact private receipt in the test-owned directory.
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := decodeImportReceipt(before)
	if err != nil || receipt.Format != 3 || len(receipt.Steps) != 1 || receipt.Steps[0].State != "acknowledged" {
		t.Fatalf("missing acknowledged Version in private receipt: %v", err)
	}
	ack, original := receipt.Steps[0].Ack, created.Load()
	if original == nil || ack.ID != original.Version.ID || ack.RevisionID != original.Revision.ID || ack.EvidenceID != original.Evidence.ID || ack.Digest != original.Revision.ContentSetDigest {
		t.Fatal("receipt differs from the actual Version acknowledgement")
	}
	steps := outcome.Items[0].Steps
	if len(steps) != 1 || steps[0].State != "acknowledged" || steps[0].Ack.ID != ack.ID || steps[0].Ack.RevisionID != ack.RevisionID || steps[0].Ack.EvidenceID != ack.EvidenceID || steps[0].Ack.Digest != ack.Digest {
		t.Fatal("incomplete report lost the acknowledged Version")
	}
	raw, _ = runAt(server.URL, 0, append([]string{"import", "resume"}, args...)...)
	var recovered importOutcome
	if json.Unmarshal(raw, &recovered) != nil || recovered.Disposition != "complete" || len(recovered.Items) != 1 || recovered.Items[0].Disposition != "complete" || !reflect.DeepEqual(recovered.Items[0].Steps, steps) {
		t.Fatalf("resume did not report the original Version as complete: %s", raw)
	}
	after, err := os.ReadFile(state) // #nosec G304 -- same test-owned receipt as before resume.
	if err != nil || !bytes.Equal(before, after) || revisionReads.Load() < 2 {
		t.Fatalf("resume did not re-read the revision and preserve the receipt: %v", err)
	}
	var version catalog.ResourceVersion
	var retained versionResult
	read("/api/v1/resource-versions/"+ack.ID, &version)
	read("/api/v1/version-revisions/"+ack.RevisionID, &retained)
	if !reflect.DeepEqual(version, original.Version) || !reflect.DeepEqual(retained.Revision, original.Revision) || !reflect.DeepEqual(retained.Evidence, original.Evidence) {
		t.Fatal("independent Catalog reads differ from the committed Version, revision or evidence")
	}
}
