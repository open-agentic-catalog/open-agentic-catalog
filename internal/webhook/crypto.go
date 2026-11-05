// At-rest encryption authenticates webhook signing material with a deployment
// key and purpose-specific associated data.

package webhook

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// Cipher protects endpoint signing material at rest. It is separate from the
// per-endpoint HMAC signing protocol and must authenticate stored ciphertext.
type Cipher interface {
	Encrypt([]byte) ([]byte, error)
	Decrypt([]byte) ([]byte, error)
}

// AESGCM uses a deployment key and fixed webhook-purpose associated data.
type AESGCM struct{ aead cipher.AEAD }

// NewAESGCM requires a decoded 32-byte key, not its base64 environment spelling.
func NewAESGCM(key []byte) (*AESGCM, error) {
	if len(key) != 32 {
		return nil, errors.New("webhook encryption key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create webhook cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create webhook AEAD: %w", err)
	}
	return &AESGCM{aead: aead}, nil
}

// Encrypt returns a fresh random nonce followed by authenticated ciphertext.
func (c *AESGCM) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("create webhook encryption nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, plaintext, []byte("open-agentic-catalog-webhook-v1")), nil
}

// Decrypt authenticates the nonce-prefixed envelope before returning plaintext.
func (c *AESGCM) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < c.aead.NonceSize() {
		return nil, errors.New("webhook secret ciphertext is truncated")
	}
	nonce := ciphertext[:c.aead.NonceSize()]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext[c.aead.NonceSize():], []byte("open-agentic-catalog-webhook-v1"))
	if err != nil {
		return nil, errors.New("webhook secret could not be decrypted")
	}
	return plaintext, nil
}
