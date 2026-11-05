package database

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func versionRevisionReadValues() []any {
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	return []any{
		"revision", restoreTestTenant, "version", 2, "content-digest", []byte(`[]`), "original-evidence", []byte(`null`), "author", at,
		"latest-evidence", restoreTestTenant, "content-digest", "evidence-digest", catalog.EvidenceComplete, []byte(`[]`), "catalog-validation-v7", "policy-v1",
		[]byte(`{"state":"not_assessed"}`), []byte(`{"state":"not_assessed"}`), []byte(`[]`), (*time.Time)(nil), at,
	}
}

func representationReadValues() []any {
	return []any{
		"revision", "representation", catalog.RepresentationRole("primary"), "application/json", "test-profile", "", []byte(`{"name":"example"}`), "content-digest", int64(18), "", "valid",
		[]byte(`[]`), []byte(`{"name":"example"}`), []byte(`{"extension":true}`), false, "", "", int64(0), 0, "", "", "", "", "", []byte(nil),
	}
}

func TestVersionRevisionDecoderPreservesEvidenceAndOptionalFields(t *testing.T) {
	t.Parallel()
	values := versionRevisionReadValues()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	expires := at.Add(time.Hour)
	values[21] = &expires
	values[7] = []byte(`{"observationId":"observation","sourceId":"source","sourceRevision":3}`)
	revision, evidence, err := scanVersionRevision(scriptedValueRow(values))
	if err != nil {
		t.Fatal(err)
	}
	wantRevision := catalog.VersionRevision{ID: "revision", TenantID: restoreTestTenant, ResourceVersionID: "version", RevisionNumber: 2, ContentSetDigest: "content-digest", Findings: []catalog.Finding{}, EvidenceSetID: "latest-evidence", Adoption: &catalog.AdoptionProvenance{ObservationID: "observation", SourceID: "source", SourceRevision: 3}, CreatedBy: "author", CreatedAt: at}
	wantEvidence := catalog.EvidenceSet{ID: "latest-evidence", TenantID: restoreTestTenant, SubjectDigest: "content-digest", Digest: "evidence-digest", State: catalog.EvidenceComplete, Findings: []catalog.Finding{}, ValidationProfile: "catalog-validation-v7", PolicyVersion: "policy-v1", LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}, ExternalRetrievals: []catalog.ExternalRetrieval{}, ExpiresAt: expires, CreatedAt: at}
	if !reflect.DeepEqual(revision, wantRevision) || !reflect.DeepEqual(evidence, wantEvidence) {
		t.Fatalf("decoded revision=%+v evidence=%+v", revision, evidence)
	}
	values = versionRevisionReadValues()
	values[7] = []byte(nil)
	revision, evidence, err = scanVersionRevision(scriptedValueRow(values))
	if err != nil || revision.Adoption != nil || !evidence.ExpiresAt.IsZero() {
		t.Fatalf("optional values were manufactured: %+v %+v %v", revision, evidence, err)
	}
}

func TestVersionRevisionDecoderRejectsEachCorruptJSONField(t *testing.T) {
	t.Parallel()
	for name, index := range map[string]int{"revision findings": 5, "adoption": 7, "evidence findings": 15, "rights": 18, "trust": 19, "retrievals": 20} {
		t.Run(name, func(t *testing.T) {
			values := versionRevisionReadValues()
			values[index] = []byte(`{`)
			revision, evidence, err := scanVersionRevision(scriptedValueRow(values))
			if err == nil || !reflect.DeepEqual(revision, catalog.VersionRevision{}) || !reflect.DeepEqual(evidence, catalog.EvidenceSet{}) {
				t.Fatalf("corrupt field returned partial data: %+v %+v %v", revision, evidence, err)
			}
		})
	}
	values := versionRevisionReadValues()
	values[20] = []byte(`[{"representationId":"representation"}]`)
	if _, _, err := scanVersionRevision(scriptedValueRow(values)); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("invalid retrieval integrity accepted: %v", err)
	}
	if _, _, err := scanVersionRevision(scannerFunc(func(...any) error { return pgx.ErrNoRows })); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing revision error=%v", err)
	}
}

func TestRepresentationDecoderPreservesDocumentsAndPackageAssessment(t *testing.T) {
	t.Parallel()
	values := representationReadValues()
	var revisionID string
	representation, err := scanRepresentation(scriptedValueRow(values), &revisionID)
	if err != nil {
		t.Fatal(err)
	}
	want := catalog.Representation{ID: "representation", Role: catalog.RepresentationRole("primary"), MediaType: "application/json", ProfileID: "test-profile", Data: json.RawMessage(`{"name":"example"}`), ContentDigest: "content-digest", Size: 18, ValidationState: "valid", Findings: []catalog.Finding{}, NormalizedFacts: map[string]any{"name": "example"}, UnknownProperties: map[string]any{"extension": true}}
	if revisionID != "revision" || !reflect.DeepEqual(representation, want) {
		t.Fatalf("decoded representation=%+v revision=%q", representation, revisionID)
	}
	values[3], values[6], values[14], values[15], values[16], values[17], values[18] = catalog.SkillPackageMediaZIP, []byte(`null`), true, "passed", "skill", int64(42), 1
	for index := 19; index < 24; index++ {
		values[index] = "not_assessed"
	}
	representation, err = scanRepresentation(scriptedValueRow(values), &revisionID)
	wantPackage := &catalog.SkillPackageInspection{StructureState: catalog.PackageAssessmentPassed, RootDirectory: "skill", ExpandedSize: 42, FileCount: 1, Assessments: catalog.SkillPackageAssessments{Malware: "not_assessed", Secrets: "not_assessed", License: "not_assessed", SBOM: "not_assessed", Provenance: "not_assessed"}}
	if err != nil || representation.Data != nil || !reflect.DeepEqual(representation.Package, wantPackage) {
		t.Fatalf("package=%+v data=%s error=%v", representation.Package, representation.Data, err)
	}
	for name, index := range map[string]int{"findings": 11, "facts": 12, "unknown properties": 13} {
		t.Run(name, func(t *testing.T) {
			values := representationReadValues()
			values[index] = []byte(`{`)
			if item, err := scanRepresentation(scriptedValueRow(values), &revisionID); err == nil || !reflect.DeepEqual(item, catalog.Representation{}) {
				t.Fatalf("malformed metadata accepted: %+v %v", item, err)
			}
		})
	}
}

func TestVersionLookupsBindTenantAndIdentifiers(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	want := catalog.ResourceVersion{ID: "version", TenantID: restoreTestTenant, ResourceID: "resource", Version: "1.2.3", CandidateRevisionID: "candidate", SealedRevisionID: "sealed", Revision: 4, CreatedAt: at, UpdatedAt: at.Add(time.Hour)}
	for _, byLabel := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			pool := &pgxpool.Pool{} // Identity only; the injected transaction handles every read.
			repository, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			row := scriptedValueRow([]any{want.ID, want.TenantID, want.ResourceID, want.Version, want.CandidateRevisionID, want.SealedRevisionID, want.Revision, want.CreatedAt, want.UpdatedAt})
			if missing {
				row = scannerFunc(func(...any) error { return pgx.ErrNoRows })
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{row}}
			ctx := identityRepositoryContext(pool, tx)
			var got catalog.ResourceVersion
			arguments := []any{restoreTestTenant, want.ID}
			if byLabel {
				got, err = repository.GetVersionByLabel(ctx, restoreTestTenant, want.ResourceID, want.Version)
				arguments = []any{restoreTestTenant, want.ResourceID, want.Version}
			} else {
				got, err = repository.GetVersion(ctx, restoreTestTenant, want.ID)
			}
			if missing {
				if catalog.ErrorCodeOf(err) != catalog.CodeNotFound || got.ID != "" {
					t.Fatalf("missing version=%+v error=%v", got, err)
				}
			} else if err != nil || got != want {
				t.Fatalf("version=%+v want=%+v error=%v", got, want, err)
			}
			if len(tx.queryRowArguments) != 1 || !reflect.DeepEqual(tx.queryRowArguments[0], arguments) {
				t.Fatalf("lookup arguments=%v", tx.queryRowArguments)
			}
		}
	}
}

func TestVersionRevisionReadStopsAtFailedPhase(t *testing.T) {
	t.Parallel()
	failure := errors.New("scripted revision read failure")
	for _, mode := range []string{"success", "revision", "representation query", "representation scan", "representation iteration", "package references", "package inventory", "evidence binding"} {
		t.Run(mode, func(t *testing.T) {
			values := versionRevisionReadValues()
			row := scriptedValueRow(values)
			representations := &identityScriptedRows{values: [][]any{representationReadValues()}}
			references, inventory := &identityScriptedRows{}, &identityScriptedRows{}
			results := []identityQueryResult{{rows: representations}, {rows: references}, {rows: inventory}}
			queries := 3
			switch mode {
			case "revision":
				row = scannerFunc(func(...any) error { return failure })
				queries = 0
			case "representation query":
				results[0].err = failure
				queries = 1
			case "representation scan":
				representations.values = [][]any{{}}
				queries = 1
			case "representation iteration":
				representations.err = failure
				queries = 1
			case "package references":
				results[1].err = failure
				queries = 2
			case "package inventory":
				results[2].err = failure
			case "evidence binding":
				values[16] = "unsupported-profile"
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{row}, queryResults: results}
			pool := &pgxpool.Pool{}
			repository, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			revision, evidence, err := repository.GetVersionRevision(identityRepositoryContext(pool, tx), restoreTestTenant, "revision")
			if mode == "success" {
				if err != nil || revision.ID != "revision" || revision.EvidenceSetID != "latest-evidence" || evidence.ID != "latest-evidence" || len(revision.Representations) != 1 || revision.Representations[0].ID != "representation" {
					t.Fatalf("revision=%+v evidence=%+v error=%v", revision, evidence, err)
				}
			} else {
				if err == nil || !reflect.DeepEqual(revision, catalog.VersionRevision{}) || !reflect.DeepEqual(evidence, catalog.EvidenceSet{}) {
					t.Fatalf("failed read returned data: %+v %+v %v", revision, evidence, err)
				}
				if mode != "representation scan" && mode != "evidence binding" && !errors.Is(err, failure) {
					t.Fatalf("lost driver error: %v", err)
				}
				if mode == "evidence binding" && catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
					t.Fatalf("wrong integrity error: %v", err)
				}
			}
			if len(tx.queryStatements) != queries || !reflect.DeepEqual(tx.queryRowArguments, [][]any{{restoreTestTenant, []string{"revision"}, false}}) {
				t.Fatalf("unexpected reads: %+v", tx)
			}
			for index := 0; index < queries; index++ {
				if !reflect.DeepEqual(tx.queryArguments[index], []any{restoreTestTenant, []string{"revision"}}) {
					t.Fatal("child read lost bounded tenant selection")
				}
				if results[index].err == nil && ![]*identityScriptedRows{representations, references, inventory}[index].closed {
					t.Fatalf("phase %d left rows open", index)
				}
			}
		})
	}
}

func TestVersionRevisionReadIncludesDeletedOnlyWhenRequested(t *testing.T) {
	t.Parallel()
	tx := &identityScriptedTx{queryRows: []pgx.Row{scriptedValueRow(versionRevisionReadValues())}, queryResults: []identityQueryResult{{rows: &identityScriptedRows{}}, {rows: &identityScriptedRows{}}, {rows: &identityScriptedRows{}}}}
	if _, _, err := readVersionRevisionIncludingDeleted(context.Background(), tx, restoreTestTenant, "revision", true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{restoreTestTenant, []string{"revision"}, true}}) {
		t.Fatal("recovery lookup lost its explicit include-deleted binding")
	}
}

func TestValidationEvidenceLookupDecodesAndRejectsCorruption(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "expiry", "missing", "findings", "rights", "trust", "retrieval JSON", "retrieval integrity"} {
		t.Run(mode, func(t *testing.T) {
			base := versionRevisionReadValues()
			values := append([]any(nil), base[10:]...)
			at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
			expires := at.Add(time.Hour)
			code := catalog.ErrorCode("")
			switch mode {
			case "expiry":
				values[11] = &expires
			case "findings":
				values[5] = []byte(`{`)
			case "rights":
				values[8] = []byte(`{`)
			case "trust":
				values[9] = []byte(`{`)
			case "retrieval JSON":
				values[10] = []byte(`{`)
			case "retrieval integrity":
				values[10] = []byte(`[{}]`)
				code = catalog.CodeDependencyUnavailable
			case "missing":
				code = catalog.CodeNotFound
			}
			row := scriptedValueRow(values)
			if mode == "missing" {
				row = scannerFunc(func(...any) error { return pgx.ErrNoRows })
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{row}}
			pool := &pgxpool.Pool{}
			repository, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := repository.GetValidationEvidence(identityRepositoryContext(pool, tx), restoreTestTenant, "revision", "latest-evidence")
			if mode == "valid" || mode == "expiry" {
				want := catalog.EvidenceSet{ID: "latest-evidence", TenantID: restoreTestTenant, SubjectDigest: "content-digest", Digest: "evidence-digest", State: catalog.EvidenceComplete, Findings: []catalog.Finding{}, ValidationProfile: "catalog-validation-v7", PolicyVersion: "policy-v1", LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}, ExternalRetrievals: []catalog.ExternalRetrieval{}, CreatedAt: at}
				if mode == "expiry" {
					want.ExpiresAt = expires
				}
				if err != nil || !reflect.DeepEqual(evidence, want) {
					t.Fatalf("evidence=%+v error=%v", evidence, err)
				}
			} else if err == nil || !reflect.DeepEqual(evidence, catalog.EvidenceSet{}) || code != "" && catalog.ErrorCodeOf(err) != code {
				t.Fatalf("corrupt evidence=%+v error=%v", evidence, err)
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{restoreTestTenant, "revision", "latest-evidence"}}) {
				t.Fatalf("lookup binding=%v", tx.queryRowArguments)
			}
		})
	}
}
