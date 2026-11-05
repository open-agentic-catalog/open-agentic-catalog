package api

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestActivityCounterContractPreservesExactUint64(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	schema := document.Components.Schemas["ActivityCounter"].Value
	for _, test := range []struct {
		value any
		valid bool
	}{
		{"0", true}, {"1", true}, {"9007199254740993", true}, {"9999999999999999999", true}, {"18446744073709551614", true}, {"18446744073709551615", true},
		{"18446744073709551616", false}, {"19999999999999999999", false}, {"99999999999999999999", false}, {"00", false}, {"01", false}, {"-1", false}, {"1.0", false}, {"1e3", false}, {"", false}, {float64(1), false}, {nil, false},
	} {
		err := schema.VisitJSON(test.value, openapi3.EnableJSONSchema2020())
		if (err == nil) != test.valid {
			t.Fatalf("counter %v: valid=%v error=%v", test.value, test.valid, err)
		}
	}
}
