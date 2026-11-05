package main

import (
	"encoding/hex"
	"errors"
	"strings"
)

// publicProfile binds both delivery phases to the independently checked fixture.
// Ordinary public and scale scenarios retain their original four-entry contract.
type publicProfile struct {
	Entries     int    `json:"entries"`
	Bytes       int    `json:"bytes"`
	ETag        string `json:"etag"`
	Digest      string `json:"digest"`
	Conditional bool   `json:"conditional"`
}

func (p publicProfile) validate() error {
	_, err := hex.DecodeString(strings.TrimPrefix(p.Digest, "sha256:"))
	if p.Entries != 1001 || p.Bytes < 1 || p.Bytes > 16<<20 || len(p.Digest) != 71 || !strings.HasPrefix(p.Digest, "sha256:") || err != nil || len(p.ETag) < 3 || len(p.ETag) > 128 || p.ETag[0] != '"' || p.ETag[len(p.ETag)-1] != '"' {
		return errors.New("invalid-public-profile")
	}
	for _, c := range p.ETag[1 : len(p.ETag)-1] {
		if c < 0x21 || c > 0x7e || c == '"' {
			return errors.New("invalid-public-etag")
		}
	}
	return nil
}
