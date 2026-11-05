package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDemoCategoriesInputAndExactReadback(t *testing.T) {
	f := demoFixtures()[0]
	f.listing.Categories = []string{" Tools ", "DATA, records", "tools"}
	input := demoListingInput(f, "publisher", "revision")
	if !reflect.DeepEqual(input["categories"], []string{"data, records", "tools"}) {
		t.Fatal("input changed")
	}
	values, ok := input["categories"].([]string)
	if !ok {
		t.Fatal("categories are not a string list")
	}
	values[0] = "changed"
	if f.listing.Categories[0] != " Tools " {
		t.Fatal("input aliases manifest")
	}
	for _, test := range []struct {
		name       string
		categories any
		valid      bool
	}{
		{"normalized", []string{"data, records", "tools"}, true},
		{"missing", nil, false},
		{"different", []string{"other"}, false},
		{"reordered", []string{"tools", "data, records"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := demoReceipt{NamespaceID: "namespace", PublisherID: "publisher"}
			steps := demoItemReadResponses(t, f, r, demoUnitItem(f))
			for i := range steps {
				if steps[i].name != "listing revision" {
					continue
				}
				var value map[string]any
				if err := json.Unmarshal([]byte(steps[i].body), &value); err != nil {
					t.Fatal(err)
				}
				value["categories"] = test.categories
				steps[i].body = demoUnitJSON(t, value)
				if !test.valid {
					steps = steps[:i+1]
				}
				break
			}
			err := verifyDemoItem(t.Context(), demoUnitClient(t, steps, false, nil), r, demoUnitItem(f), f)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v, error=%v", test.valid, err)
			}
		})
	}
}
