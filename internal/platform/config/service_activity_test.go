package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestServiceActivityConfiguration(t *testing.T) {
	clearRuntimeEnvironment(t)
	if Defaults().HTTP.ServiceActivityEnabled {
		t.Fatal("activity disclosure must default off")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Defaults()
	cfg.Auth.DevelopmentTokens = map[string]DevelopmentUser{"activity-admin": {TenantID: "00000000-0000-7000-8000-000000000001", Subject: "activity-admin", Roles: []identity.Role{identity.RoleTenantAdmin}}}
	cfg.HTTP.ServiceActivityEnabled = true
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFor(path, RoleAPI)
	if err != nil || !got.HTTP.ServiceActivityEnabled {
		t.Fatalf("JSON activation: %v", err)
	}
	t.Setenv("CATALOG_HTTP_SERVICE_ACTIVITY_ENABLED", "false")
	got, err = LoadFor(path, RoleAPI)
	if err != nil || got.HTTP.ServiceActivityEnabled {
		t.Fatalf("environment must override JSON: %v", err)
	}
	t.Setenv("CATALOG_HTTP_SERVICE_ACTIVITY_ENABLED", "true")
	got, err = LoadFor(path, RoleAPI)
	if err != nil || !got.HTTP.ServiceActivityEnabled || !got.Report().HTTP.ServiceActivityEnabled {
		t.Fatalf("environment/report activation: %v", err)
	}
	for _, bad := range []string{"1", "yes", "TRUE", "null"} {
		t.Setenv("CATALOG_HTTP_SERVICE_ACTIVITY_ENABLED", bad)
		if _, err := LoadFor(path, RoleAPI); err == nil {
			t.Fatalf("accepted noncanonical bool %q", bad)
		}
	}
}
