package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func TestSignedMembershipCursorPayloadIsStrict(t *testing.T) {
	t.Parallel()
	service := &Service{clock: identityTestClock{at: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}, cursorKey: identityTestCursorKey}
	const memberID = "00000000-0000-7000-8000-000000000001"
	valid := fmt.Sprintf(`{"v":1,"s":"reader","i":%q,"e":%d}`, memberID, service.clock.Now().Add(time.Minute).Unix())
	for _, tc := range []struct{ name, payload string }{
		{"valid", valid},
		{"malformed", `{`},
		{"unknown field", valid[:len(valid)-1] + `,"extra":true}`},
		{"wrong version", fmt.Sprintf(`{"v":2,"s":"reader","i":%q,"e":%d}`, memberID, service.clock.Now().Add(time.Minute).Unix())},
		{"invalid position", fmt.Sprintf(`{"v":1,"s":" reader","i":%q,"e":%d}`, memberID, service.clock.Now().Add(time.Minute).Unix())},
		{"trailing object", valid + `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Sign independently so malformed payloads reach decoding instead of
			// being rejected earlier by signature validation.
			mac := hmac.New(sha256.New, identityTestCursorKey)
			_, _ = mac.Write([]byte("membership-cursor-v1\x00" + tc.payload + "\x00authorization\x0010"))
			token := base64.RawURLEncoding.EncodeToString([]byte(tc.payload)) + "." + hex.EncodeToString(mac.Sum(nil))
			got, present, err := service.decodeMembershipCursor(token, "authorization", 10)
			if tc.name == "valid" {
				if err != nil || !present || got.Subject != "reader" || got.ID != memberID {
					t.Fatalf("valid signed position=%+v present=%t err=%v", got, present, err)
				}
			} else if ErrorCodeOf(err) != CodeInvalidArgument || present || got != (membershipCursor{}) {
				t.Fatalf("invalid signed position=%+v present=%t err=%v", got, present, err)
			}
		})
	}
}
