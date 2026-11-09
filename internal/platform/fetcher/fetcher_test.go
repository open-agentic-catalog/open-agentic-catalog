package fetcher

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// canceledDialContext keeps denial tests socket-free even if a policy guard is
// broken. The injected resolver still returns its answers; assertions must
// distinguish policy rejection from cancellation at the real dial boundary.
func canceledDialContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

type fixedResolver struct{ addresses []netip.Addr }

func (r fixedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, nil
}

type errorResolver struct{ err error }

func (r errorResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return nil, r.err
}

type errorReadCloser struct{ err error }

func (r errorReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (errorReadCloser) Close() error               { return nil }

func TestFetcherRejectsUnsafeURLsAndResolvedAddresses(t *testing.T) {
	policy := Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2}
	fetcher, err := New(policy, fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"http://example.com/catalog.json",
		"https://user:secret@example.com/catalog.json",
		"https://127.0.0.1/catalog.json",
		"https://[::1]/catalog.json",
		"https://example.com/catalog.json#fragment",
		"https://example.com/catalog.json?access_token=secret",
		"https://example.com/catalog.json?X-Amz-Signature=secret",
		"https://example.com:0/catalog.json",
		"https://example.com/" + strings.Repeat("ü", 400),
	} {
		if _, err := fetcher.validateURL(value); err == nil {
			t.Fatalf("unsafe URL was accepted: %s", value)
		}
	}
	if _, err := fetcher.dialContext(canceledDialContext(t), "tcp", "example.com:443"); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("private resolved address rejection = %v", err)
	}
	emptyFetcher, err := New(policy, fixedResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyFetcher.dialContext(canceledDialContext(t), "tcp", "example.com:443"); err == nil || strings.Contains(err.Error(), "%!") {
		t.Fatalf("empty DNS response returned %v", err)
	}
	publicFetcher, err := New(policy, fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111")}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := publicFetcher.validateURL("https://[2606:4700:4700::1111]/catalog.json")
	if err != nil || parsed.Host != "[2606:4700:4700::1111]" {
		t.Fatalf("public IPv6 URL = %v, %v", parsed, err)
	}
}

func TestFetcherClassifiesMalformedTargetsSeparatelyFromPolicyRefusals(t *testing.T) {
	fetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 1}, fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fetcher.validateURL("https://user:secret@example.com/catalog"); !errors.Is(err, ErrInvalidTarget) || errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("malformed target classification = %v", err)
	}
	if _, err := fetcher.validateURL("https://127.0.0.1/catalog"); !errors.Is(err, ErrPolicyDenied) || errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("literal policy refusal classification = %v", err)
	}
	if _, err := fetcher.dialContext(canceledDialContext(t), "tcp", "example.com:443"); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("resolved policy refusal classification = %v", err)
	}
}

func FuzzValidateURL(f *testing.F) {
	for _, seed := range []string{
		"https://catalog.example.com/.well-known/ard.json",
		"https://catalog.example.com:8443/ard/v1/search?type=agent",
		"http://catalog.example.com/ard.json",
		"https://user:secret@catalog.example.com/ard.json",
		"https://127.0.0.1/metadata",
		"https://catalog.example.com/ard.json?access_token=secret",
		"https://catalog.example.com/ard.json#fragment",
		"https://000000..",
		"\x00",
	} {
		f.Add(seed)
	}
	fetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2}, fixedResolver{})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, value string) {
		parsed, err := fetcher.validateURL(value)
		if err != nil {
			return
		}
		if parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.Fragment != "" || parsed.User != nil {
			t.Fatalf("accepted URL violates the outbound URL contract: %q", parsed.String())
		}
		normalized, err := fetcher.validateURL(parsed.String())
		if err != nil {
			t.Fatalf("accepted normalized URL no longer validates: %q: %v", parsed.String(), err)
		}
		if normalized.String() != parsed.String() {
			t.Fatalf("URL normalization is not idempotent: %q then %q", parsed.String(), normalized.String())
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

type fetcherTestContextKey struct{}

func TestRedactedRequestURLPreservesOptionalQuery(t *testing.T) {
	for _, test := range []struct {
		name      string
		input     string
		hideQuery bool
		want      string
	}{
		{"query visible", "https://catalog.example/metadata?cursor=fixture%2Bvalue", false, "https://catalog.example/metadata?cursor=fixture%2Bvalue"},
		{"query hidden", "https://catalog.example/metadata?cursor=fixture%2Bvalue", true, "https://catalog.example/metadata?redacted"},
		{"no query hidden", "https://catalog.example/metadata", true, "https://catalog.example/metadata"},
		{"no query visible", "https://catalog.example/metadata", false, "https://catalog.example/metadata"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := url.Parse(test.input)
			if err != nil {
				t.Fatal(err)
			}
			original := *parsed
			if got := redactedRequestURL(parsed, test.hideQuery); got != test.want {
				t.Fatalf("request diagnostic = %q, want %q", got, test.want)
			}
			if *parsed != original {
				t.Fatal("diagnostic formatting changed the transport URL")
			}
		})
	}
}

func TestNormalizeOriginCanonicalizesEffectivePorts(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "HTTPS://Catalog.Example:0443/path", want: "https://catalog.example:443"},
		{input: "https://[2001:db8::1]:00443/path", want: "https://[2001:db8::1]:443"},
		{input: "http://catalog.example:00080/path", want: "http://catalog.example:80"},
		{input: "https://catalog.example:65535/path", want: "https://catalog.example:65535"},
	} {
		got, err := NormalizeOrigin(test.input, strings.HasPrefix(test.want, "http://"))
		if err != nil || got != test.want {
			t.Errorf("NormalizeOrigin(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
	for _, input := range []string{"https://catalog.example:0", "https://catalog.example:65536"} {
		if _, err := NormalizeOrigin(input, false); err == nil {
			t.Fatalf("invalid origin port accepted: %s", input)
		}
	}
	left, _ := url.Parse("https://catalog.example:0443/one")
	right, _ := url.Parse("https://catalog.example/two")
	if !sameOrigin(left, right) {
		t.Fatal("equivalent explicit and implicit ports were treated as different origins")
	}
}

func TestFetcherLeavesCompressionNegotiationToBoundedTransport(t *testing.T) {
	fetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2, AllowHTTPForTests: true}, fixedResolver{})
	if err != nil {
		t.Fatal(err)
	}
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Content-Type") != "" {
			t.Fatal("bodyless request acquired a Content-Type")
		}
		if request.Header.Get("Accept-Encoding") != "" {
			t.Fatalf("fetcher set Accept-Encoding explicitly: %q", request.Header.Get("Accept-Encoding"))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: request}, nil
	})
	result, err := fetcher.Fetch(context.Background(), Request{URL: "http://example.com/catalog.json"})
	if err != nil || string(result.Body) != `{"ok":true}` {
		t.Fatalf("fetch = %q, %v", result.Body, err)
	}
}

func TestFetcherAllowsOnlyDeclaredOutboundHeaders(t *testing.T) {
	fetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2, AllowHTTPForTests: true}, fixedResolver{})
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]string{
		"authorization":         "Bearer credential",
		"x-api-key":             "api-key",
		"x-catalog-webhook-id":  "webhook-id",
		"x-catalog-delivery-id": "delivery-id",
		"x-catalog-timestamp":   "timestamp",
		"x-catalog-signature":   "signature",
		"ce-specversion":        "1.0",
		"ce-id":                 "event-id",
		"ce-type":               "event-type",
		"ce-source":             "event-source",
		"ce-subject":            "event-subject",
		"ce-dataschema":         "event-schema",
	}
	requests := 0
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		for name, want := range allowed {
			if got := request.Header.Get(name); got != want {
				t.Fatalf("header %s = %q, want %q", name, got, want)
			}
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	if _, err := fetcher.Fetch(context.Background(), Request{URL: "http://example.com/events", Method: http.MethodPost, Headers: allowed}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("transport requests = %d, want 1", requests)
	}

	for _, name := range []string{"Cookie", "Host", "Proxy-Authorization", "X-Forwarded-For", "Accept-Encoding"} {
		t.Run(name, func(t *testing.T) {
			if _, err := fetcher.Fetch(context.Background(), Request{URL: "http://example.com/events", Headers: map[string]string{name: "sensitive"}}); err == nil || !strings.Contains(err.Error(), "is not allowed") {
				t.Fatalf("disallowed header %s returned %v", name, err)
			}
		})
	}
	if _, err := fetcher.Fetch(context.Background(), Request{URL: "http://example.com/events", Headers: map[string]string{"Authorization": "Bearer safe\r\nX-Injected: value"}}); err == nil || !strings.Contains(err.Error(), "is invalid") {
		t.Fatalf("header injection returned %v", err)
	}
	if requests != 1 {
		t.Fatalf("rejected headers reached the transport; requests = %d", requests)
	}
}

func TestFetcherRejectsUnsupportedMethodsAndOversizedBodies(t *testing.T) {
	fetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2, AllowHTTPForTests: true}, fixedResolver{})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 1025))), Request: request}, nil
	})
	if _, err := fetcher.Fetch(context.Background(), Request{URL: "http://example.com/events", Method: http.MethodPut}); err == nil {
		t.Fatal("unsupported outbound method was accepted")
	}
	if _, err := fetcher.Fetch(context.Background(), Request{URL: "http://example.com/events", Method: http.MethodPost, Body: []byte(strings.Repeat("x", 1025))}); err == nil {
		t.Fatal("oversized outbound request body was accepted")
	}
	if requests != 0 {
		t.Fatalf("invalid request reached the transport; requests = %d", requests)
	}
	if _, err := fetcher.Fetch(context.Background(), Request{URL: "http://example.com/events"}); err == nil || !strings.Contains(err.Error(), "decoded size limit") {
		t.Fatalf("oversized response body returned %v", err)
	}
}

func TestFetcherHonorsNarrowPerRequestResponseLimit(t *testing.T) {
	fetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 2048, MaxRedirects: 1, AllowHTTPForTests: true}, fixedResolver{})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 1025))), Request: request}, nil
	})
	if _, err := fetcher.Fetch(context.Background(), Request{URL: "http://example.com/search", MaxResponseBytes: 1024}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("narrow response limit returned %v", err)
	}
	if _, err := fetcher.Fetch(context.Background(), Request{URL: "http://example.com/search", MaxResponseBytes: 512}); err == nil || !strings.Contains(err.Error(), "limit is invalid") {
		t.Fatalf("invalid response limit returned %v", err)
	}
	if requests != 1 {
		t.Fatalf("per-request response-limit transport requests = %d", requests)
	}
}

func TestFetcherRejectsMixedDNSAnswers(t *testing.T) {
	fetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2}, fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("169.254.169.254")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fetcher.dialContext(canceledDialContext(t), "tcp", "example.com:443"); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("mixed DNS policy rejection = %v", err)
	}
}

func TestAddressPolicy(t *testing.T) {
	fetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2}, fixedResolver{})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]bool{
		"93.184.216.34":        true,
		"10.0.0.1":             false,
		"127.0.0.1":            false,
		"169.254.1.1":          false,
		"192.0.2.1":            false,
		"198.51.100.1":         false,
		"203.0.113.1":          false,
		"::1":                  false,
		"fc00::1":              false,
		"64:ff9b::a00:1":       false,
		"100::1":               false,
		"2001:db8::1":          false,
		"2002:a00:1::1":        false,
		"2606:4700:4700::1111": true,
	}
	for value, want := range tests {
		if got := fetcher.addressAllowed(netip.MustParseAddr(value)); got != want {
			t.Errorf("addressAllowed(%s) = %v, want %v", value, got, want)
		}
	}
	privateFetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2, AllowedAddressPrefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}, fixedResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if !privateFetcher.addressAllowed(netip.MustParseAddr("10.0.0.1")) || privateFetcher.addressAllowed(netip.MustParseAddr("127.0.0.1")) || privateFetcher.addressAllowed(netip.MustParseAddr("192.0.2.1")) {
		t.Fatal("private-network profile did not preserve the always-denied address boundary")
	}
}

func TestOriginBoundRedirectPolicy(t *testing.T) {
	fetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2}, fixedResolver{})
	if err != nil {
		t.Fatal(err)
	}
	initialContext := context.WithValue(context.Background(), redirectPolicyContextKey{}, true)
	initial, err := http.NewRequestWithContext(initialContext, http.MethodPost, "https://hooks.example.test/events", strings.NewReader("event"))
	if err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"host":   "https://other.example.test/events",
		"path":   "https://hooks.example.test/new-events",
		"port":   "https://hooks.example.test:8443/events",
		"scheme": "http://hooks.example.test/events",
	} {
		t.Run(name, func(t *testing.T) {
			redirect, requestErr := http.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader("event"))
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			if err := fetcher.checkRedirect(redirect, []*http.Request{initial}); err == nil {
				t.Fatalf("origin-bound request followed redirect to %s", target)
			}
		})
	}
	unrestricted, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://catalog.example.test/ard.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	crossOrigin, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://cdn.example.test/ard.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fetcher.checkRedirect(crossOrigin, []*http.Request{unrestricted}); err != nil {
		t.Fatalf("ordinary safe fetch could not follow an allowed cross-origin redirect: %v", err)
	}
}

func TestRedirectCredentialsStayWithinTheExactOrigin(t *testing.T) {
	fetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2}, fixedResolver{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://catalog.example.test/ard.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, testCase := range map[string]struct {
		target string
		keep   bool
	}{
		"path":         {target: "https://catalog.example.test/next.json", keep: true},
		"default-port": {target: "https://catalog.example.test:443/next.json", keep: true},
		"port":         {target: "https://catalog.example.test:8443/next.json"},
		"host":         {target: "https://cdn.example.test/next.json"},
	} {
		t.Run(name, func(t *testing.T) {
			redirect, requestErr := http.NewRequestWithContext(context.Background(), http.MethodGet, testCase.target, nil)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			redirect.Header.Set("Authorization", "Bearer secret")
			redirect.Header.Set("X-Api-Key", "secret")
			if err := fetcher.checkRedirect(redirect, []*http.Request{initial}); err != nil {
				t.Fatal(err)
			}
			retained := redirect.Header.Get("Authorization") != "" || redirect.Header.Get("X-Api-Key") != ""
			if retained != testCase.keep {
				t.Fatalf("credentials retained=%v, want %v for %s", retained, testCase.keep, testCase.target)
			}
		})
	}
}

func TestOriginBoundRedirectPolicyIsEnforcedByHTTPClient(t *testing.T) {
	fetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2}, fixedResolver{})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if got := request.Context().Value(fetcherTestContextKey{}); got != "caller-value" {
			t.Fatalf("outbound context value = %v", got)
		}
		if deadline, ok := request.Context().Deadline(); !ok || !deadline.Before(time.Now().Add(5*time.Second)) {
			t.Fatalf("outbound context deadline = %v, present=%v", deadline, ok)
		}
		if !redirectsRejected(request) {
			t.Fatal("origin-bound redirect marker was not attached to the inherited context")
		}
		if requests > 1 {
			t.Fatal("HTTP client sent an origin-bound payload to the redirect target")
		}
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": []string{"https://other.example.test/events"}}, Body: io.NopCloser(strings.NewReader("redirect")), Request: request}, nil
	})
	callerCtx, cancel := context.WithTimeout(context.WithValue(context.Background(), fetcherTestContextKey{}, "caller-value"), 10*time.Second)
	defer cancel()
	_, err = fetcher.Fetch(callerCtx, Request{URL: "https://hooks.example.test/events", Method: http.MethodPost, Body: []byte("event"), RejectRedirects: true})
	if err == nil || !strings.Contains(err.Error(), "redirect denied") || requests != 1 {
		t.Fatalf("origin-bound fetch = requests %d, error %v", requests, err)
	}
}

func TestFetcherPreservesBoundedRequestAndResponseMetadata(t *testing.T) {
	fetcher, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 2048, MaxRedirects: 2, AllowHTTPForTests: true, UserAgent: "catalog-test/1.0"}, fixedResolver{})
	if err != nil {
		t.Fatal(err)
	}
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.Header.Get("User-Agent") != "catalog-test/1.0" || request.Header.Get("Accept") != "application/json, application/ard+json" || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("If-None-Match") != `"entry"` || request.Header.Get("If-Modified-Since") == "" {
			t.Fatalf("outbound request = %#v", request)
		}
		header := make(http.Header)
		header.Set("Content-Type", "Application/JSON; charset=utf-8")
		header.Set("ETag", `"entry"`)
		header.Set("Last-Modified", "Sun, 30 Aug 2026 12:00:00 GMT")
		header.Set("Cache-Control", "private, max-age=60")
		header.Add("Vary", "Accept")
		header.Add("Vary", "Accept-Encoding")
		header.Set("Retry-After", "120")
		header.Add("Link", "</next>; rel=next")
		header.Add("Link", "</profile>; rel=profile")
		return &http.Response{
			StatusCode: http.StatusNotModified,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    request,
		}, nil
	})
	result, err := fetcher.Fetch(context.Background(), Request{
		URL:          "http://example.com/catalog",
		Method:       http.MethodPost,
		Body:         []byte(`{"query":"catalog"}`),
		Accept:       []string{"application/json", "application/ard+json"},
		ETag:         `"entry"`,
		LastModified: "Sun, 30 Aug 2026 12:00:00 GMT",
	})
	if err != nil || result.Status != http.StatusNotModified || result.MediaType != "application/json" || result.ETag != `"entry"` || result.CacheControl != "private, max-age=60" || len(result.Vary) != 2 || result.RetryAfter != "120" || len(result.Links) != 2 || result.BodyDigest == "" || result.RequestedURL == "" || result.FinalURL == "" || result.FetchedAt.IsZero() || result.Duration < 0 {
		t.Fatalf("fetch result = %#v, %v", result, err)
	}

	transportErr := errors.New("transport unavailable")
	fetcher.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})
	if _, err := fetcher.Fetch(context.Background(), Request{URL: "http://example.com/catalog"}); err == nil || !strings.Contains(err.Error(), "transport unavailable") {
		t.Fatalf("transport failure returned %v", err)
	}
	if _, err := fetcher.Fetch(context.Background(), Request{URL: "http://example.com/catalog?pageSize=100&pageToken=opaque-secret", RedactQueryInErrors: true}); err == nil || strings.Contains(err.Error(), "opaque-secret") || !strings.Contains(err.Error(), "redacted") || !errors.Is(err, transportErr) {
		t.Fatalf("redacted transport failure returned %v", err)
	}
	fetcher.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: errorReadCloser{err: errors.New("read failed")}, Request: request}, nil
	})
	if _, err := fetcher.Fetch(context.Background(), Request{URL: "http://example.com/catalog"}); err == nil || !strings.Contains(err.Error(), "read fetched body") {
		t.Fatalf("response read failure returned %v", err)
	}
}

func TestFetcherURLDNSAndRedirectGuardrails(t *testing.T) {
	policy := Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 1, AllowedOrigins: map[string]struct{}{"https://example.com:443": {}}}
	fetcher, err := New(policy, errorResolver{err: errors.New("DNS unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := fetcher.validateURL("https://EXAMPLE.com/catalog")
	if err != nil || parsed.Host != "example.com" {
		t.Fatalf("normalized allowed host = %#v, %v", parsed, err)
	}
	for _, value := range []string{
		"",
		"https://example.com/catalog?broken=%zz",
		"https://example.com:65536/catalog",
		"https://example.com\n/catalog",
		"https://example.com./catalog",
		"https://exämple.com/catalog",
		"https://other.example/catalog",
	} {
		if _, err := fetcher.validateURL(value); err == nil {
			t.Fatalf("unsafe URL was accepted: %q", value)
		}
	}
	if _, err := fetcher.dialContext(canceledDialContext(t), "tcp", "missing-port"); err == nil || !strings.Contains(err.Error(), "parse upstream address") {
		t.Fatalf("malformed dial address returned %v", err)
	}
	if _, err := fetcher.dialContext(canceledDialContext(t), "tcp", "example.com:443"); err == nil || !strings.Contains(err.Error(), "DNS unavailable") {
		t.Fatalf("resolver failure returned %v", err)
	}

	target, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/next", nil)
	viaOne, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/start", nil)
	viaTwo, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/middle", nil)
	if err := fetcher.checkRedirect(target, []*http.Request{viaOne, viaTwo}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("redirect limit returned %v", err)
	}
	deniedTarget, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/next", nil)
	if err := fetcher.checkRedirect(deniedTarget, nil); err == nil || !errors.Is(err, ErrPolicyDenied) || errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("unsafe redirect target returned %v", err)
	}
	malformedTarget, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://user:secret@example.com/next", nil)
	if err := fetcher.checkRedirect(malformedTarget, []*http.Request{viaOne}); err == nil || !errors.Is(err, ErrPolicyDenied) || errors.Is(err, ErrInvalidTarget) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("malformed remote redirect classification returned %v", err)
	}
	if err := fetcher.checkRedirect(target, []*http.Request{viaOne}); err != nil {
		t.Fatalf("last permitted redirect rejected: %v", err)
	}
	if err := fetcher.checkRedirect(target, nil); err != nil {
		t.Fatalf("initial redirect validation returned %v", err)
	}
	if redirectsRejected(viaOne) {
		t.Fatal("ordinary request unexpectedly rejected redirects")
	}
	httpsURL, _ := url.Parse("https://example.com/path")
	httpURL, _ := url.Parse("http://example.com/path")
	unknownURL, _ := url.Parse("custom://example.com/path")
	if sameOrigin(httpsURL, httpURL) || effectivePort(unknownURL) != "" || fetcher.addressAllowed(netip.Addr{}) {
		t.Fatal("origin, port, or invalid-address boundary was relaxed")
	}
}

func TestFetcherAcceptsExactPolicyCeilings(t *testing.T) {
	policy := Policy{Timeout: time.Second, MaxResponseBytes: 64 << 20, MaxRedirects: 10}
	f, err := New(policy, fixedResolver{})
	if err != nil {
		t.Fatalf("valid maximum policy: %v", err)
	}
	f.client.CloseIdleConnections()
	for _, invalid := range []Policy{
		{Timeout: time.Second, MaxResponseBytes: (64 << 20) + 1, MaxRedirects: 10},
		{Timeout: time.Second, MaxResponseBytes: 64 << 20, MaxRedirects: 11},
	} {
		if _, err := New(invalid, fixedResolver{}); err == nil {
			t.Fatal("policy ceiling exceeded")
		}
	}
}

func TestFetcherURLBoundsBeforeAndAfterNormalization(t *testing.T) {
	f, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024}, fixedResolver{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.client.CloseIdleConnections)
	prefix := "https://example.com/"
	longHost := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	// Leading port zeroes shrink during normalization. The raw input bound
	// must still reject this URL even though its normalized form fits.
	padded := "https://example.com:0000000443/"
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"exact URL limit", prefix + strings.Repeat("a", 2048-len(prefix)), true},
		{"raw URL over limit", padded + strings.Repeat("a", 2049-len(padded)), false},
		{"exact hostname limit", "https://" + longHost + "/", true},
		{"hostname over limit", "https://" + longHost + "d/", false},
		{"trailing dot", "https://example.com./", false},
		{"malformed escape", "https://%zz/", false},
		{"port overflow", "https://example.com:65536/", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := f.validateURL(tc.input)
			if tc.valid {
				if err != nil || value == nil {
					t.Fatalf("valid boundary rejected: %v", err)
				}
			} else if !errors.Is(err, ErrInvalidTarget) {
				t.Fatalf("invalid target returned %v", err)
			}
		})
	}
}

func TestFetcherCanceledDialPreservesCancellation(t *testing.T) {
	f, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024}, fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.client.CloseIdleConnections)
	connection, err := f.dialContext(canceledDialContext(t), "tcp", "example.com:443")
	if connection != nil {
		_ = connection.Close()
		t.Fatal("cancelled dial returned a connection")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled dial returned %v", err)
	}
}

type fetchTrace struct {
	t        *testing.T
	outcomes []string
}

func (f *fetchTrace) Start(ctx context.Context, name string, kind catalog.TraceKind, cause catalog.TraceContext) (context.Context, func(string)) {
	if name != "HTTP outbound" || kind != catalog.TraceClient || cause != "" {
		f.t.Error("unexpected outbound trace identity")
	}
	return ctx, func(outcome string) { f.outcomes = append(f.outcomes, outcome) }
}

func TestFetcherTraceReportsHTTPOutcome(t *testing.T) {
	for _, tc := range []struct {
		status  int
		outcome string
	}{
		{http.StatusOK, "ok"}, {399, "ok"}, {http.StatusBadRequest, "error"}, {http.StatusServiceUnavailable, "error"},
	} {
		t.Run(http.StatusText(tc.status)+tc.outcome, func(t *testing.T) {
			f, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024}, fixedResolver{})
			if err != nil {
				t.Fatal(err)
			}
			f.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
			})
			trace := &fetchTrace{t: t}
			ctx := catalog.WithTraceContext(catalog.WithTracer(t.Context(), trace), catalog.TraceContext("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"))
			result, err := f.Fetch(ctx, Request{URL: "https://example.com/catalog"})
			if err != nil || result.Status != tc.status || len(trace.outcomes) != 1 || trace.outcomes[0] != tc.outcome {
				t.Fatalf("status=%d trace=%v error=%v", result.Status, trace.outcomes, err)
			}
		})
	}
}
