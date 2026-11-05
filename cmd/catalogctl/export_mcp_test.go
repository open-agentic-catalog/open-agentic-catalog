package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type mcpExportFixture struct {
	Name           string                     `json:"name"`
	Profile        string                     `json:"profile"`
	MediaType      string                     `json:"mediaType"`
	ReleaseID      string                     `json:"releaseId"`
	Remote         int                        `json:"remote"`
	Document       json.RawMessage            `json:"document"`
	Expected       map[string]json.RawMessage `json:"expected"`
	OmittedHeaders []exportMCPHeader          `json:"omittedHeaders"`
}

// This file is also the data contract for the batch 2 browser projection tests.
func mcpExportFixtures(t *testing.T) []mcpExportFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/mcp-client-export/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []mcpExportFixture
	if json.Unmarshal(raw, &cases) != nil || len(cases) != 4 {
		t.Fatal("invalid shared golden fixtures")
	}
	return cases
}

func TestMCPExportSharedFixtures(t *testing.T) {
	for _, c := range mcpExportFixtures(t) {
		for format, expected := range c.Expected {
			t.Run(c.Name+"/"+format, func(t *testing.T) {
				raw, notes, err := projectMCPExport(c.Document, c.Profile, c.MediaType, c.ReleaseID, format, 0)
				if err != nil {
					t.Fatal(err)
				}
				var got, want any
				if json.Unmarshal(raw, &got) != nil || json.Unmarshal(expected, &want) != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("output differs from client golden: %s", raw)
				}
				if notes.ClientFormat != format || notes.Remote != 1 || !reflect.DeepEqual(notes.OmittedHeaders, c.OmittedHeaders) || len(notes.Setup) == 0 {
					t.Fatalf("lost private setup notes: %+v", notes)
				}
				all, _ := json.Marshal(notes)
				if bytes.Contains(raw, []byte("PRIVATE_")) || bytes.Contains(all, []byte("PRIVATE_")) {
					t.Fatal("copied a secret or free-form description")
				}
			})
		}
	}
}

// The positive golden documents must also pass the existing pinned validators.
// These validators use embedded inputs and make no network calls.
func TestMCPExportFixtureNativeValidity(t *testing.T) {
	for _, c := range mcpExportFixtures(t) {
		t.Run(c.Name, func(t *testing.T) {
			result := adapter.NewRegistry().Validate(t.Context(), catalog.KindMCPServer, catalog.RepresentationInput{Role: catalog.RepresentationPrimary, MediaType: c.MediaType, Data: c.Document})
			if result.ProfileID != c.Profile {
				t.Fatalf("wrong pinned profile: %s", result.ProfileID)
			}
			for _, finding := range result.Findings {
				if finding.Severity == catalog.SeverityError {
					t.Fatalf("invalid positive native fixture: %+v", finding)
				}
			}
		})
	}
}

func TestMCPExportManyRemotes(t *testing.T) {
	c := mcpExportFixtures(t)[0]
	remotes := make([]map[string]string, 18)
	for i := range remotes {
		remotes[i] = map[string]string{"type": "sse", "url": "https://mcp.test/PRIVATE_ENDPOINT"}
	}
	remotes[0]["type"] = "PRIVATE_TRANSPORT"
	remotes[16]["url"] = "https://mcp.test/selected?route=a%2Fb"
	raw, err := json.Marshal(map[string]any{"remotes": remotes})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = projectMCPExport(raw, c.Profile, c.MediaType, c.ReleaseID, "mcp-vscode", 0)
	if err == nil {
		t.Fatal("accepted ambiguous remote selection")
	}
	if len(err.Error()) > 768 || strings.Contains(err.Error(), "PRIVATE") || !strings.Contains(err.Error(), "1 (unsupported)") || !strings.Contains(err.Error(), "additional entries omitted") {
		t.Fatalf("unbounded or unsafe selection diagnostic: %v", err)
	}
	output, notes, err := projectMCPExport(raw, c.Profile, c.MediaType, c.ReleaseID, "mcp-vscode", 17)
	if err != nil || notes.Remote != 17 {
		t.Fatalf("diagnostic limit truncated remote selection: %+v %v", notes, err)
	}
	var config map[string]map[string]map[string]string
	if json.Unmarshal(output, &config) != nil || config["servers"]["catalog-"+c.ReleaseID]["url"] != remotes[16]["url"] {
		t.Fatalf("lost original remote position: %s", output)
	}
}

func TestMCPExportURLs(t *testing.T) {
	for _, value := range []string{"https://mcp.example.test/mcp", "https://mcp.example.test:443/mcp?x=a%2Fb&y=one+two", "https://[::1]/mcp"} {
		if !mcpExportURL(value) {
			t.Errorf("refused literal URL %q", value)
		}
	}
	for _, value := range []string{"", "http://mcp.test/mcp", "https:mcp", "https:///mcp", "https://user:password@mcp.test/mcp", "https://mcp.test/mcp#", "https://mcp.test/mcp#fragment", "https://mcp.test/has space", "https://mcp.test/has\nnewline", "https://mcp.test/has\u00a0space", "https://mcp.test/\\path", "https://mcp.test/{path}", "https://mcp.test/${TOKEN}", "https://mcp.test/${TOKEN:-default}", "https://mcp.test/${env:TOKEN}", "https://mcp.test/${input:token}", "https://mcp.test/${command:run}", "https://mcp.test/${workspaceFolder}", "https://mcp.test/${userHome}", "https://mcp.test/${/}", "https://mcp.test:0/mcp", "https://mcp.test:65536/mcp", "https://mcp.test:/mcp", "https://mcp.test:bad/mcp", "https://mcp.test/%zz", "https://mcp.test/?x=%zz"} {
		if mcpExportURL(value) {
			t.Errorf("accepted unsafe URL %q", value)
		}
	}
}

func TestMCPExportSelectionAndParsing(t *testing.T) {
	c := mcpExportFixtures(t)[0]
	multi := []byte(`{"remotes":[{"type":"stdio","url":"PRIVATE_URL"},{"type":"sse","url":"https://mcp.test/sse"}]}`)
	for _, position := range []int{0, -1, 3} {
		_, _, err := projectMCPExport(multi, c.Profile, c.MediaType, c.ReleaseID, "mcp-vscode", position)
		if err == nil || !strings.Contains(err.Error(), "1 (unsupported), 2 (sse)") || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatalf("unsafe/missing selection diagnostic: %v", err)
		}
	}
	if _, _, err := projectMCPExport(multi, c.Profile, c.MediaType, c.ReleaseID, "mcp-vscode", 1); err == nil {
		t.Fatal("unsupported selected remote accepted")
	}
	raw, notes, err := projectMCPExport(multi, c.Profile, c.MediaType, c.ReleaseID, "mcp-vscode", 2)
	if err != nil || notes.Remote != 2 || !bytes.Contains(raw, []byte(`"type": "sse"`)) {
		t.Fatalf("original remote position changed: %s %v", raw, err)
	}
	invalid := []string{
		`null`, `[]`, `{}`, `{"remotes":null}`, `{"remotes":{}}`, `{"remotes":[null]}`, `{"remotes":[{"type":"sse","url":"https://mcp.test","url":"https://other.test"}]}`,
		`{"remotes":[{"type":"sse","URL":"https://mcp.test"}]}`, `{"remotes":[{"type":"sse","url":7}]}`, `{"remotes":[{"type":"sse","url":"https://mcp.test"}]} {}`,
		`{"remotes":[{"type":"sse","url":"https://mcp.test/\ud800"}]}`,
	}
	for _, doc := range invalid {
		if _, _, err := projectMCPExport([]byte(doc), c.Profile, c.MediaType, c.ReleaseID, "mcp-vscode", 0); err == nil {
			t.Errorf("accepted malformed metadata: %s", doc)
		}
	}
	if _, _, err := projectMCPExport([]byte("{\"remotes\":[{\"type\":\"sse\",\"url\":\"https://mcp.test/\xff\"}]}"), c.Profile, c.MediaType, c.ReleaseID, "mcp-vscode", 0); err == nil {
		t.Fatal("accepted malformed UTF-8")
	}
	for _, change := range []string{"profile", "media", "release", "format", "card schema"} {
		profile, media, release, format := c.Profile, c.MediaType, c.ReleaseID, "mcp-vscode"
		switch change {
		case "profile":
			profile = adapter.ProfilePrivateMCP
		case "media":
			media = adapter.MediaMCPServerCard
		case "release":
			release = "org/server"
		case "format":
			format = "native"
		case "card schema":
			profile = adapter.ProfileMCPServerCard
			media = adapter.MediaMCPServerCard
		}
		if _, _, err := projectMCPExport(multi, profile, media, release, format, 2); err == nil {
			t.Errorf("accepted %s mismatch", change)
		}
	}
}

func TestMCPExportHeaderDescriptors(t *testing.T) {
	for _, raw := range []string{`null`, ` null `, `{}`, `[null]`, `[{"name":""}]`, `[{"name":"X-Test\nLeak"}]`, `[{"name":"X-Test"},{"name":"x-test"}]`, `[{"name":"X","isRequired":null}]`, `[{"name":"X","isRequired":"true"}]`} {
		if _, err := mcpExportHeaders(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted invalid headers: %s", raw)
		}
		doc := []byte(`{"remotes":[{"type":"sse","url":"https://mcp.test","headers":` + raw + `}]}`)
		if _, _, err := projectMCPExport(doc, adapter.ProfileMCPRegistry, adapter.MediaMCPRegistry, exportReleaseID, "mcp-vscode", 0); err == nil || !strings.Contains(err.Error(), "invalid header descriptors") {
			t.Fatalf("projection ignored invalid headers: %v", err)
		}
	}
	got, err := mcpExportHeaders(json.RawMessage(`[{"name":"X-Optional","value":"PRIVATE","variables":{"token":{"default":"PRIVATE"}}}]`))
	if err != nil || len(got) != 1 || got[0].Required {
		t.Fatalf("optional header: %+v %v", got, err)
	}
}

func TestMCPExportFlags(t *testing.T) {
	base := []string{"--release-id", exportReleaseID, "--output-dir", "unused"}
	env := remoteTestEnvironment("https://catalog.invalid", "token")
	for _, format := range []string{"mcp-vscode", "mcp-claude-code", "mcp-cursor"} {
		o, err := parseExport(append(append([]string{}, base...), "--format", format, "--remote", "2"), env)
		if err != nil || o.remotePosition != 2 || !o.remoteSelected {
			t.Fatalf("client flags: %+v %v", o, err)
		}
	}
	for _, extra := range [][]string{{"--remote", "1"}, {"--format", "draft", "--remote", "1"}, {"--format", "mcp-vscode", "--remote", "0"}, {"--format", "mcp-vscode", "--remote", "-1"}, {"--format", "mcp-vscode", "--remote", "PRIVATE_SENTINEL"}, {"--format", "mcp-vscode", "--version-revision-id", exportRepID}} {
		if _, err := parseExport(append(append([]string{}, base...), extra...), env); err == nil || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
			t.Fatalf("invalid flag handling: %v", err)
		}
	}
	for _, mode := range []string{"preview", "batch"} {
		for _, extra := range [][]string{{"--remote", "1"}, {"--format", "mcp-vscode"}} {
			args := append([]string{mode, "--output-dir", "unused", "--release-id", exportReleaseID}, extra...)
			if mode == "preview" {
				args = append([]string{mode, "--output-dir", "unused", "--query", "browse"}, extra...)
			}
			if _, err := parseExportBatch(args, env); err == nil {
				t.Fatalf("accepted client format/selector in %s", mode)
			}
		}
	}
}

func mcpExportTestRecord(t *testing.T, raw []byte, artifact bool) ([]byte, exportRepresentation, http.Header) {
	t.Helper()
	c := mcpExportFixtures(t)[0]
	rep, headers := exportTestArtifact(raw)
	rep.MediaType = c.MediaType
	rep.ProfileID = c.Profile
	headers.Set("Content-Type", c.MediaType)
	if !artifact {
		rep.ArtifactID = ""
		rep.Data = raw
	}
	encoded, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	record := exportTestRecord(string(encoded))
	record = bytes.ReplaceAll(record, []byte(`"kind":"agent"`), []byte(`"kind":"mcp_server"`))
	record = bytes.ReplaceAll(record, []byte(`"release":{"id"`), []byte(`"release":{"state":"published","id"`))
	if _, _, err := selectTestExport(record, ""); err != nil {
		t.Fatalf("fixture record: %s: %v", record, err)
	}
	return record, rep, headers
}

func TestMCPExportPreparation(t *testing.T) {
	raw := mcpExportFixtures(t)[0].Document
	for _, scenario := range []string{"inline", "artifact", "denied artifact", "bad digest", "bad media", "bad size", "content limit", "generated limit", "wrong kind", "retired", "deprecated", "external", "multiple representations", "explicit representation", "unsupported profile"} {
		t.Run(scenario, func(t *testing.T) {
			raw := raw
			if scenario == "generated limit" {
				raw = []byte(`{"remotes":[{"type":"sse","url":"https://mcp.test"}]}`)
			}
			artifact := scenario == "artifact" || scenario == "denied artifact" || scenario == "bad digest" || scenario == "bad media" || scenario == "bad size"
			record, rep, headers := mcpExportTestRecord(t, raw, artifact)
			switch scenario {
			case "wrong kind":
				record = bytes.ReplaceAll(record, []byte(`"kind":"mcp_server"`), []byte(`"kind":"agent"`))
			case "retired":
				record = bytes.ReplaceAll(record, []byte(`"state":"published"`), []byte(`"state":"retired"`))
			case "deprecated":
				record = bytes.ReplaceAll(record, []byte(`"state":"published"`), []byte(`"state":"deprecated"`))
			case "external":
				record = bytes.ReplaceAll(record, []byte(`"source":"local"`), []byte(`"source":"indexed"`))
			case "bad digest":
				headers.Set("Content-Digest", "sha-256=:bad=:")
			case "bad media":
				headers.Set("Content-Type", "application/json")
			case "bad size":
				headers.Set("Content-Length", "1")
			case "unsupported profile":
				record = bytes.ReplaceAll(record, []byte(adapter.ProfileMCPRegistry), []byte(adapter.ProfilePrivateMCP))
			case "multiple representations", "explicit representation":
				var r exportRecord
				_ = json.Unmarshal(record, &r)
				second := r.Revision.Representations[0]
				second.ID = "00000000-0000-7000-8000-000000000008"
				r.Revision.Representations = append(r.Revision.Representations, second)
				record, _ = json.Marshal(r)
			}
			calls := 0
			session := &remoteSession{origin: "https://catalog.invalid", token: "fixture-token", client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.Host != "catalog.invalid" || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Fatal("unexpected network/credential boundary", r.URL)
				}
				if r.URL.Path == "/api/v1/catalog/resources/"+exportReleaseID {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(record))}, nil
				}
				if !artifact || r.URL.Path != "/api/v1/artifacts/"+rep.ArtifactID+"/content" {
					t.Fatal("unexpected endpoint", r.URL)
				}
				status := 200
				body := raw
				if scenario == "denied artifact" {
					status = 403
					body = []byte(`{}`)
				}
				return &http.Response{StatusCode: status, Header: headers, ContentLength: rep.Size, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})}}
			limit := int64(8 << 20)
			if scenario == "content limit" {
				limit = 1
			}
			// A minimal native document can produce a larger wrapper/key projection.
			if scenario == "generated limit" {
				limit = int64(len(raw))
			}
			options := exportOptions{releaseID: exportReleaseID, format: "mcp-vscode", maxContentBytes: limit, remote: remoteOptions{path: "/api/v1/catalog/resources/" + exportReleaseID, maxResponseBytes: 8 << 20}}
			if scenario == "explicit representation" {
				options.representationID = "00000000-0000-7000-8000-000000000008"
			}
			m, content, err := prepareExport(context.Background(), session, options)
			success := scenario == "inline" || scenario == "artifact" || scenario == "deprecated" || scenario == "explicit representation"
			if (err == nil) != success {
				t.Fatalf("unexpected result: %v", err)
			}
			if !success {
				expected := map[string]string{"multiple representations": "--representation-id", "unsupported profile": "supported retained native MCP", "wrong kind": "published or deprecated MCP", "retired": "published or deprecated MCP", "content limit": "inline JSON exceeds", "generated limit": "client JSON exceeds", "external": "record identities"}
				if want := expected[scenario]; want != "" && !strings.Contains(err.Error(), want) {
					t.Fatalf("wrong refusal guard: %v, want %s", err, want)
				}
				wantCalls := 1
				if artifact {
					wantCalls = 2
				}
				if calls != wantCalls {
					t.Fatalf("unexpected reads on refusal: %d, want %d", calls, wantCalls)
				}
				return
			}
			if m.Disposition != "mcp_client_configuration" || m.MCP == nil || m.MCP.Remote != 1 || m.File.Name != "mcp.json" || m.File.Digest != exportChecksum(content) || m.Recorded.Digest != rep.ContentDigest || m.File.Digest == m.Recorded.Digest {
				t.Fatalf("lost generated/source binding: %+v", m)
			}
			if scenario == "explicit representation" && m.RepresentationID != options.representationID {
				t.Fatal("selected another representation")
			}
			wantCalls := 1
			if artifact {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("unexpected reads: %d", calls)
			}
		})
	}
}

func TestMCPExportPrivateFiles(t *testing.T) {
	c := mcpExportFixtures(t)[0]
	for _, format := range []string{"mcp-vscode", "mcp-claude-code", "mcp-cursor"} {
		t.Run(format, func(t *testing.T) {
			content, notes, err := projectMCPExport(c.Document, c.Profile, c.MediaType, c.ReleaseID, format, 1)
			if err != nil {
				t.Fatal(err)
			}
			manifest := exportManifest{Format: 1, Disposition: "mcp_client_configuration", MCP: notes, File: &exportFile{Name: mcpExportFilename(format), exportDigest: exportDigest{Digest: exportChecksum(content), Size: int64(len(content)), Scope: "generated_client_configuration"}}}
			base := t.TempDir()
			parent, err := os.OpenRoot(base)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parent.Close() }()
			if err := writeExport(t.Context(), parent, "configuration", manifest, content); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"configuration", "configuration/" + manifest.File.Name, "configuration/export.json"} {
				info, err := parent.Stat(name)
				if err != nil || info.Mode().Perm()&0077 != 0 {
					t.Fatalf("private file %s: %v", name, err)
				}
			}
			if err := writeExport(t.Context(), parent, "configuration", manifest, content); err == nil {
				t.Fatal("overwrote existing export")
			}
			actual, err := parent.ReadFile("configuration/" + manifest.File.Name)
			if err != nil || !bytes.Equal(actual, content) {
				t.Fatal("existing file changed")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cancellation := &mcpExportCancelContext{Context: ctx, cancel: cancel, filename: filepath.Join(base, "cancelled", manifest.File.Name)}
			if err := writeExport(cancellation, parent, "cancelled", manifest, content); err == nil {
				t.Fatal("published sidecar after cancellation")
			}
			if _, err := parent.Stat("cancelled"); !os.IsNotExist(err) {
				t.Fatal("cancelled export left configuration or sidecar")
			}
			manifest.File.Digest = "sha256:" + strings.Repeat("0", 64)
			if err := writeExport(t.Context(), parent, "failed", manifest, content); err == nil {
				t.Fatal("saved changed generated bytes")
			}
			if _, err := parent.Stat("failed"); !os.IsNotExist(err) {
				t.Fatal("failed export directory remains")
			}
			manifest.File.Digest = exportChecksum(content)
			manifest.File.Name = "../settings.json"
			if err := writeExport(t.Context(), parent, "unsafe", manifest, content); err == nil {
				t.Fatal("accepted unsafe configuration filename")
			}
		})
	}
}

// Cancel after the generated file exists, before the completion sidecar. This
// deterministic filesystem boundary requires no timer or background goroutine.
type mcpExportCancelContext struct {
	context.Context //nolint:containedctx // Test wrapper implements cancellation at the file-publication boundary.
	cancel          context.CancelFunc
	filename        string
}

func (c *mcpExportCancelContext) Err() error {
	if _, err := os.Stat(c.filename); err == nil {
		c.cancel()
	}
	return c.Context.Err()
}
