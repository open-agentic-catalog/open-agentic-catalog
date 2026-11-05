package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Optional metadata is validated before the existing core/data traversal.
// Derived vector quality and model configuration are outside this verifier.
func verifyRestoredSemanticSchema(ctx context.Context, tx pgx.Tx, schema string) error {
	plan, err := planSemanticSchema(ctx, tx)
	if err != nil || !plan.Applied {
		return errors.New("restored semantic schema version, checksum or vector extension is invalid")
	}
	var ledgerValid bool
	if err := tx.QueryRow(ctx, `SELECT count(*)=1 AND coalesce(bool_and(version=$1 AND checksum=$2),false) FROM `+pgx.Identifier{schema, "catalog_semantic_schema"}.Sanitize(), plan.Version, plan.Checksum).Scan(&ledgerValid); err != nil {
		return err
	}
	if !ledgerValid {
		return errors.New("restored semantic ledger is not exact")
	}
	names := make([]string, 0, len(semanticTableColumns))
	for name := range semanticTableColumns {
		names = append(names, name)
	}
	sort.Strings(names)
	rows, err := tx.Query(ctx, `SELECT c.relname,c.relowner=(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user),c.relrowsecurity,c.relforcerowsecurity,
 coalesce(p.polname,''),coalesce(p.polcmd,''),coalesce(p.polpermissive AND p.polroles=ARRAY[0::oid],false),
 coalesce(pg_catalog.pg_get_expr(p.polqual,p.polrelid),''),coalesce(pg_catalog.pg_get_expr(p.polwithcheck,p.polrelid),'')
 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 LEFT JOIN pg_catalog.pg_policy p ON p.polrelid=c.oid
 WHERE n.nspname=$1 AND c.relname=ANY($2::text[]) ORDER BY c.relname,p.polname LIMIT 5`, schema, names)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	const predicate = "(tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)"
	for rows.Next() {
		var table, name, command, qualifier, check string
		var owned, enabled, forced, permissive bool
		if err := rows.Scan(&table, &owned, &enabled, &forced, &name, &command, &permissive, &qualifier, &check); err != nil {
			rows.Close()
			return err
		}
		valid := owned && !seen[table]
		if table == "catalog_semantic_schema" {
			valid = valid && !enabled && !forced && name == ""
		} else {
			valid = valid && enabled && forced && name == "tenant_isolation" && command == "*" && permissive && qualifier == predicate && check == predicate
		}
		if !valid {
			rows.Close()
			return errors.New("restored semantic ownership or RLS policy drifted")
		}
		seen[table] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(seen) != 4 {
		return errors.New("restored semantic policy inventory is incomplete")
	}
	if err := verifyRestoredSemanticConstraints(ctx, tx, schema, names); err != nil {
		return err
	}
	return verifyRestoredSemanticGuard(ctx, tx, schema, names)
}

func verifyRestoredSemanticConstraints(ctx context.Context, tx pgx.Tx, schema string, names []string) error {
	rows, err := tx.Query(ctx, `SELECT c.relname,k.conname,k.contype,k.convalidated,pg_catalog.pg_get_constraintdef(k.oid,true)
 FROM pg_catalog.pg_constraint k JOIN pg_catalog.pg_class c ON c.oid=k.conrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname=$1 AND c.relname=ANY($2::text[]) ORDER BY c.relname,k.conname LIMIT 65`, schema, names)
	if err != nil {
		return err
	}
	defer rows.Close()
	var inventory strings.Builder
	count := 0
	for rows.Next() {
		count++
		var table, name, kind, definition string
		var valid bool
		if err := rows.Scan(&table, &name, &kind, &valid, &definition); err != nil {
			return err
		}
		if !valid || count > 64 {
			return errors.New("restored semantic constraint inventory is invalid")
		}
		if count > 1 {
			inventory.WriteByte('\x1e')
		}
		inventory.WriteString(strings.Join([]string{table, name, kind, fmt.Sprint(valid), strings.Join(strings.Fields(definition), " ")}, "\x1f"))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(inventory.String()))
	if hex.EncodeToString(digest[:]) != "bf00df893ffc68bcb22641c784ca113efad0e9c6bd4eb642178331bf0cc16943" {
		return fmt.Errorf("restored semantic constraint inventory drifted (observed digest %x)", digest)
	}
	return nil
}

func verifyRestoredSemanticGuard(ctx context.Context, tx pgx.Tx, schema string, names []string) error {
	rows, err := tx.Query(ctx, `SELECT t.tgname,c.relname,p.proname,t.tgenabled,pg_catalog.pg_get_triggerdef(t.oid,true),p.prosrc,
 l.lanname,p.prosecdef,p.proowner=c.relowner AND p.pronamespace=c.relnamespace,coalesce(p.proconfig,ARRAY[]::text[])
 FROM pg_catalog.pg_trigger t JOIN pg_catalog.pg_class c ON c.oid=t.tgrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_catalog.pg_proc p ON p.oid=t.tgfoid JOIN pg_catalog.pg_language l ON l.oid=p.prolang
 WHERE n.nspname=$1 AND c.relname=ANY($2::text[]) AND NOT t.tgisinternal ORDER BY t.tgname LIMIT 2`, schema, names)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	body, _, err := semanticSchemaInput()
	if err != nil {
		return err
	}
	const start = "AS $guard$"
	_, remaining, ok := strings.Cut(string(body), start)
	source, _, terminated := strings.Cut(remaining, "$guard$;")
	if !ok || !terminated {
		return errors.New("embedded semantic guard source is missing")
	}
	expected := `CREATE TRIGGER semantic_generation_guard BEFORE UPDATE ON ` + pgx.Identifier{schema}.Sanitize() + `.semantic_generations FOR EACH ROW EXECUTE FUNCTION catalog_semantic_generation_guard()`
	for rows.Next() {
		count++
		var name, table, function, enabled, definition, actual, language string
		var definer, owned bool
		var settings []string
		if err := rows.Scan(&name, &table, &function, &enabled, &definition, &actual, &language, &definer, &owned, &settings); err != nil {
			return err
		}
		// pg_get_triggerdef omits a schema only when it is visible in search_path.
		definition = strings.ReplaceAll(definition, pgx.Identifier{schema}.Sanitize()+".", "")
		expected = strings.ReplaceAll(expected, pgx.Identifier{schema}.Sanitize()+".", "")
		if count != 1 || name != "semantic_generation_guard" || table != "semantic_generations" || function != "catalog_semantic_generation_guard" || enabled != "O" || language != "plpgsql" || definer || !owned || len(settings) != 0 || strings.TrimSpace(actual) != strings.TrimSpace(source) || strings.Join(strings.Fields(definition), " ") != expected {
			return errors.New("restored semantic guard drifted")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("restored semantic guard is missing")
	}
	return nil
}
