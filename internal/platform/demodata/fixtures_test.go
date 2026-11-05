package demodata

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestSampleInventoryAndArchives(t *testing.T) {
	counts := map[catalog.Kind]int{}
	namespaces := map[string]int{}
	seen := map[string]bool{}
	for _, sample := range Samples() {
		if sample.Name == "" || sample.Description == "" || seen[sample.Key] {
			t.Fatal("invalid sample inventory")
		}
		seen[sample.Key] = true
		counts[sample.Kind]++
		namespaces[sample.Namespace]++
		if sample.Kind != catalog.KindPlugin {
			result := adapter.NewRegistry().Validate(t.Context(), sample.Kind, Representation(sample, ""))
			for _, finding := range result.Findings {
				if sample.Kind != catalog.KindMCPServer || finding.Code != "MCP_SERVER_CARD_EXPERIMENTAL" || finding.Severity != "warning" {
					t.Fatalf("%s representation: %+v", sample.Key, result.Findings)
				}
			}
			continue
		}
		representation := Representation(sample, "https://127.0.0.1:18443")
		raw, digest, ok := Archive("/" + sample.Key + ".zip")
		if representation.URL != "https://127.0.0.1:18443/"+sample.Key+".zip" || representation.ExpectedDigest == nil || *representation.ExpectedDigest != digest {
			t.Fatal("Plugin fixture identity mismatch")
		}
		if !ok {
			t.Fatal("missing archive")
		}
		again, other, _ := Archive("/" + sample.Key + ".zip")
		if !bytes.Equal(raw, again) || digest != other || digest != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) || len(raw) > 8192 {
			t.Fatal("archive drift or size")
		}
		result := adapter.InspectPlugin(t.Context(), catalog.PluginMediaZIP, raw, catalog.DefaultDiagnosticLimits())
		if len(result.Findings) != 0 {
			t.Fatalf("%s: %+v", sample.Key, result.Findings)
		}
		archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(archive.File))
		for _, f := range archive.File {
			got = append(got, f.Name)
			if f.Mode().Perm() != 0444 || f.Modified.Year() != 1980 {
				t.Fatal("archive metadata drift")
			}
		}
		expected := map[string][]string{"repository-essentials": {"plugin.json", "skills/review-repository/SKILL.md"}, "documentation-toolkit": {"mcp.json", "plugin.json"}, "release-workflow": {"mcp.json", "plugin.json", "skills/prepare-release/SKILL.md"}}
		if !reflect.DeepEqual(got, expected[sample.Key]) {
			t.Fatalf("%s inventory %v", sample.Key, got)
		}
	}
	if !reflect.DeepEqual(counts, map[catalog.Kind]int{catalog.KindAgent: 3, catalog.KindMCPServer: 3, catalog.KindSkill: 3, catalog.KindPlugin: 3}) || !reflect.DeepEqual(namespaces, map[string]int{"catalog-demo-v1": 9, "ide-plugins": 3}) {
		t.Fatalf("bad allocation %v %v", counts, namespaces)
	}
	for _, path := range []string{"/../plugin.zip", "/repository-essentials.zip/", "/unknown.zip", "/repository-essentials", "//repository-essentials.zip"} {
		if _, _, ok := Archive(path); ok {
			t.Fatal("unexpected archive", path)
		}
	}
}

// Unknown keys must not silently select another sample by display name or kind.
func TestRepresentationRequiresKnownSampleKey(t *testing.T) {
	known := Samples()[0]
	for _, key := range []string{"", "not-defined"} {
		unknown := known
		unknown.Key = key
		if got := Representation(unknown, "https://127.0.0.1:18443"); !reflect.DeepEqual(got, catalog.RepresentationInput{}) {
			t.Fatalf("unknown sample key %q returned a representation", key)
		}
	}
}
