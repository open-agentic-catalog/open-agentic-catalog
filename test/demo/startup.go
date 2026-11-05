package main

import "errors"

// startupError retains the cause internally; stderr exposes only the fixed stage.
type startupError struct {
	stage string
	cause error
}

func (e *startupError) Error() string { return e.cause.Error() }
func (e *startupError) Unwrap() error { return e.cause }

func atStartupStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	var existing *startupError
	if errors.As(err, &existing) {
		return err
	}
	return &startupError{stage: stage, cause: err}
}

func startupStage(err error) string {
	var failure *startupError
	if errors.As(err, &failure) {
		switch failure.stage {
		case "sample.load", "prepared.write", "prepared.read", "tls.prepare",
			"tls.not-yet-valid", "tls.expired", "tls.hostname", "listener.address",
			"listener.address-in-use", "listener.serve":
			return failure.stage
		}
	}
	return "unclassified"
}
