//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPerformanceCLIReportsSuccessAndFailure(t *testing.T) {
	baseline := validPage(t)
	var invalid atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if invalid.Load() {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write(baseline)
	}))
	defer server.Close()
	p := profile{Port: serverPort(t, server), Seconds: 1, Workers: 1, MaxRequests: 1000}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{writeProfile(t, raw)}
	var stdout, stderr bytes.Buffer
	if code := execute(args, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, &stderr)
	}
	var result report
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Measured.Count == 0 || result.Measured.Count != result.Measured.Succeeded || result.Measured.Failed != 0 || result.Profile != p {
		t.Fatalf("invalid successful report: %+v %v", result, err)
	}
	invalid.Store(true)
	stdout.Reset()
	if code := execute(args, &stdout, &stderr); code != 1 || stderr.String() != "performance-workload-incomplete\n" {
		t.Fatalf("code=%d stderr=%q", code, &stderr)
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Measured.Count != 0 {
		t.Fatalf("failed baseline fabricated work: %+v %v", result, err)
	}
	stderr.Reset()
	if code := execute(args, failedReportWriter{}, &stderr); code != 1 || stderr.Len() != 0 {
		t.Fatalf("report write failure code=%d stderr=%q", code, &stderr)
	}
}

func TestPerformancePhaseCountsChangedResponsesAndExactQuota(t *testing.T) {
	baseline := validPage(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1)%2 == 0 {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write(baseline)
	}))
	defer server.Close()
	client := newClient()
	defer client.CloseIdleConnections()
	p := profile{Port: serverPort(t, server), Workers: 2, MaxRequests: 5}
	result := phase(t.Context(), client, p, 60, sha256.Sum256(baseline))
	if result.Count != 5 || result.Succeeded != 3 || result.Failed != 2 || calls.Load() != 5 || result.LatencySecondsTotal <= 0 || result.SuccessfulResponseBytes != uint64(3*len(baseline)) {
		t.Fatalf("quota or outcomes not reconciled: %+v calls=%d", result, calls.Load())
	}
	for i := 1; i < len(result.Buckets); i++ {
		if result.Buckets[i] < result.Buckets[i-1] {
			t.Fatal("latency buckets are not cumulative")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result := phase(ctx, client, p, 60, sha256.Sum256(baseline)); result.Count != 0 || calls.Load() != 5 {
		t.Fatal("canceled phase started new work")
	}
}

type failedReportWriter struct{}

func (failedReportWriter) Write([]byte) (int, error) { return 0, errors.New("private sink failure") }
