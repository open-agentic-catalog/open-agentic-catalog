//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuthActualCommandStatusLogoutAndNoMutationReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil { // #nosec G302 -- Private test directory requires owner traversal.
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "catalogctl")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, ".") // #nosec G204,G702 -- Fixed package, owned temporary executable.
	if err := build.Run(); err != nil {
		t.Fatal("cannot build test CLI")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer private-access-token" || r.Header.Get("Idempotency-Key") != "login-reindex-once" {
			t.Error("CLI command binding changed")
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"UNAUTHENTICATED","detail":"Refused"}`))
	}))
	defer server.Close()
	path := filepath.Join(dir, "login.json")
	value := map[string]any{"format": 1, "issuer": "https://issuer.invalid/realm", "clientId": "cli", "apiOrigin": server.URL, "subject": "human", "tenantId": "00000000-0000-7000-8000-000000000001", "membershipId": "00000000-0000-7000-8000-000000000002", "nonce": "nonce", "authTime": 0, "accessToken": "private-access-token", "refreshToken": "private-refresh-token", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string, error) {
		cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204,G702 -- Owned executable and local synthetic fixture inputs.
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}, "CATALOG_ENV_FILE=/not-used")
		var out, diag bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &diag
		err := cmd.Run()
		if strings.Contains(out.String()+diag.String(), "private-access-token") || strings.Contains(out.String()+diag.String(), "private-refresh-token") {
			t.Fatal("CLI exposed credentials")
		}
		return out.String(), diag.String(), err
	}
	if out, diag, err := run("auth", "status", "--file", path); err != nil || !strings.Contains(out, `"loggedIn":true`) || diag != "" {
		t.Fatal("actual status failed")
	}
	if out, diag, err := run("remote", "reindex", "--base-url", server.URL, "--login-file", path, "--reason", "Explicit qualification", "--idempotency-key", "login-reindex-once"); err == nil || out != "" || !strings.Contains(diag, "UNAUTHENTICATED") || calls.Load() != 1 {
		t.Fatal("401 command failure was not preserved exactly once")
	}
	if out, diag, err := run("auth", "logout", "--file", path); err != nil || !strings.Contains(out, `"loggedIn":false`) || diag != "" {
		t.Fatal("actual logout failed")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("logout retained credentials")
	}
	if out, _, err := run("remote", "capabilities", "--base-url", server.URL, "--login-file", path); err == nil || out != "" || calls.Load() != 1 {
		t.Fatal("logged-out command contacted API")
	}
}
