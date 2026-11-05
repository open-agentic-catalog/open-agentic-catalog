// Unit tests exercise isolated validation and controlled dependencies.
package httpserver

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

var openAPIPathParameter = regexp.MustCompile(`\{([^}]+)\}`)

func TestSkillPackageRequestSchemaRequiresOneTransientPackageAndImmutableHandoff(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	schema := document.Components.Schemas["RepresentationInput"]
	if schema == nil || schema.Value == nil {
		t.Fatal("RepresentationInput schema is absent")
	}
	valid := map[string]any{
		"role":        "primary",
		"mediaType":   "application/agent-skills+zip",
		"packageData": "UEsDBAoAAAAAAA==",
		"handoff": map[string]any{
			"git": map[string]any{"repository": "https://git.example.com/team/skills.git", "commit": strings.Repeat("a", 40), "directory": "skills/safe-skill"},
			"oci": map[string]any{"reference": "registry.example.com/team/safe-skill@sha256:" + strings.Repeat("b", 64)},
		},
	}
	if err := schema.Value.VisitJSON(valid, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("valid Skill package input does not match OpenAPI: %v", err)
	}
	for _, reference := range []string{
		"localhost/team/safe-skill@sha256:" + strings.Repeat("b", 64),
		"registry:5000/team/safe-skill@sha256:" + strings.Repeat("b", 64),
		"127.0.0.1/team/safe-skill@sha256:" + strings.Repeat("b", 64),
		"[2001:db8::1]/team/safe-skill@sha256:" + strings.Repeat("b", 64),
	} {
		value := map[string]any{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": "UEsDBAoAAAAAAA==", "handoff": map[string]any{"oci": map[string]any{"reference": reference}}}
		if err := schema.Value.VisitJSON(value, openapi3.EnableJSONSchema2020()); err != nil {
			t.Errorf("valid explicit OCI registry %q does not match OpenAPI: %v", reference, err)
		}
	}
	invalid := []map[string]any{
		{"role": "primary", "mediaType": "invalid", "data": map[string]any{}},
		{"role": "primary", "mediaType": " application/json ", "data": map[string]any{}},
		{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": "UEsDBAoAAAAAAA=="},
		{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": "", "handoff": valid["handoff"]},
		{"role": "primary", "mediaType": "application/a2a-agent-card+json", "packageData": "UEsDBAoAAAAAAA==", "handoff": valid["handoff"]},
		{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": "UEsDBAoAAAAAAA==", "content": "duplicate", "handoff": valid["handoff"]},
		{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": "UEsDBAoAAAAAAA==", "handoff": map[string]any{}},
		{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": "UEsDBAoAAAAAAA==", "handoff": map[string]any{"oci": map[string]any{"reference": "registry.example.com/team/safe-skill:latest"}}},
		{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": "UEsDBAoAAAAAAA==", "handoff": map[string]any{"oci": map[string]any{"reference": "team/safe-skill@sha256:" + strings.Repeat("b", 64)}}},
		{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": "UEsDBAoAAAAAAA==", "handoff": map[string]any{"oci": map[string]any{"reference": "registry.example.com:/team/safe-skill@sha256:" + strings.Repeat("b", 64)}}},
		{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": "UEsDBAoAAAAAAA==", "handoff": map[string]any{"git": map[string]any{"repository": "https://git.example.com/team/skills.git?", "commit": strings.Repeat("a", 40), "directory": "skills/safe-skill"}}},
		{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": "UEsDBAoAAAAAAA==", "handoff": valid["handoff"], "unknown": true},
	}
	for index, value := range invalid {
		if err := schema.Value.VisitJSON(value, openapi3.EnableJSONSchema2020()); err == nil {
			t.Fatalf("invalid Skill package request %d matches OpenAPI: %#v", index, value)
		}
	}
}

func TestRouterErrorHandlerPreservesMuxBehavior(t *testing.T) {
	router := http.NewServeMux()
	pathValue := ""
	router.HandleFunc("GET /api/v1/resources/{resourceId}", func(writer http.ResponseWriter, request *http.Request) {
		pathValue = request.PathValue("resourceId")
		writer.WriteHeader(http.StatusNoContent)
	})
	handler := requestIDMiddleware(routerErrorHandler(router))

	registered := httptest.NewRecorder()
	handler.ServeHTTP(registered, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/resources/one", nil))
	if registered.Code != http.StatusNoContent || pathValue != "one" {
		t.Fatalf("registered route = status %d, path value %q", registered.Code, pathValue)
	}

	redirected := httptest.NewRecorder()
	handler.ServeHTTP(redirected, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/not//a-route", nil))
	if redirected.Code != http.StatusTemporaryRedirect || redirected.Header().Get("Location") != "/not/a-route" {
		t.Fatalf("canonical redirect = status %d, headers %#v, body %s", redirected.Code, redirected.Header(), redirected.Body.String())
	}

	// Control and ARD route failures have different closed wire contracts.
	router.HandleFunc("GET /ard/v1/entries/{entryId}", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	for _, surface := range []string{"control", "ard"} {
		for _, missing := range []bool{true, false} {
			name := surface + "/wrong-method"
			path := "/api/v1/resources/one"
			if surface == "ard" {
				path = "/ard/v1/entries/one"
			}
			method, status, code, title, detail, allow := http.MethodPost, 405, "METHOD_NOT_ALLOWED", "Method not allowed", "method is not allowed for this route", "GET, HEAD"
			problemType := "https://catalog.example.com/problems/method-not-allowed"
			if missing {
				name = surface + "/missing-route"
				path += "/missing"
				method, status, code, title, detail, allow = http.MethodGet, 404, "NOT_FOUND", "Not found", "route was not found", ""
				problemType = "https://catalog.example.com/problems/not-found"
			}
			t.Run(name, func(t *testing.T) {
				r := httptest.NewRequestWithContext(t.Context(), method, path, nil)
				r.Header.Set("X-Request-ID", "router-error-1")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				mediaType := "application/problem+json"
				want := map[string]any{"type": problemType, "title": title, "status": float64(status), "detail": detail, "instance": path, "code": code, "requestId": "router-error-1"}
				if surface == "ard" {
					mediaType = "application/json"
					want = map[string]any{"errorCode": code, "message": detail}
				}
				var got map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if w.Code != status || w.Header().Get("Content-Type") != mediaType || w.Header().Get("Allow") != allow || w.Header().Get("X-Request-ID") != "router-error-1" || !reflect.DeepEqual(got, want) || strings.Contains(strings.ToLower(w.Body.String()), "<html") {
					t.Fatalf("router error status=%d headers=%v body=%v", w.Code, w.Header(), got)
				}
			})
		}
	}

	invalidTargetRequest := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	invalidTargetRequest.RequestURI = "*"
	invalidTargetRequest.URL.Path = "*"
	invalidTarget := httptest.NewRecorder()
	handler.ServeHTTP(invalidTarget, invalidTargetRequest)
	if invalidTarget.Code != http.StatusBadRequest {
		t.Fatalf("invalid target status = %d; body = %s", invalidTarget.Code, invalidTarget.Body.String())
	}
	var problem Problem
	if err := json.Unmarshal(invalidTarget.Body.Bytes(), &problem); err != nil || problem.Code != catalog.CodeInvalidArgument {
		t.Fatalf("invalid target problem = %#v, %v", problem, err)
	}
}

func TestEveryLiteralRouteIsDocumentedOrAnExplicitDispatcher(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	documented := make(map[string]struct{})
	for path, item := range document.Paths.Map() {
		for method := range item.Operations() {
			documented[strings.ToUpper(method)+" "+path] = struct{}{}
		}
	}

	dispatchers := map[string]struct{}{
		"POST /api/v1/access/roles/{roleCommand}":                                   {},
		"POST /api/v1/access/groups/{groupCommand}":                                 {},
		"POST /api/v1/access/groups/{groupId}/members/{memberCommand}":              {},
		"POST /api/v1/access/assignments/{accessAssignmentCommand}":                 {},
		"POST /api/v1/memberships/{membershipCommand}":                              {},
		"POST /api/v1/role-assignments/{assignmentCommand}":                         {},
		"POST /api/v1/resources/{resourceCommand}":                                  {},
		"POST /api/v1/publishers/{publisherId}/publication-domains/{domainCommand}": {},
		"POST /api/v1/review-submissions/{submissionCommand}":                       {},
		"POST /api/v1/catalog-listings/{listingCommand}":                            {},
		"POST /api/v1/listing-releases/{releaseCommand}":                            {},
		"POST /api/v1/publication/snapshots/{snapshotCommand}":                      {},
		"POST /api/v1/federation/sources/{sourceCommand}":                           {},
		"POST /api/v1/observations/{observationCommand}":                            {},
		"POST /api/v1/version-revisions/{revisionCommand}":                          {},
		"POST /api/v1/jobs/{jobCommand}":                                            {},
		"POST /api/v1/webhooks/{webhookCommand}":                                    {},
	}

	parsed, err := parser.ParseFile(token.NewFileSet(), "server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	routeCount := 0
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "HandleFunc" {
			return true
		}
		routeCount++
		if len(call.Args) == 0 {
			t.Error("HandleFunc registration has no route pattern")
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			t.Errorf("HandleFunc route %d is not a static string literal", routeCount)
			return true
		}
		pattern, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Errorf("decode HandleFunc route %d: %v", routeCount, err)
			return true
		}
		if _, ok := documented[pattern]; ok {
			return true
		}
		if _, ok := dispatchers[pattern]; ok {
			return true
		}
		t.Errorf("registered route %q is absent from OpenAPI and is not an explicit command dispatcher", pattern)
		return true
	})
	if routeCount == 0 {
		t.Fatal("no HandleFunc route registrations were found")
	}
}

func TestOpenAPIIdempotencyHeadersMatchCommandMiddleware(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"IdempotencyReplayed", "RetryAfter"} {
		if header := document.Components.Headers[name]; header == nil || header.Value == nil {
			t.Fatalf("OpenAPI idempotency response header %q is absent", name)
		}
	}
	problem := document.Components.Responses["Problem"]
	if problem == nil || problem.Value == nil || problem.Value.Headers["Idempotency-Replayed"] == nil || problem.Value.Headers["Retry-After"] == nil {
		t.Fatal("OpenAPI Problem response does not document idempotent replay and retry headers")
	}
	for path, item := range document.Paths.Map() {
		for method, operation := range item.Operations() {
			method := strings.ToUpper(method)
			request, err := http.NewRequestWithContext(context.Background(), method, materializeOpenAPIPath(path), nil)
			if err != nil {
				t.Fatal(err)
			}
			parameters := append(openapi3.Parameters{}, item.Parameters...)
			parameters = append(parameters, operation.Parameters...)
			documented := parameters.GetByInAndName("header", "Idempotency-Key") != nil
			if required := requiresIdempotency(request); documented != required {
				t.Errorf("%s %s documents Idempotency-Key=%v, middleware requires=%v", method, path, documented, required)
			}
			if !documented {
				continue
			}
			for status, response := range operation.Responses.Map() {
				if !strings.HasPrefix(status, "2") {
					continue
				}
				if response == nil || response.Value == nil || response.Value.Headers["Idempotency-Replayed"] == nil {
					t.Errorf("%s %s response %s does not document Idempotency-Replayed", method, path, status)
				}
			}
		}
	}
}

func TestOpenAPIRequiresPreconditionsForRevisionCreation(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/resource-versions/{versionId}/revisions",
		"/api/v1/catalog-listings/{listingId}/revisions",
	} {
		item := document.Paths.Value(path)
		if item == nil || item.Post == nil {
			t.Fatalf("POST %s is not documented", path)
		}
		parameters := append(openapi3.Parameters{}, item.Parameters...)
		parameters = append(parameters, item.Post.Parameters...)
		if parameters.GetByInAndName("header", "If-Match") == nil {
			t.Errorf("POST %s does not require If-Match", path)
		}
	}
}

func TestOpenAPIArtifactDeliveryContract(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	parameter := document.Components.Parameters["ArtifactId"]
	if parameter == nil || parameter.Value == nil || parameter.Value.Schema == nil || parameter.Value.Schema.Value == nil {
		t.Fatal("OpenAPI artifact identifier parameter is incomplete")
	}
	if err := parameter.Value.Schema.Value.VisitJSON(strings.Repeat("a", 64), openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("valid document identifier does not match OpenAPI: %v", err)
	}
	for _, invalid := range []string{"missing", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("g", 64)} {
		if err := parameter.Value.Schema.Value.VisitJSON(invalid, openapi3.EnableJSONSchema2020()); err == nil {
			t.Errorf("invalid document identifier %q matches OpenAPI", invalid)
		}
	}
	for path, requiredHeaders := range map[string][]string{
		"/api/v1/artifacts/{artifactId}/content": {"Content-Digest", "Content-Length", "Content-Disposition", "Cache-Control", "X-Request-ID"},
		"/public/v1/artifacts/{artifactId}":      {"ETag", "Content-Digest", "Content-Length", "Content-Disposition", "Cache-Control", "X-Request-ID"},
	} {
		item := document.Paths.Value(path)
		if item == nil || item.Get == nil {
			t.Fatalf("GET %s is not documented", path)
		}
		response := item.Get.Responses.Value("200")
		if response == nil || response.Value == nil {
			t.Fatalf("GET %s has no 200 response", path)
		}
		for _, name := range requiredHeaders {
			if response.Value.Headers[name] == nil {
				t.Errorf("GET %s does not document %s", path, name)
			}
		}
		media := response.Value.Content.Get("*/*")
		if media == nil || media.Schema == nil || media.Schema.Value == nil || media.Schema.Value.Type == nil || !media.Schema.Value.Type.Is("string") || media.Schema.Value.Format != "binary" {
			t.Errorf("GET %s does not document its dynamic binary response media type", path)
		}
		if path == "/public/v1/artifacts/{artifactId}" {
			notModified := item.Get.Responses.Value("304")
			if notModified == nil || notModified.Value == nil || notModified.Value.Headers["ETag"] == nil || notModified.Value.Headers["Cache-Control"] == nil || notModified.Value.Headers["X-Request-ID"] == nil || notModified.Value.Headers["Last-Modified"] != nil {
				t.Errorf("GET %s has an inaccurate 304 response contract", path)
			}
		}
	}
}

func materializeOpenAPIPath(path string) string {
	return openAPIPathParameter.ReplaceAllStringFunc(path, func(value string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(value, "{"), "}")
		switch name {
		case "domain":
			return "example.com"
		case "artifactId":
			return strings.Repeat("a", 64)
		default:
			return "00000000-0000-7000-8000-000000000999"
		}
	})
}
