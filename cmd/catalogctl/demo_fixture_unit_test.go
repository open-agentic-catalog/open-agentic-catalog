package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
)

// Expanding fixtures must not change the original demo's immutable representation evidence.
func TestExpandedDemoPreservesOriginalRepresentations(t *testing.T) {
	existing := smokeFixtures(demoID)
	for _, sample := range demodata.Samples() {
		for _, original := range existing {
			if original.Segment != sample.Key+"-"+demoID {
				continue
			}
			representation := demodata.Representation(sample, "")
			raw, err := json.Marshal(representation)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			// The original API input omits optional zero-valued input fields.
			for key, value := range got {
				if value == "" || value == nil {
					delete(got, key)
				}
			}
			wantRaw, err := json.Marshal(original.Representation)
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if err := json.Unmarshal(wantRaw, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) || sample.Name != original.Name || sample.Description != original.Description {
				t.Fatalf("%s changed original demo evidence", sample.Key)
			}
		}
	}
}
