//go:build functional

package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPublicDeliveryMeasuresFullAndConditionalResponses(t *testing.T) {
	body, public := publicFixture(t)
	var full, conditional atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/.well-known/ard.json" || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
			t.Error("public measurement gained another request surface")
		}
		w.Header().Set("ETag", public.ETag)
		if r.Header.Get("If-None-Match") == public.ETag {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	for _, cached := range []bool{false, true} {
		selected := *public
		selected.Conditional = cached
		p := profile{Scenario: "public", Public: &selected, Port: serverPort(t, server), Seconds: 1, Workers: 2, MaxRequests: 1000}
		beforeFull, beforeConditional := full.Load(), conditional.Load()
		report, err := run(t.Context(), p)
		if err != nil || report.Verdict != "passed" || report.BaselineResults != 1001 || report.ResponseDigest != public.Digest || report.Measured.Failed != 0 || report.Measured.Count == 0 {
			t.Fatalf("conditional=%v: %+v %v", cached, report, err)
		}
		succeeded := report.Measured.Succeeded
		if succeeded < 0 || succeeded > 1000 {
			t.Fatal("successful count exceeds the request bound")
			return
		}
		if cached {
			if report.Measured.SuccessfulResponseBytes != 0 || conditional.Load()-beforeConditional != int64(report.Measured.Count) || full.Load()-beforeFull != 1 {
				t.Fatal("conditional phase did not retain exactly one full baseline and empty 304 samples")
			}
		} else if report.Measured.SuccessfulResponseBytes != uint64(succeeded)*uint64(len(body)) || conditional.Load() != beforeConditional || full.Load()-beforeFull != int64(report.Measured.Count)+1 {
			t.Fatal("full response bytes are not exact")
		}
	}
}
