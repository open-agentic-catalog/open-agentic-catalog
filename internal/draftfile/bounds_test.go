package draftfile

import (
	"errors"
	"strings"
	"testing"
)

func TestStructureIsBoundedBeforeTypedDecoding(t *testing.T) {
	for _, raw := range []string{
		`{"representations":[` + strings.Repeat(`{},`, 1024) + `{}]}`,
		strings.Repeat("[", 65) + strings.Repeat("]", 65),
		`{"unknown":{` + strings.Repeat(`"a":0,`, 131072) + `"last":0}}`,
	} {
		if value, err := Decode([]byte(raw)); !errors.Is(err, ErrLimit) || value.Format != "" {
			t.Fatal("structural overflow was not refused before decoding", err)
		}
	}
	for _, raw := range []string{"}", "[}", `{"broken":`} {
		if _, err := Decode([]byte(raw)); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid syntax changed error classification", err)
		}
	}
}
