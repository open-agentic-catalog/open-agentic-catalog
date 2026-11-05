package webhook

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

type dispatchTrace struct {
	start func(catalog.TraceContext)
	end   func(string)
}

func (s dispatchTrace) Start(ctx context.Context, name string, kind catalog.TraceKind, cause catalog.TraceContext) (context.Context, func(string)) {
	if name == "webhook dispatch" && kind == catalog.TraceAttempt {
		s.start(cause)
		return ctx, s.end
	}
	return ctx, func(string) {}
}

func TestDispatchTraceEndsAfterLeaseCheckedAcknowledgement(t *testing.T) {
	for _, fail := range []bool{false, true} {
		now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
		cause := catalog.TraceContext("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
		event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000100", TenantID: "00000000-0000-7000-8000-000000000001", Type: catalog.CanonicalEventType("resource.created"), CreatedAt: now, CausalTrace: cause}
		outbox := &memoryOutbox{events: []catalog.OutboxEvent{event}, published: map[string]time.Time{}}
		if fail {
			outbox.publishErr = errors.New("lost lease")
		}
		observer := &recordingOutboxObserver{}
		cipher, _ := NewAESGCM(keyBytes(5))
		d, err := newTestDispatcher(outbox, NewMemoryStore(), cipher, &recordingSender{}, clock.NewFake(now), &sequenceIDs{}, "trace-worker", time.Minute, 3, time.Second, time.Hour, observer)
		if err != nil {
			t.Fatal(err)
		}
		started, ended := 0, 0
		tracer := dispatchTrace{start: func(got catalog.TraceContext) {
			started++
			if got != cause {
				t.Error("lost creator link")
			}
		}, end: func(outcome string) {
			ended++
			_, published := outbox.published[event.ID]
			if published == fail || (outcome == "error") != fail {
				t.Error("trace ended before acknowledgement or hid lease failure")
			}
		}}
		_, err = d.DispatchOnce(catalog.WithTracer(t.Context(), tracer))
		if (err != nil) != fail || started != 1 || ended != 1 || len(observer.batches) != 1 || observer.batches[0].failed != fail {
			t.Fatalf("trace lifecycle: start=%d end=%d err=%v", started, ended, err)
		}
	}
}
