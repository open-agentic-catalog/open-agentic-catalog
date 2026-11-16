// Anonymous command units use controlled transports; no listener or binary runs here.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
)

const discoveryUnitEntry = `{"identifier":"urn:air:agent:example:reader","displayName":"Reader\u001b[31m","type":"application/a2a+json","url":"https://never-fetch.invalid/entry","extensions":{"future":true},"version":""}`

func TestDiscoveryArgumentsPreserveSelectionAndRejectAuthorityInputs(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"manifest", "--help"}, {"list", "--help"}} {
		if _, err := parseDiscovery(args); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("help %q: %v", args, err)
		}
	}
	for _, action := range []string{"manifest", "list"} {
		options, err := parseDiscovery([]string{action, "--base-url", "https://catalog.example:443"})
		if err != nil || options.baseURL != "https://catalog.example:443" || options.timeout != 30*time.Second {
			t.Fatal(options, err)
		}
	}
	continuation := " opaque+/= ?&next=other \u00e9 "
	filter := " displayName = 'Reader' AND type = 'application/a2a+json' "
	options, err := parseDiscovery([]string{"list", "--base-url", "https://catalog.example", "--filter", filter, "--order-by", "updatedAt DESC", "--page-size", "1", "--page-token", continuation, "--timeout", "2m"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(options.path)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/ard/v1/agents" || !reflect.DeepEqual(parsed.Query(), url.Values{"filter": {filter}, "orderBy": {"updatedAt DESC"}, "pageSize": {"1"}, "pageToken": {continuation}}) {
		t.Fatal("rewritten List selection", options.path)
	}
	for _, order := range []string{"displayName", "displayName DESC", "updatedAt", "updatedAt DESC"} {
		if _, err := parseDiscovery([]string{"list", "--base-url", "https://catalog.example", "--order-by", order}); err != nil {
			t.Fatal(order, err)
		}
	}
	for _, extra := range [][]string{
		{"--token", "secret\x1b"}, {"--token-file", "/private/secret"}, {"--login-file", "/private/login"}, {"--issuer-ca-file", "/private/issuer"},
		{"--header", "Host: other"}, {"--host", "other"}, {"--filter", strings.Repeat("f", 2049)}, {"--page-token", strings.Repeat("t", 4097)},
		{"--filter", "\xff"}, {"--page-token", "a\x00b"}, {"--order-by", "identifier"}, {"--page-size", "0"}, {"--page-size", "101"},
		{"--page-size", "not-an-integer-secret"}, {"--timeout", "0s"}, {"--timeout", "121s"}, {"--timeout", "secret"}, {"extra"},
	} {
		_, err := parseDiscovery(append([]string{"list", "--base-url", "https://catalog.example", "--ca-file", "/private/ca"}, extra...))
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/private") || strings.ContainsRune(err.Error(), '\x1b') {
			t.Fatalf("unsafe or accepted flags %q: %v", extra, err)
		}
	}
	for _, args := range [][]string{{"search"}, {"manifest"}, {"manifest", "--base-url", "http://catalog.example"}, {"manifest", "--base-url", "https://user:secret@catalog.example"}, {"manifest", "--base-url", "https://catalog.example/path"}, {"manifest", "--base-url", "https://catalog.example", "--page-size", "1"}} {
		if _, err := parseDiscovery(args); err == nil {
			t.Fatal("accepted unsupported command", args)
		}
	}
}

func TestDiscoveryAnonymousSessionKeepsControlCredentialsRequired(t *testing.T) {
	t.Parallel()
	if actual := controlclient.Diagnostic("request", "", 20); actual != "request" {
		t.Fatal("anonymous diagnostics changed without a bearer", actual)
	}
	if actual := controlclient.Diagnostic(strings.Repeat("\u202e", 300), "", 256); len([]rune(actual)) != 259 {
		t.Fatal("anonymous diagnostics lost the rune bound")
	}
	ctx := t.Context()
	session, err := controlclient.NewAnonymous(ctx, "https://catalog.example", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	transport, ok := session.Client.Transport.(*http.Transport)
	if !ok || session.Token != "" || session.Origin != "https://catalog.example" || session.Client.Timeout != time.Second || transport.Proxy != nil || !transport.DisableCompression || !transport.DisableKeepAlives || transport.MaxResponseHeaderBytes != controlclient.MaxHeaders || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("anonymous session lost transport policy")
	}
	if err := session.Client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal("redirect admitted", err)
	}
	if _, err := controlclient.New(ctx, controlclient.Options{BaseURL: "https://catalog.example", Timeout: time.Second}); err == nil {
		t.Fatal("Control admitted missing bearer")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := controlclient.NewAnonymous(cancelled, "https://catalog.example", "/not-read", time.Second); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatal(err)
	}
	if _, err := controlclient.NewAnonymous(ctx, "http://catalog.example", "/not-read", time.Second); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatal(err)
	}
}

func TestDiscoveryExchangeOneAnonymousRequestAndSafeJSON(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"manifest", "list"} {
		t.Run(action, func(t *testing.T) {
			options, err := parseDiscovery([]string{action, "--base-url", "https://catalog.example"})
			if err != nil {
				t.Fatal(err)
			}
			raw := `{"entries":[` + discoveryUnitEntry + `]}`
			if action == "list" {
				raw = `{"items":[` + discoveryUnitEntry + `],"total":2,"pageToken":"next+/= \u00e9"}`
			}
			body := &catalogctlTrackedBody{Reader: strings.NewReader(raw)}
			calls := 0
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Method != http.MethodGet || request.URL.String() != options.baseURL+options.path || request.Host != "catalog.example" || request.Body != nil || request.Header.Get("Accept") != "application/json" || request.Header.Get("Cache-Control") != "no-cache" {
					t.Fatal("changed fixed request", request)
				}
				if len(request.Header) != 2 {
					t.Fatal("unexpected credential, conditional or authority header", request.Header)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}, "X-Request-Id": {"request-1"}, "Etag": {`"generation-2"`}}, Body: body}, nil
			})}
			result, err := exchangeDiscovery(t.Context(), client, options.baseURL, options)
			if err != nil || calls != 1 || body.closes != 1 || string(result.Data) != raw || result.Meta.RequestID != "request-1" || result.Meta.ETag != `"generation-2"` {
				t.Fatal(result, err, calls, body.closes)
			}
			encoded, err := remoteJSON(result)
			if err != nil || bytes.ContainsRune(encoded, '\x1b') || bytes.Contains(encoded, []byte("\u00e9")) {
				t.Fatal("unsafe terminal encoding", string(encoded), err)
			}
			var decoded remoteEnvelope
			if err := json.Unmarshal(encoded, &decoded); err != nil || !json.Valid(decoded.Data) {
				t.Fatal(err)
			}
		})
	}
}

func TestDiscoveryResponseValidationAndRefusal(t *testing.T) {
	t.Parallel()
	valid := `{"items":[` + discoveryUnitEntry + `],"total":1}`
	for _, raw := range []string{valid, `{"items":[],"total":4,"pageToken":"continue"}`, `{"items":[],"total":0}`, `{"items":[],"total":0,"future":true}`} {
		if !validDiscoveryData([]byte(raw), 1) {
			t.Fatal("valid page refused", raw)
		}
	}
	for _, raw := range []string{`{}`, `{"items":null,"total":0}`, `{"items":[],"total":null}`, `{"items":[],"total":-1}`, `{"items":[],"total":1.5}`, `{"items":[],"total":"0"}`, `{"items":[],"total":0,"pageToken":null}`, `{"items":[],"total":0,"pageToken":1}`, `{"items":[],"total":0,"pageToken":"\u0000"}`, `{"items":[null],"total":1}`, `{"items":[{"identifier":null,"displayName":"n","type":"t","url":"https://example.com"}],"total":1}`, strings.Replace(valid, `"url":"https://never-fetch.invalid/entry"`, `"data":null`, 1), strings.Replace(valid, `"total":1`, `"total":0`, 1), strings.Replace(valid, discoveryUnitEntry, discoveryUnitEntry+","+discoveryUnitEntry, 1)} {
		if validDiscoveryData([]byte(raw), 1) {
			t.Fatal("invalid page accepted", raw)
		}
	}
	for _, test := range []struct {
		name, raw, media, code string
		status                 int
	}{
		{"wrong media", valid, "text/html", "invalid_response", 200}, {"bad charset", valid, "application/json; charset=latin-1", "invalid_response", 200},
		{"duplicate member", `{"items":[],"items":[],"total":0}`, "application/json", "invalid_response", 200},
		{"unpaired Unicode", `{"items":[],"total":0,"extra":"\ud800"}`, "application/json", "invalid_response", 200},
		{"invalid UTF8", valid + "\xff", "application/json", "invalid_response", 200}, {"trailing", valid + "{}", "application/json", "invalid_response", 200},
		{"oversize valid JSON", valid + strings.Repeat(" ", controlclient.MaxBody+1-len(valid)), "application/json", "invalid_response", 200},
		{"bounded read", valid + strings.Repeat(" ", controlclient.MaxBody+4096-len(valid)), "application/json", "invalid_response", 200},
		{"bad shape", `{"items":[],"total":null}`, "application/json", "invalid_response", 200},
		{"ARD refusal", `{"errorCode":"INVALID_ARGUMENT","message":"private-secret\u001b"}`, "application/json", "INVALID_ARGUMENT", 400},
		{"untrusted code", `{"errorCode":"private-secret","message":"secret"}`, "application/json", "http_error", 503},
		{"redirect", `{}`, "application/json", "http_error", 302},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &catalogctlTrackedBody{Reader: strings.NewReader(test.raw)}
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": {test.media}, "X-Request-Id": {"refused-request-1"}}, Body: body}, nil
			})}
			result, err := exchangeDiscovery(t.Context(), client, "https://catalog.example", discoveryOptions{path: "/ard/v1/agents", pageSize: 1})
			var failure remoteAPIError
			if !errors.As(err, &failure) || failure.Code != test.code || failure.Status != test.status || failure.RequestID != "refused-request-1" || len(result.Data) != 0 || body.closes != 1 || body.bytesRead > controlclient.MaxBody+1 || strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "\x1b") {
				t.Fatalf("code=%q status=%d requestId=%q dataBytes=%d bytesRead=%d closes=%d err=%v", failure.Code, failure.Status, failure.RequestID, len(result.Data), body.bytesRead, body.closes, err)
			}
		})
	}
	for _, name := range []string{"Content-Type", "X-Request-Id", "Etag"} {
		client := &http.Client{Transport: catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
			h := http.Header{"Content-Type": {"application/json"}}
			h[name] = []string{"one", "two"}
			return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader(valid))}, nil
		})}
		if _, err := exchangeDiscovery(t.Context(), client, "https://catalog.example", discoveryOptions{path: "/ard/v1/agents", pageSize: 1}); err == nil {
			t.Fatal("ambiguous metadata accepted", name)
		}
	}
}

func TestDiscoveryAcceptsExactResponseByteLimit(t *testing.T) {
	t.Parallel()
	const document = `{"items":[],"total":0}`
	raw := document + strings.Repeat(" ", controlclient.MaxBody-len(document))
	body := &catalogctlTrackedBody{Reader: strings.NewReader(raw)}
	client := &http.Client{Transport: catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
	})}
	result, err := exchangeDiscovery(t.Context(), client, "https://catalog.example", discoveryOptions{path: "/ard/v1/agents", pageSize: 1})
	if err != nil || len(result.Data) != controlclient.MaxBody || body.bytesRead != controlclient.MaxBody || body.closes != 1 {
		t.Fatalf("exact-limit response: dataBytes=%d bytesRead=%d closes=%d err=%v", len(result.Data), body.bytesRead, body.closes, err)
	}
}

func TestDiscoveryHelpAndArgumentFailures(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := runDiscovery(t.Context(), []string{"list", "--help"}, &out); err != nil || out.String() != discoveryUsage {
		t.Fatal(out.String(), err)
	}
	if err := runDiscovery(t.Context(), []string{"--help"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := runDiscovery(t.Context(), []string{"manifest", "--token-file", "private-secret"}, io.Discard); err == nil {
		t.Fatal("credential flag accepted")
	}
}
