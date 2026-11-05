// Hybrid examples validate against the same source contract served by the API.
package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestListingDeclarationFilterSchemas(t *testing.T) {
	categoryTerms := func(count int) []any {
		terms := make([]any, count)
		for index := range terms {
			terms[index] = string(rune('a' + index))
		}
		return terms
	}
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CatalogSearch", "CatalogExploreQuery"} {
		t.Run(name, func(t *testing.T) {
			schema := document.Components.Schemas[name].Value
			for _, body := range []any{
				map[string]any{"license": ""},
				map[string]any{"license": " MIT ", "categories": []any{" TOOLS "}},
				map[string]any{"license": strings.Repeat("🙂", 256), "categories": []any{strings.Repeat("🙂", 80)}},
				map[string]any{"categories": categoryTerms(16)},
			} {
				if err := schema.VisitJSON(body, openapi3.EnableJSONSchema2020()); err != nil {
					t.Fatalf("valid Listing filters rejected: %v", err)
				}
			}
			for _, body := range []any{
				map[string]any{"license": nil}, map[string]any{"categories": nil},
				map[string]any{"license": " \t "}, map[string]any{"license": "x\x00"},
				map[string]any{"license": strings.Repeat("🙂", 257)},
				map[string]any{"categories": []any{""}}, map[string]any{"categories": []any{" "}},
				map[string]any{"categories": []any{"x\x00"}},
				map[string]any{"categories": []any{strings.Repeat("🙂", 81)}},
				map[string]any{"categories": []any{strings.Repeat("🙂", 80) + " "}},
				map[string]any{"categories": categoryTerms(17)},
			} {
				if err := schema.VisitJSON(body, openapi3.EnableJSONSchema2020()); err == nil {
					t.Fatalf("schema accepts invalid Listing filters: %+v", body)
				}
			}
		})
	}
	schema := document.Components.Schemas["CatalogSearch"].Value
	for _, field := range []string{"license", "categories"} {
		body := map[string]any{"license": "MIT", "origins": []any{"local"}}
		if field == "categories" {
			body = map[string]any{"categories": []any{"tools"}, "origins": []any{"local"}}
		}
		if err := schema.VisitJSON(body, openapi3.EnableJSONSchema2020()); err != nil {
			t.Fatal(err)
		}
		for _, origins := range []any{[]any{"indexed"}, []any{"local", "live"}} {
			body["origins"] = origins
			if err := schema.VisitJSON(body, openapi3.EnableJSONSchema2020()); err != nil {
				t.Fatalf("schema rejects implemented external declaration filters: %v", err)
			}
		}
	}
}

func TestSearchTextSchemasRejectNUL(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"text", "publisher", "tags"} {
		t.Run(field, func(t *testing.T) {
			schema := document.Components.Schemas["CatalogSearch"].Value.Properties[field].Value
			if field == "tags" {
				schema = schema.Items.Value
			}
			for _, value := range []string{"ordinary", "", "Unicode 🙂", "literal \\u0000"} {
				if err := schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err != nil {
					t.Fatalf("valid %s rejected: %v", field, err)
				}
			}
			for _, value := range []string{"\x00", "before\x00after", "after\x00\n"} {
				if err := schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err == nil {
					t.Fatalf("schema accepts NUL in %s: %q", field, value)
				}
			}
		})
	}
}

func TestHybridSearchExamples(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Files.ReadFile("examples/hybrid-search.json")
	if err != nil {
		t.Fatal(err)
	}
	var example struct {
		Search           any   `json:"search"`
		FilteredSearches []any `json:"filteredSearches"`
		NamespaceExplore any   `json:"namespaceExplore"`
		Reindex          any   `json:"reindex"`
		Responses        []any `json:"responses"`
		Continuations    []any `json:"continuations"`
	}
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]any{"/api/v1/catalog/search": example.Search, "/api/v1/catalog:reindex": example.Reindex} {
		schema := document.Paths.Value(path).Post.RequestBody.Value.Content.Get("application/json").Schema.Value
		if err := schema.VisitJSON(body, openapi3.EnableJSONSchema2020()); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if len(example.FilteredSearches) != 3 {
		t.Fatal("missing origin/namespace examples")
	}
	for _, body := range example.FilteredSearches {
		if err := document.Components.Schemas["CatalogSearch"].Value.VisitJSON(body, openapi3.EnableJSONSchema2020()); err != nil {
			t.Fatal(err)
		}
	}
	if err := document.Components.Schemas["CatalogExplore"].Value.VisitJSON(example.NamespaceExplore, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatal(err)
	}
	for _, body := range []any{map[string]any{"namespaceId": "00000000-0000-7000-8000-000000000001", "origins": []any{"indexed"}}} {
		if err := document.Components.Schemas["CatalogSearch"].Value.VisitJSON(body, openapi3.EnableJSONSchema2020()); err == nil {
			t.Fatal("schema accepts conflicting selectors")
		}
	}
	for _, body := range example.Continuations {
		if err := document.Components.Schemas["CatalogSearch"].Value.VisitJSON(body, openapi3.EnableJSONSchema2020()); err != nil {
			t.Fatalf("continuation example: %v", err)
		}
	}
	if len(example.Continuations) != 2 {
		t.Fatal("missing live and hybrid continuation examples")
	}
	if len(example.Responses) != 4 {
		t.Fatal("missing retrieval states")
	}
	for _, response := range example.Responses {
		if err := document.Components.Schemas["CatalogSearchResponse"].Value.VisitJSON(response, openapi3.EnableJSONSchema2020()); err != nil {
			t.Fatal(err)
		}
	}
}
