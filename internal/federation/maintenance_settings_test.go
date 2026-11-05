package federation

import (
	"context"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"testing"
	"time"
)

type maintenancePolicy struct {
	enabled  bool
	interval time.Duration
	attempts int
	err      error
}

func (p *maintenancePolicy) WithAutomaticSourcePolicy(_ context.Context, _ string, _ time.Duration, _ int, read func(bool, time.Duration, int) error) error {
	if p.err != nil {
		return p.err
	}
	return read(p.enabled, p.interval, p.attempts)
}
func TestAutomaticAdmissionRechecksPausedOrPostponedSettings(t *testing.T) {
	now := time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC)
	completed := now.Add(-2 * time.Minute)
	sources := NewMemoryStore()
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: "00000000-0000-7000-8000-000000000001", Revision: 1, Status: SourceActive, Type: SourceARDManifest, RetrievalMode: RetrievalIndexed, UpdateMode: UpdateAutomatic, PolicyDigest: federationTestPolicyDigest, UpdatedAt: now.Add(-time.Hour), LastCompleteSnapshotID: "snapshot"}
	sources.sources[source.ID] = source
	sources.snapshots["snapshot"] = Snapshot{CompletedAt: &completed}
	work := jobs.NewMemoryStore()
	policy := &maintenancePolicy{enabled: true, interval: time.Minute, attempts: 2}
	store, err := NewMemoryMaintenanceStore(sources, work, policy)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ListDueAutomaticSources(t.Context(), now, time.Minute, federationTestPolicyDigest, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatal(candidates, err)
	}
	job := jobs.Job{ID: "00000000-0000-7000-8000-000000000011", TenantID: source.TenantID, Kind: "source.sync", State: jobs.StateQueued, AttemptLimit: 5, CreatedAt: now, UpdatedAt: now, NotBefore: now, DeduplicationKey: "automatic"}
	policy.enabled = false
	if selected, err := store.ListDueAutomaticSources(t.Context(), now, time.Minute, federationTestPolicyDigest, 1); err != nil || len(selected) != 0 {
		t.Fatal(selected, err)
	}
	if _, _, err = store.AdmitAutomaticSync(t.Context(), candidates[0], job, time.Minute, federationTestPolicyDigest, 32); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("paused admission", err)
	}
	policy.enabled = true
	policy.interval = time.Hour
	if _, _, err = store.AdmitAutomaticSync(t.Context(), candidates[0], job, time.Minute, federationTestPolicyDigest, 32); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("postponed admission", err)
	}
	policy.interval = time.Minute
	admitted, reused, err := store.AdmitAutomaticSync(t.Context(), candidates[0], job, time.Minute, federationTestPolicyDigest, 32)
	if err != nil || reused || admitted.AttemptLimit != 2 {
		t.Fatal(admitted, reused, err)
	}
	policy.enabled = false
	policy.attempts = 9
	retained, err := work.Get(t.Context(), source.TenantID, job.ID)
	if err != nil || retained.AttemptLimit != 2 {
		t.Fatal("policy rewrote admitted work", retained, err)
	}
	policy.err = errors.New("policy unavailable")
	if _, err = store.ListDueAutomaticSources(t.Context(), now, time.Minute, federationTestPolicyDigest, 1); !errors.Is(err, policy.err) {
		t.Fatal("read failure inherited", err)
	}
	if _, _, err = store.AdmitAutomaticSync(t.Context(), candidates[0], job, time.Minute, federationTestPolicyDigest, 32); !errors.Is(err, policy.err) {
		t.Fatal("admission failure inherited", err)
	}
}
