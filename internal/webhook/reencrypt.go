// Operator secret rewrapping authenticates retained signing material and
// detects completed replacements so stopped-runtime maintenance can resume
// safely.

package webhook

import (
	"bytes"
	"errors"
)

// ReencryptSecret verifies retained signing material before rewrapping it. Trying
// the replacement key first makes a stopped-runtime maintenance batch resumable
// without introducing a runtime key ring or changing the ciphertext format.
func ReencryptSecret(ciphertext, oldKey, newKey []byte) ([]byte, bool, error) {
	if bytes.Equal(oldKey, newKey) {
		return nil, false, errors.New("webhook replacement keys must differ")
	}
	oldCipher, err := NewAESGCM(oldKey)
	if err != nil {
		return nil, false, err
	}
	newCipher, err := NewAESGCM(newKey)
	if err != nil {
		return nil, false, err
	}
	if _, err := readSigningMaterial(newCipher, ciphertext); err == nil {
		return bytes.Clone(ciphertext), false, nil
	}
	if _, err := readSigningMaterial(oldCipher, ciphertext); err != nil {
		return nil, false, errors.New("retained webhook secret is invalid or neither supplied key can decrypt it")
	}
	plaintext, err := oldCipher.Decrypt(ciphertext)
	if err != nil {
		return nil, false, err
	}
	defer clear(plaintext)
	replacement, err := newCipher.Encrypt(plaintext)
	if err != nil {
		return nil, false, err
	}
	verified, err := newCipher.Decrypt(replacement)
	defer clear(verified)
	if err != nil || !bytes.Equal(plaintext, verified) {
		return nil, false, errors.New("webhook replacement verification failed")
	}
	return replacement, true, nil
}
