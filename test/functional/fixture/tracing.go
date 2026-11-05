// Tracing fixtures capture bounded OTLP batches and simulate collector rejection.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

// traceReceiver retains bounded opaque protobuf batches. The external test
// driver decodes them using the official OTLP bindings; this image stays stdlib-only.
type traceReceiver struct {
	mu      sync.Mutex
	batches [][]byte
	bytes   int
	reject  bool
}

// registerTracingFixture attaches one shared capture/control state to the test mux.
func registerTracingFixture(mux *http.ServeMux) {
	receiver := new(traceReceiver)
	mux.HandleFunc("POST /v1/traces", receiver.receive)
	mux.HandleFunc("GET /tracing/state", receiver.report)
	mux.HandleFunc("POST /tracing/control", receiver.control)
}

// receive checks fixture authentication and export headers, then retains a bounded
// opaque batch under the mutex; the external decoder checks protobuf contents.
func (s *traceReceiver) receive(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/x-protobuf" || r.Header.Get("Authorization") != "Bearer tracing-fixture" || r.Header.Get("Traceparent") != "" || r.Header.Get("Tracestate") != "" || r.Header.Get("Baggage") != "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reject || s.bytes+len(raw) > 16<<20 || len(s.batches) >= 2048 {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	s.batches = append(s.batches, raw)
	s.bytes += len(raw)
	w.Header().Set("Content-Type", "application/x-protobuf")
}

// report returns the current capture while preventing concurrent slice mutation.
func (s *traceReceiver) report(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Batches [][]byte `json:"batches"`
	}{s.batches}); err != nil {
		return
	}
}

// control changes rejection behavior for the owned collector-failure scenario.
func (s *traceReceiver) control(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Reject bool `json:"reject"`
		Reset  bool `json:"reset"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reject = input.Reject
	if input.Reset {
		s.batches = nil
		s.bytes = 0
	}
	w.WriteHeader(http.StatusNoContent)
}
