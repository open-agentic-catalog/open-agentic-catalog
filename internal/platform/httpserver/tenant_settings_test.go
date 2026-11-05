package httpserver

import (
	"context"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/tenantsettings"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type settingsHandlerStore struct {
	readErr, saveErr error
	writes           int
}

func (s *settingsHandlerStore) Get(_ context.Context, tenant string) (tenantsettings.Record, error) {
	return tenantsettings.Record{TenantID: tenant}, s.readErr
}
func (s *settingsHandlerStore) Save(_ context.Context, r tenantsettings.Record, _ tenantsettings.Policy, _ string, _ audit.Record) (tenantsettings.Record, error) {
	s.writes++
	return r, s.saveErr
}
func TestTenantSettingsHandlerConsumptionAndFailures(t *testing.T) {
	for _, mode := range []string{"read", "save", "missing overrides", "null overrides", "unknown field", "oversize", "read failure", "save failure", "denied", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			store := &settingsHandlerStore{}
			service, err := tenantsettings.New(store, 15*time.Minute, 5, clock.NewFake(time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC)), id.Generator{})
			if err != nil {
				t.Fatal(err)
			}
			actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}}
			ctx := identityfixture.WithActor(t.Context(), actor)
			initial, err := service.Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			server := &Server{tenantSettings: service, maxBodyBytes: 1 << 20}
			method := "POST"
			body := `{"overrides":{"newJobMaxAttempts":2},"reason":"Change future attempts"}`
			want := 200
			writes := 0
			switch mode {
			case "read":
				method = "GET"
			case "save":
				writes = 1
			case "missing overrides":
				body = `{"reason":"Reset"}`
				want = 400
			case "null overrides":
				body = `{"overrides":null,"reason":"Reset"}`
				want = 400
			case "unknown field":
				body = `{"overrides":{},"reason":"Reset","other":true}`
				want = 400
			case "oversize":
				body = `{"overrides":{},"reason":"` + strings.Repeat("a", 8192) + `"}`
				want = 400
			case "read failure":
				method = "GET"
				store.readErr = errors.New("dependency failed")
				want = 500
			case "save failure":
				store.saveErr = errors.New("dependency failed")
				want = 500
				writes = 1
			case "denied":
				ctx = t.Context()
				want = 403
			case "unavailable":
				server.tenantSettings = nil
				want = 503
			}
			request := httptest.NewRequestWithContext(ctx, method, "/api/v1/tenant-settings", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("If-Match", service.Tag(initial))
			w := httptest.NewRecorder()
			server.handleTenantSettings(w, request)
			if w.Code != want || store.writes != writes {
				t.Fatalf("status=%d body=%s writes=%d", w.Code, w.Body.String(), store.writes)
			}
			if want == 200 && (w.Header().Get("ETag") == "" || w.Header().Get("Cache-Control") != "no-store") {
				t.Fatal("missing private validator")
			}
		})
	}
}
