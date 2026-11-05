//go:build minikube

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGatewaySettingsKeepNativeAuthAndDisableExternalWork(t *testing.T) {
	settings, err := gatewayEnvironment([]string{"admin-secret", "reader-secret", "private-secret", "signing-secret", "marker-secret", "invalid-secret"})
	if err != nil {
		t.Fatal(err)
	}
	for name, wanted := range map[string]string{"REGISTRY_URL": gatewayOrigin, "AUTH_PROVIDER": "cognito", "DISABLE_AI_REGISTRY_TOOLS_SERVER": "true", "SECURITY_SCAN_ENABLED": "false", "ARD_PUBLISHER_DOMAIN": "gateway.gateway-qualification.svc.cluster.local", "DEPLOYMENT_MODE": "registry-only", "REGISTRY_STATIC_TOKEN_AUTH_ENABLED": "true", "AUTH_SERVER_URL": "http://auth-server:8888", "HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1", "MCP_TELEMETRY_DISABLED": "1", "UPDATE_CHECK_ENABLED": "false", "ANS_INTEGRATION_ENABLED": "false", "OTEL_SDK_DISABLED": "true", "METRICS_LEGACY_HTTP_POST": "false", "EGRESS_AUTH_ENABLED": "false", "DOCUMENTDB_USE_TLS": "false"} {
		if settings[name] != wanted {
			t.Fatalf("%s=%q; want %q", name, settings[name], wanted)
		}
	}
	var keys map[string]struct {
		Key    string   `json:"key"`
		Groups []string `json:"groups"`
	}
	if err := json.Unmarshal([]byte(settings["REGISTRY_API_KEYS"]), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys["reader"].Key != "reader-secret" || strings.Join(keys["reader"].Groups, ",") != "qualified-reader" || strings.Join(keys["private"].Groups, ",") != "qualified-private" {
		t.Fatal("static group mapping changed", keys)
	}
}

func TestGatewayRunningIdentityRejectsAmbiguousUnreadyOrWrongImages(t *testing.T) {
	expected := agntcyImage{Reference: "owned:gateway", ID: "sha256:exact"}
	valid := `{"items":[{"spec":{"containers":[{"image":"owned:gateway"}]},"status":{"containerStatuses":[{"ready":true,"imageID":"docker://sha256:exact"}]}}]}`
	if id, err := gatewayRunningImage([]byte(valid), expected); err != nil || id != expected.ID {
		t.Fatal(id, err)
	}
	for _, raw := range []string{`{}`, `not-json`, strings.Replace(valid, "true", "false", 1), strings.Replace(valid, "sha256:exact", "sha256:other", 1), strings.Replace(valid, "owned:gateway", "owned:other", 1), strings.Replace(valid, `"containers":[{"image":"owned:gateway"}]`, `"containers":[]`, 1)} {
		if _, err := gatewayRunningImage([]byte(raw), expected); err == nil {
			t.Fatal("admitted invalid inventory", raw)
		}
	}
}

func TestGatewayScenarioUsesPrivateEnvironmentAndScopedProgress(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://ambient.invalid")
	h := harness{options: options{scenario: "gateway"}, kubeconfig: "/owned/kubeconfig", profile: "owned-profile", minikubeHome: "/owned/home"}
	for _, entry := range h.environment() {
		if strings.HasPrefix(entry, "HTTP_PROXY=") {
			t.Fatal("inherited proxy", entry)
		}
	}
	if h.managedBuild() != nil || h.managedWorkloads() != nil {
		t.Fatal("unexpected managed state")
	}
	h.gateway = &gatewayState{report: gatewayReport{Build: map[string]string{"version": "exact"}, Workloads: map[string]string{}}}
	h.setManagedCatalogValues("hash")
	if h.managedBuild()["version"] != "exact" || h.gateway.report.CatalogValues != "hash" {
		t.Fatal("missing managed identity")
	}
	h.managedWorkloads()["migration"] = "image"
	if h.gateway.report.Workloads["migration"] != "image" {
		t.Fatal("missing migration identity")
	}
}

func TestGatewayQualificationRejectsIncompleteSuccessAndMissingDenials(t *testing.T) {
	for _, raw := range []string{`{}`, `{"status":"pass"}`, `{"status":"fail"}`, `invalid`} {
		if validateGatewayQualification([]byte(raw)) == nil {
			t.Fatal("admitted incomplete native qualification", raw)
		}
	}
	evidence := map[string]any{}
	for _, key := range []string{"publisher", "descriptors", "tls", "current_discovery", "groups", "characterization", "explore", "no_match", "list_no_match", "list", "list_reader", "list_private", "list_types", "list_media", "list_tag", "list_after_denial", "search", "search_reader", "search_private", "search_types", "search_media", "search_tag", "search_after_denial"} {
		evidence[key] = map[string]bool{"present": true}
	}
	official := map[string]any{}
	for _, name := range []string{"manifest", "publisher", "registry"} {
		official[name] = map[string]any{"exit": 1, "output": "CONFORMANCE STATUS: FAIL"}
	}
	evidence["official"] = official
	captures := []map[string]any{
		{"path": "/.well-known/ard.json", "credential": "missing", "status": 404},
		{"path": "/.well-known/ai-catalog.json", "credential": "missing", "status": 200},
		{"path": "/api/ard/agents", "credential": "missing", "status": 401},
		{"path": "/api/ard/agents", "credential": "invalid", "status": 401},
		{"path": "/api/ard/search", "credential": "missing", "status": 401},
		{"path": "/api/ard/search", "credential": "invalid", "status": 401},
		{"path": "/api/servers/toggle", "credential": "reader", "status": 403},
		{"path": "/api/ard/explore", "credential": "admin", "status": 405},
	}
	report := map[string]any{"status": "pass", "evidence": evidence, "captures": captures}
	verify := func(wantError bool) {
		t.Helper()
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateGatewayQualification(raw); (err != nil) != wantError {
			t.Fatal("qualification admission", err)
		}
	}
	verify(false)
	report["error"] = "contradictory success"
	verify(true)
	delete(report, "error")
	for _, key := range []string{"publisher", "search_private", "tls"} {
		value := evidence[key]
		delete(evidence, key)
		verify(true)
		evidence[key] = value
	}
	delete(official, "registry")
	verify(true)
	official["registry"] = map[string]any{"output": "CONFORMANCE STATUS: PASS"}
	verify(true)
	official["registry"] = map[string]any{"exit": 0, "output": "incomplete"}
	verify(true)
	official["registry"] = map[string]any{"exit": 1, "output": "CONFORMANCE STATUS: FAIL"}
	captures[3]["status"] = 200
	verify(true)
}

func TestGatewayBootstrapRequiresCompletedExactImage(t *testing.T) {
	image := agntcyImage{Reference: "owned:bootstrap", ID: "sha256:exact"}
	valid := `{"items":[{"spec":{"containers":[{"image":"owned:bootstrap"}]},"status":{"phase":"Succeeded","containerStatuses":[{"ready":false,"imageID":"docker://sha256:exact"}]}}]}`
	if _, err := gatewayPodImage([]byte(valid), image, true); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{strings.Replace(valid, "Succeeded", "Failed", 1), strings.Replace(valid, "sha256:exact", "sha256:other", 1)} {
		if _, err := gatewayPodImage([]byte(raw), image, true); err == nil {
			t.Fatal("admitted invalid native bootstrap")
		}
	}
}
