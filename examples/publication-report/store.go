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

var errFull = errors.New("inbox_full")
var errEmpty = errors.New("inbox_empty")

const admissionLock int64 = 209001
const processorLock int64 = 209002

type store interface {
	Accept(context.Context, Event, time.Time) error
	Next(context.Context) (*Event, error)
	Complete(context.Context, *Event, *Report, time.Time, *[2]int64) error
	Fail(context.Context, time.Time, string) error
	Read(context.Context) (Saved, error)
}

type postgresStore struct {
	pool      *pgxpool.Pool
	capacity  int
	retention time.Duration
}

func (s *postgresStore) init(ctx context.Context, tenant, host, origin string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, schema); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO publication_receiver_settings(singleton,tenant,host,origin) VALUES(true,$1,$2,$3) ON CONFLICT DO NOTHING`, tenant, host, origin); err != nil {
		return err
	}
	var a, b, c string
	if err = tx.QueryRow(ctx, `SELECT tenant,host,origin FROM publication_receiver_settings WHERE singleton`).Scan(&a, &b, &c); err != nil {
		return err
	}
	if a != tenant || b != host || c != origin {
		return errors.New("receiver_database_binding_mismatch")
	}
	return tx.Commit(ctx)
}

func (s *postgresStore) bind(ctx context.Context, tenant, host, origin string) error {
	var a, b, c string
	if err := s.pool.QueryRow(ctx, `SELECT tenant,host,origin FROM publication_receiver_settings WHERE singleton`).Scan(&a, &b, &c); err != nil {
		return err
	}
	if a != tenant || b != host || c != origin {
		return errors.New("receiver_database_binding_mismatch")
	}
	return nil
}

func (s *postgresStore) Accept(ctx context.Context, e Event, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, admissionLock); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM publication_receiver_inbox WHERE source=$1 AND id=$2)`, e.Source, e.ID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `WITH old AS (SELECT source,id FROM publication_receiver_inbox WHERE completed_at < $1 ORDER BY completed_at,source,id LIMIT 128) DELETE FROM publication_receiver_inbox i USING old WHERE i.source=old.source AND i.id=old.id`, now.Add(-s.retention)); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM publication_receiver_inbox LIMIT 1 OFFSET $1)`, s.capacity-1).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return errFull
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO publication_receiver_inbox(source,id,event,accepted_at) VALUES($1,$2,$3,$4)`, e.Source, e.ID, raw, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *postgresStore) Next(ctx context.Context) (*Event, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT event FROM publication_receiver_inbox WHERE completed_at IS NULL ORDER BY accepted_at,source,id LIMIT 1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errEmpty
	}
	if err != nil {
		return nil, err
	}
	var e Event
	if err = json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *postgresStore) Complete(ctx context.Context, e *Event, r *Report, at time.Time, loss *[2]int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if e != nil {
		tag, err := tx.Exec(ctx, `UPDATE publication_receiver_inbox SET completed_at=$3 WHERE source=$1 AND id=$2 AND completed_at IS NULL`, e.Source, e.ID, at)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("pending_receipt_changed")
		}
	}
	if r != nil {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE publication_receiver_report SET last_success=$1,attempted_at=$2,failure='' WHERE singleton`, raw, at)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("report_row_missing")
		}
	}
	if loss != nil {
		if _, err = tx.Exec(ctx, `UPDATE publication_receiver_report SET capacity_loss=$1,delivery_loss=$2,loss_observed_at=$3 WHERE singleton`, loss[0], loss[1], at); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *postgresStore) Fail(ctx context.Context, at time.Time, code string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE publication_receiver_report SET attempted_at=$1,failure=$2 WHERE singleton`, at, code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("report_row_missing")
	}
	return nil
}

func (s *postgresStore) Read(ctx context.Context) (Saved, error) {
	var raw []byte
	var out Saved
	if err := s.pool.QueryRow(ctx, `SELECT last_success,attempted_at,failure,capacity_loss,delivery_loss,loss_observed_at FROM publication_receiver_report WHERE singleton`).Scan(&raw, &out.LastAttemptAt, &out.RefreshFailure, &out.CapacityLoss, &out.DeliveryLoss, &out.LossObservedAt); err != nil {
		return Saved{}, err
	}
	if len(raw) > 0 {
		out.LastSuccessful = &Report{}
		if err := json.Unmarshal(raw, out.LastSuccessful); err != nil {
			return Saved{}, err
		}
	}
	return out, nil
}

// A session lock rejects a second processor; it supplies no lease or takeover protocol.
func (s *postgresStore) processor(ctx context.Context) (func(), error) {
	c, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var ok bool
	if err = c.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, processorLock).Scan(&ok); err != nil || !ok {
		c.Release()
		return nil, errors.New("processor_already_running")
	}
	return func() {
		release, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer stop()
		if _, err := c.Exec(release, `SELECT pg_advisory_unlock($1)`, processorLock); err != nil {
			_ = c.Conn().Close(release)
		}
		c.Release()
	}, nil
}
