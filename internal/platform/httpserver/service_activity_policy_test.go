package httpserver

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
)

type activityPolicy struct {
	identity.PermissionStore
	allowed bool
	failure error
	calls   int
}

func (p *activityPolicy) CheckPermissions(_ context.Context, _ identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	p.calls++
	if len(checks) != 1 || checks[0].Permission != "access.manage" || checks[0].NamespaceID != "" {
		panic("wrong activity authority")
	}
	return identity.PermissionDecision{Revision: 1, Allowed: []bool{p.allowed}}, p.failure
}

// These units prove a cached principal cannot substitute for a fresh policy
// check. Native PostgreSQL authority changes retain their owning integration tests.
func TestActivityRechecksPolicyAndFailsClosed(t *testing.T) {
	process, err := processinfo.New()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{process: process, role: "api", observability: observability.New(nil)}
	policy := &activityPolicy{allowed: true}
	actor := identity.BindPermissionStore(identity.Actor{TenantID: projectionTenant, Subject: "cached-admin"}, policy)
	ctx := identity.WithActor(t.Context(), actor)
	for _, state := range []struct {
		allowed bool
		failure error
		status  int
	}{
		{true, nil, 200},
		{false, nil, 403},
		{false, identity.E(identity.CodeUnauthenticated, "membership suspended"), 403},
		{false, identity.E(identity.CodeDependency, "private database error"), 503},
		{true, nil, 200},
	} {
		policy.allowed, policy.failure = state.allowed, state.failure
		req := httptest.NewRequestWithContext(ctx, "GET", "/api/v1/service-activity", nil)
		response := httptest.NewRecorder()
		s.handleServiceActivity(response, req)
		if response.Code != state.status {
			t.Fatalf("policy result: %d %s", response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		if strings.Contains(response.Body.String(), "private database error") || state.status != 200 && strings.Contains(response.Body.String(), "families") {
			t.Fatal("unsafe failure")
		}
	}
	if policy.calls != 5 {
		t.Fatalf("expected five fresh permission checks, got %d", policy.calls)
	}
}
