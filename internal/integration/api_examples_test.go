//go:build functional

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/open-agentic-catalog/open-agentic-catalog/api"
)

// Compare the complete example with the composed default-off runtime: schema
// validation alone cannot catch omitted operations or obsolete capability states.
func TestCapabilitiesExampleMatchesDefaultRuntime(t *testing.T) {
	runtime := buildRuntime(t)
	defer runtime.Close()
	server := httptest.NewServer(runtime.HTTP.Handler())
	defer server.Close()

	result := request(t, server.URL, http.MethodGet, "/api/v1/capabilities", "admin", "", "", nil)
	requireStatus(t, result, http.StatusOK)
	var actual any
	decode(t, result, &actual)

	document, err := openapi3.NewLoader().LoadFromData(api.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	schema := document.Components.Schemas["Capabilities"]
	if schema == nil || schema.Value == nil || len(schema.Value.Examples) != 1 {
		t.Fatal("Capabilities schema example is absent")
	}
	// Normalize YAML numbers to the same JSON representation as the HTTP body.
	raw, err := json.Marshal(schema.Value.Examples[0])
	if err != nil {
		t.Fatal(err)
	}
	var example any
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(example, actual) {
		t.Fatalf("capabilities example differs from the default-off runtime\nexample: %s\nactual: %s", raw, result.body)
	}
}
