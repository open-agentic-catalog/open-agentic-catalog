package database

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestRuntimeRoleAdmissionAcceptsExactPrivileges checks the admission sequence;
// privilege inspection units own the individual manifest and inventory rules.
func TestRuntimeRoleAdmissionAcceptsExactPrivileges(t *testing.T) {
	t.Parallel()
	for _, runtimeRole := range []string{RuntimeRoleAPI, RuntimeRoleWorker} {
		t.Run(runtimeRole, func(t *testing.T) {
			want := RoleAttributes{Name: privilegeTestAPI, Login: true}
			if runtimeRole == RuntimeRoleWorker {
				want.Name, want.BypassRLS = privilegeTestWorker, true
			}
			f := newPrivilegeInspectionFixture(t, runtimeRole, false, privilegeTestSchema)
			f.tx.queryRows = append([]pgx.Row{
				scriptedValueRow([]any{want.Name, want.Name}),
				privilegeRoleRow(want.Name, want.BypassRLS),
				scriptedValueRow([]any{false}),
				scriptedValueRow([]any{privilegeTestSchema, want.Name}),
			}, f.tx.queryRows...)
			reads, queries := len(f.tx.queryRows), len(f.tx.queryResults)
			got, err := verifyRuntimeRole(t.Context(), f.tx, runtimeRole)
			if err != nil || got != want {
				t.Fatalf("admitted attributes=%+v error=%v, want %+v", got, err, want)
			}
			if len(f.tx.queryRowStatements) != reads || len(f.tx.queryStatements) != queries || len(f.tx.queryRows) != 0 || len(f.tx.queryResults) != 0 {
				t.Fatal("admission did not complete exactly one privilege inspection")
			}
			wantBindings := [][]any{nil, {want.Name}, {want.Name}, nil, {want.Name, privilegeTestSchema}}
			if f.tx.queryRowStatements[0] != "SELECT session_user,current_user" || !reflect.DeepEqual(f.tx.queryRowArguments[:5], wantBindings) || !reflect.DeepEqual(f.tx.queryArguments[0], []any{want.Name, privilegeTestSchema}) {
				t.Fatalf("admission lost session, role or schema binding: rows=%v query=%v", f.tx.queryRowArguments[:5], f.tx.queryArguments[0])
			}
			if len(f.tx.statements) != 0 || f.tx.commits != 0 || f.tx.rollbacks != 0 {
				t.Fatal("read-only admission wrote state or took transaction ownership")
			}
		})
	}
}

// TestRuntimeRoleAdmissionRejectsUnsafeOrUnreadableRoles requires zero returned
// authority and no later inspection after each failed admission boundary.
func TestRuntimeRoleAdmissionRejectsUnsafeOrUnreadableRoles(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"session read", "assumed role", "attribute read", "unsafe attributes", "membership read", "membership", "schema read", "privilege inspection", "privilege drift"} {
		t.Run(mode, func(t *testing.T) {
			failure := errors.New("injected admission read failure")
			f := newPrivilegeInspectionFixture(t, RuntimeRoleAPI, false, privilegeTestSchema)
			f.tx.queryRows = append([]pgx.Row{
				scriptedValueRow([]any{privilegeTestAPI, privilegeTestAPI}),
				privilegeRoleRow(privilegeTestAPI, false),
				scriptedValueRow([]any{false}),
				scriptedValueRow([]any{privilegeTestSchema, privilegeTestAPI}),
			}, f.tx.queryRows...)
			reads, queries := 0, 0
			var cause error
			var message string
			switch mode {
			case "session read":
				reads, cause, message = 1, failure, "inspect PostgreSQL session and current roles"
			case "assumed role":
				f.tx.queryRows[0] = scriptedValueRow([]any{privilegeTestOwner, privilegeTestAPI})
				reads, message = 1, fmt.Sprintf("session role %q assumed current role %q", privilegeTestOwner, privilegeTestAPI)
			case "attribute read":
				reads, cause, message = 2, failure, "inspect PostgreSQL role"
			case "unsafe attributes":
				f.tx.queryRows[1] = privilegeRoleRow(privilegeTestAPI, true)
				reads, message = 2, "must be NOBYPASSRLS"
			case "membership read":
				reads, cause, message = 3, failure, "inspect PostgreSQL api role memberships"
			case "membership":
				f.tx.queryRows[2] = scriptedValueRow([]any{true})
				reads, message = 3, "must not participate in database role memberships"
			case "schema read":
				reads, cause, message = 4, failure, "resolve active Catalog schema"
			case "privilege inspection":
				f.tx.queryResults[0].err = failure
				reads, queries, cause, message = 5, 1, failure, "inspect PostgreSQL api column privileges"
			case "privilege drift":
				f.schemaValues[1] = true // Schema CREATE is forbidden even when the inventory is readable.
				reads, queries = len(f.tx.queryRows), len(f.tx.queryResults)
				message = fmt.Sprintf("PostgreSQL api role %q does not match runtime privilege manifest v%d: schema CREATE is not permitted", privilegeTestAPI, runtimePrivilegeManifestVersion)
			}
			if cause != nil && mode != "privilege inspection" {
				f.tx.queryRows[reads-1] = scannerFunc(func(...any) error { return cause })
			}
			got, err := verifyRuntimeRole(t.Context(), f.tx, RuntimeRoleAPI)
			if err == nil || got != (RoleAttributes{}) || cause != nil && !errors.Is(err, cause) || !strings.Contains(err.Error(), message) {
				t.Fatalf("refused admission returned attributes=%+v error=%v, want %q", got, err, message)
			}
			if len(f.tx.queryRowStatements) != reads || len(f.tx.queryStatements) != queries || len(f.tx.statements) != 0 || f.tx.commits != 0 || f.tx.rollbacks != 0 {
				t.Fatalf("refused admission continued or took ownership: reads=%d queries=%d writes=%d commits=%d rollbacks=%d", len(f.tx.queryRowStatements), len(f.tx.queryStatements), len(f.tx.statements), f.tx.commits, f.tx.rollbacks)
			}
		})
	}
}

// Existing units own valid role projections, every unsafe attribute and either
// membership direction. These cases verify failure propagation and short-circuiting.
func TestRuntimeRoleBindingFailuresStopBeforeMutation(t *testing.T) {
	failure := errors.New("role catalog read failed")
	for _, mode := range []string{"owner query", "API is owner", "worker is owner", "API read", "worker read", "API attributes", "worker attributes", "API membership query", "worker membership query", "API member", "worker member"} {
		t.Run(mode, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: privilegeBindingRows()}
			api, worker := privilegeTestAPI, privilegeTestWorker
			reads, cause, message := 5, error(nil), ""
			switch mode {
			case "owner query":
				reads, cause = 1, failure
			case "API is owner":
				api, reads, message = privilegeTestOwner, 1, "distinct from the migration role"
			case "worker is owner":
				worker, reads, message = privilegeTestOwner, 3, "distinct from the migration role"
			case "API read":
				reads, cause = 2, pgx.ErrNoRows
			case "worker read":
				reads, cause = 4, failure
			case "API attributes":
				tx.queryRows[1], reads, message = privilegeRoleRow(api, true), 2, "NOBYPASSRLS"
			case "worker attributes":
				tx.queryRows[3], reads, message = privilegeRoleRow(worker, false), 4, "must have BYPASSRLS"
			case "API membership query":
				reads, cause = 3, failure
			case "worker membership query":
				reads, cause = 5, failure
			case "API member":
				tx.queryRows[2], reads, message = scriptedValueRow([]any{true}), 3, "must not participate"
			case "worker member":
				tx.queryRows[4], message = scriptedValueRow([]any{true}), "must not participate"
			}
			if cause != nil {
				tx.queryRows[reads-1] = scannerFunc(func(...any) error { return cause })
			}
			err := validateRuntimeRoleBindings(t.Context(), tx, api, worker)
			if err == nil || cause != nil && !errors.Is(err, cause) || message != "" && !strings.Contains(err.Error(), message) {
				t.Fatalf("binding failure=%v", err)
			}
			want := [][]any{nil, {api}, {api}, {worker}, {worker}}
			if !reflect.DeepEqual(tx.queryRowArguments, want[:reads]) || len(tx.statements) != 0 || len(tx.queryStatements) != 0 || tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatalf("binding calls=%v writes=%v", tx.queryRowArguments, tx.statements)
			}
		})
	}
}
