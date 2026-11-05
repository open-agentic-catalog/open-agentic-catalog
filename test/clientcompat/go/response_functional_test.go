//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package clientcompat_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
)

func TestResponseHeaderLimit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Private-Detail", "private:"+strings.Repeat("x", 33<<10))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	hc := explicitHTTPClient()
	defer hc.CloseIdleConnections()
	c, err := client.NewClientWithResponses(server.URL, client.WithHTTPClient(boundedDoer{hc, responseLimit}))
	must(t, err)
	response, err := c.GetCapabilitiesWithResponse(context.Background())
	if err == nil || response != nil || calls.Load() != 1 || strings.Contains(err.Error(), "private") {
		t.Fatalf("header limit: response=%v error=%v calls=%d", response, err, calls.Load())
	}
}
