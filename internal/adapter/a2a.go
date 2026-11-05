// A2A validation combines strict pinned ProtoJSON decoding with semantic checks
// for Agent Card interfaces, security schemes and signatures.

package adapter

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"mime"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/a2apb"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

var a2aProtocolVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// validateA2AAgentCard strictly decodes the pinned ProtoJSON card and reports
// required-field, interface, security and signature-shape findings.
func validateA2AAgentCard(raw json.RawMessage, limits catalog.DiagnosticLimits) (*a2apb.AgentCard, []catalog.Finding) {
	card := &a2apb.AgentCard{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, card); err != nil {
		return nil, []catalog.Finding{errorFinding("A2A_PROTOJSON_INVALID", "", "The A2A Agent Card does not conform to the pinned ProtoJSON contract.")}
	}
	findings := catalog.NewFindingCollector(limits)
	required := func(value, path, field string) {
		if strings.TrimSpace(value) == "" {
			findings.Add(errorFinding("A2A_REQUIRED_FIELD", path, "A2A requires a non-empty "+field+"."))
		}
	}
	required(card.GetName(), "/name", "name")
	required(card.GetDescription(), "/description", "description")
	required(card.GetVersion(), "/version", "version")
	if card.GetCapabilities() == nil {
		findings.Add(errorFinding("A2A_REQUIRED_FIELD", "/capabilities", "A2A requires a capabilities object."))
	}
	if len(card.GetSupportedInterfaces()) == 0 {
		findings.Add(errorFinding("A2A_REQUIRED_FIELD", "/supportedInterfaces", "A2A requires at least one supported interface."))
	}
	for index, agentInterface := range card.GetSupportedInterfaces() {
		if findings.Exhausted() {
			break
		}
		path := fmt.Sprintf("/supportedInterfaces/%d", index)
		if agentInterface == nil || strings.TrimSpace(agentInterface.GetProtocolBinding()) == "" || !a2aProtocolVersionPattern.MatchString(agentInterface.GetProtocolVersion()) {
			findings.Add(errorFinding("A2A_INTERFACE_INVALID", path, "Each A2A interface requires a non-empty protocolBinding and a major.minor protocolVersion."))
			continue
		}
		if !catalog.ValidExternalHTTPSURL(agentInterface.GetUrl()) {
			findings.Add(errorFinding("A2A_INTERFACE_INVALID", path+"/url", "A production A2A interface must use a safe absolute HTTPS URL."))
		}
	}
	if provider := card.GetProvider(); provider != nil {
		if !catalog.ValidExternalHTTPSURL(provider.GetUrl()) || strings.TrimSpace(provider.GetOrganization()) == "" {
			findings.Add(errorFinding("A2A_PROVIDER_INVALID", "/provider", "An A2A provider requires an organization and a safe absolute HTTPS URL."))
		}
	}
	for _, optionalURL := range []struct {
		value string
		path  string
	}{{card.GetDocumentationUrl(), "/documentationUrl"}, {card.GetIconUrl(), "/iconUrl"}} {
		if optionalURL.value != "" && !catalog.ValidExternalHTTPSURL(optionalURL.value) {
			findings.Add(errorFinding("A2A_URL_INVALID", optionalURL.path, "The A2A URL must be a safe absolute HTTPS URL."))
		}
	}
	validateA2AMediaTypes(card.GetDefaultInputModes(), "/defaultInputModes", findings)
	validateA2AMediaTypes(card.GetDefaultOutputModes(), "/defaultOutputModes", findings)
	if len(card.GetSkills()) == 0 {
		findings.Add(errorFinding("A2A_REQUIRED_FIELD", "/skills", "A2A requires at least one Agent Skill."))
	}
	for index, skill := range card.GetSkills() {
		if findings.Exhausted() {
			break
		}
		path := fmt.Sprintf("/skills/%d", index)
		if skill == nil || strings.TrimSpace(skill.GetId()) == "" || strings.TrimSpace(skill.GetName()) == "" || strings.TrimSpace(skill.GetDescription()) == "" || len(skill.GetTags()) == 0 {
			findings.Add(errorFinding("A2A_SKILL_INVALID", path, "Each A2A Agent Skill requires id, name, description, and at least one tag."))
			continue
		}
		for tagIndex, tag := range skill.GetTags() {
			if findings.Exhausted() {
				break
			}
			if strings.TrimSpace(tag) == "" {
				findings.Add(errorFinding("A2A_SKILL_INVALID", fmt.Sprintf("%s/tags/%d", path, tagIndex), "A2A Agent Skill tags must be non-empty."))
			}
		}
		validateA2AMediaTypesOptional(skill.GetInputModes(), path+"/inputModes", findings)
		validateA2AMediaTypesOptional(skill.GetOutputModes(), path+"/outputModes", findings)
		validateA2ASecurityRequirements(skill.GetSecurityRequirements(), card.GetSecuritySchemes(), path+"/securityRequirements", findings)
	}
	validateA2ASecuritySchemes(card.GetSecuritySchemes(), findings)
	validateA2ASecurityRequirements(card.GetSecurityRequirements(), card.GetSecuritySchemes(), "/securityRequirements", findings)
	for index, signature := range card.GetSignatures() {
		if findings.Exhausted() {
			break
		}
		path := fmt.Sprintf("/signatures/%d", index)
		if signature == nil || strings.TrimSpace(signature.GetProtected()) == "" || strings.TrimSpace(signature.GetSignature()) == "" {
			findings.Add(errorFinding("A2A_SIGNATURE_INVALID", path, "Each A2A Agent Card signature requires protected and signature values."))
			continue
		}
		protected := parseA2AProtectedHeader(signature.GetProtected())
		if protected == nil {
			findings.Add(errorFinding("A2A_SIGNATURE_INVALID", path+"/protected", "The protected JWS header must be an unambiguous base64url JSON object with a signing alg and a string kid."))
		} else {
			for name := range signature.GetHeader().GetFields() {
				if findings.Exhausted() {
					break
				}
				if _, exists := protected[name]; exists {
					findings.Add(errorFinding("A2A_SIGNATURE_INVALID", path+"/header", "Protected and unprotected JWS header names must be disjoint."))
					break
				}
			}
		}
		if _, err := base64.RawURLEncoding.DecodeString(signature.GetSignature()); err != nil || strings.ContainsAny(signature.GetSignature(), "\r\n") {
			findings.Add(errorFinding("A2A_SIGNATURE_INVALID", path+"/signature", "The JWS signature must be base64url encoded without padding or whitespace."))
		}
	}
	return card, findings.Findings()
}

// parseA2AProtectedHeader returns exact header members after structural checks,
// or nil for invalid metadata. It does not verify the signature or resolve keys.
func parseA2AProtectedHeader(value string) map[string]any {
	// Go's base64 decoder ignores CR/LF, but JWS encoded fields forbid them.
	if strings.ContainsAny(value, "\r\n") {
		return nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || jsondoc.Validate(decoded) != nil {
		return nil
	}
	var header map[string]any
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.UseNumber()
	if err := decoder.Decode(&header); err != nil {
		return nil
	}
	// Struct decoding matches field names case-insensitively. JOSE names are
	// exact, and the pinned A2A profile requires both alg and kid protected.
	algorithm, hasAlgorithm := header["alg"].(string)
	_, hasKeyID := header["kid"].(string)
	if !hasAlgorithm || strings.TrimSpace(algorithm) == "" || strings.EqualFold(algorithm, "none") || !hasKeyID {
		return nil
	}
	return header
}

// validateA2AMediaTypes requires at least one default interaction media type
// and validates each spelling.
func validateA2AMediaTypes(values []string, path string, findings *catalog.FindingCollector) {
	if findings.Exhausted() {
		return
	}
	if len(values) == 0 {
		findings.Add(errorFinding("A2A_REQUIRED_FIELD", path, "A2A requires at least one default media type."))
		return
	}
	validateA2AMediaTypesOptional(values, path, findings)
}

// validateA2AMediaTypesOptional reports invalid type/subtype spellings at their
// indexed JSON paths.
func validateA2AMediaTypesOptional(values []string, path string, findings *catalog.FindingCollector) {
	if findings.Exhausted() {
		return
	}
	for index, value := range values {
		if findings.Exhausted() {
			break
		}
		mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
		if err != nil || !strings.Contains(mediaType, "/") {
			findings.Add(errorFinding("A2A_MEDIA_TYPE_INVALID", fmt.Sprintf("%s/%d", path, index), "A2A interaction modes must be valid media types."))
		}
	}
}

// validateA2ASecuritySchemes checks named scheme definitions and
// transport-specific required metadata without authenticating a runtime.
func validateA2ASecuritySchemes(schemes map[string]*a2apb.SecurityScheme, findings *catalog.FindingCollector) {
	if findings.Exhausted() {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(schemes)) {
		if findings.Exhausted() {
			break
		}
		scheme := schemes[name]
		path := name
		if len(name) <= catalog.MaxFindingPathBytes {
			path = "/securitySchemes/" + escapeJSONPointer(name)
		}
		if strings.TrimSpace(name) == "" || scheme == nil || scheme.GetScheme() == nil {
			findings.Add(errorFinding("A2A_SECURITY_SCHEME_INVALID", path, "Each A2A security scheme needs a non-empty name and exactly one scheme definition."))
			continue
		}
		switch {
		case scheme.GetApiKeySecurityScheme() != nil:
			value := scheme.GetApiKeySecurityScheme()
			if (value.GetLocation() != "query" && value.GetLocation() != "header" && value.GetLocation() != "cookie") || strings.TrimSpace(value.GetName()) == "" {
				findings.Add(errorFinding("A2A_SECURITY_SCHEME_INVALID", path, "An API key scheme requires location query, header, or cookie and a name."))
			}
		case scheme.GetHttpAuthSecurityScheme() != nil:
			if strings.TrimSpace(scheme.GetHttpAuthSecurityScheme().GetScheme()) == "" {
				findings.Add(errorFinding("A2A_SECURITY_SCHEME_INVALID", path, "An HTTP authentication scheme requires its registered scheme name."))
			}
		case scheme.GetOauth2SecurityScheme() != nil:
			validateA2AOAuth2(scheme.GetOauth2SecurityScheme(), path, findings)
		case scheme.GetOpenIdConnectSecurityScheme() != nil:
			if !catalog.ValidExternalHTTPSURL(scheme.GetOpenIdConnectSecurityScheme().GetOpenIdConnectUrl()) {
				findings.Add(errorFinding("A2A_SECURITY_SCHEME_INVALID", path, "An OpenID Connect scheme requires a safe absolute HTTPS discovery URL."))
			}
		case scheme.GetMtlsSecurityScheme() != nil:
		default:
			findings.Add(errorFinding("A2A_SECURITY_SCHEME_INVALID", path, "The A2A security scheme type is not recognized."))
		}
	}
}

// validateA2AOAuth2 checks the declared flow's required endpoints, optional
// refresh URL and applicable scopes map.
func validateA2AOAuth2(scheme *a2apb.OAuth2SecurityScheme, path string, findings *catalog.FindingCollector) {
	if findings.Exhausted() {
		return
	}
	if scheme.GetFlows() == nil || scheme.GetFlows().GetFlow() == nil {
		findings.Add(errorFinding("A2A_SECURITY_SCHEME_INVALID", path+"/oauth2SecurityScheme/flows", "An OAuth 2.0 scheme requires one configured flow."))
		return
	}
	if scheme.GetOauth2MetadataUrl() != "" && !catalog.ValidExternalHTTPSURL(scheme.GetOauth2MetadataUrl()) {
		findings.Add(errorFinding("A2A_SECURITY_SCHEME_INVALID", path+"/oauth2SecurityScheme/oauth2MetadataUrl", "OAuth metadata must use a safe absolute HTTPS URL."))
	}
	flows := scheme.GetFlows()
	validURL := catalog.ValidExternalHTTPSURL
	//lint:ignore SA1019 The pinned A2A profile still accepts this deprecated flow and the Catalog must validate it safely.
	implicit := flows.GetImplicit() //nolint:staticcheck // The pinned profile still accepts this deprecated flow.
	//lint:ignore SA1019 The pinned A2A profile still accepts this deprecated flow and the Catalog must validate it safely.
	password := flows.GetPassword() //nolint:staticcheck // The pinned profile still accepts this deprecated flow.
	switch {
	case flows.GetAuthorizationCode() != nil:
		flow := flows.GetAuthorizationCode()
		if !validURL(flow.GetAuthorizationUrl()) || !validURL(flow.GetTokenUrl()) || flow.GetScopes() == nil {
			findings.Add(errorFinding("A2A_OAUTH_FLOW_INVALID", path, "Authorization Code requires HTTPS authorization and token URLs and a scopes map."))
		}
		validateA2AOptionalURL(flow.GetRefreshUrl(), path+"/refreshUrl", findings)
	case flows.GetClientCredentials() != nil:
		flow := flows.GetClientCredentials()
		if !validURL(flow.GetTokenUrl()) || flow.GetScopes() == nil {
			findings.Add(errorFinding("A2A_OAUTH_FLOW_INVALID", path, "Client Credentials requires an HTTPS token URL and a scopes map."))
		}
		validateA2AOptionalURL(flow.GetRefreshUrl(), path+"/refreshUrl", findings)
	case flows.GetDeviceCode() != nil:
		flow := flows.GetDeviceCode()
		if !validURL(flow.GetDeviceAuthorizationUrl()) || !validURL(flow.GetTokenUrl()) || flow.GetScopes() == nil {
			findings.Add(errorFinding("A2A_OAUTH_FLOW_INVALID", path, "Device Code requires HTTPS device authorization and token URLs and a scopes map."))
		}
		validateA2AOptionalURL(flow.GetRefreshUrl(), path+"/refreshUrl", findings)
	case implicit != nil:
		flow := implicit
		if !validURL(flow.GetAuthorizationUrl()) {
			findings.Add(errorFinding("A2A_OAUTH_FLOW_INVALID", path, "Implicit OAuth requires an HTTPS authorization URL."))
		}
		validateA2AOptionalURL(flow.GetRefreshUrl(), path+"/refreshUrl", findings)
	case password != nil:
		flow := password
		if !validURL(flow.GetTokenUrl()) {
			findings.Add(errorFinding("A2A_OAUTH_FLOW_INVALID", path, "Password OAuth requires an HTTPS token URL."))
		}
		validateA2AOptionalURL(flow.GetRefreshUrl(), path+"/refreshUrl", findings)
	}
}

// validateA2AOptionalURL accepts an omitted refresh endpoint or a safe retained
// HTTPS reference.
func validateA2AOptionalURL(value, path string, findings *catalog.FindingCollector) {
	if findings.Exhausted() {
		return
	}
	if value == "" || catalog.ValidExternalHTTPSURL(value) {
		return
	}
	findings.Add(errorFinding("A2A_OAUTH_FLOW_INVALID", path, "OAuth refresh URLs must use safe absolute HTTPS URLs."))
}

// validateA2ASecurityRequirements rejects null requirement objects and
// references to undefined named schemes.
func validateA2ASecurityRequirements(requirements []*a2apb.SecurityRequirement, schemes map[string]*a2apb.SecurityScheme, path string, findings *catalog.FindingCollector) {
	if findings.Exhausted() {
		return
	}
	for index, requirement := range requirements {
		if findings.Exhausted() {
			break
		}
		if requirement == nil {
			findings.Add(errorFinding("A2A_SECURITY_REQUIREMENT_INVALID", fmt.Sprintf("%s/%d", path, index), "An A2A security requirement must be an object."))
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(requirement.GetSchemes())) {
			if findings.Exhausted() {
				break
			}
			if _, exists := schemes[name]; !exists {
				findings.Add(errorFinding("A2A_SECURITY_REQUIREMENT_INVALID", fmt.Sprintf("%s/%d", path, index), "An A2A security requirement references an undefined scheme."))
			}
		}
	}
}

// escapeJSONPointer escapes a map key as one RFC 6901 pointer segment.
func escapeJSONPointer(value string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(value)
}
