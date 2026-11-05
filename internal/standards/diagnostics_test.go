package standards

import (
	"strings"
	"testing"
)

func TestSchemaDiagnosticPointerBound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		segments []string
		want     string
	}{
		{"escaped", []string{"a/b", "~name"}, "/a~1b/~0name"},
		{"exact", []string{strings.Repeat("x", 1023)}, "/" + strings.Repeat("x", 1023)},
		{"over", []string{strings.Repeat("x", 1024)}, ""},
		{"escaping over", []string{strings.Repeat("~", 512)}, ""},
		{"UTF8 bytes", []string{strings.Repeat("é", 512)}, ""},
		{"segments over", []string{strings.Repeat("x", 1022), "y"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonPointer(tc.segments); got != tc.want {
				t.Fatalf("pointer length=%d, want %d", len(got), len(tc.want))
			}
		})
	}
}
