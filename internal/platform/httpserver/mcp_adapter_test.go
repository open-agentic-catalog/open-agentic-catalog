package httpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/catalogmcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

type mcpUnitAuth struct{ failure error }

func (a mcpUnitAuth) Authenticate(_ context.Context, token string) (identity.Actor, error) {
	if token != "synthetic-unit-token-589" {
		return identity.Actor{}, errors.New("PRIVATE_INVALID_TOKEN")
	}
	return projectionActor(identity.RoleReader), a.failure
}

type mcpUnitReader struct {
	catalogmcp.Reader
	calls     int
	panicRead bool
	failure   error
}

func (r *mcpUnitReader) ReadAccess(ctx context.Context, _ url.Values) (controlclient.Envelope, error) {
	r.calls++
	if r.panicRead {
		panic("PRIVATE_PANIC")
	}
	if r.failure != nil {
		return controlclient.Envelope{}, r.failure
	}
	if _, ok := identity.FromContext(ctx); !ok {
		return controlclient.Envelope{}, errors.New("missing actor")
	}
	data := json.RawMessage(`{"tenantId":"00000000-0000-7000-8000-000000000001","permissions":[],"availablePermissions":[],"namespaceGrants":[],"authorizationRevision":1,"scopeEnforcement":false,"scopes":[]}`)
	return controlclient.Envelope{Data: data}, nil
}

// These adapter units use recorders and controlled ports only. They start no
// listeners, processes, domain services or databases. Real exchanges are tagged functional.
func TestMCPHTTPIsolatedAdapterAuthenticationScopeAndSafety(t *testing.T) {
	settings := config.Defaults().MCP
	reader := &mcpUnitReader{}
	h := &mcpHTTP{reader: reader, authenticator: mcpUnitAuth{}, settings: settings, publicOrigin: "https://catalog.example.com", version: "unit", timeout: time.Second, maxBody: 256 << 10, slots: make(chan struct{}, 1), logger: slog.New(slog.DiscardHandler), metrics: observability.New(time.Now)}
	invoke := func(path, method, body, token string, headers http.Header) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Mcp-Protocol-Version", "2025-11-25")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		for name, values := range headers {
			r.Header[name] = values
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "PRIVATE") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("unsafe adapter output", w.Body.String())
		}
		return w
	}
	valid := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"catalog_control_access","arguments":{}}}`
	if w := invoke("/mcp", http.MethodPost, valid, "synthetic-unit-token-589", nil); w.Code != 200 || strings.Contains(w.Body.String(), `"isError":true`) || reader.calls != 1 {
		t.Fatal(w.Code, w.Body.String(), reader.calls)
	}
	for _, tc := range []struct {
		name, path, method, body, token string
		headers                         http.Header
		status                          int
	}{
		{"absent token", "/mcp", http.MethodPost, valid, "", nil, 401},
		{"invalid token", "/mcp", http.MethodPost, valid, "invalid", nil, 401},
		{"query", "/mcp?url=private", http.MethodPost, valid, "synthetic-unit-token-589", nil, 400},
		{"origin", "/mcp", http.MethodPost, valid, "synthetic-unit-token-589", http.Header{"Origin": {"null"}}, 403},
		{"GET stream", "/mcp", http.MethodGet, "", "", nil, 405},
		{"malformed", "/mcp", http.MethodPost, "[]", "synthetic-unit-token-589", nil, 400},
		{"oversized", "/mcp", http.MethodPost, strings.Repeat("x", 256<<10+1), "synthetic-unit-token-589", nil, 413},
		{"identifier reflection", "/mcp", http.MethodPost, strings.Replace(valid, `"id":1`, `"id":"synthetic-unit-token-589"`, 1), "synthetic-unit-token-589", nil, 400},
		{"unsupported name encoding", "/mcp", http.MethodPost, valid, "synthetic-unit-token-589", http.Header{"Mcp-Name": {"=?base64?!!!?="}}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := reader.calls
			w := invoke(tc.path, tc.method, tc.body, tc.token, tc.headers)
			if w.Code != tc.status || reader.calls != before {
				t.Fatalf("%d %s reads=%d", w.Code, w.Body.String(), reader.calls-before)
			}
		})
	}
	h.authenticator = mcpUnitAuth{failure: identity.E(identity.CodeDependency, "PRIVATE_STORE_OUTAGE")}
	if w := invoke("/mcp", http.MethodPost, valid, "synthetic-unit-token-589", nil); w.Code != 503 || w.Header().Get("WWW-Authenticate") != "" {
		t.Fatal(w.Code, w.Header())
	}
	h.authenticator = mcpUnitAuth{}
	h.enforceScopes = true
	resource := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"catalog_control_search","arguments":{}}}`
	if w := invoke("/mcp", http.MethodPost, resource, "synthetic-unit-token-589", nil); w.Code != 403 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "insufficient_scope") {
		t.Fatal(w.Code, w.Body.String())
	}
	reader.panicRead = true
	if w := invoke("/mcp", http.MethodPost, valid, "synthetic-unit-token-589", nil); w.Code != 500 {
		t.Fatal("panic not contained", w.Code)
	}
	reader.panicRead = false
	if w := invoke("/mcp", http.MethodPost, valid, "synthetic-unit-token-589", nil); w.Code != 200 {
		t.Fatal("panic poisoned next caller", w.Code)
	}
	reader.failure = errors.New("PRIVATE_READER_FAILURE")
	before := reader.calls
	if w := invoke("/mcp", http.MethodPost, valid, "synthetic-unit-token-589", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"isError":true`) || reader.calls != before+1 || w.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("application read failure lost the safe MCP tool-error contract", w.Code, w.Body.String())
	}
	reader.failure = nil
	h.slots <- struct{}{}
	if w := invoke("/mcp", http.MethodPost, valid, "synthetic-unit-token-589", nil); w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("overload", w.Code)
	}
	<-h.slots
	if w := invoke("/mcp", "OPTIONS", "", "", http.Header{"Origin": {h.publicOrigin}, "Access-Control-Request-Method": {"POST"}}); w.Code != 204 {
		t.Fatal("preflight", w.Code)
	}
	if w := invoke(mcpMetadataPath, http.MethodGet, "", "", nil); w.Code != 404 {
		t.Fatal("development metadata", w.Code)
	}
	h.issuer = "https://issuer.example.com"
	for _, method := range []string{"GET", "HEAD", "POST"} {
		w := invoke(mcpMetadataPath, method, "", "", nil)
		if method == "POST" {
			if w.Code != 405 {
				t.Fatal(w.Code)
			}
			continue
		}
		if w.Code != 200 || method == "HEAD" && w.Body.Len() != 0 || method == "GET" && !strings.Contains(w.Body.String(), h.publicOrigin+"/mcp") {
			t.Fatal(method, w.Code, w.Body.String())
		}
	}
}

func TestMCPMirroredNameDecodingAndNoCommandClassification(t *testing.T) {
	name := "catalog_control_access"
	for _, value := range []string{name, "=?base64?" + base64.StdEncoding.EncodeToString([]byte(name)) + "?="} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", bytes.NewReader(nil))
		r.Header.Set("Mcp-Name", value)
		if !normalizeMCPName(r) || r.Header.Get("Mcp-Name") != name || requiresIdempotency(r) {
			t.Fatal("name/command admission", value)
		}
	}
	if !mcpResourceTool("catalog_control_release") || mcpResourceTool("catalog_control_access") {
		t.Fatal("tool scope classifier")
	}
	for _, value := range []string{"=?base64?YQ==", "=?base64?" + strings.Repeat("A", 1025) + "?=", "=?base64?/w==?=", "=?base64?YQ0K?="} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
		r.Header.Set("Mcp-Name", value)
		if normalizeMCPName(r) || r.Header.Get("Mcp-Name") != value {
			t.Fatal("invalid mirrored name was accepted or rewritten")
		}
	}
}

type mcpFaultAuth struct {
	cancel context.CancelFunc
	panic  bool
}

func (a mcpFaultAuth) Authenticate(context.Context, string) (identity.Actor, error) {
	if a.panic {
		panic("PRIVATE_AUTH_PANIC")
	}
	a.cancel()
	return identity.Actor{}, errors.New("PRIVATE_CANCELLED_AUTH")
}

func TestMCPHTTPAuthenticationFaultAndUnreadableBody(t *testing.T) {
	for _, mode := range []string{"cancelled authentication", "authentication panic", "unreadable body"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader := &mcpUnitReader{}
			h := &mcpHTTP{reader: reader, authenticator: mcpUnitAuth{}, settings: config.Defaults().MCP, publicOrigin: "https://catalog.example.com", timeout: time.Second, maxBody: 256 << 10, slots: make(chan struct{}, 1), logger: slog.New(slog.DiscardHandler), metrics: observability.New(time.Now)}
			body := io.Reader(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			status := http.StatusServiceUnavailable
			switch mode {
			case "cancelled authentication":
				h.authenticator = mcpFaultAuth{cancel: cancel}
			case "authentication panic":
				h.authenticator = mcpFaultAuth{panic: true}
				status = http.StatusInternalServerError
			case "unreadable body":
				body = iotest.ErrReader(errors.New("PRIVATE_BODY_FAILURE"))
				status = http.StatusBadRequest
			}
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", body)
			r.Header.Set("Authorization", "Bearer synthetic-unit-token-589")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != status || reader.calls != 0 || len(h.slots) != 0 || strings.Contains(w.Body.String(), "PRIVATE") || w.Header().Get("WWW-Authenticate") != "" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("fault response = %d %s, reads=%d slots=%d", w.Code, w.Body.String(), reader.calls, len(h.slots))
			}
		})
	}
}
