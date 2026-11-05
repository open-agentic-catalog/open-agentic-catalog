package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const guardrailTenantID = "00000000-0000-7000-8000-000000000099"

type guardrailClock struct{ now time.Time }

func (c *guardrailClock) Now() time.Time { return c.now }

type guardrailIDs struct{ next int }

func (g *guardrailIDs) New(time.Time) (string, error) {
	g.next++
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", g.next), nil
}

type guardrailValidator func(context.Context, Kind, RepresentationInput) ValidationResult

func (f guardrailValidator) Validate(ctx context.Context, kind Kind, input RepresentationInput) ValidationResult {
	return f(ctx, kind, input)
}

type guardrailExternalFetcher struct {
	policyDigest string
	at           time.Time
	body         []byte
	mediaType    string
	finalURL     string
	status       int
	err          error
	requests     []ExternalRepresentationFetchRequest
}

func (f *guardrailExternalFetcher) PolicyDigest() string { return f.policyDigest }

func (f *guardrailExternalFetcher) FetchExternalRepresentation(_ context.Context, request ExternalRepresentationFetchRequest) (ExternalRepresentationFetchResult, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return ExternalRepresentationFetchResult{}, f.err
	}
	body := f.body
	if body == nil {
		body = []byte(`{}`)
	}
	mediaType := f.mediaType
	if mediaType == "" {
		mediaType, _ = representationBaseMediaType(request.MediaType)
	}
	status := f.status
	if status == 0 {
		status = 200
	}
	finalURL := f.finalURL
	if finalURL == "" {
		finalURL = request.URL
	}
	return ExternalRepresentationFetchResult{RequestedURL: request.URL, FinalURL: finalURL, Status: status, MediaType: mediaType, Body: append([]byte(nil), body...), ContentDigest: digestBytes(body), PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: f.policyDigest, RetrievedAt: f.at}, nil
}

type guardrailFixture struct {
	service *Service
	repo    *MemoryRepository
	clock   *guardrailClock
	fetcher *guardrailExternalFetcher
	admin   identity.Actor
	reader  identity.Actor
}

func (f guardrailFixture) adminContext() context.Context {
	return identityfixture.WithActor(context.Background(), f.admin)
}

func (f guardrailFixture) readerContext() context.Context {
	return identityfixture.WithActor(context.Background(), f.reader)
}

func newGuardrailFixture(t *testing.T) guardrailFixture {
	t.Helper()
	return newGuardrailFixtureWithOutboxMode(t, false)
}

func newGuardrailFixtureWithOutboxMode(t *testing.T, acknowledgeAtCreation bool) guardrailFixture {
	t.Helper()
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	repo := NewMemoryRepository()
	clock := &guardrailClock{now: now}
	externalFetcher := &guardrailExternalFetcher{policyDigest: "sha256:" + strings.Repeat("a", 64), at: now}
	service, err := NewService(Options{
		Repository: repo,
		Clock:      clock,
		IDs:        &guardrailIDs{},
		Validator: guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult {
			return ValidationResult{ProfileID: "test-profile", NormalizedFacts: map[string]any{}, Unknown: map[string]any{}}
		}),
		ExternalFetcher:             externalFetcher,
		CursorKey:                   []byte("01234567890123456789012345678901"),
		Policy:                      "test-policy",
		AcknowledgeOutboxAtCreation: acknowledgeAtCreation,
	})
	if err != nil {
		t.Fatal(err)
	}
	adminActor := identity.Actor{
		TenantID:     guardrailTenantID,
		Subject:      "admin",
		RealmID:      "00000000-0000-7000-8000-000000000090",
		MembershipID: "00000000-0000-7000-8000-000000000091",
		ActorType:    identity.ActorHuman,
		Roles: []identity.Role{
			identity.RoleTenantAdmin,
			identity.RoleContributor,
			identity.RoleReviewer,
			identity.RolePublisher,
			identity.RoleReader,
			identity.RoleSecurityReviewer,
		},
	}
	readerActor := identity.Actor{TenantID: guardrailTenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}, RealmID: adminActor.RealmID, MembershipID: "00000000-0000-7000-8000-000000000092", ActorType: identity.ActorHuman}
	return guardrailFixture{
		service: service,
		repo:    repo,
		clock:   clock,
		fetcher: externalFetcher,
		admin:   adminActor,
		reader:  readerActor,
	}
}

func TestSensitiveCatalogReadsFailClosedWhenAuditIsUnavailable(t *testing.T) {
	fixture := newGuardrailFixture(t)
	ctx := fixture.adminContext()
	namespace, err := fixture.service.CreateNamespace(ctx, CreateNamespaceInput{Slug: "audit-read", DisplayName: "Audit read"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := fixture.service.CreateResource(ctx, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:audit:read", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	_, revision, bound, err := fixture.service.CreateVersion(ctx, resource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "text/plain", Content: "audit-read-document"}}})
	if err != nil {
		t.Fatal(err)
	}
	documentID := revision.Representations[0].ArtifactID
	const submissionID = "00000000-0000-7000-8000-000000000080"
	listing, err := fixture.service.CreateListing(ctx, CreateListingInput{ResourceID: resource.ID, NamespaceID: namespace.ID})
	if err != nil {
		t.Fatal(err)
	}
	fixture.repo.submissions[submissionID] = ReviewSubmission{ID: submissionID, TenantID: guardrailTenantID, ListingID: listing.ID, VersionRevisionID: revision.ID, EvidenceSetID: bound.ID, EvidenceDigest: bound.Digest, PolicyVersion: bound.PolicyVersion}
	failingRepository := &auditAppendFailureRepository{Repository: fixture.repo, err: errors.New("audit unavailable")}
	service, err := NewService(Options{
		Repository: failingRepository,
		Clock:      fixture.clock,
		IDs:        &guardrailIDs{},
		Validator: guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult {
			return ValidationResult{ProfileID: "test-profile", NormalizedFacts: map[string]any{}, Unknown: map[string]any{}}
		}),
		CursorKey: []byte("01234567890123456789012345678901"),
		Policy:    "test-policy",
	})
	if err != nil {
		t.Fatal(err)
	}

	if loaded, evidence, namespace, err := service.GetVersionRevision(fixture.readerContext(), revision.ID); ErrorCodeOf(err) != CodeDependencyUnavailable || loaded.ID != "" || evidence.ID != "" || namespace != "" {
		t.Fatalf("version-revision read disclosed data after audit failure: %#v / %#v / %v", loaded, evidence, err)
	}
	if artifact, content, err := service.GetArtifact(fixture.readerContext(), documentID); ErrorCodeOf(err) != CodeDependencyUnavailable || artifact.ID != "" || content != nil {
		t.Fatalf("Catalog-document read disclosed data after audit failure: %#v / %q / %v", artifact, content, err)
	}
	if evidence, err := service.GetSubmissionEvidence(fixture.readerContext(), submissionID); ErrorCodeOf(err) != CodeDependencyUnavailable || evidence.ID != "" {
		t.Fatalf("submitted evidence disclosed after audit failure: %#v / %v", evidence, err)
	}
	// A later validation changes the latest evidence, not the submitted binding.
	if _, latest, _, err := fixture.service.RevalidateVersionRevision(ctx, revision.ID); err != nil || latest.ID == bound.ID {
		t.Fatalf("later validation: %#v / %v", latest, err)
	}
	if evidence, err := fixture.service.GetSubmissionEvidence(fixture.readerContext(), submissionID); err != nil || evidence.ID != bound.ID || evidence.Digest != bound.Digest {
		t.Fatalf("historical submitted evidence: %#v / %v", evidence, err)
	}
	broken := fixture.repo.submissions[submissionID]
	broken.EvidenceDigest = "sha256:" + strings.Repeat("0", 64)
	fixture.repo.submissions[submissionID] = broken
	if evidence, err := fixture.service.GetSubmissionEvidence(fixture.readerContext(), submissionID); ErrorCodeOf(err) != CodeConflict || evidence.ID != "" {
		t.Fatalf("corrupt submission binding disclosed: %#v / %v", evidence, err)
	}
	if artifact, content, err := service.GetPublicArtifact(context.Background(), guardrailTenantID, documentID); err != nil || artifact.ID != documentID || string(content) != "audit-read-document" {
		t.Fatalf("public document read incorrectly required private-read audit: %#v / %q / %v", artifact, content, err)
	}
}

func TestCatalogServiceCanAcknowledgeOutboxInTheOriginatingTransaction(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name               string
		acknowledge        bool
		wantPendingOutbox  int
		wantPublishedAtSet bool
	}{
		{name: "delivery enabled", wantPendingOutbox: 1},
		{name: "delivery disabled", acknowledge: true, wantPublishedAtSet: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newGuardrailFixtureWithOutboxMode(t, test.acknowledge)
			fixture.clock.now = fixture.clock.now.Add(789 * time.Nanosecond)
			if _, err := fixture.service.CreateNamespace(fixture.adminContext(), CreateNamespaceInput{Slug: "catalog", DisplayName: "Catalog"}); err != nil {
				t.Fatal(err)
			}
			if len(fixture.repo.audit) != 1 || len(fixture.repo.outbox) != 1 {
				t.Fatalf("evidence counts = audit %d, outbox %d", len(fixture.repo.audit), len(fixture.repo.outbox))
			}
			auditAt := fixture.repo.audit[0].At
			outboxAt := fixture.repo.outbox[0].CreatedAt
			if !outboxAt.Equal(auditAt) || auditAt.Nanosecond()%1000 != 0 {
				t.Fatalf("evidence times = audit %v, outbox %v", auditAt, outboxAt)
			}
			publishedAt := fixture.repo.outbox[0].PublishedAt
			if (publishedAt != nil) != test.wantPublishedAtSet {
				t.Fatalf("publishedAt = %v, want set %t", publishedAt, test.wantPublishedAtSet)
			}
			if publishedAt != nil && !publishedAt.Equal(auditAt) {
				t.Fatalf("publishedAt = %v, want %v", publishedAt, auditAt)
			}
			pending, err := fixture.repo.ClaimOutbox(context.Background(), "worker", fixture.clock.Now(), time.Minute, 10)
			if err != nil || len(pending) != test.wantPendingOutbox {
				t.Fatalf("pending outbox = %d, %v; want %d", len(pending), err, test.wantPendingOutbox)
			}
		})
	}
}

func TestHTTPSRepresentationsBindExactBytesRenewAndRequireRevisionOnDrift(t *testing.T) {
	t.Parallel()

	fixture := newGuardrailFixture(t)
	ctx := fixture.adminContext()
	namespace, err := fixture.service.CreateNamespace(ctx, CreateNamespaceInput{Slug: "external", DisplayName: "External"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := fixture.service.CreateResource(ctx, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:external:agent", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	url := "https://content.example.com/agent.json"
	original := []byte(`{"name":"original"}`)
	fixture.fetcher.body = original
	version, revision, evidence, err := fixture.service.CreateVersion(ctx, resource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", URL: url}}})
	if err != nil {
		t.Fatal(err)
	}
	representation := revision.Representations[0]
	if evidence.State != EvidenceComplete || evidence.ValidationProfile != currentValidationProfile || len(evidence.ExternalRetrievals) != 1 || evidence.ExpiresAt.Sub(evidence.ExternalRetrievals[0].RetrievedAt) != ExternalRepresentationFreshness || representation.URL != url || representation.ArtifactID == "" || representation.ContentDigest != digestBytes(original) || len(representation.Data) != 0 {
		t.Fatalf("HTTPS revision = %#v / %#v", revision, evidence)
	}
	if retrieval := evidence.ExternalRetrievals[0]; retrieval.RepresentationID != representation.ID || retrieval.RequestedURL != url || retrieval.FinalURL != url || retrieval.Status != 200 || retrieval.MediaType != "application/json" || retrieval.ContentDigest != representation.ContentDigest || retrieval.Size != representation.Size || retrieval.PolicyDigest != fixture.fetcher.PolicyDigest() {
		t.Fatalf("retrieval evidence = %#v", retrieval)
	}
	document, err := fixture.repo.GetDocument(context.Background(), guardrailTenantID, representation.ArtifactID)
	if err != nil || !bytes.Equal(document.Content, original) {
		t.Fatalf("retained external bytes = %q, %v", document.Content, err)
	}
	if !fixture.service.validationEvidenceCurrentForRevision(revision, evidence, fixture.clock.Now()) {
		t.Fatal("new external evidence was not current")
	}
	publicationVerification, err := fixture.service.validateExternalPublication(ctx, guardrailTenantID, revision)
	if err != nil || len(publicationVerification) != 1 || publicationVerification[0].ContentDigest != representation.ContentDigest {
		t.Fatalf("publication-time external verification = %#v, %v", publicationVerification, err)
	}
	documentKey := guardrailTenantID + "/" + representation.ArtifactID
	storedDocument := fixture.repo.documents[documentKey]
	delete(fixture.repo.documents, documentKey)
	if _, err := fixture.service.validateExternalPublication(ctx, guardrailTenantID, revision); ErrorCodeOf(err) != CodePublicationNotReady {
		t.Fatalf("publication without retained external bytes returned %v", err)
	}
	fixture.repo.documents[documentKey] = storedDocument
	fixture.fetcher.err = errors.New("safe route unavailable")
	if _, err := fixture.service.validateExternalPublication(ctx, guardrailTenantID, revision); ErrorCodeOf(err) != CodeDependencyUnavailable {
		t.Fatalf("publication with unavailable external origin returned %v", err)
	}
	fixture.fetcher.err = nil
	fixture.fetcher.body = []byte(`{"name":"publication drift"}`)
	if _, err := fixture.service.validateExternalPublication(ctx, guardrailTenantID, revision); ErrorCodeOf(err) != CodePublicationNotReady {
		t.Fatalf("publication with external byte drift returned %v", err)
	}
	fixture.fetcher.body = original

	fixture.clock.now = fixture.clock.now.Add(ExternalRepresentationFreshness + time.Second)
	if fixture.service.validationEvidenceCurrentForRevision(revision, evidence, fixture.clock.Now()) {
		t.Fatal("expired external evidence remained current")
	}
	fixture.fetcher.at = fixture.clock.Now()
	validatedRevision, renewed, _, err := fixture.service.RevalidateVersionRevision(ctx, revision.ID)
	if err != nil || renewed.State != EvidenceComplete || renewed.ID == evidence.ID || validatedRevision.ContentSetDigest != revision.ContentSetDigest || !renewed.ExpiresAt.Equal(fixture.clock.Now().Add(ExternalRepresentationFreshness)) {
		t.Fatalf("renewed external evidence = %#v / %#v, %v", validatedRevision, renewed, err)
	}

	changed := []byte(`{"name":"changed"}`)
	fixture.clock.now = fixture.clock.now.Add(time.Minute)
	fixture.fetcher.at = fixture.clock.Now()
	fixture.fetcher.body = changed
	_, drifted, _, err := fixture.service.RevalidateVersionRevision(ctx, revision.ID)
	if err != nil || drifted.State != EvidenceBlocked || !hasFinding(drifted.Findings, "EXTERNAL_CONTENT_CHANGED") {
		t.Fatalf("drift evidence = %#v, %v", drifted, err)
	}
	document, err = fixture.repo.GetDocument(context.Background(), guardrailTenantID, representation.ArtifactID)
	if err != nil || !bytes.Equal(document.Content, original) {
		t.Fatalf("byte drift rewrote retained evidence: %q, %v", document.Content, err)
	}
	updated, changedRevision, changedEvidence, err := fixture.service.CreateVersionRevision(ctx, version.ID, []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", URL: url}}, version.Revision)
	if err != nil || updated.Revision != version.Revision+1 || changedEvidence.State != EvidenceComplete || changedRevision.ContentSetDigest == revision.ContentSetDigest || changedRevision.Representations[0].ContentDigest != digestBytes(changed) {
		t.Fatalf("replacement revision = %#v / %#v / %#v, %v", updated, changedRevision, changedEvidence, err)
	}
}

func TestHTTPSRepresentationFailuresAreBoundedAndFailClosed(t *testing.T) {
	t.Parallel()

	fixture := newGuardrailFixture(t)
	ctx := fixture.adminContext()
	namespace, err := fixture.service.CreateNamespace(ctx, CreateNamespaceInput{Slug: "external-failures", DisplayName: "External failures"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := fixture.service.CreateResource(ctx, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:external:failures", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	url := "https://content.example.com/agent.json"
	fixture.fetcher.status = 404
	_, blockedRevision, blockedEvidence, err := fixture.service.CreateVersion(ctx, resource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", URL: url}}})
	if err != nil || blockedEvidence.State != EvidenceBlocked || !hasFinding(blockedEvidence.Findings, "EXTERNAL_HTTP_STATUS") || len(blockedEvidence.ExternalRetrievals) != 1 || blockedRevision.Representations[0].ArtifactID == "" || blockedRevision.Representations[0].ValidationState != "valid" || hasFinding(blockedRevision.Representations[0].Findings, "EXTERNAL_HTTP_STATUS") || hasFinding(blockedRevision.Findings, "EXTERNAL_HTTP_STATUS") {
		t.Fatalf("non-200 external response = %#v / %#v, %v", blockedRevision, blockedEvidence, err)
	}

	fixture.fetcher.status = 200
	fixture.clock.now = fixture.clock.now.Add(time.Minute)
	fixture.fetcher.at = fixture.clock.Now()
	_, recoveredRun, _, err := fixture.service.RevalidateVersionRevision(ctx, blockedRevision.ID)
	if err != nil || recoveredRun.State != EvidenceComplete || len(recoveredRun.Findings) != 0 {
		t.Fatalf("status-only external recovery = %#v, %v", recoveredRun, err)
	}
	fixture.fetcher.err = errors.New("redirect denied")
	_, failedRun, _, err := fixture.service.RevalidateVersionRevision(ctx, blockedRevision.ID)
	if err != nil || failedRun.State != EvidenceBlocked || !hasFinding(failedRun.Findings, "EXTERNAL_RETRIEVAL_FAILED") || len(failedRun.ExternalRetrievals) != 0 {
		t.Fatalf("failed retrieval run = %#v, %v", failedRun, err)
	}
	fixture.fetcher.err = nil
	fixture.fetcher.finalURL = "https://other.example.com/agent.json"
	otherResource, err := fixture.service.CreateResource(ctx, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:external:cross-origin", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.service.CreateVersion(ctx, otherResource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", URL: url}}}); ErrorCodeOf(err) != CodeDependencyUnavailable {
		t.Fatalf("cross-origin retrieval returned %v", err)
	}
	fixture.fetcher.finalURL = ""
	fixture.fetcher.mediaType = "text/plain"
	mediaResource, err := fixture.service.CreateResource(ctx, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:external:media", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	_, mediaRevision, mediaEvidence, err := fixture.service.CreateVersion(ctx, mediaResource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", URL: url}}})
	if err != nil || mediaEvidence.State != EvidenceBlocked || !hasFinding(mediaEvidence.Findings, "EXTERNAL_MEDIA_TYPE_MISMATCH") || mediaRevision.Representations[0].ValidationState != "valid" || hasFinding(mediaRevision.Findings, "EXTERNAL_MEDIA_TYPE_MISMATCH") {
		t.Fatalf("media mismatch evidence = %#v / %#v, %v", mediaRevision, mediaEvidence, err)
	}
	fixture.fetcher.mediaType = "application/json"
	fixture.clock.now = fixture.clock.now.Add(time.Minute)
	fixture.fetcher.at = fixture.clock.Now()
	_, recoveredRun, _, err = fixture.service.RevalidateVersionRevision(ctx, mediaRevision.ID)
	if err != nil || recoveredRun.State != EvidenceComplete || len(recoveredRun.Findings) != 0 {
		t.Fatalf("media-only external recovery = %#v, %v", recoveredRun, err)
	}
	fixture.fetcher.body = []byte{0xff}
	fixture.clock.now = fixture.clock.now.Add(time.Minute)
	fixture.fetcher.at = fixture.clock.Now()
	_, invalidRun, _, err := fixture.service.RevalidateVersionRevision(ctx, mediaRevision.ID)
	if err != nil || invalidRun.State != EvidenceBlocked || !hasFinding(invalidRun.Findings, "EXTERNAL_CONTENT_CHANGED") || !hasFinding(invalidRun.Findings, "EXTERNAL_CONTENT_INVALID") || hasFinding(invalidRun.Findings, "NORMALIZED_FACTS_CHANGED") || hasFinding(invalidRun.Findings, "REPRESENTATION_VALIDATION_STATE_CHANGED") {
		t.Fatalf("invalid external bytes = %#v, %v", invalidRun, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := fixture.service.fetchExternalRepresentation(canceled, guardrailTenantID, url, "application/json", 1024); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled external retrieval returned %v", err)
	}
}

func hasFinding(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func requireCatalogCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	if ErrorCodeOf(err) != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func TestCatalogServiceConstructionAndAuthorizationGuardrails(t *testing.T) {
	t.Parallel()

	fixture := newGuardrailFixture(t)
	base := Options{
		Repository: fixture.repo,
		Clock:      fixture.clock,
		IDs:        &guardrailIDs{},
		Validator:  guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult { return ValidationResult{} }),
		CursorKey:  []byte("01234567890123456789012345678901"),
	}
	for name, remove := range map[string]func(*Options){
		"repository": func(options *Options) { options.Repository = nil },
		"clock":      func(options *Options) { options.Clock = nil },
		"ids":        func(options *Options) { options.IDs = nil },
		"validator":  func(options *Options) { options.Validator = nil },
	} {
		t.Run("missing "+name, func(t *testing.T) {
			options := base
			remove(&options)
			_, err := NewService(options)
			requireCatalogCode(t, err, CodeInvalidArgument)
		})
	}
	service, err := NewService(base)
	if err != nil || service.policy != "default-v1" || service.ValidationLimits() != DefaultDiagnosticLimits() {
		t.Fatalf("service defaults = %#v, %v", service, err)
	}
	for _, limits := range []DiagnosticLimits{{MaxFindings: 1}, {MaxDiagnosticBytes: 1024}, {MaxFindings: 513, MaxDiagnosticBytes: 1024}, {MaxFindings: 1, MaxDiagnosticBytes: 262145}} {
		options := base
		options.DiagnosticLimits = limits
		_, err := NewService(options)
		requireCatalogCode(t, err, CodeInvalidArgument)
	}
	configured := base
	configured.DiagnosticLimits = DiagnosticLimits{MaxFindings: 1, MaxDiagnosticBytes: 1024}
	service, err = NewService(configured)
	if err != nil || service.ValidationLimits() != configured.DiagnosticLimits {
		t.Fatalf("configured service limits = %#v, %v", service, err)
	}
	configured.DiagnosticLimits.MaxFindings = 2
	if service.ValidationLimits().MaxFindings != 1 {
		t.Fatal("service retained mutable diagnostic options")
	}

	unauthenticated := context.Background()
	readCalls := map[string]func() error{
		"list namespaces": func() error {
			_, err := fixture.service.ListNamespaces(unauthenticated, NamespaceListRequest{})
			return err
		},
		"get namespace": func() error { _, err := fixture.service.GetNamespace(unauthenticated, "missing"); return err },
		"list publishers": func() error {
			_, err := fixture.service.ListPublishers(unauthenticated, PublisherListRequest{})
			return err
		},
		"get publisher": func() error { _, err := fixture.service.GetPublisher(unauthenticated, "missing"); return err },
		"get resource":  func() error { _, err := fixture.service.GetResource(unauthenticated, "missing"); return err },
		"list resources": func() error {
			_, err := fixture.service.ListResources(unauthenticated, ResourceListRequest{})
			return err
		},
		"get version": func() error { _, err := fixture.service.GetVersion(unauthenticated, "missing"); return err },
		"get version revision": func() error {
			_, _, _, err := fixture.service.GetVersionRevision(unauthenticated, "missing")
			return err
		},
		"get listing":          func() error { _, err := fixture.service.GetListing(unauthenticated, "missing"); return err },
		"get listing revision": func() error { _, err := fixture.service.GetListingRevision(unauthenticated, "missing"); return err },
		"get submission":       func() error { _, err := fixture.service.GetSubmission(unauthenticated, "missing"); return err },
		"get release":          func() error { _, err := fixture.service.GetRelease(unauthenticated, "missing"); return err },
		"get artifact":         func() error { _, _, err := fixture.service.GetArtifact(unauthenticated, "missing"); return err },
		"search":               func() error { _, err := fixture.service.Search(unauthenticated, SearchQuery{}); return err },
		"explore":              func() error { _, err := fixture.service.Explore(unauthenticated, ExploreRequest{}); return err },
		"get catalog record":   func() error { _, err := fixture.service.GetCatalogRecord(unauthenticated, "missing"); return err },
		"audit": func() error {
			_, err := fixture.service.AuditEvents(unauthenticated, AuditEventListRequest{Limit: 10})
			return err
		},
	}
	for name, call := range readCalls {
		t.Run(name, func(t *testing.T) { requireCatalogCode(t, call(), CodeForbidden) })
	}

	actorWithoutRoles := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000e5bd510e", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: guardrailTenantID, Subject: "authenticated"})
	if rows, err := fixture.service.ListResources(actorWithoutRoles, ResourceListRequest{Limit: 10}); err != nil || len(rows) != 0 {
		t.Fatalf("principal without grants must receive an empty authorized collection: %v / %v", rows, err)
	}
	if _, err := fixture.service.CreateNamespace(unauthenticated, CreateNamespaceInput{}); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("anonymous namespace creation returned %v", err)
	}
	if _, _, _, err := fixture.service.CreateVersion(unauthenticated, "missing", CreateVersionInput{}); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("anonymous version creation returned %v", err)
	}
	if _, _, _, err := fixture.service.CreateVersionRevision(unauthenticated, "missing", nil, 1); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("anonymous version revision returned %v", err)
	}
	if _, _, _, err := fixture.service.RevalidateVersionRevision(unauthenticated, "missing"); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("anonymous version validation returned %v", err)
	}
	if _, _, err := fixture.service.CreateListingRevision(unauthenticated, "missing", CreateListingRevisionInput{}, 1); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("anonymous listing revision returned %v", err)
	}
	if _, err := fixture.service.Submit(unauthenticated, "missing"); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("anonymous submission returned %v", err)
	}
	if _, _, err := fixture.service.DecideSubmission(unauthenticated, "missing", "approve", "", 1); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("anonymous review returned %v", err)
	}
	if _, _, _, err := fixture.service.Publish(unauthenticated, "missing", PublishInput{}, 1); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("anonymous publication returned %v", err)
	}
	if _, err := fixture.service.TransitionRelease(unauthenticated, "missing", ReleaseRetired, "reason", "", nil, 1); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("anonymous release transition returned %v", err)
	}
}

func TestCatalogServiceAdministrativePagesValidateAndFetchOneExtra(t *testing.T) {
	t.Parallel()

	fixture := newGuardrailFixture(t)
	adminContext := fixture.adminContext()
	readerContext := fixture.readerContext()
	namespaces := make([]Namespace, 0, 3)
	for _, input := range []CreateNamespaceInput{
		{Slug: "charlie", DisplayName: "Charlie"},
		{Slug: "alpha", DisplayName: "Alpha"},
		{Slug: "bravo", DisplayName: "Bravo"},
	} {
		namespace, err := fixture.service.CreateNamespace(adminContext, input)
		if err != nil {
			t.Fatal(err)
		}
		namespaces = append(namespaces, namespace)
	}
	page, err := fixture.service.ListNamespaces(readerContext, NamespaceListRequest{Limit: 1})
	if err != nil || len(page) != 2 || page[0].Slug != "alpha" || page[1].Slug != "bravo" {
		t.Fatalf("namespace lookahead page = %#v, %v", page, err)
	}
	page, err = fixture.service.ListNamespaces(readerContext, NamespaceListRequest{AfterSlug: page[0].Slug, AfterID: page[0].ID, Limit: 1})
	if err != nil || len(page) != 2 || page[0].Slug != "bravo" {
		t.Fatalf("positioned namespace lookahead page = %#v, %v", page, err)
	}
	for _, request := range []NamespaceListRequest{
		{},
		{Limit: MaxCatalogPageSize + 1},
		{AfterID: namespaces[0].ID, Limit: 1},
		{AfterSlug: "alpha", AfterID: "not-a-uuid", Limit: 1},
	} {
		if _, err := fixture.service.ListNamespaces(readerContext, request); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid namespace page %#v returned %v", request, err)
		}
	}

	publishers := make([]Publisher, 0, 3)
	for _, input := range []CreatePublisherInput{
		{DisplayName: "Charlie", Domains: []string{"charlie.example"}},
		{DisplayName: "Alpha", Domains: []string{"alpha.example"}},
		{DisplayName: "Bravo", Domains: []string{"bravo.example"}},
	} {
		publisher, err := fixture.service.CreatePublisher(adminContext, input)
		if err != nil {
			t.Fatal(err)
		}
		publishers = append(publishers, publisher)
	}
	publisherPage, err := fixture.service.ListPublishers(readerContext, PublisherListRequest{Limit: 1})
	if err != nil || len(publisherPage) != 2 || publisherPage[0].DisplayName != "Alpha" || publisherPage[1].DisplayName != "Bravo" {
		t.Fatalf("publisher lookahead page = %#v, %v", publisherPage, err)
	}
	publisherPage, err = fixture.service.ListPublishers(readerContext, PublisherListRequest{AfterDisplayName: publisherPage[0].DisplayName, AfterID: publisherPage[0].ID, Limit: 1})
	if err != nil || len(publisherPage) != 2 || publisherPage[0].DisplayName != "Bravo" {
		t.Fatalf("positioned publisher lookahead page = %#v, %v", publisherPage, err)
	}
	for _, request := range []PublisherListRequest{
		{},
		{Limit: MaxCatalogPageSize + 1},
		{AfterID: publishers[0].ID, Limit: 1},
		{AfterDisplayName: "Alpha", AfterID: "not-a-uuid", Limit: 1},
	} {
		if _, err := fixture.service.ListPublishers(readerContext, request); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid publisher page %#v returned %v", request, err)
		}
	}
}

func TestRepresentationMediaTypeRequiresTypeAndSubtype(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"application/json", "Application/JSON", `text/markdown; profile="urn:air:agent-skills"`} {
		if !validRepresentationMediaType(value) {
			t.Fatalf("valid representation media type %q was rejected", value)
		}
	}
	for _, value := range []string{"", "invalid", " application/json", "application/json ", "application/\x00json", strings.Repeat("a", 256)} {
		if validRepresentationMediaType(value) {
			t.Fatalf("invalid representation media type %q was accepted", value)
		}
	}
}

func TestValidationRunsRenewImmutableContentWithoutStrandingApproval(t *testing.T) {
	fixture := newGuardrailFixture(t)
	service := fixture.service
	ctx := fixture.adminContext()

	namespace, err := service.CreateNamespace(ctx, CreateNamespaceInput{Slug: "validation", DisplayName: "Validation"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := service.CreatePublisher(ctx, CreatePublisherInput{DisplayName: "Validation Publisher", Domains: []string{"catalog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(ctx, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:validation:agent", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	_, revision, initialEvidence, err := service.CreateVersion(ctx, resource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"renewable"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if !initialEvidence.ExpiresAt.IsZero() || initialEvidence.ValidationProfile != currentValidationProfile {
		t.Fatalf("initial deterministic evidence = %#v", initialEvidence)
	}

	if revision, evidence, namespace, err := service.RevalidateVersionRevision(fixture.readerContext(), revision.ID); ErrorCodeOf(err) != CodeForbidden || revision.ID != "" || evidence.ID != "" || namespace != "" {
		t.Fatalf("reader validation returned %v", err)
	}
	otherTenant := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000f5120eb8", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: "00000000-0000-7000-8000-000000000098", Subject: "contributor", Roles: []identity.Role{identity.RoleContributor}})
	if revision, evidence, namespace, err := service.RevalidateVersionRevision(otherTenant, revision.ID); ErrorCodeOf(err) != CodeNotFound || revision.ID != "" || evidence.ID != "" || namespace != "" {
		t.Fatalf("cross-tenant validation returned %v", err)
	}

	listing, err := service.CreateListing(ctx, CreateListingInput{NamespaceID: namespace.ID, ResourceID: resource.ID})
	if err != nil {
		t.Fatal(err)
	}
	listing, _, err = service.CreateListingRevision(ctx, listing.ID, CreateListingRevisionInput{VersionRevisionID: revision.ID, DisplayName: "Renewable agent", Description: "Exercises renewable structural validation.", PublisherID: publisher.ID, Visibility: VisibilityTenant}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.now = fixture.clock.now.Add(30 * 24 * time.Hour)
	submission, err := service.Submit(ctx, listing.ID)
	if err != nil {
		t.Fatalf("deterministic evidence expired with wall-clock time: %v", err)
	}
	reviewer := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000f3936bbb", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: guardrailTenantID, Subject: "independent-reviewer", Roles: []identity.Role{identity.RoleReviewer}})
	submission, _, err = service.DecideSubmission(reviewer, submission.ID, "approve", "reviewed", submission.Revision)
	if err != nil {
		t.Fatal(err)
	}

	originalRevision := fixture.repo.versionRevisions[revision.ID]
	originalRevisionJSON, err := json.Marshal(originalRevision)
	if err != nil {
		t.Fatal(err)
	}
	service.validator = guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult {
		return ValidationResult{ProfileID: "test-profile", Findings: []Finding{{Code: "CURRENT_POLICY_BLOCK", Severity: SeverityError, Path: "/name", Message: "blocked by current policy"}}, NormalizedFacts: map[string]any{}, Unknown: map[string]any{}}
	})
	validatedRevision, blockedEvidence, _, err := service.RevalidateVersionRevision(ctx, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blockedEvidence.State != EvidenceBlocked || blockedEvidence.ID == initialEvidence.ID || validatedRevision.ContentSetDigest != revision.ContentSetDigest || validatedRevision.EvidenceSetID != blockedEvidence.ID || !blockedEvidence.ExpiresAt.IsZero() {
		t.Fatalf("blocked validation run = revision %#v evidence %#v", validatedRevision, blockedEvidence)
	}
	storedRevisionJSON, err := json.Marshal(fixture.repo.versionRevisions[revision.ID])
	if err != nil {
		t.Fatal(err)
	}
	if string(storedRevisionJSON) != string(originalRevisionJSON) {
		t.Fatalf("immutable version revision was rewritten: before %s after %s", originalRevisionJSON, storedRevisionJSON)
	}
	retained, err := fixture.repo.GetValidationEvidence(context.Background(), guardrailTenantID, revision.ID, initialEvidence.ID)
	if err != nil || retained.Digest != initialEvidence.Digest {
		t.Fatalf("prior validation evidence = %#v, %v", retained, err)
	}
	publishContext := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000a7a77f47", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: guardrailTenantID, Subject: "publisher", Roles: []identity.Role{identity.RolePublisher}})
	if _, _, _, err := service.Publish(publishContext, listing.ID, PublishInput{SubmissionID: submission.ID}, listing.Revision); ErrorCodeOf(err) != CodePublicationNotReady {
		t.Fatalf("publication with current blocked evidence returned %v", err)
	}

	service.validator = guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult {
		return ValidationResult{ProfileID: "test-profile", NormalizedFacts: map[string]any{}, Unknown: map[string]any{}}
	})
	_, currentEvidence, _, err := service.RevalidateVersionRevision(ctx, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if currentEvidence.State != EvidenceComplete || currentEvidence.ID == initialEvidence.ID || currentEvidence.ID == blockedEvidence.ID || len(fixture.repo.validationRuns[revision.ID]) != 3 {
		t.Fatalf("renewed validation evidence = %#v, runs=%#v", currentEvidence, fixture.repo.validationRuns[revision.ID])
	}
	loadedRevision, loadedEvidence, _, err := service.GetVersionRevision(fixture.readerContext(), revision.ID)
	if err != nil || loadedRevision.EvidenceSetID != currentEvidence.ID || loadedEvidence.ID != currentEvidence.ID {
		t.Fatalf("current validation projection = %#v / %#v / %v", loadedRevision, loadedEvidence, err)
	}
	if _, release, _, err := service.Publish(publishContext, listing.ID, PublishInput{SubmissionID: submission.ID}, listing.Revision); err != nil || release.State != ReleasePublished {
		t.Fatalf("publication after identical current validation = %#v, %v", release, err)
	}
}

func TestPendingReviewFreezesItsValidationRun(t *testing.T) {
	fixture := newGuardrailFixture(t)
	ctx := fixture.adminContext()
	namespace, err := fixture.service.CreateNamespace(ctx, CreateNamespaceInput{Slug: "pending-validation", DisplayName: "Pending validation"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := fixture.service.CreatePublisher(ctx, CreatePublisherInput{DisplayName: "Pending Publisher", Domains: []string{"catalog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := fixture.service.CreateResource(ctx, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:validation:pending", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	_, revision, _, err := fixture.service.CreateVersion(ctx, resource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"pending"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := fixture.service.CreateListing(ctx, CreateListingInput{NamespaceID: namespace.ID, ResourceID: resource.ID})
	if err != nil {
		t.Fatal(err)
	}
	listing, _, err = fixture.service.CreateListingRevision(ctx, listing.ID, CreateListingRevisionInput{VersionRevisionID: revision.ID, DisplayName: "Pending agent", Description: "Exercises the review evidence binding.", PublisherID: publisher.ID, Visibility: VisibilityTenant}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	submission, err := fixture.service.Submit(ctx, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.service.RevalidateVersionRevision(ctx, revision.ID); err != nil {
		t.Fatal(err)
	}
	reviewer := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000f3936bbb", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: guardrailTenantID, Subject: "independent-reviewer", Roles: []identity.Role{identity.RoleReviewer}})
	if _, _, err := fixture.service.DecideSubmission(reviewer, submission.ID, "approve", "reviewed", submission.Revision); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("approval after a newer validation run returned %v", err)
	}
}

func TestRevalidationFailsClosedOnRetainedRepresentationCorruption(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name   string
		input  RepresentationInput
		mutate func(*MemoryRepository, VersionRevision)
	}{
		{
			name:  "inline document",
			input: RepresentationInput{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"original"}`)},
			mutate: func(repository *MemoryRepository, revision VersionRevision) {
				revision.Representations[0].Data = json.RawMessage(`{"name":"tampered"}`)
				repository.versionRevisions[revision.ID] = revision
			},
		},
		{
			name:  "ambiguous inline document",
			input: RepresentationInput{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"original"}`)},
			mutate: func(repository *MemoryRepository, revision VersionRevision) {
				ambiguous := json.RawMessage(`{"name":"first","name":"second"}`)
				revision.Representations[0].Data = ambiguous
				revision.Representations[0].ContentDigest = digestBytes(ambiguous)
				revision.Representations[0].Size = int64(len(ambiguous))
				revision.ContentSetDigest, _ = contentSetDigest(revision.Representations)
				repository.versionRevisions[revision.ID] = revision
			},
		},
		{
			name:  "bounded document",
			input: RepresentationInput{Role: RepresentationPrimary, MediaType: "text/markdown", Content: "# Original"},
			mutate: func(repository *MemoryRepository, revision VersionRevision) {
				key := guardrailTenantID + "/" + revision.Representations[0].ArtifactID
				document := repository.documents[key]
				document.Content = []byte("# Tampered")
				repository.documents[key] = document
			},
		},
		{
			name:  "external reference",
			input: RepresentationInput{Role: RepresentationPrimary, MediaType: "application/json", URL: "https://content.example/original.json"},
			mutate: func(repository *MemoryRepository, revision VersionRevision) {
				revision.Representations[0].URL = "https://content.example/tampered.json"
				repository.versionRevisions[revision.ID] = revision
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newGuardrailFixture(t)
			ctx := fixture.adminContext()
			namespace, err := fixture.service.CreateNamespace(ctx, CreateNamespaceInput{Slug: "integrity", DisplayName: "Integrity"})
			if err != nil {
				t.Fatal(err)
			}
			resource, err := fixture.service.CreateResource(ctx, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:integrity:agent", Kind: KindAgent})
			if err != nil {
				t.Fatal(err)
			}
			_, revision, _, err := fixture.service.CreateVersion(ctx, resource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{testCase.input}})
			if err != nil {
				t.Fatal(err)
			}
			testCase.mutate(fixture.repo, fixture.repo.versionRevisions[revision.ID])
			auditCount, outboxCount := len(fixture.repo.audit), len(fixture.repo.outbox)
			if _, _, _, err := fixture.service.RevalidateVersionRevision(ctx, revision.ID); ErrorCodeOf(err) != CodeDependencyUnavailable {
				t.Fatalf("corrupt retained representation returned %v", err)
			}
			if len(fixture.repo.validationRuns[revision.ID]) != 1 || len(fixture.repo.audit) != auditCount || len(fixture.repo.outbox) != outboxCount {
				t.Fatalf("failed integrity check changed state: runs=%#v audit=%d outbox=%d", fixture.repo.validationRuns[revision.ID], len(fixture.repo.audit), len(fixture.repo.outbox))
			}
		})
	}
}

func TestRevalidationBlocksDerivedProjectionDrift(t *testing.T) {
	t.Parallel()
	fixture := newGuardrailFixture(t)
	fixture.service.validator = guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult {
		return ValidationResult{ProfileID: "test-profile", NormalizedFacts: map[string]any{"name": "original"}, Unknown: map[string]any{"extension": "original"}}
	})
	ctx := fixture.adminContext()
	namespace, err := fixture.service.CreateNamespace(ctx, CreateNamespaceInput{Slug: "projection", DisplayName: "Projection"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := fixture.service.CreateResource(ctx, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:projection:agent", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	_, revision, _, err := fixture.service.CreateVersion(ctx, resource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"original"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.validator = guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult {
		return ValidationResult{ProfileID: "different-profile", NormalizedFacts: map[string]any{"name": "changed"}, Unknown: map[string]any{"extension": "changed"}}
	})
	_, evidence, _, err := fixture.service.RevalidateVersionRevision(ctx, revision.ID)
	if err != nil || evidence.State != EvidenceBlocked {
		t.Fatalf("projection drift evidence = %#v, %v", evidence, err)
	}
	wanted := map[string]bool{"REPRESENTATION_PROFILE_CHANGED": false, "NORMALIZED_FACTS_CHANGED": false, "UNKNOWN_PROPERTIES_CHANGED": false}
	for _, finding := range evidence.Findings {
		if _, ok := wanted[finding.Code]; ok {
			wanted[finding.Code] = true
		}
	}
	for code, found := range wanted {
		if !found {
			t.Fatalf("projection drift did not record %s: %#v", code, evidence.Findings)
		}
	}
}

func TestCatalogServiceValidatesGovernedAuthoringInputs(t *testing.T) {
	fixture := newGuardrailFixture(t)

	for _, input := range []CreateNamespaceInput{
		{Slug: "-invalid", DisplayName: "Invalid"},
		{Slug: "valid", DisplayName: ""},
	} {
		_, err := fixture.service.CreateNamespace(fixture.adminContext(), input)
		requireCatalogCode(t, err, CodeInvalidArgument)
	}
	namespace, err := fixture.service.CreateNamespace(fixture.adminContext(), CreateNamespaceInput{Slug: " DEFAULT ", DisplayName: " Default "})
	if err != nil || namespace.Slug != "default" || namespace.DisplayName != "Default" {
		t.Fatalf("normalized namespace = %#v, %v", namespace, err)
	}
	if _, err := fixture.service.CreateNamespace(fixture.adminContext(), CreateNamespaceInput{Slug: "default", DisplayName: "Duplicate"}); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("duplicate namespace returned %v", err)
	}
	if got, err := fixture.service.GetNamespace(fixture.readerContext(), namespace.ID); err != nil || got.ID != namespace.ID {
		t.Fatalf("get namespace = %#v, %v", got, err)
	}
	if _, err := fixture.service.GetNamespace(fixture.readerContext(), "00000000-0000-7000-8000-000000000099"); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("missing namespace returned %v", err)
	}

	for _, input := range []CreatePublisherInput{
		{DisplayName: "", Domains: []string{"example.com"}},
		{DisplayName: "Invalid", Domains: []string{"localhost"}},
	} {
		_, err := fixture.service.CreatePublisher(fixture.adminContext(), input)
		requireCatalogCode(t, err, CodeInvalidArgument)
	}
	publisher, err := fixture.service.CreatePublisher(fixture.adminContext(), CreatePublisherInput{DisplayName: " Example ", Domains: []string{"CATALOG.EXAMPLE.COM.", "catalog.example.com"}})
	if err != nil || len(publisher.Domains) != 1 || publisher.Domains[0] != "catalog.example.com" {
		t.Fatalf("normalized publisher = %#v, %v", publisher, err)
	}
	if _, err := fixture.service.CreatePublisher(fixture.adminContext(), CreatePublisherInput{DisplayName: "Conflict", Domains: []string{"catalog.example.com"}}); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("duplicate publisher domain returned %v", err)
	}
	if _, err := fixture.service.ApprovePublisherPublicationDomain(fixture.adminContext(), publisher.ID, "catalog.example.com", "", publisher.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("empty approval reason returned %v", err)
	}
	if _, err := fixture.service.ApprovePublisherPublicationDomain(fixture.adminContext(), publisher.ID, "other.example.com", "approved", publisher.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("undeclared publisher domain returned %v", err)
	}
	publisher, err = fixture.service.ApprovePublisherPublicationDomain(fixture.adminContext(), publisher.ID, "CATALOG.EXAMPLE.COM.", "approved", publisher.Revision)
	if err != nil || len(publisher.ApprovedPublicationDomains) != 1 {
		t.Fatalf("publisher publication-domain approval = %#v, %v", publisher, err)
	}
	if audit := fixture.repo.audit[len(fixture.repo.audit)-1]; audit.Action != "publisher.publication_domain_approved" || audit.Reason != "approved" || audit.TargetID != publisher.ID {
		t.Fatalf("publication-domain approval audit = %#v", audit)
	}
	if event := fixture.repo.outbox[len(fixture.repo.outbox)-1]; event.Type != "com.open.agentic.catalog.publisher.publication.domain.approved.v1" || event.Subject != publisher.ID {
		t.Fatalf("publication-domain approval outbox event = %#v", event)
	}
	evidenceCount := len(fixture.repo.audit)
	if _, err := fixture.service.ApprovePublisherPublicationDomain(fixture.adminContext(), publisher.ID, "catalog.example.com", "stale", 1); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("stale publisher revision returned %v", err)
	}
	if len(fixture.repo.audit) != evidenceCount || len(fixture.repo.outbox) != evidenceCount {
		t.Fatalf("failed approval persisted evidence: audit=%d outbox=%d want=%d", len(fixture.repo.audit), len(fixture.repo.outbox), evidenceCount)
	}
	if got, err := fixture.service.GetPublisher(fixture.readerContext(), publisher.ID); err != nil || got.ID != publisher.ID {
		t.Fatalf("get publisher = %#v, %v", got, err)
	}
	if _, err := fixture.service.GetPublisher(fixture.readerContext(), "00000000-0000-7000-8000-000000000098"); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("missing publisher returned %v", err)
	}

	for _, input := range []CreateResourceInput{
		{NamespaceID: namespace.ID, CanonicalIdentifier: "identifier", Kind: Kind("unsupported")},
		{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:not valid", Kind: KindAgent},
		{NamespaceID: "missing", CanonicalIdentifier: "urn:example:missing", Kind: KindAgent},
	} {
		_, err := fixture.service.CreateResource(fixture.adminContext(), input)
		if ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("resource input %#v returned %v", input, err)
		}
	}
	resource, err := fixture.service.CreateResource(fixture.adminContext(), CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:default:guardrails", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.CreateResource(fixture.adminContext(), CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: resource.CanonicalIdentifier, Kind: KindAgent}); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("duplicate resource returned %v", err)
	}
	if _, err := fixture.service.ListResources(fixture.readerContext(), ResourceListRequest{Kind: Kind("unsupported")}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid resource kind returned %v", err)
	}
	if resources, err := fixture.service.ListResources(fixture.readerContext(), ResourceListRequest{Kind: KindAgent, Limit: 100}); err != nil || len(resources) != 1 {
		t.Fatalf("resource list = %#v, %v", resources, err)
	}

	invalidRepresentations := [][]RepresentationInput{
		nil,
		{{Role: RepresentationRole("invalid"), MediaType: "application/json", Data: json.RawMessage(`{}`)}},
		{{Role: RepresentationPrimary, Data: json.RawMessage(`{}`)}},
		{{Role: RepresentationPrimary, MediaType: "text/plain\r\nx-test: unsafe", Content: "unsafe"}},
		{{Role: RepresentationPrimary, MediaType: "text/plain; note=\"unsafe\u0085value\"", Content: "unsafe"}},
		{{Role: RepresentationPrimary, MediaType: "text/plain", Content: "unsafe\x00text"}},
		{{Role: RepresentationPrimary, MediaType: "text/plain", Content: string([]byte{0xff})}},
		{{Role: RepresentationPrimary, MediaType: "application/json", URL: "http://example.com/card"}},
		{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{}`), Content: "duplicate"}},
		{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`[]`)}},
	}
	for _, representations := range invalidRepresentations {
		_, _, _, err := fixture.service.CreateVersion(fixture.adminContext(), resource.ID, CreateVersionInput{Version: "invalid", Representations: representations})
		requireCatalogCode(t, err, CodeInvalidArgument)
	}
	if _, _, _, err := fixture.service.CreateVersion(fixture.adminContext(), resource.ID, CreateVersionInput{Version: "invalid-late", Representations: []RepresentationInput{
		{Role: RepresentationPrimary, MediaType: "text/markdown", Content: "# Valid first document"},
		{Role: RepresentationRole("invalid"), MediaType: "text/plain", Content: "invalid second document"},
	}}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("late invalid representation returned %v", err)
	}
	if _, err := fixture.repo.GetVersionByLabel(context.Background(), guardrailTenantID, resource.ID, "invalid-late"); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("invalid revision retained state: %v", err)
	}
	if _, _, _, err := fixture.service.CreateVersion(fixture.adminContext(), resource.ID, CreateVersionInput{Version: "", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{}`)}}}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("empty version label returned %v", err)
	}
	version, revision, evidence, err := fixture.service.CreateVersion(fixture.adminContext(), resource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "text/markdown", Content: "# Guardrails"}}})
	if err != nil || evidence.State != EvidenceComplete || revision.Representations[0].ArtifactID == "" {
		t.Fatalf("managed representation = %#v, %#v, %v", revision, evidence, err)
	}
	artifact, content, err := fixture.service.GetArtifact(fixture.readerContext(), revision.Representations[0].ArtifactID)
	if err != nil || artifact.ID == "" || string(content) != "# Guardrails" {
		t.Fatalf("managed artifact = %#v, %q, %v", artifact, content, err)
	}
	if _, _, err := fixture.service.GetPublicArtifact(context.Background(), guardrailTenantID, artifact.ID); err != nil {
		t.Fatalf("public artifact read returned %v", err)
	}
	if _, _, err := fixture.service.GetArtifact(fixture.readerContext(), "missing"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid document identifier returned %v", err)
	}
	if _, _, _, err := fixture.service.CreateVersion(fixture.adminContext(), resource.ID, CreateVersionInput{Version: version.Version, Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{}`)}}}); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("duplicate version returned %v", err)
	}
	blockedResource, err := fixture.service.CreateResource(fixture.adminContext(), CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:default:external", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	_, _, blocked, err := fixture.service.CreateVersion(fixture.adminContext(), blockedResource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationMetadata, MediaType: "application/json", URL: "https://example.com/card.json"}}})
	if err != nil || blocked.State != EvidenceBlocked {
		t.Fatalf("external evidence = %#v, %v", blocked, err)
	}
}

func TestCatalogDocumentsAreRepositoryBackedAndBounded(t *testing.T) {
	t.Parallel()

	fixture := newGuardrailFixture(t)
	service, err := NewService(Options{
		Repository: fixture.repo,
		Clock:      fixture.clock,
		IDs:        &guardrailIDs{},
		Validator: guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult {
			return ValidationResult{ProfileID: "skill-test", NormalizedFacts: map[string]any{}, Unknown: map[string]any{}}
		}),
		CursorKey: []byte("01234567890123456789012345678901"),
	})
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := service.CreateNamespace(fixture.adminContext(), CreateNamespaceInput{Slug: "documents", DisplayName: "Documents"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(fixture.adminContext(), CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:documents:skill", Kind: KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	content := "---\nname: document-skill\ndescription: Exact transactional content.\n---\n\n# Instructions\nKeep these bytes.\n"
	mediaType := `text/markdown; profile="https://Example.com/Profile"`
	if _, _, _, err := service.CreateVersion(fixture.adminContext(), resource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: " " + mediaType + " ", Content: content}}}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("outer-whitespace media type returned %v", err)
	}
	_, revision, _, err := service.CreateVersion(fixture.adminContext(), resource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: mediaType, Content: content}}})
	if err != nil {
		t.Fatal(err)
	}
	documentID := revision.Representations[0].ArtifactID
	artifact, stored, err := service.GetArtifact(fixture.readerContext(), documentID)
	if err != nil || artifact.ID != documentID || artifact.Digest != "sha256:"+documentID || artifact.MediaType != mediaType || revision.Representations[0].MediaType != mediaType || artifact.Size != int64(len(content)) || string(stored) != content || !artifact.CreatedAt.Equal(fixture.clock.Now()) {
		t.Fatalf("repository-backed document = %#v, %q, %v", artifact, stored, err)
	}
	stored[0] = 'x'
	_, reread, err := service.GetArtifact(fixture.readerContext(), documentID)
	if err != nil || string(reread) != content {
		t.Fatalf("document read mutated retained bytes: %q, %v", reread, err)
	}
	otherTenant := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000869c4bfc", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: "00000000-0000-7000-8000-000000000098", Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	if _, _, err := service.GetArtifact(otherTenant, documentID); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("cross-tenant document read returned %v", err)
	}
	for _, invalidID := range []string{"missing", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("g", 64)} {
		if _, _, err := service.GetArtifact(fixture.readerContext(), invalidID); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid document identifier %q returned %v", invalidID, err)
		}
	}
	if _, _, err := service.GetArtifact(fixture.readerContext(), strings.Repeat("a", 64)); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("unknown valid document identifier returned %v", err)
	}

	oversizedResource, err := service.CreateResource(fixture.adminContext(), CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:documents:oversized", Kind: KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("x", MaxCatalogDocumentBytes+1)
	if _, _, _, err := service.CreateVersion(fixture.adminContext(), oversizedResource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "text/markdown", Content: oversized}}}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("oversized document returned %v", err)
	}
	combinedResource, err := service.CreateResource(fixture.adminContext(), CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:documents:combined-oversized", Kind: KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.CreateVersion(fixture.adminContext(), combinedResource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{
		{Role: RepresentationPrimary, MediaType: "text/plain", Content: strings.Repeat("a", MaxCatalogDocumentBytes/2)},
		{Role: RepresentationMetadata, MediaType: "text/plain", Content: strings.Repeat("b", MaxCatalogDocumentBytes/2+1)},
	}}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("combined oversized documents returned %v", err)
	}
}

func TestCatalogLifecycleEnforcesReviewPublicationAndReleaseState(t *testing.T) {
	fixture := newGuardrailFixture(t)
	service := fixture.service

	namespace, err := service.CreateNamespace(fixture.adminContext(), CreateNamespaceInput{Slug: "lifecycle", DisplayName: "Lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := service.CreatePublisher(fixture.adminContext(), CreatePublisherInput{DisplayName: "Catalog Publisher", Domains: []string{"catalog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err = service.ApprovePublisherPublicationDomain(fixture.adminContext(), publisher.ID, "catalog.example.com", "approved for public publication", publisher.Revision)
	if err != nil {
		t.Fatal(err)
	}
	createVersion := func(name, versionLabel string) (Resource, ResourceVersion, VersionRevision, EvidenceSet) {
		t.Helper()
		resource, createErr := service.CreateResource(fixture.adminContext(), CreateResourceInput{
			NamespaceID:         namespace.ID,
			CanonicalIdentifier: "urn:air:catalog.example.com:lifecycle:" + name,
			Kind:                KindAgent,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		version, revision, evidence, createErr := service.CreateVersion(fixture.adminContext(), resource.ID, CreateVersionInput{
			Version: versionLabel,
			Representations: []RepresentationInput{{
				Role:      RepresentationPrimary,
				MediaType: "application/json",
				Data:      json.RawMessage(`{"name":"` + name + `"}`),
			}},
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return resource, version, revision, evidence
	}

	resource, versionOne, versionRevisionOne, evidenceOne := createVersion("source", "1.0.0")
	target, _, targetRevision, _ := createVersion("target", "2.0.0")
	listing, err := service.CreateListing(fixture.adminContext(), CreateListingInput{NamespaceID: namespace.ID, ResourceID: resource.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Submit(fixture.adminContext(), listing.ID); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("submission without candidate returned %v", err)
	}

	baseRevision := CreateListingRevisionInput{
		VersionRevisionID:     versionRevisionOne.ID,
		DisplayName:           "Source agent",
		Description:           "A governed source agent.",
		Tags:                  []string{" Governance ", "catalog", "catalog"},
		Categories:            []string{"Agents"},
		RepresentativeQueries: []string{"Find source agents", "Show governed source agents"},
		PublisherID:           publisher.ID,
		DocumentationURL:      "https://docs.example.com/source",
		SupportURL:            "https://support.example.com/source",
		TermsURL:              "https://example.com/terms",
		Visibility:            VisibilityPublic,
	}
	if _, _, err := service.CreateListingRevision(fixture.adminContext(), listing.ID, baseRevision, listing.Revision+1); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("stale listing revision returned %v", err)
	}
	wrongVersion := baseRevision
	wrongVersion.VersionRevisionID = targetRevision.ID
	if _, _, err := service.CreateListingRevision(fixture.adminContext(), listing.ID, wrongVersion, listing.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("cross-resource listing revision returned %v", err)
	}
	invalidInputs := []CreateListingRevisionInput{
		func() CreateListingRevisionInput { input := baseRevision; input.DisplayName = ""; return input }(),
		func() CreateListingRevisionInput {
			input := baseRevision
			input.Tags = make([]string, 33)
			return input
		}(),
		func() CreateListingRevisionInput {
			input := baseRevision
			input.DocumentationURL = "http://docs.example.com"
			return input
		}(),
		func() CreateListingRevisionInput { input := baseRevision; input.PublisherID = "missing"; return input }(),
		func() CreateListingRevisionInput {
			input := baseRevision
			input.Relationships = make([]Relationship, 65)
			return input
		}(),
		func() CreateListingRevisionInput {
			input := baseRevision
			input.Relationships = []Relationship{{Type: "invokes", TargetIdentifier: target.CanonicalIdentifier}}
			return input
		}(),
		func() CreateListingRevisionInput {
			input := baseRevision
			input.Relationships = []Relationship{{Type: "depends_on", TargetIdentifier: "urn:air:not valid"}}
			return input
		}(),
		func() CreateListingRevisionInput {
			input := baseRevision
			input.Relationships = []Relationship{{Type: "depends_on", TargetIdentifier: "urn:air:catalog.example.com:lifecycle:missing"}}
			return input
		}(),
		func() CreateListingRevisionInput {
			input := baseRevision
			input.Relationships = []Relationship{{Type: "supersedes", TargetIdentifier: target.CanonicalIdentifier}}
			return input
		}(),
		func() CreateListingRevisionInput {
			input := baseRevision
			input.Relationships = []Relationship{{Type: "depends_on", TargetIdentifier: resource.CanonicalIdentifier, TargetVersion: versionOne.Version}}
			return input
		}(),
		func() CreateListingRevisionInput {
			input := baseRevision
			relationship := Relationship{Type: "depends_on", TargetIdentifier: target.CanonicalIdentifier, TargetVersion: "2.0.0"}
			input.Relationships = []Relationship{relationship, relationship}
			return input
		}(),
	}
	for index, input := range invalidInputs {
		if _, _, err := service.CreateListingRevision(fixture.adminContext(), listing.ID, input, listing.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid listing input %d returned %v", index, err)
		}
	}
	baseRevision.Relationships = []Relationship{
		{Type: " recommended_with ", TargetIdentifier: target.CanonicalIdentifier},
		{Type: "depends_on", TargetIdentifier: target.CanonicalIdentifier, TargetVersion: "2.0.0"},
	}
	listing, listingRevisionOne, err := service.CreateListingRevision(fixture.adminContext(), listing.ID, baseRevision, listing.Revision)
	if err != nil || len(listingRevisionOne.Relationships) != 2 || listingRevisionOne.Tags[0] != "catalog" {
		t.Fatalf("listing revision = %#v, %v", listingRevisionOne, err)
	}
	if _, _, err := service.CreateListingRevision(fixture.adminContext(), listing.ID, baseRevision, listing.Revision); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("duplicate listing revision returned %v", err)
	}

	legacyEvidence := fixture.repo.evidenceSets[evidenceOne.ID]
	legacyEvidence.ValidationProfile = "catalog-validation-v2"
	fixture.repo.evidenceSets[legacyEvidence.ID] = legacyEvidence
	if _, err := service.Submit(fixture.adminContext(), listing.ID); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("submission with legacy validation evidence returned %v", err)
	}
	fixture.repo.evidenceSets[evidenceOne.ID] = evidenceOne

	submissionOne, err := service.Submit(fixture.adminContext(), listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Submit(fixture.adminContext(), listing.ID); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("duplicate active submission returned %v", err)
	}
	if _, _, err := service.DecideSubmission(fixture.adminContext(), submissionOne.ID, "invalid", "reason", submissionOne.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid review action returned %v", err)
	}
	if _, _, err := service.DecideSubmission(fixture.adminContext(), submissionOne.ID, "reject", "", submissionOne.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("reason-free rejection returned %v", err)
	}
	if _, _, err := service.DecideSubmission(fixture.readerContext(), submissionOne.ID, "approve", "Reader cannot review", submissionOne.Revision); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("reader review returned %v", err)
	}
	if _, _, err := service.DecideSubmission(fixture.adminContext(), submissionOne.ID, "approve", "Submitter cannot self-review", submissionOne.Revision); ErrorCodeOf(err) != CodeSeparationOfDuties {
		t.Fatalf("self review returned %v", err)
	}
	reviewer := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-000075bea508", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: guardrailTenantID, Subject: "reviewer", Roles: []identity.Role{identity.RoleReviewer}})
	for name, reason := range map[string]string{
		"blank":          "",
		"control":        "reviewed\nwith newline",
		"invalid utf-8":  string([]byte{0xff}),
		"too many runes": strings.Repeat("\u00e9", 2001),
	} {
		t.Run("review reason "+name, func(t *testing.T) {
			if _, _, decisionErr := service.DecideSubmission(reviewer, submissionOne.ID, "approve", reason, submissionOne.Revision); ErrorCodeOf(decisionErr) != CodeInvalidArgument {
				t.Fatalf("unsafe approval reason returned %v", decisionErr)
			}
		})
	}
	approvalReason := strings.Repeat("\u00e9", 2000)
	submissionOne, versionOne, err = service.DecideSubmission(reviewer, submissionOne.ID, "approve", "  "+approvalReason+"  ", submissionOne.Revision)
	if err != nil || submissionOne.State != SubmissionApproved || versionOne.SealedRevisionID != versionRevisionOne.ID {
		t.Fatalf("approval = %#v, %#v, %v", submissionOne, versionOne, err)
	}
	if len(submissionOne.Decisions) != 1 || submissionOne.Decisions[0].Reason != approvalReason {
		t.Fatalf("normalized approval reason = %#v", submissionOne.Decisions)
	}
	if _, _, _, err := service.CreateVersionRevision(fixture.adminContext(), versionOne.ID, []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{}`)}}, versionOne.Revision); ErrorCodeOf(err) != CodeVersionSealed {
		t.Fatalf("sealed version revision returned %v", err)
	}

	publishContext := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000a7a77f47", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: guardrailTenantID, Subject: "publisher", Roles: []identity.Role{identity.RolePublisher}})
	if _, _, _, err := service.Publish(fixture.readerContext(), listing.ID, PublishInput{SubmissionID: submissionOne.ID}, listing.Revision); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("reader publication returned %v", err)
	}
	if _, _, _, err := service.Publish(publishContext, listing.ID, PublishInput{SubmissionID: "missing"}, listing.Revision); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("missing submission publication returned %v", err)
	}

	storedListingRevision := fixture.repo.listingRevisions[listingRevisionOne.ID]
	storedResource := fixture.repo.resources[resource.ID]
	storedPublisher := fixture.repo.publishers[publisher.ID]
	storedEvidence := fixture.repo.evidenceSets[evidenceOne.ID]
	mutations := []struct {
		name    string
		mutate  func()
		restore func()
	}{
		{
			name: "representative queries",
			mutate: func() {
				changed := storedListingRevision
				changed.RepresentativeQueries = nil
				fixture.repo.listingRevisions[changed.ID] = changed
			},
			restore: func() { fixture.repo.listingRevisions[storedListingRevision.ID] = storedListingRevision },
		},
		{
			name: "AIR identifier",
			mutate: func() {
				changed := storedResource
				changed.CanonicalIdentifier = "urn:example:source"
				fixture.repo.resources[changed.ID] = changed
			},
			restore: func() { fixture.repo.resources[storedResource.ID] = storedResource },
		},
		{
			name: "publisher publication-domain approval",
			mutate: func() {
				changed := storedPublisher
				changed.ApprovedPublicationDomains = nil
				fixture.repo.publishers[changed.ID] = changed
			},
			restore: func() { fixture.repo.publishers[storedPublisher.ID] = storedPublisher },
		},
		{
			name: "legacy validation profile",
			mutate: func() {
				changed := storedEvidence
				changed.ValidationProfile = "catalog-validation-v2"
				fixture.repo.evidenceSets[changed.ID] = changed
			},
			restore: func() { fixture.repo.evidenceSets[storedEvidence.ID] = storedEvidence },
		},
		{
			name: "expired evidence",
			mutate: func() {
				changed := storedEvidence
				changed.ExpiresAt = fixture.clock.now
				fixture.repo.evidenceSets[changed.ID] = changed
			},
			restore: func() { fixture.repo.evidenceSets[storedEvidence.ID] = storedEvidence },
		},
	}
	for _, testCase := range mutations {
		t.Run("publication rejects "+testCase.name, func(t *testing.T) {
			testCase.mutate()
			defer testCase.restore()
			_, _, _, publishErr := service.Publish(publishContext, listing.ID, PublishInput{SubmissionID: submissionOne.ID}, listing.Revision)
			requireCatalogCode(t, publishErr, CodePublicationNotReady)
		})
	}
	listing, releaseOne, prior, err := service.Publish(publishContext, listing.ID, PublishInput{SubmissionID: submissionOne.ID}, listing.Revision)
	if err != nil || prior != nil || releaseOne.State != ReleasePublished {
		t.Fatalf("first release = %#v, prior %#v, %v", releaseOne, prior, err)
	}

	_, versionRevisionTwo, _, err := service.CreateVersion(fixture.adminContext(), resource.ID, CreateVersionInput{
		Version:         "2.0.0",
		Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"source-v2"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondRevision := baseRevision
	secondRevision.VersionRevisionID = versionRevisionTwo.ID
	secondRevision.Description = "A governed source agent, version two."
	listing, _, err = service.CreateListingRevision(fixture.adminContext(), listing.ID, secondRevision, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	submissionTwo, err := service.Submit(fixture.adminContext(), listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	submissionTwo, versionTwo, err := service.DecideSubmission(reviewer, submissionTwo.ID, "approve", "reviewed", submissionTwo.Revision)
	if err != nil || versionTwo.SealedRevisionID != versionRevisionTwo.ID {
		t.Fatalf("second approval = %#v, %#v, %v", submissionTwo, versionTwo, err)
	}
	if _, _, _, err := service.Publish(publishContext, listing.ID, PublishInput{SubmissionID: submissionTwo.ID, PriorDisposition: ReleaseDeprecated}, listing.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid prior disposition returned %v", err)
	}
	if _, _, _, err := service.Publish(publishContext, listing.ID, PublishInput{SubmissionID: submissionTwo.ID, PriorDisposition: ReleaseSuperseded}, listing.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("reason-free supersession returned %v", err)
	}
	if _, _, _, err := service.Publish(publishContext, listing.ID, PublishInput{SubmissionID: submissionTwo.ID, PriorReason: strings.Repeat("r", 2001)}, listing.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("overlong prior release reason returned %v", err)
	}
	listing, releaseTwo, prior, err := service.Publish(publishContext, listing.ID, PublishInput{SubmissionID: submissionTwo.ID, PriorDisposition: ReleaseSuperseded, PriorReason: "version two replaces version one"}, listing.Revision)
	if err != nil || prior == nil || prior.ID != releaseOne.ID || prior.State != ReleaseSuperseded || prior.ReplacementReleaseID != releaseTwo.ID {
		t.Fatalf("replacement release = %#v, prior %#v, %v", releaseTwo, prior, err)
	}

	if _, err := service.Search(fixture.readerContext(), SearchQuery{Kinds: []Kind{Kind("unsupported")}}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid search returned %v", err)
	}
	page, err := service.Search(fixture.readerContext(), SearchQuery{Text: "source", Visibility: VisibilityPublic})
	if err != nil || page.Total != 1 || page.Items[0].Release.ID != releaseTwo.ID {
		t.Fatalf("catalog search = %#v, %v", page, err)
	}
	if _, err := service.Explore(fixture.readerContext(), ExploreRequest{}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("facet-free explore returned %v", err)
	}
	if _, err := service.Explore(fixture.readerContext(), ExploreRequest{Facets: []FacetRequest{{Field: "owner"}}}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("unknown facet returned %v", err)
	}
	if _, err := service.Explore(fixture.readerContext(), ExploreRequest{Facets: []FacetRequest{{Field: "kind", Limit: 101}}}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid facet limit returned %v", err)
	}
	explore, err := service.Explore(fixture.readerContext(), ExploreRequest{Facets: []FacetRequest{{Field: "kind"}, {Field: "tag", Limit: 1}}})
	if err != nil || explore.Matched != 1 || len(explore.Facets["kind"].Buckets) != 1 {
		t.Fatalf("catalog explore = %#v, %v", explore, err)
	}
	if record, err := service.GetCatalogRecord(fixture.readerContext(), releaseTwo.ID); err != nil || record.Release.ID != releaseTwo.ID {
		t.Fatalf("catalog record = %#v, %v", record, err)
	}
	if _, err := service.GetCatalogRecord(fixture.readerContext(), "missing"); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("missing catalog record returned %v", err)
	}
	if events, err := service.AuditEvents(fixture.adminContext(), AuditEventListRequest{Limit: 100}); err != nil || len(events) == 0 || len(events) > 100 {
		t.Fatalf("audit events = %d, %v", len(events), err)
	}

	if _, err := service.TransitionRelease(publishContext, releaseTwo.ID, ReleasePublished, "invalid", "", nil, releaseTwo.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid release target returned %v", err)
	}
	if _, err := service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseRetired, "", "", nil, releaseTwo.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("reason-free release transition returned %v", err)
	}
	if _, err := service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseRetired, strings.Repeat("r", 2001), "", nil, releaseTwo.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("overlong release transition reason returned %v", err)
	}
	if _, err := service.TransitionRelease(fixture.readerContext(), releaseTwo.ID, ReleaseRetired, "retire", "", nil, releaseTwo.Revision); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("reader release transition returned %v", err)
	}
	if _, err := service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseSuperseded, "replace", releaseTwo.ID, nil, releaseTwo.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("self replacement returned %v", err)
	}
	if _, err := service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseDeprecated, "self guidance", releaseTwo.ID, nil, releaseTwo.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("deprecated self replacement returned %v", err)
	}
	if _, err := service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseSuperseded, "replace", "missing", nil, releaseTwo.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("malformed replacement returned %v", err)
	}
	missingReleaseID := "00000000-0000-7000-8000-000000000093"
	if _, err := service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseSuperseded, "replace", missingReleaseID, nil, releaseTwo.Revision); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("missing replacement returned %v", err)
	}
	crossListingID := "00000000-0000-7000-8000-000000000091"
	crossReleaseID := "00000000-0000-7000-8000-000000000092"
	fixture.repo.listings[crossListingID] = CatalogListing{ID: crossListingID, TenantID: guardrailTenantID, CurrentReleaseID: crossReleaseID}
	fixture.repo.releases[crossReleaseID] = ListingRelease{ID: crossReleaseID, TenantID: guardrailTenantID, ListingID: crossListingID, State: ReleasePublished}
	if _, err := service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseRetired, "retired without replacement", releaseOne.ID, nil, releaseTwo.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("retirement replacement guidance returned %v", err)
	}
	sunset := fixture.clock.now.Add(30 * 24 * time.Hour)
	if _, err := service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseSuperseded, "superseded with sunset", releaseTwo.ID, &sunset, releaseTwo.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("supersession sunset guidance returned %v", err)
	}
	pastSunset := fixture.clock.now.Add(-time.Minute)
	if _, err := service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseDeprecated, "past sunset", "", &pastSunset, releaseTwo.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("past deprecation sunset returned %v", err)
	}
	subMicrosecondSunset := fixture.clock.now.Add(time.Nanosecond)
	if _, err := service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseDeprecated, "unrepresentable sunset", "", &subMicrosecondSunset, releaseTwo.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("sub-microsecond deprecation sunset returned %v", err)
	}
	releaseTwo, err = service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseDeprecated, "scheduled retirement", crossReleaseID, &sunset, releaseTwo.Revision)
	if err != nil || releaseTwo.State != ReleaseDeprecated || releaseTwo.SunsetAt == nil || releaseTwo.ReplacementReleaseID != crossReleaseID {
		t.Fatalf("deprecated release = %#v, %v", releaseTwo, err)
	}
	releaseTwo, err = service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseRetired, "retired", "", nil, releaseTwo.Revision)
	if err != nil || releaseTwo.State != ReleaseRetired {
		t.Fatalf("retired release = %#v, %v", releaseTwo, err)
	}
	if _, err := service.TransitionRelease(publishContext, releaseTwo.ID, ReleaseDeprecated, "cannot reopen", "", nil, releaseTwo.Revision); ErrorCodeOf(err) != CodeInvalidTransition {
		t.Fatalf("terminal release transition returned %v", err)
	}
	if records, err := service.PublicRecords(context.Background(), guardrailTenantID); err != nil || len(records) != 0 {
		t.Fatalf("retired public records = %#v, %v", records, err)
	}
}
