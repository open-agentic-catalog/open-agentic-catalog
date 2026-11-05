package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWebhookMaintenanceKeyFilesAndCommandValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	key := bytes.Repeat([]byte{7}, 32)
	for _, value := range []string{"", "invalid", base64.StdEncoding.EncodeToString(key[:31]), base64.StdEncoding.EncodeToString(append(append([]byte(nil), key...), 8)), base64.StdEncoding.EncodeToString(key) + "!", strings.Repeat("x", 1025)} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		loaded, err := readWebhookMaintenanceKey(path)
		want := "webhook maintenance key file must contain base64 for exactly 32 bytes"
		if len(value) > 1024 {
			want = "webhook maintenance key must be a regular file of at most 1024 bytes"
		}
		if err == nil || err.Error() != want || loaded != nil {
			t.Fatalf("invalid key returned data or wrong error: %v", err)
		}
	}
	if _, err := readWebhookMaintenanceKey(filepath.Dir(path)); err == nil {
		t.Fatal("directory accepted as key file")
	}
	if _, err := readWebhookMaintenanceKey(path + "-missing"); err == nil {
		t.Fatal("missing key file accepted")
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	exactLimit := strings.Repeat(" ", 1024-len(encoded)-1) + encoded + "\n"
	if err := os.WriteFile(path, []byte(exactLimit), 0600); err != nil {
		t.Fatal(err)
	}
	if loaded, err := readWebhookMaintenanceKey(path); err != nil || !bytes.Equal(loaded, key) {
		t.Fatal("valid exact-limit key file rejected")
	}
	if err := os.WriteFile(path, []byte(exactLimit+" "), 0600); err != nil {
		t.Fatal(err)
	}
	if loaded, err := readWebhookMaintenanceKey(path); err == nil || loaded != nil {
		t.Fatal("otherwise valid over-limit key accepted")
	}
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"repair", "webhook-secrets", "--runtimes-stopped", "--tenant", "00000000-0000-7000-8000-000000000001", "--reason", "Replace encryption key", "--old-key-file", path, "--new-key-file", path}
	for _, args := range [][]string{{"repair", "webhook-secrets"}, append(append([]string{}, base...), "--limit", "0"), append(append([]string{}, base...), "--after", "invalid"), append(append([]string{}, base...), "--runtimes-stopped=false"), append(append([]string{}, base...), "unexpected")} {
		if err := runArgs(args); err == nil || !strings.Contains(err.Error(), "usage: catalogctl repair webhook-secrets") {
			t.Fatalf("arguments accepted: %v", err)
		}
	}
	if err := runArgs(append(append([]string{}, base...), "--config", path+"-missing")); err == nil {
		t.Fatal("missing configuration accepted")
	}
	if err := runArgs(append(append([]string{}, base...), "--old-key-file", path+"-missing", "--config", path+"-missing-config")); err == nil || err.Error() != "webhook maintenance key file could not be opened" {
		t.Fatalf("old-key preflight reached configuration or exposed a path: %v", err)
	}
	if err := runArgs(append(append([]string{}, base...), "--new-key-file", path+"-missing", "--config", path+"-missing-config")); err == nil || err.Error() != "webhook maintenance key file could not be opened" {
		t.Fatalf("new-key preflight reached configuration or exposed a path: %v", err)
	}
	if err := runArgs(append(append([]string{}, base...), "--unknown")); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if err := runArgs(base); err == nil || !strings.Contains(err.Error(), "database.driver=postgres") {
		t.Fatalf("memory maintenance accepted: %v", err)
	}
}

// Mounted regular secrets remain supported, but a FIFO must be rejected without
// waiting for another process to open its writer end.
func TestWebhookMaintenanceMountedFilesRejectFIFO(t *testing.T) {
	directory := t.TempDir()
	mount := filepath.Join(directory, "mounted-key")
	target := filepath.Join(directory, "current-key")
	if err := os.Symlink(target, mount); err != nil {
		t.Fatal(err)
	}
	for _, value := range []byte{7, 9} {
		want := bytes.Repeat([]byte{value}, 32)
		replacement := filepath.Join(directory, "replacement")
		if err := os.WriteFile(replacement, []byte(base64.StdEncoding.EncodeToString(want)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, target); err != nil {
			t.Fatal(err)
		}
		got, err := readWebhookMaintenanceKey(mount)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("mounted key not reread: %v", err)
		}
		clear(got)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(target, 0600); err != nil {
		t.Fatal(err)
	}
	// No writer is opened: a blocking open would never reach the file-mode guard.
	got, err := readWebhookMaintenanceKey(mount)
	if err == nil || err.Error() != "webhook maintenance key must be a regular file of at most 1024 bytes" || got != nil {
		t.Fatalf("FIFO was not rejected safely: %v", err)
	}
	args := []string{"repair", "webhook-secrets", "--runtimes-stopped", "--tenant", "00000000-0000-7000-8000-000000000001", "--reason", "Replace encryption key", "--old-key-file", mount, "--new-key-file", mount, "--config", filepath.Join(directory, "absent-config")}
	var commandErr error
	output := captureCatalogctlStdout(t, func() error { commandErr = runArgs(args); return nil })
	if commandErr == nil || commandErr.Error() != err.Error() || len(output) != 0 {
		t.Fatalf("FIFO command reached configuration or wrote output: %v", commandErr)
	}
}
