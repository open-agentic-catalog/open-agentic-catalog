package scalefixture

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestRepresentationsValidate(t *testing.T) {
	for _, i := range []int{0, 1, 2, 50, 100} {
		result := adapter.NewRegistry().Validate(t.Context(), Kind(i), Representation(i))
		for _, finding := range result.Findings {
			if finding.Severity == "error" {
				t.Errorf("representation %d: %+v", i, finding)
			}
		}
	}
}

func TestPluginFixturesBindCompletePackagesAndIndexedMetadata(t *testing.T) {
	seen := map[string]bool{}
	for _, i := range []int{3, 7, 11} {
		input := Representation(i)
		body, ok := PluginArchive(input.URL)
		if !ok || Kind(i) != catalog.KindPlugin || input.MediaType != catalog.PluginMediaZIP || len(input.Data) != 0 || len(input.PackageData) != 0 || input.ExpectedDigest == nil || *input.ExpectedDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(body)) {
			t.Fatal("invalid external package identity", i)
		}
		inspection := adapter.InspectPlugin(t.Context(), input.MediaType, body, catalog.DefaultDiagnosticLimits())
		if inspection.Plugin == nil || catalog.ValidatePluginInspection(inspection.Plugin) != nil || len(inspection.Plugin.Components) == 0 {
			t.Fatal("complete package evidence was not retained", i)
		}
		for _, finding := range inspection.Findings {
			if finding.Severity == catalog.SeverityError {
				t.Fatal(finding)
			}
		}
		entry := Entry(i)
		metadata, typed := entry["metadata"].(map[string]any)
		if !typed || entry["type"] != catalog.PluginMediaZIP || entry["url"] != input.URL || entry["data"] != nil || metadata["packageSize"] != len(body) || metadata["packageDigest"] != *input.ExpectedDigest {
			t.Fatal("indexed package metadata differs from actual bytes", entry)
		}
		seen[input.URL] = true
	}
	if len(seen) != 3 {
		t.Fatal("sample package coverage was lost")
	}
	if _, ok := PluginArchive("https://unexpected.example/plugin.zip"); ok {
		t.Fatal("arbitrary package destination was accepted")
	}
}

func TestFourKindPopulations(t *testing.T) {
	for _, trial := range []struct {
		items          int
		local, indexed map[catalog.Kind]int
	}{
		{1000, map[catalog.Kind]int{catalog.KindAgent: 225, catalog.KindMCPServer: 224, catalog.KindSkill: 224, catalog.KindPlugin: 224}, map[catalog.Kind]int{catalog.KindAgent: 25, catalog.KindMCPServer: 24, catalog.KindSkill: 24, catalog.KindPlugin: 24}},
		{10000, map[catalog.Kind]int{catalog.KindAgent: 2250, catalog.KindMCPServer: 2249, catalog.KindSkill: 2249, catalog.KindPlugin: 2249}, map[catalog.Kind]int{catalog.KindAgent: 250, catalog.KindMCPServer: 249, catalog.KindSkill: 249, catalog.KindPlugin: 249}},
		{100000, map[catalog.Kind]int{catalog.KindAgent: 22500, catalog.KindMCPServer: 22499, catalog.KindSkill: 22499, catalog.KindPlugin: 22499}, map[catalog.Kind]int{catalog.KindAgent: 2500, catalog.KindMCPServer: 2499, catalog.KindSkill: 2499, catalog.KindPlugin: 2499}},
	} {
		for _, population := range []struct {
			indexed  bool
			expected map[catalog.Kind]int
		}{{false, trial.local}, {true, trial.indexed}} {
			counts := map[catalog.Kind]int{}
			for i := 0; i < Count(trial.items, population.indexed); i++ {
				counts[Kind(i)]++
			}
			if !maps.Equal(counts, population.expected) {
				t.Fatal(trial, population, counts)
			}
		}
	}
}
