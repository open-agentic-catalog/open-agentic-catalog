// Control read projections use static records and fail on unexpected port calls.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type controlReadRepository struct {
	catalog.Repository
	record      catalog.CatalogRecord
	namespace   catalog.Namespace
	publisher   catalog.Publisher
	submission  catalog.ReviewSubmission
	document    catalog.DocumentBlob
	replacement catalog.ListingRelease
	read        func(context.Context, string, string, string) error
	appendAudit func(context.Context, audit.Record) error
}

func (r controlReadRepository) GetNamespace(c context.Context, tenant, key string) (catalog.Namespace, error) {
	return r.namespace, r.read(c, tenant, key, "Namespace")
}
func (r controlReadRepository) GetPublisher(c context.Context, tenant, key string) (catalog.Publisher, error) {
	return r.publisher, r.read(c, tenant, key, "Publisher")
}
func (r controlReadRepository) GetResource(c context.Context, tenant, key string) (catalog.Resource, error) {
	return r.record.Resource, r.read(c, tenant, key, "Resource")
}
func (r controlReadRepository) GetVersion(c context.Context, tenant, key string) (catalog.ResourceVersion, error) {
	return r.record.Version, r.read(c, tenant, key, "Version")
}
func (r controlReadRepository) GetListing(c context.Context, tenant, key string) (catalog.CatalogListing, error) {
	return r.record.Listing, r.read(c, tenant, key, "Listing")
}
func (r controlReadRepository) GetListingRevision(c context.Context, tenant, key string) (catalog.ListingRevision, error) {
	return r.record.ListingRevision, r.read(c, tenant, key, "ListingRevision")
}
func (r controlReadRepository) GetSubmission(c context.Context, tenant, key string) (catalog.ReviewSubmission, error) {
	return r.submission, r.read(c, tenant, key, "Submission")
}
func (r controlReadRepository) GetRelease(c context.Context, tenant, key string) (catalog.ListingRelease, error) {
	item := r.record.Release
	if key == r.replacement.ID {
		item = r.replacement
	}
	return item, r.read(c, tenant, key, "Release")
}
func (r controlReadRepository) GetCatalogRecord(c context.Context, tenant, key string) (catalog.CatalogRecord, error) {
	return r.record, r.read(c, tenant, key, "CatalogRecord")
}
func (r controlReadRepository) GetDocument(c context.Context, tenant, key string) (catalog.DocumentBlob, error) {
	return r.document, r.read(c, tenant, key, "Document")
}
func (r controlReadRepository) GetVersionRevision(c context.Context, tenant, key string) (catalog.VersionRevision, catalog.EvidenceSet, error) {
	return r.record.VersionRevision, r.record.Evidence, r.read(c, tenant, key, "VersionRevision")
}
func (r controlReadRepository) AppendAuditEvent(c context.Context, record audit.Record) error {
	return r.appendAudit(c, record)
}

// Each fixture is a retained response projection, not a record created by a workflow.
func controlReadFixture(t *testing.T) controlReadRepository {
	t.Helper()
	digest := audit.DigestBytes([]byte("retained content"))
	r := controlReadRepository{}
	r.namespace = catalog.Namespace{ID: projectionItem, TenantID: projectionTenant, Slug: "default", DisplayName: "Default", Revision: 3, CreatedAt: projectionTime, UpdatedAt: projectionTime}
	r.publisher = catalog.Publisher{ID: projectionItem, TenantID: projectionTenant, DisplayName: "Publisher", Domains: []string{"example.com"}, ApprovedPublicationDomains: []string{}, Revision: 3, CreatedAt: projectionTime, UpdatedAt: projectionTime}
	r.record.Resource = catalog.Resource{ID: projectionItem, TenantID: projectionTenant, NamespaceID: projectionResource, CanonicalIdentifier: "urn:air:example.com:default:agent", Kind: catalog.KindAgent, CreatedBy: "author", Revision: 3, CreatedAt: projectionTime, UpdatedAt: projectionTime}
	r.record.Version = catalog.ResourceVersion{ID: projectionItem, TenantID: projectionTenant, ResourceID: projectionItem, Version: "1.0.0", CandidateRevisionID: projectionItem, SealedRevisionID: projectionItem, Revision: 3, CreatedAt: projectionTime, UpdatedAt: projectionTime}
	r.record.VersionRevision = catalog.VersionRevision{ID: projectionItem, TenantID: projectionTenant, ResourceVersionID: projectionItem, RevisionNumber: 1, ContentSetDigest: "sha256:ca3e4f24dc513fe46895e87c298bfcd8a29efe5bca1101b45609441146da1c8b", EvidenceSetID: projectionNext, Representations: []catalog.Representation{{ID: projectionNext, Role: catalog.RepresentationPrimary, MediaType: "application/a2a-agent-card+json", ProfileID: "a2a-agent-card-1.0.1-3303592", Data: json.RawMessage(`{"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"description":"Retained agent card","name":"Agent","skills":[{"description":"Example skill","id":"example","name":"Example","tags":["example"]}],"supportedInterfaces":[{"protocolBinding":"JSONRPC","protocolVersion":"1.0","url":"https://agents.example.com/agent"}],"version":"1.0.0"}`), ContentDigest: "sha256:1d479f45256c3cabd8f3e7f20898f8d8467a6a16ba22dd837a31fe88a62a9a63", Size: 384}}, CreatedBy: "author", CreatedAt: projectionTime}
	r.record.Evidence = catalog.EvidenceSet{ID: projectionNext, TenantID: projectionTenant, SubjectDigest: "sha256:ca3e4f24dc513fe46895e87c298bfcd8a29efe5bca1101b45609441146da1c8b", Digest: "sha256:6338863211ec8c6d24fb5e9945456e411409c794859bf4c84359c69788cff5fb", State: catalog.EvidenceComplete, LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}, ValidationProfile: "catalog-validation-v7", PolicyVersion: "fixture", CreatedAt: projectionTime}
	r.record.Listing = catalog.CatalogListing{ID: projectionItem, TenantID: projectionTenant, NamespaceID: projectionResource, ResourceID: projectionItem, CandidateRevisionID: projectionItem, CurrentReleaseID: projectionItem, Revision: 3, CreatedAt: projectionTime, UpdatedAt: projectionTime}
	r.record.ListingRevision = catalog.ListingRevision{ID: projectionItem, TenantID: projectionTenant, ListingID: projectionItem, VersionRevisionID: projectionItem, PublisherID: projectionResource, RevisionNumber: 1, DisplayName: "Agent", Description: "Retained listing", Visibility: catalog.VisibilityPublic, Digest: "sha256:da2cbab40a267ae0d5e9fe8c69132b27339bf9ae8823cd753619192f88cf8138", CreatedBy: "author", CreatedAt: projectionTime}
	r.submission = catalog.ReviewSubmission{ID: projectionItem, TenantID: projectionTenant, ListingID: projectionItem, VersionRevisionID: projectionItem, ListingRevisionID: projectionItem, EvidenceSetID: projectionNext, EvidenceDigest: r.record.Evidence.Digest, PolicyVersion: "fixture", State: catalog.SubmissionSubmitted, SubmittedBy: "author", Revision: 3, SubmittedAt: projectionTime}
	r.record.Release = catalog.ListingRelease{ID: projectionItem, TenantID: projectionTenant, ListingID: projectionItem, SubmissionID: projectionItem, VersionRevisionID: projectionItem, ListingRevisionID: projectionItem, State: catalog.ReleasePublished, ActivatedBy: "publisher", Revision: 3, ActivatedAt: projectionTime, UpdatedAt: projectionTime}
	r.record.Source = "local"
	r.replacement = r.record.Release
	r.replacement.ID = projectionNext
	r.document = catalog.DocumentBlob{ID: strings.TrimPrefix(digest, "sha256:"), TenantID: projectionTenant, Digest: digest, MediaType: "text/plain", Content: []byte("retained content"), Size: 16, CreatedAt: projectionTime}
	if err := catalog.ValidateVersionRevisionIntegrity(r.record.VersionRevision, r.record.Evidence); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateEvidenceSetIntegrity(r.record.Evidence); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateListingRevisionDigest(r.record.ListingRevision); err != nil {
		t.Fatal(err)
	}
	return r
}

func requireControlJSON(t *testing.T, w *httptest.ResponseRecorder, status int, want any) {
	t.Helper()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got, expected any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	if w.Code != status || w.Header().Get("Content-Type") != "application/json; charset=utf-8" || !reflect.DeepEqual(got, expected) {
		t.Fatalf("status=%d headers=%v body=%s want=%s", w.Code, w.Header(), w.Body.String(), raw)
	}
}

func TestControlReadProjection(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"Namespace", "Publisher", "Resource", "Version", "VersionRevision", "Listing", "ListingRevision", "Submission", "Release", "CatalogRecord", "Document"} {
		for _, outcome := range []string{"retained", "missing", "unavailable", "audit-unavailable"} {
			sensitive := kind == "VersionRevision" || kind == "Document"
			if outcome == "audit-unavailable" && !sensitive {
				continue
			}
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
				repo := controlReadFixture(t)
				reads, audits := 0, 0
				key := projectionItem
				if kind == "Document" {
					key = repo.document.ID
					if err := catalog.ValidateDocumentBlob(projectionTenant, repo.document); err != nil {
						t.Fatal(err)
					}
				}
				repo.read = func(c context.Context, tenant, id, operation string) error {
					reads++
					expectedOps := map[string][]string{
						"Resource": {"Resource", "Resource"}, "Version": {"Version", "Resource", "Version"},
						"VersionRevision": {"VersionRevision", "Version", "Resource"}, "Listing": {"Listing", "Listing"},
						"ListingRevision": {"ListingRevision", "Listing", "ListingRevision"}, "Submission": {"Submission", "Listing", "Submission"},
						"Release": {"Release", "Listing", "Release"}, "CatalogRecord": {"CatalogRecord", "CatalogRecord"},
					}
					ops := expectedOps[kind]
					if ops == nil {
						ops = []string{kind}
					}
					if !sameProjectionContext(c, ctx) || tenant != projectionTenant || id != key || reads > len(ops) || operation != ops[reads-1] {
						t.Fatalf("read binding %s %s %s", tenant, id, operation)
					}
					if outcome == "missing" {
						return catalog.E(catalog.CodeNotFound, "record was not found")
					}
					if outcome == "unavailable" {
						return catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
					}
					return nil
				}
				repo.appendAudit = func(c context.Context, record audit.Record) error {
					audits++
					action := audit.ActionVersionRevisionRead
					wantDigest := repo.record.VersionRevision.ContentSetDigest
					if kind == "Document" {
						action = audit.ActionCatalogDocumentRead
						wantDigest = repo.document.Digest
					}
					if !sameProjectionContext(c, ctx) || reads != func() int {
						if kind == "VersionRevision" {
							return 3
						}
						return 1
					}() || record.Action != action || record.TargetID != key || record.Actor != projectionActor(identity.RoleReader).MembershipID || record.AfterDigest != wantDigest {
						t.Fatalf("audit=%+v", record)
					}
					if err := audit.ValidateNewRecord(record); err != nil {
						t.Fatal(err)
					}
					if outcome == "audit-unavailable" {
						return errors.New("private-fixture-audit")
					}
					return nil
				}
				s := projectionServer(projectionCatalog(t, repo, nil))
				var handler http.HandlerFunc
				var want any
				path, etag := "", `"rev-3"`
				switch kind {
				case "Namespace":
					handler, path, want = s.handleNamespaceGet, "namespaceId", repo.namespace
				case "Publisher":
					handler, path, want = s.handlePublisherGet, "publisherId", repo.publisher
				case "Resource":
					handler, path, want = s.handleResourceGet, "resourceId", repo.record.Resource
				case "Version":
					handler, path, want = s.handleVersionGet, "versionId", repo.record.Version
				case "VersionRevision":
					handler, path, want = s.handleVersionRevisionGet, "revisionId", revisionEvidenceResponse{NamespaceID: repo.record.Resource.NamespaceID, Revision: repo.record.VersionRevision, Evidence: repo.record.Evidence}
					etag = ""
				case "Listing":
					handler, path, want = s.handleListingGet, "listingId", repo.record.Listing
				case "ListingRevision":
					handler, path, want = s.handleListingRevisionGet, "revisionId", repo.record.ListingRevision
					etag = ""
				case "Submission":
					projected := repo.submission
					projected.NamespaceID = repo.record.Listing.NamespaceID
					projected.ListingConcurrencyRevision = repo.record.Listing.Revision
					handler, path, want = s.handleSubmissionGet, "submissionId", projected
				case "Release":
					handler, path, want = s.handleReleaseGet, "releaseId", repo.record.Release
				case "CatalogRecord":
					handler, path, want = s.handleCatalogRecordGet, "catalogRecordId", repo.record
				case "Document":
					handler, path, want = s.handleArtifactGet, "artifactId", nil
					etag = ""
				}
				r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
				r.SetPathValue(path, key)
				w := httptest.NewRecorder()
				handler(w, r)
				wantReads := map[string]int{"Resource": 2, "Version": 3, "VersionRevision": 3, "Listing": 2, "ListingRevision": 3, "Submission": 3, "Release": 3, "CatalogRecord": 2}[kind]
				if wantReads == 0 || outcome == "missing" || outcome == "unavailable" {
					wantReads = 1
				}
				if reads != wantReads {
					t.Fatalf("reads=%d", reads)
				}
				expectedAudits := 0
				if sensitive && (outcome == "retained" || outcome == "audit-unavailable") {
					expectedAudits = 1
				}
				if audits != expectedAudits {
					t.Fatalf("audits=%d want=%d", audits, expectedAudits)
				}
				if outcome != "retained" {
					status, code := 503, catalog.CodeDependencyUnavailable
					if outcome == "missing" {
						status, code = 404, catalog.CodeNotFound
					}
					requireProjectionProblem(t, w, status, code)
					if w.Header().Get("ETag") != "" || w.Header().Get("Content-Disposition") != "" || strings.Contains(w.Body.String(), "retained content") {
						t.Fatal("error disclosed a success receipt or content")
					}
					return
				}
				if kind == "Document" {

					if w.Code != 200 || w.Body.String() != string(repo.document.Content) || w.Header().Get("Content-Type") != "text/plain" || w.Header().Get("Content-Length") != "16" || w.Header().Get("Content-Digest") != digestHeader(repo.document.Content) || w.Header().Get("Content-Disposition") != `attachment; filename="`+key+`"` {
						t.Fatalf("attachment headers=%v body=%s", w.Header(), w.Body.String())
					}
				} else {
					requireControlJSON(t, w, 200, want)
				}
				if w.Header().Get("ETag") != etag {
					t.Fatalf("etag=%q want=%q", w.Header().Get("ETag"), etag)
				}
			})
		}
	}
}
