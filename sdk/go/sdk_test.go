package catalogclient

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type doerFunc func(*http.Request) (*http.Response, error)

func TestOpenMetadataLiteralKeys(t *testing.T) {
	const wire = `{"additional_properties":{"must":"stay nested"},"model_extra":null,"values":[null,false,""]}`
	var expected map[string]json.RawMessage
	if err := json.Unmarshal([]byte(wire), &expected); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{
		"additional_properties": map[string]any{"must": "stay nested"},
		"model_extra":           nil,
		"values":                []any{nil, false, ""},
	}
	for _, tc := range []struct {
		name        string
		constructed any
		decoded     any
	}{
		{"create", &CreateListingRevision_Extensions{AdditionalProperties: values}, &CreateListingRevision_Extensions{}},
		{"read", &ListingRevision_Extensions{AdditionalProperties: values}, &ListingRevision_Extensions{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.constructed)
			if err != nil || !bytes.Equal(encoded, want) {
				t.Fatal("constructor changed literal metadata keys or values", string(encoded), err)
			}
			if err := json.Unmarshal([]byte(wire), tc.decoded); err != nil {
				t.Fatal(err)
			}
			decoded, err := json.Marshal(tc.decoded)
			if err != nil || !bytes.Equal(decoded, want) {
				t.Fatal("decoder changed literal metadata keys or values", string(decoded), err)
			}
		})
	}
}

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func unitClient(t *testing.T, config Config, doer HttpRequestDoer) *SDK {
	t.Helper()
	if config.Origin == "" {
		config.Origin = "https://catalog.example"
	}
	c, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	testBoundary(t, c).next = doer
	return c
}

func testBoundary(t *testing.T, c *SDK) *boundedClient {
	t.Helper()
	generated, ok := c.ClientInterface.(*Client)
	if !ok {
		t.Fatal("unexpected generated client")
	}
	boundary, ok := generated.Client.(*boundedClient)
	if !ok {
		t.Fatal("missing maintained boundary")
	}
	return boundary
}

func response(body string, status int, media string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {media}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: -1}
}

func TestConfiguration(t *testing.T) {
	for _, config := range []Config{
		{}, {Origin: "https://secret@catalog.example"}, {Origin: "https://catalog.example/path"},
		{Origin: "https://catalog.example?token=SECRET"}, {Origin: "https://catalog.example#SECRET"},
		{Origin: "https://catalog.example?"}, {Origin: "https://catalog.example#"},
		{Origin: "http://catalog.example", AllowLoopbackHTTP: true}, {Origin: "http://127.0.0.1"},
		{Origin: "https://catalog.example:0"}, {Origin: "https://catalog.example:65536"}, {Origin: "https://catalog.example:"},
		{Origin: "https://catalog.example", Token: "SECRET\n"},
		{Origin: "https://catalog.example", Token: "SECRET", TokenProvider: func(context.Context) (string, error) { return "", nil }},
		{Origin: "https://catalog.example", Timeout: -1}, {Origin: "https://catalog.example", ResponseBytes: -1},
		{Origin: "https://catalog.example", RequestBytes: MaxBodyBytes + 1}, {Origin: "https://catalog.example", HeaderBytes: -1},
	} {
		if c, err := New(config); err == nil {
			c.Close()
			t.Fatalf("accepted invalid configuration: origin=%q", config.Origin)
		} else if strings.Contains(err.Error(), "SECRET") {
			t.Fatal("configuration leaked")
		}
	}
	for _, origin := range []string{"https://catalog.example", "https://catalog.example/", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		roots := x509.NewCertPool()
		c, err := New(Config{Origin: origin, AllowLoopbackHTTP: true, RootCAs: roots})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		boundary := testBoundary(t, c)
		if boundary.config.Timeout != DefaultTimeout || boundary.config.ResponseBytes != DefaultResponseBytes || boundary.config.RequestBytes != DefaultRequestBytes || c.transport.MaxResponseHeaderBytes != DefaultHeaderBytes || c.transport.Proxy != nil || c.transport.DisableKeepAlives || c.transport.TLSClientConfig.InsecureSkipVerify || c.transport.TLSClientConfig.RootCAs == roots {
			t.Fatal("unsafe or inconsistent defaults")
		}
	}
}

type trackedBody struct {
	io.Reader
	count    int
	closed   bool
	closeErr bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.count += n
	return n, err
}
func (b *trackedBody) Close() error {
	b.closed = true
	if b.closeErr {
		return errors.New("SECRET close")
	}
	return nil
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("SECRET read") }

type cancelingReader struct {
	cancel context.CancelFunc
	reader io.Reader
}

func (r cancelingReader) Read(p []byte) (int, error) {
	r.cancel()
	if r.reader != nil {
		return r.reader.Read(p)
	}
	return 0, errors.New("SECRET interrupted response")
}

func TestInterruptedResponseRetainsCorrelation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &trackedBody{Reader: cancelingReader{cancel: cancel}}
	c := unitClient(t, Config{}, doerFunc(func(*http.Request) (*http.Response, error) {
		r := response("", http.StatusAccepted, "application/json")
		r.Header.Set("X-Request-ID", "accepted-request")
		r.Body = body
		return r, nil
	}))
	r, err := c.GetCapabilities(ctx)
	if r != nil {
		_ = r.Body.Close()
	}
	var diagnostic *Error
	if r != nil || !errors.Is(err, context.Canceled) || !errors.As(err, &diagnostic) || diagnostic.StatusCode != http.StatusAccepted || diagnostic.RequestID != "accepted-request" || !body.closed || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("interrupted response lost correlation, cleanup or safe classification", err)
	}
}

func TestBoundedResponseBeforeGeneratedParsing(t *testing.T) {
	for _, tc := range []struct {
		name             string
		length           int
		broken, closeErr bool
		encoding         string
		code             string
	}{
		{"exact", 32, false, false, "", ""}, {"overflow", 4096, false, false, "", "response_over_limit"},
		{"read", 0, true, false, "", "response_unreadable"}, {"close", 32, false, true, "", "response_unreadable"},
		{"compressed", 32, false, false, "gzip", "unsupported_content_encoding"}, {"identity", 32, false, false, "identity", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reader io.Reader = strings.NewReader(`{}` + strings.Repeat(" ", max(tc.length-2, 0)))
			if tc.broken {
				reader = brokenReader{}
			}
			body := &trackedBody{Reader: reader, closeErr: tc.closeErr}
			c := unitClient(t, Config{ResponseBytes: 32}, doerFunc(func(*http.Request) (*http.Response, error) {
				r := response("", 200, "application/json")
				r.Body = body
				r.Header.Set("Content-Encoding", tc.encoding)
				r.Header.Set("X-Request-ID", "correlation")
				return r, nil
			}))
			r, err := c.GetCapabilitiesWithResponse(context.Background())
			if (err != nil) != (tc.code != "") || !body.closed || body.count > 33 {
				t.Fatalf("failure=%v closed=%v read=%d", err, body.closed, body.count)
			}
			if err != nil {
				var diagnostic *Error
				if !errors.As(err, &diagnostic) || diagnostic.Code != tc.code || diagnostic.StatusCode != http.StatusOK || diagnostic.RequestID != "correlation" || strings.Contains(err.Error(), "SECRET") {
					t.Fatal("incorrect or unsafe response diagnostic", err)
				}
				return
			}
			if r.JSON200 == nil || len(r.Body) != 32 || r.HTTPResponse.Header.Get("X-Request-ID") != "correlation" {
				t.Fatal("typed/raw response changed")
			}
		})
	}
}

func TestResponseLimitOverrideAndNoTruncation(t *testing.T) {
	for _, limit := range []int64{0, -1, MaxBodyBytes + 1} {
		if _, err := WithResponseLimit(context.Background(), limit); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	c := unitClient(t, Config{ResponseBytes: 2}, doerFunc(func(*http.Request) (*http.Response, error) { return response("{}  ", 200, "application/json"), nil }))
	ctx, err := WithResponseLimit(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.GetCapabilitiesWithResponse(ctx)
	if err != nil || len(r.Body) != 4 {
		t.Fatal("valid override failed", err)
	}
	if _, err = c.GetCapabilitiesWithResponse(context.Background()); err == nil {
		t.Fatal("override changed default")
	}
}

func TestConcurrentCallLimitsAndCredentials(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type tokenKey struct{}
		type outcome struct {
			token    string
			response *GetCapabilitiesResponse
			err      error
		}
		var providers atomic.Int32
		bodies := make(chan *trackedBody, 3)
		release := make(chan struct{})
		c := unitClient(t, Config{ResponseBytes: 2, TokenProvider: func(ctx context.Context) (string, error) {
			providers.Add(1)
			token, ok := ctx.Value(tokenKey{}).(string)
			if !ok {
				return "", errors.New("missing call credential context")
			}
			return token, nil
		}}, doerFunc(func(r *http.Request) (*http.Response, error) {
			token, ok := r.Context().Value(tokenKey{}).(string)
			if !ok {
				return nil, errors.New("missing call request context")
			}
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("another call's credentials were used")
			}
			body := &trackedBody{Reader: strings.NewReader("{}  ")}
			bodies <- body
			<-release
			result := response("", http.StatusOK, "application/json")
			result.Body = body
			result.Header.Set("X-Request-ID", token)
			return result, nil
		}))
		results := make(chan outcome, 3)
		for _, tc := range []struct {
			token string
			limit int64
		}{{"default", 0}, {"smaller", 1}, {"larger", 4}} {
			ctx := context.WithValue(context.Background(), tokenKey{}, tc.token)
			if tc.limit != 0 {
				var err error
				ctx, err = WithResponseLimit(ctx, tc.limit)
				if err != nil {
					t.Fatal(err)
				}
			}
			go func() {
				result, err := c.GetCapabilitiesWithResponse(ctx)
				results <- outcome{tc.token, result, err}
			}()
		}
		// All three calls must reach the injected boundary before any completes.
		synctest.Wait()
		if len(bodies) != 3 {
			t.Error("concurrent calls did not reach the boundary")
		}
		close(release)
		for range 3 {
			result := <-results
			if result.token == "larger" {
				if result.err != nil || result.response == nil || result.response.JSON200 == nil || string(result.response.Body) != "{}  " || result.response.HTTPResponse.Header.Get("X-Request-ID") != result.token {
					t.Error("larger per-call budget failed", result.err)
				}
			} else {
				var diagnostic *Error
				if result.response != nil || !errors.As(result.err, &diagnostic) || diagnostic.Code != "response_over_limit" || diagnostic.RequestID != result.token {
					t.Error("another call's budget or correlation was used", result.err)
				}
			}
		}
		if providers.Load() != 3 {
			t.Error("credential provider did not run exactly once per call")
		}
		close(bodies)
		for body := range bodies {
			if !body.closed {
				t.Error("concurrent response left its input open")
			}
		}
	})
}

type privateMarshalFailure struct{}

func (privateMarshalFailure) MarshalJSON() ([]byte, error) {
	return nil, errors.New("SECRET native evidence")
}

func TestTypedMarshalFailureBeforeCredentialsAndTransmission(t *testing.T) {
	providers, calls := 0, 0
	c := unitClient(t, Config{TokenProvider: func(context.Context) (string, error) {
		providers++
		return "explicit", nil
	}}, doerFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		data, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Contains(data, []byte(`"name":"Example"`)) || r.Header.Get("Idempotency-Key") != "original-key" {
			t.Error("valid typed command changed", err)
		}
		_ = r.Body.Close()
		return response("{}", http.StatusCreated, "application/json"), nil
	}))
	data := map[string]interface{}{"name": "Example"}
	body := CreateResourceVersionJSONRequestBody{Version: "1.0.0", Representations: []RepresentationInput{{Role: "primary", MediaType: "application/a2a-agent-card+json", Data: &data}}}
	result, err := c.CreateResourceVersionWithResponse(context.Background(), UUID{}, &CreateResourceVersionParams{IdempotencyKey: "original-key"}, body)
	if err != nil || result.JSON201 == nil || providers != 1 || calls != 1 {
		t.Fatal("valid typed control failed", err)
	}
	data["name"] = privateMarshalFailure{}
	result, err = c.CreateResourceVersionWithResponse(context.Background(), UUID{}, &CreateResourceVersionParams{IdempotencyKey: "original-key"}, body)
	var diagnostic *Error
	if result != nil || !errors.As(err, &diagnostic) || diagnostic.Code != "request_or_decode_failed" || strings.Contains(err.Error(), "SECRET") || providers != 1 || calls != 1 {
		t.Fatal("marshal failure exposed evidence or reached credentials/transport", err)
	}
}

func TestCredentialsAndDestinationBeforeTransmission(t *testing.T) {
	providers, calls := 0, 0
	c := unitClient(t, Config{TokenProvider: func(context.Context) (string, error) { providers++; return "current-token", nil }}, doerFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		for name := range r.Header {
			for _, controlled := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Accept-Encoding"} {
				if strings.EqualFold(name, controlled) && name != controlled {
					t.Fatalf("uncontrolled header variant %q", name)
				}
			}
		}
		if r.Header.Get("Authorization") != "Bearer current-token" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Fatal("credential boundary bypassed")
		}
		return response(`{}`, 200, "application/json"), nil
	}))
	editor := func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer stale")
		r.Header.Set("Cookie", "SECRET")
		r.Header.Set("Proxy-Authorization", "SECRET")
		// Editors can assign map keys directly without Go's canonicalization.
		r.Header["authorization"] = []string{"Bearer stale-lowercase"}
		r.Header["cOoKiE"] = []string{"SECRET"}
		r.Header["proxy-authorization"] = []string{"SECRET"}
		r.Header["accept-encoding"] = []string{"gzip"}
		return nil
	}
	for range 2 {
		r, err := c.GetCapabilitiesWithResponse(context.Background(), editor)
		if err != nil || r.JSON200 == nil {
			t.Fatal(err)
		}
	}
	for _, edit := range []RequestEditorFn{
		func(_ context.Context, r *http.Request) error { r.URL.Host = "other.example"; return nil },
		func(_ context.Context, r *http.Request) error { r.Host = "other.example"; return nil },
		func(_ context.Context, r *http.Request) error {
			r.URL.Opaque = "https://other.example/api/v1/capabilities"
			return nil
		},
		func(_ context.Context, r *http.Request) error {
			r.URL.User = url.UserPassword("SECRET", "SECRET")
			return nil
		},
		func(context.Context, *http.Request) error { return errors.New("SECRET editor") },
	} {
		if _, err := c.GetCapabilitiesWithResponse(context.Background(), edit); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("unsafe editor accepted")
		}
	}
	if calls != 2 || providers != 2 {
		t.Fatal("rejected request reached credentials or transport")
	}
	for _, provider := range []func(context.Context) (string, error){
		func(context.Context) (string, error) { return "SECRET\r\n", nil },
		func(context.Context) (string, error) { return "", errors.New("SECRET provider") },
	} {
		bad := unitClient(t, Config{TokenProvider: provider}, doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid credential transmitted")
			return nil, errors.New("unexpected transmission")
		}))
		if _, err := bad.GetCapabilitiesWithResponse(context.Background()); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("provider error exposed")
		}
	}
	anonymous := unitClient(t, Config{}, doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("anonymous call authenticated")
		}
		return response(`{}`, 200, "application/json"), nil
	}))
	if _, err := anonymous.GetCapabilitiesWithResponse(context.Background(), editor); err != nil {
		t.Fatal(err)
	}
}

func TestRawRequestBoundAndSingleAttempt(t *testing.T) {
	for _, payload := range []string{"", "{}", "{}" + strings.Repeat(" ", 31)} {
		calls := 0
		body := &trackedBody{Reader: strings.NewReader(payload)}
		c := unitClient(t, Config{RequestBytes: 32}, doerFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.GetBody != nil {
				t.Fatal("mutation can rewind")
			}
			data, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(data, []byte(payload)) {
				t.Fatal("raw request changed")
			}
			if r.Header.Get("Idempotency-Key") != "caller-key" || r.Header.Get("If-Match") != `"opaque"` {
				t.Fatal("command binding changed")
			}
			return nil, errors.New("SECRET uncertain write")
		}))
		result, err := c.CreateResourceVersionWithBody(context.Background(), UUID{}, &CreateResourceVersionParams{IdempotencyKey: "caller-key"}, "application/json", body, func(_ context.Context, r *http.Request) error { r.Header.Set("If-Match", `"opaque"`); return nil })
		if result != nil {
			_ = result.Body.Close()
			t.Fatal("uncertain mutation returned a response")
		}
		wantCalls := 1
		if len(payload) > 32 {
			wantCalls = 0
		}
		if err == nil || strings.Contains(err.Error(), "SECRET") || !body.closed || body.count > 33 || calls != wantCalls {
			t.Fatal("unbounded or repeated request", err, calls)
		}
	}
}

func TestSafeTypedErrorsAndContextClassification(t *testing.T) {
	c := unitClient(t, Config{}, doerFunc(func(*http.Request) (*http.Response, error) {
		r := response(`{"createdAt":"SECRET invalid time"}`, 200, "application/json")
		r.Header.Set("X-Request-ID", "decode-request")
		return r, nil
	}))
	_, err := c.GetNamespaceWithResponse(context.Background(), UUID{})
	var diagnostic *Error
	if !errors.As(err, &diagnostic) || strings.Contains(err.Error(), "SECRET") || diagnostic.StatusCode != http.StatusOK || diagnostic.RequestID != "decode-request" {
		t.Fatal("typed decoder cause exposed", err)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		c := unitClient(t, Config{}, doerFunc(func(*http.Request) (*http.Response, error) { return nil, cause }))
		_, err := c.GetCapabilitiesWithResponse(context.Background())
		if !errors.Is(err, cause) {
			t.Fatal("context cause lost", err)
		}
	}
	if safeError(nil) != nil || validToken(strings.Repeat("a", (32<<10)+1)) {
		t.Fatal("invalid diagnostic or token")
	}
}

func TestTransportFailureDiscardsResponseAndClosesBody(t *testing.T) {
	for _, cause := range []error{errors.New("SECRET transport failure"), context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader("SECRET response body"), closeErr: true}
			calls := 0
			client := unitClient(t, Config{}, doerFunc(func(*http.Request) (*http.Response, error) {
				calls++
				r := response("", http.StatusOK, "application/json")
				r.Body = body
				return r, cause
			}))
			got, err := client.GetCapabilitiesWithResponse(t.Context())
			var diagnostic *Error
			if got != nil || !errors.As(err, &diagnostic) || strings.Contains(err.Error(), "SECRET") || !body.closed || body.count != 0 || calls != 1 {
				t.Fatalf("failed response was consumed, disclosed, retried or left open: result=%+v error=%v calls=%d closed=%t read=%d", got, err, calls, body.closed, body.count)
			}
			if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
				if !errors.Is(err, cause) {
					t.Fatal("context cause lost", err)
				}
			} else if diagnostic.Code != "request_or_decode_failed" {
				t.Fatal("unsafe transport diagnostic classification", err)
			}
		})
	}
}

func TestRejectedRawInputsDoNotReachTransport(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reader   io.Reader
		closeErr bool
		editor   RequestEditorFn
	}{
		{name: "unreadable", reader: brokenReader{}},
		{name: "close-failure", reader: strings.NewReader(`{}`), closeErr: true},
		{name: "destination", reader: strings.NewReader(`{}`), editor: func(_ context.Context, r *http.Request) error { r.URL = nil; return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{Reader: tc.reader, closeErr: tc.closeErr}
			c := unitClient(t, Config{}, doerFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("rejected input reached transport")
				return nil, errors.New("unexpected transmission")
			}))
			var editors []RequestEditorFn
			if tc.editor != nil {
				editors = append(editors, tc.editor)
			}
			r, err := c.CreateNamespaceWithBody(context.Background(), &CreateNamespaceParams{IdempotencyKey: "caller-key"}, "application/json", body, editors...)
			if r != nil {
				_ = r.Body.Close()
			}
			if err == nil || !body.closed || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("input was accepted or leaked", err, body.closed)
			}
		})
	}
}

func TestNilHeadersAndProviderCancellation(t *testing.T) {
	c := unitClient(t, Config{Token: "explicit"}, doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer explicit" {
			t.Fatal("nil headers bypassed credentials")
		}
		return response(`{}`, 200, "application/json"), nil
	}))
	if _, err := c.GetCapabilitiesWithResponse(context.Background(), func(_ context.Context, r *http.Request) error { r.Header = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c = unitClient(t, Config{TokenProvider: func(context.Context) (string, error) { cancel(); return "explicit", nil }}, doerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("provider cancellation reached transport")
		return nil, errors.New("unexpected transmission")
	}))
	if _, err := c.GetCapabilitiesWithResponse(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("provider cancellation lost", err)
	}
}

func TestBodylessProblemAndPartialResponseBranches(t *testing.T) {
	cached := unitClient(t, Config{}, doerFunc(func(*http.Request) (*http.Response, error) { return response("", 304, ""), nil }))
	manifest, err := cached.GetARDManifestWithResponse(context.Background(), nil)
	if err != nil || manifest.StatusCode() != 304 || len(manifest.Body) != 0 {
		t.Fatal("not-modified response mishandled", err)
	}
	deleted := unitClient(t, Config{}, doerFunc(func(*http.Request) (*http.Response, error) { return response("", 204, ""), nil }))
	removed, err := deleted.DeleteResourceWithResponse(context.Background(), UUID{}, &DeleteResourceParams{IdempotencyKey: "caller-key", IfMatch: `"revision"`}, DeleteResourceJSONRequestBody{Reason: "Remove draft"})
	if err != nil || removed.StatusCode() != 204 || len(removed.Body) != 0 {
		t.Fatal("no-content response mishandled", err)
	}
	calls := 0
	c := unitClient(t, Config{}, doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		r := response(`{"items":[],"partial":true,"nextCursor":"opaque+/=","diagnostics":[],"generation":"fixture","profile":"lexical","tier":"lexical","durableTotal":0,"liveCount":0}`, 200, "application/json")
		r.Header.Set("ETag", `"etag"`)
		r.Header.Set("Retry-After", "5")
		return r, nil
	}))
	r, err := c.SearchCatalogWithResponse(context.Background(), SearchCatalogJSONRequestBody{})
	if err != nil || r.JSON200 == nil || !r.JSON200.Partial || r.JSON200.NextCursor == nil || *r.JSON200.NextCursor != "opaque+/=" || calls != 1 || r.HTTPResponse.Header.Get("ETag") != `"etag"` {
		t.Fatal("partial page changed", err)
	}
	problem := unitClient(t, Config{}, doerFunc(func(*http.Request) (*http.Response, error) {
		return response(`{"code":"FORBIDDEN","status":403,"title":"Forbidden","type":"about:blank"}`, 403, "application/problem+json"), nil
	}))
	p, err := problem.GetCapabilitiesWithResponse(context.Background())
	if err != nil || p.ApplicationproblemJSONDefault == nil || p.ApplicationproblemJSONDefault.Code != "FORBIDDEN" {
		t.Fatal("Control Problem lost", err)
	}
	ard := unitClient(t, Config{}, doerFunc(func(*http.Request) (*http.Response, error) {
		return response(`{"errorCode":"INVALID_ARGUMENT","message":"Unsupported filter"}`, 400, "application/json"), nil
	}))
	a, err := ard.SearchARDRegistryWithResponse(context.Background(), ARDSearch{})
	if err != nil || a.StatusCode() != 400 || a.JSONDefault == nil || a.JSONDefault.ErrorCode != "INVALID_ARGUMENT" {
		t.Fatal("ARD error lost", err)
	}
}

func TestCancellationBeforeTransmissionClosesInput(t *testing.T) {
	for _, phase := range []string{"before", "reader-error", "reader-eof", "provider"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			providers := 0
			config := Config{TokenProvider: func(context.Context) (string, error) {
				providers++
				cancel()
				return "", errors.New("SECRET provider canceled")
			}}
			if phase == "before" {
				cancel()
			}
			c := unitClient(t, config, doerFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("canceled request reached transport")
				return nil, errors.New("unexpected transmission")
			}))
			var input io.Reader = strings.NewReader(`{}`)
			if strings.HasPrefix(phase, "reader-") {
				var finish io.Reader
				if phase == "reader-eof" {
					finish = input
				}
				input = cancelingReader{cancel: cancel, reader: finish}
			}
			body := &trackedBody{Reader: input}
			result, err := c.CreateNamespaceWithBody(ctx, &CreateNamespaceParams{IdempotencyKey: "caller-key"}, "application/json", body)
			if result != nil {
				_ = result.Body.Close()
				t.Fatal("canceled mutation returned a response")
			}
			wantProviders := 0
			if phase == "provider" {
				wantProviders = 1
			}
			if !errors.Is(err, context.Canceled) || !body.closed || providers != wantProviders {
				t.Fatal("cancellation or cleanup lost", err, body.closed, providers)
			}
		})
	}
}

func TestSafeErrorNilAndUntrustedCodes(t *testing.T) {
	var typedNil *Error
	for _, cause := range []error{typedNil, &Error{Code: "SECRET"}, errors.New("SECRET")} {
		if err := safeError(cause); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("unsafe diagnostic", err)
		}
	}
	if typedNil.Unwrap() != nil || strings.Contains(typedNil.Error(), "SECRET") {
		t.Fatal("nil diagnostic mishandled")
	}
}

func TestBoundedClientMalformedResponses(t *testing.T) {
	for _, r := range []*http.Response{nil, {StatusCode: 200}} {
		c := unitClient(t, Config{}, doerFunc(func(*http.Request) (*http.Response, error) { return r, nil }))
		result, err := c.GetCapabilities(context.Background())
		if result != nil {
			_ = result.Body.Close()
		}
		if err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	c := unitClient(t, Config{Timeout: time.Second}, doerFunc(func(r *http.Request) (*http.Response, error) {
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		return nil, errors.New("SECRET network")
	}))
	result, err := c.GetCapabilities(context.Background())
	if result != nil {
		_ = result.Body.Close()
	}
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("transport cause exposed")
	}
}
func TestPersonalLabelQueryPreservesLiteralValueAndOptionalPresence(t *testing.T) {
	for _, label := range []*string{nil, pointerLabel(" Case é%_ "), pointerLabel("")} {
		calls := 0
		client := unitClient(t, Config{Token: "selected"}, doerFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/personal/items" || r.URL.Query().Has("label") != (label != nil) || label != nil && r.URL.Query().Get("label") != *label {
				t.Fatal("saved label query changed", r.Method, r.URL.Path, r.URL.Query())
			}
			return response(`{"items":[]}`, 200, "application/json"), nil
		}))
		got, err := client.ListPersonalItemsWithResponse(t.Context(), &ListPersonalItemsParams{Label: label})
		if err != nil || got.JSON200 == nil || calls != 1 {
			t.Fatal("saved library read", got, err, calls)
		}
	}
}

func pointerLabel(value string) *string { return &value }

// A finite seeded corpus exercises semantic wire values independently of the
// generated encoders. Integer values remain inside every SDK's exact range.
func TestGeneratedWireSemantics(t *testing.T) {
	words := []string{"", "é", "日本語", "😀", "%+&=", "line\nvalue", "quoted\"value", "two words"}
	for seed := 0; seed < 32; seed++ {
		label := fmt.Sprintf("case-%d-%s", seed, words[seed%len(words)])
		selected := &label
		if seed%3 == 0 {
			selected = nil
		} else if seed%3 == 1 {
			selected = pointerLabel("")
		}
		calls := 0
		client := unitClient(t, Config{Token: "selected"}, doerFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			if request.URL.Scheme != "https" || request.URL.Host != "catalog.example" || request.URL.Path != "/api/v1/personal/items" || request.Method != http.MethodGet {
				t.Fatal("generated query changed fixed request destination", request.URL, request.Method)
			}
			want := url.Values{}
			if selected != nil {
				want.Set("label", *selected)
			}
			if !reflect.DeepEqual(request.URL.Query(), want) {
				t.Fatal("query meaning changed", seed, request.URL.Query(), want)
			}
			return response(`{"items":[]}`, http.StatusOK, "application/json"), nil
		}))
		result, err := client.ListPersonalItemsWithResponse(t.Context(), &ListPersonalItemsParams{Label: selected})
		if err != nil || result.JSON200 == nil || calls != 1 {
			t.Fatal("accepted generated query did not execute exactly once", seed, err, calls)
		}
		want := map[string]any{"additional_properties": map[string]any{"literal": words[seed%len(words)]}, "model_extra": nil,
			"values": []any{nil, false, "", label}, "count": float64(seed * 65537)}
		value := CreateListingRevision_Extensions{AdditionalProperties: want}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var actual map[string]any
		if err := json.Unmarshal(encoded, &actual); err != nil || !reflect.DeepEqual(actual, want) {
			t.Fatal("metadata meaning changed", seed, actual, want, err)
		}
		// Codec-only work has no transmission. Malformed typed marshaling has
		// a separately asserted zero-transmission control above.
		if calls != 1 {
			t.Fatal("codec work contacted a destination")
		}
	}
}
