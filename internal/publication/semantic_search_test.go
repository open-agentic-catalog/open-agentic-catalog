// Public hybrid tests use the real coordinator with an immutable memory
// snapshot and recording semantic ports, without inference or SQL services.
package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type publicSemanticFixture struct {
	catalog.SemanticSearchStore
	catalog.SemanticProvider
	generation                               catalog.SemanticGeneration
	ready                                    bool
	activeError, providerError, compareError error
	similarities                             map[string]float64
	vector                                   []float32
	activeCalls, queries, comparisons        int
	lookupTenant, lookupProfile, lookupScope string
	lookupDimensions                         int
	queryText                                string
	comparedGeneration                       catalog.SemanticGeneration
	comparedVector                           []float32
	keys                                     []string
}

func (f *publicSemanticFixture) Active(_ context.Context, tenant, profile, scope string, dimensions int) (catalog.SemanticGeneration, bool, error) {
	f.activeCalls++
	f.lookupTenant, f.lookupProfile, f.lookupScope, f.lookupDimensions = tenant, profile, scope, dimensions
	return f.generation, f.ready, f.activeError
}

func (f *publicSemanticFixture) Query(_ context.Context, text string) ([]float32, error) {
	f.queries++
	f.queryText = text
	return f.vector, f.providerError
}

func (f *publicSemanticFixture) Similarities(_ context.Context, generation catalog.SemanticGeneration, vector []float32, keys []string) (map[string]float64, error) {
	f.comparisons++
	f.comparedGeneration, f.comparedVector, f.keys = generation, slices.Clone(vector), slices.Clone(keys)
	return f.similarities, f.compareError
}

// publicSemanticSearch constructs the concrete coordinator; alternate ranking
// profiles model a deployment change without replacing the ranking algorithm.
func publicSemanticSearch(t *testing.T, f *publicSemanticFixture, rankingProfile string) *catalog.SemanticSearch {
	t.Helper()
	s, err := catalog.NewSemanticSearch(catalog.SemanticSearchOptions{
		Store: f, Provider: f, Profile: f.generation.Profile, RankingProfile: rankingProfile,
		Dimensions: 2, Cutoff: .5, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// newPublicHybridFixture reuses publication's candidate/activation helpers.
// Beta matches both tiers, Gamma only semantic, Epsilon only lexical; Alpha is
// filtered out and Delta has no matching text or above-cutoff vector.
func newPublicHybridFixture(t *testing.T) (*Manager, *publicSemanticFixture, Snapshot) {
	t.Helper()
	records := make([]catalog.CatalogRecord, 0, 5)
	for _, name := range []string{"alpha", "beta", "delta", "epsilon", "gamma"} {
		record := eligiblePublicationRecord()
		record.Resource.ID, record.Version.ID, record.Release.ID = "resource-"+name, "version-"+name, "release-"+name
		record.Resource.CanonicalIdentifier = "urn:air:catalog.example.com:default:" + name
		record.ListingRevision.DisplayName = name
		record.ListingRevision.Description = "A published resource."
		record.ListingRevision.RepresentativeQueries = []string{"Find a resource", "Show resources"}
		record.ListingRevision.Tags = []string{"selected"}
		streaming := "true"
		if name == "beta" || name == "gamma" {
			streaming = "false"
		}
		mode, tag := "application/json", "other"
		if name == "beta" || name == "gamma" {
			mode, tag = "text/plain", "weather"
		}
		record.VersionRevision.Representations[0].Data = json.RawMessage(`{"defaultInputModes":["` + mode + `"],"defaultOutputModes":["` + mode + `"],"capabilities":{"streaming":` + streaming + `},"skills":[{"id":"forecast","tags":["` + tag + `","` + tag + `"]},{"id":"forecast"}]}`)
		if name == "alpha" || name == "beta" || name == "epsilon" {
			record.ListingRevision.DisplayName = "Needle " + name
		}
		if name == "alpha" {
			record.ListingRevision.Tags = []string{"excluded"}
		}
		records = append(records, record)
	}
	m, _, _ := newPublicationManager(t, records)
	snapshot := activatePublicationSnapshot(t, m)
	if err := ValidateSnapshotIntegrity(snapshot); err != nil || len(snapshot.ARD.Entries) != 6 {
		t.Fatalf("invalid public fixture: %v entries=%d", err, len(snapshot.ARD.Entries))
	}
	f := &publicSemanticFixture{
		generation: catalog.SemanticGeneration{ID: "00000000-0000-7000-8000-000000000901", TenantID: snapshot.TenantID,
			Profile: "sha256:" + strings.Repeat("a", 64), Scope: "public:" + snapshot.ID,
			SourceGeneration: strconv.FormatInt(snapshot.Generation, 10), Dimensions: 2},
		ready: true, vector: []float32{1, 0},
		similarities: map[string]float64{"entry:0000000001": 1, "entry:0000000002": .8, "entry:0000000003": .1, "entry:0000000005": .95},
	}
	m.semantic = publicSemanticSearch(t, f, "sha256:"+strings.Repeat("b", 64))
	m.records = failingPublicationRecords{err: errors.New("public search must not read mutable records")}
	return m, f, snapshot
}

// publicHybridRequest excludes the Registry self-entry and the otherwise
// strongest semantic candidate before comparison.
func publicHybridRequest() SearchRequest {
	return SearchRequest{Query: SearchQuery{Text: " needle ", Filter: map[string]any{"tags": []string{"selected"}}}, Federation: "none", PageSize: 2}
}

// TestPublicHybridRanking preserves retained ordinals through filtering and
// projects actual fused scores with this Registry's provenance.
func TestPublicHybridRanking(t *testing.T) {
	m, f, snapshot := newPublicHybridFixture(t)
	request := publicHybridRequest()
	request.PageSize = 10
	got, err := m.Search(t.Context(), snapshot.TenantID, request)
	if err != nil {
		t.Fatal(err)
	}
	// RRF60: Beta occurs at lexical rank 1 and semantic rank 2 (99);
	// Gamma at semantic rank 1 only (50), Epsilon at lexical rank 2 (49).
	want := []SearchResult{
		{Entry: snapshot.ARD.Entries[1], Score: 99, Source: "https://catalog.example.com/ard/v1/"},
		{Entry: snapshot.ARD.Entries[4], Score: 50, Source: "https://catalog.example.com/ard/v1/"},
		{Entry: snapshot.ARD.Entries[3], Score: 49, Source: "https://catalog.example.com/ard/v1/"},
	}
	if !reflect.DeepEqual(got.Results, want) || got.Tier != "hybrid" || !got.PartialIndex || got.FallbackReason != "" || got.PageToken != "" {
		t.Fatalf("ranked response = %+v; want %+v", got, want)
	}
	if f.lookupTenant != snapshot.TenantID || f.lookupScope != "public:"+snapshot.ID || f.lookupProfile != f.generation.Profile || f.lookupDimensions != 2 || f.comparedGeneration != f.generation || !slices.Equal(f.comparedVector, []float32{1, 0}) || f.queryText != "needle" || !slices.Equal(f.keys, []string{"entry:0000000002", "entry:0000000003", "entry:0000000004", "entry:0000000005"}) || f.activeCalls != 1 || f.queries != 1 || f.comparisons != 1 {
		t.Fatalf("comparison changed snapshot/filter bindings: %+v", f)
	}
	retained, err := m.Active(t.Context(), snapshot.TenantID)
	if err != nil || !reflect.DeepEqual(retained, snapshot) {
		t.Fatalf("search changed retained evidence: %v", err)
	}
}

// TestPublicHybridEmptyAndExactQueries avoids inference for empty filtered
// populations and exact identities; a successful empty hybrid match stays empty.
func TestPublicHybridEmptyAndExactQueries(t *testing.T) {
	for _, mode := range []string{"filtered empty", "no relevance", "exact identity"} {
		t.Run(mode, func(t *testing.T) {
			m, f, snapshot := newPublicHybridFixture(t)
			request := publicHybridRequest()
			switch mode {
			case "filtered empty":
				request.Query.Filter = map[string]any{"tags": []string{"absent"}}
			case "no relevance":
				request.Query.Text = "unmatched"
				f.similarities = map[string]float64{"entry:0000000002": .1, "entry:0000000003": .1, "entry:0000000004": .1, "entry:0000000005": .1}
			case "exact identity":
				request.Query.Text = snapshot.ARD.Entries[1].Identifier
			}
			got, err := m.Search(t.Context(), snapshot.TenantID, request)
			if err != nil || got.PageToken != "" || got.FallbackReason != "" || got.PartialIndex {
				t.Fatalf("response = %+v, %v", got, err)
			}
			if mode == "exact identity" {
				if got.Tier != "core" || len(got.Results) != 1 || got.Results[0].Identifier != request.Query.Text || got.Results[0].Score != 100 || f.activeCalls != 0 || f.queries != 0 {
					t.Fatalf("exact identity reached inference or changed result: %+v fixture=%+v", got, f)
				}
			} else if got.Tier != "hybrid" || len(got.Results) != 0 || f.activeCalls != 1 || (mode == "filtered empty" && (f.queries != 0 || f.comparisons != 0)) || (mode == "no relevance" && (f.queries != 1 || f.comparisons != 1)) {
				t.Fatalf("empty hybrid result misclassified: %+v fixture=%+v", got, f)
			}
		})
	}
}

// TestPublicHybridPaginationAndMembership preserves all matched identities,
// including results displaced by public live augmentation or a page boundary.
func TestPublicHybridPaginationAndMembership(t *testing.T) {
	m, _, snapshot := newPublicHybridFixture(t)
	request := publicHybridRequest()
	first, err := m.Search(t.Context(), snapshot.TenantID, request)
	if err != nil || len(first.Results) != 2 || !strings.HasPrefix(first.PageToken, "h1:") {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	for index, want := range map[int]bool{0: false, 1: true, 2: false, 3: true, 4: true} {
		entry := snapshot.ARD.Entries[index]
		if first.ContainsLocalIdentity(entry.Identifier, entry.Version) != want || first.ContainsLocalIdentity(entry.Identifier, "other-version") {
			t.Fatalf("membership for entry %d did not use complete exact matched set", index)
		}
	}
	if (SearchResponse{}).ContainsLocalIdentity(first.Results[0].Identifier, first.Results[0].Version) {
		t.Fatal("empty response claims a local identity")
	}
	request.PageToken = first.PageToken
	last, err := m.Search(t.Context(), snapshot.TenantID, request)
	if err != nil || last.Tier != "hybrid" || len(last.Results) != 1 || last.Results[0].Identifier != snapshot.ARD.Entries[3].Identifier || last.PageToken != "" || !last.ContainsLocalIdentity(first.Results[0].Identifier, first.Results[0].Version) {
		t.Fatalf("last page = %+v, %v", last, err)
	}
	for _, emitted := range []int{0, 1} {
		token, err := m.SearchContinuation(first, emitted)
		if err != nil || !strings.HasPrefix(token, "h1:") {
			t.Fatalf("hybrid rebase after %d results: %q, %v", emitted, token, err)
		}
		request.PageToken = token
		resumed, err := m.Search(t.Context(), snapshot.TenantID, request)
		if err != nil || len(resumed.Results) != 2 || resumed.Results[0].Identifier != first.Results[emitted].Identifier || resumed.Tier != "hybrid" {
			t.Fatalf("rebased page after %d results = %+v, %v", emitted, resumed, err)
		}
	}
}

// TestPublicHybridContinuationChanges uses a real signed first-page token to
// reject changed rank state or unavailable inference without lexical replay.
func TestPublicHybridContinuationChanges(t *testing.T) {
	for _, mode := range []string{"ranking profile", "index generation", "vector", "indexing", "source generation", "provider outage", "hinted provider outage", "comparison changed", "disabled", "tampered prefix"} {
		t.Run(mode, func(t *testing.T) {
			m, f, snapshot := newPublicHybridFixture(t)
			request := publicHybridRequest()
			first, err := m.Search(t.Context(), snapshot.TenantID, request)
			if err != nil || first.PageToken == "" {
				t.Fatalf("initial page = %+v, %v", first, err)
			}
			request.PageToken = first.PageToken
			want := catalog.CodeInvalidArgument
			switch mode {
			case "ranking profile":
				m.semantic = publicSemanticSearch(t, f, "sha256:"+strings.Repeat("c", 64))
			case "index generation":
				f.generation.ID = "00000000-0000-7000-8000-000000000902"
			case "vector":
				f.vector = []float32{0, 1}
			case "indexing":
				f.ready, want = false, catalog.CodeDependencyUnavailable
			case "source generation":
				f.generation.SourceGeneration, want = "999", catalog.CodeDependencyUnavailable
			case "provider outage":
				f.providerError, want = catalog.ErrEmbeddingUnavailable, catalog.CodeDependencyUnavailable
			case "hinted provider outage":
				f.providerError, want = &catalog.EmbeddingRetryError{RetryAfter: 2 * time.Minute}, catalog.CodeDependencyUnavailable
			case "comparison changed":
				f.compareError, want = catalog.ErrSearchGenerationChanged, catalog.CodeDependencyUnavailable
			case "disabled":
				m.semantic, want = nil, catalog.CodeDependencyUnavailable
			case "tampered prefix":
				request.PageToken = strings.Replace(request.PageToken, strings.Repeat("b", 64), strings.Repeat("c", 64), 1)
			}
			got, err := m.Search(t.Context(), snapshot.TenantID, request)
			if catalog.ErrorCodeOf(err) != want || !reflect.DeepEqual(got, SearchResponse{}) || (mode == "tampered prefix" && f.activeCalls != 1) {
				t.Fatalf("continuation returned %+v, %v; want %s", got, err, want)
			}
		})
	}
}

// TestPublicHybridFreshFallbackAndFailures keeps recoverable first-page fallback
// distinct from failures of the authoritative semantic generation/comparison store.
func TestPublicHybridFreshFallbackAndFailures(t *testing.T) {
	for _, mode := range []string{"indexing", "source generation", "provider outage", "comparison changed", "generation failure", "comparison failure"} {
		t.Run(mode, func(t *testing.T) {
			m, f, snapshot := newPublicHybridFixture(t)
			failure := errors.New("semantic store failed")
			reason, queries, comparisons := "indexing", 0, 0
			switch mode {
			case "indexing":
				f.ready = false
			case "source generation":
				f.generation.SourceGeneration = "999"
			case "provider outage":
				f.providerError, reason, queries = catalog.ErrEmbeddingUnavailable, "provider_unavailable", 1
			case "comparison changed":
				f.compareError, reason, queries, comparisons = catalog.ErrSearchGenerationChanged, "index_changed", 1, 1
			case "generation failure":
				f.activeError = failure
			case "comparison failure":
				f.compareError, queries, comparisons = failure, 1, 1
			}
			got, err := m.Search(t.Context(), snapshot.TenantID, publicHybridRequest())
			if mode == "generation failure" || mode == "comparison failure" {
				if !errors.Is(err, failure) || !reflect.DeepEqual(got, SearchResponse{}) {
					t.Fatalf("store failure became success: %+v, %v", got, err)
				}
			} else if err != nil || got.Tier != "core" || got.FallbackReason != reason || len(got.Results) != 2 || got.Results[0].Identifier != snapshot.ARD.Entries[1].Identifier || got.Results[1].Identifier != snapshot.ARD.Entries[3].Identifier || got.Results[0].Score != 95 || got.Results[1].Score != 95 {
				t.Fatalf("fresh fallback = %+v, %v", got, err)
			}
			if f.queries != queries || f.comparisons != comparisons {
				t.Fatalf("unexpected inference/comparison calls: %d/%d", f.queries, f.comparisons)
			}
		})
	}
}

// TestPublicHybridRecoveryPreservesLexicalContinuation prevents an index that
// becomes ready between pages from changing the established result order.
func TestPublicHybridRecoveryPreservesLexicalContinuation(t *testing.T) {
	m, f, snapshot := newPublicHybridFixture(t)
	f.ready = false
	request := publicHybridRequest()
	request.PageSize = 1
	first, err := m.Search(t.Context(), snapshot.TenantID, request)
	if err != nil || first.Tier != "core" || first.PageToken == "" || strings.HasPrefix(first.PageToken, "h1:") {
		t.Fatalf("lexical first page = %+v, %v", first, err)
	}
	f.ready = true
	request.PageToken = first.PageToken
	second, err := m.Search(t.Context(), snapshot.TenantID, request)
	if err != nil || second.Tier != "core" || second.FallbackReason != "" || len(second.Results) != 1 || second.Results[0].Identifier != snapshot.ARD.Entries[3].Identifier || second.PageToken != "" || f.activeCalls != 1 || f.queries != 0 || f.comparisons != 0 {
		t.Fatalf("recovery changed lexical continuation: %+v, %v; fixture=%+v", second, err, f)
	}
}

// TestPublicHybridComparisonBoundFallsBack keeps a valid maximum-size snapshot
// searchable when its Registry self-entry exceeds the vector comparison budget.
func TestPublicHybridComparisonBoundFallsBack(t *testing.T) {
	m, f, _ := newPublicHybridFixture(t)
	records := make([]catalog.CatalogRecord, catalog.MaxPublicationRecords)
	for i := range records {
		record := eligiblePublicationRecord()
		suffix := fmt.Sprintf("%04d", i)
		record.Resource.ID, record.Version.ID, record.Release.ID = "resource-"+suffix, "version-"+suffix, "release-"+suffix
		record.Resource.CanonicalIdentifier = "urn:air:catalog.example.com:default:agent-" + suffix
		record.ListingRevision.Tags = []string{"selected"}
		records[i] = record
	}
	m.records = publicationRecordSource{records: records}
	snapshot := activatePublicationSnapshot(t, m)
	if len(snapshot.ARD.Entries) != catalog.MaxPublicationRecords+1 {
		t.Fatalf("expected maximum resources plus Registry entry, got %d", len(snapshot.ARD.Entries))
	}
	f.generation.Scope, f.generation.SourceGeneration = "public:"+snapshot.ID, strconv.FormatInt(snapshot.Generation, 10)
	request := SearchRequest{Query: SearchQuery{Text: "agent"}, PageSize: 2}
	got, err := m.Search(t.Context(), snapshot.TenantID, request)
	if err != nil || got.Tier != "core" || got.FallbackReason != "work_limit" || len(got.Results) != 2 || got.PageToken == "" || f.activeCalls != 0 || f.queries != 0 || f.comparisons != 0 {
		t.Fatalf("maximum-size snapshot failed instead of falling back: %+v, %v; fixture=%+v", got, err, f)
	}
	explored, err := m.Explore(t.Context(), snapshot.TenantID, ExploreRequest{
		Query: &SearchQuery{Text: "agent"}, ResultType: ExploreResultType{Facets: []ExploreFacetRequest{{Field: "tags"}}},
	})
	wantFacets := map[string]ExploreFacetResult{"tags": {Buckets: []ExploreBucket{
		{Value: "selected", Count: catalog.MaxPublicationRecords},
		{Value: "catalog", Count: 1}, {Value: "discovery", Count: 1}, {Value: "registry", Count: 1},
	}}}
	if err != nil || explored.Tier != "core" || explored.FallbackReason != "work_limit" || !reflect.DeepEqual(explored.Facets, wantFacets) || f.activeCalls != 0 || f.queries != 0 || f.comparisons != 0 {
		t.Fatalf("maximum-size facets lost matches or reached inference: %+v, %v; fixture=%+v", explored, err, f)
	}
	// Filtering out the Registry self-entry reaches the exact comparison bound.
	request.Query.Filter = map[string]any{"tags": []string{"selected"}}
	got, err = m.Search(t.Context(), snapshot.TenantID, request)
	if err != nil || got.Tier != "hybrid" || got.FallbackReason != "" || len(got.Results) != 2 || len(f.keys) != catalog.MaxPublicationRecords || f.queries != 1 || f.comparisons != 1 {
		t.Fatalf("exact comparison limit rejected: %+v, %v; keys=%d", got, err, len(f.keys))
	}
}

// Explore has no ordered result list: distinct display-name buckets prove the
// exact hybrid membership, while ordinal comparison keys bind ranking to the
// active snapshot. Candidate construction/activation is not part of this unit.
func TestPublicHybridExploreFacetsUseActiveMatchedMembership(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		name := "hybrid"
		if fallback {
			name = "lexical fallback"
		}
		t.Run(name, func(t *testing.T) {
			m, store, clock := newPublicationManager(t, nil)
			at := clock.now
			snapshot := Snapshot{ID: "snapshot-explore", TenantID: "tenant-1", Host: m.host, Audience: m.audience, Generation: 7, ARDProfile: m.ardProfile, AICatalogProfile: m.aiCatalogProfile, State: SnapshotActive, CreatedAt: at, ActivatedAt: &at, ArtifactIDs: []string{}, AICatalog: AICatalog{SpecVersion: "1.0", Host: AICatalogHost{DisplayName: m.displayName, Identifier: "https://catalog.example.com"}}}
			for _, name := range []string{"alpha", "beta", "delta", "epsilon", "gamma"} {
				record := eligiblePublicationRecord()
				record.Resource.CanonicalIdentifier = "urn:air:catalog.example.com:default:" + name
				record.Release.ID = "release-" + name
				record.ListingRevision.DisplayName = name
				record.ListingRevision.Tags = []string{"selected", name}
				if name == "alpha" || name == "beta" || name == "epsilon" {
					record.ListingRevision.DisplayName = "Needle " + name
				}
				if name == "alpha" {
					record.ListingRevision.Tags = []string{"excluded", name}
				}
				entry, artifact, reasons := m.baseEntry(record)
				if artifact != "" || len(reasons) != 0 {
					t.Fatalf("invalid inline snapshot fixture: %q %v", artifact, reasons)
				}
				snapshot.ARD.Entries = append(snapshot.ARD.Entries, entry)
				snapshot.AICatalog.Entries = append(snapshot.AICatalog.Entries, toAICatalogEntry(entry))
			}
			snapshot.ARD.Entries = append(snapshot.ARD.Entries, m.registryEntry())
			rehashSnapshotDocuments(t, &snapshot)
			if err := m.validateSnapshotStructure(snapshot); err != nil {
				t.Fatalf("invalid retained fixture: %v", err)
			}
			store.snapshots[memorySnapshotKey(snapshot.TenantID, snapshot.ID)] = snapshot
			store.active[snapshot.TenantID] = snapshot.ID
			retainedBytes, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			f := &publicSemanticFixture{generation: catalog.SemanticGeneration{ID: "00000000-0000-7000-8000-000000000901", TenantID: snapshot.TenantID, Profile: "sha256:" + strings.Repeat("a", 64), Scope: "public:" + snapshot.ID, SourceGeneration: "7", Dimensions: 2}, ready: true, vector: []float32{1, 0}, similarities: map[string]float64{"entry:0000000001": 1, "entry:0000000002": .8, "entry:0000000003": .1, "entry:0000000005": .95}}
			if fallback {
				f.providerError = catalog.ErrEmbeddingUnavailable
			}
			m.semantic = publicSemanticSearch(t, f, "sha256:"+strings.Repeat("b", 64))
			source := &observedPublicationRecordSource{fail: true}
			m.records = source
			query := publicHybridRequest().Query
			got, err := m.Explore(t.Context(), snapshot.TenantID, ExploreRequest{Query: &query, ResultType: ExploreResultType{Facets: []ExploreFacetRequest{{Field: "displayName"}, {Field: "tags", Limit: 2}}}})
			want := ExploreResponse{Tier: "hybrid", PartialIndex: true, ResultType: "facets", Facets: map[string]ExploreFacetResult{
				"displayName": {Buckets: []ExploreBucket{{Value: "Needle beta", Count: 1}, {Value: "Needle epsilon", Count: 1}, {Value: "gamma", Count: 1}}},
				"tags":        {Buckets: []ExploreBucket{{Value: "selected", Count: 3}, {Value: "beta", Count: 1}}, OtherCount: 2},
			}}
			comparisons := 1
			if fallback {
				want.Tier, want.PartialIndex, want.FallbackReason = "core", false, "provider_unavailable"
				want.Facets["displayName"] = ExploreFacetResult{Buckets: []ExploreBucket{{Value: "Needle beta", Count: 1}, {Value: "Needle epsilon", Count: 1}}}
				want.Facets["tags"] = ExploreFacetResult{Buckets: []ExploreBucket{{Value: "selected", Count: 2}, {Value: "beta", Count: 1}}, OtherCount: 1}
				comparisons = 0
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("Explore membership/facets/diagnostics = %+v, %v; want %+v", got, err, want)
			}
			if f.activeCalls != 1 || f.queries != 1 || f.comparisons != comparisons || f.lookupTenant != "tenant-1" || f.lookupScope != "public:snapshot-explore" || f.lookupProfile != f.generation.Profile || f.lookupDimensions != 2 || f.queryText != "needle" || source.calls != 0 {
				t.Fatalf("Explore used wrong scope or mutable evidence: %+v sourceCalls=%d", f, source.calls)
			}
			if !fallback && (f.comparedGeneration != f.generation || !slices.Equal(f.comparedVector, []float32{1, 0}) || !slices.Equal(f.keys, []string{"entry:0000000002", "entry:0000000003", "entry:0000000004", "entry:0000000005"})) {
				t.Fatalf("comparison lost retained ordinals/filter/vector: %+v", f)
			}
			afterBytes, err := json.Marshal(store.snapshots[memorySnapshotKey(snapshot.TenantID, snapshot.ID)])
			if err != nil || string(afterBytes) != string(retainedBytes) {
				t.Fatal("Explore changed immutable retained evidence")
			}
		})
	}
}

func TestPublicHybridNativeFiltersApplyBeforeComparisonAndFacets(t *testing.T) {
	for field, value := range map[string]string{"data.capabilities.streaming": "false", "data.defaultInputModes": "text/plain", "data.defaultOutputModes": "text/plain", "data.skills.tags": "weather"} {
		t.Run(field, func(t *testing.T) {
			manager, fixture, snapshot := newPublicHybridFixture(t)
			request := publicHybridRequest()
			request.Query.Filter[field] = value
			request.PageSize = 10
			search, err := manager.Search(t.Context(), snapshot.TenantID, request)
			if err != nil || search.Tier != "hybrid" || len(search.Results) != 2 || !slices.Equal(fixture.keys, []string{"entry:0000000002", "entry:0000000005"}) {
				t.Fatalf("native preselection failed: %+v / %v, keys=%v", search, err, fixture.keys)
			}
			explore, err := manager.Explore(t.Context(), snapshot.TenantID, ExploreRequest{Query: &request.Query, ResultType: ExploreResultType{Facets: []ExploreFacetRequest{{Field: "data.capabilities.streaming"}, {Field: "data.skills.id"}, {Field: "data.defaultInputModes"}, {Field: "data.defaultOutputModes"}, {Field: "data.skills.tags"}}}})
			if err != nil || explore.Tier != "hybrid" || !reflect.DeepEqual(explore.Facets["data.capabilities.streaming"].Buckets, []ExploreBucket{{Value: "false", Count: 2}}) || !reflect.DeepEqual(explore.Facets["data.skills.id"].Buckets, []ExploreBucket{{Value: "forecast", Count: 2}}) {
				t.Fatalf("hybrid native facets = %+v / %v", explore, err)
			}
			for field, value := range map[string]string{"data.defaultInputModes": "text/plain", "data.defaultOutputModes": "text/plain", "data.skills.tags": "weather"} {
				if !reflect.DeepEqual(explore.Facets[field].Buckets, []ExploreBucket{{Value: value, Count: 2}}) {
					t.Fatalf("hybrid facet %s = %+v", field, explore.Facets[field])
				}
			}
		})
	}
}
