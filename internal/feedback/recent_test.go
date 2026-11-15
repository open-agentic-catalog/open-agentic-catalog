// Current-view units use static retained rows and injected permission/context ports.
package feedback

import (
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestRecentOwnFeedbackUsesCurrentTupleBeforeLimit(t *testing.T) {
	t.Parallel()
	service, store, _, ctx := fixture(t)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	key := func(i int) string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", i) }
	for i := 100; i < 140; i++ {
		r := Record{ID: key(i), TenantID: testTenant, MembershipID: testMember, ResourceID: testResource, Category: "problem", Comment: "Private current comment", State: "open", Revision: 1, CreatedAt: at.Add(-48 * time.Hour), UpdatedAt: at.Add(-time.Hour)}
		switch i {
		case 129:
			r.State, r.Category, r.UpdatedAt = "addressed", "positive", at.Add(6*time.Hour)
		case 130:
			r.State, r.Response, r.UpdatedAt = "addressed", "New reply to an older case", at.Add(2*time.Hour)
		case 138, 139:
			r.State, r.Response, r.UpdatedAt = "addressed", "Tied current reply", at.Add(time.Hour)
		case 135:
			r.State, r.UpdatedAt = "addressed", at.Add(30*time.Minute)
		case 110:
			r.State, r.UpdatedAt = "addressed", at
		case 136:
			r.State, r.ResourceID, r.UpdatedAt = "addressed", testRelease, at.Add(4*time.Hour)
		case 137:
			r.State, r.MembershipID, r.UpdatedAt = "addressed", testRealm, at.Add(5*time.Hour)
		}
		store.records[testTenant+"/"+r.ID] = r
	}
	read := func(q ListInput, want ...int) []Record {
		t.Helper()
		rows, err := service.List(ctx, q, false)
		if err != nil || len(rows) != len(want) {
			t.Fatalf("page %+v: %+v %v", q, rows, err)
		}
		for i, id := range want {
			if rows[i].ID != key(id) {
				t.Fatalf("row %d=%s want %s", i, rows[i].ID, key(id))
			}
		}
		return rows
	}
	read(ListInput{Limit: 2}, 100, 101, 102)
	q := ListInput{Order: OrderUpdatedDesc, State: "addressed", Category: "problem", MembershipID: testRealm, Limit: 2}
	first := read(q, 130, 139, 138)
	q.AfterID, q.AfterUpdatedAt = first[1].ID, &first[1].UpdatedAt
	read(q, 138, 135, 110)
	// An unseen current row can move ahead of the cursor. Only a fresh first
	// page promises to include its new position; traversal is not a snapshot.
	r := store.records[testTenant+"/"+key(138)]
	r.Revision++
	r.UpdatedAt = at.Add(3 * time.Hour)
	store.records[testTenant+"/"+r.ID] = r
	read(q, 135, 110)
	q.AfterID, q.AfterUpdatedAt = "", nil
	read(q, 138, 130, 139)
	q.AfterID, q.AfterUpdatedAt = key(110), &at
	read(q)
}

func TestRecentFeedbackRejectsInvalidOrderAndPosition(t *testing.T) {
	t.Parallel()
	s, _, _, ctx := fixture(t)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	zero := time.Time{}
	for _, q := range []ListInput{
		{Order: "random", Limit: 20},
		{Order: OrderUpdatedDesc, AfterID: testRelease, Limit: 20},
		{Order: OrderUpdatedDesc, AfterUpdatedAt: &at, Limit: 20},
		{Order: OrderUpdatedDesc, AfterUpdatedAt: &zero, AfterID: testRelease, Limit: 20},
		{Order: OrderUpdatedDesc, AfterUpdatedAt: &at, AfterID: "bad", Limit: 20},
		{AfterUpdatedAt: &at, AfterID: testRelease, Limit: 20},
	} {
		_, err := s.List(ctx, q, false)
		requireCode(t, err, catalog.CodeInvalidArgument)
	}
	admin := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: testTenant, MembershipID: testMember, RealmID: testRealm, Subject: "moderator", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
	_, err := s.List(admin, ListInput{Order: OrderUpdatedDesc, Limit: 20}, true)
	requireCode(t, err, catalog.CodeInvalidArgument)
}
