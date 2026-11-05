// Job repository units exercise tenant transaction coordination, retained
// evidence and failure disposition. PostgreSQL tests own claim selection, lease
// fencing and SQL behavior.
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
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

const jobSourceID = "00000000-0000-7000-8000-000000000004"

func TestPostgresJobFailureDisposition(t *testing.T) {
	t.Parallel()
	notBefore := time.Date(2026, time.September, 21, 12, 0, 0, 123000, time.UTC)
	retryAt := notBefore.Add(5 * time.Minute)
	noRetry := time.Time{}
	for _, test := range []struct {
		name                  string
		class                 jobs.FailureClass
		cancellationRequested bool
		attempt               int
		retryAt               time.Time
		wantState             jobs.State
		wantNotBefore         time.Time
	}{
		{"failure without retry", jobs.FailureInvalidInput, false, 2, noRetry, jobs.StateFailed, notBefore},
		{"retryable class alone does not schedule", jobs.FailureDependency, false, 2, noRetry, jobs.StateFailed, notBefore},
		{"retry below limit", jobs.FailureDependency, false, 2, retryAt, jobs.StateWaiting, retryAt},
		{"explicit retry is not reclassified", jobs.FailureInvalidInput, false, 2, retryAt, jobs.StateWaiting, retryAt},
		{"limit reached with retry", jobs.FailureDependency, false, 3, retryAt, jobs.StateDeadLetter, notBefore},
		{"limit reached without retry", jobs.FailureDependency, false, 3, noRetry, jobs.StateDeadLetter, notBefore},
		{"limit exceeded with retry", jobs.FailureDependency, false, 4, retryAt, jobs.StateDeadLetter, notBefore},
		{"limit exceeded without retry", jobs.FailureDependency, false, 4, noRetry, jobs.StateDeadLetter, notBefore},
		{"explicit cancellation before retry", jobs.FailureCancelled, false, 2, retryAt, jobs.StateCancelled, notBefore},
		{"requested cancellation before retry", jobs.FailureDependency, true, 2, retryAt, jobs.StateCancelled, notBefore},
		{"explicit cancellation at limit", jobs.FailureCancelled, false, 3, retryAt, jobs.StateCancelled, notBefore},
		{"requested cancellation above limit", jobs.FailureDependency, true, 4, noRetry, jobs.StateCancelled, notBefore},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := jobs.Job{State: jobs.StateRunning, NotBefore: notBefore, Attempt: test.attempt, AttemptLimit: 3, CancellationRequested: test.cancellationRequested}
			state, at := jobFailureDisposition(job, test.class, test.retryAt)
			if state != test.wantState || at != test.wantNotBefore {
				t.Fatalf("failure disposition = %s, %s; want %s, %s", state, at, test.wantState, test.wantNotBefore)
			}
		})
	}
}

func jobRepositoryFixture(t *testing.T) (*PostgresJobStore, *identityScriptedTx, context.Context, jobs.Job) {
	t.Helper()
	pool := &pgxpool.Pool{} // Identity only; the command owns the scripted transaction.
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresJobStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	now := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	job := jobs.Job{ID: "00000000-0000-7000-8000-000000000005", TenantID: paginationTestTenantID, Kind: "source.sync", Actor: "00000000-0000-7000-8000-000000000002", PayloadVersion: 2,
		Payload: json.RawMessage(`{"sourceId":"` + jobSourceID + `","sourceRevision":3,"trigger":"manual"}`), State: jobs.StateQueued, Priority: 2, NotBefore: now, AttemptLimit: 5,
		DeduplicationKey: "source.sync:" + jobSourceID + ":3", CreatedAt: now, UpdatedAt: now}
	return store, tx, identityRepositoryContext(pool, tx), job
}

func jobRepositoryValues(job jobs.Job) []any {
	return []any{job.ID, job.TenantID, job.Kind, job.Actor, job.ParentJobID, job.PayloadVersion, job.Payload, job.State, job.Priority, job.Progress, job.NotBefore, job.Attempt, job.AttemptLimit, job.LeaseOwner, job.LeaseExpiresAt, job.CancellationRequested, job.DeduplicationKey, job.Result, job.SafeError, job.FailureClass, job.CreatedAt, job.UpdatedAt, job.CausalTrace}
}

func TestUpdateJobLeaseBindingsAndResults(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"heartbeat", "completion", "failure disposition", "missing row", "query failure", "partial scan failure"} {
		t.Run(mode, func(t *testing.T) {
			_, query, _, job := jobRepositoryFixture(t)
			const owner = "worker-lease-owner"
			now := job.CreatedAt.Add(time.Minute)
			expires := now.Add(2 * time.Minute)
			job.State, job.Attempt, job.Progress = jobs.StateRunning, 1, 37
			job.LeaseOwner, job.LeaseExpiresAt, job.UpdatedAt = owner, &expires, now
			assignments := "progress=$5,lease_expires_at=$6"
			values := []any{37, expires}
			switch mode {
			case "completion":
				job.State, job.Progress = jobs.StateSucceeded, 100
				job.LeaseOwner, job.LeaseExpiresAt = "", nil
				job.Result = json.RawMessage(`{"state":"complete"}`)
				assignments = "state='succeeded',progress=100,result=$5,lease_owner=NULL,lease_expires_at=NULL"
				values = []any{job.Result}
			case "failure disposition":
				job.State, job.NotBefore = jobs.StateWaiting, now.Add(3*time.Minute)
				job.LeaseOwner, job.LeaseExpiresAt = "", nil
				job.SafeError, job.FailureClass = "Synchronization unavailable", jobs.FailureDependency
				assignments = "state=$5,safe_error=$6,failure_class=$7,not_before=$8,lease_owner=NULL,lease_expires_at=NULL"
				values = []any{jobs.StateWaiting, job.SafeError, jobs.FailureDependency, job.NotBefore}
			}
			failure := errors.New("lease update failed")
			row := scriptedValueRow(jobRepositoryValues(job))
			code := catalog.ErrorCode("")
			switch mode {
			case "missing row":
				row = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				code = catalog.CodeConflict
			case "query failure", "partial scan failure":
				row = scannerFunc(func(destinations ...any) error {
					if mode == "partial scan failure" {
						setScanDestination(t, destinations, 0, job.ID)
					}
					return failure
				})
				code = catalog.CodeDependencyUnavailable
			}
			query.queryRows = []pgx.Row{row}
			got, err := updateJobLease(t.Context(), query, job.TenantID, job.ID, owner, now, assignments, values...)
			if code == "" {
				if err != nil || !reflect.DeepEqual(got, job) {
					t.Fatalf("lease update = %+v, %v; want %+v", got, err, job)
				}
			} else if catalog.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, jobs.Job{}) {
				t.Fatalf("failed lease update = %+v, %v; want no job and %s", got, err, code)
			}
			if code == catalog.CodeDependencyUnavailable && !errors.Is(err, failure) {
				t.Fatal("lease update lost its error cause")
			}
			if mode == "missing row" {
				var domain *catalog.DomainError
				if !errors.As(err, &domain) || domain.Message != "job lease is not owned or has expired" {
					t.Fatalf("missing lease did not use the safe conflict message: %v", err)
				}
			}
			wantArguments := append([]any{job.TenantID, job.ID, owner, now}, values...)
			if len(query.queryRowStatements) != 1 || len(query.queryRows) != 0 || !reflect.DeepEqual(query.queryRowArguments, [][]any{wantArguments}) {
				t.Fatalf("lease update statements = %d; arguments = %#v; unread rows = %d", len(query.queryRowStatements), query.queryRowArguments, len(query.queryRows))
			}
			if !strings.HasPrefix(query.queryRowStatements[0], "UPDATE jobs SET "+assignments+",updated_at=greatest(updated_at,$4) ") {
				t.Fatal("lease update lost the code-owned assignments")
			}
			if len(query.queryStatements) != 0 || len(query.statements) != 0 || query.commits != 0 || query.rollbacks != 0 {
				t.Fatal("lease update performed unrelated work or took transaction ownership")
			}
		})
	}
}

func jobRepositoryEvidence(t *testing.T, job jobs.Job, cancel bool) audit.Record {
	t.Helper()
	action, targetType, target := audit.ActionSourceSyncRequested, "federation_source", jobSourceID
	facts := map[string]any{"sourceRevision": 3, "jobId": job.ID, "trigger": "manual", "deduplicated": false}
	if cancel {
		action, targetType, target = audit.ActionJobCancellationRequested, "job", job.ID
		after, requested := job.State, job.CancellationRequested
		switch job.State {
		case jobs.StateQueued, jobs.StateWaiting:
			after = jobs.StateCancelled
		case jobs.StateRunning:
			requested = true
		case jobs.StateSucceeded, jobs.StateFailed, jobs.StateCancelled, jobs.StateDeadLetter:
		default:
			t.Fatalf("invalid fixture state %q", job.State)
		}
		facts = map[string]any{"kind": job.Kind, "beforeState": string(job.State), "afterState": string(after), "cancellationRequested": requested}
	}
	record, err := audit.NewRecord("00000000-0000-7000-8000-000000000006", job.TenantID,
		audit.Principal{ID: job.Actor, Type: audit.PrincipalCatalogMembership, ActorType: "human", RealmID: "00000000-0000-7000-8000-000000000003"}, action, targetType, target, "Requested maintenance", audit.Execution{}, facts, job.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestPostgresSemanticJobStoreExclusions(t *testing.T) {
	repository := &PostgresRepository{}
	profile := "sha256:" + strings.Repeat("a", 64)
	tooMany := make([]string, 257)
	for i := range tooMany {
		tooMany[i] = paginationTestTenantID
	}
	for _, test := range []struct {
		name       string
		repository *PostgresRepository
		profile    string
		excluded   []string
	}{
		{"nil repository", nil, profile, nil},
		{"bad profile", repository, "invalid", nil},
		{"invalid tenant", repository, profile, []string{"not-a-tenant"}},
		{"too many tenants", repository, profile, tooMany},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := NewPostgresSemanticJobStore(test.repository, test.profile, test.excluded); err == nil || got != nil {
				t.Fatalf("invalid construction = %+v, %v", got, err)
			}
		})
	}
	for _, n := range []int{0, 1, 256} {
		excluded := make([]string, n)
		for i := range excluded {
			excluded[i] = paginationTestTenantID
		}
		store, err := NewPostgresSemanticJobStore(repository, profile, excluded)
		if err != nil || store.repository != repository || store.semanticProfile != profile || !reflect.DeepEqual(store.semanticExcluded, excluded) {
			t.Fatalf("construction = %+v, %v", store, err)
		}
		if n > 0 {
			excluded[0] = "changed"
			if store.semanticExcluded[0] != paginationTestTenantID {
				t.Fatal("caller mutation changed worker exclusions")
			}
		}
	}
}

func TestPostgresJobGetProjectionAndErrors(t *testing.T) {
	for _, mode := range []string{"retained history", "running lease", "failed history", "missing", "scan failure", "other tenant"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, job := jobRepositoryFixture(t)
			expiry := job.CreatedAt.Add(time.Minute)
			job.ParentJobID = jobSourceID
			job.State = jobs.StateSucceeded
			job.Attempt = 2
			job.Progress = 100
			job.Result = json.RawMessage(`{"state":"complete"}`)
			switch mode {
			case "running lease":
				job.State = jobs.StateRunning
				job.Progress = 30
				job.LeaseOwner = "worker"
				job.LeaseExpiresAt = &expiry
				job.CancellationRequested = true
				job.Result = nil
			case "failed history":
				job.State = jobs.StateFailed
				job.Progress = 30
				job.Result = nil
				job.SafeError = "Upstream rejected synchronization"
				job.FailureClass = jobs.FailurePermanentUpstream
			}
			job.CausalTrace = catalog.TraceContext("00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
			failure := errors.New("job scan failed")
			tx.queryRows = []pgx.Row{scriptedValueRow(jobRepositoryValues(job))}
			code := catalog.ErrorCode("")
			switch mode {
			case "missing":
				tx.queryRows = []pgx.Row{scannerFunc(func(...any) error { return pgx.ErrNoRows })}
				code = catalog.CodeNotFound
			case "scan failure":
				tx.queryRows = []pgx.Row{scannerFunc(func(...any) error { return failure })}
				code = catalog.CodeDependencyUnavailable
			case "other tenant":
				job.TenantID = jobSourceID
				code = catalog.CodeForbidden
			}
			got, err := store.Get(ctx, job.TenantID, job.ID)
			if code == "" {
				if err != nil || !reflect.DeepEqual(got, job) {
					t.Fatalf("get = %+v, %v; want %+v", got, err, job)
				}
			} else if catalog.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, jobs.Job{}) {
				t.Fatalf("failed get = %+v, %v", got, err)
			}
			if mode == "scan failure" && !errors.Is(err, failure) {
				t.Fatal("scan cause lost")
			}
			if mode == "other tenant" {
				if len(tx.queryRowStatements) != 0 {
					t.Fatal("cross-tenant query executed")
				}
				return
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{job.TenantID, job.ID}}) || len(tx.statements) != 0 || tx.commits != 0 {
				t.Fatalf("unexpected calls: %+v", tx)
			}
		})
	}
}

func TestPostgresJobListFailureAndEmptyPages(t *testing.T) {
	for _, mode := range []string{"empty", "query failure", "scan failure", "iteration failure"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, job := jobRepositoryFixture(t)
			failure := errors.New("job page failed")
			rows := &identityScriptedRows{}
			tx.queryResults = []identityQueryResult{{rows: rows}}
			switch mode {
			case "query failure":
				tx.queryResults[0].err = failure
			case "scan failure":
				rows.values = [][]any{{job.ID}}
			case "iteration failure":
				rows.values = [][]any{jobRepositoryValues(job)}
				rows.err = failure
			}
			request := jobs.ListRequest{AfterCreatedAt: &job.CreatedAt, AfterID: job.ID, Limit: 7}
			got, err := store.List(ctx, job.TenantID, request)
			if mode == "empty" {
				if err != nil || got == nil || len(got) != 0 {
					t.Fatalf("empty page = %v, %v", got, err)
				}
			} else if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || got != nil {
				t.Fatalf("failed page = %v, %v", got, err)
			}
			if (mode == "query failure" || mode == "iteration failure") && !errors.Is(err, failure) {
				t.Fatal("page cause lost")
			}
			if rows.closed != (mode != "query failure") {
				t.Fatalf("rows closed=%t", rows.closed)
			}
			if !reflect.DeepEqual(tx.queryArguments, [][]any{{job.TenantID, request.AfterCreatedAt, job.ID, 7, "", jobs.State(""), ""}}) || len(tx.statements) != 0 {
				t.Fatalf("page arguments = %v", tx.queryArguments)
			}
		})
	}
}

func TestPostgresJobEnqueueBindsSourceAndEvidence(t *testing.T) {
	for _, mode := range []string{"insert", "no evidence", "deduplicate", "parent failed", "parent dead letter", "parent missing", "parent active", "source stale", "source failure", "insert failure", "deduplicated read failure", "audit failure", "deduplicated audit failure"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, job := jobRepositoryFixture(t)
			evidence := []audit.Record{jobRepositoryEvidence(t, job, false)}
			trace := catalog.TraceContext("00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
			ctx = catalog.WithTraceContext(ctx, trace)
			existing := job
			existing.ID = "00000000-0000-7000-8000-000000000007"
			existing.State = jobs.StateRunning
			existing.Attempt = 1
			expires := existing.UpdatedAt.Add(time.Minute)
			existing.LeaseOwner = "worker"
			existing.LeaseExpiresAt = &expires
			failure := errors.New("job persistence unavailable")
			errorRow := scannerFunc(func(...any) error { return failure })
			absent := scannerFunc(func(...any) error { return pgx.ErrNoRows })
			sourceRow := scriptedValueRow([]any{1})
			inserted := scriptedValueRow([]any{job.ID})
			tx.queryRows = []pgx.Row{sourceRow, inserted}
			code := catalog.ErrorCode("")
			reads, writes := 2, 1
			deduplicated := false
			switch mode {
			case "no evidence":
				evidence = nil
				writes = 0
			case "deduplicate", "deduplicated audit failure":
				tx.queryRows = []pgx.Row{sourceRow, absent, scriptedValueRow(jobRepositoryValues(existing))}
				reads = 3
				deduplicated = true
			case "parent failed", "parent dead letter", "parent missing", "parent active":
				job.ParentJobID = existing.ID
				evidence = nil
				writes = 0
				reads = 3
				state := jobs.StateFailed
				if mode == "parent dead letter" {
					state = jobs.StateDeadLetter
				}
				if mode == "parent active" {
					state = jobs.StateRunning
					code = catalog.CodeInvalidTransition
					reads = 1
				}
				parent := scriptedValueRow([]any{state})
				if mode == "parent missing" {
					parent = absent
					code = catalog.CodeNotFound
					reads = 1
				}
				tx.queryRows = []pgx.Row{parent, sourceRow, inserted}
			case "source stale":
				tx.queryRows = []pgx.Row{absent}
				code = catalog.CodePreconditionFailed
				reads = 1
				writes = 0
			case "source failure":
				tx.queryRows = []pgx.Row{errorRow}
				code = catalog.CodeDependencyUnavailable
				reads = 1
				writes = 0
			case "insert failure":
				tx.queryRows[1] = errorRow
				code = catalog.CodeDependencyUnavailable
				writes = 0
			case "deduplicated read failure":
				tx.queryRows = []pgx.Row{sourceRow, absent, errorRow}
				code = catalog.CodeDependencyUnavailable
				reads = 3
				writes = 0
			}
			if mode == "audit failure" || mode == "deduplicated audit failure" {
				tx.execErrors = []error{failure}
				code = catalog.CodeDependencyUnavailable
			}
			got, reused, err := store.Enqueue(ctx, job, evidence...)
			if code == "" {
				want := job
				want.CausalTrace = trace
				if deduplicated {
					want = existing
				}
				if err != nil || reused != deduplicated || !reflect.DeepEqual(got, want) || tx.rollbacks != 0 {
					t.Fatalf("enqueue = %+v, %t, %v; want %+v", got, reused, err, want)
				}
			} else if catalog.ErrorCodeOf(err) != code || reused || !reflect.DeepEqual(got, jobs.Job{}) || tx.rollbacks != 1 {
				t.Fatalf("failed enqueue = %+v, %t, %v; rollbacks=%d", got, reused, err, tx.rollbacks)
			}
			if strings.Contains(mode, "failure") && !errors.Is(err, failure) {
				t.Fatal("persistence cause lost")
			}
			if len(tx.queryRowStatements) != reads || len(tx.statements) != writes || tx.commits != 0 {
				t.Fatalf("reads/writes/commits=%d/%d/%d", len(tx.queryRowStatements), len(tx.statements), tx.commits)
			}
			index := 0
			if job.ParentJobID != "" {
				if !reflect.DeepEqual(tx.queryRowArguments[0], []any{job.TenantID, job.ParentJobID}) {
					t.Fatal("parent scope lost")
				}
				index++
				if reads == 1 {
					return
				}
			}
			if !reflect.DeepEqual(tx.queryRowArguments[index], []any{job.TenantID, jobSourceID, int64(3)}) {
				t.Fatalf("source scope = %v", tx.queryRowArguments[index])
			}
			index++
			if reads > index {
				args := []any{job.ID, job.TenantID, job.Kind, job.Actor, job.ParentJobID, job.PayloadVersion, job.Payload, job.State, job.Priority, job.Progress, job.NotBefore, job.Attempt, job.AttemptLimit, job.DeduplicationKey, job.CreatedAt, job.UpdatedAt, trace}
				if !reflect.DeepEqual(tx.queryRowArguments[index], args) {
					t.Fatalf("insert arguments = %v", tx.queryRowArguments[index])
				}
				index++
			}
			if reads > index && !reflect.DeepEqual(tx.queryRowArguments[index], []any{job.TenantID, job.Kind, job.DeduplicationKey}) {
				t.Fatal("deduplication scope lost")
			}
			if code == "" && writes == 1 {
				args := tx.arguments[0]
				var details struct {
					Facts struct {
						JobID        string `json:"jobId"`
						Deduplicated bool   `json:"deduplicated"`
					} `json:"facts"`
				}
				raw, ok := args[11].([]byte)
				if !ok {
					t.Fatalf("audit details type = %T", args[11])
				}
				if err := json.Unmarshal(raw, &details); err != nil {
					t.Fatal(err)
				}
				if args[2] != job.TenantID || args[6] != jobSourceID || details.Facts.JobID != got.ID || details.Facts.Deduplicated != reused || args[10] == "" {
					t.Fatalf("audit lost admitted job binding: %v", args)
				}
			}
		})
	}
}

func TestPostgresJobCancellationTransitions(t *testing.T) {
	for _, state := range []jobs.State{jobs.StateQueued, jobs.StateWaiting, jobs.StateRunning, jobs.StateSucceeded, jobs.StateFailed, jobs.StateCancelled, jobs.StateDeadLetter} {
		for _, clock := range []struct {
			name  string
			delta time.Duration
		}{{"earlier", -time.Minute}, {"equal", 0}, {"later", time.Minute}} {
			t.Run(string(state)+"/"+clock.name, func(t *testing.T) {
				store, tx, ctx, before := jobRepositoryFixture(t)
				before.State = state
				switch state {
				case jobs.StateQueued, jobs.StateCancelled:
				case jobs.StateRunning:
					expires := before.UpdatedAt.Add(2 * time.Minute)
					before.Attempt = 1
					before.LeaseOwner = "worker"
					before.LeaseExpiresAt = &expires
				case jobs.StateSucceeded:
					before.Attempt = 1
					before.Progress = 100
					before.Result = json.RawMessage(`{"state":"complete"}`)
				case jobs.StateWaiting, jobs.StateFailed, jobs.StateDeadLetter:
					before.Attempt = 1
					before.FailureClass = jobs.FailureTransientUpstream
					before.SafeError = "Synchronization failed"
					if state == jobs.StateDeadLetter {
						before.Attempt = before.AttemptLimit
					}
				default:
					t.Fatalf("invalid fixture state %q", state)
				}
				now := before.UpdatedAt.Add(clock.delta)
				wantUpdatedAt := before.UpdatedAt
				if clock.name == "later" {
					wantUpdatedAt = before.UpdatedAt.Add(time.Minute)
				}
				after := before
				mutates := false
				switch state {
				case jobs.StateQueued, jobs.StateWaiting:
					after.State = jobs.StateCancelled
					after.UpdatedAt = wantUpdatedAt
					mutates = true
				case jobs.StateRunning:
					after.CancellationRequested = true
					after.UpdatedAt = wantUpdatedAt
					mutates = true
				case jobs.StateSucceeded, jobs.StateFailed, jobs.StateCancelled, jobs.StateDeadLetter:
				default:
					t.Fatalf("invalid fixture state %q", state)
				}
				tx.queryRows = []pgx.Row{scriptedValueRow(jobRepositoryValues(before))}
				if mutates {
					tx.queryRows = append(tx.queryRows, scriptedValueRow(jobRepositoryValues(after)))
				}
				evidence := jobRepositoryEvidence(t, before, true)
				// Audit occurrence time belongs to the supplied record, independently
				// of the monotonic Job update timestamp.
				evidence.At = before.CreatedAt.Add(17 * time.Second)
				got, err := store.RequestCancellation(ctx, before.TenantID, before.ID, now, evidence)
				if err != nil || !reflect.DeepEqual(got, after) {
					t.Fatalf("cancellation = %+v, %v; want %+v", got, err, after)
				}
				wantArgs := [][]any{{before.TenantID, before.ID}}
				if mutates {
					wantArgs = append(wantArgs, []any{before.TenantID, before.ID, after.State, after.CancellationRequested, wantUpdatedAt})
				}
				if !reflect.DeepEqual(tx.queryRowArguments, wantArgs) || len(tx.statements) != 1 || tx.commits != 0 || tx.rollbacks != 0 {
					t.Fatalf("cancellation calls = %+v", tx)
				}
				args := tx.arguments[0]
				// Decode the documented fact names independently of the production helper.
				var facts struct {
					Facts struct {
						BeforeState           jobs.State        `json:"beforeState"`
						AfterState            jobs.State        `json:"afterState"`
						CancellationRequested bool              `json:"cancellationRequested"`
						FailureClass          jobs.FailureClass `json:"failureClass"`
					} `json:"facts"`
				}
				raw, ok := args[11].([]byte)
				if !ok {
					t.Fatalf("audit details type = %T", args[11])
				}
				if err := json.Unmarshal(raw, &facts); err != nil {
					t.Fatal(err)
				}
				if args[2] != before.TenantID || args[6] != before.ID || facts.Facts.BeforeState != before.State || facts.Facts.AfterState != after.State || facts.Facts.CancellationRequested != after.CancellationRequested || facts.Facts.FailureClass != before.FailureClass || args[9] == "" || args[10] == "" || args[12] != evidence.At {
					t.Fatalf("cancellation evidence = %v", args)
				}
			})
		}
	}
}

func TestPostgresJobCancellationFailuresAndRepeatedRequest(t *testing.T) {
	for _, mode := range []string{"already requested", "same timestamp", "read failure", "missing", "update failure", "audit failure", "invalid evidence", "unknown state"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, before := jobRepositoryFixture(t)
			failure := errors.New("cancel persistence failed")
			errorRow := scannerFunc(func(...any) error { return failure })
			now := before.UpdatedAt.Add(time.Minute)
			after := before
			after.State = jobs.StateCancelled
			after.UpdatedAt = now
			tx.queryRows = []pgx.Row{scriptedValueRow(jobRepositoryValues(before)), scriptedValueRow(jobRepositoryValues(after))}
			evidence := []audit.Record{jobRepositoryEvidence(t, before, true)}
			code := catalog.CodeDependencyUnavailable
			reads, writes := 2, 0
			switch mode {
			case "already requested", "same timestamp":
				before.State = jobs.StateRunning
				before.CancellationRequested = true
				expires := now.Add(time.Minute)
				before.Attempt = 1
				before.LeaseOwner = "worker"
				before.LeaseExpiresAt = &expires
				after = before
				after.UpdatedAt = now
				tx.queryRows = []pgx.Row{scriptedValueRow(jobRepositoryValues(before)), scriptedValueRow(jobRepositoryValues(after))}
				evidence = nil
				code = ""
				if mode == "same timestamp" {
					now = before.UpdatedAt
					after = before
					reads = 1
				}
			case "read failure":
				tx.queryRows[0] = errorRow
				reads = 1
			case "missing":
				tx.queryRows[0] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				reads = 1
				code = catalog.CodeNotFound
			case "update failure":
				tx.queryRows[1] = errorRow
			case "audit failure":
				tx.execErrors = []error{failure}
				writes = 1
			case "invalid evidence":
				evidence[0].TenantID = jobSourceID
				code = catalog.CodeInternal
			case "unknown state":
				before.State = "unknown"
				tx.queryRows[0] = scriptedValueRow(jobRepositoryValues(before))
				reads = 1
				code = catalog.CodeInvalidTransition
			}
			got, err := store.RequestCancellation(ctx, before.TenantID, before.ID, now, evidence...)
			if code == "" {
				if err != nil || !reflect.DeepEqual(got, after) || tx.rollbacks != 0 {
					t.Fatalf("repeated cancellation = %+v, %v", got, err)
				}
			} else if catalog.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, jobs.Job{}) || tx.rollbacks != 1 {
				t.Fatalf("failed cancellation = %+v, %v; rollbacks=%d", got, err, tx.rollbacks)
			}
			if strings.Contains(mode, "failure") && !errors.Is(err, failure) {
				t.Fatal("cancel cause lost")
			}
			if len(tx.queryRowStatements) != reads || len(tx.statements) != writes || tx.commits != 0 {
				t.Fatalf("unexpected cancellation calls: %+v", tx)
			}
			if mode == "already requested" && !reflect.DeepEqual(tx.queryRowArguments[1], []any{before.TenantID, before.ID, jobs.StateRunning, true, now}) {
				t.Fatalf("repeated cancellation did not persist its timestamp: %v", tx.queryRowArguments)
			}
		})
	}
}
