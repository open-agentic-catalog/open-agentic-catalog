package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/draftfile"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// Only metadata and content grants exist in this isolated policy fixture.
// Any attempted history admission fails, including an incidental history read.
type draftExportPermissions struct {
	identity.PermissionStore
	deny identity.Permission
}

func (p draftExportPermissions) CheckPermissions(_ context.Context, _ identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	result := identity.PermissionDecision{Revision: 1}
	for _, check := range checks {
		allowed := (check.Permission == "resource.read" || check.Permission == "resource.read_content") && check.Permission != p.deny
		result.Allowed = append(result.Allowed, allowed)
	}
	return result, nil
}

func draftExportRequest(t *testing.T, repo controlReadRepository, query string, deny identity.Permission, scopes bool) *httptest.ResponseRecorder {
	t.Helper()
	actor := projectionActor(identity.RoleReader)
	if scopes {
		actor.Claims = map[string]string{"scope:catalog.read": "true"}
	}
	actor = identity.BindPermissionStore(actor, draftExportPermissions{deny: deny})
	r := httptest.NewRequestWithContext(identity.WithActor(t.Context(), actor), http.MethodGet, "/api/v1/version-revisions/"+projectionItem+"/export"+query, nil)
	r.SetPathValue("revisionId", projectionItem)
	w := httptest.NewRecorder()
	s := projectionServer(projectionCatalog(t, repo, nil))
	handler := s.withQueryParameters(s.withUUIDPathParameters(s.handleDraftExport, "revisionId"), "listingRevisionId")
	authorizationScopes(scopes, handler).ServeHTTP(w, r)
	return w
}

func TestDraftExportRetainedTextAndAudits(t *testing.T) {
	const native = " {\"large\":9007199254740993,\"decimal\":0.123456789012345678901,\"extension\":\"<script>hostile()</script>\"}\r\n"
	for _, stored := range []bool{false, true} {
		t.Run(map[bool]string{false: "inline", true: "artifact"}[stored], func(t *testing.T) {
			repo := controlReadFixture(t)
			rep := &repo.record.VersionRevision.Representations[0]
			rep.Data, rep.ValidationState = json.RawMessage(native), "invalid"
			if stored {
				repo.document.Content = []byte(native)
				repo.document.Size = int64(len(native))
				repo.document.Digest = audit.DigestBytes([]byte(native))
				repo.document.ID = strings.TrimPrefix(repo.document.Digest, "sha256:")
				repo.document.MediaType = rep.MediaType
				rep.Data, rep.ArtifactID = nil, repo.document.ID
				rep.ContentDigest, rep.Size = repo.document.Digest, repo.document.Size
				rep.URL = "https://never-fetch.invalid/changed-card.json"
			}
			repo.read = func(_ context.Context, tenant, key, _ string) error {
				if tenant != projectionTenant {
					t.Fatal("tenant boundary changed")
				}
				return nil
			}
			var actions []string
			repo.appendAudit = func(_ context.Context, record audit.Record) error {
				actions = append(actions, record.Action)
				if err := audit.ValidateNewRecord(record); err != nil {
					t.Fatal(err)
				}
				return nil
			}
			w := draftExportRequest(t, repo, "?listingRevisionId="+projectionItem, "", true)
			file, err := draftfile.Decode(w.Body.Bytes())
			if w.Code != 200 || err != nil || file.Listing == nil || file.CanonicalIdentifier != repo.record.Resource.CanonicalIdentifier || file.Version != "1.0.0" || file.Representations[0].Input.Mode != "data" || *file.Representations[0].Input.Text != native {
				t.Fatalf("export changed retained content: status=%d err=%v", w.Code, err)
			}
			if len(actions) != 1+map[bool]int{false: 0, true: 1}[stored] || actions[0] != audit.ActionVersionRevisionRead || stored && actions[1] != audit.ActionCatalogDocumentRead {
				t.Fatal("private-read audits missing", actions)
			}
			if w.Header().Get("Content-Disposition") != `attachment; filename="open-agentic-catalog-draft.json"` || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Digest") != digestHeader(w.Body.Bytes()) || w.Header().Get("ETag") != "" {
				t.Fatal("attachment headers disagree", w.Header())
			}
			for _, field := range []string{`"tenantId"`, `"revisionId"`, `"publisherId"`, `"validationState"`, `"artifactId"`} {
				if bytes.Contains(w.Body.Bytes(), []byte(field)) {
					t.Fatal("export copied governance or destination fields", field)
				}
			}
		})
	}
}

func TestDraftExportRefusesBeforeAttachment(t *testing.T) {
	for _, failure := range []string{"metadata grant", "content grant", "version audit", "document audit", "missing document", "missing retained content", "Listing parent", "Listing version", "resource parent", "query UUID", "duplicate query", "unknown query", "cross tenant", "file overflow"} {
		t.Run(failure, func(t *testing.T) {
			repo := controlReadFixture(t)
			query, deny, want := "", identity.Permission(""), http.StatusServiceUnavailable
			repo.read = func(_ context.Context, tenant, _, _ string) error {
				if tenant != projectionTenant {
					t.Fatal("tenant changed")
				}
				return nil
			}
			repo.appendAudit = func(_ context.Context, record audit.Record) error {
				if failure == "version audit" || failure == "document audit" && record.Action == audit.ActionCatalogDocumentRead {
					return errors.New("PRIVATE_AUDIT_SENTINEL")
				}
				return nil
			}
			rep := &repo.record.VersionRevision.Representations[0]
			switch failure {
			case "metadata grant":
				deny, want = "resource.read", 403
			case "content grant":
				deny, want = "resource.read_content", 403
			case "document audit", "missing document":
				rep.Data, rep.ArtifactID = nil, repo.document.ID
				rep.ContentDigest, rep.Size, rep.MediaType = repo.document.Digest, repo.document.Size, repo.document.MediaType
				if failure == "missing document" {
					want = 404
					repo.read = func(_ context.Context, _, _, operation string) error {
						if operation == "Document" {
							return catalog.E(catalog.CodeNotFound, "missing retained document")
						}
						return nil
					}
				}
			case "missing retained content":
				rep.Data, rep.URL = nil, "https://never-fetch.invalid/card.json"
			case "Listing parent":
				query, want = "?listingRevisionId="+projectionItem, 400
				repo.record.Listing.ResourceID = projectionNext
			case "Listing version":
				query, want = "?listingRevisionId="+projectionItem, 400
				repo.record.ListingRevision.VersionRevisionID = projectionNext
			case "resource parent":
				repo.record.Resource.ID = projectionNext
			case "query UUID":
				query, want = "?listingRevisionId=", 400
			case "duplicate query":
				query, want = "?listingRevisionId="+projectionItem+"&listingRevisionId="+projectionItem, 400
			case "unknown query":
				query, want = "?resourceId="+projectionItem, 400
			case "cross tenant":
				want = 404
				repo.read = func(context.Context, string, string, string) error {
					return catalog.E(catalog.CodeNotFound, "not found")
				}
			case "file overflow":
				want = 422
				query = "?listingRevisionId=" + projectionItem
				repo.record.ListingRevision.License = strings.Repeat("\x01", 11<<20)
			}
			w := draftExportRequest(t, repo, query, deny, false)
			if w.Code != want || w.Header().Get("Content-Disposition") != "" || w.Header().Get("Content-Digest") != "" || strings.Contains(w.Body.String(), "PRIVATE_AUDIT_SENTINEL") {
				t.Fatalf("failure disclosed attachment or cause: status=%d want=%d body=%s", w.Code, want, w.Body.String())
			}
		})
	}
}

func TestDraftExportMissingOAuthScopeStopsBeforeReads(t *testing.T) {
	repo := controlReadFixture(t)
	repo.read = func(context.Context, string, string, string) error {
		t.Fatal("missing scope reached a private domain read")
		return nil
	}
	s := projectionServer(projectionCatalog(t, repo, nil))
	actor := identity.BindPermissionStore(projectionActor(identity.RoleReader), draftExportPermissions{})
	r := httptest.NewRequestWithContext(identity.WithActor(t.Context(), actor), http.MethodGet, "/api/v1/version-revisions/"+projectionItem+"/export", nil)
	r.SetPathValue("revisionId", projectionItem)
	w := httptest.NewRecorder()
	authorizationScopes(true, http.HandlerFunc(s.handleDraftExport)).ServeHTTP(w, r)
	if w.Code != 403 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "catalog.read") || w.Header().Get("Content-Disposition") != "" {
		t.Fatal("scope denial exposed an attachment", w.Code, w.Header())
	}
}

func TestDraftExportReadAndBudgetFailures(t *testing.T) {
	for _, failure := range []string{"Version", "Resource", "ListingRevision", "Listing", "empty", "too many", "document budget", "document identity", "cancelled", "invalid inline"} {
		t.Run(failure, func(t *testing.T) {
			repo := controlReadFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			actor := identity.BindPermissionStore(projectionActor(identity.RoleReader), draftExportPermissions{})
			ctx = identity.WithActor(ctx, actor)
			repo.appendAudit = func(context.Context, audit.Record) error { return nil }
			repo.read = func(_ context.Context, _, _, operation string) error {
				if operation == failure {
					return catalog.E(catalog.CodeForbidden, "read refused")
				}
				if failure == "cancelled" && operation == "Resource" {
					cancel()
				}
				return nil
			}
			rep := &repo.record.VersionRevision.Representations[0]
			want := catalog.CodeForbidden
			switch failure {
			case "empty":
				repo.record.VersionRevision.Representations = nil
				want = catalog.CodeWorkLimitExceeded
			case "too many":
				repo.record.VersionRevision.Representations = make([]catalog.Representation, 17)
				want = catalog.CodeWorkLimitExceeded
			case "document budget":
				rep.Data = json.RawMessage(`{"text":"` + strings.Repeat("x", 9<<20) + `"}`)
				repo.record.VersionRevision.Representations = []catalog.Representation{*rep, *rep}
				want = catalog.CodeWorkLimitExceeded
			case "document identity":
				rep.ArtifactID, rep.Data = repo.document.ID, nil
				rep.Size, rep.ContentDigest, rep.MediaType = repo.document.Size, repo.document.Digest, "application/different"
				want = catalog.CodeDependencyUnavailable
			case "invalid inline":
				rep.Data = json.RawMessage(`[1,2]`)
				want = catalog.CodeDependencyUnavailable
			}
			s := projectionServer(projectionCatalog(t, repo, nil))
			_, err := s.prepareDraftExport(ctx, projectionItem, projectionItem)
			if failure == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancelled export continued", err)
				}
			} else if catalog.ErrorCodeOf(err) != want {
				t.Fatalf("wrong refusal: %v want %v", err, want)
			}
		})
	}
}

func TestDraftExportPackageReferences(t *testing.T) {
	digest := audit.DigestBytes([]byte("{}"))
	plugin := catalog.Representation{MediaType: catalog.PluginMediaZIP, ProfileID: catalog.PluginProfile, URL: "https://never-fetch.invalid/plugin.zip", Size: 100, ContentDigest: digest,
		Plugin: &catalog.PluginInspection{ExpandedSize: 2, Files: []catalog.PluginFile{{Path: "plugin.json", Size: 2, Digest: digest}}, Documents: []catalog.PluginDocument{{Path: "plugin.json", Content: "{}"}}, Assessments: catalog.NotAssessedSkillPackageAssessments()}}
	skill := catalog.Representation{MediaType: catalog.SkillPackageMediaZIP, Size: 100, ContentDigest: digest, ValidationState: "invalid",
		Package: &catalog.SkillPackageInspection{StructureState: catalog.PackageAssessmentFailed, Assessments: catalog.NotAssessedSkillPackageAssessments()},
		Handoff: &catalog.SkillHandoff{OCI: &catalog.OCIHandoffReference{Reference: "registry.example.com/skill@sha256:" + strings.Repeat("a", 64)}}}
	s := &Server{}
	for _, item := range []struct {
		kind catalog.Kind
		rep  catalog.Representation
		mode string
	}{{catalog.KindPlugin, plugin, "plugin-archive"}, {catalog.KindSkill, skill, "skill-package"}} {
		input, err := s.exportDraftInput(t.Context(), item.kind, item.rep)
		if err != nil || input.Mode != item.mode || input.Text != nil {
			t.Fatal("package reference failed or exported package bytes", input, err)
		}
		if item.kind == catalog.KindPlugin && (input.URL == nil || *input.URL != plugin.URL || input.ExpectedDigest == nil || *input.ExpectedDigest != digest) || item.kind == catalog.KindSkill && (input.Handoff == nil || !strings.Contains(*input.Handoff, "registry.example.com/skill@sha256:")) {
			t.Fatal("immutable package reference changed", input)
		}
		if _, err := s.exportDraftInput(t.Context(), catalog.KindAgent, item.rep); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
			t.Fatal("wrong resource kind accepted a package")
		}
		item.rep.ContentDigest = "broken"
		if _, err := s.exportDraftInput(t.Context(), item.kind, item.rep); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
			t.Fatal("invalid retained package accepted")
		}
	}
	if _, err := s.exportDraftInput(t.Context(), catalog.KindAgent, catalog.Representation{ArtifactID: "never-read", Size: catalog.MaxCatalogDocumentBytes + 1}); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
		t.Fatal("oversized artifact reached a read")
	}
}
