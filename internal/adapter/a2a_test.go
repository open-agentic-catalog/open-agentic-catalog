package adapter

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const a2aBoundaryCard = `{
  "name":"Agent", "description":"Local validation fixture", "version":"1.0.0",
  "supportedInterfaces":[{"url":"https://agents.example.com/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
  "capabilities":{}, "defaultInputModes":["text/plain"], "defaultOutputModes":["text/plain"],
  "skills":[{"id":"echo","name":"Echo","description":"Returns text","tags":["text"]}]
}`

func TestA2ARejectsOASFProjectionWrapper(t *testing.T) {
	card := json.RawMessage(a2aBoundaryCard)
	wrapper, err := json.Marshal(map[string]any{"card_data": card, "card_schema_version": "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		data  json.RawMessage
		valid bool
	}{{"card", card, true}, {"oasf-wrapper", wrapper, false}} {
		t.Run(tc.name, func(t *testing.T) {
			result := NewRegistry().Validate(t.Context(), catalog.KindAgent, catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: tc.data})
			if result.ProfileID != ProfileA2AAgentCard {
				t.Fatal(result.ProfileID)
			}
			if tc.valid {
				if len(result.Findings) != 0 {
					t.Fatalf("valid card rejected: %+v", result.Findings)
				}
			} else if len(result.Findings) != 1 || result.Findings[0].Code != "A2A_PROTOJSON_INVALID" || result.Findings[0].Severity != catalog.SeverityError {
				t.Fatalf("OASF wrapper did not fail the native payload contract: %+v", result.Findings)
			}
		})
	}
}

func TestA2AClientCredentialsRequiredMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, tokenURL string
		scopes         map[string]string
		valid          bool
	}{
		{"valid", "https://identity.example.com/token", map[string]string{"read": "Read"}, true},
		{"empty scopes", "https://identity.example.com/token", map[string]string{}, true},
		{"missing token URL", "", map[string]string{"read": "Read"}, false},
		{"insecure token URL", "http://identity.example.com/token", map[string]string{"read": "Read"}, false},
		{"missing scopes", "https://identity.example.com/token", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var card map[string]any
			if err := json.Unmarshal([]byte(a2aBoundaryCard), &card); err != nil {
				t.Fatal(err)
			}
			flow := map[string]any{"tokenUrl": tc.tokenURL}
			if tc.scopes != nil {
				flow["scopes"] = tc.scopes
			}
			card["securitySchemes"] = map[string]any{"oauth": map[string]any{"oauth2SecurityScheme": map[string]any{"flows": map[string]any{"clientCredentials": flow}}}}
			raw, err := json.Marshal(card)
			if err != nil {
				t.Fatal(err)
			}
			result := NewRegistry().Validate(t.Context(), catalog.KindAgent, catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: raw})
			if tc.valid {
				if len(result.Findings) != 0 {
					t.Fatalf("valid OAuth flow rejected: %+v", result.Findings)
				}
			} else if len(result.Findings) != 1 || result.Findings[0].Code != "A2A_OAUTH_FLOW_INVALID" || result.Findings[0].Path != "/securitySchemes/oauth" || result.Findings[0].Severity != catalog.SeverityError {
				t.Fatalf("invalid OAuth flow findings: %+v", result.Findings)
			}
		})
	}
}

func TestA2AProtocolVersionSpelling(t *testing.T) {
	for _, tc := range []struct {
		version string
		valid   bool
	}{
		{"1.0", true}, {"0.3", true}, {"2.12", true},
		{" 1.0", false}, {"1.0 ", false}, {"1.0\n", false}, {"1.0\r", false},
		{"\t1.0", false}, {"\u00a01.0\u2003", false}, {"1.\n0", false},
	} {
		t.Run(strconv.Quote(tc.version), func(t *testing.T) {
			raw := strings.Replace(a2aBoundaryCard, `"protocolVersion":"1.0"`, `"protocolVersion":`+strconv.Quote(tc.version), 1)
			// Bindings are extensible; validating the version must not impose the
			// interoperability harness's narrower JSONRPC-only selection.
			raw = strings.Replace(raw, `"JSONRPC"`, `"example-custom-binding"`, 1)
			result := NewRegistry().Validate(t.Context(), catalog.KindAgent, catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: json.RawMessage(raw)})
			if tc.valid {
				if len(result.Findings) != 0 {
					t.Fatalf("valid interface rejected: %#v", result.Findings)
				}
				return
			}
			if len(result.Findings) != 1 || result.Findings[0].Code != "A2A_INTERFACE_INVALID" || result.Findings[0].Severity != catalog.SeverityError || result.Findings[0].Path != "/supportedInterfaces/0" {
				t.Fatalf("invalid interface findings: %#v", result.Findings)
			}
		})
	}
}

func TestA2ASignatureMetadataBoundaries(t *testing.T) {
	encode := func(header string) string { return base64.RawURLEncoding.EncodeToString([]byte(header)) }
	valid := encode(`{"alg":"ES256","kid":"test-key"}`)
	for _, tc := range []struct {
		name        string
		protected   string
		signature   string
		unprotected map[string]any
		wantPath    string
	}{
		{name: "required members only", protected: valid},
		{name: "decoded JSON whitespace", protected: encode("{\n \"alg\": \"ES256\", \"kid\": \"test-key\"\n}")},
		{name: "opaque empty key ID", protected: encode(`{"alg":"ES256","kid":""}`)},
		{name: "extensions do not shadow required members", protected: encode(`{"alg":"ES256","kid":"test-key","ALG":"none","KID":null}`)},
		{name: "extension numbers remain opaque", protected: encode(`{"alg":"ES256","kid":"test-key","extension":1e400}`)},
		{name: "extension algorithm", protected: encode(`{"alg":"https://example.com/signing-algorithm","kid":"test-key"}`)},
		{name: "disjoint extension", protected: valid, unprotected: map[string]any{"extension": map[string]any{"enabled": true}}},
		{name: "case-distinct extension", protected: valid, unprotected: map[string]any{"ALG": "none"}},
		{name: "protected CR", protected: valid[:4] + "\r" + valid[4:], wantPath: "/protected"},
		{name: "protected LF", protected: valid + "\n", wantPath: "/protected"},
		{name: "protected CRLF", protected: "\r\n" + valid, wantPath: "/protected"},
		{name: "protected space", protected: valid + " ", wantPath: "/protected"},
		{name: "protected padding", protected: valid + "=", wantPath: "/protected"},
		{name: "algorithm alias", protected: encode(`{"ALG":"ES256","kid":"test-key"}`), wantPath: "/protected"},
		{name: "algorithm shadow", protected: encode(`{"alg":"none","ALG":"ES256","kid":"test-key"}`), wantPath: "/protected"},
		{name: "missing key ID", protected: encode(`{"alg":"ES256"}`), wantPath: "/protected"},
		{name: "null key ID", protected: encode(`{"alg":"ES256","kid":null}`), wantPath: "/protected"},
		{name: "numeric key ID", protected: encode(`{"alg":"ES256","kid":123}`), wantPath: "/protected"},
		{name: "key ID alias", protected: encode(`{"alg":"ES256","KID":"test-key"}`), wantPath: "/protected"},
		{name: "unprotected key ID only", protected: encode(`{"alg":"ES256"}`), unprotected: map[string]any{"kid": "test-key"}, wantPath: "/protected"},
		{name: "signature CR", protected: valid, signature: "c2\rln", wantPath: "/signature"},
		{name: "signature LF", protected: valid, signature: "c2ln\n", wantPath: "/signature"},
		{name: "signature CRLF", protected: valid, signature: "\r\nc2ln", wantPath: "/signature"},
		{name: "signature tab", protected: valid, signature: "c2\tln", wantPath: "/signature"},
		{name: "signature padding", protected: valid, signature: "c2ln=", wantPath: "/signature"},
		{name: "signature alphabet", protected: valid, signature: "c2!n", wantPath: "/signature"},
		{name: "conflicting algorithm", protected: valid, unprotected: map[string]any{"alg": "none"}, wantPath: "/header"},
		{name: "repeated key ID", protected: valid, unprotected: map[string]any{"kid": "test-key"}, wantPath: "/header"},
		{name: "repeated extension", protected: encode(`{"alg":"ES256","kid":"test-key","x/key":true}`), unprotected: map[string]any{"x/key": true}, wantPath: "/header"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signature := tc.signature
			if signature == "" {
				signature = "c2ln" // Syntax fixture; the Catalog does not verify signatures.
			}
			candidate := map[string]any{"protected": tc.protected, "signature": signature}
			if tc.unprotected != nil {
				candidate["header"] = tc.unprotected
			}
			var card map[string]any
			if err := json.Unmarshal([]byte(a2aBoundaryCard), &card); err != nil {
				t.Fatal(err)
			}
			// A preceding valid signature must not hide a later invalid one;
			// required names may repeat across independent signature objects.
			card["signatures"] = []any{map[string]any{"protected": valid, "signature": "c2ln"}, candidate}
			raw, err := json.Marshal(card)
			if err != nil {
				t.Fatal(err)
			}
			result := NewRegistry().Validate(t.Context(), catalog.KindAgent, catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: raw})
			if result.ProfileID != ProfileA2AAgentCard {
				t.Fatalf("profile = %q", result.ProfileID)
			}
			if tc.wantPath == "" {
				if len(result.Findings) != 0 {
					t.Fatalf("valid signature metadata rejected: %#v", result.Findings)
				}
				return
			}
			if len(result.Findings) != 1 || result.Findings[0].Code != "A2A_SIGNATURE_INVALID" || result.Findings[0].Severity != catalog.SeverityError || result.Findings[0].Path != "/signatures/1"+tc.wantPath {
				t.Fatalf("invalid signature metadata findings: %#v", result.Findings)
			}
		})
	}
}
