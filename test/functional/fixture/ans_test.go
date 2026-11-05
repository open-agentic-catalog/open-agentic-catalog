package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestANSKeyExportAcceptsOnlyBoundedPublicECKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	var output bytes.Buffer
	if err := writeANSPublicKey(public, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), public) {
		t.Fatal("native public key was altered")
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{nil, bytes.Repeat([]byte{'a'}, 4097), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("bad")}), append(append([]byte{}, public...), []byte("trailing")...)} {
		output.Reset()
		if writeANSPublicKey(raw, &output) == nil || output.Len() != 0 {
			t.Fatal("invalid/private material was exported")
		}
	}
}

func TestANSKeyExportRefusesUnselectedNamesBeforeReading(t *testing.T) {
	for _, name := range []string{"", "../ans-ra-signer", "ans-ra-signer.key", "ans-tl-attestation-next/other", "arbitrary"} {
		var output bytes.Buffer
		if err := ansPublicKey(name, &output); err == nil || err.Error() != "unselected ANS public key" || output.Len() != 0 {
			t.Fatal("unselected key path was read", name, err)
		}
	}
}
