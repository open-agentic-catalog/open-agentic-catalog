package database

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Scripted rows check classification, bounds and redaction, not SQL integrity.
func restoreCuratedValues() []any {
	c := curatedStorageItem()
	return []any{c.ID, c.NamespaceID, c.Name, c.Description, true, c.Revision, c.CreatedAt, c.UpdatedAt, true, true, true, true, true}
}

func TestRestoreCuratedClassificationAndRedaction(t *testing.T) {
	for _, mode := range []string{"valid", "text bytes", "empty name", "unnormalized name", "unnormalized description", "revision", "created", "updated", "namespace", "tenant quota", "namespace quota", "member quota", "orphan target"} {
		t.Run(mode, func(t *testing.T) {
			v := restoreCuratedValues()
			v[3] = "PRIVATE_COLLECTION_DESCRIPTION"
			switch mode {
			case "text bytes":
				v[4] = false
			case "empty name":
				v[2] = ""
			case "unnormalized name":
				v[2] = " PRIVATE_COLLECTION_NAME "
			case "unnormalized description":
				v[3] = " PRIVATE_COLLECTION_DESCRIPTION "
			case "revision":
				v[5] = int64(0)
			case "created":
				v[6] = time.Time{}
			case "updated":
				v[7] = curatedStorageItem().CreatedAt.Add(-time.Second)
			case "namespace":
				v[8] = false
			case "tenant quota":
				v[9] = false
			case "namespace quota":
				v[10] = false
			case "member quota":
				v[11] = false
			case "orphan target":
				v[12] = false
			}
			rows := &identityScriptedRows{values: [][]any{v}}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			records, more, err := scanRestoreCollections(t.Context(), tx, restoreTestTenant, "previous", 2)
			if err != nil || more || len(records) != 1 || !rows.closed {
				t.Fatalf("records=%+v more=%v closed=%v err=%v", records, more, rows.closed, err)
			}
			if !reflect.DeepEqual(tx.queryArguments[0], []any{restoreTestTenant, "previous", 3}) {
				t.Fatalf("restore selection bindings=%v", tx.queryArguments)
			}
			r := records[0]
			name, nameOK := v[2].(string)
			description, descriptionOK := v[3].(string)
			if !nameOK || !descriptionOK {
				t.Fatal("fixture text must be strings")
			}
			if r.key != v[0] || r.decodedBytes != int64(len(name)+len(description)) {
				t.Fatalf("record accounting=%+v", r)
			}
			if mode == "valid" {
				if r.finding != nil {
					t.Fatalf("valid record flagged: %+v", r.finding)
				}
				return
			}
			if r.finding == nil || r.finding.Code != "RESTORE_SHARED_COLLECTION_INVALID" || r.finding.RecordID != v[0] || r.finding.Domain != "shared_collections" || r.finding.Severity != "error" || r.finding.Class != "integrity" {
				t.Fatalf("finding=%+v", r.finding)
			}
			raw, err := json.Marshal(r.finding)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "PRIVATE_COLLECTION") || strings.Contains(string(raw), curatedStorageItem().NamespaceID) {
				t.Fatalf("finding exposed contents or namespace: %s", raw)
			}
		})
	}
}

func TestRestoreCuratedPaginationAndFailures(t *testing.T) {
	for _, mode := range []string{"empty", "limit plus one", "query", "scan", "iteration"} {
		t.Run(mode, func(t *testing.T) {
			rows := &identityScriptedRows{values: [][]any{restoreCuratedValues()}}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			sentinel := errors.New("private restore detail")
			switch mode {
			case "empty":
				rows.values = nil
			case "limit plus one":
				extra := restoreCuratedValues()
				extra[0] = "next-id"
				extra[2] = " invalid next record "
				rows.values = append(rows.values, extra)
			case "query":
				tx.queryResults[0].err = sentinel
			case "scan":
				rows.values = append(rows.values, []any{"incomplete row"})
			case "iteration":
				rows.err = sentinel
			}
			records, more, err := scanRestoreCollections(t.Context(), tx, restoreTestTenant, "previous", 1)
			switch mode {
			case "empty":
				if err != nil || more || records == nil || len(records) != 0 {
					t.Fatalf("empty page=%v more=%v err=%v", records, more, err)
				}
			case "limit plus one":
				if err != nil || !more || len(records) != 1 || records[0].finding != nil || records[0].key != curatedStorageItem().ID {
					t.Fatalf("bounded page=%+v more=%v err=%v", records, more, err)
				}
			case "scan":
				if err == nil || more || records != nil {
					t.Fatalf("scan failure=%+v more=%v err=%v", records, more, err)
				}
			default:
				if !errors.Is(err, sentinel) || more {
					t.Fatalf("dependency error=%v more=%v", err, more)
				}
			}
			if mode != "query" && !rows.closed {
				t.Fatal("restore rows retained after completion")
			}
		})
	}
}
