//go:build functional

// Functional tests exercise real process, network or application composition boundaries.
package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func TestEmbeddedPublicTrustRootsWithoutSystemBundle(t *testing.T) {
	const helperEnvironment = "CATALOG_TEST_EMBEDDED_ROOTS_HELPER"
	if os.Getenv(helperEnvironment) == "1" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			t.Fatalf("load embedded root pool: %v", err)
		}
		if roots.Equal(x509.NewCertPool()) {
			t.Fatal("embedded root pool is empty")
		}
		return
	}

	missingRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEmbeddedPublicTrustRootsWithoutSystemBundle$") // #nosec G204,G702 -- the executable is this test binary, not external input.
	command.Env = []string{
		helperEnvironment + "=1",
		"SSL_CERT_FILE=" + filepath.Join(missingRoot, "missing.pem"),
		"SSL_CERT_DIR=" + filepath.Join(missingRoot, "missing-directory"),
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("verify embedded roots in an empty trust environment: %v\n%s", err, output)
	}
}

func TestRunArgsDrainsAfterListenerFailure(t *testing.T) {
	const helperEnvironment = "CATALOG_TEST_STARTUP_COLLISION"
	collision := os.Getenv(helperEnvironment)
	if collision == "" {
		runListenerTestChildren(t, helperEnvironment, "api", "operations")
		return
	}
	if collision != "api" && collision != "operations" {
		t.Fatalf("unknown collision fixture %q", collision)
	}
	holder := listenLoopback(t)
	const siblingAddress = "127.0.0.1:0"
	cfg := config.Defaults()
	cfg.HTTP.Address, cfg.Worker.OperationsAddress = holder.Addr().String(), siblingAddress
	if collision == "operations" {
		cfg.HTTP.Address, cfg.Worker.OperationsAddress = siblingAddress, holder.Addr().String()
	}
	filename := writeListenerTestConfig(t, cfg)
	bound := make(chan net.Listener, 1)
	ready := make(chan struct{})
	err := runArgsServing([]string{"all", "--config", filename}, func(server *http.Server) error {
		if server.Addr == holder.Addr().String() {
			// Exercise failure after the sibling has really bound, not merely
			// after its goroutine was scheduled.
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				return errors.New("sibling listener did not start")
			}
		} else {
			server.BaseContext = func(listener net.Listener) context.Context {
				t.Cleanup(func() { _ = listener.Close() })
				bound <- listener
				close(ready)
				return context.Background()
			}
		}
		return serveHTTP(server)
	})
	var bindError *net.OpError
	if !errors.Is(err, syscall.EADDRINUSE) || !errors.As(err, &bindError) || bindError.Op != "listen" || bindError.Addr == nil || bindError.Addr.String() != holder.Addr().String() {
		t.Fatalf("runArgs did not return the owned bind failure: %v", err)
	}
	want := bindError.Error()
	if collision == "operations" {
		want = "worker operations server: " + want
	}
	if err.Error() != want {
		t.Fatalf("startup failure included an unexpected drain error: %v", err)
	}
	// Check the actual owned socket before child exit can hide a leaked listener.
	sibling := <-bound
	tcpSibling, ok := sibling.(*net.TCPListener)
	if !ok {
		t.Fatalf("unexpected listener type: %T", sibling)
	}
	if err := tcpSibling.SetDeadline(time.Now().Add(time.Second)); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	connection, err := sibling.Accept()
	if connection != nil {
		_ = connection.Close()
	}
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("startup failure did not close sibling listener: %v", err)
	}
	unexpected, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", holder.Addr().String())
	if unexpected != nil {
		_ = unexpected.Close()
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("startup failure lost the collision holder: %v", err)
	}
}

// TestRunArgsDrainsActiveConnection checks the client's response and accepted
// socket before child exit can hide an interrupted request or a leaked socket.
func TestRunArgsDrainsActiveConnection(t *testing.T) {
	const helperEnvironment = "CATALOG_TEST_ACTIVE_LISTENER"
	const failedAccept = "operations-accept-failure"
	const gracefulAccept = "operations-accept-failure-completes"
	active := os.Getenv(helperEnvironment)
	if active == "" {
		runListenerTestChildren(t, helperEnvironment, "api", "operations", failedAccept, gracefulAccept)
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	listener := listenLoopback(t)
	address := listener.Addr().String()
	cfg := config.Defaults()
	switch active {
	case "api":
		cfg.HTTP.Address, cfg.Worker.OperationsAddress = address, "127.0.0.1:0"
	case "operations", failedAccept, gracefulAccept:
		cfg.HTTP.Address, cfg.Worker.OperationsAddress = "127.0.0.1:0", address
	default:
		t.Fatalf("unknown active listener %q", active)
	}
	cfg.HTTP.RequestTimeout.Duration = time.Second
	cfg.HTTP.ShutdownTimeout.Duration = 7 * time.Second
	filename := writeListenerTestConfig(t, cfg)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/held", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	type clientResult struct {
		status int
		err    error
	}
	exchange := make(chan clientResult, 1)
	go func() {
		response, err := client.Do(req)
		result := clientResult{err: err}
		if response != nil {
			result.status = response.StatusCode
			_ = response.Body.Close()
		}
		exchange <- result
	}()

	accepted := make(chan net.Conn, 1)
	started, handlerDone := make(chan struct{}), make(chan struct{})
	siblingFailure := errors.New("sibling listener failed")
	acceptFails := active == failedAccept || active == gracefulAccept
	err = runArgsServing([]string{"all", "--config", filename}, func(server *http.Server) error {
		if server.Addr != address {
			if acceptFails {
				return serveHTTP(server)
			}
			select {
			case <-started:
				return siblingFailure
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		server.ConnState = func(conn net.Conn, state http.ConnState) {
			if state == http.StateNew {
				accepted <- conn
			}
		}
		// A nil channel keeps the stalled handler waiting for a real disconnect.
		var finishRequest chan struct{}
		if active == gracefulAccept {
			finishRequest = make(chan struct{})
			server.RegisterOnShutdown(func() { close(finishRequest) })
		}
		server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(handlerDone)
			close(started)
			if acceptFails {
				// Stop accepting new work while this connection is still active.
				if err := listener.Close(); err != nil {
					t.Errorf("close operations listener: %v", err)
				}
			}
			select {
			case <-r.Context().Done():
			case <-finishRequest:
				w.WriteHeader(http.StatusNoContent)
			}
		})
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	wantFailure := siblingFailure
	if acceptFails {
		var acceptError *net.OpError
		if !errors.As(err, &acceptError) || acceptError.Op != "accept" || acceptError.Addr == nil || acceptError.Addr.String() != address {
			t.Fatalf("shutdown lost the accept failure on %s: %v", address, err)
		}
		wantFailure = net.ErrClosed
	}
	wantDeadline := active != gracefulAccept
	if !errors.Is(err, wantFailure) || errors.Is(err, context.DeadlineExceeded) != wantDeadline {
		t.Fatalf("shutdown error = %v, want %v, deadline exceeded = %t", err, wantFailure, wantDeadline)
	}
	// The socket must already be closed even if the handler or client has
	// not yet been scheduled to observe the disconnect.
	select {
	case conn := <-accepted:
		if err := conn.SetReadDeadline(time.Time{}); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("shutdown returned with an open %s connection: %v", active, err)
		}
	default:
		t.Fatal("the held request had no accepted connection")
	}
	select {
	case <-handlerDone:
	case <-ctx.Done():
		t.Fatal("handler did not finish after shutdown")
	}
	select {
	case result := <-exchange:
		if ctx.Err() != nil {
			t.Fatalf("client watchdog expired: %v", ctx.Err())
		}
		if active == gracefulAccept {
			if result.err != nil || result.status != http.StatusNoContent {
				t.Fatalf("accepted request did not complete: status %d, error %v", result.status, result.err)
			}
		} else if result.err == nil {
			t.Fatalf("stalled client received HTTP %d instead of a disconnect", result.status)
		}
	case <-ctx.Done():
		t.Fatal("client remained connected after shutdown")
	}
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

// runListenerTestChildren isolates process-scoped signals and configuration, and
// forwards child coverage to the existing local functional profile.
func runListenerTestChildren(t *testing.T, helperEnvironment string, scenarios ...string) {
	t.Helper()
	testName := t.Name()
	for _, name := range scenarios {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+testName+"$", "-test.timeout=30s") // #nosec G204,G702 -- executes only this test binary.
			// Do not inherit runtime configuration or credentials from the launcher.
			command.Env = []string{helperEnvironment + "=" + name}
			if directory := flag.Lookup("test.gocoverdir"); directory != nil && directory.Value.String() != "" {
				command.Args = append(command.Args, "-test.gocoverdir="+directory.Value.String())
			}
			command.WaitDelay = time.Second
			output, err := command.CombinedOutput()
			if err != nil || ctx.Err() != nil {
				t.Fatalf("listener test child: %v (deadline: %v)\n%s", err, ctx.Err(), output)
			}
		})
	}
}

func writeListenerTestConfig(t *testing.T, cfg config.Config) string {
	t.Helper()
	cfg.Environment = "test"
	cfg.HTTP.PublicBaseURL = "http://fixture.invalid"
	cfg.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{
		"synthetic-admin": {Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleTenantAdmin}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return filename
}
