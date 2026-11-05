package catalog

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestCatalogColumnOrderAndCursorPositions(t *testing.T) {
	items := []CatalogSearchResult{
		{ID: "c", DisplayName: "Bravo", Kind: KindSkill, Publisher: "Alpha", Score: 100},
		{ID: "b", DisplayName: " alpha ", Kind: KindAgent, Publisher: "Zulu", Score: 1},
		{ID: "a", DisplayName: "ALPHA", Kind: KindAgent, Publisher: "Zulu", Score: 80},
		{ID: "d", DisplayName: "Aardvark", Kind: KindSkill, Publisher: "Zulu", Score: 5},
	}
	for _, tc := range []struct {
		order SearchOrder
		want  []string
	}{
		{"", []string{"c", "a", "d", "b"}},
		{SearchOrderNameAsc, []string{"d", "a", "b", "c"}},
		{SearchOrderNameDesc, []string{"c", "a", "b", "d"}},
		{SearchOrderKindAsc, []string{"a", "b", "d", "c"}},
		{SearchOrderKindDesc, []string{"d", "c", "a", "b"}},
		{SearchOrderPublisherAsc, []string{"c", "d", "a", "b"}},
		{SearchOrderPublisherDesc, []string{"d", "a", "b", "c"}},
	} {
		t.Run(string(tc.order), func(t *testing.T) {
			got := append([]CatalogSearchResult(nil), items...)
			sortCatalogResults(got, tc.order)
			ids := make([]string, 0, len(got))
			for i, item := range got {
				ids = append(ids, item.ID)
				if i > 0 && !CatalogSearchPositionAfter(CatalogSearchResultPosition(item, tc.order), CatalogSearchResultPosition(got[i-1], tc.order), tc.order) {
					t.Fatal("cursor does not follow sorted predecessor")
				}
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Fatal(ids, tc.want)
			}
			if CatalogSearchPositionAfter(CatalogSearchResultPosition(got[0], tc.order), CatalogSearchResultPosition(got[0], tc.order), tc.order) {
				t.Fatal("position repeats itself")
			}
		})
	}
	local := CatalogSearchResult{Publisher: "uuid", Local: &CatalogSearchLocal{Display: &CatalogPresentation{PublisherName: "  ZULU  Tools "}}}
	if p := CatalogSearchResultPosition(local, SearchOrderPublisherAsc); p.SortValue != "zulu tools" {
		t.Fatal("publisher order used hidden identity", p)
	}
	if err := ValidateSavedSearch(SearchQuery{Order: "unsupported"}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
}

func TestCatalogColumnOrderBindsContinuationAndSkipsInference(t *testing.T) {
	s, f, actor := newSemanticSearchTest(t)
	q := SearchQuery{Text: "find tools", Order: SearchOrderPublisherAsc, Limit: 1}
	if _, used, _, err := s.semanticPage(t.Context(), actor, q, false); err != nil || used || f.queries != 0 || f.searches != 0 {
		t.Fatal("column ordering invoked semantic search", used, err)
	}
	binding, err := s.searchCursorBinding(t.Context(), actor, q)
	if err != nil {
		t.Fatal(err)
	}
	pos := CatalogSearchPosition{DisplayName: "tools", ResultID: "local:1", SortValue: "publisher name"}
	token, err := s.encodeSearchCursor("generation", pos, binding)
	if err != nil {
		t.Fatal(err)
	}
	decoded, ok, err := s.decodeSearchCursor(token, binding)
	if err != nil || !ok || decoded.SortValue != pos.SortValue {
		t.Fatal(decoded, err)
	}
	q.Order = SearchOrderPublisherDesc
	changed, err := s.searchCursorBinding(t.Context(), actor, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.decodeSearchCursor(token, changed); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("changed order reused cursor", err)
	}
	// Current authorization also remains part of the binding.
	changedActor := identity.Actor{TenantID: actor.TenantID, Subject: "different"}
	changed, err = s.searchCursorBinding(t.Context(), changedActor, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.decodeSearchCursor(token, changed); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("changed actor reused cursor", err)
	}
	live := LiveCursor{Surface: "control", Generation: "1", IndexedGeneration: "1", Position: &pos, ExpiresAt: s.clock.Now().Add(time.Minute).Unix()}
	token, err = EncodeLiveCursor(live, s.cursorKey, binding)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeLiveCursor(token, "control", s.cursorKey, binding, s.clock.Now()); err != nil || got.Position.SortValue != pos.SortValue {
		t.Fatal(got, err)
	}
}

func TestColumnOrderContinuesDurableResultsBeforeSortedLiveWindow(t *testing.T) {
	locals := []CatalogSummary{unifiedLocalRecord("a", "urn:air:local.example:agent:a", "Alpha", 10), unifiedLocalRecord("z", "urn:air:local.example:agent:z", "Zulu", 90)}
	indexed := CatalogSearchResult{ID: "indexed:b", Type: SearchResultIndexed, Identifier: "urn:air:remote.example:agent:b", DisplayName: "Bravo", Score: 100}
	live := []CatalogSearchResult{{ID: "live:a", Type: SearchResultLive, Identifier: "urn:air:remote.example:agent:live-a", DisplayName: "A live", Score: 100}, {ID: "live:z", Type: SearchResultLive, Identifier: "urn:air:remote.example:agent:live-z", DisplayName: "Z live", Score: 1}}
	for _, tc := range []struct {
		order SearchOrder
		want  []string
	}{{SearchOrderNameAsc, []string{"local:a", "indexed:b", "local:z", "live:a", "live:z"}}, {SearchOrderNameDesc, []string{"local:z", "indexed:b", "local:a", "live:z", "live:a"}}} {
		t.Run(string(tc.order), func(t *testing.T) {
			repo := &unifiedSearchRepository{search: func(_ context.Context, _ string, r CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
				items := append([]CatalogSummary(nil), locals...)
				sort.Slice(items, func(i, j int) bool {
					return CatalogSearchPositionAfter(catalogRecordPosition(items[j], r.Query.Order), catalogRecordPosition(items[i], r.Query.Order), r.Query.Order)
				})
				if r.After != nil {
					items = recordsAfterPosition(items, *r.After, r.Query.Order)
				}
				return CatalogSearchRepositoryPage{Items: items, Total: 2, Generation: "42"}, nil
			}}
			external := unifiedFederatedSearcher{search: func(_ context.Context, r FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
				items := []CatalogSearchResult{indexed}
				if r.After != nil && !CatalogSearchPositionAfter(CatalogSearchResultPosition(indexed, r.Query.Order), *r.After, r.Query.Order) {
					items = nil
				}
				page := FederatedCatalogSearchPage{Indexed: items, IndexedTotal: 1, Generation: "42"}
				if r.IncludeLive {
					page.Live = append([]CatalogSearchResult(nil), live...)
					page.ReplayFingerprint = strings.Repeat("a", 64)
				}
				return page, nil
			}}
			service := newUnifiedSearchTestService(t, repo, external)
			q := SearchQuery{Limit: 1, Order: tc.order}
			got := []string{}
			for range 6 {
				page, err := service.Search(unifiedReaderContext(), q)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range page.Items {
					got = append(got, item.ID)
				}
				if page.NextCursor == "" {
					break
				}
				q.Cursor = page.NextCursor
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatal(got, tc.want)
			}
		})
	}
}

func TestCatalogCursorSortValueByteBoundary(t *testing.T) {
	s, _, _ := newSemanticSearchTest(t)
	for _, size := range []int{2048, 2049} {
		position := CatalogSearchPosition{DisplayName: "name", ResultID: "local:1", SortValue: strings.Repeat("x", size)}
		token, err := s.encodeSearchCursor("generation", position, "binding")
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.decodeSearchCursor(token, "binding")
		if (err == nil) != (size == 2048) {
			t.Fatal("sort value byte bound", size, err)
		}
		state := LiveCursor{Surface: "control", Generation: "1", IndexedGeneration: "1", Position: &position, ExpiresAt: s.clock.Now().Add(time.Minute).Unix()}
		token, err = EncodeLiveCursor(state, s.cursorKey, "binding")
		if err != nil {
			t.Fatal(err)
		}
		_, err = DecodeLiveCursor(token, "control", s.cursorKey, "binding", s.clock.Now())
		if (err == nil) != (size == 2048) {
			t.Fatal("live sort value byte bound", size, err)
		}
	}
}

func TestCatalogSearchOrderWireValidation(t *testing.T) {
	for _, value := range []string{`"name_asc"`, `"name_desc"`, `"kind_asc"`, `"kind_desc"`, `"publisher_asc"`, `"publisher_desc"`} {
		var query SearchQuery
		if err := jsondoc.Decode([]byte(`{"order":`+value+`}`), &query); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []string{`""`, `"NAME_ASC"`, `"rating_desc"`, `null`, `8`, `[]`, `{}`} {
		var query SearchQuery
		if err := jsondoc.Decode([]byte(`{"order":`+value+`}`), &query); err == nil {
			t.Fatal("invalid order accepted", value)
		}
	}
	var query SearchQuery
	if err := jsondoc.Decode([]byte(`{}`), &query); err != nil || query.Order != "" {
		t.Fatal("omitted default", query, err)
	}
}
