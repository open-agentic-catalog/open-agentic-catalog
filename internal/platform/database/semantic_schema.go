// Optional semantic schema provisioning stays under the existing migration lock.
// Neither application startup nor worker indexing installs extensions or runs DDL.
package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

const semanticSchemaVersion = 1

// SemanticSchemaPlan describes optional installation without provider inputs.
type SemanticSchemaPlan struct {
	Version         int    `json:"version"`
	Checksum        string `json:"checksum"`
	Applied         bool   `json:"applied"`
	ExtensionSchema string `json:"extensionSchema"`
}

// semanticSchemaInput returns immutable optional DDL and its independent checksum.
func semanticSchemaInput() ([]byte, string, error) {
	body, err := migrations.Files.ReadFile("semantic/000001.sql")
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

// PlanSemanticSchema checks administrator-provisioned pgvector and any existing
// optional checksum. It neither connects to inference nor changes the database.
func PlanSemanticSchema(ctx context.Context, pool *pgxpool.Pool) (SemanticSchemaPlan, error) {
	if pool == nil {
		return SemanticSchemaPlan{}, errors.New("PostgreSQL pool is required")
	}
	return planSemanticSchema(ctx, pool)
}

// planSemanticSchema resolves the administrator-owned extension schema. DDL uses
// quoted qualified names, never whichever vector type appears first in search_path.
func planSemanticSchema(ctx context.Context, query migrationPlanDatabase) (SemanticSchemaPlan, error) {
	_, checksum, err := semanticSchemaInput()
	if err != nil {
		return SemanticSchemaPlan{}, err
	}
	var extensionSchema string
	if err := query.QueryRow(ctx, `SELECT coalesce((SELECT n.nspname FROM pg_catalog.pg_extension e JOIN pg_catalog.pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector' AND e.extversion='0.8.7' AND n.nspname=ANY(current_schemas(false))),'')`).Scan(&extensionSchema); err != nil {
		return SemanticSchemaPlan{}, fmt.Errorf("inspect optional vector extension: %w", err)
	}
	if extensionSchema == "" {
		return SemanticSchemaPlan{}, errors.New("semantic setup requires administrator-provisioned pgvector 0.8.7 in an application-visible schema")
	}
	plan := SemanticSchemaPlan{Version: semanticSchemaVersion, Checksum: checksum, ExtensionSchema: extensionSchema}
	var exists bool
	if err := query.QueryRow(ctx, `SELECT to_regclass(format('%I.catalog_semantic_schema',current_schema())) IS NOT NULL`).Scan(&exists); err != nil {
		return plan, fmt.Errorf("inspect semantic schema ledger: %w", err)
	}
	if !exists {
		return plan, nil
	}
	var version int
	var stored string
	if err := query.QueryRow(ctx, `SELECT version,checksum FROM catalog_semantic_schema`).Scan(&version, &stored); err != nil {
		return plan, fmt.Errorf("read semantic schema ledger: %w", err)
	}
	if version != plan.Version || stored != checksum {
		return plan, errors.New("optional semantic schema checksum or version mismatch")
	}
	var isolated bool
	if err := query.QueryRow(ctx, `SELECT count(*)=3 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname IN ('semantic_generations','semantic_vectors','semantic_members') AND c.relkind='r' AND c.relrowsecurity AND c.relforcerowsecurity`).Scan(&isolated); err != nil {
		return plan, fmt.Errorf("inspect semantic tenant isolation: %w", err)
	}
	if !isolated {
		return plan, errors.New("optional semantic tables require forced tenant row security")
	}
	plan.Applied = true
	return plan, nil
}

// applySemanticSchema is called only with the outer schema-owner advisory lock.
// DDL and the checksum ledger commit together; repeated setup verifies, not rewrites.
func applySemanticSchema(ctx context.Context, database migrationDatabase) error {
	schema, owner, err := currentCatalogSchema(ctx, database)
	if err != nil {
		return err
	}
	if err := requireCatalogSchemaOwnership(ctx, database, schema, owner); err != nil {
		return err
	}
	plan, err := planSemanticSchema(ctx, database)
	if err != nil {
		return err
	}
	if plan.Applied {
		return nil
	}
	tx, err := database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer rollbackForCleanup(ctx, tx)()
	body, _, err := semanticSchemaInput()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, strings.ReplaceAll(string(body), "@vector@", pgx.Identifier{plan.ExtensionSchema}.Sanitize())); err != nil {
		return fmt.Errorf("apply optional semantic schema: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO catalog_semantic_schema(version,checksum) VALUES($1,$2)`, plan.Version, plan.Checksum); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// semanticTableColumns is the closed optional inventory. Core installation does
// not require these tables; a provisioned installation must contain every column.
var semanticTableColumns = map[string][]string{
	"catalog_semantic_schema": {"version", "checksum"},
	"semantic_generations":    {"tenant_id", "id", "profile", "scope", "disclosure", "source_generation", "dimensions", "state", "created_at", "updated_at"},
	"semantic_vectors":        {"tenant_id", "profile", "disclosure", "input_digest", "dimensions", "embedding", "created_at"},
	"semantic_members":        {"tenant_id", "generation_id", "item_key", "profile", "dimensions", "disclosure", "input_digest", "failure"},
}

// addSemanticPrivileges extends only the explicit optional table inventory.
// Vectors/members are insert-only; immutable rows cannot be rewritten by workers.
func addSemanticPrivileges(manifest runtimePrivilegeManifest, worker bool) runtimePrivilegeManifest {
	for table, columns := range semanticTableColumns {
		privilege := tablePrivilege{selectColumns: columns}
		if worker && table != "catalog_semantic_schema" {
			privilege.insertColumns = columns
			privilege.deleteRows = true
			if table == "semantic_generations" {
				privilege.insertColumns = columnsExcept(columns, "disclosure")
				privilege.updateColumns = []string{"state", "updated_at"}
			}
		}
		manifest.tables[table] = privilege
	}
	return manifest
}

// vectorFunctionAllowed enumerates only arithmetic and float-array conversion
// needed by this feature; installed extension functions remain denied otherwise.
func vectorFunctionAllowed(role, signature string) bool {
	switch signature {
	case "array_to_vector(real[], integer, boolean)":
		return role == RuntimeRoleAPI || role == RuntimeRoleWorker
	case "cosine_distance(vector, vector)":
		return role == RuntimeRoleAPI
	case "vector_dims(vector)", "vector_norm(vector)":
		return role == RuntimeRoleWorker
	default:
		return false
	}
}

// optionalSemanticGrant skips absent optional objects and refuses to grant
// access to lookalike tables not owned by the schema migration identity.
func optionalSemanticGrant(statement, table string) string {
	literal := semanticSQLLiteral(statement)
	tableLiteral := semanticSQLLiteral(table)
	body := `BEGIN IF to_regclass(` + tableLiteral + `) IS NOT NULL THEN
IF (SELECT pg_get_userbyid(relowner) FROM pg_catalog.pg_class WHERE oid=to_regclass(` + tableLiteral + `))<>current_user THEN RAISE EXCEPTION 'optional semantic table must belong to migration role'; END IF;
EXECUTE ` + literal + `; END IF; END`
	return "DO " + semanticSQLLiteral(body)
}

// semanticSQLLiteral uses explicit escape syntax so quoted role/schema names
// remain data even when a migration session disables standard_conforming_strings.
func semanticSQLLiteral(value string) string {
	return "E'" + strings.NewReplacer(`\`, `\\`, "'", "''").Replace(value) + "'"
}
