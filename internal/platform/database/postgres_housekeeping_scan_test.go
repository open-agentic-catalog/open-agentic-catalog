// Housekeeping units cover aggregate projection and progress combination without
// executing cleanup SQL.
package database

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/housekeeping"
)

func TestCombineHousekeepingProgress(t *testing.T) {
	t.Parallel()
	const limit = 10
	for _, test := range []struct {
		name          string
		deleted       housekeeping.Result
		marked        int64
		markedSeconds float64
		want          housekeeping.Result
	}{
		{name: "empty pages"},
		{name: "marking only", marked: 2, markedSeconds: 4,
			want: housekeeping.Result{Marked: 2, OldestEligibleAge: 4 * time.Second}},
		{name: "deletion only", deleted: housekeeping.Result{Reclaimed: 3, OldestEligibleAge: 7 * time.Second},
			want: housekeeping.Result{Reclaimed: 3, OldestEligibleAge: 7 * time.Second}},
		{name: "partial pages above combined limit", deleted: housekeeping.Result{Reclaimed: 6, OldestEligibleAge: 9 * time.Second}, marked: 5, markedSeconds: 3,
			want: housekeeping.Result{Reclaimed: 6, Marked: 5, OldestEligibleAge: 9 * time.Second}},
		{name: "older marked page", deleted: housekeeping.Result{Reclaimed: 4, OldestEligibleAge: 3 * time.Second}, marked: 5, markedSeconds: 9.5,
			want: housekeeping.Result{Reclaimed: 4, Marked: 5, OldestEligibleAge: 9500 * time.Millisecond}},
		{name: "equal ages", deleted: housekeeping.Result{Reclaimed: 1, OldestEligibleAge: 5 * time.Second}, marked: 2, markedSeconds: 5,
			want: housekeeping.Result{Reclaimed: 1, Marked: 2, OldestEligibleAge: 5 * time.Second}},
		{name: "marking saturated", deleted: housekeeping.Result{Reclaimed: 2, OldestEligibleAge: 3 * time.Second}, marked: limit, markedSeconds: 9,
			want: housekeeping.Result{Reclaimed: 2, Marked: limit, Saturated: true, OldestEligibleAge: 9 * time.Second}},
		{name: "deletion saturated", deleted: housekeeping.Result{Reclaimed: limit, Saturated: true, OldestEligibleAge: 9 * time.Second}, marked: 2, markedSeconds: 3,
			want: housekeeping.Result{Reclaimed: limit, Marked: 2, Saturated: true, OldestEligibleAge: 9 * time.Second}},
		{name: "both saturated", deleted: housekeeping.Result{Reclaimed: limit, Saturated: true, OldestEligibleAge: 3 * time.Second}, marked: limit, markedSeconds: 9,
			want: housekeeping.Result{Reclaimed: limit, Marked: limit, Saturated: true, OldestEligibleAge: 9 * time.Second}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := combineHousekeepingProgress(test.deleted, test.marked, test.markedSeconds, limit); got != test.want {
				t.Fatalf("combined progress = %+v; want %+v", got, test.want)
			}
		})
	}
}

// TestScanHousekeepingResult preserves counts and saturation and converts the
// selected page's age from seconds. Clamp boundaries have their own tests.
func TestScanHousekeepingResult(t *testing.T) {
	t.Parallel()
	const operation = "reclaim expired idempotency rows"
	for _, test := range []struct {
		name    string
		seconds float64
		want    housekeeping.Result
	}{
		{name: "empty page"},
		{name: "marked page", seconds: 3, want: housekeeping.Result{Marked: 5, OldestEligibleAge: 3 * time.Second}},
		{name: "partial page", seconds: 2.75, want: housekeeping.Result{Reclaimed: 7, OldestEligibleAge: 2750 * time.Millisecond}},
		{name: "saturated page", seconds: 120, want: housekeeping.Result{Reclaimed: housekeeping.BatchLimit, Saturated: true, OldestEligibleAge: 2 * time.Minute}},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := scriptedValueRow([]any{test.want.Reclaimed, test.want.Marked, test.want.Saturated, test.seconds})
			got, err := scanHousekeepingResult(row, operation)
			if err != nil || got != test.want {
				t.Fatalf("scan = %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
	t.Run("scan failure", func(t *testing.T) {
		failure := errors.New("synthetic aggregate scan failure")
		got, err := scanHousekeepingResult(scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, int64(7))
			return failure
		}), operation)
		if got != (housekeeping.Result{}) || catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || !errors.Is(err, failure) || !strings.Contains(err.Error(), operation) {
			t.Fatalf("scan failure returned result=%+v error=%v", got, err)
		}
	})
}
