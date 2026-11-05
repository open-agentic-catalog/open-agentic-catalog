//go:build minikube

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

const vaultNamespace = "catalog-vault-system"
const vaultRelease = "catalog-vault"
const vaultSourceSecret = "catalog-vault-source"

type vaultState struct {
	syncReady           bool
	cleanupFaultReached bool
	versions            map[string]int
	images              map[string]string
	private             []string
	rootToken           string
	unsealKey           string
	bearer              string
	databaseTLS         map[string]string
	expected            map[string]map[string]string
}
type vaultReport struct {
	CleanupVerified    bool     `json:"cleanupVerified"`
	CleanupTest        string   `json:"cleanupTest,omitempty"`
	SyncedInputs       int      `json:"syncedInputs"`
	SourceID           string   `json:"sourceId"`
	SnapshotID         string   `json:"snapshotId"`
	CredentialRevision string   `json:"credentialRevision"`
	DatabaseRoles      []string `json:"databaseRoles"`
}

func (h *harness) prepareVaultDatabase() error {
	material, err := serviceTLSMaterial(time.Now(), "vault-database-ca", namespace, []string{"postgres"})
	if err != nil {
		return err
	}
	h.vault.databaseTLS = material
	// Require passwords on every TCP connection, including loopback port-forwards.
	if err := h.applyObject("require password authentication for owned TLS database", kubernetesObject{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]string{"name": "vault-postgres-auth", "namespace": namespace}, "data": map[string]string{"pg_hba.conf": "local all all trust\nhostssl all all 0.0.0.0/0 scram-sha-256\nhostssl all all ::/0 scram-sha-256\nhost all all 0.0.0.0/0 reject\nhost all all ::/0 reject\n"}}); err != nil {
		return err
	}
	if err := h.createSecret("create owned PostgreSQL TLS leaf", "postgres-tls", map[string]string{"tls.crt": material["postgres.crt"], "tls.key": material["postgres.key"]}); err != nil {
		return err
	}
	return h.applyObject("create owned database trust", kubernetesObject{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]string{"name": "database-ca", "namespace": namespace}, "data": map[string]string{"ca.pem": material["ca.pem"]}})
}

// vaultExec keeps the bootstrap token out of process arguments and generated manifests.
func (h *harness) vaultExec(label string, input any, args ...string) ([]byte, error) {
	return h.vaultExecContext(h.ctx, label, input, args...)
}
func (h *harness) vaultExecContext(ctx context.Context, label string, input any, args ...string) ([]byte, error) {
	return h.vaultExecWithTimeout(ctx, 5*time.Second, label, input, args...)
}
func (h *harness) vaultExecWithTimeout(ctx context.Context, timeout time.Duration, label string, input any, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	var raw []byte
	if input != nil {
		var err error
		raw, err = json.Marshal(input)
		if err != nil {
			return nil, err
		}
	}
	command := make([]string, 0, 10+len(args))
	command = append(command, "-n", vaultNamespace, "exec", "-i", vaultRelease+"-0", "--", "sh", "-ec", `export VAULT_CLIENT_TIMEOUT="$1"; shift; export VAULT_TOKEN="$(cat /tmp/catalog-vault-bootstrap)"; exec vault "$@"`, "--", timeout.String())
	return h.runWithInput(ctx, label, raw, h.options.kubectl, append([]string{"--kubeconfig", h.kubeconfig, "--context", h.profile}, append(command, args...)...)...)
}

func (h *harness) installVault() error {
	if err := h.applyObject("create owned Vault namespace", kubernetesObject{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]string{"name": vaultNamespace}}); err != nil {
		return err
	}
	material, err := serviceTLSMaterial(time.Now(), "owned-vault-ca", vaultNamespace, []string{vaultRelease}, net.ParseIP("127.0.0.1"))
	if err != nil {
		return err
	}
	if err := h.applyObject("create private Vault TLS input", kubernetesObject{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]string{"name": "vault-tls", "namespace": vaultNamespace}, "stringData": map[string]string{"ca.pem": material["ca.pem"], "tls.crt": material[vaultRelease+".crt"], "tls.key": material[vaultRelease+".key"]}}); err != nil {
		return err
	}
	values, err := h.certificateValuesFile("vault-server", vaultServerValues(h.vault.images["vault"]))
	if err != nil {
		return err
	}
	pins, err := readVaultPins()
	if err != nil {
		return err
	}
	// An initialized and unsealed server cannot exist before this first install.
	if _, err := h.helm("install external persistent Vault", "install", vaultRelease, h.vaultTool(pins.Charts["vault"].File), "-n", vaultNamespace, "-f", values, "--timeout=2m"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for running uninitialized Vault", "-n", vaultNamespace, "wait", "--for=jsonpath={.status.phase}=Running", "pod/"+vaultRelease+"-0", "--timeout=2m"); err != nil {
		return err
	}
	raw, err := h.kubectl("initialize owned Vault", "-n", vaultNamespace, "exec", vaultRelease+"-0", "--", "vault", "operator", "init", "-format=json", "-key-shares=1", "-key-threshold=1")
	if err != nil {
		return err
	}
	var initialized struct {
		RootToken string   `json:"root_token"`
		Keys      []string `json:"unseal_keys_b64"`
	}
	if json.Unmarshal(raw, &initialized) != nil || initialized.RootToken == "" || len(initialized.Keys) != 1 {
		return errors.New("invalid private Vault initialization result")
	}
	h.vault.rootToken, h.vault.unsealKey = initialized.RootToken, initialized.Keys[0]
	h.vault.private = append(h.vault.private, initialized.RootToken, initialized.Keys[0])
	if _, err := h.kubectlInput("retain protected bootstrap token", []byte(initialized.RootToken), "-n", vaultNamespace, "exec", "-i", vaultRelease+"-0", "--", "sh", "-ec", "umask 077; cat > /tmp/catalog-vault-bootstrap"); err != nil {
		return err
	}
	if _, err := h.vaultExec("unseal owned Vault", map[string]string{"key": initialized.Keys[0]}, "write", "-format=json", "sys/unseal", "-"); err != nil {
		return err
	}
	if _, err := h.kubectl("require unsealed Vault readiness", "-n", vaultNamespace, "wait", "--for=condition=Ready", "pod/"+vaultRelease+"-0", "--timeout=90s"); err != nil {
		return err
	}
	if _, err := h.vaultExec("enable static KV-v2", map[string]any{"type": "kv", "options": map[string]string{"version": "2"}}, "write", "sys/mounts/catalog", "-"); err != nil {
		return err
	}
	if _, err := h.vaultExec("enable Kubernetes authentication", map[string]string{"type": "kubernetes"}, "write", "sys/auth/kubernetes", "-"); err != nil {
		return err
	}
	if _, err := h.vaultExec("configure local token review", map[string]string{"kubernetes_host": "https://kubernetes.default.svc:443"}, "write", "auth/kubernetes/config", "-"); err != nil {
		return err
	}
	if err := h.createSecret("provide independent Vault trust", "catalog-vault-ca", map[string]string{"ca.crt": material["ca.pem"]}); err != nil {
		return err
	}
	if err := h.configureVaultSync(); err != nil {
		return err
	}
	vsoValues, err := h.certificateValuesFile("vault-operator", vaultOperatorValues(h.vault.images))
	if err != nil {
		return err
	}
	if _, err := h.helm("install external VSO and proxy", "install", "catalog-vso", h.vaultTool(pins.Charts["vault-secrets-operator"].File), "-n", vaultNamespace, "-f", vsoValues, "--wait", "--timeout=3m"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for static-secret CRD", "wait", "--for=condition=Established", "crd/vaultstaticsecrets.secrets.hashicorp.com", "--timeout=60s"); err != nil {
		return err
	}
	if err := h.createVaultResources(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Minute)
	defer cancel()
	if err := waitTopology(ctx, func() (bool, error) { return h.vaultInputsReady(ctx) }); err != nil {
		return fmt.Errorf("complete VSO input synchronization: %w", err)
	}
	h.vault.syncReady = true
	h.report.Vault = &vaultReport{SyncedInputs: len(h.vault.expected), CleanupTest: h.options.vaultCleanupTest}
	h.report.Checks = append(h.report.Checks, "vault-persistent-raft-private-tls-unsealed", "vso-five-complete-owned-secret-inputs")
	return nil
}

func (h *harness) configureVaultSync() error {
	h.vault.versions = make(map[string]int)
	h.vault.expected = map[string]map[string]string{
		"catalog-api-database":       {"dsn": vaultDSN("catalog_api", h.secrets.apiPassword)},
		"catalog-worker-database":    {"dsn": vaultDSN("catalog_worker", h.secrets.workerPassword)},
		"catalog-migration-database": {"dsn": vaultDSN("catalog_owner", h.secrets.ownerPassword)},
		"catalog-cursor-signing":     {"key": h.secrets.cursorKey},
		vaultSourceSecret:            {"token": h.vault.bearer, "tenantId": primaryTenantID, "origin": "https://federation-fixture:8443", "epoch": h.suffix},
	}
	for _, role := range []string{"runtime", "owner"} {
		policy := vaultRuntimePolicy
		if role == "owner" {
			policy = vaultOwnerPolicy
		}
		if _, err := h.vaultExec("configure exact Vault "+role+" policy", map[string]string{"policy": policy}, "write", "sys/policies/acl/catalog-"+role, "-"); err != nil {
			return err
		}

		data := map[string]any{"bound_service_account_names": []string{"catalog-vault-" + role}, "bound_service_account_namespaces": []string{namespace}, "audience": "vault", "token_policies": []string{"catalog-" + role}, "token_ttl": "5m", "token_max_ttl": "10m"}
		if _, err := h.vaultExec("bind scoped Vault "+role+" identity", data, "write", "auth/kubernetes/role/catalog-"+role, "-"); err != nil {
			return err
		}
	}
	for name, data := range h.vault.expected {
		raw, err := h.vaultExec("seed private Vault input "+name, map[string]any{"data": data, "options": map[string]int{"cas": 0}}, "write", "-format=json", "catalog/data/"+name, "-")
		if err != nil {
			return err
		}
		var response struct {
			Data struct {
				Version int `json:"version"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &response) != nil || response.Data.Version != 1 {
			return errors.New("vault KV seed returned an unexpected version")
		}
		h.vault.versions[name] = response.Data.Version
	}
	return nil
}

func vaultDSN(role, password string) string {
	return fmt.Sprintf("postgres://%s:%s@postgres:5432/catalog?sslmode=verify-full&sslrootcert=/etc/open-agentic-catalog/database/ca.pem", role, password)
}

func (h *harness) vaultFixtureValues(values map[string]string) error {
	raw, err := os.ReadFile(filepath.Join(h.temporary, "fixture", "credentials", "bearer.token")) // #nosec G304 -- Generated invocation-owned credential.
	if err != nil {
		return err
	}
	if len(raw) != 64 {
		return errors.New("invalid generated source token")
	}
	h.vault.bearer = string(raw)
	ca, err := os.ReadFile(filepath.Join(h.temporary, "fixture", "trust", "functional-ca.pem")) // #nosec G304 -- Public generated fixture CA.
	if err != nil {
		return err
	}
	values["functional-ca.pem"] = string(ca)
	values["bearer.token"] = h.vault.bearer
	for _, value := range values {
		h.vault.private = append(h.vault.private, value)
	}
	return nil
}

func (h *harness) vaultDiagnostics() {
	if h.vault == nil || !h.clusterReady {
		return
	}
	raw, _ := h.runCleanupCommand(15*time.Second, h.options.kubectl, "--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", vaultNamespace, "get", "pods,pvc,events", "-o", "wide")
	fmt.Println(h.redactDiagnostic(string(raw)))
	for _, target := range []string{"statefulset/" + vaultRelease, "deployment/catalog-vso-vault-secrets-operator-controller-manager"} {
		raw, _ := h.runCleanupCommand(10*time.Second, h.options.kubectl, "--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", vaultNamespace, "logs", target, "--all-containers", "--tail=40")
		fmt.Println(h.redactDiagnostic(string(raw)))
	}
}
