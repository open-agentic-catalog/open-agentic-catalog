package artwork

import (
	"bytes"
	"encoding/json"
	"testing"
)

func FuzzDecodeArtworkInput(f *testing.F) {
	f.Add([]byte(`{"schemaVersion":1}`))
	f.Add([]byte(`{"schemaVersion":1,"icon":{"url":"https://images.example/icon.png","alt":"Owned fixture","rights":{"basis":"owned","attribution":"Fixture author"}}}`))
	f.Add([]byte(`{"schemaVersion":1,"schemaVersion":2}`))
	f.Add([]byte(`{"schemaVersion":1,"screenshots":null}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		input, err := DecodeInput(raw, DefaultLimits())
		if err != nil {
			return
		}
		if err = ValidateInput(input, DefaultLimits()); err != nil {
			t.Fatalf("decoder accepted invalid input: %v", err)
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		again, err := DecodeInput(encoded, DefaultLimits())
		if err != nil {
			t.Fatalf("canonical input rejected: %v", err)
		}
		second, err := json.Marshal(again)
		if err != nil || !bytes.Equal(encoded, second) {
			t.Fatal("canonical input not stable")
		}
	})
}

func FuzzExtractArtworkJSONLiteral(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"` + IRI + `":{"@type":"@json","@value":{"schemaVersion":2}}}`))
	f.Add([]byte(`{"` + IRI + `":[{"@type":"@json","@value":{"schemaVersion":1}}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		descriptor, err := Extract(raw, DefaultLimits())
		if err != nil || descriptor == nil {
			return
		}
		encoded, err := json.Marshal(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeDescriptor(encoded, DefaultLimits()); err != nil {
			t.Fatalf("extracted unsealed descriptor: %v", err)
		}
	})
}
