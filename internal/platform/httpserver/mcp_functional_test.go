//go:build functional

package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/catalogmcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

func mcpWire(t *testing.T, version, method, name string, args any) []byte {
	t.Helper()
	params := map[string]any{}
	if name != "" {
		params["name"] = name
		params["arguments"] = args
	}
	if version >= "2026-07-28" {
		params["_meta"] = map[string]any{mcp.MetaKeyProtocolVersion: version, mcp.MetaKeyClientInfo: map[string]any{"name": "local-test", "version": "1"}, mcp.MetaKeyClientCapabilities: map[string]any{}, "test.example/extension": true}
	}
	if method == "initialize" {
		params = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "legacy-test", "version": "1"}}
	}
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func mcpRequest(t *testing.T, client *http.Client, target, token, version, method, name string, raw []byte, headers http.Header) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 256<<10 {
		req.Body = io.NopCloser(bytes.NewReader(raw))
		req.ContentLength = -1
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if version != "" {
		req.Header.Set("Mcp-Protocol-Version", version)
	}
	if version >= "2026-07-28" {
		req.Header.Set("Mcp-Method", method)
		if name != "" {
			req.Header.Set("Mcp-Name", name)
		}
	}
	for name, values := range headers {
		if values == nil {
			req.Header.Del(name)
		} else {
			req.Header[name] = values
		}
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Mcp-Session-Id") != "" || strings.Contains(response.Header.Get("Content-Type"), "event-stream") {
		t.Fatalf("unsafe HTTP response headers: %v", response.Header)
	}
	return response.StatusCode, response.Header, body
}

func TestMCPHTTPActualCatalogBothProtocolsAndOAuthScopes(t *testing.T) {
	t.Setenv("MCPGODEBUG", "")
	server := newConfiguredHTTPServer(t, func(d *Dependencies) {
		d.MCP = config.Defaults().MCP
		d.MCP.Enabled = true
		d.PublicBaseURL = "https://catalog.example.com"
		d.AuthIssuer = "https://issuer.example.com"
		original := d.Authenticator
		d.Authenticator = mcpAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			// #nosec G101 -- synthetic local fixture token, never a production credential.
			if token == "mcp-functional-reader-token-497" {
				token = "reader"
			}
			return original.Authenticate(ctx, token)
		})
	})
	defer server.Close()
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		for _, call := range []struct {
			method, name string
			args         any
		}{{"tools/list", "", nil}, {"tools/call", "catalog_control_capabilities", map[string]any{}}, {"tools/call", "catalog_control_access", map[string]any{}}, {"tools/call", "catalog_control_search", map[string]any{}}, {"tools/call", "catalog_control_explore", map[string]any{"facets": []map[string]any{{"field": "kind"}}}}} {
			status, _, body := mcpRequest(t, server.Client(), server.URL+"/mcp", "mcp-functional-reader-token-497", version, call.method, call.name, mcpWire(t, version, call.method, call.name, call.args), nil)
			if status != 200 || strings.Contains(string(body), `"error"`) || strings.Contains(string(body), `"isError":true`) {
				t.Fatalf("%s %s %s = %d %s", version, call.method, call.name, status, body)
			}
			if call.method == "tools/list" {
				var result struct {
					Result struct {
						Tools []json.RawMessage `json:"tools"`
					} `json:"result"`
				}
				if json.Unmarshal(body, &result) != nil || len(result.Result.Tools) != 6 {
					t.Fatal("tool inventory", string(body))
				}
			}
		}
	}
	status, _, body := mcpRequest(t, server.Client(), server.URL+"/mcp", "mcp-functional-reader-token-497", "2026-07-28", "server/discover", "", mcpWire(t, "2026-07-28", "server/discover", "", nil), nil)
	if status != 200 || !strings.Contains(string(body), "2026-07-28") {
		t.Fatalf("modern discover: %d %s", status, body)
	}
	status, _, body = mcpRequest(t, server.Client(), server.URL+"/mcp", "mcp-functional-reader-token-497", "", "initialize", "", mcpWire(t, "", "initialize", "", nil), nil)
	if status != 200 || !strings.Contains(string(body), "2025-11-25") {
		t.Fatalf("initialize: %d %s", status, body)
	}
	raw := bytes.ReplaceAll(mcpWire(t, "", "initialize", "", nil), []byte("2025-11-25"), []byte("2024-11-05"))
	status, _, body = mcpRequest(t, server.Client(), server.URL+"/mcp", "mcp-functional-reader-token-497", "", "initialize", "", raw, nil)
	if status != 200 || !strings.Contains(string(body), "2025-11-25") {
		t.Fatalf("legacy negotiation: %d %s", status, body)
	}
	status, _, body = mcpRequest(t, server.Client(), server.URL+"/mcp", "mcp-functional-reader-token-497", "2025-11-25", "notifications/initialized", "", []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), nil)
	if status != 202 || len(body) != 0 {
		t.Fatalf("notification: %d %s", status, body)
	}
	// Global Control CORS must not bypass MCP's exact, separate preflight policy.
	for _, token := range []string{"", "invalid"} {
		status, header, _ := mcpRequest(t, server.Client(), server.URL+"/mcp", token, "2025-11-25", "tools/list", "", mcpWire(t, "2025-11-25", "tools/list", "", nil), nil)
		if status != 401 || !strings.Contains(header.Get("WWW-Authenticate"), "resource_metadata=\"https://catalog.example.com/") {
			t.Fatal(status, header)
		}
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+mcpMetadataPath, nil)
	req.Host = "forged.example.com"
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"resource":"https://catalog.example.com/mcp"`) || strings.Contains(string(raw), "forged") {
		t.Fatalf("metadata: %s %v", raw, err)
	}
	scopeServer := newConfiguredHTTPServer(t, func(d *Dependencies) {
		d.MCP = config.Defaults().MCP
		d.MCP.Enabled = true
		d.PublicBaseURL = "https://catalog.example.com"
		d.EnforceOAuthScopes = true
		original := d.Authenticator
		d.Authenticator = mcpAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			// #nosec G101 -- synthetic local fixture token, never a production credential.
			if token == "mcp-functional-reader-token-497" {
				token = "reader"
			}
			return original.Authenticate(ctx, token)
		})
	})
	defer scopeServer.Close()
	for _, name := range []string{"catalog_control_access", "catalog_control_capabilities", "catalog_control_search", "catalog_control_explore", "catalog_control_release", "catalog_control_indexed_record"} {
		args := map[string]any{}
		if name == "catalog_control_release" {
			args["releaseId"] = "00000000-0000-7000-8000-000000000099"
		}
		if name == "catalog_control_indexed_record" {
			args = map[string]any{"identifier": "urn:air:example.com:agents:one", "versionPresent": false}
		}
		status, header, body := mcpRequest(t, scopeServer.Client(), scopeServer.URL+"/mcp", "mcp-functional-reader-token-497", "2026-07-28", "tools/call", name, mcpWire(t, "2026-07-28", "tools/call", name, args), nil)
		expected := 200
		if mcpResourceTool(name) {
			expected = 403
		}
		if status != expected || status == 403 && !strings.Contains(header.Get("WWW-Authenticate"), `scope="catalog.read"`) {
			t.Fatalf("scope %s: %d %s", name, status, body)
		}
	}
}

type mcpAuthenticator func(context.Context, string) (identity.Actor, error)

func (f mcpAuthenticator) Authenticate(ctx context.Context, token string) (identity.Actor, error) {
	return f(ctx, token)
}

type mcpFaultReader struct {
	catalogmcp.Reader
	access func(context.Context, url.Values) (controlclient.Envelope, error)
}

func (r mcpFaultReader) ReadAccess(ctx context.Context, v url.Values) (controlclient.Envelope, error) {
	return r.access(ctx, v)
}

func newMCPFaultHTTP(t *testing.T, reader catalogmcp.Reader, auth mcpAuthenticator, timeout time.Duration, registries ...*observability.Registry) *httptest.Server {
	t.Helper()
	settings := config.Defaults().MCP
	settings.Enabled = true
	settings.MaxConcurrency = 1
	registry := observability.New(time.Now)
	if len(registries) != 0 {
		registry = registries[0]
	}
	endpoint := &mcpHTTP{reader: reader, authenticator: auth, settings: settings, publicOrigin: "https://catalog.example.com", version: "test", timeout: timeout, maxBody: 256 << 10, slots: make(chan struct{}, 1), logger: slog.New(slog.DiscardHandler), metrics: registry}
	return httptest.NewServer(requestIDMiddleware(securityHeaders(endpoint)))
}
func TestMCPHTTPAdmissionFailuresDoNotRead(t *testing.T) {
	t.Setenv("MCPGODEBUG", "")
	var reads atomic.Int32
	reader := mcpFaultReader{access: func(context.Context, url.Values) (controlclient.Envelope, error) {
		reads.Add(1)
		return controlclient.Envelope{}, errors.New("PRIVATE_DEPENDENCY")
	}}
	auth := mcpAuthenticator(func(context.Context, string) (identity.Actor, error) {
		return projectionActor(identity.RoleReader), nil
	})
	server := newMCPFaultHTTP(t, reader, auth, time.Second)
	defer server.Close()
	valid := mcpWire(t, "2026-07-28", "tools/call", "catalog_control_access", map[string]any{})
	for _, tc := range []struct {
		name, version, method string
		body                  []byte
		headers               http.Header
		status                int
		code                  string
	}{
		{"missing method header", "2026-07-28", "tools/call", valid, http.Header{"Mcp-Method": nil}, 400, "-32020"},
		{"mirrored method", "2026-07-28", "tools/call", valid, http.Header{"Mcp-Method": {"tools/list"}}, 400, "-32020"},
		{"encoded name rejection", "2026-07-28", "tools/call", valid, http.Header{"Mcp-Name": {"=?base64?!!!!?="}}, 400, "-32020"},
		{"mirrored name", "2026-07-28", "tools/call", valid, http.Header{"Mcp-Name": {"PRIVATE_ARGUMENT"}}, 400, "-32020"},
		{"metadata version", "2026-07-28", "tools/call", bytes.ReplaceAll(valid, []byte("2026-07-28"), []byte("2025-11-25")), nil, 400, "-32020"},
		{"modern version", "2027-01-01", "tools/call", mcpWire(t, "2027-01-01", "tools/call", "catalog_control_access", map[string]any{}), nil, 400, "-32022"},
		{"subscription", "2026-07-28", "subscriptions/listen", mcpWire(t, "2026-07-28", "subscriptions/listen", "", nil), nil, 404, "-32601"},
		{"null parameters", "2025-11-25", "tools/list", []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":null}`), nil, 400, "-32602"},
		{"duplicate", "2025-11-25", "tools/list", []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"tools/call"}`), nil, 400, "-32700"},
		{"chunked oversized", "2025-11-25", "tools/list", []byte(strings.Repeat("x", 256<<10+1)), nil, 413, "-32600"},
		{"media", "2025-11-25", "tools/list", valid, http.Header{"Content-Type": {"text/plain"}}, 415, "-32600"},
		{"origin", "2025-11-25", "tools/list", valid, http.Header{"Origin": {"null"}}, 403, "-32600"},
		{"ambiguous auth", "2025-11-25", "tools/list", valid, http.Header{"Authorization": {"Bearer one", "Bearer two"}}, 400, "-32600"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body := mcpRequest(t, server.Client(), server.URL+"/mcp", "synthetic-token", tc.version, tc.method, "catalog_control_access", tc.body, tc.headers)
			if status != tc.status || !strings.Contains(string(body), tc.code) || strings.Contains(string(body), "PRIVATE") {
				t.Fatalf("%d %s", status, body)
			}
		})
	}
	if reads.Load() != 0 {
		t.Fatal("refused requests reached reads", reads.Load())
	}
	for _, method := range []string{"GET", "DELETE", "PUT"} {
		req, _ := http.NewRequestWithContext(t.Context(), method, server.URL+"/mcp", nil)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST, OPTIONS" {
			t.Fatal(method, resp.StatusCode, resp.Header)
		}
	}
}

func TestMCPHTTPCancellationOverloadPanicAndCallerIsolation(t *testing.T) {
	t.Setenv("MCPGODEBUG", "")
	entered := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	var panicNext atomic.Bool
	reader := mcpFaultReader{access: func(ctx context.Context, _ url.Values) (controlclient.Envelope, error) {
		if panicNext.Swap(false) {
			panic("PRIVATE_PANIC")
		}
		entered <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
		return controlclient.Envelope{}, ctx.Err()
	}}
	auth := mcpAuthenticator(func(context.Context, string) (identity.Actor, error) {
		return projectionActor(identity.RoleReader), nil
	})
	server := newMCPFaultHTTP(t, reader, auth, 2*time.Second)
	defer server.Close()
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		ctx, cancel := context.WithCancel(t.Context())
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/mcp", bytes.NewReader(mcpWire(t, version, "tools/call", "catalog_control_access", map[string]any{})))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer first")
		req.Header.Set("Mcp-Protocol-Version", version)
		if version >= "2026-07-28" {
			req.Header.Set("Mcp-Method", "tools/call")
			req.Header.Set("Mcp-Name", "catalog_control_access")
		}
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			resp, _ := server.Client().Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
		}()
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("read not entered")
		}
		status, _, _ := mcpRequest(t, server.Client(), server.URL+"/mcp", "second", version, "tools/list", "", mcpWire(t, version, "tools/list", "", nil), nil)
		if status != 503 {
			t.Fatal("concurrency gate not applied to list", status)
		}
		cancel()
		select {
		case <-cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("HTTP cancellation did not reach read", version)
		}
		<-finished
		// Wait for the cancelled handler to release its sole slot, without sleeping.
		deadline := time.Now().Add(3 * time.Second)
		for {
			status, _, _ := mcpRequest(t, server.Client(), server.URL+"/mcp", "second", version, "tools/list", "", mcpWire(t, version, "tools/list", "", nil), nil)
			if status == 200 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("gate did not recover")
			}
		}
		panicNext.Store(true)
		status, _, body := mcpRequest(t, server.Client(), server.URL+"/mcp", "second", version, "tools/call", "catalog_control_access", mcpWire(t, version, "tools/call", "catalog_control_access", map[string]any{}), nil)
		if status != 500 || strings.Contains(string(body), "PRIVATE_PANIC") {
			t.Fatalf("panic: %d %s", status, body)
		}
		status, _, _ = mcpRequest(t, server.Client(), server.URL+"/mcp", "second", version, "tools/list", "", mcpWire(t, version, "tools/list", "", nil), nil)
		if status != 200 {
			t.Fatal("one request panic stopped another caller")
		}
	}
}

func TestMCPHTTPDependencyRefusalsDeadlineAndDisabledRoutes(t *testing.T) {
	t.Setenv("MCPGODEBUG", "")
	var reads atomic.Int32
	reader := mcpFaultReader{access: func(ctx context.Context, _ url.Values) (controlclient.Envelope, error) {
		reads.Add(1)
		<-ctx.Done()
		return controlclient.Envelope{}, ctx.Err()
	}}
	for _, dependency := range []bool{false, true} {
		server := newMCPFaultHTTP(t, reader, mcpAuthenticator(func(context.Context, string) (identity.Actor, error) {
			if dependency {
				return identity.Actor{}, identity.E(identity.CodeDependency, "PRIVATE_STORE_OUTAGE")
			}
			return identity.Actor{}, errors.New("PRIVATE_TOKEN_FAILURE")
		}), time.Second)
		status, headers, body := mcpRequest(t, server.Client(), server.URL+"/mcp", "synthetic-bearer-882", "2025-11-25", "tools/call", "catalog_control_access", mcpWire(t, "2025-11-25", "tools/call", "catalog_control_access", map[string]any{}), nil)
		server.Close()
		expected := 401
		if dependency {
			expected = 503
		}
		if status != expected || strings.Contains(string(body), "PRIVATE") || dependency && headers.Get("WWW-Authenticate") != "" {
			t.Fatal("incorrect auth failure", status, headers, string(body))
		}
	}
	if reads.Load() != 0 {
		t.Fatal("authentication refusal reached store")
	}
	server := newMCPFaultHTTP(t, reader, mcpAuthenticator(func(context.Context, string) (identity.Actor, error) {
		return projectionActor(identity.RoleReader), nil
	}), 20*time.Millisecond)
	defer server.Close()
	status, _, body := mcpRequest(t, server.Client(), server.URL+"/mcp", "synthetic-bearer-882", "2025-11-25", "tools/call", "catalog_control_access", mcpWire(t, "2025-11-25", "tools/call", "catalog_control_access", map[string]any{}), nil)
	if status != 503 || !strings.Contains(string(body), `"jsonrpc":"2.0"`) || strings.Contains(string(body), "problem") {
		t.Fatalf("MCP deadline response %d %s", status, body)
	}
	disabled := newWebhookHTTPServer(t)
	defer disabled.Close()
	for _, path := range []string{"/mcp", mcpMetadataPath} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, disabled.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := disabled.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatal("disabled route available", path)
		}
	}
}

func TestMCPHTTPCurrentMembershipRevocationWithoutSession(t *testing.T) {
	t.Setenv("MCPGODEBUG", "")
	var identities *identity.Service
	var original mcpAuthenticator
	server := newConfiguredHTTPServer(t, func(d *Dependencies) {
		identities = d.Identity
		base := d.Authenticator
		original = base.Authenticate
		d.MCP = config.Defaults().MCP
		d.MCP.Enabled = true
		d.PublicBaseURL = "https://catalog.example.com"
		d.Authenticator = mcpAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			// #nosec G101 -- synthetic local fixture token, never a production credential.
			if token == "mcp-current-member-token-882" {
				token = "reader"
			}
			return base.Authenticate(ctx, token)
		})
	})
	defer server.Close()
	call := func(token string) int {
		status, _, _ := mcpRequest(t, server.Client(), server.URL+"/mcp", token, "2026-07-28", "tools/call", "catalog_control_access", mcpWire(t, "2026-07-28", "tools/call", "catalog_control_access", map[string]any{}), nil)
		return status
	}
	if call("mcp-current-member-token-882") != 200 {
		t.Fatal("active member refused")
	}
	admin, err := original(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPermissionContext(identity.WithActor(t.Context(), admin))
	page, err := identities.ListMemberships(ctx, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	suspended := false
	for _, member := range page.Items {
		if member.Subject == "reader" {
			if _, err := identities.SuspendMembership(ctx, member.ID, "Qualify MCP current membership", "mcp-membership-test", member.Revision); err != nil {
				t.Fatal(err)
			}
			suspended = true
		}
	}
	if !suspended || call("mcp-current-member-token-882") != 401 {
		t.Fatal("revoked membership was reused")
	}
	if call("other-admin") != 200 {
		t.Fatal("another tenant's independent caller was affected")
	}
}

func TestMCPHTTPSimultaneousCallerIsolationAndShutdown(t *testing.T) {
	t.Setenv("MCPGODEBUG", "")
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			entered := make(chan string, 2)
			cancelled := make(chan string, 2)
			release := make(chan struct{})
			reader := mcpFaultReader{access: func(ctx context.Context, _ url.Values) (controlclient.Envelope, error) {
				actor, _ := identity.FromContext(ctx)
				entered <- actor.Subject
				select {
				case <-ctx.Done():
					cancelled <- actor.Subject
					return controlclient.Envelope{}, ctx.Err()
				case <-release:
					return controlclient.Envelope{Data: json.RawMessage(`{"tenantId":"00000000-0000-7000-8000-000000000001","permissions":[],"availablePermissions":[],"namespaceGrants":[],"authorizationRevision":1,"scopeEnforcement":false,"scopes":[]}`)}, nil
				}
			}}
			settings := config.Defaults().MCP
			h := &mcpHTTP{reader: reader, authenticator: mcpAuthenticator(func(_ context.Context, token string) (identity.Actor, error) {
				actor := projectionActor(identity.RoleReader)
				actor.Subject = token
				return actor, nil
			}), settings: settings, publicOrigin: "https://catalog.example.com", timeout: 5 * time.Second, maxBody: 256 << 10, slots: make(chan struct{}, 2), logger: slog.New(slog.DiscardHandler)}
			server := httptest.NewServer(h)
			defer server.Close()
			firstCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type response struct {
				status int
				body   []byte
				err    error
			}
			start := func(ctx context.Context, token string) <-chan response {
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/mcp", bytes.NewReader(mcpWire(t, version, "tools/call", "catalog_control_access", map[string]any{})))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("Mcp-Protocol-Version", version)
				if version == "2026-07-28" {
					req.Header.Set("Mcp-Method", "tools/call")
					req.Header.Set("Mcp-Name", "catalog_control_access")
				}
				result := make(chan response, 1)
				go func() {
					resp, err := server.Client().Do(req)
					if err != nil {
						result <- response{err: err}
						return
					}
					body, err := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					result <- response{resp.StatusCode, body, err}
				}()
				return result
			}
			first := start(firstCtx, "caller-one-497")
			second := start(t.Context(), "caller-two-882")
			seen := map[string]bool{}
			for range 2 {
				select {
				case subject := <-entered:
					seen[subject] = true
				case <-time.After(3 * time.Second):
					t.Fatal("both callers did not enter their reads")
				}
			}
			if !seen["caller-one-497"] || !seen["caller-two-882"] {
				t.Fatal("caller identity was shared", seen)
			}
			cancel()
			select {
			case subject := <-cancelled:
				if subject != "caller-one-497" {
					t.Fatal("wrong caller cancelled", subject)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("first caller cancellation was lost")
			}
			if result := <-first; result.err == nil {
				t.Fatal("cancelled client unexpectedly completed")
			}
			stopping := make(chan struct{})
			server.Config.RegisterOnShutdown(func() { close(stopping) })
			shutdown := make(chan error, 1)
			shutdownCtx, stopShutdown := context.WithTimeout(t.Context(), 4*time.Second)
			defer stopShutdown()
			go func() { shutdown <- server.Config.Shutdown(shutdownCtx) }()
			<-stopping
			select {
			case result := <-second:
				t.Fatal("shutdown or another caller ended the active read", result.status, result.err)
			case subject := <-cancelled:
				t.Fatal("another caller cancelled", subject)
			default:
			}
			close(release)
			result := <-second
			if result.err != nil || result.status != 200 || strings.Contains(string(result.body), `"isError":true`) {
				t.Fatal("active read did not drain successfully", result.status, result.err, string(result.body))
			}
			if err := <-shutdown; err != nil {
				t.Fatal("shutdown failed", err)
			}
		})
	}
}

func TestMCPHTTPLoopbackHostProtection(t *testing.T) {
	t.Setenv("MCPGODEBUG", "")
	server := newConfiguredHTTPServer(t, func(d *Dependencies) {
		d.MCP = config.Defaults().MCP
		d.MCP.Enabled = true
		d.PublicBaseURL = "https://catalog.example.com"
		original := d.Authenticator
		d.Authenticator = mcpAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			// #nosec G101 -- synthetic local fixture token, never a production credential.
			if token == "mcp-loopback-reader-token-832" {
				token = "reader"
			}
			return original.Authenticate(ctx, token)
		})
	})
	defer server.Close()
	raw := mcpWire(t, "2026-07-28", "tools/list", "", nil)
	status, _, _ := mcpRequest(t, server.Client(), server.URL+"/mcp", "mcp-loopback-reader-token-832", "2026-07-28", "tools/list", "", raw, nil)
	if status != http.StatusOK {
		t.Fatal("literal loopback control failed", status)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/mcp", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "catalog.example.com"
	req.Header.Set("Authorization", "Bearer mcp-loopback-reader-token-832")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/list")
	req.Header.Set("X-Forwarded-Host", "127.0.0.1")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusForbidden || bytes.Contains(body, []byte("catalog.example.com")) || bytes.Contains(body, []byte("Bearer mcp-loopback-reader-token-832")) {
		t.Fatal("loopback protection or safe SDK error projection failed", resp.StatusCode, err)
	}
}
