// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPerformanceProfileScenarioAndBoundaryValidation(t *testing.T) {
	t.Parallel()
	base := profile{Port: 1, Seconds: 1, Workers: 1, MaxRequests: 1}
	for _, scenario := range []string{"", "local", "indexed", "public", "mixed"} {
		p := base
		p.Scenario = scenario
		if scenario == "indexed" {
			p.Query = strings.Repeat("q", 80)
		}
		if err := p.validate(); err != nil {
			t.Fatalf("valid scenario %q: %v", scenario, err)
		}
	}
	maximum := profile{Port: 65535, WarmupSeconds: 60, Seconds: 3600, Workers: 8, MaxRequests: 1000000}
	if err := maximum.validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*profile){
		"scenario":              func(p *profile) { p.Scenario = "unknown" },
		"query outside indexed": func(p *profile) { p.Query = "query" },
		"query length":          func(p *profile) { p.Scenario, p.Query = "indexed", strings.Repeat("q", 81) },
		"query newline":         func(p *profile) { p.Scenario, p.Query = "indexed", "q\n" },
		"query carriage return": func(p *profile) { p.Scenario, p.Query = "indexed", "q\r" },
		"query null byte":       func(p *profile) { p.Scenario, p.Query = "indexed", "q\x00" },
		"port zero":             func(p *profile) { p.Port = 0 },
		"port overflow":         func(p *profile) { p.Port = 65536 },
		"negative warmup":       func(p *profile) { p.WarmupSeconds = -1 },
		"warmup overflow":       func(p *profile) { p.WarmupSeconds = 61 },
		"duration zero":         func(p *profile) { p.Seconds = 0 },
		"duration overflow":     func(p *profile) { p.Seconds = 3601 },
		"workers zero":          func(p *profile) { p.Workers = 0 },
		"workers overflow":      func(p *profile) { p.Workers = 9 },
		"quota below workers":   func(p *profile) { p.Workers = 2 },
		"quota overflow":        func(p *profile) { p.MaxRequests = 1000001 },
	} {
		t.Run(name, func(t *testing.T) {
			p := base
			mutate(&p)
			if err := p.validate(); err == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}
}

type performanceRoundTrip func(*http.Request) (*http.Response, error)

func (f performanceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type performanceFailedBody struct {
	err    error
	closed bool
}

func (b *performanceFailedBody) Read([]byte) (int, error) { return 0, b.err }

func (b *performanceFailedBody) Close() error { b.closed = true; return nil }

func TestPerformancePhaseFailureAccountingAndBodyClosure(t *testing.T) {
	t.Parallel()
	for _, item := range []struct {
		name                  string
		transportErr, bodyErr error
		want                  outcomes
	}{
		{"dial", errors.New("private dial failure"), nil, outcomes{Transport: 1}},
		{"header timeout", context.DeadlineExceeded, nil, outcomes{Timeout: 1}},
		{"header cancellation", context.Canceled, nil, outcomes{Canceled: 1}},
		{"body read", nil, io.ErrUnexpectedEOF, outcomes{Body: 1}},
		{"body timeout", nil, context.DeadlineExceeded, outcomes{Timeout: 1}},
		{"body cancellation", nil, context.Canceled, outcomes{Canceled: 1}},
	} {
		t.Run(item.name, func(t *testing.T) {
			body := &performanceFailedBody{err: item.bodyErr}
			client := &http.Client{Transport: performanceRoundTrip(func(*http.Request) (*http.Response, error) {
				if item.transportErr != nil {
					return nil, item.transportErr
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
			})}
			result := phase(t.Context(), client, profile{Port: 1, Workers: 1, MaxRequests: 1}, 60, [32]byte{})
			if result.Count != 1 || result.Failed != 1 || result.Succeeded != 0 || result.Outcomes != item.want || len(result.HTTPFailures) != 0 || result.Complete {
				t.Fatalf("failure accounting or quota completeness changed: %+v", result)
			}
			if item.bodyErr != nil && !body.closed {
				t.Fatal("failed response body was not closed")
			}
		})
	}
}

func TestPerformanceMixedPhaseRevalidatesChangingResponses(t *testing.T) {
	t.Parallel()
	baseline := validPage(t)
	for _, item := range []struct {
		name, scenario string
		invalid        bool
		failures       int
	}{
		{"mixed generations", "mixed", false, 0},
		{"static generation changed", "local", false, 3},
		{"mixed wrong tier", "mixed", true, 1},
	} {
		t.Run(item.name, func(t *testing.T) {
			calls := 0
			var expectedBytes uint64
			client := &http.Client{Transport: performanceRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				var page catalog.CatalogSearchResponse
				if err := json.Unmarshal(baseline, &page); err != nil {
					return nil, err
				}
				page.Generation = strings.Repeat("2", calls)
				if item.invalid && calls == 2 {
					page.Tier = "unsupported"
				}
				raw, err := json.Marshal(page)
				if err != nil {
					return nil, err
				}
				if item.scenario != "local" && !(item.invalid && calls == 2) {
					expectedBytes += uint64(len(raw))
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}
			result := phase(t.Context(), client, profile{Port: 1, Workers: 1, MaxRequests: 3, Scenario: item.scenario}, 60, sha256.Sum256(baseline))
			if calls != 3 || result.Count != 3 || result.Succeeded != 3-item.failures || result.Failed != item.failures || result.Outcomes != (outcomes{Correctness: item.failures}) || result.Complete || result.SuccessfulResponseBytes != expectedBytes {
				t.Fatalf("response validation accounting changed: %+v", result)
			}
		})
	}
}
