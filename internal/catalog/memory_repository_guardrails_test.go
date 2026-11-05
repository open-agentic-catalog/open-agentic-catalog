package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"strings"
	"testing"
	"time"
)

const guardAuthorMember = "00000000-0000-7000-8000-000000000091"
const guardReviewerMember = "00000000-0000-7000-8000-000000000092"
const guardRealm = "00000000-0000-7000-8000-000000000093"

const repositoryGuardrailTenant = "00000000-0000-7000-8000-000000000081"

func newRepositoryGuardrailFixture() *MemoryRepository {
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	repository.namespaces["namespace"] = Namespace{ID: "namespace", TenantID: repositoryGuardrailTenant, Slug: "default"}
	repository.publishers["publisher"] = Publisher{ID: "publisher", TenantID: repositoryGuardrailTenant, DisplayName: "Publisher", Domains: []string{"catalog.example.com"}, Revision: 1}
	repository.resources["resource"] = Resource{ID: "resource", TenantID: repositoryGuardrailTenant, NamespaceID: "namespace", CanonicalIdentifier: "urn:air:catalog.example.com:default:resource", Kind: KindAgent, Revision: 1, UpdatedAt: now}
	repository.versions["version"] = ResourceVersion{ID: "version", TenantID: repositoryGuardrailTenant, ResourceID: "resource", Version: "1.0.0", CandidateRevisionID: "version-revision", Revision: 1, UpdatedAt: now}
	repository.versionRevisions["version-revision"] = VersionRevision{ID: "version-revision", TenantID: repositoryGuardrailTenant, ResourceVersionID: "version", ContentSetDigest: "sha256:version-one", EvidenceSetID: "evidence", CreatedAt: now}
	repository.evidenceSets["evidence"] = EvidenceSet{ValidationProfile: currentValidationProfile, LegalRights: LegalRightsSummary{State: LegalRightsNotAssessed}, ID: "evidence", TenantID: repositoryGuardrailTenant, SubjectDigest: "sha256:version-one", State: EvidenceComplete, CreatedAt: now}
	repository.listings["listing"] = CatalogListing{ID: "listing", TenantID: repositoryGuardrailTenant, NamespaceID: "namespace", ResourceID: "resource", CandidateRevisionID: "listing-revision", CurrentReleaseID: "release", Revision: 1, UpdatedAt: now}
	repository.listingRevisions["listing-revision"] = ListingRevision{ID: "listing-revision", TenantID: repositoryGuardrailTenant, ListingID: "listing", VersionRevisionID: "version-revision", DisplayName: "Resource", Visibility: VisibilityPublic, Digest: "sha256:listing-one", CreatedAt: now}
	repository.submissions["submission"] = ReviewSubmission{ID: "submission", TenantID: repositoryGuardrailTenant, ListingID: "listing", VersionRevisionID: "version-revision", ListingRevisionID: "listing-revision", State: SubmissionSubmitted, SubmittedBy: "author", Revision: 1}
	repository.releases["release"] = ListingRelease{ID: "release", TenantID: repositoryGuardrailTenant, ListingID: "listing", SubmissionID: "submission", VersionRevisionID: "version-revision", ListingRevisionID: "listing-revision", State: ReleasePublished, Revision: 1, ActivatedAt: now, UpdatedAt: now}
	version := audit.RecordFormatV1
	repository.indexOriginatingPrincipal(AuditEvent{TenantID: repositoryGuardrailTenant, TargetType: "review_submission", TargetID: "submission", Action: "review_submission.submitted", Actor: guardAuthorMember, FormatVersion: &version, Details: map[string]any{"realmId": guardRealm, "actorType": "human", "principalType": "catalog_membership"}})
	return repository
}

func TestMemoryRepositoryPublicationDomainRevocationIsTenantBoundAndReportsAffectedReleases(t *testing.T) {
	repository := newRepositoryGuardrailFixture()
	publisher := repository.publishers["publisher"]
	delete(repository.publishers, publisher.ID)
	publisher.ID = "00000000-0000-7000-8000-000000000082"
	publisher.ApprovedPublicationDomains = []string{"catalog.example.com", "other.example.com"}
	publisher.Revision = 2
	repository.publishers[publisher.ID] = publisher
	revision := repository.listingRevisions["listing-revision"]
	revision.PublisherID = publisher.ID
	repository.listingRevisions[revision.ID] = revision

	add := func(listingID, releaseID, resourceID, identifier, publisherID string, state ReleaseState, tenantID string) {
		repository.resources[resourceID] = Resource{ID: resourceID, TenantID: tenantID, CanonicalIdentifier: identifier}
		revisionID := "revision-" + listingID
		repository.listingRevisions[revisionID] = ListingRevision{ID: revisionID, TenantID: tenantID, PublisherID: publisherID}
		repository.releases[releaseID] = ListingRelease{ID: releaseID, TenantID: tenantID, ListingRevisionID: revisionID, State: state}
		repository.listings[listingID] = CatalogListing{ID: listingID, TenantID: tenantID, ResourceID: resourceID, CurrentReleaseID: releaseID}
	}
	add("listing-a", "release-a", "resource-a", "urn:air:catalog.example.com:default:a", publisher.ID, ReleaseDeprecated, repositoryGuardrailTenant)
	add("listing-other-domain", "release-other-domain", "resource-other-domain", "urn:air:other.example.com:default:a", publisher.ID, ReleasePublished, repositoryGuardrailTenant)
	add("listing-other-publisher", "release-other-publisher", "resource-other-publisher", "urn:air:catalog.example.com:default:b", "another-publisher", ReleasePublished, repositoryGuardrailTenant)
	add("listing-retired", "release-retired", "resource-retired", "urn:air:catalog.example.com:default:c", publisher.ID, ReleaseRetired, repositoryGuardrailTenant)
	add("listing-invalid-air", "release-invalid-air", "resource-invalid-air", "not-an-air-identifier", publisher.ID, ReleasePublished, repositoryGuardrailTenant)
	add("listing-other-tenant", "release-other-tenant", "resource-other-tenant", "urn:air:catalog.example.com:default:d", publisher.ID, ReleasePublished, "tenant-two")
	repository.listings["listing-no-release"] = CatalogListing{ID: "listing-no-release", TenantID: repositoryGuardrailTenant}
	repository.listings["listing-missing-release"] = CatalogListing{ID: "listing-missing-release", TenantID: repositoryGuardrailTenant, CurrentReleaseID: "missing-release"}
	repository.releases["release-missing-revision"] = ListingRelease{ID: "release-missing-revision", TenantID: repositoryGuardrailTenant, ListingRevisionID: "missing-revision", State: ReleasePublished}
	repository.listings["listing-missing-revision"] = CatalogListing{ID: "listing-missing-revision", TenantID: repositoryGuardrailTenant, CurrentReleaseID: "release-missing-revision"}
	repository.releases["release-missing-resource"] = ListingRelease{ID: "release-missing-resource", TenantID: repositoryGuardrailTenant, ListingRevisionID: "revision-missing-resource", State: ReleasePublished}
	repository.listingRevisions["revision-missing-resource"] = ListingRevision{ID: "revision-missing-resource", TenantID: repositoryGuardrailTenant, PublisherID: publisher.ID}
	repository.listings["listing-missing-resource"] = CatalogListing{ID: "listing-missing-resource", TenantID: repositoryGuardrailTenant, ResourceID: "missing-resource", CurrentReleaseID: "release-missing-resource"}

	if _, _, err := repository.RevokePublisherPublicationDomain(t.Context(), repositoryGuardrailTenant, "missing", "catalog.example.com", 1, AuditEvent{}, OutboxEvent{}); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("missing publisher revocation returned %v", err)
	}
	if _, _, err := repository.RevokePublisherPublicationDomain(t.Context(), "tenant-two", publisher.ID, "catalog.example.com", publisher.Revision, AuditEvent{}, OutboxEvent{}); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("cross-tenant publisher revocation returned %v", err)
	}
	if _, _, err := repository.RevokePublisherPublicationDomain(t.Context(), repositoryGuardrailTenant, publisher.ID, "catalog.example.com", 1, AuditEvent{}, OutboxEvent{}); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("stale publisher revocation returned %v", err)
	}
	if _, _, err := repository.RevokePublisherPublicationDomain(t.Context(), repositoryGuardrailTenant, publisher.ID, "unapproved.example.com", publisher.Revision, AuditEvent{}, OutboxEvent{}); ErrorCodeOf(err) != CodeInvalidTransition {
		t.Fatalf("unapproved publisher revocation returned %v", err)
	}

	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	updated, affected, err := repository.RevokePublisherPublicationDomain(t.Context(), repositoryGuardrailTenant, publisher.ID, "catalog.example.com", publisher.Revision, AuditEvent{FormatVersion: func() *int { v := 1; return &v }(), ID: "00000000-0000-7000-8000-000000000083", TenantID: repositoryGuardrailTenant, Actor: "catalog-api", Action: "publisher.publication_domain_revoked", TargetType: "publisher", TargetID: publisher.ID, Reason: "reviewed domain revocation", Details: map[string]any{"principalType": "service_executor"}, At: now}, OutboxEvent{ID: "outbox-revoke", TenantID: repositoryGuardrailTenant})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != publisher.Revision+1 || len(updated.ApprovedPublicationDomains) != 1 || updated.ApprovedPublicationDomains[0] != "other.example.com" || !updated.UpdatedAt.Equal(now) {
		t.Fatalf("revoked publisher = %#v", updated)
	}
	if len(affected) != 2 || affected[0] != "release" || affected[1] != "release-a" {
		t.Fatalf("affected releases = %#v", affected)
	}
	if len(repository.audit) == 0 || repository.audit[len(repository.audit)-1].Action != "publisher.publication_domain_revoked" || len(repository.outbox) == 0 {
		t.Fatalf("revocation evidence = audit %#v, outbox %#v", repository.audit, repository.outbox)
	}
	if _, _, err := repository.RevokePublisherPublicationDomain(t.Context(), repositoryGuardrailTenant, publisher.ID, "catalog.example.com", updated.Revision, AuditEvent{}, OutboxEvent{}); ErrorCodeOf(err) != CodeInvalidTransition {
		t.Fatalf("repeat publisher revocation returned %v", err)
	}
}

func repositoryVersionRevisionParams() CreateVersionParams {
	return CreateVersionParams{
		Version:          ResourceVersion{ID: "version", TenantID: repositoryGuardrailTenant, ResourceID: "resource", Version: "1.0.0", CandidateRevisionID: "version-revision-two", Revision: 2},
		VersionRevision:  VersionRevision{ID: "version-revision-two", TenantID: repositoryGuardrailTenant, ResourceVersionID: "version", ContentSetDigest: "sha256:version-two", EvidenceSetID: "evidence-two"},
		Evidence:         EvidenceSet{ValidationProfile: currentValidationProfile, LegalRights: LegalRightsSummary{State: LegalRightsNotAssessed}, ID: "evidence-two", TenantID: repositoryGuardrailTenant, SubjectDigest: "sha256:version-two", State: EvidenceComplete},
		ExpectedRevision: 1,
	}
}

func repositoryListingRevisionParams() CreateListingRevisionParams {
	return CreateListingRevisionParams{
		Revision:         ListingRevision{ID: "listing-revision-two", TenantID: repositoryGuardrailTenant, ListingID: "listing", VersionRevisionID: "version-revision", DisplayName: "Resource two", Visibility: VisibilityPublic, Digest: "sha256:listing-two"},
		ExpectedRevision: 1,
	}
}

func TestMemoryRepositoryEnforcesAuthoringIntegrity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name string
		code ErrorCode
		call func(*MemoryRepository) error
	}{
		{
			name: "version requires resource",
			code: CodeNotFound,
			call: func(repository *MemoryRepository) error {
				params := repositoryVersionRevisionParams()
				params.Version.ID = "new-version"
				params.Version.ResourceID = "missing"
				_, _, _, err := repository.CreateVersion(ctx, params)
				return err
			},
		},
		{
			name: "version label is unique",
			code: CodeConflict,
			call: func(repository *MemoryRepository) error {
				params := repositoryVersionRevisionParams()
				params.Version.ID = "new-version"
				_, _, _, err := repository.CreateVersion(ctx, params)
				return err
			},
		},
		{
			name: "revision requires version",
			code: CodeNotFound,
			call: func(repository *MemoryRepository) error {
				params := repositoryVersionRevisionParams()
				params.Version.ID = "missing"
				_, _, _, err := repository.CreateVersionRevision(ctx, params)
				return err
			},
		},
		{
			name: "sealed version is immutable",
			code: CodeVersionSealed,
			call: func(repository *MemoryRepository) error {
				version := repository.versions["version"]
				version.SealedRevisionID = "version-revision"
				repository.versions[version.ID] = version
				_, _, _, err := repository.CreateVersionRevision(ctx, repositoryVersionRevisionParams())
				return err
			},
		},
		{
			name: "revision checks expected version",
			code: CodePreconditionFailed,
			call: func(repository *MemoryRepository) error {
				params := repositoryVersionRevisionParams()
				params.ExpectedRevision = 2
				_, _, _, err := repository.CreateVersionRevision(ctx, params)
				return err
			},
		},
		{
			name: "revision advances exactly once",
			code: CodePreconditionFailed,
			call: func(repository *MemoryRepository) error {
				params := repositoryVersionRevisionParams()
				params.Version.Revision = 3
				_, _, _, err := repository.CreateVersionRevision(ctx, params)
				return err
			},
		},
		{
			name: "revision content is unique",
			code: CodeConflict,
			call: func(repository *MemoryRepository) error {
				params := repositoryVersionRevisionParams()
				params.VersionRevision.ContentSetDigest = "sha256:version-one"
				_, _, _, err := repository.CreateVersionRevision(ctx, params)
				return err
			},
		},
		{
			name: "listing requires resource",
			code: CodeNotFound,
			call: func(repository *MemoryRepository) error {
				_, err := repository.CreateListing(ctx, CreateListingParams{Listing: CatalogListing{ID: "new-listing", TenantID: repositoryGuardrailTenant, NamespaceID: "namespace", ResourceID: "missing"}})
				return err
			},
		},
		{
			name: "listing requires namespace",
			code: CodeInvalidArgument,
			call: func(repository *MemoryRepository) error {
				repository.resources["orphan"] = Resource{ID: "orphan", TenantID: repositoryGuardrailTenant, NamespaceID: "missing"}
				_, err := repository.CreateListing(ctx, CreateListingParams{Listing: CatalogListing{ID: "new-listing", TenantID: repositoryGuardrailTenant, NamespaceID: "missing", ResourceID: "orphan"}})
				return err
			},
		},
		{
			name: "listing is unique",
			code: CodeConflict,
			call: func(repository *MemoryRepository) error {
				_, err := repository.CreateListing(ctx, CreateListingParams{Listing: CatalogListing{ID: "new-listing", TenantID: repositoryGuardrailTenant, NamespaceID: "namespace", ResourceID: "resource"}})
				return err
			},
		},
		{
			name: "listing revision requires listing",
			code: CodeNotFound,
			call: func(repository *MemoryRepository) error {
				params := repositoryListingRevisionParams()
				params.Revision.ListingID = "missing"
				_, _, err := repository.CreateListingRevision(ctx, params)
				return err
			},
		},
		{
			name: "listing revision checks expected version",
			code: CodePreconditionFailed,
			call: func(repository *MemoryRepository) error {
				params := repositoryListingRevisionParams()
				params.ExpectedRevision = 2
				_, _, err := repository.CreateListingRevision(ctx, params)
				return err
			},
		},
		{
			name: "listing revision requires version revision",
			code: CodeNotFound,
			call: func(repository *MemoryRepository) error {
				params := repositoryListingRevisionParams()
				params.Revision.VersionRevisionID = "missing"
				_, _, err := repository.CreateListingRevision(ctx, params)
				return err
			},
		},
		{
			name: "listing revision content is unique",
			code: CodeConflict,
			call: func(repository *MemoryRepository) error {
				params := repositoryListingRevisionParams()
				params.Revision.Digest = "sha256:listing-one"
				_, _, err := repository.CreateListingRevision(ctx, params)
				return err
			},
		},
		{
			name: "submission requires listing",
			code: CodeNotFound,
			call: func(repository *MemoryRepository) error {
				_, err := repository.Submit(ctx, SubmitParams{Submission: ReviewSubmission{ID: "new-submission", TenantID: repositoryGuardrailTenant, ListingID: "missing"}})
				return err
			},
		},
		{
			name: "submission binds candidate",
			code: CodePreconditionFailed,
			call: func(repository *MemoryRepository) error {
				_, err := repository.Submit(ctx, SubmitParams{Submission: ReviewSubmission{ID: "new-submission", TenantID: repositoryGuardrailTenant, ListingID: "listing", ListingRevisionID: "changed"}})
				return err
			},
		},
		{
			name: "only one active submission",
			code: CodeConflict,
			call: func(repository *MemoryRepository) error {
				_, err := repository.Submit(ctx, SubmitParams{Submission: ReviewSubmission{ID: "new-submission", TenantID: repositoryGuardrailTenant, ListingID: "listing", ListingRevisionID: "listing-revision"}})
				return err
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			repository := newRepositoryGuardrailFixture()
			requireCatalogCode(t, testCase.call(repository), testCase.code)
		})
	}
	staleValidationRepository := newRepositoryGuardrailFixture()
	delete(staleValidationRepository.submissions, "submission")
	staleValidationRepository.validationRuns["version-revision"] = []string{"evidence", "new-evidence"}
	if _, err := staleValidationRepository.Submit(ctx, SubmitParams{Submission: ReviewSubmission{ID: "new-submission", TenantID: repositoryGuardrailTenant, ListingID: "listing", VersionRevisionID: "version-revision", ListingRevisionID: "listing-revision", EvidenceSetID: "evidence", State: SubmissionSubmitted}}); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("submission with concurrently replaced validation evidence returned %v", err)
	}

	repository := newRepositoryGuardrailFixture()
	version, revision, evidence, err := repository.CreateVersionRevision(ctx, repositoryVersionRevisionParams())
	if err != nil || version.Revision != 2 || revision.ID != "version-revision-two" || evidence.ID != "evidence-two" {
		t.Fatalf("version revision = %#v, %#v, %#v, %v", version, revision, evidence, err)
	}
	listing, listingRevision, err := repository.CreateListingRevision(ctx, repositoryListingRevisionParams())
	if err != nil || listing.Revision != 2 || listing.CandidateRevisionID != listingRevision.ID {
		t.Fatalf("listing revision = %#v, %#v, %v", listing, listingRevision, err)
	}
}

func TestMemoryRepositoryValidationRunsAreAppendOnlyAndTenantBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := newRepositoryGuardrailFixture()
	run := EvidenceSet{ValidationProfile: currentValidationProfile, LegalRights: LegalRightsSummary{State: LegalRightsNotAssessed}, ID: "evidence-renewed", TenantID: repositoryGuardrailTenant, SubjectDigest: "sha256:version-one", Digest: "sha256:renewed", State: EvidenceComplete, PolicyVersion: "test-policy", CreatedAt: time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)}

	created, err := repository.CreateValidationRun(ctx, CreateValidationRunParams{TenantID: repositoryGuardrailTenant, VersionRevisionID: "version-revision", Evidence: run, Audit: AuditEvent{ID: "audit-renewed", TenantID: repositoryGuardrailTenant}, Outbox: OutboxEvent{ID: "outbox-renewed", TenantID: repositoryGuardrailTenant}})
	if err != nil || created.ID != run.ID {
		t.Fatalf("validation run = %#v, %v", created, err)
	}
	revision, current, err := repository.GetVersionRevision(ctx, repositoryGuardrailTenant, "version-revision")
	if err != nil || revision.EvidenceSetID != run.ID || current.ID != run.ID || repository.versionRevisions[revision.ID].EvidenceSetID != "evidence" {
		t.Fatalf("current validation projection = %#v / %#v / %v", revision, current, err)
	}
	historical, err := repository.GetValidationEvidence(ctx, repositoryGuardrailTenant, revision.ID, "evidence")
	if err != nil || historical.ID != "evidence" {
		t.Fatalf("historical validation evidence = %#v, %v", historical, err)
	}
	if _, err := repository.GetValidationEvidence(ctx, "tenant-two", revision.ID, run.ID); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("cross-tenant evidence read returned %v", err)
	}
	if len(repository.audit) != 1 || len(repository.outbox) != 1 {
		t.Fatalf("validation evidence transaction = audit %#v outbox %#v", repository.audit, repository.outbox)
	}

	invalid := run
	invalid.ID = "evidence-invalid"
	invalid.Digest = "sha256:invalid"
	invalid.SubjectDigest = "sha256:other"
	if _, err := repository.CreateValidationRun(ctx, CreateValidationRunParams{TenantID: repositoryGuardrailTenant, VersionRevisionID: revision.ID, Evidence: invalid}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("mismatched validation evidence returned %v", err)
	}
	if _, err := repository.CreateValidationRun(ctx, CreateValidationRunParams{TenantID: "tenant-two", VersionRevisionID: revision.ID, Evidence: run}); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("cross-tenant validation write returned %v", err)
	}
	if _, err := repository.CreateValidationRun(ctx, CreateValidationRunParams{TenantID: repositoryGuardrailTenant, VersionRevisionID: revision.ID, Evidence: run}); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("duplicate validation evidence returned %v", err)
	}
	if len(repository.validationRuns[revision.ID]) != 1 || len(repository.audit) != 1 || len(repository.outbox) != 1 {
		t.Fatalf("failed validation writes changed state: runs=%#v audit=%d outbox=%d", repository.validationRuns[revision.ID], len(repository.audit), len(repository.outbox))
	}
}

func TestMemoryRepositoryValidatesAndDeduplicatesCatalogDocuments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, time.August, 31, 14, 0, 0, 0, time.UTC)
	content := []byte("shared exact document")
	digest := digestBytes(content)
	documentID := strings.TrimPrefix(digest, "sha256:")

	params := func(versionID, label, mediaType string, createdAt time.Time) CreateVersionParams {
		document := DocumentBlob{ID: documentID, TenantID: repositoryGuardrailTenant, Digest: digest, MediaType: mediaType, Size: int64(len(content)), Content: append([]byte(nil), content...), CreatedAt: createdAt}
		revisionID := versionID + "-revision"
		evidenceID := versionID + "-evidence"
		return CreateVersionParams{
			Version:         ResourceVersion{ID: versionID, TenantID: repositoryGuardrailTenant, ResourceID: "resource", Version: label, Revision: 1, CreatedAt: createdAt, UpdatedAt: createdAt},
			VersionRevision: VersionRevision{ID: revisionID, TenantID: repositoryGuardrailTenant, ResourceVersionID: versionID, RevisionNumber: 1, Representations: []Representation{{ID: versionID + "-representation", Role: RepresentationPrimary, MediaType: mediaType, ProfileID: "test", ContentDigest: digest, Size: int64(len(content)), ArtifactID: documentID, ValidationState: "valid"}}, ContentSetDigest: digestBytes([]byte(versionID)), EvidenceSetID: evidenceID, CreatedAt: createdAt},
			Documents:       []DocumentBlob{document, cloneDocument(document)},
			Evidence:        EvidenceSet{ValidationProfile: currentValidationProfile, LegalRights: LegalRightsSummary{State: LegalRightsNotAssessed}, ID: evidenceID, TenantID: repositoryGuardrailTenant, SubjectDigest: digestBytes([]byte(versionID)), State: EvidenceComplete, CreatedAt: createdAt},
		}
	}

	repository := newRepositoryGuardrailFixture()
	if _, _, _, err := repository.CreateVersion(ctx, params("version-document-one", "2.0.0", "text/plain", now)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repository.CreateVersion(ctx, params("version-document-two", "3.0.0", "text/plain", now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	document, err := repository.GetDocument(ctx, repositoryGuardrailTenant, documentID)
	if err != nil || !document.CreatedAt.Equal(now) || string(document.Content) != string(content) {
		t.Fatalf("deduplicated document = %#v, %v", document, err)
	}

	conflicting := params("version-document-conflict", "4.0.0", "application/octet-stream", now.Add(2*time.Hour))
	if _, _, _, err := repository.CreateVersion(ctx, conflicting); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("conflicting document media type returned %v", err)
	}
	invalid := params("version-document-invalid", "5.0.0", "text/plain", now)
	invalid.Documents[0].Content = []byte("changed")
	invalid.Documents = invalid.Documents[:1]
	if _, _, _, err := repository.CreateVersion(ctx, invalid); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid document bytes returned %v", err)
	}
	invalidText := params("version-document-invalid-text", "5.1.0", "text/plain", now)
	invalidText.Documents = invalidText.Documents[:1]
	invalidText.Documents[0].Content = []byte{0xff}
	invalidText.Documents[0].Digest = digestBytes(invalidText.Documents[0].Content)
	invalidText.Documents[0].ID = strings.TrimPrefix(invalidText.Documents[0].Digest, "sha256:")
	invalidText.Documents[0].Size = 1
	invalidText.VersionRevision.Representations[0].ArtifactID = invalidText.Documents[0].ID
	invalidText.VersionRevision.Representations[0].ContentDigest = invalidText.Documents[0].Digest
	invalidText.VersionRevision.Representations[0].Size = 1
	if _, _, _, err := repository.CreateVersion(ctx, invalidText); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid UTF-8 document returned %v", err)
	}
	unreferenced := params("version-document-unreferenced", "6.0.0", "text/plain", now)
	unreferenced.VersionRevision.Representations[0].ArtifactID = strings.Repeat("a", 64)
	if _, _, _, err := repository.CreateVersion(ctx, unreferenced); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("unreferenced document returned %v", err)
	}
	missing := params("version-document-missing", "6.1.0", "text/plain", now)
	missing.Documents = nil
	if _, _, _, err := repository.CreateVersion(ctx, missing); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("missing document returned %v", err)
	}
	mismatchedReference := params("version-document-mismatched-reference", "7.0.0", "text/plain", now)
	mismatchedReference.VersionRevision.Representations = append(mismatchedReference.VersionRevision.Representations, Representation{ID: "mismatched-reference", Role: RepresentationMetadata, MediaType: "application/json", ContentDigest: digest, Size: int64(len(content)), ArtifactID: documentID, ValidationState: "valid"})
	if _, _, _, err := repository.CreateVersion(ctx, mismatchedReference); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("second mismatched document reference returned %v", err)
	}
	largeContent := []byte(strings.Repeat("x", 1<<20))
	largeDigest := digestBytes(largeContent)
	largeDocumentID := strings.TrimPrefix(largeDigest, "sha256:")
	combinedOversized := params("version-document-combined-oversized", "8.0.0", "text/plain", now)
	combinedOversized.VersionRevision.Representations[0].ArtifactID = largeDocumentID
	combinedOversized.VersionRevision.Representations[0].ContentDigest = largeDigest
	combinedOversized.VersionRevision.Representations[0].Size = int64(len(largeContent))
	largeDocument := DocumentBlob{ID: largeDocumentID, TenantID: repositoryGuardrailTenant, Digest: largeDigest, MediaType: "text/plain", Size: int64(len(largeContent)), Content: largeContent, CreatedAt: now}
	combinedOversized.Documents = make([]DocumentBlob, 17)
	for index := range combinedOversized.Documents {
		combinedOversized.Documents[index] = largeDocument
	}
	if _, _, _, err := repository.CreateVersion(ctx, combinedOversized); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("repository combined oversized documents returned %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repository.GetDocument(canceled, repositoryGuardrailTenant, documentID); err == nil {
		t.Fatal("canceled document read succeeded")
	}
}

func TestMemoryRepositoryRejectsInvalidSkillPackageMetadata(t *testing.T) {
	t.Parallel()
	repository := newRepositoryGuardrailFixture()
	now := time.Date(2026, time.August, 31, 15, 0, 0, 0, time.UTC)
	representation := validSkillPackageRepresentation()
	representation.ID = "package-representation"
	representation.Role = RepresentationPrimary
	representation.ProfileID = "agent-skills-test"
	params := CreateVersionParams{
		Version:         ResourceVersion{ID: "package-version", TenantID: repositoryGuardrailTenant, ResourceID: "resource", Version: "9.0.0", Revision: 1, CreatedAt: now, UpdatedAt: now},
		VersionRevision: VersionRevision{ID: "package-revision", TenantID: repositoryGuardrailTenant, ResourceVersionID: "package-version", RevisionNumber: 1, Representations: []Representation{representation}, EvidenceSetID: "package-evidence", CreatedAt: now},
		Evidence:        EvidenceSet{ValidationProfile: currentValidationProfile, LegalRights: LegalRightsSummary{State: LegalRightsNotAssessed}, ID: "package-evidence", TenantID: repositoryGuardrailTenant, SubjectDigest: digestBytes([]byte("package revision")), State: EvidenceComplete, CreatedAt: now},
	}
	var err error
	params.VersionRevision.ContentSetDigest, err = contentSetDigest(params.VersionRevision.Representations)
	if err != nil {
		t.Fatal(err)
	}
	if _, revision, _, err := repository.CreateVersion(context.Background(), params); err != nil || revision.Representations[0].Package.FileCount != 1 {
		t.Fatalf("valid package persistence = %#v, %v", revision, err)
	}

	repository = newRepositoryGuardrailFixture()
	invalid := params
	invalid.Version.ID = "invalid-package-version"
	invalid.Version.Version = "9.0.1"
	invalid.VersionRevision.ID = "invalid-package-revision"
	invalid.VersionRevision.ResourceVersionID = invalid.Version.ID
	invalid.VersionRevision.Representations = []Representation{cloneSkillPackageRepresentation(representation)}
	invalid.VersionRevision.Representations[0].Package.Files[0].Digest = "sha256:invalid"
	invalid.Evidence.ID = "invalid-package-evidence"
	invalid.VersionRevision.EvidenceSetID = invalid.Evidence.ID
	if _, _, _, err := repository.CreateVersion(context.Background(), invalid); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid package metadata returned %v", err)
	}

	repository = newRepositoryGuardrailFixture()
	invalid = params
	invalid.Version.ID = "unbound-package-version"
	invalid.Version.Version = "9.0.2"
	invalid.VersionRevision.ID = "unbound-package-revision"
	invalid.VersionRevision.ResourceVersionID = invalid.Version.ID
	invalid.VersionRevision.Representations = []Representation{cloneSkillPackageRepresentation(representation)}
	invalid.VersionRevision.Representations[0].Package.Files[0].Digest = "sha256:" + strings.Repeat("d", 64)
	invalid.Evidence.ID = "unbound-package-evidence"
	invalid.VersionRevision.EvidenceSetID = invalid.Evidence.ID
	if _, _, _, err := repository.CreateVersion(context.Background(), invalid); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("unbound valid-looking package inventory returned %v", err)
	}
}

func TestMemoryRepositoryEnforcesReviewAndReleaseIntegrity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, time.August, 30, 13, 0, 0, 0, time.UTC)

	decision := func(repository *MemoryRepository) error {
		_, _, err := repository.DecideSubmission(ctx, DecideSubmissionParams{ActorMembershipID: guardReviewerMember, ActorRealmID: guardRealm, TenantID: repositoryGuardrailTenant, SubmissionID: "submission", ExpectedRevision: 1, ExpectedValidationEvidenceID: "evidence", Actor: "reviewer", Action: "approve", Reason: "reviewed", At: now})
		return err
	}
	decisionTests := []struct {
		name   string
		code   ErrorCode
		mutate func(*MemoryRepository)
	}{
		{name: "missing submission", code: CodeNotFound, mutate: func(repository *MemoryRepository) { delete(repository.submissions, "submission") }},
		{name: "stale submission", code: CodePreconditionFailed, mutate: func(repository *MemoryRepository) {
			submission := repository.submissions["submission"]
			submission.Revision = 2
			repository.submissions[submission.ID] = submission
		}},
		{name: "terminal submission", code: CodeInvalidTransition, mutate: func(repository *MemoryRepository) {
			submission := repository.submissions["submission"]
			submission.State = SubmissionRejected
			repository.submissions[submission.ID] = submission
		}},
		{name: "missing version revision", code: CodeInternal, mutate: func(repository *MemoryRepository) { delete(repository.versionRevisions, "version-revision") }},
		{name: "missing version", code: CodeInternal, mutate: func(repository *MemoryRepository) { delete(repository.versions, "version") }},
		{name: "validation changed before approval", code: CodePreconditionFailed, mutate: func(repository *MemoryRepository) {
			repository.validationRuns["version-revision"] = []string{"evidence", "new-evidence"}
		}},
		{name: "sealed to other content", code: CodeConflict, mutate: func(repository *MemoryRepository) {
			version := repository.versions["version"]
			version.SealedRevisionID = "other-revision"
			repository.versions[version.ID] = version
		}},
	}
	for _, testCase := range decisionTests {
		t.Run(testCase.name, func(t *testing.T) {
			repository := newRepositoryGuardrailFixture()
			testCase.mutate(repository)
			requireCatalogCode(t, decision(repository), testCase.code)
		})
	}

	repository := newRepositoryGuardrailFixture()
	_, _, err := repository.DecideSubmission(ctx, DecideSubmissionParams{ActorMembershipID: guardReviewerMember, ActorRealmID: guardRealm, TenantID: repositoryGuardrailTenant, SubmissionID: "submission", ExpectedRevision: 1, Actor: "author", Action: "approve", At: now})
	requireCatalogCode(t, err, CodeSeparationOfDuties)
	_, _, err = repository.DecideSubmission(ctx, DecideSubmissionParams{ActorMembershipID: guardReviewerMember, ActorRealmID: guardRealm, TenantID: repositoryGuardrailTenant, SubmissionID: "submission", ExpectedRevision: 1, Actor: "reviewer", Action: "unknown", At: now})
	requireCatalogCode(t, err, CodeInvalidArgument)

	for _, action := range []string{"request_changes", "reject"} {
		repository = newRepositoryGuardrailFixture()
		submission, version, decideErr := repository.DecideSubmission(ctx, DecideSubmissionParams{ActorMembershipID: guardReviewerMember, ActorRealmID: guardRealm, TenantID: repositoryGuardrailTenant, SubmissionID: "submission", ExpectedRevision: 1, Actor: "reviewer", Action: action, Reason: "decision", At: now})
		if decideErr != nil || submission.State == SubmissionSubmitted || version.ID != "version" || len(submission.Decisions) != 1 || submission.DecidedAt == nil {
			t.Fatalf("%s decision = %#v, %#v, %v", action, submission, version, decideErr)
		}
	}

	publish := func(repository *MemoryRepository, params PublishParams) error {
		_, _, _, publishErr := repository.Publish(ctx, params)
		return publishErr
	}
	basePublish := PublishParams{TenantID: repositoryGuardrailTenant, ListingID: "listing", SubmissionID: "submission", ExpectedRevision: 1, ExpectedValidationEvidenceID: "evidence", PriorDisposition: ReleaseSuperseded, PriorReason: "replaced", Release: ListingRelease{ID: "release-two", TenantID: repositoryGuardrailTenant, ListingID: "listing", State: ReleasePublished, ActivatedAt: now}}
	publishTests := []struct {
		name   string
		code   ErrorCode
		mutate func(*MemoryRepository, *PublishParams)
	}{
		{name: "missing listing", code: CodeNotFound, mutate: func(_ *MemoryRepository, params *PublishParams) { params.ListingID = "missing" }},
		{name: "stale listing", code: CodePreconditionFailed, mutate: func(repository *MemoryRepository, _ *PublishParams) {
			listing := repository.listings["listing"]
			listing.Revision = 2
			repository.listings[listing.ID] = listing
		}},
		{name: "unapproved submission", code: CodePreconditionFailed, mutate: func(_ *MemoryRepository, _ *PublishParams) {}},
		{name: "validation changed before publication", code: CodePublicationNotReady, mutate: func(repository *MemoryRepository, _ *PublishParams) {
			submission := repository.submissions["submission"]
			submission.State = SubmissionApproved
			repository.submissions[submission.ID] = submission
			repository.validationRuns["version-revision"] = []string{"evidence", "new-evidence"}
		}},
		{name: "invalid current release", code: CodeInternal, mutate: func(repository *MemoryRepository, _ *PublishParams) {
			submission := repository.submissions["submission"]
			submission.State = SubmissionApproved
			repository.submissions[submission.ID] = submission
			delete(repository.releases, "release")
		}},
	}
	for _, testCase := range publishTests {
		t.Run(testCase.name, func(t *testing.T) {
			repository := newRepositoryGuardrailFixture()
			params := basePublish
			testCase.mutate(repository, &params)
			requireCatalogCode(t, publish(repository, params), testCase.code)
		})
	}

	repository = newRepositoryGuardrailFixture()
	submission := repository.submissions["submission"]
	submission.State = SubmissionApproved
	repository.submissions[submission.ID] = submission
	listing, release, prior, err := repository.Publish(ctx, basePublish)
	if err != nil || prior == nil || prior.State != ReleaseSuperseded || prior.ReplacementReleaseID != release.ID || listing.CurrentReleaseID != release.ID {
		t.Fatalf("replacement publication = %#v, %#v, %#v, %v", listing, release, prior, err)
	}

	transitionTests := []struct {
		name   string
		code   ErrorCode
		mutate func(*MemoryRepository, *TransitionReleaseParams)
	}{
		{name: "missing release", code: CodeNotFound, mutate: func(_ *MemoryRepository, params *TransitionReleaseParams) { params.ReleaseID = "missing" }},
		{name: "stale release", code: CodePreconditionFailed, mutate: func(repository *MemoryRepository, _ *TransitionReleaseParams) {
			release := repository.releases["release"]
			release.Revision = 2
			repository.releases[release.ID] = release
		}},
		{name: "invalid transition", code: CodeInvalidTransition, mutate: func(_ *MemoryRepository, params *TransitionReleaseParams) { params.TargetState = ReleasePublished }},
	}
	for _, testCase := range transitionTests {
		t.Run(testCase.name, func(t *testing.T) {
			repository := newRepositoryGuardrailFixture()
			params := TransitionReleaseParams{TenantID: repositoryGuardrailTenant, ReleaseID: "release", ExpectedRevision: 1, TargetState: ReleaseRetired, Reason: "retired", At: now}
			testCase.mutate(repository, &params)
			_, transitionErr := repository.TransitionRelease(ctx, params)
			requireCatalogCode(t, transitionErr, testCase.code)
		})
	}

	repository = newRepositoryGuardrailFixture()
	deprecated, err := repository.TransitionRelease(ctx, TransitionReleaseParams{TenantID: repositoryGuardrailTenant, ReleaseID: "release", ExpectedRevision: 1, TargetState: ReleaseDeprecated, Reason: "deprecated", At: now})
	if err != nil || repository.listings["listing"].CurrentReleaseID != deprecated.ID {
		t.Fatalf("deprecated release = %#v, listing = %#v, %v", deprecated, repository.listings["listing"], err)
	}
	if validReleaseTransition(ReleaseState("unknown"), ReleaseRevoked) || validReleaseTransition(ReleaseRevoked, ReleaseRetired) {
		t.Fatal("unknown or revoked release state allowed a transition")
	}
}

func TestMemoryApprovalPreservesAlreadySealedVersion(t *testing.T) {
	t.Parallel()
	repository := newRepositoryGuardrailFixture()
	sealed := repository.versions["version"]
	sealed.SealedRevisionID = "version-revision"
	repository.versions[sealed.ID] = sealed
	submission, version, err := repository.DecideSubmission(t.Context(), DecideSubmissionParams{ActorMembershipID: guardReviewerMember, ActorRealmID: guardRealm,
		TenantID: repositoryGuardrailTenant, SubmissionID: "submission", ExpectedRevision: 1,
		ExpectedValidationEvidenceID: "evidence", Actor: "reviewer", Action: "approve", Reason: "Review listing metadata",
		At: sealed.UpdatedAt.Add(time.Minute),
	})
	if err != nil || submission.State != SubmissionApproved || version != sealed || repository.versions[sealed.ID] != sealed {
		t.Fatalf("listing approval must preserve its sealed version: submission=%s unchanged=%t err=%v", submission.State, version == sealed, err)
	}
	if submission.Revision != 2 || len(submission.Decisions) != 1 || submission.DecidedAt == nil {
		t.Fatal("listing approval did not retain its independent decision")
	}
}

func TestMemoryRepositoryDoesNotExposeIncompleteCatalogRecords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mutations := []struct {
		name   string
		mutate func(*MemoryRepository)
	}{
		{name: "missing release", mutate: func(repository *MemoryRepository) { delete(repository.releases, "release") }},
		{name: "inactive release", mutate: func(repository *MemoryRepository) {
			release := repository.releases["release"]
			release.State = ReleaseRetired
			repository.releases[release.ID] = release
		}},
		{name: "missing listing revision", mutate: func(repository *MemoryRepository) { delete(repository.listingRevisions, "listing-revision") }},
		{name: "private listing", mutate: func(repository *MemoryRepository) {
			revision := repository.listingRevisions["listing-revision"]
			revision.Visibility = VisibilityPrivate
			repository.listingRevisions[revision.ID] = revision
		}},
		{name: "missing version revision", mutate: func(repository *MemoryRepository) { delete(repository.versionRevisions, "version-revision") }},
		{name: "missing version", mutate: func(repository *MemoryRepository) { delete(repository.versions, "version") }},
		{name: "missing resource", mutate: func(repository *MemoryRepository) { delete(repository.resources, "resource") }},
		{name: "missing evidence", mutate: func(repository *MemoryRepository) { delete(repository.evidenceSets, "evidence") }},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			repository := newRepositoryGuardrailFixture()
			testCase.mutate(repository)
			records := repository.catalogRecordsLocked(repositoryGuardrailTenant, true)
			if len(records) != 0 {
				t.Fatalf("records = %#v", records)
			}
		})
	}

	repository := newRepositoryGuardrailFixture()
	repository.namespaces["namespace-z"] = Namespace{ID: "namespace-z", TenantID: repositoryGuardrailTenant, Slug: "zeta"}
	repository.namespaces["namespace-a"] = Namespace{ID: "namespace-a", TenantID: repositoryGuardrailTenant, Slug: "alpha"}
	namespaces, err := repository.ListNamespaces(ctx, repositoryGuardrailTenant, NamespaceListRequest{Limit: 100})
	if err != nil || len(namespaces) != 3 || namespaces[0].Slug != "alpha" || namespaces[2].Slug != "zeta" {
		t.Fatalf("namespaces = %#v, %v", namespaces, err)
	}
	firstNamespaces, err := repository.ListNamespaces(ctx, repositoryGuardrailTenant, NamespaceListRequest{Limit: 2})
	if err != nil || len(firstNamespaces) != 2 || firstNamespaces[0].Slug != "alpha" || firstNamespaces[1].Slug != "default" {
		t.Fatalf("first namespace page = %#v, %v", firstNamespaces, err)
	}
	secondNamespaces, err := repository.ListNamespaces(ctx, repositoryGuardrailTenant, NamespaceListRequest{AfterSlug: firstNamespaces[1].Slug, AfterID: firstNamespaces[1].ID, Limit: 2})
	if err != nil || len(secondNamespaces) != 1 || secondNamespaces[0].Slug != "zeta" {
		t.Fatalf("second namespace page = %#v, %v", secondNamespaces, err)
	}
	if final, err := repository.ListNamespaces(ctx, repositoryGuardrailTenant, NamespaceListRequest{AfterSlug: secondNamespaces[0].Slug, AfterID: secondNamespaces[0].ID, Limit: 2}); err != nil || len(final) != 0 {
		t.Fatalf("final namespace page = %#v, %v", final, err)
	}
	if _, err := repository.GetNamespace(ctx, "tenant-two", "namespace"); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("cross-tenant exact namespace read returned %v", err)
	}
	if _, err := repository.ApprovePublisherPublicationDomain(ctx, PublisherDomainApproval{TenantID: repositoryGuardrailTenant, PublisherID: "missing", Domain: "catalog.example.com", ExpectedRevision: 1}); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("missing publisher publication-domain approval returned %v", err)
	}

	tenantRepository := NewMemoryRepository()
	tenantRepository.publishers["other"] = Publisher{ID: "other", TenantID: "tenant-two", Domains: []string{"shared.example.com"}}
	if _, err := tenantRepository.CreatePublisher(ctx, Publisher{ID: "local", TenantID: repositoryGuardrailTenant, Domains: []string{"shared.example.com"}}, AuditEvent{}, OutboxEvent{}); err != nil {
		t.Fatalf("cross-tenant publisher domain caused a conflict: %v", err)
	}

	repository.publishers["publisher-b"] = Publisher{ID: "publisher-b", TenantID: repositoryGuardrailTenant, DisplayName: "Publisher"}
	repository.publishers["publisher-a"] = Publisher{ID: "publisher-a", TenantID: repositoryGuardrailTenant, DisplayName: "Another"}
	publishers, err := repository.ListPublishers(ctx, repositoryGuardrailTenant)
	if err != nil || len(publishers) != 3 || publishers[0].ID != "publisher-a" || publishers[1].ID != "publisher" || publishers[2].ID != "publisher-b" {
		t.Fatalf("publishers = %#v, %v", publishers, err)
	}
	firstPublishers, err := repository.ListPublisherPage(ctx, repositoryGuardrailTenant, PublisherListRequest{Limit: 2})
	if err != nil || len(firstPublishers) != 2 || firstPublishers[0].ID != "publisher-a" || firstPublishers[1].ID != "publisher" {
		t.Fatalf("first publisher page = %#v, %v", firstPublishers, err)
	}
	secondPublishers, err := repository.ListPublisherPage(ctx, repositoryGuardrailTenant, PublisherListRequest{AfterDisplayName: firstPublishers[1].DisplayName, AfterID: firstPublishers[1].ID, Limit: 2})
	if err != nil || len(secondPublishers) != 1 || secondPublishers[0].ID != "publisher-b" {
		t.Fatalf("second publisher page = %#v, %v", secondPublishers, err)
	}
	if final, err := repository.ListPublisherPage(ctx, repositoryGuardrailTenant, PublisherListRequest{AfterDisplayName: secondPublishers[0].DisplayName, AfterID: secondPublishers[0].ID, Limit: 2}); err != nil || len(final) != 0 {
		t.Fatalf("final publisher page = %#v, %v", final, err)
	}
	if _, err := repository.GetPublisher(ctx, "tenant-two", "publisher"); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("cross-tenant exact publisher read returned %v", err)
	}

	now := repository.resources["resource"].UpdatedAt
	repository.resources["resource-b"] = Resource{ID: "resource-b", TenantID: repositoryGuardrailTenant, NamespaceID: "namespace", Kind: KindAgent, UpdatedAt: now}
	repository.resources["resource-new"] = Resource{ID: "resource-new", TenantID: repositoryGuardrailTenant, NamespaceID: "namespace", Kind: KindSkill, UpdatedAt: now.Add(time.Minute)}
	resources, err := repository.ListResources(ctx, repositoryGuardrailTenant, ResourceListRequest{Limit: 100})
	if err != nil || len(resources) != 3 || resources[0].ID != "resource-new" || resources[1].ID != "resource-b" {
		t.Fatalf("resources = %#v, %v", resources, err)
	}
	firstResources, err := repository.ListResources(ctx, repositoryGuardrailTenant, ResourceListRequest{Limit: 2})
	if err != nil || len(firstResources) != 2 || firstResources[0].ID != "resource-new" || firstResources[1].ID != "resource-b" {
		t.Fatalf("first Resource page = %#v, %v", firstResources, err)
	}
	secondResources, err := repository.ListResources(ctx, repositoryGuardrailTenant, ResourceListRequest{AfterUpdatedAt: &firstResources[1].UpdatedAt, AfterID: firstResources[1].ID, Limit: 2})
	if err != nil || len(secondResources) != 1 || secondResources[0].ID != "resource" {
		t.Fatalf("second Resource page = %#v, %v", secondResources, err)
	}
	if final, err := repository.ListResources(ctx, repositoryGuardrailTenant, ResourceListRequest{AfterUpdatedAt: &secondResources[0].UpdatedAt, AfterID: secondResources[0].ID, Limit: 2}); err != nil || len(final) != 0 {
		t.Fatalf("final Resource page = %#v, %v", final, err)
	}
	agents, err := repository.ListResources(ctx, repositoryGuardrailTenant, ResourceListRequest{Kind: KindAgent, Limit: 100})
	if err != nil || len(agents) != 2 {
		t.Fatalf("agents = %#v, %v", agents, err)
	}

	repository.versionRevisions["version-revision"] = VersionRevision{ID: "version-revision", TenantID: repositoryGuardrailTenant, EvidenceSetID: "missing"}
	if _, _, err := repository.GetVersionRevision(ctx, repositoryGuardrailTenant, "version-revision"); ErrorCodeOf(err) != CodeInternal {
		t.Fatalf("missing evidence returned %v", err)
	}
	if err := repository.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryPublicationProjectionIsFilteredAndByteBounded(t *testing.T) {
	repository := newRepositoryGuardrailFixture()
	publisher := repository.publishers["publisher"]
	publisher.ApprovedPublicationDomains = []string{"catalog.example.com"}
	repository.publishers[publisher.ID] = publisher
	listingRevision := repository.listingRevisions["listing-revision"]
	listingRevision.PublisherID = publisher.ID
	repository.listingRevisions[listingRevision.ID] = listingRevision

	page, err := repository.PublicationRecords(context.Background(), repositoryGuardrailTenant, PublicationRecordRequest{Limit: 1})
	if err != nil || page.Overflow || len(page.Records) != 1 || page.Records[0].Release.ID != "release" {
		t.Fatalf("publication projection = %#v, %v", page, err)
	}
	if other, err := repository.PublicationRecords(context.Background(), "tenant-two", PublicationRecordRequest{Limit: 1}); err != nil || len(other.Records) != 0 || other.Overflow {
		t.Fatalf("cross-tenant publication projection = %#v, %v", other, err)
	}
	if _, err := repository.PublicationRecords(context.Background(), repositoryGuardrailTenant, PublicationRecordRequest{Limit: MaxPublicationRecords + 1}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("oversized publication selection = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.PublicationRecords(cancelled, repositoryGuardrailTenant, PublicationRecordRequest{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled publication selection = %v", err)
	}

	resource := repository.resources["resource"]
	resource.CanonicalIdentifier = "not-an-air-identifier"
	repository.resources[resource.ID] = resource
	if page, err := repository.PublicationRecords(context.Background(), repositoryGuardrailTenant, PublicationRecordRequest{Limit: 1}); err != nil || len(page.Records) != 0 {
		t.Fatalf("invalid-identifier publication projection = %#v, %v", page, err)
	}
	resource.CanonicalIdentifier = "urn:air:catalog.example.com:default:resource"
	repository.resources[resource.ID] = resource
	listingRevision.PublisherID = "missing-publisher"
	repository.listingRevisions[listingRevision.ID] = listingRevision
	if page, err := repository.PublicationRecords(context.Background(), repositoryGuardrailTenant, PublicationRecordRequest{Limit: 1}); err != nil || len(page.Records) != 0 {
		t.Fatalf("missing-publisher publication projection = %#v, %v", page, err)
	}
	listingRevision.PublisherID = publisher.ID
	repository.listingRevisions[listingRevision.ID] = listingRevision

	repository.releases["release-two"] = ListingRelease{ID: "release-two", TenantID: repositoryGuardrailTenant, VersionRevisionID: "version-revision", ListingRevisionID: "listing-revision", State: ReleaseDeprecated}
	repository.listings["listing-two"] = CatalogListing{ID: "listing-two", TenantID: repositoryGuardrailTenant, CurrentReleaseID: "release-two", ResourceID: "resource"}
	if page, err := repository.PublicationRecords(context.Background(), repositoryGuardrailTenant, PublicationRecordRequest{Limit: 1}); err != nil || !page.Overflow || page.Records != nil {
		t.Fatalf("publication record overflow = %#v, %v", page, err)
	}
	ids, overflow, err := repository.PublishedReleaseIDsByVersionRevision(context.Background(), repositoryGuardrailTenant, "version-revision", 2)
	if err != nil || overflow || len(ids) != 2 || ids[0] != "release" || ids[1] != "release-two" {
		t.Fatalf("affected release projection = %#v, %t, %v", ids, overflow, err)
	}
	if ids, overflow, err := repository.PublishedReleaseIDsByVersionRevision(context.Background(), repositoryGuardrailTenant, "version-revision", 1); err != nil || !overflow || ids != nil {
		t.Fatalf("affected release overflow = %#v, %t, %v", ids, overflow, err)
	}
	if _, _, err := repository.PublishedReleaseIDsByVersionRevision(context.Background(), repositoryGuardrailTenant, "version-revision", 0); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid affected release limit = %v", err)
	}
	if _, _, err := repository.PublishedReleaseIDsByVersionRevision(cancelled, repositoryGuardrailTenant, "version-revision", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled affected release lookup = %v", err)
	}
	delete(repository.listings, "listing-two")
	delete(repository.releases, "release-two")

	revision := repository.versionRevisions["version-revision"]
	revision.Representations = []Representation{{Data: json.RawMessage(`"` + strings.Repeat("x", MaxPublicationProjectionBytes) + `"`)}}
	repository.versionRevisions[revision.ID] = revision
	if _, err := repository.PublicationRecords(context.Background(), repositoryGuardrailTenant, PublicationRecordRequest{Limit: 1}); ErrorCodeOf(err) != CodeWorkLimitExceeded {
		t.Fatalf("oversized publication projection = %v", err)
	}
}

func TestMemoryCatalogDetailAndPublicationOwnRetainedData(t *testing.T) {
	for _, boundary := range []string{"detail", "publication"} {
		t.Run(boundary, func(t *testing.T) {
			repository := newRepositoryGuardrailFixture()
			publisher := repository.publishers["publisher"]
			publisher.ApprovedPublicationDomains = []string{"catalog.example.com"}
			repository.publishers[publisher.ID] = publisher
			listing := repository.listingRevisions["listing-revision"]
			listing.PublisherID = publisher.ID
			listing.Tags = []string{"original-tag"}
			listing.Extensions = map[string]any{"private": map[string]any{"value": "original-extension"}}
			repository.listingRevisions[listing.ID] = listing
			revision := repository.versionRevisions["version-revision"]
			revision.Representations = []Representation{{ID: "representation", Data: json.RawMessage(`{"name":"original-document"}`)}}
			repository.versionRevisions[revision.ID] = revision
			evidence := repository.evidenceSets["evidence"]
			evidence.ID = "latest-evidence"
			evidence.Findings = []Finding{{Code: "CHECKED", Message: "original-finding"}}
			repository.evidenceSets[evidence.ID] = evidence
			repository.validationRuns[revision.ID] = []string{"evidence", evidence.ID}
			read := func() CatalogRecord {
				t.Helper()
				if boundary == "detail" {
					value, err := repository.GetCatalogRecord(t.Context(), repositoryGuardrailTenant, "release")
					if err != nil {
						t.Fatal(err)
					}
					return value
				}
				page, err := repository.PublicationRecords(t.Context(), repositoryGuardrailTenant, PublicationRecordRequest{Limit: 1})
				if err != nil || page.Overflow || len(page.Records) != 1 {
					t.Fatalf("publication read: %+v, %v", page, err)
				}
				return page.Records[0]
			}
			assertRetained := func(value CatalogRecord) {
				t.Helper()
				if value.Evidence.ID != "latest-evidence" || value.VersionRevision.EvidenceSetID != value.Evidence.ID || value.Release.ID != "release" || len(value.VersionRevision.Representations) != 1 || len(value.Evidence.Findings) != 1 || len(value.ListingRevision.Tags) != 1 {
					t.Fatalf("full current record or latest validation missing: %+v", value)
				}
				extension, ok := value.ListingRevision.Extensions["private"].(map[string]any)
				if !ok || extension["value"] != "original-extension" {
					t.Fatal("nested extension changed or missing")
				}
				if string(value.VersionRevision.Representations[0].Data) != `{"name":"original-document"}` || value.Evidence.Findings[0].Message != "original-finding" || value.ListingRevision.Tags[0] != "original-tag" {
					t.Fatal("caller mutation changed retained record data")
				}
			}
			value := read()
			assertRetained(value)
			value.VersionRevision.Representations[0].Data[0] = '['
			value.Evidence.Findings[0].Message = "changed"
			value.ListingRevision.Tags[0] = "changed"
			extension, ok := value.ListingRevision.Extensions["private"].(map[string]any)
			if !ok {
				t.Fatal("nested extension missing")
			}
			extension["value"] = "changed"
			assertRetained(read())
			if repository.versionRevisions[revision.ID].EvidenceSetID != "evidence" {
				t.Fatal("reading latest validation rewrote immutable revision evidence")
			}
		})
	}
}

func TestMemoryRepositoryRejectsExpiredOrUnownedOutboxChanges(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 30, 14, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	repository := NewMemoryRepository()
	repository.outbox = append(repository.outbox, OutboxEvent{ID: "event", TenantID: "tenant", LeaseOwner: "worker", LeaseExpiresAt: &expires})

	if err := repository.MarkOutboxPublished(context.Background(), "tenant", "event", "worker", expires); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("expired publication returned %v", err)
	}
	if err := repository.RenewOutbox(context.Background(), "tenant", "event", "worker", expires, time.Minute); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("expired renewal returned %v", err)
	}
	if err := repository.ReleaseOutbox(context.Background(), "tenant", "event", "other"); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("unowned release returned %v", err)
	}
}
