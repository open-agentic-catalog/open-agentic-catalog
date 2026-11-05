package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndependentLoggingConfiguration(t *testing.T) {
	c := fixtureConfig(t, "http://127.0.0.1:8080")
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "logLevel")
	delete(fields, "logFormat")
	path := filepath.Join(t.TempDir(), "ui.json")
	load := func() (Config, error) {
		t.Helper()
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return LoadConfig(path)
	}
	// Backend logging inputs cannot reconfigure the independent UI host.
	t.Setenv("CATALOG_LOG_LEVEL", "error")
	t.Setenv("CATALOG_LOG_FORMAT", "json")
	got, err := load()
	if err != nil || got.LogLevel != "info" || got.LogFormat != "text" {
		t.Fatalf("omission: %+v %v", got, err)
	}
	for _, level := range []string{"debug", "info", "warn", "error"} {
		for _, format := range []string{"text", "json"} {
			fields["logLevel"] = level
			fields["logFormat"] = format
			got, err := load()
			if err != nil || got.LogLevel != level || got.LogFormat != format {
				t.Fatalf("valid settings: %v", err)
			}
		}
	}
	for _, name := range []string{"logLevel", "logFormat"} {
		for _, value := range []any{nil, "", false, 3, "SYNTHETIC_PRIVATE_SETTING"} {
			fields["logLevel"] = "info"
			fields["logFormat"] = "text"
			fields[name] = value
			_, err := load()
			if err == nil || strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_SETTING") {
				t.Fatalf("invalid %s %v: %v", name, value, err)
			}
		}
	}
}
