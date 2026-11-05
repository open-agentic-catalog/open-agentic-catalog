// Mutation projections cover single operations against existing repository ports.
package httpserver

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type controlMutationRepository struct {
	controlReadRepository
	createNamespace func(context.Context, catalog.Namespace, catalog.AuditEvent, catalog.OutboxEvent) (catalog.Namespace, error)
	createPublisher func(context.Context, catalog.Publisher, catalog.AuditEvent, catalog.OutboxEvent) (catalog.Publisher, error)
	createResource  func(context.Context, catalog.CreateResourceParams) (catalog.Resource, error)
	createListing   func(context.Context, catalog.CreateListingParams) (catalog.CatalogListing, error)
	updateNamespace func(context.Context, catalog.UpdateNamespaceParams) (catalog.Namespace, error)
	updatePublisher func(context.Context, catalog.UpdatePublisherParams) (catalog.Publisher, error)
	domain          func(context.Context, string, string, string, int64, catalog.AuditEvent, catalog.OutboxEvent) (catalog.Publisher, []string, error)
	deleteResource  func(context.Context, catalog.DeleteResourceParams) (catalog.Resource, error)
	decide          func(context.Context, catalog.DecideSubmissionParams) (catalog.ReviewSubmission, catalog.ResourceVersion, error)
	transition      func(context.Context, catalog.TransitionReleaseParams) (catalog.ListingRelease, error)
}

func (r controlMutationRepository) CreateResource(c context.Context, p catalog.CreateResourceParams) (catalog.Resource, error) {
	return r.createResource(c, p)
}
func (r controlMutationRepository) CreateListing(c context.Context, p catalog.CreateListingParams) (catalog.CatalogListing, error) {
	return r.createListing(c, p)
}
func (r controlMutationRepository) UpdateNamespace(c context.Context, p catalog.UpdateNamespaceParams) (catalog.Namespace, error) {
	return r.updateNamespace(c, p)
}
func (r controlMutationRepository) UpdatePublisher(c context.Context, p catalog.UpdatePublisherParams) (catalog.Publisher, error) {
	return r.updatePublisher(c, p)
}
func (r controlMutationRepository) DeleteResource(c context.Context, p catalog.DeleteResourceParams) (catalog.Resource, error) {
	return r.deleteResource(c, p)
}
func (r controlMutationRepository) GetOriginatingPrincipal(_ context.Context, tenant, kind, target string) (catalog.OriginatingPrincipal, error) {
	if tenant != projectionTenant || target != projectionItem || (kind != "resource" && kind != "review_submission") {
		return catalog.OriginatingPrincipal{}, catalog.E(catalog.CodeForbidden, "unbound fixture origin")
	}
	return catalog.OriginatingPrincipal{TenantID: tenant, MembershipID: "00000000-0000-7000-8000-000000000094", RealmID: "00000000-0000-7000-8000-000000000095", Subject: "other-author"}, nil
}
func (r controlMutationRepository) DecideSubmission(c context.Context, p catalog.DecideSubmissionParams) (catalog.ReviewSubmission, catalog.ResourceVersion, error) {
	return r.decide(c, p)
}
func (r controlMutationRepository) TransitionRelease(c context.Context, p catalog.TransitionReleaseParams) (catalog.ListingRelease, error) {
	return r.transition(c, p)
}
func (r controlMutationRepository) CreateNamespace(c context.Context, p catalog.Namespace, a catalog.AuditEvent, o catalog.OutboxEvent) (catalog.Namespace, error) {
	return r.createNamespace(c, p, a, o)
}
func (r controlMutationRepository) CreatePublisher(c context.Context, p catalog.Publisher, a catalog.AuditEvent, o catalog.OutboxEvent) (catalog.Publisher, error) {
	return r.createPublisher(c, p, a, o)
}
func (r controlMutationRepository) ApprovePublisherPublicationDomain(c context.Context, input catalog.PublisherDomainApproval) (catalog.Publisher, error) {
	p, _, err := r.domain(c, input.TenantID, input.PublisherID, input.Domain, input.ExpectedRevision, input.Audit, input.Outbox)
	return p, err
}
func (r controlMutationRepository) RevokePublisherPublicationDomain(c context.Context, tenant, key, domain string, revision int64, a catalog.AuditEvent, o catalog.OutboxEvent) (catalog.Publisher, []string, error) {
	return r.domain(c, tenant, key, domain, revision, a, o)
}

func TestControlMutationProjection(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"namespace-create", "publisher-create", "resource-create", "listing-create", "namespace-patch", "publisher-patch", "domain-approve", "domain-revoke", "resource-delete", "request-changes", "reject", "deprecate", "retire", "supersede", "revoke"} {
		for _, outcome := range []string{"success", "unavailable", "stale"} {
			if outcome == "stale" && strings.HasSuffix(kind, "-create") {
				continue
			}
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				actor := projectionActor(identity.RoleTenantAdmin)
				ctx := identityfixture.WithActor(t.Context(), actor)
				repo := controlMutationRepository{controlReadRepository: controlReadFixture(t)}
				reads, writes := 0, 0
				repo.read = func(c context.Context, tenant, key, operation string) error {
					reads++
					if !sameProjectionContext(c, ctx) || tenant != projectionTenant {
						t.Fatal("read lost tenant")
					}
					wantKey := projectionItem

					if key != wantKey && !(operation == "Release" && key == projectionNext) {
						t.Fatalf("read %s key=%s", operation, key)
					}
					return nil
				}
				// Static replacement metadata is independent of the selected release.
				action := ""
				var want any
				check := func(c context.Context, a catalog.AuditEvent, o catalog.OutboxEvent) error {
					writes++
					if !sameProjectionContext(c, ctx) || a.TenantID != projectionTenant || a.Actor != actor.MembershipID || a.Action != action || o.TenantID != projectionTenant || o.Subject != a.TargetID {
						t.Fatalf("mutation evidence=%+v outbox=%+v", a, o)
					}
					if err := audit.ValidateNewRecord(a); err != nil {
						t.Fatal(err)
					}
					if !strings.HasSuffix(kind, "-create") && a.Reason != "reviewed change" {
						t.Fatalf("reason=%q", a.Reason)
					}
					if outcome == "unavailable" {
						return catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-write")
					}
					if outcome == "stale" {
						return catalog.E(catalog.CodePreconditionFailed, "revision does not match")
					}
					return nil
				}
				var body, path, location string
				status := 200
				etag := `"rev-4"`
				var selectHandler func(*Server) http.HandlerFunc
				switch kind {
				case "namespace-create":
					action = "namespace.created"
					body = `{"slug":" default ","displayName":" Default ","description":" Description "}`
					location = "/api/v1/namespaces/"
					selectHandler = func(s *Server) http.HandlerFunc { return s.handleNamespacesCreate }
					repo.createNamespace = func(c context.Context, p catalog.Namespace, a catalog.AuditEvent, o catalog.OutboxEvent) (catalog.Namespace, error) {
						if p.TenantID != projectionTenant || p.Slug != "default" || p.DisplayName != "Default" || p.Description != "Description" || p.Revision != 1 || a.TargetID != p.ID {
							t.Fatalf("namespace=%+v", p)
						}
						want = p
						return p, check(c, a, o)
					}
				case "publisher-create":
					action = "publisher.created"
					body = `{"displayName":" Publisher ","domains":["Example.COM."]}`
					location = "/api/v1/publishers/"
					selectHandler = func(s *Server) http.HandlerFunc { return s.handlePublishersCreate }
					repo.createPublisher = func(c context.Context, p catalog.Publisher, a catalog.AuditEvent, o catalog.OutboxEvent) (catalog.Publisher, error) {
						if p.TenantID != projectionTenant || p.DisplayName != "Publisher" || !reflect.DeepEqual(p.Domains, []string{"example.com"}) || len(p.ApprovedPublicationDomains) != 0 || p.Revision != 1 || a.TargetID != p.ID {
							t.Fatalf("publisher=%+v", p)
						}
						want = p
						return p, check(c, a, o)
					}
				case "resource-create":
					action = "resource.created"
					body = `{"namespaceId":"` + projectionResource + `","canonicalIdentifier":"urn:air:example.com:default:agent","kind":"agent"}`
					location = "/api/v1/resources/"
					selectHandler = func(s *Server) http.HandlerFunc { return s.handleResourcesCreate }
					repo.createResource = func(c context.Context, p catalog.CreateResourceParams) (catalog.Resource, error) {
						if p.Resource.TenantID != projectionTenant || p.Resource.NamespaceID != projectionResource || p.Resource.CanonicalIdentifier != "urn:air:example.com:default:agent" || p.Resource.Kind != catalog.KindAgent || p.Resource.Revision != 1 || p.Audit.TargetID != p.Resource.ID {
							t.Fatalf("resource=%+v", p)
						}
						want = p.Resource
						return p.Resource, check(c, p.Audit, p.Outbox)
					}
				case "listing-create":
					action = "catalog_listing.created"
					body = `{"namespaceId":"` + projectionResource + `","resourceId":"` + projectionItem + `"}`
					location = "/api/v1/catalog-listings/"
					selectHandler = func(s *Server) http.HandlerFunc { return s.handleListingCreate }
					repo.createListing = func(c context.Context, p catalog.CreateListingParams) (catalog.CatalogListing, error) {
						if p.Listing.TenantID != projectionTenant || p.Listing.ResourceID != projectionItem || p.Listing.NamespaceID != projectionResource || p.Listing.Revision != 1 || p.Audit.TargetID != p.Listing.ID {
							t.Fatalf("listing=%+v", p)
						}
						want = p.Listing
						return p.Listing, check(c, p.Audit, p.Outbox)
					}
				case "namespace-patch":
					action = "namespace.updated"
					path = "namespaceId"
					body = `{"description":"","reason":" reviewed change "}`
					repo.namespace.Description = "Old description"
					selectHandler = func(s *Server) http.HandlerFunc { return s.handleNamespacePatch }
					repo.updateNamespace = func(c context.Context, p catalog.UpdateNamespaceParams) (catalog.Namespace, error) {
						expected := repo.namespace
						expected.Description = ""
						expected.Revision = 4
						expected.UpdatedAt = projectionTime
						if p.ExpectedRevision != 3 || !reflect.DeepEqual(p.Namespace, expected) {
							t.Fatalf("namespace patch=%+v", p)
						}
						want = p.Namespace
						return p.Namespace, check(c, p.Audit, p.Outbox)
					}
				case "publisher-patch":
					action = "publisher.updated"
					path = "publisherId"
					body = `{"displayName":" Updated ","reason":" reviewed change "}`
					selectHandler = func(s *Server) http.HandlerFunc { return s.handlePublisherPatch }
					repo.updatePublisher = func(c context.Context, p catalog.UpdatePublisherParams) (catalog.Publisher, error) {
						expected := repo.publisher
						expected.DisplayName = "Updated"
						expected.Revision = 4
						expected.UpdatedAt = projectionTime
						if p.ExpectedRevision != 3 || !reflect.DeepEqual(p.Publisher, expected) {
							t.Fatalf("publisher patch=%+v", p)
						}
						want = p.Publisher
						return p.Publisher, check(c, p.Audit, p.Outbox)
					}
				case "domain-approve", "domain-revoke":
					if kind == "domain-revoke" {
						repo.publisher.ApprovedPublicationDomains = []string{"example.com"}
					}
					action = "publisher.publication_domain_approved"
					if kind == "domain-revoke" {
						action = "publisher.publication_domain_revoked"
					}
					path = "publisherId"
					body = `{"reason":" reviewed change "}`
					selectHandler = func(s *Server) http.HandlerFunc {
						if kind == "domain-approve" {
							return s.handlePublisherPublicationDomainApprove
						}
						return s.handlePublisherPublicationDomainRevoke
					}
					repo.domain = func(c context.Context, tenant, key, domain string, rev int64, a catalog.AuditEvent, o catalog.OutboxEvent) (catalog.Publisher, []string, error) {
						if tenant != projectionTenant || key != projectionItem || domain != "example.com" || rev != 3 {
							t.Fatal("publication domain handoff")
						}
						p := repo.publisher
						p.Revision = 4
						p.UpdatedAt = projectionTime
						if kind == "domain-approve" {
							p.ApprovedPublicationDomains = []string{"example.com"}
							want = p
						} else {
							p.ApprovedPublicationDomains = []string{}
							want = catalog.PublicationDomainRevocation{Publisher: p, AffectedReleaseIDs: []string{projectionNext}, WithdrawnHosts: []string{}}
						}
						return p, []string{projectionNext}, check(c, a, o)
					}
				case "resource-delete":
					action = "resource.deleted"
					path = "resourceId"
					body = `{"reason":" reviewed change "}`
					status = 204
					etag = ""
					selectHandler = func(s *Server) http.HandlerFunc { return s.handleResourceDelete }
					repo.deleteResource = func(c context.Context, p catalog.DeleteResourceParams) (catalog.Resource, error) {
						if p.TenantID != projectionTenant || p.ResourceID != projectionItem || p.ExpectedRevision != 3 || p.Reason != "reviewed change" || p.Actor != actor.Subject {
							t.Fatalf("delete=%+v", p)
						}
						return repo.record.Resource, check(c, p.Audit, p.Outbox)
					}
				case "request-changes", "reject":
					decision := "request_changes"
					state := catalog.SubmissionChangesRequired
					if kind == "reject" {
						decision = "reject"
						state = catalog.SubmissionRejected
					}
					action = "review_submission." + decision
					path = "submissionId"
					body = `{"reason":" reviewed change "}`
					selectHandler = func(s *Server) http.HandlerFunc {
						if kind == "reject" {
							return s.handleSubmissionReject
						}
						return s.handleSubmissionRequestChanges
					}
					repo.decide = func(c context.Context, p catalog.DecideSubmissionParams) (catalog.ReviewSubmission, catalog.ResourceVersion, error) {
						if p.TenantID != projectionTenant || p.SubmissionID != projectionItem || p.ExpectedRevision != 3 || p.Action != decision || p.Reason != "reviewed change" || p.Actor != actor.Subject || p.ExpectedValidationEvidenceID != "" {
							t.Fatalf("decision=%+v", p)
						}
						submission := repo.submission
						submission.State = state
						submission.Revision = 4
						submission.DecidedAt = &p.At
						version := repo.record.Version
						want = reviewDecisionResponse{Submission: submission, Version: version}
						return submission, version, check(c, p.Audit, p.Outbox)
					}
				default:
					target := map[string]catalog.ReleaseState{"deprecate": catalog.ReleaseDeprecated, "retire": catalog.ReleaseRetired, "supersede": catalog.ReleaseSuperseded, "revoke": catalog.ReleaseRevoked}[kind]
					action = "listing_release." + string(target)
					path = "releaseId"
					body = `{"reason":" reviewed change "}`
					replacement := ""
					var sunset *time.Time
					if kind == "supersede" || kind == "deprecate" {
						replacement = projectionNext
						body = `{"reason":" reviewed change ","replacementId":"` + replacement + `"}`
					}
					if kind == "deprecate" {
						at := projectionTime.Add(time.Hour)
						sunset = &at
						body = `{"reason":" reviewed change ","replacementId":"` + replacement + `","sunsetAt":"2026-09-20T13:00:00Z"}`
					}
					selectHandler = func(s *Server) http.HandlerFunc {
						return map[string]http.HandlerFunc{"deprecate": s.handleReleaseDeprecate, "retire": s.handleReleaseRetire, "supersede": s.handleReleaseSupersede, "revoke": s.handleReleaseRevoke}[kind]
					}
					repo.transition = func(c context.Context, p catalog.TransitionReleaseParams) (catalog.ListingRelease, error) {
						if p.TenantID != projectionTenant || p.ReleaseID != projectionItem || p.ExpectedRevision != 3 || p.TargetState != target || p.Reason != "reviewed change" || p.ReplacementID != replacement || !reflect.DeepEqual(p.SunsetAt, sunset) || p.Actor != actor.Subject {
							t.Fatalf("transition=%+v", p)
						}
						release := repo.record.Release
						release.State = target
						release.Revision = 4
						release.DispositionReason = p.Reason
						release.ReplacementReleaseID = p.ReplacementID
						release.SunsetAt = p.SunsetAt
						release.UpdatedAt = p.At
						want = release
						return release, check(c, p.Audit, p.Outbox)
					}
				}
				if strings.HasSuffix(kind, "-create") {
					status = 201
					etag = `"rev-1"`
				}
				s := projectionServer(projectionCatalog(t, repo, nil))
				r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("If-Match", `"rev-3"`)
				r.SetPathValue(path, projectionItem)
				r.SetPathValue("domain", "Example.COM.")
				w := httptest.NewRecorder()
				selectHandler(s)(w, r)
				if writes != 1 {
					t.Fatalf("writes=%d response=%s", writes, w.Body.String())
				}
				if outcome != "success" {
					status, code := 503, catalog.CodeDependencyUnavailable
					if outcome == "stale" {
						status, code = 412, catalog.CodePreconditionFailed
					}
					requireProjectionProblem(t, w, status, code)
					if w.Header().Get("ETag") != "" || w.Header().Get("Location") != "" {
						t.Fatal("failed mutation has success headers")
					}
					return
				}
				if status == 204 {
					if w.Code != 204 || w.Body.Len() != 0 {
						t.Fatalf("delete=%d %s", w.Code, w.Body.String())
					}
				} else {
					requireControlJSON(t, w, status, want)
				}
				if location != "" {
					var object struct {
						ID string `json:"id"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &object); err != nil {
						t.Fatal(err)
					}
					if !identity.ValidTenantID(object.ID) {
						t.Fatal("create identity")
					}
					location += object.ID
				}
				if w.Header().Get("ETag") != etag || w.Header().Get("Location") != location {
					t.Fatalf("receipt=%v", w.Header())
				}
				expectedReads := 1
				switch kind {
				case "namespace-create", "publisher-create", "resource-create":
					expectedReads = 0
				case "request-changes", "reject", "retire", "revoke":
					expectedReads = 2
				case "deprecate", "supersede":
					expectedReads = 3
				}
				if reads != expectedReads {
					t.Fatalf("reads=%d want=%d", reads, expectedReads)
				}
			})
		}
	}
}

func TestControlMutationRequiresRevision(t *testing.T) {
	t.Parallel()
	s := projectionServer(nil)
	for name, handler := range map[string]http.HandlerFunc{"namespace": s.handleNamespacePatch, "publisher": s.handlePublisherPatch, "domain-approve": s.handlePublisherPublicationDomainApprove, "domain-revoke": s.handlePublisherPublicationDomainRevoke, "resource-delete": s.handleResourceDelete, "version-revision": s.handleVersionRevisionCreate, "listing-revision": s.handleListingRevisionCreate, "review": s.handleSubmissionApprove, "publish": s.handleListingPublish, "release": s.handleReleaseRetire} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{}`)))
			requireProjectionProblem(t, w, 412, catalog.CodePreconditionFailed)
		})
	}
}

// Lifecycle success receipts remain in TestHTTPAuthoringPublicationSecurityAndReplay.
// These units prove selected path identities and failures before building evidence.
func TestControlLifecycleLookupFailure(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"version-create", "version-revise", "version-validate", "listing-revise", "submit", "publish"} {
		for _, outcome := range []string{"missing", "unavailable"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				actor := projectionActor(identity.RoleTenantAdmin)
				ctx := identityfixture.WithActor(t.Context(), actor)
				repo := controlReadFixture(t)
				operation := map[string]string{"version-create": "Resource", "version-revise": "Version", "version-validate": "VersionRevision", "listing-revise": "Listing", "submit": "Listing", "publish": "Listing"}[kind]
				calls := 0
				code, status := catalog.CodeNotFound, 404
				if outcome == "unavailable" {
					code, status = catalog.CodeDependencyUnavailable, 503
				}
				repo.read = func(c context.Context, tenant, key, gotOperation string) error {
					calls++
					a, ok := identity.FromContext(c)
					if !ok || a.MembershipID != actor.MembershipID || tenant != projectionTenant || key != projectionItem || gotOperation != operation {
						t.Fatal("lifecycle lookup lost selected identity")
					}
					return catalog.E(code, "selected record is unavailable")
				}
				s := projectionServer(projectionCatalog(t, repo, nil))
				handler := map[string]http.HandlerFunc{"version-create": s.handleVersionCreate, "version-revise": s.handleVersionRevisionCreate, "version-validate": s.handleVersionRevisionValidate, "listing-revise": s.handleListingRevisionCreate, "submit": s.handleSubmissionCreate, "publish": s.handleListingPublish}[kind]
				path := map[string]string{"version-create": "resourceId", "version-revise": "versionId", "version-validate": "revisionId", "listing-revise": "listingId", "submit": "listingId", "publish": "listingId"}[kind]
				body := `{}`
				switch kind {
				case "version-create":
					body = `{"version":"1.0.0","representations":[{"role":"primary","mediaType":"application/a2a-agent-card+json","data":` + string(repo.record.VersionRevision.Representations[0].Data) + `}]}`
				case "version-revise":
					body = `{"representations":[{"role":"primary","mediaType":"application/a2a-agent-card+json","data":` + string(repo.record.VersionRevision.Representations[0].Data) + `}]}`
				case "listing-revise":
					body = `{"versionRevisionId":"` + projectionItem + `","publisherId":"` + projectionResource + `","displayName":"Agent","description":"Retained listing","visibility":"public"}`
				case "publish":
					body = `{"submissionId":"` + projectionItem + `"}`
				}
				r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("If-Match", `"rev-3"`)
				r.SetPathValue(path, projectionItem)
				w := httptest.NewRecorder()
				handler(w, r)
				requireProjectionProblem(t, w, status, code)
				if calls != 1 || w.Header().Get("Location") != "" || w.Header().Get("ETag") != "" {
					t.Fatalf("calls=%d headers=%v", calls, w.Header())
				}
			})
		}
	}
}
