//go:build minikube

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// qualifyANSCatalog uses normal product APIs; the supervisor owns only fixture faults and owner SQL reads.
func (h *harness) qualifyANSCatalog(baseURL string, settings map[string]any) error {
	h.ans.report.Lifecycle, h.ans.report.Persistence = map[string]any{}, map[string]any{}
	settings["catalog"], settings["token"], settings["reader"], settings["other"] = baseURL, h.tokens["admin"], h.tokens["reader"], h.tokens["other"]
	settingsPath, statePath := filepath.Join(h.temporary, "ans-lifecycle.json"), filepath.Join(h.temporary, "ans-lifecycle-state.json")
	for _, phase := range []string{"bootstrap", "discovery", "shared", "revocation", "feed-outage", "feed-recovery", "finder-outage", "finder-recovery"} {
		if err := h.interopProgress("catalog-"+phase, nil, nil); err != nil {
			return err
		}
		switch phase {
		case "feed-outage", "finder-outage", "feed-recovery", "finder-recovery":
			component, replicas := "ra", 0
			if phase == "finder-outage" || phase == "finder-recovery" {
				component = "finder"
			}
			if phase == "feed-recovery" || phase == "finder-recovery" {
				replicas = 1
			}
			if err := h.scaleANS(component, replicas); err != nil {
				return err
			}
			if replicas == 1 {
				address, err := h.startPortForwardIn(ansNamespace, "service/"+component, 8080)
				if err != nil {
					return err
				}
				settings[component] = "http://" + address
			}
		}
		raw, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		if err := os.WriteFile(settingsPath, raw, 0600); err != nil {
			return err
		}
		reportPath := filepath.Join(h.options.repositoryRoot, "output/interoperability/minikube-ans-"+h.suffix, "catalog-"+phase+".json")
		ctx, cancel := context.WithTimeout(h.ctx, 3*time.Minute)
		_, runErr := h.runWithInput(ctx, "qualify ANS Catalog "+phase, nil, h.ansPython(), "-B", "test/interoperability/ans/lifecycle.py", phase, settingsPath, statePath, reportPath)
		cancel()
		raw, readErr := os.ReadFile(reportPath) // #nosec G304 -- exact invocation-owned report path.
		var report map[string]any
		if readErr == nil {
			readErr = json.Unmarshal(raw, &report)
			h.ans.report.Lifecycle[phase] = report
		}
		if runErr != nil {
			return runErr
		}
		if readErr != nil {
			return readErr
		}
		if err := validateANSPhase(raw, phase); err != nil {
			return err
		}
		query, err := ansPersistenceQuery(raw)
		if err != nil {
			return err
		}
		persisted, err := h.kubectl("verify ANS PostgreSQL "+phase, "-n", namespace, "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-At", "-c", query)
		if err != nil {
			return err
		}
		var evidence map[string]any
		if err := json.Unmarshal(persisted, &evidence); err != nil {
			return err
		}
		h.ans.report.Persistence[phase] = evidence
		if err := ansPersistenceMatches(evidence); err != nil {
			return fmt.Errorf("%s: %w", phase, err)
		}
	}
	h.report.Checks = append(h.report.Checks, "ans-catalog-live-source-trust-and-audit", "ans-exact-three-page-live-discovery", "ans-authenticated-control-and-ard", "ans-shared-native-identity-merge", "ans-native-revocation-and-cursor-restart", "ans-stale-feed-safe-admission-and-recovery", "ans-finder-outage-same-cursor-recovery", "ans-postgres-live-only-persistence")
	return nil
}

func (h *harness) scaleANS(component string, replicas int) error {
	if (component != "ra" && component != "finder" && component != "tl") || (replicas != 0 && replicas != 1) {
		return errors.New("invalid owned ANS fault")
	}
	if _, err := h.kubectl("set owned ANS outage", "-n", ansNamespace, "scale", "deployment/"+component, fmt.Sprintf("--replicas=%d", replicas)); err != nil {
		return err
	}
	if replicas == 0 {
		_, err := h.kubectl("wait for owned ANS to stop", "-n", ansNamespace, "wait", "--for=delete", "pods", "-l", "component="+component, "--timeout=1m")
		return err
	}
	_, err := h.kubectl("wait for retained ANS state recovery", "-n", ansNamespace, "rollout", "status", "deployment/"+component, "--timeout=3m")
	return err
}

func validateANSPhase(raw []byte, phase string) error {
	var report struct {
		Phase    string         `json:"phase"`
		Status   string         `json:"status"`
		Error    string         `json:"error"`
		Evidence map[string]any `json:"evidence"`
		Captures []any          `json:"captures"`
		Native   []any          `json:"native_captures"`
	}
	if json.Unmarshal(raw, &report) != nil || report.Phase != phase || report.Status != "pass" || report.Error != "" || len(report.Evidence) == 0 || len(report.Captures) == 0 || (phase != "bootstrap" && len(report.Native) == 0) {
		return errors.New("missing or contradictory ANS Catalog phase evidence")
	}
	return nil
}

// ansPersistenceQuery interpolates only validated UUIDs, with explicit tenant predicates on every retained relation.
func ansPersistenceQuery(raw []byte) (string, error) {
	var report struct {
		State struct {
			Source     string `json:"source_id"`
			Inspection string `json:"inspection_id"`
		} `json:"state"`
	}
	if json.Unmarshal(raw, &report) != nil || !agntcyUUID.MatchString(report.State.Source) || !agntcyUUID.MatchString(report.State.Inspection) {
		return "", errors.New("invalid ANS persistence IDs")
	}
	return fmt.Sprintf(`SELECT json_build_object(
 'source_bound',s.status='active' AND s.source_type='ard_registry' AND s.endpoint='%s/v1/search' AND s.last_complete_snapshot_id IS NULL,
 'inspection_bound',i.document->>'resolvedUrl'=s.endpoint AND i.document->>'profileId'='ard-0.91-aa3e598' AND i.document->>'registrySearchUrl'=s.endpoint,
 'approval_bound',r.retrieval_mode='live' AND r.policy_digest=s.configuration->>'policyDigest' AND r.configuration=s.configuration,
 'audience_bound',EXISTS(SELECT 1 FROM federation_source_audiences a WHERE a.tenant_id=s.tenant_id AND a.source_id=s.id AND a.source_revision=s.revision AND a.discovery_host='localhost' AND a.audience='authenticated'),
 'inspection_audit',EXISTS(SELECT 1 FROM audit_events a WHERE a.tenant_id=s.tenant_id AND a.target_id=i.id::text AND a.action='source.inspected'),
 'connection_audit',EXISTS(SELECT 1 FROM audit_events a WHERE a.tenant_id=s.tenant_id AND a.target_id=s.id::text AND a.action='source.connected' AND a.details#>>'{facts,inspectionId}'=i.id::text),
 'no_snapshots',NOT EXISTS(SELECT 1 FROM source_snapshots x WHERE x.tenant_id=s.tenant_id AND x.source_id=s.id),
 'no_observations',NOT EXISTS(SELECT 1 FROM observations o WHERE o.tenant_id=s.tenant_id AND o.source_id=s.id),
 'no_sync_jobs',NOT EXISTS(SELECT 1 FROM jobs j WHERE j.tenant_id=s.tenant_id AND j.kind='source.sync' AND j.payload->>'sourceId'=s.id::text))
 FROM federation_sources s JOIN source_inspections i ON i.tenant_id=s.tenant_id AND i.id='%s'
 JOIN federation_source_revisions r ON r.tenant_id=s.tenant_id AND r.source_id=s.id AND r.revision=s.revision
 WHERE s.tenant_id='%s' AND s.id='%s';`, ansOrigin, report.State.Inspection, primaryTenantID, report.State.Source), nil
}

func ansPersistenceMatches(evidence map[string]any) error {
	for _, key := range []string{"source_bound", "inspection_bound", "approval_bound", "audience_bound", "inspection_audit", "connection_audit", "no_snapshots", "no_observations", "no_sync_jobs"} {
		if evidence[key] != true {
			return fmt.Errorf("ANS PostgreSQL %s did not hold", key)
		}
	}
	return nil
}
