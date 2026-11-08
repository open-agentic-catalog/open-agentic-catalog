package demodata

import (
	"encoding/json"
	"reflect"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"
	"pgregory.net/rapid"
)

func TestPropertySampleExecutionIdentity(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		manifest, assets := manifestFixture(t)
		// Two native Agent documents and one unselected resource keep the
		// generator small, valid and independent of external package delivery.
		manifest.Resources = manifest.Resources[:3]
		manifest.Resources[1].Namespace = manifest.Namespaces[1].Key
		manifest.Publishers = append(manifest.Publishers, Publisher{Key: "second", DisplayName: "Second publisher", Domains: []string{"second.example.com"}})
		manifest.Resources[1].Publisher = "second"
		manifest.Resources[1].Identifier = "urn:air:second.example.com:demo:documentation-agent"
		selected := []string{manifest.Resources[0].Key, manifest.Resources[1].Key}
		manifest.Profiles = map[string][]string{"expanded": selected}
		baseline, err := fixtureDecode(t, manifest, assets)
		if err != nil {
			rt.Fatal(err)
		}
		identities := map[string]string{}
		for _, item := range baseline.Resources {
			identities[item.Resource.Key] = item.Digest
		}
		// Marshal/decode gives the generated authoring graph independent slices,
		// pointers and maps; invariance must not mutate the baseline fixture.
		clone := func() Manifest {
			raw, marshalErr := json.Marshal(manifest)
			if marshalErr != nil {
				rt.Fatal(marshalErr)
			}
			var result Manifest
			if decodeErr := json.Unmarshal(raw, &result); decodeErr != nil {
				rt.Fatal(decodeErr)
			}
			return result
		}
		equivalent := clone()
		equivalent.Resources = rapid.Permutation(equivalent.Resources).Draw(rt, "resource authoring order")
		equivalent.Namespaces = rapid.Permutation(equivalent.Namespaces).Draw(rt, "namespace authoring order")
		equivalent.Publishers = rapid.Permutation(equivalent.Publishers).Draw(rt, "publisher authoring order")
		equivalent.Profiles["expanded"] = rapid.Permutation(selected).Draw(rt, "profile presentation order")
		plan, err := fixtureDecode(t, equivalent, assets)
		if err != nil || plan.Digest != baseline.Digest || plan.Profile != baseline.Profile {
			rt.Fatalf("equivalent authoring changed execution identity: %v", err)
		}
		for i, item := range plan.Resources {
			if item.Resource.Key != equivalent.Profiles["expanded"][i] || item.Digest != identities[item.Resource.Key] {
				rt.Fatal("profile presentation order or selected resource identity changed")
			}
		}
		if !reflect.DeepEqual(plan.Namespaces, equivalent.Namespaces) || !reflect.DeepEqual(plan.Publishers, equivalent.Publishers) {
			rt.Fatal("selected parent presentation order changed")
		}
		// YAML comments and serialization differ from the JSON authoring form,
		// while selected native bytes and all execution identity inputs stay fixed.
		authoringJSON, err := json.Marshal(equivalent)
		if err != nil {
			rt.Fatal(err)
		}
		var authoringFields map[string]any
		if err = json.Unmarshal(authoringJSON, &authoringFields); err != nil {
			rt.Fatal(err)
		}
		authored, err := yaml.Marshal(authoringFields)
		if err != nil {
			rt.Fatal(err)
		}
		assets["catalog.yaml"] = &fstest.MapFile{Data: append([]byte("# Authoring-only comment\n"), authored...)}
		commented, err := Decode(assets, "catalog.yaml", "expanded", "https://127.0.0.1:18443")
		if err != nil || commented.Digest != baseline.Digest {
			rt.Fatalf("authoring-only comment changed execution identity: %v", err)
		}
		unselected := clone()
		unselected.Resources[2].Description += rapid.StringMatching(`[a-z]{1,16}`).Draw(rt, "unselected edit")
		assets["unreferenced.txt"] = &fstest.MapFile{Data: []byte("unreferenced changed asset")}
		ignored, err := fixtureDecode(t, unselected, assets)
		if err != nil || ignored.Digest != baseline.Digest {
			rt.Fatalf("unselected edit changed execution identity: %v", err)
		}
		for _, change := range []string{"native bytes", "listing semantics", "publisher semantics", "namespace semantics"} {
			changed := clone()
			switch change {
			case "native bytes":
				value := *changed.Resources[0].Representation.Inline + "\n"
				changed.Resources[0].Representation.Inline = &value
			case "listing semantics":
				changed.Resources[0].Description += " Changed selected semantics."
			case "publisher semantics":
				for i := range changed.Publishers {
					if changed.Publishers[i].Key == changed.Resources[0].Publisher {
						changed.Publishers[i].DisplayName += " Changed"
					}
				}
			case "namespace semantics":
				for i := range changed.Namespaces {
					if changed.Namespaces[i].Key == changed.Resources[0].Namespace {
						changed.Namespaces[i].DisplayName += " Changed"
					}
				}
			}
			result, decodeErr := fixtureDecode(t, changed, assets)
			if decodeErr != nil || result.Digest == baseline.Digest {
				rt.Fatalf("%s failed to bind selected identity: %v", change, decodeErr)
			}
		}
		if !reflect.DeepEqual(selected, manifest.Profiles["expanded"]) {
			rt.Fatal("generation changed the baseline selection")
		}
	})
}
