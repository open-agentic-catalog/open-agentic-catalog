package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func TestAdministrationCursorKeyReplacementHasNoOverlap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursor-key")
	writeKey := func(value byte) {
		t.Helper()
		key := make([]byte, 32)
		for i := range key {
			key[i] = value
		}
		if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CATALOG_CURSOR_SIGNING_KEY_FILE", path)
	t.Setenv("CATALOG_CURSOR_SIGNING_KEY", "")
	if err := os.Unsetenv("CATALOG_CURSOR_SIGNING_KEY"); err != nil {
		t.Fatal(err)
	}
	newServer := func() *Server {
		t.Helper()
		key, err := config.CursorSigningKey(config.Defaults())
		if err != nil {
			t.Fatal(err)
		}
		return &Server{cursorKey: key, now: func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }}
	}
	writeKey(1)
	old := newServer()
	binding := adminCursorBinding{Purpose: "namespaces", AuthorizationDigest: "authorization", Order: "slug,id", Limit: 1}
	page := adminPageRequest{Limit: 1, binding: binding}
	token, err := old.encodeAdminCursor(page, []string{"default", "00000000-0000-7000-8000-000000000010"})
	if err != nil {
		t.Fatal(err)
	}
	writeKey(2)
	if _, err := old.decodeAdminCursor(token, binding); err != nil {
		t.Fatal("file replacement changed an initialized cursor verifier")
	}
	fresh := newServer()
	if _, err := fresh.decodeAdminCursor(token, binding); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("replacement verifier accepted an old-key cursor")
	}
	newToken, err := fresh.encodeAdminCursor(page, []string{"default", "00000000-0000-7000-8000-000000000010"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.decodeAdminCursor(newToken, binding); err != nil {
		t.Fatal("replacement verifier rejected its current cursor")
	}
	if _, err := old.decodeAdminCursor(newToken, binding); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("mixed-key verifier accepted a replacement-key cursor")
	}
}

func TestAdministrationCursorBindsAuthorizationAndRequestShape(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	server := &Server{cursorKey: []byte("0123456789abcdef0123456789abcdef"), now: func() time.Time { return now }}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}}
	request := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/resources?kind=agent&limit=2", nil)
	request = request.WithContext(identityfixture.WithActor(request.Context(), actor))
	page, err := server.parseAdminPage(request, "resources", "", "updatedAt desc,id desc", map[string]string{"kind": "agent"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := server.encodeAdminCursor(page, []string{"2026-09-02T11:59:00Z", "00000000-0000-7000-8000-000000000010"})
	if err != nil {
		t.Fatal(err)
	}
	resume := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/resources?kind=agent&limit=2&cursor="+token, nil)
	resume = resume.WithContext(identityfixture.WithActor(resume.Context(), actor))
	decoded, err := server.parseAdminPage(resume, "resources", "", "updatedAt desc,id desc", map[string]string{"kind": "agent"})
	if err != nil || len(decoded.Position) != 2 || decoded.Position[1] != "00000000-0000-7000-8000-000000000010" {
		t.Fatalf("decoded cursor = %#v, %v", decoded, err)
	}

	mutations := map[string]struct {
		url     string
		actor   identity.Actor
		purpose string
		parent  string
		order   string
		filters map[string]string
	}{
		"changed limit":         {url: "/api/v1/resources?kind=agent&limit=3&cursor=" + token, actor: actor, purpose: "resources", order: "updatedAt desc,id desc", filters: map[string]string{"kind": "agent"}},
		"changed filter":        {url: "/api/v1/resources?kind=skill&limit=2&cursor=" + token, actor: actor, purpose: "resources", order: "updatedAt desc,id desc", filters: map[string]string{"kind": "skill"}},
		"changed purpose":       {url: "/api/v1/resources?kind=agent&limit=2&cursor=" + token, actor: actor, purpose: "jobs", order: "updatedAt desc,id desc", filters: map[string]string{"kind": "agent"}},
		"changed parent":        {url: "/api/v1/resources?kind=agent&limit=2&cursor=" + token, actor: actor, purpose: "resources", parent: "other", order: "updatedAt desc,id desc", filters: map[string]string{"kind": "agent"}},
		"changed order":         {url: "/api/v1/resources?kind=agent&limit=2&cursor=" + token, actor: actor, purpose: "resources", order: "id", filters: map[string]string{"kind": "agent"}},
		"changed OAuth ceiling": {url: "/api/v1/resources?kind=agent&limit=2&cursor=" + token, actor: identity.Actor{TenantID: actor.TenantID, Subject: actor.Subject, Roles: actor.Roles, Claims: map[string]string{"scope:catalog.read": "true"}}, purpose: "resources", order: "updatedAt desc,id desc", filters: map[string]string{"kind": "agent"}},
		"changed subject":       {url: "/api/v1/resources?kind=agent&limit=2&cursor=" + token, actor: identity.Actor{TenantID: actor.TenantID, Subject: "other", Roles: actor.Roles}, purpose: "resources", order: "updatedAt desc,id desc", filters: map[string]string{"kind": "agent"}},
		"changed tenant":        {url: "/api/v1/resources?kind=agent&limit=2&cursor=" + token, actor: identity.Actor{TenantID: "00000000-0000-7000-8000-000000000002", Subject: actor.Subject, Roles: actor.Roles}, purpose: "resources", order: "updatedAt desc,id desc", filters: map[string]string{"kind": "agent"}},
	}
	for name, test := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := httptest.NewRequestWithContext(context.Background(), "GET", test.url, nil)
			candidate = candidate.WithContext(identityfixture.WithActor(candidate.Context(), test.actor))
			if _, err := server.parseAdminPage(candidate, test.purpose, test.parent, test.order, test.filters); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("cursor misuse returned %v", err)
			}
		})
	}
}

func TestAdministrationCursorRejectsTamperingExpiryAndMalformedQueries(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	server := &Server{cursorKey: []byte("0123456789abcdef0123456789abcdef"), now: func() time.Time { return now }}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}}
	contextualize := func(target string) *http.Request {
		request := httptest.NewRequestWithContext(context.Background(), "GET", target, nil)
		return request.WithContext(identityfixture.WithActor(request.Context(), actor))
	}
	page, err := server.parseAdminPage(contextualize("/api/v1/namespaces?limit=1"), "namespaces", "", "slug,id", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	token, err := server.encodeAdminCursor(page, []string{"default", "00000000-0000-7000-8000-000000000010"})
	if err != nil {
		t.Fatal(err)
	}
	tampered := "A" + token[1:]
	invalidTargets := []string{
		"/api/v1/namespaces?limit=1&cursor=" + tampered,
		"/api/v1/namespaces?limit=1&cursor=not-a-token",
		"/api/v1/namespaces?limit=1&cursor=",
		"/api/v1/namespaces?limit=1&cursor=" + strings.Repeat("x", adminCursorMaximumSize+1),
		"/api/v1/namespaces?limit=1&limit=2",
		"/api/v1/namespaces?limit=0",
		"/api/v1/namespaces?limit=",
		"/api/v1/namespaces?limit=101",
		"/api/v1/namespaces?unknown=true",
	}
	for _, target := range invalidTargets {
		if _, err := server.parseAdminPage(contextualize(target), "namespaces", "", "slug,id", map[string]string{}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Errorf("invalid target %q returned %v", target, err)
		}
	}
	server.now = func() time.Time { return now.Add(adminCursorTTL) }
	if _, err := server.parseAdminPage(contextualize("/api/v1/namespaces?limit=1&cursor="+token), "namespaces", "", "slug,id", map[string]string{}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("expired cursor returned %v", err)
	}

	unauthenticated := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/namespaces", nil)
	if _, err := server.parseAdminPage(unauthenticated, "namespaces", "", "slug,id", map[string]string{}); catalog.ErrorCodeOf(err) != catalog.CodeUnauthenticated {
		t.Fatalf("unauthenticated page returned %v", err)
	}
}

func TestAdministrationCursorRejectsSignedInvalidPayloads(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	server := &Server{cursorKey: []byte("0123456789abcdef0123456789abcdef"), now: func() time.Time { return now }}
	binding := adminCursorBinding{Purpose: "namespaces", AuthorizationDigest: "authorization", Order: "slug,id", Limit: 1}
	expiresAt := now.Add(time.Minute).Unix()
	invalidPayloads := []string{
		`{`,
		`{"v":1,"p":["alpha"],"e":` + strconv.FormatInt(expiresAt, 10) + `,"unknown":true}`,
		`{"v":1,"p":["alpha"],"e":` + strconv.FormatInt(expiresAt, 10) + `}{}`,
		`{"v":2,"p":["alpha"],"e":` + strconv.FormatInt(expiresAt, 10) + `}`,
		`{"v":1,"p":[],"e":` + strconv.FormatInt(expiresAt, 10) + `}`,
		`{"v":1,"p":["` + strings.Repeat("x", 2049) + `"],"e":` + strconv.FormatInt(expiresAt, 10) + `}`,
	}
	for _, payload := range invalidPayloads {
		token := signAdministrationCursorPayload(t, server, binding, payload)
		if _, err := server.decodeAdminCursor(token, binding); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Errorf("signed invalid cursor payload was accepted: %q, err=%v", payload, err)
		}
	}
	validPayload := `{"v":1,"p":["alpha"],"e":` + strconv.FormatInt(expiresAt, 10) + `}`
	validToken := signAdministrationCursorPayload(t, server, binding, validPayload)
	parts := strings.Split(validToken, ".")
	for _, token := range []string{
		parts[0][:1] + "\n" + parts[0][1:] + "." + parts[1],
		"%%%" + "." + parts[1],
		parts[0] + ".not-hex",
		parts[0] + "." + strings.ToUpper(parts[1]),
	} {
		if _, err := server.decodeAdminCursor(token, binding); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Errorf("malformed cursor token %q returned %v", token, err)
		}
	}
}

func TestAdministrationBodyPageUsesTheSameBoundCursorContract(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	server := &Server{cursorKey: []byte("0123456789abcdef0123456789abcdef"), now: func() time.Time { return now }}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}}
	request := httptest.NewRequestWithContext(identityfixture.WithActor(context.Background(), actor), http.MethodPost, "/api/v1/observations:search", nil)
	limit := 2
	page, err := server.parseObservationSearchPage(request, map[string]string{"query": `{"text":"agent"}`}, &limit, nil)
	if err != nil || page.Limit != limit || len(page.Position) != 0 {
		t.Fatalf("body page = %#v, %v", page, err)
	}
	token, err := server.encodeAdminCursor(page, []string{"80", "urn:air:example.com:default:agent", "00000000-0000-7000-8000-000000000010"})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := server.parseObservationSearchPage(request, map[string]string{"query": `{"text":"agent"}`}, &limit, &token)
	if err != nil || len(resumed.Position) != 3 || resumed.Position[0] != "80" {
		t.Fatalf("resumed body page = %#v, %v", resumed, err)
	}

	zero, tooMany := 0, adminPageMaximumLimit+1
	emptyCursor, oversizedCursor := "", strings.Repeat("x", adminCursorMaximumSize+1)
	invalidCursor := "not-a-token"
	for name, invoke := range map[string]func() error{
		"query parameter": func() error {
			withQuery := httptest.NewRequestWithContext(identityfixture.WithActor(context.Background(), actor), http.MethodPost, "/api/v1/observations:search?limit=2", nil)
			_, parseErr := server.parseObservationSearchPage(withQuery, nil, nil, nil)
			return parseErr
		},
		"zero limit": func() error {
			_, parseErr := server.parseObservationSearchPage(request, nil, &zero, nil)
			return parseErr
		},
		"oversized limit": func() error {
			_, parseErr := server.parseObservationSearchPage(request, nil, &tooMany, nil)
			return parseErr
		},
		"empty cursor": func() error {
			_, parseErr := server.parseObservationSearchPage(request, nil, nil, &emptyCursor)
			return parseErr
		},
		"invalid cursor": func() error {
			_, parseErr := server.parseObservationSearchPage(request, nil, nil, &invalidCursor)
			return parseErr
		},
		"oversized cursor": func() error {
			_, parseErr := server.parseObservationSearchPage(request, nil, nil, &oversizedCursor)
			return parseErr
		},
		"unauthenticated": func() error {
			unauthenticated := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/observations:search", nil)
			_, parseErr := server.parseObservationSearchPage(unauthenticated, nil, nil, nil)
			return parseErr
		},
		"missing tenant": func() error {
			incomplete := request.WithContext(identityfixture.WithActor(request.Context(), identity.Actor{Subject: actor.Subject, Roles: actor.Roles}))
			_, parseErr := server.parseObservationSearchPage(incomplete, nil, nil, nil)
			return parseErr
		},
	} {
		t.Run(name, func(t *testing.T) {
			expected := catalog.CodeInvalidArgument
			if name == "unauthenticated" || name == "missing tenant" {
				expected = catalog.CodeUnauthenticated
			}
			if err := invoke(); catalog.ErrorCodeOf(err) != expected {
				t.Fatalf("invalid body page returned %v", err)
			}
		})
	}
}

func TestQueryBoundaryRejectsMalformedEncoding(t *testing.T) {
	server := &Server{}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}}
	for _, rawQuery := range []string{"name=%zz", "name=one;other=two"} {
		request := httptest.NewRequestWithContext(identityfixture.WithActor(context.Background(), actor), http.MethodGet, "/", nil)
		request.URL.RawQuery = rawQuery
		if err := validateQueryParameters(request, "name"); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Errorf("malformed query %q returned %v", rawQuery, err)
		}
		if _, err := parseLimit(request, 20); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Errorf("malformed limit query %q returned %v", rawQuery, err)
		}
		if _, err := server.parseAdminPage(request, "namespaces", "", "slug,id", map[string]string{"name": ""}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || !strings.Contains(err.Error(), "query parameters are malformed") {
			t.Errorf("malformed admin query %q returned %v", rawQuery, err)
		}
		if _, err := server.parseObservationSearchPage(request, nil, nil, nil); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || !strings.Contains(err.Error(), "query parameters are malformed") {
			t.Errorf("malformed observation query %q returned %v", rawQuery, err)
		}
	}
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?name=one%3Btwo", nil)
	if err := validateQueryParameters(request, "name"); err != nil {
		t.Fatalf("encoded semicolon was rejected: %v", err)
	}
}

func TestAdministrationPageReturnsOnlyTheRequestedBound(t *testing.T) {
	server := &Server{cursorKey: []byte("0123456789abcdef0123456789abcdef"), now: func() time.Time { return time.Unix(0, 0) }}
	request := adminPageRequest{Limit: 2, binding: adminCursorBinding{Purpose: "test", AuthorizationDigest: "digest", Order: "id", Limit: 2}}
	page, err := finishCursorPage(server, request, []string{"one", "two", "three"}, func(value string) []string { return []string{value} })
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("middle page = %#v, %v", page, err)
	}
	final, err := finishCursorPage(server, request, []string{"three"}, func(value string) []string { return []string{value} })
	if err != nil || len(final.Items) != 1 || final.NextCursor != "" {
		t.Fatalf("final page = %#v, %v", final, err)
	}
	for name, position := range map[string][]string{
		"empty position": nil,
		// Invalid upstream versions remain source evidence and can reach
		// observation pagination without a normalized Catalog entry.
		"oversized retained version": {"urn:air:example.com:default:agent", "true", strings.Repeat("v", adminCursorMaximumSize), "01950000-0000-7000-8000-000000000001"},
	} {
		t.Run(name, func(t *testing.T) {
			page, err := finishCursorPage(server, request, []string{"one", "two", "three"}, func(string) []string { return position })
			if catalog.ErrorCodeOf(err) != catalog.CodeInternal || len(page.Items) != 0 || page.NextCursor != "" {
				t.Fatalf("invalid cursor page = %#v, %v", page, err)
			}
		})
	}
}

func signAdministrationCursorPayload(t *testing.T, server *Server, binding adminCursorBinding, payload string) string {
	t.Helper()
	bindingBytes, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, server.cursorKey)
	_, _ = mac.Write([]byte("administration-cursor-v1\x00"))
	_, _ = mac.Write([]byte(payload))
	_, _ = mac.Write([]byte("\x00"))
	_, _ = mac.Write(bindingBytes)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + hex.EncodeToString(mac.Sum(nil))
}

// Exercise both request entry points with signed, otherwise valid tokens so a
// malformed payload cannot mask a missing wire-size bound.
func TestAdministrationPageExactBounds(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"query", "body"} {
		t.Run(mode, func(t *testing.T) {
			server := &Server{cursorKey: []byte(strings.Repeat("k", 32)), now: func() time.Time { return time.Unix(1800000000, 0) }}
			actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}}
			limit := adminPageMaximumLimit
			parse := func(cursor *string) (adminPageRequest, error) {
				request := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), actor), http.MethodGet, "/", nil)
				if mode == "body" {
					return server.parseObservationSearchPage(request, nil, &limit, cursor)
				}
				request.URL.RawQuery = "limit=" + strconv.Itoa(limit)
				if cursor != nil {
					request.URL.RawQuery += "&cursor=" + *cursor
				}
				return server.parseAdminPage(request, "namespaces", "", "slug,id", nil)
			}
			page, err := parse(nil)
			if err != nil || page.Limit != limit {
				t.Fatalf("maximum page limit = %+v, %v", page, err)
			}
			payload := adminCursorPayload{Version: 1, Position: []string{""}, ExpiresAt: server.now().Add(adminCursorTTL).Unix()}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			payload.Position[0] = strings.Repeat("n", (adminCursorMaximumSize-65)*3/4-len(raw))
			token, err := server.encodeAdminCursor(page, payload.Position)
			if err != nil || len(token) != adminCursorMaximumSize {
				t.Fatalf("exact cursor bound: bytes=%d, %v", len(token), err)
			}
			got, err := parse(&token)
			if err != nil || got.Limit != limit || len(got.Position) != 1 || got.Position[0] != payload.Position[0] {
				t.Fatalf("exact bound round trip: %+v, %v", got, err)
			}
			payload.Position[0] += "n"
			if _, err := server.encodeAdminCursor(page, payload.Position); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatalf("oversized encoding: %v", err)
			}
			raw, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			oversized := signAdministrationCursorPayload(t, server, page.binding, string(raw))
			if len(oversized) != adminCursorMaximumSize+1 {
				t.Fatal("oversized fixture has wrong wire length")
			}
			if got, err := parse(&oversized); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || len(got.Position) != 0 {
				t.Fatalf("oversized decoding: %+v, %v", got, err)
			}
		})
	}
}
