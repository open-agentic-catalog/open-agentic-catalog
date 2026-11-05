package catalog

import "github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"

// PermissionError preserves unavailable authority as unavailable, rather than
// misreporting a policy-store failure as a deliberate access refusal.
func PermissionError(message string, err error) error {
	code := CodeForbidden
	if identity.ErrorCodeOf(err) == identity.CodeDependency || ErrorCodeOf(err) == CodeDependencyUnavailable {
		code = CodeDependencyUnavailable
	}
	return Wrap(code, message, err)
}
