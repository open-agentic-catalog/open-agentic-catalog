//go:build integration

package database

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// Fixtures use actual API roles and domain publication; expected populations
// are explicit, not inferred from returned rows or the permission predicate.
func TestPostgresScopedCollectionsAndPolicyContinuation(t *testing.T) {
	documents := []catalog.SearchDocument{
		{Identifier: "urn:air:catalog.example.com:finance:visible-b", DisplayName: "B visible", Description: "Visible collection fixture", Tags: []string{"visible"}},
		{Identifier: "urn:air:catalog.example.com:finance:visible-c", DisplayName: "C visible", Description: "Visible collection fixture", Tags: []string{"visible"}},
		{Identifier: "urn:air:catalog.example.com:finance:visible-d", DisplayName: "D visible", Description: "Visible collection fixture", Tags: []string{"visible"}},
	}
	db, repo, _, seed := newSearchCostFixture(t, documents...)
	for i := range documents {
		seed(i)
	}
	author := semanticDisclosureActor(t, db, housekeepingTenantA)
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "scoped-collection"})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 10})
	if err != nil || baseline.Total != 3 {
		t.Fatal(baseline, err)
	}
	visibleNamespace, publisher := baseline.Items[0].Listing.NamespaceID, baseline.Items[0].ListingRevision.PublisherID
	hiddenNamespace, err := service.CreateNamespace(author, catalog.CreateNamespaceInput{Slug: "hidden", DisplayName: "Hidden"})
	if err != nil {
		t.Fatal(err)
	}
	hiddenResource, err := service.CreateResource(author, catalog.CreateResourceInput{NamespaceID: hiddenNamespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:hidden:hidden-a", Kind: catalog.KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	_, hiddenRevision, _, err := service.CreateVersion(author, hiddenResource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillMarkdown, Content: "---\nname: hidden-fixture\ndescription: Private collection fixture.\n---\n# Instructions\nRead metadata only.\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	hiddenListing, err := service.CreateListing(author, catalog.CreateListingInput{NamespaceID: hiddenNamespace.ID, ResourceID: hiddenResource.ID})
	if err != nil {
		t.Fatal(err)
	}
	hiddenListing, _, err = service.CreateListingRevision(author, hiddenListing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: hiddenRevision.ID, DisplayName: "A hidden", Description: "Hidden collection fixture", PublisherID: publisher, Visibility: catalog.VisibilityTenant, Tags: []string{"hidden-only"}}, hiddenListing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	submission, err := service.Submit(author, hiddenListing.ID)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "search-reviewer", Roles: []identity.Role{identity.RoleReviewer}})
	if _, _, err = service.DecideSubmission(reviewer, submission.ID, "approve", "Review hidden fixture", submission.Revision); err != nil {
		t.Fatal(err)
	}
	_, hiddenRelease, _, err := service.Publish(author, hiddenListing.ID, catalog.PublishInput{SubmissionID: submission.ID}, hiddenListing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	identityStore, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: identityStore, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	directGrant, err := authority.GrantRole(author, identity.GrantRoleInput{Subject: "scoped-direct", ActorType: identity.ActorHuman, Role: identity.RoleReader, NamespaceID: visibleNamespace, Reason: "Qualify direct selection"}, "direct")
	if err != nil {
		t.Fatal(err)
	}
	tempGrant, err := authority.GrantRole(author, identity.GrantRoleInput{Subject: "scoped-group", ActorType: identity.ActorHuman, Role: identity.RoleReader, NamespaceID: visibleNamespace, Reason: "Establish group fixture member"}, "temp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = authority.RevokeRole(author, tempGrant.ID, "Remove temporary direct path", "revoke", tempGrant.Revision); err != nil {
		t.Fatal(err)
	}
	actorFor := func(subject string) identity.Actor {
		a, err := identityStore.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: subject})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	directActor, groupActor := actorFor("scoped-direct"), actorFor("scoped-group")
	role, err := authority.SaveRoleDefinition(author, "", 0, identity.RoleDefinitionInput{Name: "Scoped collections", Permissions: []identity.Permission{"namespace.read", "resource.read", "resource.read_history"}, Reason: "Independent scoped selection oracle"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	group, err := authority.SaveAccessGroup(author, "", 0, identity.GroupInput{Name: "Scoped readers", Reason: "Qualify real group selection"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []identity.Actor{directActor, groupActor} {
		if _, err = authority.ChangeAccessGroupMember(author, group.ID, "", a.MembershipID, 0, "Explicit fixture group membership", "member", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = authority.GrantAuthorizationAssignment(author, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, NamespaceID: visibleNamespace, Reason: "Select visible namespace"}, "grant"); err != nil {
		t.Fatal(err)
	}

	// Add a sparse directory population with actual database constraints. These
	// are drafts, so they must not inflate release search or facet totals.
	if _, err := db.operator.Exec(t.Context(), `INSERT INTO resources(id,tenant_id,namespace_id,canonical_identifier,kind,created_by,revision,created_at,updated_at)
 SELECT gen_random_uuid(),$1,$2,'urn:air:catalog.example.com:hidden:plan-'||n,'skill',$3,1,clock_timestamp(),clock_timestamp() FROM generate_series(1,512) n`, housekeepingTenantA, hiddenNamespace.ID, hiddenResource.CreatedBy); err != nil {
		t.Fatal(err)
	}
	planCtx, _, err := identity.WithCollectionContext(identity.WithActor(t.Context(), groupActor))
	if err != nil {
		t.Fatal(err)
	}
	_, err = withTenantCollectionRead(planCtx, repo.pool, housekeepingTenantA, func(tx pgx.Tx) (bool, error) {
		var raw []byte
		query := `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) SELECT id FROM resources WHERE tenant_id=$1 AND deleted_at IS NULL AND ` + collectionPermissionSQL("resource.read", "resources.namespace_id") + ` ORDER BY updated_at DESC,id DESC LIMIT 3`
		if err := tx.QueryRow(planCtx, query, housekeepingTenantA).Scan(&raw); err != nil {
			return false, err
		}
		var plan []map[string]any
		if err := json.Unmarshal(raw, &plan); err != nil {
			return false, err
		}
		root, ok := plan[0]["Plan"].(map[string]any)
		if !ok {
			t.Fatal("malformed EXPLAIN root")
		}
		if root["Actual Rows"] != float64(3) {
			t.Fatalf("sparse SQL selected hidden drafts: %s", raw)
		}
		var inspect func(map[string]any)
		inspect = func(node map[string]any) {
			if node["Relation Name"] == "role_assignments" {
				loops, ok := node["Actual Loops"].(float64)
				if !ok || loops > 1 {
					t.Fatal("sparse collection repeated its grant selection for each candidate")
				}
			}
			if children, ok := node["Plans"].([]any); ok {
				for _, child := range children {
					plan, ok := child.(map[string]any)
					if !ok {
						t.Fatal("malformed EXPLAIN child")
					}
					inspect(plan)
				}
			}
		}
		inspect(root)
		t.Logf("Sparse scoped directory: 516 resources, 3 eligible; actual EXPLAIN plan: %s", raw)
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	globalCtx, _, err := identity.WithCollectionContext(author)
	if err != nil {
		t.Fatal(err)
	}
	_, err = withTenantCollectionRead(globalCtx, repo.pool, housekeepingTenantA, func(tx pgx.Tx) (bool, error) {
		var raw []byte
		query := `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) SELECT id FROM resources WHERE tenant_id=$1 AND deleted_at IS NULL AND ` + collectionPermissionSQL("resource.read", "resources.namespace_id") + ` ORDER BY updated_at DESC,id DESC LIMIT 3`
		if err := tx.QueryRow(globalCtx, query, housekeepingTenantA).Scan(&raw); err != nil {
			return false, err
		}
		var plan []map[string]any
		if err := json.Unmarshal(raw, &plan); err != nil {
			return false, err
		}
		var inspect func(any)
		inspect = func(value any) {
			switch node := value.(type) {
			case map[string]any:
				if relation, ok := node["Relation Name"].(string); ok && (relation == "role_assignments" || relation == "role_permissions" || relation == "access_group_members") && node["Actual Loops"] != float64(0) {
					t.Fatalf("global permission performed per-row scoped joins: %s", raw)
				}
				for _, child := range node {
					inspect(child)
				}
			case []any:
				for _, child := range node {
					inspect(child)
				}
			}
		}
		inspect(plan[0])
		t.Logf("Global directory fast path skips scoped authority joins: %s", raw)
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var cursor string
	if _, err := authority.GrantRole(author, identity.GrantRoleInput{Subject: "history-only", ActorType: identity.ActorHuman, Role: identity.RoleServiceClient, Reason: "Qualify history without Resource reads"}, "history-member"); err != nil {
		t.Fatal(err)
	}
	historyActor := actorFor("history-only")
	historyRole, err := authority.SaveRoleDefinition(author, "", 0, identity.RoleDefinitionInput{Name: "History only", Permissions: []identity.Permission{"resource.read_history"}, Reason: "Qualify independent summary visibility"}, false, "history-role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GrantAuthorizationAssignment(author, identity.AccessAssignmentInput{RoleDefinitionID: historyRole.ID, MembershipID: historyActor.MembershipID, NamespaceID: visibleNamespace, Reason: "Read only release history"}, "history-grant"); err != nil {
		t.Fatal(err)
	}
	historyContext := identity.WithActor(t.Context(), historyActor)
	reviewCounts, err := service.ReviewOverview(historyContext)
	if err != nil || reviewCounts.Decisions.Value != 3 || reviewCounts.ApprovedUnreleased.Value != 0 {
		var earliest, latest *time.Time
		var decisions int
		evidenceErr := db.operator.QueryRow(t.Context(), "SELECT count(*),min(decided_at),max(decided_at) FROM review_submissions WHERE tenant_id=$1 AND decided_at IS NOT NULL", housekeepingTenantA).Scan(&decisions, &earliest, &latest)
		t.Logf("retained review times: decisions=%d earliest=%v latest=%v querySucceeded=%t", decisions, earliest, latest, evidenceErr == nil)
		diagnosticContext, _, contextErr := identity.WithCollectionContext(historyContext)
		if contextErr != nil {
			t.Fatal(contextErr)
		}
		bounded, readErr := repo.ReadReviewTimes(diagnosticContext, housekeepingTenantA, reviewCounts.StartAt, reviewCounts.EndAt.Add(time.Second))
		t.Logf("review observation: start=%s end=%s decisions through next second=%+v error=%v", reviewCounts.StartAt, reviewCounts.EndAt, bounded.Decisions, readErr)
		t.Fatal("history-only review summary", reviewCounts.Decisions, err)
	}
	historyCounts, err := service.ReleaseOverview(historyContext)
	if err != nil || historyCounts.Published.Value != 3 {
		t.Fatal(historyCounts, err)
	}
	resourceCounts, err := service.ResourceOverview(historyContext)
	if err != nil || resourceCounts.Total.Value != 0 {
		t.Fatal("history permission conferred Resource reads", resourceCounts, err)
	}
	for name, a := range map[string]identity.Actor{"direct-plus-group": directActor, "group-only": groupActor} {
		t.Run(name, func(t *testing.T) {
			ctx := identity.WithActor(t.Context(), a)
			inventory, err := service.ResourceOverview(ctx)
			if err != nil || inventory.Total != (catalog.BoundedCount{Value: 3}) || len(inventory.Kinds) != 4 {
				t.Fatal("overview ignored scoped selection or duplicated group paths", inventory, err)
			}
			reviewCounts, err := service.ReviewOverview(ctx)
			if err != nil || reviewCounts.Decisions.Value != 3 {
				t.Fatal("review summary multiplied group paths", reviewCounts.Decisions, err)
			}
			releaseCounts, err := service.ReleaseOverview(ctx)
			if err != nil || releaseCounts.Published != (catalog.BoundedCount{Value: 3}) || releaseCounts.Deprecated.Value != 0 {
				t.Fatal("overview current releases", releaseCounts, err)
			}
			directory, err := service.DirectoryOverview(ctx, catalog.InventoryNamespaces)
			if err != nil || directory.Count != (catalog.BoundedCount{Value: 1}) {
				t.Fatal("overview namespace selection", directory, err)
			}
			if _, err := service.DirectoryOverview(ctx, catalog.InventoryPublishers); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
				t.Fatal("scoped Resource grant disclosed tenant Publishers", err)
			}
			namespaces, err := service.ListNamespaces(ctx, catalog.NamespaceListRequest{Limit: 1})
			if err != nil || len(namespaces) != 1 || namespaces[0].ID != visibleNamespace {
				t.Fatal("namespace selection", namespaces, err)
			}
			resources, err := service.ListResources(ctx, catalog.ResourceListRequest{Limit: 2})
			if err != nil || len(resources) != 3 {
				t.Fatal("resource sentinel", resources, err)
			}
			for _, r := range resources {
				if r.NamespaceID != visibleNamespace {
					t.Fatal("hidden resource", r)
				}
			}
			first, err := service.Search(ctx, catalog.SearchQuery{Limit: 2})
			if err != nil || first.Total != 3 || len(first.Items) != 2 || first.Items[0].ListingRevision.DisplayName != "B visible" || first.NextCursor == "" {
				t.Fatal("first page", first, err)
			}
			last, err := service.Search(ctx, catalog.SearchQuery{Limit: 2, Cursor: first.NextCursor})
			if err != nil || last.Total != 3 || len(last.Items) != 1 || last.Items[0].ListingRevision.DisplayName != "D visible" || last.NextCursor != "" {
				t.Fatal("terminal page", last, err)
			}
			if name == "group-only" {
				cursor = first.NextCursor
			}
			facets, err := service.Explore(ctx, catalog.ExploreRequest{Facets: []catalog.FacetRequest{{Field: "tag"}, {Field: "namespace"}}})
			if err != nil || facets.Matched != 3 || !reflect.DeepEqual(facets.Facets["tag"].Buckets, []catalog.FacetBucket{{Value: "visible", Count: 3}}) {
				t.Fatal("facet population", facets, err)
			}
			hidden, err := service.Search(ctx, catalog.SearchQuery{Tags: []string{"hidden-only"}})
			if err != nil || hidden.Total != 0 || len(hidden.Items) != 0 {
				t.Fatal("hidden exact filter", hidden, err)
			}
			reviews, err := service.ListSubmissions(ctx, catalog.SubmissionListRequest{HistoryPageRequest: catalog.HistoryPageRequest{Limit: 2}})
			if err != nil || len(reviews) != 3 {
				t.Fatal("review selection", reviews, err)
			}
			for _, r := range reviews {
				if r.ListingID == hiddenListing.ID {
					t.Fatal("hidden review", r)
				}
			}
			if _, err := service.GetCatalogRecord(ctx, hiddenRelease.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
				t.Fatal("hidden detail", err)
			}
		})
	}

	grouped := identity.WithActor(t.Context(), groupActor)
	forged := groupActor
	forged.TenantID = housekeepingTenantB
	if _, err := service.Search(identity.WithActor(t.Context(), forged), catalog.SearchQuery{Limit: 2}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("cross-tenant membership accepted", err)
	}
	versions, err := service.ListVersions(grouped, baseline.Items[0].Resource.ID, catalog.HistoryPageRequest{Limit: 1})
	if err != nil || len(versions) != 1 {
		t.Fatal("scoped version history", versions, err)
	}
	if _, err := service.ListVersions(grouped, hiddenResource.ID, catalog.HistoryPageRequest{Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("hidden parent history", err)
	}
	if _, _, _, err := service.GetVersionRevision(grouped, baseline.Items[0].VersionRevision.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("metadata role granted content", err)
	}

	// Federated records have tenant discovery authority, not a local namespace.
	// A hidden local collision must not remove an external identity or taint its
	// conflict/provenance; a visible current local release retains precedence.
	remote := []catalog.SearchDocument{
		{Identifier: hiddenResource.CanonicalIdentifier, DisplayName: "External hidden", Tags: []string{"remote"}},
		{Identifier: documents[0].Identifier, DisplayName: "External visible", Tags: []string{"remote"}},
		{Identifier: "urn:air:remote.example:finance:remote", DisplayName: "External remote", Tags: []string{"remote"}},
	}
	seedLexicalSourceEndpoint(t, db, repo, remote, false, "https://scoped-one.example/.well-known/ard.json", "1.0.0")
	manager := seedLexicalSourceEndpoint(t, db, repo, []catalog.SearchDocument{{Identifier: hiddenResource.CanonicalIdentifier, DisplayName: "External conflicting metadata", Tags: []string{"remote"}}}, false, "https://scoped-two.example/.well-known/ard.json", "1.0.0")
	// The Source Administrator has no publication or reindex read authority.
	// Newer invalid targets and other families must not shorten its first page.
	var sourceID string
	var sourceRevision int64
	if err := db.operator.QueryRow(t.Context(), `SELECT id,revision FROM federation_sources WHERE tenant_id=$1 ORDER BY id LIMIT 1`, housekeepingTenantA).Scan(&sourceID, &sourceRevision); err != nil {
		t.Fatal(err)
	}
	baseTime := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	for i, input := range []struct{ kind, payload string }{
		{"source.sync", fmt.Sprintf(`{"sourceId":%q,"sourceRevision":%d,"trigger":"manual"}`, sourceID, sourceRevision)},
		{"source.sync", fmt.Sprintf(`{"sourceId":%q,"sourceRevision":%d,"trigger":"manual"}`, sourceID, sourceRevision)},
		{"source.sync", `{"sourceId":"00000000-0000-7000-8000-000000000099","sourceRevision":1,"trigger":"manual"}`},
		{"publication.generate", `{"host":"catalog.example.com"}`},
		{"semantic.reindex", `{"profile":"fixture","scope":"control"}`},
	} {
		jobID, err := (id.Generator{}).New(baseTime.Add(time.Duration(i) * time.Second))
		if err != nil {
			t.Fatal(err)
		}
		job := housekeepingJob(jobID, "", baseTime.Add(time.Duration(i)*time.Second))
		job.Kind, job.Payload = input.kind, json.RawMessage(input.payload)
		if input.kind == "source.sync" {
			job.PayloadVersion = 2
		}
		// Retained jobs can outlive their source. Insert schema-valid evidence
		// directly so the missing-target case does not bypass enqueue fencing.
		if _, err := db.operator.Exec(t.Context(), `INSERT INTO jobs(id,tenant_id,kind,actor,payload_version,payload,state,not_before,attempt_limit,deduplication_key,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, job.ID, job.TenantID, job.Kind, job.Actor, job.PayloadVersion, job.Payload, job.State, job.NotBefore, job.AttemptLimit, job.DeduplicationKey, job.CreatedAt, job.UpdatedAt); err != nil {
			t.Fatal(err)
		}
	}
	jobStore, err := NewPostgresJobStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	sourceActor, err := identityStore.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: "lexical-source"})
	if err != nil {
		t.Fatal(err)
	}
	jobCtx, _, err := identity.WithCollectionContext(identity.WithActor(t.Context(), sourceActor))
	if err != nil {
		t.Fatal(err)
	}
	jobPage, err := jobStore.ListViews(jobCtx, housekeepingTenantA, jobs.ListRequest{Limit: 2, Targets: jobs.ListTargets{Enforced: true, PublicationHosts: []string{"catalog.example.com"}, SemanticProfile: "fixture"}})
	if err != nil || len(jobPage) != 2 {
		t.Fatal("family/target filtering shortened Job page", jobPage, err)
	}
	for _, job := range jobPage {
		if job.Kind != "source.sync" || job.CreatedAt.Before(baseTime) {
			t.Fatal("hidden family/target selected", job)
		}
	}
	federationRole, err := authority.SaveRoleDefinition(author, "", 0, identity.RoleDefinitionInput{Name: "Tenant federation discovery", Permissions: []identity.Permission{"federation.discover"}, Reason: "Authorize external population independently"}, false, "federation-role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GrantAuthorizationAssignment(author, identity.AccessAssignmentInput{RoleDefinitionID: federationRole.ID, MembershipID: groupActor.MembershipID, Reason: "Allow tenant federation discovery"}, "federation-grant"); err != nil {
		t.Fatal(err)
	}
	unified, err := catalog.NewSearchService(service, manager)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit indexed-only selection includes the external counterpart of a
	// visible local identity. With local origin selected, local still wins below.
	indexedCursor := ""
	indexedSeen := map[string]bool{}
	for pageNumber := range 3 {
		page, err := unified.Search(grouped, catalog.SearchQuery{Origins: []catalog.SearchResultType{catalog.SearchResultIndexed}, Limit: 1, Cursor: indexedCursor})
		if err != nil || page.DurableTotal != 3 || len(page.Items) != 1 || (page.NextCursor != "") != (pageNumber < 2) {
			t.Fatal("indexed authorized cross-page population", pageNumber, page, err)
		}
		if indexedSeen[page.Items[0].Identifier] {
			t.Fatal("duplicate indexed identity across pages", page.Items[0].Identifier)
		}
		indexedSeen[page.Items[0].Identifier] = true
		indexedCursor = page.NextCursor
	}
	// Provenance counts contributions matching the selected normalized digest;
	// the competing digest is represented separately by Conflict.
	bookmark := catalog.IndexedIdentity{Identifier: hiddenResource.CanonicalIdentifier, Version: "1.0.0", VersionPresent: true}
	external, err := unified.GetIndexed(grouped, bookmark)
	if err != nil || external.Indexed == nil || external.Indexed.SourceCount != 1 || len(external.Indexed.SourceIDs) != 1 || !external.Indexed.Conflict || external.Indexed.SameSourceConflict || external.Local != nil {
		t.Fatal("hidden local polluted external evidence", external, err)
	}
	if visible, err := unified.GetIndexed(author, bookmark); err != nil || visible.Type != catalog.SearchResultIndexed || visible.Indexed == nil {
		t.Fatal("explicit indexed bookmark with visible local counterpart", visible, err)
	}
	visibleCombined, err := unified.Search(author, catalog.SearchQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	foundLocal := false
	for _, item := range visibleCombined.Items {
		if item.Identifier == bookmark.Identifier && item.Version == bookmark.Version {
			foundLocal = true
			if item.Type != catalog.SearchResultLocal || !item.VersionPresent || item.Local == nil {
				t.Fatal("combined Search lost visible local precedence", item)
			}
		}
	}
	if !foundLocal {
		t.Fatal("combined Search omitted the visible local winner")
	}
	combined, err := unified.Search(grouped, catalog.SearchQuery{Limit: 10})
	if err != nil || combined.DurableTotal != 5 || len(combined.Items) != 5 {
		t.Fatal("local/indexed union totals", combined, err)
	}
	// Refresh the local cursor after the unrelated federation grant; this next
	// rejection is tied specifically to the group suspension below.
	refreshed, err := service.Search(grouped, catalog.SearchQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	cursor = refreshed.NextCursor
	// Selection must reject a policy change between preparatory binding and SQL,
	// independently of HTTP authentication resolving another fresh Actor.
	stale, _, err := identity.WithCollectionContext(grouped)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.SaveAccessGroup(author, group.ID, group.Revision, identity.GroupInput{Reason: "Revoke namespace pages"}, identity.GroupSuspended, "suspend"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SearchCatalog(stale, housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 2}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("SQL accepted stale policy snapshot", err)
	}
	if _, err := service.Search(grouped, catalog.SearchQuery{Limit: 2, Cursor: cursor}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("revoked continuation", err)
	}
	empty, err := service.Search(grouped, catalog.SearchQuery{Limit: 2})
	if err != nil || empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatal("revoked first page", empty, err)
	}
	// The separate direct path remains effective after group suspension.
	direct, err := service.Search(identity.WithActor(t.Context(), directActor), catalog.SearchQuery{Limit: 2})
	if err != nil || direct.Total != 3 {
		t.Fatal("group suspension removed independent direct access", direct, err)
	}
	if _, err := authority.RevokeRole(author, directGrant.ID, "Finish fixture policy withdrawal", "revoke-direct", directGrant.Revision); err != nil {
		t.Fatal(err)
	}
}
