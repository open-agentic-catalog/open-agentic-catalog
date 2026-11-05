package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestValidatePostgresVersion(t *testing.T) {
	t.Parallel()

	for _, version := range []int{180000, 180006, 189999} {
		if err := validatePostgresVersion(version); err != nil {
			t.Fatalf("PostgreSQL version %d was rejected: %v", version, err)
		}
	}

	for _, version := range []int{0, 170011, 190000} {
		err := validatePostgresVersion(version)
		if err == nil {
			t.Fatalf("PostgreSQL version %d was accepted", version)
		}
		if !strings.Contains(err.Error(), "major version 18 is required") {
			t.Fatalf("PostgreSQL version %d returned %q", version, err)
		}
	}
}

func TestPostgresOpenRejectsMalformedConfigurationBeforeConnecting(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, dsn string }{
		{"URL encoding", "postgres://invalid%zz"},
		{"lifetime", "postgres://must-not-connect.invalid/catalog?pool_max_conn_lifetime=PRIVATE_POOL_SETTING"},
		{"idle", "postgres://must-not-connect.invalid/catalog?pool_max_conn_idle_time=PRIVATE_POOL_SETTING"},
		{"health period", "postgres://must-not-connect.invalid/catalog?pool_health_check_period=PRIVATE_POOL_SETTING"},
	} {
		t.Run(test.name, func(t *testing.T) {
			observer := &recordingQueryObserver{}
			for name, open := range map[string]func() (*pgxpool.Pool, error){
				"operator": func() (*pgxpool.Pool, error) { return Open(t.Context(), test.dsn, testPoolConfig(4)) },
				"runtime": func() (*pgxpool.Pool, error) {
					return OpenRuntimeObserved(t.Context(), test.dsn, testPoolConfig(4), nil)
				},
				"observed runtime": func() (*pgxpool.Pool, error) {
					return OpenRuntimeObserved(t.Context(), test.dsn, testPoolConfig(4), observer)
				},
			} {
				t.Run(name, func(t *testing.T) {
					pool, err := open()
					var parseErr *pgconn.ParseConfigError
					if pool != nil || !errors.As(err, &parseErr) || !strings.Contains(err.Error(), "parse PostgreSQL configuration") || len(observer.observations) != 0 {
						t.Fatalf("invalid configuration returned pool=%v, error=%v, observations=%#v", pool, err, observer.observations)
					}
					if got := safeerr.Message(err); got != "invalid PostgreSQL connection configuration" {
						t.Fatalf("unsafe parse diagnostic: %q", got)
					}
				})
			}
		})
	}
}

func TestComposeConnectValidatorsPreservesOrderAndFailures(t *testing.T) {
	t.Parallel()

	requiredErr := errors.New("required validation failed")
	configuredErr := errors.New("configured validation failed")
	for _, test := range []struct {
		name           string
		requiredError  error
		configuredErr  error
		omitConfigured bool
		wantCalls      string
		wantError      error
	}{
		{name: "both succeed", wantCalls: "required,configured"},
		{name: "required fails closed", requiredError: requiredErr, wantCalls: "required", wantError: requiredErr},
		{name: "configured failure is retained", configuredErr: configuredErr, wantCalls: "required,configured", wantError: configuredErr},
		{name: "no configured validator", omitConfigured: true, wantCalls: "required"},
		{name: "mandatory failure without configured validator", omitConfigured: true, requiredError: requiredErr, wantCalls: "required", wantError: requiredErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := make([]string, 0, 2)
			required := func(context.Context, *pgconn.PgConn) error {
				calls = append(calls, "required")
				return test.requiredError
			}
			configured := func(context.Context, *pgconn.PgConn) error {
				calls = append(calls, "configured")
				return test.configuredErr
			}
			if test.omitConfigured {
				configured = nil
			}
			err := composeConnectValidators(required, configured)(context.Background(), nil)
			if !errors.Is(err, test.wantError) || strings.Join(calls, ",") != test.wantCalls {
				t.Fatalf("validation calls=%q error=%v, want calls=%q error=%v", strings.Join(calls, ","), err, test.wantCalls, test.wantError)
			}
		})
	}
	if err := composeConnectValidators(nil, nil)(context.Background(), nil); err != nil {
		t.Fatalf("empty validation chain returned %v", err)
	}
}

func TestDatabaseContextErrorsPreserveCancellationCause(t *testing.T) {
	t.Parallel()
	providerErr := errors.New("provider canceled the query")
	if got := withDatabaseContextError(context.Background(), providerErr); !errors.Is(got, providerErr) || errors.Is(got, context.Canceled) {
		t.Fatalf("active-context error = %v", got)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	got := withDatabaseContextError(canceled, providerErr)
	if !errors.Is(got, providerErr) || !errors.Is(got, context.Canceled) {
		t.Fatalf("joined cancellation error = %v", got)
	}
	alreadyWrapped := catalog.Wrap(catalog.CodeDependencyUnavailable, "query failed", context.Canceled)
	if got := withDatabaseContextError(canceled, alreadyWrapped); !errors.Is(got, alreadyWrapped) || !errors.Is(got, context.Canceled) {
		t.Fatalf("existing cancellation cause was wrapped again: %v", got)
	}
	if got := withDatabaseContextError(canceled, nil); got != nil {
		t.Fatalf("nil database error became %v", got)
	}
}

type queryObservation struct {
	operation string
	err       error
	elapsed   time.Duration
}

type recordingQueryObserver struct{ observations []queryObservation }

func (o *recordingQueryObserver) ObserveDatabase(operation string, err error, elapsed time.Duration) {
	o.observations = append(o.observations, queryObservation{operation: operation, err: err, elapsed: elapsed})
}

type querySpanStarter func(context.Context, string, catalog.TraceKind, catalog.TraceContext) (context.Context, func(string))

func (start querySpanStarter) Start(ctx context.Context, name string, kind catalog.TraceKind, cause catalog.TraceContext) (context.Context, func(string)) {
	return start(ctx, name, kind, cause)
}

func TestQueryTracerReportsBoundedSpansAndDurations(t *testing.T) {
	t.Parallel()
	providerErr := errors.New("private provider details")
	for _, tt := range []struct {
		name, sql, operation, outcome string
		err                           error
		elapsed                       time.Duration
	}{
		{name: "success", sql: " UPDATE private_table SET token=$1", operation: "update", outcome: "ok", elapsed: 10 * time.Millisecond},
		{name: "failure", sql: "ALTER private_table ADD secret text", operation: "other", outcome: "error", err: providerErr, elapsed: time.Millisecond},
		{name: "empty and backward clock", sql: " \n\t", operation: "other", outcome: "ok", elapsed: -time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var names, outcomes []string
			start := querySpanStarter(func(ctx context.Context, name string, kind catalog.TraceKind, cause catalog.TraceContext) (context.Context, func(string)) {
				if kind != catalog.TraceClient || cause != "" {
					t.Fatalf("SQL span kind=%v cause=%q", kind, cause)
				}
				names = append(names, name)
				return ctx, func(outcome string) { outcomes = append(outcomes, outcome) }
			})
			parent := catalog.WithTracer(t.Context(), start)
			parent = catalog.WithTraceContext(parent, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
			now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
			observer := &recordingQueryObserver{}
			tracer := queryTracer{observer: observer, now: func() time.Time { return now }}
			ctx := tracer.TraceQueryStart(parent, nil, pgx.TraceQueryStartData{SQL: tt.sql, Args: []any{"private argument"}})
			now = now.Add(tt.elapsed)
			tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: tt.err})
			if len(names) != 1 || names[0] != "SQL "+tt.operation || len(outcomes) != 1 || outcomes[0] != tt.outcome {
				t.Fatalf("span names=%q outcomes=%q", names, outcomes)
			}
			if len(observer.observations) != 1 || observer.observations[0].operation != tt.operation || !errors.Is(observer.observations[0].err, tt.err) || observer.observations[0].elapsed != max(0, tt.elapsed) {
				t.Fatalf("query observations = %#v", observer.observations)
			}
		})
	}
}

func TestQueryTracerReducesSQLToBoundedStatementClasses(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	observer := &recordingQueryObserver{}
	tracer := queryTracer{observer: observer, now: func() time.Time { return now }}
	ctx := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "  SELECT * FROM private_table WHERE token='private'"})
	now = now.Add(25 * time.Millisecond)
	providerErr := errors.New("postgres://user:private@database/catalog")
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: providerErr})
	tracer.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{})

	if len(observer.observations) != 2 || observer.observations[0].operation != "select" || !errors.Is(observer.observations[0].err, providerErr) || observer.observations[0].elapsed != 25*time.Millisecond || observer.observations[1].operation != "other" || observer.observations[1].elapsed != 0 {
		t.Fatalf("query observations = %#v", observer.observations)
	}
	for input, expected := range map[string]string{"SELECT": "select", "INSERT": "insert", "UPDATE": "update", "DELETE": "delete", "WITH": "with", "BEGIN": "begin", "COMMIT": "commit", "ROLLBACK": "rollback", "copy": "copy", "VACUUM": "other", "secret table": "other"} {
		if actual := databaseStatementClass(input); actual != expected {
			t.Errorf("databaseStatementClass(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestPostgresConstructorsRejectMissingDependencies(t *testing.T) {
	t.Parallel()
	if _, err := NewPostgresRepository(nil); err == nil {
		t.Fatal("nil PostgreSQL pool was accepted")
	}
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil || repository.pool != pool {
		t.Fatalf("repository = %#v, %v", repository, err)
	}
	if _, err := NewPostgresIdempotencyStore(nil); err == nil {
		t.Fatal("nil PostgreSQL repository was accepted")
	}
	store, err := NewPostgresIdempotencyStore(repository)
	if err != nil || store.repository != repository {
		t.Fatalf("idempotency store = %#v, %v", store, err)
	}
	if _, err := NewPostgresIdentityStore(nil); err == nil {
		t.Fatal("nil PostgreSQL repository was accepted by the identity store")
	}
	identityStore, err := NewPostgresIdentityStore(repository)
	if err != nil || identityStore.repository != repository {
		t.Fatalf("identity store = %#v, %v", identityStore, err)
	}
	if _, err := NewPostgresFederationStore(nil); err == nil {
		t.Fatal("nil PostgreSQL repository was accepted by the federation store")
	}
	federationStore, err := NewPostgresFederationStore(repository)
	if err != nil || federationStore.repository != repository {
		t.Fatalf("federation store = %#v, %v", federationStore, err)
	}
	if _, err := NewPostgresSourceMaintenanceStore(nil); err == nil {
		t.Fatal("nil PostgreSQL repository was accepted by the source-maintenance store")
	}
	maintenanceStore, err := NewPostgresSourceMaintenanceStore(repository)
	if err != nil || maintenanceStore.repository != repository {
		t.Fatalf("source-maintenance store = %#v, %v", maintenanceStore, err)
	}
	if _, err := maintenanceStore.ListDueAutomaticSources(context.Background(), time.Time{}, time.Minute, "policy", 0); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("zero source-maintenance selection limit error = %v", err)
	}
}

func TestPostgresReadinessRequiresWritableSession(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"writable", "read-only", "query failure", "partial scan failure"} {
		t.Run(mode, func(t *testing.T) {
			failure := errors.New("readiness query failed")
			row := scriptedValueRow([]any{mode == "writable"})
			if strings.Contains(mode, "failure") {
				row = scannerFunc(func(destinations ...any) error {
					if mode == "partial scan failure" {
						setScanDestination(t, destinations, 0, true)
					}
					return failure
				})
			}
			query := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{row}}
			err := checkPostgresReadiness(t.Context(), query)
			if mode == "writable" {
				if err != nil {
					t.Fatalf("writable session refused: %v", err)
				}
			} else {
				message := "PostgreSQL session does not accept writes"
				if mode != "read-only" {
					message = "check PostgreSQL readiness"
					if !errors.Is(err, failure) {
						t.Fatal("readiness lost the query cause")
					}
				}
				var domain *catalog.DomainError
				if !errors.As(err, &domain) || domain.Code != catalog.CodeDependencyUnavailable || domain.Message != message {
					t.Fatalf("readiness error = %v; want dependency error %q", err, message)
				}
			}
			if len(query.queryRowStatements) != 1 || !strings.Contains(query.queryRowStatements[0], "current_setting('transaction_read_only')") || len(query.queryRowArguments[0]) != 0 || len(query.queryRows) != 0 {
				t.Fatalf("unexpected readiness query: %+v", query)
			}
			if len(query.statements) != 0 || len(query.queryStatements) != 0 || query.commits != 0 || query.rollbacks != 0 {
				t.Fatal("readiness performed unrelated work or took transaction ownership")
			}
		})
	}
}

func TestStandaloneTenantTransactionResultsAndCleanup(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"read-write", "read-only", "begin failure", "tenant failure", "callback failure", "commit failure", "canceled callback", "canceled commit", "deadline before begin", "panic"} {
		t.Run(mode, func(t *testing.T) {
			failure := errors.New("transaction operation failed")
			cleanupFailure := errors.New("rollback failed")
			tx := &fakeCommandTx{}
			connection := &fakeCommandConnection{transactions: []*fakeCommandTx{tx}}
			options := pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wantCalls, wantCommits, wantRollbacks := 1, 1, 1
			var wantContextError error
			wantOperation := ""
			switch mode {
			case "read-only":
				options.AccessMode = pgx.ReadOnly
			case "begin failure", "deadline before begin":
				connection.beginErr = failure
				wantCalls, wantCommits, wantRollbacks, wantOperation = 0, 0, 0, "begin database transaction"
				if mode == "deadline before begin" {
					deadlineCtx, deadlineCancel := context.WithDeadline(ctx, time.Unix(1, 0))
					defer deadlineCancel()
					ctx, wantContextError = deadlineCtx, context.DeadlineExceeded
				}
			case "tenant failure":
				tx.execErrors = []error{failure}
				wantCalls, wantCommits, wantOperation = 0, 0, "set tenant context"
			case "callback failure", "canceled callback":
				wantCommits = 0
				tx.rollbackErr = cleanupFailure
			case "commit failure", "canceled commit":
				tx.commitErr, wantOperation = failure, "commit database transaction"
			case "panic":
				wantCommits = 0
			}
			if mode == "canceled callback" || mode == "canceled commit" {
				wantContextError = context.Canceled
			}
			calls, got := 0, 0
			var err error
			var panicked any
			started := time.Now()
			func() {
				defer func() { panicked = recover() }()
				got, err = withStandaloneTenantTx(ctx, connection, paginationTestTenantID, options, func(actual pgx.Tx) (int, error) {
					calls++
					if actual != tx || len(tx.statements) != 1 || !strings.Contains(tx.statements[0], "set_config('app.tenant_id'") || !reflect.DeepEqual(tx.arguments, [][]any{{paginationTestTenantID, "", ""}}) || tx.commits != 0 || tx.rollbacks != 0 {
						t.Fatal("callback ran without tenant setup or after transaction completion")
					}
					if errors.Is(wantContextError, context.Canceled) {
						cancel()
					}
					if mode == "panic" {
						panic("scripted callback panic")
					}
					if mode == "callback failure" || mode == "canceled callback" {
						return 99, failure
					}
					return 42, nil
				})
			}()
			switch mode {
			case "panic":
				if panicked != "scripted callback panic" {
					t.Fatalf("callback panic was lost: %v", panicked)
				}
			case "read-write", "read-only":
				if err != nil || got != 42 {
					t.Fatalf("committed result = %d, %v", got, err)
				}
			default:
				if got != 0 || !errors.Is(err, failure) || errors.Is(err, cleanupFailure) {
					t.Fatalf("failed transaction = %d, %v; want zero and primary cause", got, err)
				}
				if wantOperation != "" {
					var domain *catalog.DomainError
					if !errors.As(err, &domain) || domain.Code != catalog.CodeDependencyUnavailable || domain.Message != wantOperation {
						t.Fatalf("transaction error = %v; want %q dependency error", err, wantOperation)
					}
				}
			}
			if mode != "panic" && panicked != nil || wantContextError != nil && !errors.Is(err, wantContextError) {
				t.Fatalf("unexpected panic/context error: %v, %v", panicked, err)
			}
			if !reflect.DeepEqual(connection.options, []pgx.TxOptions{options}) || calls != wantCalls || tx.commits != wantCommits || tx.rollbacks != wantRollbacks {
				t.Fatalf("transaction calls: options=%v callback=%d commit=%d rollback=%d", connection.options, calls, tx.commits, tx.rollbacks)
			}
			if wantCommits > 0 && !errors.Is(tx.commitCtx, wantContextError) {
				t.Fatalf("commit did not receive the caller's context: %v", tx.commitCtx)
			}
			if wantRollbacks > 0 && (tx.rollbackCtx != nil || tx.rollbackDeadline.Before(started) || tx.rollbackDeadline.After(time.Now().Add(commandTransactionCleanupLimit))) {
				t.Fatalf("cleanup did not use an independent bounded context: %v, %v", tx.rollbackCtx, tx.rollbackDeadline)
			}
		})
	}
}

func TestWithTenantTransactionUsesAtomicCommandContext(t *testing.T) {
	t.Parallel()
	pool := &pgxpool.Pool{}
	transaction := &fakeCommandTx{}
	command := &commandTransaction{pool: pool, conn: &fakeCommandConnection{transactions: []*fakeCommandTx{transaction}}, tenantID: "tenant-a"}
	ctx := withCommandTransaction(context.Background(), command)
	result, err := withTenantTx(ctx, pool, "tenant-a", pgx.TxOptions{}, func(pgx.Tx) (int, error) { return 42, nil })
	if err != nil || result != 42 || transaction.commits != 0 {
		t.Fatalf("atomic result=%d error=%v commits=%d", result, err, transaction.commits)
	}
	readResult, err := withTenantRead(ctx, pool, "tenant-a", func(pgx.Tx) (string, error) { return "value", nil })
	if err != nil || readResult != "value" {
		t.Fatalf("atomic read=%q error=%v", readResult, err)
	}
	operationErr := errors.New("operation failed")
	if result, err := withTenantTx(ctx, pool, "tenant-a", pgx.TxOptions{}, func(pgx.Tx) (int, error) { return 99, operationErr }); result != 0 || !errors.Is(err, operationErr) || transaction.rollbacks != 1 || transaction.commits != 0 {
		t.Fatalf("failed repository operation returned result=%d error=%v, commits=%d rollbacks=%d", result, err, transaction.commits, transaction.rollbacks)
	}
	if _, err := withTenantTx(ctx, &pgxpool.Pool{}, "tenant-a", pgx.TxOptions{}, func(pgx.Tx) (int, error) { return 0, nil }); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("cross-pool command error = %v", err)
	}
	if _, err := withTenantTx(ctx, pool, "tenant-b", pgx.TxOptions{}, func(pgx.Tx) (int, error) { return 0, nil }); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("cross-tenant command error = %v", err)
	}
}

func TestDatabaseEvidenceEncodingAndWriteFailures(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	audit, outbox := currentTestEvidence(t, "namespace.created", "namespace", "00000000-0000-7000-8000-000000000002", now)
	binding := expectEvidence(audit.TenantID, audit.TargetType, audit.TargetID, audit.Action)
	transaction := &fakeCommandTx{}
	if err := insertEvidence(context.Background(), transaction, audit, outbox, binding); err != nil {
		t.Fatal(err)
	}
	if len(transaction.statements) != 2 {
		t.Fatalf("evidence statements = %d", len(transaction.statements))
	}

	audit.Details = map[string]any{"invalid": make(chan int)}
	invalidAuditTx := &fakeCommandTx{}
	if err := insertAuditEvent(t.Context(), invalidAuditTx, audit); catalog.ErrorCodeOf(err) != catalog.CodeInternal || len(invalidAuditTx.statements) != 0 {
		t.Fatalf("invalid standalone audit reached persistence: %v, statements=%q", err, invalidAuditTx.statements)
	}
	if err := insertEvidence(context.Background(), &fakeCommandTx{}, audit, outbox, binding); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("invalid evidence error = %v", err)
	}
	audit.Details = map[string]any{"principalType": "service_executor"}
	for name, transaction := range map[string]*fakeCommandTx{
		"audit":  {execErrors: []error{errors.New("audit failed")}},
		"outbox": {execErrors: []error{nil, errors.New("outbox failed")}},
	} {
		if err := insertEvidence(context.Background(), transaction, audit, outbox, binding); err == nil {
			t.Fatalf("%s write failure was discarded", name)
		}
	}
}

func TestPostgresAppendEvidenceUsesTheAmbientCommandTransaction(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	tenantID := "00000000-0000-7000-8000-000000000001"
	record, err := audit.NewRecord("00000000-0000-7000-8000-000000000002", tenantID, audit.Principal{ID: "00000000-0000-7000-8000-000000000003", Type: audit.PrincipalCatalogMembership, ActorType: "human", RealmID: "00000000-0000-7000-8000-000000000004"}, "namespace.created", "namespace", tenantID, "", audit.Execution{}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	data, err := events.MarshalReference(record.Action, record.TargetType, record.TargetID, record.AfterDigest)
	if err != nil {
		t.Fatal(err)
	}
	outbox := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000005", TenantID: tenantID, Type: catalog.CanonicalEventType(record.Action), Subject: tenantID, Data: data, CreatedAt: record.At}
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	appendWith := func(tx *fakeCommandTx, event audit.Record, notification *catalog.OutboxEvent) error {
		command := &commandTransaction{pool: pool, tenantID: tenantID, tx: tx, options: pgx.TxOptions{}}
		return repository.AppendEvidence(withCommandTransaction(context.Background(), command), event, notification)
	}

	auditOnly := &fakeCommandTx{}
	if err := appendWith(auditOnly, record, nil); err != nil || len(auditOnly.statements) != 1 {
		t.Fatalf("audit-only append error=%v writes=%d", err, len(auditOnly.statements))
	}
	paired := &fakeCommandTx{}
	if err := appendWith(paired, record, &outbox); err != nil || len(paired.statements) != 2 {
		t.Fatalf("paired append error=%v writes=%d", err, len(paired.statements))
	}
	mismatched := outbox
	mismatched.Subject = "00000000-0000-7000-8000-000000000099"
	rejected := &fakeCommandTx{}
	if err := appendWith(rejected, record, &mismatched); catalog.ErrorCodeOf(err) != catalog.CodeInternal || len(rejected.statements) != 0 || rejected.rollbacks != 1 {
		t.Fatalf("mismatched append error=%v writes=%d rollbacks=%d", err, len(rejected.statements), rejected.rollbacks)
	}
	for name, execErrors := range map[string][]error{
		"audit":  {errors.New("audit unavailable")},
		"outbox": {nil, errors.New("outbox unavailable")},
	} {
		t.Run(name+" failure", func(t *testing.T) {
			tx := &fakeCommandTx{execErrors: execErrors}
			if err := appendWith(tx, record, &outbox); err == nil || len(tx.statements) != len(execErrors) || tx.rollbacks != 1 {
				t.Fatalf("append error=%v writes=%d rollbacks=%d", err, len(tx.statements), tx.rollbacks)
			}
		})
	}
}

func TestVersionedEvidencePairsAreBoundBeforeWrite(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	event, _ := currentTestEvidence(t, "namespace.created", "namespace", "00000000-0000-7000-8000-000000000002", now)
	data, err := events.MarshalReference(event.Action, event.TargetType, event.TargetID, event.AfterDigest)
	if err != nil {
		t.Fatal(err)
	}
	outbox := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000003", TenantID: event.TenantID, Type: catalog.CanonicalEventType(event.Action), Subject: event.TargetID, Data: data, CreatedAt: now}
	if err := validateEvidencePair(event, outbox); err != nil {
		t.Fatal(err)
	}
	expected := expectEvidence(event.TenantID, event.TargetType, event.TargetID, event.Action)
	if err := validateEvidencePair(event, outbox, expected); err != nil {
		t.Fatalf("exact mutation-bound evidence = %v", err)
	}
	transaction := &fakeCommandTx{}
	if err := insertEvidence(context.Background(), transaction, event, outbox); catalog.ErrorCodeOf(err) != catalog.CodeInternal || len(transaction.statements) != 0 {
		t.Fatalf("unbound version-1 mutation evidence returned %v after %d writes", err, len(transaction.statements))
	}
	legacy := event
	legacy.FormatVersion = nil
	if err := validateEvidencePair(legacy, catalog.OutboxEvent{}); err == nil {
		t.Fatal("unversioned evidence pair was accepted")
	}
	for name, binding := range map[string]evidenceExpectation{
		"tenant":            expectEvidence("00000000-0000-7000-8000-000000000099", event.TargetType, event.TargetID, event.Action),
		"target type":       expectEvidence(event.TenantID, "publisher", event.TargetID, event.Action),
		"target identifier": expectEvidence(event.TenantID, event.TargetType, "00000000-0000-7000-8000-000000000099", event.Action),
		"action":            expectEvidence(event.TenantID, event.TargetType, event.TargetID, "namespace.updated"),
	} {
		t.Run("mutation "+name, func(t *testing.T) {
			transaction := &fakeCommandTx{}
			if err := insertEvidence(context.Background(), transaction, event, outbox, binding); catalog.ErrorCodeOf(err) != catalog.CodeInternal || len(transaction.statements) != 0 {
				t.Fatalf("mis-bound mutation evidence returned %v after %d writes", err, len(transaction.statements))
			}
		})
	}
	if err := validateEvidencePair(event, outbox, expected, expected); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("ambiguous mutation binding returned %v", err)
	}

	for name, mutate := range map[string]func(*catalog.OutboxEvent){
		"tenant":  func(value *catalog.OutboxEvent) { value.TenantID = "00000000-0000-7000-8000-000000000099" },
		"subject": func(value *catalog.OutboxEvent) { value.Subject = "00000000-0000-7000-8000-000000000099" },
		"type":    func(value *catalog.OutboxEvent) { value.Type = catalog.CanonicalEventType("publisher.created") },
		"data": func(value *catalog.OutboxEvent) {
			value.Data = json.RawMessage(`{"action":"namespace.created","targetType":"namespace","targetId":"00000000-0000-7000-8000-000000000099"}`)
		},
		"time": func(value *catalog.OutboxEvent) { value.CreatedAt = now.Add(time.Second) },
		"published before creation": func(value *catalog.OutboxEvent) {
			publishedAt := now.Add(-time.Second)
			value.PublishedAt = &publishedAt
		},
		"lease owner": func(value *catalog.OutboxEvent) { value.LeaseOwner = "worker" },
		"lease expiry": func(value *catalog.OutboxEvent) {
			expiresAt := now.Add(time.Minute)
			value.LeaseExpiresAt = &expiresAt
		},
		"internal": func(value *catalog.OutboxEvent) { value.Internal = true },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := outbox
			mutate(&candidate)
			transaction := &fakeCommandTx{}
			if err := insertEvidence(context.Background(), transaction, event, candidate, expected); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatalf("mis-bound evidence returned %v", err)
			}
			if len(transaction.statements) != 0 {
				t.Fatalf("mis-bound evidence executed %d statements", len(transaction.statements))
			}
		})
	}
	terminalOutbox := outbox
	publishedAt := now.Add(time.Second)
	terminalOutbox.PublishedAt = &publishedAt
	if err := validateEvidencePair(event, terminalOutbox, expected); err != nil {
		t.Fatalf("later terminal outbox acknowledgement = %v", err)
	}
}

func TestDatabasePublicOutboxUsesTheClosedEvidenceContract(t *testing.T) {
	t.Parallel()

	tenantID := "00000000-0000-7000-8000-000000000001"
	targetID := "00000000-0000-7000-8000-000000000002"
	outboxID := "00000000-0000-7000-8000-000000000003"
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	record, err := audit.NewRecord("00000000-0000-7000-8000-000000000004", tenantID, audit.Principal{ID: "catalog_schema_owner", Type: audit.PrincipalDatabaseLogin}, "identity.realm.suspended", "tenant_identity_realm", targetID, "planned maintenance", audit.Execution{CurrentUser: "catalog_schema_owner"}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	transaction := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, outboxID)
		return nil
	})}}
	outbox, err := newDatabasePublicOutbox(context.Background(), transaction, record, true)
	if err != nil {
		t.Fatal(err)
	}
	if outbox.ID != outboxID || outbox.Type != events.CanonicalType(record.Action) || outbox.TenantID != tenantID || outbox.Subject != targetID || !outbox.CreatedAt.Equal(record.At) || outbox.PublishedAt == nil || !outbox.PublishedAt.Equal(record.At) {
		t.Fatalf("database outbox = %#v", outbox)
	}
	if err := validateEvidencePair(record, outbox, expectEvidence(tenantID, record.TargetType, targetID, record.Action)); err != nil {
		t.Fatalf("database evidence pair = %v", err)
	}
	var reference events.NotificationReference
	if err := json.Unmarshal(outbox.Data, &reference); err != nil || reference.Action != "identity.realm.suspended" || reference.TargetType != record.TargetType || reference.TargetID != targetID {
		t.Fatalf("database notification reference = %#v, %v", reference, err)
	}
	unacknowledged := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, outboxID)
		return nil
	})}}
	pending, err := newDatabasePublicOutbox(context.Background(), unacknowledged, record, false)
	if err != nil || pending.PublishedAt != nil {
		t.Fatalf("unacknowledged database outbox = %#v, %v", pending, err)
	}
	nonPublic := record
	nonPublic.Action = "audit.exported"
	nonPublic.TargetType = "audit_export"
	invalid := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, outboxID)
		return nil
	})}}
	if _, err := newDatabasePublicOutbox(context.Background(), invalid, nonPublic, false); err == nil || !strings.Contains(err.Error(), "construct database outbox event") {
		t.Fatalf("non-public database outbox = %v", err)
	}

	failing := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(...any) error { return errors.New("identifier unavailable") })}}
	if _, err := newDatabasePublicOutbox(context.Background(), failing, record, false); err == nil || !strings.Contains(err.Error(), "outbox identifier") {
		t.Fatalf("database outbox identifier failure = %v", err)
	}
}

func TestDatabaseAuditRecordUsesTheDatabasePrincipal(t *testing.T) {
	t.Parallel()

	tenantID := "00000000-0000-7000-8000-000000000001"
	targetID := "00000000-0000-7000-8000-000000000002"
	recordID := "00000000-0000-7000-8000-000000000003"
	at := time.Date(2026, time.September, 3, 13, 0, 0, 123456789, time.FixedZone("operator", 3600))
	transaction := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, recordID)
		setScanDestination(t, destinations, 1, "catalog_operator")
		setScanDestination(t, destinations, 2, "catalog_schema_owner")
		return nil
	})}}
	record, err := newDatabaseAuditRecord(context.Background(), transaction, tenantID, "identity.realm.suspended", "tenant_identity_realm", targetID, "  planned identity maintenance  ", "change-42", nil, at)
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != recordID || record.Actor != "catalog_operator" || record.Reason != "planned identity maintenance" || record.At.Location() != time.UTC || record.At.Nanosecond()%1000 != 0 {
		t.Fatalf("database audit record = %#v", record)
	}
	if record.Details["principalType"] != audit.PrincipalDatabaseLogin || record.Details["currentUser"] != "catalog_schema_owner" || record.Details["operatorLabel"] != "change-42" {
		t.Fatalf("database audit attribution = %#v", record.Details)
	}
	if len(transaction.queryRowStatements) != 1 || !strings.Contains(transaction.queryRowStatements[0], "session_user,current_user") {
		t.Fatalf("database attribution query = %#v", transaction.queryRowStatements)
	}

	queryFailure := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(...any) error { return errors.New("attribution unavailable") })}}
	if _, err := newDatabaseAuditRecord(context.Background(), queryFailure, tenantID, "identity.realm.suspended", "tenant_identity_realm", targetID, "planned maintenance", "change-42", nil, at); err == nil || !strings.Contains(err.Error(), "create database audit attribution") {
		t.Fatalf("database attribution failure = %v", err)
	}

	invalidIdentity := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, "not-a-uuid")
		setScanDestination(t, destinations, 1, "catalog_operator")
		setScanDestination(t, destinations, 2, "catalog_schema_owner")
		return nil
	})}}
	if _, err := newDatabaseAuditRecord(context.Background(), invalidIdentity, tenantID, "identity.realm.suspended", "tenant_identity_realm", targetID, "planned maintenance", "change-42", nil, at); err == nil || !strings.Contains(err.Error(), "construct database audit event") {
		t.Fatalf("invalid database audit identity = %v", err)
	}
}

func TestDomainEvidenceBundlesAreBoundBeforePostgresWrites(t *testing.T) {
	t.Parallel()

	tenantID := "00000000-0000-7000-8000-000000000001"
	targetID := "00000000-0000-7000-8000-000000000002"
	otherID := "00000000-0000-7000-8000-000000000003"
	record := audit.Record{ID: "00000000-0000-7000-8000-000000000004", TenantID: tenantID, TargetID: targetID}
	outbox := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000005", TenantID: tenantID, Subject: targetID}

	for _, testCase := range []struct {
		name   string
		invoke func(pgx.Tx) error
	}{
		{name: "federation audit tenant", invoke: func(tx pgx.Tx) error {
			wrong := record
			wrong.TenantID = otherID
			return insertFederationEvidence(context.Background(), tx, tenantID, targetID, []federation.MutationEvidence{{Audit: wrong}})
		}},
		{name: "federation outbox target", invoke: func(tx pgx.Tx) error {
			wrong := outbox
			wrong.Subject = otherID
			return insertFederationEvidence(context.Background(), tx, tenantID, targetID, []federation.MutationEvidence{{Audit: record, Outbox: &wrong}})
		}},
		{name: "publication audit target", invoke: func(tx pgx.Tx) error {
			wrong := record
			wrong.TargetID = otherID
			return insertPublicationEvidence(context.Background(), tx, tenantID, targetID, []publication.MutationEvidence{{Audit: wrong}})
		}},
		{name: "publication outbox tenant", invoke: func(tx pgx.Tx) error {
			wrong := outbox
			wrong.TenantID = otherID
			return insertPublicationEvidence(context.Background(), tx, tenantID, targetID, []publication.MutationEvidence{{Audit: record, Outbox: &wrong}})
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			tx := &fakeCommandTx{}
			if err := testCase.invoke(tx); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatalf("mismatched evidence returned %v", err)
			}
			if len(tx.statements) != 0 {
				t.Fatalf("mismatched evidence reached PostgreSQL: %#v", tx.statements)
			}
		})
	}
}

func TestPostgresDomainEvidenceInsertionIsOrderedAndFailClosed(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	tenantID := "00000000-0000-7000-8000-000000000001"
	digest := "sha256:" + strings.Repeat("a", 64)
	principal := audit.Principal{ID: "00000000-0000-7000-8000-000000000002", Type: audit.PrincipalCatalogMembership, ActorType: "human", RealmID: "00000000-0000-7000-8000-000000000003"}
	sourceID := "00000000-0000-7000-8000-000000000004"
	sourceAudit, err := audit.NewRecord("00000000-0000-7000-8000-000000000005", tenantID, principal, audit.ActionSourcePaused, "federation_source", sourceID, "Pause unavailable source", audit.Execution{}, map[string]any{"beforeState": "active", "afterState": "paused", "beforeRevision": 1, "afterRevision": 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	sourceData, err := events.MarshalReference(sourceAudit.Action, sourceAudit.TargetType, sourceAudit.TargetID, sourceAudit.AfterDigest)
	if err != nil {
		t.Fatal(err)
	}
	sourceOutbox := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000006", TenantID: tenantID, Type: catalog.CanonicalEventType(sourceAudit.Action), Subject: sourceID, Data: sourceData, CreatedAt: sourceAudit.At}
	sourceInspectionAudit, err := audit.NewRecord("00000000-0000-7000-8000-000000000010", tenantID, principal, audit.ActionSourceInspected, "source_inspection", sourceID, "", audit.Execution{}, map[string]any{"outcome": "succeeded", "originClass": "public", "policyDigest": digest, "configurationDigest": digest, "inspectionDigest": digest, "expiresAt": now.Add(time.Hour).Format(time.RFC3339Nano)}, now)
	if err != nil {
		t.Fatal(err)
	}
	sourceAuditOnly := []federation.MutationEvidence{{Audit: sourceInspectionAudit}}
	sourcePaired := []federation.MutationEvidence{{Audit: sourceAudit, Outbox: &sourceOutbox}}

	publicationID := "00000000-0000-7000-8000-000000000007"
	publicationAudit, err := audit.NewRecord("00000000-0000-7000-8000-000000000008", tenantID, principal, audit.ActionPublicationActivated, "publication_snapshot", publicationID, "Activate reviewed publication", audit.Execution{}, map[string]any{"host": "catalog.example.com", "generation": 1, "ardProfile": "ard-v1", "aiCatalogProfile": "ai-v1", "ardDigest": digest, "aiCatalogDigest": digest}, now)
	if err != nil {
		t.Fatal(err)
	}
	publicationData, err := events.MarshalReferenceWithHost(publicationAudit.Action, publicationAudit.TargetType, publicationAudit.TargetID, publicationAudit.AfterDigest, "catalog.example.com")
	if err != nil {
		t.Fatal(err)
	}
	publicationOutbox := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000009", TenantID: tenantID, Type: catalog.CanonicalEventType(publicationAudit.Action), Subject: publicationID, Data: publicationData, CreatedAt: publicationAudit.At}
	publicationCandidateAudit, err := audit.NewRecord("00000000-0000-7000-8000-000000000011", tenantID, principal, audit.ActionPublicationCandidateCreated, "publication_snapshot", publicationID, "", audit.Execution{}, map[string]any{"host": "catalog.example.com", "audience": "anonymous", "generation": 1, "ardProfile": "ard-v1", "aiCatalogProfile": "ai-v1", "ardDigest": digest, "aiCatalogDigest": digest, "entryCount": 0, "exclusionCount": 0, "trigger": "direct"}, now)
	if err != nil {
		t.Fatal(err)
	}
	publicationAuditOnly := []publication.MutationEvidence{{Audit: publicationCandidateAudit}}
	publicationPaired := []publication.MutationEvidence{{Audit: publicationAudit, Outbox: &publicationOutbox}}

	for _, testCase := range []struct {
		name       string
		wantWrites int
		write      func(pgx.Tx) error
	}{
		{name: "federation absent", write: func(tx pgx.Tx) error {
			return insertFederationEvidence(context.Background(), tx, tenantID, sourceID, nil)
		}},
		{name: "federation disabled placeholder", write: func(tx pgx.Tx) error {
			return insertFederationEvidence(context.Background(), tx, tenantID, sourceID, []federation.MutationEvidence{{}})
		}},
		{name: "federation audit only", wantWrites: 1, write: func(tx pgx.Tx) error {
			return insertFederationEvidence(context.Background(), tx, tenantID, sourceID, sourceAuditOnly)
		}},
		{name: "federation paired", wantWrites: 2, write: func(tx pgx.Tx) error {
			return insertFederationEvidence(context.Background(), tx, tenantID, sourceID, sourcePaired)
		}},
		{name: "publication absent", write: func(tx pgx.Tx) error {
			return insertPublicationEvidence(context.Background(), tx, tenantID, publicationID, nil)
		}},
		{name: "publication disabled placeholder", write: func(tx pgx.Tx) error {
			return insertPublicationEvidence(context.Background(), tx, tenantID, publicationID, []publication.MutationEvidence{{}})
		}},
		{name: "publication audit only", wantWrites: 1, write: func(tx pgx.Tx) error {
			return insertPublicationEvidence(context.Background(), tx, tenantID, publicationID, publicationAuditOnly)
		}},
		{name: "publication paired", wantWrites: 2, write: func(tx pgx.Tx) error {
			return insertPublicationEvidence(context.Background(), tx, tenantID, publicationID, publicationPaired)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			tx := &fakeCommandTx{}
			if err := testCase.write(tx); err != nil {
				t.Fatal(err)
			}
			if len(tx.statements) != testCase.wantWrites {
				t.Fatalf("evidence writes = %d, want %d", len(tx.statements), testCase.wantWrites)
			}
		})
	}

	for _, testCase := range []struct {
		name  string
		write func(pgx.Tx) error
	}{
		{name: "federation audit", write: func(tx pgx.Tx) error {
			return insertFederationEvidence(context.Background(), tx, tenantID, sourceID, sourcePaired)
		}},
		{name: "federation outbox", write: func(tx pgx.Tx) error {
			return insertFederationEvidence(context.Background(), tx, tenantID, sourceID, sourcePaired)
		}},
		{name: "publication audit", write: func(tx pgx.Tx) error {
			return insertPublicationEvidence(context.Background(), tx, tenantID, publicationID, publicationPaired)
		}},
		{name: "publication outbox", write: func(tx pgx.Tx) error {
			return insertPublicationEvidence(context.Background(), tx, tenantID, publicationID, publicationPaired)
		}},
	} {
		t.Run(testCase.name+" failure", func(t *testing.T) {
			execErrors := []error{errors.New("audit unavailable")}
			if strings.Contains(testCase.name, "outbox") {
				execErrors = []error{nil, errors.New("outbox unavailable")}
			}
			tx := &fakeCommandTx{execErrors: execErrors}
			if err := testCase.write(tx); err == nil {
				t.Fatal("evidence write failure was discarded")
			}
			if got, want := len(tx.statements), len(execErrors); got != want {
				t.Fatalf("evidence writes = %d, want %d", got, want)
			}
		})
	}
}

func TestPostgresJobEvidenceFinalizationIsAtomic(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	tenantID := "00000000-0000-7000-8000-000000000001"
	principal := audit.Principal{ID: "00000000-0000-7000-8000-000000000002", Type: audit.PrincipalCatalogMembership, ActorType: "human", RealmID: "00000000-0000-7000-8000-000000000003"}
	sourceID := "00000000-0000-7000-8000-000000000004"
	job := jobs.Job{ID: "00000000-0000-7000-8000-000000000005", TenantID: tenantID, Kind: "source.sync", Actor: principal.ID, PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":1,"trigger":"manual"}`), State: jobs.StateQueued, AttemptLimit: 5, DeduplicationKey: "source.sync:" + sourceID + ":1", CreatedAt: now, UpdatedAt: now}
	enqueueAudit, err := audit.NewRecord("00000000-0000-7000-8000-000000000006", tenantID, principal, audit.ActionSourceSyncRequested, "federation_source", sourceID, "", audit.Execution{}, map[string]any{"sourceRevision": 1, "jobId": job.ID, "trigger": "manual", "deduplicated": false}, now)
	if err != nil {
		t.Fatal(err)
	}

	if err := insertJobEnqueueEvidence(context.Background(), &fakeCommandTx{}, nil, job, false); err != nil {
		t.Fatal(err)
	}
	if err := insertJobEnqueueEvidence(context.Background(), &fakeCommandTx{}, []audit.Record{enqueueAudit, enqueueAudit}, job, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("ambiguous enqueue evidence = %v", err)
	}
	enqueueTx := &fakeCommandTx{}
	if err := insertJobEnqueueEvidence(context.Background(), enqueueTx, []audit.Record{enqueueAudit}, job, true); err != nil || len(enqueueTx.statements) != 1 {
		t.Fatalf("enqueue evidence error=%v writes=%d", err, len(enqueueTx.statements))
	}
	wrongTenant := job
	wrongTenant.TenantID = "00000000-0000-7000-8000-000000000099"
	if err := insertJobEnqueueEvidence(context.Background(), &fakeCommandTx{}, []audit.Record{enqueueAudit}, wrongTenant, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("cross-tenant enqueue evidence = %v", err)
	}
	if err := insertJobEnqueueEvidence(context.Background(), &fakeCommandTx{execErrors: []error{errors.New("audit unavailable")}}, []audit.Record{enqueueAudit}, job, false); err == nil {
		t.Fatal("enqueue audit failure was discarded")
	}

	after := job
	after.State = jobs.StateCancelled
	after.UpdatedAt = now.Add(time.Second)
	cancelAudit, err := audit.NewRecord("00000000-0000-7000-8000-000000000007", tenantID, principal, audit.ActionJobCancellationRequested, "job", job.ID, "Cancel obsolete work", audit.Execution{}, map[string]any{"kind": job.Kind, "beforeState": string(job.State), "afterState": string(after.State), "cancellationRequested": false}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertJobCancellationEvidence(context.Background(), &fakeCommandTx{}, nil, job, after); err != nil {
		t.Fatal(err)
	}
	if err := insertJobCancellationEvidence(context.Background(), &fakeCommandTx{}, []audit.Record{cancelAudit, cancelAudit}, job, after); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("ambiguous cancellation evidence = %v", err)
	}
	cancelTx := &fakeCommandTx{}
	if err := insertJobCancellationEvidence(context.Background(), cancelTx, []audit.Record{cancelAudit}, job, after); err != nil || len(cancelTx.statements) != 1 {
		t.Fatalf("cancellation evidence error=%v writes=%d", err, len(cancelTx.statements))
	}
	wrongJob := after
	wrongJob.ID = "00000000-0000-7000-8000-000000000099"
	if err := insertJobCancellationEvidence(context.Background(), &fakeCommandTx{}, []audit.Record{cancelAudit}, job, wrongJob); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("cross-Job cancellation evidence = %v", err)
	}
	if err := insertJobCancellationEvidence(context.Background(), &fakeCommandTx{execErrors: []error{errors.New("audit unavailable")}}, []audit.Record{cancelAudit}, job, after); err == nil {
		t.Fatal("cancellation audit failure was discarded")
	}
}

func TestPostgresPublicationPointerAndFederationExpectationChecks(t *testing.T) {
	t.Parallel()

	currentID := "00000000-0000-7000-8000-000000000001"
	if err := checkExpectedActivePublication(context.Background(), &fakeCommandTx{}, "tenant", "catalog.example.com", nil); err != nil {
		t.Fatal(err)
	}
	if err := checkExpectedActivePublication(context.Background(), &fakeCommandTx{}, "tenant", "catalog.example.com", []publication.MutationEvidence{{}}); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name     string
		expected string
		row      pgx.Row
		wantCode catalog.ErrorCode
	}{
		{name: "empty matches no row", row: scannerFunc(func(...any) error { return pgx.ErrNoRows })},
		{name: "expected current disappeared", expected: currentID, row: scannerFunc(func(...any) error { return pgx.ErrNoRows }), wantCode: catalog.CodePreconditionFailed},
		{name: "empty differs from current", row: scannerFunc(func(destinations ...any) error { setScanDestination(t, destinations, 0, currentID); return nil }), wantCode: catalog.CodePreconditionFailed},
		{name: "current matches", expected: currentID, row: scannerFunc(func(destinations ...any) error { setScanDestination(t, destinations, 0, currentID); return nil })},
		{name: "current differs", expected: "00000000-0000-7000-8000-000000000002", row: scannerFunc(func(destinations ...any) error { setScanDestination(t, destinations, 0, currentID); return nil }), wantCode: catalog.CodePreconditionFailed},
		{name: "query failure", row: scannerFunc(func(...any) error { return errors.New("database unavailable") }), wantCode: catalog.CodeDependencyUnavailable},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{testCase.row}}
			expected := testCase.expected
			err := checkExpectedActivePublication(context.Background(), tx, "tenant", "catalog.example.com", []publication.MutationEvidence{{ExpectedActiveID: &expected}})
			if testCase.wantCode == "" {
				if err != nil {
					t.Fatalf("pointer check = %v", err)
				}
			} else if got := catalog.ErrorCodeOf(err); got != testCase.wantCode {
				t.Fatalf("pointer check code = %s, want %s (%v)", got, testCase.wantCode, err)
			}
			if len(tx.queryRowStatements) != 1 {
				t.Fatalf("pointer check queries = %d", len(tx.queryRowStatements))
			}
		})
	}

	set := &federation.ObservationSet{}
	if federationExpectedObservationSet(nil) != nil || federationExpectedObservationSet([]federation.MutationEvidence{{}, {}}) != nil {
		t.Fatal("ambiguous federation expectation was accepted")
	}
	if got := federationExpectedObservationSet([]federation.MutationEvidence{{ExpectedObservationSet: set}}); got != set {
		t.Fatalf("federation expectation = %p, want %p", got, set)
	}
}

func TestDatabaseErrorTranslationAndJSONHelpers(t *testing.T) {
	t.Parallel()
	if translateError("operation", nil) != nil {
		t.Fatal("nil database error was translated")
	}
	if code := catalog.ErrorCodeOf(translateError("read", pgx.ErrNoRows)); code != catalog.CodeNotFound {
		t.Fatalf("no rows code = %s", code)
	}
	for postgresCode, expected := range map[string]catalog.ErrorCode{
		"23505": catalog.CodeConflict,
		"23503": catalog.CodeInvalidArgument,
		"23514": catalog.CodeInvalidArgument,
		"22P02": catalog.CodeInvalidArgument,
		"22003": catalog.CodeInvalidArgument,
		"40001": catalog.CodeConflict,
		"40P01": catalog.CodeConflict,
		"42501": catalog.CodeForbidden,
		"57P01": catalog.CodeDependencyUnavailable,
	} {
		cause := &pgconn.PgError{Code: postgresCode}
		for _, original := range []error{cause, fmt.Errorf("driver: %w", cause)} {
			err := translateError("write", original)
			if code := catalog.ErrorCodeOf(err); code != expected || !errors.Is(err, cause) || !errors.Is(err, original) {
				t.Fatalf("PostgreSQL code %s translated to %s, want %s with retained cause: %v", postgresCode, code, expected, err)
			}
		}
	}
	if code := catalog.ErrorCodeOf(translateError("query", errors.New("network"))); code != catalog.CodeDependencyUnavailable {
		t.Fatalf("generic database error code = %s", code)
	}

	raw, err := marshalJSON(map[string]string{"key": "value"})
	if err != nil || string(raw) != `{"key":"value"}` {
		t.Fatalf("marshaled JSON = %s, %v", raw, err)
	}
	if _, err := marshalJSON(make(chan int)); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("invalid marshal error = %v", err)
	}
	var decoded map[string]string
	if err := unmarshalJSON(raw, &decoded); err != nil || decoded["key"] != "value" {
		t.Fatalf("decoded JSON = %#v, %v", decoded, err)
	}
	for _, empty := range [][]byte{nil, []byte("null")} {
		if err := unmarshalJSON(empty, &decoded); err != nil {
			t.Fatalf("empty JSON returned %v", err)
		}
	}
	if err := unmarshalJSON([]byte("{"), &decoded); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("invalid unmarshal error = %v", err)
	}
}

func TestCatalogDocumentEqualityIncludesStoredIdentityAndBytes(t *testing.T) {
	t.Parallel()

	document := catalog.DocumentBlob{
		ID:        "document",
		TenantID:  "tenant",
		Digest:    "sha256:digest",
		MediaType: "application/json",
		Size:      2,
		Content:   []byte("{}"),
	}
	if !sameCatalogDocument(document, document) {
		t.Fatal("identical Catalog documents were not equal")
	}
	for name, mutate := range map[string]func(*catalog.DocumentBlob){
		"identifier": func(candidate *catalog.DocumentBlob) { candidate.ID = "different" },
		"tenant":     func(candidate *catalog.DocumentBlob) { candidate.TenantID = "different" },
		"digest":     func(candidate *catalog.DocumentBlob) { candidate.Digest = "sha256:different" },
		"media type": func(candidate *catalog.DocumentBlob) { candidate.MediaType = "text/plain" },
		"size":       func(candidate *catalog.DocumentBlob) { candidate.Size++ },
		"content":    func(candidate *catalog.DocumentBlob) { candidate.Content = []byte("[]") },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := document
			mutate(&candidate)
			if sameCatalogDocument(document, candidate) {
				t.Fatal("different Catalog documents were equal")
			}
		})
	}
}

func TestPostgresIdentityEncodingScanningAndErrorTranslation(t *testing.T) {
	t.Parallel()

	if copyStringMap(nil) != nil {
		t.Fatal("nil identity claims became a non-nil map")
	}
	original := map[string]string{"scope:catalog.read": "true"}
	cloned := copyStringMap(original)
	cloned["scope:catalog.read"] = "changed"
	if original["scope:catalog.read"] != "true" {
		t.Fatal("identity claims alias the caller map")
	}
	roles := sortedIdentityRoles([]identity.Role{identity.RoleReader, identity.RoleContributor, identity.RoleReader})
	if len(roles) != 2 || roles[0] != identity.RoleContributor || roles[1] != identity.RoleReader {
		t.Fatalf("canonical identity roles = %#v", roles)
	}
	if !stringSliceContains([]string{"one", "two"}, "two") || stringSliceContains([]string{"one", "two"}, "three") {
		t.Fatal("publication-domain membership comparison was not exact")
	}

	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	assignment, err := scanRoleAssignment(scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, "assignment")
		setScanDestination(t, destinations, 1, "tenant")
		setScanDestination(t, destinations, 2, "membership")
		setScanDestination(t, destinations, 3, "subject")
		setScanDestination(t, destinations, 4, identity.ActorService)
		setScanDestination(t, destinations, 5, identity.RoleReader)
		setScanDestination(t, destinations, 6, "")
		setScanDestination(t, destinations, 7, identity.AssignmentRevoked)
		setScanDestination(t, destinations, 8, int64(2))
		setScanDestination(t, destinations, 9, "admin")
		setScanDestination(t, destinations, 10, "grant")
		setScanDestination(t, destinations, 11, now)
		setScanDestination(t, destinations, 12, "admin")
		setScanDestination(t, destinations, 13, "revoke")
		setScanDestination(t, destinations, 14, &now)
		return nil
	}))
	if err != nil || assignment.ID != "assignment" || assignment.Role != identity.RoleReader || assignment.RevokedAt == nil || !assignment.RevokedAt.Equal(now) {
		t.Fatalf("scanned role assignment = %#v, %v", assignment, err)
	}
	scanErr := identity.E(identity.CodeConflict, "scan failed")
	if _, err := scanRoleAssignment(scannerFunc(func(...any) error { return scanErr })); !errors.Is(err, scanErr) {
		t.Fatalf("scan error = %v", err)
	}

	if translateIdentityError("operation", nil) != nil {
		t.Fatal("nil identity database error was translated")
	}
	identityErr := identity.E(identity.CodeForbidden, "denied")
	if translated := translateIdentityError("operation", identityErr); !errors.Is(translated, identityErr) {
		t.Fatalf("typed identity error was replaced: %v", translated)
	}
	if code := identity.ErrorCodeOf(translateIdentityError("read", pgx.ErrNoRows)); code != identity.CodeNotFound {
		t.Fatalf("identity no-rows code = %s", code)
	}
	for postgresCode, expected := range map[string]identity.ErrorCode{
		"23505": identity.CodeConflict,
		"23503": identity.CodeInvalidArgument,
		"23514": identity.CodeInvalidArgument,
		"22P02": identity.CodeInvalidArgument,
		"40001": identity.CodeConflict,
		"40P01": identity.CodeConflict,
		"42501": identity.CodeForbidden,
	} {
		translated := translateIdentityError("write", &pgconn.PgError{Code: postgresCode})
		if code := identity.ErrorCodeOf(translated); code != expected {
			t.Fatalf("identity PostgreSQL code %s translated to %s, want %s", postgresCode, code, expected)
		}
	}
	if code := identity.ErrorCodeOf(translateIdentityError("query", errors.New("network"))); code != identity.CodeDependency {
		t.Fatalf("generic identity database error code = %s", code)
	}

	tenantID := "00000000-0000-7000-8000-000000000001"
	evidence := currentTestIdentityEvidence(t, "identity.role_assignment.granted", "role_assignment", "00000000-0000-7000-8000-000000000002", now)
	transaction := &fakeCommandTx{}
	if err := insertIdentityEvidence(context.Background(), transaction, tenantID, evidence, expectEvidence(tenantID, evidence.TargetType, evidence.TargetID, evidence.Action)); err != nil || len(transaction.statements) != 2 {
		t.Fatalf("identity evidence writes = %d, %v", len(transaction.statements), err)
	}
	if err := insertIdentityEvidence(context.Background(), &fakeCommandTx{execErrors: []error{errors.New("write failed")}}, tenantID, evidence, expectEvidence(tenantID, evidence.TargetType, evidence.TargetID, evidence.Action)); identity.ErrorCodeOf(err) != identity.CodeDependency {
		t.Fatalf("identity evidence failure = %v", err)
	}
}
