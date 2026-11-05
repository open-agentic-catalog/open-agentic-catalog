// Source directory filters preserve exact stored names and nonremoved membership.
package federation

import (
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func validateSourceListFilters(request SourceListRequest) error {
	if !utf8.ValidString(request.DisplayName) || utf8.RuneCountInString(request.DisplayName) > 160 || strings.ContainsRune(request.DisplayName, 0) {
		return catalog.E(catalog.CodeInvalidArgument, "displayName must contain at most 160 Unicode characters without NUL")
	}
	if request.Status != "" && request.Status != SourceActive && request.Status != SourcePaused && request.Status != SourceError {
		return catalog.E(catalog.CodeInvalidArgument, "status must be active, paused or error")
	}
	return nil
}
