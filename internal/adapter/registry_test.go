package adapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"strconv"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/a2apb"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestAgentCardValidationUsesPinnedProtoJSONContract(t *testing.T) {
	input := catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: json.RawMessage(`{
    "name":"Invoice resolution agent",
    "description":"Resolves invoice exceptions",
    "version":"1.0.0",
    "supportedInterfaces":[{"url":"https://agents.example.com/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
    "capabilities":{},
    "defaultInputModes":["application/json"],
    "defaultOutputModes":["application/json"],
    "skills":[{"id":"resolve-invoices","name":"Resolve invoices","description":"Resolves invoice exceptions","tags":["invoices"]}]
  }`)}
	result := NewRegistry().Validate(context.Background(), catalog.KindAgent, input)
	if hasErrors(result.Findings) {
		t.Fatalf("unexpected findings: %#v", result.Findings)
	}
	input.Data = append(input.Data[:len(input.Data)-2], []byte(`,"x-future":{"enabled":true}}`)...)
	result = NewRegistry().Validate(context.Background(), catalog.KindAgent, input)
	if !hasFinding(result.Findings, "A2A_PROTOJSON_INVALID") {
		t.Fatalf("unknown ProtoJSON property was accepted: %#v", result.Findings)
	}
}

func TestA2AAgentCardSemanticValidation(t *testing.T) {
	base := `{
    "name":"Invoice resolution agent",
    "description":"Resolves invoice exceptions",
    "version":"1.0.0",
    "supportedInterfaces":[{"url":"https://agents.example.com/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
    "capabilities":{},
    "defaultInputModes":["application/json"],
    "defaultOutputModes":["application/json"],
    "skills":[{"id":"resolve-invoices","name":"Resolve invoices","description":"Resolves invoice exceptions","tags":["invoices"]}],
    "securityRequirements":[{}]
  }`
	result := NewRegistry().Validate(context.Background(), catalog.KindAgent, catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: json.RawMessage(base)})
	if hasErrors(result.Findings) {
		t.Fatalf("anonymous A2A security requirement was rejected: %#v", result.Findings)
	}
	invalidProtocol := strings.Replace(base, `"protocolVersion":"1.0"`, `"protocolVersion":"1.0.1"`, 1)
	result = NewRegistry().Validate(context.Background(), catalog.KindAgent, catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: json.RawMessage(invalidProtocol)})
	if !hasFinding(result.Findings, "A2A_INTERFACE_INVALID") {
		t.Fatalf("patch protocol version was accepted: %#v", result.Findings)
	}
	invalidOAuth := strings.Replace(base, `"securityRequirements":[{}]`, `"securitySchemes":{"oauth":{"oauth2SecurityScheme":{"flows":{"clientCredentials":{"tokenUrl":"https://identity.example.com/token","refreshUrl":"http://identity.example.com/refresh","scopes":{}}}}}},"securityRequirements":[{"schemes":{"oauth":{"list":[]}}}]`, 1)
	result = NewRegistry().Validate(context.Background(), catalog.KindAgent, catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: json.RawMessage(invalidOAuth)})
	if !hasFinding(result.Findings, "A2A_OAUTH_FLOW_INVALID") {
		t.Fatalf("insecure OAuth refresh URL was accepted: %#v", result.Findings)
	}
	invalidSignature := strings.Replace(base, `"securityRequirements":[{}]`, `"securityRequirements":[{}],"signatures":[{"protected":"e30","signature":"c2ln"}]`, 1)
	result = NewRegistry().Validate(context.Background(), catalog.KindAgent, catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: json.RawMessage(invalidSignature)})
	if !hasFinding(result.Findings, "A2A_SIGNATURE_INVALID") {
		t.Fatalf("A2A signature without a JWS algorithm was accepted: %#v", result.Findings)
	}
}

func TestA2AAgentCardRejectsIncompleteNestedMetadata(t *testing.T) {
	_, findings := validateA2AAgentCard(json.RawMessage(`{}`), catalog.DefaultDiagnosticLimits())
	for _, code := range []string{"A2A_REQUIRED_FIELD"} {
		if !hasFinding(findings, code) {
			t.Fatalf("incomplete card is missing finding %s: %#v", code, findings)
		}
	}

	protected := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","kid":"test-key"}`))
	raw := json.RawMessage(`{
    "name":"Agent",
    "description":"Exercises nested validation",
    "version":"1.0.0",
    "supportedInterfaces":[{}],
    "capabilities":{},
    "provider":{"organization":"","url":"http://provider.example.com"},
    "documentationUrl":"http://docs.example.com",
    "iconUrl":"http://icons.example.com/agent.png",
    "defaultInputModes":["application/json"],
    "defaultOutputModes":["application/json"],
    "skills":[{}, {"id":"skill","name":"Skill","description":"A skill","tags":[" "]}],
    "signatures":[{}, {"protected":` + strconv.Quote(protected) + `,"signature":"not-base64!"}]
  }`)
	_, findings = validateA2AAgentCard(raw, catalog.DefaultDiagnosticLimits())
	for _, code := range []string{"A2A_INTERFACE_INVALID", "A2A_PROVIDER_INVALID", "A2A_URL_INVALID", "A2A_SKILL_INVALID", "A2A_SIGNATURE_INVALID"} {
		if !hasFinding(findings, code) {
			t.Fatalf("nested card is missing finding %s: %#v", code, findings)
		}
	}
}

func TestA2ADeprecatedOAuthFlowsRemainSafelyValidated(t *testing.T) {
	//lint:ignore SA1019 The pinned A2A profile still accepts this deprecated flow and the Catalog must validate it safely.
	implicit := &a2apb.ImplicitOAuthFlow{AuthorizationUrl: "http://identity.example.com/authorize"} //nolint:staticcheck // The pinned profile still accepts this deprecated flow.
	//lint:ignore SA1019 The pinned A2A profile still accepts this deprecated flow and the Catalog must validate it safely.
	implicitFlow := &a2apb.OAuthFlows_Implicit{Implicit: implicit} //nolint:staticcheck // The pinned profile still accepts this deprecated flow.
	//lint:ignore SA1019 The pinned A2A profile still accepts this deprecated flow and the Catalog must validate it safely.
	legacy := &a2apb.PasswordOAuthFlow{TokenUrl: "http://identity.example.com/token"} //nolint:staticcheck,gosec // The pinned deprecated type name is not credential material.
	//lint:ignore SA1019 The pinned A2A profile still accepts this deprecated flow and the Catalog must validate it safely.
	legacyFlow := &a2apb.OAuthFlows_Password{Password: legacy} //nolint:staticcheck // The pinned profile still accepts this deprecated flow.
	flows := map[string]*a2apb.OAuth2SecurityScheme{
		"implicit": {Flows: &a2apb.OAuthFlows{Flow: implicitFlow}},
		"password": {Flows: &a2apb.OAuthFlows{Flow: legacyFlow}},
	}
	for name, scheme := range flows {
		t.Run(name, func(t *testing.T) {
			if findings := collectA2AFindings(func(report *catalog.FindingCollector) { validateA2AOAuth2(scheme, "/securitySchemes/oauth", report) }); !hasFinding(findings, "A2A_OAUTH_FLOW_INVALID") {
				t.Fatalf("insecure deprecated OAuth flow was accepted: %#v", findings)
			}
		})
	}
	if findings := collectA2AFindings(func(report *catalog.FindingCollector) {
		validateA2AOptionalURL("https://identity.example.com/refresh", "/refreshUrl", report)
	}); len(findings) != 0 {
		t.Fatalf("safe optional OAuth URL returned findings: %#v", findings)
	}
}

func TestA2ASecurityValidatorsRejectMalformedSchemesAndFlows(t *testing.T) {
	schemes := map[string]*a2apb.SecurityScheme{
		"":        nil,
		"api/key": {Scheme: &a2apb.SecurityScheme_ApiKeySecurityScheme{ApiKeySecurityScheme: &a2apb.APIKeySecurityScheme{Location: "body"}}},
		"http":    {Scheme: &a2apb.SecurityScheme_HttpAuthSecurityScheme{HttpAuthSecurityScheme: &a2apb.HTTPAuthSecurityScheme{}}},
		"oidc":    {Scheme: &a2apb.SecurityScheme_OpenIdConnectSecurityScheme{OpenIdConnectSecurityScheme: &a2apb.OpenIdConnectSecurityScheme{OpenIdConnectUrl: "http://identity.example.com/.well-known/openid-configuration"}}},
		"mtls":    {Scheme: &a2apb.SecurityScheme_MtlsSecurityScheme{MtlsSecurityScheme: &a2apb.MutualTlsSecurityScheme{}}},
	}
	findings := collectA2AFindings(func(report *catalog.FindingCollector) { validateA2ASecuritySchemes(schemes, report) })
	if !hasFinding(findings, "A2A_SECURITY_SCHEME_INVALID") {
		t.Fatalf("malformed security schemes were accepted: %#v", findings)
	}
	foundEscapedPath := false
	for _, finding := range findings {
		foundEscapedPath = foundEscapedPath || finding.Path == "/securitySchemes/api~1key"
	}
	if !foundEscapedPath {
		t.Fatalf("security scheme JSON Pointer was not escaped: %#v", findings)
	}

	flows := map[string]*a2apb.OAuth2SecurityScheme{
		"missing": {},
		"authorization code": {
			Oauth2MetadataUrl: "http://identity.example.com/metadata",
			Flows: &a2apb.OAuthFlows{Flow: &a2apb.OAuthFlows_AuthorizationCode{AuthorizationCode: &a2apb.AuthorizationCodeOAuthFlow{
				AuthorizationUrl: "http://identity.example.com/authorize", TokenUrl: "", RefreshUrl: "http://identity.example.com/refresh",
			}}},
		},
		"device code": {Flows: &a2apb.OAuthFlows{Flow: &a2apb.OAuthFlows_DeviceCode{DeviceCode: &a2apb.DeviceCodeOAuthFlow{
			DeviceAuthorizationUrl: "http://identity.example.com/device", TokenUrl: "", RefreshUrl: "http://identity.example.com/refresh",
		}}}},
	}
	for name, scheme := range flows {
		t.Run(name, func(t *testing.T) {
			if flowFindings := collectA2AFindings(func(report *catalog.FindingCollector) { validateA2AOAuth2(scheme, "/securitySchemes/oauth", report) }); !hasFinding(flowFindings, "A2A_SECURITY_SCHEME_INVALID") && !hasFinding(flowFindings, "A2A_OAUTH_FLOW_INVALID") {
				t.Fatalf("malformed OAuth flow was accepted: %#v", flowFindings)
			}
		})
	}

	requirementFindings := collectA2AFindings(func(report *catalog.FindingCollector) {
		validateA2ASecurityRequirements([]*a2apb.SecurityRequirement{nil, {Schemes: map[string]*a2apb.StringList{"missing": {}}}}, schemes, "/securityRequirements", report)
	})
	if !hasFinding(requirementFindings, "A2A_SECURITY_REQUIREMENT_INVALID") {
		t.Fatalf("malformed security requirements were accepted: %#v", requirementFindings)
	}
}

func TestA2AProtectedHeadersAndMediaTypesAreStrict(t *testing.T) {
	encode := func(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
	for _, value := range []string{"not-base64!", encode(`{"alg":"RS256","alg":"ES256","kid":"test-key"}`), encode(`[]`), encode(`{"alg":"none","kid":"test-key"}`), encode(`{"alg":" ","kid":"test-key"}`)} {
		if parseA2AProtectedHeader(value) != nil {
			t.Fatalf("invalid protected header %q was accepted", value)
		}
	}
	if parseA2AProtectedHeader(encode(`{"alg":"ES256","kid":"test-key"}`)) == nil {
		t.Fatal("valid protected header was rejected")
	}
	if !hasFinding(collectA2AFindings(func(report *catalog.FindingCollector) { validateA2AMediaTypes(nil, "/defaultInputModes", report) }), "A2A_REQUIRED_FIELD") {
		t.Fatal("empty default media types were accepted")
	}
	if !hasFinding(collectA2AFindings(func(report *catalog.FindingCollector) {
		validateA2AMediaTypesOptional([]string{"application/json", "invalid", "text/plain\nmalformed"}, "/inputModes", report)
	}), "A2A_MEDIA_TYPE_INVALID") {
		t.Fatal("malformed optional media type was accepted")
	}
}

func TestMCPRegistryIsNotServerCard(t *testing.T) {
	input := catalog.RepresentationInput{MediaType: MediaMCPRegistry, Data: json.RawMessage(`{"name":"io.example/mcp","description":"Provides example tools.","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://mcp.example.com"}]}`)}
	result := NewRegistry().Validate(context.Background(), catalog.KindMCPServer, input)
	if hasErrors(result.Findings) {
		t.Fatalf("unexpected errors: %#v", result.Findings)
	}
	if result.ProfileID != ProfileMCPRegistry {
		t.Fatalf("wrong profile: %s", result.ProfileID)
	}
}

func TestMCPValidationRetainsProfileForInvalidDocuments(t *testing.T) {
	for _, profile := range []struct{ mediaType, id string }{
		{MediaMCPServerCard, ProfileMCPServerCard},
		{MediaMCPRegistry, ProfileMCPRegistry},
		{MediaPrivateMCP, ProfilePrivateMCP},
	} {
		t.Run(profile.id, func(t *testing.T) {
			for _, raw := range []string{`{`, `null`, `[]`} {
				result := NewRegistry().Validate(t.Context(), catalog.KindMCPServer, catalog.RepresentationInput{MediaType: profile.mediaType, Data: json.RawMessage(raw)})
				if result.ProfileID != profile.id || !hasErrors(result.Findings) || len(result.NormalizedFacts) != 0 {
					t.Fatalf("invalid %s: %#v", raw, result)
				}
			}
		})
	}
	for _, profile := range []struct{ mediaType, code string }{
		{MediaMCPServerCard, "MCP_SERVER_CARD_SCHEMA_INVALID"},
		{MediaMCPRegistry, "MCP_REGISTRY_SCHEMA_INVALID"},
	} {
		result := NewRegistry().Validate(t.Context(), catalog.KindMCPServer, catalog.RepresentationInput{MediaType: profile.mediaType, Data: json.RawMessage(`{"name":42}`)})
		if !hasFinding(result.Findings, profile.code) {
			t.Fatalf("missing %s: %#v", profile.code, result.Findings)
		}
		if profile.mediaType == MediaMCPRegistry && !hasFinding(result.Findings, "MCP_REGISTRY_VERSION_REQUIRED") {
			t.Fatalf("missing Registry version finding: %#v", result.Findings)
		}
	}
}

func TestMCPServerCardAllowsNoRemoteAndRequiresNonEmptyVersion(t *testing.T) {
	input := catalog.RepresentationInput{MediaType: MediaMCPServerCard, Data: json.RawMessage(`{"$schema":"https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json","name":"com.example/mcp","description":"Provides example tools.","version":"1.0.0"}`)}
	result := NewRegistry().Validate(context.Background(), catalog.KindMCPServer, input)
	if hasErrors(result.Findings) {
		t.Fatalf("schema-valid card without remotes was rejected: %#v", result.Findings)
	}
	input.Data = json.RawMessage(`{"$schema":"https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json","name":"com.example/mcp","description":"Provides example tools.","version":""}`)
	result = NewRegistry().Validate(context.Background(), catalog.KindMCPServer, input)
	if !hasFinding(result.Findings, "MCP_SERVER_CARD_VERSION_REQUIRED") {
		t.Fatalf("empty MCP Server Card version was accepted: %#v", result.Findings)
	}
}

func TestSkillValidation(t *testing.T) {
	valid := "---\nname: reconcile-invoices\ndescription: Reconcile disputed invoices\nmetadata:\n  author: example-org\n  version: \"1.0\"\n---\n# Workflow\n"
	for _, mediaType := range []string{MediaSkillMarkdown, MediaSkillMarkdownARD, `text/markdown; charset=utf-8; profile="urn:air:agent-skills"`} {
		result := NewRegistry().Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: mediaType, Content: valid})
		if hasErrors(result.Findings) {
			t.Fatalf("%s returned unexpected errors: %#v", mediaType, result.Findings)
		}
	}
	invalid := NewRegistry().Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: MediaSkillMarkdown, Content: "# no frontmatter"})
	if !hasErrors(invalid.Findings) {
		t.Fatal("expected invalid skill to fail")
	}
	for name, content := range map[string]string{
		"unknown field":       "---\nname: reconcile-invoices\ndescription: Reconcile disputed invoices\ncustom: value\n---\n",
		"typed metadata":      "---\nname: reconcile-invoices\ndescription: Reconcile disputed invoices\nmetadata:\n  version: 1\n---\n",
		"unclosed near match": "---\nname: reconcile-invoices\ndescription: Reconcile disputed invoices\n----\n# Workflow\n",
	} {
		result := NewRegistry().Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: MediaSkillMarkdown, Content: content})
		if !hasErrors(result.Findings) {
			t.Fatalf("%s was accepted: %#v", name, result.Findings)
		}
	}
	tooLong := "---\nname: reconcile-invoices\ndescription: Reconcile disputed invoices\ncompatibility: " + strings.Repeat("x", 501) + "\n---\n"
	result := NewRegistry().Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: MediaSkillMarkdown, Content: tooLong})
	if !hasFinding(result.Findings, "SKILL_COMPATIBILITY_INVALID") {
		t.Fatalf("oversized compatibility was accepted: %#v", result.Findings)
	}
	for _, mediaType := range []string{MediaSkillZIP, MediaSkillGZIP} {
		archive := NewRegistry().Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: mediaType, Content: "opaque package bytes"})
		if !hasErrors(archive.Findings) || !hasFinding(archive.Findings, "SKILL_PACKAGE_BYTES_REQUIRED") {
			t.Fatalf("opaque %s Skill package was not rejected: %#v", mediaType, archive.Findings)
		}
		remote := NewRegistry().Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: mediaType, URL: "https://skills.example.com/releases/example"})
		if !hasErrors(remote.Findings) || !hasFinding(remote.Findings, "SKILL_PACKAGE_BYTES_REQUIRED") {
			t.Fatalf("uninspected remote %s Skill package was not rejected: %#v", mediaType, remote.Findings)
		}
	}
}

func TestSkillDiscoveryMediaTypeUsesARDProfile(t *testing.T) {
	for _, mediaType := range []string{MediaSkillMarkdown, MediaSkillMarkdownARD, `text/markdown;profile="urn:air:agent-skills"`} {
		if got := DiscoveryMediaType(mediaType); got != MediaSkillMarkdownARD {
			t.Fatalf("DiscoveryMediaType(%q) = %q", mediaType, got)
		}
	}
	if got := DiscoveryMediaType(MediaA2AAgentCard); got != MediaA2AAgentCard {
		t.Fatalf("non-Skill discovery type changed to %q", got)
	}
	if got := DiscoveryMediaType("  invalid;type  "); got != "invalid;type" {
		t.Fatalf("unparseable discovery type changed to %q", got)
	}
	declared := `Application/Example+JSON; Profile="https://Example.com/Profile"`
	projected := DiscoveryMediaType(declared)
	mediaType, parameters, err := mime.ParseMediaType(projected)
	if err != nil || mediaType != "application/example+json" || parameters["profile"] != "https://Example.com/Profile" {
		t.Fatalf("parameterized discovery type %q projected as %q, %v", declared, projected, err)
	}
}

func TestJSONRepresentationRejectsTrailingValue(t *testing.T) {
	result := NewRegistry().Validate(context.Background(), catalog.KindAgent, catalog.RepresentationInput{
		MediaType: MediaA2AAgentCard,
		Data:      json.RawMessage(`{"name":"one"}{"name":"two"}`),
	})
	if !hasFinding(result.Findings, "JSON_TRAILING_DATA") {
		t.Fatalf("trailing JSON value was accepted: %#v", result.Findings)
	}
}

func TestAgentAndMCPValidationRejectCredentialBearingEndpoints(t *testing.T) {
	agent := NewRegistry().Validate(context.Background(), catalog.KindAgent, catalog.RepresentationInput{
		MediaType: MediaA2AAgentCard,
		Data:      json.RawMessage(`{"name":"Agent","description":"Test","version":"1.0.0","supportedInterfaces":[{"url":"https://agents.example.com/a2a?access_token=secret","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"test","name":"Test","description":"Tests the Agent","tags":["test"]}]}`),
	})
	if !hasFinding(agent.Findings, "A2A_INTERFACE_INVALID") {
		t.Fatalf("credential-bearing Agent endpoint was accepted: %#v", agent.Findings)
	}
	mcp := NewRegistry().Validate(context.Background(), catalog.KindMCPServer, catalog.RepresentationInput{
		MediaType: MediaMCPServerCard,
		Data:      json.RawMessage(`{"$schema":"https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json","name":"com.example/mcp","description":"Provides test tools.","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://mcp.example.com/messages?X-Amz-Signature=secret"}]}`),
	})
	if !hasFinding(mcp.Findings, "MCP_CARD_REMOTE_INVALID") {
		t.Fatalf("credential-bearing MCP endpoint was accepted: %#v", mcp.Findings)
	}
}

func TestRegistryRepresentationVariantsAndFailures(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	tests := []struct {
		name      string
		kind      catalog.Kind
		input     catalog.RepresentationInput
		wantCode  string
		wantError bool
	}{
		{name: "unsupported kind", kind: catalog.Kind("unsupported"), input: catalog.RepresentationInput{MediaType: "application/json"}, wantCode: "UNSUPPORTED_RESOURCE_KIND", wantError: true},
		{name: "unsupported agent media", kind: catalog.KindAgent, input: catalog.RepresentationInput{MediaType: "APPLICATION/UNKNOWN"}, wantCode: "AGENT_MEDIA_TYPE_UNSUPPORTED", wantError: true},
		{name: "private agent", kind: catalog.KindAgent, input: catalog.RepresentationInput{MediaType: MediaPrivateAgent, Data: json.RawMessage(`{"name":"Agent","framework":"custom"}`)}},
		{name: "private agent fields", kind: catalog.KindAgent, input: catalog.RepresentationInput{MediaType: MediaPrivateAgent, Data: json.RawMessage(`{}`)}, wantCode: "PRIVATE_AGENT_FIELDS_REQUIRED", wantError: true},
		{name: "private agent object", kind: catalog.KindAgent, input: catalog.RepresentationInput{MediaType: MediaPrivateAgent}, wantCode: "JSON_OBJECT_REQUIRED", wantError: true},
		{name: "unsupported MCP media", kind: catalog.KindMCPServer, input: catalog.RepresentationInput{MediaType: "not a media type"}, wantCode: "MCP_MEDIA_TYPE_UNSUPPORTED", wantError: true},
		{name: "private MCP", kind: catalog.KindMCPServer, input: catalog.RepresentationInput{MediaType: MediaPrivateMCP, Data: json.RawMessage(`{"name":"Server","transport":"stdio"}`)}},
		{name: "private MCP fields", kind: catalog.KindMCPServer, input: catalog.RepresentationInput{MediaType: MediaPrivateMCP, Data: json.RawMessage(`{"name":"Server"}`)}, wantCode: "PRIVATE_MCP_FIELDS_REQUIRED", wantError: true},
		{name: "registry distribution", kind: catalog.KindMCPServer, input: catalog.RepresentationInput{MediaType: MediaMCPRegistry, Data: json.RawMessage(`{"name":"io.example/server","description":"Example","version":"1.0.0"}`)}, wantCode: "MCP_DISTRIBUTION_NOT_DECLARED"},
		{name: "registry remote", kind: catalog.KindMCPServer, input: catalog.RepresentationInput{MediaType: MediaMCPRegistry, Data: json.RawMessage(`{"name":"io.example/server","description":"Example","version":"1.0.0","remotes":[{"type":"streamable-http","url":"http://localhost/server"}]}`)}, wantCode: "MCP_REGISTRY_REMOTE_INVALID", wantError: true},
		{name: "skill markdown content", kind: catalog.KindSkill, input: catalog.RepresentationInput{MediaType: MediaSkillMarkdown}, wantCode: "SKILL_CONTENT_REQUIRED", wantError: true},
		{name: "skill package", kind: catalog.KindSkill, input: catalog.RepresentationInput{MediaType: MediaSkillZIP}, wantCode: "SKILL_PACKAGE_BYTES_REQUIRED", wantError: true},
		{name: "skill JSON", kind: catalog.KindSkill, input: catalog.RepresentationInput{MediaType: MediaSkillJSON, Data: json.RawMessage(`{"name":"skill","description":"Example skill"}`)}, wantCode: "SKILL_JSON_NOT_INSTALLABLE"},
		{name: "skill JSON metadata", kind: catalog.KindSkill, input: catalog.RepresentationInput{MediaType: MediaSkillJSON, Data: json.RawMessage(`{}`)}, wantCode: "SKILL_METADATA_REQUIRED", wantError: true},
		{name: "skill JSON object", kind: catalog.KindSkill, input: catalog.RepresentationInput{MediaType: MediaSkillJSON, Data: json.RawMessage(`[]`)}, wantCode: "JSON_OBJECT_INVALID", wantError: true},
		{name: "unsupported skill media", kind: catalog.KindSkill, input: catalog.RepresentationInput{MediaType: "application/zip"}, wantCode: "SKILL_MEDIA_TYPE_UNSUPPORTED", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := registry.Validate(context.Background(), test.kind, test.input)
			if test.wantCode != "" && !hasFinding(result.Findings, test.wantCode) {
				t.Fatalf("missing finding %s: %#v", test.wantCode, result.Findings)
			}
			if hasErrors(result.Findings) != test.wantError {
				t.Fatalf("error findings = %#v, want error %t", result.Findings, test.wantError)
			}
		})
	}
}

func TestSkillMarkdownAdversarialBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		code string
	}{
		{name: "invalid UTF-8", raw: string([]byte{0xff}), code: "SKILL_MARKDOWN_INVALID"},
		{name: "NUL", raw: "---\nname: skill\ndescription: Example\n---\n\x00", code: "SKILL_MARKDOWN_INVALID"},
		{name: "size", raw: strings.Repeat("x", 500_001), code: "SKILL_MARKDOWN_TOO_LARGE"},
		{name: "sequence root", raw: "---\n- name\n- description\n---\n", code: "SKILL_FRONTMATTER_INVALID"},
		{name: "empty frontmatter", raw: "---\n---\n", code: "SKILL_FRONTMATTER_INVALID"},
		{name: "numeric key", raw: "---\n123: example\n---\n", code: "SKILL_FRONTMATTER_INVALID"},
		{name: "numeric name", raw: "---\nname: 123\ndescription: Example\n---\n", code: "SKILL_FRONTMATTER_INVALID"},
		{name: "scalar metadata", raw: "---\nname: example\ndescription: Example\nmetadata: value\n---\n", code: "SKILL_FRONTMATTER_INVALID"},
		{name: "alias", raw: "---\nname: &name example\ndescription: *name\n---\n", code: "SKILL_FRONTMATTER_INVALID"},
		{name: "name", raw: "---\nname: Invalid_Name\ndescription: Example\n---\n", code: "SKILL_NAME_INVALID"},
		{name: "name length", raw: "---\nname: " + strings.Repeat("a", 65) + "\ndescription: Example\n---\n", code: "SKILL_NAME_INVALID"},
		{name: "description", raw: "---\nname: example\ndescription: \"\"\n---\n", code: "SKILL_DESCRIPTION_INVALID"},
		{name: "description length", raw: "---\nname: example\ndescription: " + strings.Repeat("a", 1025) + "\n---\n", code: "SKILL_DESCRIPTION_INVALID"},
		{name: "compatibility whitespace", raw: "---\nname: example\ndescription: Example\ncompatibility: \" \"\n---\n", code: "SKILL_COMPATIBILITY_INVALID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := NewRegistry().Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: MediaSkillMarkdown, Content: test.raw})
			if !hasFinding(result.Findings, test.code) {
				t.Fatalf("missing finding %s: %#v", test.code, result.Findings)
			}
		})
	}

	for _, content := range []string{
		"---\r\nname: example\r\ndescription: Example\r\n---\r\n# Body\r\n",
		"---\nname: example\ndescription: Example\n---",
	} {
		result := NewRegistry().Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: MediaSkillMarkdown, Content: content})
		if hasErrors(result.Findings) {
			t.Fatalf("valid Markdown variant returned %#v", result.Findings)
		}
	}
}

// Bounds apply to YAML-decoded strings, before display normalization. These
// cases complement the malformed-document tests above, not archive safety.
func TestSkillFrontmatterDecodedBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, fields, code, path string
	}{
		{name: "optional omitted", fields: "name: example\ndescription: Example\n"},
		{name: "optional empty", fields: "name: example\ndescription: Example\ncompatibility: \"\"\n", code: "SKILL_COMPATIBILITY_INVALID", path: "/compatibility"},
		{name: "optional empty block", fields: "name: example\ndescription: Example\ncompatibility: |-\n", code: "SKILL_COMPATIBILITY_INVALID", path: "/compatibility"},
		{name: "optional null", fields: "name: example\ndescription: Example\ncompatibility: null\n", code: "SKILL_FRONTMATTER_INVALID"},
		{name: "optional duplicate", fields: "name: example\ndescription: Example\ncompatibility: first\ncompatibility: second\n", code: "SKILL_FRONTMATTER_INVALID"},
		{name: "name minimum", fields: "name: a\ndescription: Example\n"},
		{name: "name maximum", fields: "name: " + strings.Repeat("a", 64) + "\ndescription: Example\n"},
		{name: "name padded", fields: "name: \" example \"\ndescription: Example\n", code: "SKILL_NAME_INVALID", path: "/name"},
		{name: "name escaped tabs", fields: "name: \"\\texample\\t\"\ndescription: Example\n", code: "SKILL_NAME_INVALID", path: "/name"},
		{name: "name syntax whitespace", fields: "name:   example  \ndescription: Example\n"},
		{name: "description minimum", fields: "name: example\ndescription: x\n"},
		{name: "description blank", fields: "name: example\ndescription: \" \\t \"\n", code: "SKILL_DESCRIPTION_INVALID", path: "/description"},
		{name: "description maximum", fields: "name: example\ndescription: " + strings.Repeat("a", 1024) + "\n"},
		{name: "description padded oversized", fields: "name: example\ndescription: \" " + strings.Repeat("a", 1024) + " \"\n", code: "SKILL_DESCRIPTION_INVALID", path: "/description"},
		{name: "description literal newline oversized", fields: "name: example\ndescription: |\n  " + strings.Repeat("a", 1024) + "\n", code: "SKILL_DESCRIPTION_INVALID", path: "/description"},
		{name: "description literal chomped maximum", fields: "name: example\ndescription: |-\n  " + strings.Repeat("a", 1024) + "\n"},
		{name: "description folded maximum", fields: "name: example\ndescription: >-\n  " + strings.Repeat("a", 511) + "\n  " + strings.Repeat("b", 512) + "\n"},
		{name: "description folded newline oversized", fields: "name: example\ndescription: >\n  " + strings.Repeat("a", 511) + "\n  " + strings.Repeat("b", 512) + "\n", code: "SKILL_DESCRIPTION_INVALID", path: "/description"},
		{name: "description Unicode maximum", fields: "name: example\ndescription: " + strings.Repeat("\u00e9", 1024) + "\n"},
		{name: "description Unicode oversized", fields: "name: example\ndescription: " + strings.Repeat("\u00e9", 1025) + "\n", code: "SKILL_DESCRIPTION_INVALID", path: "/description"},
		{name: "compatibility minimum", fields: "name: example\ndescription: Example\ncompatibility: x\n"},
		{name: "compatibility maximum", fields: "name: example\ndescription: Example\ncompatibility: " + strings.Repeat("x", 500) + "\n"},
		{name: "compatibility Unicode maximum", fields: "name: example\ndescription: Example\ncompatibility: " + strings.Repeat("\u00e9", 500) + "\n"},
		{name: "compatibility Unicode oversized", fields: "name: example\ndescription: Example\ncompatibility: " + strings.Repeat("\u00e9", 501) + "\n", code: "SKILL_COMPATIBILITY_INVALID", path: "/compatibility"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := NewRegistry().Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: MediaSkillMarkdown, Content: "---\n" + test.fields + "---\n# Body\n"})
			if test.code == "" {
				if hasErrors(result.Findings) {
					t.Fatalf("valid boundary rejected: %#v", result.Findings)
				}
				return
			}
			if len(result.Findings) != 1 || result.Findings[0].Code != test.code || result.Findings[0].Severity != catalog.SeverityError || result.Findings[0].Path != "/frontmatter"+test.path {
				t.Fatalf("expected %s at /frontmatter%s, got %#v", test.code, test.path, result.Findings)
			}
		})
	}
	// Both Markdown dispatch routes preserve the existing normalized display facts.
	for _, media := range []string{MediaSkillMarkdown, MediaSkillMarkdownARD} {
		result := NewRegistry().Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: media, Content: "---\r\nname: example\r\ndescription: \" Example \"\r\nlicense: \"\"\r\nmetadata: {}\r\nallowed-tools: \"\"\r\n---\r\n"})
		if hasErrors(result.Findings) || result.NormalizedFacts["description"] != "Example" || result.NormalizedFacts["name"] != "example" {
			t.Fatalf("valid facts changed: %#v", result)
		}
	}
}

func FuzzSkillMarkdownValidation(f *testing.F) {
	f.Add("---\nname: reconcile-invoices\ndescription: Reconcile disputed invoices\n---\n# Workflow\n")
	f.Add("---\nname: bad\nmetadata:\n  version: 1\n---\n")
	registry := NewRegistry()
	f.Fuzz(func(t *testing.T, content string) {
		if len(content) > 600_000 {
			t.Skip()
		}
		_ = registry.Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: MediaSkillMarkdown, Content: content})
	})
}

func hasErrors(findings []catalog.Finding) bool {
	for _, finding := range findings {
		if finding.Severity == catalog.SeverityError {
			return true
		}
	}
	return false
}

func hasFinding(findings []catalog.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func collectA2AFindings(validate func(*catalog.FindingCollector)) []catalog.Finding {
	report := catalog.NewFindingCollector(catalog.DefaultDiagnosticLimits())
	validate(report)
	return report.Findings()
}
