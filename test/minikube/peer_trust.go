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

func peerTrustSelection(value string) (bool, error) {
	switch value {
	case "off":
		return false, nil
	case "on":
		return true, nil
	default:
		return false, errors.New("INTEROPERABILITY_TRUST_VERIFICATION must be off or on")
	}
}

func (h *harness) peerTrustPython() string {
	if h.options.scenario == "gateway" {
		return h.gatewayPython()
	}
	return h.agntcyPython()
}

// preparePeerTrust mounts existing public fixtures before the first runtime
// starts. No upstream ANS service, private key or product default is added.
func (h *harness) preparePeerTrust() error {
	if !h.options.interopTrust {
		return nil
	}
	origin, publisher := agntcyOrigin, "directory.agntcy.org"
	if h.options.scenario == "gateway" {
		origin, publisher = gatewayOrigin, "gateway.gateway-qualification.svc.cluster.local"
	}
	raw, err := h.run("prepare local peer verification authority", h.peerTrustPython(), "-B", "test/interoperability/peer_trust.py", "config", origin, publisher, "functional-fixture", "/etc/open-agentic-catalog/trust-verification")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &h.peerTrustConfig); err != nil {
		return err
	}
	keys := map[string]string{}
	for _, name := range []string{"ra.pem", "tl.pem"} {
		raw, err := os.ReadFile(filepath.Clean(filepath.Join(h.options.repositoryRoot, "internal/standards/anstrust/testdata/native", name)))
		if err != nil {
			return err
		}
		keys[name] = string(raw)
	}
	return h.applyObject("mount public peer assessment keys", kubernetesObject{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "peer-public-keys", "namespace": namespace}, "data": keys})
}

func (h *harness) qualifyPeerTrust(baseURL, certificate, sourceID string, empty bool) error {
	if !h.options.interopTrust {
		return nil
	}
	loaded := map[string]any{}
	for _, role := range []string{"api", "worker"} {
		raw, err := h.kubectl("inspect loaded peer verifier "+role, "-n", namespace, "exec", "deployment/catalog-"+role, "-c", "catalog-"+role, "--", "/usr/local/bin/catalogctl", "config", "validate", "--role", role)
		if err != nil {
			return err
		}
		var report struct {
			Configuration struct {
				Trust map[string]any `json:"trustVerification"`
			} `json:"configuration"`
		}
		if err := json.Unmarshal(raw, &report); err != nil {
			return fmt.Errorf("decode peer verifier configuration for %s: %w", role, err)
		}
		if report.Configuration.Trust["enabled"] != true || report.Configuration.Trust["policyCount"] != float64(1) {
			return errors.New("runtime peer verifier was not enabled")
		}
		loaded[role] = report.Configuration.Trust
	}
	api, err := json.Marshal(loaded["api"])
	if err != nil {
		return err
	}
	worker, err := json.Marshal(loaded["worker"])
	if err != nil {
		return err
	}
	if string(api) != string(worker) {
		return errors.New("peer API and worker trust policies differ")
	}
	settings := map[string]any{"catalog": baseURL, "certificate": certificate, "token": h.tokens["admin"], "reader": h.tokens["reader"], "other": h.tokens["other"], "source_id": sourceID, "empty": empty}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	path := filepath.Join(h.temporary, "peer-trust.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return err
	}
	output := filepath.Join(h.options.repositoryRoot, "output/interoperability/minikube-"+h.options.scenario+"-"+h.suffix, "trust.json")
	ctx, cancel := context.WithTimeout(h.ctx, 4*time.Minute)
	defer cancel()
	if _, err := h.runWithInput(ctx, "qualify explicit peer assessment", nil, h.peerTrustPython(), "-B", "test/interoperability/peer_trust.py", path, output); err != nil {
		return err
	}
	raw, err = os.ReadFile(filepath.Clean(output))
	if err != nil {
		return err
	}
	var result struct {
		Status string `json:"status"`
		JobID  string `json:"job_id"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Status != "pass" || empty == (result.JobID != "") {
		return errors.New("incomplete peer trust report")
	}
	query, err := peerTrustPersistenceQuery(sourceID, result.JobID)
	if err != nil {
		return err
	}
	persisted, err := h.kubectl("verify peer assessment owner SQL", "-n", namespace, "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-At", "-c", query)
	if err != nil {
		return err
	}
	var assertions map[string]bool
	if json.Unmarshal(persisted, &assertions) != nil || len(assertions) != 3 {
		return errors.New("missing peer trust persistence checks")
	}
	for name, passed := range assertions {
		if !passed {
			return fmt.Errorf("peer trust SQL failed: %s", name)
		}
	}
	h.report.PeerTrust = map[string]any{"configuration": loaded, "persistence": assertions, "result": json.RawMessage(raw)}
	h.report.Checks = append(h.report.Checks, "peer-enabled-verifier-explicit-only", "peer-private-assessment-owner-sql")
	return nil
}

func peerTrustPersistenceQuery(sourceID, jobID string) (string, error) {
	if !agntcyUUID.MatchString(sourceID) || jobID != "" && !agntcyUUID.MatchString(jobID) {
		return "", errors.New("invalid peer trust SQL binding")
	}
	if jobID == "" {
		return fmt.Sprintf(`SELECT json_build_object('no_trust_jobs',NOT EXISTS(SELECT 1 FROM jobs WHERE kind='source.trust_verify'),'source_exists',EXISTS(SELECT 1 FROM federation_sources WHERE tenant_id='%s' AND id='%s'),'empty_observations',NOT EXISTS(SELECT 1 FROM observations WHERE tenant_id='%s' AND source_id='%s'));`, primaryTenantID, sourceID, primaryTenantID, sourceID), nil
	}
	return fmt.Sprintf(`SELECT json_build_object(
 'one_explicit_job',(SELECT count(*) FROM jobs WHERE kind='source.trust_verify')=1,
 'exact_private_binding',EXISTS(SELECT 1 FROM jobs j WHERE j.tenant_id='%[1]s' AND j.id='%[2]s' AND j.kind='source.trust_verify' AND j.state='succeeded' AND j.attempt=1 AND j.payload->>'sourceId'='%[3]s' AND j.result->>'sourceId'='%[3]s' AND j.payload->>'exactEntryDigest'=j.result->>'exactEntryDigest' AND j.payload->>'policyDigest'=j.result->>'policyDigest' AND j.result->'assessment'->>'currentRegistration'='false'),
 'completion_and_read_audit',EXISTS(SELECT 1 FROM audit_events a WHERE a.tenant_id='%[1]s' AND a.target_id='%[2]s' AND a.action='source.trust.completed') AND EXISTS(SELECT 1 FROM audit_events a WHERE a.tenant_id='%[1]s' AND a.target_id='%[2]s' AND a.action='source.trust.read'));`, primaryTenantID, jobID, sourceID), nil
}

// Existing native access logs include rejected paths. A real manifest request
// calibrates capture, so disabled or truncated logging cannot claim zero calls.
func noPeerEvidenceRequests(logs string) error {
	if !strings.Contains(logs, "GET /.well-known/ai-catalog.json ") {
		return errors.New("missing native request-log control")
	}
	for _, route := range []string{" /checkpoint ", "/receipt ", "/status-token ", "/v1/log/checkpoint/history"} {
		if strings.Contains(logs, route) {
			return errors.New("unexpected peer evidence request")
		}
	}
	return nil
}
