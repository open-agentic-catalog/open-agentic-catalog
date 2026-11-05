//go:build integration

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// TestPostgresWorkCancellationAndConnectionRecovery exercises the waits that a
// request deadline must interrupt against real PostgreSQL. Each case also
// proves the pool remains usable after pgx has canceled or discarded work.
func TestPostgresWorkCancellationAndConnectionRecovery(t *testing.T) {
	baseDSN := os.Getenv("CATALOG_TEST_DATABASE_DSN")
	if baseDSN == "" {
		t.Skip("CATALOG_TEST_DATABASE_DSN is not set")
	}
	ctx := t.Context()
	adminPool, err := Open(ctx, baseDSN, testPoolConfig(4))
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	schema := "catalog_work_bounds_" + suffix
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = adminPool.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
	}()
	pool, err := Open(ctx, withSearchPath(t, baseDSN, schema+",public"), testPoolConfig(4))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := applyMigrationsForTest(ctx, pool); err != nil {
		t.Fatal(err)
	}
	const (
		tenantID    = "00000000-0000-7000-8000-000000000201"
		otherTenant = "00000000-0000-7000-8000-000000000202"
	)
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,display_name) VALUES ($1,'Work-bound integration tenant'),($2,'Other work-bound tenant')`, tenantID, otherTenant); err != nil {
		t.Fatal(err)
	}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	assertUsable := func(label string) {
		t.Helper()
		var one int
		if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
			t.Fatalf("database pool was unusable after %s: value=%d error=%v", label, one, err)
		}
	}
	t.Run("connection acquisition", func(t *testing.T) {
		connections := make([]interface{ Release() }, 0, 4)
		defer func() {
			for _, connection := range connections {
				connection.Release()
			}
		}()
		for range 4 {
			connection, acquireErr := pool.Acquire(ctx)
			if acquireErr != nil {
				t.Fatal(acquireErr)
			}
			connections = append(connections, connection)
		}
		// Several independent requests wait behind the exhausted pool. Every
		// waiter must cancel without stealing or leaking a held connection.
		results := make(chan error, 4)
		for range 4 {
			go func() {
				waitContext, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
				_, readErr := repository.PublicationRecords(waitContext, tenantID, catalog.PublicationRecordRequest{Limit: 1})
				results <- readErr
			}()
		}
		for range 4 {
			select {
			case readErr := <-results:
				if !errors.Is(readErr, context.DeadlineExceeded) {
					t.Errorf("connection acquisition returned %v", readErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("pool waiter did not finish")
			}
		}
		if pool.Stat().AcquiredConns() != 4 {
			t.Fatal("canceled waiter changed held connections")
		}
		for _, connection := range connections {
			connection.Release()
		}
		connections = nil
		assertUsable("connection-acquisition cancellation")
		if _, err := repository.PublicationRecords(ctx, tenantID, catalog.PublicationRecordRequest{Limit: 1}); err != nil {
			t.Fatalf("repository did not recover after concurrent pool waits: %v", err)
		}
	})

	t.Run("advisory lock", func(t *testing.T) {
		const lockKey int64 = 880185172499
		holder, acquireErr := pool.Acquire(ctx)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		defer holder.Release()
		if _, lockErr := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); lockErr != nil {
			t.Fatal(lockErr)
		}
		waitContext, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		_, waitErr := withTenantTx(waitContext, pool, tenantID, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
			_, operationErr := tx.Exec(waitContext, `SELECT pg_advisory_xact_lock($1)`, lockKey)
			return struct{}{}, operationErr
		})
		cancel()
		if !errors.Is(waitErr, context.DeadlineExceeded) {
			t.Fatalf("advisory-lock wait returned %v", waitErr)
		}
		if _, unlockErr := holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lockKey); unlockErr != nil {
			t.Fatal(unlockErr)
		}
		assertUsable("advisory-lock cancellation")
	})

	t.Run("row lock", func(t *testing.T) {
		holder, acquireErr := pool.Acquire(ctx)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		defer holder.Release()
		tx, beginErr := holder.Begin(ctx)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		defer rollbackForCleanup(ctx, tx)()
		if _, lockErr := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`, tenantID); lockErr != nil {
			t.Fatal(lockErr)
		}
		waitContext, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		_, waitErr := withTenantTx(waitContext, pool, tenantID, pgx.TxOptions{}, func(waiting pgx.Tx) (struct{}, error) {
			_, operationErr := waiting.Exec(waitContext, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE`, tenantID)
			return struct{}{}, operationErr
		})
		cancel()
		if !errors.Is(waitErr, context.DeadlineExceeded) {
			t.Fatalf("row-lock wait returned %v", waitErr)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		assertUsable("row-lock cancellation")
	})

	t.Run("statement", func(t *testing.T) {
		waitContext, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		_, statementErr := withTenantRead(waitContext, pool, tenantID, func(tx pgx.Tx) (struct{}, error) {
			_, operationErr := tx.Exec(waitContext, `SELECT pg_sleep(5)`)
			return struct{}{}, operationErr
		})
		cancel()
		if !errors.Is(statementErr, context.DeadlineExceeded) {
			t.Fatalf("long statement returned %v", statementErr)
		}
		assertUsable("statement cancellation")
	})

	t.Run("commit", func(t *testing.T) {
		commitContext, cancel := context.WithCancel(ctx)
		_, commitErr := withTenantTx(commitContext, pool, tenantID, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
			if _, updateErr := tx.Exec(commitContext, `UPDATE tenants SET display_name='must roll back' WHERE id=$1`, tenantID); updateErr != nil {
				return struct{}{}, updateErr
			}
			cancel()
			return struct{}{}, nil
		})
		if commitErr == nil {
			t.Fatal("commit ignored caller cancellation")
		}
		var displayName string
		if err := pool.QueryRow(ctx, `SELECT display_name FROM tenants WHERE id=$1`, tenantID).Scan(&displayName); err != nil || displayName != "Work-bound integration tenant" {
			t.Fatalf("canceled commit retained display name %q: %v", displayName, err)
		}
		assertUsable("commit cancellation")
	})

	t.Run("expired command-lock cleanup", func(t *testing.T) {
		const lockKey int64 = 880185172500
		connection, acquireErr := pool.Acquire(ctx)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		defer connection.Release()
		if _, lockErr := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); lockErr != nil {
			t.Fatal(lockErr)
		}
		expired, cancel := context.WithCancel(ctx)
		cancel()
		if cleanupErr := releaseCommandLockWithin(expired, connection, lockKey); cleanupErr == nil || !connection.Conn().IsClosed() {
			t.Fatalf("expired cleanup returned %v, closed=%t", cleanupErr, connection.Conn().IsClosed())
		}
		connection.Release()
		assertUsable("forced command-connection discard")
	})

	t.Run("publication generation contention", func(t *testing.T) {
		publicationStore, storeErr := NewPostgresPublicationStore(repository, "generation.example.com")
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		start := make(chan struct{})
		ready := make(chan struct{}, 2)
		results := make(chan error, 2)
		for index := range 2 {
			go func(index int) {
				generation, generationErr := publicationStore.NextGeneration(ctx, tenantID, "generation.example.com")
				ready <- struct{}{}
				<-start
				if generationErr != nil {
					results <- generationErr
					return
				}
				snapshot := publication.Snapshot{
					ID:               fmt.Sprintf("00000000-0000-7000-8000-%012d", 205+index),
					TenantID:         tenantID,
					Host:             "generation.example.com",
					Audience:         publication.AudienceAnonymous,
					Generation:       generation,
					ARDProfile:       "ard-test",
					AICatalogProfile: "ai-test",
					State:            publication.SnapshotCandidate,
					ARD:              publication.ARDManifest{Entries: []publication.Entry{}},
					AICatalog:        publication.AICatalog{SpecVersion: "1.0", Entries: []publication.AICatalogEntry{}},
					ETag:             fmt.Sprintf("\"generation-%d\"", index),
					AICatalogETag:    fmt.Sprintf("\"generation-ai-%d\"", index),
					CreatedAt:        time.Now().UTC(),
				}
				_, saveErr := publicationStore.SaveCandidate(ctx, snapshot)
				results <- saveErr
			}(index)
		}
		<-ready
		<-ready
		close(start)
		first, second := <-results, <-results
		successes, conflicts := 0, 0
		for _, resultErr := range []error{first, second} {
			switch {
			case resultErr == nil:
				successes++
			case catalog.ErrorCodeOf(resultErr) == catalog.CodeConflict:
				conflicts++
			default:
				t.Fatalf("contended generation returned %v", resultErr)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("contended generation outcomes: successes=%d conflicts=%d", successes, conflicts)
		}
		generation, generationErr := publicationStore.NextGeneration(ctx, tenantID, "generation.example.com")
		if generationErr != nil || generation != 2 {
			t.Fatalf("next generation after contention = %d, %v", generation, generationErr)
		}
		var retained int
		if countErr := pool.QueryRow(ctx, `SELECT count(*) FROM publication_snapshots WHERE tenant_id=$1 AND host='generation.example.com'`, tenantID).Scan(&retained); countErr != nil || retained != 1 {
			t.Fatalf("retained contended candidates = %d, %v", retained, countErr)
		}
	})

	t.Run("concurrent Job claims and fixed-work progress", func(t *testing.T) {
		jobStore, storeErr := NewPostgresJobStore(repository)
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		base := time.Now().UTC().Add(-time.Minute)
		enqueue := func(idValue, tenant, deduplication string, at time.Time) {
			t.Helper()
			job := jobs.Job{ID: idValue, TenantID: tenant, Kind: "fairness.test", Actor: "integration", PayloadVersion: 1, Payload: json.RawMessage(`{}`), State: jobs.StateQueued, NotBefore: at, AttemptLimit: 2, DeduplicationKey: deduplication, CreatedAt: at, UpdatedAt: at}
			if _, existing, enqueueErr := jobStore.Enqueue(ctx, job); enqueueErr != nil || existing {
				t.Fatalf("enqueue %s existing=%t: %v", idValue, existing, enqueueErr)
			}
		}
		enqueue("00000000-0000-7000-8000-000000000211", tenantID, "concurrent-a", base)
		enqueue("00000000-0000-7000-8000-000000000212", otherTenant, "concurrent-b", base)
		type claimResult struct {
			job   jobs.Job
			owner string
			err   error
		}
		start := make(chan struct{})
		results := make(chan claimResult, 2)
		for _, owner := range []string{"replica-a", "replica-b"} {
			go func(owner string) {
				<-start
				claimed, claimErr := jobStore.Claim(ctx, owner, base.Add(time.Minute), time.Minute)
				results <- claimResult{job: claimed, owner: owner, err: claimErr}
			}(owner)
		}
		close(start)
		first, second := <-results, <-results
		if first.err != nil || second.err != nil || first.job.ID == second.job.ID {
			t.Fatalf("concurrent claims = %#v / %#v", first, second)
		}
		for _, result := range []claimResult{first, second} {
			if _, completeErr := jobStore.Complete(ctx, result.job.TenantID, result.job.ID, result.owner, base.Add(time.Minute), json.RawMessage(`{}`)); completeErr != nil {
				t.Fatal(completeErr)
			}
		}

		for index := range 3 {
			enqueue(fmt.Sprintf("00000000-0000-7000-8000-%012d", 221+index), tenantID, fmt.Sprintf("fixed-a-%d", index), base.Add(time.Duration(index)*time.Nanosecond))
		}
		fixedOtherID := "00000000-0000-7000-8000-000000000224"
		enqueue(fixedOtherID, otherTenant, "fixed-b", base.Add(3*time.Nanosecond))
		seenOtherAt := -1
		for turn := range 4 {
			owner := fmt.Sprintf("fair-replica-%d", turn)
			claimed, claimErr := jobStore.Claim(ctx, owner, base.Add(time.Minute), time.Minute)
			if claimErr != nil {
				t.Fatal(claimErr)
			}
			if claimed.ID == fixedOtherID {
				seenOtherAt = turn
			}
			if _, completeErr := jobStore.Complete(ctx, claimed.TenantID, claimed.ID, owner, base.Add(time.Minute), json.RawMessage(`{}`)); completeErr != nil {
				t.Fatal(completeErr)
			}
			enqueue(fmt.Sprintf("00000000-0000-7000-8000-%012d", 225+turn), tenantID, fmt.Sprintf("arriving-a-%d", turn), base.Add(time.Second+time.Duration(turn)*time.Nanosecond))
		}
		if seenOtherAt != 3 {
			t.Fatalf("fixed equal-priority work was claimed at turn %d, want 3", seenOtherAt)
		}
	})

	t.Run("automatic source admission advances after cap turnover", func(t *testing.T) {
		sourceStore, storeErr := NewPostgresFederationStore(repository)
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		maintenanceStore, storeErr := NewPostgresSourceMaintenanceStore(repository)
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		const policyDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		now := time.Now().UTC()
		for _, source := range []federation.Source{
			{ID: "00000000-0000-7000-8000-000000000241", TenantID: tenantID, DisplayName: "Automatic A", Type: federation.SourceARDManifest, Endpoint: "https://automatic-a.example/ard.json", Status: federation.SourceActive, ProfileID: "integration", RetrievalMode: federation.RetrievalIndexed, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: federation.UpdateAutomatic, PolicyID: "integration", PolicyRevision: 1, PolicyDigest: policyDigest, Revision: 1, CreatedBy: "integration", CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute)},
			{ID: "00000000-0000-7000-8000-000000000242", TenantID: otherTenant, DisplayName: "Automatic B", Type: federation.SourceARDManifest, Endpoint: "https://automatic-b.example/ard.json", Status: federation.SourceActive, ProfileID: "integration", RetrievalMode: federation.RetrievalIndexed, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: federation.UpdateAutomatic, PolicyID: "integration", PolicyRevision: 1, PolicyDigest: policyDigest, Revision: 1, CreatedBy: "integration", CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute)},
		} {
			if _, createErr := sourceStore.CreateSource(ctx, source); createErr != nil {
				t.Fatal(createErr)
			}
		}
		first, listErr := maintenanceStore.ListDueAutomaticSources(ctx, now, time.Minute, policyDigest, 1)
		if listErr != nil || len(first) != 1 {
			t.Fatalf("first automatic candidate = %#v, %v", first, listErr)
		}
		candidate := first[0]
		payload := json.RawMessage(fmt.Sprintf(`{"sourceId":%q,"sourceRevision":%d,"trigger":"automatic"}`, candidate.SourceID, candidate.SourceRevision))
		job := jobs.Job{ID: "00000000-0000-7000-8000-000000000243", TenantID: candidate.TenantID, Kind: "source.sync", Actor: audit.InitiatorSourceMaintainer, PayloadVersion: 2, Payload: payload, State: jobs.StateQueued, NotBefore: now, AttemptLimit: 5, DeduplicationKey: "automatic-first", CreatedAt: now, UpdatedAt: now}
		if _, existing, admitErr := maintenanceStore.AdmitAutomaticSync(ctx, candidate, job, time.Minute, policyDigest, 64); admitErr != nil || existing {
			t.Fatalf("first automatic admission existing=%t: %v", existing, admitErr)
		}
		second, listErr := maintenanceStore.ListDueAutomaticSources(ctx, now, time.Minute, policyDigest, 1)
		if listErr != nil || len(second) != 1 || second[0].SourceID == candidate.SourceID || second[0].TenantID == candidate.TenantID {
			t.Fatalf("automatic candidate after turnover = %#v, first=%#v, %v", second, candidate, listErr)
		}
	})
}
