// Publication adapter tests use static retained snapshots and scripted transactions.
package database

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// publicationRepositoryEvidence builds current audit/outbox evidence from the static snapshot, using the audit encoder's normalized event time.
func publicationRepositoryEvidence(t *testing.T, s publication.Snapshot, action string, at time.Time) publication.MutationEvidence {
	t.Helper()
	digest := "sha256:" + strings.Trim(s.ETag, `"`)
	facts := map[string]any{"host": s.Host, "generation": s.Generation, "ardProfile": s.ARDProfile, "aiCatalogProfile": s.AICatalogProfile,
		"ardDigest": digest, "aiCatalogDigest": "sha256:" + strings.Trim(s.AICatalogETag, `"`)}
	if action == audit.ActionPublicationUnpublished {
		facts = map[string]any{"host": s.Host, "generation": s.Generation, "cause": "explicit_unpublish", "originatingAction": "publication.unpublish", "originatingTargetId": s.ID}
	}
	if action == audit.ActionPublicationCandidateCreated {
		facts["audience"], facts["entryCount"], facts["exclusionCount"], facts["trigger"] = string(s.Audience), len(s.AICatalog.Entries), len(s.Exclusions), "direct"
	}
	record, err := audit.NewRecord("00000000-0000-7000-8000-000000000098", s.TenantID, audit.Principal{ID: "catalog-api", Type: audit.PrincipalServiceExecutor}, action, "publication_snapshot", s.ID, "reviewed publication transition", audit.Execution{}, facts, at)
	if err != nil {
		t.Fatal(err)
	}
	record.AfterDigest = digest
	result := publication.MutationEvidence{Audit: record}
	if action != audit.ActionPublicationCandidateCreated {
		publicAction := "publication.activated"
		if action == audit.ActionPublicationUnpublished {
			publicAction = "publication.unpublished"
		}
		data, err := events.MarshalReferenceWithHost(publicAction, record.TargetType, s.ID, digest, s.Host)
		if err != nil {
			t.Fatal(err)
		}
		result.Outbox = &catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000099", TenantID: s.TenantID, Type: events.CanonicalType(publicAction), Subject: s.ID, Data: data, CreatedAt: record.At}
	}
	if err := publication.ValidateMutationEvidence(s.TenantID, s.ID, []publication.MutationEvidence{result}, action); err != nil {
		t.Fatalf("invalid evidence fixture: %v", err)
	}
	return result
}

// TestPublicationRepositoryConstructionAndReads checks store admission, host/tenant query bindings and absence without poisoning an active command.
func TestPublicationRepositoryConstructionAndReads(t *testing.T) {
	pool := &pgxpool.Pool{}
	repository := &PostgresRepository{pool: pool}
	for _, test := range []struct {
		repo *PostgresRepository
		host string
	}{{nil, "catalog.example.com"}, {repository, ""}} {
		if store, err := NewPostgresPublicationStore(test.repo, test.host); err == nil || store != nil {
			t.Fatalf("invalid constructor: %v, %v", store, err)
		}
	}
	s := publicationScanFixture(t)
	store, err := NewPostgresPublicationStore(repository, s.Host)
	if err != nil || store.repository != repository || store.host != s.Host {
		t.Fatalf("construction: %v", err)
	}
	failure := errors.New("publication read failure")
	for _, operation := range []string{"get", "active"} {
		active := operation == "active"
		for _, outcome := range []string{"found", "missing", "failure"} {
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				want := s
				args := []any{s.TenantID, s.ID, s.Host, publicationBundleFamily}
				if active {
					want.State = publication.SnapshotActive
					want.ActivatedAt = new(s.CreatedAt.Add(time.Hour))
					args = []any{s.TenantID, s.Host, publicationBundleFamily, publication.SnapshotActive}
				}
				row := scriptedValueRow(publicationScanValues(t, want))
				code := catalog.ErrorCode("")
				if outcome == "missing" {
					row = scannerFunc(func(...any) error { return pgx.ErrNoRows })
					code = catalog.CodeNotFound
				}
				if outcome == "failure" {
					row = scannerFunc(func(...any) error { return failure })
					code = catalog.CodeDependencyUnavailable
				}
				tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{row}}
				ctx := identityRepositoryContext(pool, tx)
				var got publication.Snapshot
				var err error
				if active {
					got, err = store.Active(ctx, s.TenantID)
				} else {
					got, err = store.Get(ctx, s.TenantID, s.ID)
				}
				if (code == "" && err != nil || code != "" && (err == nil || catalog.ErrorCodeOf(err) != code)) || err == nil && !reflect.DeepEqual(got, want) || err != nil && !reflect.DeepEqual(got, publication.Snapshot{}) {
					t.Fatalf("read=%+v, %v", got, err)
				}
				if outcome == "failure" && !errors.Is(err, failure) {
					t.Fatalf("lost cause: %v", err)
				}
				if !reflect.DeepEqual(tx.queryRowArguments, [][]any{args}) {
					t.Fatalf("query binding: %v", tx.queryRowArguments)
				}
				rollback := 0
				if err != nil && !(active && outcome == "missing") {
					rollback = 1
				}
				if tx.rollbacks != rollback || tx.commits != 0 {
					t.Fatalf("transaction lifecycle: commits=%d rollbacks=%d", tx.commits, tx.rollbacks)
				}
				if active && outcome == "missing" && commandTransactionFrom(ctx).failed != nil {
					t.Fatal("normal absence poisoned the enclosing command")
				}
			})
		}
	}
	for _, outcome := range []string{"generation", "query failure", "wrong host"} {
		t.Run(outcome, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{int64(8)})}}
			host := s.Host
			code := catalog.ErrorCode("")
			if outcome == "query failure" {
				tx.queryRows = []pgx.Row{scannerFunc(func(...any) error { return failure })}
				code = catalog.CodeDependencyUnavailable
			}
			if outcome == "wrong host" {
				host = "other.example.com"
				code = catalog.CodePublicationNotReady
			}
			got, err := store.NextGeneration(identityRepositoryContext(pool, tx), s.TenantID, host)
			if (code == "" && err != nil || code != "" && (err == nil || catalog.ErrorCodeOf(err) != code)) || err == nil && got != 8 || err != nil && got != 0 {
				t.Fatalf("NextGeneration=%d, %v", got, err)
			}
			if outcome == "wrong host" {
				if len(tx.queryRowStatements) != 0 {
					t.Fatal("wrong host reached SQL")
				}
			} else if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{s.TenantID, s.Host, publicationBundleFamily}}) {
				t.Fatalf("generation binding: %v", tx.queryRowArguments)
			}
			if len(tx.statements) != 0 {
				t.Fatal("generation read reserved or wrote state")
			}
		})
	}
}

// TestPublicationRepositoryCandidate checks candidate admission, retained timestamp precision and audit-only write failure propagation.
func TestPublicationRepositoryCandidate(t *testing.T) {
	pool := &pgxpool.Pool{}
	failure := errors.New("candidate write failure")
	for _, outcome := range []string{"saved", "no evidence", "wrong host", "wrong state", "oversized", "insert failure", "audit failure", "wrong evidence action"} {
		t.Run(outcome, func(t *testing.T) {
			s := publicationScanFixture(t)
			store := &PostgresPublicationStore{repository: &PostgresRepository{pool: pool}, host: s.Host}
			evidence := []publication.MutationEvidence{publicationRepositoryEvidence(t, s, audit.ActionPublicationCandidateCreated, s.CreatedAt)}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
			code := catalog.ErrorCode("")
			writes := 2
			switch outcome {
			case "no evidence":
				evidence = nil
				writes = 1
			case "wrong host":
				s.Host = "other.example.com"
				code = catalog.CodePublicationNotReady
				writes = 0
			case "wrong state":
				s.State = publication.SnapshotActive
				code = catalog.CodePublicationNotReady
				writes = 0
			case "oversized":
				s.ArtifactIDs = make([]string, publication.MaxPublicationArtifactReferences+1)
				code = catalog.CodeWorkLimitExceeded
				writes = 0
			case "insert failure":
				tx.execErrors = []error{failure}
				code = catalog.CodeDependencyUnavailable
				writes = 1
			case "audit failure":
				tx.execErrors = []error{nil, failure}
				code = catalog.CodeDependencyUnavailable
			case "wrong evidence action":
				evidence = []publication.MutationEvidence{publicationRepositoryEvidence(t, s, audit.ActionPublicationActivated, s.CreatedAt)}
				code = catalog.CodeInternal
				writes = 1
			}
			got, err := store.SaveCandidate(identityRepositoryContext(pool, tx), s, evidence...)
			if (code == "" && err != nil || code != "" && (err == nil || catalog.ErrorCodeOf(err) != code)) || len(tx.statements) != writes || tx.commits != 0 {
				t.Fatalf("candidate=%+v err=%v writes=%v", got, err, tx.statements)
			}
			if err != nil {
				if !reflect.DeepEqual(got, publication.Snapshot{}) {
					t.Fatal("failed save returned candidate")
				}
				wantRollback := 0
				if writes > 0 {
					wantRollback = 1
				}
				if tx.rollbacks != wantRollback {
					t.Fatalf("rollbacks=%d", tx.rollbacks)
				}
				if code == catalog.CodeDependencyUnavailable && !errors.Is(err, failure) {
					t.Fatalf("lost cause: %v", err)
				}
				return
			}
			want := s
			want.CreatedAt = s.CreatedAt.UTC().Truncate(time.Microsecond)
			if !reflect.DeepEqual(got, want) || tx.rollbacks != 0 {
				t.Fatalf("saved document=%+v", got)
			}
			args := tx.arguments[0]
			raw, ok := args[8].([]byte)
			if !ok {
				t.Fatalf("document type=%T", args[8])
			}
			var retained publication.Snapshot
			if err := json.Unmarshal(raw, &retained); err != nil || !reflect.DeepEqual(retained, want) {
				t.Fatalf("retained document=%+v, %v", retained, err)
			}
			if !reflect.DeepEqual(args, []any{s.ID, s.TenantID, s.Host, publicationBundleFamily, s.ARDProfile + "+" + s.AICatalogProfile, s.Generation, s.State, s.ETag, raw, want.CreatedAt, s.ActivatedAt}) {
				t.Fatalf("insert binding=%v", args)
			}
			if len(evidence) > 0 && (!strings.Contains(tx.statements[1], "INSERT INTO audit_events") || tx.arguments[1][6] != s.ID) {
				t.Fatal("candidate audit not bound to snapshot")
			}
		})
	}
}

// TestPublicationRepositorySummaryFailures complements the existing nonempty summary decoder with empty pages and driver failures.
func TestPublicationRepositorySummaryFailures(t *testing.T) {
	pool := &pgxpool.Pool{}
	s := publicationScanFixture(t)
	store := &PostgresPublicationStore{repository: &PostgresRepository{pool: pool}, host: s.Host}
	failure := errors.New("summary read failure")
	for _, outcome := range []string{"empty", "query", "scan", "iteration"} {
		t.Run(outcome, func(t *testing.T) {
			rows := &identityScriptedRows{}
			result := identityQueryResult{rows: rows}
			code := catalog.CodeDependencyUnavailable
			switch outcome {
			case "empty":
				code = ""
			case "query":
				result.err = failure
			case "scan":
				rows.values = [][]any{{"partial"}}
			case "iteration":
				rows.err = failure
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{result}}
			request := publication.SnapshotListRequest{AfterGeneration: 7, AfterID: s.ID, Limit: 2, State: publication.SnapshotActive}
			got, err := store.ListSummaries(identityRepositoryContext(pool, tx), s.TenantID, request)
			if (code == "" && err != nil || code != "" && (err == nil || catalog.ErrorCodeOf(err) != code)) || err != nil && got != nil || err == nil && (got == nil || len(got) != 0) {
				t.Fatalf("summaries=%v, %v", got, err)
			}
			if outcome != "query" && !rows.closed {
				t.Fatal("rows not closed")
			}
			if !reflect.DeepEqual(tx.queryArguments, [][]any{{s.TenantID, s.Host, publicationBundleFamily, request.AfterGeneration, request.AfterID, request.Limit, request.State}}) {
				t.Fatalf("page binding: %v", tx.queryArguments)
			}
			if !strings.Contains(tx.queryStatements[0], "ORDER BY generation DESC,id DESC LIMIT $6") {
				t.Fatal("missing stable bounded selection")
			}
			if (outcome == "query" || outcome == "iteration") && !errors.Is(err, failure) {
				t.Fatalf("lost cause: %v", err)
			}
		})
	}
}

// The real engine owns locking and atomicity. These calls assert which statements,
// evidence and options the adapter supplies, and that failure stops later writes.
func TestPublicationRepositoryTransitions(t *testing.T) {
	pool := &pgxpool.Pool{}
	failure := errors.New("publication transition failure")
	for _, action := range []string{audit.ActionPublicationActivated, audit.ActionPublicationRolledBack, audit.ActionPublicationUnpublished} {
		outcomes := []string{"success", "lock failure", "tenant missing", "pointer failure", "pointer changed", "snapshot missing", "write failure", "audit failure", "outbox failure"}
		if action != audit.ActionPublicationUnpublished {
			outcomes = append(outcomes, "realm failure", "realm suspended", "wrong state", "retire failure")
		}
		for _, outcome := range outcomes {
			t.Run(action+"/"+outcome, func(t *testing.T) {
				s := publicationScanFixture(t)
				at := s.CreatedAt.Add(2 * time.Hour)
				expectedActive := "00000000-0000-7000-8000-000000000019"
				store := &PostgresPublicationStore{repository: &PostgresRepository{pool: pool}, host: s.Host}
				operation := store.Activate
				if action == audit.ActionPublicationRolledBack {
					operation = store.Rollback
					s.State = publication.SnapshotRetired
					s.ActivatedAt = new(s.CreatedAt.Add(time.Hour))
				}
				if action == audit.ActionPublicationUnpublished {
					operation = store.Deactivate
					s.State = publication.SnapshotActive
					s.ActivatedAt = new(s.CreatedAt.Add(time.Hour))
					expectedActive = s.ID
				}
				evidence := publicationRepositoryEvidence(t, s, action, at)
				evidence.ExpectedActiveID = &expectedActive
				tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
				tx.queryRows = []pgx.Row{scriptedValueRow([]any{s.TenantID})}
				if action != audit.ActionPublicationUnpublished {
					tx.queryRows = append(tx.queryRows, scriptedValueRow([]any{true}))
				}
				pointerIndex := len(tx.queryRows)
				tx.queryRows = append(tx.queryRows, scriptedValueRow([]any{expectedActive}))
				if outcome == "wrong state" {
					s.State = publication.SnapshotActive
					s.ActivatedAt = new(s.CreatedAt.Add(time.Hour))
				}
				tx.queryRows = append(tx.queryRows, scriptedValueRow(publicationScanValues(t, s)))
				code := catalog.ErrorCode("")
				// Exec order: tenant lock, optional prior retirement, target write, audit, outbox.
				targetWrite := 1
				if action != audit.ActionPublicationUnpublished {
					targetWrite = 2
				}
				wantWrites := targetWrite + 3
				wantRows := len(tx.queryRows)
				switch outcome {
				case "lock failure":
					tx.execErrors = []error{failure}
					code = catalog.CodeDependencyUnavailable
					wantWrites = 1
					wantRows = 0
				case "tenant missing":
					tx.queryRows[0] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
					code = catalog.CodeNotFound
					wantWrites = 1
					wantRows = 1
				case "realm failure":
					tx.queryRows[1] = scannerFunc(func(...any) error { return failure })
					code = catalog.CodeDependencyUnavailable
					wantWrites = 1
					wantRows = 2
				case "realm suspended":
					tx.queryRows[1] = scriptedValueRow([]any{false})
					code = catalog.CodePublicationNotReady
					wantWrites = 1
					wantRows = 2
				case "pointer failure":
					tx.queryRows[pointerIndex] = scannerFunc(func(...any) error { return failure })
					code = catalog.CodeDependencyUnavailable
					wantWrites = 1
					wantRows = pointerIndex + 1
				case "pointer changed":
					tx.queryRows[pointerIndex] = scriptedValueRow([]any{"00000000-0000-7000-8000-000000000018"})
					code = catalog.CodePreconditionFailed
					wantWrites = 1
					wantRows = pointerIndex + 1
				case "snapshot missing":
					tx.queryRows[len(tx.queryRows)-1] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
					code = catalog.CodeNotFound
					wantWrites = 1
				case "wrong state":
					code = catalog.CodeInvalidTransition
					wantWrites = 1
				case "retire failure":
					tx.execErrors = []error{nil, failure}
					code = catalog.CodeDependencyUnavailable
					wantWrites = 2
				case "write failure":
					tx.execErrors = make([]error, targetWrite+1)
					tx.execErrors[targetWrite] = failure
					code = catalog.CodeDependencyUnavailable
					wantWrites = targetWrite + 1
				case "audit failure":
					tx.execErrors = make([]error, targetWrite+2)
					tx.execErrors[targetWrite+1] = failure
					code = catalog.CodeDependencyUnavailable
					wantWrites = targetWrite + 2
				case "outbox failure":
					tx.execErrors = make([]error, targetWrite+3)
					tx.execErrors[targetWrite+2] = failure
					code = catalog.CodeDependencyUnavailable
				}
				got, err := operation(identityRepositoryContext(pool, tx), s.TenantID, s.ID, at, evidence)
				if (code == "" && err != nil || code != "" && (err == nil || catalog.ErrorCodeOf(err) != code)) || len(tx.statements) != wantWrites || len(tx.queryRowStatements) != wantRows || tx.commits != 0 {
					t.Fatalf("transition=%+v err=%v writes=%d/%d reads=%d/%d", got, err, len(tx.statements), wantWrites, len(tx.queryRowStatements), wantRows)
				}
				if err != nil {
					if !reflect.DeepEqual(got, publication.Snapshot{}) || tx.rollbacks != 1 {
						t.Fatalf("failure leaked result or no rollback: %+v, %d", got, tx.rollbacks)
					}
					if code == catalog.CodeDependencyUnavailable && !errors.Is(err, failure) {
						t.Fatalf("lost cause: %v", err)
					}
					return
				}
				want := s
				if action == audit.ActionPublicationUnpublished {
					want.State = publication.SnapshotRetired
				} else {
					want.State = publication.SnapshotActive
					want.ActivatedAt = new(at.UTC().Truncate(time.Microsecond))
				}
				if !reflect.DeepEqual(got, want) || tx.rollbacks != 0 {
					t.Fatalf("transition=%+v; want=%+v", got, want)
				}
				if !reflect.DeepEqual(tx.arguments[0], []any{catalogStateLockKey("tenant", s.TenantID)}) {
					t.Fatal("wrong tenant lock")
				}
				if !reflect.DeepEqual(tx.queryRowArguments[0], []any{s.TenantID}) || !reflect.DeepEqual(tx.queryRowArguments[pointerIndex], []any{s.TenantID, s.Host, publicationBundleFamily, publication.SnapshotActive}) {
					t.Fatalf("tenant/pointer bindings: %v", tx.queryRowArguments)
				}
				args := tx.arguments[targetWrite]
				var raw []byte
				if action == audit.ActionPublicationUnpublished {
					raw, _ = args[2].([]byte)
					if !reflect.DeepEqual(args, []any{s.ID, publication.SnapshotRetired, raw, s.TenantID, s.Host}) {
						t.Fatalf("retire binding: %v", args)
					}
					if !reflect.DeepEqual(tx.queryRowArguments[wantRows-1], []any{s.TenantID, s.Host, publicationBundleFamily, publication.SnapshotActive, s.ID}) {
						t.Fatal("unpublish did not select exact active target")
					}
				} else {
					raw, _ = args[3].([]byte)
					if !reflect.DeepEqual(args, []any{s.ID, publication.SnapshotActive, *want.ActivatedAt, raw, s.TenantID, s.Host}) {
						t.Fatalf("activate binding: %v", args)
					}
					if !reflect.DeepEqual(tx.queryRowArguments[1], []any{s.TenantID}) || !reflect.DeepEqual(tx.queryRowArguments[wantRows-1], []any{s.TenantID, s.ID, s.Host, publicationBundleFamily}) {
						t.Fatal("realm/target binding mismatch")
					}
					if !reflect.DeepEqual(tx.arguments[1], []any{s.TenantID, s.Host, publicationBundleFamily, publication.SnapshotRetired, publication.SnapshotActive}) {
						t.Fatal("prior retirement escaped store scope")
					}
				}
				if !strings.Contains(tx.queryRowStatements[wantRows-1], "FOR UPDATE") {
					t.Fatal("target not locked")
				}
				var retained publication.Snapshot
				if err := json.Unmarshal(raw, &retained); err != nil || !reflect.DeepEqual(retained, want) {
					t.Fatalf("retained=%+v, %v", retained, err)
				}
				auditArgs, outboxArgs := tx.arguments[targetWrite+1], tx.arguments[targetWrite+2]
				if !strings.Contains(tx.statements[targetWrite+1], "INSERT INTO audit_events") || auditArgs[2] != s.TenantID || auditArgs[4] != action || auditArgs[6] != s.ID {
					t.Fatal("transition audit binding mismatch")
				}
				if !strings.Contains(tx.statements[targetWrite+2], "INSERT INTO outbox_events") || outboxArgs[1] != s.TenantID || outboxArgs[2] != evidence.Outbox.Type || outboxArgs[3] != s.ID || !reflect.DeepEqual(outboxArgs[4], evidence.Outbox.Data) {
					t.Fatal("transition notification binding mismatch")
				}
			})
		}
	}
}

// TestPublicationRepositoryTransactionOptions checks read-only and serializable intent before any ambient transaction exists.
func TestPublicationRepositoryTransactionOptions(t *testing.T) {
	pool := &pgxpool.Pool{}
	s := publicationScanFixture(t)
	store := &PostgresPublicationStore{repository: &PostgresRepository{pool: pool}, host: s.Host}
	active := s
	active.State, active.ActivatedAt = publication.SnapshotActive, new(s.CreatedAt.Add(time.Hour))
	retired := active
	retired.State = publication.SnapshotRetired
	for _, test := range []struct {
		name    string
		options pgx.TxOptions
		rows    []pgx.Row
		call    func(context.Context) error
	}{
		{"candidate", pgx.TxOptions{}, nil, func(ctx context.Context) error { _, err := store.SaveCandidate(ctx, s); return err }},
		{"get", pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, []pgx.Row{scriptedValueRow(publicationScanValues(t, s))}, func(ctx context.Context) error { _, err := store.Get(ctx, s.TenantID, s.ID); return err }},
		{"activate", pgx.TxOptions{IsoLevel: pgx.Serializable}, []pgx.Row{scriptedValueRow([]any{s.TenantID}), scriptedValueRow([]any{true}), scriptedValueRow(publicationScanValues(t, s))}, func(ctx context.Context) error {
			_, err := store.Activate(ctx, s.TenantID, s.ID, s.CreatedAt.Add(2*time.Hour))
			return err
		}},
		{"rollback", pgx.TxOptions{IsoLevel: pgx.Serializable}, []pgx.Row{scriptedValueRow([]any{s.TenantID}), scriptedValueRow([]any{true}), scriptedValueRow(publicationScanValues(t, retired))}, func(ctx context.Context) error {
			_, err := store.Rollback(ctx, s.TenantID, s.ID, s.CreatedAt.Add(2*time.Hour))
			return err
		}},
		{"deactivate", pgx.TxOptions{IsoLevel: pgx.Serializable}, []pgx.Row{scriptedValueRow([]any{s.TenantID}), scriptedValueRow(publicationScanValues(t, active))}, func(ctx context.Context) error {
			_, err := store.Deactivate(ctx, s.TenantID, s.ID, s.CreatedAt.Add(2*time.Hour))
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: test.rows}
			connection := &captureCommandConnection{transaction: tx}
			command := &commandTransaction{pool: pool, tenantID: s.TenantID, conn: connection}
			ctx := withCommandTransaction(t.Context(), command)
			if err := test.call(ctx); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(connection.options, []pgx.TxOptions{test.options}) {
				t.Fatalf("transaction options=%v", connection.options)
			}
			if len(tx.arguments) == 0 || !reflect.DeepEqual(tx.arguments[0], []any{s.TenantID, "", ""}) {
				t.Fatal("tenant context not established")
			}
			commits := 0
			if test.options.AccessMode == pgx.ReadOnly {
				commits = 1
			}
			if tx.commits != commits || tx.rollbacks != 0 {
				t.Fatalf("transaction ownership: commits=%d rollbacks=%d", tx.commits, tx.rollbacks)
			}
		})
	}
}
