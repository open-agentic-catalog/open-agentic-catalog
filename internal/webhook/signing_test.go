// Signing tests cover rotation inputs, retained secrets and exact-byte signatures.
package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/examples/webhookreceiver"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

// TestRotateInputJSONContract checks the closed optional-boolean object defined
// by the rotate-secret OpenAPI request body, independently of signing services.
func TestRotateInputJSONContract(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, raw string
		want      bool
	}{
		{"default routine", `{}`, false},
		{"explicit routine", `{"revokePrevious":false}`, false},
		{"emergency", `{"revokePrevious":true}`, true},
		{"whitespace", " \n{\t\"revokePrevious\" : true \r\n}\t", true},
		{"escaped field name", `{"revoke\u0050revious":true}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var input RotateInput
			if err := json.Unmarshal([]byte(test.raw), &input); err != nil {
				t.Fatalf("valid rotation input: %v", err)
			}
			if input.RevokePrevious != test.want {
				t.Fatalf("revokePrevious = %t, want %t", input.RevokePrevious, test.want)
			}
		})
	}

	for name, raw := range map[string]string{
		"empty JSON":               ``,
		"whitespace only":          " \n\t",
		"incomplete object":        `{"revokePrevious":`,
		"trailing value":           `{} {}`,
		"null body":                `null`,
		"array body":               `[]`,
		"boolean body":             `true`,
		"number body":              `1`,
		"string body":              `"rotation"`,
		"null flag":                `{"revokePrevious":null}`,
		"string flag":              `{"revokePrevious":"true"}`,
		"number flag":              `{"revokePrevious":0}`,
		"array flag":               `{"revokePrevious":[]}`,
		"object flag":              `{"revokePrevious":{}}`,
		"unknown field":            `{"unknown":false}`,
		"unknown beside routine":   `{"revokePrevious":false,"unknown":true}`,
		"unknown beside emergency": `{"unknown":false,"revokePrevious":true}`,
		"wrong field case":         `{"RevokePrevious":true}`,
		"duplicate flag":           `{"revokePrevious":true,"revokePrevious":false}`,
		"escaped duplicate flag":   `{"revokePrevious":true,"revoke\u0050revious":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			var input RotateInput
			if err := json.Unmarshal([]byte(raw), &input); err == nil {
				t.Fatal("invalid rotation input was accepted")
			}
		})
	}
}

// TestRotateInputJSONReuseClearsEmergencySelection verifies that successful
// decoding applies the routine default even when the receiver was used before.
func TestRotateInputJSONReuseClearsEmergencySelection(t *testing.T) {
	t.Parallel()

	for _, routine := range []string{`{}`, `{"revokePrevious":false}`} {
		t.Run(routine, func(t *testing.T) {
			var input RotateInput
			if err := json.Unmarshal([]byte(`{"revokePrevious":true}`), &input); err != nil || !input.RevokePrevious {
				t.Fatalf("emergency setup: %+v, %v", input, err)
			}
			if err := json.Unmarshal([]byte(routine), &input); err != nil {
				t.Fatalf("routine rotation input: %v", err)
			}
			if input.RevokePrevious {
				t.Fatal("successful routine decoding retained the emergency flag")
			}
		})
	}
}

type rotationFaultCipher struct {
	Cipher
	calls  int
	failAt int
}

func (c *rotationFaultCipher) Encrypt(raw []byte) ([]byte, error) {
	c.calls++
	if c.calls == c.failAt {
		return nil, errors.New("injected encryption failure")
	}
	return c.Cipher.Encrypt(raw)
}

type rotationFaultStore struct{ Store }

func (s rotationFaultStore) UpdateEndpoint(context.Context, Endpoint, int64, ...audit.Record) (Endpoint, error) {
	return Endpoint{}, errors.New("injected endpoint write failure")
}

func TestRotationFailuresLeaveRetainedSignerUnchanged(t *testing.T) {
	serviceClock := clock.NewFake(time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC))
	cipher, _ := NewAESGCM(keyBytes(4))
	store := NewMemoryStore()
	service, _ := NewService(store, cipher, &recordingSender{}, serviceClock, &sequenceIDs{})
	ctx := adminContext("00000000-0000-7000-8000-000000000001")
	created, err := service.Create(ctx, CreateInput{Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, selected := range []Cipher{failingCipher{}, &rotationFaultCipher{Cipher: cipher, failAt: 1}, &rotationFaultCipher{Cipher: cipher, failAt: 2}} {
		service.cipher = selected
		if _, err := service.RotateSecret(ctx, created.Endpoint.ID, 1, RotateInput{}); err == nil {
			t.Fatal("failed crypto operation committed a rotation")
		}
		current, _ := store.GetEndpoint(ctx, created.Endpoint.TenantID, created.Endpoint.ID)
		if current.Revision != 1 || current.SecretVersion != 1 {
			t.Fatal("failed rotation changed the retained signer")
		}
	}
	service.cipher = cipher
	service.store = rotationFaultStore{store}
	if _, err := service.RotateSecret(ctx, created.Endpoint.ID, 1, RotateInput{}); err == nil {
		t.Fatal("failed persistence reported a rotation")
	}
}

func TestSigningOverlapExpiryAndEmergencyRevocation(t *testing.T) {
	serviceClock := clock.NewFake(time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC))
	cipher, _ := NewAESGCM(keyBytes(8))
	sender := &recordingSender{}
	store := NewMemoryStore()
	service, _ := NewService(store, cipher, sender, serviceClock, &sequenceIDs{})
	ctx := adminContext("00000000-0000-7000-8000-000000000001")
	created, err := service.Create(ctx, CreateInput{Name: "Receiver", URL: "https://hooks.example.com/events", EventTypes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := service.RotateSecret(ctx, created.Endpoint.ID, 1, RotateInput{})
	if err != nil || rotated.PreviousSecretExpiresAt == nil || !rotated.PreviousSecretExpiresAt.Equal(serviceClock.Now().Add(SigningOverlap)) {
		t.Fatalf("rotation expiry: %v", err)
	}
	if _, err := service.Test(ctx, created.Endpoint.ID, 2); err != nil {
		t.Fatal(err)
	}
	request := sender.Requests()[0]
	verifyExample := func(request SendRequest, secret string, version int, wantValid bool) {
		t.Helper()
		headers := make(http.Header, len(request.Headers))
		for name, value := range request.Headers {
			headers.Set(name, value)
		}
		if err := webhookreceiver.Verify(secret, version, headers, request.Body, serviceClock.Now()); (err == nil) != wantValid {
			t.Fatalf("receiver example verification for version %d: %v; want valid %t", version, err, wantValid)
		}
	}
	verifyExample(request, rotated.Secret, 2, true)
	verifyExample(request, created.Secret, 1, true)
	if request.Headers["X-Catalog-Signature"] != signingHeader(rotated.Secret, 2, request.Headers["X-Catalog-Timestamp"], request.Body) ||
		request.Headers["X-Catalog-Previous-Signature"] != signingHeader(created.Secret, 1, request.Headers["X-Catalog-Timestamp"], request.Body) {
		t.Fatal("overlap does not verify under both independent secrets")
	}
	if _, err := service.RotateSecret(ctx, created.Endpoint.ID, 2, RotateInput{}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("overlapping routine rotation: %v", err)
	}
	serviceClock.Advance(SigningOverlap)
	if _, err := service.Test(ctx, created.Endpoint.ID, 2); err != nil {
		t.Fatal(err)
	}
	if sender.Requests()[1].Headers["X-Catalog-Previous-Signature"] != "" {
		t.Fatal("expired predecessor still signs")
	}
	verifyExample(sender.Requests()[1], rotated.Secret, 2, true)
	verifyExample(sender.Requests()[1], created.Secret, 1, false)
	rotatedAgain, err := service.RotateSecret(ctx, created.Endpoint.ID, 2, RotateInput{})
	if err != nil {
		t.Fatal(err)
	}
	emergency, err := service.RotateSecret(ctx, created.Endpoint.ID, 3, RotateInput{RevokePrevious: true})
	if err != nil || emergency.PreviousSecretExpiresAt != nil {
		t.Fatalf("emergency rotation: %v", err)
	}
	if _, err := service.Test(ctx, created.Endpoint.ID, 4); err != nil {
		t.Fatal(err)
	}
	request = sender.Requests()[2]
	verifyExample(request, emergency.Secret, 4, true)
	verifyExample(request, rotated.Secret, 2, false)
	verifyExample(request, rotatedAgain.Secret, 3, false)
	if request.Headers["X-Catalog-Previous-Signature"] != "" || request.Headers["X-Catalog-Signature"] != signingHeader(emergency.Secret, 4, request.Headers["X-Catalog-Timestamp"], request.Body) {
		t.Fatal("emergency rotation retained an old signer")
	}
	endpoint, _ := store.GetEndpoint(context.Background(), created.Endpoint.TenantID, created.Endpoint.ID)
	material, err := readSigningMaterial(cipher, endpoint.SecretCiphertext)
	if err != nil || material.Previous != "" || material.PreviousUntil != nil || material.Current == rotatedAgain.Secret {
		t.Fatal("old signing material was retained after emergency rotation")
	}
	if _, err := service.Disable(ctx, endpoint.ID, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RotateSecret(ctx, endpoint.ID, 5, RotateInput{}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("disabled rotation: %v", err)
	}
}

func TestSigningMaterialRejectsMalformedEnvelopes(t *testing.T) {
	t.Parallel()
	cipher, _ := NewAESGCM(keyBytes(6))
	secret := "whsec_" + strings.Repeat("a", 43)
	for _, raw := range []string{
		`null`, `{}`, `{"version":2}`, `{"version":1,"version":1}`, `{} {}`, `whsec_short`,
		`{"version":1,"current":"` + secret + `","unknown":true}`,
		`{"version":1,"current":"` + secret + `","previous":"` + secret + `"}`,
		`{"version":1,"current":"` + secret + `","previousUntil":"2026-09-06T12:00:00Z"}`,
		`{"version":1,"current":"` + secret + `","previous":"whsec_invalid","previousUntil":"2026-09-06T12:00:00Z"}`,
		`{"version":1,"current":"` + secret + `","previous":"` + secret + `","previousUntil":"2026-09-06T12:00:00Z"}`,
		`{"version":1,"current":"` + secret + `","previous":"whsec_` + strings.Repeat("b", 43) + `","previousUntil":"0001-01-01T00:00:00Z"}`,
		`{"version":1,"current":"whsec_invalid"}`, strings.Repeat("x", 2049),
	} {
		encrypted, err := cipher.Encrypt([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := readSigningMaterial(cipher, encrypted); err == nil {
			t.Fatal("malformed signing material accepted")
		}
	}
	if _, err := readSigningMaterial(cipher, make([]byte, 4097)); err == nil {
		t.Fatal("over-limit ciphertext accepted")
	}
	if _, err := readSigningMaterial(cipher, []byte("invalid")); err == nil {
		t.Fatal("invalid ciphertext accepted")
	}
	for _, value := range []string{"", "whsec_" + strings.Repeat("!", 43), "wrong_" + strings.Repeat("a", 43)} {
		if validSigningSecret(value) {
			t.Fatal("invalid signing secret accepted")
		}
	}
	// Exercise every alphabet boundary without relying on randomly generated keys.
	if !validSigningSecret("whsec_" + strings.Repeat("aAzZ09-_", 5) + "aZ9") {
		t.Fatal("valid signing secret alphabet rejected")
	}
	raw, err := json.Marshal(signingMaterial{Version: 1, Current: secret})
	if err != nil {
		t.Fatal(err)
	}
	encrypted, _ := cipher.Encrypt(raw)
	if material, err := readSigningMaterial(cipher, encrypted); err != nil || material.Current != secret {
		t.Fatalf("valid envelope: %v", err)
	}
	for _, size := range []int{2048, 2049} {
		padded := append(append([]byte{}, raw...), []byte(strings.Repeat(" ", size-len(raw)))...)
		encrypted, err := cipher.Encrypt(padded)
		if err != nil {
			t.Fatal(err)
		}
		material, err := readSigningMaterial(cipher, encrypted)
		if size == 2048 {
			if err != nil || material.Version != 1 || material.Current != secret || material.Previous != "" || material.PreviousUntil != nil {
				t.Fatalf("exact signing envelope limit: %+v, %v", material, err)
			}
		} else if err == nil {
			t.Fatal("otherwise valid oversized signing envelope accepted")
		}
	}
}
