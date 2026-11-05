//go:build functional

package catalogmcp

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
)

func TestServePrivateTLSCredentialSnapshotAndCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	started, cancelled, timedOut := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer startup-token" || r.URL.Path != "/api/v1/capabilities" {
			t.Error("wrong credential or destination")
		}
		n := requests.Add(1)
		if n == 3 {
			close(started)
			<-r.Context().Done()
			close(cancelled)
			return
		}
		if n == 4 {
			<-r.Context().Done()
			close(timedOut)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "tls-control")
		if n == 2 {
			_, _ = w.Write([]byte(`{"resourceKinds":["agent","mcp_server","skill","plugin"],"search":{},"limits":{"padding":"` + strings.Repeat("x", 600) + `"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"resourceKinds":["agent","mcp_server","skill","plugin"],"search":{},"limits":{}}`))
	}))
	t.Cleanup(upstream.Close)
	ca, token := filepath.Join(t.TempDir(), "ca.pem"), filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("startup-token"), 0600); err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	limits := DefaultLimits()
	limits.ToolBytes = 1024
	limits.Timeout = 2 * time.Second
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, controlclient.Options{BaseURL: upstream.URL, TokenFile: token, CAFile: ca}, limits, serverTransport, "1.2.3-test")
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "owned-tls-control", Version: "1"}, &mcp.ClientOptions{Logger: slog.New(slog.DiscardHandler)})
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if session.InitializeResult().ServerInfo.Version != "1.2.3-test" {
		t.Fatal("server did not report its application version")
	}
	if result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catalog_control_capabilities"}); err != nil || result.IsError {
		t.Fatal("private TLS control", result, err)
	} else if wire, err := json.Marshal(result); err != nil || len(wire) > limits.ToolBytes {
		t.Fatal("actual successful wire result exceeded the limit", len(wire), err)
	}
	if err := os.WriteFile(token, []byte("rotated-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catalog_control_capabilities"}); err != nil || !result.IsError || result.StructuredContent != nil || !strings.HasPrefix(toolText(t, result), "output_limit:") {
		t.Fatal("oversized projected result was accepted", result, err)
	} else if wire, err := json.Marshal(result); err != nil || len(wire) > limits.ToolBytes {
		t.Fatal("actual refusal wire result exceeded the limit", len(wire), err)
	}
	callCtx, stopCall := context.WithCancel(ctx)
	callDone := make(chan error, 1)
	go func() {
		_, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: "catalog_control_capabilities"})
		callDone <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("call never reached upstream")
	}
	stopCall()
	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("cancelled call succeeded")
		}
	case <-ctx.Done():
		t.Fatal("MCP cancellation did not finish")
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("MCP cancellation did not reach HTTP")
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catalog_control_capabilities"})
	if err != nil || !result.IsError || result.StructuredContent != nil {
		t.Fatal("configured call deadline did not produce a complete safe refusal", result, err)
	}
	// Serve gives the call and HTTP transport the same budget. Either timer
	// can win; both categories must remain safe and cancel the actual request.
	text := toolText(t, result)
	if text != "cancelled: The request was cancelled or its deadline expired." && text != "api_failure: The API request failed or its response was invalid; no dependency details are exposed." {
		t.Fatal("configured deadline returned an unexpected refusal")
	}
	select {
	case <-timedOut:
	case <-ctx.Done():
		t.Fatal("configured deadline did not cancel the actual HTTP request")
	}
	if result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catalog_control_capabilities"}); err != nil || result.IsError || requests.Load() != 5 {
		t.Fatal("call timeout retried or poisoned the MCP session", result, err, requests.Load())
	}
	// Deliberate parent cancellation owns this shutdown assertion. Closing both
	// ends of net.Pipe while the cancelled reply is pending can fail its write;
	// clean stdin EOF is separately qualified by the compiled stdio fixture.
	cancel()
	select {
	case err := <-done:
		if err == nil || err.Error() != "mcp: protocol session failed or was cancelled" {
			t.Fatal("unsafe cancellation shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not drain after cancellation")
	}
}

func TestServeIndexedSelectionRefusalsAndEOF(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	const token = "owned-indexed-token"
	item := indexedResult()
	nullVersion := strings.TrimSuffix(string(encoded(t, item)), "}") + `,"version":null}`
	reflected := item
	reflected.Description = token
	forbidden := atomic.Int64{}
	requests := atomic.Int64{}
	mode := atomic.Int64{}
	faults := []struct {
		name, body, code string
		status           int
	}{
		{"unauthenticated", `{"detail":"` + token + `"}`, "api_denied:", http.StatusUnauthorized},
		{"forbidden", `{"detail":"` + token + `"}`, "api_denied:", http.StatusForbidden},
		{"missing", `{"detail":"` + token + `"}`, "api_not_found:", http.StatusNotFound},
		{"unavailable", `{"detail":"` + token + `"}`, "api_failure:", http.StatusServiceUnavailable},
		{"redirect", `{}`, "api_failure:", http.StatusFound},
		{"null-version", nullVersion, "invalid_response:", http.StatusOK},
		{"credential-reflection", strings.Replace(string(encoded(t, reflected)), token, `\u006fwned-indexed-token`, 1), "invalid_response:", http.StatusOK},
		{"oversized-valid-json", `{"padding":"` + strings.Repeat("x", 2048) + `"}`, "api_failure:", http.StatusOK},
	}
	queries := make(chan url.Values, 32)
	nextQuery := func(t *testing.T) url.Values {
		t.Helper()
		select {
		case query := <-queries:
			return query
		case <-ctx.Done():
			t.Fatal("expected tool call did not reach the API")
			return nil
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/must-not-follow" {
			forbidden.Add(1)
			t.Error("MCP client followed an API redirect")
			return
		}
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/catalog/indexed-record" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("wrong method, destination or credential")
		}
		queries <- r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "sdk-http-control")
		if selected := mode.Load(); selected != 0 {
			fault := faults[selected-1]
			w.Header().Set("Location", "/must-not-follow")
			w.WriteHeader(fault.status)
			_, _ = w.Write([]byte(fault.body))
			return
		}
		result := item
		result.VersionPresent = r.URL.Query().Get("versionPresent") == "true"
		result.Version = r.URL.Query().Get("version")
		if err := json.NewEncoder(w).Encode(result); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(cancel)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	limits := DefaultLimits()
	limits.ResponseBytes = 2048
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, controlclient.Options{BaseURL: upstream.URL, Token: token}, limits, serverTransport, "1.2.3-test")
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "owned-indexed-control", Version: "1"}, &mcp.ClientOptions{Logger: slog.New(slog.DiscardHandler)})
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	for _, tc := range []struct {
		name    string
		present bool
		version string
	}{
		{"absent", false, ""},
		{"present-empty", true, ""},
		{"present-nonempty", true, "1.2.3+build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"identifier": item.Identifier, "versionPresent": tc.present}
			query := url.Values{"identifier": {item.Identifier}, "versionPresent": {"false"}}
			if tc.present {
				args["version"] = tc.version
				query.Set("versionPresent", "true")
				query.Set("version", tc.version)
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catalog_control_indexed_record", Arguments: args})
			if err != nil || result.IsError {
				t.Fatal("valid indexed selection rejected", result, err)
			}
			var envelope struct {
				Data resultProjection       `json:"data"`
				Meta controlclient.Metadata `json:"meta"`
			}
			if json.Unmarshal([]byte(toolText(t, result)), &envelope) != nil || envelope.Data.Identifier != item.Identifier || envelope.Data.VersionPresent != tc.present || envelope.Data.Version != tc.version || envelope.Data.Indexed == nil || envelope.Data.Indexed.NormalizedEntryDigest != testDigest || envelope.Meta.RequestID != "sdk-http-control" || !reflect.DeepEqual(nextQuery(t), query) {
				t.Fatal("indexed identity, version selection, digest or metadata changed", result)
			}
		})
	}
	args := map[string]any{"identifier": item.Identifier, "versionPresent": true, "version": ""}
	for i, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			mode.Store(int64(i + 1))
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catalog_control_indexed_record", Arguments: args})
			if err != nil || !result.IsError || !strings.HasPrefix(toolText(t, result), fault.code) || result.StructuredContent != nil || strings.Contains(toolText(t, result), token) {
				t.Fatal("unsafe or incorrectly classified API refusal", result, err)
			}
			nextQuery(t)
		})
	}
	before := requests.Load()
	invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catalog_control_indexed_record", Arguments: map[string]any{"identifier": item.Identifier, "versionPresent": false, "version": ""}})
	if err != nil || !invalid.IsError || !strings.HasPrefix(toolText(t, invalid), "invalid_arguments:") || requests.Load() != before || forbidden.Load() != 0 {
		t.Fatal("invalid arguments or redirects reached an unselected destination", invalid, err)
	}
	mode.Store(0)
	if result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catalog_control_indexed_record", Arguments: args}); err != nil || result.IsError {
		t.Fatal("API refusals poisoned the session", result, err)
	}
	nextQuery(t)
	if requests.Load() != int64(4+len(faults)) || forbidden.Load() != 0 {
		t.Fatal("unexpected retries, omitted requests or redirect traffic")
	}
	if err := session.Close(); err != nil {
		t.Fatal("client did not close cleanly", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("idle EOF did not stop the server successfully", err)
		}
	case <-ctx.Done():
		t.Fatal("server did not finish after idle EOF")
	}
}

func TestServeDiscoveryToolsRefuseUnsafeResponses(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var mode atomic.Int64
	var requests atomic.Int64
	const token = "owned-discovery-token"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("wrong credential")
		}
		var data string
		switch r.URL.Path {
		case "/api/v1/catalog/explore":
			if r.Method != http.MethodPost {
				t.Error("wrong Explore method")
			}
			data = `{"matched":2,"facets":{"tag":{"buckets":[{"value":"sample","count":1}],"otherCount":1}}}`
		case "/api/v1/me":
			if r.Method != http.MethodGet || r.URL.Query().Get("limit") != "20" {
				t.Error("wrong access read")
			}
			data = string(accessData())
		default:
			t.Error("unexpected network destination")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "discovery-fixture")
		switch mode.Load() {
		case 1:
			w.WriteHeader(http.StatusForbidden)
			data = `{"private":"` + token + `"}`
		case 2:
			data = `{"unexpected":"private"}`
		case 3:
			data = strings.ReplaceAll(strings.ReplaceAll(data, `"sample"`, `"\u006fwned-discovery-token"`), `"catalog.read"`, `"\u006fwned-discovery-token"`)
		case 4:
			data = strings.TrimSuffix(data, "}") + `,"padding":"` + strings.Repeat("x", 9000) + `"}`
		case 5:
			if r.URL.Path == "/api/v1/catalog/explore" {
				data = strings.TrimSuffix(data, "}") + `,"tier":""}`
			} else {
				data = strings.Replace(data, `"scopeEnforcement":true`, `"scopeEnforcement":null`, 1)
			}
		}
		_, _ = w.Write([]byte(data))
	}))
	defer upstream.Close()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	limits := DefaultLimits()
	limits.ResponseBytes = 4096
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, controlclient.Options{BaseURL: upstream.URL, Token: token}, limits, serverTransport, "functional")
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "discovery-control", Version: "1"}, &mcp.ClientOptions{Logger: slog.New(slog.DiscardHandler)})
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	for _, tool := range []struct {
		name string
		args map[string]any
	}{
		{"catalog_control_explore", map[string]any{"facets": []map[string]any{{"field": "tag", "limit": 1}}}},
		{"catalog_control_access", map[string]any{}},
	} {
		for index, code := range []string{"", "api_denied:", "invalid_response:", "invalid_response:", "api_failure:", "invalid_response:"} {
			mode.Store(int64(index))
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.name, Arguments: tool.args})
			if err != nil || result == nil {
				t.Fatal(tool.name, err)
			}
			if code == "" {
				if result.IsError {
					t.Fatal("valid discovery control failed", toolText(t, result))
				}
			} else if !result.IsError || result.StructuredContent != nil || !strings.HasPrefix(toolText(t, result), code) {
				t.Fatal(tool.name, code, result)
			}
			if strings.Contains(string(encoded(t, result)), token) {
				t.Fatal("credential reflected")
			}
		}
	}
	if requests.Load() != 12 {
		t.Fatal("unexpected request or retry", requests.Load())
	}
	if err := session.Close(); err != nil {
		t.Fatal("client EOF", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("server did not drain")
	}
}
