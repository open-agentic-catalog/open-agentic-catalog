package catalog

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type mutableCatalogSearchClock struct{ now time.Time }

func (c *mutableCatalogSearchClock) Now() time.Time { return c.now }

type publicationRecordsRepository struct {
	*MemoryRepository
	records    []CatalogRecord
	recordsErr error
	overflow   bool
}

func (r *publicationRecordsRepository) PublicationRecords(context.Context, string, PublicationRecordRequest) (PublicationRecordPage, error) {
	return PublicationRecordPage{Records: r.records, Overflow: r.overflow}, r.recordsErr
}

func TestCatalogSearchRejectsOutOfContractFilters(t *testing.T) {
	tests := []SearchQuery{
		{Text: strings.Repeat("x", 501)},
		{Kinds: []Kind{KindAgent, Kind("unsupported")}},
		{Kinds: []Kind{KindAgent, KindAgent}},
		{Tags: make([]string, 33)},
		{Tags: []string{strings.Repeat("x", 81)}},
		{Publisher: strings.Repeat("x", 257)},
		{Limit: 101},
		{Cursor: strings.Repeat("x", MaxCatalogCursorBytes+1)},
		{Visibility: Visibility("external")},
	}
	for _, query := range tests {
		if err := validateCatalogSearchQuery(query); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("query %#v returned %v", query, err)
		}
	}
}

func TestCatalogExploreRejectsIgnoredPaginationControls(t *testing.T) {
	t.Parallel()

	service := &Service{}
	actor := identity.Actor{MembershipID: "00000000-0000-7000-8000-0000870cd836", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: "tenant-1", Subject: "reader-1", Roles: []identity.Role{identity.RoleReader}, Namespaces: map[string][]identity.Role{"finance": {identity.RoleReader}}}
	ctx := identityfixture.WithActor(context.Background(), actor)
	for _, query := range []SearchQuery{{Limit: 1}, {Cursor: "cursor"}} {
		_, err := service.Explore(ctx, ExploreRequest{Query: query, Facets: []FacetRequest{{Field: "kind"}}})
		if ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("explore query %#v returned %v", query, err)
		}
	}
	for _, facet := range []FacetRequest{{Field: "kind", Limit: -1}, {Field: "kind", MinCount: -1}} {
		if _, err := service.Explore(ctx, ExploreRequest{Facets: []FacetRequest{facet}}); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("explore facet %#v returned %v", facet, err)
		}
	}
}

func TestListingDiscoveryMetadataRejectsSilentTruncation(t *testing.T) {
	if err := validateListingDiscoveryMetadata(make([]string, 33), nil, nil); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("excess tags returned %v", err)
	}
	if err := validateListingDiscoveryMetadata(nil, nil, []string{"no"}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("short representative query returned %v", err)
	}
}

func TestCatalogDiscoveryTermsAreNormalizedAndBounded(t *testing.T) {
	t.Parallel()

	terms := normalizeTerms([]string{" Beta ", "alpha", "beta", "", strings.Repeat("x", 81)}, 10)
	if strings.Join(terms, ",") != "alpha,beta" {
		t.Fatalf("normalized terms = %#v", terms)
	}
	boundedTerms := normalizeTerms([]string{"beta", "alpha", "ignored"}, 2)
	if strings.Join(boundedTerms, ",") != "alpha,beta" {
		t.Fatalf("bounded terms = %#v", boundedTerms)
	}
	queries := normalizeQueries([]string{" first   query ", "second query", "third query", "fourth query", "fifth query", "ignored query"})
	if len(queries) != 5 || queries[0] != "first query" || queries[4] != "fifth query" {
		t.Fatalf("normalized queries = %#v", queries)
	}
	filteredQueries := normalizeQueries([]string{" first   query ", "first query", "no", strings.Repeat("x", 241)})
	if len(filteredQueries) != 1 || filteredQueries[0] != "first query" {
		t.Fatalf("filtered queries = %#v", filteredQueries)
	}
}

func TestCatalogSearchFiltersScoresAndPaginates(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	searchClock := &mutableCatalogSearchClock{now: now}
	cursorKey := sha256.Sum256([]byte(t.Name()))
	service := &Service{clock: searchClock, cursorKey: cursorKey[:]}
	actor := identity.Actor{MembershipID: "00000000-0000-7000-8000-0000870cd836", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: "tenant-1", Subject: "reader-1", Roles: []identity.Role{identity.RoleReader}}
	records := []CatalogRecord{
		{
			Resource:        Resource{Kind: KindAgent, CanonicalIdentifier: "urn:air:catalog.example.com:finance:invoice-resolution"},
			Listing:         CatalogListing{NamespaceID: "finance"},
			ListingRevision: ListingRevision{DisplayName: "Invoice resolution agent", Description: "Reconcile invoice exceptions", Tags: []string{"finance", "integration"}, Categories: []string{"accounts-payable"}, PublisherID: "publisher-example", Visibility: VisibilityPublic},
			Release:         ListingRelease{ID: "release-agent"},
			Evidence:        EvidenceSet{Trust: TrustSummary{State: TrustVerified}},
			Source:          "local",
		},
		{
			Resource:        Resource{Kind: KindSkill, CanonicalIdentifier: "urn:air:catalog.example.com:finance:invoice-review"},
			Listing:         CatalogListing{NamespaceID: "finance"},
			ListingRevision: ListingRevision{DisplayName: "Invoice review skill", Description: "Review invoice data", Tags: []string{"finance"}, Categories: []string{"accounts-payable"}, PublisherID: "publisher-example", Visibility: VisibilityPublic},
			Release:         ListingRelease{ID: "release-skill"},
			Evidence:        EvidenceSet{Trust: TrustSummary{State: TrustUnverified}},
			Source:          "local",
		},
		{
			Resource:        Resource{Kind: KindMCPServer, CanonicalIdentifier: "urn:air:catalog.example.com:payments:event-stream"},
			Listing:         CatalogListing{NamespaceID: "payments"},
			ListingRevision: ListingRevision{DisplayName: "Payment events", Description: "Process payment events", Tags: []string{"payments", "streaming"}, Categories: []string{"payments"}, PublisherID: "publisher-confluent", Visibility: VisibilityTenant},
			Release:         ListingRelease{ID: "release-mcp"},
			Evidence:        EvidenceSet{Trust: TrustSummary{State: TrustUnverified}},
			Source:          "federated",
		},
	}

	// Seed only the repository projection; exercise the maintained Service.Search
	// and MemoryRepository.SearchCatalog paths without an authoring workflow.
	search := func(actor identity.Actor, records []CatalogRecord, query SearchQuery) (Page[CatalogSummary], error) {
		repository := NewMemoryRepository()
		for index, record := range records {
			key := fmt.Sprintf("search-%d", index)
			record.Resource.ID, record.Resource.TenantID = key, actor.TenantID
			record.Listing.ID, record.Listing.TenantID, record.Listing.ResourceID = key, actor.TenantID, key
			record.Listing.CurrentReleaseID = record.Release.ID
			record.ListingRevision.ID, record.ListingRevision.TenantID, record.ListingRevision.ListingID = key, actor.TenantID, key
			record.Release.TenantID, record.Release.ListingID = actor.TenantID, key
			record.Release.ListingRevisionID, record.Release.VersionRevisionID, record.Release.State = key, key, ReleasePublished
			record.Evidence.ID, record.Evidence.TenantID = key, actor.TenantID
			record.Evidence.State = EvidenceComplete
			record.Evidence.LegalRights = LegalRightsSummary{State: LegalRightsNotAssessed}
			repository.resources[key], repository.listings[key] = record.Resource, record.Listing
			repository.listingRevisions[key], repository.releases[record.Release.ID] = record.ListingRevision, record.Release
			repository.versions[key] = ResourceVersion{ID: key, TenantID: actor.TenantID, ResourceID: key}
			repository.versionRevisions[key] = VersionRevision{ID: key, TenantID: actor.TenantID, ResourceVersionID: key, EvidenceSetID: key}
			repository.evidenceSets[key] = record.Evidence
		}
		service.repo = repository
		return service.Search(identityfixture.WithActor(t.Context(), actor), query)
	}

	request := SearchQuery{Text: "invoice", Limit: 1}
	first, err := search(actor, records, request)
	if err != nil || first.Total != 2 || len(first.Items) != 1 || first.Items[0].Release.ID != "release-agent" || first.NextCursor == "" || strings.Contains(first.NextCursor, "offset") {
		t.Fatalf("first search page = %#v, %v", first, err)
	}
	request.Cursor = first.NextCursor
	second, err := search(actor, records, request)
	if err != nil || second.Total != 2 || len(second.Items) != 1 || second.Items[0].Release.ID != "release-skill" || second.NextCursor != "" {
		t.Fatalf("second search page = %#v, %v", second, err)
	}
	filtered, err := search(actor, records, SearchQuery{Text: "invoice", Kinds: []Kind{KindAgent}, Tags: []string{" FINANCE "}, Publisher: "publisher-example", Visibility: VisibilityPublic})
	if err != nil || filtered.Total != 1 || filtered.Items[0].Release.ID != "release-agent" || filtered.Items[0].RelevanceScore <= 0 {
		t.Fatalf("filtered search = %#v, %v", filtered, err)
	}
	empty, err := search(actor, records, SearchQuery{Text: "warehouse"})
	if err != nil || empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("unmatched search = %#v, %v", empty, err)
	}
	for name, query := range map[string]SearchQuery{
		"visibility": {Visibility: VisibilityTenant},
		"tags":       {Tags: []string{"streaming"}},
		"publisher":  {Publisher: "publisher-confluent"},
	} {
		page, filterErr := search(actor, records, query)
		if filterErr != nil || page.Total != 1 || page.Items[0].Release.ID != "release-mcp" {
			t.Fatalf("%s filter = %#v, %v", name, page, filterErr)
		}
	}
	tied := []CatalogRecord{records[0], records[0]}
	tied[0].Release.ID = "release-z"
	tied[1].Release.ID = "release-a"
	tiedPage, err := search(actor, tied, SearchQuery{})
	if err != nil || len(tiedPage.Items) != 2 || tiedPage.Items[0].Release.ID != "release-a" {
		t.Fatalf("stable release tie-break = %#v, %v", tiedPage, err)
	}
	for _, cursor := range []string{"invalid", "offset:-1", strings.Repeat("x", MaxCatalogCursorBytes+1)} {
		if _, err := search(actor, records, SearchQuery{Cursor: cursor}); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Errorf("cursor %q returned %v", cursor, err)
		}
	}

	tampered := first.NextCursor[:len(first.NextCursor)-1] + "A"
	if strings.HasSuffix(first.NextCursor, "A") {
		tampered = first.NextCursor[:len(first.NextCursor)-1] + "B"
	}
	for name, attempt := range map[string]struct {
		actor   identity.Actor
		records []CatalogRecord
		query   SearchQuery
	}{
		"tampered":       {actor: actor, records: records, query: SearchQuery{Text: "invoice", Limit: 1, Cursor: tampered}},
		"another query":  {actor: actor, records: records, query: SearchQuery{Text: "invoice review", Limit: 1, Cursor: first.NextCursor}},
		"another size":   {actor: actor, records: records, query: SearchQuery{Text: "invoice", Limit: 2, Cursor: first.NextCursor}},
		"another actor":  {actor: identity.Actor{MembershipID: "00000000-0000-7000-8000-0000f9144771", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: actor.TenantID, Subject: "reader-2", Roles: actor.Roles}, records: records, query: request},
		"changed grants": {actor: identity.Actor{MembershipID: "00000000-0000-7000-8000-0000503b54c4", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: actor.TenantID, Subject: actor.Subject, Roles: actor.Roles, Namespaces: map[string][]identity.Role{"payments": {identity.RoleReader}}}, records: records, query: request},
	} {
		if _, err := search(attempt.actor, attempt.records, attempt.query); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Errorf("cursor accepted for %s: %v", name, err)
		}
	}
	changedRecords := append([]CatalogRecord(nil), records...)
	changedRecords[0].Release.Revision++
	if _, err := search(actor, changedRecords, request); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("cursor accepted after result-set change: %v", err)
	}
	searchClock.now = now.Add(catalogSearchCursorTTL + time.Second)
	if _, err := search(actor, records, request); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("expired cursor was accepted: %v", err)
	}
}

func TestPublicationRecordsFailClosedOnVisibilityIdentityAndDomain(t *testing.T) {
	t.Parallel()

	approved := func(id, identifier string, visibility Visibility, publisherID string) CatalogRecord {
		return CatalogRecord{
			Resource:        Resource{ID: "resource-" + id, CanonicalIdentifier: identifier},
			ListingRevision: ListingRevision{ID: "listing-" + id, PublisherID: publisherID, Visibility: visibility},
			Release:         ListingRelease{ID: "release-" + id},
		}
	}
	repository := &publicationRecordsRepository{
		MemoryRepository: NewMemoryRepository(),
		records: []CatalogRecord{
			approved("public", "urn:air:approved.example:default:public", VisibilityPublic, "publisher-approved"),
			approved("tenant", "urn:air:approved.example:default:tenant", VisibilityTenant, "publisher-approved"),
			approved("private", "urn:air:approved.example:default:private", VisibilityPrivate, "publisher-approved"),
			approved("invalid", "not-an-air-identifier", VisibilityPublic, "publisher-approved"),
			approved("opaque-tenant", "internal-tenant-agent", VisibilityTenant, "publisher-approved"),
			approved("opaque-private", "internal-private-agent", VisibilityPrivate, "publisher-approved"),
			approved("unapproved", "urn:air:unapproved.example:default:item", VisibilityPublic, "publisher-approved"),
		},
	}
	service := &Service{repo: repository, clock: &guardrailClock{now: time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)}}
	records, overflow, err := service.PublicationRecords(context.Background(), "tenant", true, 10)
	if err != nil || overflow || len(records) != 3 || records[0].Release.ID != "release-public" || records[1].Release.ID != "release-tenant" || records[2].Release.ID != "release-unapproved" {
		t.Fatalf("publication records = %#v, %v", records, err)
	}

	// The repository port owns anonymous visibility selection; retain only its
	// public candidates before checking the service's independent AIR admission.
	allRecords := repository.records
	repository.records = nil
	for _, record := range allRecords {
		if record.ListingRevision.Visibility == VisibilityPublic {
			repository.records = append(repository.records, record)
		}
	}
	anonymous, anonymousOverflow, anonymousErr := service.PublicationRecords(context.Background(), "tenant", false, 10)
	if anonymousErr != nil || anonymousOverflow || len(anonymous) != 2 || anonymous[0].Release.ID != "release-public" || anonymous[1].Release.ID != "release-unapproved" {
		t.Fatalf("anonymous publication records = %#v, %v", anonymous, anonymousErr)
	}
	repository.records = allRecords

	recordsErr := errors.New("records unavailable")
	repository.recordsErr = recordsErr
	if _, _, err := service.PublicationRecords(context.Background(), "tenant", true, 10); !errors.Is(err, recordsErr) {
		t.Fatalf("record failure = %v", err)
	}
	if _, err := service.PublicRecords(context.Background(), "tenant"); !errors.Is(err, recordsErr) {
		t.Fatalf("public record failure = %v", err)
	}
	repository.recordsErr = nil
	repository.overflow = true
	if records, overflow, err := service.PublicationRecords(context.Background(), "tenant", true, 10); err != nil || !overflow || records != nil {
		t.Fatalf("publication overflow = %#v, %t, %v", records, overflow, err)
	}
	if _, err := service.PublicRecords(context.Background(), "tenant"); ErrorCodeOf(err) != CodeWorkLimitExceeded {
		t.Fatalf("public record overflow = %v", err)
	}
	repository.overflow = false
	repository.records = []CatalogRecord{approved("cancelled", "urn:air:approved.example:default:cancelled", VisibilityPublic, "publisher-approved")}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := service.PublicationRecords(cancelled, "tenant", true, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled publication projection = %v", err)
	}
}

func TestCatalogFacetContracts(t *testing.T) {
	t.Parallel()

	record := CatalogRecord{
		Resource:        Resource{Kind: KindAgent},
		Listing:         CatalogListing{NamespaceID: "finance"},
		ListingRevision: ListingRevision{Visibility: VisibilityPublic, PublisherID: "publisher-example", Tags: []string{"finance", "integration"}, Categories: []string{"accounts-payable"}},
		Evidence:        EvidenceSet{Trust: TrustSummary{State: TrustVerified}},
		Source:          "local",
	}
	expected := map[string]string{
		"kind":       "agent",
		"visibility": "public",
		"publisher":  "publisher-example",
		"tag":        "finance,integration",
		"category":   "accounts-payable",
		"namespace":  "finance",
		"trustState": "verified",
		"source":     "local",
	}
	for field, want := range expected {
		if !validCatalogFacet(field) {
			t.Errorf("supported facet %q was rejected", field)
		}
		if got := strings.Join(catalogFacetValues(record.Summary(), field), ","); got != want {
			t.Errorf("facet %q = %q, want %q", field, got, want)
		}
	}
	if validCatalogFacet("owner") || catalogFacetValues(record.Summary(), "owner") != nil {
		t.Fatal("unsupported facet was accepted")
	}
}

func TestDomainErrorPreservesCodeAndCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("dependency failed")
	wrapped := Wrap(CodeDependencyUnavailable, "load catalog", cause)
	if !errors.Is(wrapped, cause) || ErrorCodeOf(fmt.Errorf("request failed: %w", wrapped)) != CodeDependencyUnavailable {
		t.Fatalf("wrapped error lost its cause or code: %v", wrapped)
	}
	if got := wrapped.Error(); got != "DEPENDENCY_UNAVAILABLE: load catalog: dependency failed" {
		t.Fatalf("wrapped error text = %q", got)
	}
	plain := E(CodeNotFound, "resource missing")
	if errors.Unwrap(plain) != nil || ErrorCodeOf(plain) != CodeNotFound || plain.Error() != "NOT_FOUND: resource missing" {
		t.Fatalf("plain domain error = %v", plain)
	}
	if ErrorCodeOf(cause) != CodeInternal {
		t.Fatal("non-domain error did not map to the internal code")
	}
	if PreserveEvidence(nil) != nil {
		t.Fatal("nil evidence error was wrapped")
	}
	preserved := PreserveEvidence(wrapped)
	if !ErrorPreservesEvidence(preserved) || preserved.Error() != wrapped.Error() || !errors.Is(preserved, cause) {
		t.Fatalf("preserved error lost its marker, text, or cause: %v", preserved)
	}
	var marked, again *preservedEvidenceError
	markedOK := errors.As(preserved, &marked)
	againOK := errors.As(PreserveEvidence(preserved), &again)
	if !markedOK || !againOK || again != marked {
		t.Fatal("preserved error was wrapped more than once")
	}
}
