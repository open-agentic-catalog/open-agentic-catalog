package jsondoc

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
)

// Decode reads a service-owned control value. Struct member names are exact,
// unknown members and explicit nulls in typed fields are rejected. Omission
// preserves defaults. RawMessage and interface values are explicit arbitrary
// JSON boundaries: their object names and nulls retain their original meaning.
// Callers own byte limits and discard the destination after an error. Custom
// UnmarshalJSON methods must enforce these rules within their own control types.
func Decode(document []byte, target any) error {
	return decode(document, target, controlValues)
}

// DecodeNumbers preserves JSON number lexemes at arbitrary interface boundaries.
// Use it explicitly for listing extensions; typed control admission is unchanged.
func DecodeNumbers(document []byte, target any) error {
	return decode(document, target, numberValues)
}

func decode(document []byte, target any, values *jsonv2.Unmarshalers) error {
	if err := Validate(document); err != nil {
		return err
	}
	// Preserve existing number, byte-slice, duration and overlay semantics while
	// changing only the reviewed control admission rules.
	return jsonv2.Unmarshal(document, target, json.DefaultOptionsV1(),
		jsonv2.MatchCaseInsensitiveNames(false), jsonv2.RejectUnknownMembers(true),
		jsontext.AllowInvalidUTF8(false), jsontext.AllowDuplicateNames(false),
		jsonv2.WithUnmarshalers(values))
}

var controlValues = controlDecoder(false)
var numberValues = controlDecoder(true)

func controlDecoder(numbers bool) *jsonv2.Unmarshalers {
	return jsonv2.UnmarshalFromFunc(func(decoder *jsontext.Decoder, target any) error {
		switch target.(type) {
		case *json.RawMessage, *any:
			value, err := decoder.ReadValue()
			if err != nil {
				return err
			}
			if numbers {
				reader := json.NewDecoder(bytes.NewReader(value))
				reader.UseNumber()
				return reader.Decode(target)
			}
			return json.Unmarshal(value, target)
		}
		if decoder.PeekKind() == 'n' {
			return errors.New("null is not supported for a control field")
		}
		return errors.ErrUnsupported
	})
}
