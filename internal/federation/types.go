// Package federation owns controlled source discovery, synchronization, and
// search. [Manager] applies administrator-selected source policy and bounded
// outbound access; [Store] preserves complete snapshots and immutable occurrence
// evidence. External observations retain their provenance and require explicit
// adoption before entering the local governed resource lifecycle.
package federation

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

type SourceType string

const (
	SourceARDManifest SourceType = "ard_manifest"
	SourceAICatalog   SourceType = "ai_catalog"
	SourceRegistry    SourceType = "ard_registry"
	SourceHybrid      SourceType = "hybrid"
)

type SourceStatus string

const (
	SourceActive  SourceStatus = "active"
	SourcePaused  SourceStatus = "paused"
	SourceError   SourceStatus = "error"
	SourceRemoved SourceStatus = "removed"
)

type UpdateMode string

const (
	UpdateManual    UpdateMode = "manual_review"
	UpdateAutomatic UpdateMode = "automatic"
)

type RetrievalMode string

const (
	RetrievalIndexed RetrievalMode = "indexed"
	RetrievalLive    RetrievalMode = "live"
	RetrievalHybrid  RetrievalMode = "hybrid"
)

type DiscoveryAudience string

const (
	AudienceAnonymous     DiscoveryAudience = "anonymous"
	AudienceAuthenticated DiscoveryAudience = "authenticated"
)

type Scope struct {
	ExcludedIdentifiers []string       `json:"excludedIdentifiers,omitempty"`
	Identifiers         []string       `json:"identifiers,omitempty"`
	Versions            []string       `json:"versions,omitempty"`
	Kinds               []catalog.Kind `json:"kinds,omitempty"`
	Publishers          []string       `json:"publishers,omitempty"`
	Tags                []string       `json:"tags,omitempty"`
}

type LivePolicy struct {
	Enabled                         bool   `json:"enabled"`
	ReferralOnly                    bool   `json:"referralOnly,omitempty"`
	QueryPrivacyAcknowledged        bool   `json:"queryPrivacyAcknowledged"`
	AnonymousDisclosureAcknowledged bool   `json:"anonymousDisclosureAcknowledged,omitempty"`
	FederationMode                  string `json:"federationMode"`
}

type DiscoveryAssignment struct {
	Host     string            `json:"host"`
	Audience DiscoveryAudience `json:"audience"`
}

type ReferralPolicy struct {
	Enabled                         bool     `json:"enabled"`
	MaxHops                         int      `json:"maxHops,omitempty"`
	ApprovedSourceIDs               []string `json:"approvedSourceIds,omitempty"`
	AnonymousDisclosureAcknowledged bool     `json:"anonymousDisclosureAcknowledged,omitempty"`
}

type NestingPolicy struct {
	Enabled        bool     `json:"enabled"`
	MaxDepth       int      `json:"maxDepth,omitempty"`
	AllowedOrigins []string `json:"allowedOrigins,omitempty"`
}

type Source struct {
	TrustVerificationPolicy string                `json:"trustVerificationPolicy,omitempty"`
	ArtworkEnabled          bool                  `json:"artworkEnabled"`
	PresentationGeneration  int64                 `json:"presentationGeneration,omitempty"`
	ID                      string                `json:"id"`
	TenantID                string                `json:"tenantId"`
	DisplayName             string                `json:"displayName"`
	Type                    SourceType            `json:"type"`
	Endpoint                string                `json:"endpoint"`
	RegistrySearchURL       string                `json:"registrySearchUrl,omitempty"`
	RegistryListURL         string                `json:"registryListUrl,omitempty"`
	Status                  SourceStatus          `json:"status"`
	ProfileID               string                `json:"profileId"`
	RetrievalMode           RetrievalMode         `json:"retrievalMode"`
	Scope                   Scope                 `json:"scope"`
	UpdateMode              UpdateMode            `json:"updateMode"`
	Live                    LivePolicy            `json:"live"`
	Referrals               ReferralPolicy        `json:"referrals"`
	DiscoveryAssignments    []DiscoveryAssignment `json:"discoveryAssignments,omitempty"`
	Nesting                 NestingPolicy         `json:"nesting"`
	CredentialBinding       string                `json:"credentialBinding,omitempty"`
	NetworkProfile          string                `json:"networkProfile,omitempty"`
	PolicyID                string                `json:"policyId"`
	PolicyRevision          int64                 `json:"policyRevision"`
	PolicyDigest            string                `json:"policyDigest"`
	CredentialRevision      string                `json:"credentialRevision,omitempty"`
	Revision                int64                 `json:"revision"`
	LastCompleteSnapshotID  string                `json:"lastCompleteSnapshotId,omitempty"`
	LastSyncAt              *time.Time            `json:"lastSyncAt,omitempty"`
	LastError               string                `json:"lastError,omitempty"`
	CreatedBy               string                `json:"createdBy"`
	CreatedAt               time.Time             `json:"createdAt"`
	UpdatedAt               time.Time             `json:"updatedAt"`
	// InspectionID identifies the inspection consumed by this revision write. It
	// is transient persistence metadata, not current source state or API data.
	InspectionID string `json:"-"`
}

type Inspection struct {
	ID                 string            `json:"id"`
	TenantID           string            `json:"tenantId"`
	RequestedURL       string            `json:"requestedUrl"`
	ResolvedURL        string            `json:"resolvedUrl"`
	SourceType         SourceType        `json:"sourceType"`
	ProfileID          string            `json:"profileId"`
	MediaType          string            `json:"mediaType"`
	BodyDigest         string            `json:"bodyDigest"`
	EntryCount         int               `json:"entryCount"`
	KindCounts         map[string]int    `json:"kindCounts"`
	Publishers         []string          `json:"publishers"`
	Tags               []string          `json:"tags"`
	Capabilities       map[string]bool   `json:"capabilities"`
	RegistrySearchURL  string            `json:"registrySearchUrl,omitempty"`
	RegistryListURL    string            `json:"registryListUrl,omitempty"`
	NetworkProfile     string            `json:"networkProfile,omitempty"`
	CredentialBinding  string            `json:"credentialBinding,omitempty"`
	PolicyID           string            `json:"policyId"`
	PolicyRevision     int64             `json:"policyRevision"`
	PolicyDigest       string            `json:"policyDigest"`
	CredentialRevision string            `json:"credentialRevision,omitempty"`
	Warnings           []catalog.Finding `json:"warnings,omitempty"`
	ExpiresAt          time.Time         `json:"expiresAt"`
	CreatedAt          time.Time         `json:"createdAt"`
}

type SnapshotState string

const (
	SnapshotRunning    SnapshotState = "running"
	SnapshotComplete   SnapshotState = "complete"
	SnapshotIncomplete SnapshotState = "incomplete"
	SnapshotFailed     SnapshotState = "failed"
)

type Snapshot struct {
	ID                     string              `json:"id"`
	TenantID               string              `json:"tenantId"`
	SourceID               string              `json:"sourceId"`
	SourceRevision         int64               `json:"sourceRevision"`
	State                  SnapshotState       `json:"state"`
	EnumerationComplete    bool                `json:"enumerationComplete"`
	WithdrawalAuthority    bool                `json:"withdrawalAuthority"`
	ReconcileCurrentView   bool                `json:"reconcileCurrentView,omitempty"`
	BodyDigest             string              `json:"bodyDigest"`
	PolicyID               string              `json:"policyId"`
	PolicyRevision         int64               `json:"policyRevision"`
	PolicyDigest           string              `json:"policyDigest"`
	CredentialRevision     string              `json:"credentialRevision,omitempty"`
	MemberCount            int                 `json:"memberCount"`
	Added                  int                 `json:"added"`
	Updated                int                 `json:"updated"`
	Withdrawn              int                 `json:"withdrawn"`
	StartedAt              time.Time           `json:"startedAt"`
	CompletedAt            *time.Time          `json:"completedAt,omitempty"`
	Findings               []catalog.Finding   `json:"findings,omitempty"`
	Retrievals             []RetrievalEvidence `json:"retrievals,omitempty"`
	RetrievalStates        []RetrievalState    `json:"-"`
	RetrievalInvalidations []string            `json:"-"`
}

// RetrievalEvidence is the safe, immutable record exposed with a source
// snapshot. URLs and validators remain tenant-private and are represented by
// digests only.
type RetrievalEvidence struct {
	ID                   string    `json:"id"`
	RequestKey           string    `json:"requestKey"`
	RequestedURLDigest   string    `json:"requestedUrlDigest"`
	FinalURLDigest       string    `json:"finalUrlDigest"`
	ProfileID            string    `json:"profileId"`
	Status               int       `json:"status"`
	ReceivedBody         bool      `json:"receivedBody"`
	Reused               bool      `json:"reused"`
	RepresentationDigest string    `json:"representationDigest,omitempty"`
	ETagDigest           string    `json:"etagDigest,omitempty"`
	LastModifiedDigest   string    `json:"lastModifiedDigest,omitempty"`
	FetchedAt            time.Time `json:"fetchedAt"`
}

// RetrievalState is private conditional-request state. It is deliberately
// excluded from API JSON and is committed atomically with its snapshot.
type RetrievalState struct {
	TenantID              string
	SourceID              string
	SourceRevision        int64
	RequestKey            string
	RequestedURL          string
	FinalURL              string
	RequestIdentityDigest string
	ProfileID             string
	PolicyDigest          string
	CredentialRevision    string
	RepresentationDigest  string
	MediaType             string
	Body                  []byte
	ETag                  string
	LastModified          string
	VaryNames             []string
	VarySelectionDigest   string
	ListTerminal          bool
	UpdatedAt             time.Time
}

// UnmarshalJSON accepts only the current closed retained snapshot shape.
func (s *Snapshot) UnmarshalJSON(data []byte) error {
	if err := jsondoc.Validate(data); err != nil {
		return err
	}
	type snapshotAlias Snapshot
	var decoded snapshotAlias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*s = Snapshot(decoded)
	return nil
}

type Completeness string

const (
	CompleteEntry Completeness = "complete"
	Projection    Completeness = "projection"
	Unresolved    Completeness = "unresolved"
)

type Freshness string

const (
	FreshCurrent       Freshness = "current"
	FreshStale         Freshness = "stale"
	FreshWithdrawn     Freshness = "withdrawn"
	FreshSourceRemoved Freshness = "source_removed"
)

type Compatibility struct {
	Structural string            `json:"structural"`
	ProfileID  string            `json:"profileId"`
	Support    string            `json:"support"`
	Findings   []catalog.Finding `json:"findings,omitempty"`
}

type Observation struct {
	RatingsMetadata       json.RawMessage       `json:"-"`
	ArtworkMetadata       json.RawMessage       `json:"-"`
	ID                    string                `json:"id"`
	TenantID              string                `json:"tenantId"`
	SourceID              string                `json:"sourceId"`
	SnapshotID            string                `json:"-"`
	SourceKey             string                `json:"sourceKey"`
	Identifier            string                `json:"identifier,omitempty"`
	Version               string                `json:"version,omitempty"`
	VersionPresent        bool                  `json:"versionPresent"`
	Kind                  catalog.Kind          `json:"kind,omitempty"`
	MediaType             string                `json:"mediaType,omitempty"`
	Completeness          Completeness          `json:"completeness"`
	Compatibility         Compatibility         `json:"compatibility"`
	RawEntry              json.RawMessage       `json:"-"`
	NormalizedEntry       json.RawMessage       `json:"-"`
	RawEntryAvailable     bool                  `json:"rawEntryAvailable"`
	ExactEntryDigest      string                `json:"exactEntryDigest"`
	IdentityDigest        string                `json:"identityDigest"`
	NormalizedEntryDigest string                `json:"normalizedEntryDigest,omitempty"`
	NormalizationProfile  string                `json:"normalizationProfile,omitempty"`
	SearchDisplayName     string                `json:"-"`
	SearchDescription     string                `json:"-"`
	SearchTags            []string              `json:"-"`
	SearchQueries         []string              `json:"-"`
	SearchPublisher       string                `json:"-"`
	Freshness             Freshness             `json:"freshness"`
	FirstSeenAt           time.Time             `json:"firstSeenAt"`
	LastSeenAt            time.Time             `json:"lastSeenAt"`
	Occurrence            ObservationOccurrence `json:"-"`

	controlFilters ObservationControlFilters
}

type ObservationOccurrence struct {
	ObservationID            string    `json:"observationId"`
	SourceID                 string    `json:"sourceId"`
	SnapshotID               string    `json:"snapshotId"`
	SourceRevision           int64     `json:"sourceRevision"`
	RetrievalDigest          string    `json:"retrievalDigest"`
	EntryLocation            string    `json:"entryLocation"`
	ExactEntryDigest         string    `json:"exactEntryDigest"`
	NormalizedEntryDigest    string    `json:"normalizedEntryDigest,omitempty"`
	NormalizationInputDigest string    `json:"normalizationInputDigest,omitempty"`
	EvidenceComplete         bool      `json:"evidenceComplete"`
	ObservedAt               time.Time `json:"observedAt"`
}

// MaxObservationGroupContributions bounds the group preview and its selected
// occurrence-summary lookup. Complete histories have their own paged endpoint.
const MaxObservationGroupContributions = 100

// OccurrenceSummary binds an exact retained count to the latest occurrence in
// the same read snapshot. It is an internal projection, not retained evidence.
type OccurrenceSummary struct {
	Latest ObservationOccurrence
	Total  int
}

type ObservationGroupContribution struct {
	Observation      Observation           `json:"observation"`
	OccurrenceCount  int                   `json:"occurrenceCount"`
	LatestOccurrence ObservationOccurrence `json:"latestOccurrence"`
	SourceConflict   bool                  `json:"sourceConflict"`
}

type ObservationGroupMember struct {
	NormalizedEntryDigest string                         `json:"normalizedEntryDigest"`
	Contributions         []ObservationGroupContribution `json:"contributions"`
}

type ObservationGroup struct {
	ID                     string                   `json:"id"`
	Identifier             string                   `json:"identifier"`
	Version                string                   `json:"version,omitempty"`
	VersionPresent         bool                     `json:"versionPresent"`
	Kind                   catalog.Kind             `json:"kind"`
	Conflict               bool                     `json:"conflict"`
	ContributionsTruncated bool                     `json:"contributionsTruncated"`
	Members                []ObservationGroupMember `json:"members"`
}

type SourceListRequest struct {
	DisplayName      string
	Status           SourceStatus
	AfterDisplayName string
	AfterID          string
	Limit            int
}

type SnapshotListRequest struct {
	SourceID       string
	AfterStartedAt *time.Time
	AfterID        string
	Limit          int
}

type ObservationListRequest struct {
	SourceID            string
	CurrentOnly         bool
	AfterIdentifier     string
	AfterVersionPresent bool
	AfterVersion        string
	AfterID             string
	Limit               int
}

type OccurrenceListRequest struct {
	ObservationID        string
	AfterObservedAt      *time.Time
	AfterSnapshotID      string
	AfterRetrievalDigest string
	AfterEntryLocation   string
	Limit                int
}

type ObservationSearchRequest struct {
	Query           SearchQuery
	AfterScore      int
	AfterIdentifier string
	AfterID         string
	Limit           int
}

type AdoptInput struct {
	SnapshotID       string             `json:"snapshotId"`
	RetrievalDigest  string             `json:"retrievalDigest"`
	EntryLocation    string             `json:"entryLocation"`
	NamespaceID      string             `json:"namespaceId"`
	PublisherID      string             `json:"publisherId"`
	Version          string             `json:"version,omitempty"`
	DisplayName      string             `json:"displayName,omitempty"`
	Description      string             `json:"description,omitempty"`
	Categories       []string           `json:"categories,omitempty"`
	DocumentationURL string             `json:"documentationUrl,omitempty"`
	SupportURL       string             `json:"supportUrl,omitempty"`
	License          string             `json:"license,omitempty"`
	TermsURL         string             `json:"termsUrl,omitempty"`
	Visibility       catalog.Visibility `json:"visibility,omitempty"`
}

type ConnectInput struct {
	TrustVerificationPolicy string                `json:"trustVerificationPolicy,omitempty"`
	ArtworkEnabled          bool                  `json:"artworkEnabled,omitempty"`
	InspectionID            string                `json:"inspectionId"`
	DisplayName             string                `json:"displayName"`
	RetrievalMode           RetrievalMode         `json:"retrievalMode,omitempty"`
	Scope                   Scope                 `json:"scope"`
	UpdateMode              UpdateMode            `json:"updateMode"`
	Live                    LivePolicy            `json:"live"`
	Referrals               ReferralPolicy        `json:"referrals"`
	DiscoveryAssignments    []DiscoveryAssignment `json:"discoveryAssignments,omitempty"`
	Nesting                 NestingPolicy         `json:"nesting"`
	CredentialBinding       string                `json:"credentialBinding,omitempty"`
	NetworkProfile          string                `json:"networkProfile,omitempty"`
	Reason                  string                `json:"reason"`
}

type ChangeIntent string

const (
	ChangeUpdate ChangeIntent = "update"
	ChangeRemove ChangeIntent = "remove"
)

type PatchInput struct {
	TrustVerificationPolicy *string                `json:"trustVerificationPolicy,omitempty"`
	ArtworkEnabled          *bool                  `json:"artworkEnabled,omitempty"`
	InspectionID            string                 `json:"inspectionId,omitempty"`
	DisplayName             *string                `json:"displayName,omitempty"`
	RetrievalMode           *RetrievalMode         `json:"retrievalMode,omitempty"`
	Scope                   *Scope                 `json:"scope,omitempty"`
	UpdateMode              *UpdateMode            `json:"updateMode,omitempty"`
	Live                    *LivePolicy            `json:"live,omitempty"`
	Referrals               *ReferralPolicy        `json:"referrals,omitempty"`
	DiscoveryAssignments    *[]DiscoveryAssignment `json:"discoveryAssignments,omitempty"`
	Nesting                 *NestingPolicy         `json:"nesting,omitempty"`
	CredentialBinding       *string                `json:"credentialBinding,omitempty"`
	NetworkProfile          *string                `json:"networkProfile,omitempty"`
	ChangePreviewDigest     string                 `json:"changePreviewDigest,omitempty"`
	Intent                  ChangeIntent           `json:"intent,omitempty"`
	Reason                  string                 `json:"reason,omitempty"`
}

type ChangePreview struct {
	ArtworkOverridesAffected int          `json:"artworkOverridesAffected"`
	ID                       string       `json:"id"`
	TenantID                 string       `json:"tenantId"`
	SourceID                 string       `json:"sourceId"`
	SourceRevision           int64        `json:"sourceRevision"`
	Intent                   ChangeIntent `json:"intent"`
	CurrentMatched           int          `json:"currentMatched"`
	ProposedMatched          int          `json:"proposedMatched"`
	ExcludedObservations     int          `json:"excludedObservations"`
	SendsQueriesUpstream     bool         `json:"sendsQueriesUpstream"`
	ConfigurationDigest      string       `json:"configurationDigest"`
	ObservationSetDigest     string       `json:"observationSetDigest"`
	Digest                   string       `json:"digest"`
	CreatedAt                time.Time    `json:"createdAt"`
	ExpiresAt                time.Time    `json:"expiresAt"`
}

type TestResult struct {
	SourceID           string          `json:"sourceId"`
	Status             string          `json:"status"`
	HTTPStatus         int             `json:"httpStatus"`
	MediaType          string          `json:"mediaType"`
	BodyDigest         string          `json:"bodyDigest"`
	EntryCount         int             `json:"entryCount"`
	Capabilities       map[string]bool `json:"capabilities"`
	ProfileID          string          `json:"profileId"`
	SourceRevision     int64           `json:"sourceRevision"`
	PolicyID           string          `json:"policyId"`
	PolicyRevision     int64           `json:"policyRevision"`
	PolicyDigest       string          `json:"policyDigest"`
	CredentialRevision string          `json:"credentialRevision,omitempty"`
	TestedAt           time.Time       `json:"testedAt"`
	SafeDiagnostic     string          `json:"safeDiagnostic,omitempty"`
}

type SearchQuery struct {
	Text  string         `json:"text"`
	Kinds []catalog.Kind `json:"kinds,omitempty"`
	Tags  []string       `json:"tags,omitempty"`
}

type SearchResult struct {
	Observation Observation `json:"observation"`
	Score       int         `json:"score"`
	Source      string      `json:"source"`
}

type LiveSearchRequest struct {
	DirectOnly     bool              `json:"-"`
	Replay         bool              `json:"-"`
	Context        any               `json:"@context,omitempty"`
	Text           string            `json:"text"`
	Filter         map[string]any    `json:"filter,omitempty"`
	PageSize       int               `json:"pageSize,omitempty"`
	SourceIDs      []string          `json:"sourceIds,omitempty"`
	Reason         string            `json:"reason,omitempty"`
	DiscoveryHost  string            `json:"-"`
	Audience       DiscoveryAudience `json:"-"`
	Administrative bool              `json:"-"`
	CatalogTenant  bool              `json:"-"`
	catalogQuery   catalog.SearchQuery
}

type LiveResult struct {
	SourceRevision         int64           `json:"-"`
	PresentationGeneration int64           `json:"-"`
	PolicyDigest           string          `json:"-"`
	CredentialRevision     string          `json:"-"`
	Identifier             string          `json:"identifier"`
	Version                string          `json:"version,omitempty"`
	VersionPresent         bool            `json:"versionPresent"`
	Score                  int             `json:"score"`
	SourceID               string          `json:"sourceId"`
	Source                 string          `json:"source"`
	RespondingURL          string          `json:"-"`
	Entry                  json.RawMessage `json:"entry"`
}

type LiveSearchResponse struct {
	// ReplayFingerprint is absent when traversal cannot safely be replayed.
	ReplayFingerprint string           `json:"-"`
	Results           []LiveResult     `json:"results"`
	Partial           bool             `json:"partial"`
	Diagnostics       []LiveDiagnostic `json:"diagnostics,omitempty"`
}

type LiveDiagnostic struct {
	SourceID           string `json:"sourceId,omitempty"`
	Status             string `json:"status"`
	Message            string `json:"message,omitempty"`
	PolicyDigest       string `json:"policyDigest,omitempty"`
	CredentialRevision string `json:"credentialRevision,omitempty"`
}

type InspectionInput struct {
	URL               string `json:"url"`
	NetworkProfile    string `json:"networkProfile,omitempty"`
	CredentialBinding string `json:"credentialBinding,omitempty"`
}

type Referral struct {
	SourceID    string `json:"sourceId"`
	DisplayName string `json:"displayName"`
	URL         string `json:"url"`
}
