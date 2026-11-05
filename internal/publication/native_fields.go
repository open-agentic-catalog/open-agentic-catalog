package publication

import (
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"mime"
	"slices"
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Native paths are literal members inside data, not JSON-LD terms or a
// general path language. Protocol versions belong to their declared format.
var nativePaths = map[string]string{
	"data.skills.id":                           adapter.MediaA2AAgentCard,
	"data.skills.tags":                         adapter.MediaA2AAgentCard,
	"data.defaultInputModes":                   adapter.MediaA2AAgentCard,
	"data.defaultOutputModes":                  adapter.MediaA2AAgentCard,
	"data.capabilities.streaming":              adapter.MediaA2AAgentCard,
	"data.supportedInterfaces.protocolBinding": adapter.MediaA2AAgentCard,
	"data.supportedInterfaces.protocolVersion": adapter.MediaA2AAgentCard,
	"data.remotes.type":                        adapter.MediaMCPServerCard,
	"data.remotes.supportedProtocolVersions":   adapter.MediaMCPServerCard,
}

type nativeField struct {
	values  []string
	present bool
}

type nativeProjection map[string]nativeField

func nativeFilterFields(filter map[string]any) []string {
	fields := make([]string, 0, len(filter))
	for field := range filter {
		if _, ok := nativePaths[field]; ok {
			fields = append(fields, field)
		}
	}
	return fields
}

// Project each needed inline document once. Search ranking and Explore facets
// reuse these query-local values. The retained documents remain unchanged.
func projectNativeEntries(ctx context.Context, entries []Entry, filter map[string]any, facets []ExploreFacetRequest) ([]nativeProjection, error) {
	fields := nativeFilterFields(filter)
	for _, facet := range facets {
		if _, ok := nativePaths[facet.Field]; ok && !slices.Contains(fields, facet.Field) {
			fields = append(fields, facet.Field)
		}
	}
	if len(fields) == 0 {
		return nil, ctx.Err()
	}
	projections := make([]nativeProjection, len(entries))
	totalBytes := 0
	for i, entry := range entries {
		// Reuse the existing ARD document ceiling as an aggregate work bound.
		totalBytes += len(entry.Data)
		if totalBytes > MaxPublicationDocumentBytes {
			return nil, catalog.E(catalog.CodeWorkLimitExceeded, "ARD native field inspection exceeds the publication document limit")
		}
		projection, err := projectNativeEntry(ctx, entry, fields)
		if err != nil {
			if catalog.ErrorCodeOf(err) == catalog.CodeInvalidArgument {
				return nil, catalog.E(catalog.CodeInternal, "active ARD native data cannot be inspected")
			}
			return nil, err
		}
		projections[i] = projection
	}
	return projections, nil
}

func nativeAt(projections []nativeProjection, i int) nativeProjection {
	if len(projections) == 0 {
		return nil
	}
	return projections[i]
}

func projectNativeEntry(ctx context.Context, entry Entry, fields []string) (nativeProjection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	projection := make(nativeProjection, len(fields))
	mediaType, _, _ := mime.ParseMediaType(entry.Type)
	var selected []string
	for _, field := range fields {
		if nativePaths[field] == mediaType {
			selected = append(selected, field)
		} else if entry.Type != "" {
			// A disclosed other media type cannot supply this native fact.
			projection[field] = nativeField{present: true}
		}
	}
	if len(selected) == 0 || len(entry.Data) == 0 {
		return projection, nil
	}
	if len(entry.Data) > MaxPublicationDocumentBytes {
		return nil, catalog.E(catalog.CodeWorkLimitExceeded, "ARD native field inspection exceeds the publication document limit")
	}
	var document map[string]any
	// The v2 decoder rejects duplicate keys and malformed Unicode. Decode
	// only once; unselected members never acquire filter semantics. Preserve
	// numbers as raw JSON so opaque extensions need not fit in a float64.
	if jsonv2.Unmarshal(entry.Data, &document, jsonv2.WithUnmarshalers(
		jsonv2.UnmarshalFromFunc(func(dec *jsontext.Decoder, value *any) error {
			if dec.PeekKind() == '0' {
				*value = jsontext.Value(nil)
			}
			return errors.ErrUnsupported
		}),
	)) != nil {
		return nil, catalog.E(catalog.CodeInvalidArgument, "ARD inline native data is not a JSON object")
	}
	if document == nil {
		for _, field := range selected {
			projection[field] = nativeField{present: true}
		}
		return projection, nil
	}
	for _, field := range selected {
		value, err := nativePathValue(ctx, document, field)
		if err != nil {
			return nil, err
		}
		projection[field] = value
	}
	return projection, ctx.Err()
}

func nativePathValue(ctx context.Context, document map[string]any, field string) (nativeField, error) {
	path := strings.Split(field, ".")
	parent, present := document[path[1]]
	if !present {
		return nativeField{}, nil
	}
	if parent == nil {
		return nativeField{present: true}, nil
	}
	// These are card defaults, not the effective modes of each skill.
	if field == "data.defaultInputModes" || field == "data.defaultOutputModes" {
		values, err := nativeStringArray(ctx, parent)
		if err != nil {
			return nativeField{}, err
		}
		slices.Sort(values)
		return nativeField{present: true, values: slices.Compact(values)}, nil
	}
	var objects []any
	if field == "data.capabilities.streaming" {
		objects = []any{parent}
	} else {
		var ok bool
		objects, ok = parent.([]any)
		if !ok {
			return nativeField{}, invalidNativeShape()
		}
	}
	result := nativeField{present: len(objects) == 0}
	for _, raw := range objects {
		if err := ctx.Err(); err != nil {
			return nativeField{}, err
		}
		object, ok := raw.(map[string]any)
		if !ok {
			return nativeField{}, invalidNativeShape()
		}
		value, exists := object[path[2]]
		if !exists {
			continue
		}
		result.present = true
		if value == nil {
			continue
		}
		switch field {
		case "data.remotes.supportedProtocolVersions", "data.skills.tags":
			values, err := nativeStringArray(ctx, value)
			if err != nil {
				return nativeField{}, err
			}
			result.values = append(result.values, values...)
		case "data.capabilities.streaming":
			boolean, ok := value.(bool)
			if !ok {
				return nativeField{}, invalidNativeShape()
			}
			result.values = append(result.values, strconv.FormatBool(boolean))
		default:
			text, ok := value.(string)
			if !ok {
				return nativeField{}, invalidNativeShape()
			}
			result.values = append(result.values, text)
		}
	}
	slices.Sort(result.values)
	result.values = slices.Compact(result.values)
	return result, nil
}

func nativeStringArray(ctx context.Context, value any) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, invalidNativeShape()
	}
	var values []string
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text, ok := item.(string)
		if !ok {
			return nil, invalidNativeShape()
		}
		values = append(values, text)
	}
	return values, nil
}

func invalidNativeShape() error {
	return catalog.E(catalog.CodeInvalidArgument, "ARD selected native field has an invalid value shape")
}
