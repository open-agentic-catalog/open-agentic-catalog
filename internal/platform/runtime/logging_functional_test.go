//go:build functional

package runtime

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// Exercise configuration through the composed authentication, HTTP diagnostics
// and metrics layers. Helper tests alone cannot detect a lost logger at wiring.
func TestConfiguredLoggingReachesHTTPRuntime(t *testing.T) {
	for _, name := range config.EnvironmentVariables() {
		unsetEnvironment(t, name)
	}
	for _, format := range []string{"text", "json"} {
		for _, level := range []string{"debug", "info", "warn", "error"} {
			t.Run(format+"/"+level, func(t *testing.T) {
				cfg := localRuntimeConfig()
				cfg.LogLevel, cfg.LogFormat = level, format
				user := cfg.Auth.DevelopmentTokens["worker"]
				user.Subject = "private-subject"
				cfg.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{"private-token": user}
				var logs bytes.Buffer
				app, err := Build(t.Context(), BuildOptions{Role: config.RoleAPI, ConfigPath: writeRuntimeConfig(t, cfg), LogOutput: &logs})
				if err != nil {
					t.Fatal(err)
				}
				defer app.Close()
				app.HTTP.SetReady(true)
				for _, request := range []struct {
					token, path, body string
					status            int
				}{
					{"private-token", "/api/v1/me", "private-subject", http.StatusOK},
					{"private-invalid", "/api/v1/me", `"code":"UNAUTHENTICATED"`, http.StatusUnauthorized},
					{"private-invalid", "/api/v1/me", `"code":"UNAUTHENTICATED"`, http.StatusUnauthorized},
					{"private-token", "/api/v1/me?query=private-query", `"code":"INVALID_ARGUMENT"`, http.StatusBadRequest},
				} {
					r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, request.path, nil)
					r.Header.Set("Authorization", "Bearer "+request.token)
					w := httptest.NewRecorder()
					app.HTTP.Handler().ServeHTTP(w, r)
					if w.Code != request.status || !strings.Contains(w.Body.String(), request.body) {
						t.Fatalf("expected %d with %s; got %d %s", request.status, request.body, w.Code, w.Body.String())
					}
				}
				metrics := string(app.Observability.Render(t.Context()))
				if !strings.Contains(metrics, "open_agentic_catalog_security_refusals_total{reason=\"unauthenticated\"} 2\n") || !strings.Contains(metrics, "open_agentic_catalog_http_active_requests 0\n") || strings.Contains(metrics+logs.String(), "private-") {
					t.Fatal("configured filtering changed accounting or exposed request data")
				}
				want := map[string]int{"process.logging_configured": 0, "security.refusal_summary": 0, "http.request_failed": 0}
				if level != "error" {
					want["security.refusal_summary"] = 1
				}
				if level == "info" || level == "debug" {
					want["process.logging_configured"] = 1
				}
				if level == "debug" {
					want["http.request_failed"] = 3
				}
				count := 0
				for event, expected := range want {
					if strings.Count(logs.String(), event) != expected {
						t.Fatalf("%s expected %d events: %s", event, expected, logs.String())
					}
					count += expected
				}
				if strings.Count(logs.String(), "\n") != count {
					t.Fatalf("unexpected HTTP diagnostic: %s", logs.String())
				}
				for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
					if line == "" {
						continue
					}
					if format == "json" {
						var record map[string]any
						if err := json.Unmarshal([]byte(line), &record); err != nil {
							t.Fatal(err)
						}
						if record["service"] != cfg.ServiceName || record["role"] != "api" || record["environment"] != cfg.Environment {
							t.Fatalf("HTTP diagnostic lost configured process identity: %s", line)
						}
					} else if !strings.Contains(line, "service="+cfg.ServiceName+" role=api environment="+cfg.Environment) {
						t.Fatalf("HTTP diagnostic lost configured text output: %s", line)
					}
				}
			})
		}
	}
}
