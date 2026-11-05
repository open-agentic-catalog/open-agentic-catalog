package database

import (
	"encoding/json"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestListingExtensionReadPreservesNumbers(t *testing.T) {
	var value map[string]any
	if err := unmarshalListingExtensions([]byte(`{"integer":9007199254740993,"decimal":0.123456789012345678901,"exponent":1e999,"null":null,"ordinary":true}`), &value); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"integer": "9007199254740993", "decimal": "0.123456789012345678901", "exponent": "1e999"} {
		if value[key] != json.Number(want) {
			t.Fatalf("%s changed: %#v", key, value[key])
		}
	}
	for _, raw := range [][]byte{nil, []byte("null")} {
		if err := unmarshalListingExtensions(raw, &value); err != nil {
			t.Fatal(err)
		}
	}
	if catalog.ErrorCodeOf(unmarshalListingExtensions([]byte(`{`), &value)) != catalog.CodeInternal {
		t.Fatal("malformed retained JSON not classified")
	}
	if catalog.ErrorCodeOf(unmarshalListingExtensions([]byte(`{} {}`), &value)) != catalog.CodeInternal {
		t.Fatal("trailing retained JSON accepted")
	}
}
