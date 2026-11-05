package webhook

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

type panicSelectionStore struct{ Store }

func (s panicSelectionStore) ListPendingEndpoints(context.Context, catalog.OutboxEvent, int) ([]Endpoint, error) {
	panic("private-dispatch-panic")
}

type panicAckOutbox struct{ Outbox }

func (o panicAckOutbox) MarkOutboxPublished(context.Context, string, string, string, time.Time) error {
	panic("private-ack-panic")
}

func TestDispatchPanicReportsFailureWithoutAcknowledging(t *testing.T) {
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	event := catalog.OutboxEvent{ID: "event", TenantID: "tenant", Type: catalog.CanonicalEventType("resource.created"), CreatedAt: now}
	for _, mode := range []string{"selection", "acknowledgement", "disabled drainer"} {
		t.Run(mode, func(t *testing.T) {
			outbox := &memoryOutbox{events: []catalog.OutboxEvent{event}, published: map[string]time.Time{}}
			var port Outbox = outbox
			var store Store = NewMemoryStore()
			if mode == "selection" {
				store = panicSelectionStore{store}
			} else {
				port = panicAckOutbox{port}
			}
			observer := &recordingOutboxObserver{}
			var outcomes []string
			ctx := catalog.WithTracer(t.Context(), dispatchTrace{start: func(catalog.TraceContext) {}, end: func(value string) { outcomes = append(outcomes, value) }})
			var count int
			var err error
			if mode == "disabled drainer" {
				drainer, buildErr := NewDrainer(port, clock.NewFake(now), "worker", time.Minute, time.Second, observer)
				if buildErr != nil {
					t.Fatal(buildErr)
				}
				count, err = drainer.DrainOnce(ctx)
			} else {
				cipher, _ := NewAESGCM(keyBytes(5))
				dispatcher, buildErr := newTestDispatcher(port, store, cipher, &recordingSender{}, clock.NewFake(now), &sequenceIDs{}, "worker", time.Minute, 3, time.Second, time.Hour, observer)
				if buildErr != nil {
					t.Fatal(buildErr)
				}
				count, err = dispatcher.DispatchOnce(ctx)
				if len(outcomes) != 1 || outcomes[0] != "error" {
					t.Fatalf("panic trace: %v", outcomes)
				}
			}
			if !errors.Is(err, safeerr.ErrPanic) || count != 0 || len(outbox.published) != 0 || len(observer.batches) != 1 || !observer.batches[0].failed {
				t.Fatalf("false completion: count=%d error=%v published=%v observer=%v", count, err, outbox.published, observer.batches)
			}
		})
	}
}
