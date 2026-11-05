// Package id generates time-ordered UUIDv7 identifiers.
// Callers supply the timestamp and may inject entropy for deterministic tests.
// Generation does not enforce ordering among identifiers in one millisecond.
package id

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"time"
)

// Generator produces UUIDv7 identifiers without a process-global random source.
type Generator struct {
	Reader io.Reader
}

// New combines now's millisecond timestamp with entropy from Reader, or
// crypto/rand.Reader when Reader is nil. It returns no identifier if entropy
// cannot be read or the timestamp does not fit UUIDv7's unsigned 48-bit field.
func (g Generator) New(now time.Time) (string, error) {
	r := g.Reader
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, 16)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("read UUID entropy: %w", err)
	}
	milliseconds := now.UTC().UnixMilli()
	if milliseconds < 0 || milliseconds > 1<<48-1 {
		return "", fmt.Errorf("UUIDv7 timestamp is outside the supported range")
	}
	ms := uint64(milliseconds)
	var timestamp [8]byte
	binary.BigEndian.PutUint64(timestamp[:], ms)
	copy(b[:6], timestamp[2:])
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return format(b), nil
}

// format encodes the generated 16 bytes in lowercase UUID notation.
func format(b []byte) string {
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], b[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], b[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], b[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], b[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], b[10:16])
	return string(encoded)
}
