// Package jsondoc provides bounded structural checks for JSON documents that
// cross a trust boundary. The standard library intentionally accepts duplicate
// object names, but doing so makes signed, reviewed, and interpreted content
// ambiguous because different consumers may select different values.
package jsondoc

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
)

const maxDepth = 128

var (
	// ErrDuplicateMember identifies an ambiguous repeated object name.
	ErrDuplicateMember = errors.New("JSON document contains a duplicate object member")
	// ErrTrailingValue identifies bytes after the first complete JSON value.
	ErrTrailingValue = errors.New("JSON document contains a trailing value")
)

// Validate rejects malformed JSON, trailing values, duplicate object member
// names, malformed Unicode, and documents nested deeply enough to risk disproportionate parser
// work. It does not impose a byte limit; callers own that transport budget.
func Validate(document []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := validateValue(decoder, 0); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("%w: %w", ErrTrailingValue, err)
		}
		return fmt.Errorf("%w beginning with %v", ErrTrailingValue, token)
	}
	// The legacy token decoder replaces malformed UTF-8 and lone surrogate
	// escapes. Validate the original bytes before callers can lose that evidence.
	if !jsontext.Value(document).IsValid() {
		return errors.New("JSON document contains invalid Unicode")
	}
	return nil
}

// validateValue recursively consumes one JSON value, rejecting duplicate keys
// and unbalanced delimiters under the nesting ceiling.
func validateValue(decoder *json.Decoder, depth int) error {
	if depth > maxDepth {
		return errors.New("JSON document exceeds the supported nesting depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read JSON value: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("read JSON object member: %w", err)
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object member name is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return ErrDuplicateMember
			}
			seen[name] = struct{}{}
			if err := validateValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := validateValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("JSON document contains an unexpected delimiter")
	}
	return nil
}
