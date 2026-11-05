package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func TestMCPAdmissionFiniteProtocolAndOriginalBytes(t *testing.T) {
	tests := []struct {
		name, raw, version string
		status             int
		code               int64
	}{
		{"legacy initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, "", 0, 0},
		{"legacy list", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "2025-11-25", 0, 0},
		{"modern direct", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"catalog_control_access","arguments":{}}}`, "2026-07-28", 0, 0},
		{"extensions", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"vendor.test/extra":true}}}`, "2026-07-28", 0, 0},
		{"notification", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "2025-11-25", 0, 0},
		{"headerless list", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "", 400, mcp.CodeHeaderMismatch},
		{"unsupported legacy", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "2025-03-26", 400, mcp.CodeUnsupportedProtocolVersion},
		{"subscription", `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen"}`, "2026-07-28", 404, -32601},
		{"logging", `{"jsonrpc":"2.0","id":1,"method":"logging/setLevel"}`, "2025-11-25", 404, -32601},
		{"null parameters", `{"jsonrpc":"2.0","id":1,"method":"ping","params":null}`, "2025-11-25", 400, -32602},
		{"array parameters", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":[]}`, "2025-11-25", 400, -32602},
		{"scalar parameters", `{"jsonrpc":"2.0","id":1,"method":"ping","params":true}`, "2025-11-25", 400, -32602},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`, "2025-11-25", 400, -32600},
		{"duplicate", `{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"tools/call"}`, "2025-11-25", 400, -32700},
		{"trailing", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}{}`, "2025-11-25", 400, -32700},
		{"unicode", `{"jsonrpc":"2.0","id":"\ud800","method":"tools/list"}`, "2025-11-25", 400, -32700},
		{"invalid id", `{"jsonrpc":"2.0","id":{},"method":"tools/list"}`, "2025-11-25", 400, -32600},
		{"not a call", `{"jsonrpc":"2.0","method":"tools/list"}`, "2025-11-25", 400, -32600},
		{"modern initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, "2026-07-28", 404, -32601},
		{"notification with identifier", `{"jsonrpc":"2.0","id":1,"method":"notifications/initialized"}`, "2025-11-25", 400, -32600},
		{"modern legacy notification", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "2026-07-28", 400, -32600},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost/mcp", nil)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json, text/event-stream")
			if tc.version != "" {
				r.Header.Set("Mcp-Protocol-Version", tc.version)
			}
			_, status, code := admitMCP(r, []byte(tc.raw))
			if status != tc.status || code != tc.code {
				t.Fatalf("got %d %d want %d %d", status, code, tc.status, tc.code)
			}
		})
	}
}

func TestMCPAdmissionRequiresBothAcceptedResponseFormats(t *testing.T) {
	const raw = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	for _, tc := range []struct {
		name, contentType string
		accept            []string
		status            int
	}{
		{"split headers", "application/json; charset=utf-8", []string{"application/json", "text/event-stream"}, 0},
		{"positive quality", "application/json", []string{"application/json;q=0.5, text/event-stream;q=1"}, 0},
		{"alternative JSON value", "application/json", []string{"application/json;q=0, application/json;q=0.5, text/event-stream"}, 0},
		{"JSON alone", "application/json", []string{"application/json"}, 400},
		{"stream alone", "application/json", []string{"text/event-stream"}, 400},
		{"wildcard alone", "application/json", []string{"*/*"}, 400},
		{"zero JSON quality", "application/json", []string{"application/json;q=0, text/event-stream"}, 400},
		{"negative stream quality", "application/json", []string{"application/json, text/event-stream;q=-1"}, 400},
		{"invalid stream quality", "application/json", []string{"application/json, text/event-stream;q=bad"}, 400},
		{"excessive stream quality", "application/json", []string{"application/json, text/event-stream;q=1.1"}, 400},
		{"malformed stream parameters", "application/json", []string{"application/json, text/event-stream;broken"}, 400},
		{"wrong content type", "text/plain", []string{"application/json, text/event-stream"}, 415},
		{"malformed content type", "application/json;broken", []string{"application/json, text/event-stream"}, 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
			r.Header.Set("Content-Type", tc.contentType)
			r.Header["Accept"] = tc.accept
			r.Header.Set("Mcp-Protocol-Version", "2025-11-25")
			a, status, code := admitMCP(r, []byte(raw))
			if status != tc.status || status != 0 && code != -32600 || status == 0 && (a.Method != "tools/list" || string(a.ID) != "1") {
				t.Fatalf("format admission = %d/%d %+v", status, code, a)
			}
		})
	}
}

func TestMCPOperationClassificationUsesFixedLabels(t *testing.T) {
	for method, want := range map[string]string{"server/discover": "discover", "tools/list": "list", "tools/call": "call", "ping": "protocol", "notifications/initialized": "protocol", "private-method-input": "protocol"} {
		if got := (mcpAdmission{Method: method}).operation(); got != want {
			t.Fatalf("operation label = %q, want %q", got, want)
		}
	}
}

func TestMCPHeadersOriginsPreflightAndCredentialRules(t *testing.T) {
	h := &mcpHTTP{publicOrigin: "https://catalog.example.com", settings: config.MCP{AllowedOrigins: []string{"https://console.example.com"}}, issuer: "https://issuer.example.com"}
	for _, origin := range []string{"", "https://catalog.example.com", "https://console.example.com", "null", "https://evil.example.com", "https://catalog.example.com/"} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		expected := origin == "" || origin == h.publicOrigin || origin == "https://console.example.com"
		if h.origin(w, r) != expected {
			t.Fatal("origin decision", origin)
		}
	}
	for _, header := range []http.Header{{"Authorization": {"Bearer one", "Bearer one"}}, {"Origin": {""}}, {"Mcp-Session-Id": {"ignored"}}, {"Mcp-Name": {"one"}, "mcp-name": {"one"}}, {"Mcp-Param-Query": {"value"}}} {
		if mcpHeaders(header) {
			t.Fatal("ambiguous/unsupported headers admitted", header)
		}
	}
	for _, token := range []string{"Bearer one two", "Bearer one,other", "Basic one", "Bearer ", "Bearer one\t"} {
		if _, ok := mcpBearer(token); ok {
			t.Fatal("invalid bearer admitted", token)
		}
	}
	for _, reason := range []string{"", "invalid_token", "insufficient_scope"} {
		w := httptest.NewRecorder()
		h.challenge(w, reason)
		if !strings.Contains(w.Header().Get("WWW-Authenticate"), h.publicOrigin+mcpMetadataPath) {
			t.Fatal("canonical metadata missing")
		}
	}
	for _, tc := range []struct {
		path, method, headers string
		status                int
	}{{"/mcp", http.MethodPost, "authorization, mcp-method, mcp-name", 204}, {"/mcp", http.MethodGet, "", 403}, {mcpMetadataPath, http.MethodGet, "", 204}, {"/mcp", http.MethodPost, "Cookie", 403}} {
		r := httptest.NewRequestWithContext(t.Context(), "OPTIONS", tc.path, nil)
		r.Header.Set("Origin", h.publicOrigin)
		r.Header.Set("Access-Control-Request-Method", tc.method)
		r.Header.Set("Access-Control-Request-Headers", tc.headers)
		w := httptest.NewRecorder()
		h.preflight(w, r)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code)
		}
	}
}

func TestMCPFiniteResponseOverflowErrorsAndEscapedCredentials(t *testing.T) {
	a := mcpAdmission{ID: json.RawMessage("1"), Method: "tools/list"}
	for _, raw := range []string{`{"jsonrpc":"2.0","id":1,"result":{"value":"synthetic-secret"}}`, `{"jsonrpc":"2.0","id":1,"result":{"value":"synthetic\u002dsecret"}}`} {
		w := newMCPResponse(1024)
		_, _ = w.Write([]byte(raw))
		status, body := w.safe(a, "synthetic-secret")
		if status != 500 || strings.Contains(string(body), "secret") {
			t.Fatal(status, string(body))
		}
	}
	w := newMCPResponse(8)
	_, _ = w.Write([]byte("1234"))
	_, _ = w.Write([]byte("56789"))
	_, _ = w.Write([]byte("continued"))
	status, body := w.safe(a, "")
	if status != 500 || w.body.Len() != 4 || strings.Contains(string(body), "1234") {
		t.Fatal("overflow leaked prefix")
	}
	w = newMCPResponse(1024)
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32020,"message":"raw private input","data":{"private":"secret"}}}`))
	status, body = w.safe(a, "")
	if status != 400 || !strings.Contains(string(body), "-32020") || strings.Contains(string(body), "private") || strings.Contains(string(body), "secret") {
		t.Fatal(status, string(body))
	}
	w = newMCPResponse(1024)
	w.WriteHeader(http.StatusAccepted)
	status, body = w.safe(a, "")
	if status != 202 || len(body) != 0 {
		t.Fatal("notification response")
	}
	w = newMCPResponse(300 << 10)
	_, _ = w.Write([]byte(`{"result":"` + strings.Repeat("x", 260<<10) + `"}`))
	if status, _ := w.safe(a, ""); status != 500 {
		t.Fatal("definition/discovery bound")
	}
}

func FuzzMCPFiniteAdmission(f *testing.F) {
	for _, seed := range []string{`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, `{"jsonrpc":"2.0","id":"request","method":"tools/call","params":{"name":"catalog_control_access","arguments":{}}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"subscriptions/listen"}`, `[]`, `{"id":"\ud800"}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 256<<10 {
			t.Skip()
		}
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Mcp-Protocol-Version", "2025-11-25")
		a, status, _ := admitMCP(r, raw)
		if status != 0 {
			return
		}
		// Independently check the shape and finite method allowlist. Arbitrary
		// parser input must never admit a batch or a streaming/subscription method.
		var envelope map[string]json.RawMessage
		var rpc string
		if json.Unmarshal(raw, &envelope) != nil || envelope == nil || json.Unmarshal(envelope["jsonrpc"], &rpc) != nil || rpc != "2.0" {
			t.Fatal("admitted an invalid envelope")
		}
		switch a.Method {
		case "initialize", "ping", "tools/list", "tools/call", "server/discover":
			if a.ID == nil {
				t.Fatal("admitted a request without an identifier")
			}
		case "notifications/initialized":
			if a.ID != nil {
				t.Fatal("admitted a notification with an identifier")
			}
		default:
			t.Fatal("admitted an unselected method")
		}
	})
}

func TestMCPSDKErrorSanitizationAndVersionEvidence(t *testing.T) {
	a := mcpAdmission{ID: json.RawMessage("7"), Method: "tools/list"}
	for _, tc := range []struct {
		name, raw        string
		status, expected int
	}{
		{"plain SDK rejection", "PRIVATE_PARSER_ERROR", http.StatusBadRequest, http.StatusBadRequest},
		{"invalid success", "PRIVATE_INVALID_JSON", http.StatusOK, http.StatusInternalServerError},
		{"null response", "null", http.StatusOK, http.StatusInternalServerError},
		{"wrong error code", `{"jsonrpc":"2.0","id":7,"error":{"code":"PRIVATE_CODE"}}`, http.StatusBadRequest, http.StatusInternalServerError},
		{"version evidence", `{"jsonrpc":"2.0","id":7,"error":{"code":-32022,"message":"PRIVATE_MESSAGE","data":{"supported":["PRIVATE_VERSION"],"requested":"2027-01-01","private":"PRIVATE_DATA"}}}`, http.StatusBadRequest, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newMCPResponse(1024)
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.raw))
			status, raw := w.safe(a, "")
			if status != tc.expected || !json.Valid(raw) || strings.Contains(string(raw), "PRIVATE") {
				t.Fatal("unsafe SDK error", status, string(raw))
			}
			if tc.name == "version evidence" {
				var reply struct {
					Error struct {
						Code int `json:"code"`
						Data struct {
							Supported []string `json:"supported"`
							Requested string   `json:"requested"`
						} `json:"data"`
					} `json:"error"`
				}
				if json.Unmarshal(raw, &reply) != nil || reply.Error.Code != -32022 || reply.Error.Data.Requested != "2027-01-01" || len(reply.Error.Data.Supported) != 2 || reply.Error.Data.Supported[0] != "2026-07-28" || reply.Error.Data.Supported[1] != "2025-11-25" {
					t.Fatal("supported-version evidence changed", string(raw))
				}
			}
		})
	}
	// A malformed internal identifier must still produce a fixed valid error.
	if raw := mcpErrorBytes(json.RawMessage("{"), -32603, "PRIVATE_MESSAGE"); !json.Valid(raw) || strings.Contains(string(raw), "PRIVATE") {
		t.Fatal("malformed error identifier escaped safety boundary", string(raw))
	}
}
