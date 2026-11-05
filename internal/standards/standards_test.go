package standards

import (
	"encoding/json"
	"strings"
	"testing"

	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/schemas"
)

func TestPinnedARDConformanceFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		read       func() ([]byte, error)
		entryCount int
	}{
		{name: "service-publication", read: func() ([]byte, error) { return serviceapi.Files.ReadFile("examples/ard-manifest.json") }, entryCount: 1},
		{name: "official-basic", read: func() ([]byte, error) {
			return schemas.Files.ReadFile("upstream/ard/ard-0.91-aa3e598/conformance/examples/basic/ard.json")
		}, entryCount: 4},
		{name: "official-fda-ndc", read: func() ([]byte, error) {
			return schemas.Files.ReadFile("upstream/ard/ard-0.91-aa3e598/conformance/examples/fda-ndc/fda-ndc-catalog.json")
		}, entryCount: 1},
		{name: "official-local-business", read: func() ([]byte, error) {
			return schemas.Files.ReadFile("upstream/ard/ard-0.91-aa3e598/conformance/examples/local-business/local-business-catalog.json")
		}, entryCount: 2},
		{name: "official-noaa-weather", read: func() ([]byte, error) {
			return schemas.Files.ReadFile("upstream/ard/ard-0.91-aa3e598/conformance/examples/noaa-weather/noaa-weather-catalog.json")
		}, entryCount: 1},
	}
	if len(tests) != 5 {
		t.Fatalf("ARD fixture matrix contains %d fixtures, want 5", len(tests))
	}
	seen := make(map[string]struct{}, len(tests))
	for _, test := range tests {
		if _, duplicate := seen[test.name]; duplicate {
			t.Fatalf("duplicate ARD fixture name %q", test.name)
		}
		seen[test.name] = struct{}{}
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw, err := test.read()
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateARDManifest(raw); err != nil {
				t.Fatalf("manifest does not match the pinned ARD schema: %v", err)
			}
			var manifest struct {
				Entries []json.RawMessage `json:"entries"`
			}
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			if len(manifest.Entries) != test.entryCount {
				t.Fatalf("fixture has %d entries, want %d", len(manifest.Entries), test.entryCount)
			}
			for index, entry := range manifest.Entries {
				if err := ValidateARDEntry(entry); err != nil {
					t.Fatalf("entry %d does not match the pinned ARD schema and service semantics: %v", index, err)
				}
			}
		})
	}
}

func TestPinnedARDAndMCPSchemas(t *testing.T) {
	ard := []byte(`{"identifier":"urn:air:example.com:agent:weather","displayName":"Weather Agent","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather"}`)
	if err := ValidateARDEntry(ard); err != nil {
		t.Fatalf("valid ARD entry: %v", err)
	}
	if err := ValidateARDEntry([]byte(`{"identifier":"not-an-air-urn","displayName":"Weather Agent","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather"}`)); err == nil {
		t.Fatal("invalid ARD identifier passed the pinned schema")
	}
	if err := ValidateARDEntry([]byte(`{"identifier":"urn:air:not-a-domain:agent:weather","displayName":"Weather Agent","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather"}`)); err == nil {
		t.Fatal("ARD identifier with a non-domain publisher passed semantic validation")
	}
	if err := ValidateARDEntry([]byte(`{"identifier":"urn:air:example.com:agent:one","identifier":"urn:air:example.com:agent:two","displayName":"Weather Agent","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather"}`)); err == nil {
		t.Fatal("ARD entry with a duplicate member passed validation")
	}
	if err := ValidateARDManifest([]byte(`{"entries":[],"entries":[]}`)); err == nil {
		t.Fatal("ARD manifest with a duplicate member passed validation")
	}
	if err := ValidateARDEntry([]byte(`{"identifier":"urn:air:example.com:agent:weather","displayName":"Weather Agent","type":"not-a-media-type","url":"https://agents.example.com/weather"}`)); err == nil {
		t.Fatal("ARD entry with a non-media type passed semantic validation")
	}
	mcpCard := []byte(`{"$schema":"https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json","name":"com.example/weather","description":"Provides weather tools.","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://mcp.example.com/weather"}]}`)
	if err := ValidateMCPServerCard(mcpCard); err != nil {
		t.Fatalf("valid MCP Server Card: %v", err)
	}
	if err := ValidateMCPServerCard([]byte(`{"name":"Weather"}`)); err == nil {
		t.Fatal("invalid MCP Server Card passed the pinned schema")
	}
	mcpRegistry := []byte(`{"name":"com.example/weather","description":"Provides weather tools.","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://mcp.example.com/weather"}]}`)
	if err := ValidateMCPRegistryServer(mcpRegistry); err != nil {
		t.Fatalf("valid MCP Registry server.json: %v", err)
	}
}

func TestARDJSONLDExpansionUsesBaseInlineAndRemoteContexts(t *testing.T) {
	inline := []byte(`{
    "@context": {
      "label": "ard:displayName",
      "artifactType": "ard:mediaType",
      "artifactUrl": {"@id":"ard:url","@type":"@id"}
    },
    "identifier":"urn:air:example.com:agent:weather",
    "label":"Weather Agent",
    "artifactType":"application/a2a-agent-card+json",
    "artifactUrl":"https://agents.example.com/weather"
  }`)
	normalized, err := NormalizeARDEntry(inline, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateARDEntry(normalized); err != nil {
		t.Fatalf("normalized inline-context entry: %s: %v", normalized, err)
	}
	var entry map[string]any
	if err := json.Unmarshal(normalized, &entry); err != nil {
		t.Fatal(err)
	}
	if entry["displayName"] != "Weather Agent" || entry["type"] != "application/a2a-agent-card+json" || entry["url"] != "https://agents.example.com/weather" {
		t.Fatalf("canonical terms were not recovered: %#v", entry)
	}

	remote := []byte(`{"@context":"https://contexts.example.com/weather","identifier":"urn:air:example.com:agent:weather","label":"Weather Agent","artifactType":"application/a2a-agent-card+json","artifactUrl":"https://agents.example.com/weather"}`)
	fetches := 0
	normalized, err = NormalizeARDEntry(remote, func(uri string) ([]byte, error) {
		fetches++
		if uri != "https://contexts.example.com/weather" {
			t.Fatalf("unexpected context URL %q", uri)
		}
		return []byte(`{"@context":{"label":"https://agenticresourcediscovery.org/ns#displayName","artifactType":"https://agenticresourcediscovery.org/ns#mediaType","artifactUrl":{"@id":"https://agenticresourcediscovery.org/ns#url","@type":"@id"}}}`), nil
	})
	if err != nil || fetches != 1 {
		t.Fatalf("remote context expansion = %s, fetches=%d, err=%v", normalized, fetches, err)
	}
	if err := ValidateARDEntry(normalized); err != nil {
		t.Fatalf("normalized remote-context entry: %s: %v", normalized, err)
	}
}

func TestARDTrustBindingAndQueryTermResolution(t *testing.T) {
	valid := []byte(`{"identifier":"urn:air:example.com:agent:weather","displayName":"Weather Agent","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather","trustManifest":{"identity":"spiffe://workloads.example.com/weather"}}`)
	if err := ValidateARDEntry(valid); err != nil {
		t.Fatalf("aligned trust identity: %v", err)
	}
	oneSegment := []byte(`{"identifier":"urn:air:example.com:weather","displayName":"Weather Agent","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather","trustManifest":{"identity":"did:web:agents.example.com"}}`)
	if err := ValidateARDEntry(oneSegment); err != nil {
		t.Fatalf("schema-compatible one-segment identity with aligned trust: %v", err)
	}
	upperCase := []byte(`{"identifier":"urn:air:example.com:weather","displayName":"Weather Agent","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather","TrustManifest":{"identity":"did:web:agents.example.com"}}`)
	if err := ValidateARDEntry(upperCase); err != nil {
		t.Fatalf("schema-cased Trust Manifest was rejected: %v", err)
	}
	normalizedUpper, err := NormalizeARDEntry(upperCase, nil)
	if err != nil || !strings.Contains(string(normalizedUpper), `"trustManifest"`) || strings.Contains(string(normalizedUpper), `"TrustManifest"`) || !strings.Contains(string(upperCase), `"TrustManifest"`) {
		t.Fatalf("Trust Manifest normalization did not preserve raw input and emit lower-case: raw=%s normalized=%s err=%v", upperCase, normalizedUpper, err)
	}
	bothCases := []byte(`{"identifier":"urn:air:example.com:weather","displayName":"Weather Agent","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather","trustManifest":{"identity":"did:web:example.com"},"TrustManifest":{"identity":"did:web:example.com"}}`)
	if err := ValidateARDEntry(bothCases); err == nil {
		t.Fatal("ambiguous dual-spelling Trust Manifest passed semantic validation")
	}
	if _, err := NormalizeARDEntry(bothCases, nil); err == nil {
		t.Fatal("ambiguous dual-spelling Trust Manifest passed JSON-LD normalization")
	}
	invalid := strings.Replace(string(valid), "workloads.example.com", "attacker.test", 1)
	if err := ValidateARDEntry([]byte(invalid)); err == nil {
		t.Fatal("misaligned trust identity passed semantic validation")
	}
	mismatchedID := []byte(`{"@id":"https://agents.example.com/weather","identifier":"urn:air:example.com:agent:weather","displayName":"Weather Agent","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather"}`)
	if err := ValidateARDEntry(mismatchedID); err == nil {
		t.Fatal("unproven ARD @id equivalence passed semantic validation")
	}
	resolved, err := ResolveARDTerm("kind", map[string]any{"kind": "https://agenticresourcediscovery.org/ns#mediaType"})
	if err != nil || resolved != "type" {
		t.Fatalf("resolved term = %q, %v", resolved, err)
	}
}

func TestAICatalogCompatibilityAndUniqueness(t *testing.T) {
	upstreamExample, err := schemas.Files.ReadFile("upstream/ai-catalog/ai-catalog-1.0-b062278/example.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAICatalogLevel2(upstreamExample); err != nil {
		t.Fatalf("pinned AI Catalog example: %v", err)
	}
	compatibleMinor := []byte(`{"specVersion":"1.1","host":{"displayName":"Catalog"},"entries":[{"identifier":"urn:air:example.com:agent:weather","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather","futureField":true}],"futureTopLevel":true}`)
	if err := ValidateAICatalog(compatibleMinor); err != nil {
		t.Fatalf("compatible minor version was rejected: %v", err)
	}
	if err := ValidateAICatalogLevel2(compatibleMinor); err == nil || !strings.Contains(err.Error(), "specVersion") {
		t.Fatalf("producer must reject a version beyond its implemented 1.0 profile: %v", err)
	}
	hostless := []byte(`{"specVersion":"1.0","entries":[]}`)
	if err := ValidateAICatalog(hostless); err != nil {
		t.Fatalf("Level 1 does not require a host: %v", err)
	}
	if err := ValidateAICatalogLevel2(hostless); err == nil || !strings.Contains(err.Error(), "host") {
		t.Fatalf("Level 2 must require a host independently of version: %v", err)
	}
	duplicate := []byte(`{"specVersion":"1.0","entries":[{"identifier":"urn:air:example.com:agent:weather","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather"},{"identifier":"urn:air:example.com:agent:weather","version":"2.0.0","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather-v2"}]}`)
	if err := ValidateAICatalog(duplicate); err == nil {
		t.Fatal("versioned entry was allowed to collide with an unversioned identifier")
	}
	validLevel2 := []byte(`{"specVersion":"1.0","host":{"displayName":"Example Catalog","identifier":"https://catalog.example.com"},"entries":[]}`)
	if err := ValidateAICatalogLevel2(validLevel2); err != nil {
		t.Fatalf("valid Level 2 document: %v", err)
	}
}

func TestAICatalogTrustAndExtensionValidation(t *testing.T) {
	digest := strings.Repeat("a", 64)
	valid := []byte(`{
    "specVersion":"1.0",
    "host":{"displayName":"Example Catalog","identifier":"https://catalog.example.com","logoUrl":"data:image/png;base64,AA=="},
    "extensions":{"com.example.catalogProfile":{"name":"public"}},
    "entries":[{
      "identifier":"urn:air:example.com:agent:weather",
      "type":"application/a2a-agent-card+json",
      "url":"https://agents.example.com/weather",
      "trustManifest":{
        "identity":"did:web:example.com:agents:weather",
        "subject":{"url":"https://agents.example.com/weather","type":"application/a2a-agent-card+json","digest":"sha256:` + digest + `"},
        "issuedAt":"2026-08-29T12:00:00Z",
        "signature":"eyJhbGciOiJFUzI1NiJ9..c2ln"
      }
    }]
  }`)
	if err := ValidateAICatalogLevel2(valid); err != nil {
		t.Fatalf("valid trusted Level 2 Catalog: %v", err)
	}
	for name, document := range map[string][]byte{
		"unqualified extension": []byte(strings.Replace(string(valid), `"com.example.catalogProfile"`, `"custom"`, 1)),
		"publisher mismatch":    []byte(strings.Replace(string(valid), `did:web:example.com:agents:weather`, `did:web:attacker.test:agents:weather`, 1)),
		"subject mismatch":      []byte(strings.Replace(string(valid), `"type":"application/a2a-agent-card+json","digest"`, `"type":"application/mcp-server-card+json","digest"`, 1)),
		"weak digest":           []byte(strings.Replace(string(valid), `sha256:`+digest, `sha1:`+strings.Repeat("a", 40), 1)),
		"unsafe algorithm":      []byte(strings.Replace(string(valid), `eyJhbGciOiJFUzI1NiJ9`, `eyJhbGciOiJIUzI1NiJ9`, 1)),
	} {
		if err := ValidateAICatalog(document); err == nil {
			t.Fatalf("%s passed AI Catalog validation", name)
		}
	}
	nonSubstantive := []byte(`{"specVersion":"1.0","entries":[{"identifier":"urn:air:example.com:agent:weather","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather","trustManifest":{"identity":"did:web:example.com"}}]}`)
	if err := ValidateAICatalog(nonSubstantive); err == nil {
		t.Fatal("non-substantive AI Catalog Trust Manifest was accepted")
	}
}

func TestAICatalogTrustSchemaAndProvenanceValidation(t *testing.T) {
	t.Parallel()

	digest := "sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name     string
		validate func(json.RawMessage) error
		raw      string
		wantErr  bool
	}{
		{name: "trust schema", validate: validateAICatalogTrustSchema, raw: `{"identifier":"urn:example:trust","version":"1.0","governanceUri":"https://example.com/governance","verificationMethods":["manual"]}`},
		{name: "trust schema object", validate: validateAICatalogTrustSchema, raw: `[]`, wantErr: true},
		{name: "trust schema required field", validate: validateAICatalogTrustSchema, raw: `{"identifier":"urn:example:trust"}`, wantErr: true},
		{name: "trust schema governance URI", validate: validateAICatalogTrustSchema, raw: `{"identifier":"urn:example:trust","version":"1.0","governanceUri":"relative"}`, wantErr: true},
		{name: "trust schema verification methods", validate: validateAICatalogTrustSchema, raw: `{"identifier":"urn:example:trust","version":"1.0","verificationMethods":[1]}`, wantErr: true},
		{name: "provenance", validate: validateAICatalogProvenance, raw: `[{"relation":"derivedFrom","sourceId":"source-1","sourceDigest":"` + digest + `","signatureRef":"signature-1","registryUri":"https://registry.example.com","statementUri":"https://example.com/statement"}]`},
		{name: "provenance array", validate: validateAICatalogProvenance, raw: `{}`, wantErr: true},
		{name: "provenance required field", validate: validateAICatalogProvenance, raw: `[{"sourceId":"source-1"}]`, wantErr: true},
		{name: "provenance optional string", validate: validateAICatalogProvenance, raw: `[{"relation":"derivedFrom","sourceId":"source-1","signatureRef":""}]`, wantErr: true},
		{name: "provenance digest", validate: validateAICatalogProvenance, raw: `[{"relation":"derivedFrom","sourceId":"source-1","sourceDigest":"sha1:bad"}]`, wantErr: true},
		{name: "provenance URI", validate: validateAICatalogProvenance, raw: `[{"relation":"derivedFrom","sourceId":"source-1","statementUri":"relative"}]`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.validate(json.RawMessage(test.raw))
			if (err != nil) != test.wantErr {
				t.Fatalf("validation error = %v, want error %t", err, test.wantErr)
			}
		})
	}
}

func FuzzNormalizeARDEntry(f *testing.F) {
	f.Add([]byte(`{"identifier":"urn:air:example.com:agent:weather","type":"application/json; charset"}`))
	f.Add([]byte(`{"identifier":"urn:air:example.com:agent:weather","@id":"urn:air:other.example:agent:weather"}`))
	f.Add([]byte(`{"identifier":"urn:air:example.com:agent:weather","@id":"urn:air:example.com:agent:weather","type":"application/vnd.example.card+json; version=1","extension":{"sequence":9007199254740993}}`))
	f.Add([]byte(`{"identifier":"urn:air:example.com:agent:weather","displayName":"Weather Agent","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather"}`))
	f.Add([]byte(`{"identifier":"urn:air:example.com:agent:weather","displayName":"Weather Agent","type":"application/json","data":{},"trustManifest":{"identity":"https://example.com","future":{"sequence":9007199254740993}}}`))
	f.Add([]byte(`{"identifier":"urn:air:example.com:agent:weather","displayName":"Weather Agent","type":"application/json","data":{},"TrustManifest":{"identity":"https://example.com","attestations":[{"type":"custom","uri":17}]}}`))
	f.Add([]byte(`{"@context":{"@version":1.1,"opaque":{"@id":"https://example.com/opaque","@type":"@json"}},"identifier":"urn:air:example.com:agent:weather","displayName":"Weather Agent","type":"application/json","data":{"sequence":9007199254740993},"opaque":{"@context":{"@version":1.10},"sequence":9007199254740993}}`))
	f.Add([]byte(`{"@context":{"kind":"https://agenticresourcediscovery.org/ns#mediaType"},"identifier":"urn:air:example.com:agent:weather","displayName":"Weather Agent","kind":"application/a2a-agent-card+json","url":"https://agents.example.com/weather"}`))
	f.Add([]byte(`{"@context":{"topic.example":"http://purl.org/dc/terms/"},"identifier":"urn:air:example.com:agent:weather","topic.example:subject":["weather","forecast"]}`))
	f.Add([]byte(`{"http://purl.org/dc/terms/subject":"weather","@context":[{"topic":{"@id":"http://purl.org/dc/terms/subject","@protected":true}},{"topic":"https://other.example/subject"}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64*1024 {
			t.Skip()
		}
		_ = ValidateARDEntry(raw)
		_ = ValidateARDSearchEntry(raw)
		if normalized, err := NormalizeARDEntry(raw, nil); err == nil {
			_ = ValidateARDEntry(normalized)
			_ = ValidateARDSearchEntry(normalized)
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil {
			_, _, _ = ARDSubjectValues(fields)
		}
	})
}

func FuzzValidateAICatalog(f *testing.F) {
	f.Add([]byte(`{"specVersion":"1.0","entries":[]}`))
	f.Add([]byte(`{"specVersion":"1.1","entries":[{"identifier":"urn:air:example.com:agent:weather","type":"application/a2a-agent-card+json","url":"https://agents.example.com/weather"}]}`))
	f.Add([]byte(`{"specVersion":"1.0","host":{"displayName":"Catalog"},"entries":null}`))
	f.Add([]byte(`{"specVersion":"1.0","host":{"displayName":"Catalog"},"entries":[{"identifier":"urn:air:example.com:agent:weather","type":"application/json","data":null,"tags":[null],"trustManifest":{"identity":"did:web:example.com","trustSchema":{"identifier":"urn:trust:example","version":"1.0","verificationMethods":["manual"]}}}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64*1024 {
			t.Skip()
		}
		_ = ValidateAICatalog(raw)
		_ = ValidateAICatalogLevel2(raw)
	})
}
