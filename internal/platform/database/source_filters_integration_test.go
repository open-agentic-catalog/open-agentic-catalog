//go:build integration

package database

import (
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

func TestPostgresSourceFiltersBeforeBounds(t *testing.T) {
	db := newHousekeepingDatabase(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	want := []string{}
	for i := range 40 {
		source := housekeepingSource(fmt.Sprintf("00000000-0000-7000-8000-%012d", 5000+i), housekeepingTenantA, now)
		source.DisplayName = "Earlier"
		if i >= 35 {
			source.DisplayName = "Exact Name"
		}
		if i == 38 {
			source.DisplayName = "exact name"
		}
		if i == 39 {
			source.TenantID = housekeepingTenantB
		}
		retained, err := db.federation.CreateSource(t.Context(), source)
		if err != nil {
			t.Fatal(err)
		}
		if i >= 35 && i != 38 {
			retained.Status = federation.SourcePaused
			if i == 37 {
				retained.Status = federation.SourceRemoved
			}
			retained.Revision++
			if _, err := db.federation.UpdateSource(t.Context(), retained, 1); err != nil {
				t.Fatal(err)
			}
		}
		if i == 35 || i == 36 {
			want = append(want, source.ID)
		}
	}
	q := federation.SourceListRequest{DisplayName: "Exact Name", Status: federation.SourcePaused, Limit: 1}
	first, err := db.federation.ListSourcePage(t.Context(), housekeepingTenantA, q)
	if err != nil || len(first) != 1 || first[0].ID != want[0] {
		t.Fatalf("first=%+v %v", first, err)
	}
	q.AfterDisplayName, q.AfterID = first[0].DisplayName, first[0].ID
	second, err := db.federation.ListSourcePage(t.Context(), housekeepingTenantA, q)
	if err != nil || len(second) != 1 || second[0].ID != want[1] {
		t.Fatalf("second=%+v %v", second, err)
	}
	q.AfterDisplayName, q.AfterID = "", ""
	for _, name := range []string{"Exact name", " Exact Name", "Exact Name ", "absent"} {
		q.DisplayName = name
		got, err := db.federation.ListSourcePage(t.Context(), housekeepingTenantA, q)
		if err != nil || len(got) != 0 {
			t.Fatalf("literal %q=%v %v", name, got, err)
		}
	}
	q.DisplayName = "Exact Name"
	q.Status = ""
	got, err := db.federation.ListSourcePage(t.Context(), housekeepingTenantA, q)
	if err != nil || len(got) != 1 || got[0].ID != want[0] {
		t.Fatalf("name without status=%+v %v", got, err)
	}
	q.DisplayName = ""
	q.Status = federation.SourcePaused
	got, err = db.federation.ListSourcePage(t.Context(), housekeepingTenantA, q)
	if err != nil || len(got) != 1 || got[0].ID != want[0] {
		t.Fatalf("status without name=%+v %v", got, err)
	}
	q.Status = ""
	q.Limit = 100
	got, err = db.federation.ListSourcePage(t.Context(), housekeepingTenantA, q)
	if err != nil || len(got) != 38 {
		t.Fatalf("default population=%d %v", len(got), err)
	}
	var plan string
	if err := db.operator.QueryRow(t.Context(), `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) SELECT id FROM federation_sources WHERE tenant_id=$1 AND status<>'removed' AND display_name COLLATE "C"=$2 COLLATE "C" AND status='paused' ORDER BY display_name COLLATE "C",id LIMIT 2`, housekeepingTenantA, "Exact Name").Scan(&plan); err != nil {
		t.Fatal(err)
	}
	t.Logf("Exact directory selection plan (40 synthetic rows): %s", plan)

	// Cursor comparison must use the ordering's exact spelling even when an
	// operator's database column uses a case-insensitive collation.
	upper := housekeepingSource("00000000-0000-7000-8000-000000005999", housekeepingTenantA, now)
	upper.DisplayName = "EXACT NAME"
	if _, err := db.federation.CreateSource(t.Context(), upper); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(t.Context(), `CREATE COLLATION source_filter_ci (provider=icu,locale='und-u-ks-level2',deterministic=false); ALTER TABLE federation_sources ALTER COLUMN display_name TYPE text COLLATE source_filter_ci`); err != nil {
		t.Fatal(err)
	}
	for _, status := range []federation.SourceStatus{"", federation.SourceActive} {
		q = federation.SourceListRequest{Status: status, AfterDisplayName: "exact name", AfterID: "00000000-0000-7000-8000-000000005038", Limit: 2}
		got, err := db.federation.ListSourcePage(t.Context(), housekeepingTenantA, q)
		if err != nil || len(got) != 0 {
			t.Fatalf("case-distinct continuation status=%q repeated earlier names: %+v %v", status, got, err)
		}
	}
}
