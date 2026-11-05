//go:build functional

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStdioPeerReportsActiveRequestDrainTimeout(t *testing.T) {
	hold, release := context.WithCancel(t.Context())
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-hold.Done()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(release)
	client := server.Client()
	client.Timeout = 15 * time.Second
	t.Cleanup(client.CloseIdleConnections)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		response, callErr := client.Do(request)
		if callErr == nil {
			callErr = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				callErr = errors.New("fixture request failed")
			}
		}
		finished <- callErr
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture request did not start")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := stopStdio(ctx, server.Config)
	cancel()
	select {
	case drainErr := <-stopped:
		if !errors.Is(drainErr, context.DeadlineExceeded) {
			t.Error("active-request drain timeout was not reported", drainErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fixture shutdown did not finish")
	}
	release()
	select {
	case callErr := <-finished:
		if callErr != nil {
			t.Fatal(callErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fixture request did not finish")
	}
}
