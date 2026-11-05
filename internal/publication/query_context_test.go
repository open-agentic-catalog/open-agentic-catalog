package publication

import (
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

func TestSearchQueryDecoderPreservesStrictControlAdmission(t *testing.T) {
	for _, raw := range []string{
		`{"query":{"text":"agent","@context":null}}`,
		`{"query":{"text":"agent","unknown":1}}`,
		`{"query":{"Text":"agent"}}`,
		`{"query":{"text":"agent","text":"other"}}`,
		`{"query":{"text":"agent","@context":{},"@context":{}}}`,
		`{"query":{"text":null}}`, `{"query":{"text":"agent","filter":null}}`,
		`{"query":{"text":"agent"}} {}`, `{"query":null}`, `{"query":[]}`,
		`{"query":{"text":"agent","@context":{"kind":"x","kind":"y"}}}`,
	} {
		var request SearchRequest
		if err := jsondoc.Decode([]byte(raw), &request); err == nil {
			t.Errorf("invalid query accepted: %s", raw)
		}
	}
	for _, raw := range []string{`{"query":{"text":"agent"}}`, `{"query":{"text":"agent","@context":{}}}`, `{"query":{"text":"agent","@context":[]}}`, `{"query":{"text":"agent","@context":"https://agenticresourcediscovery.org/context/v1"}}`} {
		var request SearchRequest
		if err := jsondoc.Decode([]byte(raw), &request); err != nil {
			t.Errorf("valid query rejected: %s: %v", raw, err)
		}
	}
}

func TestStructuredQueryValidatesContextWithoutFilterTerms(t *testing.T) {
	for _, value := range []any{true, 17, "relative/context", make(chan int)} {
		query := SearchQuery{Text: "agent", Context: value}
		if err := normalizeStructuredQuery(&query); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("context %v: %v", value, err)
		}
	}
	// Shape validation does not fetch or interpret a remote context when there
	// are no terms to resolve. Existing term-resolution policy stays separate.
	query := SearchQuery{Text: "agent", Context: "https://unavailable.invalid/context"}
	if err := normalizeStructuredQuery(&query); err != nil {
		t.Fatal(err)
	}
}
