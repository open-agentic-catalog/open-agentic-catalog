//go:build functional

package runtime

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func TestComposedMemoryOverviewKeepsJobStoreReadMethods(t *testing.T) {
	cfg := localRuntimeConfig()
	cfg.Auth.DevelopmentTokens["overview"] = config.DevelopmentUser{Subject: "overview", TenantID: cfg.Discovery.Hosts[0].TenantID, Roles: []identity.Role{identity.RoleTenantAdmin}}
	runtime, err := Build(t.Context(), BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: config.RoleAPI, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	for _, name := range []string{"jobs", "sources"} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/overview/"+name, nil)
		request.Header.Set("Authorization", "Bearer overview")
		response := httptest.NewRecorder()
		runtime.HTTP.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatal(name, response.Code, response.Body.String())
		}
		var value struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || value.Total.Value != 0 {
			t.Fatal(name, value, err)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private overview became cacheable")
		}
	}
}
