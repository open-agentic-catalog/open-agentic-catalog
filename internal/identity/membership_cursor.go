// Membership continuation tokens bind stable positions and expiry to the
// caller's effective authorization and selected page limit.

package identity

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	membershipCursorTTL      = 15 * time.Minute
	maxMembershipCursorBytes = 2048
)

type membershipCursor struct {
	Version   int    `json:"v"`
	Subject   string `json:"s"`
	ID        string `json:"i"`
	ExpiresAt int64  `json:"e"`
}

// encodeMembershipCursor signs the next stable membership position with expiry,
// authorization digest and page limit.
func (s *Service) encodeMembershipCursor(subject, idValue, authorizationDigest string, limit int) (string, error) {
	// Base64 transport does not need HTML escaping, which can inflate a valid
	// 512-byte subject beyond the existing cursor limit.
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(membershipCursor{Version: 1, Subject: subject, ID: idValue, ExpiresAt: s.clock.Now().Add(membershipCursorTTL).Unix()}); err != nil {
		return "", Wrap(CodeInternal, "encode membership cursor", err)
	}
	payload := bytes.TrimSuffix(encoded.Bytes(), []byte("\n"))
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte("membership-cursor-v1\x00"))
	_, _ = mac.Write(payload)
	_, _ = mac.Write([]byte("\x00" + authorizationDigest + "\x00" + strconv.Itoa(limit)))
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil))
	if len(token) > maxMembershipCursorBytes {
		return "", E(CodeInternal, "membership cursor exceeds its safe bound")
	}
	return token, nil
}

// decodeMembershipCursor verifies canonical encoding, signature, shape and
// expiry; an empty token means the first page.
func (s *Service) decodeMembershipCursor(token, authorizationDigest string, limit int) (membershipCursor, bool, error) {
	if token == "" {
		return membershipCursor{}, false, nil
	}
	if len(token) > maxMembershipCursorBytes {
		return membershipCursor{}, false, E(CodeInvalidArgument, "membership cursor is invalid")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return membershipCursor{}, false, E(CodeInvalidArgument, "membership cursor is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[0] {
		return membershipCursor{}, false, E(CodeInvalidArgument, "membership cursor is invalid")
	}
	provided, err := hex.DecodeString(parts[1])
	if err != nil || hex.EncodeToString(provided) != parts[1] {
		return membershipCursor{}, false, E(CodeInvalidArgument, "membership cursor is invalid")
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte("membership-cursor-v1\x00"))
	_, _ = mac.Write(payload)
	_, _ = mac.Write([]byte("\x00" + authorizationDigest + "\x00" + strconv.Itoa(limit)))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return membershipCursor{}, false, E(CodeInvalidArgument, "membership cursor is invalid for this request")
	}
	var cursor membershipCursor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != 1 || !ValidSubject(cursor.Subject) || !ValidTenantID(cursor.ID) {
		return membershipCursor{}, false, E(CodeInvalidArgument, "membership cursor is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return membershipCursor{}, false, E(CodeInvalidArgument, "membership cursor is invalid")
	}
	if !time.Unix(cursor.ExpiresAt, 0).After(s.clock.Now()) {
		return membershipCursor{}, false, E(CodeInvalidArgument, "membership cursor has expired")
	}
	return cursor, true, nil
}
