package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestSupervisorDoesNotRestartPanickedDependency(t *testing.T) {
	calls := 0
	err := superviseDependency(t.Context(), slog.New(slog.DiscardHandler), "worker", config.Defaults().Worker, strings.NewReader(""), func(context.Context) error { calls++; panic("private-worker-panic") })
	if !errors.Is(err, safeerr.ErrPanic) || calls != 1 {
		t.Fatalf("panic restarted: %v calls=%d", err, calls)
	}
	// Even an erroneous dependency wrapper must not turn the panic into a retry.
	if retryableDependencyFailure(catalog.Wrap(catalog.CodeDependencyUnavailable, "dependency", err)) {
		t.Fatal("panic considered retryable")
	}
}

func TestPanickedComponentDrainsSiblings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		logger := slog.New(slog.DiscardHandler)
		results := make(chan error, 2)
		operations := make(chan error, 1)
		started := make(chan struct{})
		stopped := false
		go componentResult(results, func() error { close(started); <-ctx.Done(); stopped = true; return nil })
		go componentResult(results, func() error { <-started; panic("private-listener-panic") })
		remaining, operationsExited, err := waitForComponentExit(ctx, cancel, logger, results, operations, 2)
		if remaining != 1 || operationsExited || !errors.Is(err, safeerr.ErrPanic) || ctx.Err() == nil {
			t.Fatalf("supervisor: %d %v %v", remaining, operationsExited, err)
		}
		err = drainComponents(t.Context(), results, remaining, err)
		if !stopped || !errors.Is(err, safeerr.ErrPanic) {
			t.Fatalf("drain hid panic or missed sibling: %v", err)
		}
	})
}

func TestCancellationDoesNotHideReturnedPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := superviseDependency(ctx, slog.New(slog.DiscardHandler), "worker", config.Defaults().Worker, strings.NewReader(""), func(context.Context) error { cancel(); return errors.Join(context.Canceled, safeerr.ErrPanic) })
	if !errors.Is(err, safeerr.ErrPanic) {
		t.Fatalf("cancellation hid panic: %v", err)
	}
}
