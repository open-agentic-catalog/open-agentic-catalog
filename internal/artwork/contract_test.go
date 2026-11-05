package artwork_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type contractFetcher struct{ body []byte }

func (f contractFetcher) Fetch(context.Context, artwork.FetchRequest) (artwork.FetchResult, error) {
	return artwork.FetchResult{Body: f.body, MediaType: "image/png"}, nil
}

func TestPublicArtworkSchemaAndJSONLDRoundTrip(t *testing.T) {
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	service, err := artwork.New(contractFetcher{body.Bytes()}, artwork.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	asset := artwork.AssetInput{URL: "https://images.example/owned.png", Alt: "Owned fixture", Rights: &artwork.Rights{Basis: "owned", Attribution: "Catalog synthetic test"}}
	descriptor, err := service.Seal(t.Context(), "tenant", artwork.Input{SchemaVersion: 1, Icon: &asset, Screenshots: []artwork.AssetInput{asset, asset}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Screenshots[0].Caption = "First"
	descriptor.Screenshots[1].Caption = "Second"
	raw, err := os.ReadFile("../../schemas/extensions/artwork-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schemaDocument any
	if err = json.Unmarshal(raw, &schemaDocument); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const schemaID = "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/artwork/v1"
	if err = compiler.AddResource(schemaID, schemaDocument); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(schemaID + "#descriptor")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err = json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if err = schema.Validate(document); err != nil {
		t.Fatalf("sealed descriptor disagrees with checked schema: %v", err)
	}
	entry := map[string]any{
		"@id":        "urn:air:publisher.example:agent:artwork-test",
		"@type":      "application/a2a-agent-card+json",
		"url":        "https://publisher.example/agent.json",
		"name":       "Artwork test",
		"@context":   map[string]any{"oacArtwork": map[string]any{"@id": artwork.IRI, "@type": "@json"}},
		"oacArtwork": descriptor,
	}
	encoded, err = json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	fetches := 0
	normalized, err := standards.NormalizeARDEntry(encoded, func(string) ([]byte, error) {
		fetches++
		t.Fatal("inline contract attempted remote context retrieval")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := artwork.Extract(normalized, artwork.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if fetches != 0 || got == nil || got.Icon.Digest != descriptor.Icon.Digest || len(got.Screenshots) != 2 || got.Screenshots[0].Caption != "First" || got.Screenshots[1].Caption != "Second" {
		t.Fatalf("publication/receipt round trip lost facts or order: %#v", got)
	}
	aiEntry := map[string]any{"identifier": "urn:air:publisher.example:agent:artwork-test", "type": "application/a2a-agent-card+json", "url": "https://publisher.example/agent.json", "extensions": map[string]any{artwork.IRI: descriptor}}
	aiDocument, err := json.Marshal(map[string]any{"specVersion": "1.0", "host": map[string]any{"displayName": "Owned Catalog fixture"}, "entries": []any{aiEntry}})
	if err != nil {
		t.Fatal(err)
	}
	if err := standards.ValidateAICatalogLevel2(aiDocument); err != nil {
		t.Fatalf("AI Catalog extension contract rejected: %v", err)
	}
	aiRaw, err := json.Marshal(aiEntry)
	if err != nil {
		t.Fatal(err)
	}
	aiArtwork, err := artwork.ExtractAI(aiRaw, artwork.DefaultLimits())
	if err != nil || aiArtwork == nil || aiArtwork.Icon.Digest != descriptor.Icon.Digest || aiArtwork.Screenshots[0].Caption != "First" {
		t.Fatalf("AI Catalog extraction lost evidence: %#v %v", aiArtwork, err)
	}
	bad, ok := document.(map[string]any)
	if !ok {
		t.Fatal("descriptor is not an object")
	}
	bad["extra"] = true
	if err = schema.Validate(bad); err == nil {
		t.Fatal("public descriptor schema accepted unknown control field")
	}
}
