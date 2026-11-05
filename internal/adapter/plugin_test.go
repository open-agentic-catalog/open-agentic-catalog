package adapter

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

const pluginManifest = `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"acme.tools"}`

func TestPluginRejectsUnstorableDecodedMetadata(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"component name": {"plugin.json": pluginManifest, "mcp.json": `{"mcpServers":{"bad\u0000name":{"type":"stdio","command":"server"}}}`},
		"description":    {"plugin.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"acme.tools","description":"bad\u0000description"}`},
	} {
		t.Run(name, func(t *testing.T) {
			result := InspectPlugin(context.Background(), catalog.PluginMediaZIP, pluginArchive(t, catalog.PluginMediaZIP, files), catalog.DefaultDiagnosticLimits())
			if result.Plugin != nil || len(result.Findings) == 0 {
				t.Fatalf("decoded NUL reached retainable metadata: %#v", result)
			}
		})
	}
	manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"acme.tools","description":"literal \\u0000"}`
	result := InspectPlugin(context.Background(), catalog.PluginMediaZIP, pluginArchive(t, catalog.PluginMediaZIP, map[string]string{"plugin.json": manifest}), catalog.DefaultDiagnosticLimits())
	if result.Plugin == nil || len(result.Findings) != 0 || result.NormalizedFacts["description"] != `literal \u0000` {
		t.Fatalf("literal escape notation was rejected: %#v", result)
	}
}

func pluginArchive(tb testing.TB, media string, files map[string]string) []byte {
	tb.Helper()
	var out bytes.Buffer
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if media == catalog.PluginMediaZIP {
		w := zip.NewWriter(&out)
		for _, k := range keys {
			f, err := w.Create(k)
			if err != nil {
				tb.Fatal(err)
			}
			if _, err = f.Write([]byte(files[k])); err != nil {
				tb.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			tb.Fatal(err)
		}
	} else {
		g := gzip.NewWriter(&out)
		w := tar.NewWriter(g)
		for _, k := range keys {
			h := &tar.Header{Name: k, Size: int64(len(files[k])), Mode: 0600}
			if strings.HasSuffix(k, "/") {
				h.Typeflag = tar.TypeDir
			}
			if err := w.WriteHeader(h); err != nil {
				tb.Fatal(err)
			}
			if _, err := w.Write([]byte(files[k])); err != nil {
				tb.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			tb.Fatal(err)
		}
		if err := g.Close(); err != nil {
			tb.Fatal(err)
		}
	}
	return out.Bytes()
}

// FuzzPluginArchiveValidation checks both archive formats and the retained
// evidence contract. Revalidating identical evidence must preserve admission
// and must never mutate the immutable inspection supplied by the caller.
func FuzzPluginArchiveValidation(f *testing.F) {
	for _, media := range []string{catalog.PluginMediaZIP, catalog.PluginMediaGZIP} {
		for _, prefix := range []string{"", "wrapper/"} {
			f.Add(media == catalog.PluginMediaGZIP, pluginArchive(f, media, map[string]string{
				prefix + "plugin.json":          pluginManifest,
				prefix + "mcp.json":             `{"$schema":"` + standards.PluginMCPSchema + `","mcpServers":{"local":{"type":"stdio","command":"node"}}}`,
				prefix + "skills/read/SKILL.md": "---\nname: read\ndescription: Read a document.\n---\nInstructions.",
			}))
			f.Add(media == catalog.PluginMediaGZIP, pluginArchive(f, media, map[string]string{prefix + "plugin.json": "{}"}))
		}
	}
	f.Add(false, []byte("not an archive"))
	f.Fuzz(func(t *testing.T, compressedTar bool, body []byte) {
		media := catalog.PluginMediaZIP
		if compressedTar {
			media = catalog.PluginMediaGZIP
		}
		r := InspectPlugin(t.Context(), media, body, catalog.DefaultDiagnosticLimits())
		if r.Plugin == nil {
			if len(r.Findings) == 0 {
				t.Fatal("rejected archive has no finding")
			}
			return
		}
		if err := catalog.ValidatePluginInspection(r.Plugin); err != nil {
			t.Fatalf("retained inspection violates its contract: %v", err)
		}
		before, err := json.Marshal(r.Plugin)
		if err != nil {
			t.Fatal(err)
		}
		again := ValidatePluginInspection(t.Context(), r.Plugin, catalog.DefaultDiagnosticLimits())
		after, err := json.Marshal(r.Plugin)
		if err != nil || !bytes.Equal(before, after) || !reflect.DeepEqual(r, again) {
			t.Fatal("retained revalidation changed evidence or admission")
		}
	})
}

func TestPluginArchiveRoundTrip(t *testing.T) {
	for _, media := range []string{catalog.PluginMediaZIP, catalog.PluginMediaGZIP} {
		for _, prefix := range []string{"", "different-wrapper/"} {
			t.Run(media+prefix, func(t *testing.T) {
				body := pluginArchive(t, media, map[string]string{prefix + "plugin.json": pluginManifest, prefix + "skills/summarize/SKILL.md": "---\nname: summarize\ndescription: Summarize safely.\n---\nInstructions.", prefix + "mcp.json": `{"$schema":"` + standards.PluginMCPSchema + `","mcpServers":{"local":{"type":"stdio","command":"node","cwd":"${PLUGIN_DATA}"}}}`, prefix + "bin/unused.sh": "exit 1", prefix + "skills/summarize/assets/example.txt": "opaque asset"})
				r := InspectPlugin(context.Background(), media, body, catalog.DefaultDiagnosticLimits())
				if r.Plugin == nil || len(r.Findings) != 0 || len(r.Plugin.Components) != 2 || len(r.Plugin.Files) != 5 || len(r.Plugin.Documents) != 3 {
					t.Fatalf("inspection=%+v findings=%+v", r.Plugin, r.Findings)
				}
				before, _ := json.Marshal(r.Plugin)
				again := ValidatePluginInspection(context.Background(), r.Plugin, catalog.DefaultDiagnosticLimits())
				after, _ := json.Marshal(r.Plugin)
				if len(again.Findings) != 0 || !bytes.Equal(before, after) {
					t.Fatal("retained validation drifted or mutated input")
				}
				r.Plugin.Documents[0].Content += "changed"
				if ValidatePluginInspection(context.Background(), r.Plugin, catalog.DefaultDiagnosticLimits()).Plugin != nil {
					t.Fatal("tampered bytes accepted")
				}
			})
		}
	}
}

func TestPluginArchiveInvalidAndMinimal(t *testing.T) {
	for _, tc := range []struct {
		name            string
		files           map[string]string
		retained, valid bool
	}{
		{"minimal", map[string]string{"plugin.json": pluginManifest}, true, true},
		{"opaque nested files", map[string]string{"plugin.json": pluginManifest, "extension/mcp.json": "\x00binary", "extension/SKILL.md": "\x00binary", "extension/plugin.json": "unparsed"}, true, true},
		{"empty skill", map[string]string{"plugin.json": pluginManifest, "skills/empty/SKILL.md": ""}, true, false},
		{"wrong location", map[string]string{"plugin.json": pluginManifest, "mcp.json/": ""}, true, false},
		{"wrong skills", map[string]string{"plugin.json": pluginManifest, "skills": "x"}, true, false},
		{"duplicate JSON", map[string]string{"plugin.json": `{"name":"a","name":"b"}`}, true, false},
		{"missing manifest", map[string]string{"README": "hello"}, false, false},
		{"manifest directory", map[string]string{"plugin.json/": ""}, false, false},
		{"wrapper with outside file", map[string]string{"wrapper/plugin.json": pluginManifest, "outside.txt": "outside"}, false, false},
		{"invalid document encoding", map[string]string{"plugin.json": "\xff"}, false, false},
		{"duplicate MCP JSON", map[string]string{"plugin.json": pluginManifest, "mcp.json": `{"mcpServers":{},"mcpServers":{}}`}, true, false},
		{"non-object MCP JSON", map[string]string{"plugin.json": pluginManifest, "mcp.json": `[]`}, true, false},
		{"empty components", map[string]string{"plugin.json": pluginManifest, "mcp.json": `{"$schema":"` + standards.PluginMCPSchema + `","mcpServers":{}}`}, true, true},
		{"traversal", map[string]string{"plugin.json": pluginManifest, "../bad": "x"}, false, false},
		{"two roots", map[string]string{"a/plugin.json": pluginManifest, "b/plugin.json": pluginManifest}, false, false},
		{"case collision", map[string]string{"plugin.json": pluginManifest, "PLUGIN.json": pluginManifest}, false, false},
		{"invalid namespace", map[string]string{"plugin.json": `{"$schema":"` + standards.PluginManifestSchema + `","name":"a","extensions":{"bad":{}}}`}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := InspectPlugin(context.Background(), catalog.PluginMediaZIP, pluginArchive(t, catalog.PluginMediaZIP, tc.files), catalog.DefaultDiagnosticLimits())
			if (r.Plugin != nil) != tc.retained || (len(r.Findings) == 0) != tc.valid {
				t.Fatalf("result=%+v", r)
			}
			if r.Plugin != nil {
				again := ValidatePluginInspection(context.Background(), r.Plugin, catalog.DefaultDiagnosticLimits())
				if (len(again.Findings) == 0) != tc.valid {
					t.Fatal("retained validation changed admission")
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := InspectPlugin(ctx, catalog.PluginMediaZIP, pluginArchive(t, catalog.PluginMediaZIP, map[string]string{"plugin.json": pluginManifest}), catalog.DefaultDiagnosticLimits()); r.Plugin != nil {
		t.Fatal("cancelled inspection accepted")
	}
}

func TestPluginMCPProseValidation(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{"type":"stdio","command":"node","args":["../../opaque"],"env":{"X":"${UNKNOWN}"}}`, true},
		{`{"type":"stdio","command":"./bin/server","cwd":"./"}`, true},
		{`{"type":"stdio","command":"node","cwd":"${PLUGIN_ROOT}/data"}`, true},
		{`{"type":"stdio","command":"node","cwd":"./data/../cache"}`, true},
		{`{"type":"stdio","command":"node","cwd":"${PLUGIN_DATA}/state"}`, true},
		{`{"type":"stdio","command":"node","cwd":"${PLUGIN_ROOT}/state/${UNKNOWN}"}`, false},
		{`{"type":"stdio","command":"node","cwd":"/tmp"}`, false},
		{`{"type":"stdio","command":"node","cwd":"data"}`, false},
		{`{"type":"stdio","command":"node","cwd":"./C:drive"}`, false},
		{`{"type":"stdio","command":"node","env":{"PLUGIN_ROOT":"override"}}`, false},
		{`{"type":"stdio","command":"node","env":{"PLUGIN_DATA":"override"}}`, false},
		{`{"type":"stdio","command":"../escape"}`, false},
		{`{"type":"stdio","command":"node -e code"}`, false},
		{`{"type":"stdio","command":"node","cwd":"${PLUGIN_DATA}/../escape"}`, false},
		{`{"type":"streamable-http","url":"http://localhost:8000/mcp"}`, true},
		{`{"type":"streamable-http","url":"http://[::1]:8000/mcp"}`, true},
		{`{"type":"streamable-http","url":"http://example.com/mcp"}`, false},
		{`{"type":"sse","url":"https://example.com/mcp","headers":{"X-A":"1","x-a":"2"}}`, false},
		{`{"type":"sse","url":"https://example.com/mcp","headers":{"Authorization":"example"}}`, false},
		{`{"type":"sse","url":"https://user@example.com/mcp"}`, false},
		{`{"type":"sse","url":"https://example.com/mcp","headers":{"X-Mode":"read-only"}}`, true},
		{`{"type":"sse","url":"https://example.com/mcp","headers":{"X Mode":"read-only"}}`, false},
		{`{"type":"sse","url":"https://example.com/mcp","headers":{"X-Mode":"read\r\ninjected: true"}}`, false},
		{`{"type":"stdio","command":42}`, false},
		{`{"type":"future"}`, false},
	} {
		if got := len(validatePluginServer([]byte(tc.raw))) == 0; got != tc.valid {
			t.Errorf("%s valid=%v", tc.raw, got)
		}
	}
}

func TestPluginExtensionNamespaces(t *testing.T) {
	for _, name := range []string{"com.example", "org.example.tools", "io.vendor-2.client", strings.Repeat("a", 63) + ".example"} {
		if !pluginNamespace(name) {
			t.Errorf("valid namespace rejected: %q", name)
		}
	}
	for _, name := range []string{"", "single", ".example", "com.", "com..example", "com.-example", "com.example-", "Com.example", "com.ex_ample", "com.éxample", strings.Repeat("a", 64) + ".example", strings.Repeat("a.", 127) + "a"} {
		if pluginNamespace(name) {
			t.Errorf("invalid namespace accepted: %q", name)
		}
	}
}

func TestPluginComponentAndFindingBounds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		servers       int
		skill, broken bool
		wantCode      string
	}{
		{"at limit", catalog.MaxPluginComponents, false, false, ""},
		{"MCP overflow", catalog.MaxPluginComponents + 1, false, false, "PLUGIN_COMPONENT_LIMIT"},
		{"combined overflow", catalog.MaxPluginComponents, true, false, "PLUGIN_COMPONENT_LIMIT"},
		{"bounded findings", catalog.MaxPluginComponents, false, true, "PLUGIN_MCP_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			servers := make(map[string]any, tc.servers)
			for i := range tc.servers {
				server := map[string]any{"type": "stdio", "command": "node"}
				if tc.broken {
					server["command"] = "../escape"
					server["unknown"] = true
				}
				servers[fmt.Sprintf("server-%03d", i)] = server
			}
			body, err := json.Marshal(map[string]any{"$schema": standards.PluginMCPSchema, "mcpServers": servers})
			if err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"plugin.json": pluginManifest, "mcp.json": string(body)}
			if tc.skill {
				files["skills/read/SKILL.md"] = "---\nname: read\ndescription: Read a document.\n---\nInstructions."
			}
			r := InspectPlugin(t.Context(), catalog.PluginMediaZIP, pluginArchive(t, catalog.PluginMediaZIP, files), catalog.DefaultDiagnosticLimits())
			if r.Plugin == nil || len(r.Plugin.Components) > catalog.MaxPluginComponents || len(r.Findings) > catalog.MaxPluginComponents {
				t.Fatal("inspection or findings exceeded their bounds")
			}
			if tc.wantCode == "" {
				if len(r.Findings) != 0 || len(r.Plugin.Components) != catalog.MaxPluginComponents {
					t.Fatal("maximum valid component set was not admitted")
				}
			} else if len(r.Findings) == 0 || r.Findings[0].Code != tc.wantCode {
				t.Fatalf("findings = %v, want %s", r.Findings, tc.wantCode)
			}
			again := ValidatePluginInspection(t.Context(), r.Plugin, catalog.DefaultDiagnosticLimits())
			if !reflect.DeepEqual(r, again) {
				t.Fatal("bounded inspection changed on retained revalidation")
			}
		})
	}
}

func TestPluginRetainedValidationHonorsCancellation(t *testing.T) {
	r := InspectPlugin(t.Context(), catalog.PluginMediaZIP, pluginArchive(t, catalog.PluginMediaZIP, map[string]string{"plugin.json": pluginManifest}), catalog.DefaultDiagnosticLimits())
	if r.Plugin == nil {
		t.Fatal("fixture inspection failed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	again := ValidatePluginInspection(ctx, r.Plugin, catalog.DefaultDiagnosticLimits())
	if len(again.Findings) != 1 || again.Findings[0].Code != "PLUGIN_CANCELLED" {
		t.Fatalf("cancellation findings = %v", again.Findings)
	}
}

func TestPluginArchiveAdmissionBounds(t *testing.T) {
	for _, tc := range []struct {
		name, media string
		body        []byte
	}{
		{"missing bytes", catalog.PluginMediaZIP, nil},
		{"wrong format", "application/json", []byte(`{}`)},
		{"compressed byte limit", catalog.PluginMediaGZIP, make([]byte, catalog.MaxSkillPackageBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := InspectPlugin(t.Context(), tc.media, tc.body, catalog.DefaultDiagnosticLimits())
			if r.Plugin != nil || len(r.Findings) != 1 || r.Findings[0].Code != "PLUGIN_ARCHIVE_LIMIT" {
				t.Fatalf("archive admission: %v", r.Findings)
			}
		})
	}

	// Store rather than deflate this entry so the document ceiling, not the
	// independent expansion-ratio guard, is the rejection being exercised.
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	entry, err := w.CreateHeader(&zip.FileHeader{Name: "plugin.json", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(bytes.Repeat([]byte("x"), catalog.MaxPluginDocumentBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r := InspectPlugin(t.Context(), catalog.PluginMediaZIP, archive.Bytes(), catalog.DefaultDiagnosticLimits())
	if r.Plugin != nil || len(r.Findings) != 1 || r.Findings[0].Code != "PLUGIN_DOCUMENT_INVALID" {
		t.Fatalf("document limit did not fail before retention: %v", r.Findings)
	}
}

func TestPluginMCPFindingsIdentifyTheInvalidComponent(t *testing.T) {
	body := pluginArchive(t, catalog.PluginMediaZIP, map[string]string{
		"plugin.json": pluginManifest,
		"mcp.json":    `{"$schema":"` + standards.PluginMCPSchema + `","mcpServers":{"good":{"type":"stdio","command":"node"},"invalid":{"type":"stdio","command":"node","url":"https://example.com"}}}`,
	})
	r := InspectPlugin(t.Context(), catalog.PluginMediaZIP, body, catalog.DefaultDiagnosticLimits())
	if r.Plugin == nil || len(r.Findings) == 0 || len(r.Plugin.Components) != 2 || len(r.Plugin.Components[0].Findings) != 0 || len(r.Plugin.Components[1].Findings) == 0 {
		t.Fatalf("component attribution: %+v", r)
	}
}
