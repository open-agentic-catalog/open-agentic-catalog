package federation

import (
	"context"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// exclusionIdentifier preserves opaque evidence names and normalizes valid AIR
// identities exactly as retained observations do. Names remain case-sensitive.
func exclusionIdentifier(value string) string {
	if identifier, err := catalog.ParseAIRIdentifier(value); err == nil {
		return identifier.String()
	}
	return value
}

func validateExcludedIdentifiers(values []string) error {
	if len(values) > 1000 {
		return catalog.E(catalog.CodeInvalidArgument, "source exclusions exceed 1000 identifiers")
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || utf8.RuneCountInString(value) > 512 || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || strings.TrimSpace(value) != value {
			return catalog.E(catalog.CodeInvalidArgument, "source exclusions require valid nonempty identifiers within 512 Unicode characters without NUL or surrounding whitespace")
		}
		key := exclusionIdentifier(value)
		if seen[key] {
			return catalog.E(catalog.CodeInvalidArgument, "source exclusions contain a duplicate identifier")
		}
		seen[key] = true
	}
	return nil
}

func normalizeExcludedIdentifiers(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = exclusionIdentifier(value)
	}
	slices.Sort(result)
	return result
}

func scopeExcludesIdentifier(scope Scope, value string) bool {
	if len(scope.ExcludedIdentifiers) == 0 {
		return false
	}
	return slices.Contains(scope.ExcludedIdentifiers, exclusionIdentifier(value))
}

// withSourceRevision prevents a concurrent memory source edit during the final
// local draft commit. Artifact retrieval finishes before this lock is acquired.
func (m *MemoryStore) withSourceRevision(ctx context.Context, tenant, sourceID string, revision int64, commit func(context.Context) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	source, ok := m.sources[sourceID]
	if !ok || source.TenantID != tenant || source.Status == SourceRemoved || source.Revision != revision {
		return catalog.E(catalog.CodePreconditionFailed, "source policy changed during adoption")
	}
	return commit(ctx)
}
