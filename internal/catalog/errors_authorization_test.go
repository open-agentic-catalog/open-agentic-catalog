package catalog

import (
	"errors"
	"fmt"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestAuthorizationErrorsKeepServiceClassificationAndCause(t *testing.T) {
	cause := errors.New("private dependency cause")
	for _, tc := range []struct {
		identity identity.ErrorCode
		catalog  ErrorCode
	}{
		{identity.CodeForbidden, CodeForbidden},
		{identity.CodeUnauthenticated, CodeUnauthenticated},
		{identity.CodeDependency, CodeDependencyUnavailable},
		{identity.CodeInvalidArgument, CodeInvalidArgument},
		{identity.CodeConflict, CodeConflict},
		{identity.CodePreconditionFailed, CodePreconditionFailed},
		{identity.CodeNotFound, CodeNotFound},
		{identity.CodeSeparationOfDuties, CodeSeparationOfDuties},
		{identity.CodeInternal, CodeInternal},
		{identity.ErrorCode("UNKNOWN"), CodeInternal},
	} {
		t.Run(string(tc.identity), func(t *testing.T) {
			err := fmt.Errorf("admission wrapper: %w", identity.Wrap(tc.identity, "safe identity message", cause))
			if ErrorCodeOf(err) != tc.catalog || !errors.Is(err, cause) {
				t.Fatal("wrapped admission lost classification or internal cause")
			}
			// A deliberately projected domain error owns its public classification,
			// even when its private cause belongs to the identity domain.
			projected := Wrap(CodeValidationFailed, "safe domain message", err)
			if ErrorCodeOf(projected) != CodeValidationFailed || !errors.Is(projected, cause) {
				t.Fatal("identity cause overrode the public domain classification")
			}
		})
	}
}
