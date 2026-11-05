//go:build minikube

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// qualifyGatewayCatalog keeps cluster mutations in the existing owned supervisor.
func (h *harness) qualifyGatewayCatalog(baseURL string) error {
	if err := h.deployGatewayCatalogOrigin(); err != nil {
		return err
	}
	if err := h.verifyGatewayOriginIsolation(); err != nil {
		return err
	}
	h.gateway.report.Lifecycle = make(map[string]any)
	h.gateway.report.Persistence = make(map[string]any)
	// Compare resolved safe policy metadata from each actual runtime environment.
	policies := make(map[string]string)
	for _, role := range []string{"api", "worker"} {
		raw, err := h.kubectl("verify Gateway runtime policy "+role, "-n", namespace, "exec", "deployment/catalog-"+role, "-c", "catalog-"+role, "--", "/usr/local/bin/catalogctl", "config", "validate", "--role", role)
		if err != nil {
			return err
		}
		var report struct {
			Configuration struct {
				Federation struct {
					PolicyDigest string `json:"policyDigest"`
				} `json:"federation"`
			} `json:"configuration"`
		}
		if err := json.Unmarshal(raw, &report); err != nil {
			return fmt.Errorf("decode runtime federation policy: %w", err)
		}
		if !agntcyDigest.MatchString(report.Configuration.Federation.PolicyDigest) {
			return errors.New("missing runtime federation policy digest")
		}
		policies[role] = report.Configuration.Federation.PolicyDigest
	}
	if policies["api"] != policies["worker"] {
		return errors.New("api and worker federation policies differ")
	}
	h.gateway.report.Persistence["runtimePolicy"] = policies
	output := filepath.Join(h.options.repositoryRoot, "output/interoperability/minikube-gateway-"+h.suffix)
	certificate := filepath.Join(h.temporary, "gateway-ca.pem")
	if err := os.WriteFile(certificate, []byte(h.gateway.tls["ca.pem"]), 0600); err != nil {
		return err
	}
	origin, err := h.startPortForwardIn(gatewayNamespace, "service/catalog-origin", 8443)
	if err != nil {
		return err
	}
	schemaDirectory := filepath.Join(h.temporary, "gateway-schemas")
	if err := os.MkdirAll(filepath.Join(schemaDirectory, "spec/schemas"), 0700); err != nil {
		return err
	}
	assets, err := h.gatewayAssetData()
	if err != nil {
		return err
	}
	for _, name := range []string{"ard-entry.schema.json", "ard.openapi.json"} {
		if err := os.WriteFile(filepath.Join(schemaDirectory, "spec/schemas", name), []byte(assets[name]), 0600); err != nil {
			return err
		}
	}
	settings := map[string]any{"schemaDirectory": schemaDirectory, "catalog": baseURL, "origin": "https://" + origin, "certificate": certificate, "token": h.tokens["admin"], "reader": h.tokens["reader"], "other": h.tokens["other"], "secrets": map[string]string{"admin": h.gateway.secrets[0], "reader": h.gateway.secrets[1], "private": h.gateway.secrets[2], "invalid": h.gateway.secrets[5]}}
	settingsPath, statePath := filepath.Join(h.temporary, "gateway-lifecycle.json"), filepath.Join(h.temporary, "gateway-state.json")
	if err := h.gatewayRegistryEnabled(false); err != nil {
		return err
	}
	phases := []string{"bootstrap", "add", "disable", "restore", "outage", "recovery", "advertised", "reverse"}
	for _, phase := range phases {
		if err := h.interopProgress("catalog-"+phase, nil, nil); err != nil {
			return err
		}
		switch phase {
		case "outage":
			if err := h.scaleGateway(0); err != nil {
				return err
			}
		case "recovery":
			if err := h.scaleGateway(1); err != nil {
				return err
			}
		case "advertised":
			if err := h.gatewayRegistryEnabled(true); err != nil {
				return err
			}
		}
		// A Service port-forward remains bound to its original Pod. Select a fresh one after replacement.
		if phase == "bootstrap" || phase == "recovery" || phase == "advertised" {
			peer, err := h.startPortForwardIn(gatewayNamespace, "service/gateway", 443)
			if err != nil {
				return err
			}
			settings["peer"] = "https://" + peer
		}
		raw, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		if err := os.WriteFile(settingsPath, raw, 0600); err != nil {
			return err
		}
		reportPath := filepath.Join(output, "catalog-"+phase+".json")
		ctx, cancel := context.WithTimeout(h.ctx, 3*time.Minute)
		_, runErr := h.runWithInput(ctx, "qualify Gateway Catalog "+phase, nil, h.gatewayPython(), "-B", "test/interoperability/gateway/lifecycle.py", phase, settingsPath, statePath, reportPath)
		cancel()
		raw, readErr := os.ReadFile(reportPath) // #nosec G304 -- exact invocation-owned report path.
		var report map[string]any
		if readErr == nil {
			readErr = json.Unmarshal(raw, &report)
			h.gateway.report.Lifecycle[phase] = report
		}
		if runErr != nil {
			return runErr
		}
		if readErr != nil {
			return readErr
		}
		if err := validateGatewayPhase(raw, phase); err != nil {
			return err
		}
		if phase != "reverse" {
			query, err := gatewayPersistenceQuery(raw)
			if err != nil {
				return err
			}
			persisted, err := h.kubectl("verify Gateway PostgreSQL "+phase, "-n", namespace, "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-At", "-c", query)
			if err != nil {
				return err
			}
			var evidence map[string]any
			if err := json.Unmarshal(persisted, &evidence); err != nil {
				return err
			}
			h.gateway.report.Persistence[phase] = evidence
			if err := gatewayPersistenceMatches(evidence); err != nil {
				return fmt.Errorf("%s: %w", phase, err)
			}
		}
	}
	raw, err := h.kubectl("retain native reverse-import guard refusal", "-n", gatewayNamespace, "logs", "deployment/gateway", "--tail=150")
	if err != nil {
		return err
	}
	lines := make([]string, 0, 2)
	for _, line := range strings.Split(h.redactDiagnostic(string(raw)), "\n") {
		if strings.Contains(line, "ARD ingestion: refusing catalog URL") && strings.Contains(line, "catalog-origin."+gatewayNamespace) && strings.Contains(line, "validation_type=ArdValidationError") {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return errors.New("native reverse importer lacks its exact private-address guard diagnostic")
	}
	h.gateway.report.Lifecycle["reverseGuard"] = lines
	reportRaw, err := json.MarshalIndent(h.gateway.report.Lifecycle, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "lifecycle.json"), append(reportRaw, '\n'), 0600); err != nil {
		return err
	}
	if h.options.interopTrust {
		phase, ok := h.gateway.report.Lifecycle["advertised"].(map[string]any)
		if !ok {
			return errors.New("missing advertised Gateway phase")
		}
		evidence, ok := phase["evidence"].(map[string]any)
		if !ok {
			return errors.New("missing Gateway evidence")
		}
		sourceID, ok := evidence["source_id"].(string)
		if !ok {
			return errors.New("missing Gateway assessment source")
		}
		if err := h.qualifyPeerTrust(baseURL, certificate, sourceID, false); err != nil {
			return err
		}
		logs, err := h.kubectl("verify Gateway zero ANS calls", "-n", gatewayNamespace, "exec", "deployment/gateway", "--", "cat", "/var/log/containers/ai-registry/nginx-access.log")
		if err != nil {
			return err
		}
		if err := noPeerEvidenceRequests(string(logs)); err != nil {
			return err
		}
		h.report.PeerTrust["evidenceRequests"] = 0
	}
	h.report.Checks = append(h.report.Checks, "gateway-catalog-enrollment-and-trust-refusals", "gateway-native-manifest-exact-persisted-members", "gateway-native-add-disable-restore", "gateway-same-job-outage-recovery", "gateway-advertised-interface-reviewed-replacement", "gateway-native-reverse-private-guard-before-http")
	return nil
}

func (h *harness) gatewayRegistryEnabled(enabled bool) error {
	oldPod, err := h.interopPod(gatewayNamespace, "component=gateway")
	if err != nil {
		return err
	}
	// Bind this nonsecret native flag to the Pod template. Do not depend on
	// kubelet Secret-cache propagation immediately after a Secret replacement.
	patch := fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"name":"gateway","env":[{"name":"ARD_REGISTRY_ENABLED","value":"%t"}]}]}}}}`, enabled)
	if _, err := h.kubectlInput("select native Registry advertisement", []byte(patch), "-n", gatewayNamespace, "patch", "deployment/gateway", "--type=strategic", "--patch-file=/dev/stdin"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait for native Gateway settings", "-n", gatewayNamespace, "rollout", "status", "deployment/gateway", "--timeout=3m"); err != nil {
		return err
	}
	// A terminating predecessor must not serve a different discovery contract.
	_, err = h.kubectl("wait for previous native Gateway Pod to stop", "-n", gatewayNamespace, "wait", "--for=delete", "pod/"+oldPod, "--timeout=1m")
	return err
}

func (h *harness) scaleGateway(replicas int) error {
	if replicas != 0 && replicas != 1 {
		return errors.New("invalid owned Gateway replica intent")
	}
	if _, err := h.kubectl("set owned Gateway outage", "-n", gatewayNamespace, "scale", "deployment/gateway", fmt.Sprintf("--replicas=%d", replicas)); err != nil {
		return err
	}
	if replicas == 0 {
		_, err := h.kubectl("wait for only owned Gateway to stop", "-n", gatewayNamespace, "wait", "--for=delete", "pods", "-l", "component=gateway", "--timeout=1m")
		return err
	}
	_, err := h.kubectl("wait for owned Gateway recovery", "-n", gatewayNamespace, "rollout", "status", "deployment/gateway", "--timeout=3m")
	return err
}

func (h *harness) deployGatewayCatalogOrigin() error {
	// The reverse importer retains its unmodified public-only guard. This server is a byte-preserving healthy control, never a bypass.
	if err := h.applyObject("mount owned Catalog HTTPS control certificate", kubernetesObject{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "catalog-origin-tls", "namespace": gatewayNamespace}, "stringData": map[string]string{"catalog-origin.crt": h.gateway.tls["catalog-origin.crt"], "catalog-origin.key": h.gateway.tls["catalog-origin.key"]}}); err != nil {
		return err
	}
	workload := gatewayWorkload("catalog-origin", h.gateway.aliases["registry"].Reference, nil)
	workload.container["command"] = []string{"/app/.venv/bin/python", "-B", "/qualification/catalog_origin.py"}
	workload.container["volumeMounts"] = []any{map[string]any{"name": "tls", "mountPath": "/tls", "readOnly": true}, map[string]any{"name": "qualification", "mountPath": "/qualification", "readOnly": true}}
	workload.container["readinessProbe"] = map[string]any{"httpGet": map[string]any{"path": "/requests", "port": 8443, "scheme": "HTTPS"}, "periodSeconds": 2}
	workload.pod["volumes"] = []any{map[string]any{"name": "tls", "secret": map[string]any{"secretName": "catalog-origin-tls"}}, map[string]any{"name": "qualification", "configMap": map[string]any{"name": "gateway-qualification", "items": []any{map[string]string{"key": "catalog_origin.py", "path": "catalog_origin.py"}}}}}
	if err := h.applyObject("deploy narrow owned Catalog HTTPS control", workload.object); err != nil {
		return err
	}
	if err := h.applyObject("expose owned Catalog HTTPS control", kubernetesObject{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "catalog-origin", "namespace": gatewayNamespace}, "spec": map[string]any{"selector": map[string]string{"component": "catalog-origin"}, "ports": []any{map[string]any{"port": 8443, "targetPort": 8443}}}}); err != nil {
		return err
	}
	if err := h.applyObject("allow only Catalog HTTPS control to product listener", kubernetesObject{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": "catalog-origin-api-egress", "namespace": gatewayNamespace}, "spec": map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"component": "catalog-origin"}}, "policyTypes": []string{"Egress"}, "egress": []any{map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": namespace}}, "podSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/instance": releaseName, "app.kubernetes.io/component": "api"}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 8080}}}}}}); err != nil {
		return err
	}
	// Catalog already permits its product API ingress. The separate peer policy still denies database and worker operations.
	_, err := h.kubectl("wait for Catalog HTTPS control", "-n", gatewayNamespace, "rollout", "status", "deployment/catalog-origin", "--timeout=2m")
	return err
}

func validateGatewayPhase(raw []byte, phase string) error {
	var report struct {
		Phase    string         `json:"phase"`
		Status   string         `json:"status"`
		Error    string         `json:"error"`
		Evidence map[string]any `json:"evidence"`
		Captures []any          `json:"captures"`
		Native   []any          `json:"native_captures"`
	}
	if json.Unmarshal(raw, &report) != nil || report.Phase != phase || report.Status != "pass" || report.Error != "" || len(report.Evidence) == 0 || len(report.Captures) == 0 || (phase != "outage" && len(report.Native) == 0) {
		return errors.New("missing or contradictory Gateway Catalog phase evidence")
	}
	return nil
}

// gatewayPersistenceQuery interpolates only validated UUIDs, bounded counts and exact digests into owner read-only assertions.
func gatewayPersistenceQuery(raw []byte) (string, error) {
	var report struct {
		Phase    string `json:"phase"`
		Evidence struct {
			Job struct {
				ID      string `json:"id"`
				Attempt int    `json:"attempt"`
			} `json:"job"`
			Snapshot struct {
				ID         string `json:"id"`
				SourceID   string `json:"sourceId"`
				BodyDigest string `json:"bodyDigest"`
				Members    int    `json:"memberCount"`
			} `json:"snapshot"`
			Failed struct {
				ID       string `json:"id"`
				SourceID string `json:"sourceId"`
			} `json:"failed_snapshot"`
			ReplacedSourceID string `json:"replaced_source_id"`
			PreviousSnapshot struct {
				ID         string `json:"id"`
				BodyDigest string `json:"bodyDigest"`
			} `json:"previous_snapshot"`
			Attempt int    `json:"attempt"`
			Last    string `json:"last_complete"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return "", err
	}
	e := report.Evidence
	source, snapshot, previous, action, attempt := e.Snapshot.SourceID, e.Snapshot.ID, e.Snapshot.ID, "source.sync.completed", e.Job.Attempt
	complete := true
	if report.Phase == "outage" {
		source, snapshot, previous, action, attempt = e.Failed.SourceID, e.Failed.ID, e.Last, "source.sync.failed", e.Attempt
		complete = false
	}
	for _, id := range []string{source, snapshot, previous, e.Job.ID} {
		if !agntcyUUID.MatchString(id) {
			return "", errors.New("invalid Gateway persistence identity")
		}
	}
	if attempt < 1 || attempt > 20 || e.Snapshot.Members < 0 || e.Snapshot.Members > 4 || (complete && !agntcyDigest.MatchString(e.Snapshot.BodyDigest)) {
		return "", errors.New("invalid Gateway persistence count or digest")
	}
	sourceStatus := "error"
	state, jobCheck, digestCheck := "failed", "job.attempt>="+fmt.Sprint(attempt), "true"
	if complete {
		sourceStatus = "active"
		state = "complete"
		jobCheck = fmt.Sprintf("job.state='succeeded' AND job.attempt=%d AND job.result->>'id'=snapshot.id::text", attempt)
		digestCheck = fmt.Sprintf("snapshot.body_digest='%s' AND snapshot.member_count=%d", e.Snapshot.BodyDigest, e.Snapshot.Members)
	}
	replacementCheck := "true"
	if report.Phase == "advertised" {
		if !agntcyUUID.MatchString(e.ReplacedSourceID) || e.ReplacedSourceID == source || !agntcyUUID.MatchString(e.PreviousSnapshot.ID) || !agntcyDigest.MatchString(e.PreviousSnapshot.BodyDigest) {
			return "", errors.New("invalid Gateway replacement evidence")
		}
		replacementCheck = fmt.Sprintf(`(SELECT count(*)=1 FROM federation_sources old JOIN source_snapshots retained ON retained.tenant_id=old.tenant_id AND retained.source_id=old.id AND retained.id=old.last_complete_snapshot_id WHERE old.tenant_id=source.tenant_id AND old.id='%s' AND old.endpoint=source.endpoint AND old.status='removed' AND retained.id='%s' AND retained.state='complete' AND retained.body_digest='%s')`, e.ReplacedSourceID, e.PreviousSnapshot.ID, e.PreviousSnapshot.BodyDigest)
	}
	return fmt.Sprintf(`SELECT json_build_object(
 'source_bound',source.tenant_id='%s' AND source.revision=snapshot.source_revision AND source.status='%s',
 'job_bound',job.kind='source.sync' AND job.payload->>'sourceId'=source.id::text AND (job.payload->>'sourceRevision')::bigint=source.revision AND (%s),
 'snapshot_bound',snapshot.state='%s' AND snapshot.completed_at IS NOT NULL AND source.last_complete_snapshot_id='%s',
 'digest_bound',%s,
 'replacement_bound',%s,
 'audit_bound',(SELECT count(*)=1 FROM audit_events a WHERE a.tenant_id=source.tenant_id AND a.target_id=snapshot.id::text AND a.action='%s' AND a.details->'facts'->>'jobId'=job.id::text AND (a.details->'facts'->>'jobAttempt')::int=%d),
 'observation_bytes',(SELECT count(*)=(SELECT retained.member_count FROM source_snapshots retained WHERE retained.tenant_id=source.tenant_id AND retained.source_id=source.id AND retained.id=source.last_complete_snapshot_id) AND count(*)=count(*) FILTER(WHERE o.exact_entry_digest='sha256:'||encode(digest(o.raw_entry_bytes,'sha256'),'hex') AND ((o.normalized_entry_bytes IS NULL AND o.normalized_entry_digest IS NULL AND o.kind IS NULL AND o.compatibility->>'support'='unknown') OR o.normalized_entry_digest='sha256:'||encode(digest(o.normalized_entry_bytes,'sha256'),'hex'))) FROM observations o WHERE o.tenant_id=source.tenant_id AND o.source_id=source.id AND o.freshness='current'))
 FROM federation_sources source JOIN source_snapshots snapshot ON snapshot.tenant_id=source.tenant_id AND snapshot.source_id=source.id AND snapshot.id='%s'
 JOIN jobs job ON job.tenant_id=source.tenant_id AND job.id='%s' WHERE source.id='%s' AND source.tenant_id='%s';`, primaryTenantID, sourceStatus, jobCheck, state, previous, digestCheck, replacementCheck, action, attempt, snapshot, e.Job.ID, source, primaryTenantID), nil
}

func gatewayPersistenceMatches(actual map[string]any) error {
	for _, key := range []string{"source_bound", "job_bound", "snapshot_bound", "digest_bound", "replacement_bound", "audit_bound", "observation_bytes"} {
		if actual[key] != true {
			return fmt.Errorf("gateway PostgreSQL %s did not hold", key)
		}
	}
	return nil
}

// gatewayCredentialData gives only the selected tenant and exact origin access to each opaque binding.
func gatewayCredentialData(secrets []string) (map[string]string, error) {
	if len(secrets) < 6 || secrets[0] == "" || secrets[5] == "" {
		return nil, errors.New("missing synthetic Gateway credentials")
	}
	data := map[string]string{}
	for name, index := range map[string]int{"gateway-admin": 0, "gateway-invalid": 5} {
		raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "tenantId": primaryTenantID, "revision": "qualification-v1", "origin": gatewayOrigin, "type": "bearer", "secretFile": name + "-token"})
		if err != nil {
			return nil, err
		}
		data[name+".json"] = string(raw)
		data[name+"-token"] = secrets[index]
	}
	return data, nil
}

// verifyGatewayOriginIsolation proves the new proxy path without granting peer workloads private Catalog listeners.
func (h *harness) verifyGatewayOriginIsolation() error {
	pod, err := h.interopPod(gatewayNamespace, "component=catalog-origin")
	if err != nil {
		return err
	}
	if err := h.attachNetworkProbe(gatewayNamespace, pod); err != nil {
		return err
	}
	selected := agntcyProbe{gatewayNamespace, pod, "network-probe"}
	identity, err := h.policyPeer("identity")
	if err != nil {
		return err
	}
	control := agntcyProbe{namespace, identity, ""}
	api, err := h.pods("api")
	if err != nil || len(api) != 1 {
		return errors.New("catalog origin requires one ready API")
	}
	worker, err := h.pods("worker")
	if err != nil || len(worker) != 1 {
		return errors.New("catalog origin requires one ready worker")
	}
	decoy, err := h.policyPeer("decoy")
	if err != nil {
		return err
	}
	for _, target := range []struct {
		name, pod, port string
		allowed         bool
	}{
		{"api-allowed", api[0].Name, "8080", true},
		{"database-denied", "postgres-0", "5432", false},
		{"worker-denied", worker[0].Name, "9090", false},
		{"unrelated-egress-denied", decoy, "8443", false},
	} {
		address, err := h.policyAddress(target.pod, target.port)
		if err != nil {
			return err
		}
		observer := control
		if target.allowed {
			observer = selected
		}
		if err := h.agntcyPolicyTCP("gateway-origin-"+target.name, address, target.allowed, selected, observer); err != nil {
			return err
		}
	}
	return nil
}
