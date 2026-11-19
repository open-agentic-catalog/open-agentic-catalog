// Decoder/failure units use scripted transactions; real SQL and RLS stay in integration tests.
package database

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// maintenanceStorageRows supplies a valid immutable digest independently of the
// reader, with no native document payload or network fixture.
func maintenanceStorageRows(t *testing.T) ([]any, []any, []any) {
	t.Helper()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	e := catalog.EvidenceSet{ID: "00000000-0000-7000-8000-000000000008", TenantID: restoreTestTenant, SubjectDigest: "sha256:" + strings.Repeat("a", 64), State: catalog.EvidenceBlocked, Findings: []catalog.Finding{{Code: "TEST_BLOCKED", Severity: catalog.SeverityError, Message: "Blocked retained metadata"}}, ValidationProfile: "catalog-validation-v7", PolicyVersion: "policy-v1", LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}, ExternalRetrievals: []catalog.ExternalRetrieval{}, CreatedAt: at}
	encoded, err := json.Marshal(map[string]any{"runId": e.ID, "subjectDigest": e.SubjectDigest, "state": e.State, "findings": e.Findings, "validationProfile": e.ValidationProfile, "policyVersion": e.PolicyVersion, "legalRights": e.LegalRights, "trust": e.Trust, "externalRetrievals": e.ExternalRetrievals, "createdAt": e.CreatedAt, "expiresAt": nil})
	if err != nil {
		t.Fatal(err)
	}
	// Retained semantic digests sort nested object keys as well as the envelope.
	var normalized any
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&normalized); err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	e.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(encoded))
	if err := catalog.ValidateEvidenceSetIntegrity(e); err != nil {
		t.Fatal("invalid successful decoder control", err)
	}
	revisionID := "00000000-0000-7000-8000-000000000003"
	versionID := "00000000-0000-7000-8000-000000000002"
	key := []any{revisionID, "00000000-0000-7000-8000-000000000001", "00000000-0000-7000-8000-000000000005", catalog.KindAgent, "urn:air:example.com:agents:maintenance", versionID, "1.0.0", false}
	values := versionRevisionReadValues()
	values[0] = revisionID
	values[2] = versionID
	values[4] = e.SubjectDigest
	values[10] = e.ID
	values[12] = e.SubjectDigest
	values[13] = e.Digest
	values[14] = e.State
	values[15], err = json.Marshal(e.Findings)
	if err != nil {
		t.Fatal(err)
	}
	binding := []any{revisionID, "representation", "", "application/json", e.SubjectDigest, int64(7), false}
	return key, values, binding
}

func TestValidationMaintenanceStorageBindsWindowAndReadsNoDocuments(t *testing.T) {
	key, evidence, binding := maintenanceStorageRows(t)
	selected := &identityScriptedRows{values: [][]any{key}}
	bindings := &identityScriptedRows{values: [][]any{binding}}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow(evidence)}, queryResults: []identityQueryResult{{rows: selected}, {rows: bindings}}}
	pool := &pgxpool.Pool{}
	repo, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	q := catalog.ValidationMaintenanceRequest{State: "blocked", HorizonHours: 48, ObservedAt: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC), Limit: 21, Policy: catalog.ValidationMaintenancePolicy{Profile: "catalog-validation-v7", Version: "policy-v1"}}
	items, err := repo.ListValidationMaintenance(identityRepositoryContext(pool, tx), restoreTestTenant, q)
	if err != nil || len(items) != 1 || items[0].RevisionID != key[0] || items[0].EvidenceID != evidence[10] || !reflect.DeepEqual(items[0].Conditions, []string{"blocked"}) || items[0].Applicable || !selected.closed || !bindings.closed {
		t.Fatal(items, err, selected.closed, bindings.closed)
	}
	want := []any{restoreTestTenant, nil, "blocked", q.ObservedAt, 48, "catalog-validation-v7", "policy-v1", "", 21}
	revisionID, ok := key[0].(string)
	if !ok {
		t.Fatal("selected revision fixture must have a string ID", key[0])
	}
	if !reflect.DeepEqual(tx.queryArguments[0], want) || !reflect.DeepEqual(tx.queryArguments[1], []any{restoreTestTenant, []string{revisionID}}) || !reflect.DeepEqual(tx.queryRowArguments[0], []any{restoreTestTenant, []string{revisionID}, false}) {
		t.Fatal("lost tenant, window, current policy or exact hydration binding", tx.queryArguments, tx.queryRowArguments)
	}
	for _, sql := range append(tx.queryStatements, tx.queryRowStatements...) {
		if strings.Contains(sql, "inline_document") || strings.Contains(sql, "document_blobs") {
			t.Fatal("maintenance hydrated native bytes")
		}
	}
}

func TestValidationMaintenanceStorageRefusesPartialAndCorruptReads(t *testing.T) {
	for _, mode := range []string{"select-query", "select-scan", "select-iteration", "missing-evidence", "evidence-read", "bindings-query", "bindings-scan", "bindings-iteration", "bindings-missing", "bindings-oversized", "bindings-foreign", "evidence-digest", "partial-second-row"} {
		t.Run(mode, func(t *testing.T) {
			key, evidence, binding := maintenanceStorageRows(t)
			failure := errors.New("owned read failure")
			selected := &identityScriptedRows{values: [][]any{key}}
			bindings := &identityScriptedRows{values: [][]any{binding}}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow(evidence)}, queryResults: []identityQueryResult{{rows: selected}, {rows: bindings}}}
			switch mode {
			case "select-query":
				tx.queryResults[0] = identityQueryResult{err: failure}
			case "select-scan":
				selected.values = [][]any{{"short row"}}
			case "select-iteration":
				selected.err = failure
			case "missing-evidence":
				key[7] = true
			case "evidence-read":
				tx.queryRows = []pgx.Row{scriptedValueRow([]any{"short row"})}
			case "bindings-query":
				tx.queryResults[1] = identityQueryResult{err: failure}
			case "bindings-scan":
				bindings.values = [][]any{{"short row"}}
			case "bindings-iteration":
				bindings.err = failure
			case "bindings-missing":
				bindings.values = nil
			case "bindings-oversized":
				bindings.values = nil
				for range 17 {
					bindings.values = append(bindings.values, binding)
				}
			case "bindings-foreign":
				binding[0] = "foreign-revision"
			case "evidence-digest":
				evidence[13] = "sha256:" + strings.Repeat("f", 64)
			case "partial-second-row":
				second := append([]any(nil), key...)
				second[0] = "00000000-0000-7000-8000-000000000099"
				selected.values = append(selected.values, second)
				tx.queryRows = append(tx.queryRows, scriptedValueRow([]any{"short second row"}))
			}
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			q := catalog.ValidationMaintenanceRequest{State: "blocked", HorizonHours: 24, ObservedAt: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC), Limit: 100, Policy: catalog.ValidationMaintenancePolicy{Profile: "catalog-validation-v7", Version: "policy-v1"}}
			items, err := repo.ListValidationMaintenance(identityRepositoryContext(pool, tx), restoreTestTenant, q)
			if err == nil || len(items) != 0 {
				t.Fatal("failed read exposed a partial maintenance page", items, err)
			}
			if mode != "select-query" && !selected.closed {
				t.Fatal("selection rows leaked")
			}
			if (mode == "bindings-scan" || mode == "bindings-iteration" || mode == "bindings-missing" || mode == "bindings-oversized" || mode == "bindings-foreign") && !bindings.closed {
				t.Fatal("binding rows leaked")
			}
		})
	}
}

func TestValidationMaintenanceStorageBoundsBeforeIO(t *testing.T) {
	for _, limit := range []int{0, 102} {
		pool := &pgxpool.Pool{}
		repo, err := NewPostgresRepository(pool)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ListValidationMaintenance(t.Context(), restoreTestTenant, catalog.ValidationMaintenanceRequest{Limit: limit}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal(limit, err)
		}
	}
}
