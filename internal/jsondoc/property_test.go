package jsondoc

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"pgregory.net/rapid"
)

// propertyValue builds valid, bounded JSON independently of the validator.
// Four levels of at most three children keep shrinking small; exact production
// depth limits remain covered by TestValidateRejectsExcessiveDepth.
func propertyValue(t *rapid.T, depth int) any {
	kinds := 4
	if depth > 0 {
		kinds = 6
	}
	switch rapid.IntRange(0, kinds-1).Draw(t, "kind") {
	case 0:
		return nil
	case 1:
		return rapid.Bool().Draw(t, "boolean")
	case 2:
		return rapid.IntRange(-100000, 100000).Draw(t, "number")
	case 3:
		return propertyText(t)
	case 4:
		values := make([]any, rapid.IntRange(0, 3).Draw(t, "array length"))
		for index := range values {
			values[index] = propertyValue(t, depth-1)
		}
		return values
	default:
		values := make(map[string]any)
		for index := range rapid.IntRange(0, 3).Draw(t, "object length") {
			// The index guarantees distinct decoded names without filtering.
			values[fmt.Sprintf("k%d%s", index, propertyText(t))] = propertyValue(t, depth-1)
		}
		return values
	}
}

func propertyText(t *rapid.T) string {
	return string(rapid.SliceOfN(rapid.SampledFrom([]rune{'a', 'b', '"', '\\', '\n', '\t', 'é', '中', '🙂'}), 0, 8).Draw(t, "text"))
}

func propertyJSON(t *rapid.T, value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("generator produced non-JSON value: %v", err)
	}
	return raw
}

func TestPropertyValidJSONTrees(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		raw := propertyJSON(t, propertyValue(t, 4))
		if err := Validate(raw); err != nil {
			t.Fatalf("constructed valid JSON rejected: %s: %v", raw, err)
		}
	})
}

func TestPropertyDuplicateJSONNames(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		key := propertyJSON(t, "k"+propertyText(t))
		// Escape the leading ASCII k, leaving an independently known equal name.
		escaped := `"\u006b` + string(key[2:])
		first, second := propertyJSON(t, propertyValue(t, 3)), propertyJSON(t, propertyValue(t, 3))
		duplicate := []byte(fmt.Sprintf("{%s:%s,%s:%s}", key, first, escaped, second))
		valid := []byte(fmt.Sprintf("[{%s:%s},{%s:%s}]", key, first, escaped, second))
		for range rapid.IntRange(0, 5).Draw(t, "wrappers") {
			if rapid.Bool().Draw(t, "array wrapper") {
				duplicate = propertyJSON(t, []any{json.RawMessage(duplicate), propertyValue(t, 2)})
				valid = propertyJSON(t, []any{json.RawMessage(valid), propertyValue(t, 2)})
			} else {
				name := "child" + propertyText(t)
				duplicate = propertyJSON(t, map[string]any{name: json.RawMessage(duplicate)})
				valid = propertyJSON(t, map[string]any{name: json.RawMessage(valid)})
			}
		}
		if err := Validate(valid); err != nil {
			t.Fatalf("same name in distinct sibling objects rejected: %s: %v", valid, err)
		}
		if err := Validate(duplicate); !errors.Is(err, ErrDuplicateMember) {
			t.Fatalf("nested escaped duplicate: %s: got %v, want ErrDuplicateMember", duplicate, err)
		}
	})
}

func TestPropertyTrailingJSONValues(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		first, second := propertyJSON(t, propertyValue(t, 4)), propertyJSON(t, propertyValue(t, 3))
		whitespace := rapid.SampledFrom([]string{" ", "\t", "\n\r ", "\n"}).Draw(t, "separator")
		if err := Validate(append(append([]byte(nil), first...), whitespace...)); err != nil {
			t.Fatalf("valid value plus whitespace rejected: %s: %v", first, err)
		}
		joined := append(append(append([]byte(nil), first...), whitespace...), second...)
		if err := Validate(joined); !errors.Is(err, ErrTrailingValue) {
			t.Fatalf("second value accepted or misclassified: %s: %v", joined, err)
		}
	})
}

// Work on original JSON bytes: marshaling an invalid Go string first would
// repair it and prevent the property from reaching the admission boundary.
func TestPropertyLosslessJSONText(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		prefix, suffix := propertyJSON(t, propertyText(t)), propertyJSON(t, propertyText(t))
		wrap := func(fragment string) []byte {
			text := string(prefix[:len(prefix)-1]) + fragment + string(suffix[1:])
			if rapid.Bool().Draw(t, "member name") {
				return []byte("{" + text + ":true}")
			}
			return []byte("{\"text\":" + text + "}")
		}
		valid := rapid.SampledFrom([]string{"é", "🙂", "�", `\ufffd`, `\ud83d\ude42`, `\u0000`}).Draw(t, "valid text")
		if err := Validate(wrap(valid)); err != nil {
			t.Fatalf("valid text rejected: %v", err)
		}
		invalid := rapid.SampledFrom([]string{"\xff", "\xc0\xaf", "\xed\xa0\x80", "\xf4\x90\x80\x80", `\ud800`, `\udfff`, `\ud800x`, `\udc00\ud800`}).Draw(t, "invalid text")
		raw := wrap(invalid)
		if err := Validate(raw); err == nil {
			t.Fatalf("lossy text admitted: %q", raw)
		}
	})
}

func TestPropertyExactControlEnvelope(t *testing.T) {
	type child struct {
		Value string `json:"value"`
	}
	type control struct {
		Name    string          `json:"name"`
		Child   child           `json:"child"`
		Enabled bool            `json:"enabled"`
		Payload json.RawMessage `json:"payload"`
	}
	rapid.Check(t, func(t *rapid.T) {
		name, value := propertyText(t), propertyText(t)
		payload := map[string]any{"Case": nil, "case": propertyValue(t, 2)}
		raw := propertyJSON(t, map[string]any{"name": name, "child": map[string]any{"value": value}, "payload": payload})
		got := control{Enabled: true}
		if err := Decode(raw, &got); err != nil || got.Name != name || got.Child.Value != value || !got.Enabled {
			t.Fatalf("valid envelope/defaults: %+v, %v", got, err)
		}
		var decoded any
		// Compare with an independent encoding/json decode, including legacy number semantics.
		var want any
		if err := json.Unmarshal(got.Payload, &decoded); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(propertyJSON(t, payload), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, want) {
			t.Fatal("arbitrary payload changed")
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		switch rapid.IntRange(0, 5).Draw(t, "invalid control") {
		case 0:
			object["Name"] = object["name"]
			delete(object, "name")
		case 1:
			object["Name"] = propertyJSON(t, name+"other")
		case 2:
			object["name"] = json.RawMessage("null")
		case 3:
			object["child"] = propertyJSON(t, map[string]any{"Value": value})
		case 4:
			object["child"] = propertyJSON(t, map[string]any{"value": nil})
		case 5:
			object["enabled"] = json.RawMessage("null")
		}
		// Struct decoding can partially populate its target on error; no assertion
		// of transactional decoding is made. Callers discard rejected targets.
		if err := Decode(propertyJSON(t, object), &control{}); err == nil {
			t.Fatal("ambiguous or null control admitted")
		}
	})
}
