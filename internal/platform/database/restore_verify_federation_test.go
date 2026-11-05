package database

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

func TestRestoreObservationOptionalFilterDecisions(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"identifier":"urn:air:remote.example:default:agent"}`)
	normalized := json.RawMessage(`{"profile":"catalog-observation-v1","identifier":"urn:air:remote.example:default:agent","versionPresent":false,"version":"","kind":"agent","mediaType":"application/a2a-agent-card+json","displayName":"Agent","description":"","tags":[],"representativeQueries":[],"metadata":{"license":"MIT"}}`)
	compatibility := []byte(`{"structural":"valid","support":"supported"}`)
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	license := "MIT"
	for _, mode := range []string{"valid", "unavailable license", "wrong license", "invented empty topics", "known empty topics", "lost empty topics", "old version", "malformed bytes", "failed read"} {
		t.Run(mode, func(t *testing.T) {
			document := append(json.RawMessage(nil), normalized...)
			var projectedLicense *string = &license
			var categories []string
			complete := true
			code := ""
			switch mode {
			case "unavailable license":
				document = json.RawMessage(strings.Replace(string(document), `"MIT"`, `true`, 1))
				projectedLicense = nil
			case "wrong license":
				wrong := "Apache-2.0"
				projectedLicense, code = &wrong, "RESTORE_SEARCH_FILTER_PROJECTION_INVALID"
			case "invented empty topics":
				categories, code = []string{}, "RESTORE_SEARCH_FILTER_PROJECTION_INVALID"
			case "known empty topics", "lost empty topics":
				document = json.RawMessage(strings.TrimSuffix(string(document), "}") + `,"unknownProperties":{"http://purl.org/dc/terms/subject":[]}}`)
				if mode == "known empty topics" {
					categories = []string{}
				} else {
					code = "RESTORE_SEARCH_FILTER_PROJECTION_INVALID"
				}
			case "old version":
				complete, code = false, "RESTORE_SEARCH_PROJECTION_DRIFT"
			case "malformed bytes":
				document, code = json.RawMessage(`{"bad":`), "RESTORE_OBSERVATION_EVIDENCE_INVALID"
			}
			row := []any{restoreTestTenant, "source", "snapshot", "key", "urn:air:remote.example:default:agent", "", false, catalog.KindAgent, "application/a2a-agent-card+json", federation.CompleteEntry,
				compatibility, audit.DigestBytes(raw), audit.DigestBytes(raw), audit.DigestBytes(document), "catalog-observation-v1", raw, document,
				federation.FreshCurrent, at, at, "Agent", "", []string{}, []string{}, "remote.example", complete, projectedLicense, categories, "", true}
			rows := &identityScriptedRows{values: [][]any{{restoreTestTenant, int64(len(raw) + len(document) + 2048)}}}
			sentinel := errors.New("injected observation read failure")
			read := scriptedValueRow(row)
			if mode == "failed read" {
				read = scannerFunc(func(...any) error { return sentinel })
			}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}, queryRows: []pgx.Row{read}}
			records, more, err := scanRestoreObservations(t.Context(), tx, restoreTestTenant, "", 1)
			if mode == "failed read" {
				if !errors.Is(err, sentinel) || len(records) != 0 || more {
					t.Fatalf("failed read=%v %t %v", records, more, err)
				}
				return
			}
			if err != nil || more || len(records) != 1 || !rows.closed {
				t.Fatalf("records=%+v more=%t err=%v", records, more, err)
			}
			if code == "" && records[0].finding != nil || code != "" && (records[0].finding == nil || records[0].finding.Code != code) {
				t.Fatalf("finding=%+v; want %q", records[0].finding, code)
			}
		})
	}
}

func TestRestoreInspectionBindsDecodedDocumentToItsRow(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	inspection := federation.Inspection{ID: "inspection", TenantID: restoreTestTenant, RequestedURL: "https://source.example/ard.json", ResolvedURL: "https://source.example/ard.json", SourceType: federation.SourceARDManifest, ProfileID: "ard-test", MediaType: "application/ard+json", BodyDigest: digest, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: digest, CreatedAt: at, ExpiresAt: at.Add(time.Hour)}
	if err := federation.ValidateRetainedInspectionIntegrity(inspection); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(inspection)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "wrong ID", "wrong tenant", "wrong expiry", "wrong creation", "wrong shape", "invalid document", "extra row"} {
		t.Run(mode, func(t *testing.T) {
			row := []any{inspection.ID, inspection.TenantID, int64(len(raw)), raw, inspection.ExpiresAt, inspection.CreatedAt}
			switch mode {
			case "wrong ID":
				row[0] = "other"
			case "wrong tenant":
				row[1] = "other"
			case "wrong expiry":
				row[4] = at
			case "wrong creation":
				row[5] = at.Add(time.Second)
			case "wrong shape":
				row[3] = []byte(`[]`)
			case "invalid document":
				row[3] = []byte(`{}`)
			}
			rows := &identityScriptedRows{values: [][]any{row}}
			if mode == "extra row" {
				rows.values = append(rows.values, row)
			}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			records, more, err := scanRestoreSourceInspections(t.Context(), tx, restoreTestTenant, "", 1)
			if err != nil || more != (mode == "extra row") || len(records) != 1 || !rows.closed {
				t.Fatalf("records=%v more=%v error=%v", records, more, err)
			}
			invalid := mode != "valid" && mode != "extra row"
			if (records[0].finding != nil) != invalid || invalid && records[0].finding.Code != "RESTORE_SOURCE_INSPECTION_INVALID" {
				t.Fatalf("binding finding=%+v", records[0].finding)
			}
		})
	}
}

func TestRestoreSourceRevisionChecksTypedProjections(t *testing.T) {
	t.Parallel()
	source := federation.Source{ID: restoreTestTenant, TenantID: restoreTestTenant, Revision: 2, RetrievalMode: federation.RetrievalIndexed, UpdateMode: federation.UpdateManual, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("a", 64), CredentialRevision: "v1", DiscoveryAssignments: []federation.DiscoveryAssignment{{Host: "catalog.example.com", Audience: federation.AudienceAnonymous}}, Referrals: federation.ReferralPolicy{ApprovedSourceIDs: []string{"00000000-0000-7000-8000-000000000002"}}}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := json.Marshal(source.DiscoveryAssignments)
	if err != nil {
		t.Fatal(err)
	}
	referrals, err := json.Marshal(source.Referrals.ApprovedSourceIDs)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "digest", "JSON", "duplicate key", "identity", "revision", "retrieval mode", "update mode", "list endpoint", "policy", "policy revision", "policy digest", "credential revision", "audiences", "referrals"} {
		t.Run(mode, func(t *testing.T) {
			row := []any{source.ID, source.Revision, true, raw, string(source.RetrievalMode), string(source.UpdateMode), source.RegistryListURL, source.PolicyID, source.PolicyRevision, source.PolicyDigest, source.CredentialRevision, assignments, referrals}
			code := "RESTORE_SOURCE_REVISION_PROJECTION_INVALID"
			switch mode {
			case "valid":
				code = ""
			case "digest":
				row[2] = false
				code = "RESTORE_SOURCE_REVISION_INVALID"
			case "JSON":
				row[3] = []byte(`{`)
				code = "RESTORE_SOURCE_REVISION_INVALID"
			case "duplicate key":
				row[3] = []byte(`{"id":"one","id":"two"}`)
				code = "RESTORE_SOURCE_REVISION_INVALID"
			case "identity":
				row[0] = "other"
				code = "RESTORE_SOURCE_REVISION_INVALID"
			case "revision":
				row[1] = int64(3)
				code = "RESTORE_SOURCE_REVISION_INVALID"
			case "retrieval mode":
				row[4] = "live"
			case "update mode":
				row[5] = "automatic"
			case "list endpoint":
				row[6] = "https://other.example/list"
			case "policy":
				row[7] = ""
			case "policy revision":
				row[8] = int64(0)
			case "policy digest":
				row[9] = "different"
			case "credential revision":
				row[10] = "different"
			case "audiences":
				row[11] = []byte(`[]`)
			case "referrals":
				row[12] = []byte(`[]`)
			}
			positions := &identityScriptedRows{values: [][]any{{source.ID, source.Revision, int64(len(raw))}}}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: positions}}, queryRows: []pgx.Row{scriptedValueRow(row)}}
			records, more, err := scanRestoreSourceRevisions(t.Context(), tx, restoreTestTenant, "", 1)
			if err != nil || more || len(records) != 1 || !positions.closed {
				t.Fatalf("records=%v more=%v error=%v", records, more, err)
			}
			if code == "" {
				if records[0].finding != nil {
					t.Fatalf("valid configuration rejected: %+v", records[0].finding)
				}
			} else if records[0].finding == nil || records[0].finding.Code != code {
				t.Fatalf("finding=%+v want=%s", records[0].finding, code)
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{restoreTestTenant, source.ID, source.Revision}}) {
				t.Fatal("source revision lost exact tenant/revision binding")
			}
		})
	}
}

func TestRestoreSourceSnapshotDocumentAndRelationalBindings(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	completed := at.Add(time.Minute)
	digest := "sha256:" + strings.Repeat("a", 64)
	snapshot := federation.Snapshot{ID: "snapshot", TenantID: restoreTestTenant, SourceID: "source", SourceRevision: 2, State: federation.SnapshotComplete, EnumerationComplete: true, MemberCount: 1, Added: 1, BodyDigest: digest, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: digest, StartedAt: at, CompletedAt: &completed}
	if err := federation.ValidateRetainedSnapshotIntegrity(snapshot); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "malformed JSON", "invalid document", "wrong source", "missing revision", "wrong digest", "wrong completion", "member mismatch", "member overflow", "extra attempt", "attempt failure"} {
		t.Run(mode, func(t *testing.T) {
			row := []any{snapshot.ID, snapshot.SourceID, &snapshot.SourceRevision, string(snapshot.State), snapshot.EnumerationComplete, snapshot.WithdrawalAuthority, snapshot.MemberCount, snapshot.BodyDigest, raw, at, &completed, 1}
			switch mode {
			case "malformed JSON":
				row[8] = []byte(`{`)
			case "invalid document":
				row[8] = []byte(`{}`)
			case "wrong source":
				row[1] = "other"
			case "missing revision":
				row[2] = (*int64)(nil)
			case "wrong digest":
				row[7] = "wrong"
			case "wrong completion":
				row[10] = (*time.Time)(nil)
			case "member mismatch":
				row[11] = 0
			case "member overflow":
				row[11] = federation.MaxSynchronizationEntries + 1
			}
			positions := &identityScriptedRows{values: [][]any{{snapshot.ID, int64(len(raw))}}}
			attempts := &identityScriptedRows{}
			if mode == "extra attempt" {
				attempts.values = [][]any{{"extra", "key", "requested", "final", "profile", 200, true, false, "body", "", "", at}}
			}
			failure := errors.New("attempt read failed")
			result := identityQueryResult{rows: attempts}
			if mode == "attempt failure" {
				result.err = failure
			}
			tx := &identityScriptedTx{queryRows: []pgx.Row{scriptedValueRow(row)}, queryResults: []identityQueryResult{{rows: positions}, result}}
			records, more, err := scanRestoreSourceSnapshots(t.Context(), tx, restoreTestTenant, "", 2)
			if mode == "attempt failure" {
				if !errors.Is(err, failure) || len(records) != 0 || more {
					t.Fatalf("attempt failure=%v", err)
				}
				return
			}
			if err != nil || more || len(records) != 1 || !positions.closed || !attempts.closed {
				t.Fatalf("records=%v more=%v error=%v", records, more, err)
			}
			if (records[0].finding != nil) != (mode != "valid") || records[0].finding != nil && records[0].finding.Code != "RESTORE_SOURCE_SNAPSHOT_INVALID" {
				t.Fatalf("snapshot finding=%+v", records[0].finding)
			}
			if !reflect.DeepEqual(tx.queryRowArguments[0], []any{restoreTestTenant, snapshot.ID, federation.MaxSynchronizationEntries + 1}) {
				t.Fatal("snapshot binding lost tenant or bounded member selection")
			}
		})
	}
}

func TestRestoreOccurrenceDecodingRetainsProvenance(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	completed := at.Add(time.Minute)
	digest := "sha256:" + strings.Repeat("a", 64)
	revision := int64(2)
	for _, mode := range []string{"valid", "missing occurrence revision", "missing snapshot revision", "incomplete evidence", "wrong tenant", "wrong source", "wrong digest", "bad location", "outside snapshot", "extra row"} {
		t.Run(mode, func(t *testing.T) {
			row := []any{"observation", "source", "snapshot", &revision, digest, "/entries/0", digest, "", "", true, at, restoreTestTenant, "source", digest, "", digest, at, completed, restoreTestTenant, "source", &revision, federation.SnapshotComplete, at, &completed}
			switch mode {
			case "missing occurrence revision":
				row[3] = (*int64)(nil)
			case "missing snapshot revision":
				row[20] = (*int64)(nil)
			case "incomplete evidence":
				row[9] = false
			case "wrong tenant":
				row[18] = "other"
			case "wrong source":
				row[12] = "other"
			case "wrong digest":
				row[13] = "other"
			case "bad location":
				row[5] = "/entries/00"
			case "outside snapshot":
				row[22] = completed
			}
			rows := &identityScriptedRows{values: [][]any{row}}
			if mode == "extra row" {
				rows.values = append(rows.values, row)
			}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			records, more, err := scanRestoreObservationOccurrences(t.Context(), tx, restoreTestTenant, "", 1)
			if err != nil || more != (mode == "extra row") || len(records) != 1 || !rows.closed {
				t.Fatalf("records=%v more=%v error=%v", records, more, err)
			}
			invalid := mode != "valid" && mode != "extra row"
			if (records[0].finding != nil) != invalid || invalid && records[0].finding.Code != "RESTORE_OBSERVATION_OCCURRENCE_INVALID" {
				t.Fatalf("provenance finding=%+v", records[0].finding)
			}
			if !invalid && records[0].key != encodeOccurrenceKey("snapshot", digest, "/entries/0") {
				t.Fatal("occurrence continuation lost its composite identity")
			}
		})
	}
}
