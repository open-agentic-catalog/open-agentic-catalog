package database

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type identityQueryResult struct {
	rows pgx.Rows
	err  error
}

type identityScriptedTx struct {
	authorizationLocks     []string
	authorizationLockArgs  [][]any
	authorizationLockError error
	*fakeCommandTx
	queryRows          []pgx.Row
	queryResults       []identityQueryResult
	queryRowStatements []string
	queryRowArguments  [][]any
	queryStatements    []string
	queryArguments     [][]any
}

func (transaction *identityScriptedTx) QueryRow(_ context.Context, statement string, arguments ...any) pgx.Row {
	if strings.Contains(statement, "FROM tenant_authorization_state") && strings.Contains(statement, "FOR NO KEY UPDATE") {
		transaction.authorizationLocks = append(transaction.authorizationLocks, statement)
		transaction.authorizationLockArgs = append(transaction.authorizationLockArgs, arguments)
		if transaction.authorizationLockError != nil {
			return scannerFunc(func(...any) error { return transaction.authorizationLockError })
		}
		return scriptedValueRow([]any{int64(1)})
	}
	transaction.queryRowStatements = append(transaction.queryRowStatements, statement)
	transaction.queryRowArguments = append(transaction.queryRowArguments, append([]any(nil), arguments...))
	if len(transaction.queryRows) == 0 {
		return scannerFunc(func(...any) error { return errors.New("unexpected query row") })
	}
	row := transaction.queryRows[0]
	transaction.queryRows = transaction.queryRows[1:]
	return row
}

func (transaction *identityScriptedTx) Query(_ context.Context, statement string, arguments ...any) (pgx.Rows, error) {
	transaction.queryStatements = append(transaction.queryStatements, statement)
	transaction.queryArguments = append(transaction.queryArguments, append([]any(nil), arguments...))
	if len(transaction.queryResults) == 0 {
		return nil, errors.New("unexpected query")
	}
	result := transaction.queryResults[0]
	transaction.queryResults = transaction.queryResults[1:]
	return result.rows, result.err
}

type identityScriptedRows struct {
	pgx.Rows
	values [][]any
	index  int
	err    error
	closed bool
}

func (rows *identityScriptedRows) Next() bool {
	if rows.index >= len(rows.values) {
		return false
	}
	rows.index++
	return true
}

func (rows *identityScriptedRows) Scan(destinations ...any) error {
	if rows.index == 0 || rows.index > len(rows.values) {
		return errors.New("scan called without a current row")
	}
	values := rows.values[rows.index-1]
	if len(values) != len(destinations) {
		return errors.New("scan destination count does not match")
	}
	for index, value := range values {
		destination := reflect.ValueOf(destinations[index])
		if destination.Kind() != reflect.Pointer || destination.IsNil() {
			return errors.New("scan destination is not a pointer")
		}
		destination.Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

func (rows *identityScriptedRows) Err() error { return rows.err }

func (rows *identityScriptedRows) Close() { rows.closed = true }

// scriptedValueRow exposes one row through the same decoder as scripted result
// sets. It performs no connection or query and contains only test-owned values.
func scriptedValueRow(values []any) pgx.Row {
	return &identityScriptedRows{values: [][]any{values}, index: 1}
}

func identityRepositoryContext(pool *pgxpool.Pool, transaction pgx.Tx) context.Context {
	command := &commandTransaction{pool: pool, tenantID: "00000000-0000-7000-8000-000000000001", tx: transaction, options: pgx.TxOptions{IsoLevel: pgx.Serializable}}
	return withCommandTransaction(context.Background(), command)
}

func catalogPublisherRow(t *testing.T, publisher catalog.Publisher) pgx.Row {
	t.Helper()
	return scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, publisher.ID)
		setScanDestination(t, destinations, 1, publisher.TenantID)
		setScanDestination(t, destinations, 2, publisher.DisplayName)
		setScanDestination(t, destinations, 3, publisher.Domains)
		setScanDestination(t, destinations, 4, publisher.ApprovedPublicationDomains)
		setScanDestination(t, destinations, 5, publisher.Revision)
		setScanDestination(t, destinations, 6, publisher.CreatedAt)
		setScanDestination(t, destinations, 7, publisher.UpdatedAt)
		return nil
	})
}

func TestPostgresIdentityRepositoryWritesEvidenceInTheCommandTransaction(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	audit, outbox := currentTestEvidence(t, "namespace.created", "namespace", "00000000-0000-7000-8000-000000000021", now)

	namespace := catalog.Namespace{ID: "00000000-0000-7000-8000-000000000021", TenantID: tenantID, Slug: "default", DisplayName: "Default", Revision: 1, CreatedAt: now, UpdatedAt: now}
	namespaceTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	createdNamespace, err := repository.CreateNamespace(identityRepositoryContext(pool, namespaceTx), namespace, audit, outbox)
	if err != nil || createdNamespace.ID != namespace.ID || len(namespaceTx.statements) != 3 {
		t.Fatalf("created namespace = %#v, statements=%d, error=%v", createdNamespace, len(namespaceTx.statements), err)
	}
	failingNamespaceTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{execErrors: []error{errors.New("insert failed")}}}
	if _, err := repository.CreateNamespace(identityRepositoryContext(pool, failingNamespaceTx), namespace, audit, outbox); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("namespace insert failure = %v", err)
	}

	publisher := catalog.Publisher{ID: "00000000-0000-7000-8000-000000000022", TenantID: tenantID, DisplayName: "Publisher", Domains: []string{"catalog.example.com"}, ApprovedPublicationDomains: []string{}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	tenantLockRow := func() pgx.Row {
		return scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, tenantID)
			return nil
		})
	}
	publisherTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{tenantLockRow(), scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, false)
		return nil
	})}}
	audit, outbox = currentTestEvidence(t, "publisher.created", "publisher", publisher.ID, now)
	createdPublisher, err := repository.CreatePublisher(identityRepositoryContext(pool, publisherTx), publisher, audit, outbox)
	if err != nil || createdPublisher.ID != publisher.ID || len(publisherTx.statements) != 4 {
		t.Fatalf("created publisher = %#v, statements=%d, error=%v", createdPublisher, len(publisherTx.statements), err)
	}
	conflictTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{tenantLockRow(), scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, true)
		return nil
	})}}
	if _, err := repository.CreatePublisher(identityRepositoryContext(pool, conflictTx), publisher, audit, outbox); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("publisher domain conflict = %v", err)
	}
}

func TestPostgresPublisherPublicationDomainCommandsAreExactAndAtomic(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	audit, outbox := currentTestEvidence(t, "publisher.publication_domain_approved", "publisher", "00000000-0000-7000-8000-000000000022", now)
	publisher := catalog.Publisher{ID: "00000000-0000-7000-8000-000000000022", TenantID: tenantID, DisplayName: "Publisher", Domains: []string{"catalog.example.com", "other.example.com"}, ApprovedPublicationDomains: []string{}, Revision: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}

	approveTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{catalogPublisherRow(t, publisher)}}
	approved, err := repository.ApprovePublisherPublicationDomain(identityRepositoryContext(pool, approveTx), catalog.PublisherDomainApproval{TenantID: tenantID, PublisherID: publisher.ID, Domain: "catalog.example.com", ExpectedRevision: publisher.Revision, Audit: audit, Outbox: outbox})
	if err != nil || approved.Revision != 2 || len(approved.ApprovedPublicationDomains) != 1 || approved.ApprovedPublicationDomains[0] != "catalog.example.com" || !approved.UpdatedAt.Equal(now) || len(approveTx.statements) != 3 {
		t.Fatalf("approved publisher = %#v, statements=%d, error=%v", approved, len(approveTx.statements), err)
	}
	staleTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{catalogPublisherRow(t, publisher)}}
	if _, err := repository.ApprovePublisherPublicationDomain(identityRepositoryContext(pool, staleTx), catalog.PublisherDomainApproval{TenantID: tenantID, PublisherID: publisher.ID, Domain: "catalog.example.com", ExpectedRevision: 2, Audit: audit, Outbox: outbox}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale publication-domain approval = %v", err)
	}
	undeclaredTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{catalogPublisherRow(t, publisher)}}
	if _, err := repository.ApprovePublisherPublicationDomain(identityRepositoryContext(pool, undeclaredTx), catalog.PublisherDomainApproval{TenantID: tenantID, PublisherID: publisher.ID, Domain: "undeclared.example.com", ExpectedRevision: publisher.Revision, Audit: audit, Outbox: outbox}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("undeclared publication-domain approval = %v", err)
	}

	approvedPublisher := approved
	approvedPublisher.ApprovedPublicationDomains = []string{"catalog.example.com", "other.example.com"}
	releaseRows := &identityScriptedRows{values: [][]any{
		{"release-matching", "urn:air:catalog.example.com:default:matching"},
		{"release-other", "urn:air:other.example.com:default:other"},
		{"release-invalid", "not-an-air-identifier"},
	}}
	revokeTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{catalogPublisherRow(t, approvedPublisher)}, queryResults: []identityQueryResult{{rows: releaseRows}}}
	audit, outbox = currentTestEvidence(t, "publisher.publication_domain_revoked", "publisher", publisher.ID, now)
	revoked, affected, err := repository.RevokePublisherPublicationDomain(identityRepositoryContext(pool, revokeTx), tenantID, publisher.ID, "catalog.example.com", approvedPublisher.Revision, audit, outbox)
	if err != nil || revoked.Revision != 3 || len(revoked.ApprovedPublicationDomains) != 1 || revoked.ApprovedPublicationDomains[0] != "other.example.com" || len(affected) != 1 || affected[0] != "release-matching" || !releaseRows.closed || len(revokeTx.statements) != 3 {
		t.Fatalf("revoked publisher = %#v, affected=%#v, rowsClosed=%t, statements=%d, error=%v", revoked, affected, releaseRows.closed, len(revokeTx.statements), err)
	}
	unapprovedTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{catalogPublisherRow(t, publisher)}}
	if _, _, err := repository.RevokePublisherPublicationDomain(identityRepositoryContext(pool, unapprovedTx), tenantID, publisher.ID, "catalog.example.com", publisher.Revision, audit, outbox); catalog.ErrorCodeOf(err) != catalog.CodeInvalidTransition {
		t.Fatalf("unapproved publication-domain revocation = %v", err)
	}
}

func TestPostgresNamespaceAndPublisherUpdatesAreOptimisticAndDependencyAware(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	createdAt := now.Add(-time.Hour)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	audit, outbox := currentTestEvidence(t, "namespace.updated", "namespace", "00000000-0000-7000-8000-000000000021", now)

	namespaceRow := func(revision int64) pgx.Row {
		return scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, "00000000-0000-7000-8000-000000000021")
			setScanDestination(t, destinations, 1, tenantID)
			setScanDestination(t, destinations, 2, "stable-slug")
			setScanDestination(t, destinations, 3, "Old namespace")
			setScanDestination(t, destinations, 4, "Old description")
			setScanDestination(t, destinations, 5, revision)
			setScanDestination(t, destinations, 6, createdAt)
			setScanDestination(t, destinations, 7, createdAt)
			return nil
		})
	}
	updatedNamespace := catalog.Namespace{ID: "00000000-0000-7000-8000-000000000021", TenantID: tenantID, Slug: "caller-cannot-change", DisplayName: "New namespace", Description: "New description", Revision: 2, CreatedAt: now, UpdatedAt: now}
	namespaceTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{namespaceRow(1)}}
	gotNamespace, err := repository.UpdateNamespace(identityRepositoryContext(pool, namespaceTx), catalog.UpdateNamespaceParams{Namespace: updatedNamespace, ExpectedRevision: 1, Audit: audit, Outbox: outbox})
	if err != nil || gotNamespace.Slug != "stable-slug" || !gotNamespace.CreatedAt.Equal(createdAt) || gotNamespace.Revision != 2 || len(namespaceTx.statements) != 3 {
		t.Fatalf("updated namespace = %#v, statements=%d, error=%v", gotNamespace, len(namespaceTx.statements), err)
	}
	staleNamespaceTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{namespaceRow(2)}}
	if _, err := repository.UpdateNamespace(identityRepositoryContext(pool, staleNamespaceTx), catalog.UpdateNamespaceParams{Namespace: updatedNamespace, ExpectedRevision: 1, Audit: audit, Outbox: outbox}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || len(staleNamespaceTx.statements) != 0 {
		t.Fatalf("stale namespace update = %v, statements=%d", err, len(staleNamespaceTx.statements))
	}

	tenantLockRow := func() pgx.Row {
		return scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, tenantID)
			return nil
		})
	}
	currentPublisher := catalog.Publisher{ID: "00000000-0000-7000-8000-000000000022", TenantID: tenantID, DisplayName: "Old publisher", Domains: []string{"catalog.example.com"}, Revision: 1, CreatedAt: createdAt, UpdatedAt: createdAt}
	updatedPublisher := catalog.Publisher{ID: currentPublisher.ID, TenantID: tenantID, DisplayName: "New publisher", Domains: []string{"catalog.example.com", "new.example.com"}, Revision: 2, UpdatedAt: now}
	publisherTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{
		tenantLockRow(),
		catalogPublisherRow(t, currentPublisher),
		scannerFunc(func(destinations ...any) error { setScanDestination(t, destinations, 0, false); return nil }),
	}}
	audit, outbox = currentTestEvidence(t, "publisher.updated", "publisher", currentPublisher.ID, now)
	gotPublisher, err := repository.UpdatePublisher(identityRepositoryContext(pool, publisherTx), catalog.UpdatePublisherParams{Publisher: updatedPublisher, ExpectedRevision: 1, Audit: audit, Outbox: outbox})
	if err != nil || gotPublisher.DisplayName != updatedPublisher.DisplayName || !gotPublisher.CreatedAt.Equal(createdAt) || gotPublisher.Revision != 2 || len(publisherTx.statements) != 4 {
		t.Fatalf("updated publisher = %#v, statements=%d, error=%v", gotPublisher, len(publisherTx.statements), err)
	}
	if gotPublisher.ApprovedPublicationDomains == nil {
		t.Fatal("empty publication approvals must serialize as an array after metadata updates")
	}

	approved := currentPublisher
	approved.ApprovedPublicationDomains = []string{"catalog.example.com"}
	removeApprovedTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{tenantLockRow(), catalogPublisherRow(t, approved)}}
	removeAll := updatedPublisher
	removeAll.Domains = []string{"replacement.example.com"}
	if _, err := repository.UpdatePublisher(identityRepositoryContext(pool, removeApprovedTx), catalog.UpdatePublisherParams{Publisher: removeAll, ExpectedRevision: 1, Audit: audit, Outbox: outbox}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("approved publisher-domain removal = %v", err)
	}

	activePublicationTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{
		tenantLockRow(),
		catalogPublisherRow(t, currentPublisher),
		scannerFunc(func(destinations ...any) error { setScanDestination(t, destinations, 0, true); return nil }),
	}}
	if _, err := repository.UpdatePublisher(identityRepositoryContext(pool, activePublicationTx), catalog.UpdatePublisherParams{Publisher: removeAll, ExpectedRevision: 1, Audit: audit, Outbox: outbox}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("active publisher-domain removal = %v", err)
	}

	overlapTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{
		tenantLockRow(),
		catalogPublisherRow(t, currentPublisher),
		scannerFunc(func(destinations ...any) error { setScanDestination(t, destinations, 0, true); return nil }),
	}}
	if _, err := repository.UpdatePublisher(identityRepositoryContext(pool, overlapTx), catalog.UpdatePublisherParams{Publisher: updatedPublisher, ExpectedRevision: 1, Audit: audit, Outbox: outbox}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("overlapping publisher-domain update = %v", err)
	}
}

func TestPostgresNamespaceAndPublisherListsHydrateBoundedTenantRows(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	namespaceRows := &identityScriptedRows{values: [][]any{
		{"namespace-a", tenantID, "alpha", "Alpha", "First namespace", int64(2), now, now},
		{"namespace-b", tenantID, "beta", "Beta", "Second namespace", int64(1), now, now},
	}}
	namespaceTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: namespaceRows}}}
	namespaces, err := repository.ListNamespaces(identityRepositoryContext(pool, namespaceTx), tenantID, catalog.NamespaceListRequest{Limit: 100})
	if err != nil || len(namespaces) != 2 || namespaces[0].Slug != "alpha" || namespaces[1].Revision != 1 || !namespaceRows.closed {
		t.Fatalf("namespace list = %#v rowsClosed=%t, %v", namespaces, namespaceRows.closed, err)
	}

	publisherRows := &identityScriptedRows{values: [][]any{
		{"publisher-a", tenantID, "Alpha publisher", []string{"alpha.example.com"}, []string{"alpha.example.com"}, int64(2), now, now},
		{"publisher-b", tenantID, "Beta publisher", []string{"beta.example.com"}, []string{}, int64(1), now, now},
	}}
	publisherTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: publisherRows}}}
	publishers, err := repository.ListPublishers(identityRepositoryContext(pool, publisherTx), tenantID)
	if err != nil || len(publishers) != 2 || publishers[0].Domains[0] != "alpha.example.com" || len(publishers[1].ApprovedPublicationDomains) != 0 || !publisherRows.closed {
		t.Fatalf("publisher list = %#v rowsClosed=%t, %v", publishers, publisherRows.closed, err)
	}
}

func TestPostgresAdministrationPagePropagatesQueryFailureAndCancellation(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}

	for name, cause := range map[string]error{
		"SQL failure":  errors.New("query unavailable"),
		"cancellation": context.Canceled,
	} {
		t.Run(name, func(t *testing.T) {
			transaction := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{err: cause}}}
			_, pageErr := repository.ListPublisherPage(identityRepositoryContext(pool, transaction), tenantID, catalog.PublisherListRequest{Limit: 21})
			if catalog.ErrorCodeOf(pageErr) != catalog.CodeDependencyUnavailable || !errors.Is(pageErr, cause) {
				t.Fatalf("publisher page error = %v; want wrapped %v", pageErr, cause)
			}
		})
	}

	rows := &identityScriptedRows{err: errors.New("iteration interrupted")}
	transaction := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: rows}}}
	if _, err := repository.ListPublisherPage(identityRepositoryContext(pool, transaction), tenantID, catalog.PublisherListRequest{Limit: 21}); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || !rows.closed {
		t.Fatalf("publisher page iteration error = %v, rowsClosed=%t", err, rows.closed)
	}
}
