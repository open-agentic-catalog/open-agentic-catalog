//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func verifyUILogging(t *testing.T, ctx context.Context, binary, coverage string) {
	t.Helper()
	holder, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	for _, level := range []string{"debug", "info", "warn", "error"} {
		for _, format := range []string{"text", "json"} {
			t.Run("logging/"+level+"/"+format, func(t *testing.T) {
				file, cfg := commandConfig(t)
				cfg.Listen = holder.Addr().String()
				cfg.LogLevel = level
				cfg.LogFormat = format
				raw, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if level == "info" && format == "text" {
					var fields map[string]any
					if err := json.Unmarshal(raw, &fields); err != nil {
						t.Fatal(err)
					}
					delete(fields, "logLevel")
					delete(fields, "logFormat")
					raw, err = json.Marshal(fields)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(file, raw, 0600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(ctx, binary, "--config", file) // #nosec G204,G702 -- fixed test-owned executable and configuration.
				cmd.Env = []string{}
				if coverage != "" {
					cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverage)
				}
				cmd.WaitDelay = time.Second
				var out, diagnostic bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &diagnostic
				err = cmd.Run()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(diagnostic.String(), "process.failed") || json.Valid(bytes.TrimSpace(diagnostic.Bytes())) != (format == "json") {
					t.Fatalf("terminal: %v %s", err, diagnostic.String())
				}
				if level == "info" || level == "debug" {
					if !strings.Contains(out.String(), "process.logging_configured") || !strings.Contains(out.String(), "1.2.3-fixture") || !strings.Contains(out.String(), "abc123") || json.Valid(bytes.TrimSpace(out.Bytes())) != (format == "json") {
						t.Fatalf("startup/build identity: %s", out.String())
					}
				} else if out.Len() != 0 {
					t.Fatalf("filtered startup: %s", out.String())
				}
			})
		}
	}
}

// Exercise the real executable and fixed API transport: a valid self-context
// followed by unsolicited bytes used to escape through net/http's global logger.
func verifyUIClientDiagnostics(t *testing.T, ctx context.Context, binary, coverage string) {
	t.Helper()
	const private = "SYNTHETIC_PRIVATE_UNSOLICITED"
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.URL.Path != "/api/v1/me" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("login did not reach the intended API control")
			http.Error(w, "invalid control", http.StatusBadRequest)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("fixture requires HTTP/1 hijacking")
			return
		}
		connection, buffered, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = connection.Close() }()
		body := `{"tenantId":"private-tenant","membershipId":"private-member","subject":"private-subject","actorType":"human"}`
		_, err = fmt.Fprintf(buffered, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s%s", len(body), body, private)
		if err != nil || buffered.Flush() != nil {
			t.Error("write unsolicited-response fixture")
		}
	}))
	defer upstream.Close()
	for _, format := range []string{"text", "json"} {
		for _, level := range []string{"debug", "info", "warn", "error"} {
			t.Run("client diagnostics/"+format+"/"+level, func(t *testing.T) {
				upstreamCalls.Store(0)
				file, cfg := commandConfig(t)
				reservation, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				cfg.Listen, cfg.Origin = reservation.Addr().String(), "http://"+reservation.Addr().String()
				cfg.Upstream, cfg.LogLevel, cfg.LogFormat = upstream.URL, level, format
				raw, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, raw, 0600); err != nil {
					t.Fatal(err)
				}
				_ = reservation.Close()
				cmd := exec.CommandContext(ctx, binary, "--config", file) // #nosec G204,G702 -- fixed locally built executable and owned configuration.
				cmd.Env = []string{}
				if coverage != "" {
					cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverage)
				}
				output, err := os.CreateTemp(t.TempDir(), "service-output-")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = output.Close() }()
				var diagnostic bytes.Buffer
				cmd.Stdout, cmd.Stderr, cmd.WaitDelay = output, &diagnostic, time.Second
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if cmd.ProcessState == nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				}()
				client := &http.Client{Timeout: time.Second, Transport: &http.Transport{}}
				defer client.CloseIdleConnections()
				deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					r, _ := http.NewRequestWithContext(deadline, http.MethodGet, cfg.Origin+"/readyz", nil)
					response, err := client.Do(r)
					if err == nil {
						_ = response.Body.Close()
						if response.StatusCode == http.StatusOK {
							break
						}
					}
					select {
					case <-deadline.Done():
						t.Fatal("UI process did not become ready")
					case <-ticker.C:
					}
				}
				r, _ := http.NewRequestWithContext(deadline, http.MethodPost, cfg.Origin+"/auth/login", strings.NewReader(`{"alias":"reader"}`))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Origin", cfg.Origin)
				r.Header.Set("X-Catalog-Request", "1")
				response, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil || response.StatusCode != http.StatusOK || len(response.Cookies()) != 1 || !strings.Contains(string(body), "/catalog") {
					t.Fatal("valid self-context did not establish a session")
				}
				session := response.Cookies()[0].Value
				// An expected refusal is DEBUG-only and never reaches the API.
				r, _ = http.NewRequestWithContext(deadline, http.MethodPost, cfg.Origin+"/auth/login?private-query", strings.NewReader(`{"alias":"private-alias"}`))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Origin", cfg.Origin)
				r.Header.Set("X-Catalog-Request", "1")
				response, err = client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				_, readErr = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if readErr != nil || response.StatusCode != http.StatusUnauthorized || upstreamCalls.Load() != 1 {
					t.Fatal("unknown identity did not remain a local refusal")
				}
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Wait(); err != nil {
					t.Fatal("UI process did not stop cleanly")
				}
				raw, err = os.ReadFile(output.Name())
				if err != nil || diagnostic.Len() != 0 || bytes.Contains(raw, []byte(private)) || bytes.Contains(raw, []byte("private-")) || bytes.Contains(raw, []byte("synthetic-token")) || bytes.Contains(raw, []byte(session)) {
					t.Fatal("HTTP client emitted private bytes or used the wrong stream")
				}
				// Exact inventory also proves successful readiness probes stay quiet
				// at DEBUG and clean shutdown has one owner for each lifecycle event.
				want := map[string]string{}
				if level != "error" {
					want["dependency.diagnostic"] = "WARN"
				}
				if level == "debug" || level == "info" {
					for _, event := range []string{"process.logging_configured", "process.draining", "process.stopped"} {
						want[event] = "INFO"
					}
				}
				if level == "debug" {
					want["ui.session_established"], want["ui.request_failed"] = "DEBUG", "DEBUG"
				}
				for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
					if line == "" {
						continue
					}
					if format == "json" {
						var record map[string]any
						if err := json.Unmarshal([]byte(line), &record); err != nil {
							t.Fatal(err)
						}
						event, _ := record["event"].(string)
						severity, ok := want[event]
						if !ok || record["level"] != severity || record["service"] != "catalog-ui" || record["role"] != "ui" {
							t.Fatalf("unexpected, duplicated or misclassified log: %s", line)
						}
						delete(want, event)
					} else {
						found := false
						for event, severity := range want {
							if strings.Contains(" "+line+" ", " event="+event+" ") && strings.Contains(line, "level="+severity) && strings.Contains(line, "service=catalog-ui role=ui") {
								delete(want, event)
								found = true
								break
							}
						}
						if !found {
							t.Fatalf("unexpected, duplicated or misclassified log: %s", line)
						}
					}
				}
				if len(want) != 0 {
					t.Fatalf("missing process diagnostics: %v", want)
				}
			})
		}
	}
}
