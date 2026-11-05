package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestJobOverviewUsesCurrentFamilyTargetsAndDueTimes(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	service := &Service{store: store, clock: clock.NewFake(at), targetValidation: func(_ context.Context, job Job) error {
		if job.ID == "stale" {
			return catalog.E(catalog.CodeNotFound, "missing target")
		}
		return nil
	}}
	for i, state := range []State{StateQueued, StateWaiting, StateRunning, StateSucceeded, StateFailed, StateCancelled, StateDeadLetter} {
		id := fmt.Sprint(i)
		lease := at.Add(-time.Minute)
		store.jobs[id] = Job{ID: id, TenantID: "tenant", Kind: "source.sync", State: state, NotBefore: at, LeaseExpiresAt: &lease, CreatedAt: at.Add(-365 * 24 * time.Hour)}
	}
	for id, job := range map[string]Job{
		"future":  {Kind: "source.sync", State: StateWaiting, NotBefore: at.Add(time.Hour)},
		"stale":   {Kind: "source.sync", State: StateQueued},
		"private": {Kind: "publication.generate", State: StateQueued},
		"unknown": {Kind: "future.kind", State: StateQueued},
		"other":   {TenantID: "other", Kind: "source.sync", State: StateQueued},
	} {
		job.ID = id
		if job.TenantID == "" {
			job.TenantID = "tenant"
		}
		store.jobs[id] = job
	}
	out, err := service.Overview(ctx)
	if err != nil || out.Total.Value != 8 || out.Due.Value != 3 || out.OldestDueAt == nil || !out.OldestDueAt.Equal(at.Add(-time.Minute)) || len(out.Kinds) != 2 || out.Kinds[0].Kind != "source.sync" {
		t.Fatal(out, err)
	}
	for _, row := range out.Kinds[0].States {
		want := 1
		if row.State == StateWaiting {
			want = 2
		}
		if row.Count != want {
			t.Fatal(row)
		}
	}
	store.mu.Lock()
	delete(store.jobs, "2")
	store.mu.Unlock()
	out, err = service.Overview(ctx)
	if err != nil || out.Due.Value != 2 || !out.OldestDueAt.Equal(at) {
		t.Fatal(out, err)
	}
	reader := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	out, err = service.Overview(reader)
	if err != nil || out.Total.Value != 0 || out.Due.Value != 0 || out.OldestDueAt != nil || len(out.Kinds) != 0 {
		t.Fatal(out, err)
	}
	if _, err = service.Overview(context.Background()); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal(err)
	}
}

func TestJobOverviewIndependentCeilingsAndOldest(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	service := &Service{store: store, clock: clock.NewFake(at)}
	for i := range catalog.OverviewSelectionLimit {
		key := fmt.Sprint(i)
		store.jobs[key] = Job{ID: key, TenantID: "tenant", Kind: "source.sync", State: StateQueued, NotBefore: at}
	}
	out, err := service.Overview(ctx)
	if err != nil || out.Total.Limited || out.Due.Limited || out.Total.Value != 10000 || len(out.Kinds) != 2 {
		t.Fatal(out, err)
	}
	store.jobs["extra"] = Job{ID: "extra", TenantID: "tenant", Kind: "source.sync", State: StateWaiting, NotBefore: at.Add(time.Hour)}
	out, err = service.Overview(ctx)
	if err != nil || !out.Total.Limited || out.Kinds != nil || out.Due.Limited || out.Due.Value != 10000 || out.OldestDueAt == nil {
		t.Fatal(out, err)
	}
	extra := store.jobs["extra"]
	extra.NotBefore = at.Add(-time.Hour)
	store.jobs["extra"] = extra
	out, err = service.Overview(ctx)
	if err != nil || !out.Due.Limited || out.Due.Value != 10000 || !out.OldestDueAt.Equal(extra.NotBefore) {
		t.Fatal(out, err)
	}
}
