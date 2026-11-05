package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

type migrationScriptedDatabase struct {
	*identityScriptedTx
	transactions []pgx.Tx
	beginErr     error
}

func (database *migrationScriptedDatabase) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	if database.beginErr != nil {
		return nil, database.beginErr
	}
	if len(database.transactions) == 0 {
		return nil, errors.New("unexpected migration transaction")
	}
	transaction := database.transactions[0]
	database.transactions = database.transactions[1:]
	return transaction, nil
}

func TestPlanMigrationsLeavesAnEmptySchemaUnchanged(t *testing.T) {
	t.Parallel()

	transaction := &identityScriptedTx{
		fakeCommandTx: &fakeCommandTx{},
		queryRows: []pgx.Row{scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, false)
			return nil
		})},
	}
	plan, err := planMigrations(context.Background(), transaction)
	if err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 || len(plan) != len(names) || slices.ContainsFunc(plan, func(migration Migration) bool { return migration.Applied }) {
		t.Fatalf("empty-schema migration plan = %#v", plan)
	}
	for i, migration := range plan {
		if migration.Name != names[i] {
			t.Fatalf("migration %d name = %q, want %q", i, migration.Name, names[i])
		}
		versionText, _, _ := strings.Cut(names[i], "_")
		version, err := strconv.ParseInt(versionText, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		body, err := migrations.Files.ReadFile(names[i])
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if migration.Checksum != hex.EncodeToString(sum[:]) || migration.Version != version || migration.Version <= 0 || i > 0 && migration.Version <= plan[i-1].Version {
			t.Fatalf("migration metadata or ordering changed: %+v", migration)
		}
	}
	if len(transaction.statements) != 0 {
		t.Fatalf("migration plan changed the database: %#v", transaction.statements)
	}
	if len(transaction.queryRowStatements) != 1 || !strings.Contains(transaction.queryRowStatements[0], "pg_catalog.pg_class") {
		t.Fatalf("migration ledger inspection = %#v", transaction.queryRowStatements)
	}
}

func TestEmbeddedSchemaAndPrivilegeManifestVersionsAreReported(t *testing.T) {
	t.Parallel()
	version, err := EmbeddedSchemaVersion()
	if err != nil || version != 47 {
		t.Fatalf("embedded schema version = %d, %v", version, err)
	}
	if RuntimePrivilegeManifestVersion() != runtimePrivilegeManifestVersion || RuntimePrivilegeManifestVersion() <= 0 {
		t.Fatalf("runtime privilege manifest version = %d", RuntimePrivilegeManifestVersion())
	}
}

func TestPlanMigrationsRejectsAppliedChecksumDrift(t *testing.T) {
	t.Parallel()

	transaction := &identityScriptedTx{
		fakeCommandTx: &fakeCommandTx{},
		queryRows: []pgx.Row{
			scannerFunc(func(destinations ...any) error {
				setScanDestination(t, destinations, 0, true)
				return nil
			}),
			scannerFunc(func(destinations ...any) error {
				setScanDestination(t, destinations, 0, "changed-checksum")
				return nil
			}),
		},
	}
	if _, err := planMigrations(context.Background(), transaction); err == nil || !strings.Contains(err.Error(), "migration 1 checksum changed after application") {
		t.Fatalf("changed applied migration returned %v", err)
	}
	if len(transaction.statements) != 0 {
		t.Fatalf("checksum validation changed the database: %#v", transaction.statements)
	}
}

func TestPlanMigrationsRejectsUnknownAppliedVersion(t *testing.T) {
	t.Parallel()

	queryRows := []pgx.Row{scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, true)
		return nil
	})}
	for range 47 {
		queryRows = append(queryRows, scannerFunc(func(...any) error { return pgx.ErrNoRows }))
	}
	queryRows = append(queryRows, scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, int64(99))
		return nil
	}))
	transaction := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: queryRows}
	if _, err := planMigrations(context.Background(), transaction); err == nil || !strings.Contains(err.Error(), "applied migration version 99 is absent from the embedded migration bundle") {
		t.Fatalf("unknown applied migration returned %v", err)
	}
	if len(transaction.statements) != 0 {
		t.Fatalf("unknown migration inspection changed the database: %#v", transaction.statements)
	}
	arguments := transaction.queryRowArguments[len(transaction.queryRowArguments)-1]
	versions, ok := arguments[0].([]int64)
	if !ok || len(versions) != 47 || versions[0] != 1 || versions[len(versions)-1] != 47 {
		t.Fatalf("embedded migration version binding = %#v", arguments)
	}
}

func TestApplyPendingMigrationsRollsBackBeforeCommit(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("migration failure")
	for _, testCase := range []struct {
		name              string
		beginErr          error
		execErrors        []error
		commitErr         error
		errorText         string
		expectedExecs     int
		expectedCommits   int
		expectedRollbacks int
	}{
		{name: "begin", beginErr: sentinel, errorText: "begin migration 1"},
		{name: "migration body", execErrors: []error{sentinel}, errorText: "apply migration 1", expectedExecs: 1, expectedRollbacks: 1},
		{name: "migration ledger", execErrors: []error{nil, sentinel}, errorText: "record migration 1", expectedExecs: 2, expectedRollbacks: 1},
		{name: "commit", commitErr: sentinel, errorText: "commit migration 1", expectedExecs: 2, expectedCommits: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transaction := &fakeCommandTx{execErrors: testCase.execErrors, commitErr: testCase.commitErr}
			database := &migrationScriptedDatabase{
				identityScriptedTx: &identityScriptedTx{
					fakeCommandTx: &fakeCommandTx{},
					queryRows: []pgx.Row{scannerFunc(func(destinations ...any) error {
						setScanDestination(t, destinations, 0, false)
						return nil
					})},
				},
				transactions: []pgx.Tx{transaction},
				beginErr:     testCase.beginErr,
			}
			applied, err := applyPendingMigrations(context.Background(), database)
			if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), testCase.errorText) {
				t.Fatalf("migration failure = %v", err)
			}
			if len(applied) != 0 {
				t.Fatalf("failed migration reported applied entries: %#v", applied)
			}
			if len(transaction.statements) != testCase.expectedExecs || transaction.commits != testCase.expectedCommits || transaction.rollbacks != testCase.expectedRollbacks {
				t.Fatalf("transaction state: statements=%d commits=%d rollbacks=%d", len(transaction.statements), transaction.commits, transaction.rollbacks)
			}
		})
	}
}
