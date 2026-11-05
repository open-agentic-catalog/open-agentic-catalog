package observability

import (
	"context"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/housekeeping"
)

type snapshotProviderFunc func(context.Context, time.Time) (OperationalSnapshot, error)

func (function snapshotProviderFunc) OperationalSnapshot(ctx context.Context, now time.Time) (OperationalSnapshot, error) {
	return function(ctx, now)
}

func TestRegistryRendersBoundedDeterministicMetricsWithoutRequestData(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	registry := New(func() time.Time { return now })
	registry.runtimeRead = func() runtimeReading { return runtimeReading{} }
	registry.SetLifecycle(LifecycleRunning)
	registry.SetLifecycle("invalid")
	registry.SetComponent(ComponentAPI, ComponentRunning)
	registry.SetComponent(ComponentWorker, ComponentDegraded)
	registry.SetComponent("tenant-private", ComponentRunning)
	registry.SetComponent(ComponentAPI, "invalid")
	registry.SetDependency("postgresql", true)
	registry.SetDependency("https://private.example", false)
	registry.UpdateDependency("postgresql", false)
	registry.UpdateDependency("https://private.example", true)
	registry.UpdateDependency("postgresql", true)

	finish := registry.BeginHTTP()
	now = now.Add(25 * time.Millisecond)
	finish(http.MethodPost, "POST /api/v1/resources/{resourceId}", http.StatusCreated, 123, 25*time.Millisecond)
	finish(http.MethodPost, "/must-not-count-twice", http.StatusInternalServerError, 999, time.Second)
	failed := registry.BeginHTTP()
	failed("UNBOUNDED secret", "https://private.example/path?token=secret", http.StatusInternalServerError, -1, -time.Second)

	registry.ObserveOperation(OperationCatalogSearch, OutcomePartial)
	registry.ObserveOperation("private-operation", OutcomeSucceeded)
	registry.ObserveOperation(OperationCatalogSearch, "private-outcome")
	registry.JobStarted("source.sync")
	registry.JobStarted("private.kind")
	registry.JobFinished(t.Context(), "source.sync", jobs.AttemptResult{Outcome: "succeeded"})
	registry.JobFinished(t.Context(), "private.kind", jobs.AttemptResult{Outcome: "private-error"})
	registry.JobStarted("semantic.reindex")
	registry.JobStarted("semantic.reindex")
	registry.JobFinished(t.Context(), "semantic.reindex", jobs.AttemptResult{Outcome: "succeeded"})
	registry.OutboxBatch(0, nil)
	registry.OutboxBatch(2, nil)
	registry.OutboxBatch(0, errors.New("private outbox failure"))
	registry.ObserveHousekeepingBatch(housekeeping.ClassTerminalJob, "succeeded", housekeeping.Result{Reclaimed: 3, Marked: 4, Saturated: true, OldestEligibleAge: 2 * time.Minute}, 25*time.Millisecond)
	registry.ObserveHousekeepingBatch(housekeeping.ClassTerminalJob, "failed", housekeeping.Result{Reclaimed: 999, Marked: 999, Saturated: true, OldestEligibleAge: time.Hour}, time.Second)
	registry.ObserveHousekeepingBatch("tenant-private", "succeeded", housekeeping.Result{Reclaimed: 999}, time.Second)
	registry.ObserveHousekeepingBatch(housekeeping.ClassTerminalJob, "private-outcome", housekeeping.Result{Reclaimed: 999}, time.Second)
	registry.ObserveHousekeepingCycle("succeeded", 50*time.Millisecond)
	registry.ObserveHousekeepingCycle("private-outcome", time.Second)

	registry.ObserveDatabase("select", nil, 10*time.Millisecond)
	registry.ObserveDatabase("VACUUM secret_table", errors.New("postgres://user:secret@database/catalog"), 0)
	registry.SetPoolSnapshot(func() PoolSnapshot {
		return PoolSnapshot{AcquireCount: 3, AcquireSeconds: 0.02, EmptyAcquireWaitSeconds: 0.001, TotalConnections: 2, MaxConnections: 4}
	})

	registry.SetSnapshotProvider(snapshotProviderFunc(func(_ context.Context, collectedAt time.Time) (OperationalSnapshot, error) {
		return OperationalSnapshot{
			CollectedAt:  collectedAt,
			Jobs:         JobSnapshot{Queued: 2, Running: 1, Waiting: 3, Succeeded: 4, Failed: 5, Cancelled: 6, DeadLetter: 7, SourceSync: 8, Publication: 9, Other: 10, OldestAgeSec: 11},
			Sources:      SourceSnapshot{Active: 12, Paused: 13, Error: 14, Removed: 15, Complete: 16, Incomplete: 17, Failed: 18, LastAgeSec: 19},
			Publications: PublicationSnapshot{Candidate: 20, Active: 21, Retired: 22, Failed: 23, OldestAgeSec: 24},
			Outbox:       OutboxSnapshot{Pending: 25, Acknowledged: 26, OldestAgeSec: 27},
		}, nil
	}))

	rendered := string(registry.Render(context.Background()))
	for _, expected := range []string{
		`open_agentic_catalog_process_state{state="running"} 1`,
		`open_agentic_catalog_component_state{component="job_worker",state="degraded"} 1`,
		`open_agentic_catalog_dependency_ready{dependency="postgresql"} 1`,
		`open_agentic_catalog_http_responses_total{method="POST",route="/api/v1/resources/{resourceId}",status_class="2xx"} 1`,
		`open_agentic_catalog_http_response_bytes_total{method="POST",route="/api/v1/resources/{resourceId}",status_class="2xx"} 123`,
		`open_agentic_catalog_http_responses_total{method="OTHER",route="unmatched",status_class="5xx"} 1`,
		`open_agentic_catalog_operations_total{operation="catalog_search",outcome="partial"} 1`,
		`open_agentic_catalog_job_executions_total{kind="source_sync",outcome="succeeded"} 1`,
		`open_agentic_catalog_job_executions_total{kind="other",outcome="failed"} 1`,
		`open_agentic_catalog_postgresql_operations_total{operation="select",outcome="succeeded"} 1`,
		`open_agentic_catalog_job_executions_active{kind="semantic_reindex"} 1`,
		`open_agentic_catalog_job_executions_total{kind="semantic_reindex",outcome="succeeded"} 1`,
		`open_agentic_catalog_postgresql_operations_total{operation="other",outcome="failed"} 1`,
		`open_agentic_catalog_postgresql_pool_connections{state="maximum"} 4`,
		`open_agentic_catalog_postgresql_pool_acquire_seconds_total 0.02`,
		`open_agentic_catalog_postgresql_pool_empty_acquire_seconds_total 0.001`,
		`open_agentic_catalog_jobs_by_kind{kind="publication_generate"} 9`,
		`open_agentic_catalog_source_last_completed_age_seconds 19`,
		`open_agentic_catalog_publication_oldest_active_age_seconds 24`,
		`open_agentic_catalog_outbox_oldest_pending_age_seconds 27`,
		`open_agentic_catalog_operational_snapshot_available 1`,
		`open_agentic_catalog_housekeeping_cycles_total{outcome="succeeded"} 1`,
		`open_agentic_catalog_housekeeping_batches_total{class="terminal_job",outcome="succeeded"} 1`,
		`open_agentic_catalog_housekeeping_batches_total{class="terminal_job",outcome="failed"} 1`,
		`open_agentic_catalog_housekeeping_reclaimed_rows_total{class="terminal_job"} 3`,
		`open_agentic_catalog_housekeeping_marked_rows_total{class="terminal_job"} 4`,
		`open_agentic_catalog_housekeeping_batch_saturated_total{class="terminal_job"} 1`,
		`open_agentic_catalog_housekeeping_oldest_eligible_age_seconds{class="terminal_job"} 120`,
		`open_agentic_catalog_housekeeping_last_success_timestamp_seconds{class="terminal_job"} 1788436800`,
	} {
		if !strings.Contains(rendered, expected+"\n") {
			t.Errorf("metrics omit %q\n%s", expected, rendered)
		}
	}
	for _, private := range []string{"private.example", "token=secret", "private_table", "secret_table", "postgres://", "tenant-private", "private-operation", "private-outcome", "private-error"} {
		if strings.Contains(rendered, private) {
			t.Errorf("metrics exposed %q\n%s", private, rendered)
		}
	}
	if second := string(registry.Render(context.Background())); second != rendered {
		t.Fatal("unchanged metrics did not render deterministically")
	}

	state, degraded, components, dependencies := registry.HealthState()
	if state != LifecycleRunning || !degraded || components[ComponentAPI] != ComponentRunning || !dependencies["postgresql"] {
		t.Fatalf("health state = %q degraded=%v components=%v dependencies=%v", state, degraded, components, dependencies)
	}
	components[ComponentAPI] = ComponentStopped
	dependencies["postgresql"] = false
	_, _, componentsAgain, dependenciesAgain := registry.HealthState()
	if componentsAgain[ComponentAPI] != ComponentRunning || !dependenciesAgain["postgresql"] {
		t.Fatal("health state returned mutable registry maps")
	}
}

func TestRegistryReportsSnapshotFailureAndEnforcesRenderLimit(t *testing.T) {
	registry := New(nil)
	registry.SetSnapshotProvider(snapshotProviderFunc(func(context.Context, time.Time) (OperationalSnapshot, error) {
		return OperationalSnapshot{}, errors.New("sensitive snapshot failure")
	}))
	if report, err := registry.Snapshot(context.Background()); err == nil || !report.OperationalConfigured || report.OperationalAvailable || report.Operational != nil {
		t.Fatalf("failed snapshot = %#v, %v", report, err)
	}
	rendered := string(registry.Render(context.Background()))
	if !strings.Contains(rendered, "open_agentic_catalog_operational_snapshot_available 0") || !strings.Contains(rendered, "open_agentic_catalog_operational_snapshot_failures_total 2") || strings.Contains(rendered, "sensitive") {
		t.Fatalf("failed snapshot metrics = %s", rendered)
	}

	registry.SetSnapshotProvider(nil)
	if report, err := registry.Snapshot(context.Background()); err != nil || report.OperationalConfigured || report.OperationalAvailable || report.Operational != nil {
		t.Fatalf("unconfigured snapshot = %#v, %v", report, err)
	}
	registry.mu.Lock()
	for index := 0; index < 5000; index++ {
		registry.http[httpKey{method: "GET", route: "/bounded/" + strings.Repeat("x", 120) + strconv.Itoa(index), class: "2xx"}] = &httpMetric{requests: 1, count: 1}
	}
	registry.mu.Unlock()
	if bounded := string(registry.Render(context.Background())); bounded != "# metrics output exceeded its fixed service bound\n" {
		t.Fatalf("oversized metric output was not replaced by the bounded sentinel: %d bytes", len(bounded))
	}
}

func TestRegistrySafeVocabularies(t *testing.T) {
	for input, expected := range map[string]string{"GET": "GET", "TRACE": "OTHER"} {
		if actual := safeMethod(input); actual != expected {
			t.Errorf("safeMethod(%q) = %q, want %q", input, actual, expected)
		}
	}
	for input, expected := range map[string]string{"GET /readyz": "/readyz", "/api/v1/resources/{id}": "/api/v1/resources/{id}", "": "unmatched", "https://example.com": "unmatched", "/bad\nroute": "unmatched"} {
		if actual := safeRoute(input); actual != expected {
			t.Errorf("safeRoute(%q) = %q, want %q", input, actual, expected)
		}
	}
	for status, expected := range map[int]string{0: "2xx", 201: "2xx", 404: "4xx", 503: "5xx", 700: "2xx"} {
		if actual := statusClass(status); actual != expected {
			t.Errorf("statusClass(%d) = %q, want %q", status, actual, expected)
		}
	}
	if JobKindLabel("publication.generate") != "publication_generate" || JobKindLabel("source.trust_verify") != "source_trust_verify" || JobKindLabel("unknown") != "other" || safeDatabaseOperation("UPDATE") != "update" || safeDatabaseOperation("truncate") != "other" {
		t.Fatal("bounded vocabulary normalization changed")
	}
}

// TestRegistryStateTracksComponentRecovery keeps lifecycle, component degradation
// and required-dependency readiness separate without collecting diagnostics.
func TestRegistryStateTracksComponentRecovery(t *testing.T) {
	t.Parallel()
	r := New(nil)
	r.SetSnapshotProvider(snapshotProviderFunc(func(context.Context, time.Time) (OperationalSnapshot, error) {
		t.Fatal("State collected operational data")
		return OperationalSnapshot{}, nil
	}))
	r.SetPoolSnapshot(func() PoolSnapshot {
		t.Fatal("State collected pool data")
		return PoolSnapshot{}
	})
	r.runtimeRead = func() runtimeReading {
		t.Fatal("State collected runtime metrics")
		return runtimeReading{}
	}
	assertState := func(lifecycle Lifecycle, degraded bool) {
		t.Helper()
		if got, unhealthy := r.State(); got != lifecycle || unhealthy != degraded {
			t.Fatalf("State = (%s, %t), want (%s, %t)", got, unhealthy, lifecycle, degraded)
		}
	}
	assertState(LifecycleStarting, false)
	r.SetLifecycle(LifecycleRunning)
	r.SetComponent(ComponentWorker, ComponentRunning)
	r.SetComponent(ComponentOutbox, ComponentRunning)
	assertState(LifecycleRunning, false)
	r.SetComponent(ComponentWorker, ComponentDegraded)
	assertState(LifecycleRunning, true)
	r.SetComponent(ComponentOutbox, ComponentDegraded)
	r.SetComponent(ComponentWorker, ComponentRunning)
	assertState(LifecycleRunning, true)
	r.SetComponent(ComponentOutbox, ComponentRunning)
	assertState(LifecycleRunning, false)
	r.SetDependency("postgresql", false)
	assertState(LifecycleRunning, false)
	r.SetLifecycle(LifecycleDraining)
	assertState(LifecycleDraining, false)
	r.SetLifecycle(LifecycleStopped)
	assertState(LifecycleStopped, false)
}

// TestRequestDeadlineCounter accumulates reports independently of HTTP response
// accounting and keeps scrapes from resetting the label-free counter.
func TestRequestDeadlineCounter(t *testing.T) {
	t.Parallel()
	r := New(nil)
	for count := uint64(0); count <= 3; count++ {
		if count > 0 {
			r.RecordRequestDeadline()
		}
		for range 2 {
			assertCounterSample(t, string(r.Render(t.Context())), "open_agentic_catalog_http_request_deadlines_total", count)
		}
	}
}

// assertCounterSample requires one exact label-free sample and its counter type.
func assertCounterSample(t *testing.T, body, name string, value uint64) {
	t.Helper()
	want := name + " " + strconv.FormatUint(value, 10)
	var samples []string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, name) {
			samples = append(samples, line)
		}
	}
	if len(samples) != 1 || samples[0] != want || !strings.Contains(body, "# TYPE "+name+" counter\n") {
		t.Fatalf("counter %s: samples=%q, want one %q and counter type", name, samples, want)
	}
}

func TestProcessMetricsKeepElapsedTimeWhenWallClockMovesBackwards(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		process, err := processinfo.New()
		if err != nil {
			t.Fatal(err)
		}
		wall := time.Now()
		registry := New(func() time.Time { return wall }, process)
		time.Sleep(4 * time.Second)
		wall = wall.Add(-7 * time.Hour)
		report, err := registry.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		metrics := string(registry.Render(t.Context()))
		start := fmt.Sprintf("%.3f", float64(process.Read().StartedAt.UnixMilli())/1000)
		if report.UptimeSeconds != 4 || !strings.Contains(metrics, "open_agentic_catalog_process_uptime_seconds 4\n") || !strings.Contains(metrics, "open_agentic_catalog_process_start_time_seconds "+start+"\n") || strings.Contains(metrics, process.ID()) {
			t.Fatalf("clock correction affected process metrics: %s", metrics)
		}
	})
}
