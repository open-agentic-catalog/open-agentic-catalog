package catalog

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestCoreLiveWindowTraversesEveryResultWithWholeSetPrecedence(t *testing.T) {
	t.Parallel()
	locals := []CatalogSummary{unifiedLocalRecord("middle", "urn:air:local.example:agent:middle", "Middle", 60), unifiedLocalRecord("last", "urn:air:local.example:agent:last", "Last", 1)}
	live := make([]CatalogSearchResult, 0, 50)
	for i := range 50 {
		live = append(live, CatalogSearchResult{ID: fmt.Sprintf("live:%02d", i), Identifier: fmt.Sprintf("urn:air:remote.example:agent:item-%02d", i), Type: SearchResultLive, DisplayName: fmt.Sprintf("Remote %02d", i), Score: 100 - i, Live: &LiveSearchEvidence{SourceID: "source"}})
	}
	// A high-ranked live duplicate of a durable result outside the first page
	// must never displace that lower-ranked matching durable winner.
	live[0].Identifier, live[0].Version, live[0].VersionPresent = locals[1].Resource.CanonicalIdentifier, "1", true
	normalReads, probes, outbound := 0, 0, 0
	repo := &unifiedSearchRepository{search: func(_ context.Context, _ string, r CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		normalReads++
		items := slices.Clone(locals)
		if r.After != nil {
			items = recordsAfterPosition(items, *r.After)
		}
		return CatalogSearchRepositoryPage{Items: items, Generation: "42", Total: 2}, nil
	}, probe: func(_ context.Context, _ string, r CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		probes++
		if len(r.IdentityProbes) != 50 || r.After != nil || r.Limit != 0 || r.ExpectedGeneration != "42" {
			t.Fatalf("unbounded or unfenced probe: %+v", r)
		}
		return CatalogSearchRepositoryPage{Generation: "42", Matches: []IndexedIdentity{{locals[1].Resource.CanonicalIdentifier, "1", true}}}, nil
	}}
	federation := unifiedFederatedSearcher{search: func(_ context.Context, r FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		page := FederatedCatalogSearchPage{Generation: "42"}
		if r.IncludeLive {
			outbound++
			if r.LiveLimit != 50 {
				t.Fatalf("live window=%d", r.LiveLimit)
			}
			page.Live = live
			page.ReplayFingerprint = strings.Repeat("a", 64)
		}
		return page, nil
	}}
	service := newUnifiedSearchTestService(t, repo, federation)
	query := SearchQuery{Text: "agent", Limit: 1}
	seen := []string{}
	seenIDs := map[string]bool{}
	expiry := int64(0)
	for i := 0; i < 60; i++ {
		page, err := service.Search(unifiedReaderContext(), query)
		if err != nil || len(page.Items) != 1 || page.DurableTotal != 2 {
			t.Fatalf("page %d: %+v %v", i, page, err)
		}
		itemID := page.Items[0].ID
		if seenIDs[itemID] {
			t.Fatalf("duplicate result across client pages: %s", itemID)
		}
		seenIDs[itemID] = true
		seen = append(seen, itemID)
		if page.NextCursor == "" {
			break
		}
		bindingContext := identity.WithPermissionContext(unifiedReaderContext())
		if _, err := identity.CurrentDecisionDigest(bindingContext); err != nil {
			t.Fatal(err)
		}
		binding, _ := service.local.unifiedSearchCursorBinding(bindingContext, mustActor(t), query)
		state, err := DecodeLiveCursor(page.NextCursor, "control", service.local.cursorKey, binding, service.local.clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		if expiry == 0 {
			expiry = state.ExpiresAt
		}
		if state.ExpiresAt != expiry {
			t.Fatal("continuation renewed expiry")
		}
		fakeClock, ok := service.local.clock.(*mutableCatalogSearchClock)
		if !ok {
			t.Fatal("expected fake clock")
		}
		fakeClock.now = service.local.clock.Now().Add(time.Second)
		query.Cursor = page.NextCursor
	}
	// Assert the complete order independently of the production sorter. Live
	// scores descend from 99 to 51; the local result wins the score-60 name tie.
	want := make([]string, 0, 51)
	for i := 1; i < 50; i++ {
		if i == 40 {
			want = append(want, "local:middle")
		}
		want = append(want, fmt.Sprintf("live:%02d", i))
	}
	want = append(want, "local:last")
	if !slices.Equal(seen, want) {
		t.Fatalf("wrong traversal/precedence: %v", seen)
	}
	if outbound != 50 || probes != 50 || normalReads != 51 {
		t.Fatalf("live work continued after exhaustion: outbound=%d probes=%d reads=%d", outbound, probes, normalReads)
	}
}

func TestCoreLiveContinuationPreservesFailureClassification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		code ErrorCode
		want ErrorCode
	}{
		{"dependency unavailable", CodeDependencyUnavailable, CodeDependencyUnavailable},
		{"internal dependency failure", CodeInternal, CodeDependencyUnavailable},
		{"read transaction conflict", CodeConflict, CodeDependencyUnavailable},
		{"stale generation", CodeInvalidArgument, CodeInvalidArgument},
		{"permission revoked", CodeForbidden, CodeForbidden},
		{"authentication revoked", CodeUnauthenticated, CodeUnauthenticated},
	} {
		for _, probeFailure := range []bool{false, true} {
			stage := "durable page"
			if probeFailure {
				stage = "identity check"
			}
			t.Run(tc.name+"/"+stage, func(t *testing.T) {
				var failure error
				outbound := 0
				repo := &unifiedSearchRepository{search: func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
					if !probeFailure {
						return CatalogSearchRepositoryPage{Generation: "42"}, failure
					}
					return CatalogSearchRepositoryPage{Generation: "42"}, nil
				}, probe: func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
					if probeFailure {
						return CatalogSearchRepositoryPage{Generation: "42"}, failure
					}
					return CatalogSearchRepositoryPage{Generation: "42"}, nil
				}}
				f := unifiedFederatedSearcher{search: func(_ context.Context, r FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
					if r.IncludeLive {
						outbound++
					}
					return FederatedCatalogSearchPage{Generation: "42", ReplayFingerprint: strings.Repeat("a", 64), Live: []CatalogSearchResult{
						{ID: "live:first", Identifier: "urn:air:remote.example:agent:first", Type: SearchResultLive, DisplayName: "First", Score: 90},
						{ID: "live:second", Identifier: "urn:air:remote.example:agent:second", Type: SearchResultLive, DisplayName: "Second", Score: 80},
					}}, nil
				}}
				s := newUnifiedSearchTestService(t, repo, f)
				query := SearchQuery{Text: "agent", Limit: 1}
				first, err := s.Search(unifiedReaderContext(), query)
				if err != nil || first.NextCursor == "" || len(first.Items) != 1 || first.Items[0].ID != "live:first" {
					t.Fatalf("successful first page: %+v %v", first, err)
				}
				query.Cursor = first.NextCursor
				failure = E(tc.code, "private dependency sentinel")
				wantOutbound := 1
				if probeFailure {
					wantOutbound++
				}
				failed, err := s.Search(unifiedReaderContext(), query)
				if ErrorCodeOf(err) != tc.want || len(failed.Items) != 0 || failed.NextCursor != "" || outbound != wantOutbound {
					t.Fatalf("failure changed classification, advanced or contacted peer: %+v %v calls=%d", failed, err, outbound)
				}
				if tc.want == CodeDependencyUnavailable && strings.Contains(err.Error(), "sentinel") {
					t.Fatal("retryable failure exposed dependency cause")
				}
				failure = nil
				page, err := s.Search(unifiedReaderContext(), query)
				if err != nil || len(page.Items) != 1 || page.Items[0].ID != "live:second" || page.NextCursor != "" || outbound != wantOutbound+1 {
					t.Fatalf("same-token recovery: %+v %v calls=%d", page, err, outbound)
				}
			})
		}
	}
}

// Use the same already-authorized principal as the owning search tests.
func mustActor(t *testing.T) identity.Actor {
	t.Helper()
	a, ok := identity.FromContext(unifiedReaderContext())
	if !ok {
		t.Fatal("reader missing")
	}
	return a
}

func TestCoreLiveReplayFailureChangedWindowAndInvalidCursor(t *testing.T) {
	t.Parallel()
	fingerprint := strings.Repeat("a", 64)
	outbound := 0
	failure := false
	generation := "42"
	repo := &unifiedSearchRepository{search: func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		return CatalogSearchRepositoryPage{Generation: generation}, nil
	}}
	f := unifiedFederatedSearcher{search: func(_ context.Context, r FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		if r.IncludeLive {
			outbound++
		}
		page := FederatedCatalogSearchPage{Generation: generation, ReplayFingerprint: fingerprint}
		if !failure {
			for i := range 2 {
				page.Live = append(page.Live, CatalogSearchResult{ID: fmt.Sprint("live:", i), Identifier: fmt.Sprint("urn:air:remote.example:agent:", i), Type: SearchResultLive, DisplayName: "Remote", Score: 100 - i})
			}
		} else {
			page.ReplayFingerprint = ""
			page.Partial = true
		}
		return page, nil
	}}
	s := newUnifiedSearchTestService(t, repo, f)
	q := SearchQuery{Text: "agent", Limit: 1}
	first, err := s.Search(unifiedReaderContext(), q)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("overflow-only token missing: %+v %v", first, err)
	}
	q.Cursor = first.NextCursor
	for _, mutate := range []func(*SearchQuery){func(q *SearchQuery) { q.Cursor += "x" }, func(q *SearchQuery) { q.Text = "other" }, func(q *SearchQuery) { q.Limit = 2 }} {
		bad := q
		mutate(&bad)
		before := outbound
		if _, err := s.Search(unifiedReaderContext(), bad); ErrorCodeOf(err) != CodeInvalidArgument || outbound != before {
			t.Fatalf("invalid cursor caused outbound work: %v", err)
		}
	}
	for _, mutate := range []func(*identity.Actor){
		func(a *identity.Actor) { a.TenantID = "tenant-2" },
		func(a *identity.Actor) { a.Subject = "reader-2" },
		func(a *identity.Actor) { a.MembershipID = "00000000-0000-7000-8000-000000000002" },
		func(a *identity.Actor) { a.Claims = map[string]string{"scope": "catalog:read"} },
	} {
		actor := mustActor(t)
		mutate(&actor)
		before := outbound
		if _, err := s.Search(identityfixture.WithActor(context.Background(), actor), q); ErrorCodeOf(err) != CodeInvalidArgument || outbound != before {
			t.Fatalf("changed access binding caused outbound work: %v", err)
		}
	}
	failure = true
	if _, err := s.Search(unifiedReaderContext(), q); ErrorCodeOf(err) != CodeDependencyUnavailable {
		t.Fatalf("outage: %v", err)
	}
	failure = false
	retry, err := s.Search(unifiedReaderContext(), q)
	if err != nil || len(retry.Items) != 1 || retry.Items[0].ID != "live:1" || retry.NextCursor != "" {
		t.Fatalf("same-token retry: %+v %v", retry, err)
	}
	fingerprint = strings.Repeat("b", 64)
	if _, err := s.Search(unifiedReaderContext(), q); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("changed window: %v", err)
	}
	fingerprint = strings.Repeat("a", 64)
	generation = "43"
	if _, err := s.Search(unifiedReaderContext(), q); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("changed generation: %v", err)
	}
}

func TestUnshadowedLiveUsesTypedSelectedMatchesAndRejectsReadRaces(t *testing.T) {
	t.Parallel()
	key := IndexedIdentity{"urn:air:remote.example:agent:shared", "1", true}
	live := []CatalogSearchResult{{ID: "versioned", Identifier: key.Identifier, Version: "1", VersionPresent: true}, {ID: "unversioned", Identifier: key.Identifier}}
	repo := &unifiedSearchRepository{probe: func(_ context.Context, _ string, r CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		if !reflect.DeepEqual(r.IdentityProbes, []IndexedIdentity{key, {Identifier: key.Identifier}}) {
			t.Fatal("lost version presence")
		}
		return CatalogSearchRepositoryPage{Generation: "42"}, nil
	}}
	f := unifiedFederatedSearcher{probe: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		return FederatedCatalogSearchPage{Generation: "42", Matches: []IndexedIdentity{key}}, nil
	}}
	s := newUnifiedSearchTestService(t, repo, f)
	got, err := s.unshadowedLive(unifiedReaderContext(), "tenant-1", SearchQuery{Origins: []SearchResultType{SearchResultIndexed, SearchResultLive}}, live, "42", "42")
	if err != nil || len(got) != 1 || got[0].ID != "unversioned" {
		t.Fatalf("typed precedence: %+v %v", got, err)
	}
	repo.probe = func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		return CatalogSearchRepositoryPage{Generation: "43"}, nil
	}
	if _, err := s.unshadowedLive(unifiedReaderContext(), "tenant-1", SearchQuery{}, live, "42", "42"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("independent read race: %v", err)
	}
}
