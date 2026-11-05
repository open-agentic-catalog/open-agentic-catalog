//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package embeddings

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// TestVectors verifies request preprocessing, out-of-order indexes and normalization.
func TestVectors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/prefix/v1/embeddings" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected transport request")
		}
		var request struct {
			Input      []string `json:"input"`
			Model      string   `json:"model"`
			Encoding   string   `json:"encoding_format"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Model != "fixture" || request.Encoding != "float" || request.Dimensions != 2 {
			t.Error("wrong embedding request")
		}
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			if len(request.Input) != 2 || request.Input[0] != "doc:one" || request.Input[1] != "doc:two" {
				t.Error("document prefix")
			}
			_, _ = w.Write([]byte(`{"model":"served","usage":{"total_tokens":4},"data":[{"index":1,"embedding":[0,5]},{"index":0,"embedding":[3,4]}]}`))
		} else {
			if len(request.Input) != 1 || request.Input[0] != "query:find" {
				t.Error("query prefix")
			}
			_, _ = w.Write([]byte(`{"model":"served","data":[{"index":0,"embedding":[6,8]}]}`))
		}
	}))
	defer server.Close()
	s := settings(server.URL + "/prefix/v1/embeddings")
	s.ResponseModel = "served"
	s.RequestedDimensions = 2
	s.DocumentPrefix = "doc:"
	s.QueryPrefix = "query:"
	c, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	v, err := c.Documents(context.Background(), []string{"one", "two"})
	if err != nil || len(v) != 2 || math.Abs(float64(v[0][0])-.6) > 1e-6 || v[1][1] != 1 {
		t.Fatalf("vectors %v %v", v, err)
	}
	q, err := c.Query(context.Background(), "find")
	if err != nil || math.Abs(float64(q[1])-.8) > 1e-6 {
		t.Fatalf("query %v %v", q, err)
	}
}

// TestInvalidResponses proves malformed and refused results never become vectors.
func TestInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body, media string
		status            int
		want              error
	}{
		{"missing index", `{"model":"fixture","data":[{"embedding":[1,0]}]}`, "application/json", 200, ErrInvalidResponse},
		{"negative index", `{"model":"fixture","data":[{"index":-1,"embedding":[1,0]}]}`, "application/json", 200, ErrInvalidResponse},
		{"out of range", `{"model":"fixture","data":[{"index":1,"embedding":[1,0]}]}`, "application/json", 200, ErrInvalidResponse},
		{"missing vector", `{"model":"fixture","data":[{"index":0}]}`, "application/json", 200, ErrInvalidResponse},
		{"wrong dimension", `{"model":"fixture","data":[{"index":0,"embedding":[1]}]}`, "application/json", 200, ErrInvalidResponse},
		{"zero vector", `{"model":"fixture","data":[{"index":0,"embedding":[0,0]}]}`, "application/json", 200, ErrInvalidResponse},
		{"null component", `{"model":"fixture","data":[{"index":0,"embedding":[null,1]}]}`, "application/json", 200, ErrInvalidResponse},
		{"quoted component", `{"model":"fixture","data":[{"index":0,"embedding":["0",1]}]}`, "application/json", 200, ErrInvalidResponse},
		{"overflow", `{"model":"fixture","data":[{"index":0,"embedding":[1e100,0]}]}`, "application/json", 200, ErrInvalidResponse},
		{"wrong model", `{"model":"other","data":[{"index":0,"embedding":[1,0]}]}`, "application/json", 200, ErrInvalidResponse},
		{"duplicate key", `{"model":"fixture","model":"fixture","data":[]}`, "application/json", 200, ErrInvalidResponse},
		{"trailing document", `{} {}`, "application/json", 200, ErrInvalidResponse},
		{"null", `null`, "application/json", 200, ErrInvalidResponse},
		{"invalid JSON", `{"secret":"private`, "application/json", 200, ErrInvalidResponse},
		{"media", `{}`, "text/html", 200, ErrInvalidResponse},
		{"no content", ``, "application/json", 204, ErrInvalidResponse},
		{"bad input", `private input`, "text/plain", 400, ErrRejected},
		{"auth", `private credential`, "text/plain", 401, ErrAuthentication},
		{"forbidden", `private model`, "text/plain", 403, ErrAuthentication},
		{"limit", `private limit`, "text/plain", 429, ErrUnavailable},
		{"down", `private server`, "text/plain", 503, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			until := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Second)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.status == 429 {
					w.Header().Set("Retry-After", "120")
				}
				if tc.status == 503 {
					w.Header().Set("Retry-After", until.Format(http.TimeFormat))
				}
				w.Header().Set("Content-Type", tc.media)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c, err := New(settings(server.URL + "/v1/embeddings"))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			before := time.Now()
			got, err := c.Query(t.Context(), "private query")
			after := time.Now()
			if got != nil || !errors.Is(err, tc.want) {
				t.Fatalf("got %v %v", got, err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("error leaked input")
			}
			var hint *catalog.EmbeddingRetryError
			if hasHint := errors.As(err, &hint); hasHint != (tc.status == 429 || tc.status == 503) {
				t.Fatalf("status %d lost or invented a hint: %v", tc.status, err)
			}
			if tc.status == 429 && hint.RetryAfter != 2*time.Minute || tc.status == 503 && (hint.RetryAfter < until.Sub(after) || hint.RetryAfter > until.Sub(before)) {
				t.Fatalf("wire hint was not measured at receipt: %v", hint)
			}
			if calls.Load() != 1 {
				t.Fatalf("provider requests=%d, want one", calls.Load())
			}
		})
	}
}

// TestRequestBounds ensures invalid inputs and cancellation never escape to inference.
func TestRequestBounds(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	s := settings(server.URL + "/v1/embeddings")
	s.BatchSize = 1
	s.MaxInputBytes = 64
	c, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, inputs := range [][]string{nil, {"a", "b"}, {" "}, {"\xff"}, {"a\x00b"}, {strings.Repeat("x", 65)}} {
		if _, err := c.Documents(context.Background(), inputs); !errors.Is(err, ErrRejected) {
			t.Fatalf("bounds: %v", err)
		}
	}
	c.queries <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Query(ctx, "bounded"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-c.queries
	if calls.Load() != 0 {
		t.Fatal("invalid request reached server")
	}
}

// TestTLSInputs checks private trust and bearer credentials without a hosted account.
func TestTLSInputs(t *testing.T) {
	var replacement atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := "Bearer fixture-token"
		if replacement.Load() {
			expected = "Bearer replacement-token"
		}
		if r.Header.Get("Authorization") != expected {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"fixture","data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("fixture-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := settings(server.URL + "/v1/embeddings")
	s.AllowHTTP = false
	s.CAFile = ca
	s.TokenFile = token
	c, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Query(context.Background(), "find"); err != nil {
		t.Fatal(err)
	}

	// A projected file update does not reload a client already composed at startup.
	if err := os.WriteFile(token, []byte("replacement-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Query(t.Context(), "find"); err != nil {
		t.Fatal("running client changed credentials before replacement")
	}
	replacement.Store(true)
	if _, err = c.Query(t.Context(), "find"); !errors.Is(err, ErrAuthentication) {
		t.Fatal("revoked startup credential was accepted")
	}
	fresh, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err = fresh.Query(t.Context(), "find"); err != nil {
		t.Fatal("replacement client did not activate the mounted credential")
	}
	if err := os.Remove(token); err != nil {
		t.Fatal(err)
	}
	if _, err = fresh.Query(t.Context(), "find"); err != nil {
		t.Fatal("removing a file unexpectedly revoked an initialized client")
	}
	if missing, err := New(s); err == nil {
		missing.Close()
		t.Fatal("new client accepted a missing credential file")
	}
}

// TestTransportFailures covers forbidden redirects, response bounds and deadlines.
func TestTransportFailures(t *testing.T) {
	for _, mode := range []string{"redirect", "oversize", "timeout", "duplicates"} {
		t.Run(mode, func(t *testing.T) {
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					w.Header().Set("Location", "http://169.254.169.254/secret")
					w.WriteHeader(http.StatusFound)
				case "oversize":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(strings.Repeat("x", (8<<20)+1)))
				case "timeout":
					select {
					case <-r.Context().Done():
					case <-release:
					}
				case "duplicates":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"model":"fixture","data":[{"index":0,"embedding":[1,0]},{"index":0,"embedding":[0,1]}]}`))
				}
			}))
			defer server.Close()
			defer close(release)
			s := settings(server.URL + "/v1/embeddings")
			s.QueryTimeout = config.Duration{Duration: 100 * time.Millisecond}
			c, err := New(s)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			var vectors [][]float32
			if mode == "duplicates" {
				vectors, err = c.Documents(context.Background(), []string{"one", "two"})
			} else {
				var v []float32
				v, err = c.Query(context.Background(), "find")
				if v != nil {
					vectors = [][]float32{v}
				}
			}
			if err == nil || vectors != nil || strings.Contains(err.Error(), "169.254") {
				t.Fatalf("unsafe result %v %v", vectors, err)
			}
		})
	}
}

// TestSeparateWorkLimits proves a saturated indexing budget does not consume a
// query slot, and canceled waiters do not reach the provider or leak a slot.
func TestSeparateWorkLimits(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"fixture","data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	defer server.Close()
	s := settings(server.URL + "/v1/embeddings")
	s.QueryConcurrency = 1
	s.BatchConcurrency = 1
	c, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.batches <- struct{}{}
	if _, err := c.Query(t.Context(), "find"); err != nil {
		t.Fatal("indexing blocked queries", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Documents(ctx, []string{"one"}); !errors.Is(err, context.Canceled) {
		t.Fatal("index waiter cancellation", err)
	}
	<-c.batches
	c.queries <- struct{}{}
	if _, err := c.Query(ctx, "find"); !errors.Is(err, context.Canceled) {
		t.Fatal("query waiter cancellation", err)
	}
	if _, err := c.Documents(t.Context(), []string{"one"}); err != nil {
		t.Fatal("queries blocked indexing", err)
	}
	<-c.queries
	if calls.Load() != 2 || len(c.batches) != 0 || len(c.queries) != 0 {
		t.Fatal("canceled work reached provider or leaked capacity")
	}
}
