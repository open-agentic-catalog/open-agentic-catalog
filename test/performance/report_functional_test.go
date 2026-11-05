//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestPerformancePrivateReportSurvivesFailedWork(t *testing.T) {
	baseline := validPage(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) > 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
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
	path := filepath.Join(t.TempDir(), "workload.json")
	args := []string{writeProfile(t, raw), path}
	var stdout, stderr bytes.Buffer
	if code := execute(args, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatalf("failed child code=%d stdout=%q", code, &stdout)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test-owned report in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	var output report
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	if output.Verdict != "failed" || output.Measured.Outcomes.Status == 0 || output.Measured.Outcomes.Status != output.Measured.Failed || output.Measured.HTTPFailures["503"] != output.Measured.Failed || !output.Measured.Complete {
		t.Fatalf("missing failed aggregate: %+v", output)
	}
	if output.Measured.FinishedAt < output.Measured.IssuanceEndedAt || output.Measured.IssuanceEndedAt-output.Measured.StartedAt < 1000 {
		t.Fatal("phase boundaries were not retained")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("report permissions")
	}
	before := calls.Load()
	if code := execute(args, &stdout, &stderr); code != 2 || calls.Load() != before {
		t.Fatal("existing report overwritten or workload started")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	args[1] = link
	if code := execute(args, &stdout, &stderr); code != 2 || calls.Load() != before {
		t.Fatal("report symlink followed")
	}
}

func TestPerformanceRequestFailureClassification(t *testing.T) {
	for _, item := range []struct {
		status            int
		contentType, body string
		want              string
	}{
		{200, "text/plain", "body", "body"}, {503, "application/json", "{}", "status"},
	} {
		t.Run(item.want, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", item.contentType)
				w.WriteHeader(item.status)
				_, _ = w.Write([]byte(item.body))
			}))
			defer server.Close()
			client := newClient()
			defer client.CloseIdleConnections()
			_, err := search(t.Context(), client, serverPort(t, server))
			if err == nil || err.Error() != item.want {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
