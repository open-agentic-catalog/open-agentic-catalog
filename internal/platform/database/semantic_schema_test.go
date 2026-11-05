package database

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

func semanticApplyFixture(t *testing.T, extensionSchema string, applied bool) (*migrationScriptedDatabase, *fakeCommandTx) {
	t.Helper()
	_, checksum, err := semanticSchemaInput()
	if err != nil {
		t.Fatal(err)
	}
	rows := []pgx.Row{
		scriptedValueRow([]any{privilegeTestSchema, privilegeTestOwner}),
		scriptedValueRow([]any{privilegeTestOwner, privilegeTestOwner}),
		scriptedValueRow([]any{extensionSchema}),
		scriptedValueRow([]any{applied}),
	}
	if applied {
		rows = append(rows, scriptedValueRow([]any{semanticSchemaVersion, checksum}), scriptedValueRow([]any{true}))
	}
	tx := &fakeCommandTx{}
	database := &migrationScriptedDatabase{
		identityScriptedTx: &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: rows},
		transactions:       []pgx.Tx{tx},
	}
	return database, tx
}

func TestApplySemanticSchemaInstallsQualifiedDDLAndLedger(t *testing.T) {
	body, err := migrations.Files.ReadFile("semantic/000001.sql")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	checksum := hex.EncodeToString(sum[:])
	for _, test := range []struct{ schema, qualified string }{
		{"extensions", `"extensions"`}, {`extension"schema`, `"extension""schema"`},
	} {
		for _, installed := range []bool{false, true} {
			name := test.schema + "/new setup"
			if installed {
				name = test.schema + "/verified existing setup"
			}
			t.Run(name, func(t *testing.T) {
				database, tx := semanticApplyFixture(t, test.schema, installed)
				if err := applySemanticSchema(t.Context(), database); err != nil {
					t.Fatal(err)
				}
				if len(database.queryRows) != 0 || len(database.statements) != 0 || len(database.queryStatements) != 0 || !reflect.DeepEqual(database.queryRowArguments[1], []any{privilegeTestSchema}) {
					t.Fatal("incomplete/scopeless preflight or writes outside the transaction")
				}
				if installed {
					if len(database.transactions) != 1 || len(tx.statements) != 0 || tx.commits != 0 || tx.rollbacks != 0 {
						t.Fatal("verified existing semantic schema was rewritten")
					}
					return
				}
				wantStatements := []string{strings.ReplaceAll(string(body), "@vector@", test.qualified), "INSERT INTO catalog_semantic_schema(version,checksum) VALUES($1,$2)"}
				if !slices.Equal(tx.statements, wantStatements) || !reflect.DeepEqual(tx.arguments, [][]any{nil, {semanticSchemaVersion, checksum}}) || len(database.transactions) != 0 || tx.commits != 1 || tx.rollbacks != 1 {
					t.Fatalf("semantic installation: writes=%d args=%v commits=%d rollbacks=%d", len(tx.statements), tx.arguments, tx.commits, tx.rollbacks)
				}
			})
		}
	}
}

func TestApplySemanticSchemaStopsAtFailures(t *testing.T) {
	injected := errors.New("semantic setup unavailable")
	for _, stage := range []string{"schema lookup", "owner lookup", "owner mismatch", "plan lookup", "missing extension", "begin", "DDL", "ledger", "commit"} {
		t.Run(stage, func(t *testing.T) {
			database, tx := semanticApplyFixture(t, "extensions", false)
			reads, writes, commits, rollbacks, queued := 4, 0, 0, 0, 1
			message, cause := "", injected
			switch stage {
			case "schema lookup":
				database.queryRows[0] = scannerFunc(func(...any) error { return injected })
				reads, message = 1, "resolve active Catalog schema"
			case "owner lookup":
				database.queryRows[1] = scannerFunc(func(...any) error { return injected })
				reads, message = 2, "inspect Catalog schema owner"
			case "owner mismatch":
				database.queryRows[1] = scriptedValueRow([]any{"other_owner", "other_owner"})
				reads, message, cause = 2, "must be owned by migration role", nil
			case "plan lookup":
				database.queryRows[2] = scannerFunc(func(...any) error { return injected })
				reads, message = 3, "inspect optional vector extension"
			case "missing extension":
				database.queryRows[2] = scriptedValueRow([]any{""})
				reads, message, cause = 3, "requires administrator-provisioned pgvector", nil
			case "begin":
				database.beginErr = injected
			case "DDL":
				tx.execErrors = []error{injected}
				writes, rollbacks, queued, message = 1, 1, 0, "apply optional semantic schema"
			case "ledger":
				tx.execErrors = []error{nil, injected}
				writes, rollbacks, queued = 2, 1, 0
			case "commit":
				tx.commitErr = injected
				writes, commits, rollbacks, queued = 2, 1, 1, 0
			}
			err := applySemanticSchema(t.Context(), database)
			if err == nil || cause != nil && !errors.Is(err, cause) || message != "" && !strings.Contains(err.Error(), message) {
				t.Fatalf("failure = %v", err)
			}
			if len(database.queryRowStatements) != reads || len(database.transactions) != queued || len(database.statements) != 0 || len(tx.statements) != writes || tx.commits != commits || tx.rollbacks != rollbacks {
				t.Fatalf("failure cleanup: reads=%d transactions=%d writes=%d commits=%d rollbacks=%d", len(database.queryRowStatements), len(database.transactions), len(tx.statements), tx.commits, tx.rollbacks)
			}
		})
	}
}
