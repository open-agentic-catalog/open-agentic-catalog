package publication

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

func TestDisclosedFilterChecksEverySupportedField(t *testing.T) {
	raw := `{"identifier":"urn:air:peer.example:agent:weather","displayName":"Weather","type":"application/a2a-agent-card+json","representativeQueries":["Find weather"],"capabilities":["forecast"],"description":"Weather forecasts","tags":["weather"],"version":"1.0.0","updatedAt":"2026-09-25T12:00:00Z","metadata":{"region":"eu","count":9007199254740993,"enabled":true}}`
	var entry Entry
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&entry); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]string{
		"identifier": "urn:air:peer.example:agent:weather", "publisher": "peer.example", "displayName": "Weather", "type": "application/a2a-agent-card+json",
		"representativeQueries": "Find weather", "capabilities": "forecast", "description": "Weather forecasts", "tags": "weather", "version": "1.0.0", "updatedAt": "2026-09-25T12:00:00Z",
		"metadata.region": "eu", "metadata.count": "9007199254740993", "metadata.enabled": "true",
	} {
		t.Run(field, func(t *testing.T) {
			if !matchesDisclosedFilterForTest(t, entry, fields, map[string]any{field: []string{"not-present", value}}) {
				t.Fatal("matching OR filter rejected")
			}
			if matchesDisclosedFilterForTest(t, entry, fields, map[string]any{field: "not-present"}) {
				t.Fatal("disclosed contradiction accepted")
			}
		})
	}
	if matchesDisclosedFilterForTest(t, entry, fields, map[string]any{"publisher": "peer.example", "tags": "not-present"}) {
		t.Fatal("cross-field AND was not enforced")
	}
}

func TestDisclosedFilterDistinguishesEmptyAndMissingFields(t *testing.T) {
	entry := Entry{Identifier: "urn:air:peer.example:agent:weather", Metadata: map[string]any{"region": nil}}
	fields := map[string]json.RawMessage{"identifier": json.RawMessage(`"urn:air:peer.example:agent:weather"`), "tags": json.RawMessage(`[]`), "displayName": json.RawMessage(`""`), "metadata": json.RawMessage(`{"region":null}`)}
	for _, field := range []string{"type", "capabilities", "representativeQueries", "description", "version", "updatedAt", "metadata.missing"} {
		if !matchesDisclosedFilterForTest(t, entry, fields, map[string]any{field: "expected"}) {
			t.Fatalf("omitted %s treated as contradiction", field)
		}
	}
	for _, field := range []string{"tags", "displayName", "metadata.region", "publisher"} {
		if matchesDisclosedFilterForTest(t, entry, fields, map[string]any{field: "expected"}) {
			t.Fatalf("present %s treated as omitted", field)
		}
	}
}

func TestDisclosedMetadataFilterDistinguishesNullFromLiteralStrings(t *testing.T) {
	for _, tc := range []struct {
		name, member, wanted        string
		wantDisclosed, wantSnapshot bool
	}{
		{"null", `,"metadata":{"region":null}`, "<nil>", false, false},
		{"literal nil string", `,"metadata":{"region":"<nil>"}`, "<nil>", true, true},
		{"ordinary string", `,"metadata":{"region":"eu"}`, "eu", true, true},
		{"omitted key", `,"metadata":{}`, "<nil>", true, false},
		{"omitted metadata", "", "<nil>", true, false},
		{"false boolean", `,"metadata":{"region":false}`, "false", true, true},
		{"exact integer", `,"metadata":{"region":9007199254740993}`, "9007199254740993", true, true},
		{"adjacent integer", `,"metadata":{"region":9007199254740993}`, "9007199254740992", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"identifier":"urn:air:peer.example:agent:weather"` + tc.member + `}`
			if err := standards.ValidateARDEntryFields([]byte(raw)); err != nil {
				t.Fatalf("control is not a schema-valid sparse entry: %v", err)
			}
			var entry Entry
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&entry); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &fields); err != nil {
				t.Fatal(err)
			}
			for _, values := range []any{tc.wanted, []string{"not-present", tc.wanted}} {
				filter, err := ResolveSearchFilter(SearchQuery{Filter: map[string]any{"metadata.region": values, "publisher": "peer.example"}})
				if err != nil {
					t.Fatal(err)
				}
				if got := matchesDisclosedFilterForTest(t, entry, fields, filter); got != tc.wantDisclosed {
					t.Fatalf("disclosed match=%t want=%t for %s, filter=%v", got, tc.wantDisclosed, raw, filter)
				}
				if got := matchFilter(entry, filter, nil); got != tc.wantSnapshot {
					t.Fatalf("snapshot match=%t want=%t for %s, filter=%v", got, tc.wantSnapshot, raw, filter)
				}
				filter["publisher"] = "other.example"
				if matchesDisclosedFilterForTest(t, entry, fields, filter) || matchFilter(entry, filter, nil) {
					t.Fatal("matching metadata bypassed cross-field AND")
				}
			}
		})
	}
}

func TestResolveSearchFilterPreservesCallerAndRejectsInvalidTerms(t *testing.T) {
	query := SearchQuery{Context: map[string]any{"kind": "https://agenticresourcediscovery.org/ns#mediaType"}, Filter: map[string]any{"kind": "application/a2a-agent-card+json"}}
	filter, err := ResolveSearchFilter(query)
	if err != nil || filter["type"] != "application/a2a-agent-card+json" || len(filter) != 1 || query.Filter["kind"] == nil || query.Filter["type"] != nil {
		t.Fatalf("resolved=%v original=%v err=%v", filter, query.Filter, err)
	}
	query.Filter["unsupported"] = "value"
	if _, err := ResolveSearchFilter(query); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("invalid filter error=%v", err)
	}
}

func matchesDisclosedFilterForTest(t *testing.T, entry Entry, fields map[string]json.RawMessage, filter map[string]any) bool {
	t.Helper()
	matches, err := MatchDisclosedFilter(t.Context(), entry, fields, filter)
	if err != nil {
		t.Fatal(err)
	}
	return matches
}
