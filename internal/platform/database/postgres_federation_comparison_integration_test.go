//go:build integration

// Real PostgreSQL qualification covers exact API-role reads, RLS, occurrence
// reuse, immutable history, duplicate keys and complete membership work bounds.
package database

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type comparisonSeedEntry struct {
	identifier, value, input string
	present                  bool
}

// commitComparisonFixture uses the supported writer with a schema-owner fixture
// connection. Production read assertions below use the exact API login and RLS.
func commitComparisonFixture(t *testing.T, db *housekeepingDatabase, source federation.Source, at time.Time, entries []comparisonSeedEntry) federation.Snapshot {
	t.Helper()
	newID := func() string {
		t.Helper()
		v, err := (id.Generator{}).New(at)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	snapshot, template := federationSyncFixture(t, source)
	snapshot.ID, snapshot.StartedAt, snapshot.CompletedAt = newID(), at, &at
	snapshot.MemberCount, snapshot.Added = len(entries), 0
	rawEntries := make([]json.RawMessage, 0, len(entries))
	observations := make([]federation.Observation, 0, len(entries))
	for index, entry := range entries {
		if !strings.HasPrefix(entry.identifier, "urn:air:") {
			entry.identifier = "urn:air:peer.example:agent:" + entry.identifier
		}
		document := map[string]any{"identifier": entry.identifier, "value": entry.value}
		if entry.present {
			document["version"] = ""
		}
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		exact := audit.DigestBytes(raw)
		o := template
		o.Completeness = federation.Unresolved
		o.Compatibility = federation.Compatibility{Structural: "invalid", ProfileID: source.ProfileID, Support: "unknown"}
		o.ID, o.SnapshotID, o.Identifier, o.VersionPresent = newID(), snapshot.ID, entry.identifier, entry.present
		presence := byte(0)
		if entry.present {
			presence = 1
		}
		key := sha256.Sum256(append(append([]byte(entry.identifier), 0xff, presence), 0xff))
		o.SourceKey = fmt.Sprintf("identity:v1:sha256:%x", key)
		o.RawEntry, o.ExactEntryDigest, o.IdentityDigest = raw, exact, exact
		if entry.input != "" {
			o.IdentityDigest = audit.DigestBytes([]byte("catalog-observation-identity-v1\x00" + exact + "\x00" + entry.input))
		}
		o.FirstSeenAt, o.LastSeenAt = at, at
		o.Occurrence = federation.ObservationOccurrence{ObservationID: o.ID, SourceID: source.ID, SnapshotID: snapshot.ID, SourceRevision: source.Revision, EntryLocation: fmt.Sprintf("/entries/%d", index), ExactEntryDigest: exact, NormalizationInputDigest: entry.input, EvidenceComplete: true, ObservedAt: at}
		observations = append(observations, o)
		rawEntries = append(rawEntries, raw)
	}
	body, err := json.Marshal(map[string]any{"entries": rawEntries})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.BodyDigest = audit.DigestBytes(body)
	snapshot.Retrievals[0].ID = newID()
	snapshot.Retrievals[0].ProfileID = source.ProfileID
	snapshot.RetrievalStates[0].ProfileID = source.ProfileID
	snapshot.Retrievals[0].RepresentationDigest, snapshot.Retrievals[0].FetchedAt = snapshot.BodyDigest, at
	snapshot.RetrievalStates[0].RepresentationDigest = snapshot.BodyDigest
	snapshot.RetrievalStates[0].Body, snapshot.RetrievalStates[0].UpdatedAt = body, at
	snapshot.RetrievalInvalidations = nil
	for i := range observations {
		observations[i].Occurrence.RetrievalDigest = snapshot.BodyDigest
	}
	ownerRepo, err := NewPostgresRepository(db.operator)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewPostgresFederationStore(ownerRepo)
	if err != nil {
		t.Fatal(err)
	}
	source.LastCompleteSnapshotID, source.LastSyncAt, source.UpdatedAt = snapshot.ID, &at, at
	saved, _, err := owner.CommitSync(t.Context(), snapshot, observations, source, source.Revision)
	if err != nil {
		t.Fatal("commit owned comparison fixture", err)
	}
	return saved
}

func TestPostgresSourceSnapshotComparison(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "comparison-reader", Roles: []identity.Role{identity.RoleSourceAdmin}})
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "comparison-other", Roles: []identity.Role{identity.RoleSourceAdmin}})
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	source := createRevisedHousekeepingSource(t, db, "00000000-0000-7000-8000-000000000981", at)
	before := commitComparisonFixture(t, db, source, at.Add(time.Hour), []comparisonSeedEntry{{identifier: "reused", value: "same"}, {identifier: "changed", value: "old"}, {identifier: "absent", value: "absent"}, {identifier: "context", value: "same bytes"}, {identifier: "version", value: "same", present: false}})
	// An intervening empty run changes the current view, but neither selected
	// membership may be reconstructed from that mutable projection.
	commitComparisonFixture(t, db, source, at.Add(2*time.Hour), nil)
	after := commitComparisonFixture(t, db, source, at.Add(3*time.Hour), []comparisonSeedEntry{{identifier: "reused", value: "same"}, {identifier: "changed", value: "new"}, {identifier: "added", value: "added"}, {identifier: "context", value: "same bytes", input: audit.DigestBytes([]byte("new context"))}, {identifier: "version", value: "same", present: true}})
	commitComparisonFixture(t, db, source, at.Add(4*time.Hour), nil)
	manager, err := federation.NewManager(db.federation, &postgresFederationFetcher{policyDigest: source.PolicyDigest}, clock.NewFake(at), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	state := func() string {
		t.Helper()
		var result string
		err := db.operator.QueryRow(t.Context(), `SELECT jsonb_build_object(
          'source',(SELECT to_jsonb(s) FROM federation_sources s WHERE tenant_id=$1 AND id=$2),
          'snapshots',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM source_snapshots s WHERE tenant_id=$1 AND source_id=$2),
          'observations',(SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM observations o WHERE tenant_id=$1 AND source_id=$2),
          'occurrences',(SELECT jsonb_agg(to_jsonb(c) ORDER BY snapshot_id,retrieval_digest,entry_location) FROM observation_occurrences c WHERE tenant_id=$1 AND source_id=$2),
          'jobs',(SELECT count(*) FROM jobs WHERE tenant_id=$1),
          'audit',(SELECT count(*) FROM audit_events WHERE tenant_id=$1),
          'outbox',(SELECT count(*) FROM outbox_events WHERE tenant_id=$1))::text`, source.TenantID, source.ID).Scan(&result)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	originalState := state()
	request := federation.SnapshotComparisonRequest{BeforeID: before.ID, AfterID: after.ID, IncludeUnchanged: true, Limit: 1}
	want := map[string][]string{"reused": {"unchanged"}, "changed": {"changed"}, "absent": {"absent"}, "added": {"added"}, "context": {"changed"}, "version": {"added", "absent"}}
	seen := map[string][]string{}
	projectedBytes := 0
	for {
		page, err := manager.CompareSnapshots(ctx, request)
		if err != nil {
			t.Fatal("API-role comparison", err)
		}
		if page.SourceID != source.ID || page.SourceRevision != source.Revision || page.Before.ID != before.ID || page.After.ID != after.ID {
			t.Fatal("pair identity changed")
		}
		if len(page.Items) == 0 {
			break
		}
		item := page.Items[0]
		if item.Position != request.AfterPosition+1 {
			t.Fatal("unstable comparison position")
		}
		member := item.Before
		if member == nil {
			member = item.After
		}
		identifier := strings.TrimPrefix(member.Identifier, "urn:air:peer.example:agent:")
		seen[identifier] = append(seen[identifier], item.Change)
		for _, member := range []*federation.SnapshotComparisonMember{item.Before, item.After} {
			if member != nil {
				projectedBytes += federation.SnapshotComparisonMemberTextBytes(item.SourceKey, *member)
			}
		}
		if identifier == "reused" && (item.Before.ObservationID != item.After.ObservationID || item.Before.Occurrence.SnapshotID != before.ID || item.After.Occurrence.SnapshotID != after.ID) {
			t.Fatal("creation snapshot replaced reused occurrence membership")
		}
		if identifier == "context" && (item.Before.Occurrence.ExactEntryDigest != item.After.Occurrence.ExactEntryDigest || item.Before.IdentityDigest == item.After.IdentityDigest) {
			t.Fatal("context-sensitive evidence collapsed")
		}
		request.AfterPosition = item.Position
	}
	for identifier, changes := range want {
		if len(seen[identifier]) != len(changes) {
			t.Fatalf("missing %s changes=%v", identifier, seen)
		}
		for _, change := range changes {
			if !strings.Contains(strings.Join(seen[identifier], ","), change) {
				t.Fatalf("missing %s for %s", change, identifier)
			}
		}
	}
	_, err = withTenantRead(ctx, db.api, source.TenantID, func(tx pgx.Tx) (struct{}, error) {
		var actual int
		err := tx.QueryRow(ctx, comparisonMembersSQL+`SELECT coalesce(sum(text_bytes),0) FROM members`, source.TenantID, source.ID, before.ID, after.ID, federation.MaxSynchronizationEntries+1).Scan(&actual)
		if err == nil && (actual != projectedBytes || actual == 0) {
			t.Fatalf("SQL text budget=%d expected=%d (nullable kind/version must count)", actual, projectedBytes)
		}
		return struct{}{}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(want) {
		t.Fatalf("unexpected members %v", seen)
	}
	if _, err := manager.CompareSnapshots(other, request); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant comparison disclosed history: %v", err)
	}
	var visible int
	if err := withRLSCount(other, db, housekeepingTenantB, before.ID, &visible); err != nil || visible != 0 {
		t.Fatalf("RLS snapshot leak: %d %v", visible, err)
	}
	if state() != originalState {
		t.Fatal("comparison mutated retained history, current head, Jobs or evidence")
	}
	// Current configuration can advance or disappear without replacing either
	// retained source revision selected by this read.
	source.Revision++
	source.Status = federation.SourceRemoved
	source.UpdatedAt = at.Add(5 * time.Hour)
	if _, err := db.federation.UpdateSource(t.Context(), source, source.Revision-1); err != nil {
		t.Fatal(err)
	}
	request.AfterPosition = 0
	request.Limit = 100
	if page, err := manager.CompareSnapshots(ctx, request); err != nil || len(page.Items) != 7 {
		t.Fatalf("removed source lost retained comparison: %+v %v", page, err)
	}
	duplicateSource := createRevisedHousekeepingSource(t, db, "00000000-0000-7000-8000-000000000983", at)
	duplicateBefore := commitComparisonFixture(t, db, duplicateSource, at.Add(time.Hour), []comparisonSeedEntry{{identifier: "duplicate", value: "same"}, {identifier: "duplicate", value: "same"}, {identifier: "duplicate", value: "conflict"}})
	duplicateAfter := commitComparisonFixture(t, db, duplicateSource, at.Add(2*time.Hour), []comparisonSeedEntry{{identifier: "duplicate", value: "same"}})
	duplicatePage, err := manager.CompareSnapshots(ctx, federation.SnapshotComparisonRequest{BeforeID: duplicateBefore.ID, AfterID: duplicateAfter.ID, Limit: 100})
	if err != nil || len(duplicatePage.Items) != 4 {
		t.Fatalf("duplicate occurrences lost: %+v %v", duplicatePage, err)
	}
	for _, item := range duplicatePage.Items {
		if item.Change != "multiple" || item.BeforeCount != 3 || item.AfterCount != 1 || (item.Before == nil) == (item.After == nil) {
			t.Fatal("duplicate evidence was paired or collapsed")
		}
	}
	// Corrupt only this disposable schema, restoring the immutability trigger in
	// the same transaction. Production reads must refuse missing membership.
	tx, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	for _, sql := range []string{`ALTER TABLE observation_occurrences DISABLE TRIGGER observation_occurrences_immutable`, `DELETE FROM observation_occurrences WHERE snapshot_id='` + before.ID + `'`, `ALTER TABLE observation_occurrences ENABLE TRIGGER observation_occurrences_immutable`} {
		if _, err := tx.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if page, err := manager.CompareSnapshots(ctx, request); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || page.Items != nil {
		t.Fatalf("missing membership accepted: %+v %v", page, err)
	}
}

// withRLSCount deliberately uses another tenant predicate with the API login;
// forced RLS must still conceal the selected tenant's snapshot.
func withRLSCount(ctx context.Context, db *housekeepingDatabase, tenant, id string, count *int) error {
	_, err := withTenantRead(ctx, db.api, tenant, func(tx pgx.Tx) (struct{}, error) {
		return struct{}{}, tx.QueryRow(ctx, `SELECT count(*) FROM source_snapshots WHERE id=$1`, id).Scan(count)
	})
	return err
}

func TestPostgresSourceComparisonMaximumMembership(t *testing.T) {
	db := newHousekeepingDatabase(t)
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	source := createRevisedHousekeepingSource(t, db, "00000000-0000-7000-8000-000000000982", at)
	entries := make([]comparisonSeedEntry, federation.MaxSynchronizationEntries)
	for i := range entries {
		entries[i] = comparisonSeedEntry{identifier: fmt.Sprintf("member-%05d", i), value: "unchanged"}
	}
	before := commitComparisonFixture(t, db, source, at.Add(time.Hour), entries)
	after := commitComparisonFixture(t, db, source, at.Add(2*time.Hour), entries)
	request := federation.SnapshotComparisonRequest{BeforeID: before.ID, AfterID: after.ID, Limit: 2}
	started := time.Now()
	page, err := db.federation.CompareSnapshots(t.Context(), source.TenantID, request)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("maximum unchanged comparison: %+v %v", page, err)
	}
	t.Logf("10,000 unchanged members per side: %s", time.Since(started))
	// Choose the last correspondence key using PostgreSQL's exact C ordering.
	var last string
	if err := db.operator.QueryRow(t.Context(), `SELECT identifier FROM observations WHERE tenant_id=$1 AND source_id=$2 ORDER BY source_key COLLATE "C" DESC LIMIT 1`, source.TenantID, source.ID).Scan(&last); err != nil {
		t.Fatal(err)
	}
	for i := range entries {
		if "urn:air:peer.example:agent:"+entries[i].identifier == last {
			entries[i].value = "changed at last key"
		}
	}
	lastChanged := commitComparisonFixture(t, db, source, at.Add(3*time.Hour), entries)
	request.AfterID = lastChanged.ID
	started = time.Now()
	page, err = db.federation.CompareSnapshots(t.Context(), source.TenantID, request)
	if err != nil || len(page.Items) != 1 || page.Items[0].Change != "changed" || page.Items[0].After.Identifier != last {
		t.Fatalf("sparse maximum comparison: %+v %v", page, err)
	}
	t.Logf("10,000 members, only last key changed: %s", time.Since(started))
	// The output limit is still two, but selection must notice an extra input
	// occurrence before pairing or returning even the first compact row.
	_, err = db.operator.Exec(t.Context(), `INSERT INTO observation_occurrences
      (tenant_id,observation_id,source_id,snapshot_id,source_revision,retrieval_digest,entry_location,
       exact_entry_digest,normalized_entry_digest,evidence_complete,observed_at,normalization_input_digest)
      SELECT tenant_id,observation_id,source_id,snapshot_id,source_revision,retrieval_digest,'/entries/10000',
        exact_entry_digest,normalized_entry_digest,evidence_complete,observed_at,normalization_input_digest
      FROM observation_occurrences WHERE tenant_id=$1 AND snapshot_id=$2 LIMIT 1`, source.TenantID, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err = db.federation.CompareSnapshots(t.Context(), source.TenantID, request)
	if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || page.Items != nil {
		t.Fatalf("input overflow returned partial changes: %+v %v", page, err)
	}
}
