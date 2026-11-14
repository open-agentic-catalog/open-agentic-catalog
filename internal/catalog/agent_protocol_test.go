package catalog

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestDeclaredAgentProtocolsExactValues(t *testing.T) {
	raw := []byte(`{"supportedInterfaces":[{"protocolBinding":"JSONRPC"},{"protocolBinding":"HTTP+JSON"},{"protocolBinding":"JSONRPC"},{"protocolBinding":"私有+V1 "}]}`)
	if got := declaredAgentProtocols("application/a2a-agent-card+json", raw); !reflect.DeepEqual(got, []string{"JSONRPC", "HTTP+JSON", "私有+V1 "}) {
		t.Fatal(got)
	}
	if got := declaredAgentProtocols("application/a2a-agent-card+json", []byte(`{"supported_interfaces":[{"protocol_binding":"JSONRPC"}]}`)); !reflect.DeepEqual(got, []string{"JSONRPC"}) {
		t.Fatal("native ProtoJSON spelling lost", got)
	}
	if got := declaredAgentProtocols("Application/A2A-Agent-Card+JSON; charset=utf-8", []byte(`{"supportedInterfaces":[{"protocolBinding":"JSONRPC"},{"protocol_binding":"HTTP+JSON"}]}`)); !reflect.DeepEqual(got, []string{"JSONRPC", "HTTP+JSON"}) {
		t.Fatal("accepted media type or per-interface ProtoJSON spelling lost", got)
	}
	for _, test := range []struct{ media, body string }{{"application/json", string(raw)}, {"application/a2a-agent-card+json", "{"}, {"application/a2a-agent-card+json", `{"supportedInterfaces":null}`}, {"application/a2a-agent-card+json", `{"supportedInterfaces":[{"protocolBinding":3}]}`}} {
		if got := declaredAgentProtocols(test.media, []byte(test.body)); len(got) != 0 {
			t.Fatal("invented protocol", got)
		}
	}
}

func TestAgentProtocolQueryAndCursorBinding(t *testing.T) {
	q := SearchQuery{AgentProtocol: " 私有+V1 ", Origins: []SearchResultType{SearchResultLocal}}
	if err := validateCatalogSearchQuery(q); err != nil {
		t.Fatal(err)
	}
	if err := validateCatalogSearchQuery(SearchQuery{AgentProtocol: "\ufeff", Origins: q.Origins}); err != nil {
		t.Fatal("opaque binding treated as whitespace", err)
	}
	if normalizeCatalogSearchQuery(q).AgentProtocol != q.AgentProtocol {
		t.Fatal("literal normalized")
	}
	boundary := SearchQuery{AgentProtocol: strings.Repeat("🙂", 256), Origins: q.Origins}
	if err := validateCatalogSearchQuery(boundary); err != nil {
		t.Fatal("character bound was treated as a byte bound", err)
	}
	if err := ValidateSavedSearch(boundary); err != nil {
		t.Fatal("saved criteria rejected the same character bound", err)
	}
	for _, changed := range []SearchQuery{{AgentProtocol: "HTTP+JSON"}, {AgentProtocol: "HTTP+JSON", Origins: []SearchResultType{SearchResultIndexed}}, {AgentProtocol: "HTTP+JSON", Origins: []SearchResultType{SearchResultLocal, SearchResultLive}}, {AgentProtocol: strings.Repeat("x", 257), Origins: q.Origins}, {AgentProtocol: strings.Repeat("🙂", 257), Origins: q.Origins}, {AgentProtocol: " ", Origins: q.Origins}, {AgentProtocol: "\xff", Origins: q.Origins}, {AgentProtocol: "x\x00", Origins: q.Origins}} {
		if ErrorCodeOf(validateCatalogSearchQuery(changed)) != CodeInvalidArgument {
			t.Fatal("accepted invalid query", changed)
		}
		if ErrorCodeOf(ValidateSavedSearch(changed)) != CodeInvalidArgument {
			t.Fatal("saved criteria accepted invalid query", changed)
		}
	}
	service := newUnifiedSearchTestService(t, &unifiedSearchRepository{}, unifiedFederatedSearcher{})
	actor, _ := requirePermission(unifiedReaderContext(), "resource.read", "")
	before, err := service.local.unifiedSearchCursorBinding(unifiedReaderContext(), actor, q)
	if err != nil {
		t.Fatal(err)
	}
	q.AgentProtocol = "JSONRPC"
	after, err := service.local.unifiedSearchCursorBinding(unifiedReaderContext(), actor, q)
	if err != nil || before == after {
		t.Fatal("protocol not bound to continuation", err)
	}
	if err := ValidateSavedSearch(q); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryAgentProtocolUsesReleasedPrimaryContent(t *testing.T) {
	repo := NewMemoryRepository()
	const tenant = "tenant"
	rep := Representation{Role: RepresentationPrimary, MediaType: "application/a2a-agent-card+json", ProfileID: "a2a-agent-card-1.0.1-3303592", ValidationState: "valid", ArtifactID: "document"}
	repo.documents[tenant+"/document"] = DocumentBlob{Content: []byte(`{"supportedInterfaces":[{"protocolBinding":"HTTP+JSON"}]}`)}
	repo.versionRevisions["released"] = VersionRevision{TenantID: tenant, Representations: []Representation{rep}}
	record := CatalogSummary{Resource: CatalogResourceSummary{Kind: KindAgent}, VersionRevision: CatalogRevisionSummary{ID: "released"}, Listing: CatalogListingSummary{NamespaceID: "namespace"}}
	records := []CatalogSummary{record}
	reader := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenant, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	reader, _, err := identity.WithCollectionContext(reader)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.agentProtocolRecordsLocked(reader, tenant, records, "HTTP+JSON")
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	foreign := repo.versionRevisions["released"]
	foreign.TenantID = "other-tenant"
	repo.versionRevisions["released"] = foreign
	if selected, err := repo.agentProtocolRecordsLocked(reader, tenant, records, "HTTP+JSON"); err != nil || len(selected) != 0 {
		t.Fatal("foreign revision supplied a declaration", selected, err)
	}
	foreign.TenantID = tenant
	repo.versionRevisions["released"] = foreign
	if ordinary, err := repo.agentProtocolRecordsLocked(reader, tenant, records, ""); err != nil || !reflect.DeepEqual(ordinary, records) {
		t.Fatal("unfiltered metadata changed", ordinary, err)
	}
	records[0].Resource.Kind = KindMCPServer
	if otherKind, err := repo.agentProtocolRecordsLocked(reader, tenant, records, "HTTP+JSON"); err != nil || len(otherKind) != 0 {
		t.Fatal("non-Agent native metadata contributed", otherKind, err)
	}
	records[0].Resource.Kind = KindAgent
	inline := rep
	inline.ArtifactID = ""
	inline.Data = []byte(`{"supportedInterfaces":[{"protocolBinding":"JSONRPC"}]}`)
	repo.versionRevisions["released"] = VersionRevision{TenantID: tenant, Representations: []Representation{inline}}
	if selected, err := repo.agentProtocolRecordsLocked(reader, tenant, records, "JSONRPC"); err != nil || len(selected) != 1 {
		t.Fatal("inline declaration lost", selected, err)
	}
	if selected, err := repo.agentProtocolRecordsLocked(reader, tenant, records, "HTTP+JSON"); err != nil || len(selected) != 0 {
		t.Fatal("unrelated blob supplied a declaration", selected, err)
	}
	missing := rep
	missing.ArtifactID = "missing"
	repo.versionRevisions["released"] = VersionRevision{TenantID: tenant, Representations: []Representation{missing}}
	if selected, err := repo.agentProtocolRecordsLocked(reader, tenant, records, "HTTP+JSON"); err != nil || len(selected) != 0 {
		t.Fatal("missing blob invented a declaration", selected, err)
	}
	repo.versionRevisions["released"] = VersionRevision{TenantID: tenant, Representations: []Representation{rep}}
	cancelled, cancel := context.WithCancel(reader)
	cancel()
	if selected, err := repo.agentProtocolRecordsLocked(cancelled, tenant, records, "HTTP+JSON"); !errors.Is(err, context.Canceled) || selected != nil {
		t.Fatal("failed admission disclosed selection", selected, err)
	}
	for _, change := range []func(){func() { rep.Role = RepresentationDocumentation }, func() { rep.Role = RepresentationPrimary; rep.ValidationState = "invalid" }, func() { rep.ValidationState = "valid"; rep.ProfileID = "other" }} {
		change()
		repo.versionRevisions["released"] = VersionRevision{TenantID: tenant, Representations: []Representation{rep}}
		got, err = repo.agentProtocolRecordsLocked(reader, tenant, records, "HTTP+JSON")
		if err != nil || len(got) != 0 {
			t.Fatal("unsupported representation matched", err)
		}
	}
	rep.ProfileID = "a2a-agent-card-1.0.1-3303592"
	repo.versionRevisions["released"] = VersionRevision{TenantID: tenant, Representations: []Representation{rep}}
	noContent := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenant, Subject: "no-content", Roles: []identity.Role{identity.RoleServiceClient}})
	noContent, _, err = identity.WithCollectionContext(noContent)
	if err != nil {
		t.Fatal(err)
	}
	got, err = repo.agentProtocolRecordsLocked(noContent, tenant, records, "HTTP+JSON")
	if err != nil || len(got) != 0 {
		t.Fatal("content authority bypassed", err)
	}
	repo.versionRevisions["draft"] = VersionRevision{TenantID: tenant, Representations: []Representation{{Data: []byte(`{"supportedInterfaces":[{"protocolBinding":"GRPC"}]}`)}}}
	got, err = repo.agentProtocolRecordsLocked(reader, tenant, records, "GRPC")
	if err != nil || len(got) != 0 {
		t.Fatal("draft contributed", err)
	}
}
