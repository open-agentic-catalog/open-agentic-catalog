// Webhook fixtures verify signatures and retain bounded delivery evidence while
// exposing test-only delay, response and signing-key controls.

package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/examples/webhookreceiver"
)

// This invocation-owned receiver is a bounded test fixture, not an integration
// product. Its controls and volatile deduplication must never be deployed as a
// production receiver or exposed outside the isolated test network/loopback.
type webhookReceiver struct {
	mu        sync.Mutex
	endpoints map[string]receiverControl
	seen      map[string]bool
	latest    time.Time
	stats     receiverStats
	barrier   *webhookBarrier
}

type receiverControl struct {
	EndpointID  string `json:"endpointId"`
	Secret      string `json:"secret"`
	Version     int    `json:"version"`
	Status      int    `json:"status"`
	DelayMillis int    `json:"delayMillis"`
}

type receiverStats struct {
	Attempts   int      `json:"attempts"`
	Accepted   int      `json:"accepted"`
	Duplicates int      `json:"duplicates"`
	OutOfOrder int      `json:"outOfOrder"`
	Invalid    int      `json:"invalid"`
	Active     int      `json:"active"`
	MaxActive  int      `json:"maxActive"`
	IDs        []string `json:"ids"`
}

// newWebhookReceiver creates empty invocation-local endpoint and deduplication state.
func newWebhookReceiver() *webhookReceiver {
	return &webhookReceiver{endpoints: make(map[string]receiverControl), seen: make(map[string]bool), stats: receiverStats{IDs: []string{}}}
}

// control installs a bounded endpoint key/response configuration for the owned test.
func (r *webhookReceiver) control(writer http.ResponseWriter, request *http.Request) {
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 4096))
	decoder.DisallowUnknownFields()
	var control receiverControl
	if err := decoder.Decode(&control); err != nil {
		http.Error(writer, "invalid control", http.StatusBadRequest)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || len(control.EndpointID) != 36 || len(control.Secret) != 49 || !strings.HasPrefix(control.Secret, "whsec_") || control.Version < 1 || control.Version > 1000000 || control.Status < 200 || control.Status > 599 || control.DelayMillis < 0 || control.DelayMillis > 4000 {
		http.Error(writer, "invalid control", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.endpoints[control.EndpointID]; !exists && len(r.endpoints) >= 8 {
		http.Error(writer, "fixture endpoint limit", http.StatusServiceUnavailable)
		return
	}
	r.endpoints[control.EndpointID] = control
	writer.WriteHeader(http.StatusNoContent)
}

// receive verifies exact signed bytes and records bounded in-memory acceptance
// evidence before applying the configured response/delay. This is deliberately
// not durable receiver storage; it simulates retries and out-of-order delivery.
func (r *webhookReceiver) receive(writer http.ResponseWriter, request *http.Request) {
	defer func() { _ = request.Body.Close() }()
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, webhookreceiver.MaxBodyBytes))
	if err != nil {
		http.Error(writer, "invalid body", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	control, exists := r.endpoints[request.Header.Get("X-Catalog-Webhook-Id")]
	if !exists || webhookreceiver.Verify(control.Secret, control.Version, request.Header, body, time.Now()) != nil {
		r.stats.Invalid++
		r.mu.Unlock()
		http.Error(writer, "invalid signature", http.StatusUnauthorized)
		return
	}
	var event struct {
		ID     string    `json:"id"`
		Source string    `json:"source"`
		Type   string    `json:"type"`
		Time   time.Time `json:"time"`
	}
	if json.Unmarshal(body, &event) != nil || event.ID == "" || len(event.ID) > 64 || event.Source == "" || len(event.Source) > 1024 || event.Type == "" || event.Time.IsZero() {
		r.mu.Unlock()
		http.Error(writer, "invalid event", http.StatusBadRequest)
		return
	}
	r.stats.Attempts++
	r.stats.Active++
	r.stats.MaxActive = max(r.stats.MaxActive, r.stats.Active)
	key := event.Source + "\x00" + event.ID
	status := control.Status
	duplicate := r.seen[key]
	if status >= 200 && status < 300 {
		switch {
		case r.seen[key]:
			r.stats.Duplicates++
		case len(r.seen) >= 4096:
			status = http.StatusServiceUnavailable
		default:
			r.seen[key] = true
			r.stats.Accepted++
			r.stats.IDs = append(r.stats.IDs, event.ID)
			if event.Time.Before(r.latest) {
				r.stats.OutOfOrder++
			}
			if event.Time.After(r.latest) {
				r.latest = event.Time
			}
		}
	}
	barrier, attempt := r.barrierBegin(control.EndpointID, event.Source, event.ID, body, duplicate)
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.stats.Active--; r.mu.Unlock() }()
	if barrier != nil {
		if attempt < 0 {
			http.Error(writer, "barrier attempt limit", http.StatusServiceUnavailable)
			return
		}
		if !r.barrierWait(request.Context(), barrier, attempt) {
			http.Error(writer, "barrier interrupted", http.StatusServiceUnavailable)
			return
		}
	}
	if control.DelayMillis > 0 {
		timer := time.NewTimer(time.Duration(control.DelayMillis) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-request.Context().Done():
			return
		}
	}
	if status == http.StatusTooManyRequests {
		writer.Header().Set("Retry-After", "1")
	}
	writer.WriteHeader(status)
}

// report copies counters and sorts retained IDs without exposing endpoint signing keys.
func (r *webhookReceiver) report(writer http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	stats := r.stats
	stats.IDs = append([]string{}, r.stats.IDs...)
	r.mu.Unlock()
	sort.Strings(stats.IDs)
	writeJSON(writer, http.StatusOK, stats)
}

// generateWebhookInputs creates private-route policy, CA trust and random encryption
// material under the supervisor-owned directory for read-only container mounts.
func generateWebhookInputs(directory, subnet string, ca []byte) error {
	webhooks := filepath.Join(directory, "webhooks")
	if err := makeFixtureDirectory(webhooks); err != nil {
		return err
	}
	policy := map[string]any{"schemaVersion": 1, "policyId": "functional-webhooks", "revision": 1, "public": map[string]any{"mode": "allowlist", "origins": []string{}}, "privateProfiles": []map[string]any{{"name": "functional-webhooks", "origins": []string{fixtureOrigin}, "addressRanges": []string{subnet}, "assignedTenantIds": []string{fixtureTenant, "00000000-0000-7000-8000-000000000002"}, "trustBundleFile": "/config/webhooks/ca.pem", "transportRef": "direct"}}}
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return errors.New("generate fixture encryption key")
	}
	for name, data := range map[string][]byte{"policy.json": raw, "ca.pem": ca, "encryption.key": []byte(base64.StdEncoding.EncodeToString(key))} {
		if err := writeFixtureFile(filepath.Join(webhooks, name), data); err != nil {
			return err
		}
	}
	return nil
}
