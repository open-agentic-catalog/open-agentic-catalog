//go:build minikube

package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFederationCommandSuppliesOwnedValidatorAndEnabledAssertions(t *testing.T) {
	t.Setenv("CATALOG_FUNCTIONAL_CONTRACT_DRIVER", "/caller/validator")
	t.Setenv("PYTHONOPTIMIZE", "2")
	h := &harness{ctx: context.Background(), temporary: t.TempDir(), suffix: "owned", tokens: map[string]string{"admin": "private-token"}}
	validator := filepath.Join(h.temporary, "functional-contracts")
	command := h.federationCommand("http://127.0.0.1:18080", "https://127.0.0.1:18443", "/owned/ca.pem", validator)
	environment := make(map[string]string)
	for _, entry := range command.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		environment[name] = value
	}
	if environment["CATALOG_FUNCTIONAL_CONTRACT_DRIVER"] != validator || environment["PYTHONOPTIMIZE"] != "0" || environment["CATALOG_FUNCTIONAL_ADMIN_TOKEN"] != "private-token" || environment["CATALOG_FUNCTIONAL_STATE_DIRECTORY"] != h.temporary {
		t.Fatal("federation client did not receive its owned contract inputs")
	}
	if strings.Contains(strings.Join(command.Args, " "), "private-token") {
		t.Fatal("token leaked into command arguments")
	}
}

func TestFederationFailureLocationIsSafe(t *testing.T) {
	if got := federationFailureLocation([]byte(`{"scenario":"federation","lines":[12,34]}`)); got != " (federation.py lines [12 34])" {
		t.Fatalf("location = %q", got)
	}
	for _, raw := range []string{`{`, `{}`, `{"scenario":"secret","lines":[1]}`, `{"scenario":"federation","lines":[0]}`, `{"scenario":"federation","lines":[10001]}`, `{"scenario":"federation","lines":[1,2,3,4]}`, `{"scenario":"federation","lines":[1],"exception":"private"}`, strings.Repeat("x", 1025)} {
		if got := federationFailureLocation([]byte(raw)); got != "" {
			t.Fatalf("invalid diagnostic accepted: %q", got)
		}
	}
}

func TestDigestReferenceValidation(t *testing.T) {
	valid := "registry.example/catalog:v1@sha256:" + strings.Repeat("a", 64)
	if err := validateDigestReference(valid); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"tag only":       "registry.example/catalog:v1",
		"latest":         "registry.example/catalog:latest@sha256:" + strings.Repeat("a", 64),
		"short digest":   "registry.example/catalog:v1@sha256:abcd",
		"uppercase hash": "registry.example/catalog:v1@sha256:" + strings.Repeat("A", 64),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateDigestReference(value); err == nil {
				t.Fatal("unsafe image reference was accepted")
			}
		})
	}
}

func TestWebhookHelmInputsExistOnlyInSelectedProfile(t *testing.T) {
	h := &harness{temporary: t.TempDir(), application: "open-agentic-catalog:owned-fixture", options: options{scenario: "basic"}}
	for _, enabled := range []bool{false, true, false} {
		h.webhooks = enabled
		path, err := h.writeHelmValues("webhook-test", "catalog_api")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path) // #nosec G304 -- generated path under this test's temporary directory.
		if err != nil {
			t.Fatal(err)
		}
		var values struct {
			Secrets   map[string]map[string]string `json:"secrets"`
			Webhooks  map[string]string            `json:"webhooks"`
			ConfigEnv map[string]string            `json:"configEnv"`
		}
		if err := json.Unmarshal(raw, &values); err != nil {
			t.Fatal(err)
		}
		if (values.Secrets["webhookEncryption"]["name"] == "catalog-webhook-key") != enabled ||
			(values.Webhooks["egressConfigMap"] == "catalog-webhook-egress") != enabled ||
			(values.ConfigEnv["CATALOG_WEBHOOKS_ENABLED"] == "true") != enabled {
			t.Fatal("webhook inputs escaped their selected profile")
		}
	}
}

func TestFederationWorkflowRetryInputs(t *testing.T) {
	for _, scenario := range []string{"basic", "ui", "tracing", "monitoring", "semantic", "replicas", "topology"} {
		t.Run(scenario, func(t *testing.T) {
			h := &harness{temporary: t.TempDir(), application: "open-agentic-catalog:owned-fixture", options: options{scenario: scenario}}
			path, err := h.writeHelmValues("federation-test", "catalog_api")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path) // #nosec G304 -- generated path under this test's temporary directory.
			if err != nil {
				t.Fatal(err)
			}
			var values struct {
				ConfigEnv map[string]string `json:"configEnv"`
			}
			if err := json.Unmarshal(raw, &values); err != nil {
				t.Fatal(err)
			}
			attempts, ceiling := values.ConfigEnv["CATALOG_JOBS_MAX_ATTEMPTS"], values.ConfigEnv["CATALOG_JOBS_RETRY_CEILING"]
			if scenario == "replicas" || scenario == "topology" {
				if attempts != "7" || ceiling != "5.5s" {
					t.Fatalf("shared federation workflow requires seven attempts and a 5.5s ceiling: %q, %q", attempts, ceiling)
				}
			} else if attempts != "" || ceiling != "" {
				t.Fatal("federation-only retry inputs override chart defaults in an unrelated scenario")
			}
		})
	}
}

func TestVersionAndPodCIDRValidation(t *testing.T) {
	version, err := parseVersion("v1.35.1+vendor")
	if err != nil || version != (semanticVersion{major: 1, minor: 35, patch: 1}) {
		t.Fatalf("parsed version = %#v, %v", version, err)
	}
	if _, err := parseVersion("current"); err == nil {
		t.Fatal("unversioned tool was accepted")
	}
	prefix, err := privateClusterPrefix([]string{"10.244.0.0/24", "10.244.1.0/24"})
	if err != nil || prefix != "10.244.0.0/16" {
		t.Fatalf("private prefix = %q, %v", prefix, err)
	}
	for _, prefixes := range [][]string{{"203.0.113.0/24"}, {"invalid"}, {"10.0.0.0/8"}, {"10.244.0.0/24", "10.245.0.0/24"}} {
		if _, err := privateClusterPrefix(prefixes); err == nil {
			t.Fatalf("unsafe Pod CIDRs were accepted: %v", prefixes)
		}
	}
}

func TestFixtureTokenIsShortLivedRS256(t *testing.T) {
	key, _, err := newSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	token, err := signToken(key, "subject-one", "tenant-one", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatal(err)
	}
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatal(err)
	}
	expiresAt, expiresOK := claims["exp"].(float64)
	issuedAt, issuedOK := claims["iat"].(float64)
	if !expiresOK || !issuedOK || claims["iss"] != oidcIssuer || claims["aud"] != oidcAudience || claims["sub"] != "subject-one" || claims["organization_id"] != "tenant-one" || int64(expiresAt-issuedAt) != int64(45*time.Minute/time.Second) {
		t.Fatalf("unexpected token claims: %#v", claims)
	}
}

func TestBoundedCommandOutputAndSmokeComparison(t *testing.T) {
	buffer := &boundedBuffer{limit: 4}
	if count, err := buffer.Write([]byte("abcdef")); err != nil || count != 6 {
		t.Fatalf("write = %d, %v", count, err)
	}
	if got := buffer.String(); got != "abcd\n[output truncated]\n" {
		t.Fatalf("bounded output = %q", got)
	}
	left := []byte(`{"status":"passed","baseUrl":"http://127.0.0.1:10001","checks":["one"],"snapshotId":"fixed"}`)
	right := []byte(`{"snapshotId":"fixed","checks":["one","two"],"baseUrl":"http://127.0.0.1:10002","status":"passed"}`)
	if !sameSmokeState(left, right) || sameSmokeState(left, []byte(`{"status":"passed","snapshotId":"different"}`)) {
		t.Fatal("smoke comparison did not isolate durable result fields")
	}
}

func TestDiagnosticRedactionCoversGeneratedSecretsAndTokens(t *testing.T) {
	harness := &harness{
		secrets: fixtureSecrets{ownerPassword: "owner-secret", apiPassword: "api-secret", workerPassword: "worker-secret", cursorKey: "cursor-secret", monitoringPassword: "monitoring-secret"},
		tokens:  map[string]string{"admin": "signed-token"},
	}
	redacted := harness.redactDiagnostic(" owner-secret api-secret worker-secret cursor-secret signed-token monitoring-secret safe ")
	for _, secret := range []string{"owner-secret", "api-secret", "worker-secret", "cursor-secret", "signed-token", "monitoring-secret"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("diagnostic retained %q: %s", secret, redacted)
		}
	}
	if strings.Count(redacted, "[redacted]") != 6 || !strings.HasSuffix(redacted, "safe") {
		t.Fatalf("unexpected redacted diagnostic: %q", redacted)
	}
}

func TestHelmValuesKeepTopologyOptInAndSecretsReferenced(t *testing.T) {
	for _, scenario := range []string{"basic", "topology", "monitoring", "tracing", "ui", "replicas"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			harness := &harness{temporary: directory, suffix: "abcdef", application: "open-agentic-catalog:minikube-abcdef", postgres: "open-agentic-catalog:minikube-postgres-abcdef", options: options{scenario: scenario}}
			path, err := harness.writeHelmValues("initial", "catalog_api")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Clean(path))
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := json.Unmarshal(raw, &values); err != nil {
				t.Fatal(err)
			}
			api, apiOK := values["api"].(map[string]any)
			spread, spreadOK := api["topologySpread"].(map[string]any)
			enabled, enabledOK := spread["enabled"].(bool)
			if !apiOK || !spreadOK || !enabledOK {
				t.Fatalf("invalid topology spread values: %#v", values)
			}
			if got := enabled; got != (scenario == "topology") {
				t.Fatalf("topology spread = %t", got)
			}
			wantReplicas := map[string]float64{"basic": 1, "topology": 2, "monitoring": 1, "tracing": 2, "ui": 1, "replicas": 3}[scenario]
			for _, role := range []string{"api", "worker"} {
				configured, ok := values[role].(map[string]any)
				if !ok || configured["replicas"] != wantReplicas {
					t.Fatalf("%s replica count differs from scenario", role)
				}
			}
			encoded := string(raw)
			for _, secret := range []string{"ownerPassword", "apiPassword", "workerPassword", "cursorKey"} {
				if strings.Contains(encoded, secret) {
					t.Fatalf("Helm values contain secret field %q", secret)
				}
			}
		})
	}
}

func TestTopologyUsesExplicitTCPDNSWithoutChangingOtherScenarios(t *testing.T) {
	for _, scenario := range []string{"basic", "replicas", "topology"} {
		t.Run(scenario, func(t *testing.T) {
			h := &harness{temporary: t.TempDir(), application: "open-agentic-catalog:owned-fixture", options: options{scenario: scenario}}
			path, err := h.writeHelmValues("dns-test", "catalog_api")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path) // #nosec G304 -- Owned generated values file.
			if err != nil {
				t.Fatal(err)
			}
			var values struct {
				Options []map[string]string `json:"podDNSOptions"`
			}
			if err := json.Unmarshal(raw, &values); err != nil {
				t.Fatal(err)
			}
			if scenario == "topology" {
				if len(values.Options) != 1 || values.Options[0]["name"] != "use-vc" {
					t.Fatal("topology omitted explicit TCP DNS")
				}
			} else if len(values.Options) != 0 {
				t.Fatal("unselected scenario changed DNS transport")
			}
		})
	}
}
