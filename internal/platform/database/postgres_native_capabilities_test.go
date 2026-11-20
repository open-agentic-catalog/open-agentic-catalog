package database

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"reflect"
	"strings"
	"testing"
)

func TestNativeCapabilitySQLUsesBoundPredicatesAndCurrentAuthority(t *testing.T) {
	streaming := false
	q := catalog.SearchQuery{AgentProtocol: "JSONRPC' OR true --", NativeCapabilities: &catalog.NativeCapabilitiesFilter{AgentSkillIDs: []string{"invoice' OR true --"}, AgentStreaming: &streaming}}
	statement, args := localCardQuery(catalogSearchPageSQL, make([]any, 15), q)
	if len(args) != 18 || !reflect.DeepEqual(args[15], q.NativeCapabilities.AgentSkillIDs) || args[16] != false || args[17] != q.AgentProtocol {
		t.Fatal("wrong parameter binding", args)
	}
	for _, term := range []string{"resource.read_content", "CASE WHEN", "native_rep.version_revision_id=release.version_revision_id", "native_rep.tenant_id=release.tenant_id", "native_rep.role='primary'", "native_rep.validation_state='valid'", "native_rep.document_blob_id IS NOT NULL", "agentStreaming", "$16::text[]", "$17::boolean", "ARRAY[$18::text]"} {
		if !strings.Contains(statement, term) {
			t.Fatal("missing native preselection", term)
		}
	}
	if strings.Contains(statement, q.AgentProtocol) || strings.Contains(statement, q.NativeCapabilities.AgentSkillIDs[0]) {
		t.Fatal("interpolated literal")
	}
	ordinary := []any{"tenant"}
	same, bound := localCardQuery(catalogSearchPageSQL, ordinary, catalog.SearchQuery{})
	if same != catalogSearchPageSQL || !reflect.DeepEqual(bound, ordinary) {
		t.Fatal("ordinary search changed")
	}
}
