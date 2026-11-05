//go:build integration

package database

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// Reuse the real ingestion, exact-role and fixed query-count fixtures. No source
// document or retained history is forged or edited through operator SQL.
func TestPostgresObservationMetadataAndBoundedGroupQueries(t *testing.T) {
	db, repository, counts, _ := newSearchCostFixture(t)
	principal, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.performance.example", TenancyMode: "dedicated", Subject: "observation-admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleSourceAdmin}, Operator: "observation-test", Reason: "synthetic metadata fixture", AcknowledgeOutbox: true})
	if err != nil {
		t.Fatal(err)
	}
	actor := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "observation-admin", Roles: []identity.Role{identity.RoleSourceAdmin}, MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: principal.ActorType})
	const identifier = "urn:air:catalog.remote.example:agents:metadata"
	entry := func(index int) map[string]any {
		data := map[string]any{"name": fmt.Sprintf("Variant %03d", index)}
		if index == 0 {
			data["private"] = strings.Repeat("x", 512*1024)
		}
		return map[string]any{"identifier": identifier, "displayName": fmt.Sprintf("Variant %03d", index), "description": "Metadata fixture", "tags": []string{"metadata"}, "representativeQueries": []string{"Find metadata", "Find variants"}, "type": adapter.MediaA2AAgentCard, "data": data}
	}
	entries := []map[string]any{entry(0)}
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	outbound := &postgresFederationFetcher{policyDigest: "sha256:" + strings.Repeat("a", 64), result: fetcher.Result{Status: 200, MediaType: "application/ard+json", FetchedAt: time.Now(), Body: body}}
	store, err := NewPostgresFederationStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	sourceClock := clock.NewFake(time.Now().UTC().Truncate(time.Microsecond))
	manager, err := federation.NewManager(store, outbound, sourceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := manager.Inspect(actor, "https://catalog.remote.example/.well-known/ard.json")
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Connect(actor, federation.ConnectInput{InspectionID: inspection.ID, DisplayName: "Metadata fixture", Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, RetrievalMode: federation.RetrievalIndexed, Reason: "synthetic metadata fixture"})
	if err != nil {
		t.Fatal(err)
	}
	workerRepository, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	workerStore, err := NewPostgresFederationStore(workerRepository)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := federation.NewManager(workerStore, outbound, sourceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	sync := func() federation.Snapshot {
		t.Helper()
		outbound.result.Body, err = json.Marshal(map[string]any{"entries": entries})
		if err != nil {
			t.Fatal(err)
		}
		outbound.result.FetchedAt = sourceClock.Now()
		snapshot, syncErr := syncClaimedFixture(t, db, actor, worker, source.ID)
		if syncErr != nil || snapshot.State != federation.SnapshotComplete {
			t.Fatalf("complete metadata fixture: %v", syncErr)
		}
		return snapshot
	}
	assertGroup := func(want int, truncated bool) federation.ObservationGroup {
		t.Helper()
		counts.reset()
		group, groupErr := manager.ObservationGroup(actor, identifier, "", false)
		if groupErr != nil {
			t.Fatal(groupErr)
		}
		total := 0
		for _, member := range group.Members {
			for _, contribution := range member.Contributions {
				total++
				if len(contribution.Observation.RawEntry)+len(contribution.Observation.NormalizedEntry) != 0 || !contribution.Observation.RawEntryAvailable || contribution.OccurrenceCount < 1 || contribution.LatestOccurrence.ObservationID != contribution.Observation.ID {
					t.Fatal("group lost bounded metadata or provenance")
				}
			}
		}
		if total != want || group.ContributionsTruncated != truncated || group.Conflict != (want > 1) {
			t.Fatalf("group contributions=%d truncated=%t conflict=%t", total, group.ContributionsTruncated, group.Conflict)
		}
		// Members, conflict totals, source conflicts and one batched summary.
		// Each of the three repository calls has one tenant-context SELECT
		// and one combined revision/current-permission admission query.
		if counts.counts["select"] != 10 || counts.counts["begin"] != 3 || counts.counts["commit"] != 3 {
			t.Fatalf("group query count for %d contributions: %v", total, counts.counts)
		}
		t.Logf("group contributions=%d data_reads=4 tenant_reads=3 permission_reads=3 transactions=3", total)
		return group
	}
	firstSnapshot := sync()
	first := assertGroup(1, false)
	largeID := first.Members[0].Contributions[0].Observation.ID
	retained, err := store.GetObservation(actor, housekeepingTenantA, largeID)
	if err != nil || len(retained.RawEntry) < 512*1024 || len(retained.NormalizedEntry) < 512*1024 {
		t.Fatalf("large retained control: %v", err)
	}
	for index := 1; index <= 100; index++ {
		entries = append(entries, entry(index))
	}
	// One small entry occurs at many locations. Repeated exact evidence has one
	// observation and a complete occurrence history, not duplicate group members.
	for range 120 {
		entries = append(entries, entry(1))
	}
	secondSnapshot := sync()
	// Exact indexed assessment uses current occurrence evidence, even when the
	// observation retains its original snapshot. Exercise the actual API role.
	for _, test := range []struct {
		name, observation, source, snapshot, digest string
		revision                                    int64
		want                                        bool
	}{
		{"current", largeID, source.ID, secondSnapshot.ID, retained.ExactEntryDigest, source.Revision, true},
		{"original", largeID, source.ID, firstSnapshot.ID, retained.ExactEntryDigest, source.Revision, true},
		{"other observation", "00000000-0000-7000-8000-000000000099", source.ID, secondSnapshot.ID, retained.ExactEntryDigest, source.Revision, false},
		{"other source", largeID, "00000000-0000-7000-8000-000000000099", secondSnapshot.ID, retained.ExactEntryDigest, source.Revision, false},
		{"other snapshot", largeID, source.ID, "00000000-0000-7000-8000-000000000099", retained.ExactEntryDigest, source.Revision, false},
		{"other digest", largeID, source.ID, secondSnapshot.ID, "sha256:" + strings.Repeat("f", 64), source.Revision, false},
		{"other revision", largeID, source.ID, secondSnapshot.ID, retained.ExactEntryDigest, source.Revision + 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			found, err := store.ObservationHasSnapshotEvidence(actor, housekeepingTenantA, test.observation, test.source, test.snapshot, test.revision, test.digest)
			if err != nil || found != test.want {
				t.Fatal(found, err)
			}
		})
	}
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "other-observation-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if found, err := store.ObservationHasSnapshotEvidence(other, housekeepingTenantB, largeID, source.ID, secondSnapshot.ID, source.Revision, retained.ExactEntryDigest); err != nil || found {
		t.Fatal("cross-tenant occurrence disclosed", found, err)
	}
	group := assertGroup(100, true)
	for _, member := range group.Members {
		for _, contribution := range member.Contributions {
			if !contribution.SourceConflict {
				t.Fatal("same-source conflicts were lost")
			}
		}
	}
	items, err := store.ListObservationPage(actor, housekeepingTenantA, federation.ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 101})
	if err != nil || len(items) != 101 {
		t.Fatalf("metadata inventory: %d %v", len(items), err)
	}
	var repeatedID string
	for _, item := range items {
		if !item.RawEntryAvailable || len(item.RawEntry)+len(item.NormalizedEntry) != 0 {
			t.Fatal("observation page hydrated documents")
		}
		if item.SearchDisplayName == "Variant 001" {
			repeatedID = item.ID
		}
	}
	if repeatedID == "" {
		t.Fatal("repeated entry missing")
	}
	counts.reset()
	summaries, err := store.ObservationOccurrenceSummaries(actor, housekeepingTenantA, []string{largeID, repeatedID})
	if err != nil || summaries[largeID].Total != 2 || summaries[repeatedID].Total != 121 || summaries[repeatedID].Latest.EntryLocation != "/entries/1" {
		t.Fatalf("count/latest tuple changed: %+v %v", summaries, err)
	}
	if latest := summaries[largeID].Latest; !latest.ObservedAt.Equal(sourceClock.Now()) || latest.SnapshotID != max(firstSnapshot.ID, secondSnapshot.ID) {
		t.Fatal("latest summary lost snapshot-ID ordering at equal timestamps")
	}
	if counts.counts["select"] != 2 || counts.counts["begin"] != 1 || counts.counts["commit"] != 1 {
		t.Fatalf("batch query count=%v", counts.counts)
	}
	metadata, err := store.GetObservationMetadata(actor, housekeepingTenantA, largeID)
	// Compare every projected field against exact retained evidence. The second
	// sync can update the current snapshot and last-seen metadata legitimately.
	current, currentErr := store.GetObservation(actor, housekeepingTenantA, largeID)
	if currentErr != nil {
		t.Fatal(currentErr)
	}
	wantMetadata := current
	wantMetadata.RawEntry, wantMetadata.NormalizedEntry, wantMetadata.Occurrence = nil, nil, federation.ObservationOccurrence{}
	if err != nil || !reflect.DeepEqual(metadata, wantMetadata) {
		t.Fatalf("exact parent metadata: %v", err)
	}
	results, err := store.SearchObservationPage(actor, housekeepingTenantA, federation.ObservationSearchRequest{Query: federation.SearchQuery{Text: "Variant 000"}, Limit: 101})
	if err != nil || len(results) != 1 || results[0].Observation.ID != largeID || len(results[0].Observation.RawEntry)+len(results[0].Observation.NormalizedEntry) != 0 {
		t.Fatalf("search metadata: count=%d %v", len(results), err)
	}
	// Both manager occurrence paths check the parent using metadata. Histories
	// retain their complete count and cursor tuple despite large parent documents.
	preview, total, err := manager.ObservationOccurrences(actor, largeID, 1)
	if err != nil || total != 2 || len(preview) != 1 {
		t.Fatalf("history preview: %v", err)
	}
	page, err := manager.ObservationOccurrencePage(actor, federation.OccurrenceListRequest{ObservationID: repeatedID, Limit: 100})
	if err != nil || len(page) != 101 {
		t.Fatalf("history sentinel: %d %v", len(page), err)
	}
	position := page[99]
	rest, err := manager.ObservationOccurrencePage(actor, federation.OccurrenceListRequest{ObservationID: repeatedID, Limit: 100, AfterObservedAt: &position.ObservedAt, AfterSnapshotID: position.SnapshotID, AfterRetrievalDigest: position.RetrievalDigest, AfterEntryLocation: position.EntryLocation})
	if err != nil || len(rest) != 21 || !reflect.DeepEqual(rest[0], page[100]) {
		t.Fatalf("history continuation: %d %v", len(rest), err)
	}
	if _, err := store.GetObservationMetadata(actor, housekeepingTenantB, largeID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("authenticated tenant mismatch was accepted: %v", err)
	}
	for _, tenant := range []string{housekeepingTenantA, housekeepingTenantB} {
		missing := "00000000-0000-7000-8000-000000000099"
		if tenant == housekeepingTenantB {
			missing = largeID
		}
		if _, err := store.GetObservationMetadata(t.Context(), tenant, missing); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("metadata isolation: %v", err)
		}
		if partial, err := store.ObservationOccurrenceSummaries(t.Context(), tenant, []string{largeID, missing}); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || partial != nil {
			t.Fatalf("missing evidence must reject whole batch: %v", err)
		}
	}
	// API RLS alone can mask a missing SQL tenant predicate. Exercise the same
	// projections through the real BYPASSRLS worker role, with a valid control.
	workerMetadata, err := workerStore.GetObservationMetadata(t.Context(), housekeepingTenantA, largeID)
	if err != nil || !reflect.DeepEqual(workerMetadata, metadata) {
		t.Fatalf("worker metadata control: %v", err)
	}
	workerSummaries, err := workerStore.ObservationOccurrenceSummaries(t.Context(), housekeepingTenantA, []string{largeID, repeatedID})
	if err != nil || !reflect.DeepEqual(workerSummaries, summaries) {
		t.Fatalf("worker summary control: %v", err)
	}
	if _, err := workerStore.GetObservationMetadata(t.Context(), housekeepingTenantB, largeID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("worker metadata leaked another tenant: %v", err)
	}
	if partial, err := workerStore.ObservationOccurrenceSummaries(t.Context(), housekeepingTenantB, []string{largeID, repeatedID}); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || partial != nil {
		t.Fatalf("worker summary leaked another tenant: %v", err)
	}
	if items, err := workerStore.ListObservationPage(t.Context(), housekeepingTenantB, federation.ObservationListRequest{SourceID: source.ID, Limit: 2}); err != nil || len(items) != 0 {
		t.Fatalf("worker observation page leaked another tenant: %v", err)
	}
	if items, err := workerStore.ListObservationGroupMembers(t.Context(), housekeepingTenantB, identifier, "", false, 2); err != nil || len(items) != 0 {
		t.Fatalf("worker group leaked another tenant: %v", err)
	}
	if items, err := store.SearchObservationPage(t.Context(), housekeepingTenantB, federation.ObservationSearchRequest{Query: federation.SearchQuery{Text: "Variant"}, Limit: 2}); err != nil || len(items) != 0 {
		t.Fatalf("API Search leaked another tenant: %v", err)
	}
	// Search query execution belongs to the API manifest, even for an otherwise
	// readable worker tenant. BYPASSRLS must not imply lexical-query authority.
	if items, err := workerStore.SearchObservationPage(t.Context(), housekeepingTenantA, federation.ObservationSearchRequest{Query: federation.SearchQuery{Text: "Variant"}, Limit: 2}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden || items != nil {
		t.Fatalf("worker gained API Search authority: %v", err)
	}
	after, err := store.GetObservation(actor, housekeepingTenantA, largeID)
	if err != nil || !bytes.Equal(after.RawEntry, retained.RawEntry) || !bytes.Equal(after.NormalizedEntry, retained.NormalizedEntry) || after.ExactEntryDigest != retained.ExactEntryDigest {
		t.Fatalf("retained exact evidence changed: %v", err)
	}
	encoded, err := json.Marshal(group)
	if err != nil || len(encoded) > 512*1024 {
		t.Fatalf("bounded metadata group: %d bytes %v", len(encoded), err)
	}
	t.Logf("retained raw=%d normalized=%d; 100-contribution group=%d JSON bytes", len(retained.RawEntry), len(retained.NormalizedEntry), len(encoded))
}
