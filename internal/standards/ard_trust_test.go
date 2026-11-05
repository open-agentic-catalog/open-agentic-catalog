package standards

import (
	"bytes"
	"testing"
)

// The pinned schema names TrustManifest; the base context uses trustManifest.
// Both supported spellings must enforce the same known member types, including
// after JSON-LD normalization, without closing framework-specific extensions.
func TestARDTrustSchemaValidationAcrossNormalization(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, trust string
		valid       bool
	}{
		{"minimal", `{"identity":"https://example.com"}`, true},
		{"framework extensions", `{"identity":"spiffe://workloads.example.com/agent","identityType":"spiffe","trustSchema":{"governanceUri":"https://example.com/policy","verificationMethods":["custom"],"future":{"sequence":9007199254740993}},"attestations":[{"type":"custom","uri":"https://example.com/receipt","future":null}],"signature":"opaque","future":{"sequence":9007199254740993},"extensions":{"short":null,"com.example":{"future":9007199254740993}}}`, true},
		{"null manifest", `null`, false},
		{"array manifest", `[]`, false},
		{"missing identity", `{}`, false},
		{"null identity", `{"identity":null}`, false},
		{"numeric identity", `{"identity":17}`, false},
		{"attestation URI type", `{"identity":"https://example.com","attestations":[{"type":"custom","uri":17}]}`, false},
		{"attestation URI format", `{"identity":"https://example.com","attestations":[{"type":"custom","uri":"relative"}]}`, false},
		{"signature type", `{"identity":"https://example.com","signature":{}}`, false},
		{"framework type", `{"identity":"https://example.com","trustSchema":[]}`, false},
		{"framework methods type", `{"identity":"https://example.com","trustSchema":{"verificationMethods":[17]}}`, false},
		{"framework URI format", `{"identity":"https://example.com","trustSchema":{"governanceUri":"relative"}}`, false},
	} {
		for _, spelling := range []string{"trustManifest", "TrustManifest"} {
			t.Run(tc.name+"/"+spelling, func(t *testing.T) {
				raw := []byte(`{"identifier":"urn:air:example.com:agent:item","displayName":"Item","type":"application/json","data":{},"` + spelling + `":` + tc.trust + `}`)
				before := bytes.Clone(raw)
				for name, validate := range map[string]func([]byte) error{"complete": ValidateARDEntry, "fields": ValidateARDEntryFields, "search": ValidateARDSearchEntry} {
					if err := validate(raw); (err == nil) != tc.valid {
						t.Errorf("%s validation error=%v, want valid=%t", name, err, tc.valid)
					}
				}
				normalized, err := NormalizeARDEntry(raw, func(uri string) ([]byte, error) {
					t.Fatalf("trust validation fetched a context: %s", uri)
					return nil, nil
				})
				if err != nil {
					if tc.valid {
						t.Fatalf("normalize valid manifest: %v", err)
					}
				} else if err := ValidateARDEntry(normalized); (err == nil) != tc.valid {
					t.Errorf("normalized validation error=%v, want valid=%t: %s", err, tc.valid, normalized)
				}
				if !bytes.Equal(raw, before) {
					t.Fatal("validation or normalization changed source bytes")
				}
			})
		}
	}
}
