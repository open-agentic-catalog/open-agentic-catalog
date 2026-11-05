// Unit tests exercise the inference protocol through controlled endpoint results.
// Real HTTP, TLS and redirect qualification remain in the functional suite.
package embeddings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

type controlledEndpoint struct {
	post   func(context.Context, []byte, string) (fetcher.Result, error)
	calls  atomic.Int32
	closes atomic.Int32
}

func (e *controlledEndpoint) Post(ctx context.Context, body []byte, token string) (fetcher.Result, error) {
	e.calls.Add(1)
	return e.post(ctx, body, token)
}

func (e *controlledEndpoint) Close() { e.closes.Add(1) }

func protocolClient(t *testing.T, s config.Semantic, post func(context.Context, []byte, string) (fetcher.Result, error)) (*Client, *controlledEndpoint) {
	t.Helper()
	client, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	// Construction validates the production policy but makes no outbound request.
	client.Close()
	endpoint := &controlledEndpoint{post: post}
	client.endpoint = endpoint
	t.Cleanup(client.Close)
	return client, endpoint
}

func inferenceResult(body string) fetcher.Result {
	return fetcher.Result{Status: 200, MediaType: "application/json", Body: []byte(body)}
}

const singleVector = `{"model":"fixture","data":[{"index":0,"embedding":[3,4]}]}`

func TestClientProtocolProjection(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%v", batch), func(t *testing.T) {
			s := settings("https://model.invalid/v1/embeddings")
			s.QueryPrefix, s.DocumentPrefix = "query:", "document:"
			texts := []string{"first", "second"}
			wantRequest := map[string]any{"model": "fixture", "encoding_format": "float", "input": []any{"query:first"}}
			response := singleVector
			wantVectors := [][]float32{{0.6, 0.8}}
			timeout := s.QueryTimeout.Duration
			if batch {
				s.ResponseModel, s.RequestedDimensions = "served-alias", 2
				wantRequest["input"] = []any{"document:first", "document:second"}
				wantRequest["dimensions"] = float64(2)
				response = `{"model":"served-alias","data":[{"index":1,"embedding":[0,5]},{"index":0,"embedding":[-3,4]}],"usage":{"total_tokens":2}}`
				wantVectors = [][]float32{{-0.6, 0.8}, {0, 1}}
				timeout = s.BatchTimeout.Duration
			}
			started := time.Now()
			client, endpoint := protocolClient(t, s, func(ctx context.Context, raw []byte, token string) (fetcher.Result, error) {
				var request map[string]any
				if err := json.Unmarshal(raw, &request); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(request, wantRequest) || token != "synthetic-token" {
					t.Fatalf("request=%v, credential forwarded=%v", request, token == "synthetic-token")
				}
				deadline, ok := ctx.Deadline()
				if !ok || deadline.Before(started.Add(timeout)) || deadline.After(time.Now().Add(timeout)) {
					t.Fatalf("wrong operation deadline: %v", deadline)
				}
				return inferenceResult(response), nil
			})
			client.token = "synthetic-token"
			var got [][]float32
			var err error
			if batch {
				got, err = client.Documents(t.Context(), texts)
			} else {
				var vector []float32
				vector, err = client.Query(t.Context(), texts[0])
				got = [][]float32{vector}
			}
			if err != nil || !reflect.DeepEqual(got, wantVectors) {
				t.Fatalf("vectors=%v, error=%v", got, err)
			}
			if !reflect.DeepEqual(texts, []string{"first", "second"}) {
				t.Fatal("mutated caller inputs")
			}
			if endpoint.calls.Load() != 1 || client.State() != "healthy" || len(client.queries)+len(client.batches) != 0 {
				t.Fatal("request count, health or released work budget is incorrect")
			}
			client.Close()
			if endpoint.closes.Load() != 1 {
				t.Fatal("Close did not release endpoint")
			}
		})
	}
}

func TestClientInputBounds(t *testing.T) {
	s := settings("https://model.invalid/v1/embeddings")
	s.BatchSize, s.MaxInputBytes, s.DocumentPrefix = 2, 68, "doc:"
	for _, tc := range []struct {
		name  string
		texts []string
		valid bool
	}{
		{"empty batch", nil, false},
		{"batch overflow", []string{"a", "b", "c"}, false},
		{"blank", []string{"valid", " \t\n"}, false},
		{"invalid UTF8", []string{"valid", "\xff"}, false},
		{"NUL", []string{"valid", "a\x00b"}, false},
		{"prefixed byte overflow", []string{strings.Repeat("é", 32) + "x"}, false},
		{"exact byte and batch limits", []string{strings.Repeat("é", 32), "valid"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, endpoint := protocolClient(t, s, func(context.Context, []byte, string) (fetcher.Result, error) {
				return inferenceResult(`{"model":"fixture","data":[{"index":0,"embedding":[1,0]},{"index":1,"embedding":[0,1]}]}`), nil
			})
			got, err := client.Documents(t.Context(), tc.texts)
			if tc.valid {
				if err != nil || !reflect.DeepEqual(got, [][]float32{{1, 0}, {0, 1}}) || endpoint.calls.Load() != 1 {
					t.Fatalf("valid boundary: %v, %v", got, err)
				}
			} else if !errors.Is(err, ErrRejected) || got != nil || endpoint.calls.Load() != 0 || client.State() != "unknown" {
				t.Fatalf("local rejection: %v, %v, calls=%d, state=%s", got, err, endpoint.calls.Load(), client.State())
			}
		})
	}
}

func TestClientResponseRejectionIsAtomic(t *testing.T) {
	// A valid first vector must never escape when a later vector is malformed.
	prefix := `{"model":"fixture","data":[{"index":0,"embedding":[3,4]},`
	cases := []struct{ name, body, media string }{
		{"missing index", prefix + `{"embedding":[1,0]}]}`, "application/json"},
		{"negative index", prefix + `{"index":-1,"embedding":[1,0]}]}`, "application/json"},
		{"out of range index", prefix + `{"index":2,"embedding":[1,0]}]}`, "application/json"},
		{"duplicate index", prefix + `{"index":0,"embedding":[1,0]}]}`, "application/json"},
		{"fractional index", prefix + `{"index":1.5,"embedding":[1,0]}]}`, "application/json"},
		{"missing vector", prefix + `{"index":1}]}`, "application/json"},
		{"dimension mismatch", prefix + `{"index":1,"embedding":[1]}]}`, "application/json"},
		{"null component", prefix + `{"index":1,"embedding":[null,1]}]}`, "application/json"},
		{"string component", prefix + `{"index":1,"embedding":["1",0]}]}`, "application/json"},
		{"float32 overflow", prefix + `{"index":1,"embedding":[1e100,0]}]}`, "application/json"},
		{"zero vector", prefix + `{"index":1,"embedding":[0,0]}]}`, "application/json"},
		{"short batch", singleVector, "application/json"},
		{"wrong model", `{"model":"private-model","data":[{"index":0,"embedding":[1,0]},{"index":1,"embedding":[0,1]}]}`, "application/json"},
		{"null envelope", `null`, "application/json"},
		{"duplicate key", prefix + `{"index":1,"index":0,"embedding":[1,0]}]}`, "application/json"},
		{"trailing value", prefix + `{"index":1,"embedding":[1,0]}]} {}`, "application/json"},
		{"invalid JSON", `{private-response`, "application/json"},
		{"wrong media", prefix + `{"index":1,"embedding":[1,0]}]}`, "text/plain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, endpoint := protocolClient(t, settings("https://model.invalid/v1/embeddings"), func(context.Context, []byte, string) (fetcher.Result, error) {
				result := inferenceResult(tc.body)
				result.MediaType = tc.media
				return result, nil
			})
			got, err := client.Documents(t.Context(), []string{"first", "second"})
			if !errors.Is(err, ErrInvalidResponse) || err.Error() != ErrInvalidResponse.Error() || got != nil || endpoint.calls.Load() != 1 || client.State() != "degraded" || len(client.batches) != 0 {
				t.Fatalf("malformed batch: %v, %v, calls=%d, state=%s", got, err, endpoint.calls.Load(), client.State())
			}
		})
	}
}

func TestClientOutcomesAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		status         int
		err            error
		outcome, state string
	}{
		{400, ErrRejected, "rejected", "healthy"},
		{413, ErrRejected, "rejected", "healthy"},
		{422, ErrRejected, "rejected", "healthy"},
		{401, ErrAuthentication, "authentication", "degraded"},
		{403, ErrAuthentication, "authentication", "degraded"},
		{429, ErrUnavailable, "unavailable", "degraded"},
		{500, ErrUnavailable, "unavailable", "degraded"},
		{501, ErrUnavailable, "unavailable", "degraded"},
		{502, ErrUnavailable, "unavailable", "degraded"},
		{503, ErrUnavailable, "unavailable", "degraded"},
		{505, ErrUnavailable, "unavailable", "degraded"},
		{201, ErrInvalidResponse, "invalid_response", "degraded"},
		{204, ErrInvalidResponse, "invalid_response", "degraded"},
		{302, ErrInvalidResponse, "invalid_response", "degraded"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			response := inferenceResult(singleVector)
			response.RetryAfter = "120"
			client, endpoint := protocolClient(t, settings("https://model.invalid/v1/embeddings"), func(context.Context, []byte, string) (fetcher.Result, error) { return response, nil })
			type observation struct{ operation, outcome string }
			var observed []observation
			client.observer = func(operation, outcome string, elapsed time.Duration) {
				if elapsed < 0 {
					t.Error("negative duration")
				}
				observed = append(observed, observation{operation, outcome})
			}
			if _, err := client.Query(t.Context(), "query"); err != nil {
				t.Fatal(err)
			}
			response.Status, response.Body = tc.status, []byte("private provider response")
			vectors, err := client.Documents(t.Context(), []string{"document"})
			if !errors.Is(err, tc.err) || err.Error() != tc.err.Error() || vectors != nil || client.State() != tc.state {
				t.Fatalf("outcome: %v, %v, state=%s", vectors, err, client.State())
			}
			var hint *catalog.EmbeddingRetryError
			if hasHint := errors.As(err, &hint); hasHint != (tc.status == 429 || tc.status == 503) || hasHint && hint.RetryAfter != 2*time.Minute {
				t.Fatalf("status %d retained incorrect hint: %v", tc.status, hint)
			}
			// Neither a local rejection nor cancellation changes the last provider state.
			if _, err := client.Query(t.Context(), ""); !errors.Is(err, ErrRejected) || client.State() != tc.state {
				t.Fatalf("local rejection: %v, state=%s", err, client.State())
			}
			for range cap(client.queries) {
				client.queries <- struct{}{}
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err = client.Query(ctx, "query")
			for range cap(client.queries) {
				<-client.queries
			}
			if !errors.Is(err, context.Canceled) || client.State() != tc.state {
				t.Fatalf("cancellation: %v, state=%s", err, client.State())
			}
			response = inferenceResult(singleVector)
			if _, err := client.Query(t.Context(), "query"); err != nil || client.State() != "healthy" {
				t.Fatalf("recovery: %v, state=%s", err, client.State())
			}
			want := []observation{{"query", "success"}, {"batch", tc.outcome}, {"query", "rejected"}, {"query", "admission"}, {"query", "success"}}
			if !reflect.DeepEqual(observed, want) || endpoint.calls.Load() != 3 || len(client.queries)+len(client.batches) != 0 {
				t.Fatalf("observations=%v, calls=%d", observed, endpoint.calls.Load())
			}
		})
	}
}

// Grammar bounds belong to fetcher; these cases prove the parsed duration and
// safe category reach both inference operations without extra provider calls.
func TestClientRetryHints(t *testing.T) {
	received := time.Date(2026, 9, 26, 12, 0, 0, 250000000, time.UTC)
	for _, tc := range []struct {
		name, header string
		want         time.Duration
	}{
		{"seconds", "120", 2 * time.Minute},
		{"date", "Sat, 26 Sep 2026 12:02:00 GMT", 119750 * time.Millisecond},
		{"missing", "", 0},
		{"zero", "0", 0},
		{"malformed", "private provider header", 0},
		{"excessive", "86401", 0},
		{"maximum", "86400", 24 * time.Hour},
	} {
		for _, status := range []int{429, 503} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, status), func(t *testing.T) {
				client, endpoint := protocolClient(t, settings("https://model.invalid/v1/embeddings"), func(context.Context, []byte, string) (fetcher.Result, error) {
					return fetcher.Result{Status: status, RetryAfter: tc.header, FetchedAt: received, Body: []byte("private response")}, nil
				})
				for _, batch := range []bool{false, true} {
					var err error
					if batch {
						_, err = client.Documents(t.Context(), []string{"metadata"})
					} else {
						_, err = client.Query(t.Context(), "query")
					}
					var hint *catalog.EmbeddingRetryError
					hasHint := errors.As(err, &hint)
					if !errors.Is(err, ErrUnavailable) || err.Error() != ErrUnavailable.Error() || hasHint != (tc.want > 0) || hasHint && hint.RetryAfter != tc.want {
						t.Fatalf("batch=%v error=%v hint=%v want=%s", batch, err, hint, tc.want)
					}
				}
				if endpoint.calls.Load() != 2 || client.State() != "degraded" || len(client.queries)+len(client.batches) != 0 {
					t.Fatal("retry, provider state or work budget changed")
				}
			})
		}
	}
}

func TestClientTransportErrorsAndCallerDeadline(t *testing.T) {
	for _, cancelDuringPost := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancelDuringPost), func(t *testing.T) {
			s := settings("https://model.invalid/v1/embeddings")
			s.QueryTimeout.Duration = 10 * time.Second
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			callerDeadline, _ := ctx.Deadline()
			client, endpoint := protocolClient(t, s, func(ctx context.Context, _ []byte, _ string) (fetcher.Result, error) {
				deadline, ok := ctx.Deadline()
				if !ok || deadline != callerDeadline {
					t.Error("client extended the caller deadline")
				}
				if cancelDuringPost {
					cancel()
				}
				return fetcher.Result{}, errors.New("private transport diagnostic")
			})
			wantErr, wantState := ErrUnavailable, "degraded"
			if cancelDuringPost {
				wantErr, wantState = context.Canceled, "unknown"
			}
			if got, err := client.Query(ctx, "query"); got != nil || !errors.Is(err, wantErr) || err.Error() != wantErr.Error() || client.State() != wantState || endpoint.calls.Load() != 1 || len(client.queries) != 0 {
				t.Fatalf("transport error: %v, %v, state=%s", got, err, client.State())
			}
		})
	}
	for _, initial := range []string{"unknown", "healthy", "degraded"} {
		for _, operation := range []string{"query", "batch"} {
			for _, occupied := range []bool{false, true} {
				for _, expired := range []bool{false, true} {
					t.Run(fmt.Sprintf("admission/%s/%s/occupied=%t/expired=%t", initial, operation, occupied, expired), func(t *testing.T) {
						response := inferenceResult(singleVector)
						client, endpoint := protocolClient(t, settings("https://model.invalid/v1/embeddings"), func(context.Context, []byte, string) (fetcher.Result, error) { return response, nil })
						if initial != "unknown" {
							var want error
							if initial == "degraded" {
								response.Status = 503
								want = ErrUnavailable
							}
							if _, err := client.Query(t.Context(), "establish provider state"); !errors.Is(err, want) {
								t.Fatal(err)
							}
						}
						if client.State() != initial {
							t.Fatal("incorrect initial state")
						}
						before := endpoint.calls.Load()
						response = inferenceResult(singleVector)
						var observed []string
						client.observer = func(op, outcome string, elapsed time.Duration) {
							if op != operation || elapsed < 0 {
								t.Fatal("invalid admission observation")
							}
							observed = append(observed, outcome)
						}
						budget := client.queries
						if operation == "batch" {
							budget = client.batches
						}
						if occupied {
							for range cap(budget) {
								budget <- struct{}{}
							}
						}
						ctx, cancel := context.WithCancel(t.Context())
						want := context.Canceled
						if expired {
							cancel()
							ctx, cancel = context.WithDeadline(t.Context(), time.Unix(0, 0))
							want = context.DeadlineExceeded
						}
						cancel()
						var err error
						if operation == "batch" {
							_, err = client.Documents(ctx, []string{"document"})
						} else {
							_, err = client.Query(ctx, "query")
						}
						if !errors.Is(err, want) || err.Error() != want.Error() || client.State() != initial || endpoint.calls.Load() != before || !reflect.DeepEqual(observed, []string{"admission"}) {
							t.Fatalf("local admission: err=%v state=%s calls=%d observations=%v", err, client.State(), endpoint.calls.Load()-before, observed)
						}
						if occupied {
							for range cap(budget) {
								<-budget
							}
						}
						var recoveryErr error
						if operation == "batch" {
							_, recoveryErr = client.Documents(t.Context(), []string{"document"})
						} else {
							_, recoveryErr = client.Query(t.Context(), "query")
						}
						if recoveryErr != nil || client.State() != "healthy" || endpoint.calls.Load() != before+1 || len(budget) != 0 || !reflect.DeepEqual(observed, []string{"admission", "success"}) {
							t.Fatalf("admission recovery: %v %v", recoveryErr, observed)
						}
					})
				}
			}
		}
	}
}

func TestClientConcurrentBudgets(t *testing.T) {
	for _, blocked := range []string{"query", "batch"} {
		t.Run(blocked, func(t *testing.T) {
			s := settings("https://model.invalid/v1/embeddings")
			s.QueryConcurrency, s.BatchConcurrency = 1, 1
			s.QueryTimeout.Duration, s.BatchTimeout.Duration = 10*time.Second, 10*time.Second
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			finished := make(chan error, 1)
			client, endpoint := protocolClient(t, s, func(ctx context.Context, raw []byte, _ string) (fetcher.Result, error) {
				var request struct {
					Input []string `json:"input"`
				}
				if err := json.Unmarshal(raw, &request); err != nil {
					return fetcher.Result{}, err
				}
				if len(request.Input) == 1 && request.Input[0] == blocked {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return fetcher.Result{}, ctx.Err()
					}
				}
				return inferenceResult(singleVector), nil
			})
			call := func(ctx context.Context, operation string) error {
				if operation == "query" {
					_, err := client.Query(ctx, operation)
					return err
				}
				_, err := client.Documents(ctx, []string{operation})
				return err
			}
			go func() { defer close(done); finished <- call(ctx, blocked) }()
			t.Cleanup(func() { cancel(); <-done })
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first request did not enter endpoint")
			}
			waiter, stopWaiter := context.WithCancel(ctx)
			stopWaiter()
			if err := call(waiter, blocked); !errors.Is(err, context.Canceled) {
				t.Fatalf("occupied budget waiter: %v", err)
			}
			other := "query"
			if blocked == other {
				other = "batch"
			}
			if err := call(ctx, other); err != nil {
				t.Fatalf("independent budget: %v", err)
			}
			if endpoint.calls.Load() != 2 || len(client.queries)+len(client.batches) != 1 {
				t.Fatal("concurrent budgets or cancelled admission are incorrect")
			}
			close(release)
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("first request did not complete")
			}
			if len(client.queries)+len(client.batches) != 0 {
				t.Fatal("completed requests retained capacity")
			}
		})
	}
}
