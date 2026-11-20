// Personal units cover file admission and local preview without a server or executable.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
)

// personalTestFile includes significant labels, duplicate filters and both exact reference origins.
func personalTestFile() personalFile {
	absent := false
	return personalFile{Format: personalFileFormat, Origin: "https://catalog.example.test", TenantID: importTestPrincipal.TenantID, Consistency: "observed-pages", Items: []personal.Input{
		{Type: "search", Label: " Café e\u0301 🙂 ", Search: &personal.Criteria{Text: " private\nquery ", Tags: []string{"a,b", "", " exact ", "a,b"}}},
		{Type: "resource", Label: "Exact release", Reference: &personal.Reference{Origin: "local", ReleaseID: importTestPrincipal.RealmID, VersionRevisionID: importTestPrincipal.MembershipID}},
		{Type: "resource", Label: "External without version", Reference: &personal.Reference{Origin: "foreign", Kind: "agent", SourceID: importTestPrincipal.RealmID, PresentationGeneration: 3, Identifier: "urn:air:peer.example:agents:one", VersionPresent: &absent}},
	}}
}

// personalTestBytes produces fixture JSON independently of the decoder.
func personalTestBytes(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestPersonalFileOptionalEmptySearchFilters preserves omitted-filter meaning and rejects empty origins.
func TestPersonalFileOptionalEmptySearchFilters(t *testing.T) {
	for _, field := range []string{"kinds", "tags", "categories", "origins"} {
		t.Run(field, func(t *testing.T) {
			want := personalTestFile()
			criteria := want.Items[0].Search
			criteria.Origins = append(criteria.Origins, "local")
			criteria.Kinds = append(criteria.Kinds, "agent")
			criteria.Categories = []string{"tools"}
			switch field {
			case "kinds":
				criteria.Kinds = nil
			case "tags":
				criteria.Tags = nil
			case "categories":
				criteria.Categories = nil
			case "origins":
				criteria.Origins = nil
			}
			omitted := personalTestBytes(t, want)
			control, err := decodePersonalFile(omitted)
			if err != nil || !reflect.DeepEqual(control, want) {
				t.Fatal("omitted-filter control changed private intent", err)
			}
			raw := bytes.Replace(omitted, []byte(`"search":{`), []byte(`"search":{"`+field+`":[],`), 1)
			if bytes.Equal(raw, omitted) {
				t.Fatal("fixture did not supply the explicit empty filter")
			}
			got, err := decodePersonalFile(raw)
			if field == "origins" {
				if err == nil {
					t.Fatal("explicit empty origins accepted as omitted origins")
				}
				return
			}
			if err != nil {
				t.Fatal("valid empty optional filter refused", err)
			}
			encoded := personalTestBytes(t, got)
			if !bytes.Equal(encoded, omitted) {
				t.Fatal("empty optional filter changed the exact save input")
			}
			again, err := decodePersonalFile(encoded)
			if err != nil || !reflect.DeepEqual(again, want) {
				t.Fatal("optional-filter serialization changed labels, criteria or references", err)
			}
		})
	}
}

func TestPersonalFileStrictContract(t *testing.T) {
	file := personalTestFile()
	raw := personalTestBytes(t, file)
	got, err := decodePersonalFile(raw)
	if err != nil || !reflect.DeepEqual(got, file) {
		t.Fatal("exact private intent changed", err)
	}
	for name, bad := range map[string][]byte{
		"duplicate":            bytes.Replace(raw, []byte(`"format":`), []byte(`"format":"ignored","format":`), 1),
		"nested duplicate":     bytes.Replace(raw, []byte(`"label":`), []byte(`"label":"ignored","label":`), 1),
		"unknown":              append([]byte(`{"token":"PRIVATE_SENTINEL",`), raw[1:]...),
		"wrong case":           bytes.Replace(raw, []byte(`"tenantId"`), []byte(`"TenantId"`), 1),
		"null":                 bytes.Replace(raw, []byte(`"text":" private\nquery "`), []byte(`"text":null`), 1),
		"saved owner":          bytes.Replace(raw, []byte(`"type":"search"`), []byte(`"type":"search","ownerId":"private"`), 1),
		"cursor":               bytes.Replace(raw, []byte(`"text":`), []byte(`"cursor":"private","text":`), 1),
		"page size":            bytes.Replace(raw, []byte(`"text":`), []byte(`"limit":10,"text":`), 1),
		"source URL":           bytes.Replace(raw, []byte(`"text":`), []byte(`"sourceUrl":"https://evil.invalid","text":`), 1),
		"trailing":             append(bytes.Clone(raw), []byte(` {}`)...),
		"invalid UTF8":         bytes.Replace(raw, []byte("Café"), []byte{'C', 0xff}, 1),
		"unpaired surrogate":   bytes.Replace(raw, []byte("Café"), []byte(`\ud800`), 1),
		"old format":           bytes.Replace(raw, []byte(personalFileFormat), []byte("oac-personal-library-v0"), 1),
		"owner instead tenant": bytes.Replace(raw, []byte(`"tenantId"`), []byte(`"ownerId"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodePersonalFile(bad); err == nil || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
				t.Fatal("invalid/private input admitted or echoed", err)
			}
		})
	}
	for _, change := range []func(*personalFile){
		func(f *personalFile) { f.Items = nil },
		func(f *personalFile) { f.Items = make([]personal.Input, 201) },
		func(f *personalFile) { f.Origin = "https://user:secret@catalog.example.test" },
		func(f *personalFile) { f.Origin += "/api" },
		func(f *personalFile) { f.Origin += "/" },
		func(f *personalFile) { f.Consistency = "snapshot" },
		func(f *personalFile) { f.TenantID = "other" },
		func(f *personalFile) { f.Items[0].Label = "  " },
		func(f *personalFile) { f.Items[0].Label = "bad\x7flabel" },
		func(f *personalFile) { f.Items[0].Label = strings.Repeat("🙂", 121) },
		func(f *personalFile) { f.Items[0].Search.Text = strings.Repeat("q", 17000) },
		func(f *personalFile) { f.Items[1].Reference.ReleaseID = "invalid" },
		func(f *personalFile) { f.Items[2].Reference.Version = "not-advertised" },
	} {
		file := personalTestFile()
		change(&file)
		if _, err := decodePersonalFile(personalTestBytes(t, file)); err == nil {
			t.Fatal("invalid field admitted")
		}
	}
	if _, err := decodePersonalFile(append(raw, bytes.Repeat([]byte{' '}, personalFileLimit)...)); err == nil {
		t.Fatal("oversized input admitted")
	}
	file.Items = []personal.Input{}
	if _, err := decodePersonalFile(personalTestBytes(t, file)); err != nil {
		t.Fatal("empty valid library refused", err)
	}
}

func TestPersonalOfflinePreviewSelectionAndDestination(t *testing.T) {
	file := personalTestFile()
	name := filepath.Join(canonicalTempDir(t), "library.json")
	if err := os.WriteFile(name, personalTestBytes(t, file), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"preview", "--file", name, "--base-url", file.Origin, "--tenant-id", file.TenantID}
	getenv := func(name string) string {
		if name != "CATALOG_REMOTE_BASE_URL" {
			t.Fatal("offline preview accessed credentials", name)
		}
		return ""
	}
	var output bytes.Buffer
	if err := runPersonal(t.Context(), append(base, "--create", "1", "--create", "3"), getenv, &output); err != nil {
		t.Fatal(err)
	}
	var preview struct {
		Items []struct {
			Index  int            `json:"index"`
			Action string         `json:"action"`
			Item   personal.Input `json:"item"`
		} `json:"items"`
	}
	if json.Unmarshal(output.Bytes(), &preview) != nil || len(preview.Items) != 3 || preview.Items[0].Action != "create" || preview.Items[1].Action != "skip" || preview.Items[2].Action != "create" || !reflect.DeepEqual(preview.Items[0].Item, file.Items[0]) {
		t.Fatal("preview changed the selected intent")
	}
	for _, b := range output.Bytes() {
		if b >= 128 {
			t.Fatal("private preview contains unescaped terminal Unicode")
		}
	}
	for _, extra := range [][]string{{"--all", "--create", "1"}, {"--create", "4"}, {"--create", "0"}, {"--create", "1", "--create", "1"}, {"--tenant-id", importTestPrincipal.RealmID}, {"--base-url", "https://elsewhere.example.test"}, {"--token-file", "/PRIVATE_SENTINEL"}} {
		output.Reset()
		if err := runPersonal(t.Context(), append(base, extra...), getenv, &output); err == nil || strings.Contains(err.Error(), "PRIVATE_SENTINEL") || output.Len() != 0 {
			t.Fatal("invalid selection/destination flags admitted", extra, err)
		}
	}
	selected, err := personalSelection(3, false, nil)
	if err != nil || !reflect.DeepEqual(selected, []bool{false, false, false}) {
		t.Fatal("default selection created items")
	}
	// Confirmation and separate progress are mandatory even for an otherwise valid plan.
	for _, extra := range [][]string{
		{"--state-file", filepath.Join(filepath.Dir(name), "progress", "state.json")},
		{"--confirm"},
		{"--confirm", "--state-file", name},
	} {
		output.Reset()
		args := append([]string{"apply", "--file", name, "--base-url", file.Origin, "--all"}, extra...)
		err := runPersonal(t.Context(), args, func(string) string { return "" }, &output)
		if err == nil || !strings.Contains(err.Error(), "requires --confirm and a distinct --state-file") || output.Len() != 0 {
			t.Fatal("unconfirmed or overlapping restore reached credentials/mutation", err)
		}
	}
	inputRoot, err := os.OpenRoot(filepath.Dir(name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inputRoot.Close() }()
	if retained, err := inputRoot.ReadFile(filepath.Base(name)); err != nil || !bytes.Equal(retained, personalTestBytes(t, file)) {
		t.Fatal("refused restore changed its input", err)
	}
	for _, args := range [][]string{{"--help"}, {"preview", "--help"}} {
		output.Reset()
		if err := runPersonal(t.Context(), args, func(string) string { return "" }, &output); err != nil || !strings.Contains(output.String(), "23h59m") {
			t.Fatal("missing help", err)
		}
	}
}

func TestPersonalExportPrivatePublication(t *testing.T) {
	root := canonicalTempDir(t)
	directory := filepath.Join(root, "backup")
	file := personalTestFile()
	if err := writePersonalFile(t.Context(), directory, file); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]os.FileMode{directory: 0700, filepath.Join(directory, "library.json"): 0600} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("export permissions", err)
		}
	}
	_, retained, err := readPersonalFile(filepath.Join(directory, "library.json"))
	if err != nil || !reflect.DeepEqual(retained, file) {
		t.Fatal("export contents", err)
	}
	if err := writePersonalFile(t.Context(), directory, file); err == nil {
		t.Fatal("existing export overwritten")
	}
	privateRoot, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	err = writeExportFile(t.Context(), privateRoot, "library.json", []byte("replacement"))
	_ = privateRoot.Close()
	if err == nil || strings.Contains(err.Error(), "export.json") {
		t.Fatal("overwrite refusal gives another format's completion marker", err)
	}
	_, retained, err = readPersonalFile(filepath.Join(directory, "library.json"))
	if err != nil || !reflect.DeepEqual(retained, file) {
		t.Fatal("refused overwrite changed private intent", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := writePersonalFile(ctx, filepath.Join(root, "cancelled"), file); err == nil {
		t.Fatal("cancelled export succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "cancelled")); !os.IsNotExist(err) {
		t.Fatal("incomplete private export retained", err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	if err := writePersonalFile(t.Context(), filepath.Join(link, "nested"), file); err == nil {
		t.Fatal("symlink ancestor admitted")
	}
	for _, state := range []string{filepath.Join(directory, "library.json"), filepath.Join(directory, "library.json.lock")} {
		input := filepath.Join(directory, "library.json")
		if strings.HasSuffix(state, ".lock") {
			input, state = state, input
		}
		if personalSeparateFiles(input, state) {
			t.Fatal("input/progress collision admitted")
		}
	}
	for _, n := range []int{1, 2, 3} {
		if _, err := personalSelection(3, false, []int{n}); err != nil {
			t.Fatal(fmt.Sprintf("valid selection %d", n), err)
		}
	}
}

func TestPersonalFileNativeCriteriaRoundTrip(t *testing.T) {
	streaming := false
	for _, native := range []*catalog.NativeCapabilitiesFilter{
		{AgentStreaming: &streaming, AgentSkillIDs: []string{"invoice, literal "}, AgentSkillTags: []string{"Finance", "finance"}},
		{MCPTransports: []string{"sse", "streamable-http"}, MCPProtocolVersions: []string{"2026-07-28"}},
	} {
		file := personalTestFile()
		file.Items[0].Search.Origins = []catalog.SearchResultType{catalog.SearchResultLocal}
		file.Items[0].Search.NativeCapabilities = native
		raw := personalTestBytes(t, file)
		got, err := decodePersonalFile(raw)
		if err != nil || !reflect.DeepEqual(got, file) {
			t.Fatal("portable native criteria changed", err)
		}
		if !bytes.Equal(personalTestBytes(t, got), raw) {
			t.Fatal("native backup re-encoding changed intent")
		}
		file.Items[0].Search.Origins = nil
		if _, err := decodePersonalFile(personalTestBytes(t, file)); err == nil {
			t.Fatal("native backup accepted a broadened origin")
		}
	}
}
