package jobs

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestConfiguredJobBudgetPersistsAcrossServiceReplacement(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(now)
	store := NewMemoryStore()
	for _, limit := range []int{-1, 0, 21} {
		if _, err := NewService(store, fake, id.Generator{}, WithMaxAttempts(limit)); err == nil {
			t.Fatalf("accepted limit %d", limit)
		}
	}
	original, err := NewService(store, fake, id.Generator{}, WithMaxAttempts(7))
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}})
	input := EnqueueInput{Kind: "source.sync", Payload: json.RawMessage(`{}`), DeduplicationKey: "retained"}
	first, duplicate, err := original.Enqueue(ctx, input)
	if err != nil || duplicate || first.AttemptLimit != 7 {
		t.Fatalf("configured creation: %#v %t %v", first, duplicate, err)
	}
	replacement, err := NewService(store, fake, id.Generator{}, WithMaxAttempts(1))
	if err != nil {
		t.Fatal(err)
	}
	retained, duplicate, err := replacement.Enqueue(ctx, input)
	if err != nil || !duplicate || retained.ID != first.ID || retained.AttemptLimit != 7 || !retained.NotBefore.Equal(first.NotBefore) {
		t.Fatalf("deduplication rewrote policy: %#v %t %v", retained, duplicate, err)
	}
	if _, err := store.Claim(ctx, "failed-worker", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fail(ctx, first.TenantID, first.ID, "failed-worker", now, FailurePermanentUpstream, "Permanent failure.", time.Time{}); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Get(ctx, first.TenantID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tenant string
		role         identity.Role
		code         catalog.ErrorCode
	}{
		{"lost management role", first.TenantID, identity.RoleReader, catalog.CodeForbidden},
		{"another tenant", "00000000-0000-7000-8000-000000000002", identity.RoleTenantAdmin, catalog.CodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unauthorized := identityfixture.WithActor(ctx, identity.Actor{TenantID: tc.tenant, Subject: "admin", Roles: []identity.Role{tc.role}})
			if _, err := replacement.Retry(unauthorized, first.ID, "Retry with changed authority"); catalog.ErrorCodeOf(err) != tc.code {
				t.Fatalf("retry authority: %v, want %s", err, tc.code)
			}
			if _, err := replacement.Cancel(unauthorized, first.ID, "Cancel with changed authority"); catalog.ErrorCodeOf(err) != tc.code {
				t.Fatalf("cancel authority: %v, want %s", err, tc.code)
			}
			rows, err := store.List(ctx, first.TenantID, ListRequest{Limit: 10})
			if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], failed) {
				t.Fatalf("denied command changed retained state: %#v %v", rows, err)
			}
		})
	}
	child, err := replacement.Retry(ctx, first.ID, "Retry after configuration change")
	if err != nil || child.ParentJobID != first.ID || child.AttemptLimit != 7 {
		t.Fatalf("retry lost parent policy: %#v %v", child, err)
	}
	input.DeduplicationKey = "new"
	fresh, _, err := replacement.Enqueue(ctx, input)
	if err != nil || fresh.AttemptLimit != 1 {
		t.Fatalf("new default lost: %#v %v", fresh, err)
	}
	input.DeduplicationKey = "explicit"
	input.AttemptLimit = 20
	explicit, _, err := replacement.Enqueue(ctx, input)
	if err != nil || explicit.AttemptLimit != 20 {
		t.Fatalf("explicit budget lost: %#v %v", explicit, err)
	}
}

func TestWorkerReplacementKeepsDueTimeAndUsesNewCeilingOnFailure(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(now)
	store := NewMemoryStore()
	calls := 0
	job := Job{ID: "replacement", TenantID: "tenant", Kind: "source.sync", State: StateQueued, Payload: json.RawMessage(`{}`), AttemptLimit: 3, NotBefore: now, CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.Enqueue(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	handlers := map[string]Handler{"source.sync": func(context.Context, Job, func(int) error) (any, error) {
		calls++
		return nil, &HandlerError{Class: FailureRateLimited, Safe: "Wait for source.", RetryAfter: time.Minute}
	}}
	for _, ceiling := range []time.Duration{0, -time.Second, 5*time.Second - time.Nanosecond, time.Hour + time.Nanosecond} {
		if _, err := NewWorker(store, fake, "invalid", time.Minute, time.Second, ceiling, handlers); err == nil {
			t.Fatalf("accepted ceiling %s", ceiling)
		}
	}
	original, err := NewWorker(store, fake, "original", time.Minute, time.Second, time.Hour, handlers)
	if err != nil {
		t.Fatal(err)
	}
	if err := original.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	waiting, err := store.Get(t.Context(), job.TenantID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := NewWorker(store, fake, "replacement", time.Minute, time.Second, 5500*time.Millisecond, handlers)
	if err != nil {
		t.Fatal(err)
	}
	fake.Advance(waiting.NotBefore.Sub(fake.Now()) - time.Nanosecond)
	if err := replacement.RunOnce(t.Context()); err != nil || calls != 1 {
		t.Fatalf("early claim: calls=%d err=%v", calls, err)
	}
	unchanged, err := store.Get(t.Context(), job.TenantID, job.ID)
	if err != nil || !unchanged.NotBefore.Equal(waiting.NotBefore) || unchanged.AttemptLimit != 3 || unchanged.Attempt != 1 {
		t.Fatalf("replacement changed retained policy: %#v %v", unchanged, err)
	}
	fake.Advance(time.Nanosecond)
	if err := replacement.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	next, err := store.Get(t.Context(), job.TenantID, job.ID)
	if err != nil || calls != 2 || next.State != StateWaiting || next.Attempt != 2 || next.AttemptLimit != 3 || next.NotBefore.Before(fake.Now().Add(time.Minute)) || next.NotBefore.After(fake.Now().Add(time.Minute+5500*time.Millisecond)) {
		t.Fatalf("replacement schedule: %#v calls=%d err=%v", next, calls, err)
	}
	fake.Advance(next.NotBefore.Sub(fake.Now()))
	if err := replacement.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	exhausted, err := store.Get(t.Context(), job.TenantID, job.ID)
	if err != nil || calls != 3 || exhausted.State != StateDeadLetter || exhausted.Attempt != 3 || !exhausted.NotBefore.Equal(next.NotBefore) {
		t.Fatalf("exhaustion changed: %#v calls=%d err=%v", exhausted, calls, err)
	}
}
