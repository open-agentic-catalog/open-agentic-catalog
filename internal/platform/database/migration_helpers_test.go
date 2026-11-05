package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These fixtures keep migration and privilege phases separate so integration
// tests can inspect intermediate state and failures. Production uses
// ApplyAndReconcileRuntimePrivileges under one session advisory lock.
// Unit callers exercise only nil-pool rejection; real pools require integration.
func applyMigrationsForTest(ctx context.Context, pool *pgxpool.Pool) (applied []Migration, resultErr error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	lockConnection, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration lock connection: %w", err)
	}
	defer lockConnection.Release()
	if _, err := lockConnection.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationAdvisoryLockKey); err != nil {
		return nil, fmt.Errorf("obtain migration lock: %w", err)
	}
	defer func(parent context.Context) {
		if err := releaseMigrationLock(parent, lockConnection.Conn()); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}(ctx)
	return applyPendingMigrations(ctx, lockConnection)
}

func reconcileRuntimePrivilegesForTest(ctx context.Context, pool *pgxpool.Pool, apiRole, workerRole string) ([]RuntimePrivilegeReport, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return reconcileRuntimePrivileges(ctx, pool, apiRole, workerRole, true)
}
