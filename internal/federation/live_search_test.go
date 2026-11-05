package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

// This scripted transport has no listeners, DNS, subprocesses or database.
type livePagingFetcher struct {
	requests []fetcher.Request
	respond  func(context.Context, fetcher.Request) (fetcher.Result, error)
}

func (*livePagingFetcher) PolicyDigest() string { return federationTestPolicyDigest }

func (f *livePagingFetcher) Fetch(ctx context.Context, request fetcher.Request) (fetcher.Result, error) {
	f.requests = append(f.requests, request)
	result, err := f.respond(ctx, request)
	if err != nil {
		return result, err
	}
	result.PolicyID, result.PolicyRevision, result.PolicyDigest = "test-policy", 1, federationTestPolicyDigest
	if result.FinalURL == "" {
		result.FinalURL = request.URL
	}
	return result, nil
}

func newLivePagingFixture(t *testing.T, pages int, names ...string) (*Manager, *MemoryStore, *livePagingFetcher, []Source) {
	t.Helper()
	store := NewMemoryStore()
	sources := make([]Source, 0, len(names))
	for index, name := range names {
		endpoint := "https://" + name + ".example/search"
		source := Source{ID: fmt.Sprintf("00000000-0000-7000-8000-%012d", index+10), TenantID: catalogSearchTestTenantID, Endpoint: endpoint, RegistrySearchURL: endpoint,
			Status: SourceActive, Type: SourceRegistry, ProfileID: ardProfileID, Revision: 1, RetrievalMode: RetrievalLive,
			Live:                 LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true},
			DiscoveryAssignments: []DiscoveryAssignment{{Host: "catalog.example.com", Audience: AudienceAnonymous}},
			PolicyID:             "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest}
		if _, err := store.CreateSource(t.Context(), source); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, source)
	}
	outbound := &livePagingFetcher{}
	options := []ManagerOption{}
	if pages != 0 {
		options = append(options, WithLiveSearchPages(pages))
	}
	manager, err := NewManager(store, outbound, clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)), id.Generator{}, options...)
	if err != nil {
		t.Fatal(err)
	}
	return manager, store, outbound, sources
}

func livePagingContext(t *testing.T) context.Context {
	t.Helper()
	return identityfixture.WithActor(t.Context(), identity.Actor{TenantID: catalogSearchTestTenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
}

func livePagingToken(t *testing.T, request fetcher.Request) string {
	t.Helper()
	var wire struct {
		PageToken string `json:"pageToken"`
	}
	if err := json.Unmarshal(request.Body, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.PageToken
}

func livePagingEntry(name string, score int) string {
	return fmt.Sprintf(`{"identifier":"urn:air:remote.example:agent:%s","displayName":"Useful remote agent %s","type":"application/a2a-agent-card+json","score":%d,"source":"https://index.example/search"}`, name, name, score)
}

func livePagingPage(t *testing.T, token string, entries ...string) fetcher.Result {
	t.Helper()
	body := `{"results":[` + strings.Join(entries, ",") + `]`
	if token != "" {
		encoded, err := json.Marshal(token)
		if err != nil {
			t.Fatal(err)
		}
		body += `,"pageToken":` + string(encoded)
	}
	return fetcher.Result{Status: http.StatusOK, MediaType: "application/json", Body: []byte(body + "}")}
}

func TestLivePagingUsesDistinctResultsAndFinalContinuationState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pages     int
		first     []string
		calls     int
		partial   bool
		remaining bool
		scope     bool
	}{
		{"default one", 0, []string{livePagingEntry("alpha", 90)}, 1, true, true, false},
		{"explicit one", 1, []string{livePagingEntry("alpha", 90)}, 1, true, true, false},
		{"duplicate first page", 3, []string{livePagingEntry("alpha", 90), livePagingEntry("alpha", 80)}, 2, false, false, false},
		{"empty first page", 3, nil, 2, false, false, false},
		{"scope excludes first page", 3, []string{livePagingEntry("excluded", 99)}, 2, false, false, true},
		{"enough results", 3, []string{livePagingEntry("alpha", 90), livePagingEntry("beta", 80)}, 1, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, store, outbound, sources := newLivePagingFixture(t, tc.pages, "a")
			if tc.scope {
				source := sources[0]
				source.Scope.Identifiers = []string{"urn:air:remote.example:agent:beta"}
				store.sources[source.ID] = source
			}
			outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
				if livePagingToken(t, request) == "" {
					return livePagingPage(t, "opaque-next", tc.first...), nil
				}
				return livePagingPage(t, "", livePagingEntry("beta", 80)), nil
			}
			request := LiveSearchRequest{Text: "Useful agent", PageSize: 2, Context: map[string]any{"@vocab": "https://schema.org/"}, Filter: map[string]any{"tags": []string{"selected"}}, Administrative: true, Reason: "Investigate bounded peer pages"}
			response, err := manager.LiveSearch(livePagingContext(t), request)
			if err != nil || len(outbound.requests) != tc.calls || response.Partial != tc.partial || slices.ContainsFunc(response.Diagnostics, func(d LiveDiagnostic) bool { return d.Status == "continuation_unavailable" }) != tc.remaining {
				t.Fatalf("response=%+v requests=%d error=%v", response, len(outbound.requests), err)
			}
			if tc.calls == 2 && !slices.ContainsFunc(response.Results, func(r LiveResult) bool { return r.Identifier == "urn:air:remote.example:agent:beta" }) {
				t.Fatal("later useful result missing", response)
			}
			if len(response.Diagnostics) > MaxLiveDiagnostics || len(store.observations) != 0 || len(store.snapshots) != 0 {
				t.Fatal("live work escaped its evidence boundary")
			}
			var first map[string]any
			if err := json.Unmarshal(outbound.requests[0].Body, &first); err != nil {
				t.Fatal(err)
			}
			for index, call := range outbound.requests {
				var wire map[string]any
				if err := json.Unmarshal(call.Body, &wire); err != nil {
					t.Fatal(err)
				}
				if index > 0 && wire["pageToken"] != "opaque-next" {
					t.Fatal("opaque token not forwarded exactly", wire)
				}
				delete(wire, "pageToken")
				if !reflect.DeepEqual(wire, first) || call.URL != sources[0].RegistrySearchURL || call.ExpectedPolicyDigest != federationTestPolicyDigest || call.TenantID != catalogSearchTestTenantID || call.RejectRedirects != (index > 0) {
					t.Fatal("continuation changed its request or access binding", call)
				}
			}
		})
	}
}

func TestLivePagingStopsFailedAndRepeatedTokensWithoutRestart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		next  fetcher.Result
		err   error
		calls int
	}{
		{"transport", fetcher.Result{}, errors.New("synthetic transport failure"), 2},
		{"token refusal", fetcher.Result{Status: 400}, nil, 2},
		{"invalid envelope", fetcher.Result{Status: 200, Body: []byte(`{"results":null}`)}, nil, 2},
		{"same token", livePagingPage(t, "next", livePagingEntry("alpha", 80)), nil, 2},
		{"token cycle", livePagingPage(t, "other", livePagingEntry("alpha", 80)), nil, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, _, outbound, _ := newLivePagingFixture(t, 5, "a")
			outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
				switch livePagingToken(t, request) {
				case "":
					return livePagingPage(t, "next", livePagingEntry("alpha", 90)), nil
				case "other":
					return livePagingPage(t, "next"), nil
				default:
					return tc.next, tc.err
				}
			}
			response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true, Reason: "Investigate continuation failure"})
			if err != nil || !response.Partial || len(response.Results) != 1 || response.Results[0].Score != 90 || len(outbound.requests) != tc.calls {
				t.Fatalf("response=%+v requests=%d error=%v", response, len(outbound.requests), err)
			}
		})
	}
}

func TestLivePagingContinuesPastDuplicateAndEmptyIntermediatePages(t *testing.T) {
	manager, _, outbound, _ := newLivePagingFixture(t, 5, "a")
	outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
		switch livePagingToken(t, request) {
		case "":
			return livePagingPage(t, "duplicate", livePagingEntry("alpha", 80)), nil
		case "duplicate":
			return livePagingPage(t, "empty", livePagingEntry("alpha", 90)), nil
		case "empty":
			return livePagingPage(t, "last"), nil
		case "last":
			return livePagingPage(t, "", livePagingEntry("beta", 70)), nil
		default:
			t.Fatal("unexpected continuation token")
			return fetcher.Result{}, errors.New("unexpected token")
		}
	}
	response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true, Reason: "Verify useful later results"})
	if err != nil || response.Partial || len(outbound.requests) != 4 || len(response.Results) != 2 || response.Results[0].Identifier != "urn:air:remote.example:agent:alpha" || response.Results[0].Score != 90 || response.Results[1].Identifier != "urn:air:remote.example:agent:beta" {
		t.Fatalf("response=%+v calls=%d error=%v", response, len(outbound.requests), err)
	}
	if len(response.Diagnostics) != 1 || response.Diagnostics[0].Status != "succeeded" {
		t.Fatal("consumed pages left stale or repeated diagnostics", response.Diagnostics)
	}
}

func TestLivePagingCancellationBeforeResultAdmissionStopsFurtherWork(t *testing.T) {
	manager, _, outbound, _ := newLivePagingFixture(t, 3, "a", "b")
	ctx, cancel := context.WithCancel(livePagingContext(t))
	defer cancel()
	outbound.respond = func(_ context.Context, _ fetcher.Request) (fetcher.Result, error) {
		// The response arrives as the caller cancels. Do not admit its entries
		// or start another peer/page merely because transport returned success.
		cancel()
		return livePagingPage(t, "next", livePagingEntry("alpha", 90)), nil
	}
	response, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "agent", PageSize: 2, DiscoveryHost: "catalog.example.com", Audience: AudienceAnonymous})
	if err != nil || !response.Partial || len(response.Results) != 0 || len(outbound.requests) != 1 || !slices.ContainsFunc(response.Diagnostics, func(d LiveDiagnostic) bool { return d.Status == "failed" }) {
		t.Fatalf("response=%+v calls=%d error=%v", response, len(outbound.requests), err)
	}
}

func TestLivePagingPrioritizesDirectPeersAndReferralsBeforeFairContinuation(t *testing.T) {
	manager, store, outbound, sources := newLivePagingFixture(t, 3, "a", "b", "c")
	sources[0].Referrals = ReferralPolicy{Enabled: true, MaxHops: 1, ApprovedSourceIDs: []string{sources[2].ID}}
	sources[2].Live.ReferralOnly = true
	store.sources[sources[0].ID], store.sources[sources[2].ID] = sources[0], sources[2]
	outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
		token := livePagingToken(t, request)
		if request.URL == sources[2].RegistrySearchURL {
			return livePagingPage(t, "", livePagingEntry("gamma", 100), livePagingEntry("delta", 99)), nil
		}
		if token == "" {
			result := livePagingPage(t, "next")
			if request.URL == sources[0].RegistrySearchURL {
				result.Body = []byte(fmt.Sprintf(`{"results":[],"pageToken":"next","referrals":[{"identifier":"urn:air:c.example:registry:main","displayName":"C","type":"application/ai-registry+json","url":%q}]}`, sources[2].RegistrySearchURL))
			}
			return result, nil
		}
		if token == "next" {
			return livePagingPage(t, "last", livePagingEntry("alpha", 90)), nil
		}
		return livePagingPage(t, "", livePagingEntry("beta", 80)), nil
	}
	response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true, Reason: "Investigate fair bounded traversal"})
	if err != nil || response.Partial || len(response.Results) != 2 || response.Results[0].Score != 100 || response.Results[1].Score != 99 {
		t.Fatalf("response=%+v error=%v", response, err)
	}
	order := make([]string, 0, len(outbound.requests))
	for _, call := range outbound.requests {
		order = append(order, call.URL)
	}
	a, b, c := sources[0].RegistrySearchURL, sources[1].RegistrySearchURL, sources[2].RegistrySearchURL
	if !reflect.DeepEqual(order, []string{a, b, c, a, b, a, b}) {
		t.Fatal("unfair peer/page order", order)
	}
	// C filled the global limit, but A and B still had their own opportunities.
	for _, call := range outbound.requests {
		var wire map[string]any
		if err := json.Unmarshal(call.Body, &wire); err != nil {
			t.Fatal(err)
		}
		want := "none"
		if call.URL == a {
			want = "referrals"
		}
		if wire["federation"] != want {
			t.Fatal("a peer page changed the referral hop budget", wire)
		}
	}
}

func TestLivePagingCancellationStopsQueuedPeersAndReferralReads(t *testing.T) {
	manager, store, outbound, sources := newLivePagingFixture(t, 3, "a", "b", "c", "d")
	sources[0].Referrals = ReferralPolicy{Enabled: true, MaxHops: 1, ApprovedSourceIDs: []string{sources[2].ID, sources[3].ID}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads := 0
	manager.store = livePagingReadStore{Store: store, read: func(ctx context.Context, tenant, sourceID string) (Source, error) {
		reads++
		if ctx.Err() != nil || tenant != sources[0].TenantID || sourceID != sources[2].ID {
			t.Fatal("unexpected referral read after cancellation or outside its tenant")
		}
		cancel()
		return Source{}, errors.New("synthetic cancellation during referral read")
	}}
	outbound.respond = func(_ context.Context, _ fetcher.Request) (fetcher.Result, error) {
		return livePagingPage(t, "", livePagingEntry("alpha", 90)), nil
	}
	response := LiveSearchResponse{Results: make([]LiveResult, 0)}
	_, _, err := manager.traverseLivePeers(ctx, sources[0].TenantID, LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true}, sources[:2], &response)
	if err != nil || !response.Partial || len(response.Results) != 1 || reads != 1 || len(outbound.requests) != 1 || outbound.requests[0].URL != sources[0].RegistrySearchURL {
		t.Fatalf("response=%+v calls=%d reads=%d error=%v", response, len(outbound.requests), reads, err)
	}
}

func TestLivePagingEnforcesDerivedRequestAndRemainingByteBounds(t *testing.T) {
	for _, pages := range []int{3, 5} {
		t.Run(fmt.Sprintf("all peer page allowances %d", pages), func(t *testing.T) {
			manager, _, outbound, _ := newLivePagingFixture(t, pages, "a", "b", "c")
			outbound.respond = func(_ context.Context, _ fetcher.Request) (fetcher.Result, error) {
				return livePagingPage(t, strconv.Itoa(len(outbound.requests))), nil
			}
			response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", Administrative: true, Reason: "Investigate aggregate request bound"})
			if err != nil || !response.Partial || len(outbound.requests) != 3*pages {
				t.Fatalf("response=%+v calls=%d error=%v", response, len(outbound.requests), err)
			}
		})
	}
	for _, remaining := range []int{0, 512, 1024} {
		t.Run(fmt.Sprintf("remaining %d", remaining), func(t *testing.T) {
			manager, _, outbound, _ := newLivePagingFixture(t, 3, "a")
			outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
				if livePagingToken(t, request) != "" {
					if request.MaxResponseBytes != int64(remaining) {
						t.Fatalf("remaining byte override=%d want=%d", request.MaxResponseBytes, remaining)
					}
					return livePagingPage(t, "", livePagingEntry("beta", 80)), nil
				}
				result := livePagingPage(t, "next", livePagingEntry("alpha", 90))
				result.Body = append([]byte(strings.Repeat(" ", MaxLiveResponseBytes-remaining-len(result.Body))), result.Body...)
				return result, nil
			}
			response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true, Reason: "Investigate remaining byte bound"})
			calls := 1
			if remaining >= 1024 {
				calls = 2
			}
			if err != nil || len(outbound.requests) != calls || response.Partial != (calls == 1) || len(response.Results) != calls {
				t.Fatalf("response=%+v calls=%d error=%v", response, len(outbound.requests), err)
			}
		})
	}
}

func TestLivePagingRejectsChangedAuthorityAndPreservesHealthyDuplicate(t *testing.T) {
	mutations := map[string]func(*Source){
		"revision":           func(s *Source) { s.Revision++ },
		"scope":              func(s *Source) { s.Scope.Identifiers = []string{"urn:air:remote.example:agent:other"} },
		"paused":             func(s *Source) { s.Status = SourcePaused },
		"removed":            func(s *Source) { s.Status = SourceRemoved },
		"disabled":           func(s *Source) { s.Live.Enabled = false },
		"audience":           func(s *Source) { s.DiscoveryAssignments[0].Audience = AudienceAuthenticated },
		"endpoint":           func(s *Source) { s.RegistrySearchURL += "/changed" },
		"manifest endpoint":  func(s *Source) { s.Endpoint += "/changed" },
		"profile":            func(s *Source) { s.ProfileID = "different-profile" },
		"credential binding": func(s *Source) { s.CredentialBinding = "different-binding" },
		"network profile":    func(s *Source) { s.NetworkProfile = "different-route" },
		"policy":             func(s *Source) { s.PolicyDigest = "sha256:" + strings.Repeat("b", 64) },
		"policy identity":    func(s *Source) { s.PolicyID = "different-policy" },
		"policy revision":    func(s *Source) { s.PolicyRevision++ },
		"tenant":             func(s *Source) { s.TenantID = "00000000-0000-7000-8000-000000000002" },
	}
	for name, mutate := range mutations {
		for _, afterLastPage := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/final=%t", name, afterLastPage), func(t *testing.T) {
				manager, store, outbound, sources := newLivePagingFixture(t, 3, "a", "b")
				outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
					if request.URL == sources[1].RegistrySearchURL {
						return livePagingPage(t, "", livePagingEntry("shared", 80)), nil
					}
					last := livePagingToken(t, request) != ""
					if last == afterLastPage {
						store.mu.Lock()
						source := store.sources[sources[0].ID]
						mutate(&source)
						store.sources[source.ID] = source
						store.mu.Unlock()
					}
					if last {
						return livePagingPage(t, "", livePagingEntry("other", 70)), nil
					}
					return livePagingPage(t, "next", livePagingEntry("shared", 100)), nil
				}
				response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 2, DiscoveryHost: "catalog.example.com", Audience: AudienceAnonymous})
				calls := 2
				if afterLastPage {
					calls = 3
				}
				if err != nil || !response.Partial || len(outbound.requests) != calls || len(response.Results) != 1 || response.Results[0].SourceID != sources[1].ID || response.Results[0].Score != 80 {
					t.Fatalf("response=%+v calls=%d error=%v", response, len(outbound.requests), err)
				}
			})
		}
	}
}

func TestLivePagingKeepsCredentialFenceAndCanonicalEndpoint(t *testing.T) {
	for _, scenario := range []string{"credential rotation", "initial redirect", "continuation endpoint change", "unchanged credential"} {
		t.Run(scenario, func(t *testing.T) {
			manager, store, outbound, sources := newLivePagingFixture(t, 3, "a")
			source := sources[0]
			source.CredentialBinding = "reader-binding"
			store.sources[source.ID] = source
			outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
				if request.CredentialBinding != source.CredentialBinding {
					t.Fatal("credential origin binding lost")
				}
				result := livePagingPage(t, "next", livePagingEntry("alpha", 90))
				result.CredentialRevision = "generation-one"
				if livePagingToken(t, request) != "" {
					result = livePagingPage(t, "", livePagingEntry("beta", 80))
					result.CredentialRevision = "generation-one"
					if scenario == "credential rotation" {
						result.CredentialRevision = "generation-two"
					}
					if scenario == "continuation endpoint change" {
						result.FinalURL = "https://a.example/different-search"
					}
				} else if scenario == "initial redirect" {
					result.FinalURL = "https://a.example/different-search"
				}
				return result, nil
			}
			response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true, Reason: "Investigate continuation evidence"})
			wantResults, wantCalls := 0, 2
			if scenario == "initial redirect" {
				wantResults, wantCalls = 1, 1
			} else if scenario == "unchanged credential" {
				wantResults = 2
			}
			if err != nil || len(response.Results) != wantResults || len(outbound.requests) != wantCalls || response.Partial != (scenario != "unchanged credential") {
				t.Fatalf("response=%+v calls=%d error=%v", response, len(outbound.requests), err)
			}
		})
	}
}

func TestLivePagingSharesDeadlineAndRetainsOnlyVerifiedContributions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, _, outbound, sources := newLivePagingFixture(t, 3, "a", "b")
		var deadline time.Time
		outbound.respond = func(ctx context.Context, request fetcher.Request) (fetcher.Result, error) {
			current, ok := ctx.Deadline()
			if !ok || !deadline.IsZero() && !deadline.Equal(current) {
				t.Fatal("each page received a new deadline")
			}
			deadline = current
			if request.URL == sources[1].RegistrySearchURL {
				return livePagingPage(t, "", livePagingEntry("healthy", 80)), nil
			}
			if livePagingToken(t, request) == "" {
				time.Sleep(1500 * time.Millisecond)
				return livePagingPage(t, "next", livePagingEntry("unverified", 90)), nil
			}
			<-ctx.Done()
			return fetcher.Result{}, ctx.Err()
		}
		started := time.Now()
		response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true, Reason: "Investigate shared deadline"})
		if err != nil || time.Since(started) != 2*time.Second || !response.Partial || len(response.Results) != 1 || response.Results[0].SourceID != sources[1].ID || len(outbound.requests) != 3 {
			t.Fatalf("response=%+v calls=%d elapsed=%s error=%v", response, len(outbound.requests), time.Since(started), err)
		}
	})
}

func TestLivePagingReaderNeedsNoAdministrativeSourcePermission(t *testing.T) {
	manager, _, outbound, _ := newLivePagingFixture(t, 3, "a")
	outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
		if livePagingToken(t, request) == "" {
			return livePagingPage(t, "next"), nil
		}
		return livePagingPage(t, "", livePagingEntry("alpha", 90)), nil
	}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: catalogSearchTestTenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	page, err := manager.SearchFederatedCatalog(ctx, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "Useful remote agent"}, Limit: 2, IncludeLive: true})
	if err != nil || page.Partial || len(page.Live) != 1 || len(outbound.requests) != 2 {
		t.Fatalf("page=%+v calls=%d error=%v", page, len(outbound.requests), err)
	}
	if _, err := manager.GetSource(ctx, "00000000-0000-7000-8000-000000000010"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("reader unexpectedly has administrator source authority", err)
	}
}

type livePagingReadStore struct {
	Store
	read func(context.Context, string, string) (Source, error)
}

func (s livePagingReadStore) GetSource(ctx context.Context, tenant, sourceID string) (Source, error) {
	return s.read(ctx, tenant, sourceID)
}

func TestLivePagingOmitsUnverifiableSourceWithoutExtendingDeadline(t *testing.T) {
	for _, failRead := range []int{1, 2} {
		t.Run(fmt.Sprintf("source read %d", failRead), func(t *testing.T) {
			manager, store, outbound, sources := newLivePagingFixture(t, 3, "a")
			reads := 0
			var deadline time.Time
			manager.store = livePagingReadStore{Store: store, read: func(ctx context.Context, tenant, sourceID string) (Source, error) {
				reads++
				current, ok := ctx.Deadline()
				if !ok || !current.Equal(deadline) || tenant != sources[0].TenantID || sourceID != sources[0].ID {
					t.Fatal("source check lost its deadline or tenant/identity binding")
				}
				if reads == failRead {
					return Source{}, errors.New("synthetic source read outage")
				}
				return store.GetSource(ctx, tenant, sourceID)
			}}
			outbound.respond = func(ctx context.Context, request fetcher.Request) (fetcher.Result, error) {
				deadline, _ = ctx.Deadline()
				if livePagingToken(t, request) == "" {
					return livePagingPage(t, "next", livePagingEntry("alpha", 90)), nil
				}
				return livePagingPage(t, "", livePagingEntry("beta", 80)), nil
			}
			response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true, Reason: "Investigate source read outage"})
			if err != nil || !response.Partial || len(response.Results) != 0 || reads != failRead || len(outbound.requests) != failRead {
				t.Fatalf("response=%+v calls=%d reads=%d error=%v", response, len(outbound.requests), reads, err)
			}
		})
	}
}

func TestLivePagingPreservesEqualScoreVisitOrderAndVersionPresence(t *testing.T) {
	t.Run("tie against an earlier peer page", func(t *testing.T) {
		manager, _, outbound, sources := newLivePagingFixture(t, 3, "a", "b")
		outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
			if request.URL == sources[1].RegistrySearchURL {
				return livePagingPage(t, "", livePagingEntry("shared", 80)), nil
			}
			if livePagingToken(t, request) == "" {
				return livePagingPage(t, "next", livePagingEntry("shared", 50)), nil
			}
			return livePagingPage(t, "", livePagingEntry("shared", 80), livePagingEntry("other", 70)), nil
		}
		response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true, Reason: "Investigate merge ties"})
		if err != nil || response.Partial || len(response.Results) != 2 || response.Results[0].SourceID != sources[1].ID || response.Results[0].Score != 80 {
			t.Fatalf("response=%+v error=%v", response, err)
		}
	})
	t.Run("absent version and present version are distinct", func(t *testing.T) {
		manager, _, outbound, _ := newLivePagingFixture(t, 3, "a")
		absent := livePagingEntry("alpha", 90)
		present := strings.Replace(absent, `"score":90`, `"score":90,"version":"1"`, 1)
		outbound.respond = func(_ context.Context, _ fetcher.Request) (fetcher.Result, error) {
			return livePagingPage(t, "unused", absent, present), nil
		}
		response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true, Reason: "Investigate version identity"})
		if err != nil || !response.Partial || len(outbound.requests) != 1 || len(response.Results) != 2 || response.Results[0].VersionPresent || !response.Results[1].VersionPresent || response.Results[1].Version != "1" {
			t.Fatalf("response=%+v calls=%d error=%v", response, len(outbound.requests), err)
		}
	})
}

func TestLivePagingTokensAreRequestLocalAndAuditRemainsOneAction(t *testing.T) {
	manager, store, outbound, sources := newLivePagingFixture(t, 3, "a", "b")
	sources[1].TenantID = "00000000-0000-7000-8000-000000000002"
	store.sources[sources[1].ID] = sources[1]
	evidence := &recordingFederationEvidence{}
	manager.evidence = evidence
	outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
		prefix := "a"
		if request.TenantID == sources[1].TenantID {
			prefix = "b"
		}
		token := livePagingToken(t, request)
		if token == "" {
			return livePagingPage(t, prefix+"-private-token"), nil
		}
		if token != prefix+"-private-token" {
			t.Fatal("token crossed the caller/tenant boundary", request)
		}
		return livePagingPage(t, "", livePagingEntry(prefix, 90)), nil
	}
	for _, source := range []Source{sources[0], sources[1], sources[0]} {
		actor := identity.Actor{TenantID: source.TenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}, RealmID: auditEvidenceRealmID, MembershipID: auditEvidenceMembershipID, ActorType: identity.ActorHuman}
		ctx := identityfixture.WithActor(t.Context(), actor)
		ctx = audit.WithExecution(ctx, audit.Execution{RequestID: "paging-request", AuthorizationDigest: actor.AuthorizationDigest()})
		response, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "private-query-text", PageSize: 2, Administrative: true, Reason: "Investigate isolated live request"})
		if err != nil || response.Partial || len(response.Results) != 1 || response.Results[0].SourceID != source.ID {
			t.Fatalf("response=%+v error=%v", response, err)
		}
	}
	if len(outbound.requests) != 6 || len(evidence.audit) != 3 || len(evidence.outbox) != 0 {
		t.Fatalf("calls=%d audit=%d outbox=%d", len(outbound.requests), len(evidence.audit), len(evidence.outbox))
	}
	for _, record := range evidence.audit {
		facts := auditFacts(t, record)
		contacted, ok := facts["contactedSourceIds"].([]any)
		if facts["outcome"] != outboundOutcomeSucceeded || !ok || len(contacted) != 1 {
			t.Fatal("administrative action counted pages as peers or kept stale partial status", facts)
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"private-token", "private-query-text", "https://"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("audit exposed outbound private input", string(raw))
			}
		}
	}
}
