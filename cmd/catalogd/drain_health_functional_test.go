//go:build functional

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// TestHealthDuringSignalDrain holds an accepted API readiness request while
// the worker's operations listener is still open during graceful shutdown.
func TestHealthDuringSignalDrain(t *testing.T) {
	const child = "CATALOG_TEST_SIGNAL_DRAIN"
	if os.Getenv(child) == "" {
		runListenerTestChildren(t, child, "sigterm")
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	api, worker := listenLoopback(t), listenLoopback(t)
	cfg := config.Defaults()
	cfg.HTTP.Address, cfg.Worker.OperationsAddress = api.Addr().String(), worker.Addr().String()
	filename := writeListenerTestConfig(t, cfg)

	holdNext, accepted := make(chan struct{}), make(chan struct{})
	draining, release := make(chan struct{}), make(chan struct{})
	serve := func(server *http.Server) error {
		listener := worker
		if server.Addr == cfg.HTTP.Address {
			listener = api
			handler := server.Handler
			server.RegisterOnShutdown(func() { close(draining) })
			server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-holdNext:
					close(accepted)
					select {
					case <-release:
					case <-ctx.Done():
						return
					}
				default:
				}
				handler.ServeHTTP(w, r)
			})
		}
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
	stopped := make(chan struct{})
	var runErr error
	go func() {
		defer close(stopped)
		runErr = runArgsServing([]string{"all", "--config", filename}, serve)
	}()
	t.Cleanup(func() {
		// Release both Serve calls even if an assertion failed before SIGTERM.
		_ = api.Close()
		_ = worker.Close()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("catalogd did not stop after fixture cleanup")
		}
	})

	client := &http.Client{
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	for _, listener := range []net.Listener{api, worker} {
		got := readHealth(ctx, client, listener, "/readyz")
		for listener == worker && got.httpCode == http.StatusServiceUnavailable && got.fields["state"] == "starting" && ctx.Err() == nil {
			time.Sleep(5 * time.Millisecond)
			got = readHealth(ctx, client, worker, "/readyz")
		}
		if got.err != nil || got.httpCode != http.StatusOK || got.fields["status"] != "ready" {
			t.Fatalf("initial readiness on %s: %+v", listener.Addr(), got)
		}
	}
	close(holdNext)
	apiResult := make(chan healthResult, 1)
	go func() { apiResult <- readHealth(ctx, client, api, "/readyz") }()
	await := func(event <-chan struct{}, description string) {
		t.Helper()
		select {
		case <-event:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", description)
		}
	}
	await(accepted, "API to accept the readiness request")
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = self.Release() }()
	if err := self.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	await(draining, "API shutdown to start")
	ready := readHealth(ctx, client, worker, "/readyz")
	if ready.err != nil || ready.httpCode != http.StatusServiceUnavailable || ready.fields["role"] != "worker" ||
		ready.fields["state"] != "draining" || ready.fields["status"] != "not_ready" || ready.fields["ready"] != false {
		t.Errorf("worker readiness during drain: %+v", ready)
	}
	live := readHealth(ctx, client, worker, "/livez")
	if live.err != nil || live.httpCode != http.StatusOK || live.fields["role"] != "worker" ||
		live.fields["state"] != "draining" || live.fields["status"] != "live" {
		t.Errorf("worker liveness during drain: %+v", live)
	}
	close(release)
	got := <-apiResult
	if got.err != nil || got.httpCode != http.StatusServiceUnavailable || got.fields["code"] != "DEPENDENCY_UNAVAILABLE" {
		t.Errorf("accepted API readiness request during drain: %+v", got)
	}
	await(stopped, "catalogd to finish shutdown")
	if runErr != nil {
		t.Fatalf("SIGTERM shutdown: %v", runErr)
	}
}

type healthResult struct {
	httpCode int
	fields   map[string]any
	err      error
}

func readHealth(ctx context.Context, client *http.Client, listener net.Listener, path string) healthResult {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listener.Addr().String()+path, nil)
	if err != nil {
		return healthResult{err: err}
	}
	response, err := client.Do(request)
	if err != nil {
		return healthResult{err: err}
	}
	defer func() { _ = response.Body.Close() }()
	result := healthResult{httpCode: response.StatusCode}
	result.err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result.fields)
	return result
}
