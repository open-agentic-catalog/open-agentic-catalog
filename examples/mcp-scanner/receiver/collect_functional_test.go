//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNativeMCPMetadataOnlyCollection(t *testing.T) {
	for _, jsonResponse := range []bool{true, false} {
		t.Run(map[bool]string{true: "JSON", false: "SSE"}[jsonResponse], func(t *testing.T) {
			var invoked atomic.Int32
			server := mcp.NewServer(&mcp.Implementation{Name: "owned-tools", Version: "1.0"}, nil)
			server.AddTool(&mcp.Tool{Name: "sum", Description: "Add two integers.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				invoked.Add(1)
				return &mcp.CallToolResult{}, nil
			})
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: jsonResponse})
			var mu sync.Mutex
			methods := map[string]int{}
			deletes := 0
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer mcp-secret" {
					t.Error("wrong runtime credential")
					http.Error(w, "denied", http.StatusUnauthorized)
					return
				}
				if r.Method == http.MethodPost {
					body, _ := io.ReadAll(r.Body)
					r.Body = io.NopCloser(bytes.NewReader(body))
					var request struct {
						Method string `json:"method"`
					}
					_ = json.Unmarshal(body, &request)
					mu.Lock()
					methods[request.Method]++
					mu.Unlock()
				}
				if r.Method == http.MethodDelete {
					mu.Lock()
					deletes++
					mu.Unlock()
				}
				handler.ServeHTTP(w, r)
			}))
			defer peer.Close()
			ca := writePrivate(t, "mcp-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}))
			mapping := testMapping
			mapping.Endpoint = peer.URL + "/mcp"
			mapping.CAFile = ca
			mapping.TokenFile = writePrivate(t, "mcp-token", []byte("mcp-secret"))
			catalogToken := writePrivate(t, "catalog-token", []byte("catalog-secret"))
			collector := &mcpCollector{catalogTokenFile: catalogToken, now: func() time.Time { return fixed }}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			result, err := collector.Collect(ctx, mapping)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Complete || result.Pages != 1 || result.Digest != sha(result.Document) {
				t.Fatal("incomplete capture")
			}
			var capture struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			if json.Unmarshal(result.Document, &capture) != nil || len(capture.Tools) != 1 || capture.Tools[0].Name != "sum" {
				t.Fatal("native tool omitted")
			}
			mu.Lock()
			defer mu.Unlock()
			if invoked.Load() != 0 || methods["initialize"] != 1 || methods["notifications/initialized"] != 1 || methods["tools/list"] != 1 || len(methods) != 3 || deletes != 1 {
				t.Fatalf("unexpected protocol operations: %v, deletes=%d", methods, deletes)
			}
		})
	}
}

func TestMCPWireValidationAndPaging(t *testing.T) {
	for _, mode := range []string{"two-pages", "repeat", "too-many-pages", "duplicate-tool", "duplicate-json", "null-cursor", "bad-tool", "too-many-tools", "large", "redirect", "wrong-credential", "same-credential", "wrong-trust", "no-session-termination", "large-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			var pages, forbidden atomic.Int32
			redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				forbidden.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer redirect.Close()
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer runtime-key" {
					http.Error(w, "denied", http.StatusUnauthorized)
					return
				}
				if mode == "redirect" {
					http.Redirect(w, r, redirect.URL, http.StatusTemporaryRedirect)
					return
				}
				if r.Method == http.MethodDelete {
					if mode == "no-session-termination" {
						w.WriteHeader(http.StatusMethodNotAllowed)
						return
					}
					if mode == "large-cleanup" {
						_, _ = io.WriteString(w, strings.Repeat("x", 4097))
						return
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						Cursor string `json:"cursor"`
					} `json:"params"`
				}
				body, _ := io.ReadAll(r.Body)
				if json.Unmarshal(body, &req) != nil {
					t.Error("bad SDK request")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if req.Method == "notifications/initialized" {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				var result any
				switch req.Method {
				case "initialize":
					w.Header().Set("Mcp-Session-Id", "owned-session")
					result = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "owned", "version": "1"}}
				case "tools/list":
					p := int(pages.Add(1))
					name := "tool_" + strconv.Itoa(p)
					if mode == "duplicate-tool" {
						name = "duplicate"
					}
					tool := map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}, "unknownEvidence": map[string]any{"exactNumber": json.Number("9007199254740993")}}
					if mode == "bad-tool" {
						tool["name"] = "bad name"
					}
					page := map[string]any{"tools": []any{tool}}
					result = page
					if mode == "null-cursor" {
						page["nextCursor"] = nil
					}
					if mode == "two-pages" && p == 1 || mode == "duplicate-tool" && p == 1 || mode == "repeat" {
						page["nextCursor"] = "second"
					}
					if mode == "too-many-pages" {
						page["nextCursor"] = "next-" + name
					}
					if mode == "large" {
						tool["description"] = strings.Repeat("x", captureLimit)
					}
					if mode == "too-many-tools" {
						many := []any{}
						for i := 0; i < 129; i++ {
							many = append(many, map[string]any{"name": "tool_" + strconv.Itoa(i), "inputSchema": map[string]any{"type": "object"}})
						}
						page["tools"] = many
					}
				default:
					forbidden.Add(1)
					http.Error(w, "refused", http.StatusBadRequest)
					return
				}
				out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
				if mode == "duplicate-json" && req.Method == "tools/list" {
					out = bytes.Replace(out, []byte(`"tools":`), []byte(`"tools":[],"tools":`), 1)
				}
				_, _ = w.Write(out)
			}))
			defer peer.Close()
			mapping := testMapping
			mapping.Endpoint = peer.URL + "/mcp"
			mapping.TokenFile = writePrivate(t, "runtime-token", []byte("runtime-key"))
			mapping.CAFile = writePrivate(t, "ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}))
			apiToken := writePrivate(t, "api-token", []byte("catalog-key"))
			if mode == "wrong-credential" {
				mapping.TokenFile = writePrivate(t, "wrong-token", []byte("incorrect"))
			}
			if mode == "same-credential" {
				mapping.TokenFile = apiToken
			}
			if mode == "wrong-trust" {
				mapping.CAFile = ""
			}
			col := &mcpCollector{catalogTokenFile: apiToken, now: func() time.Time { return fixed }}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			out, err := col.Collect(ctx, mapping)
			switch mode {
			case "no-session-termination":
				if err != nil || !out.Complete || out.Pages != 1 {
					t.Fatalf("optional session termination refused: %v", err)
				}
			case "two-pages":
				if err != nil || !out.Complete || out.Pages != 2 || !bytes.Contains(out.Document, []byte("9007199254740993")) {
					t.Fatalf("exact paged capture failed: %v", err)
				}
			case "repeat", "too-many-pages":
				if err != nil || out.Complete || out.Reason == "" || !bytes.Contains(out.Document, []byte("nextCursor")) {
					t.Fatalf("partial traversal not retained: %v", err)
				}
			default:
				if err == nil {
					t.Fatal("invalid runtime response accepted")
				}
			}
			if forbidden.Load() != 0 {
				t.Fatal("forbidden destination or method contacted")
			}
		})
	}
}
