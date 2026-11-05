// AI Catalog document tests enforce pinned field types and preserve opaque JSON.
package standards

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestAICatalogRejectsNullTypedFields checks optional field types against the
// pinned AI Catalog prose and CDDL through both public validators.
func TestAICatalogRejectsNullTypedFields(t *testing.T) {
	t.Parallel()

	const valid = `{
		"specVersion":"1.0","host":{"displayName":"Catalog"},
		"entries":[{
			"identifier":"urn:air:example.com:agent:weather","type":"application/json","data":{},
			"displayName":"Weather","description":"Forecasts","tags":["weather"],
			"publisher":{"identifier":"https://example.com","displayName":"Example","identityType":"domain"},
			"trustManifest":{
				"identity":"did:web:example.com","identityType":"did",
				"trustSchema":{"identifier":"urn:trust:example","version":"1.0","verificationMethods":["manual"]},
				"attestations":[{"type":"review","uri":"https://example.com/review","size":0,"description":"Reviewed"}],
				"provenance":[{"relation":"derivedFrom","sourceId":"source-1"}]
			}
		}]
	}`
	tests := []struct {
		name, before, after, field string
	}{
		{"display name", `"displayName":"Weather"`, `"displayName":null`, "displayName"},
		{"description", `"description":"Forecasts"`, `"description":null`, "description"},
		{"tags", `"tags":["weather"]`, `"tags":null`, "tags"},
		{"tag element", `"tags":["weather"]`, `"tags":["weather",null]`, "tags"},
		{"publisher identity type", `"identityType":"domain"`, `"identityType":null`, "publisher.identityType"},
		{"trust identity type", `"identityType":"did"`, `"identityType":null`, "identityType"},
		{"verification methods", `"verificationMethods":["manual"]`, `"verificationMethods":null`, "verificationMethods"},
		{"verification method element", `"verificationMethods":["manual"]`, `"verificationMethods":["manual",null]`, "verificationMethods"},
		{"attestations", `"attestations":[{"type":"review","uri":"https://example.com/review","size":0,"description":"Reviewed"}]`, `"attestations":null`, "attestations"},
		{"attestation element", `"attestations":[{"type":"review","uri":"https://example.com/review","size":0,"description":"Reviewed"}]`, `"attestations":[null]`, "attestations"},
		{"provenance", `"provenance":[{"relation":"derivedFrom","sourceId":"source-1"}]`, `"provenance":null`, "provenance"},
		{"provenance element", `"provenance":[{"relation":"derivedFrom","sourceId":"source-1"}]`, `"provenance":[null]`, "provenance"},
		{"attestation size", `"size":0`, `"size":null`, "size"},
		{"attestation description", `"description":"Reviewed"`, `"description":null`, "description"},
	}
	for name, validate := range map[string]func([]byte) error{
		"consumer": ValidateAICatalog,
		"producer": ValidateAICatalogLevel2,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate([]byte(valid)); err != nil {
				t.Fatalf("valid control: %v", err)
			}
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					if strings.Count(valid, test.before) != 1 {
						t.Fatal("mutation must target exactly one field")
					}
					raw := strings.Replace(valid, test.before, test.after, 1)
					if err := validate([]byte(raw)); err == nil || !strings.Contains(err.Error(), test.field) {
						t.Fatalf("null %s must fail with field context: %v", test.field, err)
					}
				})
			}
			for _, entries := range []string{`null`, ` null `} {
				raw := `{"specVersion":"1.0","host":{"displayName":"Catalog"},"entries":` + entries + `}`
				if err := validate([]byte(raw)); err == nil || !strings.Contains(err.Error(), "entries") {
					t.Fatalf("null entries must fail with field context: %v", err)
				}
			}
			if err := validate([]byte(`{"specVersion":"1.0","host":{"displayName":"Catalog"},"entries":[]}`)); err != nil {
				t.Fatalf("empty entries must remain valid: %v", err)
			}
		})
	}
}

// TestAICatalogPreservesEmptyTypedAndOpaqueValues prevents typed-field checks
// from restricting the specification's opaque JSON boundaries.
func TestAICatalogPreservesEmptyTypedAndOpaqueValues(t *testing.T) {
	t.Parallel()

	// Empty collections remain valid when trustSchema supplies substantive
	// evidence. Unknown fields and extensions are not service control objects.
	const entry = `{"identifier":"urn:air:example.com:agent:weather","type":"application/json",
		"displayName":"","description":"","tags":[""],
		"publisher":{"identifier":"https://example.com","displayName":"Example","identityType":""},
		"trustManifest":{"identity":"did:web:example.com","identityType":"",
			"trustSchema":{"identifier":"urn:trust:example","version":"1.0","verificationMethods":[]},
			"attestations":[],"provenance":[],"futureTrustField":null,
			"extensions":{"com.example.trust":null}},
		"extensions":{"com.example.metadata":null},"futureEntryField":null,"data":`
	for _, value := range []string{`null`, `false`, `0`, `"opaque"`, `[]`, `{}`} {
		raw := `{"specVersion":"1.0","host":{"displayName":"Catalog"},"futureTopLevel":null,
			"extensions":{"https://example.com/extensions/metadata":` + value + `},"entries":[` + entry + value + `}]}`
		if err := ValidateAICatalogLevel2([]byte(raw)); err != nil {
			t.Fatalf("opaque JSON %s and empty typed fields must remain valid: %v", value, err)
		}
	}
	emptyTags := aiCatalogWithEntries("[" + strings.Replace(entry, `"tags":[""]`, `"tags":[]`, 1) + `null}]`)
	if err := ValidateAICatalog(emptyTags); err != nil {
		t.Fatalf("empty tags must remain valid: %v", err)
	}
}

// TestAICatalogTrustDocumentPropagation attributes nested evidence failures to
// their containing manifest and field.
func TestAICatalogTrustDocumentPropagation(t *testing.T) {
	t.Parallel()

	const valid = `{"specVersion":"1.0","entries":[{
		"identifier":"urn:air:example.com:agent:weather","type":"application/json","data":{},
		"trustManifest":{"identity":"did:web:example.com",
			"attestations":[{"type":"review","uri":"https://example.com/review"}],
			"provenance":[{"relation":"derivedFrom","sourceId":"source-1"}],
			"issuedAt":"2026-09-19T12:00:00Z","expiresAt":"2026-09-20T12:00:00.123+01:00"}
	} ]}`
	if err := ValidateAICatalog([]byte(valid)); err != nil {
		t.Fatalf("valid control: %v", err)
	}
	for _, test := range []struct{ before, after, field string }{
		{`"type":"review",`, ``, "attestations[0].type"},
		{`"relation":"derivedFrom",`, ``, "provenance[0].relation"},
		{`"issuedAt":"2026-09-19T12:00:00Z"`, `"issuedAt":42`, "issuedAt"},
		{`"expiresAt":"2026-09-20T12:00:00.123+01:00"`, `"expiresAt":false`, "expiresAt"},
	} {
		t.Run(test.field, func(t *testing.T) {
			if strings.Count(valid, test.before) != 1 {
				t.Fatal("mutation must target exactly one field")
			}
			raw := strings.Replace(valid, test.before, test.after, 1)
			if err := ValidateAICatalog([]byte(raw)); err == nil || !strings.Contains(err.Error(), "trustManifest") || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("nested failure must retain trust and field context: %v", err)
			}
		})
	}
}

// TestAICatalogUnboundTrustAndExtensionNamespaces preserves the selected
// AIR-only entry trust binding independently of namespace validation.
func TestAICatalogUnboundTrustAndExtensionNamespaces(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, identifier, identity string
		valid                      bool
	}{
		{"AIR without trust", "urn:air:example.com:agent:weather", "", true},
		{"opaque without trust", "local-weather", "", true},
		{"HTTPS identifier without trust", "https://example.com/weather", "", true},
		{"matching DID", "urn:air:example.com:agent:weather", "did:web:example.com", true},
		{"matching SPIFFE subdomain", "urn:air:example.com:agent:weather", "spiffe://workloads.example.com/agent/weather", true},
		{"matching HTTPS identity", "urn:air:example.com:agent:weather", "https://example.com/identity", true},
		{"mismatched domain", "urn:air:example.com:agent:weather", "did:web:other.example", false},
		{"domain suffix is not a subdomain", "urn:air:example.com:agent:weather", "https://notexample.com/identity", false},
		{"publisher is not an identity suffix", "urn:air:example.com:agent:weather", "https://example.com.attacker.test/identity", false},
		{"unresolvable domain", "urn:air:example.com:agent:weather", "did:key:opaque", false},
		{"opaque cannot borrow publisher authority", "local-weather", "did:web:example.com", false},
		{"HTTPS identifier cannot borrow URL authority", "https://example.com/weather", "https://example.com/identity", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := map[string]any{
				"identifier": tc.identifier, "type": "application/json", "url": "https://example.com/weather.json",
				"publisher": map[string]any{"identifier": "did:web:example.com", "displayName": "Example"},
			}
			if tc.identity != "" {
				entry["trustManifest"] = map[string]any{
					"identity":    tc.identity,
					"trustSchema": map[string]any{"identifier": "urn:trust:example", "version": "1.0"},
				}
			}
			raw, err := json.Marshal(map[string]any{"specVersion": "1.0", "host": map[string]any{"displayName": "Catalog"}, "entries": []any{entry}})
			if err != nil {
				t.Fatal(err)
			}
			for name, validate := range map[string]func([]byte) error{"consumer": ValidateAICatalog, "producer": ValidateAICatalogLevel2} {
				err := validate(raw)
				if (err == nil) != tc.valid || err != nil && !strings.Contains(err.Error(), "trust domain") {
					t.Errorf("%s trust binding: %v, want valid %t", name, err, tc.valid)
				}
			}
		})
	}
}

// Both public levels apply the namespace policy at every extension container.
func TestAICatalogExtensionNamespacesAtEveryLevel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, key string
		valid     bool
	}{
		{"two labels", "com.example", true},
		{"three labels and hyphen", "com.example.weather-data", true},
		{"existing label characters", "COM.Example-2", true},
		{"HTTPS", "https://example.com/extensions/metadata", true},
		{"URN compatibility", "urn:example:metadata", true},
		{"encoded space", "https://example.com/meta%20data", true},
		{"empty", "", false},
		{"unqualified", "metadata", false},
		{"relative URI", "/metadata", false},
		{"empty label", "com..example", false},
		{"leading hyphen", "com.example.-weather", false},
		{"trailing hyphen", "com.example.weather-", false},
		{"label space", "com.example.bad key", false},
		{"leading URI space", " https://example.com/metadata", false},
		{"trailing URI space", "https://example.com/metadata ", false},
		{"interior URI space", "https://example.com/meta data", false},
		{"URI tab", "urn:example:meta\tdata", false},
		{"URI newline", "https://example.com/meta\ndata", false},
		{"URI control", "urn:example:meta\x7fdata", false},
		{"Unicode leading space", "\u00a0https://example.com/metadata", false},
		{"Unicode interior space", "https://example.com/meta\u2003data", false},
	} {
		for _, location := range []string{"document", "entry", "host trust", "entry trust"} {
			t.Run(tc.name+"/"+location, func(t *testing.T) {
				raw := aiCatalogNamespaceFixture(t, location, tc.key)
				before := bytes.Clone(raw)
				for name, validate := range map[string]func([]byte) error{"consumer": ValidateAICatalog, "producer": ValidateAICatalogLevel2} {
					err := validate(raw)
					if (err == nil) != tc.valid || err != nil && !strings.Contains(err.Error(), "extension key") {
						t.Errorf("%s namespace %q: %v, want valid %t", name, tc.key, err, tc.valid)
					}
					if !bytes.Equal(raw, before) {
						t.Fatalf("%s validation rewrote namespace or opaque values", name)
					}
				}
			})
		}
	}
}

func aiCatalogNamespaceFixture(t *testing.T, location, key string) []byte {
	t.Helper()
	entry := map[string]any{"identifier": "urn:air:example.com:agent:weather", "type": "application/json", "data": map[string]any{}}
	host := map[string]any{"displayName": "Catalog"}
	document := map[string]any{"specVersion": "1.0", "host": host, "entries": []any{entry}}
	owner := document
	switch location {
	case "entry":
		owner = entry
	case "host trust", "entry trust":
		owner = map[string]any{"identity": "did:web:example.com", "trustSchema": map[string]any{"identifier": "urn:trust:example", "version": "1.0"}}
		if location == "host trust" {
			host["trustManifest"] = owner
		} else {
			entry["trustManifest"] = owner
		}
	}
	owner["extensions"] = map[string]json.RawMessage{key: json.RawMessage(`{"future":9007199254740993,"nullable":null,"items":[[],{},true,"opaque"]}`)}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
