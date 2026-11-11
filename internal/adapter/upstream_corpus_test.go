// Reviewed upstream definitions exercise native profiles without running any
// resource, fetching a declared URL or importing the fixture's helper code.
package adapter

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

const upstreamRoot = "testdata/upstream"

// upstreamBytes reads inert source data. No source file is imported or executed.
func upstreamBytes(tb testing.TB, name string) []byte {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join(upstreamRoot, filepath.FromSlash(name)))
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}

// upstreamChecksums reads the reviewed oracle, independently of native parsing.
func upstreamChecksums(tb testing.TB) map[string]string {
	tb.Helper()
	out := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(upstreamBytes(tb, "checksums.txt"))))
	for scanner.Scan() {
		sum, name, ok := strings.Cut(scanner.Text(), "  ")
		decoded, err := hex.DecodeString(sum)
		if !ok || err != nil || len(decoded) != sha256.Size || !fs.ValidPath(name) || out[name] != "" {
			tb.Fatalf("invalid or duplicate checksum line %q", scanner.Text())
		}
		out[name] = "sha256:" + sum
	}
	if err := scanner.Err(); err != nil {
		tb.Fatal(err)
	}
	return out
}

// TestReviewedUpstreamSourceInventory detects omitted, added or changed inputs,
// including license text and the explicitly labeled static Agent Card derivation.
func TestReviewedUpstreamSourceInventory(t *testing.T) {
	sums := upstreamChecksums(t)
	if len(sums) != 26 {
		t.Fatalf("source/derivation inventory has %d files, want 26", len(sums))
	}
	seen := map[string]bool{}
	err := filepath.WalkDir(upstreamRoot, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(upstreamRoot, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "README.md" || relative == "checksums.txt" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 != 0 {
			t.Fatalf("%s is not a regular non-executable source file", relative)
		}
		raw := upstreamBytes(t, relative)
		if want, ok := sums[relative]; !ok || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != want {
			t.Errorf("unreviewed source bytes: %s", relative)
		}
		seen[relative] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(sums) {
		t.Fatalf("source file set differs from reviewed checksums: seen=%d expected=%d", len(seen), len(sums))
	}
	for name := range sums {
		if !seen[name] {
			t.Errorf("missing reviewed input %s", name)
		}
	}
}

// upstreamArchive preserves native member bytes and Git executable flags in a
// test-authored container. The expected inventory below does not use this walk.
func upstreamArchive(tb testing.TB, directory, prefix, media string) []byte {
	tb.Helper()
	var entries []testArchiveEntry
	base := filepath.Join(upstreamRoot, filepath.FromSlash(directory))
	err := filepath.WalkDir(base, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		raw, err := fs.ReadFile(os.DirFS(base), filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		entries = append(entries, testArchiveEntry{name: prefix + filepath.ToSlash(rel), content: raw, mode: info.Mode()})
		return nil
	})
	if err != nil {
		tb.Fatal(err)
	}
	if media == MediaSkillGZIP || media == catalog.PluginMediaGZIP {
		return makeGZIP(tb, entries)
	}
	return makeZIP(tb, entries)
}

// TestReviewedUpstreamJupyterSkill validates a complete native tree rather than
// treating a root SKILL.md pass as evidence that referenced assets survived.
func TestReviewedUpstreamJupyterSkill(t *testing.T) {
	sums := upstreamChecksums(t)
	for _, media := range []string{MediaSkillZIP, MediaSkillGZIP} {
		t.Run(media, func(t *testing.T) {
			handoff := &catalog.SkillHandoff{Git: &catalog.GitHandoffReference{Repository: "https://github.com/openai/skills", Commit: "49f948faa9258a0c61caceaf225e179651397431", Directory: "skills/.curated/jupyter-notebook"}}
			result := NewRegistry().Validate(t.Context(), catalog.KindSkill, catalog.RepresentationInput{MediaType: media, PackageData: upstreamArchive(t, "openai/jupyter-notebook", "jupyter-notebook/", media), Handoff: handoff})
			if hasErrors(result.Findings) || result.ProfileID != ProfileAgentSkills || result.Package == nil || !reflect.DeepEqual(result.Handoff, handoff) {
				t.Fatalf("Skill rejected or handoff changed: %+v", result)
			}
			p := result.Package
			if p.StructureState != catalog.PackageAssessmentPassed || p.RootDirectory != "jupyter-notebook" || p.FileCount != 12 || len(p.Files) != 12 || p.ExpandedSize != 30864 || p.Assessments != catalog.NotAssessedSkillPackageAssessments() {
				t.Fatalf("package evidence=%+v", p)
			}
			expected := jupyterInventory()
			for _, file := range p.Files {
				size, ok := expected[strings.TrimPrefix(file.Path, "jupyter-notebook/")]
				if !ok || file.Size != size || file.Executable || file.Digest != sums["openai/"+file.Path] {
					t.Fatalf("unexpected inventory file: %+v", file)
				}
				delete(expected, strings.TrimPrefix(file.Path, "jupyter-notebook/"))
			}
			if len(expected) != 0 {
				t.Fatalf("omitted source members: %v", expected)
			}
			if result.NormalizedFacts["name"] != "jupyter-notebook" || result.NormalizedFacts["description"] != "Use when the user asks to create, scaffold, or edit Jupyter notebooks (`.ipynb`) for experiments, explorations, or tutorials; prefer the bundled templates and run the helper script `new_notebook.py` to generate a clean starting notebook." || !hasFinding(result.Findings, "SKILL_PACKAGE_ASSESSMENTS_NOT_ASSESSED") {
				t.Fatalf("facts/findings=%+v / %+v", result.NormalizedFacts, result.Findings)
			}
		})
	}
	// Independently reviewed local references, including client icon hints and
	// templates loaded by the inert helper. These are not external URL checks.
	for source, refs := range map[string][]string{
		"SKILL.md":                {"scripts/new_notebook.py", "assets/experiment-template.ipynb", "assets/tutorial-template.ipynb", "references/experiment-patterns.md", "references/tutorial-patterns.md", "references/notebook-structure.md", "references/quality-checklist.md"},
		"agents/openai.yaml":      {"assets/jupyter-small.svg", "assets/jupyter.png"},
		"scripts/new_notebook.py": {"experiment-template.ipynb", "tutorial-template.ipynb"},
	} {
		content := string(upstreamBytes(t, "openai/jupyter-notebook/"+source))
		for _, ref := range refs {
			if !strings.Contains(content, ref) {
				t.Errorf("reviewed local reference %s missing from %s", ref, source)
			}
		}
	}
	for _, name := range []string{"experiment", "tutorial"} {
		var notebook struct {
			Format int               `json:"nbformat"`
			Cells  []json.RawMessage `json:"cells"`
		}
		if err := json.Unmarshal(upstreamBytes(t, "openai/jupyter-notebook/assets/"+name+"-template.ipynb"), &notebook); err != nil || notebook.Format != 4 || len(notebook.Cells) == 0 {
			t.Fatalf("notebook asset is not a nonempty v4 document: %+v %v", notebook, err)
		}
	}
}

// jupyterInventory is the reviewed complete upstream tree, with byte sizes
// taken from Git rather than the parser or the local directory walk.
func jupyterInventory() map[string]int64 {
	return map[string]int64{
		"LICENSE.txt": 10776, "SKILL.md": 4154, "agents/openai.yaml": 325,
		"assets/experiment-template.ipynb": 2570, "assets/jupyter-small.svg": 1043, "assets/jupyter.png": 2713, "assets/tutorial-template.ipynb": 2490,
		"references/experiment-patterns.md": 699, "references/notebook-structure.md": 751, "references/quality-checklist.md": 572, "references/tutorial-patterns.md": 685, "scripts/new_notebook.py": 4086,
	}
}

// TestReviewedUpstreamStandaloneSkill protects a real near-limit description
// and Unicode Markdown without turning standalone metadata into a package.
func TestReviewedUpstreamStandaloneSkill(t *testing.T) {
	raw := upstreamBytes(t, "huggingface/hf-cli/SKILL.md")
	r := NewRegistry().Validate(t.Context(), catalog.KindSkill, catalog.RepresentationInput{MediaType: MediaSkillMarkdownARD, Content: string(raw)})
	description, ok := r.NormalizedFacts["description"].(string)
	if hasErrors(r.Findings) || r.ProfileID != ProfileAgentSkills || r.NormalizedFacts["name"] != "hf-cli" || !ok || utf8.RuneCountInString(description) != 1008 || fmt.Sprintf("%x", sha256.Sum256([]byte(description))) != "3d8b9cc5f7424603ccb447f38ace1f943cc2ac5692c50c648eb57f72034c238d" || r.Package != nil || r.Handoff != nil {
		t.Fatalf("standalone Skill=%+v", r)
	}
	if !utf8.Valid(raw) || !strings.Contains(string(raw), "—") {
		t.Fatal("reviewed Unicode Markdown was lost")
	}
}

// TestReviewedUpstreamResearcherCard checks declared capabilities and every
// producer field through the pinned ProtoJSON binding and semantic validator.
func TestReviewedUpstreamResearcherCard(t *testing.T) {
	raw := upstreamBytes(t, "a2a-dotnet/researcher-card.json")
	result := NewRegistry().Validate(t.Context(), catalog.KindAgent, catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: raw})
	if len(result.Findings) != 0 || result.ProfileID != ProfileA2AAgentCard || !reflect.DeepEqual(result.NormalizedFacts, map[string]any{"name": "Researcher Agent", "description": "Agent which conducts research.", "version": "1.0.0", "interfaces": 1}) {
		t.Fatalf("Agent=%+v", result)
	}
	card, findings := validateA2AAgentCard(raw, catalog.DefaultDiagnosticLimits())
	if len(findings) != 0 || card == nil {
		t.Fatalf("ProtoJSON=%+v", findings)
	}
	if !reflect.DeepEqual(card.GetDefaultInputModes(), []string{"text/plain"}) || !reflect.DeepEqual(card.GetDefaultOutputModes(), []string{"text/plain"}) || card.GetCapabilities() == nil || !card.GetCapabilities().GetStreaming() || card.GetCapabilities().GetPushNotifications() {
		t.Fatal("declared modes/capabilities changed")
	}
	interfaces := card.GetSupportedInterfaces()
	if len(interfaces) != 1 || interfaces[0].GetUrl() != "https://agents.example.com/researcher" || interfaces[0].GetProtocolBinding() != "JSONRPC" || interfaces[0].GetProtocolVersion() != "1.0" {
		t.Fatalf("interfaces=%+v", interfaces)
	}
	skills := card.GetSkills()
	if len(skills) != 1 || skills[0].GetId() != "research" || skills[0].GetName() != "Research" || skills[0].GetDescription() != "Conducts research on a given topic." || !reflect.DeepEqual(skills[0].GetTags(), []string{"research", "planning"}) {
		t.Fatalf("skills=%+v", skills)
	}
}

// TestReviewedUpstreamMCPDistributions keeps Registry packages, remote URLs and
// secret-input descriptors distinct from runtime credentials and Server Cards.
func TestReviewedUpstreamMCPDistributions(t *testing.T) {
	for _, test := range []struct {
		directory, name, version, packages, remotes string
		remoteCount                                 int
	}{
		{"playwright", "io.github.microsoft/playwright-mcp", "0.0.85", `[{"registryType":"npm","identifier":"@playwright/mcp","version":"0.0.85","transport":{"type":"stdio"}}]`, `null`, 0},
		{"context7", "io.github.upstash/context7", "4.0.3", `[{"registryType":"npm","identifier":"@upstash/context7-mcp","version":"4.0.3","transport":{"type":"stdio"},"environmentVariables":[{"name":"CONTEXT7_API_KEY","description":"API key for authentication","isRequired":false,"isSecret":true}]}]`, `[{"type":"streamable-http","url":"https://mcp.context7.com/mcp","headers":[{"name":"Authorization","description":"API key for authentication. Accepts \"Bearer <key>\" or the raw key.","isRequired":false,"isSecret":true}]}]`, 1},
	} {
		t.Run(test.directory, func(t *testing.T) {
			raw := upstreamBytes(t, test.directory+"/server.json")
			if err := standards.ValidateMCPRegistryServer(raw); err != nil {
				t.Fatal(err)
			}
			r := NewRegistry().Validate(t.Context(), catalog.KindMCPServer, catalog.RepresentationInput{MediaType: MediaMCPRegistry, Data: raw})
			if hasErrors(r.Findings) || r.ProfileID != ProfileMCPRegistry || !hasFinding(r.Findings, "MCP_REGISTRY_NOT_SERVER_CARD") || !reflect.DeepEqual(r.NormalizedFacts, map[string]any{"name": test.name, "version": test.version, "packages": 1, "remotes": test.remoteCount}) {
				t.Fatalf("Registry=%+v", r)
			}
			var document map[string]json.RawMessage
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{"packages": test.packages, "remotes": test.remotes} {
				var expected, actual any
				if err := json.Unmarshal([]byte(want), &expected); err != nil {
					t.Fatal(err)
				}
				if len(document[key]) != 0 {
					if err := json.Unmarshal(document[key], &actual); err != nil {
						t.Fatal(err)
					}
				}
				if !reflect.DeepEqual(actual, expected) {
					t.Fatalf("%s changed: %s", key, document[key])
				}
			}
			// The very same valid distribution bytes are not a Server Card.
			wrong := NewRegistry().Validate(t.Context(), catalog.KindMCPServer, catalog.RepresentationInput{MediaType: MediaMCPServerCard, Data: raw})
			schemaError := false
			for _, finding := range wrong.Findings {
				if finding.Code == "MCP_SERVER_CARD_SCHEMA_INVALID" && finding.Severity == catalog.SeverityError {
					schemaError = true
				}
			}
			if wrong.ProfileID != ProfileMCPServerCard || !schemaError {
				t.Fatalf("Registry relabeled as Server Card: %+v", wrong)
			}
		})
	}
}

// TestReviewedUpstreamContext7Plugin binds both portable components and every
// native file to their source without resolving the declared OAuth URL.
func TestReviewedUpstreamContext7Plugin(t *testing.T) {
	sums := upstreamChecksums(t)
	for _, media := range []string{catalog.PluginMediaZIP, catalog.PluginMediaGZIP} {
		t.Run(media, func(t *testing.T) {
			r := NewRegistry().Validate(t.Context(), catalog.KindPlugin, catalog.RepresentationInput{MediaType: media, PackageData: upstreamArchive(t, "context7/plugin", "", media)})
			if len(r.Findings) != 0 || r.ProfileID != catalog.PluginProfile || r.Plugin == nil || !reflect.DeepEqual(r.NormalizedFacts, map[string]any{"name": "context7", "version": "1.0.2", "description": "Up-to-date documentation lookup. Pull version-specific documentation and code examples directly from source repositories into your coding agent's context."}) {
				t.Fatalf("Plugin=%+v", r)
			}
			p := r.Plugin
			expected := map[string]int64{"LICENSE": 1079, "README.md": 4251, "mcp.json": 205, "plugin.json": 589, "skills/context7-mcp/SKILL.md": 4570}
			if p.RootDirectory != "" || p.ExpandedSize != 10694 || len(p.Files) != 5 || p.Assessments != catalog.NotAssessedSkillPackageAssessments() {
				t.Fatalf("Plugin evidence=%+v", p)
			}
			for _, file := range p.Files {
				size, ok := expected[file.Path]
				if !ok || file.Size != size || file.Executable || file.Digest != sums["context7/plugin/"+file.Path] {
					t.Fatalf("unexpected native file: %+v", file)
				}
				delete(expected, file.Path)
			}
			if len(expected) != 0 {
				t.Fatalf("omitted Plugin files: %v", expected)
			}
			want := []catalog.PluginComponent{{Kind: "mcp_server", Name: "context7", Path: "mcp.json"}, {Kind: "skill", Name: "context7-mcp", Path: "skills/context7-mcp/SKILL.md"}}
			if len(p.Components) != len(want) || len(p.Documents) != 3 {
				t.Fatalf("components=%+v, retained document count=%d", p.Components, len(p.Documents))
			}
			for index, component := range p.Components {
				if component.Kind != want[index].Kind || component.Name != want[index].Name || component.Path != want[index].Path || len(component.Findings) != 0 {
					t.Fatalf("unexpected component: %+v", component)
				}
			}
			documents := map[string]bool{"plugin.json": true, "mcp.json": true, "skills/context7-mcp/SKILL.md": true}
			for _, doc := range p.Documents {
				if !documents[doc.Path] || doc.Content != string(upstreamBytes(t, "context7/plugin/"+doc.Path)) {
					t.Fatalf("retained document changed: %s", doc.Path)
				}
				delete(documents, doc.Path)
			}
			if len(documents) != 0 {
				t.Fatalf("missing retained Plugin documents: %v", documents)
			}
			if err := standards.ValidatePluginManifest(upstreamBytes(t, "context7/plugin/plugin.json")); err != nil {
				t.Fatal(err)
			}
			if err := standards.ValidatePluginMCP(upstreamBytes(t, "context7/plugin/mcp.json")); err != nil {
				t.Fatal(err)
			}
			var config struct {
				Servers map[string]struct {
					Type string `json:"type"`
					URL  string `json:"url"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal(upstreamBytes(t, "context7/plugin/mcp.json"), &config); err != nil || len(config.Servers) != 1 || config.Servers["context7"].Type != "streamable-http" || config.Servers["context7"].URL != "https://mcp.context7.com/mcp/oauth" {
				t.Fatalf("native MCP declaration=%+v %v", config, err)
			}
		})
	}
}
