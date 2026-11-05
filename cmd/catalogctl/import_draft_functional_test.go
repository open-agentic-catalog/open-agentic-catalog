//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	binary := filepath.Join(t.TempDir(), "catalogctl")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, ".") // #nosec G204,G702 -- fixed source and invocation-owned binary.
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", raw, err)
	}
	fault := &atomic.Int32{}
	runtime, server, archiveOrigin, _, writes := configuredDemoRuntimeWithPeerControl(t, fault)
	seed, err := executeExpandedDemo(ctx, server.URL, filepath.Join(t.TempDir(), "seed.json"), archiveOrigin, false)
	if err != nil {
		t.Fatal(err)
	}
	token := clientSkillToken(t, "local-contributor")
	run := func(success bool, args ...string) []byte {
		t.Helper()
		if len(args) < 2 || args[1] != "preview" {
			args = append(args, "--base-url", server.URL, "--token-file", token)
		}
		command := exec.CommandContext(ctx, binary, args...) // #nosec G204,G702 -- owned binary, loopback API and synthetic files.
		command.Env = append(os.Environ(), "CATALOG_CONFIG_FILE=/missing/PRIVATE_SENTINEL", "CATALOG_REMOTE_TOKEN=ignored", "CATALOG_REMOTE_CA_FILE=")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if (err == nil) != success || success && stderr.Len() != 0 || strings.Contains(stderr.String(), "PRIVATE_SENTINEL") || strings.Contains(stdout.String(), "local-contributor") {
			t.Fatalf("CLI: %v stdout=%s stderr=%s", err, &stdout, &stderr)
		}
		return stdout.Bytes()
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
		directory := filepath.Join(t.TempDir(), "export")
		run(true, "export", "--format", "draft", "--version-revision-id", record.VersionRevision.ID, "--listing-revision-id", record.ListingRevision.ID, "--output-dir", directory)
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
		if json.Unmarshal(run(true, "import", "preview", "--file", filename, "--namespace-id", record.Resource.NamespaceID, "--publisher-id", record.ListingRevision.PublisherID), &preview) != nil || !preview.LocallyValid {
			t.Fatal("four-kind offline preview failed")
		}
		state := filepath.Join(t.TempDir(), "private", "state.json")
		var outcome importOutcome
		args := []string{"--file", filename, "--namespace-id", record.Resource.NamespaceID, "--publisher-id", record.ListingRevision.PublisherID, "--state-file", state}
		if json.Unmarshal(run(true, append([]string{"import", "apply"}, args...)...), &outcome) != nil || outcome.Disposition != "complete" || len(outcome.Items[0].Steps) != 4 {
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
		if json.Unmarshal(run(true, append([]string{"import", "resume"}, args...)...), &complete) != nil || complete.Disposition != "complete" {
			t.Fatal("completed shared import did not resume")
		}
		if item.Kind == catalog.KindPlugin {
			// Different archive bytes are rejected through normal admission.
			// The unit request assertion separately checks checksum forwarding;
			// this fixture also enforces its independent archive allowlist.
			fault.Store(2)
			file.CanonicalIdentifier = "custom:changed-reusable-plugin"
			writeImportDraftTest(t, filename, file)
			failedState := filepath.Join(t.TempDir(), "private", "state.json")
			var failed importOutcome
			if json.Unmarshal(run(false, "import", "apply", "--file", filename, "--namespace-id", record.Resource.NamespaceID, "--content-only", "--state-file", failedState), &failed) != nil || len(failed.Items[0].Steps) != 2 || failed.Items[0].Steps[0].State != "acknowledged" || failed.Items[0].Steps[1].State != "failed" {
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
	// The reusable file contains only a handoff. Package bytes come from one
	// confined explicit CLI argument and retain the normal server inspection.
	packageDir := t.TempDir()
	packageFile := filepath.Join(packageDir, "draft.json")
	handoff := `{"oci":{"reference":"registry.example.com/safe-skill@sha256:` + strings.Repeat("a", 64) + `"}}`
	packaged := draftfile.File{Format: draftfile.Format, Kind: catalog.KindSkill, CanonicalIdentifier: "custom:reusable-package", Version: "1.0.0", Representations: []draftfile.Representation{{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, ProfileID: adapter.ProfileAgentSkills, Input: draftfile.Input{Mode: "skill-package", Handoff: &handoff}}}}
	writeImportDraftTest(t, packageFile, packaged)
	writeImportTestFile(t, packageDir, "safe-skill/SKILL.md", []byte("---\nname: safe-skill\ndescription: Inspect metadata.\n---\nDo not execute this example.\n"), 0600)
	writeImportTestFile(t, packageDir, "safe-skill/scripts/run.sh", []byte("#!/bin/sh\nexit 1\n"), 0700)
	missingState := filepath.Join(t.TempDir(), "private", "state.json")
	before := writes.Load()
	run(false, "import", "preview", "--file", packageFile, "--namespace-id", agent.Resource.NamespaceID)
	run(false, "import", "apply", "--file", packageFile, "--namespace-id", agent.Resource.NamespaceID, "--state-file", missingState)
	if writes.Load() != before {
		t.Fatal("missing package created remote records")
	}
	if _, err := os.Stat(missingState); !os.IsNotExist(err) {
		t.Fatal("missing package created a command receipt")
	}
	var packageResult importOutcome
	raw := run(true, "import", "apply", "--file", packageFile, "--namespace-id", agent.Resource.NamespaceID, "--package-directory", "1=safe-skill", "--state-file", filepath.Join(t.TempDir(), "private", "state.json"))
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
		args := []string{"--file", filename, "--resource-id", longTarget.ID, "--max-request-bytes", "768", "--state-file", filepath.Join(t.TempDir(), "private", "state.json")}
		wantSteps := 1
		if withListing {
			wantSteps = 2
			args = append(args, "--publisher-id", agent.ListingRevision.PublisherID, "--listing-id", longListing.ID)
		}
		for _, action := range []string{"apply", "resume"} {
			var outcome importOutcome
			if json.Unmarshal(run(true, append([]string{"import", action}, args...)...), &outcome) != nil || outcome.Disposition != "complete" || len(outcome.Items[0].Steps) != wantSteps {
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
		state := filepath.Join(t.TempDir(), "private", "state.json")
		args := []string{"import", "apply", "--file", filename, "--publisher-id", agent.ListingRevision.PublisherID, "--state-file", state}
		if otherTenant {
			args = append(args, "--resource-id", agent.Resource.ID)
		} else {
			args = append(args, "--namespace-id", agent.Resource.NamespaceID)
		}
		run(false, args...)
		if _, err = os.Stat(state); !os.IsNotExist(err) {
			t.Fatal("denied import created a command receipt")
		}
	}
}
