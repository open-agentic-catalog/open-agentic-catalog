package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestMCPToolDeclarationAdmission(t *testing.T) {
	for _, raw := range []string{`{"tools":[]}`, `{"tools":[{"name":"extract_pdf_tables"},{"name":"Read","description":""},{"name":"read","description":"Unicode: λ"}]}`} {
		if _, err := ParseMCPTools([]byte(raw)); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{`{}`, `{"tools":null}`, `{"tools":[null]}`, `{"Tools":[]}`, `{"tools":[],"tools":[]}`, `{"tools":[],"extension":true}`, `{"tools":[]} {}`, `{"tools":[{"name":"x","description":null}]}`, `{"tools":[{"Name":"x"}]}`, `{"tools":[{"name":"x","inputSchema":{}}]}`, `{"tools":[{"name":""}]}`, `{"tools":[{"name":" \u0085"}]}`, `{"tools":[{"name":"x"},{"name":"x"}]}`, `{"tools":[{"name":"x\u0000"}]}`, `{"tools":[{"name":"\ud800"}]}`} {
		if _, err := ParseMCPTools([]byte(raw)); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("accepted %s: %v", raw, err)
		}
	}
	for _, count := range []int{128, 129} {
		raw, err := json.Marshal(MCPToolDeclaration{Tools: []MCPTool{{Name: strings.Repeat("🙂", count), Description: strings.Repeat("λ", 1024)}}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = ParseMCPTools(raw)
		if (err == nil) != (count == 128) {
			t.Fatal("code point bounds", count, err)
		}
	}
	for _, count := range []int{64, 65} {
		tools := make([]MCPTool, count)
		for i := range tools {
			tools[i].Name = strings.Repeat("x", i+1)
		}
		raw, err := json.Marshal(MCPToolDeclaration{Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseMCPTools(raw); (err == nil) != (count == 64) {
			t.Fatal("tool count bound", count, err)
		}
	}
	raw, err := json.Marshal(MCPToolDeclaration{Tools: []MCPTool{{Name: "x", Description: strings.Repeat("λ", 1025)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMCPTools(raw); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("description bound", err)
	}
	// A declaration exactly at the transport byte ceiling is valid, including
	// insignificant JSON whitespace; one more byte must reject the whole input.
	exact := []byte(`{"tools":[]}` + strings.Repeat(" ", 65536-len(`{"tools":[]}`)))
	if value, err := ParseMCPTools(exact); err != nil || value.Tools == nil || len(value.Tools) != 0 {
		t.Fatal("exact byte ceiling rejected an explicitly empty declaration", err)
	}
	oversize := append(slices.Clone(exact), ' ')
	if _, err := ParseMCPTools(oversize); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("byte bound", err)
	}
	base := RepresentationInput{Role: RepresentationMetadata, MediaType: MCPToolsMediaType, Data: json.RawMessage(`{"tools":[]}`)}
	if err := validateMCPToolInputs(KindMCPServer, []RepresentationInput{base}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*RepresentationInput){func(v *RepresentationInput) { v.Role = RepresentationPrimary }, func(v *RepresentationInput) { v.URL = "https://never.example/tools" }, func(v *RepresentationInput) { v.ProfileID = "other" }} {
		invalid := base
		change(&invalid)
		if err := validateMCPToolInputs(KindMCPServer, []RepresentationInput{invalid}); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal("invalid attachment admitted", err)
		}
	}
	if err := validateMCPToolInputs(KindAgent, []RepresentationInput{base}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("wrong kind", err)
	}
	if err := validateMCPToolInputs(KindMCPServer, []RepresentationInput{base, base}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("conflicting declarations", err)
	}
}

func TestMCPToolMatchingBoundedIndependentTerms(t *testing.T) {
	if got := MatchMCPTools([]MCPTool{{Name: "pdf"}, {Name: "tables"}}, "pdf tables", "r", "d"); got != nil {
		t.Fatal("terms spanned different tools", got)
	}
	tools := []MCPTool{{Name: "extract_pdf_tables", Description: "PDF tables " + strings.Repeat("λ", 300)}, {Name: "b_pdf_tables"}, {Name: "a_pdf_tables"}, {Name: "unrelated", Description: "pdf tables"}}
	got := MatchMCPTools(tools, "PDF tables", "representation", "sha256:digest")
	if got == nil || got.Score != 70 || got.MatchedToolCount != 4 || len(got.Tools) != 3 || !got.Truncated || got.Tools[0].Name != "extract_pdf_tables" || !got.Tools[0].DescriptionTruncated || len([]rune(got.Tools[0].Description)) != 256 || got.Tools[1].Name != "a_pdf_tables" || got.RepresentationID != "representation" {
		t.Fatal(got)
	}
	one := MatchMCPTools(tools[:1], "pdf tables", "r", "d")
	if one.Score != got.Score {
		t.Fatal("inventory size boosted score")
	}
}

func TestMCPToolMatchWeightsAndSnippetBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, description string
		score             int
	}{
		{"extract_pdf_tables", "PDF tables", 70},
		{"extract_pdf_tables", "other", 60},
		{"other", "PDF tables", 50},
		{"extract_pdf", "tables", 40},
	} {
		got := MatchMCPTools([]MCPTool{{Name: tc.name, Description: tc.description}}, "pdf tables", "rep", "sha256:declaration")
		if got == nil || got.Score != tc.score || got.MatchedToolCount != 1 || got.Truncated || got.ContentDigest != "sha256:declaration" {
			t.Fatalf("independent field weights: %#v, got %#v", tc, got)
		}
	}
	for _, size := range []int{256, 257} {
		got := MatchMCPTools([]MCPTool{{Name: "pdf_tables", Description: strings.Repeat("🙂", size)}}, "pdf tables", "rep", "digest")
		if got == nil || len(got.Tools) != 1 || got.Tools[0].Name != "pdf_tables" || got.Tools[0].Description != strings.Repeat("🙂", 256) || got.Tools[0].DescriptionTruncated != (size == 257) {
			t.Fatalf("Unicode snippet boundary at %d: %#v", size, got)
		}
	}
}

func TestMCPToolInlineCanonicalByteAdmission(t *testing.T) {
	tools := make([]MCPTool, 12)
	for i := range tools {
		tools[i] = MCPTool{Name: strings.Repeat("x", i+1), Description: strings.Repeat("<", 1024)}
	}
	raw, err := json.Marshal(MCPToolDeclaration{Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	// Literal HTML characters are valid JSON. Their retained inline spelling
	// expands to six-byte escapes, independently of the source byte budget.
	raw = []byte(strings.ReplaceAll(string(raw), `\u003c`, "<"))
	if _, err := ParseMCPTools(raw); err != nil {
		t.Fatal("raw declaration is not a valid within-budget control", err)
	}
	input := RepresentationInput{Role: RepresentationMetadata, MediaType: MCPToolsMediaType, Data: raw}
	if err := validateMCPToolInputs(KindMCPServer, []RepresentationInput{input}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("inline canonical byte overflow was not rejected during preflight", err)
	}
	input.Data, input.Content = nil, string(raw)
	if err := validateMCPToolInputs(KindMCPServer, []RepresentationInput{input}); err != nil {
		t.Fatal("exact text retention incorrectly inherited inline JSON expansion", err)
	}
}

func TestMCPToolQueryScopeAndProfile(t *testing.T) {
	q := SearchQuery{IncludeMCPTools: true, Origins: []SearchResultType{SearchResultLocal}, Text: "pdf tables"}
	if err := ValidateSavedSearch(q); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SearchQuery){func(v *SearchQuery) { v.Origins = nil }, func(v *SearchQuery) { v.Origins = []SearchResultType{SearchResultIndexed} }, func(v *SearchQuery) { v.Text = " \n" }, func(v *SearchQuery) { v.Kinds = []Kind{KindAgent} }, func(v *SearchQuery) { v.AgentProtocol = "JSONRPC" }} {
		wrong := q
		change(&wrong)
		if err := validateCatalogSearchQuery(wrong); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal(wrong, err)
		}
		if err := ValidateSavedSearch(wrong); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal("saved scope broadened", err)
		}
	}
	if coreProfile(q) != MCPToolsSearchProfile || coreProfile(SearchQuery{}) != CoreSearchProfile {
		t.Fatal("profile binding")
	}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "tools-reader", Roles: []identity.Role{identity.RoleReader}})
	actor, _ := identity.FromContext(ctx)
	service := &Service{policy: "tools-test", cursorKey: []byte("01234567890123456789012345678901")}
	selected, err := service.unifiedSearchCursorBinding(ctx, actor, q)
	if err != nil {
		t.Fatal(err)
	}
	ordinary := q
	ordinary.IncludeMCPTools = false
	other, err := service.unifiedSearchCursorBinding(ctx, actor, ordinary)
	if err != nil || other == selected {
		t.Fatal("private tool selection was absent from cursor authority", err)
	}

}

func TestMemoryMCPToolsReleasedContentAuthority(t *testing.T) {
	const tenant = "tenant"
	m := NewMemoryRepository()
	raw, err := os.ReadFile("testdata/mcp-tools/tool-only.json")
	if err != nil {
		t.Fatal(err)
	}
	m.versionRevisions["released"] = VersionRevision{TenantID: tenant, Representations: []Representation{{ID: "rep", Role: RepresentationMetadata, MediaType: MCPToolsMediaType, ProfileID: MCPToolsProfile, ValidationState: "valid", Data: raw, ContentDigest: "digest"}}}
	row := CatalogSummary{Resource: CatalogResourceSummary{Kind: KindMCPServer, CanonicalIdentifier: "opaque:mcp"}, VersionRevision: CatalogRevisionSummary{ID: "released"}, Listing: CatalogListingSummary{NamespaceID: "ns"}, ListingRevision: CatalogDisplaySummary{DisplayName: "Document Utilities", Description: "Document processing"}}
	q := SearchQuery{IncludeMCPTools: true, Origins: []SearchResultType{SearchResultLocal}, Text: "pdf tables"}
	for _, role := range []identity.Role{identity.RoleReader, identity.RoleServiceClient} {
		ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenant, Subject: "reader", Roles: []identity.Role{role}})
		ctx, _, err = identity.WithCollectionContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := m.mcpToolRecordsLocked(ctx, tenant, []CatalogSummary{row}, q)
		if err != nil {
			t.Fatal(err)
		}
		matches := filterAndScoreRecords(selected, q)
		if role == identity.RoleReader {
			if len(matches) != 1 || matches[0].ToolMatch == nil || matches[0].ToolMatch.Tools[0].Name != "extract_pdf_tables" {
				t.Fatal(matches)
			}
		} else if len(matches) != 0 {
			t.Fatal("private content disclosed", matches)
		}
		ordinary := q
		ordinary.IncludeMCPTools = false
		if len(filterAndScoreRecords([]CatalogSummary{row}, ordinary)) != 0 {
			t.Fatal("ordinary search expanded")
		}
	}
}

type mcpRecordReadRepository struct {
	Repository
	record   CatalogRecord
	audits   []AuditEvent
	auditErr error
}

func (r *mcpRecordReadRepository) GetCatalogRecord(_ context.Context, tenant, release string) (CatalogRecord, error) {
	if tenant != r.record.Resource.TenantID || release != r.record.Release.ID {
		return CatalogRecord{}, E(CodeNotFound, "record unavailable")
	}
	return r.record, nil
}

func (r *mcpRecordReadRepository) AppendAuditEvent(_ context.Context, event AuditEvent) error {
	if r.auditErr != nil {
		return r.auditErr
	}
	r.audits = append(r.audits, event)
	return nil
}

func TestMCPRecordReadRedactsOrAuditsToolDeclarations(t *testing.T) {
	primary := Representation{ID: "primary", Role: RepresentationPrimary, MediaType: "application/json"}
	tools := Representation{ID: "tools", Role: RepresentationMetadata, MediaType: MCPToolsMediaType, ProfileID: MCPToolsProfile, Data: json.RawMessage(`{"tools":[{"name":"extract_pdf_tables"}]}`)}
	digest := "sha256:" + strings.Repeat("a", 64)
	auditFailure := errors.New("audit storage unavailable")
	for _, tc := range []struct {
		name                            string
		content, declaration, failAudit bool
	}{
		{name: "metadata reader with declaration", declaration: true},
		{name: "metadata reader without declaration"},
		{name: "content reader with declaration", content: true, declaration: true},
		{name: "content reader without declaration", content: true},
		{name: "content reader when audit fails", content: true, declaration: true, failAudit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, policy := pass2CatalogSelectionContext(t, "resource.read")
			policy.allowed["resource.read_content"] = tc.content
			record := CatalogRecord{
				Resource:        Resource{TenantID: pass2CatalogTenant, NamespaceID: pass2CatalogNamespace, Kind: KindMCPServer},
				Release:         ListingRelease{ID: "00000000-0000-7000-8000-000000000081"},
				VersionRevision: VersionRevision{ID: pass2DraftRevision, ContentSetDigest: digest, Representations: []Representation{primary}},
			}
			if tc.declaration {
				record.VersionRevision.Representations = append(record.VersionRevision.Representations, tools)
			}
			repo := &mcpRecordReadRepository{record: record}
			if tc.failAudit {
				repo.auditErr = auditFailure
			}
			service := Service{repo: repo, ids: &guardrailIDs{}, clock: &guardrailClock{now: time.Unix(100, 0).UTC()}}
			got, err := service.GetCatalogRecord(ctx, record.Release.ID)
			if tc.failAudit {
				if ErrorCodeOf(err) != CodeDependencyUnavailable || !errors.Is(err, auditFailure) || !reflect.DeepEqual(got, CatalogRecord{}) {
					t.Fatalf("record disclosed or audit failure lost: %+v, %v", got, err)
				}
				return
			}
			want := []Representation{primary}
			if tc.content && tc.declaration {
				want = append(want, tools)
			}
			if err != nil || got.Release.ID != record.Release.ID || got.ToolDeclarationRedacted != !tc.content || !reflect.DeepEqual(got.VersionRevision.Representations, want) {
				t.Fatalf("release read: %+v, %v", got, err)
			}
			if tc.content && tc.declaration {
				if len(repo.audits) != 1 {
					t.Fatalf("expected one private read audit, got %d", len(repo.audits))
				}
				event := repo.audits[0]
				if event.Action != audit.ActionVersionRevisionRead || event.TargetID != record.VersionRevision.ID || event.TenantID != pass2CatalogTenant || event.AfterDigest != digest {
					t.Fatalf("audit did not identify the disclosed revision: %+v", event)
				}
			} else if len(repo.audits) != 0 {
				t.Fatal("private read was recorded without disclosing a declaration")
			}
		})
	}
}

func TestMCPToolDatasetProjectionOffline(t *testing.T) {
	raw, err := os.ReadFile("testdata/mcp-tools/dataset-derived.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := ParseMCPTools(raw)
	if err != nil || len(value.Tools) != 2 {
		t.Fatal(err)
	}
	for _, tc := range []struct{ query, name string }{{"version history", "app_get"}, {"filter available", "app_list"}} {
		match := MatchMCPTools(value.Tools, tc.query, "r", "d")
		if match == nil || match.MatchedToolCount != 1 || match.Tools[0].Name != tc.name {
			t.Fatal("source-derived match", tc, match)
		}
	}
}

func FuzzMCPToolDeclaration(f *testing.F) {
	for _, seed := range []string{`{"tools":[]}`, `{"tools":[{"name":"extract_pdf_tables","description":"PDF tables λ"}]}`, `{"tools":[{"name":"x"},{"name":"x"}]}`, `{"tools":null}`, `{"tools":[{"name":"\ud800"}]}`, `{"tools":[{"name":"x","description":null}]}`} {
		f.Add([]byte(seed))
	}
	f.Add([]byte("{\"tools\":[{\"name\":\"\xff\"}]}"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		value, err := ParseMCPTools(raw)
		if err != nil {
			if ErrorCodeOf(err) != CodeInvalidArgument {
				t.Fatal("invalid declarations must have the documented error code", err)
			}
			return
		}
		if !utf8.Valid(raw) || len(raw) > 65536 || value.Tools == nil || len(value.Tools) > 64 {
			t.Fatal("accepted declaration violates the byte, encoding or count contract")
		}
		seen := map[string]bool{}
		for _, tool := range value.Tools {
			if !utf8.ValidString(tool.Name) || !utf8.ValidString(tool.Description) || strings.TrimSpace(tool.Name) == "" || utf8.RuneCountInString(tool.Name) > 128 || utf8.RuneCountInString(tool.Description) > 1024 || strings.ContainsRune(tool.Name+tool.Description, 0) || seen[tool.Name] {
				t.Fatal("accepted tool violates the text or unique-name contract")
			}
			seen[tool.Name] = true
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		// JSON escaping can expand valid text beyond the independent byte bound.
		if len(encoded) <= 65536 {
			decoded, err := ParseMCPTools(encoded)
			if err != nil || !slices.Equal(decoded.Tools, value.Tools) {
				t.Fatal("accepted declaration changed through the native JSON codec", err)
			}
		}
	})
}
