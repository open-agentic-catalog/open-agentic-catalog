// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func captureCatalogctlStdout(t *testing.T, call func() error) []byte {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	callErr := call()
	os.Stdout = original
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	raw, readErr := io.ReadAll(reader)
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if callErr != nil {
		t.Fatal(callErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	return raw
}

func TestSmokeTargetAndArgumentsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name        string
		value       string
		allowRemote bool
	}{
		{name: "remote requires opt in", value: "https://catalog.example.com"},
		{name: "remote requires TLS", value: "http://catalog.example.com", allowRemote: true},
		{name: "credentials forbidden", value: "http://user@127.0.0.1:8080"},
		{name: "path forbidden", value: "http://127.0.0.1:8080/catalog"},
		{name: "scheme forbidden", value: "file:///tmp/catalog"},
		{name: "non-HTTP origin", value: "ftp://127.0.0.1:8080"},
		{name: "malformed URL", value: "%"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateSmokeBaseURL(test.value, test.allowRemote); err == nil {
				t.Fatalf("validateSmokeBaseURL(%q, %v) succeeded", test.value, test.allowRemote)
			}
		})
	}
	if got, err := validateSmokeBaseURL("http://[::1]:8080/", false); err != nil || got != "http://[::1]:8080" {
		t.Fatalf("loopback URL = %q, %v", got, err)
	}
	if _, err := parseSmokeOptions([]string{"--run-id", "NOT_VALID"}); err == nil {
		t.Fatal("invalid run ID succeeded")
	}
	if _, err := parseSmokeOptions([]string{"unexpected"}); err == nil {
		t.Fatal("unexpected positional argument succeeded")
	}
	for _, arguments := range [][]string{
		{"--unknown"},
		{"--timeout", "0s"},
		{"--timeout", "11m"},
		{"--admin-token="},
		{"--base-url", "http://127.0.0.1:8080/?query=value", "--run-id", "valid"},
		{"--base-url", "http://127.0.0.1:8080/#fragment", "--run-id", "valid"},
	} {
		if _, err := parseSmokeOptions(arguments); err == nil {
			t.Fatalf("invalid smoke arguments %#v succeeded", arguments)
		}
	}
	options, err := parseSmokeOptions([]string{"--base-url", "http://localhost:8080/", "--run-id", " Mixed-Case "})
	if err != nil || options.BaseURL != "http://localhost:8080" || options.RunID != "mixed-case" {
		t.Fatalf("normalized smoke options = %#v, %v", options, err)
	}
	generated, err := parseSmokeOptions([]string{"--base-url", "http://127.0.0.1:8080"})
	if err != nil || !smokeRunIDPattern.MatchString(generated.RunID) {
		t.Fatalf("generated smoke options = %#v, %v", generated, err)
	}
	remote, err := parseSmokeOptions([]string{"--base-url", "https://catalog.example.com", "--allow-remote", "--run-id", "remote"})
	if err != nil || remote.BaseURL != "https://catalog.example.com" || !remote.AllowRemote {
		t.Fatalf("explicit remote smoke options = %#v, %v", remote, err)
	}
	if err := smoke([]string{"--run-id", "not_valid"}); err == nil {
		t.Fatal("smoke command accepted invalid options")
	}
}

func TestSmokeResponseAndRunHelpers(t *testing.T) {
	runID, err := newSmokeRunID()
	if err != nil || !strings.HasPrefix(runID, "smoke-") || len(runID) != len("smoke-")+10 {
		t.Fatalf("smoke run ID = %q, %v", runID, err)
	}
	missing := missingSmokeIdentifiers([]string{"one", "three"}, []string{"one", "two", "three", "four"})
	if !slices.Equal(missing, []string{"two", "four"}) {
		t.Fatalf("missing identifiers = %#v", missing)
	}
	t.Setenv("CATALOG_TEST_SMOKE_VALUE", " configured ")
	if got := environmentOrDefault("CATALOG_TEST_SMOKE_VALUE", "fallback"); got != "configured" {
		t.Fatalf("configured environment value = %q", got)
	}
	t.Setenv("CATALOG_TEST_SMOKE_VALUE", " \t ")
	if got := environmentOrDefault("CATALOG_TEST_SMOKE_VALUE", "fallback"); got != "fallback" {
		t.Fatalf("blank override = %q", got)
	}
	if got := environmentOrDefault("CATALOG_TEST_SMOKE_MISSING", "fallback"); got != "fallback" {
		t.Fatalf("environment fallback = %q", got)
	}

	var decoded map[string]string
	if err := decodeSmokeResponse(smokeHTTPResponse{}, &decoded); err == nil {
		t.Fatal("empty smoke response was accepted")
	}
	if err := decodeSmokeResponse(smokeHTTPResponse{Body: []byte(`{"value":`)}, &decoded); err == nil {
		t.Fatal("malformed smoke response was accepted")
	}
	if err := decodeSmokeResponse(smokeHTTPResponse{Body: []byte(`{"value":"one"}{"value":"two"}`)}, &decoded); err == nil {
		t.Fatal("trailing smoke response was accepted")
	}
	if err := decodeSmokeResponse(smokeHTTPResponse{Body: []byte(`{"value":"ok"}`)}, &decoded); err != nil || decoded["value"] != "ok" {
		t.Fatalf("valid smoke response = %#v, %v", decoded, err)
	}

	documentContent := []byte("exact smoke document")
	documentDigest := sha256.Sum256(documentContent)
	documentID := hex.EncodeToString(documentDigest[:])
	documentHeaders := make(http.Header)
	documentHeaders.Set("Content-Length", "20")
	documentHeaders.Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(documentDigest[:])+":")
	documentHeaders.Set("Content-Type", "text/plain")
	documentHeaders.Set("Content-Disposition", `attachment; filename="`+documentID+`"`)
	documentHeaders.Set("ETag", `"`+documentID+`"`)
	documentHeaders.Set("Cache-Control", "public, immutable")
	validDocument := smokeHTTPResponse{Header: documentHeaders, Body: documentContent}
	if err := validateSmokeDocument(validDocument, documentID, documentContent, true); err != nil {
		t.Fatalf("valid smoke document returned %v", err)
	}
	if err := validateSmokeDocument(validDocument, documentID, []byte("different"), true); err == nil {
		t.Fatal("mismatched smoke document bytes were accepted")
	}
	if err := validateSmokeDocument(smokeHTTPResponse{Header: make(http.Header), Body: documentContent}, documentID, documentContent, false); err == nil {
		t.Fatal("smoke document without exact content headers was accepted")
	}
	missingPublicValidators := smokeHTTPResponse{Header: documentHeaders.Clone(), Body: documentContent}
	missingPublicValidators.Header.Del("ETag")
	if err := validateSmokeDocument(missingPublicValidators, documentID, documentContent, true); err == nil {
		t.Fatal("public smoke document without validators was accepted")
	}
}

type catalogctlRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn catalogctlRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type catalogctlErrorReadCloser struct{ err error }

func (reader catalogctlErrorReadCloser) Read([]byte) (int, error) { return 0, reader.err }

func (catalogctlErrorReadCloser) Close() error { return nil }

func TestSmokeHTTPClientControlledFailures(t *testing.T) {
	client := &smokeHTTPClient{baseURL: "http://example.invalid", client: &http.Client{Transport: catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport unavailable")
	})}}
	if _, err := client.request(context.Background(), "encode", http.MethodPost, "/resource", "", "", "", make(chan int), http.StatusCreated); err == nil || !strings.Contains(err.Error(), "encode request") {
		t.Fatalf("unencodable request returned %v", err)
	}

	badURLClient := &smokeHTTPClient{baseURL: "%", client: client.client}
	if _, err := badURLClient.request(context.Background(), "create", http.MethodGet, "/resource", "", "", "", nil, http.StatusOK); err == nil || !strings.Contains(err.Error(), "create request") {
		t.Fatalf("invalid request URL returned %v", err)
	}

	errorClient := client
	if _, err := errorClient.request(context.Background(), "transport", http.MethodGet, "/resource", "", "", "", nil, http.StatusOK); err == nil || !strings.Contains(err.Error(), "transport unavailable") {
		t.Fatalf("transport failure returned %v", err)
	}

	readFailure := errors.New("read failed")
	failedBody := &catalogctlTrackedBody{Reader: catalogctlErrorReadCloser{err: readFailure}}
	errorClient.client.Transport = catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: failedBody, Request: request}, nil
	})
	if result, err := errorClient.request(context.Background(), "read", http.MethodGet, "/resource", "", "", "", nil, http.StatusOK); !errors.Is(err, readFailure) || !strings.Contains(err.Error(), "read response") || failedBody.closes != 1 || result.Header != nil || result.Body != nil || len(errorClient.checks) != 0 {
		t.Fatalf("read failure returned %v", err)
	}

	errorClient.client.Transport = catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTeapot, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("detail ", 200))), Request: request}, nil
	})
	if result, err := errorClient.request(context.Background(), "status", http.MethodGet, "/resource", "", "", "", nil, http.StatusOK); err == nil || !strings.Contains(err.Error(), "returned 418, expected 200") || !strings.HasSuffix(err.Error(), "...") || result.Header != nil || result.Body != nil || len(errorClient.checks) != 0 {
		t.Fatalf("unexpected status returned %v", err)
	}
}

// Construction remains inert: inspect the production transport policy without
// dialing, then exercise its redirect decision directly.
func TestLocalWorkflowClientPolicy(t *testing.T) {
	client := newLocalWorkflowClient()
	defer client.CloseIdleConnections()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == http.DefaultTransport || transport.Proxy != nil || transport.DialContext == nil || transport.TLSHandshakeTimeout != 5*time.Second || transport.ResponseHeaderTimeout != 10*time.Second || transport.MaxResponseHeaderBytes != 32<<10 || client.Jar != nil {
		t.Fatal("local workflow lost its independent, proxy-free bounded transport")
	}
	second := newLocalWorkflowClient()
	defer second.CloseIdleConnections()
	if second == client || second.Transport == client.Transport {
		t.Fatal("workflow clients share mutable transport state")
	}
	if client.CheckRedirect == nil || !errors.Is(client.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("workflow client accepts redirects")
	}
}

func TestSmokeHTTPClientResponseBoundsAndCleanup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		size int
		want string
	}{
		{name: "exact limit", size: smokeMaximumResponseBytes},
		{name: "over limit", size: smokeMaximumResponseBytes + 1024, want: "bounded check: response exceeds 8388608 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &catalogctlTrackedBody{Reader: strings.NewReader(strings.Repeat("x", tc.size))}
			calls := 0
			client := &smokeHTTPClient{baseURL: "https://catalog.invalid", checks: []string{"previous"}, client: &http.Client{Transport: catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/octet-stream"}}, Body: body}, nil
			})}}
			result, err := client.request(t.Context(), "bounded", http.MethodGet, "/public/v1/artifacts/document", "", "", "", nil, http.StatusOK)
			if calls != 1 || body.closes != 1 || body.bytesRead > smokeMaximumResponseBytes+1 {
				t.Fatalf("calls=%d close=%d bytes=%d", calls, body.closes, body.bytesRead)
			}
			if tc.want != "" {
				if err == nil || err.Error() != tc.want || result.Header != nil || result.Body != nil || !slices.Equal(client.checks, []string{"previous"}) {
					t.Fatalf("failed exchange result=%+v error=%v checks=%v", result, err, client.checks)
				}
				if body.bytesRead != smokeMaximumResponseBytes+1 {
					t.Fatal("oversize response was not stopped at limit+1")
				}
			} else if err != nil || len(result.Body) != tc.size || strings.Trim(string(result.Body), "x") != "" || !slices.Equal(client.checks, []string{"previous", "bounded"}) {
				t.Fatalf("exact limit result length=%d error=%v checks=%v", len(result.Body), err, client.checks)
			}
		})
	}
}

func TestSmokeHTTPClientConditionalHeadersAndCheckHistory(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusNotModified, http.StatusNotFound} {
		responseHeaders := http.Header{"Etag": {`"revision"`}, "X-Values": {"one", "two"}}
		inputHeaders := map[string]string{"If-None-Match": `"revision"`}
		body := &catalogctlTrackedBody{Reader: strings.NewReader("")}
		calls := 0
		client := &smokeHTTPClient{baseURL: "https://catalog.invalid", checks: []string{"previous"}, client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Context() != t.Context() || r.URL.String() != "https://catalog.invalid/document" || r.Method != http.MethodGet || r.Body != nil && r.Body != http.NoBody || r.Header.Get("Accept") != "application/json" || r.Header.Get("If-None-Match") != `"revision"` || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "" || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("If-Match") != "" {
				t.Fatal("conditional anonymous request lost its binding")
			}
			return &http.Response{StatusCode: status, Header: responseHeaders, Body: body}, nil
		})}}
		result, err := client.requestWithHeaders(t.Context(), "expected response", http.MethodGet, "/document", "", "", "", nil, inputHeaders, status)
		if err != nil || calls != 1 || body.closes != 1 || len(result.Body) != 0 || !slices.Equal(client.checks, []string{"previous", "expected response"}) {
			t.Fatalf("expected status %d not recorded: %v", status, err)
		}
		if result.Header.Get("ETag") != `"revision"` || !slices.Equal(result.Header.Values("X-Values"), []string{"one", "two"}) {
			t.Fatal("response headers were not preserved")
		}
		result.Header["X-Values"][0] = "changed"
		responseHeaders.Set("ETag", `"later"`)
		if responseHeaders.Values("X-Values")[0] != "one" || result.Header.Get("ETag") != `"revision"` || len(inputHeaders) != 1 || inputHeaders["If-None-Match"] != `"revision"` {
			t.Fatal("headers share mutable state")
		}
	}
}
