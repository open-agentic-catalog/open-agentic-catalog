// Package webhookreceiver provides a copyable, standard-library-only Catalog
// webhook verification example. It does not implement durable deduplication or
// application processing; those belong to the receiving application.
package webhookreceiver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxBodyBytes is the receiver's accepted event size. Bound the HTTP body read
// before calling Verify; checking an already allocated body does not bound memory.
const MaxBodyBytes = 256 << 10

// Verify checks the exact raw body, one timestamp, and at most two signature
// headers against one receiver-configured secret version. Never select a key
// from an untrusted request URL or fetch a key indicated by the sender.
// A valid signature authenticates bytes, not business authorization or uniqueness:
// the receiver must validate the event and durably deduplicate it before processing.
func Verify(secret string, version int, headers http.Header, body []byte, now time.Time) error {
	invalid := errors.New("invalid webhook signature")
	if len(secret) != 49 || !strings.HasPrefix(secret, "whsec_") || version < 1 || len(body) == 0 || len(body) > MaxBodyBytes || len(headers.Values("X-Catalog-Timestamp")) != 1 {
		return invalid
	}
	// Validate the configured format without decoding it: the entire whsec_
	// string, including its prefix, is the sender's HMAC key.
	for _, character := range secret[6:] {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_') {
			return invalid
		}
	}
	timestamp := headers.Get("X-Catalog-Timestamp")
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || strconv.FormatInt(seconds, 10) != timestamp {
		return invalid
	}
	// Compare integer seconds before constructing a time to avoid overflow on
	// attacker-selected extreme Unix values. Five minutes past, 30 seconds ahead.
	if seconds < now.Add(-5*time.Minute).Unix() || seconds > now.Add(30*time.Second).Unix() {
		return invalid
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	want := "v1,kid=" + strconv.Itoa(version) + ",sha256=" + hex.EncodeToString(mac.Sum(nil))
	verified := false
	for _, name := range []string{"X-Catalog-Signature", "X-Catalog-Previous-Signature"} {
		values := headers.Values(name)
		if len(values) > 1 || len(headers.Get(name)) > 128 {
			return invalid
		}
		verified = hmac.Equal([]byte(headers.Get(name)), []byte(want)) || verified
	}
	if !verified {
		return invalid
	}
	return nil
}
