//go:build functional

package webhooksender

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/examples/webhookreceiver"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

func TestHTTPSWebhookReceiverScopedTrustSignatureAndIsolation(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	secret := "whsec_" + strings.Repeat("a", 43)
	var hits atomic.Int32
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://unapproved.example/events", http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == "/large" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, strings.Repeat("x", 2048))
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil || webhookreceiver.Verify(secret, 1, r.Header, body, time.Now()) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/throttled":
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		case "/unavailable":
			w.Header().Set("Retry-After", "Sat, 26 Sep 2026 12:00:00 GMT")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	policy := fetcher.RoutingPolicy{ID: "webhook-test", Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64), Public: fetcher.PublicRoute{Mode: fetcher.PublicDisabled}, Timeout: time.Second, MaxResponseBytes: 1024, PrivateRoutes: []fetcher.PrivateRoute{{Name: "receiver", Origins: []string{receiver.URL}, AddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, AssignedTenantIDs: []string{tenant}, TrustBundlePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: receiver.Certificate().Raw}), AllowTestAddresses: true}}}
	sender, err := NewWithPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"specversion":"1.0","id":"event-1","source":"urn:test","type":"test"}`)
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	request := webhook.SendRequest{TenantID: tenant, URL: receiver.URL + "/events", Body: body, Headers: map[string]string{"X-Catalog-Timestamp": timestamp, "X-Catalog-Signature": "v1,kid=1,sha256=" + hex.EncodeToString(mac.Sum(nil))}}
	if result, err := sender.Send(context.Background(), request); err != nil || result.Status != http.StatusNoContent {
		t.Fatalf("verified HTTPS delivery: %+v %v", result, err)
	}
	request.TenantID = "00000000-0000-7000-8000-000000000002"
	if _, err := sender.Send(context.Background(), request); err == nil || hits.Load() != 1 {
		t.Fatal("private receiver accepted an unassigned tenant")
	}
	request.TenantID = tenant
	for _, test := range []struct {
		path, hint string
		status     int
	}{
		{"/throttled", "120", http.StatusTooManyRequests},
		{"/unavailable", "Sat, 26 Sep 2026 12:00:00 GMT", http.StatusServiceUnavailable},
	} {
		request.URL = receiver.URL + test.path
		before := hits.Load()
		result, err := sender.Send(context.Background(), request)
		if err != nil || result.Status != test.status || result.RetryAfter != test.hint || hits.Load() != before+1 {
			t.Fatalf("HTTPS retry response lost or retried inside transport: %+v, %v", result, err)
		}
	}
	request.URL = receiver.URL + "/redirect"
	if result, err := sender.Send(context.Background(), request); err == nil && result.Status != http.StatusTemporaryRedirect {
		t.Fatal("redirect was followed")
	}
	request.URL = receiver.URL + "/large"
	if _, err := sender.Send(context.Background(), request); err == nil {
		t.Fatal("over-limit receiver response accepted")
	}
	request.URL = receiver.URL + "/events"
	policy.PrivateRoutes[0].TrustBundlePEM = nil
	untrusted, err := NewWithPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	before := hits.Load()
	if _, err := untrusted.Send(context.Background(), request); err == nil || hits.Load() != before {
		t.Fatal("private CA escaped its configured transport")
	}
	policy.PrivateRoutes = append(policy.PrivateRoutes, policy.PrivateRoutes[0])
	policy.PrivateRoutes[1].Name = "ambiguous"
	if _, err := NewWithPolicy(policy); err == nil {
		t.Fatal("ambiguous private routing accepted")
	}
	policy.AllowHTTPForTests = true
	if _, err := NewWithPolicy(policy); err == nil {
		t.Fatal("HTTP webhook policy accepted")
	}
}
