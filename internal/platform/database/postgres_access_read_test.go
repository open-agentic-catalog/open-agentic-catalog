// Identity repository units use the existing tenant transaction coordinator.
package database

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func accessRepositoryFixture(t *testing.T) (*PostgresIdentityStore, *identityScriptedTx, context.Context, identity.Membership) {
	t.Helper()
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdentityStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	member := identity.Membership{ID: "00000000-0000-7000-8000-000000000010", TenantID: restoreTestTenant, RealmID: "00000000-0000-7000-8000-000000000100", Subject: "member", ActorType: identity.ActorHuman, State: identity.MembershipActive, Revision: 1, CreatedAt: at, UpdatedAt: at}
	return store, tx, identityRepositoryContext(pool, tx), member
}

func accessAssignment(member identity.Membership) identity.RoleAssignment {
	return identity.RoleAssignment{ID: "00000000-0000-7000-8000-000000000020", TenantID: member.TenantID, MembershipID: member.ID, Subject: member.Subject, ActorType: member.ActorType, Role: identity.RoleContributor, NamespaceID: "00000000-0000-7000-8000-000000000030", State: identity.AssignmentActive, Revision: 1, GrantedBy: "admin", GrantReason: "grant access", GrantedAt: member.CreatedAt}
}

func accessAssignmentValues(a identity.RoleAssignment) []any {
	return []any{a.ID, a.TenantID, a.MembershipID, a.Subject, a.ActorType, a.Role, a.NamespaceID, a.State, a.Revision, a.GrantedBy, a.GrantReason, a.GrantedAt, a.RevokedBy, a.RevokeReason, a.RevokedAt}
}

// Resolution owns a direct pool boundary; malformed verified input is rejected
// before that boundary, without invoking a resolver or opening a connection.
func TestIdentityResolutionRejectsIncompletePrincipals(t *testing.T) {
	for _, mode := range []string{"issuer", "subject", "long subject", "control subject", "no tenancy", "both tenancy selectors", "invalid tenant"} {
		t.Run(mode, func(t *testing.T) {
			store, _, ctx, member := accessRepositoryFixture(t)
			principal := identity.Principal{Issuer: "https://identity.example.com", Subject: member.Subject, TenantID: member.TenantID}
			switch mode {
			case "issuer":
				principal.Issuer = ""
			case "subject":
				principal.Subject = ""
			case "long subject":
				principal.Subject = strings.Repeat("s", 513)
			case "control subject":
				principal.Subject = "member\n"
			case "no tenancy":
				principal.TenantID = ""
			case "both tenancy selectors":
				principal.ExternalOrganization = "organization"
			case "invalid tenant":
				principal.TenantID = "not-a-uuid"
			}
			got, err := store.Resolve(ctx, principal)
			if identity.ErrorCodeOf(err) != identity.CodeUnauthenticated || !reflect.DeepEqual(got, identity.Actor{}) {
				t.Fatalf("principal admitted: %+v %v", got, err)
			}
		})
	}
}

// Existing populated reads and membership query/iteration failures remain in
// postgres_membership_test.go; these cover the missing wrapper cases.
func TestIdentityMembershipReadFailuresAndEmptyPage(t *testing.T) {
	for _, mode := range []string{"empty", "scan", "missing exact", "failed exact"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, member := accessRepositoryFixture(t)
			failure := errors.New("membership read failure")
			if strings.HasSuffix(mode, "exact") {
				cause := failure
				code := identity.CodeDependency
				if mode == "missing exact" {
					cause = pgx.ErrNoRows
					code = identity.CodeNotFound
				}
				tx.queryRows = []pgx.Row{scannerFunc(func(...any) error { return cause })}
				got, err := store.GetMembership(ctx, member.TenantID, member.ID)
				if identity.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, identity.Membership{}) || !reflect.DeepEqual(tx.queryRowArguments, [][]any{{member.TenantID, member.ID}}) {
					t.Fatalf("exact=%+v %v args=%v", got, err, tx.queryRowArguments)
				}
				if mode == "failed exact" && !errors.Is(err, failure) {
					t.Fatal("read cause lost")
				}
				return
			}
			rows := &identityScriptedRows{}
			if mode == "scan" {
				rows.values = [][]any{{}}
			}
			tx.queryResults = []identityQueryResult{{rows: rows}}
			got, err := store.ListMemberships(ctx, member.TenantID, "before", member.ID, 2)
			if mode == "empty" {
				if err != nil || got == nil || len(got) != 0 {
					t.Fatalf("empty=%v %v", got, err)
				}
			} else if identity.ErrorCodeOf(err) != identity.CodeDependency || got != nil {
				t.Fatalf("scan=%v %v", got, err)
			}
			if !rows.closed || !reflect.DeepEqual(tx.queryArguments, [][]any{{member.TenantID, "before", member.ID, 2}}) {
				t.Fatalf("scope/cleanup=%v %t", tx.queryArguments, rows.closed)
			}
		})
	}
}

func TestIdentityRolePagesDiscardPartialResults(t *testing.T) {
	failure := errors.New("role page failure")
	for _, mode := range []string{"empty", "query", "scan", "iteration"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, member := accessRepositoryFixture(t)
			rows := &identityScriptedRows{values: [][]any{accessAssignmentValues(accessAssignment(member))}}
			result := identityQueryResult{rows: rows}
			switch mode {
			case "empty":
				rows.values = nil
			case "query":
				result.err = failure
			case "scan":
				rows.values = append(rows.values, []any{})
			case "iteration":
				rows.err = failure
			}
			tx.queryResults = []identityQueryResult{result}
			request := identity.RoleAssignmentListRequest{MembershipID: member.ID, AfterSubject: "before", AfterNamespaceID: "00000000-0000-7000-8000-000000000029", AfterRole: identity.RoleContributor, AfterID: accessAssignment(member).ID, Limit: 2}
			got, err := store.ListRoleAssignments(ctx, member.TenantID, request)
			if mode == "empty" {
				if err != nil || got == nil || len(got) != 0 {
					t.Fatalf("empty=%v %v", got, err)
				}
			} else if identity.ErrorCodeOf(err) != identity.CodeDependency || got != nil {
				t.Fatalf("partial=%v %v", got, err)
			}
			if (mode == "query" || mode == "iteration") && !errors.Is(err, failure) {
				t.Fatal("cause lost")
			}
			if mode != "query" && !rows.closed {
				t.Fatal("role rows leaked")
			}
			if !reflect.DeepEqual(tx.queryArguments, [][]any{{member.TenantID, request.AfterSubject, request.AfterNamespaceID, request.AfterRole, request.AfterID, request.Limit, member.ID}}) {
				t.Fatalf("role scope=%v", tx.queryArguments)
			}
		})
	}
}
