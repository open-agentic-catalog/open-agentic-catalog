package publication

import (
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

func TestPublicLiveCompositionPreservesOffsetsOrderingAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	m := &Manager{cursorKey: []byte(strings.Repeat("k", 32)), clock: clock.NewFake(now)}
	local := SearchResult{Entry: Entry{Identifier: "urn:air:local.example:agent:a"}, Score: 60, Source: "https://local.example/ard/v1/"}
	live := []SearchResult{{Entry: Entry{Identifier: "urn:air:remote.example:agent:a"}, Score: 90, Source: "https://remote.example/ard/v1/"}, {Entry: Entry{Identifier: "urn:air:remote.example:agent:b"}, Score: 60, Source: "https://remote.example/ard/v1/"}, {Entry: Entry{Identifier: "urn:air:remote.example:agent:c"}, Score: 10, Source: "https://remote.example/ard/v1/"}}
	fp := strings.Repeat("a", 64)
	state := searchPageState{generation: 1, binding: "binding", baseBinding: "binding", limit: 1, total: 1, count: 1, expiresAt: now.Add(15 * time.Minute).Unix()}
	names := []string{live[0].Identifier, local.Identifier, live[1].Identifier, live[2].Identifier}
	for i, want := range names {
		response := SearchResponse{Tier: "core", page: state, local: map[string]struct{}{local.Identifier + "\x00": {}}}
		if state.offset == 0 {
			response.Results = []SearchResult{local}
		}
		page, err := m.ContinueLiveSearch(response, live, fp)
		if err != nil || len(page.Results) != 1 || page.Results[0].Identifier != want {
			t.Fatalf("page %d: %+v %v", i, page, err)
		}
		if i == len(names)-1 {
			if page.PageToken != "" {
				t.Fatal("extra final token")
			}
			break
		}
		cursor, err := catalog.DecodeLiveCursor(page.PageToken, "public", m.cursorKey, "binding", now)
		if err != nil || cursor.ExpiresAt != state.expiresAt || cursor.Offset != map[bool]int{true: 1, false: 0}[i > 0] {
			t.Fatalf("position/expiry: %+v %v", cursor, err)
		}
		state.live = &cursor
		state.offset = cursor.Offset
	}
	// A failed replay must not return a replacement token or usable partial page.
	base := SearchResponse{Tier: "core", page: state}
	if page, err := m.ContinueLiveSearch(base, live, ""); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || page.PageToken != "" {
		t.Fatalf("outage: %+v %v", page, err)
	}
	if _, err := m.ContinueLiveSearch(base, live, strings.Repeat("b", 64)); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("changed window: %v", err)
	}
}

func TestPublicUnsafeFirstWindowHasOnlyDurableContinuation(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	m := &Manager{cursorKey: []byte(strings.Repeat("k", 32)), clock: clock.NewFake(now)}
	response := SearchResponse{Tier: "core", page: searchPageState{generation: 1, binding: "binding", baseBinding: "binding", limit: 1, total: 1, count: 1, expiresAt: now.Add(time.Minute).Unix()}}
	live := []SearchResult{{Entry: Entry{Identifier: "a"}, Score: 100}, {Entry: Entry{Identifier: "b"}, Score: 90}}
	page, err := m.ContinueLiveSearch(response, live, "")
	if err != nil || len(page.Results) != 1 || !page.Partial || len(page.Diagnostics) != 1 || page.PageToken == "" || strings.HasPrefix(page.PageToken, catalog.LiveCursorPrefix) {
		t.Fatalf("unsafe window: %+v %v", page, err)
	}
	offset, err := m.decodeCursor(page.PageToken, 1, "binding")
	if err != nil || offset != 0 {
		t.Fatalf("displaced local offset: %d %v", offset, err)
	}
	if response.NeedsLiveReplay(false) || !(SearchResponse{Tier: "core"}).NeedsLiveReplay(true) || !(SearchResponse{Tier: "hybrid"}).NeedsLiveReplay(true) {
		t.Fatal("batch boundaries")
	}
}

func TestPublicLiveCompositionRejectsInvalidProgressAndKeepsTieOrder(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	m := &Manager{cursorKey: []byte(strings.Repeat("k", 32)), clock: clock.NewFake(now)}
	expiry := now.Add(time.Minute).Unix()
	fp := strings.Repeat("a", 64)
	response := SearchResponse{Tier: "core", page: searchPageState{limit: 3, generation: 1, binding: "binding", baseBinding: "binding", expiresAt: expiry}}
	if _, err := m.ContinueLiveSearch(SearchResponse{}, nil, fp); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("invalid page: %v", err)
	}
	bad := response
	bad.page.live = &catalog.LiveCursor{LiveOffset: 2, Fingerprint: fp}
	if _, err := m.ContinueLiveSearch(bad, nil, fp); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("offset: %v", err)
	}
	// Same identifier with distinct versions/sources exercises public tie order.
	result := func(version, source string) SearchResult {
		return SearchResult{Entry: Entry{Identifier: "urn:air:remote.example:agent:a", Version: version}, Score: 90, Source: source}
	}
	got, err := m.ContinueLiveSearch(response, []SearchResult{result("2", "https://b.example/"), result("1", "https://c.example/"), result("1", "https://b.example/")}, fp)
	if err != nil || got.Results[0].Source != "https://b.example/" || got.Results[0].Version != "1" || got.Results[2].Version != "2" {
		t.Fatalf("ties: %+v %v", got, err)
	}
}

func TestPublicHybridLiveTailRetainsRankingAndFinalPageToken(t *testing.T) {
	m, f, _ := newPublicHybridFixture(t)
	query := SearchRequest{Query: SearchQuery{Text: "weather", Filter: map[string]any{"tags": []any{"selected"}}}, PageSize: 1, Federation: "auto"}
	// Obtain the independent durable-only order first. Live score 100 must not
	// reorder it, even where precise ranks round to equal displayed scores.
	durableQuery := query
	durableQuery.Federation = "none"
	want := []string{}
	for {
		page, err := m.Search(t.Context(), "tenant-1", durableQuery)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Results {
			want = append(want, item.Identifier)
		}
		if page.PageToken == "" {
			break
		}
		durableQuery.PageToken = page.PageToken
	}
	if len(want) < 2 {
		t.Fatal("populated hybrid control required")
	}
	live := []SearchResult{{Entry: Entry{Identifier: "urn:air:peer.example:agent:a"}, Score: 100, Source: "https://peer.example/ard/v1/"}, {Entry: Entry{Identifier: "urn:air:peer.example:agent:b"}, Score: 99, Source: "https://peer.example/ard/v1/"}}
	want = append(want, live[0].Identifier, live[1].Identifier)
	fp := strings.Repeat("a", 64)
	for i, id := range want {
		page, err := m.Search(t.Context(), "tenant-1", query)
		if err != nil || page.Tier != "hybrid" || !page.NeedsLiveReplay(query.PageToken == "") {
			t.Fatalf("page admission %d: %+v %v", i, page, err)
		}
		if query.PageToken != "" {
			if _, err := m.ContinueLiveSearch(page, live, ""); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
				t.Fatalf("replay outage: %v", err)
			}
		}
		merged, err := m.ContinueLiveSearch(page, live, fp)
		if err != nil || len(merged.Results) != 1 || merged.Results[0].Identifier != id {
			t.Fatalf("page %d: %+v %v", i, merged, err)
		}
		if i == len(want)-1 {
			if merged.PageToken != "" {
				t.Fatal("extra final page")
			}
			break
		}
		if !strings.HasPrefix(merged.PageToken, catalog.LiveCursorPrefix) {
			t.Fatal("missing tail token")
		}
		query.PageToken = merged.PageToken
	}
	// Ranking changes require a restart even when only the live tail remains.
	f.vector = []float32{0, 1}
	if _, err := m.Search(t.Context(), "tenant-1", query); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("ranking drift on tail: %v", err)
	}
}
