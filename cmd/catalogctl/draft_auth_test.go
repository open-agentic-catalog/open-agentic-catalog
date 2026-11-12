package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Flag and missing-file controls stay isolated: no API, issuer or binary runs.
func TestExportLoginCredentials(t *testing.T) {
	for _, mode := range []string{"native", "draft", "batch", "preview"} {
		t.Run(mode, func(t *testing.T) {
			parse := func(extra []string, env map[string]string) (remoteOptions, error) {
				getenv := func(key string) string { return env[key] }
				args := []string{"--output-dir", "unused", "--base-url", "https://catalog.invalid"}
				if mode == "preview" {
					o, err := parseExportBatch(append(append([]string{"preview", "--query", "search"}, args...), extra...), getenv)
					return o.remote, err
				}
				args = append(args, "--release-id", exportReleaseID)
				if mode == "draft" || mode == "batch" {
					args = append(args, "--format", "draft")
				}
				if mode == "batch" {
					o, err := parseExportBatch(append(append([]string{"batch"}, args...), extra...), getenv)
					return o.remote, err
				}
				o, err := parseExport(append(args, extra...), getenv)
				return o.remote, err
			}
			login := []string{"--login-file", "private-login", "--issuer-ca-file", "private-roots"}
			o, err := parse(login, nil)
			if err != nil || o.loginFile != "private-login" || o.issuerCAFile != "private-roots" || o.token != "" {
				t.Fatal("explicit human credentials were not selected", err)
			}
			for _, env := range []map[string]string{{"CATALOG_REMOTE_TOKEN": "PRIVATE_SENTINEL"}, {"CATALOG_REMOTE_TOKEN_FILE": "PRIVATE_SENTINEL"}} {
				if _, err := parse(login, env); err == nil || !strings.Contains(safeerr.Message(err), "cannot be combined") || strings.Contains(safeerr.Message(err), "PRIVATE_SENTINEL") {
					t.Fatal("inherited bearer conflict did not fail safely", err)
				}
			}
			if _, err := parse(append(login, "--token-file", "other"), nil); err == nil || !strings.Contains(safeerr.Message(err), "cannot be combined") {
				t.Fatal("explicit bearer conflict was accepted", err)
			}
			if _, err := parse(append(login, "--token-file", ""), map[string]string{"CATALOG_REMOTE_TOKEN_FILE": "inherited"}); err == nil || !strings.Contains(safeerr.Message(err), "cannot be combined") {
				t.Fatal("empty flag bypassed inherited conflict", err)
			}
			if _, err := parse([]string{"--issuer-ca-file", "roots"}, nil); err == nil || !strings.Contains(safeerr.Message(err), "requires --login-file") {
				t.Fatal("issuer roots accepted without login", err)
			}
			o, err = parse([]string{"--token-file", "selected"}, map[string]string{"CATALOG_REMOTE_TOKEN": "other"})
			if err != nil || o.tokenFile != "selected" || o.token != "" {
				t.Fatal("token file lost its normal precedence", err)
			}
		})
	}
}

func TestImportLoginCredentials(t *testing.T) {
	t.Setenv("CATALOG_REMOTE_TOKEN", "")
	t.Setenv("CATALOG_REMOTE_TOKEN_FILE", "")
	filename, draft := importDraftTestFile(t, false)
	writeImportDraftTest(t, filename, draft)
	for _, action := range []string{"apply", "resume"} {
		t.Run(action, func(t *testing.T) {
			directory := canonicalTempDir(t)
			if err := os.Chmod(directory, 0700); err != nil { // #nosec G302 -- Private synthetic credential directory requires owner-only access.
				t.Fatal(err)
			}
			args := []string{action, "--file", filename, "--namespace-id", importPlaceholderID, "--state-file", filepath.Join(directory, "private", "state.json"), "--base-url", "https://catalog.invalid", "--login-file", filepath.Join(directory, "missing-login"), "--issuer-ca-file", filepath.Join(directory, "missing-roots")}
			// Apply reaches the existing local credential guard. Resume first requires
			// its receipt, so conflict checks use a missing input and must run earlier.
			if action == "apply" {
				var out bytes.Buffer
				err := runImport(t.Context(), args, &out)
				if safeerr.Message(err) != "auth: no usable login credentials; sign in again" {
					t.Fatal("import did not select the shared login provider", err)
				}
				if _, err := os.Stat(filepath.Join(directory, "private", "state.json")); !os.IsNotExist(err) {
					t.Fatal("unusable credentials created a command receipt")
				}
			}
			for _, env := range []string{"CATALOG_REMOTE_TOKEN", "CATALOG_REMOTE_TOKEN_FILE"} {
				t.Run(env, func(t *testing.T) {
					t.Setenv(env, "PRIVATE_SENTINEL")
					var out bytes.Buffer
					invalid := append([]string(nil), args...)
					invalid[2] = filepath.Join(directory, "missing-draft")
					invalid = append(invalid, "--token-file", "")
					err := runImport(t.Context(), invalid, &out)
					if err == nil || !strings.Contains(safeerr.Message(err), "cannot be combined") || strings.Contains(safeerr.Message(err), "PRIVATE_SENTINEL") || out.Len() != 0 {
						t.Fatal("credential conflict was not refused before input reads", err)
					}
				})
			}
		})
	}
	var out bytes.Buffer
	err := runImport(t.Context(), []string{"apply", "--file", "missing-draft", "--issuer-ca-file", "roots"}, &out)
	if err == nil || !strings.Contains(safeerr.Message(err), "requires --login-file") {
		t.Fatal("import accepted issuer roots without login", err)
	}
}
