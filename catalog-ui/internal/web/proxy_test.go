// Proxy units retain contract routing and request bounds without starting servers.
package web

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func TestDirectHostProxyRouteAllowlist(t *testing.T) {
	t.Parallel()
	const id = "00000000-0000-7000-8000-000000000001"
	c := fixtureConfig(t, "https://api.example.test")
	c.BasePath = "/nested"
	s := directHost(t, c)
	cookie := directHostSession(t, s)
	for _, test := range []struct {
		method, path string
		allowed      bool
	}{
		{http.MethodGet, "/version", true},
		{http.MethodGet, "/openapi/control-v1.yaml", true},
		{http.MethodGet, "/api/v1/resources", true},
		{http.MethodGet, "/api/v1/service-activity", true},
		{http.MethodPost, "/api/v1/service-activity", false},
		{http.MethodHead, "/api/v1/service-activity", false},
		{http.MethodGet, "/api/v1/service-activity/extra", false},
		{http.MethodGet, "/api/v1/publication/hosts", true},
		{http.MethodPost, "/api/v1/publication/hosts", false},
		{http.MethodGet, "/api/v1/publication/hosts/extra", false},
		{http.MethodGet, "/api/v1/resources/" + id, true},
		{http.MethodGet, "/api/v1/version-revisions/" + id + "/export?listingRevisionId=" + id, true},
		{http.MethodPost, "/api/v1/version-revisions/" + id + "/export", false},
		{http.MethodPost, "/api/v1/version-revisions/" + id + ":validate", true},
		{http.MethodPost, "/version", false},
		{http.MethodHead, "/api/v1/resources", false},
		{http.MethodDelete, "/api/v1/resources/" + id, false},
		{http.MethodGet, "/api/v1/catalog/search", false},
		{http.MethodPost, "/api/v1/version-revisions/" + id + ":validate-extra", false},
		{http.MethodGet, "/api/v1/resources/" + id + "/extra", false},
		{http.MethodGet, "/prefix/api/v1/resources", false},
		{http.MethodGet, "/api/v2/resources", false},
		{http.MethodGet, "/api/v1/unknown", false},
		{http.MethodGet, "/metrics", false},
		{http.MethodGet, "/readyz", false},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			calls := 0
			body := &transportResponseBody{reader: strings.NewReader(`{}`)}
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if !test.allowed || calls != 1 || r.Method != test.method || r.URL.String() != c.Upstream+test.path {
					t.Fatal("proxy delegated an unexpected method or path")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}, nil
			})
			w := uiRequest(s, test.method, "/_catalog"+test.path, "", cookie)
			if test.allowed {
				if w.Code != http.StatusOK || w.Body.String() != `{}` || calls != 1 || body.closes != 1 {
					t.Fatal("allowed route did not return and release its upstream response")
				}
			} else {
				assertUIProblem(t, w, http.StatusNotFound, "not_found")
				if calls != 0 {
					t.Fatal("rejected route contacted the API")
				}
			}
			if len(s.admission) != 0 || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("route retained admission or changed browser state or caching")
			}
		})
	}
}

func TestDirectHostProxyRequestBoundsAndSession(t *testing.T) {
	t.Parallel()
	const limit = 512
	const payload = `{"text":"catalog"}`
	exact := payload + strings.Repeat(" ", limit-len(payload))
	for _, test := range []struct {
		name, raw, cookie, code string
		status, read            int
		readError               bool
	}{
		{name: "exact limit", raw: exact, cookie: "valid", status: 200, read: limit},
		{name: "limit plus one", raw: exact + " ", cookie: "valid", status: 413, code: "request_too_large", read: limit + 1},
		{name: "bounded larger body", raw: exact + strings.Repeat(" ", 1024), cookie: "valid", status: 413, code: "request_too_large", read: limit + 1},
		{name: "read failure", raw: payload, cookie: "valid", readError: true, status: 413, code: "request_too_large", read: len(payload)},
		{name: "missing session", raw: payload, status: 401, code: "login_required"},
		{name: "invalid session", raw: payload, cookie: "invalid", status: 401, code: "login_required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := fixtureConfig(t, "https://api.example.test")
			c.RequestBytes = limit
			s := directHost(t, c)
			body := &transportResponseBody{reader: strings.NewReader(test.raw)}
			if test.readError {
				body.reader = io.MultiReader(body.reader, iotest.ErrReader(errors.New("private request read diagnostic")))
			}
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, c.Origin+"/_catalog/api/v1/catalog/search", body)
			r.Header.Set("Origin", c.Origin)
			r.Header.Set("X-Catalog-Request", "1")
			r.Header.Set("Content-Type", "application/json")
			switch test.cookie {
			case "valid":
				r.AddCookie(directHostSession(t, s))
			case "invalid":
				r.Header.Set("Cookie", c.CookieName+"=invalid-session")
			}
			calls := 0
			responseBody := &transportResponseBody{reader: strings.NewReader(`{}`)}
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if test.code != "" || calls != 1 {
					t.Fatal("rejected request reached the API")
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil || string(raw) != exact {
					t.Fatal("exact-limit request body was not preserved")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: responseBody}, nil
			})
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if test.code != "" {
				assertUIProblem(t, w, test.status, test.code)
			} else if w.Code != test.status || w.Body.String() != `{}` || calls != 1 {
				t.Fatal("exact-limit request was rejected")
			}
			if body.bytesRead != test.read || responseBody.closes != calls || len(s.admission) != 0 || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("request bound/cleanup: read=%d want=%d calls=%d closes=%d admission=%d", body.bytesRead, test.read, calls, responseBody.closes, len(s.admission))
			}
		})
	}
}

func TestDirectHostProxyForwarding(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"", "/nested"} {
		t.Run("deployment"+base, func(t *testing.T) {
			c := fixtureConfig(t, "https://api.example.test")
			c.BasePath = base
			s := directHost(t, c)
			const path = "/api/v1/resources"
			const query = "cursor=a%2Bb%2Fc&filter=first&filter=second&url=https%3A%2F%2Funtrusted.invalid%2F"
			// Payload validation belongs to the API; the proxy preserves opaque bytes.
			const requestBody = "{\"displayName\":\"synthetic resource\"}\n"
			const responseBody = "{\"id\":\"created-resource\"}\n"
			wantedHeaders := http.Header{
				"Accept": {"application/json"}, "Content-Type": {"application/json"},
				"If-Match": {`"rev-1"`}, "If-None-Match": {`"other-revision"`},
				"Idempotency-Key": {"command-one"}, "Authorization": {"Bearer test-token"},
			}
			approved := http.Header{
				"Content-Type": {"application/json"}, "Content-Disposition": {`attachment; filename="resource.json"`},
				"Content-Digest": {"sha-256=:synthetic:"}, "Etag": {`"rev-2"`},
				"Idempotency-Replayed": {"true"}, "X-Request-Id": {"request-one"},
				"Retry-After": {"3"}, "Last-Modified": {"Mon, 21 Sep 2026 12:00:00 GMT"},
			}
			upstreamHeaders := approved.Clone()
			for _, name := range []string{"Set-Cookie", "Access-Control-Allow-Origin", "X-Upstream-Private", "Connection", "Server", "WWW-Authenticate"} {
				upstreamHeaders.Set(name, "private upstream value")
			}
			upstreamHeaders.Set("Cache-Control", "public, max-age=3600")
			upstreamHeaders.Set("Content-Security-Policy", "default-src *")
			body := &transportResponseBody{reader: strings.NewReader(responseBody)}
			calls := 0
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				raw, err := io.ReadAll(r.Body)
				if calls != 1 || r.Method != http.MethodPost || r.URL.String() != c.Upstream+path+"?"+query || r.Host != "api.example.test" || err != nil || string(raw) != requestBody || !reflect.DeepEqual(r.Header, wantedHeaders) {
					t.Fatal("proxy changed method/path/query/body or leaked browser headers")
				}
				return &http.Response{StatusCode: http.StatusCreated, Header: upstreamHeaders, Body: body}, nil
			})
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, c.Origin+base+"/_catalog"+path+"?"+query, strings.NewReader(requestBody))
			r.Header = wantedHeaders.Clone()
			r.Header.Set("Origin", c.Origin)
			r.Header.Set("X-Catalog-Request", "1")
			r.Header.Set("Authorization", "Bearer browser-override")
			for _, name := range []string{"Forwarded", "X-Forwarded-Host", "X-Original-Host", "X-Tenant-Id", "Proxy-Authorization", "Connection", "X-Browser-Only", "X-Request-ID", "Traceparent", "Tracestate", "Baggage"} {
				r.Header.Set(name, "browser-supplied")
			}
			r.AddCookie(directHostSession(t, s))
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != http.StatusCreated || w.Body.String() != responseBody || calls != 1 || body.closes != 1 || body.bytesRead != len(responseBody) || len(s.admission) != 0 {
				t.Fatal("forwarding changed the response or retained upstream resources")
			}
			for name, values := range approved {
				if !reflect.DeepEqual(w.Header().Values(name), values) {
					t.Fatalf("approved response header %s was not preserved", name)
				}
			}
			for _, name := range []string{"Set-Cookie", "Access-Control-Allow-Origin", "X-Upstream-Private", "Connection", "Server", "WWW-Authenticate"} {
				if w.Header().Get(name) != "" {
					t.Fatalf("unapproved response header %s reached the browser", name)
				}
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") != "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'" {
				t.Fatal("upstream response weakened host response protections")
			}
		})
	}
}

func TestDirectHostProxyResponseDecisions(t *testing.T) {
	t.Parallel()
	const local = "/api/v1/resources/00000000-0000-7000-8000-000000000001?view=full#details"
	const denied = `{"type":"about:blank","title":"Forbidden","status":403,"code":"FORBIDDEN"}`
	for _, base := range []string{"", "/nested"} {
		t.Run("deployment"+base, func(t *testing.T) {
			for _, test := range []struct {
				name, raw, location, code string
				status                    int
				local, transportError     bool
			}{
				{name: "transport failure", code: "catalog_unavailable", transportError: true},
				{name: "redirect", status: 302, raw: "private redirect response", location: "https://untrusted.invalid/", code: "catalog_redirect_refused"},
				{name: "method-preserving redirect", status: 307, raw: "private redirect response", location: local, code: "catalog_redirect_refused"},
				{name: "conditional response", status: 304},
				{name: "API denial", status: 403, raw: denied},
				{name: "local location", status: 200, raw: `{}`, location: local, local: true},
				{name: "absolute API location", status: 200, raw: `{}`, location: "https://api.example.test" + local},
				{name: "foreign location", status: 200, raw: `{}`, location: "https://untrusted.invalid/"},
				{name: "scheme-relative location", status: 200, raw: `{}`, location: "//untrusted.invalid/path"},
				{name: "non-product location", status: 200, raw: `{}`, location: "/auth/login"},
				{name: "backslash location", status: 200, raw: `{}`, location: `/api/v1/\untrusted.invalid/path`},
			} {
				t.Run(test.name, func(t *testing.T) {
					c := fixtureConfig(t, "https://api.example.test")
					c.BasePath = base
					s := directHost(t, c)
					cookie := directHostSession(t, s)
					body := &transportResponseBody{reader: strings.NewReader(test.raw)}
					calls := 0
					s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						if calls != 1 || r.Method != http.MethodGet || r.URL.String() != c.Upstream+"/api/v1/resources" || r.Header.Get("Authorization") != "Bearer test-token" {
							t.Fatal("response handling followed a redirect or changed the fixed request")
						}
						if test.transportError {
							return nil, errors.New("private transport diagnostic")
						}
						return &http.Response{StatusCode: test.status, Header: http.Header{"Location": {test.location}, "Etag": {`"rev-1"`}, "Content-Type": {"application/problem+json"}, "Set-Cookie": {"upstream=private"}}, Body: body}, nil
					})
					w := uiRequest(s, http.MethodGet, "/_catalog/api/v1/resources", "", cookie)
					wantRead, wantCloses := len(test.raw), 1
					if test.code != "" {
						assertUIProblem(t, w, http.StatusBadGateway, test.code)
						wantRead = 0
						if w.Header().Get("ETag") != "" {
							t.Fatal("rejected upstream response supplied browser headers")
						}
					} else {
						location := ""
						if test.local {
							location = base + "/_catalog" + local
						}
						if w.Code != test.status || w.Body.String() != test.raw || w.Header().Get("ETag") != `"rev-1"` || w.Header().Get("Location") != location {
							t.Fatal("proxy lost response status/body/validator or exposed an unsafe location")
						}
					}
					if test.transportError {
						wantCloses = 0
					}
					if calls != 1 || body.closes != wantCloses || body.bytesRead != wantRead || len(s.admission) != 0 || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != "no-store" {
						t.Fatalf("response ownership: calls=%d closes=%d read=%d admission=%d", calls, body.closes, body.bytesRead, len(s.admission))
					}
				})
			}
		})
	}
}
