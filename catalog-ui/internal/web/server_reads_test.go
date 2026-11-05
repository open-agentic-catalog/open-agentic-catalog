// Host read units use temporary assets, recorders and controlled API responses.
package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/buildinfo"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
)

func TestDirectHostNavigationAndAssets(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"", "/nested"} {
		t.Run("deployment"+base, func(t *testing.T) {
			c := fixtureConfig(t, "https://api.example.test")
			c.BasePath = base
			if err := os.Mkdir(filepath.Join(c.Assets, "assets", "folder"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(c.Assets, "assets", ".private"), []byte("private asset"), 0600); err != nil {
				t.Fatal(err)
			}
			s := directHost(t, c)
			index := `<!doctype html><script type="module" src="` + base + `/assets/app-test.js"></script>`
			const asset = "export const ok = true;"
			for _, test := range []struct {
				method, route, body, code string
				status                    int
				asset                     bool
			}{
				{method: "GET", route: "/", status: 200, body: index},
				{method: "GET", route: "/access", status: 200, body: index},
				{method: "GET", route: "/service", status: 200, body: index},
				{method: "GET", route: "/overview?view=content", status: 200, body: index},
				{method: "GET", route: "/overview/private", status: 404, code: "not_found"},
				{method: "GET", route: "/api-reference", status: 200, body: index},
				{method: "GET", route: "/catalog", status: 200, body: index},
				{method: "GET", route: "/catalog/record", status: 200, body: index},
				{method: "GET", route: "/indexed-record", status: 200, body: index},
				{method: "GET", route: "/my-ratings", status: 200, body: index},
				{method: "GET", route: "/my-ratings/00000000-0000-7000-8000-000000000001", status: 200, body: index},
				{method: "GET", route: "/ratings/00000000-0000-7000-8000-000000000001", status: 200, body: index},
				{method: "GET", route: "/feedback/00000000-0000-7000-8000-000000000001", status: 200, body: index},
				{method: "GET", route: "/my-feedback/00000000-0000-7000-8000-000000000001", status: 200, body: index},
				{method: "GET", route: "/my-work", status: 200, body: index},
				{method: "GET", route: "/saved-items", status: 200, body: index},
				{method: "GET", route: "/saved-items/new", status: 200, body: index},
				{method: "GET", route: "/ratings", status: 200, body: index},
				{method: "GET", route: "/ratings/unknown", status: 404, code: "not_found"},
				{method: "GET", route: "/resources", status: 200, body: index},
				{method: "GET", route: "/resources/record", status: 200, body: index},
				{method: "HEAD", route: "/resources/record", status: 200},
				{method: "GET", route: "/api/v1/me", status: 404, code: "not_found"},
				{method: "GET", route: "/assets", status: 404, code: "not_found"},
				{method: "GET", route: "/unknown", status: 404, code: "not_found"},
				{method: "GET", route: "/indexed-record/record", status: 404, code: "not_found"},
				{method: "GET", route: "/resources/record/extra", status: 404, code: "not_found"},
				{method: "GET", route: "/resources/", status: 404, code: "not_found"},
				{method: "GET", route: "/catalog/../service", status: 400, code: "invalid_ui_request"},
				{method: "GET", route: "/catalog//record", status: 400, code: "invalid_ui_request"},
				{method: "GET", route: "/assets/%2e%2e/index.html", status: 400, code: "invalid_ui_request"},
				{method: "GET", route: "/catalog\\record", status: 400, code: "invalid_ui_request"},
				{method: "GET", route: "/catalog/%00", status: 400, code: "invalid_ui_request"},
				{method: "GET", route: "/assets/app-test.js", status: 200, body: asset, asset: true},
				{method: "HEAD", route: "/assets/app-test.js", status: 200, asset: true},
				{method: "POST", route: "/assets/app-test.js", status: 405, code: "method_not_allowed"},
				{method: "GET", route: "/assets/missing.js", status: 404, code: "not_found"},
				{method: "GET", route: "/assets/folder", status: 404, code: "not_found"},
				{method: "GET", route: "/assets/.private", status: 404, code: "not_found"},
			} {
				t.Run(test.method+" "+test.route, func(t *testing.T) {
					// Navigation and assets do not require a session or browser command headers.
					r := httptest.NewRequestWithContext(t.Context(), test.method, c.Origin+base+test.route, nil)
					w := httptest.NewRecorder()
					s.ServeHTTP(w, r)
					cache := "no-store"
					if test.asset {
						cache = "public, max-age=31536000, immutable"
					}
					if w.Code != test.status || w.Header().Get("Cache-Control") != cache || w.Header().Get("Set-Cookie") != "" || len(s.admission) != 0 {
						t.Fatalf("route status/cache/cookies/admission = %d/%q/%q/%d", w.Code, w.Header().Get("Cache-Control"), w.Header().Get("Set-Cookie"), len(s.admission))
					}
					if test.code != "" {
						assertUIProblem(t, w, test.status, test.code)
						return
					}
					if w.Body.String() != test.body || w.Header().Get("X-Content-Type-Options") != "nosniff" {
						t.Fatal("GET/HEAD lost exact content or response protections")
					}
					if test.asset {
						if w.Header().Get("Content-Length") != strconv.Itoa(len(asset)) {
							t.Fatal("asset GET/HEAD lost the representation length")
						}
					} else if w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
						t.Fatal("navigation did not return the HTML shell")
					}
				})
			}
		})
	}
}

func TestDirectHostCurrentSessionWithoutUsableCookie(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"anonymous", "invalid"} {
		t.Run(name, func(t *testing.T) {
			s := directHost(t, fixtureConfig(t, "https://api.example.test"))
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, s.config.Origin+"/auth/session", nil)
			r.Header.Set("X-Catalog-Request", "1")
			if name == "invalid" {
				r.Header.Set("Cookie", s.config.CookieName+"=invalid-session")
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if name == "invalid" {
				assertUIProblem(t, w, http.StatusUnauthorized, "login_required")
			} else {
				assertSessionProjection(t, w, "null", 0)
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" || len(s.admission) != 0 {
				t.Fatal("session read changed browser state, caching or admission")
			}
		})
	}
}

func TestDirectHostCurrentSessionResponses(t *testing.T) {
	t.Parallel()
	const limit = 64 << 10
	selfContext := strings.Replace(testAccess, `"subject":`, `"realmId":"00000000-0000-7000-8000-000000000003","subject":`, 1)
	exact := selfContext + strings.Repeat(" ", limit-len(selfContext))
	for _, test := range []struct {
		name, raw, code           string
		status, wantStatus        int
		readError, transportError bool
	}{
		{name: "exact limit", raw: exact, status: 200, wantStatus: 200},
		{name: "service actor", raw: strings.Replace(selfContext, `"actorType":"human"`, `"actorType":"service"`, 1), status: 200, wantStatus: 200},
		{name: "overflow whitespace", raw: exact + " ", status: 200, wantStatus: 503, code: "catalog_unavailable"},
		{name: "overflow trailing input", raw: exact + "private invalid trailing input", status: 200, wantStatus: 503, code: "catalog_unavailable"},
		{name: "bounded large response", raw: exact + strings.Repeat(" ", 1024), status: 200, wantStatus: 503, code: "catalog_unavailable"},
		{name: "missing tenant", raw: strings.Replace(selfContext, `"tenantId":"00000000-0000-7000-8000-000000000001",`, "", 1), status: 200, wantStatus: 503, code: "catalog_unavailable"},
		{name: "missing membership", raw: strings.Replace(selfContext, `"membershipId":"00000000-0000-7000-8000-000000000002",`, "", 1), status: 200, wantStatus: 503, code: "catalog_unavailable"},
		{name: "missing subject", raw: strings.Replace(selfContext, `"subject":"reader",`, "", 1), status: 200, wantStatus: 503, code: "catalog_unavailable"},
		{name: "malformed JSON", raw: `{"subject":`, status: 200, wantStatus: 503, code: "catalog_unavailable"},
		{name: "wrong field type", raw: strings.Replace(selfContext, `"subject":"reader"`, `"subject":7`, 1), status: 200, wantStatus: 503, code: "catalog_unavailable"},
		{name: "trailing JSON", raw: selfContext + `{}`, status: 200, wantStatus: 503, code: "catalog_unavailable"},
		{name: "read failure", raw: selfContext, status: 200, readError: true, wantStatus: 503, code: "catalog_unavailable"},
		{name: "unauthorized", raw: "private upstream diagnostic", status: 401, wantStatus: 401, code: "catalog_login_denied"},
		{name: "forbidden", raw: "private upstream diagnostic", status: 403, wantStatus: 401, code: "catalog_login_denied"},
		{name: "unavailable", raw: "private upstream diagnostic", status: 503, wantStatus: 503, code: "catalog_unavailable"},
		{name: "redirect", raw: "private upstream diagnostic", status: 302, wantStatus: 503, code: "catalog_unavailable"},
		{name: "transport failure", transportError: true, wantStatus: 503, code: "catalog_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := fixtureConfig(t, "https://api.example.test")
			c.BasePath = "/nested"
			s := directHost(t, c)
			cookie := directHostSession(t, s)
			body := &transportResponseBody{reader: strings.NewReader(test.raw)}
			if test.readError {
				body.reader = io.MultiReader(body.reader, iotest.ErrReader(errors.New("private read diagnostic")))
			}
			calls := 0
			s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls != 1 || r.Method != http.MethodGet || r.URL.String() != "https://api.example.test/api/v1/me" || r.Header.Get("Accept") != "application/json" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Cookie") != "" {
					t.Fatal("self-context request lost fixed path or session binding")
				}
				if test.transportError {
					return nil, errors.New("private transport diagnostic")
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Location": {"https://untrusted.invalid/"}}, Body: body, ContentLength: -1}, nil
			})
			w := uiRequest(s, http.MethodGet, "/auth/session?url=https://untrusted.invalid/", "", cookie)
			if w.Code != test.wantStatus {
				t.Fatalf("session read status=%d want=%d for %d upstream bytes", w.Code, test.wantStatus, len(test.raw))
			}
			if test.code != "" {
				assertUIProblem(t, w, test.wantStatus, test.code)
			} else {
				assertSessionProjection(t, w, test.raw, s.now().Add(time.Hour).Unix())
			}
			wantRead, wantCloses := min(len(test.raw), limit+1), 1
			if test.status != http.StatusOK {
				wantRead = 0
			}
			if test.transportError {
				wantCloses = 0
			}
			if calls != 1 || body.closes != wantCloses || body.bytesRead != wantRead || len(s.admission) != 0 {
				t.Fatalf("self-context ownership: calls=%d closes=%d bytes=%d want=%d admission=%d", calls, body.closes, body.bytesRead, wantRead, len(s.admission))
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "test-token") {
				t.Fatal("self-context response lost privacy or changed browser state")
			}
		})
	}
}

func TestDirectHostCurrentSessionRefreshesAccess(t *testing.T) {
	t.Parallel()
	s := directHost(t, fixtureConfig(t, "https://api.example.test"))
	at := s.now()
	s.now = func() time.Time { return at }
	expires := at.Add(time.Hour).Unix()
	cookie := directHostSession(t, s)
	bodies := []*transportResponseBody{
		{reader: strings.NewReader(testAccess)},
		{reader: strings.NewReader(strings.Replace(testAccess, `"permissions":["resource.read"]`, `"permissions":[]`, 1))},
	}
	calls := 0
	s.apiClient.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls >= len(bodies) || r.URL.String() != "https://api.example.test/api/v1/me" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatal("session refresh made an unexpected request")
		}
		body := bodies[calls]
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}, nil
	})
	for index, raw := range []string{testAccess, strings.Replace(testAccess, `"permissions":["resource.read"]`, `"permissions":[]`, 1)} {
		w := uiRequest(s, http.MethodGet, "/auth/session", "", cookie)
		assertSessionProjection(t, w, raw, expires)
		if calls != index+1 || bodies[index].closes != 1 || len(s.admission) != 0 || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("session reused stale access, changed its cookie or retained resources")
		}
		at = at.Add(time.Minute)
	}
}

func assertSessionProjection(t *testing.T, w *httptest.ResponseRecorder, raw string, expires int64) {
	t.Helper()
	var access, got any
	if err := json.Unmarshal([]byte(raw), &access); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"access": access, "expiresAt": float64(expires)}
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || !reflect.DeepEqual(got, want) {
		t.Fatal("session projection changed access, absolute expiry or response shape")
	}
}

// Build reads return only this host's embedded metadata and never consult the API.
func TestHostBuildRead(t *testing.T) {
	for _, base := range []string{"", "/nested"} {
		t.Run(base, func(t *testing.T) {
			c := fixtureConfig(t, "https://api.example.test")
			c.BasePath = base
			s, err := New(c, nil, buildinfo.New("ui-build", "own-ui-commit", "modified"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			s.apiClient = &http.Client{Transport: noBuildFetch{t}}
			w := uiRequest(s, "GET", "/_build", "")
			var got buildinfo.Info
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got != (buildinfo.Info{Version: "ui-build", Commit: "own-ui-commit", SourceState: "modified"}) || w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("unsafe build response: %d %s", w.Code, w.Body.String())
			}
			if w := uiRequest(s, "POST", "/_build", ""); w.Code != 404 {
				t.Fatal("build read allowed mutation")
			}
			r := httptest.NewRequestWithContext(t.Context(), "GET", c.Origin+base+"/_build", nil)
			w = httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal("host build omitted same-origin request protection")
			}
			r.Header.Set("X-Catalog-Request", "1")
			r.Header.Set("Origin", "https://foreign.example")
			w = httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal("foreign build read accepted")
			}
			s.Drain()
			if w := uiRequest(s, "GET", "/_build", ""); w.Code != 503 {
				t.Fatal("drain bypassed")
			}
		})
	}
}

type noBuildFetch struct{ t *testing.T }

func (f noBuildFetch) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Fatal("host build contacted API")
	return nil, errors.New("forbidden")
}

func TestPolicyAdministrationNavigationIsBounded(t *testing.T) {
	for _, route := range []string{"access-roles", "access-groups", "access-assignments", "trust-assessments"} {
		for _, suffix := range []string{"", "/new", "/00000000-0000-7000-8000-000000000001"} {
			if !clientNavigation("/" + route + suffix) {
				t.Fatalf("implemented policy route rejected: %s%s", route, suffix)
			}
		}
		for _, suffix := range []string{"/", "/new/private", "/member/history"} {
			if clientNavigation("/" + route + suffix) {
				t.Fatalf("unimplemented policy route admitted: %s%s", route, suffix)
			}
		}
	}
}

func TestPrivateCaseNavigationIsBounded(t *testing.T) {
	for _, family := range []string{"feedback", "my-feedback", "ratings", "my-ratings"} {
		for _, suffix := range []string{"", "/00000000-0000-7000-8000-000000000001"} {
			if !clientNavigation("/" + family + suffix) {
				t.Fatalf("case route rejected: %s%s", family, suffix)
			}
		}
		for _, suffix := range []string{"/", "/new", "/unknown", "/private/item", "/00000000-0000-7000-8000-000000000001/extra"} {
			if clientNavigation("/" + family + suffix) {
				t.Fatalf("unimplemented case route admitted: %s%s", family, suffix)
			}
		}
	}
}

func TestHostStatusIsIndependentAndOriginProtected(t *testing.T) {
	for _, base := range []string{"", "/nested"} {
		t.Run(base, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := fixtureConfig(t, "https://api.example.test")
				c.BasePath = base
				s, err := New(c, nil, buildinfo.New("ui-build", "ui-sha", "clean"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(s.Close)
				s.apiClient = &http.Client{Transport: noBuildFetch{t}}
				time.Sleep(2 * time.Second)
				w := uiRequest(s, "GET", "/_status", "")
				var got struct {
					buildinfo.Info
					processinfo.Snapshot
					Role string `json:"role"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" || got.InstanceID != s.process.ID() || got.UptimeSeconds != 2 || !got.StartedAt.Equal(s.process.Read().StartedAt) || got.Role != "ui" || got.Version != "ui-build" || got.Commit != "ui-sha" {
					t.Fatalf("host status: %d %s", w.Code, w.Body.String())
				}
				for _, query := range []string{"?", "?limit=1"} {
					if w := uiRequest(s, "GET", "/_status"+query, ""); w.Code != 400 {
						t.Fatal("status accepted query")
					}
				}
				if w := uiRequest(s, "POST", "/_status", ""); w.Code != 404 {
					t.Fatal("status accepted mutation")
				}
				r := httptest.NewRequestWithContext(t.Context(), "GET", c.Origin+base+"/_status", nil)
				w = httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != 403 {
					t.Fatal("missing origin proof accepted")
				}
				r.Header.Set("X-Catalog-Request", "1")
				r.Header.Set("Origin", "https://foreign.example")
				w = httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != 403 {
					t.Fatal("foreign origin accepted")
				}
				r.Header.Del("Origin")
				r.Header.Set("Sec-Fetch-Site", "cross-site")
				w = httptest.NewRecorder()
				s.ServeHTTP(w, r)
				assertUIProblem(t, w, http.StatusForbidden, "same_origin_required")
				// The status route participates in admission, even though it performs no upstream work.
				for range cap(s.admission) {
					s.admission <- struct{}{}
				}
				w = uiRequest(s, "GET", "/_status", "")
				assertUIProblem(t, w, http.StatusServiceUnavailable, "ui_busy")
				if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" || len(s.admission) != cap(s.admission) {
					t.Fatal("busy status cached, wrote a cookie or changed occupied admission slots")
				}
				for range cap(s.admission) {
					<-s.admission
				}
				if w := uiRequest(s, "GET", "/_status", ""); w.Code != http.StatusOK || len(s.admission) != 0 {
					t.Fatal("status did not recover or leaked an admission slot")
				}
				next, err := New(c, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(next.Close)
				if next.process.ID() == s.process.ID() || next.process.Read().UptimeSeconds != 0 {
					t.Fatal("UI restart retained execution identity")
				}
				s.Drain()
				w = uiRequest(s, "GET", "/_status", "")
				assertUIProblem(t, w, http.StatusServiceUnavailable, "ui_draining")
				if len(s.admission) != 0 {
					t.Fatal("drained status occupied an admission slot")
				}
			})
		})
	}
}

func TestSettingsNavigationIsSingleton(t *testing.T) {
	if !clientNavigation("/settings") {
		t.Fatal("settings route rejected")
	}
	for _, route := range []string{"/settings/", "/settings/new", "/settings/other-tenant", "/settings/private/item"} {
		if clientNavigation(route) {
			t.Fatalf("unimplemented settings route admitted: %s", route)
		}
	}
}

func TestPersonalNavigationIsFinite(t *testing.T) {
	for _, route := range []string{"/my-work", "/saved-items", "/saved-items/new", "/saved-items/00000000-0000-7000-8000-000000000001"} {
		if !clientNavigation(route) {
			t.Fatal(route)
		}
	}
	for _, route := range []string{"/my-work/private", "/saved-items/", "/saved-items/private/more"} {
		if clientNavigation(route) {
			t.Fatal("unbounded personal route", route)
		}
	}
}
