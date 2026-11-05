//go:build minikube

package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"net"
	"strings"
	"testing"
	"time"
)

func TestAGNTCYPersistenceBindsExactTenantAndAllIdentities(t *testing.T) {
	discovery := map[string]any{"source_id": "00000000-0000-7000-8000-000000000011", "job_id": "00000000-0000-7000-8000-000000000012", "snapshot_id": "00000000-0000-7000-8000-000000000013", "inspection_id": "00000000-0000-7000-8000-000000000014", "manifest_sha256": "sha256:" + strings.Repeat("a", 64), "members": float64(0), "observations": float64(0)}
	query, err := agntcyPersistenceQuery(discovery)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{primaryTenantID, fmt.Sprint(discovery["source_id"]), fmt.Sprint(discovery["job_id"]), fmt.Sprint(discovery["snapshot_id"]), fmt.Sprint(discovery["inspection_id"]), "job.tenant_id=source.tenant_id", "snapshot.tenant_id=source.tenant_id", "inspection.tenant_id=source.tenant_id", "o.tenant_id=source.tenant_id", "source.last_complete_snapshot_id=snapshot.id", "job.result->>'id'=snapshot.id::text"} {
		if !strings.Contains(query, required) {
			t.Fatalf("missing retained-state binding %s", required)
		}
	}
	for _, key := range []string{"source_id", "job_id", "snapshot_id", "inspection_id", "manifest_sha256"} {
		for _, invalid := range []any{nil, 123, "' OR true --", "", true} {
			altered := cloneAGNTCYMap(discovery)
			altered[key] = invalid
			if _, err := agntcyPersistenceQuery(altered); err == nil {
				t.Fatalf("accepted invalid %s: %v", key, invalid)
			}
		}
	}
	actual := cloneAGNTCYMap(discovery)
	delete(actual, "members")
	delete(actual, "observations")
	for _, key := range []string{"source_active", "job_succeeded", "job_bound", "snapshot_bound", "inspection_bound"} {
		actual[key] = true
	}
	actual["source_type"], actual["endpoint"] = "ard_manifest", agntcyOrigin+"/.well-known/ai-catalog.json"
	actual["source_revision"], actual["member_count"], actual["current_observations"] = float64(1), float64(0), float64(0)
	if err := agntcyPersistenceMatches(actual, discovery); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"members", "observations"} {
		for _, invalid := range []any{nil, true, float64(-1), float64(1)} {
			altered := cloneAGNTCYMap(discovery)
			altered[key] = invalid
			if err := agntcyPersistenceMatches(actual, altered); err == nil {
				t.Fatalf("accepted contradictory %s: %v", key, invalid)
			}
		}
	}
	for key := range actual {
		altered := cloneAGNTCYMap(actual)
		delete(altered, key)
		if err := agntcyPersistenceMatches(altered, discovery); err == nil {
			t.Fatalf("accepted missing %s", key)
		}
	}
	for _, key := range []string{"member_count", "current_observations", "source_revision"} {
		altered := cloneAGNTCYMap(actual)
		altered[key] = false
		if err := agntcyPersistenceMatches(altered, discovery); err == nil {
			t.Fatalf("accepted untyped %s", key)
		}
	}
}

func cloneAGNTCYMap(input map[string]any) map[string]any {
	result := make(map[string]any)
	for key, value := range input {
		result[key] = value
	}
	return result
}

func TestAGNTCYProjectedPayloadsRequireRetainedBlockedEvidence(t *testing.T) {
	evidence := func(codes ...any) map[string]any {
		return map[string]any{"state": "blocked", "revision_id": "00000000-0000-7000-8000-000000000011", "codes": codes}
	}
	valid := map[string]any{"agent": map[string]any{"rejected_projection": evidence("A2A_PROTOJSON_INVALID")},
		"mcp_server": map[string]any{"evidence": evidence("MCP_SERVER_CARD_SCHEMA_INVALID", "MCP_SERVER_CARD_VERSION_REQUIRED", "MCP_SERVER_CARD_EXPERIMENTAL")}}
	if err := agntcyProjectedPayloads(map[string]any{"kinds": valid}); err != nil {
		t.Fatal(err)
	}
	if err := agntcyProjectedPayloads(nil); err == nil {
		t.Fatal("accepted missing payload evidence")
	}
	for kind, field := range map[string]string{"agent": "rejected_projection", "mcp_server": "evidence"} {
		parent, ok := valid[kind].(map[string]any)
		if !ok {
			t.Fatal("invalid synthetic kind")
		}
		original, ok := parent[field].(map[string]any)
		if !ok {
			t.Fatal("invalid synthetic evidence")
		}
		for key, replacements := range map[string][]any{
			"state": {nil, "complete", "invalid"}, "revision_id": {nil, "unretained", true},
			"codes": {nil, []any{}, []any{"OTHER_FAILURE"}, []any{"A2A_PROTOJSON_INVALID", "OTHER_FAILURE"}},
		} {
			for _, replacement := range replacements {
				changed := cloneAGNTCYMap(original)
				changed[key] = replacement
				kinds := cloneAGNTCYMap(valid)
				kinds[kind] = map[string]any{field: changed}
				if err := agntcyProjectedPayloads(map[string]any{"kinds": kinds}); err == nil {
					t.Fatalf("accepted invalid %s %s: %v", kind, key, replacement)
				}
			}
		}
		kinds := cloneAGNTCYMap(valid)
		delete(kinds, kind)
		if err := agntcyProjectedPayloads(map[string]any{"kinds": kinds}); err == nil {
			t.Fatal("accepted missing kind", kind)
		}
	}
}

func TestAGNTCYNativeImportsCannotBeSkippedOrCountedAsNonemptyFederation(t *testing.T) {
	rows := make([]any, 0, 3)
	for _, kind := range []string{"a2a", "mcp", "agent-skill"} {
		rows = append(rows, map[string]any{"kind": kind, "status": "pass"})
	}
	value := map[string]any{"status": "pass", "stored_after": float64(6), "cases": rows,
		"federation": map[string]any{"disposition": "unsupported_nonempty", "manifest_entries": float64(0), "transfer": "unrun", "native_change": "unrun", "withdrawal": "unrun", "outage_recovery": "unrun"}}
	if err := agntcyNativeImports(map[string]any{"native_imports": value}); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"transfer", "native_change", "withdrawal", "outage_recovery"} {
		original, ok := value["federation"].(map[string]any)
		if !ok {
			t.Fatal("invalid synthetic federation evidence")
		}
		federation := cloneAGNTCYMap(original)
		federation[stage] = "pass"
		altered := cloneAGNTCYMap(value)
		altered["federation"] = federation
		if err := agntcyNativeImports(map[string]any{"native_imports": altered}); err == nil {
			t.Fatal("counted unsupported transfer as passed", stage)
		}
	}
	for _, report := range []map[string]any{nil, {"native_imports": true}} {
		if err := agntcyNativeImports(report); err == nil {
			t.Fatal("accepted absent import evidence")
		}
	}
	for key, replacements := range map[string][]any{
		"status": {nil, "running", "fail"}, "stored_after": {nil, false, float64(7)},
		"cases": {nil, rows[:2], []any{rows[0], rows[0], rows[2]}, []any{rows[0], rows[1], nil},
			[]any{rows[0], rows[1], map[string]any{"kind": "agent-skill", "status": "unrun"}}},
		"federation": {nil, map[string]any{"disposition": "supported", "manifest_entries": float64(1)}},
	} {
		for _, replacement := range replacements {
			altered := cloneAGNTCYMap(value)
			altered[key] = replacement
			if err := agntcyNativeImports(map[string]any{"native_imports": altered}); err == nil {
				t.Fatalf("accepted invalid %s: %v", key, replacement)
			}
		}
	}
}

func TestAGNTCYDiscoveryRequiresFourDistinctPassedGroups(t *testing.T) {
	rows := make([]any, 0, 4)
	for _, name := range []string{"artifact", "discovery", "search", "conformance"} {
		rows = append(rows, map[string]any{"name": name, "status": "pass", "evidence": map[string]any{"source_id": "synthetic"}})
	}
	if _, err := agntcyDiscovery(map[string]any{"cases": rows}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{nil, rows[:3], append(append([]any{}, rows...), rows[0]), []any{rows[0], rows[0], rows[2], rows[3]}} {
		if _, err := agntcyDiscovery(map[string]any{"cases": value}); err == nil {
			t.Fatalf("accepted invalid groups %v", value)
		}
	}
	for _, replacement := range []any{nil, map[string]any{"name": "other", "status": "pass"}, map[string]any{"name": "discovery", "status": "unrun"}, map[string]any{"name": "discovery", "status": "pass"}} {
		altered := append([]any{}, rows...)
		altered[1] = replacement
		if _, err := agntcyDiscovery(map[string]any{"cases": altered}); err == nil {
			t.Fatalf("accepted invalid group %v", replacement)
		}
	}
}

func TestAGNTCYGatewayCertificateVerifiesServiceAndLoopbackWithoutBroadeningDatabase(t *testing.T) {
	now := time.Now()
	material, err := serviceTLSMaterial(now, "owned-directory-ca", agntcyNamespace, []string{"directory-gateway"}, net.ParseIP("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(material["directory-gateway.crt"]))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(material["ca.pem"])) {
		t.Fatal("missing CA")
	}
	for _, name := range []string{"127.0.0.1", "directory-gateway." + agntcyNamespace + ".svc.cluster.local"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots, CurrentTime: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := leaf.VerifyHostname("public.example.com"); err == nil {
		t.Fatal("accepted unrelated origin")
	}
	database, err := databaseTLSMaterial(now)
	if err != nil {
		t.Fatal(err)
	}
	block, _ = pem.Decode([]byte(database["postgres.crt"]))
	leaf, err = x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.IPAddresses) != 0 {
		t.Fatal("database trust broadened")
	}
}

func TestAGNTCYChecksDurableJobReferenceRatherThanPublicProjection(t *testing.T) {
	reference := jobs.SnapshotReference{ID: "00000000-0000-7000-8000-000000000011", SourceID: "00000000-0000-7000-8000-000000000012", State: "complete"}
	raw, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	var retained map[string]any
	if err := json.Unmarshal(raw, &retained); err != nil {
		t.Fatal(err)
	}
	if retained["id"] != reference.ID || retained["snapshotId"] != nil {
		t.Fatal("durable result contract changed")
	}
	public := jobs.PublicView(jobs.Job{Kind: "source.sync", Result: raw})
	if public.Result == nil || public.Result.SnapshotID != reference.ID {
		t.Fatal("public result lost the snapshot identity")
	}
}
