package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Relation and oversize cases represent damaged recovery input, outside current
// schema guarantees. Ordinary adapter controls preserve JSONB and row bindings.
func TestRestoreArtworkBoundsHydrationAndClassifiesRetainedCorruption(t *testing.T) {
	value, raw := retainedArtworkUnitValue(t)
	for _, mode := range []string{"valid", "relation mismatch", "wrong field type", "digest mismatch", "oversized", "read failure", "continuation"} {
		t.Run(mode, func(t *testing.T) {
			size := int64(len(raw))
			binding := true
			retained := raw
			switch mode {
			case "relation mismatch":
				binding = false
			case "wrong field type":
				retained = artworkWithNumericField(t, raw, "reason")
			case "digest mismatch":
				retained = []byte(strings.Replace(string(raw), "Hide publisher icon", "Show publisher icon", 1))
			case "oversized":
				size = 49153
			}
			if mode != "oversized" {
				size = int64(len(retained))
			}
			rows := &identityScriptedRows{values: [][]any{{value.ID, size}}}
			if mode == "continuation" {
				rows.values = append(rows.values, []any{"00000000-0000-7000-8000-000000000052", size})
			}
			failure := errors.New("retained artwork unavailable")
			row := scriptedValueRow([]any{retained, binding})
			if mode == "read failure" {
				row = scannerFunc(func(...any) error { return failure })
			}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}, queryRows: []pgx.Row{row}}
			records, more, err := scanRestoreSourceArtwork(t.Context(), tx, value.TenantID, "", 1)
			if !rows.closed || !reflect.DeepEqual(tx.queryArguments, [][]any{{value.TenantID, "", 2}}) {
				t.Fatal("restore selection lost page bound or cleanup")
			}
			if mode == "read failure" {
				if !errors.Is(err, failure) || records != nil || more {
					t.Fatal("failed hydration returned a successful page", err)
				}
				return
			}
			if err != nil || len(records) != 1 || more != (mode == "continuation") || records[0].key != value.ID {
				t.Fatalf("restore records=%+v more=%t error=%v", records, more, err)
			}
			invalid := mode != "valid" && mode != "continuation"
			if (records[0].finding != nil) != invalid {
				t.Fatalf("corruption disposition=%+v", records[0])
			}
			if invalid && records[0].finding.Code != "RESTORE_SOURCE_ARTWORK_INVALID" {
				t.Fatal("incorrect restore finding", records[0].finding)
			}
			if mode == "oversized" {
				if len(tx.queryRowStatements) != 0 || records[0].decodedBytes != 0 {
					t.Fatal("oversized retained artwork was hydrated")
				}
			} else if records[0].decodedBytes != size || !reflect.DeepEqual(tx.queryRowArguments, [][]any{{value.TenantID, value.ID}}) {
				t.Fatal("restore hydration lost exact tenant, identity or byte accounting")
			}
		})
	}
}
