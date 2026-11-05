// Shared OIDC fixtures encode local keys and signed tokens without network access.
package authn

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
)

func signOIDCTestToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	return signOIDCTestTokenWithKeyID(t, key, "test-key", claims)
}

func signOIDCTestTokenWithKeyID(t *testing.T, key *rsa.PrivateKey, keyID string, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return signOIDCRawTestToken(t, key, keyID, "RS256", string(payload))
}

func signOIDCRawTestToken(t *testing.T, key *rsa.PrivateKey, keyID, algorithm, payload string) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": algorithm, "kid": keyID, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s.%s", unsigned, base64.RawURLEncoding.EncodeToString(signature))
}

func oidcTestKeySet(keyID string, key *rsa.PublicKey) map[string]any {
	return map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": keyID, "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})}}}
}

func cloneAnyMap(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
