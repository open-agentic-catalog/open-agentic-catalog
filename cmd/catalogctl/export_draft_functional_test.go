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

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/draftfile"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestExportDraftCLIEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	binary := buildCatalogctlProcessBinary(t, ctx)
	fault := &atomic.Int32{}
	runtime, server, archiveOrigin, _, writes := configuredDemoRuntimeWithPeerControl(t, fault)
	seed, err := executeExpandedDemo(ctx, server.URL, filepath.Join(canonicalTempDir(t), "seed.json"), archiveOrigin, false)
	if err != nil {
		t.Fatal(err)
	}
	readerToken := clientSkillToken(t, "local-reader")
	contributor, err := runtime.Authenticator.Authenticate(ctx, "local-contributor")
	if err != nil {
		t.Fatal(err)
	}
	run := func(success bool, token string, args ...string) []byte {
		t.Helper()
		arguments := append([]string{"export"}, args...)
		arguments = append(arguments, "--base-url", server.URL, "--token-file", token)
		command := exec.CommandContext(ctx, binary, arguments...) // #nosec G204,G702 -- owned binary, local API and synthetic credentials only.
		command.Env = append(catalogctlProcessEnvironment(t), "CATALOG_REMOTE_TOKEN=ignored", "CATALOG_CONFIG_FILE=/missing/PRIVATE_SENTINEL", "CATALOG_DATABASE_DSN=ignored")
		var output, diagnostic bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostic
		err := command.Run()
		if (err == nil) != success || success && diagnostic.Len() != 0 || !success && (diagnostic.Len() == 0 || output.Len() != 0) || strings.Contains(diagnostic.String(), "PRIVATE_SENTINEL") {
			t.Fatalf("draft CLI: err=%v stdout=%s stderr=%s", err, &output, &diagnostic)
		}
		return output.Bytes()
	}
	readDraft := func(directory string) draftfile.File {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(directory, "draft.json")) // #nosec G304 -- owned export directory and fixed filename.
		if err != nil {
			t.Fatal(err)
		}
		file, err := draftfile.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(directory, "draft.json"))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("draft content is not private", err)
		}
		marker, err := os.ReadFile(filepath.Join(directory, "export.json")) // #nosec G304 -- owned directory and fixed completion marker.
		var manifest exportManifest
		if err != nil || json.Unmarshal(marker, &manifest) != nil || manifest.File == nil || manifest.File.Digest != exportChecksum(raw) || manifest.Recorded != (exportDigest{}) || manifest.ContentSetDigest == "" {
			t.Fatal("draft completion marker has invalid checksum scopes", err)
		}
		return file
	}
	seen := map[catalog.Kind]bool{}
	var selected catalog.CatalogRecord
	// The upstream archive now returns different bytes. Export must use the
	// retained URL/checksum and must not try to refresh the original source.
	fault.Store(2)
	before := writes.Load()
	for _, item := range seed.Items {
		if item.ReleaseID == "" || seen[item.Kind] {
			continue
		}
		seen[item.Kind] = true
		reply, err := requestRemote(ctx, remoteOptions{baseURL: server.URL, tokenFile: readerToken, timeout: 20 * time.Second, maxResponseBytes: remoteMaxBody, path: "/api/v1/catalog/resources/" + item.ReleaseID})
		var record catalog.CatalogRecord
		if err != nil || json.Unmarshal(reply.Data, &record) != nil {
			t.Fatal("exact governed record missing", err)
		}
		directory := filepath.Join(canonicalTempDir(t), "draft")
		result := run(true, readerToken, "--format", "draft", "--release-id", item.ReleaseID, "--output-dir", directory)
		var output exportResult
		file := readDraft(directory)
		preview := exec.CommandContext(ctx, binary, "import", "preview", "--file", filepath.Join(directory, "draft.json"), "--namespace-id", record.Resource.NamespaceID, "--content-only") // #nosec G204,G702 -- owned executable and exported fixture input; preview is offline.
		preview.Env = catalogctlProcessEnvironment(t)
		previewOutput, err := preview.Output()
		var admission importBatchPreview
		if err != nil || json.Unmarshal(previewOutput, &admission) != nil || !admission.LocallyValid {
			t.Fatalf("exported %s cannot enter normal offline import: %v %s", item.Kind, err, previewOutput)
		}
		if json.Unmarshal(result, &output) != nil || output.Disposition != "reusable_draft" || output.VersionRevisionID != record.VersionRevision.ID || output.BrowserCompatible == nil || file.Kind != record.Resource.Kind || file.CanonicalIdentifier != record.Resource.CanonicalIdentifier || file.Listing == nil || len(file.Representations) != len(record.VersionRevision.Representations) {
			t.Fatal("four-kind draft differs from its selected release")
		}
		for index, representation := range record.VersionRevision.Representations {
			input := file.Representations[index].Input
			if representation.Plugin != nil {
				if input.Mode != "plugin-archive" || input.ExpectedDigest == nil || *input.ExpectedDigest != representation.ContentDigest || input.URL == nil || *input.URL != representation.URL {
					t.Fatal("Plugin export lost its retained archive constraint")
				}
			} else if representation.ArtifactID != "" {
				actor, err := runtime.Authenticator.Authenticate(ctx, "local-reader")
				if err != nil {
					t.Fatal(err)
				}
				_, raw, err := runtime.Catalog.GetArtifact(identity.WithActor(ctx, actor), representation.ArtifactID)
				if err != nil || input.Text == nil || *input.Text != string(raw) {
					t.Fatal("stored document export changed bytes", err)
				}
			}
		}
		if item.Kind == catalog.KindAgent {
			selected = record
		}
	}
	if len(seen) != 4 || writes.Load() != before || selected.Resource.ID == "" {
		t.Fatal("four-kind read-only export qualification incomplete")
	}

	var preview bytes.Buffer
	previewDir := filepath.Join(canonicalTempDir(t), "preview")
	if err := runExport(ctx, []string{"preview", "--query", "browse", "--output-dir", previewDir, "--base-url", server.URL, "--token-file", readerToken}, remoteTestEnvironment("", ""), &preview); err != nil {
		t.Fatal(err)
	}
	batchDir := filepath.Join(canonicalTempDir(t), "batch")
	var batch exportBatchReport
	raw := run(true, readerToken, "batch", "--format", "draft", "--selection-file", filepath.Join(previewDir, "selection.json"), "--output-dir", batchDir)
	if json.Unmarshal(raw, &batch) != nil || !batch.Complete || len(batch.Items) == 0 {
		t.Fatal("draft batch did not complete")
	}
	for _, item := range batch.Items {
		if item.Outcome != "complete" || item.BrowserCompatible == nil || item.Disposition != "reusable_draft" {
			t.Fatal("draft batch lost a per-release outcome")
		}
		readDraft(filepath.Join(batchDir, item.Selection.ReleaseID))
	}

	// A readable Version with validation findings remains exportable. Native
	// JSON is deliberately unfinished and must survive without normalization.
	const incomplete = "{\"unfinished\":\r\n"
	_, revision, evidence, err := runtime.Catalog.CreateVersion(identity.WithActor(ctx, contributor), selected.Resource.ID, catalog.CreateVersionInput{Version: "draft-export-incomplete", Representations: []catalog.RepresentationInput{
		{Role: catalog.RepresentationPrimary, MediaType: selected.VersionRevision.Representations[0].MediaType, Content: incomplete},
		{Role: catalog.RepresentationMetadata, MediaType: selected.VersionRevision.Representations[0].MediaType, Data: selected.VersionRevision.Representations[0].Data},
	}})
	if err != nil || evidence.State != catalog.EvidenceBlocked {
		t.Fatal("blocked draft fixture did not retain content", err)
	}
	directory := filepath.Join(canonicalTempDir(t), "incomplete")
	run(true, readerToken, "--format", "draft", "--version-revision-id", revision.ID, "--output-dir", directory)
	file := readDraft(directory)
	if file.Listing != nil || len(file.Representations) != 2 || *file.Representations[0].Input.Text != incomplete || file.Representations[1].Role != catalog.RepresentationMetadata {
		t.Fatal("incomplete draft was repaired, omitted or given unrelated Listing metadata")
	}

	// An inspected Skill exports its immutable handoff, while package bytes stay
	// outside the reusable file. No Git or OCI request occurs during export.
	assets, skill := importTestInput(t)
	skill.Resources[0].Kind = catalog.KindSkill
	skill.Resources[0].Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, PackageDirectory: "safe-skill", Handoff: &catalog.SkillHandoff{OCI: &catalog.OCIHandoffReference{Reference: "registry.example.com/safe-skill@sha256:" + strings.Repeat("a", 64)}}}}
	writeImportTestFile(t, assets, "safe-skill/SKILL.md", []byte("---\nname: safe-skill\ndescription: Inspect metadata.\n---\nDo not execute this example.\n"), 0600)
	plan, err := prepareImportTest(t, assets, skill, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	var packaged catalog.CreateVersionInput
	if err := json.Unmarshal(plan.Bodies[1], &packaged); err != nil {
		t.Fatal(err)
	}
	resource, err := runtime.Catalog.CreateResource(identity.WithActor(ctx, contributor), catalog.CreateResourceInput{NamespaceID: selected.Resource.NamespaceID, CanonicalIdentifier: "custom:export-package-only", Kind: catalog.KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	_, skillRevision, _, err := runtime.Catalog.CreateVersion(identity.WithActor(ctx, contributor), resource.ID, packaged)
	if err != nil {
		t.Fatal(err)
	}
	packageDir := filepath.Join(canonicalTempDir(t), "package-only")
	run(true, readerToken, "--format", "draft", "--version-revision-id", skillRevision.ID, "--output-dir", packageDir)
	packageFile := readDraft(packageDir)
	if packageFile.Representations[0].Input.Mode != "skill-package" || packageFile.Representations[0].Input.Handoff == nil || !strings.Contains(*packageFile.Representations[0].Input.Handoff, "registry.example.com/safe-skill@sha256:") {
		t.Fatal("package-only Skill lost its separate package requirement or immutable handoff")
	}
	t.Run("transfer inspected Skill to another Catalog", func(t *testing.T) {
		qualifySkillDraftTransfer(t, binary, server.URL, packageDir, packaged.Representations[0].PackageData, resource, skillRevision)
	})

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
		t.Fatal("reader assignment missing", err)
	}
	if _, err := runtime.Identity.RevokeRole(adminCtx, assignments[0].ID, "Test exact draft permissions", "draft-revoke", assignments[0].Revision); err != nil {
		t.Fatal(err)
	}
	role, err := runtime.Identity.SaveRoleDefinition(adminCtx, "", 0, identity.RoleDefinitionInput{Name: "Exact draft reader", Permissions: []identity.Permission{"resource.read", "resource.read_content"}, Reason: "Test exact reads without history"}, false, "draft-role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Identity.GrantAuthorizationAssignment(adminCtx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: reader.MembershipID, NamespaceID: selected.Resource.NamespaceID, Reason: "Test exact revision export"}, "draft-assignment"); err != nil {
		t.Fatal(err)
	}
	noHistory := filepath.Join(canonicalTempDir(t), "no-history")
	run(true, readerToken, "--format", "draft", "--version-revision-id", selected.VersionRevision.ID, "--listing-revision-id", selected.ListingRevision.ID, "--output-dir", noHistory)
	readDraft(noHistory)
	artifactScope := filepath.Join(canonicalTempDir(t), "scoped-artifact")
	run(true, readerToken, "--format", "draft", "--version-revision-id", revision.ID, "--output-dir", artifactScope)
	readDraft(artifactScope)
	beforeRoleChange := time.Now().UTC()
	if _, err := runtime.Identity.SaveRoleDefinition(adminCtx, role.ID, role.Revision, identity.RoleDefinitionInput{Name: role.Name, Permissions: []identity.Permission{"resource.read"}, Reason: "Revoke content before export"}, false, "draft-role-change"); err != nil {
		afterRoleChange := time.Now().UTC()
		retained, lookupErr := runtime.Identity.GetRoleDefinition(adminCtx, role.ID)
		t.Logf("role update evidence: lookupSucceeded=%t active=%t revisionUnchanged=%t before=%s after=%s retainedUpdate=%s", lookupErr == nil, retained.State == identity.DefinitionActive, retained.Revision == role.Revision, beforeRoleChange.Format(time.RFC3339Nano), afterRoleChange.Format(time.RFC3339Nano), retained.UpdatedAt.Format(time.RFC3339Nano))
		t.Fatal(err)
	}
	other, err := runtime.Authenticator.Authenticate(ctx, "other-tenant-admin")
	if err != nil || other.TenantID == selected.Resource.TenantID {
		t.Fatal("cross-tenant control did not authenticate in another tenant", err)
	}
	for _, token := range []string{readerToken, clientSkillToken(t, "other-tenant-admin")} {
		denied := filepath.Join(canonicalTempDir(t), "denied")
		run(false, token, "--format", "draft", "--version-revision-id", selected.VersionRevision.ID, "--output-dir", denied)
		if _, err := os.Lstat(denied); !os.IsNotExist(err) {
			t.Fatal("denied draft export left content")
		}
	}
	if writes.Load() != before {
		t.Fatal("draft export issued a mutating HTTP request")
	}
}

// qualifySkillDraftTransfer imports the exported handoff and separately supplied
// ZIP into a second Catalog. Each apply or resume starts a fresh CLI process.
func qualifySkillDraftTransfer(t *testing.T, binary, sourceURL, directory string, archive []byte, source catalog.Resource, revision catalog.VersionRevision) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	destination := buildSmokeTestRuntime(t)
	t.Cleanup(destination.Close)
	admin, err := destination.Authenticator.Authenticate(ctx, "local-admin")
	if err != nil {
		t.Fatal(err)
	}
	adminCtx := identity.WithActor(ctx, admin)
	ns, err := destination.Catalog.CreateNamespace(adminCtx, catalog.CreateNamespaceInput{Slug: "transferred", DisplayName: "Transferred Skills"})
	if err != nil {
		t.Fatal(err)
	}
	var calls, writes atomic.Int64
	var lastRevisionRead atomic.Value
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			writes.Add(1)
		} else if strings.HasPrefix(r.URL.Path, "/api/v1/version-revisions/") {
			lastRevisionRead.Store(r.URL.Path)
		}
		destination.HTTP.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(api.Close)
	state := filepath.Join(canonicalTempDir(t), "private", "receipt.json")
	token := clientSkillToken(t, "local-contributor")
	run := func(action, origin, refusal string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, binary, "import", action, // #nosec G204,G702 -- test-built CLI and owned loopback Catalogs.
			"--file", filepath.Join(directory, "draft.json"), "--package-file", "1=safe-skill.zip",
			"--namespace-id", ns.ID, "--state-file", state, "--base-url", origin, "--token-file", token)
		command.Env = catalogctlProcessEnvironment(t)
		var diagnostic bytes.Buffer
		command.Stderr = &diagnostic
		output, err := command.Output()
		if refusal == "" {
			if err != nil || diagnostic.Len() != 0 {
				t.Fatalf("%s: %v %s", action, err, &diagnostic)
			}
		} else if command.ProcessState == nil || command.ProcessState.ExitCode() != 1 || !strings.Contains(diagnostic.String(), refusal) {
			t.Fatalf("expected %s to refuse %q: %v %s", action, refusal, err, &diagnostic)
		}
		return output
	}
	zipPath := writeImportTestFile(t, directory, "safe-skill.zip", archive, 0600)
	var applied importOutcome
	if err := json.Unmarshal(run("apply", api.URL, ""), &applied); err != nil || applied.Disposition != "complete" || len(applied.Items) != 1 || len(applied.Items[0].Steps) != 2 || writes.Load() != 2 {
		t.Fatalf("expected one Resource and one Version at the destination: %v", err)
	}
	receipt := readDemoRecoveryFile(t, state)
	beforeCalls := calls.Load()
	run("apply", api.URL, "apply requires a new state file")
	run("resume", sourceURL, "receipt is not for this reusable file and origin")
	if err := os.Remove(zipPath); err != nil {
		t.Fatal(err)
	}
	if out := run("resume", api.URL, "import assets must exist"); len(out) != 0 {
		t.Fatal("missing package unexpectedly produced an import report")
	}
	if calls.Load() != beforeCalls || !bytes.Equal(receipt, readDemoRecoveryFile(t, state)) {
		t.Fatal("a refused import contacted the destination or changed the receipt")
	}
	writeImportTestFile(t, directory, "safe-skill.zip", archive, 0600)
	lastRevisionRead.Store("")
	var resumed importOutcome
	if err := json.Unmarshal(run("resume", api.URL, ""), &resumed); err != nil || !reflect.DeepEqual(resumed, applied) {
		t.Fatal("restoring the package did not recover the original acknowledgements", err)
	}
	steps := resumed.Items[0].Steps
	if lastRevisionRead.Load() != "/api/v1/version-revisions/"+steps[1].Ack.RevisionID || writes.Load() != 2 || !bytes.Equal(receipt, readDemoRecoveryFile(t, state)) {
		t.Fatal("resume must read the exact destination revision and preserve both records and receipt")
	}
	resource, err := destination.Catalog.GetResource(adminCtx, steps[0].Ack.ID)
	if err != nil || resource.ID == source.ID || resource.CanonicalIdentifier != source.CanonicalIdentifier || resource.NamespaceID != ns.ID {
		t.Fatal("transferred Resource lost its canonical identity or destination Namespace", err)
	}
	got, evidence, _, err := destination.Catalog.GetVersionRevision(adminCtx, steps[1].Ack.RevisionID)
	if err != nil || got.ID == revision.ID || evidence.State != catalog.EvidenceComplete || len(got.Representations) != 1 {
		t.Fatal("expected a new, validated destination revision", err)
	}
	want, retained := revision.Representations[0], got.Representations[0]
	if retained.ContentDigest != want.ContentDigest || !reflect.DeepEqual(retained.Package, want.Package) || !reflect.DeepEqual(retained.Handoff, want.Handoff) {
		t.Fatal("destination Skill archive digest, inspected files or immutable handoff differs from the source")
	}
}
