package draftfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func pointer(value string) *string { return &value }

func sample() File {
	return File{Format: Format, Kind: catalog.KindAgent, CanonicalIdentifier: "custom:agent", Version: "1.0.0",
		Representations: []Representation{{Role: catalog.RepresentationPrimary, MediaType: "application/a2a-agent-card+json", Input: Input{Mode: "data", Text: pointer(`{"unknown":9007199254740993}`)}}}}
}

func TestSharedFileFixtures(t *testing.T) {
	raw, err := os.ReadFile("../../api/testdata/catalog-draft/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string `json:"name"`
		Document string `json:"document"`
		Valid    bool   `json:"valid"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			file, err := Decode([]byte(test.Document))
			if (err == nil) != test.Valid {
				t.Fatalf("valid=%v: %v", test.Valid, err)
			}
			if !test.Valid {
				if !reflect.DeepEqual(file, File{}) || !errors.Is(err, ErrInvalid) {
					t.Fatal("invalid file returned partial input or an unclassified error", err)
				}
				return
			}
			encoded, err := Encode(file)
			if err != nil {
				t.Fatal(err)
			}
			result, err := Decode(encoded)
			if err != nil || !reflect.DeepEqual(file, result) {
				t.Fatal("file changed authored fields")
			}
			if err := BrowserCompatible(file); err != nil {
				t.Fatal("small shared fixture cannot fit existing raw editors", err)
			}
		})
	}
}

func TestTextFidelityAndSafeFailure(t *testing.T) {
	value := sample()
	const native = " {\"integer\":9007199254740993,\"decimal\":0.123456789012345678901,\"exponent\":1e999,\"signatures\":[{\"signature\":\"AA\"}],\"path\":\"/scripts/run\"}\r\n"
	value.Representations[0].Input.Text = pointer(native)
	value.Listing = &Listing{DisplayName: "", Description: "", Visibility: catalog.VisibilityPrivate, Extensions: pointer(`{"number":9007199254740993,`)}
	raw, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil || *decoded.Representations[0].Input.Text != native || *decoded.Listing.Extensions != *value.Listing.Extensions {
		t.Fatal("opaque text was parsed, repaired or rounded", err)
	}
	for _, raw := range [][]byte{[]byte(`{"private-secret-value":"secret"}`), {0xff}, []byte("\xef\xbb\xbf{}"), []byte(`{"format":"\ud800"}`)} {
		_, err := Decode(raw)
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private-secret-value") {
			t.Fatal("invalid input was accepted or disclosed", err)
		}
	}
	value.Representations[0].Input.Text = pointer(string([]byte{0xff}))
	if output, err := Encode(value); !errors.Is(err, ErrInvalid) || output != nil {
		t.Fatal("invalid UTF-8 was normalized or published", err)
	}
	value = sample()
	value.Representations[0].Input.URL = pointer("https://inactive.example.test/card.json")
	if output, err := Encode(value); !errors.Is(err, ErrInvalid) || output != nil {
		t.Fatal("inactive input was serialized", err)
	}
}

func TestEncodedAndDecodedByteLimits(t *testing.T) {
	value := sample()
	value.CanonicalIdentifier = strings.Repeat("\U0001f642", 512)
	if _, err := Encode(value); err != nil {
		t.Fatal("schema character bounds were treated as byte bounds", err)
	}
	value.CanonicalIdentifier += "x"
	if _, err := Encode(value); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized identifier accepted", err)
	}
	value = sample()
	value.Representations[0].Input.Text = pointer(strings.Repeat("x", BrowserTextBytes))
	if err := BrowserCompatible(value); err != nil {
		t.Fatal(err)
	}
	value.Representations[0].Input.Text = pointer(*value.Representations[0].Input.Text + "x")
	if _, err := Encode(value); err != nil {
		t.Fatal("browser editor limit changed the shared CLI file limit", err)
	}
	if err := BrowserCompatible(value); !errors.Is(err, ErrLimit) {
		t.Fatal("browser size overflow accepted", err)
	}
	value.Representations[0].Input.Text = pointer(strings.Repeat("\U0001f642", catalog.MaxCatalogDocumentBytes/4))
	if _, err := Encode(value); err != nil {
		t.Fatal("exact document byte budget rejected", err)
	}
	value.Representations = append(value.Representations, Representation{Role: catalog.RepresentationMetadata, MediaType: "text/plain", Input: Input{Mode: "content", Text: pointer("x")}})
	if raw, err := Encode(value); !errors.Is(err, ErrLimit) || raw != nil {
		t.Fatal("aggregate revision budget not enforced", err)
	}
	value = sample()
	value.Representations[0].Input.Text = pointer(strings.Repeat("\x01", 11<<20))
	if raw, err := Encode(value); !errors.Is(err, ErrLimit) || raw != nil {
		t.Fatal("JSON escaping overflow produced a completed file", err)
	}
	if _, err := Decode(make([]byte, MaxBytes+1)); !errors.Is(err, ErrLimit) {
		t.Fatal("encoded input overflow accepted", err)
	}
}

func TestPreparationAndExtensionBudgets(t *testing.T) {
	value := sample()
	skills := []string{"one"}
	value.Kind = catalog.KindPlugin
	value.Representations[0].Input = Input{Mode: "plugin-preparation", Manifest: pointer(strings.Repeat("x", BrowserTextBytes)), MCP: pointer(""), Skills: &skills}
	if err := BrowserCompatible(value); err != nil {
		t.Fatal(err)
	}
	value.Representations[0].Input.Manifest = pointer(strings.Repeat("x", BrowserTextBytes+1))
	if err := BrowserCompatible(value); !errors.Is(err, ErrLimit) {
		t.Fatal("oversized preparation document accepted", err)
	}
	value.Representations[0].Input.Manifest = pointer("")
	skills[0] = strings.Repeat("x", BrowserTextBytes+1)
	if err := BrowserCompatible(value); !errors.Is(err, ErrLimit) {
		t.Fatal("oversized prepared Skill accepted", err)
	}
	skills[0] = strings.Repeat("x", catalog.MaxCatalogDocumentBytes)
	value.Representations[0].Input.MCP = pointer("x")
	if _, err := Encode(value); !errors.Is(err, ErrLimit) {
		t.Fatal("aggregate preparation budget not enforced", err)
	}
	value = sample()
	value.Listing = &Listing{Visibility: catalog.VisibilityPrivate, Extensions: pointer(strings.Repeat("x", BrowserTextBytes))}
	if err := BrowserCompatible(value); err != nil {
		t.Fatal(err)
	}
	value.Listing.Extensions = pointer(*value.Listing.Extensions + "x")
	if err := BrowserCompatible(value); !errors.Is(err, ErrLimit) {
		t.Fatal("oversized extension editor accepted", err)
	}
}

func TestListingProjection(t *testing.T) {
	asset := artwork.Asset{AssetInput: artwork.AssetInput{URL: "https://images.example.test/icon.png", Alt: "Example", Rights: &artwork.Rights{Basis: "owned", Attribution: "Example"}}, Digest: "sha256:" + strings.Repeat("a", 64), MediaType: "image/png", Size: 100, Width: 1, Height: 1, RightsAssessment: artwork.NotAssessed}
	sealed := artwork.Descriptor{SchemaVersion: 1, Icon: &asset, Screenshots: []artwork.Asset{asset}}
	source := catalog.ListingRevision{ID: "source-id", TenantID: "tenant-id", PublisherID: "publisher-id", DisplayName: "Test", Description: "Description", Visibility: catalog.VisibilityPrivate, Tags: []string{"One"}, Categories: []string{"Two"}, RepresentativeQueries: []string{"Find this"}, DocumentationURL: "https://docs.example.test", SupportURL: "https://support.example.test", TermsURL: "https://terms.example.test", License: "Apache-2.0", Relationships: []catalog.Relationship{{Type: "depends_on", TargetIdentifier: "custom:other"}}, Extensions: map[string]any{artwork.IRI: sealed, "example.test": map[string]any{"integer": json.Number("9007199254740993"), "decimal": json.Number("0.123456789012345678901")}}}
	listing, err := FromListing(source)
	if err != nil {
		t.Fatal(err)
	}
	value := sample()
	value.Listing = listing
	raw, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"source-id", "tenant-id", "publisher-id", "rightsAssessment", "sha256:", "image/png", "digest", "width"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("projection copied %q", forbidden)
		}
	}
	if listing.Artwork.Icon.URL != asset.URL || len(listing.Artwork.Screenshots) != 1 || !strings.Contains(*listing.Extensions, "9007199254740993") || strings.Contains(*listing.Extensions, artwork.IRI) {
		t.Fatal("projection lost authoring input or extension precision")
	}
	listing.Tags[0] = "Changed"
	listing.Relationships[0].TargetIdentifier = "changed"
	listing.Artwork.Icon.Rights.Attribution = "Changed"
	if source.Tags[0] != "One" || source.Relationships[0].TargetIdentifier != "custom:other" || asset.Rights.Attribution != "Example" {
		t.Fatal("projection shared mutable retained input")
	}
	for _, extensions := range []map[string]any{{artwork.IRI: true}, {artwork.IRI: math.Inf(1)}, {"example.test": math.Inf(1)}} {
		source.Extensions = extensions
		if value, err := FromListing(source); value != nil || !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid retained extension projection accepted", err)
		}
	}
	if value, err := FromListing(catalog.ListingRevision{}); err != nil || value.Extensions != nil || value.Artwork != nil {
		t.Fatal("empty optional extensions changed", err)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"format":"open-agentic-catalog-draft-v1","kind":"agent","canonicalIdentifier":"","version":"","representations":[{"role":"primary","mediaType":"","profileId":"","input":{"mode":"data","text":"{"}}]}`))
	f.Add([]byte(`{"format":null}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		value, err := Decode(raw)
		if err != nil {
			return
		}
		encoded, err := Encode(value)
		if err != nil {
			t.Fatal("accepted file cannot be encoded", err)
		}
		decoded, err := Decode(encoded)
		if err != nil || !reflect.DeepEqual(decoded.Representations, value.Representations) {
			t.Fatal("accepted native input changed", err)
		}
		again, err := Encode(decoded)
		if err != nil || !bytes.Equal(again, encoded) {
			t.Fatal("file serialization is not stable", err)
		}
	})
}

func TestDocumentModeUsesNativeJSONInputs(t *testing.T) {
	for media, want := range map[string]string{
		"application/json":                                "data",
		"application/a2a-agent-card+json":                 "data",
		"application/mcp-server-card+json":                "data",
		"application/agent-skills+json":                   "data",
		"Application/JSON; charset=utf-8":                 "data",
		"text/markdown; profile=\"urn:air:agent-skills\"": "content",
		"text/plain; note=\"+json\"":                      "content",
		"unfinished media type":                           "content",
	} {
		if got := DocumentMode(media); got != want {
			t.Errorf("%q: want %s, got %s", media, want, got)
		}
	}
}
