// Transport units use local responses; real TLS, redirects and socket deadlines
// remain in remote_functional_test.go.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
)

type catalogctlTrackedBody struct {
	io.Reader
	reads, bytesRead, closes int
	closeErr                 error
}

func (b *catalogctlTrackedBody) Read(p []byte) (int, error) {
	b.reads++
	n, err := b.Reader.Read(p)
	b.bytesRead += n
	return n, err
}
func (b *catalogctlTrackedBody) Close() error { b.closes++; return b.closeErr }

func TestRemoteSessionUsesExplicitFreshLoginWithoutIssuerTraffic(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil { // #nosec G302 -- credential directory requires owner-only traversal.
		t.Fatal(err)
	}
	filename := filepath.Join(directory, "login.json")
	const origin = "https://catalog.invalid"
	token := strings.Repeat("t", 32)
	credential := map[string]any{
		"format": 1, "issuer": "https://issuer.invalid", "clientId": "local-client", "apiOrigin": origin,
		"subject": "human-reader", "tenantId": demoTenant, "membershipId": demoTestUUID(4800),
		"nonce": "synthetic-nonce", "authTime": 0, "accessToken": token, "refreshToken": "",
		"expiresAt": time.Now().Add(time.Hour).UTC(),
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	session, err := newRemoteSession(t.Context(), remoteOptions{baseURL: origin, loginFile: filename, timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	if session.origin != origin || session.token != token || session.client.Timeout != time.Second {
		t.Fatal("login token did not reach the origin-bound session")
	}
	if retained, err := os.ReadFile(filename); err != nil || !bytes.Equal(retained, raw) { // #nosec G304 -- reads only this test's private synthetic credential file.
		t.Fatal("fresh login unexpectedly rewrote credentials", err)
	}
	for _, extra := range []remoteOptions{{token: "other-token"}, {tokenFile: filepath.Join(directory, "missing-token")}} {
		extra.baseURL, extra.loginFile = origin, filename
		if session, err := newRemoteSession(t.Context(), extra); err == nil || session != nil || err.Error() != "remote: login credentials cannot be combined with another bearer source" {
			t.Fatalf("ambiguous login credential source admitted: %v", err)
		}
	}
	if session, err := newRemoteSession(t.Context(), remoteOptions{baseURL: origin, loginFile: filepath.Join(directory, "missing-login")}); err == nil || session != nil || err.Error() != "auth: no usable login credentials; sign in again" {
		t.Fatalf("missing explicit login cache admitted: %v", err)
	}
}

func TestRemoteTrustRequestPreservesVersionPresence(t *testing.T) {
	const source = "00000000-0000-7000-8000-000000000010"
	base := ` {"sourceRevision":1,"identifier":"urn:air:peer.example:agents:one","versionPresent":false,"mediaType":"application/a2a-agent-card+json","delivery":"url","exactEntryDigest":"sha256:` + strings.Repeat("a", 64) + `","text":"one","reason":"Verify registration"} `
	for _, version := range []string{"absent", "", "1.0"} {
		t.Run(version, func(t *testing.T) {
			raw := base
			if version != "absent" {
				raw = strings.Replace(raw, `"versionPresent":false`, `"versionPresent":true,"version":"`+version+`"`, 1)
			}
			path := filepath.Join(t.TempDir(), "request.json")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			options, err := parseRemote([]string{"trust-request", "--source-id", source, "--request-file", path, "--idempotency-key", "trust-version-1"}, remoteTestEnvironment("https://catalog.invalid", "fixture-token"))
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				_ = r.Body.Close()
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(body, &fields); err != nil {
					t.Fatal(err)
				}
				if _, present := fields["version"]; present != (version != "absent") {
					t.Error("assessment request lost explicit version presence")
				}
				if string(body) != raw || r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") != "trust-version-1" {
					t.Error("assessment request changed its validated bytes or command binding")
				}
				return &http.Response{StatusCode: http.StatusAccepted, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"owned-job"}`))}, nil
			})}
			if _, err := exchangeRemote(t.Context(), client, options, "https://catalog.invalid", "fixture-token"); err != nil || calls != 1 {
				t.Fatalf("single assessment request: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestRemoteExchangeRequestAndOpaqueSuccess(t *testing.T) {
	t.Parallel()
	const origin = "https://catalog.invalid:8443"
	const payload = ` {"partial":true,"total":9007199254740993,"unknown":null,"url":"https://never-fetch.invalid"} `
	for _, tc := range []struct {
		name                    string
		args                    []string
		method, path, body, key string
		status                  int
	}{
		{"read", []string{"capabilities"}, "GET", "/api/v1/capabilities", "", "", 200},
		{"search", []string{"search", "--text", "café", "--cursor", "opaque", "--limit", "2"}, "POST", "/api/v1/catalog/search", `{"text":"café","limit":2,"cursor":"opaque"}`, "", 200},
		{"reindex", []string{"reindex", "--reason", "Rebuild search", "--idempotency-key", "retry-key"}, "POST", "/api/v1/catalog:reindex", `{"reason":"Rebuild search"}`, "retry-key", 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, err := parseRemote(tc.args, remoteTestEnvironment(origin, "fixture-token"))
			if err != nil {
				t.Fatal(err)
			}
			body := &catalogctlTrackedBody{Reader: strings.NewReader(payload)}
			meta := controlclient.Metadata{RequestID: strings.Repeat("request", 50), ETag: `"` + strings.Repeat("revision", 200) + `"`}
			calls := 0
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Context() != t.Context() || r.URL.String() != origin+tc.path || r.Method != tc.method || r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Accept") != "application/json, application/problem+json" || r.Header.Get("Idempotency-Key") != tc.key || r.Header.Get("Cookie") != "" {
					t.Error("request lost origin, context or command binding")
				}
				var raw []byte
				if r.Body != nil {
					var readErr error
					raw, readErr = io.ReadAll(r.Body)
					if readErr != nil {
						t.Fatal(readErr)
					}
					_ = r.Body.Close()
				}
				contentType := ""
				if tc.method == "POST" {
					contentType = "application/json"
				}
				if r.Header.Get("Content-Type") != contentType {
					t.Errorf("request media=%q", r.Header.Get("Content-Type"))
				}
				if tc.body == "" {
					if len(raw) != 0 {
						t.Error("GET request acquired a body")
					}
				} else {
					var got, want map[string]any
					if json.Unmarshal(raw, &got) != nil || json.Unmarshal([]byte(tc.body), &want) != nil || !reflect.DeepEqual(got, want) {
						t.Errorf("request body=%s", raw)
					}
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}, "X-Request-Id": {meta.RequestID}, "Etag": {meta.ETag}}, Body: body}, nil
			})}
			result, err := exchangeRemote(t.Context(), client, options, origin, options.token)
			if err != nil || string(result.Data) != payload || result.Meta != meta || calls != 1 || body.closes != 1 {
				t.Fatalf("exchange result=%+v err=%v calls=%d closes=%d", result, err, calls, body.closes)
			}
		})
	}
}

func TestRemoteExchangeResponseClassificationAndCleanup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                      string
		status                    int
		media, data, code, detail string
		reindex                   bool
	}{
		{name: "redirect without location", status: 302, code: "redirect_refused", detail: "API redirects are not followed"},
		{name: "proxy body", status: 502, media: "text/html", data: "private-response fixture-token", code: "http_error", detail: "API returned an unsuccessful response"},
		{name: "problem", status: 403, media: "application/problem+json", data: `{"code":"denied","detail":"fixture-token denied","status":200}`, code: "denied", detail: "[redacted] denied"},
		{name: "empty problem", status: 500, media: "application/problem+json", data: `{}`, code: "http_error", detail: "API returned an unsuccessful response"},
		{name: "wrong problem fields", status: 500, media: "application/problem+json", data: `{"code":4}`, code: "http_error", detail: "API returned an unsuccessful response"},
		{name: "duplicate problem", status: 400, media: "application/problem+json", data: `{"code":"first","code":"second"}`, code: "http_error", detail: "API returned an unsuccessful response"},
		{name: "duplicate object", status: 200, media: "application/json", data: `{"x":1,"x":2}`, code: "invalid_response", detail: "expected an unambiguous JSON object"},
		{name: "trailing value", status: 200, media: "application/json", data: `{} {}`, code: "invalid_response", detail: "expected an unambiguous JSON object"},
		{name: "non-object", status: 200, media: "application/json", data: `[]`, code: "invalid_response", detail: "expected an unambiguous JSON object"},
		{name: "invalid UTF-8", status: 200, media: "application/json", data: "{\"x\":\"\xff\"}", code: "invalid_response", detail: "expected an unambiguous JSON object"},
		{name: "wrong media", status: 200, media: "text/plain", data: `{}`, code: "invalid_response", detail: "expected an unambiguous JSON object"},
		{name: "malformed media", status: 200, media: "application/json; broken", data: `{}`, code: "invalid_response", detail: "expected an unambiguous JSON object"},
		{name: "read requires 200", status: 202, media: "application/json", data: `{}`, code: "http_error", detail: "API returned an unsuccessful response"},
		{name: "reindex requires 202", status: 200, media: "application/json", data: `{}`, code: "http_error", detail: "API returned an unsuccessful response", reindex: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &catalogctlTrackedBody{Reader: strings.NewReader(tc.data)}
			calls := 0
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {tc.media}, "X-Request-Id": {"fixture-token-request"}}, Body: body}, nil
			})}
			options := remoteOptions{path: "/api/v1/capabilities"}
			if tc.reindex {
				options.reindex = &remoteReindex{Reason: "Rebuild search"}
			}
			result, err := exchangeRemote(t.Context(), client, options, "https://catalog.invalid", "fixture-token")
			var failure remoteAPIError
			if !errors.As(err, &failure) || failure.Status != tc.status || failure.Code != tc.code || failure.Detail != tc.detail || failure.RequestID != "[redacted]-request" || !reflect.DeepEqual(result, remoteEnvelope{}) || body.closes != 1 || calls != 1 {
				t.Fatalf("failure=%v result=%+v calls=%d closes=%d", err, result, calls, body.closes)
			}
			if strings.Contains(err.Error(), "fixture-token") || strings.Contains(err.Error(), "private-response") {
				t.Fatalf("unsafe diagnostic: %v", err)
			}
			if tc.status == 302 && body.reads != 0 {
				t.Fatal("redirect body was read")
			}
		})
	}
}

func TestRemoteExchangeMetadataAndDiagnosticBounds(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"X-Request-ID", "ETag"} {
		for _, values := range [][]string{{"one", "two"}, {"fixture-token-reflected"}, {"\xff"}} {
			body := &catalogctlTrackedBody{Reader: strings.NewReader(`{}`)}
			headers := http.Header{"Content-Type": {"application/json"}}
			for _, value := range values {
				headers.Add(name, value)
			}
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: body}, nil
			})}
			result, err := exchangeRemote(t.Context(), client, remoteOptions{path: "/api/v1/capabilities"}, "https://catalog.invalid", "fixture-token")
			var failure remoteAPIError
			if !errors.As(err, &failure) || failure.Code != "invalid_response" || failure.Detail != "response metadata is ambiguous or unsafe" || !reflect.DeepEqual(result, remoteEnvelope{}) || body.closes != 1 || strings.Contains(err.Error(), "fixture-token") {
				t.Fatalf("%s unsafe metadata result=%+v err=%v", name, result, err)
			}
		}
	}
	body := &catalogctlTrackedBody{Reader: strings.NewReader(`{"code":"` + strings.Repeat("é", 129) + `","detail":"` + strings.Repeat("界", 1025) + `"}`)}
	client := &http.Client{Transport: catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Content-Type": {"application/problem+json"}, "X-Request-Id": {strings.Repeat("r", 257)}}, Body: body}, nil
	})}
	_, err := exchangeRemote(t.Context(), client, remoteOptions{path: "/api/v1/capabilities"}, "https://catalog.invalid", "fixture-token")
	var failure remoteAPIError
	if !errors.As(err, &failure) || failure.Code != strings.Repeat("é", 128)+"..." || failure.Detail != strings.Repeat("界", 1024)+"..." || failure.RequestID != strings.Repeat("r", 256)+"..." || body.closes != 1 {
		t.Fatalf("diagnostic bounds=%v", err)
	}
}

func TestRemoteExchangeBodyBoundsAndFailures(t *testing.T) {
	t.Parallel()
	for _, size := range []int{remoteMaxBody, remoteMaxBody + 1} {
		data := `{"text":"` + strings.Repeat("x", size-len(`{"text":""}`)) + `"}`
		body := &catalogctlTrackedBody{Reader: strings.NewReader(data + strings.Repeat(" ", 1024))}
		// Exact-limit success has no trailing bytes; the oversized response proves
		// the reader stops at limit+1 rather than consuming an unbounded stream.
		if size == remoteMaxBody {
			body.Reader = strings.NewReader(data)
		}
		client := &http.Client{Transport: catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
		})}
		result, err := exchangeRemote(t.Context(), client, remoteOptions{}, "https://catalog.invalid", "fixture-token")
		if body.closes != 1 || body.bytesRead != size {
			t.Fatalf("read=%d close=%d", body.bytesRead, body.closes)
		}
		if size == remoteMaxBody {
			if err != nil || string(result.Data) != data {
				t.Fatalf("exact limit failed: %v", err)
			}
		} else {
			var failure remoteAPIError
			if !errors.As(err, &failure) || failure.Code != "invalid_response" || !reflect.DeepEqual(result, remoteEnvelope{}) {
				t.Fatalf("oversize error=%v", err)
			}
		}
	}
	for _, phase := range []string{"transport", "transport deadline", "transport cancellation", "read", "read deadline", "read cancellation"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			privateErr := errors.New("private transport fixture-token")
			cause := privateErr
			if strings.HasSuffix(phase, "deadline") {
				cause = context.DeadlineExceeded
			}
			body := &catalogctlTrackedBody{Reader: catalogctlErrorReadCloser{err: cause}}
			calls := 0
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if strings.HasSuffix(phase, "cancellation") {
					cancel()
				}
				if strings.HasPrefix(phase, "transport") {
					return nil, cause
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
			})}
			result, err := exchangeRemote(ctx, client, remoteOptions{}, "https://catalog.invalid", "fixture-token")
			if err == nil || strings.Contains(err.Error(), "fixture-token") || errors.Is(err, privateErr) || calls != 1 || !reflect.DeepEqual(result, remoteEnvelope{}) {
				t.Fatalf("unsafe failure: %v", err)
			}
			switch phase {
			case "transport":
				if err.Error() != "remote: request failed; check API origin, connectivity and TLS trust" {
					t.Fatal(err)
				}
			case "read":
				var failure remoteAPIError
				if !errors.As(err, &failure) || failure.Code != "invalid_response" {
					t.Fatal(err)
				}
			default:
				if err.Error() != "remote: request cancelled or deadline exceeded" {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(phase, "read") && body.closes != 1 {
				t.Fatal("failed response body leaked")
			}
		})
	}
}

func TestRemoteFileBoundsAndCancelledPreflight(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "mounted-secret")
	for _, limit := range []int{remoteMaxToken, remoteMaxCA} {
		data := strings.Repeat("a", limit)
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := remoteReadFile(file, int64(limit))
		if err != nil || string(got) != data {
			t.Fatalf("exact-limit file: %v", err)
		}
		if _, err := remoteReadFile(file, int64(limit-1)); err == nil {
			t.Fatal("oversized secret accepted")
		}
	}
	if _, err := remoteReadFile(filepath.Dir(file), remoteMaxToken); err == nil {
		t.Fatal("directory accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := requestRemote(ctx, remoteOptions{baseURL: "https://catalog.invalid", token: "fixture-token"})
	if err == nil || err.Error() != "remote: request cancelled or deadline exceeded" || !reflect.DeepEqual(result, remoteEnvelope{}) {
		t.Fatalf("cancelled preflight=%+v %v", result, err)
	}
}
