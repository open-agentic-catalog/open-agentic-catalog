// Package observability owns the service's bounded, provider-neutral runtime
// metrics and diagnostic snapshots. It deliberately accepts only fixed label
// vocabularies so request data and tenant identifiers cannot become labels.
package observability

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/housekeeping"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
)

// MaxMetricsBytes is the hard upper bound for one rendered metrics response.
const MaxMetricsBytes = 1 << 20

// Lifecycle is the bounded process lifecycle vocabulary exposed by health and
// metrics surfaces.
type Lifecycle string

const (
	LifecycleStarting Lifecycle = "starting"
	LifecycleRunning  Lifecycle = "running"
	LifecycleDraining Lifecycle = "draining"
	LifecycleStopped  Lifecycle = "stopped"
)

// Component identifies one fixed process component. It must never contain a
// tenant, resource, endpoint, or other runtime-supplied value.
type Component string

const (
	ComponentAPI                 Component = "api"
	ComponentWorker              Component = "job_worker"
	ComponentOutbox              Component = "outbox_processor"
	ComponentSourceMaintenance   Component = "source_maintainer"
	ComponentSemanticMaintenance Component = "semantic_maintainer"
	ComponentHousekeeping        Component = "housekeeper"
	ComponentOperations          Component = "operations_server"
	ComponentWebhookDelivery     Component = "webhook_delivery"
)

// ComponentState is the bounded health state of a process component.
type ComponentState string

const (
	ComponentStarting ComponentState = "starting"
	ComponentRunning  ComponentState = "running"
	ComponentDegraded ComponentState = "degraded"
	ComponentStopped  ComponentState = "stopped"
)

// Operation identifies a fixed service workflow for outcome counters.
type Operation string

const (
	OperationCatalogSearch     Operation = "catalog_search"
	OperationObservationSearch Operation = "observation_search"
	OperationLiveSearch        Operation = "live_search"
	OperationPublicationBuild  Operation = "publication_build"
	OperationPublicationChange Operation = "publication_change"
	OperationSourceSync        Operation = "source_sync"
	OperationValidation        Operation = "validation"
	OperationOutboxProcessing  Operation = "outbox_processing"
	OperationReadiness         Operation = "readiness"
)

// Outcome is the bounded result vocabulary used for workflow counters.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomePartial   Outcome = "partial"
	OutcomeRejected  Outcome = "rejected"
	OutcomeFailed    Outcome = "failed"
	OutcomeIdle      Outcome = "idle"
)

// PoolSnapshot is the fixed provider-neutral subset of PostgreSQL pool state
// used by metrics and diagnostics.
type PoolSnapshot struct {
	AcquireCount            int64   `json:"acquireCount"`
	AcquireSeconds          float64 `json:"acquireSeconds"`
	CanceledAcquireCount    int64   `json:"canceledAcquireCount"`
	EmptyAcquireCount       int64   `json:"emptyAcquireCount"`
	EmptyAcquireWaitSeconds float64 `json:"emptyAcquireWaitSeconds"`
	AcquiredConnections     int32   `json:"acquiredConnections"`
	IdleConnections         int32   `json:"idleConnections"`
	TotalConnections        int32   `json:"totalConnections"`
	MaxConnections          int32   `json:"maxConnections"`
}

// JobSnapshot contains only fixed global Job counts and an age signal.
type JobSnapshot struct {
	Semantic        int64 `json:"semantic"`
	SemanticPending int64 `json:"semanticPending"`
	Queued          int64 `json:"queued"`
	Running         int64 `json:"running"`
	Waiting         int64 `json:"waiting"`
	Succeeded       int64 `json:"succeeded"`
	Failed          int64 `json:"failed"`
	Cancelled       int64 `json:"cancelled"`
	DeadLetter      int64 `json:"deadLetter"`
	SourceSync      int64 `json:"sourceSync"`
	Publication     int64 `json:"publication"`
	Other           int64 `json:"other"`
	OldestAgeSec    int64 `json:"oldestRunnableAgeSeconds"`
}

// SourceSnapshot contains only fixed global source and synchronization counts.
type SourceSnapshot struct {
	Active     int64 `json:"active"`
	Paused     int64 `json:"paused"`
	Error      int64 `json:"error"`
	Removed    int64 `json:"removed"`
	Complete   int64 `json:"completeSnapshots"`
	Incomplete int64 `json:"incompleteSnapshots"`
	Failed     int64 `json:"failedSnapshots"`
	LastAgeSec int64 `json:"lastCompletedAgeSeconds"`
}

// PublicationSnapshot contains only fixed global publication counts and age.
type PublicationSnapshot struct {
	Candidate    int64 `json:"candidate"`
	Active       int64 `json:"active"`
	Retired      int64 `json:"retired"`
	Failed       int64 `json:"failed"`
	OldestAgeSec int64 `json:"oldestActiveAgeSeconds"`
}

// OutboxSnapshot contains only fixed global outbox counts and pending age.
type OutboxSnapshot struct {
	Pending      int64 `json:"pending"`
	Acknowledged int64 `json:"acknowledged"`
	OldestAgeSec int64 `json:"oldestPendingAgeSeconds"`
}

// OperationalSnapshot is a fixed aggregate projection. It intentionally has
// no record identifiers, tenant dimensions, URLs, queries, or error text.
type OperationalSnapshot struct {
	CollectedAt  time.Time           `json:"collectedAt"`
	Jobs         JobSnapshot         `json:"jobs"`
	Sources      SourceSnapshot      `json:"sources"`
	Publications PublicationSnapshot `json:"publications"`
	Outbox       OutboxSnapshot      `json:"outbox"`
	Webhooks     WebhookSnapshot     `json:"webhooks"`
}

// SnapshotProvider reads a fixed aggregate operational projection.
type SnapshotProvider interface {
	OperationalSnapshot(context.Context, time.Time) (OperationalSnapshot, error)
}

// DiagnosticReport is a bounded process-local and aggregate service snapshot.
type DiagnosticReport struct {
	Lifecycle             Lifecycle                    `json:"lifecycle"`
	UptimeSeconds         int64                        `json:"uptimeSeconds"`
	Components            map[Component]ComponentState `json:"components"`
	Dependencies          map[string]bool              `json:"dependencies"`
	PostgreSQLPool        *PoolSnapshot                `json:"postgresqlPool,omitempty"`
	OperationalConfigured bool                         `json:"operationalConfigured"`
	OperationalAvailable  bool                         `json:"operationalAvailable"`
	Operational           *OperationalSnapshot         `json:"operational,omitempty"`
}

type httpKey struct {
	method string
	route  string
	class  string
}

type httpMetric struct {
	requests uint64
	bytes    uint64
	count    uint64
	sum      float64
	buckets  [12]uint64
}

type operationKey struct {
	operation Operation
	outcome   Outcome
}

type jobKey struct {
	kind    string
	outcome string
}

type databaseKey struct {
	operation string
	outcome   string
}

type housekeepingKey struct {
	class   housekeeping.Class
	outcome string
}

type durationMetric struct {
	count   uint64
	sum     float64
	buckets [12]uint64
}

type housekeepingMetric struct {
	batches     map[housekeepingKey]uint64
	durations   map[housekeepingKey]durationMetric
	reclaimed   map[housekeeping.Class]uint64
	marked      map[housekeeping.Class]uint64
	saturated   map[housekeeping.Class]uint64
	oldest      map[housekeeping.Class]float64
	lastSuccess map[housekeeping.Class]float64
	cycles      map[string]uint64
	cycleTimes  map[string]durationMetric
}

// Registry stores only process-local counters and fixed aggregate snapshots.
// It is safe for concurrent request, worker, and scrape use.
type Registry struct {
	activity            [14]activityMetric
	activeCatalog       uint64
	activityInvalid     bool
	mcp                 map[mcpKey]uint64
	embeddings          map[embeddingKey]durationMetric
	retrieval           map[embeddingKey]uint64
	traceBatches        uint64
	traceSpans          uint64
	traceFailures       uint64
	mu                  sync.Mutex
	startedAt           time.Time
	process             *processinfo.Process
	now                 func() time.Time
	lifecycle           Lifecycle
	components          map[Component]ComponentState
	dependencies        map[string]bool
	requests            uint64
	errors              uint64
	requestDeadlines    uint64
	securityRefusals    [2]uint64
	activeRequests      int64
	http                map[httpKey]*httpMetric
	operations          map[operationKey]uint64
	activeJobs          map[string]int64
	jobs                map[jobKey]uint64
	database            map[databaseKey]uint64
	databaseSeconds     map[databaseKey]float64
	housekeeping        housekeepingMetric
	webhookAttempts     map[webhookAttemptKey]durationMetric
	jobRetries          map[string]retryMetric
	webhookRetries      retryMetric
	poolSnapshot        func() PoolSnapshot
	provider            SnapshotProvider
	operationalFailures uint64
	runtimeRead         func() runtimeReading
}

// RecordRequestDeadline increments one label-free product-work outcome. Route
// detail remains available through the ordinary bounded HTTP metric family.
func (r *Registry) RecordRequestDeadline() {
	r.mu.Lock()
	r.requestDeadlines++
	r.mu.Unlock()
}

var durationBuckets = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// New creates an empty registry with an injectable clock for deterministic
// uptime and aggregate-snapshot tests. An optional shared process supplies
// startup and monotonic elapsed time; the clock still timestamps aggregates.
func New(now func() time.Time, process ...*processinfo.Process) *Registry {
	if now == nil {
		now = time.Now
	}
	started := now()
	registry := &Registry{
		runtimeRead:     readRuntime,
		startedAt:       started,
		now:             now,
		lifecycle:       LifecycleStarting,
		components:      make(map[Component]ComponentState),
		dependencies:    make(map[string]bool),
		http:            make(map[httpKey]*httpMetric),
		operations:      make(map[operationKey]uint64),
		activeJobs:      make(map[string]int64),
		jobs:            make(map[jobKey]uint64),
		database:        make(map[databaseKey]uint64),
		databaseSeconds: make(map[databaseKey]float64),
		housekeeping: housekeepingMetric{
			batches: make(map[housekeepingKey]uint64), durations: make(map[housekeepingKey]durationMetric),
			reclaimed: make(map[housekeeping.Class]uint64), marked: make(map[housekeeping.Class]uint64),
			saturated: make(map[housekeeping.Class]uint64), oldest: make(map[housekeeping.Class]float64),
			lastSuccess: make(map[housekeeping.Class]float64), cycles: make(map[string]uint64),
			cycleTimes: make(map[string]durationMetric),
		},
	}
	if len(process) > 0 && process[0] != nil {
		registry.process = process[0]
		registry.startedAt = process[0].Read().StartedAt
	}
	return registry
}

// SetPoolSnapshot installs a process-local PostgreSQL pool summary provider.
// Provider-specific types remain in the database composition boundary.
func (r *Registry) SetPoolSnapshot(provider func() PoolSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.poolSnapshot = provider
}

// SetSnapshotProvider installs the worker-only database aggregate provider.
func (r *Registry) SetSnapshotProvider(provider SnapshotProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.provider = provider
}

// SetLifecycle records a valid process lifecycle transition for reporting.
func (r *Registry) SetLifecycle(state Lifecycle) {
	if !validLifecycle(state) {
		return
	}
	r.mu.Lock()
	r.lifecycle = state
	r.mu.Unlock()
}

// SetComponent records one fixed process component state.
func (r *Registry) SetComponent(component Component, state ComponentState) {
	if !validComponent(component) || !validComponentState(state) {
		return
	}
	r.mu.Lock()
	r.components[component] = state
	r.mu.Unlock()
}

// SetDependency records readiness for a fixed required dependency. PostgreSQL
// is currently the only dependency whose availability gates either role.
func (r *Registry) SetDependency(name string, ready bool) {
	if name != "postgresql" {
		return
	}
	r.mu.Lock()
	r.dependencies[name] = ready
	r.mu.Unlock()
}

// UpdateDependency updates a dependency that the composition root already
// registered. This prevents the in-memory development runtime from claiming a
// PostgreSQL dependency merely because its repository readiness check ran.
func (r *Registry) UpdateDependency(name string, ready bool) {
	if name != "postgresql" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.dependencies[name]; exists {
		r.dependencies[name] = ready
	}
}

// State returns the current lifecycle and whether any process component is
// degraded without performing dependency I/O.
func (r *Registry) State() (Lifecycle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	degraded := false
	for _, state := range r.components {
		if state == ComponentDegraded {
			degraded = true
			break
		}
	}
	return r.lifecycle, degraded
}

// HealthState returns a bounded point-in-time process view without contacting
// a dependency or collecting database aggregates.
func (r *Registry) HealthState() (Lifecycle, bool, map[Component]ComponentState, map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	degraded := false
	for _, state := range r.components {
		if state == ComponentDegraded {
			degraded = true
			break
		}
	}
	return r.lifecycle, degraded, cloneComponents(r.components), cloneDependencies(r.dependencies)
}

// BeginHTTP increments the active gauge and returns the single completion
// callback for a request. The route is accepted only after ServeMux resolves a
// registered pattern; unmatched and unsafe values collapse to one label.
func (r *Registry) BeginHTTP() func(method, route string, status int, responseBytes int64, elapsed time.Duration) {
	return r.BeginHTTPFamily(FamilyAuxiliary)
}

// BeginHTTPFamily records listener classification before admitting active work.
// Non-API callers use BeginHTTP and remain auxiliary, even with shared routes.
func (r *Registry) BeginHTTPFamily(family HTTPFamily) func(method, route string, status int, responseBytes int64, elapsed time.Duration) {
	if family >= activityFamilyCount {
		family = FamilyAuxiliary
	}
	r.mu.Lock()
	activeAdded := false
	if family <= FamilyControlCommand {
		activeAdded = incrementActivity(&r.activeCatalog)
		r.activityInvalid = r.activityInvalid || !activeAdded
	}
	r.activeRequests++
	r.mu.Unlock()
	var once sync.Once
	return func(method, route string, status int, responseBytes int64, elapsed time.Duration) {
		once.Do(func() {
			key := httpKey{method: safeMethod(method), route: safeRoute(route), class: statusClass(status)}
			seconds := max(0, elapsed.Seconds())
			r.mu.Lock()
			defer r.mu.Unlock()
			r.activeRequests--
			if activeAdded {
				r.activeCatalog--
			}
			r.activityInvalid = r.activityInvalid || r.requests == ^uint64(0)
			r.requests++
			metricFamily := &r.activity[family]
			if !incrementActivity(&metricFamily.completed) {
				r.activityInvalid = true
			}
			metricFamily.seconds += seconds
			for i, methodName := range activityMethods {
				if key.method == methodName && !incrementActivity(&metricFamily.methods[i]) {
					r.activityInvalid = true
				}
			}
			for i, class := range activityClasses {
				if key.class == class && !incrementActivity(&metricFamily.classes[i]) {
					r.activityInvalid = true
				}
			}
			if status >= 500 {
				r.errors++
			}
			metric := r.http[key]
			if metric == nil {
				metric = &httpMetric{}
				r.http[key] = metric
			}
			metric.requests++
			if responseBytes > 0 {
				metric.bytes += uint64(responseBytes)
			}
			metric.count++
			metric.sum += seconds
			for index, bucket := range durationBuckets {
				if seconds <= bucket {
					metric.buckets[index]++
				}
			}
		})
	}
}

// ObserveOperation increments one fixed workflow outcome counter.
func (r *Registry) ObserveOperation(operation Operation, outcome Outcome) {
	if !validOperation(operation) || !validOutcome(outcome) {
		return
	}
	r.mu.Lock()
	if operation == OperationCatalogSearch && r.operations[operationKey{operation: operation, outcome: outcome}] == ^uint64(0) {
		r.activityInvalid = true
	}
	r.operations[operationKey{operation: operation, outcome: outcome}]++
	r.mu.Unlock()
}

// JobStarted records one active execution using a bounded Job-kind label.
func (r *Registry) JobStarted(kind string) {
	kind = JobKindLabel(kind)
	r.mu.Lock()
	r.activeJobs[kind]++
	r.mu.Unlock()
}

// JobFinished completes an active execution and records its bounded outcome.
func (r *Registry) JobFinished(_ context.Context, kind string, result jobs.AttemptResult) {
	kind = JobKindLabel(kind)
	outcome := JobOutcomeLabel(result.Outcome)
	r.mu.Lock()
	if r.activeJobs[kind] > 0 {
		r.activeJobs[kind]--
	}
	r.jobs[jobKey{kind: kind, outcome: outcome}]++
	r.mu.Unlock()
}

// OutboxBatch records only whether a bounded dispatch batch was idle,
// successful, or failed. The error value is never retained or rendered.
func (r *Registry) OutboxBatch(processed int, err error) {
	outcome := OutcomeIdle
	if err != nil {
		outcome = OutcomeFailed
	} else if processed > 0 {
		outcome = OutcomeSucceeded
	}
	r.ObserveOperation(OperationOutboxProcessing, outcome)
}

// ObserveHousekeepingBatch records only a fixed class and aggregate bounded
// result. It deliberately never accepts the underlying error or row identity.
func (r *Registry) ObserveHousekeepingBatch(class housekeeping.Class, outcome string, result housekeeping.Result, elapsed time.Duration) {
	if !validHousekeepingClass(class) || outcome != "succeeded" && outcome != "failed" {
		return
	}
	key := housekeepingKey{class: class, outcome: outcome}
	r.mu.Lock()
	r.housekeeping.batches[key]++
	r.housekeeping.durations[key] = observeDuration(r.housekeeping.durations[key], elapsed)
	if outcome == "succeeded" {
		if result.Reclaimed > 0 {
			r.housekeeping.reclaimed[class] += uint64(result.Reclaimed)
		}
		if result.Marked > 0 {
			r.housekeeping.marked[class] += uint64(result.Marked)
		}
		if result.Saturated {
			r.housekeeping.saturated[class]++
		}
		if result.OldestEligibleAge > 0 {
			r.housekeeping.oldest[class] = result.OldestEligibleAge.Seconds()
		} else {
			r.housekeeping.oldest[class] = 0
		}
		r.housekeeping.lastSuccess[class] = float64(r.now().UTC().Unix())
	}
	r.mu.Unlock()
}

// ObserveHousekeepingCycle records the aggregate fair-cycle outcome after all
// classes have received an opportunity.
func (r *Registry) ObserveHousekeepingCycle(outcome string, elapsed time.Duration) {
	if outcome != "succeeded" && outcome != "failed" {
		return
	}
	r.mu.Lock()
	r.housekeeping.cycles[outcome]++
	r.housekeeping.cycleTimes[outcome] = observeDuration(r.housekeeping.cycleTimes[outcome], elapsed)
	r.mu.Unlock()
}

// ObserveDatabase records a query using only its fixed statement class,
// outcome, and duration. The SQL text and error value are never retained.
func (r *Registry) ObserveDatabase(operation string, err error, elapsed time.Duration) {
	operation = safeDatabaseOperation(operation)
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
	}
	key := databaseKey{operation: operation, outcome: outcome}
	r.mu.Lock()
	r.database[key]++
	r.databaseSeconds[key] += max(0, elapsed.Seconds())
	r.mu.Unlock()
}

// Snapshot returns bounded process state and, when configured, one current
// aggregate database projection.
func (r *Registry) Snapshot(ctx context.Context) (DiagnosticReport, error) {
	r.mu.Lock()
	now := r.now()
	report := DiagnosticReport{
		Lifecycle:     r.lifecycle,
		UptimeSeconds: max(int64(0), int64(now.Sub(r.startedAt).Seconds())),
		Components:    cloneComponents(r.components),
		Dependencies:  cloneDependencies(r.dependencies),
	}
	if r.process != nil {
		report.UptimeSeconds = r.process.Read().UptimeSeconds
	}
	now = now.UTC()
	poolSnapshotProvider := r.poolSnapshot
	provider := r.provider
	report.OperationalConfigured = provider != nil
	r.mu.Unlock()
	if poolSnapshotProvider != nil {
		value := poolSnapshotProvider()
		report.PostgreSQLPool = &value
	}
	if provider == nil {
		return report, nil
	}
	snapshot, err := provider.OperationalSnapshot(ctx, now)
	if err != nil {
		if _, configured := report.Components[ComponentWebhookDelivery]; configured {
			report.Components[ComponentWebhookDelivery] = ComponentDegraded
			if report.Lifecycle == LifecycleStopped {
				report.Components[ComponentWebhookDelivery] = ComponentStopped
			}
		}
		r.mu.Lock()
		r.operationalFailures++
		r.mu.Unlock()
		return report, err
	}
	report.OperationalAvailable = true
	report.Operational = &snapshot
	if _, configured := report.Components[ComponentWebhookDelivery]; configured {
		report.Components[ComponentWebhookDelivery] = ComponentRunning
		if snapshot.Webhooks.LastAttemptFailedReceivers > 0 {
			report.Components[ComponentWebhookDelivery] = ComponentDegraded
		}
		if report.Lifecycle == LifecycleStopped {
			report.Components[ComponentWebhookDelivery] = ComponentStopped
		}
	}
	return report, nil
}

// Render returns a deterministic, size-bounded Prometheus text exposition.
func (r *Registry) Render(ctx context.Context) []byte {
	report, snapshotErr := r.Snapshot(ctx)
	r.mu.Lock()
	requests := r.requests
	errorsTotal := r.errors
	requestDeadlines := r.requestDeadlines
	active := r.activeRequests
	httpValues := cloneHTTP(r.http)
	operations := cloneOperations(r.operations)
	activeJobs := cloneActiveJobs(r.activeJobs)
	jobs := cloneJobs(r.jobs)
	databaseValues := cloneDatabase(r.database)
	databaseSeconds := cloneDatabaseSeconds(r.databaseSeconds)
	housekeepingValues := cloneHousekeeping(r.housekeeping)
	webhookValues := cloneWebhookAttempts(r.webhookAttempts)
	operationalFailures := r.operationalFailures
	r.mu.Unlock()

	var output bytes.Buffer
	writeRuntime(&output, r.runtimeRead())
	writeMetricHeader(&output, "open_agentic_catalog_process_start_time_seconds", "Unix timestamp when this process initialized.", "gauge")
	fmt.Fprintf(&output, "open_agentic_catalog_process_start_time_seconds %.3f\n", float64(r.startedAt.UnixMilli())/1000)
	writeMetricHeader(&output, "open_agentic_catalog_process_uptime_seconds", "Seconds since this process initialized.", "gauge")
	fmt.Fprintf(&output, "open_agentic_catalog_process_uptime_seconds %d\n", report.UptimeSeconds)
	writeMetricHeader(&output, "open_agentic_catalog_process_state", "Current bounded process lifecycle state.", "gauge")
	for _, state := range []Lifecycle{LifecycleStarting, LifecycleRunning, LifecycleDraining, LifecycleStopped} {
		value := 0
		if report.Lifecycle == state {
			value = 1
		}
		fmt.Fprintf(&output, "open_agentic_catalog_process_state{state=%s} %d\n", quote(string(state)), value)
	}
	writeComponentMetrics(&output, report.Components)
	writeDependencyMetrics(&output, report.Dependencies)

	writeMetricHeader(&output, "open_agentic_catalog_http_requests_total", "HTTP requests completed.", "counter")
	fmt.Fprintf(&output, "open_agentic_catalog_http_requests_total %d\n", requests)
	writeMetricHeader(&output, "open_agentic_catalog_http_errors_total", "HTTP responses with status 500 or above.", "counter")
	fmt.Fprintf(&output, "open_agentic_catalog_http_errors_total %d\n", errorsTotal)
	writeMetricHeader(&output, "open_agentic_catalog_http_request_deadlines_total", "Product requests whose cooperative service deadline expired.", "counter")
	fmt.Fprintf(&output, "open_agentic_catalog_http_request_deadlines_total %d\n", requestDeadlines)
	writeMetricHeader(&output, "open_agentic_catalog_http_active_requests", "HTTP requests currently executing.", "gauge")
	fmt.Fprintf(&output, "open_agentic_catalog_http_active_requests %d\n", active)
	writeHTTPMetrics(&output, httpValues)
	writeOperationMetrics(&output, operations)
	writeJobMetrics(&output, activeJobs, jobs)
	writeDatabaseMetrics(&output, databaseValues, databaseSeconds)
	writeHousekeepingMetrics(&output, housekeepingValues)
	writeWebhookAttemptMetrics(&output, webhookValues)
	if report.PostgreSQLPool != nil {
		writePoolMetrics(&output, *report.PostgreSQLPool)
	}
	if report.OperationalConfigured {
		writeMetricHeader(&output, "open_agentic_catalog_operational_snapshot_available", "Whether the configured bounded aggregate operational snapshot was collected.", "gauge")
		available := 0
		if report.OperationalAvailable && snapshotErr == nil {
			available = 1
		}
		fmt.Fprintf(&output, "open_agentic_catalog_operational_snapshot_available %d\n", available)
		writeMetricHeader(&output, "open_agentic_catalog_operational_snapshot_failures_total", "Bounded aggregate snapshot collection failures.", "counter")
		fmt.Fprintf(&output, "open_agentic_catalog_operational_snapshot_failures_total %d\n", operationalFailures)
	}
	if report.Operational != nil {
		writeOperationalMetrics(&output, *report.Operational)
		writeWebhookOperationalMetrics(&output, report.Operational.Webhooks)
	}
	r.writeTraceMetrics(&output)
	r.writeSecurityMetrics(&output)
	r.writeEmbeddingMetrics(&output)
	r.writeMCPMetrics(&output)
	r.writeRetryMetrics(&output)
	if output.Len() > MaxMetricsBytes {
		return []byte("# metrics output exceeded its fixed service bound\n")
	}
	return output.Bytes()
}

// writeHTTPMetrics renders sorted route/status counters and cumulative latency buckets from a detached snapshot.
func writeHTTPMetrics(output *bytes.Buffer, values map[httpKey]httpMetric) {
	writeMetricHeader(output, "open_agentic_catalog_http_responses_total", "HTTP responses by registered route template and status class.", "counter")
	writeMetricHeader(output, "open_agentic_catalog_http_response_bytes_total", "HTTP response bytes by registered route template and status class.", "counter")
	writeMetricHeader(output, "open_agentic_catalog_http_request_duration_seconds", "HTTP request latency by registered route template and status class.", "histogram")
	keys := make([]httpKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].method+"\x00"+keys[i].route+"\x00"+keys[i].class < keys[j].method+"\x00"+keys[j].route+"\x00"+keys[j].class
	})
	for _, key := range keys {
		metric := values[key]
		labels := fmt.Sprintf("method=%s,route=%s,status_class=%s", quote(key.method), quote(key.route), quote(key.class))
		fmt.Fprintf(output, "open_agentic_catalog_http_responses_total{%s} %d\n", labels, metric.requests)
		fmt.Fprintf(output, "open_agentic_catalog_http_response_bytes_total{%s} %d\n", labels, metric.bytes)
		for index, bucket := range durationBuckets {
			fmt.Fprintf(output, "open_agentic_catalog_http_request_duration_seconds_bucket{%s,le=%s} %d\n", labels, quote(strconv.FormatFloat(bucket, 'g', -1, 64)), metric.buckets[index])
		}
		fmt.Fprintf(output, "open_agentic_catalog_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", labels, metric.count)
		fmt.Fprintf(output, "open_agentic_catalog_http_request_duration_seconds_sum{%s} %s\n", labels, strconv.FormatFloat(metric.sum, 'g', -1, 64))
		fmt.Fprintf(output, "open_agentic_catalog_http_request_duration_seconds_count{%s} %d\n", labels, metric.count)
	}
}

// writeOperationMetrics emits workflow outcome counters in deterministic operation/outcome order.
func writeOperationMetrics(output *bytes.Buffer, values map[operationKey]uint64) {
	writeMetricHeader(output, "open_agentic_catalog_operations_total", "Bounded service workflow outcomes.", "counter")
	keys := make([]operationKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return string(keys[i].operation)+"\x00"+string(keys[i].outcome) < string(keys[j].operation)+"\x00"+string(keys[j].outcome)
	})
	for _, key := range keys {
		fmt.Fprintf(output, "open_agentic_catalog_operations_total{operation=%s,outcome=%s} %d\n", quote(string(key.operation)), quote(string(key.outcome)), values[key])
	}
}

// writeJobMetrics renders fixed-kind active gauges separately from sorted completed-attempt counters.
func writeJobMetrics(output *bytes.Buffer, active map[string]int64, values map[jobKey]uint64) {
	writeMetricHeader(output, "open_agentic_catalog_job_executions_active", "Jobs currently executing in this process by bounded kind.", "gauge")
	for _, kind := range []string{"source_sync", "publication_generate", "semantic_reindex", "other"} {
		fmt.Fprintf(output, "open_agentic_catalog_job_executions_active{kind=%s} %d\n", quote(kind), active[kind])
	}
	writeMetricHeader(output, "open_agentic_catalog_job_executions_total", "Completed job execution attempts by bounded kind and outcome.", "counter")
	keys := make([]jobKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].kind+"\x00"+keys[i].outcome < keys[j].kind+"\x00"+keys[j].outcome })
	for _, key := range keys {
		fmt.Fprintf(output, "open_agentic_catalog_job_executions_total{kind=%s,outcome=%s} %d\n", quote(key.kind), quote(key.outcome), values[key])
	}
}

// writeDatabaseMetrics pairs sorted statement-class outcome counts with their cumulative elapsed seconds.
func writeDatabaseMetrics(output *bytes.Buffer, values map[databaseKey]uint64, seconds map[databaseKey]float64) {
	writeMetricHeader(output, "open_agentic_catalog_postgresql_operations_total", "PostgreSQL operations by safe statement class and outcome.", "counter")
	writeMetricHeader(output, "open_agentic_catalog_postgresql_operation_duration_seconds_total", "Cumulative PostgreSQL operation duration by safe statement class and outcome.", "counter")
	keys := make([]databaseKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].operation+"\x00"+keys[i].outcome < keys[j].operation+"\x00"+keys[j].outcome
	})
	for _, key := range keys {
		labels := fmt.Sprintf("operation=%s,outcome=%s", quote(key.operation), quote(key.outcome))
		fmt.Fprintf(output, "open_agentic_catalog_postgresql_operations_total{%s} %d\n", labels, values[key])
		fmt.Fprintf(output, "open_agentic_catalog_postgresql_operation_duration_seconds_total{%s} %s\n", labels, strconv.FormatFloat(seconds[key], 'g', -1, 64))
	}
}

// writeHousekeepingMetrics emits every fixed cleanup class and success/failure combination, including zero-valued series.
func writeHousekeepingMetrics(output *bytes.Buffer, values housekeepingMetric) {
	classes := []housekeeping.Class{
		housekeeping.ClassIdempotency, housekeeping.ClassSourcePreview, housekeeping.ClassSourceInspection,
		housekeeping.ClassSourceState, housekeeping.ClassTerminalJob, housekeeping.ClassWebhookDelivery, housekeeping.ClassOutbox,
	}
	writeMetricHeader(output, "open_agentic_catalog_housekeeping_cycles_total", "Completed fair housekeeping cycles by outcome.", "counter")
	writeMetricHeader(output, "open_agentic_catalog_housekeeping_cycle_duration_seconds", "Housekeeping fair-cycle duration by outcome.", "histogram")
	for _, outcome := range []string{"failed", "succeeded"} {
		fmt.Fprintf(output, "open_agentic_catalog_housekeeping_cycles_total{outcome=%s} %d\n", quote(outcome), values.cycles[outcome])
		writeDurationHistogram(output, "open_agentic_catalog_housekeeping_cycle_duration_seconds", "outcome="+quote(outcome), values.cycleTimes[outcome])
	}
	writeMetricHeader(output, "open_agentic_catalog_housekeeping_batches_total", "Bounded housekeeping class operations by outcome.", "counter")
	writeMetricHeader(output, "open_agentic_catalog_housekeeping_batch_duration_seconds", "Bounded housekeeping class-operation duration by outcome.", "histogram")
	writeMetricHeader(output, "open_agentic_catalog_housekeeping_reclaimed_rows_total", "Disposable rows reclaimed by fixed housekeeping class.", "counter")
	writeMetricHeader(output, "open_agentic_catalog_housekeeping_marked_rows_total", "Operational rows assigned prospective cleanup eligibility by fixed class.", "counter")
	writeMetricHeader(output, "open_agentic_catalog_housekeeping_batch_saturated_total", "Housekeeping operations that reached their fixed batch ceiling.", "counter")
	writeMetricHeader(output, "open_agentic_catalog_housekeeping_oldest_eligible_age_seconds", "Oldest eligible age observed within the most recent bounded selection.", "gauge")
	writeMetricHeader(output, "open_agentic_catalog_housekeeping_last_success_timestamp_seconds", "Unix timestamp of the last successful bounded class operation.", "gauge")
	for _, class := range classes {
		classLabel := "class=" + quote(string(class))
		for _, outcome := range []string{"failed", "succeeded"} {
			key := housekeepingKey{class: class, outcome: outcome}
			labels := classLabel + ",outcome=" + quote(outcome)
			fmt.Fprintf(output, "open_agentic_catalog_housekeeping_batches_total{%s} %d\n", labels, values.batches[key])
			writeDurationHistogram(output, "open_agentic_catalog_housekeeping_batch_duration_seconds", labels, values.durations[key])
		}
		fmt.Fprintf(output, "open_agentic_catalog_housekeeping_reclaimed_rows_total{%s} %d\n", classLabel, values.reclaimed[class])
		fmt.Fprintf(output, "open_agentic_catalog_housekeeping_marked_rows_total{%s} %d\n", classLabel, values.marked[class])
		fmt.Fprintf(output, "open_agentic_catalog_housekeeping_batch_saturated_total{%s} %d\n", classLabel, values.saturated[class])
		fmt.Fprintf(output, "open_agentic_catalog_housekeeping_oldest_eligible_age_seconds{%s} %s\n", classLabel, strconv.FormatFloat(values.oldest[class], 'g', -1, 64))
		fmt.Fprintf(output, "open_agentic_catalog_housekeeping_last_success_timestamp_seconds{%s} %s\n", classLabel, strconv.FormatFloat(values.lastSuccess[class], 'f', -1, 64))
	}
}

// writeDurationHistogram renders cumulative fixed buckets, the infinite bucket and matching sum/count values.
func writeDurationHistogram(output *bytes.Buffer, name, labels string, metric durationMetric) {
	for index, bucket := range durationBuckets {
		fmt.Fprintf(output, "%s_bucket{%s,le=%s} %d\n", name, labels, quote(strconv.FormatFloat(bucket, 'g', -1, 64)), metric.buckets[index])
	}
	fmt.Fprintf(output, "%s_bucket{%s,le=\"+Inf\"} %d\n", name, labels, metric.count)
	fmt.Fprintf(output, "%s_sum{%s} %s\n", name, labels, strconv.FormatFloat(metric.sum, 'g', -1, 64))
	fmt.Fprintf(output, "%s_count{%s} %d\n", name, labels, metric.count)
}

// observeDuration adds one nonnegative duration to every matching cumulative bucket and returns the updated value.
func observeDuration(metric durationMetric, elapsed time.Duration) durationMetric {
	seconds := max(0, elapsed.Seconds())
	metric.count++
	metric.sum += seconds
	for index, bucket := range durationBuckets {
		if seconds <= bucket {
			metric.buckets[index]++
		}
	}
	return metric
}

// writePoolMetrics projects connection states and acquisition counters without connection identifiers or SQL.
func writePoolMetrics(output *bytes.Buffer, pool PoolSnapshot) {
	writeMetricHeader(output, "open_agentic_catalog_postgresql_pool_connections", "PostgreSQL pool connections by state.", "gauge")
	for _, item := range []struct {
		state string
		value int32
	}{{"acquired", pool.AcquiredConnections}, {"idle", pool.IdleConnections}, {"maximum", pool.MaxConnections}, {"total", pool.TotalConnections}} {
		fmt.Fprintf(output, "open_agentic_catalog_postgresql_pool_connections{state=%s} %d\n", quote(item.state), item.value)
	}
	writeMetricHeader(output, "open_agentic_catalog_postgresql_pool_acquires_total", "PostgreSQL pool acquisitions.", "counter")
	fmt.Fprintf(output, "open_agentic_catalog_postgresql_pool_acquires_total %d\n", pool.AcquireCount)
	writeMetricHeader(output, "open_agentic_catalog_postgresql_pool_acquire_seconds_total", "Cumulative PostgreSQL pool acquisition wait.", "counter")
	fmt.Fprintf(output, "open_agentic_catalog_postgresql_pool_acquire_seconds_total %s\n", strconv.FormatFloat(pool.AcquireSeconds, 'g', -1, 64))
	writeMetricHeader(output, "open_agentic_catalog_postgresql_pool_acquire_canceled_total", "Canceled PostgreSQL pool acquisitions.", "counter")
	fmt.Fprintf(output, "open_agentic_catalog_postgresql_pool_acquire_canceled_total %d\n", pool.CanceledAcquireCount)
	writeMetricHeader(output, "open_agentic_catalog_postgresql_pool_empty_acquires_total", "Acquisitions that waited for an empty PostgreSQL pool.", "counter")
	fmt.Fprintf(output, "open_agentic_catalog_postgresql_pool_empty_acquires_total %d\n", pool.EmptyAcquireCount)
	writeMetricHeader(output, "open_agentic_catalog_postgresql_pool_empty_acquire_seconds_total", "Cumulative wait for acquisitions from an empty PostgreSQL pool.", "counter")
	fmt.Fprintf(output, "open_agentic_catalog_postgresql_pool_empty_acquire_seconds_total %s\n", strconv.FormatFloat(pool.EmptyAcquireWaitSeconds, 'g', -1, 64))
}

// writeOperationalMetrics renders persisted aggregate counts and ages without loading individual tenant records.
func writeOperationalMetrics(output *bytes.Buffer, snapshot OperationalSnapshot) {
	writeMetricHeader(output, "open_agentic_catalog_jobs", "Persisted Jobs by bounded state.", "gauge")
	for _, item := range []struct {
		state string
		value int64
	}{{"cancelled", snapshot.Jobs.Cancelled}, {"dead_letter", snapshot.Jobs.DeadLetter}, {"failed", snapshot.Jobs.Failed}, {"queued", snapshot.Jobs.Queued}, {"running", snapshot.Jobs.Running}, {"succeeded", snapshot.Jobs.Succeeded}, {"waiting", snapshot.Jobs.Waiting}} {
		fmt.Fprintf(output, "open_agentic_catalog_jobs{state=%s} %d\n", quote(item.state), item.value)
	}
	writeMetricHeader(output, "open_agentic_catalog_jobs_by_kind", "Persisted Jobs by bounded kind.", "gauge")
	for _, item := range []struct {
		kind  string
		value int64
	}{{"other", snapshot.Jobs.Other}, {"publication_generate", snapshot.Jobs.Publication}, {"source_sync", snapshot.Jobs.SourceSync}, {"semantic_reindex", snapshot.Jobs.Semantic}} {
		fmt.Fprintf(output, "open_agentic_catalog_jobs_by_kind{kind=%s} %d\n", quote(item.kind), item.value)
	}
	writeMetricHeader(output, "open_agentic_catalog_job_oldest_runnable_age_seconds", "Age of the oldest queued, waiting, or expired-lease Job.", "gauge")
	writeMetricHeader(output, "open_agentic_catalog_semantic_pending_jobs", "Queued, running or waiting semantic indexing Jobs across tenants.", "gauge")
	fmt.Fprintf(output, "open_agentic_catalog_semantic_pending_jobs %d\n", snapshot.Jobs.SemanticPending)
	fmt.Fprintf(output, "open_agentic_catalog_job_oldest_runnable_age_seconds %d\n", snapshot.Jobs.OldestAgeSec)
	writeMetricHeader(output, "open_agentic_catalog_sources", "Configured federation sources by state.", "gauge")
	for _, item := range []struct {
		state string
		value int64
	}{{"active", snapshot.Sources.Active}, {"error", snapshot.Sources.Error}, {"paused", snapshot.Sources.Paused}, {"removed", snapshot.Sources.Removed}} {
		fmt.Fprintf(output, "open_agentic_catalog_sources{state=%s} %d\n", quote(item.state), item.value)
	}
	writeMetricHeader(output, "open_agentic_catalog_source_snapshots", "Retained source synchronization snapshots by outcome.", "gauge")
	for _, item := range []struct {
		state string
		value int64
	}{{"complete", snapshot.Sources.Complete}, {"failed", snapshot.Sources.Failed}, {"incomplete", snapshot.Sources.Incomplete}} {
		fmt.Fprintf(output, "open_agentic_catalog_source_snapshots{state=%s} %d\n", quote(item.state), item.value)
	}
	writeMetricHeader(output, "open_agentic_catalog_source_last_completed_age_seconds", "Age of the most recently completed source synchronization snapshot.", "gauge")
	fmt.Fprintf(output, "open_agentic_catalog_source_last_completed_age_seconds %d\n", snapshot.Sources.LastAgeSec)
	writeMetricHeader(output, "open_agentic_catalog_publication_snapshots", "Publication snapshots by state.", "gauge")
	for _, item := range []struct {
		state string
		value int64
	}{{"active", snapshot.Publications.Active}, {"candidate", snapshot.Publications.Candidate}, {"failed", snapshot.Publications.Failed}, {"retired", snapshot.Publications.Retired}} {
		fmt.Fprintf(output, "open_agentic_catalog_publication_snapshots{state=%s} %d\n", quote(item.state), item.value)
	}
	writeMetricHeader(output, "open_agentic_catalog_publication_oldest_active_age_seconds", "Age of the oldest active publication snapshot.", "gauge")
	fmt.Fprintf(output, "open_agentic_catalog_publication_oldest_active_age_seconds %d\n", snapshot.Publications.OldestAgeSec)
	writeMetricHeader(output, "open_agentic_catalog_outbox_events", "Outbox events by delivery state.", "gauge")
	fmt.Fprintf(output, "open_agentic_catalog_outbox_events{state=\"pending\"} %d\n", snapshot.Outbox.Pending)
	fmt.Fprintf(output, "open_agentic_catalog_outbox_events{state=\"acknowledged\"} %d\n", snapshot.Outbox.Acknowledged)
	writeMetricHeader(output, "open_agentic_catalog_outbox_oldest_pending_age_seconds", "Age of the oldest unacknowledged outbox event.", "gauge")
	fmt.Fprintf(output, "open_agentic_catalog_outbox_oldest_pending_age_seconds %d\n", snapshot.Outbox.OldestAgeSec)
}

// writeComponentMetrics emits one-hot state gauges for each sorted configured component.
func writeComponentMetrics(output *bytes.Buffer, components map[Component]ComponentState) {
	writeMetricHeader(output, "open_agentic_catalog_component_state", "Current component state using a bounded vocabulary.", "gauge")
	keys := make([]string, 0, len(components))
	for component := range components {
		keys = append(keys, string(component))
	}
	sort.Strings(keys)
	for _, key := range keys {
		current := components[Component(key)]
		for _, state := range []ComponentState{ComponentStarting, ComponentRunning, ComponentDegraded, ComponentStopped} {
			value := 0
			if current == state {
				value = 1
			}
			fmt.Fprintf(output, "open_agentic_catalog_component_state{component=%s,state=%s} %d\n", quote(key), quote(string(state)), value)
		}
	}
}

// writeDependencyMetrics renders sorted dependency readiness as numeric zero/one gauges.
func writeDependencyMetrics(output *bytes.Buffer, dependencies map[string]bool) {
	writeMetricHeader(output, "open_agentic_catalog_dependency_ready", "Whether a required or optional dependency most recently passed its bounded check.", "gauge")
	keys := make([]string, 0, len(dependencies))
	for name := range dependencies {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		value := 0
		if dependencies[name] {
			value = 1
		}
		fmt.Fprintf(output, "open_agentic_catalog_dependency_ready{dependency=%s} %d\n", quote(name), value)
	}
}

// writeMetricHeader writes HELP and TYPE declarations for code-owned metric metadata.
func writeMetricHeader(output *bytes.Buffer, name, help, kind string) {
	fmt.Fprintf(output, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
}

// quote escapes bounded label values before inserting them into metric exposition.
func quote(value string) string { return strconv.Quote(value) }

// safeMethod reduces unknown HTTP methods to the fixed OTHER label.
func safeMethod(value string) string {
	switch value {
	case "GET", "HEAD", "POST", "PATCH", "DELETE", "OPTIONS":
		return value
	default:
		return "OTHER"
	}
}

// safeRoute accepts bounded registered route text; callers must pass router patterns rather than request paths.
func safeRoute(value string) string {
	if _, route, ok := strings.Cut(value, " "); ok {
		value = route
	}
	if value == "" || len(value) > 180 || strings.ContainsAny(value, "\r\n\t\"") || !strings.HasPrefix(value, "/") {
		return "unmatched"
	}
	return value
}

// statusClass maps status codes to a bounded class, treating an unset or invalid code as implicit success.
func statusClass(status int) string {
	if status < 100 || status > 599 {
		status = 200
	}
	return strconv.Itoa(status/100) + "xx"
}

// JobKindLabel maps implemented job kinds to fixed metric labels and groups everything else as other.
func JobKindLabel(value string) string {
	switch value {
	case "source.trust_verify":
		return "source_trust_verify"
	case "source.sync":
		return "source_sync"
	case "publication.generate":
		return "publication_generate"
	case "semantic.reindex":
		return "semantic_reindex"
	default:
		return "other"
	}
}

// JobOutcomeLabel retains the fixed execution-outcome vocabulary and treats unknown outcomes as failed.
func JobOutcomeLabel(value string) string {
	switch value {
	case "succeeded", "failed", "cancelled", "waiting", "dead_letter", "unsupported", "dependency_error":
		return value
	default:
		return "failed"
	}
}

// safeDatabaseOperation reduces statement classes to the fixed lowercase vocabulary without retaining SQL text.
func safeDatabaseOperation(value string) string {
	switch strings.ToLower(value) {
	case "select", "insert", "update", "delete", "with", "begin", "commit", "rollback", "copy", "other":
		return strings.ToLower(value)
	default:
		return "other"
	}
}

// validLifecycle admits only the process lifecycle states represented by health and metric contracts.
func validLifecycle(value Lifecycle) bool {
	return value == LifecycleStarting || value == LifecycleRunning || value == LifecycleDraining || value == LifecycleStopped
}

// validComponent restricts component series to the fixed API, worker and maintenance vocabulary.
func validComponent(value Component) bool {
	return value == ComponentAPI || value == ComponentWorker || value == ComponentOutbox || value == ComponentSourceMaintenance || value == ComponentSemanticMaintenance || value == ComponentHousekeeping || value == ComponentOperations || value == ComponentWebhookDelivery
}

// validHousekeepingClass restricts cleanup labels to the seven implemented retention classes.
func validHousekeepingClass(value housekeeping.Class) bool {
	switch value {
	case housekeeping.ClassIdempotency, housekeeping.ClassSourcePreview, housekeeping.ClassSourceInspection, housekeeping.ClassSourceState, housekeeping.ClassTerminalJob, housekeeping.ClassWebhookDelivery, housekeeping.ClassOutbox:
		return true
	default:
		return false
	}
}

// validComponentState admits only starting, running, degraded and stopped component states.
func validComponentState(value ComponentState) bool {
	return value == ComponentStarting || value == ComponentRunning || value == ComponentDegraded || value == ComponentStopped
}

// validOperation limits workflow counters to the explicitly instrumented service operations.
func validOperation(value Operation) bool {
	switch value {
	case OperationCatalogSearch, OperationObservationSearch, OperationLiveSearch, OperationPublicationBuild, OperationPublicationChange, OperationSourceSync, OperationValidation, OperationOutboxProcessing, OperationReadiness:
		return true
	default:
		return false
	}
}

// validOutcome restricts operation results to the fixed success, partial, rejection, failure and idle vocabulary.
func validOutcome(value Outcome) bool {
	return value == OutcomeSucceeded || value == OutcomePartial || value == OutcomeRejected || value == OutcomeFailed || value == OutcomeIdle
}

// cloneComponents detaches component state so health responses do not expose the registry's mutable map.
func cloneComponents(input map[Component]ComponentState) map[Component]ComponentState {
	output := make(map[Component]ComponentState, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// cloneDependencies copies dependency readiness for reporting after the registry lock is released.
func cloneDependencies(input map[string]bool) map[string]bool {
	output := make(map[string]bool, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// cloneHTTP copies pointed-to counters and fixed bucket arrays while the caller holds the registry lock.
func cloneHTTP(input map[httpKey]*httpMetric) map[httpKey]httpMetric {
	output := make(map[httpKey]httpMetric, len(input))
	for key, value := range input {
		output[key] = *value
	}
	return output
}

// cloneOperations detaches workflow counters before formatting them outside the registry lock.
func cloneOperations(input map[operationKey]uint64) map[operationKey]uint64 {
	output := make(map[operationKey]uint64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// cloneActiveJobs copies in-process execution gauges independently of completed-attempt counts.
func cloneActiveJobs(input map[string]int64) map[string]int64 {
	output := make(map[string]int64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// cloneJobs copies completed job-attempt counters for rendering without retaining the mutable registry map.
func cloneJobs(input map[jobKey]uint64) map[jobKey]uint64 {
	output := make(map[jobKey]uint64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// cloneDatabase detaches statement-class count aggregates for a consistent scrape snapshot.
func cloneDatabase(input map[databaseKey]uint64) map[databaseKey]uint64 {
	output := make(map[databaseKey]uint64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// cloneDatabaseSeconds copies cumulative statement-class durations alongside the detached operation counts.
func cloneDatabaseSeconds(input map[databaseKey]float64) map[databaseKey]float64 {
	output := make(map[databaseKey]float64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// cloneHousekeeping copies every cleanup map, including value-owned duration arrays, before rendering outside the lock.
func cloneHousekeeping(input housekeepingMetric) housekeepingMetric {
	output := housekeepingMetric{
		batches: make(map[housekeepingKey]uint64, len(input.batches)), durations: make(map[housekeepingKey]durationMetric, len(input.durations)),
		reclaimed: make(map[housekeeping.Class]uint64, len(input.reclaimed)), marked: make(map[housekeeping.Class]uint64, len(input.marked)),
		saturated: make(map[housekeeping.Class]uint64, len(input.saturated)), oldest: make(map[housekeeping.Class]float64, len(input.oldest)),
		lastSuccess: make(map[housekeeping.Class]float64, len(input.lastSuccess)), cycles: make(map[string]uint64, len(input.cycles)),
		cycleTimes: make(map[string]durationMetric, len(input.cycleTimes)),
	}
	for key, value := range input.batches {
		output.batches[key] = value
	}
	for key, value := range input.durations {
		output.durations[key] = value
	}
	for key, value := range input.reclaimed {
		output.reclaimed[key] = value
	}
	for key, value := range input.marked {
		output.marked[key] = value
	}
	for key, value := range input.saturated {
		output.saturated[key] = value
	}
	for key, value := range input.oldest {
		output.oldest[key] = value
	}
	for key, value := range input.lastSuccess {
		output.lastSuccess[key] = value
	}
	for key, value := range input.cycles {
		output.cycles[key] = value
	}
	for key, value := range input.cycleTimes {
		output.cycleTimes[key] = value
	}
	return output
}
