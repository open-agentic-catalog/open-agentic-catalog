// Publication bounds and integrity checks apply the same construction ceilings
// to candidates, retained documents and activation eligibility.

package publication

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

const (
	// A publication contains at most one entry per eligible local release plus
	// the ARD Registry self-entry. These are safety ceilings, not page sizes.
	MaxPublicationARDEntries         = catalog.MaxPublicationRecords + 1
	MaxPublicationAICatalogEntries   = catalog.MaxPublicationRecords
	MaxPublicationExclusions         = catalog.MaxPublicationRecords
	MaxPublicationArtifactReferences = catalog.MaxPublicationRecords
	MaxPublicationDocumentBytes      = 16 << 20
	MaxPublicationSnapshotBytes      = 36 << 20
	MaxPublicationWorkUnits          = 100_000
	MaxConcurrentConstructions       = 1
)

// ConstructionLimiter is shared by every host-bound manager in one process so
// independently bounded publication builds cannot multiply their peak memory.
type ConstructionLimiter struct{ slots chan struct{} }

// NewConstructionLimiter creates the process-wide admission semaphore for
// bounded publication construction.
func NewConstructionLimiter() *ConstructionLimiter {
	return &ConstructionLimiter{slots: make(chan struct{}, MaxConcurrentConstructions)}
}

// acquire admits bounded construction work and returns a release callback,
// failing when the process limit or context prevents admission.
func (limiter *ConstructionLimiter) acquire(ctx context.Context) (func(), error) {
	if limiter == nil {
		return nil, errors.New("publication construction limiter is required")
	}
	select {
	case limiter.slots <- struct{}{}:
		return func() { <-limiter.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type constructionBudget struct {
	workUnits int
	ardBytes  int
	aiBytes   int
}

// newConstructionBudget accounts for the fixed Registry entry and host envelope
// before adding release entries.
func newConstructionBudget(registry Entry, host AICatalogHost) (constructionBudget, error) {
	ard, err := json.Marshal(ARDManifest{Entries: []Entry{registry}})
	if err != nil {
		return constructionBudget{}, catalog.Wrap(catalog.CodeInternal, "measure base ARD publication document", err)
	}
	ai, err := json.Marshal(AICatalog{SpecVersion: "1.0", Host: host, Entries: []AICatalogEntry{}})
	if err != nil {
		return constructionBudget{}, catalog.Wrap(catalog.CodeInternal, "measure base AI Catalog publication document", err)
	}
	return constructionBudget{ardBytes: len(ard), aiBytes: len(ai)}, nil
}

// addRecord charges nested record work against the aggregate construction
// ceiling and honors cancellation.
func (budget *constructionBudget) addRecord(ctx context.Context, record catalog.CatalogRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	units := 1 + len(record.VersionRevision.Representations) + len(record.VersionRevision.Findings) + len(record.ListingRevision.Tags) + len(record.ListingRevision.Categories) + len(record.ListingRevision.RepresentativeQueries) + len(record.ListingRevision.Relationships) + len(record.Evidence.Findings) + len(record.Evidence.ExternalRetrievals)
	budget.workUnits += units
	if budget.workUnits > MaxPublicationWorkUnits {
		return catalog.E(catalog.CodeWorkLimitExceeded, "publication construction exceeds the supported aggregate-work limit")
	}
	return nil
}

// addEntries accounts for exact serialized entry bytes, including separators,
// before accepting another publication entry.
func (budget *constructionBudget) addEntries(ctx context.Context, ard *Entry, ai AICatalogEntry, aiIndex int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	aiRaw, err := json.Marshal(ai)
	if err != nil {
		return catalog.Wrap(catalog.CodeInternal, "measure AI Catalog publication entry", err)
	}
	budget.aiBytes += len(aiRaw)
	if aiIndex > 0 {
		budget.aiBytes++
	}
	if budget.aiBytes > MaxPublicationDocumentBytes {
		return catalog.E(catalog.CodeWorkLimitExceeded, "AI Catalog publication document exceeds the supported encoded-byte limit")
	}
	if ard != nil {
		ardRaw, err := json.Marshal(*ard)
		if err != nil {
			return catalog.Wrap(catalog.CodeInternal, "measure ARD publication entry", err)
		}
		budget.ardBytes += len(ardRaw) + 1
		if budget.ardBytes > MaxPublicationDocumentBytes {
			return catalog.E(catalog.CodeWorkLimitExceeded, "ARD publication document exceeds the supported encoded-byte limit")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// ValidateSnapshotBounds rejects oversized retained state before structural
// validation or persistence allocates maps proportional to attacker-controlled
// entry collections.
func ValidateSnapshotBounds(snapshot Snapshot) error {
	if len(snapshot.ARD.Entries) > MaxPublicationARDEntries || len(snapshot.AICatalog.Entries) > MaxPublicationAICatalogEntries || len(snapshot.Exclusions) > MaxPublicationExclusions || len(snapshot.ArtifactIDs) > MaxPublicationArtifactReferences {
		return catalog.E(catalog.CodeWorkLimitExceeded, "publication snapshot exceeds the supported entry limit")
	}
	ardRaw, err := json.Marshal(snapshot.ARD)
	if err != nil {
		return catalog.Wrap(catalog.CodePublicationNotReady, "encode ARD publication document", err)
	}
	if len(ardRaw) > MaxPublicationDocumentBytes {
		return catalog.E(catalog.CodeWorkLimitExceeded, "ARD publication document exceeds the supported encoded-byte limit")
	}
	aiRaw, err := json.Marshal(snapshot.AICatalog)
	if err != nil {
		return catalog.Wrap(catalog.CodePublicationNotReady, "encode AI Catalog publication document", err)
	}
	if len(aiRaw) > MaxPublicationDocumentBytes {
		return catalog.E(catalog.CodeWorkLimitExceeded, "AI Catalog publication document exceeds the supported encoded-byte limit")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return catalog.Wrap(catalog.CodePublicationNotReady, "encode publication snapshot", err)
	}
	if len(raw) > MaxPublicationSnapshotBytes {
		return catalog.E(catalog.CodeWorkLimitExceeded, "publication snapshot exceeds the supported retained-byte limit")
	}
	return nil
}

// ValidateEncodedSnapshotBounds protects corrupt retained rows before JSON decoding.
func ValidateEncodedSnapshotBounds(raw []byte) error {
	if len(raw) > MaxPublicationSnapshotBytes {
		return catalog.E(catalog.CodeWorkLimitExceeded, "publication snapshot exceeds the supported retained-byte limit")
	}
	return nil
}

// ValidateSnapshotIntegrity verifies immutable retained publication evidence
// without consulting today's host, profile, publisher, or eligibility policy.
// Restore qualification uses this narrower contract so valid history is not
// rejected merely because deployment configuration has since changed.
func ValidateSnapshotIntegrity(snapshot Snapshot) error {
	if err := ValidateSnapshotBounds(snapshot); err != nil {
		return err
	}
	if snapshot.ID == "" || snapshot.TenantID == "" || snapshot.Host == "" || snapshot.Generation < 1 || snapshot.ARDProfile == "" || snapshot.AICatalogProfile == "" || snapshot.CreatedAt.IsZero() || snapshot.Audience != AudienceAnonymous && snapshot.Audience != AudienceAuthenticated {
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot identity or generation metadata is invalid")
	}
	switch snapshot.State {
	case SnapshotCandidate:
		if snapshot.ActivatedAt != nil {
			return catalog.E(catalog.CodePublicationNotReady, "candidate publication snapshot has activation evidence")
		}
	case SnapshotActive, SnapshotRetired:
		if snapshot.ActivatedAt == nil || snapshot.ActivatedAt.Before(snapshot.CreatedAt) {
			return catalog.E(catalog.CodePublicationNotReady, "publication snapshot activation evidence is invalid")
		}
	default:
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot state is invalid")
	}
	ardRaw, err := standards.MarshalAndValidateARDManifest(snapshot.ARD)
	if err != nil {
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot is not a conforming ARD manifest")
	}
	ardSum := sha256.Sum256(ardRaw)
	if snapshot.ETag != `"`+hex.EncodeToString(ardSum[:])+`"` || snapshot.ARDContentDigest != "sha-256=:"+base64.StdEncoding.EncodeToString(ardSum[:])+":" {
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot ARD digest metadata is inconsistent")
	}
	aiRaw, err := json.Marshal(snapshot.AICatalog)
	if err != nil || standards.ValidateAICatalogLevel2(aiRaw) != nil {
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot is not a conforming AI Catalog Level 2 document")
	}
	aiSum := sha256.Sum256(aiRaw)
	if snapshot.AICatalogETag != `"`+hex.EncodeToString(aiSum[:])+`"` || snapshot.AIContentDigest != "sha-256=:"+base64.StdEncoding.EncodeToString(aiSum[:])+":" {
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot AI Catalog digest metadata is inconsistent")
	}
	aiEntries := make(map[string]AICatalogEntry, len(snapshot.AICatalog.Entries))
	artifactIDs := make([]string, 0, len(snapshot.ArtifactIDs))
	for _, entry := range snapshot.AICatalog.Entries {
		metadata, ok := entry.Extensions[aiCatalogMetadataExtension].(map[string]any)
		releaseID, releaseOK := metadata[catalogReleaseIDMetadata].(string)
		if !ok || !releaseOK || releaseID == "" {
			return catalog.E(catalog.CodePublicationNotReady, "AI Catalog snapshot release metadata is invalid")
		}
		if _, duplicate := aiEntries[releaseID]; duplicate {
			return catalog.E(catalog.CodePublicationNotReady, "AI Catalog snapshot contains a duplicate release")
		}
		aiEntries[releaseID] = entry
		if artifactID, managed, artifactErr := retainedArtifactID(snapshot.Host, entry.URL); artifactErr != nil {
			return catalog.E(catalog.CodePublicationNotReady, "AI Catalog snapshot artifact reference is invalid")
		} else if managed {
			artifactIDs = append(artifactIDs, artifactID)
		}
	}
	registryCount := 0
	seenARD := make(map[string]struct{}, len(snapshot.ARD.Entries))
	for _, entry := range snapshot.ARD.Entries {
		releaseID, ok := entry.Metadata[catalogReleaseIDMetadata].(string)
		if !ok || releaseID == "" {
			return catalog.E(catalog.CodePublicationNotReady, "ARD snapshot release metadata is invalid")
		}
		if releaseID == "registry:self" {
			registryCount++
			if !retainedRegistryURL(snapshot.Host, entry.URL) {
				return catalog.E(catalog.CodePublicationNotReady, "ARD snapshot Registry reference is invalid")
			}
			continue
		}
		if _, duplicate := seenARD[releaseID]; duplicate {
			return catalog.E(catalog.CodePublicationNotReady, "ARD snapshot contains a duplicate release")
		}
		aiEntry, exists := aiEntries[releaseID]
		if !exists || !sameAICatalogEntry(toAICatalogEntry(entry), aiEntry) {
			return catalog.E(catalog.CodePublicationNotReady, "ARD and AI Catalog snapshot membership is inconsistent")
		}
		seenARD[releaseID] = struct{}{}
		if artifactID, managed, artifactErr := retainedArtifactID(snapshot.Host, entry.URL); artifactErr != nil {
			return catalog.E(catalog.CodePublicationNotReady, "ARD snapshot artifact reference is invalid")
		} else if managed {
			artifactIDs = append(artifactIDs, artifactID)
		}
	}
	// ARD requires representative discovery queries, while AI Catalog does not.
	// Consequently every non-registry ARD entry must exist in AI Catalog, but a
	// valid AI Catalog snapshot may deliberately contain additional releases.
	if registryCount != 1 || !slices.Equal(snapshot.ArtifactIDs, sortedUniqueStrings(snapshot.ArtifactIDs)) || !slices.Equal(snapshot.ArtifactIDs, sortedUniqueStrings(artifactIDs)) {
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot membership metadata is inconsistent")
	}
	return nil
}

// retainedRegistryURL checks that a Registry self-reference remains bound to
// the retained publication host.
func retainedRegistryURL(host, value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.User == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && strings.EqualFold(parsed.Hostname(), host) && parsed.Path == "/ard/v1/" && parsed.RawQuery == "" && parsed.Fragment == ""
}

// retainedArtifactID distinguishes local artifact references and validates
// their host/path identity.
func retainedArtifactID(host, value string) (string, bool, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", false, errors.New("publication snapshot URL is invalid")
	}
	if !strings.EqualFold(parsed.Hostname(), host) || !strings.HasPrefix(parsed.Path, "/public/v1/artifacts/") {
		return "", false, nil
	}
	artifactID := strings.TrimPrefix(parsed.Path, "/public/v1/artifacts/")
	if parsed.User != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.RawQuery != "" || parsed.Fragment != "" || artifactID == "" || len(artifactID) > 512 || strings.ContainsRune(artifactID, '/') || strings.IndexFunc(artifactID, func(character rune) bool { return character < 0x20 || character == 0x7f }) >= 0 {
		return "", true, errors.New("managed artifact URL is invalid")
	}
	return artifactID, true, nil
}
