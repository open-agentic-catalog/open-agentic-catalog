// Publication management builds immutable discovery documents, checks current
// release eligibility and serves the selected audience's active snapshot.

package publication

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

type RecordSource interface {
	PublicationRecords(context.Context, string, bool, int) ([]catalog.CatalogRecord, bool, error)
}

type Clock interface{ Now() time.Time }
type IDGenerator interface {
	New(time.Time) (string, error)
}

// Options binds one publication manager to one exact discovery host, tenant
// audience, profile pair, and immutable snapshot store.
type Options struct {
	Ratings                   ratings.Store
	RatingsSharingEnabled     bool
	RatingsMinimumSharedCount int
	ArtworkAllowed            func(artwork.Descriptor) bool
	Semantic                  *catalog.SemanticSearch
	Records                   RecordSource
	Store                     Store
	Clock                     Clock
	IDs                       IDGenerator
	BaseURL                   string
	PublisherDomain           string
	ARDProfile                string
	AICatalogProfile          string
	CursorKey                 []byte
	DisplayName               string
	Host                      string
	Audience                  Audience
	Evidence                  EvidenceSink
	// ConstructionLimiter is shared across host managers in one process.
	ConstructionLimiter *ConstructionLimiter
	// AcknowledgeOutboxAtCreation makes public evidence terminal when webhook
	// delivery is deliberately disabled.
	AcknowledgeOutboxAtCreation bool
}

// Manager builds and serves immutable ARD and AI Catalog publication snapshots
// for one configured discovery-host binding.
type Manager struct {
	ratings                     ratings.Store
	ratingsSharingEnabled       bool
	ratingsMinimumSharedCount   int
	artworkAllowed              func(artwork.Descriptor) bool
	semantic                    *catalog.SemanticSearch
	records                     RecordSource
	store                       Store
	clock                       Clock
	ids                         IDGenerator
	baseURL                     string
	publisherDomain             string
	ardProfile                  string
	aiCatalogProfile            string
	cursorKey                   []byte
	displayName                 string
	host                        string
	audience                    Audience
	evidence                    EvidenceSink
	acknowledgeOutboxAtCreation bool
	constructionLimiter         *ConstructionLimiter
}

const cursorTTL = 15 * time.Minute
const aiCatalogMetadataExtension = "https://ai-catalog.org/extensions/metadata"
const catalogReleaseIDMetadata = "catalogReleaseId"
const catalogCreatedAtMetadata = "catalogCreatedAt"
const maxStructuredFilters = 20
const MaxARDPageSize = 100

// NewManager validates publication dependencies, host configuration and
// construction limits before serving the configured audience.
func NewManager(options Options) (*Manager, error) {
	if options.Host == "" {
		options.Host = options.PublisherDomain
	}
	if options.Audience == "" {
		options.Audience = AudienceAnonymous
	}
	if options.Records == nil || options.Store == nil || options.Clock == nil || options.IDs == nil || len(options.CursorKey) < 32 || options.BaseURL == "" || options.PublisherDomain == "" || options.ARDProfile == "" || options.AICatalogProfile == "" || options.Host == "" || options.Audience != AudienceAnonymous && options.Audience != AudienceAuthenticated {
		return nil, catalog.E(catalog.CodeInvalidArgument, "publication manager dependencies and profiles are required")
	}
	if options.RatingsMinimumSharedCount == 0 {
		options.RatingsMinimumSharedCount = 5
	}
	if options.RatingsMinimumSharedCount < 5 || options.RatingsMinimumSharedCount > 1000 || options.RatingsSharingEnabled && options.Ratings == nil {
		return nil, catalog.E(catalog.CodeInvalidArgument, "rating sharing configuration is invalid")
	}
	if options.DisplayName == "" {
		options.DisplayName = "Open Agentic Catalog"
	}
	if options.ConstructionLimiter == nil {
		options.ConstructionLimiter = NewConstructionLimiter()
	}
	return &Manager{ratings: options.Ratings, ratingsSharingEnabled: options.RatingsSharingEnabled, ratingsMinimumSharedCount: options.RatingsMinimumSharedCount, artworkAllowed: options.ArtworkAllowed, semantic: options.Semantic, records: options.Records, store: options.Store, clock: options.Clock, ids: options.IDs, baseURL: strings.TrimSuffix(options.BaseURL, "/"), publisherDomain: options.PublisherDomain, ardProfile: options.ARDProfile, aiCatalogProfile: options.AICatalogProfile, cursorKey: append([]byte(nil), options.CursorKey...), displayName: options.DisplayName, host: options.Host, audience: options.Audience, evidence: options.Evidence, acknowledgeOutboxAtCreation: options.AcknowledgeOutboxAtCreation, constructionLimiter: options.ConstructionLimiter}, nil
}

// BuildCandidate projects eligible releases into bounded immutable documents.
// Invalid records are recorded as exclusions rather than partially published.
func (m *Manager) BuildCandidate(ctx context.Context, tenantID string) (response Snapshot, resultErr error) {
	ctx = identity.WithPermissionContext(ctx)
	if err := m.authorize(ctx, tenantID, "publication.generate"); err != nil {
		return Snapshot{}, err
	}
	ctx, endTrace := catalog.StartTrace(ctx, "publication build", catalog.TraceInternal, "")
	defer func() {
		if resultErr != nil {
			endTrace("error")
		} else {
			endTrace("ok")
		}
	}()
	releaseConstruction, err := m.constructionLimiter.acquire(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer releaseConstruction()
	records, overflow, err := m.publicationRecords(ctx, tenantID)
	if err != nil {
		return Snapshot{}, err
	}
	if overflow || len(records) > catalog.MaxPublicationRecords {
		return Snapshot{}, catalog.E(catalog.CodeWorkLimitExceeded, "eligible publication records exceed the supported limit")
	}
	generation, err := m.store.NextGeneration(ctx, tenantID, m.host)
	if err != nil {
		return Snapshot{}, err
	}
	ardEntries := make([]Entry, 0, len(records)+1)
	aiEntries := make([]AICatalogEntry, 0, len(records))
	exclusions := make([]Exclusion, 0)
	artifacts := make([]string, 0)
	artifactSet := make(map[string]struct{}, len(records))
	registryEntry := m.registryEntry()
	aiHost := AICatalogHost{DisplayName: m.displayName, Identifier: "https://" + m.publisherDomain}
	budget, err := newConstructionBudget(registryEntry, aiHost)
	if err != nil {
		return Snapshot{}, err
	}
	ardEntries = append(ardEntries, registryEntry)
	now := catalog.TransitionTime(m.clock.Now())
	shared, err := m.sharedRatings(ctx, tenantID, records, now)
	if err != nil {
		return Snapshot{}, err
	}
	for _, record := range records {
		if err := budget.addRecord(ctx, record); err != nil {
			return Snapshot{}, err
		}
		entry, artifactID, reasons := m.baseEntry(record)
		attachRatingSummary(&entry, shared[ratings.Key(publicationRatingTarget(record))])
		if len(reasons) > 0 {
			if len(exclusions) == MaxPublicationExclusions {
				return Snapshot{}, catalog.E(catalog.CodeWorkLimitExceeded, "publication exclusions exceed the supported limit")
			}
			exclusions = append(exclusions, Exclusion{ResourceID: record.Resource.ID, Identifier: record.Resource.CanonicalIdentifier, Profiles: []string{m.ardProfile, m.aiCatalogProfile}, Reasons: reasons})
			continue
		}
		aiEntry := toAICatalogEntry(entry)
		var ardEntry *Entry
		if validRepresentativeQueries(record.ListingRevision.RepresentativeQueries) {
			entry.RepresentativeQueries = append([]string(nil), record.ListingRevision.RepresentativeQueries...)
			ardEntry = &entry
		} else {
			if len(exclusions) == MaxPublicationExclusions {
				return Snapshot{}, catalog.E(catalog.CodeWorkLimitExceeded, "publication exclusions exceed the supported limit")
			}
			exclusions = append(exclusions, Exclusion{ResourceID: record.Resource.ID, Identifier: record.Resource.CanonicalIdentifier, Profiles: []string{m.ardProfile}, Reasons: []string{"REPRESENTATIVE_QUERIES_REQUIRED"}})
		}
		if err := budget.addEntries(ctx, ardEntry, aiEntry, len(aiEntries)); err != nil {
			return Snapshot{}, err
		}
		aiEntries = append(aiEntries, aiEntry)
		if ardEntry != nil {
			ardEntries = append(ardEntries, *ardEntry)
		}
		if _, exists := artifactSet[artifactID]; artifactID != "" && !exists {
			if len(artifacts) == MaxPublicationArtifactReferences {
				return Snapshot{}, catalog.E(catalog.CodeWorkLimitExceeded, "publication artifact references exceed the supported limit")
			}
			artifactSet[artifactID] = struct{}{}
			artifacts = append(artifacts, artifactID)
		}
	}
	sort.Slice(ardEntries, func(i, j int) bool {
		if ardEntries[i].Identifier != ardEntries[j].Identifier {
			return ardEntries[i].Identifier < ardEntries[j].Identifier
		}
		return ardEntries[i].Version < ardEntries[j].Version
	})
	sort.Slice(aiEntries, func(i, j int) bool {
		if aiEntries[i].Identifier != aiEntries[j].Identifier {
			return aiEntries[i].Identifier < aiEntries[j].Identifier
		}
		return aiEntries[i].Version < aiEntries[j].Version
	})
	sort.Strings(artifacts)
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	// Use the same precision as activation and PostgreSQL before retaining
	// creation evidence; a clock backstep must not round activation below it.
	idValue, err := m.ids.New(now)
	if err != nil {
		return Snapshot{}, catalog.Wrap(catalog.CodeInternal, "create publication snapshot identifier", err)
	}
	snapshot := Snapshot{ID: idValue, TenantID: tenantID, Host: m.host, Audience: m.audience, Generation: generation, ARDProfile: m.ardProfile, AICatalogProfile: m.aiCatalogProfile, State: SnapshotCandidate, ARD: ARDManifest{Entries: ardEntries}, AICatalog: AICatalog{SpecVersion: "1.0", Host: aiHost, Entries: aiEntries}, Exclusions: exclusions, ArtifactIDs: artifacts, CreatedAt: now}
	raw, err := standards.MarshalAndValidateARDManifest(snapshot.ARD)
	if err != nil {
		return Snapshot{}, catalog.Wrap(catalog.CodeInternal, "validate ARD candidate", err)
	}
	sum := sha256.Sum256(raw)
	snapshot.ETag = `"` + hex.EncodeToString(sum[:]) + `"`
	snapshot.ARDContentDigest = "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
	aiRaw, err := json.Marshal(snapshot.AICatalog)
	if err != nil {
		return Snapshot{}, catalog.Wrap(catalog.CodeInternal, "marshal AI Catalog candidate", err)
	}
	if err := standards.ValidateAICatalogLevel2(aiRaw); err != nil {
		return Snapshot{}, catalog.Wrap(catalog.CodeInternal, "validate AI Catalog candidate", err)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	aiSum := sha256.Sum256(aiRaw)
	snapshot.AICatalogETag = `"` + hex.EncodeToString(aiSum[:]) + `"`
	snapshot.AIContentDigest = "sha-256=:" + base64.StdEncoding.EncodeToString(aiSum[:]) + ":"
	if err := ValidateSnapshotBounds(snapshot); err != nil {
		return Snapshot{}, err
	}
	if err := m.validateSnapshotStructure(snapshot); err != nil {
		return Snapshot{}, err
	}
	evidence, err := m.candidateEvidence(ctx, snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	return m.store.SaveCandidate(ctx, snapshot, evidence)
}

// Activate rechecks candidate eligibility and atomically activates it with
// evidence fenced to the previously active snapshot.
func (m *Manager) Activate(ctx context.Context, tenantID, snapshotID, reason string) (response Snapshot, resultErr error) {
	ctx = identity.WithPermissionContext(ctx)
	if err := m.authorize(ctx, tenantID, "publication.activate"); err != nil {
		return Snapshot{}, err
	}
	ctx, endTrace := catalog.StartTrace(ctx, "publication activate", catalog.TraceInternal, "")
	defer func() {
		outcome := "ok"
		if resultErr != nil {
			outcome = "error"
		}
		endTrace(outcome)
	}()
	reason, err := audit.NormalizeReason(audit.ActionPublicationActivated, reason)
	if err != nil {
		return Snapshot{}, catalog.Wrap(catalog.CodeInvalidArgument, "publication activation reason is invalid", err)
	}
	releaseConstruction, err := m.constructionLimiter.acquire(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer releaseConstruction()
	snapshot, err := m.store.Get(ctx, tenantID, snapshotID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := m.validateSnapshotEligibility(ctx, snapshot); err != nil {
		return Snapshot{}, err
	}
	priorID, err := m.currentActiveID(ctx, tenantID)
	if err != nil {
		return Snapshot{}, err
	}
	at := activationTime(m.clock.Now(), snapshot)
	active := snapshot
	active.State = SnapshotActive
	active.ActivatedAt = &at
	evidence, err := m.transitionEvidence(ctx, active, audit.ActionPublicationActivated, reason, priorID)
	if err != nil {
		return Snapshot{}, err
	}
	evidence.ExpectedActiveID = &priorID
	return m.store.Activate(ctx, tenantID, snapshotID, at, evidence)
}

// Unpublish requires a bounded reason and retires the active snapshot with
// withdrawal evidence.
func (m *Manager) Unpublish(ctx context.Context, tenantID, reason string) (Snapshot, error) {
	ctx = identity.WithPermissionContext(ctx)
	if err := m.authorize(ctx, tenantID, "publication.unpublish"); err != nil {
		return Snapshot{}, err
	}
	reason, err := audit.NormalizeRequiredReason(reason)
	if err != nil {
		return Snapshot{}, catalog.Wrap(catalog.CodeInvalidArgument, "publication unpublish reason is invalid", err)
	}
	releaseConstruction, err := m.constructionLimiter.acquire(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer releaseConstruction()
	snapshot, err := m.store.Active(ctx, tenantID)
	if err != nil {
		return Snapshot{}, err
	}
	at := m.clock.Now()
	retired := snapshot
	retired.State = SnapshotRetired
	evidence, err := m.withdrawalEvidence(ctx, tenantID, retired, reason, WithdrawalCause{Cause: "explicit_unpublish", OriginatingAction: "publication.unpublish", OriginatingTargetID: snapshot.ID}, true)
	if err != nil {
		return Snapshot{}, err
	}
	expected := snapshot.ID
	evidence.ExpectedActiveID = &expected
	return m.store.Deactivate(ctx, tenantID, snapshot.ID, at, evidence)
}

// Rollback rechecks a retained snapshot against current eligibility before
// restoring it under an active-pointer precondition.
func (m *Manager) Rollback(ctx context.Context, tenantID, snapshotID, reason string) (Snapshot, error) {
	ctx = identity.WithPermissionContext(ctx)
	if err := m.authorize(ctx, tenantID, "publication.rollback"); err != nil {
		return Snapshot{}, err
	}
	reason, err := audit.NormalizeReason(audit.ActionPublicationRolledBack, reason)
	if err != nil {
		return Snapshot{}, catalog.Wrap(catalog.CodeInvalidArgument, "publication rollback reason is invalid", err)
	}
	releaseConstruction, err := m.constructionLimiter.acquire(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer releaseConstruction()
	snapshot, err := m.store.Get(ctx, tenantID, snapshotID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := m.validateSnapshotEligibility(ctx, snapshot); err != nil {
		return Snapshot{}, err
	}
	priorID, err := m.currentActiveID(ctx, tenantID)
	if err != nil {
		return Snapshot{}, err
	}
	at := activationTime(m.clock.Now(), snapshot)
	active := snapshot
	active.State = SnapshotActive
	active.ActivatedAt = &at
	evidence, err := m.transitionEvidence(ctx, active, audit.ActionPublicationRolledBack, reason, priorID)
	if err != nil {
		return Snapshot{}, err
	}
	evidence.ExpectedActiveID = &priorID
	return m.store.Rollback(ctx, tenantID, snapshotID, at, evidence)
}

// Reactivation must advance at storage precision even when the wall clock has
// moved backwards or two transitions occur within the same microsecond.
func activationTime(now time.Time, snapshot Snapshot) time.Time {
	at := catalog.TransitionTime(now, snapshot.CreatedAt)
	if snapshot.ActivatedAt != nil {
		at = catalog.TransitionTime(at, snapshot.ActivatedAt.Add(time.Microsecond))
	}
	return at
}

// Active returns only a structurally valid active snapshot whose retained
// tenant and state agree.
func (m *Manager) Active(ctx context.Context, tenantID string) (Snapshot, error) {
	snapshot, err := m.store.Active(ctx, tenantID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := m.validateSnapshotStructure(snapshot); err != nil {
		return Snapshot{}, err
	}
	if snapshot.TenantID != tenantID || snapshot.State != SnapshotActive {
		return Snapshot{}, catalog.E(catalog.CodePublicationNotReady, "active publication snapshot is bound to inconsistent tenant or state metadata")
	}
	return snapshot, nil
}

// ListSnapshotSummaries validates generation pagination and selects one extra
// summary for continuation detection.
func (m *Manager) ListSnapshotSummaries(ctx context.Context, tenantID string, request SnapshotListRequest) ([]SnapshotSummary, error) {
	ctx = identity.WithPermissionContext(ctx)
	if err := m.authorize(ctx, tenantID, "publication.read"); err != nil {
		return nil, err
	}
	if request.Limit < 1 || request.Limit > MaxARDPageSize || request.AfterGeneration < 0 || request.AfterGeneration == 0 && request.AfterID != "" || request.AfterGeneration > 0 && request.AfterID == "" {
		return nil, catalog.E(catalog.CodeInvalidArgument, "publication-snapshot page position or limit is invalid")
	}
	if request.State != "" && request.State != SnapshotCandidate && request.State != SnapshotActive && request.State != SnapshotRetired {
		return nil, catalog.E(catalog.CodeInvalidArgument, "publication-snapshot state is invalid")
	}
	request.Limit++
	return m.store.ListSummaries(ctx, tenantID, request)
}

// GetSnapshot reads the exact retained snapshot from the explicit tenant;
// callers own administrative authorization.
func (m *Manager) GetSnapshot(ctx context.Context, tenantID, snapshotID string) (Snapshot, error) {
	ctx = identity.WithPermissionContext(ctx)
	if err := m.authorize(ctx, tenantID, "publication.read"); err != nil {
		return Snapshot{}, err
	}
	return m.store.Get(ctx, tenantID, snapshotID)
}

type WithdrawalCause struct {
	Cause               string
	OriginatingAction   string
	OriginatingTargetID string
}

// WithdrawDomain retires an active snapshot affected by a revoked publisher
// domain and reports whether withdrawal occurred.
func (m *Manager) WithdrawDomain(ctx context.Context, tenantID, domain string, cause WithdrawalCause) (bool, error) {
	return m.withdrawMatching(ctx, tenantID, func(snapshot Snapshot) bool {
		return snapshotContainsPublisherDomain(snapshot, domain)
	}, cause)
}

// WithdrawRelease retires an active snapshot affected by a withdrawn release
// and reports whether withdrawal occurred.
func (m *Manager) WithdrawRelease(ctx context.Context, tenantID, releaseID string, cause WithdrawalCause) (bool, error) {
	return m.withdrawMatching(ctx, tenantID, func(snapshot Snapshot) bool {
		return snapshotContainsRelease(snapshot, releaseID)
	}, cause)
}

// withdrawMatching retires a matching active snapshot with cause evidence;
// inconsistent retained structure triggers fail-closed withdrawal.
func (m *Manager) withdrawMatching(ctx context.Context, tenantID string, matches func(Snapshot) bool, cause WithdrawalCause) (bool, error) {
	releaseConstruction, err := m.constructionLimiter.acquire(ctx)
	if err != nil {
		return false, err
	}
	defer releaseConstruction()
	snapshot, err := m.store.Active(ctx, tenantID)
	if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A structurally inconsistent active snapshot cannot safely prove that a
	// revoked release or domain is absent, so emergency withdrawal fails closed.
	structureErr := m.validateSnapshotStructure(snapshot)
	if structureErr == nil && snapshot.TenantID == tenantID && snapshot.State == SnapshotActive && !matches(snapshot) {
		return false, nil
	}
	retired := snapshot
	retired.State = SnapshotRetired
	evidence, err := m.withdrawalEvidence(ctx, tenantID, retired, "", cause, structureErr == nil)
	if err != nil {
		return false, err
	}
	expected := snapshot.ID
	evidence.ExpectedActiveID = &expected
	if _, err := m.store.Deactivate(ctx, tenantID, snapshot.ID, m.clock.Now(), evidence); err != nil {
		return false, err
	}
	return true, nil
}

// currentActiveID returns the tenant's active snapshot ID, treating an absent
// active snapshot as an empty pointer.
func (m *Manager) currentActiveID(ctx context.Context, tenantID string) (string, error) {
	active, err := m.store.Active(ctx, tenantID)
	if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return active.ID, nil
}

// snapshotContainsRelease reports whether retained publication membership
// refers to the selected release.
func snapshotContainsRelease(snapshot Snapshot, releaseID string) bool {
	for _, entry := range snapshot.ARD.Entries {
		if value, _ := entry.Metadata[catalogReleaseIDMetadata].(string); value == releaseID {
			return true
		}
	}
	for _, entry := range snapshot.AICatalog.Entries {
		metadata, _ := entry.Extensions[aiCatalogMetadataExtension].(map[string]any)
		if value, _ := metadata[catalogReleaseIDMetadata].(string); value == releaseID {
			return true
		}
	}
	return false
}

// snapshotContainsPublisherDomain reports whether retained entries use the
// selected AIR publisher domain.
func snapshotContainsPublisherDomain(snapshot Snapshot, domain string) bool {
	for _, entry := range snapshot.ARD.Entries {
		releaseID, _ := entry.Metadata[catalogReleaseIDMetadata].(string)
		if releaseID == "" || releaseID == "registry:self" {
			continue
		}
		identifier, err := catalog.ParseAIRIdentifier(entry.Identifier)
		if err == nil && identifier.Publisher == domain {
			return true
		}
	}
	for _, entry := range snapshot.AICatalog.Entries {
		identifier, err := catalog.ParseAIRIdentifier(entry.Identifier)
		if err == nil && identifier.Publisher == domain {
			return true
		}
	}
	return false
}

// baseEntry selects a public-compatible primary representation and returns its
// discovery entry, artifact reference and exclusion reasons.
func (m *Manager) baseEntry(record catalog.CatalogRecord) (Entry, string, []string) {
	reasons := make([]string, 0)
	if m.audience == AudienceAnonymous && record.ListingRevision.Visibility != catalog.VisibilityPublic {
		reasons = append(reasons, "LISTING_NOT_PUBLIC")
	}
	if m.audience == AudienceAuthenticated && record.ListingRevision.Visibility != catalog.VisibilityPublic && record.ListingRevision.Visibility != catalog.VisibilityTenant {
		reasons = append(reasons, "LISTING_NOT_TENANT_VISIBLE")
	}
	_, err := catalog.ParseAIRIdentifier(record.Resource.CanonicalIdentifier)
	if err != nil {
		reasons = append(reasons, "AIR_IDENTIFIER_REQUIRED")
	}
	if record.Evidence.State != catalog.EvidenceComplete || record.Evidence.SubjectDigest != record.VersionRevision.ContentSetDigest || !record.Evidence.ExpiresAt.IsZero() && !record.Evidence.ExpiresAt.After(m.clock.Now()) {
		reasons = append(reasons, "PUBLICATION_EVIDENCE_BLOCKED")
	}
	var selected *catalog.Representation
	for index := range record.VersionRevision.Representations {
		representation := &record.VersionRevision.Representations[index]
		if representation.Role != catalog.RepresentationPrimary {
			continue
		}
		switch record.Resource.Kind {
		case catalog.KindPlugin:
			if catalog.IsPluginMediaType(representation.MediaType) && representation.Plugin != nil {
				selected = representation
			}
		case catalog.KindAgent:
			if representation.MediaType == adapter.MediaA2AAgentCard {
				selected = representation
			}
		case catalog.KindMCPServer:
			if representation.MediaType == adapter.MediaMCPServerCard {
				selected = representation
			}
		case catalog.KindSkill:
			if adapter.IsSkillMarkdownMediaType(representation.MediaType) || representation.MediaType == adapter.MediaSkillZIP || representation.MediaType == adapter.MediaSkillGZIP || representation.MediaType == adapter.MediaSkillJSON {
				selected = representation
			}
		}
	}
	if selected == nil || selected.ValidationState != "valid" {
		reasons = append(reasons, "STANDARD_PUBLIC_REPRESENTATION_REQUIRED")
		return Entry{}, "", reasons
	}
	createdAt := record.Release.ActivatedAt
	if createdAt.IsZero() {
		createdAt = record.Release.UpdatedAt
	}
	entry := Entry{Context: ARDContext, ID: record.Resource.CanonicalIdentifier, Identifier: record.Resource.CanonicalIdentifier, DisplayName: record.ListingRevision.DisplayName, Type: adapter.DiscoveryMediaType(selected.MediaType), Description: record.ListingRevision.Description, Tags: append([]string(nil), record.ListingRevision.Tags...), Version: record.Version.Version, UpdatedAt: record.Release.UpdatedAt.UTC().Format(time.RFC3339Nano), Metadata: map[string]any{catalogReleaseIDMetadata: record.Release.ID, catalogCreatedAtMetadata: createdAt.UTC().Format(time.RFC3339Nano)}}
	license := catalog.NewCatalogPresentation("", "", record.ListingRevision.License).License
	if license != "" {
		entry.Metadata["license"] = license
	}
	entry.Subjects = append([]string(nil), record.ListingRevision.Categories...)
	if len(entry.Subjects) > 0 {
		entry.Context = []any{ARDContext, map[string]any{"dcterms": "http://purl.org/dc/terms/"}}
	}
	if presentation := catalog.ListingArtwork(record.ListingRevision.Extensions); presentation != nil && m.artworkAllowed != nil && m.artworkAllowed(*presentation.Descriptor) {
		entry.Artwork = presentation.Descriptor
		contexts := []any{entry.Context}
		if existing, ok := entry.Context.([]any); ok {
			contexts = append([]any(nil), existing...)
		}
		contexts = append(contexts, map[string]any{artwork.IRI: map[string]any{"@id": artwork.IRI, "@type": "@json"}})
		entry.Context = contexts
	}
	artifactID := ""
	if selected.Plugin != nil {
		entry.Metadata["packageDigest"] = selected.ContentDigest
		entry.Metadata["packageSize"] = selected.Size
	}
	switch {
	case len(selected.Data) > 0:
		entry.Data = append(json.RawMessage(nil), selected.Data...)
	case selected.URL != "":
		entry.URL = selected.URL
	case selected.ArtifactID != "":
		artifactID = selected.ArtifactID
		entry.URL = m.baseURL + "/public/v1/artifacts/" + selected.ArtifactID
	default:
		reasons = append(reasons, "PUBLIC_DELIVERY_FORM_REQUIRED")
	}
	return entry, artifactID, reasons
}

// validRepresentativeQueries enforces the producer requirement of two to five
// representative queries.
func validRepresentativeQueries(queries []string) bool {
	return len(queries) >= 2 && len(queries) <= 5
}

// registryEntry builds the host-bound ARD self-entry advertising this Registry
// interface.
func (m *Manager) registryEntry() Entry {
	return Entry{
		Context:               ARDContext,
		ID:                    "urn:air:" + m.publisherDomain + ":registry:open-agentic-catalog",
		Identifier:            "urn:air:" + m.publisherDomain + ":registry:open-agentic-catalog",
		DisplayName:           m.displayName + " Registry",
		Type:                  "application/ai-registry+json",
		URL:                   m.baseURL + "/ard/v1/",
		RepresentativeQueries: []string{"Discover catalog resources", "Search this agentic resource catalog"},
		Description:           "Registry endpoint for discovering public agentic resources.",
		Tags:                  []string{"catalog", "discovery", "registry"},
		Metadata:              map[string]any{catalogReleaseIDMetadata: "registry:self"},
	}
}

// toAICatalogEntry projects shared discovery metadata into AI Catalog fields
// and its namespaced metadata extension.
func toAICatalogEntry(entry Entry) AICatalogEntry {
	metadata := make(map[string]any, len(entry.Metadata))
	for key, value := range entry.Metadata {
		metadata[key] = cloneJSONValue(value)
	}
	mediaType := entry.Type
	if adapter.IsSkillMarkdownMediaType(mediaType) {
		mediaType = adapter.MediaSkillMarkdown
	}
	result := AICatalogEntry{
		Identifier:  entry.Identifier,
		DisplayName: entry.DisplayName,
		Type:        mediaType,
		URL:         entry.URL,
		Data:        append(json.RawMessage(nil), entry.Data...),
		Version:     entry.Version,
		Description: entry.Description,
		Tags:        append([]string(nil), entry.Tags...),
		UpdatedAt:   entry.UpdatedAt,
	}
	if len(metadata) > 0 {
		result.Extensions = map[string]any{aiCatalogMetadataExtension: metadata}
	}
	if entry.Ratings != nil {
		if result.Extensions == nil {
			result.Extensions = make(map[string]any)
		}
		value := map[string]any{"reporter": entry.Ratings.Reporter, "identifier": entry.Ratings.Identifier, "versionPresent": entry.Ratings.VersionPresent, "minimum": entry.Ratings.Minimum, "maximum": entry.Ratings.Maximum, "count": entry.Ratings.Count, "sum": entry.Ratings.Sum, "measuredAt": entry.Ratings.MeasuredAt.UTC().Format(time.RFC3339Nano)}
		if entry.Ratings.Version != "" {
			value["version"] = entry.Ratings.Version
		}
		result.Extensions[ratings.IRI] = value
	}
	if entry.Artwork != nil {
		if result.Extensions == nil {
			result.Extensions = make(map[string]any)
		}
		result.Extensions[artwork.IRI] = entry.Artwork
	}
	return result
}

// validateSnapshotEligibility rechecks retained structure and entry membership
// against the current bounded eligible-release projection.
func (m *Manager) validateSnapshotEligibility(ctx context.Context, snapshot Snapshot) error {
	if err := m.validateSnapshotStructure(snapshot); err != nil {
		return err
	}
	records, overflow, err := m.publicationRecords(ctx, snapshot.TenantID)
	if err != nil {
		return err
	}
	if overflow || len(records) > catalog.MaxPublicationRecords {
		return catalog.E(catalog.CodeWorkLimitExceeded, "eligible publication records exceed the supported limit")
	}
	eligibleARD := make(map[string]Entry, len(records))
	eligibleAI := make(map[string]AICatalogEntry, len(records))
	eligibleArtifacts := make(map[string]string, len(records))
	registryEntry := m.registryEntry()
	eligibleARD["registry:self"] = registryEntry
	budget, err := newConstructionBudget(registryEntry, AICatalogHost{DisplayName: m.displayName, Identifier: "https://" + m.publisherDomain})
	if err != nil {
		return err
	}
	shared, err := m.sharedRatings(ctx, snapshot.TenantID, records, snapshot.CreatedAt)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := budget.addRecord(ctx, record); err != nil {
			return err
		}
		entry, artifactID, reasons := m.baseEntry(record)
		attachRatingSummary(&entry, shared[ratings.Key(publicationRatingTarget(record))])
		if len(reasons) == 0 {
			aiEntry := toAICatalogEntry(entry)
			var ardEntry *Entry
			if validRepresentativeQueries(record.ListingRevision.RepresentativeQueries) {
				entry.RepresentativeQueries = append([]string(nil), record.ListingRevision.RepresentativeQueries...)
				ardEntry = &entry
			}
			if err := budget.addEntries(ctx, ardEntry, aiEntry, len(eligibleAI)); err != nil {
				return err
			}
			eligibleAI[record.Release.ID] = aiEntry
			eligibleArtifacts[record.Release.ID] = artifactID
			if ardEntry != nil {
				eligibleARD[record.Release.ID] = entry
			}
		}
	}
	ardArtifacts, ardReleases, err := validateARDPublicationEntries(snapshot.ARD.Entries, eligibleARD, eligibleArtifacts)
	if err != nil {
		return err
	}
	aiArtifacts, aiReleases, err := validateAICatalogPublicationEntries(snapshot.AICatalog.Entries, eligibleAI, eligibleArtifacts)
	if err != nil {
		return err
	}
	if !slices.Equal(ardReleases, sortedMapKeysFromEntries(eligibleARD)) || !slices.Equal(aiReleases, sortedMapKeysFromEntries(eligibleAI)) {
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot membership no longer matches the eligible release set")
	}
	allArtifacts := append(append([]string(nil), ardArtifacts...), aiArtifacts...)
	allArtifacts = sortedUniqueStrings(allArtifacts)
	if !slices.Equal(allArtifacts, sortedStrings(snapshot.ArtifactIDs)) {
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot representations are inconsistent")
	}
	return nil
}

// validateSnapshotStructure checks document profiles, digests and row/document
// bindings without assuming current release eligibility.
func (m *Manager) validateSnapshotStructure(snapshot Snapshot) error {
	if err := ValidateSnapshotIntegrity(snapshot); err != nil {
		return err
	}
	if snapshot.Host != m.host || snapshot.Audience != m.audience {
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot host or audience does not match its configured delivery boundary")
	}
	if snapshot.ARDProfile != m.ardProfile || snapshot.AICatalogProfile != m.aiCatalogProfile {
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot profiles do not match the configured delivery boundary")
	}
	if snapshot.AICatalog.Host.DisplayName != m.displayName || snapshot.AICatalog.Host.Identifier != "https://"+m.publisherDomain {
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot AI Catalog host metadata is inconsistent")
	}
	if err := m.validateImmutableMembership(snapshot); err != nil {
		return err
	}
	return nil
}

// validateImmutableMembership verifies retained release and artifact membership
// across both discovery documents.
func (m *Manager) validateImmutableMembership(snapshot Snapshot) error {
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
		if artifactID, ok := m.snapshotArtifactID(entry.URL); ok {
			artifactIDs = append(artifactIDs, artifactID)
		}
	}
	registryCount := 0
	ardReleases := make(map[string]struct{}, len(snapshot.ARD.Entries))
	for _, entry := range snapshot.ARD.Entries {
		releaseID, ok := entry.Metadata[catalogReleaseIDMetadata].(string)
		if !ok || releaseID == "" {
			return catalog.E(catalog.CodePublicationNotReady, "ARD snapshot release metadata is invalid")
		}
		if releaseID == "registry:self" {
			registryCount++
			if !samePublicationEntry(entry, m.registryEntry()) {
				return catalog.E(catalog.CodePublicationNotReady, "ARD snapshot Registry entry is inconsistent")
			}
			continue
		}
		if _, duplicate := ardReleases[releaseID]; duplicate {
			return catalog.E(catalog.CodePublicationNotReady, "ARD snapshot contains a duplicate release")
		}
		aiEntry, exists := aiEntries[releaseID]
		if !exists || !sameAICatalogEntry(toAICatalogEntry(entry), aiEntry) {
			return catalog.E(catalog.CodePublicationNotReady, "ARD and AI Catalog snapshot membership is inconsistent")
		}
		ardReleases[releaseID] = struct{}{}
		if artifactID, ok := m.snapshotArtifactID(entry.URL); ok {
			artifactIDs = append(artifactIDs, artifactID)
		}
	}
	if registryCount != 1 {
		return catalog.E(catalog.CodePublicationNotReady, "ARD snapshot must contain exactly one Registry entry")
	}
	if !slices.Equal(snapshot.ArtifactIDs, sortedUniqueStrings(snapshot.ArtifactIDs)) || !slices.Equal(snapshot.ArtifactIDs, sortedUniqueStrings(artifactIDs)) {
		return catalog.E(catalog.CodePublicationNotReady, "publication snapshot artifact allowlist is inconsistent")
	}
	return nil
}

// snapshotArtifactID extracts a valid local artifact identifier only from the
// configured publication origin.
func (m *Manager) snapshotArtifactID(value string) (string, bool) {
	prefix := m.baseURL + "/public/v1/artifacts/"
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	artifactID := strings.TrimPrefix(value, prefix)
	if artifactID == "" || strings.ContainsAny(artifactID, "/?#") {
		return "", false
	}
	return artifactID, true
}

// publicationRecords requests the same bounded, audience-appropriate release
// projection used for construction and activation checks.
func (m *Manager) publicationRecords(ctx context.Context, tenantID string) ([]catalog.CatalogRecord, bool, error) {
	return m.records.PublicationRecords(ctx, tenantID, m.audience == AudienceAuthenticated, catalog.MaxPublicationRecords)
}

// sortedMapKeysFromEntries returns stable membership keys for deterministic
// entry validation.
func sortedMapKeysFromEntries[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// sortedUniqueStrings returns an ordered set used for retained membership
// comparisons.
func sortedUniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

// validateARDPublicationEntries checks each retained ARD entry against eligible
// release content and collects exact release/artifact membership.
func validateARDPublicationEntries(entries []Entry, eligible map[string]Entry, eligibleArtifacts map[string]string) ([]string, []string, error) {
	artifacts := make([]string, 0)
	releaseIDs := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		releaseID, ok := entry.Metadata[catalogReleaseIDMetadata].(string)
		current, exists := eligible[releaseID]
		_, duplicate := releaseIDs[releaseID]
		if !ok || releaseID == "" || duplicate || !exists || !samePublicationEntry(entry, current) {
			return nil, nil, catalog.E(catalog.CodePublicationNotReady, "publication snapshot contains a release that is no longer eligible")
		}
		releaseIDs[releaseID] = struct{}{}
		if artifactID := eligibleArtifacts[releaseID]; artifactID != "" && !slices.Contains(artifacts, artifactID) {
			artifacts = append(artifacts, artifactID)
		}
	}
	sort.Strings(artifacts)
	return artifacts, sortedMapKeys(releaseIDs), nil
}

// validateAICatalogPublicationEntries verifies the same eligible-release and
// artifact bindings in the AI Catalog document.
func validateAICatalogPublicationEntries(entries []AICatalogEntry, eligible map[string]AICatalogEntry, eligibleArtifacts map[string]string) ([]string, []string, error) {
	artifacts := make([]string, 0)
	releaseIDs := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		metadata, ok := entry.Extensions[aiCatalogMetadataExtension].(map[string]any)
		if !ok {
			return nil, nil, catalog.E(catalog.CodePublicationNotReady, "AI Catalog snapshot metadata is invalid")
		}
		releaseID, ok := metadata[catalogReleaseIDMetadata].(string)
		current, exists := eligible[releaseID]
		_, duplicate := releaseIDs[releaseID]
		if !ok || releaseID == "" || duplicate || !exists || !sameAICatalogEntry(entry, current) {
			return nil, nil, catalog.E(catalog.CodePublicationNotReady, "AI Catalog snapshot contains a release that is no longer eligible")
		}
		releaseIDs[releaseID] = struct{}{}
		if artifactID := eligibleArtifacts[releaseID]; artifactID != "" && !slices.Contains(artifacts, artifactID) {
			artifacts = append(artifacts, artifactID)
		}
	}
	sort.Strings(artifacts)
	return artifacts, sortedMapKeys(releaseIDs), nil
}

// sortedMapKeys returns lexically ordered set members for stable publication
// metadata.
func sortedMapKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// sortedStrings copies and orders strings without changing the caller's slice.
func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

// samePublicationEntry compares complete ARD entry content using deterministic
// JSON encoding.
func samePublicationEntry(left, right Entry) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

// sameAICatalogEntry compares complete AI Catalog entry content using
// deterministic JSON encoding.
func sameAICatalogEntry(left, right AICatalogEntry) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

// ArtifactAllowed reports whether the artifact belongs to the tenant's
// structurally valid active snapshot.
func (m *Manager) ArtifactAllowed(ctx context.Context, tenantID, artifactID string) bool {
	snapshot, err := m.Active(ctx, tenantID)
	return err == nil && slices.Contains(snapshot.ArtifactIDs, artifactID)
}

// Search evaluates a bounded discovery query against the tenant's active
// immutable snapshot.
func (m *Manager) Search(ctx context.Context, tenantID string, request SearchRequest) (SearchResponse, error) {
	request.Query.Text = strings.Join(strings.Fields(request.Query.Text), " ")
	if request.Query.Text == "" || utf8.RuneCountInString(request.Query.Text) > 500 {
		return SearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "ARD Search query.text is required and must be at most 500 characters")
	}
	if err := normalizeStructuredQuery(&request.Query); err != nil {
		return SearchResponse{}, err
	}
	if request.Federation == "" {
		request.Federation = "auto"
	}
	if request.Federation != "none" && request.Federation != "referrals" && request.Federation != "auto" {
		return SearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "ARD federation mode is invalid")
	}
	if request.PageSize < 0 {
		return SearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "ARD pageSize must be positive")
	}
	if request.PageSize == 0 {
		request.PageSize = 10
	}
	if request.PageSize > MaxARDPageSize {
		return SearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "ARD pageSize cannot exceed 100")
	}
	snapshot, err := m.Active(ctx, tenantID)
	if err != nil {
		return SearchResponse{}, err
	}
	binding, err := m.cursorBinding(tenantID, "search", struct {
		Query      SearchQuery `json:"query"`
		Federation string      `json:"federation"`
		PageSize   int         `json:"pageSize"`
	}{Query: request.Query, Federation: request.Federation, PageSize: request.PageSize})
	if err != nil {
		return SearchResponse{}, err
	}
	if len(request.PageToken) > catalog.MaxCatalogCursorBytes {
		return SearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "ARD pageToken exceeds its bound")
	}
	binding += "\x00" + identity.DecisionDigest(ctx)
	var liveCursor *catalog.LiveCursor
	prefix, innerToken := semanticTokenParts(request.PageToken)
	baseBinding := binding
	if prefix != "" {
		binding += "\x00" + prefix
	}
	var offset int
	expiresAt := m.clock.Now().Add(cursorTTL).Unix()
	if strings.HasPrefix(request.PageToken, catalog.LiveCursorPrefix) {
		cursor, decodeErr := catalog.DecodeLiveCursor(request.PageToken, "public", m.cursorKey, binding, m.clock.Now())
		if decodeErr != nil {
			return SearchResponse{}, decodeErr
		}
		if request.Federation != "auto" || cursor.Generation != strconv.FormatInt(snapshot.Generation, 10) || cursor.Fingerprint == "" {
			return SearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "ARD live pageToken is stale")
		}
		liveCursor, offset, expiresAt = &cursor, cursor.Offset, cursor.ExpiresAt
		if cursor.Mode == "hybrid" {
			prefix = cursor.Ranking
			binding = baseBinding + "\x00" + prefix
		} else if cursor.Mode != "" {
			return SearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "ARD live pageToken mode is invalid")
		}
	} else {
		offset, err = m.decodeCursor(innerToken, snapshot.Generation, binding)
		if err != nil {
			return SearchResponse{}, err
		}
		if innerToken != "" {
			// decodeCursor already authenticated and validated this payload.
			raw, _ := base64.RawURLEncoding.DecodeString(innerToken)
			expiresAt, _ = strconv.ParseInt(strings.Split(string(raw), ":")[3], 10, 64)
		}
	}
	native, err := projectNativeEntries(ctx, snapshot.ARD.Entries, request.Query.Filter, nil)
	if err != nil {
		return SearchResponse{}, err
	}
	results := make([]SearchResult, 0)
	for i, entry := range snapshot.ARD.Entries {
		if !matchFilter(entry, request.Query.Filter, nativeAt(native, i)) {
			continue
		}
		score := CoreLexicalScore(entry, request.Query.Text)
		if score == 0 {
			continue
		}
		results = append(results, SearchResult{Entry: entry, Score: score, Source: m.baseURL + "/ard/v1/"})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return entryLess(results[i].Entry, results[j].Entry)
	})
	var retrieval catalog.SnapshotRanking
	if request.PageToken == "" || prefix != "" {
		hybrid, ranking, err := m.rankSemantic(ctx, tenantID, snapshot, request.Query, native)
		if err != nil {
			return SearchResponse{}, err
		}
		retrieval = ranking
		if ranking.Profile != "" {
			currentPrefix := "h1:" + strings.TrimPrefix(ranking.Profile, "sha256:") + ":" + ranking.Generation + ":" + ranking.Fingerprint + ":"
			if prefix != "" && prefix != currentPrefix {
				return SearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "ARD hybrid pageToken is stale; restart the search")
			}
			prefix = currentPrefix
			binding = baseBinding + "\x00" + prefix
			results = hybrid
		} else if prefix != "" {
			return SearchResponse{}, catalog.E(catalog.CodeDependencyUnavailable, "ARD hybrid continuation is unavailable; retry or restart the search")
		}
	}
	if offset > len(results) {
		return SearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "ARD pageToken is stale")
	}
	end := min(offset+request.PageSize, len(results))
	local := make(map[string]struct{}, len(results))
	for _, result := range results {
		local[result.Identifier+"\x00"+result.Version] = struct{}{}
	}
	response := SearchResponse{Results: results[offset:end], page: searchPageState{generation: snapshot.Generation, offset: offset, count: end - offset, total: len(results), binding: binding, baseBinding: baseBinding, live: liveCursor, limit: request.PageSize, expiresAt: expiresAt}, local: local}
	response.Tier = "core"
	response.FallbackReason = retrieval.FallbackReason
	response.PartialIndex = retrieval.PartialIndex
	response.page.prefix = prefix
	if retrieval.Profile != "" {
		response.Tier = "hybrid"
	}
	if end < len(results) {
		response.PageToken = prefix + m.encodeCursorAt(snapshot.Generation, end, binding, expiresAt)
	}
	return response, nil
}

// SearchContinuation returns a cursor after the number of local results that
// a caller actually emitted from a Search page. It lets the public auto mode
// interleave live results without losing displaced local results.
func (m *Manager) SearchContinuation(response SearchResponse, emittedLocal int) (string, error) {
	page := response.page
	if page.generation <= 0 || page.offset < 0 || page.count < 0 || page.total < page.offset+page.count || page.binding == "" || emittedLocal < 0 || emittedLocal > page.count {
		return "", catalog.E(catalog.CodeInternal, "ARD Search continuation state is invalid")
	}
	next := page.offset + emittedLocal
	if next >= page.total {
		return "", nil
	}
	return page.prefix + m.encodeCursorAt(page.generation, next, page.binding, page.expiresAt), nil
}

// Explore builds requested facets from the active immutable discovery snapshot
// under the exploration limits.
func (m *Manager) Explore(ctx context.Context, tenantID string, request ExploreRequest) (ExploreResponse, error) {
	if len(request.ResultType.Facets) == 0 || len(request.ResultType.Facets) > 10 {
		return ExploreResponse{}, catalog.E(catalog.CodeInvalidArgument, "one to ten facets are required")
	}
	if request.Query != nil {
		request.Query.Text = strings.Join(strings.Fields(request.Query.Text), " ")
		if utf8.RuneCountInString(request.Query.Text) > 500 {
			return ExploreResponse{}, catalog.E(catalog.CodeInvalidArgument, "ARD Explore query.text must be at most 500 characters")
		}
		if err := normalizeStructuredQuery(request.Query); err != nil {
			return ExploreResponse{}, err
		}
	}
	seenFacets := make(map[string]struct{}, len(request.ResultType.Facets))
	var queryContext any
	if request.Query != nil {
		queryContext = request.Query.Context
	}
	for index := range request.ResultType.Facets {
		facet := &request.ResultType.Facets[index]
		resolved, resolveErr := standards.ResolveARDTerm(facet.Field, queryContext)
		if resolveErr != nil || !supportedStructuredField(resolved) {
			return ExploreResponse{}, catalog.E(catalog.CodeInvalidArgument, "ARD facet field is not supported by this Registry profile")
		}
		facet.Field = resolved
		if _, duplicate := seenFacets[facet.Field]; duplicate {
			return ExploreResponse{}, catalog.E(catalog.CodeInvalidArgument, "ARD facet fields must be unique")
		}
		seenFacets[facet.Field] = struct{}{}
		if facet.Limit < 0 || facet.Limit > 100 || facet.MinCount < 0 {
			return ExploreResponse{}, catalog.E(catalog.CodeInvalidArgument, "facet limits are invalid")
		}
		if facet.Limit == 0 {
			facet.Limit = 20
		}
	}
	snapshot, err := m.Active(ctx, tenantID)
	if err != nil {
		return ExploreResponse{}, err
	}
	var filter map[string]any
	if request.Query != nil {
		filter = request.Query.Filter
	}
	native, err := projectNativeEntries(ctx, snapshot.ARD.Entries, filter, request.ResultType.Facets)
	if err != nil {
		return ExploreResponse{}, err
	}
	indices := make([]int, 0, len(snapshot.ARD.Entries))
	for i, entry := range snapshot.ARD.Entries {
		if matchFilter(entry, filter, nativeAt(native, i)) && (request.Query == nil || request.Query.Text == "" || CoreLexicalScore(entry, request.Query.Text) > 0) {
			indices = append(indices, i)
		}
	}
	var retrieval catalog.SnapshotRanking
	if request.Query != nil {
		hybrid, ranking, err := m.rankSemantic(ctx, tenantID, snapshot, *request.Query, native)
		if err != nil {
			return ExploreResponse{}, err
		}
		retrieval = ranking
		if ranking.Profile != "" {
			type resultIdentity struct{ identifier, version, mediaType string }
			selected := make(map[resultIdentity]bool, len(hybrid))
			for _, item := range hybrid {
				selected[resultIdentity{item.Identifier, item.Version, item.Type}] = true
			}
			indices = indices[:0]
			for i, entry := range snapshot.ARD.Entries {
				if selected[resultIdentity{entry.Identifier, entry.Version, entry.Type}] {
					indices = append(indices, i)
				}
			}
		}
	}
	response := ExploreResponse{ResultType: "facets", Facets: make(map[string]ExploreFacetResult)}
	response.Tier = "core"
	response.FallbackReason = retrieval.FallbackReason
	response.PartialIndex = retrieval.PartialIndex
	if retrieval.Profile != "" {
		response.Tier = "hybrid"
	}
	for _, facet := range request.ResultType.Facets {
		counts := make(map[string]int)
		for _, i := range indices {
			for _, value := range facetValues(snapshot.ARD.Entries[i], facet.Field, nativeAt(native, i)) {
				counts[value]++
			}
		}
		buckets := make([]ExploreBucket, 0)
		for value, count := range counts {
			if count >= max(1, facet.MinCount) {
				buckets = append(buckets, ExploreBucket{Value: value, Count: count})
			}
		}
		sort.Slice(buckets, func(i, j int) bool {
			if buckets[i].Count != buckets[j].Count {
				return buckets[i].Count > buckets[j].Count
			}
			return buckets[i].Value < buckets[j].Value
		})
		result := ExploreFacetResult{}
		if len(buckets) > facet.Limit {
			for _, bucket := range buckets[facet.Limit:] {
				result.OtherCount += bucket.Count
			}
			buckets = buckets[:facet.Limit]
		}
		result.Buckets = buckets
		response.Facets[facet.Field] = result
	}
	return response, nil
}

// List returns bounded filtered discovery entries with snapshot-bound
// continuation.
func (m *Manager) List(ctx context.Context, tenantID string, request ListRequest) (ListResponse, error) {
	if request.PageSize < 0 {
		return ListResponse{}, catalog.E(catalog.CodeInvalidArgument, "pageSize must be positive")
	}
	if request.PageSize == 0 {
		request.PageSize = 20
	}
	if request.PageSize > MaxARDPageSize {
		return ListResponse{}, catalog.E(catalog.CodeInvalidArgument, "pageSize cannot exceed 100")
	}
	request.Filter = strings.TrimSpace(request.Filter)
	request.OrderBy = strings.TrimSpace(request.OrderBy)
	var clauses []listFilterClause
	if request.Filter != "" {
		var err error
		clauses, err = parseListFilter(request.Filter)
		if err != nil {
			return ListResponse{}, err
		}
	}
	snapshot, err := m.Active(ctx, tenantID)
	if err != nil {
		return ListResponse{}, err
	}
	entries := append([]Entry(nil), snapshot.ARD.Entries...)
	if len(clauses) > 0 {
		filtered := entries[:0]
		for _, entry := range entries {
			if matchListFilter(entry, snapshot, clauses) {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
	}
	switch request.OrderBy {
	case "", "displayName":
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].DisplayName != entries[j].DisplayName {
				return entries[i].DisplayName < entries[j].DisplayName
			}
			return entryLess(entries[i], entries[j])
		})
	case "displayName DESC":
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].DisplayName != entries[j].DisplayName {
				return entries[i].DisplayName > entries[j].DisplayName
			}
			return entryLess(entries[i], entries[j])
		})
	case "updatedAt DESC":
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].UpdatedAt != entries[j].UpdatedAt {
				return entries[i].UpdatedAt > entries[j].UpdatedAt
			}
			return entryLess(entries[i], entries[j])
		})
	case "updatedAt":
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].UpdatedAt != entries[j].UpdatedAt {
				return entries[i].UpdatedAt < entries[j].UpdatedAt
			}
			return entryLess(entries[i], entries[j])
		})
	default:
		return ListResponse{}, catalog.E(catalog.CodeInvalidArgument, "orderBy supports displayName or updatedAt with optional DESC")
	}
	binding, err := m.cursorBinding(tenantID, "list", struct {
		Filter   string `json:"filter"`
		OrderBy  string `json:"orderBy"`
		PageSize int    `json:"pageSize"`
	}{Filter: request.Filter, OrderBy: request.OrderBy, PageSize: request.PageSize})
	if err != nil {
		return ListResponse{}, err
	}
	offset, err := m.decodeCursor(request.PageToken, snapshot.Generation, binding)
	if err != nil || offset > len(entries) {
		return ListResponse{}, catalog.E(catalog.CodeInvalidArgument, "pageToken is invalid or stale")
	}
	end := min(offset+request.PageSize, len(entries))
	response := ListResponse{Items: entries[offset:end], Total: len(entries)}
	if end < len(entries) {
		response.PageToken = m.encodeCursor(snapshot.Generation, end, binding)
	}
	return response, nil
}

// CoreLexicalScore averages each query term's strongest weighted metadata match
// and requires every term to match. Search, Explore and the offline relevance
// harness share this scorer; it is distinct from the Control API's core profile.
func CoreLexicalScore(entry Entry, query string) int {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return 0
	}
	fields := []struct {
		value  string
		weight int
	}{{entry.Identifier, 100}, {entry.DisplayName, 95}, {strings.Join(entry.RepresentativeQueries, " "), 90}, {entry.Description, 72}, {strings.Join(entry.Tags, " "), 65}}
	total := 0
	for _, term := range terms {
		best := 0
		for _, field := range fields {
			if strings.Contains(strings.ToLower(field.value), term) && field.weight > best {
				best = field.weight
			}
		}
		if best == 0 {
			return 0
		}
		total += best
	}
	return min(100, total/len(terms))
}

// ResolveSearchFilter returns the validated canonical terms for a query without
// mutating the caller's map. Public federation must use the same terms as local search.
func ResolveSearchFilter(query SearchQuery) (map[string]any, error) {
	if err := normalizeStructuredQuery(&query); err != nil {
		return nil, err
	}
	return query.Filter, nil
}

// MatchDisclosedFilter checks canonical constraints against the fields actually
// returned by a peer. ARD 5.3.2 permits omitted projection terms, even when used
// for filtering; the required identifier always discloses its publisher.
func MatchDisclosedFilter(ctx context.Context, entry Entry, fields map[string]json.RawMessage, filter map[string]any) (bool, error) {
	native, err := projectNativeEntry(ctx, entry, nativeFilterFields(filter))
	if err != nil {
		return false, err
	}
	for field, raw := range filter {
		_, present := fields[field]
		if strings.HasPrefix(field, "metadata.") {
			_, present = entry.Metadata[strings.TrimPrefix(field, "metadata.")]
		}
		if strings.HasPrefix(field, "data.") {
			present = native[field].present
		}
		if (present || field == "publisher") && !matchFilterField(entry, field, raw, native) {
			return false, nil
		}
	}
	return true, nil
}

// matchFilter requires every field to match at least one requested exact value.
func matchFilter(entry Entry, filter map[string]any, native nativeProjection) bool {
	for field, raw := range filter {
		if !matchFilterField(entry, field, raw, native) {
			return false
		}
	}
	return true
}

func matchFilterField(entry Entry, field string, raw any, native nativeProjection) bool {
	entryValues := facetValues(entry, field, native)
	for _, wanted := range toStrings(raw) {
		if slices.Contains(entryValues, wanted) {
			return true
		}
	}
	return false
}

// normalizeStructuredQuery resolves JSON-LD aliases, rejects duplicate
// canonical fields and enforces aggregate filter bounds.
func normalizeStructuredQuery(query *SearchQuery) error {
	if err := standards.ValidateARDQueryContext(query.Context); err != nil {
		return catalog.E(catalog.CodeInvalidArgument, "ARD query @context must be an absolute URI, object, or array")
	}
	if len(query.Filter) > maxStructuredFilters {
		return catalog.E(catalog.CodeInvalidArgument, "ARD query.filter cannot contain more than 20 fields")
	}
	normalized := make(map[string]any, len(query.Filter))
	totalValues := 0
	for field, raw := range query.Filter {
		resolved, err := standards.ResolveARDTerm(field, query.Context)
		if err != nil || !supportedStructuredField(resolved) {
			return catalog.E(catalog.CodeInvalidArgument, "ARD filter field is not supported by this Registry profile")
		}
		if _, duplicate := normalized[resolved]; duplicate {
			return catalog.E(catalog.CodeInvalidArgument, "ARD filter aliases must resolve to unique terms")
		}
		values := toStrings(raw)
		if len(values) == 0 || len(values) > 100 {
			return catalog.E(catalog.CodeInvalidArgument, "ARD filter values must be a string or a non-empty string array")
		}
		totalValues += len(values)
		if totalValues > 200 {
			return catalog.E(catalog.CodeInvalidArgument, "ARD query.filter contains too many values")
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 512 {
				return catalog.E(catalog.CodeInvalidArgument, "ARD filter values must be non-empty and at most 512 characters")
			}
		}
		normalized[resolved] = raw
	}
	query.Filter = normalized
	return nil
}

// supportedStructuredField accepts reviewed discovery fields and a single
// bounded metadata key.
func supportedStructuredField(field string) bool {
	if _, ok := nativePaths[field]; ok {
		return true
	}
	switch field {
	case "identifier", "displayName", "type", "representativeQueries", "capabilities", "description", "tags", "version", "updatedAt", "publisher", standards.ARDSubjectIRI:
		return true
	default:
		key := strings.TrimPrefix(field, "metadata.")
		return key != field && key != "" && !strings.Contains(key, ".") && len(key) <= 128
	}
}

// facetValues projects a supported discovery field, including AIR publisher or
// one metadata value.
func facetValues(entry Entry, field string, native nativeProjection) []string {
	if strings.HasPrefix(field, "data.") {
		return native[field].values
	}
	switch field {
	case standards.ARDSubjectIRI:
		values := slices.Clone(entry.Subjects)
		slices.Sort(values)
		return slices.Compact(values)
	case "type":
		return []string{entry.Type}
	case "tags":
		return entry.Tags
	case "capabilities":
		return entry.Capabilities
	case "representativeQueries":
		return entry.RepresentativeQueries
	case "description":
		return []string{entry.Description}
	case "version":
		return []string{entry.Version}
	case "updatedAt":
		return []string{entry.UpdatedAt}
	case "displayName":
		return []string{entry.DisplayName}
	case "identifier":
		return []string{entry.Identifier}
	case "publisher":
		identifier, err := catalog.ParseAIRIdentifier(entry.Identifier)
		if err == nil {
			return []string{identifier.Publisher}
		}
		return nil
	default:
		if strings.HasPrefix(field, "metadata.") {
			value, ok := entry.Metadata[strings.TrimPrefix(field, "metadata.")]
			// Null is valid metadata but has no comparable scalar value. Do
			// not turn it into the literal string "<nil>" for filters/facets.
			if ok && value != nil {
				return []string{fmt.Sprint(value)}
			}
		}
		return nil
	}
}

// entryLess orders entries by identity, version and media type for
// deterministic browse results.
func entryLess(left, right Entry) bool {
	if left.Identifier != right.Identifier {
		return left.Identifier < right.Identifier
	}
	if left.Version != right.Version {
		return left.Version < right.Version
	}
	return left.Type < right.Type
}

// toStrings accepts a scalar string or homogeneous string array and rejects
// mixed arrays.
func toStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil
			}
			result = append(result, text)
		}
		return result
	default:
		return nil
	}
}

type listFilterClause struct {
	field  string
	values []string
	after  time.Time
}

var listClausePattern = regexp.MustCompile(`(?i)^\s*(displayName|type|publisherId|createdAfter|updatedAfter)\s*(=|>)\s*'([^']+)'\s*$`)

// parseListFilter parses the bounded supported List filter clauses and
// validates field-specific values.
func parseListFilter(filter string) ([]listFilterClause, error) {
	if len(filter) > 2048 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "filter exceeds the supported length")
	}
	rawClauses, err := splitListFilterClauses(filter)
	if err != nil || len(rawClauses) == 0 || len(rawClauses) > 5 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "filter must use supported clauses joined by AND")
	}
	clauses := make([]listFilterClause, 0, len(rawClauses))
	seen := make(map[string]struct{}, len(rawClauses))
	for _, raw := range rawClauses {
		parts := listClausePattern.FindStringSubmatch(raw)
		if parts == nil {
			return nil, catalog.E(catalog.CodeInvalidArgument, "filter clause syntax is invalid")
		}
		field := canonicalListField(parts[1])
		if _, duplicate := seen[field]; duplicate {
			return nil, catalog.E(catalog.CodeInvalidArgument, "filter fields must be unique")
		}
		seen[field] = struct{}{}
		operator := parts[2]
		value := strings.TrimSpace(parts[3])
		clause := listFilterClause{field: field}
		switch field {
		case "displayName":
			if operator != "=" || value == "" || utf8.RuneCountInString(value) > 160 {
				return nil, catalog.E(catalog.CodeInvalidArgument, "displayName requires a bounded equality value")
			}
			clause.values = []string{value}
		case "type", "publisherId":
			if operator != "=" {
				return nil, catalog.E(catalog.CodeInvalidArgument, "type and publisherId support equality only")
			}
			for _, item := range strings.Split(value, ",") {
				item = strings.TrimSpace(item)
				if item == "" || utf8.RuneCountInString(item) > 255 || slices.Contains(clause.values, item) {
					return nil, catalog.E(catalog.CodeInvalidArgument, "type and publisherId require unique comma-separated values")
				}
				clause.values = append(clause.values, item)
			}
			if len(clause.values) > 20 {
				return nil, catalog.E(catalog.CodeInvalidArgument, "filter clause contains too many values")
			}
		case "createdAfter", "updatedAfter":
			if operator != ">" {
				return nil, catalog.E(catalog.CodeInvalidArgument, "time filters require the greater-than operator")
			}
			clause.after, err = parseListTime(value)
			if err != nil {
				return nil, catalog.E(catalog.CodeInvalidArgument, "time filters require an ISO 8601 date or timestamp")
			}
		}
		clauses = append(clauses, clause)
	}
	return clauses, nil
}

// splitListFilterClauses splits whitespace-delimited AND operators outside
// quotes and rejects empty or unterminated clauses.
func splitListFilterClauses(filter string) ([]string, error) {
	clauses := make([]string, 0, 5)
	quoted := false
	start := 0
	for index := 0; index < len(filter); index++ {
		if filter[index] == '\'' {
			quoted = !quoted
			continue
		}
		if quoted || index+3 > len(filter) || !strings.EqualFold(filter[index:index+3], "AND") {
			continue
		}
		if index == 0 || index+3 == len(filter) || !isListSpace(filter[index-1]) || !isListSpace(filter[index+3]) {
			continue
		}
		clause := strings.TrimSpace(filter[start:index])
		if clause == "" {
			return nil, errors.New("empty filter clause")
		}
		clauses = append(clauses, clause)
		index += 2
		start = index + 1
	}
	if quoted {
		return nil, errors.New("unterminated filter value")
	}
	last := strings.TrimSpace(filter[start:])
	if last == "" {
		return nil, errors.New("empty filter clause")
	}
	return append(clauses, last), nil
}

// isListSpace recognizes the ASCII whitespace accepted around List filter
// operators.
func isListSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

// canonicalListField normalizes accepted case-insensitive field names to their
// contract spelling.
func canonicalListField(field string) string {
	switch strings.ToLower(field) {
	case "displayname":
		return "displayName"
	case "publisherid":
		return "publisherId"
	case "createdafter":
		return "createdAfter"
	case "updatedafter":
		return "updatedAfter"
	default:
		return strings.ToLower(field)
	}
}

// parseListTime accepts an RFC 3339 instant or an ISO calendar date.
func parseListTime(value string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	return time.Parse("2006-01-02", value)
}

// matchListFilter applies exact List predicates and exclusive time bounds to
// retained entry metadata.
func matchListFilter(entry Entry, snapshot Snapshot, clauses []listFilterClause) bool {
	for _, clause := range clauses {
		switch clause.field {
		case "displayName":
			if !strings.EqualFold(entry.DisplayName, clause.values[0]) {
				return false
			}
		case "type":
			if !slices.Contains(clause.values, entry.Type) {
				return false
			}
		case "publisherId":
			identifier, err := catalog.ParseAIRIdentifier(entry.Identifier)
			if err != nil || !slices.Contains(clause.values, identifier.Publisher) {
				return false
			}
		case "createdAfter":
			createdAt := snapshot.CreatedAt
			if value, ok := entry.Metadata[catalogCreatedAtMetadata].(string); ok {
				parsed, err := time.Parse(time.RFC3339Nano, value)
				if err != nil {
					return false
				}
				createdAt = parsed
			}
			if !createdAt.After(clause.after) {
				return false
			}
		case "updatedAfter":
			updatedAt, err := time.Parse(time.RFC3339Nano, entry.UpdatedAt)
			if err != nil || !updatedAt.After(clause.after) {
				return false
			}
		}
	}
	return true
}

// cursorBinding authenticates the tenant, operation, host, audience and request
// as one pagination binding.
func (m *Manager) cursorBinding(tenantID, operation string, request any) (string, error) {
	raw, err := json.Marshal(struct {
		BaseURL   string   `json:"baseUrl"`
		Host      string   `json:"host"`
		Audience  Audience `json:"audience"`
		TenantID  string   `json:"tenantId"`
		Operation string   `json:"operation"`
		Request   any      `json:"request"`
	}{BaseURL: m.baseURL, Host: m.host, Audience: m.audience, TenantID: tenantID, Operation: operation, Request: request})
	if err != nil {
		return "", catalog.Wrap(catalog.CodeInternal, "bind pagination cursor", err)
	}
	mac := hmac.New(sha256.New, m.cursorKey)
	_, _ = mac.Write([]byte("cursor-binding-v1\x00"))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// encodeCursor signs a generation/offset continuation with bounded expiry and
// the request binding.
func (m *Manager) encodeCursor(generation int64, offset int, binding string) string {
	return m.encodeCursorAt(generation, offset, binding, m.clock.Now().Add(cursorTTL).Unix())
}

func (m *Manager) encodeCursorAt(generation int64, offset int, binding string, expiresAt int64) string {
	payload := "1:" + strconv.FormatInt(generation, 10) + ":" + strconv.Itoa(offset) + ":" + strconv.FormatInt(expiresAt, 10)
	mac := hmac.New(sha256.New, m.cursorKey)
	_, _ = mac.Write([]byte("cursor-token-v1\x00" + payload + "\x00" + binding))
	return base64.RawURLEncoding.EncodeToString([]byte(payload + ":" + hex.EncodeToString(mac.Sum(nil))))
}

// decodeCursor validates signature, generation, nonnegative offset and expiry;
// an empty token starts at offset zero.
func (m *Manager) decodeCursor(token string, generation int64, binding string) (int, error) {
	if token == "" {
		return 0, nil
	}
	if len(token) > 512 {
		return 0, catalog.E(catalog.CodeInvalidArgument, "page token is invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, catalog.E(catalog.CodeInvalidArgument, "page token is invalid")
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 5 || parts[0] != "1" {
		return 0, catalog.E(catalog.CodeInvalidArgument, "page token is invalid")
	}
	payload := strings.Join(parts[:4], ":")
	provided, err := hex.DecodeString(parts[4])
	if err != nil {
		return 0, catalog.E(catalog.CodeInvalidArgument, "page token is invalid")
	}
	mac := hmac.New(sha256.New, m.cursorKey)
	_, _ = mac.Write([]byte("cursor-token-v1\x00" + payload + "\x00" + binding))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return 0, catalog.E(catalog.CodeInvalidArgument, "page token is invalid for this request")
	}
	parsedGeneration, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || parsedGeneration != generation {
		return 0, catalog.E(catalog.CodeInvalidArgument, "page token is stale")
	}
	offset, err := strconv.Atoi(parts[2])
	if err != nil || offset < 0 {
		return 0, catalog.E(catalog.CodeInvalidArgument, "page token is invalid")
	}
	expiresAt, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || !time.Unix(expiresAt, 0).After(m.clock.Now()) {
		return 0, catalog.E(catalog.CodeInvalidArgument, "page token has expired")
	}
	return offset, nil
}

// Host is the immutable configured publication binding used by claimed work.
func (m *Manager) Host() string { return m.host }
func (m *Manager) authorize(ctx context.Context, tenant string, action identity.Permission) error {
	actor, err := identity.RequirePermission(ctx, action, "")
	if err != nil {
		return catalog.PermissionError("current publication permission is required", err)
	}
	if actor.TenantID != tenant {
		return catalog.E(catalog.CodeForbidden, "publication tenant does not match")
	}
	if action == "publication.generate" {
		return identity.RequireInternalTarget(ctx, "publication.generate", tenant, m.host)
	}
	return nil
}
