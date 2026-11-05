package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// These admission checks use recorders only; denied input must never reach a network.
func TestAdmissionRejectsBeforeForwarding(t *testing.T) {
	const key = "01234567890123456789012345678901"
	cases := []struct {
		name, method, path, key, authorization, body string
		status                                       int
	}{
		{"missing", "POST", "/mcp", "", "", "{}", 401},
		{"wrong", "POST", "/mcp", "wrong", "", "{}", 401},
		{"catalog-token", "POST", "/mcp", key, "Bearer catalog-test", "{}", 401},
		{"path", "POST", "/other", key, "", "{}", 404},
		{"query", "POST", "/mcp?other=1", key, "", "{}", 404},
		{"method", "PUT", "/mcp", key, "", "{}", 404},
		{"invalid-json", "POST", "/mcp", key, "", "{", 400},
		{"oversize", "POST", "/mcp", key, "", strings.Repeat("x", maxBody+1), 413},
		{"other-tool", "POST", "/mcp", key, "", `{"method":"tools/call","params":{"name":"get-env"}}`, 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := &url.URL{Scheme: "http", Host: "must-not-resolve.invalid"}
			f := newFixture(key, target)
			request := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, strings.NewReader(tc.body))
			request.Header.Set("X-Mcp-Test-Key", tc.key)
			request.Header.Set("Authorization", tc.authorization)
			response := httptest.NewRecorder()
			f.ServeHTTP(response, request)
			if response.Code != tc.status || f.seen.Forwarded != 0 {
				t.Fatalf("response %d; observations %+v", response.Code, f.seen)
			}
			snapshot := httptest.NewRecorder()
			f.status(snapshot, request)
			var seen observations
			if err := json.Unmarshal(snapshot.Body.Bytes(), &seen); err != nil {
				t.Fatal(err)
			}
			if seen.Requests != 1 {
				t.Fatalf("requests = %d", seen.Requests)
			}
			if strings.Contains(snapshot.Body.String(), key) || strings.Contains(snapshot.Body.String(), "catalog-test") {
				t.Fatal("credential retained in observations")
			}
		})
	}
}

func TestDemoSessionCeilingDoesNotForward(t *testing.T) {
	f := newFixture("01234567890123456789012345678901", &url.URL{Scheme: "http", Host: "must-not-resolve.invalid"})
	f.sessionLimit = 256
	f.seen.Methods["initialize"] = 256
	request := httptest.NewRequestWithContext(t.Context(), "POST", "/mcp", strings.NewReader(`{"method":"initialize","params":{}}`))
	request.Header.Set("X-Mcp-Test-Key", f.key)
	response := httptest.NewRecorder()
	f.ServeHTTP(response, request)
	if response.Code != 429 || f.seen.Forwarded != 0 {
		t.Fatal("session ceiling forwarded or admitted")
	}
	request = httptest.NewRequestWithContext(t.Context(), "POST", "/mcp", strings.NewReader(`{}`))
	request.Header.Set("X-Mcp-Test-Key", f.key)
	request.Header.Set("Cookie", "private-synthetic-cookie")
	response = httptest.NewRecorder()
	f.ServeHTTP(response, request)
	if response.Code != 401 || f.seen.Forwarded != 0 {
		t.Fatal("cookie forwarded")
	}
}
