package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func ardMCPUnit(t *testing.T, mode DiscoveryMode) (*Server, *int) {
	t.Helper()
	snapshot, _ := publicFixtureSnapshot(t, mode)
	reads := new(int)
	s := publicProjectionServer(t, mode, func(ctx context.Context, _ string) (publication.Snapshot, error) {
		*reads++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("SDK callback lost the HTTP deadline")
		}
		if requestID(ctx) != "ard-mcp-unit-request" {
			t.Fatal("SDK callback lost request correlation")
		}
		return snapshot, nil
	})
	binding := s.discovery[publicFixtureHost]
	binding.MCPEnabled = true
	s.discovery[publicFixtureHost] = binding
	h := &mcpHTTP{discovery: &ardMCPBinding{s, binding}, settings: config.Defaults().MCP, publicOrigin: publicFixtureOrigin, version: "test", timeout: time.Second, maxBody: 256 << 10, slots: make(chan struct{}, 1), logger: slog.New(slog.DiscardHandler), metrics: observability.New(time.Now)}
	s.ardMCP = map[string]*mcpHTTP{publicFixtureHost: h}
	return s, reads
}

func ardMCPRecorder(t *testing.T, s *Server, version, method, name, token string, args any) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.WithValue(t.Context(), requestIDKey{}, "ard-mcp-unit-request"), http.MethodPost, publicFixtureOrigin+ardMCPPath, strings.NewReader(string(mcpWire(t, version, method, name, args))))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Mcp-Protocol-Version", version)
	if version >= "2026-07-28" {
		r.Header.Set("Mcp-Method", method)
		if name != "" {
			r.Header.Set("Mcp-Name", name)
		}
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.handleARDMCP(w, r)
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Mcp-Session-Id") != "" {
		t.Fatal(w.Header())
	}
	return w
}

func TestARDMCPOriginPreservesDeploymentSchemeAndPort(t *testing.T) {
	for _, tc := range []struct{ base, host, want string }{
		{"https://control.example.com", publicFixtureHost, publicFixtureOrigin},
		{"https://control.example.com:8443/", publicFixtureHost, publicFixtureOrigin + ":8443"},
		{"http://127.0.0.1:8080", "::1", "http://[::1]:8080"},
		{"https://control.example.com", "2001:db8::1", "https://[2001:db8::1]"},
	} {
		if got := discoveryMCPOrigin(tc.base, tc.host); got != tc.want {
			t.Fatalf("base=%q host=%q: origin=%q, want %q", tc.base, tc.host, got, tc.want)
		}
	}
}

func TestARDMCPPublicationPagingAndSeparateInventory(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		for _, mode := range []DiscoveryMode{DiscoveryAnonymous, DiscoveryAuthenticated} {
			t.Run(version+"/"+string(mode), func(t *testing.T) {
				s, reads := ardMCPUnit(t, mode)
				token := "reader"
				if mode == DiscoveryAnonymous {
					token = "ignored-invalid-token"
				}
				method := "server/discover"
				if version == "2025-11-25" {
					method = "initialize"
				}
				if w := ardMCPRecorder(t, s, version, method, "", token, nil); w.Code != 200 || !strings.Contains(w.Body.String(), ardMCPName) {
					t.Fatal(w.Code, w.Body.String())
				}
				listed := ardMCPRecorder(t, s, version, "tools/list", "", token, nil)
				var inventory struct {
					Result struct {
						Tools []struct {
							Name        string          `json:"name"`
							InputSchema json.RawMessage `json:"inputSchema"`
						} `json:"tools"`
					} `json:"result"`
				}
				if json.Unmarshal(listed.Body.Bytes(), &inventory) != nil || listed.Code != 200 || len(inventory.Result.Tools) != 1 || inventory.Result.Tools[0].Name != "search" || *reads != 0 {
					t.Fatal("static inventory leaked another plane or read publication", listed.Body.String(), *reads)
				}
				var input struct {
					Properties struct {
						Federation struct {
							Default string `json:"default"`
						} `json:"federation"`
					} `json:"properties"`
				}
				if json.Unmarshal(inventory.Result.Tools[0].InputSchema, &input) != nil || input.Properties.Federation.Default != "auto" {
					t.Fatal("tool does not declare the REST federation default")
				}
				args := map[string]any{"query": map[string]any{"text": "fixture"}, "federation": "none", "pageSize": 1}
				seen := map[string]bool{}
				for page := 0; page < 2; page++ {
					w := ardMCPRecorder(t, s, version, "tools/call", "search", token, args)
					var result struct {
						Result struct {
							Structured json.RawMessage `json:"structuredContent"`
							Content    []struct {
								Text string `json:"text"`
							} `json:"content"`
							IsError bool `json:"isError"`
						} `json:"result"`
					}
					if json.Unmarshal(w.Body.Bytes(), &result) != nil || w.Code != 200 || result.Result.IsError || len(result.Result.Content) != 1 || string(result.Result.Structured) != result.Result.Content[0].Text {
						t.Fatal(w.Code, w.Body.String())
					}
					var response publication.SearchResponse
					if json.Unmarshal(result.Result.Structured, &response) != nil || len(response.Results) != 1 {
						t.Fatal(string(result.Result.Structured))
					}
					id := response.Results[0].Identifier
					if seen[id] || id != "urn:air:"+publicFixtureHost+":default:alpha" && id != "urn:air:"+publicFixtureHost+":default:beta" {
						t.Fatal("wrong independent fixture identity", id)
					}
					seen[id] = true
					if (response.PageToken != "") != (page == 0) {
						t.Fatal("continuation contract", response.PageToken)
					}
					args["pageToken"] = response.PageToken
				}
				before := *reads
				if w := ardMCPRecorder(t, s, version, "tools/call", "catalog_control_search", token, map[string]any{"text": "fixture"}); w.Code != 200 && w.Code != 400 {
					t.Fatal(w.Code)
				}
				if *reads != before {
					t.Fatal("Control fallback reached publication")
				}
			})
		}
	}
}

func TestARDMCPCompleteSparseOutputAndBudget(t *testing.T) {
	response := publication.SearchResponse{Results: []publication.SearchResult{{Entry: publication.Entry{Identifier: "urn:air:example.com:sparse:one", Metadata: map[string]any{"literal": json.Number("9007199254740993"), "false": false}, Data: json.RawMessage(`{"quoted":"<native>&\"","unknown":[false,null]}`)}, TrustManifest: json.RawMessage(`{"identity":"example.com","framework":{"opaque":9007199254740993}}`), Score: 0, Source: "https://peer.example/ard/v1"}}, Referrals: []publication.Referral{{Identifier: "urn:air:example.com:registry:peer", DisplayName: "Peer", Type: "application/ai-registry+json", URL: "https://peer.example/ard/v1"}}, PageToken: "exact-page-token", Partial: true, Tier: "core"}
	result := ardMCPResult(response, "test", 1<<20, "")
	if result.IsError || len(result.Content) != 1 {
		t.Fatal(result)
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal("non-text output")
	}
	text := content.Text
	var raw json.RawMessage
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil || json.Unmarshal(encoded, &raw) != nil || string(raw) != text {
		t.Fatal(text, err)
	}
	var got publication.SearchResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&got) != nil || len(got.Results) != 1 || got.Results[0].Type != "" || got.Results[0].DisplayName != "" || got.PageToken != "exact-page-token" || !reflect.DeepEqual(got.Referrals, response.Referrals) {
		t.Fatal(got)
	}
	entry := got.Results[0]
	if !reflect.DeepEqual(entry.Metadata, map[string]any{"literal": json.Number("9007199254740993"), "false": false}) {
		t.Fatalf("published scalar metadata changed: %v", entry.Metadata)
	}
	for _, field := range []struct {
		name string
		raw  json.RawMessage
		want map[string]any
	}{
		{"data", entry.Data, map[string]any{"quoted": `<native>&"`, "unknown": []any{false, nil}}},
		{"trustManifest", entry.TrustManifest, map[string]any{"identity": "example.com", "framework": map[string]any{"opaque": json.Number("9007199254740993")}}},
	} {
		var value map[string]any
		d := json.NewDecoder(bytes.NewReader(field.raw))
		d.UseNumber()
		if err := d.Decode(&value); err != nil || !reflect.DeepEqual(value, field.want) {
			t.Errorf("%s = %v, %v; want %v", field.name, value, err, field.want)
		}
	}
	for _, limit := range []int{1024, len(text)} {
		oversized := response
		oversized.Results = append([]publication.SearchResult(nil), response.Results...)
		oversized.Results[0].Description = strings.Repeat(`<"&`, limit)
		refused := ardMCPResult(oversized, "test", limit, "")
		content, ok := refused.Content[0].(*mcp.TextContent)
		if !ok || !refused.IsError || refused.StructuredContent != nil || !strings.Contains(content.Text, "output_limit") {
			t.Fatal("truncated success", refused)
		}
	}
	if r := ardMCPResult(response, "test", 1<<20, "exact-page-token"); !r.IsError || r.StructuredContent != nil {
		t.Fatal("credential reflection", r)
	}
	response.FallbackReason, response.PartialIndex = "provider_unavailable", true
	response.Diagnostics = []publication.SourceDiagnostic{{Source: "private-source-sentinel", Status: "failed", Message: "private-dependency-sentinel"}}
	result = ardMCPResult(response, "test", 1<<20, "")
	wantMeta := map[string]any{"partial": true, "tier": "core", "fallbackReason": "provider_unavailable", "partialIndex": true}
	if result.IsError || !reflect.DeepEqual(result.Meta[ardMCPMeta], wantMeta) {
		t.Fatal("lost REST search metadata", result)
	}
	encoded, err = json.Marshal(result)
	if err != nil || bytes.Contains(encoded, []byte("private-")) || bytes.Contains(encoded, []byte(`"diagnostics"`)) {
		t.Fatal("private source diagnostics crossed the MCP boundary", err)
	}
}

func TestARDMCPModernWireOutputBudgetCountsTheCompleteResult(t *testing.T) {
	s, _ := ardMCPUnit(t, DiscoveryAnonymous)
	args := map[string]any{"query": map[string]any{"text": "fixture"}, "federation": "none", "pageSize": 2}
	control := ardMCPRecorder(t, s, "2026-07-28", "tools/call", "search", "", args)
	var reply struct {
		Result json.RawMessage `json:"result"`
	}
	if control.Code != http.StatusOK || json.Unmarshal(control.Body.Bytes(), &reply) != nil || len(reply.Result) == 0 || bytes.Contains(reply.Result, []byte(`"isError":true`)) {
		t.Fatal("complete result positive control", control.Code, control.Body.String())
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(reply.Result, &fields) != nil || len(fields["structuredContent"]) == 0 || len(fields["_meta"]) == 0 {
		t.Fatal("positive control omitted result components")
	}
	// Measure the actual SDK wire result, independently of the budget helper.
	// The native payload alone fits; duplicating it as text and adding protocol
	// metadata must not allow a complete result one byte over the configured cap.
	limit := len(reply.Result) - 1
	if limit < 1024 || len(fields["structuredContent"]) >= limit {
		t.Fatal("fixture does not isolate complete-result overhead")
	}
	s.ardMCP[publicFixtureHost].settings.MaxToolBytes = limit
	refused := ardMCPRecorder(t, s, "2026-07-28", "tools/call", "search", "", args)
	var failure struct {
		Result struct {
			IsError    bool            `json:"isError"`
			Structured json.RawMessage `json:"structuredContent"`
			Content    []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if refused.Code != http.StatusOK || json.Unmarshal(refused.Body.Bytes(), &failure) != nil || !failure.Result.IsError || failure.Result.Structured != nil || len(failure.Result.Content) != 1 || !strings.HasPrefix(failure.Result.Content[0].Text, "output_limit:") || bytes.Contains(refused.Body.Bytes(), []byte("urn:air:")) {
		t.Fatal("over-budget wire result was not refused completely", refused.Code, refused.Body.String())
	}
}

func TestARDMCPSearchFailuresAreSafeToolRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		err        error
		cancel     bool
	}{
		{"invalid query", "invalid_arguments:", catalog.E(catalog.CodeInvalidArgument, "private-query-sentinel"), false},
		{"no publication", "publication_unavailable:", catalog.E(catalog.CodeNotFound, "private-publication-sentinel"), false},
		{"revoked access", "discovery_denied:", catalog.E(catalog.CodeForbidden, "private-grant-sentinel"), false},
		{"lost identity", "discovery_denied:", catalog.E(catalog.CodeUnauthenticated, "private-identity-sentinel"), false},
		{"dependency outage", "search_unavailable:", catalog.E(catalog.CodeDependencyUnavailable, "private-dependency-sentinel"), false},
		{"unexpected cause", "search_unavailable:", errors.New("private-cause-sentinel"), false},
		{"cancelled read", "cancelled:", errors.New("private-cancel-sentinel"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, _ := publicFixtureSnapshot(t, DiscoveryAnonymous)
			var cause error
			reads := 0
			projection := publicProjectionServer(t, DiscoveryAnonymous, func(context.Context, string) (publication.Snapshot, error) {
				reads++
				return snapshot, cause
			})
			h := &mcpHTTP{discovery: &ardMCPBinding{projection, projection.discovery[publicFixtureHost]}, settings: config.Defaults().MCP}
			call := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "search", Arguments: json.RawMessage(`{"query":{"text":"fixture"},"federation":"none","pageSize":2}`)}}
			control := h.callARD(t.Context(), call, "")
			if control.IsError || control.StructuredContent == nil || reads != 1 {
				t.Fatal("valid Search did not reach the selected snapshot", control, reads)
			}
			controlJSON, err := json.Marshal(control.StructuredContent)
			if err != nil || !bytes.Contains(controlJSON, []byte("urn:air:"+publicFixtureHost+":default:alpha")) || !bytes.Contains(controlJSON, []byte("urn:air:"+publicFixtureHost+":default:beta")) {
				t.Fatal("positive control did not return the expected public entries", err)
			}
			cause = tc.err
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			refused := h.callARD(ctx, call, "")
			encoded, err := json.Marshal(refused)
			if err != nil || !refused.IsError || refused.StructuredContent != nil || len(refused.Content) != 1 || reads != 2 || bytes.Contains(encoded, []byte("private-")) || bytes.Contains(encoded, []byte("urn:air:")) {
				t.Fatal("unsafe or vacuous refusal", refused, reads, err)
			}
			text, ok := refused.Content[0].(*mcp.TextContent)
			if !ok || !strings.HasPrefix(text.Text, tc.code) {
				t.Fatal("wrong public failure classification", refused)
			}
		})
	}
}

func TestARDMCPAdmissionCannotSelectAnotherPlaneOrHost(t *testing.T) {
	for name, change := range map[string]func(*Server, *http.Request){
		"unknown host": func(_ *Server, r *http.Request) { r.Host = "unknown.example.com" },
		"flag omitted": func(s *Server, _ *http.Request) {
			b := s.discovery[publicFixtureHost]
			b.MCPEnabled = false
			s.discovery[publicFixtureHost] = b
		},
		"disabled host": func(s *Server, _ *http.Request) {
			b := s.discovery[publicFixtureHost]
			b.Mode = DiscoveryDisabled
			s.discovery[publicFixtureHost] = b
		},
		"forwarded":              func(_ *Server, r *http.Request) { r.Header["Forwarded"] = []string{""} },
		"forwarded host":         func(_ *Server, r *http.Request) { r.Header.Set("X-Forwarded-Host", publicFixtureHost) },
		"absolute port conflict": func(_ *Server, r *http.Request) { r.URL.Host = publicFixtureHost + ":8443" },
		"absolute userinfo":      func(_ *Server, r *http.Request) { r.URL.User = url.User("private-sentinel") },
	} {
		t.Run(name, func(t *testing.T) {
			s, reads := ardMCPUnit(t, DiscoveryAnonymous)
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, publicFixtureOrigin+ardMCPPath, strings.NewReader("{}"))
			change(s, r)
			w := httptest.NewRecorder()
			s.handleARDMCP(w, r)
			if w.Code != 404 || *reads != 0 || strings.Contains(w.Body.String(), "private-sentinel") {
				t.Fatal(w.Code, w.Body.String(), *reads)
			}
		})
	}
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		for _, raw := range []string{`{"query":{"text":"fixture"},"baseURL":"https://never-invoke.invalid"}`, `{"query":null}`, `{"Query":{"text":"fixture"}}`, `{"query":{"text":"fixture","text":"other"}}`, `{"query":{"text":"fixture"},"pageSize":-1}`, `{"query":{"text":"fixture"},"federation":"all"}`} {
			s, reads := ardMCPUnit(t, DiscoveryAnonymous)
			w := ardMCPRecorder(t, s, version, "tools/call", "search", "", json.RawMessage(raw))
			if (w.Code != 200 || !strings.Contains(w.Body.String(), `"isError":true`)) && w.Code != 400 || *reads != 0 {
				t.Fatal("invalid request reached publication", raw, w.Code, w.Body.String(), *reads)
			}
		}
	}
}

func TestARDMCPProtocolMethodsAndEndpointMetadata(t *testing.T) {
	s, reads := ardMCPUnit(t, DiscoveryAuthenticated)
	h := s.ardMCP[publicFixtureHost]
	h.issuer = "https://issuer.example.com"
	for _, tc := range []struct {
		method, path, origin, preflight string
		status                          int
	}{
		{http.MethodGet, ardMCPMetadataPath, "", "", http.StatusOK},
		{http.MethodHead, ardMCPMetadataPath, "", "", http.StatusOK},
		{http.MethodOptions, ardMCPMetadataPath, publicFixtureOrigin, http.MethodGet, http.StatusNoContent},
		{http.MethodOptions, ardMCPPath, publicFixtureOrigin, http.MethodPost, http.StatusNoContent},
		{http.MethodOptions, ardMCPPath, publicFixtureOrigin, http.MethodDelete, http.StatusForbidden},
		{http.MethodGet, ardMCPPath, "", "", http.StatusMethodNotAllowed},
		{http.MethodDelete, ardMCPPath, "", "", http.StatusMethodNotAllowed},
		{http.MethodGet, ardMCPMetadataPath, "https://unapproved.example", "", http.StatusForbidden},
	} {
		r := httptest.NewRequestWithContext(t.Context(), tc.method, publicFixtureOrigin+tc.path, nil)
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if tc.preflight != "" {
			r.Header.Set("Access-Control-Request-Method", tc.preflight)
		}
		w := httptest.NewRecorder()
		s.handleARDMCP(w, r)
		if w.Code != tc.status || *reads != 0 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(tc, w.Code, w.Body.String(), *reads)
		}
		if tc.method == http.MethodHead && w.Body.Len() != 0 {
			t.Fatal("HEAD metadata returned a body")
		}
	}
	h.issuer = ""
	w := httptest.NewRecorder()
	s.handleARDMCP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, publicFixtureOrigin+ardMCPMetadataPath, nil))
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "issuer.example.com") || *reads != 0 {
		t.Fatal("development metadata advertised OAuth", w.Code, w.Body.String())
	}
}

func TestARDMCPDistinguishesParseAndClosedEnvelopeErrors(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		valid := string(mcpWire(t, version, "tools/list", "", nil))
		for _, tc := range []struct {
			raw  string
			code int
		}{
			{strings.TrimSuffix(valid, "}"), -32700},
			{valid + " {}", -32700},
			{strings.TrimSuffix(valid, "}") + `,"unexpected":true}`, -32600},
		} {
			s, reads := ardMCPUnit(t, DiscoveryAnonymous)
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, publicFixtureOrigin+ardMCPPath, strings.NewReader(tc.raw))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json, text/event-stream")
			r.Header.Set("Mcp-Protocol-Version", version)
			if version == "2026-07-28" {
				r.Header.Set("Mcp-Method", "tools/list")
			}
			w := httptest.NewRecorder()
			s.handleARDMCP(w, r)
			var reply struct {
				Error struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(w.Body.Bytes(), &reply) != nil || w.Code != http.StatusBadRequest || reply.Error.Code != tc.code || *reads != 0 {
				t.Fatal("wrong safe protocol error", version, w.Code, w.Body.String(), *reads)
			}
		}
	}
}

func TestARDMCPBearerWhitespaceCannotBypassCredentialReflection(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		for _, authorization := range []string{"Bearer reader", "Bearer  reader"} {
			s, reads := ardMCPUnit(t, DiscoveryAuthenticated)
			wire := mcpWire(t, version, "tools/list", "", nil)
			for _, reflectToken := range []bool{false, true} {
				raw := wire
				if reflectToken {
					raw = bytes.Replace(wire, []byte(`"id":1`), []byte(`"id":"reader"`), 1)
				}
				r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, publicFixtureOrigin+ardMCPPath, bytes.NewReader(raw))
				r.Header.Set("Authorization", authorization)
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Accept", "application/json, text/event-stream")
				r.Header.Set("Mcp-Protocol-Version", version)
				if version == "2026-07-28" {
					r.Header.Set("Mcp-Method", "tools/list")
				}
				w := httptest.NewRecorder()
				s.handleARDMCP(w, r)
				if reflectToken {
					if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "reader") {
						t.Fatal("credential was reflected", version, w.Code)
					}
				} else if w.Code != http.StatusOK {
					t.Fatal("valid whitespace bearer was not admitted", version, w.Code)
				}
				if *reads != 0 {
					t.Fatal("protocol inventory read a publication")
				}
			}
		}
	}
}

func TestARDMCPAuthenticationOutageIsFailedNotRejected(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		for _, outage := range []bool{false, true} {
			s, reads := ardMCPUnit(t, DiscoveryAuthenticated)
			s.authenticator = discoveryAdmissionAuthenticator(func(context.Context, string) (identity.Actor, error) {
				if outage {
					return identity.Actor{}, identity.Wrap(identity.CodeDependency, "private resolver detail", errors.New("private database sentinel"))
				}
				return identity.Actor{}, errors.New("private invalid-token sentinel")
			})
			w := ardMCPRecorder(t, s, version, "tools/list", "", "synthetic-auth-token", nil)
			wantStatus, wantOutcome := http.StatusUnauthorized, "rejected"
			if outage {
				wantStatus, wantOutcome = http.StatusServiceUnavailable, "failed"
			}
			if w.Code != wantStatus || *reads != 0 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "synthetic-auth-token") {
				t.Fatal("unsafe authentication failure", version, w.Code, w.Body.String(), *reads)
			}
			if outage && w.Header().Get("WWW-Authenticate") != "" {
				t.Fatal("dependency outage invalidated a token")
			}
			sample, err := s.ardMCP[publicFixtureHost].metrics.Activity(true)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range sample.MCP.Outcomes {
				var want uint64
				if row.Operation == "protocol" && row.Outcome == wantOutcome {
					want = 1
				}
				if row.Completed != want {
					t.Fatalf("%s outage=%t: %s/%s=%d, want %d", version, outage, row.Operation, row.Outcome, row.Completed, want)
				}
			}
		}
	}
}
