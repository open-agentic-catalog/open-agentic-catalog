package catalogmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func accessData() json.RawMessage {
	return json.RawMessage(`{"tenantId":"` + testID + `","subject":"PRIVATE_SUBJECT","membershipId":"PRIVATE_MEMBER","realmId":"PRIVATE_REALM","actorType":"human","claims":{"password":"PRIVATE_CLAIM"},"permissions":[],"availablePermissions":["resource.read"],"authorizationRevision":3,"namespaceGrants":[{"namespaceId":"` + testID + `","permissions":["resource.read"]}],"nextCursor":"opaque+/=","scopeEnforcement":true,"scopes":["catalog.read"]}`)
}

func TestDiscoveryArgumentsStayClosedBeforeAnyRead(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"catalog_control_explore", `{}`},
		{"catalog_control_explore", `{"facets":[]}`},
		{"catalog_control_explore", `{"query":null,"facets":[{"field":"kind"}]}`},
		{"catalog_control_explore", `{"query":{"origins":["live"]},"facets":[{"field":"kind"}]}`},
		{"catalog_control_explore", `{"query":{"cursor":"x"},"facets":[{"field":"kind"}]}`},
		{"catalog_control_explore", `{"query":{"limit":1},"facets":[{"field":"kind"}]}`},
		{"catalog_control_explore", `{"query":{"namespaceId":""},"facets":[{"field":"kind"}]}`},
		{"catalog_control_explore", `{"query":{"visibility":""},"facets":[{"field":"kind"}]}`},
		{"catalog_control_explore", `{"facets":[{"field":"kind","limit":0}]}`},
		{"catalog_control_explore", `{"facets":[{"field":"kind","limit":101}]}`},
		{"catalog_control_explore", `{"facets":[{"field":"kind","limit":null}]}`},
		{"catalog_control_explore", `{"facets":[{"field":"kind","minCount":-1}]}`},
		{"catalog_control_explore", `{"facets":[{"field":"kind","minCount":null}]}`},
		{"catalog_control_explore", `{"facets":[{"field":"kind"},{"field":"kind"}]}`},
		{"catalog_control_explore", `{"facets":[{"field":"owner"}]}`},
		{"catalog_control_explore", `{"facets":[{"field":"kind","url":"https://never.test"}]}`},
		{"catalog_control_access", `{"namespaceId":""}`},
		{"catalog_control_access", `{"namespaceId":"https://never.test"}`},
		{"catalog_control_access", `{"limit":0}`},
		{"catalog_control_access", `{"limit":101}`},
		{"catalog_control_access", `{"limit":null}`},
		{"catalog_control_access", `{"Limit":1}`},
		{"catalog_control_access", `{"limit":1,"limit":2}`},
		{"catalog_control_access", `{"cursor":""}`},
		{"catalog_control_access", `{"cursor":null}`},
		{"catalog_control_access", `{"cursor":"` + strings.Repeat("x", 2049) + `"}`},
		{"catalog_control_access", `{"subject":"another user"}`},
		{"catalog_control_access", `{"baseURL":"https://never.test"}`},
		{"catalog_control_access", `{} {}`},
	} {
		t.Run(tc.name+"/"+tc.raw[:min(70, len(tc.raw))], func(t *testing.T) {
			s := unitService(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid arguments reached the network boundary")
				return nil, errors.New("unexpected request")
			})
			result := call(t, s, tc.name, tc.raw)
			if !result.IsError || !strings.HasPrefix(toolText(t, result), "invalid_arguments:") {
				t.Fatal(result)
			}
		})
	}
	selected, err := toolRequest("catalog_control_explore", json.RawMessage(`{"query":{"namespaceId":"`+testID+`","license":" MIT ","categories":["TOOLS"]},"facets":[{"field":"kind"},{"field":"tag","limit":1,"minCount":2}]}`))
	if err != nil || selected.explore.Query.Limit != 0 || selected.explore.Query.Cursor != "" || selected.explore.Query.Origins != nil || selected.explore.Query.License != " MIT " || selected.explore.Facets[0].Limit != 20 || selected.explore.Facets[1].MinCount != 2 {
		t.Fatal(selected, err)
	}
	selected, err = toolRequest("catalog_control_access", json.RawMessage(`{}`))
	if err != nil || selected.access.Limit != 20 || selected.access.Cursor != "" {
		t.Fatal(selected, err)
	}
}

func TestExploreProjectionRetainsCountsAndRefusesMalformedWholeResults(t *testing.T) {
	selected, err := toolRequest("catalog_control_explore", json.RawMessage(`{"facets":[{"field":"kind","limit":1,"minCount":2},{"field":"tag","limit":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	control := `{"matched":12,"facets":{"kind":{"buckets":[{"value":"agent","count":3}],"otherCount":9},"tag":{"buckets":[],"otherCount":20}},"tier":"core","profile":"core-lexical-v1","fallbackReason":"indexing","partialIndex":true,"privateAddition":"DO_NOT_INCLUDE"}`
	projected, err := project("catalog_control_explore", json.RawMessage(control), selected)
	if err != nil || strings.Contains(string(projected), "DO_NOT_INCLUDE") {
		t.Fatal(string(projected), err)
	}
	var value catalog.ExploreResponse
	if json.Unmarshal(projected, &value) != nil || value.Matched != 12 || value.Facets["kind"].OtherCount != 9 || value.Facets["tag"].OtherCount != 20 || value.Tier != "core" || !value.PartialIndex || value.FallbackReason != "indexing" {
		t.Fatal(string(projected))
	}
	// Optional search evidence may be absent, but present values must satisfy
	// the Control Explore contract rather than disappear during projection.
	omitted := `{"matched":0,"facets":{"kind":{"buckets":[]},"tag":{"buckets":[]}}}`
	if raw, err := project("catalog_control_explore", json.RawMessage(omitted), selected); err != nil || string(raw) != omitted {
		t.Fatal("valid omitted evidence rejected or altered", string(raw), err)
	}
	for _, change := range [][2]string{
		{`"matched":12`, `"matched":null`}, {`"matched":12`, `"matched":-1`},
		{`"count":3`, `"count":null`}, {`"count":3`, `"count":1`}, {`"count":3`, `"count":13`},
		{`"value":"agent"`, `"value":null`}, {`"value":"agent"`, `"value":""`},
		{`"otherCount":9`, `"otherCount":0`}, {`"otherCount":9`, `"otherCount":null`},
		{`"buckets":[]`, `"buckets":null`}, {`"tag":`, `"unexpected":`},
		{`"partialIndex":true`, `"partialIndex":null`}, {`"tier":"core"`, `"tier":"unknown"`},
		{`"tier":"core"`, `"tier":""`}, {`"tier":"core"`, `"tier":null`},
		{`"profile":"core-lexical-v1"`, `"profile":"invalid"`},
		{`"profile":"core-lexical-v1"`, `"profile":""`}, {`"profile":"core-lexical-v1"`, `"profile":null`},
		{`"fallbackReason":"indexing"`, `"fallbackReason":"raw-error"`},
		{`"fallbackReason":"indexing"`, `"fallbackReason":""`}, {`"fallbackReason":"indexing"`, `"fallbackReason":null`},
	} {
		if raw, err := project("catalog_control_explore", json.RawMessage(strings.Replace(control, change[0], change[1], 1)), selected); err == nil || raw != nil {
			t.Errorf("accepted %v: %s", change, raw)
		}
	}
}

func TestAccessProjectionExcludesIdentityAndPreservesCursor(t *testing.T) {
	selected, err := toolRequest("catalog_control_access", json.RawMessage(`{"namespaceId":"`+testID+`","limit":1,"cursor":"opaque+/="}`))
	if err != nil {
		t.Fatal(err)
	}
	control := string(accessData())
	projected, err := project("catalog_control_access", json.RawMessage(control), selected)
	if err != nil || strings.Contains(string(projected), "PRIVATE_") {
		t.Fatal(string(projected), err)
	}
	var value accessProjection
	if json.Unmarshal(projected, &value) != nil || value.AuthorizationRevision != 3 || value.NextCursor != "opaque+/=" || value.AvailablePermissionsUse != "navigation_only" || len(value.Permissions) != 0 || len(value.NamespaceGrants) != 1 || !value.ScopeEnforcement || !reflect.DeepEqual(value.Scopes, []string{"catalog.read"}) {
		t.Fatal(string(projected))
	}
	for _, change := range [][2]string{
		{`"permissions":[]`, `"permissions":null`}, {`"availablePermissions":["resource.read"]`, `"availablePermissions":["invented.permission"]`},
		{`"authorizationRevision":3`, `"authorizationRevision":null`}, {`"authorizationRevision":3`, `"authorizationRevision":0`},
		{`"scopeEnforcement":true`, `"scopeEnforcement":null`}, {`"scopes":["catalog.read"]`, `"scopes":null`},
		{`"scopes":["catalog.read"]`, `"scopes":["catalog.read","catalog.read"]`},
		{`"namespaceGrants":[`, `"namespaceGrants":null,"ignored":[`},
		{`"nextCursor":"opaque+/="`, `"nextCursor":null`},
		{`"nextCursor":"opaque+/="`, `"nextCursor":"` + strings.Repeat("x", 2049) + `"`},
		{`"namespaceId":"` + testID + `"`, `"namespaceId":"` + testRelease + `"`},
	} {
		if raw, err := project("catalog_control_access", json.RawMessage(strings.Replace(control, change[0], change[1], 1)), selected); err == nil || raw != nil {
			t.Fatalf("accepted %v: %s", change, raw)
		}
	}
}

func TestDiscoveryRemoteReadsKeepAuthorityAndWholeResultBudgets(t *testing.T) {
	for _, tc := range []struct{ name, args, path, body string }{
		{"catalog_control_explore", `{"facets":[{"field":"kind"}]}`, "/api/v1/catalog/explore", `{"matched":1,"facets":{"kind":{"buckets":[{"value":"agent","count":1}]}}}`},
		{"catalog_control_access", `{"limit":1,"cursor":"opaque+/="}`, "/api/v1/me", string(accessData())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := unitService(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "catalog.test" || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer PRIVATE_TOKEN_SENTINEL" {
					t.Fatal("authority or credential changed")
				}
				if tc.name == "catalog_control_access" && (r.Method != http.MethodGet || r.URL.Query().Get("cursor") != "opaque+/=") {
					t.Fatal("altered access continuation")
				}
				if tc.name == "catalog_control_explore" && r.Method != http.MethodPost {
					t.Fatal("wrong Explore method")
				}
				return response(tc.body, 200), nil
			})
			if result := call(t, s, tc.name, tc.args); result.IsError {
				t.Fatal(toolText(t, result))
			}
			s.limits.ToolBytes = 1
			result := call(t, s, tc.name, tc.args)
			if !result.IsError || result.StructuredContent != nil || !strings.HasPrefix(toolText(t, result), "output_limit:") || strings.Contains(toolText(t, result), "opaque+/=") {
				t.Fatal(result)
			}
		})
	}
}

func TestAllToolOutputSchemasMatchTheirProjectedResults(t *testing.T) {
	capabilities := json.RawMessage(`{"resourceKinds":["agent","mcp_server","skill","plugin"],"search":{},"limits":{}}`)
	data := map[string]json.RawMessage{"catalog_control_capabilities": capabilities, "catalog_control_search": page(t, localResult()), "catalog_control_release": encoded(t, localRecord()), "catalog_control_indexed_record": encoded(t, indexedResult()), "catalog_control_explore": json.RawMessage(`{"matched":1,"facets":{"kind":{"buckets":[{"value":"agent","count":1}]}}}`), "catalog_control_access": accessData()}
	arguments := map[string]string{"catalog_control_capabilities": `{}`, "catalog_control_search": `{}`, "catalog_control_release": `{"releaseId":"` + testRelease + `"}`, "catalog_control_indexed_record": `{"identifier":"urn:air:example.test:agents:one","versionPresent":true}`, "catalog_control_explore": `{"facets":[{"field":"kind"}]}`, "catalog_control_access": `{}`}
	for _, tool := range tools() {
		var schema any
		if err := json.Unmarshal(encoded(t, tool.OutputSchema), &schema); err != nil {
			t.Fatal(err)
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("urn:test:"+tool.Name, schema); err != nil {
			t.Fatal(err)
		}
		compiled, err := compiler.Compile("urn:test:" + tool.Name)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := toolRequest(tool.Name, json.RawMessage(arguments[tool.Name]))
		if err != nil {
			t.Fatal(err)
		}
		projection, err := project(tool.Name, data[tool.Name], selected)
		if err != nil {
			t.Fatal(tool.Name, err)
		}
		var result any
		if err := json.Unmarshal(encoded(t, controlclient.Envelope{Data: projection, Meta: controlclient.Metadata{RequestID: "request-1"}}), &result); err != nil {
			t.Fatal(err)
		}
		if err := compiled.Validate(result); err != nil {
			t.Fatal(tool.Name, err)
		}
		calls := 0
		reader := isolatedReader{func(ctx context.Context, operation string, input any) (controlclient.Envelope, error) {
			calls++
			want := map[string]string{"catalog_control_capabilities": "capabilities", "catalog_control_search": "search", "catalog_control_release": "release", "catalog_control_indexed_record": "indexed", "catalog_control_explore": "explore", "catalog_control_access": "access"}[tool.Name]
			if ctx != t.Context() || operation != want {
				t.Fatal("wrong shared read", operation, input)
			}
			return controlclient.Envelope{Data: data[tool.Name], Meta: controlclient.Metadata{RequestID: "isolated-read", ETag: `"rev-3"`}}, nil
		}}
		called := Call(t.Context(), reader, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: tool.Name, Arguments: json.RawMessage(arguments[tool.Name])}}, "unit", 256<<10, "")
		if called.IsError || calls != 1 {
			t.Fatal("valid isolated read failed", tool.Name, called)
		}
		if err := json.Unmarshal(encoded(t, called.StructuredContent), &result); err != nil {
			t.Fatal(err)
		}
		if err := compiled.Validate(result); err != nil {
			t.Fatal(tool.Name, err)
		}
		if tool.Title == "" {
			t.Fatal("missing title")
		}
	}
	if size := len(encoded(t, tools())); size >= MaxFrameBytes {
		t.Fatal("static tools exceed protocol budget", size)
	}
}

type isolatedReader struct {
	read func(context.Context, string, any) (controlclient.Envelope, error)
}

func (r isolatedReader) ReadCapabilities(c context.Context) (controlclient.Envelope, error) {
	return r.read(c, "capabilities", nil)
}
func (r isolatedReader) ReadSearch(c context.Context, q catalog.SearchQuery) (controlclient.Envelope, error) {
	return r.read(c, "search", q)
}
func (r isolatedReader) ReadExplore(c context.Context, q catalog.ExploreRequest) (controlclient.Envelope, error) {
	return r.read(c, "explore", q)
}
func (r isolatedReader) ReadRelease(c context.Context, id string) (controlclient.Envelope, error) {
	return r.read(c, "release", id)
}
func (r isolatedReader) ReadIndexed(c context.Context, id catalog.IndexedIdentity) (controlclient.Envelope, error) {
	return r.read(c, "indexed", id)
}
func (r isolatedReader) ReadAccess(c context.Context, q url.Values) (controlclient.Envelope, error) {
	return r.read(c, "access", q)
}

func TestSharedReaderRefusalsNeverExposeCauses(t *testing.T) {
	for _, code := range []catalog.ErrorCode{catalog.CodeForbidden, catalog.CodeNotFound, catalog.CodeDependencyUnavailable} {
		reader := isolatedReader{func(context.Context, string, any) (controlclient.Envelope, error) {
			return controlclient.Envelope{}, catalog.E(code, "PRIVATE_CAUSE")
		}}
		result := Call(t.Context(), reader, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "catalog_control_access", Arguments: json.RawMessage(`{}`)}}, "unit", 256<<10, "")
		want := map[catalog.ErrorCode]string{catalog.CodeForbidden: "api_denied:", catalog.CodeNotFound: "api_not_found:", catalog.CodeDependencyUnavailable: "api_failure:"}[code]
		if !result.IsError || !strings.HasPrefix(toolText(t, result), want) || strings.Contains(string(encoded(t, result)), "PRIVATE_CAUSE") {
			t.Fatal(result)
		}
	}
}

func TestSharedOutputCredentialChecksBelongToEachCall(t *testing.T) {
	data := json.RawMessage(strings.Replace(string(accessData()), `"catalog.read"`, `"caller-one"`, 1))
	reader := isolatedReader{func(context.Context, string, any) (controlclient.Envelope, error) {
		return controlclient.Envelope{Data: data, Meta: controlclient.Metadata{RequestID: "safe-request"}}, nil
	}}
	request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "catalog_control_access", Arguments: json.RawMessage(`{}`)}}
	for _, credential := range []string{"caller-one", "caller-two", "", "caller-one"} {
		result := Call(t.Context(), reader, request, "unit", 256<<10, credential)
		if result.IsError != (credential == "caller-one") {
			t.Fatal("credential checks leaked across callers", credential, result)
		}
		if result.IsError && (result.StructuredContent != nil || strings.Contains(toolText(t, result), credential)) {
			t.Fatal("reflected credential exposed")
		}
	}
}

func TestDeclaredAgentProtocolToolSelection(t *testing.T) {
	for _, binding := range []string{"HTTP+JSON", "私有+V1 "} {
		selected, err := toolRequest("catalog_control_search", encoded(t, map[string]any{"agentProtocol": binding, "origins": []string{"local"}}))
		if err != nil || selected.query.AgentProtocol != binding || !reflect.DeepEqual(selected.query.Origins, []catalog.SearchResultType{catalog.SearchResultLocal}) {
			t.Fatal("protocol lost", err)
		}
	}
	for _, raw := range []string{`{"agentProtocol":"HTTP+JSON"}`, `{"agentProtocol":"HTTP+JSON","origins":["local","live"]}`, `{"agentProtocol":null}`, `{"agentProtocol":" "}`} {
		if _, err := toolRequest("catalog_control_search", json.RawMessage(raw)); err == nil {
			t.Fatal("invalid protocol accepted", raw)
		}
	}
	selected, err := toolRequest("catalog_control_explore", json.RawMessage(`{"query":{"agentProtocol":"私有+V1 "},"facets":[{"field":"category"}]}`))
	if err != nil || selected.explore.Query.AgentProtocol != "私有+V1 " || selected.explore.Query.Origins != nil {
		t.Fatal("local facet selection lost", err)
	}
}

func TestDeclaredAgentProtocolRemoteWire(t *testing.T) {
	const binding = "私有+V1 "
	for _, operation := range []string{"search", "explore"} {
		t.Run(operation, func(t *testing.T) {
			requests := 0
			s := unitService(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.Host != "catalog.test" || r.URL.Path != "/api/v1/catalog/"+operation || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer PRIVATE_TOKEN_SENTINEL" {
					t.Fatal("protocol criteria changed the fixed request authority")
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]json.RawMessage
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				if operation == "explore" {
					var query map[string]json.RawMessage
					if err := json.Unmarshal(body["query"], &query); err != nil {
						t.Fatal(err)
					}
					body = query
				}
				var actual string
				if err := json.Unmarshal(body["agentProtocol"], &actual); err != nil || actual != binding {
					t.Fatal("literal protocol lost before the API exchange", actual, err)
				}
				if operation == "search" {
					var origins []string
					if err := json.Unmarshal(body["origins"], &origins); err != nil || !reflect.DeepEqual(origins, []string{"local"}) {
						t.Fatal("local-only Search scope lost", origins, err)
					}
					item := localResult()
					item.Kind, item.Local.Resource.Kind = catalog.KindAgent, catalog.KindAgent
					return response(string(page(t, item)), http.StatusOK), nil
				}
				if body["origins"] != nil || body["cursor"] != nil || body["limit"] != nil {
					t.Fatal("Search-only fields entered Explore")
				}
				return response(`{"matched":1,"facets":{"kind":{"buckets":[{"value":"agent","count":1}]}}}`, http.StatusOK), nil
			})
			query := map[string]any{"agentProtocol": binding}
			args := query
			if operation == "search" {
				query["origins"] = []string{"local"}
			} else {
				args = map[string]any{"query": query, "facets": []map[string]string{{"field": "kind"}}}
			}
			result := call(t, s, "catalog_control_"+operation, string(encoded(t, args)))
			if result.IsError || requests != 1 {
				t.Fatal("valid protocol selection did not make exactly one exchange", result, requests)
			}
			for _, invalid := range []any{nil, " ", strings.Repeat("🙂", 257)} {
				query["agentProtocol"] = invalid
				result := call(t, s, "catalog_control_"+operation, string(encoded(t, args)))
				if !result.IsError || !strings.HasPrefix(toolText(t, result), "invalid_arguments:") || requests != 1 {
					t.Fatal("invalid protocol reached the API exchange", result, requests)
				}
			}
		})
	}
}

func TestDeclaredMCPToolsRemoteWire(t *testing.T) {
	for _, operation := range []string{"search", "explore"} {
		t.Run(operation, func(t *testing.T) {
			requests := 0
			s := unitService(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.Host != "catalog.test" || r.URL.Path != "/api/v1/catalog/"+operation {
					t.Fatal("changed fixed API authority")
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]json.RawMessage
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				if operation == "explore" {
					if err := json.Unmarshal(body["query"], &body); err != nil {
						t.Fatal(err)
					}
				}
				if string(body["includeMcpTools"]) != "true" || string(body["text"]) != `"pdf tables"` {
					t.Fatal("private tool selector lost", string(raw))
				}
				if operation == "explore" {
					return response(`{"matched":1,"facets":{"kind":{"buckets":[{"value":"mcp_server","count":1}]}},"profile":"core-mcp-tools-v1","tier":"core"}`, http.StatusOK), nil
				}
				item := localResult()
				item.Kind, item.Local.Resource.Kind = catalog.KindMCPServer, catalog.KindMCPServer
				item.ToolMatch = &catalog.MCPToolMatch{RepresentationID: testID, ContentDigest: testDigest, MatchedToolCount: 1, Tools: []catalog.MCPToolSnippet{{Name: "extract_pdf_tables"}}}
				return response(string(encoded(t, catalog.CatalogSearchResponse{Items: []catalog.CatalogSearchResult{item}, DurableTotal: 1, LiveCount: 0, Profile: catalog.MCPToolsSearchProfile, Tier: "core", Generation: testDigest})), http.StatusOK), nil
			})
			query := map[string]any{"text": "pdf tables", "includeMcpTools": true}
			args := query
			if operation == "search" {
				query["origins"] = []string{"local"}
			} else {
				args = map[string]any{"query": query, "facets": []map[string]string{{"field": "kind"}}}
			}
			result := call(t, s, "catalog_control_"+operation, string(encoded(t, args)))
			if result.IsError || requests != 1 {
				t.Fatal("valid tool search refused", result)
			}
			if operation == "search" && !strings.Contains(string(encoded(t, result.StructuredContent)), "extract_pdf_tables") {
				t.Fatal("match explanation discarded", result)
			}
			for i, invalid := range []map[string]any{{"includeMcpTools": true, "text": "pdf"}, {"includeMcpTools": true, "text": "pdf", "origins": []string{"live"}}, {"includeMcpTools": true, "text": " ", "origins": []string{"local"}}, {"includeMcpTools": nil}} {
				if operation == "explore" {
					invalid = map[string]any{"query": invalid, "facets": []map[string]string{{"field": "kind"}}}
					if i == 0 {
						continue
					}
				}
				result = call(t, s, "catalog_control_"+operation, string(encoded(t, invalid)))
				if !result.IsError || requests != 1 {
					t.Fatal("invalid selection reached transport", invalid, requests)
				}
			}
		})
	}
}

func TestDeclaredMCPToolProjectionRejectsMalformedEvidence(t *testing.T) {
	valid := &catalog.MCPToolMatch{RepresentationID: testID, ContentDigest: testDigest, MatchedToolCount: 1, Tools: []catalog.MCPToolSnippet{{Name: "extract_pdf_tables"}}}
	if !validToolMatch(valid) {
		t.Fatal("valid match refused")
	}
	for _, edit := range []func(*catalog.MCPToolMatch){func(m *catalog.MCPToolMatch) { m.RepresentationID = "bad" }, func(m *catalog.MCPToolMatch) { m.ContentDigest = "bad" }, func(m *catalog.MCPToolMatch) { m.MatchedToolCount = 65 }, func(m *catalog.MCPToolMatch) { m.Tools = nil }, func(m *catalog.MCPToolMatch) { m.Truncated = true }, func(m *catalog.MCPToolMatch) { m.Tools = []catalog.MCPToolSnippet{{Name: " "}} }, func(m *catalog.MCPToolMatch) {
		m.Tools = []catalog.MCPToolSnippet{{Name: "x", DescriptionTruncated: true}}
	}} {
		invalid := *valid
		edit(&invalid)
		if validToolMatch(&invalid) {
			t.Fatal("malformed match accepted", invalid)
		}
	}
}

func TestControlMCPRejectsUnadvertisedPrivateNativeSelectors(t *testing.T) {
	for _, raw := range []string{`{"nativeCapabilities":{"agentStreaming":false},"origins":["local"]}`, `{"nativeCapabilities":{"agentSkillIds":["invoice"]},"origins":["local"]}`} {
		if _, err := toolRequest("catalog_control_search", json.RawMessage(raw)); err == nil {
			t.Fatal("unadvertised selector could be silently lost")
		}
	}
	if _, err := toolRequest("catalog_control_explore", json.RawMessage(`{"query":{"nativeCapabilities":{"agentStreaming":false}},"facets":[{"field":"kind"}]}`)); err == nil {
		t.Fatal("unadvertised Explore selector accepted")
	}
}
