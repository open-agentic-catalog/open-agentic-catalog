package adapter

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestA2ANestedDiagnosticBudgets(t *testing.T) {
	limits := catalog.DiagnosticLimits{MaxFindings: 2, MaxDiagnosticBytes: 1024}
	r, err := NewRegistryWithLimits(limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, member, valid, invalid, code, path string
		skill                                    bool
	}{
		{"interfaces", "supportedInterfaces", `[{"url":"https://agents.example.com/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}]`, `[{},{},{},{}]`, "A2A_INTERFACE_INVALID", "/supportedInterfaces/0", false},
		{"tags", "tags", `["text"]`, `["","","",""]`, "A2A_SKILL_INVALID", "/skills/0/tags/0", true},
		{"skill modes", "inputModes", `["text/plain"]`, `["invalid","invalid","invalid","invalid"]`, "A2A_MEDIA_TYPE_INVALID", "/skills/0/inputModes/0", true},
		{"schemes", "securitySchemes", `{"a":{"mtlsSecurityScheme":{}}}`, `{"d":{},"c":{},"b":{},"a":{}}`, "A2A_SECURITY_SCHEME_INVALID", "/securitySchemes/a", false},
		{"requirement array", "securityRequirements", `[{}]`, `[{"schemes":{"a":{}}},{"schemes":{"b":{}}},{"schemes":{"c":{}}},{"schemes":{"d":{}}}]`, "A2A_SECURITY_REQUIREMENT_INVALID", "/securityRequirements/0", false},
		{"requirement map", "securityRequirements", `[{}]`, `[{"schemes":{"d":{},"c":{},"b":{},"a":{}}}]`, "A2A_SECURITY_REQUIREMENT_INVALID", "/skills/0/securityRequirements/0", true},
		{"signatures", "signatures", `[{"protected":"` + base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","kid":"fixture"}`)) + `","signature":"c2ln"}]`, `[{},{},{},{}]`, "A2A_SIGNATURE_INVALID", "/signatures/0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var card map[string]any
			if err := json.Unmarshal([]byte(a2aBoundaryCard), &card); err != nil {
				t.Fatal(err)
			}
			target := card
			if tc.skill {
				skills, ok := card["skills"].([]any)
				if !ok || len(skills) != 1 {
					t.Fatal("control card must contain one skill")
				}
				target, ok = skills[0].(map[string]any)
				if !ok {
					t.Fatal("control skill must be an object")
				}
			}
			validate := func(value string) catalog.ValidationResult {
				t.Helper()
				target[tc.member] = json.RawMessage(value)
				raw, err := json.Marshal(card)
				if err != nil {
					t.Fatal(err)
				}
				return r.Validate(t.Context(), catalog.KindAgent, catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: raw})
			}
			if control := validate(tc.valid); len(control.Findings) != 0 {
				t.Fatalf("valid control failed: %+v", control.Findings)
			}
			result := validate(tc.invalid)
			assertNativeDiagnosticBudget(t, result, limits)
			if len(result.Findings) != 2 || result.Findings[0].Code != tc.code || result.Findings[0].Path != tc.path || result.Findings[0].Severity != catalog.SeverityError {
				t.Fatalf("wrong nested producer or retained prefix: %+v", result.Findings)
			}
		})
	}
}

func TestZeroRegistryUsesDefaultDiagnosticBudget(t *testing.T) {
	var registry Registry
	input := catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: json.RawMessage(a2aBoundaryCard)}
	if result := registry.Validate(t.Context(), catalog.KindAgent, input); len(result.Findings) != 0 {
		t.Fatalf("zero Registry rejects valid input: %+v", result.Findings)
	}
	input.Data = json.RawMessage(strings.Replace(a2aBoundaryCard, `"tags":["text"]`, `"tags":[`+strings.Repeat(`"",`, 129)+`""]`, 1))
	result := registry.Validate(t.Context(), catalog.KindAgent, input)
	assertNativeDiagnosticBudget(t, result, catalog.DefaultDiagnosticLimits())
	if len(result.Findings) != 128 || result.Findings[0].Path != "/skills/0/tags/0" {
		t.Fatalf("zero Registry lost default prefix: %+v", result.Findings)
	}
}

func TestNativeDiagnosticBudgetsAndValidLargeCards(t *testing.T) {
	limits := catalog.DiagnosticLimits{MaxFindings: 4, MaxDiagnosticBytes: 1024}
	r, err := NewRegistryWithLimits(limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistryWithLimits(catalog.DiagnosticLimits{}); err == nil {
		t.Fatal("invalid budget accepted")
	}
	card := map[string]any{
		"name": "Agent", "description": "Fixture", "version": "1.0.0",
		"supportedInterfaces": []any{map[string]any{"url": "https://agents.example.com/a2a", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}},
		"capabilities":        map[string]any{}, "defaultInputModes": []string{"application/json"}, "defaultOutputModes": []string{"application/json"},
		"skills": []any{map[string]any{"id": "test", "name": "Test", "tags": []string{"test"}, "description": strings.Repeat("x", 1<<20)}},
	}
	validate := func() catalog.ValidationResult {
		t.Helper()
		raw, marshalErr := json.Marshal(card)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return r.Validate(t.Context(), catalog.KindAgent, catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: raw})
	}
	if got := validate(); len(got.Findings) != 0 {
		t.Fatalf("valid large Card blocked: %+v", got.Findings)
	}
	// A long but valid scheme name is not itself a diagnostic or a new input limit.
	card["securitySchemes"] = map[string]any{strings.Repeat("s", 2048): map[string]any{"mtlsSecurityScheme": map[string]any{}}}
	if got := validate(); len(got.Findings) != 0 {
		t.Fatalf("valid scheme name blocked: %+v", got.Findings)
	}
	delete(card, "securitySchemes")
	validSkills := card["skills"]
	invalidSkills := make([]any, 50000)
	for i := range invalidSkills {
		invalidSkills[i] = map[string]any{}
	}
	card["skills"] = invalidSkills
	assertNativeDiagnosticBudget(t, validate(), limits)
	card["skills"] = validSkills
	card["defaultInputModes"] = strings.Split(strings.Repeat("invalid,", 50000), ",")
	assertNativeDiagnosticBudget(t, validate(), limits)
	card["defaultInputModes"] = []string{"application/json"}
	card["securitySchemes"] = map[string]any{"z": map[string]any{}, "a": map[string]any{}, "c": map[string]any{}, "b": map[string]any{}, "d": map[string]any{}}
	first := validate()
	assertNativeDiagnosticBudget(t, first, limits)
	for range 10 {
		if next := validate(); !reflect.DeepEqual(first.Findings, next.Findings) {
			t.Fatal("diagnostic selection depends on map iteration")
		}
	}
	card["securitySchemes"] = map[string]any{strings.Repeat("/", 2048): map[string]any{}}
	assertNativeDiagnosticBudget(t, validate(), limits)

	remotes := make([]any, 50000)
	for i := range remotes {
		remotes[i] = map[string]any{"url": "http://unsafe.example"}
	}
	raw, err := json.Marshal(map[string]any{"name": "example/mcp", "version": "1.0.0", "remotes": remotes})
	if err != nil {
		t.Fatal(err)
	}
	for _, media := range []string{MediaMCPRegistry, MediaMCPServerCard} {
		assertNativeDiagnosticBudget(t, r.Validate(t.Context(), catalog.KindMCPServer, catalog.RepresentationInput{MediaType: media, Data: raw}), limits)
	}
}

func TestPluginDiagnosticExhaustionIsExplicit(t *testing.T) {
	limits := catalog.DiagnosticLimits{MaxFindings: 2, MaxDiagnosticBytes: 1024}
	body := pluginArchive(t, catalog.PluginMediaZIP, map[string]string{
		"plugin.json": pluginManifest,
		"mcp.json":    `{"mcpServers":{"a":{"type":"stdio"},"b":{"type":"stdio"},"c":{"type":"stdio"}}}`,
	})
	result := InspectPlugin(t.Context(), catalog.PluginMediaZIP, body, limits)
	assertNativeDiagnosticBudget(t, result, limits)
	if result.Plugin == nil || len(result.Plugin.Documents) != 2 {
		t.Fatal("bounded invalid inspection lost raw evidence")
	}
	original, err := json.Marshal(result.Plugin)
	if err != nil {
		t.Fatal(err)
	}
	revalidated := ValidatePluginInspection(t.Context(), result.Plugin, limits)
	assertNativeDiagnosticBudget(t, revalidated, limits)
	after, err := json.Marshal(result.Plugin)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("revalidation rewrote retained Plugin inspection")
	}
	valid := InspectPlugin(t.Context(), catalog.PluginMediaZIP, pluginArchive(t, catalog.PluginMediaZIP, map[string]string{"plugin.json": pluginManifest}), limits)
	if valid.Plugin == nil || len(valid.Findings) != 0 {
		t.Fatalf("valid Plugin rejected: %+v", valid.Findings)
	}
}

func assertNativeDiagnosticBudget(t *testing.T, result catalog.ValidationResult, limits catalog.DiagnosticLimits) {
	t.Helper()
	raw, err := json.Marshal(result.Findings)
	if err != nil || len(raw) > limits.MaxDiagnosticBytes || len(result.Findings) > limits.MaxFindings {
		t.Fatalf("findings count=%d bytes=%d error=%v", len(result.Findings), len(raw), err)
	}
	markers := 0
	for _, f := range result.Findings {
		if f.Code == catalog.DiagnosticBudgetExhausted {
			markers++
			if f.Severity != catalog.SeverityError {
				t.Fatal("exhaustion was not blocking")
			}
		}
	}
	if markers != 1 {
		t.Fatalf("exhaustion markers=%d findings=%+v", markers, result.Findings)
	}
}
