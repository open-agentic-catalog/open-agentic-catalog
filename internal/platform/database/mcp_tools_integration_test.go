//go:build integration

package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

type mcpToolFixture struct {
	db                         *housekeepingDatabase
	repo                       *PostgresRepository
	service                    *catalog.Service
	authorActor, reviewerActor identity.Actor
	namespace, publisher       string
	card                       json.RawMessage
}

func newMCPToolFixture(t *testing.T) *mcpToolFixture {
	t.Helper()
	db, repo, _, _ := newSearchCostFixture(t)
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "mcp-tools-test"})
	if err != nil {
		t.Fatal(err)
	}
	author, _ := identity.FromContext(semanticDisclosureActor(t, db, housekeepingTenantA))
	f := &mcpToolFixture{db: db, repo: repo, service: service, authorActor: author}
	store, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: "search-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	f.reviewerActor = reviewer
	if err := db.operator.QueryRow(t.Context(), "SELECT id::text FROM namespaces WHERE tenant_id=$1", housekeepingTenantA).Scan(&f.namespace); err != nil {
		t.Fatal(err)
	}
	if err := db.operator.QueryRow(t.Context(), "SELECT id::text FROM publishers WHERE tenant_id=$1", housekeepingTenantA).Scan(&f.publisher); err != nil {
		t.Fatal(err)
	}
	f.card, err = os.ReadFile("../../../samples/cards/documentation-mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *mcpToolFixture) author(t *testing.T) context.Context {
	t.Helper()
	return identity.WithActor(t.Context(), f.authorActor)
}
func (f *mcpToolFixture) reviewer(t *testing.T) context.Context {
	t.Helper()
	return identity.WithActor(t.Context(), f.reviewerActor)
}

func (f *mcpToolFixture) representations(t *testing.T, version, declaration string, blob bool) []catalog.RepresentationInput {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(f.card, &doc); err != nil {
		t.Fatal(err)
	}
	doc["version"] = version
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaMCPServerCard, Data: raw}}
	if declaration != "" {
		rep := catalog.RepresentationInput{Role: catalog.RepresentationMetadata, MediaType: catalog.MCPToolsMediaType}
		if blob {
			rep.Content = declaration
		} else {
			rep.Data = json.RawMessage(declaration)
		}
		inputs = append(inputs, rep)
	}
	return inputs
}
func (f *mcpToolFixture) publish(t *testing.T, listing catalog.CatalogListing, revision catalog.VersionRevision, name string, relationships ...catalog.Relationship) catalog.ListingRelease {
	t.Helper()
	listing, _, err := f.service.CreateListingRevision(f.author(t), listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: revision.ID, PublisherID: f.publisher, DisplayName: name, Description: "Processes documents", Visibility: catalog.VisibilityTenant, Relationships: relationships, Tags: []string{"documents"}}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := f.service.Submit(f.author(t), listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.DecideSubmission(f.reviewer(t), sub.ID, "approve", "Review exact tool declaration", sub.Revision); err != nil {
		t.Fatal(err)
	}
	_, release, _, err := f.service.Publish(f.author(t), listing.ID, catalog.PublishInput{SubmissionID: sub.ID}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return release
}
func (f *mcpToolFixture) seed(t *testing.T, n int, name, declaration string, blob bool, relationships ...catalog.Relationship) catalog.CatalogRecord {
	t.Helper()
	r, err := f.service.CreateResource(f.author(t), catalog.CreateResourceInput{NamespaceID: f.namespace, Kind: catalog.KindMCPServer, CanonicalIdentifier: fmt.Sprintf("urn:air:catalog.example.com:performance:mcp-%d", n)})
	if err != nil {
		t.Fatal(err)
	}
	_, rev, ev, err := f.service.CreateVersion(f.author(t), r.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: f.representations(t, "1.0.0", declaration, blob)})
	if err != nil || ev.State != "complete" {
		t.Fatal("revision", ev, err)
	}
	listing, err := f.service.CreateListing(f.author(t), catalog.CreateListingInput{NamespaceID: f.namespace, ResourceID: r.ID})
	if err != nil {
		t.Fatal(err)
	}
	release := f.publish(t, listing, rev, name, relationships...)
	record, err := f.service.GetCatalogRecord(f.author(t), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestPostgresMCPToolSearchLifecycleAndAuthority(t *testing.T) {
	f := newMCPToolFixture(t)
	declaration := `{"tools":[{"name":"extract_pdf_tables","description":"PDF tables"},{"name":"read_pdf_tables"},{"name":"pdf_tables_archive"},{"name":"unrelated","description":"PDF tables"}]}`
	first := f.seed(t, 0, "Document Utilities", declaration, false)
	f.seed(t, 1, "Other Utilities", declaration, true)
	f.seed(t, 2, "PDF tables listing", "", false)
	absent := f.seed(t, 3, "No declaration", "", false)
	f.seed(t, 4, "Explicitly empty", `{"tools":[]}`, false)
	f.seed(t, 5, "Split terms", `{"tools":[{"name":"pdf"},{"name":"tables"}]}`, false)
	q := catalog.SearchQuery{Text: "pdf tables", IncludeMCPTools: true, Origins: []catalog.SearchResultType{catalog.SearchResultLocal}, Limit: 1}
	store, err := NewPostgresFederationStore(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	search, err := catalog.NewSearchService(f.service, &searchCostInterleave{searcher: postgresIndexedCatalogSearcher{store: store}, before: func() { t.Fatal("private tool search reached federation") }})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := range 3 {
		page, err := search.Search(f.author(t), q)
		if err != nil || page.DurableTotal != 3 || len(page.Items) != 1 || page.Tier != "core" || page.Profile != catalog.MCPToolsSearchProfile || page.LiveCount != 0 {
			t.Fatal("selected population", page, err)
		}
		item := page.Items[0]
		if seen[item.ID] {
			t.Fatal("duplicate owning server")
		}
		seen[item.ID] = true
		if item.ToolMatch != nil && (item.ToolMatch.MatchedToolCount != 4 || !item.ToolMatch.Truncated || len(item.ToolMatch.Tools) != 3 || item.ToolMatch.RepresentationID == "" || item.Local.VersionRevision.ID == "") {
			t.Fatal("unbound tool detail", item)
		}
		if i < 2 && page.NextCursor == "" || i == 2 && page.NextCursor != "" {
			t.Fatal("pagination bound")
		}
		if i == 0 {
			changed := q
			changed.IncludeMCPTools = false
			changed.Cursor = page.NextCursor
			if _, err := search.Search(f.author(t), changed); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatal("cursor changed private mode", err)
			}
		}
		q.Cursor = page.NextCursor
	}
	ordered := q
	ordered.Cursor = ""
	ordered.Order = catalog.SearchOrderNameDesc
	for i, name := range []string{"PDF tables listing", "Other Utilities", "Document Utilities"} {
		part, err := search.Search(f.author(t), ordered)
		if err != nil || part.DurableTotal != 3 || len(part.Items) != 1 || part.Items[0].DisplayName != name || (i == 2) != (part.NextCursor == "") {
			t.Fatal("ordered tool continuation", part, err)
		}
		ordered.Cursor = part.NextCursor
	}
	filtered := q
	filtered.Cursor = ""
	filtered.Tags = []string{"documents"}
	matchedFilter, err := search.Search(f.author(t), filtered)
	if err != nil || matchedFilter.DurableTotal != 3 {
		t.Fatal("ordinary tag filter changed", matchedFilter, err)
	}
	filtered.Tags = []string{"absent-tag"}
	excluded, err := search.Search(f.author(t), filtered)
	if err != nil || excluded.DurableTotal != 0 {
		t.Fatal("tool matches bypassed tag filter", excluded, err)
	}

	q.Cursor = ""
	ordinary := q
	ordinary.IncludeMCPTools = false
	out, err := search.Search(f.author(t), ordinary)
	if err != nil || out.DurableTotal != 1 || out.Items[0].ToolMatch != nil {
		t.Fatal("ordinary search changed", out, err)
	}
	exact := q
	exact.Text = first.Resource.CanonicalIdentifier
	out, err = search.Search(f.author(t), exact)
	if err != nil || len(out.Items) != 1 || out.Items[0].Score != 100 {
		t.Fatal("exact identifier precedence", out, err)
	}
	facets, err := f.service.Explore(f.author(t), catalog.ExploreRequest{Query: catalog.SearchQuery{IncludeMCPTools: true, Text: q.Text}, Facets: []catalog.FacetRequest{{Field: "kind"}, {Field: "tag"}}})
	if err != nil || facets.Matched != 3 || facets.Profile != catalog.MCPToolsSearchProfile || len(facets.Facets["kind"].Buckets) != 1 || facets.Facets["kind"].Buckets[0].Count != 3 {
		t.Fatal("facet population", facets, err)
	}
	foreign, err := f.repo.SearchCatalog(t.Context(), housekeepingTenantB, catalog.CatalogSearchRepositoryRequest{Query: q, Limit: 10})
	if err != nil || foreign.Total != 0 {
		t.Fatal("tenant leak", foreign, err)
	}
	// Metadata-only authority keeps ordinary Listing matches but cannot infer inventories.
	key := sha256.Sum256([]byte(t.Name() + "authority"))
	identities, err := NewPostgresIdentityStore(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: identities, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GrantRole(f.author(t), identity.GrantRoleInput{Subject: "tools-metadata", ActorType: identity.ActorHuman, Role: identity.RoleServiceClient, Reason: "Own metadata-only fixture"}, "member"); err != nil {
		t.Fatal(err)
	}
	actor, err := identities.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: "tools-metadata"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := authority.SaveRoleDefinition(f.author(t), "", 0, identity.RoleDefinitionInput{Name: "Tools metadata", Permissions: []identity.Permission{"resource.read"}, Reason: "Separate Listing and content authority"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GrantAuthorizationAssignment(f.author(t), identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: actor.MembershipID, Reason: "Allow metadata only"}, "grant"); err != nil {
		t.Fatal(err)
	}
	reader := identity.WithActor(t.Context(), actor)
	out, err = search.Search(reader, q)
	if err != nil || out.DurableTotal != 1 || len(out.Items) != 1 || out.Items[0].DisplayName != "PDF tables listing" || out.Items[0].ToolMatch != nil {
		t.Fatal("private influence without content authority", out, err)
	}
	metadataFacets, err := f.service.Explore(reader, catalog.ExploreRequest{Query: catalog.SearchQuery{IncludeMCPTools: true, Text: q.Text}, Facets: []catalog.FacetRequest{{Field: "kind"}, {Field: "tag"}}})
	if err != nil || metadataFacets.Matched != 1 || len(metadataFacets.Facets["kind"].Buckets) != 1 || metadataFacets.Facets["kind"].Buckets[0].Count != 1 || len(metadataFacets.Facets["tag"].Buckets) != 1 || metadataFacets.Facets["tag"].Buckets[0].Count != 1 {
		t.Fatal("private tool matches influenced metadata-only facet counts", metadataFacets, err)
	}
	for _, item := range []catalog.CatalogRecord{first, absent} {
		record, err := f.service.GetCatalogRecord(reader, item.Release.ID)
		if err != nil || !record.ToolDeclarationRedacted || len(record.VersionRevision.Representations) != 1 {
			t.Fatal("exact record leaked private declaration", record, err)
		}
		if record.VersionRevision.Representations[0].Role != catalog.RepresentationPrimary {
			t.Fatal("public primary was changed by private redaction")
		}
	}

	if _, _, _, err := f.service.GetVersionRevision(reader, first.VersionRevision.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("exact declaration disclosure", err)
	}
	// Any-scope navigation permissions cannot authorize another Namespace's inventory.
	hiddenNamespace, err := f.service.CreateNamespace(f.author(t), catalog.CreateNamespaceInput{Slug: "hidden-tools", DisplayName: "Hidden tools"})
	if err != nil {
		t.Fatal(err)
	}
	originalNamespace := f.namespace
	f.namespace = hiddenNamespace.ID
	hidden := f.seed(t, 9, "Hidden utilities", `{"tools":[{"name":"classified_unique"}]}`, false)
	f.namespace = originalNamespace
	scopedRole, err := authority.SaveRoleDefinition(f.author(t), "", 0, identity.RoleDefinitionInput{Name: "Scoped tool content", Permissions: []identity.Permission{"resource.read_content"}, Reason: "Exact namespace disclosure"}, false, "scoped-role")
	if err != nil {
		t.Fatal(err)
	}
	scopedGrant, err := authority.GrantAuthorizationAssignment(f.author(t), identity.AccessAssignmentInput{RoleDefinitionID: scopedRole.ID, MembershipID: actor.MembershipID, NamespaceID: originalNamespace, Reason: "Read tools only in first namespace"}, "scoped-grant")
	if err != nil {
		t.Fatal(err)
	}
	allowedInScope, err := search.Search(reader, q)
	if err != nil || allowedInScope.DurableTotal != 3 {
		t.Fatal("scoped content positive control", allowedInScope, err)
	}
	hiddenQuery := q
	hiddenQuery.Text = "classified unique"
	positive, err := search.Search(f.author(t), hiddenQuery)
	if err != nil || positive.DurableTotal != 1 {
		t.Fatal("hidden tool fixture did not match", positive, err)
	}
	denied, err := search.Search(reader, hiddenQuery)
	if err != nil || denied.DurableTotal != 0 || len(denied.Items) != 0 {
		t.Fatal("another namespace's tool text affected selection", denied, err)
	}
	hiddenFacets, err := f.service.Explore(reader, catalog.ExploreRequest{Query: catalog.SearchQuery{IncludeMCPTools: true, Text: hiddenQuery.Text}, Facets: []catalog.FacetRequest{{Field: "kind"}, {Field: "tag"}}})
	if err != nil || hiddenFacets.Matched != 0 || len(hiddenFacets.Facets["kind"].Buckets) != 0 || len(hiddenFacets.Facets["tag"].Buckets) != 0 {
		t.Fatal("another namespace's private tool text influenced facets", hiddenFacets, err)
	}
	hiddenRecord, err := f.service.GetCatalogRecord(reader, hidden.Release.ID)
	if err != nil || !hiddenRecord.ToolDeclarationRedacted || len(hiddenRecord.VersionRevision.Representations) != 1 {
		t.Fatal("another namespace's exact record disclosed tools", hiddenRecord, err)
	}
	if _, err := authority.RevokeAuthorizationAssignment(f.author(t), scopedGrant.ID, scopedGrant.Revision, "Finish scoped disclosure control", "revoke-scoped"); err != nil {
		t.Fatal(err)
	}

	grant, err := authority.GrantRole(f.author(t), identity.GrantRoleInput{Subject: "tools-metadata", ActorType: identity.ActorHuman, Role: identity.RoleReader, Reason: "Temporary content reader"}, "content-grant")
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := identities.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: "tools-metadata"})
	if err != nil {
		t.Fatal(err)
	}
	contentReader := identity.WithActor(t.Context(), authorized)
	allowed, err := search.Search(contentReader, q)
	if err != nil || allowed.DurableTotal != 3 || allowed.NextCursor == "" {
		t.Fatal("content grant positive control", allowed, err)
	}
	if _, err := authority.RevokeRole(f.author(t), grant.ID, "Revoke content before continuation", "revoke-content", grant.Revision); err != nil {
		t.Fatal(err)
	}
	continuation := q
	continuation.Cursor = allowed.NextCursor
	if _, err := search.Search(contentReader, continuation); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("revocation resumed private cursor", err)
	}
	out, err = search.Search(contentReader, q)
	if err != nil || out.DurableTotal != 1 || out.Items[0].ToolMatch != nil {
		t.Fatal("revoked content still influenced fresh search", out, err)
	}

	// An unpublished edit has no influence, then follows independent review into release.
	_, draft, _, err := f.service.CreateVersion(f.author(t), first.Resource.ID, catalog.CreateVersionInput{Version: "2.0.0", Representations: f.representations(t, "2.0.0", `{"tools":[{"name":"render_slides"}]}`, true)})
	if err != nil {
		t.Fatal(err)
	}
	out, err = search.Search(f.author(t), q)
	if err != nil || out.DurableTotal != 3 {
		t.Fatal("draft changed current tools", out, err)
	}
	newer := q
	newer.Text = "render slides"
	out, err = search.Search(f.author(t), newer)
	if err != nil || out.DurableTotal != 0 {
		t.Fatal("draft leaked", out, err)
	}
	deprecated, err := f.service.TransitionRelease(f.author(t), first.Release.ID, catalog.ReleaseDeprecated, "Lifecycle control", "", nil, first.Release.Revision)
	if err != nil {
		t.Fatal(err)
	}
	out, err = search.Search(f.author(t), q)
	if err != nil || out.DurableTotal != 3 {
		t.Fatal("deprecation removed tools", out, err)
	}
	if _, err := f.service.TransitionRelease(f.author(t), deprecated.ID, catalog.ReleaseRevoked, "Withdraw fixture", "", nil, deprecated.Revision); err != nil {
		t.Fatal(err)
	}
	out, err = search.Search(f.author(t), q)
	if err != nil || out.DurableTotal != 2 {
		t.Fatal("withdrawn tools still match", out, err)
	}
	listing, err := f.service.GetListing(f.author(t), first.Listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	listing, _, err = f.service.CreateListingRevision(f.author(t), listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: draft.ID, PublisherID: f.publisher, DisplayName: "Document Utilities", Description: "Processes documents", Visibility: catalog.VisibilityTenant}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	// Reusing an old approval may republish only that exact approved revision,
	// never the changed candidate declaration. This is an existing supported workflow.
	listing, approvedAgain, _, err := f.service.Publish(f.author(t), listing.ID, catalog.PublishInput{SubmissionID: first.Release.SubmissionID}, listing.Revision)
	if err != nil || approvedAgain.VersionRevisionID != first.VersionRevision.ID || approvedAgain.VersionRevisionID == draft.ID {
		t.Fatal("earlier approval authorized changed tools", approvedAgain, err)
	}
	out, err = search.Search(f.author(t), newer)
	if err != nil || out.DurableTotal != 0 {
		t.Fatal("unreviewed replacement tools became discoverable", out, err)
	}
	replacement := f.publish(t, listing, draft, "Document Utilities")
	out, err = search.Search(f.author(t), newer)
	if err != nil || out.DurableTotal != 1 || out.Items[0].Local.Release.ID != replacement.ID || out.Items[0].Local.VersionRevision.ID != draft.ID {
		t.Fatal("replacement revision binding", out, err)
	}
	retained, _, _, err := f.service.GetVersionRevision(f.author(t), first.VersionRevision.ID)
	if err != nil || !bytes.Equal(retained.Representations[1].Data, first.VersionRevision.Representations[1].Data) {
		t.Fatal("history rewritten", err)
	}
}

func TestPostgresMCPToolProjectionAndBoundedPlan(t *testing.T) {
	f := newMCPToolFixture(t)
	tools := make([]catalog.MCPTool, catalog.MaxMCPTools)
	for i := range tools {
		tools[i] = catalog.MCPTool{Name: fmt.Sprintf("pdf_tables_%02d", i), Description: strings.Repeat("λ", 1024)}
	}
	// Keep maximum tool count inside the independent aggregate byte budget.
	for i := range tools {
		tools[i].Description = strings.Repeat("x", 900)
	}
	raw, err := json.Marshal(catalog.MCPToolDeclaration{Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 101 {
		f.seed(t, i, fmt.Sprintf("Bounded %02d", i), string(raw), i%2 == 0)
	}
	q := catalog.SearchQuery{Text: "pdf tables", IncludeMCPTools: true, Origins: []catalog.SearchResultType{catalog.SearchResultLocal}, Limit: 2}
	page, err := f.repo.SearchCatalog(f.author(t), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: q, Limit: 2})
	if err != nil || page.Total != 101 || len(page.Items) != 2 || page.Items[0].ToolMatch.MatchedToolCount != 64 || page.Items[0].RelevanceScore != 60 || page.Items[0].ToolMatch.Score != 60 {
		t.Fatal("bounded inventory", page, err)
	}
	planContext, _, err := identity.WithCollectionContext(f.author(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = withSearchDiagnosticTx(planContext, f.db.api, func(tx pgx.Tx) (bool, error) {
		if err := initializeCollectionPolicy(planContext, tx, housekeepingTenantA); err != nil {
			return false, err
		}
		_, statement := catalogSearchStatements(q)
		args := append(catalogSearchParameters(housekeepingTenantA, q), false, 0, "", "", 2)
		var raw []byte
		if err := tx.QueryRow(planContext, "EXPLAIN (ANALYZE,BUFFERS,TIMING OFF,FORMAT JSON) "+statement, append([]any{pgx.QueryExecModeExec}, args...)...).Scan(&raw); err != nil {
			return false, err
		}
		var plans []struct {
			Plan searchPlanNode `json:"Plan"`
		}
		if err := json.Unmarshal(raw, &plans); err != nil {
			return false, err
		}
		if len(plans) != 1 || plans[0].Plan.Rows != 2 {
			t.Fatal("page bound absent")
		}
		var walk func(searchPlanNode)
		walk = func(n searchPlanNode) {
			if n.Temp != 0 {
				t.Fatal("plan spilled")
			}
			for _, c := range n.Plans {
				walk(c)
			}
		}
		walk(plans[0].Plan)
		t.Logf("bounded tool page plan: %s", raw)
		return true, nil
	})
	if err != nil {
		t.Fatal("tool selection plan", err)
	}
	// Independent expected field weights qualify the Go matcher used by the
	// memory repository against actual SQL, including cross-field/tool terms.
	for _, tc := range []struct {
		tools []catalog.MCPTool
		query string
		score int
	}{
		{[]catalog.MCPTool{{Name: "pdf_tables", Description: "PDF tables"}}, "pdf tables", 70},
		{[]catalog.MCPTool{{Name: "pdf_tables", Description: "other"}}, "pdf tables", 60},
		{[]catalog.MCPTool{{Name: "other", Description: "PDF tables"}}, "pdf tables", 50},
		{[]catalog.MCPTool{{Name: "pdf", Description: "Tables"}}, "pdf tables", 40},
		{[]catalog.MCPTool{{Name: "pdf"}, {Name: "tables"}}, "pdf tables", 0},
		{[]catalog.MCPTool{{Name: "résumé_λ"}}, "RÉSUMÉ λ", 60},
	} {
		raw, err := json.Marshal(tc.tools)
		if err != nil {
			t.Fatal(err)
		}
		var actual int
		if err := f.db.api.QueryRow(t.Context(), `SELECT coalesce(max(`+mcpToolScoreSQL+`),0)::integer FROM jsonb_array_elements($1::jsonb) tool`, string(raw), tc.query).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		matched := catalog.MatchMCPTools(tc.tools, tc.query, "representation", "digest")
		memoryScore := 0
		if matched != nil {
			memoryScore = matched.Score
		}
		if actual != tc.score || memoryScore != tc.score {
			t.Fatal("SQL/memory tool score parity", tc.query, actual, memoryScore, tc.score)
		}
	}

	for _, table := range []string{"representations", "document_blobs"} {
		var granted bool
		if err := f.db.worker.QueryRow(t.Context(), "SELECT has_column_privilege(current_user,$1,'mcp_tools','SELECT')", table).Scan(&granted); err != nil || granted {
			t.Fatal("worker gained private projection", table, err)
		}
	}
	tx, err := f.db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(t.Context(), tx)()
	var schema string
	if err := tx.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := verifyRestoreConstraints(t.Context(), tx, schema); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresMCPToolProjectionUpgradeAndRebuild(t *testing.T) {
	db := newHousekeepingDatabase(t)
	tx, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(t.Context(), tx)()
	schema := fmt.Sprintf("catalog_tools_upgrade_%d", time.Now().UnixNano())
	var crypto string
	if err := tx.QueryRow(t.Context(), `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pgcrypto'`).Scan(&crypto); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()+`; SET LOCAL search_path TO `+pgx.Identifier{schema}.Sanitize()+`,`+pgx.Identifier{crypto}.Sanitize()+`,public`); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "000049" {
			continue
		}
		raw, err := fs.ReadFile(migrations.Files, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), string(raw)); err != nil {
			t.Fatal(entry.Name(), err)
		}
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,display_name,created_at) VALUES ($1,'Tool upgrade',now()),($2,'Logical restore control',now())`, housekeepingTenantA, housekeepingTenantB); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{ "tools": [{"name":"extract_pdf_tables","description":"Private declaration"}] }`)
	sum := sha256.Sum256(raw)
	blob := hex.EncodeToString(sum[:])
	if _, err := tx.Exec(t.Context(), `INSERT INTO document_blobs(tenant_id,id,media_type,content_digest,size_bytes,content,created_at) VALUES ($1,$2,$3,$4,$5,$6,now())`, housekeepingTenantA, blob, catalog.MCPToolsMediaType, "sha256:"+blob, len(raw), raw); err != nil {
		t.Fatal(err)
	}
	migration, err := fs.ReadFile(migrations.Files, "000049_mcp_tool_declarations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), string(migration)); err != nil {
		t.Fatal(err)
	}
	// A logical restore excludes generated columns; insertion recomputes from exact bytes.
	if _, err := tx.Exec(t.Context(), `INSERT INTO document_blobs(tenant_id,id,media_type,content_digest,size_bytes,content,created_at) SELECT $1,id,media_type,content_digest,size_bytes,content,created_at FROM document_blobs WHERE tenant_id=$2; REINDEX TABLE document_blobs`, pgx.QueryExecModeSimpleProtocol, housekeepingTenantB, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{housekeepingTenantA, housekeepingTenantB} {
		var content, derived []byte
		var digest string
		var matched bool
		if err := tx.QueryRow(t.Context(), `SELECT content,content_digest,mcp_tools,catalog_mcp_tools_vector_v1(mcp_tools) @@ catalog_core_lexical_query_v1('pdf tables') FROM document_blobs WHERE tenant_id=$1 AND id=$2`, tenant, blob).Scan(&content, &digest, &derived, &matched); err != nil {
			t.Fatal(err)
		}
		var tools []catalog.MCPTool
		if json.Unmarshal(derived, &tools) != nil || len(tools) != 1 || tools[0].Name != "extract_pdf_tables" || !bytes.Equal(content, raw) || digest != "sha256:"+blob || !matched {
			t.Fatal("rebuild changed evidence or lost tools", digest, matched)
		}
	}
}

func TestPostgresMCPToolsSkipHybridProvider(t *testing.T) {
	f := newMCPToolFixture(t)
	f.seed(t, 0, "Document utilities", `{"tools":[{"name":"extract_pdf_tables","description":"private sentinel"}]}`, true)
	store := provisionSemanticFixture(t, f.db)
	g := catalog.SemanticGeneration{ID: "00000000-0000-7000-8000-000000000952", TenantID: housekeepingTenantA, Profile: "sha256:" + strings.Repeat("a", 64), Scope: "control", Dimensions: 3}
	if err := f.db.operator.QueryRow(t.Context(), "SELECT generation::text FROM catalog_search_generations WHERE tenant_id=$1", g.TenantID).Scan(&g.SourceGeneration); err != nil {
		t.Fatal(err)
	}
	job := housekeepingJob(g.ID, "", time.Now().UTC().Add(-time.Second))
	job.Kind, job.Priority, job.PayloadVersion = "semantic.reindex", 100, 1
	var err error
	job.Payload, err = json.Marshal(catalog.SemanticReindexInput{Profile: g.Profile, Scope: g.Scope, SourceGeneration: g.SourceGeneration})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.db.apiJobs.Enqueue(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	workerJobs, err := NewPostgresJobStore(store.repository, g.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := workerJobs.Claim(t.Context(), "index", time.Now().UTC(), time.Minute); err != nil || claimed.ID != g.ID {
		t.Fatal(err)
	}
	provider := &agentProtocolProvider{}
	indexer, err := catalog.NewSemanticIndexer(store, provider, 16, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := indexer.Run(t.Context(), g, "index"); err != nil || count != 1 {
		t.Fatal("indexing", count, err)
	}
	for _, text := range provider.texts {
		if strings.Contains(text, "extract_pdf_tables") || strings.Contains(text, "private sentinel") {
			t.Fatal("private inventory embedded")
		}
	}
	apiStore, err := NewPostgresSemanticStore(t.Context(), f.repo)
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := catalog.NewSemanticSearch(catalog.SemanticSearchOptions{Store: apiStore, Provider: provider, Profile: g.Profile, RankingProfile: "sha256:" + strings.Repeat("b", 64), Dimensions: 3, Cutoff: 0.5, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: f.repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "tools", SemanticSearch: semantic})
	if err != nil {
		t.Fatal(err)
	}
	q := catalog.SearchQuery{Text: "pdf tables", IncludeMCPTools: true, Origins: []catalog.SearchResultType{catalog.SearchResultLocal}, Limit: 5}
	page, err := service.Search(f.author(t), q)
	if err != nil || page.Total != 1 || page.Tier != "core" || page.Profile != catalog.MCPToolsSearchProfile {
		t.Fatal("tool search used hybrid", page, err)
	}
	facets, err := service.Explore(f.author(t), catalog.ExploreRequest{Query: catalog.SearchQuery{Text: q.Text, IncludeMCPTools: true}, Facets: []catalog.FacetRequest{{Field: "kind"}}})
	if err != nil || facets.Matched != 1 || facets.Tier != "core" {
		t.Fatal("tool facets used hybrid", facets, err)
	}
	if len(provider.queries) != 0 {
		t.Fatal("tool query sent to provider", provider.queries)
	}
	ordinary := q
	ordinary.IncludeMCPTools = false
	ordinary.Text = "Document"
	if page, err := service.Search(f.author(t), ordinary); err != nil || page.Tier != "hybrid" || len(provider.queries) != 1 {
		t.Fatal("hybrid positive control", page, err)
	}
}

func TestPostgresMCPToolExactReadRequiresAudit(t *testing.T) {
	f := newMCPToolFixture(t)
	private := f.seed(t, 0, "Private declaration", `{"tools":[{"name":"audit_private_sentinel"}]}`, false)
	absent := f.seed(t, 1, "No declaration", "", false)
	count := func() int {
		t.Helper()
		var n int
		if err := f.db.operator.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND target_type='version_revision' AND target_id=$2 AND action='evidence.version_revision.read'`, housekeepingTenantA, private.VersionRevision.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	record, err := f.service.GetCatalogRecord(f.author(t), private.Release.ID)
	if err != nil || record.ToolDeclarationRedacted || len(record.VersionRevision.Representations) != 2 || count() != before+1 {
		t.Fatal("private released read lacked audit", record, err)
	}
	columns := apiPrivilegeManifest().tables["audit_events"].insertColumns
	quoted := make([]string, len(columns))
	for i, name := range columns {
		quoted[i] = pgx.Identifier{name}.Sanitize()
	}
	if _, err := f.db.operator.Exec(t.Context(), "REVOKE INSERT ("+strings.Join(quoted, ",")+") ON audit_events FROM "+pgx.Identifier{f.db.api.Config().ConnConfig.User}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	before = count()
	record, err = f.service.GetCatalogRecord(f.author(t), private.Release.ID)
	if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || record.Release.ID != "" || count() != before {
		t.Fatal("failed audit disclosed private content", record, err)
	}
	if _, err := f.service.GetCatalogRecord(f.author(t), absent.Release.ID); err != nil {
		t.Fatal("ordinary read acquired private audit dependency", err)
	}
	if _, reports, err := ApplyAndReconcileRuntimePrivileges(t.Context(), f.db.operator, f.db.api.Config().ConnConfig.User, f.db.worker.Config().ConnConfig.User); err != nil || len(reports) != 2 || !reports[0].Compliant || !reports[1].Compliant {
		t.Fatal("restore exact audit authority", err)
	}
	if record, err := f.service.GetCatalogRecord(f.author(t), private.Release.ID); err != nil || len(record.VersionRevision.Representations) != 2 || count() != before+1 {
		t.Fatal("restored audit could not disclose", record, err)
	}
}
