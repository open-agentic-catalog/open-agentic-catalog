package database

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPostgresDraftDeletionRetainsEvidenceAndRejectsDependencies(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	createdAt := now.Add(-time.Hour)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	resourceRow := func(revision int64) pgx.Row {
		return scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, "00000000-0000-7000-8000-000000000023")
			setScanDestination(t, destinations, 1, tenantID)
			setScanDestination(t, destinations, 2, "namespace")
			setScanDestination(t, destinations, 3, "urn:air:catalog.example.com:stable:draft")
			setScanDestination(t, destinations, 4, catalog.KindAgent)
			setScanDestination(t, destinations, 5, "creator")
			setScanDestination(t, destinations, 6, revision)
			setScanDestination(t, destinations, 7, createdAt)
			setScanDestination(t, destinations, 8, createdAt)
			return nil
		})
	}
	dependenciesRow := func(adopted, reviewed, released bool) pgx.Row {
		return scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, adopted)
			setScanDestination(t, destinations, 1, reviewed)
			setScanDestination(t, destinations, 2, released)
			return nil
		})
	}
	record, outbox := currentTestEvidence(t, "resource.deleted", "resource", "00000000-0000-7000-8000-000000000023", now)
	params := catalog.DeleteResourceParams{
		TenantID:         tenantID,
		ResourceID:       "00000000-0000-7000-8000-000000000023",
		ExpectedRevision: 1,
		Actor:            "creator",
		Reason:           "discard unused draft",
		At:               now,
		Audit:            record,
		Outbox:           outbox,
	}

	successTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{resourceRow(1), dependenciesRow(false, false, false)}}
	deleted, err := repository.DeleteResource(identityRepositoryContext(pool, successTx), params)
	if err != nil || deleted.DeletedAt == nil || !deleted.DeletedAt.Equal(now) || deleted.DeletedBy != params.Actor || deleted.DeleteReason != params.Reason || deleted.Revision != 2 || len(successTx.statements) != 3 {
		t.Fatalf("deleted Resource = %#v, statements=%d, error=%v", deleted, len(successTx.statements), err)
	}

	staleTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{resourceRow(2)}}
	if _, err := repository.DeleteResource(identityRepositoryContext(pool, staleTx), params); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || len(staleTx.statements) != 0 {
		t.Fatalf("stale Resource deletion = %v, statements=%d", err, len(staleTx.statements))
	}

	for name, dependency := range map[string][3]bool{
		"adoption": {true, false, false},
		"review":   {false, true, false},
		"release":  {false, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{resourceRow(1), dependenciesRow(dependency[0], dependency[1], dependency[2])}}
			if _, err := repository.DeleteResource(identityRepositoryContext(pool, tx), params); catalog.ErrorCodeOf(err) != catalog.CodeConflict || len(tx.statements) != 0 {
				t.Fatalf("dependent Resource deletion = %v, statements=%d", err, len(tx.statements))
			}
		})
	}
}

func TestPostgresResourceReadsHydrateOnlyRepositoryResults(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	created := catalog.Resource{ID: "00000000-0000-7000-8000-000000000024", TenantID: tenantID, NamespaceID: "namespace", CanonicalIdentifier: "urn:air:catalog.example.com:stable:created", Kind: catalog.KindAgent, CreatedBy: "creator", Revision: 1, CreatedAt: now, UpdatedAt: now}
	record, outbox := currentTestEvidence(t, "resource.created", "resource", created.ID, now)
	createParams := catalog.CreateResourceParams{Resource: created, Audit: record, Outbox: outbox}
	createTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	createdResult, err := repository.CreateResource(identityRepositoryContext(pool, createTx), createParams)
	if err != nil || createdResult.ID != created.ID || len(createTx.statements) != 3 {
		t.Fatalf("created Resource = %#v statements=%d, %v", createdResult, len(createTx.statements), err)
	}
	failedCreateTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{execErrors: []error{errors.New("insert failed")}}}
	if _, err := repository.CreateResource(identityRepositoryContext(pool, failedCreateTx), createParams); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("failed Resource create = %v", err)
	}
	resourceValues := []any{"00000000-0000-7000-8000-000000000023", tenantID, "namespace", "urn:air:catalog.example.com:stable:resource", catalog.KindAgent, "creator", int64(1), now, now}
	resourceRow := scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, "00000000-0000-7000-8000-000000000023")
		setScanDestination(t, destinations, 1, tenantID)
		setScanDestination(t, destinations, 2, "namespace")
		setScanDestination(t, destinations, 3, "urn:air:catalog.example.com:stable:resource")
		setScanDestination(t, destinations, 4, catalog.KindAgent)
		setScanDestination(t, destinations, 5, "creator")
		setScanDestination(t, destinations, 6, int64(1))
		setScanDestination(t, destinations, 7, now)
		setScanDestination(t, destinations, 8, now)
		return nil
	})
	getTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{resourceRow}}
	resource, err := repository.GetResource(identityRepositoryContext(pool, getTx), tenantID, "00000000-0000-7000-8000-000000000023")
	if err != nil || resource.ID != "00000000-0000-7000-8000-000000000023" || resource.Kind != catalog.KindAgent {
		t.Fatalf("Resource read = %#v, %v", resource, err)
	}
	identifierTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{resourceRow}}
	resource, err = repository.GetResourceByIdentifier(identityRepositoryContext(pool, identifierTx), tenantID, resource.CanonicalIdentifier)
	if err != nil || resource.ID != "00000000-0000-7000-8000-000000000023" {
		t.Fatalf("Resource identifier read = %#v, %v", resource, err)
	}
	rows := &identityScriptedRows{values: [][]any{resourceValues}}
	listTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: rows}}}
	resources, err := repository.ListResources(identityRepositoryContext(pool, listTx), tenantID, catalog.ResourceListRequest{Kind: catalog.KindAgent, Limit: 100})
	if err != nil || len(resources) != 1 || resources[0].CanonicalIdentifier != resource.CanonicalIdentifier || !rows.closed {
		t.Fatalf("Resource list = %#v rowsClosed=%t, %v", resources, rows.closed, err)
	}
	lockTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, "00000000-0000-7000-8000-000000000023")
		return nil
	})}}
	if err := lockActiveResource(t.Context(), lockTx, tenantID, "00000000-0000-7000-8000-000000000023"); err != nil {
		t.Fatalf("active Resource lock = %v", err)
	}
	missingLockTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(...any) error { return pgx.ErrNoRows })}}
	if err := lockActiveResource(t.Context(), missingLockTx, tenantID, "missing"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing Resource lock = %v", err)
	}
}

func TestResourceExactFilterBindsSQLBeforeLimit(t *testing.T) {
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	rows := &identityScriptedRows{}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: rows}}}
	const identifier = "Café:e\u0301:🙂"
	_, err = repository.ListResources(identityRepositoryContext(pool, tx), paginationTestTenantID, catalog.ResourceListRequest{CanonicalIdentifier: identifier, Kind: catalog.KindAgent, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.queryStatements) != 1 || len(tx.queryArguments) != 1 {
		t.Fatal("exact lookup performed per-row reads")
	}
	statement := tx.queryStatements[0]
	if !strings.Contains(statement, "AND canonical_identifier=$6") || !strings.Contains(statement, "ORDER BY updated_at DESC,id DESC LIMIT $5") || !strings.Contains(statement, "deleted_at IS NULL") {
		t.Fatal("exact lookup lost SQL bound or ordering", statement)
	}
	args := tx.queryArguments[0]
	if len(args) != 6 || args[0] != paginationTestTenantID || args[1] != catalog.KindAgent || args[4] != 2 || args[5] != identifier || !rows.closed {
		t.Fatal("lookup lost exact bound inputs", args)
	}
}
