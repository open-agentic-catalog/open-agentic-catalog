package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPConfigurationDefaultsOverridesAndRefusals(t *testing.T) {
	t.Setenv("MCPGODEBUG", "")
	defaults := Defaults()
	if defaults.MCP.Enabled || defaults.MCP.MaxToolBytes != 256<<10 || defaults.MCP.MaxConcurrency != 4 || defaults.MCP.Validate(false) != nil {
		t.Fatal(defaults.MCP)
	}
	for _, change := range []func(*MCP){
		func(c *MCP) { c.MaxToolBytes = 1023 }, func(c *MCP) { c.MaxToolBytes = 1<<20 + 1 }, func(c *MCP) { c.MaxConcurrency = 0 }, func(c *MCP) { c.MaxConcurrency = 17 },
		func(c *MCP) { c.AllowedOrigins = []string{"null"} }, func(c *MCP) { c.AllowedOrigins = []string{"https://*.example.com"} }, func(c *MCP) { c.AllowedOrigins = []string{"https://u:p@example.com"} }, func(c *MCP) { c.AllowedOrigins = []string{"https://example.com/"} }, func(c *MCP) { c.AllowedOrigins = []string{"https://example.com?"} }, func(c *MCP) { c.AllowedOrigins = []string{"https://example.com", "https://example.com"} }, func(c *MCP) { c.AllowedOrigins = []string{"https://EXAMPLE.com"} }, func(c *MCP) { c.AllowedOrigins = make([]string, 65) },
	} {
		cfg := defaults.MCP
		change(&cfg)
		if cfg.Validate(false) == nil {
			t.Fatalf("invalid settings admitted: %+v", cfg)
		}
	}
	cfg := defaults.MCP
	cfg.AllowedOrigins = []string{"http://localhost:4180"}
	if cfg.Validate(false) != nil || cfg.Validate(true) == nil {
		t.Fatal("production origin boundary")
	}
	cfg.MaxToolBytes = 1 << 20
	cfg.MaxConcurrency = 16
	cfg.AllowedOrigins = []string{"https://console.example.com"}
	if cfg.Validate(true) != nil {
		t.Fatal("valid upper bounds refused")
	}
	t.Setenv("MCPGODEBUG", "allowsessionsinstateless=1")
	if cfg.Validate(false) != nil {
		t.Fatal("disabled MCP must not depend on SDK compatibility env")
	}
	cfg.Enabled = true
	if err := cfg.Validate(false); err == nil || strings.Contains(err.Error(), "allowsessionsinstateless") {
		t.Fatal("unsafe override validation", err)
	}
	t.Setenv("MCPGODEBUG", "")
	t.Setenv("CATALOG_MCP_ENABLED", "true")
	t.Setenv("CATALOG_MCP_MAX_TOOL_BYTES", "4096")
	t.Setenv("CATALOG_MCP_MAX_CONCURRENCY", "2")
	t.Setenv("CATALOG_MCP_ALLOWED_ORIGINS", `["https://console.example.com"]`)
	loaded, err := LoadFor("../../../config/local.json", RoleAPI)
	if err != nil || !loaded.MCP.Enabled || loaded.MCP.MaxToolBytes != 4096 || loaded.MCP.MaxConcurrency != 2 || len(loaded.MCP.AllowedOrigins) != 1 {
		t.Fatalf("overrides: %+v %v", loaded.MCP, err)
	}
	raw, err := json.Marshal(loaded.Report())
	if err != nil || !strings.Contains(string(raw), `"mcp"`) {
		t.Fatal("configuration report omitted MCP", err)
	}
	for _, value := range []string{"null", `[null]`, `[1]`, `["*"]`, `[] []`} {
		t.Setenv("CATALOG_MCP_ALLOWED_ORIGINS", value)
		if _, err := LoadFor("../../../config/local.json", RoleAPI); err == nil {
			t.Fatal("invalid origin override accepted", value)
		}
	}
}

func TestARDMCPHostActivationAndIndependentOverrideGuard(t *testing.T) {
	t.Setenv("CATALOG_MCP_ENABLED", "false")
	t.Setenv("MCPGODEBUG", "")
	t.Setenv("CATALOG_DISCOVERY_HOSTS", `[{"host":"catalog.example.com","tenantId":"00000000-0000-7000-8000-000000000001","mode":"anonymous","publisherDomain":"example.com","mcpEnabled":true}]`)
	for _, role := range []Role{RoleAPI, RoleAll} {
		cfg, err := LoadFor("../../../config/local.json", role)
		if err != nil || cfg.MCP.Enabled || len(cfg.Discovery.Hosts) != 1 || !cfg.Discovery.Hosts[0].MCPEnabled {
			t.Fatal("independent host activation", role, err)
		}
	}
	t.Setenv("MCPGODEBUG", "allowsessionsinstateless=1")
	for _, role := range []Role{RoleAPI, RoleAll} {
		if _, err := LoadFor("../../../config/local.json", role); err == nil || strings.Contains(err.Error(), "allowsessionsinstateless") {
			t.Fatal("ARD-only activation bypassed SDK override guard", role, err)
		}
	}
	t.Setenv("MCPGODEBUG", "")
	for _, value := range []string{
		`[{"host":"catalog.example.com","tenantId":"00000000-0000-7000-8000-000000000001","mode":"disabled","publisherDomain":"example.com","mcpEnabled":true}]`,
		`[{"host":"catalog.example.com","tenantId":"00000000-0000-7000-8000-000000000001","mode":"anonymous","publisherDomain":"example.com","mcpEnabled":null}]`,
		`[{"host":"catalog.example.com","tenantId":"00000000-0000-7000-8000-000000000001","mode":"anonymous","publisherDomain":"example.com","mcpEnabled":"true"}]`,
	} {
		t.Setenv("CATALOG_DISCOVERY_HOSTS", value)
		if _, err := LoadFor("../../../config/local.json", RoleAPI); err == nil {
			t.Fatal("invalid activation admitted", value)
		}
	}
}
