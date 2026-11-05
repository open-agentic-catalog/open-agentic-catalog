// Reservation repair separates bounded inspection, digest-bound plans and guarded
// application that excludes actively executing commands.

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
)

const idempotencyRepairPlanVersion = "catalog.idempotency-repair/v2"

// IdempotencyRepairPlan is a machine-readable, tenant- and target-bounded
// decision. It never exposes the client-supplied idempotency key.
type IdempotencyRepairPlan struct {
	Version              string    `json:"version"`
	Action               string    `json:"action"`
	TenantID             string    `json:"tenantId"`
	ReservationID        string    `json:"reservationId"`
	Actor                string    `json:"actor"`
	Route                string    `json:"route"`
	RequestDigest        string    `json:"requestDigest"`
	AuthorizationDigest  string    `json:"authorizationDigest,omitempty"`
	IdempotencyKeyDigest string    `json:"idempotencyKeyDigest"`
	ProtocolVersion      int       `json:"protocolVersion"`
	State                string    `json:"state"`
	Active               bool      `json:"active"`
	Eligible             bool      `json:"eligible"`
	Reason               string    `json:"reason"`
	CreatedAt            time.Time `json:"createdAt"`
	StartedAt            time.Time `json:"startedAt"`
	ExpiresAt            time.Time `json:"expiresAt"`
	PlanDigest           string    `json:"planDigest"`
	Refusal              string    `json:"refusal,omitempty"`
}

// IdempotencyRepairResult records the applied transition and its evidence.
type IdempotencyRepairResult struct {
	Plan          IdempotencyRepairPlan `json:"plan"`
	Applied       bool                  `json:"applied"`
	DatabaseActor string                `json:"databaseActor"`
	AuditEventID  string                `json:"auditEventId"`
	ReleasedAt    time.Time             `json:"releasedAt"`
}

// PendingIdempotencyReservation is the safe operator inventory used to choose
// one explicit repair target. Client keys and response bodies are omitted.
type PendingIdempotencyReservation struct {
	TenantID             string    `json:"tenantId"`
	ReservationID        string    `json:"reservationId"`
	Actor                string    `json:"actor"`
	Route                string    `json:"route"`
	RequestDigest        string    `json:"requestDigest"`
	AuthorizationDigest  string    `json:"authorizationDigest,omitempty"`
	IdempotencyKeyDigest string    `json:"idempotencyKeyDigest"`
	ProtocolVersion      int       `json:"protocolVersion"`
	State                string    `json:"state"`
	Active               bool      `json:"active"`
	Repairable           bool      `json:"repairable"`
	CreatedAt            time.Time `json:"createdAt"`
	StartedAt            time.Time `json:"startedAt"`
	ExpiresAt            time.Time `json:"expiresAt"`
}

// PendingIdempotencyReport is tenant-bounded and deliberately capped.
type PendingIdempotencyReport struct {
	TenantID string                          `json:"tenantId"`
	Items    []PendingIdempotencyReservation `json:"items"`
	Count    int                             `json:"count"`
}

type idempotencyRepairRecord struct {
	scope               idempotency.Scope
	reservationID       string
	requestDigest       string
	authorizationDigest *string
	protocol            int
	responseStatus      *int
	createdAt           time.Time
	startedAt           time.Time
	expiresAt           time.Time
	releasedAt          *time.Time
}

// ListPendingIdempotencyReservations returns safe targets for the focused
// repair workflow. State is rechecked under the same advisory-lock protocol
// used by plan and apply.
func ListPendingIdempotencyReservations(ctx context.Context, pool *pgxpool.Pool, tenantID string, limit int) (PendingIdempotencyReport, error) {
	if !uuidText(tenantID) {
		return PendingIdempotencyReport{}, errors.New("pending idempotency inventory requires an explicit tenant UUID")
	}
	if limit < 1 || limit > 100 {
		return PendingIdempotencyReport{}, errors.New("pending idempotency inventory limit must be between 1 and 100")
	}
	if _, err := verifyRepairRole(ctx, pool); err != nil {
		return PendingIdempotencyReport{}, err
	}
	reservationIDs, err := withTenantRead(ctx, pool, tenantID, func(tx pgx.Tx) ([]string, error) {
		rows, err := tx.Query(ctx, `
      SELECT reservation_id::text
        FROM idempotency_keys
       WHERE tenant_id=$1 AND released_at IS NULL AND (
         protocol_version=2 AND atomic_response_status IS NULL
       )
       ORDER BY started_at,reservation_id LIMIT $2`, tenantID, limit)
		if err != nil {
			return nil, fmt.Errorf("list pending idempotency reservations: %w", err)
		}
		defer rows.Close()
		ids := make([]string, 0)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("scan pending idempotency reservation: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate pending idempotency reservations: %w", err)
		}
		return ids, nil
	})
	if err != nil {
		return PendingIdempotencyReport{}, err
	}
	report := PendingIdempotencyReport{TenantID: tenantID, Items: make([]PendingIdempotencyReservation, 0, len(reservationIDs))}
	for _, reservationID := range reservationIDs {
		plan, err := PlanIdempotencyRepair(ctx, pool, tenantID, reservationID, "pending reservation inventory")
		if err != nil {
			return PendingIdempotencyReport{}, err
		}
		if plan.State == "completed" || plan.State == "released" {
			continue
		}
		report.Items = append(report.Items, PendingIdempotencyReservation{
			TenantID: plan.TenantID, ReservationID: plan.ReservationID, Actor: plan.Actor, Route: plan.Route,
			RequestDigest: plan.RequestDigest, AuthorizationDigest: plan.AuthorizationDigest, IdempotencyKeyDigest: plan.IdempotencyKeyDigest,
			ProtocolVersion: plan.ProtocolVersion, State: plan.State, Active: plan.Active, Repairable: plan.Eligible,
			CreatedAt: plan.CreatedAt, StartedAt: plan.StartedAt, ExpiresAt: plan.ExpiresAt,
		})
	}
	report.Count = len(report.Items)
	return report, nil
}

// PlanIdempotencyRepair inspects one reservation without changing it. Only a
// protocol-v2 pending row whose advisory lock is free can be eligible.
func PlanIdempotencyRepair(ctx context.Context, pool *pgxpool.Pool, tenantID, reservationID, reason string) (plan IdempotencyRepairPlan, resultErr error) {
	if err := validateRepairInput(tenantID, reservationID, reason); err != nil {
		return IdempotencyRepairPlan{}, err
	}
	reason = strings.TrimSpace(reason)
	if _, err := verifyRepairRole(ctx, pool); err != nil {
		return IdempotencyRepairPlan{}, err
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return IdempotencyRepairPlan{}, fmt.Errorf("acquire idempotency repair connection: %w", err)
	}
	defer connection.Release()
	record, err := readRepairRecord(ctx, pool, connection, tenantID, reservationID)
	if err != nil {
		return IdempotencyRepairPlan{}, err
	}
	lockKey := idempotency.AdvisoryLockKey(record.scope)
	var locked bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&locked); err != nil {
		return IdempotencyRepairPlan{}, fmt.Errorf("inspect command advisory lock: %w", err)
	}
	if !locked {
		return makeIdempotencyRepairPlan(record, reason, true)
	}
	defer func() {
		if err := releaseCommandLock(ctx, connection, lockKey); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	record, err = readRepairRecord(ctx, pool, connection, tenantID, reservationID)
	if err != nil {
		return IdempotencyRepairPlan{}, err
	}
	return makeIdempotencyRepairPlan(record, reason, false)
}

// ApplyIdempotencyRepair releases exactly the reviewed plan. It holds the command
// advisory lock through the transaction committing the row transition and audit
// event. Repair has no post-commit consumer and creates no outbox record.
func ApplyIdempotencyRepair(ctx context.Context, pool *pgxpool.Pool, tenantID, reservationID, reason, expectedPlanDigest string) (result IdempotencyRepairResult, resultErr error) {
	if err := validateRepairInput(tenantID, reservationID, reason); err != nil {
		return IdempotencyRepairResult{}, err
	}
	reason = strings.TrimSpace(reason)
	if !validSHA256Digest(expectedPlanDigest) {
		return IdempotencyRepairResult{}, errors.New("idempotency repair apply requires an exact sha256 plan digest")
	}
	databaseActor, err := verifyRepairRole(ctx, pool)
	if err != nil {
		return IdempotencyRepairResult{}, err
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return IdempotencyRepairResult{}, fmt.Errorf("acquire idempotency repair connection: %w", err)
	}
	defer connection.Release()
	record, err := readRepairRecord(ctx, pool, connection, tenantID, reservationID)
	if err != nil {
		return IdempotencyRepairResult{}, err
	}
	lockKey := idempotency.AdvisoryLockKey(record.scope)
	var locked bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&locked); err != nil {
		return IdempotencyRepairResult{}, fmt.Errorf("obtain command advisory lock for repair: %w", err)
	}
	if !locked {
		return IdempotencyRepairResult{}, errors.New("idempotency reservation is still owned by an active command")
	}
	defer func() {
		if err := releaseCommandLock(ctx, connection, lockKey); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()

	command := &commandTransaction{pool: pool, conn: connection, tenantID: tenantID}
	err = command.runShort(ctx, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var applyErr error
		result, applyErr = applyIdempotencyRepairTx(ctx, tx, tenantID, reservationID, reason, expectedPlanDigest, databaseActor)
		return applyErr
	})
	if err != nil {
		return IdempotencyRepairResult{}, err
	}
	return result, nil
}

// applyIdempotencyRepairTx applies validated, reason-normalized input under the
// caller's command advisory lock and authorized tenant transaction. The result
// is provisional until runShort commits; the caller retains cleanup ownership.
func applyIdempotencyRepairTx(ctx context.Context, tx pgx.Tx, tenantID, reservationID, reason, expectedPlanDigest, databaseActor string) (IdempotencyRepairResult, error) {
	record, err := scanRepairRecord(tx.QueryRow(ctx, `
      SELECT tenant_id::text,actor,route,idempotency_key,reservation_id::text,request_digest,
             authorization_digest,protocol_version,
             atomic_response_status,
             created_at,started_at,expires_at,released_at
        FROM idempotency_keys WHERE tenant_id=$1 AND reservation_id=$2 FOR UPDATE`, tenantID, reservationID))
	if err != nil {
		return IdempotencyRepairResult{}, err
	}
	plan, err := makeIdempotencyRepairPlan(record, reason, false)
	if err != nil {
		return IdempotencyRepairResult{}, err
	}
	if !plan.Eligible {
		return IdempotencyRepairResult{}, fmt.Errorf("idempotency reservation cannot be repaired: %s", plan.Refusal)
	}
	if plan.PlanDigest != expectedPlanDigest {
		return IdempotencyRepairResult{}, errors.New("idempotency repair plan changed; review a new dry-run plan")
	}

	var releasedAt time.Time
	updated, err := tx.Exec(ctx, `
      UPDATE idempotency_keys
         SET released_at=clock_timestamp(),repair_reason=$3,execution_token=NULL
       WHERE tenant_id=$1 AND reservation_id=$2 AND protocol_version=2
         AND atomic_response_status IS NULL AND released_at IS NULL`, tenantID, reservationID, reason)
	if err != nil {
		return IdempotencyRepairResult{}, fmt.Errorf("release interrupted idempotency reservation: %w", err)
	}
	if updated.RowsAffected() != 1 {
		return IdempotencyRepairResult{}, errors.New("idempotency reservation changed while applying its repair")
	}
	if err := tx.QueryRow(ctx, `SELECT released_at FROM idempotency_keys WHERE tenant_id=$1 AND reservation_id=$2`, tenantID, reservationID).Scan(&releasedAt); err != nil {
		return IdempotencyRepairResult{}, fmt.Errorf("read released idempotency reservation: %w", err)
	}

	auditEvent, err := newDatabaseAuditRecord(ctx, tx, tenantID, audit.ActionIdempotencyReservationReleased, "idempotency_reservation", reservationID, reason, "", map[string]any{"reservationId": reservationID, "protocolVersion": record.protocol}, releasedAt)
	if err != nil {
		return IdempotencyRepairResult{}, err
	}
	auditEvent.BeforeDigest = digestText("idempotency-reservation:pending")
	auditEvent.AfterDigest = digestText("idempotency-reservation:released")
	if err := insertAuditEvent(ctx, tx, auditEvent); err != nil {
		return IdempotencyRepairResult{}, fmt.Errorf("insert idempotency repair audit event: %w", err)
	}
	return IdempotencyRepairResult{Plan: plan, Applied: true, DatabaseActor: databaseActor, AuditEventID: auditEvent.ID, ReleasedAt: releasedAt}, nil
}

// readRepairRecord reads one explicit tenant/reservation target in a short
// read-only transaction on the supplied lock connection.
func readRepairRecord(ctx context.Context, pool *pgxpool.Pool, connection commandConnection, tenantID, reservationID string) (record idempotencyRepairRecord, err error) {
	command := &commandTransaction{pool: pool, conn: connection, tenantID: tenantID}
	options := pgx.TxOptions{AccessMode: pgx.ReadOnly}
	err = command.runShort(ctx, options, func(tx pgx.Tx) error {
		const query = `
      SELECT tenant_id::text,actor,route,idempotency_key,reservation_id::text,request_digest,
             authorization_digest,protocol_version,
             atomic_response_status,
             created_at,started_at,expires_at,released_at
        FROM idempotency_keys WHERE tenant_id=$1 AND reservation_id=$2`
		var err error
		record, err = scanRepairRecord(tx.QueryRow(ctx, query, tenantID, reservationID))
		return err
	})
	return record, err
}

// scanRepairRecord decodes repair-relevant reservation state and reports a missing
// target without falling back to another tenant.
func scanRepairRecord(row scanner) (idempotencyRepairRecord, error) {
	var record idempotencyRepairRecord
	if err := row.Scan(
		&record.scope.TenantID, &record.scope.Actor, &record.scope.Route, &record.scope.Key,
		&record.reservationID, &record.requestDigest, &record.authorizationDigest, &record.protocol, &record.responseStatus,
		&record.createdAt, &record.startedAt, &record.expiresAt, &record.releasedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return idempotencyRepairRecord{}, errors.New("tenant-scoped idempotency reservation was not found")
		}
		return idempotencyRepairRecord{}, fmt.Errorf("read idempotency repair target: %w", err)
	}
	return record, nil
}

// makeIdempotencyRepairPlan classifies retained response, prior release and live
// lock states before permitting interrupted-command repair. The plan exposes a key
// digest, never the raw idempotency key.
func makeIdempotencyRepairPlan(record idempotencyRepairRecord, reason string, active bool) (IdempotencyRepairPlan, error) {
	if record.protocol != atomicCommandProtocolVersion {
		return IdempotencyRepairPlan{}, errors.New("unsupported command reservation protocol")
	}
	plan := IdempotencyRepairPlan{
		Version: idempotencyRepairPlanVersion, Action: "release_interrupted_reservation",
		TenantID: record.scope.TenantID, ReservationID: record.reservationID, Actor: record.scope.Actor,
		Route: record.scope.Route, RequestDigest: record.requestDigest, AuthorizationDigest: stringValue(record.authorizationDigest), IdempotencyKeyDigest: digestText(record.scope.Key),
		ProtocolVersion: record.protocol, Active: active, Reason: strings.TrimSpace(reason), CreatedAt: record.createdAt.UTC(),
		StartedAt: record.startedAt.UTC(), ExpiresAt: record.expiresAt.UTC(),
	}
	switch {
	case record.responseStatus != nil:
		plan.State = "completed"
		plan.Refusal = "the reservation already has a retained response"
	case record.releasedAt != nil:
		plan.State = "released"
		plan.Refusal = "the reservation was already released"
	case active:
		plan.State = "active"
		plan.Refusal = "the command still owns its advisory lock"
	default:
		plan.State = "interrupted"
		plan.Eligible = true
	}
	digest, err := repairPlanDigest(plan)
	if err != nil {
		return IdempotencyRepairPlan{}, err
	}
	plan.PlanDigest = digest
	return plan, nil
}

// repairPlanDigest hashes the complete reviewable plan with its digest field
// cleared, binding apply to the inspected state and reason.
func repairPlanDigest(plan IdempotencyRepairPlan) (string, error) {
	plan.PlanDigest = ""
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("encode idempotency repair plan: %w", err)
	}
	return digestBytes(raw), nil
}

// digestText hashes exact UTF-8 bytes with the shared prefixed SHA-256
// representation.
func digestText(value string) string { return digestBytes([]byte(value)) }

// stringValue represents an absent nullable database string as empty text in
// repair projections.
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// digestBytes returns a lowercase hexadecimal SHA-256 digest prefixed with its
// algorithm name.
func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// validSHA256Digest accepts only the exact lowercase prefixed SHA-256 form used by
// command authorization and repair plans.
func validSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	payload := strings.TrimPrefix(value, "sha256:")
	if payload != strings.ToLower(payload) {
		return false
	}
	_, err := hex.DecodeString(payload)
	return err == nil
}

// validateRepairInput requires explicit UUID targets and an audit-valid repair
// reason before any operator database action.
func validateRepairInput(tenantID, reservationID, reason string) error {
	if !uuidText(tenantID) || !uuidText(reservationID) {
		return errors.New("idempotency repair requires explicit UUID tenant and reservation targets")
	}
	if _, err := audit.NormalizeReason(audit.ActionIdempotencyReservationReleased, reason); err != nil {
		return fmt.Errorf("idempotency repair reason is invalid: %w", err)
	}
	return nil
}

// uuidText accepts the hyphenated hexadecimal UUID text shape without imposing a
// version or variant; it does not trim input.
func uuidText(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		switch index {
		case 8, 13, 18, 23:
			if character != '-' {
				return false
			}
		default:
			digit := character >= '0' && character <= '9'
			lower := character >= 'a' && character <= 'f'
			upper := character >= 'A' && character <= 'F'
			if !digit && !lower && !upper {
				return false
			}
		}
	}
	return true
}

// verifyRepairRole requires the current database identity to own the idempotency
// table; ordinary runtime authority cannot authorize repair.
func verifyRepairRole(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	if pool == nil {
		return "", errors.New("PostgreSQL pool is required")
	}
	return verifyIdempotencyRepairAuthority(ctx, pool)
}

// verifyIdempotencyRepairAuthority checks the owner read without taking ownership
// of the caller's pool or session.
func verifyIdempotencyRepairAuthority(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (string, error) {
	var currentUser, tableOwner string
	err := query.QueryRow(ctx, `
    SELECT current_user,pg_get_userbyid(class.relowner)
      FROM pg_catalog.pg_class class
     WHERE class.oid=to_regclass('idempotency_keys')`).Scan(&currentUser, &tableOwner)
	if err != nil {
		return "", fmt.Errorf("inspect idempotency repair database authority: %w", err)
	}
	if currentUser != tableOwner {
		return "", fmt.Errorf("idempotency repair requires the operator-controlled schema owner; current role %q does not own the repair table", currentUser)
	}
	return currentUser, nil
}
