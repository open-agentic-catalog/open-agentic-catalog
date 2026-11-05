package catalog

import (
	"strings"
	"testing"
)

func TestPublisherDisplayNameCharacterBounds(t *testing.T) {
	for _, count := range []int{159, 160, 161} {
		f := newGuardrailFixture(t)
		name := strings.Repeat("🙂", count)
		publisher, err := f.service.CreatePublisher(f.adminContext(), CreatePublisherInput{DisplayName: name, Domains: []string{"example.com"}})
		if count == 161 {
			if ErrorCodeOf(err) != CodeInvalidArgument {
				t.Fatalf("oversized publisher: %v", err)
			}
			continue
		}
		if err != nil || publisher.DisplayName != name {
			t.Fatalf("publisher name changed: %#v, %v", publisher, err)
		}
		updatedName := strings.Repeat("é", count)
		updated, err := f.service.UpdatePublisher(f.adminContext(), publisher.ID, PatchPublisherInput{DisplayName: &updatedName, Reason: "Correct name"}, publisher.Revision)
		if err != nil || updated.DisplayName != updatedName {
			t.Fatalf("publisher update: %#v, %v", updated, err)
		}
		oversized := strings.Repeat("é", 161)
		if _, err := f.service.UpdatePublisher(f.adminContext(), updated.ID, PatchPublisherInput{DisplayName: &oversized, Reason: "Invalid name"}, updated.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("oversized update: %v", err)
		}
		stored, err := f.service.GetPublisher(f.adminContext(), updated.ID)
		if err != nil || stored.DisplayName != updatedName || stored.Revision != updated.Revision {
			t.Fatalf("rejection changed publisher: %#v, %v", stored, err)
		}
	}
}

func TestListingAndAdoptionCharacterBounds(t *testing.T) {
	for _, count := range []int{159, 160} {
		f, input := adoptionFixture(t)
		input.DisplayName = strings.Repeat("🙂", count)
		input.Description = strings.Repeat("中", 4000)
		// Rejected text leaves the atomic adoption boundary untouched.
		for _, field := range []string{"name", "description", "NUL name", "NUL description"} {
			bad := input
			switch field {
			case "name":
				bad.DisplayName = strings.Repeat("é", 161)
			case "NUL name":
				bad.DisplayName = "bad\x00name"
			case "NUL description":
				bad.Description = "bad\x00description"
			default:
				bad.Description += "中"
			}
			if _, err := f.service.AdoptExternalObservation(f.adminContext(), bad); ErrorCodeOf(err) != CodeInvalidArgument {
				t.Fatalf("invalid adoption %s: %v", field, err)
			}
			if len(f.repo.resources) != 0 || len(f.repo.audit) != 2 || len(f.repo.outbox) != 2 {
				t.Fatal("rejected adoption wrote state or evidence")
			}
		}
		adopted, err := f.service.AdoptExternalObservation(f.adminContext(), input)
		if err != nil || adopted.ListingRevision.DisplayName != input.DisplayName || adopted.ListingRevision.Description != input.Description {
			t.Fatalf("valid adoption text: %#v, %v", adopted, err)
		}
		revision := CreateListingRevisionInput{VersionRevisionID: adopted.VersionRevision.ID, DisplayName: input.DisplayName, Description: input.Description, PublisherID: input.PublisherID, Visibility: VisibilityPrivate}
		listing, stored, err := f.service.CreateListingRevision(f.adminContext(), adopted.Listing.ID, revision, adopted.Listing.Revision)
		if err != nil || stored.DisplayName != input.DisplayName || stored.Description != input.Description {
			t.Fatalf("valid listing text: %#v, %v", stored, err)
		}
		for _, field := range []string{"name", "description", "NUL name", "NUL description"} {
			bad := revision
			switch field {
			case "name":
				bad.DisplayName = strings.Repeat("é", 161)
			case "NUL name":
				bad.DisplayName = "bad\x00name"
			case "NUL description":
				bad.Description = "bad\x00description"
			default:
				bad.Description += "中"
			}
			if _, _, err := f.service.CreateListingRevision(f.adminContext(), listing.ID, bad, listing.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
				t.Fatalf("invalid listing %s: %v", field, err)
			}
			if f.repo.listings[listing.ID].Revision != listing.Revision || len(f.repo.listingRevisions) != 2 {
				t.Fatal("rejected listing revision wrote state")
			}
		}
	}
}

func TestDisplayTextRejectsNULWithoutEvidence(t *testing.T) {
	f := newGuardrailFixture(t)
	ctx := f.adminContext()
	for _, field := range []string{"name", "description"} {
		input := CreateNamespaceInput{Slug: "valid", DisplayName: "Valid", Description: "Valid description"}
		if field == "name" {
			input.DisplayName += "\x00"
		} else {
			input.Description += "\x00"
		}
		if _, err := f.service.CreateNamespace(ctx, input); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("namespace %s: %v", field, err)
		}
	}
	if _, err := f.service.CreatePublisher(ctx, CreatePublisherInput{DisplayName: "Publisher\x00", Domains: []string{"example.com"}}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("publisher: %v", err)
	}
	if len(f.repo.namespaces) != 0 || len(f.repo.publishers) != 0 || len(f.repo.audit) != 0 || len(f.repo.outbox) != 0 {
		t.Fatal("rejection wrote state or evidence")
	}
	ns, err := f.service.CreateNamespace(ctx, CreateNamespaceInput{Slug: "valid", DisplayName: "Valid"})
	if err != nil {
		t.Fatal(err)
	}
	bad := "invalid\x00text"
	for _, patch := range []PatchNamespaceInput{{DisplayName: &bad, Reason: "Correct name"}, {Description: &bad, Reason: "Correct description"}} {
		if _, err := f.service.UpdateNamespace(ctx, ns.ID, patch, ns.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("namespace patch: %v", err)
		}
	}
	publisher, err := f.service.CreatePublisher(ctx, CreatePublisherInput{DisplayName: "Publisher", Domains: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.UpdatePublisher(ctx, publisher.ID, PatchPublisherInput{DisplayName: &bad, Reason: "Correct name"}, publisher.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("publisher patch: %v", err)
	}
	if f.repo.namespaces[ns.ID].Revision != ns.Revision || f.repo.publishers[publisher.ID].Revision != publisher.Revision || len(f.repo.audit) != 2 || len(f.repo.outbox) != 2 {
		t.Fatal("rejected updates wrote state or evidence")
	}
}
