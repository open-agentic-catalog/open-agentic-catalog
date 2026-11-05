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

func TestServeHTTPPropagatesListenerStartupFailure(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listener address: %v", err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close reserved listener: %v", err)
		}
	})

	server := &http.Server{Addr: listener.Addr().String(), Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	if err := serveHTTP(server); err == nil {
		t.Fatal("listener address collision succeeded")
	}
}

func TestRunArgsDrainsAfterListenerFailure(t *testing.T) {
	const helperEnvironment = "CATALOG_TEST_STARTUP_COLLISION"
	collision := os.Getenv(helperEnvironment)
	if collision == "" {
		for _, name := range []string{"api", "operations"} {
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunArgsDrainsAfterListenerFailure$", "-test.timeout=30s") // #nosec G204,G702 -- executes only this test binary.
				// Do not inherit runtime configuration or credentials from the launcher.
				command.Env = []string{helperEnvironment + "=" + name}
				if directory := flag.Lookup("test.gocoverdir"); directory != nil && directory.Value.String() != "" {
					command.Args = append(command.Args, "-test.gocoverdir="+directory.Value.String())
				}
				command.WaitDelay = time.Second
				output, err := command.CombinedOutput()
				if err != nil || ctx.Err() != nil {
					t.Fatalf("startup collision child: %v (deadline: %v)\n%s", err, ctx.Err(), output)
				}
			})
		}
		return
	}
	if collision != "api" && collision != "operations" {
		t.Fatalf("unknown collision fixture %q", collision)
	}
	listen := func(address string) net.Listener {
		t.Helper()
		listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		return listener
	}
	holder := listen("127.0.0.1:0")
	const siblingAddress = "127.0.0.1:0"
	cfg := config.Defaults()
	cfg.Environment = "test"
	cfg.HTTP.Address, cfg.Worker.OperationsAddress = holder.Addr().String(), siblingAddress
	if collision == "operations" {
		cfg.HTTP.Address, cfg.Worker.OperationsAddress = siblingAddress, holder.Addr().String()
	}
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
	bound := make(chan net.Listener, 1)
	ready := make(chan struct{})
	err = runArgsServing([]string{"all", "--config", filename}, func(server *http.Server) error {
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
