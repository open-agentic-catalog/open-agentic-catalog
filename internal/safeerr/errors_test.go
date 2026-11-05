package safeerr

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

type explosiveError struct{}

func (explosiveError) Error() string { panic("must not format private error") }

func TestReviewedProjectionPreservesInternalCauses(t *testing.T) {
	cause := errors.New("private-token")
	reviewed := Wrap(cause, "cannot read configuration file")
	wrapped := fmt.Errorf("private path: %w", reviewed)
	if Message(wrapped) != "cannot read configuration file" || !errors.Is(wrapped, cause) || reviewed.Error() != cause.Error() {
		t.Fatal("projection changed cause or lost reviewed guidance")
	}
	for _, err := range []error{cause, explosiveError{}, errors.Join(cause, explosiveError{})} {
		if got := Message(err); got != "operation failed; check configuration and dependency availability" {
			t.Fatalf("unreviewed projection: %q", got)
		}
	}
	if Wrap(nil, "unused") != nil || New("usage: command").Error() != "usage: command" {
		t.Fatal("empty or literal projection")
	}
}

func TestPanicBoundaryDiscardsValuesAndRetainsCleanupFailure(t *testing.T) {
	for _, value := range []any{"private-panic", explosiveError{}, nil} {
		cleanup := false
		cause := errors.New("cleanup-cause")
		err := func() (err error) {
			defer Recover(&err)
			defer func() { cleanup = true; err = cause }()
			panic(value)
		}()
		if !cleanup || !errors.Is(err, ErrPanic) || !errors.Is(err, cause) || strings.Contains(Message(err), "private") {
			t.Fatalf("panic outcome: %v", err)
		}
		if Message(errors.Join(New("less important"), err)) != "application panic; process must stop" {
			t.Fatal("panic hidden by reviewed sibling")
		}
	}
	if err := func() (err error) { defer Recover(&err); return nil }(); err != nil {
		t.Fatal(err)
	}
}

func TestStagesPreserveSpecificContextAndIdentity(t *testing.T) {
	cause := New("reviewed guidance")
	err := At("configuration", cause)
	if err.Error() != cause.Error() || Stage(At("startup", err)) != "configuration" || !errors.Is(err, cause) || Message(err) != "reviewed guidance" {
		t.Fatal("stage changed safe message or cause")
	}
	if At("unused", nil) != nil || Stage(cause) != "command" || Stage(At("startup", ErrPanic)) != "panic" {
		t.Fatal("stage defaults or panic precedence")
	}
}
