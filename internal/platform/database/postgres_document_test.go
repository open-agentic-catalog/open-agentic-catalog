package database

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Scripted rows test integrity and error classification, not SQL execution or
// relationship constraints. The zero pool is only the fake transaction's identity.
func TestDocumentReadRejectsCorruptionAndInactiveContent(t *testing.T) {
	t.Parallel()
	body := []byte(`{"name":"private document"}`)
	sum := sha256.Sum256(body)
	documentID := hex.EncodeToString(sum[:])
	for _, test := range []struct {
		name     string
		mutate   func(*catalog.DocumentBlob)
		inactive bool
		code     catalog.ErrorCode
	}{
		{name: "valid"},
		{name: "modified content", mutate: func(d *catalog.DocumentBlob) { d.Content = []byte(`{"name":"changed document"}`) }, code: catalog.CodeDependencyUnavailable},
		{name: "wrong content address", mutate: func(d *catalog.DocumentBlob) { d.ID = "wrong" }, code: catalog.CodeDependencyUnavailable},
		{name: "wrong digest", mutate: func(d *catalog.DocumentBlob) { d.Digest = "sha256:wrong" }, code: catalog.CodeDependencyUnavailable},
		{name: "wrong size", mutate: func(d *catalog.DocumentBlob) { d.Size++ }, code: catalog.CodeDependencyUnavailable},
		{name: "inactive document", inactive: true, code: catalog.CodeNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			document := catalog.DocumentBlob{ID: documentID, TenantID: paginationTestTenantID, Digest: "sha256:" + documentID, MediaType: "application/json", Size: int64(len(body)), Content: body, CreatedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
			if test.mutate != nil {
				test.mutate(&document)
			}
			pool := &pgxpool.Pool{}
			repository, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			row := &identityScriptedRows{index: 1, values: [][]any{{document.ID, document.TenantID, document.Digest, document.MediaType, document.Size, document.Content, document.CreatedAt, !test.inactive}}}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{row}}
			got, err := repository.GetDocument(identityRepositoryContext(pool, tx), paginationTestTenantID, documentID)
			if test.code == "" {
				if err != nil || !reflect.DeepEqual(got, document) {
					t.Fatalf("document = %#v, %v, want %#v", got, err, document)
				}
			} else if catalog.ErrorCodeOf(err) != test.code || !reflect.DeepEqual(got, catalog.DocumentBlob{}) {
				t.Fatalf("invalid document disclosed content or lost error code: %#v, %v", got, err)
			}
			if errors.Is(err, catalog.ErrInactiveDocument) != test.inactive {
				t.Fatalf("inactive marker = %v, want %t", err, test.inactive)
			}
			requireTenantBoundQuery(t, tx.queryRowStatements, tx.queryRowArguments)
			if !reflect.DeepEqual(tx.queryRowArguments[0], []any{paginationTestTenantID, documentID}) {
				t.Fatalf("document query arguments = %#v", tx.queryRowArguments)
			}
		})
	}
}

func TestMissingDocumentReadClassifiesReferencesAndDriverErrors(t *testing.T) {
	t.Parallel()
	failure := errors.New("scripted driver failure")
	for _, test := range []struct {
		name         string
		readError    error
		inspectError error
		referenced   bool
		active       bool
		code         catalog.ErrorCode
		cause        error
	}{
		{"missing and unreferenced", pgx.ErrNoRows, nil, false, false, catalog.CodeNotFound, nil},
		{"missing but actively referenced", pgx.ErrNoRows, nil, true, true, catalog.CodeNotFound, nil},
		{"missing and inactive", pgx.ErrNoRows, nil, true, false, catalog.CodeNotFound, catalog.ErrInactiveDocument},
		{"document query fails", failure, nil, false, false, catalog.CodeDependencyUnavailable, failure},
		{"reference query fails", pgx.ErrNoRows, failure, false, false, catalog.CodeDependencyUnavailable, failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			pool := &pgxpool.Pool{}
			repository, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			rows := []pgx.Row{scannerFunc(func(...any) error { return test.readError })}
			wantQueries := 1
			if errors.Is(test.readError, pgx.ErrNoRows) {
				wantQueries++
				rows = append(rows, scannerFunc(func(destinations ...any) error {
					setScanDestination(t, destinations, 0, test.referenced)
					setScanDestination(t, destinations, 1, test.active)
					return test.inspectError
				}))
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: rows}
			got, err := repository.GetDocument(identityRepositoryContext(pool, tx), paginationTestTenantID, "document-id")
			if catalog.ErrorCodeOf(err) != test.code || test.cause != nil && !errors.Is(err, test.cause) || !reflect.DeepEqual(got, catalog.DocumentBlob{}) {
				t.Fatalf("missing document = %#v, %v, want code=%s cause=%v", got, err, test.code, test.cause)
			}
			if errors.Is(err, catalog.ErrInactiveDocument) != errors.Is(test.cause, catalog.ErrInactiveDocument) {
				t.Fatalf("incorrect inactive-document classification: %v", err)
			}
			if len(tx.queryRowArguments) != wantQueries {
				t.Fatalf("queries = %d, want %d", len(tx.queryRowArguments), wantQueries)
			}
			for index, arguments := range tx.queryRowArguments {
				requireTenantBoundQuery(t, tx.queryRowStatements[index:index+1], tx.queryRowArguments[index:index+1])
				if !reflect.DeepEqual(arguments, []any{paginationTestTenantID, "document-id"}) {
					t.Fatalf("query %d lost document binding: %#v", index, arguments)
				}
			}
		})
	}
}
