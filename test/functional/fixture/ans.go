package main

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"slices"
)

// ansPublicKey exports only a selected native RA or TL public key to its owned supervisor.
func ansPublicKey(name string, output io.Writer) error {
	if !slices.Contains([]string{"ans-ra-signer", "ans-tl-attestation", "ans-ra-signer-next", "ans-tl-attestation-next"}, name) {
		return errors.New("unselected ANS public key")
	}
	raw, err := os.ReadFile("/ans-state/keys/" + name + ".pub") // #nosec G304,G703 -- exact four-name public-key allowlist above.
	if err != nil {
		return err
	}
	return writeANSPublicKey(raw, output)
}

func writeANSPublicKey(raw []byte, output io.Writer) error {
	if len(raw) > 4096 {
		return errors.New("oversized ANS public key")
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 {
		return errors.New("invalid ANS public key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return errors.New("invalid ANS public key")
	}
	if _, ok := key.(*ecdsa.PublicKey); !ok {
		return errors.New("unexpected ANS key algorithm")
	}
	_, err = output.Write(raw)
	return err
}
