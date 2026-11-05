package federation

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

func TestLiveReplayProofBindsActualEvidenceAndEmptyPeers(t *testing.T) {
	m, store, outbound, sources := newLivePagingFixture(t, 2, "a", "b")
	sourceWithBinding := store.sources[sources[0].ID]
	sourceWithBinding.CredentialBinding = "partner-reader"
	sourceWithBinding.CredentialRevision = "cached-revision"
	store.sources[sourceWithBinding.ID] = sourceWithBinding
	revision, token, changed, emptyFailure := "revision-one", "first-token", false, false
	outbound.respond = func(_ context.Context, r fetcher.Request) (fetcher.Result, error) {
		if r.URL == sources[1].RegistrySearchURL {
			if emptyFailure {
				return fetcher.Result{}, fmt.Errorf("temporary unavailable")
			}
			return livePagingPage(t, ""), nil
		}
		if livePagingToken(t, r) != "" {
			result := livePagingPage(t, "", livePagingEntry("second", 50))
			result.CredentialRevision = revision
			return result, nil
		}
		entry := livePagingEntry("first", 90)
		if changed {
			entry = strings.Replace(entry, "Useful remote agent", "Changed remote agent", 1)
		}
		result := livePagingPage(t, token, entry)
		result.CredentialRevision = revision
		result.ETag = fmt.Sprint("request-", len(outbound.requests))
		return result, nil
	}
	request := catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "agent"}, IncludeLive: true, Limit: 2, LiveLimit: catalog.LiveSearchWindow}
	read := func() catalog.FederatedCatalogSearchPage {
		t.Helper()
		page, err := m.SearchLiveCatalog(livePagingContext(t), request)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := read()
	if len(first.Live) != 2 || first.ReplayFingerprint == "" || len(outbound.requests) != 3 {
		t.Fatalf("first=%+v requests=%d", first, len(outbound.requests))
	}
	token = "different-opaque-token"
	same := read()
	if same.ReplayFingerprint != first.ReplayFingerprint {
		t.Fatal("opaque token/receipt changed content proof")
	}
	revision = "revision-two"
	rotated := read()
	if rotated.ReplayFingerprint == first.ReplayFingerprint || rotated.ReplayFingerprint == "" {
		t.Fatal("actual revisions not fenced")
	}
	revision = "revision-one"
	changed = true
	if read().ReplayFingerprint == first.ReplayFingerprint {
		t.Fatal("entry change not fenced")
	}
	changed = false
	emptyFailure = true
	unavailable := read()
	if unavailable.ReplayFingerprint != "" || len(unavailable.Live) != 2 {
		t.Fatal("failed empty peer should prevent replay while preserving healthy results")
	}
	emptyFailure = false
	if read().ReplayFingerprint != first.ReplayFingerprint {
		t.Fatal("recovery changed stable proof")
	}
	source := store.sources[sources[1].ID]
	source.Revision++
	store.sources[source.ID] = source
	if read().ReplayFingerprint == first.ReplayFingerprint {
		t.Fatal("empty peer revision omitted")
	}
}

func TestLiveReplayBoundedCapsDoNotDisableProof(t *testing.T) {
	m, _, outbound, _ := newLivePagingFixture(t, 1, "a")
	outbound.respond = func(context.Context, fetcher.Request) (fetcher.Result, error) {
		entries := make([]string, 0, 50)
		for i := range 50 {
			entries = append(entries, livePagingEntry(fmt.Sprintf("item-%02d", i), 100-i))
		}
		return livePagingPage(t, "more-results", entries...), nil
	}
	page, err := m.SearchLiveCatalog(livePagingContext(t), catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "agent"}, IncludeLive: true, Limit: 1, LiveLimit: 50})
	if err != nil || len(page.Live) != 50 || page.ReplayFingerprint == "" || !page.Partial || len(outbound.requests) != 1 {
		t.Fatalf("bounded replay: %+v %v", page, err)
	}
}

func TestPeerContinuationExactSearchLimit(t *testing.T) {
	for _, size := range []int{4096, 4097} {
		_, err := decodeLiveResponse([]byte(`{"results":[],"pageToken":"` + strings.Repeat("x", size) + `"}`))
		if (err == nil) != (size == 4096) {
			t.Fatalf("peer token %d: %v", size, err)
		}
	}
}

func TestLiveReplayRefusesUnverifiedTraversal(t *testing.T) {
	m, _, _, sources := newLivePagingFixture(t, 1, "a")
	response := LiveSearchResponse{}
	peer := newLiveSearchPeer(sources[0], 0, 0)
	peer.validPage = true
	peer.pages = 1
	peer.replayFetches = []liveReplayFetch{{Policy: federationTestPolicyDigest}}
	proof := m.replayFingerprint(t.Context(), sources, []*liveSearchPeer{peer}, response)
	if proof == "" {
		t.Fatal("verified empty control missing proof")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if m.replayFingerprint(ctx, sources, []*liveSearchPeer{peer}, response) != "" {
		t.Fatal("canceled traversal has proof")
	}
	peer.invalid = true
	if m.replayFingerprint(t.Context(), sources, []*liveSearchPeer{peer}, response) != "" {
		t.Fatal("revoked source has proof")
	}
	peer.invalid = false
	peer.replayFetches = nil
	if m.replayFingerprint(t.Context(), sources, []*liveSearchPeer{peer}, response) != "" {
		t.Fatal("missing fetch evidence has proof")
	}
}

func TestCoreLiveTopologyOutagePreservesCompletedDurablePage(t *testing.T) {
	m, store, outbound, _ := newLivePagingFixture(t, 1, "a")
	m.store = &catalogSearchFailureStore{MemoryStore: store, liveErr: catalog.E(catalog.CodeDependencyUnavailable, "private topology failure sentinel")}
	durable := catalog.FederatedCatalogSearchPage{Generation: "42", IndexedTotal: 1, Indexed: []catalog.CatalogSearchResult{{ID: "indexed:one", Type: catalog.SearchResultIndexed}}}
	request := catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "agent"}, IncludeLive: true, Limit: 1, LiveLimit: 50}
	page, err := m.searchLiveCatalog(livePagingContext(t), request, durable)
	if err != nil || page.Generation != "42" || page.IndexedTotal != 1 || len(page.Indexed) != 1 || !page.Partial || page.ReplayFingerprint != "" || len(page.Diagnostics) != 1 || len(outbound.requests) != 0 || strings.Contains(page.Diagnostics[0].Message, "sentinel") {
		t.Fatalf("initial outage erased durable results: %+v %v", page, err)
	}
}
