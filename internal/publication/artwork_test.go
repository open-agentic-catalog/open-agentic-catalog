package publication

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

func publicationArtwork() artwork.Descriptor {
	asset := artwork.Asset{AssetInput: artwork.AssetInput{URL: "https://images.example/icon.png", Alt: "Owned icon", Rights: &artwork.Rights{Basis: "owned", Attribution: "Catalog test fixture"}}, Digest: "sha256:" + strings.Repeat("a", 64), MediaType: "image/png", Size: 100, Width: 2, Height: 2, RightsAssessment: artwork.NotAssessed}
	return artwork.Descriptor{SchemaVersion: 1, Icon: &asset, Screenshots: []artwork.Asset{asset, asset}}
}

func TestPublicationArtworkRoundTripAndOptionalPolicyExclusion(t *testing.T) {
	record := eligiblePublicationRecord()
	record.ListingRevision.Categories = []string{"testing"}
	descriptor := publicationArtwork()
	descriptor.Screenshots[0].Caption = "First"
	descriptor.Screenshots[1].Caption = "Second"
	record.ListingRevision.Extensions = map[string]any{artwork.IRI: descriptor, "https://example/private": "must not publish"}
	manager := &Manager{artworkAllowed: func(artwork.Descriptor) bool { return true }}
	entry, _, reasons := manager.baseEntry(record)
	if len(reasons) > 0 || entry.Artwork == nil {
		t.Fatalf("eligible artwork omitted: %v", reasons)
	}
	entry.RepresentativeQueries = record.ListingRevision.RepresentativeQueries
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must not publish") {
		t.Fatal("opaque extension leaked")
	}
	normalized, err := standards.NormalizeARDEntry(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := artwork.Extract(normalized, artwork.HardLimits())
	if err != nil || decoded == nil || decoded.Screenshots[0].Caption != "First" || decoded.Screenshots[1].Caption != "Second" {
		t.Fatalf("JSON-LD changed artwork: %#v %v", decoded, err)
	}
	ai := toAICatalogEntry(entry)
	aiRaw, err := json.Marshal(ai)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = artwork.ExtractAI(aiRaw, artwork.HardLimits())
	if err != nil || decoded == nil || decoded.Icon.Digest != descriptor.Icon.Digest {
		t.Fatalf("AI Catalog artwork missing: %#v %v", decoded, err)
	}
	manager.artworkAllowed = func(artwork.Descriptor) bool { return false }
	entry, _, reasons = manager.baseEntry(record)
	if len(reasons) > 0 || entry.Artwork != nil {
		t.Fatalf("optional refusal excluded resource: %v", reasons)
	}
	manager.artworkAllowed = nil
	entry, _, _ = manager.baseEntry(record)
	if entry.Artwork != nil {
		t.Fatal("missing media policy exposed artwork")
	}
}
