package controlclient

import (
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"strings"
	"testing"
)

func TestExplicitNativeSearchPreservesFalseAndLocalAdmission(t *testing.T) {
	streaming := false
	q := Search{Limit: 1, Origins: []catalog.SearchResultType{catalog.SearchResultLocal}, NativeCapabilities: &catalog.NativeCapabilitiesFilter{AgentStreaming: &streaming, AgentSkillIDs: []string{"invoice, literal "}}}
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"agentStreaming":false`) || !strings.Contains(string(raw), `"invoice, literal "`) {
		t.Fatal("native criteria omitted or normalized", string(raw))
	}
	q.Origins = nil
	if err := q.Validate(); err == nil {
		t.Fatal("omitted origins broadened private search")
	}
	q.Origins = []catalog.SearchResultType{catalog.SearchResultLocal}
	q.Kinds = []catalog.Kind{catalog.KindSkill}
	if err := q.Validate(); err == nil {
		t.Fatal("incompatible kind accepted")
	}
}
