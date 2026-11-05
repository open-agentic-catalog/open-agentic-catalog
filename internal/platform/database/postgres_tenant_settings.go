package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/tenantsettings"
)

type PostgresTenantSettingsStore struct{ repository *PostgresRepository }

func NewPostgresTenantSettingsStore(repository *PostgresRepository) (*PostgresTenantSettingsStore, error) {
	if repository == nil {
		return nil, errors.New("tenant settings repository is required")
	}
	return &PostgresTenantSettingsStore{repository}, nil
}
func readTenantSettings(ctx context.Context, tx pgx.Tx, tenant string) (tenantsettings.Record, error) {
	r := tenantsettings.Record{TenantID: tenant}
	var interval *int64
	err := tx.QueryRow(ctx, `SELECT revision,automatic_source_sync_enabled,automatic_source_sync_interval_ns,new_job_max_attempts,updated_at FROM tenant_settings WHERE tenant_id=$1`, tenant).Scan(&r.Revision, &r.Overrides.AutomaticSourceSyncEnabled, &interval, &r.Overrides.NewJobMaxAttempts, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return tenantsettings.Record{TenantID: tenant}, nil
	}
	if err != nil {
		return tenantsettings.Record{}, translateError("read tenant settings", err)
	}
	if r.UpdatedAt != nil {
		value := r.UpdatedAt.UTC()
		r.UpdatedAt = &value
	}
	if interval != nil {
		value := tenantsettings.Duration(*interval)
		r.Overrides.AutomaticSourceSyncInterval = &value
	}
	return r, nil
}
func (p *PostgresTenantSettingsStore) Get(ctx context.Context, tenant string) (tenantsettings.Record, error) {
	return withTenantRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (tenantsettings.Record, error) { return readTenantSettings(ctx, tx, tenant) })
}
func lockTenantSettings(ctx context.Context, tx pgx.Tx, tenant string, shared bool) error {
	function := "pg_advisory_xact_lock"
	if shared {
		function = "pg_advisory_xact_lock_shared"
	}
	_, err := tx.Exec(ctx, `SELECT `+function+`($1)`, catalogStateLockKey("tenant-settings", tenant))
	return translateError("lock tenant settings", err)
}
func (p *PostgresTenantSettingsStore) Save(ctx context.Context, r tenantsettings.Record, defaults tenantsettings.Policy, tag string, evidence audit.Record) (tenantsettings.Record, error) {
	return withTenantTx(ctx, p.repository.pool, r.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (tenantsettings.Record, error) {
		if err := lockTenantSettings(ctx, tx, r.TenantID, false); err != nil {
			return tenantsettings.Record{}, err
		}
		old, err := readTenantSettings(ctx, tx, r.TenantID)
		if err != nil {
			return tenantsettings.Record{}, err
		}
		if err = tenantsettings.ValidateSave(old, r, defaults, tag, evidence); err != nil {
			return tenantsettings.Record{}, err
		}
		var interval *int64
		if r.Overrides.AutomaticSourceSyncInterval != nil {
			value := int64(*r.Overrides.AutomaticSourceSyncInterval)
			interval = &value
		}
		if old.Revision == 0 {
			_, err = tx.Exec(ctx, `INSERT INTO tenant_settings(tenant_id,revision,automatic_source_sync_enabled,automatic_source_sync_interval_ns,new_job_max_attempts,updated_at) VALUES($1,$2,$3,$4,$5,$6)`, r.TenantID, r.Revision, r.Overrides.AutomaticSourceSyncEnabled, interval, r.Overrides.NewJobMaxAttempts, r.UpdatedAt)
		} else {
			_, err = tx.Exec(ctx, `UPDATE tenant_settings SET revision=$2,automatic_source_sync_enabled=$3,automatic_source_sync_interval_ns=$4,new_job_max_attempts=$5,updated_at=$6 WHERE tenant_id=$1`, r.TenantID, r.Revision, r.Overrides.AutomaticSourceSyncEnabled, interval, r.Overrides.NewJobMaxAttempts, r.UpdatedAt)
		}
		if err != nil {
			return tenantsettings.Record{}, translateError("save tenant settings", err)
		}
		if err = insertAuditEvent(ctx, tx, evidence); err != nil {
			return tenantsettings.Record{}, err
		}
		return r, nil
	})
}

// resolveNewJobAttempts runs after the shared settings lock in a fresh statement.
func resolveNewJobAttempts(ctx context.Context, tx pgx.Tx, tenant string, fallback int) (int, error) {
	record, err := readTenantSettings(ctx, tx, tenant)
	if err != nil {
		return 0, err
	}
	if record.Overrides.NewJobMaxAttempts != nil {
		return *record.Overrides.NewJobMaxAttempts, nil
	}
	return fallback, nil
}
