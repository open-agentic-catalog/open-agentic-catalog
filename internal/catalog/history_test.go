package catalog

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestAuthoringHistoriesBoundPagesAndIsolation(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	const parent = "00000000-0000-7000-8000-000000000010"
	const first = "00000000-0000-7000-8000-000000000021"
	const second = "00000000-0000-7000-8000-000000000022"
	repo := NewMemoryRepository()
	repo.resources[parent] = Resource{ID: parent, TenantID: tenant}
	repo.versions[parent] = ResourceVersion{ID: parent, TenantID: tenant, ResourceID: parent}
	repo.listings[parent] = CatalogListing{ID: parent, TenantID: tenant, ResourceID: parent}
	for _, id := range []string{second, first} {
		repo.versions[id] = ResourceVersion{ID: id, TenantID: tenant, ResourceID: parent}
		repo.versionRevisions[id] = VersionRevision{ID: id, TenantID: tenant, ResourceVersionID: parent, RevisionNumber: 1}
		repo.listings[id] = CatalogListing{ID: id, TenantID: tenant, ResourceID: parent}
		repo.listingRevisions[id] = ListingRevision{ID: id, TenantID: tenant, ListingID: parent, RevisionNumber: 1}
		repo.submissions[id] = ReviewSubmission{ID: id, TenantID: tenant, ListingID: parent, State: SubmissionSubmitted}
		repo.releases[id] = ListingRelease{ID: id, TenantID: tenant, ListingID: parent}
	}
	service, err := NewService(Options{Repository: repo, Clock: &guardrailClock{now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, IDs: &administrationIDs{}, Validator: administrationValidator{}, CursorKey: []byte(strings.Repeat("a", 32)), Policy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{TenantID: tenant, Subject: "reader", Roles: []identity.Role{identity.RoleReader}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	unauthenticated := t.Context()
	other := actor
	other.TenantID = "00000000-0000-7000-8000-000000000002"
	otherCtx := identityfixture.WithActor(t.Context(), other)
	denied := actor
	denied.Roles = []identity.Role{identity.RoleServiceClient}
	deniedCtx := identityfixture.WithActor(t.Context(), denied)
	cases := map[string]func(context.Context, HistoryPageRequest) (any, error){
		"versions": func(ctx context.Context, p HistoryPageRequest) (any, error) {
			return service.ListVersions(ctx, parent, p)
		},
		"version revisions": func(ctx context.Context, p HistoryPageRequest) (any, error) {
			return service.ListVersionRevisions(ctx, parent, p)
		},
		"listings": func(ctx context.Context, p HistoryPageRequest) (any, error) {
			return service.ListListings(ctx, parent, p)
		},
		"listing revisions": func(ctx context.Context, p HistoryPageRequest) (any, error) {
			return service.ListListingRevisions(ctx, parent, p)
		},
		"submissions": func(ctx context.Context, p HistoryPageRequest) (any, error) {
			return service.ListSubmissions(ctx, SubmissionListRequest{HistoryPageRequest: p, ListingID: parent, State: SubmissionSubmitted})
		},
		"releases": func(ctx context.Context, p HistoryPageRequest) (any, error) {
			return service.ListReleases(ctx, parent, p)
		},
	}
	for name, read := range cases {
		t.Run(name, func(t *testing.T) {
			items, err := read(ctx, HistoryPageRequest{Limit: 1, AfterID: parent})
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(items)
			if err != nil {
				t.Fatal(err)
			}
			var ids []struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(body, &ids); err != nil {
				t.Fatal(err)
			}
			if len(ids) != 2 || ids[0].ID != first || ids[1].ID != second {
				t.Fatalf("page plus sentinel: %s", body)
			}
			items, err = read(ctx, HistoryPageRequest{Limit: 1, AfterID: second})
			if err != nil {
				t.Fatal(err)
			}
			body, _ = json.Marshal(items)
			if string(body) != "[]" {
				t.Fatalf("empty continuation: %s", body)
			}
			for _, invalid := range []HistoryPageRequest{{Limit: 0}, {Limit: 101}, {Limit: 1, AfterID: "bad"}} {
				if _, err := read(ctx, invalid); ErrorCodeOf(err) != CodeInvalidArgument {
					t.Fatalf("invalid page: %v", err)
				}
			}
			if _, err := read(otherCtx, HistoryPageRequest{Limit: 1}); ErrorCodeOf(err) != CodeNotFound {
				t.Fatalf("cross-tenant read: %v", err)
			}
			if _, err := read(deniedCtx, HistoryPageRequest{Limit: 1}); ErrorCodeOf(err) != CodeForbidden {
				t.Fatalf("role denial: %v", err)
			}
			if _, err := read(unauthenticated, HistoryPageRequest{Limit: 1}); ErrorCodeOf(err) != CodeForbidden {
				t.Fatalf("unauthenticated read: %v", err)
			}
		})
	}
	if _, err := service.ListSubmissions(ctx, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 1}, State: "invalid"}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("review state: %v", err)
	}
	if _, err := service.ListSubmissions(ctx, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 1}, ListingID: "bad"}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("listing filter: %v", err)
	}
	filtered, err := service.ListSubmissions(ctx, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 1}, State: SubmissionApproved})
	if err != nil || len(filtered) != 0 {
		t.Fatalf("state filter: %#v %v", filtered, err)
	}
	deleted := repo.resources[parent]
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	deleted.DeletedAt = &now
	repo.resources[parent] = deleted
	for name, read := range cases {
		if _, err := read(ctx, HistoryPageRequest{Limit: 1}); ErrorCodeOf(err) != CodeNotFound {
			t.Errorf("%s deleted parent: %v", name, err)
		}
	}
}
