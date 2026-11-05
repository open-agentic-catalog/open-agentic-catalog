//go:build integration

package database

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// Current implementation baseline, not a capacity or candidate-performance gate.
// Seed through the existing schema-owner fixture; measure through the API role.
func TestPostgresAuthorizationBaseline(t *testing.T) {
	db, repository, counts, seed := newSearchCostFixture(t)
	provisioned, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{
		TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.performance.example", TenancyMode: "dedicated", Subject: "authorization-baseline", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}, Operator: "authorization-baseline", Reason: "seed owned authorization baseline", AcknowledgeOutbox: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdentityStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	principal := identity.Principal{Issuer: "https://identity.performance.example", Subject: "authorization-baseline", TenantID: housekeepingTenantA}
	previous := 0
	statementCount := 0
	compactBytes, compactStatements := 0, 0
	for _, size := range []int{0, 100, 1000} {
		if size > previous {
			tx, err := db.operator.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for index, sql := range []string{
				`INSERT INTO namespaces(id,tenant_id,slug,display_name,created_at,updated_at,revision) SELECT md5('rbac-baseline-'||n::text)::uuid,$1,'rbac-'||n::text,'Authorization baseline',clock_timestamp(),clock_timestamp(),1 FROM generate_series($2::int,$3::int) n`,
				`INSERT INTO namespace_memberships(id,tenant_id,membership_id,namespace_id,state,revision,created_by,created_at) SELECT uuidv7(),$1,$4,md5('rbac-baseline-'||n::text)::uuid,'active',1,'baseline',clock_timestamp() FROM generate_series($2::int,$3::int) n`,
				`INSERT INTO role_assignments(id,tenant_id,membership_id,namespace_id,role,state,revision,granted_by,grant_reason,granted_at) SELECT uuidv7(),$1,$4,md5('rbac-baseline-'||n::text)::uuid,'contributor','active',1,'baseline','owned grant hydration measurement',clock_timestamp() FROM generate_series($2::int,$3::int) n`,
			} {
				args := []any{housekeepingTenantA, previous + 1, size}
				if index > 0 {
					args = append(args, provisioned.MembershipID)
				}
				if _, err := tx.Exec(t.Context(), sql, args...); err != nil {
					_ = tx.Rollback(t.Context())
					t.Fatal(err)
				}
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		// Warm the existing prepared statements before measuring five matched calls.
		if _, err := store.Resolve(t.Context(), principal); err != nil {
			t.Fatal(err)
		}
		counts.reset()
		elapsed := time.Duration(0)
		var actor identity.Actor
		for range 5 {
			start := time.Now()
			actor, err = store.Resolve(t.Context(), principal)
			elapsed += time.Since(start)
			if err != nil || len(actor.Namespaces) != size {
				t.Fatalf("grant hydration size=%d actual=%d err=%v", size, len(actor.Namespaces), err)
			}
		}
		raw, err := json.Marshal(actor)
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for _, n := range counts.counts {
			total += n
		}
		if size == 0 {
			statementCount = total
		} else if total != statementCount {
			t.Fatalf("resolution statement growth: %d versus %d", total, statementCount)
		}
		t.Logf("current resolution grants=%d mean=%s statements/call=%d actorJSONBytes=%d classes=%v", size, elapsed/5, total/5, len(raw), counts.counts)
		// The maintained historical hydration control and current authentication
		// share this fixture/cardinality. Runtime authentication uses only the
		// latter; neither timings nor legacy role checks establish current authority.
		if _, err := store.ResolvePrincipal(t.Context(), principal); err != nil {
			t.Fatal(err)
		}
		counts.reset()
		startCompact := time.Now()
		for range 5 {
			compact, resolveErr := store.ResolvePrincipal(t.Context(), principal)
			encoded, encodeErr := json.Marshal(compact)
			if resolveErr != nil || encodeErr != nil || len(compact.Roles) != 0 || compact.Namespaces != nil {
				t.Fatal("current principal hydration or resolution failure")
			}
			if size == 0 {
				compactBytes = len(encoded)
			} else if len(encoded) != compactBytes {
				t.Fatal("current principal grows with retained grant count")
			}
		}
		compactElapsed := time.Since(startCompact)
		compactTotal := 0
		for _, count := range counts.counts {
			compactTotal += count
		}
		if size == 0 {
			compactStatements = compactTotal
		} else if compactTotal != compactStatements {
			t.Fatal("current principal query count grows with retained grants")
		}
		t.Logf("compact resolution grants=%d mean=%s statements/call=%d actorJSONBytes=%d classes=%v", size, compactElapsed/5, compactTotal/5, compactBytes, counts.counts)
		if size > 0 {
			var namespaceID string
			if err := db.operator.QueryRow(t.Context(), `SELECT md5('rbac-baseline-1')::uuid::text`).Scan(&namespaceID); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			for range 10000 {
				if !actor.HasRole(identity.RoleContributor, namespaceID) {
					t.Fatal("current scoped role lost")
				}
			}
			t.Logf("current in-memory exact role check grants=%d mean=%s; excludes resolution", size, time.Since(start)/10000)
			tx := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{AccessMode: pgx.ReadOnly})
			var allowed bool
			exactSQL := `SELECT EXISTS(SELECT 1 FROM tenant_memberships m JOIN tenant_identity_realms r ON r.tenant_id=m.tenant_id AND r.id=m.realm_id AND r.state='active' JOIN role_assignments a ON a.tenant_id=m.tenant_id AND a.membership_id=m.id AND a.state='active' LEFT JOIN namespace_memberships nm ON nm.tenant_id=a.tenant_id AND nm.membership_id=a.membership_id AND nm.namespace_id=a.namespace_id AND nm.state='active' WHERE m.tenant_id=$1 AND m.id=$2 AND m.state='active' AND a.role='contributor' AND a.namespace_id=$3 AND nm.id IS NOT NULL)`
			start = time.Now()
			for range 5 {
				if err := tx.QueryRow(t.Context(), exactSQL, housekeepingTenantA, provisioned.MembershipID, namespaceID).Scan(&allowed); err != nil || !allowed {
					t.Fatalf("exact SQL control=%v err=%v", allowed, err)
				}
			}
			t.Logf("candidate direct exact EXISTS grants=%d mean=%s; excludes transaction setup and group/custom role cost", size, time.Since(start)/5)
			_ = tx.Rollback(t.Context())
		}
		previous = size
	}
	for n := 0; n < 20; n++ {
		seed(n)
	}
	counts.reset()
	start := time.Now()
	snapshot, err := repository.SearchCatalogSnapshot(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: catalog.SearchQuery{Text: "Performance"}, Limit: 10}, catalog.FederatedCatalogSearchRequest{Limit: 10})
	if err != nil || snapshot.Local.Total != 20 || len(snapshot.Local.Items) != 10 || snapshot.Indexed.IndexedTotal != 0 {
		t.Fatalf("baseline snapshot %+v %v", snapshot, err)
	}
	t.Logf("current global-reader search records=20 page=10 elapsed=%s statementClasses=%v", time.Since(start), counts.counts)
	if counts.counts["begin"] != 1 || counts.counts["commit"] != 1 {
		t.Fatalf("search lost single snapshot: %v", counts.counts)
	}
}
