package jsondoc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeControlFields(t *testing.T) {
	type child struct {
		Name string `json:"name"`
	}
	type control struct {
		Name       string            `json:"name"`
		Child      *child            `json:"child"`
		Items      []child           `json:"items"`
		Labels     map[string]string `json:"labels"`
		Data       json.RawMessage   `json:"data"`
		Extensions map[string]any    `json:"extensions"`
		Bytes      []byte            `json:"bytes"`
	}
	for _, raw := range []string{
		`{"Name":"alias"}`, `{"name":"first","Name":"second"}`,
		`{"child":{"Name":"alias"}}`, `{"items":[{"Name":"alias"}]}`,
		`{"name":null}`, `{"child":null}`, `{"child":{"name":null}}`,
		`{"items":null}`, `{"items":[null]}`, `{"labels":{"key":null}}`,
		`{"bytes":null}`, `null`, `{"name":"a","name":"b"}`, `{} {}`,
		`{"name":"\ud800"}`, `{"unexpected":true}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var target control
			if err := Decode([]byte(raw), &target); err == nil {
				t.Fatal("invalid control accepted")
			}
		})
	}
	// Omission overlays defaults, typed fields remain exact, and arbitrary
	// standards/extension data retains case distinctions, null and numeric values.
	target := control{Name: "default"}
	const data = `{"Name":null,"name":[null,42],"text":"\ud83d\ude42"}`
	raw := `{"child":{"name":"é"},"items":[{"name":"中"}],"labels":{"Name":"A","name":"B"},"data":` + data + `,"extensions":{"Name":null,"name":42},"bytes":"AQI="}`
	if err := Decode([]byte(raw), &target); err != nil {
		t.Fatal(err)
	}
	if target.Name != "default" || target.Child.Name != "é" || target.Items[0].Name != "中" || target.Labels["Name"] != "A" || target.Labels["name"] != "B" || string(target.Data) != data || target.Extensions["Name"] != nil || target.Extensions["name"] != float64(42) || string(target.Bytes) != "\x01\x02" {
		t.Fatalf("control or payload changed: %#v", target)
	}
	if err := Decode([]byte(`{"data":null}`), &target); err != nil || string(target.Data) != "null" {
		t.Fatalf("arbitrary JSON null: %s, %v", target.Data, err)
	}
	if err := Decode([]byte(strings.Repeat("[", 129)+"0"+strings.Repeat("]", 129)), new(any)); err == nil {
		t.Fatal("decode bypassed structural depth limit")
	}
}

func TestDecodeNumbersPreservesExtensionsAndControlRules(t *testing.T) {
	type input struct {
		Name       string         `json:"name"`
		Extensions map[string]any `json:"extensions"`
	}
	const raw = `{"name":"example","extensions":{"integer":9007199254740993,"decimal":0.123456789012345678901,"exponent":1e999,"null":null,"array":[1.20,true,"text"]}}`
	var value input
	if err := DecodeNumbers([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"integer": "9007199254740993", "decimal": "0.123456789012345678901", "exponent": "1e999"} {
		if value.Extensions[key] != json.Number(want) {
			t.Fatalf("%s changed: %#v", key, value.Extensions[key])
		}
	}
	array, ok := value.Extensions["array"].([]any)
	if !ok || len(array) != 3 || value.Extensions["null"] != nil || array[0] != json.Number("1.20") {
		t.Fatal("arbitrary JSON changed")
	}
	for _, bad := range []string{`{"Name":"alias"}`, `{"name":null}`, `{"extensions":null}`, `{"extensions":{"x":1,"x":2}}`, `{"extensions":{"x":"\ud800"}}`, `{"unknown":1}`, `{} {}`} {
		if DecodeNumbers([]byte(bad), new(input)) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
