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

func sourceAcceptsVersionAppend(status SourceStatus) bool {
	return status == SourceActive || status == SourceError
}

// withAdoptionOccurrence fences same-revision synchronization as well as policy
// changes, after native acquisition and before the local repository mutation.
func (m *MemoryStore) withAdoptionOccurrence(ctx context.Context, tenant, sourceID string, revision int64, selected Observation, occurrence ObservationOccurrence, appendVersion bool, commit func(context.Context) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	source, ok := m.sources[sourceID]
	if !ok || source.TenantID != tenant || source.Status == SourceRemoved || source.Revision != revision || appendVersion && !sourceAcceptsVersionAppend(source.Status) {
		return catalog.E(catalog.CodePreconditionFailed, "source policy changed during adoption")
	}
	current := false
	for _, obs := range m.observations {
		if obs.TenantID != tenant || obs.SourceID != sourceID {
			continue
		}
		if obs.ID == selected.ID {
			current = obs.Freshness == FreshCurrent && obs.Completeness == CompleteEntry && obs.ExactEntryDigest == selected.ExactEntryDigest && obs.NormalizedEntryDigest == selected.NormalizedEntryDigest
		}
		if obs.Identifier == selected.Identifier && obs.VersionPresent == selected.VersionPresent && obs.Version == selected.Version && obs.Freshness == FreshCurrent && obs.NormalizedEntryDigest != "" && obs.NormalizedEntryDigest != selected.NormalizedEntryDigest {
			return catalog.E(catalog.CodeConflict, "selected source has conflicting current observations")
		}
	}
	snap := m.snapshots[occurrence.SnapshotID]
	saved, ok := m.occurrences[occurrenceKey(tenant, occurrence)]
	if !current || !ok || saved != occurrence || !saved.EvidenceComplete || snap.TenantID != tenant || snap.State != SnapshotComplete || !snap.EnumerationComplete {
		return catalog.E(catalog.CodePreconditionFailed, "selected observation is no longer current and complete")
	}
	return commit(ctx)
}
