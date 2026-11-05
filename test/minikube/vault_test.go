//go:build minikube

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestVaultSynchronizationRequiresCurrentSuccessfulCondition(t *testing.T) {
	good := `{"metadata":{"uid":"owned","generation":2},"status":{"conditions":[{"type":"SecretSynced","status":"True","reason":"Synced","observedGeneration":2}]}}`
	for _, tc := range []struct {
		name, raw string
		ok        bool
	}{
		{"current", good, true},
		{"stale condition", strings.Replace(good, `"observedGeneration":2`, `"observedGeneration":1`, 1), false},
		{"failed condition", strings.Replace(good, `"status":"True"`, `"status":"False"`, 1), false},
		{"wrong reason", strings.Replace(good, `"reason":"Synced"`, `"reason":"Error"`, 1), false},
		{"wrong condition", strings.Replace(good, `"type":"SecretSynced"`, `"type":"Ready"`, 1), false},
		{"missing identity", strings.Replace(good, `"uid":"owned"`, `"uid":""`, 1), false},
		{"not observed", `{"metadata":{"uid":"owned","generation":2},"status":{"lastGeneration":2}}`, false},
		{"malformed", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uid, ok := currentVaultSync([]byte(tc.raw))
			if ok != tc.ok || (ok && uid != "owned") {
				t.Fatalf("current condition admitted=%v, want %v", ok, tc.ok)
			}
		})
	}
}
func TestVaultDestinationRequiresOwnerCompleteDataAndExactRevision(t *testing.T) {
	input := map[string]string{"token": "private-synthetic-token", "tenantId": primaryTenantID, "origin": "https://federation-fixture:8443", "epoch": "never-reused"}
	expected, err := vaultExpectedData(vaultSourceSecret, input, 1)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(data map[string]string, owner string) []byte {
		encoded := map[string]string{}
		for k, v := range data {
			encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
		}
		raw, err := json.Marshal(map[string]any{"data": encoded, "metadata": map[string]any{"ownerReferences": []any{map[string]any{"uid": owner, "kind": "VaultStaticSecret", "name": vaultSourceSecret, "apiVersion": "secrets.hashicorp.com/v1beta1"}}}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if !validateVaultDestination(encode(expected, "vss"), "vss", vaultSourceSecret, expected) {
		t.Fatal("rejected complete owned generation")
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]string)
	}{
		{"old provider version", func(m map[string]string) { m["vault-kv-version"] = "0" }},
		{"missing token", func(m map[string]string) { delete(m, "bearer.token") }},
		{"different credential", func(m map[string]string) { m["bearer.token"] = "retired-value" }},
		{"raw provider data", func(m map[string]string) { m["_raw"] = "unwanted" }},
		{"wrong tenant", func(m map[string]string) {
			m["bearer.json"] = strings.Replace(m["bearer.json"], primaryTenantID, otherTenantID, 1)
		}},
		{"reused source revision", func(m map[string]string) {
			m["bearer.json"] = strings.Replace(m["bearer.json"], "never-reused-kv-1", "old-kv-1", 1)
		}},
		{"malformed metadata", func(m map[string]string) { m["bearer.json"] = "{" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copyData := map[string]string{}
			for k, v := range expected {
				copyData[k] = v
			}
			tc.change(copyData)
			if validateVaultDestination(encode(copyData, "vss"), "vss", vaultSourceSecret, expected) {
				t.Fatal("admitted stale, incomplete or unexpected data")
			}
		})
	}
	if validateVaultDestination(encode(expected, "other"), "vss", vaultSourceSecret, expected) || validateVaultDestination([]byte(strings.Replace(string(encode(expected, "vss")), "VaultStaticSecret", "OtherKind", 1)), "vss", vaultSourceSecret, expected) {
		t.Fatal("admitted unrelated destination writer")
	}
}
func TestVaultProjectionAndDefaults(t *testing.T) {
	data := map[string]string{"token": "sentinel", "tenantId": primaryTenantID, "origin": "https://federation-fixture:8443", "epoch": "epoch"}
	object := vaultRuntimeSecret(vaultSourceSecret, data)
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{`"overwrite":false`, `"excludeRaw":true`, `"excludes":[".*"]`, `"vaultAuthRef":"catalog-vault-runtime"`, `"bearer.json"`, `"bearer.token"`, ".Metadata", ".Secrets"} {
		if !strings.Contains(text, required) {
			t.Fatalf("missing delivery constraint %s", required)
		}
	}
	for _, forbidden := range []string{"sentinel", "rolloutRestartTargets", "VaultDynamicSecret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("leaked value or expanded provider scope: %s", forbidden)
		}
	}
	if (options{scenario: "vault"}).clusterCNI() != "calico" || (options{scenario: "basic"}).clusterCNI() != "bridge" {
		t.Fatal("Vault changes the ordinary network scenario")
	}
	h := &harness{options: options{scenario: "vault"}, vault: &vaultState{}, postgres: "postgres:test"}
	job := h.databaseRoleJob("bootstrap", "postgres")
	raw, err = json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"PGSSLMODE", "verify-full", "PGSSLROOTCERT", "database-ca"} {
		if !strings.Contains(string(raw), required) {
			t.Fatal("owner bootstrap lacks verified TLS", required)
		}
	}
	dsn := vaultDSN("catalog_api", "synthetic")
	if !strings.Contains(dsn, "@postgres:5432/") || !strings.Contains(dsn, "sslmode=verify-full") || strings.Contains(dsn, "pgbouncer") {
		t.Fatal("Vault changes database routing or weakens TLS")
	}
}
func TestVaultBootstrapAndProviderSecretsAreRedacted(t *testing.T) {
	h := &harness{vault: &vaultState{private: []string{"bootstrap-root", "unseal-material", "source-value"}}}
	got := h.redactDiagnostic("bootstrap-root unseal-material source-value")
	if got != "[redacted] [redacted] [redacted]" {
		t.Fatal("provider credential escaped diagnostics")
	}
}

func TestVaultSuccessorGenerationChangesBothMarkers(t *testing.T) {
	input := map[string]string{"token": "new-private-value", "tenantId": primaryTenantID, "origin": "https://federation-fixture:8443", "epoch": "epoch"}
	for _, version := range []int{0, -1} {
		if _, err := vaultExpectedData(vaultSourceSecret, input, version); err == nil {
			t.Fatal("admitted nonpositive KV version")
		}
	}
	got, err := vaultExpectedData(vaultSourceSecret, input, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got["vault-kv-version"] != "3" || got["bearer.token"] != "new-private-value" || !strings.Contains(got["bearer.json"], `"revision":"epoch-kv-3"`) {
		t.Fatal("successor projection lost data or generation binding")
	}
	database, err := vaultExpectedData("catalog-api-database", map[string]string{"dsn": "private-successor-dsn"}, 2)
	if err != nil || database["vault-kv-version"] != "2" || database["dsn"] != "private-successor-dsn" || len(database) != 2 {
		t.Fatal("database projection lost exact successor")
	}
}
func TestVaultDenialRejectsTransportAndUnrelatedFailures(t *testing.T) {
	for _, message := range []string{"connection refused", "TLS handshake failed", "Code: 500", "Code: 404"} {
		if vaultPermissionDenied(errors.New(message)) {
			t.Fatal("counted infrastructure failure as policy denial")
		}
	}
	if !vaultPermissionDenied(errors.New("Error reading path: Code: 403. Errors: permission denied")) {
		t.Fatal("did not recognize explicit Vault policy refusal")
	}
}
func TestVaultStartupInputsExcludeUnrelatedSource(t *testing.T) {
	expected := []string{"catalog-api-database", "catalog-worker-database", "catalog-migration-database", "catalog-cursor-signing"}
	if !slices.Equal(vaultStartupInputs, expected) {
		t.Fatal("startup admission lost a required input or added source credentials")
	}
}

func TestVaultDatabasePasswordFixtureLeavesDefaultScenariosAlone(t *testing.T) {
	for _, selected := range []bool{true, false} {
		h := &harness{options: options{scenario: "basic"}}
		if selected {
			h.options.scenario = "vault"
			h.vault = &vaultState{}
		}
		raw, err := json.Marshal(h.dependencyObjects())
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, required := range []string{"hba_file=/etc/postgres/auth/pg_hba.conf", `"name":"vault-postgres-auth"`, `"mountPath":"/etc/postgres/auth"`} {
			if strings.Contains(text, required) != selected {
				t.Fatal("password fixture changed the default scenario or omitted its selected inputs", required)
			}
		}
	}
}
