package database

import (
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestAgentProtocolSQLBindingAndAuthority(t *testing.T) {
	q := catalog.SearchQuery{AgentProtocol: "HTTP+JSON' OR true --"}
	sql, args := agentProtocolQuery(catalogSearchPageSQL, make([]any, 15), q)
	if len(args) != 16 || args[15] != q.AgentProtocol || strings.Contains(sql, q.AgentProtocol) || !strings.Contains(sql, "ARRAY[$16::text]") || !strings.Contains(sql, "CASE WHEN") || !strings.Contains(sql, "resource.read_content") || !strings.Contains(sql, "agent_rep.version_revision_id=release.version_revision_id") || !strings.Contains(sql, "agent_rep.role='primary'") {
		t.Fatal("unbound or unauthorized native selection")
	}
	ordinary := []any{"tenant"}
	unchanged, bound := agentProtocolQuery(catalogSearchPageSQL, ordinary, catalog.SearchQuery{})
	if unchanged != catalogSearchPageSQL || !reflect.DeepEqual(ordinary, bound) {
		t.Fatal("ordinary statement changed")
	}
}
