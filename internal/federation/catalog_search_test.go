package federation

import (
	"context"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type catalogSearchFailureStore struct {
	*MemoryStore
	indexedErr error
	liveErr    error
}

const catalogSearchTestTenantID = "00000000-0000-7000-8000-000000000001"

func (s *catalogSearchFailureStore) SearchIndexedCatalog(ctx context.Context, tenantID string, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
	if s.indexedErr != nil {
		return catalog.FederatedCatalogSearchPage{}, s.indexedErr
	}
	return s.MemoryStore.SearchIndexedCatalog(ctx, tenantID, request)
}

func (s *catalogSearchFailureStore) ListCatalogLiveSources(ctx context.Context, tenantID, policyDigest string, limit int) ([]Source, bool, error) {
	if s.liveErr != nil {
		return nil, false, s.liveErr
	}
	return s.MemoryStore.ListCatalogLiveSources(ctx, tenantID, policyDigest, limit)
}

func TestMemoryIndexedCatalogSearchGroupsBoundsAndReappliesCurrentScope(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	const tenantID = catalogSearchTestTenantID
	identifier := "urn:air:remote.example:default:alpha"
	selectedDigest := "sha256:" + strings.Repeat("a", 64)
	for index := 0; index < 18; index++ {
		sourceID := fmt.Sprintf("00000000-0000-7000-8000-%012d", index+10)
		store.sources[sourceID] = Source{ID: sourceID, TenantID: tenantID, Status: SourceActive, RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, Revision: 1}
		store.observations[fmt.Sprintf("selected-%d", index)] = indexedSearchObservation(sourceID, identifier, "Alpha agent", selectedDigest)
	}
	firstSourceID := "00000000-0000-7000-8000-000000000010"
	conflict := indexedSearchObservation(firstSourceID, identifier, "Zulu agent", "sha256:"+strings.Repeat("b", 64))
	store.observations["conflict"] = conflict
	second := indexedSearchObservation(firstSourceID, "urn:air:remote.example:default:beta", "Beta agent", "sha256:"+strings.Repeat("c", 64))
	store.observations["second"] = second

	query := catalog.SearchQuery{Text: "agent", Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{"remote"}, Publisher: "remote.example", Visibility: catalog.VisibilityTenant}
	first, err := store.SearchIndexedCatalog(context.Background(), tenantID, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 1})
	if err != nil || first.IndexedTotal != 2 || len(first.Indexed) != 1 || !first.HasMore || first.Generation == "" {
		t.Fatalf("first indexed page = %#v, %v", first, err)
	}
	item := first.Indexed[0]
	exact := catalog.FederatedCatalogSearchRequest{Limit: 1, ExactIdentity: &catalog.IndexedIdentity{Identifier: second.Identifier, Version: "1", VersionPresent: true}}
	if found, err := store.SearchIndexedCatalog(t.Context(), tenantID, exact); err != nil || found.IndexedTotal != 1 || len(found.Indexed) != 1 || found.Indexed[0].Identifier != second.Identifier {
		t.Fatalf("exact indexed lookup: %#v %v", found, err)
	}
	exact.ExactIdentity.VersionPresent = false
	exact.ExactIdentity.Version = ""
	if found, err := store.SearchIndexedCatalog(t.Context(), tenantID, exact); err != nil || len(found.Indexed) != 0 {
		t.Fatalf("absent version matched a present version: %#v %v", found, err)
	}
	if item.Identifier != identifier || item.ID != catalog.IndexedSearchResultID(identifier, "1", true) || item.Indexed == nil || item.Indexed.NormalizedEntryDigest != selectedDigest || item.Indexed.SourceCount != 18 || len(item.Indexed.SourceIDs) != MaxCatalogSearchAttributions || !item.Indexed.SourcesTruncated || !item.Indexed.Conflict || !item.Indexed.SameSourceConflict {
		t.Fatalf("grouped indexed result = %#v", item)
	}
	narrow, err := store.SearchIndexedCatalog(context.Background(), tenantID, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "Alpha"}, Limit: 10})
	if err != nil || narrow.IndexedTotal != 1 || len(narrow.Indexed) != 1 || narrow.Indexed[0].Indexed == nil || !narrow.Indexed[0].Indexed.Conflict || !narrow.Indexed[0].Indexed.SameSourceConflict {
		t.Fatalf("query-independent conflict evidence = %#v, %v", narrow, err)
	}
	after := catalog.CatalogSearchResultPosition(item)
	secondPage, err := store.SearchIndexedCatalog(context.Background(), tenantID, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 1, After: &after, ExpectedGeneration: first.Generation})
	if err != nil || secondPage.IndexedTotal != 2 || len(secondPage.Indexed) != 1 || secondPage.HasMore || secondPage.Indexed[0].Identifier != second.Identifier {
		t.Fatalf("second indexed page = %#v, %v", secondPage, err)
	}
	if otherTenant, err := store.SearchIndexedCatalog(context.Background(), "00000000-0000-7000-8000-000000000002", catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10}); err != nil || otherTenant.IndexedTotal != 0 || len(otherTenant.Indexed) != 0 {
		t.Fatalf("cross-tenant indexed page = %#v, %v", otherTenant, err)
	}
	publicQuery := query
	publicQuery.Visibility = catalog.VisibilityPublic
	if public, err := store.SearchIndexedCatalog(context.Background(), tenantID, catalog.FederatedCatalogSearchRequest{Query: publicQuery, Limit: 10}); err != nil || public.IndexedTotal != 0 {
		t.Fatalf("public visibility indexed page = %#v, %v", public, err)
	}

	changed := store.sources[firstSourceID]
	changed.Revision++
	store.sources[firstSourceID] = changed
	if _, err := store.SearchIndexedCatalog(context.Background(), tenantID, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10, ExpectedGeneration: first.Generation}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("stale indexed generation returned %v", err)
	}
	for sourceID, source := range store.sources {
		source.Scope = Scope{Tags: []string{"excluded"}}
		store.sources[sourceID] = source
	}
	if excluded, err := store.SearchIndexedCatalog(context.Background(), tenantID, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10}); err != nil || excluded.IndexedTotal != 0 || len(excluded.Indexed) != 0 {
		t.Fatalf("narrowed current scope page = %#v, %v", excluded, err)
	}
}

func indexedSearchObservation(sourceID, identifier, displayName, digest string) Observation {
	return Observation{
		TenantID: catalogSearchTestTenantID, SourceID: sourceID, Identifier: identifier, Version: "1", VersionPresent: true,
		Kind: catalog.KindAgent, MediaType: "application/a2a-agent-card+json", Completeness: CompleteEntry,
		Compatibility: Compatibility{Structural: "valid", Support: "supported"}, NormalizedEntryDigest: digest,
		SearchDisplayName: displayName, SearchDescription: "Remote searchable agent", SearchTags: []string{"remote"}, SearchPublisher: "remote.example", Freshness: FreshCurrent,
	}
}

func TestFederatedCatalogSearchUsesDerivedLiveTopologyAndRejectsOversizedProjection(t *testing.T) {
	t.Parallel()

	const (
		tenantID   = "00000000-0000-7000-8000-000000000001"
		sourceID   = "00000000-0000-7000-8000-000000000010"
		registry   = "https://registry.remote.example/ard/v1/search"
		identifier = "urn:air:remote.example:default:agent"
	)
	valid := fmt.Sprintf(`{"identifier":%q,"version":"1","displayName":"Remote agent","description":"A bounded remote agent.","tags":["remote"],"type":"application/a2a-agent-card+json","url":"https://agents.remote.example/card.json","score":1,"source":"https://registry.remote.example/"}`, identifier)
	oversized := fmt.Sprintf(`{"identifier":"urn:air:remote.example:default:oversized","version":"1","displayName":%q,"tags":["remote"],"type":"application/a2a-agent-card+json","url":"https://agents.remote.example/oversized.json","score":100,"source":"https://registry.remote.example/"}`, strings.Repeat("x", maxCatalogSearchDisplayNameBytes+1))
	misalignedTrust := strings.TrimSuffix(strings.Replace(valid, identifier, identifier+"-unrelated", 1), "}") + `,"trustManifest":{"identity":"https://unrelated.example"}}`
	outbound := &routingFetcher{results: map[string]fetcher.Result{
		http.MethodPost + " " + registry: {Status: http.StatusOK, FinalURL: registry, MediaType: "application/json", Body: []byte(`{"results":[` + valid + `,` + oversized + `,` + misalignedTrust + `]}`)},
	}}
	store := NewMemoryStore()
	store.sources[sourceID] = Source{
		ID: sourceID, TenantID: tenantID, DisplayName: "Remote", Type: SourceRegistry, Endpoint: registry, RegistrySearchURL: registry,
		Status: SourceActive, RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}},
		Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, PolicyID: "test-policy", PolicyRevision: 1,
		PolicyDigest: federationTestPolicyDigest, Revision: 1,
	}
	manager, err := NewManager(store, outbound, clock.NewFake(time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	page, err := manager.SearchFederatedCatalog(ctx, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "remote agent", Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{"remote"}, Publisher: "remote.example"}, Limit: 10, IncludeLive: true})
	if err != nil || len(page.Live) != 1 || page.Live[0].Identifier != identifier || page.Live[0].Score != catalog.CoreLexicalScore(catalog.SearchDocument{Identifier: identifier, DisplayName: "Remote agent", Description: "A bounded remote agent.", Tags: []string{"remote"}}, "remote agent") || !page.Partial {
		t.Fatalf("federated live page = %#v, %v", page, err)
	}
	if len(page.Diagnostics) != 1 || page.Diagnostics[0].SourceID != sourceID || page.Diagnostics[0].Status != "invalid_response" {
		t.Fatalf("federated live diagnostics = %#v", page.Diagnostics)
	}
	if len(outbound.requests) != 1 || outbound.requests[0].URL != registry || outbound.requests[0].MaxResponseBytes != MaxLiveResponseBytes {
		t.Fatalf("federated live requests = %#v", outbound.requests)
	}
	withoutLive, err := manager.SearchFederatedCatalog(ctx, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "remote agent"}, Limit: 10})
	if err != nil || len(withoutLive.Live) != 0 || len(outbound.requests) != 1 {
		t.Fatalf("durable-only continuation = %#v requests %d, %v", withoutLive, len(outbound.requests), err)
	}
}

func TestFederatedCatalogSearchRanksAndDeduplicatesLiveResultsLocally(t *testing.T) {
	t.Parallel()

	const (
		tenantID   = catalogSearchTestTenantID
		identifier = "urn:air:remote.example:default:finance-assistant"
	)
	type liveSource struct {
		id       string
		endpoint string
		entry    string
	}
	// Equivalent integer encodings must survive admission before local ranking.
	sources := []liveSource{
		{
			id:       "00000000-0000-7000-8000-000000000010",
			endpoint: "https://registry-low.remote.example/search",
			entry: fmt.Sprintf(`{"identifier":%q,"version":"1","displayName":"Remote result","description":"Finance assistant","type":%q,"score":9e1,"source":"https://registry-low.remote.example/"}`, identifier, adapter.MediaA2AAgentCard) + `,` +
				fmt.Sprintf(`{"identifier":"urn:air:remote.example:default:other","version":"1","displayName":"Remote result","description":"Finance assistant","type":%q,"score":100.0,"source":"https://registry-low.remote.example/"}`, adapter.MediaA2AAgentCard),
		},
		{
			id:       "00000000-0000-7000-8000-000000000011",
			endpoint: "https://registry-best.remote.example/search",
			entry:    fmt.Sprintf(`{"identifier":%q,"version":"1","displayName":"Finance Assistant","type":%q,"score":1,"source":"https://registry-best.remote.example/"}`, identifier, adapter.MediaA2AAgentCard),
		},
		{
			id:       "00000000-0000-7000-8000-000000000012",
			endpoint: "https://registry-invalid.remote.example/search",
			entry:    fmt.Sprintf(`{"identifier":%q,"version":"1","displayName":%q,"description":"Finance assistant","type":%q,"score":100,"source":"https://registry-invalid.remote.example/"}`, identifier, strings.Repeat("x", maxCatalogSearchDisplayNameBytes+1), adapter.MediaA2AAgentCard),
		},
	}
	store := NewMemoryStore()
	fetches := &routingFetcher{results: make(map[string]fetcher.Result, len(sources))}
	for _, value := range sources {
		store.sources[value.id] = Source{
			ID: value.id, TenantID: tenantID, Status: SourceActive, RetrievalMode: RetrievalLive,
			RegistrySearchURL: value.endpoint, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}},
			Live: LivePolicy{Enabled: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1,
		}
		fetches.results[http.MethodPost+" "+value.endpoint] = fetcher.Result{Status: http.StatusOK, Body: []byte(`{"results":[` + value.entry + `]}`)}
	}
	manager, err := NewManager(store, fetches, clock.NewFake(time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	page, err := manager.SearchFederatedCatalog(ctx, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "finance assistant"}, Limit: 1, IncludeLive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Live) != 1 || page.Live[0].Live == nil || page.Live[0].Live.SourceID != sources[1].id || page.Live[0].Score != 65 || page.Live[0].Live.UpstreamScore != 1 {
		t.Fatalf("locally selected live result = %#v", page.Live)
	}
	if !page.Partial || !slices.ContainsFunc(page.Diagnostics, func(value catalog.CatalogSearchDiagnostic) bool {
		return value.SourceID == sources[2].id && value.Status == "invalid_response"
	}) {
		t.Fatalf("live result diagnostics = %#v", page.Diagnostics)
	}
}

func TestCatalogSearchProjectionBounds(t *testing.T) {
	t.Parallel()

	if !validCatalogSearchProjection("Agent", "Description", []string{"tag"}, []string{"find agent"}, "example.com") {
		t.Fatal("valid Catalog projection was rejected")
	}
	for name, valid := range map[string]bool{
		"display":        validCatalogSearchProjection(strings.Repeat("x", maxCatalogSearchDisplayNameBytes+1), "", nil, nil, "example.com"),
		"description":    validCatalogSearchProjection("Agent", strings.Repeat("x", maxCatalogSearchDescriptionBytes+1), nil, nil, "example.com"),
		"tag count":      validCatalogSearchProjection("Agent", "", make([]string, maxCatalogSearchTags+1), nil, "example.com"),
		"tag characters": validCatalogSearchProjection("Agent", "", []string{strings.Repeat("x", maxCatalogSearchTagCharacters+1)}, nil, "example.com"),
		"query count":    validCatalogSearchProjection("Agent", "", nil, make([]string, maxCatalogSearchQueries+1), "example.com"),
		"query bytes":    validCatalogSearchProjection("Agent", "", nil, []string{strings.Repeat("x", maxCatalogSearchQueryBytes+1)}, "example.com"),
		"publisher":      validCatalogSearchProjection("Agent", "", nil, nil, strings.Repeat("x", maxCatalogSearchPublisherBytes+1)),
		"control":        validCatalogSearchProjection("Agent\x00", "", nil, nil, "example.com"),
	} {
		if valid {
			t.Errorf("%s projection exceeded its bound", name)
		}
	}
}

func TestCatalogLiveResultContractCoversSupportedKindsAndRejectsUnsafeProjection(t *testing.T) {
	t.Parallel()

	const identifier = "urn:air:remote.example:default:search-result"
	for _, testCase := range []struct {
		name      string
		mediaType string
		kind      catalog.Kind
	}{
		{name: "agent", mediaType: adapter.MediaA2AAgentCard, kind: catalog.KindAgent},
		{name: "MCP server", mediaType: adapter.MediaMCPRegistry, kind: catalog.KindMCPServer},
		{name: "Skill", mediaType: adapter.MediaSkillJSON, kind: catalog.KindSkill},
		{name: "case-colliding extensions", mediaType: adapter.MediaA2AAgentCard, kind: catalog.KindAgent},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			entry := []byte(fmt.Sprintf(`{"identifier":%q,"version":"1","type":%q,"displayName":"","description":"Remote searchable resource","tags":[" Remote ","remote"]}`, identifier, testCase.mediaType))
			if testCase.name == "case-colliding extensions" {
				entry = append(entry[:len(entry)-1], []byte(`,"Type":"application/mcp-server-card+json","DisplayName":{},"Tags":["wrong"]}`)...)
			}
			item, ok := catalogLiveResult(LiveResult{Identifier: identifier, Version: "1", VersionPresent: true, Score: 91, SourceID: "source-one", Source: "https://registry.remote.example/", Entry: entry}, catalog.SearchQuery{Text: "remote", Kinds: []catalog.Kind{testCase.kind}, Tags: []string{"remote"}, Publisher: "remote.example"})
			if !ok || item.Kind != testCase.kind || item.DisplayName != identifier || len(item.Tags) != 1 || item.Tags[0] != "remote" || item.Live == nil || item.Live.UpstreamScore != 91 {
				t.Fatalf("Catalog live projection = %#v, accepted=%t", item, ok)
			}
		})
	}
	representativeQueryEntry := []byte(fmt.Sprintf(`{"identifier":%q,"version":"1","type":%q,"displayName":"Unrelated resource","description":"No matching description","tags":["other"],"representativeQueries":["  Locate   specialty helper  ","Locate specialty helper"]}`, identifier, adapter.MediaA2AAgentCard))
	representativeQueryResult, ok := catalogLiveResult(LiveResult{Identifier: identifier, Version: "1", VersionPresent: true, SourceID: "source-one", Source: "https://registry.remote.example/", Entry: representativeQueryEntry}, catalog.SearchQuery{Text: "specialty helper"})
	if !ok || representativeQueryResult.Score != 55 {
		t.Fatalf("representative-query live score = %#v, accepted=%t", representativeQueryResult, ok)
	}
	unicodeEntry := []byte(fmt.Sprintf(`{"identifier":%q,"version":"1","type":%q,"displayName":"A\u00a0Z","description":"Units \u00b2"}`, identifier, adapter.MediaA2AAgentCard))
	unicodeResult, ok := catalogLiveResult(LiveResult{Identifier: identifier, Version: "1", VersionPresent: true, Score: 99, SourceID: "source-one", Source: "https://registry.remote.example/", Entry: unicodeEntry}, catalog.SearchQuery{Text: "\u00b2"})
	if !ok || unicodeResult.Score != 50 || unicodeResult.DisplayName != "A Z" || unicodeResult.Live.UpstreamScore != 99 {
		t.Fatalf("Unicode live projection = %#v, accepted=%t", unicodeResult, ok)
	}

	for kind, expectedCount := range map[catalog.Kind]int{
		catalog.KindAgent:           2,
		catalog.KindMCPServer:       3,
		catalog.KindSkill:           5,
		catalog.KindPlugin:          2,
		catalog.Kind("unsupported"): 0,
	} {
		if mediaTypes := catalogSearchMediaTypes(kind); len(mediaTypes) != expectedCount {
			t.Errorf("media types for %q = %#v", kind, mediaTypes)
		}
	}

	valid := func(overrides string) LiveResult {
		entry := []byte(fmt.Sprintf(`{"identifier":%q,"version":"1","type":%q,"displayName":"Remote resource","description":"Searchable","tags":["remote"]%s}`, identifier, adapter.MediaA2AAgentCard, overrides))
		return LiveResult{Identifier: identifier, Version: "1", VersionPresent: true, SourceID: "source-one", Source: "https://registry.remote.example/", Entry: entry}
	}
	invalid := []struct {
		name   string
		result LiveResult
		query  catalog.SearchQuery
	}{
		{name: "malformed", result: LiveResult{Identifier: identifier, Version: "1", Entry: []byte(`{`)}},
		{name: "mismatched evidence", result: func() LiveResult {
			result := valid("")
			result.Identifier = "urn:air:remote.example:default:other"
			return result
		}()},
		{name: "invalid AIR", result: LiveResult{Identifier: "not-an-air", Version: "1", Entry: []byte(`{"identifier":"not-an-air","version":"1","type":"application/a2a-agent-card+json"}`)}},
		{name: "unknown media", result: func() LiveResult {
			result := valid(`,"ignored":true`)
			result.Entry = []byte(fmt.Sprintf(`{"identifier":%q,"version":"1","type":"application/example"}`, identifier))
			return result
		}()},
		{name: "kind filter", result: valid(""), query: catalog.SearchQuery{Kinds: []catalog.Kind{catalog.KindSkill}}},
		{name: "tag filter", result: valid(""), query: catalog.SearchQuery{Tags: []string{"missing"}}},
		{name: "publisher filter", result: valid(""), query: catalog.SearchQuery{Publisher: "different.example"}},
		{name: "projection bound", result: func() LiveResult {
			result := valid("")
			result.Entry = []byte(fmt.Sprintf(`{"identifier":%q,"version":"1","type":%q,"displayName":%q}`, identifier, adapter.MediaA2AAgentCard, strings.Repeat("x", maxCatalogSearchDisplayNameBytes+1)))
			return result
		}()},
		{name: "lexical miss", result: valid(""), query: catalog.SearchQuery{Text: "unmatched"}},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			if item, ok := catalogLiveResult(testCase.result, testCase.query); ok {
				t.Fatalf("unsafe live result was accepted: %#v", item)
			}
		})
	}

	page := catalog.FederatedCatalogSearchPage{Diagnostics: []catalog.CatalogSearchDiagnostic{{SourceID: "source-one", Status: "failed"}}}
	appendCatalogSearchDiagnostic(&page, catalog.CatalogSearchDiagnostic{SourceID: "source-one", Status: "failed"})
	for index := 0; index < MaxLiveDiagnostics+3; index++ {
		appendCatalogSearchDiagnostic(&page, catalog.CatalogSearchDiagnostic{SourceID: fmt.Sprintf("source-%d", index), Status: "failed"})
	}
	if len(page.Diagnostics) != MaxLiveDiagnostics {
		t.Fatalf("bounded Catalog diagnostics = %#v", page.Diagnostics)
	}
}

func TestFederatedCatalogSearchPropagatesFailuresAndStalePolicyDegradation(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000001"
	store := &catalogSearchFailureStore{MemoryStore: NewMemoryStore(), indexedErr: errors.New("indexed unavailable")}
	manager, err := NewManager(store, &routingFetcher{results: map[string]fetcher.Result{}}, clock.NewFake(time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	reader := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	if _, err := manager.SearchFederatedCatalog(context.Background(), catalog.FederatedCatalogSearchRequest{}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("unauthenticated Catalog federation returned %v", err)
	}
	if _, err := manager.SearchFederatedCatalog(reader, catalog.FederatedCatalogSearchRequest{}); !errors.Is(err, store.indexedErr) {
		t.Fatalf("indexed failure returned %v", err)
	}
	if _, err := manager.SearchLiveCatalog(context.Background(), catalog.FederatedCatalogSearchRequest{}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("unauthenticated live-only Catalog search returned %v", err)
	}
	// A completed durable snapshot must not cause another indexed read.
	if page, err := manager.SearchLiveCatalog(reader, catalog.FederatedCatalogSearchRequest{}); err != nil || len(page.Live) != 0 || page.IndexedTotal != 0 {
		t.Fatalf("live-only search repeated failed indexed selection: %+v %v", page, err)
	}

	store.indexedErr = nil
	store.liveErr = errors.New("topology unavailable")
	// A failing topology is a positive control for the same excluded-origin boundary.
	for _, query := range []catalog.SearchQuery{
		{Text: "agent", Origins: []catalog.SearchResultType{catalog.SearchResultLocal}},
		{Text: "agent", Origins: []catalog.SearchResultType{catalog.SearchResultIndexed}},
		{Text: "agent", Origins: []catalog.SearchResultType{catalog.SearchResultLocal, catalog.SearchResultIndexed}},
		{Text: "agent", NamespaceID: tenantID},
	} {
		page, err := manager.SearchLiveCatalog(reader, catalog.FederatedCatalogSearchRequest{Query: query, IncludeLive: true})
		if err != nil || len(page.Live) != 0 || page.Partial || len(page.Diagnostics) != 0 {
			t.Fatalf("excluded origins read unavailable live topology: %+v %v", page, err)
		}
	}

	if _, err := manager.SearchFederatedCatalog(reader, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "agent"}, IncludeLive: true}); !errors.Is(err, store.liveErr) {
		t.Fatalf("live topology failure returned %v", err)
	}

	store.liveErr = nil
	store.sources["source-stale"] = Source{ID: "source-stale", TenantID: tenantID, Status: SourceActive, RetrievalMode: RetrievalLive, RegistrySearchURL: "https://stale.remote.example/search", Live: LivePolicy{Enabled: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("b", 64), Revision: 1}
	page, err := manager.SearchFederatedCatalog(reader, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "agent"}, Limit: -1, IncludeLive: true})
	if err != nil || !page.Partial || len(page.Diagnostics) != 1 || page.Diagnostics[0].Status != "policy_stale" || len(page.Live) != 0 {
		t.Fatalf("stale-policy Catalog search = %#v, %v", page, err)
	}
	if page, err := manager.SearchFederatedCatalog(reader, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "agent", Visibility: catalog.VisibilityPublic}, IncludeLive: true}); err != nil || page.Partial || len(page.Diagnostics) != 0 {
		t.Fatalf("non-tenant live search boundary = %#v, %v", page, err)
	}
	if _, err := manager.LiveSearch(reader, LiveSearchRequest{Text: "agent", CatalogTenant: true, SourceIDs: []string{"source-stale"}}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("Catalog source override returned %v", err)
	}
}

func TestLiveSearchBoundsTopologyDuplicatesOrderingAndPageSize(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000001"
	admin := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	store := NewMemoryStore()
	for index := 1; index <= 2; index++ {
		sourceID := fmt.Sprintf("source-%d", index)
		endpoint := fmt.Sprintf("https://registry-%d.remote.example/search", index)
		store.sources[sourceID] = Source{ID: sourceID, TenantID: tenantID, Status: SourceActive, RetrievalMode: RetrievalLive, RegistrySearchURL: endpoint, Live: LivePolicy{Enabled: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1}
	}
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"POST https://registry-1.remote.example/search": {Status: http.StatusOK, Body: []byte(`{"results":[]}`)},
	}}
	manager, err := NewManager(store, fetches, clock.NewFake(time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)), id.Generator{}, WithExecutionLimits(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LiveSearch(admin, LiveSearchRequest{Text: "agent", Administrative: true, SourceIDs: []string{"source-1", "source-2"}, Reason: "Investigate selected sources"}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("oversized selected topology returned %v", err)
	}
	response, err := manager.LiveSearch(admin, LiveSearchRequest{Text: "agent", Administrative: true, Reason: "Investigate bounded topology"})
	if err != nil || !response.Partial || len(fetches.requests) != 1 {
		t.Fatalf("bounded administrator topology = %#v, requests=%d, %v", response, len(fetches.requests), err)
	}
	paused := store.sources["source-2"]
	paused.Status = SourcePaused
	store.sources[paused.ID] = paused
	if _, err := manager.LiveSearch(admin, LiveSearchRequest{Text: "agent", Administrative: true, SourceIDs: []string{paused.ID}, Reason: "Investigate paused source"}); catalog.ErrorCodeOf(err) != catalog.CodeSourcePolicyDenied {
		t.Fatalf("ineligible selected source returned %v", err)
	}
	invalid := Source{ID: "source-invalid", TenantID: tenantID, Status: SourceActive, RetrievalMode: RetrievalLive, RegistrySearchURL: "not-a-url", Live: LivePolicy{Enabled: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1}
	store.sources = map[string]Source{invalid.ID: invalid}
	response, err = manager.LiveSearch(admin, LiveSearchRequest{Text: "agent", Administrative: true, SourceIDs: []string{invalid.ID}, Reason: "Investigate invalid configured endpoint"})
	if err != nil || !response.Partial || len(response.Diagnostics) != 1 || response.Diagnostics[0].Status != "failed" {
		t.Fatalf("invalid configured endpoint = %#v, %v", response, err)
	}

	const endpoint = "https://registry.remote.example/search"
	result := func(identifier, version string, score int) string {
		return fmt.Sprintf(`{"identifier":%q,"version":%q,"type":%q,"displayName":"Remote agent","score":%d,"source":"https://registry.remote.example/"}`, identifier, version, adapter.MediaA2AAgentCard, score)
	}
	identifierA := "urn:air:remote.example:default:a"
	identifierB := "urn:air:remote.example:default:b"
	fetches = &routingFetcher{results: map[string]fetcher.Result{
		"POST " + endpoint: {Status: http.StatusOK, Body: []byte(`{"results":[` + result(identifierB, "1", 10) + `,` + result(identifierA, "2", 10) + `,` + result(identifierA, "1", 10) + `,` + result(identifierB, "1", 20) + `]}`)},
	}}
	store = NewMemoryStore()
	store.sources["source-one"] = Source{ID: "source-one", TenantID: tenantID, Status: SourceActive, RetrievalMode: RetrievalLive, RegistrySearchURL: endpoint, Live: LivePolicy{Enabled: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1}
	manager, err = NewManager(store, fetches, clock.NewFake(time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	response, err = manager.LiveSearch(admin, LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true, Reason: "Investigate bounded result ordering"})
	if err != nil || len(response.Results) != 2 || response.Results[0].Identifier != identifierB || response.Results[0].Score != 20 || response.Results[1].Identifier != identifierA || response.Results[1].Version != "1" {
		t.Fatalf("deduplicated bounded live order = %#v, %v", response, err)
	}
}

func TestMemoryFederationSearchFiltersFallbackCursorAndLiveSourceBound(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000001"
	store := NewMemoryStore()
	store.sources["indexed"] = Source{ID: "indexed", TenantID: tenantID, Status: SourceActive, RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, Revision: 1}
	identifier := "urn:air:remote.example:default:fallback"
	first := indexedSearchObservation("indexed", identifier, "", "sha256:"+strings.Repeat("a", 64))
	second := first
	second.ID = "second"
	second.NormalizedEntryDigest = "sha256:" + strings.Repeat("b", 64)
	third := indexedSearchObservation("indexed", "urn:air:remote.example:default:beta", "Beta", "sha256:"+strings.Repeat("c", 64))
	store.observations["first"] = first
	store.observations["second"] = second
	store.observations["third"] = third

	page, err := store.SearchIndexedCatalog(context.Background(), tenantID, catalog.FederatedCatalogSearchRequest{})
	if err != nil || page.IndexedTotal != 2 || len(page.Indexed) != 2 || page.Indexed[1].DisplayName != identifier || page.Indexed[1].Indexed == nil || !page.Indexed[1].Indexed.Conflict {
		t.Fatalf("fallback indexed page = %#v, %v", page, err)
	}
	for name, query := range map[string]catalog.SearchQuery{
		"kind":      {Kinds: []catalog.Kind{catalog.KindSkill}},
		"tag":       {Tags: []string{"missing"}},
		"publisher": {Publisher: "different.example"},
		"text":      {Text: "unmatched"},
	} {
		filtered, searchErr := store.SearchIndexedCatalog(context.Background(), tenantID, catalog.FederatedCatalogSearchRequest{Query: query})
		if searchErr != nil || filtered.IndexedTotal != 0 || len(filtered.Indexed) != 0 {
			t.Errorf("%s filter page = %#v, %v", name, filtered, searchErr)
		}
	}
	after := catalog.CatalogSearchResultPosition(page.Indexed[len(page.Indexed)-1])
	exhausted, err := store.SearchIndexedCatalog(context.Background(), tenantID, catalog.FederatedCatalogSearchRequest{After: &after, ExpectedGeneration: page.Generation})
	if err != nil || exhausted.IndexedTotal != 2 || len(exhausted.Indexed) != 0 {
		t.Fatalf("exhausted indexed cursor = %#v, %v", exhausted, err)
	}
	if observations, err := store.ListObservationGroupMembers(context.Background(), tenantID, "urn:air:remote.example:default:missing", "1", true, 1); err != nil || len(observations) != 0 {
		t.Fatalf("missing exact observation group = %#v, %v", observations, err)
	}

	for index := 1; index <= 2; index++ {
		idValue := fmt.Sprintf("live-%d", index)
		store.sources[idValue] = Source{ID: idValue, TenantID: tenantID, Status: SourceActive, RetrievalMode: RetrievalLive, RegistrySearchURL: fmt.Sprintf("https://live-%d.example/search", index), Live: LivePolicy{Enabled: true}, Revision: 1}
	}
	sources, truncated, err := store.ListCatalogLiveSources(context.Background(), tenantID, federationTestPolicyDigest, 1)
	if err != nil || !truncated || len(sources) != 1 || sources[0].ID != "live-1" {
		t.Fatalf("bounded live sources = %#v, truncated=%t, %v", sources, truncated, err)
	}
}

func TestBoundedLiveSelectionPrioritizesCurrentPolicyWithoutHidingStaleEvidence(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const host = "catalog.example.com"
	currentPolicy := federationTestPolicyDigest
	stalePolicy := "sha256:" + strings.Repeat("b", 64)
	store := NewMemoryStore()
	for _, source := range []Source{
		{ID: "source-a-stale", TenantID: tenantID, Status: SourceActive, RetrievalMode: RetrievalHybrid, RegistrySearchURL: "https://stale.example/search", PolicyDigest: stalePolicy, Live: LivePolicy{Enabled: true, AnonymousDisclosureAcknowledged: true}, Referrals: ReferralPolicy{Enabled: true, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: []DiscoveryAssignment{{Host: host, Audience: AudienceAnonymous}}},
		{ID: "source-z-current", TenantID: tenantID, Status: SourceActive, RetrievalMode: RetrievalHybrid, RegistrySearchURL: "https://current.example/search", PolicyDigest: currentPolicy, Live: LivePolicy{Enabled: true, AnonymousDisclosureAcknowledged: true}, Referrals: ReferralPolicy{Enabled: true, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: []DiscoveryAssignment{{Host: host, Audience: AudienceAnonymous}}},
	} {
		store.sources[source.ID] = source
	}

	selections := []struct {
		name string
		list func() ([]Source, bool, error)
	}{
		{name: "Catalog", list: func() ([]Source, bool, error) {
			return store.ListCatalogLiveSources(context.Background(), tenantID, currentPolicy, 1)
		}},
		{name: "discovery", list: func() ([]Source, bool, error) {
			return store.ListDiscoveryLiveSources(context.Background(), tenantID, host, AudienceAnonymous, currentPolicy, 1)
		}},
		{name: "referral", list: func() ([]Source, bool, error) {
			return store.ListDiscoveryReferralSources(context.Background(), tenantID, host, AudienceAnonymous, currentPolicy, 1)
		}},
	}
	for _, selection := range selections {
		t.Run(selection.name, func(t *testing.T) {
			items, truncated, err := selection.list()
			if err != nil || !truncated || len(items) != 1 || items[0].ID != "source-z-current" {
				t.Fatalf("selection = %#v, truncated=%t, %v", items, truncated, err)
			}
		})
	}
	items, truncated, err := store.ListCatalogLiveSources(context.Background(), tenantID, currentPolicy, 2)
	if err != nil || truncated || len(items) != 2 || items[0].ID != "source-z-current" || items[1].ID != "source-a-stale" {
		t.Fatalf("selection with diagnostic room = %#v, truncated=%t, %v", items, truncated, err)
	}
	store.sources["source-indexed-referral"] = Source{ID: "source-indexed-referral", TenantID: tenantID, Status: SourceActive, RetrievalMode: RetrievalIndexed, RegistrySearchURL: "https://indexed.example/search", PolicyDigest: currentPolicy, Referrals: ReferralPolicy{Enabled: true, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: []DiscoveryAssignment{{Host: host, Audience: AudienceAnonymous}}}
	referrals, truncated, err := store.ListDiscoveryReferralSources(context.Background(), tenantID, host, AudienceAnonymous, currentPolicy, 3)
	if err != nil || truncated || !slices.ContainsFunc(referrals, func(source Source) bool { return source.ID == "source-indexed-referral" }) {
		t.Fatalf("indexed referral selection = %#v, truncated=%t, %v", referrals, truncated, err)
	}
}

func TestMemoryFederationEvidenceOrderingAndIsolation(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000001"
	store := NewMemoryStore()
	baseTime := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	occurrences := []ObservationOccurrence{
		{ObservationID: "observation", SnapshotID: "snapshot-new", RetrievalDigest: "digest-a", EntryLocation: "/entries/0", ObservedAt: baseTime.Add(time.Minute)},
		{ObservationID: "observation", SnapshotID: "snapshot-b", RetrievalDigest: "digest-a", EntryLocation: "/entries/0", ObservedAt: baseTime},
		{ObservationID: "observation", SnapshotID: "snapshot-a", RetrievalDigest: "digest-b", EntryLocation: "/entries/0", ObservedAt: baseTime},
		{ObservationID: "observation", SnapshotID: "snapshot-a", RetrievalDigest: "digest-a", EntryLocation: "/entries/1", ObservedAt: baseTime},
		{ObservationID: "observation", SnapshotID: "snapshot-a", RetrievalDigest: "digest-a", EntryLocation: "/entries/0", ObservedAt: baseTime},
	}
	for _, occurrence := range occurrences {
		store.occurrences[occurrenceKey(tenantID, occurrence)] = occurrence
	}
	items, total, err := store.ListObservationOccurrences(context.Background(), tenantID, "observation", 3)
	if err != nil || total != len(occurrences) || len(items) != 3 || items[0].SnapshotID != "snapshot-new" || items[1].SnapshotID != "snapshot-b" || items[2].RetrievalDigest != "digest-a" {
		t.Fatalf("bounded occurrence order = %#v, total=%d, %v", items, total, err)
	}
	items, total, err = store.ListObservationOccurrences(context.Background(), tenantID, "observation", 10)
	orderedKeys := make([]string, 0, len(items))
	for _, item := range items {
		orderedKeys = append(orderedKeys, item.SnapshotID+"|"+item.RetrievalDigest+"|"+item.EntryLocation)
	}
	wantOrder := []string{
		"snapshot-new|digest-a|/entries/0",
		"snapshot-b|digest-a|/entries/0",
		"snapshot-a|digest-a|/entries/0",
		"snapshot-a|digest-a|/entries/1",
		"snapshot-a|digest-b|/entries/0",
	}
	if err != nil || total != len(occurrences) || !slices.Equal(orderedKeys, wantOrder) {
		t.Fatalf("complete occurrence order = %#v, total=%d, %v", orderedKeys, total, err)
	}
	firstPage, err := store.ListObservationOccurrencePage(context.Background(), tenantID, OccurrenceListRequest{ObservationID: "observation", Limit: 2})
	if err != nil || len(firstPage) != 2 || firstPage[0].SnapshotID != "snapshot-new" || firstPage[1].SnapshotID != "snapshot-b" {
		t.Fatalf("first occurrence page = %#v, %v", firstPage, err)
	}
	firstPosition := firstPage[len(firstPage)-1]
	secondPage, err := store.ListObservationOccurrencePage(context.Background(), tenantID, OccurrenceListRequest{
		ObservationID:        "observation",
		Limit:                2,
		AfterObservedAt:      &firstPosition.ObservedAt,
		AfterSnapshotID:      firstPosition.SnapshotID,
		AfterRetrievalDigest: firstPosition.RetrievalDigest,
		AfterEntryLocation:   firstPosition.EntryLocation,
	})
	if err != nil || len(secondPage) != 2 || secondPage[0].SnapshotID != "snapshot-a" || secondPage[0].RetrievalDigest != "digest-a" || secondPage[0].EntryLocation != "/entries/0" || secondPage[1].EntryLocation != "/entries/1" {
		t.Fatalf("second occurrence page = %#v, %v", secondPage, err)
	}
	secondPosition := secondPage[len(secondPage)-1]
	lastPage, err := store.ListObservationOccurrencePage(context.Background(), tenantID, OccurrenceListRequest{
		ObservationID:        "observation",
		Limit:                2,
		AfterObservedAt:      &secondPosition.ObservedAt,
		AfterSnapshotID:      secondPosition.SnapshotID,
		AfterRetrievalDigest: secondPosition.RetrievalDigest,
		AfterEntryLocation:   secondPosition.EntryLocation,
	})
	if err != nil || len(lastPage) != 1 || lastPage[0].RetrievalDigest != "digest-b" {
		t.Fatalf("last occurrence page = %#v, %v", lastPage, err)
	}
	if otherTenant, err := store.ListObservationOccurrencePage(context.Background(), "00000000-0000-7000-8000-000000000002", OccurrenceListRequest{ObservationID: "observation", Limit: 10}); err != nil || len(otherTenant) != 0 {
		t.Fatalf("cross-tenant occurrence page = %#v, %v", otherTenant, err)
	}
	summaries, err := store.ObservationOccurrenceSummaries(context.Background(), tenantID, []string{"observation"})
	latest, count := summaries["observation"].Latest, summaries["observation"].Total
	if err != nil || count != len(occurrences) || latest.SnapshotID != "snapshot-new" {
		t.Fatalf("occurrence summary = %#v, count=%d, %v", latest, count, err)
	}
	if _, err := store.ObservationOccurrenceSummaries(context.Background(), tenantID, []string{"missing"}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing occurrence summary returned %v", err)
	}
	if _, err := store.GetObservationOccurrence(context.Background(), tenantID, "different-observation", "snapshot-a", "digest-a", "/entries/0"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-observation occurrence read returned %v", err)
	}

	observation := func(idValue, identifier, version, sourceID, normalizedDigest, exactDigest string, versionPresent bool) Observation {
		return Observation{ID: idValue, Identifier: identifier, Version: version, VersionPresent: versionPresent, SourceID: sourceID, NormalizedEntryDigest: normalizedDigest, ExactEntryDigest: exactDigest}
	}
	for name, pair := range map[string][]Observation{
		"identifier": {
			observation("same", "urn:air:z.example:default:item", "1", "source", "n", "e", true),
			observation("same", "urn:air:a.example:default:item", "1", "source", "n", "e", true),
		},
		"typed version": {
			observation("same", identifierForOrderTest, "", "source", "n", "e", false),
			observation("same", identifierForOrderTest, "1", "source", "n", "e", true),
		},
		"version": {
			observation("same", identifierForOrderTest, "2", "source", "n", "e", true),
			observation("same", identifierForOrderTest, "1", "source", "n", "e", true),
		},
		"source": {
			observation("same", identifierForOrderTest, "1", "source-b", "n", "e", true),
			observation("same", identifierForOrderTest, "1", "source-a", "n", "e", true),
		},
		"normalized digest": {
			observation("same", identifierForOrderTest, "1", "source", "n-b", "e", true),
			observation("same", identifierForOrderTest, "1", "source", "n-a", "e", true),
		},
		"exact digest": {
			observation("same", identifierForOrderTest, "1", "source", "n", "e-b", true),
			observation("same", identifierForOrderTest, "1", "source", "n", "e-a", true),
		},
		"identifier tie": {
			observation("id-b", identifierForOrderTest, "1", "source", "n", "e", true),
			observation("id-a", identifierForOrderTest, "1", "source", "n", "e", true),
		},
	} {
		t.Run(name, func(t *testing.T) {
			wantFirst := pair[1]
			sortObservations(pair)
			if !reflect.DeepEqual(pair[0], wantFirst) {
				t.Fatalf("observation order = %#v", pair)
			}
		})
	}
}

const identifierForOrderTest = "urn:air:remote.example:default:ordered"

// Every Skill format recognized in a live answer must also be eligible for the
// outbound kind filter. Otherwise a correct peer excludes Markdown Skills.
func TestCatalogLiveSkillFilterIncludesRecognizedMarkdownTypes(t *testing.T) {
	for _, media := range []string{adapter.MediaSkillJSON, adapter.MediaSkillZIP, adapter.MediaSkillGZIP, adapter.MediaSkillMarkdown, adapter.MediaSkillMarkdownARD} {
		if kindForMediaType(media) != catalog.KindSkill || !slices.Contains(catalogSearchMediaTypes(catalog.KindSkill), media) {
			t.Errorf("recognized Skill is missing from the live type filter: %s", media)
		}
	}
}
