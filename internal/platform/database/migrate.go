// Migration planning verifies embedded checksums; application and exact runtime
// grants share one schema-owner advisory-lock boundary.

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

type Migration struct {
	Version  int64  `json:"version"`
	Name     string `json:"name"`
	Checksum string `json:"checksum"`
	Applied  bool   `json:"applied"`
}

const (
	migrationAdvisoryLockKey  int64 = 880185172399
	migrationLockCleanupLimit       = 5 * time.Second
)

type migrationDatabase interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

type migrationPlanDatabase interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// migrationLockSession is the acquired PostgreSQL session used for bounded
// unlock and connection discard; its caller retains pool release ownership.
type migrationLockSession interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Close(context.Context) error
}

// Plan compares the applied ledger with embedded migration checksums and returns
// the ordered plan without applying SQL or reconciling privileges.
func Plan(ctx context.Context, pool *pgxpool.Pool) ([]Migration, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return planMigrations(ctx, pool)
}

// EmbeddedSchemaVersion returns the highest immutable migration bundled with
// this binary. Runtime-role verification separately proves that its exact
// schema objects and privileges match the binary before startup succeeds.
func EmbeddedSchemaVersion() (int64, error) {
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations: %w", err)
	}
	var latest int64
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		versionText, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return 0, fmt.Errorf("migration name %q has no version prefix", entry.Name())
		}
		version, err := strconv.ParseInt(versionText, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse migration version %q: %w", versionText, err)
		}
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		return 0, errors.New("embedded migration bundle is empty")
	}
	return latest, nil
}

// planMigrations validates embedded names/checksums and the existing ledger before
// marking applied entries, without schema writes.
func planMigrations(ctx context.Context, database migrationPlanDatabase) ([]Migration, error) {
	var migrationTableExists bool
	if err := database.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
    FROM pg_catalog.pg_class class
    JOIN pg_catalog.pg_namespace namespace ON namespace.oid=class.relnamespace
   WHERE namespace.nspname=current_schema()
     AND class.relname='catalog_schema_migrations'
     AND class.relkind IN ('r','p')
)`).Scan(&migrationTableExists); err != nil {
		return nil, fmt.Errorf("inspect migration table: %w", err)
	}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	result := make([]Migration, 0, len(entries))
	versions := make([]int64, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		versionText, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration name %q has no version prefix", entry.Name())
		}
		version, err := strconv.ParseInt(versionText, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse migration version %q: %w", versionText, err)
		}
		body, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(body)
		migration := Migration{Version: version, Name: entry.Name(), Checksum: hex.EncodeToString(sum[:])}
		versions = append(versions, version)
		if migrationTableExists {
			var stored string
			err = database.QueryRow(ctx, `SELECT checksum FROM catalog_schema_migrations WHERE version = $1`, version).Scan(&stored)
			switch {
			case err == nil:
				if stored != migration.Checksum {
					return nil, fmt.Errorf("migration %d checksum changed after application", version)
				}
				migration.Applied = true
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				return nil, fmt.Errorf("read migration state: %w", err)
			}
		}
		result = append(result, migration)
	}
	if migrationTableExists {
		var unknownVersion int64
		err := database.QueryRow(ctx, `
SELECT version
  FROM catalog_schema_migrations
 WHERE NOT (version = ANY($1::bigint[]))
 ORDER BY version
 LIMIT 1`, versions).Scan(&unknownVersion)
		switch {
		case err == nil:
			return nil, fmt.Errorf("applied migration version %d is absent from the embedded migration bundle", unknownVersion)
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return nil, fmt.Errorf("inspect unknown migration versions: %w", err)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Version < result[j].Version })
	return result, nil
}

// ApplyAndReconcileRuntimePrivileges holds one session advisory lock across
// every forward migration and the exact runtime-role grant reconciliation.
// Runtime roles are validated before any schema change is committed.
func ApplyAndReconcileRuntimePrivileges(ctx context.Context, pool *pgxpool.Pool, apiRole, workerRole string, semantic ...bool) (applied []Migration, reports []RuntimePrivilegeReport, resultErr error) {
	if len(semantic) > 1 {
		return nil, nil, errors.New("at most one semantic setup selection is permitted")
	}
	if pool == nil {
		return nil, nil, errors.New("PostgreSQL pool is required")
	}
	if err := ValidateRuntimeDatabaseRoles(apiRole, workerRole); err != nil {
		return nil, nil, err
	}
	lockConnection, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire migration lock connection: %w", err)
	}
	defer lockConnection.Release()
	if _, err := lockConnection.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationAdvisoryLockKey); err != nil {
		return nil, nil, fmt.Errorf("obtain migration lock: %w", err)
	}
	defer func(parent context.Context) {
		if err := releaseMigrationLock(parent, lockConnection.Conn()); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}(ctx)
	if err := validateRuntimeRoleBindings(ctx, lockConnection, apiRole, workerRole); err != nil {
		return nil, nil, err
	}
	applied, err = applyPendingMigrations(ctx, lockConnection)
	if err != nil {
		return nil, nil, err
	}
	if len(semantic) == 1 && semantic[0] {
		if err := applySemanticSchema(ctx, lockConnection); err != nil {
			return applied, nil, err
		}
	}
	reports, err = reconcileRuntimePrivileges(ctx, lockConnection, apiRole, workerRole, false)
	if err != nil {
		return applied, nil, err
	}
	return applied, reports, nil
}

// applyPendingMigrations commits each unapplied migration and its checksum ledger
// row together; earlier commits remain if a later migration fails.
func applyPendingMigrations(ctx context.Context, database migrationDatabase) ([]Migration, error) {
	plan, err := planMigrations(ctx, database)
	if err != nil {
		return nil, err
	}
	applied := make([]Migration, 0)
	for _, migration := range plan {
		if migration.Applied {
			continue
		}
		body, err := migrations.Files.ReadFile(migration.Name)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", migration.Name, err)
		}
		tx, err := database.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return nil, fmt.Errorf("begin migration %d: %w", migration.Version, err)
		}
		if _, err = tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("apply migration %d: %w", migration.Version, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO catalog_schema_migrations(version, checksum) VALUES ($1, $2)`, migration.Version, migration.Checksum); err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("record migration %d: %w", migration.Version, err)
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit migration %d: %w", migration.Version, err)
		}
		migration.Applied = true
		applied = append(applied, migration)
	}
	return applied, nil
}

// releaseMigrationLock attempts session unlock within a cancellation-independent
// deadline and closes the connection if lock release cannot be confirmed.
func releaseMigrationLock(parent context.Context, connection migrationLockSession) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), migrationLockCleanupLimit)
	defer cancel()

	var unlocked bool
	if err := connection.QueryRow(cleanupCtx, `SELECT pg_advisory_unlock($1)`, migrationAdvisoryLockKey).Scan(&unlocked); err != nil {
		unlockErr := fmt.Errorf("release migration lock: %w", err)
		if closeErr := connection.Close(cleanupCtx); closeErr != nil {
			return errors.Join(unlockErr, fmt.Errorf("close migration lock connection: %w", closeErr))
		}
		return unlockErr
	}
	if unlocked {
		return nil
	}

	unlockErr := errors.New("release migration lock: advisory lock was not held by its connection")
	if closeErr := connection.Close(cleanupCtx); closeErr != nil {
		return errors.Join(unlockErr, fmt.Errorf("close migration lock connection: %w", closeErr))
	}
	return unlockErr
}
