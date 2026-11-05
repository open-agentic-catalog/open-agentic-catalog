//go:build minikube

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type databaseRenewalReport struct {
	Phases                      []databaseRenewalPhase `json:"phases"`
	ProjectedWithoutRestart     bool                   `json:"projectedWithoutRestart"`
	StagedLeavesNotActive       bool                   `json:"stagedLeavesNotActive"`
	PredecessorOnlyTrustRefused bool                   `json:"predecessorOnlyTrustRefused"`
}

type databaseRenewalPhase struct {
	Name         string                            `json:"name"`
	TrustSHA256  string                            `json:"trustSha256"`
	PoolerLeaf   string                            `json:"poolerLeaf"`
	PostgresLeaf string                            `json:"postgresLeaf"`
	Backends     map[string]databaseRenewalBackend `json:"backends"`
	JobID        string                            `json:"jobId"`
}

type databaseRenewalBackend struct {
	PID        int       `json:"pid"`
	Started    time.Time `json:"started"`
	Postmaster time.Time `json:"postmaster"`
}

func databaseHash(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func databaseLeaf(raw string) (*x509.Certificate, error) {
	block, rest := pem.Decode([]byte(raw))
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid owned database leaf")
	}
	return x509.ParseCertificate(block.Bytes)
}

// databaseConnection uses a loopback-only, invocation-owned port-forward. TLS
// still verifies the namespace-local server name and exact served certificate.
func databaseConnection(ctx context.Context, address, name, role, password, trust, leaf string) (*pgx.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return nil, errors.New("invalid owned database forward")
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return nil, errors.New("invalid owned database port")
	}
	expected, err := databaseLeaf(leaf)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(trust)) {
		return nil, errors.New("invalid owned database trust")
	}
	cfg, err := pgx.ParseConfig("postgres://127.0.0.1/catalog?sslmode=require")
	if err != nil {
		return nil, err
	}
	cfg.Host, cfg.Port, cfg.User, cfg.Password = host, uint16(number), role, password
	cfg.ConnectTimeout, cfg.Fallbacks = 5*time.Second, nil
	cfg.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: name,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || !bytes.Equal(state.PeerCertificates[0].Raw, expected.Raw) {
				return errors.New("unexpected verified database leaf")
			}
			return nil
		}}
	return pgx.ConnectConfig(ctx, cfg)
}

func databaseBackend(ctx context.Context, conn *pgx.Conn, role string, pooled bool) (databaseRenewalBackend, error) {
	var result databaseRenewalBackend
	var user string
	var encrypted, recovery bool
	err := conn.QueryRow(ctx, `SELECT current_user,pg_backend_pid(),a.backend_start,pg_postmaster_start_time(),s.ssl,pg_is_in_recovery() FROM pg_stat_activity a JOIN pg_stat_ssl s USING(pid) WHERE a.pid=pg_backend_pid()`).Scan(&user, &result.PID, &result.Started, &result.Postmaster, &encrypted, &recovery)
	mode := conn.PgConn().ParameterStatus("pgbouncer.pool_mode")
	if err != nil || user != role || !encrypted || recovery || (pooled && mode != "session") || (!pooled && mode != "") {
		return result, errors.New("database renewal lost exact role, TLS, writer or routing")
	}
	return result, nil
}

// readDatabasePublicMaterial selects only public bytes from the Kubernetes
// response. Existing private key fields never enter command output or reports.
func (h *harness) readDatabasePublicMaterial() (map[string]string, error) {
	result := map[string]string{}
	for _, item := range []struct{ kind, name, key string }{{"configmap", "database-ca", "ca\\.pem"}, {"secret", "postgres-tls", "tls\\.crt"}, {"secret", "pgbouncer-tls", "tls\\.crt"}} {
		raw, err := h.kubectl("read public database certificate", "-n", namespace, "get", item.kind, item.name, "-o", "jsonpath={.data."+item.key+"}")
		if err != nil {
			return nil, err
		}
		if item.kind == "secret" {
			raw, err = base64.StdEncoding.DecodeString(string(raw))
			if err != nil {
				return nil, err
			}
		}
		if len(raw) == 0 {
			return nil, errors.New("missing public database certificate")
		}
		result[item.name] = string(raw)
	}
	return result, nil
}

// attachDatabaseTrustProbe mounts only the existing public trust volume in a
// test-only diagnostic container. No tool or credential enters the app image.
func (h *harness) attachDatabaseTrustProbe(pod string) error {
	raw, err := h.kubectl("inspect public trust mount", "-n", namespace, "get", "pod", pod, "-o", "json")
	if err != nil {
		return err
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	spec, ok := object["spec"].(map[string]any)
	if !ok {
		return errors.New("missing owned Pod spec")
	}
	existing, _ := spec["ephemeralContainers"].([]any)
	spec["ephemeralContainers"] = append(existing, map[string]any{
		"name": "database-trust-probe", "image": h.postgres, "imagePullPolicy": "Never", "command": []string{"sleep", "3600"},
		"volumeMounts":    []any{map[string]any{"name": "database-trust", "mountPath": "/database-trust", "readOnly": true}},
		"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65532, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
	})
	raw, err = json.Marshal(object)
	if err != nil {
		return err
	}
	if _, err := h.kubectlInput("attach public trust probe", raw, "replace", "--raw", "/api/v1/namespaces/"+namespace+"/pods/"+pod+"/ephemeralcontainers", "-f", "-"); err != nil {
		return err
	}
	_, err = h.kubectl("wait for public trust probe", "-n", namespace, "wait", "pod/"+pod, "--for=jsonpath={.status.ephemeralContainerStatuses[?(@.name==\"database-trust-probe\")].state.running}", "--timeout=60s")
	return err
}

type databaseProjection struct{ pod, container, path, value string }

func (h *harness) awaitDatabaseProjection(files []databaseProjection) error {
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Minute)
	defer cancel()
	return waitTopology(ctx, func() (bool, error) {
		for _, file := range files {
			raw, err := h.topologyCommand(ctx, "observe projected database input", nil, "-n", namespace, "exec", file.pod, "-c", file.container, "--", "sha256sum", file.path)
			if err != nil {
				return false, err
			}
			if !databaseProjectionMatches(raw, file.path, file.value) {
				return false, nil
			}
		}
		return true, nil
	})
}

func databaseProjectionMatches(raw []byte, path, value string) bool {
	return strings.TrimSpace(string(raw)) == databaseHash([]byte(value))+"  "+path
}

func (h *harness) databasePods() (map[string][]podIdentity, error) {
	result := map[string][]podIdentity{}
	for _, role := range []string{"api", "worker", "pgbouncer", "postgres"} {
		pods, err := h.pods(role)
		count := 1
		if role == "api" || role == "worker" {
			count = 2
		}
		if err != nil || len(pods) != count {
			return nil, fmt.Errorf("database renewal requires %d %s Pods", count, role)
		}
		for _, pod := range pods {
			if !pod.Ready || pod.Restarts != 0 {
				return nil, errors.New("database renewal requires ready, unrestarted Pods")
			}
		}
		result[role] = pods
	}
	return result, nil
}

func databasePodTransition(before, after map[string][]podIdentity, activate, restartPostgres bool) error {
	for _, role := range []string{"api", "worker", "pgbouncer", "postgres"} {
		if len(before[role]) == 0 || len(before[role]) != len(after[role]) {
			return errors.New("database renewal changed replica intent")
		}
		seen := map[string]bool{}
		for _, pod := range after[role] {
			if pod.UID == "" || seen[pod.UID] || pod.ImageID != before[role][0].ImageID || pod.ServiceAccount != before[role][0].ServiceAccount {
				return errors.New("database renewal lost a unique Pod, image or account")
			}
			seen[pod.UID] = true
			if !pod.Ready || pod.Restarts != 0 {
				return errors.New("database renewal has unready or restarted Pods")
			}
			previous := false
			for _, old := range before[role] {
				if old.UID == pod.UID {
					previous = true
				}
			}
			replace := activate && (role != "postgres" || restartPostgres)
			if previous == replace {
				return errors.New("database Pod activation differs from the selected phase")
			}
		}
	}
	return nil
}

// databaseOwnerSQL selects the real server container even after diagnostic
// containers attach. Combined command output must remain strict SQL evidence.
func (h *harness) databaseOwnerSQL(query string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()
	return h.topologyCommand(ctx, "observe direct database renewal state", nil, "-n", namespace, "exec", "postgres-0", "-c", "postgres", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-At", "-c", query)
}

func (h *harness) replaceDatabaseConsumers(restartPostgres bool) error {
	// This is certificate maintenance, not a chart/schema upgrade. Drain API,
	// then workers and the single pooler. Wait for actual old Pods to disappear.
	for _, role := range []string{"api", "worker", "pgbouncer"} {
		if _, err := h.kubectl("drain "+role+" for database TLS", "-n", namespace, "scale", "deployment/catalog-"+role, "--replicas=0"); err != nil {
			return err
		}
		if _, err := h.kubectl("wait for old "+role+" Pods", "-n", namespace, "wait", "pod", "-l", "app.kubernetes.io/instance=catalog,app.kubernetes.io/component="+role, "--for=delete", "--timeout=90s"); err != nil {
			return err
		}
	}
	raw, err := h.databaseOwnerSQL("SELECT count(*) FROM pg_stat_activity WHERE usename IN ('catalog_api','catalog_worker')")
	if err != nil || strings.TrimSpace(string(raw)) != "0" {
		return errors.New("old runtime database sessions did not drain")
	}
	if restartPostgres {
		if _, err := h.kubectl("activate PostgreSQL server certificate", "-n", namespace, "delete", "pod/postgres-0", "--wait=true", "--timeout=90s"); err != nil {
			return err
		}
		if _, err := h.kubectl("wait for PostgreSQL certificate activation", "-n", namespace, "rollout", "status", "statefulset/postgres", "--timeout=2m"); err != nil {
			return err
		}
	}
	for _, role := range []string{"pgbouncer", "api", "worker"} {
		count := "2"
		if role == "pgbouncer" {
			count = "1"
		}
		if _, err := h.kubectl("activate "+role+" database inputs", "-n", namespace, "scale", "deployment/catalog-"+role, "--replicas="+count); err != nil {
			return err
		}
		if _, err := h.kubectl("wait for "+role+" database activation", "-n", namespace, "rollout", "status", "deployment/catalog-"+role, "--timeout=2m"); err != nil {
			return err
		}
	}
	return nil
}

func (h *harness) databaseRenewalJob(baseURL, sourceID, phase string) (string, error) {
	get, err := h.request(http.MethodGet, baseURL+"/api/v1/federation/sources/"+sourceID, h.tokens["admin"], "", "", nil, nil)
	var source struct {
		Revision int64 `json:"revision"`
	}
	if err != nil || get.Status != 200 || json.Unmarshal(get.Body, &source) != nil || source.Revision < 1 {
		return "", errors.New("read current renewal source")
	}
	accepted, err := h.request(http.MethodPost, baseURL+"/api/v1/federation/sources/"+sourceID+":sync", h.tokens["admin"], "database-renewal-"+h.suffix+"-"+phase, fmt.Sprintf(`"rev-%d"`, source.Revision), map[string]any{}, nil)
	var envelope struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if err != nil || accepted.Status != 202 || json.Unmarshal(accepted.Body, &envelope) != nil || !identity.ValidTenantID(envelope.Job.ID) {
		return "", errors.New("enqueue database renewal Job")
	}
	done, err := h.waitJobState(baseURL, envelope.Job.ID, "succeeded", 30*time.Second)
	if err != nil || done["id"] != envelope.Job.ID {
		return "", errors.New("renewal worker did not complete the accepted Job")
	}
	// Bind the HTTP result to its persisted tenant-owned Job.
	raw, err := h.databaseOwnerSQL(fmt.Sprintf("SELECT state FROM jobs WHERE tenant_id='%s' AND id='%s'", primaryTenantID, envelope.Job.ID))
	if err != nil || strings.TrimSpace(string(raw)) != "succeeded" {
		return "", errors.New("renewal Job has no retained completion")
	}
	return envelope.Job.ID, nil
}

func (h *harness) databaseRenewalSource(baseURL string) (string, error) {
	inspection, err := h.request(http.MethodPost, baseURL+"/api/v1/federation/source-inspections", h.tokens["admin"], "database-inspect-"+h.suffix, "", map[string]any{"url": "https://federation-fixture:8443/.well-known/ard.json", "networkProfile": "functional-fixture"}, nil)
	var inspected struct {
		ID string `json:"id"`
	}
	if err != nil || inspection.Status != 201 || json.Unmarshal(inspection.Body, &inspected) != nil || !identity.ValidTenantID(inspected.ID) {
		return "", fmt.Errorf("inspect database renewal source: HTTP %d", inspection.Status)
	}
	response, err := h.request(http.MethodPost, baseURL+"/api/v1/federation/sources", h.tokens["admin"], "database-source-"+h.suffix, "", map[string]any{"inspectionId": inspected.ID, "displayName": "Database renewal fixture", "retrievalMode": "indexed", "scope": map[string]any{"kinds": []string{"agent"}}, "updateMode": "manual_review", "networkProfile": "functional-fixture", "reason": "Qualify worker database reconnection"}, nil)
	var source struct {
		ID string `json:"id"`
	}
	if err != nil || response.Status != 201 || json.Unmarshal(response.Body, &source) != nil || !identity.ValidTenantID(source.ID) {
		return "", fmt.Errorf("connect database renewal source: HTTP %d", response.Status)
	}
	return source.ID, nil
}

func (h *harness) observeDatabasePhase(name, trust, poolerLeaf, postgresLeaf, sourceID, runID string, smoke []byte) (databaseRenewalPhase, error) {
	result := databaseRenewalPhase{Name: name, TrustSHA256: databaseHash([]byte(trust)), Backends: map[string]databaseRenewalBackend{}}
	poolerCert, err := databaseLeaf(poolerLeaf)
	if err != nil {
		return result, err
	}
	postgresCert, err := databaseLeaf(postgresLeaf)
	if err != nil {
		return result, err
	}
	result.PoolerLeaf, result.PostgresLeaf = databaseHash(poolerCert.Raw), databaseHash(postgresCert.Raw)
	// These SQL controls use owned loopback tunnels. The actual runtime Pods
	// separately prove ordinary network routing and successful API/Job work.
	for _, entry := range []struct {
		role, password, name, resource, leaf string
		port                                 int
	}{
		{"catalog_api", h.secrets.apiPassword, "catalog-pgbouncer", "service/catalog-pgbouncer", poolerLeaf, 6432},
		{"catalog_worker", h.secrets.workerPassword, "catalog-pgbouncer", "service/catalog-pgbouncer", poolerLeaf, 6432},
		{"catalog_owner", h.secrets.ownerPassword, "postgres", "pod/postgres-0", postgresLeaf, 5432},
	} {
		address, err := h.startPortForward(entry.resource, entry.port)
		if err != nil {
			return result, err
		}
		ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
		conn, err := databaseConnection(ctx, address, entry.name, entry.role, entry.password, trust, entry.leaf)
		if err != nil {
			cancel()
			return result, fmt.Errorf("%s fresh verified database connection failed", entry.role)
		}
		backend, err := databaseBackend(ctx, conn, entry.role, entry.role != "catalog_owner")
		_ = conn.Close(ctx)
		cancel()
		if err != nil {
			return result, err
		}
		result.Backends[entry.role] = backend
	}
	if result.Backends["catalog_api"].Postmaster != result.Backends["catalog_owner"].Postmaster || result.Backends["catalog_worker"].Postmaster != result.Backends["catalog_owner"].Postmaster {
		return result, errors.New("pooler uses a different PostgreSQL instance")
	}
	pods, err := h.databasePods()
	if err != nil {
		return result, err
	}
	for role, selected := range pods {
		h.recordPlacement("database-tls-"+name+"-"+role, selected)
	}
	// Address counts prove all actual runtime sessions use the current pooler.
	address, err := h.policyAddress(pods["pgbouncer"][0].Name, "6432")
	if err != nil {
		return result, err
	}
	ip, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(ip) == nil {
		return result, errors.New("invalid pooler Pod IP")
	}
	raw, err := h.databaseOwnerSQL(fmt.Sprintf("SELECT count(*) FILTER(WHERE usename='catalog_api'),count(*) FILTER(WHERE usename='catalog_worker'),count(*) FILTER(WHERE client_addr IS DISTINCT FROM '%s'::inet OR NOT s.ssl) FROM pg_stat_activity a JOIN pg_stat_ssl s USING(pid) WHERE usename IN ('catalog_api','catalog_worker')", ip))
	if err != nil {
		return result, err
	}
	if err := validatePooledBackendCounts(raw); err != nil {
		return result, err
	}
	forwarded, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return result, err
	}
	baseURL := "http://" + forwarded
	if err := h.waitHTTP(baseURL + "/readyz"); err != nil {
		return result, err
	}
	replay, err := h.runSmoke(baseURL, runID)
	if err != nil {
		return result, err
	}
	if !sameSmokeState(smoke, replay) {
		return result, errors.New("database certificate maintenance changed governed state")
	}
	result.JobID, err = h.databaseRenewalJob(baseURL, sourceID, name)
	return result, err
}

func (h *harness) verifyDatabaseRenewal(runID string, smoke []byte) error {
	old, err := h.readDatabasePublicMaterial()
	if err != nil {
		return err
	}
	next, err := databaseTLSMaterial(time.Now())
	if err != nil {
		return err
	}
	for _, name := range []string{"postgres", "catalog-pgbouncer"} {
		if _, err := tls.X509KeyPair([]byte(next[name+".crt"]), []byte(next[name+".key"])); err != nil {
			return err
		}
	}
	forwarded, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	sourceID, err := h.databaseRenewalSource("http://" + forwarded)
	if err != nil {
		return err
	}
	report := &databaseRenewalReport{}
	h.report.DatabaseTLS = report
	original, err := h.observeDatabasePhase("baseline", old["database-ca"], old["pgbouncer-tls"], old["postgres-tls"], sourceID, runID, smoke)
	if err != nil {
		return err
	}
	report.Phases = append(report.Phases, original)
	for _, phase := range []string{"overlap", "successor", "retired"} {
		fmt.Printf("[pgbouncer] qualify database certificate %s\n", phase)
		before, err := h.databasePods()
		if err != nil {
			return err
		}
		trust := old["database-ca"] + next["ca.pem"]
		if phase == "retired" {
			trust = next["ca.pem"]
		}
		if phase != "successor" {
			for _, role := range []string{"api", "worker"} {
				for _, pod := range before[role] {
					if err := h.attachDatabaseTrustProbe(pod.Name); err != nil {
						return err
					}
				}
			}
			if err := h.applyObject("replace public database CA bundle", kubernetesObject{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "database-ca", "namespace": namespace, "labels": testLabels("database-trust")}, "data": map[string]string{"ca.pem": trust}}); err != nil {
				return err
			}
			files := []databaseProjection{{before["pgbouncer"][0].Name, "pgbouncer", "/etc/pgbouncer/trust/ca.pem", trust}}
			for _, role := range []string{"api", "worker"} {
				for _, pod := range before[role] {
					files = append(files, databaseProjection{pod.Name, "database-trust-probe", "/database-trust/ca.pem", trust})
				}
			}
			if err := h.awaitDatabaseProjection(files); err != nil {
				return err
			}
		} else {
			for _, entry := range []struct{ secret, name string }{{"postgres-tls", "postgres"}, {"pgbouncer-tls", "catalog-pgbouncer"}} {
				patch, err := json.Marshal(map[string]any{"data": map[string]string{"tls.crt": base64.StdEncoding.EncodeToString([]byte(next[entry.name+".crt"])), "tls.key": base64.StdEncoding.EncodeToString([]byte(next[entry.name+".key"]))}})
				if err != nil {
					return err
				}
				if _, err := h.kubectlInput("deliver successor database server keypair", patch, "-n", namespace, "patch", "secret", entry.secret, "--type=merge", "--patch-file=/dev/stdin"); err != nil {
					return err
				}
			}
			if err := h.awaitDatabaseProjection([]databaseProjection{{"postgres-0", "postgres", "/etc/postgres/tls/tls.crt", next["postgres.crt"]}, {before["pgbouncer"][0].Name, "pgbouncer", "/etc/pgbouncer/tls/tls.crt", next["catalog-pgbouncer.crt"]}}); err != nil {
				return err
			}
			// New files do not establish activation. Both processes still serve the
			// exact original leaves before their explicit replacement.
			for _, entry := range []struct {
				resource, name, leaf string
				port                 int
			}{{"service/catalog-pgbouncer", "catalog-pgbouncer", old["pgbouncer-tls"], 6432}, {"pod/postgres-0", "postgres", old["postgres-tls"], 5432}} {
				address, err := h.startPortForward(entry.resource, entry.port)
				if err != nil {
					return err
				}
				role, password := "catalog_api", h.secrets.apiPassword
				if entry.name == "postgres" {
					role, password = "catalog_owner", h.secrets.ownerPassword
				}
				ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
				conn, err := databaseConnection(ctx, address, entry.name, role, password, trust, entry.leaf)
				if err == nil {
					_ = conn.Close(ctx)
				}
				cancel()
				if err != nil {
					return errors.New("file-only database delivery unexpectedly changed the served leaf")
				}
			}
			report.StagedLeavesNotActive = true
		}
		projected, err := h.databasePods()
		if err != nil {
			return err
		}
		if err := databasePodTransition(before, projected, false, false); err != nil {
			return err
		}
		if phase != "successor" {
			report.ProjectedWithoutRestart = true
		}
		if err := h.replaceDatabaseConsumers(phase == "successor"); err != nil {
			return err
		}
		after, err := h.databasePods()
		if err != nil {
			return err
		}
		if err := databasePodTransition(before, after, true, phase == "successor"); err != nil {
			return err
		}
		poolerLeaf, postgresLeaf := next["catalog-pgbouncer.crt"], next["postgres.crt"]
		if phase == "overlap" {
			poolerLeaf, postgresLeaf = old["pgbouncer-tls"], old["postgres-tls"]
		}
		observed, err := h.observeDatabasePhase(phase, trust, poolerLeaf, postgresLeaf, sourceID, runID, smoke)
		if err != nil {
			return err
		}
		previous := report.Phases[len(report.Phases)-1]
		if err := databaseFreshBackends(previous, observed, phase == "successor"); err != nil {
			return err
		}
		report.Phases = append(report.Phases, observed)
	}
	// Wrong predecessor trust must fail at native certificate verification on
	// both newly activated endpoints, with successor SQL already proved above.
	for _, entry := range []struct {
		resource, name, leaf, role, password string
		port                                 int
	}{{"service/catalog-pgbouncer", "catalog-pgbouncer", next["catalog-pgbouncer.crt"], "catalog_api", h.secrets.apiPassword, 6432}, {"pod/postgres-0", "postgres", next["postgres.crt"], "catalog_owner", h.secrets.ownerPassword, 5432}} {
		address, err := h.startPortForward(entry.resource, entry.port)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
		conn, err := databaseConnection(ctx, address, entry.name, entry.role, entry.password, old["database-ca"], entry.leaf)
		if conn != nil {
			_ = conn.Close(ctx)
		}
		cancel()
		var authority x509.UnknownAuthorityError
		if !errors.As(err, &authority) {
			return errors.New("predecessor-only database trust did not produce native CA refusal")
		}
	}
	report.PredecessorOnlyTrustRefused = true
	h.report.Checks = append(h.report.Checks, "database-ca-projection-without-pod-activation", "database-server-secret-projection-without-leaf-activation", "database-successor-fresh-two-hop-sql-and-worker-jobs", "database-old-ca-retirement-and-direct-owner")
	return nil
}

func databaseFreshBackends(previous, current databaseRenewalPhase, restart bool) error {
	if previous.JobID == "" || current.JobID == "" || previous.JobID == current.JobID {
		return errors.New("renewal phase reused a Job")
	}
	for _, role := range []string{"catalog_api", "catalog_worker", "catalog_owner"} {
		old, ok := previous.Backends[role]
		next, present := current.Backends[role]
		if !ok || !present || old.PID < 1 || next.PID < 1 || old.Started.IsZero() || next.Started.IsZero() || old.Postmaster.IsZero() || next.Postmaster.IsZero() {
			return errors.New("missing database backend identity")
		}
		if old.PID == next.PID && old.Started.Equal(next.Started) && old.Postmaster.Equal(next.Postmaster) {
			return errors.New("renewal reused a backend")
		}
		if old.Postmaster.Equal(next.Postmaster) == restart {
			return errors.New("database restart differs from leaf activation")
		}
	}
	return nil
}
