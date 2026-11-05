package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

func TestMemoryClaimAuditRequiresLiveTenantClaim(t *testing.T) {
	for _, scenario := range []string{"live", "expired", "other-owner", "other-tenant", "missing", "terminal", "empty-owner", "nil-clock", "invalid-record", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
			expires := now.Add(time.Minute)
			repo := NewMemoryRepository()
			record := newAuditInvestigationRecord(t, "00000000-0000-7000-8000-000000000101", "worker", "resource.created", "resource", "00000000-0000-7000-8000-000000000201", "claim-test", now)
			event := OutboxEvent{ID: "event", TenantID: record.TenantID, LeaseOwner: "owner", LeaseExpiresAt: &expires}
			owner := "owner"
			var serviceClock Clock = clock.NewFake(now)
			ctx := context.Background()
			switch scenario {
			case "expired":
				serviceClock = clock.NewFake(expires)
			case "other-owner":
				event.LeaseOwner = "replacement"
			case "other-tenant":
				event.TenantID = "other-tenant"
			case "missing":
				event.ID = "other-event"
			case "terminal":
				event.PublishedAt = &now
			case "empty-owner":
				owner = ""
			case "nil-clock":
				serviceClock = nil
			case "invalid-record":
				record.Action = "invalid"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			repo.outbox = []OutboxEvent{event}
			err := repo.AppendClaimedOutboxAudit(ctx, record.TenantID, "event", owner, serviceClock, record)
			if scenario == "live" {
				if err != nil || len(repo.audit) != 1 {
					t.Fatalf("live audit: %v %#v", err, repo.audit)
				}
			} else if err == nil || len(repo.audit) != 0 {
				t.Fatalf("invalid claim retained evidence: %v %#v", err, repo.audit)
			}
		})
	}
}

type claimAuditClock func() time.Time

func (c claimAuditClock) Now() time.Time { return c() }

func TestMemoryClaimAuditReadsClockInsideEvidenceLock(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	repo := NewMemoryRepository()
	record := newAuditInvestigationRecord(t, "00000000-0000-7000-8000-000000000101", "worker", "resource.created", "resource", "00000000-0000-7000-8000-000000000201", "claim-test", now)
	repo.outbox = []OutboxEvent{{ID: "event", TenantID: record.TenantID, LeaseOwner: "owner", LeaseExpiresAt: &expires}}
	called := false
	serviceClock := claimAuditClock(func() time.Time {
		called = true
		if repo.mu.TryLock() {
			repo.mu.Unlock()
			t.Error("claim clock was sampled before acquiring the evidence lock")
		}
		return expires
	})
	if err := repo.AppendClaimedOutboxAudit(context.Background(), record.TenantID, "event", "owner", serviceClock, record); ErrorCodeOf(err) != CodeConflict || !called || len(repo.audit) != 0 {
		t.Fatalf("expiry at the protected decision: %v, sampled=%t, audit count=%d", err, called, len(repo.audit))
	}
}
