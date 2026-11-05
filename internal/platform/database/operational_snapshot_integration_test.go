//go:build integration

// Operational diagnostics use real worker permissions, global rows and SQL ages.
package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

// TestPostgresOperationalSnapshotCountsAndFailures checks global counts and
// rejects partial results when any required aggregate loses read permission.
func TestPostgresOperationalSnapshotCountsAndFailures(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := t.Context()
	repo, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.September, 21, 12, 0, 0, 0, time.FixedZone("fixture", 2*60*60))
	want := observability.OperationalSnapshot{CollectedAt: at.UTC()}
	if got, err := repo.OperationalSnapshot(ctx, at); err != nil || got != want {
		t.Fatalf("empty snapshot = %+v, %v; want %+v", got, err, want)
	}
	// Include two tenants and both live and exactly expired running leases.
	// A much older live claim must not determine the runnable age.
	for i, job := range []struct {
		tenant, kind string
		age          time.Duration
		lease        *time.Time
	}{
		{housekeepingTenantA, "publication.generate", time.Minute, nil},
		{housekeepingTenantA, "semantic.reindex", 2 * time.Minute, nil},
		{housekeepingTenantA, "diagnostic.test", 3 * time.Minute, nil},
		{housekeepingTenantB, "diagnostic.test", 10 * time.Minute, pointerTo(at.Add(time.Minute))},
		{housekeepingTenantB, "semantic.reindex", 5 * time.Minute, &at},
	} {
		id := fmt.Sprintf("00000000-0000-7000-8000-%012d", 901+i)
		created := at.Add(-job.age)
		if _, err := db.operator.Exec(ctx, `INSERT INTO jobs
 (id,tenant_id,kind,actor,payload_version,payload,state,not_before,attempt_limit,deduplication_key,created_at,updated_at)
 VALUES($1::uuid,$2,$3,'diagnostic-test',1,'{}','queued',$4,5,($1::uuid)::text,$4,$4)`, id, job.tenant, job.kind, created); err != nil {
			t.Fatal(err)
		}
		if job.lease != nil {
			if _, err := db.operator.Exec(ctx, `UPDATE jobs SET state='running',attempt=1,
 lease_owner='diagnostic-worker',lease_expires_at=$3,updated_at=$4 WHERE tenant_id=$1 AND id=$2`, job.tenant, id, *job.lease, at.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
	insertHousekeepingOutboxFixtures(t, db, at.Add(-90*time.Second))
	if _, err := db.operator.Exec(ctx, `INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,created_at)
 VALUES(uuidv7(),$1,'com.open.agentic.catalog.resource.created.v1','other-tenant',
 '{"action":"resource.created","targetType":"resource","targetId":"other-tenant"}',$2)`, housekeepingTenantB, at.Add(-30*time.Second)); err != nil {
		t.Fatal(err)
	}
	want.Jobs = observability.JobSnapshot{Queued: 3, Running: 2, Publication: 1, Semantic: 2, SemanticPending: 2, Other: 2, OldestAgeSec: 300}
	want.Outbox = observability.OutboxSnapshot{Pending: 2, Acknowledged: 3, OldestAgeSec: 90}
	want.Webhooks = observability.WebhookSnapshot{DispatchableEvents: 1, OldestDispatchableAgeSec: 90}
	if got, err := repo.OperationalSnapshot(ctx, at); err != nil || got != want {
		t.Fatalf("global snapshot = %+v, %v; want %+v", got, err, want)
	}
	// Database timestamps ahead of the supplied clock must not produce negative ages.
	earlier := want
	earlier.CollectedAt = at.Add(-time.Hour).UTC()
	earlier.Jobs.OldestAgeSec, earlier.Outbox.OldestAgeSec = 0, 0
	earlier.Webhooks = observability.WebhookSnapshot{}
	if got, err := repo.OperationalSnapshot(ctx, earlier.CollectedAt); err != nil || got != earlier {
		t.Fatalf("future-record snapshot = %+v, %v; want %+v", got, err, earlier)
	}

	role := pgx.Identifier{db.worker.Config().ConnConfig.User}.Sanitize()
	for _, test := range []struct{ name, table, column, operation string }{
		{"jobs", "jobs", "state", "collect job operational aggregates"},
		{"sources", "federation_sources", "status", "collect source operational aggregates"},
		{"source snapshots", "source_snapshots", "state", "collect source snapshot operational aggregates"},
		{"publications", "publication_snapshots", "state", "collect publication operational aggregates"},
		{"outbox", "outbox_events", "published_at", "collect outbox operational aggregates"},
		{"webhook dispatch", "webhook_endpoints", "event_types", "collect dispatchable webhook aggregates"},
		{"webhook retries", "webhook_deliveries", "event_type", "collect webhook retry aggregates"},
		{"webhook receivers", "webhook_deliveries", "updated_at", "collect webhook receiver outcome aggregates"},
		{"webhook loss", "webhook_notification_state", "capacity_loss_count", "collect webhook reconciliation aggregates"},
	} {
		t.Run(test.name, func(t *testing.T) {
			column, table := pgx.Identifier{test.column}.Sanitize(), pgx.Identifier{test.table}.Sanitize()
			if _, err := db.operator.Exec(ctx, "REVOKE SELECT ("+column+") ON "+table+" FROM "+role); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				if _, err := db.operator.Exec(cleanup, "GRANT SELECT ("+column+") ON "+table+" TO "+role); err != nil {
					t.Errorf("restore test worker permission: %v", err)
				}
			})
			got, err := repo.OperationalSnapshot(ctx, at)
			if got != (observability.OperationalSnapshot{}) {
				t.Fatalf("failed collection returned a partial snapshot: %+v, %v", got, err)
			}
			var domain *catalog.DomainError
			if !errors.As(err, &domain) || domain.Code != catalog.CodeForbidden || !strings.HasPrefix(domain.Message, test.operation+":") {
				t.Fatalf("collection error = %v; want forbidden with %s context", err, test.operation)
			}
			var denied *pgconn.PgError
			if !errors.As(err, &denied) || denied.Code != "42501" {
				t.Fatalf("collection error = %v; want PostgreSQL permission denial", err)
			}
		})
		if got, err := repo.OperationalSnapshot(ctx, at); err != nil || got != want {
			t.Fatalf("collection did not recover after %s: %+v, %v", test.name, got, err)
		}
	}
}
