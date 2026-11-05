//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestCommandExecutablesKeepPrivateFailuresOffStderr(t *testing.T) {
	const sentinel = "SYNTHETIC_PRIVATE_DIAGNOSTIC"
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	for _, name := range []string{"catalogd", "catalogctl"} {
		t.Run(name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), name)
			build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../"+name) // #nosec G204,G702 -- fixed repository command, test-owned executable.
			if raw, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s", err, raw)
			}
			base := []string{"worker"}
			help := []string{"worker", "--help"}
			versionArgs := []string{"version"}
			if name == "catalogctl" {
				base = []string{"migrate", "plan"}
				help = []string{"migrate", "plan", "--help"}
			}
			missing := filepath.Join(t.TempDir(), sentinel)
			invalid := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(invalid, []byte(`{"`+sentinel+`":"`+sentinel+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
			cases := []struct {
				name      string
				args, env []string
				want      string
				success   bool
			}{
				{name: "quoted DSN", args: base, env: []string{"CATALOG_DATABASE_DRIVER=postgres", "CATALOG_DATABASE_DSN=user=fixture password='prefix " + sentinel}, want: "PostgreSQL connection configuration"},
				{name: "URI DSN", args: base, env: []string{"CATALOG_DATABASE_DRIVER=postgres", "CATALOG_DATABASE_DSN=postgres://user:" + sentinel + "@127.0.0.1:invalid/db?application_name=" + sentinel}, want: "PostgreSQL connection configuration"},
				{name: "keyword option", args: base, env: []string{"CATALOG_DATABASE_DRIVER=postgres", "CATALOG_DATABASE_DSN=host=127.0.0.1 port=" + sentinel + " password=" + sentinel}, want: "PostgreSQL connection configuration"},
				{name: "unknown flag", args: append(append([]string{}, base...), "--"+sentinel), want: "invalid flags"},
				{name: "missing private path", args: append(append([]string{}, base...), "--config", missing), want: "configuration file"},
				{name: "unknown JSON field", args: append(append([]string{}, base...), "--config", invalid), want: "configuration JSON"},
				{name: "invalid override", args: base, env: []string{"CATALOG_HTTP_REQUEST_TIMEOUT=" + sentinel}, want: "CATALOG_HTTP_REQUEST_TIMEOUT"},
				{name: "invalid log level", args: base, env: []string{"CATALOG_LOG_LEVEL=" + sentinel, "CATALOG_LOG_FORMAT=json"}, want: "logLevel"},
				{name: "help", args: help, env: []string{"CATALOG_LOG_LEVEL=" + sentinel, "CATALOG_LOG_FORMAT=" + sentinel}, want: "config", success: true},
				{name: "version", args: versionArgs, env: []string{"CATALOG_LOG_LEVEL=" + sentinel}, want: "dev", success: true},
			}
			if name == "catalogd" {
				cfg := config.Defaults()
				cfg.Auth.Mode = "oidc"
				cfg.Auth.Issuer = "https://issuer.invalid"
				cfg.Auth.Audience = "catalog"
				cfg.Auth.TenancyMode = "dedicated"
				cfg.Auth.DedicatedTenantID = "00000000-0000-7000-8000-000000000001"
				cfg.Auth.TrustBundleFile = filepath.Join(t.TempDir(), sentinel)
				raw, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				filename := filepath.Join(t.TempDir(), "oidc.json")
				if err := os.WriteFile(filename, raw, 0600); err != nil {
					t.Fatal(err)
				}
				cases = append(cases, struct {
					name      string
					args, env []string
					want      string
					success   bool
				}{name: "missing issuer trust", args: []string{"all", "--config", filename}, want: "OIDC trust bundle must be a readable regular file"})
			}

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					command := exec.CommandContext(ctx, binary, tc.args...) // #nosec G204,G702 -- executes the fixed command built above with synthetic test arguments.
					command.Env = tc.env
					command.WaitDelay = time.Second
					var stdout, stderr bytes.Buffer
					command.Stdout = &stdout
					command.Stderr = &stderr
					err := command.Run()
					var exit *exec.ExitError
					if tc.success {
						if err != nil || stderr.Len() != 0 || !strings.Contains(stdout.String(), tc.want) {
							t.Fatalf("healthy command: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
						}
					} else if !errors.As(err, &exit) || exit.ExitCode() != 1 || stdout.Len() != 0 || strings.Count(stderr.String(), "\n") != 1 || !strings.Contains(stderr.String(), tc.want) {
						t.Fatalf("failed command: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
					}
					if tc.name == "invalid log level" && name == "catalogd" && !strings.HasPrefix(stderr.String(), "time=") {
						t.Fatalf("untrusted configuration selected terminal JSON: %s", stderr.String())
					}
					if ctx.Err() != nil || strings.Contains(stdout.String()+stderr.String(), sentinel) {
						t.Fatal("command leaked private data or exceeded deadline")
					}
				})
			}
			verifyExecutableLogging(t, ctx, binary, name)
			if name == "catalogctl" {
				t.Run("remote help has one output owner", func(t *testing.T) {
					var expected string
					for _, action := range []string{"", "capabilities", "search", "record", "job", "reindex"} {
						arguments := []string{"remote"}
						if action != "" {
							arguments = append(arguments, action)
						}
						arguments = append(arguments, "--help")
						command := exec.CommandContext(ctx, binary, arguments...) // #nosec G204,G702 -- fixed repository executable and allowlisted help commands.
						command.Env = []string{"CATALOG_REMOTE_BASE_URL=https://" + sentinel, "CATALOG_REMOTE_TOKEN=" + sentinel, "CATALOG_REMOTE_TOKEN_FILE=" + sentinel, "CATALOG_REMOTE_CA_FILE=" + sentinel}
						command.WaitDelay = time.Second
						var stdout, stderr bytes.Buffer
						command.Stdout, command.Stderr = &stdout, &stderr
						if err := command.Run(); err != nil || stderr.Len() != 0 || !strings.HasPrefix(stdout.String(), "usage: catalogctl remote ") || strings.Contains(stdout.String(), sentinel) {
							t.Fatalf("remote %s help: %v stdout=%q stderr=%q", action, err, stdout.String(), stderr.String())
						}
						if expected == "" {
							expected = stdout.String()
						} else if stdout.String() != expected {
							t.Fatalf("remote %s emitted extra or different help", action)
						}
					}
				})
			}
		})
	}
}

func TestProcessPanicDrainsAndExitsUnsuccessfully(t *testing.T) {
	if os.Getenv("CATALOG_TEST_PANIC_CHILD") == "1" {
		started := make(chan struct{})
		var address string
		err := runArgsServing([]string{"all", "--config", os.Getenv("CATALOG_TEST_PANIC_CONFIG")}, func(server *http.Server) error {
			if server.MaxHeaderBytes == 8<<10 {
				<-started
				panic("SYNTHETIC_PRIVATE_PANIC")
			}
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				return err
			}
			address = listener.Addr().String()
			close(started)
			err = server.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		})
		if !errors.Is(err, safeerr.ErrPanic) {
			t.Fatalf("lost panic: %v", err)
		}
		if connection, dialErr := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", address); dialErr == nil {
			_ = connection.Close()
			t.Fatal("sibling listener survived drain")
		}
		diagnostics.Report(os.Stderr, "catalogd", "INTERNAL", err, true)
		os.Exit(1)
	}
	// Isolate runtime inputs from any developer environment.
	cfg := config.Defaults()
	cfg.Environment = "test"
	cfg.HTTP.Address = "127.0.0.1:0"
	cfg.Worker.OperationsAddress = "127.0.0.2:0"
	cfg.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{"synthetic-admin": {Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleTenantAdmin}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessPanicDrainsAndExitsUnsuccessfully$") // #nosec G204,G702 -- only this test binary and fixed child selector.
			command.Env = []string{"CATALOG_TEST_PANIC_CHILD=1", "CATALOG_TEST_PANIC_CONFIG=" + filename, "CATALOG_LOG_FORMAT=" + format}
			if directory := flag.Lookup("test.gocoverdir"); directory != nil && directory.Value.String() != "" {
				command.Args = append(command.Args, "-test.gocoverdir="+directory.Value.String())
				command.Env = append(command.Env, "GOCOVERDIR="+directory.Value.String())
			}
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			command.WaitDelay = time.Second
			err := command.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || ctx.Err() != nil {
				t.Fatalf("panic child exit=%v deadline=%v", err, ctx.Err())
			}
			if !strings.Contains(stderr.String(), "INTERNAL_PANIC") || strings.Count(stderr.String(), "\n") != 1 || strings.Contains(stdout.String()+stderr.String(), "SYNTHETIC_PRIVATE_PANIC") || strings.Contains(stderr.String(), "goroutine") {
				t.Fatalf("panic output: %q %q", stdout.String(), stderr.String())
			}
			if json.Valid(bytes.TrimSpace(stderr.Bytes())) != (format == "json") {
				t.Fatalf("panic lost selected output format: %s", stderr.String())
			}
		})
	}
}
