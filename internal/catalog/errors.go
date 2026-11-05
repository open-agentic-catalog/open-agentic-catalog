// Domain errors keep client-safe classifications separate from internal causes
// and mark the narrow failed-command path that preserves completed evidence.

package catalog

import (
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// ErrInactiveDocument distinguishes a document with no active Resource
// reference from a document absent from PostgreSQL. Readers still receive
// NOT_FOUND, while the document reader is not allowed to resurrect bytes
// retained only for a soft-deleted Resource.
var ErrInactiveDocument = errors.New("catalog document has no active resource reference")

// ErrSearchGenerationChanged identifies a repository generation check without
// changing the public stale-cursor error. First-page composition may retry this
// race; a client-supplied continuation must remain stale.
var ErrSearchGenerationChanged = errors.New("catalog search generation changed")

// ErrorCode identifies a stable service error independently of its message or
// internal cause. HTTP handlers map these codes to problem responses.
type ErrorCode string

const (
	CodeInvalidArgument          ErrorCode = "INVALID_ARGUMENT"
	CodeNotFound                 ErrorCode = "NOT_FOUND"
	CodeMethodNotAllowed         ErrorCode = "METHOD_NOT_ALLOWED"
	CodeConflict                 ErrorCode = "CONFLICT"
	CodePreconditionFailed       ErrorCode = "PRECONDITION_FAILED"
	CodeUnauthenticated          ErrorCode = "UNAUTHENTICATED"
	CodeForbidden                ErrorCode = "FORBIDDEN"
	CodeValidationFailed         ErrorCode = "VALIDATION_FAILED"
	CodeVersionSealed            ErrorCode = "VERSION_SEALED"
	CodeSeparationOfDuties       ErrorCode = "SEPARATION_OF_DUTIES"
	CodeInvalidTransition        ErrorCode = "INVALID_TRANSITION"
	CodeIdempotencyKeyReused     ErrorCode = "IDEMPOTENCY_KEY_REUSED"
	CodeDependencyUnavailable    ErrorCode = "DEPENDENCY_UNAVAILABLE"
	CodeWebhookTestBusy          ErrorCode = "WEBHOOK_TEST_BUSY"
	CodeUnsupportedProfile       ErrorCode = "UNSUPPORTED_PROFILE"
	CodePublicationNotReady      ErrorCode = "PUBLICATION_NOT_READY"
	CodeSourcePolicyDenied       ErrorCode = "SOURCE_POLICY_DENIED"
	CodeAuditExportRangeTooLarge ErrorCode = "AUDIT_EXPORT_RANGE_TOO_LARGE"
	CodeWorkLimitExceeded        ErrorCode = "WORK_LIMIT_EXCEEDED"
	CodeRequestDeadlineExceeded  ErrorCode = "REQUEST_DEADLINE_EXCEEDED"
	CodeInternal                 ErrorCode = "INTERNAL"
)

// DomainError separates the stable client-facing code and message from an
// internal cause. Error() includes that cause and must not be sent to clients
// or copied into routine logs; transport adapters select safe projections.
type DomainError struct {
	Code    ErrorCode
	Message string
	Fields  map[string]string
	Cause   error
}

// preservedEvidenceError marks a failed command whose reviewed audit evidence
// was prepared successfully. HTTP idempotency may then commit that evidence
// with the exact problem response instead of rolling the command transaction
// back. The wrapper does not change the public error code or detail.
type preservedEvidenceError struct{ err error }

// Error retains the wrapped diagnostic text without changing the public
// classification.
func (e *preservedEvidenceError) Error() string { return e.err.Error() }

// Unwrap preserves inspection of the underlying domain error.
func (e *preservedEvidenceError) Unwrap() error { return e.err }

// Error includes the internal cause for diagnostics; HTTP responses must use
// the safe code and message fields instead.
func (e *DomainError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the internal cause to errors.Is and errors.As.
func (e *DomainError) Unwrap() error { return e.Cause }

// E constructs a domain error whose message must be safe for the caller.
func E(code ErrorCode, message string) error {
	return &DomainError{Code: code, Message: message}
}

// Wrap retains an internal cause without making it part of the public message.
func Wrap(code ErrorCode, message string, cause error) error {
	return &DomainError{Code: code, Message: message, Cause: cause}
}

// PreserveEvidence marks an error returned after its required audit append has
// succeeded. It is intentionally narrow: ordinary command failures must still
// roll back domain changes and their audit and outbox records. The command
// boundary may retain the error response separately for idempotent replay.
func PreserveEvidence(err error) error {
	if err == nil || ErrorPreservesEvidence(err) {
		return err
	}
	return &preservedEvidenceError{err: err}
}

// ErrorPreservesEvidence reports whether an error carries the reviewed
// evidence-preservation marker used by the HTTP command boundary.
func ErrorPreservesEvidence(err error) bool {
	var marked *preservedEvidenceError
	return errors.As(err, &marked)
}

// ErrorCodeOf unwraps domain errors and maps unclassified errors to INTERNAL.
func ErrorCodeOf(err error) ErrorCode {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		return domainErr.Code
	}
	var admission *identity.Error
	if errors.As(err, &admission) {
		switch admission.Code {
		case identity.CodeForbidden:
			return CodeForbidden
		case identity.CodeUnauthenticated:
			return CodeUnauthenticated
		case identity.CodeDependency:
			return CodeDependencyUnavailable
		case identity.CodeInvalidArgument:
			return CodeInvalidArgument
		case identity.CodeConflict:
			return CodeConflict
		case identity.CodePreconditionFailed:
			return CodePreconditionFailed
		case identity.CodeNotFound:
			return CodeNotFound
		case identity.CodeSeparationOfDuties:
			return CodeSeparationOfDuties
		case identity.CodeInternal:
			return CodeInternal
		}
	}
	return CodeInternal
}
