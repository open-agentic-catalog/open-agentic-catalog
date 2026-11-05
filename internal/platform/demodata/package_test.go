package demodata

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPackageDirectoryBudgets(t *testing.T) {
	for _, test := range []struct {
		name  string
		files int
		size  int
		valid bool
	}{
		{"exact file count", MaxFiles, 1, true},
		{"too many files", MaxFiles + 1, 1, false},
		{"exact total bytes", 2, MaxAssetBytes, true},
		{"too many total bytes", 3, MaxAssetBytes, false},
		{"oversize asset", 1, MaxAssetBytes + 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assets := fstest.MapFS{}
			for i := range test.files {
				name := fmt.Sprintf("package/nested/file-%03d", i)
				if i == 0 {
					name = "package/plugin.json"
				}
				assets[name] = &fstest.MapFile{Data: bytes.Repeat([]byte{'a'}, test.size)}
			}
			raw, err := packageBytes(assets, RepresentationSource{MediaType: catalog.PluginMediaZIP, PackageDirectory: "package"})
			if (err == nil) != test.valid {
				t.Fatalf("package valid = %v, want %v: %v", err == nil, test.valid, err)
			}
			if !test.valid {
				if raw != nil {
					t.Fatal("rejected package returned partial bytes")
				}
				return
			}
			archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil || len(archive.File) != test.files {
				t.Fatalf("package inventory changed: %v", err)
			}
			for _, member := range archive.File {
				reader, err := member.Open()
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(reader)
				_ = reader.Close()
				if err != nil || !bytes.Equal(body, assets["package/"+member.Name].Data) {
					t.Fatalf("package member changed: %s: %v", member.Name, err)
				}
			}
		})
	}
}

func TestPackageDirectoryRejectsIncompleteAndUnsafeInventory(t *testing.T) {
	source := RepresentationSource{MediaType: catalog.PluginMediaZIP, PackageDirectory: "package"}
	assets := fstest.MapFS{"package/plugin.json": {Data: []byte(`{"name":"example"}`)}}
	if _, err := packageBytes(assets, source); err != nil {
		t.Fatal("valid package control", err)
	}
	for _, name := range []string{"../package", "missing"} {
		invalid := source
		invalid.PackageDirectory = name
		if raw, err := packageBytes(assets, invalid); err == nil || raw != nil {
			t.Fatalf("unsafe or missing directory accepted: %s", name)
		}
	}
	// The marker and parent directory consume two entries; nested entries count
	// toward the same work ceiling even when they contain no regular files.
	for _, count := range []int{MaxFiles*2 - 2, MaxFiles*2 - 1, MaxFiles*2 + 1} {
		t.Run(fmt.Sprintf("nested entries %d", count), func(t *testing.T) {
			assets := fstest.MapFS{"package/plugin.json": {Data: []byte(`{"name":"example"}`)}}
			for i := range count {
				assets[fmt.Sprintf("package/nested/empty-%03d", i)] = &fstest.MapFile{Mode: 0700 | fs.ModeDir}
			}
			raw, err := packageBytes(assets, source)
			if count == MaxFiles*2-2 {
				if err != nil {
					t.Fatal("exact directory-entry budget rejected", err)
				}
			} else if err == nil || raw != nil {
				t.Fatal("nested directory inventory exceeded work bound")
			}
		})
	}
}

func TestSkillPackageDirectoryRootAdmission(t *testing.T) {
	for _, test := range []struct {
		name, directory string
		valid           bool
	}{
		{"valid nested root", "skills/order-triage", true},
		{"uppercase root", "skills/Order-Triage", false},
		{"underscore root", "skills/order_triage", false},
		{"oversize root", "skills/" + strings.Repeat("a", 65), false},
		{"traversal root", "skills/../order-triage", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Every root contains the same marker and file so invalid names
			// cannot pass merely because package content is missing.
			assets := fstest.MapFS{
				test.directory + "/SKILL.md":            {Data: []byte("exact skill marker")},
				test.directory + "/references/help.txt": {Data: []byte("exact reference")},
			}
			raw, err := packageBytes(assets, RepresentationSource{MediaType: catalog.SkillPackageMediaZIP, PackageDirectory: test.directory})
			if !test.valid {
				if err == nil || raw != nil {
					t.Fatal("invalid Skill root produced an archive")
				}
				return
			}
			if err != nil {
				t.Fatal("valid Skill root rejected", err)
			}
			archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			wantNames := []string{"order-triage/SKILL.md", "order-triage/references/help.txt"}
			wantBodies := []string{"exact skill marker", "exact reference"}
			if len(archive.File) != len(wantNames) {
				t.Fatalf("archive has %d members, want %d", len(archive.File), len(wantNames))
			}
			for i, member := range archive.File {
				reader, err := member.Open()
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(reader)
				_ = reader.Close()
				if err != nil || member.Name != wantNames[i] || string(body) != wantBodies[i] {
					t.Fatalf("Skill package member %d = %q %q: %v", i, member.Name, body, err)
				}
			}
		})
	}
}

func TestPackageFileMapBudgetsAndBodies(t *testing.T) {
	large := strings.Repeat("a", MaxAssetBytes)
	assets := fstest.MapFS{"asset": {Data: []byte(large)}}
	source := RepresentationSource{Files: map[string]Body{
		"a.txt": {Inline: &large},
		"b.txt": {File: "asset"},
	}}
	raw, err := packageBytes(assets, source)
	if err != nil {
		t.Fatal("exact aggregate byte limit", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(archive.File))
	for _, member := range archive.File {
		names = append(names, member.Name)
		if member.UncompressedSize64 != uint64(MaxAssetBytes) {
			t.Fatal("accepted package silently truncated content")
		}
	}
	if !reflect.DeepEqual(names, []string{"a.txt", "b.txt"}) {
		t.Fatalf("archive inventory = %v", names)
	}
	for _, test := range []struct {
		name string
		path string
		body Body
	}{
		{"aggregate exceeded", "c.txt", Body{Inline: new("x")}},
		{"traversal", "../c.txt", Body{Inline: new("")}},
		{"missing asset", "c.txt", Body{File: "missing"}},
		{"ambiguous body", "c.txt", Body{File: "asset", Inline: new("")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source.Files[test.path] = test.body
			defer delete(source.Files, test.path)
			if raw, err := packageBytes(assets, source); err == nil || raw != nil {
				t.Fatal("invalid package accepted")
			}
		})
	}
}
