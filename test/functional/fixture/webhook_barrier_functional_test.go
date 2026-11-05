//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Exercise the actual independent control/read/send connections. Socket loss
// must end the held request, and release must remain usable after its sender dies.
func TestWebhookBarrierOverHTTP(t *testing.T) {
	r := configuredBarrierReceiver()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/barrier", r.barrierControl)
	mux.HandleFunc("GET /webhooks/barrier", r.barrierReport)
	mux.HandleFunc("POST /webhooks/events", r.receive)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	exchange := func(req *http.Request, want int) []byte {
		t.Helper()
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		req.RequestURI = ""
		response, err := client.Do(req) // #nosec G704 -- fixed invocation-owned loopback fixture.
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != want {
			t.Fatalf("exchange: status=%d error=%v", response.StatusCode, err)
		}
		return body
	}
	control := func(action string) {
		exchange(httptest.NewRequestWithContext(t.Context(), "POST", "/webhooks/barrier", bytes.NewReader(barrierInput(action))), 204)
	}
	control("arm")
	defer control("release")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := signedBarrierRequest(ctx, barrierEvent)
	req.URL.Scheme = "http"
	req.URL.Host = server.Listener.Addr().String()
	req.RequestURI = ""
	result := make(chan error, 1)
	go func() {
		response, err := client.Do(req) // #nosec G704 -- fixed invocation-owned loopback fixture.
		if response != nil {
			_ = response.Body.Close()
		}
		result <- err
	}()
	observe := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
			body := exchange(httptest.NewRequestWithContext(t.Context(), "GET", "/webhooks/barrier", nil), 200)
			var value webhookBarrier
			if json.Unmarshal(body, &value) != nil {
				t.Fatal("invalid observation")
			}
			if len(value.Attempts) == 1 && value.Attempts[0].Outcome == want {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("barrier phase not observed")
	}
	observe("blocked")
	select {
	case err := <-result:
		t.Fatalf("held response escaped: %v", err)
	default:
	}
	cancel()
	if err := <-result; err == nil {
		t.Fatal("canceled exchange succeeded")
	}
	observe("disconnected")
	control("release")
	exchange(signedBarrierRequest(t.Context(), barrierEvent), 204)
	raw := exchange(httptest.NewRequestWithContext(t.Context(), "GET", "/webhooks/barrier", nil), 200)
	var value webhookBarrier
	if json.Unmarshal(raw, &value) != nil || len(value.Attempts) != 2 || !value.Attempts[1].Duplicate || value.Attempts[1].Digest != value.Attempts[0].Digest {
		t.Fatal("selected redelivery evidence lost")
	}
}
