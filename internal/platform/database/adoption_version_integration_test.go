//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
)

// Real ingestion and least-privilege runtime logins qualify the append, not
// owner-written provenance. Each invocation owns its schema and roles.
func TestPostgresVersionAdoption(t *testing.T) {
	db := newHousekeepingDatabase(t)
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(t.Name()))
	clk := clock.NewFake(time.Now().UTC().Truncate(time.Microsecond))
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clk, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "adoption-integration"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "version-adopter", Roles: []identity.Role{identity.RoleTenantAdmin}})
	ns, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "adoption", DisplayName: "Adoption"})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := service.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Local", Domains: []string{"remote.example"}})
	if err != nil {
		t.Fatal(err)
	}
	outbound := &postgresFederationFetcher{policyDigest: "sha256:" + strings.Repeat("a", 64), result: fetcher.Result{Status: 200, MediaType: "application/ard+json", FetchedAt: clk.Now()}}
	set := func(label string) {
		outbound.result.Body = []byte(fmt.Sprintf(`{"entries":[{"identifier":"urn:air:remote.example:agents:adopt","version":%q,"displayName":"Upstream","description":"Upstream metadata","type":"application/a2a-agent-card+json","data":{"name":"Upstream","version":%q}}]}`, label, label))
		outbound.result.FetchedAt = clk.Now()
	}
	set("opaque-first")
	manager, err := federation.NewManager(db.federation, outbound, clk, id.Generator{}, federation.WithObservationAdopter(service), federation.WithVersionAdoption(true))
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := manager.Inspect(ctx, "https://remote.example/ard.json")
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Connect(ctx, federation.ConnectInput{InspectionID: inspection.ID, DisplayName: "Upstream", Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, RetrievalMode: federation.RetrievalIndexed, Reason: "Owned version fixture"})
	if err != nil {
		t.Fatal(err)
	}
	workerRepo, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	workerStore, err := NewPostgresFederationStore(workerRepo)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := federation.NewManager(workerStore, outbound, clk, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	selectVersion := func(label string) (federation.Observation, federation.ObservationOccurrence) {
		t.Helper()
		clk.Advance(time.Second)
		set(label)
		snapshot, err := syncClaimedFixture(t, db, ctx, worker, source.ID)
		if err != nil || snapshot.State != federation.SnapshotComplete {
			t.Fatal("sync", err)
		}
		items, _, err := db.federation.ListCurrentObservations(ctx, housekeepingTenantA, source.ID, 10)
		if err != nil || len(items) != 1 {
			t.Fatal("current", len(items), err)
		}
		occurrences, _, err := db.federation.ListObservationOccurrences(ctx, housekeepingTenantA, items[0].ID, 10)
		if err != nil || len(occurrences) != 1 {
			t.Fatal("occurrences", err)
		}
		return items[0], occurrences[0]
	}
	old, occ := selectVersion("opaque-first")
	first, err := manager.AdoptObservation(ctx, old.ID, federation.AdoptInput{SnapshotID: occ.SnapshotID, RetrievalDigest: occ.RetrievalDigest, EntryLocation: occ.EntryLocation, NamespaceID: ns.ID, PublisherID: pub.ID, DisplayName: "My local title", Description: "Keep my custom description", Visibility: catalog.VisibilityPrivate})
	if err != nil {
		t.Fatal(err)
	}
	origin, err := repo.GetOriginatingPrincipal(ctx, housekeepingTenantA, "resource", first.Resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	var last federation.Observation
	var lastOccurrence federation.ObservationOccurrence
	var lastAdoptedRevisionID string
	readChanges := func(check string) []federation.UpstreamChange {
		t.Helper()
		rows, err := manager.UpstreamChanges(ctx, federation.UpstreamChangesQuery{Check: check, NamespaceID: ns.ID, SourceID: source.ID, Limit: 20})
		if err != nil {
			t.Fatal("upstream inventory", err)
		}
		return rows
	}
	if rows := readChanges("all"); len(rows) != 0 {
		t.Fatal("equal adopted evidence", rows)
	}
	// More than one hundred real adopted Versions exercise store-bound projection.
	for n := 0; n < 101; n++ {
		obs, occ := selectVersion(fmt.Sprintf("opaque-%03d", n))
		last = obs
		lastOccurrence = occ
		input := federation.AdoptVersionInput{ObservationID: obs.ID, SnapshotID: occ.SnapshotID, RetrievalDigest: occ.RetrievalDigest, EntryLocation: occ.EntryLocation, PriorVersionRevisionID: first.VersionRevision.ID}
		if n == 0 || n == 100 {
			rows := readChanges("all")
			if len(rows) != 1 || rows[0].Outcome != "unadopted_version" || rows[0].Target.ResourceID != first.Resource.ID || rows[0].Target.PriorVersionRevisionID != first.VersionRevision.ID || rows[0].Occurrence != occ || rows[0].LatestAttempt == nil || rows[0].LatestAttempt.SnapshotID != occ.SnapshotID {
				t.Fatal("upstream selection", rows)
			}
			if rows := readChanges("entry_comparison"); len(rows) != 0 {
				t.Fatal("wrong check", rows)
			}
		}
		if n == 0 {
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for range 2 {
				wg.Go(func() { _, err := manager.AdoptVersion(ctx, first.Resource.ID, input); results <- err })
			}
			wg.Wait()
			close(results)
			successes, conflicts := 0, 0
			for err := range results {
				switch {
				case err == nil:
					successes++
				case catalog.ErrorCodeOf(err) == catalog.CodeConflict:
					conflicts++
				default:
					t.Fatal("competing append", err)
				}
			}
			if successes != 1 || conflicts != 1 {
				t.Fatal("competing append", successes, conflicts)
			}
		} else {
			appended, err := manager.AdoptVersion(ctx, first.Resource.ID, input)
			if err != nil {
				t.Fatal("append", n, err)
			}
			lastAdoptedRevisionID = appended.VersionRevision.ID
		}
	}
	if rows := readChanges("all"); len(rows) != 0 {
		t.Fatal("existing Version in a large history", rows)
	}
	retained, _, err := repo.GetVersionRevision(ctx, housekeepingTenantA, first.VersionRevision.ID)
	if err != nil || !reflect.DeepEqual(retained.Adoption, first.VersionRevision.Adoption) || retained.ContentSetDigest != first.VersionRevision.ContentSetDigest {
		t.Fatal("rewrote old proof", err)
	}
	resource, err := repo.GetResource(ctx, housekeepingTenantA, first.Resource.ID)
	if err != nil || !reflect.DeepEqual(resource, first.Resource) {
		t.Fatal("rewrote parent", err)
	}
	listing, err := repo.GetListing(ctx, housekeepingTenantA, first.Listing.ID)
	if err != nil || listing.CandidateRevisionID != first.ListingRevision.ID || listing.CurrentReleaseID != "" {
		t.Fatal("changed Listing", err)
	}
	after, err := repo.GetOriginatingPrincipal(ctx, housekeepingTenantA, "resource", first.Resource.ID)
	if err != nil || after != origin {
		t.Fatal("changed creator", err)
	}
	target, err := manager.VersionAdoptionTarget(ctx, old.ID)
	if err != nil || target.Version == old.Version || target.ResourceID != first.Resource.ID {
		t.Fatal("exact bounded destination", target, err)
	}
	signalCtx, _, err := identity.WithCollectionContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	signalTarget := ratings.Target{Origin: "foreign", SourceID: source.ID, PresentationGeneration: federation.EffectivePresentationGeneration(source), Identifier: last.Identifier, Kind: last.Kind, VersionPresent: true, Version: last.Version}
	signals, err := db.federation.RatingSignals(signalCtx, housekeepingTenantA, []federation.RatingSignalTarget{{Target: signalTarget, EntryDigest: last.NormalizedEntryDigest}})
	if err != nil || signals[ratings.Key(signalTarget)].ImportedResourceID != first.Resource.ID || signals[ratings.Key(signalTarget)].ChangedEntry {
		t.Fatal("exact selected signal", signals, err)
	}
	signals, err = db.federation.RatingSignals(signalCtx, housekeepingTenantA, []federation.RatingSignalTarget{{Target: signalTarget, EntryDigest: "sha256:" + strings.Repeat("b", 64)}})
	if err != nil || !signals[ratings.Key(signalTarget)].ChangedEntry {
		t.Fatal("changed entry hidden", signals, err)
	}
	signalTarget.Version = "unadopted"
	signals, err = db.federation.RatingSignals(signalCtx, housekeepingTenantA, []federation.RatingSignalTarget{{Target: signalTarget}})
	if err != nil || signals[ratings.Key(signalTarget)].ImportedResourceID != "" {
		t.Fatal("false import", err)
	}
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "other-adopter", Roles: []identity.Role{identity.RoleTenantAdmin}})
	if _, _, err := repo.GetVersionAdoptionProof(other, housekeepingTenantB, first.Resource.ID, first.VersionRevision.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("cross-tenant proof", err)
	}
	if _, err := manager.VersionAdoptionTarget(other, last.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("cross-tenant target lookup", err)
	}
	if _, err := manager.AdoptVersion(other, first.Resource.ID, federation.AdoptVersionInput{ObservationID: last.ID, SnapshotID: lastOccurrence.SnapshotID, RetrievalDigest: lastOccurrence.RetrievalDigest, EntryLocation: lastOccurrence.EntryLocation, PriorVersionRevisionID: first.VersionRevision.ID}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("cross-tenant append", err)
	}
	var versions, audits int
	if err := db.operator.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM resource_versions WHERE tenant_id=$1 AND resource_id=$2),(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='resource_version.created')`, housekeepingTenantA, first.Resource.ID).Scan(&versions, &audits); err != nil || versions != 102 || audits != 101 {
		t.Fatal("atomic count", versions, audits, err)
	}
	if err := db.operator.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM resource_versions WHERE tenant_id=$1),(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='resource_version.created')`, housekeepingTenantB).Scan(&versions, &audits); err != nil || versions != 0 || audits != 0 {
		t.Fatal("cross-tenant append changed caller state", versions, audits, err)
	}
	// Repeat one adopted label with new metadata, alongside two additional opaque labels.
	clk.Advance(time.Second)
	set(last.Version)
	entry := strings.TrimSuffix(strings.TrimPrefix(string(outbound.result.Body), `{"entries":[`), `]}`)
	changedEntry := strings.Replace(entry, "Upstream metadata", "Changed metadata", 1)
	nextEntry := strings.ReplaceAll(entry, last.Version, "next-candidate")
	otherEntry := strings.ReplaceAll(entry, last.Version, "previous-looking")
	outbound.result.Body = []byte(`{"entries":[` + changedEntry + "," + nextEntry + "," + nextEntry + "," + otherEntry + `]}`)
	snap, err := syncClaimedFixture(t, db, ctx, worker, source.ID)
	if err != nil || snap.State != federation.SnapshotComplete {
		t.Fatal("refresh", snap.State, err)
	}
	changes := readChanges("entry_comparison")
	if len(changes) != 1 || changes[0].Outcome != "changed_entry" || changes[0].Target.UpstreamVersion != last.Version || changes[0].Target.PriorVersionRevisionID != lastAdoptedRevisionID || changes[0].PriorDigest != last.NormalizedEntryDigest || changes[0].Occurrence.NormalizedEntryDigest == changes[0].PriorDigest {
		t.Fatal("changed metadata", changes)
	}
	if rows := readChanges("unadopted_version"); len(rows) != 2 {
		t.Fatal("distinct new observations", rows)
	} else {
		for _, row := range rows {
			if row.Target.UpstreamVersion == "next-candidate" && row.Occurrence.EntryLocation != "/entries/1" {
				t.Fatal("repeated entry selection", row)
			}
		}
	}
	q := federation.UpstreamChangesQuery{Check: "all", Limit: 1}
	page, err := manager.UpstreamChanges(ctx, q)
	if err != nil || len(page) != 2 {
		t.Fatal("lookahead", page, err)
	}
	q.AfterID = page[0].Occurrence.ObservationID
	nextPage, err := manager.UpstreamChanges(ctx, q)
	if err != nil || len(nextPage) != 2 || nextPage[0].Occurrence.ObservationID != page[1].Occurrence.ObservationID {
		t.Fatal("keyset", nextPage, err)
	}
	if rows, err := manager.UpstreamChanges(other, q); err != nil || len(rows) != 0 {
		t.Fatal("foreign inventory", rows, err)
	}
	evidenceOnly := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "source-observer", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if rows, err := manager.UpstreamChanges(evidenceOnly, q); err != nil || len(rows) != 0 {
		t.Fatal("source evidence alone disclosed editable Resources", rows, err)
	}
	store, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clk, IDs: id.Generator{}, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := authority.GrantRole(ctx, identity.GrantRoleInput{Subject: "source-observer", ActorType: identity.ActorHuman, Role: identity.RoleContributor, NamespaceID: ns.ID, Reason: "Maintain adopted Resources"}, "upstream-edit")
	if err != nil {
		t.Fatal(err)
	}
	// The new maintainer can inspect another user's adoption. Paging and exact
	// rechecks must leave the job queue, audit, outbox and governed state alone.
	counts := func() [5]int {
		t.Helper()
		var n [5]int
		if err := db.operator.QueryRow(ctx, `SELECT
		  (SELECT count(*) FROM jobs WHERE tenant_id=$1),
		  (SELECT count(*) FROM audit_events WHERE tenant_id=$1),
		  (SELECT count(*) FROM outbox_events WHERE tenant_id=$1),
		  (SELECT count(*) FROM resource_versions WHERE tenant_id=$1),
		  (SELECT count(*) FROM listing_releases WHERE tenant_id=$1)`, housekeepingTenantA).Scan(&n[0], &n[1], &n[2], &n[3], &n[4]); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := counts()
	outbound.beforeReturn = func() error { t.Fatal("inventory contacted the Source"); return nil }
	exact := federation.UpstreamChangesQuery{Check: "entry_comparison", ObservationID: changes[0].Occurrence.ObservationID, Limit: 1}
	if rows, err := manager.UpstreamChanges(evidenceOnly, exact); err != nil || !reflect.DeepEqual(rows, changes) {
		t.Fatal("scoped adoption evidence", rows, err)
	}
	if rows, err := manager.UpstreamChanges(evidenceOnly, q); err != nil || !reflect.DeepEqual(rows, nextPage) {
		t.Fatal("scoped continuation", rows, err)
	}
	if after := counts(); after != before {
		t.Fatalf("inventory reads changed retained state: %v to %v", before, after)
	}
	if _, err := authority.RevokeRole(ctx, grant.ID, "End adoption maintenance", "upstream-revoke", grant.Revision); err != nil {
		t.Fatal(err)
	}
	if rows, err := manager.UpstreamChanges(evidenceOnly, exact); err != nil || len(rows) != 0 {
		t.Fatal("revoked namespace grant disclosed the selected observation", rows, err)
	}
	// The maintained inventory must still use the complete snapshot after the
	// real worker records a failed refresh. Only the latest-attempt fact advances.
	clk.Advance(time.Hour)
	outbound.beforeReturn = nil
	outbound.result.Status = 503
	outbound.result.FetchedAt = clk.Now()
	failed, syncErr := syncClaimedFixture(t, db, ctx, worker, source.ID)
	if syncErr == nil || failed.State != federation.SnapshotFailed || failed.CompletedAt == nil || !failed.CompletedAt.After(changes[0].SnapshotCompletedAt) {
		t.Fatal("expected a later failed refresh", failed.State, syncErr)
	}
	rows := readChanges("entry_comparison")
	if len(rows) != 1 {
		t.Fatal("failed refresh lost the changed entry", rows)
	}
	attempt := rows[0].LatestAttempt
	if attempt == nil || attempt.SnapshotID != failed.ID || attempt.Revision != source.Revision ||
		attempt.State != federation.SnapshotFailed || !attempt.CompletedAt.Equal(*failed.CompletedAt) {
		t.Fatal("inventory did not report the failed attempt", attempt)
	}
	if rows[0].Occurrence != changes[0].Occurrence || !rows[0].SnapshotCompletedAt.Equal(changes[0].SnapshotCompletedAt) ||
		rows[0].Target != changes[0].Target || rows[0].PriorDigest != changes[0].PriorDigest || rows[0].Outcome != "changed_entry" {
		t.Fatal("failed refresh replaced complete adoption evidence", rows[0])
	}
	if _, err := manager.SetStatus(ctx, source.ID, federation.SourcePaused, source.Revision, federation.StatusChangeInput{Reason: "Pause after inventory selection"}); err != nil {
		t.Fatal(err)
	}
	if rows := readChanges("all"); len(rows) != 0 {
		t.Fatal("paused Source suggested changes", rows)
	}
	// Native validation errors remain evidence on drafts; they are not waived.
	if first.VersionRevision.EvidenceSetID == "" || len(first.VersionRevision.Representations) == 0 {
		t.Fatal("missing native evidence")
	}
}
