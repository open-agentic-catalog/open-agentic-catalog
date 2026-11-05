// Capabilities validation units use static responses without running a smoke
// workflow, constructing a Catalog or contacting a server.
package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The wire fixture follows Control OpenAPI and the capabilities producer. It
// advertises only the kinds and operations required by the current smoke journey.
const smokeCapabilitiesFixture = `{
	"resourceKinds": ["agent", "mcp_server", "skill"],
	"operations": ["catalog.lifecycle", "catalog.search", "publication.ard", "publication.ai_catalog", "registry.search", "registry.explore", "registry.list"],
	"profiles": {"ard": [], "aiCatalog": [], "agent": [], "mcp": [], "skill": [], "plugin": []},
	"governance": {"publicationAuthorization": "administrator_approved", "publisherAuthority": "not_assessed", "legalRights": "not_assessed"},
	"search": {"tier": "core", "profile": "core-lexical-v1", "semantic": "disabled", "degraded": false},
	"optionalCapabilities": {"credentialResolution": "disabled", "managedMedia": "unavailable", "softwareAssessment": "unavailable", "webhooks": "disabled"},
	"limits": {
		"requestBodyBytes": 2097152, "catalogDocumentBytes": 16777216,
		"pluginDocumentBytes": 500000, "pluginComponents": 128,
		"skillPackageBytes": 16777216, "skillPackageExpandedBytes": 67108864,
		"skillPackageFileBytes": 16777216, "skillPackageFiles": 1024,
		"skillPackageDepth": 16, "skillPackageExpansionRatio": 100,
		"catalogPageSize": 100, "ardPageSize": 100, "liveFederationPageSize": 50,
		"publicationRecords": 1000, "publicationProjectionBytes": 33554432,
		"publicationARDEntries": 1001, "publicationAICatalogEntries": 1000,
		"publicationExclusions": 1000, "publicationArtifactReferences": 1000,
		"publicationDocumentBytes": 16777216, "publicationSnapshotBytes": 37748736,
		"publicationWorkUnits": 100000, "publicationConcurrentConstructions": 1
	},
	"profileBundle": {"digest": "fixture", "artifacts": 0},
	"degradations": []
}`

func TestSmokeCapabilitiesAcceptsRequiredSubset(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "required capabilities", body: smokeCapabilitiesFixture},
		{name: "additional and reordered capabilities", body: strings.NewReplacer(
			`["agent", "mcp_server", "skill"]`, `["plugin", "skill", "agent", "mcp_server"]`,
			`["catalog.lifecycle", "catalog.search", "publication.ard", "publication.ai_catalog", "registry.search", "registry.explore", "registry.list"]`,
			`["source.inspect", "registry.list", "registry.explore", "registry.search", "publication.ai_catalog", "publication.ard", "catalog.search", "catalog.lifecycle"]`,
		).Replace(smokeCapabilitiesFixture)},
		// The detailed search-state matrix remains in TestSmokeSearchCapabilityModes.
		{name: "degraded hybrid remains usable", body: strings.NewReplacer(
			`"tier": "core"`, `"tier": "hybrid"`,
			`"profile": "core-lexical-v1"`, `"profile": "sha256:`+strings.Repeat("a", 64)+`"`,
			`"semantic": "disabled"`, `"semantic": "enabled"`,
			`"degraded": false`, `"degraded": true`,
		).Replace(smokeCapabilitiesFixture)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateSmokeCapabilities(smokeHTTPResponse{Body: []byte(test.body)}); err != nil {
				t.Fatalf("valid capabilities rejected: %v", err)
			}
		})
	}
}

func TestSmokeCapabilitiesRejectsInconsistentState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, original, replacement, want string
	}{
		{"publication authorization", `"publicationAuthorization": "administrator_approved"`, `"publicationAuthorization": "automatic"`, "unexpected governance state"},
		{"publisher authority", `"publisherAuthority": "not_assessed"`, `"publisherAuthority": "trusted"`, "unexpected governance state"},
		{"legal rights", `"legalRights": "not_assessed"`, `"legalRights": "verified"`, "unexpected governance state"},
		{"search validation handoff", `"semantic": "disabled"`, `"semantic": "unknown"`, "unexpected search state"},
		{"document bytes", `"catalogDocumentBytes": 16777216`, `"catalogDocumentBytes": 16777217`, "bounded document limit"},
		{"publication records", `"publicationRecords": 1000`, `"publicationRecords": 1001`, "publication work limits are inconsistent"},
		{"projection bytes", `"publicationProjectionBytes": 33554432`, `"publicationProjectionBytes": 33554433`, "publication work limits are inconsistent"},
		{"publication document bytes", `"publicationDocumentBytes": 16777216`, `"publicationDocumentBytes": 16777217`, "publication work limits are inconsistent"},
		{"snapshot bytes", `"publicationSnapshotBytes": 37748736`, `"publicationSnapshotBytes": 37748737`, "publication work limits are inconsistent"},
		{"work units", `"publicationWorkUnits": 100000`, `"publicationWorkUnits": 100001`, "publication work limits are inconsistent"},
		{"concurrent constructions", `"publicationConcurrentConstructions": 1`, `"publicationConcurrentConstructions": 2`, "publication work limits are inconsistent"},
		{"agent absent", `"agent", `, "", `resource kind "agent" is absent`},
		{"MCP server absent", `"mcp_server", `, "", `resource kind "mcp_server" is absent`},
		{"skill absent", `, "skill"]`, "]", `resource kind "skill" is absent`},
		{"lifecycle absent", `"catalog.lifecycle", `, "", `operation "catalog.lifecycle" is absent`},
		{"Catalog search absent", `"catalog.search", `, "", `operation "catalog.search" is absent`},
		{"ARD publication absent", `"publication.ard", `, "", `operation "publication.ard" is absent`},
		{"AI Catalog publication absent", `"publication.ai_catalog", `, "", `operation "publication.ai_catalog" is absent`},
		{"Registry search absent", `"registry.search", `, "", `operation "registry.search" is absent`},
		{"Registry Explore absent", `"registry.explore", `, "", `operation "registry.explore" is absent`},
		{"Registry List absent", `, "registry.list"]`, "]", `operation "registry.list" is absent`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if strings.Count(smokeCapabilitiesFixture, test.original) != 1 {
				t.Fatal("case must change exactly one fixture field or array member")
			}
			body := []byte(strings.Replace(smokeCapabilitiesFixture, test.original, test.replacement, 1))
			if !json.Valid(body) {
				t.Fatal("rejection fixture must remain valid JSON")
			}
			err := validateSmokeCapabilities(smokeHTTPResponse{Body: body})
			if err == nil || !strings.Contains(err.Error(), "authenticated capabilities check: "+test.want) {
				t.Fatalf("error = %v; want capabilities context and %q", err, test.want)
			}
		})
	}
}

func TestSmokeCapabilitiesPreservesDecodeFailure(t *testing.T) {
	t.Parallel()
	// Decoder permutations are covered by TestSmokeResponseAndRunHelpers; this
	// case verifies the capabilities context and preservation of the decode cause.
	err := validateSmokeCapabilities(smokeHTTPResponse{Body: []byte(`{"resourceKinds": !}`)})
	var syntaxError *json.SyntaxError
	if !errors.As(err, &syntaxError) || !strings.HasPrefix(err.Error(), "authenticated capabilities check: decode JSON response:") {
		t.Fatalf("error = %v; want wrapped JSON syntax error with capabilities context", err)
	}
}
