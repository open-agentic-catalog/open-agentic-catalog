package database

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
)

func TestIdempotencyRepairPlanClassifiesOnlyInactiveProtocolTwoPendingRows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.FixedZone("fixture", 3600))
	status := 201
	released := now.Add(time.Minute)
	base := idempotencyRepairRecord{
		scope: idempotency.Scope{
			TenantID: "00000000-0000-7000-8000-000000000001",
			Actor:    "operator@example.com",
			Route:    "POST /api/v1/resources",
			Key:      "do-not-expose-this-key",
		},
		reservationID:       "00000000-0000-7000-8000-000000000002",
		requestDigest:       "request-digest",
		authorizationDigest: stringPointer("sha256:" + strings.Repeat("a", 64)),
		protocol:            atomicCommandProtocolVersion,
		createdAt:           now,
		startedAt:           now.Add(time.Second),
		expiresAt:           now.Add(24 * time.Hour),
	}
	tests := []struct {
		name     string
		mutate   func(*idempotencyRepairRecord)
		active   bool
		state    string
		eligible bool
	}{
		{name: "interrupted", state: "interrupted", eligible: true},
		{name: "active", active: true, state: "active"},
		{name: "completed", mutate: func(record *idempotencyRepairRecord) { record.responseStatus = &status }, state: "completed"},
		{name: "completed with lock held", active: true, mutate: func(record *idempotencyRepairRecord) { record.responseStatus = &status }, state: "completed"},
		{name: "released", mutate: func(record *idempotencyRepairRecord) { record.releasedAt = &released }, state: "released"},
		{name: "released with lock held", active: true, mutate: func(record *idempotencyRepairRecord) { record.releasedAt = &released }, state: "released"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := base
			if test.mutate != nil {
				test.mutate(&record)
			}
			plan, err := makeIdempotencyRepairPlan(record, "  confirmed process interruption  ", test.active)
			if err != nil {
				t.Fatal(err)
			}
			if plan.State != test.state || plan.Eligible != test.eligible || plan.PlanDigest == "" || plan.Version != "catalog.idempotency-repair/v2" {
				t.Fatalf("plan = %#v", plan)
			}
			if plan.TenantID != record.scope.TenantID || plan.ReservationID != record.reservationID || plan.Actor != record.scope.Actor || plan.Route != record.scope.Route || plan.RequestDigest != record.requestDigest || plan.AuthorizationDigest != *record.authorizationDigest || plan.ProtocolVersion != atomicCommandProtocolVersion || plan.Active != test.active || plan.Reason != "confirmed process interruption" || plan.Action != "release_interrupted_reservation" {
				t.Fatalf("reviewable plan lost target or command binding: %#v", plan)
			}
			if plan.CreatedAt != record.createdAt.UTC() || plan.StartedAt != record.startedAt.UTC() || plan.ExpiresAt != record.expiresAt.UTC() || (plan.Refusal == "") != test.eligible {
				t.Fatalf("plan timestamps or refusal = %#v", plan)
			}
			if wantKey := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(record.scope.Key))); plan.IdempotencyKeyDigest != wantKey {
				t.Fatalf("key digest = %q, want %q", plan.IdempotencyKeyDigest, wantKey)
			}
			if wantDigest, digestErr := repairPlanDigest(plan); digestErr != nil || !validSHA256Digest(plan.PlanDigest) || plan.PlanDigest != wantDigest {
				t.Fatalf("plan digest does not bind the returned plan: %q, %v", plan.PlanDigest, digestErr)
			}
			raw, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), base.scope.Key) || plan.IdempotencyKeyDigest == base.scope.Key {
				t.Fatalf("repair plan exposed the client key: %s", raw)
			}
		})
	}
}

func TestIdempotencyRepairRefusesUnsupportedProtocol(t *testing.T) {
	for _, protocol := range []int{0, 1, 3} {
		if _, err := makeIdempotencyRepairPlan(idempotencyRepairRecord{protocol: protocol}, "reason", false); err == nil {
			t.Fatalf("accepted protocol %d", protocol)
		}
	}
}

func stringPointer(value string) *string { return &value }

func TestIdempotencyRepairPlanDigestBindsReviewedInputs(t *testing.T) {
	t.Parallel()
	plan := IdempotencyRepairPlan{Version: idempotencyRepairPlanVersion, Action: "release_interrupted_reservation", TenantID: "tenant", ReservationID: "reservation", Reason: "reason", State: "interrupted", Eligible: true}
	first, err := repairPlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.PlanDigest = "ignored-existing-digest"
	second, err := repairPlanDigest(plan)
	if err != nil || second != first {
		t.Fatalf("stable digest = %q, %q, %v", first, second, err)
	}
	for name, change := range map[string]func(*IdempotencyRepairPlan){
		"version":       func(p *IdempotencyRepairPlan) { p.Version = "unsupported-version" },
		"action":        func(p *IdempotencyRepairPlan) { p.Action = "different-action" },
		"tenant":        func(p *IdempotencyRepairPlan) { p.TenantID = "different-tenant" },
		"reservation":   func(p *IdempotencyRepairPlan) { p.ReservationID = "different-reservation" },
		"actor":         func(p *IdempotencyRepairPlan) { p.Actor = "different-actor" },
		"route":         func(p *IdempotencyRepairPlan) { p.Route = "POST /api/v1/resources" },
		"request":       func(p *IdempotencyRepairPlan) { p.RequestDigest = "sha256:" + strings.Repeat("a", 64) },
		"authorization": func(p *IdempotencyRepairPlan) { p.AuthorizationDigest = "sha256:" + strings.Repeat("b", 64) },
		"key":           func(p *IdempotencyRepairPlan) { p.IdempotencyKeyDigest = "sha256:" + strings.Repeat("c", 64) },
		"protocol":      func(p *IdempotencyRepairPlan) { p.ProtocolVersion = atomicCommandProtocolVersion },
		"active":        func(p *IdempotencyRepairPlan) { p.Active = true },
		"eligible":      func(p *IdempotencyRepairPlan) { p.Eligible = false },
		"reason":        func(p *IdempotencyRepairPlan) { p.Reason = "different reason" },
		"state":         func(p *IdempotencyRepairPlan) { p.State = "active" },
		"created":       func(p *IdempotencyRepairPlan) { p.CreatedAt = p.CreatedAt.Add(time.Second) },
		"started":       func(p *IdempotencyRepairPlan) { p.StartedAt = p.StartedAt.Add(time.Second) },
		"expires":       func(p *IdempotencyRepairPlan) { p.ExpiresAt = p.ExpiresAt.Add(time.Second) },
		"refusal":       func(p *IdempotencyRepairPlan) { p.Refusal = "command still active" },
	} {
		t.Run(name, func(t *testing.T) {
			changedPlan := plan
			change(&changedPlan)
			changed, err := repairPlanDigest(changedPlan)
			if err != nil || changed == first {
				t.Fatalf("changed %s did not change digest: %q, %v", name, changed, err)
			}
		})
	}
}

func TestIdempotencyRepairPlanRejectsUnencodableTimestamp(t *testing.T) {
	t.Parallel()
	// PostgreSQL timestamps can exceed JSON's four-digit year range. Such a
	// retained value must not produce an eligible, partially encoded plan.
	for _, field := range []string{"created", "started", "expires"} {
		t.Run(field, func(t *testing.T) {
			now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
			record := idempotencyRepairRecord{
				scope:         idempotency.Scope{TenantID: "00000000-0000-7000-8000-000000000001", Actor: "operator@example.com", Route: "POST /api/v1/resources", Key: "fixture-key"},
				reservationID: "00000000-0000-7000-8000-000000000002", requestDigest: "request-digest", authorizationDigest: stringPointer("sha256:" + strings.Repeat("a", 64)),
				protocol: atomicCommandProtocolVersion, createdAt: now, startedAt: now, expiresAt: now.Add(time.Hour),
			}
			badTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			switch field {
			case "created":
				record.createdAt = badTime
			case "started":
				record.startedAt = badTime
			case "expires":
				record.expiresAt = badTime
			}
			plan, err := makeIdempotencyRepairPlan(record, "confirmed process interruption", false)
			var encodeErr *json.MarshalerError
			if err == nil || !strings.Contains(err.Error(), "encode idempotency repair plan") || !errors.As(err, &encodeErr) || plan != (IdempotencyRepairPlan{}) {
				t.Fatalf("invalid timestamp yielded %#v, %v", plan, err)
			}
		})
	}
}

func TestValidateIdempotencyRepairInput(t *testing.T) {
	t.Parallel()
	validTenant := "00000000-0000-7000-8000-000000000001"
	validReservation := "00000000-0000-7000-8000-000000000002"
	if err := validateRepairInput(validTenant, validReservation, "interrupted deployment"); err != nil {
		t.Fatal(err)
	}
	if err := validateRepairInput(validTenant, validReservation, strings.Repeat("\u00e9", 2000)); err != nil {
		t.Fatalf("valid multibyte repair reason was rejected: %v", err)
	}
	if !uuidText("d2e5e398-08bf-416d-9478-c7013d8be508") {
		t.Fatal("PostgreSQL UUID text was rejected")
	}
	if !validSHA256Digest("sha256:"+strings.Repeat("a", 64)) || validSHA256Digest("sha256:"+strings.Repeat("A", 64)) || validSHA256Digest("sha256:short") || validSHA256Digest(strings.Repeat("a", 64)) {
		t.Fatal("repair plan digest validation accepted the wrong syntax")
	}
	for _, test := range []struct {
		tenant, reservation, reason string
	}{
		{tenant: "not-a-uuid", reservation: validReservation, reason: "reason"},
		{tenant: validTenant, reservation: "00000000-0000-7000-8000-00000000000g", reason: "reason"},
		{tenant: validTenant, reservation: validReservation, reason: " "},
		{tenant: validTenant, reservation: validReservation, reason: strings.Repeat("x", 2001)},
		{tenant: validTenant, reservation: validReservation, reason: strings.Repeat("\u00e9", 2001)},
		{tenant: validTenant, reservation: validReservation, reason: "invalid\x00reason"},
		{tenant: validTenant, reservation: validReservation, reason: string([]byte{0xff})},
	} {
		if err := validateRepairInput(test.tenant, test.reservation, test.reason); err == nil {
			t.Fatalf("invalid repair input was accepted: %#v", test)
		}
	}
}

func TestCompatibleCommandOptions(t *testing.T) {
	t.Parallel()
	active := pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite}
	if !compatibleCommandOptions(active, pgx.TxOptions{}) || !compatibleCommandOptions(active, pgx.TxOptions{AccessMode: pgx.ReadOnly}) || !compatibleCommandOptions(active, active) {
		t.Fatal("compatible command transaction options were rejected")
	}
	if compatibleCommandOptions(active, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}) || compatibleCommandOptions(active, pgx.TxOptions{AccessMode: pgx.ReadOnly, DeferrableMode: pgx.Deferrable}) {
		t.Fatal("incompatible command transaction options were accepted")
	}
}

type scannerFunc func(...any) error

func (scan scannerFunc) Scan(destinations ...any) error { return scan(destinations...) }

func setScanDestination[T any](t *testing.T, destinations []any, index int, value T) {
	t.Helper()
	if index >= len(destinations) {
		t.Fatalf("scan destination %d is missing", index)
	}
	destination, ok := destinations[index].(*T)
	if !ok {
		t.Fatalf("scan destination %d has type %T", index, destinations[index])
	}
	*destination = value
}

func TestScanIdempotencyRepairRecord(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	authorizationDigest := "sha256:" + strings.Repeat("a", 64)
	status := 201
	released := now.Add(time.Minute)
	for _, tt := range []struct {
		name           string
		responseStatus *int
		releasedAt     *time.Time
	}{
		{name: "pending"},
		{name: "completed", responseStatus: &status},
		{name: "released", releasedAt: &released},
	} {
		t.Run(tt.name, func(t *testing.T) {
			record, err := scanRepairRecord(scannerFunc(func(destinations ...any) error {
				setScanDestination(t, destinations, 0, "00000000-0000-7000-8000-000000000001")
				setScanDestination(t, destinations, 1, "operator@example.com")
				setScanDestination(t, destinations, 2, "POST /api/v1/resources")
				setScanDestination(t, destinations, 3, "private-key")
				setScanDestination(t, destinations, 4, "00000000-0000-7000-8000-000000000002")
				setScanDestination(t, destinations, 5, "request-digest")
				setScanDestination(t, destinations, 6, &authorizationDigest)
				setScanDestination(t, destinations, 7, atomicCommandProtocolVersion)
				setScanDestination(t, destinations, 8, tt.responseStatus)
				setScanDestination(t, destinations, 9, now)
				setScanDestination(t, destinations, 10, now.Add(time.Second))
				setScanDestination(t, destinations, 11, now.Add(time.Hour))
				setScanDestination(t, destinations, 12, tt.releasedAt)
				return nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			want := idempotencyRepairRecord{
				scope:         idempotency.Scope{TenantID: "00000000-0000-7000-8000-000000000001", Actor: "operator@example.com", Route: "POST /api/v1/resources", Key: "private-key"},
				reservationID: "00000000-0000-7000-8000-000000000002", requestDigest: "request-digest", authorizationDigest: &authorizationDigest,
				protocol: atomicCommandProtocolVersion, createdAt: now, startedAt: now.Add(time.Second), expiresAt: now.Add(time.Hour),
				responseStatus: tt.responseStatus, releasedAt: tt.releasedAt,
			}
			if !reflect.DeepEqual(record, want) {
				t.Fatalf("repair record = %#v, want %#v", record, want)
			}
		})
	}
	if stringValue(nil) != "" || stringValue(&authorizationDigest) != authorizationDigest {
		t.Fatal("nullable authorization digest projected incorrectly")
	}

	if _, err := scanRepairRecord(scannerFunc(func(...any) error { return pgx.ErrNoRows })); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing repair record error = %v", err)
	}
	readErr := errors.New("read failed")
	if _, err := scanRepairRecord(scannerFunc(func(...any) error { return readErr })); err == nil || !errors.Is(err, readErr) {
		t.Fatalf("failed repair scan error = %v", err)
	}
}

func TestIdempotencyRepairEntryPointsFailClosedBeforeQueries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenantID := "00000000-0000-7000-8000-000000000001"
	reservationID := "00000000-0000-7000-8000-000000000002"
	// Invalid inputs must be rejected before this inert pool can query.
	pool := &pgxpool.Pool{}
	if got, err := ListPendingIdempotencyReservations(ctx, pool, "invalid", 50); err == nil || !strings.Contains(err.Error(), "explicit tenant UUID") || !reflect.DeepEqual(got, PendingIdempotencyReport{}) {
		t.Fatalf("invalid inventory tenant = %#v, %v", got, err)
	}
	for _, limit := range []int{0, 101} {
		if got, err := ListPendingIdempotencyReservations(ctx, pool, tenantID, limit); err == nil || !strings.Contains(err.Error(), "limit must be between 1 and 100") || !reflect.DeepEqual(got, PendingIdempotencyReport{}) {
			t.Fatalf("invalid inventory limit = %#v, %v", got, err)
		}
	}
	if _, err := ListPendingIdempotencyReservations(ctx, nil, tenantID, 50); err == nil || !strings.Contains(err.Error(), "pool is required") {
		t.Fatalf("missing inventory pool error = %v", err)
	}
	for _, tt := range []struct{ tenant, reservation, reason, want string }{
		{tenant: "invalid", reservation: reservationID, reason: "reason", want: "explicit UUID tenant and reservation"},
		{tenant: tenantID, reservation: "invalid", reason: "reason", want: "explicit UUID tenant and reservation"},
		{tenant: tenantID, reservation: reservationID, reason: " ", want: "reason is invalid"},
	} {
		if got, err := PlanIdempotencyRepair(ctx, pool, tt.tenant, tt.reservation, tt.reason); err == nil || !strings.Contains(err.Error(), tt.want) || got != (IdempotencyRepairPlan{}) {
			t.Fatalf("invalid plan input = %#v, %v; want %q", got, err, tt.want)
		}
		if got, err := ApplyIdempotencyRepair(ctx, pool, tt.tenant, tt.reservation, tt.reason, "sha256:"+strings.Repeat("a", 64)); err == nil || !strings.Contains(err.Error(), tt.want) || got != (IdempotencyRepairResult{}) {
			t.Fatalf("invalid apply input = %#v, %v; want %q", got, err, tt.want)
		}
	}
	if _, err := PlanIdempotencyRepair(ctx, nil, tenantID, reservationID, "reason"); err == nil || !strings.Contains(err.Error(), "pool is required") {
		t.Fatalf("missing plan pool error = %v", err)
	}
	if got, err := ApplyIdempotencyRepair(ctx, pool, tenantID, reservationID, "reason", "invalid"); err == nil || !strings.Contains(err.Error(), "exact sha256") || got != (IdempotencyRepairResult{}) {
		t.Fatalf("invalid apply digest = %#v, %v", got, err)
	}
	if _, err := ApplyIdempotencyRepair(ctx, nil, tenantID, reservationID, "reason", "sha256:"+strings.Repeat("a", 64)); err == nil || !strings.Contains(err.Error(), "pool is required") {
		t.Fatalf("missing apply pool error = %v", err)
	}
}

func TestUUIDAndDigestSyntaxRejectMalformedValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"000000000000-7000-8000-000000000001",
		"00000000_0000-7000-8000-000000000001",
		"00000000-0000-7000-8000-00000000000!",
	} {
		if uuidText(value) {
			t.Fatalf("malformed UUID %q was accepted", value)
		}
	}
	if validSHA256Digest("sha256:" + strings.Repeat("g", 64)) {
		t.Fatal("non-hexadecimal digest was accepted")
	}
}

const (
	idempotencyApplyReason = "Confirmed interruption of the original request"
	idempotencyApplyOwner  = "catalog_owner"
	idempotencyApplyAudit  = "00000000-0000-7000-8000-000000000003"
)

type idempotencyApplyFixture struct {
	tx         *identityScriptedTx
	record     idempotencyRepairRecord
	reviewed   IdempotencyRepairPlan
	releasedAt time.Time
}

func repairRecordRow(record idempotencyRepairRecord) pgx.Row {
	return scriptedValueRow([]any{
		record.scope.TenantID, record.scope.Actor, record.scope.Route, record.scope.Key,
		record.reservationID, record.requestDigest, record.authorizationDigest, record.protocol, record.responseStatus,
		record.createdAt, record.startedAt, record.expiresAt, record.releasedAt,
	})
}

func newIdempotencyApplyFixture(t *testing.T) idempotencyApplyFixture {
	t.Helper()
	releasedAt := time.Date(2026, time.September, 21, 12, 0, 0, 123000, time.UTC)
	record := idempotencyRepairRecord{
		scope: idempotency.Scope{
			TenantID: "00000000-0000-7000-8000-000000000001", Actor: "00000000-0000-7000-8000-000000000010",
			Route: "POST /api/v1/resources", Key: "synthetic-repair-client-key",
		},
		reservationID: "00000000-0000-7000-8000-000000000002", requestDigest: "sha256:" + strings.Repeat("b", 64),
		authorizationDigest: stringPointer("sha256:" + strings.Repeat("a", 64)), protocol: atomicCommandProtocolVersion,
		createdAt: releasedAt.Add(-5 * time.Minute), startedAt: releasedAt.Add(-4 * time.Minute), expiresAt: releasedAt.Add(time.Hour),
	}
	// Reuse the separately tested planner to obtain the reviewed input. A free
	// command lock permits repair before expiry; this helper cannot prove the lock.
	reviewed, err := makeIdempotencyRepairPlan(record, idempotencyApplyReason, false)
	if err != nil || !reviewed.Eligible {
		t.Fatalf("valid fixture plan = %#v, %v", reviewed, err)
	}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{
		repairRecordRow(record),
		scriptedValueRow([]any{releasedAt}),
		scriptedValueRow([]any{idempotencyApplyAudit, "maintenance_login", idempotencyApplyOwner}),
	}}
	return idempotencyApplyFixture{tx: tx, record: record, reviewed: reviewed, releasedAt: releasedAt}
}

func TestReadRepairRecordTransactionAndFailureHandoff(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "begin failure", "tenant failure", "read failure", "commit failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newIdempotencyApplyFixture(t)
			failure := errors.New("repair read failed")
			row := repairRecordRow(f.record)
			f.tx.queryRows = []pgx.Row{scannerFunc(func(destinations ...any) error {
				if len(f.tx.statements) != 1 || !strings.Contains(f.tx.statements[0], "set_config('app.tenant_id'") || !reflect.DeepEqual(f.tx.arguments, [][]any{{f.record.scope.TenantID, "", ""}}) || f.tx.commits != 0 || f.tx.rollbacks != 0 {
					t.Fatal("repair read ran before tenant setup or after transaction completion")
				}
				if mode == "read failure" {
					return failure
				}
				return row.Scan(destinations...)
			})}
			connection := &captureCommandConnection{transactions: []pgx.Tx{f.tx}}
			wantReads, wantCommits, wantRollbacks := 1, 1, 0
			want := f.record
			switch mode {
			case "begin failure":
				connection.beginErr = failure
				wantReads, wantCommits, want = 0, 0, idempotencyRepairRecord{}
			case "tenant failure":
				f.tx.execErrors = []error{failure}
				wantReads, wantCommits, wantRollbacks, want = 0, 0, 1, idempotencyRepairRecord{}
			case "read failure":
				wantCommits, wantRollbacks, want = 0, 1, idempotencyRepairRecord{}
			case "commit failure":
				f.tx.commitErr = failure
				wantRollbacks = 1 // The decoded record is returned with the commit error.
			}
			got, err := readRepairRecord(t.Context(), &pgxpool.Pool{}, connection, f.record.scope.TenantID, f.record.reservationID)
			if mode == "success" && err != nil || mode != "success" && !errors.Is(err, failure) || !reflect.DeepEqual(got, want) {
				t.Fatalf("repair read = %#v, %v; want %#v", got, err, want)
			}
			if !reflect.DeepEqual(connection.options, []pgx.TxOptions{{AccessMode: pgx.ReadOnly}}) || f.tx.commits != wantCommits || f.tx.rollbacks != wantRollbacks || len(f.tx.queryRows) != 1-wantReads {
				t.Fatalf("repair transaction: options=%v commit=%d rollback=%d unread=%d", connection.options, f.tx.commits, f.tx.rollbacks, len(f.tx.queryRows))
			}
			if len(f.tx.queryRowStatements) != wantReads || len(f.tx.queryStatements) != 0 || len(f.tx.statements) > 1 {
				t.Fatal("repair read performed unrelated work")
			}
			if wantReads > 0 && (!reflect.DeepEqual(f.tx.queryRowArguments, [][]any{{f.record.scope.TenantID, f.record.reservationID}}) || !strings.Contains(f.tx.queryRowStatements[0], "FROM idempotency_keys WHERE tenant_id=$1 AND reservation_id=$2")) {
				t.Fatalf("repair target binding = %v, %v", f.tx.queryRowStatements, f.tx.queryRowArguments)
			}
		})
	}
}

func TestApplyIdempotencyRepairTxBindsReleaseAndAudit(t *testing.T) {
	t.Parallel()
	f := newIdempotencyApplyFixture(t)
	tenant, reservation := f.record.scope.TenantID, f.record.reservationID
	got, err := applyIdempotencyRepairTx(t.Context(), f.tx, tenant, reservation, idempotencyApplyReason, f.reviewed.PlanDigest, idempotencyApplyOwner)
	want := IdempotencyRepairResult{Plan: f.reviewed, Applied: true, DatabaseActor: idempotencyApplyOwner, AuditEventID: idempotencyApplyAudit, ReleasedAt: f.releasedAt}
	if err != nil || got != want {
		t.Fatalf("applied repair = %#v, %v; want %#v", got, err, want)
	}
	if !reflect.DeepEqual(f.tx.queryRowArguments, [][]any{{tenant, reservation}, {tenant, reservation}, nil}) || len(f.tx.queryRows) != 0 || len(f.tx.queryStatements) != 0 {
		t.Fatalf("repair reread/release/attribution bindings = %#v", f.tx.queryRowArguments)
	}
	if len(f.tx.statements) != 2 || !strings.Contains(f.tx.statements[0], "UPDATE idempotency_keys") || !strings.Contains(f.tx.statements[1], "INSERT INTO audit_events(") {
		t.Fatalf("repair must update one reservation and write one audit record, with no outbox: %#v", f.tx.statements)
	}
	if !reflect.DeepEqual(f.tx.arguments[0], []any{tenant, reservation, idempotencyApplyReason}) {
		t.Fatalf("release lost its reviewed target or reason: %#v", f.tx.arguments[0])
	}
	args := f.tx.arguments[1]
	wantFields := []any{
		new(audit.RecordFormatV1), idempotencyApplyAudit, tenant, "maintenance_login", audit.ActionIdempotencyReservationReleased,
		"idempotency_reservation", reservation, idempotencyApplyReason, "",
		digestText("idempotency-reservation:pending"), digestText("idempotency-reservation:released"),
	}
	if len(args) != 13 || !reflect.DeepEqual(args[:11], wantFields) || !reflect.DeepEqual(args[12], f.releasedAt) {
		t.Fatalf("repair audit lost release identity, attribution, digests or time: %#v", args)
	}
	raw, ok := args[11].([]byte)
	if !ok {
		t.Fatalf("repair audit details have type %T; want encoded JSON", args[11])
	}
	var details audit.Details
	if err := json.Unmarshal(raw, &details); err != nil {
		t.Fatal(err)
	}
	wantDetails := audit.Details{PrincipalType: audit.PrincipalDatabaseLogin, CurrentUser: idempotencyApplyOwner,
		Facts: map[string]any{"reservationId": reservation, "protocolVersion": float64(2)}}
	if !reflect.DeepEqual(details, wantDetails) {
		t.Fatalf("repair audit details = %#v; want only bounded release facts %#v", details, wantDetails)
	}
	if f.tx.commits != 0 || f.tx.rollbacks != 0 {
		t.Fatal("repair operation took transaction ownership")
	}
}

type noIdempotencyRepairUpdateTx struct{ *identityScriptedTx }

func (tx noIdempotencyRepairUpdateTx) Exec(ctx context.Context, statement string, args ...any) (pgconn.CommandTag, error) {
	_, err := tx.identityScriptedTx.Exec(ctx, statement, args...)
	return pgconn.NewCommandTag("UPDATE 0"), err
}

func TestApplyIdempotencyRepairTxStopsOnFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"reread", "newly completed", "changed reason", "plan encoding", "update", "zero updated rows", "release time", "audit attribution", "audit insert"} {
		t.Run(mode, func(t *testing.T) {
			f := newIdempotencyApplyFixture(t)
			var tx pgx.Tx = f.tx
			failure := errors.New("injected idempotency repair failure")
			failedRow := scannerFunc(func(...any) error { return failure })
			reason := idempotencyApplyReason
			wantReads, wantWrites := 1, 0
			wantError := ""
			switch mode {
			case "reread":
				f.tx.queryRows[0] = failedRow
			case "newly completed":
				f.record.responseStatus = new(201)
				f.tx.queryRows[0] = repairRecordRow(f.record)
				wantError = "cannot be repaired: the reservation already has a retained response"
			case "changed reason":
				reason = "Different reason from the reviewed plan"
				wantError = "repair plan changed; review a new dry-run plan"
			case "plan encoding":
				// One propagation case for PostgreSQL's wider timestamp range;
				// the existing planner tests own the timestamp-field matrix.
				f.record.expiresAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
				f.tx.queryRows[0] = repairRecordRow(f.record)
				wantError = "encode idempotency repair plan"
			case "update":
				f.tx.execErrors, wantWrites = []error{failure}, 1
			case "zero updated rows":
				tx, wantWrites = noIdempotencyRepairUpdateTx{f.tx}, 1
				wantError = "reservation changed while applying its repair"
			case "release time":
				f.tx.queryRows[1], wantReads, wantWrites = failedRow, 2, 1
			case "audit attribution":
				f.tx.queryRows[2], wantReads, wantWrites = failedRow, 3, 1
			case "audit insert":
				f.tx.execErrors = []error{nil, failure}
				wantReads, wantWrites = 3, 2
			}
			got, err := applyIdempotencyRepairTx(t.Context(), tx, f.record.scope.TenantID, f.record.reservationID, reason, f.reviewed.PlanDigest, idempotencyApplyOwner)
			if err == nil || got != (IdempotencyRepairResult{}) {
				t.Fatalf("failed apply = %#v, %v; want no successful result", got, err)
			}
			if wantError != "" && !strings.Contains(err.Error(), wantError) || wantError == "" && !errors.Is(err, failure) {
				t.Fatalf("repair error = %v; lost refusal or injected cause", err)
			}
			if mode == "plan encoding" {
				var encodeErr *json.MarshalerError
				if !errors.As(err, &encodeErr) {
					t.Fatalf("repair lost the plan encoding cause: %v", err)
				}
			}
			if len(f.tx.queryRowStatements) != wantReads || len(f.tx.statements) != wantWrites || len(f.tx.queryStatements) != 0 {
				t.Fatalf("failure skipped or continued work: reads=%d writes=%d; want %d, %d", len(f.tx.queryRowStatements), len(f.tx.statements), wantReads, wantWrites)
			}
			if f.tx.commits != 0 || f.tx.rollbacks != 0 {
				t.Fatal("failed repair took transaction ownership")
			}
		})
	}
}
