//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// Qualify the actual command against admitted, reviewed and published native
// resources. No public service, real credentials or retained Compose data is used.
func TestExportCLIEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	binary := filepath.Join(canonicalTempDir(t), "catalogctl")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, ".") // #nosec G204,G702 -- fixed build into an owned temporary directory.
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", raw, err)
	}
	runtime, server, origin, _, writes := configuredDemoRuntime(t)
	report, err := executeExpandedDemo(ctx, server.URL, filepath.Join(canonicalTempDir(t), "seed.json"), origin, false)
	if err != nil {
		t.Fatal(err)
	}
	readerToken := clientSkillToken(t, "local-reader")
	run := func(release, directory, token string, success bool, extra ...string) exportResult {
		t.Helper()
		args := make([]string, 0, 9+len(extra))
		args = append(args, "export", "--release-id", release, "--output-dir", directory, "--base-url", server.URL, "--token-file", token)
		command := exec.CommandContext(ctx, binary, append(args, extra...)...) // #nosec G204,G702 -- owned executable, local Catalog and synthetic token files.
		command.Env = append(os.Environ(), "CATALOG_CONFIG_FILE=/missing/PRIVATE_SENTINEL", "CATALOG_DATABASE_DSN=postgres://user:PRIVATE_SENTINEL@invalid", "CATALOG_REMOTE_TOKEN=ignored-token", "CATALOG_REMOTE_CA_FILE=")
		var output, diagnostic bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostic
		err := command.Run()
		if (err == nil) != success || success && diagnostic.Len() != 0 || !success && (output.Len() != 0 || diagnostic.Len() == 0) {
			t.Fatalf("export result: %v stdout=%s stderr=%s", err, &output, &diagnostic)
		}
		if strings.Contains(output.String()+diagnostic.String(), "PRIVATE_SENTINEL") {
			t.Fatal("command used or disclosed runtime credentials")
		}
		var result exportResult
		if success {
			if json.Unmarshal(output.Bytes(), &result) != nil || result.Format != 1 || result.ReleaseID != release {
				t.Fatal("invalid stdout result", output.String())
			}
		} else {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatal("failed export must exit 1", err)
			}
		}
		return result
	}
	before := writes.Load()
	seen := map[catalog.Kind]bool{}
	var artifactRecord, inlineRecord catalog.CatalogRecord
	for _, item := range report.Items {
		if item.ReleaseID == "" || seen[item.Kind] {
			continue
		}
		seen[item.Kind] = true
		reply, err := requestRemote(ctx, remoteOptions{baseURL: server.URL, tokenFile: readerToken, timeout: 20 * time.Second, path: "/api/v1/catalog/resources/" + item.ReleaseID})
		if err != nil {
			t.Fatal(err)
		}
		var record catalog.CatalogRecord
		if json.Unmarshal(reply.Data, &record) != nil || len(record.VersionRevision.Representations) != 1 {
			t.Fatal("missing governed fixture")
		}
		rep := record.VersionRevision.Representations[0]
		directory := filepath.Join(canonicalTempDir(t), "export")
		result := run(item.ReleaseID, directory, readerToken, true, "--representation-id", rep.ID)
		root, err := os.OpenRoot(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		raw, err := root.ReadFile("export.json")
		var manifest exportManifest
		if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.Kind != item.Kind || manifest.ReleaseID != item.ReleaseID || manifest.RepresentationID != rep.ID || manifest.VersionRevisionID != item.VersionRevisionID || result.Disposition != manifest.Disposition {
			t.Fatal("sidecar does not bind the exact selected release", err)
		}
		if rep.Plugin != nil {
			var inspection catalog.PluginInspection
			if manifest.File != nil || manifest.Disposition != "reference_only" || manifest.URL != rep.URL || json.Unmarshal(manifest.Plugin, &inspection) != nil || len(inspection.Documents) != len(rep.Plugin.Documents) || inspection.Documents[0] != rep.Plugin.Documents[0] {
				t.Fatal("external archive misrepresented or inspected documents lost")
			}
		} else {
			if manifest.File == nil || manifest.File.Name != "native.json" && manifest.File.Name != "native.bin" {
				t.Fatal("unsafe exported filename")
			}
			content, err := root.ReadFile(manifest.File.Name)
			if err != nil || exportChecksum(content) != manifest.File.Digest || int64(len(content)) != manifest.File.Size {
				t.Fatal("exported bytes do not match their file checksum", err)
			}
			if rep.ArtifactID != "" {
				artifactRecord = record
				reader, err := runtime.Authenticator.Authenticate(ctx, "local-reader")
				if err != nil {
					t.Fatal(err)
				}
				_, retained, err := runtime.Catalog.GetArtifact(identity.WithActor(ctx, reader), rep.ArtifactID)
				if err != nil || !bytes.Equal(content, retained) || manifest.Disposition != "exact_document" || manifest.Recorded.Digest != manifest.File.Digest {
					t.Fatal("retained exact document bytes changed", err)
				}
			} else {
				inlineRecord = record
				if !bytes.Equal(content, rep.Data) || manifest.Disposition != "inline_json" || manifest.File.Scope != "record_inline_json" {
					t.Fatal("inline document changed or presented as retained bytes")
				}
			}
		}

		if item.Kind == catalog.KindMCPServer {
			for _, format := range []string{"mcp-vscode", "mcp-claude-code", "mcp-cursor"} {
				clientDir := filepath.Join(canonicalTempDir(t), "client-config")
				result := run(item.ReleaseID, clientDir, readerToken, true, "--format", format)
				if result.Disposition != "mcp_client_configuration" {
					t.Fatal("missing client configuration result")
				}
				clientRoot, err := os.OpenRoot(clientDir)
				if err != nil {
					t.Fatal(err)
				}
				content, err := clientRoot.ReadFile(mcpExportFilename(format))
				_ = clientRoot.Close()
				if err != nil {
					t.Fatal(err)
				}
				var config map[string]map[string]map[string]string
				if json.Unmarshal(content, &config) != nil {
					t.Fatal("invalid client JSON")
				}
				wrapper := "mcpServers"
				if format == "mcp-vscode" {
					wrapper = "servers"
				}
				entry := config[wrapper]["catalog-"+item.ReleaseID]
				if len(config) != 1 || len(config[wrapper]) != 1 || !strings.HasPrefix(entry["url"], "https://") || format != "mcp-cursor" && entry["type"] != "http" || format == "mcp-cursor" && len(entry) != 1 {
					t.Fatalf("wrong client configuration: %s", content)
				}
				run(item.ReleaseID, clientDir, readerToken, false, "--format", format)
				run(item.ReleaseID, filepath.Join(canonicalTempDir(t), "wrong-tenant-client"), clientSkillToken(t, "other-tenant-admin"), false, "--format", format)
			}
		}
		original := append([]byte{}, raw...)
		run(item.ReleaseID, directory, readerToken, false)
		if current, err := root.ReadFile("export.json"); err != nil || !bytes.Equal(current, original) {
			t.Fatal("repeat export changed existing output", err)
		}
	}
	if len(seen) != 4 || writes.Load() != before || artifactRecord.Release.ID == "" || inlineRecord.Release.ID == "" {
		t.Fatal("four-kind read-only qualification incomplete")
	}
	expectedReleases := 0
	for _, item := range report.Items {
		if item.ReleaseID != "" {
			expectedReleases++
		}
	}
	selectionPath := qualifyExportBatchCLI(t, ctx, binary, server.URL, readerToken, expectedReleases)
	qualifyExportBatchByteBudget(t, ctx, server.URL, readerToken, inlineRecord, artifactRecord)
	qualifyExportBatchSignal(t, ctx, binary)
	if writes.Load() != before {
		t.Fatal("batch export performed mutations")
	}
	// An explicit representation must belong to the exact selected revision.
	denied := filepath.Join(canonicalTempDir(t), "unselected-representation")
	run(artifactRecord.Release.ID, denied, readerToken, false, "--representation-id", exportRepID)
	if _, err := os.Lstat(denied); !os.IsNotExist(err) {
		t.Fatal("representation refusal left output")
	}
	denied = filepath.Join(canonicalTempDir(t), "cross-tenant")
	run(artifactRecord.Release.ID, denied, clientSkillToken(t, "other-tenant-admin"), false)
	if _, err := os.Lstat(denied); !os.IsNotExist(err) {
		t.Fatal("cross-tenant refusal left output")
	}

	// Prove metadata disclosure is not artifact authority. Replace the Reader's
	// broad assignment with a real scoped custom role containing resource.read.
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
		t.Fatal("missing reader assignment", err)
	}
	if _, err := runtime.Identity.RevokeRole(adminCtx, assignments[0].ID, "Test explicit content refusal", "export-revoke", assignments[0].Revision); err != nil {
		t.Fatal(err)
	}
	role, err := runtime.Identity.SaveRoleDefinition(adminCtx, "", 0, identity.RoleDefinitionInput{Name: "Export metadata reader", Description: "Metadata only", Permissions: []identity.Permission{"resource.read"}, Reason: "Test metadata and content separation"}, false, "export-role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Identity.GrantAuthorizationAssignment(adminCtx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: reader.MembershipID, NamespaceID: artifactRecord.Resource.NamespaceID, Reason: "Test namespace metadata only"}, "export-assignment"); err != nil {
		t.Fatal(err)
	}
	before = writes.Load()
	denied = filepath.Join(canonicalTempDir(t), "content-denied")
	run(artifactRecord.Release.ID, denied, readerToken, false)
	if _, err := os.Lstat(denied); !os.IsNotExist(err) {
		t.Fatal("content refusal left output")
	}
	// The record API already discloses inline JSON to this metadata Reader.
	run(inlineRecord.Release.ID, filepath.Join(canonicalTempDir(t), "metadata-json"), readerToken, true)
	if writes.Load() != before {
		t.Fatal("export performed a mutating API request")
	}
	var changedAccess bytes.Buffer
	if err := runExport(ctx, []string{"batch", "--selection-file", selectionPath, "--output-dir", filepath.Join(canonicalTempDir(t), "changed-permissions"), "--base-url", server.URL, "--token-file", readerToken}, remoteTestEnvironment("", ""), &changedAccess); err == nil {
		t.Fatal("old preview preserved revoked content permissions")
	}
	var changedReport exportBatchReport
	if json.Unmarshal(changedAccess.Bytes(), &changedReport) != nil || changedReport.Complete {
		t.Fatal("permission loss hid partial outcomes")
	}
	completed, refused := false, false
	for _, item := range changedReport.Items {
		completed = completed || item.Outcome == "complete" || item.Outcome == "reference_only"
		refused = refused || item.Outcome == "denied"
	}
	if !completed || !refused {
		t.Fatal("expected retained metadata access and revoked artifact access", changedReport)
	}
	// A frozen plan does not confer the permissions held during preview. Actual
	// CLI reads must now report denied/not-found outcomes and retain no content.
	var after bytes.Buffer
	directory := filepath.Join(canonicalTempDir(t), "revoked-batch")
	if err := runExport(ctx, []string{"batch", "--selection-file", selectionPath, "--output-dir", directory, "--base-url", server.URL, "--token-file", clientSkillToken(t, "other-tenant-admin")}, remoteTestEnvironment("", ""), &after); err == nil {
		t.Fatal("cross-tenant batch unexpectedly succeeded")
	}
	var partial exportBatchReport
	if json.Unmarshal(after.Bytes(), &partial) != nil || partial.Complete || len(partial.Items) == 0 {
		t.Fatal("missing batch refusal report")
	}
	for _, item := range partial.Items {
		if item.Outcome != "denied" && item.Code != "not_found" {
			t.Fatal("unexpected cross-tenant outcome", item)
		}
	}
}

func qualifyExportBatchCLI(t *testing.T, ctx context.Context, binary, origin, token string, expected int) string {
	t.Helper()
	run := func(success bool, result any, args ...string) {
		t.Helper()
		command := exec.CommandContext(ctx, binary, append([]string{"export"}, append(args, "--base-url", origin, "--token-file", token)...)...) // #nosec G204,G702 -- owned executable, local API and synthetic credential.
		command.Env = append(os.Environ(), "CATALOG_REMOTE_CA_FILE=", "CATALOG_REMOTE_TOKEN=ignored", "CATALOG_DATABASE_DSN=ignored", "CATALOG_CONFIG_FILE=/missing")
		var out, diagnostic bytes.Buffer
		command.Stdout, command.Stderr = &out, &diagnostic
		err := command.Run()
		if (err == nil) != success || success && diagnostic.Len() != 0 || !success && diagnostic.Len() == 0 || json.Unmarshal(out.Bytes(), result) != nil {
			t.Fatalf("batch CLI: %v stdout=%s stderr=%s", err, &out, &diagnostic)
		}
	}
	var search, browse exportSelection
	directory := filepath.Join(canonicalTempDir(t), "search")
	run(true, &search, "preview", "--query", "search", "--output-dir", directory, "--limit", "1", "--max-pages", "64")
	if !search.Complete || len(search.Items) != expected {
		t.Fatalf("missing Search releases: %d != %d", len(search.Items), expected)
	}
	run(true, &browse, "preview", "--query", "browse", "--output-dir", filepath.Join(canonicalTempDir(t), "browse"), "--limit", "2", "--max-pages", "64")
	searchIDs := map[string]bool{}
	for _, item := range search.Items {
		searchIDs[item.ReleaseID] = true
	}
	if len(browse.Items) != len(search.Items) {
		t.Fatal("Browse lost releases")
	}
	for _, item := range browse.Items {
		if !searchIDs[item.ReleaseID] {
			t.Fatal("Search and Browse selected different exact releases")
		}
	}
	var report exportBatchReport
	output := filepath.Join(canonicalTempDir(t), "batch")
	run(true, &report, "batch", "--selection-file", filepath.Join(directory, "selection.json"), "--output-dir", output)
	if !report.Complete || !report.SelectionComplete || len(report.Items) != expected {
		t.Fatal("batch incomplete", report)
	}
	kinds := map[catalog.Kind]bool{}
	for _, item := range report.Items {
		kinds[item.Selection.Kind] = true
		raw, err := os.ReadFile(filepath.Join(output, item.Selection.ReleaseID, "export.json")) // #nosec G304 -- owned private output, validated release UUID and fixed filename.
		var sidecar exportManifest
		if err != nil || json.Unmarshal(raw, &sidecar) != nil || !item.Selection.matches(sidecar) {
			t.Fatal("wrong per-release completion marker", err)
		}
		if item.Disposition == "reference_only" && (item.Outcome != "reference_only" || sidecar.File != nil) {
			t.Fatal("package reference presented as full native content")
		}
	}
	if len(kinds) != 4 {
		t.Fatal("missing resource kinds")
	}
	// Execute the same real local boundary in-process for source coverage. The
	// compiled invocation above separately owns exit status and stderr checks.
	var inProcess bytes.Buffer
	if err := runExport(ctx, []string{"preview", "--query", "browse", "--limit", "2", "--max-pages", "64", "--output-dir", filepath.Join(canonicalTempDir(t), "covered-preview"), "--base-url", origin, "--token-file", token}, remoteTestEnvironment("", ""), &inProcess); err != nil {
		t.Fatal(err)
	}
	inProcess.Reset()
	coveredDir := filepath.Join(canonicalTempDir(t), "covered-batch")
	if err := runExport(ctx, []string{"batch", "--selection-file", filepath.Join(directory, "selection.json"), "--output-dir", coveredDir, "--base-url", origin, "--token-file", token}, remoteTestEnvironment("", ""), &inProcess); err != nil {
		t.Fatal(err)
	}
	var covered exportBatchReport
	if json.Unmarshal(inProcess.Bytes(), &covered) != nil || !covered.Complete || len(covered.Items) != expected {
		t.Fatal("in-process real batch lost outcomes")
	}
	// A completed batch survives a broken stdout consumer, as single exports do.
	failedOutput := filepath.Join(canonicalTempDir(t), "failed-stdout")
	if err := runExport(ctx, []string{"batch", "--release-id", search.Items[0].ReleaseID, "--output-dir", failedOutput, "--base-url", origin, "--token-file", token}, remoteTestEnvironment("", ""), importShortWriter{}); err == nil {
		t.Fatal("short stdout accepted")
	}
	if _, err := os.Stat(filepath.Join(failedOutput, "batch.json")); err != nil {
		t.Fatal("stdout failure lost the batch report", err)
	}
	// Exact duplicate UUIDs collapse, regardless of spelling. Content digests do
	// not deduplicate different release identities.
	duplicate := search.Items[0].ReleaseID
	run(true, &report, "batch", "--release-id", duplicate, "--release-id", strings.ToUpper(duplicate), "--output-dir", filepath.Join(canonicalTempDir(t), "duplicates"))
	if len(report.Items) != 1 {
		t.Fatal("exact selection not deduplicated")
	}
	var limited exportSelection
	limitedDir := filepath.Join(canonicalTempDir(t), "limited")
	run(false, &limited, "preview", "--query", "search", "--max-records", "1", "--output-dir", limitedDir)
	if limited.Complete || limited.StopReason != "record_limit" || len(limited.Items) != 1 {
		t.Fatal("limited selection misreported")
	}
	run(false, &report, "batch", "--selection-file", filepath.Join(limitedDir, "selection.json"), "--output-dir", filepath.Join(canonicalTempDir(t), "limited-batch"))
	if report.Complete || report.SelectionComplete || report.Items[0].Outcome != "complete" && report.Items[0].Outcome != "reference_only" {
		t.Fatal("incomplete enumeration hidden", report)
	}
	return filepath.Join(directory, "selection.json")
}

func qualifyExportBatchByteBudget(t *testing.T, ctx context.Context, origin, token string, inline, artifact catalog.CatalogRecord) {
	t.Helper()
	// Count the actual successful record bodies, including their JSON newline.
	// The unchanged API fixture is the valid control for the reduced byte budget.
	recordBytes := int64(0)
	for _, id := range []string{inline.Release.ID, artifact.Release.ID} {
		reply, err := requestRemote(ctx, remoteOptions{baseURL: origin, tokenFile: token, timeout: 10 * time.Second, path: "/api/v1/catalog/resources/" + id})
		if err != nil {
			t.Fatal(err)
		}
		recordBytes += int64(len(reply.Data))
	}
	limit := recordBytes + artifact.VersionRevision.Representations[0].Size
	for _, delta := range []int64{0, -1} {
		t.Run("aggregate-artifact-budget/"+strconv.FormatInt(delta, 10), func(t *testing.T) {
			directory := filepath.Join(canonicalTempDir(t), "batch")
			args := []string{"batch", "--release-id", inline.Release.ID, "--release-id", artifact.Release.ID, "--output-dir", directory, "--base-url", origin, "--token-file", token, "--max-total-bytes", strconv.FormatInt(limit+delta, 10)}
			// A later selector must remain unattempted after overflow.
			if delta < 0 {
				args = append(args, "--release-id", exportReleaseID)
			}
			var output bytes.Buffer
			err := runExport(ctx, args, remoteTestEnvironment("", ""), &output)
			var report exportBatchReport
			if (err == nil) != (delta == 0) || json.Unmarshal(output.Bytes(), &report) != nil || report.Complete != (delta == 0) || report.ResponseBytes != limit || len(report.Items) < 2 || report.Items[0].Outcome != "complete" {
				t.Fatal("aggregate response accounting or completion changed", report, err)
			}
			if delta == 0 {
				if len(report.Items) != 2 || report.Items[1].Outcome != "complete" {
					t.Fatal("exact aggregate budget must permit both exports", report)
				}
			} else if len(report.Items) != 3 || report.Items[1].Outcome != "failed" || report.Items[2].Outcome != "not_attempted" || report.Items[2].Code != "budget_exhausted" {
				t.Fatal("overflow did not stop later acquisition", report)
			}
			if _, err := os.Stat(filepath.Join(directory, inline.Release.ID, "export.json")); err != nil {
				t.Fatal("overflow lost earlier completed files", err)
			}
			if _, err := os.Stat(filepath.Join(directory, artifact.Release.ID, "export.json")); (err == nil) != (delta == 0) {
				t.Fatal("unverified artifact has a completion marker", err)
			}
			raw, err := os.ReadFile(filepath.Join(directory, "batch.json")) // #nosec G304 -- invocation-owned temporary directory and fixed report filename.
			var saved exportBatchReport
			if err != nil || json.Unmarshal(raw, &saved) != nil || saved.Complete != report.Complete || saved.ResponseBytes != report.ResponseBytes || len(saved.Items) != len(report.Items) {
				t.Fatal("saved aggregate outcome differs from stdout", err)
			}
		})
	}
}

func qualifyExportBatchSignal(t *testing.T, parent context.Context, binary string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	var calls atomic.Int32
	secondEntered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("unexpected signal fixture request")
		}
		switch n {
		case 1:
			if r.URL.Path != "/api/v1/catalog/resources/"+exportReleaseID {
				t.Error("wrong first release")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(exportTestRecord(exportTestRepresentation(`{"safe":"native"}`)))
		case 2:
			if r.URL.Path != "/api/v1/catalog/resources/"+exportRepID {
				t.Error("wrong second release")
			}
			close(secondEntered)
			<-r.Context().Done()
		default:
			t.Error("signal cancellation started a later request")
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	defer cancel()
	directory := filepath.Join(canonicalTempDir(t), "batch")
	command := exec.CommandContext(ctx, binary, "export", "batch", "--release-id", exportReleaseID, "--release-id", exportRepID, "--release-id", "00000000-0000-7000-8000-000000000099", "--base-url", server.URL, "--token-file", clientSkillToken(t, "fixture-token"), "--output-dir", directory) // #nosec G204,G702 -- owned compiled CLI, loopback fixture and synthetic token.
	command.Env = append(os.Environ(), "CATALOG_REMOTE_CA_FILE=", "CATALOG_REMOTE_TOKEN=ignored", "CATALOG_DATABASE_DSN=ignored", "CATALOG_CONFIG_FILE=/missing")
	var output, diagnostic bytes.Buffer
	command.Stdout, command.Stderr = &output, &diagnostic
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = command.Wait()
		close(done)
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-secondEntered:
	case <-done:
		t.Fatal("CLI exited before cancellation fixture", waitErr, diagnostic.String())
	case <-ctx.Done():
		t.Fatal("CLI never reached the second record")
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		var exit *exec.ExitError
		if !errors.As(waitErr, &exit) || exit.ExitCode() != 1 || diagnostic.Len() == 0 {
			t.Fatal("SIGTERM must produce a classified unsuccessful exit", waitErr, diagnostic.String())
		}
	case <-ctx.Done():
		t.Fatal("CLI did not complete after SIGTERM")
	}
	var report exportBatchReport
	if json.Unmarshal(output.Bytes(), &report) != nil || report.Complete || len(report.Items) != 3 || report.Items[0].Outcome != "complete" || report.Items[1].Outcome != "failed" || report.Items[2].Outcome != "not_attempted" || calls.Load() != 2 {
		t.Fatal("SIGTERM lost progress or started later work", output.String())
	}
	raw, err := os.ReadFile(filepath.Join(directory, "batch.json")) // #nosec G304 -- owned temporary output and fixed report filename.
	var saved exportBatchReport
	if err != nil || json.Unmarshal(raw, &saved) != nil || !bytes.Equal(importTestJSON(t, saved), importTestJSON(t, report)) {
		t.Fatal("SIGTERM did not preserve the final report", err)
	}
	if _, err := os.Stat(filepath.Join(directory, exportReleaseID, "export.json")); err != nil {
		t.Fatal("SIGTERM lost completed files", err)
	}
	if _, err := os.Stat(filepath.Join(directory, exportRepID)); !os.IsNotExist(err) {
		t.Fatal("interrupted release left output", err)
	}
}

func TestExportBatchCancellationRetainsReport(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			cancel()
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(exportTestRecord(exportTestRepresentation(`{"safe":"native"}`)))
	}))
	defer server.Close()
	directory := filepath.Join(canonicalTempDir(t), "batch")
	var output bytes.Buffer
	err := runExport(ctx, []string{"batch", "--release-id", exportReleaseID, "--release-id", exportRepID, "--release-id", "00000000-0000-7000-8000-000000000099", "--output-dir", directory}, remoteTestEnvironment(server.URL, "synthetic"), &output)
	var report exportBatchReport
	if err == nil || json.Unmarshal(output.Bytes(), &report) != nil || report.Complete || len(report.Items) != 3 || report.Items[0].Outcome != "complete" || report.Items[1].Outcome != "failed" || report.Items[2].Outcome != "not_attempted" || calls.Load() != 2 {
		t.Fatal("cancelled batch lost progress/report", err, output.String())
	}
	raw, err := os.ReadFile(filepath.Join(directory, "batch.json")) // #nosec G304 -- owned temporary batch output and fixed report filename.
	if err != nil || json.Unmarshal(raw, &report) != nil || report.Complete {
		t.Fatal("cancelled batch report was not saved", err)
	}
	if _, err := os.Stat(filepath.Join(directory, exportReleaseID, "export.json")); err != nil {
		t.Fatal("cancelled batch deleted completed files", err)
	}
	if _, err := os.Stat(filepath.Join(directory, exportRepID)); !os.IsNotExist(err) {
		t.Fatal("cancelled item left content", err)
	}
}

func TestExportPreviewMalformedCursorLeavesNoSelection(t *testing.T) {
	for _, mode := range []string{"search", "browse"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			page := bytes.Replace(exportTestPage(t, mode, []string{exportReleaseID}, "", false), []byte(`"nextCursor":""`), []byte(`"nextCursor":null`), 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("unexpected preview credentials")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(page)
			}))
			defer server.Close()
			directory := filepath.Join(canonicalTempDir(t), "preview")
			var output bytes.Buffer
			err := runExport(t.Context(), []string{"preview", "--query", mode, "--output-dir", directory}, remoteTestEnvironment(server.URL, "fixture-token"), &output)
			if err == nil || output.Len() != 0 || calls.Load() != 1 {
				t.Fatal("malformed cursor produced a misleading preview", err, output.String(), calls.Load())
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("malformed cursor left a saved selection", err)
			}
		})
	}
}

func TestExportLocalTLSFaultsAndDeadline(t *testing.T) {
	for _, fault := range []string{"success", "stdout failure", "output collision", "missing version", "record redirect", "artifact redirect", "bad digest", "truncated", "deadline"} {
		t.Run(fault, func(t *testing.T) {
			var redirectCalls atomic.Int64
			var artifactCalls atomic.Int64
			destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectCalls.Add(1) }))
			defer destination.Close()
			const content = "# Exact retained document\r\n"
			directory := filepath.Join(canonicalTempDir(t), "export")
			artifactEntered := make(chan struct{})
			rep, headers := exportTestArtifact([]byte(content))
			repJSON, err := json.Marshal(rep)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("unexpected credentials or method")
				}
				if r.URL.Path == "/api/v1/catalog/resources/"+exportReleaseID {
					if fault == "record redirect" {
						http.Redirect(w, r, destination.URL, http.StatusFound)
						return
					}
					if fault == "output collision" {
						// Another caller creates output after our preflight, before
						// our verified download is ready for file publication.
						if err := os.Mkdir(directory, 0700); err != nil {
							t.Error(err)
							http.Error(w, "fixture creation failed", http.StatusInternalServerError)
							return
						}
						if err := os.WriteFile(filepath.Join(directory, "other-export"), []byte("preserve this caller's files"), 0600); err != nil {
							t.Error(err)
							http.Error(w, "fixture creation failed", http.StatusInternalServerError)
							return
						}
					}
					w.Header().Set("Content-Type", "application/json")
					record := exportTestRecord(string(repJSON))
					if fault == "missing version" {
						record = bytes.Replace(record, []byte(`,"version":"1.0.0"`), nil, 1)
					}
					_, _ = w.Write(record)
					return
				}
				if r.URL.Path != "/api/v1/artifacts/"+rep.ArtifactID+"/content" {
					t.Error("unselected request", r.URL.Path)
				}
				artifactCalls.Add(1)
				if fault == "artifact redirect" {
					http.Redirect(w, r, destination.URL, http.StatusFound)
					return
				}
				if fault == "deadline" {
					close(artifactEntered)
					<-r.Context().Done()
					return
				}
				for key, values := range headers {
					w.Header()[key] = values
				}
				if fault == "bad digest" {
					w.Header().Set("Content-Digest", "sha-256=:bad=:")
				}
				if fault == "truncated" {
					_, _ = io.WriteString(w, content[:len(content)-1])
				} else {
					_, _ = io.WriteString(w, content)
				}
			}))
			defer server.Close()
			ca := filepath.Join(canonicalTempDir(t), "root.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--release-id", exportReleaseID, "--output-dir", directory, "--base-url", server.URL, "--ca-file", ca}
			if fault == "deadline" {
				args = append(args, "--timeout", "2s")
			}
			var stdout bytes.Buffer
			var output io.Writer = &stdout
			if fault == "stdout failure" {
				output = importShortWriter{}
			}
			err = runExport(t.Context(), args, remoteTestEnvironment("", "fixture-token"), output)
			switch fault {
			case "output collision":
				if err == nil || stdout.Len() != 0 || !strings.Contains(err.Error(), "cannot save private files") {
					t.Fatal("concurrent output creation was accepted or lost", err)
				}
				entries, readErr := os.ReadDir(directory)
				if readErr != nil || len(entries) != 1 || entries[0].Name() != "other-export" {
					t.Fatal("collision changed another caller's directory", entries, readErr)
				}
				root, openErr := os.OpenRoot(directory)
				if openErr != nil {
					t.Fatal(openErr)
				}
				defer func() { _ = root.Close() }()
				if raw, readErr := root.ReadFile("other-export"); readErr != nil || string(raw) != "preserve this caller's files" {
					t.Fatal("collision changed another caller's file", readErr)
				}
			case "stdout failure":
				if err == nil {
					t.Fatal("short stdout accepted")
				}
				if _, err := os.Stat(filepath.Join(directory, "export.json")); err != nil {
					t.Fatal("stdout failure removed completed export", err)
				}
			case "success":
				root, openErr := os.OpenRoot(directory)
				if openErr != nil {
					t.Fatal(openErr)
				}
				defer func() { _ = root.Close() }()
				if raw, readErr := root.ReadFile("native.bin"); err != nil || readErr != nil || string(raw) != content || stdout.Len() == 0 {
					t.Fatal("verified TLS export failed", err, readErr)
				}
			default:
				if err == nil || stdout.Len() != 0 {
					t.Fatal("failed transfer produced success output", err)
				}
				if _, statErr := os.Lstat(directory); !os.IsNotExist(statErr) {
					t.Fatal("failed transfer left output", statErr)
				}
			}
			if fault == "deadline" {
				select {
				case <-artifactEntered:
				default:
					t.Fatal("deadline control did not reach the artifact read")
				}
			}
			if redirectCalls.Load() != 0 {
				t.Fatal("export followed a redirect")
			}
			wantArtifactCalls := int64(1)
			if fault == "record redirect" || fault == "missing version" {
				wantArtifactCalls = 0
			}
			if artifactCalls.Load() != wantArtifactCalls {
				t.Fatal("record refusal or artifact selection lost", artifactCalls.Load())
			}
		})
	}
}
