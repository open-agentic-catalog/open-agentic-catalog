//go:build integration

package database

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// Diagnostic matched samples, not timing gates or production capacity claims.
// Fixture setup/ANALYZE/warmup are excluded; queries use the exact API login.
func TestPostgresAuthorizationCurrentPolicyCost(t *testing.T) {
	db, repository, counts, seed := newSearchCostFixture(t)
	store, err := NewPostgresIdentityStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	const issuer = "https://identity.performance.example"
	resolve := func(subject string) identity.Actor {
		t.Helper()
		actor, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: issuer, Subject: subject})
		if err != nil {
			t.Fatal(err)
		}
		return actor
	}
	admin := resolve("search-author")
	testClock := clock.NewFake(time.Now().Add(time.Hour))
	administration, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: testClock, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	role, err := administration.SaveRoleDefinition(identity.WithActor(t.Context(), admin), "", 0, identity.RoleDefinitionInput{Name: "Cost reader", Permissions: []identity.Permission{"namespace.read", "resource.read"}, Reason: "Owned bounded current-policy costs"}, false, "cost-role")
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"cost-dense", "cost-sparse"} {
		if _, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: issuer, TenancyMode: "dedicated", Subject: subject, ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleServiceClient}, Operator: "cost", Reason: "Owned current-policy cost principal", AcknowledgeOutbox: true}); err != nil {
			t.Fatal(err)
		}
	}
	dense, sparse := resolve("cost-dense"), resolve("cost-sparse")
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO namespaces(id,tenant_id,slug,display_name,created_at,updated_at,revision) SELECT md5('cost-scope-'||n)::uuid,$1,'cost-'||lpad(n::text,4,'0'),'Cost scope',statement_timestamp(),statement_timestamp(),1 FROM generate_series(1,1000) n`, []any{housekeepingTenantA}},
		{`INSERT INTO access_groups(id,tenant_id,realm_id,name,state,revision,created_at,updated_at) SELECT md5('cost-group-'||n)::uuid,$1,$2,'Cost group '||n,'active',1,statement_timestamp(),statement_timestamp() FROM generate_series(1,100) n`, []any{housekeepingTenantA, dense.RealmID}},
		{`INSERT INTO access_group_members(id,tenant_id,realm_id,group_id,membership_id,state,revision,added_by,add_reason,added_at) SELECT uuidv7(),$1,$2,md5('cost-group-'||n)::uuid,$3,'active',1,'cost','Owned dense group population',statement_timestamp() FROM generate_series(1,100) n`, []any{housekeepingTenantA, dense.RealmID, dense.MembershipID}},
		{`INSERT INTO access_group_members(id,tenant_id,realm_id,group_id,membership_id,state,revision,added_by,add_reason,added_at) VALUES(uuidv7(),$1,$2,md5('cost-group-1')::uuid,$3,'active',1,'cost','Owned sparse group population',statement_timestamp())`, []any{housekeepingTenantA, sparse.RealmID, sparse.MembershipID}},
		{`INSERT INTO role_assignments(id,tenant_id,realm_id,role_definition_id,role,group_id,namespace_id,state,revision,granted_by,grant_reason,granted_at) SELECT uuidv7(),$1,$2,$3,'',md5('cost-group-'||((n-1)/10+1))::uuid,md5('cost-scope-'||n)::uuid,'active',1,'cost','Owned scoped group costs',statement_timestamp() FROM generate_series(1,1000) n`, []any{housekeepingTenantA, dense.RealmID, role.ID}},
	} {
		if _, err := db.operator.Exec(t.Context(), statement.sql, statement.args...); err != nil {
			t.Fatal("seed current-policy cost fixture", err)
		}
	}
	var first, other, groupID string
	if err := db.operator.QueryRow(t.Context(), `SELECT md5('cost-scope-1')::uuid::text,md5('cost-scope-11')::uuid::text,md5('cost-group-1')::uuid::text`).Scan(&first, &other, &groupID); err != nil {
		t.Fatal(err)
	}
	for n := range 20 {
		seed(n)
	}
	var searchNamespace string
	if err := db.operator.QueryRow(t.Context(), `SELECT id::text FROM namespaces WHERE tenant_id=$1 AND slug='performance'`, housekeepingTenantA).Scan(&searchNamespace); err != nil {
		t.Fatal(err)
	}
	if _, err := administration.GrantAuthorizationAssignment(identity.WithActor(t.Context(), admin), identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: groupID, NamespaceID: searchNamespace, Reason: "Measure governed Search with retained group cardinality"}, "cost-search"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(t.Context(), `ANALYZE namespaces; ANALYZE role_assignments; ANALYZE access_groups; ANALYZE access_group_members; ANALYZE role_permissions; ANALYZE listing_releases`); err != nil {
		t.Fatal(err)
	}
	catalogService, err := catalog.NewService(catalog.Options{Repository: repository, Clock: testClock, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte("01234567890123456789012345678901"), Policy: "cost"})
	if err != nil {
		t.Fatal(err)
	}
	measure := func(name string, operation func() error) {
		t.Helper()
		if err := operation(); err != nil {
			t.Fatal(name, err)
		}
		counts.reset()
		start := time.Now()
		for range 5 {
			if err := operation(); err != nil {
				t.Fatal(name, err)
			}
		}
		elapsed := time.Since(start)
		total := 0
		for _, n := range counts.counts {
			total += n
		}
		t.Logf("current-policy case=%s samples=5 mean=%s statements/call=%d classes=%v", name, elapsed/5, total/5, counts.counts)
	}
	for _, subject := range []string{"cost-dense", "cost-sparse"} {
		actor := resolve(subject)
		expected := []bool{true, subject == "cost-dense", false}
		checks := []identity.PermissionCheck{{Permission: "resource.read", NamespaceID: first}, {Permission: "resource.read", NamespaceID: other}, {Permission: "access.manage"}}
		measure(subject+"-resolve", func() error {
			value, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: issuer, Subject: subject})
			if len(value.Roles) != 0 || value.Namespaces != nil || value.MembershipID != actor.MembershipID {
				t.Fatal("compact resolver hydrated policy")
			}
			return err
		})
		measure(subject+"-checks", func() error {
			before := counts.counts["select"]
			decision, err := store.CheckPermissions(t.Context(), actor, checks)
			if err == nil && !reflect.DeepEqual(decision.Allowed, expected) {
				t.Fatal("dense/sparse scope decisions changed", decision)
			}
			if err == nil && counts.counts["select"]-before != 2 {
				t.Fatal("permission read must use tenant setup and one revision/decision statement")
			}
			return err
		})
		measure(subject+"-self", func() error {
			projection, err := identity.CurrentSelfAccess(identity.WithActor(t.Context(), actor), identity.SelfAccessRequest{Limit: 25})
			want := 25
			if subject == "cost-sparse" {
				want = 11
			}
			if err == nil && (len(projection.NamespaceGrants) != want || len(projection.Permissions) != 0 || len(projection.AvailablePermissions) != 2) {
				t.Fatal("self projection lost its bounds or scope", projection)
			}
			return err
		})
		measure(subject+"-directory", func() error {
			page, err := catalogService.ListNamespaces(identity.WithActor(t.Context(), actor), catalog.NamespaceListRequest{Limit: 25})
			want := 26
			if subject == "cost-sparse" {
				want = 11
			}
			if err == nil && len(page) != want {
				t.Fatal("directory did not select authorized limit+1", len(page))
			}
			return err
		})
		measure(subject+"-fence", func() error {
			callbacks := 0
			err := store.WithPermissionAdmission(t.Context(), actor, checks[:1], func(context.Context) error { callbacks++; return nil })
			if err == nil && callbacks != 1 {
				t.Fatal("admission lost callback")
			}
			return err
		})
		measure(subject+"-search", func() error {
			page, err := catalogService.Search(identity.WithActor(t.Context(), actor), catalog.SearchQuery{Text: "Performance", Limit: 10})
			if err == nil && (page.Total != 20 || len(page.Items) != 10 || page.PartialIndex) {
				t.Fatal("scoped Search changed population or page bounds", page)
			}
			return err
		})
	}
	// Writes churn through the actual audited administration path. Reads made
	// with previously resolved principals must see each committed revision.
	for n := range 6 {
		testClock.Advance(time.Second)
		state := identity.GroupSuspended
		if n%2 == 1 {
			state = identity.GroupActive
		}
		start := time.Now()
		changed, err := administration.SaveAccessGroup(identity.WithActor(t.Context(), admin), groupID, int64(n+1), identity.GroupInput{Reason: "Measure current group churn"}, state, fmt.Sprint("cost-churn-", n))
		if err != nil || changed.Revision != int64(n+2) {
			t.Fatal("audited group churn failed", err)
		}
		for _, actor := range []identity.Actor{dense, sparse} {
			decision, err := store.CheckPermissions(t.Context(), actor, []identity.PermissionCheck{{Permission: "resource.read", NamespaceID: first}})
			if err != nil || decision.Allowed[0] != (state == identity.GroupActive) {
				t.Fatal("previous principal retained stale group authority", err)
			}
		}
		t.Logf("current-policy churn=%s elapsed=%s", state, time.Since(start))
	}
}
