// Scenario validation checks response invariants while allowing mixed-write
// generations and cursor values to change during performance measurements.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// The unchanged smoke uses byte-identical response validation. Mixed writes
// legitimately change generations and cursors, but not the closed response,
// local tenant boundary, union shape or bounded page accounting.
func validateScenario(body []byte, p profile) (int, error) {
	if p.Scenario == "hybrid" {
		return validateHybrid(body, p.HybridItems)
	}
	if p.Scale != nil {
		return validateScale(body, p.Scale)
	}
	if p.Scenario == "" || p.Scenario == "local" {
		page, err := validateBaseline(body)
		return len(page.Items), err
	}
	if jsondoc.Validate(body) != nil {
		return 0, errors.New("invalid-scenario-json")
	}
	if p.Scenario == "public" {
		var document struct {
			Entries []struct {
				Identifier string `json:"identifier"`
				Type       string `json:"type"`
			} `json:"entries"`
		}
		expected := 4
		if p.Public != nil {
			expected = p.Public.Entries
		}
		if json.Unmarshal(body, &document) != nil || len(document.Entries) != expected {
			return 0, errors.New("invalid-public-manifest")
		}
		seen := make(map[string]bool)
		registries := 0
		for _, entry := range document.Entries {
			if entry.Identifier == "" || entry.Type == "" || seen[entry.Identifier] {
				return 0, errors.New("invalid-public-entry")
			}
			seen[entry.Identifier] = true
			if entry.Type == "application/ai-registry+json" {
				registries++
			}
		}
		if registries != 1 {
			return 0, errors.New("invalid-public-registry")
		}
		return len(document.Entries), nil
	}
	var page catalog.CatalogSearchResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&page) != nil || decoder.Decode(new(any)) != io.EOF || page.Partial || page.LiveCount != 0 || page.Generation == "" || page.Profile != catalog.CoreSearchProfile || page.Tier != "core" || page.DurableTotal < 3 || len(page.Items) != min(20, page.DurableTotal) || (page.NextCursor != "") != (page.DurableTotal > 20) {
		return 0, errors.New("invalid-scenario-page")
	}
	if p.MixedScaleItems != 0 {
		interval := 30
		if p.Seconds == 3600 {
			interval = 60
		}
		if page.DurableTotal < p.MixedScaleItems || page.DurableTotal > p.MixedScaleItems+max(1, p.Seconds/interval) {
			return 0, errors.New("invalid-mixed-scale-population")
		}
	}
	seen := make(map[string]bool)
	for _, item := range page.Items {
		if item.ID == "" || seen[item.ID] || item.Live != nil {
			return 0, errors.New("invalid-scenario-union")
		}
		seen[item.ID] = true
		if item.Local != nil {
			if p.Scenario == "indexed" || item.Type != catalog.SearchResultLocal || item.Indexed != nil || item.Local.Resource.TenantID != "00000000-0000-7000-8000-000000000001" {
				return 0, errors.New("invalid-or-cross-tenant-result")
			}
		} else {
			sources := 1
			if p.MixedScaleItems != 0 {
				sources = scaleSources(item.Identifier)
			}
			if item.Type != catalog.SearchResultIndexed || item.Indexed == nil || item.Indexed.SourceCount != sources || len(item.Indexed.SourceIDs) != sources || item.Indexed.SourcesTruncated || item.Indexed.Conflict || item.Indexed.SameSourceConflict {
				return 0, errors.New("invalid-scenario-union")
			}
			for index, source := range item.Indexed.SourceIDs {
				if source == "" || slices.Contains(item.Indexed.SourceIDs[:index], source) {
					return 0, errors.New("invalid-scenario-union")
				}
			}
		}
	}
	return len(page.Items), nil
}

// validateHybrid preserves full fixture accounting while a concurrent rebuild
// changes the generation. Only declared lexical fallback may replace hybrid.
func validateHybrid(body []byte, items int) (int, error) {
	if jsondoc.Validate(body) != nil {
		return 0, errors.New("invalid-hybrid-json")
	}
	var page catalog.CatalogSearchResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&page) != nil || decoder.Decode(new(any)) != io.EOF || page.PartialIndex {
		return 0, errors.New("invalid-hybrid-page")
	}
	if items == 1000 {
		// Three demo releases are outside the exact scale tag. This pilot
		// requires all 997 added releases, not a top-k approximate count.
		if page.DurableTotal != 997 || len(page.Items) != 20 || page.NextCursor == "" {
			return 0, errors.New("invalid-hybrid-scale-page")
		}
		for _, item := range page.Items {
			if item.Local == nil || !slices.Contains(item.Tags, "scale") {
				return 0, errors.New("invalid-hybrid-scale-filter")
			}
		}
	} else if page.DurableTotal != 3 || len(page.Items) != 3 || page.NextCursor != "" {
		return 0, errors.New("invalid-hybrid-page")
	}
	if page.Tier == "hybrid" {
		if len(page.Profile) != 71 || page.Profile[:7] != "sha256:" || page.FallbackReason != "" {
			return 0, errors.New("invalid-hybrid-mode")
		}
	} else if page.Tier != "core" || page.Profile != catalog.CoreSearchProfile || (page.FallbackReason != "indexing" && page.FallbackReason != "index_changed" && page.FallbackReason != "provider_unavailable") {
		return 0, errors.New("invalid-hybrid-fallback")
	}
	page.Tier, page.Profile = "core", catalog.CoreSearchProfile
	// Reuse the baseline's union, tenant and identity checks after checking
	// the pilot's complete totals and continuation above.
	page.DurableTotal, page.NextCursor = len(page.Items), ""
	raw, err := json.Marshal(page)
	if err != nil {
		return 0, err
	}
	_, err = validateBaseline(raw)
	return len(page.Items), err
}
