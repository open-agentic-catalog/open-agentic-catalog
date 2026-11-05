package database

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

func jobViewValues(j jobs.Job) []any {
	return []any{j.ID, j.TenantID, j.Kind, j.ParentJobID, j.State, j.Priority, j.Progress, j.NotBefore, j.Attempt, j.AttemptLimit, j.CancellationRequested, j.Result, j.SafeError, j.FailureClass, j.CreatedAt, j.UpdatedAt}
}

func TestPostgresJobViewReads(t *testing.T) {
	for _, operation := range []string{"get", "list"} {
		for _, fault := range []string{"none", "empty", "scan", "other tenant", "query", "iterate"} {
			if operation == "get" && (fault == "query" || fault == "iterate") {
				continue
			}
			t.Run(operation+"/"+fault, func(t *testing.T) {
				store, tx, ctx, job := jobRepositoryFixture(t)
				job.State = jobs.StateSucceeded
				job.Result = json.RawMessage(`{"id":"00000000-0000-7000-8000-000000000008","sourceId":"` + jobSourceID + `","state":"complete"}`)
				failure := errors.New("private driver fault")
				rows := &identityScriptedRows{values: [][]any{jobViewValues(job)}}
				tx.queryRows = []pgx.Row{scriptedValueRow(jobViewValues(job))}
				tx.queryResults = []identityQueryResult{{rows: rows}}
				switch fault {
				case "empty":
					rows.values = nil
					tx.queryRows = []pgx.Row{scannerFunc(func(...any) error { return pgx.ErrNoRows })}
				case "scan":
					rows.values = [][]any{{job.ID}}
					tx.queryRows = []pgx.Row{scannerFunc(func(...any) error { return failure })}
				case "query":
					tx.queryResults[0].err = failure
				case "iterate":
					rows.err = failure
				case "other tenant":
					job.TenantID = jobSourceID
				}
				var err error
				var got []jobs.View
				if operation == "get" {
					var view jobs.View
					view, err = store.GetView(ctx, job.TenantID, job.ID)
					if err == nil {
						got = []jobs.View{view}
					}
				} else {
					got, err = store.ListViews(ctx, job.TenantID, jobs.ListRequest{AfterCreatedAt: &job.CreatedAt, AfterID: job.ID, Limit: 7, Kind: "source.sync", State: jobs.StateSucceeded, ParentJobID: jobSourceID})
				}
				switch fault {
				case "none":
					if err != nil || !reflect.DeepEqual(got, []jobs.View{jobs.PublicView(job)}) {
						t.Fatalf("metadata differs: %+v, %v", got, err)
					}
				case "empty":
					if operation == "get" && catalog.ErrorCodeOf(err) != catalog.CodeNotFound || operation == "list" && (err != nil || got == nil || len(got) != 0) {
						t.Fatalf("empty read: %+v, %v", got, err)
					}
				case "other tenant":
					if catalog.ErrorCodeOf(err) != catalog.CodeForbidden || len(tx.queryArguments)+len(tx.queryRowArguments) != 0 {
						t.Fatalf("cross-tenant transaction used: %v", err)
					}
					return
				default:
					if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || got != nil {
						t.Fatalf("failed read exposed data: %+v, %v", got, err)
					}
					if (fault != "scan" || operation == "get") && !errors.Is(err, failure) {
						t.Fatal("driver cause lost")
					}
				}
				statements := tx.queryRowStatements
				arguments := tx.queryRowArguments
				wantArgs := []any{job.TenantID, job.ID}
				if operation == "list" {
					statements, arguments = tx.queryStatements, tx.queryArguments
					wantArgs = []any{job.TenantID, &job.CreatedAt, job.ID, 7, false, []string(nil), "", "", "source.sync", jobs.StateSucceeded, jobSourceID}
					if rows.closed != (fault != "query") {
						t.Fatal("row cleanup changed")
					}
				}
				if len(statements) != 1 || !strings.Contains(statements[0], jobViewColumns) || !reflect.DeepEqual(arguments, [][]any{wantArgs}) || tx.commits != 0 {
					t.Fatal("metadata read lost selection or command transaction ownership")
				}
				if operation == "list" {
					selection := strings.SplitN(statements[0], " ORDER BY", 2)[0]
					for _, predicate := range []string{"kind=$9", "state=$10", "parent_job_id=nullif($11,'')::uuid"} {
						if !strings.Contains(selection, predicate) {
							t.Fatalf("filter %s missing before page bound", predicate)
						}
					}
				}
				for _, private := range []string{"payload", "lease_owner", "deduplication_key", "causal_trace"} {
					projection := strings.SplitN(statements[0], " FROM jobs", 2)[0]
					if strings.Contains(projection, private) {
						t.Fatalf("public read selected %s", private)
					}
				}
			})
		}
	}
}
