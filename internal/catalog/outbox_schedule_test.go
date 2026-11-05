package catalog

import (
	"context"
	"testing"
	"time"
)

func TestMemoryOutboxDeferralOwnershipAndDueOrder(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	repo := NewMemoryRepository()
	repo.outbox = []OutboxEvent{{ID: "b", TenantID: "a", CreatedAt: now}, {ID: "a", TenantID: "b", CreatedAt: now}}
	items, err := repo.ClaimOutbox(ctx, "worker", now, time.Minute, 1)
	if err != nil || len(items) != 1 || items[0].ID != "a" {
		t.Fatalf("claim order: %v %v", items, err)
	}
	for _, input := range []struct{ tenant, id, owner string }{{"a", "a", "worker"}, {"b", "missing", "worker"}, {"b", "a", "other"}} {
		if err := repo.DeferOutbox(ctx, input.tenant, input.id, input.owner, now, now.Add(time.Second)); ErrorCodeOf(err) != CodeConflict {
			t.Fatalf("invalid ownership accepted: %v", err)
		}
	}
	for _, until := range []time.Time{now, now.Add(-time.Second), now.Add(time.Hour + time.Nanosecond)} {
		if err := repo.DeferOutbox(ctx, "b", "a", "worker", now, until); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid deferral accepted: %v", err)
		}
	}
	if err := repo.DeferOutbox(ctx, "b", "a", "worker", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	items, err = repo.ClaimOutbox(ctx, "worker", now, time.Minute, 1)
	if err != nil || len(items) != 1 || items[0].ID != "b" {
		t.Fatal("deferred event starved another tenant")
	}
	if err := repo.DeferOutbox(ctx, "a", "b", "worker", now.Add(time.Minute), now.Add(2*time.Minute)); ErrorCodeOf(err) != CodeConflict {
		t.Fatal("expired lease deferred an event")
	}
	if err := repo.MarkOutboxPublished(ctx, "a", "b", "worker", now); err != nil {
		t.Fatal(err)
	}
	items, err = repo.ClaimOutbox(ctx, "restarted", now.Add(time.Hour), time.Minute, 1)
	if err != nil || len(items) != 1 || items[0].ID != "a" {
		t.Fatal("due event did not recover after restart")
	}
}
