package federation

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestObservationControlFilterDeclarations(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/control-filters.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name       string          `json:"name"`
		Entry      json.RawMessage `json:"entry"`
		License    *string         `json:"license"`
		Categories []string        `json:"categories"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			before := string(c.Entry)
			got := ProjectObservationControlFilters(c.Entry)
			if !reflect.DeepEqual(got, ObservationControlFilters{License: c.License, Categories: c.Categories}) || string(c.Entry) != before {
				t.Fatalf("projection = %+v; want license=%v categories=%v, with unchanged bytes", got, c.License, c.Categories)
			}
			if !got.matches(catalog.SearchQuery{}) {
				t.Fatal("optional evidence excluded an unfiltered observation")
			}
			if got.matches(catalog.SearchQuery{License: "MIT"}) != (c.License != nil && *c.License == "MIT") || got.matches(catalog.SearchQuery{Categories: []string{"tools"}}) != containsAllStrings(c.Categories, []string{"tools"}) {
				t.Fatal("matching coerced unavailable evidence or changed declaration semantics")
			}
			if c.License != nil && !got.matches(catalog.SearchQuery{License: *c.License}) {
				t.Fatal("matching changed an exact license declaration")
			}
			if len(c.Categories) > 0 && !got.matches(catalog.SearchQuery{Categories: c.Categories}) {
				t.Fatal("matching rejected independently declared categories")
			}
			// The same independently specified declarations must survive live
			// projection, where extensions are top-level ARD properties.
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(c.Entry, &wire); err != nil {
				t.Fatal(err)
			}
			var extensions map[string]json.RawMessage
			_ = json.Unmarshal(wire["unknownProperties"], &extensions)
			delete(wire, "unknownProperties")
			for name, value := range extensions {
				wire[name] = value
			}
			liveRaw, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			live := projectLiveControlFilters(t.Context(), liveRaw, true)
			if !reflect.DeepEqual(live, got) {
				t.Fatalf("live projection = %+v; indexed = %+v", live, got)
			}
		})
	}
	for _, bad := range []string{"{", "[]", "null", `{"metadata":{"license":"x\u0000"}}`, `{"unknownProperties":{"http://purl.org/dc/terms/subject":["x\u0000"]}}`} {
		if got := ProjectObservationControlFilters([]byte(bad)); got.License != nil || got.Categories != nil {
			t.Fatalf("unusable input produced declarations: %+v", got)
		}
	}
}

func TestMemoryIndexedControlFiltersPrecedeGroupingAndPaging(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	const sourceID = "00000000-0000-7000-8000-000000000010"
	store.sources[sourceID] = Source{ID: sourceID, TenantID: catalogSearchTestTenantID, Status: SourceActive, RetrievalMode: RetrievalIndexed, Revision: 1}
	for i, name := range []string{"a", "b", "c", "d"} {
		observation := indexedSearchObservation(sourceID, "urn:air:remote.example:default:"+name, "Agent "+name, "sha256:"+strings.Repeat(name, 64))
		observation.controlFilters = ProjectObservationControlFilters([]byte(`{"metadata":{"license":"MIT"},"unknownProperties":{"http://purl.org/dc/terms/subject":[" TOOLS ","CAFÉ"]}}`))
		if i == 0 { // The first sorted match is ineligible, before ranking/limit.
			observation.controlFilters = ObservationControlFilters{}
		}
		store.observations[name] = observation
	}
	conflict := store.observations["b"]
	conflict.NormalizedEntryDigest = "sha256:" + strings.Repeat("e", 64)
	conflict.controlFilters = ObservationControlFilters{}
	store.observations["conflict"] = conflict
	query := catalog.SearchQuery{Text: "agent", License: "MIT", Categories: []string{"café", "tools"}, Origins: []catalog.SearchResultType{catalog.SearchResultIndexed}}
	var after *catalog.CatalogSearchPosition
	generation := ""
	names := make([]string, 0, 3)
	for range 3 {
		page, err := store.SearchIndexedCatalog(t.Context(), catalogSearchTestTenantID, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 1, After: after, ExpectedGeneration: generation})
		if err != nil || page.IndexedTotal != 3 || len(page.Indexed) != 1 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		item := page.Indexed[0]
		if strings.HasSuffix(item.Identifier, ":b") && (!item.Indexed.Conflict || !item.Indexed.SameSourceConflict) {
			t.Fatal("a filter hid query-independent conflict evidence")
		}
		names = append(names, item.Identifier)
		position := catalog.CatalogSearchResultPosition(item)
		after, generation = &position, page.Generation
		if page.HasMore != (len(names) < 3) {
			t.Fatal("incorrect continuation")
		}
	}
	if strings.Join(names, ",") != "urn:air:remote.example:default:b,urn:air:remote.example:default:c,urn:air:remote.example:default:d" {
		t.Fatalf("traversal=%v", names)
	}
	query.License = "mit"
	page, err := store.SearchIndexedCatalog(t.Context(), catalogSearchTestTenantID, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10})
	if err != nil || page.IndexedTotal != 0 {
		t.Fatalf("case-sensitive license=%+v %v", page, err)
	}
	query.License = "MIT"
	source := store.sources[sourceID]
	source.Scope.ExcludedIdentifiers = []string{"urn:air:remote.example:default:c"}
	source.Revision++
	store.sources[sourceID] = source
	page, err = store.SearchIndexedCatalog(t.Context(), catalogSearchTestTenantID, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10})
	if err != nil || page.IndexedTotal != 2 || len(page.Indexed) != 2 || page.HasMore || page.Indexed[0].Identifier != names[0] || page.Indexed[1].Identifier != names[2] {
		t.Fatalf("current source exclusion bypassed by declaration filters: %+v %v", page, err)
	}
}
