// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestMixedScaleChecksDuplicateSourcesAndPopulationWithoutRelaxingLoad(t *testing.T) {
	page := catalog.CatalogSearchResponse{DurableTotal: 1000, Generation: "current", Profile: catalog.CoreSearchProfile, Tier: "core", NextCursor: "next"}
	for i := range 20 {
		sources := []string{"one"}
		if i < 7 {
			sources = append(sources, "two")
		}
		page.Items = append(page.Items, catalog.CatalogSearchResult{ID: fmt.Sprint(i), Identifier: fmt.Sprintf("urn:air:scale.example:scale:indexed-%06d", i), Type: catalog.SearchResultIndexed, Indexed: &catalog.IndexedSearchEvidence{SourceCount: len(sources), SourceIDs: sources}})
	}
	p := profile{Scenario: "mixed", MixedScaleItems: 1000, Seconds: 30}
	check := func(profile profile, wantError bool) {
		t.Helper()
		raw, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		count, err := validateScenario(raw, profile)
		if (err != nil) != wantError || !wantError && count != 20 {
			t.Fatal(count, err, wantError)
		}
	}
	check(p, false)
	check(profile{Scenario: "mixed"}, true)
	page.DurableTotal = 1001
	check(p, false)
	page.DurableTotal = 1002
	check(p, true)
	page.DurableTotal = 999
	check(p, true)
	page.DurableTotal = 1000
	page.Items[0].Indexed.SourceIDs = []string{"one", "one"}
	check(p, true)
	page.Items[0].Indexed.SourceIDs = []string{"one", ""}
	check(p, true)
	page.Items[0].Indexed.SourceIDs = []string{"one", "two"}
	page.Items[0].Identifier = "urn:air:other.example:scale:indexed-000000"
	check(p, true)
	page.Items[0].Identifier = "urn:air:scale.example:scale:indexed-000007"
	check(p, true)
	valid := profile{Scenario: "mixed", MixedScaleItems: 1000, Port: 8080, Seconds: 30, Workers: 2, MaxRequests: 1000}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	badScenario, badSize := valid, valid
	badScenario.Scenario, badSize.MixedScaleItems = "local", 999
	for _, change := range []profile{badScenario, badSize} {
		if change.validate() == nil {
			t.Fatal("invalid mixed-scale profile accepted")
		}
	}
}

func TestPerformanceScenarioContracts(t *testing.T) {
	var page catalog.CatalogSearchResponse
	if err := json.Unmarshal(validPage(t), &page); err != nil {
		t.Fatal(err)
	}
	encode := func() []byte {
		t.Helper()
		raw, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, p := range []profile{{Scenario: "local"}, {Scenario: "mixed"}} {
		if count, err := validateScenario(encode(), p); err != nil || count != 3 {
			t.Fatalf("scenario=%s count=%d error=%v", p.Scenario, count, err)
		}
	}
	if _, err := validateScenario(encode(), profile{Scenario: "indexed"}); err == nil {
		t.Fatal("local records accepted as indexed")
	}
	for i := range page.Items {
		page.Items[i].Local = nil
		page.Items[i].Type = catalog.SearchResultIndexed
		page.Items[i].Indexed = &catalog.IndexedSearchEvidence{SourceCount: 1, SourceIDs: []string{"owned"}}
	}
	if count, err := validateScenario(encode(), profile{Scenario: "indexed"}); err != nil || count != 3 {
		t.Fatal("indexed fixture rejected")
	}
	page.Items[0].Type = catalog.SearchResultLocal
	if _, err := validateScenario(encode(), profile{Scenario: "mixed"}); err == nil {
		t.Fatal("mismatched union discriminator accepted")
	}
	page.Items[0].Type = catalog.SearchResultIndexed
	page.Items[0].Live = &catalog.LiveSearchEvidence{}
	if _, err := validateScenario(encode(), profile{Scenario: "mixed"}); err == nil {
		t.Fatal("unexpected live result accepted")
	}
	page.Items[0].Live = nil
	page.DurableTotal = 30
	if _, err := validateScenario(encode(), profile{Scenario: "mixed"}); err == nil {
		t.Fatal("incomplete page accepted")
	}
	page.DurableTotal = 3
	page.Items[0].Local = new((catalog.CatalogRecord{Resource: catalog.Resource{TenantID: "foreign"}}).Summary().SearchLocal())
	if _, err := validateScenario(encode(), profile{Scenario: "mixed"}); err == nil {
		t.Fatal("cross-tenant local record accepted")
	}
	for _, body := range []string{`{"entries":[]}`, `{"entries":[],"entries":[{},{},{}]}`, `{}`} {
		if _, err := validateScenario([]byte(body), profile{Scenario: "public"}); err == nil {
			t.Fatal("bad manifest accepted")
		}
	}
	manifest := `{"entries":[{"identifier":"registry","type":"application/ai-registry+json"},{"identifier":"agent","type":"agent"},{"identifier":"mcp","type":"mcp"},{"identifier":"skill","type":"skill"}]}`
	if count, err := validateScenario([]byte(manifest), profile{Scenario: "public"}); err != nil || count != 4 {
		t.Fatal("three releases plus Registry entry rejected")
	}
}

func TestHybridLoadModeAccounting(t *testing.T) {
	var page catalog.CatalogSearchResponse
	if err := json.Unmarshal(validPage(t), &page); err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		raw, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		_, err = validateScenario(raw, profile{Scenario: "hybrid"})
		if (err == nil) != want {
			t.Fatalf("hybrid accounting: %v", err)
		}
	}
	check(false) // A default-off core fixture cannot qualify enabled retrieval.
	page.Tier, page.Profile = "hybrid", "sha256:"+strings.Repeat("a", 64)
	check(true)
	page.Tier, page.Profile, page.FallbackReason = "core", catalog.CoreSearchProfile, "indexing"
	check(true)
	page.FallbackReason = "private error"
	check(false)
	page.FallbackReason = "provider_unavailable"
	page.DurableTotal++
	check(false)
}

// The large pilot checks complete population accounting independently of the
// small browser fixture, while retaining the same tenant/union validation.
func TestHybridScaleLoadAccounting(t *testing.T) {
	var page catalog.CatalogSearchResponse
	if err := json.Unmarshal(validPage(t), &page); err != nil {
		t.Fatal(err)
	}
	item := page.Items[0]
	page.Items = nil
	for i := range 20 {
		candidate := item
		local := *item.Local
		candidate.Local = &local
		candidate.ID = "pilot-" + strconv.Itoa(i)
		candidate.Tags = []string{"scale"}
		page.Items = append(page.Items, candidate)
	}
	page.Tier, page.Profile = "hybrid", "sha256:"+strings.Repeat("a", 64)
	page.DurableTotal, page.NextCursor = 997, "next"
	check := func(want bool) {
		t.Helper()
		raw, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		_, err = validateScenario(raw, profile{Scenario: "hybrid", HybridItems: 1000})
		if (err == nil) != want {
			t.Fatalf("pilot accounting: %v", err)
		}
	}
	check(true)
	page.DurableTotal = 20
	check(false)
	page.DurableTotal, page.NextCursor = 997, ""
	check(false)
	page.NextCursor = "next"
	page.Items[0].Tags = nil
	check(false)
	for _, p := range []profile{{Scenario: "hybrid", HybridItems: 1001}, {Scenario: "local", HybridItems: 1000}} {
		if p.validate() == nil {
			t.Fatal("unsupported pilot profile accepted")
		}
	}
}

func TestPerformanceRejectsWrongSearchProfileTierAndLocalUnion(t *testing.T) {
	for name, mutate := range map[string]func(*catalog.CatalogSearchResponse){
		"profile":                func(p *catalog.CatalogSearchResponse) { p.Profile = "unimplemented-profile" },
		"tier":                   func(p *catalog.CatalogSearchResponse) { p.Tier = "unimplemented-tier" },
		"discriminator":          func(p *catalog.CatalogSearchResponse) { p.Items[0].Type = catalog.SearchResultIndexed },
		"extra indexed evidence": func(p *catalog.CatalogSearchResponse) { p.Items[0].Indexed = &catalog.IndexedSearchEvidence{} },
		"extra live evidence":    func(p *catalog.CatalogSearchResponse) { p.Items[0].Live = &catalog.LiveSearchEvidence{} },
	} {
		t.Run(name, func(t *testing.T) {
			var page catalog.CatalogSearchResponse
			if err := json.Unmarshal(validPage(t), &page); err != nil {
				t.Fatal(err)
			}
			mutate(&page)
			raw, err := json.Marshal(page)
			if err != nil {
				t.Fatal(err)
			}
			for _, scenario := range []string{"", "local", "mixed"} {
				if _, err := validateScenario(raw, profile{Scenario: scenario}); err == nil {
					t.Errorf("scenario %q accepted an invalid %s", scenario, name)
				}
			}
		})
	}
}

func TestPerformanceIndexedPaginationAndEvidenceContracts(t *testing.T) {
	t.Parallel()
	page := catalog.CatalogSearchResponse{Profile: catalog.CoreSearchProfile, Tier: "core", Generation: "1", DurableTotal: 21, NextCursor: "bounded-cursor"}
	for i := range 20 {
		page.Items = append(page.Items, catalog.CatalogSearchResult{ID: strconv.Itoa(i), Type: catalog.SearchResultIndexed, Indexed: &catalog.IndexedSearchEvidence{SourceCount: 1, SourceIDs: []string{"owned"}}})
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"indexed", "mixed"} {
		t.Run(scenario, func(t *testing.T) {
			if count, err := validateScenario(raw, profile{Scenario: scenario}); err != nil || count != 20 {
				t.Fatalf("valid bounded page: count=%d error=%v", count, err)
			}
			for name, mutate := range map[string]func(*catalog.CatalogSearchResponse){
				"no cursor":            func(p *catalog.CatalogSearchResponse) { p.NextCursor = "" },
				"unexpected cursor":    func(p *catalog.CatalogSearchResponse) { p.DurableTotal = 20 },
				"short page":           func(p *catalog.CatalogSearchResponse) { p.Items = p.Items[:19] },
				"oversized page":       func(p *catalog.CatalogSearchResponse) { p.Items = append(p.Items, p.Items[0]) },
				"partial":              func(p *catalog.CatalogSearchResponse) { p.Partial = true },
				"profile":              func(p *catalog.CatalogSearchResponse) { p.Profile = "unsupported" },
				"tier":                 func(p *catalog.CatalogSearchResponse) { p.Tier = "unsupported" },
				"duplicate ID":         func(p *catalog.CatalogSearchResponse) { p.Items[1].ID = p.Items[0].ID },
				"missing ID":           func(p *catalog.CatalogSearchResponse) { p.Items[0].ID = "" },
				"missing indexed":      func(p *catalog.CatalogSearchResponse) { p.Items[0].Indexed = nil },
				"missing source":       func(p *catalog.CatalogSearchResponse) { p.Items[0].Indexed.SourceIDs = nil },
				"empty source":         func(p *catalog.CatalogSearchResponse) { p.Items[0].Indexed.SourceIDs[0] = "" },
				"multiple sources":     func(p *catalog.CatalogSearchResponse) { p.Items[0].Indexed.SourceIDs = []string{"owned", "other"} },
				"source count":         func(p *catalog.CatalogSearchResponse) { p.Items[0].Indexed.SourceCount = 2 },
				"truncated sources":    func(p *catalog.CatalogSearchResponse) { p.Items[0].Indexed.SourcesTruncated = true },
				"conflict":             func(p *catalog.CatalogSearchResponse) { p.Items[0].Indexed.Conflict = true },
				"same-source conflict": func(p *catalog.CatalogSearchResponse) { p.Items[0].Indexed.SameSourceConflict = true },
			} {
				t.Run(name, func(t *testing.T) {
					var changed catalog.CatalogSearchResponse
					if err := json.Unmarshal(raw, &changed); err != nil {
						t.Fatal(err)
					}
					mutate(&changed)
					body, err := json.Marshal(changed)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := validateScenario(body, profile{Scenario: scenario}); err == nil {
						t.Fatal("invalid fixture page accepted")
					}
				})
			}
			terminalPage := page
			terminalPage.DurableTotal, terminalPage.NextCursor = 20, ""
			terminal, err := json.Marshal(terminalPage)
			if err != nil {
				t.Fatal(err)
			}
			if count, err := validateScenario(terminal, profile{Scenario: scenario}); err != nil || count != 20 {
				t.Fatalf("valid terminal page: count=%d error=%v", count, err)
			}
		})
	}
}

func TestPerformancePublicManifestRejectsAmbiguousEntries(t *testing.T) {
	t.Parallel()
	manifest := `{"entries":[{"identifier":"registry","type":"application/ai-registry+json"},{"identifier":"agent","type":"agent"},{"identifier":"mcp","type":"mcp"},{"identifier":"skill","type":"skill"}]}`
	for name, body := range map[string]string{
		"empty identifier":     strings.Replace(manifest, `"identifier":"agent"`, `"identifier":""`, 1),
		"empty type":           strings.Replace(manifest, `"type":"agent"`, `"type":""`, 1),
		"duplicate identifier": strings.Replace(manifest, `"identifier":"agent"`, `"identifier":"registry"`, 1),
		"no Registry":          strings.Replace(manifest, "application/ai-registry+json", "agent", 1),
		"two Registries":       strings.Replace(manifest, `"type":"agent"`, `"type":"application/ai-registry+json"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateScenario([]byte(body), profile{Scenario: "public"}); err == nil {
				t.Fatal("ambiguous public manifest accepted")
			}
		})
	}
}
