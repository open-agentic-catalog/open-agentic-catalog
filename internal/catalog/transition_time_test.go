package catalog

import (
	"testing"
	"time"
)

func TestTransitionTime(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{-2 * time.Second, 0, 2 * time.Second} {
		now := base.Add(offset + 999*time.Nanosecond)
		got := TransitionTime(now, base.Add(-time.Second), base)
		want := base
		if offset > 0 {
			want = base.Add(offset)
		}
		if !got.Equal(want) || got.Location() != time.UTC {
			t.Fatalf("offset %v: got %v, want %v", offset, got, want)
		}
	}
	if got := TransitionTime(base.In(time.FixedZone("offset", 3600))); !got.Equal(base) || got.Location() != time.UTC {
		t.Fatalf("unbounded timestamp = %v", got)
	}
}
