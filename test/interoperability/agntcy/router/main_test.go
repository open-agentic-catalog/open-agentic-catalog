package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type observedBody struct {
	io.Reader
	closed bool
}

func (b *observedBody) Close() error { b.closed = true; return nil }

func TestRoutesAndBytePreservation(t *testing.T) {
	for _, tc := range []struct {
		schema                bool
		method, path, forward string
	}{
		{true, "GET", "/api/1.0.0/objects/record", "/api/objects/record"},
		{true, "POST", "/api/1.0.0/validate", "/api/validate"},
		{false, "GET", "/.well-known/ard.json", "/.well-known/ard.json"},
		{false, "GET", "/.well-known/ai-catalog.json", "/.well-known/ai-catalog.json"},
		{false, "GET", "/v1/agents?pageSize=1", "/v1/agents?pageSize=1"},
		{false, "GET", "/v1/agents/exact-id", "/v1/agents/exact-id"},
		{false, "POST", "/v1/search", "/v1/search"},
		{false, "POST", "/v1/explore", "/v1/explore"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			body := &observedBody{Reader: strings.NewReader("{ \"native\":true }\n")}
			calls := 0
			client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "http://owned:8080"+tc.forward {
					t.Fatal(r.URL)
				}
				raw, _ := io.ReadAll(r.Body)
				if string(raw) != "request bytes" {
					t.Fatal(string(raw))
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-Host") != "" {
					t.Fatal("private caller headers forwarded")
				}
				if r.Header.Get("Accept") != "application/json" {
					t.Fatal(r.Header)
				}
				return &http.Response{StatusCode: http.StatusAccepted, Body: body, Header: http.Header{"Content-Type": []string{"application/json"}, "Etag": []string{"native-etag"}, "Set-Cookie": []string{"private"}, "Link": []string{"<native>; rel=next"}}}, nil
			})}
			selected, err := handler("http://owned:8080", tc.schema, client)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, strings.NewReader("request bytes"))
			request.Header.Set("Authorization", "secret")
			request.Header.Set("Cookie", "private")
			request.Header.Set("X-Forwarded-Host", "external")
			request.Header.Set("Accept", "application/json")
			recorder := httptest.NewRecorder()
			selected.ServeHTTP(recorder, request)
			if calls != 1 || recorder.Code != 202 || recorder.Body.String() != "{ \"native\":true }\n" || !body.closed || recorder.Header().Get("ETag") != "native-etag" || recorder.Header().Get("Set-Cookie") != "" {
				t.Fatalf("calls=%d, closed=%v, response=%v", calls, body.closed, recorder)
			}
		})
	}
}

func TestDeniedRoutesNeverReachUpstream(t *testing.T) {
	for _, schema := range []bool{true, false} {
		calls := 0
		client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected upstream") })}
		selected, _ := handler("http://owned:8080", schema, client)
		for _, tc := range []struct{ method, path string }{{"DELETE", "/v1/agents"}, {"POST", "/v1/agents"}, {"GET", "/v1/admin"}, {"GET", "/api/9.9.9/objects/record"}, {"GET", "/api/1.0.0/../secret"}, {"GET", "/api/1.0.0/%2e%2e/secret"}, {"GET", "//v1/agents"}, {"GET", "/v1/agents/../admin"}} {
			recorder := httptest.NewRecorder()
			selected.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil))
			if recorder.Code != 404 {
				t.Fatalf("schema=%v %v: %d", schema, tc, recorder.Code)
			}
		}
		recorder := httptest.NewRecorder()
		selected.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), "GET", "/healthz", nil))
		if recorder.Code != 200 || calls != 0 {
			t.Fatal("health or denied route called upstream")
		}
	}
	for _, origin := range []string{"https://owned", "http://user:password@owned", "http://owned/path", "http://owned?query", "http://owned#fragment", "http:///empty"} {
		if _, err := handler(origin, false, &http.Client{}); err == nil {
			t.Fatal(origin)
		}
	}
}

func TestTransportAndBodyBounds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		request  int
		response int
		failure  bool
		want     int
	}{
		{"exact-request", requestLimit, 1, false, 200}, {"large-request", requestLimit + 1, 1, false, 413},
		{"exact-response", 0, responseLimit, false, 200}, {"large-response", 0, responseLimit + 1, false, 502}, {"transport", 0, 0, true, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			body := &observedBody{Reader: strings.NewReader(strings.Repeat("x", tc.response))}
			selected, _ := handler("http://owned:8080", false, &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if tc.failure {
					return nil, errors.New("private error")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
			})})
			recorder := httptest.NewRecorder()
			selected.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), "POST", "/v1/search", strings.NewReader(strings.Repeat("r", tc.request))))
			if recorder.Code != tc.want || strings.Contains(recorder.Body.String(), "private error") {
				t.Fatal(recorder.Code, recorder.Body.Len())
			}
			if tc.want == 413 && calls != 0 {
				t.Fatal("oversized request forwarded")
			}
			if !tc.failure && calls > 0 && !body.closed {
				t.Fatal("upstream body not closed")
			}
		})
	}
}
