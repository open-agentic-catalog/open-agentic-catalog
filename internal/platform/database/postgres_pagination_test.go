package database

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

const paginationTestTenantID = "00000000-0000-7000-8000-000000000001"

type captureCommandConnection struct {
	transaction  pgx.Tx
	transactions []pgx.Tx
	options      []pgx.TxOptions
	beginErr     error
}

func (connection *captureCommandConnection) BeginTx(_ context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	connection.options = append(connection.options, options)
	if connection.beginErr != nil {
		return nil, connection.beginErr
	}
	if connection.transactions != nil {
		if len(connection.transactions) == 0 {
			return nil, errors.New("unexpected transaction")
		}
		transaction := connection.transactions[0]
		connection.transactions = connection.transactions[1:]
		return transaction, nil
	}
	return connection.transaction, nil
}

func TestPostgresAdministrationExactReadsAndSimplePages(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}

	namespaceTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, "00000000-0000-7000-8000-000000000010")
		setScanDestination(t, destinations, 1, paginationTestTenantID)
		setScanDestination(t, destinations, 2, "alpha")
		setScanDestination(t, destinations, 3, "Alpha")
		setScanDestination(t, destinations, 4, "First namespace")
		setScanDestination(t, destinations, 5, int64(2))
		setScanDestination(t, destinations, 6, now)
		setScanDestination(t, destinations, 7, now)
		return nil
	})}}
	namespace, err := repository.GetNamespace(identityRepositoryContext(pool, namespaceTx), paginationTestTenantID, "00000000-0000-7000-8000-000000000010")
	if err != nil || namespace.Slug != "alpha" {
		t.Fatalf("exact namespace = %#v, %v", namespace, err)
	}
	requireTenantBoundQuery(t, namespaceTx.queryRowStatements, namespaceTx.queryRowArguments)

	publisher := catalog.Publisher{ID: "00000000-0000-7000-8000-000000000020", TenantID: paginationTestTenantID, DisplayName: "Alpha publisher", Domains: []string{"alpha.example.com"}, ApprovedPublicationDomains: []string{"alpha.example.com"}, Revision: 2, CreatedAt: now, UpdatedAt: now}
	publisherTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{catalogPublisherRow(t, publisher)}}
	gotPublisher, err := repository.GetPublisher(identityRepositoryContext(pool, publisherTx), paginationTestTenantID, publisher.ID)
	if err != nil || gotPublisher.ID != publisher.ID || gotPublisher.DisplayName != publisher.DisplayName {
		t.Fatalf("exact publisher = %#v, %v", gotPublisher, err)
	}
	requireTenantBoundQuery(t, publisherTx.queryRowStatements, publisherTx.queryRowArguments)

	publisherRows := &identityScriptedRows{values: [][]any{{publisher.ID, publisher.TenantID, publisher.DisplayName, publisher.Domains, publisher.ApprovedPublicationDomains, publisher.Revision, publisher.CreatedAt, publisher.UpdatedAt}}}
	publisherPageTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: publisherRows}}}
	publisherPage, err := repository.ListPublisherPage(identityRepositoryContext(pool, publisherPageTx), paginationTestTenantID, catalog.PublisherListRequest{AfterDisplayName: "A", AfterID: "00000000-0000-7000-8000-000000000019", Limit: 2})
	if err != nil || len(publisherPage) != 1 || publisherPage[0].ID != publisher.ID || !publisherRows.closed {
		t.Fatalf("publisher page = %#v, rowsClosed=%t, %v", publisherPage, publisherRows.closed, err)
	}
	requireTenantBoundQuery(t, publisherPageTx.queryStatements, publisherPageTx.queryArguments)

	auditRows := &identityScriptedRows{values: [][]any{{(*int)(nil), "00000000-0000-7000-8000-000000000030", paginationTestTenantID, "admin", "namespace.created", "namespace", namespace.ID, "reason", "request", "", "digest", []byte(`{"reviewed":true,"largeNumber":9007199254740993}`), now}}}
	auditTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: auditRows}}}
	auditFrom, auditBefore := now.Add(-time.Hour), now.Add(time.Hour)
	auditPage, err := repository.AuditEvents(identityRepositoryContext(pool, auditTx), paginationTestTenantID, catalog.AuditEventListRequest{Filters: audit.Query{Actor: "admin", Action: "namespace.created", RequestID: "request", TargetType: "namespace", TargetID: namespace.ID, OccurredFrom: &auditFrom, OccurredBefore: &auditBefore}, Limit: 2})
	if err != nil || len(auditPage) != 1 {
		t.Fatalf("audit page = %#v, rowsClosed=%t, %v", auditPage, auditRows.closed, err)
	}
	largeNumber, numberOK := auditPage[0].Details["largeNumber"].(json.Number)
	if auditPage[0].Details["reviewed"] != true || !numberOK || largeNumber.String() != "9007199254740993" || !auditRows.closed {
		t.Fatalf("audit page = %#v, rowsClosed=%t, %v", auditPage, auditRows.closed, err)
	}
	requireTenantBoundQuery(t, auditTx.queryStatements, auditTx.queryArguments)
	arguments := auditTx.queryArguments[0]
	if len(arguments) != 12 || arguments[3] != "admin" || arguments[4] != "namespace.created" || arguments[5] != "request" || arguments[6] != "namespace" || arguments[7] != namespace.ID || arguments[8] != &auditFrom || arguments[9] != &auditBefore || arguments[10] != 2 || arguments[11] != audit.MaxEncodedRecordBytes {
		t.Fatalf("audit database filters were not bound before the limit: %#v", arguments)
	}
}

func TestPostgresAuditExportStartsSharedRepeatableReadTransaction(t *testing.T) {
	from := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	emptyMetadata := &identityScriptedRows{}
	emptyRecords := &identityScriptedRows{}
	transaction := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: emptyMetadata}, {rows: emptyRecords}}}
	connection := &captureCommandConnection{transaction: transaction}
	command := &commandTransaction{pool: pool, conn: connection, tenantID: paginationTestTenantID}
	access, err := audit.NewRecord("00000000-0000-7000-8000-000000000030", paginationTestTenantID, audit.Principal{ID: "catalog-api", Type: audit.PrincipalServiceExecutor}, audit.ActionAuditExported, "audit_range", paginationTestTenantID, "verify transaction isolation", audit.Execution{}, map[string]any{
		"rowCount":       0,
		"eventsDigest":   audit.DigestBytes([]byte("[]")),
		"occurredFrom":   from.Format(time.RFC3339Nano),
		"occurredBefore": before.Format(time.RFC3339Nano),
	}, before)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := repository.ExportAuditEvents(withCommandTransaction(t.Context(), command), paginationTestTenantID, catalog.AuditExportRequest{Filters: audit.Query{OccurredFrom: &from, OccurredBefore: &before}, Cutoff: before, GeneratedAt: before, Access: access})
	if err != nil || exported.Manifest.RowCount != 0 {
		t.Fatalf("audit export = %#v, %v", exported, err)
	}
	wantOptions := pgx.TxOptions{IsoLevel: pgx.RepeatableRead}
	if len(connection.options) != 1 || connection.options[0] != wantOptions || command.tx != transaction {
		t.Fatalf("command transaction options = %#v, retained=%p, want %#v and %p", connection.options, command.tx, wantOptions, transaction)
	}
	if !emptyMetadata.closed || !emptyRecords.closed || len(transaction.statements) != 2 {
		t.Fatalf("export resources metadataClosed=%t recordsClosed=%t statements=%d", emptyMetadata.closed, emptyRecords.closed, len(transaction.statements))
	}
	if err := command.complete(t.Context(), 200, func(context.Context, pgx.Tx) error { return nil }); err != nil || transaction.commits != 1 {
		t.Fatalf("complete shared audit transaction commits=%d, error=%v", transaction.commits, err)
	}
}

func TestPostgresIdentityAndJobPagesDecodeBoundedRows(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	identityStore, err := NewPostgresIdentityStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	assignmentRows := &identityScriptedRows{values: [][]any{{
		"00000000-0000-7000-8000-000000000040", paginationTestTenantID, "00000000-0000-7000-8000-000000000041", "reader@example.com", identity.ActorHuman,
		identity.RoleReader, "", identity.AssignmentActive, int64(1), "admin", "grant", now, "", "", (*time.Time)(nil),
	}}}
	assignmentTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: assignmentRows}}}
	assignments, err := identityStore.ListRoleAssignments(identityRepositoryContext(pool, assignmentTx), paginationTestTenantID, identity.RoleAssignmentListRequest{Limit: 2})
	if err != nil || len(assignments) != 1 || assignments[0].Subject != "reader@example.com" || !assignmentRows.closed {
		t.Fatalf("role-assignment page = %#v, rowsClosed=%t, %v", assignments, assignmentRows.closed, err)
	}
	requireTenantBoundQuery(t, assignmentTx.queryStatements, assignmentTx.queryArguments)

	jobStore, err := NewPostgresJobStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	jobRows := &identityScriptedRows{values: [][]any{{
		"00000000-0000-7000-8000-000000000050", paginationTestTenantID, "source.sync", "admin", "", 2, json.RawMessage(`{"sourceId":"00000000-0000-7000-8000-000000000060"}`), jobs.StateSucceeded,
		1, 100, now, 1, 5, "", (*time.Time)(nil), false, "private-deduplication-key", json.RawMessage(`{"id":"00000000-0000-7000-8000-000000000061","sourceId":"00000000-0000-7000-8000-000000000060","state":"complete"}`), "", jobs.FailureClass(""), now, now, catalog.TraceContext(""),
	}}}
	jobTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: jobRows}}}
	jobPage, err := jobStore.List(identityRepositoryContext(pool, jobTx), paginationTestTenantID, jobs.ListRequest{Limit: 2})
	if err != nil || len(jobPage) != 1 || jobPage[0].Kind != "source.sync" || !jobRows.closed {
		t.Fatalf("job page = %#v, rowsClosed=%t, %v", jobPage, jobRows.closed, err)
	}
	view := jobs.PublicView(jobPage[0])
	if view.Result == nil || view.Result.Type != "source_snapshot" {
		t.Fatalf("safe job view = %#v", view)
	}
	requireTenantBoundQuery(t, jobTx.queryStatements, jobTx.queryArguments)
}

func TestPostgresPublicationAndWebhookPagesDecodeBoundedRows(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	publicationStore, err := NewPostgresPublicationStore(repository, "catalog.example.com")
	if err != nil {
		t.Fatal(err)
	}
	publicationRows := &identityScriptedRows{values: [][]any{{
		"00000000-0000-7000-8000-000000000070", paginationTestTenantID, "catalog.example.com", publication.AudienceAuthenticated, int64(3), "ard-test", "ai-test", publication.SnapshotActive, `"snapshot-etag"`, now, &now,
	}}}
	publicationTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: publicationRows}}}
	snapshots, err := publicationStore.ListSummaries(identityRepositoryContext(pool, publicationTx), paginationTestTenantID, publication.SnapshotListRequest{Limit: 2})
	if err != nil || len(snapshots) != 1 || snapshots[0].Generation != 3 || !publicationRows.closed {
		t.Fatalf("publication page = %#v, rowsClosed=%t, %v", snapshots, publicationRows.closed, err)
	}
	requireTenantBoundQuery(t, publicationTx.queryStatements, publicationTx.queryArguments)

	webhookStore, err := NewPostgresWebhookStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	endpointRows := &identityScriptedRows{values: [][]any{{
		"00000000-0000-7000-8000-000000000080", paginationTestTenantID, "Audit sink", "https://hooks.example.com/catalog", []string{"com.open.agentic.catalog.resource.created.v1"}, webhook.StatusActive, int64(2), 1, []byte("encrypted"), "admin", now, now,
	}}}
	endpointTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: endpointRows}}}
	endpoints, err := webhookStore.ListEndpointPage(identityRepositoryContext(pool, endpointTx), paginationTestTenantID, webhook.EndpointListRequest{Limit: 2})
	if err != nil || len(endpoints) != 1 || endpoints[0].Name != "Audit sink" || !endpointRows.closed {
		t.Fatalf("webhook page = %#v, rowsClosed=%t, %v", endpoints, endpointRows.closed, err)
	}
	requireTenantBoundQuery(t, endpointTx.queryStatements, endpointTx.queryArguments)

	deliveryRows := &identityScriptedRows{values: [][]any{{
		"00000000-0000-7000-8000-000000000081", paginationTestTenantID, endpoints[0].ID, "00000000-0000-7000-8000-000000000082", "com.open.agentic.catalog.resource.created.v1", webhook.DeliverySucceeded, 1, 204, "", (*time.Time)(nil), now, now,
	}}}
	deliveryTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: deliveryRows}}}
	deliveries, err := webhookStore.ListDeliveries(identityRepositoryContext(pool, deliveryTx), paginationTestTenantID, webhook.DeliveryListRequest{EndpointID: endpoints[0].ID, Limit: 2})
	if err != nil || len(deliveries) != 1 || deliveries[0].ResponseStatus != 204 || !deliveryRows.closed {
		t.Fatalf("webhook delivery page = %#v, rowsClosed=%t, %v", deliveries, deliveryRows.closed, err)
	}
	requireTenantBoundQuery(t, deliveryTx.queryStatements, deliveryTx.queryArguments)
}

func TestPostgresFederationPagesDecodeBoundedRows(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresFederationStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	sourceRaw := []byte(`{"id":"00000000-0000-7000-8000-000000000090","tenantId":"` + paginationTestTenantID + `","displayName":"Alpha source","type":"ard_manifest","endpoint":"https://source.example.com/ard.json","retrievalMode":"indexed","policyId":"policy","policyRevision":1,"policyDigest":"sha256:` + strings.Repeat("a", 64) + `","profileId":"ard-test","scope":{},"updateMode":"manual_review","live":{},"referrals":{},"nesting":{},"createdBy":"admin","createdAt":"2026-09-02T12:00:00Z","updatedAt":"2026-09-02T12:00:00Z"}`)
	sourceRows := &identityScriptedRows{values: [][]any{{sourceRaw, sourceRaw, federation.SourceActive, int64(2), "", (*time.Time)(nil), now, true, string(federation.RetrievalIndexed), "policy", int64(1), "sha256:" + strings.Repeat("a", 64), "", []byte(`[]`), []byte(`[]`)}}}
	sourceTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: sourceRows}}}
	sources, err := store.ListSourcePage(identityRepositoryContext(pool, sourceTx), paginationTestTenantID, federation.SourceListRequest{Limit: 2})
	if err != nil || len(sources) != 1 || sources[0].DisplayName != "Alpha source" || !sourceRows.closed {
		t.Fatalf("source page = %#v, rowsClosed=%t, %v", sources, sourceRows.closed, err)
	}
	requireTenantBoundQuery(t, sourceTx.queryStatements, sourceTx.queryArguments)

	snapshot := federation.Snapshot{ID: "00000000-0000-7000-8000-000000000091", TenantID: paginationTestTenantID, SourceID: sources[0].ID, State: federation.SnapshotComplete, StartedAt: now}
	snapshotRaw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshotRows := &identityScriptedRows{values: [][]any{{snapshotRaw}}}
	snapshotTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: snapshotRows}}}
	snapshots, err := store.ListSnapshotPage(identityRepositoryContext(pool, snapshotTx), paginationTestTenantID, federation.SnapshotListRequest{SourceID: sources[0].ID, Limit: 2})
	if err != nil || len(snapshots) != 1 || snapshots[0].ID != snapshot.ID || !snapshotRows.closed {
		t.Fatalf("source snapshot page = %#v, rowsClosed=%t, %v", snapshots, snapshotRows.closed, err)
	}
	requireTenantBoundQuery(t, snapshotTx.queryStatements, snapshotTx.queryArguments)

	observationValues := postgresObservationMetadataValues(now, sources[0].ID, snapshot.ID)
	observationRows := &identityScriptedRows{values: [][]any{observationValues}}
	observationTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: observationRows}}}
	observations, err := store.ListObservationPage(identityRepositoryContext(pool, observationTx), paginationTestTenantID, federation.ObservationListRequest{SourceID: sources[0].ID, CurrentOnly: true, Limit: 2})
	if err != nil || len(observations) != 1 || observations[0].Identifier != "urn:air:source.example.com:default:agent" || !observations[0].RawEntryAvailable || !observationRows.closed {
		t.Fatalf("observation page = %#v, rowsClosed=%t, %v", observations, observationRows.closed, err)
	}
	requireTenantBoundQuery(t, observationTx.queryStatements, observationTx.queryArguments)

	searchRows := &identityScriptedRows{values: [][]any{append(append([]any(nil), observationValues...), 80)}}
	searchTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: searchRows}}}
	searchTx.queryRows = []pgx.Row{scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, int64(1))
		setScanDestination(t, destinations, 1, false)
		return nil
	})}
	searchResults, err := store.SearchObservationPage(identityRepositoryContext(pool, searchTx), paginationTestTenantID, federation.ObservationSearchRequest{Query: federation.SearchQuery{Text: "agent", Kinds: []catalog.Kind{catalog.KindAgent}}, Limit: 2})
	if err != nil || len(searchResults) != 1 || searchResults[0].Score != 80 || searchResults[0].Source != sources[0].ID || !searchRows.closed {
		t.Fatalf("observation search page = %#v, rowsClosed=%t, %v", searchResults, searchRows.closed, err)
	}
	requireTenantBoundQuery(t, searchTx.queryStatements, searchTx.queryArguments)

	occurrenceRows := &identityScriptedRows{values: [][]any{{observations[0].ID, sources[0].ID, snapshot.ID, int64(2), "sha256:" + strings.Repeat("b", 64), "/entries/0", "sha256:" + strings.Repeat("c", 64), "sha256:" + strings.Repeat("d", 64), true, now, "sha256:" + strings.Repeat("e", 64)}}}
	occurrenceTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: occurrenceRows}}}
	occurrences, err := store.ListObservationOccurrencePage(identityRepositoryContext(pool, occurrenceTx), paginationTestTenantID, federation.OccurrenceListRequest{ObservationID: observations[0].ID, Limit: 2})
	if err != nil || len(occurrences) != 1 || occurrences[0].EntryLocation != "/entries/0" || !occurrenceRows.closed {
		t.Fatalf("observation occurrence page = %#v, rowsClosed=%t, %v", occurrences, occurrenceRows.closed, err)
	}
	requireTenantBoundQuery(t, occurrenceTx.queryStatements, occurrenceTx.queryArguments)
}

func postgresObservationValues(now time.Time, sourceID, snapshotID string) []any {
	return []any{
		"00000000-0000-7000-8000-000000000092", paginationTestTenantID, sourceID, snapshotID, "entry-1",
		"urn:air:source.example.com:default:agent", "1.0.0", true, catalog.KindAgent, "application/json", federation.CompleteEntry,
		[]byte(`{"structural":"valid","profileId":"ard-test","support":"supported"}`), []byte(`{"name":"Agent"}`), []byte(`{"name":"Agent"}`),
		"sha256:" + strings.Repeat("f", 64), "sha256:" + strings.Repeat("1", 64), "sha256:" + strings.Repeat("2", 64), "ard-test", "Agent", "Searchable agent", []string{"agent"}, []string{"find agent"}, "source.example.com", federation.FreshCurrent, now, now,
	}
}

func requireTenantBoundQuery(t *testing.T, statements []string, argumentSets [][]any) {
	t.Helper()
	if len(statements) != 1 || len(argumentSets) != 1 || len(argumentSets[0]) == 0 || argumentSets[0][0] != paginationTestTenantID || !strings.Contains(statements[0], "tenant_id=$1") {
		t.Fatalf("query is not explicitly tenant-bound: statements=%#v arguments=%#v", statements, argumentSets)
	}
}

// postgresObservationMetadataValues models SQL presence without document buffers.
func postgresObservationMetadataValues(now time.Time, sourceID, snapshotID string) []any {
	values := postgresObservationValues(now, sourceID, snapshotID)
	return append(append(values[:12:12], true), values[14:]...)
}
