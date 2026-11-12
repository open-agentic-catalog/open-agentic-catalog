//go:build functional

package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// Exercise the real command and private TLS transport. Every URL is an owned
// loopback fixture; a declared MCP path must never receive a request.
func TestMCPExportCLIPrivateTLS(t *testing.T) {
	binary := filepath.Join(canonicalTempDir(t), "catalogctl")
	build := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-o", binary, ".") // #nosec G204,G702 -- fixed build in an owned temporary directory.
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", raw, err)
	}
	for _, scenario := range []string{"inline", "artifact", "denied record", "denied artifact", "bad digest", "wrong media", "wrong size", "multiple remotes", "explicit remote", "retired", "wrong representation", "wrong kind", "URL template", "HTTP endpoint"} {
		t.Run(scenario, func(t *testing.T) {
			var mcpCalls, artifactCalls atomic.Int64
			var record []byte
			var rep exportRepresentation
			var headers http.Header
			var document []byte
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("wrong method or credentials")
				}
				switch r.URL.Path {
				case "/api/v1/catalog/resources/" + exportReleaseID:
					if scenario == "denied record" {
						http.Error(w, "denied", http.StatusForbidden)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(record)
				case "/api/v1/artifacts/" + rep.ArtifactID + "/content":
					artifactCalls.Add(1)
					if scenario == "denied artifact" {
						http.Error(w, "denied", http.StatusForbidden)
						return
					}
					for name, values := range headers {
						w.Header()[name] = values
					}
					_, _ = w.Write(document)
				default:
					mcpCalls.Add(1)
					http.Error(w, "no MCP connection allowed", http.StatusInternalServerError)
				}
			}))
			defer server.Close()
			remotes := []map[string]any{{"type": "streamable-http", "url": server.URL + "/mcp?route=a%2Fb", "headers": []map[string]any{{"name": "Authorization", "isRequired": true, "value": "PRIVATE_HEADER", "default": "PRIVATE_DEFAULT"}}}}
			if scenario == "multiple remotes" || scenario == "explicit remote" {
				remotes = append(remotes, map[string]any{"type": "sse", "url": server.URL + "/sse"})
			}
			if scenario == "URL template" {
				remotes[0]["url"] = server.URL + "/${command:PRIVATE_COMMAND}"
			}
			if scenario == "HTTP endpoint" {
				remotes[0]["url"] = strings.Replace(server.URL, "https://", "http://", 1) + "/mcp"
			}
			var err error
			document, err = json.Marshal(map[string]any{"name": "org.example/test", "description": "Local test fixture.", "version": "1.0.0", "remotes": remotes})
			if err != nil {
				t.Fatal(err)
			}
			artifact := scenario == "artifact" || scenario == "denied artifact" || scenario == "bad digest" || scenario == "wrong media" || scenario == "wrong size"
			record, rep, headers = mcpExportTestRecord(t, document, artifact)
			switch scenario {
			case "bad digest":
				headers.Set("Content-Digest", "sha-256=:bad=:")
			case "wrong media":
				headers.Set("Content-Type", "application/json")
			case "wrong size":
				headers.Set("Content-Length", "1")
			case "retired":
				record = bytes.ReplaceAll(record, []byte(`"state":"published"`), []byte(`"state":"retired"`))
			case "wrong kind":
				record = bytes.ReplaceAll(record, []byte(`"kind":"mcp_server"`), []byte(`"kind":"agent"`))
			}
			ca := filepath.Join(canonicalTempDir(t), "ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(canonicalTempDir(t), "configuration")
			run := func() ([]byte, error) {
				args := []string{"export", "--release-id", exportReleaseID, "--format", "mcp-claude-code", "--output-dir", directory, "--base-url", server.URL, "--ca-file", ca}
				if scenario == "explicit remote" {
					args = append(args, "--remote", "2")
				}
				if scenario == "wrong representation" {
					args = append(args, "--representation-id", "00000000-0000-7000-8000-000000000009")
				}
				command := exec.CommandContext(t.Context(), binary, args...) // #nosec G204,G702 -- owned command and local private TLS fixture.
				command.Env = append(os.Environ(), "CATALOG_REMOTE_TOKEN=fixture-token", "CATALOG_REMOTE_TOKEN_FILE=", "CATALOG_REMOTE_CA_FILE=", "CATALOG_CONFIG_FILE=/missing/PRIVATE_RUNTIME")
				return command.CombinedOutput()
			}
			output, err := run()
			success := scenario == "inline" || scenario == "artifact" || scenario == "explicit remote"
			if (err == nil) != success {
				t.Fatalf("command result: %s %v", output, err)
			}
			for _, secret := range []string{"PRIVATE_HEADER", "PRIVATE_DEFAULT", "PRIVATE_RUNTIME", "fixture-token", "PRIVATE_COMMAND", server.URL} {
				if bytes.Contains(output, []byte(secret)) {
					t.Fatal("terminal disclosed private inputs")
				}
			}
			wantArtifacts := int64(0)
			if artifact {
				wantArtifacts = 1
			}
			if mcpCalls.Load() != 0 || artifactCalls.Load() != wantArtifacts {
				t.Fatal("unexpected MCP/artifact requests", mcpCalls.Load(), artifactCalls.Load())
			}
			if !success {
				expected := map[string]string{"denied record": `"status":403`, "denied artifact": `"status":403`, "bad digest": "invalid_response", "wrong media": "invalid_response", "wrong size": "invalid_response", "multiple remotes": "select --remote N", "retired": "published or deprecated MCP release", "wrong representation": "selected representation", "wrong kind": "published or deprecated MCP release", "URL template": "literal HTTPS URL", "HTTP endpoint": "literal HTTPS URL"}
				if expected[scenario] == "" || !bytes.Contains(output, []byte(expected[scenario])) {
					t.Fatalf("command failed at the wrong guard: %s, want %s", output, expected[scenario])
				}
				if _, err := os.Stat(directory); !os.IsNotExist(err) {
					t.Fatal("failed export left output")
				}
				return
			}
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			content, err := root.ReadFile(".mcp.json")
			if err != nil {
				t.Fatal(err)
			}
			expectedType := "http"
			if scenario == "explicit remote" {
				expectedType = "sse"
			}
			if !bytes.Contains(content, []byte(`"type": "`+expectedType+`"`)) || bytes.Contains(content, []byte("PRIVATE")) || bytes.Contains(content, []byte("headers")) {
				t.Fatalf("unsafe/wrong client output: %s", content)
			}
			var configuration map[string]map[string]map[string]string
			selected := 0
			if scenario == "explicit remote" {
				selected = 1
			}
			if json.Unmarshal(content, &configuration) != nil || len(configuration) != 1 || len(configuration["mcpServers"]) != 1 {
				t.Fatalf("invalid client shape: %s", content)
			}
			entry := configuration["mcpServers"]["catalog-"+exportReleaseID]
			if len(entry) != 2 || entry["url"] != remotes[selected]["url"] || entry["type"] != expectedType {
				t.Fatalf("exported another connection or extra fields: %s", content)
			}
			for _, name := range []string{".", ".mcp.json", "export.json"} {
				info, err := os.Stat(filepath.Join(directory, name))
				if err != nil || info.Mode().Perm()&0077 != 0 {
					t.Fatal("output is not private", name, err)
				}
			}
			manifest, err := root.ReadFile("export.json")
			if err != nil {
				t.Fatal(err)
			}
			var sidecar exportManifest
			if json.Unmarshal(manifest, &sidecar) != nil || sidecar.MCP == nil || sidecar.MCP.ClientFormat != "mcp-claude-code" || sidecar.File == nil || sidecar.File.Digest != exportChecksum(content) || sidecar.Recorded.Digest != rep.ContentDigest {
				t.Fatalf("invalid source/output binding: %s", manifest)
			}
			wantHeaders := []exportMCPHeader{{Name: "Authorization", Required: true}}
			if selected == 1 {
				wantHeaders = []exportMCPHeader{}
			}
			if sidecar.MCP.Remote != selected+1 || sidecar.MCP.Transport != remotes[selected]["type"] || !reflect.DeepEqual(sidecar.MCP.OmittedHeaders, wantHeaders) || sidecar.File.Size != int64(len(content)) || sidecar.File.Name != ".mcp.json" || sidecar.File.Scope != "generated_client_configuration" {
				t.Fatalf("incomplete selected connection sidecar: %+v", sidecar)
			}
			if _, err := run(); err == nil {
				t.Fatal("overwrote completed export")
			}
			after, err := root.ReadFile(".mcp.json")
			if err != nil || !bytes.Equal(after, content) {
				t.Fatal("repeat changed completed export")
			}
			if mcpCalls.Load() != 0 || artifactCalls.Load() != wantArtifacts {
				t.Fatal("unexpected MCP/artifact requests", mcpCalls.Load(), artifactCalls.Load())
			}
			if strings.Contains(string(manifest), "PRIVATE") {
				t.Fatal("sidecar copied header values")
			}
		})
	}
}
