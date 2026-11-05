// Restore schema checks compare the ledger, objects, RLS, constraints and triggers
// with embedded expectations before data verification.

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

const (
	maxRestoreSchemaConstraints = 2048
	maxRestoreSchemaTriggers    = 128
	maxRestoreSchemaFunctions   = 256
)

// verifyRestoreSchema records migration, ownership, privilege and guard
// findings and reports whether tenant data scans can safely proceed. A usable
// schema can still have integrity findings that prevent recovery qualification.
func verifyRestoreSchema(ctx context.Context, tx pgx.Tx, options RestoreVerificationOptions, report *RestoreVerificationReport) (bool, error) {
	embeddedVersion, err := EmbeddedSchemaVersion()
	if err != nil {
		return false, err
	}
	report.SchemaVersion = embeddedVersion
	usable := true
	plan, planErr := planMigrations(ctx, tx)
	if planErr != nil {
		usable = false
		appendRestoreFinding(report, *integrityFinding("RESTORE_MIGRATION_LEDGER_INVALID", "schema", "", "stop; use the exact compatible migration binary and investigate the immutable ledger before any mutation"))
	} else {
		for _, migration := range plan {
			if !migration.Applied {
				usable = false
				appendRestoreFinding(report, *integrityFinding("RESTORE_MIGRATION_PENDING", "schema", strconvVersion(migration.Version), "stop; review the exact migration plan and apply it with runtimes stopped before verification"))
				break
			}
		}
	}
	var serverVersion int
	if err := tx.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&serverVersion); err != nil {
		return false, fmt.Errorf("inspect restored PostgreSQL version: %w", err)
	}
	if serverVersion/10000 != 18 {
		usable = false
		appendRestoreFinding(report, *integrityFinding("RESTORE_POSTGRES_MAJOR_INVALID", "schema", "", "restore into the supported PostgreSQL 18 major before qualification"))
	}
	var hasCrypto bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_extension WHERE extname='pgcrypto')`).Scan(&hasCrypto); err != nil {
		return false, fmt.Errorf("inspect restored pgcrypto extension: %w", err)
	}
	if !hasCrypto {
		usable = false
		appendRestoreFinding(report, *integrityFinding("RESTORE_PGCRYPTO_MISSING", "schema", "", "restore the required pgcrypto extension before qualification"))
	}
	schema, migrationRole, err := currentCatalogSchema(ctx, tx)
	if err != nil {
		return false, err
	}
	if err := requireCatalogSchemaOwnership(ctx, tx, schema, migrationRole); err != nil {
		usable = false
		appendRestoreFinding(report, *integrityFinding("RESTORE_SCHEMA_OWNERSHIP_INVALID", "schema", "", "stop and restore the Catalog schema under its dedicated migration owner"))
	}
	schemaInventoryValid := true
	if err := requireCatalogTables(ctx, tx, schema, migrationRole); err != nil {
		usable = false
		schemaInventoryValid = false
		appendRestoreFinding(report, *integrityFinding("RESTORE_SCHEMA_INVENTORY_INVALID", "schema", "", "stop and investigate missing, additional, or incorrectly owned Catalog objects"))
	} else if err := verifyRestoreTableInventory(ctx, tx, schema); err != nil {
		usable = false
		schemaInventoryValid = false
		appendRestoreFinding(report, *integrityFinding("RESTORE_SCHEMA_INVENTORY_INVALID", "schema", "", "stop and investigate missing, additional, or incorrectly owned Catalog objects"))
	}
	metadataBounded := schemaInventoryValid
	if metadataBounded {
		if err := verifyRestoreMetadataBounds(ctx, tx, schema); err != nil {
			usable = false
			metadataBounded = false
			appendRestoreFinding(report, *integrityFinding("RESTORE_SCHEMA_INVENTORY_INVALID", "schema", "", "stop and investigate the unexpected Catalog schema metadata volume"))
		}
	}
	if err := validateRuntimeRoleBindings(ctx, tx, options.APIRole, options.WorkerRole); err != nil {
		appendRestoreFinding(report, *integrityFinding("RESTORE_RUNTIME_ROLE_ATTRIBUTES_INVALID", "database_role", "", "keep runtimes stopped and correct the named role attributes or memberships"))
	} else if metadataBounded {
		for _, binding := range []struct {
			runtimeRole  string
			databaseRole string
		}{{RuntimeRoleAPI, options.APIRole}, {RuntimeRoleWorker, options.WorkerRole}} {
			privileges, inspectErr := inspectRuntimePrivileges(ctx, tx, schema, binding.runtimeRole, binding.databaseRole)
			if inspectErr != nil {
				return false, inspectErr
			}
			if !privileges.Compliant {
				appendRestoreFinding(report, *integrityFinding("RESTORE_RUNTIME_PRIVILEGES_INVALID", "database_role", binding.runtimeRole, "keep runtimes stopped and reconcile the exact reviewed privilege manifest"))
			}
		}
	}
	if metadataBounded {
		if err := verifyRestoreRLS(ctx, tx, schema); err != nil {
			usable = false
			appendRestoreFinding(report, *integrityFinding("RESTORE_RLS_POLICY_INVALID", "schema", "", "keep traffic disabled and restore the exact forced-RLS policy inventory"))
		}
		if err := verifyRestoreConstraints(ctx, tx, schema); err != nil {
			appendRestoreFinding(report, *integrityFinding("RESTORE_CONSTRAINT_INVENTORY_INVALID", "schema", "", "stop and investigate constraint identity or validation drift before qualification"))
		}
		if err := verifySearchProjectionRepairIndex(ctx, tx, schema); err != nil {
			appendRestoreFinding(report, *integrityFinding("RESTORE_SEARCH_REPAIR_INDEX_INVALID", "schema", "", "restore the exact sparse repair index before running bounded projection repair"))
		}
		if err := verifyRestoreTriggers(ctx, tx, schema); err != nil {
			appendRestoreFinding(report, *integrityFinding("RESTORE_GUARD_INVENTORY_INVALID", "schema", "", "stop and restore the exact enabled lifecycle guard definitions before qualification"))
		}
	}
	var tenantExists bool
	if usable {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1)`, options.TenantID).Scan(&tenantExists); err != nil {
			return false, fmt.Errorf("inspect selected restore tenant: %w", err)
		}
		if !tenantExists {
			appendRestoreFinding(report, *integrityFinding("RESTORE_TENANT_MISSING", "tenant", options.TenantID, "confirm the external tenant inventory and selected recovery point"))
			usable = false
		}
	}
	if usable {
		var present bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_authorization_state WHERE tenant_id=$1 AND revision>0)`, options.TenantID).Scan(&present); err != nil {
			return false, fmt.Errorf("inspect restored authorization state: %w", err)
		}
		if !present {
			appendRestoreFinding(report, *integrityFinding("RESTORE_AUTHORIZATION_STATE_MISSING", "tenant", options.TenantID, "keep traffic disabled; investigate missing retained authorization state without reinitializing it"))
			usable = false
		}
	}
	return usable, nil
}

// verifyRestoreMetadataBounds rejects an oversized Catalog function inventory
// before broader privilege and guard inspection.
func verifyRestoreMetadataBounds(ctx context.Context, tx pgx.Tx, schema string) error {
	var functions int
	if err := tx.QueryRow(ctx, `
SELECT count(*)
  FROM (
    SELECT 1
      FROM pg_catalog.pg_proc procedure
      JOIN pg_catalog.pg_namespace namespace ON namespace.oid=procedure.pronamespace
     WHERE namespace.nspname=$1
     LIMIT $2
  ) bounded`, schema, maxRestoreSchemaFunctions+1).Scan(&functions); err != nil {
		return err
	}
	if functions > maxRestoreSchemaFunctions {
		return errors.New("catalog schema function inventory exceeds its verification bound")
	}
	return nil
}

// verifySearchProjectionRepairIndex requires the live, ready nonunique
// tenant/ID index and exact sparse predicate used to bound search-projection
// repair.
func verifySearchProjectionRepairIndex(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, schema string) error {
	const expectedPredicate = `((normalized_entry_digest IS NOT NULL) AND ((search_display_name IS NULL) OR (search_description IS NULL) OR (search_tags IS NULL) OR (search_representative_queries IS NULL) OR (search_publisher IS NULL) OR (search_vector IS NULL) OR (search_vector_version <> 2) OR (search_filter_version <> 1)))`
	var valid, ready, live, unique bool
	var keyCount, attributeCount int
	var table, method, firstKey, secondKey, predicate string
	err := query.QueryRow(ctx, `
SELECT index_record.indisvalid,index_record.indisready,index_record.indislive,index_record.indisunique,
       index_record.indnkeyatts,index_record.indnatts,table_class.relname,access_method.amname,
       pg_catalog.pg_get_indexdef(index_record.indexrelid,1,true),
       pg_catalog.pg_get_indexdef(index_record.indexrelid,2,true),
       pg_catalog.pg_get_expr(index_record.indpred,index_record.indrelid)
  FROM pg_catalog.pg_index index_record
  JOIN pg_catalog.pg_class index_class ON index_class.oid=index_record.indexrelid
  JOIN pg_catalog.pg_class table_class ON table_class.oid=index_record.indrelid
  JOIN pg_catalog.pg_am access_method ON access_method.oid=index_class.relam
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=index_class.relnamespace
 WHERE namespace.nspname=$1 AND index_class.relname='observations_catalog_search_projection_missing'`, schema).Scan(&valid, &ready, &live, &unique, &keyCount, &attributeCount, &table, &method, &firstKey, &secondKey, &predicate)
	if err != nil || !valid || !ready || !live || unique || keyCount != 2 || attributeCount != 2 || table != "observations" || method != "btree" || firstKey != "tenant_id" || secondKey != "id" || strings.Join(strings.Fields(predicate), " ") != expectedPredicate {
		return errors.New("bounded search repair index drifted")
	}
	return nil
}

// strconvVersion formats a migration version for the restore finding's object
// identifier.
func strconvVersion(version int64) string {
	return fmt.Sprintf("%d", version)
}

// verifyRestoreRLS requires the exact forced tenant-isolation and
// verified-realm policy inventory, with no tenant RLS on the migration ledger.
func verifyRestoreRLS(ctx context.Context, tx pgx.Tx, schema string) error {
	expectedTables := make(map[string]bool, len(catalogTableNames)-1)
	for _, table := range catalogTableNames {
		if table != "catalog_schema_migrations" {
			expectedTables[table] = false
		}
	}
	rows, err := tx.Query(ctx, `
SELECT class.relname,class.relrowsecurity,class.relforcerowsecurity,
       coalesce(policy.polname,''),coalesce(policy.polcmd,''),coalesce(policy.polpermissive,false),
       coalesce(pg_catalog.pg_get_expr(policy.polqual,policy.polrelid),''),
       coalesce(pg_catalog.pg_get_expr(policy.polwithcheck,policy.polrelid),'')
  FROM pg_catalog.pg_class class
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=class.relnamespace
  LEFT JOIN pg_catalog.pg_policy policy ON policy.polrelid=class.oid
 WHERE namespace.nspname=$1 AND class.relname=ANY($2::text[])
	ORDER BY class.relname,policy.polname
	LIMIT $3`, schema, catalogTableNames, len(catalogTableNames)+3)
	if err != nil {
		return err
	}
	defer rows.Close()
	policyCounts := make(map[string]int)
	verifiedRealm := false
	for rows.Next() {
		var table, name, command, qualifier, check string
		var enabled, forced, permissive bool
		if err := rows.Scan(&table, &enabled, &forced, &name, &command, &permissive, &qualifier, &check); err != nil {
			return err
		}
		if table == "catalog_schema_migrations" {
			if enabled || forced || name != "" {
				return errors.New("migration table unexpectedly uses tenant RLS")
			}
			continue
		}
		if !enabled || !forced || name == "" || !permissive && !(table == "personal_items" && name == "personal_owner") {
			return errors.New("catalog table is missing forced RLS")
		}
		expectedTables[table] = true
		policyCounts[table]++
		normalizedQualifier := strings.Join(strings.Fields(qualifier), " ")
		normalizedCheck := strings.Join(strings.Fields(check), " ")
		switch name {
		case "personal_owner":
			const expected = `(membership_id = (NULLIF(current_setting('app.membership_id'::text, true), ''::text))::uuid)`
			if table != "personal_items" || command != "*" || permissive || normalizedQualifier != expected || normalizedCheck != expected {
				return errors.New("personal owner isolation policy definition drifted")
			}
		case "tenant_isolation":
			expected := `(tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)`
			if table == "tenants" {
				expected = `(id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)`
			}
			if command != "*" || normalizedQualifier != expected || normalizedCheck != expected {
				return errors.New("tenant isolation policy definition drifted")
			}
		case "verified_identity_realm_resolution":
			const expected = `((state = 'active'::text) AND (issuer = NULLIF(current_setting('app.identity_issuer'::text, true), ''::text)) AND (((NULLIF(current_setting('app.identity_tenant_id'::text, true), ''::text) IS NOT NULL) AND (tenant_id = (NULLIF(current_setting('app.identity_tenant_id'::text, true), ''::text))::uuid)) OR ((external_organization_key <> ''::text) AND (external_organization_key = NULLIF(current_setting('app.identity_organization'::text, true), ''::text)))))`
			if table != "tenant_identity_realms" || command != "r" || normalizedCheck != "" || normalizedQualifier != expected {
				return errors.New("identity-realm resolution policy definition drifted")
			}
			verifiedRealm = true
		default:
			return errors.New("unexpected Catalog RLS policy")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for table, seen := range expectedTables {
		expectedCount := 1
		if table == "tenant_identity_realms" || table == "personal_items" {
			expectedCount = 2
		}
		if !seen || policyCounts[table] != expectedCount {
			return errors.New("catalog RLS policy inventory is incomplete")
		}
	}
	if !verifiedRealm {
		return errors.New("identity-realm resolution policy is missing")
	}
	return nil
}

// verifyRestoreTableInventory compares a bounded table-name inventory with the
// exact Catalog schema, rejecting missing or additional tables.
func verifyRestoreTableInventory(ctx context.Context, tx pgx.Tx, schema string) error {
	rows, err := tx.Query(ctx, `
SELECT class.relname
  FROM pg_catalog.pg_class class
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=class.relnamespace
 WHERE namespace.nspname=$1 AND class.relkind IN ('r','p')
	ORDER BY class.relname LIMIT $2`, schema, len(catalogTableNames)+len(semanticTableColumns)+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	actual := make([]string, 0, len(catalogTableNames))
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return err
		}
		actual = append(actual, table)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	expected := append([]string(nil), catalogTableNames...)
	optional := false
	for _, table := range actual {
		if _, ok := semanticTableColumns[table]; ok {
			optional = true
		}
	}
	if optional {
		for table := range semanticTableColumns {
			expected = append(expected, table)
		}
	}
	sort.Strings(expected)
	if strings.Join(actual, "\x00") != strings.Join(expected, "\x00") {
		return errors.New("catalog table inventory drifted")
	}
	if optional {
		return verifyRestoredSemanticSchema(ctx, tx, schema)
	}
	return nil
}

// verifyRestoreConstraints compares bounded, normalized constraint definitions
// with the PostgreSQL 18 reference digest and rejects unvalidated constraints.
func verifyRestoreConstraints(ctx context.Context, tx pgx.Tx, schema string) error {
	rows, err := tx.Query(ctx, `
SELECT table_record.relname,constraint_record.conname,constraint_record.contype,constraint_record.convalidated,
       pg_catalog.pg_get_constraintdef(constraint_record.oid,true)
  FROM pg_catalog.pg_constraint constraint_record
	  JOIN pg_catalog.pg_class table_record ON table_record.oid=constraint_record.conrelid
	  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=table_record.relnamespace
 WHERE namespace.nspname=$1
	ORDER BY table_record.relname,constraint_record.conname LIMIT $2`, schema, maxRestoreSchemaConstraints+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	var inventory strings.Builder
	constraintCount := 0
	for rows.Next() {
		constraintCount++
		if constraintCount > maxRestoreSchemaConstraints {
			return errors.New("catalog constraint inventory exceeds its verification bound")
		}
		var table, name, kind, definition string
		var validated bool
		if err := rows.Scan(&table, &name, &kind, &validated, &definition); err != nil {
			return err
		}
		// Optional constraints are checked against their own exact inventory.
		if _, optional := semanticTableColumns[table]; optional {
			continue
		}
		if inventory.Len() > 0 {
			inventory.WriteByte('\x1e')
		}
		inventory.WriteString(table)
		inventory.WriteByte('\x1f')
		inventory.WriteString(name)
		inventory.WriteByte('\x1f')
		inventory.WriteString(kind)
		inventory.WriteByte('\x1f')
		inventory.WriteString(fmt.Sprintf("%t", validated))
		inventory.WriteByte('\x1f')
		inventory.WriteString(strings.Join(strings.Fields(definition), " "))
		if !validated {
			return errors.New("unexpected unvalidated Catalog constraint")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	digest := sha256.Sum256([]byte(inventory.String()))
	// Qualified on PostgreSQL 18 through migration 000047, including exact
	// Publisher verification binding and coherent challenge/proof timestamps.
	if hex.EncodeToString(digest[:]) != "1de47391d7f806e49996de2d00db5ba090f870b94fb0e92f43fcaf8e31cfc646" {
		return fmt.Errorf("catalog constraint inventory or definition drifted (observed digest %x)", digest)
	}
	return nil
}

// verifyRestoreTriggers checks the bounded trigger inventory and critical guard
// execution properties and bodies against embedded migrations, without altering
// guards.
func verifyRestoreTriggers(ctx context.Context, tx pgx.Tx, schema string) error {
	expectedFunctions, err := embeddedTriggerFunctionSources()
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
SELECT trigger_record.tgname,table_record.relname,function_record.proname,
       trigger_record.tgenabled,pg_catalog.pg_get_triggerdef(trigger_record.oid,true),
       function_record.prosrc,language_record.lanname,function_record.prosecdef,
       function_record.proowner=table_record.relowner,
       coalesce(function_record.proconfig,ARRAY[]::text[])
  FROM pg_catalog.pg_trigger trigger_record
  JOIN pg_catalog.pg_class table_record ON table_record.oid=trigger_record.tgrelid
  JOIN pg_catalog.pg_namespace namespace ON namespace.oid=table_record.relnamespace
  JOIN pg_catalog.pg_proc function_record ON function_record.oid=trigger_record.tgfoid
  JOIN pg_catalog.pg_language language_record ON language_record.oid=function_record.prolang
 WHERE namespace.nspname=$1 AND NOT trigger_record.tgisinternal
	ORDER BY trigger_record.tgname LIMIT $2`, schema, maxRestoreSchemaTriggers+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := make(map[string]bool, len(expectedRestoreTriggers))
	var inventory strings.Builder
	triggerCount := 0
	for rows.Next() {
		triggerCount++
		if triggerCount > maxRestoreSchemaTriggers {
			return errors.New("catalog trigger inventory exceeds its verification bound")
		}
		var name, table, function, enabled, definition, source, language string
		var securityDefiner, tableOwnerFunction bool
		var settings []string
		if err := rows.Scan(&name, &table, &function, &enabled, &definition, &source, &language, &securityDefiner, &tableOwnerFunction, &settings); err != nil {
			return err
		}
		// The optional guard is checked with its schema before these core guards.
		if _, optional := semanticTableColumns[table]; optional {
			continue
		}
		if inventory.Len() > 0 {
			inventory.WriteByte('\x1e')
		}
		for index, value := range []string{name, table, function, enabled, strings.Join(strings.Fields(definition), " ")} {
			if index > 0 {
				inventory.WriteByte('\x1f')
			}
			inventory.WriteString(value)
		}
		expected, critical := expectedRestoreTriggers[name]
		if !critical {
			continue
		}
		requiresPinnedSearchPath := function != "reject_federation_source_history_mutation" && function != "reject_version_validation_run_mutation"
		// Migration 000025 confines notification-state DML to two fixed
		// owner-authorized triggers. Every other trigger remains invoker-only.
		requiresDefiner := function == "admit_webhook_notification" || function == "record_webhook_delivery_loss" || function == "record_item_rating"
		if enabled != "O" || table != expected.table || function != expected.function || language != "plpgsql" || securityDefiner != requiresDefiner || requiresDefiner && !tableOwnerFunction || containsString(settings, "search_path=pg_catalog") != requiresPinnedSearchPath {
			return errors.New("critical trigger identity or execution properties drifted")
		}
		normalizedDefinition := strings.ToLower(strings.Join(strings.Fields(definition), " "))
		for _, phrase := range expected.definition {
			for _, word := range strings.Fields(phrase) {
				if !strings.Contains(normalizedDefinition, word) {
					return errors.New("critical trigger definition drifted")
				}
			}
		}
		expectedSource, ok := expectedFunctions[function]
		if function == "project_observation_catalog_search" {
			// Migration 27 binds its one normalization call to the owning schema.
			// Expand only that exact template; foreign-schema calls still fail.
			expectedSource = strings.ReplaceAll(expectedSource, "%s.catalog_core_lexical_vector_v1", pgx.Identifier{schema}.Sanitize()+".catalog_core_lexical_vector_v1")
		}
		if function == "project_observation_control_filters" {
			for _, helper := range []string{"catalog_observation_license_v1", "catalog_observation_categories_v1"} {
				expectedSource = strings.ReplaceAll(expectedSource, "%s."+helper, pgx.Identifier{schema}.Sanitize()+"."+helper)
			}
		}

		if !ok || strings.TrimSpace(source) != strings.TrimSpace(expectedSource) {
			return errors.New("critical trigger function definition drifted")
		}
		seen[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for name := range expectedRestoreTriggers {
		if !seen[name] {
			return errors.New("critical trigger is missing")
		}
	}
	digest := sha256.Sum256([]byte(inventory.String()))
	// PostgreSQL 18 through migration 000047, including Publisher domain verification. Guard bodies are checked separately against their latest embedded definitions.
	if hex.EncodeToString(digest[:]) != "240b8f4025779509852576425dde45056d9ce397be5cfb4dc9e12391a0ec2b46" {
		return fmt.Errorf("catalog trigger inventory or definition drifted (observed digest %x)", digest)
	}
	return nil
}

// embeddedTriggerFunctionSources extracts the latest embedded body for each
// critical trigger function in migration order and fails if any expected
// function is missing. It recognizes the maintained migration declaration
// forms, not arbitrary SQL.
func embeddedTriggerFunctionSources() (map[string]string, error) {
	wanted := make(map[string]bool)
	for _, expected := range expectedRestoreTriggers {
		wanted[expected.function] = true
	}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make(map[string]string, len(wanted))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		raw, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			return nil, err
		}
		text := string(raw)
		for function := range wanted {
			for _, prefix := range []string{"CREATE FUNCTION " + function + "()", "CREATE OR REPLACE FUNCTION " + function + "()"} {
				start := strings.Index(text, prefix)
				if start < 0 {
					continue
				}
				as := strings.Index(text[start:], "AS $")
				if as < 0 {
					continue
				}
				as += start + len("AS ")
				tagEnd := strings.IndexByte(text[as+1:], '$')
				if tagEnd < 0 {
					continue
				}
				tagEnd += as + 1
				tag := text[as : tagEnd+1]
				bodyStart := tagEnd + 1
				bodyEnd := strings.Index(text[bodyStart:], tag)
				if bodyEnd < 0 {
					continue
				}
				result[function] = text[bodyStart : bodyStart+bodyEnd]
			}
		}
	}
	for function := range wanted {
		if _, ok := result[function]; !ok {
			return nil, fmt.Errorf("embedded migration does not define critical trigger function %s", function)
		}
	}
	return result, nil
}
