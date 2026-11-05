//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package clientcompat_test

import (
	"context"
	"crypto/x509"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
)

func TestExplicitTLSVerification(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-credential" {
			t.Error("explicit authentication missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	// Certificate refusals are expected and must not flood the test output.
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	for _, tc := range []struct {
		name       string
		trust      bool
		serverName string
		accept     bool
	}{
		{name: "untrusted"},
		{name: "explicit-private-CA", trust: true, accept: true},
		{name: "wrong-hostname", trust: true, serverName: "different.invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hc := explicitHTTPClient()
			defer hc.CloseIdleConnections()
			transport, ok := hc.Transport.(*http.Transport)
			if !ok {
				t.Fatal("expected explicit HTTP transport")
			}
			// Never depend on the host's trust store, and never disable verification.
			transport.TLSClientConfig.RootCAs = x509.NewCertPool()
			if tc.trust {
				transport.TLSClientConfig.RootCAs.AddCert(server.Certificate())
			}
			transport.TLSClientConfig.ServerName = tc.serverName
			c, err := client.NewClient(server.URL, client.WithHTTPClient(boundedDoer{hc, responseLimit}),
				client.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
					r.Header.Set("Authorization", "Bearer test-credential")
					return nil
				}))
			must(t, err)
			before := calls.Load()
			response, err := c.GetCapabilities(context.Background())
			if !tc.accept {
				if err == nil || response != nil || calls.Load() != before {
					t.Fatal("certificate failure reached the authenticated HTTP handler")
				}
				if err.Error() != "Catalog request failed" {
					t.Fatal("TLS failure exposed transport details")
				}
				return
			}
			must(t, err)
			defer func() { must(t, response.Body.Close()) }()
			data, err := io.ReadAll(response.Body)
			must(t, err)
			must(t, expectedJSON(response, data, http.StatusOK))
			if calls.Load() != before+1 {
				t.Fatal("trusted request did not make exactly one HTTP attempt")
			}
		})
	}
}
