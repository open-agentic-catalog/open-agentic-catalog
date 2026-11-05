package draftfile

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// FromListing projects retained metadata, omitting Catalog identities and sealed
// artwork facts. Destination policy and ordinary Save validation apply again.
func FromListing(source catalog.ListingRevision) (*Listing, error) {
	result := &Listing{
		DisplayName: source.DisplayName, Description: source.Description, Visibility: source.Visibility,
		Tags: append([]string(nil), source.Tags...), Categories: append([]string(nil), source.Categories...),
		RepresentativeQueries: append([]string(nil), source.RepresentativeQueries...),
		DocumentationURL:      source.DocumentationURL, SupportURL: source.SupportURL, TermsURL: source.TermsURL, License: source.License,
		Relationships: append([]catalog.Relationship(nil), source.Relationships...),
	}
	extensions := make(map[string]any, len(source.Extensions))
	for key, value := range source.Extensions {
		if key != artwork.IRI {
			extensions[key] = value
			continue
		}
		raw, err := jsonv2.Marshal(value, json.DefaultOptionsV1(), jsontext.AllowInvalidUTF8(false))
		if err != nil {
			return nil, ErrInvalid
		}
		sealed, err := artwork.DecodeDescriptor(raw, artwork.HardLimits())
		if err != nil {
			return nil, ErrInvalid
		}
		input := &artwork.Input{SchemaVersion: artwork.SchemaVersion}
		asset := func(value artwork.Asset) artwork.AssetInput {
			projected := value.AssetInput
			if projected.Rights != nil {
				rights := *projected.Rights
				projected.Rights = &rights
			}
			return projected
		}
		if sealed.Icon != nil {
			icon := asset(*sealed.Icon)
			input.Icon = &icon
		}
		for _, item := range sealed.Screenshots {
			input.Screenshots = append(input.Screenshots, asset(item))
		}
		result.Artwork = input
	}
	if len(extensions) > 0 {
		raw, err := jsonv2.Marshal(extensions, json.DefaultOptionsV1(), jsontext.AllowInvalidUTF8(false))
		if err != nil {
			return nil, ErrInvalid
		}
		text := string(raw)
		result.Extensions = &text
	}
	return result, nil
}
