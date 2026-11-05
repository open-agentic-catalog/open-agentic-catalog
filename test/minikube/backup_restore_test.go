//go:build minikube

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
)

func backupTestValue[T any](t *testing.T, value any) T {
	t.Helper()
	got, ok := value.(T)
	if !ok {
		t.Fatalf("unexpected fixture value type %T", value)
	}
	return got
}

func TestBackupDatabaseHasDistinctSelectorsAndVolumeIdentity(t *testing.T) {
	h := &harness{options: options{scenario: "backup-restore"}, postgres: "owned-postgres"}
	objects, err := h.backupDatabaseObjects()
	if err != nil || len(objects) != 2 {
		t.Fatal("restore must create just a Service and StatefulSet")
	}
	for _, object := range objects {
		metadata := backupTestValue[map[string]any](t, object["metadata"])
		if metadata["name"] != restoredPostgres || metadata["namespace"] != namespace {
			t.Fatal("restore escaped its owned fixture identity")
		}
		spec := backupTestValue[map[string]any](t, object["spec"])
		if object["kind"] == "Service" {
			if backupTestValue[map[string]string](t, spec["selector"])["app.kubernetes.io/component"] != restoredPostgres {
				t.Fatal("restore Service selects the original database")
			}
			continue
		}
		template := backupTestValue[map[string]any](t, spec["template"])
		metadata = backupTestValue[map[string]any](t, template["metadata"])
		labels := backupTestValue[map[string]string](t, metadata["labels"])
		selector := backupTestValue[map[string]string](t, backupTestValue[map[string]any](t, spec["selector"])["matchLabels"])
		if labels["app.kubernetes.io/component"] != restoredPostgres || selector["app.kubernetes.io/component"] != restoredPostgres || spec["serviceName"] != restoredPostgres {
			t.Fatal("restored database overlaps original selection")
		}
		pod := backupTestValue[map[string]any](t, template["spec"])
		containers := backupTestValue[[]any](t, pod["containers"])
		if len(containers) != 1 {
			t.Fatal("expected one PostgreSQL container")
		}
		container := backupTestValue[map[string]any](t, containers[0])
		if container["image"] != h.postgres || len(backupTestValue[[]any](t, spec["volumeClaimTemplates"])) != 1 {
			t.Fatal("restore needs the pinned image and its own claim template")
		}
		probeObject := backupTestValue[map[string]any](t, container["readinessProbe"])
		exec := backupTestValue[map[string]any](t, probeObject["exec"])
		probe := backupTestValue[[]string](t, exec["command"])
		if !slices.Contains(probe, "--host=127.0.0.1") {
			t.Fatal("restore readiness can accept the temporary Unix-only initialization server")
		}
		for _, volume := range backupTestValue[[]any](t, pod["volumes"]) {
			if _, shared := backupTestValue[map[string]any](t, volume)["persistentVolumeClaim"]; shared {
				t.Fatal("restore must not attach an existing source claim")
			}
		}
	}
	for _, object := range h.dependencyObjects() {
		if object["kind"] == "StatefulSet" && backupTestValue[map[string]any](t, object["metadata"])["name"] != "postgres" {
			t.Fatal("restore mutated the original fixture")
		}
	}
}

func TestBackupRolesSelectOnlyTheRequestedFixture(t *testing.T) {
	h := &harness{options: options{scenario: "backup-restore"}}
	job := h.databaseRoleJob("restore-roles", restoredPostgres)
	spec := backupTestValue[map[string]any](t, job["spec"])
	template := backupTestValue[map[string]any](t, spec["template"])
	pod := backupTestValue[map[string]any](t, template["spec"])
	container := backupTestValue[map[string]any](t, backupTestValue[[]any](t, pod["containers"])[0])
	inputs := make(map[string]map[string]any)
	for _, raw := range backupTestValue[[]any](t, container["env"]) {
		env := backupTestValue[map[string]any](t, raw)
		name := backupTestValue[string](t, env["name"])
		if inputs[name] != nil {
			t.Fatal("duplicate role setup input")
		}
		inputs[name] = env
	}
	for name, expected := range map[string]string{"PGHOST": restoredPostgres, "PGPORT": "5432", "PGUSER": "catalog_owner", "PGDATABASE": "catalog"} {
		if inputs[name] == nil || inputs[name]["value"] != expected {
			t.Fatal("missing or incorrect database input: " + name)
		}
	}
	for name, expected := range map[string][2]string{"PGPASSWORD": {"postgres-owner", "password"}, "API_PASSWORD": {"catalog-role-passwords", "api"}, "WORKER_PASSWORD": {"catalog-role-passwords", "worker"}} {
		if inputs[name] == nil || inputs[name]["value"] != nil {
			t.Fatal("missing or plaintext credential input: " + name)
		}
		from := backupTestValue[map[string]any](t, inputs[name]["valueFrom"])
		secret := backupTestValue[map[string]string](t, from["secretKeyRef"])
		if secret["name"] != expected[0] || secret["key"] != expected[1] {
			t.Fatal("incorrect credential scope: " + name)
		}
	}
}

func TestBackupTenantInventoryRequiresEveryExpectedTenant(t *testing.T) {
	for _, raw := range []string{`["` + primaryTenantID + `","` + otherTenantID + `"]`, "[\n  \"" + primaryTenantID + "\",\n  \"" + otherTenantID + "\"\n]"} {
		if !validBackupTenants([]byte(raw)) {
			t.Fatal("complete tenant inventory rejected because of JSON formatting")
		}
	}
	for _, raw := range []string{`null`, `[]`, `{}`, `["` + primaryTenantID + `"]`, `["` + primaryTenantID + `","` + primaryTenantID + `"]`, `["wrong","` + otherTenantID + `"]`, `["` + primaryTenantID + `","` + otherTenantID + `","unexpected"]`} {
		if validBackupTenants([]byte(raw)) {
			t.Fatal("incomplete or unexpected tenant inventory accepted")
		}
	}
}

func TestBackupJobRequiresAnExplicitNameBeforeCommands(t *testing.T) {
	if name, err := backupJobName(kubernetesObject{"metadata": map[string]any{"name": "restore-check"}}); err != nil || name != "restore-check" {
		t.Fatal("valid fixture Job name failed")
	}
	for _, job := range []kubernetesObject{{}, {"metadata": "invalid"}, {"metadata": map[string]any{"name": 1}}, {"metadata": map[string]any{"name": ""}}} {
		if _, err := backupJobName(job); err == nil {
			t.Fatal("invalid fixture metadata accepted")
		}
	}
}

func TestBackupInventoryRefusesPartialOrUnboundedEvidence(t *testing.T) {
	row := backupTable{Table: "audit_events", Rows: 2, Bytes: 1024, Digest: strings.Repeat("a", 64)}
	encode := func(row backupTable) []byte {
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if got, err := decodeBackupInventory(encode(row)); err != nil || len(got) != 1 || got[0] != row {
		t.Fatalf("complete inventory failed: %v", err)
	}
	for _, field := range []string{"name", "rows", "bytes", "digest"} {
		bad := row
		switch field {
		case "name":
			bad.Table = ""
		case "rows":
			bad.Rows = 10001
		case "bytes":
			bad.Bytes = (64 << 20) + 1
		case "digest":
			bad.Digest = "invalid"
		}
		if _, err := decodeBackupInventory(encode(bad)); err == nil {
			t.Fatal("accepted invalid " + field)
		}
	}
	for _, raw := range [][]byte{nil, []byte(`{`), append(append(encode(row), '\n'), encode(row)...), []byte(strings.Repeat(string(encode(row))+"\n", 151))} {
		if _, err := decodeBackupInventory(raw); err == nil {
			t.Fatal("accepted incomplete or duplicate inventory")
		}
	}
	empty := row
	empty.Rows, empty.Bytes = 0, 0
	if _, err := decodeBackupInventory(encode(empty)); err != nil {
		t.Fatal("an empty table still has a valid inventory")
	}
	for _, name := range []string{"rows", "bytes"} {
		t.Run("null "+name, func(t *testing.T) {
			fields := map[string]any{"table": row.Table, "rows": row.Rows, "bytes": row.Bytes, "digest": row.Digest}
			fields[name] = nil
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeBackupInventory(raw); err == nil {
				t.Fatal("null is not an observed count")
			}
		})
	}
}

func TestBackupRequiresAllMigrationsApplied(t *testing.T) {
	if version, err := backupSchemaVersion([]byte(`[{"version":1,"applied":true},{"version":2,"applied":true}]`)); err != nil || version != 2 {
		t.Fatal("complete migration baseline failed")
	}
	for _, raw := range []string{`[]`, `{}`, `null`, `[{"version":1}]`, `[{"version":1,"applied":true},{"version":2,"applied":false}]`, `[{"version":2,"applied":true},{"version":1,"applied":true}]`} {
		if _, err := backupSchemaVersion([]byte(raw)); err == nil {
			t.Fatal("accepted incomplete migration baseline")
		}
	}
}

func backupTestPage() database.RestoreVerificationReport {
	return database.RestoreVerificationReport{FormatVersion: database.RestoreVerificationFormatVersion,
		CheckSetVersion: database.RestoreVerificationCheckSetVersion, TenantID: primaryTenantID, RecoveryPoint: "backup-point",
		SchemaVersion: 47, PrivilegeManifestVersion: database.RuntimePrivilegeManifestVersion(), Traversal: "complete", Integrity: "valid",
		Processed: 7, Findings: []database.RestoreFinding{}, AssuranceBoundary: "owned isolated fixture"}
}

func TestBackupPageRequiresCurrentCompleteEvidence(t *testing.T) {
	encode := func(page database.RestoreVerificationReport) []byte {
		raw, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if next, err := acceptBackupPage(encode(backupTestPage()), primaryTenantID, "backup-point", 47); err != nil || next != "" {
		t.Fatalf("complete page failed: %v", err)
	}
	for code, domain := range map[string]string{"RESTORE_ACTIONABLE_JOB_RETAINED": "job", "RESTORE_UNACKNOWLEDGED_OUTBOX_EVENT": "outbox_event", "RESTORE_PENDING_INTERNAL_OUTBOX_EVENT": "outbox_event"} {
		page := backupTestPage()
		page.Findings = []database.RestoreFinding{{Class: "operational", Severity: "warning", Code: code, Domain: domain}}
		if _, err := acceptBackupPage(encode(page), primaryTenantID, "backup-point", 47); err != nil {
			t.Fatal("reviewed warning rejected")
		}
	}
	for name, change := range map[string]func(*database.RestoreVerificationReport){
		"format":         func(p *database.RestoreVerificationReport) { p.FormatVersion++ },
		"checks":         func(p *database.RestoreVerificationReport) { p.CheckSetVersion-- },
		"schema":         func(p *database.RestoreVerificationReport) { p.SchemaVersion-- },
		"grants":         func(p *database.RestoreVerificationReport) { p.PrivilegeManifestVersion-- },
		"tenant":         func(p *database.RestoreVerificationReport) { p.TenantID = otherTenantID },
		"point":          func(p *database.RestoreVerificationReport) { p.RecoveryPoint = "another" },
		"integrity":      func(p *database.RestoreVerificationReport) { p.Integrity = "invalid" },
		"blocked":        func(p *database.RestoreVerificationReport) { p.Traversal = "blocked" },
		"missing cursor": func(p *database.RestoreVerificationReport) { p.Traversal = "continued" },
		"extra cursor":   func(p *database.RestoreVerificationReport) { p.Continuation = "unexpected" },
		"rows":           func(p *database.RestoreVerificationReport) { p.Processed = 101 },
		"bytes": func(p *database.RestoreVerificationReport) {
			p.DecodedBytes = database.MaxRestoreVerificationDecodedBytes + 1
		},
		"missing findings": func(p *database.RestoreVerificationReport) { p.Findings = nil },
		"missing boundary": func(p *database.RestoreVerificationReport) { p.AssuranceBoundary = "" },
		"repair": func(p *database.RestoreVerificationReport) {
			p.Findings = []database.RestoreFinding{{Class: "repairable"}}
		},
		"unreviewed warning": func(p *database.RestoreVerificationReport) {
			p.Findings = []database.RestoreFinding{{Class: "operational", Severity: "warning", Code: "UNREVIEWED"}}
		},
		"wrong warning domain": func(p *database.RestoreVerificationReport) {
			p.Findings = []database.RestoreFinding{{Class: "operational", Severity: "warning", Code: "RESTORE_ACTIONABLE_JOB_RETAINED", Domain: "outbox_event"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			page := backupTestPage()
			change(&page)
			if _, err := acceptBackupPage(encode(page), primaryTenantID, "backup-point", 47); err == nil {
				t.Fatal("invalid page accepted")
			}
		})
	}
}

func TestBackupVerificationReadsEveryPageAndRejectsBrokenChains(t *testing.T) {
	for _, fault := range []string{"", "late integrity", "cycle", "read failure", "page bound"} {
		t.Run(fault, func(t *testing.T) {
			calls := 0
			count, err := readBackupPages(primaryTenantID, "backup-point", 47, func(cursor string) ([]byte, error) {
				calls++
				if calls == 1 && cursor != "" || calls == 2 && cursor != "next" {
					t.Fatal("cursor does not follow preceding page")
				}
				page := backupTestPage()
				if calls == 1 || fault == "page bound" || fault == "cycle" {
					page.Traversal, page.Continuation = "continued", "next"
				}
				if fault == "page bound" && calls > 1 {
					page.Continuation = strings.Repeat("x", calls)
				}
				if calls > 1 && fault == "late integrity" {
					page.Integrity = "invalid"
				}
				if calls > 1 && fault == "read failure" {
					return nil, errors.New("fixture read failed")
				}
				return json.Marshal(page)
			})
			if fault == "" && (err != nil || count != 2) || fault != "" && err == nil {
				t.Fatalf("chain result: pages %d, error %v", count, err)
			}
			if fault == "page bound" && calls != 100 {
				t.Fatal("page bound not exercised")
			}
		})
	}
}

func TestBackupReplayRequiresOriginalBytesAndValidators(t *testing.T) {
	expected := httpResult{Status: http.StatusCreated, Body: []byte(`{"id":"retained"}`), Header: http.Header{"Etag": {`"rev-1"`}}}
	for _, fault := range []string{"", "bytes", "status", "etag", "marker"} {
		actual := httpResult{Status: expected.Status, Body: slices.Clone(expected.Body), Header: expected.Header.Clone()}
		actual.Header.Set("Idempotency-Replayed", "true")
		switch fault {
		case "bytes":
			actual.Body = []byte(`{ "id":"retained"}`)
		case "status":
			actual.Status = http.StatusOK
		case "etag":
			actual.Header.Del("ETag")
		case "marker":
			actual.Header.Del("Idempotency-Replayed")
		}
		if sameBackupReplay(expected, actual) != (fault == "") {
			t.Fatal("incorrect replay acceptance: " + fault)
		}
	}
}
