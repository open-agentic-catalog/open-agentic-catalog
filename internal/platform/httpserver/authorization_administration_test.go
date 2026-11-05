package httpserver

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

type authorizationProjectionStore struct {
	identity.Store
	identity.AdministrationStore
	actor   identity.Actor
	failure bool
	calls   int
}

func (p *authorizationProjectionStore) ResolvePrincipal(ctx context.Context, principal identity.Principal) (identity.Actor, error) {
	return p.AdministrationStore.ResolvePrincipal(ctx, principal)
}

func (p *authorizationProjectionStore) CheckPermissions(_ context.Context, a identity.Actor, c []identity.PermissionCheck) (identity.PermissionDecision, error) {
	if a.MembershipID != p.actor.MembershipID || len(c) != 1 || c[0].Permission != "access.manage" || c[0].NamespaceID != "" {
		return identity.PermissionDecision{}, identity.E(identity.CodeInternal, "incorrect projection admission")
	}
	return identity.PermissionDecision{Revision: 8, Allowed: []bool{true}}, nil
}
func (p *authorizationProjectionStore) record(c identity.AdministrationChange) error {
	p.calls++
	if c.Actor.MembershipID != p.actor.MembershipID || c.Evidence.RequestID != "access-request" || c.Evidence.OutboxID != "" || c.Evidence.Reason != "Reviewed change" {
		return identity.E(identity.CodeInternal, "incorrect write projection")
	}
	if p.failure {
		return identity.E(identity.CodeConflict, "concurrent change")
	}
	return nil
}
func (p *authorizationProjectionStore) SaveDefinition(_ context.Context, v identity.SaveDefinitionParams) (identity.RoleDefinition, error) {
	return identity.RoleDefinition{ID: v.ID, Revision: 9, Name: v.Input.Name, Permissions: v.Input.Permissions}, p.record(v.AdministrationChange)
}
func (p *authorizationProjectionStore) SaveGroup(_ context.Context, v identity.SaveGroupParams) (identity.AccessGroup, error) {
	return identity.AccessGroup{ID: v.ID, Revision: 9, Name: v.Input.Name, State: v.Transition}, p.record(v.AdministrationChange)
}
func (p *authorizationProjectionStore) ChangeGroupMember(_ context.Context, v identity.ChangeGroupMemberParams) (identity.GroupMember, error) {
	return identity.GroupMember{ID: v.ID, GroupID: v.GroupID, MembershipID: v.MembershipID, Revision: 9}, p.record(v.AdministrationChange)
}
func (p *authorizationProjectionStore) GrantAccessAssignment(_ context.Context, v identity.GrantAccessAssignmentParams) (identity.AccessAssignment, error) {
	return identity.AccessAssignment{ID: v.ID, RoleDefinitionID: v.Input.RoleDefinitionID, GroupID: v.Input.GroupID, Revision: 9}, p.record(v.AdministrationChange)
}
func (p *authorizationProjectionStore) RevokeAccessAssignment(_ context.Context, v identity.AdministrationChange) (identity.AccessAssignment, error) {
	return identity.AccessAssignment{ID: v.ID, State: identity.AssignmentRevoked, Revision: 9}, p.record(v)
}
func TestAuthorizationMutationHandlerProjections(t *testing.T) {
	cases := []struct {
		name, pathKey, body string
		method              string
		create              bool
		handler             func(*Server) http.HandlerFunc
	}{
		{"create role", "", `{"name":"Reviewed role","permissions":["resource.read"],"reason":"Reviewed change"}`, "POST", true, func(s *Server) http.HandlerFunc { return s.handleDefinitionCreate }},
		{"update role", "roleId", `{"name":"Reviewed role","permissions":["resource.read"],"reason":"Reviewed change"}`, "PATCH", false, func(s *Server) http.HandlerFunc { return s.handleDefinitionUpdate }},
		{"archive role", "roleCommand", `{"reason":"Reviewed change"}`, "POST", false, func(s *Server) http.HandlerFunc { return s.dispatchDefinitionCommand }},
		{"create group", "", `{"name":"Reviewed group","reason":"Reviewed change"}`, "POST", true, func(s *Server) http.HandlerFunc { return s.handleAccessGroupCreate }},
		{"update group", "groupId", `{"name":"Reviewed group","reason":"Reviewed change"}`, "PATCH", false, func(s *Server) http.HandlerFunc { return s.handleAccessGroupUpdate }},
		{"suspend group", "groupCommand", `{"reason":"Reviewed change"}`, "POST", false, func(s *Server) http.HandlerFunc { return s.dispatchAccessGroupCommand }},
		{"reactivate group", "groupCommand", `{"reason":"Reviewed change"}`, "POST", false, func(s *Server) http.HandlerFunc { return s.dispatchAccessGroupCommand }},
		{"archive group", "groupCommand", `{"reason":"Reviewed change"}`, "POST", false, func(s *Server) http.HandlerFunc { return s.dispatchAccessGroupCommand }},
		{"add group member", "groupId", `{"membershipId":"` + projectionNext + `","reason":"Reviewed change"}`, "POST", true, func(s *Server) http.HandlerFunc { return s.handleGroupMemberCreate }},
		{"revoke group member", "memberCommand", `{"reason":"Reviewed change"}`, "POST", false, func(s *Server) http.HandlerFunc { return s.dispatchGroupMemberCommand }},
		{"grant assignment", "", `{"roleDefinitionId":"` + projectionItem + `","groupId":"` + projectionNext + `","reason":"Reviewed change"}`, "POST", true, func(s *Server) http.HandlerFunc { return s.handleAccessAssignmentCreate }},
		{"revoke assignment", "accessAssignmentCommand", `{"reason":"Reviewed change"}`, "POST", false, func(s *Server) http.HandlerFunc { return s.dispatchAccessAssignmentCommand }},
	}
	for _, tc := range cases {
		for _, outcome := range []string{"success", "conflict", "malformed body", "missing revision", "invalid command ID"} {
			if outcome == "missing revision" && tc.create || outcome == "invalid command ID" && !strings.Contains(tc.pathKey, "Command") {
				continue
			}
			t.Run(tc.name+"/"+outcome, func(t *testing.T) {
				actor := projectionActor(identity.RoleTenantAdmin)
				store := &authorizationProjectionStore{actor: actor, failure: outcome == "conflict"}
				service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.NewFake(projectionTime), IDs: id.Generator{}, CursorKey: []byte(strings.Repeat("p", 32))})
				if err != nil {
					t.Fatal(err)
				}
				s := projectionServer(nil)
				s.identity = service
				ctx := context.WithValue(identity.WithActor(t.Context(), actor), requestIDKey{}, "access-request")
				body := tc.body
				if outcome == "malformed body" {
					body = `{"reason":`
				}
				request := httptest.NewRequestWithContext(t.Context(), tc.method, "/api/v1/access/test", strings.NewReader(body)).WithContext(ctx)
				request.Header.Set("Content-Type", "application/json")
				if outcome != "missing revision" {
					request.Header.Set("If-Match", `"rev-4"`)
				}
				value := projectionItem
				if strings.Contains(tc.pathKey, "Command") {
					suffix := strings.Fields(tc.name)[0]

					if outcome == "invalid command ID" {
						value = "invalid"
					}
					value += ":" + suffix
				}
				if tc.pathKey != "" {
					request.SetPathValue(tc.pathKey, value)
				}
				if tc.pathKey == "memberCommand" {
					request.SetPathValue("groupId", projectionNext)
				}
				w := httptest.NewRecorder()
				tc.handler(s)(w, request)
				wantCalls := 1
				switch outcome {
				case "conflict":
					requireProjectionProblem(t, w, 409, catalog.CodeConflict)
				case "malformed body", "invalid command ID":
					wantCalls = 0
					requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
				case "missing revision":
					wantCalls = 0
					requireProjectionProblem(t, w, 412, catalog.CodePreconditionFailed)
				case "success":
					want := 200
					if tc.create {
						want = 201
					}
					if w.Code != want || w.Header().Get("ETag") != `"rev-9"` {
						t.Fatalf("projection status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
					}
					var body map[string]any
					if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["revision"] != float64(9) {
						t.Fatal("revision projection", err)
					}
				}
				if store.calls != wantCalls {
					t.Fatalf("write invocations=%d want=%d", store.calls, wantCalls)
				}
				if wantCalls == 0 && (w.Header().Get("ETag") != "" || w.Header().Get("Location") != "") {
					t.Fatal("rejected command exposed success headers")
				}
			})
		}
	}
}
func TestAuthorizationRequestBoundsAndUnknownCommands(t *testing.T) {
	for _, query := range []string{"?limit=0", "?limit=101", "?cursor=", "?cursor=a&cursor=b", "?cursor=" + strings.Repeat("x", 2049)} {
		r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/access/roles"+query, nil)
		if _, _, err := authorizationPageInputs(r); err == nil {
			t.Fatal("invalid page accepted", query)
		}
	}
	for _, tc := range []struct {
		key string
		f   func(*Server) http.HandlerFunc
	}{{"roleCommand", func(s *Server) http.HandlerFunc { return s.dispatchDefinitionCommand }}, {"groupCommand", func(s *Server) http.HandlerFunc { return s.dispatchAccessGroupCommand }}, {"memberCommand", func(s *Server) http.HandlerFunc { return s.dispatchGroupMemberCommand }}, {"accessAssignmentCommand", func(s *Server) http.HandlerFunc { return s.dispatchAccessAssignmentCommand }}} {
		s := projectionServer(nil)
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/access/unknown", nil)
		r.SetPathValue(tc.key, projectionItem+":unknown")
		w := httptest.NewRecorder()
		tc.f(s)(w, r)
		requireProjectionProblem(t, w, 404, catalog.CodeNotFound)
	}
}

func TestAuthorizationReplayGateCoversEveryCachePath(t *testing.T) {
	for _, mode := range []string{"local", "durable", "atomic"} {
		t.Run(mode, func(t *testing.T) {
			base := idempotency.NewMemoryStore()
			logger := slog.New(slog.DiscardHandler)
			cache := newIdempotencyCache(base, logger)
			if mode == "atomic" {
				cache.atomic = &atomicReplayStore{}
			}
			calls, admissions := 0, 0
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("ETag", `"rev-1"`)
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"private":"retained"}`))
			})
			request := func() *http.Request {
				r := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/access/roles", strings.NewReader(`{}`))
				r.Header.Set("Idempotency-Key", "replay-test")
				return r.WithContext(identity.WithActor(r.Context(), projectionActor(identity.RoleTenantAdmin)))
			}
			first := httptest.NewRecorder()
			cache.middleware(1024, next).ServeHTTP(first, request())
			if first.Code != 201 {
				t.Fatal("initial command", first.Code)
			}
			if mode == "durable" {
				cache = newIdempotencyCache(base, logger)
			}
			if mode == "atomic" {
				atomic := cache.atomic
				cache = newIdempotencyCache(base, logger)
				cache.atomic = atomic
			}
			cache.replayAdmission = func(_ *http.Request, _ idempotency.ReplayAuthority, emit func()) error {
				admissions++
				return catalog.E(catalog.CodeForbidden, "current access removed")
			}
			denied := httptest.NewRecorder()
			cache.middleware(1024, next).ServeHTTP(denied, request())
			requireProjectionProblem(t, denied, 403, catalog.CodeForbidden)
			if strings.Contains(denied.Body.String(), "retained") || denied.Header().Get("ETag") != "" || denied.Header().Get("Idempotency-Replayed") != "" || calls != 1 || admissions != 1 {
				t.Fatal("replay escaped fresh admission")
			}
			cache.replayAdmission = func(_ *http.Request, _ idempotency.ReplayAuthority, emit func()) error {
				admissions++
				emit()
				return nil
			}
			allowed := httptest.NewRecorder()
			cache.middleware(1024, next).ServeHTTP(allowed, request())
			if allowed.Code != 201 || allowed.Header().Get("Idempotency-Replayed") != "true" || allowed.Body.String() != first.Body.String() || calls != 1 || admissions != 2 {
				t.Fatal("authorized replay changed retained command")
			}
		})
	}
}

func (p *authorizationProjectionStore) projectionRead(r *identity.AuthorizationPageRequest) error {
	p.calls++
	if r != nil && (r.Limit != 21 || r.AuthorizationRevision != 8) {
		return identity.E(identity.CodeInternal, "unbounded projection read")
	}
	if p.failure {
		return identity.E(identity.CodeConflict, "authorization snapshot changed")
	}
	return nil
}
func (p *authorizationProjectionStore) ListDefinitions(_ context.Context, _ identity.Actor, r identity.AuthorizationPageRequest) ([]identity.RoleDefinition, error) {
	return []identity.RoleDefinition{{ID: projectionItem, Revision: 7, Name: "Reviewed role", Permissions: []identity.Permission{"resource.read"}}}, p.projectionRead(&r)
}
func (p *authorizationProjectionStore) GetDefinition(_ context.Context, _ identity.Actor, key string) (identity.RoleDefinition, error) {
	return identity.RoleDefinition{ID: key, Revision: 7, Name: "Reviewed role"}, p.projectionRead(nil)
}
func (p *authorizationProjectionStore) ListGroups(_ context.Context, _ identity.Actor, r identity.AuthorizationPageRequest) ([]identity.AccessGroup, error) {
	return []identity.AccessGroup{{ID: projectionItem, Revision: 7, Name: "Reviewed group"}}, p.projectionRead(&r)
}
func (p *authorizationProjectionStore) GetGroup(_ context.Context, _ identity.Actor, key string) (identity.AccessGroup, error) {
	return identity.AccessGroup{ID: key, Revision: 7, Name: "Reviewed group"}, p.projectionRead(nil)
}
func (p *authorizationProjectionStore) ListGroupMembers(_ context.Context, _ identity.Actor, r identity.AuthorizationPageRequest) ([]identity.GroupMember, error) {
	if r.GroupID != projectionItem {
		return nil, identity.E(identity.CodeInternal, "group filter lost")
	}
	return []identity.GroupMember{{ID: projectionNext, GroupID: r.GroupID, Revision: 7}}, p.projectionRead(&r)
}
func (p *authorizationProjectionStore) ListAccessAssignments(_ context.Context, _ identity.Actor, r identity.AuthorizationPageRequest) ([]identity.AccessAssignment, error) {
	if r.RoleDefinitionID != projectionItem {
		return nil, identity.E(identity.CodeInternal, "role filter lost")
	}
	return []identity.AccessAssignment{{ID: projectionNext, RoleDefinitionID: r.RoleDefinitionID, Revision: 7}}, p.projectionRead(&r)
}
func (p *authorizationProjectionStore) ListEffectiveAssignments(_ context.Context, _ identity.Actor, r identity.AuthorizationPageRequest) ([]identity.EffectiveAssignment, error) {
	return []identity.EffectiveAssignment{{Assignment: identity.AccessAssignment{ID: projectionItem, Revision: 7}}}, p.projectionRead(&r)
}
func TestAuthorizationReadHandlerProjections(t *testing.T) {
	for _, mode := range []string{"roles", "role", "groups", "group", "members", "impact", "assignments", "self"} {
		for _, outcome := range []string{"success", "conflict", "invalid limit", "invalid cursor"} {
			if (mode == "role" || mode == "group") && strings.HasPrefix(outcome, "invalid") {
				continue
			}
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				actor := projectionActor(identity.RoleTenantAdmin)
				store := &authorizationProjectionStore{actor: actor, failure: outcome == "conflict"}
				service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.NewFake(projectionTime), IDs: id.Generator{}, CursorKey: []byte(strings.Repeat("p", 32))})
				if err != nil {
					t.Fatal(err)
				}
				s := projectionServer(nil)
				s.identity = service
				target := "/api/v1/access/test"
				if mode == "assignments" {
					target += "?roleDefinitionId=" + projectionItem
				}
				if strings.HasPrefix(outcome, "invalid") {
					target = "/api/v1/access/test?limit=0"
					if outcome == "invalid cursor" {
						target = "/api/v1/access/test?cursor="
					}
				}
				r := httptest.NewRequestWithContext(identity.WithActor(t.Context(), actor), "GET", target, nil)
				r.SetPathValue("roleId", projectionItem)
				r.SetPathValue("groupId", projectionItem)
				w := httptest.NewRecorder()
				switch mode {
				case "roles":
					s.handleDefinitionsList(w, r)
				case "role":
					s.handleDefinitionGet(w, r)
				case "groups":
					s.handleAccessGroupsList(w, r)
				case "group":
					s.handleAccessGroupGet(w, r)
				case "members":
					s.handleGroupMembersList(w, r)
				case "impact":
					s.handleDefinitionAssignments(w, r)
				case "assignments":
					s.handleAccessAssignmentsList(w, r)
				case "self":
					s.handleEffectiveAccess(w, r)
				}
				wantCalls := 1
				switch {
				case strings.HasPrefix(outcome, "invalid"):
					wantCalls = 0
					requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
				case outcome == "conflict":
					requireProjectionProblem(t, w, 409, catalog.CodeConflict)
				default:
					if w.Code != 200 {
						t.Fatal("read status", w.Code, w.Body.String())
					}
					var body map[string]any
					if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if mode == "role" || mode == "group" {
						if body["id"] != projectionItem || w.Header().Get("ETag") != `"rev-7"` {
							t.Fatal("read revision binding")
						}
					} else {
						items, ok := body["items"].([]any)
						if !ok || body["authorizationRevision"] != float64(8) || len(items) != 1 {
							t.Fatal("bounded page projection")
						}
					}
				}
				if store.calls != wantCalls {
					t.Fatalf("read invocations=%d want=%d", store.calls, wantCalls)
				}
			})
		}
	}
	s := projectionServer(nil)
	w := httptest.NewRecorder()
	s.handlePermissionCatalog(w, httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/access/permissions", nil))
	var descriptors []identity.PermissionDescriptor
	if err := json.Unmarshal(w.Body.Bytes(), &descriptors); err != nil || len(descriptors) != 50 {
		t.Fatal("permission catalog projection", err)
	}
	for _, d := range descriptors {
		if d.Requires == nil {
			t.Fatal("null companion list")
		}
	}
}

func TestEarlyValidationReplayIsMarkedOnlyBySafeProblemRenderer(t *testing.T) {
	actor := projectionActor(identity.RoleReader)
	ctx := identityfixture.WithActor(t.Context(), actor)
	request := httptest.NewRequestWithContext(ctx, "POST", "/api/v1/namespaces", nil)
	server := &Server{}
	for _, trial := range []struct {
		name    string
		render  func(*responseCapture)
		allowed bool
	}{
		{"safe validation", func(c *responseCapture) {
			writeProblem(&statusWriter{ResponseWriter: c}, request, catalog.E(catalog.CodeInvalidArgument, "invalid namespace input"))
		}, true},
		{"plain forged response", func(c *responseCapture) {
			c.WriteHeader(http.StatusBadRequest)
			_, _ = c.Write([]byte("private bytes"))
		}, false},
		{"private success", func(c *responseCapture) { c.WriteHeader(http.StatusCreated); _, _ = c.Write([]byte("private bytes")) }, false},
		{"internal error", func(c *responseCapture) { writeProblem(c, request, catalog.E(catalog.CodeInternal, "private cause")) }, false},
	} {
		t.Run(trial.name, func(t *testing.T) {
			capture := newResponseCapture()
			trial.render(capture)
			authority := replayAuthority(request, capture)
			emitted := false
			err := server.accessReplayAdmission(request, authority, func() { emitted = true })
			if emitted != trial.allowed || trial.allowed && err != nil || !trial.allowed && catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
				t.Fatal("unsafe/no-check replay admitted", emitted, err)
			}
			if trial.allowed {
				unbound := request.WithContext(t.Context())
				if err := server.accessReplayAdmission(unbound, authority, func() { t.Fatal("unbound validation replay emitted") }); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
					t.Fatal(err)
				}
			}
		})
	}
}

type capabilityProjectionStore struct {
	identity.Store
	identity.AdministrationStore
	check func(identity.Actor, []identity.PermissionCheck) (identity.PermissionDecision, error)
}

func (s capabilityProjectionStore) ResolvePrincipal(ctx context.Context, principal identity.Principal) (identity.Actor, error) {
	return s.AdministrationStore.ResolvePrincipal(ctx, principal)
}

func (s capabilityProjectionStore) CheckPermissions(_ context.Context, actor identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	return s.check(actor, checks)
}

func TestPermissionDecisionHandlerPreservesAdvisoryResultsAndFailures(t *testing.T) {
	actor := projectionActor(identity.RoleReader)
	checks := []identity.PermissionCheck{{Permission: "resource.read", NamespaceID: projectionItem}, {Permission: "resource.edit", NamespaceID: projectionItem}}
	for _, trial := range []struct {
		name, body string
		bound      bool
		failure    error
		status     int
		code       catalog.ErrorCode
		calls      int
	}{
		{"mixed decisions", `{"checks":[{"permission":"resource.read","namespaceId":"` + projectionItem + `"},{"permission":"resource.edit","namespaceId":"` + projectionItem + `"}]}`, true, nil, 200, "", 1},
		{"unavailable policy", `{"checks":[{"permission":"resource.read","namespaceId":"` + projectionItem + `"},{"permission":"resource.edit","namespaceId":"` + projectionItem + `"}]}`, true, identity.E(identity.CodeDependency, "private policy failure"), 503, catalog.CodeDependencyUnavailable, 1},
		{"malformed body", `{"checks":`, true, nil, 400, catalog.CodeInvalidArgument, 0},
		{"unknown field", `{"checks":[],"grant":true}`, true, nil, 400, catalog.CodeInvalidArgument, 0},
		{"no member", `{"checks":[]}`, false, nil, 401, catalog.CodeUnauthenticated, 0},
	} {
		t.Run(trial.name, func(t *testing.T) {
			calls := 0
			store := capabilityProjectionStore{check: func(got identity.Actor, input []identity.PermissionCheck) (identity.PermissionDecision, error) {
				calls++
				if got.MembershipID != actor.MembershipID || !reflect.DeepEqual(input, checks) {
					t.Fatal("permission order, scope or member changed", got, input)
				}
				return identity.PermissionDecision{Revision: 8, Allowed: []bool{true, false}}, trial.failure
			}}
			service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.NewFake(projectionTime), IDs: id.Generator{}, CursorKey: []byte(strings.Repeat("p", 32))})
			if err != nil {
				t.Fatal(err)
			}
			s := projectionServer(nil)
			s.identity = service
			ctx := t.Context()
			if trial.bound {
				ctx = identity.WithActor(ctx, actor)
			}
			r := httptest.NewRequestWithContext(ctx, "POST", "/api/v1/access/capabilities", strings.NewReader(trial.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.handlePermissionDecisions(w, r)
			if trial.status == 200 {
				var got identity.PermissionDecision
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || got.Revision != 8 || !reflect.DeepEqual(got.Allowed, []bool{true, false}) {
					t.Fatal("advisory denial must remain a successful ordered result", got, err, w.Code)
				}
			} else {
				requireProjectionProblem(t, w, trial.status, trial.code)
				if strings.Contains(w.Body.String(), "private policy failure") {
					t.Fatal("dependency cause leaked")
				}
			}
			if calls != trial.calls {
				t.Fatalf("policy calls=%d want=%d", calls, trial.calls)
			}
		})
	}
}

func TestCommandReplayRequiresCurrentActorPermissionsAndJobVisibility(t *testing.T) {
	for _, mode := range []string{"allowed", "old envelope", "missing checks", "no actor", "service actor", "denied permission", "missing Job service", "missing Job", "Job reread failure", "visible Job"} {
		t.Run(mode, func(t *testing.T) {
			actor := projectionActor(identity.RolePublisher)
			authority := idempotency.ReplayAuthority{Version: 1, HumanOnly: true, Checks: []identity.PermissionCheck{{Permission: "resource.read"}}}
			if mode == "service actor" {
				actor.ActorType = identity.ActorService
			}
			if mode == "denied permission" {
				authority.Checks = []identity.PermissionCheck{{Permission: "access.manage"}}
			}
			ctx := identityfixture.WithActor(t.Context(), actor)
			if mode == "no actor" {
				ctx = t.Context()
			}
			if mode == "old envelope" {
				authority.Version = 0
			}
			if mode == "missing checks" {
				authority.Checks = nil
			}
			s := projectionServer(nil)
			reads := 0
			if strings.Contains(mode, "Job") {
				authority.JobID = projectionItem
				if mode != "missing Job service" {
					s = jobProjectionServer(t, jobProjectionStore{get: func(_ context.Context, tenant, key string) (jobs.Job, error) {
						reads++
						if tenant != actor.TenantID || key != authority.JobID {
							t.Fatal("replay Job lookup lost tenant or target")
						}
						if mode == "Job reread failure" && reads > 1 {
							return jobs.Job{}, catalog.E(catalog.CodeDependencyUnavailable, "private Job dependency")
						}
						if mode == "missing Job" {
							return jobs.Job{}, catalog.E(catalog.CodeNotFound, "Job was not found")
						}
						return projectionJob(), nil
					}})
				}
			}
			r := httptest.NewRequestWithContext(ctx, "POST", "/api/v1/resources", nil)
			emissions := 0
			err := s.accessReplayAdmission(r, authority, func() { emissions++ })
			wantCode, wantEmissions, wantRead := catalog.CodeForbidden, 0, false
			if mode == "allowed" || mode == "visible Job" {
				wantCode, wantEmissions = "", 1
			}
			if mode == "missing Job" || mode == "visible Job" || mode == "Job reread failure" {
				wantRead = true
			}
			if mode == "missing Job" {
				wantCode = catalog.CodeNotFound
			}
			if mode == "Job reread failure" {
				wantCode = catalog.CodeDependencyUnavailable
			}
			wrongError := err != nil
			if wantCode != "" {
				wrongError = err == nil || catalog.ErrorCodeOf(err) != wantCode
			}
			if wrongError || emissions != wantEmissions || (reads > 0) != wantRead {
				t.Fatalf("err=%v emissions=%d reads=%d; want code=%s emissions=%d read=%t", err, emissions, reads, wantCode, wantEmissions, wantRead)
			}
		})
	}
}

func TestRatingWithdrawalReplayRequiresCurrentMembershipAndExactOwner(t *testing.T) {
	for _, mode := range []string{"owner without rating grants", "moderator is not the owner", "store unavailable", "service actor", "no current membership"} {
		t.Run(mode, func(t *testing.T) {
			actor := projectionActor(identity.RoleReader)
			actor.Issuer = "https://issuer.example.test"
			original := identityfixture.WithActor(t.Context(), actor)
			value := ratingProjectionRecord(t, original)
			actor.Roles = nil
			if mode == "moderator is not the owner" {
				actor = projectionActor(identity.RoleTenantAdmin)
				actor.Issuer = "https://issuer.example.test"
				actor.Subject = "another-moderator"
			}
			if mode == "service actor" {
				actor.ActorType = identity.ActorService
			}
			ctx := identityfixture.WithActor(t.Context(), actor)
			if mode == "no current membership" {
				ctx = t.Context()
			}
			reads := 0
			s := ratingProjectionServer(t, ratingProjectionStore{get: func(_ context.Context, tenant, id string) (ratings.Record, error) {
				reads++
				if tenant != value.TenantID || id != value.ID {
					t.Fatal("replay changed its rating tenant or identity")
				}
				if mode == "store unavailable" {
					return ratings.Record{}, catalog.E(catalog.CodeDependencyUnavailable, "private ratings storage failure")
				}
				return value, nil
			}})
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/ratings/"+value.ID+":withdraw", strings.NewReader(`{}`))
			emissions := 0
			err := s.accessReplayAdmission(r, idempotency.ReplayAuthority{Version: 1, HumanOnly: true}, func() { emissions++ })
			wantCode, wantEmissions, wantReads := catalog.ErrorCode(""), 0, 0
			switch mode {
			case "owner without rating grants":
				wantEmissions, wantReads = 1, 1
			case "moderator is not the owner":
				wantCode, wantReads = catalog.CodeNotFound, 1
			case "store unavailable":
				wantCode, wantReads = catalog.CodeDependencyUnavailable, 1
			default:
				wantCode = catalog.CodeForbidden
			}
			wrongError := err != nil
			if wantCode != "" {
				wrongError = err == nil || catalog.ErrorCodeOf(err) != wantCode
			}
			if wrongError || emissions != wantEmissions || reads != wantReads {
				t.Fatalf("withdrawal replay: err=%v emissions=%d reads=%d; want %s/%d/%d", err, emissions, reads, wantCode, wantEmissions, wantReads)
			}
		})
	}
}

func TestRatingSubmissionReplayRejectsInvalidLiveReceipt(t *testing.T) {
	s := ratingProjectionServer(t, ratingProjectionStore{})
	body := `{"target":{"origin":"foreign","sourceId":"` + projectionNext + `","presentationGeneration":1,"identifier":"` + federationFixtureAIR + `","versionPresent":false,"kind":"agent"},"receipt":"invalid","score":4}`
	r := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader)), http.MethodPost, "/api/v1/ratings", strings.NewReader(body))
	err := s.accessReplayAdmission(r, idempotency.ReplayAuthority{Version: 1, HumanOnly: true}, func() { t.Fatal("invalid live receipt disclosed a retained command response") })
	if err == nil || catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("rating replay ignored or misclassified the live receipt", err)
	}
}
