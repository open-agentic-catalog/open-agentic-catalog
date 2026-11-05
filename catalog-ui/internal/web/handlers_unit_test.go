// Direct host tests use static mounted inputs and recording transports, never listeners.
package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type hostRoundTripFunc func(*http.Request) (*http.Response, error)

func (f hostRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// directHost retains real routing and cookies while refusing unexpected outbound work.
func directHost(t *testing.T, c Config) *Server {
	t.Helper()
	s := newFixture(t, c)
	at := time.Now().UTC().Truncate(time.Second)
	s.now = func() time.Time { return at }
	deny := hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected outbound request: %s %s", r.Method, r.URL.Path)
		return nil, errors.New("unexpected outbound request")
	})
	s.apiClient.Transport = deny
	if s.login != nil {
		s.login.client.Transport = deny
	}
	return s
}

// directHostSession supplies an authentic session without replaying a login workflow.
func directHostSession(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	if err := s.setSession(w, "test-token", s.now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("session fixture needs one cookie")
	}
	return cookies[0]
}

func TestDirectHostRoutingAndRuntimeConfiguration(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"development", "oidc"} {
		for _, base := range []string{"", "/nested"} {
			t.Run(mode+base, func(t *testing.T) {
				c := fixtureConfig(t, "https://api.example.test")
				if mode == "oidc" {
					c = oidcFixtureConfig(t, c.Upstream, "https://issuer.example.test")
				}
				c.BasePath = base
				if base != "" {
					c.DocumentationURL = "https://manual.example.test"
				}
				s := directHost(t, c)
				w := uiRequest(s, http.MethodGet, "/runtime.json", "")
				var got map[string]string
				want := map[string]string{"basePath": base, "authMode": mode, "documentationUrl": c.DocumentationURL}
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" || len(w.Result().Cookies()) != 0 {
					t.Fatalf("runtime configuration=%d %s", w.Code, w.Body.String())
				}
			})
		}
	}
	c := fixtureConfig(t, "https://api.example.test")
	c.BasePath = "/nested"
	for _, test := range []struct {
		method, path, code string
		status             int
	}{
		{http.MethodPost, "/livez", "method_not_allowed", 405},
		{http.MethodPost, "/readyz", "method_not_allowed", 405},
		{http.MethodGet, "/runtime.json", "not_found", 404},
		{http.MethodGet, "/nested-other/runtime.json", "not_found", 404},
		{http.MethodGet, "/nested/auth/login", "not_found", 404},
		{http.MethodPost, "/nested/auth/options", "not_found", 404},
		{http.MethodGet, "/nested/auth/logout", "not_found", 404},
		{http.MethodPost, "/nested/auth/session", "not_found", 404},
		{http.MethodPost, "/nested/_reference", "not_found", 404},
		{http.MethodPost, "/nested/catalog", "method_not_allowed", 405},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			s := directHost(t, c)
			var cookie *http.Cookie
			calls := 0
			if test.path == "/nested/auth/session" || test.path == "/nested/_reference" {
				cookie = directHostSession(t, s)
				body, upstreamPath := testAccess, "/api/v1/me"
				if test.path == "/nested/_reference" {
					body, upstreamPath = referenceUnitYAML, "/openapi/control-v1.yaml"
				}
				s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Method != http.MethodGet || r.URL.String() != c.Upstream+upstreamPath || r.Header.Get("Authorization") != "Bearer test-token" {
						t.Fatal("read control lost its fixed destination or session")
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				control := uiRequest(s, http.MethodGet, strings.TrimPrefix(test.path, c.BasePath), "", cookie)
				if test.path == "/nested/auth/session" {
					assertSessionProjection(t, control, testAccess, s.now().Add(time.Hour).Unix())
				} else {
					var got struct {
						YAML string `json:"yaml"`
					}
					if control.Code != http.StatusOK || json.Unmarshal(control.Body.Bytes(), &got) != nil || got.YAML != referenceUnitYAML {
						t.Fatalf("reference read control=%d %s", control.Code, control.Body.String())
					}
				}
				if calls != 1 {
					t.Fatal("read control did not contact the API exactly once")
				}
			}
			r := httptest.NewRequestWithContext(t.Context(), test.method, c.Origin+test.path, nil)
			body := &transportResponseBody{reader: strings.NewReader(`{"alias":"reader"}`)}
			r.Body = body
			r.Header.Set("Origin", c.Origin)
			r.Header.Set("X-Catalog-Request", "1")
			if cookie != nil {
				r.AddCookie(cookie)
			}
			before := calls
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			assertUIProblem(t, w, test.status, test.code)
			if calls != before || body.bytesRead != 0 || len(s.admission) != 0 || len(w.Result().Cookies()) != 0 || w.Header().Get("Location") != "" {
				t.Fatal("rejected route consumed input, contacted API, leaked admission or changed browser state")
			}
		})
	}
}

func TestDirectHostCallbackAdmission(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"busy", "draining", "admitted"} {
		t.Run(mode, func(t *testing.T) {
			c := oidcFixtureConfig(t, "https://api.example.test", "https://issuer.example.test")
			c.BasePath = "/nested"
			s := directHost(t, c)
			state := loginState{Binding: s.binding, State: strings.Repeat("a", 43), Nonce: strings.Repeat("b", 43), Verifier: strings.Repeat("c", 43), Expires: s.now().Add(5 * time.Minute).Unix()}
			cookie := pendingLoginCookie(t, s, state, state.State)
			status, code, slots := 401, "login_not_completed", 0
			switch mode {
			case "busy":
				for range cap(s.admission) {
					s.admission <- struct{}{}
				}
				status, code, slots = 503, "ui_busy", cap(s.admission)
			case "draining":
				s.Drain()
				status, code = 503, "ui_draining"
			}
			// Provider redirects do not carry the browser command header or Origin.
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, c.Origin+c.BasePath+"/auth/callback?state="+state.State+"&error=access_denied", nil)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			assertUIProblem(t, w, status, code)
			if len(s.admission) != slots {
				t.Fatal("callback changed another request's admission slot")
			}
			cookies := w.Result().Cookies()
			if mode == "admitted" {
				if len(cookies) != 1 || cookies[0].Name != cookie.Name || cookies[0].MaxAge != -1 || cookies[0].Path != "/nested/" {
					t.Fatal("admitted callback did not consume its pending state")
				}
			} else if len(cookies) != 0 {
				t.Fatal("admission rejection consumed callback state")
			}
		})
	}
}

func TestDirectHostProxyRejectsDuplicateHeaders(t *testing.T) {
	t.Parallel()
	s := directHost(t, fixtureConfig(t, "https://api.example.test"))
	cookie := directHostSession(t, s)
	for _, test := range []struct{ name, value string }{
		{"Accept", "application/json"},
		{"Content-Type", "application/json"},
		{"If-Match", `"rev-1"`},
		{"If-None-Match", `"rev-1"`},
		{"Idempotency-Key", "command-one"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if values := r.Header.Values(test.name); !reflect.DeepEqual(values, []string{test.value}) {
					t.Fatalf("single command header changed: %v", values)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, s.config.Origin+"/_catalog/api/v1/catalog/search", strings.NewReader(`{}`))
			r.Header.Set("Origin", s.config.Origin)
			r.Header.Set("X-Catalog-Request", "1")
			r.Header.Add(test.name, test.value)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r.Clone(t.Context()))
			if w.Code != 200 || calls != 1 || len(s.admission) != 0 {
				t.Fatalf("single command header rejected: %d calls=%d", w.Code, calls)
			}
			r.Body = io.NopCloser(strings.NewReader(`{}`))
			r.Header.Add(test.name, test.value)
			w = httptest.NewRecorder()
			s.ServeHTTP(w, r)
			assertUIProblem(t, w, 400, "ambiguous_header")
			if calls != 1 || len(s.admission) != 0 || len(w.Result().Cookies()) != 0 {
				t.Fatal("header rejection contacted upstream, leaked admission or wrote cookies")
			}
		})
	}
}

func TestDirectHostProxyAttachments(t *testing.T) {
	t.Parallel()
	for _, media := range []string{"text/html; charset=utf-8", "image/svg+xml", "application/json"} {
		t.Run(media, func(t *testing.T) {
			c := fixtureConfig(t, "https://api.example.test")
			c.BasePath = "/nested"
			s := directHost(t, c)
			cookie := directHostSession(t, s)
			const path = "/api/v1/artifacts/00000000-0000-7000-8000-000000000001/content"
			const body = "retained-document-bytes"
			calls := 0
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.String() != c.Upstream+path || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Cookie") != "" {
					t.Fatal("artifact request lost fixed destination or session binding")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {media}, "Content-Disposition": {`inline; filename="upstream"`}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			w := uiRequest(s, http.MethodGet, "/_catalog"+path, "", cookie)
			wantDisposition := `attachment; filename="catalog-document"`
			if media == "application/json" {
				wantDisposition = `inline; filename="upstream"`
			}
			if w.Code != 200 || w.Body.String() != body || w.Header().Get("Content-Type") != media || w.Header().Get("Content-Disposition") != wantDisposition || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" || calls != 1 || len(s.admission) != 0 {
				t.Fatalf("unsafe document response: %d %v calls=%d", w.Code, w.Header(), calls)
			}
		})
	}
}

func TestDirectHostReferenceTransportFailure(t *testing.T) {
	t.Parallel()
	s := directHost(t, fixtureConfig(t, "https://api.example.test"))
	cookie := directHostSession(t, s)
	calls := 0
	s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.String() != s.config.Upstream+"/openapi/control-v1.yaml" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Cookie") != "" {
			t.Fatal("reference request lost fixed destination or session binding")
		}
		return nil, errors.New("private transport diagnostic")
	})
	w := uiRequest(s, http.MethodGet, "/_reference?url=https://untrusted.invalid/", "", cookie)
	assertUIProblem(t, w, 502, "catalog_unavailable")
	if calls != 1 || len(s.admission) != 0 || len(w.Result().Cookies()) != 0 {
		t.Fatal("failed reference retried, retained admission or wrote cookies")
	}
}

func TestDirectHostDevelopmentLoginRejections(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, alias, code string
		apiStatus, status int
	}{
		{"unknown alias", "unknown", "unknown_development_user", 0, 401},
		{"API denial", "reader", "catalog_login_denied", 403, 401},
		{"API unavailable", "reader", "catalog_unavailable", 503, 503},
		{"transport failure", "reader", "catalog_unavailable", 0, 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := directHost(t, fixtureConfig(t, "https://api.example.test"))
			calls := 0
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.String() != s.config.Upstream+"/api/v1/me" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Fatal("login did not request current access for its configured user")
				}
				if test.apiStatus == 0 {
					return nil, errors.New("private transport failure: test-token")
				}
				return &http.Response{StatusCode: test.apiStatus, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("private API diagnostic: test-token"))}, nil
			})
			w := uiRequest(s, http.MethodPost, "/auth/login", `{"alias":"`+test.alias+`"}`)
			assertUIProblem(t, w, test.status, test.code)
			wantCalls := 1
			if test.alias == "unknown" {
				wantCalls = 0
			}
			if calls != wantCalls || len(w.Result().Cookies()) != 0 || len(s.admission) != 0 {
				t.Fatalf("rejected login calls/cookies/admission = %d/%d/%d", calls, len(w.Result().Cookies()), len(s.admission))
			}
		})
	}
}

func TestDirectHostDevelopmentSessionSize(t *testing.T) {
	t.Parallel()
	for _, large := range []bool{false, true} {
		name, token := "ordinary", "test-token"
		if large {
			name, token = "oversized cookie", strings.Repeat("t", 4000)
		}
		t.Run(name, func(t *testing.T) {
			c := fixtureConfig(t, "https://api.example.test")
			users, err := json.Marshal([]developmentUser{{Alias: "reader", Label: "Reader", Token: token}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(c.DevelopmentUsersFile, users, 0600); err != nil {
				t.Fatal(err)
			}
			s := directHost(t, c)
			calls := 0
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.String() != c.Upstream+"/api/v1/me" || r.Header.Get("Authorization") != "Bearer "+token {
					t.Fatal("login skipped current Catalog access verification")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(testAccess))}, nil
			})
			w := uiRequest(s, http.MethodPost, "/auth/login", `{"alias":"reader"}`)
			var location map[string]string
			if large {
				assertUIProblem(t, w, 422, "session_token_too_large")
				if len(w.Result().Cookies()) != 0 {
					t.Fatal("oversized session issued a partial cookie")
				}
			} else if w.Code != 200 || len(w.Result().Cookies()) != 1 || json.Unmarshal(w.Body.Bytes(), &location) != nil || !reflect.DeepEqual(location, map[string]string{"location": "/catalog"}) {
				t.Fatalf("ordinary session rejected: %d %s", w.Code, w.Body.String())
			}
			if calls != 1 || len(s.admission) != 0 || strings.Contains(w.Body.String(), token) {
				t.Fatal("login repeated access, leaked token or retained admission")
			}
		})
	}
}
