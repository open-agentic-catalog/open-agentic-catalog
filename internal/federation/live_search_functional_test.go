//go:build functional

package federation

import (
	"crypto/sha256"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestLiveControlFiltersOverHTTPS(t *testing.T) {
	for _, tc := range []struct {
		name, license, match, nonmatch string
		categories                     []string
	}{
		{
			name: "combined", license: "MIT", categories: []string{"café", "tools"},
			match:    `,"metadata":{"license":"MIT"},"@context":{"dc":"http://purl.org/dc/terms/"},"dc:subject":["TOOLS","CAFÉ"]`,
			nonmatch: `,"metadata":{"license":"MIT"},"http://purl.org/dc/terms/subject":"tools"`,
		},
		{
			name: "license without topics", license: "MIT",
			match:    `,"metadata":{"license":"MIT"}`,
			nonmatch: `,"metadata":{"license":"mit"}`,
		},
		{
			name: "categories with unusable license", categories: []string{"café", "tools"},
			match:    `,"metadata":{"license":true},"@context":{"dc":"http://purl.org/dc/terms/"},"dc:subject":["TOOLS","CAFÉ"]`,
			nonmatch: `,"http://purl.org/dc/terms/subject":"tools"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			entry := func(name, fields string) string {
				return strings.TrimSuffix(livePagingEntry(name, 90), "}") + fields + "}"
			}
			match := entry("both", tc.match)
			server, router := newFederationHTTPSOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var wire struct {
					Query struct {
						Filter map[string]any `json:"filter"`
					} `json:"query"`
					PageToken string `json:"pageToken"`
				}
				want := 0
				if tc.license != "" {
					want = 1
				}
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil || len(wire.Query.Filter) != want || tc.license != "" && wire.Query.Filter["metadata.license"] != tc.license {
					t.Error("HTTPS peer received a lossy predicate", wire, err)
					http.Error(w, "invalid fixture request", http.StatusBadRequest)
					return
				}
				result := livePagingPage(t, "next", entry("nonmatch", tc.nonmatch), livePagingEntry("sparse", 99))
				if wire.PageToken == "next" {
					result = livePagingPage(t, "", match)
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write(result.Body); err != nil {
					t.Error(err)
				}
			}))
			_, store, _, sources := newLivePagingFixture(t, 3, "a")
			source := sources[0]
			source.Endpoint, source.RegistrySearchURL, source.NetworkProfile = server.URL+"/search", server.URL+"/search", "owned-https"
			store.sources[source.ID] = source
			manager, err := NewManager(store, router, clock.Real{}, id.Generator{}, WithLiveSearchPages(3))
			if err != nil {
				t.Fatal(err)
			}
			query := catalog.SearchQuery{Text: "agent", License: tc.license, Categories: tc.categories, Origins: []catalog.SearchResultType{catalog.SearchResultLive}}
			page, err := manager.SearchLiveCatalog(livePagingContext(t), catalog.FederatedCatalogSearchRequest{Query: query, Limit: 1, IncludeLive: true})
			if err != nil || calls.Load() != 2 || len(page.Live) != 1 || page.Live[0].Identifier != "urn:air:remote.example:agent:both" || string(page.Live[0].Live.Entry) != match || !page.Partial || len(page.Diagnostics) != 1 || page.Diagnostics[0].Status != "insufficient_evidence" {
				t.Fatalf("HTTPS selection=%+v calls=%d error=%v", page, calls.Load(), err)
			}
		})
	}
}

func TestLivePagingHTTPSDoesNotForwardTokensThroughRedirects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		initial bool
	}{
		{"unredirected control", 0, false},
		{"continuation 307", http.StatusTemporaryRedirect, false},
		{"continuation 308", http.StatusPermanentRedirect, false},
		{"initial 307", http.StatusTemporaryRedirect, true},
		{"initial 308", http.StatusPermanentRedirect, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls, forwarded atomic.Int32
			server, router := newFederationHTTPSOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var wire struct {
					PageToken string `json:"pageToken"`
				}
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil || r.Method != http.MethodPost {
					t.Error("invalid owned peer request", err, r.Method)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				if r.URL.Path == "/redirect-target" {
					if wire.PageToken != "" {
						forwarded.Add(1)
					}
				} else if tc.status != 0 && (tc.initial || wire.PageToken != "") {
					w.Header().Set("Location", "/redirect-target")
					w.WriteHeader(tc.status)
					return
				}
				result := livePagingPage(t, "opaque-secret-next", livePagingEntry("alpha", 90))
				if wire.PageToken != "" {
					result = livePagingPage(t, "", livePagingEntry("beta", 80))
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write(result.Body); err != nil {
					t.Error(err)
				}
			}))
			_, store, _, sources := newLivePagingFixture(t, 3, "a")
			source := sources[0]
			source.Endpoint, source.RegistrySearchURL = server.URL+"/search", server.URL+"/search"
			source.NetworkProfile = "owned-https"
			store.sources[source.ID] = source
			manager, err := NewManager(store, router, clock.Real{}, id.Generator{}, WithLiveSearchPages(3))
			if err != nil {
				t.Fatal(err)
			}
			response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true, Reason: "Verify continuation redirects"})
			want := 1
			if tc.status == 0 {
				want = 2
			}
			if err != nil || len(response.Results) != want || response.Partial != (tc.status != 0) || calls.Load() != 2 || forwarded.Load() != 0 {
				t.Fatalf("response=%+v calls=%d token forwards=%d error=%v", response, calls.Load(), forwarded.Load(), err)
			}
		})
	}
}

func TestLivePagingHTTPSRetainsEarlierPagesWhenResponseLimitsStopContinuation(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		policyLimit, first, last int
		partial                  bool
	}{
		{"exact accumulated allowance", MaxLiveResponseBytes, 3 << 20, 2 << 20, false},
		{"accumulated allowance exceeded", MaxLiveResponseBytes, 3 << 20, (2 << 20) + 1, true},
		{"lower policy allowance exceeded", 16 << 10, 1024, (16 << 10) + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server, router := newFederationHTTPSOriginWithResponseLimit(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var wire struct {
					PageToken string `json:"pageToken"`
					PageSize  int    `json:"pageSize"`
					Query     struct {
						Text string `json:"text"`
					} `json:"query"`
				}
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil || r.TLS == nil || r.Method != http.MethodPost || r.URL.Path != "/search" || wire.PageSize != 3 || wire.Query.Text != "agent" {
					t.Error("invalid owned HTTPS request", err, r.Method, r.URL.Path, wire)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				size := tc.first
				page := livePagingPage(t, "second", livePagingEntry("alpha", 90))
				switch wire.PageToken {
				case "":
				case "second":
					page = livePagingPage(t, "third", livePagingEntry("beta", 80))
				case "third":
					page = livePagingPage(t, "", livePagingEntry("gamma", 70))
					size = tc.last
				default:
					t.Error("peer was restarted or received an unexpected token")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				// Each response is valid JSON. Only the last byte count changes
				// between the aggregate control and its rejection case.
				body := strings.Repeat(" ", size-len(page.Body)) + string(page.Body)
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(body))
				if err != nil && !tc.partial {
					t.Error(err)
				}
			}), int64(tc.policyLimit))
			_, store, _, sources := newLivePagingFixture(t, 3, "a")
			source := sources[0]
			source.Endpoint, source.RegistrySearchURL = server.URL+"/search", server.URL+"/search"
			source.NetworkProfile = "owned-https"
			store.sources[source.ID] = source
			manager, err := NewManager(store, router, clock.Real{}, id.Generator{}, WithLiveSearchPages(3), WithLiveSearchTimeout(MaxLiveSearchTimeout))
			if err != nil {
				t.Fatal(err)
			}
			response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 3, Administrative: true, Reason: "Verify HTTPS response limits"})
			want := 3
			if tc.partial {
				want = 2
			}
			if err != nil || len(response.Results) != want || response.Partial != tc.partial || calls.Load() != 3 {
				t.Fatalf("response=%+v calls=%d error=%v", response, calls.Load(), err)
			}
			if response.Results[0].Identifier != "urn:air:remote.example:agent:alpha" || response.Results[1].Identifier != "urn:air:remote.example:agent:beta" || response.Results[0].SourceID != source.ID || response.Results[1].SourceID != source.ID {
				t.Fatal("earlier admitted pages or their provenance were lost", response.Results)
			}
			if tc.partial != slices.ContainsFunc(response.Diagnostics, func(d LiveDiagnostic) bool { return d.Status == "failed" }) || tc.partial != slices.ContainsFunc(response.Diagnostics, func(d LiveDiagnostic) bool { return d.Status == "continuation_unavailable" }) {
				t.Fatal("response-limit failure was not distinguished from complete paging", response.Diagnostics)
			}
		})
	}
}

func TestCoreClientPaginationReplaysOwnedHTTPSPeer(t *testing.T) {
	var calls atomic.Int32
	var outage atomic.Bool
	server, router := newFederationHTTPSOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var wire struct {
			PageSize  int    `json:"pageSize"`
			PageToken string `json:"pageToken"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil || wire.PageSize != 50 || wire.PageToken != "" {
			t.Error("unexpected peer request", wire, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if outage.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"private":"upstream-secret-sentinel"}`))
			return
		}
		result := livePagingPage(t, "", livePagingEntry("first", 90), livePagingEntry("second", 80), livePagingEntry("third", 70))
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(result.Body); err != nil {
			t.Error(err)
		}
	}))
	_, store, _, sources := newLivePagingFixture(t, 1, "a")
	source := sources[0]
	source.Endpoint, source.RegistrySearchURL, source.NetworkProfile = server.URL+"/search", server.URL+"/search", "owned-https"
	store.sources[source.ID] = source
	// This test exercises HTTPS replay, not wall-clock cursor expiry.
	serviceClock := clock.NewFake(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	manager, err := NewManager(store, router, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(t.Name()))
	local, err := catalog.NewService(catalog.Options{Repository: catalog.NewMemoryRepository(), Clock: serviceClock, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "owned-https"})
	if err != nil {
		t.Fatal(err)
	}
	search, err := catalog.NewSearchService(local, manager)
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: catalogSearchTestTenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	query := catalog.SearchQuery{Text: "agent", Limit: 1}
	seen := make([]string, 0, 3)
	for i := range 3 {
		if i == 1 {
			outage.Store(true)
			failed, err := search.Search(ctx, query)
			if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || len(failed.Items) != 0 || failed.NextCursor != "" || strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("HTTPS replay outage advanced or leaked: %+v %v", failed, err)
			}
			outage.Store(false)
		}
		page, err := search.Search(ctx, query)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("HTTPS page: %+v %v", page, err)
		}
		seen = append(seen, page.Items[0].Identifier)
		query.Cursor = page.NextCursor
	}
	if query.Cursor != "" || calls.Load() != 4 || !slices.Equal(seen, []string{"urn:air:remote.example:agent:first", "urn:air:remote.example:agent:second", "urn:air:remote.example:agent:third"}) {
		t.Fatalf("incomplete traversal: %v calls=%d", seen, calls.Load())
	}
}
