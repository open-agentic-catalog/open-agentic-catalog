package standards

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeARDEntryNumericContextVersion(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"1.1", "1.10", "11e-1"} {
		t.Run(version, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"identifier":"urn:air:example.com:demo:agent","displayName":"Demo","type":"application/json","data":{"sequence":9007199254740993,"@version":1.10,"@context":{"@version":1.10}},"@context":{"@version":%s,"art":{"@id":"https://example.com/art","@type":"@json"}},"art":{"sequence":9007199254740993,"@version":1.10,"@context":{"@version":1.10},"screenshots":["first","second"]}}`, version))
			before := bytes.Clone(raw)
			got, err := NormalizeARDEntry(raw, func(string) ([]byte, error) {
				t.Fatal("inline context attempted a network fetch")
				return nil, errors.New("unexpected fetch")
			})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, before) {
				t.Fatalf("input changed: %s", raw)
			}
			var document map[string]json.RawMessage
			if err := json.Unmarshal(got, &document); err != nil {
				t.Fatal(err)
			}
			var literal struct {
				Type  string          `json:"@type"`
				Value json.RawMessage `json:"@value"`
			}
			if err := json.Unmarshal(document["https://example.com/art"], &literal); err != nil {
				t.Fatal(err)
			}
			if literal.Type != "@json" {
				t.Fatalf("opaque literal type changed: %s", literal.Type)
			}
			for _, field := range []struct {
				name string
				raw  json.RawMessage
				want map[string]any
			}{
				{"native data", document["data"], map[string]any{"sequence": json.Number("9007199254740993"), "@version": json.Number("1.10"), "@context": map[string]any{"@version": json.Number("1.10")}}},
				{"JSON literal", literal.Value, map[string]any{"sequence": json.Number("9007199254740993"), "@version": json.Number("1.10"), "@context": map[string]any{"@version": json.Number("1.10")}, "screenshots": []any{"first", "second"}}},
			} {
				decoder := json.NewDecoder(bytes.NewReader(field.raw))
				decoder.UseNumber()
				var value map[string]any
				if err := decoder.Decode(&value); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(value, field.want) {
					t.Fatalf("%s changed: %s", field.name, field.raw)
				}
			}
			if err := ValidateARDEntry(got); err != nil {
				t.Fatalf("normalized complete entry: %v", err)
			}
		})
	}
}

func TestNormalizeARDEntryInvalidContextVersion(t *testing.T) {
	t.Parallel()
	for _, version := range []string{`"1.1"`, "null", "true", "false", "1", "1.2", "0", "1e1000", "{}", "[]"} {
		t.Run(version, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"identifier":"urn:air:example.com:demo:agent","displayName":"Demo","type":"application/json","url":"https://example.com/card","@context":{"@version":%s}}`, version))
			if _, err := NormalizeARDEntry(raw, nil); err == nil {
				t.Fatal("invalid context version was accepted")
			}
		})
	}
}

func TestNumericContextVersionsInArraysScopesAndRemoteDocuments(t *testing.T) {
	t.Parallel()
	contexts := []struct {
		name    string
		context string
		remote  string
		scoped  bool
	}{
		{"array", `[{"@version":%s}, {"opaque":{"@id":"https://example.com/opaque","@type":"@json"}}]`, "", false},
		{"scoped", `{"@version":1.1,"child":{"@id":"https://example.com/child","@context":{"@version":%s,"value":"https://example.com/value"}}}`, "", true},
		{"remote", `"https://example.com/context"`, `{"@context":{"@version":%s,"opaque":{"@id":"https://example.com/opaque","@type":"@json"}}}`, false},
		{"remote array", `"https://example.com/context"`, `{"@context":[{"@version":%s},{"opaque":{"@id":"https://example.com/opaque","@type":"@json"}}]}`, false},
		{"remote scoped", `"https://example.com/context"`, `{"@context":{"child":{"@id":"https://example.com/child","@context":{"@version":%s,"value":"https://example.com/value"}}}}`, true},
	}
	for _, tc := range contexts {
		for _, version := range []string{"1.1", "1.10", "11e-1", `"1.1"`, "1.2"} {
			t.Run(tc.name+"/"+version, func(t *testing.T) {
				calls := 0
				contextValue, remote := tc.context, tc.remote
				if remote == "" {
					contextValue = fmt.Sprintf(contextValue, version)
				} else {
					remote = fmt.Sprintf(remote, version)
				}
				raw := []byte(fmt.Sprintf(`{"identifier":"urn:air:example.com:demo:agent","displayName":"Demo","type":"application/json","url":"https://example.com/card","@context":%s,"child":{"value":"hello"},"opaque":{"sequence":9007199254740993}}`, contextValue))
				got, err := NormalizeARDEntry(raw, func(uri string) ([]byte, error) {
					calls++
					if tc.remote == "" || uri != "https://example.com/context" {
						t.Fatalf("unexpected context fetch %q", uri)
					}
					return []byte(remote), nil
				})
				if tc.remote == "" && calls != 0 || tc.remote != "" && calls != 1 {
					t.Fatalf("context fetch count %d", calls)
				}
				if version == `"1.1"` || version == "1.2" {
					if err == nil || !strings.Contains(err.Error(), "invalid @version value") {
						t.Fatalf("invalid context version: %s, %v", got, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if tc.scoped && !bytes.Contains(got, []byte(`https://example.com/value`)) {
					t.Fatalf("scoped term was not expanded: %s", got)
				}
			})
		}
	}
}

func TestResolveARDTermNumericVersionPreservesCallerContext(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"1.1", "1.10", "11e-1"} {
		t.Run(version, func(t *testing.T) {
			contextValue := map[string]any{"@version": json.Number(version), "alias": map[string]any{"@id": ardNamespace + "tags"}}
			before := map[string]any{"@version": json.Number(version), "alias": map[string]any{"@id": ardNamespace + "tags"}}
			got, err := ResolveARDTerm("alias", contextValue)
			if err != nil || got != "tags" || !reflect.DeepEqual(contextValue, before) {
				t.Fatalf("term=%q error=%v context=%v", got, err, contextValue)
			}
		})
	}
	protected := []any{
		map[string]any{"@version": json.Number("1.1"), "alias": map[string]any{"@id": ardNamespace + "tags", "@protected": true}},
		map[string]any{"alias": ardNamespace + "displayName"},
	}
	if _, err := ResolveARDTerm("tags", protected); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("protected context override error=%v", err)
	}
}
