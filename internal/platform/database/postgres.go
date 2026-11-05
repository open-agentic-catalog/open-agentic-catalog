// Package database implements PostgreSQL persistence, migrations, RLS, and roles.
// Repositories retain tenant predicates even when the worker bypasses RLS.
// Command transactions commit mutations, audit evidence, outbox records, and
// replay responses atomically. Schema-owner migration and repair operations
// remain separate from exact-privilege API and worker runtime connections.
package database

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"maps"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	auditpkg "github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// PostgresRepository is the shared pool-backed root for domain stores. Its data
// operations establish an explicit tenant RLS or worker lease boundary.
type PostgresRepository struct {
	pool *pgxpool.Pool
}

// QueryObserver receives only a normalized PostgreSQL statement class,
// terminal error value, and elapsed time. Implementations must not retain the
// error because it can include provider details.
type QueryObserver interface {
	ObserveDatabase(string, error, time.Duration)
}

const supportedPostgresMajor = 18

const postgresCancelFallbackDelay = 500 * time.Millisecond

// PoolConfig carries the resolved per-process pool policy. Durations must be
// positive; the configuration loader owns defaults and environment precedence.
type PoolConfig struct {
	MaxConnections    int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
}

// Open creates the generic PostgreSQL pool used by operator commands. It
// deliberately permits read-only sessions; long-running runtimes use
// OpenRuntimeObserved and mutating operator commands still require a writer.
func Open(ctx context.Context, dsn string, settings PoolConfig) (*pgxpool.Pool, error) {
	return open(ctx, dsn, settings, nil, false)
}

// OpenRuntimeObserved opens an API or worker pool whose physical connections
// must accept read/write transactions. The tracer receives SQL only inside the
// process; the service tracer immediately reduces it to a fixed statement class.
func OpenRuntimeObserved(ctx context.Context, dsn string, settings PoolConfig, observer QueryObserver) (*pgxpool.Pool, error) {
	var tracer pgx.QueryTracer
	if observer != nil {
		tracer = queryTracer{observer: observer, now: time.Now}
	}
	return open(ctx, dsn, settings, tracer, true)
}

// open configures cancellation recovery, UTC sessions and optional writer
// admission, then checks connectivity and the exact PostgreSQL major before
// returning the pool.
func open(ctx context.Context, dsn string, settings PoolConfig, tracer pgx.QueryTracer, requireReadWrite bool) (*pgxpool.Pool, error) {
	configuration, err := configurePool(dsn, settings, tracer, requireReadWrite)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	var serverVersionNumber int
	if err := pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&serverVersionNumber); err != nil {
		pool.Close()
		return nil, fmt.Errorf("inspect PostgreSQL version: %w", err)
	}
	if err := validatePostgresVersion(serverVersionNumber); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// configurePool applies the service policy after parsing the DSN, preserving
// unrelated driver options and the mandatory connection safety callbacks.
func configurePool(dsn string, settings PoolConfig, tracer pgx.QueryTracer, requireReadWrite bool) (*pgxpool.Config, error) {
	if settings.MaxConnLifetime <= 0 || settings.MaxConnIdleTime <= 0 || settings.HealthCheckPeriod <= 0 {
		return nil, safeerr.New("PostgreSQL pool durations must be positive")
	}
	configuration, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, safeerr.Wrap(fmt.Errorf("parse PostgreSQL configuration: %w", err), "invalid PostgreSQL connection configuration")
	}
	if settings.MaxConnections > 0 {
		configuration.MaxConns = settings.MaxConnections
	}
	configuration.ConnConfig.Tracer = tracer
	// pgx normally interrupts a canceled query by closing its connection. A
	// PostgreSQL cancel request keeps a responsive connection usable so an
	// atomic command can roll back and retain its exact failure response while
	// it still owns the session advisory lock. The network deadline remains the
	// bounded fallback when PostgreSQL cannot acknowledge cancellation.
	configuration.ConnConfig.BuildContextWatcherHandler = func(connection *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{
			Conn:          connection,
			DeadlineDelay: postgresCancelFallbackDelay,
		}
	}
	configuration.MaxConnLifetime = settings.MaxConnLifetime
	configuration.MaxConnIdleTime = settings.MaxConnIdleTime
	configuration.HealthCheckPeriod = settings.HealthCheckPeriod
	if requireReadWrite {
		configuration.ConnConfig.ValidateConnect = composeConnectValidators(validateRuntimePooler, composeConnectValidators(pgconn.ValidateConnectTargetSessionAttrsReadWrite, configuration.ConnConfig.ValidateConnect))
	}
	configuration.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		connection.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
		_, err := connection.Exec(ctx, `SET TIME ZONE 'UTC'`)
		return err
	}
	return configuration, nil
}

// validateRuntimePooler runs before SQL admission on every physical runtime
// connection. Session locks span several short transactions in command execution.
// Missing status permits direct PostgreSQL; it cannot qualify an older pooler.
func validateRuntimePooler(_ context.Context, connection *pgconn.PgConn) error {
	return validateRuntimePoolMode(connection.ParameterStatus("pgbouncer.pool_mode"))
}

func validateRuntimePoolMode(mode string) error {
	switch mode {
	case "", "session":
		return nil
	default:
		return safeerr.New("PostgreSQL runtime requires session pooling")
	}
}

// composeConnectValidators runs mandatory session admission before configured
// checks so DSN options cannot replace the runtime writer requirement.
func composeConnectValidators(required, configured pgconn.ValidateConnectFunc) pgconn.ValidateConnectFunc {
	return func(ctx context.Context, connection *pgconn.PgConn) error {
		if required != nil {
			if err := required(ctx, connection); err != nil {
				return err
			}
		}
		if configured != nil {
			return configured(ctx, connection)
		}
		return nil
	}
}

// validatePostgresVersion accepts only the supported major while leaving
// compatible patch revisions unrestricted.
func validatePostgresVersion(serverVersionNumber int) error {
	major := serverVersionNumber / 10000
	if major != supportedPostgresMajor {
		return fmt.Errorf("PostgreSQL major version %d is required; server reports major version %d (server_version_num %d)", supportedPostgresMajor, major, serverVersionNumber)
	}
	return nil
}

type queryTraceKey struct{}

type queryTrace struct {
	end       func(string)
	operation string
	startedAt time.Time
}

type queryTracer struct {
	observer QueryObserver
	now      func() time.Time
}

// TraceQueryStart reduces SQL to a fixed statement class before retaining span and
// timing state; raw SQL does not enter the stored context.
func (t queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	operation := "other"
	if fields := strings.Fields(data.SQL); len(fields) > 0 {
		operation = databaseStatementClass(fields[0])
	}
	ctx, end := catalog.StartTrace(ctx, "SQL "+operation, catalog.TraceClient, "")
	return context.WithValue(ctx, queryTraceKey{}, queryTrace{operation: operation, startedAt: t.now(), end: end})
}

// TraceQueryEnd records a fixed outcome and nonnegative duration, passing the
// transient error only through the bounded observer contract.
func (t queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	trace, _ := ctx.Value(queryTraceKey{}).(queryTrace)
	if trace.end != nil {
		outcome := "ok"
		if data.Err != nil {
			outcome = "error"
		}
		trace.end(outcome)
	}
	if trace.operation == "" {
		trace.operation = "other"
	}
	if trace.startedAt.IsZero() {
		trace.startedAt = t.now()
	}
	t.observer.ObserveDatabase(trace.operation, data.Err, max(0, t.now().Sub(trace.startedAt)))
}

// databaseStatementClass maps recognized SQL leading keywords to the metric
// vocabulary and collapses unknown values to other.
func databaseStatementClass(value string) string {
	switch strings.ToLower(value) {
	case "select", "insert", "update", "delete", "with", "begin", "commit", "rollback", "copy":
		return strings.ToLower(value)
	default:
		return "other"
	}
}

// NewPostgresRepository wraps a nonnil pool; callers retain responsibility for
// startup admission checks and pool shutdown.
func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &PostgresRepository{pool: pool}, nil
}

// Ping reports whether this repository can safely serve runtime work, not only
// whether PostgreSQL is reachable.
func (p *PostgresRepository) Ping(ctx context.Context) error {
	return checkPostgresReadiness(ctx, p.pool)
}

// checkPostgresReadiness requires a writable session and preserves query errors.
func checkPostgresReadiness(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) error {
	var readWrite bool
	if err := query.QueryRow(ctx, `SELECT current_setting('transaction_read_only') = 'off'`).Scan(&readWrite); err != nil {
		return catalog.Wrap(catalog.CodeDependencyUnavailable, "check PostgreSQL readiness", err)
	}
	if !readWrite {
		return catalog.E(catalog.CodeDependencyUnavailable, "PostgreSQL session does not accept writes")
	}
	return nil
}

// withTenantTx joins a matching atomic command or establishes and commits an
// isolated tenant transaction, rolling back on operation failure.
func withTenantTx[T any](ctx context.Context, pool *pgxpool.Pool, tenantID string, options pgx.TxOptions, operation func(pgx.Tx) (T, error)) (T, error) {
	var zero T
	command := commandTransactionFrom(ctx)
	if command != nil {
		if err := command.validate(pool, tenantID); err != nil {
			return zero, err
		}
		var result T
		err := command.run(ctx, options, func(tx pgx.Tx) error {
			var operationErr error
			result, operationErr = operation(tx)
			return operationErr
		})
		if err != nil {
			return zero, withDatabaseContextError(ctx, err)
		}
		return result, nil
	}
	return withStandaloneTenantTx(ctx, pool, tenantID, options, operation)
}

// withStandaloneTenantTx owns one tenant transaction outside an atomic command.
// Results are returned only after commit; cleanup also runs after a panic.
func withStandaloneTenantTx[T any](ctx context.Context, connection commandConnection, tenantID string, options pgx.TxOptions, operation func(pgx.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := connection.BeginTx(ctx, options)
	if err != nil {
		return zero, withDatabaseContextError(ctx, catalog.Wrap(catalog.CodeDependencyUnavailable, "begin database transaction", err))
	}
	defer rollbackForCleanup(ctx, tx)()
	if err = setTenantContext(ctx, tx, tenantID); err != nil {
		return zero, withDatabaseContextError(ctx, err)
	}
	if err := admitRecordedPermissions(ctx, tx, options.AccessMode == pgx.ReadOnly, nil); err != nil {
		return zero, err
	}
	result, err := operation(tx)
	if err != nil {
		return zero, withDatabaseContextError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, withDatabaseContextError(ctx, translateError("commit database transaction", err))
	}
	return result, nil
}

// PostgreSQL reports a native query-canceled error when its cancel protocol
// preserves the connection. Keep the caller's standard context cause in that
// error chain so domain code can continue to use errors.Is for cancellation.
func withDatabaseContextError(ctx context.Context, err error) error {
	if err == nil || ctx.Err() == nil || errors.Is(err, ctx.Err()) {
		return err
	}
	return errors.Join(err, ctx.Err())
}

// withTenantRead requests read-only tenant access while retaining an enclosing
// command transaction's sharing and rollback contract.
func withTenantRead[T any](ctx context.Context, pool *pgxpool.Pool, tenantID string, operation func(pgx.Tx) (T, error)) (T, error) {
	options := pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}
	if command := commandTransactionFrom(ctx); command != nil {
		command.mu.Lock()
		active := command.tx != nil
		command.mu.Unlock()
		if active {
			options.IsoLevel = ""
		}
	}
	return withTenantTx(ctx, pool, tenantID, options, operation)
}

// lockTenantState acquires the tenant's transaction-scoped advisory lock and
// proves tenant visibility before protected transitions.
func lockTenantState(ctx context.Context, tx pgx.Tx, tenantID, operation string) error {
	if err := acquireCatalogStateLock(ctx, tx, "tenant", tenantID); err != nil {
		return translateError(operation, err)
	}
	var lockedTenantID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1`, tenantID).Scan(&lockedTenantID); err != nil {
		return translateError(operation, err)
	}
	return nil
}

// acquireCatalogStateLock obtains a transaction-lifetime lock for a code-owned
// state scope and identifier.
func acquireCatalogStateLock(ctx context.Context, tx pgx.Tx, scope, identifier string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, catalogStateLockKey(scope, identifier))
	return err
}

// catalogStateLockKey separates state scopes and identifiers into a deterministic
// nonnegative advisory-lock key.
func catalogStateLockKey(scope, identifier string) int64 {
	digest := sha256.Sum256([]byte(scope + "\x00" + identifier))
	return int64(binary.BigEndian.Uint64(digest[:8]) >> 1)
}

type evidenceExpectation struct {
	tenantID   string
	targetType string
	targetID   string
	actions    []string
}

// expectEvidence declares the exact tenant, target and allowed actions required
// for one mutation's evidence.
func expectEvidence(tenantID, targetType, targetID string, actions ...string) evidenceExpectation {
	return evidenceExpectation{tenantID: tenantID, targetType: targetType, targetID: targetID, actions: actions}
}

// insertEvidence requires one exact mutation binding and appends matching
// audit/outbox rows in the caller's transaction.
func insertEvidence(ctx context.Context, tx pgx.Tx, event catalog.AuditEvent, outbox catalog.OutboxEvent, expected ...evidenceExpectation) error {
	if len(expected) != 1 {
		return catalog.E(catalog.CodeInternal, "version-1 mutation evidence requires one exact binding")
	}
	if err := validateEvidencePair(event, outbox, expected...); err != nil {
		return err
	}
	if err := insertAuditEvent(ctx, tx, event); err != nil {
		return err
	}
	return insertOutboxEvent(ctx, tx, outbox)
}

// validateEvidencePair checks current audit shape and exact tenant, target,
// action, time and public reference agreement before writing evidence.
func validateEvidencePair(event catalog.AuditEvent, outbox catalog.OutboxEvent, expected ...evidenceExpectation) error {
	if err := auditpkg.ValidateNewRecord(event); err != nil {
		return catalog.Wrap(catalog.CodeInternal, "validate mutation audit evidence", err)
	}
	if len(expected) > 1 {
		return catalog.E(catalog.CodeInternal, "audit evidence has ambiguous mutation binding")
	}
	if len(expected) == 1 {
		binding := expected[0]
		actionMatches := false
		for _, action := range binding.actions {
			if event.Action == action {
				actionMatches = true
				break
			}
		}
		if event.TenantID != binding.tenantID || event.TargetType != binding.targetType || event.TargetID != binding.targetID || !actionMatches {
			return catalog.E(catalog.CodeInternal, "audit evidence does not match its Catalog mutation")
		}
	}
	if event.TenantID != outbox.TenantID || event.TargetID != outbox.Subject || outbox.Type != catalog.CanonicalEventType(event.Action) || outbox.Internal || !outbox.CreatedAt.Equal(event.At) || outbox.PublishedAt != nil && outbox.PublishedAt.Before(outbox.CreatedAt) || outbox.LeaseOwner != "" || outbox.LeaseExpiresAt != nil {
		return catalog.E(catalog.CodeInternal, "audit and outbox evidence do not describe the same tenant event")
	}
	if err := events.ValidateReferenceData(outbox.Data, event.Action, event.TargetType, event.TargetID, event.AfterDigest, ""); err != nil {
		return catalog.E(catalog.CodeInternal, "audit and outbox evidence do not describe the same tenant event")
	}
	return nil
}

// insertOutboxEvent persists a classified event and current causal context in the
// originating mutation's transaction.
func insertOutboxEvent(ctx context.Context, tx pgx.Tx, outbox catalog.OutboxEvent) error {
	if _, err := tx.Exec(ctx, `
    INSERT INTO outbox_events(id, tenant_id, event_type, subject, data, is_internal, created_at, published_at,causal_trace)
    VALUES ($1,$2,$3,$4,$5,$6,$7,$8,nullif($9,''))`, outbox.ID, outbox.TenantID, outbox.Type, outbox.Subject, outbox.Data, outbox.Internal, outbox.CreatedAt, outbox.PublishedAt, catalog.CausalTrace(ctx)); err != nil {
		return translateError("insert outbox event", err)
	}
	return nil
}

// insertAuditEvent validates current audit format and encodes declared details
// before insertion in the caller's transaction.
func insertAuditEvent(ctx context.Context, tx pgx.Tx, event catalog.AuditEvent) error {
	if len(identity.RecordedPermissionChecks(ctx)) > 0 {
		event.Details = maps.Clone(event.Details)
		if event.Details == nil {
			event.Details = map[string]any{}
		}
		event.Details["authorizationDigest"] = identity.DecisionDigest(ctx)
	}
	if err := auditpkg.ValidateNewRecord(event); err != nil {
		return catalog.Wrap(catalog.CodeInternal, "validate audit event", err)
	}
	details, err := json.Marshal(event.Details)
	if err != nil {
		return catalog.Wrap(catalog.CodeInternal, "marshal audit details", err)
	}
	if _, err := tx.Exec(ctx, `
    INSERT INTO audit_events(format_version,id,tenant_id,actor,action,target_type,target_id,reason,request_id,before_digest,after_digest,details,occurred_at)
    VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		event.FormatVersion, event.ID, event.TenantID, event.Actor, event.Action, event.TargetType, event.TargetID, event.Reason, event.RequestID, event.BeforeDigest, event.AfterDigest, details, event.At); err != nil {
		return translateError("insert audit event", err)
	}
	return nil
}

// translateError maps PostgreSQL absence, constraints, concurrency and
// authorization failures to domain codes while retaining internal causes.
func translateError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.E(catalog.CodeNotFound, operation+": object was not found")
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return catalog.Wrap(catalog.CodeConflict, operation+": unique constraint failed", err)
		case "23503", "23514", "22P02":
			return catalog.Wrap(catalog.CodeInvalidArgument, operation+": relational constraint failed", err)
		case "22003":
			return catalog.Wrap(catalog.CodeInvalidArgument, operation+": numeric value is outside the supported range", err)
		case "40001", "40P01":
			return catalog.Wrap(catalog.CodeConflict, operation+": concurrent transaction conflict", err)
		case "42501":
			return catalog.Wrap(catalog.CodeForbidden, operation+": tenant policy denied access", err)
		}
	}
	return catalog.Wrap(catalog.CodeDependencyUnavailable, operation, err)
}

// marshalJSON classifies failure to encode database-owned typed values as an
// internal error.
func marshalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, catalog.Wrap(catalog.CodeInternal, "marshal database JSON", err)
	}
	return raw, nil
}

// unmarshalJSON decodes retained JSON; absent or JSON-null optional columns leave
// the typed target unchanged.
func unmarshalJSON(raw []byte, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return catalog.Wrap(catalog.CodeInternal, "decode database JSON", err)
	}
	return nil
}

type versionBundle struct {
	version  catalog.ResourceVersion
	revision catalog.VersionRevision
	evidence catalog.EvidenceSet
}

type listingRevisionBundle struct {
	listing  catalog.CatalogListing
	revision catalog.ListingRevision
}

type decisionBundle struct {
	submission catalog.ReviewSubmission
	version    catalog.ResourceVersion
}

type publicationBundle struct {
	listing catalog.CatalogListing
	release catalog.ListingRelease
	prior   *catalog.ListingRelease
}
