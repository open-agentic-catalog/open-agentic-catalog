package jsondoc

import (
	"errors"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name     string
		document string
		valid    bool
		wantErr  error
	}{
		{name: "object", document: `{"a":[1,{"b":true}],"c":null}`, valid: true},
		{name: "scalar", document: `"value"`, valid: true},
		{name: "large number", document: `{"a":1e10000}`, valid: true},
		{name: "sibling names", document: `[{"a":1},{"a":2}]`, valid: true},
		{name: "duplicate top level", document: `{"a":1,"a":2}`, wantErr: ErrDuplicateMember},
		{name: "duplicate nested", document: `{"a":{"b":1,"b":2}}`, wantErr: ErrDuplicateMember},
		{name: "escaped duplicate", document: `{"a":1,"\u0061":2}`, wantErr: ErrDuplicateMember},
		{name: "trailing", document: `{} []`, wantErr: ErrTrailingValue},
		{name: "invalid trailing", document: `{} ]`, wantErr: ErrTrailingValue},
		{name: "empty", document: ``},
		{name: "unclosed object", document: `{"a":1`},
		{name: "unclosed array", document: `[1`},
		{name: "malformed", document: `{"a":`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Validate([]byte(test.document))
			if test.valid && err != nil {
				t.Fatalf("valid JSON rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid JSON accepted")
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error=%v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateRejectsExcessiveDepth(t *testing.T) {
	for _, container := range []struct{ open, close string }{{"[", "]"}, {`{"a":`, "}"}} {
		atLimit := strings.Repeat(container.open, maxDepth) + "0" + strings.Repeat(container.close, maxDepth)
		if err := Validate([]byte(atLimit)); err != nil {
			t.Fatalf("valid document at nesting limit rejected: %v", err)
		}
		overLimit := container.open + atLimit + container.close
		if err := Validate([]byte(overLimit)); err == nil || !strings.Contains(err.Error(), "nesting depth") {
			t.Fatalf("document exceeding nesting limit: %v", err)
		}
	}
}

// TestValidateRejectsContainerBoundaries checks incomplete and mismatched closes
// through the public entry point, retaining the standard decoder's state machine.
func TestValidateRejectsContainerBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ document, want string }{
		{`{`, "read JSON object member:"},
		{`{]`, "JSON object is not closed"},
		{`[`, "read JSON value:"},
		{`[}`, "JSON array is not closed"},
		{`{"child":[}`, "JSON array is not closed"},
		{`[{]`, "JSON object is not closed"},
	} {
		t.Run(tc.document, func(t *testing.T) {
			if err := Validate([]byte(tc.document)); err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateUnicode(t *testing.T) {
	for _, raw := range []string{`{"text":"\ud800"}`, `{"\udfff":1}`, `{"text":"\ud800\u0041"}`, "{\"text\":\"" + string([]byte{0xff}) + "\"}"} {
		if err := Validate([]byte(raw)); err == nil {
			t.Errorf("invalid Unicode accepted: %q", raw)
		}
	}
	for _, raw := range []string{`{"text":"\ud83d\ude42"}`, `{"text":"\ufffd"}`, `{"text":"é中🙂"}`, `{"text":"\\ud800"}`} {
		if err := Validate([]byte(raw)); err != nil {
			t.Errorf("valid Unicode rejected: %q: %v", raw, err)
		}
	}
}
