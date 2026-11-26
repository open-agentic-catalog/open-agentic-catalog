package federation

import (
	"context"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type upstreamLocalFixture struct {
	*catalog.MemoryRepository
	resources map[string]*catalog.AdoptedResourceInventory
}

func (f upstreamLocalFixture) AdoptedResourceInventories(context.Context, string, string, string) (map[string]*catalog.AdoptedResourceInventory, error) {
	return f.resources, nil
}

func TestUpstreamChangesSelectExactCompleteEvidence(t *testing.T) {
	const tenant = "tenant"
	const identifier = "urn:air:remote.example:agents:sample"
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	resource := &catalog.AdoptedResourceInventory{ResourceID: "resource", NamespaceID: "ns", Kind: catalog.KindAgent, Labels: map[string]bool{"alpha": true, "local-draft": true}, Proofs: map[string]catalog.AdoptedVersionProof{}}
	store.BindLocalCatalog(upstreamLocalFixture{catalog.NewMemoryRepository(), map[string]*catalog.AdoptedResourceInventory{identifier: resource}})
	source := Source{ID: "source", TenantID: tenant, Status: SourceError, RetrievalMode: RetrievalHybrid, Revision: 2, LastCompleteSnapshotID: "complete"}
	store.sources[source.ID] = source
	complete := Snapshot{ID: "complete", SourceID: source.ID, TenantID: tenant, State: SnapshotComplete, EnumerationComplete: true, SourceRevision: 1, CompletedAt: &now}
	store.snapshots[complete.ID] = complete
	priorSnap := complete
	priorSnap.ID = "prior"
	store.snapshots[priorSnap.ID] = priorSnap
	later := now.Add(time.Hour)
	store.snapshots["failed"] = Snapshot{ID: "failed", SourceID: source.ID, TenantID: tenant, State: SnapshotFailed, SourceRevision: 2, CompletedAt: &later}
	add := func(id, label, normalized, profile, snapshot string) (Observation, ObservationOccurrence) {
		t.Helper()
		obs := Observation{ID: id, TenantID: tenant, Identifier: identifier, SourceID: source.ID, SnapshotID: snapshot, Kind: catalog.KindAgent, MediaType: "application/a2a-agent-card+json", Version: label, VersionPresent: true, Compatibility: Compatibility{Structural: "valid", Support: "supported"}, Completeness: CompleteEntry, Freshness: FreshCurrent, NormalizationProfile: profile, ExactEntryDigest: observationDigest([]byte(id)), NormalizedEntryDigest: observationDigest([]byte(normalized))}
		if snapshot == "prior" {
			obs.Freshness = FreshWithdrawn
		}
		store.observations[id] = obs
		occ := ObservationOccurrence{ObservationID: id, SourceID: source.ID, SnapshotID: snapshot, SourceRevision: 1, EntryLocation: "/entries/1", ExactEntryDigest: obs.ExactEntryDigest, NormalizedEntryDigest: obs.NormalizedEntryDigest, RetrievalDigest: observationDigest([]byte(id + " retrieval")), EvidenceComplete: true, ObservedAt: now}
		store.occurrences[occurrenceKey(tenant, occ)] = occ
		return obs, occ
	}
	proof := func(id, label, digest, profile string) {
		o, c := add("old-"+id, label, digest, profile, "prior")
		resource.Proofs[id] = catalog.AdoptedVersionProof{LocalVersion: label, AdoptionProvenance: catalog.AdoptionProvenance{ObservationID: o.ID, SourceID: o.SourceID, SnapshotID: c.SnapshotID, SourceRevision: c.SourceRevision, EntryLocation: c.EntryLocation, RetrievalDigest: c.RetrievalDigest, ExactEntryDigest: c.ExactEntryDigest, NormalizedEntryDigest: c.NormalizedEntryDigest, NormalizationProfile: profile}}
	}
	proof("proof-2", "alpha", "original", observationNormalizationProfile)
	proof("proof-1", "alpha", "original", observationNormalizationProfile)
	proof("proof-3", "beta", "prior beta", "another-normalization-profile")
	proof("proof-4", "format", "same projection", observationNormalizationProfile)
	add("raw-only", "format", "same projection", observationNormalizationProfile, "complete")
	add("a", "alpha", "changed", observationNormalizationProfile, "complete")
	add("b", "beta", "current beta", observationNormalizationProfile, "complete")
	candidate, extra := add("c", "RC", "new version", observationNormalizationProfile, "complete")
	add("d", "\u2003local-draft\u2003", "manual", observationNormalizationProfile, "complete")
	add("e", "", "no version", observationNormalizationProfile, "complete")
	extra.EntryLocation = "/entries/0"
	extra.RetrievalDigest = observationDigest([]byte("alternate"))
	store.occurrences[occurrenceKey(tenant, extra)] = extra
	second := extra
	second.RetrievalDigest = observationDigest([]byte("alternate two"))
	store.occurrences[occurrenceKey(tenant, second)] = second
	if second.RetrievalDigest < extra.RetrievalDigest {
		extra = second
	}
	read := func(t *testing.T, check, after string, limit int) []UpstreamChange {
		t.Helper()
		rows, err := store.ListUpstreamChanges(t.Context(), tenant, UpstreamChangesQuery{Check: check, AfterID: after, Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	rows := read(t, "all", "", 20)
	if len(rows) != 3 || rows[0].Outcome != "changed_entry" || rows[0].Target.PriorVersionRevisionID != "proof-1" || rows[1].Outcome != "comparison_unavailable" || rows[1].Target.PriorVersionRevisionID != "proof-3" || rows[2].Outcome != "unadopted_version" || rows[2].Occurrence != extra || rows[2].Target.PriorVersionRevisionID != "proof-1" {
		t.Fatal("mixed deterministic page", rows)
	}
	if rows[0].LatestAttempt == nil || rows[0].LatestAttempt.State != SnapshotFailed || rows[0].SnapshotCompletedAt != now {
		t.Fatal("complete snapshot age", rows[0])
	}
	if len(read(t, "entry_comparison", "", 20)) != 2 || len(read(t, "unadopted_version", "", 20)) != 1 || len(read(t, "all", "", 1)) != 2 || len(read(t, "all", "b", 1)) != 1 {
		t.Fatal("selector or continuation")
	}
	t.Run("historical observation must identify the adopted Resource", func(t *testing.T) {
		query := UpstreamChangesQuery{Check: "entry_comparison", ObservationID: "a", Limit: 20}
		original := store.observations[resource.Proofs["proof-1"].ObservationID]
		t.Cleanup(func() { store.observations[original.ID] = original })
		unrelated := original
		unrelated.Identifier = "urn:air:remote.example:agents:other"
		store.observations[original.ID] = unrelated
		candidates, err := store.ListUpstreamChanges(t.Context(), tenant, query)
		if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || len(candidates) != 0 {
			t.Fatalf("inconsistent historical observation returned %d candidates, err = %v", len(candidates), err)
		}

		store.observations[original.ID] = original
		candidates, err = store.ListUpstreamChanges(t.Context(), tenant, query)
		if err != nil || len(candidates) != 1 || candidates[0].Occurrence.ObservationID != "a" || candidates[0].Outcome != "changed_entry" || candidates[0].Target.PriorVersionRevisionID != "proof-1" {
			t.Fatalf("restored historical observation returned %+v, err = %v", candidates, err)
		}
	})
	t.Run("matching unfamiliar profiles cannot establish equality or change", func(t *testing.T) {
		previous := resource.Proofs["proof-3"]
		original, member := store.observations["b"], rows[1].Occurrence
		t.Cleanup(func() {
			store.observations["b"] = original
			store.occurrences[occurrenceKey(tenant, member)] = member
		})
		for _, content := range []string{"prior beta", "different beta"} {
			_, occ := add("b", "beta", content, previous.NormalizationProfile, "complete")
			rows := read(t, "entry_comparison", "", 20)
			if len(rows) != 2 || rows[0].Outcome != "changed_entry" || rows[0].Occurrence.ObservationID != "a" {
				t.Fatal("unavailable comparison displaced a confirmed change", rows)
			}
			row := rows[1]
			if row.Outcome != "comparison_unavailable" || row.Target.PriorVersionRevisionID != "proof-3" ||
				row.CurrentProfile != previous.NormalizationProfile || row.PriorProfile != previous.NormalizationProfile ||
				row.PriorDigest != previous.NormalizedEntryDigest || row.Occurrence != occ {
				t.Fatal("unfamiliar profile classified or lost its evidence", row)
			}
		}
	})
	t.Run("a second Source advertising the same entry has no adoption relationship", func(t *testing.T) {
		peer := source
		peer.ID, peer.LastCompleteSnapshotID = "other-source", "other-complete"
		snap := complete
		snap.ID, snap.SourceID = peer.LastCompleteSnapshotID, peer.ID
		other, member := candidate, extra
		other.ID, other.SourceID, other.SnapshotID = "other-observation", peer.ID, snap.ID
		member.ObservationID, member.SourceID, member.SnapshotID = other.ID, peer.ID, snap.ID
		store.sources[peer.ID], store.snapshots[snap.ID] = peer, snap
		store.observations[other.ID], store.occurrences[occurrenceKey(tenant, member)] = other, member
		rows := read(t, "unadopted_version", "", 20)
		if len(rows) != 1 || rows[0].Occurrence != extra {
			t.Fatal("an identical advertisement from another Source changed the selection", rows)
		}
	})
	t.Run("a manually authored Resource without proof has no upstream changes", func(t *testing.T) {
		saved := resource.Proofs
		resource.Proofs = nil
		t.Cleanup(func() { resource.Proofs = saved })
		if rows := read(t, "all", "", 20); len(rows) != 0 {
			t.Fatal("identifier and local version alone produced a relationship", rows)
		}
	})
	add("conflict", "RC", "other payload", observationNormalizationProfile, "complete")
	if len(read(t, "unadopted_version", "", 20)) != 0 {
		t.Fatal("conflicting copies suggested")
	}
	source.Scope.ExcludedIdentifiers = []string{identifier}
	store.sources[source.ID] = source
	if len(read(t, "all", "", 20)) != 0 {
		t.Fatal("source exclusion ignored")
	}
	source.Scope = Scope{}
	store.sources[source.ID] = source
	// Fresh evidence from a partial attempt cannot establish complete membership.
	add("partial-only", "partial", "partial result", observationNormalizationProfile, "failed")
	if rows := read(t, "all", "", 20); len(rows) != 2 {
		t.Fatal("failed attempt supplied membership", rows)
	}
	// Dropping the current complete occurrence of a selected observation is corruption.
	selected := store.observations["a"]
	var saved ObservationOccurrence
	for key, c := range store.occurrences {
		if c.ObservationID == selected.ID {
			saved = c
			delete(store.occurrences, key)
		}
	}
	if _, err := store.ListUpstreamChanges(t.Context(), tenant, UpstreamChangesQuery{Check: "all", Limit: 20}); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatal("missing selected occurrence", err)
	}
	store.occurrences[occurrenceKey(tenant, saved)] = saved
	original := resource.Proofs["proof-1"]
	broken := original
	broken.RetrievalDigest = observationDigest([]byte("missing"))
	resource.Proofs["proof-1"] = broken
	if _, err := store.ListUpstreamChanges(t.Context(), tenant, UpstreamChangesQuery{Check: "all", Limit: 20}); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatal("missing selected proof returned success", err)
	}
}
