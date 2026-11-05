// These regressions keep live envelope decoding aligned with the pinned wire
// types and exercise partial failures through the existing federation manager.
package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// TestLiveResponseOptionalTypes compares actual decoding with the public schema;
// an omitted member is valid, but a present member must have its declared type.
func TestLiveResponseOptionalTypes(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	// Appendix D.3 makes upstream OpenAPI authoritative for this envelope
	// and its extensible referrals. Compare both contracts independently.
	upstreamSchema := pinnedSearchSchema(t, "SearchResponse")
	for _, test := range []struct {
		name, body string
		accept     bool
	}{
		{"omitted", `{"results":[]}`, true},
		{"empty referrals", `{"results":[],"referrals":[]}`, true},
		{"opaque token", `{"results":[],"pageToken":"eyJvZmZzZXQiOiAyMH0="}`, true},
		{"empty token", `{"results":[],"pageToken":""}`, true},
		{"valid referral", `{"results":[],"referrals":[{"identifier":"urn:air:example.com:registry:one","displayName":"Registry","type":"application/ai-registry+json","url":"https://example.com/"}]}`, true},
		{"null token", `{"results":[],"pageToken": null }`, false},
		{"null referrals", `{"results":[],"referrals":null}`, false},
		{"numeric token", `{"results":[],"pageToken":20}`, false},
		{"object referrals", `{"results":[],"referrals":{}}`, false},
		{"null results", `{"results":null}`, false},
		{"stale ANS envelope", `{"results":[],"staleSince":"2026-09-02T00:00:00Z"}`, false},
		{"cased results", `{"Results":[]}`, false},
		{"results alias collision", `{"results":[],"RESULTS":[]}`, false},
		{"cased referrals", `{"results":[],"Referrals":[]}`, false},
		{"cased token", `{"results":[],"PageToken":"next"}`, false},
		{"referral extension", `{"results":[],"referrals":[{"identifier":"urn:air:example.com:registry:one","displayName":"Registry","type":"application/ai-registry+json","url":"https://example.com/","extra":true}]}`, true},
		{"referral URL extension", `{"results":[],"referrals":[{"identifier":"urn:air:example.com:registry:one","displayName":"Registry","type":"application/ai-registry+json","url":"https://example.com/","URL":"https://other.example/"}]}`, true},
		{"referral typed extensions", `{"results":[],"referrals":[{"identifier":"urn:air:example.com:registry:one","displayName":"Registry","type":"application/ai-registry+json","url":"https://example.com/","URL":{},"Identifier":42,"DisplayName":false,"Type":[]}]}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(test.body), &value); err != nil {
				t.Fatal(err)
			}
			if err := upstreamSchema.Validate(value); (err == nil) != test.accept {
				t.Fatalf("accept=%v: pinned schema=%v", test.accept, err)
			}
			schemaErr := document.Components.Schemas["ARDSearchResponse"].Value.VisitJSON(value, openapi3.EnableJSONSchema2020())
			decoded, decodeErr := decodeLiveResponse([]byte(test.body))
			if (schemaErr == nil) != test.accept || (decodeErr == nil) != test.accept {
				t.Fatalf("accept=%v: schema=%v decoder=%v", test.accept, schemaErr, decodeErr)
			}
			if test.accept && len(decoded.Referrals) > 0 && decoded.Referrals[0] != (liveReferral{Identifier: "urn:air:example.com:registry:one", DisplayName: "Registry", Type: "application/ai-registry+json", URL: "https://example.com/"}) {
				t.Fatalf("extension changed referral authority: %+v", decoded.Referrals)
			}
		})
	}
}

// TestLivePeerShapeIsolation uses independently authored full-entry responses
// shared by HF/ANS models, plus literal incompatible envelope variants. It does
// not execute or certify those products; source evidence is in the ARD record.
func TestLivePeerShapeIsolation(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	for _, test := range []struct {
		name, suffix string
		valid        bool
		sparse       bool
	}{
		{"fresh response", "", true, false},
		{"sparse publisher-scoped response", "", true, true},
		{"ANS stale response", `,"staleSince":"2026-09-02T00:00:00Z"`, false, false},
		{"cased envelope member", `,"Referrals":[]`, false, false},
		{"Gateway inferred null token", `,"pageToken":null`, false, false},
		{"null referrals", `,"referrals":null`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			outbound := &routingFetcher{results: map[string]fetcher.Result{}}
			expectedEntries := make(map[string]string, 2)
			ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenant, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
			for i := range 2 {
				endpoint := fmt.Sprintf("https://peer%d.example/search", i)
				source := Source{ID: fmt.Sprintf("00000000-0000-7000-8000-%012d", i+10), TenantID: tenant, DisplayName: "Peer", Type: SourceRegistry, Endpoint: endpoint, RegistrySearchURL: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, FederationMode: "none"}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin"}
				if test.sparse && i == 1 {
					source.Scope = Scope{Publishers: []string{"peer1.example"}}
				}
				if _, err := store.CreateSource(ctx, source); err != nil {
					t.Fatal(err)
				}
				suffix := ""
				if i == 1 {
					suffix = test.suffix
				}
				entry := fmt.Sprintf(`{"identifier":"urn:air:peer%d.example:agent:weather","displayName":"Weather Agent","type":"application/a2a-agent-card+json","data":{"name":"Weather Agent"},"score":80,"source":%q,"exampleExtension":{"note":"retain exactly"}}`, i, endpoint)
				if test.sparse && i == 1 {
					entry = fmt.Sprintf(`{"identifier":"urn:air:peer1.example:agent:weather","score":80,"source":%q,"exampleExtension":{"note":"retain exactly"}}`, endpoint)
				}
				expectedEntries[endpoint] = entry
				body := `{"results":[` + entry + `]` + suffix + `}`
				outbound.results["POST "+endpoint] = fetcher.Result{Status: 200, FinalURL: endpoint, MediaType: "application/json", Body: []byte(body)}
			}
			manager, err := NewManager(store, outbound, clock.NewFake(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)), id.Generator{})
			if err != nil {
				t.Fatal(err)
			}
			response, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "weather", PageSize: 10, Administrative: true, Reason: "Check peer response isolation"})
			want := 1
			if test.valid {
				want = 2
			}
			if err != nil || len(response.Results) != want || response.Partial == test.valid || len(response.Diagnostics) != 2 {
				t.Fatalf("response=%#v error=%v", response, err)
			}
			if !slices.ContainsFunc(response.Results, func(result LiveResult) bool {
				return result.Source == "https://peer0.example/search" && result.RespondingURL == result.Source
			}) {
				t.Fatalf("healthy peer provenance lost: %#v", response.Results)
			}
			if !test.valid && !slices.ContainsFunc(response.Diagnostics, func(d LiveDiagnostic) bool {
				return d.Status == "invalid_response" && d.SourceID == "00000000-0000-7000-8000-000000000011"
			}) {
				t.Fatalf("missing invalid peer diagnostic: %#v", response.Diagnostics)
			}
			for _, result := range response.Results {
				if string(result.Entry) != expectedEntries[result.Source] || result.Score != 80 {
					t.Fatalf("peer metadata or extensions were rewritten: %#v", result)
				}
				if test.sparse && result.Source == "https://peer1.example/search" {
					if _, ok := catalogLiveResult(result, catalog.SearchQuery{}); ok {
						t.Fatal("type-less ARD result became a supported tenant Catalog projection")
					}
					if scopeMatches(Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, sourceEntry{Identifier: result.Identifier}) {
						t.Fatal("type-less result passed a kind-scoped source")
					}
				}
			}
			if len(outbound.requests) != 2 {
				t.Fatalf("unexpected fetches: %d", len(outbound.requests))
			}
			for _, request := range outbound.requests {
				var body map[string]any
				if err := json.Unmarshal(request.Body, &body); err != nil {
					t.Fatal(err)
				}
				want := map[string]any{"query": map[string]any{"text": "weather"}, "federation": "none", "pageSize": float64(10)}
				if !reflect.DeepEqual(body, want) {
					t.Fatalf("unexpected outbound query: %s", request.Body)
				}
			}
		})
	}
}

// FuzzLiveResponseOptionalTypes guards null/type distinctions under arbitrary
// JSON encodings while leaving item validation to the existing manager tests.
func FuzzLiveResponseOptionalTypes(f *testing.F) {
	for _, seed := range []string{`{"results":[]}`, `{"results":[],"pageToken":null}`, `{"results":[],"referrals":null}`, `{"results":[],"referrals":[]}`, `{"results":[],"pageToken":"next"}`, `{"results":[],"results":[]}`} {
		f.Add(seed)
	}
	f.Add(`{"results":[{"data":{"opaque":1e1000}}]}`)
	f.Fuzz(func(t *testing.T, body string) {
		if _, err := decodeLiveResponse([]byte(body)); err != nil {
			return
		}
		var fields map[string]any
		decoder := json.NewDecoder(strings.NewReader(body))
		decoder.UseNumber() // Native result members are opaque to this envelope oracle.
		if err := decoder.Decode(&fields); err != nil {
			t.Fatal(err)
		}
		if value, present := fields["pageToken"]; present {
			if _, ok := value.(string); !ok {
				t.Fatal("accepted non-string token")
			}
		}
		if value, present := fields["referrals"]; present {
			if _, ok := value.([]any); !ok {
				t.Fatal("accepted non-array referrals")
			}
		}
	})
}
