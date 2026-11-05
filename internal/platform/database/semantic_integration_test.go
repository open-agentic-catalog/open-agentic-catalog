//go:build integration

// Optional vector storage is qualified with the same exact runtime identities
// as core persistence, in a test-owned schema and disposable PostgreSQL database.
package database

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// TestPostgresSemanticStorage checks explicit installation, exact role grants,
// tenant isolation, immutable batches and profile/lease/source generation fences.
func TestPostgresSemanticStorage(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := t.Context()
	if _, err := PlanSemanticSchema(ctx, db.operator); err == nil {
		t.Fatal("optional setup accepted missing pgvector")
	}
	var schema, apiRole, workerRole string
	for _, query := range []struct {
		pool interface {
			QueryRow(context.Context, string, ...any) pgx.Row
		}
		sql    string
		result *string
	}{
		{db.operator, "SELECT current_schema()", &schema}, {db.api, "SELECT current_user", &apiRole}, {db.worker, "SELECT current_user", &workerRole},
	} {
		if err := query.pool.QueryRow(ctx, query.sql).Scan(query.result); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.operator.Exec(ctx, `CREATE EXTENSION vector WITH SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Run("reject different extension version", func(t *testing.T) {
		// Change only this disposable fixture's extension metadata inside a
		// rolled-back transaction. The extension binary and stored schema stay
		// unchanged; this control checks the actual SQL version predicate.
		tx, err := db.operator.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackForCleanup(t.Context(), tx)()
		if _, err := tx.Exec(t.Context(), `UPDATE pg_catalog.pg_extension SET extversion='0.8.6' WHERE extname='vector'`); err != nil {
			t.Fatal(err)
		}
		if _, err := planSemanticSchema(t.Context(), tx); err == nil || !strings.Contains(err.Error(), "administrator-provisioned pgvector 0.8.7") {
			t.Fatalf("different extension version returned %v", err)
		}
	})
	var extensionVersion string
	if err := db.operator.QueryRow(ctx, `SELECT extversion FROM pg_catalog.pg_extension WHERE extname='vector'`).Scan(&extensionVersion); err != nil || extensionVersion != "0.8.7" {
		t.Fatalf("installed extension version = %q, %v", extensionVersion, err)
	}
	plan, err := PlanSemanticSchema(ctx, db.operator)
	if err != nil || plan.Applied || plan.ExtensionSchema != schema {
		t.Fatalf("unapplied plan = %+v, %v", plan, err)
	}
	for range 2 {
		applied, reports, err := ApplyAndReconcileRuntimePrivileges(ctx, db.operator, apiRole, workerRole, true)
		if err != nil || len(applied) != 0 || len(reports) != 2 || !reports[0].Compliant || !reports[1].Compliant {
			t.Fatalf("optional apply = %+v, %+v, %v", applied, reports, err)
		}
	}
	plan, err = PlanSemanticSchema(ctx, db.api)
	if err != nil || !plan.Applied {
		t.Fatalf("applied plan = %+v, %v", plan, err)
	}
	repo, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresSemanticStore(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	g := catalog.SemanticGeneration{ID: "00000000-0000-7000-8000-000000000901", TenantID: housekeepingTenantA, Profile: "sha256:" + strings.Repeat("a", 64), Scope: "control", SourceGeneration: "0", Dimensions: 3}
	job := housekeepingJob(g.ID, "", time.Now().UTC().Add(-time.Second))
	job.Kind = "semantic.reindex"
	job.Payload, err = json.Marshal(map[string]string{"profile": g.Profile, "scope": g.Scope})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.apiJobs.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := db.workerJobs.Claim(ctx, "disabled", time.Now().UTC(), time.Minute); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("disabled worker claimed semantic work: %v", err)
	}
	otherJobs, err := NewPostgresJobStore(repo, "sha256:"+strings.Repeat("f", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherJobs.Claim(ctx, "different-profile", time.Now().UTC(), time.Minute); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("incompatible worker claimed semantic work: %v", err)
	}
	semanticJobs, err := NewPostgresJobStore(repo, g.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := semanticJobs.Claim(ctx, "semantic-test", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin(ctx, g, "wrong-owner"); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("wrong lease = %v", err)
	}
	for range 2 {
		if err := store.Begin(ctx, g, "semantic-test"); err != nil {
			t.Fatal(err)
		}
	}
	member := catalog.SemanticMember{Key: "local:example", InputDigest: "sha256:" + strings.Repeat("b", 64), Vector: []float32{1, 0, 0}}
	for range 2 {
		if err := store.SaveBatch(ctx, g, "semantic-test", []catalog.SemanticMember{member}); err != nil {
			t.Fatal(err)
		}
	}
	changed := member
	changed.InputDigest = "sha256:" + strings.Repeat("c", 64)
	if err := store.SaveBatch(ctx, g, "semantic-test", []catalog.SemanticMember{changed}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("rewrite = %v", err)
	}
	var count int
	if err := db.worker.QueryRow(ctx, "SELECT count(*) FROM semantic_vectors WHERE tenant_id=$1", g.TenantID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rolled-back conflicting vector count = %d, %v", count, err)
	}
	for _, tenant := range []string{housekeepingTenantA, housekeepingTenantB} {
		_, err := withTenantTx(ctx, db.api, tenant, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
			for _, table := range []string{"semantic_generations", "semantic_vectors", "semantic_members"} {
				if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE tenant_id=$1", g.TenantID).Scan(&count); err != nil {
					return struct{}{}, err
				}
				want := 0
				if tenant == housekeepingTenantA {
					want = 1
				}
				if count != want {
					t.Fatalf("%s tenant %s count = %d, want %d", table, tenant, count, want)
				}
			}
			return struct{}{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.api.Exec(ctx, "DELETE FROM semantic_vectors WHERE tenant_id=$1", g.TenantID); err == nil {
		t.Fatal("API can delete derived data")
	}
	if _, err := db.worker.Exec(ctx, "UPDATE semantic_vectors SET embedding=embedding WHERE tenant_id=$1", g.TenantID); err == nil {
		t.Fatal("worker can rewrite vectors")
	}
	if _, err := db.worker.Exec(ctx, `INSERT INTO semantic_members(tenant_id,generation_id,item_key,profile,dimensions,disclosure,input_digest,failure) VALUES($1,$2,'cross-tenant',$3,3,'control',$4,'')`, housekeepingTenantB, g.ID, g.Profile, member.InputDigest); err == nil {
		t.Fatal("cross-tenant parent accepted")
	}
	wrong := g
	wrong.Profile = "sha256:" + strings.Repeat("d", 64)
	if err := store.SaveBatch(ctx, wrong, "semantic-test", []catalog.SemanticMember{member}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("wrong profile = %v", err)
	}
	wrong = g
	wrong.TenantID = housekeepingTenantB
	if err := store.SaveBatch(ctx, wrong, "semantic-test", []catalog.SemanticMember{member}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("wrong tenant = %v", err)
	}
	if err := store.SaveBatch(ctx, g, "semantic-test", []catalog.SemanticMember{{Key: "rejected", Failure: "input_rejected"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, g, "semantic-test", 3); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("incomplete generation = %v", err)
	}
	if err := store.Complete(ctx, g, "semantic-test", 2); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, g, "semantic-test", 2); err != nil {
		t.Fatalf("completion replay = %v", err)
	}
	apiRepo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	queryStore, err := NewPostgresSemanticStore(ctx, apiRepo)
	if err != nil {
		t.Fatal(err)
	}
	scores, err := queryStore.Similarities(ctx, g, []float32{0.6, 0.8, 0}, []string{member.Key, "missing", "rejected"})
	if err != nil || len(scores) != 1 || math.Abs(scores[member.Key]-.6) > 0.000001 {
		t.Fatalf("exact-role cosine = %v, %v", scores, err)
	}
	if _, err := queryStore.Similarities(ctx, wrong, member.Vector, []string{member.Key}); err == nil {
		t.Fatal("cross-tenant similarity returned")
	}
	if err := store.SaveBatch(ctx, g, "semantic-test", []catalog.SemanticMember{member}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("write after completion = %v", err)
	}
	if _, err := db.worker.Exec(ctx, `UPDATE semantic_generations SET state='building',updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, g.TenantID, g.ID); err == nil {
		t.Fatal("ready generation reverted")
	}
	// Use explicit Job times, never a changed host/database clock or a sleep.
	expired := g
	expired.ID = "00000000-0000-7000-8000-000000000902"
	expiredJob := housekeepingJob(expired.ID, "", time.Now().UTC().Add(-3*time.Minute))
	expiredJob.Kind = job.Kind
	expiredJob.Payload = job.Payload
	if _, _, err := db.apiJobs.Enqueue(ctx, expiredJob); err != nil {
		t.Fatal(err)
	}
	if _, err := semanticJobs.Claim(ctx, "expired", time.Now().UTC().Add(-2*time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin(ctx, expired, "expired"); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("expired lease = %v", err)
	}
	if _, err := semanticJobs.Claim(ctx, "replacement", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin(ctx, expired, "replacement"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBatch(ctx, expired, "expired", []catalog.SemanticMember{member}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("replaced lease = %v", err)
	}
	if _, err := db.apiJobs.RequestCancellation(ctx, expired.TenantID, expired.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBatch(ctx, expired, "replacement", []catalog.SemanticMember{member}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("cancelled indexing = %v", err)
	}
	if _, err := db.operator.Exec(ctx, `INSERT INTO catalog_search_generations(tenant_id,generation) VALUES($1,1) ON CONFLICT(tenant_id) DO UPDATE SET generation=catalog_search_generations.generation+1`, g.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := queryStore.Similarities(ctx, g, member.Vector, []string{member.Key}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("stale source similarity = %v", err)
	}
	public := g
	public.ID = "00000000-0000-7000-8000-000000000903"
	public.Scope = "public:" + public.ID
	publicJob := housekeepingJob(public.ID, "", time.Now().UTC().Add(-time.Second))
	publicJob.Kind = job.Kind
	publicJob.Payload, err = json.Marshal(map[string]string{"profile": public.Profile, "scope": public.Scope})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.apiJobs.Enqueue(ctx, publicJob); err != nil {
		t.Fatal(err)
	}
	if _, err := semanticJobs.Claim(ctx, "public", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin(ctx, public, "public"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.worker.Exec(ctx, `INSERT INTO semantic_members(tenant_id,generation_id,item_key,profile,dimensions,disclosure,input_digest,failure) VALUES($1,$2,'public-item',$3,3,'public',$4,'')`, public.TenantID, public.ID, public.Profile, member.InputDigest); err == nil {
		t.Fatal("private vector reused in public generation")
	}
	if err := store.SaveBatch(ctx, public, "public", []catalog.SemanticMember{member}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, public, "public", 1); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing public snapshot activated = %v", err)
	}
	if _, err := db.operator.Exec(ctx, `ALTER TABLE semantic_vectors NO FORCE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanSemanticSchema(ctx, db.api); err == nil {
		t.Fatal("missing forced RLS accepted")
	}
	if _, err := db.operator.Exec(ctx, `ALTER TABLE semantic_vectors FORCE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if _, err := semanticJobs.Complete(ctx, g.TenantID, g.ID, "semantic-test", time.Now().UTC(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	unrelated := housekeepingJob("00000000-0000-7000-8000-000000000904", "", time.Now().UTC())
	if _, _, err := db.apiJobs.Enqueue(ctx, unrelated); err != nil {
		t.Fatal(err)
	}
	if _, err := db.apiJobs.RequestCancellation(ctx, unrelated.TenantID, unrelated.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.housekeeper.ReclaimTerminalJobs(ctx, 10*time.Millisecond, 10); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("unrelated terminal Job was not reclaimed")
		case <-tick.C:
			result, err := db.housekeeper.ReclaimTerminalJobs(ctx, 10*time.Millisecond, 10)
			if err != nil {
				t.Fatal(err)
			}
			if result.Reclaimed == 0 {
				continue
			}
			if result.Reclaimed != 1 {
				t.Fatalf("reclaimed = %d", result.Reclaimed)
			}
		}
		break
	}
	if err := db.worker.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE tenant_id=$1 AND id=$2`, g.TenantID, g.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("generation Job was removed: %d, %v", count, err)
	}
	// A checksum change is not repaired by silently overwriting stored evidence.
	if _, err := db.operator.Exec(ctx, `UPDATE catalog_semantic_schema SET checksum=repeat('0',64)`); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanSemanticSchema(ctx, db.operator); err == nil {
		t.Fatal("checksum drift accepted")
	}
	t.Run("literal quoting is independent of session string settings", func(t *testing.T) {
		tx, err := db.operator.BeginTx(t.Context(), pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackForCleanup(t.Context(), tx)()
		name := `semantic\'$semantic_grant$`
		qualified := pgx.Identifier{"pg_temp", name}.Sanitize()
		for _, statement := range []string{
			`SET LOCAL standard_conforming_strings=off`,
			`CREATE TEMP TABLE ` + pgx.Identifier{name}.Sanitize() + `(value integer)`,
			`INSERT INTO ` + qualified + ` VALUES(1)`,
			optionalSemanticGrant(`UPDATE `+qualified+` SET value=2`, qualified),
		} {
			if _, err := tx.Exec(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
		}
		var value int
		if err := tx.QueryRow(t.Context(), `SELECT value FROM `+qualified).Scan(&value); err != nil || value != 2 {
			t.Fatalf("quoted statement = %d, %v", value, err)
		}
	})
}
