// Embedding-specific boundary tests complement the shared fetcher regressions.
package fetcher

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEmbeddingEndpointRejectsInvalidPolicy(t *testing.T) {
	endpoint, err := NewEmbeddingEndpoint(Policy{MaxResponseBytes: 1024}, "https://model.example/v1/embeddings", false)
	if endpoint != nil || err == nil || err.Error() != "invalid embedding transport policy" {
		t.Fatalf("invalid policy = %v, %v", endpoint, err)
	}
}

func TestEmbeddingEndpointMapsRequestsAndPreservesResponses(t *testing.T) {
	const target = "https://model.example/v1/embeddings"
	const input = `{"model":"test-model","input":["test text"]}`
	for _, test := range []struct {
		name, token string
		status      int
	}{
		{"anonymous", "", http.StatusOK},
		{"authenticated failure", "synthetic-token", http.StatusTooManyRequests},
		{"redirect refused", "synthetic-token", http.StatusTemporaryRedirect},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint, err := NewEmbeddingEndpoint(Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2}, target, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(endpoint.Close)
			endpoint.fetcher.resolver = errorResolver{err: errors.New("unexpected DNS lookup")}
			body := &adapterBody{Reader: strings.NewReader(`{"untrusted":true}`)}
			calls := 0
			endpoint.fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				defer func() { _ = request.Body.Close() }()
				raw, readErr := io.ReadAll(request.Body)
				if readErr != nil || string(raw) != input || request.Method != http.MethodPost || request.URL.String() != target || request.Header.Get("Content-Type") != "application/json" {
					t.Fatalf("embedding request changed: %s %s, body=%q, error=%v", request.Method, request.URL, raw, readErr)
				}
				wantAuth := ""
				if test.token != "" {
					wantAuth = "Bearer " + test.token
				}
				if request.Header.Get("Authorization") != wantAuth {
					t.Fatal("embedding credential was not scoped to the endpoint request")
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Retry-After": {"5"}, "Location": {"https://other.example/embeddings"}}, Body: body, Request: request}, nil
			})
			result, err := endpoint.Post(t.Context(), []byte(input), test.token)
			if test.status == http.StatusTemporaryRedirect {
				if err == nil || err.Error() != "embedding transport failed" || !reflect.DeepEqual(result, Result{}) || calls != 1 || body.closes != 1 {
					t.Fatalf("redirect refusal: calls=%d closes=%d result=%#v error=%v", calls, body.closes, result, err)
				}
				return
			}
			if err != nil || result.Status != test.status || result.MediaType != "application/json" || string(result.Body) != `{"untrusted":true}` || result.RetryAfter != "5" {
				t.Fatalf("embedding response projection = %#v, %v", result, err)
			}
			if calls != 1 || body.closes != 1 {
				t.Fatalf("outbound calls=%d, response body closes=%d", calls, body.closes)
			}
		})
	}
}

func TestEmbeddingEndpointSanitizesFailuresAndPreservesCancellation(t *testing.T) {
	for _, mode := range []string{"transport", "read", "oversized", "cancelled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			endpoint, err := NewEmbeddingEndpoint(Policy{Timeout: time.Second, MaxResponseBytes: 1024}, "https://model.example/v1/embeddings", false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(endpoint.Close)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, time.Unix(0, 0))
				defer stop()
			}
			privateErr := errors.New("private provider error")
			body := &adapterBody{Reader: strings.NewReader(strings.Repeat("x", 1025))}
			calls := 0
			endpoint.fetcher.resolver = errorResolver{err: errors.New("unexpected DNS lookup")}
			endpoint.fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Body != nil {
					defer func() { _ = request.Body.Close() }()
				}
				switch mode {
				case "transport":
					return nil, privateErr
				case "cancelled":
					cancel()
					return nil, privateErr
				case "deadline":
					return nil, request.Context().Err()
				case "read":
					body.Reader = errorReadCloser{err: privateErr}
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: request}, nil
			})
			result, err := endpoint.Post(ctx, []byte(`{}`), "synthetic-token")
			if err == nil || !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("failed embedding result = %#v, %v", result, err)
			}
			switch mode {
			case "cancelled":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation cause = %v", err)
				}
			case "deadline":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("deadline cause = %v", err)
				}
			default:
				if err.Error() != "embedding transport failed" || errors.Is(err, privateErr) {
					t.Fatalf("private dependency error escaped: %v", err)
				}
			}
			if mode != "deadline" && calls != 1 {
				t.Fatalf("expected one outbound attempt, got %d", calls)
			}
			if (mode == "read" || mode == "oversized") && body.closes != 1 {
				t.Fatalf("failed response body closes=%d", body.closes)
			}
		})
	}
}

// TestEmbeddingPrivateDNS checks all answers before dialing, including rebinding
// to public/metadata addresses, without widening ordinary resource fetch policy.
func TestEmbeddingPrivateDNS(t *testing.T) {
	policy := Policy{Timeout: time.Second, MaxResponseBytes: 1024, AllowedAddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	endpoint, err := NewEmbeddingEndpoint(policy, "http://model.example/v1/embeddings", true)
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	for _, answers := range [][]string{{"8.8.8.8"}, {"127.0.0.1", "8.8.8.8"}, {"127.0.0.1", "169.254.169.254"}, {"10.0.0.2"}} {
		values := make([]netip.Addr, len(answers))
		for i, address := range answers {
			values[i] = netip.MustParseAddr(address)
		}
		endpoint.fetcher.resolver = fixedResolver{addresses: values}
		connection, err := endpoint.fetcher.dialContext(canceledDialContext(t), "tcp", "model.example:80")
		if connection != nil {
			_ = connection.Close()
			t.Fatal("unsafe DNS answer reached dial")
		}
		if !errors.Is(err, ErrPolicyDenied) {
			t.Fatalf("DNS denial: %v", err)
		}
	}
	ordinary, err := New(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.addressAllowed(netip.MustParseAddr("127.0.0.1")) || endpoint.fetcher.telemetryHeaders {
		t.Fatal("inference permissions widened other boundaries")
	}
	for _, target := range []string{"http://8.8.8.8/v1/embeddings", "http://169.254.169.254/v1/embeddings", "https://private:token@model.example/v1/embeddings", "https://model.example/v1/embeddings?private=token", "https://model.example/v1/embeddings?", "https://model.example/v1/embeddings#private"} {
		if _, err := NewEmbeddingEndpoint(policy, target, true); err == nil || strings.Contains(err.Error(), "token") {
			t.Fatalf("unsafe endpoint failure: %v", err)
		}
	}
	if _, err := endpoint.Post(t.Context(), nil, "private\r\nheader"); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe credential failure: %v", err)
	}
}
