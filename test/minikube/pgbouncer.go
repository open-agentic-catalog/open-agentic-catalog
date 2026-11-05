//go:build minikube

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"time"
)

const pgbouncerImage = "edoburu/pgbouncer:v1.26.0-p0@sha256:b17551c776ef7e5769ef80b956d20f85e2fd25dd8912d31d58f782aad495b711"

// databaseTLSMaterial creates invocation-owned trust and distinct endpoint keys.
// Nothing is logged; only the receiving Kubernetes Secrets obtain private keys.
func databaseTLSMaterial(now time.Time) (map[string]string, error) {
	return serviceTLSMaterial(now, "owned-database-ca", namespace, []string{"postgres", "catalog-pgbouncer"})
}

// serviceTLSMaterial creates independent keys for actual namespace-local fixture callers.
func serviceTLSMaterial(now time.Time, caName, selectedNamespace string, names []string, addresses ...net.IP) (map[string]string, error) {
	for _, address := range addresses {
		if address.To16() == nil {
			return nil, errors.New("invalid fixture certificate IP address")
		}
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: caName}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	// Use the encoded CA's generated SubjectKeyId so leaf certificates receive
	// AuthorityKeyId. Strict OpenSSL clients require that issuer binding.
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	material := map[string]string{"ca.pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))}
	for index, name := range names {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(index + 2)), IPAddresses: addresses, DNSNames: []string{name, name + "." + selectedNamespace, name + "." + selectedNamespace + ".svc", name + "." + selectedNamespace + ".svc.cluster.local"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
		if err != nil {
			return nil, err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		material[name+".crt"] = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		material[name+".key"] = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	}
	return material, nil
}

func (h *harness) preparePgBouncer() error {
	material, err := databaseTLSMaterial(time.Now())
	if err != nil {
		return err
	}
	for name, values := range map[string]map[string]string{
		"postgres-tls":   {"tls.crt": material["postgres.crt"], "tls.key": material["postgres.key"]},
		"pgbouncer-tls":  {"tls.crt": material["catalog-pgbouncer.crt"], "tls.key": material["catalog-pgbouncer.key"]},
		"pgbouncer-auth": {"userlist.txt": fmt.Sprintf("\"catalog_api\" \"%s\"\n\"catalog_worker\" \"%s\"\n", h.secrets.apiPassword, h.secrets.workerPassword)},
	} {
		if err := h.createSecret("create private "+name, name, values); err != nil {
			return err
		}
	}
	return h.applyObject("create database CA", kubernetesObject{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "database-ca", "namespace": namespace, "labels": testLabels("database-trust")}, "data": map[string]string{"ca.pem": material["ca.pem"]}})
}

func pooledChartValues() map[string]any {
	// #nosec G101 -- Existing Secret names only, never credential content.
	return map[string]any{"enabled": true, "image": pgbouncerImage, "upstream": map[string]any{"host": "postgres", "port": 5432, "database": "catalog"}, "authSecret": "pgbouncer-auth", "tlsSecret": "pgbouncer-tls", "upstreamCAConfigMap": "database-ca", "apiPoolSize": 16, "workerPoolSize": 16, "maxClientConnections": 48, "queryWaitTimeoutSeconds": 5, "writerEgress": []any{map[string]any{"podSelector": map[string]any{"matchLabels": testLabels("postgres")}}}}
}

func (h *harness) verifyPgBouncer(baseURL, runID string, smoke []byte) error {
	if err := h.applyNetworkPolicies(); err != nil {
		return err
	}
	// Restrict upstream ingress too: only the pooler plus explicit owner Jobs.
	if err := h.applyObject("restrict PostgreSQL ingress", kubernetesObject{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": "postgres-ingress", "namespace": namespace, "labels": testLabels("network-policy")}, "spec": map[string]any{"podSelector": map[string]any{"matchLabels": testLabels("postgres")}, "policyTypes": []string{"Ingress"}, "ingress": []any{map[string]any{"from": []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/instance": releaseName}, "matchExpressions": []any{map[string]any{"key": "app.kubernetes.io/component", "operator": "In", "values": []string{"pgbouncer", "migration", "identity-provision", "database"}}}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 5432}}}}}}); err != nil {
		return err
	}
	apis, err := h.pods("api")
	if err != nil || len(apis) != 2 {
		return errors.New("pooler qualification requires two API replicas")
	}
	workers, err := h.pods("worker")
	if err != nil || len(workers) != 2 {
		return errors.New("pooler qualification requires two worker replicas")
	}
	poolers, err := h.pods("pgbouncer")
	if err != nil || len(poolers) != 1 {
		return errors.New("pooler qualification requires one PgBouncer")
	}
	address, err := h.policyAddress(poolers[0].Name, "6432")
	if err != nil {
		return err
	}
	identity, err := h.policyPeer("identity")
	if err != nil {
		return err
	}
	// Use existing ephemeral probe attachment; its executable is invocation-owned.
	for _, pod := range append(apis, workers...) {
		if err := h.attachUIProbe(pod.Name); err != nil {
			return err
		}
		if err := h.awaitPolicyTCPFrom("pooler-permitted-"+pod.Name, pod.Name, "network-probe", address, true, pod.Name, "network-probe"); err != nil {
			return err
		}
	}
	if err := h.awaitPolicyTCPFrom("pooler-unselected-ingress-denied", identity, "", address, false, apis[0].Name, "network-probe"); err != nil {
		return err
	}
	// The selected UI identity has no database credentials. Probe its network
	// namespace with the maintained fixture executable, without installing a UI.
	uiLabels := map[string]string{"app.kubernetes.io/name": "open-agentic-catalog", "app.kubernetes.io/instance": releaseName, "app.kubernetes.io/component": "ui"}
	if err := h.applyObject("create credential-free UI policy probe", kubernetesObject{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "pooler-ui-probe", "namespace": namespace, "labels": uiLabels}, "spec": map[string]any{"automountServiceAccountToken": false, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}, "containers": []any{map[string]any{"name": "probe", "image": h.fixture, "imagePullPolicy": "Never", "command": []string{"/federation-fixture", "network-probe", "hold"}, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}}, "resources": map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "16Mi"}, "limits": map[string]string{"cpu": "100m", "memory": "32Mi"}}}}}}); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for UI policy probe", "-n", namespace, "wait", "pod/pooler-ui-probe", "--for=condition=Ready", "--timeout=60s"); err != nil {
		return err
	}
	if err := h.awaitPolicyTCPFrom("pooler-ui-ingress-denied", "pooler-ui-probe", "probe", address, false, apis[0].Name, "network-probe"); err != nil {
		return err
	}
	postgresAddress, err := h.policyAddress("postgres-0", "5432")
	if err != nil {
		return err
	}
	if err := h.attachUIProbe("postgres-0"); err != nil {
		return err
	}
	if err := h.awaitPolicyTCPFrom("postgres-ui-ingress-denied", "pooler-ui-probe", "probe", postgresAddress, false, "postgres-0", "network-probe"); err != nil {
		return err
	}
	poolerIP, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("SELECT count(*) FILTER (WHERE usename='catalog_api'), count(*) FILTER (WHERE usename='catalog_worker'), count(*) FILTER (WHERE client_addr IS DISTINCT FROM '%s'::inet) FROM pg_stat_activity WHERE usename IN ('catalog_api','catalog_worker')", poolerIP)
	out, err := h.kubectl("observe pooled PostgreSQL clients", "-n", namespace, "exec", "postgres-0", "-c", "postgres", "--", "psql", "-XAt", "-U", "catalog_owner", "-d", "catalog", "-c", query)
	if err != nil {
		return err
	}
	if err := validatePooledBackendCounts(out); err != nil {
		return err
	}
	if err := h.verifyCrossReplicaIdempotency(); err != nil {
		return err
	}
	h.report.Images["pgbouncer-running"] = poolers[0].ImageID
	originalRuntimeUIDs := map[string]string{}
	for _, pod := range append(apis, workers...) {
		originalRuntimeUIDs[pod.Name] = pod.UID
	}
	if _, err := h.kubectl("replace only pooler Pod", "-n", namespace, "delete", "pod", poolers[0].Name, "--wait=true", "--timeout=60s"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for replacement pooler", "-n", namespace, "rollout", "status", "deployment/catalog-pgbouncer", "--timeout=2m"); err != nil {
		return err
	}
	if err := h.waitHTTP(baseURL + "/readyz"); err != nil {
		return err
	}
	afterAPI, err := h.pods("api")
	if err != nil {
		return err
	}
	afterWorker, err := h.pods("worker")
	if err != nil {
		return err
	}
	for _, pod := range append(afterAPI, afterWorker...) {
		if originalRuntimeUIDs[pod.Name] != pod.UID {
			return errors.New("runtime restarted instead of recovering its original pool")
		}
	}
	// Existing replay, retained publication and no-op/config rollout assertions.
	if err := h.verifyReconfiguration(runID, smoke); err != nil {
		return err
	}
	values, err := h.writeHelmValues("pgbouncer-coordinated", "catalog_api")
	if err != nil {
		return err
	}
	if err := h.deployCatalog(values, "4m"); err != nil {
		return err
	}
	address, err = h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	if err := h.waitHTTP("http://" + address + "/readyz"); err != nil {
		return err
	}
	replayed, err := h.runSmoke("http://"+address, runID)
	if err != nil {
		return err
	}
	if !sameSmokeState(smoke, replayed) {
		return errors.New("coordinated pooled upgrade changed retained governed state")
	}
	h.report.Checks = append(h.report.Checks, "pgbouncer-coordinated-install-and-upgrade")
	h.report.Checks = append(h.report.Checks, "pgbouncer-private-tls-two-role-routing", "pgbouncer-replacement-original-api-recovery")
	if err := h.verifyDatabaseRenewal(runID, smoke); err != nil {
		return err
	}
	// Return the runtime Secrets to direct DSNs, drain by the maintained upgrade
	// coordinator, then remove the pooler. Never rewrite operator credentials.
	for role, password := range map[string]string{"api": h.secrets.apiPassword, "worker": h.secrets.workerPassword} {
		value := fmt.Sprintf("postgres://catalog_%s:%s@postgres:5432/catalog?sslmode=verify-full&sslrootcert=/etc/open-agentic-catalog/database/ca.pem", role, password)
		if err := h.applyObject("return "+role+" DSN to direct PostgreSQL", kubernetesObject{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "catalog-" + role + "-database", "namespace": namespace}, "stringData": map[string]string{"dsn": value}}); err != nil {
			return err
		}
	}
	// Remove test-only policy restrictions before the explicit direct transition.
	if _, err := h.kubectl("remove selected routing policies", "-n", namespace, "delete", "networkpolicy", "postgres-ingress", "catalog-api-egress", "catalog-worker-egress"); err != nil {
		return err
	}
	h.pgbouncerDirect = true
	values, err = h.writeHelmValues("pgbouncer-direct", "catalog_api")
	if err != nil {
		return err
	}
	if err := h.deployCatalog(values, "4m"); err != nil {
		return err
	}
	address, err = h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	if err := h.waitHTTP("http://" + address + "/readyz"); err != nil {
		return err
	}
	if _, err := h.runSmoke("http://"+address, runID); err != nil {
		return err
	}
	if resources, err := h.kubectl("check pooler removed", "-n", namespace, "get", "deployment,service,configmap,serviceaccount,networkpolicy", "-l", "app.kubernetes.io/component=pgbouncer", "-o", "json"); err != nil {
		return err
	} else {
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(resources, &list) != nil || len(list.Items) != 0 {
			return errors.New("disabled pooler retained chart resources")
		}
	}
	h.report.Checks = append(h.report.Checks, "pgbouncer-direct-return-and-disable")
	return nil
}

// validatePooledBackendCounts rejects incomplete or diagnostic-contaminated SQL
// output. Every runtime session must originate at the selected pooler Pod IP.
func validatePooledBackendCounts(raw []byte) error {
	fields := strings.Split(strings.TrimSpace(string(raw)), "|")
	if len(fields) != 3 {
		return errors.New("invalid pooled backend count evidence")
	}
	counts := make([]int, 3)
	for index, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil || value < 0 {
			return errors.New("invalid pooled backend count evidence")
		}
		counts[index] = value
	}
	if counts[0] < 1 || counts[1] < 1 || counts[2] != 0 {
		return fmt.Errorf("runtime routing counts: api=%d worker=%d bypass=%d", counts[0], counts[1], counts[2])
	}
	return nil
}
