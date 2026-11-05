package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestOptionalCommandBodyUsesStrictDecoderWhenPresent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, media string
		length            int64
		valid             bool
	}{
		{"no body", "", "", 0, true},
		{"empty object", `{}`, "application/json", 2, true},
		{"charset", `{}`, "application/json; charset=utf-8", 2, true},
		{"null", `null`, "application/json", -1, false},
		{"wrong media", `{}`, "text/plain", -1, false},
		{"missing media", `{}`, "", -1, false},
		{"invalid media", `{}`, "application/json; charset", -1, false},
		{"unknown length object", `{}`, "application/json", -1, true},
		{"unknown length empty stream", "", "application/json", -1, false},
		{"unknown field", `{"unexpected":true}`, "application/json", -1, false},
		{"duplicate key", `{"x":1,"x":2}`, "application/json", -1, false},
		{"trailing object", `{} {}`, "application/json", -1, false},
		{"array", `[]`, "application/json", -1, false},
		{"oversized", strings.Repeat(" ", 33) + `{}`, "application/json", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/command", strings.NewReader(tc.body))
			request.ContentLength = tc.length
			request.Header.Set("Content-Type", tc.media)
			if tc.name == "no body" {
				request.Body = nil
			}
			var target struct{}
			err := decodeOptionalEmptyObject(httptest.NewRecorder(), request, 32, &target)
			if tc.valid && err != nil || !tc.valid && catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("decode error=%v valid=%t", err, tc.valid)
			}
		})
	}
}
