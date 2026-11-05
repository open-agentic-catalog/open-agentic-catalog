package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTracingReceiverBoundsAndControl(t *testing.T) {
	s := new(traceReceiver)
	send := func(raw []byte, header string) int {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/traces", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/x-protobuf")
		r.Header.Set("Authorization", "Bearer tracing-fixture")
		if header != "" {
			r.Header.Set(header, "private")
		}
		w := httptest.NewRecorder()
		s.receive(w, r)
		return w.Code
	}
	if send([]byte{1}, "") != 200 || len(s.batches) != 1 {
		t.Fatal("valid batch not retained")
	}
	for _, h := range []string{"Traceparent", "Tracestate", "Baggage", "Authorization", "Content-Type"} {
		if send(nil, h) != 400 {
			t.Errorf("header boundary: %s", h)
		}
	}
	if send(make([]byte, (1<<20)+1), "") != 413 {
		t.Fatal("oversize batch accepted")
	}
	for _, body := range []string{`{"reset":true,"reject":true}`, `{"reject":false}`} {
		w := httptest.NewRecorder()
		s.control(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/tracing/control", strings.NewReader(body)))
		if w.Code != 204 {
			t.Fatal("control rejected")
		}
		if s.reject && (len(s.batches) != 0 || send(nil, "") != 503) {
			t.Fatal("outage/reset failed")
		}
	}
	for _, body := range []string{`{"unknown":true}`, `{} {}`, strings.Repeat("x", 1025)} {
		w := httptest.NewRecorder()
		s.control(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/tracing/control", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal("invalid control accepted")
		}
	}
	s.bytes = 16 << 20
	if send([]byte{1}, "") != 503 {
		t.Fatal("capture byte cap ignored")
	}
	s.bytes = 0
	s.batches = make([][]byte, 2048)
	if send(nil, "") != 503 {
		t.Fatal("capture count cap ignored")
	}
}
