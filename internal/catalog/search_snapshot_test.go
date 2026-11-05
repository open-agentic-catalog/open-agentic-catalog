package catalog

import (
	"context"
	"errors"
	"testing"
)

type snapshotSearchRepository struct {
	*unifiedSearchRepository
	snapshot func(context.Context, string, CatalogSearchRepositoryRequest, FederatedCatalogSearchRequest) (CatalogSearchSnapshot, error)
}

func (r snapshotSearchRepository) SearchCatalogSnapshot(ctx context.Context, tenant string, local CatalogSearchRepositoryRequest, indexed FederatedCatalogSearchRequest) (CatalogSearchSnapshot, error) {
	if local.IdentityProbes != nil {
		page, err := r.SearchCatalog(ctx, tenant, local)
		return CatalogSearchSnapshot{Local: page, Indexed: FederatedCatalogSearchPage{Generation: indexed.ExpectedGeneration}}, err
	}
	return r.snapshot(ctx, tenant, local, indexed)
}

type snapshotLiveSearcher struct {
	unifiedFederatedSearcher
	live func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error)
}

func (s snapshotLiveSearcher) SearchLiveCatalog(ctx context.Context, request FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
	return s.live(ctx, request)
}

func TestUnifiedSearchUsesCompletedDurableSnapshotBeforeLive(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "snapshot error", "live error", "cancel", "mismatched generation", "missing generation"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(unifiedReaderContext())
			defer cancel()
			failure := errors.New("injected search failure")
			complete, liveCalls := false, 0
			repository := snapshotSearchRepository{unifiedSearchRepository: &unifiedSearchRepository{search: func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
				t.Fatal("snapshot search repeated the independent local read")
				return CatalogSearchRepositoryPage{}, nil
			}}}
			repository.snapshot = func(_ context.Context, tenant string, local CatalogSearchRepositoryRequest, indexed FederatedCatalogSearchRequest) (CatalogSearchSnapshot, error) {
				if tenant != "tenant-1" || local.Limit != 3 || indexed.Limit != 3 || local.Query.Text != "needle" || indexed.Query.Text != "needle" || local.After != nil || indexed.After != nil || !indexed.IncludeLive {
					t.Fatal("snapshot changed authorization/query/page inputs")
				}
				if mode == "snapshot error" {
					return CatalogSearchSnapshot{}, failure
				}
				page := CatalogSearchSnapshot{Local: CatalogSearchRepositoryPage{Generation: "42", Total: 1}, Indexed: FederatedCatalogSearchPage{Generation: "42", IndexedTotal: 2}}
				if mode == "mismatched generation" {
					page.Indexed.Generation = "43"
				}
				if mode == "missing generation" {
					page.Local.Generation, page.Indexed.Generation = "", ""
				}
				if mode == "cancel" {
					cancel()
				}
				complete = true
				return page, nil
			}
			federated := snapshotLiveSearcher{unifiedFederatedSearcher: unifiedFederatedSearcher{search: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
				t.Fatal("snapshot search repeated indexed selection")
				return FederatedCatalogSearchPage{}, nil
			}}, live: func(_ context.Context, request FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
				liveCalls++
				if !complete || !request.IncludeLive || request.Query.Text != "needle" {
					t.Fatal("live discovery preceded snapshot completion or changed inputs")
				}
				if mode == "live error" {
					return FederatedCatalogSearchPage{}, failure
				}
				return FederatedCatalogSearchPage{Generation: "ignored", IndexedTotal: 99, Live: []CatalogSearchResult{{ID: "live:one", Type: SearchResultLive, Identifier: "urn:air:remote.example:default:one", Live: &LiveSearchEvidence{SourceID: "source"}}}, Partial: true, Diagnostics: []CatalogSearchDiagnostic{{Status: "unavailable"}}}, nil
			}}
			page, err := newUnifiedSearchTestService(t, repository, federated).Search(ctx, SearchQuery{Text: " needle ", Limit: 2})
			switch mode {
			case "success":
				if err != nil || page.DurableTotal != 3 || page.Generation != combinedSearchGeneration("42", "42") || page.LiveCount != 1 || !page.Partial || len(page.Diagnostics) != 1 || liveCalls != 1 {
					t.Fatalf("snapshot/live merge: %+v %v calls=%d", page, err, liveCalls)
				}
			case "snapshot error", "live error":
				if !errors.Is(err, failure) || liveCalls != map[string]int{"snapshot error": 0, "live error": 1}[mode] {
					t.Fatalf("dependency error: %v calls=%d", err, liveCalls)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) || liveCalls != 0 {
					t.Fatalf("cancellation: %v calls=%d", err, liveCalls)
				}
			default:
				if ErrorCodeOf(err) != CodeInternal || liveCalls != 0 {
					t.Fatalf("invalid snapshot: %v calls=%d", err, liveCalls)
				}
			}
		})
	}
}
