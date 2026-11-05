package catalog

import (
	"context"
	"testing"
	"time"
)

func TestMemoryOutboxLeasesPreventConcurrentClaims(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	repository.outbox = append(repository.outbox, OutboxEvent{ID: "event-one", TenantID: "tenant-one", Type: "resource.created", CreatedAt: now})
	first, err := repository.ClaimOutbox(context.Background(), "worker-one", now, time.Minute, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	second, err := repository.ClaimOutbox(context.Background(), "worker-two", now, time.Minute, 10)
	if err != nil || len(second) != 0 {
		t.Fatalf("concurrent claim = %#v, %v", second, err)
	}
	if err := repository.MarkOutboxPublished(context.Background(), "tenant-one", "event-one", "worker-two", now.Add(time.Second)); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("non-owner publication returned %v", err)
	}
	if err := repository.RenewOutbox(context.Background(), "tenant-one", "event-one", "worker-two", now.Add(time.Second), time.Minute); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("non-owner renewal returned %v", err)
	}
	if err := repository.RenewOutbox(context.Background(), "tenant-two", "event-one", "worker-one", now.Add(time.Second), time.Minute); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("cross-tenant renewal returned %v", err)
	}
	if err := repository.MarkOutboxPublished(context.Background(), "tenant-two", "event-one", "worker-one", now.Add(time.Second)); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("cross-tenant publication returned %v", err)
	}
	if err := repository.ReleaseOutbox(context.Background(), "tenant-two", "event-one", "worker-one"); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("cross-tenant release returned %v", err)
	}
	if err := repository.RenewOutbox(context.Background(), "tenant-one", "event-one", "worker-one", now.Add(time.Second), 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := repository.ReleaseOutbox(context.Background(), "tenant-one", "event-one", "worker-one"); err != nil {
		t.Fatal(err)
	}
	second, err = repository.ClaimOutbox(context.Background(), "worker-two", now.Add(time.Second), time.Minute, 10)
	if err != nil || len(second) != 1 {
		t.Fatalf("claim after release = %#v, %v", second, err)
	}
	if err := repository.MarkOutboxPublished(context.Background(), "tenant-one", "event-one", "worker-two", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if items, err := repository.ClaimOutbox(context.Background(), "worker-three", now.Add(2*time.Minute), time.Minute, 10); err != nil || len(items) != 0 {
		t.Fatalf("published event was reclaimed: %#v, %v", items, err)
	}

	limited := NewMemoryRepository()
	limited.outbox = append(limited.outbox,
		OutboxEvent{ID: "event-one", CreatedAt: now},
		OutboxEvent{ID: "event-two", CreatedAt: now},
	)
	if items, err := limited.ClaimOutbox(context.Background(), "worker", now, time.Minute, 1); err != nil || len(items) != 1 {
		t.Fatalf("bounded claim = %#v, %v", items, err)
	}
}

func TestCanonicalEventTypesAreVersioned(t *testing.T) {
	tests := map[string]string{
		"resource.created":                             "com.open.agentic.catalog.resource.created.v1",
		"listing_release.published":                    "com.open.agentic.catalog.listing.release.published.v1",
		"review_submission.submitted":                  "com.open.agentic.catalog.review.submission.created.v1",
		"review_submission.approve":                    "com.open.agentic.catalog.review.decision.recorded.v1",
		"com.open.agentic.catalog.resource.created.v1": "com.open.agentic.catalog.resource.created.v1",
	}
	for action, expected := range tests {
		if actual := CanonicalEventType(action); actual != expected {
			t.Fatalf("CanonicalEventType(%q) = %q, want %q", action, actual, expected)
		}
	}
}
