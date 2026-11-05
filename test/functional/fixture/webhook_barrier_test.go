package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

const barrierEndpoint = "00000000-0000-7000-8000-000000000001"
const barrierEvent = "00000000-0000-7000-8000-000000000002"

func barrierInput(action string) []byte {
	return fmt.Appendf(nil, `{"action":%q,"endpointId":%q,"source":"owned-source","eventId":%q}`, action, barrierEndpoint, barrierEvent)
}

func signedBarrierRequest(ctx context.Context, event string) *http.Request {
	body := fmt.Appendf(nil, `{"id":%q,"source":"owned-source","type":"namespace.created","time":"2026-10-02T12:00:00Z","data":{"targetId":"owned"}}`, event)
	req := httptest.NewRequestWithContext(ctx, "POST", "/webhooks/events", bytes.NewReader(body))
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("whsec_"+strings.Repeat("a", 43)))
	_, _ = mac.Write([]byte(stamp + "."))
	_, _ = mac.Write(body)
	req.Header.Set("X-Catalog-Timestamp", stamp)
	req.Header.Set("X-Catalog-Signature", "v1,kid=1,sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-Catalog-Webhook-Id", barrierEndpoint)
	return req
}

func configuredBarrierReceiver() *webhookReceiver {
	r := newWebhookReceiver()
	r.endpoints[barrierEndpoint] = receiverControl{EndpointID: barrierEndpoint, Secret: "whsec_" + strings.Repeat("a", 43), Version: 1, Status: 204}
	return r
}

func TestWebhookBarrierReleaseDisconnectionAndExpiry(t *testing.T) {
	for _, outcome := range []string{"released", "disconnected", "expired"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := configuredBarrierReceiver()
				control := func(action string) {
					t.Helper()
					response := httptest.NewRecorder()
					r.barrierControl(response, httptest.NewRequestWithContext(t.Context(), "POST", "/webhooks/barrier", bytes.NewReader(barrierInput(action))))
					if response.Code != 204 {
						t.Fatal("control rejected")
					}
				}
				control("arm")
				// An unrelated accepted event must not enter the selected evidence.
				unrelated := httptest.NewRecorder()
				r.receive(unrelated, signedBarrierRequest(t.Context(), "unrelated"))
				if unrelated.Code != 204 || len(r.barrier.Attempts) != 0 {
					t.Fatal("unrelated event satisfied barrier")
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				response := httptest.NewRecorder()
				finished := make(chan struct{})
				go func() { defer close(finished); r.receive(response, signedBarrierRequest(ctx, barrierEvent)) }()
				synctest.Wait()
				r.mu.Lock()
				inFlight := len(r.barrier.Attempts) == 1 && r.barrier.Attempts[0].Outcome == "blocked" && r.stats.Active == 1
				r.mu.Unlock()
				if !inFlight {
					t.Fatal("missing selected in-flight acceptance")
				}
				select {
				case <-finished:
					t.Fatal("response escaped barrier")
				default:
				}
				switch outcome {
				case "released":
					control("release")
				case "disconnected":
					cancel()
				case "expired":
					time.Sleep(31 * time.Second)
				}
				<-finished
				if r.barrier.Attempts[0].Outcome != outcome || r.stats.Active != 0 {
					t.Fatal("barrier exit not recorded")
				}
				if outcome == "released" && response.Code != 204 || outcome != "released" && response.Code != 503 {
					t.Fatal("wrong barrier result")
				}
				// Explicit cleanup is idempotent even after the sender is gone.
				control("release")
				control("release")
				// Later redelivery must not race the now-expired original barrier
				// deadline after explicit release, including cleanup after expiry.
				time.Sleep(31 * time.Second)
				replay := httptest.NewRecorder()
				r.receive(replay, signedBarrierRequest(t.Context(), barrierEvent))
				first, second := r.barrier.Attempts[0], r.barrier.Attempts[1]
				if replay.Code != 204 || first.Duplicate || !second.Duplicate || first.Digest != second.Digest || second.Outcome != "released" || first.Outcome != outcome {
					t.Fatal("released barrier rejected redelivery or changed prior evidence")
				}
				report := httptest.NewRecorder()
				r.barrierReport(report, httptest.NewRequestWithContext(t.Context(), "GET", "/webhooks/barrier", nil))
				if strings.Contains(report.Body.String(), "whsec_") || strings.Contains(report.Body.String(), "targetId") {
					t.Fatal("report disclosed private inputs")
				}
			})
		})
	}
}

func TestWebhookBarrierRejectsInvalidControlsAndBoundsAttempts(t *testing.T) {
	r := configuredBarrierReceiver()
	for _, raw := range [][]byte{[]byte(`{}`), []byte(`{"unknown":1}`), append(barrierInput("arm"), 'x'), barrierInput("unknown"), barrierInput("release"), bytes.ReplaceAll(barrierInput("arm"), []byte(barrierEndpoint), []byte(barrierEvent))} {
		response := httptest.NewRecorder()
		r.barrierControl(response, httptest.NewRequestWithContext(t.Context(), "POST", "/webhooks/barrier", bytes.NewReader(raw)))
		if response.Code != 400 && response.Code != 409 {
			t.Fatal("invalid control accepted")
		}
	}
	arm := httptest.NewRecorder()
	r.barrierControl(arm, httptest.NewRequestWithContext(t.Context(), "POST", "/webhooks/barrier", bytes.NewReader(barrierInput("arm"))))
	for _, action := range []string{"arm", "release"} {
		raw := barrierInput(action)
		if action == "release" {
			raw = bytes.ReplaceAll(raw, []byte(barrierEvent), []byte(barrierEndpoint))
		}
		response := httptest.NewRecorder()
		r.barrierControl(response, httptest.NewRequestWithContext(t.Context(), "POST", "/webhooks/barrier", bytes.NewReader(raw)))
		if response.Code != 409 {
			t.Fatal("existing selection overwritten")
		}
	}
	release := httptest.NewRecorder()
	r.barrierControl(release, httptest.NewRequestWithContext(t.Context(), "POST", "/webhooks/barrier", bytes.NewReader(barrierInput("release"))))
	invalid := signedBarrierRequest(t.Context(), barrierEvent)
	invalid.Header.Set("X-Catalog-Signature", "invalid")
	r.receive(httptest.NewRecorder(), invalid)
	if len(r.barrier.Attempts) != 0 {
		t.Fatal("unsigned evidence accepted")
	}
	for index := range 9 {
		response := httptest.NewRecorder()
		r.receive(response, signedBarrierRequest(t.Context(), barrierEvent))
		if index < 8 && response.Code != 204 || index == 8 && response.Code != 503 {
			t.Fatal("attempt limit not enforced")
		}
	}
	report := httptest.NewRecorder()
	r.barrierReport(report, httptest.NewRequestWithContext(t.Context(), "GET", "/webhooks/barrier", nil))
	var value map[string]any
	if json.Unmarshal(report.Body.Bytes(), &value) != nil || len(value) != 5 || len(r.barrier.Attempts) != 8 {
		t.Fatal("unbounded report")
	}
}
