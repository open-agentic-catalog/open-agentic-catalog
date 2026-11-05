//go:build functional

// Functional tests exercise local HTTP or composed service workflows.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
	"net/http/httptest"
)

// Response assertions share one immutable parsed contract. Every body is still validated.
var controlResponseDocument = sync.OnceValues(func() (*openapi3.T, error) {
	return openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
})

func requireControlSchema(t *testing.T, name string, body []byte) {
	t.Helper()
	document, err := controlResponseDocument()
	if err != nil {
		t.Fatal(err)
	}
	schema := document.Components.Schemas[name]
	if schema == nil || schema.Value == nil {
		t.Fatalf("OpenAPI schema %q is absent", name)
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode %s response: %v", name, err)
	}
	if err := schema.Value.VisitJSON(value, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("response does not match OpenAPI %s: %v\n%s", name, err, body)
	}
}

func TestControlResponseContractCachePreservesStrictValidation(t *testing.T) {
	t.Parallel()
	document, err := controlResponseDocument()
	if err != nil {
		t.Fatal(err)
	}
	second, err := controlResponseDocument()
	if err != nil || second != document {
		t.Fatal("response contract was reparsed", err)
	}
	schema := document.Components.Schemas["OverviewBoundedCount"].Value
	if err = schema.VisitJSON(map[string]any{"value": float64(1), "limited": false}, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatal(err)
	}
	for _, value := range []map[string]any{
		{"value": float64(-1), "limited": false},
		{"value": float64(10001), "limited": true},
		{"value": float64(1), "limited": false, "unexpected": true},
	} {
		if err = schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err == nil {
			t.Fatal("invalid response passed cached schema", value)
		}
	}
}

func requireControlProblem(t *testing.T, result httpResult, status int, code catalog.ErrorCode) Problem {
	t.Helper()
	requireStatus(t, result, status)
	if result.header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("problem Content-Type = %q; body = %s", result.header.Get("Content-Type"), result.body)
	}
	if result.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("problem Cache-Control = %q", result.header.Get("Cache-Control"))
	}
	requireControlSchema(t, "Problem", result.body)
	var problem Problem
	decodeResult(t, result, &problem)
	if problem.Status != status || problem.Code != code || problem.RequestID == "" || problem.RequestID != result.header.Get("X-Request-ID") {
		t.Fatalf("problem = %#v; headers = %#v", problem, result.header)
	}
	return problem
}

func TestEveryDocumentedOperationIsRouted(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	routeDefinitions := &Server{capabilities: CapabilitySnapshot{ServiceActivity: CapabilityEnabled}, personal: &personal.Service{}, mcp: &mcpHTTP{}, webhooks: &webhook.Service{}, feedback: &feedback.Service{}, ratings: &ratings.Service{}}
	router := http.NewServeMux()
	routeDefinitions.routes(router)
	server := newContractHTTPServer(t)
	defer server.Close()
	for path, item := range document.Paths.Map() {
		for method := range item.Operations() {
			path := materializeOpenAPIPath(path)
			method := strings.ToUpper(method)
			t.Run(method+" "+path, func(t *testing.T) {
				var body any
				if method == http.MethodPost || method == http.MethodPatch {
					body = map[string]any{}
				}
				key := ""
				request, err := http.NewRequestWithContext(context.Background(), method, path, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, pattern := router.Handler(request); pattern == "" {
					t.Fatalf("documented operation has no matching route pattern")
				}
				if requiresIdempotency(request) {
					key = "route-contract"
				}
				result := requestJSON(t, server.URL, method, path, "admin", key, `"rev-1"`, body)
				if path == "/mcp" && (method == http.MethodGet || method == http.MethodDelete) {
					if result.status != http.StatusMethodNotAllowed || result.header.Get("Allow") != "POST, OPTIONS" {
						t.Fatalf("documented MCP method refusal changed: status=%d headers=%v", result.status, result.header)
					}
					requireControlSchema(t, "MCPReply", result.body)
					return
				}
				if result.status == http.StatusMethodNotAllowed || result.status == http.StatusNotImplemented || result.status == http.StatusPermanentRedirect || result.status == http.StatusTemporaryRedirect || result.status == http.StatusMovedPermanently {
					t.Fatalf("documented operation is not directly routed: status=%d body=%s", result.status, result.body)
				}
				if result.status >= http.StatusBadRequest && strings.HasPrefix(result.header.Get("Content-Type"), "text/plain") {
					t.Fatalf("documented operation returned a plain-text error: %s", result.body)
				}
			})
		}
	}
}

func TestBodyBearingCommandsRejectObsoleteMethodsAndInvalidActions(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	const identifier = "00000000-0000-7000-8000-000000000001"
	for base, suffix := range map[string]string{
		"/api/v1/role-assignments/":   ":revoke",
		"/api/v1/resources/":          ":delete",
		"/api/v1/federation/sources/": ":remove",
	} {
		t.Run(base, func(t *testing.T) {
			// A dispatcher exists for POST, but must not serve old DELETE commands.
			old := requestJSON(t, server.URL, http.MethodDelete, base+identifier, "admin", "old-command", `"rev-1"`, map[string]any{"reason": "Must not run"})
			requireControlProblem(t, old, http.StatusMethodNotAllowed, catalog.CodeMethodNotAllowed)
			for _, segment := range []string{identifier, identifier + ":unknown", suffix} {
				result := requestJSON(t, server.URL, http.MethodPost, base+segment, "admin", "bad-command", `"rev-1"`, map[string]any{"reason": "Must not run"})
				requireControlProblem(t, result, http.StatusNotFound, catalog.CodeNotFound)
			}
			invalidID := requestJSON(t, server.URL, http.MethodPost, base+"invalid"+suffix, "admin", "invalid-id", `"rev-1"`, map[string]any{"reason": "Must not run"})
			requireControlProblem(t, invalidID, http.StatusBadRequest, catalog.CodeInvalidArgument)
			noKey := requestJSON(t, server.URL, http.MethodPost, base+identifier+suffix, "admin", "", `"rev-1"`, map[string]any{"reason": "Missing key"})
			requireControlProblem(t, noKey, http.StatusBadRequest, catalog.CodeInvalidArgument)
		})
	}
}

func TestDisabledWebhookOperationsAreNotRouted(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	router := http.NewServeMux()
	(&Server{}).routes(router)
	found := 0
	for path, item := range document.Paths.Map() {
		if !strings.HasPrefix(path, "/api/v1/webhooks") {
			continue
		}
		for method := range item.Operations() {
			found++
			request, err := http.NewRequestWithContext(context.Background(), strings.ToUpper(method), materializeOpenAPIPath(path), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, pattern := router.Handler(request); pattern != "" {
				t.Errorf("disabled webhook operation %s %s matched %q", method, path, pattern)
			}
		}
	}
	if found == 0 {
		t.Fatal("OpenAPI contains no webhook operations")
	}
}

func TestEveryDocumentedOperationRejectsUndeclaredQueryParameters(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	server := newContractHTTPServer(t)
	defer server.Close()
	sequence := 0
	for path, item := range document.Paths.Map() {
		for method := range item.Operations() {
			sequence++
			method := strings.ToUpper(method)
			target := materializeOpenAPIPath(path) + "?undeclared=true"
			var body any
			if method == http.MethodPost || method == http.MethodPatch || method == http.MethodDelete {
				body = map[string]any{}
			}
			key := ""
			request, err := http.NewRequestWithContext(context.Background(), method, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if requiresIdempotency(request) {
				key = "strict-query-" + strconv.Itoa(sequence)
			}
			result := requestJSON(t, server.URL, method, target, "admin", key, `"rev-1"`, body)
			if result.status != http.StatusBadRequest {
				t.Errorf("%s %s accepted an undeclared query parameter: status=%d body=%s", method, path, result.status, result.body)
			}
		}
	}
}

func TestRepeatedControlQueryParametersAreRejected(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	for _, target := range []string{
		"/api/v1/memberships?limit=1&limit=2",
		"/api/v1/resources?kind=agent&kind=skill",
		"/api/v1/catalog/resources?cursor=one&cursor=two",
		"/api/v1/publication/snapshots?host=one.example&host=two.example",
		"/api/v1/observation-groups?identifier=one&identifier=two&versionPresent=false",
	} {
		result := requestJSON(t, server.URL, http.MethodGet, target, "admin", "", "", nil)
		requireControlProblem(t, result, http.StatusBadRequest, catalog.CodeInvalidArgument)
	}
}

func TestMalformedControlQueryParametersAreRejected(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	result := requestJSON(t, server.URL, http.MethodGet, "/api/v1/memberships?limit=1;cursor=invalid", "admin", "", "", nil)
	requireControlProblem(t, result, http.StatusBadRequest, catalog.CodeInvalidArgument)
}

func TestRouterGeneratedErrorsFollowSurfaceContracts(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()

	unknownControl := requestJSON(t, server.URL, http.MethodGet, "/api/v1/not-a-route", "admin", "", "", nil)
	problem := requireControlProblem(t, unknownControl, http.StatusNotFound, catalog.CodeNotFound)
	if problem.Instance != "/api/v1/not-a-route" {
		t.Fatalf("unknown-route problem instance = %q", problem.Instance)
	}

	unauthenticatedUnknown := requestJSON(t, server.URL, http.MethodGet, "/api/v1/not-a-route", "", "", "", nil)
	requireControlProblem(t, unauthenticatedUnknown, http.StatusUnauthorized, catalog.CodeUnauthenticated)

	unsupportedMethod := requestJSON(t, server.URL, http.MethodPut, "/api/v1/resources", "admin", "", "", nil)
	requireControlProblem(t, unsupportedMethod, http.StatusMethodNotAllowed, catalog.CodeMethodNotAllowed)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		if !strings.Contains(unsupportedMethod.header.Get("Allow"), method) {
			t.Errorf("method-not-allowed Allow = %q; want %s", unsupportedMethod.header.Get("Allow"), method)
		}
	}

	unknownService := requestJSON(t, server.URL, http.MethodGet, "/not-a-route", "", "", "", nil)
	requireControlProblem(t, unknownService, http.StatusNotFound, catalog.CodeNotFound)
	unsupportedServiceMethod := requestJSON(t, server.URL, http.MethodPost, "/livez", "", "", "", nil)
	requireControlProblem(t, unsupportedServiceMethod, http.StatusMethodNotAllowed, catalog.CodeMethodNotAllowed)
	if !strings.Contains(unsupportedServiceMethod.header.Get("Allow"), http.MethodGet) {
		t.Fatalf("service method-not-allowed Allow = %q", unsupportedServiceMethod.header.Get("Allow"))
	}

	for _, testCase := range []struct {
		name   string
		method string
		path   string
		status int
		code   catalog.ErrorCode
		allow  string
	}{
		{name: "unknown discovery route", method: http.MethodGet, path: "/ard/v1/not-a-route", status: http.StatusNotFound, code: catalog.CodeNotFound},
		{name: "unsupported discovery method", method: http.MethodGet, path: "/ard/v1/search", status: http.StatusMethodNotAllowed, code: catalog.CodeMethodNotAllowed, allow: http.MethodPost},
		{name: "unsupported well-known method", method: http.MethodPost, path: "/.well-known/ard.json", status: http.StatusMethodNotAllowed, code: catalog.CodeMethodNotAllowed, allow: http.MethodGet},
		{name: "unsupported public document method", method: http.MethodPost, path: "/public/v1/artifacts/missing", status: http.StatusMethodNotAllowed, code: catalog.CodeMethodNotAllowed, allow: http.MethodGet},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := requestJSON(t, server.URL, testCase.method, testCase.path, "", "", "", nil)
			requireStatus(t, result, testCase.status)
			if result.header.Get("Content-Type") != "application/json" || result.header.Get("Cache-Control") != "no-store" || result.header.Get("X-Request-ID") == "" {
				t.Fatalf("ARD router error headers = %#v; body = %s", result.header, result.body)
			}
			var problem ardProblem
			decodeResult(t, result, &problem)
			if problem.ErrorCode != testCase.code || problem.Message == "" {
				t.Fatalf("ARD router problem = %#v", problem)
			}
			if testCase.allow != "" && !strings.Contains(result.header.Get("Allow"), testCase.allow) {
				t.Fatalf("ARD method-not-allowed Allow = %q; want %s", result.header.Get("Allow"), testCase.allow)
			}
		})
	}
}

func TestUndocumentedCompatibilityAliasesAreNotRouted(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()

	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/catalog"},
		{method: http.MethodGet, path: "/api/v1/publication-snapshots"},
		{method: http.MethodPost, path: "/api/v1/publication-snapshots"},
		{method: http.MethodGet, path: "/api/v1/publication-snapshots/00000000-0000-7000-8000-000000000999"},
		{method: http.MethodPost, path: "/api/v1/federation-sources:inspect"},
		{method: http.MethodGet, path: "/api/v1/federation-sources"},
		{method: http.MethodPost, path: "/api/v1/federation-sources"},
		{method: http.MethodGet, path: "/api/v1/federation/runs/00000000-0000-7000-8000-000000000999"},
		{method: http.MethodPost, path: "/api/v1/federation-sources:live-search"},
		{method: http.MethodPost, path: "/api/v1/publishers/00000000-0000-7000-8000-000000000999/domains/catalog.example.com:verify"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			var body any
			key := ""
			if test.method == http.MethodPost {
				body = map[string]any{}
				key = "removed-alias"
			}
			result := requestJSON(t, server.URL, test.method, test.path, "admin", key, "", body)
			if result.status != http.StatusNotFound {
				t.Fatalf("undocumented alias returned %d, want 404: %s", result.status, result.body)
			}
		})
	}
}

func TestEveryUUIDPathParameterIsRejectedBeforeDomainHandling(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	server := newWebhookHTTPServer(t)
	defer server.Close()

	checked := 0
	for path, item := range document.Paths.Map() {
		for method, operation := range item.Operations() {
			parameters := append(openapi3.Parameters{}, item.Parameters...)
			parameters = append(parameters, operation.Parameters...)
			for _, parameter := range parameters {
				if parameter == nil || parameter.Value == nil || parameter.Value.In != "path" || parameter.Value.Schema == nil || parameter.Value.Schema.Value == nil || parameter.Value.Schema.Value.Format != "uuid" {
					continue
				}
				requestPath := strings.Replace(path, "{"+parameter.Value.Name+"}", "not-a-uuid", 1)
				requestPath = materializeOpenAPIPath(requestPath)
				result := requestJSON(t, server.URL, strings.ToUpper(method), requestPath, "admin", "invalid-uuid-path-"+strconv.Itoa(checked), `"rev-1"`, map[string]any{})
				problem := requireControlProblem(t, result, http.StatusBadRequest, catalog.CodeInvalidArgument)
				if !strings.Contains(problem.Detail, parameter.Value.Name) {
					t.Errorf("%s %s problem does not identify %s: %#v", method, path, parameter.Value.Name, problem)
				}
				checked++
			}
		}
	}
	if checked < 40 {
		t.Fatalf("only %d UUID path operation parameters were exercised", checked)
	}
}

// The complete route inventory enables optional surfaces. Separate tests prove
// that default-off webhook and MCP paths remain unavailable.
func newContractHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newConfiguredHTTPServer(t, func(d *Dependencies) {
		d.Capabilities.ServiceActivity = CapabilityEnabled
		d.MCP = config.Defaults().MCP
		d.MCP.Enabled = true
		d.PublicBaseURL = "https://catalog.example.com"
		d.AuthIssuer = "https://issuer.example.com"
	})
}
