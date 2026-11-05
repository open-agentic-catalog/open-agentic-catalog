package ratings_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The published extension schema must compile offline and agree with emitted
// summaries. Runtime admission also checks enclosing identity and arithmetic.
func TestRatingExtensionSchema(t *testing.T) {
	raw, err := os.ReadFile("../../schemas/extensions/ratings-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err = c.AddResource("https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/ratings/v1", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile(ratings.IRI)
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]any{"reporter": "https://catalog.example.com", "identifier": "urn:air:example.com:agents:helper", "versionPresent": true, "version": "1.0.0", "minimum": 1, "maximum": 5, "count": 5, "sum": 17, "measuredAt": "2026-10-08T12:00:00Z"}
	for name, change := range map[string]func(map[string]any){
		"valid":                 func(map[string]any) {},
		"missing consent count": func(m map[string]any) { delete(m, "count") },
		"private identity":      func(m map[string]any) { m["subject"] = "private" },
		"wrong-case alias":      func(m map[string]any) { m["Count"] = m["count"] },
		"unsupported scale":     func(m map[string]any) { m["maximum"] = 10 },
		"fractional count":      func(m map[string]any) { m["count"] = 5.5 },
		"unsafe total":          func(m map[string]any) { m["sum"] = 9007199254740992.0 },
		"local time":            func(m map[string]any) { m["measuredAt"] = "2026-10-08T12:00:00+01:00" },
		"invalid date":          func(m map[string]any) { m["measuredAt"] = "not-a-dateZ" },
	} {
		t.Run(name, func(t *testing.T) {
			m := map[string]any{}
			for k, v := range valid {
				m[k] = v
			}
			change(m)
			wire, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(wire))
			if err != nil {
				t.Fatal(err)
			}
			err = schema.Validate(instance)
			if (err == nil) != (name == "valid") {
				t.Fatalf("schema %s: %v", name, err)
			}
			if name == "valid" && ratings.DecodeSummary(wire, "https://catalog.example.com", "urn:air:example.com:agents:helper", true, "1.0.0") == nil {
				t.Fatal("emitted valid summary not admitted")
			}
		})
	}
}
