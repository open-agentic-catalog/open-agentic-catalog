// Command transaction sharing binds repository work to the replay lock connection
// and starts the interactive transaction on the first write.

package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const commandTransactionCleanupLimit = 5 * time.Second

type commandTransactionKey struct{}

type commandConnection interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// commandTransaction owns the connection carrying the command advisory lock.
// Read preparation uses short transactions on that connection. The first
// write starts one transaction that remains open until the HTTP response has
// been encoded and added to the idempotency row.
type commandTransaction struct {
	authorizationShared    bool
	authorizationExclusive bool
	mu                     sync.Mutex
	pool                   *pgxpool.Pool
	conn                   commandConnection
	tenantID               string
	tx                     pgx.Tx
	options                pgx.TxOptions
	failed                 error
	closed                 bool
	cleanupOnce            sync.Once
	cleanupDeadline        time.Time
}

// abort records failure and rolls back any active command transaction under the
// command mutex; an already completed command is unchanged.
func (c *commandTransaction) abort(ctx context.Context, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if err == nil {
		err = catalog.E(catalog.CodeInternal, "command was aborted")
	}
	c.failLocked(ctx, err)
}

// withCommandTransaction attaches the connection-bound command coordinator so
// repository operations can share its tenant transaction.
func withCommandTransaction(ctx context.Context, command *commandTransaction) context.Context {
	return context.WithValue(ctx, commandTransactionKey{}, command)
}

// commandTransactionFrom returns the request's command coordinator, or nil for
// ordinary repository calls.
func commandTransactionFrom(ctx context.Context) *commandTransaction {
	command, _ := ctx.Value(commandTransactionKey{}).(*commandTransaction)
	return command
}

// validate rejects attempts to join the command through another pool or tenant
// before any transaction work is performed.
func (c *commandTransaction) validate(pool *pgxpool.Pool, tenantID string) error {
	if c.pool != pool {
		return catalog.E(catalog.CodeInternal, "command attempted to use an unrelated database pool")
	}
	if !strings.EqualFold(c.tenantID, tenantID) {
		return catalog.E(catalog.CodeForbidden, "command attempted to cross its tenant boundary")
	}
	return nil
}

// run serializes repository operations, keeping preparatory reads short and
// sharing the first write transaction through completion. Shared-transaction
// failures and recovered panics poison subsequent work; ordinary preparatory
// read errors are returned without poisoning the coordinator.
func (c *commandTransaction) run(ctx context.Context, options pgx.TxOptions, operation func(pgx.Tx) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() {
		if recovered := recover(); recovered != nil {
			c.failLocked(ctx, catalog.E(catalog.CodeInternal, "command transaction panicked"))
			panic(recovered)
		}
	}()
	if c.failed != nil {
		return c.failed
	}
	if c.closed {
		return catalog.E(catalog.CodeInternal, "command transaction is already complete")
	}
	if options.AccessMode == pgx.ReadOnly && c.tx == nil {
		return c.runShort(ctx, options, operation)
	}
	if c.tx == nil {
		tx, err := c.conn.BeginTx(ctx, options)
		if err != nil {
			c.failed = translateError("begin command transaction", err)
			return c.failed
		}
		c.tx = tx
		c.options = options
		if err := setTenantContext(ctx, tx, c.tenantID); err != nil {
			c.failLocked(ctx, err)
			return err
		}
	} else if !compatibleCommandOptions(c.options, options) {
		err := catalog.E(catalog.CodeInternal, "command requested incompatible database transaction options")
		c.failLocked(ctx, err)
		return err
	}
	if err := admitRecordedPermissions(ctx, c.tx, options.AccessMode == pgx.ReadOnly, c); err != nil {
		c.failLocked(ctx, err)
		return err
	}
	if err := operation(c.tx); err != nil {
		c.failLocked(ctx, err)
		return err
	}
	return nil
}

// runShort commits one tenant-scoped operation on the advisory-lock connection
// without retaining a shared transaction. Callers must serialize connection use.
func (c *commandTransaction) runShort(ctx context.Context, options pgx.TxOptions, operation func(pgx.Tx) error) error {
	tx, err := c.conn.BeginTx(ctx, options)
	if err != nil {
		return translateError("begin database transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := c.cleanupContext(ctx)
			defer cancel()
			_ = tx.Rollback(cleanupCtx)
		}
	}()
	if err := setTenantContext(ctx, tx, c.tenantID); err != nil {
		return err
	}
	if err := admitRecordedPermissions(ctx, tx, options.AccessMode == pgx.ReadOnly, nil); err != nil {
		return err
	}
	if err := operation(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return translateError("commit database transaction", err)
	}
	committed = true
	return nil
}

// failLocked records the failure and rolls back the shared transaction within the
// command cleanup deadline; the caller must hold c.mu.
func (c *commandTransaction) failLocked(parent context.Context, err error) {
	c.failed = err
	if c.tx == nil {
		return
	}
	cleanupCtx, cancel := c.cleanupContext(parent)
	defer cancel()
	_ = c.tx.Rollback(cleanupCtx)
	c.tx = nil
}

// complete atomically retains the encoded response with successful mutations,
// refusing a success response after a retained shared-transaction failure.
// Preparatory read errors do not set that failure state. Rolled-back failures
// retain only their response in a fresh transaction.
func (c *commandTransaction) complete(parent context.Context, responseStatus int, operation func(context.Context, pgx.Tx) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return catalog.E(catalog.CodeInternal, "command transaction is already complete")
	}
	c.closed = true
	// Check under the same lock as completion: a swallowed shared-transaction failure
	// must never turn a rolled-back command into a retained success response.
	if c.failed != nil && responseStatus >= 200 && responseStatus < 400 {
		return catalog.Wrap(catalog.CodeInternal, "command transaction failed before successful response", c.failed)
	}
	ctx, cancel := c.cleanupContext(parent)
	defer cancel()
	if c.failed != nil {
		// A failed repository operation rolled the command transaction back.
		// Retaining the failure response in a fresh transaction is safe because
		// no command mutation from this attempt can have committed.
		return c.runShortUnlocked(ctx, pgx.TxOptions{}, func(tx pgx.Tx) error { return operation(ctx, tx) })
	}
	if c.tx == nil {
		return c.runShortUnlocked(ctx, pgx.TxOptions{}, func(tx pgx.Tx) error { return operation(ctx, tx) })
	}
	if err := operation(ctx, c.tx); err != nil {
		_ = c.tx.Rollback(ctx)
		c.tx = nil
		return err
	}
	if err := c.tx.Commit(ctx); err != nil {
		c.tx = nil
		return translateError("commit command and idempotent response", err)
	}
	c.tx = nil
	return nil
}

// runShortUnlocked completes one tenant transaction using the supplied cleanup
// context; it does not acquire c.mu and is called while completion owns the mutex.
func (c *commandTransaction) runShortUnlocked(ctx context.Context, options pgx.TxOptions, operation func(pgx.Tx) error) error {
	tx, err := c.conn.BeginTx(ctx, options)
	if err != nil {
		return translateError("begin database transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := setTenantContext(ctx, tx, c.tenantID); err != nil {
		return err
	}
	if err := operation(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return translateError("commit database transaction", err)
	}
	return nil
}

// cleanupContext starts the command's one cancellation-independent cleanup
// window on first use. Completion, rollback, advisory unlock, and fallback
// connection close all consume this same absolute deadline.
func (c *commandTransaction) cleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	c.cleanupOnce.Do(func() {
		c.cleanupDeadline = time.Now().Add(commandTransactionCleanupLimit)
	})
	return context.WithDeadline(context.WithoutCancel(parent), c.cleanupDeadline)
}

// rollbackForCleanup returns a deferred rollback with a bounded context
// independent of request cancellation; rollback errors are deliberately ignored
// during cleanup.
func rollbackForCleanup(parent context.Context, tx pgx.Tx) func() {
	return func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), commandTransactionCleanupLimit)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}
}

// compatibleCommandOptions allows unspecified options and compatible reads to join
// a write transaction but rejects requested isolation or deferrability changes.
func compatibleCommandOptions(active, requested pgx.TxOptions) bool {
	if requested.AccessMode == pgx.ReadOnly {
		return (requested.IsoLevel == "" || requested.IsoLevel == active.IsoLevel) &&
			(requested.DeferrableMode == "" || requested.DeferrableMode == active.DeferrableMode)
	}
	return (requested.IsoLevel == "" || requested.IsoLevel == active.IsoLevel) &&
		(requested.AccessMode == "" || requested.AccessMode == active.AccessMode) &&
		(requested.DeferrableMode == "" || requested.DeferrableMode == active.DeferrableMode)
}

// setTenantContext installs transaction-local tenant and actor values for RLS and
// attribution, rejecting disagreement with an authenticated actor's tenant.
func setTenantContext(ctx context.Context, tx pgx.Tx, tenantID string) error {
	actorSubject, membershipID := "", ""
	if actor, ok := identity.FromContext(ctx); ok {
		if !strings.EqualFold(actor.TenantID, tenantID) {
			return catalog.E(catalog.CodeForbidden, "database transaction attempted to cross the authenticated tenant boundary")
		}
		actorSubject = actor.Subject
		membershipID = actor.MembershipID
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),set_config('app.actor_subject',$2,true),set_config('app.membership_id',$3,true)`, tenantID, actorSubject, membershipID); err != nil {
		return translateError("set tenant context", err)
	}
	return nil
}

// releaseCommandLock gives advisory unlock and fallback connection closure one
// bounded cancellation-independent cleanup window.
func releaseCommandLock(parent context.Context, connection *pgxpool.Conn, lockKey int64) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), commandTransactionCleanupLimit)
	defer cancel()
	return releaseCommandLockWithin(cleanupCtx, connection, lockKey)
}

// releaseCommandLockWithin unlocks on the owning session and closes that
// connection if unlock fails or ownership cannot be confirmed, preventing
// uncertain lock state from returning to the pool.
func releaseCommandLockWithin(cleanupCtx context.Context, connection *pgxpool.Conn, lockKey int64) error {
	var unlocked bool
	if err := connection.QueryRow(cleanupCtx, `SELECT pg_advisory_unlock($1)`, lockKey).Scan(&unlocked); err != nil {
		unlockErr := fmt.Errorf("release command advisory lock: %w", err)
		if closeErr := connection.Conn().Close(cleanupCtx); closeErr != nil {
			return errors.Join(unlockErr, fmt.Errorf("close command lock connection: %w", closeErr))
		}
		return unlockErr
	}
	if unlocked {
		return nil
	}
	unlockErr := errors.New("release command advisory lock: lock was not held by its connection")
	if closeErr := connection.Conn().Close(cleanupCtx); closeErr != nil {
		return errors.Join(unlockErr, fmt.Errorf("close command lock connection: %w", closeErr))
	}
	return unlockErr
}
