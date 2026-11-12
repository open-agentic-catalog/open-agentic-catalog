package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestAuthLocalCommandsAndSafeErrors(t *testing.T) {
	getenv := func(string) string { return "" }
	for _, args := range [][]string{nil, {"help"}, {"login", "--help"}, {"status", "--help"}} {
		var out, diag bytes.Buffer
		if err := runAuth(t.Context(), args, getenv, &out, &diag); err != nil || !strings.Contains(out.String(), "S256") && !strings.Contains(out.String(), "public") || diag.Len() != 0 {
			t.Fatal("auth help failed")
		}
	}
	dir := canonicalTempDir(t)
	if err := os.Chmod(dir, 0700); err != nil { // #nosec G302 -- Private test directory requires owner traversal.
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials")
	for _, command := range []string{"status", "logout"} {
		var out, diag bytes.Buffer
		if err := runAuth(t.Context(), []string{command, "--file", path}, getenv, &out, &diag); err != nil || out.String() != "{\"loggedIn\":false,\"expired\":false,\"canRefresh\":false}\n" || diag.Len() != 0 {
			t.Fatalf("%s local output invalid: %v", command, err)
		}
	}
	for _, args := range [][]string{{"private-unknown"}, {"login", "--token", "private-value"}, {"status"}, {"status", "--file", path, "--timeout", "0s"}, {"logout", "--file", path, "extra"}} {
		var out, diag bytes.Buffer
		err := runAuth(t.Context(), args, getenv, &out, &diag)
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe auth failure")
		}
	}
}

type authFailedOutput struct{}

func (authFailedOutput) Write([]byte) (int, error) {
	return 0, errors.New("private output failure")
}

func TestAuthCommandsRejectUnsafeInputsAndOutputWithoutNetwork(t *testing.T) {
	getenv := func(string) string { return "" }
	dir := canonicalTempDir(t)
	if err := os.Chmod(dir, 0700); err != nil { // #nosec G302 -- Owned private credential fixture.
		t.Fatal(err)
	}
	path := filepath.Join(dir, "login")
	var output, diagnostics bytes.Buffer
	err := runAuth(t.Context(), []string{"login", "--file", path, "--issuer", "https://issuer.invalid", "--client-id", "cli", "--base-url", "https://api.invalid", "--listen", "0.0.0.0:0", "--scope", "catalog.read", "--scope", "offline_access"}, getenv, &output, &diagnostics)
	if err == nil || !strings.Contains(safeerr.Message(err), "--listen must be") || output.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatalf("unsafe callback was not refused before login: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid callback created credential material")
	}
	for _, command := range []string{"status", "logout"} {
		err := runAuth(t.Context(), []string{command, "--file", filepath.Join(dir, "missing-private-parent", "login")}, getenv, &output, &diagnostics)
		if err == nil || strings.Contains(safeerr.Message(err), "missing-private-parent") || output.Len() != 0 || diagnostics.Len() != 0 {
			t.Fatalf("%s exposed a private path or produced successful output", command)
		}
	}
	if err := runAuth(t.Context(), []string{"status", "--file", path}, getenv, authFailedOutput{}, io.Discard); safeerr.Message(err) != "auth: cannot write local credential status" {
		t.Fatalf("writer failure lost safe classification: %v", err)
	}
}

func TestRemoteLoginFilePrecedence(t *testing.T) {
	for _, env := range []map[string]string{{}, {"CATALOG_REMOTE_TOKEN": "secret"}, {"CATALOG_REMOTE_TOKEN_FILE": "token-file"}} {
		getenv := func(k string) string { return env[k] }
		o, err := parseRemote([]string{"capabilities", "--login-file", "login", "--issuer-ca-file", "roots"}, getenv)
		if len(env) == 0 {
			if err != nil || o.loginFile != "login" || o.issuerCAFile != "roots" {
				t.Fatal("explicit credentials rejected")
			}
		} else if err == nil {
			t.Fatal("inherited bearer conflict accepted")
		}
		if _, err = parseRemote([]string{"capabilities", "--login-file", "login", "--token-file", "token-file"}, getenv); err == nil {
			t.Fatal("explicit bearer conflict accepted")
		}
	}
	if _, err := parseRemote([]string{"capabilities", "--issuer-ca-file", "roots"}, func(string) string { return "" }); err == nil {
		t.Fatal("issuer trust without login accepted")
	}
	if _, err := parseRemote([]string{"capabilities", "--login-file", "login", "--token-file", ""}, func(k string) string {
		if k == "CATALOG_REMOTE_TOKEN_FILE" {
			return "inherited"
		}
		return ""
	}); err == nil {
		t.Fatal("empty flag bypassed inherited bearer rejection")
	}
}
