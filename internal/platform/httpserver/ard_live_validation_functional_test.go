//go:build functional

// These contract tests compose real publication and federation services with an
// injected peer transport; no hosted Catalog or artifact is contacted.
package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func newARDLiveSearchFixture(t *testing.T, entry json.RawMessage, options ...federation.ManagerOption) (*Server, *countingARDSearchFetcher) {
	t.Helper()
	return newARDLiveSearchFixtureWithSemantic(t, entry, nil, options...)
}

func newARDLiveSearchFixtureWithSemantic(t *testing.T, entry json.RawMessage, semantic *catalog.SemanticSearch, options ...federation.ManagerOption) (*Server, *countingARDSearchFetcher) {
	t.Helper()
	const tenant = "00000000-0000-7000-8000-000000000001"
	const host = "127.0.0.1"
	const endpoint = "https://peer.example/search"
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	key := sha256.Sum256([]byte("isolated ARD contract fixture"))
	localRecord := conformingARDSearchRecord(tenant, "healthy", 0, now)
	localRecord.VersionRevision.Representations[0].Data = json.RawMessage(`{"name":"healthy","defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],"skills":[{"id":"weather","tags":["weather"]}],"capabilities":{"streaming":false}}`)
	pub, err := publication.NewManager(publication.Options{Semantic: semantic, Records: fixedPublicationRecordSource{tenantID: tenant, records: []catalog.CatalogRecord{localRecord}}, Store: publication.NewMemoryStore(), Clock: clock.NewFake(now), IDs: id.Generator{}, BaseURL: "https://127.0.0.1", PublisherDomain: "catalog.example.com", ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278", CursorKey: key[:], Host: host, Audience: publication.AudienceAnonymous})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pub.BuildCandidate(publicationFixtureContext(t, tenant), tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pub.Activate(publicationFixtureContext(t, tenant), tenant, snapshot.ID, "Activate contract fixture"); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	fetches := &countingARDSearchFetcher{responses: routedFetcher{http.MethodPost + " " + endpoint: fetcher.Result{Status: 200, FinalURL: endpoint, MediaType: "application/json", Body: []byte(`{"results":[` + string(entry) + `]}`), PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: digest}}}
	store := federation.NewMemoryStore()
	source := federation.Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenant, DisplayName: "Peer", Type: federation.SourceRegistry, Endpoint: endpoint, RegistrySearchURL: endpoint, Status: federation.SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: federation.RetrievalLive, Scope: federation.Scope{Publishers: []string{"peer.example"}}, UpdateMode: federation.UpdateManual, Live: federation.LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true, FederationMode: "none"}, DiscoveryAssignments: []federation.DiscoveryAssignment{{Host: host, Audience: federation.AudienceAnonymous}}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: digest, Revision: 1, CreatedBy: "test", CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	options = append(options, federation.WithDiscoveryTargets([]federation.DiscoveryTarget{{Host: host, TenantID: tenant, Audience: federation.AudienceAnonymous}}))
	fed, err := federation.NewManager(store, fetches, clock.NewFake(now), id.Generator{}, options...)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{now: time.Now, federation: fed, discovery: map[string]DiscoveryHost{host: {Host: host, TenantID: tenant, Mode: DiscoveryAnonymous, PublisherDomain: "catalog.example.com", Publication: pub}}, maxBodyBytes: 1 << 20, logger: slog.New(slog.DiscardHandler)}, fetches
}

func TestARDAutoPeerPagingPreservesLocalContinuation(t *testing.T) {
	const peerID = "urn:air:peer.example:agent:weather"
	entry := json.RawMessage(`{"identifier":"` + peerID + `","score":100,"source":"https://peer.example/search"}`)
	server, fetches := newARDLiveSearchFixture(t, entry, federation.WithLiveSearchPages(3))
	fetches.page = func(request fetcher.Request, result fetcher.Result) fetcher.Result {
		var wire struct {
			PageToken string `json:"pageToken"`
		}
		if err := json.Unmarshal(request.Body, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.PageToken == "" {
			result.Body = []byte(`{"results":[],"pageToken":"opaque-peer-next"}`)
		} else if wire.PageToken != "opaque-peer-next" || !request.RejectRedirects {
			t.Fatal("continuation lost its token/redirect boundary", request)
		}
		return result
	}
	query := publication.SearchRequest{Query: publication.SearchQuery{Text: "resumable"}, Federation: "auto", PageSize: 1}
	first := callARDSearch(t, server, query)
	requireStatus(t, first, http.StatusOK)
	var firstPage publication.SearchResponse
	decodeResult(t, first, &firstPage)
	if len(firstPage.Results) != 1 || firstPage.Results[0].Identifier != peerID || firstPage.PageToken == "" || fetches.calls != 2 || strings.Contains(first.header.Get("Server-Timing"), "partial") {
		t.Fatalf("first=%+v calls=%d headers=%v", firstPage, fetches.calls, first.header)
	}
	query.PageToken = firstPage.PageToken
	second := callARDSearch(t, server, query)
	requireStatus(t, second, http.StatusOK)
	var secondPage publication.SearchResponse
	decodeResult(t, second, &secondPage)
	if len(secondPage.Results) != 1 || secondPage.Results[0].Identifier != "urn:air:catalog.example.com:agent:healthy" || secondPage.PageToken != "" || fetches.calls != 2 {
		t.Fatalf("second=%+v calls=%d", secondPage, fetches.calls)
	}
	if firstPage.PageToken == "opaque-peer-next" {
		t.Fatal("peer token escaped into client continuation")
	}
}

func TestARDAutoContinuationFailurePreservesLocalAndEarlierPeerResults(t *testing.T) {
	const peerID = "urn:air:peer.example:agent:weather"
	const token = "private-continuation-sentinel"
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		partial bool
	}{
		{"complete control", http.StatusOK, `{"results":[{"identifier":"urn:air:peer.example:agent:other","score":80,"source":"https://peer.example/search"}]}`, false},
		{"later peer outage", http.StatusServiceUnavailable, `{"private":"upstream-failure-sentinel"}`, true},
		{"malformed later page", http.StatusOK, `{"results":null,"private":"upstream-failure-sentinel"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := json.RawMessage(`{"identifier":"` + peerID + `","score":100,"source":"https://peer.example/search"}`)
			server, fetches := newARDLiveSearchFixture(t, entry, federation.WithLiveSearchPages(3))
			fetches.page = func(request fetcher.Request, result fetcher.Result) fetcher.Result {
				var wire struct {
					PageToken string `json:"pageToken"`
				}
				if err := json.Unmarshal(request.Body, &wire); err != nil {
					t.Fatal(err)
				}
				if wire.PageToken == "" {
					result.Body = []byte(`{"results":[` + string(entry) + `],"pageToken":"` + token + `"}`)
				} else {
					if wire.PageToken != token || !request.RejectRedirects {
						t.Fatal("continuation changed its token or redirect boundary", request)
					}
					result.Status, result.Body = tc.status, []byte(tc.body)
				}
				return result
			}
			response := callARDSearch(t, server, publication.SearchRequest{Query: publication.SearchQuery{Text: "resumable"}, Federation: "auto", PageSize: 3})
			requireStatus(t, response, http.StatusOK)
			var page publication.SearchResponse
			decodeResult(t, response, &page)
			want := 3
			if tc.partial {
				want = 2
			}
			if len(page.Results) != want || page.PageToken != "" || fetches.calls != 2 || strings.Contains(response.header.Get("Server-Timing"), "partial") != tc.partial || !slices.ContainsFunc(page.Results, func(r publication.SearchResult) bool { return r.Identifier == peerID }) || !slices.ContainsFunc(page.Results, func(r publication.SearchResult) bool {
				return r.Identifier == "urn:air:catalog.example.com:agent:healthy"
			}) {
				t.Fatalf("page=%+v calls=%d headers=%v", page, fetches.calls, response.header)
			}
			wireResponse := string(response.body) + response.header.Get("Server-Timing")
			for _, private := range []string{token, "upstream-failure-sentinel"} {
				if strings.Contains(wireResponse, private) {
					t.Fatal("private peer evidence entered public response", private)
				}
			}
			if strings.Contains(string(response.body), `"diagnostics"`) || strings.Contains(string(response.body), `"partial"`) {
				t.Fatal("private diagnostics entered the ARD body")
			}
		})
	}
}

func TestARDLiveResultsPreserveHealthyResultsAndSignalInvalidFields(t *testing.T) {
	for _, tc := range []struct {
		name, score, extra string
		valid              bool
	}{
		{"sparse integer", "80", "", true},
		{"sparse decimal", "80.0", "", true},
		{"sparse exponent", "8e1", "", true},
		{"ordinary type", "80", `,"type":"application/json"`, true},
		{"unfamiliar type", "80", `,"type":"application/vnd.example.card+json"`, true},
		{"parameterized type", "80", `,"type":"application/vnd.example.card+json; version=1"`, true},
		{"quoted type parameter", "80", `,"type":"text/x-acme; profile=\"https://schemas.example/card\""`, true},
		{"type spelling preserved", "80", `,"type":" APPLICATION/VND.EXAMPLE.CARD+JSON; version=1 "`, true},
		{"matching identity without type", "80", `,"@id":"urn:air:peer.example:agent:weather"`, true},
		{"matching identity", "80", `,"type":"application/json","@id":"urn:air:peer.example:agent:weather"`, true},
		{"invalid type", "80", `,"type":"not-a-media-type"`, false},
		{"invalid type parameters", "80", `,"type":"application/json; charset"`, false},
		{"empty type", "80", `,"type":""`, false},
		{"blank type", "80", `,"type":" "`, false},
		{"mismatched identity", "80", `,"@id":"urn:air:other.example:agent:weather"`, false},
		{"empty identity", "80", `,"@id":""`, false},
		{"blank identity", "80", `,"@id":" "`, false},
		{"exact metadata", "80", `,"metadata":{"value":9007199254740993}`, true},
		{"null metadata", "80", `,"metadata":{"value":null}`, true},
		{"aligned lowercase trust", "80", `,"trustManifest":{"identity":"https://peer.example"}`, true},
		{"aligned uppercase trust", "80", `,"TrustManifest":{"identity":"did:web:workloads.peer.example"}`, true},
		{"lowercase trust extensions", "80", `,"trustManifest":{"identity":"https://peer.example","signature":"opaque","trustSchema":{"governanceUri":"https://policy.example/rules","future":9007199254740993},"attestations":[{"type":"custom","uri":"https://receipts.example/one"}],"future":[false,null,9007199254740993]}`, true},
		{"uppercase trust extensions", "80", `,"TrustManifest":{"identity":"https://peer.example","signature":"opaque","trustSchema":{"governanceUri":"https://policy.example/rules","future":9007199254740993},"attestations":[{"type":"custom","uri":"https://receipts.example/one"}],"future":[false,null,9007199254740993]}`, true},
		{"misaligned lowercase trust", "80", `,"trustManifest":{"identity":"https://unrelated.example"}`, false},
		{"misaligned uppercase trust", "80", `,"TrustManifest":{"identity":"https://peer.example.attacker.test"}`, false},
		{"empty trust identity", "80", `,"trustManifest":{"identity":""}`, false},
		{"malformed trust attestation", "80", `,"trustManifest":{"identity":"https://peer.example","attestations":[{"type":"custom","uri":17}]}`, false},
		{"ambiguous trust spellings", "80", `,"trustManifest":{"identity":"https://peer.example"},"TrustManifest":{"identity":"https://peer.example"}`, false},
		{"object metadata", "80", `,"metadata":{"value":{}}`, false},
		{"array metadata", "80", `,"metadata":{"value":[]}`, false},
		{"invalid context", "80", `,"@context":true`, false},
		{"invalid name", "80", `,"displayName":42`, false},
		{"near integer", "80.000000000000000000001", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"identifier":"urn:air:peer.example:agent:weather","score":` + tc.score + `,"source":"https://peer.example/search"` + tc.extra + `}`)
			server, fetches := newARDLiveSearchFixture(t, raw)
			// A malformed entry must not hide valid entries from that same peer
			// or the independently published local result.
			fetches.page = func(_ fetcher.Request, result fetcher.Result) fetcher.Result {
				result.Body = []byte(`{"results":[{"identifier":"urn:air:peer.example:agent:healthy-peer","score":90,"source":"https://peer.example/search"},` + string(raw) + `]}`)
				return result
			}
			response := callARDSearch(t, server, publication.SearchRequest{Query: publication.SearchQuery{Text: "resumable"}, Federation: "auto", PageSize: 10})
			requireStatus(t, response, http.StatusOK)
			requireControlSchema(t, "ARDSearchResponse", response.body)
			var original map[string]json.RawMessage
			if err := json.Unmarshal(raw, &original); err != nil {
				t.Fatal(err)
			}
			wantTrust := original["trustManifest"]
			if len(wantTrust) == 0 {
				wantTrust = original["TrustManifest"]
			}
			var result struct {
				Results []publication.SearchResult `json:"results"`
			}
			if err := json.Unmarshal(response.body, &result); err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Results []map[string]json.RawMessage `json:"results"`
			}
			if err := json.Unmarshal(response.body, &wire); err != nil {
				t.Fatal(err)
			}
			for _, item := range wire.Results {
				if _, upper := item["TrustManifest"]; upper {
					t.Fatal("public Search emitted uppercase TrustManifest")
				}
				if string(item["identifier"]) == `"urn:air:peer.example:agent:weather"` {
					if !bytes.Equal(item["trustManifest"], wantTrust) {
						t.Fatalf("peer trust values were lost or changed: %s", response.body)
					}
					for _, field := range []string{"displayName", "type", "@id"} {
						want, wantPresent := original[field]
						got, gotPresent := item[field]
						if gotPresent != wantPresent || !bytes.Equal(got, want) {
							t.Fatalf("peer field %s changed presence or value: %s", field, response.body)
						}
					}
					if tc.name == "null metadata" {
						var metadata map[string]json.RawMessage
						if err := json.Unmarshal(item["metadata"], &metadata); err != nil || string(metadata["value"]) != "null" {
							t.Fatalf("valid null metadata was dropped or converted: %s (%v)", response.body, err)
						}
					}
				} else if len(item["trustManifest"]) != 0 {
					t.Fatal("local result inherited a peer trust declaration")
				}
			}
			want := 2
			if tc.valid {
				want = 3
			}
			if len(result.Results) != want || fetches.calls != 1 || strings.Contains(response.header.Get("Server-Timing"), "partial") == tc.valid {
				t.Fatalf("results=%s headers=%v calls=%d", response.body, response.header, fetches.calls)
			}
			var local, remote, healthyPeer bool
			for _, item := range result.Results {
				switch item.Identifier {
				case "urn:air:peer.example:agent:weather":
					remote = true
					if item.Score != 80 {
						t.Fatalf("wrong projected score: %d", item.Score)
					}
				case "urn:air:catalog.example.com:agent:healthy":
					local = true
				case "urn:air:peer.example:agent:healthy-peer":
					healthyPeer = true
				default:
					t.Fatalf("unexpected result identity: %q", item.Identifier)
				}
			}
			if !local || !healthyPeer || remote != tc.valid {
				t.Fatal("healthy local/peer result lost or invalid peer emitted")
			}
			if tc.name == "exact metadata" && !strings.Contains(string(response.body), "9007199254740993") {
				t.Fatal("metadata precision lost")
			}
			if strings.Contains(string(response.body), `"diagnostics"`) || strings.Contains(string(response.body), `"partial"`) {
				t.Fatal("private diagnostics entered the ARD body")
			}
		})
	}
}

// Public trust disclosure uses the existing source boundary. An administrator
// can still read exact peer evidence without making it anonymously discoverable.
func TestARDLiveTrustDisclosureRespectsSourceEligibility(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	const sourceID = "00000000-0000-7000-8000-000000000010"
	const trust = `{"identity":"https://peer.example","signature":"unverified-peer-evidence"}`
	raw := json.RawMessage(`{"identifier":"urn:air:peer.example:agent:weather","score":80,"source":"https://peer.example/search","TrustManifest":` + trust + `}`)
	for _, tc := range []struct {
		name    string
		mutate  func(*federation.Source)
		allowed bool
		control bool
	}{
		{"approved anonymous", func(*federation.Source) {}, true, true},
		{"authenticated assignment", func(s *federation.Source) { s.DiscoveryAssignments[0].Audience = federation.AudienceAuthenticated }, false, true},
		{"unassigned host", func(s *federation.Source) { s.DiscoveryAssignments[0].Host = "other.example" }, false, false},
		{"private network", func(s *federation.Source) { s.NetworkProfile = "private" }, false, false},
		{"missing anonymous acknowledgement", func(s *federation.Source) { s.Live.AnonymousDisclosureAcknowledged = false }, false, false},
		{"paused source", func(s *federation.Source) { s.Status = federation.SourcePaused }, false, false},
		{"other tenant", func(s *federation.Source) { s.TenantID = "00000000-0000-7000-8000-000000000002" }, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, fetches := newARDLiveSearchFixture(t, raw)
			ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: tenant, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
			source, err := server.federation.GetSource(ctx, sourceID)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&source)
			store := federation.NewMemoryStore()
			if _, err := store.CreateSource(ctx, source); err != nil {
				t.Fatal(err)
			}
			server.federation, err = federation.NewManager(store, fetches, clock.NewFake(source.CreatedAt), id.Generator{}, federation.WithDiscoveryTargets([]federation.DiscoveryTarget{{Host: "127.0.0.1", TenantID: tenant, Audience: federation.AudienceAnonymous}}))
			if err != nil {
				t.Fatal(err)
			}
			response := callARDSearch(t, server, publication.SearchRequest{Query: publication.SearchQuery{Text: "resumable"}, Federation: "auto", PageSize: 10})
			requireStatus(t, response, http.StatusOK)
			requireControlSchema(t, "ARDSearchResponse", response.body)
			var page publication.SearchResponse
			decodeResult(t, response, &page)
			want, calls := 1, 0
			if tc.allowed {
				want, calls = 2, 1
			}
			if len(page.Results) != want || fetches.calls != calls || strings.Contains(string(response.body), "unverified-peer-evidence") != tc.allowed || strings.Contains(string(response.body), sourceID) {
				t.Fatalf("source boundary or disclosure changed: %s calls=%d", response.body, fetches.calls)
			}
			if tc.control {
				request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/federation/sources:live-search", strings.NewReader(`{"text":"weather","reason":"Inspect approved source evidence"}`))
				request.Header.Set("Content-Type", "application/json")
				control := httptest.NewRecorder()
				server.handleLiveFederationSearch(control, request)
				if control.Code != http.StatusOK {
					t.Fatalf("Control read failed: %d %s", control.Code, control.Body.String())
				}
				requireControlSchema(t, "LiveSearchResponse", control.Body.Bytes())
				var evidence federation.LiveSearchResponse
				if err := json.Unmarshal(control.Body.Bytes(), &evidence); err != nil || len(evidence.Results) != 1 || !bytes.Equal(evidence.Results[0].Entry, raw) || fetches.calls != calls+1 {
					t.Fatalf("Control lost original spelling/evidence or fetched trust URLs: %+v calls=%d err=%v", evidence, fetches.calls, err)
				}
			}
		})
	}
}

func TestARDLiveTrustDisclosureRequiresAuthenticatedAudience(t *testing.T) {
	const sourceID = "00000000-0000-7000-8000-000000000010"
	const trust = `{"identity":"https://peer.example","signature":"private-unverified-claim"}`
	raw := json.RawMessage(`{"identifier":"urn:air:peer.example:agent:weather","score":80,"source":"https://peer.example/search","TrustManifest":` + trust + `}`)
	fixture, fetches := newARDLiveSearchFixture(t, raw)
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: projectionTenant, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	source, err := fixture.federation.GetSource(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	source.DiscoveryAssignments = []federation.DiscoveryAssignment{{Host: publicFixtureHost, Audience: federation.AudienceAuthenticated}}
	source.Live.AnonymousDisclosureAcknowledged = false
	source.NetworkProfile = "private"
	store := federation.NewMemoryStore()
	if _, err := store.CreateSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	manager, err := federation.NewManager(store, fetches, clock.NewFake(source.CreatedAt), id.Generator{}, federation.WithDiscoveryTargets([]federation.DiscoveryTarget{{Host: publicFixtureHost, TenantID: projectionTenant, Audience: federation.AudienceAuthenticated}}))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := publicFixtureSnapshot(t, DiscoveryAuthenticated)
	reads := 0
	server := publicProjectionServer(t, DiscoveryAuthenticated, func(_ context.Context, tenant string) (publication.Snapshot, error) {
		if tenant != projectionTenant {
			t.Fatal("authenticated Search read another tenant's snapshot")
		}
		reads++
		return snapshot, nil
	})
	server.federation = manager
	other, _ := identity.FromContext(identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000002", Subject: "other-reader", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}, Claims: map[string]string{"scope:catalog.read": "true"}}))
	authenticator, ok := server.authenticator.(discoveryAuthenticator)
	if !ok {
		t.Fatal("unexpected discovery authenticator fixture")
	}
	authenticator["other-reader"] = other
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"other tenant", "other-reader", http.StatusForbidden},
		{"authorized reader", "reader", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, publicFixtureOrigin+"/ard/v1/search", strings.NewReader(`{"query":{"text":"weather"},"federation":"auto","pageSize":10}`))
			request.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response := httptest.NewRecorder()
			server.handleARDSearch(response, request)
			if response.Code != tc.status || strings.Contains(response.Body.String(), sourceID) {
				t.Fatalf("authenticated disclosure status or private source ID: %d %s", response.Code, response.Body.String())
			}
			if tc.status != http.StatusOK {
				requireControlSchema(t, "ARDError", response.Body.Bytes())
				if reads != 0 || fetches.calls != 0 || strings.Contains(response.Body.String(), "private-unverified-claim") {
					t.Fatal("refused caller read a snapshot, contacted the peer or received its claim")
				}
				return
			}
			requireControlSchema(t, "ARDSearchResponse", response.Body.Bytes())
			var page publication.SearchResponse
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || len(page.Results) != 1 || page.Results[0].Identifier != "urn:air:peer.example:agent:weather" || string(page.Results[0].TrustManifest) != trust || reads != 1 || fetches.calls != 1 || response.Header().Get("Vary") != "Authorization" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("authorized reader lost the private declaration or response protections: %s reads=%d calls=%d err=%v", response.Body.String(), reads, fetches.calls, err)
			}
		})
	}
}

func TestARDLiveResultsEnforceDisclosedFilters(t *testing.T) {
	const media = "application/a2a-agent-card+json"
	alias := map[string]any{"kind": "https://agenticresourcediscovery.org/ns#mediaType"}
	for _, tc := range []struct {
		name, extra  string
		queryContext any
		filter       map[string]any
		wantPeer     bool
	}{
		{"type match", `,"type":"application/a2a-agent-card+json"`, nil, map[string]any{"type": media}, true},
		{"type mismatch", `,"type":"application/mcp-server-card+json"`, nil, map[string]any{"type": media}, false},
		{"type extension cannot hide mismatch", `,"type":"application/mcp-server-card+json","Type":"application/a2a-agent-card+json"`, nil, map[string]any{"type": media}, false},
		{"type extension cannot reject match", `,"type":"application/a2a-agent-card+json","Type":{}`, nil, map[string]any{"type": media}, true},
		{"publisher match", "", nil, map[string]any{"publisher": "peer.example"}, true},
		{"publisher mismatch", "", nil, map[string]any{"publisher": "catalog.example.com"}, false},
		{"tags OR match", `,"tags":["weather"]`, nil, map[string]any{"tags": []string{"other", "weather"}}, true},
		{"tags mismatch", `,"tags":["other"]`, nil, map[string]any{"tags": "weather"}, false},
		{"empty tags contradict", `,"tags":[]`, nil, map[string]any{"tags": "weather"}, false},
		{"metadata match", `,"metadata":{"region":"eu"}`, nil, map[string]any{"metadata.region": "eu"}, true},
		{"metadata mismatch", `,"metadata":{"region":"us"}`, nil, map[string]any{"metadata.region": "eu"}, false},
		{"null metadata", `,"metadata":{"region":null}`, nil, map[string]any{"metadata.region": "<nil>"}, false},
		{"null metadata OR", `,"metadata":{"region":null}`, nil, map[string]any{"metadata.region": []string{"eu", "<nil>"}}, false},
		{"literal nil metadata", `,"metadata":{"region":"<nil>"}`, nil, map[string]any{"metadata.region": "<nil>"}, true},
		{"false metadata", `,"metadata":{"enabled":false}`, nil, map[string]any{"metadata.enabled": "false"}, true},
		{"null with matching local", `,"metadata":{"catalogReleaseId":null}`, nil, map[string]any{"metadata.catalogReleaseId": []string{"release-healthy", "<nil>"}}, false},
		{"literal with matching local", `,"metadata":{"catalogReleaseId":"<nil>"}`, nil, map[string]any{"metadata.catalogReleaseId": []string{"release-healthy", "<nil>"}}, true},
		{"omitted with matching local", "", nil, map[string]any{"metadata.catalogReleaseId": []string{"release-healthy", "<nil>"}}, true},
		{"metadata extension cannot hide mismatch", `,"metadata":{"region":"us"},"Metadata":{"region":"eu"}`, nil, map[string]any{"metadata.region": "eu"}, false},
		{"metadata extension cannot reject match", `,"metadata":{"region":"eu"},"Metadata":[]`, nil, map[string]any{"metadata.region": "eu"}, true},
		{"exact numeric metadata match", `,"metadata":{"count":9007199254740993}`, nil, map[string]any{"metadata.count": "9007199254740993"}, true},
		{"exact numeric metadata mismatch", `,"metadata":{"count":9007199254740993}`, nil, map[string]any{"metadata.count": "9007199254740992"}, false},
		{"AND match", `,"type":"application/a2a-agent-card+json","tags":["weather"]`, nil, map[string]any{"type": media, "tags": "weather"}, true},
		{"AND mismatch", `,"type":"application/a2a-agent-card+json","tags":["other"]`, nil, map[string]any{"type": media, "tags": "weather"}, false},
		{"alias match", `,"type":"application/a2a-agent-card+json"`, alias, map[string]any{"kind": media}, true},
		{"alias mismatch", `,"type":"application/mcp-server-card+json"`, alias, map[string]any{"kind": media}, false},
		{"omitted type", "", nil, map[string]any{"type": media}, true},
		{"omitted tags", "", nil, map[string]any{"tags": "weather"}, true},
		{"omitted metadata", "", nil, map[string]any{"metadata.region": "eu"}, true},
		{"omitted metadata key", `,"metadata":{}`, nil, map[string]any{"metadata.region": "eu"}, true},
		{"omitted and disclosed match", `,"type":"application/a2a-agent-card+json"`, nil, map[string]any{"type": media, "tags": "weather"}, true},
		{"license match", `,"metadata":{"license":"Apache-2.0"}`, nil, map[string]any{"metadata.license": "Apache-2.0"}, true},
		{"license mismatch", `,"metadata":{"license":"MIT"}`, nil, map[string]any{"metadata.license": "Apache-2.0"}, false},
		{"license omitted", "", nil, map[string]any{"metadata.license": "Apache-2.0"}, true},
		{"topic full scalar", `,"http://purl.org/dc/terms/subject":"finance"`, nil, map[string]any{"http://purl.org/dc/terms/subject": "finance"}, true},
		{"topic other prefix", `,"@context":{"peer":"http://purl.org/dc/terms/"},"peer:subject":["finance"]`, map[string]any{"client.example": "http://purl.org/dc/terms/"}, map[string]any{"client.example:subject": "finance"}, true},
		{"topic mismatch", `,"@context":{"peer":"http://purl.org/dc/terms/"},"peer:subject":"weather"`, nil, map[string]any{"http://purl.org/dc/terms/subject": "finance"}, false},
		{"topic null", `,"http://purl.org/dc/terms/subject":null`, nil, map[string]any{"http://purl.org/dc/terms/subject": "finance"}, false},
		{"topic malformed", `,"http://purl.org/dc/terms/subject":{}`, nil, map[string]any{"http://purl.org/dc/terms/subject": "finance"}, false},
		{"topic remote context", `,"@context":"https://unavailable.invalid/context","peer:subject":"finance"`, nil, map[string]any{"http://purl.org/dc/terms/subject": "finance"}, false},
		{"topic omitted", "", nil, map[string]any{"http://purl.org/dc/terms/subject": "finance"}, true},
		{"native skill match", `,"type":"application/a2a-agent-card+json","data":{"skills":[{"id":"weather"}]}`, nil, map[string]any{"data.skills.id": "weather"}, true},
		{"native skill mismatch", `,"type":"application/a2a-agent-card+json","data":{"skills":[{"id":"other"}]}`, nil, map[string]any{"data.skills.id": "weather"}, false},
		{"native malformed skill", `,"type":"application/a2a-agent-card+json","data":{"skills":[{"id":42}]}`, nil, map[string]any{"data.skills.id": "weather"}, false},
		{"native sparse card", `,"type":"application/a2a-agent-card+json","data":{}`, nil, map[string]any{"data.skills.id": "weather"}, true},
		{"native empty skills", `,"type":"application/a2a-agent-card+json","data":{"skills":[]}`, nil, map[string]any{"data.skills.id": "weather"}, false},
		{"native URL only", `,"type":"application/a2a-agent-card+json","url":"https://never-fetch.invalid/card"`, nil, map[string]any{"data.skills.id": "weather"}, true},
		{"native missing type", `,"data":{"skills":[{"id":"other"}]}`, nil, map[string]any{"data.skills.id": "weather"}, true},
		{"native wrong type", `,"type":"application/agent-plugins+zip","data":{"skills":[{"id":"weather"}]}`, nil, map[string]any{"data.skills.id": "weather"}, false},
		{"native false", `,"type":"application/a2a-agent-card+json","data":{"capabilities":{"streaming":false}}`, nil, map[string]any{"data.capabilities.streaming": "false"}, true},
		{"native opaque number", `,"type":"application/a2a-agent-card+json","data":{"capabilities":{"streaming":false},"_meta":{"opaque":1e1000}}`, nil, map[string]any{"data.capabilities.streaming": "false"}, true},
		{"native false mismatch", `,"type":"application/a2a-agent-card+json","data":{"capabilities":{"streaming":false}}`, nil, map[string]any{"data.capabilities.streaming": "true"}, false},
		{"native null", `,"type":"application/a2a-agent-card+json","data":{"capabilities":{"streaming":null}}`, nil, map[string]any{"data.capabilities.streaming": "false"}, false},
		{"native independent interfaces", `,"type":"application/a2a-agent-card+json","data":{"supportedInterfaces":[{"protocolBinding":"JSONRPC","protocolVersion":"1.0"},{"protocolBinding":"HTTP+JSON","protocolVersion":"2.0"}]}`, nil, map[string]any{"data.supportedInterfaces.protocolBinding": "JSONRPC", "data.supportedInterfaces.protocolVersion": "2.0"}, true},
		{"native MCP version", `,"type":"application/mcp-server-card+json","data":{"remotes":[{"type":"streamable-http","supportedProtocolVersions":["2025-11-25"]}]}`, nil, map[string]any{"data.remotes.type": "streamable-http", "data.remotes.supportedProtocolVersions": "2025-11-25"}, true},
		{"native Registry is not a Card", `,"type":"application/vnd.mcp.server+json","data":{"remotes":[{"type":"streamable-http"}]}`, nil, map[string]any{"data.remotes.type": "streamable-http"}, false},
		{"native input mode OR", `,"type":"application/a2a-agent-card+json","data":{"defaultInputModes":["text/plain","text/plain"]}`, nil, map[string]any{"data.defaultInputModes": []string{"image/png", "text/plain"}}, true},
		{"native input mismatch", `,"type":"application/a2a-agent-card+json","data":{"defaultInputModes":["image/png"]}`, nil, map[string]any{"data.defaultInputModes": "text/plain"}, false},
		{"native input malformed", `,"type":"application/a2a-agent-card+json","data":{"defaultInputModes":["text/plain",42]}`, nil, map[string]any{"data.defaultInputModes": "text/plain"}, false},
		{"native input empty", `,"type":"application/a2a-agent-card+json","data":{"defaultInputModes":[]}`, nil, map[string]any{"data.defaultInputModes": "text/plain"}, false},
		{"native input sparse", `,"type":"application/a2a-agent-card+json","data":{}`, nil, map[string]any{"data.defaultInputModes": "text/plain"}, true},
		{"native input wrong media", `,"type":"application/mcp-server-card+json","data":{"defaultInputModes":["text/plain"]}`, nil, map[string]any{"data.defaultInputModes": "text/plain"}, false},
		{"native output match", `,"type":"application/a2a-agent-card+json","data":{"defaultOutputModes":["text/plain"]}`, nil, map[string]any{"data.defaultOutputModes": "text/plain"}, true},
		{"native output null", `,"type":"application/a2a-agent-card+json","data":{"defaultOutputModes":null}`, nil, map[string]any{"data.defaultOutputModes": "text/plain"}, false},
		{"native output scalar", `,"type":"application/a2a-agent-card+json","data":{"defaultOutputModes":"text/plain"}`, nil, map[string]any{"data.defaultOutputModes": "text/plain"}, false},
		{"native output URL only", `,"type":"application/a2a-agent-card+json","url":"https://never-fetch.invalid/card"`, nil, map[string]any{"data.defaultOutputModes": "text/plain"}, true},
		{"native skill tags match", `,"type":"application/a2a-agent-card+json","data":{"skills":[{"tags":["weather","weather"]}]}`, nil, map[string]any{"data.skills.tags": "weather"}, true},
		{"native skill tags mismatch", `,"type":"application/a2a-agent-card+json","data":{"skills":[{"tags":["Weather"]}]}`, nil, map[string]any{"data.skills.tags": "weather"}, false},
		{"native skill tags malformed", `,"type":"application/a2a-agent-card+json","data":{"skills":[{"tags":["weather",null]}]}`, nil, map[string]any{"data.skills.tags": "weather"}, false},
		{"native skill tags sparse", `,"type":"application/a2a-agent-card+json","data":{"skills":[{}]}`, nil, map[string]any{"data.skills.tags": "weather"}, true},
		{"native skill tags empty", `,"type":"application/a2a-agent-card+json","data":{"skills":[{"tags":[]}]}`, nil, map[string]any{"data.skills.tags": "weather"}, false},
		{"native declaration aliases", `,"type":"application/a2a-agent-card+json","data":{"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],"skills":[{"tags":["weather"]}]}`, map[string]any{"body.example": "https://agenticresourcediscovery.org/ns#data"}, map[string]any{"body.example.defaultInputModes": "text/plain", "body.example.defaultOutputModes": "text/plain", "body.example.skills.tags": "weather"}, true},
		{"native declaration input conjunction", `,"type":"application/a2a-agent-card+json","data":{"defaultInputModes":["image/png"],"defaultOutputModes":["text/plain"],"skills":[{"tags":["weather"]}]}`, nil, map[string]any{"data.defaultInputModes": "text/plain", "data.defaultOutputModes": "text/plain", "data.skills.tags": "weather"}, false},
		{"native declaration output conjunction", `,"type":"application/a2a-agent-card+json","data":{"defaultInputModes":["text/plain"],"defaultOutputModes":["image/png"],"skills":[{"tags":["weather"]}]}`, nil, map[string]any{"data.defaultInputModes": "text/plain", "data.defaultOutputModes": "text/plain", "data.skills.tags": "weather"}, false},
		{"native declaration tags conjunction", `,"type":"application/a2a-agent-card+json","data":{"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],"skills":[{"tags":["other"]}]}`, nil, map[string]any{"data.defaultInputModes": "text/plain", "data.defaultOutputModes": "text/plain", "data.skills.tags": "weather"}, false},
		{"native input default ignores skill override", `,"type":"application/a2a-agent-card+json","data":{"defaultInputModes":["text/plain"],"skills":[{"inputModes":["image/png"]}]}`, nil, map[string]any{"data.defaultInputModes": "text/plain"}, true},
		{"native input default does not inherit skill override", `,"type":"application/a2a-agent-card+json","data":{"defaultInputModes":["image/png"],"skills":[{"inputModes":["text/plain"]}]}`, nil, map[string]any{"data.defaultInputModes": "text/plain"}, false},
		{"native output default ignores skill override", `,"type":"application/a2a-agent-card+json","data":{"defaultOutputModes":["text/plain"],"skills":[{"outputModes":["image/png"]}]}`, nil, map[string]any{"data.defaultOutputModes": "text/plain"}, true},
		{"native output default does not inherit skill override", `,"type":"application/a2a-agent-card+json","data":{"defaultOutputModes":["image/png"],"skills":[{"outputModes":["text/plain"]}]}`, nil, map[string]any{"data.defaultOutputModes": "text/plain"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"identifier":"urn:air:peer.example:agent:weather","score":80,"source":"https://peer.example/search"` + tc.extra + `}`)
			server, fetches := newARDLiveSearchFixture(t, raw)
			fetches.page = func(request fetcher.Request, result fetcher.Result) fetcher.Result {
				var wire publication.SearchRequest
				if err := json.Unmarshal(request.Body, &wire); err != nil {
					t.Fatal(err)
				}
				got, err := json.Marshal(wire.Query)
				want, wantErr := json.Marshal(publication.SearchQuery{Text: "resumable", Context: tc.queryContext, Filter: tc.filter})
				if err != nil || wantErr != nil || string(got) != string(want) {
					t.Fatalf("forwarded query changed: %s, want %s", got, want)
				}
				return result
			}
			response := callARDSearch(t, server, publication.SearchRequest{Query: publication.SearchQuery{Text: "resumable", Context: tc.queryContext, Filter: tc.filter}, Federation: "auto", PageSize: 10})
			requireStatus(t, response, http.StatusOK)
			var result publication.SearchResponse
			if err := json.Unmarshal(response.body, &result); err != nil {
				t.Fatal(err)
			}
			if tc.name == "omitted type" {
				var wire struct {
					Results []map[string]json.RawMessage `json:"results"`
				}
				if err := json.Unmarshal(response.body, &wire); err != nil {
					t.Fatal(err)
				}
				for _, item := range wire.Results {
					if string(item["identifier"]) == `"urn:air:peer.example:agent:weather"` {
						if _, present := item["type"]; present {
							t.Fatalf("sparse filtered result gained a contradictory type: %s", response.body)
						}
					}
				}
			}
			peer := false
			for _, item := range result.Results {
				peer = peer || item.Identifier == "urn:air:peer.example:agent:weather"
			}
			if peer != tc.wantPeer || fetches.calls != 1 || strings.Contains(response.header.Get("Server-Timing"), "partial") == tc.wantPeer {
				t.Fatalf("peer=%t want=%t calls=%d body=%s headers=%v", peer, tc.wantPeer, fetches.calls, response.body, response.header)
			}
			if tc.name == "type mismatch" || tc.name == "publisher mismatch" || tc.filter["metadata.catalogReleaseId"] != nil || tc.filter["data.skills.id"] == "weather" || tc.filter["data.capabilities.streaming"] == "false" || tc.filter["data.defaultInputModes"] == "text/plain" || tc.filter["data.defaultOutputModes"] == "text/plain" || tc.filter["data.skills.tags"] == "weather" || tc.name == "native declaration aliases" {
				local := false
				for _, item := range result.Results {
					local = local || item.Identifier == "urn:air:catalog.example.com:agent:healthy"
				}
				want := 1
				if tc.wantPeer {
					want++
				}
				if !local || len(result.Results) != want || result.PageToken != "" {
					t.Fatal("healthy matching local result was lost")
				}
			}
			if strings.Contains(string(response.body), `"diagnostics"`) || strings.Contains(string(response.body), `"partial"`) {
				t.Fatal("private diagnostics entered the ARD body")
			}
		})
	}
}

func TestARDQueryContextAdmissionDoesNotDependOnSelectedTerms(t *testing.T) {
	for _, operation := range []string{"search-none", "search-auto", "search-type", "explore-publisher"} {
		for _, tc := range []struct {
			name, member string
			valid        bool
		}{
			{"omitted", "", true}, {"object", `,"@context":{}`, true}, {"array", `,"@context":[]`, true},
			{"base URI", `,"@context":"https://agenticresourcediscovery.org/context/v1"`, true},
			{"numeric version", `,"@context":{"@version":1.1}`, true},
			{"numeric version trailing zero", `,"@context":{"@version":1.10}`, true},
			{"numeric version exponent", `,"@context":{"@version":11e-1}`, true},
			{"boolean", `,"@context":true`, false}, {"number", `,"@context":17`, false}, {"null", `,"@context":null`, false}, {"relative URI", `,"@context":"relative/context"`, false},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				body := `{"query":{"text":"resumable"` + tc.member
				if operation == "search-type" {
					body += `,"filter":{"type":"application/a2a-agent-card+json"}`
				}
				body += `}`
				path := "/ard/v1/search"
				switch operation {
				case "explore-publisher":
					body += `,"resultType":{"facets":[{"field":"publisher"}]}}`
					path = "/ard/v1/explore"
				case "search-auto":
					body += `,"federation":"auto"}`
				default:
					body += `,"federation":"none"}`
				}
				server, fetches := newARDLiveSearchFixture(t, []byte(`{"identifier":"urn:air:peer.example:agent:weather","score":80,"source":"https://peer.example/search"}`))
				request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1"+path, strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				if operation == "explore-publisher" {
					server.handleARDExplore(response, request)
				} else {
					server.handleARDSearch(response, request)
				}
				wantStatus, wantCalls := http.StatusBadRequest, 0
				if tc.valid {
					wantStatus = http.StatusOK
					if operation == "search-auto" {
						wantCalls = 1
					}
				}
				if response.Code != wantStatus || fetches.calls != wantCalls {
					t.Fatalf("status=%d want=%d calls=%d want=%d body=%s", response.Code, wantStatus, fetches.calls, wantCalls, response.Body)
				}
				if !tc.valid {
					var problem struct {
						ErrorCode string `json:"errorCode"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.ErrorCode != "INVALID_ARGUMENT" {
						t.Fatalf("invalid ARD error: %s (%v)", response.Body, err)
					}
				}
			})
		}
	}
}

func TestARDNativeFilterPeerRefusalPreservesMatchingLocalResult(t *testing.T) {
	server, fetches := newARDLiveSearchFixture(t, json.RawMessage(`{"identifier":"urn:air:peer.example:agent:weather","score":80,"source":"https://peer.example/search"}`))
	key := http.MethodPost + " https://peer.example/search"
	refusal := fetches.responses[key]
	refusal.Status = http.StatusBadRequest
	refusal.Body = []byte(`{"error":{"code":"INVALID_ARGUMENT","message":"Unsupported native filter"}}`)
	fetches.responses[key] = refusal
	response := callARDSearch(t, server, publication.SearchRequest{Query: publication.SearchQuery{Text: "resumable", Filter: map[string]any{"data.skills.id": "weather"}}, Federation: "auto"})
	requireStatus(t, response, http.StatusOK)
	var result publication.SearchResponse
	if err := json.Unmarshal(response.body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Identifier != "urn:air:catalog.example.com:agent:healthy" || fetches.calls != 1 || !strings.Contains(response.header.Get("Server-Timing"), "partial") {
		t.Fatalf("peer refusal lost matching local data or retried: %s / %v", response.body, response.header)
	}
	if strings.Contains(string(response.body), "Unsupported native filter") || strings.Contains(string(response.body), `"diagnostics"`) {
		t.Fatal("private peer diagnosis was disclosed")
	}
}

func TestARDAutoClientPagesReplayBoundedWindowAndRecoverSameToken(t *testing.T) {
	server, fetches := newARDLiveSearchFixture(t, json.RawMessage(`{"identifier":"urn:air:peer.example:agent:one","score":100,"source":"https://peer.example/search"}`))
	outage, change, trustChange := false, false, false
	fetches.page = func(request fetcher.Request, result fetcher.Result) fetcher.Result {
		var wire struct {
			PageSize  int    `json:"pageSize"`
			PageToken string `json:"pageToken"`
		}
		if err := json.Unmarshal(request.Body, &wire); err != nil || wire.PageSize != 50 || wire.PageToken != "" {
			t.Fatalf("client progress changed fixed fresh peer request: %+v %v", wire, err)
		}
		if outage {
			result.Status = http.StatusServiceUnavailable
			result.Body = []byte(`{"private":"upstream-secret"}`)
			return result
		}
		name := "one"
		if change {
			name = "changed"
		}
		signature := "opaque-original"
		if trustChange {
			signature = "opaque-changed"
		}
		result.Body = []byte(`{"results":[{"identifier":"urn:air:peer.example:agent:` + name + `","score":100,"source":"https://peer.example/search","TrustManifest":{"identity":"https://peer.example","signature":"` + signature + `"}},{"identifier":"urn:air:peer.example:agent:two","score":99,"source":"https://peer.example/search","trustManifest":{"identity":"https://peer.example","signature":"second-peer"}}]}`)
		return result
	}
	query := publication.SearchRequest{Query: publication.SearchQuery{Text: "resumable"}, Federation: "auto", PageSize: 1}
	first := callARDSearch(t, server, query)
	requireStatus(t, first, http.StatusOK)
	requireControlSchema(t, "ARDSearchResponse", first.body)
	var page publication.SearchResponse
	decodeResult(t, first, &page)
	if len(page.Results) != 1 || !strings.HasSuffix(page.Results[0].Identifier, ":one") || !strings.HasPrefix(page.PageToken, catalog.LiveCursorPrefix) {
		t.Fatalf("first page: %+v", page)
	}
	if string(page.Results[0].TrustManifest) != `{"identity":"https://peer.example","signature":"opaque-original"}` || fetches.calls != 1 {
		t.Fatalf("first page trust or request budget changed: %+v calls=%d", page, fetches.calls)
	}
	query.PageToken = page.PageToken
	original := query.PageToken
	for _, tc := range []struct {
		name   string
		mutate func(*publication.SearchRequest)
	}{
		{"tampered token", func(q *publication.SearchRequest) { q.PageToken += "x" }},
		{"changed query", func(q *publication.SearchRequest) { q.Query.Text = "another query" }},
		{"changed page size", func(q *publication.SearchRequest) { q.PageSize = 2 }},
		{"changed federation mode", func(q *publication.SearchRequest) { q.Federation = "none" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := query
			tc.mutate(&bad)
			before := fetches.calls
			rejected := callARDSearch(t, server, bad)
			requireStatus(t, rejected, http.StatusBadRequest)
			requireControlSchema(t, "ARDError", rejected.body)
			var problem struct {
				ErrorCode string `json:"errorCode"`
			}
			decodeResult(t, rejected, &problem)
			if problem.ErrorCode != "INVALID_ARGUMENT" {
				t.Fatalf("unexpected ARD error: %+v", problem)
			}
			if fetches.calls != before {
				t.Fatal("invalid token binding contacted peer")
			}
		})
	}
	outage = true
	failed := callARDSearch(t, server, query)
	requireStatus(t, failed, http.StatusServiceUnavailable)
	if strings.Contains(string(failed.body), "upstream-secret") || strings.Contains(string(failed.body), "pageToken") {
		t.Fatal("failed replay exposed response or advanced")
	}
	outage = false
	second := callARDSearch(t, server, query)
	requireStatus(t, second, http.StatusOK)
	requireControlSchema(t, "ARDSearchResponse", second.body)
	page = publication.SearchResponse{}
	decodeResult(t, second, &page)
	if len(page.Results) != 1 || !strings.HasSuffix(page.Results[0].Identifier, ":two") || page.PageToken == "" || strings.HasPrefix(page.PageToken, catalog.LiveCursorPrefix) {
		t.Fatalf("retry lost local continuation: %+v", page)
	}
	if string(page.Results[0].TrustManifest) != `{"identity":"https://peer.example","signature":"second-peer"}` || fetches.calls != 3 {
		t.Fatalf("same-token recovery lost trust or repeated requests: %+v calls=%d", page, fetches.calls)
	}
	query.PageToken = page.PageToken
	before := fetches.calls
	third := callARDSearch(t, server, query)
	requireStatus(t, third, http.StatusOK)
	page = publication.SearchResponse{}
	decodeResult(t, third, &page)
	if len(page.Results) != 1 || page.Results[0].Identifier != "urn:air:catalog.example.com:agent:healthy" || len(page.Results[0].TrustManifest) != 0 || page.PageToken != "" || fetches.calls != before {
		t.Fatalf("durable tail repeated live: %+v", page)
	}
	query.PageToken = original
	change = true
	requireStatus(t, callARDSearch(t, server, query), http.StatusBadRequest)
	change = false
	trustChange = true
	changedTrust := callARDSearch(t, server, query)
	requireStatus(t, changedTrust, http.StatusBadRequest)
	requireControlSchema(t, "ARDError", changedTrust.body)
	if !strings.Contains(string(changedTrust.body), `"errorCode":"INVALID_ARGUMENT"`) || strings.Contains(string(changedTrust.body), "opaque-changed") {
		t.Fatalf("trust-only change was not safely fenced: %s", changedTrust.body)
	}
	trustChange = false
	requireStatus(t, callARDSearch(t, server, query), http.StatusOK)
}

type publicLiveHybridStore struct{ catalog.SemanticSearchStore }

func (publicLiveHybridStore) Active(_ context.Context, tenant, profile, scope string, dimensions int) (catalog.SemanticGeneration, bool, error) {
	return catalog.SemanticGeneration{ID: "00000000-0000-7000-8000-000000000011", TenantID: tenant, Profile: profile, Scope: scope, Dimensions: dimensions, SourceGeneration: "1"}, true, nil
}
func (publicLiveHybridStore) Similarities(_ context.Context, _ catalog.SemanticGeneration, _ []float32, keys []string) (map[string]float64, error) {
	return map[string]float64{keys[0]: 1}, nil
}

type publicLiveHybridProvider struct{ catalog.SemanticProvider }

func (publicLiveHybridProvider) Query(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}

func TestARDHybridClientContinuesLiveAfterLastDurablePage(t *testing.T) {
	semantic, err := catalog.NewSemanticSearch(catalog.SemanticSearchOptions{Store: publicLiveHybridStore{}, Provider: publicLiveHybridProvider{}, Profile: "sha256:" + strings.Repeat("a", 64), RankingProfile: "sha256:" + strings.Repeat("b", 64), Dimensions: 2, Cutoff: .5, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	server, fetches := newARDLiveSearchFixtureWithSemantic(t, json.RawMessage(`{"identifier":"urn:air:peer.example:agent:one","score":100,"source":"https://peer.example/search"}`), semantic)
	outage := false
	fetches.page = func(_ fetcher.Request, result fetcher.Result) fetcher.Result {
		if outage {
			result.Status = 503
			result.Body = []byte(`{"private":"secret"}`)
			return result
		}
		result.Body = []byte(`{"results":[{"identifier":"urn:air:peer.example:agent:one","score":100,"source":"https://peer.example/search","TrustManifest":{"identity":"https://peer.example","signature":"one"}},{"identifier":"urn:air:peer.example:agent:two","score":99,"source":"https://peer.example/search","trustManifest":{"identity":"https://peer.example","signature":"two"}}]}`)
		return result
	}
	query := publication.SearchRequest{Query: publication.SearchQuery{Text: "resumable"}, Federation: "auto", PageSize: 1}
	want := []string{"urn:air:catalog.example.com:agent:healthy", "urn:air:peer.example:agent:one", "urn:air:peer.example:agent:two"}
	for i, id := range want {
		response := callARDSearch(t, server, query)
		requireStatus(t, response, 200)
		requireControlSchema(t, "ARDSearchResponse", response.body)
		if response.header.Get("Catalog-Search-Tier") != "hybrid" {
			t.Fatal("hybrid header lost")
		}
		var page publication.SearchResponse
		decodeResult(t, response, &page)
		if len(page.Results) != 1 || page.Results[0].Identifier != id || page.Results[0].Score < 0 || page.Results[0].Score > 100 {
			t.Fatalf("page %d: %+v", i, page)
		}
		wantTrust := []string{"", `{"identity":"https://peer.example","signature":"one"}`, `{"identity":"https://peer.example","signature":"two"}`}[i]
		if string(page.Results[0].TrustManifest) != wantTrust {
			t.Fatalf("hybrid order mixed or lost peer trust: %+v", page)
		}
		for _, field := range []string{`"phase"`, `"fingerprint"`, `"tier"`, `"diagnostics"`} {
			if strings.Contains(string(response.body), field) {
				t.Fatalf("private state entered ARD: %s", response.body)
			}
		}
		if i == 2 {
			if page.PageToken != "" {
				t.Fatal("extra terminal token")
			}
			break
		}
		if !strings.HasPrefix(page.PageToken, catalog.LiveCursorPrefix) {
			t.Fatal("last durable page omitted live tail token")
		}
		query.PageToken = page.PageToken
		if i == 0 {
			outage = true
			failed := callARDSearch(t, server, query)
			requireStatus(t, failed, 503)
			requireControlSchema(t, "ARDError", failed.body)
			if strings.Contains(string(failed.body), "secret") || strings.Contains(string(failed.body), "pageToken") {
				t.Fatal("failed replay leaked or advanced")
			}
			outage = false
		}
	}
}
