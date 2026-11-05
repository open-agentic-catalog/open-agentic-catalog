// Qualifier tests keep malformed input and numeric checks service-free.
package main

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// TestRunInvalidInput rejects disabled, unknown and trailing fixture configuration.
func TestRunInvalidInput(t *testing.T) {
	for _, raw := range []string{"", "{}", "null", `{"enabled":true,"unknown":1}`, `{} {}`} {
		if err := run(strings.NewReader(raw), &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid fixture %q", raw)
		}
	}
}

// TestSameVector covers finite normalization as well as component agreement.
func TestSameVector(t *testing.T) {
	if !sameVector([]float32{1, 0}, []float32{1, 0}) {
		t.Fatal("equal normalized vectors rejected")
	}
	for _, pair := range [][2][]float32{
		{nil, nil}, {{1}, {1, 0}}, {{0}, {0}}, {{2}, {2}},
		{{1, 0}, {0, 1}}, {{float32(math.NaN())}, {1}}, {{1}, {float32(math.Inf(1))}},
	} {
		if sameVector(pair[0], pair[1]) {
			t.Fatalf("accepted invalid pair: %v", pair)
		}
	}
}
