package catalog

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"testing"
)

func TestPluginArchiveMetadataRequiresCompleteExactIdentity(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		raw            string
		valid, present bool
	}{
		{"", true, false}, {"null", true, false}, {`{"unrelated":"kept"}`, true, false},
		{`{"packageDigest":"` + digest + `","packageSize":100}`, true, true},
		{`{"packageDigest":"` + digest + `"}`, false, false},
		{`{"packageSize":100}`, false, false},
		{`{"packageDigest":"bad","packageSize":100}`, false, false},
		{`{"packageDigest":null,"packageSize":100}`, false, false},
		{`{"packageDigest":"` + digest + `","packageSize":"100"}`, false, false},
		{`{"packageDigest":"` + digest + `","packageSize":0}`, false, false},
		{`{"packageDigest":"` + digest + `","packageSize":16777217}`, false, false},
		{`{"packageDigest":"` + digest + `","packageSize":10,"packageSize":100}`, false, false},
		{`[]`, false, false},
	} {
		got, err := PluginExpectedDigest(json.RawMessage(tc.raw))
		if (err == nil) != tc.valid || (got != nil) != tc.present || got != nil && *got != digest {
			t.Errorf("metadata %s: %v %v", tc.raw, got, err)
		}
	}
}

func TestPluginRetainedInventoryRejectsCorruption(t *testing.T) {
	const doc = `{"name":"tools"}`
	for name, mutate := range map[string]func(*PluginInspection){
		"valid":              func(*PluginInspection) {},
		"missing manifest":   func(p *PluginInspection) { p.Documents = nil },
		"changed bytes":      func(p *PluginInspection) { p.Documents[0].Content += "x" },
		"size mismatch":      func(p *PluginInspection) { p.ExpandedSize++ },
		"unassessed changed": func(p *PluginInspection) { p.Assessments.Malware = PackageAssessmentPassed },
		"traversing root":    func(p *PluginInspection) { p.RootDirectory = "../root" },
		"too many directories": func(p *PluginInspection) {
			p.Directories = make([]string, MaxSkillPackageFiles*MaxSkillPackageDepth+1)
		},
		"traversing directory":       func(p *PluginInspection) { p.Directories = []string{"../escape"} },
		"unsorted directories":       func(p *PluginInspection) { p.Directories = []string{"z", "a"} },
		"case colliding directories": func(p *PluginInspection) { p.Directories = []string{"Dir", "dir"} },
		"traversing file":            func(p *PluginInspection) { p.Files[0].Path = "../plugin.json" },
		"negative file size":         func(p *PluginInspection) { p.Files[0].Size = -1 },
		"oversized file":             func(p *PluginInspection) { p.Files[0].Size = MaxSkillPackageFileBytes + 1 },
		"invalid file checksum":      func(p *PluginInspection) { p.Files[0].Digest = "not-a-digest" },
		"missing root manifest": func(p *PluginInspection) {
			p.Files[0].Path = "mcp.json"
			p.Documents[0].Path = "mcp.json"
		},
		"unretained MCP document": func(p *PluginInspection) {
			p.Files = append([]PluginFile{{Path: "mcp.json", Size: int64(len(doc)), Digest: p.Files[0].Digest}}, p.Files...)
			p.ExpandedSize += int64(len(doc))
		},
		"file used as parent": func(p *PluginInspection) {
			p.Files = append(p.Files, PluginFile{Path: "plugin.json/child", Size: 0, Digest: p.Files[0].Digest})
		},
		"case collision": func(p *PluginInspection) { p.Directories = []string{"PLUGIN.JSON"} },
		"missing parent": func(p *PluginInspection) {
			p.RootDirectory = "root"
			p.Files[0].Path = "root/plugin.json"
			p.Documents[0].Path = "root/plugin.json"
		},
		"foreign component": func(p *PluginInspection) { p.Components = []PluginComponent{{Kind: "agent", Path: "plugin.json"}} },
		"unretained component": func(p *PluginInspection) {
			p.Components = []PluginComponent{{Kind: "skill", Path: "skills/read/SKILL.md"}}
		},
		"NUL component name": func(p *PluginInspection) {
			p.Components = []PluginComponent{{Kind: "mcp_server", Name: "bad\x00name", Path: "plugin.json"}}
		},
		"invalid UTF-8 component name": func(p *PluginInspection) {
			p.Components = []PluginComponent{{Kind: "mcp_server", Name: "bad\xffname", Path: "plugin.json"}}
		},
		"arbitrary file content": func(p *PluginInspection) {
			p.Files = append(p.Files, PluginFile{Path: "script.sh", Size: int64(len(doc)), Digest: p.Files[0].Digest, Executable: true})
			p.Documents = append(p.Documents, PluginDocument{Path: "script.sh", Content: doc})
			p.ExpandedSize += int64(len(doc))
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := &PluginInspection{ExpandedSize: int64(len(doc)), Files: []PluginFile{{Path: "plugin.json", Size: int64(len(doc)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(doc)))}}, Documents: []PluginDocument{{Path: "plugin.json", Content: doc}}, Assessments: NotAssessedSkillPackageAssessments()}
			mutate(p)
			if err := ValidatePluginInspection(p); (err == nil) != (name == "valid") {
				t.Fatalf("inspection: %v", err)
			}
		})
	}
}

// This fixture models retained byte identities, not format admission; the
// adapter tests own JSON schemas and Skill frontmatter validation.
func pluginInspectionDocuments(documents map[string]string) *PluginInspection {
	p := &PluginInspection{Assessments: NotAssessedSkillPackageAssessments()}
	directories := make(map[string]bool)
	for name, content := range documents {
		p.Documents = append(p.Documents, PluginDocument{Path: name, Content: content})
		p.Files = append(p.Files, PluginFile{Path: name, Size: int64(len(content)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))})
		p.ExpandedSize += int64(len(content))
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			directories[parent] = true
		}
	}
	for name := range directories {
		p.Directories = append(p.Directories, name)
	}
	sort.Strings(p.Directories)
	sort.Slice(p.Documents, func(i, j int) bool { return p.Documents[i].Path < p.Documents[j].Path })
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path })
	return p
}

func TestPluginRetainedDocumentByteLimits(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		valid         bool
	}{
		{"at limit", strings.Repeat("x", MaxPluginDocumentBytes), true},
		{"over limit", strings.Repeat("x", MaxPluginDocumentBytes+1), false},
		{"invalid UTF-8", "\xff", false},
		{"NUL", "before\x00after", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := pluginInspectionDocuments(map[string]string{"plugin.json": tc.content})
			if err := ValidatePluginInspection(p); (err == nil) != tc.valid {
				t.Fatalf("document admission: %v", err)
			}
		})
	}

	// Share each large string across fixtures; only the limit boundary needs
	// full-sized input. No archive compression, disk or service is involved.
	documents := map[string]string{"plugin.json": "{}"}
	full := strings.Repeat("x", MaxPluginDocumentBytes)
	remaining := MaxCatalogDocumentBytes - 2
	for i := 0; remaining > 0; i++ {
		name := fmt.Sprintf("skills/skill-%02d/SKILL.md", i)
		documents[name] = full[:min(remaining, len(full))]
		remaining -= len(documents[name])
	}
	p := pluginInspectionDocuments(documents)
	if err := ValidatePluginInspection(p); err != nil {
		t.Fatalf("exact aggregate limit rejected: %v", err)
	}
	documents["plugin.json"] += " "
	if err := ValidatePluginInspection(pluginInspectionDocuments(documents)); ErrorCodeOf(err) != CodeValidationFailed {
		t.Fatalf("aggregate limit overflow: %v", err)
	}
}

func TestPluginInspectionStorageSize(t *testing.T) {
	for _, tc := range []struct{ compact, spaced string }{
		{`{"a":[1,2],"b":{}}`, `{"a": [1, 2], "b": {}}`},
		{`{"text":"a,b:c\\\"","next":"\\"}`, `{"text": "a,b:c\\\"", "next": "\\"}`},
		{`{"text":"\u0001\n\t<>&"}`, `{"text": "\u0001\n\t<>&"}`},
	} {
		if got := pluginInspectionStorageSize([]byte(tc.compact)); got != len(tc.spaced) {
			t.Errorf("storage size = %d, want %d", got, len(tc.spaced))
		}
	}
}

func TestPluginEncodedInspectionBudget(t *testing.T) {
	documents := map[string]string{"plugin.json": "{}"}
	for i := range 8 {
		documents[fmt.Sprintf("skills/skill-%d/SKILL.md", i)] = strings.Repeat("\x01", 450_000)
	}
	encodedSize := func(p *PluginInspection) int {
		t.Helper()
		encoded, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return pluginInspectionStorageSize(encoded)
	}
	p := pluginInspectionDocuments(documents)
	excess := encodedSize(p) - MaxPluginInspectionBytes
	if excess <= 0 || p.ExpandedSize > MaxCatalogDocumentBytes {
		t.Fatal("fixture must exceed only the encoded budget")
	}
	if err := ValidatePluginInspection(p); ErrorCodeOf(err) != CodeValidationFailed {
		t.Fatalf("JSON-escaped overflow accepted: %v", err)
	}
	// Replacing a six-byte JSON escape with ASCII preserves raw length while
	// reducing encoded size by five. Finish with at most four ASCII bytes.
	replace := (excess + 4) / 5
	const last = "skills/skill-7/SKILL.md"
	documents[last] = strings.Repeat("x", replace) + documents[last][replace:]
	p = pluginInspectionDocuments(documents)
	documents[last] += strings.Repeat("x", MaxPluginInspectionBytes-encodedSize(p))
	p = pluginInspectionDocuments(documents)
	if encodedSize(p) != MaxPluginInspectionBytes {
		t.Fatal("fixture did not reach the exact encoded limit")
	}
	if err := ValidatePluginInspection(p); err != nil {
		t.Fatalf("exact encoded budget rejected: %v", err)
	}
	documents[last] += "x"
	if err := ValidatePluginInspection(pluginInspectionDocuments(documents)); ErrorCodeOf(err) != CodeValidationFailed {
		t.Fatalf("one-byte encoded overflow accepted: %v", err)
	}
}

func TestPluginRepresentationRejectsConflictingDeliveryMetadata(t *testing.T) {
	for name, mutate := range map[string]func(*Representation){
		"valid":              func(*Representation) {},
		"inline document":    func(r *Representation) { r.Data = json.RawMessage(`{}`) },
		"stored artifact":    func(r *Representation) { r.ArtifactID = "artifact" },
		"Skill inspection":   func(r *Representation) { r.Package = &SkillPackageInspection{} },
		"Skill handoff":      func(r *Representation) { r.Handoff = &SkillHandoff{} },
		"wrong media":        func(r *Representation) { r.MediaType = "application/json" },
		"wrong profile":      func(r *Representation) { r.ProfileID = "other-profile" },
		"unsafe URL":         func(r *Representation) { r.URL = "http://example.com/archive.zip" },
		"invalid checksum":   func(r *Representation) { r.ContentDigest = "bad" },
		"empty archive":      func(r *Representation) { r.Size = 0 },
		"oversized archive":  func(r *Representation) { r.Size = MaxSkillPackageBytes + 1 },
		"expansion overflow": func(r *Representation) { r.Plugin.ExpandedSize = r.Size*MaxSkillPackageExpansionRatio + 1 },
		"missing inspection": func(r *Representation) { r.Plugin = nil },
	} {
		t.Run(name, func(t *testing.T) {
			r := Representation{MediaType: PluginMediaZIP, ProfileID: PluginProfile, URL: "https://example.com/archive.zip", ContentDigest: "sha256:" + strings.Repeat("a", 64), Size: 100, Plugin: pluginInspectionDocuments(map[string]string{"plugin.json": "{}"})}
			mutate(&r)
			err := ValidatePackageRepresentation(r)
			if (err == nil) != (name == "valid") {
				t.Fatalf("representation admission: %v", err)
			}
		})
	}
}

func TestPluginRevisionArchiveBudgetAndEvidenceBinding(t *testing.T) {
	p := pluginInspectionDocuments(map[string]string{"plugin.json": "{}"})
	r := Representation{MediaType: PluginMediaZIP, ProfileID: PluginProfile, URL: "https://example.com/archive.zip", ContentDigest: "sha256:" + strings.Repeat("a", 64), Size: MaxCatalogDocumentBytes / 2, Plugin: p}
	revision := VersionRevision{Representations: []Representation{r, r}}
	var err error
	revision.ContentSetDigest, err = contentSetDigest(revision.Representations)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePackageRevisionDigest(revision); err != nil {
		t.Fatalf("exact archive budget rejected: %v", err)
	}
	revision.Representations[1].Size++
	if err := ValidatePackageRevisionDigest(revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("archive budget overflow: %v", err)
	}
	revision.Representations[1].Size--
	p.Files[0].Executable = true
	if err := ValidatePackageRevisionDigest(revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("changed sealed inventory accepted: %v", err)
	}
}

func TestRepresentationInputDecodingPreservesChecksumPresenceAndStrictness(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, raw      string
		valid, present bool
	}{
		{"omitted", `{"role":"primary","mediaType":"application/agent-plugins+zip","url":"https://example.com/plugin.zip"}`, true, false},
		{"checksum", `{"expectedDigest":"` + digest + `"}`, true, true},
		{"empty is present", `{"expectedDigest":""}`, true, true},
		{"null", `{"expectedDigest":null}`, false, false},
		{"number", `{"expectedDigest":42}`, false, false},
		{"wrong case", `{"Role":"primary"}`, false, false},
		{"null role", `{"role":null}`, false, false},
		{"unknown control field", `{"unexpected":true}`, false, false},
		{"duplicate checksum", `{"expectedDigest":"a","expectedDigest":"b"}`, false, false},
		{"duplicate standard field", `{"data":{"future":1,"future":2}}`, false, false},
		{"trailing input", `{} {}`, false, false},
		{"null representation", `null`, false, false},
		{"array representation", `[]`, false, false},
		{"malformed", `{`, false, false},
		{"future standard field", `{"data":{"future":[null,42]}}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input RepresentationInput
			err := json.Unmarshal([]byte(tc.raw), &input)
			if (err == nil) != tc.valid || (input.ExpectedDigest != nil) != tc.present {
				t.Fatalf("decode: %+v, %v", input, err)
			}
			if tc.name == "checksum" && *input.ExpectedDigest != digest {
				t.Fatal("checksum changed")
			}
			if tc.name == "future standard field" && string(input.Data) != `{"future":[null,42]}` {
				t.Fatal("raw standards data changed")
			}
		})
	}
}
