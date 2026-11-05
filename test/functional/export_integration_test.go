//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/draftfile"
)

// Extend the existing real import/process fixture after its draft-only counts
// have passed. Govern one complete native document; do not simulate SQL JSON
// encoding with a memory store or rewrite immutable retained evidence.
func qualifyProcessExport(ctx context.Context, t *testing.T, f processFixture, owner *pgx.Conn, dsn, address, tenant, namespace, publisher, listing, revision, expectedVersion string) {
	t.Helper()
	var submission catalog.ReviewSubmission
	decodeProcessJSON(t, f.request(ctx, address, "POST", "/api/v1/catalog-listings/"+listing+"/submissions", "local-contributor", `{}`, 201), &submission)
	f.request(ctx, address, "POST", "/api/v1/review-submissions/"+submission.ID+":approve", "local-reviewer", `{"reason":"Review the complete native export control"}`, 200, map[string]string{"If-Match": `"rev-1"`})
	var publication struct {
		Release catalog.ListingRelease `json:"release"`
	}
	decodeProcessJSON(t, f.request(ctx, address, "POST", "/api/v1/catalog-listings/"+listing+":publish", "local-publisher", `{"submissionId":"`+submission.ID+`"}`, 201, map[string]string{"If-Match": `"rev-2"`}), &publication)
	var record catalog.CatalogRecord
	decodeProcessJSON(t, f.request(ctx, address, "GET", "/api/v1/catalog/resources/"+publication.Release.ID, "local-reader", "", 200), &record)
	if len(record.VersionRevision.Representations) != 1 || record.VersionRevision.ID != revision {
		t.Fatal("native export fixture lost its exact revision")
	}
	representation := record.VersionRevision.Representations[0]
	if representation.ArtifactID != "" || len(representation.Data) == 0 {
		t.Fatal("PostgreSQL fixture must exercise inline JSON")
	}
	var numericOK bool
	if err := owner.QueryRow(ctx, `SELECT inline_document->'extensions'->'integer'='9007199254740993'::jsonb AND inline_document->'extensions'->'decimal'='0.123456789012345678901'::jsonb AND inline_document->'extensions'->'lexical'='1000'::jsonb FROM representations WHERE tenant_id=$1 AND version_revision_id=$2`, tenant, revision).Scan(&numericOK); err != nil || !numericOK {
		t.Fatal("native numeric SQL control failed", err)
	}
	dir := t.TempDir()
	preview := filepath.Join(dir, "preview")
	command := f.command(ctx, "catalogctl", "", "export", "preview", "--query", "browse", "--namespace", namespace, "--limit", "1", "--output-dir", preview, "--base-url", "http://"+address)
	command.Env = append(command.Env, "CATALOG_REMOTE_TOKEN=local-reader")
	var selection struct {
		Complete bool
		Items    []struct{ ReleaseID, Version string }
	}
	decodeProcessJSON(t, f.run(ctx, command, 0), &selection)
	if !selection.Complete || len(selection.Items) != 1 || selection.Items[0].ReleaseID != publication.Release.ID || selection.Items[0].Version != expectedVersion {
		t.Fatal("PostgreSQL preview lost exact release/version")
	}
	output := filepath.Join(dir, "batch")
	command = f.command(ctx, "catalogctl", "", "export", "batch", "--selection-file", filepath.Join(preview, "selection.json"), "--output-dir", output, "--base-url", "http://"+address)
	command.Env = append(command.Env, "CATALOG_REMOTE_TOKEN=local-reader")
	var report struct {
		Complete bool
		Items    []struct{ Outcome string }
	}
	decodeProcessJSON(t, f.run(ctx, command, 0), &report)
	if !report.Complete || len(report.Items) != 1 || report.Items[0].Outcome != "complete" {
		t.Fatal("real PostgreSQL batch export incomplete")
	}
	assetDir := filepath.Join(output, publication.Release.ID)
	raw, err := os.ReadFile(filepath.Join(assetDir, "native.json")) // #nosec G304 -- owned temporary output and exact fixture release UUID.
	if err != nil || !bytes.Equal(raw, representation.Data) || !bytes.Contains(raw, []byte("9007199254740993")) || !bytes.Contains(raw, []byte("0.123456789012345678901")) || bytes.Contains(raw, []byte("1e3")) {
		t.Fatal("PostgreSQL inline response bytes/precision changed", err)
	}
	var sidecar struct {
		Recorded, File struct {
			Digest, Scope string
			Size          int64
		}
	}
	saved, err := os.ReadFile(filepath.Join(assetDir, "export.json")) // #nosec G304 -- owned temporary output and fixed sidecar filename.
	if err != nil {
		t.Fatal(err)
	}
	decodeProcessJSON(t, saved, &sidecar)
	if sidecar.Recorded.Digest != representation.ContentDigest || sidecar.Recorded.Size != representation.Size || sidecar.File.Digest != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) || sidecar.File.Size != int64(len(raw)) || sidecar.File.Scope != "record_inline_json" || sidecar.File.Digest == sidecar.Recorded.Digest {
		t.Fatal("inline file checksum confused with the canonical retained digest")
	}
	// Reusable export goes through the compiled CLI and actual PostgreSQL
	// reads. It preserves delivered jsonb text, not original upload notation.
	draftDir := filepath.Join(dir, "reusable")
	command = f.command(ctx, "catalogctl", "", "export", "--format", "draft", "--version-revision-id", revision, "--listing-revision-id", record.ListingRevision.ID, "--output-dir", draftDir, "--base-url", "http://"+address)
	command.Env = append(command.Env, "CATALOG_REMOTE_TOKEN=local-reader")
	var draftResult struct {
		VersionRevisionID string `json:"versionRevisionId"`
		BrowserCompatible bool   `json:"browserCompatible"`
	}
	decodeProcessJSON(t, f.run(ctx, command, 0), &draftResult)
	draftRaw, err := os.ReadFile(filepath.Join(draftDir, "draft.json")) // #nosec G304 -- owned temporary output and fixed filename.
	if err != nil {
		t.Fatal(err)
	}
	draft, err := draftfile.Decode(draftRaw)
	if err != nil || draftResult.VersionRevisionID != revision || !draftResult.BrowserCompatible || draft.Listing == nil || draft.Listing.DisplayName != record.ListingRevision.DisplayName || draft.CanonicalIdentifier != record.Resource.CanonicalIdentifier || draft.Version != expectedVersion {
		t.Fatal("reusable PostgreSQL export changed exact identities or Listing metadata", err)
	}
	var storedText string
	if err := owner.QueryRow(ctx, `SELECT inline_document::text FROM representations WHERE tenant_id=$1 AND version_revision_id=$2`, tenant, revision).Scan(&storedText); err != nil {
		t.Fatal(err)
	}
	// Record JSON embeds RawMessage and compacts whitespace. The reusable
	// file carries the stored text inside a JSON string without that rewrite.
	if len(draft.Representations) != 1 || draft.Representations[0].Input.Text == nil || *draft.Representations[0].Input.Text != storedText {
		t.Fatal("reusable PostgreSQL export changed retained native text")
	}
	// Re-import is explicit draft creation, with a selected destination and a
	// fresh canonical identity. The export sidecar is not an import manifest.
	var destination catalog.Namespace
	decodeProcessJSON(t, f.request(ctx, address, "POST", "/api/v1/namespaces", "local-admin", `{"slug":"export-destination","displayName":"Export destination"}`, 201, map[string]string{"Idempotency-Key": "export-destination"}), &destination)
	manifest := map[string]any{"format": 1, "resources": []any{map[string]any{"key": "export-reimport", "namespaceId": destination.ID, "canonicalIdentifier": "custom:export-reimport", "kind": "agent", "version": "1.0.0", "listing": map[string]any{"publisherId": publisher, "displayName": "Re-imported draft", "description": "Explicit native document re-import", "visibility": "private"}, "representations": []any{map[string]any{"role": "primary", "mediaType": representation.MediaType, "profileId": representation.ProfileID, "dataFile": "native.json"}}}}}
	input := filepath.Join(assetDir, "import.json")
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	var before, after int
	if err := owner.QueryRow(ctx, "SELECT count(*) FROM listing_releases").Scan(&before); err != nil {
		t.Fatal(err)
	}
	command = f.command(ctx, "catalogctl", dsn, "import", "apply", "--file", input, "--state-file", filepath.Join(dir, "reimport", "state.json"), "--base-url", "http://"+address)
	command.Env = append(command.Env, "CATALOG_REMOTE_TOKEN=local-contributor")
	var imported struct {
		Disposition string
		Items       []struct {
			Steps []struct{ Ack struct{ RevisionID string } }
		}
	}
	decodeProcessJSON(t, f.run(ctx, command, 0), &imported)
	if imported.Disposition != "complete" || len(imported.Items) != 1 || len(imported.Items[0].Steps) != 4 {
		t.Fatal("complete native JSON did not re-import as a draft")
	}
	var equal bool
	if err := owner.QueryRow(ctx, `SELECT a.inline_document=b.inline_document FROM representations a JOIN representations b ON b.tenant_id=a.tenant_id WHERE a.tenant_id=$1 AND a.version_revision_id=$2 AND b.version_revision_id=$3`, tenant, revision, imported.Items[0].Steps[1].Ack.RevisionID).Scan(&equal); err != nil || !equal {
		t.Fatal("re-import lost native JSON values", err)
	}
	if err := owner.QueryRow(ctx, "SELECT count(*) FROM listing_releases").Scan(&after); err != nil || before != after {
		t.Fatal("re-import bypassed governance", err)
	}
	t.Log("native export: PostgreSQL inline precision, separate byte digests, frozen preview, batch and draft-only re-import passed")
}
