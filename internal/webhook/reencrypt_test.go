package webhook

import (
	"bytes"
	"testing"
)

func TestReencryptSecretPreservesMaterialAndResumes(t *testing.T) {
	oldKey, newKey := keyBytes(4), keyBytes(9)
	oldCipher, _ := NewAESGCM(oldKey)
	newCipher, _ := NewAESGCM(newKey)
	for _, raw := range []string{
		"whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		`{"version":1,"current":"whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","previous":"whsec_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","previousUntil":"2026-09-06T12:00:00Z"}`,
	} {
		original, _ := oldCipher.Encrypt([]byte(raw))
		replaced, changed, err := ReencryptSecret(original, oldKey, newKey)
		if err != nil || !changed || bytes.Equal(original, replaced) {
			t.Fatalf("replacement: changed=%v err=%v", changed, err)
		}
		plain, err := newCipher.Decrypt(replaced)
		if err != nil || string(plain) != raw {
			t.Fatal("replacement changed retained signing material")
		}
		if _, err := oldCipher.Decrypt(replaced); err == nil {
			t.Fatal("old key still decrypts replacement")
		}
		again, changed, err := ReencryptSecret(replaced, oldKey, newKey)
		if err != nil || changed || !bytes.Equal(again, replaced) {
			t.Fatal("resume rewrote an already verified replacement")
		}
		if _, _, err := ReencryptSecret(original, keyBytes(3), newKey); err == nil {
			t.Fatal("wrong source key accepted")
		}
	}
	for _, pair := range [][2][]byte{{oldKey, oldKey}, {nil, newKey}, {oldKey, nil}} {
		if _, _, err := ReencryptSecret(nil, pair[0], pair[1]); err == nil {
			t.Fatal("invalid replacement keys accepted")
		}
	}
	for _, cipher := range []*AESGCM{oldCipher, newCipher} {
		invalid, _ := cipher.Encrypt([]byte(`{"unknown":true}`))
		if _, _, err := ReencryptSecret(invalid, oldKey, newKey); err == nil {
			t.Fatal("invalid plaintext accepted under an otherwise valid key")
		}
	}
}
