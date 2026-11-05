//go:build integration

package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPrivateTracePersistenceAndIsolation(t *testing.T) {
	db := newHousekeepingDatabase(t)
	cause := catalog.TraceContext("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	other := catalog.TraceContext("00-4bf92f3577b34da6a3ce929d0e0e4737-00f067aa0ba902b8-00")
	ctx := catalog.WithTraceContext(t.Context(), cause)
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobID, _ := (id.Generator{}).New(now)
	job := jobs.Job{ID: jobID, TenantID: housekeepingTenantA, Kind: "trace.fixture", Actor: "fixture", PayloadVersion: 1, Payload: json.RawMessage(`{}`), State: jobs.StateQueued, AttemptLimit: 3, DeduplicationKey: "trace-fixture", NotBefore: now, CreatedAt: now, UpdatedAt: now}
	created, existing, err := db.apiJobs.Enqueue(ctx, job)
	if err != nil || existing || created.CausalTrace != cause {
		t.Fatalf("enqueue causal metadata: %v", err)
	}
	job.ID, _ = (id.Generator{}).New(now)
	replay, existing, err := db.apiJobs.Enqueue(catalog.WithTraceContext(ctx, other), job)
	if err != nil || !existing || replay.ID != created.ID || replay.CausalTrace != cause {
		t.Fatalf("deduplicated creation context changed: %v", err)
	}
	for _, store := range []*PostgresJobStore{db.apiJobs, db.workerJobs} {
		if _, err := store.Get(ctx, housekeepingTenantB, created.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("cross-tenant lookup: %v", err)
		}
	}
	claimed, err := db.workerJobs.Claim(ctx, "first", now.Add(time.Second), time.Second)
	if err != nil || claimed.CausalTrace != cause {
		t.Fatalf("claim context: %v", err)
	}
	takeover, err := db.workerJobs.Claim(ctx, "second", now.Add(3*time.Second), time.Minute)
	if err != nil || takeover.ID != created.ID || takeover.CausalTrace != cause || takeover.Attempt <= claimed.Attempt {
		t.Fatalf("takeover context: %v", err)
	}
	for _, pool := range []*pgxpool.Pool{db.api, db.worker} {
		_, err := withTenantTx(ctx, pool, housekeepingTenantA, pgx.TxOptions{}, func(tx pgx.Tx) (bool, error) {
			_, err := tx.Exec(ctx, `UPDATE jobs SET causal_trace=$1 WHERE tenant_id=$2 AND id=$3`, other, housekeepingTenantA, created.ID)
			return false, err
		})
		if err == nil {
			t.Fatal("runtime role rewrote causal context")
		}
	}
	if _, err := db.operator.Exec(ctx, `UPDATE jobs SET causal_trace=$1 WHERE tenant_id=$2 AND id=$3`, other, housekeepingTenantA, created.ID); err == nil {
		t.Fatal("owner bypassed causal immutability")
	}
	eventID, _ := (id.Generator{}).New(now)
	data, err := events.MarshalReference("resource.created", "resource", eventID, "")
	if err != nil {
		t.Fatal(err)
	}
	event := catalog.OutboxEvent{ID: eventID, TenantID: housekeepingTenantA, Type: events.CanonicalType("resource.created"), Subject: eventID, Data: data, CreatedAt: now}
	_, err = withTenantTx(ctx, db.api, housekeepingTenantA, pgx.TxOptions{}, func(tx pgx.Tx) (bool, error) {
		return true, insertOutboxEvent(ctx, tx, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := NewPostgresRepository(db.worker)
	claimedEvents, err := repo.ClaimOutbox(ctx, "trace-worker", now.Add(time.Second), time.Minute, 1)
	if err != nil || len(claimedEvents) != 1 || claimedEvents[0].CausalTrace != cause {
		t.Fatalf("outbox context: %v", err)
	}
	if _, err := db.operator.Exec(ctx, `UPDATE outbox_events SET causal_trace=NULL WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, eventID); err == nil {
		t.Fatal("outbox cause was mutable")
	}
	for _, value := range []any{created, jobs.PublicView(created), claimedEvents[0]} {
		raw, err := json.Marshal(value)
		if err != nil || bytes.Contains(raw, []byte(cause)) || bytes.Contains(raw, []byte("causal")) {
			t.Fatal("private metadata escaped JSON projection")
		}
	}
	rolledBackID, _ := (id.Generator{}).New(now)
	event.ID = rolledBackID
	_, err = withTenantTx(ctx, db.api, housekeepingTenantA, pgx.TxOptions{}, func(tx pgx.Tx) (bool, error) {
		if err := insertOutboxEvent(ctx, tx, event); err != nil {
			return false, err
		}
		return false, errors.New("rollback fixture")
	})
	var count int
	if err == nil {
		t.Fatal("transaction did not fail")
	}
	if err := db.operator.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE id=$1`, rolledBackID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback retained causal event: %v", err)
	}
}
