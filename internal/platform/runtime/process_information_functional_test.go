//go:build functional

package runtime

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
)

// Real composition and middleware qualify roles and restarts with tracing off.
func TestProcessInformationRuntimeRolesAndRestarts(t *testing.T) {
	for _, name := range config.EnvironmentVariables() {
		unsetEnvironment(t, name)
	}
	for _, role := range []config.Role{config.RoleAPI, config.RoleWorker, config.RoleAll} {
		t.Run(string(role), func(t *testing.T) {
			cfg := localRuntimeConfig()
			cfg.Auth.DevelopmentTokens["reader"] = config.DevelopmentUser{Subject: "reader", TenantID: cfg.Discovery.Hosts[0].TenantID, Roles: []identity.Role{identity.RoleReader}}
			cfg.Auth.DevelopmentTokens["service-client"] = config.DevelopmentUser{Subject: "service-client", TenantID: cfg.Discovery.Hosts[0].TenantID, RoleTemplates: []string{"service_client"}}
			path := writeRuntimeConfig(t, cfg)
			options := BuildOptions{ConfigPath: path, Role: role, Version: "process-fixture", Commit: "source-sha", SourceState: "clean", Logger: slog.New(slog.DiscardHandler)}
			app, err := Build(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(app.Close)
			read := func(handler http.Handler, path, token string, want int) map[string]json.RawMessage {
				t.Helper()
				r := httptest.NewRequestWithContext(t.Context(), "GET", path, nil)
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
				}
				if path != "/version" && w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("process information cached")
				}
				if want != 200 {
					return nil
				}
				var got map[string]json.RawMessage
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				return got
			}
			check := func(handler http.Handler, path, token string) {
				t.Helper()
				raw := read(handler, path, token, 200)
				data, _ := json.Marshal(raw)
				var snapshot processinfo.Snapshot
				if err := json.Unmarshal(data, &snapshot); err != nil {
					t.Fatal(err)
				}
				if snapshot.InstanceID != app.Process.ID() || !snapshot.StartedAt.Equal(app.Process.Read().StartedAt) || snapshot.UptimeSeconds < 0 || snapshot.ObservedAt.IsZero() || string(raw["version"]) != `"process-fixture"` || string(raw["commit"]) != `"source-sha"` || string(raw["role"]) != strconv.Quote(string(role)) {
					t.Fatalf("incoherent process response: %s", data)
				}
				again := read(handler, path, token, 200)
				if string(again["instanceId"]) != string(raw["instanceId"]) || string(again["startedAt"]) != string(raw["startedAt"]) {
					t.Fatal("read changed process identity")
				}
			}
			if app.HTTP != nil {
				// A reader can inspect process information without an administrator role.
				check(app.HTTP.Handler(), "/api/v1/service-information", "reader")
				// A nonhuman base identity has no product-action grants, but can
				// inspect this authenticated operational information too.
				read(app.HTTP.Handler(), "/api/v1/service-information", "service-client", 200)
				read(app.HTTP.Handler(), "/api/v1/service-information", "", 401)
				read(app.HTTP.Handler(), "/api/v1/service-information", "invalid", 401)
				read(app.HTTP.Handler(), "/api/v1/service-information?limit=1", "worker", 400)
				raw := read(app.HTTP.Handler(), "/version", "", 200)
				if len(raw) != 6 || raw["instanceId"] != nil {
					t.Fatal("static API version expanded")
				}
			}
			if app.Operations != nil { // all is a development combined process; worker surface still names its worker role.
				if role == config.RoleWorker {
					check(app.Operations.Handler(), "/status", "")
				} else {
					raw := read(app.Operations.Handler(), "/status", "", 200)
					if string(raw["instanceId"]) != strconv.Quote(app.Process.ID()) || string(raw["role"]) != `"worker"` {
						t.Fatal("combined runtime worker status lost the shared execution or existing role")
					}
				}
				if raw := read(app.Operations.Handler(), "/version", "", 200); raw["instanceId"] != nil {
					t.Fatal("worker version changed")
				}
			}
			metrics := string(app.Observability.Render(t.Context()))
			start := float64(app.Process.Read().StartedAt.UnixMilli()) / 1000
			if !strings.Contains(metrics, "open_agentic_catalog_process_start_time_seconds "+strconv.FormatFloat(start, 'f', 3, 64)) || strings.Contains(metrics, app.Process.ID()) {
				t.Fatal("process metric differs or includes execution label")
			}
			replacementStarted := time.Now()
			replacement, err := Build(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(replacement.Close)
			if replacement.Process.ID() == app.Process.ID() || replacement.Process.Read().StartedAt.IsZero() || replacement.Process.Read().UptimeSeconds > int64(time.Since(replacementStarted)/time.Second)+1 {
				t.Fatal("replacement retained old execution")
			}
		})
	}
}
