//go:build minikube

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGatewayPersistenceRequiresBoundJobSnapshotDigestAndAudit(t *testing.T) {
	id := "00000000-0000-7000-8000-000000000003"
	evidence := map[string]any{"job": map[string]any{"id": id, "attempt": 2}, "snapshot": map[string]any{"id": id, "sourceId": id, "memberCount": 3, "bodyDigest": "sha256:" + strings.Repeat("a", 64)}}
	report := map[string]any{"phase": "recovery", "evidence": evidence}
	encode := func() []byte {
		t.Helper()
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	query, err := gatewayPersistenceQuery(encode())
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"job.attempt=2", "job.result->>'id'=snapshot.id::text", "a.details->'facts'->>'jobId'=job.id::text", "jobAttempt')::int=2", "o.tenant_id=source.tenant_id", "source.last_complete_snapshot_id=", "count(*)=(SELECT retained.member_count", "retained.source_id=source.id", "o.kind IS NULL", "digest(o.raw_entry_bytes", "digest(o.normalized_entry_bytes"} {
		if !strings.Contains(query, required) {
			t.Fatal("missing persistence binding", required)
		}
	}
	snapshot, ok := evidence["snapshot"].(map[string]any)
	if !ok {
		t.Fatal("missing snapshot fixture")
	}
	for _, key := range []string{"id", "sourceId", "bodyDigest", "memberCount"} {
		old := snapshot[key]
		snapshot[key] = "' OR true --"
		if _, err := gatewayPersistenceQuery(encode()); err == nil {
			t.Fatal("admitted unsafe", key)
		}
		snapshot[key] = old
	}
	job, ok := evidence["job"].(map[string]any)
	if !ok {
		t.Fatal("missing job fixture")
	}
	job["attempt"] = 0
	if _, err := gatewayPersistenceQuery(encode()); err == nil {
		t.Fatal("admitted missing attempt")
	}
	job["attempt"] = 3
	report["phase"] = "outage"
	evidence["failed_snapshot"] = map[string]any{"id": id, "sourceId": id}
	evidence["last_complete"] = id
	evidence["attempt"] = 2
	query, err = gatewayPersistenceQuery(encode())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "job.attempt>=2") || !strings.Contains(query, "snapshot.state='failed'") || !strings.Contains(query, "jobAttempt')::int=2") {
		t.Fatal("lost exact failed attempt", query)
	}
}

func TestGatewayPersistenceRejectsMissingFalseAndUntypedAssertions(t *testing.T) {
	actual := map[string]any{}
	for _, key := range []string{"source_bound", "job_bound", "snapshot_bound", "digest_bound", "replacement_bound", "audit_bound", "observation_bytes"} {
		actual[key] = true
	}
	if err := gatewayPersistenceMatches(actual); err != nil {
		t.Fatal(err)
	}
	for key := range actual {
		for _, bad := range []any{nil, false, "true", 1} {
			actual[key] = bad
			if gatewayPersistenceMatches(actual) == nil {
				t.Fatal("admitted", key, bad)
			}
		}
		actual[key] = true
	}
}

func TestGatewayReplacementPreservesTheRemovedSourceSnapshot(t *testing.T) {
	id := "00000000-0000-7000-8000-000000000003"
	oldID := "00000000-0000-7000-8000-000000000004"
	evidence := map[string]any{"job": map[string]any{"id": id, "attempt": 1}, "snapshot": map[string]any{"id": id, "sourceId": id, "memberCount": 4, "bodyDigest": "sha256:" + strings.Repeat("a", 64)}, "replaced_source_id": oldID, "previous_snapshot": map[string]any{"id": oldID, "bodyDigest": "sha256:" + strings.Repeat("b", 64)}}
	encode := func() []byte {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"phase": "advertised", "evidence": evidence})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	query, err := gatewayPersistenceQuery(encode())
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"old.tenant_id=source.tenant_id", "old.endpoint=source.endpoint", "old.status='removed'", "retained.source_id=old.id", "retained.id=old.last_complete_snapshot_id", "retained.body_digest='sha256:" + strings.Repeat("b", 64)} {
		if !strings.Contains(query, required) {
			t.Fatal("missing replacement binding", required)
		}
	}
	for _, bad := range []string{id, "", "' OR true --"} {
		evidence["replaced_source_id"] = bad
		if _, err := gatewayPersistenceQuery(encode()); err == nil {
			t.Fatal("admitted invalid replacement identity", bad)
		}
	}
}

func TestGatewayPhaseRejectsEmptySuccessAndWrongPhase(t *testing.T) {
	report := map[string]any{"phase": "bootstrap", "status": "pass", "evidence": map[string]any{"source_id": "owned"}, "captures": []any{"record"}, "native_captures": []any{"record"}}
	encode := func() []byte {
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if err := validateGatewayPhase(encode(), "bootstrap"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"phase", "status", "evidence", "captures", "native_captures"} {
		old := report[key]
		report[key] = nil
		if validateGatewayPhase(encode(), "bootstrap") == nil {
			t.Fatal("admitted", key)
		}
		report[key] = old
	}
	report["error"] = "failed"
	if validateGatewayPhase(encode(), "bootstrap") == nil {
		t.Fatal("admitted contradictory error")
	}
}

func TestGatewayCredentialInputsStayTenantAndOriginBound(t *testing.T) {
	data, err := gatewayCredentialData([]string{"admin-value", "reader", "private", "signing", "marker", "invalid-value"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gateway-admin", "gateway-invalid"} {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(data[name+".json"]), &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["tenantId"] != primaryTenantID || metadata["origin"] != gatewayOrigin || metadata["type"] != "bearer" || metadata["secretFile"] != name+"-token" || strings.Contains(data[name+".json"], "-value") {
			t.Fatal("broadened or leaked credential binding", metadata)
		}
	}
	if len(data) != 4 || data["gateway-admin-token"] != "admin-value" || data["gateway-invalid-token"] != "invalid-value" {
		t.Fatal("wrong mounted credential inventory")
	}
	for _, bad := range [][]string{nil, {}, {""}, {"admin", "r", "p", "s", "m", ""}} {
		if _, err := gatewayCredentialData(bad); err == nil {
			t.Fatal("admitted missing credential")
		}
	}
}
