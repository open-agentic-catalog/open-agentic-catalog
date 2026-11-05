package main

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
)

// demoListingExtensionsMatch permits only the server's validated artwork facts
// to differ from authoring input. Every opaque extension remains exact.
func demoListingExtensionsMatch(expected, retained any) bool {
	normalize := func(value any) (map[string]json.RawMessage, bool) {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, false
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, false
		}
		return object, true
	}
	inputExtensions, ok := normalize(expected)
	if !ok {
		return false
	}
	outputExtensions, ok := normalize(retained)
	if !ok || (inputExtensions == nil) != (outputExtensions == nil) || len(inputExtensions) != len(outputExtensions) {
		return false
	}
	for key, raw := range inputExtensions {
		actual, exists := outputExtensions[key]
		if !exists {
			return false
		}
		if key != artwork.IRI {
			var want, got any
			if json.Unmarshal(raw, &want) != nil || json.Unmarshal(actual, &got) != nil || !reflect.DeepEqual(want, got) {
				return false
			}
			continue
		}
		input, err := artwork.DecodeInput(raw, artwork.HardLimits())
		if err != nil {
			return false
		}
		descriptor, err := artwork.DecodeDescriptor(actual, artwork.HardLimits())
		if err != nil {
			return false
		}
		projected := artwork.Input{SchemaVersion: descriptor.SchemaVersion}
		if descriptor.Icon != nil {
			icon := descriptor.Icon.AssetInput
			projected.Icon = &icon
		}
		for _, asset := range descriptor.Screenshots {
			projected.Screenshots = append(projected.Screenshots, asset.AssetInput)
		}
		want, err := json.Marshal(input)
		if err != nil {
			return false
		}
		got, err := json.Marshal(projected)
		if err != nil || !bytes.Equal(want, got) {
			return false
		}
	}
	return true
}
