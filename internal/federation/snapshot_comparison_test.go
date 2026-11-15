// Isolated historical comparisons exercise retained relationships without a
// server, synchronization, database or native-document decoder.
package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// comparisonMemoryFixture builds only the immutable evidence needed by the
// read, deliberately leaving current source configuration absent.
func comparisonMemoryFixture() (*MemoryStore, Snapshot, Snapshot) {
	m := NewMemoryStore()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	before := Snapshot{ID: "00000000-0000-7000-8000-000000000101", TenantID: "tenant", SourceID: "source", SourceRevision: 2, State: SnapshotComplete, EnumerationComplete: true, StartedAt: now, CompletedAt: &now}
	after := before
	after.ID = "00000000-0000-7000-8000-000000000102"
	after.StartedAt = now.Add(time.Hour)
	after.CompletedAt = &after.StartedAt
	m.snapshots[before.ID], m.snapshots[after.ID] = before, after
	return m, before, after
}

// addComparisonMember retains an exact occurrence, including reused observations
// whose immutable creation snapshot differs from the selected after snapshot.
func addComparisonMember(m *MemoryStore, s Snapshot, id, key, raw, input, normalized string, versionPresent bool) {
	o := Observation{ID: id, TenantID: s.TenantID, SourceID: s.SourceID, SnapshotID: s.ID, SourceKey: key, Identifier: key, VersionPresent: versionPresent, Kind: catalog.KindAgent, ExactEntryDigest: observationDigest([]byte(raw)), NormalizedEntryDigest: observationDigest([]byte(normalized)), FirstSeenAt: s.StartedAt, LastSeenAt: s.StartedAt, RawEntry: json.RawMessage(`{"private":"native-sentinel"}`)}
	o.IdentityDigest = observationIdentityDigest(o.ExactEntryDigest, input)
	for _, previous := range m.observations {
		if previous.ID == id {
			o.SnapshotID = previous.SnapshotID
			o.FirstSeenAt = previous.FirstSeenAt
		}
	}
	o.Freshness = FreshWithdrawn // Current freshness is irrelevant to the selected membership.
	m.observations[observationKey(o)] = o
	snapshot := m.snapshots[s.ID]
	occ := ObservationOccurrence{ObservationID: id, SourceID: s.SourceID, SnapshotID: s.ID, SourceRevision: s.SourceRevision, RetrievalDigest: observationDigest([]byte(s.ID)), EntryLocation: fmt.Sprintf("/entries/%d", snapshot.MemberCount), ExactEntryDigest: o.ExactEntryDigest, NormalizedEntryDigest: o.NormalizedEntryDigest, NormalizationInputDigest: input, EvidenceComplete: true, ObservedAt: s.StartedAt}
	m.occurrences[occurrenceKey(s.TenantID, occ)] = occ
	snapshot.MemberCount++
	m.snapshots[s.ID] = snapshot
}

func TestSnapshotComparisonUsesSelectedOccurrences(t *testing.T) {
	m, b, a := comparisonMemoryFixture()
	addComparisonMember(m, b, "reused", "reused", "same", "", "same", false)
	addComparisonMember(m, a, "reused", "reused", "same", "", "same", false)
	addComparisonMember(m, b, "old", "edited", "old", "", "old", false)
	addComparisonMember(m, a, "new", "edited", "new", "", "new", false)
	addComparisonMember(m, b, "gone", "absent", "gone", "", "gone", false)
	addComparisonMember(m, a, "added", "added", "added", "", "added", false)
	addComparisonMember(m, b, "input-old", "context", "same bytes", "", "same normalized", false)
	addComparisonMember(m, a, "input-new", "context", "same bytes", observationDigest([]byte("new context")), "same normalized", false)
	addComparisonMember(m, b, "normalized-old", "normalization", "same", "", "old normalization", false)
	addComparisonMember(m, a, "normalized-new", "normalization", "same", observationDigest([]byte("normalizer context")), "new normalization", false)
	want := map[string]string{"reused": "unchanged", "edited": "changed", "absent": "absent", "added": "added", "context": "changed", "normalization": "changed"}
	got, err := m.CompareSnapshots(t.Context(), b.TenantID, SnapshotComparisonRequest{BeforeID: b.ID, AfterID: a.ID, IncludeUnchanged: true, Limit: 100})
	if err != nil || len(got.Items) != len(want) {
		t.Fatalf("comparison=%+v err=%v", got, err)
	}
	for _, item := range got.Items {
		if item.Change != want[item.SourceKey] {
			t.Fatalf("unexpected change %+v", item)
		}
		if item.SourceKey == "reused" && (item.Before.ObservationID != item.After.ObservationID || item.Before.Occurrence.SnapshotID != b.ID || item.After.Occurrence.SnapshotID != a.ID) {
			t.Fatal("reuse lost exact membership")
		}
		if item.SourceKey == "context" && (item.Before.Occurrence.ExactEntryDigest != item.After.Occurrence.ExactEntryDigest || item.Before.IdentityDigest == item.After.IdentityDigest) {
			t.Fatal("normalization-input difference lost")
		}
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"native-sentinel", "freshness", "lastSeenAt", "credential", "RawEntry"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("summary disclosed %s", private)
		}
	}
	request := SnapshotComparisonRequest{BeforeID: b.ID, AfterID: a.ID, Limit: 1}
	var paged []SnapshotEntryChange
	for {
		page, err := m.CompareSnapshots(t.Context(), b.TenantID, request)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 0 {
			break
		}
		paged = append(paged, page.Items...)
		request.AfterPosition = page.Items[0].Position
	}
	if len(paged) != len(want)-1 {
		t.Fatalf("changed-only paging lost entries: %+v", paged)
	}
	for i, item := range paged {
		if item.Change == "unchanged" || item.Position != i+1 {
			t.Fatal("unstable filtered positions")
		}
	}
}

func TestSnapshotComparisonPreservesMultipleOccurrencesAndVersionPresence(t *testing.T) {
	m, b, a := comparisonMemoryFixture()
	addComparisonMember(m, b, "one", "duplicate", "first", "", "first", false)
	addComparisonMember(m, b, "two", "duplicate", "conflict", "", "conflict", false)
	addComparisonMember(m, a, "one", "duplicate", "first", "", "first", false)
	identifier := "urn:air:source.example:agent"
	missing := sourceScopedObservationKey(identifier, "", false, "", "", "")
	empty := sourceScopedObservationKey(identifier, "", true, "", "", "")
	addComparisonMember(m, b, "missing", missing, "unversioned", "", "unversioned", false)
	addComparisonMember(m, a, "empty", empty, "empty version", "", "empty version", true)
	got, err := m.CompareSnapshots(t.Context(), b.TenantID, SnapshotComparisonRequest{BeforeID: b.ID, AfterID: a.ID, Limit: 100})
	if err != nil || len(got.Items) != 5 {
		t.Fatalf("comparison=%+v %v", got, err)
	}
	counts := map[string]int{}
	for _, item := range got.Items {
		counts[item.Change]++
		if item.SourceKey == "duplicate" && (item.Change != "multiple" || item.BeforeCount != 2 || item.AfterCount != 1 || (item.Before == nil) == (item.After == nil)) {
			t.Fatal("duplicate evidence was arbitrarily paired")
		}
	}
	if !reflect.DeepEqual(counts, map[string]int{"multiple": 3, "absent": 1, "added": 1}) {
		t.Fatalf("counts=%v", counts)
	}
}

func TestSnapshotComparisonRefusesUnavailableEvidence(t *testing.T) {
	for _, mode := range []string{"different source", "different revision", "failed", "incomplete", "missing", "foreign tenant", "count", "digest", "missing observation", "work", "text", "cancelled", "revision cursor"} {
		t.Run(mode, func(t *testing.T) {
			m, b, a := comparisonMemoryFixture()
			addComparisonMember(m, b, "entry", "entry", "entry", "", "entry", false)
			request := SnapshotComparisonRequest{BeforeID: b.ID, AfterID: a.ID, Limit: 100}
			ctx := t.Context()
			code := catalog.CodeDependencyUnavailable
			s := m.snapshots[a.ID]
			switch mode {
			case "different source":
				s.SourceID = "other"
				code = catalog.CodeInvalidArgument
			case "different revision":
				s.SourceRevision++
				code = catalog.CodeInvalidArgument
			case "failed":
				s.State = SnapshotFailed
				s.EnumerationComplete = false
				code = catalog.CodePreconditionFailed
			case "incomplete":
				s.State = SnapshotIncomplete
				s.EnumerationComplete = false
				code = catalog.CodePreconditionFailed
			case "missing":
				request.AfterID = "missing"
				code = catalog.CodeNotFound
			case "foreign tenant":
				s.TenantID = "other"
				code = catalog.CodeNotFound
			case "count":
				s.MemberCount = 1
			case "digest":
				for k, o := range m.occurrences {
					o.ExactEntryDigest = observationDigest([]byte("corrupt"))
					m.occurrences[k] = o
				}
			case "missing observation":
				clear(m.observations)
			case "work":
				s.MemberCount = MaxSynchronizationEntries + 1
				code = catalog.CodePreconditionFailed
			case "text":
				for k, o := range m.observations {
					o.Identifier = strings.Repeat("x", MaxSnapshotComparisonTextBytes+1)
					m.observations[k] = o
				}
				code = catalog.CodePreconditionFailed
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "revision cursor":
				request.ExpectedSourceRevision = 3
				code = catalog.CodeInvalidArgument
			}
			m.snapshots[a.ID] = s
			got, err := m.CompareSnapshots(ctx, b.TenantID, request)
			if err == nil || got.SourceID != "" || got.Items != nil {
				t.Fatalf("partial comparison returned: %+v %v", got, err)
			}
			if mode != "cancelled" && catalog.ErrorCodeOf(err) != code {
				t.Fatalf("error=%v want=%s", err, code)
			}
		})
	}
}

func TestSnapshotComparisonCompleteEmptyAndSameSelection(t *testing.T) {
	m, b, a := comparisonMemoryFixture()
	for _, after := range []string{a.ID, b.ID} {
		got, err := m.CompareSnapshots(t.Context(), b.TenantID, SnapshotComparisonRequest{BeforeID: b.ID, AfterID: after, Limit: 1})
		if err != nil || got.Items == nil || len(got.Items) != 0 || got.Before.MemberCount != 0 || got.After.MemberCount != 0 {
			t.Fatalf("empty complete=%+v %v", got, err)
		}
	}
}
