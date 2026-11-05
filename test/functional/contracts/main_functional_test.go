//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestContractLoaderCannotFetchChangedExternalReferences(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	raw := []byte(fmt.Sprintf(`{"openapi":"3.1.0","info":{"title":"test","version":"1"},"paths":{},"components":{"schemas":{"Secret":{"$ref":%q}}}}`, server.URL+"/private-secret"))
	if _, err := loadContract(context.Background(), raw); !errors.Is(err, errContract) {
		t.Fatal("accepted contract drift")
	}
	if requests.Load() != 0 {
		t.Fatal("external schema was fetched")
	}
}

func TestBoundedTransportRedirectAndCancellation(t *testing.T) {
	t.Parallel()
	var followed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/redirect":
			http.Redirect(w, req, "/followed", http.StatusFound)
		case "/followed":
			followed.Add(1)
		case "/slow":
			<-req.Context().Done()
		case "/truncated", "/truncated-error":
			w.Header().Set("Content-Length", "100")
			if req.URL.Path == "/truncated-error" {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			_, _ = w.Write([]byte("private-secret"))
		case "/error", "/large":
			if req.URL.Path == "/error" {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			_, _ = w.Write([]byte(strings.Repeat("x", maximumResponse+1)))
		}
	}))
	defer server.Close()
	client, err := localClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	for _, path := range []string{"/large", "/error"} {
		if _, err := request(context.Background(), client, server.URL, path, "private-secret", ""); !errors.Is(err, errLimit) {
			t.Fatalf("%s: expected byte limit; got %v", path, err)
		}
	}
	for _, path := range []string{"/truncated", "/truncated-error"} {
		if _, err := request(context.Background(), client, server.URL, path, "private-secret", ""); !errors.Is(err, errTransport) {
			t.Fatalf("%s: expected safe truncation failure; got %v", path, err)
		}
	}
	if _, err := request(context.Background(), client, server.URL, "/redirect", "private-secret", ""); !errors.Is(err, errTransport) || followed.Load() != 0 {
		t.Fatal("redirect was followed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := request(ctx, client, server.URL, "/slow", "private-secret", ""); !errors.Is(err, errTransport) {
		t.Fatal("request was not canceled")
	}
	for _, base := range []string{"https://example.com", "http://example.com:80", "http://127.0.0.1:80?", "http://user:secret@127.0.0.1:80", "http://127.0.0.1:80/path"} {
		if _, err := localClient(base); err == nil {
			t.Fatal("accepted non-owned target")
		}
	}
}
