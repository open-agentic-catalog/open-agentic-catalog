//go:build functional

// Observation tests protect provider health and bounded metric outcomes through
// real client calls to an owned loopback server, without an inference service.
package embeddings

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestObservedClientConstruction keeps optional startup independent of inference
// and never reports a request outcome for disabled or invalid configuration.
func TestObservedClientConstruction(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	for _, mode := range []string{"enabled", "disabled", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			s := settings(server.URL + "/v1/embeddings")
			switch mode {
			case "disabled":
				s.Enabled = false
				s.TokenFile = filepath.Join(t.TempDir(), "missing-token")
			case "invalid":
				s.Dimensions = 0
			}
			observations := 0
			client, err := NewObserved(s, func(string, string, time.Duration) { observations++ })
			if client != nil {
				t.Cleanup(client.Close)
			}
			if (err != nil) != (mode == "invalid") || (client != nil) != (mode == "enabled") {
				t.Fatalf("construction: client present=%v, error=%v", client != nil, err)
			}
			if client != nil && client.State() != "unknown" {
				t.Fatalf("unqueried client state = %q", client.State())
			}
			if requests.Load() != 0 || observations != 0 {
				t.Fatal("construction or state inspection performed inference or emitted an outcome")
			}
		})
	}
}

// TestObservedClientTransitions distinguishes provider health from input rejection
// and caller cancellation, and verifies that successful work restores health.
func TestObservedClientTransitions(t *testing.T) {
	for _, initial := range []string{"unknown", "healthy", "degraded"} {
		for _, tc := range []struct {
			name, operation, outcome, state string
			status                          int32
			local                           bool
			wantErr                         error
		}{
			{"query success", "query", "success", "healthy", http.StatusOK, false, nil},
			{"batch success", "batch", "success", "healthy", http.StatusOK, false, nil},
			{"provider rejection", "query", "rejected", initial, http.StatusBadRequest, false, ErrRejected},
			{"input rejection", "batch", "rejected", initial, http.StatusOK, true, ErrRejected},
			{"cancelled query", "query", "admission", initial, http.StatusOK, true, context.Canceled},
			{"cancelled batch", "batch", "admission", initial, http.StatusOK, true, context.Canceled},
			{"deadline", "query", "admission", initial, http.StatusOK, true, context.DeadlineExceeded},
			{"authentication", "query", "authentication", "degraded", http.StatusUnauthorized, false, ErrAuthentication},
			{"invalid response", "batch", "invalid_response", "degraded", http.StatusNoContent, false, ErrInvalidResponse},
			{"unavailable", "batch", "unavailable", "degraded", http.StatusServiceUnavailable, false, ErrUnavailable},
		} {
			t.Run(initial+"/"+tc.name, func(t *testing.T) {
				var requests, status atomic.Int32
				status.Store(http.StatusOK)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					w.Header().Set("Content-Type", "application/json")
					code := int(status.Load())
					w.WriteHeader(code)
					if code == http.StatusOK {
						_, _ = w.Write([]byte(`{"model":"fixture","data":[{"index":0,"embedding":[1,0]}]}`))
					}
				}))
				t.Cleanup(server.Close)
				type observation struct {
					operation, outcome string
					duration           time.Duration
				}
				var observations []observation
				s := settings(server.URL + "/v1/embeddings")
				s.QueryConcurrency, s.BatchConcurrency = 1, 1
				client, err := NewObserved(s, func(operation, outcome string, duration time.Duration) {
					observations = append(observations, observation{operation, outcome, duration})
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(client.Close)
				if initial != "unknown" {
					var wantErr error
					if initial == "degraded" {
						status.Store(http.StatusServiceUnavailable)
						wantErr = ErrUnavailable
					}
					if _, err := client.Query(t.Context(), "establish health"); !errors.Is(err, wantErr) {
						t.Fatal("establish initial health", err)
					}
				}
				if client.State() != initial {
					t.Fatalf("initial state = %q, want %q", client.State(), initial)
				}
				observations = nil
				before := requests.Load()
				status.Store(tc.status)
				ctx := t.Context()
				if errors.Is(tc.wantErr, context.Canceled) || errors.Is(tc.wantErr, context.DeadlineExceeded) {
					var cancel context.CancelFunc
					if errors.Is(tc.wantErr, context.Canceled) {
						ctx, cancel = context.WithCancel(ctx)
					} else {
						ctx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
					}
					cancel()
					// Saturating the selected budget makes cancellation deterministic:
					// there is no race between an available slot and a closed context.
					budget := client.queries
					if tc.operation == "batch" {
						budget = client.batches
					}
					budget <- struct{}{}
					t.Cleanup(func() { <-budget })
				}
				var vectors [][]float32
				if tc.operation == "batch" {
					inputs := []string{"private document"}
					if tc.name == "input rejection" {
						inputs = nil
					}
					vectors, err = client.Documents(ctx, inputs)
				} else {
					var vector []float32
					vector, err = client.Query(ctx, "private query")
					if vector != nil {
						vectors = [][]float32{vector}
					}
				}
				if !errors.Is(err, tc.wantErr) || (vectors != nil) != (tc.wantErr == nil) {
					t.Fatalf("result = %v, error = %v, want error %v", vectors, err, tc.wantErr)
				}
				if client.State() != tc.state || len(observations) != 1 {
					t.Fatalf("state = %q, observations = %+v", client.State(), observations)
				}
				got := observations[0]
				if got.operation != tc.operation || got.outcome != tc.outcome || got.duration < 0 {
					t.Fatalf("unexpected metric (must contain only fixed labels): %+v", got)
				}
				wantCalls := int32(1)
				if tc.local {
					wantCalls = 0
				}
				if requests.Load()-before != wantCalls {
					t.Fatalf("request count = %d, want %d", requests.Load()-before, wantCalls)
				}
			})
		}
	}
}
