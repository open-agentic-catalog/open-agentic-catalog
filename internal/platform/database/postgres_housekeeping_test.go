package database

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/housekeeping"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

func TestPostgresHousekeepingConstructionAndBounds(t *testing.T) {
	if _, err := NewPostgresHousekeepingStore(nil); err == nil {
		t.Fatal("nil PostgreSQL repository was accepted")
	}
	repository := &PostgresRepository{}
	store, err := NewPostgresHousekeepingStore(repository)
	if err != nil || store.repository != repository {
		t.Fatalf("housekeeping store = %+v, %v", store, err)
	}
	for _, limit := range []int{1, housekeeping.BatchLimit} {
		if err := validHousekeepingTiming(time.Nanosecond, limit); err != nil {
			t.Fatalf("valid retention/limit %d rejected: %v", limit, err)
		}
	}
	for _, limit := range []int{0, housekeeping.BatchLimit + 1} {
		if _, err := store.ReclaimExpiredIdempotency(t.Context(), limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("idempotency limit %d returned %v", limit, err)
		}
		if _, err := store.ReclaimExpiredSourcePreviews(t.Context(), limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("preview limit %d returned %v", limit, err)
		}
		if _, err := store.ReclaimUnusedSourceInspections(t.Context(), limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("inspection limit %d returned %v", limit, err)
		}
		if _, err := store.ReclaimObsoleteSourceCoordination(t.Context(), limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("source coordination limit %d returned %v", limit, err)
		}
		if _, err := store.ReclaimTerminalJobs(t.Context(), time.Hour, limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("Job limit %d returned %v", limit, err)
		}
		if _, err := store.ReclaimAcknowledgedOutbox(t.Context(), time.Hour, limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("outbox limit %d returned %v", limit, err)
		}
		if _, err := store.ReclaimWebhookDeliveries(t.Context(), time.Hour, limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("delivery limit %d returned %v", limit, err)
		}
	}
	if _, err := store.ReclaimTerminalJobs(t.Context(), 0, 1); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("zero Job retention returned %v", err)
	}
	if _, err := store.ReclaimAcknowledgedOutbox(t.Context(), -time.Second, 1); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("negative outbox retention returned %v", err)
	}
	if _, err := store.ReclaimWebhookDeliveries(t.Context(), 0, 1); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("zero delivery retention returned %v", err)
	}
}

func TestSourceSyncJobTargetRecognizesOnlyClosedPayloads(t *testing.T) {
	sourceID := "00000000-0000-7000-8000-000000000001"
	for _, test := range []struct {
		name     string
		job      jobs.Job
		valid    bool
		revision int64
	}{
		{name: "protocol one", job: jobs.Job{Kind: "source.sync", PayloadVersion: 1, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":2}`)}},
		{name: "protocol one manual", job: jobs.Job{Kind: "source.sync", PayloadVersion: 1, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":3,"trigger":"manual"}`)}},
		{name: "protocol two automatic", job: jobs.Job{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":4,"trigger":"automatic"}`)}, valid: true, revision: 4},
		{name: "other kind", job: jobs.Job{Kind: "other", PayloadVersion: 2, Payload: json.RawMessage(`{}`)}},
		{name: "unsupported protocol", job: jobs.Job{Kind: "source.sync", PayloadVersion: 3, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":1}`)}},
		{name: "malformed", job: jobs.Job{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{`)}},
		{name: "missing trigger", job: jobs.Job{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":1}`)}},
		{name: "wrong trigger", job: jobs.Job{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":1,"trigger":"manual-review"}`)}},
		{name: "unknown member", job: jobs.Job{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":1,"trigger":"manual","extra":true}`)}},
		{name: "missing source", job: jobs.Job{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceRevision":1,"trigger":"manual"}`)}},
		{name: "invalid source", job: jobs.Job{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"not-a-uuid","sourceRevision":1,"trigger":"manual"}`)}},
		{name: "zero revision", job: jobs.Job{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":0,"trigger":"manual"}`)}},
		{name: "fractional revision", job: jobs.Job{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceRevision":1.5,"trigger":"manual"}`)}},
		{name: "duplicate source", job: jobs.Job{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + sourceID + `","sourceId":"` + sourceID + `","sourceRevision":1,"trigger":"manual"}`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			id, revision, valid := sourceSyncJobTarget(test.job)
			if valid != test.valid || valid && (id != sourceID || revision != test.revision) {
				t.Fatalf("target = %q/%d valid=%t", id, revision, valid)
			}
		})
	}
}

func TestHousekeepingSQLAvoidsPhysicalAndUnboundedPagination(t *testing.T) {
	for name, query := range map[string]string{
		"source retrieval":   sourceRetrievalCleanupSQL,
		"source maintenance": sourceMaintenanceCleanupSQL,
	} {
		lower := strings.ToLower(query)
		if strings.Contains(lower, "ctid") || strings.Contains(lower, "offset") || !strings.Contains(lower, "limit $1") || !strings.Contains(lower, "skip locked") {
			t.Fatalf("%s cleanup query is not logically bounded", name)
		}
	}
}

func TestBoundedHousekeepingAge(t *testing.T) {
	maximum := time.Duration(1<<63 - 1)
	for _, test := range []struct {
		seconds float64
		want    time.Duration
	}{
		{seconds: -1},
		{seconds: 1.5, want: 1500 * time.Millisecond},
		{seconds: maximum.Seconds() * 2, want: maximum},
	} {
		if got := boundedHousekeepingAge(test.seconds); got != test.want {
			t.Fatalf("age for %g seconds = %s, want %s", test.seconds, got, test.want)
		}
	}
}
