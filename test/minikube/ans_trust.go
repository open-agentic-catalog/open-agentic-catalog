//go:build minikube

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

var ansTrustPhases = []string{"disabled", "unselected", "selected", "unsupported", "corruption", "history", "history-missing", "history-corrupt", "restart", "cancel", "cancel-running", "policy-change", "tl-outage", "tl-recovery", "finder-independent", "overlap", "key-removal", "restored", "expiry", "revocation", "ra-signer", "ra-key-removal", "ra-restored", "tl-signer", "tl-key-removal", "signers-restored", "disabled-again"}

func (h *harness) qualifyANSTrust(settings map[string]any) error {
	output, ok := settings["output"].(string)
	if !ok {
		return errors.New("missing owned ANS output")
	}
	directory := filepath.Join(output, "trust")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	keys := map[string]string{}
	for component, id := range map[string]string{"ra": "ans-ra-signer", "tl": "ans-tl-attestation"} {
		raw, err := h.kubectl("export public ANS assessment key", "-n", ansNamespace, "exec", "deployment/"+component, "-c", "public-key", "--", "/federation-fixture", "ans-public-key", id)
		if err != nil {
			return err
		}
		keys[component+".pem"] = string(raw)
		if err := os.WriteFile(filepath.Join(directory, component+".pem"), raw, 0o600); err != nil {
			return err
		}
	}
	fake, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKIXPublicKey(&fake.PublicKey)
	if err != nil {
		return err
	}
	keys["other.pem"] = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err := os.WriteFile(filepath.Join(directory, "other.pem"), []byte(keys["other.pem"]), 0o600); err != nil {
		return err
	}
	if err := h.applyObject("mount exact public ANS authority keys", kubernetesObject{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "ans-public-keys", "namespace": namespace}, "data": keys}); err != nil {
		return err
	}
	settings["trust_output"], settings["trust_state"] = directory, filepath.Join(h.temporary, "ans-trust-state.json")
	h.ans.publicKeys = keys
	settings["tl_key"] = h.ans.secrets[1]
	settings["lifecycle_state"] = filepath.Join(h.temporary, "ans-lifecycle-state.json")
	// Copy the actually built command from this invocation's image; no second build.
	container := "ans-command-" + h.suffix
	if _, err := h.run("create owned command export", h.options.docker, "create", "--name", container, h.application); err != nil {
		return err
	}
	_, copyErr := h.run("export current Catalog command", h.options.docker, "cp", container+":/usr/local/bin/catalogctl", filepath.Join(h.temporary, "catalogctl"))
	_, removeErr := h.runCleanupCommand(30*time.Second, h.options.docker, "rm", container)
	if err := errors.Join(copyErr, removeErr); err != nil {
		return err
	}
	settings["catalogctl"] = filepath.Join(h.temporary, "catalogctl")
	h.ans.report.Trust = map[string]any{}
	for _, phase := range ansTrustPhases {
		switch phase {
		case "ra-signer", "ra-key-removal", "ra-restored", "tl-signer", "tl-key-removal", "signers-restored":
			if phase == "ra-signer" || phase == "tl-signer" {
				component, id, file := "ra", "ans-ra-signer-next", "ra-next.pem"
				if phase == "tl-signer" {
					component, id, file = "tl", "ans-tl-attestation-next", "tl-next.pem"
				}
				if err := h.rotateANSSigner(settings, component, id, file); err != nil {
					return err
				}
			}
			h.ans.trustConfig = ansSignerTrustConfiguration(phase)
			if err := h.rollANSTrust(settings, phase); err != nil {
				return err
			}
		case "unselected", "overlap", "key-removal", "restored", "expiry", "disabled-again":
			enabled, logs, age := phase != "disabled-again", []string{"tl.pem"}, "1h"
			if phase == "overlap" {
				logs = append(logs, "other.pem")
			}
			if phase == "key-removal" {
				logs = []string{"other.pem"}
			}
			if phase == "expiry" {
				age = "5s"
			}
			h.ans.trustConfig = ansTrustConfiguration(enabled, logs, age)
			if err := h.rollANSTrust(settings, phase); err != nil {
				return err
			}
		case "restart", "cancel", "finder-independent", "revocation":
			if err := h.scaleTrustWorker(0); err != nil {
				return err
			}
		case "tl-outage":
			if err := h.scaleANS("tl", 0); err != nil {
				return err
			}
		case "tl-recovery":
			if err := h.scaleANS("tl", 1); err != nil {
				return err
			}
			address, err := h.startPortForwardIn(ansNamespace, "service/tl", 8080)
			if err != nil {
				return err
			}
			settings["tl"] = "http://" + address
		}
		if err := h.runANSTrustPhase(settings, phase); err != nil {
			return err
		}
		switch phase {
		case "restart":
			if err := h.scaleTrustWorker(1); err != nil {
				return err
			}
			if err := h.runANSTrustPhase(settings, "restart-complete"); err != nil {
				return err
			}
		case "revocation":
			if err := h.scaleTrustWorker(1); err != nil {
				return err
			}
			if err := h.runANSTrustPhase(settings, "revocation-complete"); err != nil {
				return err
			}
		case "cancel":
			if err := h.scaleTrustWorker(1); err != nil {
				return err
			}
		case "finder-independent":
			if err := h.scaleANS("finder", 0); err != nil {
				return err
			}
			if err := h.scaleTrustWorker(1); err != nil {
				return err
			}
			if err := h.runANSTrustPhase(settings, "finder-complete"); err != nil {
				return err
			}
			if err := h.scaleANS("finder", 1); err != nil {
				return err
			}
			address, err := h.startPortForwardIn(ansNamespace, "service/finder", 8080)
			if err != nil {
				return err
			}
			settings["finder"] = "http://" + address
		}
	}
	h.report.Checks = append(h.report.Checks, "ans-real-v1-v2-private-assessments", "ans-explicit-only-zero-discovery-evidence", "ans-negative-proof-and-status-controls", "ans-native-checkpoint-advancement-and-history", "ans-native-ra-and-tl-signer-changes", "ans-key-overlap-removal-and-expiry", "ans-policy-cancel-restart-and-outage-recovery", "ans-offline-current-command-controls", "ans-private-postgres-audit-bindings")
	return nil
}

func ansTrustConfiguration(enabled bool, logs []string, age string) map[string]any {
	files := make([]string, 0, len(logs))
	for _, name := range logs {
		files = append(files, "/etc/open-agentic-catalog/trust-verification/"+name)
	}
	return map[string]any{"enabled": enabled, "maxStatusAge": age, "policies": []any{map[string]any{"name": "owned-ans", "profile": "ans-registration-v1", "registrationAuthority": "ans-ra-local", "logOrigin": "owned-ans", "evidenceOrigin": ansOrigin, "networkProfile": "functional-fixture", "publisherDomains": []string{"alpha.ans.test", "beta.ans.test", "gamma.ans.test", "shared.ans.test", "trust-v2.ans.test", "ra-next.ans.test", "tl-next.ans.test"}, "registrationKeys": []any{map[string]string{"id": "ans-ra-signer", "file": "/etc/open-agentic-catalog/trust-verification/ra.pem"}}, "logKeyFiles": files}}}
}

// Derive offline authority from the same loaded policy, including freshness.
func ansOfflinePolicy(input map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	cfg := config.TrustVerificationDefaults()
	if json.Unmarshal(raw, &cfg) != nil || len(cfg.Policies) != 1 {
		return nil, errors.New("missing single fixture authority")
	}
	p := cfg.Policies[0]
	keys := make([]map[string]string, 0, len(p.RegistrationKeys))
	for _, key := range p.RegistrationKeys {
		keys = append(keys, map[string]string{"id": key.ID, "file": filepath.Base(key.File)})
	}
	logs := make([]string, 0, len(p.LogKeyFiles))
	for _, file := range p.LogKeyFiles {
		logs = append(logs, filepath.Base(file))
	}
	return map[string]any{"profile": p.Profile, "registrationAuthority": p.RegistrationAuthority, "logOrigin": p.LogOrigin, "evidenceOrigin": p.EvidenceOrigin, "publisherDomains": p.PublisherDomains, "registrationKeys": keys, "logKeyFiles": logs, "maxStatusAgeSeconds": int(cfg.MaxStatusAge.Seconds()), "futureSkewSeconds": int(cfg.FutureSkew.Seconds())}, nil
}

func (h *harness) scaleTrustWorker(replicas int) error {
	if _, err := h.kubectl("set owned assessment worker count", "-n", namespace, "scale", "deployment/catalog-worker", fmt.Sprintf("--replicas=%d", replicas)); err != nil {
		return err
	}
	if replicas == 0 {
		_, err := h.kubectl("wait for assessment worker drain", "-n", namespace, "wait", "--for=delete", "pods", "-l", "app.kubernetes.io/component=worker", "--timeout=2m")
		return err
	}
	_, err := h.kubectl("wait for assessment worker", "-n", namespace, "rollout", "status", "deployment/catalog-worker", "--timeout=3m")
	return err
}

// Drain before every owner hook, including configuration-only transitions.
func (h *harness) rollANSTrust(settings map[string]any, phase string) error {
	for _, role := range []string{"api", "worker"} {
		if _, err := h.kubectl("drain owned trust "+role, "-n", namespace, "scale", "deployment/catalog-"+role, "--replicas=0"); err != nil {
			return err
		}
		if _, err := h.kubectl("wait for owned trust drain", "-n", namespace, "wait", "--for=delete", "pods", "-l", "app.kubernetes.io/component="+role, "--timeout=2m"); err != nil {
			return err
		}
	}
	values, err := h.writeHelmValues("trust-"+phase, "catalog_api")
	if err != nil {
		return err
	}
	if _, err = h.helm("apply matching API/worker trust policy", "upgrade", releaseName, h.options.repositoryRoot+"/deployments/helm/open-agentic-catalog", "--namespace", namespace, "--values", values, "--wait", "--wait-for-jobs", "--timeout=4m"); err != nil {
		return err
	}
	address, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	settings["catalog"] = "http://" + address
	address, ok := settings["catalog"].(string)
	if !ok {
		return errors.New("missing restarted Catalog address")
	}
	if err := h.waitHTTP(address + "/readyz"); err != nil {
		return err
	}
	raw, err := os.ReadFile(values) // #nosec G304 -- invocation-owned generated Helm values.
	if err != nil {
		return err
	}
	if h.ans.report.TrustConfigurations == nil {
		h.ans.report.TrustConfigurations = map[string]string{}
	}
	h.ans.report.TrustConfigurations[phase] = fmt.Sprintf("%x", sha256.Sum256([]byte(h.redactDiagnostic(string(raw)))))
	return h.verifyInstalledWorkloads()
}

func (h *harness) runANSTrustPhase(settings map[string]any, phase string) error {
	if err := h.interopProgress("trust-"+phase, nil, nil); err != nil {
		return err
	}
	if h.ans.trustConfig != nil {
		policy, err := ansOfflinePolicy(h.ans.trustConfig)
		if err != nil {
			return err
		}
		settings["offline_policy"] = policy
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	path := filepath.Join(h.temporary, "ans-trust.json")
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	output, ok := settings["trust_output"].(string)
	if !ok {
		return errors.New("missing owned trust output")
	}
	reportPath := filepath.Join(output, phase+".json")
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Minute)
	_, runErr := h.runWithInput(ctx, "qualify native ANS trust "+phase, nil, h.ansPython(), "-B", "test/interoperability/ans/trust.py", phase, path, reportPath)
	cancel()
	raw, readErr := os.ReadFile(reportPath) // #nosec G304 -- invocation-owned exact report path.
	var report map[string]any
	if readErr == nil {
		readErr = json.Unmarshal(raw, &report)
		h.ans.report.Trust[phase] = report
	}
	if runErr != nil {
		return runErr
	}
	if readErr != nil {
		return readErr
	}
	if err := validateANSTrustPhase(raw, phase); err != nil {
		return err
	}
	if phase == "tl-signer" || phase == "signers-restored" {
		proofs, err := verifyANSSignerPhase(raw, []byte(h.ans.publicKeys["tl-next.pem"]))
		if err != nil {
			return err
		}
		report["nativeSignatures"] = proofs
	}
	query, err := ansTrustPersistenceQuery(raw)
	if err != nil {
		return err
	}
	persisted, err := h.kubectl("verify private assessment PostgreSQL evidence", "-n", namespace, "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-At", "-c", query)
	if err != nil {
		return err
	}
	var evidence map[string]any
	if json.Unmarshal(persisted, &evidence) != nil {
		return errors.New("missing trust SQL result")
	}
	for key, value := range evidence {
		if value != true {
			return fmt.Errorf("trust PostgreSQL %s failed", key)
		}
	}
	if len(evidence) != 7 {
		return errors.New("incomplete trust SQL checks")
	}
	report["postgres"] = evidence
	return nil
}

var ansTrustRequiredChecks = map[string]string{
	"disabled":        "discovery_and_refusal_have_zero_evidence_calls",
	"unselected":      "discovery_and_refusal_have_zero_evidence_calls",
	"disabled-again":  "discovery_and_refusal_have_zero_evidence_calls disabled_policy_preserves_private_history",
	"selected":        "real_v1 real_v2 explicit_only bounded_requests replay_no_fetch private_tenant_authority actual_command_offline wrong_key_and_transplant",
	"unsupported":     "unsupported_framework_zero_evidence",
	"corruption":      "receipt-signature receipt-root checkpoint missing-status gone-status offline_tamper",
	"history":         "native_log_advanced exact_history_request native_signed_evidence offline_agrees single_attempt",
	"history-missing": "native_log_advanced exact_history_request native_signed_evidence offline_agrees single_attempt",
	"history-corrupt": "native_log_advanced exact_history_request native_signed_evidence offline_agrees single_attempt",
	"restart":         "queued_without_evidence", "cancel": "queued_without_evidence", "finder-independent": "queued_without_evidence",
	"restart-complete": "durable_job_and_exact_history", "finder-complete": "durable_job_and_exact_history",
	"cancel-running": "inflight_cancel_stops_followup_fetch_and_completion",
	"policy-change":  "source_change_stops_followup_fetch_and_completion",
	"tl-outage":      "evidence_outage_does_not_break_discovery", "tl-recovery": "same_job_recovered",
	"overlap": "loaded_key_policy_and_historical_evidence", "key-removal": "loaded_key_policy_and_historical_evidence",
	"restored": "loaded_key_policy_and_historical_evidence", "expiry": "loaded_key_policy_and_historical_evidence",
	"revocation": "native_revocation_and_historical_preservation", "revocation-complete": "native_terminal_registration_not_current",
	"ra-signer":        "native_new_signer old_and_new_registrations exact_fingerprints offline_agrees historical_bytes single_attempt",
	"ra-key-removal":   "old_key_rejected new_registration_current offline_agrees historical_bytes single_attempt",
	"ra-restored":      "old_and_new_registrations exact_fingerprints offline_agrees historical_bytes single_attempt",
	"tl-signer":        "native_new_signer old_and_new_registrations exact_fingerprints offline_agrees historical_bytes single_attempt",
	"tl-key-removal":   "old_key_rejected new_registration_current offline_agrees historical_bytes single_attempt",
	"signers-restored": "old_and_new_registrations exact_fingerprints offline_agrees historical_bytes single_attempt",
}

func validateANSTrustPhase(raw []byte, phase string) error {
	var r struct {
		Phase    string          `json:"phase"`
		Status   string          `json:"status"`
		Error    string          `json:"error"`
		Checks   map[string]bool `json:"checks"`
		Captures []any           `json:"captures"`
		History  map[string]struct {
			OldSize uint64 `json:"oldSize"`
			NewSize uint64 `json:"newSize"`
			OldRoot string `json:"oldRoot"`
			NewRoot string `json:"newRoot"`
			Path    string `json:"historyPath"`
			Digest  string `json:"originalHistorySHA256"`
			JobID   string `json:"jobId"`
		} `json:"checkpoint_history"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Phase != phase || r.Status != "pass" || r.Error != "" || len(r.Checks) == 0 || len(r.Captures) == 0 {
		return errors.New("incomplete ANS assessment phase")
	}
	expected, ok := ansTrustRequiredChecks[phase]
	if !ok || len(r.Checks) != len(strings.Fields(expected)) {
		return errors.New("incorrect ANS assessment check inventory")
	}
	for _, name := range strings.Fields(expected) {
		if !r.Checks[name] {
			return fmt.Errorf("missing ANS assessment check %s", name)
		}
	}
	for name, ok := range r.Checks {
		if !ok {
			return fmt.Errorf("failed ANS assessment check %s", name)
		}
	}
	if phase == "history" || strings.HasPrefix(phase, "history-") {
		history, ok := r.History[phase]
		oldRoot, oldErr := base64.StdEncoding.Strict().DecodeString(history.OldRoot)
		newRoot, newErr := base64.StdEncoding.Strict().DecodeString(history.NewRoot)
		if !ok || history.OldSize == 0 || history.NewSize <= history.OldSize || oldErr != nil || newErr != nil || len(oldRoot) != 32 || len(newRoot) != 32 || history.OldRoot == history.NewRoot ||
			history.Path != fmt.Sprintf("/v1/log/checkpoint/history?fromSize=%d&toSize=%d&limit=1", history.OldSize, history.OldSize) || !agntcyDigest.MatchString("sha256:"+history.Digest) || !agntcyUUID.MatchString(history.JobID) {
			return errors.New("incomplete native checkpoint advancement evidence")
		}
	}
	return nil
}

func ansTrustPersistenceQuery(raw []byte) (string, error) {
	var r struct {
		SourceID string   `json:"source_id"`
		Jobs     []string `json:"jobs"`
	}
	if json.Unmarshal(raw, &r) != nil || !agntcyUUID.MatchString(r.SourceID) {
		return "", errors.New("invalid trust source SQL binding")
	}
	ids := map[string]bool{}
	for _, id := range r.Jobs {
		if !agntcyUUID.MatchString(id) {
			return "", errors.New("invalid trust Job SQL binding")
		}
		ids[id] = true
	}
	return fmt.Sprintf(`SELECT json_build_object(
 'exact_job_count',(SELECT count(*) FROM jobs j WHERE j.tenant_id='%[1]s' AND j.kind='source.trust_verify' AND j.payload->>'sourceId'='%[2]s')=%[3]d,
 'source_exists',EXISTS(SELECT 1 FROM federation_sources s WHERE s.tenant_id='%[1]s' AND s.id='%[2]s'),
 'no_snapshots',NOT EXISTS(SELECT 1 FROM source_snapshots s WHERE s.tenant_id='%[1]s' AND s.source_id='%[2]s'),
 'no_observations',NOT EXISTS(SELECT 1 FROM observations o WHERE o.tenant_id='%[1]s' AND o.source_id='%[2]s'),
 'target_binding',NOT EXISTS(SELECT 1 FROM jobs j WHERE j.tenant_id='%[1]s' AND j.kind='source.trust_verify' AND j.payload->>'sourceId'='%[2]s' AND (j.payload->>'exactEntryDigest' NOT LIKE 'sha256:%%' OR j.payload->>'policyDigest' NOT LIKE 'sha256:%%')),
 'completion_audit',NOT EXISTS(SELECT 1 FROM jobs j WHERE j.tenant_id='%[1]s' AND j.kind='source.trust_verify' AND j.payload->>'sourceId'='%[2]s' AND j.state='succeeded' AND NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.tenant_id=j.tenant_id AND a.target_id=j.id::text AND a.action='source.trust.completed' AND a.details->'facts'->>'entryDigest'=j.payload->>'exactEntryDigest' AND a.details->'facts'->>'policyDigest'=j.payload->>'policyDigest' AND a.details->'facts'->>'sourceId'=j.payload->>'sourceId' AND a.details->'facts'->>'jobId'=j.id::text)),
 'private_result',NOT EXISTS(SELECT 1 FROM jobs j WHERE j.tenant_id='%[1]s' AND j.kind='source.trust_verify' AND j.payload->>'sourceId'='%[2]s' AND j.state='succeeded' AND (j.result->>'softwareTrust' IS DISTINCT FROM 'not_assessed' OR j.result->>'rights' IS DISTINCT FROM 'not_assessed')));`, primaryTenantID, r.SourceID, len(ids)), nil
}
