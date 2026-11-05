// Unit tests exercise isolated validation and controlled dependencies.
package fetcher

import (
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// adapterBody observes ownership without a socket or background reader.
type adapterBody struct {
	io.Reader
	reads  int
	closes int
}

func (b *adapterBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}

func (b *adapterBody) Close() error { b.closes++; return nil }

func TestTelemetryRejectedTargetClosesBodyWithoutReading(t *testing.T) {
	const endpoint = "https://collector.example/v1/traces"
	for _, test := range []struct{ name, method, target string }{
		{"method", http.MethodGet, endpoint},
		{"origin", http.MethodPost, "https://other.example/v1/traces"},
		{"path", http.MethodPost, endpoint + "/other"},
		{"query", http.MethodPost, endpoint + "?private=value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewTelemetryClient(Policy{Timeout: time.Second, MaxResponseBytes: 1024}, endpoint, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.CloseIdleConnections)
			transport, ok := client.Transport.(telemetryTransport)
			if !ok {
				t.Fatal("telemetry client bypassed the endpoint-bound transport")
			}
			transport.fetcher.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("rejected target reached the outbound transport")
				return nil, errors.New("unexpected outbound call")
			})
			body := &adapterBody{Reader: strings.NewReader("private-body")}
			request, err := http.NewRequestWithContext(t.Context(), test.method, test.target, body)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(request)
			if response != nil {
				defer func() { _ = response.Body.Close() }()
			}
			if response != nil || err == nil || err.Error() != "telemetry request target is not permitted" {
				t.Fatalf("target rejection = %v, %v", response, err)
			}
			if body.closes != 1 || body.reads != 0 {
				t.Fatalf("rejected request body: closes=%d reads=%d", body.closes, body.reads)
			}
		})
	}
}

func TestTelemetryRoundTripRejectsUnsafeInputBeforeTransport(t *testing.T) {
	const endpoint = "https://collector.example/v1/traces"
	for _, test := range []struct {
		name   string
		reader io.Reader
		header http.Header
		want   string
	}{
		{"oversized body", strings.NewReader(strings.Repeat("x", 1025)), nil, "telemetry request exceeds its bound"},
		{"read failure", errorReadCloser{err: errors.New("private read failure")}, nil, "telemetry request exceeds its bound"},
		{"duplicate header", strings.NewReader("x"), http.Header{"Authorization": {"first", "second"}}, "telemetry request header is invalid"},
		{"empty header values", strings.NewReader("x"), http.Header{"Authorization": {}}, "telemetry request header is invalid"},
		{"propagation header", strings.NewReader("x"), http.Header{"Traceparent": {"private trace"}}, "telemetry transport failed"},
		{"header injection", strings.NewReader("x"), http.Header{"Authorization": {"private\r\nvalue"}}, "telemetry transport failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewTelemetryClient(Policy{Timeout: time.Second, MaxResponseBytes: 1024}, endpoint, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.CloseIdleConnections)
			transport, ok := client.Transport.(telemetryTransport)
			if !ok {
				t.Fatal("telemetry client bypassed the endpoint-bound transport")
			}
			transport.fetcher.resolver = errorResolver{err: errors.New("unexpected DNS lookup")}
			transport.fetcher.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("unsafe exporter input reached the outbound transport")
				return nil, errors.New("unexpected outbound call")
			})
			body := &adapterBody{Reader: test.reader}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, body)
			if err != nil {
				t.Fatal(err)
			}
			if test.header != nil {
				request.Header = test.header
			}
			response, err := transport.RoundTrip(request)
			if response != nil {
				defer func() { _ = response.Body.Close() }()
			}
			if response != nil || err == nil || err.Error() != test.want || body.closes != 1 {
				t.Fatalf("input rejection: response=%v error=%v body closes=%d", response, err, body.closes)
			}
		})
	}
}

func TestTelemetryRoundTripProjectsBoundedResponse(t *testing.T) {
	const endpoint = "https://COLLECTOR.example/v1/traces"
	const requestLimit = 128 << 10
	const responseLimit = 64 << 10
	for _, test := range []struct {
		name   string
		input  string
		status int
	}{
		{"exact bounds", strings.Repeat("x", requestLimit), http.StatusOK},
		{"empty request", "", http.StatusOK},
		{"http error retained", "trace", http.StatusServiceUnavailable},
		{"redirect refused", "trace", http.StatusTemporaryRedirect},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewTelemetryClient(Policy{Timeout: time.Second, MaxResponseBytes: requestLimit, MaxRedirects: 2, UserAgent: "catalog-test"}, endpoint, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.CloseIdleConnections)
			transport, ok := client.Transport.(telemetryTransport)
			if !ok {
				t.Fatal("telemetry client bypassed the endpoint-bound transport")
			}
			transport.fetcher.resolver = errorResolver{err: errors.New("unexpected DNS lookup")}
			responseBytes := strings.Repeat("y", responseLimit)
			responseBody := &adapterBody{Reader: strings.NewReader(responseBytes)}
			calls := 0
			transport.fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				var raw []byte
				if request.Body != nil {
					defer func() { _ = request.Body.Close() }()
					var readErr error
					raw, readErr = io.ReadAll(request.Body)
					if readErr != nil {
						t.Fatal(readErr)
					}
				}
				if string(raw) != test.input || request.Method != http.MethodPost || request.URL.String() != "https://collector.example/v1/traces" {
					t.Fatal("export method, normalized target or body changed")
				}
				if test.input != "" && request.Header.Get("Content-Type") != "application/x-protobuf" {
					t.Fatal("export content type changed")
				}
				if request.Header.Get("Authorization") != "Bearer synthetic-token" || request.Header.Get("X-Collector-Tenant") != "test-installation" || request.Header.Get("User-Agent") != "catalog-test" || request.Header.Get("Content-Length") != "" || request.ContentLength != int64(len(test.input)) {
					t.Fatal("export credentials, headers or derived framing changed")
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": {"application/x-protobuf"}, "Retry-After": {"7"}, "Set-Cookie": {"private-cookie"}, "Location": {"https://other.example/traces"}}, Body: responseBody, Request: request}, nil
			})
			requestBody := &adapterBody{Reader: strings.NewReader(test.input)}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, requestBody)
			if err != nil {
				t.Fatal(err)
			}
			if test.input == "" {
				request.Body = nil
			}
			request.Header = http.Header{"Content-Type": {"application/x-protobuf"}, "Content-Length": {"999999"}, "User-Agent": {"sdk"}, "Authorization": {"Bearer synthetic-token"}, "X-Collector-Tenant": {"test-installation"}}
			response, err := transport.RoundTrip(request)
			if response != nil {
				defer func() { _ = response.Body.Close() }()
			}
			if test.status == http.StatusTemporaryRedirect {
				if response != nil || err == nil || err.Error() != "telemetry transport failed" || calls != 1 || responseBody.closes != 1 || requestBody.closes != 1 {
					t.Fatalf("redirect refusal: calls=%d closes=%d/%d response=%v error=%v", calls, requestBody.closes, responseBody.closes, response, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(response.Body)
			if err != nil || string(raw) != responseBytes || response.StatusCode != test.status || response.ContentLength != int64(len(responseBytes)) || response.Request != request {
				t.Fatalf("export response projection: status=%d bytes=%d error=%v", response.StatusCode, len(raw), err)
			}
			if len(response.Header) != 2 || response.Header.Get("Content-Type") != "application/x-protobuf" || response.Header.Get("Retry-After") != "7" {
				t.Fatalf("unexpected exporter response headers: %v", response.Header)
			}
			if calls != 1 || responseBody.closes != 1 || (request.Body != nil && requestBody.closes != 1) || request.Header.Get("User-Agent") != "sdk" {
				t.Fatal("request ownership, response cleanup or single-call boundary changed")
			}
		})
	}
}

func TestTelemetryRoundTripSanitizesTransportAndResponseFailures(t *testing.T) {
	const endpoint = "https://collector.example/v1/traces"
	for _, mode := range []string{"transport", "read", "response bound"} {
		t.Run(mode, func(t *testing.T) {
			// The adapter's 64 KiB response cap must win over the wider policy.
			client, err := NewTelemetryClient(Policy{Timeout: time.Second, MaxResponseBytes: 128 << 10}, endpoint, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.CloseIdleConnections)
			transport, ok := client.Transport.(telemetryTransport)
			if !ok {
				t.Fatal("telemetry client bypassed the endpoint-bound transport")
			}
			transport.fetcher.resolver = errorResolver{err: errors.New("unexpected DNS lookup")}
			privateErr := errors.New("private collector error")
			responseBody := &adapterBody{Reader: strings.NewReader(strings.Repeat("y", (64<<10)+1))}
			calls := 0
			transport.fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				defer func() { _ = request.Body.Close() }()
				if mode == "transport" {
					return nil, privateErr
				}
				if mode == "read" {
					responseBody.Reader = errorReadCloser{err: privateErr}
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: responseBody, Request: request}, nil
			})
			body := &adapterBody{Reader: strings.NewReader("trace")}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, body)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(request)
			if response != nil {
				defer func() { _ = response.Body.Close() }()
			}
			if response != nil || err == nil || err.Error() != "telemetry transport failed" || errors.Is(err, privateErr) || body.closes != 1 || calls != 1 {
				t.Fatalf("collector failure: response=%v error=%v body closes=%d", response, err, body.closes)
			}
			if mode != "transport" && responseBody.closes != 1 {
				t.Fatalf("failed collector response body closes=%d", responseBody.closes)
			}
		})
	}
}

func TestTelemetryClientRejectsInvalidPolicy(t *testing.T) {
	client, err := NewTelemetryClient(Policy{MaxResponseBytes: 1024}, "https://collector.example/v1/traces", false)
	if client != nil || err == nil {
		t.Fatalf("invalid telemetry policy = %v, %v", client, err)
	}
}

func TestTelemetryHeaderNamePolicy(t *testing.T) {
	for _, name := range []string{"bad header", strings.Repeat("x", 65), "pRoXy-Custom", "sEc-Custom", "baggage", "Content-Length"} {
		if ValidTelemetryHeader(name) {
			t.Errorf("unsafe header accepted: %q", name)
		}
	}
	for _, name := range []string{"Authorization", "X-Collector-Tenant", strings.Repeat("x", 64)} {
		if !ValidTelemetryHeader(name) {
			t.Errorf("permitted header rejected: %q", name)
		}
	}
}

func TestTelemetryPrivatePolicyRejectsOutsideAndMixedDNSAnswers(t *testing.T) {
	policy := Policy{Timeout: time.Second, MaxResponseBytes: 1 << 20, AllowedAddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	client, err := NewTelemetryClient(policy, "http://collector.example/v1/traces", true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	transport, ok := client.Transport.(telemetryTransport)
	if !ok {
		t.Fatal("telemetry client bypassed hardened transport")
	}
	fetch := transport.fetcher
	if !fetch.addressAllowed(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("approved loopback collector denied")
	}
	for _, addresses := range [][]string{{"8.8.8.8"}, {"10.0.0.1"}, {"127.0.0.1", "8.8.8.8"}, {"127.0.0.1", "169.254.169.254"}} {
		values := make([]netip.Addr, 0, len(addresses))
		for _, address := range addresses {
			values = append(values, netip.MustParseAddr(address))
		}
		fetch.resolver = fixedResolver{addresses: values}
		// Every answer is checked before any dial, including an otherwise
		// permitted address preceding a denied answer in the same DNS reply.
		connection, err := fetch.dialContext(canceledDialContext(t), "tcp", "collector.example:4318")
		if connection != nil {
			_ = connection.Close()
			t.Fatal("denied answer reached dial")
		}
		if !errors.Is(err, ErrPolicyDenied) {
			t.Fatalf("DNS boundary: %v", err)
		}
	}
	if _, err := NewTelemetryClient(policy, "http://8.8.8.8/v1/traces", true); err == nil {
		t.Fatal("public plaintext literal accepted")
	}
	ordinary, err := New(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.addressAllowed(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("telemetry loopback permission leaked into ordinary fetcher")
	}
}
