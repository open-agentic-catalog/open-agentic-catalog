// Hybrid cursors have a separate signed envelope from lexical continuation.
// Precise ranks and a vector fingerprint keep pagination stable across replicas.
package catalog

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

type semanticCursor struct {
	Generation        string                 `json:"g"`
	SourceGeneration  string                 `json:"s"`
	VectorFingerprint string                 `json:"f"`
	Position          SemanticSearchPosition `json:"p"`
	ExpiresAt         int64                  `json:"e"`
}

// encodeSemanticCursor signs only opaque identities, a precise position and expiry.
func (s *Service) encodeSemanticCursor(cursor semanticCursor, binding string) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", Wrap(CodeInternal, "encode hybrid cursor", err)
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte("catalog-hybrid-cursor-v1\x00" + binding + "\x00"))
	_, _ = mac.Write(payload)
	token := "h1." + base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil))
	if len(token) > MaxCatalogCursorBytes {
		return "", E(CodeInternal, "hybrid cursor exceeds its bound")
	}
	return token, nil
}

// decodeSemanticCursor authenticates before accepting a closed, bounded payload.
func (s *Service) decodeSemanticCursor(token, binding string) (semanticCursor, error) {
	invalid := func() (semanticCursor, error) {
		return semanticCursor{}, E(CodeInvalidArgument, "hybrid cursor is invalid or expired; restart the search")
	}
	if token == "" {
		return semanticCursor{}, nil
	}
	if len(token) > MaxCatalogCursorBytes {
		return invalid()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "h1" {
		return invalid()
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
		return invalid()
	}
	provided, err := hex.DecodeString(parts[2])
	if err != nil || hex.EncodeToString(provided) != parts[2] {
		return invalid()
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte("catalog-hybrid-cursor-v1\x00" + binding + "\x00"))
	_, _ = mac.Write(payload)
	if !hmac.Equal(provided, mac.Sum(nil)) || jsondoc.Validate(payload) != nil {
		return invalid()
	}
	var cursor semanticCursor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.Decode(&struct{}{}) != io.EOF || !identity.ValidTenantID(cursor.Generation) || cursor.SourceGeneration == "" || len(cursor.SourceGeneration) > 256 || len(cursor.VectorFingerprint) != 64 || cursor.Position.Rank < 1 || cursor.Position.Rank > 1e12 || len(cursor.Position.ID) == 0 || len(cursor.Position.ID) > 256 || len(cursor.Position.SortName) > 640 || !time.Unix(cursor.ExpiresAt, 0).After(s.clock.Now()) {
		return invalid()
	}
	return cursor, nil
}
