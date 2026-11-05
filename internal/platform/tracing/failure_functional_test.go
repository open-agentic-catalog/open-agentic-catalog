//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package tracing

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func tlsConfig(t *testing.T, server *httptest.Server) config.Tracing {
	t.Helper()
	c := config.Defaults().Tracing
	c.Enabled, c.SampleRatio, c.Installation = true, 1, "failure-fixture"
	c.Endpoint = server.URL + "/v1/traces"
	c.AllowedAddressPrefixes = []string{"127.0.0.0/8"}
	c.CAFile = inputFile(t, "ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	return c
}

func TestExporterFailureContracts(t *testing.T) {
	for _, scenario := range []string{"partial", "malformed", "oversized", "rejected", "retry", "retry-after", "redirect", "untrusted-tls", "private-address-denied"} {
		t.Run(scenario, func(t *testing.T) {
			var calls, redirects atomic.Int32
			target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirects.Add(1) }))
			defer target.Close()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				w.Header().Set("Content-Type", "application/x-protobuf")
				switch scenario {
				case "partial":
					raw, _ := proto.Marshal(&collector.ExportTraceServiceResponse{PartialSuccess: &collector.ExportTracePartialSuccess{RejectedSpans: 1, ErrorMessage: "fixture-secret"}})
					_, _ = w.Write(raw)
				case "malformed":
					_, _ = io.WriteString(w, "fixture-secret")
				case "oversized":
					_, _ = io.WriteString(w, strings.Repeat("x", 65<<10))
				case "rejected":
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, "fixture-secret")
				case "retry":
					if n == 1 {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
				case "retry-after":
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(http.StatusTooManyRequests)
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				}
			}))
			defer server.Close()
			c := tlsConfig(t, server)
			c.ExportTimeout.Duration = 3 * time.Second
			if scenario == "untrusted-tls" {
				c.CAFile = ""
			}
			if scenario == "private-address-denied" {
				c.AllowedAddressPrefixes = nil
			}
			var logs bytes.Buffer
			metrics := observability.New(time.Now)
			p, err := New(t.Context(), c, config.RoleAPI, "fixture", "00000000-0000-7000-8000-000000000001", slog.New(slog.NewJSONHandler(&logs, nil)), metrics)
			if scenario == "private-address-denied" {
				if err == nil || calls.Load() != 0 {
					t.Fatal("denied address reached collector")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, end := p.Start(t.Context(), "fixture", catalog.TraceServer, "")
			end("ok")
			start := time.Now()
			flush, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			err = p.provider.ForceFlush(flush)
			if scenario == "retry" {
				if err != nil || calls.Load() != 2 {
					t.Fatalf("retry: calls=%d err=%v", calls.Load(), err)
				}
			} else if err == nil {
				t.Fatal("export failure not observable")
			}
			if time.Since(start) > 4500*time.Millisecond || redirects.Load() != 0 || stringsContainSecrets(logs.String()) {
				t.Fatal("export containment failed")
			}
			if scenario != "retry" && calls.Load() > 1 {
				t.Fatal("non-retryable response retried")
			}
			want := "open_agentic_catalog_trace_export_failures_total 1"
			if scenario == "retry" {
				want = "open_agentic_catalog_trace_export_failures_total 0"
			}
			if !bytes.Contains(metrics.Render(t.Context()), []byte(want)) {
				t.Fatal("failure accounting")
			}
			_ = p.Shutdown(flush)
		})
	}
}

func TestQueueIsNonBlockingAndShutdownBounded(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	c := tlsConfig(t, server)
	c.QueueSize, c.BatchSize = 16, 1
	p, err := New(t.Context(), c, config.RoleAPI, "fixture", "00000000-0000-7000-8000-000000000001", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, end := p.Start(t.Context(), "fixture", catalog.TraceServer, "")
	end("ok")
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("export did not start")
	}
	start := time.Now()
	for range 10000 {
		_, end := p.Start(t.Context(), "fixture", catalog.TraceServer, "")
		end("ok")
	}
	if time.Since(start) > time.Second {
		t.Fatal("saturated queue blocked application")
	}
	shutdown, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start = time.Now()
	_ = p.Shutdown(shutdown)
	if time.Since(start) > time.Second {
		t.Fatal("shutdown restarted its budget")
	}
}
