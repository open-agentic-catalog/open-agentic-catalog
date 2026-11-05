//go:build minikube && functional

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// An aborted drain observation must cancel and join the actual blocked request,
// independently of the overall harness context and later cluster cleanup.
func TestDrainRequestIndependentCancellation(t *testing.T) {
	for _, ui := range []bool{false, true} {
		t.Run(map[bool]string{false: "api", true: "ui"}[ui], func(t *testing.T) {
			testIndependentRequestCancellation(t, ui)
		})
	}
}

func testIndependentRequestCancellation(t *testing.T, ui bool) {
	t.Helper()
	entered, exited := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(exited) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h := &harness{ctx: t.Context()}
	result := make(chan error, 1)
	go func() {
		var err error
		if ui {
			_, err = h.uiCommandContext(ctx, server.Client(), server.URL, "GET", "/browser/_catalog/api/v1/me", "", "", "", "")
		} else {
			_, err = h.requestContext(ctx, "GET", server.URL, "", "", "", nil, nil)
		}
		result <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("request did not enter")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request not joined")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("server request not canceled")
	}
	if h.ctx.Err() != nil {
		t.Fatal("independent abort canceled harness cleanup context")
	}
}
