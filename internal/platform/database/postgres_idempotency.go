// Durable commands reserve a key under a session lock, join later writes to one
// transaction and atomically retain the HTTP response.

package database

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
)

const atomicCommandProtocolVersion = 2

// PostgresIdempotencyStore retains command reservations and exact responses across replicas and restarts.
type PostgresIdempotencyStore struct{ repository *PostgresRepository }

// NewPostgresIdempotencyStore creates a durable command replay store.
func NewPostgresIdempotencyStore(repository *PostgresRepository) (*PostgresIdempotencyStore, error) {
	if repository == nil {
		return nil, errors.New("PostgreSQL repository is required")
	}
	return &PostgresIdempotencyStore{repository: repository}, nil
}

// Execute serializes one command key and durably reserves it before execution.
// Outbound preparation stays outside a database transaction. The first tenant write starts a
// shared command transaction; completion adds the exact response and commits.
func (p *PostgresIdempotencyStore) Execute(ctx context.Context, scope idempotency.Scope, digest string, expiresAt time.Time, execute func(context.Context) idempotency.Response) (result idempotency.Execution, resultErr error) {
	if !validSHA256Digest(scope.AuthorizationDigest) {
		return idempotency.Execution{}, catalog.E(catalog.CodeInternal, "command authorization digest is invalid")
	}
	connection, err := p.repository.pool.Acquire(ctx)
	if err != nil {
		return idempotency.Execution{}, catalog.Wrap(catalog.CodeDependencyUnavailable, "acquire command lock connection", err)
	}
	defer connection.Release()
	lockKey := idempotency.AdvisoryLockKey(scope)
	var locked bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&locked); err != nil {
		return idempotency.Execution{}, catalog.Wrap(catalog.CodeDependencyUnavailable, "obtain command advisory lock", err)
	}
	if !locked {
		return idempotency.Execution{Pending: true}, nil
	}
	command := &commandTransaction{pool: p.repository.pool, conn: connection, tenantID: scope.TenantID}
	defer func() {
		cleanupCtx, cancel := command.cleanupContext(ctx)
		defer cancel()
		if err := releaseCommandLockWithin(cleanupCtx, connection, lockKey); err != nil {
			resultErr = errors.Join(resultErr, catalog.Wrap(catalog.CodeDependencyUnavailable, "release command advisory lock", err))
		}
	}()

	return p.executeAtomic(ctx, command, scope, digest, expiresAt, execute)
}

// executeAtomic hands a locked command from reservation through response capture.
// Incomplete execution is aborted before returning control to the lock owner.
func (p *PostgresIdempotencyStore) executeAtomic(ctx context.Context, command *commandTransaction, scope idempotency.Scope, digest string, expiresAt time.Time, execute func(context.Context) idempotency.Response) (idempotency.Execution, error) {
	commandCompleted := false
	defer func() {
		if commandCompleted {
			return
		}
		command.abort(ctx, catalog.E(catalog.CodeInternal, "atomic command exited before completion"))
	}()
	reservation, reservationID, executionToken, err := p.reserveAtomic(ctx, command, scope, digest, expiresAt)
	if err != nil {
		return idempotency.Execution{}, err
	}
	if reservation.Pending {
		return idempotency.Execution{ReservationID: reservationID, Pending: true}, nil
	}
	if !reservation.Acquired {
		return idempotency.Execution{ReservationID: reservationID, Replayed: true, Response: reservation.Response}, nil
	}

	commandContext := withCommandTransaction(ctx, command)
	commandContext = audit.WithCommandID(commandContext, reservationID)
	commandContext = idempotency.WithCommandAbort(commandContext, func(err error) { command.abort(ctx, err) })
	response := execute(commandContext)
	if response.Digest != digest {
		return idempotency.Execution{}, catalog.E(catalog.CodeInternal, "command response digest does not match its reservation")
	}
	authority, err := json.Marshal(response.Authority)
	if err != nil {
		return idempotency.Execution{}, catalog.Wrap(catalog.CodeInternal, "encode replay authority", err)
	}
	headers, err := json.Marshal(response.Headers)
	if err != nil {
		return idempotency.Execution{}, catalog.Wrap(catalog.CodeInternal, "encode idempotent response headers", err)
	}
	err = command.complete(ctx, response.Status, func(completionCtx context.Context, tx pgx.Tx) error {
		updated, updateErr := tx.Exec(completionCtx, `
      UPDATE idempotency_keys
         SET replay_authorization=$11,atomic_response_status=$8,atomic_response_headers=$9,atomic_response_body=$10,completed_at=clock_timestamp()
       WHERE tenant_id=$1 AND actor=$2 AND route=$3 AND idempotency_key=$4 AND request_digest=$5
         AND execution_token=$6 AND authorization_digest=$7
         AND protocol_version=2 AND atomic_response_status IS NULL AND released_at IS NULL`,
			scope.TenantID, scope.Actor, scope.Route, scope.Key, digest, executionToken, scope.AuthorizationDigest, response.Status, headers, response.Body, authority)
		if updateErr != nil {
			return translateError("complete atomic command response", updateErr)
		}
		if updated.RowsAffected() != 1 {
			return catalog.E(catalog.CodeConflict, "atomic command reservation is not owned by this request")
		}
		return nil
	})
	if err != nil {
		return idempotency.Execution{}, err
	}
	commandCompleted = true
	return idempotency.Execution{ReservationID: reservationID, Response: response}, nil
}

// reserveAtomic durably reserves or replays a command in a short transaction.
// Pending crashed commands require explicit repair; replay checks the retained
// request and current authorization binding.
func (p *PostgresIdempotencyStore) reserveAtomic(ctx context.Context, command *commandTransaction, scope idempotency.Scope, digest string, expiresAt time.Time) (reservation idempotency.Reservation, reservationID, executionToken string, err error) {
	err = command.runShort(ctx, pgx.TxOptions{}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
      INSERT INTO idempotency_keys(
        tenant_id,actor,route,idempotency_key,request_digest,expires_at,created_at,
        protocol_version,authorization_digest,execution_token,started_at
      ) VALUES ($1,$2,$3,$4,$5,$6,clock_timestamp(),2,$7,uuidv7(),clock_timestamp())
      ON CONFLICT DO NOTHING
      RETURNING reservation_id::text,execution_token::text`,
			scope.TenantID, scope.Actor, scope.Route, scope.Key, digest, expiresAt, scope.AuthorizationDigest).Scan(&reservationID, &executionToken)
		switch {
		case err == nil:
			reservation = idempotency.Reservation{Acquired: true}
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return translateError("reserve atomic command", err)
		}

		var storedDigest string
		var status *int
		var headers, body, authority []byte
		var expired bool
		var protocol int
		var storedAuthorizationDigest *string
		var releasedAt *time.Time
		err = tx.QueryRow(ctx, `
      SELECT reservation_id::text,request_digest,
             atomic_response_status,
             atomic_response_headers,
             atomic_response_body,
		     expires_at<=clock_timestamp(),
		     protocol_version,authorization_digest,released_at,replay_authorization
        FROM idempotency_keys
       WHERE tenant_id=$1 AND actor=$2 AND route=$3 AND idempotency_key=$4
       FOR UPDATE`, scope.TenantID, scope.Actor, scope.Route, scope.Key).Scan(
			&reservationID, &storedDigest, &status, &headers, &body, &expired, &protocol, &storedAuthorizationDigest, &releasedAt, &authority,
		)
		if err != nil {
			return translateError("read atomic command reservation", err)
		}

		if protocol != atomicCommandProtocolVersion {
			return catalog.E(catalog.CodeDependencyUnavailable, "unsupported command reservation protocol")
		}

		completed := status != nil
		if completed && expired {
			return p.restartCompletedAtomic(ctx, tx, scope, digest, expiresAt, &reservationID, &executionToken, &reservation)
		}
		if releasedAt != nil && expired {
			return p.restartReleasedAtomic(ctx, tx, scope, digest, expiresAt, false, &reservationID, &executionToken, &reservation)
		}
		if storedDigest != digest {
			return catalog.E(catalog.CodeIdempotencyKeyReused, "the Idempotency-Key was already used with another request")
		}
		if storedAuthorizationDigest == nil || *storedAuthorizationDigest != scope.AuthorizationDigest {
			return catalog.E(catalog.CodeForbidden, "current authorization does not match the idempotent command")
		}
		if completed {
			response := idempotency.Response{Digest: storedDigest, Status: *status, Body: append([]byte(nil), body...)}
			if len(authority) > 0 {
				if err := json.Unmarshal(authority, &response.Authority); err != nil {
					return catalog.Wrap(catalog.CodeInternal, "decode replay authority", err)
				}
			}
			if len(headers) > 0 {
				if err := json.Unmarshal(headers, &response.Headers); err != nil {
					return catalog.Wrap(catalog.CodeInternal, "decode idempotent response headers", err)
				}
			}
			reservation = idempotency.Reservation{Response: response}
			return nil
		}
		if releasedAt != nil {
			return p.restartReleasedAtomic(ctx, tx, scope, digest, expiresAt, true, &reservationID, &executionToken, &reservation)
		}
		// A pending v2 row with the advisory lock free is a crashed command and
		// must pass through reviewed repair.
		reservation = idempotency.Reservation{Pending: true}
		return nil
	})
	return reservation, reservationID, executionToken, err
}

// restartCompletedAtomic replaces only an expired completed reservation with fresh
// reservation and execution identities inside the caller's transaction.
func (p *PostgresIdempotencyStore) restartCompletedAtomic(ctx context.Context, tx pgx.Tx, scope idempotency.Scope, digest string, expiresAt time.Time, reservationID, executionToken *string, reservation *idempotency.Reservation) error {
	err := tx.QueryRow(ctx, `
    UPDATE idempotency_keys
       SET request_digest=$5,
           atomic_response_status=NULL,atomic_response_headers=NULL,atomic_response_body=NULL,replay_authorization=NULL,
           expires_at=$6,protocol_version=2,authorization_digest=$7,reservation_id=uuidv7(),execution_token=uuidv7(),
           created_at=clock_timestamp(),started_at=clock_timestamp(),completed_at=NULL
     WHERE tenant_id=$1 AND actor=$2 AND route=$3 AND idempotency_key=$4
       AND completed_at IS NOT NULL AND expires_at<=clock_timestamp()
     RETURNING reservation_id::text,execution_token::text`, scope.TenantID, scope.Actor, scope.Route, scope.Key, digest, expiresAt, scope.AuthorizationDigest).Scan(reservationID, executionToken)
	if err != nil {
		return translateError("restart atomic command reservation", err)
	}
	*reservation = idempotency.Reservation{Acquired: true}
	return nil
}

// restartReleasedAtomic reacquires a repaired reservation with fresh fencing
// identities. Unexpired reuse is allowed only after the caller has checked both
// request and authorization digests.
func (p *PostgresIdempotencyStore) restartReleasedAtomic(ctx context.Context, tx pgx.Tx, scope idempotency.Scope, digest string, expiresAt time.Time, allowUnexpired bool, reservationID, executionToken *string, reservation *idempotency.Reservation) error {
	err := tx.QueryRow(ctx, `
    UPDATE idempotency_keys
       SET request_digest=$5,expires_at=$6,created_at=clock_timestamp(),authorization_digest=$7,
           reservation_id=uuidv7(),execution_token=uuidv7(),started_at=clock_timestamp(),released_at=NULL,repair_reason=NULL
     WHERE tenant_id=$1 AND actor=$2 AND route=$3 AND idempotency_key=$4
       AND protocol_version=2 AND atomic_response_status IS NULL AND released_at IS NOT NULL
       AND ($8 OR expires_at<=clock_timestamp())
     RETURNING reservation_id::text,execution_token::text`, scope.TenantID, scope.Actor, scope.Route, scope.Key, digest, expiresAt, scope.AuthorizationDigest, allowUnexpired).Scan(reservationID, executionToken)
	if err != nil {
		return translateError("restart released atomic command reservation", err)
	}
	*reservation = idempotency.Reservation{Acquired: true}
	return nil
}
