//go:build integration

package database

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPostgresRestoreSemanticSchema(t *testing.T) {
	db := newHousekeepingDatabase(t)
	var schema, apiRole, workerRole string
	if err := db.operator.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := db.api.QueryRow(t.Context(), `SELECT current_user`).Scan(&apiRole); err != nil {
		t.Fatal(err)
	}
	if err := db.worker.QueryRow(t.Context(), `SELECT current_user`).Scan(&workerRole); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(t.Context(), `CREATE EXTENSION vector WITH SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplyAndReconcileRuntimePrivileges(t.Context(), db.operator, apiRole, workerRole, true); err != nil {
		t.Fatal(err)
	}
	tx, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(t.Context(), tx)()
	if err := verifyRestoreTableInventory(t.Context(), tx, schema); err != nil {
		t.Fatal(err)
	}
	if err := verifyRestoreConstraints(t.Context(), tx, schema); err != nil {
		t.Fatal(err)
	}
	if err := verifyRestoreTriggers(t.Context(), tx, schema); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRestoreSemanticSchemaRejectsDrift(t *testing.T) {
	db := newHousekeepingDatabase(t)
	var schema, apiRole, workerRole string
	if err := db.operator.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := db.api.QueryRow(t.Context(), `SELECT current_user`).Scan(&apiRole); err != nil {
		t.Fatal(err)
	}
	if err := db.worker.QueryRow(t.Context(), `SELECT current_user`).Scan(&workerRole); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(t.Context(), `CREATE EXTENSION vector WITH SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplyAndReconcileRuntimePrivileges(t.Context(), db.operator, apiRole, workerRole, true); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"missing table":          `DROP TABLE semantic_members`,
		"unknown table":          `CREATE TABLE unexpected_semantic(id integer)`,
		"ledger checksum":        `UPDATE catalog_semantic_schema SET checksum=repeat('0',64)`,
		"RLS disabled":           `ALTER TABLE semantic_members DISABLE ROW LEVEL SECURITY`,
		"RLS not forced":         `ALTER TABLE semantic_members NO FORCE ROW LEVEL SECURITY`,
		"extra policy":           `CREATE POLICY unexpected ON semantic_members USING (true)`,
		"wrong predicate":        `ALTER POLICY tenant_isolation ON semantic_members USING (true)`,
		"wrong policy roles":     `ALTER POLICY tenant_isolation ON semantic_members TO ` + pgx.Identifier{apiRole}.Sanitize(),
		"missing constraint":     `ALTER TABLE semantic_members DROP CONSTRAINT semantic_members_pkey`,
		"extra constraint":       `ALTER TABLE semantic_members ADD CONSTRAINT unexpected CHECK (dimensions>0)`,
		"unvalidated constraint": `ALTER TABLE semantic_members ADD CONSTRAINT unexpected CHECK (dimensions>0) NOT VALID`,
		"missing guard":          `DROP TRIGGER semantic_generation_guard ON semantic_generations`,
		"disabled guard":         `ALTER TABLE semantic_generations DISABLE TRIGGER semantic_generation_guard`,
		"guard body":             `CREATE OR REPLACE FUNCTION catalog_semantic_generation_guard() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NEW; END'`,
		"guard definer":          `ALTER FUNCTION catalog_semantic_generation_guard() SECURITY DEFINER`,
	}
	for name, mutation := range cases {
		t.Run(name, func(t *testing.T) {
			tx, err := db.operator.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackForCleanup(t.Context(), tx)()
			if _, err := tx.Exec(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			if err := verifyRestoreTableInventory(t.Context(), tx, schema); err == nil {
				t.Fatal("accepted semantic schema drift")
			}
		})
	}
}
