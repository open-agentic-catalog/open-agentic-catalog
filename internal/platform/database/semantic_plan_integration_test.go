//go:build integration

// The enabled fixture collects bounded, read-only plans after its HTTP load.
// It reuses the actual hybrid statement and never outputs SQL or metadata.
package database

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
)

// TestPostgresSemanticPlans accepts only its supervisor's owned loopback port.
// Default integration runs skip it; no retained or caller-selected DSN is used.
func TestPostgresSemanticPlans(t *testing.T) {
	raw := os.Getenv("CATALOG_SEMANTIC_PLAN_PORT")
	if raw == "" {
		t.Skip("owned semantic runner only")
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != raw {
		t.Fatal("invalid owned semantic port")
	}
	cfg, err := pgxpool.ParseConfig(fmt.Sprintf("postgres://catalog_api:catalog-api@127.0.0.1:%d/catalog?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresSemanticStore(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewPostgresIdentityStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := authority.ResolvePrincipal(t.Context(), identity.Principal{TenantID: "00000000-0000-7000-8000-000000000001", Issuer: authn.DevelopmentIssuer, Subject: "local-admin"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, _, err := identity.WithCollectionContext(identity.WithActor(t.Context(), actor))
	if err != nil {
		t.Fatal(err)
	}
	type sample struct {
		Mode       string         `json:"mode"`
		JITEnabled bool           `json:"jitEnabled"`
		Execution  float64        `json:"executionMillis"`
		Planning   float64        `json:"planningMillis"`
		JIT        float64        `json:"jitMillis"`
		Plan       searchPlanNode `json:"plan"`
	}
	results := make([]sample, 0, 4)
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		for _, jit := range []bool{true, false} {
			value := sample{Mode: mode, JITEnabled: jit}
			_, err := withTenantCollectionRead(ctx, pool, actor.TenantID, func(tx pgx.Tx) (bool, error) {
				var generation, profile string
				if err := tx.QueryRow(t.Context(), "SELECT id::text,profile FROM semantic_generations WHERE tenant_id=$1 AND scope='control' AND dimensions=384 AND state='ready' ORDER BY created_at DESC LIMIT 1", "00000000-0000-7000-8000-000000000001").Scan(&generation, &profile); err != nil {
					return false, err
				}
				if _, err := tx.Exec(t.Context(), "SET LOCAL plan_cache_mode="+mode); err != nil {
					return false, err
				}
				if !jit {
					if _, err := tx.Exec(t.Context(), "SET LOCAL jit=off"); err != nil {
						return false, err
					}
				}
				statement := strings.NewReplacer("@cosine@", pgx.Identifier{store.vectorSchema, "cosine_distance"}.Sanitize(), "@vector@", pgx.Identifier{store.vectorSchema, "vector"}.Sanitize()).Replace(semanticSearchInputsSQL + semanticSearchRanksSQL)
				const name = "semantic_plan_diagnostic"
				if _, err := tx.Conn().Prepare(t.Context(), name, statement); err != nil {
					return false, err
				}
				vector := "1" + strings.Repeat(",0", 383)
				// All literals are fixed or quoted database-owned identifiers, never
				// request text. Empty license/category inputs preserve the same
				// 997 exact tagged candidates in this diagnostic population.
				args := "'00000000-0000-7000-8000-000000000001','Scale','',NULL,ARRAY['scale'],'',''," + semanticSQLLiteral(generation) + "," + semanticSQLLiteral(profile) + ",ARRAY[" + vector + "]::real[],0.2,false,false,0,'','',21,'',true,true,'',NULL"
				var raw []byte
				if err := tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) EXECUTE "+name+"("+args+")").Scan(&raw); err != nil {
					return false, err
				}
				if err := tx.Conn().Deallocate(t.Context(), name); err != nil {
					return false, err
				}
				var plans []struct {
					Execution float64        `json:"Execution Time"`
					Planning  float64        `json:"Planning Time"`
					Plan      searchPlanNode `json:"Plan"`
					JIT       struct {
						Timing struct {
							Total float64 `json:"Total"`
						} `json:"Timing"`
					} `json:"JIT"`
				}
				if json.Unmarshal(raw, &plans) != nil || len(plans) != 1 {
					return false, fmt.Errorf("invalid semantic plan")
				}
				if plans[0].Plan.Rows != 21 {
					return false, fmt.Errorf("semantic plan lost its authorized page population")
				}
				value.Execution, value.Planning, value.JIT, value.Plan = plans[0].Execution, plans[0].Planning, plans[0].JIT.Timing.Total, plans[0].Plan
				return true, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			results = append(results, value)
		}
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("semantic-plans: %s\n", encoded)
}
