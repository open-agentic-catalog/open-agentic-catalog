package catalog

import (
	"fmt"
	"testing"
)

func TestReleaseRelationshipsPreserveDeclarationsAndScope(t *testing.T) {
	f, _, _, reader, _ := collectionFixture(t)
	id := func(n int) string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", n) }
	target := f.repo.resources[id(102)]
	exact := Relationship{Type: "depends_on", TargetIdentifier: target.CanonicalIdentifier, TargetVersion: f.repo.versions[id(202)].Version}
	unversioned := exact
	unversioned.TargetVersion = ""
	older := exact
	older.TargetVersion = "older"
	recommended := exact
	recommended.Type = "recommended_with"
	hidden := Relationship{Type: "bundled_with", TargetIdentifier: f.repo.resources[id(100)].CanonicalIdentifier}
	for _, n := range []int{0, 1, 3} {
		revision := f.repo.listingRevisions[id(500+n)]
		revision.Relationships = []Relationship{exact, unversioned, older, recommended, hidden}
		f.repo.listingRevisions[revision.ID] = revision
	}
	read := func(n int, view RelationshipView, limit int) RelationshipSelection {
		t.Helper()
		rows, err := f.service.ListReleaseRelationships(reader, id(n), RelationshipPageRequest{View: view, Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	page := read(601, OutgoingRelationships, 10)
	if len(page.Items) != 5 {
		t.Fatal(page)
	}
	for i, want := range []Relationship{exact, unversioned, older, recommended, hidden} {
		item := page.Items[i]
		if item.Declaration != want {
			t.Fatal("authored text changed", item)
		}
		if i == 2 || i == 4 {
			if item.Related != nil {
				t.Fatal("unavailable target leaked", item)
			}
		} else if item.Related == nil || item.Related.ReleaseID != id(602) || item.Related.ListingRevisionID != id(502) {
			t.Fatal(item)
		}
	}
	incoming := read(602, IncomingRelationships, 3)
	if len(incoming.Items) != 4 {
		t.Fatal(incoming)
	}
	for _, item := range incoming.Items {
		if item.Related.ReleaseID != id(601) || item.Related.ListingRevisionID != id(501) {
			t.Fatal("hidden source or wrong revision", item)
		}
	}
	deps := read(602, ReleaseDependencies, 2)
	if len(deps.Items) != 2 || deps.Items[0].Declaration != exact || deps.Items[1].Declaration != unversioned {
		t.Fatal(deps)
	}
	if _, err := f.service.ListReleaseRelationships(reader, id(600), RelationshipPageRequest{View: IncomingRelationships, Limit: 1}); ErrorCodeOf(err) != CodeNotFound {
		t.Fatal("hidden subject", err)
	}
	for _, request := range []RelationshipPageRequest{{View: "other", Limit: 1}, {View: IncomingRelationships, Limit: 0}, {View: IncomingRelationships, Limit: 101}} {
		if _, err := f.service.ListReleaseRelationships(reader, id(602), request); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal(request, err)
		}
	}
	old := f.repo.releases[id(602)]
	old.State = ReleaseRetired
	f.repo.releases[old.ID] = old
	if edges := read(601, OutgoingRelationships, 10).Items; edges[0].Related != nil || edges[1].Related != nil {
		t.Fatal("withdrawn target linked", edges)
	}
	_, err := f.service.ListReleaseRelationships(reader, id(602), RelationshipPageRequest{View: ReleaseDependencies, Limit: 1, Generation: deps.Generation, AfterReleaseID: id(601), AfterOrdinal: 1})
	if ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("stale cursor", err)
	}
}
