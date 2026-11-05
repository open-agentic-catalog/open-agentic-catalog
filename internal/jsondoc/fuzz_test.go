package jsondoc

import (
	"bytes"
	"encoding/json"
	"testing"
	"unicode/utf8"
)

func FuzzControlJSON(f *testing.F) {
	for _, seed := range []string{`{"name":"Example"}`, `{"name":"\ud83d\ude42"}`, `{"name":"\ud800"}`, `{"name":null}`, `{"Name":"alias"}`, `{"name":"first","Name":"second"}`, "{\"name\":\"\xff\"}", `{"name":"\u0000"}`, `{"name":"�"}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 4096 {
			return
		}
		var got struct {
			Name string `json:"name"`
		}
		if err := Decode(raw, &got); err != nil {
			return
		}
		// Independent legacy decoder checks accepted values and exact control keys.
		// It is not an oracle for invalid surrogate escapes (covered by properties).
		if !utf8.Valid(raw) || !utf8.ValidString(got.Name) {
			t.Fatal("accepted invalid UTF-8")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			t.Fatalf("accepted non-object: %q", raw)
		}
		for key, value := range fields {
			if key != "name" || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				t.Fatalf("accepted invalid control: %q", raw)
			}
			var want string
			if err := json.Unmarshal(value, &want); err != nil || got.Name != want {
				t.Fatalf("accepted value changed: %q", raw)
			}
		}
	})
}
