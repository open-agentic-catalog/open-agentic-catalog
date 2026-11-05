package catalog

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type unifiedSearchRepository struct {
	Repository
	search func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error)
	probe  func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error)
}

func (r *unifiedSearchRepository) SearchCatalog(ctx context.Context, tenantID string, request CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
	if request.IdentityProbes != nil {
		if r.probe != nil {
			return r.probe(ctx, tenantID, request)
		}
		return CatalogSearchRepositoryPage{Generation: request.ExpectedGeneration}, nil
	}
	return r.search(ctx, tenantID, request)
}

type unifiedFederatedSearcher struct {
	search func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error)
	probe  func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error)
}

func (s unifiedFederatedSearcher) SearchFederatedCatalog(ctx context.Context, request FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
	if request.IdentityProbes != nil {
		if s.probe != nil {
			return s.probe(ctx, request)
		}
		return FederatedCatalogSearchPage{Generation: request.ExpectedGeneration}, nil
	}
	return s.search(ctx, request)
}

func TestUnifiedCatalogSearchMergesDurableResultsAndDoesNotContinueLiveResults(t *testing.T) {
	t.Parallel()

	localItems := []CatalogSummary{
		unifiedLocalRecord("release-a", "urn:air:local.example:default:a", "Alpha", 90),
		unifiedLocalRecord("release-d", "urn:air:local.example:default:d", "Delta", 60),
	}
	indexed := CatalogSearchResult{ID: IndexedSearchResultID("urn:air:remote.example:default:b", "1", true), Type: SearchResultIndexed, Identifier: "urn:air:remote.example:default:b", Version: "1", VersionPresent: true, Kind: KindAgent, DisplayName: "Beta", Tags: []string{}, Score: 80, Indexed: &IndexedSearchEvidence{ObservationGroupID: "observation-group-v1:sha256:" + strings.Repeat("a", 64), NormalizedEntryDigest: "sha256:" + strings.Repeat("b", 64), SourceIDs: []string{"source-b"}, SourceCount: 1}}
	live := CatalogSearchResult{ID: LiveSearchResultID("https://remote.example", "urn:air:remote.example:default:c", "1", true, "sha256:"+strings.Repeat("c", 64)), Type: SearchResultLive, Identifier: "urn:air:remote.example:default:c", Version: "1", VersionPresent: true, Kind: KindAgent, DisplayName: "Charlie", Tags: []string{}, Score: 70, Live: &LiveSearchEvidence{SourceID: "source-c", Source: "https://remote.example", Entry: []byte(`{"identifier":"urn:air:remote.example:default:c"}`)}}

	var localRequests []CatalogSearchRepositoryRequest
	repository := &unifiedSearchRepository{search: func(_ context.Context, tenantID string, request CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		if tenantID != "tenant-1" {
			t.Fatalf("tenant = %q", tenantID)
		}
		localRequests = append(localRequests, request)
		items := localItems
		if request.After != nil {
			items = localItems[1:]
		}
		return CatalogSearchRepositoryPage{Items: append([]CatalogSummary(nil), items...), Total: 2, Generation: "11"}, nil
	}}
	var federatedRequests []FederatedCatalogSearchRequest
	federated := unifiedFederatedSearcher{search: func(_ context.Context, request FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		federatedRequests = append(federatedRequests, request)
		if request.After != nil {
			return FederatedCatalogSearchPage{Indexed: []CatalogSearchResult{}, IndexedTotal: 1, Generation: "11"}, nil
		}
		return FederatedCatalogSearchPage{Indexed: []CatalogSearchResult{indexed}, IndexedTotal: 1, Generation: "11", Live: []CatalogSearchResult{live}}, nil
	}}
	service := newUnifiedSearchTestService(t, repository, federated)
	ctx := unifiedReaderContext()

	first, err := service.Search(ctx, SearchQuery{Text: "  catalog   result ", Tags: []string{" B ", "a", "b"}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "local:release-a" || first.Items[1].ID != indexed.ID || first.NextCursor == "" || first.DurableTotal != 3 || first.LiveCount != 0 || !first.Partial || first.Profile != CoreSearchProfile || first.Tier != "core" || first.Generation == "" {
		t.Fatalf("first unified page = %#v", first)
	}
	if !slices.ContainsFunc(first.Diagnostics, func(value CatalogSearchDiagnostic) bool { return value.Status == "live_results_limited" }) {
		t.Fatalf("first-page diagnostics = %#v", first.Diagnostics)
	}
	if len(localRequests) != 1 || localRequests[0].Limit != 3 || localRequests[0].After != nil || localRequests[0].ExpectedGeneration != "" || localRequests[0].Query.Text != "catalog result" || !slices.Equal(localRequests[0].Query.Tags, []string{"a", "b"}) {
		t.Fatalf("first local request = %#v", localRequests)
	}
	if len(federatedRequests) != 1 || !federatedRequests[0].IncludeLive || federatedRequests[0].Limit != 3 || federatedRequests[0].After != nil {
		t.Fatalf("first federated request = %#v", federatedRequests)
	}

	second, err := service.Search(ctx, SearchQuery{Text: "catalog result", Tags: []string{"a", "b"}, Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "local:release-d" || second.NextCursor != "" || second.LiveCount != 0 || second.Partial || second.DurableTotal != 3 {
		t.Fatalf("second unified page = %#v", second)
	}
	if len(localRequests) != 2 || localRequests[1].After == nil || localRequests[1].ExpectedGeneration != "11" || len(federatedRequests) != 2 || federatedRequests[1].IncludeLive || federatedRequests[1].After == nil || federatedRequests[1].ExpectedGeneration != "11" {
		t.Fatalf("continuation requests = local %#v federated %#v", localRequests, federatedRequests)
	}
}

func TestUnifiedCatalogSearchPrefersLocalIdentityAndKeepsTypedVersionsDistinct(t *testing.T) {
	t.Parallel()

	identifier := "urn:air:example.com:default:shared"
	local := localCatalogSearchResult(unifiedLocalRecord("release-local", identifier, "Local", 10))
	indexed := CatalogSearchResult{ID: IndexedSearchResultID(identifier, "1", true), Type: SearchResultIndexed, Identifier: identifier, Version: "1", VersionPresent: true, DisplayName: "Indexed", Score: 99, Indexed: &IndexedSearchEvidence{}}
	live := CatalogSearchResult{ID: LiveSearchResultID("https://example.com", identifier, "1", true, "digest"), Type: SearchResultLive, Identifier: identifier, Version: "1", VersionPresent: true, DisplayName: "Live", Score: 100, Live: &LiveSearchEvidence{}}
	unversioned := indexed
	unversioned.ID = IndexedSearchResultID(identifier, "", false)
	unversioned.Version = ""
	unversioned.VersionPresent = false

	items := deduplicateCatalogResults([]CatalogSearchResult{indexed, unversioned, live, local})
	if len(items) != 2 || items[0].Type != SearchResultIndexed || items[0].VersionPresent || items[1].Type != SearchResultLocal || items[1].ID != local.ID {
		t.Fatalf("deduplicated results = %#v", items)
	}

	tied := []CatalogSearchResult{{ID: "result-b", Score: 50, DisplayName: "Same"}, {ID: "result-a", Score: 50, DisplayName: "Same"}}
	sortCatalogResults(tied)
	if tied[0].ID != "result-a" || tied[1].ID != "result-b" {
		t.Fatalf("ID tie ordering = %#v", tied)
	}
}

func TestUnifiedCatalogSearchReconcilesSharedPostgreSQLGeneration(t *testing.T) {
	t.Parallel()

	calls := 0
	repository := &unifiedSearchRepository{search: func(_ context.Context, _ string, request CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		calls++
		if calls == 1 {
			return CatalogSearchRepositoryPage{Items: []CatalogSummary{}, Generation: "41"}, nil
		}
		if request.ExpectedGeneration != "42" {
			t.Fatalf("re-read generation = %q", request.ExpectedGeneration)
		}
		return CatalogSearchRepositoryPage{Items: []CatalogSummary{}, Generation: "42"}, nil
	}}
	federated := unifiedFederatedSearcher{search: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		return FederatedCatalogSearchPage{
			Indexed:    []CatalogSearchResult{},
			Generation: "42",
			Live: []CatalogSearchResult{{
				ID:          "live:result",
				Type:        SearchResultLive,
				Identifier:  "urn:air:remote.example:default:live",
				DisplayName: "Live result",
				Live:        &LiveSearchEvidence{SourceID: "source-live"},
			}},
		}, nil
	}}
	service := newUnifiedSearchTestService(t, repository, federated)
	response, err := service.Search(unifiedReaderContext(), SearchQuery{})
	if err != nil || calls != 2 || response.Generation != combinedSearchGeneration("42", "42") || response.LiveCount != 1 || len(response.Items) != 1 {
		t.Fatalf("generation reconciliation = calls %d response %#v, %v", calls, response, err)
	}
}

func TestUnifiedCatalogSearchReconcilesRepeatedFirstPageGenerationChanges(t *testing.T) {
	t.Parallel()
	for _, changes := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(changes), func(t *testing.T) {
			localCalls, indexedCalls, liveCalls := 0, 0, 0
			repository := &unifiedSearchRepository{search: func(_ context.Context, _ string, request CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
				localCalls++
				if localCalls == 1 {
					return CatalogSearchRepositoryPage{Generation: "41"}, nil
				}
				if localCalls <= changes+1 {
					return CatalogSearchRepositoryPage{}, Wrap(CodeInvalidArgument, "catalog cursor is stale", ErrSearchGenerationChanged)
				}
				return CatalogSearchRepositoryPage{Generation: request.ExpectedGeneration}, nil
			}}
			federated := unifiedFederatedSearcher{search: func(_ context.Context, request FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
				indexedCalls++
				page := FederatedCatalogSearchPage{Generation: strconv.Itoa(41 + indexedCalls)}
				if request.IncludeLive {
					liveCalls++
					page.Live = []CatalogSearchResult{{ID: "live:one", Type: SearchResultLive, Identifier: "urn:air:remote.example:default:one", Live: &LiveSearchEvidence{SourceID: "source"}}}
					page.Partial = true
					page.Diagnostics = []CatalogSearchDiagnostic{{Status: "unavailable"}}
				}
				return page, nil
			}}
			service := newUnifiedSearchTestService(t, repository, federated)
			page, err := service.Search(unifiedReaderContext(), SearchQuery{})
			if changes == 3 {
				if !errors.Is(err, ErrSearchGenerationChanged) || localCalls != 4 || indexedCalls != 3 || liveCalls != 1 {
					t.Fatalf("unbounded reconciliation: local=%d indexed=%d live=%d err=%v", localCalls, indexedCalls, liveCalls, err)
				}
				return
			}
			generation := strconv.Itoa(42 + changes)
			if err != nil || page.Generation != combinedSearchGeneration(generation, generation) || liveCalls != 1 || indexedCalls != changes+1 || localCalls != changes+2 || page.LiveCount != 1 || !page.Partial || len(page.Diagnostics) != 1 {
				t.Fatalf("first-page reconciliation: local=%d indexed=%d live=%d page=%+v err=%v", localCalls, indexedCalls, liveCalls, page, err)
			}
		})
	}
}

func TestUnifiedCatalogSearchStopsReconciliationOnCancellationAndRefreshFailure(t *testing.T) {
	t.Parallel()
	for _, cancelFirst := range []bool{false, true} {
		ctx, cancel := context.WithCancel(unifiedReaderContext())
		localCalls, indexedCalls := 0, 0
		refreshFailure := errors.New("indexed refresh failed")
		repository := &unifiedSearchRepository{search: func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
			localCalls++
			if localCalls == 1 {
				return CatalogSearchRepositoryPage{Generation: "41"}, nil
			}
			return CatalogSearchRepositoryPage{}, Wrap(CodeInvalidArgument, "catalog cursor is stale", ErrSearchGenerationChanged)
		}}
		federated := unifiedFederatedSearcher{search: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
			indexedCalls++
			if indexedCalls > 1 {
				return FederatedCatalogSearchPage{}, refreshFailure
			}
			if cancelFirst {
				cancel()
			}
			return FederatedCatalogSearchPage{Generation: "42"}, nil
		}}
		service := newUnifiedSearchTestService(t, repository, federated)
		_, err := service.Search(ctx, SearchQuery{})
		cancel()
		if cancelFirst && (!errors.Is(err, context.Canceled) || localCalls != 1 || indexedCalls != 1) || !cancelFirst && (!errors.Is(err, refreshFailure) || localCalls != 2 || indexedCalls != 2) {
			t.Fatalf("cancel=%t local=%d indexed=%d err=%v", cancelFirst, localCalls, indexedCalls, err)
		}
	}
}

func TestUnifiedCatalogSearchRefreshReplacesDurablePageAndPreservesLiveEvidence(t *testing.T) {
	t.Parallel()
	localCalls, indexedCalls := 0, 0
	repository := &unifiedSearchRepository{search: func(_ context.Context, tenant string, request CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		localCalls++
		if tenant != "tenant-1" || request.Limit != 21 || request.After != nil || request.Query.Text != "catalog" {
			t.Fatal("reconciliation changed the tenant, query or bounded first-page selection")
		}
		switch localCalls {
		case 1:
			return CatalogSearchRepositoryPage{Items: []CatalogSummary{unifiedLocalRecord("stale", "urn:air:local.example:default:stale", "Stale", 100)}, Total: 1, Generation: "41"}, nil
		case 2:
			if request.ExpectedGeneration != "42" {
				t.Fatal("initial indexed generation was not selected")
			}
			return CatalogSearchRepositoryPage{}, Wrap(CodeInvalidArgument, "catalog cursor is stale", ErrSearchGenerationChanged)
		default:
			if localCalls != 3 || request.ExpectedGeneration != "43" {
				t.Fatal("unexpected reconciliation attempt")
			}
			return CatalogSearchRepositoryPage{Generation: "43"}, nil
		}
	}}
	indexed := CatalogSearchResult{ID: "indexed:new", Type: SearchResultIndexed, Identifier: "urn:air:remote.example:default:new", DisplayName: "Zulu", Indexed: &IndexedSearchEvidence{SourceCount: 1, SourceIDs: []string{"source"}}}
	live := CatalogSearchResult{ID: "live:one", Type: SearchResultLive, Identifier: "urn:air:remote.example:default:live", DisplayName: "alpha", Live: &LiveSearchEvidence{SourceID: "live-source"}}
	federated := unifiedFederatedSearcher{search: func(_ context.Context, request FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		indexedCalls++
		if request.Limit != 21 || request.Query.Text != "catalog" || request.After != nil || request.ExpectedGeneration != "" || request.IncludeLive != (indexedCalls == 1) {
			t.Fatal("refresh changed the query or repeated outbound live discovery")
		}
		if indexedCalls == 1 {
			return FederatedCatalogSearchPage{Indexed: []CatalogSearchResult{{ID: "indexed:stale", Identifier: "urn:air:remote.example:default:stale"}}, IndexedTotal: 99, HasMore: true, Generation: "42", Live: []CatalogSearchResult{live}, Partial: true, Diagnostics: []CatalogSearchDiagnostic{{Status: "unavailable"}}}, nil
		}
		if indexedCalls != 2 {
			t.Fatal("unexpected indexed refresh")
		}
		return FederatedCatalogSearchPage{Indexed: []CatalogSearchResult{indexed}, IndexedTotal: 1, Generation: "43"}, nil
	}}
	page, err := newUnifiedSearchTestService(t, repository, federated).Search(unifiedReaderContext(), SearchQuery{Text: " catalog "})
	if err != nil {
		t.Fatal(err)
	}
	if localCalls != 3 || indexedCalls != 2 || page.DurableTotal != 1 || page.NextCursor != "" || page.Generation != combinedSearchGeneration("43", "43") {
		t.Fatalf("stale generation, counts or continuation survived: %+v", page)
	}
	if !reflect.DeepEqual(page.Items, []CatalogSearchResult{live, indexed}) || page.LiveCount != 1 || !page.Partial || len(page.Diagnostics) != 1 || page.Diagnostics[0].Status != "unavailable" {
		t.Fatalf("durable refresh lost live evidence or retained stale results: %+v", page)
	}
}

func TestUnifiedCatalogSearchRejectsInvalidAuthorityInputAndDependencyFailures(t *testing.T) {
	t.Parallel()

	if _, err := NewSearchService(nil, unifiedFederatedSearcher{}); err == nil {
		t.Fatal("missing local dependency was accepted")
	}
	localFailure := errors.New("local failed")
	repository := &unifiedSearchRepository{search: func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		return CatalogSearchRepositoryPage{}, localFailure
	}}
	federatedCalls := 0
	federated := unifiedFederatedSearcher{search: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		federatedCalls++
		return FederatedCatalogSearchPage{}, errors.New("federated failed")
	}}
	service := newUnifiedSearchTestService(t, repository, federated)
	actor, _ := identity.FromContext(unifiedReaderContext())
	localBinding, err := service.local.searchCursorBinding(identity.WithActor(context.Background(), actor), actor, SearchQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	localCursor, err := service.local.encodeSearchCursor("local-generation", CatalogSearchPosition{Score: 1, DisplayName: "name", ResultID: "local:release"}, localBinding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Search(unifiedReaderContext(), SearchQuery{Cursor: localCursor}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("local cursor used for unified search returned %v", err)
	}
	unifiedBinding, err := service.local.unifiedSearchCursorBinding(identity.WithActor(context.Background(), actor), actor, SearchQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	incompleteUnifiedCursor, err := service.local.encodeSearchCursor("local-generation", CatalogSearchPosition{Score: 1, DisplayName: "name", ResultID: "local:release"}, unifiedBinding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Search(unifiedReaderContext(), SearchQuery{Cursor: incompleteUnifiedCursor}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("unified cursor without indexed generation returned %v", err)
	}
	for name, ctx := range map[string]context.Context{
		"missing actor":     context.Background(),
		"unbound principal": identity.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000048530f7", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: "tenant-1", Subject: "viewer"}),
	} {
		if _, err := service.Search(ctx, SearchQuery{}); ErrorCodeOf(err) != CodeForbidden {
			t.Fatalf("%s returned %v", name, err)
		}
	}
	for _, query := range []SearchQuery{{Tags: make([]string, 33)}, {Text: "invalid\x00query"}, {Tags: []string{"invalid\x00tag"}}} {
		if _, err := service.Search(unifiedReaderContext(), query); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("query %#v returned %v", query, err)
		}
	}
	if _, err := service.Search(unifiedReaderContext(), SearchQuery{}); !errors.Is(err, localFailure) || federatedCalls != 0 {
		t.Fatalf("local failure = federated calls %d, %v", federatedCalls, err)
	}

	repository.search = func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		return CatalogSearchRepositoryPage{Items: []CatalogSummary{}, Generation: "memory-local"}, nil
	}
	if _, err := service.Search(unifiedReaderContext(), SearchQuery{}); err == nil || err.Error() != "federated failed" {
		t.Fatalf("federated failure returned %v", err)
	}

	reconciliationFailure := errors.New("generation reconciliation failed")
	localCalls := 0
	repository.search = func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		localCalls++
		if localCalls == 1 {
			return CatalogSearchRepositoryPage{Items: []CatalogSummary{}, Generation: "41"}, nil
		}
		return CatalogSearchRepositoryPage{}, reconciliationFailure
	}
	service.federated = unifiedFederatedSearcher{search: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		return FederatedCatalogSearchPage{Indexed: []CatalogSearchResult{}, Generation: "42"}, nil
	}}
	if _, err := service.Search(unifiedReaderContext(), SearchQuery{}); !errors.Is(err, reconciliationFailure) || localCalls != 2 {
		t.Fatalf("generation reconciliation failure = local calls %d, %v", localCalls, err)
	}
}

func TestSharedNumericSearchGenerationRequiresTwoUnsignedNumbers(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		left  string
		right string
		want  bool
	}{
		{left: "0", right: "42", want: true},
		{left: "18446744073709551615", right: "1", want: true},
		{left: "", right: "1"},
		{left: "1", right: ""},
		{left: "memory", right: "1"},
		{left: "1", right: "memory"},
		{left: "18446744073709551616", right: "1"},
	} {
		if got := sharedNumericSearchGeneration(test.left, test.right); got != test.want {
			t.Fatalf("sharedNumericSearchGeneration(%q, %q) = %t, want %t", test.left, test.right, got, test.want)
		}
	}
}

func newUnifiedSearchTestService(t *testing.T, repository Repository, federated FederatedCatalogSearcher) *SearchService {
	t.Helper()
	key := sha256.Sum256([]byte(t.Name()))
	local := &Service{repo: repository, clock: &mutableCatalogSearchClock{now: time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)}, cursorKey: key[:], policy: "test-policy"}
	service, err := NewSearchService(local, federated)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func unifiedReaderContext() context.Context {
	return identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000870cd836", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: "tenant-1", Subject: "reader-1", Roles: []identity.Role{identity.RoleReader}})
}

func unifiedLocalRecord(releaseID, identifier, displayName string, score int) CatalogSummary {
	return CatalogSummary{
		Resource:        CatalogResourceSummary{CanonicalIdentifier: identifier, Kind: KindAgent},
		Version:         CatalogVersionSummary{Version: "1"},
		ListingRevision: CatalogDisplaySummary{DisplayName: displayName, Tags: []string{}, PublisherID: "publisher-local"},
		Release:         CatalogReleaseSummary{ID: releaseID},
		RelevanceScore:  score,
		Source:          "local",
	}
}
