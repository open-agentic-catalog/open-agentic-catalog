//go:build integration

// Real PostgreSQL projection, exact grants, selection and upgrade qualification.
package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"reflect"
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

func TestPostgresNativeCapabilityProjectionAndPrivileges(t *testing.T) {
	db := newHousekeepingDatabase(t)
	type projectionCase struct{ media, raw, want string }
	cases := make([]projectionCase, 0, 32)
	cases = append(cases, []projectionCase{
		{adapter.MediaA2AAgentCard, `{"skills":[{"id":"resolve-invoices","tags":["finance","finance"]},{"id":"other","tags":["billing"]}],"default_input_modes":["text/plain"],"defaultOutputModes":["application/json"],"capabilities":{"streaming":false},"unknown":{"nul":"\u0000","exact":9007199254740993,"huge":1e100000}}`, `{"agentSkillIds":["other","resolve-invoices"],"agentSkillTags":["billing","finance"],"agentDefaultInputModes":["text/plain"],"agentDefaultOutputModes":["application/json"],"agentStreaming":false}`},
		{adapter.MediaMCPServerCard, `{"remotes":[{"type":"sse","supportedProtocolVersions":["2025-11-25"]},{"type":"streamable-http","supportedProtocolVersions":["2026-07-28"]}]}`, `{"mcpTransports":["sse","streamable-http"],"mcpProtocolVersions":["2025-11-25","2026-07-28"]}`},
		{adapter.MediaA2AAgentCard, `{"capabilities":{"streaming":null}}`, `{}`},
		{adapter.MediaA2AAgentCard, `{"skills":[{"id":"literal\\u0000"}]}`, `{"agentSkillIds":["literal\\u0000"]}`},
		{adapter.MediaA2AAgentCard, `{"skills":[{"id":"actual\u0000"}]}`, `{"unavailable":true}`},
		{adapter.MediaA2AAgentCard, `{"skills":[{"id":false}]}`, `{"unavailable":true}`},
		{adapter.MediaA2AAgentCard, `{"capabilities":{"streaming":"false"}}`, `{"unavailable":true}`},
		{adapter.MediaA2AAgentCard, `{"defaultInputModes":false}`, `{"unavailable":true}`},
		{adapter.MediaA2AAgentCard, `{"defaultInputModes":["` + strings.Repeat("🙂", 513) + `"]}`, `{"unavailable":true}`},
		{adapter.MediaA2AAgentCard, `null`, `{"unavailable":true}`},
		{adapter.MediaA2AAgentCard, `invalid`, `{"unavailable":true}`},
		{adapter.MediaMCPServerCard, `{"remotes":[{"type":"sse","supportedProtocolVersions":[false]}]}`, `{"unavailable":true}`},
		{adapter.MediaMCPServerCard, `{"remotes":[{"type":false}]}`, `{"unavailable":true}`},
		{"application/json", `{"skills":[{"id":"other"}]}`, `{}`},
	}...)
	for _, count := range []int{256, 257} {
		raw := `{"skills":[` + strings.TrimSuffix(strings.Repeat(`{"id":"same"},`, count), ",") + `],"capabilities":{"streaming":false}}`
		want := `{"agentSkillIds":["same"],"agentStreaming":false}`
		if count == 257 {
			want = `{"unavailable":true}`
		}
		cases = append(cases, projectionCase{adapter.MediaA2AAgentCard, raw, want})
	}
	// These repeated values remain small after deduplication. Admission must
	// count the actual inspected values and bytes before deduplicating them.
	for _, count := range []int{64, 65} {
		literal := strings.Repeat("x", 512)
		raw := `{"defaultInputModes":[` + strings.TrimSuffix(strings.Repeat(`"`+literal+`",`, count), ",") + `],"capabilities":{"streaming":false}}`
		want := `{"agentDefaultInputModes":["` + literal + `"],"agentStreaming":false}`
		if count == 65 {
			want = `{"unavailable":true}`
		}
		cases = append(cases, projectionCase{adapter.MediaA2AAgentCard, raw, want})
	}
	for _, count := range []int{32, 33} {
		literal := strings.Repeat("x", 512)
		modes := `[` + strings.TrimSuffix(strings.Repeat(`"`+literal+`",`, count), ",") + `]`
		raw := `{"defaultInputModes":` + modes + `,"defaultOutputModes":` + modes + `,"capabilities":{"streaming":false}}`
		want := `{"agentDefaultInputModes":["` + literal + `"],"agentDefaultOutputModes":["` + literal + `"],"agentStreaming":false}`
		if count == 33 {
			want = `{"unavailable":true}`
		}
		cases = append(cases, projectionCase{adapter.MediaA2AAgentCard, raw, want})
	}
	for _, count := range []int{128, 129} {
		first := `[` + strings.TrimSuffix(strings.Repeat(`"same",`, 128), ",") + `]`
		tags := `[` + strings.TrimSuffix(strings.Repeat(`"same",`, count), ",") + `]`
		raw := `{"skills":[{"tags":` + first + `},{"tags":` + tags + `}],"capabilities":{"streaming":false}}`
		want := `{"agentSkillTags":["same"],"agentStreaming":false}`
		if count == 129 {
			want = `{"unavailable":true}`
		}
		cases = append(cases, projectionCase{adapter.MediaA2AAgentCard, raw, want})
	}
	const prefix = `{"capabilities":{"streaming":false},"unknown":"`
	const suffix = `"}`
	bounded := prefix + strings.Repeat("x", (16<<20)-len(prefix)-len(suffix)) + suffix
	cases = append(cases,
		projectionCase{adapter.MediaA2AAgentCard, bounded, `{"agentStreaming":false}`},
		projectionCase{adapter.MediaA2AAgentCard, bounded + " ", `{"unavailable":true}`},
	)
	for _, tc := range cases {
		var actual []byte
		if err := db.api.QueryRow(t.Context(), "SELECT catalog_native_capabilities_v1($1,$2,NULL)", tc.media, []byte(tc.raw)).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		var got, want any
		if json.Unmarshal(actual, &got) != nil || json.Unmarshal([]byte(tc.want), &want) != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("independent SQL result", tc.media, len(tc.raw), string(actual), tc.want)
		}
		memory, err := json.Marshal(catalog.ProjectNativeCapabilities(tc.media, []byte(tc.raw)))
		if err != nil {
			t.Fatal(err)
		}
		var projected any
		if json.Unmarshal(memory, &projected) != nil || !reflect.DeepEqual(projected, want) {
			t.Fatal("memory projection differs from independent expectation", string(memory), tc.want)
		}
	}
	for _, table := range []string{"document_blobs", "representations"} {
		var allowed bool
		if err := db.api.QueryRow(t.Context(), "SELECT has_column_privilege(current_user,$1,'native_capabilities','SELECT')", table).Scan(&allowed); err != nil || !allowed {
			t.Fatal("API projection grant missing", table, err)
		}
		if err := db.worker.QueryRow(t.Context(), "SELECT has_column_privilege(current_user,$1,'native_capabilities','SELECT')", table).Scan(&allowed); err != nil || allowed {
			t.Fatal("worker gained private projection", table, err)
		}
		var generated string
		if err := db.operator.QueryRow(t.Context(), `SELECT pg_get_expr(d.adbin,d.adrelid) FROM pg_attrdef d JOIN pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum WHERE d.adrelid=$1::regclass AND a.attname='native_capabilities'`, table).Scan(&generated); err != nil || !strings.Contains(generated, "catalog_native_capabilities_v1") {
			t.Fatal("projection is not generated", err)
		}
	}
}

func TestPostgresNativeCapabilityProjectionUpgradePreservesBytes(t *testing.T) {
	db := newHousekeepingDatabase(t)
	tx, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(t.Context(), tx)()
	schema := fmt.Sprintf("catalog_native_upgrade_%d", time.Now().UnixNano())
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
		if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "000054" {
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
	if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,display_name,created_at) VALUES ($1,'Native upgrade',now())`, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"skills":[{"id":"resolve-invoices"}],"capabilities":{"streaming":false},"extension":{"nul":"\u0000","huge":1e100000}}`)
	sum := sha256.Sum256(raw)
	key := hex.EncodeToString(sum[:])
	if _, err := tx.Exec(t.Context(), `INSERT INTO document_blobs(tenant_id,id,media_type,content_digest,size_bytes,content,created_at) VALUES ($1,$2,$3,$4,$5,$6,now())`, housekeepingTenantA, key, adapter.MediaA2AAgentCard, "sha256:"+key, len(raw), raw); err != nil {
		t.Fatal(err)
	}
	ddl, err := fs.ReadFile(migrations.Files, "000054_native_capability_projection.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), string(ddl)); err != nil {
		t.Fatal(err)
	}
	var content, projection []byte
	var digest string
	if err := tx.QueryRow(t.Context(), `SELECT content,content_digest,native_capabilities FROM document_blobs WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, key).Scan(&content, &digest, &projection); err != nil || !bytes.Equal(content, raw) || digest != "sha256:"+key || !bytes.Contains(projection, []byte("resolve-invoices")) || !bytes.Contains(projection, []byte(`"agentStreaming": false`)) {
		t.Fatal("upgrade changed evidence or missed facts", string(projection), err)
	}
}

func TestPostgresNativeCapabilitiesIndependentFieldsAndPaging(t *testing.T) {
	docs := make([]catalog.SearchDocument, 9)
	listings := make([]catalog.CreateListingRevisionInput, len(docs))
	for i := range docs {
		docs[i] = catalog.SearchDocument{Identifier: fmt.Sprintf("urn:air:catalog.example.com:finance:native-%d", i), DisplayName: fmt.Sprintf("Native %d", i), Description: "Searchable native fixture"}
	}
	_, repo, _, seed := newSearchCostFixtureWithListings(t, listings, docs...)
	original, err := os.ReadFile("../../../samples/cards/dice-rest.json")
	if err != nil {
		t.Fatal(err)
	}
	for i := range docs {
		if i == 1 {
			continue
		} // The fixture reserves this slot for a Skill archive.
		var card map[string]any
		if err := json.Unmarshal(original, &card); err != nil {
			t.Fatal(err)
		}
		card["skills"] = []map[string]any{
			{"id": "resolve-invoices", "name": "Invoices", "description": "Resolve invoices", "tags": []string{"billing"}},
			{"id": "other", "name": "Finance", "description": "Read finance metadata", "tags": []string{"finance"}},
		}
		card["defaultInputModes"] = []string{"text/plain"}
		card["defaultOutputModes"] = []string{"application/json"}
		switch {
		case i == 3:
			card["capabilities"] = map[string]any{"streaming": true}
		case i%2 == 0:
			card["capabilities"] = map[string]any{"streaming": false}
		default:
			card["capabilities"] = map[string]any{}
		}
		raw, err := json.Marshal(card)
		if err != nil {
			t.Fatal(err)
		}
		seed(i, raw)
	}
	streaming := false
	q := catalog.SearchQuery{Origins: []catalog.SearchResultType{catalog.SearchResultLocal}, NativeCapabilities: &catalog.NativeCapabilitiesFilter{AgentSkillIDs: []string{"resolve-invoices"}, AgentSkillTags: []string{"finance"}, AgentDefaultInputModes: []string{"text/plain"}, AgentDefaultOutputModes: []string{"application/json"}, AgentStreaming: &streaming}}
	var after *catalog.CatalogSearchPosition
	seen := map[string]bool{}
	names := map[string]bool{}
	for {
		page, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: q, Limit: 2, After: after})
		if err != nil || page.Total != 5 || len(page.Items) > 2 {
			t.Fatal("independent fields / false population", page, err)
		}
		for _, item := range page.Items {
			if seen[item.Release.ID] {
				t.Fatal("repeated release")
			}
			seen[item.Release.ID] = true
			names[item.ListingRevision.DisplayName] = true
		}
		if !page.HasMore {
			break
		}
		last := page.Items[len(page.Items)-1]
		after = &catalog.CatalogSearchPosition{Score: last.RelevanceScore, DisplayName: catalog.CatalogSearchSortName(last.ListingRevision.DisplayName), ResultID: "local:" + last.Release.ID}
	}
	if len(seen) != 5 {
		t.Fatal("lost page membership", seen)
	}
	if !reflect.DeepEqual(names, map[string]bool{"Native 0": true, "Native 2": true, "Native 4": true, "Native 6": true, "Native 8": true}) {
		t.Fatal("wrong matching records survived paging", names)
	}
	facets, err := repo.ExploreCatalog(t.Context(), housekeepingTenantA, q, []catalog.FacetRequest{{Field: "kind", Limit: 10}})
	if err != nil || facets.Matched != 5 || facets.Facets["kind"].Buckets[0].Count != 5 {
		t.Fatal("facets broadened", facets, err)
	}
	// All other selectors remain valid, so omitting any one SQL predicate
	// would expose the five-record control population in its mismatch case.
	for _, tc := range []struct {
		name, literal string
		values        func(*catalog.NativeCapabilitiesFilter) *[]string
	}{
		{"skill IDs", "resolve-invoices", func(f *catalog.NativeCapabilitiesFilter) *[]string { return &f.AgentSkillIDs }},
		{"skill tags", "finance", func(f *catalog.NativeCapabilitiesFilter) *[]string { return &f.AgentSkillTags }},
		{"input modes", "text/plain", func(f *catalog.NativeCapabilitiesFilter) *[]string { return &f.AgentDefaultInputModes }},
		{"output modes", "application/json", func(f *catalog.NativeCapabilitiesFilter) *[]string { return &f.AgentDefaultOutputModes }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, choice := range []struct {
				values []string
				want   int
			}{{[]string{"not-declared", tc.literal}, 5}, {[]string{"not-declared"}, 0}} {
				selected := q
				selected.NativeCapabilities = q.NativeCapabilities.Clone()
				*tc.values(selected.NativeCapabilities) = choice.values
				page, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: selected, Limit: 2})
				if err != nil || page.Total != choice.want {
					t.Fatal("independent predicate or OR alternatives", page, choice, err)
				}
				facets, err := repo.ExploreCatalog(t.Context(), housekeepingTenantA, selected, []catalog.FacetRequest{{Field: "kind", Limit: 10}})
				if err != nil || facets.Matched != choice.want {
					t.Fatal("facet predicate or OR alternatives", facets, choice, err)
				}
			}
		})
	}
	truth := true
	for _, tc := range []struct {
		value *bool
		want  int
	}{{nil, 8}, {&truth, 1}} {
		selected := q
		selected.NativeCapabilities = q.NativeCapabilities.Clone()
		selected.NativeCapabilities.AgentStreaming = tc.value
		page, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: selected, Limit: 2})
		if err != nil || page.Total != tc.want {
			t.Fatal("omitted or true streaming", page, err)
		}
		facets, err := repo.ExploreCatalog(t.Context(), housekeepingTenantA, selected, []catalog.FacetRequest{{Field: "kind", Limit: 10}})
		if err != nil || facets.Matched != tc.want {
			t.Fatal("omitted or true streaming facets", facets, err)
		}
	}
	q.NativeCapabilities.AgentSkillIDs = []string{"Resolve-invoices"}
	empty, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: q, Limit: 2})
	if err != nil || empty.Total != 0 {
		t.Fatal("literal case changed", empty, err)
	}
}

func TestPostgresNativeMCPRemoteDeclarations(t *testing.T) {
	f := newMCPToolFixture(t)
	var card map[string]any
	if err := json.Unmarshal(f.card, &card); err != nil {
		t.Fatal(err)
	}
	card["remotes"] = []any{
		map[string]any{"type": "sse", "url": "https://inert.example/one", "supportedProtocolVersions": []string{"2025-11-25"}},
		map[string]any{"type": "streamable-http", "url": "https://inert.example/two", "supportedProtocolVersions": []string{"2026-07-28"}},
	}
	var err error
	f.card, err = json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	f.seed(t, 0, "MCP remote declarations", `{"tools":[{"name":"invoice_lookup","description":"Look up invoices"}]}`, false)
	q := catalog.SearchQuery{Origins: []catalog.SearchResultType{catalog.SearchResultLocal}, NativeCapabilities: &catalog.NativeCapabilitiesFilter{MCPTransports: []string{"sse"}, MCPProtocolVersions: []string{"2026-07-28"}}}
	for _, toolText := range []string{"", "invoice"} {
		q.Text = toolText
		q.IncludeMCPTools = toolText != ""
		result, err := f.service.Search(f.author(t), q)
		if err != nil || result.Total != 1 {
			t.Fatal("independent remotes and same-release tools", result, err)
		}
		facet := q
		facet.Limit = 0
		out, err := f.service.Explore(f.author(t), catalog.ExploreRequest{Query: facet, Facets: []catalog.FacetRequest{{Field: "kind"}}})
		if err != nil || out.Matched != 1 {
			t.Fatal("MCP remote facets", out, err)
		}
	}
	q.NativeCapabilities.MCPTransports = []string{"SSE"}
	out, err := f.service.Search(f.author(t), q)
	if err != nil || out.Total != 0 {
		t.Fatal("MCP case changed", out, err)
	}
	for _, tc := range []struct {
		transports, protocols []string
		want                  int
	}{
		{[]string{"not-declared", "sse"}, []string{"not-declared", "2026-07-28"}, 1},
		{[]string{"sse"}, []string{"not-declared"}, 0},
		{[]string{"not-declared"}, []string{"2026-07-28"}, 0},
	} {
		selected := q
		selected.NativeCapabilities = &catalog.NativeCapabilitiesFilter{MCPTransports: tc.transports, MCPProtocolVersions: tc.protocols}
		result, err := f.service.Search(f.author(t), selected)
		if err != nil || result.Total != tc.want {
			t.Fatal("independent MCP predicate or OR alternatives", result, tc, err)
		}
		facets, err := f.service.Explore(f.author(t), catalog.ExploreRequest{Query: selected, Facets: []catalog.FacetRequest{{Field: "kind"}}})
		if err != nil || facets.Matched != tc.want {
			t.Fatal("independent MCP facet predicate", facets, tc, err)
		}
	}
}

func TestPostgresNativeCapabilityContentScopePrecedesPaging(t *testing.T) {
	f := newMCPToolFixture(t)
	for i := range 5 {
		f.seed(t, i, fmt.Sprintf("A denied %d", i), "", false)
	}
	visible, err := f.service.CreateNamespace(f.author(t), catalog.CreateNamespaceInput{Slug: "visible-native", DisplayName: "Visible native cards"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		r, err := f.service.CreateResource(f.author(t), catalog.CreateResourceInput{NamespaceID: visible.ID, Kind: catalog.KindMCPServer, CanonicalIdentifier: fmt.Sprintf("urn:air:catalog.example.com:visible-native:mcp-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		_, v, ev, err := f.service.CreateVersion(f.author(t), r.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: f.representations(t, "1.0.0", "", false)})
		if err != nil || ev.State != "complete" {
			t.Fatal(ev, err)
		}
		listing, err := f.service.CreateListing(f.author(t), catalog.CreateListingInput{NamespaceID: visible.ID, ResourceID: r.ID})
		if err != nil {
			t.Fatal(err)
		}
		f.publish(t, listing, v, fmt.Sprintf("Z visible %d", i))
	}
	store, err := NewPostgresIdentityStore(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GrantRole(f.author(t), identity.GrantRoleInput{Subject: "native-scoped", ActorType: identity.ActorHuman, Role: identity.RoleServiceClient, Reason: "Own metadata-only fixture"}, "native-member"); err != nil {
		t.Fatal(err)
	}
	actor, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: "native-scoped"})
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range []struct {
		name, namespace string
		permissions     []identity.Permission
	}{
		{"Native metadata", "", []identity.Permission{"resource.read"}},
		{"Native content", visible.ID, []identity.Permission{"resource.read_content"}},
	} {
		role, err := authority.SaveRoleDefinition(f.author(t), "", 0, identity.RoleDefinitionInput{Name: grant.name, Permissions: grant.permissions, Reason: "Independent native read scope"}, false, strings.ReplaceAll(grant.name, " ", "-")+"-role")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := authority.GrantAuthorizationAssignment(f.author(t), identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: actor.MembershipID, NamespaceID: grant.namespace, Reason: "Exact native fixture authority"}, strings.ReplaceAll(grant.name, " ", "-")+"-assignment"); err != nil {
			t.Fatal(err)
		}
	}
	ctx := identity.WithActor(t.Context(), actor)
	ordinary, err := f.service.Search(ctx, catalog.SearchQuery{Limit: 1})
	if err != nil || ordinary.Total != 7 {
		t.Fatal("metadata control", ordinary, err)
	}
	var card map[string]json.RawMessage
	if err := json.Unmarshal(f.card, &card); err != nil {
		t.Fatal(err)
	}
	var remotes []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(card["remotes"], &remotes); err != nil || len(remotes) == 0 {
		t.Fatal("remote fixture", err)
	}
	q := catalog.SearchQuery{Origins: []catalog.SearchResultType{catalog.SearchResultLocal}, NativeCapabilities: &catalog.NativeCapabilitiesFilter{MCPTransports: []string{remotes[0].Type}}, Limit: 1}
	first, err := f.service.Search(ctx, q)
	if err != nil || first.Total != 2 || len(first.Items) != 1 || first.NextCursor == "" || first.Items[0].Listing.NamespaceID != visible.ID {
		t.Fatal("denied rows consumed page or counts", first, err)
	}
	q.Cursor = first.NextCursor
	second, err := f.service.Search(ctx, q)
	if err != nil || second.Total != 2 || len(second.Items) != 1 || second.NextCursor != "" || second.Items[0].Release.ID == first.Items[0].Release.ID {
		t.Fatal("native scope continuation", second, err)
	}
	q.Cursor = ""
	q.Limit = 0
	facets, err := f.service.Explore(ctx, catalog.ExploreRequest{Query: q, Facets: []catalog.FacetRequest{{Field: "namespace", Limit: 10}}})
	wantNamespace := catalog.FacetResult{Buckets: []catalog.FacetBucket{{Value: visible.ID, Count: 2}}}
	if err != nil || facets.Matched != 2 || !reflect.DeepEqual(facets.Facets["namespace"], wantNamespace) {
		t.Fatal("denied rows leaked into facets", facets, err)
	}
}
