package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	fixture "github.com/open-agentic-catalog/open-agentic-catalog/test/performance/scalefixture"
)

func TestScaleCasesHaveBoundedRequests(t *testing.T) {
	for _, size := range []int{1000, 10000, 100000} {
		for _, name := range scaleCases {
			s := &scaleProfile{Items: size, Case: name, Publisher: "00000000-0000-7000-8000-000000000010"}
			if name == "later" || name == "terminal" || strings.HasSuffix(name, "-third") {
				s.Cursor = "opaque"
			}
			if err := s.validate(); err != nil {
				t.Fatal(err)
			}
			q, total, offset := s.query()
			if total < 0 || total > size || q.Limit < 1 || q.Limit > 100 || offset > total {
				t.Fatalf("invalid query %s/%d: %+v %d %d", name, size, q, total, offset)
			}
			path, raw, err := s.request()
			if err != nil || !json.Valid(raw) || !strings.HasPrefix(path, "/api/v1/catalog/") {
				t.Fatalf("invalid request %s %s %v", path, raw, err)
			}
		}
	}
}

func TestScaleCursorNormalizationPreservesOtherEvidence(t *testing.T) {
	s := &scaleProfile{Items: 1000, Case: "all"}
	page := catalog.CatalogSearchResponse{DurableTotal: 1000, Generation: "generation-one", NextCursor: "first", Items: make([]catalog.CatalogSearchResult, 20)}
	encode := func() []byte {
		raw, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	first, err := scaleDigest(encode(), s)
	if err != nil {
		t.Fatal(err)
	}
	page.NextCursor = "rotated"
	next, err := scaleDigest(encode(), s)
	if err != nil || first != next {
		t.Fatal("cursor rotation changed semantic digest", err)
	}
	page.Generation = "generation-two"
	next, err = scaleDigest(encode(), s)
	if err != nil || first == next {
		t.Fatal("generation change was hidden", err)
	}
	page.NextCursor = ""
	if _, err := scaleDigest(encode(), s); err == nil {
		t.Fatal("accepted missing continuation")
	}
}

func TestScaleClosedDecoderAndProfile(t *testing.T) {
	for _, name := range []string{"later", "terminal", "publisher-asc-third", "publisher-desc-third"} {
		s := scaleProfile{Items: 1000, Case: name, Publisher: "00000000-0000-7000-8000-000000000010"}
		if s.validate() == nil {
			t.Fatal("later-page profile accepted without a cursor")
		}
	}
	for _, raw := range []string{`{"items":[],"items":[]}`, `{} {}`, `{"unexpected":true}`} {
		if err := decodeScale([]byte(raw), new(catalog.CatalogSearchResponse)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, s := range []scaleProfile{{Items: 1001, Case: "all"}, {Items: 1000, Case: "arbitrary"}, {Items: 1000, Case: "all", Publisher: "bad"}, {Items: 1000, Case: "all", Publisher: "00000000-0000-7000-8000-000000000010", Cursor: "unexpected"}} {
		if s.validate() == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}

func TestScaleZeroAndTerminalPageOracles(t *testing.T) {
	s := &scaleProfile{Items: 1000, Case: "absent"}
	page := catalog.CatalogSearchResponse{Items: []catalog.CatalogSearchResult{}, Profile: catalog.CoreSearchProfile, Generation: "generation", Tier: "core"}
	check := func(wantError bool) {
		raw, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		_, err = validateScale(raw, s)
		if (err != nil) != wantError {
			t.Fatalf("case=%s error=%v wantError=%v", s.Case, err, wantError)
		}
	}
	check(false)
	page.Partial = true
	check(true)
	page.Partial = false
	s.Case, s.Cursor = "terminal", "opaque"
	page.DurableTotal = 7
	for i := 5; i < 7; i++ {
		page.Items = append(page.Items, catalog.CatalogSearchResult{ID: fixture.Identifier(i, false), Identifier: fixture.Identifier(i, false), DisplayName: fixture.Name(i, false), Type: catalog.SearchResultLocal, Local: new((catalog.CatalogRecord{Resource: catalog.Resource{TenantID: fixture.Tenant}, Evidence: catalog.EvidenceSet{LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}}}).Summary().SearchLocal())})
	}
	check(false)
	page.NextCursor = "unexpected"
	check(true)
	page.NextCursor = ""
	page.Items[0], page.Items[1] = page.Items[1], page.Items[0]
	check(true)
	page.Items[0], page.Items[1] = page.Items[1], page.Items[0]
	page.Items[0].Local.Resource.TenantID = "another-tenant"
	check(true)
}

// TestScalePopulationOracle binds fixture identities to their population while
// retaining mixed results in queries that search both local and indexed data.
func TestScalePopulationOracle(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		total, count, offset   int
		indexed, mixed, cursor bool
	}{
		{name: "local", total: 897, count: 20},
		{name: "indexed", total: 97, count: 20, indexed: true},
		{name: "exact-local", total: 1, count: 1},
		{name: "exact-indexed", total: 1, count: 1, indexed: true},
		{name: "later", total: 897, count: 20, offset: 40, cursor: true},
		{name: "publisher-asc-first", total: 897, count: 20},
		{name: "publisher-desc-first", total: 897, count: 20},
		{name: "publisher-asc-third", total: 897, count: 20, offset: 40, cursor: true},
		{name: "publisher-desc-third", total: 897, count: 20, offset: 40, cursor: true},
		{name: "terminal", total: 7, count: 2, offset: 5, cursor: true},
		{name: "all", total: 1000, count: 20, mixed: true},
		{name: "common", total: 994, count: 20, mixed: true},
		{name: "rare", total: 10, count: 10, mixed: true},
		{name: "kind", total: 248, count: 20, mixed: true},
		{name: "plugin", total: 248, count: 20, mixed: true},
		{name: "plugin-local", total: 224, count: 20},
		{name: "plugin-indexed", total: 24, count: 20, indexed: true},
		{name: "tag", total: 200, count: 20, mixed: true},
		{name: "wide", total: 1000, count: 100, mixed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &scaleProfile{Items: 1000, Case: tc.name, Publisher: "00000000-0000-7000-8000-000000000010"}
			if tc.cursor {
				s.Cursor = "opaque"
			}
			if err := s.validate(); err != nil {
				t.Fatal(err)
			}
			page := catalog.CatalogSearchResponse{Items: []catalog.CatalogSearchResult{}, DurableTotal: tc.total, Profile: catalog.CoreSearchProfile, Generation: "generation", Tier: "core"}
			if tc.offset+tc.count < tc.total {
				page.NextCursor = "next"
			}
			setPopulation := func(item *catalog.CatalogSearchResult, indexed bool, index int) {
				item.Local, item.Indexed = nil, nil
				if indexed {
					item.Type = catalog.SearchResultIndexed
					item.Indexed = &catalog.IndexedSearchEvidence{SourceCount: 1, SourceIDs: []string{"fixture-source"}}
					if index >= 0 && index < 7 {
						item.Indexed.SourceCount = 2
						item.Indexed.SourceIDs = append(item.Indexed.SourceIDs, "duplicate-source")
					}
				} else {
					item.Type = catalog.SearchResultLocal
					item.Local = new((catalog.CatalogRecord{Resource: catalog.Resource{TenantID: fixture.Tenant}, Evidence: catalog.EvidenceSet{LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}}}).Summary().SearchLocal())
				}
			}
			indexedCount := tc.count / 2
			if tc.name == "rare" {
				indexedCount = 1
			}
			for position := 0; position < tc.count; position++ {
				indexed := tc.indexed || tc.mixed && position < indexedCount
				i := tc.offset + position
				if tc.mixed && !indexed {
					i -= indexedCount
				}
				switch tc.name {
				case "rare":
					i *= 100
				case "kind":
					i = 4*i + 2
				case "plugin", "plugin-local", "plugin-indexed":
					i = 4*i + 3
				case "tag":
					i = 5*i + 1
				}
				item := catalog.CatalogSearchResult{ID: fixture.Identifier(i, indexed), Identifier: fixture.Identifier(i, indexed), DisplayName: fixture.Name(i, indexed), Kind: fixture.Kind(i), Tags: fixture.Tags(i), Description: fixture.Description(i)}
				setPopulation(&item, indexed, i)
				page.Items = append(page.Items, item)
			}
			if strings.HasPrefix(tc.name, "publisher-") {
				for i := range page.Items {
					page.Items[i].Publisher = s.Publisher
					page.Items[i].Local.Display = &catalog.CatalogPresentation{PublisherName: fixture.Publisher}
				}
			}
			check := func(wantError bool) {
				t.Helper()
				raw, err := json.Marshal(page)
				if err != nil {
					t.Fatal(err)
				}
				count, err := validateScale(raw, s)
				if (err != nil) != wantError || !wantError && count != tc.count {
					t.Fatalf("count=%d err=%v wantError=%v", count, err, wantError)
				}
			}
			check(false)
			if strings.HasPrefix(tc.name, "publisher-") {
				page.Items[0].Publisher = "other"
				check(true)
				page.Items[0].Publisher = s.Publisher
				page.Items[0].Local.Display.PublisherName = "Other publisher"
				check(true)
				page.Items[0].Local.Display.PublisherName = fixture.Publisher
				page.Items[0], page.Items[1] = page.Items[1], page.Items[0]
				check(true)
				page.Items[0], page.Items[1] = page.Items[1], page.Items[0]
				check(false)
			}
			if tc.indexed || tc.mixed {
				// Keep the count and both contributions; corrupt only their IDs.
				evidence := page.Items[0].Indexed
				original := evidence.SourceIDs
				for _, sources := range [][]string{{original[0], original[0]}, {original[0], ""}, {"", original[1]}} {
					evidence.SourceIDs = sources
					check(true)
				}
				evidence.SourceIDs = original
				check(false)
			}
			if !tc.mixed {
				// Keep the expected identifier and other page evidence unchanged;
				// corrupt only the last result's population and matching shape.
				setPopulation(&page.Items[len(page.Items)-1], !tc.indexed, -1)
				check(true)
			}
		})
	}
}

func TestScaleDuplicateSourcesApplyOnlyToExactFixtureIdentities(t *testing.T) {
	for i := range 8 {
		wanted := 2
		if i == 7 {
			wanted = 1
		}
		if actual := scaleSources(fixture.Identifier(i, true)); actual != wanted {
			t.Fatalf("index %d: got %d, want %d", i, actual, wanted)
		}
	}
	for _, identifier := range []string{
		"urn:air:scale.example:scale:indexed-0",
		"urn:air:scale.example:scale:indexed-+00000",
		"urn:air:scale.example:scale:indexed--00000",
		"urn:air:scale.example:scale:indexed-0000000",
		"urn:air:scale.example:scale:indexed-000000/1.0.0",
		"urn:air:scale.example:scale:indexed-invalid",
		"urn:air:scale.example:scale:local-000000",
		"urn:air:other.example:scale:indexed-000000",
		"",
	} {
		if actual := scaleSources(identifier); actual != 1 {
			t.Fatalf("nonmatching identity %q gained duplicate contributions: %d", identifier, actual)
		}
	}
}

func TestScaleFacetOracleChecksOtherCountAndBucketOrder(t *testing.T) {
	s := &scaleProfile{Items: 1000, Case: "facets"}
	page := catalog.ExploreResponse{Matched: 897, Facets: map[string]catalog.FacetResult{
		"kind": {Buckets: []catalog.FacetBucket{{Value: "agent", Count: 225}, {Value: "mcp_server", Count: 224}}, OtherCount: 448},
		"tag":  {Buckets: []catalog.FacetBucket{{Value: "scale", Count: 897}, {Value: "group-0", Count: 180}}, OtherCount: 724},
	}}
	check := func(wantError bool) {
		raw, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		_, err = validateScale(raw, s)
		if (err != nil) != wantError {
			t.Fatalf("error=%v wantError=%v", err, wantError)
		}
	}
	check(false)
	wrong := page.Facets["tag"]
	wrong.OtherCount++
	page.Facets["tag"] = wrong
	check(true)
	wrong.OtherCount--
	wrong.Buckets[0], wrong.Buckets[1] = wrong.Buckets[1], wrong.Buckets[0]
	page.Facets["tag"] = wrong
	check(true)
}
