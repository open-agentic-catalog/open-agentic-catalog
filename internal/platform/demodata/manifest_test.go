package demodata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demoarchive"
	"github.com/open-agentic-catalog/open-agentic-catalog/samples"
	"go.yaml.in/yaml/v3"
)

func manifestFixture(t *testing.T) (Manifest, fstest.MapFS) {
	t.Helper()
	assets := fstest.MapFS{}
	if err := fs.WalkDir(samples.Files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, e := fs.ReadFile(samples.Files, name)
		if e != nil {
			return e
		}
		assets[name] = &fstest.MapFile{Data: raw}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var node yaml.Node
	if yaml.Unmarshal(assets["catalog.yaml"].Data, &node) != nil {
		t.Fatal("default YAML")
	}
	var value any
	if node.Decode(&value) != nil {
		t.Fatal("default node")
	}
	raw := manifestJSON(t, value)
	var m Manifest
	if json.Unmarshal(raw, &m) != nil {
		t.Fatal("default manifest")
	}
	for i := range m.Resources {
		r := &m.Resources[i].Representation
		if r.File != "" {
			body := string(assets[r.File].Data)
			r.Inline = &body
			r.File = ""
		}
	}
	return m, assets
}
func fixtureDecode(t *testing.T, m Manifest, assets fstest.MapFS) (Plan, error) {
	t.Helper()
	raw, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	assets["catalog.yaml"] = &fstest.MapFile{Data: raw}
	return Decode(assets, "catalog.yaml", "expanded", "https://127.0.0.1:18443")
}

// These frozen hashes/metadata were captured from N-88 before extraction, not computed by the new loader.
func TestN88ExtractionEquivalence(t *testing.T) {
	raw, e := os.ReadFile("testdata/n88-baseline.json")
	if e != nil {
		t.Fatal(e)
	}
	var baseline map[string]struct {
		BodyDigest            string       `json:"bodyDigest"`
		Name                  string       `json:"name"`
		Description           string       `json:"description"`
		Namespace             string       `json:"namespace"`
		Kind                  catalog.Kind `json:"kind"`
		Tags                  []string     `json:"tags"`
		RepresentativeQueries []string     `json:"representativeQueries"`
	}
	if json.Unmarshal(raw, &baseline) != nil {
		t.Fatal("baseline")
	}
	if len(baseline) != 12 {
		t.Fatal("incomplete original published sample baseline")
	}
	plan := DefaultPlan("expanded", "https://127.0.0.1:18443")
	seen := map[string]bool{}
	for _, item := range plan.Resources {
		r := item.Resource
		expected, ok := baseline[r.Key]
		if !ok {
			continue
		}
		seen[r.Key] = true
		identifierNamespace := "demo"
		if expected.Kind == catalog.KindPlugin {
			identifierNamespace = "ide-plugins"
		}
		if r.Identifier != "urn:air:demo.catalog.example.com:"+identifierNamespace+":"+r.Key || r.Version != "1.0.0" || r.State != "published" {
			t.Fatalf("N-88 identity or lifecycle drift: %s", r.Key)
		}
		if item.BodyDigest != expected.BodyDigest || r.Name != expected.Name || r.Description != expected.Description || r.Namespace != expected.Namespace || r.Kind != expected.Kind || !reflect.DeepEqual(r.Listing.Tags, expected.Tags) || !reflect.DeepEqual(r.Listing.RepresentativeQueries, expected.RepresentativeQueries) {
			t.Fatalf("N-88 drift: %s", r.Key)
		}
	}
	for key := range baseline {
		if !seen[key] {
			t.Fatalf("N-88 original sample missing: %s", key)
		}
	}
	minimal := DefaultPlan("minimal", "")
	keys := make([]string, 0, len(minimal.Resources))
	for _, item := range minimal.Resources {
		keys = append(keys, item.Resource.Key)
		if item.Resource.Identifier != "urn:air:demo.catalog.example.com:demo:"+item.Resource.Key || item.Resource.Version != "1.0.0" {
			t.Fatal("identity changed")
		}
	}
	if !reflect.DeepEqual(keys, []string{"order-agent", "order-mcp", "order-skill", "draft-agent", "pending-mcp"}) || minimal.Resources[3].Resource.State != "draft" || minimal.Resources[4].Resource.State != "pending_review" || minimal.Resources[0].BodyDigest != minimal.Resources[3].BodyDigest || minimal.Resources[1].BodyDigest != minimal.Resources[4].BodyDigest {
		t.Fatal("original lifecycle/body drift")
	}
}
func TestManifestSnapshotIdentity(t *testing.T) {
	m, assets := manifestFixture(t)
	inline, err := fixtureDecode(t, m, assets)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(*m.Resources[0].Representation.Inline)
	assets["cards/order.json"] = &fstest.MapFile{Data: raw}
	m.Resources[0].Representation.Inline = nil
	m.Resources[0].Representation.File = "cards/order.json"
	file, err := fixtureDecode(t, m, assets)
	if err != nil || file.Digest != inline.Digest || file.Resources[0].Digest != inline.Resources[0].Digest {
		t.Fatal("authoring form changed execution identity", err)
	}
	assets["cards/order.json"].Data = []byte(`{}`)
	if _, err = Decode(assets, "catalog.yaml", "expanded", "https://127.0.0.1:18443"); err == nil {
		t.Fatal("invalid edited asset accepted")
	}
	if !bytes.Equal(file.Resources[0].Representation.Data, raw) {
		t.Fatal("prepared plan reread mutable asset")
	}
	m, assets = manifestFixture(t)
	m.Resources[0], m.Resources[1] = m.Resources[1], m.Resources[0]
	m.Profiles["expanded"][0], m.Profiles["expanded"][1] = m.Profiles["expanded"][1], m.Profiles["expanded"][0]
	reordered, err := fixtureDecode(t, m, assets)
	if err != nil || reordered.Digest != inline.Digest {
		t.Fatal("ordering changed execution identity", err)
	}
	assets["catalog.yaml"].Data = append([]byte("# only a comment\n"), assets["catalog.yaml"].Data...)
	comment, err := Decode(assets, "catalog.yaml", "expanded", "https://127.0.0.1:18443")
	if err != nil || comment.Digest != inline.Digest {
		t.Fatal("comment changed identity", err)
	}
	other := DefaultPlan("expanded", "https://127.0.0.1:18444")
	if other.Digest == inline.Digest || other.Resources[len(other.Resources)-1].Digest == inline.Resources[len(inline.Resources)-1].Digest {
		t.Fatal("archive binding not captured")
	}
}

func TestManifestSelectsReferencedParentsAndPreservesProfileOrder(t *testing.T) {
	m, assets := manifestFixture(t)
	m.Resources = m.Resources[:2]
	m.Namespaces = []Namespace{{Key: "second", DisplayName: "Second namespace"}, {Key: "first", DisplayName: "First namespace"}, {Key: "unused", DisplayName: "Unused namespace"}}
	m.Publishers = []Publisher{
		{Key: "second", DisplayName: "Second publisher", Domains: []string{"second.example.com"}},
		{Key: "first", DisplayName: "First publisher", Domains: []string{"first.example.com"}},
		{Key: "unused", DisplayName: "Unused publisher", Domains: []string{"unused.example.com"}},
	}
	m.Resources[0].Namespace, m.Resources[0].Publisher = "first", "first"
	m.Resources[0].Identifier = "urn:air:first.example.com:demo:order-agent"
	m.Resources[1].Namespace, m.Resources[1].Publisher = "second", "second"
	m.Resources[1].Identifier = "urn:air:second.example.com:demo:documentation-agent"
	m.Profiles = map[string][]string{"expanded": {"documentation-agent", "order-agent"}, "single": {"order-agent"}}
	plan, err := fixtureDecode(t, m, assets)
	if err != nil {
		t.Fatal("two-publisher control", err)
	}
	if len(plan.Resources) != 2 {
		t.Fatalf("selected resource count = %d, want 2", len(plan.Resources))
	}
	if keys := []string{plan.Resources[0].Resource.Key, plan.Resources[1].Resource.Key}; !reflect.DeepEqual(keys, []string{"documentation-agent", "order-agent"}) {
		t.Fatalf("profile order or membership changed: %v", keys)
	}
	wantNamespaces := []Namespace{{Key: "second", DisplayName: "Second namespace"}, {Key: "first", DisplayName: "First namespace"}}
	wantPublishers := []Publisher{
		{Key: "second", DisplayName: "Second publisher", Domains: []string{"second.example.com"}},
		{Key: "first", DisplayName: "First publisher", Domains: []string{"first.example.com"}},
	}
	if !reflect.DeepEqual(plan.Namespaces, wantNamespaces) || !reflect.DeepEqual(plan.Publishers, wantPublishers) {
		t.Fatalf("referenced parent definitions changed: namespaces=%+v publishers=%+v", plan.Namespaces, plan.Publishers)
	}
	single, err := Decode(assets, "catalog.yaml", "single", "")
	if err != nil || len(single.Resources) != 1 || single.Resources[0].Resource.Key != "order-agent" || !reflect.DeepEqual(single.Namespaces, wantNamespaces[1:]) || !reflect.DeepEqual(single.Publishers, wantPublishers[1:]) {
		t.Fatalf("unselected resource parents included: %+v: %v", single, err)
	}
	// Authoring and profile order remain presentation choices, while the
	// execution digest binds the same selected definitions independently of order.
	m.Namespaces[0], m.Namespaces[1] = m.Namespaces[1], m.Namespaces[0]
	m.Publishers[0], m.Publishers[1] = m.Publishers[1], m.Publishers[0]
	m.Resources[0], m.Resources[1] = m.Resources[1], m.Resources[0]
	m.Profiles["expanded"] = []string{"order-agent", "documentation-agent"}
	reordered, err := fixtureDecode(t, m, assets)
	if err != nil || reordered.Digest != plan.Digest {
		t.Fatal("two-publisher authoring order changed execution identity", err)
	}
	if !reflect.DeepEqual(reordered.Namespaces, []Namespace{wantNamespaces[1], wantNamespaces[0]}) || !reflect.DeepEqual(reordered.Publishers, []Publisher{wantPublishers[1], wantPublishers[0]}) || reordered.Resources[0].Resource.Key != "order-agent" || reordered.Resources[1].Resource.Key != "documentation-agent" {
		t.Fatal("plan lost selected authoring order")
	}
}

func TestManifestStrictYAML(t *testing.T) {
	_, assets := manifestFixture(t)
	original := string(assets["catalog.yaml"].Data)
	cases := map[string]string{
		"duplicate": "format: 1\n" + original, "unknown": "unknown: value\n" + original, "wrong case": strings.Replace(original, "format:", "Format:", 1),
		"alias": strings.Replace(original, "dataset: catalog-demo", "dataset: &name catalog-demo\nunknown: *name", 1), "merge": "<<: {}\n" + original,
		"null": strings.Replace(original, "dataset: catalog-demo", "dataset: null", 1), "wrong scalar": strings.Replace(original, "dataset: catalog-demo", "dataset: 1", 1),
		"tag": strings.Replace(original, "dataset: catalog-demo", "dataset: !!str catalog-demo", 1), "multiple": original + "\n---\n{}", "malformed UTF8": original + string([]byte{0xff}),
		"depth": "format: " + strings.Repeat("[", 40) + "1" + strings.Repeat("]", 40), "empty": "", "too large": strings.Repeat(" ", MaxManifestBytes+1),
	}
	for name, raw := range cases {
		if raw == original {
			t.Fatalf("unsafe-input control did not alter manifest: %s", name)
		}
		t.Run(name, func(t *testing.T) {
			assets["catalog.yaml"] = &fstest.MapFile{Data: []byte(raw)}
			if _, err := Decode(assets, "catalog.yaml", "expanded", ""); err == nil {
				t.Fatal("unsafe YAML accepted")
			}
		})
	}
}
func TestManifestReferencesAndControls(t *testing.T) {
	m, assets := manifestFixture(t)
	if _, err := fixtureDecode(t, m, assets); err != nil {
		t.Fatal("valid admission control", err)
	}
	for name, mutate := range map[string]func(*Manifest){
		"empty revision":   func(m *Manifest) { m.Revision = " " },
		"empty resources":  func(m *Manifest) { m.Resources = []Resource{} },
		"empty namespaces": func(m *Manifest) { m.Namespaces = []Namespace{} },
		"empty publishers": func(m *Manifest) { m.Publishers = []Publisher{} },
		"empty profiles":   func(m *Manifest) { m.Profiles = map[string][]string{} },
		"invalid namespace key": func(m *Manifest) {
			previous := m.Namespaces[0].Key
			m.Namespaces[0].Key = "../namespace"
			for i := range m.Resources {
				if m.Resources[i].Namespace == previous {
					m.Resources[i].Namespace = "../namespace"
				}
			}
		},
		"empty namespace name": func(m *Manifest) { m.Namespaces[0].DisplayName = " " },
		"invalid publisher key": func(m *Manifest) {
			previous := m.Publishers[0].Key
			m.Publishers[0].Key = "../publisher"
			for i := range m.Resources {
				if m.Resources[i].Publisher == previous {
					m.Resources[i].Publisher = "../publisher"
				}
			}
		},
		"empty publisher name": func(m *Manifest) { m.Publishers[0].DisplayName = " " },
		"duplicate publisher key": func(m *Manifest) {
			p := m.Publishers[0]
			p.DisplayName = "Different display name"
			m.Publishers = append(m.Publishers, p)
		},
		"empty publisher domains": func(m *Manifest) { m.Publishers[0].Domains = []string{} },
		"unnormalized publisher domain": func(m *Manifest) {
			m.Publishers[0].Domains = append(m.Publishers[0].Domains, "DEMO.catalog.example.com")
		},
		"invalid profile key":        func(m *Manifest) { m.Profiles["../profile"] = []string{"order-agent"} },
		"empty unselected profile":   func(m *Manifest) { m.Profiles["unused"] = []string{} },
		"empty name":                 func(m *Manifest) { m.Resources[0].Name = " " },
		"empty description":          func(m *Manifest) { m.Resources[0].Description = " " },
		"empty version":              func(m *Manifest) { m.Resources[0].Version = " " },
		"empty license":              func(m *Manifest) { m.Resources[0].Listing.License = " " },
		"empty tag":                  func(m *Manifest) { m.Resources[0].Listing.Tags = []string{" "} },
		"empty category":             func(m *Manifest) { m.Resources[0].Listing.Categories = []string{" "} },
		"category NUL":               func(m *Manifest) { m.Resources[0].Listing.Categories = []string{"a\x00b"} },
		"category length":            func(m *Manifest) { m.Resources[0].Listing.Categories = []string{strings.Repeat("🙂", 81)} },
		"category count":             func(m *Manifest) { m.Resources[0].Listing.Categories = make([]string, 17) },
		"empty query":                func(m *Manifest) { m.Resources[0].Listing.RepresentativeQueries[0] = " " },
		"insecure documentation URL": func(m *Manifest) { m.Resources[0].Listing.DocumentationURL = "http://docs.example.com" },
		"missing body asset": func(m *Manifest) {
			m.Resources[0].Representation.Inline = nil
			m.Resources[0].Representation.File = "cards/missing.json"
		},
		"format": func(m *Manifest) { m.Format = 2 }, "dataset": func(m *Manifest) { m.Dataset = "../data" }, "duplicate resource": func(m *Manifest) { m.Resources = append(m.Resources, m.Resources[0]) },
		"namespace": func(m *Manifest) { m.Resources[0].Namespace = "missing" }, "publisher": func(m *Manifest) { m.Resources[0].Publisher = "missing" }, "duplicate namespace": func(m *Manifest) { m.Namespaces = append(m.Namespaces, m.Namespaces[0]) },
		"domain": func(m *Manifest) { m.Publishers[0].Domains = []string{"other.example.com"} }, "identifier": func(m *Manifest) { m.Resources[0].Identifier = "bad" }, "duplicate identifier": func(m *Manifest) { m.Resources[1].Identifier = m.Resources[0].Identifier },
		"missing profile entry": func(m *Manifest) { m.Profiles["unused"] = []string{"missing"} }, "duplicate profile entry": func(m *Manifest) { m.Profiles["unused"] = []string{"order-agent", "order-agent"} },
		"state": func(m *Manifest) { m.Resources[0].State = "execute" }, "origin": func(m *Manifest) { m.Resources[0].Origin = "trusted" }, "visibility": func(m *Manifest) { m.Resources[0].Listing.Visibility = "any" },
		"multiple bodies": func(m *Manifest) { m.Resources[0].Representation.File = "card.json" }, "missing body": func(m *Manifest) { m.Resources[0].Representation.Inline = nil }, "package and inline": func(m *Manifest) { m.Resources[0].Representation.PackageDirectory = "packages/repository-essentials" },
		"invalid native": func(m *Manifest) { v := `{}`; m.Resources[0].Representation.Inline = &v }, "unselected invalid": func(m *Manifest) {
			m.Profiles["expanded"] = []string{"order-agent"}
			v := `{}`
			m.Resources[1].Representation.Inline = &v
		},
		"unknown kind": func(m *Manifest) { m.Resources[0].Kind = "program" }, "asset bound": func(m *Manifest) {
			v := strings.Repeat("x", MaxAssetBytes+1)
			m.Resources[0].Representation.Inline = &v
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, assets := manifestFixture(t)
			mutate(&m)
			if plan, err := fixtureDecode(t, m, assets); err == nil || !reflect.DeepEqual(plan, Plan{}) {
				t.Fatal("invalid input accepted")
			}
		})
	}
	m, assets = manifestFixture(t)
	for _, origin := range []string{"https://public.example.com", "http://127.0.0.1", "https://user@127.0.0.1", "https://127.0.0.1?"} {
		raw := manifestJSON(t, m)
		assets["catalog.yaml"] = &fstest.MapFile{Data: raw}
		if _, err := Decode(assets, "catalog.yaml", "expanded", origin); err == nil {
			t.Fatal("unsafe fixture binding accepted")
		}
	}
	if _, err := Decode(assets, "catalog.yaml", "missing", ""); err == nil {
		t.Fatal("unknown selection accepted")
	}
}
func TestAssetAndPackageBoundaries(t *testing.T) {
	assets := fstest.MapFS{"ok": {Data: bytes.Repeat([]byte{'a'}, MaxAssetBytes)}, "link": {Data: []byte("ok"), Mode: fs.ModeSymlink}, "pipe": {Mode: fs.ModeNamedPipe}}
	if raw, err := readAsset(assets, "ok", MaxAssetBytes); err != nil || len(raw) != MaxAssetBytes {
		t.Fatal("exact asset bound", err)
	}
	for _, name := range []string{"../ok", "/ok", "a/../ok", "a\\ok", "a:ok", ".", "link", "pipe", "missing"} {
		if _, err := readAsset(assets, name, MaxAssetBytes); err == nil {
			t.Fatal("unsafe asset", name)
		}
	}
	if _, err := readAsset(assets, "ok", MaxAssetBytes-1); err == nil {
		t.Fatal("oversize asset")
	}
	for _, mode := range []fs.FileMode{fs.ModeSymlink, fs.ModeNamedPipe} {
		m, a := manifestFixture(t)
		a["packages/repository-essentials/extra"] = &fstest.MapFile{Mode: mode}
		if _, err := fixtureDecode(t, m, a); err == nil {
			t.Fatal("unsafe package member")
		}
	}
	files := map[string]Body{}
	v := "x"
	for i := range MaxFiles {
		files[strings.Repeat("a", i+1)] = Body{Inline: &v}
	}
	// Long names are valid within the fixed path-byte ceiling; exact file-count limit succeeds.
	if _, err := packageBytes(assets, RepresentationSource{Files: files}); err != nil {
		t.Fatal(err)
	}
	files["extra"] = Body{Inline: &v}
	if _, err := packageBytes(assets, RepresentationSource{Files: files}); err == nil {
		t.Fatal("file count exceeded")
	}
	m, a := manifestFixture(t)
	source := m.Resources[9].Representation
	source.Files = map[string]Body{}
	source.PackageDirectory = ""
	if _, err := packageBytes(a, source); err == nil {
		t.Fatal("empty package")
	}
}
func TestPreparedPublicationIsAtomicAndImmutable(t *testing.T) {
	plan := DefaultPlan("expanded", "https://127.0.0.1:18443")
	dir := filepath.Join(t.TempDir(), "prepared")
	if err := Prepare(dir, plan); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, AllowlistFilename)) // #nosec G304 -- owned private test preparation directory.
	if err != nil {
		t.Fatal(err)
	}
	if err := Prepare(dir, plan); err != nil {
		t.Fatal("idempotent preparation", err)
	}
	for _, fault := range []string{"changed", "bad bytes"} {
		t.Run(fault, func(t *testing.T) {
			changed := DefaultPlan("expanded", "https://127.0.0.1:18443")
			switch fault {
			case "removed":
				changed.Resources = changed.Resources[:len(changed.Resources)-1]
			case "changed":
				changed.Resources[len(changed.Resources)-1].BodyDigest = "sha256:" + strings.Repeat("a", 64)
			case "bad bytes":
				changed.Resources[len(changed.Resources)-1].Archive = []byte("changed")
			}
			if Prepare(dir, changed) == nil {
				t.Fatal("unsafe replacement accepted")
			}
			after, _ := os.ReadFile(filepath.Join(dir, AllowlistFilename)) // #nosec G304 -- owned private test preparation directory.
			if !bytes.Equal(before, after) {
				t.Fatal("failed prepare changed publication")
			}
		})
	}
	omitted := DefaultPlan("expanded", "https://127.0.0.1:18443")
	omitted.Resources = omitted.Resources[:len(omitted.Resources)-1]
	if err := Prepare(dir, omitted); err != nil {
		t.Fatal("retained archive omitted from profile", err)
	}
	afterOmission, _ := os.ReadFile(filepath.Join(dir, AllowlistFilename)) // #nosec G304 -- only the prepared allowlist in this unit's private temporary directory.
	if !bytes.Equal(before, afterOmission) {
		t.Fatal("omitted immutable archive was removed")
	}
	// A crashed, unreferenced staging file is not served or admitted.
	if err := os.WriteFile(filepath.Join(dir, "objects", ".prepare-interrupted"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := ReadPrepared(dir)
	if err != nil || len(read) != 3 {
		t.Fatal("prepared read", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "prepare.lock"), os.O_RDWR, 0600) // #nosec G304 -- owned private test preparation lock.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		t.Fatal("lock")
	}
	if Prepare(dir, plan) == nil {
		t.Fatal("concurrent prepare accepted")
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	raw := plan.Resources[len(plan.Resources)-1]
	if err := os.WriteFile(filepath.Join(dir, objectName(raw.BodyDigest)), []byte("tamper"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrepared(dir); err == nil {
		t.Fatal("tampered bytes served")
	}
	if Prepare(dir, plan) == nil {
		t.Fatal("invalid prior publication silently repaired")
	}
}
func TestFilesystemSelectionRejectsLinks(t *testing.T) {
	dir := t.TempDir()
	m, _ := manifestFixture(t)
	raw := manifestJSON(t, m)
	if err := os.WriteFile(filepath.Join(dir, "catalog.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	m.Resources = m.Resources[:1]
	m.Profiles = map[string][]string{"expanded": {"order-agent"}}
	m.Resources[0].Representation.File = "card.json"
	m.Resources[0].Representation.Inline = nil
	raw = manifestJSON(t, m)
	if err := os.WriteFile(filepath.Join(dir, "catalog.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "card.json")
	if err := os.WriteFile(outside, DefaultPlan("minimal", "").Resources[0].Representation.Data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "card.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(dir, "catalog.yaml"), "expanded", ""); err == nil {
		t.Fatal("file link accepted")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(alias, "catalog.yaml"), "expanded", ""); err == nil {
		t.Fatal("manifest directory link accepted")
	}
}
func TestArchiveAllowlistBinding(t *testing.T) {
	a := DefaultPlan("expanded", "").Allowlist()
	raw := manifestJSON(t, a)
	decoded, err := demoarchive.Decode(raw)
	if err != nil || decoded.Digest() != a.Digest() {
		t.Fatal(err)
	}
	for _, mutate := range []func(*demoarchive.Allowlist){func(a *demoarchive.Allowlist) { a.TenantID = "other" }, func(a *demoarchive.Allowlist) { a.Entries[0].Path = "/../secret.zip" }, func(a *demoarchive.Allowlist) { a.Entries[0].Digest = "sha256:bad" }, func(a *demoarchive.Allowlist) { a.Entries[0].Size = demoarchive.MaxArchiveBytes + 1 }, func(a *demoarchive.Allowlist) { a.Entries = append(a.Entries, a.Entries[0]) }} {
		bad := DefaultPlan("expanded", "").Allowlist()
		mutate(&bad)
		encoded := manifestJSON(t, bad)
		if _, err := demoarchive.Decode(encoded); err == nil {
			t.Fatal("unsafe allowlist accepted")
		}
	}
}

func TestSkillPackageAndEncodedRequestLimit(t *testing.T) {
	m, assets := manifestFixture(t)
	r := m.Resources[6] // original standalone Skill
	if r.Kind != catalog.KindSkill {
		t.Fatal("fixture kind")
	}
	content := *r.Representation.Inline
	r.Representation = RepresentationSource{MediaType: catalog.SkillPackageMediaZIP, Files: map[string]Body{"triage-order-exception/SKILL.md": {Inline: &content}}, Handoff: &catalog.SkillHandoff{Git: &catalog.GitHandoffReference{Repository: "https://git.example.com/skills/catalog.git", Commit: strings.Repeat("a", 40), Directory: "skills/triage-order-exception"}}}
	item, err := prepareResource(assets, r, "")
	if err != nil || len(item.Representation.PackageData) == 0 {
		t.Fatal("complete Skill with immutable handoff", err)
	}
	r.Representation.Handoff = nil
	if _, err := prepareResource(assets, r, ""); err == nil {
		t.Fatal("archive accepted without handoff")
	}
	r.Representation.Handoff = item.Representation.Handoff
	// Native bytes fit; base64+JSON exceeds the actual default 2 MiB request ceiling.
	large := strings.Repeat("x", 800<<10)
	r.Representation.Files["triage-order-exception/first.txt"] = Body{Inline: &large}
	r.Representation.Files["triage-order-exception/second.txt"] = Body{Inline: &large}
	if _, err := prepareResource(assets, r, ""); err == nil {
		t.Fatal("encoded request budget not enforced")
	}
	// A native directory acquires exactly one Skill root rather than a rootless archive.
	r.Representation.Files = nil
	r.Representation.PackageDirectory = "native/triage-order-exception"
	assets["native/triage-order-exception/SKILL.md"] = &fstest.MapFile{Data: []byte(content)}
	if _, err := prepareResource(assets, r, ""); err != nil {
		t.Fatal("native Skill directory root", err)
	}
}
func TestPreviewIsLocalAndBounded(t *testing.T) {
	plan := DefaultPlan("minimal", "")
	dir := t.TempDir()
	filename := filepath.Join(dir, "receipt.json")
	preview, err := PreviewReceipt(plan, filename)
	if err != nil || preview.Items[0].Disposition != "candidate addition" {
		t.Fatal("fresh preview", err)
	}
	missingParent := filepath.Join(dir, "missing", "receipt.json")
	withoutParent, err := PreviewReceipt(plan, missingParent)
	if err != nil || !reflect.DeepEqual(withoutParent, preview) {
		t.Fatal("missing parent changed fresh preview", err)
	}
	if _, err := os.Stat(filepath.Dir(missingParent)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview created the missing receipt directory")
	}
	raw := []byte(`{"tenantId":"00000000-0000-7000-8000-000000000001","dataset":"catalog-demo-v1","items":[{"key":"order-agent"},{"key":"other-retained"}]}`)
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	preview, err = PreviewReceipt(plan, filename)
	if err != nil || preview.Items[0].Disposition != "receipt key; state/content unverified" || preview.Items[len(preview.Items)-1].Key != "other-retained" {
		t.Fatal("receipt key preview", err)
	}
	encoded, err := EncodePreview(plan, filename)
	if err != nil || bytes.Contains(encoded, plan.Resources[0].Representation.Data) || !bytes.Contains(encoded, []byte("unverified")) {
		t.Fatal("native contents or false authority in preview", err)
	}
	for _, raw := range []string{`{"tenantId":"other","items":[]}`, `{"tenantId":"` + TenantID + `","items":[{"key":"duplicate"},{"key":"duplicate"}]}`, `{"tenantId":"` + TenantID + `","items":null}`, `{"unknown":true}`, strings.Repeat("x", (32<<10)+1)} {
		if err := os.WriteFile(filename, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := PreviewReceipt(plan, filename); err == nil {
			t.Fatal("malformed receipt used")
		}
	}
}

func TestPreviewReceiptBoundsAndFailurePropagation(t *testing.T) {
	plan := DefaultPlan("minimal", "")
	directory := t.TempDir()
	filename := filepath.Join(directory, "receipt.json")
	for _, count := range []int{MaxResources, MaxResources + 1} {
		items := make([]map[string]string, 0, count)
		for i := range count {
			items = append(items, map[string]string{"key": fmt.Sprintf("retained-%03d", i)})
		}
		raw := manifestJSON(t, map[string]any{"tenantId": TenantID, "items": items})
		if err := os.WriteFile(filename, raw, 0600); err != nil {
			t.Fatal(err)
		}
		preview, err := PreviewReceipt(plan, filename)
		if count == MaxResources {
			if err != nil || len(preview.Items) != len(plan.Resources)+count {
				t.Fatal("exact receipt count was truncated or rejected", err)
			}
			for i, item := range preview.Items[len(plan.Resources):] {
				if item.Key != fmt.Sprintf("retained-%03d", i) || item.Disposition != "receipt key outside selected profile; retained, not deleted" {
					t.Fatalf("omitted receipt item lost retention or ordering: %+v", item)
				}
			}
		} else if err == nil {
			t.Fatal("oversized receipt inventory accepted")
		}
	}
	// A syntactically valid receipt at the byte ceiling isolates the transport bound.
	raw := []byte(`{"tenantId":"` + TenantID + `","items":[]}`)
	raw = append(raw, bytes.Repeat([]byte(" "), (128<<10)-len(raw))...)
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewReceipt(plan, filename); err != nil {
		t.Fatal("exact receipt byte budget", err)
	}
	if err := os.WriteFile(filename, append(raw, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if encoded, err := EncodePreview(plan, filename); err == nil || encoded != nil {
		t.Fatal("oversized receipt produced a usable preview")
	}
	prepared := filepath.Join(directory, "prepared")
	if err := PrepareWithReceipt(prepared, plan, filename, ""); err == nil {
		t.Fatal("invalid receipt allowed preparation")
	}
	if _, err := os.Stat(prepared); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid receipt created preparation output")
	}
	// A link to an otherwise valid receipt remains outside the asset boundary.
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(directory, "alias.json")
	if err := os.Symlink(filename, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewReceipt(plan, alias); err == nil {
		t.Fatal("linked receipt accepted")
	}
}

func manifestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestManifestCollectionAndTransportBoundaries(t *testing.T) {
	m, assets := manifestFixture(t)
	r := m.Resources[0]
	m.Resources = nil
	m.Profiles = map[string][]string{"expanded": {}}
	for i := range MaxResources {
		item := r
		item.Key = fmt.Sprintf("example-%d", i)
		item.Identifier = "urn:air:demo.catalog.example.com:demo:" + item.Key
		m.Resources = append(m.Resources, item)
		m.Profiles["expanded"] = append(m.Profiles["expanded"], item.Key)
	}
	if _, err := fixtureDecode(t, m, assets); err != nil {
		t.Fatal("exact resource count", err)
	}
	m.Resources = append(m.Resources, r)
	if _, err := fixtureDecode(t, m, assets); err == nil {
		t.Fatal("resource count exceeded")
	}
	_, assets = manifestFixture(t)
	raw := assets["catalog.yaml"].Data
	raw = append(raw, bytes.Repeat([]byte(" "), MaxManifestBytes-len(raw))...)
	assets["catalog.yaml"] = &fstest.MapFile{Data: raw}
	if _, err := Decode(assets, "catalog.yaml", "minimal", ""); err != nil {
		t.Fatal("exact YAML byte budget", err)
	}
	raw = append(raw, ' ')
	assets["catalog.yaml"].Data = raw
	if _, err := Decode(assets, "catalog.yaml", "minimal", ""); err == nil {
		t.Fatal("manifest transport exceeded")
	}
}

func TestPreparationRejectsAncestorLinksBeforeWrites(t *testing.T) {
	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(alias, "new-directory")
	if Prepare(selected, DefaultPlan("expanded", "")) == nil {
		t.Fatal("linked preparation ancestor accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "new-directory")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe selection created output before rejection")
	}
	dir := filepath.Join(t.TempDir(), "prepared")
	if err := Prepare(dir, DefaultPlan("expanded", "")); err != nil {
		t.Fatal(err)
	}
	alias = filepath.Join(t.TempDir(), "prepared-alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrepared(alias); err == nil {
		t.Fatal("linked prepared root accepted")
	}
}

func TestPrivateDirectoryRefusesInvalidSelections(t *testing.T) {
	if PrepareDirectory("") == nil || PrepareDirectory("invalid\x00path") == nil {
		t.Fatal("invalid directory accepted")
	}
	path := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(path, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if PrepareDirectory(path) == nil {
		t.Fatal("file selected as private directory")
	}
	directory := filepath.Join(t.TempDir(), "public")
	// #nosec G301 -- deliberately non-private owned test directory must be refused.
	if err := os.Mkdir(directory, 0755); err != nil {
		t.Fatal(err)
	}
	// Mkdir applies the process umask; set the unsafe fixture mode explicitly.
	// #nosec G302 -- deliberately public owned test directory must be refused.
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if PrepareDirectory(directory) == nil {
		t.Fatal("public preparation directory accepted")
	}
}

func TestPublicSamplesRequireRepresentativeQueries(t *testing.T) {
	for _, count := range []int{0, 1, 2, 5, 6} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			m, assets := manifestFixture(t)
			m.Resources[0].Listing.RepresentativeQueries = []string{}
			for i := 0; i < count; i++ {
				m.Resources[0].Listing.RepresentativeQueries = append(m.Resources[0].Listing.RepresentativeQueries, fmt.Sprintf("Find useful sample query %d", i))
			}
			_, err := fixtureDecode(t, m, assets)
			if (err == nil) != (count >= 2 && count <= 5) {
				t.Fatal("wrong published query bound", count, err)
			}
			m.Resources[0].State = "draft"
			if _, err = fixtureDecode(t, m, assets); err != nil {
				t.Fatal("draft incorrectly constrained", err)
			}
		})
	}
}

func TestOriginalReceiptPreparesOmittedArchivePaths(t *testing.T) {
	origin := "https://127.0.0.1:18443"
	plan, err := Load("", "curated", origin)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "receipt.json")
	raw := []byte(`{"dataset":"catalog-demo-v2","tenantId":"00000000-0000-7000-8000-000000000001","items":[{"key":"repository-essentials"},{"key":"documentation-toolkit"},{"key":"release-workflow"}]}`)
	if err = os.WriteFile(state, raw, 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "tls-only-directory")
	if err = PrepareWithReceipt(dir, plan, state, origin); err != nil {
		t.Fatal(err)
	}
	files, err := ReadPrepared(dir)
	if err != nil || len(files) != 6 {
		t.Fatal("old URLs not materialized", err, len(files))
	}
	for _, original := range DefaultPlan("expanded", origin).Resources {
		if len(original.Archive) > 0 && !bytes.Equal(files["/"+original.Resource.Key+".zip"], original.Archive) {
			t.Fatal("original archive bytes changed")
		}
	}
	if len(plan.Resources) != 14 {
		t.Fatal("archive preparation mutated the selected API plan")
	}
	for _, fault := range []string{"duplicate publisher name", "duplicate publisher domain"} {
		m, assets := manifestFixture(t)
		if fault == "duplicate publisher name" {
			p := m.Publishers[0]
			p.Key = "other-publisher"
			m.Publishers = append(m.Publishers, p)
		} else {
			m.Publishers[0].Domains = append(m.Publishers[0].Domains, m.Publishers[0].Domains[0])
		}
		if _, err = fixtureDecode(t, m, assets); err == nil {
			t.Fatal("ambiguous parent accepted", fault)
		}
	}
}
