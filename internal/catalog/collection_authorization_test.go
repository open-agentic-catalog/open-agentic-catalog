package catalog

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const collectionNamespace = "aaaaaaaa-0000-7000-8000-000000000010"
const collectionHiddenNamespace = "aaaaaaaa-0000-7000-8000-000000000011"

type collectionEvidence struct{ records []identity.ChangeEvidence }

type failedFederationAdmission struct{ identity.PermissionStore }

func (s failedFederationAdmission) CheckPermissions(ctx context.Context, actor identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	for _, check := range checks {
		if check.Permission == "federation.discover" {
			return identity.PermissionDecision{}, identity.E(identity.CodeDependency, "current federation policy unavailable")
		}
	}
	return s.PermissionStore.CheckPermissions(ctx, actor, checks)
}

func TestSearchDoesNotDiscloseQueryWhenFederationAdmissionFails(t *testing.T) {
	f := newGuardrailFixture(t)
	store, err := identity.NewMemoryStore([]identity.Seed{{TenantID: guardrailTenantID, Issuer: "collection-failure", Subject: "reader", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}}})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: guardrailTenantID, Issuer: "collection-failure", Subject: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	actor = identity.BindPermissionStore(actor, failedFederationAdmission{PermissionStore: store})
	search, err := NewSearchService(f.service, unifiedFederatedSearcher{search: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		t.Fatal("failed admission disclosed a query")
		return FederatedCatalogSearchPage{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := search.Search(identity.WithActor(t.Context(), actor), SearchQuery{Text: "private query", Limit: 1})
	if ErrorCodeOf(err) != CodeDependencyUnavailable || len(page.Items) != 0 || page.DurableTotal != 0 {
		t.Fatal("failed authority silently degraded or disclosed results", page, err)
	}
}

func (s *collectionEvidence) AppendIdentityEvidence(_ context.Context, _ string, e identity.ChangeEvidence) error {
	s.records = append(s.records, e)
	return nil
}

// Retained policy and domain data are real in-memory implementations. Resource
// expectations are chosen independently: hidden rows sort first and carry a
// tag that must never contribute to results, counts or facets.
func collectionFixture(t *testing.T) (guardrailFixture, *identity.Service, context.Context, context.Context, context.Context) {
	t.Helper()
	f := newGuardrailFixture(t)
	sink := &collectionEvidence{}
	store, err := identity.NewMemoryStore([]identity.Seed{
		{TenantID: guardrailTenantID, Issuer: "collection-unit", Subject: "admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}},
		{TenantID: guardrailTenantID, Issuer: "collection-unit", Subject: "direct", ActorType: identity.ActorHuman, Namespaces: map[string][]identity.Role{collectionNamespace: {identity.RoleReader}}},
		{TenantID: guardrailTenantID, Issuer: "collection-unit", Subject: "group", ActorType: identity.ActorHuman},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: f.clock, IDs: &guardrailIDs{next: 900}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	actorContext := func(subject string) context.Context {
		actor, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: guardrailTenantID, Issuer: "collection-unit", Subject: subject})
		if err != nil {
			t.Fatal(err)
		}
		return identity.WithActor(t.Context(), actor)
	}
	owner, direct, grouped := actorContext("admin"), actorContext("direct"), actorContext("group")
	groupActor, _ := identity.FromContext(grouped)
	role, err := authority.SaveRoleDefinition(owner, "", 0, identity.RoleDefinitionInput{Name: "Scoped metadata and history", Permissions: []identity.Permission{"namespace.read", "resource.read", "resource.read_history"}, Reason: "Independent collection policy fixture"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	group, err := authority.SaveAccessGroup(owner, "", 0, identity.GroupInput{Name: "Collection readers", Reason: "Independent group path"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ChangeAccessGroupMember(owner, group.ID, "", groupActor.MembershipID, 0, "Add explicit member", "member", false); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GrantAuthorizationAssignment(owner, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, NamespaceID: collectionNamespace, Reason: "Read permitted namespace only"}, "grant"); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != 4 {
		t.Fatal("administration skipped its evidence", len(sink.records))
	}
	f.repo.namespaces[collectionNamespace] = Namespace{ID: collectionNamespace, TenantID: guardrailTenantID, Slug: "visible", DisplayName: "Visible"}
	f.repo.namespaces[collectionHiddenNamespace] = Namespace{ID: collectionHiddenNamespace, TenantID: guardrailTenantID, Slug: "aaa-hidden", DisplayName: "Hidden"}
	template := newRepositoryGuardrailFixture()
	record, ok := template.catalogRecordLocked(repositoryGuardrailTenant, template.listings["listing"], false)
	if !ok {
		t.Fatal("invalid retained template")
	}
	for i, name := range []string{"A hidden", "B visible", "C visible", "D hidden"} {
		value := clone(record)
		key := func(offset int) string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", offset+i) }
		ns := collectionHiddenNamespace
		tag := "hidden-only"
		if i == 1 || i == 2 {
			ns = collectionNamespace
			tag = "visible"
		}
		value.Resource.ID, value.Resource.TenantID, value.Resource.NamespaceID, value.Resource.CanonicalIdentifier = key(100), guardrailTenantID, ns, fmt.Sprintf("urn:air:catalog.example.com:default:collection-%d", i)
		value.Version.ID, value.Version.TenantID, value.Version.ResourceID = key(200), guardrailTenantID, value.Resource.ID
		value.VersionRevision.ID, value.VersionRevision.TenantID, value.VersionRevision.ResourceVersionID, value.VersionRevision.EvidenceSetID = key(300), guardrailTenantID, value.Version.ID, key(700)
		value.Evidence.ID, value.Evidence.TenantID = key(700), guardrailTenantID
		value.Listing.ID, value.Listing.TenantID, value.Listing.NamespaceID, value.Listing.ResourceID, value.Listing.CurrentReleaseID = key(400), guardrailTenantID, ns, value.Resource.ID, key(600)
		value.ListingRevision.ID, value.ListingRevision.TenantID, value.ListingRevision.ListingID, value.ListingRevision.VersionRevisionID = key(500), guardrailTenantID, value.Listing.ID, value.VersionRevision.ID
		value.ListingRevision.DisplayName, value.ListingRevision.Tags = name, []string{tag}
		value.Release.ID, value.Release.TenantID, value.Release.ListingID, value.Release.ListingRevisionID, value.Release.VersionRevisionID = key(600), guardrailTenantID, value.Listing.ID, value.ListingRevision.ID, value.VersionRevision.ID
		f.repo.resources[value.Resource.ID], f.repo.versions[value.Version.ID] = value.Resource, value.Version
		f.repo.versionRevisions[value.VersionRevision.ID], f.repo.evidenceSets[value.Evidence.ID] = value.VersionRevision, value.Evidence
		f.repo.listings[value.Listing.ID], f.repo.listingRevisions[value.ListingRevision.ID], f.repo.releases[value.Release.ID] = value.Listing, value.ListingRevision, value.Release
		submission := clone(template.submissions["submission"])
		submission.ID, submission.TenantID, submission.ListingID = key(800), guardrailTenantID, value.Listing.ID
		f.repo.submissions[submission.ID] = submission
	}
	return f, authority, owner, direct, grouped
}

func TestScopedCollectionsSelectBeforePagingCountsAndFacets(t *testing.T) {
	f, _, _, direct, grouped := collectionFixture(t)
	for name, ctx := range map[string]context.Context{"direct": direct, "group": grouped} {
		t.Run(name, func(t *testing.T) {
			ns, err := f.service.ListNamespaces(ctx, NamespaceListRequest{Limit: 1})
			if err != nil || len(ns) != 1 || ns[0].ID != collectionNamespace {
				t.Fatal("namespace population", ns, err)
			}
			resources, err := f.service.ListResources(ctx, ResourceListRequest{Limit: 1})
			if err != nil || len(resources) != 2 {
				t.Fatal("resource sentinel", resources, err)
			}
			for _, r := range resources {
				if r.NamespaceID != collectionNamespace {
					t.Fatal("hidden resource", r)
				}
			}
			resources, err = f.service.ListResources(ctx, ResourceListRequest{Limit: 1, AfterUpdatedAt: &resources[0].UpdatedAt, AfterID: resources[0].ID})
			if err != nil || len(resources) != 1 {
				t.Fatal("resource continuation", resources, err)
			}
			first, err := f.service.Search(ctx, SearchQuery{Limit: 1})
			if err != nil || first.Total != 2 || len(first.Items) != 1 || first.Items[0].ListingRevision.DisplayName != "B visible" || first.NextCursor == "" {
				t.Fatal("first authorized page", first, err)
			}
			next, err := f.service.Search(ctx, SearchQuery{Limit: 1, Cursor: first.NextCursor})
			if err != nil || next.Total != 2 || len(next.Items) != 1 || next.Items[0].ListingRevision.DisplayName != "C visible" || next.NextCursor != "" {
				t.Fatal("terminal page", next, err)
			}
			facets, err := f.service.Explore(ctx, ExploreRequest{Facets: []FacetRequest{{Field: "tag"}, {Field: "namespace"}}})
			if err != nil || facets.Matched != 2 || !reflect.DeepEqual(facets.Facets["tag"].Buckets, []FacetBucket{{Value: "visible", Count: 2}}) {
				t.Fatal("facets included hidden records", facets, err)
			}
			hidden, err := f.service.Search(ctx, SearchQuery{Tags: []string{"hidden-only"}})
			if err != nil || hidden.Total != 0 || len(hidden.Items) != 0 {
				t.Fatal("hidden filter leak", hidden, err)
			}
			submissions, err := f.service.ListSubmissions(ctx, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 1}})
			if err != nil || len(submissions) != 2 {
				t.Fatal("review selection before sentinel", submissions, err)
			}
			for _, s := range submissions {
				if f.repo.listings[s.ListingID].NamespaceID != collectionNamespace {
					t.Fatal("hidden submission", s)
				}
			}
		})
	}
	// The same scope owns every fixed-parent history; these require history,
	// not content permission, and must deny a hidden parent.
	if rows, err := f.service.ListVersions(grouped, "00000000-0000-7000-8000-000000000101", HistoryPageRequest{Limit: 1}); err != nil || len(rows) != 1 {
		t.Fatal("scoped versions", rows, err)
	}
	if rows, err := f.service.ListVersionRevisions(grouped, "00000000-0000-7000-8000-000000000201", HistoryPageRequest{Limit: 1}); err != nil || len(rows) != 1 {
		t.Fatal("scoped version revisions", rows, err)
	}
	if rows, err := f.service.ListListings(grouped, "00000000-0000-7000-8000-000000000101", HistoryPageRequest{Limit: 1}); err != nil || len(rows) != 1 {
		t.Fatal("scoped listings", rows, err)
	}
	if rows, err := f.service.ListListingRevisions(grouped, "00000000-0000-7000-8000-000000000401", HistoryPageRequest{Limit: 1}); err != nil || len(rows) != 1 {
		t.Fatal("scoped listing revisions", rows, err)
	}
	if rows, err := f.service.ListReleases(grouped, "00000000-0000-7000-8000-000000000401", HistoryPageRequest{Limit: 1}); err != nil || len(rows) != 1 {
		t.Fatal("scoped releases", rows, err)
	}
	if _, err := f.service.ListVersions(grouped, "00000000-0000-7000-8000-000000000100", HistoryPageRequest{Limit: 1}); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("hidden parent history", err)
	}
	// Metadata/history authority must not implicitly authorize retained content.
	if _, _, _, err := f.service.GetVersionRevision(grouped, "00000000-0000-7000-8000-000000000301"); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("metadata granted content", err)
	}
	if _, err := f.service.GetCatalogRecord(grouped, "00000000-0000-7000-8000-000000000600"); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("hidden exact detail", err)
	}
}

func TestScopedSearchRevocationRejectsExistingCursorAndSkipsFederation(t *testing.T) {
	f, authority, owner, _, grouped := collectionFixture(t)
	calls := 0
	unified, err := NewSearchService(f.service, unifiedFederatedSearcher{search: func(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		calls++
		t.Fatal("namespace-only query disclosed to federation")
		return FederatedCatalogSearchPage{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := unified.Search(grouped, SearchQuery{Limit: 1})
	if err != nil || first.DurableTotal != 2 || len(first.Items) != 1 || first.NextCursor == "" || calls != 0 {
		t.Fatal(first, err, calls)
	}
	for _, origin := range []SearchResultType{SearchResultIndexed, SearchResultLive} {
		if _, err := unified.Search(grouped, SearchQuery{Origins: []SearchResultType{origin}}); ErrorCodeOf(err) != CodeForbidden || calls != 0 {
			t.Fatal("external origin admission", err, calls)
		}
	}
	groups, err := authority.ListAccessGroups(owner, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.SaveAccessGroup(owner, groups.Items[0].ID, groups.Items[0].Revision, identity.GroupInput{Reason: "Revoke pending page authority"}, identity.GroupSuspended, "suspend"); err != nil {
		t.Fatal(err)
	}
	if _, err := unified.Search(grouped, SearchQuery{Limit: 1, Cursor: first.NextCursor}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("revoked continuation admitted", err)
	}
	fresh, err := unified.Search(grouped, SearchQuery{Limit: 1})
	if err != nil || fresh.DurableTotal != 0 || len(fresh.Items) != 0 || calls != 0 {
		t.Fatal("revoked first page", fresh, err)
	}
}

// A repository can be reached after service admission but before it acquires
// its own lock. Every collection/history boundary must reject that stale read,
// rather than returning retained rows selected under withdrawn authority.
func TestMemoryCollectionRepositoriesRejectPolicyChangesBeforeSelection(t *testing.T) {
	f, authority, owner, _, grouped := collectionFixture(t)
	selected, _, err := identity.WithCollectionContext(grouped)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := authority.ListAccessGroups(owner, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.SaveAccessGroup(owner, groups.Items[0].ID, groups.Items[0].Revision, identity.GroupInput{Reason: "Revoke before repository selection"}, identity.GroupSuspended, "suspend"); err != nil {
		t.Fatal(err)
	}
	resource, version, listing := "00000000-0000-7000-8000-000000000101", "00000000-0000-7000-8000-000000000201", "00000000-0000-7000-8000-000000000401"
	for name, read := range map[string]func() error{
		"resource lookup": func() error { _, e := f.repo.GetResource(selected, guardrailTenantID, resource); return e },
		"identifier lookup": func() error {
			_, e := f.repo.GetResourceByIdentifier(selected, guardrailTenantID, "urn:air:catalog.example.com:collection:visible")
			return e
		},
		"version lookup": func() error { _, e := f.repo.GetVersion(selected, guardrailTenantID, version); return e },
		"version label lookup": func() error {
			_, e := f.repo.GetVersionByLabel(selected, guardrailTenantID, resource, "1.0.0")
			return e
		},
		"private revision": func() error {
			r, evidence, e := f.repo.GetVersionRevision(selected, guardrailTenantID, "00000000-0000-7000-8000-000000000301")
			if r.ID != "" || evidence.ID != "" {
				t.Fatal("stale content disclosure")
			}
			return e
		},
		"private evidence": func() error {
			_, e := f.repo.GetValidationEvidence(selected, guardrailTenantID, "00000000-0000-7000-8000-000000000301", "missing")
			return e
		},
		"document": func() error {
			doc, e := f.repo.GetDocument(selected, guardrailTenantID, "missing")
			if doc.ID != "" {
				t.Fatal("stale document disclosure")
			}
			return e
		},
		"listing lookup": func() error { _, e := f.repo.GetListing(selected, guardrailTenantID, listing); return e },
		"listing revision lookup": func() error {
			_, e := f.repo.GetListingRevision(selected, guardrailTenantID, "00000000-0000-7000-8000-000000000501")
			return e
		},
		"submission lookup": func() error { _, e := f.repo.GetSubmission(selected, guardrailTenantID, "missing"); return e },
		"release lookup": func() error {
			_, e := f.repo.GetRelease(selected, guardrailTenantID, "00000000-0000-7000-8000-000000000601")
			return e
		},
		"record lookup": func() error {
			r, e := f.repo.GetCatalogRecord(selected, guardrailTenantID, "00000000-0000-7000-8000-000000000601")
			if r.Release.ID != "" {
				t.Fatal("stale record disclosure")
			}
			return e
		},
		"local precedence": func() error {
			found, e := f.repo.HasPublishedIdentity(selected, guardrailTenantID, "urn:air:catalog.example.com:collection:visible", "1.0.0")
			if found {
				t.Fatal("stale local precedence")
			}
			return e
		},
		"namespaces": func() error {
			rows, e := f.repo.ListNamespaces(selected, guardrailTenantID, NamespaceListRequest{Limit: 1})
			if len(rows) != 0 {
				t.Fatal("stale namespace disclosure")
			}
			return e
		},
		"resources": func() error {
			rows, e := f.repo.ListResources(selected, guardrailTenantID, ResourceListRequest{Limit: 1})
			if len(rows) != 0 {
				t.Fatal("stale resource disclosure")
			}
			return e
		},
		"versions": func() error {
			rows, e := f.repo.ListVersions(selected, guardrailTenantID, resource, HistoryPageRequest{Limit: 1})
			if len(rows) != 0 {
				t.Fatal("stale version disclosure")
			}
			return e
		},
		"version revisions": func() error {
			rows, e := f.repo.ListVersionRevisions(selected, guardrailTenantID, version, HistoryPageRequest{Limit: 1})
			if len(rows) != 0 {
				t.Fatal("stale revision disclosure")
			}
			return e
		},
		"listings": func() error {
			rows, e := f.repo.ListListings(selected, guardrailTenantID, resource, HistoryPageRequest{Limit: 1})
			if len(rows) != 0 {
				t.Fatal("stale listing disclosure")
			}
			return e
		},
		"listing revisions": func() error {
			rows, e := f.repo.ListListingRevisions(selected, guardrailTenantID, listing, HistoryPageRequest{Limit: 1})
			if len(rows) != 0 {
				t.Fatal("stale listing revision disclosure")
			}
			return e
		},
		"releases": func() error {
			rows, e := f.repo.ListReleases(selected, guardrailTenantID, listing, HistoryPageRequest{Limit: 1})
			if len(rows) != 0 {
				t.Fatal("stale release disclosure")
			}
			return e
		},
		"reviews": func() error {
			rows, e := f.repo.ListSubmissions(selected, guardrailTenantID, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 1}})
			if len(rows) != 0 {
				t.Fatal("stale review disclosure")
			}
			return e
		},
		"search": func() error {
			page, e := f.repo.SearchCatalog(selected, guardrailTenantID, CatalogSearchRepositoryRequest{Limit: 1})
			if len(page.Items) != 0 || page.Total != 0 {
				t.Fatal("stale search disclosure")
			}
			return e
		},
		"facets": func() error {
			page, e := f.repo.ExploreCatalog(selected, guardrailTenantID, SearchQuery{}, []FacetRequest{{Field: "tag"}})
			if page.Matched != 0 || len(page.Facets) != 0 {
				t.Fatal("stale facet disclosure")
			}
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			if e := read(); identity.ErrorCodeOf(e) != identity.CodeInvalidArgument {
				t.Fatal("stale snapshot accepted", e)
			}
		})
	}
	fresh, _, err := identity.WithCollectionContext(grouped)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := f.repo.ListResources(fresh, "00000000-0000-7000-8000-000000000999", ResourceListRequest{Limit: 1}); identity.ErrorCodeOf(err) != identity.CodeForbidden || len(rows) != 0 {
		t.Fatal("collection argument crossed tenant boundary", rows, err)
	}
}

func TestLocalShadowOnlyUsesVisibleCurrentReleases(t *testing.T) {
	f, _, _, _, grouped := collectionFixture(t)
	ctx, _, err := identity.WithCollectionContext(grouped)
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range map[int]bool{0: false, 1: true} {
		identifier := fmt.Sprintf("urn:air:catalog.example.com:default:collection-%d", index)
		found, err := f.repo.HasPublishedIdentity(ctx, guardrailTenantID, identifier, "1.0.0")
		if err != nil || found != want {
			t.Fatal("shadow population", found, want, err)
		}
	}
	listing := f.repo.listings["00000000-0000-7000-8000-000000000401"]
	listing.CurrentReleaseID = ""
	f.repo.listings[listing.ID] = listing
	if found, err := f.repo.HasPublishedIdentity(ctx, guardrailTenantID, "urn:air:catalog.example.com:default:collection-1", "1.0.0"); err != nil || found {
		t.Fatal("historical release shadowed discovery", found, err)
	}
}
