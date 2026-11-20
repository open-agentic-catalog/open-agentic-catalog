// Isolated selector and retained-card projection contracts; no runtime or I/O.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestNativeCapabilityAdmissionAndPresence(t *testing.T) {
	falsity := false
	q := SearchQuery{Origins: []SearchResultType{SearchResultLocal}, NativeCapabilities: &NativeCapabilitiesFilter{AgentStreaming: &falsity, AgentSkillIDs: []string{"z", " 私有 "}}}
	if err := validateCatalogSearchQuery(q); err != nil {
		t.Fatal(err)
	}
	normal := normalizeCatalogSearchQuery(q)
	if !reflect.DeepEqual(normal.NativeCapabilities.AgentSkillIDs, []string{" 私有 ", "z"}) || !reflect.DeepEqual(q.NativeCapabilities.AgentSkillIDs, []string{"z", " 私有 "}) || normal.NativeCapabilities.AgentStreaming == nil || *normal.NativeCapabilities.AgentStreaming {
		t.Fatal("literal normalization or false presence", normal, q)
	}
	*normal.NativeCapabilities.AgentStreaming = true
	if *q.NativeCapabilities.AgentStreaming {
		t.Fatal("normalization aliased caller boolean")
	}
	if err := ValidateSavedSearch(q); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []SearchQuery{
		{NativeCapabilities: q.NativeCapabilities},
		{Origins: []SearchResultType{SearchResultIndexed}, NativeCapabilities: q.NativeCapabilities},
		{Origins: q.Origins, NativeCapabilities: &NativeCapabilitiesFilter{}},
		{Origins: q.Origins, NativeCapabilities: &NativeCapabilitiesFilter{AgentSkillIDs: []string{}}},
		{Origins: q.Origins, NativeCapabilities: &NativeCapabilitiesFilter{AgentSkillIDs: []string{"x", "x"}}},
		{Origins: q.Origins, NativeCapabilities: &NativeCapabilitiesFilter{AgentSkillIDs: []string{" "}}},
		{Origins: q.Origins, NativeCapabilities: &NativeCapabilitiesFilter{AgentSkillIDs: []string{"x\x00"}}},
		{Origins: q.Origins, NativeCapabilities: &NativeCapabilitiesFilter{AgentSkillIDs: []string{strings.Repeat("🙂", 257)}}},
		{Origins: q.Origins, NativeCapabilities: &NativeCapabilitiesFilter{AgentStreaming: &falsity, MCPTransports: []string{"http"}}},
		{Origins: q.Origins, Kinds: []Kind{KindSkill}, NativeCapabilities: q.NativeCapabilities},
		{Origins: q.Origins, AgentProtocol: "JSONRPC", NativeCapabilities: &NativeCapabilitiesFilter{MCPTransports: []string{"http"}}},
		{Origins: q.Origins, IncludeMCPTools: true, Text: "weather", NativeCapabilities: q.NativeCapabilities},
	} {
		if ErrorCodeOf(validateCatalogSearchQuery(bad)) != CodeInvalidArgument || ErrorCodeOf(ValidateSavedSearch(bad)) != CodeInvalidArgument {
			t.Fatal("invalid criteria admitted", bad)
		}
	}
	for _, raw := range []string{`{"nativeCapabilities":null}`, `{"nativeCapabilities":{"agentStreaming":null}}`, `{"nativeCapabilities":{"agentStreaming":"false"}}`, `{"nativeCapabilities":{"AgentStreaming":false}}`, `{"nativeCapabilities":{"path":"data.skills.id"}}`, `{"nativeCapabilities":{"agentSkillIds":["one"],"agentSkillIds":["two"]}}`} {
		var got SearchQuery
		if jsondoc.Decode([]byte(raw), &got) == nil {
			t.Fatal("open or null native control admitted", raw)
		}
	}
	values := make([]string, 16, 17)
	for i := range values {
		values[i] = fmt.Sprintf("id-%d", i)
	}
	q.NativeCapabilities = &NativeCapabilitiesFilter{AgentSkillIDs: values}
	if err := validateCatalogSearchQuery(q); err != nil {
		t.Fatal(err)
	}
	values = append(values, "seventeenth")
	q.NativeCapabilities.AgentSkillIDs = values
	if ErrorCodeOf(validateCatalogSearchQuery(q)) != CodeInvalidArgument {
		t.Fatal("unbounded selectors")
	}
	q.NativeCapabilities = &NativeCapabilitiesFilter{AgentSkillIDs: []string{strings.Repeat("🙂", 256)}}
	if err := validateCatalogSearchQuery(q); err != nil {
		t.Fatal("rune bound treated as bytes", err)
	}
	q.NativeCapabilities.AgentSkillIDs = make([]string, 16)
	for i := range q.NativeCapabilities.AgentSkillIDs {
		q.NativeCapabilities.AgentSkillIDs[i] = fmt.Sprint(i) + strings.Repeat("<", 250)
	}
	if ErrorCodeOf(validateCatalogSearchQuery(q)) != CodeInvalidArgument {
		t.Fatal("encoded aggregate budget ignored")
	}
}

func TestNativeCapabilityProjectionExactIndependentFields(t *testing.T) {
	raw := []byte(`{"skills":[{"id":"resolve-invoices","tags":["billing","billing"],"inputModes":["not-a-default"]},{"id":"other","tags":["finance"]}],"default_input_modes":["text/plain"],"defaultOutputModes":["application/json"],"capabilities":{"streaming":false},"extension":{"huge":1e100000,"nul":"\u0000","precise":9007199254740993}}`)
	before := string(raw)
	facts := ProjectNativeCapabilities("Application/A2A-Agent-Card+JSON; charset=utf-8", raw)
	falsity, truth := false, true
	want := NativeCapabilitiesFilter{AgentSkillIDs: []string{"other", "resolve-invoices"}, AgentSkillTags: []string{"billing", "finance"}, AgentDefaultInputModes: []string{"text/plain"}, AgentDefaultOutputModes: []string{"application/json"}, AgentStreaming: &falsity}
	if facts.Unavailable || !reflect.DeepEqual(facts.NativeCapabilitiesFilter, want) || string(raw) != before {
		t.Fatal("native facts or bytes changed", facts)
	}
	if !(&NativeCapabilitiesFilter{AgentSkillIDs: []string{"absent", "resolve-invoices"}, AgentSkillTags: []string{"finance"}, AgentStreaming: &falsity}).matches(facts) {
		t.Fatal("independent OR/AND or false match lost")
	}
	for _, filter := range []NativeCapabilitiesFilter{{AgentSkillIDs: []string{"Resolve-invoices"}}, {AgentSkillTags: []string{"Finance"}}, {AgentDefaultInputModes: []string{"not-a-default"}}, {AgentStreaming: &truth}, {MCPTransports: []string{"http"}}} {
		if filter.matches(facts) {
			t.Fatal("inferred or normalized fact", filter)
		}
	}
	for _, body := range []string{`{}`, `{"capabilities":{}}`, `{"capabilities":{"streaming":null}}`} {
		if (&NativeCapabilitiesFilter{AgentStreaming: &falsity}).matches(ProjectNativeCapabilities("application/a2a-agent-card+json", []byte(body))) {
			t.Fatal("missing/null became false", body)
		}
	}
	streaming := ProjectNativeCapabilities("application/a2a-agent-card+json", []byte(`{"capabilities":{"streaming":true}}`))
	if !(&NativeCapabilitiesFilter{AgentStreaming: &truth}).matches(streaming) || (&NativeCapabilitiesFilter{AgentStreaming: &falsity}).matches(streaming) {
		t.Fatal("explicit true was lost or confused with false", streaming)
	}
	mcp := ProjectNativeCapabilities("application/mcp-server-card+json", []byte(`{"remotes":[{"type":"streamable-http","supportedProtocolVersions":["2025-11-25"]},{"type":"sse","supportedProtocolVersions":["2026-07-28"]}]}`))
	if !(&NativeCapabilitiesFilter{MCPTransports: []string{"streamable-http"}, MCPProtocolVersions: []string{"2026-07-28"}}).matches(mcp) {
		t.Fatal("invented remote tuple correlation", mcp)
	}
	if (&NativeCapabilitiesFilter{MCPTransports: []string{"STREAMABLE-HTTP"}}).matches(mcp) {
		t.Fatal("transport was lowercased")
	}
	if got := ProjectNativeCapabilities("application/vnd.open-agentic-catalog.mcp-tools+json", raw); got.Unavailable || got.agent() || got.mcp() {
		t.Fatal("other representation supplied facts", got)
	}
}

func TestNativeCapabilityProjectionBoundsAndUnavailable(t *testing.T) {
	bad := make([]string, 0, 13)
	bad = append(bad, []string{`null`, `[]`, `{"skills":false}`, `{"skills":[null]}`, `{"skills":[{"id":12}]}`, `{"skills":[{"tags":[null]}]}`, `{"skills":[{"id":"private\u0000value"}]}`, `{"defaultInputModes":"text/plain"}`, `{"capabilities":{"streaming":"false"}}`}...)
	values := make([]string, 257)
	for i := range values {
		values[i] = "x"
	}
	array, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	bad = append(bad, `{"defaultInputModes":`+string(array)+`}`, `{"skills":[{"id":"`+strings.Repeat("🙂", 513)+`"}]}`)
	const prefix = `{"capabilities":{"streaming":false},"unknown":"`
	const suffix = `"}`
	bounded := prefix + strings.Repeat("x", (16<<20)-len(prefix)-len(suffix)) + suffix
	control := ProjectNativeCapabilities("application/a2a-agent-card+json", []byte(bounded))
	if control.Unavailable || control.AgentStreaming == nil || *control.AgentStreaming {
		t.Fatal("valid card at the byte ceiling lost its declaration", control)
	}
	// Trailing whitespace keeps JSON valid and isolates the byte ceiling.
	bad = append(bad, bounded+" ")
	values = make([]string, 65)
	for i := range values {
		values[i] = strings.Repeat("a", 512)
	}
	array, err = json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	bad = append(bad, `{"defaultInputModes":`+string(array)+`}`)
	for _, body := range bad {
		facts := ProjectNativeCapabilities("application/a2a-agent-card+json", []byte(body))
		if !facts.Unavailable || facts.agent() || facts.mcp() {
			t.Fatal("partial or fabricated projection", len(body), facts)
		}
	}
	literal := ProjectNativeCapabilities("application/a2a-agent-card+json", []byte(`{"skills":[{"id":"literal\\u0000"}]}`))
	if literal.Unavailable || !reflect.DeepEqual(literal.AgentSkillIDs, []string{`literal\u0000`}) {
		t.Fatal("literal backslash confused with NUL", literal)
	}
}

func TestNativeCapabilityEachArrayRequiresAnExactAlternative(t *testing.T) {
	agent := NativeCapabilityProjection{NativeCapabilitiesFilter: NativeCapabilitiesFilter{
		AgentSkillIDs: []string{"resolve-invoices"}, AgentSkillTags: []string{"Finance, billing "},
		AgentDefaultInputModes: []string{"Text/Plain"}, AgentDefaultOutputModes: []string{"application/json"},
	}}
	mcp := NativeCapabilityProjection{NativeCapabilitiesFilter: NativeCapabilitiesFilter{
		MCPTransports: []string{"streamable-http"}, MCPProtocolVersions: []string{"2026-07-28"},
	}}
	for _, tc := range []struct {
		name, literal string
		facts         NativeCapabilityProjection
		filter        func([]string) NativeCapabilitiesFilter
	}{
		{"skill IDs", "resolve-invoices", agent, func(v []string) NativeCapabilitiesFilter { return NativeCapabilitiesFilter{AgentSkillIDs: v} }},
		{"skill tags", "Finance, billing ", agent, func(v []string) NativeCapabilitiesFilter { return NativeCapabilitiesFilter{AgentSkillTags: v} }},
		{"input modes", "Text/Plain", agent, func(v []string) NativeCapabilitiesFilter { return NativeCapabilitiesFilter{AgentDefaultInputModes: v} }},
		{"output modes", "application/json", agent, func(v []string) NativeCapabilitiesFilter { return NativeCapabilitiesFilter{AgentDefaultOutputModes: v} }},
		{"transports", "streamable-http", mcp, func(v []string) NativeCapabilitiesFilter { return NativeCapabilitiesFilter{MCPTransports: v} }},
		{"protocol versions", "2026-07-28", mcp, func(v []string) NativeCapabilitiesFilter { return NativeCapabilitiesFilter{MCPProtocolVersions: v} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, selected := range []struct {
				values []string
				want   bool
			}{
				{[]string{"not-declared", tc.literal}, true},
				{[]string{"not-declared"}, false},
				{[]string{tc.literal + " "}, false},
			} {
				filter := tc.filter(selected.values)
				if got := filter.matches(tc.facts); got != selected.want {
					t.Fatal("exact OR matching", selected.values, got, selected.want)
				}
				unavailable := tc.facts
				unavailable.Unavailable = true
				if filter.matches(unavailable) {
					t.Fatal("unavailable evidence supplied a match")
				}
			}
		})
	}
}

func TestNativeCapabilityMemoryUsesOneReleasedAuthorizedCard(t *testing.T) {
	repo := NewMemoryRepository()
	const tenant = "tenant"
	rep := Representation{Role: RepresentationPrimary, MediaType: "application/a2a-agent-card+json", ProfileID: "a2a-agent-card-1.0.1-3303592", ValidationState: "valid", ArtifactID: "card"}
	repo.documents[tenant+"/card"] = DocumentBlob{Content: []byte(`{"skills":[{"id":"resolve-invoices"}],"capabilities":{"streaming":false},"supportedInterfaces":[{"protocolBinding":"JSONRPC"}]}`)}
	repo.versionRevisions["released"] = VersionRevision{TenantID: tenant, Representations: []Representation{rep}}
	record := CatalogSummary{Resource: CatalogResourceSummary{Kind: KindAgent}, VersionRevision: CatalogRevisionSummary{ID: "released"}, Listing: CatalogListingSummary{NamespaceID: "namespace"}}
	reader := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenant, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	ctx, _, err := identity.WithCollectionContext(reader)
	if err != nil {
		t.Fatal(err)
	}
	falsity := false
	query := SearchQuery{NativeCapabilities: &NativeCapabilitiesFilter{AgentSkillIDs: []string{"resolve-invoices"}, AgentStreaming: &falsity}, AgentProtocol: "JSONRPC"}
	got, err := repo.nativeCapabilityRecordsLocked(ctx, tenant, []CatalogSummary{record}, query)
	if err != nil || len(got) != 1 {
		t.Fatal("retained blob positive control", got, err)
	}
	other := rep
	other.ArtifactID = ""
	other.Data = json.RawMessage(`{"skills":[{"id":"resolve-invoices"}],"capabilities":{"streaming":false}}`)
	rep.ArtifactID = ""
	rep.Data = json.RawMessage(`{"supportedInterfaces":[{"protocolBinding":"JSONRPC"}],"skills":[{"id":"other"}]}`)
	repo.versionRevisions["released"] = VersionRevision{TenantID: tenant, Representations: []Representation{rep, other}}
	if got, err = repo.nativeCapabilityRecordsLocked(ctx, tenant, []CatalogSummary{record}, query); err != nil || len(got) != 0 {
		t.Fatal("mixed declarations from separate cards", got, err)
	}
	query.AgentProtocol = ""
	other.Role = RepresentationMetadata
	repo.versionRevisions["released"] = VersionRevision{TenantID: tenant, Representations: []Representation{rep, other}}
	if got, err = repo.nativeCapabilityRecordsLocked(ctx, tenant, []CatalogSummary{record}, query); err != nil || len(got) != 0 {
		t.Fatal("supporting card supplied facts", got, err)
	}
	repo.versionRevisions["released"] = VersionRevision{TenantID: "foreign", Representations: []Representation{other}}
	if got, err = repo.nativeCapabilityRecordsLocked(ctx, tenant, []CatalogSummary{record}, query); err != nil || len(got) != 0 {
		t.Fatal("foreign revision supplied facts", got, err)
	}
}

func TestNativeCapabilityCursorBindingRetainsLiteralsAndBooleanPresence(t *testing.T) {
	streaming := false
	q := SearchQuery{Origins: []SearchResultType{SearchResultLocal}, NativeCapabilities: &NativeCapabilitiesFilter{AgentSkillIDs: []string{"z", "A, exact "}, AgentStreaming: &streaming}}
	actor := identity.Actor{TenantID: "tenant", Subject: "reader"}
	service := &Service{cursorKey: []byte("0123456789abcdef0123456789abcdef"), policy: "native"}
	original, err := service.searchCursorBinding(t.Context(), actor, q)
	if err != nil {
		t.Fatal(err)
	}
	sorted := q
	sorted.NativeCapabilities = q.NativeCapabilities.Clone()
	slices.Reverse(sorted.NativeCapabilities.AgentSkillIDs)
	same, err := service.searchCursorBinding(t.Context(), actor, sorted)
	if err != nil || original != same {
		t.Fatal("array ordering changed cursor", err)
	}
	for _, changed := range []*NativeCapabilitiesFilter{{AgentSkillIDs: q.NativeCapabilities.AgentSkillIDs}, {AgentSkillIDs: []string{"z", "A, exact"}, AgentStreaming: &streaming}, {AgentSkillIDs: []string{"Z", "A, exact "}, AgentStreaming: &streaming}} {
		next := q
		next.NativeCapabilities = changed
		binding, err := service.searchCursorBinding(t.Context(), actor, next)
		if err != nil || binding == original {
			t.Fatal("changed native semantics reused cursor", err)
		}
	}
	if q.NativeCapabilities.AgentSkillIDs[0] != "z" || *q.NativeCapabilities.AgentStreaming {
		t.Fatal("cursor normalization mutated caller")
	}
}
