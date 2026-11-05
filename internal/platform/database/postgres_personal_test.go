package database

import (
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
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
)

func personalStorageControl(t *testing.T, tx *identityScriptedTx) (*PostgresPersonalStore, context.Context, identity.Actor, *commandTransaction) {
	t.Helper()
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresPersonalStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman}
	ctx := identity.WithActor(identityRepositoryContext(pool, tx), actor)
	return store, ctx, actor, commandTransactionFrom(ctx)
}

func personalStorageItem() personal.Item {
	now := time.Date(2026, time.October, 24, 12, 0, 0, 0, time.UTC)
	return personal.Item{ID: "00000000-0000-7000-8000-000000000004", Input: personal.Input{Type: "search", Label: "Exact_%[x]", Search: &personal.Criteria{Text: "rate limits"}}, Revision: 1, CreatedAt: now, UpdatedAt: now}
}

func personalStorageValues(item personal.Item, raw string) []any {
	return []any{item.ID, []byte(raw), item.Revision, item.CreatedAt, item.UpdatedAt}
}

const personalStorageInput = `{"type":"search","label":"Exact_%[x]","search":{"text":"rate limits"}}`

func TestPostgresPersonalDecodesStoredInputAndRejectsInvalidEvidence(t *testing.T) {
	if _, err := NewPostgresPersonalStore(nil); err == nil {
		t.Fatal("personal store accepted a missing repository")
	}
	want := personalStorageItem()
	item, err := scanPersonal(scriptedValueRow(personalStorageValues(want, personalStorageInput)))
	if err != nil || !reflect.DeepEqual(item, want) {
		t.Fatalf("decoded saved item = %#v, %v", item, err)
	}
	for _, raw := range []string{
		`{`,
		`{"type":"search","label":"Exact","search":{"text":"rate limits"},"owner":"forged"}`,
		`{"type":"resource","label":"Missing reference"}`,
	} {
		item, err := scanPersonal(scriptedValueRow(personalStorageValues(want, raw)))
		if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || !reflect.DeepEqual(item, personal.Item{}) {
			t.Fatalf("invalid retained input returned %#v, %v", item, err)
		}
	}
	if _, err := scanPersonal(scannerFunc(func(...any) error { return pgx.ErrNoRows })); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing saved item returned %v", err)
	}
}

func TestPostgresPersonalReadsBindOwnerAndLiteralFilters(t *testing.T) {
	want := personalStorageItem()
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow(personalStorageValues(want, personalStorageInput))}}
	store, ctx, actor, _ := personalStorageControl(t, tx)
	item, err := store.Get(ctx, actor, want.ID)
	if err != nil || !reflect.DeepEqual(item, want) {
		t.Fatalf("read saved item = %#v, %v", item, err)
	}
	if wantArgs := []any{actor.TenantID, actor.MembershipID, actor.RealmID, want.ID}; !reflect.DeepEqual(tx.queryRowArguments[0], wantArgs) {
		t.Fatalf("saved item owner binding = %#v", tx.queryRowArguments)
	}
	rows := &identityScriptedRows{values: [][]any{personalStorageValues(want, personalStorageInput)}}
	tx = &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: rows}}}
	store, ctx, actor, _ = personalStorageControl(t, tx)
	label := want.Label
	query := personal.ListRequest{Type: "search", AfterID: "00000000-0000-7000-8000-000000000000", Limit: 2, Label: &label}
	items, err := store.List(ctx, actor, query)
	if err != nil || !reflect.DeepEqual(items, []personal.Item{want}) || !rows.closed {
		t.Fatalf("saved item page = %#v, closed=%t, %v", items, rows.closed, err)
	}
	wantArgs := []any{actor.TenantID, actor.MembershipID, actor.RealmID, "search", query.AfterID, 2, &label}
	if !reflect.DeepEqual(tx.queryArguments[0], wantArgs) || strings.Contains(tx.queryStatements[0], label) {
		t.Fatalf("literal filter or private owner binding lost: %#v", tx.queryArguments)
	}
	for _, change := range []func(*identity.Actor){
		func(a *identity.Actor) { a.TenantID = "different" },
		func(a *identity.Actor) { a.MembershipID = "different" },
		func(a *identity.Actor) { a.RealmID = "different" },
	} {
		other := actor
		change(&other)
		if _, err := store.Get(ctx, other, want.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
			t.Fatalf("caller-selected owner was accepted: %v", err)
		}
	}
	if len(tx.queryArguments) != 1 || len(tx.queryRowArguments) != 0 {
		t.Fatal("refused owner reached the database")
	}
}

func TestPostgresPersonalListRefusalsReturnNoPartialPage(t *testing.T) {
	for _, kind := range []string{"owner", "limit", "label", "query", "decode", "iteration"} {
		t.Run(kind, func(t *testing.T) {
			rows := &identityScriptedRows{values: [][]any{personalStorageValues(personalStorageItem(), personalStorageInput)}}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: rows}}}
			store, ctx, actor, _ := personalStorageControl(t, tx)
			query := personal.ListRequest{Limit: 2}
			wantCode := catalog.CodeDependencyUnavailable
			switch kind {
			case "owner":
				actor.MembershipID = "forged"
				wantCode = catalog.CodeForbidden
			case "limit":
				query.Limit = 102
				wantCode = catalog.CodeInvalidArgument
			case "label":
				label := strings.Repeat("x", 121)
				query.Label = &label
				wantCode = catalog.CodeInvalidArgument
			case "query":
				tx.queryResults[0].err = errors.New("query failed")
			case "decode":
				rows.values = append(rows.values, personalStorageValues(personalStorageItem(), `{"type":"resource"}`))
			case "iteration":
				rows.err = errors.New("iteration failed")
			}
			items, err := store.List(ctx, actor, query)
			if catalog.ErrorCodeOf(err) != wantCode || items != nil {
				t.Fatalf("refused page = %#v, %v", items, err)
			}
			if (kind == "decode" || kind == "iteration") && !rows.closed {
				t.Fatal("failed page retained its rows")
			}
			if (kind == "owner" || kind == "limit" || kind == "label") && len(tx.queryStatements) != 0 {
				t.Fatal("invalid private page reached the database")
			}
		})
	}
}

func TestPostgresPersonalWritesKeepMutationAndAuditAtomic(t *testing.T) {
	for _, kind := range []string{"create", "update", "remove", "quota", "audit failure", "input constraint"} {
		t.Run(kind, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
			store, ctx, actor, command := personalStorageControl(t, tx)
			item := personalStorageItem()
			expected, remove, action := int64(0), false, "personal.item.created"
			tx.queryRows = []pgx.Row{scriptedValueRow([]any{actor.MembershipID}), scriptedValueRow([]any{0})}
			if kind == "update" || kind == "remove" {
				old := item
				expected, item.Revision, item.UpdatedAt = 1, 2, item.UpdatedAt.Add(time.Minute)
				tx.queryRows[1] = scriptedValueRow(personalStorageValues(old, personalStorageInput))
				action = "personal.item.updated"
				if kind == "remove" {
					remove, action = true, "personal.item.removed"
				}
			}
			switch kind {
			case "quota":
				tx.queryRows[1] = scriptedValueRow([]any{200})
			case "audit failure":
				tx.execErrors = []error{nil, errors.New("audit unavailable")}
			case "input constraint":
				tx.execErrors = []error{&pgconn.PgError{Code: "23514", ConstraintName: "personal_items_input_check"}}
			}
			evidence, err := audit.NewRecord("00000000-0000-7000-8000-000000000005", actor.TenantID,
				audit.Principal{ID: actor.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: "human", RealmID: actor.RealmID},
				action, "personal_item", item.ID, "", audit.Execution{}, map[string]any{"revision": item.Revision}, item.UpdatedAt)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := store.Save(ctx, actor, item, expected, remove, evidence)
			if kind == "quota" || kind == "audit failure" || kind == "input constraint" {
				wantCode := catalog.CodeConflict
				if kind == "audit failure" {
					wantCode = catalog.CodeDependencyUnavailable
				} else if kind == "input constraint" {
					wantCode = catalog.CodeInvalidArgument
				}
				if catalog.ErrorCodeOf(err) != wantCode || !reflect.DeepEqual(actual, personal.Item{}) || tx.rollbacks != 1 || tx.commits != 0 || command.failed == nil {
					t.Fatalf("failed saved item = %#v, rollback=%d commit=%d, %v", actual, tx.rollbacks, tx.commits, err)
				}
				if kind == "quota" && len(tx.statements) != 0 || kind == "input constraint" && len(tx.statements) != 1 {
					t.Fatal("refused save wrote extra mutation or audit records")
				}
				return
			}
			if err != nil || !reflect.DeepEqual(actual, item) || len(tx.statements) != 2 || tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatalf("saved item = %#v, statements=%d commit=%d rollback=%d, %v", actual, len(tx.statements), tx.commits, tx.rollbacks, err)
			}
			if !reflect.DeepEqual(tx.arguments[0][:4], []any{actor.TenantID, actor.MembershipID, actor.RealmID, item.ID}) {
				t.Fatalf("save changed its selected owner or item: %#v", tx.arguments[0])
			}
			if !strings.Contains(tx.statements[1], "INSERT INTO audit_events") || tx.arguments[1][2] != actor.TenantID || tx.arguments[1][3] != actor.MembershipID || tx.arguments[1][4] != action || tx.arguments[1][5] != "personal_item" || tx.arguments[1][6] != item.ID {
				t.Fatal("save lost its owner-bound audit record")
			}
			var retained struct {
				RealmID       string `json:"realmId"`
				PrincipalType string `json:"principalType"`
				ActorType     string `json:"actorType"`
				Facts         struct {
					Revision int64 `json:"revision"`
				} `json:"facts"`
			}
			rawAudit, ok := tx.arguments[1][11].([]byte)
			if !ok {
				t.Fatalf("saved audit details have an unexpected type: %T", tx.arguments[1][11])
			}
			if err := json.Unmarshal(rawAudit, &retained); err != nil || retained.RealmID != actor.RealmID || retained.PrincipalType != "catalog_membership" || retained.ActorType != "human" || retained.Facts.Revision != item.Revision {
				t.Fatalf("save lost private attribution or revision facts: %#v, %v", retained, err)
			}
			if kind == "remove" && !strings.HasPrefix(tx.statements[0], "DELETE FROM personal_items") || kind == "update" && !strings.HasPrefix(tx.statements[0], "UPDATE personal_items") || kind == "create" && !strings.HasPrefix(tx.statements[0], "INSERT INTO personal_items") {
				t.Fatal("save performed a different transition")
			}
			if err := command.complete(ctx, 200, func(context.Context, pgx.Tx) error { return nil }); err != nil || tx.commits != 1 {
				t.Fatalf("shared save did not commit once: %v, commits=%d", err, tx.commits)
			}
		})
	}
}
