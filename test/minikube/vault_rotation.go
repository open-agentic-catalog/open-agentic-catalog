//go:build minikube

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func (h *harness) vaultSourceTest(baseURL, phase, revision string) error {
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Minute)
	defer cancel()
	attempt := 0
	return waitTopology(ctx, func() (bool, error) {
		attempt++
		path := "/api/v1/federation/sources/" + h.report.Vault.SourceID
		current, err := h.request("GET", baseURL+path, h.tokens["admin"], "", "", nil, nil)
		if err != nil {
			return false, err
		}
		if current.Status != 200 {
			return false, errors.New("source read failed during generation observation")
		}
		tested, err := h.request("POST", baseURL+path+":test", h.tokens["admin"], fmt.Sprintf("vault-%s-%s-%d", phase, h.suffix, attempt), current.Header.Get("ETag"), map[string]any{}, nil)
		if err != nil {
			return false, err
		}
		if tested.Status == 503 {
			var problem struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(tested.Body, &problem) != nil || problem.Code != "DEPENDENCY_UNAVAILABLE" {
				return false, errors.New("unexpected source failure during projection")
			}
			return false, nil
		}
		var result struct {
			Status   string `json:"status"`
			Revision string `json:"credentialRevision"`
		}
		if tested.Status != 200 || json.Unmarshal(tested.Body, &result) != nil || result.Status != "succeeded" {
			return false, errors.New("source test failed during generation observation")
		}
		return result.Revision == revision, nil
	})
}

func (h *harness) attachVaultMetadataProbe(pod string) error {
	raw, err := h.kubectl("inspect source projection Pod", "-n", namespace, "get", "pod", pod, "-o", "json")
	if err != nil {
		return err
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return errors.New("invalid source projection Pod")
	}
	spec, ok := object["spec"].(map[string]any)
	if !ok {
		return errors.New("source projection Pod has no spec")
	}
	existing, _ := spec["ephemeralContainers"].([]any)
	spec["ephemeralContainers"] = append(existing, map[string]any{"name": "vault-metadata-probe", "image": h.postgres, "imagePullPolicy": "Never", "command": []string{"sleep", "3600"}, "volumeMounts": []any{map[string]any{"name": "federation-credentials", "mountPath": "/source", "readOnly": true}}, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}})
	raw, err = json.Marshal(object)
	if err != nil {
		return err
	}
	if _, err := h.kubectlInput("attach owned metadata-only observer", raw, "replace", "--raw", "/api/v1/namespaces/"+namespace+"/pods/"+pod+"/ephemeralcontainers", "-f", "-"); err != nil {
		return err
	}
	_, err = h.kubectl("wait for metadata observer", "-n", namespace, "wait", "pod/"+pod, "--for=jsonpath={.status.ephemeralContainerStatuses[?(@.name==\"vault-metadata-probe\")].state.running}", "--timeout=60s")
	return err
}
func (h *harness) waitVaultMountedRevision(pods []podIdentity, revision string) error {
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Minute)
	defer cancel()
	return waitTopology(ctx, func() (bool, error) {
		for _, pod := range pods {
			raw, err := h.topologyCommand(ctx, "observe mounted source metadata", nil, "-n", namespace, "exec", pod.Name, "-c", "vault-metadata-probe", "--", "cat", "/source/bearer.json")
			if err != nil {
				return false, err
			}
			var metadata struct {
				Revision string `json:"revision"`
				Tenant   string `json:"tenantId"`
				Origin   string `json:"origin"`
				Type     string `json:"type"`
			}
			if json.Unmarshal(raw, &metadata) != nil || metadata.Tenant != primaryTenantID || metadata.Origin != "https://federation-fixture:8443" || metadata.Type != "bearer" {
				return false, errors.New("mounted source metadata lost its tenant/origin binding")
			}
			if metadata.Revision != revision {
				return false, nil
			}
		}
		return true, nil
	})
}

func (h *harness) vaultOriginClient() (*http.Client, string, error) {
	raw, err := os.ReadFile(filepath.Join(h.temporary, "fixture", "trust", "functional-ca.pem")) // #nosec G304 -- Owned public CA.
	if err != nil {
		return nil, "", err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, "", errors.New("invalid source fixture trust")
	}
	address, err := h.startPortForward("service/federation-fixture", 8443)
	if err != nil {
		return nil, "", err
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "federation-fixture"}, DisableKeepAlives: true}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, "https://" + address + "/credentials/bearer/manifest.json", nil
}
func vaultOriginStatus(ctx context.Context, client *http.Client, url, token string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return 0, errors.New("owned source credential control failed to connect")
	}
	_ = response.Body.Close()
	return response.StatusCode, nil
}

func (h *harness) verifyVaultSourceRotation(baseURL string, before []podIdentity) error {
	for _, pod := range before {
		if err := h.attachVaultMetadataProbe(pod.Name); err != nil {
			return err
		}
	}
	old := h.vault.bearer
	successor, err := randomHex(32)
	if err != nil {
		return err
	}
	h.vault.private = append(h.vault.private, successor)
	if err := h.scaleVaultOperator("0"); err != nil {
		return err
	}
	source := map[string]string{"token": successor, "tenantId": primaryTenantID, "origin": "https://federation-fixture:8443", "epoch": h.suffix}
	if err := h.vaultWriteGeneration(vaultSourceSecret, source); err != nil {
		return err
	}
	if err := h.vaultWriteGeneration("catalog-api-database", h.vault.expected["catalog-api-database"]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
	stale, err := h.vaultInputReady(ctx, "catalog-api-database")
	cancel()
	if err != nil || stale {
		return errors.New("stale destination passed expected-version validation")
	}
	if err := h.requireFreshVaultInputs(vaultStartupInputs...); err == nil {
		return errors.New("maintenance admitted a stopped controller and stale inputs")
	}
	if err := h.vaultSourceTest(baseURL, "controller-stopped", h.suffix+"-kv-1"); err != nil {
		return err
	}
	if err := h.vaultUnchanged(before); err != nil {
		return err
	}
	if err := h.scaleVaultOperator("1"); err != nil {
		return err
	}
	if err := h.waitVaultInputs(); err != nil {
		return err
	}
	revision := fmt.Sprintf("%s-kv-%d", h.suffix, h.vault.versions[vaultSourceSecret])
	if err := h.waitVaultMountedRevision(before, revision); err != nil {
		return err
	}
	if err := h.patchVaultFixtureSecret("local-fixture-material", map[string]string{"bearer.token": successor}); err != nil {
		return err
	}
	client, url, err := h.vaultOriginClient()
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	ctx, cancel = context.WithTimeout(h.ctx, 3*time.Minute)
	defer cancel()
	if err := waitTopology(ctx, func() (bool, error) {
		status, err := vaultOriginStatus(ctx, client, url, successor)
		return status == 200, err
	}); err != nil {
		return err
	}
	status, err := vaultOriginStatus(ctx, client, url, old)
	if err != nil || status != 401 {
		return errors.New("retired source credential still accepted")
	}
	if err := h.vaultSourceTest(baseURL, "successor", revision); err != nil {
		return err
	}
	if err := h.vaultSnapshot(baseURL, "successor"); err != nil {
		return err
	}
	if err := h.vaultUnchanged(before); err != nil {
		return err
	}
	h.vault.bearer = successor
	h.report.Checks = append(h.report.Checks, "vault-stale-input-maintenance-refusal", "vault-controller-outage-retains-accepted-credentials", "vault-source-projection-and-upstream-retirement-without-restart")
	return nil
}

func (h *harness) rotateVaultDatabases() (string, error) {
	before, err := h.vaultRuntimePods()
	if err != nil {
		return "", err
	}
	for _, role := range []string{"api", "worker"} {
		if _, err := h.kubectl("drain "+role+" before password activation", "-n", namespace, "scale", "deployment/catalog-"+role, "--replicas=0"); err != nil {
			return "", err
		}
		if _, err := h.kubectl("wait for old "+role+" removal", "-n", namespace, "wait", "pod", "-l", "app.kubernetes.io/instance=catalog,app.kubernetes.io/component="+role, "--for=delete", "--timeout=90s"); err != nil {
			return "", err
		}
	}
	raw, err := h.databaseOwnerSQL("SELECT count(*) FROM pg_stat_activity WHERE usename IN ('catalog_api','catalog_worker')")
	if err != nil || strings.TrimSpace(string(raw)) != "0" {
		return "", errors.New("runtime sessions did not drain before password activation")
	}
	roles := []string{"catalog_api", "catalog_worker", "catalog_owner"}
	old := []string{h.secrets.apiPassword, h.secrets.workerPassword, h.secrets.ownerPassword}
	next := make([]string, 3)
	var sql strings.Builder
	sql.WriteString("BEGIN;\n")
	for i, role := range roles {
		next[i], err = randomHex(24)
		if err != nil {
			return "", err
		}
		h.vault.private = append(h.vault.private, next[i])
		fmt.Fprintf(&sql, "ALTER ROLE %s PASSWORD '%s';\n", role, next[i])
	}
	sql.WriteString("COMMIT;\n")
	if _, err := h.kubectlInput("activate three owned database passwords", []byte(sql.String()), "-n", namespace, "exec", "-i", "postgres-0", "-c", "postgres", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog"); err != nil {
		return "", err
	}
	h.secrets.apiPassword, h.secrets.workerPassword, h.secrets.ownerPassword = next[0], next[1], next[2]
	names := []string{"catalog-api-database", "catalog-worker-database", "catalog-migration-database"}
	// A confirmed partial provider update must not start either runtime.
	if err := h.vaultWriteGeneration(names[0], map[string]string{"dsn": vaultDSN(roles[0], next[0])}); err != nil {
		return "", err
	}
	for i := 1; i < 3; i++ {
		h.vault.expected[names[i]] = map[string]string{"dsn": vaultDSN(roles[i], next[i])}
	}
	if err := h.requireFreshVaultInputs(vaultStartupInputs...); err == nil {
		return "", errors.New("partially updated database credentials passed maintenance admission")
	}
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil || len(pods) != 0 {
			return "", errors.New("partial password replacement started runtimes")
		}
	}
	for i := 1; i < 3; i++ {
		if err := h.vaultWriteGeneration(names[i], h.vault.expected[names[i]]); err != nil {
			return "", err
		}
	}
	if err := h.waitVaultInputs(); err != nil {
		return "", err
	}
	if err := h.patchVaultFixtureSecret("postgres-owner", map[string]string{"password": next[2]}); err != nil {
		return "", err
	}
	if err := h.patchVaultFixtureSecret("catalog-role-passwords", map[string]string{"api": next[0], "worker": next[1]}); err != nil {
		return "", err
	}
	for i, role := range roles {
		if err := h.vaultPasswordDenied(role, old[i]); err != nil {
			return "", err
		}
	}

	values, err := h.writeHelmValues("vault-database-successor", "catalog_api")
	if err != nil {
		return "", err
	}
	if err := h.deployCatalog(values, "4m"); err != nil {
		return "", err
	}
	if err := h.verifyVaultDatabases(); err != nil {
		return "", err
	}
	after, err := h.vaultRuntimePods()
	if err != nil || sameUIDs(before, after) {
		return "", errors.New("database activation did not replace startup consumers")
	}
	address, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return "", err
	}
	baseURL := "http://" + address
	if err := h.waitHTTP(baseURL + "/readyz"); err != nil {
		return "", err
	}
	if err := h.vaultSnapshot(baseURL, "database-successor"); err != nil {
		return "", err
	}
	h.report.Checks = append(h.report.Checks, "vault-database-passwords-drained-and-rotated", "vault-partial-database-replacement-recovers-forward", "vault-three-retired-passwords-refused-on-fresh-tls-connections")
	return baseURL, nil
}

func (h *harness) vaultPasswordDenied(role, password string) error {
	address, err := h.startPortForward("pod/postgres-0", 5432)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	conn, authErr := databaseConnection(ctx, address, "postgres", role, password, h.vault.databaseTLS["ca.pem"], h.vault.databaseTLS["postgres.crt"])
	if conn != nil {
		_ = conn.Close(ctx)
	}
	if authErr == nil {
		return fmt.Errorf("database accepted a refused password for %s", role)
	}
	var denied *pgconn.PgError
	if !errors.As(authErr, &denied) || denied.Code != "28P01" {
		return fmt.Errorf("expected password authentication refusal for %s: %s", role, h.redactDiagnostic(authErr.Error()))
	}
	return nil
}
