package clock

import (
	"testing"
	"time"
)

func TestRealClockReturnsCurrentUTCTime(t *testing.T) {
	t.Parallel()

	before := time.Now().UTC()
	actual := (Real{}).Now()
	after := time.Now().UTC()
	if actual.Before(before) || actual.After(after) {
		t.Fatalf("real clock returned %s outside [%s, %s]", actual, before, after)
	}
	if actual.Location() != time.UTC {
		t.Fatalf("real clock location = %s, want UTC", actual.Location())
	}
}

func TestFakeClockNormalizesAndAdvances(t *testing.T) {
	t.Parallel()

	location := time.FixedZone("test", 2*60*60)
	initial := time.Date(2026, time.August, 29, 12, 30, 0, 0, location)
	clock := NewFake(initial)
	if got, want := clock.Now(), initial.UTC(); !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("initial fake time = %s, want %s in UTC", got, want)
	}
	clock.Advance(90 * time.Minute)
	if got, want := clock.Now(), initial.UTC().Add(90*time.Minute); !got.Equal(want) {
		t.Fatalf("advanced fake time = %s, want %s", got, want)
	}
}
