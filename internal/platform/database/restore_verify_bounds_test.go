package database

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func TestRestoreOversizedMetadataIsNotHydrated(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		scan     func(context.Context, pgx.Tx, string, string, int) ([]restoreRecord, bool, error)
		position []any
		maximum  int64
		code     string
	}{
		{"evidence", scanRestoreValidationEvidence, []any{"record"}, maxRestoreEvidenceMetaBytes, "RESTORE_VALIDATION_EVIDENCE_INVALID"},
		{"listing", scanRestoreListingRevisions, []any{"record"}, maxRestoreSmallRecordBytes, "RESTORE_LISTING_REVISION_INVALID"},
		{"preview", scanRestoreSourcePreviews, []any{"record"}, maxRestoreSmallRecordBytes, "RESTORE_SOURCE_PREVIEW_INVALID"},
		{"source revision", scanRestoreSourceRevisions, []any{restoreTestTenant, int64(1)}, maxRestoreSmallRecordBytes, "RESTORE_SOURCE_REVISION_INVALID"},
		{"source snapshot", scanRestoreSourceSnapshots, []any{"record"}, maxRestoreSmallRecordBytes, "RESTORE_SOURCE_SNAPSHOT_INVALID"},
		{"retrieval state", scanRestoreSourceRetrievalStates, []any{restoreTestTenant, int64(1), "request"}, maxRestoreRetrievalBodyBytes + maxRestoreSmallRecordBytes, "RESTORE_SOURCE_RETRIEVAL_STATE_INVALID"},
		{"observation", scanRestoreObservations, []any{"record"}, maxRestoreObservationBytes, "RESTORE_OBSERVATION_EVIDENCE_INVALID"},
		{"publication", scanRestorePublicationSnapshots, []any{"record"}, publication.MaxPublicationSnapshotBytes + maxRestoreSmallDocumentBytes, "RESTORE_PUBLICATION_SNAPSHOT_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, oversized := range []bool{false, true} {
				size := tc.maximum
				if oversized {
					size++
				}
				row := append(append([]any(nil), tc.position...), size)
				rows := &identityScriptedRows{values: [][]any{row, row}}
				failure := errors.New("follow-up read failed")
				tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}, queryRows: []pgx.Row{scannerFunc(func(...any) error { return failure })}}
				records, more, err := tc.scan(t.Context(), tx, restoreTestTenant, "", 1)
				if !rows.closed {
					t.Fatal("metadata rows left open")
				}
				if oversized {
					if err != nil || !more || len(records) != 1 || records[0].decodedBytes != 0 || records[0].finding == nil || records[0].finding.Code != tc.code || len(tx.queryRowStatements) != 0 {
						t.Fatalf("oversized metadata hydrated or not reported: records=%+v more=%v error=%v", records, more, err)
					}
				} else {
					if len(tx.queryRowStatements) != 1 || tx.queryRowArguments[0][0] != restoreTestTenant {
						t.Fatal("in-bound record was not read with tenant binding")
					}
					// Listing decode errors are retained findings; other read failures
					// abort the stage. Neither may silently certify the record.
					if tc.name == "listing" {
						if err != nil || !more || len(records) != 1 || records[0].finding == nil || records[0].finding.Code != "RESTORE_LISTING_REVISION_UNREADABLE" {
							t.Fatalf("listing read failure=%+v %v", records, err)
						}
					} else if !errors.Is(err, failure) || len(records) != 0 || more {
						t.Fatalf("read failure lost: records=%+v more=%v error=%v", records, more, err)
					}
				}
			}
		})
	}
}

func TestRestoreVersionMetadataBoundsBeforeHydration(t *testing.T) {
	t.Parallel()
	valid := []any{1, int64(1), 0, int64(0), 0, int64(0), false, int64(1), int64(1)}
	for _, tc := range []struct {
		name  string
		index int
		value any
	}{
		{"no representations", 0, 0},
		{"too many representations", 0, 17},
		{"representation bytes", 1, int64(catalog.MaxPublicationProjectionBytes + 1)},
		{"inventory count", 2, maxRestoreRevisionFiles + 1},
		{"inventory bytes", 3, int64(maxRestorePackageMetaBytes + 1)},
		{"reference count", 4, 33},
		{"reference bytes", 5, int64(maxRestorePackageMetaBytes + 1)},
		{"revision metadata", 7, int64(maxRestoreRevisionMetaBytes + 1)},
		{"evidence metadata", 8, int64(maxRestoreEvidenceMetaBytes + 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := append([]any(nil), valid...)
			values[tc.index] = tc.value
			rows := &identityScriptedRows{values: [][]any{{"revision"}, {"next"}}}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}, queryRows: []pgx.Row{scriptedValueRow(values)}}
			records, more, err := scanRestoreVersionRevisions(t.Context(), tx, restoreTestTenant, "", 1)
			if err != nil || !more || len(records) != 1 || !rows.closed || records[0].decodedBytes != 0 || records[0].finding == nil || records[0].finding.Code != "RESTORE_VERSION_REVISION_BOUNDS_INVALID" {
				t.Fatalf("bounds result=%+v more=%v error=%v", records, more, err)
			}
			if len(tx.queryStatements) != 1 || len(tx.queryRowStatements) != 1 || !reflect.DeepEqual(tx.queryRowArguments[0], []any{restoreTestTenant, "revision", maxRestoreRevisionFiles + 1, 33}) {
				t.Fatal("oversized revision triggered hydration or lost bounded selection")
			}
		})
	}
}

func TestRetainedSnapshotAttemptComparison(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 10, 12, 0, 0, 123456000, time.UTC)
	first := federation.RetrievalEvidence{ID: "first", RequestKey: "request", RequestedURLDigest: "requested", FinalURLDigest: "final", ProfileID: "profile", Status: 200, ReceivedBody: true, RepresentationDigest: "body", ETagDigest: "etag", LastModifiedDigest: "modified", FetchedAt: at}
	second := first
	second.ID = "second"
	toRow := func(value federation.RetrievalEvidence) []any {
		return []any{value.ID, value.RequestKey, value.RequestedURLDigest, value.FinalURLDigest, value.ProfileID, value.Status, value.ReceivedBody, value.Reused, value.RepresentationDigest, value.ETagDigest, value.LastModifiedDigest, value.FetchedAt}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*federation.RetrievalEvidence)
		match  bool
	}{
		{"equal", func(*federation.RetrievalEvidence) {}, true},
		{"database precision", func(v *federation.RetrievalEvidence) { v.FetchedAt = at.Add(999 * time.Nanosecond) }, true},
		{"identity", func(v *federation.RetrievalEvidence) { v.ID = "other" }, false},
		{"request key", func(v *federation.RetrievalEvidence) { v.RequestKey = "other" }, false},
		{"requested URL", func(v *federation.RetrievalEvidence) { v.RequestedURLDigest = "other" }, false},
		{"final URL", func(v *federation.RetrievalEvidence) { v.FinalURLDigest = "other" }, false},
		{"profile", func(v *federation.RetrievalEvidence) { v.ProfileID = "other" }, false},
		{"status", func(v *federation.RetrievalEvidence) { v.Status = 304 }, false},
		{"received body", func(v *federation.RetrievalEvidence) { v.ReceivedBody = false }, false},
		{"reused", func(v *federation.RetrievalEvidence) { v.Reused = true }, false},
		{"representation", func(v *federation.RetrievalEvidence) { v.RepresentationDigest = "other" }, false},
		{"etag", func(v *federation.RetrievalEvidence) { v.ETagDigest = "other" }, false},
		{"last modified", func(v *federation.RetrievalEvidence) { v.LastModifiedDigest = "other" }, false},
		{"timestamp", func(v *federation.RetrievalEvidence) { v.FetchedAt = at.Add(time.Microsecond) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := first
			tc.mutate(&actual)
			rows := &identityScriptedRows{values: [][]any{toRow(second), toRow(actual)}}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			match, err := retainedSnapshotAttemptsMatch(t.Context(), tx, restoreTestTenant, "snapshot", []federation.RetrievalEvidence{first, second})
			if err != nil || match != tc.match || !rows.closed || !reflect.DeepEqual(tx.queryArguments[0], []any{restoreTestTenant, "snapshot", maxRestoreSnapshotRetrievals + 1}) {
				t.Fatalf("match=%v error=%v closed=%v", match, err, rows.closed)
			}
		})
	}
	for _, count := range []int{0, 1, maxRestoreSnapshotRetrievals + 1} {
		t.Run(fmt.Sprintf("row count %d", count), func(t *testing.T) {
			rows := &identityScriptedRows{}
			for index := 0; index < count; index++ {
				value := first
				value.ID = fmt.Sprint(index)
				rows.values = append(rows.values, toRow(value))
			}
			match, err := retainedSnapshotAttemptsMatch(t.Context(), &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}, restoreTestTenant, "snapshot", []federation.RetrievalEvidence{first, second})
			if err != nil || match || !rows.closed {
				t.Fatalf("incorrect row count matched: %v %v", match, err)
			}
		})
	}
	tx := &identityScriptedTx{}
	if match, err := retainedSnapshotAttemptsMatch(t.Context(), tx, restoreTestTenant, "snapshot", make([]federation.RetrievalEvidence, maxRestoreSnapshotRetrievals+1)); err != nil || match || len(tx.queryStatements) != 0 {
		t.Fatal("oversized expected evidence triggered a read")
	}
	for _, mode := range []string{"query", "scan", "iteration"} {
		t.Run(mode, func(t *testing.T) {
			failure := errors.New("retrieval read failed")
			rows := &identityScriptedRows{}
			result := identityQueryResult{rows: rows}
			switch mode {
			case "query":
				result.err = failure
			case "scan":
				rows.values = [][]any{{}}
			case "iteration":
				rows.err = failure
			}
			match, err := retainedSnapshotAttemptsMatch(t.Context(), &identityScriptedTx{queryResults: []identityQueryResult{result}}, restoreTestTenant, "snapshot", nil)
			if match || err == nil || mode != "scan" && !errors.Is(err, failure) || mode != "query" && !rows.closed {
				t.Fatalf("failed comparison=%v error=%v closed=%v", match, err, rows.closed)
			}
		})
	}
}
