// Feedback row tests preserve private record projections without a database.
package database

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
)

// TestScanFeedback preserves all selected fields, including coalesced optional
// context and cleared text, and classifies missing rows and driver failures.
func TestScanFeedback(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 19, 13, 0, 0, 123456000, time.UTC)
	base := feedback.Record{
		ID: "00000000-0000-7000-8000-000000000021", TenantID: restoreTestTenant,
		MembershipID: "00000000-0000-7000-8000-000000000022", ResourceID: "00000000-0000-7000-8000-000000000023",
		Category: "documentation", Comment: "Clarify configuration — thanks.", State: "open", Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	addressed := base
	addressed.ReleaseID = "00000000-0000-7000-8000-000000000024"
	addressed.VersionRevisionID = "00000000-0000-7000-8000-000000000025"
	addressed.Response, addressed.State, addressed.Revision = "Added an example.", "addressed", 2
	addressed.UpdatedAt = now.Add(time.Hour)
	cleared := addressed
	cleared.Comment, cleared.Response, cleared.State, cleared.Revision = "", "", "removed", 3
	cleared.UpdatedAt = now.Add(2 * time.Hour)
	for _, want := range []feedback.Record{base, addressed, cleared} {
		t.Run(want.State, func(t *testing.T) {
			row := scriptedValueRow([]any{want.ID, want.TenantID, want.MembershipID, want.ResourceID,
				want.ReleaseID, want.VersionRevisionID, want.Category, want.Comment, want.Response,
				want.State, want.Revision, want.CreatedAt, want.UpdatedAt})
			got, err := scanFeedback(row)
			if err != nil || got != want {
				t.Fatalf("scan = %+v, %v; want %+v", got, err, want)
			}
		})
	}
	failure := errors.New("synthetic feedback scan failure")
	for _, test := range []struct {
		name string
		err  error
		code catalog.ErrorCode
	}{
		{"missing", pgx.ErrNoRows, catalog.CodeNotFound},
		{"driver", failure, catalog.CodeDependencyUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := scanFeedback(scannerFunc(func(...any) error { return test.err }))
			if catalog.ErrorCodeOf(err) != test.code || (errors.Is(test.err, failure) && !errors.Is(err, failure)) {
				t.Fatalf("scan error = %v; want code %s", err, test.code)
			}
		})
	}
}
