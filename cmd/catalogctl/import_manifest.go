package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"go.yaml.in/yaml/v3"
)

const (
	importMaxManifestBytes = 2 << 20
	importMaxPreparedBytes = 64 << 20
	importMaxResources     = 64
	importStepsPerResource = 4
	importMaxSteps         = importMaxResources * importStepsPerResource
	importMaxReportBytes   = 2 << 20
	importPlaceholderID    = "00000000-0000-7000-8000-000000000001"
)

type importManifest struct {
	Format    int              `json:"format"`
	Resources []importResource `json:"resources"`
}

type importResource struct {
	contentOnly         bool
	existingResource    bool
	Key                 string                 `json:"key"`
	NamespaceID         string                 `json:"namespaceId"`
	CanonicalIdentifier string                 `json:"canonicalIdentifier"`
	Kind                catalog.Kind           `json:"kind"`
	Version             string                 `json:"version"`
	Listing             importListing          `json:"listing"`
	Representations     []importRepresentation `json:"representations"`
}

// RawMessage is intentional: jsondoc's interface decoder uses float64. Keep
// arbitrary extension values out of that decoder and use UseNumber later.
type importListing struct {
	PublisherID           string                 `json:"publisherId"`
	DisplayName           string                 `json:"displayName"`
	Description           string                 `json:"description"`
	Visibility            catalog.Visibility     `json:"visibility"`
	Tags                  []string               `json:"tags,omitempty"`
	Categories            []string               `json:"categories,omitempty"`
	RepresentativeQueries []string               `json:"representativeQueries,omitempty"`
	DocumentationURL      string                 `json:"documentationUrl,omitempty"`
	SupportURL            string                 `json:"supportUrl,omitempty"`
	License               string                 `json:"license,omitempty"`
	TermsURL              string                 `json:"termsUrl,omitempty"`
	Relationships         []catalog.Relationship `json:"relationships,omitempty"`
	Extensions            json.RawMessage        `json:"extensions,omitempty"`
	ExtensionsFile        string                 `json:"extensionsFile,omitempty"`
}

type importRepresentation struct {
	data             json.RawMessage
	content          *string
	Role             catalog.RepresentationRole `json:"role"`
	MediaType        string                     `json:"mediaType"`
	ProfileID        string                     `json:"profileId,omitempty"`
	DataFile         string                     `json:"dataFile,omitempty"`
	ContentFile      string                     `json:"contentFile,omitempty"`
	PackageFile      string                     `json:"packageFile,omitempty"`
	PackageDirectory string                     `json:"packageDirectory,omitempty"`
	URL              string                     `json:"url,omitempty"`
	Handoff          *catalog.SkillHandoff      `json:"handoff,omitempty"`
	ExpectedDigest   *string                    `json:"expectedDigest,omitempty"`
}

func decodeImportManifest(raw []byte) (importManifest, error) {
	var manifest importManifest
	if len(raw) > importMaxManifestBytes || !utf8.Valid(raw) {
		return manifest, safeerr.New("import manifest must be valid UTF-8 and at most 2 MiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var node, extra yaml.Node
	if decoder.Decode(&node) != nil || len(node.Content) != 1 || !errors.Is(decoder.Decode(&extra), io.EOF) {
		return manifest, safeerr.New("import manifest must contain exactly one YAML document")
	}
	count := 0
	value, err := importYAMLValue(&node, 0, &count)
	if err != nil {
		return manifest, err
	}
	encoded, err := json.Marshal(value)
	if err != nil || jsondoc.Decode(encoded, &manifest) != nil {
		return manifest, safeerr.New("import manifest has unknown, wrongly typed or null control fields")
	}
	if manifest.Format != 1 || len(manifest.Resources) < 1 || len(manifest.Resources) > importMaxResources {
		return manifest, safeerr.New("import format must be 1 with one to 64 resources")
	}
	return manifest, nil
}

// Convert scalar number lexemes directly to json.Number. YAML's default
// interface decoding would round floats and accepts non-JSON numeric forms.
func importYAMLValue(node *yaml.Node, depth int, count *int) (any, error) {
	*count++
	invalid := safeerr.New("import YAML exceeds its depth/node budget or uses unsupported tags, aliases, keys or scalars")
	if depth > 32 || *count > 20000 || node.Anchor != "" || node.Style&yaml.TaggedStyle != 0 {
		return nil, invalid
	}
	switch node.Kind {
	case yaml.AliasNode:
		return nil, invalid
	case yaml.DocumentNode:
		return importYAMLValue(node.Content[0], depth+1, count)
	case yaml.MappingNode:
		object := make(map[string]any, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			*count++
			if *count > 20000 || key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Anchor != "" || key.Style&yaml.TaggedStyle != 0 || key.Value == "<<" || !utf8.ValidString(key.Value) {
				return nil, invalid
			}
			if _, exists := object[key.Value]; exists {
				return nil, invalid
			}
			value, err := importYAMLValue(node.Content[index+1], depth+1, count)
			if err != nil {
				return nil, err
			}
			object[key.Value] = value
		}
		return object, nil
	case yaml.SequenceNode:
		values := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := importYAMLValue(child, depth+1, count)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	case yaml.ScalarNode:
		if !utf8.ValidString(node.Value) {
			return nil, invalid
		}
		switch node.Tag {
		case "!!str":
			// YAML's resolver treats an overflowing exponent as a string. A
			// plain JSON number still has numeric meaning; quoted text does not.
			if node.Style == 0 && importJSONNumber(node.Value) {
				return json.Number(node.Value), nil
			}
			return node.Value, nil
		case "!!null":
			return json.RawMessage("null"), nil // Closed control decoding rejects null outside extensions.
		case "!!bool":
			value, err := strconv.ParseBool(node.Value)
			if err == nil {
				return value, nil
			}
		case "!!int", "!!float":
			if importJSONNumber(node.Value) {
				return json.Number(node.Value), nil
			}
		}
	}
	return nil, invalid
}

func importJSONNumber(value string) bool {
	return len(value) > 0 && (value[0] == '-' || value[0] >= '0' && value[0] <= '9') && json.Valid([]byte(value)) && !strings.ContainsAny(value, " \r\n\t")
}
