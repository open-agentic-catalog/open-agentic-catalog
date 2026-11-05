//go:build functional

// Functional tests exercise real process, network or application composition boundaries.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
)

func TestRemoteSearchContractAndWire(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(api.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	const identifier = "00000000-0000-7000-8000-000000000099"
	for _, test := range []struct {
		args                    []string
		path, method, operation string
	}{
		{[]string{"capabilities"}, "/api/v1/capabilities", "GET", "getCapabilities"},
		{[]string{"search", "--text", "café", "--kind", "agent", "--kind", "skill", "--tag", "one", "--tag", "two", "--license", " MIT ", "--category", " TOOLS ", "--category", "café", "--origin", "local", "--namespace", identifier, "--publisher", "publisher", "--visibility", "tenant", "--limit", "2", "--cursor", "opaque"}, "/api/v1/catalog/search", "POST", "searchCatalog"},
		{[]string{"record", "--id", identifier}, "/api/v1/catalog/resources/{catalogRecordId}", "GET", "getCatalogRecord"},
		{[]string{"job", "--id", identifier}, "/api/v1/jobs/{jobId}", "GET", "getJob"},
	} {
		t.Run(test.operation, func(t *testing.T) {
			operation := document.Paths.Value(test.path).GetOperation(test.method)
			if operation == nil || operation.OperationID != test.operation {
				t.Fatal("client operation drift")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := strings.ReplaceAll(strings.ReplaceAll(test.path, "{catalogRecordId}", identifier), "{jobId}", identifier)
				if r.Method != test.method || r.URL.Path != expectedPath || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Idempotency-Key") != "" {
					t.Errorf("incorrect exchange: %s %s", r.Method, r.URL.Path)
				}
				if test.method == "POST" {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if r.Header.Get("Content-Type") != "application/json" || operation.RequestBody.Value.Content.Get("application/json").Schema.Value.VisitJSON(body) != nil {
						t.Error("Search body violates contract")
					}
					tags, ok := body["tags"].([]any)
					if !ok || body["text"] != "café" || body["cursor"] != "opaque" || body["limit"] != float64(2) || len(tags) != 2 || body["publisher"] != "publisher" || body["visibility"] != "tenant" {
						t.Errorf("missing filters: %#v", body)
					}
					if body["license"] != " MIT " || body["namespaceId"] != identifier || !reflect.DeepEqual(body["categories"], []any{" TOOLS ", "café"}) || !reflect.DeepEqual(body["origins"], []any{"local"}) {
						t.Errorf("declaration/origin filters changed: %#v", body)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "request-1")
				w.Header().Set("ETag", `"rev-1"`)
				_, _ = io.WriteString(w, `{"partial":true,"durableTotal":9007199254740993,"liveCount":1,"items":[{"type":"local"},{"type":"indexed","conflicts":["x"]},{"type":"live","url":"https://do-not-fetch.test"}],"extensions":{"unknown":null},"nextCursor":"opaque","diagnostics":["partial"]}`)
			}))
			defer server.Close()
			var out bytes.Buffer
			if err := runRemote(context.Background(), test.args, remoteTestEnvironment(server.URL, "test-token"), &out); err != nil {
				t.Fatal(err)
			}
			var envelope remoteEnvelope
			if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Meta.RequestID != "request-1" || envelope.Meta.ETag != `"rev-1"` || !bytes.Contains(envelope.Data, []byte("9007199254740993")) || !bytes.Contains(envelope.Data, []byte(`"unknown":null`)) {
				t.Fatalf("lossy output: %s", &out)
			}
		})
	}
}

func TestRemoteErrorsAndBounds(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name              string
		status            int
		media, body, code string
	}{
		{"problem", 403, "application/problem+json", `{"code":"forbidden","detail":"secret-token \u001b]52;clipboard\u0007\u202e","status":500}`, "forbidden"},
		{"missing", 404, "application/problem+json", `{"code":"not_found","detail":"gone"}`, "not_found"},
		{"rate limited", 429, "application/problem+json", `{"code":"rate_limited","detail":"retry later"}`, "rate_limited"},
		{"unavailable", 503, "application/problem+json", `{"code":"unavailable","detail":"retry later"}`, "unavailable"},
		{"html", 502, "text/html", "secret-token private proxy body", "http_error"},
		{"wrong problem fields", 500, "application/problem+json", `{"code":4,"detail":{}}`, "http_error"},
		{"duplicate problem", 400, "application/problem+json", `{"detail":"one","detail":"secret-token"}`, "http_error"},
		{"duplicate success", 200, "application/json", `{"a":1,"a":2}`, "invalid_response"},
		{"trailing", 200, "application/json", `{} {}`, "invalid_response"},
		{"array", 200, "application/json", `[]`, "invalid_response"},
		{"bad media", 200, "text/plain", `{}`, "invalid_response"},
		{"invalid utf8", 200, "application/json", "{\"x\":\"" + string([]byte{0xff}) + "\"}", "invalid_response"},
		{"large", 200, "application/json", strings.Repeat("x", remoteMaxBody+1), "invalid_response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", test.media)
				w.Header().Set("X-Request-ID", "request-2")
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			var out bytes.Buffer
			err := runRemote(context.Background(), []string{"capabilities"}, remoteTestEnvironment(server.URL, "secret-token"), &out)
			var failure remoteAPIError
			if !errors.As(err, &failure) || failure.Code != test.code || failure.Status != test.status || failure.RequestID != "request-2" || out.Len() != 0 || requests.Load() != 1 {
				t.Fatalf("failure: %v output %s", err, &out)
			}
			if strings.ContainsAny(err.Error(), "\x1b\a\u202e") || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "private proxy") {
				t.Fatalf("unsafe error: %q", err)
			}
		})
	}
}

func TestRemoteMountedTokenRotation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mount := filepath.Join(dir, "access-token")
	var requests atomic.Int32
	var wantToken atomic.Pointer[string]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+*wantToken.Load() || r.Header.Get("Cookie") != "" {
			t.Error("stale credentials or a retained cookie reached the API")
		}
		w.Header().Set("Content-Type", "application/json")
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "not-a-cli-credential", HttpOnly: true, SameSite: http.SameSiteStrictMode}) // #nosec G124 -- Loopback HTTP fixture deliberately tests refusal to retain an otherwise sendable cookie.
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	// Each invocation opens the symlink again, as projected secrets require.
	env := func(name string) string {
		if name == "CATALOG_REMOTE_TOKEN_FILE" {
			return mount
		}
		return remoteTestEnvironment(server.URL, "ignored-env-token")(name)
	}
	for index, token := range []string{"first-token", "rotated-token"} {
		path := filepath.Join(dir, token)
		if err := os.WriteFile(path, []byte("\n"+token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "next")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(link, mount); err != nil {
			t.Fatal(err)
		}
		wantToken.Store(&token)
		if err := runRemote(context.Background(), []string{"capabilities"}, env, io.Discard); err != nil || requests.Load() != int32(index+1) {
			t.Fatalf("mounted token read failed: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "rotated-token"), []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runRemote(context.Background(), []string{"capabilities"}, env, &output); err == nil || output.Len() != 0 || requests.Load() != 2 {
		t.Fatal("invalid mounted token fell back to another credential or contacted the API")
	}
}

func TestRemoteExactSizeLimits(t *testing.T) {
	t.Parallel()
	// Keep the JSON valid on both sides of the limit so the assertion cannot
	// accidentally pass because a parser rejects the oversized fixture.
	for _, size := range []int{remoteMaxBody, remoteMaxBody + 1} {
		body := `{"text":"` + strings.Repeat("x", size-len(`{"text":""}`)) + `"}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}))
		var output bytes.Buffer
		err := runRemote(context.Background(), []string{"capabilities"}, remoteTestEnvironment(server.URL, "token"), &output)
		server.Close()
		if size > remoteMaxBody {
			var failure remoteAPIError
			if !errors.As(err, &failure) || failure.Code != "invalid_response" || output.Len() != 0 {
				t.Fatalf("oversized object accepted: %v", err)
			}
		} else {
			var envelope remoteEnvelope
			if err != nil || json.Unmarshal(output.Bytes(), &envelope) != nil || string(envelope.Data) != body {
				t.Fatalf("exact-limit object rejected or changed: %v", err)
			}
		}
	}
	file := filepath.Join(t.TempDir(), "bounded-input")
	for _, size := range []int{remoteMaxToken, remoteMaxCA} {
		value := bytes.Repeat([]byte("x"), size)
		if err := os.WriteFile(file, value, 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := remoteReadFile(file, int64(size)); err != nil || !bytes.Equal(got, value) {
			t.Fatalf("exact-limit file rejected: %v", err)
		}
		if _, err := remoteReadFile(file, int64(size-1)); err == nil {
			t.Fatal("file above selected limit accepted")
		}
	}
}

func TestRemoteMetadataIsOpaque(t *testing.T) {
	t.Parallel()
	requestID := strings.Repeat("request", 50)
	etag := `"` + strings.Repeat("version", 200) + `"`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("ETag", etag)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	var output bytes.Buffer
	if err := runRemote(context.Background(), []string{"capabilities"}, remoteTestEnvironment(server.URL, "test-token"), &output); err != nil {
		t.Fatal(err)
	}
	var envelope remoteEnvelope
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Meta.RequestID != requestID || envelope.Meta.ETag != etag {
		t.Fatal("successful response metadata was rewritten")
	}
}

func TestRemoteBodyDeadline(t *testing.T) {
	t.Parallel()
	const prefix = `{"partial":`
	flushed := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, prefix)
		if err == nil {
			err = http.NewResponseController(w).Flush()
		}
		flushed <- err
		<-r.Context().Done()
	}))
	defer server.Close()
	// The safety bound must not count as the request deadline under test.
	safety, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	ctx, cancel := context.WithTimeout(safety, time.Second)
	defer cancel()
	transport := server.Client().Transport
	body := &catalogctlTrackedBody{}
	client := &http.Client{Timeout: 5 * time.Second, Transport: catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(request)
		if err != nil {
			return nil, err
		}
		body.Reader = response.Body
		// Observe real reads while preserving the network body's actual closer.
		response.Body = struct {
			io.Reader
			io.Closer
		}{Reader: body, Closer: response.Body}
		return response, nil
	})}
	result, err := exchangeRemote(ctx, client, remoteOptions{path: "/api/v1/capabilities"}, server.URL, "test-token")
	if safety.Err() != nil {
		t.Fatalf("fixture safety deadline fired: %v", safety.Err())
	}
	if body.bytesRead != len(prefix) {
		t.Fatalf("body phase not reached: read %d bytes, want %d; error=%v", body.bytesRead, len(prefix), err)
	}
	select {
	case flushErr := <-flushed:
		if flushErr != nil {
			t.Fatalf("partial response flush failed: %v", flushErr)
		}
	case <-safety.Done():
		t.Fatal("partial response flush was not observed")
	}
	if ctx.Err() != context.DeadlineExceeded || err == nil || err.Error() != "remote: request cancelled or deadline exceeded" || len(result.Data) != 0 || result.Meta != (controlclient.Metadata{}) {
		t.Fatalf("body deadline misreported: context=%v result=%+v error=%v", ctx.Err(), result, err)
	}
}

func TestRemoteUnsafeMetadataFailsWithoutRewriting(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"X-Request-ID", "ETag"} {
		for _, values := range [][]string{{"one", "two"}, {"reflect-test-token"}, {string([]byte{0xff})}} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				for _, value := range values {
					w.Header().Add(name, value)
				}
				_, _ = io.WriteString(w, `{}`)
			}))
			var output bytes.Buffer
			err := runRemote(context.Background(), []string{"capabilities"}, remoteTestEnvironment(server.URL, "test-token"), &output)
			server.Close()
			if err == nil || output.Len() != 0 || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("unsafe %s accepted or exposed: %v", name, err)
			}
		}
	}
}

func TestRemoteTLSRedirectsAndFiles(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer file-token" {
			t.Error("file did not override environment token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"state":"failed"}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("file-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args := make([]string, 0, 7)
	args = append(args, "job", "--id", "00000000-0000-7000-8000-000000000099", "--token-file", token)
	var out bytes.Buffer
	if err := runRemote(context.Background(), args, remoteTestEnvironment(server.URL, "ignored"), &out); err == nil || out.Len() != 0 || calls.Load() != 0 {
		t.Fatal("untrusted TLS accepted")
	}
	args = append(args, "--ca-file", ca)
	if err := runRemote(context.Background(), args, remoteTestEnvironment(server.URL, "ignored"), &out); err != nil || calls.Load() != 1 {
		t.Fatalf("private CA failed: %v", err)
	}
	if !strings.Contains(out.String(), `"state":"failed"`) {
		t.Fatal("failed Job status not returned")
	}
	if err := runRemote(context.Background(), args, remoteTestEnvironment(strings.Replace(server.URL, "127.0.0.1", "localhost", 1), "ignored"), io.Discard); err == nil || calls.Load() != 1 {
		t.Fatal("private CA bypassed hostname verification")
	}
	for _, file := range []string{dir, filepath.Join(dir, "absent"), filepath.Join(dir, "fifo")} {
		if strings.HasSuffix(file, "fifo") {
			if err := syscall.Mkfifo(file, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := remoteReadFile(file, remoteMaxToken); err == nil {
			t.Fatal("nonregular/missing file accepted")
		}
	}
	if _, err := remoteReadFile(token, 2); err == nil {
		t.Fatal("oversized file accepted")
	}
	for _, status := range []int{301, 302, 303, 307, 308} {
		var redirect *httptest.Server
		redirect = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/capabilities" {
				t.Error("same-origin redirect followed")
			}
			w.Header().Set("Location", redirect.URL+"/next")
			w.WriteHeader(status)
		}))
		err := runRemote(context.Background(), []string{"capabilities"}, remoteTestEnvironment(redirect.URL, "file-token"), io.Discard)
		redirect.Close()
		var failure remoteAPIError
		if !errors.As(err, &failure) || failure.Code != "redirect_refused" {
			t.Fatalf("redirect: %v", err)
		}
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", server.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	if err := runRemote(context.Background(), []string{"capabilities", "--ca-file", ca}, remoteTestEnvironment(redirect.URL, "file-token"), io.Discard); err == nil || calls.Load() != 1 {
		t.Fatal("cross-origin redirect followed")
	}
}

func TestRemoteCancellationAndSafeJSON(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	if err := runRemote(ctx, []string{"capabilities"}, remoteTestEnvironment(server.URL, "token"), &output); err == nil || err.Error() != "remote: request cancelled or deadline exceeded" || output.Len() != 0 {
		t.Fatalf("cancellation misreported: error=%v output=%q", err, output.String())
	}
	if err := runRemote(context.Background(), []string{"capabilities", "--timeout", "20ms"}, remoteTestEnvironment(server.URL, "token"), &output); err == nil || err.Error() != "remote: request cancelled or deadline exceeded" || output.Len() != 0 {
		t.Fatalf("deadline misreported: error=%v output=%q", err, output.String())
	}
}

func TestRemoteAgainstCatalog(t *testing.T) {
	runtime := buildSmokeTestRuntime(t)
	defer runtime.Close()
	server := httptest.NewServer(runtime.HTTP.Handler())
	defer server.Close()
	options, err := parseSmokeOptions([]string{"--base-url", server.URL, "--run-id", "remote-contract"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := executeSmoke(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("connector HTTP output", func(t *testing.T) {
		checkConnectorHTTP(t, server.URL, report.Resources[0].Identifier, smokeFixtures(options.RunID)[0].Representation["data"])
	})
	call := func(token string, args ...string) (remoteEnvelope, error) {
		var out bytes.Buffer
		err := runRemote(context.Background(), args, remoteTestEnvironment(server.URL, token), &out)
		var envelope remoteEnvelope
		if err == nil {
			err = json.Unmarshal(out.Bytes(), &envelope)
		} else if out.Len() != 0 {
			t.Fatal("failure wrote success output")
		}
		return envelope, err
	}
	deny := func(token string, status int, code catalog.ErrorCode, args ...string) {
		t.Helper()
		_, err := call(token, args...)
		var failure remoteAPIError
		if !errors.As(err, &failure) || failure.Status != status || failure.Code != string(code) || failure.RequestID == "" {
			t.Fatalf("expected HTTP %d %s with request ID, got %v", status, code, err)
		}
	}
	if _, err := call("local-reader", "capabilities"); err != nil {
		t.Fatal(err)
	}
	deny("invalid-token", http.StatusUnauthorized, catalog.CodeUnauthenticated, "capabilities")
	cursor := ""
	seen := map[string]bool{}
	for range 3 {
		envelope, err := call("local-reader", "search", "--tag", "remote-contract", "--limit", "1", "--cursor", cursor)
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Items []struct {
				Type  string                     `json:"type"`
				Local catalog.CatalogSearchLocal `json:"local"`
			} `json:"items"`
			NextCursor string `json:"nextCursor"`
			Partial    bool   `json:"partial"`
		}
		if err := json.Unmarshal(envelope.Data, &page); err != nil || len(page.Items) != 1 || page.Partial {
			t.Fatalf("page: %s %v", envelope.Data, err)
		}
		id := page.Items[0].Local.Release.ID
		if seen[id] {
			t.Fatal("duplicate page")
		}
		seen[id] = true
		cursor = page.NextCursor
		if _, err := call("local-reader", "record", "--id", id); err != nil {
			t.Fatal(err)
		}
		deny("other-tenant-admin", http.StatusNotFound, catalog.CodeNotFound, "record", "--id", id)
	}
	if cursor != "" || len(seen) != 3 {
		t.Fatal("incomplete continuation")
	}
	deny("local-reader", http.StatusBadRequest, catalog.CodeInvalidArgument, "search", "--cursor", "invalid")
	deny("local-reader", http.StatusNotFound, catalog.CodeNotFound, "record", "--id", report.Resources[0].ResourceID)
	deny("local-reader", http.StatusNotFound, catalog.CodeNotFound, "job", "--id", report.Resources[0].ResourceID)
}

type remoteBrokenWriter struct{ short bool }

func (w remoteBrokenWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, errors.New("sensitive writer internals")
}

func TestRemoteLocalFailures(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	for _, short := range []bool{false, true} {
		err := runRemote(context.Background(), []string{"capabilities"}, remoteTestEnvironment(server.URL, "token"), remoteBrokenWriter{short})
		if err == nil || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("write failure: %v", err)
		}
	}
}

func TestRemoteResponseHeaderAndReadFailures(t *testing.T) {
	t.Parallel()
	for _, largeHeaders := range []bool{true, false} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if largeHeaders {
				w.Header().Set("X-Large", strings.Repeat("x", remoteMaxHeaders*2))
			} else {
				w.Header().Set("Content-Length", "100")
			}
			_, _ = io.WriteString(w, `{}`)
		}))
		var out bytes.Buffer
		err := runRemote(context.Background(), []string{"capabilities"}, remoteTestEnvironment(server.URL, "token"), &out)
		server.Close()
		if err == nil || out.Len() != 0 {
			t.Fatalf("malformed response accepted: %v", err)
		}
	}
}

// Real HTTP verifies the public flag, bounded reader and lossless output path
// together. Ingestion and exact stored-byte integrity use the repository suite.
func TestRemoteLargeExactRecordBudget(t *testing.T) {
	data := `{"versionRevision":{"representations":[{"content":"` + strings.Repeat("x", remoteMaxBody) + `"}]},"extension":9007199254740993}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/catalog/resources/00000000-0000-7000-8000-000000000001" {
			t.Error("wrong detail route")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, data)
	}))
	defer server.Close()
	args := make([]string, 0, 5)
	args = append(args, "record", "--id", "00000000-0000-7000-8000-000000000001")
	var output bytes.Buffer
	env := remoteTestEnvironment(server.URL, "fixture-token")
	if err := runRemote(t.Context(), args, env, &output); err == nil || !strings.Contains(err.Error(), "configured byte limit") || output.Len() != 0 {
		t.Fatal("default overflow did not fail clearly")
	}
	args = append(args, "--max-response-bytes", strconv.Itoa(len(data)))
	if err := runRemote(t.Context(), args, env, &output); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	// Map serialization can reorder object members; decode using json.Number.
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded["extension"]) != "9007199254740993" || !bytes.Contains(decoded["versionRevision"], []byte(strings.Repeat("x", remoteMaxBody))) {
		t.Fatal("large response changed bytes or precision")
	}
}

func TestRemoteTrustCommandTransport(t *testing.T) {
	const source = "00000000-0000-7000-8000-000000000010"
	path := filepath.Join(t.TempDir(), "selection.json")
	raw := `{"sourceRevision":1,"identifier":"urn:air:peer.example:agents:one","versionPresent":false,"mediaType":"application/a2a-agent-card+json","delivery":"url","exactEntryDigest":"sha256:` + strings.Repeat("a", 64) + `","text":"one","reason":"Verify registration"}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer owned-token" {
			t.Error("missing command authority")
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/federation/sources/"+source+"/trust-assessments") {
			t.Error("unexpected destination")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			if r.Header.Get("Idempotency-Key") != "trust-request-1" {
				t.Error("missing command key")
			}
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["identifier"] != "urn:air:peer.example:agents:one" {
				t.Error("selection changed")
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":"`+source+`","state":"queued"}`)
		} else {
			_, _ = io.WriteString(w, `{"current":false,"applicability":"historical","result":{"evidence":{"receipt":"ZXhhY3Q="}}}`)
		}
	}))
	defer server.Close()
	for _, args := range [][]string{{"trust-request", "--source-id", source, "--request-file", path, "--idempotency-key", "trust-request-1"}, {"trust-result", "--source-id", source, "--id", source}} {
		var out bytes.Buffer
		if err := runRemote(t.Context(), args, remoteTestEnvironment(server.URL, "owned-token"), &out); err != nil || out.Len() == 0 {
			t.Fatalf("command=%v output=%s", err, &out)
		}
	}
	if calls != 2 {
		t.Fatalf("command contacts=%d", calls)
	}
}
