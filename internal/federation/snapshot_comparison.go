// Historical source comparison uses immutable occurrence membership, never the
// mutable current observation view or a fresh upstream request.
package federation

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// MaxSnapshotComparisonTextBytes bounds projected text across both selected
// memberships, independently of the per-snapshot synchronization entry ceiling.
const MaxSnapshotComparisonTextBytes = 32 << 20

// SnapshotComparisonRequest selects an immutable pair and a position in its
// deterministic filtered result. Limit includes the caller's lookahead row.
type SnapshotComparisonRequest struct {
	BeforeID               string
	AfterID                string
	IncludeUnchanged       bool
	AfterPosition          int
	ExpectedSourceRevision int64
	Limit                  int
}

// SnapshotComparisonSummary deliberately omits retrieval and policy details.
type SnapshotComparisonSummary struct {
	ID                  string    `json:"id"`
	MemberCount         int       `json:"memberCount"`
	StartedAt           time.Time `json:"startedAt"`
	WithdrawalAuthority bool      `json:"withdrawalAuthority"`
}

// SnapshotComparisonMember links one exact occurrence without loading native
// documents or exposing mutable freshness and last-seen projections.
type SnapshotComparisonMember struct {
	ObservationID  string                `json:"observationId"`
	Identifier     string                `json:"identifier"`
	Version        string                `json:"version"`
	VersionPresent bool                  `json:"versionPresent"`
	Kind           catalog.Kind          `json:"kind"`
	IdentityDigest string                `json:"identityDigest"`
	Occurrence     ObservationOccurrence `json:"occurrence"`
}

// SnapshotEntryChange pairs only unique source keys. Multiple occurrences keep
// separate before/after rows, preserving conflicts without inventing a match.
type SnapshotEntryChange struct {
	Position    int                       `json:"position"`
	SourceKey   string                    `json:"sourceKey"`
	Change      string                    `json:"change"`
	BeforeCount int                       `json:"beforeCount"`
	AfterCount  int                       `json:"afterCount"`
	Before      *SnapshotComparisonMember `json:"before,omitempty"`
	After       *SnapshotComparisonMember `json:"after,omitempty"`
}

// SnapshotComparison is a compact page over one exact pair. Pairwise changes
// are independent of synchronization transition counters.
type SnapshotComparison struct {
	SourceID       string                    `json:"sourceId"`
	SourceRevision int64                     `json:"sourceRevision"`
	Before         SnapshotComparisonSummary `json:"before"`
	After          SnapshotComparisonSummary `json:"after"`
	Items          []SnapshotEntryChange     `json:"items"`
}

// NewSnapshotComparison validates exact pair eligibility before membership is
// selected. Retained reads remain available after source configuration removal.
func NewSnapshotComparison(before, after Snapshot, request SnapshotComparisonRequest) (SnapshotComparison, error) {
	for _, snapshot := range []Snapshot{before, after} {
		if snapshot.ID == "" || snapshot.SourceID == "" || snapshot.TenantID == "" || snapshot.SourceRevision < 1 || snapshot.StartedAt.IsZero() || snapshot.MemberCount < 0 {
			return SnapshotComparison{}, comparisonEvidenceError()
		}
		if snapshot.State == SnapshotComplete && (snapshot.CompletedAt == nil || snapshot.CompletedAt.Before(snapshot.StartedAt)) {
			return SnapshotComparison{}, comparisonEvidenceError()
		}
	}
	if before.SourceID != after.SourceID || before.SourceRevision != after.SourceRevision {
		return SnapshotComparison{}, catalog.E(catalog.CodeInvalidArgument, "comparison requires snapshots from the same source and source revision")
	}
	if before.State != SnapshotComplete || after.State != SnapshotComplete || !before.EnumerationComplete || !after.EnumerationComplete {
		return SnapshotComparison{}, catalog.E(catalog.CodePreconditionFailed, "comparison requires two complete enumerations; failed or incomplete membership is unavailable")
	}
	if before.MemberCount > MaxSynchronizationEntries || after.MemberCount > MaxSynchronizationEntries {
		return SnapshotComparison{}, catalog.E(catalog.CodePreconditionFailed, "source snapshot comparison exceeds the supported membership work limit")
	}
	if request.ExpectedSourceRevision != 0 && request.ExpectedSourceRevision != before.SourceRevision {
		return SnapshotComparison{}, catalog.E(catalog.CodeInvalidArgument, "comparison source revision does not match the selected snapshots")
	}
	summary := func(s Snapshot) SnapshotComparisonSummary {
		return SnapshotComparisonSummary{ID: s.ID, MemberCount: s.MemberCount, StartedAt: s.StartedAt, WithdrawalAuthority: s.WithdrawalAuthority}
	}
	return SnapshotComparison{SourceID: before.SourceID, SourceRevision: before.SourceRevision, Before: summary(before), After: summary(after), Items: []SnapshotEntryChange{}}, nil
}

// CompareSnapshots requires current summary authority on every page. IDs and
// cursor values are validated before the persistence boundary sees them.
func (m *Manager) CompareSnapshots(ctx context.Context, request SnapshotComparisonRequest) (SnapshotComparison, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return SnapshotComparison{}, err
	}
	if !identity.ValidTenantID(request.BeforeID) || !identity.ValidTenantID(request.AfterID) || request.Limit < 1 || request.Limit > 100 || request.AfterPosition < 0 || request.AfterPosition > 2*MaxSynchronizationEntries || request.ExpectedSourceRevision < 0 {
		return SnapshotComparison{}, catalog.E(catalog.CodeInvalidArgument, "snapshot comparison selection, position or limit is invalid")
	}
	request.Limit++
	return m.store.CompareSnapshots(ctx, actor.TenantID, request)
}

// CompareSnapshots selects bounded compact membership under one memory lock.
// PostgreSQL applies the equivalent work bounds and page selection in SQL.
func (m *MemoryStore) CompareSnapshots(ctx context.Context, tenantID string, request SnapshotComparisonRequest) (SnapshotComparison, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	before, b := m.snapshots[request.BeforeID]
	after, a := m.snapshots[request.AfterID]
	if !b || !a || before.TenantID != tenantID || after.TenantID != tenantID {
		return SnapshotComparison{}, catalog.E(catalog.CodeNotFound, "source snapshot was not found")
	}
	comparison, err := NewSnapshotComparison(before, after, request)
	if err != nil {
		return SnapshotComparison{}, err
	}
	type group struct{ before, after []SnapshotComparisonMember }
	groups := map[string]*group{}
	selected := [2][]ObservationOccurrence{}
	wanted := map[string]bool{}
	for key, occurrence := range m.occurrences {
		if err := ctx.Err(); err != nil {
			return SnapshotComparison{}, err
		}
		if !strings.HasPrefix(key, tenantID+"/") {
			continue
		}
		for side, snapshot := range []Snapshot{before, after} {
			if occurrence.SnapshotID != snapshot.ID {
				continue
			}
			selected[side] = append(selected[side], occurrence)
			if len(selected[side]) > MaxSynchronizationEntries {
				return SnapshotComparison{}, comparisonWorkError()
			}
			wanted[occurrence.ObservationID] = true
		}
	}
	if len(selected[0]) != before.MemberCount || len(selected[1]) != after.MemberCount {
		return SnapshotComparison{}, comparisonEvidenceError()
	}
	observations := map[string]Observation{}
	for _, observation := range m.observations {
		if observation.TenantID == tenantID && wanted[observation.ID] {
			observations[observation.ID] = observation
		}
	}
	textBytes := 0
	for side, snapshot := range []Snapshot{before, after} {
		for _, occurrence := range selected[side] {
			if err := ctx.Err(); err != nil {
				return SnapshotComparison{}, err
			}
			observation, ok := observations[occurrence.ObservationID]
			if !ok || ValidateRetainedOccurrenceIntegrity(occurrence, observation, snapshot) != nil {
				return SnapshotComparison{}, comparisonEvidenceError()
			}
			member := SnapshotComparisonMember{ObservationID: observation.ID, Identifier: observation.Identifier, Version: observation.Version, VersionPresent: observation.VersionPresent, Kind: observation.Kind, IdentityDigest: observation.IdentityDigest, Occurrence: occurrence}
			textBytes += SnapshotComparisonMemberTextBytes(observation.SourceKey, member)
			if textBytes > MaxSnapshotComparisonTextBytes {
				return SnapshotComparison{}, comparisonWorkError()
			}
			g := groups[observation.SourceKey]
			if g == nil {
				g = &group{}
				groups[observation.SourceKey] = g
			}
			if side == 0 {
				g.before = append(g.before, member)
			} else {
				g.after = append(g.after, member)
			}
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	position := 0
	appendChange := func(change SnapshotEntryChange) {
		position++
		change.Position = position
		if position > request.AfterPosition && len(comparison.Items) < request.Limit {
			comparison.Items = append(comparison.Items, change)
		}
	}
	for _, key := range keys {
		g := groups[key]
		base := SnapshotEntryChange{SourceKey: key, BeforeCount: len(g.before), AfterCount: len(g.after)}
		if len(g.before) > 1 || len(g.after) > 1 {
			for side, members := range [][]SnapshotComparisonMember{g.before, g.after} {
				sort.Slice(members, func(i, j int) bool {
					x, y := members[i].Occurrence, members[j].Occurrence
					if x.RetrievalDigest != y.RetrievalDigest {
						return x.RetrievalDigest < y.RetrievalDigest
					}
					return x.EntryLocation < y.EntryLocation
				})
				for _, member := range members {
					change := base
					change.Change = "multiple"
					if side == 0 {
						change.Before = &member
					} else {
						change.After = &member
					}
					appendChange(change)
				}
			}
			continue
		}
		if len(g.before) == 1 {
			base.Before = &g.before[0]
		}
		if len(g.after) == 1 {
			base.After = &g.after[0]
		}
		base.Change = "changed"
		switch {
		case base.Before == nil:
			base.Change = "added"
		case base.After == nil:
			base.Change = "absent"
		case base.Before.IdentityDigest == base.After.IdentityDigest && base.Before.Occurrence.ExactEntryDigest == base.After.Occurrence.ExactEntryDigest && base.Before.Occurrence.NormalizedEntryDigest == base.After.Occurrence.NormalizedEntryDigest && base.Before.Occurrence.NormalizationInputDigest == base.After.Occurrence.NormalizationInputDigest:
			base.Change = "unchanged"
		}
		if base.Change != "unchanged" || request.IncludeUnchanged {
			appendChange(base)
		}
	}
	return comparison, nil
}

// SnapshotComparisonMemberTextBytes measures projected text without native
// documents. SQL applies the same byte budget before returning any page.
func SnapshotComparisonMemberTextBytes(key string, m SnapshotComparisonMember) int {
	o := m.Occurrence
	return len(key) + len(m.ObservationID) + len(m.Identifier) + len(m.Version) + len(m.Kind) + len(m.IdentityDigest) + len(o.ObservationID) + len(o.SourceID) + len(o.SnapshotID) + len(o.RetrievalDigest) + len(o.EntryLocation) + len(o.ExactEntryDigest) + len(o.NormalizedEntryDigest) + len(o.NormalizationInputDigest)
}

// comparisonWorkError refuses unsupported work without a partial result.
func comparisonWorkError() error {
	return catalog.E(catalog.CodePreconditionFailed, "source snapshot comparison exceeds the supported membership work limit")
}

// comparisonEvidenceError keeps retained inconsistency diagnostics private.
func comparisonEvidenceError() error {
	return catalog.E(catalog.CodeDependencyUnavailable, "retained source membership evidence is unavailable")
}
