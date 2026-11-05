package httpserver

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestActivityHandlerHasBoundedLocalShapeAndClosedInputs(t *testing.T) {
	process, err := processinfo.New()
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{process: process, role: "api", observability: observability.New(nil)}
	handler := server.withQueryParameters(server.handleServiceActivity)
	for _, tt := range []struct {
		method, target, body string
		status               int
	}{
		{"GET", "/api/v1/service-activity", "", http.StatusOK},
		{"HEAD", "/api/v1/service-activity", "", http.StatusOK},
		{"GET", "/api/v1/service-activity?tenantId=other", "", http.StatusBadRequest},
		{"GET", "/api/v1/service-activity?source=prometheus", "", http.StatusBadRequest},
		{"GET", "/api/v1/service-activity?metric=http", "", http.StatusBadRequest},
		{"GET", "/api/v1/service-activity?instance=another", "", http.StatusBadRequest},
		{"GET", "/api/v1/service-activity?window=1h", "", http.StatusBadRequest},
		{"GET", "/api/v1/service-activity", "{}", http.StatusBadRequest},
	} {
		request := httptest.NewRequestWithContext(activityAdminContext(t), tt.method, tt.target, strings.NewReader(tt.body))
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != tt.status {
			t.Fatalf("%s: %d %s", tt.target, response.Code, response.Body.String())
		}
		// Production middleware also applies no-store to early admission failures.
		if tt.status == http.StatusOK {
			var got serviceActivityResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if response.Header().Get("Cache-Control") != "no-store" || got.InstanceID != process.ID() || got.Scope != "responding_api_process" || got.Source != "process_memory" || got.Role != "api" || len(got.HTTP.Families) != 14 || got.MCP.Enabled || got.ElapsedSeconds < 0 {
				t.Fatalf("wrong sample %+v", got)
			}
			if response.Body.Len() > maxServiceActivityBytes {
				t.Fatal("oversized sample")
			}
		}
	}
	for _, fault := range []string{"missing registry", "missing process", "worker role"} {
		bad := &Server{process: server.process, role: server.role, observability: server.observability}
		switch fault {
		case "missing registry":
			bad.observability = nil
		case "missing process":
			bad.process = nil
		case "worker role":
			bad.role = "worker"
		}
		response := httptest.NewRecorder()
		bad.handleServiceActivity(response, httptest.NewRequestWithContext(activityAdminContext(t), "GET", "/api/v1/service-activity", nil))
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "families") {
			t.Fatalf("invalid sample leaked success: %s", response.Body.String())
		}
	}
}

func TestActivityWorstCaseTypedEncodingFitsResponseBudget(t *testing.T) {
	core, err := observability.New(nil).Activity(true)
	if err != nil {
		t.Fatal(err)
	}
	core.HTTP.Completed = math.MaxUint64
	core.HTTP.ActiveCatalog = math.MaxUint64
	// This encoding bound deliberately permits larger combinations than valid
	// disjoint populations; snapshot validity has separate rejection tests.
	for i := range core.HTTP.Families {
		row := &core.HTTP.Families[i]
		row.Completed = math.MaxUint64
		row.DurationSeconds = math.MaxFloat64
		for j := range row.Methods {
			row.Methods[j].Completed = math.MaxUint64
		}
		for j := range row.StatusClasses {
			row.StatusClasses[j].Completed = math.MaxUint64
		}
	}
	for i := range core.ControlSearch.Outcomes {
		core.ControlSearch.Outcomes[i].Completed = math.MaxUint64
	}
	for i := range core.MCP.Outcomes {
		core.MCP.Outcomes[i].Completed = math.MaxUint64
	}
	stamp := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	sample := serviceActivityResponse{Schema: "service-activity-v1", Population: "catalog-http-v1", Scope: "responding_api_process", Source: "process_memory", InstanceID: "ffffffff-ffff-7fff-bfff-ffffffffffff", Role: "all", StartedAt: stamp, ObservedAt: stamp, ElapsedSeconds: math.MaxFloat64, ActivitySample: core}
	raw, err := json.Marshal(sample)
	if err != nil || len(raw) > maxServiceActivityBytes {
		t.Fatalf("response exceeds fixed budget: %d %v", len(raw), err)
	}
	if !strings.Contains(string(raw), `"completed":"18446744073709551615"`) || !strings.Contains(string(raw), `"activeCatalog":"18446744073709551615"`) {
		t.Fatal("counter precision lost")
	}
	t.Logf("worst-case typed shape: %d of %d bytes", len(raw), maxServiceActivityBytes)
}

func activityAdminContext(t *testing.T) context.Context {
	t.Helper()
	return identityfixture.WithActor(t.Context(), identity.Actor{TenantID: projectionTenant, Subject: "activity-admin", Roles: []identity.Role{identity.RoleTenantAdmin}})
}

func TestActivityCurrentPermissionAndOAuthAdmission(t *testing.T) {
	p, err := processinfo.New()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{process: p, role: "api", observability: observability.New(nil)}
	for _, tt := range []struct {
		name                   string
		role                   identity.Role
		scope, enforce, forged bool
		status                 int
	}{
		{name: "administrator", role: identity.RoleTenantAdmin, status: 200},
		{name: "scoped administrator", role: identity.RoleTenantAdmin, scope: true, enforce: true, status: 200},
		{name: "administrator missing scope", role: identity.RoleTenantAdmin, enforce: true, status: 403},
		{name: "scoped reader", role: identity.RoleReader, scope: true, enforce: true, status: 403},
		{name: "grantless service", scope: true, enforce: true, status: 403},
		{name: "forged administrator role", role: identity.RoleTenantAdmin, scope: true, enforce: true, forged: true, status: 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			actor := identity.Actor{TenantID: projectionTenant, Subject: "activity-principal", Claims: map[string]string{}}
			if tt.role != "" {
				actor.Roles = []identity.Role{tt.role}
			} else {
				actor.ActorType = identity.ActorService
			}
			if tt.scope {
				actor.Claims["scope:tenant.admin"] = "true"
			}
			ctx := identityfixture.WithActor(t.Context(), actor)
			if tt.forged {
				ctx = identity.WithActor(t.Context(), actor)
			}
			for _, method := range []string{"GET", "HEAD"} {
				req := httptest.NewRequestWithContext(ctx, method, "/api/v1/service-activity", nil)
				response := httptest.NewRecorder()
				authorizationScopes(tt.enforce, s.withQueryParameters(s.handleServiceActivity)).ServeHTTP(response, req)
				if response.Code != tt.status {
					t.Fatalf("%s: %d %s", method, response.Code, response.Body.String())
				}
				if tt.status != 200 && strings.Contains(response.Body.String(), "families") {
					t.Fatal("denial disclosed counters")
				}
			}
		})
	}
}

func TestActivityCapabilityUsesOnlyExplicitEnabledOrDisabledStates(t *testing.T) {
	for _, test := range []struct {
		input CapabilityState
		want  CapabilityState
	}{
		{"", CapabilityDisabled},
		{CapabilityDisabled, CapabilityDisabled},
		{CapabilityEnabled, CapabilityEnabled},
	} {
		snapshot, err := normalizeCapabilities(CapabilitySnapshot{ServiceActivity: test.input}, false)
		if err != nil || snapshot.ServiceActivity != test.want {
			t.Fatalf("activity state %q: got %q, error %v", test.input, snapshot.ServiceActivity, err)
		}
	}
	for _, invalid := range []CapabilityState{CapabilityUnavailable, "unknown"} {
		if _, err := normalizeCapabilities(CapabilitySnapshot{ServiceActivity: invalid}, false); err == nil {
			t.Fatalf("accepted unsupported activity state %q", invalid)
		}
	}
}
