package personal

import (
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestExactLabelFiltersBeforePageBoundsWithoutChangingOwnership(t *testing.T) {
	s, store, _, _, ctx := fixture(t)
	testClock, ok := s.clock.(*clock.Fake)
	if !ok {
		t.Fatal("label fixture requires an explicitly advanced clock")
	}
	labels := []string{"Earlier unrelated", "Case", "case", " Case ", "café", "cafe\u0301", "%_", "🙂", "Case"}
	want := []string{}
	for _, label := range labels {
		testClock.Advance(time.Millisecond)
		input := searchInput()
		input.Label = label
		item, err := s.Save(ctx, "", 0, input, false)
		if err != nil {
			t.Fatal(err)
		}
		if label == "Case" {
			want = append(want, item.ID)
		}
	}
	actor, _ := identity.FromContext(ctx)
	label := "Case"
	page, err := s.List(ctx, ListRequest{Label: &label, Type: "search", Limit: 1})
	if err != nil || len(page) != 2 || page[0].ID != want[0] || page[1].ID != want[1] {
		t.Fatal("exact filter must precede limit plus one", page, err)
	}
	page, err = s.List(ctx, ListRequest{Label: &label, AfterID: want[0], Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != want[1] {
		t.Fatal("filtered continuation", page, err)
	}
	for _, label := range labels[2:8] {
		page, err := s.List(ctx, ListRequest{Label: &label, Limit: 1})
		if err != nil || len(page) != 1 || page[0].Label != label {
			t.Fatal("literal spelling, whitespace, Unicode and wildcards", label, page, err)
		}
	}
	page, err = s.List(ctx, ListRequest{Label: &label, Type: "resource", Limit: 1})
	if err != nil || len(page) != 0 {
		t.Fatal("type and label must intersect", page, err)
	}
	for _, dimension := range []string{"membership", "realm", "tenant"} {
		other := actor
		switch dimension {
		case "membership":
			other.MembershipID = realm
		case "realm":
			other.RealmID = member
		case "tenant":
			other.TenantID = realm
		}
		otherCtx := identityfixture.WithActor(t.Context(), other)
		page, err := s.List(otherCtx, ListRequest{Label: &label, Limit: 1})
		if err != nil || len(page) != 0 {
			t.Fatal("label selection escaped owner", dimension, page, err)
		}
	}
	for _, invalid := range []string{"", " \t", "\n", "label\u0085", strings.Repeat("🙂", 121), string([]byte{0xff})} {
		if _, err := s.List(ctx, ListRequest{Label: &invalid, Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal("invalid supplied label accepted", err)
		}
		if _, err := store.List(ctx, actor, ListRequest{Label: &invalid, Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal("store accepted invalid supplied label", err)
		}
	}
	maximum := strings.Repeat("🙂", 120)
	page, err = s.List(ctx, ListRequest{Label: &maximum, Limit: 1})
	if err != nil || len(page) != 0 {
		t.Fatal("valid 120-code-point label", page, err)
	}
}
