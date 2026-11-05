// Reindex payload tests protect the closed, versioned identity consumed by
// workers and restore checks without executing a Job or opening a database.
package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDecodeSemanticReindex preserves canonical identities at supported bounds.
func TestDecodeSemanticReindex(t *testing.T) {
	profile := "sha256:" + strings.Repeat("ab", 32)
	for _, input := range []SemanticReindexInput{
		{Profile: profile, Scope: "control", SourceGeneration: "0"},
		{Profile: profile, Scope: "control", SourceGeneration: "9223372036854775807"},
		{Profile: profile, Scope: "public:01900000-0000-7000-8000-000000000001", SourceGeneration: "42"},
	} {
		t.Run(input.Scope+"/"+input.SourceGeneration, func(t *testing.T) {
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			for _, payload := range [][]byte{raw, append(raw, []byte(strings.Repeat(" ", 1024-len(raw)))...)} {
				got, err := DecodeSemanticReindex(payload, 1)
				if err != nil || got != input {
					t.Fatalf("decode %d-byte payload = %+v, %v; want %+v", len(payload), got, err, input)
				}
			}
		})
	}
}

// TestDecodeSemanticReindexRejectsInvalidPayload returns no partially decoded
// identity for malformed, ambiguous, obsolete or noncanonical Job evidence.
func TestDecodeSemanticReindexRejectsInvalidPayload(t *testing.T) {
	valid := SemanticReindexInput{Profile: "sha256:" + strings.Repeat("ab", 32), Scope: "control", SourceGeneration: "42"}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		payload []byte
		version int
	}{
		{"missing version", raw, 0},
		{"future version", raw, 2},
		{"negative version", raw, -1},
		{"oversized", append(append([]byte(nil), raw...), []byte(strings.Repeat(" ", 1025-len(raw)))...), 1},
		{"empty", nil, 1},
		{"malformed", []byte(`{"profile":`), 1},
		{"null", []byte(`null`), 1},
		{"array", []byte(`[]`), 1},
		{"empty object", []byte(`{}`), 1},
		{"trailing value", append(append([]byte(nil), raw...), []byte(` {}`)...), 1},
		{"duplicate field", []byte(strings.Replace(string(raw), `"scope":"control"`, `"scope":"control","scope":"control"`, 1)), 1},
		{"unknown field", []byte(strings.Replace(string(raw), `"scope":"control"`, `"scope":"control","private":"sensitive-value"`, 1)), 1},
		{"wrong field type", []byte(strings.Replace(string(raw), `"sourceGeneration":"42"`, `"sourceGeneration":42`, 1)), 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertRejectedReindex(t, tc.payload, tc.version)
		})
	}
	for _, tc := range []struct {
		field string
		value string
	}{
		{"profile", ""},
		{"profile", strings.TrimPrefix(valid.Profile, "sha256:")},
		{"profile", "sha256:" + strings.Repeat("AB", 32)},
		{"profile", "sha256:" + strings.Repeat("a", 63)},
		{"profile", "sha256:" + strings.Repeat("z", 64)},
		{"scope", ""},
		{"scope", "public:"},
		{"scope", "public:not-a-tenant"},
		{"scope", "private:01900000-0000-7000-8000-000000000001"},
		{"scope", "control "},
		{"sourceGeneration", ""},
		{"sourceGeneration", "-1"},
		{"sourceGeneration", "-0"},
		{"sourceGeneration", "+1"},
		{"sourceGeneration", "01"},
		{"sourceGeneration", " 1"},
		{"sourceGeneration", "1.0"},
		{"sourceGeneration", "9223372036854775808"},
	} {
		t.Run(tc.field+"/"+tc.value, func(t *testing.T) {
			fields := map[string]string{"profile": valid.Profile, "scope": valid.Scope, "sourceGeneration": valid.SourceGeneration}
			fields[tc.field] = tc.value
			payload, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			assertRejectedReindex(t, payload, 1)
		})
	}
}

// assertRejectedReindex checks that failed decoding cannot supply a Job identity
// or expose private payload values through its domain error.
func assertRejectedReindex(t *testing.T, payload []byte, version int) {
	t.Helper()
	got, err := DecodeSemanticReindex(payload, version)
	if err == nil || ErrorCodeOf(err) != CodeInvalidArgument || got != (SemanticReindexInput{}) {
		t.Fatalf("decode = %+v, %v; want empty identity and INVALID_ARGUMENT", got, err)
	}
	if strings.Contains(err.Error(), "sensitive-value") {
		t.Fatal("invalid payload leaked into error")
	}
}
