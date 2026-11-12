//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/httpserver"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
)

// The acquisition double supplies one fixed native document during admission.
// This qualifies CLI access and artifact audit, not the outbound transport.
type mcpExportAcquisition struct {
	body  []byte
	calls atomic.Int64
}

func (*mcpExportAcquisition) PolicyDigest() string { return "sha256:" + strings.Repeat("c", 64) }

func (f *mcpExportAcquisition) FetchExternalRepresentation(_ context.Context, r catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	if r.URL != "https://metadata.fixture.invalid/card.json" || r.TenantID != demoTenant || r.MediaType != adapter.MediaMCPServerCard || r.MaxResponseBytes < int64(len(f.body)) {
		return catalog.ExternalRepresentationFetchResult{}, errors.New("unexpected acquisition request")
	}
	f.calls.Add(1)
	return catalog.ExternalRepresentationFetchResult{RequestedURL: r.URL, FinalURL: r.URL, Status: http.StatusOK, MediaType: r.MediaType, Body: bytes.Clone(f.body), ContentDigest: exportChecksum(f.body), PolicyID: "mcp-export-fixture", PolicyRevision: 1, PolicyDigest: f.PolicyDigest(), RetrievedAt: time.Now().UTC()}, nil
}

func TestMCPExportCurrentAccessAndAudit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	runtime := buildSmokeTestRuntime(t)
	defer runtime.Close()
	bootstrap := httptest.NewServer(runtime.HTTP.Handler())
	defer bootstrap.Close()
	receipt, err := executeDemo(ctx, bootstrap.URL, filepath.Join(canonicalTempDir(t), "receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := runtime.Authenticator.Authenticate(ctx, "local-admin")
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := runtime.Authenticator.Authenticate(ctx, "local-reviewer")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := runtime.Authenticator.Authenticate(ctx, "local-reader")
	if err != nil {
		t.Fatal(err)
	}
	adminCtx := identity.WithActor(ctx, admin)
	acquisition := &mcpExportAcquisition{}
	service, err := catalog.NewService(catalog.Options{Repository: runtime.Repository, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), ExternalFetcher: acquisition, CursorKey: bytes.Repeat([]byte("c"), 32)})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpserver.New(httpserver.Dependencies{Catalog: service, Identity: runtime.Identity, Federation: runtime.Federation, Jobs: runtime.Jobs, Authenticator: runtime.Authenticator, Idempotency: idempotency.NewMemoryStore(), MaxBodyBytes: 2 << 20, RequestTimeout: 20 * time.Second, CursorKey: bytes.Repeat([]byte("c"), 32)})
	if err != nil {
		t.Fatal(err)
	}
	var mutations atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
		}
		handler.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	resource, err := service.CreateResource(adminCtx, catalog.CreateResourceInput{NamespaceID: receipt.NamespaceID, CanonicalIdentifier: "custom:mcp-client-export", Kind: catalog.KindMCPServer})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := service.CreateListing(adminCtx, catalog.CreateListingInput{NamespaceID: receipt.NamespaceID, ResourceID: resource.ID})
	if err != nil {
		t.Fatal(err)
	}
	fixture := mcpExportFixtures(t)[2]
	publish := func(version string, artifact bool) (catalog.ListingRelease, catalog.Representation) {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal(fixture.Document, &doc); err != nil {
			t.Fatal(err)
		}
		doc["version"] = version
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		input := catalog.RepresentationInput{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaMCPServerCard, Data: raw}
		if artifact {
			acquisition.body = raw
			input.Data = nil
			input.URL = "https://metadata.fixture.invalid/card.json"
		}
		_, revision, evidence, err := service.CreateVersion(adminCtx, resource.ID, catalog.CreateVersionInput{Version: version, Representations: []catalog.RepresentationInput{input}})
		if err != nil || evidence.State != catalog.EvidenceComplete {
			t.Fatalf("native admission: %v %+v", err, evidence)
		}
		listing, _, err = service.CreateListingRevision(adminCtx, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: revision.ID, DisplayName: "MCP export access", Description: "Local authorization fixture.", PublisherID: receipt.PublisherID, Visibility: catalog.VisibilityTenant}, listing.Revision)
		if err != nil {
			t.Fatal(err)
		}
		submission, err := service.Submit(adminCtx, listing.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := service.DecideSubmission(identity.WithActor(ctx, reviewer), submission.ID, "approve", "Independent fixture review", submission.Revision); err != nil {
			t.Fatal(err)
		}
		var release catalog.ListingRelease
		listing, release, _, err = service.Publish(adminCtx, listing.ID, catalog.PublishInput{SubmissionID: submission.ID, PriorDisposition: catalog.ReleaseSuperseded, PriorReason: "Select the new fixture version"}, listing.Revision)
		if err != nil {
			t.Fatal(err)
		}
		rep := revision.Representations[0]
		if (rep.ArtifactID != "") != artifact {
			t.Fatal("wrong retained delivery form")
		}
		return release, rep
	}
	run := func(releaseID, format string, wantSuccess bool, wantCode string) exportManifest {
		t.Helper()
		directory := filepath.Join(canonicalTempDir(t), "connection")
		var output bytes.Buffer
		before := acquisition.calls.Load()
		err := runExport(ctx, []string{"--release-id", releaseID, "--format", format, "--output-dir", directory}, remoteTestEnvironment(server.URL, "local-reader"), &output)
		if (err == nil) != wantSuccess {
			t.Fatalf("export success=%v: %v", wantSuccess, err)
		}
		if mutations.Load() != 0 || acquisition.calls.Load() != before {
			t.Fatal("export made a mutation or reacquired native metadata")
		}
		if !wantSuccess {
			var apiError remoteAPIError
			if !errors.As(err, &apiError) || apiError.Code != wantCode {
				t.Fatalf("wrong refusal: %v, want %s", err, wantCode)
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("refusal left output")
			}
			if output.Len() != 0 {
				t.Fatal("refusal wrote a success result")
			}
			return exportManifest{}
		}
		root, err := os.OpenRoot(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		raw, err := root.ReadFile("export.json")
		if err != nil {
			t.Fatal(err)
		}
		var manifest exportManifest
		if json.Unmarshal(raw, &manifest) != nil || manifest.MCP == nil || manifest.File == nil || manifest.ReleaseID != releaseID || manifest.MCP.ClientFormat != format {
			t.Fatalf("invalid completion sidecar: %s", raw)
		}
		content, err := root.ReadFile(mcpExportFilename(format))
		if err != nil || exportChecksum(content) != manifest.File.Digest {
			t.Fatal("wrong client file checksum", err)
		}
		return manifest
	}
	inline, _ := publish("2.0.0", false)
	run(inline.ID, "mcp-vscode", true, "")
	assignments, err := runtime.Identity.ListRoleAssignments(adminCtx, identity.RoleAssignmentListRequest{MembershipID: reader.MembershipID, Limit: 100})
	if err != nil || len(assignments) != 1 {
		t.Fatal("missing initial reader grant", err)
	}
	if _, err := runtime.Identity.RevokeRole(adminCtx, assignments[0].ID, "Use exact metadata scope", "mcp-revoke-reader", assignments[0].Revision); err != nil {
		t.Fatal(err)
	}
	roleInput := identity.RoleDefinitionInput{Name: "MCP metadata reader", Description: "Exact metadata access", Permissions: []identity.Permission{"resource.read"}, Reason: "Separate metadata and artifact authority"}
	role, err := runtime.Identity.SaveRoleDefinition(adminCtx, "", 0, roleInput, false, "mcp-role")
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := runtime.Identity.GrantAuthorizationAssignment(adminCtx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: reader.MembershipID, NamespaceID: receipt.NamespaceID, Reason: "Read only metadata in the fixture namespace"}, "mcp-grant")
	if err != nil {
		t.Fatal(err)
	}
	run(inline.ID, "mcp-cursor", true, "")
	artifact, rep := publish("3.0.0", true)
	auditCount := func() int {
		t.Helper()
		events, err := service.AuditEvents(adminCtx, catalog.AuditEventListRequest{Filters: audit.Query{Action: audit.ActionCatalogDocumentRead, TargetType: "catalog_document", TargetID: rep.ArtifactID}, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Actor != reader.MembershipID {
				t.Fatalf("wrong artifact-read actor: %+v", event)
			}
		}
		return len(events)
	}
	if auditCount() != 0 {
		t.Fatal("fixture setup read the exported artifact")
	}
	run(inline.ID, "mcp-vscode", false, "NOT_FOUND")
	// Scoped object denial hides existence; a metadata grant is not content authority.
	run(artifact.ID, "mcp-vscode", false, "NOT_FOUND")
	if auditCount() != 0 {
		t.Fatal("denied content read appended successful-read evidence")
	}
	roleInput.Permissions = append(roleInput.Permissions, "resource.read_content")
	if _, err := runtime.Identity.SaveRoleDefinition(adminCtx, role.ID, role.Revision, roleInput, false, "mcp-content-role"); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"mcp-vscode", "mcp-claude-code", "mcp-cursor"} {
		manifest := run(artifact.ID, format, true, "")
		if manifest.Recorded.Digest != rep.ContentDigest || manifest.Recorded.Size != rep.Size || manifest.RepresentationID != rep.ID {
			t.Fatal("artifact source binding changed")
		}
	}
	if auditCount() != 3 {
		t.Fatal("each successful artifact export must append one read record")
	}
	if _, err := service.TransitionRelease(adminCtx, artifact.ID, catalog.ReleaseDeprecated, "Retain readable metadata", "", nil, artifact.Revision); err != nil {
		t.Fatal(err)
	}
	run(artifact.ID, "mcp-vscode", true, "")
	if auditCount() != 4 {
		t.Fatal("deprecated export lost its read audit")
	}
	if _, err := runtime.Identity.RevokeAuthorizationAssignment(adminCtx, assignment.ID, assignment.Revision, "Revoke fixture access", "mcp-revoke-all"); err != nil {
		t.Fatal(err)
	}
	// With no resource-read grants, collection admission refuses the action itself.
	run(artifact.ID, "mcp-cursor", false, "FORBIDDEN")
	if auditCount() != 4 {
		t.Fatal("revoked export disclosed artifact content")
	}
}
