package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failedBody struct{ err error }

func (b failedBody) Read([]byte) (int, error) { return 0, b.err }

func TestHandlerFailurePrivacyAndSlotRecovery(t *testing.T) {
	private := errors.New("PRIVATE_DATABASE_OR_BODY_CAUSE")
	for _, tc := range []struct {
		name, message string
		status        int
		accepts       int
	}{
		{"body read", "invalid body", 400, 0},
		{"missing signer", "signer unavailable", 503, 0},
		{"malformed signer", "signer unavailable", 503, 0},
		{"storage refusal", "acceptance unavailable", 503, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := filepath.Join(t.TempDir(), "PRIVATE_SIGNER_PATH.json")
			good := []byte(`{"secret":"` + testSecret + `","version":1}`)
			if tc.name != "missing signer" {
				content := good
				if tc.name == "malformed signer" {
					content = []byte(`{"secret":"PRIVATE_CONFIGURED_SECRET","version":0}`)
				}
				if err := os.WriteFile(key, content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			accepts := 0
			reject := tc.name == "storage refusal"
			s := &storeCalls{accept: func(_ context.Context, e Event, at time.Time) error {
				accepts++
				if e != publicationEvent() || !at.Equal(testTime) {
					t.Fatal("invalid receipt accepted")
				}
				if reject {
					return private
				}
				return nil
			}}
			a := &application{store: s, catalog: &catalogCalls{}, tenant: testTenant, host: testHost, signerFile: key, now: func() time.Time { return testTime }, slots: make(chan struct{}, 1)}
			raw := eventBytes("publication.activated")
			post := func(body io.Reader) *httptest.ResponseRecorder {
				r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://receiver.test/webhook", body)
				r.Header = signature(raw, 1, testSecret)
				w := httptest.NewRecorder()
				a.ServeHTTP(w, r)
				return w
			}
			var body io.Reader = strings.NewReader(string(raw))
			if tc.name == "body read" {
				body = failedBody{private}
			}
			reply := post(body)
			if reply.Code != tc.status || strings.TrimSpace(reply.Body.String()) != tc.message || reply.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("unsafe or wrong refusal", reply.Code, reply.Body.String())
			}
			for _, secret := range []string{private.Error(), key, "PRIVATE_CONFIGURED_SECRET", testSecret, string(raw)} {
				if strings.Contains(reply.Body.String(), secret) {
					t.Fatal("public refusal disclosed private input")
				}
			}
			if accepts != tc.accepts || len(a.slots) != 0 {
				t.Fatal("failed request accepted work or retained its HTTP slot", accepts, len(a.slots))
			}
			// Restore only the selected failure, then reuse the same handler and slot.
			if tc.name == "missing signer" || tc.name == "malformed signer" {
				if err := os.WriteFile(key, good, 0600); err != nil {
					t.Fatal(err)
				}
			}
			reject = false
			reply = post(strings.NewReader(string(raw)))
			if reply.Code != 204 || accepts != tc.accepts+1 || len(a.slots) != 0 {
				t.Fatal("repaired request could not use the released slot", reply.Code, accepts)
			}
		})
	}
}

func TestHealthAndRoutingAvoidAdmissionAndDependencies(t *testing.T) {
	a := &application{store: &storeCalls{}, catalog: &catalogCalls{}, signerFile: filepath.Join(t.TempDir(), "missing"), slots: make(chan struct{}, 1)}
	a.slots <- struct{}{}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/healthz", 200}, {http.MethodGet, "/webhook", 405}, {http.MethodPost, "/other", 404},
	} {
		r := httptest.NewRequestWithContext(t.Context(), tc.method, "https://receiver.test"+tc.path, nil)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || len(a.slots) != 1 {
			t.Fatal("routing consumed admission or changed response", tc, w.Code)
		}
		if tc.status == 405 && w.Header().Get("Allow") != "POST" {
			t.Fatal("missing allowed-method hint")
		}
	}
}
