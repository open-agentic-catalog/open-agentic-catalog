package httpserver

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestSelfAccessOAuthScopesMatchContract(t *testing.T) {
	contract, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	known := contract.Components.Schemas["CurrentAccess"].Value.Properties["scopes"].Value.Items.Value.Enum
	want := make([]string, 0, len(known))
	claims := map[string]string{"private": "do-not-disclose", "scope:unknown.write": "true"}
	for _, value := range known {
		scope, ok := value.(string)
		if !ok || scope == "" {
			t.Fatalf("invalid scope in current-access contract: %v", value)
		}
		want = append(want, scope)
		claims["scope:"+scope] = "true"
	}
	slices.Sort(want)
	if len(want) == 0 || !slices.Contains(want, "ratings.write") {
		t.Fatal("current-access contract must include the own-rating OAuth ceiling")
	}
	for _, test := range []struct {
		name    string
		enforce bool
		claims  map[string]string
		want    []string
	}{
		{"all contracted scopes", true, claims, want},
		{"ratings write present", true, map[string]string{"scope:ratings.write": "true", "scope:unknown.write": "true", "private": "do-not-disclose"}, []string{"ratings.write"}},
		{"ratings write missing", true, map[string]string{"scope:catalog.read": "true"}, []string{"catalog.read"}},
		{"ratings write false", true, map[string]string{"scope:ratings.write": "false"}, []string{}},
		{"no token claims despite administrator permissions", true, nil, []string{}},
		{"enforcement disabled", false, claims, []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := projectionServer(nil)
			s.enforceOAuthScopes = test.enforce
			actor := projectionActor(identity.RoleTenantAdmin)
			actor.Claims = test.claims
			r := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), actor), http.MethodGet, "/api/v1/me", nil)
			w := httptest.NewRecorder()
			authorizationScopes(test.enforce, http.HandlerFunc(s.handleSelf)).ServeHTTP(w, r)
			var got selfResponse
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
				t.Fatalf("self response: %d %s", w.Code, w.Body.String())
			}
			if !reflect.DeepEqual(got.Scopes, test.want) || got.ScopeEnforcement != test.enforce {
				t.Fatalf("scope projection=%v enforcement=%t; want %v/%t", got.Scopes, got.ScopeEnforcement, test.want, test.enforce)
			}
			if strings.Contains(w.Body.String(), "unknown.write") || strings.Contains(w.Body.String(), "do-not-disclose") {
				t.Fatal("self response disclosed unknown or private claims")
			}
		})
	}
}

func TestSelfAccessWithoutProductRoles(t *testing.T) {
	s := &Server{logger: slog.New(slog.DiscardHandler), cursorKey: []byte(strings.Repeat("k", 32)), now: func() time.Time { return time.Unix(100, 0) }, enforceOAuthScopes: true}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "specialist", MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman, Claims: map[string]string{"private": "do-not-disclose", "scope:audit.read": "true"}, Namespaces: map[string][]identity.Role{"00000000-0000-7000-8000-000000000010": {identity.RoleReviewer}, "00000000-0000-7000-8000-000000000011": {identity.RoleContributor}}}
	request := func(path string, a identity.Actor) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		r = r.WithContext(identityfixture.WithActor(r.Context(), a))
		w := httptest.NewRecorder()
		authorizationScopes(true, s.withQueryParameters(s.handleSelf, "limit", "cursor", "namespaceId")).ServeHTTP(w, r)
		return w
	}
	w := request("/api/v1/me?limit=1", actor)
	if w.Code != 200 || strings.Contains(w.Body.String(), "do-not-disclose") {
		t.Fatalf("self: %d %s", w.Code, w.Body.String())
	}
	var result selfResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Permissions) != 0 || len(result.NamespaceGrants) != 1 || result.NextCursor == "" || len(result.Scopes) != 1 || result.Scopes[0] != "audit.read" {
		t.Fatalf("projection: %#v", result)
	}
	path := "/api/v1/me?limit=1&cursor=" + result.NextCursor
	for _, namespace := range []string{"00000000-0000-7000-8000-000000000011", "00000000-0000-7000-8000-000000000099"} {
		exact := request("/api/v1/me?namespaceId="+namespace, actor)
		var projected selfResponse
		if exact.Code != 200 || json.Unmarshal(exact.Body.Bytes(), &projected) != nil || projected.NextCursor != "" {
			t.Fatalf("exact self grant: %d %s", exact.Code, exact.Body.String())
		}
		if namespace == "00000000-0000-7000-8000-000000000011" && (len(projected.NamespaceGrants) != 1 || projected.NamespaceGrants[0].NamespaceID != namespace) || namespace == "00000000-0000-7000-8000-000000000099" && len(projected.NamespaceGrants) != 0 {
			t.Fatalf("wrong exact grant projection: %#v", projected)
		}
	}
	if filtered := request(path+"&namespaceId=00000000-0000-7000-8000-000000000011", actor); filtered.Code != 400 {
		t.Fatalf("cursor accepted a different filter: %d", filtered.Code)
	}
	if w = request(path, actor); w.Code != 200 {
		t.Fatalf("next: %d %s", w.Code, w.Body.String())
	}
	actor.Subject = "other"
	if w = request(path, actor); w.Code != 400 {
		t.Fatalf("cross-actor cursor: %d", w.Code)
	}
	for _, path := range []string{"/api/v1/me?namespaceId=bad", "/api/v1/me?namespaceId=", "/api/v1/me?tenantId=x", "/api/v1/me?limit=0", "/api/v1/me?limit=1&limit=2", "/api/v1/me?cursor=bad"} {
		if w = request(path, actor); w.Code != 400 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if w = request("/api/v1/me", identity.Actor{}); w.Code != 401 {
		t.Fatalf("missing actor: %d", w.Code)
	}
}

func TestSelfAccessRejectsSignedInvalidNamespacePositions(t *testing.T) {
	s := &Server{logger: slog.New(slog.DiscardHandler), cursorKey: []byte(strings.Repeat("k", 32)), now: func() time.Time { return time.Unix(100, 0) }}
	const prior = "00000000-0000-7000-8000-000000000010"
	const selected = "00000000-0000-7000-8000-000000000011"
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "specialist", MembershipID: "00000000-0000-7000-8000-000000000002", ActorType: identity.ActorHuman, Namespaces: map[string][]identity.Role{selected: {identity.RoleReviewer}}}
	request := func(target string) *http.Request {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		return r.WithContext(identityfixture.WithActor(r.Context(), actor))
	}
	target := "/api/v1/me?limit=1&namespaceId=" + selected
	page, err := s.parseAdminPage(request(target), "self-access", "", "namespaceId", map[string]string{"namespaceId": selected})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		position []string
		valid    bool
	}{
		{"valid namespace", []string{prior}, true},
		{"wrong cardinality", []string{prior, selected}, false},
		{"non UUID namespace", []string{"not-a-namespace"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			token, err := s.encodeAdminCursor(page, test.position)
			if err != nil {
				t.Fatal(err)
			}
			r := request(target + "&cursor=" + token)
			// Prove the envelope is accepted with the exact actor/filter binding;
			// the rejection below must come from the self route's position contract.
			decoded, err := s.parseAdminPage(r, "self-access", "", "namespaceId", map[string]string{"namespaceId": selected})
			if err != nil || !reflect.DeepEqual(decoded.Position, test.position) {
				t.Fatalf("signed envelope rejected before route: %v", err)
			}
			w := httptest.NewRecorder()
			s.handleSelf(w, r)
			if !test.valid {
				requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
				var body map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				_, hasGrants := body["namespaceGrants"]
				_, hasSubject := body["subject"]
				if body["detail"] != "cursor is invalid" || hasGrants || hasSubject {
					t.Fatal("invalid position returned access data or another guard")
				}
				return
			}
			var got selfResponse
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got.NamespaceGrants, []identity.NamespacePermissionAccess{{NamespaceID: selected, Permissions: []identity.Permission{"namespace.read", "ratings.own", "resource.read", "resource.read_content", "resource.read_history", "review.decide"}}}) || got.NextCursor != "" || got.Subject != actor.Subject {
				t.Fatalf("valid self continuation=%+v status=%d", got, w.Code)
			}
		})
	}
}

// A policy change between cursor binding and self selection cannot produce a
// continuation whose digest describes a different policy from its page.
type changingSelfPolicy struct {
	identity.PermissionStore
	checks int
}

func (s *changingSelfPolicy) CheckPermissions(_ context.Context, _ identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	s.checks++
	return identity.PermissionDecision{Revision: int64(s.checks), Allowed: make([]bool, len(checks))}, nil
}
func (s *changingSelfPolicy) ReadSelfAccess(_ context.Context, _ identity.Actor, request identity.SelfAccessRequest) (identity.SelfAccessProjection, error) {
	return identity.SelfAccessProjection{AuthorizationRevision: request.AuthorizationRevision, Permissions: []identity.Permission{}, AvailablePermissions: []identity.Permission{}, NamespaceGrants: []identity.NamespacePermissionAccess{}}, nil
}
func TestSelfAccessRejectsPolicyChangeDuringSelection(t *testing.T) {
	policy := &changingSelfPolicy{}
	actor := identity.BindPermissionStore(identity.Actor{TenantID: projectionTenant, MembershipID: projectionItem, RealmID: projectionResource, Subject: "member", ActorType: identity.ActorHuman}, policy)
	request := httptest.NewRequestWithContext(identity.WithActor(t.Context(), actor), http.MethodGet, "/api/v1/me", nil)
	server := &Server{logger: slog.New(slog.DiscardHandler), cursorKey: []byte(strings.Repeat("k", 32)), now: func() time.Time { return time.Unix(100, 0) }}
	response := httptest.NewRecorder()
	server.handleSelf(response, request)
	requireProjectionProblem(t, response, http.StatusConflict, catalog.CodeConflict)
	if policy.checks != 2 || strings.Contains(response.Body.String(), "namespaceGrants") || strings.Contains(response.Body.String(), "member") {
		t.Fatalf("changed policy disclosed self projection: status=%d checks=%d", response.Code, policy.checks)
	}
}

type unavailableSelfPolicy struct {
	identity.PermissionStore
	reads int
}

func (*unavailableSelfPolicy) CheckPermissions(_ context.Context, _ identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	return identity.PermissionDecision{Revision: 1, Allowed: make([]bool, len(checks))}, nil
}

func (p *unavailableSelfPolicy) ReadSelfAccess(context.Context, identity.Actor, identity.SelfAccessRequest) (identity.SelfAccessProjection, error) {
	p.reads++
	return identity.SelfAccessProjection{}, identity.E(identity.CodeDependency, "private authorization storage error")
}

func TestSelfAccessRefusalsDoNotReturnAnEmptyPermissionProjection(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		policy := &unavailableSelfPolicy{}
		actor := identity.BindPermissionStore(identity.Actor{TenantID: projectionTenant, MembershipID: projectionItem, RealmID: projectionResource, Subject: "member", ActorType: identity.ActorHuman}, policy)
		path := "/api/v1/me"
		status, code, reads := http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable, 1
		if malformed {
			path += "?namespaceId=%zz"
			status, code, reads = http.StatusBadRequest, catalog.CodeInvalidArgument, 0
		}
		r := httptest.NewRequestWithContext(identity.WithActor(t.Context(), actor), http.MethodGet, path, nil)
		s := projectionServer(nil)
		w := httptest.NewRecorder()
		s.handleSelf(w, r)
		requireProjectionProblem(t, w, status, code)
		if policy.reads != reads || strings.Contains(w.Body.String(), "private authorization") || strings.Contains(w.Body.String(), "permissions") || strings.Contains(w.Body.String(), "namespaceGrants") {
			t.Fatalf("failed self read became a private or successful projection: %d %s reads=%d", w.Code, w.Body.String(), policy.reads)
		}
	}
}
