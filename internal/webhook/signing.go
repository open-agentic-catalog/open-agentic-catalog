// Webhook signing preserves the exact transmitted bytes and supports one
// bounded predecessor secret during routine rotation.

package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// SigningOverlap bounds routine dual-signature rotation; emergency rotation
// discards the predecessor instead of providing this grace period.
const SigningOverlap = 15 * time.Minute

// UnmarshalJSON distinguishes an absent flag from JSON null; encoding/json's
// default bool decoding would silently treat null as routine rotation.
func (input *RotateInput) UnmarshalJSON(raw []byte) error {
	if err := jsondoc.Validate(raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errors.New("webhook rotation requires a JSON object")
	}
	input.RevokePrevious = false
	for name, value := range fields {
		value = bytes.TrimSpace(value)
		if name != "revokePrevious" || !bytes.Equal(value, []byte("true")) && !bytes.Equal(value, []byte("false")) {
			return errors.New("webhook rotation accepts only a boolean revokePrevious field")
		}
		input.RevokePrevious = bytes.Equal(value, []byte("true"))
	}
	return nil
}

// signingMaterial is stored only inside the authenticated encryption envelope.
// Exactly one predecessor is retained; it is never returned through the API.
type signingMaterial struct {
	Version       int        `json:"version"`
	Current       string     `json:"current"`
	Previous      string     `json:"previous,omitempty"`
	PreviousUntil *time.Time `json:"previousUntil,omitempty"`
}

// readSigningMaterial authenticates and bounds retained material, accepting a
// valid bare secret or the closed versioned rotation envelope.
func readSigningMaterial(cipher Cipher, encrypted []byte) (signingMaterial, error) {
	if len(encrypted) > 4096 {
		return signingMaterial{}, errors.New("webhook signing material exceeds its bound")
	}
	raw, err := cipher.Decrypt(encrypted)
	if err != nil {
		return signingMaterial{}, err
	}
	if validSigningSecret(string(raw)) {
		return signingMaterial{Version: 1, Current: string(raw)}, nil
	}
	if len(raw) > 2048 || jsondoc.Validate(raw) != nil {
		return signingMaterial{}, errors.New("webhook signing material is invalid")
	}
	var material signingMaterial
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&material); err != nil || material.Version != 1 ||
		!validSigningSecret(material.Current) || (material.Previous == "") != (material.PreviousUntil == nil) ||
		material.Previous != "" && (!validSigningSecret(material.Previous) || material.Previous == material.Current || material.PreviousUntil.IsZero()) {
		return signingMaterial{}, errors.New("webhook signing material is invalid")
	}
	return material, nil
}

// validSigningSecret reports whether the value has the exact whsec_ prefix,
// length and URL-safe alphabet.
func validSigningSecret(value string) bool {
	if len(value) != 49 || !strings.HasPrefix(value, "whsec_") {
		return false
	}
	for _, character := range value[6:] {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

// signingHeader binds the timestamp spelling and exact transmitted body bytes.
// The whsec_ string itself is the HMAC key; decoding or re-encoding either the
// secret or JSON body would produce a different receiver verification contract.
func signingHeader(secret string, version int, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%s.", timestamp)
	_, _ = mac.Write(body)
	return fmt.Sprintf("v1,kid=%d,sha256=%s", version, hex.EncodeToString(mac.Sum(nil)))
}
