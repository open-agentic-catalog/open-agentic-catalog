// History projections use bounded repository pages and exact retained evidence.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type historyProjectionRepository struct {
	catalog.Repository
	parent      func(context.Context, string, string, string) error
	page        func(context.Context, string, string, string, catalog.HistoryPageRequest) ([]string, error)
	submissions func(context.Context, string, catalog.SubmissionListRequest) ([]catalog.ReviewSubmission, error)
}

func (r historyProjectionRepository) GetResource(ctx context.Context, tenant, key string) (catalog.Resource, error) {
	err := r.parent(ctx, "GetResource", tenant, key)
	return catalog.Resource{ID: key, TenantID: tenant}, err
}
func (r historyProjectionRepository) GetVersion(ctx context.Context, tenant, key string) (catalog.ResourceVersion, error) {
	err := r.parent(ctx, "GetVersion", tenant, key)
	return catalog.ResourceVersion{ID: key, TenantID: tenant, ResourceID: projectionResource}, err
}
func (r historyProjectionRepository) GetListing(ctx context.Context, tenant, key string) (catalog.CatalogListing, error) {
	err := r.parent(ctx, "GetListing", tenant, key)
	return catalog.CatalogListing{ID: key, TenantID: tenant}, err
}
func (r historyProjectionRepository) ListVersions(ctx context.Context, tenant, parent string, request catalog.HistoryPageRequest) ([]catalog.ResourceVersion, error) {
	ids, err := r.page(ctx, "ListVersions", tenant, parent, request)
	items := make([]catalog.ResourceVersion, 0, len(ids))
	for _, key := range ids {
		items = append(items, catalog.ResourceVersion{ID: key, TenantID: tenant, ResourceID: parent, Version: "1.0.0", Revision: 1, CreatedAt: projectionTime, UpdatedAt: projectionTime})
	}
	return items, err
}
func (r historyProjectionRepository) ListVersionRevisions(ctx context.Context, tenant, parent string, request catalog.HistoryPageRequest) ([]catalog.RevisionSummary, error) {
	ids, err := r.page(ctx, "ListVersionRevisions", tenant, parent, request)
	items := make([]catalog.RevisionSummary, 0, len(ids))
	for _, key := range ids {
		items = append(items, catalog.RevisionSummary{ID: key, RevisionNumber: 1, Digest: "sha256:" + strings.Repeat("a", 64), CreatedBy: "projection-user", CreatedAt: projectionTime})
	}
	return items, err
}
func (r historyProjectionRepository) ListListings(ctx context.Context, tenant, parent string, request catalog.HistoryPageRequest) ([]catalog.CatalogListing, error) {
	ids, err := r.page(ctx, "ListListings", tenant, parent, request)
	items := make([]catalog.CatalogListing, 0, len(ids))
	for _, key := range ids {
		items = append(items, catalog.CatalogListing{ID: key, TenantID: tenant, NamespaceID: projectionResource, ResourceID: parent, Revision: 1, CreatedAt: projectionTime, UpdatedAt: projectionTime})
	}
	return items, err
}
func (r historyProjectionRepository) ListListingRevisions(ctx context.Context, tenant, parent string, request catalog.HistoryPageRequest) ([]catalog.RevisionSummary, error) {
	ids, err := r.page(ctx, "ListListingRevisions", tenant, parent, request)
	items := make([]catalog.RevisionSummary, 0, len(ids))
	for _, key := range ids {
		items = append(items, catalog.RevisionSummary{ID: key, RevisionNumber: 1, Digest: "sha256:" + strings.Repeat("a", 64), CreatedBy: "projection-user", CreatedAt: projectionTime})
	}
	return items, err
}
func (r historyProjectionRepository) ListReleases(ctx context.Context, tenant, parent string, request catalog.HistoryPageRequest) ([]catalog.ListingRelease, error) {
	ids, err := r.page(ctx, "ListReleases", tenant, parent, request)
	items := make([]catalog.ListingRelease, 0, len(ids))
	for _, key := range ids {
		items = append(items, catalog.ListingRelease{ID: key, TenantID: tenant, ListingID: parent, SubmissionID: projectionResource, VersionRevisionID: projectionResource, ListingRevisionID: projectionResource, State: catalog.ReleasePublished, ActivatedBy: "projection-user", Revision: 1, ActivatedAt: projectionTime, UpdatedAt: projectionTime})
	}
	return items, err
}
func (r historyProjectionRepository) ListSubmissions(ctx context.Context, tenant string, request catalog.SubmissionListRequest) ([]catalog.ReviewSubmission, error) {
	return r.submissions(ctx, tenant, request)
}

func TestHistoryPageProjection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind, parentOperation, pageOperation string
	}{
		{"versions", "GetResource", "ListVersions"},
		{"version-revisions", "GetVersion", "ListVersionRevisions"},
		{"listings", "GetResource", "ListListings"},
		{"listing-revisions", "GetListing", "ListListingRevisions"},
		{"releases", "GetListing", "ListReleases"},
		{"submissions", "GetListing", "ListSubmissions"},
	} {
		kind := tc.kind
		for _, outcome := range []string{"pages", "empty", "parent-missing", "store-unavailable"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				actor := projectionActor(identity.RoleReader)
				ctx := identityfixture.WithActor(t.Context(), actor)
				parents, reads := 0, 0
				check := func(c context.Context, tenant, parent string) {
					if !sameProjectionContext(c, ctx) || tenant != actor.TenantID || parent != projectionResource {
						t.Fatal("history read lost actor, tenant or parent binding")
					}
				}
				read := func(c context.Context, operation, tenant, parent string, page catalog.HistoryPageRequest) ([]string, error) {
					if operation != tc.pageOperation {
						t.Fatalf("history page operation = %q, want %q", operation, tc.pageOperation)
					}
					check(c, tenant, parent)
					reads++
					expectedParents := reads
					if kind == "version-revisions" {
						expectedParents *= 2
					}
					if parents != expectedParents || page.Limit != 2 {
						t.Fatalf("history page not authorized and bounded: parents=%d reads=%d request=%+v", parents, reads, page)
					}
					if outcome == "store-unavailable" {
						return nil, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
					}
					if outcome == "empty" {
						return nil, nil
					}
					switch reads {
					case 1:
						if page.AfterID != "" {
							t.Fatal("unexpected initial position")
						}
						return []string{projectionItem, projectionNext}, nil
					case 2:
						if page.AfterID != projectionItem {
							t.Fatalf("continuation after=%q", page.AfterID)
						}
						return []string{projectionNext}, nil
					default:
						t.Fatal("unexpected page read")
						return nil, nil
					}
				}
				repo := historyProjectionRepository{
					parent: func(c context.Context, operation, tenant, key string) error {
						if operation != tc.parentOperation && !(kind == "version-revisions" && operation == "GetResource") {
							t.Fatalf("history parent operation = %q, want %q", operation, tc.parentOperation)
						}
						check(c, tenant, key)
						parents++
						if outcome == "parent-missing" {
							return catalog.E(catalog.CodeNotFound, "missing history parent")
						}
						return nil
					}, page: read,
					submissions: func(c context.Context, tenant string, request catalog.SubmissionListRequest) ([]catalog.ReviewSubmission, error) {
						if request.State != catalog.SubmissionApproved || request.ListingID != projectionResource {
							t.Fatalf("submission filters=%+v", request)
						}
						ids, err := read(c, "ListSubmissions", tenant, request.ListingID, request.HistoryPageRequest)
						items := make([]catalog.ReviewSubmission, 0, len(ids))
						for _, key := range ids {
							items = append(items, catalog.ReviewSubmission{ID: key, TenantID: tenant, ListingID: projectionResource, VersionRevisionID: projectionResource, ListingRevisionID: projectionResource, EvidenceSetID: projectionResource, EvidenceDigest: "sha256:" + strings.Repeat("a", 64), PolicyVersion: "fixture-policy", State: catalog.SubmissionApproved, Revision: 1, SubmittedBy: actor.MembershipID, SubmittedAt: projectionTime, DecidedAt: &projectionTime})
						}
						return items, err
					},
				}
				s := projectionServer(projectionCatalog(t, repo, nil))
				handler, pathKey := s.handleVersionsList, "resourceId"
				query := url.Values{"limit": {"1"}}
				switch kind {
				case "version-revisions":
					handler, pathKey = s.handleVersionRevisionsList, "versionId"
				case "listings":
					handler = s.handleListingsList
				case "listing-revisions":
					handler, pathKey = s.handleListingRevisionsList, "listingId"
				case "releases":
					handler, pathKey = s.handleReleasesList, "listingId"
				case "submissions":
					handler, pathKey = s.handleSubmissionsList, ""
					query.Set("listingId", projectionResource)
					query.Set("state", "approved")
				}
				invoke := func() *httptest.ResponseRecorder {
					r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/history?"+query.Encode(), nil)
					if pathKey != "" {
						r.SetPathValue(pathKey, projectionResource)
					}
					w := httptest.NewRecorder()
					handler(w, r)
					return w
				}
				w := invoke()
				switch outcome {
				case "parent-missing":
					requireProjectionProblem(t, w, 404, catalog.CodeNotFound)
					if reads != 0 {
						t.Fatal("read after missing parent")
					}
					return
				case "store-unavailable":
					requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
					return
				}
				var page cursorPage[json.RawMessage]
				decode := func(w *httptest.ResponseRecorder) {
					t.Helper()
					page = cursorPage[json.RawMessage]{}
					if w.Code != 200 || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
						t.Fatalf("history response=%d %v %s", w.Code, w.Header(), w.Body)
					}
					if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
						t.Fatal(err)
					}
					if page.Items == nil {
						t.Fatal("items must be an array")
					}
				}
				checkItem := func(want string) {
					t.Helper()
					if len(page.Items) != 1 {
						t.Fatalf("page=%s", w.Body)
					}
					var item map[string]json.RawMessage
					if err := json.Unmarshal(page.Items[0], &item); err != nil {
						t.Fatal(err)
					}
					if string(item["id"]) != `"`+want+`"` {
						t.Fatalf("wrong history item=%s", page.Items[0])
					}
					if kind == "version-revisions" || kind == "listing-revisions" {
						if string(item["digest"]) != `"sha256:`+strings.Repeat("a", 64)+`"` || string(item["revisionNumber"]) != "1" || string(item["createdBy"]) != `"projection-user"` {
							t.Fatalf("revision summary=%s", page.Items[0])
						}
						for _, field := range []string{"representations", "evidence", "document", "content"} {
							if _, ok := item[field]; ok {
								t.Fatalf("summary exposed %s", field)
							}
						}
					}
					if kind == "submissions" && (string(item["listingId"]) != `"`+projectionResource+`"` || string(item["state"]) != `"approved"`) {
						t.Fatalf("submission=%s", page.Items[0])
					}
				}
				decode(w)
				if outcome == "empty" {
					if len(page.Items) != 0 || page.NextCursor != "" {
						t.Fatalf("empty page=%s", w.Body)
					}
					return
				}
				checkItem(projectionItem)
				if page.NextCursor == "" {
					t.Fatal("missing continuation")
				}
				query.Set("cursor", page.NextCursor)
				w = invoke()
				decode(w)
				checkItem(projectionNext)
				if page.NextCursor != "" || reads != 2 || parents != func() int {
					if kind == "version-revisions" {
						return 4
					}
					return 2
				}() {
					t.Fatal("terminal page or call count")
				}
			})
		}
	}
}

type historyEvidenceRepository struct {
	catalog.Repository
	submission  func(context.Context, string, string) (catalog.ReviewSubmission, error)
	revision    func(context.Context, string, string) (catalog.VersionRevision, catalog.EvidenceSet, error)
	appendAudit func(context.Context, audit.Record) error
	evidence    func(context.Context, string, string, string) (catalog.EvidenceSet, error)
}

func (r historyEvidenceRepository) GetVersion(c context.Context, tenant, key string) (catalog.ResourceVersion, error) {
	return catalog.ResourceVersion{ID: key, TenantID: tenant, ResourceID: projectionResource}, nil
}
func (r historyEvidenceRepository) GetResource(c context.Context, tenant, key string) (catalog.Resource, error) {
	return catalog.Resource{ID: key, TenantID: tenant, NamespaceID: projectionTenant}, nil
}
func (r historyEvidenceRepository) GetSubmission(c context.Context, tenant, key string) (catalog.ReviewSubmission, error) {
	return r.submission(c, tenant, key)
}
func (r historyEvidenceRepository) GetVersionRevision(c context.Context, tenant, key string) (catalog.VersionRevision, catalog.EvidenceSet, error) {
	return r.revision(c, tenant, key)
}
func (r historyEvidenceRepository) AppendAuditEvent(c context.Context, record audit.Record) error {
	return r.appendAudit(c, record)
}
func (r historyEvidenceRepository) GetValidationEvidence(c context.Context, tenant, revision, key string) (catalog.EvidenceSet, error) {
	return r.evidence(c, tenant, revision, key)
}

func TestSubmissionEvidenceProjection(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"retained", "missing", "audit-unavailable", "binding-conflict"} {
		t.Run(outcome, func(t *testing.T) {
			actor := projectionActor(identity.RoleReader)
			ctx := identityfixture.WithActor(t.Context(), actor)
			bound := catalog.EvidenceSet{ID: projectionResource, TenantID: projectionTenant, Digest: "sha256:" + strings.Repeat("a", 64), SubjectDigest: "sha256:" + strings.Repeat("b", 64), State: catalog.EvidenceComplete, ValidationProfile: "fixture-profile", Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}, PolicyVersion: "fixture-policy", LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, CreatedAt: projectionTime}
			var calls []string
			check := func(c context.Context, tenant string) {
				if !sameProjectionContext(c, ctx) || tenant != actor.TenantID {
					t.Fatal("evidence read binding")
				}
			}
			repo := historyEvidenceRepository{
				submission: func(c context.Context, tenant, key string) (catalog.ReviewSubmission, error) {
					check(c, tenant)
					calls = append(calls, "submission")
					if key != projectionItem {
						t.Fatal("submission id")
					}
					if outcome == "missing" {
						return catalog.ReviewSubmission{}, catalog.E(catalog.CodeNotFound, "missing submission")
					}
					return catalog.ReviewSubmission{ID: key, TenantID: tenant, VersionRevisionID: projectionNext, EvidenceSetID: bound.ID, EvidenceDigest: bound.Digest, PolicyVersion: bound.PolicyVersion}, nil
				},
				revision: func(c context.Context, tenant, key string) (catalog.VersionRevision, catalog.EvidenceSet, error) {
					check(c, tenant)
					calls = append(calls, "revision")
					if key != projectionNext {
						t.Fatal("revision id")
					}
					latest := bound
					latest.ID = projectionTenant
					latest.Digest = "sha256:" + strings.Repeat("c", 64)
					return catalog.VersionRevision{ID: key, TenantID: tenant, ContentSetDigest: bound.SubjectDigest}, latest, nil
				},
				appendAudit: func(c context.Context, record audit.Record) error {
					check(c, record.TenantID)
					calls = append(calls, "audit")
					if err := audit.ValidateNewRecord(record); err != nil {
						t.Fatal(err)
					}
					if record.Action != audit.ActionVersionRevisionRead || record.TargetID != projectionNext || record.Actor != actor.MembershipID {
						t.Fatalf("audit=%+v", record)
					}
					if outcome == "audit-unavailable" {
						return errors.New("private-fixture-audit")
					}
					return nil
				},
				evidence: func(c context.Context, tenant, revision, key string) (catalog.EvidenceSet, error) {
					check(c, tenant)
					calls = append(calls, "evidence")
					if revision != projectionNext || key != bound.ID {
						t.Fatal("evidence lookup binding")
					}
					result := bound
					if outcome == "binding-conflict" {
						result.Digest = "sha256:" + strings.Repeat("b", 64)
					}
					return result, nil
				},
			}
			s := projectionServer(projectionCatalog(t, repo, nil))
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/evidence", nil)
			r.SetPathValue("submissionId", projectionItem)
			w := httptest.NewRecorder()
			s.handleSubmissionEvidenceGet(w, r)
			expectedCalls := []string{"submission", "revision", "audit", "evidence"}
			switch outcome {
			case "missing":
				requireProjectionProblem(t, w, 404, catalog.CodeNotFound)
				expectedCalls = expectedCalls[:1]
			case "audit-unavailable":
				requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
				expectedCalls = expectedCalls[:3]
			case "binding-conflict":
				requireProjectionProblem(t, w, 409, catalog.CodeConflict)
			default:
				var got catalog.EvidenceSet
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if w.Code != 200 || !reflect.DeepEqual(got, bound) {
					t.Fatalf("evidence response=%d %s", w.Code, w.Body)
				}
			}
			if !reflect.DeepEqual(calls, expectedCalls) {
				t.Fatalf("calls=%v want %v", calls, expectedCalls)
			}
		})
	}
}
