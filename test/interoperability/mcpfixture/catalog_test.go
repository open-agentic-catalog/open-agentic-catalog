package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestCatalogExchangeBindsOriginAndCommandHeaders(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(`{"id":"retained"}`)}
			client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "http://127.0.0.1:8080/api/v1/catalog-listings" || r.Header.Get("Authorization") != "Bearer synthetic" || r.Header.Get("If-Match") != `"rev-2"` {
					t.Fatal("lost fixed origin or request authority")
				}
				if method == http.MethodPost {
					data, err := io.ReadAll(r.Body)
					if err != nil || string(data) != `{"example":true}` || r.Header.Get("Idempotency-Key") != "command-one" || r.Header.Get("Content-Type") != "application/json" {
						t.Fatal("lost command body or binding")
					}
				} else if r.Body != nil || r.Header.Get("Idempotency-Key") != "" {
					t.Fatal("GET carried command content")
				}
				return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"Etag": {`"rev-3"`}, "Set-Cookie": {"never-retain"}}, Body: body}, nil
			})}
			response, err := catalogExchange(t.Context(), client, catalogRequest{Method: method, Path: "/api/v1/catalog-listings", Token: "synthetic", ETag: `"rev-2"`, Key: "command-one", Body: []byte(`{"example":true}`)})
			if err != nil || response.Status != 201 || response.ETag != `"rev-3"` || string(response.Body) != `{"id":"retained"}` || !body.closed {
				t.Fatalf("unexpected response/closure: %#v, %v", response, err)
			}
		})
	}
}

func TestCatalogExchangeRejectsTargetsBeforeTransport(t *testing.T) {
	client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid target reached transport")
		return nil, errors.New("unexpected request")
	})}
	for _, path := range []string{"https://example.invalid/api/v1/resources", "//elsewhere/api/v1/resources", "/mcp", "/api/v1/x#fragment", "/api/v1/\\x", "%"} {
		if _, err := catalogExchange(t.Context(), client, catalogRequest{Method: "GET", Path: path}); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	if _, err := catalogExchange(t.Context(), client, catalogRequest{Method: "DELETE", Path: "/api/v1/resources"}); err == nil {
		t.Fatal("accepted unsupported fixture method")
	}
}

func TestCatalogExchangeBoundsAndClosesResponses(t *testing.T) {
	for _, data := range []string{`{"problem":true}`, "broken", `{}` + strings.Repeat(" ", 2*1024*1024)} {
		body := &trackedBody{Reader: strings.NewReader(data)}
		client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: body}, nil
		})}
		response, err := catalogExchange(t.Context(), client, catalogRequest{Method: "GET", Path: "/api/v1/resources"})
		if !body.closed || (err == nil) != (data == `{"problem":true}`) {
			t.Fatal("incorrect body bound or closure")
		}
		if err == nil && response.Status != 403 {
			t.Fatal("denial became success")
		}
	}
}
