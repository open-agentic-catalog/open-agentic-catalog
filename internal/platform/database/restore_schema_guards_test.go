package database

import (
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Each row has the required critical properties, but its synthetic trigger
// declaration deliberately cannot pass the PostgreSQL 18 inventory digest.
// Exact deparsed DDL and actual guard execution retain integration coverage.
func restoreTriggerRows(t *testing.T, schema string) [][]any {
	t.Helper()
	sources, err := embeddedTriggerFunctionSources()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(expectedRestoreTriggers))
	for name := range expectedRestoreTriggers {
		names = append(names, name)
	}
	slices.Sort(names)
	rows := make([][]any, 0, len(names))
	for _, name := range names {
		expected := expectedRestoreTriggers[name]
		settings := []string{"search_path=pg_catalog"}
		if expected.function == "reject_federation_source_history_mutation" || expected.function == "reject_version_validation_run_mutation" {
			settings = nil
		}
		definer := expected.function == "admit_webhook_notification" || expected.function == "record_webhook_delivery_loss" || expected.function == "record_item_rating"
		source := sources[expected.function]
		if expected.function == "project_observation_catalog_search" {
			source = strings.ReplaceAll(source, "%s.catalog_core_lexical_vector_v1", pgx.Identifier{schema}.Sanitize()+".catalog_core_lexical_vector_v1")
		}
		if expected.function == "project_observation_control_filters" {
			for _, helper := range []string{"catalog_observation_license_v1", "catalog_observation_categories_v1"} {
				source = strings.ReplaceAll(source, "%s."+helper, pgx.Identifier{schema}.Sanitize()+"."+helper)
			}
		}
		rows = append(rows, []any{name, expected.table, expected.function, "O", strings.Join(expected.definition, " "), source, "plpgsql", definer, true, settings})
	}
	return rows
}

func TestRestoreTriggersRequireCriticalIdentityAndExecutionProperties(t *testing.T) {
	for _, mode := range []string{"critical inventory", "whitespace", "missing", "disabled", "table", "function", "language", "unexpected definer", "missing definer", "foreign owner", "search path", "unexpected search path", "timing", "source", "foreign lexical schema", "foreign filter schema"} {
		t.Run(mode, func(t *testing.T) {
			rows := &identityScriptedRows{values: restoreTriggerRows(t, privilegeTestSchema)}
			byName := func(name string) []any {
				return rows.values[slices.IndexFunc(rows.values, func(v []any) bool { return v[0] == name })]
			}
			ordinary := byName("resources_lifecycle")
			definer := byName("outbox_events_notification_admission")
			message := "catalog trigger inventory or definition drifted (observed digest "
			switch mode {
			case "whitespace":
				definition, definitionOK := ordinary[4].(string)
				source, sourceOK := ordinary[5].(string)
				if !definitionOK || !sourceOK {
					t.Fatal("trigger fixture definition/source is not text")
				}
				ordinary[4], ordinary[5] = " \n"+definition+"\t", " \n"+source+"\t"
			case "missing":
				rows.values = rows.values[1:]
				message = "critical trigger is missing"
			case "disabled":
				ordinary[3], message = "D", "critical trigger identity or execution properties drifted"
			case "table":
				ordinary[1], message = "jobs", "critical trigger identity or execution properties drifted"
			case "function":
				ordinary[2], message = "other_function", "critical trigger identity or execution properties drifted"
			case "language":
				ordinary[6], message = "sql", "critical trigger identity or execution properties drifted"
			case "unexpected definer":
				ordinary[7], message = true, "critical trigger identity or execution properties drifted"
			case "missing definer":
				definer[7], message = false, "critical trigger identity or execution properties drifted"
			case "foreign owner":
				definer[8], message = false, "critical trigger identity or execution properties drifted"
			case "search path":
				ordinary[9], message = []string{"search_path=public"}, "critical trigger identity or execution properties drifted"
			case "unexpected search path":
				byName("observation_occurrences_immutable")[9], message = []string{"search_path=pg_catalog"}, "critical trigger identity or execution properties drifted"
			case "timing":
				ordinary[4], message = "after update", "critical trigger definition drifted"
			case "source":
				ordinary[5], message = "BEGIN RETURN NEW; END", "critical trigger function definition drifted"
			case "foreign lexical schema":
				row := byName("observations_catalog_search_projection")
				source, ok := row[5].(string)
				if !ok {
					t.Fatal("trigger fixture source is not text")
				}
				row[5] = strings.ReplaceAll(source, pgx.Identifier{privilegeTestSchema}.Sanitize()+".catalog_core_lexical_vector_v1", "foreign_schema.catalog_core_lexical_vector_v1")
				message = "critical trigger function definition drifted"
			case "foreign filter schema":
				row := byName("observations_control_filter_projection")
				definition, ok := row[5].(string)
				if !ok {
					t.Fatal("trigger fixture source is not text")
				}
				row[5] = strings.ReplaceAll(definition, pgx.Identifier{privilegeTestSchema}.Sanitize()+".catalog_observation_license_v1", "foreign_schema.catalog_observation_license_v1")
				message = "critical trigger function definition drifted"
			}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			err := verifyRestoreTriggers(t.Context(), tx, privilegeTestSchema)
			if err == nil || !strings.HasPrefix(err.Error(), message) || !rows.closed {
				t.Fatalf("trigger error=%v, want %q; closed=%v", err, message, rows.closed)
			}
		})
	}
}

func TestRestoreTriggerInventoryBoundAndUnknownGuards(t *testing.T) {
	for _, count := range []int{0, maxRestoreSchemaTriggers, maxRestoreSchemaTriggers + 1} {
		rows := &identityScriptedRows{}
		for range count {
			rows.values = append(rows.values, []any{"unknown", "resources", "unknown", "O", "BEFORE UPDATE", "", "plpgsql", false, true, []string{}})
		}
		tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
		err := verifyRestoreTriggers(t.Context(), tx, privilegeTestSchema)
		message := "critical trigger is missing"
		if count > maxRestoreSchemaTriggers {
			message = "catalog trigger inventory exceeds its verification bound"
		}
		if err == nil || err.Error() != message || !rows.closed || rows.index != count {
			t.Fatalf("count %d: error=%v read=%d closed=%v", count, err, rows.index, rows.closed)
		}
	}
}
