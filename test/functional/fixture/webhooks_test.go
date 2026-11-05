package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOwnedWebhookReceiverVerifiesDeduplicatesAndBoundsState(t *testing.T) {
	r := newWebhookReceiver()
	id := "00000000-0000-7000-8000-000000000001"
	secret := "whsec_" + strings.Repeat("a", 43)
	configure := func(status, delay int) {
		t.Helper()
		raw, err := json.Marshal(receiverControl{EndpointID: id, Secret: secret, Version: 1, Status: status, DelayMillis: delay}) // #nosec G117 -- invocation-owned verifier test input, never retained in a report.
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		r.control(response, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhooks/control", bytes.NewReader(raw)))
		if response.Code != http.StatusNoContent {
			t.Fatal("valid receiver configuration rejected")
		}
	}
	configure(http.StatusNoContent, 0)
	send := func(eventID, at string, valid bool, canceled bool) int {
		t.Helper()
		raw := []byte(fmt.Sprintf(`{"id":%q,"source":"test-source","type":"test-event","time":%q}`, eventID, at))
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhooks/events", bytes.NewReader(raw))
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(stamp + "."))
		_, _ = mac.Write(raw)
		signature := "v1,kid=1,sha256=" + hex.EncodeToString(mac.Sum(nil))
		if !valid {
			signature = "invalid"
		}
		req.Header.Set("X-Catalog-Timestamp", stamp)
		req.Header.Set("X-Catalog-Signature", signature)
		req.Header.Set("X-Catalog-Webhook-Id", id)
		if canceled {
			ctx, cancel := context.WithCancel(req.Context())
			cancel()
			req = req.WithContext(ctx)
		}
		response := httptest.NewRecorder()
		r.receive(response, req)
		return response.Code
	}
	for _, event := range []struct{ id, at string }{{"one", "2026-09-07T12:00:00Z"}, {"one", "2026-09-07T12:00:00Z"}, {"two", "2000-01-01T00:00:00Z"}} {
		if send(event.id, event.at, true, false) != http.StatusNoContent {
			t.Fatal("valid signed events failed")
		}
	}
	if send("bad", "2026-09-07T12:00:00Z", false, false) != http.StatusUnauthorized {
		t.Fatal("invalid signature accepted")
	}
	configure(http.StatusTooManyRequests, 0)
	if send("retry", "2026-09-07T12:00:00Z", true, false) != http.StatusTooManyRequests || r.seen["test-source\x00retry"] {
		t.Fatal("rejected delivery was deduplicated as accepted")
	}
	configure(http.StatusNoContent, 1)
	if send("three", "2026-09-07T12:00:00Z", true, false) != http.StatusNoContent {
		t.Fatal("delayed receiver failed")
	}
	_ = send("canceled", "2026-09-07T12:00:00Z", true, true)
	if r.stats.Active != 0 || r.stats.Duplicates != 1 || r.stats.OutOfOrder != 1 || r.stats.Invalid != 1 {
		t.Fatalf("receiver statistics: %#v", r.stats)
	}
	response := httptest.NewRecorder()
	r.report(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/webhooks/state", nil))
	if strings.Contains(response.Body.String(), secret) {
		t.Fatal("receiver report exposed a key")
	}
	configure(http.StatusNoContent, 0)
	for len(r.seen) < 4096 {
		r.seen[strconv.Itoa(len(r.seen))] = true
	}
	if send("over-cap", "2026-09-07T12:00:00Z", true, false) != http.StatusServiceUnavailable || len(r.seen) != 4096 {
		t.Fatal("receiver fixture state grew without bound")
	}
	for _, raw := range []string{`{}`, `{"unknown":true}`, `{} {}`, strings.Repeat(" ", 4097)} {
		response := httptest.NewRecorder()
		r.control(response, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhooks/control", strings.NewReader(raw)))
		if response.Code != http.StatusBadRequest {
			t.Fatal("invalid fixture control accepted")
		}
	}
}

func TestGeneratedWebhookMountExcludesReceiverPrivateKey(t *testing.T) {
	directory := t.TempDir()
	if err := generate([]string{"--directory", directory, "--subnet", "172.31.9.0/28", "--webhooks"}); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(directory, "server.pem"), filepath.Join(directory, "server-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || certificate.VerifyHostname("api") != nil || certificate.VerifyHostname("unassigned.example") == nil {
		t.Fatal("receiver certificate does not bind the fixed API network namespace")
	}
	items, err := os.ReadDir(filepath.Join(directory, "webhooks"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatal("webhook mount contains unrelated receiver inputs")
	}
	raw, err := os.ReadFile(filepath.Join(directory, "webhooks", "encryption.key")) // #nosec G304 -- exact invocation-owned temporary fixture path.
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil || len(decoded) != 32 {
		t.Fatal("generated encryption key is invalid")
	}
	raw, err = os.ReadFile(filepath.Join(directory, "webhooks", "policy.json")) // #nosec G304 -- exact invocation-owned temporary fixture path.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "/config/webhooks/ca.pem") || strings.Contains(string(raw), "/federation/") {
		t.Fatal("webhook policy borrowed another transport's mounted configuration")
	}
}
