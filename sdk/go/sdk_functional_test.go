//go:build functional

package catalogclient

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFunctionalVerifiedTLSAndCredentialRotation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		want := "Bearer first"
		if n == 2 {
			want = "Bearer second"
		}
		if len(r.Header.Values("Authorization")) != 1 || r.Header.Get("Authorization") != want {
			t.Error("current explicit token missing")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" || len(r.Header.Values("Accept-Encoding")) != 1 || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("editor headers bypassed credential or encoding controls")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	for _, tc := range []struct {
		name      string
		trust     bool
		wrongName bool
		pass      bool
	}{
		{"untrusted", false, false, false}, {"wrong-hostname", true, true, false}, {"trusted", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := x509.NewCertPool()
			if tc.trust {
				selected = roots
			}
			token := "first"
			c, err := New(Config{Origin: server.URL, RootCAs: selected, TokenProvider: func(context.Context) (string, error) { return token, nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if tc.wrongName {
				c.transport.TLSClientConfig.ServerName = "wrong.invalid"
			}
			before := calls.Load()
			editor := func(_ context.Context, r *http.Request) error {
				r.Header["authorization"] = []string{"Bearer stale"}
				r.Header["cOoKiE"] = []string{"synthetic-cookie"}
				r.Header["proxy-authorization"] = []string{"synthetic-proxy-token"}
				r.Header["accept-encoding"] = []string{"gzip"}
				return nil
			}
			r, err := c.GetCapabilitiesWithResponse(context.Background(), editor)
			if !tc.pass {
				if err == nil || r != nil || calls.Load() != before {
					t.Fatal("TLS refusal reached HTTP")
				}
				return
			}
			if err != nil || r.JSON200 == nil {
				t.Fatal("verified TLS failed", err)
			}
			token = "second"
			if _, err = c.GetCapabilitiesWithResponse(context.Background(), editor); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != before+2 {
				t.Fatal("extra request")
			}
		})
	}
}

func TestFunctionalNoRedirectOrAmbientProxy(t *testing.T) {
	var forbidden atomic.Int32
	var requests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forbidden.Add(1) }))
	defer destination.Close()
	t.Setenv("HTTP_PROXY", destination.URL)
	t.Setenv("HTTPS_PROXY", destination.URL)
	t.Setenv("ALL_PROXY", destination.URL)
	t.Setenv("NO_PROXY", "")
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("X-Received-Host", r.Host)
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	address, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	// A loopback URL would bypass Go's environment proxy even if the SDK
	// accidentally enabled one. Map a certificate hostname locally instead.
	selected := "https://" + net.JoinHostPort(origin.Certificate().DNSNames[0], address.Port())
	roots := x509.NewCertPool()
	roots.AddCert(origin.Certificate())
	c, err := New(Config{Origin: selected, RootCAs: roots, Token: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.transport.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
		if target == destination.Listener.Addr().String() {
			forbidden.Add(1)
			return nil, errors.New("unexpected proxy connection")
		}
		if target != strings.TrimPrefix(selected, "https://") {
			return nil, errors.New("unexpected dial destination")
		}
		return (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
	}
	r, err := c.GetCapabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != http.StatusTemporaryRedirect || r.Header.Get("Location") != destination.URL || forbidden.Load() != 0 || requests.Load() != 1 {
		t.Fatal("redirect or proxy received credentials")
	}
	opaque, err := c.GetCapabilities(context.Background(), func(_ context.Context, r *http.Request) error {
		r.URL.Opaque = "https://other.example/api/v1/capabilities"
		return nil
	})
	if opaque != nil {
		_ = opaque.Body.Close()
		t.Errorf("opaque target reached HTTP with Host %q", opaque.Header.Get("X-Received-Host"))
	}
	if err == nil || requests.Load() != 1 || forbidden.Load() != 0 {
		t.Fatal("opaque target bypassed origin confinement", err)
	}
}

func TestFunctionalReusedConnectionDoesNotRepeatCommands(t *testing.T) {
	for _, body := range []string{`{"displayName":"Test","slug":"test"}`, ""} {
		t.Run(map[bool]string{true: "empty", false: "body"}[body == ""], func(t *testing.T) {
			var mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{}`)
					return
				}
				mutations.Add(1)
				if r.Header.Get("Idempotency-Key") != "original-key" {
					t.Error("command key changed")
				}
				_, _ = io.Copy(io.Discard, r.Body)
				conn, _, err := http.NewResponseController(w).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			}))
			defer server.Close()
			c, err := New(Config{Origin: server.URL, AllowLoopbackHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			warm, err := c.GetCapabilities(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_ = warm.Body.Close()
			var reused atomic.Bool
			ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }})
			result, err := c.CreateNamespaceWithBody(ctx, &CreateNamespaceParams{IdempotencyKey: "original-key"}, "application/json", strings.NewReader(body))
			if result != nil {
				_ = result.Body.Close()
			}
			if err == nil || mutations.Load() != 1 || !reused.Load() {
				t.Fatalf("uncertain result=%v attempts=%d reused=%v", err, mutations.Load(), reused.Load())
			}
		})
	}
}

func TestFunctionalBodyDeadlinesAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "body-deadline")
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	for _, ownDeadline := range []bool{false, true} {
		t.Run(map[bool]string{true: "caller", false: "configured"}[ownDeadline], func(t *testing.T) {
			timeout := time.Second
			if ownDeadline {
				timeout = 5 * time.Second
			}
			c, err := New(Config{Origin: server.URL, AllowLoopbackHTTP: true, Timeout: timeout})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			var headers atomic.Bool
			ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotFirstResponseByte: func() { headers.Store(true) }})
			if ownDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			_, err = c.GetCapabilitiesWithResponse(ctx)
			var diagnostic *Error
			if !headers.Load() || !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &diagnostic) || diagnostic.StatusCode != http.StatusOK || diagnostic.RequestID != "body-deadline" {
				t.Fatal("body deadline not reached", err)
			}
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err = c.GetCapabilitiesWithResponse(cancelled); !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation classification lost", err)
			}
		})
	}
}

func TestFunctionalResponseHeaderLimit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Large", strings.Repeat("x", 4096))
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	c, err := New(Config{Origin: server.URL, AllowLoopbackHTTP: true, HeaderBytes: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.GetCapabilitiesWithResponse(context.Background()); err == nil || calls.Load() != 1 {
		t.Fatal("oversized headers accepted", err)
	}
}

func TestFunctionalResponseBodyFramingAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bytes   int
		chunked bool
		short   bool
		code    string
	}{
		{name: "fixed-exact", bytes: 32},
		{name: "chunked-exact", bytes: 32, chunked: true},
		{name: "fixed-over-limit", bytes: 33, code: "response_over_limit"},
		{name: "chunked-over-limit", bytes: 33, chunked: true, code: "response_over_limit"},
		{name: "incomplete-fixed-body", bytes: 32, short: true, code: "response_unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			finished := make(chan struct{})
			payload := "{}" + strings.Repeat(" ", tc.bytes-2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "body-framing")
				if calls.Add(1) > 1 {
					_, _ = io.WriteString(w, "{}")
					return
				}
				defer close(finished)
				if !tc.chunked {
					length := len(payload)
					if tc.short {
						length += 5
					}
					w.Header().Set("Content-Length", strconv.Itoa(length))
				}
				_, _ = io.WriteString(w, payload)
				if tc.chunked {
					if err := http.NewResponseController(w).Flush(); err != nil {
						t.Error("could not flush chunked control", err)
						return
					}
					if tc.code != "" {
						// No final chunk: the SDK must stop at limit+1 and close the
						// connection instead of waiting for the server to finish.
						<-r.Context().Done()
					}
				}
			}))
			defer server.Close()
			c, err := New(Config{Origin: server.URL, AllowLoopbackHTTP: true, ResponseBytes: 32, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			result, err := c.GetCapabilitiesWithResponse(context.Background())
			if tc.code == "" {
				if err != nil || result == nil || result.JSON200 == nil || string(result.Body) != payload || result.HTTPResponse.Header.Get("X-Request-ID") != "body-framing" {
					t.Fatal("exact body control failed", err)
				}
				if tc.chunked && (result.HTTPResponse.ContentLength != -1 || len(result.HTTPResponse.TransferEncoding) != 1 || result.HTTPResponse.TransferEncoding[0] != "chunked") {
					t.Fatal("control did not use unknown-length chunked HTTP")
				}
			} else {
				var diagnostic *Error
				if result != nil || !errors.As(err, &diagnostic) || diagnostic.Code != tc.code || diagnostic.StatusCode != http.StatusOK || diagnostic.RequestID != "body-framing" {
					t.Fatal("incomplete or oversized body returned a partial result", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("body failure caused another request")
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("response body did not release the server")
			}
			result, err = c.GetCapabilitiesWithResponse(context.Background())
			if err != nil || result == nil || result.JSON200 == nil || calls.Load() != 2 {
				t.Fatal("client did not recover for the next explicit call", err)
			}
		})
	}
}

func TestFunctionalHTTPRefusalsDoNotRepeatCommands(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			const original = `{"displayName":"Example","slug":"example"}`
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || r.Method != http.MethodPost || string(body) != original || r.Header.Get("Idempotency-Key") != "original-key" {
					t.Error("command was changed", err)
				}
				w.Header().Set("Content-Type", "application/problem+json")
				w.Header().Set("Retry-After", "7")
				w.Header().Set("X-Request-ID", "retry-decision")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"type":"about:blank","title":"Try later","status":`+strconv.Itoa(status)+`,"code":"DEPENDENCY_UNAVAILABLE"}`)
			}))
			defer server.Close()
			c, err := New(Config{Origin: server.URL, AllowLoopbackHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			result, err := c.CreateNamespaceWithBodyWithResponse(context.Background(), &CreateNamespaceParams{IdempotencyKey: "original-key"}, "application/json", strings.NewReader(original))
			if err != nil || result == nil || result.StatusCode() != status || result.ApplicationproblemJSONDefault == nil || result.ApplicationproblemJSONDefault.Status != status || result.HTTPResponse.Header.Get("Retry-After") != "7" || result.HTTPResponse.Header.Get("X-Request-ID") != "retry-decision" || calls.Load() != 1 {
				t.Fatal("HTTP refusal lost metadata or repeated the command", err)
			}
		})
	}
}
