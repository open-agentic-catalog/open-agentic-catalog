package federation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type timedMaintenanceStore struct {
	maintenanceStoreStub
	mu     sync.Mutex
	scans  []time.Time
	errors []error
}

func (s *timedMaintenanceStore) ListDueAutomaticSources(_ context.Context, at time.Time, _ time.Duration, _ string, _ int) ([]MaintenanceCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scans = append(s.scans, at)
	if len(s.scans) <= len(s.errors) {
		return nil, s.errors[len(s.scans)-1]
	}
	return nil, nil
}

func (s *timedMaintenanceStore) scanTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.scans...)
}

func TestAutomaticMaintainerRescansAtBoundedCadenceAndStopsOnCancellation(t *testing.T) {
	for _, test := range []struct {
		name     string
		interval time.Duration
		rescan   time.Duration
	}{
		{"short interval", time.Minute, 6 * time.Second},
		{"long interval capped", 24 * time.Hour, time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := &timedMaintenanceStore{}
				maintainer, err := NewMaintainer(store, clock.Real{}, id.Generator{}, test.interval, federationTestPolicyDigest, 5)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				started := time.Now()
				done := make(chan error, 1)
				go func() { done <- maintainer.Run(ctx) }()
				synctest.Wait()
				if scans := store.scanTimes(); len(scans) != 1 || !scans[0].Equal(started) {
					t.Fatalf("expected one immediate scan, got %v", scans)
				}
				for scan := 1; scan <= 2; scan++ {
					// These sleeps advance bubble time only, not the host clock.
					time.Sleep(test.rescan - time.Nanosecond)
					synctest.Wait()
					if scans := store.scanTimes(); len(scans) != scan {
						t.Fatalf("scan ran before its interval: %v", scans)
					}
					time.Sleep(time.Nanosecond)
					synctest.Wait()
					if scans := store.scanTimes(); len(scans) != scan+1 || !scans[scan].Equal(started.Add(time.Duration(scan)*test.rescan)) {
						t.Fatalf("scan did not run at the expected cadence: %v", scans)
					}
				}
				cancel()
				synctest.Wait()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancelled maintainer returned %v", err)
					}
				default:
					t.Fatal("maintainer did not stop after cancellation")
				}
				time.Sleep(test.rescan)
				synctest.Wait()
				if scans := store.scanTimes(); len(scans) != 3 {
					t.Fatalf("maintainer scanned after shutdown: %v", scans)
				}
			})
		})
	}
}

func TestAutomaticMaintainerToleratesNotFoundButReturnsPeriodicDependencyFailure(t *testing.T) {
	missing := catalog.E(catalog.CodeNotFound, "source disappeared during selection")
	unavailable := catalog.E(catalog.CodeDependencyUnavailable, "maintenance store unavailable")
	for _, test := range []struct {
		name   string
		errors []error
	}{
		{"initial source disappearance", []error{missing, nil, unavailable}},
		{"periodic source disappearance", []error{nil, missing, unavailable}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := &timedMaintenanceStore{errors: test.errors}
				maintainer, err := NewMaintainer(store, clock.Real{}, id.Generator{}, time.Minute, federationTestPolicyDigest, 5)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- maintainer.Run(ctx) }()
				for scan := 1; scan <= 3; scan++ {
					synctest.Wait()
					if scans := store.scanTimes(); len(scans) != scan {
						t.Fatalf("scan count = %d, want %d", len(scans), scan)
					}
					if scan < 3 {
						select {
						case err := <-done:
							t.Fatalf("maintainer stopped on a recoverable scan: %v", err)
						default:
						}
						time.Sleep(6 * time.Second)
					}
				}
				select {
				case err := <-done:
					if !errors.Is(err, unavailable) {
						t.Fatalf("periodic dependency failure returned %v", err)
					}
				default:
					t.Fatal("maintainer swallowed a periodic dependency failure")
				}
				time.Sleep(6 * time.Second)
				synctest.Wait()
				if scans := store.scanTimes(); len(scans) != 3 {
					t.Fatalf("maintainer scanned after a fatal error: %v", scans)
				}
			})
		})
	}
}
