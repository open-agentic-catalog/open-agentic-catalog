// Contract-delivery tests bind cache responses to the embedded OpenAPI bytes
// without starting a Catalog, database or listener.
package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
)

// TestOpenAPIConditionalDeliveryMatchesContract prevents the documented
// statuses and headers from drifting from exact-byte contract delivery.
func TestOpenAPIConditionalDeliveryMatchesContract(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	const path = "/openapi/control-v1.yaml"
	item := document.Paths.Value(path)
	if item == nil || item.Get == nil {
		t.Fatal("contract download operation is missing")
	}
	operation := item.Get
	foundConditional := false
	for _, parameter := range operation.Parameters {
		if parameter.Value.Name == "If-None-Match" && parameter.Value.In == "header" && !parameter.Value.Required {
			foundConditional = true
		}
	}
	if !foundConditional {
		t.Fatal("contract download must document optional If-None-Match")
	}
	digest := sha256.Sum256(serviceapi.ControlV1)
	etag := `"` + hex.EncodeToString(digest[:]) + `"`
	for _, test := range []struct {
		name, conditional string
		status            int
	}{
		{name: "unconditional", status: http.StatusOK},
		{name: "matching", conditional: etag, status: http.StatusNotModified},
		{name: "outdated", conditional: `"previous-build"`, status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
			if test.conditional != "" {
				request.Header.Set("If-None-Match", test.conditional)
			}
			response := httptest.NewRecorder()
			server := &Server{}
			server.handleOpenAPI(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			contract := operation.Responses.Status(test.status)
			if contract == nil || contract.Value == nil {
				t.Fatalf("status %d is undocumented", test.status)
			}
			for name, expected := range map[string]string{
				"ETag":           etag,
				"Content-Digest": digestHeader(serviceapi.ControlV1),
				"Cache-Control":  "public, max-age=300",
			} {
				if contract.Value.Headers[name] == nil {
					t.Errorf("%s is undocumented on status %d", name, test.status)
				}
				if actual := response.Header().Get(name); actual != expected {
					t.Errorf("%s = %q, want %q", name, actual, expected)
				}
			}
			if test.status == http.StatusNotModified {
				if response.Body.Len() != 0 || len(contract.Value.Content) != 0 {
					t.Fatal("304 must have neither response bytes nor a documented body")
				}
			} else if !bytes.Equal(response.Body.Bytes(), serviceapi.ControlV1) || response.Header().Get("Content-Type") != "application/yaml; charset=utf-8" {
				t.Fatal("200 must serve exact embedded YAML")
			}
		})
	}
}
