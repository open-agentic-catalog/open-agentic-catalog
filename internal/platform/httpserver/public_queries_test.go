// Registry query units bind static active snapshots and controlled topology
// reads; they never fetch from peers or run an onboarding/publication workflow.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

type publicTopologyStore struct {
	federation.Store
	selectSources func(context.Context, string, string, federation.DiscoveryAudience, string, int) ([]federation.Source, bool, error)
}

func (s publicTopologyStore) ListDiscoveryLiveSources(c context.Context, t, h string, a federation.DiscoveryAudience, p string, n int) ([]federation.Source, bool, error) {
	return s.selectSources(c, t, h, a, p, n)
}
func (s publicTopologyStore) ListDiscoveryReferralSources(c context.Context, t, h string, a federation.DiscoveryAudience, p string, n int) ([]federation.Source, bool, error) {
	return s.selectSources(c, t, h, a, p, n)
}

type publicPolicyFetcher struct{ federation.Fetcher }

func (publicPolicyFetcher) PolicyDigest() string { return "sha256:" + strings.Repeat("a", 64) }

func TestPublicSearchProjection(t *testing.T) {
	t.Parallel()
	for _, mode := range []DiscoveryMode{DiscoveryAnonymous, DiscoveryAuthenticated} {
		for _, scenario := range []string{"none", "auto default", "auto continuation", "auto unavailable", "auto partial", "referrals", "referrals unavailable", "referrals partial"} {
			t.Run(fmt.Sprintf("%s/%s", mode, scenario), func(t *testing.T) {
				snapshot, _ := publicFixtureSnapshot(t, mode)
				ctx := t.Context()
				reads, topologyReads := 0, 0
				s := publicProjectionServer(t, mode, func(c context.Context, tenant string) (publication.Snapshot, error) {
					reads++
					if !samePublicProjectionContext(c, ctx, mode) || tenant != projectionTenant {
						t.Fatal("search snapshot binding")
					}
					return snapshot, nil
				})
				federationMode := "auto"
				if scenario == "none" {
					federationMode = "none"
				}
				if strings.HasPrefix(scenario, "referrals") {
					federationMode = "referrals"
				}
				audience := federation.AudienceAnonymous
				if mode == DiscoveryAuthenticated {
					audience = federation.AudienceAuthenticated
				}
				source := federation.Source{ID: projectionNext, TenantID: projectionTenant, DisplayName: "Approved registry", Type: federation.SourceRegistry, Endpoint: "https://peer.example/ard/v1/", RegistrySearchURL: "https://peer.example/ard/v1/search", Status: federation.SourceActive, ProfileID: publicFixtureARDProfile, RetrievalMode: federation.RetrievalLive, UpdateMode: federation.UpdateManual, CreatedBy: "administrator", Live: federation.LivePolicy{Enabled: true, ReferralOnly: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true, FederationMode: "none"}, PolicyID: "default", PolicyRevision: 1, PolicyDigest: (publicPolicyFetcher{}).PolicyDigest(), Referrals: federation.ReferralPolicy{Enabled: true, MaxHops: 1, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: []federation.DiscoveryAssignment{{Host: publicFixtureHost, Audience: audience}}, Revision: 1, CreatedAt: projectionTime, UpdatedAt: projectionTime}
				if federationMode != "none" {
					manager, err := federation.NewManager(publicTopologyStore{selectSources: func(c context.Context, tenant, host string, a federation.DiscoveryAudience, policy string, limit int) ([]federation.Source, bool, error) {
						topologyReads++
						actor, ok := identity.FromContext(c)
						if !ok || actor.TenantID != projectionTenant || actor.Subject != "discovery-registry" || actor.ActorType != identity.ActorService || len(actor.Roles) != 0 || identity.RequireInternalTarget(c, "federation.discover", projectionTenant, publicFixtureHost) != nil || tenant != projectionTenant || host != publicFixtureHost || a != audience || policy != (publicPolicyFetcher{}).PolicyDigest() || limit != 3 {
							t.Fatal("discovery topology handoff")
						}
						if strings.HasSuffix(scenario, "unavailable") {
							return nil, false, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-topology")
						}
						if federationMode == "referrals" {
							return []federation.Source{source}, strings.HasSuffix(scenario, "partial"), nil
						}
						return []federation.Source{}, strings.HasSuffix(scenario, "partial"), nil
					}}, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{})
					if err != nil {
						t.Fatal(err)
					}
					s.federation = manager
				}
				pageSize := 1
				if scenario == "auto default" {
					pageSize = 0
					federationMode = ""
				}
				token := ""
				pages := 1
				if scenario == "auto continuation" {
					pages = 2
				}
				for page := 0; page < pages; page++ {
					input := publication.SearchRequest{Query: publication.SearchQuery{Text: "fixture", Filter: map[string]any{"type": []string{"text/markdown"}}}, Federation: federationMode, PageSize: pageSize, PageToken: token}
					body, err := json.Marshal(input)
					if err != nil {
						t.Fatal(err)
					}
					r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/ard/v1/search", strings.NewReader(string(body)))
					r.Host = publicFixtureHost
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("Authorization", "Bearer reader")
					w := httptest.NewRecorder()
					s.handleARDSearch(w, r)
					var got publication.SearchResponse
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					count := 1
					if scenario == "auto default" {
						count = 2
					}
					wantVary := ""
					if mode == DiscoveryAuthenticated {
						wantVary = "Authorization"
					}
					if w.Code != 200 || len(got.Results) != count || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Vary") != wantVary || w.Header().Get("Link") != publicDiscoveryLinks() || w.Header().Get("Catalog-Search-Tier") != "core" {
						t.Fatalf("search response=%d %s %v", w.Code, w.Body, w.Header())
					}
					wantID := snapshot.ARD.Entries[1+page].Identifier
					if got.Results[0].Identifier != wantID || got.Results[0].Source != publicFixtureOrigin+"/ard/v1/" || got.Results[0].Score < 1 {
						t.Fatalf("search projection=%+v", got.Results)
					}
					partial := strings.HasSuffix(scenario, "partial") || strings.HasSuffix(scenario, "unavailable")
					if (w.Header().Get("Server-Timing") == `federation;desc="partial"`) != partial {
						t.Fatal("partial federation signal lost")
					}
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
						t.Fatal(err)
					}
					for _, key := range []string{"partial", "diagnostics", "tier", "fallbackReason", "partialIndex"} {
						if _, ok := fields[key]; ok {
							t.Fatalf("non-ARD field %s exposed", key)
						}
					}
					if strings.Contains(w.Body.String(), "private-fixture") {
						t.Fatal("private discovery evidence exposed")
					}
					if federationMode == "referrals" && !strings.HasSuffix(scenario, "unavailable") {
						if len(got.Referrals) != 1 || got.Referrals[0] != (publication.Referral{Identifier: "urn:air:" + publicFixtureHost + ":registry:" + source.ID, DisplayName: source.DisplayName, Type: "application/ai-registry+json", URL: source.RegistrySearchURL}) {
							t.Fatalf("referrals=%+v", got.Referrals)
						}
					} else if len(got.Referrals) != 0 {
						t.Fatal("unexpected referrals")
					}
					if page == 0 && pageSize == 1 {
						if got.PageToken == "" {
							t.Fatal("local continuation missing")
						}
					} else if got.PageToken != "" {
						t.Fatal("unexpected continuation")
					}
					token = got.PageToken
					if page == 0 && pages == 2 {
						s.federation = nil
					} // Continuing must never repeat live disclosure.
				}
				wantTopology := 1
				if scenario == "none" {
					wantTopology = 0
				}
				if reads != pages || topologyReads != wantTopology {
					t.Fatalf("snapshot/topology reads=%d/%d", reads, topologyReads)
				}
			})
		}
	}
}
func TestPublicListAndExploreProjection(t *testing.T) {
	t.Parallel()
	for _, mode := range []DiscoveryMode{DiscoveryAnonymous, DiscoveryAuthenticated} {
		for _, operation := range []string{"list", "list default", "explore"} {
			t.Run(fmt.Sprintf("%s/%s", mode, operation), func(t *testing.T) {
				snapshot, _ := publicFixtureSnapshot(t, mode)
				reads := 0
				s := publicProjectionServer(t, mode, func(c context.Context, tenant string) (publication.Snapshot, error) {
					reads++
					if !samePublicProjectionContext(c, t.Context(), mode) || tenant != projectionTenant {
						t.Fatal("query snapshot binding")
					}
					return snapshot, nil
				})
				path := "/ard/v1/agents?filter=" + url.QueryEscape(`type='text/markdown'`) + "&orderBy=" + url.QueryEscape("displayName DESC") + "&pageSize=1"
				method, body := http.MethodGet, ""
				pages := 2
				if operation == "list default" {
					path = "/ard/v1/agents"
					pages = 1
				}
				if operation == "explore" {
					method = http.MethodPost
					path = "/ard/v1/explore"
					body = `{"query":{"text":"fixture"},"resultType":{"facets":[{"field":"tags","limit":1}]}}`
					pages = 1
				}
				for page := 0; page < pages; page++ {
					r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
					r.Host = publicFixtureHost
					r.Header.Set("Authorization", "Bearer reader")
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					if operation == "explore" {
						s.handleARDExplore(w, r)
					} else {
						s.handleARDList(w, r)
					}
					vary := ""
					cache := "public, max-age=60, must-revalidate"
					if mode == DiscoveryAuthenticated {
						vary = "Authorization"
						cache = "private, max-age=60, must-revalidate"
					}
					if operation == "explore" {
						cache = "no-store"
					}
					if w.Code != 200 || w.Header().Get("Cache-Control") != cache || w.Header().Get("Vary") != vary || w.Header().Get("Link") != publicDiscoveryLinks() {
						t.Fatalf("query response=%d %s %v", w.Code, w.Body, w.Header())
					}
					if operation == "explore" {
						var got publication.ExploreResponse
						if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
							t.Fatal(err)
						}
						facet, ok := got.Facets["tags"]
						if !ok || got.ResultType != "facets" || len(facet.Buckets) != 1 || facet.Buckets[0] != (publication.ExploreBucket{Value: "fixture", Count: 2}) || w.Header().Get("Catalog-Search-Tier") != "core" {
							t.Fatalf("explore=%s", w.Body)
						}
						continue
					}
					var got publication.ListResponse
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if operation == "list default" {
						if len(got.Items) != 3 || got.Total != 3 || got.PageToken != "" {
							t.Fatalf("default page=%s", w.Body)
						}
						continue
					}
					if len(got.Items) != 1 || got.Total != 2 || got.Items[0].Identifier != snapshot.ARD.Entries[2-page].Identifier {
						t.Fatalf("filtered descending list=%s", w.Body)
					}
					if page == 0 {
						if got.PageToken == "" {
							t.Fatal("list continuation missing")
						}
						path += "&pageToken=" + url.QueryEscape(got.PageToken)
					} else if got.PageToken != "" {
						t.Fatal("list final continuation")
					}
				}
				if reads != pages {
					t.Fatalf("snapshot reads=%d", reads)
				}
			})
		}
	}
}

func TestPublicMetadataSearchAndExploreDistinguishNull(t *testing.T) {
	t.Parallel()
	for _, mode := range []DiscoveryMode{DiscoveryAnonymous, DiscoveryAuthenticated} {
		t.Run(string(mode), func(t *testing.T) {
			snapshot, _ := publicFixtureSnapshot(t, mode)
			snapshot.ARD.Entries[1].Metadata["region"] = nil
			snapshot.ARD.Entries[2].Metadata["region"] = "<nil>"
			snapshot = sealPublicFixtureSnapshot(t, snapshot)
			before, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			s := publicProjectionServer(t, mode, func(_ context.Context, tenant string) (publication.Snapshot, error) {
				if tenant != projectionTenant {
					t.Fatal("metadata query changed tenant scope")
				}
				return snapshot, nil
			})
			for _, operation := range []string{"search", "explore", "filtered explore"} {
				t.Run(operation, func(t *testing.T) {
					body := `{"query":{"text":"fixture","filter":{"metadata.region":"<nil>"}},"federation":"none"}`
					path := "/ard/v1/search"
					if operation != "search" {
						path = "/ard/v1/explore"
						body = `{"resultType":{"facets":[{"field":"metadata.region"}]}}`
						if operation == "filtered explore" {
							body = `{"query":{"filter":{"metadata.region":"<nil>"}},"resultType":{"facets":[{"field":"metadata.region"}]}}`
						}
					}
					r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
					r.Host = publicFixtureHost
					r.Header.Set("Authorization", "Bearer reader")
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					if operation == "search" {
						s.handleARDSearch(w, r)
					} else {
						s.handleARDExplore(w, r)
					}
					if w.Code != http.StatusOK {
						t.Fatalf("metadata %s failed: %d %s", operation, w.Code, w.Body)
					}
					if operation == "search" {
						var got publication.SearchResponse
						if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
							t.Fatal(err)
						}
						if len(got.Results) != 1 || got.Results[0].Identifier != snapshot.ARD.Entries[2].Identifier || got.Results[0].Metadata["region"] != "<nil>" || got.PageToken != "" {
							t.Fatalf("null matched literal string or literal value lost: %s", w.Body)
						}
					} else {
						var got publication.ExploreResponse
						if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
							t.Fatal(err)
						}
						want := publication.ExploreFacetResult{Buckets: []publication.ExploreBucket{{Value: "<nil>", Count: 1}}}
						if got.ResultType != "facets" || len(got.Facets) != 1 || !reflect.DeepEqual(got.Facets["metadata.region"], want) {
							t.Fatalf("null inflated literal string facet: %s", w.Body)
						}
					}
				})
			}
			after, err := json.Marshal(snapshot)
			if err != nil || string(after) != string(before) {
				t.Fatal("metadata queries modified immutable snapshot evidence")
			}
		})
	}
}

func TestPublicA2ADeclarationSearchAndExplore(t *testing.T) {
	for _, mode := range []DiscoveryMode{DiscoveryAnonymous, DiscoveryAuthenticated} {
		t.Run(string(mode), func(t *testing.T) {
			snapshot, _ := publicFixtureSnapshot(t, mode)
			data := json.RawMessage(`{"defaultInputModes":["text/plain","text/plain"],"defaultOutputModes":["text/plain"],"skills":[{"tags":["weather","weather"]}]}`)
			snapshot.ARD.Entries[1].Type, snapshot.ARD.Entries[1].Data = "application/a2a-agent-card+json", data
			snapshot.ARD.Entries[1].URL = ""
			// The same JSON on a different declared format supplies no A2A facts.
			snapshot.ARD.Entries[2].Type, snapshot.ARD.Entries[2].Data = "application/mcp-server-card+json", data
			snapshot.ARD.Entries[2].URL = ""
			for i := range snapshot.AICatalog.Entries {
				snapshot.AICatalog.Entries[i].Type = snapshot.ARD.Entries[i+1].Type
				snapshot.AICatalog.Entries[i].Data, snapshot.AICatalog.Entries[i].URL = data, ""
			}
			snapshot.ArtifactIDs = nil
			snapshot = sealPublicFixtureSnapshot(t, snapshot)
			s := publicProjectionServer(t, mode, func(_ context.Context, tenant string) (publication.Snapshot, error) {
				if tenant != projectionTenant {
					t.Fatal("native query changed tenant")
				}
				return snapshot, nil
			})
			for field, value := range map[string]string{"defaultInputModes": "text/plain", "defaultOutputModes": "text/plain", "skills.tags": "weather"} {
				for _, operation := range []string{"search", "explore"} {
					body := `{"query":{"@context":{"body.example":"https://agenticresourcediscovery.org/ns#data"},"text":"fixture","filter":{"body.example.` + field + `":"` + value + `"}}`
					if operation == "search" {
						body += `,"federation":"none"}`
					} else {
						body += `,"resultType":{"facets":[{"field":"data.` + field + `"}]}}`
					}
					r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ard/v1/"+operation, strings.NewReader(body))
					r.Host = publicFixtureHost
					r.Header.Set("Authorization", "Bearer reader")
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					if operation == "search" {
						s.handleARDSearch(w, r)
					} else {
						s.handleARDExplore(w, r)
					}
					if w.Code != http.StatusOK {
						t.Fatalf("native %s/%s failed: %d %s", operation, field, w.Code, w.Body)
					}
					if operation == "search" {
						var got publication.SearchResponse
						if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Results) != 1 || got.Results[0].Identifier != snapshot.ARD.Entries[1].Identifier {
							t.Fatalf("native search supplied wrong media facts: %s / %v", w.Body, err)
						}
					} else {
						var got publication.ExploreResponse
						want := publication.ExploreFacetResult{Buckets: []publication.ExploreBucket{{Value: value, Count: 1}}}
						if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || !reflect.DeepEqual(got.Facets["data."+field], want) {
							t.Fatalf("native facet lost or double-counted values: %s / %v", w.Body, err)
						}
					}
				}
			}
		})
	}
}
