package processinfo

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestClockCorrectionsDoNotChangeElapsedTime(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 6, 10, 0, 0, 0, time.FixedZone("fixture", 3600))
	wall, elapsed := start, time.Duration(0)
	p, err := newProcess(func() time.Time { return wall }, func(got time.Time) time.Duration {
		if got != start {
			t.Fatal("elapsed source lost the original start instant")
		}
		return elapsed
	}, id.Generator{Reader: bytes.NewReader(make([]byte, 16))})
	if err != nil {
		t.Fatal(err)
	}
	for _, trial := range []struct {
		wall    time.Duration
		elapsed time.Duration
		seconds int64
	}{{0, 0, 0}, {-time.Hour, 2500 * time.Millisecond, 2}, {24 * time.Hour, 3500 * time.Millisecond, 3}, {0, -time.Second, 0}} {
		wall, elapsed = start.Add(trial.wall), trial.elapsed
		got := p.Read()
		if got.InstanceID != p.ID() || !got.StartedAt.Equal(start) || got.StartedAt.Location() != time.UTC || !got.ObservedAt.Equal(wall) || got.ObservedAt.Location() != time.UTC || got.UptimeSeconds != trial.seconds {
			t.Fatalf("snapshot = %#v", got)
		}
		data, err := json.Marshal(got)
		if err != nil || strings.Contains(string(data), "fixture") || strings.Contains(string(data), "m=") {
			t.Fatalf("invalid public time projection: %s %v", data, err)
		}
	}
}

func TestIdentityFailureIsSafe(t *testing.T) {
	t.Parallel()
	p, err := newProcess(time.Now, time.Since, id.Generator{Reader: io.LimitReader(strings.NewReader("private-fixture"), 0)})
	if p != nil || err == nil || err.Error() != "process identity unavailable" {
		t.Fatalf("result = %v %v", p, err)
	}
}

func TestProcessLifetimeAndConcurrentReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, err := New()
		if err != nil {
			t.Fatal(err)
		}
		initial := p.Read()
		time.Sleep(3500 * time.Millisecond)
		var group sync.WaitGroup
		for range 12 {
			group.Go(func() {
				got := p.Read()
				if got.InstanceID != initial.InstanceID || got.StartedAt != initial.StartedAt || got.UptimeSeconds != 3 || !got.ObservedAt.Equal(initial.ObservedAt.Add(3500*time.Millisecond)) {
					t.Errorf("inconsistent process reading: %#v", got)
				}
			})
		}
		group.Wait()
		next, err := New()
		if err != nil || next.ID() == p.ID() || next.Read().UptimeSeconds != 0 {
			t.Fatalf("new execution = %v %v", next, err)
		}
	})
}

func TestPreciseElapsedSamplePreservesExistingReadContract(t *testing.T) {
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	p := &Process{instance: "execution", started: start, now: func() time.Time { return start.Add(-time.Hour) }, since: func(got time.Time) time.Duration {
		if !got.Equal(start) {
			t.Fatal("lost original monotonic start")
		}
		return 1250 * time.Millisecond
	}}
	snapshot, elapsed := p.ReadWithElapsed()
	if elapsed != 1250*time.Millisecond || snapshot.UptimeSeconds != 1 || !snapshot.ObservedAt.Before(snapshot.StartedAt) || snapshot != p.Read() {
		t.Fatalf("imprecise or wall-derived sample: %+v %v", snapshot, elapsed)
	}
}
