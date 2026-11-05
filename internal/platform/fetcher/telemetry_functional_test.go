//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package fetcher

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type telemetryTestBody struct {
	io.Reader
	closed bool
}

func (b *telemetryTestBody) Close() error { b.closed = true; return nil }

type telemetryErrorReader struct{}

func (telemetryErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("private-read-failure")
}

func TestTelemetryRequestBoundsBeforeNetwork(t *testing.T) {
	const limit = 1024
	validBody := strings.Repeat("x", limit)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, err := io.ReadAll(r.Body)
		if err != nil || string(raw) != validBody {
			t.Error("exact-bound body changed")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	endpoint := server.URL + "/v1/traces"
	client, err := NewTelemetryClient(Policy{Timeout: time.Second, MaxResponseBytes: limit, AllowedAddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}, endpoint, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	for _, test := range []struct {
		name      string
		reader    io.Reader
		duplicate bool
		want      string
	}{
		{"oversized", strings.NewReader(validBody + "x"), false, "telemetry request exceeds its bound"},
		{"read failure", telemetryErrorReader{}, false, "telemetry request exceeds its bound"},
		{"duplicate header", strings.NewReader(validBody), true, "telemetry request header is invalid"},
		{"exact bound", strings.NewReader(validBody), false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &telemetryTestBody{Reader: test.reader}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, body)
			if test.duplicate {
				req.Header["Authorization"] = []string{"private-first", "private-second"}
			}
			before := calls.Load()
			res, err := client.Transport.RoundTrip(req)
			if res != nil {
				defer func() { _ = res.Body.Close() }()
			}
			if !body.closed {
				t.Fatal("request body was not closed")
			}
			if test.want != "" {
				if err == nil || err.Error() != test.want || res != nil || calls.Load() != before {
					t.Fatalf("rejection leaked input or reached network: %v", err)
				}
			} else if err != nil || res.StatusCode != http.StatusNoContent || calls.Load() != before+1 {
				t.Fatalf("exact-bound request rejected: %v", err)
			}
		})
	}
}

func TestTelemetryEndpointPreservesApprovedSpelling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Error("wrong export path")
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	endpoint := strings.Replace(server.URL, "127.0.0.1", "LOCALHOST", 1) + "/v1/traces"
	client, err := NewTelemetryClient(Policy{Timeout: time.Second, MaxResponseBytes: 1 << 20, AllowedAddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}}, endpoint, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	for _, path := range []string{endpoint, endpoint + "/other"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(""))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-protobuf")
		res, err := client.Do(req)
		if path != endpoint {
			if err == nil {
				_ = res.Body.Close()
				t.Fatal("changed target accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal("normalized approved endpoint rejected:", err)
		}
		if res.StatusCode != http.StatusOK {
			t.Errorf("export status %d", res.StatusCode)
		}
		_ = res.Body.Close()
	}
}
