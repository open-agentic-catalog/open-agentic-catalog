// Review queue selection narrows the authorized population before page limits.
package catalog

import (
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestMemoryReviewNamespaceSelectionBeforeLimit(t *testing.T) {
	t.Parallel()
	const namespace = "aaaaaaaa-0000-7000-8000-000000000010"
	const other = "aaaaaaaa-0000-7000-8000-000000000011"
	repo := NewMemoryRepository()
	ctx, _, err := identity.WithCollectionContext(identityfixture.WithActor(t.Context(), identity.Actor{TenantID: repositoryGuardrailTenant, Subject: "reader", Roles: []identity.Role{identity.RoleReader}}))
	if err != nil {
		t.Fatal(err)
	}
	// Three nonmatching submissions sort before the sparse selected population.
	for i := range 6 {
		key := fmt.Sprintf("00000000-0000-7000-8000-%012d", i+1)
		ns := other
		if i >= 3 {
			ns = namespace
		}
		repo.resources[key] = Resource{ID: key, TenantID: repositoryGuardrailTenant, NamespaceID: ns, CanonicalIdentifier: "private:" + key}
		repo.listings[key] = CatalogListing{ID: key, TenantID: repositoryGuardrailTenant, NamespaceID: ns, ResourceID: key}
		repo.submissions[key] = ReviewSubmission{ID: key, TenantID: repositoryGuardrailTenant, ListingID: key, State: SubmissionApproved}
	}
	read := func(request SubmissionListRequest, want ...string) {
		t.Helper()
		items, err := repo.ListSubmissions(ctx, repositoryGuardrailTenant, request)
		if err != nil || len(items) != len(want) {
			t.Fatalf("selected page %+v: %+v %v", request, items, err)
		}
		for i := range want {
			if items[i].ID != want[i] {
				t.Fatalf("item %d = %s, want %s", i, items[i].ID, want[i])
			}
		}
	}
	const fourth = "00000000-0000-7000-8000-000000000004"
	const fifth = "00000000-0000-7000-8000-000000000005"
	const sixth = "00000000-0000-7000-8000-000000000006"
	request := SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 2}, NamespaceID: namespace, State: SubmissionApproved}
	read(request, fourth, fifth)
	request.AfterID = fifth
	read(request, sixth)
	request.AfterID = ""
	request.ListingID = fourth
	read(request, fourth)
	request.NamespaceID = other
	read(request)
	request.ListingID = ""
	request.NamespaceID = "aaaaaaaa-0000-7000-8000-000000000099"
	read(request)
	request.NamespaceID = namespace
	request.State = SubmissionSubmitted
	read(request)
	request.State = SubmissionApproved
	request.Unreleased = true
	repo.releases["retained"] = ListingRelease{TenantID: repositoryGuardrailTenant, SubmissionID: fourth, State: ReleaseRevoked}
	read(request, fifth, sixth)
}

func TestMemoryReviewNamespaceFieldsFollowIndependentPermissions(t *testing.T) {
	t.Parallel()
	repo := NewMemoryRepository()
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	row := pass2ReviewSelectionRow(repo, 1, at, at, SubmissionApproved)
	resource := repo.resources[repo.listings[row.ListingID].ResourceID]
	resource.CanonicalIdentifier = "urn:air:example.com:units:review"
	repo.resources[resource.ID] = resource
	revision := repo.listingRevisions[row.ListingRevisionID]
	revision.DisplayName = "Submitted review title"
	repo.listingRevisions[revision.ID] = revision
	for _, test := range []struct {
		name       string
		grants     []identity.Permission
		wantRows   int
		identifier string
		display    string
	}{
		{name: "history only", grants: []identity.Permission{"resource.read_history"}, wantRows: 1},
		{name: "history and metadata", grants: []identity.Permission{"resource.read_history", "resource.read"}, wantRows: 1, identifier: "urn:air:example.com:units:review"},
		{name: "history and content", grants: []identity.Permission{"resource.read_history", "resource.read_content"}, wantRows: 1, display: "Submitted review title"},
		{name: "all fields", grants: []identity.Permission{"resource.read_history", "resource.read", "resource.read_content"}, wantRows: 1, identifier: "urn:air:example.com:units:review", display: "Submitted review title"},
		{name: "metadata and content without history", grants: []identity.Permission{"resource.read", "resource.read_content"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := pass2CatalogSelectionContext(t, test.grants...)
			rows, err := repo.ListSubmissions(ctx, pass2CatalogTenant, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 1}, NamespaceID: pass2CatalogNamespace})
			if err != nil || len(rows) != test.wantRows {
				t.Fatal("filtered history population", rows, err)
			}
			if len(rows) != 0 && (rows[0].ID != row.ID || rows[0].CanonicalIdentifier != test.identifier || rows[0].SubmittedDisplayName != test.display) {
				t.Fatalf("filtered context disclosed wrong fields: %+v", rows[0])
			}
		})
	}
}
