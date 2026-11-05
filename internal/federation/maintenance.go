// Maintenance schedules durable source synchronization and freshness work using
// fixed bounded selection and worker-owned execution.

package federation

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

const (
	maintenanceBatchLimit = 32
	maintenanceLiveLimit  = 64
)

type MaintenanceCandidate struct {
	TenantID       string
	SourceID       string
	SourceRevision int64
	DueAt          time.Time
}

// MaintenanceStore combines due-source selection with atomic Job admission so
// concurrent maintainers cannot enqueue duplicate automatic work.
type MaintenanceStore interface {
	ListDueAutomaticSources(context.Context, time.Time, time.Duration, string, int) ([]MaintenanceCandidate, error)
	AdmitAutomaticSync(context.Context, MaintenanceCandidate, jobs.Job, time.Duration, string, int) (jobs.Job, bool, error)
}

// Maintainer periodically admits bounded automatic synchronization jobs; it
// performs no source retrieval itself.
type Maintainer struct {
	store       MaintenanceStore
	clock       Clock
	ids         IDGenerator
	interval    time.Duration
	policy      string
	maxAttempts int
}

// NewMaintainer validates the maintenance store, clock, IDs, cadence and policy
// binding.
func NewMaintainer(store MaintenanceStore, clock Clock, ids IDGenerator, interval time.Duration, policyDigest string, maxAttempts int) (*Maintainer, error) {
	if store == nil || clock == nil || ids == nil || interval < time.Minute || interval > 24*time.Hour || !validPolicyDigest(policyDigest) || maxAttempts < 1 || maxAttempts > 20 {
		return nil, errors.New("automatic source maintainer configuration is invalid")
	}
	return &Maintainer{store: store, clock: clock, ids: ids, interval: interval, policy: policyDigest, maxAttempts: maxAttempts}, nil
}

// Run performs source maintenance at the configured cadence until cancellation
// or an operation error.
func (m *Maintainer) Run(ctx context.Context) error {
	if err := m.Scan(ctx); err != nil && catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		return err
	}
	rescan := m.interval / 10
	if rescan > time.Minute {
		rescan = time.Minute
	}
	if rescan < time.Second {
		rescan = time.Second
	}
	ticker := time.NewTicker(rescan)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := m.Scan(ctx); err != nil && catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				return err
			}
		}
	}
}

// Scan considers one bounded due batch and tolerates candidates invalidated by
// concurrent source changes while propagating real dependency failures.
func (m *Maintainer) Scan(ctx context.Context) error {
	now := m.clock.Now()
	candidates, err := m.store.ListDueAutomaticSources(ctx, now, m.interval, m.policy, maintenanceBatchLimit)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		jobID, err := m.ids.New(now.Add(time.Duration(candidate.SourceRevision) * time.Nanosecond))
		if err != nil {
			return err
		}
		payload, err := json.Marshal(struct {
			SourceID       string `json:"sourceId"`
			SourceRevision int64  `json:"sourceRevision"`
			Trigger        string `json:"trigger"`
		}{candidate.SourceID, candidate.SourceRevision, "automatic"})
		if err != nil {
			return err
		}
		job := jobs.Job{ID: jobID, TenantID: candidate.TenantID, Kind: "source.sync", Actor: audit.InitiatorSourceMaintainer, PayloadVersion: 2, Payload: payload, State: jobs.StateQueued, NotBefore: now, AttemptLimit: m.maxAttempts, DeduplicationKey: sourceSyncDeduplicationKey(candidate.SourceID, candidate.SourceRevision), CreatedAt: now, UpdatedAt: now}
		if _, _, err := m.store.AdmitAutomaticSync(ctx, candidate, job, m.interval, m.policy, maintenanceLiveLimit); err != nil {
			if catalog.ErrorCodeOf(err) == catalog.CodePreconditionFailed || catalog.ErrorCodeOf(err) == catalog.CodeNotFound || catalog.ErrorCodeOf(err) == catalog.CodeConflict {
				continue
			}
			return err
		}
	}
	return nil
}

// sourceSyncDeduplicationKey binds live synchronization work to the exact
// source ID and configuration revision.
func sourceSyncDeduplicationKey(sourceID string, revision int64) string {
	return "source.sync:" + sourceID + ":" + strconv.FormatInt(revision, 10)
}

// maintenanceJitter deterministically spreads a source revision across ten
// percent of its interval without storing scheduler state.
func maintenanceJitter(sourceID string, revision int64, interval time.Duration) time.Duration {
	window := interval / 10
	if window <= 0 {
		return 0
	}
	digest := sha256.Sum256([]byte("source-maintenance-jitter-v1\x00" + sourceID + "\x00" + strconv.FormatInt(revision, 10)))
	value := int64(binary.BigEndian.Uint32(digest[:4]))
	return time.Duration(value % window.Nanoseconds())
}

// AutomaticSourcePolicy fences an exact tenant admission against policy changes.
type AutomaticSourcePolicy interface {
	WithAutomaticSourcePolicy(context.Context, string, time.Duration, int, func(bool, time.Duration, int) error) error
}

type memoryMaintenanceStore struct {
	settings AutomaticSourcePolicy
	sources  *MemoryStore
	jobs     jobs.Store
	mu       sync.Mutex
	last     map[string]time.Time
}

// NewMemoryMaintenanceStore connects in-memory source selection to the Job
// store for development scheduling.
func NewMemoryMaintenanceStore(sources *MemoryStore, jobStore jobs.Store, policies ...AutomaticSourcePolicy) (MaintenanceStore, error) {
	if sources == nil || jobStore == nil {
		return nil, errors.New("memory automatic-maintenance dependencies are required")
	}
	if len(policies) > 1 || len(policies) == 1 && policies[0] == nil {
		return nil, errors.New("one nonnil automatic source policy is required")
	}
	store := &memoryMaintenanceStore{sources: sources, jobs: jobStore, last: make(map[string]time.Time)}
	if len(policies) == 1 {
		store.settings = policies[0]
	}
	return store, nil
}

// ListDueAutomaticSources selects bounded active sources whose automatic
// synchronization is due under the current policy.
func (m *memoryMaintenanceStore) ListDueAutomaticSources(ctx context.Context, now time.Time, interval time.Duration, policy string, limit int) ([]MaintenanceCandidate, error) {
	if limit < 1 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "automatic source selection limit must be positive")
	}
	// Snapshot selection inputs before policy reads. A waiting settings writer
	// must not invert the admission order (settings, maintenance, sources, Jobs).
	m.mu.Lock()
	m.sources.mu.RLock()
	type selected struct {
		source              Source
		completed, admitted time.Time
	}
	snapshot := make([]selected, 0, len(m.sources.sources))
	for _, source := range m.sources.sources {
		item := selected{source: source, admitted: m.last[source.TenantID+"/"+source.ID+"/"+strconv.FormatInt(source.Revision, 10)]}
		if saved, ok := m.sources.snapshots[source.LastCompleteSnapshotID]; ok && saved.CompletedAt != nil {
			item.completed = *saved.CompletedAt
		}
		snapshot = append(snapshot, item)
	}
	m.sources.mu.RUnlock()
	m.mu.Unlock()
	items := make([]MaintenanceCandidate, 0, limit)
	for _, item := range snapshot {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source := item.source
		if !automaticSourceEligible(source, policy) {
			continue
		}
		err := m.withPolicy(ctx, source.TenantID, interval, 5, func(enabled bool, effective time.Duration, _ int) error {
			if !enabled {
				return nil
			}
			due := memoryAutomaticDue(source, item.completed, item.admitted, effective)
			if !due.After(now) {
				items = append(items, MaintenanceCandidate{TenantID: source.TenantID, SourceID: source.ID, SourceRevision: source.Revision, DueAt: due})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Slice(items, func(i, j int) bool { return maintenanceCandidateLess(items[i], items[j]) })
		if len(items) > limit {
			items = items[:limit]
		}
	}
	return items, nil
}

// maintenanceCandidateLess orders due candidates deterministically so repeated
// sweeps advance fairly.
func maintenanceCandidateLess(left, right MaintenanceCandidate) bool {
	if !left.DueAt.Equal(right.DueAt) {
		return left.DueAt.Before(right.DueAt)
	}
	if left.TenantID != right.TenantID {
		return left.TenantID < right.TenantID
	}
	return left.SourceID < right.SourceID
}

// AdmitAutomaticSync rechecks the selected source revision and scheduling
// constraints before deduplicating automatic Job admission.
func (m *memoryMaintenanceStore) AdmitAutomaticSync(ctx context.Context, candidate MaintenanceCandidate, job jobs.Job, interval time.Duration, policy string, maxLive int) (jobs.Job, bool, error) {
	var result jobs.Job
	var existing bool
	err := m.withPolicy(ctx, candidate.TenantID, interval, job.AttemptLimit, func(enabled bool, effective time.Duration, attempts int) error {
		if !enabled {
			return catalog.E(catalog.CodePreconditionFailed, "automatic source synchronization is paused")
		}
		var err error
		job.AttemptLimit = attempts
		job.DefaultAttemptLimit = false
		result, existing, err = m.admit(ctx, candidate, job, effective, policy, maxLive)
		return err
	})
	return result, existing, err
}

func (m *memoryMaintenanceStore) withPolicy(ctx context.Context, tenant string, fallback time.Duration, attempts int, read func(bool, time.Duration, int) error) error {
	if m.settings == nil {
		return read(true, fallback, attempts)
	}
	return m.settings.WithAutomaticSourcePolicy(ctx, tenant, fallback, attempts, read)
}
func memoryAutomaticDue(source Source, completed, admitted time.Time, interval time.Duration) time.Time {
	anchor := source.UpdatedAt
	initial := true
	if !completed.IsZero() {
		anchor = completed
		initial = false
	}
	if admitted.After(anchor) {
		anchor = admitted
		initial = false
	}
	due := anchor.Add(maintenanceJitter(source.ID, source.Revision, interval))
	if !initial {
		due = due.Add(interval)
	}
	return due
}
func (m *memoryMaintenanceStore) admit(ctx context.Context, candidate MaintenanceCandidate, job jobs.Job, interval time.Duration, policy string, maxLive int) (jobs.Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sources.mu.RLock()
	source, ok := m.sources.sources[candidate.SourceID]
	defer m.sources.mu.RUnlock()
	if !ok || source.TenantID != candidate.TenantID || source.Revision != candidate.SourceRevision || !automaticSourceEligible(source, policy) {
		return jobs.Job{}, false, catalog.E(catalog.CodePreconditionFailed, "automatic source is no longer eligible")
	}
	var completed time.Time
	if snapshot, found := m.sources.snapshots[source.LastCompleteSnapshotID]; found && snapshot.CompletedAt != nil {
		completed = *snapshot.CompletedAt
	}
	key := source.TenantID + "/" + source.ID + "/" + strconv.FormatInt(source.Revision, 10)
	if memoryAutomaticDue(source, completed, m.last[key], interval).After(job.CreatedAt) {
		return jobs.Job{}, false, catalog.E(catalog.CodePreconditionFailed, "automatic source is no longer due")
	}
	listed, err := m.jobs.List(ctx, candidate.TenantID, jobs.ListRequest{Limit: maxLive + 1})
	if err != nil {
		return jobs.Job{}, false, err
	}
	live := 0
	for _, existing := range listed {
		if existing.Kind == "source.sync" && existing.PayloadVersion == 2 && (existing.State == jobs.StateQueued || existing.State == jobs.StateRunning || existing.State == jobs.StateWaiting) {
			var payload struct {
				Trigger string `json:"trigger"`
			}
			if json.Unmarshal(existing.Payload, &payload) == nil && payload.Trigger == "automatic" {
				live++
			}
		}
	}
	if live >= maxLive {
		return jobs.Job{}, false, catalog.E(catalog.CodeConflict, "automatic source synchronization capacity is full")
	}
	created, existing, err := m.jobs.Enqueue(ctx, job)
	if err != nil {
		return jobs.Job{}, false, err
	}
	m.last[candidate.TenantID+"/"+candidate.SourceID+"/"+strconv.FormatInt(candidate.SourceRevision, 10)] = job.CreatedAt
	return created, existing, nil
}

// automaticSourceEligible requires the supported active automatic-sync
// configuration and current approved policy.
func automaticSourceEligible(source Source, policy string) bool {
	return (source.Status == SourceActive || source.Status == SourceError) && source.UpdateMode == UpdateAutomatic && (source.RetrievalMode == RetrievalIndexed || source.RetrievalMode == RetrievalHybrid) && source.PolicyDigest == policy && validPolicyDigest(source.PolicyDigest) && (source.Type != SourceRegistry || source.RegistryListURL != "")
}
