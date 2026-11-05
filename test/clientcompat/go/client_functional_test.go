//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package clientcompat_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
)

func TestExplicitTransportAndCancellation(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	hc := explicitHTTPClient()
	defer hc.CloseIdleConnections()
	transport, ok := hc.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("ambient proxy enabled")
	}
	c, err := client.NewClient(origin.URL, client.WithHTTPClient(boundedDoer{hc, responseLimit}))
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.GetCapabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, response.Body.Close()) }()
	if response.StatusCode != http.StatusTemporaryRedirect || redirected.Load() != 0 {
		t.Fatal("redirect followed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if unexpected, err := c.GetCapabilities(ctx); err == nil {
		must(t, unexpected.Body.Close())
		t.Fatal("cancelled call succeeded")
	}
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
			return
		}
		<-r.Context().Done()
	}))
	defer stalled.Close()
	// Observe transport success before the bounded reader starts. Otherwise a
	// timeout during connection setup could falsely pass the body-deadline test.
	var receivedHeaders bool
	c, err = client.NewClient(stalled.URL, client.WithHTTPClient(boundedDoer{doerFunc(func(r *http.Request) (*http.Response, error) {
		response, err := hc.Do(r)
		receivedHeaders = err == nil
		return response, err
	}), responseLimit}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if unexpected, err := c.GetCapabilities(ctx); err == nil {
		must(t, unexpected.Body.Close())
		t.Fatal("body deadline ignored")
	}
	if !receivedHeaders || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("test did not reach the response body deadline")
	}
}

// A lost response on a reused connection can trigger net/http's transparent
// retry for a replayable POST carrying Idempotency-Key. Replays must instead be
// explicit caller decisions, just like the workflow test's deliberate replay.
func TestMutationConnectionFailureIsNotRetried(t *testing.T) {
	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		mutations.Add(1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
			return
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	hc := explicitHTTPClient()
	defer hc.CloseIdleConnections()
	c, err := client.NewClient(server.URL, client.WithHTTPClient(boundedDoer{hc, responseLimit}))
	must(t, err)
	// Consume the first response so a keep-alive transport can reuse it.
	response, err := c.GetCapabilities(context.Background())
	must(t, err)
	must(t, response.Body.Close())
	response, err = c.CreateNamespace(context.Background(), &client.CreateNamespaceParams{IdempotencyKey: "caller-command"}, client.CreateNamespaceJSONRequestBody{Slug: "example", DisplayName: "Example"})
	if err == nil {
		must(t, response.Body.Close())
		t.Fatal("lost response reported success")
	}
	if mutations.Load() != 1 {
		t.Fatalf("one caller command caused %d HTTP attempts", mutations.Load())
	}
}
