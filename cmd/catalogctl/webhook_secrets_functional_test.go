//go:build functional

package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pgx parser loads OS user defaults through libc, which may contact NSS.
func TestWebhookMaintenanceRejectsInvalidDatabaseConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"repair", "webhook-secrets", "--runtimes-stopped", "--tenant", "00000000-0000-7000-8000-000000000001", "--reason", "Replace encryption key", "--old-key-file", path, "--new-key-file", path}
	configuration := filepath.Join(t.TempDir(), "invalid-dsn.json")
	if err := os.WriteFile(configuration, []byte(`{"database":{"driver":"postgres","dsn":"postgres://%"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runArgs(append(append([]string{}, base...), "--config", configuration)); err == nil || !strings.Contains(err.Error(), "parse PostgreSQL configuration") {
		t.Fatalf("invalid database configuration did not fail parsing: %v", err)
	}
}
