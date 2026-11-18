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
	// More than one hundred real adopted Versions exercise store-bound projection.
	for n := 0; n < 101; n++ {
		obs, occ := selectVersion(fmt.Sprintf("opaque-%03d", n))
		last = obs
		lastOccurrence = occ
		input := federation.AdoptVersionInput{ObservationID: obs.ID, SnapshotID: occ.SnapshotID, RetrievalDigest: occ.RetrievalDigest, EntryLocation: occ.EntryLocation, PriorVersionRevisionID: first.VersionRevision.ID}
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
		} else if _, err := manager.AdoptVersion(ctx, first.Resource.ID, input); err != nil {
			t.Fatal("append", n, err)
		}
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
	// Native validation errors remain evidence on drafts; they are not waived.
	if first.VersionRevision.EvidenceSetID == "" || len(first.VersionRevision.Representations) == 0 {
		t.Fatal("missing native evidence")
	}
}
