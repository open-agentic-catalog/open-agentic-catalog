// Package clock supplies real and deterministic test clocks.
// Both implementations return UTC. Fake clocks advance only through explicit
// calls, so lease and retention tests need no wall-clock sleeps.
package clock

import (
	"sync"
	"time"
)

// Clock makes time-dependent domain behavior deterministic in tests.
type Clock interface {
	// Now returns the current instant in UTC; test implementations may hold it fixed.
	Now() time.Time
}

// Real reads the system clock without retaining mutable state.
type Real struct{}

// Now returns the current system time normalized to UTC.
func (Real) Now() time.Time { return time.Now().UTC() }

// Fake is a concurrency-safe clock whose time is controlled by the caller.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake fixes the initial instant in UTC without starting a timer.
func NewFake(now time.Time) *Fake { return &Fake{now: now.UTC()} }

// Now returns the last explicitly set instant, even when wall time has advanced.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance adds d atomically; negative durations can model a clock moving back.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
