//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunRejectsOccupiedListener(t *testing.T) {
	const helperEnvironment = "CATALOG_UI_TEST_STARTUP_COLLISION"
	if os.Getenv(helperEnvironment) != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunRejectsOccupiedListener$", "-test.timeout=10s") // #nosec G204,G702 -- executes only this test binary.
		command.Env = []string{helperEnvironment + "=1"}
		if directory := flag.Lookup("test.gocoverdir"); directory != nil && directory.Value.String() != "" {
			command.Args = append(command.Args, "-test.gocoverdir="+directory.Value.String())
		}
		command.WaitDelay = time.Second
		output, err := command.CombinedOutput()
		if err != nil || ctx.Err() != nil {
			t.Fatalf("UI startup collision child: %v (deadline: %v)\n%s", err, ctx.Err(), output)
		}
		return
	}
	holder, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	filename, cfg := commandConfig(t)
	cfg.Listen = holder.Addr().String()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(filename); err == nil || err.Error() != "UI listener failed" {
		t.Fatalf("UI startup did not return the sanitized listener failure: %v", err)
	}
	unexpected, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", holder.Addr().String())
	if unexpected != nil {
		_ = unexpected.Close()
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("UI startup failure lost the collision holder: %v", err)
	}
}

func TestExecutableRejectsMissingConfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "catalog-ui")
	arguments := []string{"build", "-ldflags=-X main.version=1.2.3-fixture -X main.commit=abc123", "-o", binary}
	directory := os.Getenv("CATALOG_PROCESS_COVERAGE_DIR")
	if directory != "" {
		var err error
		directory, err = os.MkdirTemp(directory, "catalog-ui-")
		if err != nil {
			t.Fatal(err)
		}
		arguments = append(arguments, "-cover", "-covermode=atomic", "-coverpkg=github.com/open-agentic-catalog/open-agentic-catalog/catalog-ui/cmd/catalog-ui")
	}
	arguments = append(arguments, ".")
	build := exec.CommandContext(ctx, "go", arguments...) // #nosec G204,G702 -- fixed Go build of this package into a test-owned directory.
	build.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build UI test executable: %v\n%s", err, output)
	}
	verifyUILogging(t, ctx, binary, directory)
	verifyUIClientDiagnostics(t, ctx, binary, directory)
	command := exec.CommandContext(ctx, binary, "--config", filepath.Join(t.TempDir(), "missing.json")) // #nosec G204,G702 -- executes only the binary built above.
	command.Env = []string{}
	if directory != "" {
		command.Env = append(command.Env, "GOCOVERDIR="+directory)
	}
	command.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || ctx.Err() != nil {
		t.Fatalf("missing config exit: %v (deadline: %v)", err, ctx.Err())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "UI configuration") || strings.Contains(stderr.String(), "coverage") {
		t.Fatalf("missing config output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	malformedManual, cfg := commandConfig(t)
	manual := filepath.Join(cfg.Assets, "docs")
	if err := os.Mkdir(manual, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manual, "documentation-bundle.json"), []byte(`{"unexpected":"SYNTHETIC_PRIVATE_UI"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args       []string
		success    bool
		diagnostic string
	}{
		{args: []string{"--SYNTHETIC_PRIVATE_UI"}},
		{args: []string{"--probe=SYNTHETIC_PRIVATE_UI"}},
		{args: []string{"--config", filepath.Join(t.TempDir(), "SYNTHETIC_PRIVATE_UI")}},
		{args: []string{"--config", malformedManual}, diagnostic: "UI documentation bundle is invalid"},
		{args: []string{"--help"}, success: true},
	} {
		probe := exec.CommandContext(ctx, binary, tc.args...) // #nosec G204,G702 -- fixed locally built executable, synthetic argument fixtures.
		probe.Env = command.Env
		var out, diagnostic bytes.Buffer
		probe.Stdout = &out
		probe.Stderr = &diagnostic
		probe.WaitDelay = time.Second
		err := probe.Run()
		if tc.success {
			if err != nil || diagnostic.Len() != 0 || !strings.Contains(out.String(), "config") {
				t.Fatalf("UI help: %v %q %q", err, out.String(), diagnostic.String())
			}
		} else {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || out.Len() != 0 || strings.Count(diagnostic.String(), "\n") != 1 || !strings.Contains(diagnostic.String(), tc.diagnostic) {
				t.Fatalf("UI failure: %v %q %q", err, out.String(), diagnostic.String())
			}
		}
		if strings.Contains(out.String()+diagnostic.String(), "SYNTHETIC_PRIVATE_UI") {
			t.Fatal("UI argument/path disclosure")
		}
	}

	if directory != "" {
		for _, pattern := range []string{"covmeta.*", "covcounters.*"} {
			files, err := filepath.Glob(filepath.Join(directory, pattern))
			if err != nil || len(files) == 0 {
				t.Fatalf("UI process did not emit %s: %v", pattern, err)
			}
		}
	}
}

func TestRunHostServesAndDrainsRealListener(t *testing.T) {
	reservation, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reservation.Close() }()
	address := reservation.Addr().String()
	filename, cfg := commandConfig(t)
	cfg.Listen, cfg.Origin = address, "http://"+address
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	configured := make(chan *http.Server, 1)
	finished := make(chan struct{})
	var server *http.Server
	var runErr error
	defer func() {
		cancel()
		if server != nil {
			_ = server.Close()
		}
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("UI host did not stop during cleanup")
		}
	}()
	go func() {
		runErr = runHost(filename, func(server *http.Server, drain func()) error {
			configured <- server
			return serve(ctx, server, drain)
		})
		close(finished)
	}()
	select {
	case server = <-configured:
	case <-finished:
		t.Fatalf("UI host failed before listening: %v", runErr)
	case <-ctx.Done():
		t.Fatal("UI host construction timed out")
	}
	client := &http.Client{Transport: &http.Transport{}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Origin+"/readyz", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
			_ = response.Body.Close()
			var readiness struct {
				Status string `json:"status"`
			}
			if response.StatusCode != http.StatusOK || readErr != nil || json.Unmarshal(body, &readiness) != nil || readiness.Status != "ok" {
				t.Fatalf("UI readiness: status=%d body=%q error=%v", response.StatusCode, body, readErr)
			}
			break
		}
		select {
		case <-finished:
			t.Fatalf("UI host stopped before readiness: %v", runErr)
		case <-ctx.Done():
			t.Fatal("UI listener never became ready")
		case <-poll.C:
		}
	}
	if ctx.Err() != nil {
		t.Fatal("UI readiness outlived its deadline")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("UI host did not finish graceful shutdown")
	}
	if runErr != nil {
		t.Fatalf("UI shutdown: %v", runErr)
	}
	// The real shutdown callback must have marked this host unready.
	recorder := httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, cfg.Origin+"/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("drained UI readiness = %d", recorder.Code)
	}
	rebound, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", address)
	if err != nil {
		t.Fatalf("UI shutdown retained its listener: %v", err)
	}
	_ = rebound.Close()
}
