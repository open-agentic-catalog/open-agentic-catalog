package catalog

import (
	"encoding/json"
	"testing"
	"time"
)

// Direct repository reads must enforce parent visibility even when no service
// authorization check runs first. Fixtures model retained rows, not a workflow.
func TestMemoryHistoryParentGuardsAndLimits(t *testing.T) {
	t.Parallel()
	reads := map[string]func(*MemoryRepository) (any, error){
		"versions": func(r *MemoryRepository) (any, error) {
			return r.ListVersions(t.Context(), repositoryGuardrailTenant, "resource", HistoryPageRequest{Limit: 1})
		},
		"version revisions": func(r *MemoryRepository) (any, error) {
			return r.ListVersionRevisions(t.Context(), repositoryGuardrailTenant, "version", HistoryPageRequest{Limit: 1})
		},
		"listings": func(r *MemoryRepository) (any, error) {
			return r.ListListings(t.Context(), repositoryGuardrailTenant, "resource", HistoryPageRequest{Limit: 1})
		},
		"listing revisions": func(r *MemoryRepository) (any, error) {
			return r.ListListingRevisions(t.Context(), repositoryGuardrailTenant, "listing", HistoryPageRequest{Limit: 1})
		},
		"releases": func(r *MemoryRepository) (any, error) {
			return r.ListReleases(t.Context(), repositoryGuardrailTenant, "listing", HistoryPageRequest{Limit: 1})
		},
		"submissions": func(r *MemoryRepository) (any, error) {
			return r.ListSubmissions(t.Context(), repositoryGuardrailTenant, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 1}})
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			for _, state := range []string{"visible", "missing", "foreign", "deleted"} {
				t.Run(state, func(t *testing.T) {
					r := newRepositoryGuardrailFixture()
					// Add a lower-ID row to prove selection happens after sorting.
					v := r.versions["version"]
					v.ID = "first"
					r.versions[v.ID] = v
					vr := r.versionRevisions["version-revision"]
					vr.ID = "first"
					r.versionRevisions[vr.ID] = vr
					l := r.listings["listing"]
					l.ID = "first"
					r.listings[l.ID] = l
					lr := r.listingRevisions["listing-revision"]
					lr.ID = "first"
					r.listingRevisions[lr.ID] = lr
					release := r.releases["release"]
					release.ID = "first"
					r.releases[release.ID] = release
					submission := r.submissions["submission"]
					submission.ID = "first"
					r.submissions[submission.ID] = submission
					resource := r.resources["resource"]
					switch state {
					case "missing":
						delete(r.resources, resource.ID)
					case "foreign":
						resource.TenantID = "other-tenant"
						r.resources[resource.ID] = resource
					case "deleted":
						now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
						resource.DeletedAt = &now
						r.resources[resource.ID] = resource
					}
					items, err := read(r)
					if err != nil {
						t.Fatal(err)
					}
					raw, err := json.Marshal(items)
					if err != nil {
						t.Fatal(err)
					}
					var ids []struct {
						ID string `json:"id"`
					}
					if err := json.Unmarshal(raw, &ids); err != nil {
						t.Fatal(err)
					}
					if state == "visible" {
						if len(ids) != 1 || ids[0].ID != "first" {
							t.Fatalf("bounded sorted page: %s", raw)
						}
					} else if string(raw) != "[]" {
						t.Fatalf("invisible parent leaked history: %s", raw)
					}
				})
			}
		})
	}
}
