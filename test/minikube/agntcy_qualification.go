//go:build minikube

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

// qualifyAGNTCY passes private invocation inputs to the shared four-group assertions.
// Only the supervisor owns cluster mutation, port-forwards and cleanup.
func (h *harness) qualifyAGNTCY(baseURL string) error {
	native, err := h.startPortForwardIn(agntcyNamespace, "service/directory-apiserver", 8889)
	if err != nil {
		return err
	}
	grpc, err := h.startPortForwardIn(agntcyNamespace, "service/directory-apiserver", 8888)
	if err != nil {
		return err
	}
	gateway, err := h.startPortForwardIn(agntcyNamespace, "service/directory-gateway", 8443)
	if err != nil {
		return err
	}
	certificate := filepath.Join(h.temporary, "directory-ca.pem")
	if err := os.WriteFile(certificate, []byte(h.agntcy.tls["ca.pem"]), 0600); err != nil {
		return err
	}
	settings := map[string]string{"origin": agntcyOrigin, "catalog": baseURL, "native": "http://" + native, "peer": "https://" + gateway, "grpc": grpc, "token": h.tokens["admin"], "certificate": certificate, "private": h.temporary, "trust": fmt.Sprint(h.options.interopTrust)}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	path := filepath.Join(h.temporary, "directory-qualification.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return err
	}
	output := filepath.Join(h.options.repositoryRoot, "output/interoperability/minikube-agntcy-"+h.suffix)
	if err := h.interopProgress("qualification", nil, nil); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Minute)
	runErr := h.runAGNTCYQualification(ctx, path, output)
	cancel()
	raw, readErr := os.ReadFile(filepath.Join(output, "qualification.json")) // #nosec G304 -- exact report under the invocation-owned evidence directory.
	if readErr == nil {
		readErr = json.Unmarshal(raw, &h.agntcy.report.Qualification)
	}
	if runErr != nil {
		return runErr
	}
	if readErr != nil {
		return readErr
	}
	if h.agntcy.report.Qualification["status"] != "pass" {
		return errors.New("directory qualification report did not pass")
	}
	peer, ok := h.agntcy.report.Qualification["peer"].(map[string]any)
	if !ok || peer["release"] != h.agntcy.report.Inputs.Release || peer["revision"] != h.agntcy.report.Inputs.Revision {
		return errors.New("directory qualification used different release inputs")
	}
	if err := agntcyNativeImports(h.agntcy.report.Qualification); err != nil {
		return err
	}
	if err := agntcyProjectedPayloads(h.agntcy.report.Qualification); err != nil {
		return err
	}
	discovery, err := agntcyDiscovery(h.agntcy.report.Qualification)
	if err != nil {
		return err
	}
	query, err := agntcyPersistenceQuery(discovery)
	if err != nil {
		return err
	}
	raw, err = h.kubectl("verify retained Directory source Job and snapshot", "-n", namespace, "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-At", "-c", query)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &h.agntcy.report.Persistence); err != nil {
		return err
	}
	if err := agntcyPersistenceMatches(h.agntcy.report.Persistence, discovery); err != nil {
		return err
	}
	sourceID, ok := discovery["source_id"].(string)
	if !ok {
		return errors.New("missing Directory source identity")
	}
	if err := h.qualifyPeerTrust(baseURL, certificate, sourceID, true); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "directory-six-exact-native-records", "directory-five-projections-and-complete-pagination", "directory-native-filter-grammar", "directory-four-kind-dispositions", "directory-four-mvp-groups", "directory-official-conformance-and-five-raw-entry-schemas", "directory-postgres-source-job-snapshot-binding")
	h.report.Checks = append(h.report.Checks, "directory-native-a2a-mcp-skill-imports", "directory-import-repeat-malformed-and-inventory-controls", "directory-nonempty-federation-eligibility")
	h.report.Checks = append(h.report.Checks, "directory-projected-a2a-and-mcp-payload-refusals")
	return h.interopProgress("qualified", nil, nil)
}

func agntcyProjectedPayloads(report map[string]any) error {
	kinds, ok := report["kinds"].(map[string]any)
	if !ok {
		return errors.New("missing Directory payload validation")
	}
	for _, expected := range []struct {
		kind, field string
		codes       []any
	}{
		{"agent", "rejected_projection", []any{"A2A_PROTOJSON_INVALID"}},
		{"mcp_server", "evidence", []any{"MCP_SERVER_CARD_SCHEMA_INVALID", "MCP_SERVER_CARD_VERSION_REQUIRED", "MCP_SERVER_CARD_EXPERIMENTAL"}},
	} {
		kind, ok := kinds[expected.kind].(map[string]any)
		if !ok {
			return errors.New("missing Directory resource-kind evidence")
		}
		value, ok := kind[expected.field].(map[string]any)
		if !ok || value["state"] != "blocked" {
			return errors.New("directory projected payload was not blocked")
		}
		id, ok := value["revision_id"].(string)
		if !ok || !agntcyUUID.MatchString(id) {
			return errors.New("missing retained Directory payload identity")
		}
		codes, ok := value["codes"].([]any)
		if !ok || len(codes) != len(expected.codes) {
			return errors.New("unexpected Directory payload validation failure")
		}
		for index, code := range codes {
			if code != expected.codes[index] {
				return errors.New("unexpected Directory payload validation code")
			}
		}
	}
	return nil
}

// Require the supplemental qualification, independently of the retained four-group MVP.
func agntcyNativeImports(report map[string]any) error {
	value, ok := report["native_imports"].(map[string]any)
	if !ok || value["status"] != "pass" || value["stored_after"] != float64(6) {
		return errors.New("missing or failed native import qualification")
	}
	rows, ok := value["cases"].([]any)
	if !ok || len(rows) != 3 {
		return errors.New("missing three native import cases")
	}
	wanted := map[string]bool{"a2a": true, "mcp": true, "agent-skill": true}
	for _, row := range rows {
		item, ok := row.(map[string]any)
		if !ok {
			return errors.New("invalid native import case")
		}
		kind, _ := item["kind"].(string)
		if !wanted[kind] || item["status"] != "pass" {
			return errors.New("unpassed or duplicate native import case")
		}
		delete(wanted, kind)
	}
	federation, ok := value["federation"].(map[string]any)
	if !ok || federation["disposition"] != "unsupported_nonempty" || federation["manifest_entries"] != float64(0) {
		return errors.New("missing nonempty Directory federation disposition")
	}
	for _, stage := range []string{"transfer", "native_change", "withdrawal", "outage_recovery"} {
		if federation[stage] != "unrun" {
			return errors.New("unsupported Directory transfer has a contradictory lifecycle result")
		}
	}
	return nil
}

func agntcyDiscovery(report map[string]any) (map[string]any, error) {
	cases, ok := report["cases"].([]any)
	if !ok || len(cases) != 4 {
		return nil, errors.New("missing four Directory qualification groups")
	}
	seen := make(map[string]bool)
	var discovery map[string]any
	for _, item := range cases {
		value, ok := item.(map[string]any)
		if !ok || value["status"] != "pass" {
			return nil, errors.New("unpassed Directory qualification group")
		}
		name, ok := value["name"].(string)
		if !ok || seen[name] || (name != "artifact" && name != "discovery" && name != "search" && name != "conformance") {
			return nil, errors.New("invalid Directory group inventory")
		}
		seen[name] = true
		if name == "discovery" {
			discovery, _ = value["evidence"].(map[string]any)
		}
	}
	if discovery == nil {
		return nil, errors.New("missing discovery evidence")
	}
	return discovery, nil
}

var agntcyUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var agntcyDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// agntcyPersistenceQuery admits UUID/digest values only before interpolation into owner read-only SQL.
func agntcyPersistenceQuery(discovery map[string]any) (string, error) {
	ids := make([]string, 0, 4)
	for _, key := range []string{"source_id", "job_id", "snapshot_id", "inspection_id"} {
		id, ok := discovery[key].(string)
		if !ok || !agntcyUUID.MatchString(id) {
			return "", errors.New("invalid persisted Directory evidence identity")
		}
		ids = append(ids, id)
	}
	digest, ok := discovery["manifest_sha256"].(string)
	if !ok || !agntcyDigest.MatchString(digest) {
		return "", errors.New("invalid Directory manifest digest")
	}
	// An empty snapshot is meaningful only when its source, job and exact retrieved bytes agree.
	return fmt.Sprintf(`SELECT json_build_object(
 'source_id',source.id,'job_id',job.id,'snapshot_id',snapshot.id,'inspection_id',inspection.id,
 'source_active',source.status='active','source_type',source.source_type,
 'endpoint',source.endpoint,'source_revision',source.revision,
 'job_succeeded',job.state='succeeded' AND job.kind='source.sync' AND job.attempt>0 AND job.progress=100,
 'job_bound',job.payload->>'sourceId'=source.id::text AND (job.payload->>'sourceRevision')::bigint=source.revision AND job.result->>'id'=snapshot.id::text AND job.result->>'sourceId'=source.id::text AND job.result->>'state'='complete',
 'snapshot_bound',snapshot.source_revision=source.revision AND snapshot.state='complete' AND snapshot.enumeration_complete AND snapshot.withdrawal_authority AND snapshot.completed_at IS NOT NULL AND source.last_complete_snapshot_id=snapshot.id,
 'member_count',snapshot.member_count,'manifest_sha256',snapshot.body_digest,
 'current_observations',(SELECT count(*) FROM observations o WHERE o.tenant_id=source.tenant_id AND o.source_id=source.id AND o.freshness='current'),
 'inspection_bound',inspection.document->>'resolvedUrl'=source.endpoint)
 FROM federation_sources source JOIN jobs job ON job.tenant_id=source.tenant_id AND job.id='%s'
 JOIN source_snapshots snapshot ON snapshot.tenant_id=source.tenant_id AND snapshot.source_id=source.id AND snapshot.id='%s'
 JOIN source_inspections inspection ON inspection.tenant_id=source.tenant_id AND inspection.id='%s'
 WHERE source.tenant_id='%s' AND source.id='%s';`, ids[1], ids[2], ids[3], primaryTenantID, ids[0]), nil
}

func agntcyPersistenceMatches(actual, discovery map[string]any) error {
	for _, key := range []string{"source_id", "job_id", "snapshot_id", "inspection_id", "manifest_sha256"} {
		if actual[key] != discovery[key] {
			return fmt.Errorf("directory PostgreSQL %s binding differs", key)
		}
	}
	for _, key := range []string{"source_active", "job_succeeded", "job_bound", "snapshot_bound", "inspection_bound"} {
		if actual[key] != true {
			return fmt.Errorf("directory PostgreSQL %s did not hold", key)
		}
	}
	members, membersOK := discovery["members"].(float64)
	observations, observationsOK := discovery["observations"].(float64)
	if !membersOK || !observationsOK || members < 0 || observations < 0 || members != observations {
		return errors.New("invalid Directory discovery membership evidence")
	}
	if actual["source_type"] != "ard_manifest" || actual["endpoint"] != agntcyOrigin+"/.well-known/ai-catalog.json" || actual["source_revision"] != float64(1) || actual["member_count"] != members || actual["current_observations"] != observations {
		return errors.New("unexpected PostgreSQL Directory source or snapshot state")
	}
	return nil
}

// Give Python time to interrupt its bounded native/checker process groups before cluster cleanup.
func (h *harness) runAGNTCYQualification(ctx context.Context, path, output string) error {
	fmt.Println("[agntcy] qualify native Directory and PostgreSQL Catalog")
	command := exec.CommandContext(ctx, h.agntcyPython(), "-B", "test/interoperability/agntcy_fresh.py", path, output) // #nosec G204 -- preflight-validated Python and exact invocation-owned paths; no shell.
	command.Dir, command.Env = h.options.repositoryRoot, h.environment()
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 5 * time.Second
	buffer := &boundedBuffer{limit: maximumCommandOutput}
	command.Stdout, command.Stderr = buffer, buffer
	if err := command.Run(); err != nil {
		return fmt.Errorf("native Directory qualification: %w\n%s", err, h.redactDiagnostic(buffer.String()))
	}
	fmt.Print(h.redactDiagnostic(buffer.String()))
	return nil
}
