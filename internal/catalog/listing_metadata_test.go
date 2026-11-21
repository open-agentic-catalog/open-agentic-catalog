package catalog

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestListingMetadataReleasedFields(t *testing.T) {
	f, _, _, _, reader := collectionFixture(t)
	id := func(n int) string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", n) }
	listing := f.repo.listings[id(402)]
	released := f.repo.listingRevisions[id(502)]
	// The competing candidate declares every field; the released pointer remains authoritative.
	candidate := released
	candidate.ID, candidate.DocumentationURL, candidate.SupportURL, candidate.License = id(580), "https://docs.example.test", "https://support.example.test", "MIT"
	f.repo.listingRevisions[candidate.ID] = candidate
	listing.CandidateRevisionID = candidate.ID
	f.repo.listings[listing.ID] = listing
	names := []string{"documentation", "support", "license"}
	beforeAudit, beforeEvents := len(f.repo.audit), len(f.repo.outbox)
	for present := range 8 {
		values := []string{"", "", ""}
		declared := []string{candidate.DocumentationURL, candidate.SupportURL, strings.Repeat("L", MaxCompactLicenseCharacters+1)}
		for i := range values {
			if present&(1<<i) != 0 {
				values[i] = declared[i]
			}
		}
		released.DocumentationURL, released.SupportURL, released.License = values[0], values[1], values[2]
		f.repo.listingRevisions[released.ID] = released
		for selected := 1; selected < 8; selected++ {
			var fields, want []string
			for i, name := range names {
				if selected&(1<<i) != 0 {
					fields = append(fields, name)
					if values[i] == "" {
						want = append(want, name)
					}
				}
			}
			slices.Reverse(fields)
			checks, err := ParseListingMetadataChecks(strings.Join(fields, ","))
			if err != nil {
				t.Fatal(err)
			}
			rows, err := f.service.ListListingMetadataGaps(reader, ListingMetadataQuery{Checks: checks, Limit: 1, AfterID: id(401)})
			if err != nil {
				t.Fatal(err)
			}
			if len(want) == 0 {
				if len(rows) != 0 {
					t.Fatalf("present %d selected %v returned %v", present, fields, rows)
				}
				continue
			}
			if len(rows) != 1 || rows[0].ListingID != listing.ID || rows[0].ListingRevisionID != released.ID || rows[0].ReleaseID != listing.CurrentReleaseID || rows[0].ResourceID != listing.ResourceID || rows[0].NamespaceID != collectionNamespace || !slices.Equal(rows[0].Missing, want) {
				t.Fatalf("present %d selected %v returned %v, want %v", present, fields, rows, want)
			}
		}
	}
	if len(f.repo.audit) != beforeAudit || len(f.repo.outbox) != beforeEvents {
		t.Fatal("metadata read wrote evidence")
	}
	for _, input := range []string{"", "tags", "support,support", ",license", "license,", "Documentation", "support, license"} {
		if _, err := ParseListingMetadataChecks(input); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal(input, err)
		}
	}
}

func TestListingMetadataCurrentReleaseAndIntegrity(t *testing.T) {
	f, _, _, reader, _ := collectionFixture(t)
	id := func(n int) string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", n) }
	q := ListingMetadataQuery{Checks: ListingMetadataChecks{Support: true}, Limit: 1}
	for _, index := range []int{1, 2} {
		lr := f.repo.listingRevisions[id(500+index)]
		lr.SupportURL = ""
		f.repo.listingRevisions[lr.ID] = lr
	}
	rel := f.repo.releases[id(602)]
	rel.State = ReleaseDeprecated
	f.repo.releases[rel.ID] = rel
	rows, err := f.service.ListListingMetadataGaps(reader, q)
	if err != nil || len(rows) != 2 || rows[0].ListingID != id(401) || rows[1].State != ReleaseDeprecated {
		t.Fatal("authorized lookahead", rows, err)
	}
	q.AfterID = rows[0].ListingID
	for _, state := range []ReleaseState{ReleaseRetired, ReleaseRevoked, ReleaseSuperseded} {
		rel.State = state
		f.repo.releases[rel.ID] = rel
		rows, err = f.service.ListListingMetadataGaps(reader, q)
		if err != nil || len(rows) != 0 {
			t.Fatal(state, rows, err)
		}
	}
	rel.State = ReleasePublished
	rel.ListingRevisionID = id(501) // Other Listing's valid sealed revision.
	f.repo.releases[rel.ID] = rel
	q.AfterID = ""
	if rows, err = f.service.ListListingMetadataGaps(reader, q); ErrorCodeOf(err) != CodeDependencyUnavailable || rows != nil {
		t.Fatal("partial page on broken binding", rows, err)
	}
	for _, bad := range []ListingMetadataQuery{{Limit: 1}, {Checks: q.Checks}, {Checks: q.Checks, Limit: 101}, {Checks: q.Checks, Limit: 1, Kind: "other"}, {Checks: q.Checks, Limit: 1, AfterID: "bad"}, {Checks: q.Checks, Limit: 1, NamespaceID: "bad"}} {
		if _, err := f.service.ListListingMetadataGaps(reader, bad); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal(bad, err)
		}
	}
}

func TestListingMetadataChecksIntegrityOnlyInReadableNamespaces(t *testing.T) {
	f, _, admin, reader, _ := collectionFixture(t)
	// A broken release in the first (hidden) Listing must not prevent a scoped
	// reader from reading the next two Listings or reveal an error in that namespace.
	const hidden = "00000000-0000-7000-8000-000000000400"
	delete(f.repo.releases, f.repo.listings[hidden].CurrentReleaseID)
	q := ListingMetadataQuery{Checks: ListingMetadataChecks{Support: true}, Limit: 1}
	rows, err := f.service.ListListingMetadataGaps(reader, q)
	if err != nil || len(rows) != 2 || rows[0].DisplayName != "B visible" || rows[1].DisplayName != "C visible" {
		t.Fatalf("scoped page and lookahead = %v, error = %v", rows, err)
	}
	q.NamespaceID = collectionHiddenNamespace
	if rows, err := f.service.ListListingMetadataGaps(reader, q); err != nil || len(rows) != 0 {
		t.Fatalf("hidden namespace = %v, error = %v", rows, err)
	}
	if rows, err := f.service.ListListingMetadataGaps(admin, q); ErrorCodeOf(err) != CodeDependencyUnavailable || rows != nil {
		t.Fatalf("authorized read of broken release = %v, error = %v", rows, err)
	}
}
