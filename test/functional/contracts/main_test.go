// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
)

func TestMethodAwarePrivateExchange(t *testing.T) {
	t.Parallel()
	namespace := `{"id":"00000000-0000-7000-8000-000000000003","tenantId":"00000000-0000-7000-8000-000000000001","slug":"owned","displayName":"Owned","revision":1,"createdAt":"2026-09-07T00:00:00Z","updatedAt":"2026-09-07T00:00:00Z"}`
	for _, check := range []struct {
		method, path, body string
		status             int
		valid              bool
	}{
		{"POST", "/api/v1/namespaces", namespace, 201, true},
		{"PATCH", "/api/v1/namespaces/00000000-0000-7000-8000-000000000003", namespace, 200, true},
		{"GET", "/api/v1/namespaces?limit=1", `{"items":[]}`, 200, true},
		{"GET", "/api/v1/namespaces", namespace, 201, false},
		{"POST", "/api/v1/namespaces", `{"items":[]}`, 201, false},
		{"DELETE", "/api/v1/namespaces/00000000-0000-7000-8000-000000000003", "", 204, false},
		{"POST", "/api/v1/resources/00000000-0000-7000-8000-000000000003:delete", "", 204, true},
		{"POST", "/api/v1/resources/00000000-0000-7000-8000-000000000003:delete", "private-secret", 204, false},
		{"DELETE", "/api/v1/resources/00000000-0000-7000-8000-000000000003", "", 204, false},
		{"get", "/api/v1/namespaces", `{"items":[]}`, 200, false},
		{"GET", "/missing", `{}`, 200, false},
	} {
		raw, err := json.Marshal(validateInput{check.method, check.path, check.status, http.Header{"Content-Type": {"application/json"}}, []byte(check.body)})
		if err != nil {
			t.Fatal(err)
		}
		err = validateStdin(context.Background(), bytes.NewReader(raw))
		if (err == nil) != check.valid {
			t.Errorf("%s %s %d valid=%v: %v", check.method, check.path, check.status, check.valid, err)
		}
	}
	for _, raw := range []string{`{"method":"GET","method":"POST"}`, `{"unknown":"private-secret"}`, `{}`, `{} {}`} {
		if err := validateStdin(context.Background(), strings.NewReader(raw)); !errors.Is(err, errContract) {
			t.Fatal("accepted invalid input")
		}
	}
}

func TestContractSelectionAndJSONSchema2020(t *testing.T) {
	t.Parallel()
	document, err := loadContract(context.Background(), serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name, path, media, body string
		status, expected        int
		valid                   bool
	}{
		{"page", "/api/v1/namespaces", "application/json", `{"items":[]}`, 200, 200, true},
		{"wrong schema", "/api/v1/namespaces", "application/json", `{"items":"private-secret"}`, 200, 200, false},
		{"unknown member", "/api/v1/namespaces", "application/json", `{"items":[],"private-secret":true}`, 200, 200, false},
		{"duplicate", "/api/v1/namespaces", "application/json", `{"items":[],"items":[]}`, 200, 200, false},
		{"wrong status", "/api/v1/namespaces", "application/json", `{"items":[]}`, 201, 200, false},
		{"wrong media", "/api/v1/namespaces", "text/plain", `{"items":[]}`, 200, 200, false},
		{"invalid media", "/api/v1/namespaces", "bad;", `{"items":[]}`, 200, 200, false},
		{"undocumented", "/livez", "application/json", `{}`, 299, 299, false},
		{"unknown operation", "/missing", "application/json", `{}`, 200, 200, false},
		{"text", "/metrics", "text/plain; version=0.0.4", "metric 1\n", 200, 200, true},
		{"empty", "/public/v1/artifacts/{artifactId}", "", "", 304, 304, true},
		{"nonempty 304", "/public/v1/artifacts/{artifactId}", "", "private-secret", 304, 304, false},
	} {
		t.Run(check.name, func(t *testing.T) {
			result := exchange{check.status, http.Header{"Content-Type": []string{check.media}}, []byte(check.body)}
			err := validate(document, check.path, result, check.expected)
			if (err == nil) != check.valid {
				t.Fatalf("valid = %v; error = %v", check.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-secret") {
				t.Fatal("validator emitted private data")
			}
		})
	}
	// Prove the selected validator handles 2020-12 null rather than silently
	// treating a 3.1 schema as the older nullable keyword convention.
	schema := &openapi3.Schema{Type: &openapi3.Types{"string", "null"}}
	if err := schema.VisitJSON(nil, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatal(err)
	}
}

func TestProblemReplayPreservesExecutionRequestID(t *testing.T) {
	t.Parallel()
	document, err := loadContract(t.Context(), serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	result := exchange{503, http.Header{"Content-Type": {"application/problem+json"}, "Cache-Control": {"no-store"}, "X-Request-Id": {"current"}}, []byte(`{"type":"about:blank","title":"Unavailable","status":503,"detail":"Deadline exceeded","code":"REQUEST_DEADLINE_EXCEEDED","requestId":"original"}`)}
	if validateMethod(document, http.MethodPost, "/api/v1/namespaces", result, 503) == nil {
		t.Fatal("fresh response accepted mismatched request identity")
	}
	result.header.Set("Idempotency-Replayed", "true")
	if err := validateMethod(document, http.MethodPost, "/api/v1/namespaces", result, 503); err != nil {
		t.Fatal(err)
	}
	result.header.Del("X-Request-ID")
	if validateMethod(document, http.MethodPost, "/api/v1/namespaces", result, 503) == nil {
		t.Fatal("replay omitted current delivery identity")
	}
}

func TestPrivateExchangeHeaderAndInputBounds(t *testing.T) {
	t.Parallel()
	for _, check := range []struct {
		name    string
		headers http.Header
		valid   bool
	}{
		{"canonicalized", http.Header{"content-type": {"application/json"}}, true},
		{"case-fold duplicate", http.Header{"content-type": {"application/json"}, "Content-Type": {"application/json"}}, false},
		{"empty name", http.Header{"": {"secret"}}, false},
		{"empty values", http.Header{"Content-Type": {}}, false},
		{"injection", http.Header{"Content-Type": {"application/json\r\nX-Secret: value"}}, false},
		{"oversize", http.Header{"Content-Type": {"application/json"}, "X-Large": {strings.Repeat("x", 64<<10)}}, false},
	} {
		t.Run(check.name, func(t *testing.T) {
			raw, err := json.Marshal(validateInput{"GET", "/api/v1/namespaces", 200, check.headers, []byte(`{"items":[]}`)})
			if err != nil {
				t.Fatal(err)
			}
			if err := validateStdin(t.Context(), bytes.NewReader(raw)); (err == nil) != check.valid {
				t.Fatalf("valid=%t: %v", check.valid, err)
			}
		})
	}
	for _, path := range []string{"/api/v1/namespaces#fragment", "/api/v1/namespaces\n", strings.Repeat("x", 8193)} {
		raw, err := json.Marshal(validateInput{"GET", path, 200, nil, nil})
		if err != nil {
			t.Fatal(err)
		}
		if err := validateStdin(t.Context(), bytes.NewReader(raw)); err == nil {
			t.Fatal("accepted unsafe path")
		}
	}
	if err := validateStdin(t.Context(), strings.NewReader(strings.Repeat("x", (12<<20)+1))); err == nil {
		t.Fatal("accepted oversized input")
	}
}

func TestProblemHeadersRemainIndependentAssertions(t *testing.T) {
	t.Parallel()
	document, err := loadContract(context.Background(), serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"type":"about:blank","title":"Unauthorized","status":401,"detail":"Authentication required","code":"UNAUTHENTICATED","requestId":"test-request"}`)
	result := exchange{401, http.Header{"Content-Type": {"application/problem+json"}, "X-Request-Id": {"test-request"}, "Cache-Control": {"no-store"}}, body}
	if err := validate(document, "/api/v1/capabilities", result, 401); err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"X-Request-ID", "Cache-Control"} {
		modified := result
		modified.header = result.header.Clone()
		modified.header.Del(header)
		if err := validate(document, "/api/v1/capabilities", modified, 401); err == nil {
			t.Fatalf("accepted missing %s", header)
		}
	}
}

func TestPrivateExchangeRejectsMalformedEnvelopeAndReadFailure(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"method":1,"path":"/livez","status":200}`,
		`{"method":"GET","path":"/livez","status":200,"body":"not-base64!"}`,
		`{"method":"GET","path":"/livez","status":200,"headers":{"Content-Type":"text/plain"}}`,
	} {
		if err := validateStdin(t.Context(), strings.NewReader(raw)); !errors.Is(err, errContract) {
			t.Fatalf("malformed exchange error = %v", err)
		}
	}
	if err := validateStdin(t.Context(), failingExchangeReader{}); !errors.Is(err, errContract) || err.Error() != errContract.Error() {
		t.Fatalf("reader failure exposed an internal cause: %v", err)
	}
}

type failingExchangeReader struct{}

func (failingExchangeReader) Read([]byte) (int, error) {
	return 0, errors.New("private-secret reader failure")
}

func TestResponseBodyBoundAndProblemStatusCannotBeBypassedByReplay(t *testing.T) {
	t.Parallel()
	document, err := loadContract(t.Context(), serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	result := exchange{200, http.Header{"Content-Type": {"text/plain"}}, []byte(strings.Repeat("x", maximumResponse))}
	if err := validate(document, "/metrics", result, 200); err != nil {
		t.Fatalf("exact response limit rejected: %v", err)
	}
	result.body = append(result.body, 'x')
	if err := validate(document, "/metrics", result, 200); !errors.Is(err, errContract) {
		t.Fatal("over-limit body accepted")
	}
	for _, replay := range []string{"", "true"} {
		result = exchange{503, http.Header{"Content-Type": {"application/problem+json"}, "Cache-Control": {"no-store"}, "X-Request-Id": {"owned"}, "Idempotency-Replayed": {replay}}, []byte(`{"type":"about:blank","title":"Unavailable","status":503,"detail":"Owned unavailable dependency","code":"DEPENDENCY_UNAVAILABLE","requestId":"owned"}`)}
		if err := validateMethod(document, http.MethodPost, "/api/v1/namespaces", result, 503); err != nil {
			t.Fatalf("valid problem control rejected: %v", err)
		}
		// Change only the body status so schema rejection cannot hide a missing
		// comparison with the actual HTTP status, including replayed responses.
		result.body = bytes.Replace(result.body, []byte(`"status":503`), []byte(`"status":401`), 1)
		if err := validateMethod(document, http.MethodPost, "/api/v1/namespaces", result, 503); !errors.Is(err, errContract) {
			t.Fatal("problem status disagreed with transport status")
		}
	}
}
