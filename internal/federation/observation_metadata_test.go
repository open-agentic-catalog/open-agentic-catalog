// Public observation projections must not expose retained document buffers.
package federation

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type metadataParentStore struct {
	Store          // Full GetObservation is deliberately unavailable to these callers.
	t              *testing.T
	parent, tenant string
	failure        error
	calls          []string
}

func (s *metadataParentStore) GetObservationMetadata(_ context.Context, tenant, parent string) (Observation, error) {
	s.calls = append(s.calls, "metadata")
	if tenant != s.tenant || parent != s.parent {
		s.t.Fatal("parent lookup lost tenant or observation")
	}
	return Observation{ID: parent, RawEntryAvailable: true}, s.failure
}

func (s *metadataParentStore) ListObservationOccurrences(_ context.Context, tenant, parent string, limit int) ([]ObservationOccurrence, int, error) {
	s.calls = append(s.calls, "preview")
	if tenant != s.tenant || parent != s.parent || limit != 50 {
		s.t.Fatal("preview lookup lost default bound or tenant parent")
	}
	return []ObservationOccurrence{{ObservationID: parent}}, 1, nil
}

func (s *metadataParentStore) ListObservationOccurrencePage(_ context.Context, tenant string, request OccurrenceListRequest) ([]ObservationOccurrence, error) {
	s.calls = append(s.calls, "page")
	if tenant != s.tenant || request.ObservationID != s.parent || request.Limit != 2 {
		s.t.Fatal("history lookup lost sentinel or tenant parent")
	}
	return []ObservationOccurrence{{ObservationID: request.ObservationID}}, nil
}

func TestOccurrenceParentChecksUseMetadata(t *testing.T) {
	for _, mode := range []string{"preview", "page"} {
		for _, failed := range []bool{false, true} {
			t.Run(mode+"/"+map[bool]string{false: "retained", true: "failure"}[failed], func(t *testing.T) {
				store := &metadataParentStore{t: t, tenant: "00000000-0000-7000-8000-000000000001", parent: "00000000-0000-7000-8000-000000000002"}
				if failed {
					store.failure = errors.New("parent metadata failure")
				}
				manager := &Manager{store: store}
				ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: store.tenant, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
				var items []ObservationOccurrence
				var err error
				if mode == "preview" {
					var total int
					items, total, err = manager.ObservationOccurrences(ctx, store.parent, 0)
					if !failed && total != 1 || failed && total != 0 {
						t.Fatal("preview count changed")
					}
				} else {
					items, err = manager.ObservationOccurrencePage(ctx, OccurrenceListRequest{ObservationID: store.parent, Limit: 1})
				}
				wantCalls := []string{"metadata"}
				if failed {
					if !errors.Is(err, store.failure) || items != nil {
						t.Fatalf("parent failure lost: %v", err)
					}
				} else {
					wantCalls = append(wantCalls, mode)
					if err != nil || len(items) != 1 || items[0].ObservationID != store.parent {
						t.Fatalf("history: %+v %v", items, err)
					}
				}
				if !reflect.DeepEqual(store.calls, wantCalls) {
					t.Fatalf("read calls: %v", store.calls)
				}
			})
		}
	}
}

func TestObservationMetadataKeepsExactReadsIndependent(t *testing.T) {
	for _, mode := range []string{"get", "list", "group", "search"} {
		t.Run(mode, func(t *testing.T) {
			store := NewMemoryStore()
			raw := json.RawMessage(`{"private":"` + strings.Repeat("x", 1024*1024) + `"}`)
			now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
			original := Observation{ID: "one", TenantID: "tenant", SourceID: "source", SnapshotID: "snapshot", SourceKey: "entry-one", Identifier: "urn:air:peer.example:agents:one", Version: "1", VersionPresent: true,
				Kind: catalog.KindAgent, MediaType: "application/json", Completeness: CompleteEntry,
				RawEntry: raw, NormalizedEntry: raw, ExactEntryDigest: observationDigest(raw), IdentityDigest: "identity", NormalizedEntryDigest: observationDigest(raw), NormalizationProfile: "profile",
				Freshness: FreshCurrent, SearchDisplayName: "Example", SearchDescription: "Metadata example", SearchPublisher: "peer.example", SearchTags: []string{"tag"}, SearchQueries: []string{"query"},
				FirstSeenAt: now, LastSeenAt: now.Add(time.Hour), Occurrence: ObservationOccurrence{ObservationID: "one", SnapshotID: "snapshot", EntryLocation: "/entries/0"},
				Compatibility: Compatibility{Structural: "valid", ProfileID: "profile", Support: "supported", Findings: []catalog.Finding{{Code: "retained"}}}}
			store.sources["source"] = Source{ID: "source", TenantID: "tenant", Status: SourceActive, RetrievalMode: RetrievalIndexed, Scope: Scope{Identifiers: []string{original.Identifier}}}
			store.observations["first"] = original
			second := original
			second.ID = "two"
			store.observations["second"] = second
			var item Observation
			var err error
			switch mode {
			case "get":
				item, err = store.GetObservationMetadata(t.Context(), original.TenantID, original.ID)
			case "list", "group":
				var items []Observation
				if mode == "list" {
					items, err = store.ListObservationPage(t.Context(), original.TenantID, ObservationListRequest{Limit: 1})
				} else {
					items, err = store.ListObservationGroupMembers(t.Context(), original.TenantID, original.Identifier, original.Version, true, 1)
				}
				if err != nil || len(items) != 1 {
					t.Fatalf("metadata page: %d, %v", len(items), err)
				}
				for _, accessible := range items[:cap(items)] {
					if len(accessible.RawEntry)+len(accessible.NormalizedEntry) != 0 {
						t.Fatal("page capacity exposes retained documents")
					}
				}
				item = items[0]
			case "search":
				var items []SearchResult
				items, err = store.SearchObservationPage(t.Context(), original.TenantID, ObservationSearchRequest{Limit: 1})
				if err != nil || len(items) != 1 {
					t.Fatalf("search page: %d, %v", len(items), err)
				}
				for _, accessible := range items[:cap(items)] {
					if len(accessible.Observation.RawEntry)+len(accessible.Observation.NormalizedEntry) != 0 {
						t.Fatal("search capacity exposes retained documents")
					}
				}
				item = items[0].Observation
			}
			// All other metadata must survive; availability derives from retained
			// bytes, not the cached boolean on the stored value.
			want := original
			want.RawEntry, want.NormalizedEntry, want.Occurrence = nil, nil, ObservationOccurrence{}
			want.RawEntryAvailable = true
			if err != nil || !reflect.DeepEqual(item, want) {
				t.Fatalf("metadata changed evidence contract: %v", err)
			}
			item.SearchTags[0], item.SearchQueries[0], item.Compatibility.Findings[0].Code = "changed", "changed", "changed"
			retained, err := store.GetObservation(t.Context(), original.TenantID, original.ID)
			if err != nil || !reflect.DeepEqual(retained, original) || retained.SearchTags[0] != "tag" || retained.SearchQueries[0] != "query" || retained.Compatibility.Findings[0].Code != "retained" {
				t.Fatalf("metadata read changed retained state: %v", err)
			}
		})
	}
}

func TestObservationSummaryLatestOrdering(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	for _, dimension := range []string{"observed time", "snapshot", "retrieval", "location"} {
		t.Run(dimension, func(t *testing.T) {
			older := ObservationOccurrence{ObservationID: "observation", SourceID: "source", SnapshotID: "snapshot-z", RetrievalDigest: "digest-z", EntryLocation: "/entries/2", ObservedAt: now}
			latest := older
			switch dimension {
			case "observed time":
				latest.ObservedAt, latest.SnapshotID = now.Add(time.Second), "snapshot-a"
			case "snapshot":
				older.SnapshotID, older.RetrievalDigest = "snapshot-a", "digest-a"
			case "retrieval":
				latest.RetrievalDigest, older.EntryLocation = "digest-a", "/entries/1"
			case "location":
				latest.EntryLocation = "/entries/10" // Byte order, not numeric array index.
			}
			store := NewMemoryStore()
			for _, item := range []ObservationOccurrence{latest, older} {
				store.occurrences[occurrenceKey("tenant", item)] = item
			}
			got, err := store.ObservationOccurrenceSummaries(t.Context(), "tenant", []string{"observation"})
			if err != nil || !reflect.DeepEqual(got, map[string]OccurrenceSummary{"observation": {Latest: latest, Total: 2}}) {
				t.Fatalf("latest tuple/count lost %s precedence: %+v, %v", dimension, got, err)
			}
		})
	}
}

func TestObservationSummaryBatchBoundsAndIsolation(t *testing.T) {
	store := NewMemoryStore()
	if result, err := store.ObservationOccurrenceSummaries(t.Context(), "tenant", nil); err != nil || len(result) != 0 {
		t.Fatalf("empty selection: %v", err)
	}
	if _, err := store.ObservationOccurrenceSummaries(t.Context(), "tenant", make([]string, MaxObservationGroupContributions+1)); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("unbounded selection: %v", err)
	}
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	for _, tenant := range []string{"tenant", "other"} {
		for _, observation := range []string{"first", "second"} {
			for _, snapshot := range []string{"older", "newer"} {
				at := now
				if snapshot == "newer" {
					at = at.Add(time.Hour)
				}
				if tenant == "other" {
					at = at.Add(time.Hour)
				}
				item := ObservationOccurrence{ObservationID: observation, SnapshotID: snapshot, EntryLocation: "/" + observation, ObservedAt: at}
				store.occurrences[occurrenceKey(tenant, item)] = item
			}
		}
	}
	summaries, err := store.ObservationOccurrenceSummaries(t.Context(), "tenant", []string{"first", "second"})
	if err != nil || len(summaries) != 2 {
		t.Fatalf("batch: %v", err)
	}
	for _, key := range []string{"first", "second"} {
		if got := summaries[key]; got.Total != 2 || got.Latest.ObservationID != key || got.Latest.SnapshotID != "newer" || !got.Latest.ObservedAt.Equal(now.Add(time.Hour)) {
			t.Fatalf("incorrect count/latest: %+v", got)
		}
	}
	if partial, err := store.ObservationOccurrenceSummaries(t.Context(), "tenant", []string{"first", "missing"}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound || partial != nil {
		t.Fatalf("partial summary accepted: %v", err)
	}
}
