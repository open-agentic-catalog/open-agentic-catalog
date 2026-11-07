package catalog

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const (
	pass2CatalogTenant    = "00000000-0000-7000-8000-000000000061"
	pass2CatalogMember    = "00000000-0000-7000-8000-000000000062"
	pass2CatalogRealm     = "00000000-0000-7000-8000-000000000063"
	pass2CatalogNamespace = "00000000-0000-7000-8000-000000000064"
	pass2CatalogOther     = "00000000-0000-7000-8000-000000000065"
	pass2EmptyResource    = "00000000-0000-7000-8000-000000000071"
	pass2DraftResource    = "00000000-0000-7000-8000-000000000072"
	pass2DraftVersion     = "00000000-0000-7000-8000-000000000073"
	pass2DraftRevision    = "00000000-0000-7000-8000-000000000074"
	pass2DraftListing     = "00000000-0000-7000-8000-000000000075"
	pass2DraftListingRev  = "00000000-0000-7000-8000-000000000076"
)

// Only finite collection-permission decisions are scripted. No identity
// administration, application composition or external repository is involved.
type pass2CatalogSelectionPolicy struct {
	identity.PermissionStore
	allowed            map[identity.Permission]bool
	fail               identity.Permission
	failure            error
	failureCalls       int
	rowChecks          int
	failAfterRowChecks int
}

func (p *pass2CatalogSelectionPolicy) CheckPermissions(_ context.Context, _ identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	decision := identity.PermissionDecision{Revision: 41, Allowed: make([]bool, len(checks))}
	for i, check := range checks {
		if check.NamespaceID != "" {
			p.rowChecks++
		}
		if check.Permission == p.fail && p.rowChecks > p.failAfterRowChecks {
			p.failureCalls++
			return identity.PermissionDecision{}, p.failure
		}
		decision.Allowed[i] = check.NamespaceID == pass2CatalogNamespace && p.allowed[check.Permission]
	}
	return decision, nil
}

func pass2CatalogSelectionContext(t *testing.T, grants ...identity.Permission) (context.Context, *pass2CatalogSelectionPolicy) {
	t.Helper()
	policy := &pass2CatalogSelectionPolicy{allowed: make(map[identity.Permission]bool)}
	for _, grant := range grants {
		policy.allowed[grant] = true
	}
	actor := identity.Actor{TenantID: pass2CatalogTenant, MembershipID: pass2CatalogMember, RealmID: pass2CatalogRealm, Subject: "metadata-selection-reader", ActorType: identity.ActorHuman}
	ctx, _, err := identity.WithCollectionContext(identity.WithActor(t.Context(), identity.BindPermissionStore(actor, policy)))
	if err != nil {
		t.Fatal("valid collection control", err)
	}
	return ctx, policy
}

func pass2PersonalWorkRepository() *MemoryRepository {
	repo := NewMemoryRepository()
	owner := workOwner{pass2CatalogTenant, pass2CatalogMember, pass2CatalogRealm}
	repo.resources[pass2EmptyResource] = Resource{ID: pass2EmptyResource, TenantID: pass2CatalogTenant, NamespaceID: pass2CatalogNamespace, Kind: KindAgent, CanonicalIdentifier: "urn:air:example.com:units:empty"}
	repo.resources[pass2DraftResource] = Resource{ID: pass2DraftResource, TenantID: pass2CatalogTenant, NamespaceID: pass2CatalogNamespace, Kind: KindSkill, CanonicalIdentifier: "urn:air:example.com:units:draft"}
	repo.versions[pass2DraftVersion] = ResourceVersion{ID: pass2DraftVersion, TenantID: pass2CatalogTenant, ResourceID: pass2DraftResource, CandidateRevisionID: pass2DraftRevision, SealedRevisionID: pass2DraftRevision}
	repo.listings[pass2DraftListing] = CatalogListing{ID: pass2DraftListing, TenantID: pass2CatalogTenant, NamespaceID: pass2CatalogNamespace, ResourceID: pass2DraftResource, CandidateRevisionID: pass2DraftListingRev}
	repo.listingRevisions[pass2DraftListingRev] = ListingRevision{ID: pass2DraftListingRev, TenantID: pass2CatalogTenant, ListingID: pass2DraftListing, VersionRevisionID: pass2DraftRevision, DisplayName: "Private candidate title"}
	repo.workOwners = map[string]workOwner{workAttributionKey("resource", pass2EmptyResource, ""): owner, workAttributionKey("catalog_listing", pass2DraftListing, pass2DraftListingRev): owner}
	return repo
}

func TestPersonalWorkProjectionUsesSeparateFieldPermissions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                   string
		meta, history, content bool
	}{
		{name: "all fields", meta: true, history: true, content: true},
		{name: "metadata only", meta: true},
		{name: "history only", history: true},
		{name: "history and content", history: true, content: true},
		{name: "history and metadata", history: true, meta: true},
		{name: "no resource permissions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, policy := pass2CatalogSelectionContext(t)
			policy.allowed = map[identity.Permission]bool{"resource.read": tc.meta, "resource.read_history": tc.history, "resource.read_content": tc.content}
			repo := pass2PersonalWorkRepository()
			want := []PersonalWorkItem{}
			if tc.history {
				candidate := PersonalWorkItem{ID: "candidate:" + pass2DraftListing, Type: "candidate", State: "unsubmitted_candidate", ResourceID: pass2DraftResource, NamespaceID: pass2CatalogNamespace, Kind: KindSkill, ListingID: pass2DraftListing, ListingRevisionID: pass2DraftListingRev, VersionRevisionID: pass2DraftRevision, CurrentCandidateRevisionID: pass2DraftListingRev}
				if tc.meta {
					candidate.CanonicalIdentifier = "urn:air:example.com:units:draft"
				}
				if tc.content {
					candidate.DisplayName = "Private candidate title"
				}
				want = append(want, candidate)
			}
			if tc.meta {
				want = append(want, PersonalWorkItem{ID: "resource:" + pass2EmptyResource, Type: "resource", State: "empty_resource", ResourceID: pass2EmptyResource, NamespaceID: pass2CatalogNamespace, Kind: KindAgent, CanonicalIdentifier: "urn:air:example.com:units:empty"})
			}
			got, err := repo.ListPersonalWork(ctx, pass2CatalogTenant, PersonalWorkRequest{Queue: "drafts", Limit: 101})
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("work projection violated per-field disclosure: got=%+v want=%+v error=%v", got, want, err)
			}
		})
	}
}

func TestPersonalWorkProjectionExcludesDeletedEmptyResource(t *testing.T) {
	t.Parallel()
	ctx, _ := pass2CatalogSelectionContext(t, "resource.read")
	repo := pass2PersonalWorkRepository()
	query := PersonalWorkRequest{Queue: "drafts", Limit: 2}
	control, err := repo.ListPersonalWork(ctx, pass2CatalogTenant, query)
	if err != nil || len(control) != 1 || control[0].ResourceID != pass2EmptyResource {
		t.Fatal("valid owned empty-resource control", control, err)
	}
	resource := repo.resources[pass2EmptyResource]
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	resource.DeletedAt = &at
	repo.resources[resource.ID] = resource
	got, err := repo.ListPersonalWork(ctx, pass2CatalogTenant, query)
	if err != nil || len(got) != 0 {
		t.Fatal("deleted empty resource remained in personal work", got, err)
	}
}

func TestPersonalWorkProjectionRejectsCrossTenantBeforeRowChecks(t *testing.T) {
	t.Parallel()
	ctx, policy := pass2CatalogSelectionContext(t, "resource.read", "resource.read_history", "resource.read_content")
	repo := pass2PersonalWorkRepository()
	if control, err := repo.ListPersonalWork(ctx, pass2CatalogTenant, PersonalWorkRequest{Queue: "drafts", Limit: 101}); err != nil || len(control) != 2 {
		t.Fatal("valid current-tenant control", control, err)
	}
	policy.rowChecks = 0
	got, err := repo.ListPersonalWork(ctx, pass2CatalogOther, PersonalWorkRequest{Queue: "drafts", Limit: 101})
	if identity.ErrorCodeOf(err) != identity.CodeForbidden || got != nil || policy.rowChecks != 0 {
		t.Fatal("cross-tenant selection reached row permissions", got, err, policy.rowChecks)
	}
}

func TestPersonalWorkProjectionReturnsNoRowsOnPermissionFailure(t *testing.T) {
	t.Parallel()
	for _, permission := range []identity.Permission{"resource.read_history", "resource.read", "resource.read_content"} {
		for _, after := range []int{0, 3} {
			t.Run(fmt.Sprintf("%s/after-%d-checks", permission, after), func(t *testing.T) {
				ctx, policy := pass2CatalogSelectionContext(t, "resource.read", "resource.read_history", "resource.read_content")
				repo := pass2PersonalWorkRepository()
				query := PersonalWorkRequest{Queue: "drafts", Limit: 101}
				if control, err := repo.ListPersonalWork(ctx, pass2CatalogTenant, query); err != nil || len(control) != 2 {
					t.Fatal("valid work-selection control", control, err)
				}
				cause := errors.New("synthetic current-policy lookup failure")
				policy.fail, policy.failure = permission, cause
				// Each visible work row requires three field checks. A late
				// failure follows the fully admitted empty-resource row.
				policy.rowChecks, policy.failAfterRowChecks = 0, after
				got, err := repo.ListPersonalWork(ctx, pass2CatalogTenant, query)
				if !errors.Is(err, cause) || got != nil || policy.failureCalls != 1 || policy.rowChecks <= after {
					t.Fatal("permission failure became partial success or lost its cause", got, err, policy.failureCalls)
				}
			})
		}
	}
}
