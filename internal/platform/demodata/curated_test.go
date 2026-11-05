package demodata

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/samples"
)

func TestCuratedCompletePackagesAndTruthfulProfiles(t *testing.T) {
	plan, err := Decode(samples.Files, "catalog.yaml", "curated", "https://127.0.0.1:18443")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[catalog.Kind]int{}
	for _, item := range plan.Resources {
		r := item.Resource
		if r.State != "published" {
			if r.Origin != "synthetic" {
				t.Fatal("unsupported workflow example")
			}
			continue
		}
		counts[r.Kind]++
		if len(r.Listing.Categories) != 2 {
			t.Fatalf("missing purpose categories: %s", r.Key)
		}
		if r.Provenance == nil || r.Origin == "synthetic" || r.Listing.DocumentationURL == "" {
			t.Fatalf("missing curation: %s", r.Key)
		}
		if r.Kind == catalog.KindMCPServer && r.Listing.License != "Apache-2.0 AND MIT" {
			t.Fatal("upstream mixed licensing lost")
		}
		if r.Kind == catalog.KindSkill {
			if item.Representation.Handoff == nil || item.Representation.Handoff.Git == nil || item.Representation.Handoff.Git.Commit != "8a1541c4a3ffa5a20a5a91de0dcf3f0bab1d1ef4" {
				t.Fatal("missing immutable upstream handoff")
			}
			archive, err := zip.NewReader(bytes.NewReader(item.Representation.PackageData), int64(len(item.Representation.PackageData)))
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, f := range archive.File {
				reader, e := f.Open()
				if e != nil {
					t.Fatal(e)
				}
				raw, e := io.ReadAll(reader)
				_ = reader.Close()
				if e != nil {
					t.Fatal(e)
				}
				source, e := fs.ReadFile(samples.Files, "upstream/skills/"+f.Name)
				if e != nil || !bytes.Equal(raw, source) {
					t.Fatal("package changed/missing upstream file: " + f.Name)
				}
				seen[f.Name] = true
			}
			if !seen[r.Key+"/LICENSE.txt"] || !seen[r.Key+"/SKILL.md"] {
				t.Fatal("missing package licence/root")
			}
			if r.Key == "webapp-testing" && (!seen[r.Key+"/scripts/with_server.py"] || len(seen) != 6) {
				t.Fatal("incomplete webapp-testing package")
			}
		}
		if r.Kind == catalog.KindPlugin {
			if r.Namespace != "ide-plugins" || r.Origin != "adapted" || !strings.Contains(r.Provenance.Attribution, "Local Catalog curator") {
				t.Fatal("misattributed local bundle")
			}
			result := adapter.InspectPlugin(context.Background(), catalog.PluginMediaZIP, item.Archive, catalog.DefaultDiagnosticLimits())
			for _, f := range result.Findings {
				if f.Severity == catalog.SeverityError {
					t.Fatalf("plugin %s: %+v", r.Key, f)
				}
			}
		}
		if r.Key == "filesystem" || r.Key == "memory" {
			var card map[string]any
			if json.Unmarshal(item.Representation.Data, &card) != nil || card["remotes"] != nil {
				t.Fatal("fabricated stdio remote")
			}
		}
	}
	want := map[catalog.Kind]int{catalog.KindAgent: 3, catalog.KindMCPServer: 3, catalog.KindSkill: 3, catalog.KindPlugin: 3}
	if !reflect.DeepEqual(counts, want) || len(plan.Resources) != 14 {
		t.Fatalf("curated inventory %v/%d", counts, len(plan.Resources))
	}
}

func TestCuratedUpstreamChecksums(t *testing.T) {
	raw, err := fs.ReadFile(samples.Files, "upstream/checksums.txt")
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(rows) != 10 {
		t.Fatalf("unexpected complete upstream inventory: %d", len(rows))
	}
	for _, row := range rows {
		parts := strings.SplitN(row, "  ", 2)
		if len(parts) != 2 {
			t.Fatal("checksum format")
		}
		expected, e := hex.DecodeString(parts[0])
		if e != nil || len(expected) != 32 {
			t.Fatal("checksum identity")
		}
		body, e := fs.ReadFile(samples.Files, parts[1])
		if e != nil || digest(body) != "sha256:"+parts[0] {
			t.Fatal("upstream byte drift: " + parts[1])
		}
	}
}

func TestCuratedProvenanceRejections(t *testing.T) {
	for _, mutate := range []func(*Resource){func(r *Resource) { r.Provenance = nil }, func(r *Resource) { r.Provenance.Source = "http://example.com" }, func(r *Resource) { r.Provenance.Revision = "" }, func(r *Resource) { r.Provenance.Attribution = "" }, func(r *Resource) { r.Provenance.Qualification = "trusted" }, func(r *Resource) { r.Provenance.Prerequisites = nil }, func(r *Resource) { r.Listing.DocumentationURL = "https://user:secret@example.com" }} {
		m, assets := manifestFixture(t)
		for i := range m.Resources {
			if m.Resources[i].Key == "hello-world" {
				mutate(&m.Resources[i])
				break
			}
		}
		if _, err := fixtureDecode(t, m, assets); err == nil {
			t.Fatal("invalid curation accepted in unselected definition")
		}
	}
}

func TestCuratedEverythingDistributionIsNotCard(t *testing.T) {
	raw, err := fs.ReadFile(samples.Files, "cards/everything.registry.json")
	if err != nil {
		t.Fatal(err)
	}
	result := adapter.NewRegistry().Validate(context.Background(), catalog.KindMCPServer, catalog.RepresentationInput{MediaType: adapter.MediaMCPRegistry, Role: catalog.RepresentationDistribution, Data: raw})
	if result.ProfileID != adapter.ProfileMCPRegistry {
		t.Fatal("Registry relabelled as Card")
	}
	for _, f := range result.Findings {
		if f.Severity == catalog.SeverityError {
			t.Fatal(f)
		}
	}
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil || document["$schema"] != nil || document["version"] != "2.0.0" {
		t.Fatal("distribution identity changed")
	}
}

func TestCuratedFirstPartySkillCompleteSnapshot(t *testing.T) {
	source := os.DirFS("../../../examples/client-skills/catalog-discovery")
	actual := map[string]bool{}
	err := fs.WalkDir(source, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		original, err := fs.ReadFile(source, path)
		if err != nil {
			return err
		}
		embedded, err := fs.ReadFile(samples.Files, "first-party/catalog-discovery/"+filepath.ToSlash(path))
		if err != nil || !bytes.Equal(original, embedded) {
			t.Fatalf("incomplete/stale first-party file: %s", path)
		}
		actual[path] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = fs.WalkDir(samples.Files, "first-party/catalog-discovery", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && !actual[strings.TrimPrefix(path, "first-party/catalog-discovery/")] {
			t.Fatalf("unexpected first-party file: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
