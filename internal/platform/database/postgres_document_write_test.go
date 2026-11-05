package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type existingDocumentTx struct{ *identityScriptedTx }

func (tx existingDocumentTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	_, err := tx.identityScriptedTx.Exec(ctx, sql, args...)
	return pgconn.NewCommandTag("INSERT 0 0"), err
}

func documentWriteFixture() (catalog.VersionRevision, catalog.DocumentBlob) {
	body := []byte(`{"name":"example"}`)
	sum := sha256.Sum256(body)
	id := hex.EncodeToString(sum[:])
	document := catalog.DocumentBlob{ID: id, TenantID: restoreTestTenant, Digest: "sha256:" + id, MediaType: "application/json", Size: int64(len(body)), Content: body, CreatedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
	revision := catalog.VersionRevision{TenantID: restoreTestTenant, Representations: []catalog.Representation{{ID: "representation", ArtifactID: id, MediaType: document.MediaType, ContentDigest: document.Digest, Size: document.Size}}}
	return revision, document
}

func TestDocumentInsertRejectsInvalidBindingsBeforeWriting(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*catalog.VersionRevision, *[]catalog.DocumentBlob)
		code   catalog.ErrorCode
	}{
		{"corrupt content", func(_ *catalog.VersionRevision, d *[]catalog.DocumentBlob) { (*d)[0].Content = []byte("modified") }, catalog.CodeInvalidArgument},
		{"wrong tenant", func(_ *catalog.VersionRevision, d *[]catalog.DocumentBlob) {
			(*d)[0].TenantID = "00000000-0000-7000-8000-000000000002"
		}, catalog.CodeInvalidArgument},
		{"missing document", func(_ *catalog.VersionRevision, d *[]catalog.DocumentBlob) { *d = nil }, catalog.CodeInvalidArgument},
		{"unreferenced document", func(r *catalog.VersionRevision, _ *[]catalog.DocumentBlob) { r.Representations[0].ArtifactID = "" }, catalog.CodeInvalidArgument},
		{"media mismatch", func(r *catalog.VersionRevision, _ *[]catalog.DocumentBlob) {
			r.Representations[0].MediaType = "text/plain"
		}, catalog.CodeInvalidArgument},
		{"digest mismatch", func(r *catalog.VersionRevision, _ *[]catalog.DocumentBlob) {
			r.Representations[0].ContentDigest = "different"
		}, catalog.CodeInvalidArgument},
		{"size mismatch", func(r *catalog.VersionRevision, _ *[]catalog.DocumentBlob) { r.Representations[0].Size++ }, catalog.CodeInvalidArgument},
		{"conflicting duplicates", func(_ *catalog.VersionRevision, d *[]catalog.DocumentBlob) {
			duplicate := (*d)[0]
			duplicate.MediaType = "text/plain"
			*d = append(*d, duplicate)
		}, catalog.CodeConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			revision, document := documentWriteFixture()
			documents := []catalog.DocumentBlob{document}
			tc.mutate(&revision, &documents)
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
			ids, err := insertDocumentBlobs(t.Context(), tx, revision, documents)
			if catalog.ErrorCodeOf(err) != tc.code || ids != nil || len(tx.statements) != 0 || len(tx.queryRowStatements) != 0 {
				t.Fatalf("invalid binding reached storage: ids=%v error=%v tx=%+v", ids, err, tx)
			}
		})
	}
}

func TestDocumentInsertAndExistingContentComparison(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"new", "same existing", "different media", "different digest", "different size", "different bytes", "insert failure", "existing read failure"} {
		t.Run(mode, func(t *testing.T) {
			revision, document := documentWriteFixture()
			values := []any{document.MediaType, document.Digest, document.Size, document.Content}
			failure := errors.New("document driver failure")
			script := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
			var tx pgx.Tx = existingDocumentTx{script}
			wantCode := catalog.ErrorCode("")
			switch mode {
			case "new":
				tx = script
			case "different media":
				values[0] = "text/plain"
				wantCode = catalog.CodeConflict
			case "different digest":
				values[1] = "other"
				wantCode = catalog.CodeConflict
			case "different size":
				values[2] = document.Size + 1
				wantCode = catalog.CodeConflict
			case "different bytes":
				values[3] = []byte("other")
				wantCode = catalog.CodeConflict
			case "insert failure":
				script.execErrors = []error{failure}
			}
			script.queryRows = []pgx.Row{scriptedValueRow(values)}
			if mode == "existing read failure" {
				script.queryRows = []pgx.Row{scannerFunc(func(...any) error { return failure })}
			}
			ids, err := insertDocumentBlobs(t.Context(), tx, revision, []catalog.DocumentBlob{document})
			switch {
			case mode == "insert failure" || mode == "existing read failure":
				if !errors.Is(err, failure) || ids != nil {
					t.Fatalf("driver failure lost: ids=%v error=%v", ids, err)
				}
			case wantCode != "":
				if catalog.ErrorCodeOf(err) != wantCode || ids != nil {
					t.Fatalf("conflicting content accepted: ids=%v error=%v", ids, err)
				}
			default:
				if err != nil || !reflect.DeepEqual(ids, map[string]struct{}{document.ID: {}}) {
					t.Fatalf("document insert result=%v error=%v", ids, err)
				}
			}
			if len(script.arguments) != 1 || !reflect.DeepEqual(script.arguments[0], []any{document.TenantID, document.ID, document.MediaType, document.Digest, document.Size, document.Content, document.CreatedAt}) {
				t.Fatalf("insert lost immutable fields: %v", script.arguments)
			}
			if mode == "new" || mode == "insert failure" {
				if len(script.queryRowStatements) != 0 {
					t.Fatal("unnecessary existing-content read")
				}
			} else if !reflect.DeepEqual(script.queryRowArguments, [][]any{{document.TenantID, document.ID}}) {
				t.Fatal("existing-content lookup lost tenant or digest binding")
			}
		})
	}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	if ids, err := insertDocumentBlobs(t.Context(), tx, catalog.VersionRevision{TenantID: restoreTestTenant, Representations: []catalog.Representation{{Data: []byte(`{}`)}}}, nil); err != nil || len(ids) != 0 || len(tx.statements) != 0 {
		t.Fatalf("inline-only revision performed storage work: %v %v", ids, err)
	}
}

func TestDocumentInsertEnforcesAggregateRevisionByteBudget(t *testing.T) {
	for _, over := range []bool{false, true} {
		name := "exact budget"
		if over {
			name = "one byte over"
		}
		t.Run(name, func(t *testing.T) {
			revision, base := documentWriteFixture()
			revision.Representations = nil
			documents := make([]catalog.DocumentBlob, 0, 2)
			for index := range 2 {
				length := catalog.MaxCatalogDocumentBytes / 2
				if index == 1 && over {
					length++
				}
				document := base
				document.MediaType = "text/plain"
				document.Content = bytes.Repeat([]byte{byte('a' + index)}, length)
				sum := sha256.Sum256(document.Content)
				document.ID = hex.EncodeToString(sum[:])
				document.Digest = "sha256:" + document.ID
				document.Size = int64(length)
				if err := catalog.ValidateDocumentBlob(revision.TenantID, document); err != nil {
					t.Fatalf("aggregate fixture has invalid individual document: %v", err)
				}
				documents = append(documents, document)
				revision.Representations = append(revision.Representations, catalog.Representation{ArtifactID: document.ID, MediaType: document.MediaType, ContentDigest: document.Digest, Size: document.Size})
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
			ids, err := insertDocumentBlobs(t.Context(), tx, revision, documents)
			if over {
				if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || ids != nil || len(tx.statements) != 0 || len(tx.queryRowStatements) != 0 {
					t.Fatalf("aggregate overflow reached storage: ids=%v err=%v writes=%d", ids, err, len(tx.statements))
				}
				return
			}
			want := map[string]struct{}{documents[0].ID: {}, documents[1].ID: {}}
			if err != nil || !reflect.DeepEqual(ids, want) || len(tx.statements) != 2 || len(tx.queryRowStatements) != 0 {
				t.Fatalf("exact aggregate budget rejected: ids=%v err=%v writes=%d", ids, err, len(tx.statements))
			}
			for index, document := range documents {
				wantArgs := []any{document.TenantID, document.ID, document.MediaType, document.Digest, document.Size, document.Content, document.CreatedAt}
				if !reflect.DeepEqual(tx.arguments[index], wantArgs) {
					t.Fatal("exact-budget document lost immutable storage binding")
				}
			}
		})
	}
}
