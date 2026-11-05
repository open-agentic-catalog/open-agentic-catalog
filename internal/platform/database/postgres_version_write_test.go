package database

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func versionWriteParams(t *testing.T, revised bool) catalog.CreateVersionParams {
	t.Helper()
	revision, document := documentWriteFixture()
	versionID := "00000000-0000-7000-8000-000000000011"
	revision.ID, revision.ResourceVersionID = "00000000-0000-7000-8000-000000000012", versionID
	revision.RevisionNumber, revision.ContentSetDigest, revision.CreatedBy, revision.CreatedAt = 1, document.Digest, "author", document.CreatedAt
	revision.EvidenceSetID = "00000000-0000-7000-8000-000000000013"
	evidence := catalog.EvidenceSet{ID: revision.EvidenceSetID, TenantID: restoreTestTenant, SubjectDigest: revision.ContentSetDigest, Digest: document.Digest, State: catalog.EvidenceComplete, ValidationProfile: "catalog-validation-v7", PolicyVersion: "policy-v1", LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}, CreatedAt: document.CreatedAt}
	version := catalog.ResourceVersion{ID: versionID, TenantID: restoreTestTenant, ResourceID: "00000000-0000-7000-8000-000000000010", Version: "1.0.0", CandidateRevisionID: revision.ID, Revision: 1, CreatedAt: document.CreatedAt, UpdatedAt: document.CreatedAt}
	action := "resource_version.created"
	if revised {
		action = "resource_version.revised"
		version.Revision = 2
		revision.RevisionNumber = 2
	}
	audit, outbox := currentTestEvidence(t, action, "resource_version", version.ID, document.CreatedAt)
	return catalog.CreateVersionParams{Version: version, VersionRevision: revision, Documents: []catalog.DocumentBlob{document}, Evidence: evidence, ExpectedRevision: 1, Audit: audit, Outbox: outbox}
}

func currentVersionWriteRow(params catalog.CreateVersionParams, sealed string) pgx.Row {
	v := params.Version
	return scriptedValueRow([]any{v.ID, v.TenantID, v.ResourceID, v.Version, "previous", sealed, int64(1), v.CreatedAt, v.UpdatedAt})
}

func TestVersionWritesStopAndAbortAtEachFailedWrite(t *testing.T) {
	t.Parallel()
	for _, revised := range []bool{false, true} {
		stages := []string{"resource_versions", "document_blobs", "evidence_sets", "resource_version_revisions", "version_validation_runs", "representations", "UPDATE resource_versions", "audit_events", "outbox_events"}
		if revised {
			stages = stages[1:]
		}
		for failAt := -1; failAt < len(stages); failAt++ {
			t.Run(fmt.Sprintf("revised=%t/failure=%d", revised, failAt), func(t *testing.T) {
				params := versionWriteParams(t, revised)
				sentinel := errors.New("version write failed")
				tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{params.Version.ResourceID})}}
				if revised {
					tx.queryRows = append([]pgx.Row{currentVersionWriteRow(params, "")}, tx.queryRows...)
				}
				if failAt >= 0 {
					tx.execErrors = make([]error, failAt+1)
					tx.execErrors[failAt] = sentinel
				}
				pool := &pgxpool.Pool{}
				repo, err := NewPostgresRepository(pool)
				if err != nil {
					t.Fatal(err)
				}
				ctx := identityRepositoryContext(pool, tx)
				var version catalog.ResourceVersion
				var revision catalog.VersionRevision
				var evidence catalog.EvidenceSet
				if revised {
					version, revision, evidence, err = repo.CreateVersionRevision(ctx, params)
				} else {
					version, revision, evidence, err = repo.CreateVersion(ctx, params)
				}
				wantWrites := len(stages)
				if failAt >= 0 {
					wantWrites = failAt + 1
					if !errors.Is(err, sentinel) || version.ID != "" || revision.ID != "" || evidence.ID != "" || tx.rollbacks != 1 || commandTransactionFrom(ctx).failed == nil {
						t.Fatalf("failed write not aborted: version=%+v revision=%+v evidence=%+v error=%v rollbacks=%d", version, revision, evidence, err, tx.rollbacks)
					}
				} else if err != nil || !reflect.DeepEqual(version, params.Version) || !reflect.DeepEqual(revision, params.VersionRevision) || !reflect.DeepEqual(evidence, params.Evidence) || tx.rollbacks != 0 {
					t.Fatalf("successful write lost input: %v", err)
				}
				if len(tx.statements) != wantWrites || tx.commits != 0 {
					t.Fatalf("writes=%d commits=%d, want %d writes and command-owned commit", len(tx.statements), tx.commits, wantWrites)
				}
				for index, statement := range tx.statements {
					if !strings.Contains(statement, stages[index]) {
						t.Fatalf("write %d targeted unexpected storage: %s", index, statement)
					}
				}
				if failAt < 0 {
					updateIndex := len(stages) - 3
					if tx.arguments[updateIndex][0] != restoreTestTenant || tx.arguments[updateIndex][1] != params.Version.ID || tx.arguments[updateIndex][2] != params.VersionRevision.ID {
						t.Fatal("candidate update lost tenant/version/revision binding")
					}
				}
			})
		}
	}
}

type staleVersionWriteTx struct{ *identityScriptedTx }

func (tx staleVersionWriteTx) Exec(ctx context.Context, statement string, args ...any) (pgconn.CommandTag, error) {
	tag, err := tx.identityScriptedTx.Exec(ctx, statement, args...)
	if strings.HasPrefix(statement, "UPDATE resource_versions") {
		tag = pgconn.NewCommandTag("UPDATE 0")
	}
	return tag, err
}

func TestVersionRevisionWriteGuardsAndOptimisticConflict(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing version", "sealed", "missing resource", "stale revision", "skipped revision", "concurrent update", "invalid evidence"} {
		t.Run(mode, func(t *testing.T) {
			params := versionWriteParams(t, true)
			current := currentVersionWriteRow(params, "")
			resource := scriptedValueRow([]any{params.Version.ResourceID})
			code, writes := catalog.CodePreconditionFailed, 0
			switch mode {
			case "missing version":
				current = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				code = catalog.CodeNotFound
			case "sealed":
				current = currentVersionWriteRow(params, "sealed")
				code = catalog.CodeVersionSealed
			case "missing resource":
				resource = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				code = catalog.CodeNotFound
			case "stale revision":
				params.ExpectedRevision = 2
			case "skipped revision":
				params.Version.Revision = 3
			case "concurrent update":
				writes = 6
			case "invalid evidence":
				params.Evidence.ValidationProfile = "unsupported"
				code = catalog.CodeInvalidArgument
			}
			script := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{current, resource}}
			var tx pgx.Tx = script
			if mode == "concurrent update" {
				tx = staleVersionWriteTx{script}
			}
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			version, revision, evidence, err := repo.CreateVersionRevision(identityRepositoryContext(pool, tx), params)
			if catalog.ErrorCodeOf(err) != code || version.ID != "" || revision.ID != "" || evidence.ID != "" || len(script.statements) != writes || script.rollbacks != 1 {
				t.Fatalf("guard failed: %v writes=%d rollbacks=%d", err, len(script.statements), script.rollbacks)
			}
		})
	}
}

func TestSkillMetadataWritesPreserveHandoffsAndBatchInventory(t *testing.T) {
	t.Parallel()
	firstDigest, secondDigest := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	representation := catalog.Representation{
		ID: "representation",
		Package: &catalog.SkillPackageInspection{Files: []catalog.SkillPackageFile{
			{Path: "skill/SKILL.md", Size: 4, MediaType: "text/markdown", Digest: firstDigest},
			{Path: "skill/tool.sh", Size: 8, MediaType: "text/plain", Digest: secondDigest, Executable: true},
		}},
		Handoff: &catalog.SkillHandoff{
			Git: &catalog.GitHandoffReference{Repository: "https://example.com/skill.git", Commit: strings.Repeat("a", 40), Directory: "skill"},
			OCI: &catalog.OCIHandoffReference{Reference: "registry.example/skill@" + firstDigest},
		},
	}
	for failAt := -1; failAt < 3; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			sentinel := errors.New("metadata write failed")
			tx := &fakeCommandTx{}
			if failAt >= 0 {
				tx.execErrors = make([]error, failAt+1)
				tx.execErrors[failAt] = sentinel
			}
			err := insertSkillPackageMetadata(t.Context(), tx, restoreTestTenant, representation)
			if failAt >= 0 {
				if !errors.Is(err, sentinel) || len(tx.statements) != failAt+1 {
					t.Fatalf("write failure=%v count=%d", err, len(tx.statements))
				}
				return
			}
			if err != nil || len(tx.statements) != 3 {
				t.Fatalf("metadata writes=%d error=%v", len(tx.statements), err)
			}
			want := [][]any{{restoreTestTenant, representation.ID, representation.Handoff.Git.Repository, representation.Handoff.Git.Commit, "skill"}, {restoreTestTenant, representation.ID, representation.Handoff.OCI.Reference}, {restoreTestTenant, representation.ID, []string{"skill/SKILL.md", "skill/tool.sh"}, []int64{4, 8}, []string{"text/markdown", "text/plain"}, []string{firstDigest, secondDigest}, []bool{false, true}}}
			if !reflect.DeepEqual(tx.arguments, want) {
				t.Fatalf("metadata mapping=%v", tx.arguments)
			}
		})
	}
	for _, representation := range []catalog.Representation{{}, {Package: &catalog.SkillPackageInspection{}, Handoff: &catalog.SkillHandoff{}}} {
		tx := &fakeCommandTx{}
		if err := insertSkillPackageMetadata(t.Context(), tx, restoreTestTenant, representation); err != nil || len(tx.statements) != 0 {
			t.Fatal("absent package metadata performed writes")
		}
	}
}

func TestValidationRunInsertionPreservesExpiry(t *testing.T) {
	t.Parallel()
	for _, expires := range []time.Time{{}, time.Date(2026, 9, 10, 13, 0, 0, 0, time.UTC)} {
		evidence := versionWriteParams(t, false).Evidence
		evidence.ExpiresAt = expires
		tx := &fakeCommandTx{}
		if err := insertVersionValidationRun(t.Context(), tx, restoreTestTenant, "revision", 3, evidence); err != nil {
			t.Fatal(err)
		}
		var expected any
		if !expires.IsZero() {
			expected = expires
		}
		if !reflect.DeepEqual(tx.arguments, [][]any{{restoreTestTenant, "revision", 3, evidence.ID, expected, evidence.CreatedAt}}) {
			t.Fatal("validation run lost immutable identity or expiry")
		}
	}
}

func TestRevalidationWritesStopAtReadAndWriteFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "subject read", "resource read", "revision read", "run number", "revision children", "tenant mismatch", "digest mismatch", "unsupported evidence", "lock", "evidence insert", "run insert", "audit insert", "outbox insert"} {
		t.Run(mode, func(t *testing.T) {
			base := versionWriteParams(t, false)
			audit, outbox := currentTestEvidence(t, "version_revision.validated", "version_revision", base.VersionRevision.ID, base.Evidence.CreatedAt)
			params := catalog.CreateValidationRunParams{TenantID: restoreTestTenant, VersionRevisionID: base.VersionRevision.ID, Evidence: base.Evidence, Audit: audit, Outbox: outbox}
			revisionValues := versionRevisionReadValues()
			revisionValues[0], revisionValues[4], revisionValues[12] = base.VersionRevision.ID, base.Evidence.SubjectDigest, base.Evidence.SubjectDigest
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{base.Evidence.SubjectDigest, base.Version.ResourceID}), scriptedValueRow([]any{base.Version.ResourceID}), scriptedValueRow(revisionValues), scriptedValueRow([]any{3})}, queryResults: []identityQueryResult{{rows: &identityScriptedRows{}}, {rows: &identityScriptedRows{}}, {rows: &identityScriptedRows{}}}}
			sentinel := errors.New("revalidation dependency failed")
			writeCount, readCount := 5, 4
			code := catalog.ErrorCode("")
			switch mode {
			case "subject read":
				tx.queryRows[0] = scannerFunc(func(...any) error { return sentinel })
				writeCount, readCount = 0, 1
			case "resource read":
				tx.queryRows[1] = scannerFunc(func(...any) error { return sentinel })
				writeCount, readCount = 0, 2
			case "revision read":
				tx.queryRows[2] = scannerFunc(func(...any) error { return sentinel })
				writeCount, readCount = 1, 3
			case "run number":
				tx.queryRows[3] = scannerFunc(func(...any) error { return sentinel })
				writeCount = 1
			case "revision children":
				tx.queryResults[0].err = sentinel
				writeCount, readCount = 1, 3
			case "tenant mismatch":
				params.Evidence.TenantID = "other"
				code = catalog.CodeInvalidArgument
				writeCount, readCount = 1, 2
			case "digest mismatch":
				params.Evidence.SubjectDigest = "other"
				code = catalog.CodeInvalidArgument
				writeCount, readCount = 1, 2
			case "unsupported evidence":
				params.Evidence.ValidationProfile = "unsupported"
				code = catalog.CodeInvalidArgument
				writeCount, readCount = 1, 3
			default:
				for index, phase := range []string{"lock", "evidence insert", "run insert", "audit insert", "outbox insert"} {
					if mode == phase {
						tx.execErrors = make([]error, index+1)
						tx.execErrors[index] = sentinel
						writeCount = index + 1
						if index == 0 {
							readCount = 2
						}
					}
				}
			}
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			result, err := repo.CreateValidationRun(identityRepositoryContext(pool, tx), params)
			if mode == "success" {
				if err != nil || !reflect.DeepEqual(result, params.Evidence) || tx.rollbacks != 0 {
					t.Fatalf("successful revalidation=%+v error=%v", result, err)
				}
				if !reflect.DeepEqual(tx.arguments[2], []any{restoreTestTenant, params.VersionRevisionID, 3, params.Evidence.ID, nil, params.Evidence.CreatedAt}) {
					t.Fatal("revalidation run lost exact revision/evidence binding")
				}
			} else if err == nil || result.ID != "" || tx.rollbacks != 1 || code != "" && catalog.ErrorCodeOf(err) != code || code == "" && !errors.Is(err, sentinel) {
				t.Fatalf("failed revalidation=%+v error=%v rollback=%d", result, err, tx.rollbacks)
			}
			if len(tx.statements) != writeCount || len(tx.queryRowStatements) != readCount || tx.commits != 0 {
				t.Fatalf("unexpected follow-up work: writes=%d reads=%d commits=%d", len(tx.statements), len(tx.queryRowStatements), tx.commits)
			}
		})
	}
}
