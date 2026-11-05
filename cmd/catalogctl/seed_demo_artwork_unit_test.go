package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
)

func TestDemoArtworkSealingResponseComparison(t *testing.T) {
	inputAsset := artwork.AssetInput{URL: "https://images.example/icon.png", Alt: "Owned icon", Rights: &artwork.Rights{Basis: "owned", Attribution: "Fixture author"}}
	input := artwork.Input{SchemaVersion: 1, Icon: &inputAsset, Screenshots: []artwork.AssetInput{inputAsset, {URL: "https://images.example/screen.jpg", Alt: "Second", Rights: inputAsset.Rights}}}
	sealed := func(a artwork.AssetInput) artwork.Asset {
		return artwork.Asset{AssetInput: a, Digest: "sha256:" + strings.Repeat("a", 64), MediaType: "image/png", Size: 100, Width: 32, Height: 32, RightsAssessment: artwork.NotAssessed}
	}
	icon := sealed(inputAsset)
	descriptor := artwork.Descriptor{SchemaVersion: 1, Icon: &icon, Screenshots: []artwork.Asset{sealed(input.Screenshots[0]), sealed(input.Screenshots[1])}}
	extensions := func(value any) map[string]any {
		return map[string]any{artwork.IRI: value, "opaque": map[string]any{"nested": []any{"unchanged", float64(3)}}}
	}
	expected := extensions(input)
	actual := extensions(descriptor)
	if !demoListingExtensionsMatch(expected, actual) {
		t.Fatal("valid sealed facts must preserve authoring input")
	}
	id, revisionID := demoTestUUID(1400), demoTestUUID(1401)
	body := map[string]any{"displayName": "Artwork", "extensions": expected}
	revision := map[string]any{"id": revisionID, "tenantId": demoTenant, "listingId": id, "displayName": "Artwork", "extensions": actual}
	result := map[string]any{"listing": map[string]any{"id": id, "tenantId": demoTenant, "candidateRevisionId": revisionID}, "revision": revision}
	step := demoStep{Path: "/api/v1/catalog-listings/" + id + "/revisions", Body: []byte(demoUnitJSON(t, body)), Response: []byte(demoUnitJSON(t, result))}
	if err := validateDemoStepResponse(step); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(map[string]any, map[string]any)
	}{
		{"opaque changed", func(_, got map[string]any) { got["opaque"] = "changed" }},
		{"opaque added", func(_, got map[string]any) { got["extra"] = true }},
		{"opaque removed", func(_, got map[string]any) { delete(got, "opaque") }},
		{"opaque renamed", func(_, got map[string]any) { got["other"] = got["opaque"]; delete(got, "opaque") }},
		{"forged authoring facts", func(want, _ map[string]any) { want[artwork.IRI] = descriptor }},
		{"authoring text changed", func(_, got map[string]any) {
			changed := descriptor
			a := *changed.Icon
			a.Alt = "Other"
			changed.Icon = &a
			got[artwork.IRI] = changed
		}},
		{"rights changed", func(_, got map[string]any) {
			changed := descriptor
			a := *changed.Icon
			a.Rights = &artwork.Rights{Basis: "owned", Attribution: "Other"}
			changed.Icon = &a
			got[artwork.IRI] = changed
		}},
		{"screenshots reordered", func(_, got map[string]any) {
			changed := descriptor
			changed.Screenshots = []artwork.Asset{descriptor.Screenshots[1], descriptor.Screenshots[0]}
			got[artwork.IRI] = changed
		}},
		{"invalid sealed digest", func(_, got map[string]any) {
			changed := descriptor
			a := *changed.Icon
			a.Digest = "invalid"
			changed.Icon = &a
			got[artwork.IRI] = changed
		}},
		{"invalid sealed assessment", func(_, got map[string]any) {
			changed := descriptor
			a := *changed.Icon
			a.RightsAssessment = "approved"
			changed.Icon = &a
			got[artwork.IRI] = changed
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, got := extensions(input), extensions(descriptor)
			tc.change(want, got)
			if demoListingExtensionsMatch(want, got) {
				t.Fatal("accepted mismatching extensions")
			}
			body["extensions"] = want
			revision["extensions"] = got
			step.Body = json.RawMessage(demoUnitJSON(t, body))
			step.Response = json.RawMessage(demoUnitJSON(t, result))
			if validateDemoStepResponse(step) == nil {
				t.Fatal("accepted mismatching revision response")
			}
		})
	}
	body["extensions"], revision["extensions"] = expected, actual
	revision["displayName"] = "Changed"
	step.Body = json.RawMessage(demoUnitJSON(t, body))
	step.Response = json.RawMessage(demoUnitJSON(t, result))
	if validateDemoStepResponse(step) == nil {
		t.Fatal("artwork must not relax other revision fields")
	}
}
