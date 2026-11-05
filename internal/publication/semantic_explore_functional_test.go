//go:build functional

package publication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/embeddings"
)

func TestPublicHybridExploreWithEmbeddingHTTP(t *testing.T) {
	manager, store, snapshot := newPublicHybridFixture(t)
	mutable := &observedPublicationRecordSource{fail: true}
	manager.records = mutable
	var unavailable atomic.Bool
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Input      []string `json:"input"`
			Model      string   `json:"model"`
			Encoding   string   `json:"encoding_format"`
			Dimensions int      `json:"dimensions"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" || request.Model != "fixture" || request.Encoding != "float" || request.Dimensions != 2 || !slices.Equal(request.Input, []string{"query:needle"}) {
			t.Errorf("unexpected embedding request: %+v, error=%v", request, err)
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		if unavailable.Load() {
			http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"fixture","data":[{"index":0,"embedding":[4,0]}]}`))
	}))
	defer server.Close()
	settings := config.SemanticDefaults()
	settings.Enabled, settings.AllowHTTP = true, true
	settings.Endpoint, settings.Model, settings.ProfileRevision = server.URL+"/v1/embeddings", "fixture", "public-explore"
	settings.Dimensions, settings.RequestedDimensions = 2, 2
	settings.QueryPrefix = "query:"
	settings.AllowedAddressPrefixes = []string{"127.0.0.1/32"}
	provider, err := embeddings.New(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	store.generation.Profile = settings.EmbeddingProfile()
	manager.semantic, err = catalog.NewSemanticSearch(catalog.SemanticSearchOptions{
		Store: store, Provider: provider, Profile: settings.EmbeddingProfile(), RankingProfile: settings.RankingProfile(),
		Dimensions: settings.Dimensions, Cutoff: settings.SimilarityCutoff, Timeout: settings.QueryTimeout.Duration,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for _, phase := range []string{"healthy", "unavailable", "recovered"} {
		t.Run(phase, func(t *testing.T) {
			outage := phase == "unavailable"
			unavailable.Store(outage)
			wantIDs := []string{
				"urn:air:catalog.example.com:default:beta",
				"urn:air:catalog.example.com:default:gamma",
				"urn:air:catalog.example.com:default:epsilon",
			}
			tier, fallback, providerState := "hybrid", "", "healthy"
			if outage {
				wantIDs = []string{wantIDs[0], wantIDs[2]}
				tier, fallback, providerState = "core", "provider_unavailable", "degraded"
			}
			beforeCalls, beforeComparisons := calls.Load(), store.comparisons
			query := publicHybridRequest().Query
			explored, err := manager.Explore(ctx, snapshot.TenantID, ExploreRequest{
				Query: &query, ResultType: ExploreResultType{Facets: []ExploreFacetRequest{{Field: "tags"}, {Field: "publisher"}}},
			})
			wantFacets := map[string]ExploreFacetResult{
				"tags":      {Buckets: []ExploreBucket{{Value: "selected", Count: len(wantIDs)}}},
				"publisher": {Buckets: []ExploreBucket{{Value: "catalog.example.com", Count: len(wantIDs)}}},
			}
			if err != nil || explored.ResultType != "facets" || explored.Tier != tier || explored.FallbackReason != fallback || explored.PartialIndex != !outage || !reflect.DeepEqual(explored.Facets, wantFacets) {
				t.Fatalf("Explore = %+v, error=%v; want %s/%s facets %+v", explored, err, tier, fallback, wantFacets)
			}
			comparisons := 1
			if outage {
				comparisons = 0
			}
			if calls.Load() != beforeCalls+1 || provider.State() != providerState || store.comparisons != beforeComparisons+comparisons {
				t.Fatalf("Explore did not use the expected HTTP/comparison boundary: calls=%d state=%s comparisons=%d", calls.Load()-beforeCalls, provider.State(), store.comparisons-beforeComparisons)
			}
			// Check Explore before Search overwrites the recording store's inputs.
			if store.queries != 0 || store.lookupTenant != snapshot.TenantID || store.lookupScope != "public:"+snapshot.ID || store.lookupProfile != settings.EmbeddingProfile() || store.lookupDimensions != 2 {
				t.Fatalf("Explore changed snapshot lookup bindings: %+v", store)
			}
			if !outage && (store.comparedGeneration != store.generation || !slices.Equal(store.comparedVector, []float32{1, 0}) || !slices.Equal(store.keys, []string{"entry:0000000002", "entry:0000000003", "entry:0000000004", "entry:0000000005"})) {
				t.Fatalf("Explore lost normalized vector, filters or retained ordinals: %+v", store)
			}

			// Explore includes all matches, including those beyond Search's first
			// page. Exact expected IDs keep agreement from masking shared errors.
			searchRequest := publicHybridRequest()
			searchRequest.PageSize = 1
			found := make([]string, 0, len(wantIDs))
			for page := range wantIDs {
				search, err := manager.Search(ctx, snapshot.TenantID, searchRequest)
				wantFallback := fallback
				if page > 0 {
					wantFallback = ""
				}
				if err != nil || search.Tier != tier || search.FallbackReason != wantFallback || search.PartialIndex != !outage || len(search.Results) != 1 {
					t.Fatalf("Search page %d = %+v, error=%v", page, search, err)
				}
				found = append(found, search.Results[0].Identifier)
				if (search.PageToken == "") != (page == len(wantIDs)-1) || (search.PageToken != "" && search.PageToken == searchRequest.PageToken) {
					t.Fatalf("Search page %d has an incorrect continuation", page)
				}
				searchRequest.PageToken = search.PageToken
			}
			if !slices.Equal(found, wantIDs) || explored.Facets["tags"].Buckets[0].Count != len(found) || explored.Facets["publisher"].Buckets[0].Count != len(found) {
				t.Fatalf("Search matched set %v disagrees with expected identities %v or Explore facets", found, wantIDs)
			}
			wantCalls, wantComparisons := int64(1+len(wantIDs)), 1+len(wantIDs)
			if outage {
				// The first Search request falls back; its lexical continuation
				// does not retry inference or change the established result set.
				wantCalls, wantComparisons = 2, 0
			}
			if calls.Load()-beforeCalls != wantCalls || store.comparisons-beforeComparisons != wantComparisons || store.queries != 0 || store.lookupTenant != snapshot.TenantID || store.lookupScope != "public:"+snapshot.ID || store.lookupProfile != settings.EmbeddingProfile() || store.lookupDimensions != 2 {
				t.Fatalf("unexpected HTTP or snapshot bindings: calls=%d comparisons=%d store=%+v", calls.Load()-beforeCalls, store.comparisons-beforeComparisons, store)
			}
			if !outage && (store.comparedGeneration != store.generation || !slices.Equal(store.comparedVector, []float32{1, 0}) || !slices.Equal(store.keys, []string{"entry:0000000002", "entry:0000000003", "entry:0000000004", "entry:0000000005"})) {
				t.Fatalf("comparison lost normalized vector, filters or retained ordinals: %+v", store)
			}
			// Preserve the complete unfiltered journey above, then qualify the
			// new native preselection through the same real HTTP adapter.
			query.Filter["data.capabilities.streaming"] = "false"
			query.Filter["data.defaultInputModes"] = "text/plain"
			query.Filter["data.defaultOutputModes"] = "text/plain"
			query.Filter["data.skills.tags"] = "weather"
			native, err := manager.Explore(ctx, snapshot.TenantID, ExploreRequest{Query: &query, ResultType: ExploreResultType{Facets: []ExploreFacetRequest{{Field: "data.capabilities.streaming"}, {Field: "data.skills.id"}, {Field: "data.defaultInputModes"}, {Field: "data.defaultOutputModes"}, {Field: "data.skills.tags"}}}})
			count := 2
			if outage {
				count = 1
			}
			if err != nil || native.Tier != tier || native.FallbackReason != fallback || !reflect.DeepEqual(native.Facets["data.capabilities.streaming"].Buckets, []ExploreBucket{{Value: "false", Count: count}}) || !reflect.DeepEqual(native.Facets["data.skills.id"].Buckets, []ExploreBucket{{Value: "forecast", Count: count}}) {
				t.Fatalf("native HTTP facets = %+v / %v", native, err)
			}
			for field, value := range map[string]string{"data.defaultInputModes": "text/plain", "data.defaultOutputModes": "text/plain", "data.skills.tags": "weather"} {
				if !reflect.DeepEqual(native.Facets[field].Buckets, []ExploreBucket{{Value: value, Count: count}}) {
					t.Fatalf("native HTTP facet %s = %+v", field, native.Facets[field])
				}
			}
			if !outage && !slices.Equal(store.keys, []string{"entry:0000000002", "entry:0000000005"}) {
				t.Fatalf("native preselection lost ordinals: %v", store.keys)
			}
			retained, err := manager.Active(ctx, snapshot.TenantID)
			if err != nil || !reflect.DeepEqual(retained, snapshot) || mutable.calls != 0 {
				t.Fatalf("public reads changed snapshot evidence or accessed mutable records: calls=%d error=%v", mutable.calls, err)
			}
		})
	}
}
