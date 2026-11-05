//go:build integration

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type personalTestAuthority struct{}

func (personalTestAuthority) Admit(context.Context, personal.Reference) error { return nil }

func TestPostgresPersonalLibraryOwnershipQuotaAndAtomicAudit(t *testing.T) {
	db := newHousekeepingDatabase(t)
	var schema string
	if e := db.operator.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&schema); e != nil {
		t.Fatal(e)
	}
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	owner := func(subject, issuer string) context.Context {
		t.Helper()
		p, e := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: issuer, TenancyMode: "dedicated", Subject: subject, ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}, Operator: "personal-test", Reason: "Qualify private human library", AcknowledgeOutbox: true})
		if e != nil {
			t.Fatal(e)
		}
		return postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: subject, MembershipID: p.MembershipID, RealmID: p.RealmID, ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
	}
	ctx := owner("same-subject", "https://personal.example")
	other := owner("other-human", "https://personal.example")
	otherRealm := owner("same-subject", "https://personal-other.example")
	otherTenant := semanticDisclosureActor(t, db, housekeepingTenantB)
	store, err := NewPostgresPersonalStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	service, err := personal.New(store, personalTestAuthority{}, clock.Real{}, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	input := personal.Input{Type: "search", Label: "Private catalog search", Search: &personal.Criteria{Order: catalog.SearchOrderPublisherDesc, Text: "private-text-sentinel", Origins: []catalog.SearchResultType{catalog.SearchResultLocal}}}
	item, err := service.Save(ctx, "", 0, input, false)
	if err != nil {
		t.Fatal(err)
	}
	qualifyPersonalLabelFilters(t, store, ctx)
	// The label matches an actual owner item; other retained human contexts
	// must not discover it through the filtered collection either.
	for _, otherContext := range []context.Context{other, otherRealm, otherTenant} {
		page, e := service.List(otherContext, personal.ListRequest{Label: &input.Label, Limit: 1})
		if e != nil || len(page) != 0 {
			t.Fatal("SQL owner/realm/tenant label isolation", page, e)
		}
	}
	page, err := service.List(ctx, personal.ListRequest{Label: &input.Label, Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != item.ID {
		t.Fatal("SQL owner label positive control", page, err)
	}

	// The physical JSONB encoding includes separator spaces. Its own fixed
	// retained-input ceiling must be a safe input refusal, not an internal error.
	edge := personal.Input{Type: "search", Label: strings.Repeat("x", 120), Search: &personal.Criteria{License: strings.Repeat("x", 256), Publisher: strings.Repeat("x", 256)}}
	for i := range 32 {
		edge.Search.Tags = append(edge.Search.Tags, strings.Repeat("🙂", 78)+fmt.Sprintf("%02d", i))
	}
	for i := range 16 {
		edge.Search.Categories = append(edge.Search.Categories, strings.Repeat("🙂", 78)+fmt.Sprintf("%02d", i))
	}
	encoded, encodeErr := json.Marshal(edge)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	filler := personal.MaxBodyBytes - len(encoded)
	if filler < 0 || filler > 500 {
		t.Fatal("boundary fixture outside text limit", filler)
	}
	edge.Search.Text = strings.Repeat("x", filler)
	if _, e := service.Save(ctx, "", 0, edge, false); catalog.ErrorCodeOf(e) != catalog.CodeInvalidArgument {
		t.Fatal("retained input bound did not refuse safely", e)
	}
	if _, err = service.Get(other, item.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("another administrator read owner item", err)
	}
	for _, otherContext := range []context.Context{otherRealm, otherTenant} {
		if _, e := service.Get(otherContext, item.ID); catalog.ErrorCodeOf(e) != catalog.CodeNotFound {
			t.Fatal("cross realm or tenant read", e)
		}
	}
	a, _ := identity.FromContext(ctx)
	if _, err = store.Get(other, a, item.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("caller-selected owner accepted", err)
	}
	// A fresh store uses PostgreSQL, not process-local retention.
	fresh, _ := NewPostgresPersonalStore(repo)
	again, _ := personal.New(fresh, personalTestAuthority{}, clock.Real{}, id.Generator{})
	if got, e := again.Get(ctx, item.ID); e != nil || got.Search.Text != input.Search.Text || got.Search.Order != catalog.SearchOrderPublisherDesc {
		t.Fatal(got, e)
	}
	input.Label = "Renamed"
	item, err = service.Save(ctx, item.ID, item.Revision, input, false)
	if err != nil || item.Revision != 2 {
		t.Fatal(item, err)
	}
	if _, err = service.Save(ctx, item.ID, 1, input, false); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("stale write accepted", err)
	}
	// Owner RLS remains effective even when an explicit SQL predicate is omitted.
	_, err = withTenantRead(other, db.api, housekeepingTenantA, func(tx pgx.Tx) (struct{}, error) {
		var count int
		e := tx.QueryRow(other, `SELECT count(*) FROM personal_items`).Scan(&count)
		if e == nil && count != 0 {
			t.Fatal("RLS exposed another owner")
		}
		return struct{}{}, e
	})
	if err != nil {
		t.Fatal(err)
	}
	// Actual restore constraint digest is derived from this newly migrated PostgreSQL 18 schema.
	_, err = withTenantRead(ctx, db.api, housekeepingTenantA, func(tx pgx.Tx) (struct{}, error) {
		if e := verifyRestoreConstraints(ctx, tx, schema); e != nil {
			return struct{}{}, e
		}
		if e := verifyRestoreRLS(ctx, tx, schema); e != nil {
			return struct{}{}, e
		}
		if e := verifyRestoreTriggers(ctx, tx, schema); e != nil {
			return struct{}{}, e
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Populate to one below the hard quota through the actual audited service.
	for i := 1; i < personal.MaxItems-1; i++ {
		if _, e := service.Save(ctx, "", 0, input, false); e != nil {
			t.Fatal(i, e)
		}
	}
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, e := service.Save(ctx, "", 0, input, false); outcomes <- e })
	}
	wg.Wait()
	close(outcomes)
	success := 0
	for e := range outcomes {
		if e == nil {
			success++
		} else if catalog.ErrorCodeOf(e) != catalog.CodeConflict {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("concurrent quota writes", success)
	}
	// Audit failure rolls back a valid mutation, with no public outbox notification.
	var before int
	if e := db.operator.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{schema, "outbox_events"}.Sanitize()).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if _, e := db.operator.Exec(ctx, `CREATE FUNCTION `+pgx.Identifier{schema, "personal_test_fail_audit"}.Sanitize()+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='personal.item.updated' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER personal_test_fail_audit BEFORE INSERT ON `+pgx.Identifier{schema, "audit_events"}.Sanitize()+` FOR EACH ROW EXECUTE FUNCTION `+pgx.Identifier{schema, "personal_test_fail_audit"}.Sanitize()+`() `); e != nil {
		t.Fatal(e)
	}
	input.Label = "Must not commit"
	if _, e := service.Save(ctx, item.ID, item.Revision, input, false); e == nil {
		t.Fatal("audit failure accepted")
	}
	if got, e := service.Get(ctx, item.ID); e != nil || got.Label != "Renamed" || got.Revision != 2 {
		t.Fatal("audit rollback failed", got, e)
	}
	var after int
	if e := db.operator.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{schema, "outbox_events"}.Sanitize()).Scan(&after); e != nil || after != before {
		t.Fatal("private preference produced outbox", after, before, e)
	}
}

// Exercise literal selection through the exact API role before the existing quota/atomicity campaign.
func qualifyPersonalLabelFilters(t *testing.T, store *PostgresPersonalStore, ctx context.Context) {
	t.Helper()
	serviceClock := clock.NewFake(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
	service, err := personal.New(store, personalTestAuthority{}, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	labels := []string{"Earlier unrelated", "Exact", "exact", " Exact ", "café", "cafe\u0301", "%_", strings.Repeat("🙂", 120), "Exact"}
	items := make([]personal.Item, 0, len(labels))
	want := []string{}
	for _, label := range labels {
		serviceClock.Advance(time.Millisecond)
		item, err := service.Save(ctx, "", 0, personal.Input{Type: "search", Label: label, Search: &personal.Criteria{Text: "private"}}, false)
		if err != nil {
			t.Fatal("label fixture", err)
		}
		items = append(items, item)
		if label == "Exact" {
			want = append(want, item.ID)
		}
	}
	label := "Exact"
	page, err := service.List(ctx, personal.ListRequest{Label: &label, Type: "search", Limit: 1})
	if err != nil || len(page) != 2 || page[0].ID != want[0] || page[1].ID != want[1] {
		t.Fatal("label before SQL limit plus one", page, err)
	}
	page, err = service.List(ctx, personal.ListRequest{Label: &label, AfterID: want[0], Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != want[1] {
		t.Fatal("SQL label continuation", page, err)
	}
	for _, literal := range labels[2:8] {
		page, err := service.List(ctx, personal.ListRequest{Label: &literal, Limit: 1})
		if err != nil || len(page) != 1 || page[0].Label != literal {
			t.Fatal("SQL changed literal spelling", literal, page, err)
		}
	}
	page, err = service.List(ctx, personal.ListRequest{Label: &label, Type: "resource", Limit: 1})
	if err != nil || len(page) != 0 {
		t.Fatal("SQL type/label intersection", page, err)
	}
	for _, invalid := range []string{"", "   ", "\n", strings.Repeat("🙂", 121)} {
		if _, err := service.List(ctx, personal.ListRequest{Label: &invalid, Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal("invalid SQL label", err)
		}
		actor, _ := identity.FromContext(ctx)
		if _, err := store.List(ctx, actor, personal.ListRequest{Label: &invalid, Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal("invalid direct SQL-store label", err)
		}
	}
	// Omitting the label keeps the original ID ordering and includes the preexisting saved search.
	page, err = service.List(ctx, personal.ListRequest{Limit: 100})
	if err != nil || len(page) != len(items)+1 || !slices.IsSortedFunc(page, func(a, b personal.Item) int { return strings.Compare(a.ID, b.ID) }) {
		t.Fatal("unfiltered SQL default", page, err)
	}
	for _, item := range items {
		serviceClock.Advance(time.Millisecond)
		if _, err := service.Save(ctx, item.ID, item.Revision, personal.Input{}, true); err != nil {
			t.Fatal("remove owned filter fixture", err)
		}
	}
}

func TestPostgresMyWorkExactAttributionAndQueues(t *testing.T) {
	db, repo, _, seed := newSearchCostFixture(t)
	seed(0)
	ctx := semanticDisclosureActor(t, db, housekeepingTenantA)
	// Seed creates a published release. Its exact submission is never returned
	// as approved-and-never-released, regardless of current release state.
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte("01234567890123456789012345678901"), Policy: "my-work"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: "approved", Limit: 1})
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	records, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 1})
	if err != nil || len(records.Items) != 1 {
		t.Fatal(records, err)
	}
	record := records.Items[0]
	// This caller differs from the author despite having administrator authority.
	resource, err := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: record.Resource.NamespaceID, CanonicalIdentifier: "urn:air:catalog.example.com:performance:my-draft", Kind: catalog.KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: "drafts", Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].ResourceID != resource.ID || rows[0].Type != "resource" {
		t.Fatal(rows, err)
	}
	_, revision, _, err := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillMarkdown, Content: "---\nname: personal-work\ndescription: Synthetic personal work fixture.\n---\n# Instructions\nRead metadata only.\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: "drafts", Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].VersionRevisionID != revision.ID || rows[0].Type != "version" {
		t.Fatal(rows, err)
	}
	listing, err := service.CreateListing(ctx, catalog.CreateListingInput{NamespaceID: record.Resource.NamespaceID, ResourceID: resource.ID})
	if err != nil {
		t.Fatal(err)
	}
	listing, lr, err := service.CreateListingRevision(ctx, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: revision.ID, DisplayName: "My exact candidate", Description: "A bounded fixture", PublisherID: record.ListingRevision.PublisherID, Visibility: catalog.VisibilityTenant}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: "drafts", Limit: 1})
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	if rows[0].ListingRevisionID != lr.ID {
		t.Fatal("candidate revision attribution missing", rows)
	}
	submitted, err := service.Submit(ctx, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: "submitted", Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].SubmissionID != submitted.ID {
		t.Fatal(rows, err)
	}
	for _, queue := range []string{"changes_required", "approved"} {
		rows, err = service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: queue, Limit: 1})
		if err != nil || len(rows) != 0 {
			t.Fatal(queue, rows, err)
		}
	}
	// Exercise both positive decision queues through independent review and
	// immutable resubmission, rather than accepting only their empty results.
	reviewer := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "personal-work-reviewer", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReviewer}})
	if _, _, err := service.DecideSubmission(reviewer, submitted.ID, "request_changes", "Clarify the synthetic listing description", submitted.Revision); err != nil {
		t.Fatal(err)
	}
	rows, err = service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: "changes_required", Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].SubmissionID != submitted.ID || rows[0].ListingRevisionID != lr.ID || rows[0].CurrentCandidateRevisionID != lr.ID {
		t.Fatal("changes-required queue lost the exact submission", rows, err)
	}
	correction := catalog.CreateListingRevisionInput{VersionRevisionID: revision.ID, DisplayName: "Corrected exact candidate", Description: "A clarified bounded fixture", PublisherID: record.ListingRevision.PublisherID, Visibility: catalog.VisibilityTenant}
	listing, corrected, err := service.CreateListingRevision(ctx, listing.ID, correction, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: "changes_required", Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].ListingRevisionID != lr.ID || rows[0].CurrentCandidateRevisionID != corrected.ID || rows[0].DisplayName != "My exact candidate" {
		t.Fatal("correction replaced immutable submission context", rows, err)
	}
	resubmitted, err := service.Submit(ctx, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: "changes_required", Limit: 1})
	if err != nil || len(rows) != 0 {
		t.Fatal("resubmitted correction remains pending", rows, err)
	}
	rows, err = service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: "submitted", Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].SubmissionID != resubmitted.ID || rows[0].ListingRevisionID != corrected.ID {
		t.Fatal("resubmission did not bind the correction", rows, err)
	}
	if _, _, err := service.DecideSubmission(reviewer, resubmitted.ID, "approve", "Synthetic correction reviewed independently", resubmitted.Revision); err != nil {
		t.Fatal(err)
	}
	rows, err = service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: "approved", Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].SubmissionID != resubmitted.ID {
		t.Fatal("approved-and-never-released queue", rows, err)
	}
	if other, e := service.ListPersonalWork(reviewer, catalog.PersonalWorkRequest{Queue: "approved", Limit: 1}); e != nil || len(other) != 0 {
		t.Fatal("reviewer inherited submitter ownership", other, e)
	}
	correction.DisplayName = "A later unsubmitted candidate"
	listing, later, err := service.CreateListingRevision(ctx, listing.ID, correction, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: "approved", Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].ListingRevisionID != corrected.ID || rows[0].CurrentCandidateRevisionID != later.ID {
		t.Fatal("later candidate revoked or rewrote exact approval", rows, err)
	}
	if _, _, _, err := service.Publish(ctx, listing.ID, catalog.PublishInput{SubmissionID: resubmitted.ID}, listing.Revision); err != nil {
		t.Fatal(err)
	}
	rows, err = service.ListPersonalWork(ctx, catalog.PersonalWorkRequest{Queue: "approved", Limit: 1})
	if err != nil || len(rows) != 0 {
		t.Fatal("released submission remains in never-released queue", rows, err)
	}
}
