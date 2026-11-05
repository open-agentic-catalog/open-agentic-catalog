//go:build functional

package federation

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

// The real hardened transport must preserve the response hint and its receipt
// timestamp without retrying underneath the durable Job's attempt budget.
func TestSourceRetryHintsSurviveHTTPSWithoutTransportRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		hint   string
		class  SyncFailureClass
	}{
		{"rate limit seconds", http.StatusTooManyRequests, "120", SyncFailureRateLimited},
		{"rate limit date", http.StatusTooManyRequests, "date", SyncFailureRateLimited},
		{"unavailable date", http.StatusServiceUnavailable, "date", SyncFailureTransient},
		{"unavailable malformed", http.StatusServiceUnavailable, "soon", SyncFailureTransient},
		{"unavailable excessive", http.StatusServiceUnavailable, "86401", SyncFailureTransient},
		{"unavailable absent", http.StatusServiceUnavailable, "", SyncFailureTransient},
		{"authentication with hint", http.StatusForbidden, "120", SyncFailureAuthentication},
		{"permanent with hint", http.StatusNotImplemented, "120", SyncFailurePermanent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deadline := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Second)
			hint := tc.hint
			if hint == "date" {
				hint = deadline.Format(http.TimeFormat)
			}
			var calls atomic.Int64
			server, outbound := newFederationHTTPSOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/catalog.json" || r.TLS == nil {
					t.Errorf("unexpected source request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", hint)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"private":"upstream detail"}`)
			}))
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			result, err := outbound.Fetch(ctx, fetcher.Request{URL: server.URL + "/catalog.json", TenantID: auditEvidenceTenantID, NetworkProfile: "owned-https", ExpectedPolicyDigest: federationTestPolicyDigest})
			if err != nil || result.Status != tc.status || result.RetryAfter != hint || result.FetchedAt.IsZero() || calls.Load() != 1 {
				t.Fatalf("source response: status=%d hint=%q calls=%d err=%v", result.Status, result.RetryAfter, calls.Load(), err)
			}
			failure := syncHTTPError(result)
			class, delay := ClassifySyncError(failure)
			var expected time.Duration
			if tc.hint == "date" {
				expected = deadline.Sub(result.FetchedAt)
				if expected <= 0 {
					t.Fatal("HTTP-date fixture expired before receipt")
				}
			} else if tc.hint == "120" && tc.status == http.StatusTooManyRequests {
				expected = 2 * time.Minute
			}
			if class != tc.class || delay != expected {
				t.Fatalf("response classification: %s %s, want %s %s", class, delay, tc.class, expected)
			}
			if strings.Contains(failure.Error(), "upstream detail") {
				t.Fatal("private upstream response became retained failure text")
			}
		})
	}
}
