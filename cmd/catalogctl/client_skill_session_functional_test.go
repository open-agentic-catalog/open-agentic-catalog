//go:build functional

// Functional tests exercise real process, network or application composition boundaries.
package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestClientSkillSessionFixture is deliberately skipped by default. The caller
// supplies an empty temporary directory and owns stopping the bounded session.
func TestClientSkillSessionFixture(t *testing.T) {
	directory := os.Getenv("CATALOG_CLIENT_SKILL_SESSION_DIR")
	if directory == "" {
		t.Skip("explicit model-client fixture; set CATALOG_CLIENT_SKILL_SESSION_DIR to an empty temporary directory")
	}
	if !filepath.IsAbs(directory) {
		t.Fatal("session directory must be absolute")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("session directory must exist and be empty")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	runtime := buildSmokeTestRuntime(t)
	defer runtime.Close()
	seedServer := httptest.NewServer(runtime.HTTP.Handler())
	defer seedServer.Close()
	if _, err := executeDemo(t.Context(), seedServer.URL, filepath.Join(t.TempDir(), "receipt.json")); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(clientSkillSessionHandler(runtime.HTTP.Handler(), clientSkillPluginRecord(t), clientSkillLargeRecord(t)))
	defer server.Close()
	for name, value := range map[string]string{"origin": server.URL, "reader-token": "local-reader"} {
		if err := root.WriteFile(name, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("fixture ready; origin and synthetic token file in %s; create stop to exit", directory)
	timer := time.NewTimer(15 * time.Minute)
	defer timer.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-timer.C:
			return
		case <-tick.C:
			if _, err := root.Stat("stop"); err == nil {
				return
			}
		}
	}
}
