package database

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestLocalSearchProjectionsAreAPIReadOnly(t *testing.T) {
	api, worker := apiPrivilegeManifest(), workerPrivilegeManifest()
	for table, columns := range map[string][]string{
		"resources":         {"search_identifier_vector"},
		"observations":      {"search_identifier_vector", "search_display_vector", "search_description_vector", "search_tags_vector", "search_queries_vector"},
		"listing_revisions": {"search_display_vector", "search_description_vector", "search_tags_vector", "search_queries_vector", "search_sort_name"},
	} {
		for _, column := range columns {
			if !slices.Contains(api.tables[table].selectColumns, column) || slices.Contains(worker.tables[table].selectColumns, column) {
				t.Fatalf("unexpected reader authority for %s.%s", table, column)
			}
			for _, manifest := range []runtimePrivilegeManifest{api, worker} {
				if slices.Contains(manifest.tables[table].insertColumns, column) || slices.Contains(manifest.tables[table].updateColumns, column) {
					t.Fatalf("runtime can write generated %s.%s", table, column)
				}
			}
		}
	}
	const function = "catalog_core_lexical_array_vector_v1(text[])"
	if !slices.Contains(api.functions, function) || !slices.Contains(worker.functions, function) {
		t.Fatal("generated text-array helper is required for API authoring and worker ingestion")
	}
}

func TestRuntimePrivilegeManifestsStayNarrowAndInternallyConsistent(t *testing.T) {
	knownTables := make(map[string]bool, len(catalogTableNames))
	for _, table := range catalogTableNames {
		if table == "" || knownTables[table] {
			t.Fatalf("invalid or duplicate Catalog table %q", table)
		}
		knownTables[table] = true
	}
	if !slices.IsSorted(catalogTableNames) {
		t.Fatal("Catalog table inventory must stay sorted")
	}
	if !slices.IsSorted(catalogFunctionNames) {
		t.Fatal("Catalog function inventory must stay sorted")
	}
	knownFunctions := make(map[string]bool, len(catalogFunctionNames))
	for _, function := range catalogFunctionNames {
		if function == "" || knownFunctions[function] {
			t.Fatalf("invalid or duplicate Catalog function %q", function)
		}
		knownFunctions[function] = true
	}
	if len(catalogTableColumns) != len(catalogTableNames) {
		t.Fatalf("Catalog column inventory covers %d tables, want %d", len(catalogTableColumns), len(catalogTableNames))
	}
	for table, columns := range catalogTableColumns {
		if !knownTables[table] {
			t.Fatalf("column inventory refers to unknown table %q", table)
		}
		assertUniquePrivilegeColumns(t, "schema", table, "column", columns)
		if len(columns) == 0 {
			t.Fatalf("column inventory for %s is empty", table)
		}
	}

	for runtimeRole, manifest := range map[string]runtimePrivilegeManifest{
		RuntimeRoleAPI:    apiPrivilegeManifest(),
		RuntimeRoleWorker: workerPrivilegeManifest(),
	} {
		for table, privilege := range manifest.tables {
			columns, optional := semanticTableColumns[table]
			if !knownTables[table] && !optional {
				t.Fatalf("%s manifest refers to unknown table %q", runtimeRole, table)
			}
			if !optional {
				columns = catalogTableColumns[table]
			}
			assertUniquePrivilegeColumns(t, runtimeRole, table, "SELECT", privilege.selectColumns)
			assertUniquePrivilegeColumns(t, runtimeRole, table, "INSERT", privilege.insertColumns)
			assertUniquePrivilegeColumns(t, runtimeRole, table, "UPDATE", privilege.updateColumns)
			for _, column := range append(append(append([]string(nil), privilege.selectColumns...), privilege.insertColumns...), privilege.updateColumns...) {
				if !slices.Contains(columns, column) {
					t.Fatalf("%s manifest refers to unknown column %s.%s", runtimeRole, table, column)
				}
			}
		}
		for _, function := range manifest.functions {
			if !knownFunctions[function] {
				t.Fatalf("%s manifest refers to unknown function %q", runtimeRole, function)
			}
		}
	}

	api := apiPrivilegeManifest()
	worker := workerPrivilegeManifest()
	for _, manifest := range []runtimePrivilegeManifest{api, worker} {
		if privilege := manifest.tables["catalog_schema_migrations"]; len(privilege.selectColumns) != 0 || len(privilege.insertColumns) != 0 || len(privilege.updateColumns) != 0 || privilege.deleteRows {
			t.Fatalf("migration ledger privilege = %#v", privilege)
		}
		if privilege := manifest.tables["tenants"]; len(privilege.insertColumns) != 0 || len(privilege.updateColumns) != 0 || privilege.deleteRows {
			t.Fatalf("tenant bootstrap privilege = %#v", privilege)
		}
	}
	if len(api.tables["tenants"].selectColumns) == 0 || len(worker.tables["tenants"].selectColumns) != 0 {
		t.Fatal("tenant read authority does not match API and worker needs")
	}
	if !slices.Equal(api.tables["outbox_events"].selectColumns, []string{"tenant_id", "is_internal", "published_at"}) || len(api.tables["source_retrieval_documents"].selectColumns) != 0 || len(api.tables["source_maintenance_state"].selectColumns) != 0 {
		t.Fatal("API role can read worker-private state")
	}
	if len(worker.tables["audit_events"].selectColumns) != 0 || len(worker.tables["tenant_memberships"].selectColumns) != 0 {
		t.Fatal("worker role can read API or identity state")
	}
	if got := worker.tables["idempotency_keys"].selectColumns; !slices.Equal(got, []string{"tenant_id", "reservation_id", "protocol_version", "expires_at", "atomic_response_status", "released_at"}) {
		t.Fatalf("worker idempotency cleanup columns = %#v", got)
	}
	if !api.tables["idempotency_keys"].deleteRows {
		t.Fatal("API role lacks bounded idempotency cleanup authority")
	}
	if len(api.tables["webhook_deliveries"].insertColumns) == 0 {
		t.Fatal("API role lacks enabled-webhook test-delivery authority")
	}
	if !worker.tables["source_retrieval_states"].deleteRows {
		t.Fatal("worker role lacks conditional-retrieval invalidation authority")
	}
	if _, ok := worker.tables["personal_items"]; ok {
		t.Fatal("worker has private library authority")
	}
	for table, privilege := range api.tables {
		if table != "idempotency_keys" && table != "role_permissions" && table != "personal_items" && table != "publisher_domain_verifications" && privilege.deleteRows {
			t.Fatalf("API role has unexpected DELETE on %s", table)
		}
	}
	workerDeleteTables := []string{"idempotency_keys", "jobs", "outbox_events", "source_change_previews", "source_inspections", "source_maintenance_state", "source_retrieval_states", "webhook_deliveries", "semantic_generations", "semantic_vectors", "semantic_members"}
	for table, privilege := range worker.tables {
		if !slices.Contains(workerDeleteTables, table) && privilege.deleteRows {
			t.Fatalf("worker role has unexpected DELETE on %s", table)
		}
	}
	for _, table := range workerDeleteTables {
		if !worker.tables[table].deleteRows {
			t.Fatalf("worker role lacks bounded housekeeping DELETE on %s", table)
		}
	}
	if got := worker.tables["federation_sources"].updateColumns; !slices.Equal(got, []string{"status", "last_complete_snapshot_id", "last_sync_at", "last_error", "updated_at"}) {
		t.Fatalf("worker source update columns = %#v", got)
	}
	if got := worker.tables["publication_snapshots"].updateColumns; len(got) != 0 {
		t.Fatalf("worker publication update columns = %#v", got)
	}
	if !slices.Equal(api.functions, []string{"catalog_control_term_v1(text)", "catalog_observation_license_v1(jsonb)", "catalog_observation_categories_v1(jsonb)", "catalog_core_lexical_array_vector_v1(text[])", "catalog_core_lexical_query_v1(text)", "catalog_core_lexical_terms_v1(text)", "catalog_core_lexical_vector_v1(text)", "catalog_core_sort_name_v1(text)"}) || !slices.Equal(worker.functions, []string{"catalog_control_term_v1(text)", "catalog_observation_license_v1(jsonb)", "catalog_observation_categories_v1(jsonb)", "catalog_core_lexical_array_vector_v1(text[])", "catalog_core_lexical_terms_v1(text)", "catalog_core_lexical_vector_v1(text)"}) {
		t.Fatalf("runtime lexical function grants = API %#v, worker %#v", api.functions, worker.functions)
	}
	for _, manifest := range []runtimePrivilegeManifest{api, worker} {
		state := manifest.tables["webhook_notification_state"]
		if len(state.selectColumns) == 0 || len(state.insertColumns) != 0 || len(state.updateColumns) != 0 || state.deleteRows {
			t.Fatal("runtime notification state authority must remain read-only")
		}
	}
}

func TestRuntimeRoleAttributeValidation(t *testing.T) {
	validAPI := RoleAttributes{Name: "api", Login: true}
	validWorker := RoleAttributes{Name: "worker", Login: true, BypassRLS: true}
	if err := validateRuntimeRoleAttributes(validAPI, RuntimeRoleAPI); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeRoleAttributes(validWorker, RuntimeRoleWorker); err != nil {
		t.Fatal(err)
	}
	for name, attributes := range map[string]RoleAttributes{
		"no login":    {Name: "api"},
		"superuser":   {Name: "api", Login: true, Superuser: true},
		"inherit":     {Name: "api", Login: true, Inherit: true},
		"create role": {Name: "api", Login: true, CreateRole: true},
		"create db":   {Name: "api", Login: true, CreateDB: true},
		"replication": {Name: "api", Login: true, Replication: true},
		"bypass RLS":  {Name: "api", Login: true, BypassRLS: true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateRuntimeRoleAttributes(attributes, RuntimeRoleAPI); err == nil {
				t.Fatal("unsafe API role attributes were accepted")
			}
		})
	}
	if err := validateRuntimeRoleAttributes(RoleAttributes{Name: "worker", Login: true}, RuntimeRoleWorker); err == nil {
		t.Fatal("worker role without BYPASSRLS was accepted")
	}
	if err := validateRuntimeRoleAttributes(validAPI, "combined"); err == nil {
		t.Fatal("unknown runtime role was accepted")
	}
}

func TestRuntimeDatabaseRoleNameValidation(t *testing.T) {
	if err := ValidateRuntimeDatabaseRoles("Catalog-API", "catalog_worker"); err != nil {
		t.Fatal(err)
	}
	for name, roles := range map[string][2]string{
		"empty API":       {"", "worker"},
		"empty worker":    {"api", ""},
		"same role":       {"catalog", "catalog"},
		"whitespace":      {"catalog api", "worker"},
		"NUL":             {"api\x00role", "worker"},
		"over byte limit": {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "worker"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRuntimeDatabaseRoles(roles[0], roles[1]); err == nil {
				t.Fatal("invalid PostgreSQL role names were accepted")
			}
		})
	}
}

func TestCatalogStateLockKeysSeparateScopesAndIdentifiers(t *testing.T) {
	key := catalogStateLockKey("tenant", "one")
	if key != catalogStateLockKey("tenant", "one") || key == catalogStateLockKey("version-validation", "one") || key == catalogStateLockKey("tenant", "two") {
		t.Fatal("state-lock keys are not deterministic and scope-separated")
	}
}

func TestRuntimePrivilegeManifestSelectionAndSQLConstruction(t *testing.T) {
	t.Parallel()

	api, err := manifestForRuntimeRole(RuntimeRoleAPI)
	if err != nil || !slices.Equal(api.functions, []string{"catalog_control_term_v1(text)", "catalog_observation_license_v1(jsonb)", "catalog_observation_categories_v1(jsonb)", "catalog_core_lexical_array_vector_v1(text[])", "catalog_core_lexical_query_v1(text)", "catalog_core_lexical_terms_v1(text)", "catalog_core_lexical_vector_v1(text)", "catalog_core_sort_name_v1(text)"}) {
		t.Fatalf("API manifest = %#v, %v", api, err)
	}
	worker, err := manifestForRuntimeRole(RuntimeRoleWorker)
	if err != nil || !slices.Equal(worker.functions, []string{"catalog_control_term_v1(text)", "catalog_observation_license_v1(jsonb)", "catalog_observation_categories_v1(jsonb)", "catalog_core_lexical_array_vector_v1(text[])", "catalog_core_lexical_terms_v1(text)", "catalog_core_lexical_vector_v1(text)"}) {
		t.Fatalf("worker manifest = %#v, %v", worker, err)
	}
	if _, err := manifestForRuntimeRole("combined"); err == nil {
		t.Fatal("unsupported runtime role returned a manifest")
	}
	if got := quotedColumns([]string{"ordinary", "select", "contains space"}); got != `"ordinary","select","contains space"` {
		t.Fatalf("quoted columns = %q", got)
	}
	if !containsString([]string{"one", "two"}, "two") || containsString([]string{"one", "two"}, "three") {
		t.Fatal("string membership helper returned an incorrect result")
	}

	transaction := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	manifest := runtimePrivilegeManifest{
		tables: map[string]tablePrivilege{
			"example table": {
				selectColumns: []string{"read column"},
				insertColumns: []string{"insert column"},
				updateColumns: []string{"update column"},
				deleteRows:    true,
			},
		},
		functions: []string{"example_function(text)"},
	}
	if err := grantRuntimeManifest(context.Background(), transaction, "catalog schema", "runtime role", manifest); err != nil {
		t.Fatal(err)
	}
	statements := strings.Join(transaction.statements, "\n")
	for _, fragment := range []string{
		`GRANT SELECT ("read column") ON TABLE "catalog schema"."example table" TO "runtime role"`,
		`GRANT INSERT ("insert column")`,
		`GRANT UPDATE ("update column")`,
		`GRANT DELETE ON TABLE`,
		`GRANT EXECUTE ON FUNCTION "catalog schema".example_function(text)`,
	} {
		if !strings.Contains(statements, fragment) {
			t.Fatalf("grant statements omitted %q: %s", fragment, statements)
		}
	}
	failing := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{execErrors: []error{errors.New("grant failed")}}}
	if err := grantRuntimeManifest(context.Background(), failing, "catalog", "runtime", manifest); err == nil || !strings.Contains(err.Error(), "grant failed") {
		t.Fatalf("failed grant returned %v", err)
	}
}

func TestRuntimePrivilegeRoleBindingAndCatalogInventoryInspection(t *testing.T) {
	t.Parallel()

	roleRow := func(name string, bypassRLS bool) scannerFunc {
		return func(destinations ...any) error {
			setScanDestination(t, destinations, 0, name)
			setScanDestination(t, destinations, 1, true)
			setScanDestination(t, destinations, 2, false)
			setScanDestination(t, destinations, 3, false)
			setScanDestination(t, destinations, 4, false)
			setScanDestination(t, destinations, 5, false)
			setScanDestination(t, destinations, 6, false)
			setScanDestination(t, destinations, 7, bypassRLS)
			return nil
		}
	}
	textRow := func(value string) scannerFunc {
		return func(destinations ...any) error {
			setScanDestination(t, destinations, 0, value)
			return nil
		}
	}
	boolRow := func(value bool) scannerFunc {
		return func(destinations ...any) error {
			setScanDestination(t, destinations, 0, value)
			return nil
		}
	}
	transaction := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	transaction.queryRows = append(transaction.queryRows,
		textRow("migration"),
		roleRow("api", false), boolRow(false),
		roleRow("worker", true), boolRow(false),
	)
	if err := validateRuntimeRoleBindings(context.Background(), transaction, "api", "worker"); err != nil {
		t.Fatal(err)
	}
	if len(transaction.queryRows) != 0 {
		t.Fatalf("role validation left %d scripted rows unused", len(transaction.queryRows))
	}
	membershipTransaction := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{boolRow(true)}}
	if err := validateRuntimeRoleMembership(context.Background(), membershipTransaction, "api", RuntimeRoleAPI); err == nil || !strings.Contains(err.Error(), "must not participate") {
		t.Fatalf("runtime role membership returned %v", err)
	}
	if len(membershipTransaction.queryRowStatements) != 1 || !strings.Contains(membershipTransaction.queryRowStatements[0], "granted_role.rolname=$1") {
		t.Fatalf("runtime role membership inspection did not cover incoming grants: %#v", membershipTransaction.queryRowStatements)
	}

	schemaTransaction := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	schemaTransaction.queryRows = append(schemaTransaction.queryRows, scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, "catalog")
		setScanDestination(t, destinations, 1, "migration")
		return nil
	}))
	schemaTransaction.queryRows = append(schemaTransaction.queryRows, scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, "migration")
		setScanDestination(t, destinations, 1, "migration")
		return nil
	}))
	for range catalogTableNames {
		schemaTransaction.queryRows = append(schemaTransaction.queryRows, textRow("migration"))
	}
	schema, owner, err := currentCatalogSchema(context.Background(), schemaTransaction)
	if err != nil || schema != "catalog" || owner != "migration" {
		t.Fatalf("Catalog schema = %q / %q, %v", schema, owner, err)
	}
	if err := requireCatalogSchemaOwnership(context.Background(), schemaTransaction, schema, owner); err != nil {
		t.Fatal(err)
	}
	if err := requireCatalogTables(context.Background(), schemaTransaction, schema, owner); err != nil {
		t.Fatal(err)
	}
	wrongSchemaOwner := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, "different-owner")
		setScanDestination(t, destinations, 1, "migration")
		return nil
	})}}
	if err := requireCatalogSchemaOwnership(context.Background(), wrongSchemaOwner, "catalog", "migration"); err == nil || !strings.Contains(err.Error(), "must be owned") {
		t.Fatalf("wrong Catalog schema owner returned %v", err)
	}
	databaseOwnedSchema := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, "pg_database_owner")
		setScanDestination(t, destinations, 1, "migration")
		return nil
	})}}
	if err := requireCatalogSchemaOwnership(context.Background(), databaseOwnedSchema, "public", "migration"); err != nil {
		t.Fatalf("database-owner schema binding was rejected: %v", err)
	}

	wrongOwner := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	wrongOwner.queryRows = append(wrongOwner.queryRows, textRow("another-owner"))
	if err := requireCatalogTables(context.Background(), wrongOwner, "catalog", "migration"); err == nil || !strings.Contains(err.Error(), "must be owned") {
		t.Fatalf("wrong Catalog table owner returned %v", err)
	}
}

func TestRuntimePrivilegeInspectionProducesBoundedSafeDifferences(t *testing.T) {
	t.Parallel()

	transaction := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	transaction.queryRows = append(transaction.queryRows,
		scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, true)
			setScanDestination(t, destinations, 1, true)
			setScanDestination(t, destinations, 2, true)
			setScanDestination(t, destinations, 3, true)
			return nil
		}),
		scannerFunc(func(destinations ...any) error {
			for index := range 7 {
				setScanDestination(t, destinations, index, false)
			}
			return nil
		}),
		scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, 1)
			return nil
		}),
	)
	columnRows := &identityScriptedRows{values: [][]any{
		{"tenants", "id", true, false, false},
		{"tenants", "display_name", false, true, true},
	}}
	publicTableRows := &identityScriptedRows{values: [][]any{{"tenants", true, true}}}
	functionRows := &identityScriptedRows{values: [][]any{
		{"catalog_core_lexical_query_v1(text)", true, false, false},
		{"catalog_core_lexical_vector_v1(text)", true, false, false},
	}}
	extensionRows := &identityScriptedRows{values: [][]any{
		{"pgcrypto", "catalog", "digest", "bytea, text", false, true, false},
		{"pgcrypto", "catalog", "digest", "text, text", false, true, false},
		{"vector", "catalog", "vector_dims", "vector", true, true, true},
	}}
	sequenceRows := &identityScriptedRows{values: [][]any{{"unexpected_sequence", true, false, false, true}}}
	transaction.queryResults = []identityQueryResult{
		{rows: columnRows},
		{rows: publicTableRows},
		{rows: functionRows},
		{rows: extensionRows},
		{rows: sequenceRows},
	}
	report, err := inspectRuntimePrivileges(context.Background(), transaction, "catalog", RuntimeRoleAPI, "api")
	if err != nil {
		t.Fatal(err)
	}
	if report.Compliant || report.ManifestVersion != runtimePrivilegeManifestVersion || report.Schema != "catalog" || report.RuntimeRole != RuntimeRoleAPI || report.DatabaseRole != "api" {
		t.Fatalf("runtime privilege report = %#v", report)
	}
	if len(report.Differences) != maxPrivilegeDifferences {
		t.Fatalf("difference count = %d, want bounded %d", len(report.Differences), maxPrivilegeDifferences)
	}
	differences := strings.Join(report.Differences, "\n")
	for _, expected := range []string{"schema CREATE is not permitted", "unexpected PUBLIC privilege on Catalog schema", "missing SELECT on tenants.display_name", "unexpected INSERT on tenants.display_name"} {
		if !strings.Contains(differences, expected) {
			t.Fatalf("privilege report omitted %q: %#v", expected, report.Differences)
		}
	}
	if len(transaction.queryRows) != 0 || len(transaction.queryResults) != 0 {
		t.Fatalf("privilege inspection left scripted results: rows=%d queries=%d", len(transaction.queryRows), len(transaction.queryResults))
	}
	if len(transaction.queryArguments) < 4 || !slices.Equal(transaction.queryArguments[3], []any{runtimeCryptoExtension, "api", "api"}) ||
		!strings.Contains(transaction.queryStatements[3], "has_schema_privilege($3") ||
		!strings.Contains(transaction.queryStatements[3], "namespace.nspname='pg_catalog'") ||
		!strings.Contains(transaction.queryStatements[3], "extension.extname<>'plpgsql'") {
		t.Fatalf("extension visibility query = %q %#v", transaction.queryStatements[3], transaction.queryArguments[3])
	}
	if !slices.ContainsFunc(transaction.queryRowStatements, func(statement string) bool {
		return strings.Contains(statement, "catalog_schema_migrations") && strings.Contains(statement, "acldefault")
	}) {
		t.Fatalf("future-object default inspection does not resolve the Catalog migration owner and built-in defaults: %#v", transaction.queryRowStatements)
	}
	for name, rows := range map[string]*identityScriptedRows{
		"columns": columnRows, "public tables": publicTableRows, "functions": functionRows, "extensions": extensionRows, "sequences": sequenceRows,
	} {
		if !rows.closed {
			t.Fatalf("%s rows were not closed", name)
		}
	}
}

func TestDatabasePrivilegeEntryPointsRejectNilPools(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if _, err := reconcileRuntimePrivilegesForTest(ctx, nil, "api", "worker"); err == nil {
		t.Fatal("nil privilege-reconciliation pool was accepted")
	}
	if _, err := VerifyRuntimeRole(ctx, nil, RuntimeRoleAPI); err == nil {
		t.Fatal("nil runtime-verification pool was accepted")
	}
	if _, err := Plan(ctx, nil); err == nil {
		t.Fatal("nil migration-plan pool was accepted")
	}
	if _, err := applyMigrationsForTest(ctx, nil); err == nil {
		t.Fatal("nil migration-apply pool was accepted")
	}
	if _, _, err := ApplyAndReconcileRuntimePrivileges(ctx, nil, "api", "worker"); err == nil {
		t.Fatal("nil combined migration pool was accepted")
	}
}

func assertUniquePrivilegeColumns(t *testing.T, runtimeRole, table, operation string, columns []string) {
	t.Helper()
	seen := make(map[string]bool, len(columns))
	for _, column := range columns {
		if column == "" || seen[column] {
			t.Fatalf("%s %s list for %s contains invalid column %q", runtimeRole, operation, table, column)
		}
		seen[column] = true
	}
}
