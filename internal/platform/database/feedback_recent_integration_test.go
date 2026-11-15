//go:build integration

// Recent feedback qualifies real role-scoped SQL, mutable paging and its index.
package database

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresFeedbackRecentCurrentPagesAndIndex(t *testing.T) {
	db, repo, _, _ := newSearchCostFixture(t)
	author := semanticDisclosureActor(t, db, housekeepingTenantA)
	a, _ := identity.FromContext(author)
	c := clock.NewFake(time.Now().UTC().Truncate(time.Microsecond).Add(time.Minute))
	key := sha256.Sum256([]byte(t.Name()))
	resources, err := catalog.NewService(catalog.Options{Repository: repo, Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "recent-feedback"})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := resources.CreateNamespace(author, catalog.CreateNamespaceInput{Slug: "recent", DisplayName: "Recent feedback"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := resources.CreateResource(author, catalog.CreateResourceInput{NamespaceID: ns.ID, CanonicalIdentifier: "urn:air:catalog.example.com:recent:feedback", Kind: catalog.KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresFeedbackStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := feedback.NewService(store, repo, c, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]feedback.Record, 60)
	for i := range rows {
		c.Advance(time.Second)
		rows[i], err = svc.Create(author, feedback.CreateInput{ResourceID: resource.ID, Category: "problem", Comment: fmt.Sprintf("Owned recent case %d", i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	defaultPage, err := svc.List(author, feedback.ListInput{Limit: 20}, false)
	if err != nil || len(defaultPage) != 21 || defaultPage[0].ID != rows[0].ID || defaultPage[20].ID != rows[20].ID {
		t.Fatal("creation order or sentinel changed", defaultPage, err)
	}
	address := func(index int, response string) {
		t.Helper()
		rows[index], err = svc.Change(author, rows[index].ID, rows[index].Revision, "address", feedback.EditInput{}, response)
		if err != nil {
			t.Fatal(err)
		}
	}
	c.Advance(time.Hour)
	address(58, "Earlier current reply")
	c.Advance(time.Hour)
	address(30, "Recent reply to an older submission")
	address(31, "Tied update time")
	if !rows[30].UpdatedAt.Equal(rows[31].UpdatedAt) {
		t.Fatal("tie fixture is not tied")
	}
	c.Advance(time.Hour)
	address(0, "")
	read := func(q feedback.ListInput, want ...int) []feedback.Record {
		t.Helper()
		page, e := svc.List(author, q, false)
		if e != nil || len(page) != len(want) {
			t.Fatal("recent page", page, e)
		}
		for i, index := range want {
			if page[i].ID != rows[index].ID {
				t.Fatalf("row %d=%s want %s", i, page[i].ID, rows[index].ID)
			}
		}
		return page
	}
	q := feedback.ListInput{Order: feedback.OrderUpdatedDesc, State: "addressed", Category: "problem", ResourceID: resource.ID, Limit: 2}
	first := read(q, 0, 31, 30)
	if first[0].Response != "" || first[0].State != "addressed" {
		t.Fatal("empty response invented text", first[0])
	}
	q.AfterUpdatedAt, q.AfterID = &first[1].UpdatedAt, first[1].ID
	read(q, 30, 58)
	c.Advance(time.Hour)
	address(30, "Current replacement response")
	read(q, 58) // The moved row is ahead of this current-view boundary.
	q.AfterUpdatedAt, q.AfterID = nil, ""
	read(q, 30, 0, 31)
	for _, action := range []struct {
		index  int
		action string
	}{{31, "reopen"}, {0, "withdraw"}, {58, "remove"}} {
		c.Advance(time.Second)
		changed, e := svc.Change(author, rows[action.index].ID, rows[action.index].Revision, action.action, feedback.EditInput{}, "")
		if e != nil || changed.Response != "" || action.action != "reopen" && changed.Comment != "" {
			t.Fatal("text clearing or lifecycle changed", changed, e)
		}
	}
	read(q, 30)
	// Omitting state keeps the current terminal/open cases in update order.
	// Continuation must use the same tuple even without the state predicate.
	all := feedback.ListInput{Order: feedback.OrderUpdatedDesc, Limit: 2}
	current := read(all, 58, 0, 31)
	if current[0].State != "removed" || current[1].State != "withdrawn" || current[2].State != "open" || current[0].Comment != "" || current[0].Response != "" || current[1].Comment != "" || current[1].Response != "" {
		t.Fatal("unfiltered recent page changed states or disclosed cleared text", current)
	}
	all.AfterUpdatedAt, all.AfterID = &current[1].UpdatedAt, current[1].ID
	read(all, 31, 30, 59)
	// Newer cases outside each selected filter outnumber the page sentinel.
	// Filtering after LIMIT would hide the eligible older reply entirely.
	otherResource, err := resources.CreateResource(author, catalog.CreateResourceInput{NamespaceID: ns.ID, CanonicalIdentifier: "urn:air:catalog.example.com:recent:other-feedback", Kind: catalog.KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	filtered := make([]feedback.Record, 4)
	for i := range filtered {
		input := feedback.CreateInput{ResourceID: resource.ID, Category: "positive", Comment: "Current feedback outside the selected category"}
		if i%2 == 1 {
			input.ResourceID, input.Category, input.Comment = otherResource.ID, "problem", "Current feedback outside the selected Resource"
		}
		c.Advance(time.Second)
		filtered[i], err = svc.Create(author, input)
		if err != nil {
			t.Fatal(err)
		}
		filtered[i], err = svc.Change(author, filtered[i].ID, filtered[i].Revision, "address", feedback.EditInput{}, "Current reply outside the selected filter")
		if err != nil {
			t.Fatal(err)
		}
	}
	read(q, 30)
	// Another human's newer reply never enters the owner's page. Removing its
	// Resource-read grant leaves the owner's own-feedback permission independent.
	principal, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.performance.example", TenancyMode: "dedicated", Subject: "recent-feedback-reader", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}, Operator: "recent-feedback-test", Reason: "Qualify independent feedback ownership", AcknowledgeOutbox: true})
	if err != nil {
		t.Fatal(err)
	}
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: principal.Subject, MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: principal.ActorType, Roles: []identity.Role{identity.RoleReader}})
	otherCase, err := svc.Create(other, feedback.CreateInput{ResourceID: resource.ID, Category: "problem", Comment: "Other member's private comment"})
	if err != nil {
		t.Fatal(err)
	}
	c.Advance(time.Hour)
	otherCase, err = svc.Change(author, otherCase.ID, otherCase.Revision, "address", feedback.EditInput{}, "Other member's private reply")
	if err != nil {
		t.Fatal(err)
	}
	identityStore, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: identityStore, Clock: c, IDs: id.Generator{}, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	revoked := false
	for _, grant := range principal.Assignments {
		if grant.Role == identity.RoleReader {
			if _, err = authority.RevokeRole(author, grant.ID, "Remove only Resource-read authority", "recent-reader-revoke", grant.Revision); err != nil {
				t.Fatal(err)
			}
			revoked = true
		}
	}
	if !revoked {
		t.Fatal("reader grant fixture missing")
	}
	if _, err = resources.GetResource(other, resource.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("resource-read control", err)
	}
	otherPage, err := svc.List(other, q, false)
	if err != nil || len(otherPage) != 1 || otherPage[0].ID != otherCase.ID {
		t.Fatal("own feedback incorrectly required Resource-read authority", otherPage, err)
	}
	read(q, 30)
	foreign := semanticDisclosureActor(t, db, housekeepingTenantB)
	foreignNS, err := resources.CreateNamespace(foreign, catalog.CreateNamespaceInput{Slug: "foreign-recent", DisplayName: "Foreign recent"})
	if err != nil {
		t.Fatal(err)
	}
	foreignResource, err := resources.CreateResource(foreign, catalog.CreateResourceInput{NamespaceID: foreignNS.ID, CanonicalIdentifier: "urn:air:catalog.example.com:foreign-recent:feedback", Kind: catalog.KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	foreignCase, err := svc.Create(foreign, feedback.CreateInput{ResourceID: foreignResource.ID, Category: "problem", Comment: "Foreign private case"})
	if err != nil {
		t.Fatal(err)
	}
	foreignCase, err = svc.Change(foreign, foreignCase.ID, foreignCase.Revision, "address", feedback.EditInput{}, "Foreign private reply")
	if err != nil {
		t.Fatal(err)
	}
	foreignQ := feedback.ListInput{Order: feedback.OrderUpdatedDesc, State: "addressed", Limit: 2}
	foreignPage, err := svc.List(foreign, foreignQ, false)
	if err != nil || len(foreignPage) != 1 || foreignPage[0].ID != foreignCase.ID {
		t.Fatal("foreign positive control", foreignPage, err)
	}
	ownerPage, err := svc.List(author, foreignQ, false)
	if err != nil || len(ownerPage) != 3 || ownerPage[0].ID != filtered[3].ID || ownerPage[1].ID != filtered[2].ID || ownerPage[2].ID != filtered[1].ID {
		t.Fatal("unfiltered own selection included another member or tenant", ownerPage, err)
	}
	read(q, 30)
	// Bulk synthetic rows are only query-plan inputs. The assertions above use
	// real domain commands; this fixture does not pretend to audit bulk seeding.
	stamp := c.Now()
	if _, err = db.operator.Exec(t.Context(), `INSERT INTO item_feedback(id,tenant_id,membership_id,resource_id,category,comment,response,state,revision,created_at,updated_at)
SELECT ('10000000-0000-7000-8000-'||lpad(i::text,12,'0'))::uuid,$1,$2,$3,'problem','Synthetic plan fixture','','open',1,$4,$4 FROM generate_series(1,5000) i`, housekeepingTenantA, a.MembershipID, resource.ID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = db.operator.Exec(t.Context(), `UPDATE item_feedback SET state='addressed',revision=2,updated_at=$3 WHERE tenant_id=$1 AND membership_id=$2 AND id::text LIKE '10000000-%'`, housekeepingTenantA, a.MembershipID, stamp.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.operator.Exec(t.Context(), `ANALYZE item_feedback`); err != nil {
		t.Fatal(err)
	}
	planQ := feedback.ListInput{MembershipID: a.MembershipID, Order: feedback.OrderUpdatedDesc, State: "addressed", Limit: 21}
	statement, args := feedbackListStatement(housekeepingTenantA, planQ)
	_, err = withTenantRead(identity.WithPermissionContext(author), db.api, housekeepingTenantA, func(tx pgx.Tx) (struct{}, error) {
		var raw json.RawMessage
		if e := tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE,BUFFERS,TIMING OFF,FORMAT JSON) "+statement, append([]any{pgx.QueryExecModeExec}, args...)...).Scan(&raw); e != nil {
			return struct{}{}, e
		}
		if !strings.Contains(string(raw), "item_feedback_member_state_updated_page") || strings.Contains(string(raw), `"Node Type": "Sort"`) {
			t.Fatalf("recent page lacks its ordered index: %s", raw)
		}
		t.Logf("API-role recent-page plan over 5000 synthetic rows: %s", raw)
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = resources.DeleteResource(author, resource.ID, "Qualify deleted-parent exclusion", resource.Revision); err != nil {
		t.Fatal(err)
	}
	page, err := svc.List(author, q, false)
	if err != nil || len(page) != 0 {
		t.Fatal("deleted Resource disclosed feedback", len(page), err)
	}
	page, err = svc.List(author, foreignQ, false)
	if err != nil || len(page) != 2 || page[0].ID != filtered[3].ID || page[1].ID != filtered[1].ID {
		t.Fatal("deleted-parent exclusion hid live Resource feedback or included deleted/foreign cases", page, err)
	}
}
