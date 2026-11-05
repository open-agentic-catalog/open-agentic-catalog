// Package draftfile decodes bounded, reusable authoring input. It does not read
// files, fetch URLs, validate native profiles or authorize Catalog commands.
package draftfile

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"mime"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const (
	Format = "open-agentic-catalog-draft-v1"
	// MaxBytes bounds the complete encoded file, including JSON escaping.
	MaxBytes = 64 << 20
	// BrowserTextBytes preserves the current raw editor limit. Guided editors
	// can have lower generation bounds and their existing Advanced fallback.
	BrowserTextBytes = 2 << 20
)

var (
	ErrInvalid = errors.New("draft JSON does not match the file contract")
	ErrLimit   = errors.New("draft JSON exceeds a supported byte or structure limit")
)

// File contains authoring intent, not destination identities or command state.
type File struct {
	Format              string           `json:"format"`
	Kind                catalog.Kind     `json:"kind"`
	CanonicalIdentifier string           `json:"canonicalIdentifier"`
	Version             string           `json:"version"`
	Representations     []Representation `json:"representations"`
	Listing             *Listing         `json:"listing,omitempty"`
}

type Representation struct {
	Role      catalog.RepresentationRole `json:"role"`
	MediaType string                     `json:"mediaType"`
	ProfileID string                     `json:"profileId"`
	Input     Input                      `json:"input"`
}

// Input is the closed union in CatalogDraftRepresentation. Decode checks the
// selected mode against its schema, including fields from inactive modes.
// Pointer strings distinguish absent values from deliberately blank input.
type Input struct {
	Mode           string              `json:"mode"`
	Text           *string             `json:"text,omitempty"`
	URL            *string             `json:"url,omitempty"`
	Handoff        *string             `json:"handoff,omitempty"`
	ExpectedDigest *string             `json:"expectedDigest,omitempty"`
	Manifest       *string             `json:"manifest,omitempty"`
	MCP            *string             `json:"mcp,omitempty"`
	Skills         *[]string           `json:"skills,omitempty"`
	SourceFiles    *[]PluginSourceFile `json:"sourceFiles,omitempty"`
}

type PluginSourceFile struct {
	Path     string `json:"path"`
	Retained bool   `json:"retained"`
}

// DocumentMode preserves opaque text while selecting the normal Control API
// input form. JSON adapters require data even when storage uses an artifact.
func DocumentMode(mediaType string) string {
	media, _, err := mime.ParseMediaType(mediaType)
	if err == nil && (media == "application/json" || strings.HasSuffix(media, "+json")) {
		return "data"
	}
	return "content"
}

type Listing struct {
	DisplayName           string                 `json:"displayName"`
	Description           string                 `json:"description"`
	Visibility            catalog.Visibility     `json:"visibility"`
	Tags                  []string               `json:"tags,omitempty"`
	Categories            []string               `json:"categories,omitempty"`
	RepresentativeQueries []string               `json:"representativeQueries,omitempty"`
	DocumentationURL      string                 `json:"documentationUrl,omitempty"`
	SupportURL            string                 `json:"supportUrl,omitempty"`
	TermsURL              string                 `json:"termsUrl,omitempty"`
	License               string                 `json:"license,omitempty"`
	Relationships         []catalog.Relationship `json:"relationships,omitempty"`
	Extensions            *string                `json:"extensions,omitempty"`
	Artwork               *artwork.Input         `json:"artwork,omitempty"`
}

// The checked-in OpenAPI component is the sole schema. Native schemas are not
// copied into this codec. Loading it resolves only embedded local references.
var fileSchema = sync.OnceValue(func() *openapi3.Schema {
	contract, err := openapi3.NewLoader().LoadFromData(api.ControlV1)
	if err != nil {
		panic("cannot load the embedded draft file contract")
	}
	schema := contract.Components.Schemas["CatalogDraftFile"]
	if schema == nil || schema.Value == nil {
		panic("missing embedded draft file contract")
	}
	return schema.Value
})

// InitializeContract resolves the embedded schema before API request admission.
// CLI callers can retain lazy loading; workers do not need this initialization.
func InitializeContract() {
	_ = fileSchema()
}

// Decode checks the file envelope and work bounds. Native documents, extension
// editor text and handoffs remain strings, including incomplete or invalid text.
func Decode(raw []byte) (File, error) {
	if len(raw) > MaxBytes {
		return File{}, ErrLimit
	}
	if err := checkStructure(raw); err != nil {
		return File{}, err
	}
	var file File
	if jsondoc.Decode(raw, &file) != nil {
		return File{}, ErrInvalid
	}
	var value any
	if json.Unmarshal(raw, &value) != nil || fileSchema().VisitJSON(value, openapi3.EnableJSONSchema2020()) != nil {
		return File{}, ErrInvalid
	}
	if err := checkTextBytes(file, catalog.MaxCatalogDocumentBytes); err != nil {
		return File{}, err
	}
	return file, nil
}

// Bound structural allocation before a typed slice or duplicate-key map is
// created. Opaque document strings count as single values. The limits exceed
// the largest legal file shape, including sixteen complete Plugin inventories.
func checkStructure(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	type frame struct {
		array bool
		items int
	}
	stack := make([]frame, 0, 8)
	for count := 0; ; count++ {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return ErrInvalid
		}
		if count >= 262144 {
			return ErrLimit
		}
		delimiter, composite := token.(json.Delim)
		if composite && (delimiter == '}' || delimiter == ']') {
			stack = stack[:len(stack)-1]
			continue
		}
		if len(stack) > 0 && stack[len(stack)-1].array {
			stack[len(stack)-1].items++
			if stack[len(stack)-1].items > 1024 {
				return ErrLimit
			}
		}
		if composite {
			if len(stack) >= 64 {
				return ErrLimit
			}
			stack = append(stack, frame{array: delimiter == '['})
		}
	}
}

// Encode validates before returning a complete file. Strict marshaling refuses
// invalid UTF-8 instead of silently changing authored text.
func Encode(file File) ([]byte, error) {
	if err := checkTextBytes(file, catalog.MaxCatalogDocumentBytes); err != nil {
		return nil, err
	}
	var output limitedBuffer
	err := jsonv2.MarshalWrite(&output, file, json.DefaultOptionsV1(), jsontext.AllowInvalidUTF8(false))
	if err != nil {
		if errors.Is(err, ErrLimit) {
			return nil, ErrLimit
		}
		return nil, ErrInvalid
	}
	raw := output.buffer.Bytes()
	if _, err := Decode(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

type limitedBuffer struct{ buffer bytes.Buffer }

func (output *limitedBuffer) Write(raw []byte) (int, error) {
	if len(raw) > MaxBytes-output.buffer.Len() {
		return 0, ErrLimit
	}
	return output.buffer.Write(raw)
}

// BrowserCompatible checks raw editor sizes only. Local native findings and
// guarded JSON command serialization still decide whether the author can Save.
func BrowserCompatible(file File) error {
	return checkTextBytes(file, BrowserTextBytes)
}

func checkTextBytes(file File, perText int) error {
	documents, preparation := 0, 0
	check := func(text *string, total *int) bool {
		if text == nil {
			return true
		}
		*total += len(*text)
		return len(*text) <= perText && *total <= catalog.MaxCatalogDocumentBytes
	}
	for _, rep := range file.Representations {
		if rep.Input.SourceFiles != nil {
			for _, source := range *rep.Input.SourceFiles {
				if _, ok := catalog.NormalizeSkillPackagePath(source.Path); !ok {
					return ErrInvalid
				}
			}
		}
		if !check(rep.Input.Text, &documents) || !check(rep.Input.Manifest, &preparation) || !check(rep.Input.MCP, &preparation) {
			return ErrLimit
		}
		if rep.Input.Skills != nil {
			for _, text := range *rep.Input.Skills {
				if !check(&text, &preparation) {
					return ErrLimit
				}
			}
		}
	}
	if file.Listing != nil && file.Listing.Extensions != nil && len(*file.Listing.Extensions) > perText {
		return ErrLimit
	}
	if file.Listing != nil && len(file.Listing.License) > MaxBytes {
		return ErrLimit
	}
	return nil
}
