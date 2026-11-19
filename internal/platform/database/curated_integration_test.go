//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/collections"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresSharedCollectionsScopesLifecycleAndIntegrity(t *testing.T) {
	db, repo, _, seed := newSearchCostFixture(t)
	seed(0)
	seed(1)
	admin := semanticDisclosureActor(t, db, housekeepingTenantA)
	source, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 10})
	if err != nil || len(source.Items) != 2 {
		t.Fatal(source, err)
	}
	key := sha256.Sum256([]byte(t.Name()))
	catalogService, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "shared-collection-test"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := catalogService.CreateNamespace(admin, catalog.CreateNamespaceInput{Slug: "curation", DisplayName: "Curation"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresCollectionStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	service, err := collections.New(store, clock.Real{}, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := service.Create(admin, collections.Input{NamespaceID: owner.ID, Name: "Team choices"})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range source.Items {
		collection, err = service.Change(admin, collection.ID, collection.Revision, collections.Add, collections.Patch{}, record.Listing.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	identityStore, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: identityStore, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	ownerGrant, err := authority.GrantRole(admin, identity.GrantRoleInput{Subject: "curator", ActorType: identity.ActorHuman, Role: identity.RoleContributor, NamespaceID: owner.ID, Reason: "Curate without target visibility"}, "curator")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := identityStore.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: "curator"})
	if err != nil {
		t.Fatal(err)
	}
	curator := identity.WithActor(t.Context(), principal)
	hidden, err := service.Members(curator, collection.ID, collections.MemberRequest{Limit: 1})
	if err != nil || len(hidden.Items) != 0 {
		t.Fatal("hidden target disclosure", hidden, err)
	}
	if _, err := service.Change(curator, collection.ID, collection.Revision, collections.Add, collections.Patch{}, source.Items[0].Listing.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("management granted source read", err)
	}
	// Add one later local release in the owner's namespace, beyond hidden members.
	resource, err := catalogService.CreateResource(admin, catalog.CreateResourceInput{NamespaceID: owner.ID, CanonicalIdentifier: "urn:air:catalog.example.com:curation:visible", Kind: catalog.KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := catalogService.CreateListing(admin, catalog.CreateListingInput{NamespaceID: owner.ID, ResourceID: resource.ID})
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := identityStore.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: "search-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	publish := func(version string) catalog.CatalogRecord {
		t.Helper()
		_, revision, _, err := catalogService.CreateVersion(admin, resource.ID, catalog.CreateVersionInput{Version: version, Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillMarkdown, Content: "---\nname: curated-visible\ndescription: Metadata fixture.\n---\n# Instructions\nRead metadata only.\n"}}})
		if err != nil {
			t.Fatal(err)
		}
		current, err := catalogService.GetListing(admin, listing.ID)
		if err != nil {
			t.Fatal(err)
		}
		current, _, err = catalogService.CreateListingRevision(admin, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: revision.ID, PublisherID: source.Items[0].ListingRevision.PublisherID, DisplayName: "Visible entry", Description: "Current collection entry and searchable fixture.", Visibility: catalog.VisibilityTenant}, current.Revision)
		if err != nil {
			t.Fatal(err)
		}
		submission, err := catalogService.Submit(admin, listing.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := catalogService.DecideSubmission(identity.WithActor(admin, reviewer), submission.ID, "approve", "Review exact fixture", submission.Revision); err != nil {
			t.Fatal(err)
		}
		_, release, _, err := catalogService.Publish(admin, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, current.Revision)
		if err != nil {
			t.Fatal(err)
		}
		result, err := catalogService.GetCatalogRecord(admin, release.ID)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := publish("1.0.0")
	collection, err = service.Change(admin, collection.ID, collection.Revision, collections.Add, collections.Patch{}, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.Members(curator, collection.ID, collections.MemberRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Release.ID != first.Release.ID {
		t.Fatal("hidden prefix consumed SQL limit", page, err)
	}

	filtered, err := service.Members(curator, collection.ID, collections.MemberRequest{Limit: 1, Text: "fixture"})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Release.ID != first.Release.ID {
		t.Fatal("hidden matching rows consumed filtered SQL limit", filtered, err)
	}
	filtered, err = service.Members(curator, collection.ID, collections.MemberRequest{Limit: 1, Text: "fixture", Kinds: []catalog.Kind{catalog.KindSkill}})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Release.ID != first.Release.ID {
		t.Fatal("kind and text intersection", filtered, err)
	}
	filtered, err = service.Members(curator, collection.ID, collections.MemberRequest{Limit: 1, Text: "fixture", Kinds: []catalog.Kind{catalog.KindAgent}})
	if err != nil || len(filtered.Items) != 0 {
		t.Fatal("nonmatching kind returned members", filtered, err)
	}
	replacement := publish("2.0.0")
	if _, err := service.Members(curator, collection.ID, collections.MemberRequest{Limit: 1, Revision: collection.Revision, Generation: page.Generation}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("stale release generation accepted", err)
	}
	page, err = service.Members(curator, collection.ID, collections.MemberRequest{Limit: 100})
	if err != nil || len(page.Items) != 1 || page.Items[0].Release.ID != replacement.Release.ID {
		t.Fatal("current release not followed", page, err)
	}
	_, err = catalogService.TransitionRelease(admin, replacement.Release.ID, catalog.ReleaseDeprecated, "Deprecate test release", "", nil, replacement.Release.Revision)
	if err != nil {
		t.Fatal(err)
	}
	page, err = service.Members(curator, collection.ID, collections.MemberRequest{Limit: 100})
	if err != nil || len(page.Items) != 1 || page.Items[0].Release.State != catalog.ReleaseDeprecated {
		t.Fatal("deprecated entry disappeared", page, err)
	}
	retired, err := catalogService.TransitionRelease(admin, replacement.Release.ID, catalog.ReleaseRetired, "Retire collection target", "", nil, page.Items[0].Release.Revision)
	if err != nil {
		t.Fatal(err)
	}
	page, err = service.Members(curator, collection.ID, collections.MemberRequest{Limit: 100})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("terminal release remained visible", page, err)
	}
	replacement = publish("3.0.0")
	page, err = service.Members(curator, collection.ID, collections.MemberRequest{Limit: 100})
	if err != nil || len(page.Items) != 1 || page.Items[0].Release.ID != replacement.Release.ID || page.Items[0].Release.ID == retired.ID {
		t.Fatal("retained membership did not reappear on a new eligible release", page, err)
	}
	collection, err = service.Change(curator, collection.ID, collection.Revision, collections.Remove, collections.Patch{}, source.Items[0].Listing.ID)
	if err != nil {
		t.Fatal("known hidden removal failed", err)
	}
	// Reuse the principal resolved before either grant. Fresh reads must use
	// retained policy, not the actor's earlier role projection or cached rows.
	targetGrant, err := authority.GrantRole(admin, identity.GrantRoleInput{Subject: "curator", ActorType: identity.ActorHuman, Role: identity.RoleReader, NamespaceID: source.Items[1].Listing.NamespaceID, Reason: "Temporarily read the referenced namespace"}, "curator-target-read")
	if err != nil {
		t.Fatal(err)
	}
	withTargetRead, err := service.Members(curator, collection.ID, collections.MemberRequest{Limit: 100})
	if err != nil || len(withTargetRead.Items) != 2 || withTargetRead.Collection.Revision != collection.Revision {
		t.Fatal("current target grant did not reveal the retained member", withTargetRead, err)
	}
	filtered, err = service.Members(curator, collection.ID, collections.MemberRequest{Limit: 100, Text: "fixture", Kinds: []catalog.Kind{catalog.KindSkill}})
	if err != nil || len(filtered.Items) != 2 {
		t.Fatal("current target grant did not reveal the filtered member", filtered, err)
	}
	wantedFiltered := []string{source.Items[1].Listing.ID, replacement.Listing.ID}
	sort.Strings(wantedFiltered)
	if got := []string{filtered.Items[0].Listing.ID, filtered.Items[1].Listing.ID}; !reflect.DeepEqual(got, wantedFiltered) {
		t.Fatal("filtered grant selected unexpected members", got, wantedFiltered)
	}
	if _, err := authority.RevokeRole(admin, targetGrant.ID, "Withdraw target read without changing collection access", "curator-target-revoke", targetGrant.Revision); err != nil {
		t.Fatal(err)
	}

	filtered, err = service.Members(curator, collection.ID, collections.MemberRequest{Limit: 1, Text: "fixture", Kinds: []catalog.Kind{catalog.KindSkill}})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Listing.ID != replacement.Listing.ID {
		t.Fatal("revocation did not remove filtered matches", filtered, err)
	}
	afterTargetRevoke, err := service.Members(curator, collection.ID, collections.MemberRequest{Limit: 100})
	if err != nil || len(afterTargetRevoke.Items) != 1 || afterTargetRevoke.Items[0].Listing.ID != replacement.Listing.ID || afterTargetRevoke.Collection.Revision != collection.Revision {
		t.Fatal("revoked target remained visible or changed curation", afterTargetRevoke, err)
	}
	other := semanticDisclosureActor(t, db, housekeepingTenantB)
	if _, err := service.Get(other, collection.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("foreign collection disclosed", err)
	}
	for _, query := range []string{`SELECT id FROM shared_collections`, `SELECT listing_id FROM shared_collection_members`} {
		_, err := db.worker.Exec(t.Context(), query)
		var denied *pgconn.PgError
		if !errors.As(err, &denied) || denied.Code != "42501" {
			t.Fatal("worker collection access did not fail on privileges", query, err)
		}
	}
	// Exact API role retains tenant RLS and same-tenant parent enforcement.
	memberCount, err := withTenantRead(t.Context(), db.api, housekeepingTenantA, func(tx pgx.Tx) (int, error) {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM shared_collection_members WHERE tenant_id=$1 AND collection_id=$2`, housekeepingTenantA, collection.ID).Scan(&count)
		return count, err
	})
	if err != nil || memberCount == 0 {
		t.Fatal("member RLS positive control", memberCount, err)
	}
	_, err = withTenantTx(t.Context(), db.api, housekeepingTenantB, pgx.TxOptions{}, func(tx pgx.Tx) (bool, error) {
		var count int
		if e := tx.QueryRow(t.Context(), `SELECT count(*) FROM shared_collection_members WHERE tenant_id=$1 AND collection_id=$2`, housekeepingTenantA, collection.ID).Scan(&count); e != nil || count != 0 {
			t.Fatal("member RLS disclosed another tenant", count, e)
		}
		members, e := tx.Exec(t.Context(), `DELETE FROM shared_collection_members WHERE tenant_id=$1 AND collection_id=$2`, housekeepingTenantA, collection.ID)
		if e != nil || members.RowsAffected() != 0 {
			t.Fatal("member RLS permitted cross-tenant delete", members, e)
		}
		tag, e := tx.Exec(t.Context(), `DELETE FROM shared_collections WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, collection.ID)
		if e == nil && tag.RowsAffected() != 0 {
			t.Fatal("RLS permitted cross-tenant delete")
		}
		return true, e
	})
	if err != nil {
		t.Fatal(err)
	}
	otherOwner, err := catalogService.CreateNamespace(other, catalog.CreateNamespaceInput{Slug: "other-curation", DisplayName: "Other tenant curation"})
	if err != nil {
		t.Fatal(err)
	}
	otherCollection, err := service.Create(other, collections.Input{NamespaceID: otherOwner.ID, Name: "Other tenant collection"})
	if err != nil {
		t.Fatal(err)
	}
	// Use the schema owner to isolate composite foreign keys from RLS and
	// permission admission. Both referenced objects exist, in different tenants.
	_, err = db.operator.Exec(t.Context(), `INSERT INTO shared_collection_members(tenant_id,collection_id,listing_id) VALUES($1,$2,$3)`, housekeepingTenantB, otherCollection.ID, replacement.Listing.ID)
	var foreignKey *pgconn.PgError
	if !errors.As(err, &foreignKey) || foreignKey.Code != "23503" || foreignKey.ConstraintName != "shared_collection_members_tenant_id_listing_id_fkey" {
		t.Fatal("member accepted another tenant's existing Listing", err)
	}
	_, err = db.operator.Exec(t.Context(), `INSERT INTO shared_collections(tenant_id,id,namespace_id,name,description,revision,created_at,updated_at) VALUES($1,gen_random_uuid(),$2,'Invalid owner','',1,statement_timestamp(),statement_timestamp())`, housekeepingTenantB, owner.ID)
	if !errors.As(err, &foreignKey) || foreignKey.Code != "23503" || foreignKey.ConstraintName != "shared_collections_tenant_id_namespace_id_fkey" {
		t.Fatal("collection accepted another tenant's existing namespace", err)
	}
	retained, err := service.Members(admin, collection.ID, collections.MemberRequest{Limit: 100})
	if err != nil || len(retained.Items) != 2 {
		t.Fatal("tenant denials changed the original members", retained, err)
	}
	// Restore scans accept unavailable members and expose no authored text.
	_, err = withTenantRead(t.Context(), db.operator, housekeepingTenantA, func(tx pgx.Tx) (bool, error) {
		records, more, e := scanRestoreCollections(t.Context(), tx, housekeepingTenantA, "", 100)
		if e != nil || more || len(records) != 1 || records[0].finding != nil {
			t.Fatal("restore collection qualification", records, more, e)
		}
		return true, e
	})
	if err != nil {
		t.Fatal(err)
	}
	// Operator corruption in this disposable schema is reported without text.
	if _, err := db.operator.Exec(t.Context(), `UPDATE shared_collections SET description=chr(1),revision=revision+1 WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, collection.ID); err != nil {
		t.Fatal(err)
	}
	_, err = withTenantRead(t.Context(), db.operator, housekeepingTenantA, func(tx pgx.Tx) (bool, error) {
		findings, more, e := scanRestoreCollections(t.Context(), tx, housekeepingTenantA, "", 100)
		if e != nil || more || len(findings) != 1 || findings[0].finding == nil {
			t.Fatal("restore accepted corrupt collection text", findings, more, e)
		}
		return true, e
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(t.Context(), `UPDATE shared_collections SET description='',revision=revision+1 WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, collection.ID); err != nil {
		t.Fatal(err)
	}
	collection, err = service.Get(curator, collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Change(curator, collection.ID, collection.Revision, collections.Delete, collections.Patch{}, ""); err != nil {
		t.Fatal("hidden membership blocked whole deletion", err)
	}
	if _, err := catalogService.GetCatalogRecord(admin, replacement.Release.ID); err != nil {
		t.Fatal("collection deletion changed source", err)
	}
	// Revoking owner authority hides an existing aggregate and denies edits;
	// neither the compact principal nor a previously resolved parent grants it.
	retainedCollection, err := service.Create(admin, collections.Input{NamespaceID: owner.ID, Name: "Owner revocation control"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := service.Get(curator, retainedCollection.ID); err != nil || got != retainedCollection {
		t.Fatal("owner read positive control", got, err)
	}
	if _, err := authority.RevokeRole(admin, ownerGrant.ID, "Withdraw collection owner permissions", "curator-owner-revoke", ownerGrant.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(curator, retainedCollection.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("revoked owner still read collection metadata", err)
	}
	if rows, err := service.List(curator, collections.ListRequest{Limit: 100}); err != nil || len(rows) != 0 {
		t.Fatal("revoked owner remained in the collection directory", rows, err)
	}
	if _, err := service.Change(curator, retainedCollection.ID, retainedCollection.Revision, collections.Delete, collections.Patch{}, ""); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("revoked owner still deleted the collection", err)
	}
	if got, err := service.Get(admin, retainedCollection.ID); err != nil || got != retainedCollection {
		t.Fatal("owner revocation or denied deletion changed retained metadata", got, err)
	}
}

func TestPostgresSharedCollectionQuotaRaces(t *testing.T) {
	db := newHousekeepingDatabase(t)
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	admin := semanticDisclosureActor(t, db, housekeepingTenantA)
	key := sha256.Sum256([]byte(t.Name()))
	catalogService, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "collection-quota-test"})
	if err != nil {
		t.Fatal(err)
	}
	namespaces := []string{}
	for n := 0; n < 11; n++ {
		ns, err := catalogService.CreateNamespace(admin, catalog.CreateNamespaceInput{Slug: fmt.Sprintf("quota-%d", n), DisplayName: "Quota fixture"})
		if err != nil {
			t.Fatal(err)
		}
		namespaces = append(namespaces, ns.ID)
	}
	store, err := NewPostgresCollectionStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	service, err := collections.New(store, clock.Real{}, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	// Operator-created rows are confined to this harness-owned disposable schema.
	if _, err := db.operator.Exec(t.Context(), `INSERT INTO shared_collections(tenant_id,id,namespace_id,name,description,revision,created_at,updated_at) SELECT $1,gen_random_uuid(),$2,'Quota fixture','',1,statement_timestamp(),statement_timestamp() FROM generate_series(1,100)`, housekeepingTenantA, namespaces[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(admin, collections.Input{NamespaceID: namespaces[0], Name: "Overflow"}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatal("namespace quota", err)
	}
	// Two creates compete for the final slot in the same owner namespace.
	if _, err := db.operator.Exec(t.Context(), `DELETE FROM shared_collections WHERE tenant_id=$1 AND id=(SELECT id FROM shared_collections WHERE tenant_id=$1 AND namespace_id=$2 ORDER BY id LIMIT 1)`, housekeepingTenantA, namespaces[0]); err != nil {
		t.Fatal(err)
	}
	namespaceResults := make(chan error, 2)
	var namespaceWG sync.WaitGroup
	for range 2 {
		namespaceWG.Go(func() {
			_, e := service.Create(admin, collections.Input{NamespaceID: namespaces[0], Name: "Last namespace slot"})
			namespaceResults <- e
		})
	}
	namespaceWG.Wait()
	close(namespaceResults)
	admitted, capacityConflicts := 0, 0
	for err := range namespaceResults {
		switch {
		case err == nil:
			admitted++
		case catalog.ErrorCodeOf(err) == catalog.CodeConflict:
			capacityConflicts++
		default:
			t.Fatal(err)
		}
	}
	if admitted != 1 || capacityConflicts != 1 {
		t.Fatal("concurrent namespace capacity", admitted, capacityConflicts)
	}
	var namespaceCount int
	if err := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM shared_collections WHERE tenant_id=$1 AND namespace_id=$2`, housekeepingTenantA, namespaces[0]).Scan(&namespaceCount); err != nil || namespaceCount != 100 {
		t.Fatal(namespaceCount, err)
	}
	if _, err := db.operator.Exec(t.Context(), `DELETE FROM shared_collections WHERE tenant_id=$1 AND namespace_id=$2`, housekeepingTenantA, namespaces[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(t.Context(), `INSERT INTO shared_collections(tenant_id,id,namespace_id,name,description,revision,created_at,updated_at) SELECT $1,gen_random_uuid(),($2::uuid[])[1+(n%11)],'Quota fixture','',1,statement_timestamp(),statement_timestamp() FROM generate_series(0,998) n`, housekeepingTenantA, namespaces); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, ns := range namespaces[:2] {
		wg.Go(func() {
			_, err := service.Create(admin, collections.Input{NamespaceID: ns, Name: "Last tenant slot"})
			results <- err
		})
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
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("concurrent creates exceeded tenant quota", successes, conflicts)
	}
	var total int
	if err := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM shared_collections WHERE tenant_id=$1`, housekeepingTenantA).Scan(&total); err != nil || total != 1000 {
		t.Fatal(total, err)
	}
}

func TestPostgresSharedCollectionMemberQuotaAndAuditRollback(t *testing.T) {
	db, repo, _, seed := newSearchCostFixture(t)
	seed(0)
	seed(1)
	admin := semanticDisclosureActor(t, db, housekeepingTenantA)
	records, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 10})
	if err != nil || len(records.Items) != 2 {
		t.Fatal(records, err)
	}
	store, err := NewPostgresCollectionStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	service, err := collections.New(store, clock.Real{}, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ns := records.Items[0].Listing.NamespaceID
	packed, err := service.Create(admin, collections.Input{NamespaceID: ns, Name: "Retained unavailable references"})
	if err != nil {
		t.Fatal(err)
	}
	// A waiting edit respects cancellation without advancing the aggregate.
	locked, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(t.Context(), locked)()
	if _, err := locked.Exec(t.Context(), `SELECT id FROM shared_collections WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, housekeepingTenantA, packed.ID); err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(admin, 100*time.Millisecond)
	name := "Canceled edit"
	_, waitErr := service.Change(waiting, packed.ID, packed.Revision, collections.Update, collections.Patch{Name: &name}, "")
	cancel()
	if !errors.Is(waitErr, context.DeadlineExceeded) {
		t.Fatal("lock wait did not respect deadline", waitErr)
	}
	if err := locked.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	afterWait, err := service.Get(admin, packed.ID)
	if err != nil || afterWait.Revision != packed.Revision || afterWait.Name != packed.Name {
		t.Fatal("canceled edit changed state", afterWait, err)
	}
	principal, ok := identity.FromContext(admin)
	if !ok {
		t.Fatal("principal missing")
	}
	// Bounded synthetic retained references are local and same-tenant. Their
	// current ineligibility is valid collection state; it does not free capacity.
	rows, err := db.operator.Query(t.Context(), `WITH resources_inserted AS (
 INSERT INTO resources(id,tenant_id,namespace_id,canonical_identifier,kind,created_by,revision,created_at,updated_at)
 SELECT gen_random_uuid(),$1,$2,'urn:air:catalog.example.com:performance:retained-quota-'||n,'skill',$3,1,statement_timestamp(),statement_timestamp() FROM generate_series(1,200) n RETURNING id
 ) INSERT INTO catalog_listings(id,tenant_id,namespace_id,resource_id,revision,created_at,updated_at)
 SELECT gen_random_uuid(),$1,$2,id,1,statement_timestamp(),statement_timestamp() FROM resources_inserted RETURNING id`, housekeepingTenantA, ns, principal.MembershipID)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		ids = append(ids, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) != 200 {
		t.Fatal(len(ids), err)
	}
	if _, err := db.operator.Exec(t.Context(), `INSERT INTO shared_collection_members(tenant_id,collection_id,listing_id) SELECT $1,$2,unnest($3::uuid[])`, housekeepingTenantA, packed.ID, ids[:199]); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, record := range records.Items {
		wg.Go(func() {
			_, err := service.Change(admin, packed.ID, packed.Revision, collections.Add, collections.Patch{}, record.Listing.ID)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	successes, stale := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case catalog.ErrorCodeOf(err) == catalog.CodePreconditionFailed:
			stale++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatal("member ETag race", successes, stale)
	}
	packed, err = service.Get(admin, packed.ID)
	if err != nil || packed.Revision != 2 {
		t.Fatal(packed, err)
	}
	page, err := service.Members(admin, packed.ID, collections.MemberRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("hidden capacity/page", page, err)
	}
	var extra string
	for _, record := range records.Items {
		if record.Release.ID != page.Items[0].Release.ID {
			extra = record.Listing.ID
		}
	}
	if _, err := service.Change(admin, packed.ID, packed.Revision, collections.Add, collections.Patch{}, extra); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatal("SQL member quota not enforced", err)
	}
	if _, err := db.operator.Exec(t.Context(), `INSERT INTO shared_collection_members(tenant_id,collection_id,listing_id) VALUES($1,$2,$3)`, housekeepingTenantA, packed.ID, ids[199]); err == nil {
		t.Fatal("database guard accepted 201st member")
	}
	packed, err = service.Change(admin, packed.ID, packed.Revision, collections.Remove, collections.Patch{}, ids[0])
	if err != nil {
		t.Fatal("known ineligible member removal", err)
	}
	// Capture retained rows as well as eligible projections: most references are
	// deliberately ineligible and would be invisible to a member-page check.
	state := func(t *testing.T) (int, []string, int) {
		t.Helper()
		var parents, audits int
		var members []string
		err := db.operator.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM shared_collections WHERE tenant_id=$1),
 (SELECT coalesce(array_agg(listing_id::text ORDER BY listing_id),ARRAY[]::text[]) FROM shared_collection_members WHERE tenant_id=$1 AND collection_id=$2),
 (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action LIKE 'collection.%')`, housekeepingTenantA, packed.ID).Scan(&parents, &members, &audits)
		if err != nil {
			t.Fatal(err)
		}
		return parents, members, audits
	}
	beforeParents, beforeMembers, beforeAudits := state(t)
	// Fail at audit insertion, after each mutation's domain SQL has executed.
	if _, err := db.operator.Exec(t.Context(), `CREATE FUNCTION fail_collection_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'collection.%' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; REVOKE ALL ON FUNCTION fail_collection_audit() FROM PUBLIC; CREATE TRIGGER fail_collection_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION fail_collection_audit()`); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []collections.Operation{collections.Create, collections.Update, collections.Add, collections.Remove, collections.Delete} {
		t.Run(string(operation), func(t *testing.T) {
			var commandErr error
			switch operation {
			case collections.Create:
				_, commandErr = service.Create(admin, collections.Input{NamespaceID: ns, Name: "Uncommitted collection"})
			case collections.Update:
				name, description := "Uncommitted name", "Uncommitted description"
				_, commandErr = service.Change(admin, packed.ID, packed.Revision, operation, collections.Patch{Name: &name, Description: &description}, "")
			case collections.Add:
				_, commandErr = service.Change(admin, packed.ID, packed.Revision, operation, collections.Patch{}, extra)
			case collections.Remove:
				_, commandErr = service.Change(admin, packed.ID, packed.Revision, operation, collections.Patch{}, ids[1])
			case collections.Delete:
				_, commandErr = service.Change(admin, packed.ID, packed.Revision, operation, collections.Patch{}, "")
			}
			var failure *pgconn.PgError
			if catalog.ErrorCodeOf(commandErr) != catalog.CodeDependencyUnavailable || !errors.As(commandErr, &failure) || failure.Message != "injected audit failure" {
				t.Fatalf("command did not reach the intended SQL audit failure: %v", commandErr)
			}
			after, err := service.Get(admin, packed.ID)
			if err != nil || after != packed {
				t.Fatal("audit failure changed parent metadata or revision", after, err)
			}
			parents, members, audits := state(t)
			if parents != beforeParents || audits != beforeAudits || !reflect.DeepEqual(members, beforeMembers) {
				t.Fatalf("audit failure retained partial rows: parents=%d/%d members=%v audits=%d/%d", parents, beforeParents, members, audits, beforeAudits)
			}
		})
	}
	if _, err := db.operator.Exec(t.Context(), `DROP TRIGGER fail_collection_audit ON audit_events; DROP FUNCTION fail_collection_audit()`); err != nil {
		t.Fatal(err)
	}
	after, err := service.Get(admin, packed.ID)
	if err != nil || after.Revision != packed.Revision {
		t.Fatal("audit failure advanced parent", after, err)
	}
	page, err = service.Members(admin, packed.ID, collections.MemberRequest{Limit: 100})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("audit failure retained child", page, err)
	}
	packed, err = service.Change(admin, packed.ID, packed.Revision, collections.Add, collections.Patch{}, extra)
	if err != nil {
		t.Fatal("rollback prevented later valid addition", err)
	}
	page, err = service.Members(admin, packed.ID, collections.MemberRequest{Limit: 100})
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	if _, err := service.Change(admin, packed.ID, packed.Revision, collections.Delete, collections.Patch{}, ""); err != nil {
		t.Fatal("bounded full collection deletion", err)
	}
}

func TestPostgresCollectionFiltersReleasedFieldsAndBoundedPages(t *testing.T) {
	documents := []catalog.SearchDocument{
		{Identifier: "urn:air:catalog.example.com:finance:first", DisplayName: "Invoice", Description: "CAFÉ"},
		{Identifier: "urn:air:catalog.example.com:finance:invoice-cafe", DisplayName: "Unrelated", Description: "Other", Tags: []string{"invoice", "café"}},
		{Identifier: "urn:air:catalog.example.com:finance:second", DisplayName: "INVOICE café", Description: "Second"},
		{Identifier: "urn:air:catalog.example.com:finance:third", DisplayName: "Invoice", Description: "Café third"},
		{Identifier: "urn:air:catalog.example.com:finance:fourth", DisplayName: "Invoice cafeteria", Description: "Other"},
		{Identifier: "urn:air:catalog.example.com:finance:agent", DisplayName: "Invoice Agent", Description: "CAFÉ"},
		{Identifier: "urn:air:catalog.example.com:finance:elsewhere", DisplayName: "Invoice", Description: "CAFÉ in another collection"},
	}
	db, repo, _, seed := newSearchCostFixture(t, documents...)
	for n := range documents {
		if n == 5 {
			seed(n, json.RawMessage(`{"name":"Collection Agent","description":"Metadata fixture","version":"1.0.0","supportedInterfaces":[{"url":"https://agent.example.com/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],"skills":[{"id":"collection","name":"Collection","description":"Read metadata only","tags":["collection"]}]}`))
		} else {
			seed(n)
		}
	}
	admin := semanticDisclosureActor(t, db, housekeepingTenantA)
	source, err := repo.SearchCatalog(admin, housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 100})
	if err != nil || len(source.Items) != len(documents) {
		t.Fatal(source, err)
	}
	store, err := NewPostgresCollectionStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	service, err := collections.New(store, clock.Real{}, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := service.Create(admin, collections.Input{NamespaceID: source.Items[0].Listing.NamespaceID, Name: "Filtered selection"})
	if err != nil {
		t.Fatal(err)
	}
	byIdentifier := map[string]catalog.CatalogSummary{}
	for _, record := range source.Items {
		byIdentifier[record.Resource.CanonicalIdentifier] = record
		if record.Resource.CanonicalIdentifier == documents[6].Identifier {
			continue
		}
		collection, err = service.Change(admin, collection.ID, collection.Revision, collections.Add, collections.Patch{}, record.Listing.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	// A valid matching member of a second collection must not enter this page.
	otherCollection, err := service.Create(admin, collections.Input{NamespaceID: collection.NamespaceID, Name: "Other selection"})
	if err != nil {
		t.Fatal(err)
	}
	outside := byIdentifier[documents[6].Identifier]
	if _, err = service.Change(admin, otherCollection.ID, otherCollection.Revision, collections.Add, collections.Patch{}, outside.Listing.ID); err != nil {
		t.Fatal(err)
	}
	outsidePage, err := service.Members(admin, otherCollection.ID, collections.MemberRequest{Limit: 1, Text: "invoice café", Kinds: []catalog.Kind{catalog.KindSkill}})
	if err != nil || len(outsidePage.Items) != 1 || outsidePage.Items[0].Listing.ID != outside.Listing.ID {
		t.Fatal("other collection matching control", outsidePage, err)
	}
	// Keep an unpublished candidate whose metadata would change matching if the
	// selector incorrectly joined the mutable candidate instead of the release.
	original := byIdentifier[documents[0].Identifier]
	key := sha256.Sum256([]byte(t.Name()))
	domain, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "collection-filter-test"})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := domain.GetListing(admin, original.Listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = domain.CreateListingRevision(admin, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: original.VersionRevision.ID, PublisherID: original.ListingRevision.PublisherID, DisplayName: "Candidate-only", Description: "Draft replacement", Visibility: catalog.VisibilityTenant}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	wanted := []string{original.Listing.ID, byIdentifier[documents[2].Identifier].Listing.ID, byIdentifier[documents[3].Identifier].Listing.ID}
	agent := byIdentifier[documents[5].Identifier]
	for _, selection := range []struct {
		name  string
		kinds []catalog.Kind
		ids   []string
	}{
		{"skills", []catalog.Kind{catalog.KindSkill, catalog.KindSkill}, wanted},
		{"agent", []catalog.Kind{catalog.KindAgent}, []string{agent.Listing.ID}},
		{"mixed", []catalog.Kind{catalog.KindSkill, catalog.KindAgent, catalog.KindSkill}, append([]string{agent.Listing.ID}, wanted...)},
		{"all kinds", nil, append([]string{agent.Listing.ID}, wanted...)},
	} {
		sort.Strings(selection.ids)
		after := ""
		for n, want := range selection.ids {
			page, err := service.Members(admin, collection.ID, collections.MemberRequest{Limit: 1, Text: "  INVOICE\u2003café ", Kinds: selection.kinds, AfterID: after})
			if err != nil || len(page.Items) != min(2, len(selection.ids)-n) || page.Items[0].Listing.ID != want {
				t.Fatal(selection.name, "filters after limit or incorrect collection/lexical/released fields", page, err)
			}
			if page.Items[0].Listing.ID == original.Listing.ID && (page.Items[0].ListingRevision.ID != original.ListingRevision.ID || page.Items[0].ListingRevision.DisplayName != documents[0].DisplayName || page.Items[0].ListingRevision.Description != documents[0].Description) {
				t.Fatal("unpublished candidate replaced released response metadata", page.Items[0])
			}
			after = want
		}
		page, err := service.Members(admin, collection.ID, collections.MemberRequest{Limit: 1, Text: "invoice café", Kinds: selection.kinds, AfterID: after})
		if err != nil || len(page.Items) != 0 {
			t.Fatal(selection.name, "filtered traversal did not end", page, err)
		}
	}
	for _, text := range []string{"candidate-only", "inv", "%_*", "cafe"} {
		page, err := service.Members(admin, collection.ID, collections.MemberRequest{Limit: 100, Text: text})
		if err != nil || len(page.Items) != 0 {
			t.Fatal("unreleased, identifier-only, prefix or wildcard match", text, page, err)
		}
	}
	for _, kind := range []catalog.Kind{catalog.KindMCPServer, catalog.KindPlugin} {
		page, err := service.Members(admin, collection.ID, collections.MemberRequest{Limit: 100, Kinds: []catalog.Kind{kind}})
		if err != nil || len(page.Items) != 0 {
			t.Fatal("kind predicate ignored", kind, page, err)
		}
	}
	probe, err := service.Members(admin, collection.ID, collections.MemberRequest{Text: "invoice", Kinds: []catalog.Kind{catalog.KindSkill}})
	if err != nil || len(probe.Items) != 0 {
		t.Fatal("zero-limit probe hydrated members", probe, err)
	}
	if _, err := service.Members(admin, collection.ID, collections.MemberRequest{Limit: 1, Text: "invoice", Revision: collection.Revision + 1, Generation: probe.Generation}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("parent selection changed between probe and query", err)
	}
	if _, err := service.Members(semanticDisclosureActor(t, db, housekeepingTenantB), collection.ID, collections.MemberRequest{Limit: 1, Text: "invoice", Kinds: []catalog.Kind{catalog.KindSkill}}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("filtered cross-tenant disclosure", err)
	}
}
