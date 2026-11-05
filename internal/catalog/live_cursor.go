package catalog

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// LiveSearchWindow is a finite protocol bound, not an upstream inventory size.
const LiveSearchWindow = 50

const LiveCursorPrefix = "lc1."

// LiveCursor contains positions and opaque evidence only. It never contains a
// nested cursor, a peer token, an endpoint, a query or returned documents.
type LiveCursor struct {
	Mode              string                  `json:"m,omitempty"`
	HybridPosition    *SemanticSearchPosition `json:"h,omitempty"`
	Ranking           string                  `json:"r,omitempty"`
	VectorFingerprint string                  `json:"q,omitempty"`
	Version           int                     `json:"v"`
	Surface           string                  `json:"k"`
	Generation        string                  `json:"g"`
	IndexedGeneration string                  `json:"x,omitempty"`
	Position          *CatalogSearchPosition  `json:"p,omitempty"`
	Offset            int                     `json:"o,omitempty"`
	LiveOffset        int                     `json:"l"`
	Fingerprint       string                  `json:"f"`
	ExpiresAt         int64                   `json:"e"`
}

// EncodeLiveCursor signs a flat envelope with the original fixed expiry.
func EncodeLiveCursor(cursor LiveCursor, key []byte, binding string) (string, error) {
	cursor.Version = 1
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", Wrap(CodeInternal, "encode live search cursor", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("live-client-cursor-v1\x00" + binding + "\x00"))
	_, _ = mac.Write(payload)
	token := LiveCursorPrefix + base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil))
	if len(token) > MaxCatalogCursorBytes {
		return "", E(CodeInternal, "live search cursor exceeds its bound")
	}
	return token, nil
}

// DecodeLiveCursor authenticates and validates before any replay work.
func DecodeLiveCursor(token, surface string, key []byte, binding string, now time.Time) (LiveCursor, error) {
	invalid := func() (LiveCursor, error) {
		return LiveCursor{}, E(CodeInvalidArgument, "live search cursor is invalid or expired; restart the search")
	}
	if len(token) > MaxCatalogCursorBytes || !strings.HasPrefix(token, LiveCursorPrefix) {
		return invalid()
	}
	parts := strings.Split(strings.TrimPrefix(token, LiveCursorPrefix), ".")
	if len(parts) != 2 {
		return invalid()
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[0] {
		return invalid()
	}
	provided, err := hex.DecodeString(parts[1])
	if err != nil || hex.EncodeToString(provided) != parts[1] {
		return invalid()
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("live-client-cursor-v1\x00" + binding + "\x00"))
	_, _ = mac.Write(payload)
	if !hmac.Equal(provided, mac.Sum(nil)) || jsondoc.Validate(payload) != nil {
		return invalid()
	}
	var cursor LiveCursor
	if jsonv2.Unmarshal(payload, &cursor, jsonv2.RejectUnknownMembers(true), jsonv2.MatchCaseInsensitiveNames(false)) != nil || cursor.Version != 1 || cursor.Surface != surface || cursor.Generation == "" || len(cursor.Generation) > 128 || len(cursor.IndexedGeneration) > 128 || cursor.Offset < 0 || cursor.Offset > MaxPublicationRecords || cursor.LiveOffset < 0 || cursor.LiveOffset > LiveSearchWindow || cursor.Fingerprint != "" && len(cursor.Fingerprint) != 64 || !time.Unix(cursor.ExpiresAt, 0).After(now) || time.Unix(cursor.ExpiresAt, 0).After(now.Add(catalogSearchCursorTTL)) {
		return invalid()
	}
	if decoded, err := hex.DecodeString(cursor.Fingerprint); err != nil || hex.EncodeToString(decoded) != cursor.Fingerprint || cursor.Fingerprint == "" && cursor.LiveOffset != 0 {
		return invalid()
	}
	if surface == "control" {
		if cursor.IndexedGeneration == "" || cursor.Offset != 0 {
			return invalid()
		}
		if p := cursor.Position; p != nil && (len(p.SortValue) > 2048 || p.Score < 0 || p.Score > 100 || p.DisplayName == "" || len(p.DisplayName) > 640 || p.ResultID == "" || len(p.ResultID) > 256) {
			return invalid()
		}
	} else if surface != "public" || cursor.Position != nil || cursor.IndexedGeneration != "" {
		return invalid()
	}
	switch cursor.Mode {
	case "":
		if cursor.HybridPosition != nil || cursor.Ranking != "" || cursor.VectorFingerprint != "" {
			return invalid()
		}
	case "live":
		if surface != "control" || cursor.Position != nil || cursor.HybridPosition != nil || cursor.Ranking != "" || cursor.VectorFingerprint != "" || cursor.Fingerprint == "" {
			return invalid()
		}
	case "hybrid":
		if cursor.Position != nil || cursor.Ranking == "" || len(cursor.Ranking) > 256 || cursor.Fingerprint == "" {
			return invalid()
		}
		if surface == "control" {
			if !validLiveDigest(cursor.VectorFingerprint) {
				return invalid()
			}
			if p := cursor.HybridPosition; p != nil && (p.Rank < 1 || p.Rank > 1e12 || p.ID == "" || len(p.ID) > 256 || len(p.SortName) > 640) {
				return invalid()
			}
		} else if cursor.HybridPosition != nil || cursor.VectorFingerprint != "" {
			return invalid()
		}
	default:
		return invalid()
	}
	return cursor, nil
}

func validLiveDigest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == value
}
