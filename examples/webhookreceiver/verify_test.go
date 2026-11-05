// Receiver tests exercise signing bytes, bounded headers and freshness without
// requiring an HTTP server or downstream persistence.
package webhookreceiver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestVerifyCurrentPreviousAndAdversarialRequests covers ordinary verification,
// predecessor overlap and malformed request rejection.
func TestVerifyCurrentPreviousAndAdversarialRequests(t *testing.T) {
	secret := "whsec_" + strings.Repeat("a", 43)
	body := []byte(`{"specversion":"1.0","id":"event-id"}`)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	sign := func(at time.Time) http.Header {
		return signedRequest(secret, 1, strconv.FormatInt(at.Unix(), 10), body)
	}
	for _, at := range []time.Time{now, now.Add(-5 * time.Minute), now.Add(30 * time.Second)} {
		if err := Verify(secret, 1, sign(at), body, now); err != nil {
			t.Fatal(err)
		}
	}
	header := sign(now)
	header.Set("X-Catalog-Previous-Signature", header.Get("X-Catalog-Signature"))
	header.Set("X-Catalog-Signature", "v1,kid=2,sha256=other-key")
	if err := Verify(secret, 1, header, body, now); err != nil {
		t.Fatal("overlap did not verify")
	}
	for name, mutate := range map[string]func(http.Header){
		"missing timestamp":   func(h http.Header) { h.Del("X-Catalog-Timestamp") },
		"repeated timestamp":  func(h http.Header) { h.Add("X-Catalog-Timestamp", h.Get("X-Catalog-Timestamp")) },
		"overflow":            func(h http.Header) { h.Set("X-Catalog-Timestamp", "99999999999999999999999999999") },
		"maximum int":         func(h http.Header) { h.Set("X-Catalog-Timestamp", "9223372036854775807") },
		"minimum int":         func(h http.Header) { h.Set("X-Catalog-Timestamp", "-9223372036854775808") },
		"noncanonical":        func(h http.Header) { h.Set("X-Catalog-Timestamp", "+"+h.Get("X-Catalog-Timestamp")) },
		"missing signature":   func(h http.Header) { h.Del("X-Catalog-Signature") },
		"repeated signature":  func(h http.Header) { h.Add("X-Catalog-Signature", h.Get("X-Catalog-Signature")) },
		"oversized signature": func(h http.Header) { h.Set("X-Catalog-Signature", strings.Repeat("x", 129)) },
		"repeated predecessor": func(h http.Header) {
			h.Add("X-Catalog-Previous-Signature", h.Get("X-Catalog-Signature"))
			h.Add("X-Catalog-Previous-Signature", h.Get("X-Catalog-Signature"))
		},
		"oversized predecessor": func(h http.Header) { h.Set("X-Catalog-Previous-Signature", strings.Repeat("x", 129)) },
		"substituted kid": func(h http.Header) {
			h.Set("X-Catalog-Signature", strings.Replace(h.Get("X-Catalog-Signature"), "kid=1", "kid=2", 1))
		},
		"unknown algorithm": func(h http.Header) {
			h.Set("X-Catalog-Signature", strings.Replace(h.Get("X-Catalog-Signature"), "sha256", "sha1", 1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := sign(now)
			mutate(h)
			if Verify(secret, 1, h, body, now) == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	for _, at := range []time.Time{now.Add(-5*time.Minute - time.Second), now.Add(31 * time.Second)} {
		if Verify(secret, 1, sign(at), body, now) == nil {
			t.Fatal("expired or future signature accepted")
		}
	}
	for _, value := range [][]byte{nil, []byte("tampered"), make([]byte, MaxBodyBytes+1)} {
		if Verify(secret, 1, sign(now), value, now) == nil {
			t.Fatal("invalid body accepted")
		}
	}
	if Verify("bad", 1, sign(now), body, now) == nil || Verify(secret, 0, sign(now), body, now) == nil {
		t.Fatal("invalid receiver configuration accepted")
	}
}

// TestVerifyRejectsMalformedConfiguredSecrets signs with each invalid value so
// rejection proves format validation rather than a coincidental MAC mismatch.
func TestVerifyRejectsMalformedConfiguredSecrets(t *testing.T) {
	now := time.Unix(1800000000, 0)
	body := []byte(`{"id":"example"}`)
	for name, secret := range map[string]string{
		"wrong prefix": "other_" + strings.Repeat("a", 43),
		"punctuation":  "whsec_" + strings.Repeat("!", 43),
		"whitespace":   "whsec_" + strings.Repeat("a", 42) + " ",
		"newline":      "whsec_" + strings.Repeat("a", 42) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			header := signedRequest(secret, 1, strconv.FormatInt(now.Unix(), 10), body)
			if Verify(secret, 1, header, body, now) == nil {
				t.Fatal("malformed configured secret accepted despite matching signature")
			}
		})
	}
}

// TestVerifyExactBodyLimitAndKey checks the accepted size ceiling and the
// URL-safe secret alphabet without accepting reserialized bytes or another key.
func TestVerifyExactBodyLimitAndKey(t *testing.T) {
	now := time.Unix(1800000000, 0)
	secret := "whsec_" + strings.Repeat("Ab0-_", 8) + "xyz"
	body := []byte(strings.Repeat(" ", MaxBodyBytes-2) + "{}")
	header := signedRequest(secret, 17, strconv.FormatInt(now.Unix(), 10), body)
	if err := Verify(secret, 17, header, body, now); err != nil {
		t.Fatalf("exact size limit and URL-safe key rejected: %v", err)
	}
	if Verify(secret, 17, header, []byte("{}"), now) == nil {
		t.Fatal("reserialized JSON accepted under the original signature")
	}
	if Verify("whsec_"+strings.Repeat("b", 43), 17, header, body, now) == nil {
		t.Fatal("different well-formed key accepted")
	}
}

// signedRequest signs exact bytes with an explicit test key and timestamp.
func signedRequest(secret string, version int, timestamp string, body []byte) http.Header {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	header := make(http.Header)
	header.Set("X-Catalog-Timestamp", timestamp)
	header.Set("X-Catalog-Signature", "v1,kid="+strconv.Itoa(version)+",sha256="+hex.EncodeToString(mac.Sum(nil)))
	return header
}
