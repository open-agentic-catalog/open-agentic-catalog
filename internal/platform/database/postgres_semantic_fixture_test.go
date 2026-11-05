// Semantic repository fixtures use the existing command coordinator. They do
// not open a pool, install pgvector or invoke an embedding provider.
package database

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const semanticSnapshotTestID = "00000000-0000-7000-8000-000000000902"

type semanticRepositoryFixture struct {
	store      *PostgresSemanticStore
	tx         *identityScriptedTx
	connection *captureCommandConnection
	command    *commandTransaction
	g          catalog.SemanticGeneration
}

func newSemanticRepositoryFixture(t *testing.T, read bool) semanticRepositoryFixture {
	t.Helper()
	pool := &pgxpool.Pool{} // Identity only; all operations use the scripted transaction.
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	f := semanticRepositoryFixture{
		store: &PostgresSemanticStore{repository: &PostgresRepository{pool: pool}, vectorSchema: `vector"schema`},
		tx:    tx, command: commandTransactionFrom(identityRepositoryContext(pool, tx)),
		g: catalog.SemanticGeneration{ID: "00000000-0000-7000-8000-000000000901", TenantID: paginationTestTenantID, Profile: "sha256:" + strings.Repeat("a", 64), Scope: "control", SourceGeneration: "7", Dimensions: 3},
	}
	if read {
		f.connection = &captureCommandConnection{transaction: tx}
		f.command = &commandTransaction{pool: pool, conn: f.connection, tenantID: f.g.TenantID}
	}
	return f
}

func (f semanticRepositoryFixture) context(t *testing.T) context.Context {
	t.Helper()
	return withCommandTransaction(t.Context(), f.command)
}

func (f semanticRepositoryFixture) checkRead(t *testing.T, err error, isolation pgx.TxIsoLevel) {
	t.Helper()
	if !reflect.DeepEqual(f.connection.options, []pgx.TxOptions{{IsoLevel: isolation, AccessMode: pgx.ReadOnly}}) {
		t.Fatalf("read transaction options = %v", f.connection.options)
	}
	if len(f.tx.statements) != 1 || !reflect.DeepEqual(f.tx.arguments[0], []any{f.g.TenantID, "", ""}) {
		t.Fatalf("read must only set tenant context: %v %v", f.tx.statements, f.tx.arguments)
	}
	commits, rollbacks := 1, 0
	if err != nil {
		commits, rollbacks = 0, 1
	}
	if f.tx.commits != commits || f.tx.rollbacks != rollbacks {
		t.Fatalf("read commits/rollbacks = %d/%d", f.tx.commits, f.tx.rollbacks)
	}
}

func (f semanticRepositoryFixture) checkWrite(t *testing.T, err error) {
	t.Helper()
	rollbacks := 0
	if err != nil {
		rollbacks = 1
	}
	if f.tx.commits != 0 || f.tx.rollbacks != rollbacks {
		t.Fatalf("command commits/rollbacks = %d/%d", f.tx.commits, f.tx.rollbacks)
	}
}

func semanticFaultRow(err error) pgx.Row {
	return scannerFunc(func(...any) error { return err })
}

func semanticLeaseArguments(g catalog.SemanticGeneration) []any {
	return []any{g.TenantID, g.ID, "worker", g.Profile, g.Scope}
}

func semanticGenerationArguments(g catalog.SemanticGeneration, ready bool) []any {
	return []any{g.TenantID, g.ID, g.Profile, g.Scope, g.SourceGeneration, g.Dimensions, ready}
}
