package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

const admissionLock int64 = 215001
const processorLock int64 = 215002

var errEmpty = errors.New("inbox_empty")
var errFull = errors.New("receiver_capacity")

type observation struct {
	ResourceID         string           `json:"resourceId"`
	AttemptedReleaseID string           `json:"attemptedReleaseId,omitempty"`
	State              string           `json:"state"`
	CheckedAt          time.Time        `json:"checkedAt"`
	EligibleAt         *time.Time       `json:"eligibleAt,omitempty"`
	Failure            string           `json:"failure,omitempty"`
	Catalog            *reference       `json:"catalogEvidence,omitempty"`
	Capture            *capture         `json:"capture,omitempty"`
	Scanner            *scannerIdentity `json:"scanner,omitempty"`
	AnalysisReused     bool             `json:"analysisReused"`
	Trigger            *Event           `json:"trigger,omitempty"`
	ScanKey            string           `json:"-"`
	ScanFile           string           `json:"scanFile,omitempty"`
	ScanDigest         string           `json:"scanSha256,omitempty"`
}

type work struct {
	Event    Event
	Attempts int
}
type store interface {
	Accept(context.Context, Event, time.Time) error
	Next(context.Context, time.Time) (*work, error)
	Finish(context.Context, *work, *observation, json.RawMessage, time.Time, string) error
	Retry(context.Context, *work, time.Time, string) error
	Cached(context.Context, string) (json.RawMessage, error)
	Reconciled(context.Context, []mapping, map[string]string, [2]int64, time.Time) error
	ReconciliationFailed(context.Context, string, time.Time) error
}
type postgresStore struct {
	pool      *pgxpool.Pool
	capacity  int
	retention time.Duration
}

func (s *postgresStore) init(ctx context.Context, tenant, origin, digest string) error {
	var catalog bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname='catalog_schema_migrations')`).Scan(&catalog); err != nil || catalog {
		return errors.New("dedicated_receiver_database_required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, schema); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO mcp_receiver_settings(singleton,tenant,origin,mapping_digest) VALUES(true,$1,$2,$3) ON CONFLICT DO NOTHING`, tenant, origin, digest); err != nil {
		return err
	}
	if err = bindSettings(ctx, tx, tenant, origin, digest); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func bindSettings(ctx context.Context, q rowQuerier, tenant, origin, digest string) error {
	var a, b, c string
	if err := q.QueryRow(ctx, `SELECT tenant,origin,mapping_digest FROM mcp_receiver_settings WHERE singleton`).Scan(&a, &b, &c); err != nil {
		return err
	}
	if a != tenant || b != origin || c != digest {
		return errors.New("receiver_binding_mismatch")
	}
	return nil
}

func (s *postgresStore) Accept(ctx context.Context, e Event, at time.Time) error {
	if e.Source == reconciliationSource {
		return errors.New("invalid_notification_source")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, admissionLock); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_receiver_inbox WHERE source=$1 AND id=$2)`, e.Source, e.ID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `WITH old AS (SELECT source,id FROM mcp_receiver_inbox WHERE source<>$1 AND completed_at<$2 ORDER BY completed_at,source,id LIMIT 128) DELETE FROM mcp_receiver_inbox i USING old WHERE i.source=old.source AND i.id=old.id`, reconciliationSource, at.Add(-s.retention)); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_receiver_inbox WHERE source<>$1 LIMIT 1 OFFSET $2)`, reconciliationSource, s.capacity-1).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return errFull
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO mcp_receiver_inbox(source,id,event,accepted_at,next_attempt_at) VALUES($1,$2,$3,$4,$4)`, e.Source, e.ID, raw, at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *postgresStore) Next(ctx context.Context, at time.Time) (*work, error) {
	var raw []byte
	var w work
	err := s.pool.QueryRow(ctx, `SELECT event,attempts FROM mcp_receiver_inbox WHERE completed_at IS NULL AND next_attempt_at<=$1 ORDER BY next_attempt_at,accepted_at,source,id LIMIT 1`, at).Scan(&raw, &w.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errEmpty
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &w.Event); err != nil {
		return nil, err
	}
	return &w, nil
}

func (s *postgresStore) Cached(ctx context.Context, key string) (json.RawMessage, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT report FROM mcp_receiver_scans WHERE scan_key=$1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}

func (s *postgresStore) Finish(ctx context.Context, w *work, o *observation, report json.RawMessage, at time.Time, code string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, admissionLock); err != nil {
		return err
	}
	if len(report) > 0 {
		if o == nil || !hashPattern.MatchString(o.ScanKey) || len(report) > reportLimit {
			return errors.New("invalid_saved_report")
		}
		if _, err = tx.Exec(ctx, `WITH old AS (SELECT scan_key FROM mcp_receiver_scans s WHERE created_at<$1 AND NOT EXISTS(SELECT 1 FROM mcp_receiver_observations o WHERE o.scan_key=s.scan_key) ORDER BY created_at,scan_key LIMIT 128) DELETE FROM mcp_receiver_scans s USING old WHERE s.scan_key=old.scan_key`, at.Add(-s.retention)); err != nil {
			return err
		}
		var allowed bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_receiver_scans WHERE scan_key=$1) OR NOT EXISTS(SELECT 1 FROM mcp_receiver_scans LIMIT 1 OFFSET $2)`, o.ScanKey, s.capacity-1).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return errFull
		}
		if _, err = tx.Exec(ctx, `INSERT INTO mcp_receiver_scans(scan_key,report,created_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, o.ScanKey, []byte(report), at); err != nil {
			return err
		}
	}
	if o != nil && o.ResourceID != "" {
		if len(report) == 0 {
			var previous []byte
			var oldKey *string
			err = tx.QueryRow(ctx, `SELECT document,scan_key FROM mcp_receiver_observations WHERE resource_id=$1`, o.ResourceID).Scan(&previous, &oldKey)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if len(previous) > 0 {
				var old observation
				if json.Unmarshal(previous, &old) != nil {
					return errors.New("invalid_saved_observation")
				}
				o.Catalog, o.Capture, o.Scanner = old.Catalog, old.Capture, old.Scanner
				if oldKey != nil {
					o.ScanKey = *oldKey
				}
			}
			if code != "" {
				o.Failure = code
			}
		}
		raw, marshalErr := json.Marshal(o)
		if marshalErr != nil {
			return marshalErr
		}
		release := ""
		if o.Catalog != nil {
			release = o.Catalog.ReleaseID
		}
		var key *string
		if o.ScanKey != "" {
			key = &o.ScanKey
		}
		if _, err = tx.Exec(ctx, `INSERT INTO mcp_receiver_observations(resource_id,release_id,scan_key,document,checked_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(resource_id) DO UPDATE SET release_id=excluded.release_id,scan_key=excluded.scan_key,document=excluded.document,checked_at=excluded.checked_at`, o.ResourceID, release, key, raw, at); err != nil {
			return err
		}
	} else if w != nil && code != "" {
		// A delayed event for an old release must not invalidate a newer report.
		if _, err = tx.Exec(ctx, `UPDATE mcp_receiver_observations SET document=jsonb_set(jsonb_set(jsonb_set(document,'{state}','"historical"'),'{failure}',to_jsonb($2::text)),'{checkedAt}',to_jsonb($3::timestamptz)),checked_at=$3 WHERE release_id=$1`, w.Event.TargetID, code, at); err != nil {
			return err
		}
	}
	if w != nil {
		tag, err := tx.Exec(ctx, `UPDATE mcp_receiver_inbox SET completed_at=$3,outcome=$4 WHERE source=$1 AND id=$2 AND completed_at IS NULL AND event->>'targetId'=$5`, w.Event.Source, w.Event.ID, at, code, w.Event.TargetID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("pending_receipt_changed")
		}
	}
	return tx.Commit(ctx)
}

func (s *postgresStore) Retry(ctx context.Context, w *work, next time.Time, code string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE mcp_receiver_inbox SET attempts=attempts+1,next_attempt_at=$3,outcome=$4 WHERE source=$1 AND id=$2 AND completed_at IS NULL AND attempts=$5`, w.Event.Source, w.Event.ID, next, code, w.Attempts)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("pending_receipt_changed")
	}
	return nil
}

func (s *postgresStore) Reconciled(ctx context.Context, configs []mapping, found map[string]string, loss [2]int64, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, admissionLock); err != nil {
		return err
	}
	for _, m := range configs {
		release, ok := found[m.ResourceID]
		if !ok {
			if _, err = tx.Exec(ctx, `UPDATE mcp_receiver_observations SET document=jsonb_set(jsonb_set(document,'{state}','"not_listed"'),'{checkedAt}',to_jsonb($2::timestamptz)),checked_at=$2 WHERE resource_id=$1`, m.ResourceID, at); err != nil {
				return err
			}
			continue
		}
		e := Event{ID: m.ResourceID, Source: reconciliationSource, Action: "reconcile", TargetID: release}
		raw, marshalErr := json.Marshal(e)
		if marshalErr != nil {
			return marshalErr
		}
		// One coalesced reconciliation slot per configured Resource is bounded
		// separately. It must not consume notification admission capacity.
		if _, err = tx.Exec(ctx, `INSERT INTO mcp_receiver_inbox(source,id,event,accepted_at,next_attempt_at) VALUES($1,$2,$3,$4,$4) ON CONFLICT(source,id) DO UPDATE SET event=excluded.event,accepted_at=excluded.accepted_at,next_attempt_at=excluded.next_attempt_at,attempts=0,completed_at=NULL,outcome='' WHERE mcp_receiver_inbox.completed_at IS NOT NULL`, e.Source, e.ID, raw, at); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE mcp_receiver_observations SET document=jsonb_set(jsonb_set(document,'{state}','"pending"'),'{checkedAt}',to_jsonb($2::timestamptz)),checked_at=$2 WHERE resource_id=$1 AND release_id<>$3`, m.ResourceID, at, release); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE mcp_receiver_settings SET capacity_loss=$1,delivery_loss=$2,reconciled_at=$3,reconciliation_failure='' WHERE singleton`, loss[0], loss[1], at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *postgresStore) ReconciliationFailed(ctx context.Context, code string, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE mcp_receiver_settings SET reconciliation_failure=$1 WHERE singleton`, code); err != nil {
		return err
	}
	// A failed traversal never claims absence. Old observations become explicitly stale.
	if _, err = tx.Exec(ctx, `UPDATE mcp_receiver_observations SET document=jsonb_set(jsonb_set(document,'{state}','"historical"'),'{checkedAt}',to_jsonb($1::timestamptz)),checked_at=$1`, at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *postgresStore) processor(ctx context.Context) (func(context.Context) error, func(), error) {
	c, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	var ok bool
	if err = c.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, processorLock).Scan(&ok); err != nil || !ok {
		c.Release()
		return nil, nil, errors.New("processor_already_running")
	}
	guard := func(ctx context.Context) error { _, err := c.Exec(ctx, "SELECT 1"); return err }
	closeLock := func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if _, err := c.Exec(ctx, `SELECT pg_advisory_unlock($1)`, processorLock); err != nil {
			_ = c.Conn().Close(ctx)
		}
		c.Release()
	}
	return guard, closeLock, nil
}
