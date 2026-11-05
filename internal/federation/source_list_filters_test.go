package federation

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestSourceListFiltersBeforeBounds(t *testing.T) {
	store := NewMemoryStore()
	for i := range 40 {
		source := Source{ID: fmt.Sprintf("00000000-0000-7000-8000-%012d", i+1), TenantID: "tenant-a", DisplayName: "Earlier", Status: SourceActive}
		if i >= 35 {
			source.DisplayName = "Exact Name"
			source.Status = SourcePaused
		}
		switch i {
		case 36:
			source.Status = SourceActive
		case 37:
			source.DisplayName = "exact name"
		case 38:
			source.Status = SourceRemoved
		case 39:
			source.TenantID = "tenant-b"
		}
		store.sources[source.ID] = source
	}
	second := Source{ID: "00000000-0000-7000-8000-000000000099", TenantID: "tenant-a", DisplayName: "Exact Name", Status: SourcePaused}
	store.sources[second.ID] = second
	q := SourceListRequest{DisplayName: "Exact Name", Status: SourcePaused, Limit: 1}
	first, err := store.ListSourcePage(context.Background(), "tenant-a", q)
	if err != nil || len(first) != 1 || first[0].ID != "00000000-0000-7000-8000-000000000036" {
		t.Fatalf("first=%+v %v", first, err)
	}
	q.AfterDisplayName, q.AfterID = first[0].DisplayName, first[0].ID
	last, err := store.ListSourcePage(context.Background(), "tenant-a", q)
	if err != nil || len(last) != 1 || last[0].ID != second.ID {
		t.Fatalf("continuation=%+v %v", last, err)
	}
	for _, name := range []string{"exact Name", " Exact Name", "Exact Name ", "missing"} {
		got, err := store.ListSourcePage(context.Background(), "tenant-a", SourceListRequest{DisplayName: name, Limit: 1})
		if err != nil || len(got) != 0 {
			t.Fatalf("literal %q=%v %v", name, got, err)
		}
	}
	for _, state := range []SourceStatus{SourceActive, SourcePaused, SourceError} {
		item := second
		item.Status = state
		store.sources[item.ID] = item
		got, err := store.ListSourcePage(context.Background(), "tenant-a", SourceListRequest{DisplayName: item.DisplayName, Status: state, Limit: 100})
		found := false
		for _, s := range got {
			if s.ID == item.ID {
				found = true
			}
			if s.Status != state {
				t.Fatal("state predicate omitted")
			}
		}
		if err != nil || !found {
			t.Fatalf("state %s missing: %v", state, err)
		}
	}
	got, err := store.ListSourcePage(context.Background(), "tenant-a", SourceListRequest{Limit: 100})
	if err != nil || len(got) != 39 {
		t.Fatalf("nonremoved default=%d %v", len(got), err)
	}
}

func TestSourceListFilterValidation(t *testing.T) {
	for _, q := range []SourceListRequest{{DisplayName: strings.Repeat("界", 161)}, {DisplayName: "a\x00b"}, {DisplayName: string([]byte{255})}, {Status: SourceRemoved}, {Status: "Active"}} {
		if catalog.ErrorCodeOf(validateSourceListFilters(q)) != catalog.CodeInvalidArgument {
			t.Fatalf("accepted %+v", q)
		}
	}
	for _, q := range []SourceListRequest{{}, {DisplayName: strings.Repeat("界", 160)}, {DisplayName: " spaced "}, {Status: SourceActive}, {Status: SourcePaused}, {Status: SourceError}} {
		if err := validateSourceListFilters(q); err != nil {
			t.Fatalf("refused %+v %v", q, err)
		}
	}
}
