package personal

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"strings"
	"testing"
)

type followTargets struct {
	authority
	values map[string]catalog.ReleaseObservation
}

func (f *followTargets) CurrentListingRelease(_ context.Context, id string) (catalog.ReleaseObservation, error) {
	o, ok := f.values[id]
	if !ok || f.deny {
		return catalog.ReleaseObservation{}, catalog.E(catalog.CodeForbidden, "private target")
	}
	return o, nil
}

func TestFollowObservationOwnershipAndTransitions(t *testing.T) {
	s, _, audit, _, ctx := fixture(t)
	a := &followTargets{values: map[string]catalog.ReleaseObservation{member: {ReleaseID: tenant, Revision: 1}, realm: {ReleaseID: member, Revision: 1}}}
	s.authority = a
	follow := Input{Type: "follow", ListingID: member, Label: "owner label"}
	first := a.values[member]
	item, err := s.SaveObserved(ctx, "", 0, follow, false, &first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Save(ctx, "", 0, Input{Type: "follow", ListingID: realm, Label: "other Listing"}, false)
	if err != nil {
		t.Fatal(err)
	}
	check := func(id, want string) {
		t.Helper()
		r, err := s.ResolveFollow(ctx, id)
		if err != nil || r.Change != want || (r.Current == nil) != (want == "") || (r.Status == "unavailable") != (want == "") {
			t.Fatalf("resolve %s: %+v, %v", want, r, err)
		}
	}
	check(item.ID, "unchanged")
	check(second.ID, "unacknowledged")
	changed := catalog.ReleaseObservation{ReleaseID: realm, Revision: 1}
	a.values[member] = changed
	check(item.ID, "changed")
	check(second.ID, "unacknowledged")
	follow.Label = "other proposed label"
	duplicate, err := s.SaveObserved(ctx, "", 0, follow, false, &first)
	if err != nil || !duplicate.Existing || duplicate.ID != item.ID || duplicate.Label != item.Label || duplicate.Revision != 1 || len(audit.records) != 2 {
		t.Fatal(duplicate, err)
	}
	if _, err = s.SaveObserved(ctx, item.ID, 1, follow, false, &first); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("accepted a stale target", err)
	}
	item, err = s.SaveObserved(ctx, item.ID, 1, follow, false, &changed)
	if err != nil {
		t.Fatal(err)
	}
	check(item.ID, "unchanged")
	changed.Revision++
	a.values[member] = changed
	check(item.ID, "changed")
	a.deny = true
	check(item.ID, "")
	item, err = s.Save(ctx, item.ID, item.Revision, follow, false)
	if err != nil {
		t.Fatal("owner rename", err)
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), changed.ReleaseID) || strings.Contains(string(raw), "acknowledged") {
		t.Fatal("private baseline escaped", string(raw))
	}
	if _, err = s.Save(ctx, "", 0, follow, false); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("admitted unreadable target", err)
	}
	a.deny = false
	check(item.ID, "changed")
	if _, err = s.SaveObserved(ctx, item.ID, 1, follow, false, &changed); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("accepted stale item revision", err)
	}
	if _, err = s.Save(ctx, item.ID, item.Revision, searchInput(), false); err == nil {
		t.Fatal("converted follow to search")
	}
	a.deny = true
	if _, err = s.Save(ctx, item.ID, item.Revision, Input{}, true); err != nil {
		t.Fatal("owner unfollow", err)
	}
}

func TestFollowRejectsMixedPreferenceAndInvalidObservations(t *testing.T) {
	s, _, _, _, ctx := fixture(t)
	for _, in := range []Input{{Type: "follow", Label: "x"}, {Type: "follow", Label: "x", ListingID: member, Search: &Criteria{}}, {Type: "search", Label: "x", ListingID: member, Search: &Criteria{}}} {
		if Validate(in) == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
	for _, o := range []catalog.ReleaseObservation{{Revision: 1}, {ReleaseID: member}} {
		if _, err := s.SaveObserved(ctx, "", 0, Input{Type: "follow", Label: "x", ListingID: member}, false, &o); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal(err)
		}
	}
}
