package standards

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func aiCatalogWithEntries(entries string) []byte {
	return []byte(`{"specVersion":"1.0","entries":` + entries + `}`)
}

// TestNativeSchemaValidatorsRejectMalformedJSON ensures representation validators
// reject incomplete or multiple JSON values before applying their schemas.
func TestNativeSchemaValidatorsRejectMalformedJSON(t *testing.T) {
	t.Parallel()
	for name, validate := range map[string]func([]byte) error{
		"MCP Server Card": ValidateMCPServerCard,
		"MCP Registry":    ValidateMCPRegistryServer,
		"Plugin manifest": ValidatePluginManifest,
		"Plugin MCP":      ValidatePluginMCP,
		"Plugin server":   ValidatePluginMCPServer,
	} {
		t.Run(name, func(t *testing.T) {
			for _, raw := range []string{"", "{", "{} {}"} {
				if err := validate([]byte(raw)); err == nil || !strings.HasPrefix(err.Error(), "decode JSON document:") {
					t.Fatalf("malformed JSON %q was not rejected during decoding: %v", raw, err)
				}
			}
		})
	}
}

func TestAICatalogDocumentAndEntryShapeGuardrails(t *testing.T) {
	t.Parallel()

	invalidDocuments := map[string][]byte{
		"ambiguous JSON":        []byte(`{"specVersion":"1.0","specVersion":"1.0","entries":[]}`),
		"null document":         []byte(`null`),
		"array document":        []byte(`[]`),
		"missing version":       []byte(`{"entries":[]}`),
		"wrong version type":    []byte(`{"specVersion":1,"entries":[]}`),
		"malformed version":     []byte(`{"specVersion":"01.0","entries":[]}`),
		"unsupported major":     []byte(`{"specVersion":"2.0","entries":[]}`),
		"host not object":       []byte(`{"specVersion":"1.0","host":[],"entries":[]}`),
		"host missing name":     []byte(`{"specVersion":"1.0","host":{},"entries":[]}`),
		"host empty identifier": []byte(`{"specVersion":"1.0","host":{"displayName":"Catalog","identifier":""},"entries":[]}`),
		"host relative URL":     []byte(`{"specVersion":"1.0","host":{"displayName":"Catalog","documentationUrl":"relative"},"entries":[]}`),
		"host invalid trust":    []byte(`{"specVersion":"1.0","host":{"displayName":"Catalog","trustManifest":{}},"entries":[]}`),
		"extensions not object": []byte(`{"specVersion":"1.0","extensions":[],"entries":[]}`),
		"signature not JWS":     []byte(`{"specVersion":"1.0","signature":"invalid","entries":[]}`),
		"entries wrong type":    []byte(`{"specVersion":"1.0","entries":{}}`),
	}
	for name, document := range invalidDocuments {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAICatalog(document); err == nil {
				t.Fatal("invalid document was accepted")
			}
		})
	}

	invalidEntries := map[string]string{
		"not object":              `[]`,
		"missing identifier":      `{"type":"application/json","url":"https://example.com"}`,
		"empty identifier":        `{"identifier":"","type":"application/json","url":"https://example.com"}`,
		"missing type":            `{"identifier":"urn:air:example.com:default:item","url":"https://example.com"}`,
		"missing delivery":        `{"identifier":"urn:air:example.com:default:item","type":"application/json"}`,
		"duplicate delivery":      `{"identifier":"urn:air:example.com:default:item","type":"application/json","url":"https://example.com","data":{}}`,
		"relative URL":            `{"identifier":"urn:air:example.com:default:item","type":"application/json","url":"relative"}`,
		"display name type":       `{"identifier":"urn:air:example.com:default:item","type":"application/json","url":"https://example.com","displayName":1}`,
		"description type":        `{"identifier":"urn:air:example.com:default:item","type":"application/json","url":"https://example.com","description":1}`,
		"empty version":           `{"identifier":"urn:air:example.com:default:item","type":"application/json","url":"https://example.com","version":""}`,
		"invalid update time":     `{"identifier":"urn:air:example.com:default:item","type":"application/json","url":"https://example.com","updatedAt":"yesterday"}`,
		"update time type":        `{"identifier":"urn:air:example.com:default:item","type":"application/json","url":"https://example.com","updatedAt":42}`,
		"invalid tags":            `{"identifier":"urn:air:example.com:default:item","type":"application/json","url":"https://example.com","tags":[1]}`,
		"invalid publisher":       `{"identifier":"urn:air:example.com:default:item","type":"application/json","url":"https://example.com","publisher":[]}`,
		"invalid extensions":      `{"identifier":"urn:air:example.com:default:item","type":"application/json","url":"https://example.com","extensions":{"custom":true}}`,
		"invalid trust":           `{"identifier":"urn:air:example.com:default:item","type":"application/json","url":"https://example.com","trustManifest":[]}`,
		"invalid trust schema":    `{"identifier":"urn:air:example.com:default:item","type":"application/json","data":{},"trustManifest":{"identity":"https://example.com","trustSchema":[]}}`,
		"empty extension segment": `{"identifier":"urn:air:example.com:default:item","type":"application/json","data":{},"extensions":{"com..example":true}}`,
	}
	for name, entry := range invalidEntries {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAICatalog(aiCatalogWithEntries("[" + entry + "]")); err == nil {
				t.Fatal("invalid entry was accepted")
			}
		})
	}

	duplicateVersion := aiCatalogWithEntries(`[
		{"identifier":"urn:air:example.com:default:item","version":"1.0.0","type":"application/json","data":{}},
		{"identifier":"urn:air:example.com:default:item","version":"1.0.0","type":"application/json","data":{}}
	]`)
	if err := ValidateAICatalog(duplicateVersion); err == nil {
		t.Fatal("duplicate identifier and version was accepted")
	}
	duplicateUnversioned := aiCatalogWithEntries(`[
		{"identifier":"urn:air:example.com:default:item","type":"application/json","data":{}},
		{"identifier":"urn:air:example.com:default:item","type":"application/json","data":{}}
	]`)
	if err := ValidateAICatalog(duplicateUnversioned); err == nil {
		t.Fatal("duplicate unversioned identifier was accepted")
	}
}

func TestAICatalogTrustEvidenceGuardrails(t *testing.T) {
	t.Parallel()

	validDigest := "sha256:" + strings.Repeat("a", 64)
	validators := []struct {
		name     string
		validate func(json.RawMessage) error
		raw      string
		wantErr  bool
	}{
		{"publisher", validateAICatalogPublisher, `{"identifier":"https://example.com","displayName":"Example","identityType":"domain"}`, false},
		{"publisher object", validateAICatalogPublisher, `[]`, true},
		{"publisher identifier", validateAICatalogPublisher, `{"displayName":"Example"}`, true},
		{"publisher display name", validateAICatalogPublisher, `{"identifier":"https://example.com"}`, true},
		{"publisher identity type", validateAICatalogPublisher, `{"identifier":"https://example.com","displayName":"Example","identityType":1}`, true},
		{"attestation", validateAICatalogAttestations, `[{"type":"review","uri":"https://example.com/review","digest":"` + validDigest + `","size":10,"description":"Reviewed"}]`, false},
		{"attestations array", validateAICatalogAttestations, `{}`, true},
		{"attestation type", validateAICatalogAttestations, `[{"uri":"https://example.com/review"}]`, true},
		{"attestation URI", validateAICatalogAttestations, `[{"type":"review","uri":"relative"}]`, true},
		{"attestation digest", validateAICatalogAttestations, `[{"type":"review","uri":"https://example.com/review","digest":"invalid"}]`, true},
		{"attestation size", validateAICatalogAttestations, `[{"type":"review","uri":"https://example.com/review","size":-1}]`, true},
		{"attestation description", validateAICatalogAttestations, `[{"type":"review","uri":"https://example.com/review","description":1}]`, true},
	}
	for _, testCase := range validators {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.validate(json.RawMessage(testCase.raw))
			if (err != nil) != testCase.wantErr {
				t.Fatalf("error = %v, want error %t", err, testCase.wantErr)
			}
		})
	}

	subjects := []struct {
		name    string
		raw     string
		typeID  string
		url     string
		bind    bool
		wantErr bool
	}{
		{"valid", `{"type":"application/json","digest":"` + validDigest + `","url":"https://example.com/item"}`, "application/json", "https://example.com/item", true, false},
		{"object", `[]`, "", "", false, true},
		{"type", `{"digest":"` + validDigest + `"}`, "", "", false, true},
		{"type mismatch", `{"type":"application/json","digest":"` + validDigest + `"}`, "text/plain", "", true, true},
		{"digest missing", `{"type":"application/json"}`, "", "", false, true},
		{"digest invalid", `{"type":"application/json","digest":"bad"}`, "", "", false, true},
		{"URL relative", `{"type":"application/json","digest":"` + validDigest + `","url":"relative"}`, "", "", false, true},
		{"URL mismatch", `{"type":"application/json","digest":"` + validDigest + `","url":"https://example.com/other"}`, "application/json", "https://example.com/item", true, true},
	}
	for _, testCase := range subjects {
		t.Run("subject "+testCase.name, func(t *testing.T) {
			err := validateAICatalogSubject(json.RawMessage(testCase.raw), testCase.typeID, testCase.url, testCase.bind)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("error = %v, want error %t", err, testCase.wantErr)
			}
		})
	}

	invalidManifests := map[string]string{
		"object":                 `[]`,
		"identity":               `{"identity":"relative","trustSchema":{"identifier":"x","version":"1"}}`,
		"identity type":          `{"identity":"https://example.com","identityType":1,"trustSchema":{"identifier":"x","version":"1"}}`,
		"attestations array":     `{"identity":"https://example.com","attestations":{}}`,
		"privacy URL":            `{"identity":"https://example.com","trustSchema":{"identifier":"x","version":"1"},"privacyPolicyUrl":"relative"}`,
		"issued time":            `{"identity":"https://example.com","trustSchema":{"identifier":"x","version":"1"},"issuedAt":"today"}`,
		"subject":                `{"identity":"https://example.com","trustSchema":{"identifier":"x","version":"1"},"subject":[]}`,
		"extensions":             `{"identity":"https://example.com","trustSchema":{"identifier":"x","version":"1"},"extensions":{"custom":true}}`,
		"signed without subject": `{"identity":"https://example.com","issuedAt":"2026-08-30T12:00:00Z","signature":"eyJhbGciOiJFUzI1NiJ9..c2ln"}`,
		"signed without time":    `{"identity":"https://example.com","subject":{"type":"application/json","digest":"` + validDigest + `"},"signature":"eyJhbGciOiJFUzI1NiJ9..c2ln"}`,
	}
	for name, raw := range invalidManifests {
		t.Run("manifest "+name, func(t *testing.T) {
			if err := validateAICatalogTrustManifest(json.RawMessage(raw), "", "", ""); err == nil {
				t.Fatal("invalid Trust Manifest was accepted")
			}
		})
	}
}

func TestDetachedJWSAndDigestSyntax(t *testing.T) {
	t.Parallel()

	encode := func(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
	for _, algorithm := range []string{"ES256", "ES384", "EdDSA", "PS256", "PS384", "RS256"} {
		jws := encode(`{"alg":"`+algorithm+`"}`) + ".." + encode("signature")
		if err := validateDetachedJWS(json.RawMessage(`"` + jws + `"`)); err != nil {
			t.Fatalf("algorithm %s returned %v", algorithm, err)
		}
	}
	invalidJWS := []string{
		`1`,
		`""`,
		`"not.compact"`,
		`"invalid..c2ln"`,
		`"%%%..c2ln"`,
		`"` + encode(`[]`) + `..c2ln"`,
		`"` + encode(`{"alg":"ES256","alg":"RS256"}`) + `..c2ln"`,
		`"` + encode(`{}`) + `..c2ln"`,
		`"` + encode(`{"alg":"HS256"}`) + `..c2ln"`,
		`"` + encode(`{"alg":"ES256"}`) + `..%%%"`,
	}
	for _, raw := range invalidJWS {
		if err := validateDetachedJWS(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid JWS %s was accepted", raw)
		}
	}

	validDigests := []string{
		"sha256:" + strings.Repeat("a", 64),
		"sha384:" + strings.Repeat("b", 96),
		"sha512:" + strings.Repeat("c", 128),
		"sha3-256:" + strings.Repeat("d", 64),
	}
	for _, digest := range validDigests {
		if err := validateDigest(json.RawMessage(`"` + digest + `"`)); err != nil {
			t.Fatalf("digest %q returned %v", digest, err)
		}
	}
	for _, raw := range []string{`1`, `""`, `"sha256"`, `"sha256:ABC"`, `"sha1:` + strings.Repeat("a", 40) + `"`, `"sha256:` + strings.Repeat("g", 64) + `"`} {
		if err := validateDigest(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid digest %s was accepted", raw)
		}
	}
}

func TestARDManifestAndJSONLDBoundaries(t *testing.T) {
	t.Parallel()

	entry := map[string]any{
		"@context":              ARDBaseContext,
		"@id":                   "urn:air:example.com:default:weather",
		"identifier":            "urn:air:example.com:default:weather",
		"displayName":           "Weather Agent",
		"type":                  "application/a2a-agent-card+json",
		"url":                   "https://agents.example.com/weather",
		"representativeQueries": []string{"Find weather agents", "Show weather agents"},
	}
	raw, err := MarshalAndValidateARDManifest(map[string]any{"entries": []any{entry}})
	if err != nil || ValidateARDManifest(raw) != nil {
		t.Fatalf("valid ARD manifest = %s, %v", raw, err)
	}
	// The schema permits distinct IRIs; publication also requires proven identity
	// equivalence, so passing schema validation alone must not publish this entry.
	entry["@id"] = "urn:air:example.com:default:different"
	semanticOnly, err := json.Marshal(map[string]any{"entries": []any{entry}})
	if err != nil || ValidateARDManifest(semanticOnly) != nil {
		t.Fatalf("semantic-negative fixture must pass schema validation: %s, %v", semanticOnly, err)
	}
	if document, err := MarshalAndValidateARDManifest(map[string]any{"entries": []any{entry}}); err == nil || !strings.Contains(err.Error(), "ARD entry 0") || document != nil {
		t.Fatalf("publication accepted or failed to attribute an inconsistent identity: %s, %v", document, err)
	}
	entry["@id"] = entry["identifier"]
	if _, err := MarshalAndValidateARDManifest(map[string]any{"entries": []any{map[string]any{"identifier": "invalid"}}}); err == nil {
		t.Fatal("invalid ARD entry was accepted in a manifest")
	}
	if _, err := MarshalAndValidateARDManifest(make(chan int)); err == nil {
		t.Fatal("unmarshalable ARD manifest was accepted")
	}
	if err := ValidateARDManifest([]byte(`{"entries":"invalid"}`)); err == nil {
		t.Fatal("invalid ARD manifest shape was accepted")
	}

	for name, input := range map[string][]byte{
		"ambiguous":                []byte(`{"identifier":"one","identifier":"two"}`),
		"null":                     []byte(`null`),
		"array":                    []byte(`[]`),
		"processor type confusion": []byte(`{"":10}`),
	} {
		t.Run("normalize "+name, func(t *testing.T) {
			if _, err := NormalizeARDEntry(input, nil); err == nil {
				t.Fatal("invalid ARD JSON-LD entry was accepted")
			}
		})
	}
	remote := []byte(`{"@context":"https://contexts.example.com/remote","identifier":"urn:air:example.com:default:item","displayName":"Item","type":"application/json","url":"https://example.com"}`)
	if _, err := NormalizeARDEntry(remote, nil); err == nil {
		t.Fatal("unconfigured remote context fetch was accepted")
	}
	if _, err := NormalizeARDEntry(remote, func(string) ([]byte, error) { return nil, errors.New("offline") }); err == nil {
		t.Fatal("remote context fetch failure was ignored")
	}
	if _, err := NormalizeARDEntry(remote, func(string) ([]byte, error) { return []byte(`{"@context":{},"@context":{}}`), nil }); err == nil {
		t.Fatal("ambiguous remote context was accepted")
	}

	base, err := loadBaseContext()
	if err != nil {
		t.Fatal(err)
	}
	loader := &boundedContextLoader{baseContext: base, fetch: func(string) ([]byte, error) { return []byte(`{}`), nil }}
	if document, err := loader.LoadDocument(ARDBaseContext); err != nil || document.DocumentURL != ARDBaseContext {
		t.Fatalf("base context load = %#v, %v", document, err)
	}
	for index := 0; index < maxRemoteContexts; index++ {
		if _, err := loader.LoadDocument("https://contexts.example.com/context"); err != nil {
			t.Fatalf("remote context %d returned %v", index, err)
		}
	}
	if _, err := loader.LoadDocument("https://contexts.example.com/over-budget"); err == nil {
		t.Fatal("remote context budget was not enforced")
	}

	for _, term := range []string{"", "unknown", "displayName.child"} {
		if _, err := ResolveARDTerm(term, nil); err == nil {
			t.Fatalf("unsupported term %q was accepted", term)
		}
	}
	if term, err := ResolveARDTerm("publisher", nil); err != nil || term != "publisher" {
		t.Fatalf("publisher term = %q, %v", term, err)
	}
	if term, err := ResolveARDTerm("metadata.region", nil); err != nil || term != "metadata.region" {
		t.Fatalf("metadata term = %q, %v", term, err)
	}
}

func TestARDSemanticValidationRejectsAmbiguousIdentityAndTypes(t *testing.T) {
	t.Parallel()
	valid := `"identifier":"urn:air:example.com:default:agent","displayName":"Agent","type":"application/json","url":"https://example.com/agent"`
	tests := map[string]string{
		"malformed object":        `{"identifier":`,
		"identifier type":         `{"identifier":1,"displayName":"Agent","type":"application/json"}`,
		"identifier form":         `{"identifier":"urn:example:agent","displayName":"Agent","type":"application/json"}`,
		"display name type":       `{"identifier":"urn:air:example.com:default:agent","displayName":1,"type":"application/json"}`,
		"type type":               `{"identifier":"urn:air:example.com:default:agent","displayName":"Agent","type":1}`,
		"media type":              `{"identifier":"urn:air:example.com:default:agent","displayName":"Agent","type":"invalid"}`,
		"node identifier type":    `{` + valid + `,"@id":1}`,
		"node identity mismatch":  `{` + valid + `,"@id":"urn:air:example.com:default:other"}`,
		"trust type":              `{` + valid + `,"trustManifest":"invalid"}`,
		"trust identity missing":  `{` + valid + `,"trustManifest":{}}`,
		"trust identity empty":    `{` + valid + `,"trustManifest":{"identity":""}}`,
		"trust identity blank":    `{` + valid + `,"trustManifest":{"identity":"  "}}`,
		"trust identity mismatch": `{` + valid + `,"trustManifest":{"identity":"https://other.example/issuer"}}`,
		"trust deceptive prefix":  `{` + valid + `,"trustManifest":{"identity":"https://notexample.com"}}`,
		"trust deceptive suffix":  `{` + valid + `,"TrustManifest":{"identity":"https://example.com.attacker.test"}}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateARDSemantics([]byte(raw)); err == nil {
				t.Fatalf("invalid ARD semantics were accepted: %s", raw)
			}
		})
	}
	if err := validateARDSemantics([]byte(`{` + valid + `,"TrustManifest":{"identity":"did:web:catalog.example.com"}}`)); err != nil {
		t.Fatalf("legacy-cased trust manifest was rejected: %v", err)
	}
	if publisher := ardPublisher("not-an-air-identifier"); publisher != "" {
		t.Fatalf("invalid AIR publisher = %q", publisher)
	}
	if domain := trustIdentityDomain("did:web:%"); domain != "" {
		t.Fatalf("invalid did:web domain = %q", domain)
	}
	if domain := trustIdentityDomain("%"); domain != "" {
		t.Fatalf("invalid trust identity domain = %q", domain)
	}
}

// Sparse fields use the pinned shape plus the reviewed trust-member alias;
// validation must never alter retained source bytes.
func TestARDEntryFieldsPreserveSparseAndTrustEvidence(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{}`, true}, {`{"extension":{"value":9007199254740993}}`, true},
		{`{"trustManifest":{"identity":"did:web:example.com"}}`, true},
		{`{"TrustManifest":{"identity":"did:web:example.com"}}`, true},
		{`{"trustManifest":{"identity":42}}`, false},
		{`{"trustManifest":null}`, false},
		{`{"trustManifest":{},"TrustManifest":{}}`, false},
		{`{"tags":null}`, false}, {`{"identifier":"one","identifier":"two"}`, false},
		{`null`, false}, {`[]`, false}, {`{`, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			raw := []byte(tc.raw)
			err := ValidateARDEntryFields(raw)
			if (err == nil) != tc.valid || string(raw) != tc.raw {
				t.Fatalf("valid=%t error=%v raw=%s", tc.valid, err, raw)
			}
		})
	}
}

func TestARDSearchEntryValidatesPresentSemantics(t *testing.T) {
	t.Parallel()
	const identifier = `"identifier":"urn:air:example.com:default:agent"`
	for _, tc := range []struct {
		name, fields         string
		searchOK, completeOK bool
	}{
		{"omitted type and node identity", "", true, false},
		{"ordinary type", `,"type":"application/json"`, true, true},
		{"unfamiliar type", `,"type":"application/vnd.example.card+json"`, true, true},
		{"parameterized type", `,"type":"application/vnd.example.card+json; version=1"`, true, true},
		{"quoted parameter", `,"type":"text/x-acme; profile=\"https://schemas.example/card\""`, true, true},
		{"type spelling preserved", `,"type":" APPLICATION/VND.EXAMPLE.CARD+JSON; version=1 "`, true, true},
		{"invalid type", `,"type":"not-a-media-type"`, false, false},
		{"invalid parameters", `,"type":"application/json; charset"`, false, false},
		{"empty type", `,"type":""`, false, false},
		{"blank type", `,"type":" "`, false, false},
		{"matching identity without type", `,"@id":"urn:air:example.com:default:agent"`, true, false},
		{"matching identity", `,"type":"application/json","@id":"urn:air:example.com:default:agent"`, true, true},
		{"escaped matching identity", `,"type":"application/json","@id":"urn:air:example.com:default:\u0061gent"`, true, true},
		{"mismatched identity", `,"type":"application/json","@id":"urn:air:other.example:default:agent"`, false, false},
		{"empty identity", `,"type":"application/json","@id":""`, false, false},
		{"blank identity", `,"type":"application/json","@id":" "`, false, false},
		{"unfamiliar extensions", `,"type":"application/vnd.example.card+json; version=1","Type":"not-a-media-type","@Id":"not-the-identifier","future":{"sequence":9007199254740993,"empty":null}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{` + identifier + tc.fields + `}`)
			before := string(raw)
			// These strings pass the pinned field schema. Query-context and
			// structural callers must not acquire entry-specific semantics.
			if err := ValidateARDEntryFields(raw); err != nil {
				t.Fatalf("valid field shapes rejected: %v", err)
			}
			if err := ValidateARDSearchEntry(raw); (err == nil) != tc.searchOK {
				t.Fatalf("Search valid=%t, want %t: %v", err == nil, tc.searchOK, err)
			}
			complete := []byte(`{` + identifier + tc.fields + `,"displayName":"Agent","url":"https://example.com/agent"}`)
			if err := ValidateARDEntry(complete); (err == nil) != tc.completeOK {
				t.Fatalf("complete valid=%t, want %t: %v", err == nil, tc.completeOK, err)
			}
			if string(raw) != before {
				t.Fatal("validation changed original sparse evidence")
			}
		})
	}
}
