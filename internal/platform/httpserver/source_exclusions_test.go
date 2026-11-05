package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

func TestSourceExclusionHTTPShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, field string
		valid       bool
	}{
		{"absent", "", true}, {"empty", `,"excludedIdentifiers":[]`, true},
		{"identifier", `,"excludedIdentifiers":["urn:air:remote.example:team:agent"]`, true},
		{"null", `,"excludedIdentifiers":null`, false}, {"wrong type", `,"excludedIdentifiers":"agent"`, false},
		{"null item", `,"excludedIdentifiers":[null]`, false},
		{"wrong case", `,"ExcludedIdentifiers":[]`, false}, {"duplicate field", `,"excludedIdentifiers":[],"excludedIdentifiers":[]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input struct {
				Scope federation.Scope `json:"scope"`
			}
			r := httptest.NewRequestWithContext(t.Context(), "POST", "/", strings.NewReader(`{"scope":{"kinds":["agent"]`+tc.field+`}}`))
			r.Header.Set("Content-Type", "application/json")
			err := decodeJSON(httptest.NewRecorder(), r, 1<<20, &input)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t error=%v", tc.valid, err)
			}
		})
	}
}
