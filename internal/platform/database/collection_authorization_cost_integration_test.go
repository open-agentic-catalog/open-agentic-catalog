//go:build integration

package database

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// The global branch must not decode the same policy JSON for every candidate.
// Generated candidates exercise the real SQL expression and exact API role
// without fabricating governed Resources or weakening their constraints.
func TestPostgresCollectionGlobalDecisionRunsOncePerStatement(t *testing.T) {
	db, repo, _, _ := newSearchCostFixture(t)
	ctx, _, err := identity.WithCollectionContext(semanticDisclosureActor(t, db, housekeepingTenantA))
	if err != nil {
		t.Fatal(err)
	}
	_, err = withTenantCollectionRead(ctx, repo.pool, housekeepingTenantA, func(tx pgx.Tx) (bool, error) {
		var raw []byte
		namespace := `CASE WHEN candidate.n%2=0 THEN $2::uuid ELSE $3::uuid END`
		query := `EXPLAIN (ANALYZE,BUFFERS,VERBOSE,TIMING OFF,FORMAT JSON) SELECT candidate.n FROM generate_series(1,90000) candidate(n) WHERE ` + collectionPermissionSQL("resource.read", namespace)
		if err := tx.QueryRow(ctx, query, housekeepingTenantA, housekeepingTenantA, housekeepingTenantB).Scan(&raw); err != nil {
			return false, err
		}
		var plans []map[string]any
		if err := json.Unmarshal(raw, &plans); err != nil {
			return false, err
		}
		if len(plans) != 1 {
			t.Fatal("unexpected EXPLAIN envelope")
		}
		root, ok := plans[0]["Plan"].(map[string]any)
		if !ok {
			t.Fatal("malformed EXPLAIN root")
		}
		if root["Actual Rows"] != float64(90000) {
			t.Fatal("global permission changed the independently expected candidate count")
		}
		decisions := 0
		var inspect func(map[string]any)
		inspect = func(node map[string]any) {
			if node["Parent Relationship"] == "InitPlan" {
				output, err := json.Marshal(node["Output"])
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(output), "app.collection_permissions") {
					if node["Actual Loops"] != float64(1) {
						t.Fatal("global policy evaluation was repeated or omitted")
					}
					decisions++
				}
			}
			if children, ok := node["Plans"].([]any); ok {
				for _, child := range children {
					plan, ok := child.(map[string]any)
					if !ok {
						t.Fatal("malformed EXPLAIN child")
					}
					inspect(plan)
				}
			}
		}
		inspect(root)
		t.Logf("global collection candidates=90000 policy_initplans=%d execution_ms=%v", decisions, plans[0]["Execution Time"])
		if decisions != 1 {
			t.Fatal("global policy must use one statement-local evaluation")
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
