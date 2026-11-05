// Hybrid response codecs preserve effective retrieval without adding client-side
// inference or treating optional indexing as a new discovery protocol.
package clientcompat_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/api"
	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
)

// TestHybridSearchResponseCodecs checks the maintained disabled, enabled,
// indexing and outage examples against both OpenAPI and generated round trips.
func TestHybridSearchResponseCodecs(t *testing.T) {
	raw, err := api.Files.ReadFile("examples/hybrid-search.json")
	must(t, err)
	var examples struct {
		Responses []json.RawMessage `json:"responses"`
	}
	must(t, json.Unmarshal(raw, &examples))
	if len(examples.Responses) != 4 {
		t.Fatal("expected all four retrieval examples")
	}
	schema := component(t, "CatalogSearchResponse")
	for i, response := range examples.Responses {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			roundTrip[client.CatalogSearchResponse](t, schema, response)
		})
	}
	// Explicit false is meaningful too: optional pointer fields must not drop it.
	for _, partial := range []bool{false, true} {
		var response map[string]json.RawMessage
		must(t, json.Unmarshal(examples.Responses[1], &response))
		response["partialIndex"] = json.RawMessage(strconv.FormatBool(partial))
		encoded, err := json.Marshal(response)
		must(t, err)
		roundTrip[client.CatalogSearchResponse](t, schema, encoded)
	}
}
