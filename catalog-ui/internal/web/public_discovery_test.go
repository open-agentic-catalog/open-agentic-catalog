// Anonymous host units use recorders and injected transports, never listeners.
package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPublicDiscoveryAdmission keeps disabled reads and private routes separate.
func TestPublicDiscoveryAdmission(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"", "/nested"} {
		for _, enabled := range []bool{false, true} {
			c := fixtureConfig(t, "https://api.example.test")
			c.BasePath = base
			c.PublicDiscoveryEnabled = enabled
			s := directHost(t, c)
			calls := 0
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"entries":[]}`))}, nil
			})
			for _, route := range []string{"/discover", "/_discovery/manifest"} {
				w := uiRequest(s, http.MethodGet, route, "")
				want := 404
				if enabled {
					want = 200
				}
				if w.Code != want {
					t.Fatalf("enabled=%v route=%s status=%d", enabled, route, w.Code)
				}
			}
			wantCalls := 0
			if enabled {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatal("navigation or disabled feature contacted API")
			}
			assertUIProblem(t, uiRequest(s, http.MethodGet, "/_catalog/api/v1/resources", ""), 401, "login_required")
			runtime := uiRequest(s, http.MethodGet, "/runtime.json", "")
			var value map[string]any
			if json.Unmarshal(runtime.Body.Bytes(), &value) != nil {
				t.Fatal("invalid runtime")
			}
			if enabled {
				if value["publicDiscoveryEnabled"] != true {
					t.Fatal("enabled capability omitted")
				}
			} else if _, exists := value["publicDiscoveryEnabled"]; exists {
				t.Fatal("default changed public runtime contract")
			}
		}
	}
}

// TestPublicDiscoveryWire proves credentials and caller authority never reach the API.
func TestPublicDiscoveryWire(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ method, path, body, wantPath, wantQuery, wantBody string }{
		{"GET", "/_discovery/manifest", "", "/.well-known/ard.json", "", ""},
		{"GET", "/_discovery/agents?pageSize=5&pageToken=a%2B%2F%3D%E6%BC%A2", "", "/ard/v1/agents", "pageSize=5&pageToken=a%2B%2F%3D%E6%BC%A2", ""},
		{"POST", "/_discovery/search", `{"text":" hello 漢 ","pageSize":5,"pageToken":"literal+/="}`, "/ard/v1/search", "", `{"query":{"text":" hello 漢 "},"federation":"none","pageSize":5,"pageToken":"literal+/="}`},
	} {
		t.Run(test.path, func(t *testing.T) {
			c := fixtureConfig(t, "https://api.example.test")
			c.PublicDiscoveryEnabled = true
			s := directHost(t, c)
			cookie := directHostSession(t, s)
			calls := 0
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if r.URL.Scheme != "https" || r.URL.Host != "api.example.test" || r.Host != "api.example.test" || r.URL.Path != test.wantPath || r.URL.RawQuery != test.wantQuery || string(raw) != test.wantBody {
					t.Fatalf("wrong fixed request: %s %q", r.URL, string(raw))
				}
				for _, h := range []string{"Authorization", "Cookie", "Forwarded", "X-Forwarded-Host", "X-Original-Host", "If-None-Match", "If-Modified-Since"} {
					if r.Header.Get(h) != "" {
						t.Fatalf("forwarded %s", h)
					}
				}
				if r.Header.Get("Cache-Control") != "no-cache" {
					t.Fatal("missing revalidation")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}, "Set-Cookie": []string{"private=value"}, "Cache-Control": []string{"public, max-age=60"}, "Etag": []string{`"snapshot"`}, "Last-Modified": []string{"Mon, 16 Nov 2026 00:00:00 GMT"}, "Retry-After": []string{"2"}, "X-Request-Id": []string{"public-request-1"}}, Body: io.NopCloser(strings.NewReader(`{"items":[],"pageToken":"next"}`))}, nil
			})
			r := httptest.NewRequestWithContext(t.Context(), test.method, c.Origin+test.path, strings.NewReader(test.body))
			r.Header.Set("Origin", c.Origin)
			r.Header.Set("X-Catalog-Request", "1")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer private-sentinel")
			r.Header.Set("If-None-Match", `"old"`)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 200 || calls != 1 || w.Body.String() != `{"items":[],"pageToken":"next"}` || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != "no-store" || len(s.admission) != 0 {
				t.Fatal("public read changed session, cache or response")
			}
			if w.Header().Get("ETag") != `"snapshot"` || w.Header().Get("Last-Modified") != "Mon, 16 Nov 2026 00:00:00 GMT" || w.Header().Get("Retry-After") != "2" || w.Header().Get("X-Request-ID") != "public-request-1" {
				t.Fatal("public response lost safe cache or diagnostic headers")
			}

		})
	}
}

// TestPublicDiscoveryRefusals requires zero upstream work for malformed inputs.
func TestPublicDiscoveryRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/_discovery/private", "", 404}, {"POST", "/_discovery/agents", `{}`, 405}, {"GET", "/_discovery/search", "", 405}, {"GET", "/_discovery/manifest?host=private", "", 400},
		{"GET", "/_discovery/agents?url=https://other.invalid", "", 400}, {"GET", "/_discovery/agents?pageSize=0", "", 400}, {"GET", "/_discovery/agents?pageSize=101", "", 400}, {"GET", "/_discovery/agents?pageSize=5&pageSize=5", "", 400}, {"GET", "/_discovery/agents?pageToken=%FF", "", 400}, {"GET", "/_discovery/agents?pageToken=%00", "", 400}, {"GET", "/_discovery/manifest", "body", 400},
		{"POST", "/_discovery/search", `{"text":"hello","pageSize":20,"federation":"auto"}`, 400}, {"POST", "/_discovery/search", `{"text":"hello","pageSize":20,"query":{"@context":"https://other.invalid"}}`, 400},
		{"POST", "/_discovery/search", `{"text":"hello","Text":"secret","pageSize":20}`, 400}, {"POST", "/_discovery/search", `{"text":"a","text":"b","pageSize":20}`, 400}, {"POST", "/_discovery/search", `{"text":null,"pageSize":20}`, 400}, {"POST", "/_discovery/search", `{"text":"hello","pageSize":null}`, 400}, {"POST", "/_discovery/search", `{"text":"hello","pageSize":0}`, 400}, {"POST", "/_discovery/search", `{"text":"hello","pageSize":20} {}`, 400}, {"POST", "/_discovery/search", `{"text":"\ud800","pageSize":20}`, 400},
	}
	for _, test := range tests {
		t.Run(test.path+test.body, func(t *testing.T) {
			c := fixtureConfig(t, "https://api.example.test")
			c.PublicDiscoveryEnabled = true
			s := directHost(t, c)
			s.apiClient.Transport = hostRoundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("rejected input contacted API")
				return nil, errors.New("unexpected public request")
			})
			w := uiRequest(s, test.method, test.path, test.body)
			if w.Code != test.status {
				t.Fatalf("status=%d, want %d", w.Code, test.status)
			}
		})
	}
	for _, header := range []string{"Forwarded", "X-Forwarded-Host", "X-Original-Host"} {
		c := fixtureConfig(t, "https://api.example.test")
		c.PublicDiscoveryEnabled = true
		s := directHost(t, c)
		s.apiClient.Transport = hostRoundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("forwarded authority contacted API")
			return nil, errors.New("unexpected public request")
		})
		r := httptest.NewRequestWithContext(t.Context(), "GET", c.Origin+"/_discovery/manifest", nil)
		r.Header.Set("Origin", c.Origin)
		r.Header.Set("X-Catalog-Request", "1")
		r.Header.Set(header, "other.invalid")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("forwarded authority accepted")
		}
	}
}

// TestPublicDiscoveryResponsePolicy preserves ARD failures while rejecting unsafe bodies and redirects.
func TestPublicDiscoveryResponsePolicy(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status      int
		media, body string
		want        int
	}{
		{200, "application/json", `{"items":[]}`, 200}, {401, "application/json", `{"errorCode":"UNAUTHENTICATED","message":"anonymous feed required"}`, 401}, {404, "application/json", `{"errorCode":"NOT_FOUND"}`, 404},
		{302, "application/json", `{}`, 502}, {304, "application/json", "", 502}, {200, "text/html", `{}`, 502}, {200, "application/json", `{"a":1,"a":2}`, 502}, {200, "application/json", `{} {}`, 502}, {200, "application/json", strings.Repeat(" ", 513), 502},
	} {
		c := fixtureConfig(t, "https://api.example.test")
		c.PublicDiscoveryEnabled = true
		c.ResponseBytes = 512
		s := directHost(t, c)
		body := &transportResponseBody{reader: strings.NewReader(test.body)}
		s.apiClient.Transport = hostRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": []string{test.media}, "Location": []string{"https://other.invalid"}}, Body: body}, nil
		})
		w := uiRequest(s, "GET", "/_discovery/agents", "")
		if w.Code != test.want || body.closes != 1 || w.Header().Get("Location") != "" {
			t.Fatalf("status=%d want=%d closes=%d", w.Code, test.want, body.closes)
		}
	}
}

func TestPublicDiscoveryManifestRecoversOnNextRead(t *testing.T) {
	t.Parallel()
	c := fixtureConfig(t, "https://api.example.test")
	c.PublicDiscoveryEnabled = true
	s := directHost(t, c)
	cookie := directHostSession(t, s)
	const manifest = `{"entries":[]}`
	calls := 0
	s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.String() != "https://api.example.test/.well-known/ard.json" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("public manifest read changed its destination or forwarded credentials")
		}
		if calls == 1 {
			return nil, errors.New("private transport diagnostic")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(manifest))}, nil
	})
	read := func() *httptest.ResponseRecorder {
		return uiRequest(s, http.MethodGet, "/_discovery/manifest", "", cookie)
	}
	failed := read()
	assertUIProblem(t, failed, http.StatusBadGateway, "catalog_unavailable")
	if calls != 1 || failed.Header().Get("Cache-Control") != "no-store" || failed.Header().Get("Set-Cookie") != "" || len(s.admission) != 0 {
		t.Fatal("failed manifest read retried, changed the session or cache policy, or retained admission")
	}
	refreshed := read()
	if calls != 2 || refreshed.Code != http.StatusOK || refreshed.Body.String() != manifest {
		t.Fatalf("explicit refresh returned status %d and body %q after %d upstream calls", refreshed.Code, refreshed.Body.String(), calls)
	}
}

// TestPublicDiscoveryConfig remains opt-in and rejects ambiguous non-boolean input.
func TestPublicDiscoveryConfig(t *testing.T) {
	t.Parallel()
	c := fixtureConfig(t, "https://api.example.test")
	file := filepath.Join(t.TempDir(), "public.json")
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"false", "true", "null", `"true"`, "1"} {
		data := strings.Replace(string(raw), `"publicDiscoveryEnabled":false`, `"publicDiscoveryEnabled":`+value, 1)
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadConfig(file)
		valid := value == "true" || value == "false"
		if (err == nil) != valid || valid && got.PublicDiscoveryEnabled != (value == "true") {
			t.Fatalf("configuration %s accepted=%v", value, err == nil)
		}
	}
}

// The anonymous boundary uses the same finite admission and browser-origin rules as private reads.
func TestPublicDiscoveryBrowserAdmission(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, header, value string
		status              int
	}{
		{"missing request marker", "X-Catalog-Request", "", 403},
		{"foreign origin", "Origin", "https://other.example", 403},
		{"foreign fetch metadata", "Sec-Fetch-Site", "cross-site", 403},
		{"draining", "", "", 503},
		{"saturated", "", "", 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := fixtureConfig(t, "https://api.example.test")
			c.PublicDiscoveryEnabled = true
			s := directHost(t, c)
			s.apiClient.Transport = hostRoundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("rejected browser input contacted API")
				return nil, errors.New("unexpected public request")
			})
			r := httptest.NewRequestWithContext(t.Context(), "GET", c.Origin+"/_discovery/manifest", nil)
			r.Header.Set("X-Catalog-Request", "1")
			if test.header != "" {
				r.Header.Set(test.header, test.value)
			}
			if test.name == "draining" {
				s.Drain()
			}
			if test.name == "saturated" {
				for range cap(s.admission) {
					s.admission <- struct{}{}
				}
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status=%d, want %d", w.Code, test.status)
			}
		})
	}
}

// Bounds and media admission fail before outbound calls, with a valid multi-byte control.
func TestPublicDiscoveryInputBounds(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, path, text string
		contentTypes     []string
		bytes            int64
		status           int
	}{
		{name: "Unicode code point boundary", text: strings.Repeat("漢", 500), status: 200},
		{name: "Unicode over limit", text: strings.Repeat("漢", 501), status: 400},
		{name: "UTF8 JSON media", contentTypes: []string{"application/json; charset=UTF-8"}, status: 200},
		{name: "missing media", contentTypes: []string{}, status: 415},
		{name: "foreign media", contentTypes: []string{"text/plain"}, status: 415},
		{name: "unknown parameter", contentTypes: []string{"application/json; profile=arbitrary"}, status: 415},
		{name: "duplicate media", contentTypes: []string{"application/json", "text/plain"}, status: 415},
		{name: "request bytes", bytes: 8, status: 413},
		{name: "query on Search", path: "/_discovery/search?host=private", status: 400},
		{name: "empty manifest query", path: "/_discovery/manifest?", status: 400},
		{name: "malformed List query", path: "/_discovery/agents?pageToken=%zz", status: 400},
		{name: "token byte bound", path: "/_discovery/agents?pageToken=" + strings.Repeat("a", 4097), status: 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := fixtureConfig(t, "https://api.example.test")
			c.PublicDiscoveryEnabled = true
			if test.bytes > 0 {
				c.RequestBytes = test.bytes
			}
			s := directHost(t, c)
			calls := 0
			s.apiClient.Transport = hostRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"results":[]}`))}, nil
			})
			path, method := test.path, http.MethodPost
			if path == "" {
				path = "/_discovery/search"
			}
			if !strings.HasPrefix(path, "/_discovery/search") {
				method = http.MethodGet
			}
			text := test.text
			if text == "" {
				text = "Valid selection"
			}
			body, err := json.Marshal(map[string]any{"text": text, "pageSize": 20})
			if err != nil {
				t.Fatal(err)
			}
			var input io.Reader
			if method == http.MethodPost {
				input = strings.NewReader(string(body))
			}
			r := httptest.NewRequestWithContext(t.Context(), method, c.Origin+path, input)
			r.Header.Set("Origin", c.Origin)
			r.Header.Set("X-Catalog-Request", "1")
			types := test.contentTypes
			if types == nil {
				types = []string{"application/json"}
			}
			for _, media := range types {
				r.Header.Add("Content-Type", media)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			wantCalls := 0
			if test.status == 200 {
				wantCalls = 1
			}
			if w.Code != test.status || calls != wantCalls || len(s.admission) != 0 {
				t.Fatalf("status=%d calls=%d slots=%d", w.Code, calls, len(s.admission))
			}
		})
	}
}
