package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestImportIdentityCharacterLimits(t *testing.T) {
	t.Parallel()
	for _, character := range []string{"x", "é", "🙂"} {
		for _, field := range []string{"identifier", "version", "relationship identifier", "relationship version"} {
			limit := 128
			if strings.Contains(field, "identifier") {
				limit = 512
			}
			for _, count := range []int{limit - 1, limit, limit + 1} {
				t.Run(fmt.Sprintf("%s/%s/%d", field, character, count), func(t *testing.T) {
					dir, manifest := importTestInput(t)
					value := strings.Repeat(character, count)
					resource := &manifest.Resources[0]
					switch field {
					case "identifier":
						resource.CanonicalIdentifier = value
					case "version":
						resource.Version = value
					default:
						rel := catalog.Relationship{Type: "depends_on", TargetIdentifier: "custom:external", TargetVersion: "1"}
						if field == "relationship identifier" {
							rel.TargetIdentifier = value
						} else {
							rel.TargetVersion = value
						}
						resource.Listing.Relationships = []catalog.Relationship{rel}
					}
					plan, err := prepareImportTest(t, dir, manifest, 2<<20)
					if count > limit {
						if err == nil {
							t.Fatal("oversized import was prepared")
						}
						return
					}
					if err != nil || !plan.Preview.LocallyValid {
						t.Fatalf("valid Unicode import: %v", err)
					}
					var create catalog.CreateResourceInput
					var version catalog.CreateVersionInput
					var listing catalog.CreateListingRevisionInput
					if json.Unmarshal(plan.Bodies[0], &create) != nil || json.Unmarshal(plan.Bodies[1], &version) != nil || json.Unmarshal(plan.Bodies[3], &listing) != nil || create.CanonicalIdentifier != resource.CanonicalIdentifier || version.Version != resource.Version || len(listing.Relationships) != len(resource.Listing.Relationships) {
						t.Fatal("prepared command changed an identity")
					}
					if len(listing.Relationships) == 1 && listing.Relationships[0] != resource.Listing.Relationships[0] {
						t.Fatal("prepared command changed a relationship")
					}
				})
			}
		}
	}
}

func TestUnicodeImportStillEnforcesRequestByteBudget(t *testing.T) {
	t.Parallel()
	dir, manifest := importTestInput(t)
	manifest.Resources[0].CanonicalIdentifier = strings.Repeat("🙂", 512)
	manifest.Resources[0].Version = strings.Repeat("🙂", 128)
	plan, err := prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	limit := len(plan.Bodies[0])
	for _, body := range plan.Bodies {
		if len(body) > limit {
			limit = len(body)
		}
	}
	if _, err = prepareImportTest(t, dir, manifest, limit); err != nil {
		t.Fatalf("exact byte budget rejected: %v", err)
	}
	if _, err = prepareImportTest(t, dir, manifest, limit-1); err == nil {
		t.Fatal("character admission bypassed the request byte budget")
	}
}
