// Hybrid/live merge tests use bounded in-memory pages and the existing search ports.
package catalog

import (
	"context"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"slices"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// hybridDurablePage uses distinct fused ranks that round to the same display
// score; sorting that score by name would reverse the durable order.
func hybridDurablePage() SemanticSearchPage {
	local := unifiedLocalRecord("release-a", "urn:air:local.example:default:a", "Local", 99)
	local.ListingRevision.Description = "Local description"
	local.ListingRevision.Tags = []string{"catalog"}
	indexed := CatalogSearchResult{ID: IndexedSearchResultID("urn:air:remote.example:default:b", "1", true),
		Type: SearchResultIndexed, Identifier: "urn:air:remote.example:default:b", Version: "1", VersionPresent: true,
		Kind: KindAgent, DisplayName: "Indexed", Score: 99, Indexed: &IndexedSearchEvidence{SourceIDs: []string{"source-b"}, SourceCount: 1}}
	return SemanticSearchPage{Total: 2, PartialIndex: true, Items: []SemanticSearchItem{
		{Local: &local, Position: SemanticSearchPosition{Rank: 32786885246, SortName: "local", ID: "local:release-a"}},
		{Result: indexed, Position: SemanticSearchPosition{Rank: 32513661202, SortName: "indexed", ID: indexed.ID}},
	}}
}

// hybridLiveResult supplies identity and source evidence already selected by
// the live-search port; transport decoding belongs to federation tests.
func hybridLiveResult(identifier, version string, present bool, score int) CatalogSearchResult {
	return CatalogSearchResult{ID: LiveSearchResultID("https://remote.example", identifier, version, present, "digest"),
		Type: SearchResultLive, Identifier: identifier, Version: version, VersionPresent: present,
		Kind: KindAgent, DisplayName: "Live", Score: score,
		Live: &LiveSearchEvidence{SourceID: "source-live", Source: "https://remote.example"}}
}

// TestUnifiedHybridMerge preserves durable rank and deduplicates exact live
// identities while retaining different versions and version-presence states.
func TestUnifiedHybridMerge(t *testing.T) {
	s, f, actor := newSemanticSearchTest(t)
	f.page = hybridDurablePage()
	local := f.page.Items[0].Local
	identifier := local.Resource.CanonicalIdentifier
	versioned := hybridLiveResult(identifier, "2", true, 99)
	emptyVersion := hybridLiveResult(identifier, "", true, 98)
	unversioned := hybridLiveResult(identifier, "", false, 97)
	lowerDuplicate := versioned
	lowerDuplicate.Score = 1
	lowerDuplicate.ID = LiveSearchResultID("https://other.example", identifier, "2", true, "other-digest")
	lowerDuplicate.Live = &LiveSearchEvidence{SourceID: "source-other", Source: "https://other.example"}
	live := []CatalogSearchResult{lowerDuplicate, hybridLiveResult(identifier, "1", true, 100), unversioned,
		hybridLiveResult(f.page.Items[1].Result.Identifier, "1", true, 100), emptyVersion, versioned}
	calls := 0
	federated := snapshotLiveSearcher{live: func(ctx context.Context, request FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		calls++
		gotActor, ok := identity.FromContext(ctx)
		if !ok || gotActor.TenantID != actor.TenantID || f.searches != 1 || !request.IncludeLive || request.Limit != 6 || request.Query.Text != "find tools" || !slices.Equal(request.Query.Tags, []string{"a", "b"}) || request.After != nil {
			t.Fatalf("live request before durable selection or with changed inputs: %+v", request)
		}
		return FederatedCatalogSearchPage{Live: live}, nil
	}}
	service := &SearchService{local: s, federated: federated}
	got, err := service.Search(identityfixture.WithActor(t.Context(), actor), SearchQuery{Text: " find  tools ", Tags: []string{"B", "a", "b"}, Limit: 6})
	if err != nil {
		t.Fatal(err)
	}
	wantLocal := CatalogSearchResult{ID: "local:release-a", Type: SearchResultLocal, Identifier: identifier,
		Version: "1", VersionPresent: true, Kind: KindAgent, DisplayName: "Local", Description: "Local description",
		Tags: []string{"catalog"}, Publisher: "publisher-local", Score: 99, Local: new(local.SearchLocal())}
	want := []CatalogSearchResult{wantLocal, f.page.Items[1].Result, versioned, emptyVersion, unversioned}
	if !reflect.DeepEqual(got.Items, want) || calls != 1 || got.LiveCount != 3 || got.DurableTotal != 2 || got.Partial || !got.PartialIndex || got.NextCursor != "" || len(got.Diagnostics) != 0 || got.Tier != "hybrid" || got.Profile != s.semantic.options.RankingProfile || got.Generation == "" || got.FallbackReason != "" {
		t.Fatalf("hybrid/live response = %+v; want items %+v", got, want)
	}
	request := f.searchRequest
	if request.LocalOnly || request.After != nil || request.Generation != f.g || !slices.Equal(request.Vector, f.vector) || request.Cutoff != s.semantic.options.Cutoff || request.Query.Text != "find tools" || request.Query.Limit != 6 || !slices.Equal(request.Query.Tags, []string{"a", "b"}) {
		t.Fatalf("semantic request = %+v", request)
	}
	got.Items[0].Tags[0] = "changed response"
	if local.ListingRevision.Tags[0] != "catalog" {
		t.Fatal("local projection aliases retained listing tags")
	}
}

// TestUnifiedHybridLiveBounds distinguishes omitted unique results from
// duplicate results and preserves upstream partial-failure diagnostics.
func TestUnifiedHybridLiveBounds(t *testing.T) {
	for _, mode := range []string{"empty", "duplicates only", "overflow", "upstream partial", "no live port"} {
		t.Run(mode, func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			f.page = hybridDurablePage()
			livePage := FederatedCatalogSearchPage{}
			diagnostic := CatalogSearchDiagnostic{SourceID: "source-offline", Status: "unavailable", Message: "Source unavailable."}
			wantCount, wantPartial := 0, mode == "no live port"
			switch mode {
			case "duplicates only":
				livePage.Live = []CatalogSearchResult{hybridLiveResult(f.page.Items[0].Local.Resource.CanonicalIdentifier, "1", true, 100)}
			case "overflow":
				livePage.Live = []CatalogSearchResult{hybridLiveResult("urn:air:remote.example:default:c", "1", true, 90), hybridLiveResult("urn:air:remote.example:default:d", "1", true, 80)}
				wantCount, wantPartial = 1, true
				livePage.Partial, livePage.Diagnostics = true, []CatalogSearchDiagnostic{diagnostic}
			case "upstream partial":
				livePage.Partial, livePage.Diagnostics, wantPartial = true, []CatalogSearchDiagnostic{diagnostic}, true
			}
			calls := 0
			var federated FederatedCatalogSearcher = snapshotLiveSearcher{live: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
				calls++
				return livePage, nil
			}}
			if mode == "no live port" {
				federated = unifiedFederatedSearcher{search: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
					t.Fatal("hybrid search repeated durable federation")
					return FederatedCatalogSearchPage{}, nil
				}}
			}
			got, err := (&SearchService{local: s, federated: federated}).Search(identityfixture.WithActor(t.Context(), actor), SearchQuery{Text: "find", Limit: 3})
			if err != nil || got.LiveCount != wantCount || len(got.Items) != 2+wantCount || got.Partial != wantPartial || got.DurableTotal != 2 || got.NextCursor != "" || got.Tier != "hybrid" || (mode != "no live port" && calls != 1) {
				t.Fatalf("response = %+v, %v; calls=%d", got, err, calls)
			}
			wantDiagnostics := append([]CatalogSearchDiagnostic(nil), livePage.Diagnostics...)
			if mode == "no live port" {
				wantDiagnostics = []CatalogSearchDiagnostic{{Status: "failed", Message: "Live federation was unavailable within the request deadline."}}
			}
			if mode == "overflow" {
				if got.Items[2].ID != livePage.Live[0].ID || len(got.Diagnostics) != 2 || got.Diagnostics[1].Status != "live_results_limited" || got.Diagnostics[1].Message == "" {
					t.Fatalf("live overflow result or diagnostic lost: %+v", got)
				}
				wantDiagnostics = append(wantDiagnostics, got.Diagnostics[1])
			}
			if !reflect.DeepEqual(got.Diagnostics, wantDiagnostics) {
				t.Fatalf("diagnostics = %+v; want %+v", got.Diagnostics, wantDiagnostics)
			}
		})
	}
}

// TestUnifiedHybridContinuation keeps the exact durable position even when
// the first page omits transient live results; subsequent pages never call live.
func TestUnifiedHybridContinuation(t *testing.T) {
	s, f, actor := newSemanticSearchTest(t)
	f.page = hybridDurablePage()
	f.page.Total, f.page.HasMore = 3, true
	last := f.page.Items[1].Position
	liveCalls := 0
	service := &SearchService{local: s, federated: snapshotLiveSearcher{live: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		liveCalls++
		return FederatedCatalogSearchPage{Live: []CatalogSearchResult{hybridLiveResult("urn:air:remote.example:default:c", "1", true, 100)}}, nil
	}}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	query := SearchQuery{Text: "find", Limit: 2}
	first, err := service.Search(ctx, query)
	if err != nil || first.NextCursor == "" || !first.Partial || first.LiveCount != 0 || len(first.Items) != 2 || len(first.Diagnostics) != 1 || first.Diagnostics[0].Status != "live_results_limited" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	final := unifiedLocalRecord("release-final", "urn:air:local.example:default:final", "Final", 10)
	f.page.Items = []SemanticSearchItem{{Local: &final}}
	f.page.HasMore = false
	query.Cursor = first.NextCursor
	second, err := service.Search(ctx, query)
	if err != nil || second.Tier != "hybrid" || second.Profile != first.Profile || second.Generation != first.Generation || second.DurableTotal != 3 || len(second.Items) != 1 || second.Items[0].ID != "local:release-final" || second.NextCursor != "" || second.Partial || second.LiveCount != 0 || len(second.Diagnostics) != 0 || liveCalls != 1 || f.searches != 2 || f.searchRequest.After == nil || *f.searchRequest.After != last {
		t.Fatalf("continuation = %+v, %v; live calls=%d request=%+v", second, err, liveCalls, f.searchRequest)
	}
}

// TestUnifiedHybridErrors propagates store, live and caller failures without
// returning a partial success or repeating a dependency call.
func TestUnifiedHybridErrors(t *testing.T) {
	for _, mode := range []string{"semantic store", "live", "live cancellation"} {
		t.Run(mode, func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			failure := errors.New("injected dependency failure")
			ctx, cancel := context.WithCancel(identityfixture.WithActor(t.Context(), actor))
			defer cancel()
			wantCalls := 1
			if mode == "semantic store" {
				f.searchError, wantCalls = failure, 0
			}
			calls := 0
			service := &SearchService{local: s, federated: snapshotLiveSearcher{live: func(ctx context.Context, _ FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
				calls++
				if mode == "live cancellation" {
					cancel()
					return FederatedCatalogSearchPage{}, ctx.Err()
				}
				return FederatedCatalogSearchPage{}, failure
			}}}
			got, err := service.Search(ctx, SearchQuery{Text: "find", Limit: 1})
			if mode == "live cancellation" {
				failure = context.Canceled
			}
			if mode == "live" {
				if err != nil || !got.Partial || len(got.Items) != 1 || got.NextCursor == "" || len(got.Diagnostics) != 1 || got.Diagnostics[0].Status != "failed" || calls != 1 {
					t.Fatalf("healthy durable page lost: %+v %v", got, err)
				}
				return
			}
			if !errors.Is(err, failure) || calls != wantCalls || f.searches != 1 || !reflect.DeepEqual(got, CatalogSearchResponse{}) {
				t.Fatalf("failure response = %+v, %v; calls=%d", got, err, calls)
			}
		})
	}
}
