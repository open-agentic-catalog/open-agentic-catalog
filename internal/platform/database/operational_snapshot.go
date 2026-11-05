// Worker diagnostics aggregate fixed global job, outbox and source counters
// without returning tenant identifiers or private documents.

package database

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

// OperationalSnapshot returns only fixed global aggregates. Runtime wiring
// exposes it from the worker role, whose BYPASSRLS authority already exists
// solely for bounded cross-tenant leasing; API callers never use this method.
func (p *PostgresRepository) OperationalSnapshot(ctx context.Context, now time.Time) (observability.OperationalSnapshot, error) {
	return collectOperationalSnapshot(ctx, p.pool, now)
}

// collectOperationalSnapshot assembles one complete projection or returns no
// snapshot on a failed read. The caller retains pool and session ownership.
func collectOperationalSnapshot(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, now time.Time) (observability.OperationalSnapshot, error) {
	now = now.UTC()
	snapshot := observability.OperationalSnapshot{CollectedAt: now}
	if err := query.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE state='queued'),
       count(*) FILTER (WHERE state='running'),
       count(*) FILTER (WHERE state='waiting'),
       count(*) FILTER (WHERE state='succeeded'),
       count(*) FILTER (WHERE state='failed'),
       count(*) FILTER (WHERE state='cancelled'),
       count(*) FILTER (WHERE state='dead_letter'),
       count(*) FILTER (WHERE kind='source.sync'),
       count(*) FILTER (WHERE kind='publication.generate'),
       count(*) FILTER (WHERE kind NOT IN ('source.sync','publication.generate','semantic.reindex')),
       count(*) FILTER (WHERE kind='semantic.reindex'),
       count(*) FILTER (WHERE kind='semantic.reindex' AND state IN ('queued','running','waiting')),
       coalesce(greatest(0,extract(epoch FROM ($1::timestamptz-min(created_at) FILTER (
         WHERE state IN ('queued','waiting') OR state='running' AND lease_expires_at <= $1
       )))::bigint),0)
  FROM jobs`, now).Scan(
		&snapshot.Jobs.Queued, &snapshot.Jobs.Running, &snapshot.Jobs.Waiting,
		&snapshot.Jobs.Succeeded, &snapshot.Jobs.Failed, &snapshot.Jobs.Cancelled,
		&snapshot.Jobs.DeadLetter, &snapshot.Jobs.SourceSync, &snapshot.Jobs.Publication,
		&snapshot.Jobs.Other, &snapshot.Jobs.Semantic, &snapshot.Jobs.SemanticPending, &snapshot.Jobs.OldestAgeSec,
	); err != nil {
		return observability.OperationalSnapshot{}, translateError("collect job operational aggregates", err)
	}
	if err := query.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE status='active'),
       count(*) FILTER (WHERE status='paused'),
       count(*) FILTER (WHERE status='error'),
       count(*) FILTER (WHERE status='removed')
  FROM federation_sources`).Scan(
		&snapshot.Sources.Active, &snapshot.Sources.Paused,
		&snapshot.Sources.Error, &snapshot.Sources.Removed,
	); err != nil {
		return observability.OperationalSnapshot{}, translateError("collect source operational aggregates", err)
	}
	if err := query.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE state='complete'),
       count(*) FILTER (WHERE state='incomplete'),
       count(*) FILTER (WHERE state='failed'),
       coalesce(greatest(0,extract(epoch FROM ($1::timestamptz-max(completed_at) FILTER (WHERE state='complete')))::bigint),0)
  FROM source_snapshots`, now).Scan(
		&snapshot.Sources.Complete, &snapshot.Sources.Incomplete,
		&snapshot.Sources.Failed, &snapshot.Sources.LastAgeSec,
	); err != nil {
		return observability.OperationalSnapshot{}, translateError("collect source snapshot operational aggregates", err)
	}
	if err := query.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE state='candidate'),
       count(*) FILTER (WHERE state='active'),
       count(*) FILTER (WHERE state='retired'),
       count(*) FILTER (WHERE state='failed'),
       coalesce(greatest(0,extract(epoch FROM ($1::timestamptz-min(activated_at) FILTER (WHERE state='active')))::bigint),0)
  FROM publication_snapshots`, now).Scan(
		&snapshot.Publications.Candidate, &snapshot.Publications.Active,
		&snapshot.Publications.Retired, &snapshot.Publications.Failed,
		&snapshot.Publications.OldestAgeSec,
	); err != nil {
		return observability.OperationalSnapshot{}, translateError("collect publication operational aggregates", err)
	}
	if err := query.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE published_at IS NULL),
       count(*) FILTER (WHERE published_at IS NOT NULL),
       coalesce(greatest(0,extract(epoch FROM ($1::timestamptz-min(created_at) FILTER (WHERE published_at IS NULL)))::bigint),0)
  FROM outbox_events`, now).Scan(
		&snapshot.Outbox.Pending, &snapshot.Outbox.Acknowledged, &snapshot.Outbox.OldestAgeSec,
	); err != nil {
		return observability.OperationalSnapshot{}, translateError("collect outbox operational aggregates", err)
	}
	var err error
	snapshot.Webhooks, err = webhookOperationalSnapshot(ctx, query, now)
	if err != nil {
		return observability.OperationalSnapshot{}, err
	}
	return snapshot, nil
}
