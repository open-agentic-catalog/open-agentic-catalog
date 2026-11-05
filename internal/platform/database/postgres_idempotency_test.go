// Durable reservation units use the existing inert transaction fixtures; real
// advisory locks, SQL predicates and atomic commit remain integration contracts.
package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
)

func TestPostgresIdempotencyRejectsAuthorizationBeforePoolAccess(t *testing.T) {
	t.Parallel()
	store := &PostgresIdempotencyStore{repository: &PostgresRepository{pool: &pgxpool.Pool{}}}
	for _, authorization := range []string{"", "sha256:short", strings.Repeat("a", 64), "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("g", 64)} {
		called := false
		scope := idempotency.Scope{TenantID: restoreTestTenant, Actor: "actor-a", Route: "POST /api/v1/resources", Key: "fixture-key", AuthorizationDigest: authorization}
		got, err := store.Execute(t.Context(), scope, "request", time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC), func(context.Context) idempotency.Response {
			called = true
			return idempotency.Response{}
		})
		if catalog.ErrorCodeOf(err) != catalog.CodeInternal || !strings.Contains(err.Error(), "authorization digest is invalid") || !reflect.DeepEqual(got, idempotency.Execution{}) || called {
			t.Fatalf("invalid authorization returned %#v, %v; executor called=%t", got, err, called)
		}
	}
}

func TestReserveAtomicCreatesBoundReservation(t *testing.T) {
	t.Parallel()
	scope := idempotency.Scope{TenantID: restoreTestTenant, Actor: "actor-a", Route: "POST /api/v1/resources", Key: "fixture-key", AuthorizationDigest: "sha256:" + strings.Repeat("a", 64)}
	expires := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	const reservationID = "00000000-0000-7000-8000-000000000010"
	const token = "00000000-0000-7000-8000-000000000011"
	providerErr := &pgconn.PgError{Code: "42501"}
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "created"},
		{name: "insert denied", err: providerErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := scriptedValueRow([]any{reservationID, token})
			if tt.err != nil {
				row = scannerFunc(func(...any) error { return tt.err })
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{row}}
			connection := &captureCommandConnection{transaction: tx}
			command := &commandTransaction{conn: connection, tenantID: scope.TenantID}
			got, gotID, gotToken, err := (&PostgresIdempotencyStore{}).reserveAtomic(t.Context(), command, scope, "request-digest", expires)
			if tt.err != nil {
				if !errors.Is(err, tt.err) || catalog.ErrorCodeOf(err) != catalog.CodeForbidden || !reflect.DeepEqual(got, idempotency.Reservation{}) || gotID != "" || gotToken != "" || tx.commits != 0 || tx.rollbacks != 1 {
					t.Fatalf("failed reservation = %#v, %q, %q, %v; commits=%d rollbacks=%d", got, gotID, gotToken, err, tx.commits, tx.rollbacks)
				}
			} else if err != nil || !reflect.DeepEqual(got, idempotency.Reservation{Acquired: true}) || gotID != reservationID || gotToken != token || tx.commits != 1 || tx.rollbacks != 0 {
				t.Fatalf("created reservation = %#v, %q, %q, %v; commits=%d rollbacks=%d", got, gotID, gotToken, err, tx.commits, tx.rollbacks)
			}
			wantArguments := [][]any{{scope.TenantID, scope.Actor, scope.Route, scope.Key, "request-digest", expires, scope.AuthorizationDigest}}
			if !reflect.DeepEqual(tx.queryRowArguments, wantArguments) || len(tx.queryRows) != 0 || !reflect.DeepEqual(connection.options, []pgx.TxOptions{{}}) {
				t.Fatalf("reservation bindings=%#v options=%#v remaining rows=%d", tx.queryRowArguments, connection.options, len(tx.queryRows))
			}
			if !reflect.DeepEqual(tx.arguments, [][]any{{scope.TenantID, "", ""}}) || command.tx != nil {
				t.Fatalf("short reservation tenant context=%#v, retained transaction=%v", tx.arguments, command.tx)
			}
		})
	}
}

func TestReserveAtomicRetainedBindingReplayAndRestart(t *testing.T) {
	t.Parallel()
	scope := idempotency.Scope{TenantID: restoreTestTenant, Actor: "actor-a", Route: "POST /api/v1/resources", Key: "fixture-key", AuthorizationDigest: "sha256:" + strings.Repeat("a", 64)}
	expires := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	status := 201
	released := expires.Add(-time.Hour)
	const retainedID = "00000000-0000-7000-8000-000000000010"
	const freshID = "00000000-0000-7000-8000-000000000020"
	const freshToken = "00000000-0000-7000-8000-000000000021"
	const digest = "request-digest"
	responseBody := []byte(`{"id":"retained"}`)
	for _, tt := range []struct {
		name                                  string
		status                                *int
		releasedAt                            *time.Time
		expired, otherRequest, otherAuthority bool
		headers                               []byte
		replayAuthority                       []byte
		want                                  idempotency.Reservation
		wantCode                              catalog.ErrorCode
	}{
		{name: "pending", want: idempotency.Reservation{Pending: true}},
		{name: "expired pending still needs repair", expired: true, want: idempotency.Reservation{Pending: true}},
		{name: "pending request mismatch", otherRequest: true, wantCode: catalog.CodeIdempotencyKeyReused},
		{name: "expired pending request mismatch", expired: true, otherRequest: true, wantCode: catalog.CodeIdempotencyKeyReused},
		{name: "pending authorization mismatch", otherAuthority: true, wantCode: catalog.CodeForbidden},
		{name: "completed replay", status: &status, headers: []byte(`{"ETag":["rev-2"],"Content-Type":["application/json"]}`), want: idempotency.Reservation{Response: idempotency.Response{Digest: digest, Status: 201, Headers: map[string][]string{"ETag": {"rev-2"}, "Content-Type": {"application/json"}}, Body: responseBody}}},
		{name: "completed absent headers", status: &status, want: idempotency.Reservation{Response: idempotency.Response{Digest: digest, Status: 201, Body: responseBody}}},
		{name: "completed null headers", status: &status, headers: []byte(`null`), want: idempotency.Reservation{Response: idempotency.Response{Digest: digest, Status: 201, Body: responseBody}}},
		{name: "completed request mismatch", status: &status, otherRequest: true, wantCode: catalog.CodeIdempotencyKeyReused},
		{name: "completed authorization mismatch", status: &status, otherAuthority: true, wantCode: catalog.CodeForbidden},
		{name: "completed wrong header shape", status: &status, headers: []byte(`{"ETag":7}`), wantCode: catalog.CodeInternal},
		{name: "completed wrong replay authority shape", status: &status, replayAuthority: []byte(`{"version":"invalid"}`), wantCode: catalog.CodeInternal},
		{name: "released same binding", releasedAt: &released, want: idempotency.Reservation{Acquired: true}},
		{name: "released request mismatch", releasedAt: &released, otherRequest: true, wantCode: catalog.CodeIdempotencyKeyReused},
		{name: "released authorization mismatch", releasedAt: &released, otherAuthority: true, wantCode: catalog.CodeForbidden},
		{name: "expired completed new binding", status: &status, expired: true, otherRequest: true, otherAuthority: true, want: idempotency.Reservation{Acquired: true}},
		{name: "expired released new binding", releasedAt: &released, expired: true, otherRequest: true, otherAuthority: true, want: idempotency.Reservation{Acquired: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			storedDigest, authorization := digest, scope.AuthorizationDigest
			if tt.otherRequest {
				storedDigest = "different-request-digest"
			}
			if tt.otherAuthority {
				authorization = "sha256:" + strings.Repeat("b", 64)
			}
			var body []byte
			if tt.status != nil {
				body = bytes.Clone(responseBody)
			}
			rows := []pgx.Row{
				scannerFunc(func(...any) error { return pgx.ErrNoRows }),
				scriptedValueRow([]any{retainedID, storedDigest, tt.status, tt.headers, body, tt.expired, atomicCommandProtocolVersion, &authorization, tt.releasedAt, tt.replayAuthority}),
			}
			wantArgs := [][]any{
				{scope.TenantID, scope.Actor, scope.Route, scope.Key, digest, expires, scope.AuthorizationDigest},
				{scope.TenantID, scope.Actor, scope.Route, scope.Key},
			}
			wantID, wantToken := retainedID, ""
			if tt.want.Acquired {
				rows = append(rows, scriptedValueRow([]any{freshID, freshToken}))
				args := []any{scope.TenantID, scope.Actor, scope.Route, scope.Key, digest, expires, scope.AuthorizationDigest}
				if tt.releasedAt != nil {
					args = append(args, !tt.expired)
				}
				wantArgs = append(wantArgs, args)
				wantID, wantToken = freshID, freshToken
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: rows}
			connection := &captureCommandConnection{transaction: tx}
			command := &commandTransaction{conn: connection, tenantID: scope.TenantID}
			got, gotID, gotToken, err := (&PostgresIdempotencyStore{}).reserveAtomic(t.Context(), command, scope, digest, expires)
			if tt.wantCode != "" {
				if catalog.ErrorCodeOf(err) != tt.wantCode || tx.commits != 0 || tx.rollbacks != 1 {
					t.Fatalf("rejected reservation error=%v commits=%d rollbacks=%d", err, tx.commits, tx.rollbacks)
				}
				if tt.wantCode == catalog.CodeInternal {
					var decodeErr *json.UnmarshalTypeError
					if !errors.As(err, &decodeErr) {
						t.Fatalf("retained response decode lost cause: %v", err)
					}
				}
			} else if err != nil || tx.commits != 1 || tx.rollbacks != 0 {
				t.Fatalf("reservation error=%v commits=%d rollbacks=%d", err, tx.commits, tx.rollbacks)
			}
			if !reflect.DeepEqual(got, tt.want) || gotID != wantID || gotToken != wantToken || !reflect.DeepEqual(tx.queryRowArguments, wantArgs) || len(tx.queryRows) != 0 {
				t.Fatalf("reservation=%#v IDs=%q/%q queries=%#v remaining rows=%d; want %#v IDs=%q/%q queries=%#v", got, gotID, gotToken, tx.queryRowArguments, len(tx.queryRows), tt.want, wantID, wantToken, wantArgs)
			}
			if !reflect.DeepEqual(tx.arguments, [][]any{{scope.TenantID, "", ""}}) || !reflect.DeepEqual(connection.options, []pgx.TxOptions{{}}) || command.tx != nil {
				t.Fatalf("short transaction context=%#v options=%#v retained transaction=%v", tx.arguments, connection.options, command.tx)
			}
			if len(got.Response.Body) > 0 {
				body[0] = 'X'
				if !bytes.Equal(got.Response.Body, responseBody) {
					t.Fatal("replay body aliases the database scan buffer")
				}
			}
		})
	}
}

func TestReserveAtomicReadAndRestartFailures(t *testing.T) {
	t.Parallel()
	scope := idempotency.Scope{TenantID: restoreTestTenant, Actor: "actor-a", Route: "POST /api/v1/resources", Key: "fixture-key", AuthorizationDigest: "sha256:" + strings.Repeat("a", 64)}
	status := 201
	expires := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	released := expires.Add(-time.Hour)
	providerErr := &pgconn.PgError{Code: "40001"}
	for _, name := range []string{"read", "completed restart", "released restart"} {
		t.Run(name, func(t *testing.T) {
			rows := []pgx.Row{scannerFunc(func(...any) error { return pgx.ErrNoRows })}
			if name != "read" {
				var retainedStatus *int
				var releasedAt *time.Time
				if name == "completed restart" {
					retainedStatus = &status
				} else {
					releasedAt = &released
				}
				rows = append(rows, scriptedValueRow([]any{"00000000-0000-7000-8000-000000000010", "request-digest", retainedStatus, []byte(nil), []byte(nil), true, atomicCommandProtocolVersion, &scope.AuthorizationDigest, releasedAt, []byte(nil)}))
			}
			rows = append(rows, scannerFunc(func(...any) error { return providerErr }))
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: rows}
			command := &commandTransaction{conn: &captureCommandConnection{transaction: tx}, tenantID: scope.TenantID}
			got, _, token, err := (&PostgresIdempotencyStore{}).reserveAtomic(t.Context(), command, scope, "request-digest", expires)
			if !errors.Is(err, providerErr) || catalog.ErrorCodeOf(err) != catalog.CodeConflict || !reflect.DeepEqual(got, idempotency.Reservation{}) || token != "" || tx.commits != 0 || tx.rollbacks != 1 || len(tx.queryRows) != 0 {
				t.Fatalf("failed reservation=%#v token=%q error=%v commits=%d rollbacks=%d remaining rows=%d", got, token, err, tx.commits, tx.rollbacks, len(tx.queryRows))
			}
		})
	}
}
