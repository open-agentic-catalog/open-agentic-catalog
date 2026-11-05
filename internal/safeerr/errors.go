// Package safeerr separates reviewed diagnostic text from private error causes.
// It also contains the process-fatal classification for recovered application panics.
package safeerr

import "errors"

// ErrPanic marks an interrupted application boundary. It is never retryable and
// deliberately retains neither the panic value nor a stack.
var ErrPanic = errors.New("application panic")

type described struct {
	message string
	cause   error
}

func (e *described) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return e.message
}
func (e *described) Unwrap() error { return e.cause }

// New marks a fixed, reviewed usage or configuration message safe for output.
// Never pass user input, dependency errors or formatted private values here.
func New(message string) error { return &described{message: message} }

// Wrap preserves an internal cause while assigning a reviewed output message.
func Wrap(err error, message string) error {
	if err == nil {
		return nil
	}
	return &described{message: message, cause: err}
}

// Message selects only explicitly reviewed text; ordinary Error methods are not
// evaluated. In particular a joined panic cannot be hidden by another message.
func Message(err error) string {
	if errors.Is(err, ErrPanic) {
		return "application panic; process must stop"
	}
	var safe *described
	if errors.As(err, &safe) {
		return safe.message
	}
	return "operation failed; check configuration and dependency availability"
}

// Recover must be deferred directly at an owned execution boundary. It returns
// a fatal marker, allowing the caller to cancel and drain without formatting the
// recovered value. Deferred resource cleanup still runs during unwinding.
func Recover(result *error) {
	if recover() != nil {
		*result = errors.Join(ErrPanic, *result)
	}
}

// staged retains a fixed operation label without rewriting the internal cause.
type staged struct {
	stage string
	cause error
}

func (e *staged) Error() string { return e.cause.Error() }
func (e *staged) Unwrap() error { return e.cause }

// At attaches code-owned stage vocabulary, retaining a more specific existing
// stage. Never supply argument values, URLs or object identifiers as stage names.
func At(stage string, err error) error {
	if err == nil {
		return nil
	}
	var existing *staged
	if errors.As(err, &existing) {
		return err
	}
	return &staged{stage: stage, cause: err}
}

// Stage projects the operation label, with panic taking precedence over normal
// operation classifications. It does not format the error or its causes.
func Stage(err error) string {
	if errors.Is(err, ErrPanic) {
		return "panic"
	}
	var value *staged
	if errors.As(err, &value) {
		return value.stage
	}
	return "command"
}
