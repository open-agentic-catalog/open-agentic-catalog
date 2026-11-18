package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionAdoptionIsExplicitAndDefaultOff(t *testing.T) {
	clearRuntimeEnvironment(t)
	cfg, err := LoadFor(filepath.Join("..", "..", "..", "config", "local.json"), RoleAll)
	if err != nil || cfg.Federation.VersionAdoptionEnabled {
		t.Fatal("Version adoption must default off", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, adoptionFlagConfig(t, `{"versionAdoptionEnabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadFor(path, RoleAll)
	if err != nil || !cfg.Federation.VersionAdoptionEnabled {
		t.Fatal("explicit JSON enablement", err)
	}
	t.Setenv("CATALOG_FEDERATION_VERSION_ADOPTION_ENABLED", "false")
	cfg, err = LoadFor(path, RoleAll)
	if err != nil || cfg.Federation.VersionAdoptionEnabled {
		t.Fatal("environment must override JSON", err)
	}
	t.Setenv("CATALOG_FEDERATION_VERSION_ADOPTION_ENABLED", "invalid")
	if _, err = LoadFor(path, RoleAll); err == nil {
		t.Fatal("invalid feature flag accepted")
	}
}

func TestVersionAdoptionRejectsMalformedConfiguration(t *testing.T) {
	clearRuntimeEnvironment(t)
	for _, raw := range []string{`{"federation":{"versionAdoptionEnabled":null}}`, `{"federation":{"versionAdoptionEnabled":"true"}}`, `{"federation":{"versionAdoptionEnabled":true,"versionAdoptionEnabled":false}}`, `{"federation":{"VersionAdoptionEnabled":true}}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, adoptionFlagConfig(t, raw[len(`{"federation":`):len(raw)-1]), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFor(path, RoleAll); err == nil {
			t.Fatal("malformed flag accepted", raw)
		}
	}
}

func adoptionFlagConfig(t *testing.T, federation string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "local.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]json.RawMessage
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["federation"] = json.RawMessage(federation)
	raw, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
