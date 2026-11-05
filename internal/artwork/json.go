package artwork

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

func checkEncodedSize(value any, l Limits) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > l.MaxDescriptorBytes {
		return fmt.Errorf("%w: descriptor size", ErrInvalid)
	}
	return nil
}

// closedObject rejects nulls, wrong casing and unknown fields in addition to
// the duplicate/Unicode checks performed once at the document boundary.
func closedObject(raw []byte, keys ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: expected object", ErrInvalid)
	}
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	for key, value := range object {
		if !allowed[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("%w: unknown or null member", ErrInvalid)
		}
	}
	return object, nil
}

func validateAssetJSON(raw []byte, sealed bool) error {
	keys := []string{"url", "alt", "caption", "rights"}
	if sealed {
		keys = append(keys, "digest", "mediaType", "size", "width", "height", "rightsAssessment")
	}
	obj, err := closedObject(raw, keys...)
	if err != nil {
		return err
	}
	for _, key := range []string{"url", "alt", "rights"} {
		if _, ok := obj[key]; !ok {
			return fmt.Errorf("%w: missing asset member", ErrInvalid)
		}
	}
	if sealed {
		for _, key := range []string{"digest", "mediaType", "size", "width", "height", "rightsAssessment"} {
			if _, ok := obj[key]; !ok {
				return fmt.Errorf("%w: missing image facts", ErrInvalid)
			}
		}
	}
	rights, err := closedObject(obj["rights"], "basis", "attribution", "reference")
	if err != nil {
		return err
	}
	for _, key := range []string{"basis", "attribution"} {
		if _, ok := rights[key]; !ok {
			return fmt.Errorf("%w: missing rights member", ErrInvalid)
		}
	}
	return nil
}

func validateDescriptorJSON(raw []byte, l Limits, sealed bool) error {
	if err := l.Validate(); err != nil {
		return err
	}
	if len(raw) > l.MaxDescriptorBytes {
		return fmt.Errorf("%w: descriptor size", ErrInvalid)
	}
	if err := jsondoc.Validate(raw); err != nil {
		return fmt.Errorf("%w: ambiguous descriptor JSON", ErrInvalid)
	}
	obj, err := closedObject(raw, "schemaVersion", "icon", "screenshots")
	if err != nil {
		return err
	}
	if _, ok := obj["schemaVersion"]; !ok {
		return fmt.Errorf("%w: missing descriptor version", ErrInvalid)
	}
	if icon, ok := obj["icon"]; ok {
		if err := validateAssetJSON(icon, sealed); err != nil {
			return err
		}
	}
	if screenshots, ok := obj["screenshots"]; ok {
		var list []json.RawMessage
		if err := json.Unmarshal(screenshots, &list); err != nil {
			return fmt.Errorf("%w: screenshot array", ErrInvalid)
		}
		if len(list) > l.MaxScreenshots {
			return fmt.Errorf("%w: screenshot count", ErrInvalid)
		}
		for _, asset := range list {
			if err := validateAssetJSON(asset, sealed); err != nil {
				return err
			}
		}
	}
	return nil
}

func DecodeInput(raw []byte, l Limits) (Input, error) {
	if err := validateDescriptorJSON(raw, l, false); err != nil {
		return Input{}, err
	}
	var input Input
	if err := json.Unmarshal(raw, &input); err != nil {
		return Input{}, fmt.Errorf("%w: descriptor types", ErrInvalid)
	}
	if err := ValidateInput(input, l); err != nil {
		return Input{}, err
	}
	return input, nil
}

func DecodeDescriptor(raw []byte, l Limits) (Descriptor, error) {
	if err := validateDescriptorJSON(raw, l, true); err != nil {
		return Descriptor{}, err
	}
	var descriptor Descriptor
	if err := json.Unmarshal(raw, &descriptor); err != nil {
		return Descriptor{}, fmt.Errorf("%w: descriptor types", ErrInvalid)
	}
	if err := ValidateDescriptor(descriptor, l); err != nil {
		return Descriptor{}, err
	}
	return descriptor, nil
}

// Extract recognizes only the exact full IRI after JSON-LD normalization. A
// missing or future-version extension is ignored; a malformed recognized term
// is an optional presentation error, never authority to fetch another URL.
// The caller bounds the entire ARD entry and omits presentation on error.
func Extract(entry []byte, l Limits) (*Descriptor, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	if err := jsondoc.Validate(entry); err != nil {
		return nil, fmt.Errorf("%w: ambiguous entry JSON", ErrInvalid)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(entry, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: expected entry", ErrInvalid)
	}
	raw, exists := object[IRI]
	if !exists {
		return nil, nil //nolint:nilnil // An absent or unsupported optional presentation term is not a document error.
	}
	if len(raw) > l.MaxDescriptorBytes+128 {
		return nil, fmt.Errorf("%w: extension size", ErrInvalid)
	}
	// Expanded processors may retain the JSON literal as a singleton array.
	if len(raw) > 0 && raw[0] == '[' {
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil || len(values) != 1 {
			return nil, fmt.Errorf("%w: ambiguous JSON literal", ErrInvalid)
		}
		raw = values[0]
	}
	wrapper, err := closedObject(raw, "@type", "@value")
	if err != nil {
		return nil, err
	}
	var datatype string
	if err := json.Unmarshal(wrapper["@type"], &datatype); err != nil || datatype != "@json" {
		return nil, fmt.Errorf("%w: expected JSON literal", ErrInvalid)
	}
	descriptorRaw, exists := wrapper["@value"]
	if !exists {
		return nil, fmt.Errorf("%w: missing JSON literal", ErrInvalid)
	}
	return decodeOptionalDescriptor(descriptorRaw, l)
}

// ExtractAI recognizes the same sealed descriptor in the predecessor AI
// Catalog's explicit extension map. It never interprets that map as ARD metadata.
func ExtractAI(entry []byte, l Limits) (*Descriptor, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	if err := jsondoc.Validate(entry); err != nil {
		return nil, fmt.Errorf("%w: ambiguous entry JSON", ErrInvalid)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(entry, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: expected entry", ErrInvalid)
	}
	raw, exists := object["extensions"]
	if !exists {
		return nil, nil //nolint:nilnil // An absent or unsupported optional presentation term is not a document error.
	}
	var extensions map[string]json.RawMessage
	if err := json.Unmarshal(raw, &extensions); err != nil || extensions == nil {
		return nil, fmt.Errorf("%w: expected extension map", ErrInvalid)
	}
	descriptor, exists := extensions[IRI]
	if !exists {
		return nil, nil //nolint:nilnil // An absent or unsupported optional presentation term is not a document error.
	}
	return decodeOptionalDescriptor(descriptor, l)
}

func decodeOptionalDescriptor(raw []byte, l Limits) (*Descriptor, error) {
	if len(raw) > l.MaxDescriptorBytes {
		return nil, fmt.Errorf("%w: descriptor size", ErrInvalid)
	}
	var header struct {
		SchemaVersion json.RawMessage `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, fmt.Errorf("%w: descriptor object", ErrInvalid)
	}
	var version int
	if len(header.SchemaVersion) == 0 || bytes.Equal(bytes.TrimSpace(header.SchemaVersion), []byte("null")) || json.Unmarshal(header.SchemaVersion, &version) != nil || version <= 0 {
		return nil, fmt.Errorf("%w: descriptor version", ErrInvalid)
	}
	if version != SchemaVersion {
		return nil, nil //nolint:nilnil // An absent or unsupported optional presentation term is not a document error.
	}
	d, err := DecodeDescriptor(raw, l)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

const (
	IconSourceListing       = "listing"
	IconSourceLocalOverride = "local_override"
	IconSourcePublisher     = "publisher"
	IconSourceNative        = "native"
)

// Presentation identifies the selected authority without conferring its
// permissions. MutableIcon marks native bytes as current display hints.
type Presentation struct {
	Descriptor        *Descriptor `json:"descriptor,omitempty"`
	MutableIcon       bool        `json:"mutableIcon,omitempty"`
	IconSource        string      `json:"iconSource,omitempty"`
	ScreenshotsSource string      `json:"screenshotsSource,omitempty"`
	Receipt           string      `json:"receipt,omitempty"`
}

func ValidIconSource(source string) bool {
	return source == IconSourceListing || source == IconSourceLocalOverride || source == IconSourcePublisher || source == IconSourceNative
}
func ValidScreenshotsSource(source string) bool {
	return source == IconSourceListing || source == IconSourceLocalOverride || source == IconSourcePublisher
}
