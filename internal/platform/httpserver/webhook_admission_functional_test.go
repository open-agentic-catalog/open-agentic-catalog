//go:build functional

package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

type blockedTestWebhookSender struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (s *blockedTestWebhookSender) Send(ctx context.Context, _ webhook.SendRequest) (webhook.SendResult, error) {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return webhook.SendResult{}, ctx.Err()
		}
	}
	return webhook.SendResult{Status: http.StatusNoContent}, nil
}

func TestHTTPWebhookTestAdmissionAndReplay(t *testing.T) {
	sender := &blockedTestWebhookSender{entered: make(chan struct{}), release: make(chan struct{})}
	server := newWebhookHTTPServer(t, sender)
	defer server.Close()
	created := requestJSON(t, server.URL, http.MethodPost, "/api/v1/webhooks", "admin", "admission-create", "", map[string]any{"name": "Receiver", "url": "https://hooks.example.com/test", "eventTypes": []string{"*"}})
	requireStatus(t, created, http.StatusCreated)
	var endpoint webhook.CreateResult
	decodeResult(t, created, &endpoint)
	path := "/api/v1/webhooks/" + endpoint.Endpoint.ID + ":test"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+path, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer admin")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "admitted-test")
	req.Header.Set("If-Match", `"rev-1"`)
	done := make(chan struct{})
	var admitted httpResult
	var admittedError error
	go func() {
		defer close(done)
		response, err := server.Client().Do(req)
		if err != nil {
			admittedError = err
			return
		}
		admitted.status, admitted.header = response.StatusCode, response.Header.Clone()
		admitted.body, admittedError = io.ReadAll(response.Body)
		if err := response.Body.Close(); admittedError == nil {
			admittedError = err
		}
	}()
	release := sync.OnceFunc(func() { close(sender.release) })
	// This defer must release the sender before closing the HTTP server,
	// including when an assertion fails.
	defer func() {
		release()
		<-done
		if admittedError != nil {
			t.Errorf("admitted request: %v", admittedError)
		}
	}()
	select {
	case <-sender.entered:
	case <-done:
		t.Fatalf("initial request returned before sending: status=%d err=%v", admitted.status, admittedError)
	}
	busy := requestJSON(t, server.URL, http.MethodPost, path, "admin", "busy-test", `"rev-1"`, map[string]any{})
	requireStatus(t, busy, http.StatusServiceUnavailable)
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(busy.body, &problem); err != nil || problem.Code != "WEBHOOK_TEST_BUSY" {
		t.Fatal("no-send refusal lost its stable code")
	}
	replay := requestJSON(t, server.URL, http.MethodPost, path, "admin", "busy-test", `"rev-1"`, map[string]any{})
	requireStatus(t, replay, http.StatusServiceUnavailable)
	if string(busy.body) != string(replay.body) {
		t.Fatal("busy command replay changed its response")
	}
	requireStatus(t, requestJSON(t, server.URL, http.MethodPost, path, "other-admin", "other-test", `"rev-1"`, map[string]any{}), http.StatusNotFound)
	requireStatus(t, requestJSON(t, server.URL, http.MethodGet, "/readyz", "", "", "", nil), http.StatusOK)
	if sender.calls.Load() != 1 {
		t.Fatal("busy, replayed or foreign requests reached the sender")
	}
	release()
	<-done
	if admittedError != nil {
		t.Fatal(admittedError)
	}
	requireStatus(t, admitted, http.StatusOK)
	// Parsing the full OpenAPI document is assertion work, not concurrent
	// request work. Keep it outside the deliberately blocked sender's deadline.
	requireControlSchema(t, "Problem", busy.body)
	requireControlSchema(t, "WebhookDelivery", admitted.body)
	var firstDelivery webhook.Delivery
	decodeResult(t, admitted, &firstDelivery)
	if firstDelivery.State != webhook.DeliverySucceeded || firstDelivery.EndpointID != endpoint.Endpoint.ID {
		t.Fatal("admitted request did not retain its successful delivery")
	}
	// A completed refusal must not turn into a send when the slot is free.
	afterRelease := requestJSON(t, server.URL, http.MethodPost, path, "admin", "busy-test", `"rev-1"`, map[string]any{})
	requireStatus(t, afterRelease, http.StatusServiceUnavailable)
	if string(afterRelease.body) != string(busy.body) || sender.calls.Load() != 1 {
		t.Fatal("released slot changed the cached no-send refusal")
	}
	fresh := requestJSON(t, server.URL, http.MethodPost, path, "admin", "deliberate-later-test", `"rev-1"`, map[string]any{})
	requireStatus(t, fresh, http.StatusOK)
	requireControlSchema(t, "WebhookDelivery", fresh.body)
	var secondDelivery webhook.Delivery
	decodeResult(t, fresh, &secondDelivery)
	if secondDelivery.ID == firstDelivery.ID || secondDelivery.EventID == firstDelivery.EventID || secondDelivery.EndpointID != endpoint.Endpoint.ID || secondDelivery.State != webhook.DeliverySucceeded || sender.calls.Load() != 2 {
		t.Fatal("fresh explicit key did not create exactly one new delivery")
	}
	freshReplay := requestJSON(t, server.URL, http.MethodPost, path, "admin", "deliberate-later-test", `"rev-1"`, map[string]any{})
	requireStatus(t, freshReplay, http.StatusOK)
	if string(freshReplay.body) != string(fresh.body) || sender.calls.Load() != 2 {
		t.Fatal("successful test replay sent another request")
	}
	history := requestJSON(t, server.URL, http.MethodGet, "/api/v1/webhooks/"+endpoint.Endpoint.ID+"/deliveries?limit=10", "admin", "", "", nil)
	requireStatus(t, history, http.StatusOK)
	requireControlSchema(t, "WebhookDeliveryPage", history.body)
	var page cursorPage[webhook.Delivery]
	decodeResult(t, history, &page)
	seen := make(map[string]bool)
	for _, item := range page.Items {
		seen[item.ID] = true
	}
	if len(page.Items) != 2 || len(seen) != 2 || !seen[firstDelivery.ID] || !seen[secondDelivery.ID] || page.NextCursor != "" {
		t.Fatal("busy refusals or replayed tests manufactured delivery evidence")
	}
}
