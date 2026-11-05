package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"github.com/open-agentic-catalog/open-agentic-catalog/samples"
)

const importTestYAML = `format: 1
resources:
  - key: sample-agent
    namespaceId: 00000000-0000-7000-8000-000000000010
    canonicalIdentifier: custom:sample-agent
    kind: agent
    version: "release candidate"
    listing:
      publisherId: 00000000-0000-7000-8000-000000000020
      displayName: Example agent
      description: Helps with local examples.
      visibility: private
    representations:
      - role: primary
        mediaType: application/vnd.open-agentic-catalog.agent+json
        dataFile: agent.json
`

func writeImportTestFile(t *testing.T, dir, name string, raw []byte, mode os.FileMode) string {
	t.Helper()
	filename := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, raw, mode); err != nil {
		t.Fatal(err)
	}
	return filename
}

func importTestInput(t *testing.T) (string, importManifest) {
	t.Helper()
	dir := t.TempDir()
	writeImportTestFile(t, dir, "agent.json", []byte(`{"name":"Private example","framework":"custom","unknown":{"integer":9007199254740993}}`), 0600)
	manifest, err := decodeImportManifest([]byte(importTestYAML))
	if err != nil {
		t.Fatal(err)
	}
	return dir, manifest
}

func prepareImportTest(t *testing.T, dir string, manifest importManifest, limit int) (importPlan, error) {
	t.Helper()
	return prepareImportTestContext(t.Context(), t, dir, manifest, limit)
}

func prepareImportTestContext(ctx context.Context, t *testing.T, dir string, manifest importManifest, limit int) (importPlan, error) {
	t.Helper()
	raw, err := json.Marshal(manifest) // JSON is also admitted YAML.
	if err != nil {
		t.Fatal(err)
	}
	filename := writeImportTestFile(t, dir, "import.yaml", raw, 0600)
	batch, err := prepareImportFile(ctx, filename, limit)
	if err != nil {
		return importPlan{}, err
	}
	return batch.Items[0], nil
}

func TestImportYAMLStrictControlsAndExactExtensions(t *testing.T) {
	raw := strings.Replace(importTestYAML, "      visibility: private", `      visibility: private
      extensions:
        integer: 9007199254740993
        decimal: 0.12345678901234567890123456789
        exponent: 1e999
        flag: true
        nothing: null
        items: ["αβ", false]`, 1)
	manifest, err := decodeImportManifest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	extensions, err := importJSONObject(manifest.Resources[0].Listing.Extensions)
	if err != nil || extensions["exponent"] != json.Number("1e999") || extensions["integer"] != json.Number("9007199254740993") {
		t.Fatalf("numeric extensions changed type or value: %#v %v", extensions, err)
	}
	for _, value := range []string{"9007199254740993", "0.12345678901234567890123456789", "1e999", `"nothing":null`, `"flag":true`, `"αβ"`} {
		if !bytes.Contains(manifest.Resources[0].Listing.Extensions, []byte(value)) {
			t.Fatalf("extension value changed: %s", value)
		}
	}
	invalid := map[string]string{
		"unknown":            strings.Replace(importTestYAML, "format: 1", "format: 1\nunknown: x", 1),
		"case":               strings.Replace(importTestYAML, "namespaceId:", "NamespaceId:", 1),
		"duplicate":          strings.Replace(importTestYAML, "format: 1", "format: 1\nformat: 1", 1),
		"alias":              strings.Replace(importTestYAML, "format: 1", "format: &format 1", 1),
		"alias use":          strings.Replace(importTestYAML, "format: 1", "format: *missing", 1),
		"merge":              strings.Replace(importTestYAML, "      visibility: private", "      visibility: private\n      <<: {}", 1),
		"tag":                strings.Replace(importTestYAML, "format: 1", "format: !!int 1", 1),
		"null":               strings.Replace(importTestYAML, "      visibility: private", "      visibility: null", 1),
		"wrong type":         strings.Replace(importTestYAML, `version: "release candidate"`, "version: 1", 1),
		"trailing document":  importTestYAML + "---\nformat: 1\n",
		"trailing invalid":   importTestYAML + "---\n[invalid\n",
		"invalid UTF8":       importTestYAML + "\xff",
		"oversize":           strings.Repeat(" ", importMaxManifestBytes+1),
		"wrong format":       strings.Replace(importTestYAML, "format: 1", "format: 2", 1),
		"no resources":       "format: 1\nresources: []\n",
		"too many resources": "format: 1\nresources: [" + strings.Repeat("{},", importMaxResources) + "{}]\n",
		"depth":              "format: 1\nresources: " + strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34),
		"nodes":              "format: 1\nresources: [" + strings.Repeat("0,", 20001) + "]",
		"non JSON number":    strings.Replace(raw, "integer: 9007199254740993", "integer: 0x10", 1),
		"infinite number":    strings.Replace(raw, "decimal: 0.12345678901234567890123456789", "decimal: .inf", 1),
		"numeric key":        strings.Replace(raw, "integer:", "1:", 1),
		"timestamp":          strings.Replace(importTestYAML, `version: "release candidate"`, "version: 2026-10-10", 1),
		"unknown nested":     strings.Replace(importTestYAML, "      visibility: private", "      visibility: private\n      versionRevisionId: forbidden", 1),
		"empty":              "",
	}
	for name, input := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeImportManifest([]byte(input)); err == nil {
				t.Fatal("unsafe or unsupported manifest accepted")
			}
		})
	}
}

func TestImportPreviewRequestShapesAndLosslessValues(t *testing.T) {
	dir, manifest := importTestInput(t)
	listing := &manifest.Resources[0].Listing
	listing.DisplayName = strings.Repeat("界", 160)
	listing.Categories = []string{"Examples"}
	listing.Tags = []string{"Local"}
	listing.RepresentativeQueries = []string{"find useful examples"}
	listing.DocumentationURL, listing.SupportURL, listing.TermsURL = "https://example.com/docs", "https://example.com/support", "https://example.com/terms"
	listing.License = "Apache-2.0"
	listing.Relationships = []catalog.Relationship{{Type: "recommended_with", TargetIdentifier: "custom:other", TargetVersion: "next"}}
	listing.ExtensionsFile = "extensions.json"
	extensions := []byte(`{"exact":9007199254740993,"small":0.1234567890123456789,"big":1e999,"nullable":null,"artwork":{"icon":{"url":"https://media.example.com/icon.png"}}}`)
	writeImportTestFile(t, dir, listing.ExtensionsFile, extensions, 0600)
	plan, err := prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || !plan.Preview.LocallyValid {
		t.Fatalf("preview: %v, %#v", err, plan.Preview.Findings)
	}
	if plan.Preview.ServerLimitsChecked || len(plan.Preview.Commands) != 4 || len(plan.Bodies) != 4 || len(plan.Preview.PendingServerChecks) == 0 {
		t.Fatal("preview falsely qualifies server checks or loses commands")
	}
	resource, _ := importJSONObject(plan.Bodies[0])
	if resource["canonicalIdentifier"] != "custom:sample-agent" || resource["namespaceId"] != manifest.Resources[0].NamespaceID {
		t.Fatal("preview changes author identity or requires AIR")
	}
	version, _ := importJSONObject(plan.Bodies[1])
	representations, ok := version["representations"].([]any)
	if !ok || len(representations) != 1 {
		t.Fatal("wrong representation collection")
	}
	representation, ok := representations[0].(map[string]any)
	if !ok {
		t.Fatal("wrong representation object")
	}
	data, ok := representation["data"].(map[string]any)
	if !ok {
		t.Fatal("native JSON object lost")
	}
	unknown, ok := data["unknown"].(map[string]any)
	if !ok {
		t.Fatal("unknown native object lost")
	}
	integer, ok := unknown["integer"].(json.Number)
	if !ok || integer.String() != "9007199254740993" || version["version"] != "release candidate" {
		t.Fatal("native unknown fields or version labels changed")
	}
	metadata, _ := importJSONObject(plan.Bodies[3])
	for field, want := range map[string]any{
		"publisherId": manifest.Resources[0].Listing.PublisherID,
		"displayName": strings.Repeat("界", 160),
		"description": manifest.Resources[0].Listing.Description,
		"visibility":  "private",
		"tags":        []any{"Local"}, "categories": []any{"Examples"},
		"representativeQueries": []any{"find useful examples"},
		"documentationUrl":      "https://example.com/docs", "supportUrl": "https://example.com/support", "termsUrl": "https://example.com/terms",
		"license":       "Apache-2.0",
		"relationships": []any{map[string]any{"type": "recommended_with", "targetIdentifier": "custom:other", "targetVersion": "next"}},
	} {
		if !reflect.DeepEqual(metadata[field], want) {
			t.Fatalf("listing field %s changed: got %#v, want %#v", field, metadata[field], want)
		}
	}
	retainedExtensions, ok := metadata["extensions"].(map[string]any)
	if !ok {
		t.Fatal("extension object lost")
	}
	exact, ok := retainedExtensions["exact"].(json.Number)
	if !ok || exact.String() != "9007199254740993" || metadata["license"] != "Apache-2.0" {
		t.Fatal("listing metadata lost")
	}
	relationships, ok := metadata["relationships"].([]any)
	if !ok || len(relationships) != 1 {
		t.Fatal("listing relationships lost")
	}
	if plan.Preview.Assets[1].Digest != importDigest(extensions) || plan.Preview.Assets[1].Scope != "file-bytes" {
		t.Fatal("extension checksum scope changed")
	}
	total := 0
	largest := 0
	for index, command := range plan.Preview.Commands {
		total += len(plan.Bodies[index])
		largest = max(largest, len(plan.Bodies[index]))
		if command.Method != http.MethodPost || command.BodyBytes != len(plan.Bodies[index]) || command.BodyDigest != importDigest(plan.Bodies[index]) {
			t.Fatal("request byte/digest accounting is wrong")
		}
	}
	if total != plan.Preview.PreparedBytes || plan.Preview.Commands[3].IfMatch == "" || !strings.HasPrefix(plan.Preview.Commands[3].DigestScope, "request-template-body") {
		t.Fatal("derived bindings, If-Match or total accounting lost")
	}
	if _, err = prepareImportTest(t, dir, manifest, len(plan.Bodies[1])-1); err == nil {
		t.Fatal("encoded request limit ignored")
	}
	if _, err = prepareImportTest(t, dir, manifest, largest); err != nil {
		t.Fatalf("exact request byte limit rejected: %v", err)
	}
	if _, err = prepareImportTest(t, dir, manifest, largest-1); err == nil {
		t.Fatal("listing request bypasses the selected byte limit")
	}
}

func TestImportPreviewNativeFindingsAndMCPRoles(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*importResource, string)
		want   string
	}{
		{"invalid private native", func(r *importResource, dir string) {
			writeImportTestFile(t, dir, "agent.json", []byte(`{"name":"SENSITIVE_SENTINEL"}`), 0600)
		}, "PRIVATE_AGENT_FIELDS_REQUIRED"},
		{"no primary", func(r *importResource, _ string) { r.Representations[0].Role = catalog.RepresentationMetadata }, "PRIMARY_REPRESENTATION_COUNT"},
		{"two primary", func(r *importResource, _ string) { r.Representations = append(r.Representations, r.Representations[0]) }, "PRIMARY_REPRESENTATION_COUNT"},
		{"unsupported declared media", func(r *importResource, _ string) { r.Representations[0].MediaType = adapter.MediaPrivateMCP }, "AGENT_MEDIA_TYPE_UNSUPPORTED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, manifest := importTestInput(t)
			test.mutate(&manifest.Resources[0], dir)
			plan, err := prepareImportTest(t, dir, manifest, 2<<20)
			if err != nil || plan.Preview.LocallyValid || !hasImportFinding(plan, test.want) {
				t.Fatalf("expected %s: %v %#v", test.want, err, plan.Preview.Findings)
			}
			encoded, _ := json.Marshal(plan.Preview)
			if bytes.Contains(encoded, []byte("SENSITIVE_SENTINEL")) {
				t.Fatal("preview disclosed native content")
			}
		})
	}
	dir, manifest := importTestInput(t)
	resource := &manifest.Resources[0]
	resource.Kind, resource.Version = catalog.KindMCPServer, "2.0.0"
	for _, file := range []string{"everything.json", "everything.registry.json"} {
		raw, err := samples.Files.ReadFile("cards/" + file)
		if err != nil {
			t.Fatal(err)
		}
		writeImportTestFile(t, dir, file, raw, 0600)
	}
	resource.Representations = []importRepresentation{
		{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaMCPServerCard, DataFile: "everything.json"},
		{Role: catalog.RepresentationDistribution, MediaType: adapter.MediaMCPRegistry, DataFile: "everything.registry.json"},
	}
	plan, err := prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || !plan.Preview.LocallyValid || !hasImportFinding(plan, "MCP_SERVER_CARD_EXPERIMENTAL") {
		t.Fatalf("MCP pair failed: %v %#v", err, plan.Preview.Findings)
	}
	resource.Version = "different"
	plan, err = prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || plan.Preview.LocallyValid || !hasImportFinding(plan, "NATIVE_VERSION_MISMATCH") {
		t.Fatalf("native version mismatch lost: %v", err)
	}
	resource.Version = "2.0.0"
	resource.Representations = resource.Representations[1:]
	resource.Representations[0].Role = catalog.RepresentationPrimary
	plan, err = prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || !plan.Preview.LocallyValid || !hasImportFinding(plan, "MCP_REGISTRY_NOT_SERVER_CARD") {
		t.Fatalf("private Registry draft rejected: %v %#v", err, plan.Preview.Findings)
	}
}

func hasImportFinding(plan importPlan, code string) bool {
	for _, finding := range plan.Preview.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func TestImportSkillDirectoryPreservesInventoryAndDoesNotExecute(t *testing.T) {
	dir, manifest := importTestInput(t)
	resource := &manifest.Resources[0]
	resource.Kind = catalog.KindSkill
	handoff := &catalog.SkillHandoff{Git: &catalog.GitHandoffReference{Repository: "https://example.com/skills.git", Commit: strings.Repeat("a", 40), Directory: "skills/safe-skill"}}
	resource.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, PackageDirectory: "safe-skill", Handoff: handoff}}
	markdown := "---\nname: safe-skill\ndescription: Helps with local examples\n---\n# Instructions\nKeep this exact.\n"
	writeImportTestFile(t, dir, "safe-skill/SKILL.md", []byte(markdown), 0600)
	writeImportTestFile(t, dir, "safe-skill/references/example.txt", []byte("supporting evidence\n"), 0600)
	sentinel := filepath.Join(dir, "must-not-exist")
	writeImportTestFile(t, dir, "safe-skill/scripts/run.sh", []byte("#!/bin/sh\ntouch '"+sentinel+"'\n"), 0700)
	plan, err := prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || !plan.Preview.LocallyValid || plan.Preview.Representations[0].PackageFiles != 3 || plan.Preview.Representations[0].ExecutableFiles != 1 {
		t.Fatalf("complete directory: %v %#v", err, plan.Preview)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("package was executed")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	first, err := importSkillDirectory(context.Background(), root, "safe-skill")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "safe-skill/SKILL.md"), time.Unix(99, 0), time.Unix(99, 0)); err != nil {
		t.Fatal(err)
	}
	second, err := importSkillDirectory(context.Background(), root, "safe-skill")
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("archive depends on filesystem timestamps")
	}
	archive, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{}
	for _, file := range archive.File {
		if file.FileInfo().IsDir() {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		contents[file.Name] = string(raw)
		if strings.HasSuffix(file.Name, "run.sh") && file.Mode()&0111 == 0 {
			t.Fatal("executable evidence lost")
		}
	}
	if contents["safe-skill/SKILL.md"] != markdown || contents["safe-skill/references/example.txt"] != "supporting evidence\n" {
		t.Fatal("archive lost exact supporting bytes")
	}
	writeImportTestFile(t, dir, "skill.zip", first, 0600)
	resource.Representations[0].PackageDirectory, resource.Representations[0].PackageFile = "", "skill.zip"
	plan, err = prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || plan.Preview.Assets[0].Digest != importDigest(first) {
		t.Fatal("existing ZIP changed")
	}
	var version catalog.CreateVersionInput
	if err := json.Unmarshal(plan.Bodies[1], &version); err != nil || !bytes.Equal(version.Representations[0].PackageData, first) {
		t.Fatalf("ZIP bytes changed: %v", err)
	}
	if _, err := prepareImportTest(t, dir, manifest, len(first)+100); err == nil {
		t.Fatal("base64 expansion did not count against request limit")
	}
	resource.Representations[0] = importRepresentation{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillMarkdown, ContentFile: "safe-skill/SKILL.md"}
	plan, err = prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || !plan.Preview.LocallyValid || plan.Preview.Representations[0].PackageFiles != 0 {
		t.Fatalf("document-only Skill: %v", err)
	}
	if err := json.Unmarshal(plan.Bodies[1], &version); err != nil || version.Representations[0].Content != markdown || len(version.Representations[0].PackageData) != 0 {
		t.Fatal("Markdown content/delivery changed")
	}
}

func TestImportSkillGZIPAndUnsafeArchives(t *testing.T) {
	dir, manifest := importTestInput(t)
	r := &manifest.Resources[0]
	r.Kind = catalog.KindSkill
	r.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaGZIP, PackageFile: "skill.tar.gz", Handoff: &catalog.SkillHandoff{OCI: &catalog.OCIHandoffReference{Reference: "registry.example.com/skills/safe-skill@sha256:" + strings.Repeat("a", 64)}}}}
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gz)
	markdown := []byte("---\nname: safe-skill\ndescription: Helps with local examples\n---\n# Instructions\n")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "safe-skill/SKILL.md", Mode: 0644, Size: int64(len(markdown))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(markdown); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	writeImportTestFile(t, dir, "skill.tar.gz", buffer.Bytes(), 0600)
	plan, err := prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || !plan.Preview.LocallyValid || plan.Preview.Assets[0].Digest != importDigest(buffer.Bytes()) {
		t.Fatalf("GZIP: %v %#v", err, plan.Preview.Findings)
	}
	writeImportTestFile(t, dir, "skill.tar.gz", []byte("not an archive"), 0600)
	plan, err = prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || plan.Preview.LocallyValid {
		t.Fatal("malformed archive passed local validation")
	}
	r.Representations[0] = importRepresentation{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, PackageDirectory: "safe-skill", Handoff: r.Representations[0].Handoff}
	writeImportTestFile(t, dir, "safe-skill/README.md", []byte("missing SKILL.md"), 0600)
	plan, err = prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || plan.Preview.LocallyValid || !hasImportFinding(plan, "SKILL_PACKAGE_SKILL_MD_REQUIRED") {
		t.Fatalf("missing marker: %v %#v", err, plan.Preview.Findings)
	}
}

func TestImportSkillDirectoryDepthIsRelativeToPackageRoot(t *testing.T) {
	for _, directory := range []string{"safe-skill", "assets/skills/safe-skill"} {
		for _, depth := range []int{catalog.MaxSkillPackageDepth, catalog.MaxSkillPackageDepth + 1} {
			t.Run(directory+"/depth-"+strconv.Itoa(depth), func(t *testing.T) {
				dir, manifest := importTestInput(t)
				r := &manifest.Resources[0]
				r.Kind = catalog.KindSkill
				r.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, PackageDirectory: directory, Handoff: &catalog.SkillHandoff{OCI: &catalog.OCIHandoffReference{Reference: "registry.example.com/safe-skill@sha256:" + strings.Repeat("a", 64)}}}}
				writeImportTestFile(t, dir, directory+"/SKILL.md", []byte("---\nname: safe-skill\ndescription: Helps with local examples\n---\nInstructions\n"), 0600)
				member := strings.Repeat("level/", depth-1) + "evidence.txt"
				writeImportTestFile(t, dir, directory+"/"+member, []byte("supporting evidence"), 0600)
				plan, err := prepareImportTest(t, dir, manifest, 2<<20)
				if depth > catalog.MaxSkillPackageDepth {
					if err == nil {
						t.Fatalf("over-deep member was not rejected during directory preparation: %v", err)
					}
					return
				}
				if err != nil || !plan.Preview.LocallyValid || plan.Preview.Representations[0].PackageFiles != 2 {
					t.Fatalf("valid package at depth limit rejected: %v %#v", err, plan.Preview.Findings)
				}
			})
		}
	}
}

func TestImportRejectsUnsafePathsAndDocuments(t *testing.T) {
	for name, mutate := range map[string]func(*importResource, string){
		"escape":           func(r *importResource, _ string) { r.Representations[0].DataFile = "../agent.json" },
		"absolute":         func(r *importResource, _ string) { r.Representations[0].DataFile = "/etc/passwd" },
		"backslash":        func(r *importResource, _ string) { r.Representations[0].DataFile = `a\agent.json` },
		"directory":        func(r *importResource, _ string) { r.Representations[0].DataFile = "." },
		"missing":          func(r *importResource, _ string) { r.Representations[0].DataFile = "missing.json" },
		"empty source":     func(r *importResource, _ string) { r.Representations[0].DataFile = "" },
		"multiple sources": func(r *importResource, _ string) { r.Representations[0].ContentFile = "agent.json" },
		"duplicate JSON": func(_ *importResource, dir string) {
			writeImportTestFile(t, dir, "agent.json", []byte(`{"name":"a","name":"b"}`), 0600)
		},
		"JSON trailing": func(_ *importResource, dir string) { writeImportTestFile(t, dir, "agent.json", []byte(`{} {}`), 0600) },
		"JSON array":    func(_ *importResource, dir string) { writeImportTestFile(t, dir, "agent.json", []byte(`[]`), 0600) },
		"JSON null":     func(_ *importResource, dir string) { writeImportTestFile(t, dir, "agent.json", []byte(`null`), 0600) },
		"JSON surrogate": func(_ *importResource, dir string) {
			writeImportTestFile(t, dir, "agent.json", []byte(`{"name":"\ud800"}`), 0600)
		},
		"bad kind":                 func(r *importResource, _ string) { r.Kind = "unknown" },
		"bad key":                  func(r *importResource, _ string) { r.Key = "../secret" },
		"bad UUID":                 func(r *importResource, _ string) { r.NamespaceID = "not-a-UUID" },
		"blank identifier":         func(r *importResource, _ string) { r.CanonicalIdentifier = " " },
		"bad AIR":                  func(r *importResource, _ string) { r.CanonicalIdentifier = "urn:air:bad_domain:agent" },
		"blank version":            func(r *importResource, _ string) { r.Version = " " },
		"no representations":       func(r *importResource, _ string) { r.Representations = nil },
		"too many representations": func(r *importResource, _ string) { r.Representations = make([]importRepresentation, 17) },
		"bad role":                 func(r *importResource, _ string) { r.Representations[0].Role = "unknown" },
		"bad media":                func(r *importResource, _ string) { r.Representations[0].MediaType = "invalid" },
		"too long version":         func(r *importResource, _ string) { r.Version = strings.Repeat("界", 129) },
		"too long display":         func(r *importResource, _ string) { r.Listing.DisplayName = strings.Repeat("界", 161) },
		"blank description":        func(r *importResource, _ string) { r.Listing.Description = " " },
		"unsafe docs URL":          func(r *importResource, _ string) { r.Listing.DocumentationURL = "http://example.com" },
		"null extension":           func(r *importResource, _ string) { r.Listing.Extensions = json.RawMessage(`null`) },
		"both extension forms": func(r *importResource, _ string) {
			r.Listing.Extensions = json.RawMessage(`{}`)
			r.Listing.ExtensionsFile = "agent.json"
		},
		"handoff without package": func(r *importResource, _ string) { r.Representations[0].Handoff = &catalog.SkillHandoff{} },
		"checksum without Plugin": func(r *importResource, _ string) {
			v := "sha256:" + strings.Repeat("a", 64)
			r.Representations[0].ExpectedDigest = &v
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, manifest := importTestInput(t)
			mutate(&manifest.Resources[0], dir)
			if _, err := prepareImportTest(t, dir, manifest, 2<<20); err == nil {
				t.Fatal("invalid import accepted")
			}
		})
	}
	dir, manifest := importTestInput(t)
	filename := writeImportTestFile(t, dir, "import.yaml", []byte(importTestYAML), 0600)
	if err := os.Symlink("agent.json", filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	manifest.Resources[0].Representations[0].DataFile = "link.json"
	if _, err := prepareImportTest(t, dir, manifest, 2<<20); err == nil {
		t.Fatal("file symlink accepted")
	}
	if err := os.Symlink(dir, filepath.Join(dir, "link-dir")); err != nil {
		t.Fatal(err)
	}
	manifest.Resources[0].Representations[0].DataFile = "link-dir/agent.json"
	if _, err := prepareImportTest(t, dir, manifest, 2<<20); err == nil {
		t.Fatal("ancestor symlink accepted")
	}
	if err := os.Symlink(filename, filepath.Join(dir, "manifest-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareImportFile(context.Background(), filepath.Join(dir, "manifest-link"), 2<<20); err == nil {
		t.Fatal("manifest symlink accepted")
	}
	if _, err := prepareImportFile(context.Background(), filepath.Join(dir, "link-dir/import.yaml"), 2<<20); err == nil {
		t.Fatal("manifest ancestor symlink accepted")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := readImportAsset(context.Background(), root, "agent.json", 1); err == nil {
		t.Fatal("file bound ignored")
	}
	if _, err := readImportAsset(context.Background(), root, "link-dir", 100); err == nil {
		t.Fatal("directory accepted as regular asset")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareImportFile(ctx, filename, 2<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preview: %v", err)
	}
}

func TestImportURLAndPluginRemainOffline(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("preview made a network request")
		return nil, errors.New("network forbidden")
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	dir, manifest := importTestInput(t)
	r := &manifest.Resources[0]
	r.Kind = catalog.KindPlugin
	digest := "sha256:" + strings.Repeat("a", 64)
	r.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: "https://archives.example.com/release.zip", ExpectedDigest: &digest}}
	plan, err := prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || !plan.Preview.LocallyValid || plan.Preview.Representations[0].LocalValidation != "pending-server-retrieval" || len(plan.Preview.Assets) != 0 {
		t.Fatalf("Plugin URL: %v %#v", err, plan.Preview)
	}
	for name, source := range map[string]importRepresentation{
		"local manifest": {Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, DataFile: "agent.json"},
		"local archive":  {Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, PackageFile: "agent.json"},
		"HTTP":           {Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: "http://archives.example.com/release.zip"},
		"userinfo":       {Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: "https://user@example.com/release.zip"},
		"profile":        {Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: "https://example.com/release.zip", ProfileID: "unreviewed"},
		"wrong checksum": {Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: "https://example.com/release.zip", ExpectedDigest: new("invalid")},
	} {
		t.Run(name, func(t *testing.T) {
			r.Representations = []importRepresentation{source}
			if _, err := prepareImportTest(t, dir, manifest, 2<<20); err == nil {
				t.Fatal("invalid Plugin input accepted")
			}
		})
	}
	r.Kind = catalog.KindAgent
	r.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, URL: "https://cards.example.com/agent.json"}}
	plan, err = prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || !plan.Preview.LocallyValid || plan.Preview.Representations[0].LocalValidation != "pending-server-retrieval" {
		t.Fatalf("remote Agent content falsely validated: %v", err)
	}
}

func TestImportCLIHelpLimitsOutputAndValidationExit(t *testing.T) {
	dir, _ := importTestInput(t)
	filename := writeImportTestFile(t, dir, "import.yaml", []byte(importTestYAML), 0600)
	var output bytes.Buffer
	if err := runImport(context.Background(), []string{"preview", "--file", filename}, &output); err != nil {
		t.Fatal(err)
	}
	if err := runImport(context.Background(), []string{"preview", "--file", filename}, importShortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("preview short output: %v", err)
	}
	commandHelp := captureCatalogctlStdout(t, func() error { return runArgs([]string{"import", "preview", "--help"}) })
	if !bytes.Contains(commandHelp, []byte("usage: catalogctl import preview")) {
		t.Fatal("top-level dispatcher does not expose import preview help")
	}
	var report importBatchPreview
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || !report.LocallyValid || len(report.Resources) != 1 || len(report.Resources[0].Commands) != 4 {
		t.Fatalf("CLI output: %v", err)
	}
	for _, input := range [][]string{{"--help"}, {"preview", "--help"}, {"help"}} {
		output.Reset()
		if err := runImport(context.Background(), input, &output); err != nil || !strings.Contains(output.String(), "no network") {
			t.Fatalf("help: %v", err)
		}
	}
	for _, input := range [][]string{nil, {"apply"}, {"resume"}, {"preview"}, {"preview", "--file", filename, "extra"}, {"preview", "--file", filename, "--timeout", "0s"}, {"preview", "--file", filename, "--timeout", "11m"}, {"preview", "--file", filename, "--max-request-bytes", "0"}, {"preview", "--file", filename, "--max-request-bytes", "67108865"}, {"preview", "--unknown", "SECRET_SENTINEL"}} {
		output.Reset()
		err := runImport(context.Background(), input, &output)
		if err == nil || output.Len() != 0 || strings.Contains(safeerr.Message(err), "SECRET_SENTINEL") {
			t.Fatalf("invalid CLI input: %v", err)
		}
	}
	writeImportTestFile(t, dir, "agent.json", []byte(`{"name":"SENSITIVE_SENTINEL"}`), 0600)
	output.Reset()
	err := runImport(context.Background(), []string{"preview", "--file", filename}, &output)
	if err == nil || json.Unmarshal(output.Bytes(), &report) != nil || report.LocallyValid || strings.Contains(output.String(), "SENSITIVE_SENTINEL") {
		t.Fatal("invalid native report/exit classification wrong")
	}
	if err := runImport(context.Background(), []string{"--help"}, importShortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short output: %v", err)
	}
}

func TestImportAdditionalPreparationGuards(t *testing.T) {
	dir, manifest := importTestInput(t)
	manifest.Resources[0].CanonicalIdentifier = " URN:AIR:Publisher.Example:agents:MixedCase "
	manifest.Resources[0].Version = " release candidate "
	manifest.Resources[0].Listing.Extensions = json.RawMessage(`{"` + artwork.IRI + `":{"schemaVersion":1,"icon":{"url":"https://images.example.com/icon.png","alt":"Example icon","rights":{"basis":"owned","attribution":"Example project"}}}}`)
	plan, err := prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil || !plan.Preview.LocallyValid {
		t.Fatalf("artwork input failed without retrieval: %v", err)
	}
	resource, err := importJSONObject(plan.Bodies[0])
	if err != nil || resource["canonicalIdentifier"] != "urn:air:publisher.example:agents:MixedCase" {
		t.Fatal("AIR normalization differs from Resource creation")
	}
	version, err := importJSONObject(plan.Bodies[1])
	if err != nil || version["version"] != "release candidate" {
		t.Fatal("version normalization differs from Version creation")
	}
	for name, mutate := range map[string]func(*importResource){
		"bad artwork": func(r *importResource) {
			r.Listing.Extensions = json.RawMessage(`{"` + artwork.IRI + `":{"schemaVersion":1,"unknown":true}}`)
		},
		"NUL tag":                          func(r *importResource) { r.Listing.Tags = []string{"a\x00b"} },
		"short normalized query":           func(r *importResource) { r.Listing.RepresentativeQueries = []string{"a   "} },
		"control extensions unknown field": func(r *importResource) { r.Listing.ExtensionsFile = "missing" },
		"invalid handoff": func(r *importResource) {
			r.Kind = catalog.KindSkill
			r.Representations[0] = importRepresentation{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, PackageFile: "agent.json", Handoff: &catalog.SkillHandoff{}}
		},
		"text NUL": func(r *importResource) {
			r.Kind = catalog.KindSkill
			r.Representations[0] = importRepresentation{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillMarkdown, ContentFile: "binary.txt"}
		},
		"URL unsupported media": func(r *importResource) {
			r.Representations[0] = importRepresentation{Role: catalog.RepresentationPrimary, MediaType: "application/unknown", URL: "https://example.com/card"}
		},
		"URL Skill archive": func(r *importResource) {
			r.Kind = catalog.KindSkill
			r.Representations[0] = importRepresentation{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, URL: "https://example.com/skill.zip"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, manifest := importTestInput(t)
			writeImportTestFile(t, dir, "binary.txt", []byte("a\x00b"), 0600)
			mutate(&manifest.Resources[0])
			if _, err := prepareImportTest(t, dir, manifest, 2<<20); err == nil {
				t.Fatal("invalid preparation accepted")
			}
		})
	}
	filename := writeImportTestFile(t, dir, "oversized.json", []byte("{}"), 0600)
	if err := os.Truncate(filename, importMaxPreparedBytes+1); err != nil {
		t.Fatal(err)
	}
	manifest.Resources[0].Listing.Extensions = nil
	manifest.Resources[0].Listing.ExtensionsFile = "oversized.json"
	if _, err := prepareImportTest(t, dir, manifest, 2<<20); err == nil {
		t.Fatal("oversized extension file accepted")
	}
}

func TestImportDirectoryAndArchiveSafetyGuards(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"../escape", "missing", "Invalid_Name"} {
		if _, err := importSkillDirectory(context.Background(), root, name); err == nil {
			t.Fatalf("invalid directory accepted: %s", name)
		}
	}
	writeImportTestFile(t, dir, "safe-skill/SKILL.md", []byte("---\nname: safe-skill\ndescription: Useful skill\n---\nInstructions\n"), 0600)
	writeImportTestFile(t, dir, "safe-skill/huge.txt", []byte(""), 0600)
	if err := os.Truncate(filepath.Join(dir, "safe-skill/huge.txt"), catalog.MaxSkillPackageFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := importSkillDirectory(context.Background(), root, "safe-skill"); err == nil {
		t.Fatal("oversized directory member accepted")
	}
	if err := os.Remove(filepath.Join(dir, "safe-skill/huge.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("SKILL.md", filepath.Join(dir, "safe-skill/link")); err != nil {
		t.Fatal(err)
	}
	if _, err := importSkillDirectory(context.Background(), root, "safe-skill"); err == nil {
		t.Fatal("directory member symlink accepted")
	}
	if err := os.Remove(filepath.Join(dir, "safe-skill/link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "safe-skill/fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readImportAsset(context.Background(), root, "safe-skill/fifo", 100); err == nil {
		t.Fatal("special file accepted as asset")
	}
	if _, err := importSkillDirectory(context.Background(), root, "safe-skill"); err == nil {
		t.Fatal("special directory member accepted")
	}
	if err := os.Remove(filepath.Join(dir, "safe-skill/fifo")); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < catalog.MaxSkillPackageFiles; index++ {
		writeImportTestFile(t, dir, "safe-skill/files/"+strconv.Itoa(index), nil, 0600)
	}
	if _, err := importSkillDirectory(context.Background(), root, "safe-skill"); err == nil {
		t.Fatal("directory file count bound ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := importSkillDirectory(ctx, root, "safe-skill"); !errors.Is(err, context.Canceled) {
		t.Fatalf("directory cancellation: %v", err)
	}
	var archiveBuffer importArchiveBuffer
	if _, err := archiveBuffer.Write(make([]byte, catalog.MaxSkillPackageBytes)); err != nil {
		t.Fatal(err)
	}
	if count, err := archiveBuffer.Write([]byte{1}); err == nil || count != 0 || archiveBuffer.Len() != catalog.MaxSkillPackageBytes {
		t.Fatal("archive write bound ignored")
	}

	archiveDir, manifest := importTestInput(t)
	r := &manifest.Resources[0]
	r.Kind = catalog.KindSkill
	r.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, PackageFile: "unsafe.zip", Handoff: &catalog.SkillHandoff{OCI: &catalog.OCIHandoffReference{Reference: "registry.example.com/safe-skill@sha256:" + strings.Repeat("a", 64)}}}}
	var zipped bytes.Buffer
	writer := zip.NewWriter(&zipped)
	entry, err := writer.Create("../outside/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("unsafe member")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writeImportTestFile(t, archiveDir, "unsafe.zip", zipped.Bytes(), 0600)
	plan, err := prepareImportTest(t, archiveDir, manifest, 2<<20)
	if err != nil || plan.Preview.LocallyValid || !hasImportFinding(plan, "SKILL_PACKAGE_PATH_INVALID") {
		t.Fatalf("unsafe archive bypasses native validation: %v %#v", err, plan.Preview.Findings)
	}
}

type importShortWriter struct{}

func (importShortWriter) Write(raw []byte) (int, error) { return len(raw) - 1, nil }

func singleImportTestBatch(plan importPlan) importBatchPlan {
	return importBatchPlan{Preview: importBatchPreview{Format: 1, LocallyValid: plan.Preview.LocallyValid,
		ManifestDigest: plan.Preview.ManifestDigest, MaxResources: importMaxResources, MaxSteps: importMaxSteps,
		MaxPreparedBytes: importMaxPreparedBytes, MaxReportBytes: importMaxReportBytes, PreparedBytes: plan.Preview.PreparedBytes,
		Resources: []importPreview{plan.Preview}}, Items: []importPlan{plan}}
}
