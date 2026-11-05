package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gorilla/securecookie"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
)

func hostLogFixture(s *Server, level, format string) *bytes.Buffer {
	logs := &bytes.Buffer{}
	s.logger = (diagnostics.Process{Service: "catalog-ui", Role: "ui", Environment: "development", Version: "fixture"}).Logger(diagnostics.NewLogger(logs, level, format))
	return logs
}

func hostLogRecords(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		for key := range record {
			switch key {
			case "time", "level", "msg", "event", "component", "service", "role", "environment", "version", "request_id", "upstream_request_id", "route", "status", "error_code", "suppressed":
			default:
				t.Fatalf("unreviewed host log field %s", key)
			}
		}
		if record["service"] != "catalog-ui" || record["role"] != "ui" || record["environment"] != "development" || record["version"] != "fixture" {
			t.Fatalf("missing common identity: %v", record)
		}
		records = append(records, record)
	}
	return records
}

func TestHostDiagnosticSeverityFilteringAndPrivacy(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, level := range []string{"debug", "info", "warn", "error"} {
			for _, tc := range []struct {
				name, path, body, event, severity, component string
				status                                       int
			}{
				{"unknown user", "/auth/login", `{"alias":"private-sentinel"}`, "ui.request_failed", "DEBUG", "ui", 401},
				{"API denied", "/auth/login", `{"alias":"reader"}`, "ui.request_failed", "DEBUG", "ui", 401},
				{"API transport", "/auth/login", `{"alias":"reader"}`, "dependency.unavailable", "WARN", "ui_api", 503},
				{"API status", "/auth/login", `{"alias":"reader"}`, "dependency.unavailable", "WARN", "ui_api", 503},
				{"invalid self", "/auth/login", `{"alias":"reader"}`, "dependency.unavailable", "WARN", "ui_api", 503},
				{"session size", "/auth/login", `{"alias":"reader"}`, "ui.request_failed", "WARN", "ui", 422},
				{"established", "/auth/login", `{"alias":"reader"}`, "ui.session_established", "DEBUG", "ui", 200},
				{"invalid state", "/auth/callback?state=private-sentinel&code=private-code", "", "ui.request_failed", "DEBUG", "ui", 400},
				{"discovery outage", "/auth/login", `{}`, "dependency.unavailable", "WARN", "ui_issuer", 503},
				{"state encoding", "/auth/login", `{}`, "ui.request_failed", "ERROR", "ui", 500},
			} {
				t.Run(format+"/"+level+"/"+tc.name, func(t *testing.T) {
					c := fixtureConfig(t, "https://api.example.test")
					if tc.name == "invalid state" || tc.name == "discovery outage" || tc.name == "state encoding" {
						c = oidcFixtureConfig(t, c.Upstream, "https://issuer.example.test")
					}
					s := directHost(t, c)
					logs := hostLogFixture(s, level, format)
					if tc.name == "session size" {
						s.users[0].Token = strings.Repeat("t", 4000)
					}
					s.apiClient.Transport = hostRoundTripFunc(func(*http.Request) (*http.Response, error) {
						if tc.name == "API transport" {
							return nil, errors.New("private-sentinel")
						}
						status, body := http.StatusOK, testAccess
						if tc.name == "API denied" {
							status = http.StatusForbidden
						}
						if tc.name == "API status" {
							status = http.StatusServiceUnavailable
						}
						if tc.name == "invalid self" {
							body = `{"secret":"private-sentinel"}`
						}
						return &http.Response{StatusCode: status, Header: http.Header{"X-Request-Id": {"upstream-safe"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
					})
					if s.login != nil {
						s.login.client.Transport = hostRoundTripFunc(func(*http.Request) (*http.Response, error) {
							if tc.name != "state encoding" {
								return nil, errors.New("private-sentinel")
							}
							return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"issuer":"https://issuer.example.test","authorization_endpoint":"https://issuer.example.test/authorize","token_endpoint":"https://issuer.example.test/token","jwks_uri":"https://issuer.example.test/keys"}`))}, nil
						})
						if tc.name == "state encoding" {
							s.codecs = []securecookie.Codec{securecookie.New(nil, nil)}
						}
					}
					method := http.MethodPost
					if strings.Contains(tc.path, "callback") {
						method = http.MethodGet
					}
					r := httptest.NewRequestWithContext(t.Context(), method, c.Origin+tc.path, strings.NewReader(tc.body))
					r.Header.Set("Origin", c.Origin)
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("X-Catalog-Request", "1")
					r.Header.Set("X-Request-ID", "current-safe")
					r.Header.Set("Authorization", "Bearer private-sentinel")
					w := httptest.NewRecorder()
					s.ServeHTTP(w, r)
					if w.Code != tc.status || len(s.admission) != 0 {
						t.Fatalf("status/admission: %d/%d", w.Code, len(s.admission))
					}
					want := tc.severity == "ERROR" || tc.severity == "WARN" && level != "error" || tc.severity == "DEBUG" && level == "debug"
					if (logs.Len() != 0) != want || strings.Contains(logs.String(), "private-") || strings.Contains(logs.String(), "test-token") || strings.Contains(logs.String(), "https://") {
						t.Fatalf("filter/privacy: %s", logs.String())
					}
					if !want {
						return
					}
					if format == "json" {
						records := hostLogRecords(t, logs)
						if len(records) != 1 || records[0]["event"] != tc.event || records[0]["level"] != tc.severity || records[0]["component"] != tc.component || records[0]["request_id"] != "current-safe" || records[0]["status"] != float64(tc.status) {
							t.Fatalf("owner/classification: %v", records)
						}
					} else if !strings.Contains(logs.String(), "event="+tc.event) || !strings.Contains(logs.String(), "level="+tc.severity) || !strings.Contains(logs.String(), "request_id=current-safe") {
						t.Fatalf("text fields: %s", logs.String())
					}
				})
			}
		}
	}
}

func TestHostDependencyWarningsRecoveryAndCancellation(t *testing.T) {
	s := directHost(t, fixtureConfig(t, "https://api.example.test"))
	logs := hostLogFixture(s, "debug", "json")
	cookie := directHostSession(t, s)
	s.apiClient.Transport = hostRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("private-outage") })
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() { uiRequest(s, "GET", "/_catalog/api/v1/resources", "", cookie) })
	}
	group.Wait()
	records := hostLogRecords(t, logs)
	if len(records) != 1 || records[0]["event"] != "dependency.unavailable" || records[0]["suppressed"] != float64(0) {
		t.Fatalf("first/concurrent warnings: %v", records)
	}
	now := s.now().Add(time.Minute)
	s.now = func() time.Time { return now }
	uiRequest(s, "GET", "/_catalog/api/v1/resources", "", cookie)
	records = hostLogRecords(t, logs)
	if len(records) != 2 || records[1]["suppressed"] != float64(19) {
		t.Fatalf("suppression summary: %v", records)
	}
	s.apiClient.Transport = hostRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`[]`))}, nil
	})
	for range 2 {
		uiRequest(s, "GET", "/_catalog/api/v1/resources", "", cookie)
	}
	records = hostLogRecords(t, logs)
	if len(records) != 3 || records[2]["event"] != "dependency.recovered" || records[2]["level"] != "INFO" {
		t.Fatalf("observed recovery: %v", records)
	}
	logs.Reset()
	s.apiClient.Transport = hostRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, context.Canceled })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := httptest.NewRequestWithContext(ctx, "GET", s.config.Origin+"/auth/session", nil)
	r.Header.Set("X-Catalog-Request", "1")
	r.AddCookie(cookie)
	s.ServeHTTP(httptest.NewRecorder(), r)
	records = hostLogRecords(t, logs)
	if len(records) != 1 || records[0]["level"] != "DEBUG" || s.dependencies[0].failed.Load() {
		t.Fatalf("cancellation claimed outage: %v", records)
	}
}

func TestHostRequestCorrelationIsBoundedAndNeverForwarded(t *testing.T) {
	for _, values := range [][]string{nil, {"valid_ui"}, {"first", "second"}, {"private/unsafe"}, {"private\nunsafe"}, {strings.Repeat("a", 129)}} {
		for _, upstream := range [][]string{nil, {"valid_api"}, {"first", "second"}, {"private/unsafe"}, {"private\nunsafe"}, {strings.Repeat("b", 129)}} {
			s := directHost(t, fixtureConfig(t, "https://api.example.test"))
			logs := hostLogFixture(s, "debug", "json")
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				for _, key := range []string{"X-Request-ID", "Traceparent", "Tracestate", "Baggage"} {
					if r.Header.Get(key) != "" {
						t.Fatal("browser correlation forwarded")
					}
				}
				return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"X-Request-Id": upstream}, Body: io.NopCloser(strings.NewReader(`{"code":"FORBIDDEN"}`))}, nil
			})
			r := httptest.NewRequestWithContext(t.Context(), "GET", s.config.Origin+"/_catalog/api/v1/resources/private-resource?query=private-query", nil)
			r.Header["X-Request-Id"] = values
			r.Header.Set("X-Catalog-Request", "1")
			for _, key := range []string{"Traceparent", "Tracestate", "Baggage"} {
				r.Header.Set(key, "private-sentinel")
			}
			r.AddCookie(directHostSession(t, s))
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			records := hostLogRecords(t, logs)
			if w.Code != 403 || w.Body.String() != `{"code":"FORBIDDEN"}` || len(records) != 1 || records[0]["route"] != "catalog" || strings.Contains(logs.String(), "private-") {
				t.Fatalf("unsafe/changed forwarding: %v", records)
			}
			local, _ := records[0]["request_id"].(string)
			if !safeRequestID.MatchString(local) || len(values) == 1 && values[0] == "valid_ui" && local != "valid_ui" {
				t.Fatalf("local correlation: %q", local)
			}
			want := local
			if len(upstream) == 1 && upstream[0] == "valid_api" {
				want = "valid_api"
				if records[0]["upstream_request_id"] != want {
					t.Fatal("lost backend correlation")
				}
			} else if records[0]["upstream_request_id"] != nil {
				t.Fatal("unsafe backend correlation")
			}
			if w.Header().Get("X-Request-ID") != want || len(w.Header().Values("X-Request-ID")) != 1 {
				t.Fatal("unsafe/ambiguous response correlation")
			}
		}
	}
}

func TestHostIssuerRecoveryAndQuietHealth(t *testing.T) {
	c := oidcFixtureConfig(t, "https://api.example.test", "https://issuer.example.test")
	s := directHost(t, c)
	logs := hostLogFixture(s, "info", "json")
	s.login.client.Transport = hostRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("private-outage") })
	if w := uiRequest(s, "POST", "/auth/login", `{}`); w.Code != 503 {
		t.Fatal("missing issuer failure")
	}
	s.login.client.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Request-ID") != "" || r.Header.Get("Traceparent") != "" {
			t.Fatal("UI context forwarded to issuer")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"issuer":"https://issuer.example.test","authorization_endpoint":"https://issuer.example.test/authorize","token_endpoint":"https://issuer.example.test/token","jwks_uri":"https://issuer.example.test/keys"}`))}, nil
	})
	for range 2 {
		w := uiRequest(s, "POST", "/auth/login", `{}`)
		if w.Code != 200 || len(w.Result().Cookies()) != 1 || strings.Contains(logs.String(), w.Result().Cookies()[0].Value) {
			t.Fatal("recovery failed or disclosed login state")
		}
	}
	records := hostLogRecords(t, logs)
	if len(records) != 2 || records[0]["event"] != "dependency.unavailable" || records[1]["event"] != "dependency.recovered" || records[1]["component"] != "ui_issuer" {
		t.Fatalf("issuer recovery: %v", records)
	}
	logs.Reset()
	for _, path := range []string{"/livez", "/readyz", "/runtime.json", "/catalog"} {
		if w := uiRequest(s, "GET", path, ""); w.Code != 200 {
			t.Fatal("healthy control failed")
		}
	}
	if logs.Len() != 0 {
		t.Fatal("healthy request logging enabled")
	}
	s.Drain()
	s.Drain()
	if uiRequest(s, "GET", "/livez", "").Code != 200 || uiRequest(s, "GET", "/readyz", "").Code != 503 {
		t.Fatal("logging changed drain contract")
	}
	records = hostLogRecords(t, logs)
	if len(records) != 1 || records[0]["event"] != "process.draining" {
		t.Fatalf("drain duplicated or health logged: %v", records)
	}
}

func TestIssuerObservationPreservesTransportAndRequiresExactGETSuccess(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, tc := range []struct {
			status                  int
			getFailure, postFailure bool
		}{
			{200, false, false}, {202, true, false}, {204, true, false}, {299, true, false},
			{300, true, true}, {399, true, true}, {400, true, false}, {401, true, false},
			{403, true, false}, {429, true, true}, {499, true, false}, {500, true, true}, {503, true, true},
		} {
			t.Run(fmt.Sprintf("%s/%d", method, tc.status), func(t *testing.T) {
				var failed atomic.Bool
				body := &transportResponseBody{reader: strings.NewReader("private-issuer-response")}
				response := &http.Response{StatusCode: tc.status, Body: body}
				request := httptest.NewRequestWithContext(t.Context(), method, unitOIDCIssuer+"/endpoint", nil)
				calls := 0
				transport := issuerTransport{hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r != request {
						t.Fatal("observation replaced the issuer request/context")
					}
					return response, nil
				}), &failed}
				got, err := transport.RoundTrip(request)
				if got != nil {
					defer func() { _ = got.Body.Close() }()
				}
				// Discovery/key GETs require 200. Token exchange accepts 2xx;
				// OAuth 4xx refusal classification belongs to the callback.
				want := tc.getFailure
				if method == http.MethodPost {
					want = tc.postFailure
				}
				if got != response || err != nil || calls != 1 || failed.Load() != want || body.bytesRead != 0 || body.closes != 0 {
					t.Fatal("issuer observation changed delegation, body ownership or classification")
				}
				// A later successful exchange in the same request must not erase
				// an earlier issuer failure (including SDK auth-style fallback).
				failed.Store(true)
				response = &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}
				next, err := transport.RoundTrip(request)
				if next != nil {
					defer func() { _ = next.Body.Close() }()
				}
				if err != nil || next != response || !failed.Load() || calls != 2 {
					t.Fatal("successful transport erased an observed failure")
				}
			})
		}
	}
	var failed atomic.Bool
	cause := errors.New("private-issuer-error")
	transport := issuerTransport{hostRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, cause }), &failed}
	response, err := transport.RoundTrip(httptest.NewRequestWithContext(t.Context(), http.MethodGet, unitOIDCIssuer, nil))
	if response != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if response != nil || !errors.Is(err, cause) || !failed.Load() {
		t.Fatal("transport failure lost its cause or was not observed")
	}
}

func TestHostDependencyOutcomesRemainIndependent(t *testing.T) {
	s := directHost(t, fixtureConfig(t, "https://api.example.test"))
	logs := hostLogFixture(s, "debug", "json")
	r := httptest.NewRequestWithContext(t.Context(), "GET", s.config.Origin+"/auth/callback?code=private-code", nil)
	failed := &hostResponse{status: 401, apiOutcome: 2, issuerOK: true, code: "login_exchange_failed"}
	failed.issuerFailed.Store(true)
	s.logResponse(r, failed)
	records := hostLogRecords(t, logs)
	if len(records) != 2 || records[0]["event"] != "dependency.unavailable" || records[1]["event"] != "dependency.unavailable" || records[0]["component"] != "ui_api" || records[1]["component"] != "ui_issuer" {
		t.Fatalf("dependency failure ownership: %v", records)
	}
	logs.Reset()
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	s.logResponse(r.WithContext(ctx), &hostResponse{status: 200, apiOutcome: 1, issuerOK: true})
	if !s.dependencies[0].failed.Load() || !s.dependencies[1].failed.Load() {
		t.Fatal("canceled request claimed dependency recovery")
	}
	records = hostLogRecords(t, logs)
	if len(records) != 1 || records[0]["error_code"] != "request_canceled" || records[0]["level"] != "DEBUG" {
		t.Fatalf("cancellation: %v", records)
	}
	logs.Reset()
	// An API refusal proves that dependency responded, without repairing the
	// issuer state or claiming a successful user login.
	s.logResponse(r, &hostResponse{status: 403, apiOutcome: 1})
	records = hostLogRecords(t, logs)
	if len(records) != 2 || records[0]["event"] != "dependency.recovered" || records[0]["component"] != "ui_api" || records[1]["error_code"] != "catalog_rejected" || s.dependencies[0].failed.Load() || !s.dependencies[1].failed.Load() {
		t.Fatalf("independent recovery: %v", records)
	}
}

func TestHostResponsePreservesControllerAndWriteOwnership(t *testing.T) {
	recorder := httptest.NewRecorder()
	observed := &hostResponse{ResponseWriter: recorder}
	observed.WriteHeader(http.StatusAccepted)
	observed.WriteHeader(http.StatusInternalServerError)
	if n, err := observed.Write([]byte("accepted")); n != 8 || err != nil {
		t.Fatal("response write changed")
	}
	if err := http.NewResponseController(observed).Flush(); err != nil || !recorder.Flushed || recorder.Code != 202 || observed.status != 202 || recorder.Body.String() != "accepted" {
		t.Fatal("diagnostic wrapper broke response control or first-status ownership")
	}
}

func TestHostOIDCExchangeDiagnosticOwnership(t *testing.T) {
	// Exercise the callback's classification with local response bytes. Signature
	// and complete login qualification remain in the real OIDC functional fixture.
	for _, tc := range []struct {
		name, body, code, level string
		status                  int
	}{
		{"discovery failure", `{"private":"response"}`, "identity_provider_unavailable", "WARN", 503},
		{"invalid grant", `{"error":"invalid_grant","error_description":"private-refusal"}`, "login_exchange_failed", "DEBUG", 400},
		{"access denied", `{"error":"access_denied","error_description":"private-refusal"}`, "login_exchange_failed", "DEBUG", 403},
		{"invalid client", `{"error":"invalid_client","error_description":"private-credential"}`, "login_exchange_failed", "WARN", 401},
		{"server failure despite grant code", `{"error":"invalid_grant"}`, "login_exchange_failed", "WARN", 503},
		{"rate limited despite grant code", `{"error":"invalid_grant"}`, "login_exchange_failed", "WARN", 429},
		{"invalid token JSON", `{"private":`, "login_exchange_failed", "WARN", 200},
		{"missing token", `{"private":"response"}`, "login_exchange_failed", "WARN", 200},
		{"wrong token type", `{"access_token":"private-access","token_type":"MAC","expires_in":3600}`, "login_exchange_failed", "WARN", 200},
		{"expired token", `{"access_token":"private-access","token_type":"Bearer","expires_in":-1}`, "login_exchange_failed", "WARN", 200},
		{"valid exchange missing identity", `{"access_token":"private-access","token_type":"Bearer","expires_in":3600}`, "invalid_login_identity", "DEBUG", 200},
		{"valid exchange malformed identity", `{"access_token":"private-access","token_type":"Bearer","expires_in":3600,"id_token":"private-invalid-jwt"}`, "invalid_login_identity", "DEBUG", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := directHost(t, oidcFixtureConfig(t, "https://api.example.test", unitOIDCIssuer))
				logs := hostLogFixture(s, "debug", "json")
				state := loginState{Binding: s.binding, State: strings.Repeat("s", 43), Nonce: "private-nonce", Verifier: "private-verifier", Expires: s.now().Add(5 * time.Minute).Unix()}
				cookie := pendingLoginCookie(t, s, state, state.State)
				discoveries, exchanges := 0, 0
				var bodies []*transportResponseBody
				s.login.client.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					status, raw := http.StatusOK, unitOIDCDiscovery
					switch {
					case r.Method == http.MethodGet && r.URL.String() == unitOIDCIssuer+"/.well-known/openid-configuration":
						discoveries++
						if tc.name == "discovery failure" {
							status, raw = tc.status, tc.body
						}
					case r.Method == http.MethodPost && r.URL.String() == unitOIDCIssuer+"/token":
						exchanges++
						if err := r.ParseForm(); err != nil || r.PostForm.Get("code") != "private-code" || r.PostForm.Get("code_verifier") != state.Verifier {
							t.Fatal("exchange omitted the bound code or PKCE verifier")
						}
						status, raw = tc.status, tc.body
					default:
						t.Fatalf("unexpected issuer request: %s %s", r.Method, r.URL.Path)
					}
					body := &transportResponseBody{reader: strings.NewReader(raw)}
					bodies = append(bodies, body)
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
				})
				// directHost's API transport rejects any unintended API call. These
				// refused identities must never reach self-context or issue a session.
				w := uiRequest(s, http.MethodGet, "/auth/callback?state="+state.State+"&code=private-code", "", cookie)
				status, minExchanges, maxExchanges := http.StatusUnauthorized, 1, 2
				if tc.name == "discovery failure" {
					status, minExchanges, maxExchanges = http.StatusServiceUnavailable, 0, 0
				}
				assertUIProblem(t, w, status, tc.code)
				if discoveries != 1 || exchanges < minExchanges || exchanges > maxExchanges || len(s.admission) != 0 {
					t.Fatalf("unexpected callback work: discovery=%d exchange=%d", discoveries, exchanges)
				}
				for _, body := range bodies {
					if body.closes != 1 {
						t.Fatal("callback did not close an issuer response")
					}
				}
				cookies := w.Result().Cookies()
				if len(cookies) != 1 || cookies[0].Name != cookie.Name || cookies[0].MaxAge != -1 {
					t.Fatal("failed callback created a session or retained pending state")
				}
				records := hostLogRecords(t, logs)
				event, component, code := "ui.request_failed", "ui", tc.code
				if tc.level == "WARN" {
					event, component, code = "dependency.unavailable", "ui_issuer", "dependency_unavailable"
				}
				if len(records) != 1 || records[0]["event"] != event || records[0]["component"] != component || records[0]["error_code"] != code || records[0]["level"] != tc.level || records[0]["status"] != float64(status) || strings.Contains(logs.String(), "private-") || strings.Contains(logs.String(), cookie.Value) {
					t.Fatalf("callback diagnostic classification/privacy: %v", records)
				}
			})
		})
	}
}
