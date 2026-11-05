// Job collection filters select retained metadata before visibility and page bounds.
package jobs

import (
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// validateListFilters keeps optional exact filters within the public collection contract.
func validateListFilters(request ListRequest) error {
	if !utf8.ValidString(request.Kind) || utf8.RuneCountInString(request.Kind) > 100 || strings.ContainsRune(request.Kind, 0) {
		return catalog.E(catalog.CodeInvalidArgument, "kind must be at most 100 Unicode characters without NUL")
	}
	if request.State != "" {
		switch request.State {
		case StateQueued, StateRunning, StateWaiting, StateSucceeded, StateFailed, StateCancelled, StateDeadLetter:
		default:
			return catalog.E(catalog.CodeInvalidArgument, "state must be a supported Job state")
		}
	}
	if request.ParentJobID != "" && !identity.ValidTenantID(request.ParentJobID) {
		return catalog.E(catalog.CodeInvalidArgument, "parentJobId must be a UUID")
	}
	return nil
}

// matchesListFilters never reads a retry parent or changes the child's admission.
func matchesListFilters(job Job, request ListRequest) bool {
	return (request.Kind == "" || job.Kind == request.Kind) &&
		(request.State == "" || job.State == request.State) &&
		(request.ParentJobID == "" || strings.EqualFold(job.ParentJobID, request.ParentJobID))
}
