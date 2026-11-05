package publication

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const nativeAgentData = `{"defaultInputModes":["text/plain","application/json","text/plain"],"defaultOutputModes":["text/plain","text/plain"],"skills":[{"id":"forecast","tags":["weather","weather"],"inputModes":["image/png"],"outputModes":["image/png"]},{"id":"forecast","tags":["Weather"]},{"id":"report","tags":["report","weather"]}],"capabilities":{"streaming":false},"supportedInterfaces":[{"protocolBinding":"JSONRPC","protocolVersion":"1.0"},{"protocolBinding":"HTTP+JSON","protocolVersion":"2.0"}]}`
const nativeMCPData = `{"remotes":[{"type":"streamable-http","supportedProtocolVersions":["2025-11-25","2025-11-25","2025-06-18"]},{"type":"sse","supportedProtocolVersions":["2025-11-25"]}]}`

func TestNativePathsUseDeclaredMediaAndExactScalarShapes(t *testing.T) {
	for field, expected := range map[string][]string{
		"data.skills.id":                           {"forecast", "report"},
		"data.skills.tags":                         {"Weather", "report", "weather"},
		"data.defaultInputModes":                   {"application/json", "text/plain"},
		"data.defaultOutputModes":                  {"text/plain"},
		"data.capabilities.streaming":              {"false"},
		"data.supportedInterfaces.protocolBinding": {"HTTP+JSON", "JSONRPC"},
		"data.supportedInterfaces.protocolVersion": {"1.0", "2.0"},
		"data.remotes.type":                        {"sse", "streamable-http"},
		"data.remotes.supportedProtocolVersions":   {"2025-06-18", "2025-11-25"},
	} {
		t.Run(field, func(t *testing.T) {
			media := nativePaths[field]
			data := nativeAgentData
			if media == adapter.MediaMCPServerCard {
				data = nativeMCPData
			}
			entry := Entry{Type: media + "; charset=utf-8", Data: json.RawMessage(data)}
			before := string(entry.Data)
			projection, err := projectNativeEntry(t.Context(), entry, []string{field})
			if err != nil || !projection[field].present || !reflect.DeepEqual(projection[field].values, expected) || string(entry.Data) != before {
				t.Fatalf("native values = %+v / %v, want %v; data changed=%t", projection, err, expected, string(entry.Data) != before)
			}
			if !matchFilter(entry, map[string]any{field: []string{"absent", expected[0]}}, projection) || matchFilter(entry, map[string]any{field: "absent"}, projection) {
				t.Fatal("OR/exact-value comparison failed")
			}
			for _, other := range []string{"", adapter.MediaMCPRegistry, "application/agent-plugins+zip", adapter.MediaSkillMarkdownARD, "invalid"} {
				entry.Type = other
				projected, err := projectNativeEntry(t.Context(), entry, []string{field})
				if err != nil || len(projected[field].values) != 0 || matchFilter(entry, map[string]any{field: expected[0]}, projected) {
					t.Fatalf("unrelated type %q supplied facts: %+v / %v", other, projected, err)
				}
			}
			entry.Type, entry.Data, entry.URL = media, nil, "https://no-fetch.invalid/card"
			projection, err = projectNativeEntry(t.Context(), entry, []string{field})
			if err != nil || projection[field].present || len(projection[field].values) != 0 {
				t.Fatal("URL-only entry gained native facts")
			}
		})
	}
}

func TestNativeStringArrayDeclarationsAndSparseAdmission(t *testing.T) {
	for _, tc := range []struct {
		field, data string
		present     bool
		values      []string
		invalid     bool
	}{
		{"data.defaultInputModes", `{}`, false, nil, false},
		{"data.defaultInputModes", `{"default_input_modes":["text/plain"]}`, false, nil, false},
		{"data.defaultInputModes", `{"defaultInputModes":[]}`, true, nil, false},
		{"data.defaultInputModes", `{"defaultInputModes":null}`, true, nil, false},
		{"data.defaultInputModes", `{"defaultInputModes":["text/plain","Text/Plain","text/plain"]}`, true, []string{"Text/Plain", "text/plain"}, false},
		{"data.defaultInputModes", `{"defaultInputModes":"text/plain"}`, false, nil, true},
		{"data.defaultInputModes", `{"defaultInputModes":{}}`, false, nil, true},
		{"data.defaultInputModes", `{"defaultInputModes":["text/plain",null]}`, false, nil, true},
		{"data.defaultInputModes", `{"defaultInputModes":["text/plain",42]}`, false, nil, true},
		{"data.defaultOutputModes", `{}`, false, nil, false},
		{"data.defaultOutputModes", `{"defaultOutputModes":[]}`, true, nil, false},
		{"data.defaultOutputModes", `{"defaultOutputModes":null}`, true, nil, false},
		{"data.defaultOutputModes", `{"defaultOutputModes":["text/plain","text/plain"],"extra":1e1000}`, true, []string{"text/plain"}, false},
		{"data.defaultOutputModes", `{"defaultInputModes":42,"defaultOutputModes":["text/plain"]}`, true, []string{"text/plain"}, false},
		{"data.defaultOutputModes", `{"defaultOutputModes":true}`, false, nil, true},
		{"data.defaultOutputModes", `{"defaultOutputModes":[["text/plain"]]}`, false, nil, true},
		{"data.skills.tags", `{}`, false, nil, false},
		{"data.skills.tags", `{"skills":[{}]}`, false, nil, false},
		{"data.skills.tags", `{"skills":[]}`, true, nil, false},
		{"data.skills.tags", `{"skills":null}`, true, nil, false},
		{"data.skills.tags", `{"skills":[{"tags":null}]}`, true, nil, false},
		{"data.skills.tags", `{"skills":[{"tags":[]}]}`, true, nil, false},
		{"data.skills.tags", `{"skills":[{}, {"tags":["text/plain","text/plain"]},{"tags":["Text/Plain","text/plain"]}]}`, true, []string{"Text/Plain", "text/plain"}, false},
		{"data.skills.tags", `{"skills":[{"tags":"text/plain"}]}`, false, nil, true},
		{"data.skills.tags", `{"skills":[{"tags":["text/plain",false]}]}`, false, nil, true},
		{"data.skills.tags", `{"skills":[{"tags":["text/plain"]},42]}`, false, nil, true},
	} {
		t.Run(tc.field+"/"+tc.data, func(t *testing.T) {
			entry := Entry{Type: adapter.MediaA2AAgentCard, Data: json.RawMessage(tc.data)}
			projection, err := projectNativeEntry(t.Context(), entry, []string{tc.field})
			if tc.invalid {
				if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
					t.Fatalf("invalid selected shape accepted: %+v / %v", projection, err)
				}
				return
			}
			got := projection[tc.field]
			if err != nil || got.present != tc.present || !reflect.DeepEqual(got.values, tc.values) {
				t.Fatalf("projection = %+v / %v, want %v, present=%t", projection, err, tc.values, tc.present)
			}
			filter := map[string]any{tc.field: "text/plain"}
			matched := slices.Contains(tc.values, "text/plain")
			if matchFilter(entry, filter, projection) != matched {
				t.Fatal("local matching invented a value")
			}
			disclosed, err := MatchDisclosedFilter(t.Context(), entry, map[string]json.RawMessage{"data": entry.Data, "type": json.RawMessage(`"application/a2a-agent-card+json"`)}, filter)
			if err != nil || disclosed != (!tc.present || matched) || string(entry.Data) != tc.data {
				t.Fatalf("sparse admission=%t error=%v; evidence changed=%t", disclosed, err, string(entry.Data) != tc.data)
			}
		})
	}
}

func TestNativeDisclosureDistinguishesMissingFalseNullAndInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		data    string
		present bool
		values  []string
		invalid bool
	}{
		{`{}`, false, nil, false}, {`{"capabilities":{}}`, false, nil, false},
		{`{"capabilities":{"streaming":false}}`, true, []string{"false"}, false},
		{`{"capabilities":{"streaming":true}}`, true, []string{"true"}, false},
		{`{"capabilities":{"streaming":null}}`, true, nil, false},
		{`{"capabilities":null}`, true, nil, false}, {`null`, true, nil, false},
		{`{"capabilities":{"streaming":"false"}}`, false, nil, true},
		{`{"capabilities":{"streaming":0}}`, false, nil, true},
		{`{"capabilities":{"streaming":1e1000}}`, false, nil, true},
		{`{"capabilities":[]}`, false, nil, true}, {`[]`, false, nil, true},
		{`{"capabilities":{"streaming":false,"streaming":true}}`, false, nil, true},
		{`{"capabilities":{"streaming":false}} {}`, false, nil, true},
		{`{"irrelevant":"\ud800"}`, false, nil, true},
	} {
		t.Run(tc.data, func(t *testing.T) {
			entry := Entry{Type: adapter.MediaA2AAgentCard, Data: json.RawMessage(tc.data)}
			projection, err := projectNativeEntry(t.Context(), entry, []string{"data.capabilities.streaming"})
			if tc.invalid {
				if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
					t.Fatalf("invalid data accepted: %v", err)
				}
				return
			}
			got := projection["data.capabilities.streaming"]
			if err != nil || got.present != tc.present || !reflect.DeepEqual(got.values, tc.values) {
				t.Fatalf("projection = %+v / %v", projection, err)
			}
			matches, err := MatchDisclosedFilter(t.Context(), entry, map[string]json.RawMessage{"data": entry.Data, "type": json.RawMessage(`"application/a2a-agent-card+json"`)}, map[string]any{"data.capabilities.streaming": "false"})
			want := !tc.present || slices.Contains(tc.values, "false")
			if err != nil || matches != want {
				t.Fatalf("sparse admission = %t / %v, want %t", matches, err, want)
			}
		})
	}
}

func TestNativeOpaqueNumbersRemainUninterpreted(t *testing.T) {
	for _, tc := range []struct {
		media, data, field, want string
	}{
		{adapter.MediaA2AAgentCard, `{"capabilities":{"streaming":false,"opaque":1e1000},"_meta":{"precise":9007199254740993,"tiny":1e-1000}}`, "data.capabilities.streaming", "false"},
		{adapter.MediaMCPServerCard, `{"remotes":[{"type":"streamable-http","_meta":{"opaque":1e1000}}],"extra":[1e-1000,9007199254740993]}`, "data.remotes.type", "streamable-http"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			entry := Entry{Type: tc.media, Data: json.RawMessage(tc.data)}
			projection, err := projectNativeEntry(t.Context(), entry, []string{tc.field})
			if err != nil || !projection[tc.field].present || !reflect.DeepEqual(projection[tc.field].values, []string{tc.want}) {
				t.Fatalf("opaque numeric extension prevented selected declaration: %+v / %v", projection, err)
			}
			if string(entry.Data) != tc.data {
				t.Fatal("native evidence was changed")
			}
		})
	}
}

func TestNativeArrayPathsDoNotJoinInterfacesOrInventSparseValues(t *testing.T) {
	entry := Entry{Type: adapter.MediaA2AAgentCard, Data: json.RawMessage(nativeAgentData)}
	filter := map[string]any{"data.supportedInterfaces.protocolBinding": "JSONRPC", "data.supportedInterfaces.protocolVersion": "2.0"}
	projection, err := projectNativeEntry(t.Context(), entry, nativeFilterFields(filter))
	if err != nil || !matchFilter(entry, filter, projection) {
		t.Fatal("independent path constraints incorrectly require one interface")
	}
	filter = map[string]any{"data.skills.id": "report", "data.skills.tags": "Weather", "data.defaultInputModes": "text/plain"}
	projection, err = projectNativeEntry(t.Context(), entry, nativeFilterFields(filter))
	if err != nil || !matchFilter(entry, filter, projection) {
		t.Fatal("skill tags incorrectly join one skill or use its mode override")
	}
	for _, field := range []string{"data.defaultInputModes", "data.defaultOutputModes"} {
		projection, err := projectNativeEntry(t.Context(), entry, []string{field})
		if err != nil || matchFilter(entry, map[string]any{field: "image/png"}, projection) {
			t.Fatalf("skill override supplied a card default for %s: %+v / %v", field, projection, err)
		}
	}
	for _, tc := range []struct {
		field, data      string
		present, invalid bool
	}{
		{"data.skills.id", `{"skills":[]}`, true, false},
		{"data.skills.id", `{"skills":[{}]}`, false, false},
		{"data.skills.id", `{"skills":null}`, true, false},
		{"data.skills.id", `{"skills":[{"id":null}]}`, true, false},
		{"data.skills.id", `{"skills":{}}`, false, true},
		{"data.skills.id", `{"skills":[42]}`, false, true},
		{"data.skills.id", `{"skills":[{"id":{}}]}`, false, true},
		{"data.remotes.supportedProtocolVersions", `{"remotes":[{"supportedProtocolVersions":"2025-11-25"}]}`, false, true},
		{"data.remotes.supportedProtocolVersions", `{"remotes":[{"supportedProtocolVersions":["2025-11-25",42]}]}`, false, true},
		{"data.remotes.supportedProtocolVersions", `{"remotes":[{"supportedProtocolVersions":[]}]}`, true, false},
		{"data.remotes.supportedProtocolVersions", `{"remotes":[{"supportedProtocolVersions":null}]}`, true, false},
	} {
		entry := Entry{Type: nativePaths[tc.field], Data: json.RawMessage(tc.data)}
		got, err := projectNativeEntry(t.Context(), entry, []string{tc.field})
		if tc.invalid {
			if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("shape accepted: %s / %v", tc.data, err)
			}
			continue
		}
		if err != nil || got[tc.field].present != tc.present || len(got[tc.field].values) != 0 {
			t.Fatalf("sparse projection %s = %+v / %v", tc.data, got, err)
		}
	}
}

func TestNativeProjectionBoundsAndCancellation(t *testing.T) {
	entry := Entry{Type: adapter.MediaA2AAgentCard, Data: json.RawMessage(nativeAgentData)}
	filter := map[string]any{"data.skills.id": "forecast", "data.defaultInputModes": "text/plain", "data.skills.tags": "weather"}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := nativeStringArray(ctx, []any{"text/plain"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("string-array inspection ignored cancellation: %v", err)
	}
	if _, err := projectNativeEntries(ctx, []Entry{entry}, filter, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if _, err := projectNativeEntries(ctx, []Entry{entry}, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("empty projection ignored cancellation: %v", err)
	}
	data := json.RawMessage(`{"extra":"` + strings.Repeat("x", MaxPublicationDocumentBytes/2) + `"}`)
	large := Entry{Type: adapter.MediaA2AAgentCard, Data: data}
	if _, err := projectNativeEntries(t.Context(), []Entry{large, large}, filter, nil); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
		t.Fatalf("aggregate bound ignored: %v", err)
	}
	large.Data = json.RawMessage(strings.Repeat("x", MaxPublicationDocumentBytes+1))
	if _, err := projectNativeEntry(t.Context(), large, nativeFilterFields(filter)); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
		t.Fatalf("single document bound ignored: %v", err)
	}
	malformed := Entry{Type: adapter.MediaA2AAgentCard, Data: json.RawMessage(`{"skills":[42]}`)}
	if _, err := projectNativeEntries(t.Context(), []Entry{malformed}, filter, nil); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("invalid retained data was reported as a query fault: %v", err)
	}
	if _, err := projectNativeEntries(t.Context(), []Entry{large}, nil, nil); err != nil {
		t.Fatal("unselected native data was parsed")
	}
}

func TestNativeQueryAliasesAndUnsupportedPaths(t *testing.T) {
	context := map[string]any{"body.example": "https://agenticresourcediscovery.org/ns#", "payload.example": "https://agenticresourcediscovery.org/ns#data", "id": "urn:not-a-native-member"}
	filter, err := ResolveSearchFilter(SearchQuery{Context: context, Filter: map[string]any{"body.example:data.skills.id": "forecast"}})
	if err != nil || filter["data.skills.id"] != "forecast" {
		t.Fatalf("leading data alias was not resolved: %v / %v", filter, err)
	}
	for _, field := range []string{"data.skills.id", "https://agenticresourcediscovery.org/ns#data.skills.id", "body.example:data.skills.id", "payload.example.skills.id"} {
		resolved, err := ResolveSearchFilter(SearchQuery{Context: context, Filter: map[string]any{field: "forecast"}})
		if err != nil || resolved["data.skills.id"] != "forecast" {
			t.Fatalf("equivalent native term %s failed: %v / %v", field, resolved, err)
		}
	}
	for _, path := range []string{"defaultInputModes", "defaultOutputModes", "skills.tags"} {
		for _, prefix := range []string{"data.", "https://agenticresourcediscovery.org/ns#data.", "body.example:data.", "payload.example."} {
			resolved, err := ResolveSearchFilter(SearchQuery{Context: context, Filter: map[string]any{prefix + path: []string{"one", "two"}}})
			if err != nil || !reflect.DeepEqual(resolved["data."+path], []string{"one", "two"}) {
				t.Fatalf("new native alias %s failed: %v / %v", prefix+path, resolved, err)
			}
		}
	}
	for _, filter := range []map[string]any{
		{"data.skills.id": "forecast", "body.example:data.skills.id": "forecast"},
		{"data.skills.name": "forecast"}, {"data.skills.id.nested": "forecast"}, {"data.skills[0].id": "forecast"},
		{"data.capabilities.streaming": false},
		{"data.skills.tags.nested": "weather"}, {"data.skills[0].tags": "weather"},
		{"data.default_input_modes": "text/plain"}, {"data.defaultInputModes.extra": "text/plain"},
		{"data.skills.inputModes": "text/plain"},
		{"data.defaultInputModes": "text/plain", "payload.example.defaultInputModes": "text/plain"},
	} {
		if _, err := ResolveSearchFilter(SearchQuery{Context: context, Filter: filter}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("unsupported query accepted: %v / %v", filter, err)
		}
	}
}

func TestNativeActiveSearchAndFacetsShareValues(t *testing.T) {
	records := make([]catalog.CatalogRecord, 0, 3)
	for i, data := range []string{nativeAgentData, `{"defaultInputModes":["text/plain"],"defaultOutputModes":["application/json"],"skills":[{"id":"forecast","tags":["weather"]}],"capabilities":{"streaming":true}}`, nativeMCPData} {
		record := eligiblePublicationRecord()
		name := []string{"one", "two", "mcp"}[i]
		record.Resource.ID, record.Version.ID, record.Release.ID = "resource-"+name, "version-"+name, "release-"+name
		record.Resource.CanonicalIdentifier = "urn:air:catalog.example.com:default:" + name
		record.VersionRevision.Representations[0].Data = json.RawMessage(data)
		if i == 2 {
			record.Resource.Kind = catalog.KindMCPServer
			record.VersionRevision.Representations[0].MediaType = adapter.MediaMCPServerCard
		}
		records = append(records, record)
	}
	manager, _, _ := newPublicationManager(t, records)
	snapshot := activatePublicationSnapshot(t, manager)
	filter := map[string]any{"data.skills.id": "forecast", "data.capabilities.streaming": "false", "data.defaultInputModes": "text/plain", "data.defaultOutputModes": "text/plain", "data.skills.tags": "weather"}
	search, err := manager.Search(t.Context(), "tenant-1", SearchRequest{Query: SearchQuery{Text: "agent", Filter: filter}, Federation: "none", PageSize: 1})
	if err != nil || len(search.Results) != 1 || search.Results[0].Identifier != records[0].Resource.CanonicalIdentifier || search.PageToken != "" {
		t.Fatalf("search = %+v / %v", search, err)
	}
	facets := []ExploreFacetRequest{{Field: "data.skills.id"}, {Field: "data.capabilities.streaming"}, {Field: "data.remotes.type"}, {Field: "data.remotes.supportedProtocolVersions"}, {Field: "data.defaultInputModes"}, {Field: "data.defaultOutputModes"}, {Field: "data.skills.tags"}}
	explore, err := manager.Explore(t.Context(), "tenant-1", ExploreRequest{ResultType: ExploreResultType{Facets: facets}})
	if err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string][]ExploreBucket{
		"data.skills.id":                         {{Value: "forecast", Count: 2}, {Value: "report", Count: 1}},
		"data.capabilities.streaming":            {{Value: "false", Count: 1}, {Value: "true", Count: 1}},
		"data.remotes.type":                      {{Value: "sse", Count: 1}, {Value: "streamable-http", Count: 1}},
		"data.remotes.supportedProtocolVersions": {{Value: "2025-06-18", Count: 1}, {Value: "2025-11-25", Count: 1}},
		"data.defaultInputModes":                 {{Value: "text/plain", Count: 2}, {Value: "application/json", Count: 1}},
		"data.defaultOutputModes":                {{Value: "application/json", Count: 1}, {Value: "text/plain", Count: 1}},
		"data.skills.tags":                       {{Value: "weather", Count: 2}, {Value: "Weather", Count: 1}, {Value: "report", Count: 1}},
	} {
		if !reflect.DeepEqual(explore.Facets[field].Buckets, want) {
			t.Fatalf("facet %s = %+v, want %v", field, explore.Facets[field], want)
		}
	}
	explore, err = manager.Explore(t.Context(), "tenant-1", ExploreRequest{Query: &SearchQuery{Filter: filter}, ResultType: ExploreResultType{Facets: facets}})
	if err != nil || !reflect.DeepEqual(explore.Facets["data.skills.id"].Buckets, []ExploreBucket{{Value: "forecast", Count: 1}, {Value: "report", Count: 1}}) || len(explore.Facets["data.remotes.type"].Buckets) != 0 {
		t.Fatalf("filtered facets = %+v / %v", explore, err)
	}
	retained, err := manager.Active(t.Context(), "tenant-1")
	if err != nil || !reflect.DeepEqual(retained, snapshot) {
		t.Fatal("native discovery rewrote retained evidence")
	}
	request := SearchRequest{Query: SearchQuery{Text: "agent", Filter: map[string]any{"data.defaultInputModes": "text/plain"}}, Federation: "none", PageSize: 1}
	first, err := manager.Search(t.Context(), "tenant-1", request)
	if err != nil || len(first.Results) != 1 || first.PageToken == "" {
		t.Fatalf("new native filter did not retain overflow: %+v / %v", first, err)
	}
	request.PageToken = first.PageToken
	last, err := manager.Search(t.Context(), "tenant-1", request)
	if err != nil || len(last.Results) != 1 || last.PageToken != "" || last.Results[0].Identifier == first.Results[0].Identifier {
		t.Fatalf("new native continuation repeated or lost a result: %+v / %v", last, err)
	}
	request.Query.Filter["data.defaultInputModes"] = "application/json"
	if _, err := manager.Search(t.Context(), "tenant-1", request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("changed native filter reused a cursor: %v", err)
	}
}

func FuzzNativeFields(f *testing.F) {
	f.Add(nativeAgentData, false)
	f.Add(nativeMCPData, true)
	f.Add(`{"skills":[{}]}`, false)
	f.Add(`{"defaultInputModes":[],"defaultOutputModes":null,"skills":[{"tags":["a","a"]}]}`, false)
	f.Add(`{"defaultInputModes":["text/plain",42],"skills":[{"tags":[null]}]}`, false)
	f.Fuzz(func(t *testing.T, data string, mcp bool) {
		if len(data) > 65536 {
			t.Skip()
		}
		media := adapter.MediaA2AAgentCard
		if mcp {
			media = adapter.MediaMCPServerCard
		}
		fields := make([]string, 0, len(nativePaths))
		for field := range nativePaths {
			fields = append(fields, field)
		}
		projection, err := projectNativeEntry(t.Context(), Entry{Type: media, Data: json.RawMessage(data)}, fields)
		if err != nil {
			return
		}
		for field, value := range projection {
			if !slices.IsSorted(value.values) || len(slices.Compact(slices.Clone(value.values))) != len(value.values) || (nativePaths[field] != media && len(value.values) > 0) {
				t.Fatalf("invalid field projection: %+v", projection)
			}
		}
	})
}
