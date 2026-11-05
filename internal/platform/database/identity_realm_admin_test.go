// Operator realm units stop at pool admission or exercise existing tx helpers.
package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestIdentityRealmOperatorInputRejection(t *testing.T) {
	const realmID = "00000000-0000-7000-8000-000000000100"
	for _, mode := range []string{"tenant", "cursor", "zero limit", "excess limit"} {
		t.Run("list/"+mode, func(t *testing.T) {
			tenant, after, limit := restoreTestTenant, "", 1
			switch mode {
			case "tenant":
				tenant = "invalid"
			case "cursor":
				after = "invalid"
			case "zero limit":
				limit = 0
			case "excess limit":
				limit = 101
			}
			got, err := ListIdentityRealms(t.Context(), &pgxpool.Pool{}, tenant, after, limit)
			if err == nil || !reflect.DeepEqual(got, IdentityRealmPage{}) {
				t.Fatalf("invalid list admitted=%+v %v", got, err)
			}
		})
	}
	for _, mode := range []string{"tenant", "realm", "revision", "operator empty", "operator bound", "state", "reason empty", "reason controls", "reactivation reason"} {
		t.Run("transition/"+mode, func(t *testing.T) {
			input := IdentityRealmTransitionInput{TenantID: restoreTestTenant, RealmID: realmID, ExpectedRevision: 1, TargetState: identity.RealmSuspended, Operator: "operator", Reason: "suspend realm"}
			reasonError := false
			switch mode {
			case "tenant":
				input.TenantID = "invalid"
			case "realm":
				input.RealmID = "invalid"
			case "revision":
				input.ExpectedRevision = 0
			case "operator empty":
				input.Operator = "  "
			case "operator bound":
				input.Operator = strings.Repeat("o", 513)
			case "state":
				input.TargetState = "unknown"
			case "reason empty":
				input.Reason = "  "
				reasonError = true
			case "reason controls":
				input.Reason = "unsafe\nreason"
				reasonError = true
			case "reactivation reason":
				input.TargetState = identity.RealmActive
				input.Reason = "unsafe\nreason"
				reasonError = true
			}
			got, err := TransitionIdentityRealm(t.Context(), &pgxpool.Pool{}, input)
			if err == nil || !reflect.DeepEqual(got, IdentityRealmTransitionResult{}) {
				t.Fatalf("invalid transition admitted=%+v %v", got, err)
			}
			if reasonError && !strings.Contains(err.Error(), "reason is invalid") {
				t.Fatalf("reason validation not reached: %v", err)
			}
		})
	}
}

func TestIdentityRealmPageFailuresAndEmptyResults(t *testing.T) {
	failure := errors.New("realm list failure")
	for _, mode := range []string{"empty", "count", "query", "scan", "iteration"} {
		t.Run(mode, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{0})}}
			rows := &identityScriptedRows{}
			result := identityQueryResult{rows: rows}
			switch mode {
			case "count":
				tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
			case "query":
				result.err = failure
			case "scan":
				rows.values = [][]any{{}}
			case "iteration":
				rows.err = failure
			}
			tx.queryResults = []identityQueryResult{result}
			after := "00000000-0000-7000-8000-000000000100"
			got, err := listIdentityRealmsTx(t.Context(), tx, restoreTestTenant, after, 1)
			if mode == "empty" {
				if err != nil || got.Items == nil || len(got.Items) != 0 || got.Total != 0 || got.NextAfter != "" {
					t.Fatalf("empty=%+v %v", got, err)
				}
			} else if err == nil || !reflect.DeepEqual(got, IdentityRealmPage{}) {
				t.Fatalf("partial page=%+v %v", got, err)
			}
			if mode != "empty" && mode != "scan" && !errors.Is(err, failure) {
				t.Fatal("list cause lost")
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{restoreTestTenant}}) {
				t.Fatal("realm total scope lost")
			}
			if mode == "count" {
				if len(tx.queryStatements) != 0 {
					t.Fatal("count failure reached selection")
				}
			} else if !reflect.DeepEqual(tx.queryArguments, [][]any{{restoreTestTenant, after, 2}}) || mode != "query" && !rows.closed {
				t.Fatalf("page scope/cleanup=%v %t", tx.queryArguments, rows.closed)
			}
			if tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatal("helper took transaction ownership")
			}
		})
	}
}

// Successful shutdown/reactivation and their interlocks remain in
// TestIdentityRealmTransactionHelpersEnforceShutdownInterlocks.
func TestIdentityRealmTransitionStopsAtFailedBoundary(t *testing.T) {
	stages := []string{"lock", "tenant", "realm", "active realms", "active snapshots", "update", "audit attribution", "outbox identifier", "audit insert", "outbox insert"}
	for index, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
			realm := identity.Realm{ID: "00000000-0000-7000-8000-000000000100", TenantID: restoreTestTenant, Issuer: "https://identity.example.com", TenancyMode: identity.RealmDedicated, State: identity.RealmActive, Revision: 1, CreatedAt: at.Add(-time.Hour), UpdatedAt: at.Add(-time.Hour)}
			input := IdentityRealmTransitionInput{TenantID: realm.TenantID, RealmID: realm.ID, ExpectedRevision: 1, TargetState: identity.RealmSuspended, Operator: "operator", Reason: "suspend tenant access", TenantDiscoveryDisabled: true}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{
				scriptedValueRow([]any{realm.TenantID}), postgresRealmRow(t, realm), scriptedValueRow([]any{0}), scriptedValueRow([]any{0}), scriptedValueRow([]any{at}), scriptedValueRow([]any{"00000000-0000-7000-8000-000000000199", "catalog_owner", "catalog_owner"}), scriptedValueRow([]any{"00000000-0000-7000-8000-000000000198"}),
			}}
			failure := errors.New("realm transition failure")
			writes, reads := 1, index
			switch {
			case index == 0:
				tx.execErrors = []error{failure}
			case index < 8:
				tx.queryRows[index-1] = scannerFunc(func(...any) error { return failure })
			default:
				reads = 7
				writes = index - 6
				tx.execErrors = make([]error, writes)
				tx.execErrors[writes-1] = failure
			}
			got, err := transitionIdentityRealmTx(t.Context(), tx, input)
			if !errors.Is(err, failure) || !reflect.DeepEqual(got, IdentityRealmTransitionResult{}) || len(tx.statements) != writes || len(tx.queryRowStatements) != reads || tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatalf("transition=%+v %v writes/reads=%d/%d want=%d/%d", got, err, len(tx.statements), len(tx.queryRowStatements), writes, reads)
			}
			expected := [][]any{{realm.TenantID}, {realm.TenantID, realm.ID}, {realm.TenantID, realm.ID}, {realm.TenantID}, {realm.TenantID, realm.ID, identity.RealmSuspended, int64(2)}}
			scoped := min(reads, len(expected))
			if scoped > 0 && !reflect.DeepEqual(tx.queryRowArguments[:scoped], expected[:scoped]) {
				t.Fatalf("realm scope=%v", tx.queryRowArguments)
			}
			if tx.arguments[0][0] != catalogStateLockKey("tenant", realm.TenantID) {
				t.Fatal("realm lock scope lost")
			}
		})
	}
}
