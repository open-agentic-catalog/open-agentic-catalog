//go:build minikube

// Fixture resources provision isolated dependencies, credentials and chart values for tests.

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type kubernetesObject map[string]any

// applyObject server-side applies a generated fixture object through the harness's isolated Kubernetes context.
func (h *harness) applyObject(label string, object kubernetesObject) error {
	raw, err := json.Marshal(object)
	if err != nil {
		return err
	}
	_, err = h.kubectlInput(label, raw, "apply", "--server-side", "--field-manager=open-agentic-catalog-minikube", "-f", "-")
	return err
}

// createSecret creates an opaque Secret in the test namespace, passing encoded values through stdin rather than command arguments.
func (h *harness) createSecret(label, name string, values map[string]string) error {
	data := make(map[string]string, len(values))
	for key, value := range values {
		data[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	object := kubernetesObject{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": name, "namespace": h.catalogNamespace(), "labels": testLabels("secret")},
		"type":     "Opaque", "data": data,
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return err
	}
	_, err = h.kubectlInput(label, raw, "create", "-f", "-")
	return err
}

// testLabels returns the ownership and component labels used to select this harness's fixture resources.
func testLabels(component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "open-agentic-catalog-e2e",
		"app.kubernetes.io/instance":   releaseName,
		"app.kubernetes.io/component":  component,
		"app.kubernetes.io/managed-by": "open-agentic-catalog-minikube",
	}
}

// prepareDependencies provisions the restricted test namespace, local TLS and OIDC material, PostgreSQL and fixed network fixtures before runtime installation.
func (h *harness) prepareDependencies() error {
	if err := h.applyObject("create restricted test namespace", kubernetesObject{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": h.catalogNamespace(), "labels": map[string]string{
			"pod-security.kubernetes.io/enforce":         "restricted",
			"pod-security.kubernetes.io/enforce-version": "latest",
			"pod-security.kubernetes.io/audit":           "restricted",
			"pod-security.kubernetes.io/warn":            "restricted",
		}},
	}); err != nil {
		return err
	}
	prefixes := make([]string, 0, len(h.report.Nodes))
	for _, node := range h.report.Nodes {
		prefixes = append(prefixes, node.PodCIDR)
	}
	privatePrefix, err := privateClusterPrefix(prefixes)
	if err != nil {
		return err
	}
	fixtureDirectory := filepath.Join(h.temporary, "fixture")
	if err := os.Mkdir(fixtureDirectory, 0o700); err != nil {
		return err
	}
	if _, err := h.run("generate local TLS fixture", "go", "run", "./test/functional/fixture", "generate", "--directory", fixtureDirectory, "--subnet", privatePrefix); err != nil {
		return err
	}
	policyPath := filepath.Join(fixtureDirectory, "policy.json")
	policyRaw, err := os.ReadFile(policyPath) // #nosec G304 -- this is the exact invocation-owned fixture path.
	if err != nil {
		return err
	}
	var policy map[string]any
	if err := json.Unmarshal(policyRaw, &policy); err != nil {
		return err
	}
	profiles, ok := policy["privateProfiles"].([]any)
	if !ok || len(profiles) != 1 {
		return errors.New("generated federation policy has an unexpected private profile")
	}
	profile, ok := profiles[0].(map[string]any)
	if !ok {
		return errors.New("generated federation policy profile is invalid")
	}
	profile["origins"] = []string{"https://federation-fixture:8443", "https://decoy-fixture:8443"}
	if h.agntcy != nil {
		profile["origins"] = []string{"https://federation-fixture:8443", "https://decoy-fixture:8443", agntcyOrigin}
	}
	if h.gateway != nil {
		profile["origins"] = []string{"https://federation-fixture:8443", "https://decoy-fixture:8443", gatewayOrigin}
		// Same allowed origin and tenant, but deliberately without the peer CA.
		untrusted := map[string]any{}
		for k, v := range profile {
			untrusted[k] = v
		}
		untrusted["name"] = "gateway-untrusted"
		delete(untrusted, "trustBundleFile")
		untrusted["addressRanges"] = []string{privatePrefix, localServiceCIDR}
		policy["privateProfiles"] = append(profiles, untrusted)
	}
	if h.ans != nil {
		profile["origins"] = []string{"https://federation-fixture:8443", "https://decoy-fixture:8443", ansOrigin}
		untrusted := map[string]any{}
		for k, v := range profile {
			untrusted[k] = v
		}
		untrusted["name"] = "ans-untrusted"
		delete(untrusted, "trustBundleFile")
		untrusted["addressRanges"] = []string{privatePrefix, localServiceCIDR}
		policy["privateProfiles"] = append(profiles, untrusted)
	}
	if h.peerTLS != nil {
		origins, ok := profile["origins"].([]string)
		if !ok {
			return errors.New("invalid Catalog peer policy origins")
		}
		profile["origins"] = append(origins, peerOrigin("a"), peerOrigin("b"))
	}
	if h.options.scenario == "replicas" {
		// Only this owned two-tenant queue case approves both tenants for the fixture origin.
		profile["assignedTenantIds"] = []string{primaryTenantID, otherTenantID}
	}
	profile["addressRanges"] = []string{privatePrefix, localServiceCIDR}
	policy["policyId"] = "minikube-local-fixtures"
	policyRaw, err = json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	policyRaw = append(policyRaw, '\n')
	caRaw, err := os.ReadFile(filepath.Join(fixtureDirectory, "trust", "functional-ca.pem")) // #nosec G304 -- the path is generated under the invocation-owned fixture directory.
	if err != nil {
		return err
	}
	if h.agntcy != nil {
		caRaw = append(caRaw, []byte(h.agntcy.tls["ca.pem"])...)
	}
	if h.gateway != nil {
		caRaw = append(caRaw, []byte(h.gateway.tls["ca.pem"])...)
	}
	if h.ans != nil {
		caRaw = append(caRaw, []byte(h.ans.tls["ca.pem"])...)
	}
	if h.peerTLS != nil {
		caRaw = append(caRaw, []byte(h.peerTLS["ca.pem"])...)
	}
	certificateRaw, err := os.ReadFile(filepath.Join(fixtureDirectory, "server.pem")) // #nosec G304 -- the path is generated under the invocation-owned fixture directory.
	if err != nil {
		return err
	}
	certificateKeyRaw, err := os.ReadFile(filepath.Join(fixtureDirectory, "server-key.pem")) // #nosec G304 -- the path is generated under the invocation-owned fixture directory.
	if err != nil {
		return err
	}
	signingKey, jwkSet, err := newSigningKey()
	if err != nil {
		return err
	}
	h.signingKey = signingKey
	if err := h.createSecretsAndConfiguration(string(policyRaw), string(caRaw), string(certificateRaw), string(certificateKeyRaw), string(jwkSet)); err != nil {
		return err
	}
	if h.options.scenario == "pgbouncer" {
		if err := h.preparePgBouncer(); err != nil {
			return err
		}
	}
	if h.vault != nil {
		if err := h.prepareVaultDatabase(); err != nil {
			return err
		}
	}
	// Apply independently so failure diagnostics identify the exact boundary.
	for _, object := range h.dependencyObjects() {
		kind, ok := object["kind"].(string)
		if !ok || kind == "" {
			return errors.New("generated dependency has no Kubernetes kind")
		}
		if err := h.applyObject("apply "+strings.ToLower(kind)+" dependency", object); err != nil {
			return err
		}
	}
	if _, err := h.kubectl("wait for PostgreSQL", "-n", h.catalogNamespace(), "rollout", "status", "statefulset/postgres", "--timeout=3m"); err != nil {
		return err
	}
	if err := h.createDatabaseRoles(); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for local identity fixture", "-n", h.catalogNamespace(), "rollout", "status", "deployment/identity-fixture", "--timeout=2m"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for network decoy fixture", "-n", h.catalogNamespace(), "rollout", "status", "deployment/decoy-fixture", "--timeout=2m"); err != nil {
		return err
	}
	if h.vault != nil {
		if err := h.installVault(); err != nil {
			return err
		}
	}
	h.report.Checks = append(h.report.Checks, "local-postgres-identity-and-http-fixtures")
	return nil
}

// createSecretsAndConfiguration separates generated credentials and private keys into Secrets and non-secret fixture policy and trust into ConfigMaps.
func (h *harness) createSecretsAndConfiguration(policy, ca, certificate, certificateKey, jwkSet string) error {
	ownerDSN := fmt.Sprintf("postgres://catalog_owner:%s@postgres:5432/catalog?sslmode=disable", h.secrets.ownerPassword)
	apiDSN := fmt.Sprintf("postgres://catalog_api:%s@postgres:5432/catalog?sslmode=disable", h.secrets.apiPassword)
	workerDSN := fmt.Sprintf("postgres://catalog_worker:%s@postgres:5432/catalog?sslmode=disable", h.secrets.workerPassword)
	if h.options.scenario == "pgbouncer" {
		ownerDSN = fmt.Sprintf("postgres://catalog_owner:%s@postgres:5432/catalog?sslmode=verify-full&sslrootcert=/etc/open-agentic-catalog/database/ca.pem", h.secrets.ownerPassword)
		apiDSN = fmt.Sprintf("postgres://catalog_api:%s@catalog-pgbouncer:6432/catalog?sslmode=verify-full&sslrootcert=/etc/open-agentic-catalog/database/ca.pem", h.secrets.apiPassword)
		workerDSN = fmt.Sprintf("postgres://catalog_worker:%s@catalog-pgbouncer:6432/catalog?sslmode=verify-full&sslrootcert=/etc/open-agentic-catalog/database/ca.pem", h.secrets.workerPassword)
	}
	if h.vault != nil {
		ownerDSN = vaultDSN("catalog_owner", h.secrets.ownerPassword)
		apiDSN = vaultDSN("catalog_api", h.secrets.apiPassword)
		workerDSN = vaultDSN("catalog_worker", h.secrets.workerPassword)
	}
	for _, secret := range []struct {
		label  string
		name   string
		values map[string]string
	}{
		{label: "create PostgreSQL owner Secret", name: "postgres-owner", values: map[string]string{"username": "catalog_owner", "password": h.secrets.ownerPassword, "database": "catalog"}},
		{label: "create role bootstrap Secret", name: "catalog-role-passwords", values: map[string]string{"api": h.secrets.apiPassword, "worker": h.secrets.workerPassword}},
		{label: "create API database Secret", name: "catalog-api-database", values: map[string]string{"dsn": apiDSN}},
		{label: "create worker database Secret", name: "catalog-worker-database", values: map[string]string{"dsn": workerDSN}},
		{label: "create migration database Secret", name: "catalog-migration-database", values: map[string]string{"dsn": ownerDSN}},
		{label: "create cursor signing Secret", name: "catalog-cursor-signing", values: map[string]string{"key": h.secrets.cursorKey}},
		{label: "create local fixture material", name: "local-fixture-material", values: map[string]string{"server.pem": certificate, "server-key.pem": certificateKey, "jwks.json": jwkSet}},
	} {
		if h.vault != nil {
			if secret.name == "catalog-api-database" || secret.name == "catalog-worker-database" || secret.name == "catalog-migration-database" || secret.name == "catalog-cursor-signing" {
				continue
			}
			if secret.name == "local-fixture-material" {
				if err := h.vaultFixtureValues(secret.values); err != nil {
					return err
				}
			}
		}
		if err := h.createSecret(secret.label, secret.name, secret.values); err != nil {
			return err
		}
	}
	if h.gateway != nil {
		data, err := gatewayCredentialData(h.gateway.secrets)
		if err != nil {
			return err
		}
		if err := h.createSecret("create exact tenant/origin Gateway credential bindings", "catalog-gateway-credentials", data); err != nil {
			return err
		}
	}
	for _, entry := range []struct{ name, component, key, value string }{
		{name: "catalog-federation-policy", component: "federation-policy", key: "policy.json", value: policy},
		{name: "catalog-federation-trust", component: "federation-trust", key: "functional-ca.pem", value: ca},
		{name: "postgres-init", component: "postgres", key: "010-catalog.sql", value: "CREATE EXTENSION IF NOT EXISTS pgcrypto;\n"},
	} {
		if err := h.applyObject("create "+entry.component+" ConfigMap", kubernetesObject{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": entry.name, "namespace": h.catalogNamespace(), "labels": testLabels(entry.component)},
			"data":     map[string]string{entry.key: entry.value},
		}); err != nil {
			return err
		}
	}
	return nil
}

// dependencyObjects builds the local PostgreSQL and identity/network fixtures; topology tests keep these dependencies on the control-plane node.
func (h *harness) dependencyObjects() []kubernetesObject {
	placement := map[string]string{}
	if h.options.scenario == "topology" {
		for _, node := range h.report.Nodes {
			if node.ControlPlane {
				placement["kubernetes.io/hostname"] = node.Name
				break
			}
		}
	}
	serviceAccount := kubernetesObject{
		"apiVersion": "v1", "kind": "ServiceAccount",
		"metadata":                     map[string]any{"name": "fixture", "namespace": h.catalogNamespace(), "labels": testLabels("fixture")},
		"automountServiceAccountToken": false,
	}
	postgresService := kubernetesObject{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": "postgres", "namespace": h.catalogNamespace(), "labels": testLabels("postgres")},
		"spec":     map[string]any{"selector": map[string]string{"app.kubernetes.io/name": "open-agentic-catalog-e2e", "app.kubernetes.io/component": "postgres"}, "ports": []any{map[string]any{"name": "postgres", "port": 5432, "targetPort": "postgres"}}},
	}
	postgresContainer := map[string]any{
		"name": "postgres", "image": h.postgres, "imagePullPolicy": "Never",
		"ports": []any{map[string]any{"name": "postgres", "containerPort": 5432}},
		"env": []any{
			secretEnvironment("POSTGRES_USER", "postgres-owner", "username"), secretEnvironment("POSTGRES_PASSWORD", "postgres-owner", "password"), secretEnvironment("POSTGRES_DB", "postgres-owner", "database"),
		},
		"readinessProbe":  map[string]any{"exec": map[string]any{"command": []string{"pg_isready", "-U", "catalog_owner", "-d", "catalog"}}, "periodSeconds": 2, "timeoutSeconds": 2, "failureThreshold": 30},
		"livenessProbe":   map[string]any{"exec": map[string]any{"command": []string{"pg_isready", "-U", "catalog_owner", "-d", "catalog"}}, "periodSeconds": 10, "timeoutSeconds": 2, "failureThreshold": 6},
		"resources":       map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "256Mi"}, "limits": map[string]string{"cpu": "1", "memory": "1Gi"}},
		"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": false, "capabilities": map[string]any{"drop": []string{"ALL"}}},
		"volumeMounts": []any{
			map[string]any{"name": "data", "mountPath": "/var/lib/postgresql"}, map[string]any{"name": "run", "mountPath": "/var/run/postgresql"}, map[string]any{"name": "tmp", "mountPath": "/tmp"}, map[string]any{"name": "init", "mountPath": "/docker-entrypoint-initdb.d", "readOnly": true},
		},
	}
	postgresVolumes := []any{
		map[string]any{"name": "run", "emptyDir": map[string]any{"sizeLimit": "32Mi"}}, map[string]any{"name": "tmp", "emptyDir": map[string]any{"sizeLimit": "32Mi"}}, map[string]any{"name": "init", "configMap": map[string]any{"name": "postgres-init"}},
	}
	if h.options.scenario == "pgbouncer" || h.vault != nil {
		postgresContainer["args"] = []string{"postgres", "-c", "ssl=on", "-c", "ssl_cert_file=/etc/postgres/tls/tls.crt", "-c", "ssl_key_file=/etc/postgres/tls/tls.key"}
		mounts, _ := postgresContainer["volumeMounts"].([]any) // Fixed literal above.
		postgresContainer["volumeMounts"] = append(mounts, map[string]any{"name": "database-tls", "mountPath": "/etc/postgres/tls", "readOnly": true})
		// #nosec G101 -- Secret name, never credential content.
		postgresVolumes = append(postgresVolumes, map[string]any{"name": "database-tls", "secret": map[string]any{"secretName": "postgres-tls", "defaultMode": int32(0440)}})
		// Set the extended volume list before placing it into the fixed Pod spec.
	}
	if h.vault != nil {
		args, _ := postgresContainer["args"].([]string)
		postgresContainer["args"] = append(args, "-c", "hba_file=/etc/postgres/auth/pg_hba.conf")
		mounts, _ := postgresContainer["volumeMounts"].([]any)
		postgresContainer["volumeMounts"] = append(mounts, map[string]any{"name": "vault-postgres-auth", "mountPath": "/etc/postgres/auth", "readOnly": true})
		postgresVolumes = append(postgresVolumes, map[string]any{"name": "vault-postgres-auth", "configMap": map[string]string{"name": "vault-postgres-auth"}})
	}
	postgres := kubernetesObject{
		"apiVersion": "apps/v1", "kind": "StatefulSet",
		"metadata": map[string]any{"name": "postgres", "namespace": h.catalogNamespace(), "labels": testLabels("postgres")},
		"spec": map[string]any{
			"serviceName": "postgres", "replicas": 1,
			"selector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": "open-agentic-catalog-e2e", "app.kubernetes.io/component": "postgres"}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": testLabels("postgres")},
				"spec": map[string]any{
					"serviceAccountName": "fixture", "automountServiceAccountToken": false, "enableServiceLinks": false, "nodeSelector": placement,
					"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 999, "runAsGroup": 999, "fsGroup": 999, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
					"containers":      []any{postgresContainer},
					"volumes":         postgresVolumes,
				},
			},
			"volumeClaimTemplates": []any{map[string]any{
				"metadata": map[string]any{"name": "data", "labels": testLabels("postgres")},
				"spec":     map[string]any{"accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]string{"storage": "2Gi"}}},
			}},
		},
	}
	identityService := h.fixtureService("identity-fixture", "identity", "app.kubernetes.io/component", "identity", 8081)
	federationService := h.fixtureService("federation-fixture", "federation", "open-agentic-catalog.github.io/fixture", "primary", 8443)
	decoyService := h.fixtureService("decoy-fixture", "decoy", "app.kubernetes.io/component", "decoy", 8443)
	identity := h.fixtureDeployment("identity-fixture", "identity", placement)
	decoy := h.fixtureDeployment("decoy-fixture", "decoy", placement)
	return []kubernetesObject{serviceAccount, postgresService, postgres, identityService, federationService, decoyService, identity, decoy}
}

// fixtureService builds a namespaced fixture Service with explicit component selection and a named target port.
func (h *harness) fixtureService(name, component, selectorKey, selectorValue string, port int) kubernetesObject {
	return kubernetesObject{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": name, "namespace": h.catalogNamespace(), "labels": testLabels(component)},
		"spec":     map[string]any{"selector": map[string]string{"app.kubernetes.io/name": "open-agentic-catalog-e2e", selectorKey: selectorValue}, "ports": []any{map[string]any{"name": component, "port": port, "targetPort": component}}},
	}
}

// fixtureDeployment builds a tokenless, non-root fixture Pod template using invocation-generated identity and TLS material.
func (h *harness) fixtureDeployment(name, component string, placement map[string]string) kubernetesObject {
	labels := testLabels(component)
	if component == "identity" {
		labels["open-agentic-catalog.github.io/fixture"] = "primary"
	}
	ports := []any{map[string]any{"name": "identity", "containerPort": 8081}}
	fixtureArgs := []string{"serve", "--directory", "/fixture", "--oidc-address", ":8081", "--oidc-issuer", oidcIssuer, "--oidc-jwks", "/fixture/jwks.json"}
	materialSecret := map[string]any{"secretName": "local-fixture-material", "defaultMode": 292}
	if h.vault != nil {
		fixtureArgs = append(fixtureArgs, "--credentials")
		materialSecret["items"] = []any{map[string]string{"key": "server.pem", "path": "server.pem"}, map[string]string{"key": "server-key.pem", "path": "server-key.pem"}, map[string]string{"key": "jwks.json", "path": "jwks.json"}, map[string]string{"key": "functional-ca.pem", "path": "trust/functional-ca.pem"}, map[string]string{"key": "bearer.token", "path": "credentials/bearer.token"}}
	}
	if component == "identity" {
		ports = append(ports, map[string]any{"name": "federation", "containerPort": 8443})
	} else {
		ports = append(ports, map[string]any{"name": "decoy", "containerPort": 8443})
	}
	return kubernetesObject{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": h.catalogNamespace(), "labels": testLabels(component)},
		"spec": map[string]any{
			"replicas": 1,
			"selector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": "open-agentic-catalog-e2e", "app.kubernetes.io/component": component}},
			"template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": map[string]any{
				"serviceAccountName": "fixture", "automountServiceAccountToken": false, "enableServiceLinks": false, "nodeSelector": placement,
				"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
				"containers": []any{map[string]any{
					"name": "fixture", "image": h.fixture, "imagePullPolicy": "Never",
					"args":            fixtureArgs,
					"ports":           ports,
					"readinessProbe":  map[string]any{"httpGet": map[string]any{"path": "/healthz", "port": 8081}, "periodSeconds": 2, "timeoutSeconds": 1, "failureThreshold": 30},
					"livenessProbe":   map[string]any{"httpGet": map[string]any{"path": "/healthz", "port": 8081}, "periodSeconds": 10, "timeoutSeconds": 2, "failureThreshold": 3},
					"resources":       map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "16Mi"}, "limits": map[string]string{"cpu": "200m", "memory": "64Mi"}},
					"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}},
					"volumeMounts":    []any{map[string]any{"name": "material", "mountPath": "/fixture", "readOnly": true}, map[string]any{"name": "tmp", "mountPath": "/tmp"}},
				}},
				"volumes": []any{map[string]any{"name": "material", "secret": materialSecret}, map[string]any{"name": "tmp", "emptyDir": map[string]any{"sizeLimit": "16Mi"}}},
			}},
		},
	}
}

// secretEnvironment references one Secret key from a container environment variable without embedding its value.
func secretEnvironment(name, secret, key string) map[string]any {
	return map[string]any{"name": name, "valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": secret, "key": key}}}
}

// databaseRoleJob creates exact runtime identities in the explicitly selected fixture database.
func (h *harness) databaseRoleJob(name, host string) kubernetesObject {
	script := `attempt=0
until pg_isready --host="$PGHOST" --port="$PGPORT" --username="$PGUSER" --dbname="$PGDATABASE" --timeout=1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 30 ]; then
    echo "PostgreSQL did not become reachable before the bounded bootstrap deadline" >&2
    exit 1
  fi
  sleep 1
done
psql --no-psqlrc --set=ON_ERROR_STOP=1 --set=api_password="$API_PASSWORD" --set=worker_password="$WORKER_PASSWORD" <<'SQL'
SELECT 'CREATE ROLE catalog_api' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='catalog_api') \gexec
SELECT 'CREATE ROLE catalog_worker' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='catalog_worker') \gexec
SELECT format('ALTER ROLE catalog_api WITH LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS', :'api_password') \gexec
SELECT format('ALTER ROLE catalog_worker WITH LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION BYPASSRLS', :'worker_password') \gexec
GRANT CONNECT ON DATABASE catalog TO catalog_api, catalog_worker;
GRANT USAGE ON SCHEMA public TO catalog_api, catalog_worker;
SQL`
	return h.databaseJob(name, script, []any{
		map[string]any{"name": "PGHOST", "value": host}, map[string]any{"name": "PGPORT", "value": "5432"},
		map[string]any{"name": "PGUSER", "value": "catalog_owner"}, map[string]any{"name": "PGDATABASE", "value": "catalog"},
		secretEnvironment("PGPASSWORD", "postgres-owner", "password"), secretEnvironment("API_PASSWORD", "catalog-role-passwords", "api"), secretEnvironment("WORKER_PASSWORD", "catalog-role-passwords", "worker"),
	})
}

// createDatabaseRoles uses a dedicated fixture Job to create distinct API and worker identities, then verifies PostgreSQL 18, pgcrypto and both roles.
func (h *harness) createDatabaseRoles() error {
	job := h.databaseRoleJob("database-role-bootstrap", "postgres")
	if err := h.applyObject("create exact PostgreSQL roles", job); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for PostgreSQL role bootstrap", "-n", h.catalogNamespace(), "wait", "--for=condition=complete", "job/database-role-bootstrap", "--timeout=2m"); err != nil {
		return err
	}
	query := "SELECT current_setting('server_version_num')::int / 10000, EXISTS(SELECT 1 FROM pg_extension WHERE extname='pgcrypto'), (SELECT count(*) FROM pg_roles WHERE rolname IN ('catalog_api','catalog_worker'))"
	raw, err := h.kubectl("verify PostgreSQL 18 and core roles", "-n", h.catalogNamespace(), "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-U", "catalog_owner", "-d", "catalog", "-At", "-F", ",", "-c", query)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) != "18,t,2" {
		return fmt.Errorf("unexpected PostgreSQL dependency identity %q", strings.TrimSpace(string(raw)))
	}
	return nil
}

// databaseJob builds a deadline-bounded, non-retrying psql fixture Job with caller-supplied script and environment.
func (h *harness) databaseJob(name, script string, environment []any) kubernetesObject {
	placement := map[string]string{}
	if h.options.scenario == "topology" {
		for _, node := range h.report.Nodes {
			if node.ControlPlane {
				placement["kubernetes.io/hostname"] = node.Name
			}
		}
	}
	mounts := []any{map[string]any{"name": "tmp", "mountPath": "/tmp"}}
	volumes := []any{map[string]any{"name": "tmp", "emptyDir": map[string]any{"sizeLimit": "16Mi"}}}
	if h.vault != nil {
		environment = append(environment, map[string]any{"name": "PGSSLMODE", "value": "verify-full"}, map[string]any{"name": "PGSSLROOTCERT", "value": "/etc/open-agentic-catalog/database/ca.pem"})
		mounts = append(mounts, map[string]any{"name": "database-trust", "mountPath": "/etc/open-agentic-catalog/database", "readOnly": true})
		volumes = append(volumes, map[string]any{"name": "database-trust", "configMap": map[string]string{"name": "database-ca"}})
	}
	job := kubernetesObject{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": name, "namespace": h.catalogNamespace(), "labels": testLabels("database-bootstrap")},
		"spec": map[string]any{"backoffLimit": 0, "activeDeadlineSeconds": 120, "ttlSecondsAfterFinished": 300, "template": map[string]any{
			"metadata": map[string]any{"labels": testLabels("database-bootstrap")},
			"spec": map[string]any{
				"restartPolicy": "Never", "serviceAccountName": "fixture", "automountServiceAccountToken": false, "enableServiceLinks": false, "nodeSelector": placement,
				"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 999, "runAsGroup": 999, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
				"containers": []any{map[string]any{
					"name": "psql", "image": h.postgres, "imagePullPolicy": "Never", "command": []string{"/bin/sh", "-ec", script}, "env": environment,
					"resources":       map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "32Mi"}, "limits": map[string]string{"cpu": "200m", "memory": "128Mi"}},
					"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}},
					"volumeMounts":    mounts,
				}}, "volumes": volumes,
			},
		}},
	}
	return job
}

// writeHelmValues writes invocation-owned application values for the selected scenario, including test-only topology, credentials references and enabled fixture integrations.
func (h *harness) writeHelmValues(marker, apiRole string) (string, error) {
	repository, tag, found := strings.Cut(h.application, ":")
	if !found {
		return "", errors.New("developer image has no tag")
	}
	replicas := h.options.runtimeReplicas()
	apiCPURequest := "25m"
	if h.options.scenario == "autoscaling" {
		apiCPURequest = "50m"
	}
	nodeSelector := map[string]string{}
	spread := false
	if h.options.scenario == "topology" {
		nodeSelector["open-agentic-catalog.github.io/test-worker"] = "true"
		spread = true
	}
	hosts := []map[string]string{{"host": "127.0.0.1", "tenantId": primaryTenantID, "mode": "anonymous", "publisherDomain": "catalog.example.com"}}
	if h.ans != nil {
		hosts = append(hosts, map[string]string{"host": "localhost", "tenantId": primaryTenantID, "mode": "authenticated", "publisherDomain": "catalog.example.com"})
	}
	discovery, err := json.Marshal(hosts)
	if err != nil {
		return "", err
	}
	values := map[string]any{
		"fullnameOverride":              "catalog",
		"image":                         map[string]any{"repository": repository, "tag": tag, "digest": "", "pullPolicy": "Never"},
		"terminationGracePeriodSeconds": 40,
		"api": map[string]any{
			"replicas": replicas, "topologySpread": map[string]any{"enabled": spread, "topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "DoNotSchedule"},
			"podDisruptionBudget": map[string]any{"enabled": true, "maxUnavailable": 1},
			"resources":           map[string]any{"requests": map[string]string{"cpu": apiCPURequest, "memory": "64Mi"}, "limits": map[string]string{"cpu": "500m", "memory": "256Mi"}},
		},
		"worker": map[string]any{
			"replicas": replicas, "topologySpread": map[string]any{"enabled": spread, "topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "DoNotSchedule"},
			"podDisruptionBudget": map[string]any{"enabled": true, "maxUnavailable": 1},
			"resources":           map[string]any{"requests": map[string]string{"cpu": "25m", "memory": "64Mi"}, "limits": map[string]string{"cpu": "750m", "memory": "384Mi"}},
		},
		"migration":     map[string]any{"activeDeadlineSeconds": 120, "backoffLimit": 0, "ttlSecondsAfterFinished": 300, "podAnnotations": map[string]string{}, "resources": map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "32Mi"}, "limits": map[string]string{"cpu": "250m", "memory": "128Mi"}}},
		"databaseRoles": map[string]string{"api": apiRole, "worker": "catalog_worker"},
		"secrets": map[string]any{
			"apiDatabase": map[string]string{"name": "catalog-api-database", "key": "dsn"}, "workerDatabase": map[string]string{"name": "catalog-worker-database", "key": "dsn"},
			"migrationDatabase": map[string]string{"name": "catalog-migration-database", "key": "dsn"}, "cursorSigning": map[string]string{"name": "catalog-cursor-signing", "key": "key"},
		},
		"federation":     map[string]any{"policy": map[string]string{"existingConfigMap": "catalog-federation-policy", "key": "policy.json", "document": "{}"}, "trustBundleConfigMap": "catalog-federation-trust"},
		"podAnnotations": map[string]string{"open-agentic-catalog.github.io/test-rollout": marker}, "nodeSelector": nodeSelector,
		"configEnv": map[string]any{
			"CATALOG_ENVIRONMENT": "test", "CATALOG_PROFILE": "minikube-e2e", "CATALOG_PUBLIC_BASE_URL": "http://127.0.0.1",
			"CATALOG_AUTH_ISSUER": oidcIssuer, "CATALOG_AUTH_AUDIENCE": oidcAudience, "CATALOG_AUTH_TENANCY_MODE": "shared", "CATALOG_AUTH_ORGANIZATION_CLAIM": "organization_id",
			"CATALOG_DISCOVERY_HOSTS": string(discovery), "CATALOG_DATABASE_MAX_CONNECTIONS": "8", "CATALOG_WORKER_CONCURRENCY": "2", "CATALOG_WORKER_LEASE_DURATION": "10s",
			"CATALOG_WORKER_SOURCE_MAINTENANCE_INTERVAL": "1m", "CATALOG_FEDERATION_REQUEST_TIMEOUT": "5s",
		},
	}
	if h.options.scenario == "topology" {
		values["podDNSOptions"] = []map[string]string{{"name": "use-vc"}}
	}
	if h.options.scenario == "replicas" || h.options.scenario == "topology" || h.options.scenario == "peers" {
		// The shared federation workflow qualifies these non-default budgets,
		// including an upstream retry minimum added to the capped local delay.
		config, ok := values["configEnv"].(map[string]any)
		if !ok {
			return "", errors.New("invalid federation test Helm values")
		}
		config["CATALOG_JOBS_MAX_ATTEMPTS"] = "7"
		config["CATALOG_JOBS_RETRY_CEILING"] = "5.5s"
		if h.options.scenario == "replicas" {
			config["CATALOG_WORKER_CONCURRENCY"] = "1"
		}
	}
	if h.options.scenario == "pgbouncer" {
		values["databaseTrustBundleConfigMap"] = "database-ca"
		if h.pgbouncerDirect {
			values["pgbouncer"] = map[string]any{"enabled": false}
		} else {
			values["pgbouncer"] = pooledChartValues()
		}
		config, ok := values["configEnv"].(map[string]any)
		if !ok {
			return "", errors.New("invalid generated pooler configuration")
		}
		config["CATALOG_DATABASE_MAX_CONNECTIONS"] = "4"
	}
	if h.vault != nil {
		values["databaseTrustBundleConfigMap"] = "database-ca"
		values["podDNSOptions"] = []map[string]string{{"name": "use-vc"}}
		references, ok := values["secrets"].(map[string]any)
		if !ok {
			return "", errors.New("invalid generated vault Secret references")
		}
		references["federationCredentials"] = map[string]string{"name": vaultSourceSecret}
		config, ok := values["configEnv"].(map[string]any)
		if !ok {
			return "", errors.New("invalid generated vault configuration")
		}
		config["CATALOG_CREDENTIAL_RESOLUTION_ENABLED"] = "true"
		config["CATALOG_CREDENTIAL_RESOLUTION_DIRECTORY"] = "/etc/open-agentic-catalog/federation/credentials"
	}
	if h.gateway != nil {
		secrets, ok := values["secrets"].(map[string]any)
		if !ok {
			return "", errors.New("invalid generated Gateway secrets")
		}
		secrets["federationCredentials"] = map[string]string{"name": "catalog-gateway-credentials"}
		config, ok := values["configEnv"].(map[string]any)
		if !ok {
			return "", errors.New("invalid generated Gateway configuration")
		}
		config["CATALOG_CREDENTIAL_RESOLUTION_ENABLED"] = "true"
		config["CATALOG_CREDENTIAL_RESOLUTION_DIRECTORY"] = "/etc/open-agentic-catalog/federation/credentials"
		config["CATALOG_JOBS_MAX_ATTEMPTS"] = "20"
		config["CATALOG_JOBS_RETRY_CEILING"] = "15s"
	}
	if h.ans != nil {
		config, ok := values["configEnv"].(map[string]any)
		if !ok {
			return "", errors.New("invalid generated ANS configuration")
		}
		config["CATALOG_FEDERATION_MAX_LIVE_PAGES_PER_SOURCE"] = "3"
		if h.ans.trustConfig != nil {
			// The enabled verifier needs its 15s attempt plus the 5s lease margin.
			config["CATALOG_WORKER_LEASE_DURATION"] = "30s"
			raw, err := json.Marshal(h.ans.trustConfig)
			if err != nil {
				return "", err
			}
			config["CATALOG_TRUST_VERIFICATION"] = string(raw)
			values["trustVerification"] = map[string]any{"publicKeysConfigMap": "ans-public-keys"}
			config["CATALOG_JOBS_MAX_ATTEMPTS"] = "20"
			config["CATALOG_JOBS_RETRY_CEILING"] = "5s"
		}
	}
	if h.peerTrustConfig != nil {
		config, ok := values["configEnv"].(map[string]any)
		if !ok {
			return "", errors.New("invalid peer trust configuration")
		}
		raw, err := json.Marshal(h.peerTrustConfig)
		if err != nil {
			return "", err
		}
		config["CATALOG_TRUST_VERIFICATION"] = string(raw)
		config["CATALOG_WORKER_LEASE_DURATION"] = "30s"
		values["trustVerification"] = map[string]any{"publicKeysConfigMap": "peer-public-keys"}
	}
	if h.semantic != nil {
		values["semanticSearch"] = h.semantic
	}
	if h.tracing {
		secrets, secretsOK := values["secrets"].(map[string]any)
		config, configOK := values["configEnv"].(map[string]any)
		if !secretsOK || !configOK {
			return "", errors.New("invalid generated tracing value sections")
		}
		secrets["tracingInputs"] = map[string]string{"name": "catalog-tracing"}
		settings, err := json.Marshal(map[string]any{"enabled": true, "endpoint": "https://federation-fixture:8443/v1/traces", "installation": "minikube-tracing", "sampleRatio": 1, "allowedAddressPrefixes": []string{localServiceCIDR}, "headersFile": "/etc/open-agentic-catalog/tracing/headers.json", "caFile": "/etc/open-agentic-catalog/tracing/ca.pem"})
		if err != nil {
			return "", err
		}
		config["CATALOG_TRACING"] = string(settings)
	}
	if h.webhooks {
		secrets, secretsOK := values["secrets"].(map[string]any)
		config, configOK := values["configEnv"].(map[string]any)
		if !secretsOK || !configOK {
			return "", errors.New("invalid generated Helm value sections")
		}
		secrets["webhookEncryption"] = map[string]string{"name": "catalog-webhook-key", "key": "key"}
		values["webhooks"] = map[string]string{"egressConfigMap": "catalog-webhook-egress"}
		config["CATALOG_WEBHOOKS_ENABLED"] = "true"
		config["CATALOG_WEBHOOKS_TIMEOUT"] = "2s"
		config["CATALOG_WEBHOOKS_POLL_INTERVAL"] = "100ms"
		config["CATALOG_WEBHOOKS_RETRY_CEILING"] = "5s"
	}
	if h.options.scenario == "peers" {
		if err := configureCatalogPeerValues(values, h.catalogNamespace()); err != nil {
			return "", err
		}
	}
	raw, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(h.temporary, fmt.Sprintf("values-%s-%s.json", marker, apiRole))
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// provisionIdentity runs an explicit schema-owner operator Job to establish one deterministic test tenant, realm and subject.
func (h *harness) provisionIdentity(name, tenantID, organization, subject, roles string) error {
	command := []string{"/usr/local/bin/catalogctl", "identity", "provision", "--tenant", tenantID, "--tenant-name", "Minikube " + organization, "--issuer", oidcIssuer, "--tenancy-mode", "shared", "--external-organization", organization, "--subject", subject, "--roles", roles, "--operator", "minikube-harness", "--reason", "Provision deterministic local identity"}
	mounts := []any{map[string]any{"name": "tmp", "mountPath": "/tmp"}}
	volumes := []any{map[string]any{"name": "tmp", "emptyDir": map[string]any{"sizeLimit": "16Mi"}}}
	if h.options.scenario == "pgbouncer" || h.vault != nil {
		mounts = append(mounts, map[string]any{"name": "database-trust", "mountPath": "/etc/open-agentic-catalog/database", "readOnly": true})
		volumes = append(volumes, map[string]any{"name": "database-trust", "configMap": map[string]any{"name": "database-ca"}})
	}
	job := kubernetesObject{
		"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": name, "namespace": h.catalogNamespace(), "labels": testLabels("identity-provision")},
		"spec": map[string]any{
			"backoffLimit": 0, "activeDeadlineSeconds": 120, "ttlSecondsAfterFinished": 300,
			"template": map[string]any{
				"metadata": map[string]any{"labels": testLabels("identity-provision")},
				"spec": map[string]any{
					"restartPolicy": "Never", "serviceAccountName": "fixture", "automountServiceAccountToken": false, "enableServiceLinks": false,
					"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
					"containers": []any{map[string]any{
						"name": "provision", "image": h.application, "imagePullPolicy": "Never", "command": command,
						"env":             []any{map[string]any{"name": "CATALOG_ENVIRONMENT", "value": "test"}, map[string]any{"name": "CATALOG_PROFILE", "value": "minikube-provision"}, map[string]any{"name": "CATALOG_DATABASE_DRIVER", "value": "postgres"}, secretEnvironment("CATALOG_DATABASE_DSN", "catalog-migration-database", "dsn")},
						"resources":       map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "32Mi"}, "limits": map[string]string{"cpu": "250m", "memory": "128Mi"}},
						"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}}, "volumeMounts": mounts,
					}},
					"volumes": volumes,
				},
			},
		},
	}
	if err := h.applyObject("provision "+organization+" identity realm", job); err != nil {
		return err
	}
	_, err := h.kubectl("wait for "+organization+" identity provisioning", "-n", h.catalogNamespace(), "wait", "--for=condition=complete", "job/"+name, "--timeout=2m")
	return err
}

// issueTokens signs the fixed smoke identities with the invocation's fixture key; these tokens are not issued by Catalog.
func (h *harness) issueTokens() error {
	if h.signingKey == nil {
		return errors.New("OIDC signing key is absent")
	}
	h.tokens = make(map[string]string)
	for name, identity := range map[string]struct{ subject, organization string }{
		"admin":       {"oidc-admin", "tenant-one"},
		"contributor": {"oidc-contributor", "tenant-one"},
		"reviewer":    {"oidc-reviewer", "tenant-one"},
		"publisher":   {"oidc-publisher", "tenant-one"},
		"reader":      {"oidc-reader", "tenant-one"},
		"other":       {"oidc-other-admin", "tenant-two"},
	} {
		token, err := signToken(h.signingKey, identity.subject, identity.organization, time.Now().UTC())
		if err != nil {
			return err
		}
		h.tokens[name] = token
	}
	return nil
}
