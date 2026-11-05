//go:build functional

package trustevidence

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

func TestAcquireThroughOwnedTLSBoundary(t *testing.T) {
	for _, name := range []string{"valid", "untrusted TLS", "denied address", "redirect", "oversized", "bad media", "bundle budget", "cancel"} {
		t.Run(name, func(t *testing.T) {
			var calls, redirectCalls atomic.Int32
			redirect := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectCalls.Add(1) }))
			defer redirect.Close()
			var handler http.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("fixture handler was not installed") })
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
			defer server.Close()
			c, b := fixture(t, server.URL)
			if name == "bundle budget" {
				c.TrustVerification.Limits.BundleBytes = 1024
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Api-Key") != "" || len(r.TLS.PeerCertificates) != 0 {
					t.Error("credentials reached public evidence endpoint")
				}
				if r.Method != http.MethodGet || r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("wrong evidence request")
				}
				switch name {
				case "redirect":
					http.Redirect(w, r, redirect.URL+"/stolen", http.StatusFound)
					return
				case "cancel":
					cancel()
					<-r.Context().Done()
					return
				case "oversized":
					w.Header().Set("Content-Type", "application/scitt-receipt+cose")
					_, _ = w.Write([]byte(strings.Repeat("x", int(c.TrustVerification.Limits.ReceiptBytes)+1)))
					return
				case "bad media":
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte("not a receipt"))
					return
				}
				response := serveEvidence(b, fetcher.Request{URL: server.URL + r.URL.RequestURI()})
				w.Header().Set("Content-Type", response.MediaType)
				_, _ = w.Write(response.Body)
			})
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			if name == "untrusted TLS" {
				roots = x509.NewCertPool()
			}
			origin, err := fetcher.NormalizeOrigin(server.URL, false)
			if err != nil {
				t.Fatal(err)
			}
			f, err := fetcher.New(fetcher.Policy{AllowedAddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, Timeout: time.Second, MaxResponseBytes: 1 << 20, MaxRedirects: 0, AllowedOrigins: map[string]struct{}{origin: {}}, AllowTestAddresses: name != "denied address", RootCAs: roots}, nil)
			if err != nil {
				t.Fatal(err)
			}
			// Neither a proxy nor its embedded credentials may bypass address policy.
			t.Setenv("HTTPS_PROXY", "http://proxy-user:proxy-secret@127.0.0.1:1")
			a, err := New(c, f)
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.Acquire(ctx, "owned-ans", tenant, b.Entry)
			if name == "valid" {
				_, v, _ := c.TrustVerification.Policy("owned-ans")
				r, e := v.Verify(context.Background(), got, assessmentTime)
				if err != nil || e != nil || !r.CurrentRegistration || calls.Load() != 3 {
					t.Fatalf("TLS evidence failed: %+v %v %v", r, err, e)
				}
			} else if err == nil {
				t.Fatal("unsafe retrieval accepted")
			}
			if redirectCalls.Load() != 0 {
				t.Fatal("redirect target was contacted")
			}
			if (name == "denied address" || name == "untrusted TLS") && calls.Load() != 0 {
				t.Fatal("request escaped address/TLS validation")
			}
			if name == "bundle budget" {
				raw, e := json.Marshal(got)
				if !errors.Is(err, ErrBudget) || calls.Load() != 0 || e != nil || int64(len(raw)) > c.TrustVerification.Limits.BundleBytes {
					t.Fatal("entry exceeded the complete bundle budget or contacted the authority", err, e)
				}
			}
			if name == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
		})
	}
}

type loopbackResolver struct{ calls atomic.Int32 }

func (r *loopbackResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls.Add(1)
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func TestANSOriginDoesNotAuthorizePrivateDNSAnswers(t *testing.T) {
	c, b := fixture(t, nativeOrigin)
	resolver := &loopbackResolver{}
	f, err := fetcher.New(fetcher.Policy{Timeout: time.Second, MaxResponseBytes: 1 << 20, AllowedOrigins: map[string]struct{}{nativeOrigin + ":443": {}}}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(c, f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acquire(context.Background(), "owned-ans", tenant, b.Entry); err == nil || resolver.calls.Load() == 0 {
		t.Fatal("approved name bypassed DNS/IP admission", err)
	}
}
