// Live result admission uses the pinned sparse Search contract, not full-entry
// requirements or Catalog's own response schema as its independent oracle.
package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

// pinnedSearchSchema compiles an unchanged upstream HTTP definition with
// exclusively embedded reference resources. No remote loader is available.
func pinnedSearchSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	const base = "https://raw.githubusercontent.com/ards-project/ard-spec/main/spec/schemas/"
	raw, err := schemas.Files.ReadFile("upstream/ard/ard-0.91-aa3e598/spec/schemas/ard.openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var upstream struct {
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &upstream); err != nil {
		t.Fatal(err)
	}
	_, ok := upstream.Components.Schemas[name]
	if !ok {
		t.Fatalf("missing pinned %s", name)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	compiler.AssertFormat()
	entry, err := schemas.Files.ReadFile("upstream/ard/ard-0.91-aa3e598/spec/schemas/ard-entry.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(entry))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource(base+"ard-entry.schema.json", document); err != nil {
		t.Fatal(err)
	}
	definitions := map[string]any{"components": map[string]any{"schemas": upstream.Components.Schemas}}
	if err := compiler.AddResource(base+"ard.openapi.yaml", definitions); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(base + "ard.openapi.yaml#/components/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestLiveSearchValidatesSparseResultFields(t *testing.T) {
	schema := pinnedSearchSchema(t, "SearchResultItem")
	type resultCase struct {
		name, field, value string
		schemaValid, valid bool
	}
	baseCases := []resultCase{
		{"sparse", "", "", true, true},
		{"extensions", "extension", `{"large":9007199254740993}`, true, true},
		{"score extension", "Score", `1`, true, true},
		{"source extension", "Source", `"https://other.example/search"`, true, true},
		{"identifier extension", "Identifier", `"urn:air:other.example:agent:wrong"`, true, true},
		{"delivery extension", "URL", `{}`, true, true},
		{"scalar metadata", "metadata", `{"region":"EU","count":3,"enabled":true,"missing":null}`, true, true},
		{"metadata object", "metadata", `{"region":{}}`, false, false},
		{"metadata array", "metadata", `{"region":[]}`, false, false},
		{"context boolean", "@context", `true`, false, false},
		{"name number", "displayName", `17`, false, false},
		{"name null", "displayName", `null`, false, false},
		{"capabilities number", "capabilities", `17`, false, false},
		{"description number", "description", `17`, false, false},
		{"updated number", "updatedAt", `17`, false, false},
		{"updated format", "updatedAt", `"not-a-timestamp"`, false, false},
		{"node identity number", "@id", `17`, false, false},
		{"matching node identity", "@id", `"urn:air:peer.example:agent:weather"`, true, true},
		{"mismatched node identity", "@id", `"urn:air:other.example:agent:weather"`, true, false},
		{"empty node identity", "@id", `""`, true, false},
		{"blank node identity", "@id", `" "`, true, false},
		{"unfamiliar type", "type", `"application/vnd.example.card+json"`, true, true},
		{"parameterized type", "type", `"application/vnd.example.card+json; version=1"`, true, true},
		{"quoted type parameter", "type", `"text/x-acme; profile=\"https://schemas.example/card\""`, true, true},
		{"type spelling preserved", "type", `" APPLICATION/VND.EXAMPLE.CARD+JSON; version=1 "`, true, true},
		{"invalid type", "type", `"not-a-media-type"`, true, false},
		{"invalid type parameters", "type", `"application/json; charset"`, true, false},
		{"empty type", "type", `""`, true, false},
		{"blank type", "type", `" "`, true, false},
		{"trust identity number", "TrustManifest", `{"identity":17}`, false, false},
		{"null delivery", "url", `null`, false, false},
		{"null tags", "tags", `null`, false, false},
		{"array data", "data", `[]`, false, false},
		{"integer decimal", "score", `80.0`, true, true},
		{"integer exponent", "score", `8e1`, true, true},
		{"fraction", "score", `80.5`, false, false},
		{"near integer", "score", `80.000000000000000000001`, false, false},
		{"score null", "score", `null`, false, false},
		{"score boolean", "score", `true`, false, false},
		{"score string", "score", `"80"`, false, false},
		{"score range", "score", `101`, false, false},
	}
	// Prose adds publisher alignment; the Catalog also applies the schema's
	// uppercase trust definition to its supported lowercase alias.
	trustCases := []struct {
		name, trust  string
		shape, valid bool
	}{
		{"publisher", `{"identity":"https://peer.example"}`, true, true},
		{"publisher subdomain", `{"identity":"spiffe://workloads.peer.example/agent"}`, true, true},
		{"did publisher", `{"identity":"did:web:peer.example"}`, true, true},
		{"framework extensions", `{"identity":"https://peer.example","attestations":[{"type":"ANS-Registration","uri":"https://peer.example/receipt"}],"future":{"sequence":9007199254740993}}`, true, true},
		{"empty identity", `{"identity":""}`, true, false},
		{"blank identity", `{"identity":"  "}`, true, false},
		{"unrelated identity", `{"identity":"https://unrelated.example"}`, true, false},
		{"deceptive prefix", `{"identity":"https://notpeer.example"}`, true, false},
		{"deceptive suffix", `{"identity":"https://peer.example.attacker.test"}`, true, false},
		{"missing identity", `{}`, false, false},
		{"null identity", `{"identity":null}`, false, false},
		{"null manifest", `null`, false, false},
		{"attestation URI type", `{"identity":"https://peer.example","attestations":[{"type":"custom","uri":17}]}`, false, false},
		{"signature type", `{"identity":"https://peer.example","signature":{}}`, false, false},
		{"framework type", `{"identity":"https://peer.example","trustSchema":[]}`, false, false},
	}
	tests := make([]resultCase, 0, len(baseCases)+2*len(trustCases))
	tests = append(tests, baseCases...)
	for _, spelling := range []string{"trustManifest", "TrustManifest"} {
		for _, tc := range trustCases {
			tests = append(tests, resultCase{spelling + "/" + tc.name, spelling, tc.trust, spelling == "trustManifest" || tc.shape, tc.valid})
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const endpoint = "https://peer.example/search"
			fields := map[string]json.RawMessage{"identifier": json.RawMessage(`"urn:air:peer.example:agent:weather"`), "score": json.RawMessage(`80`), "source": json.RawMessage(`"https://peer.example/search"`)}
			// Place the selected field last so case-colliding extensions cannot
			// pass merely because a later canonical name overwrites them.
			delete(fields, tc.field)
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if tc.field != "" {
				key, err := json.Marshal(tc.field)
				if err != nil {
					t.Fatal(err)
				}
				raw = append(raw[:len(raw)-1], []byte(`,`+string(key)+`:`+tc.value+`}`)...)
			}
			instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if valid := schema.Validate(instance) == nil; valid != tc.schemaValid {
				t.Fatalf("pinned schema valid=%t, want %t", valid, tc.schemaValid)
			}
			const healthy = `{"identifier":"urn:air:peer.example:agent:healthy","score":90,"source":"https://peer.example/search"}`
			outbound := &routingFetcher{results: map[string]fetcher.Result{"POST " + endpoint: {Status: 200, FinalURL: endpoint, MediaType: "application/json", Body: []byte(`{"results":[` + healthy + `,` + string(raw) + `]}`)}}}
			store := NewMemoryStore()
			ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: auditEvidenceTenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
			source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: auditEvidenceTenantID, DisplayName: "Peer", Type: SourceRegistry, Endpoint: endpoint, RegistrySearchURL: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Publishers: []string{"peer.example"}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, FederationMode: "none"}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin"}
			if _, err := store.CreateSource(ctx, source); err != nil {
				t.Fatal(err)
			}
			manager, err := NewManager(store, outbound, clock.NewFake(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)), id.Generator{})
			if err != nil {
				t.Fatal(err)
			}
			response, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "weather", PageSize: 10, Administrative: true, Reason: "Validate Search contract"})
			want := 1
			if tc.valid {
				want = 2
			}
			if err != nil || len(response.Results) != want || response.Partial == tc.valid || len(outbound.requests) != 1 || response.Results[0].Identifier != "urn:air:peer.example:agent:healthy" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			if tc.valid && (string(response.Results[1].Entry) != string(raw) || response.Results[1].Score != 80 || response.Results[1].Identifier != "urn:air:peer.example:agent:weather" || response.Results[1].Source != endpoint) {
				t.Fatal("validated result changed raw evidence or score")
			}
			if !tc.valid {
				invalid := false
				for _, diagnostic := range response.Diagnostics {
					if diagnostic.Status == "invalid_response" {
						invalid = true
						message := "An upstream result was omitted because its present fields did not match the ARD Search contract."
						if tc.field == "score" {
							message = "An upstream result was omitted because its identity, score, source, or delivery was invalid."
						}
						if diagnostic.SourceID != source.ID || diagnostic.Message != message {
							t.Fatalf("unexpected invalid-result diagnostic: %+v", diagnostic)
						}
					}
				}
				if !invalid {
					t.Fatalf("invalid result lacked a sanitized diagnostic: %+v", response.Diagnostics)
				}
			}
		})
	}
}

func TestLiveScoreUsesExactBoundedIntegers(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		score int
		valid bool
	}{
		{"0", 0, true}, {"-0.0e99999999999999999999999", 0, true}, {"100.00", 100, true}, {"8e1", 80, true}, {"8.0e+1", 80, true}, {"8000e-2", 80, true},
		{"-1", 0, false}, {"101", 0, false}, {"0.1", 0, false}, {"80.000000000000000000001", 0, false}, {"1e99999999999999999999", 0, false}, {"1e-99999999999999999999", 0, false}, {"", 0, false}, {`"80"`, 0, false}, {"null", 0, false}, {"80 81", 0, false},
		{"0." + strings.Repeat("0", 256) + "1", 0, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, ok := parseLiveScore([]byte(tc.raw))
			if ok != tc.valid || ok && got != tc.score {
				t.Fatalf("score=%d valid=%t, want %d/%t", got, ok, tc.score, tc.valid)
			}
		})
	}
}
