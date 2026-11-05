// AIR parser tests distinguish supported naming syntax from Catalog size bounds.
package catalog

import (
	"strings"
	"testing"
)

// TestParseAIRIdentifier checks naming syntax, normalization and bounded inputs.
func TestParseAIRIdentifier(t *testing.T) {
	t.Parallel()
	// Keep every segment valid so the overall byte boundary is independent.
	maximumIdentifier := "urn:air:example.com:" + strings.Repeat(strings.Repeat("n", 64)+":", 7) + strings.Repeat("x", 37)
	maximumPublisher := strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("b", 61)
	if len(maximumIdentifier) != 512 || len(maximumPublisher) != 253 {
		t.Fatal("boundary fixtures have incorrect lengths")
	}
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "namespaced", value: "URN:AIR:Example.COM:finance:reconcile", want: "urn:air:example.com:finance:reconcile"},
		{name: "one segment compatibility", value: "urn:air:example.com:reconcile", want: "urn:air:example.com:reconcile"},
		{name: "alphabet endpoints", value: "urn:air:a0-z9.example:aAzZ09-_.", want: "urn:air:a0-z9.example:aAzZ09-_."},
		{name: "wrong URN scheme", value: "other:air:example.com:agent", wantErr: true},
		{name: "missing", value: "agent:reconcile", wantErr: true},
		{name: "IP authority", value: "urn:air:127.0.0.1:finance:x", wantErr: true},
		{name: "empty segment", value: "urn:air:example.com:finance::x", wantErr: true},
		{name: "identifier byte limit", value: maximumIdentifier, want: maximumIdentifier},
		{name: "identifier over byte limit", value: maximumIdentifier + "x", wantErr: true},
		{name: "invalid UTF-8", value: "urn:air:example.com:" + string([]byte{0xff}), wantErr: true},
		{name: "segment alphabet and case", value: "urn:air:example.com:Team_09:Agent-1.2", want: "urn:air:example.com:Team_09:Agent-1.2"},
		{name: "segment length limit", value: "urn:air:example.com:" + strings.Repeat("n", 64), want: "urn:air:example.com:" + strings.Repeat("n", 64)},
		{name: "segment over length limit", value: "urn:air:example.com:" + strings.Repeat("n", 65), wantErr: true},
		{name: "segment whitespace", value: "urn:air:example.com:agent name", wantErr: true},
		{name: "namespace slash", value: "urn:air:example.com:team/one:agent", wantErr: true},
		{name: "non-ASCII segment", value: "urn:air:example.com:ag\u00e9nt", wantErr: true},
		{name: "dot segment", value: "urn:air:example.com:.", wantErr: true},
		{name: "parent segment", value: "urn:air:example.com:..:agent", wantErr: true},
		{name: "publisher length limit", value: "urn:air:" + maximumPublisher + ":agent", want: "urn:air:" + maximumPublisher + ":agent"},
		{name: "publisher over length limit", value: "urn:air:" + maximumPublisher + "b:agent", wantErr: true},
		{name: "publisher label limit", value: "urn:air:" + strings.Repeat("a", 63) + ".com:agent", want: "urn:air:" + strings.Repeat("a", 63) + ".com:agent"},
		{name: "publisher label over limit", value: "urn:air:" + strings.Repeat("a", 64) + ".com:agent", wantErr: true},
		{name: "publisher without dot", value: "urn:air:example:agent", wantErr: true},
		{name: "empty publisher label", value: "urn:air:example..com:agent", wantErr: true},
		{name: "publisher label leading hyphen", value: "urn:air:-example.com:agent", wantErr: true},
		{name: "publisher label trailing hyphen", value: "urn:air:example-.com:agent", wantErr: true},
		{name: "invalid publisher character", value: "urn:air:exam_ple.com:agent", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAIRIdentifier(tt.value)
			if tt.wantErr {
				if ErrorCodeOf(err) != CodeInvalidArgument || got.Publisher != "" || len(got.Namespace) != 0 || got.Name != "" {
					t.Fatalf("invalid identifier returned %#v, %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tt.want {
				t.Fatalf("got %q want %q", got.String(), tt.want)
			}
		})
	}
}

func FuzzParseAIRIdentifier(f *testing.F) {
	f.Add("urn:air:example.com:team:agent")
	f.Add("urn:air:x")
	f.Fuzz(func(t *testing.T, value string) {
		parsed, err := ParseAIRIdentifier(value)
		if err == nil {
			if _, err := ParseAIRIdentifier(parsed.String()); err != nil {
				t.Fatalf("normalized value no longer parses: %v", err)
			}
		}
	})
}
