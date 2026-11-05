package federation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

func TestLiveControlCategoryContexts(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      []string
	}{
		{"inline alias", `{"@context":{"topic":"http://purl.org/dc/terms/subject"},"topic":[" TOOLS ","CAFÉ"]}`, []string{"café", "tools"}},
		{"publisher entry alias", `{"@context":{"publisher":"http://purl.org/dc/terms/subject"},"publisher":["TOOLS","CAFÉ"]}`, []string{"café", "tools"}},
		{"prefix", `{"@context":{"dc":"http://purl.org/dc/terms/"},"dc:subject":"TOOLS"}`, []string{"tools"}},
		{"base and inline", `{"@context":["https://agenticresourcediscovery.org/context/v1",{"topic":"http://purl.org/dc/terms/subject"}],"topic":"TOOLS"}`, []string{"tools"}},
		{"combined aliases", `{"@context":{"a":"http://purl.org/dc/terms/subject","b":"http://purl.org/dc/terms/subject"},"a":"TOOLS","b":"CAFÉ"}`, []string{"café", "tools"}},
		{"value object", `{"@context":{"topic":"http://purl.org/dc/terms/subject"},"topic":{"@value":"tools"}}`, nil},
		{"IRI coercion", `{"@context":{"topic":{"@id":"http://purl.org/dc/terms/subject","@type":"@id"}},"topic":"https://tools.example/"}`, nil},
		{"reverse", `{"@context":{"topic":{"@reverse":"http://purl.org/dc/terms/subject"}},"topic":"tools"}`, nil},
		{"remote not fetched", `{"@context":"https://context.example/private","http://purl.org/dc/terms/subject":"tools"}`, nil},
		{"protected redefinition", `{"@context":[{"@protected":true,"topic":"http://purl.org/dc/terms/subject"},{"topic":"https://other.example/topic"}],"topic":"tools"}`, nil},
		{"alias raw count", `{"@context":{"a":"http://purl.org/dc/terms/subject","b":"http://purl.org/dc/terms/subject"},"a":[` + strings.Repeat(`"tools",`, 15) + `"tools"],"b":"tools"}`, nil},
		{"invalid JSON", `{`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := projectLiveControlFilters(t.Context(), []byte(tc.raw), true)
			if !reflect.DeepEqual(got.Categories, tc.want) {
				t.Fatalf("categories=%v want=%v", got.Categories, tc.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := projectLiveControlFilters(ctx, []byte(`{"metadata":{"license":"MIT"},"http://purl.org/dc/terms/subject":"tools"}`), true); got.License == nil || *got.License != "MIT" || got.Categories != nil {
		t.Fatal("cancelled category work lost independent license evidence", got)
	}
}

func TestLiveControlFiltersPrecedeAcceptedCountsAndPaging(t *testing.T) {
	for _, fault := range []string{"none", "refusal", "outage", "exclusion"} {
		t.Run(fault, func(t *testing.T) {
			manager, store, outbound, sources := newLivePagingFixture(t, 3, "healthy", "other")
			if fault == "exclusion" {
				source := sources[0]
				source.Scope.ExcludedIdentifiers = []string{"urn:air:remote.example:agent:match"}
				store.sources[source.ID] = source
			}
			entry := func(name, fields string) string {
				base := livePagingEntry(name, 90)
				return strings.TrimSuffix(base, "}") + fields + "}"
			}
			match := entry("match", `,"metadata":{"license":"MIT"},"@context":{"topic":"http://purl.org/dc/terms/subject"},"topic":[" TOOLS ","CAFÉ"]`)
			outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
				var body struct {
					Query struct {
						Filter map[string]any `json:"filter"`
					} `json:"query"`
				}
				if err := json.Unmarshal(request.Body, &body); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(body.Query.Filter, map[string]any{"metadata.license": "MIT"}) {
					t.Fatal("lossy category preselection or missing exact license", body.Query.Filter)
				}
				if request.URL == sources[1].RegistrySearchURL {
					if fault == "refusal" {
						return fetcher.Result{Status: 403}, nil
					}
					if fault == "outage" {
						return fetcher.Result{}, errors.New("synthetic peer outage")
					}
					return livePagingPage(t, ""), nil
				}
				if livePagingToken(t, request) == "" {
					return livePagingPage(t, "next",
						entry("or-only", `,"metadata":{"license":"MIT"},"http://purl.org/dc/terms/subject":"tools"`),
						entry("wrong-case", `,"metadata":{"license":"mit"},"http://purl.org/dc/terms/subject":["tools","café"]`),
						entry("sparse", ""),
						entry("malformed", `,"metadata":{"license":true},"http://purl.org/dc/terms/subject":["tools",1]`),
						entry("oversized", `,"metadata":{"license":"MIT"},"http://purl.org/dc/terms/subject":[`+strings.Repeat(`"tools",`, 16)+`"café"]`)), nil
				}
				return livePagingPage(t, "", match), nil
			}
			query := catalog.SearchQuery{Text: "agent", License: "MIT", Categories: []string{"café", "tools"}, Origins: []catalog.SearchResultType{catalog.SearchResultLive}}
			page, err := manager.SearchLiveCatalog(livePagingContext(t), catalog.FederatedCatalogSearchRequest{Query: query, Limit: 1, IncludeLive: true})
			want := 1
			if fault == "exclusion" {
				want = 0
			}
			if err != nil || len(page.Live) != want || !page.Partial || len(outbound.requests) != 3 || !slices.ContainsFunc(page.Diagnostics, func(d catalog.CatalogSearchDiagnostic) bool { return d.Status == "insufficient_evidence" }) {
				t.Fatalf("page=%+v requests=%d error=%v", page, len(outbound.requests), err)
			}
			if want == 1 && (page.Live[0].Identifier != "urn:air:remote.example:agent:match" || string(page.Live[0].Live.Entry) != match) {
				t.Fatal("matching original evidence was lost", page)
			}
			if len(store.observations) != 0 || len(store.snapshots) != 0 || slices.ContainsFunc(page.Diagnostics, func(d catalog.CatalogSearchDiagnostic) bool { return d.Status == "invalid_response" }) {
				t.Fatal("optional evidence became retained state or an ARD violation", page)
			}
		})
	}
}

func TestLiveControlKnownNonmatchesRemainHealthy(t *testing.T) {
	manager, _, outbound, _ := newLivePagingFixture(t, 3, "healthy")
	entry := func(name, fields string) string {
		return strings.TrimSuffix(livePagingEntry(name, 90), "}") + fields + "}"
	}
	outbound.respond = func(_ context.Context, _ fetcher.Request) (fetcher.Result, error) {
		return livePagingPage(t, "",
			entry("wrong-case", `,"metadata":{"license":"mit"},"http://purl.org/dc/terms/subject":["tools","café"]`),
			entry("wrong-license-without-topics", `,"metadata":{"license":"mit"}`),
			entry("wrong-topic-without-license", `,"http://purl.org/dc/terms/subject":"other"`),
			entry("or-only", `,"metadata":{"license":"MIT"},"http://purl.org/dc/terms/subject":"tools"`),
			entry("known-empty", `,"metadata":{"license":"MIT"},"http://purl.org/dc/terms/subject":[]`)), nil
	}
	query := catalog.SearchQuery{Text: "agent", License: "MIT", Categories: []string{"café", "tools"}, Origins: []catalog.SearchResultType{catalog.SearchResultLive}}
	page, err := manager.SearchLiveCatalog(livePagingContext(t), catalog.FederatedCatalogSearchRequest{Query: query, Limit: 1, IncludeLive: true})
	if err != nil || len(page.Live) != 0 || page.Partial || len(page.Diagnostics) != 0 || len(outbound.requests) != 1 {
		t.Fatalf("known nonmatches became a partial failure: page=%+v requests=%d error=%v", page, len(outbound.requests), err)
	}
}

func TestLiveControlSingleDeclarationFilters(t *testing.T) {
	for _, tc := range []struct {
		name, license, match, nonmatch string
		categories                     []string
	}{
		{
			name: "license without topics", license: "MIT",
			match:    `,"metadata":{"license":"MIT"}`,
			nonmatch: `,"metadata":{"license":"mit"}`,
		},
		{
			name: "categories with unusable license", categories: []string{"café", "tools"},
			match:    `,"metadata":{"license":true},"http://purl.org/dc/terms/subject":["TOOLS","CAFÉ"]`,
			nonmatch: `,"http://purl.org/dc/terms/subject":"tools"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, _, outbound, _ := newLivePagingFixture(t, 3, "healthy")
			entry := func(name, fields string) string {
				return strings.TrimSuffix(livePagingEntry(name, 90), "}") + fields + "}"
			}
			match := entry("match", tc.match)
			outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
				var wire struct {
					Query struct {
						Filter map[string]any `json:"filter"`
					} `json:"query"`
				}
				if err := json.Unmarshal(request.Body, &wire); err != nil {
					t.Fatal(err)
				}
				want := 0
				if tc.license != "" {
					want = 1
				}
				if len(wire.Query.Filter) != want || tc.license != "" && wire.Query.Filter["metadata.license"] != tc.license {
					t.Fatal("independent filter changed upstream preselection", wire.Query.Filter)
				}
				return livePagingPage(t, "", entry("nonmatch", tc.nonmatch), match), nil
			}
			query := catalog.SearchQuery{Text: "agent", License: tc.license, Categories: tc.categories, Origins: []catalog.SearchResultType{catalog.SearchResultLive}}
			page, err := manager.SearchLiveCatalog(livePagingContext(t), catalog.FederatedCatalogSearchRequest{Query: query, Limit: 1, IncludeLive: true})
			if err != nil || len(page.Live) != 1 || page.Live[0].Identifier != "urn:air:remote.example:agent:match" || string(page.Live[0].Live.Entry) != match || page.Partial || len(page.Diagnostics) != 0 || len(outbound.requests) != 1 {
				t.Fatalf("unselected declaration affected admission: page=%+v requests=%d error=%v", page, len(outbound.requests), err)
			}
		})
	}
}
