// Package catalog owns governed resource lifecycles and repository contracts.
// [Service] authorizes authoring, review, release and private-read operations;
// [Repository] implementations preserve tenant boundaries, immutable history
// and atomic transition evidence. Discovery projections do not authorize
// installation or execution. Infrastructure supplies validation, persistence,
// time, identifiers and outbound retrieval through the domain ports.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

type Kind string

const (
	KindAgent     Kind = "agent"
	KindMCPServer Kind = "mcp_server"
	KindSkill     Kind = "skill"
	KindPlugin    Kind = "plugin"
)

// Valid reports whether the role is primary, distribution, metadata or documentation.
func (r RepresentationRole) Valid() bool {
	return r == RepresentationPrimary || r == RepresentationDistribution || r == RepresentationMetadata || r == RepresentationDocumentation
}

// Valid reports whether the kind has a native governed Catalog lifecycle.
func (k Kind) Valid() bool {
	return k == KindAgent || k == KindMCPServer || k == KindSkill || k == KindPlugin
}

type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilityTenant  Visibility = "tenant"
	VisibilityPublic  Visibility = "public"
)

// Valid reports whether the visibility is one of the supported disclosure
// levels.
func (v Visibility) Valid() bool {
	return v == VisibilityPrivate || v == VisibilityTenant || v == VisibilityPublic
}

type RepresentationRole string

const (
	RepresentationPrimary       RepresentationRole = "primary"
	RepresentationDistribution  RepresentationRole = "distribution"
	RepresentationMetadata      RepresentationRole = "metadata"
	RepresentationDocumentation RepresentationRole = "documentation"
)

type FindingSeverity string

const (
	SeverityInfo    FindingSeverity = "info"
	SeverityWarning FindingSeverity = "warning"
	SeverityError   FindingSeverity = "error"
)

type Finding struct {
	Code     string          `json:"code"`
	Severity FindingSeverity `json:"severity"`
	Path     string          `json:"path,omitempty"`
	Message  string          `json:"message"`
}

type Namespace struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenantId"`
	Slug        string    `json:"slug"`
	DisplayName string    `json:"displayName"`
	Description string    `json:"description,omitempty"`
	Revision    int64     `json:"revision"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Publisher struct {
	ID                         string    `json:"id"`
	TenantID                   string    `json:"tenantId"`
	DisplayName                string    `json:"displayName"`
	Domains                    []string  `json:"domains"`
	ApprovedPublicationDomains []string  `json:"approvedPublicationDomains"`
	Revision                   int64     `json:"revision"`
	CreatedAt                  time.Time `json:"createdAt"`
	UpdatedAt                  time.Time `json:"updatedAt"`
}

type PublicationDomainRevocation struct {
	Publisher          Publisher `json:"publisher"`
	AffectedReleaseIDs []string  `json:"affectedReleaseIds"`
	WithdrawnHosts     []string  `json:"withdrawnHosts"`
}

type Resource struct {
	ID                  string     `json:"id"`
	TenantID            string     `json:"tenantId"`
	NamespaceID         string     `json:"namespaceId"`
	CanonicalIdentifier string     `json:"canonicalIdentifier"`
	Kind                Kind       `json:"kind"`
	CreatedBy           string     `json:"createdBy"`
	Revision            int64      `json:"revision"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
	DeletedBy           string     `json:"deletedBy,omitempty"`
	DeleteReason        string     `json:"deleteReason,omitempty"`
	DeletedAt           *time.Time `json:"deletedAt,omitempty"`
}

type ResourceVersion struct {
	ID                  string    `json:"id"`
	TenantID            string    `json:"tenantId"`
	ResourceID          string    `json:"resourceId"`
	Version             string    `json:"version"`
	CandidateRevisionID string    `json:"candidateRevisionId,omitempty"`
	SealedRevisionID    string    `json:"sealedRevisionId,omitempty"`
	Revision            int64     `json:"revision"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type Representation struct {
	Plugin            *PluginInspection       `json:"plugin,omitempty"`
	ID                string                  `json:"id"`
	Role              RepresentationRole      `json:"role"`
	MediaType         string                  `json:"mediaType"`
	ProfileID         string                  `json:"profileId"`
	URL               string                  `json:"url,omitempty"`
	Data              json.RawMessage         `json:"data,omitempty"`
	ContentDigest     string                  `json:"contentDigest"`
	Size              int64                   `json:"size"`
	ArtifactID        string                  `json:"artifactId,omitempty"`
	ValidationState   string                  `json:"validationState"`
	Findings          []Finding               `json:"findings,omitempty"`
	NormalizedFacts   map[string]any          `json:"normalizedFacts,omitempty"`
	UnknownProperties map[string]any          `json:"unknownProperties,omitempty"`
	Package           *SkillPackageInspection `json:"package,omitempty"`
	Handoff           *SkillHandoff           `json:"handoff,omitempty"`
}

// ExternalRetrieval is immutable evidence for one bounded anonymous HTTPS
// retrieval. It records only the metadata needed to bind a validation run to
// the exact decoded bytes and deployment-owned outbound policy used.
type ExternalRetrieval struct {
	RepresentationID string    `json:"representationId"`
	RequestedURL     string    `json:"requestedUrl"`
	FinalURL         string    `json:"finalUrl"`
	Status           int       `json:"status"`
	MediaType        string    `json:"mediaType"`
	ContentDigest    string    `json:"contentDigest"`
	Size             int64     `json:"size"`
	PolicyID         string    `json:"policyId"`
	PolicyRevision   int64     `json:"policyRevision"`
	PolicyDigest     string    `json:"policyDigest"`
	RetrievedAt      time.Time `json:"retrievedAt"`
	ExpiresAt        time.Time `json:"expiresAt"`
}

// DocumentBlob is an exact, bounded representation document retained by the
// Catalog. Its identifier is the lower-case SHA-256 digest without the
// algorithm prefix so existing v1 artifact URLs remain stable.
type DocumentBlob struct {
	ID        string
	TenantID  string
	Digest    string
	MediaType string
	Size      int64
	Content   []byte
	CreatedAt time.Time
}

type VersionRevision struct {
	ID                string              `json:"id"`
	TenantID          string              `json:"tenantId"`
	ResourceVersionID string              `json:"resourceVersionId"`
	RevisionNumber    int                 `json:"revisionNumber"`
	Representations   []Representation    `json:"representations"`
	ContentSetDigest  string              `json:"contentSetDigest"`
	Findings          []Finding           `json:"findings,omitempty"`
	EvidenceSetID     string              `json:"evidenceSetId"`
	Adoption          *AdoptionProvenance `json:"adoption,omitempty"`
	CreatedBy         string              `json:"createdBy"`
	CreatedAt         time.Time           `json:"createdAt"`
}

// AdoptionProvenance immutably binds a locally governed draft to the exact
// external occurrence selected by an authorized contributor.
type AdoptionProvenance struct {
	ObservationID         string    `json:"observationId"`
	SourceID              string    `json:"sourceId"`
	SourceRevision        int64     `json:"sourceRevision"`
	SnapshotID            string    `json:"snapshotId"`
	RetrievalDigest       string    `json:"retrievalDigest"`
	EntryLocation         string    `json:"entryLocation"`
	ExactEntryDigest      string    `json:"exactEntryDigest"`
	NormalizedEntryDigest string    `json:"normalizedEntryDigest"`
	NormalizationProfile  string    `json:"normalizationProfile"`
	AdoptedBy             string    `json:"adoptedBy"`
	AdoptedAt             time.Time `json:"adoptedAt"`
}

type ExternalObservationAdoptionInput struct {
	// SourceAdmissionRevision fences current policy separately from immutable provenance.
	SourceAdmissionRevision int64
	// AdmitCommit guards the final memory-store commit after artifact preparation.
	// PostgreSQL also verifies SourceAdmissionRevision under its source-row lock.
	AdmitCommit           func(context.Context, func(context.Context) error) error
	NamespaceID           string
	CanonicalIdentifier   string
	Kind                  Kind
	Version               string
	Representation        RepresentationInput
	DisplayName           string
	Description           string
	Tags                  []string
	Categories            []string
	RepresentativeQueries []string
	PublisherID           string
	DocumentationURL      string
	SupportURL            string
	License               string
	TermsURL              string
	Visibility            Visibility
	Provenance            AdoptionProvenance
}

type ExternalObservationAdoption struct {
	Resource        Resource        `json:"resource"`
	Version         ResourceVersion `json:"version"`
	VersionRevision VersionRevision `json:"versionRevision"`
	Evidence        EvidenceSet     `json:"evidence"`
	Listing         CatalogListing  `json:"listing"`
	ListingRevision ListingRevision `json:"listingRevision"`
}

type CatalogListing struct {
	ID                  string    `json:"id"`
	TenantID            string    `json:"tenantId"`
	NamespaceID         string    `json:"namespaceId"`
	ResourceID          string    `json:"resourceId"`
	CandidateRevisionID string    `json:"candidateRevisionId,omitempty"`
	CurrentReleaseID    string    `json:"currentReleaseId,omitempty"`
	Revision            int64     `json:"revision"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type Relationship struct {
	Type             string `json:"type"`
	TargetIdentifier string `json:"targetIdentifier"`
	TargetVersion    string `json:"targetVersion,omitempty"`
}

type ListingRevision struct {
	ID                    string         `json:"id"`
	TenantID              string         `json:"tenantId"`
	ListingID             string         `json:"listingId"`
	RevisionNumber        int            `json:"revisionNumber"`
	VersionRevisionID     string         `json:"versionRevisionId"`
	DisplayName           string         `json:"displayName"`
	Description           string         `json:"description"`
	Tags                  []string       `json:"tags,omitempty"`
	Categories            []string       `json:"categories,omitempty"`
	RepresentativeQueries []string       `json:"representativeQueries,omitempty"`
	PublisherID           string         `json:"publisherId"`
	DocumentationURL      string         `json:"documentationUrl,omitempty"`
	SupportURL            string         `json:"supportUrl,omitempty"`
	License               string         `json:"license,omitempty"`
	TermsURL              string         `json:"termsUrl,omitempty"`
	Visibility            Visibility     `json:"visibility"`
	Relationships         []Relationship `json:"relationships,omitempty"`
	Extensions            map[string]any `json:"extensions,omitempty"`
	Digest                string         `json:"digest"`
	CreatedBy             string         `json:"createdBy"`
	CreatedAt             time.Time      `json:"createdAt"`
}

type EvidenceState string

const (
	EvidenceComplete EvidenceState = "complete"
	EvidenceBlocked  EvidenceState = "blocked"
)

type EvidenceSet struct {
	ID                 string              `json:"id"`
	TenantID           string              `json:"tenantId"`
	SubjectDigest      string              `json:"subjectDigest"`
	Digest             string              `json:"digest"`
	State              EvidenceState       `json:"state"`
	Findings           []Finding           `json:"findings,omitempty"`
	ValidationProfile  string              `json:"validationProfile"`
	PolicyVersion      string              `json:"policyVersion"`
	LegalRights        LegalRightsSummary  `json:"legalRights"`
	Trust              TrustSummary        `json:"trust"`
	ExternalRetrievals []ExternalRetrieval `json:"externalRetrievals,omitempty"`
	ExpiresAt          time.Time           `json:"expiresAt,omitzero"`
	CreatedAt          time.Time           `json:"createdAt"`
}

type LegalRightsState string

const (
	LegalRightsNotAssessed LegalRightsState = "not_assessed"
)

// LegalRightsSummary exposes only the implemented assessment state.
type LegalRightsSummary struct {
	State LegalRightsState `json:"state"`
}

// UnmarshalJSON accepts only the closed not_assessed rights summary and
// rejects duplicate keys, unknown fields and unsupported assessment claims.
func (s *LegalRightsSummary) UnmarshalJSON(data []byte) error {
	var stored struct {
		State LegalRightsState `json:"state"`
	}
	if err := jsondoc.Validate(data); err != nil {
		return err
	}
	if err := jsondoc.Decode(data, &stored); err != nil {
		return err
	}
	if stored.State != LegalRightsNotAssessed {
		return fmt.Errorf("unsupported legal rights state %q", stored.State)
	}
	*s = LegalRightsSummary{State: LegalRightsNotAssessed}
	return nil
}

type TrustState string

const (
	TrustNotAssessed TrustState = "not_assessed"
	TrustUnsupported TrustState = "unsupported"
	TrustUnverified  TrustState = "unverified"
	TrustVerified    TrustState = "verified"
	TrustFailed      TrustState = "failed"
	TrustExpired     TrustState = "expired"
)

type TrustSummary struct {
	State  TrustState `json:"state"`
	Reason string     `json:"reason,omitempty"`
}

type SubmissionState string

const (
	SubmissionSubmitted       SubmissionState = "submitted"
	SubmissionApproved        SubmissionState = "approved"
	SubmissionChangesRequired SubmissionState = "changes_required"
	SubmissionRejected        SubmissionState = "rejected"
)

type ReviewDecision struct {
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

type ReviewSubmission struct {
	// Queue-only context, projected under current independent metadata/content permissions.
	CanonicalIdentifier        string `json:"canonicalIdentifier,omitempty"`
	SubmittedDisplayName       string `json:"submittedDisplayName,omitempty"`
	ListingConcurrencyRevision int64  `json:"listingConcurrencyRevision,omitempty"`
	// NamespaceID is a current authorized read projection, not retained submission evidence.
	NamespaceID       string           `json:"namespaceId,omitempty"`
	ID                string           `json:"id"`
	TenantID          string           `json:"tenantId"`
	ListingID         string           `json:"listingId"`
	VersionRevisionID string           `json:"versionRevisionId"`
	ListingRevisionID string           `json:"listingRevisionId"`
	EvidenceSetID     string           `json:"evidenceSetId"`
	EvidenceDigest    string           `json:"evidenceDigest"`
	PolicyVersion     string           `json:"policyVersion"`
	State             SubmissionState  `json:"state"`
	SubmittedBy       string           `json:"submittedBy"`
	Decisions         []ReviewDecision `json:"decisions,omitempty"`
	Revision          int64            `json:"revision"`
	SubmittedAt       time.Time        `json:"submittedAt"`
	DecidedAt         *time.Time       `json:"decidedAt,omitempty"`
}

type ReleaseState string

const (
	ReleasePublished  ReleaseState = "published"
	ReleaseDeprecated ReleaseState = "deprecated"
	ReleaseRetired    ReleaseState = "retired"
	ReleaseSuperseded ReleaseState = "superseded"
	ReleaseRevoked    ReleaseState = "revoked"
)

type ListingRelease struct {
	ID                   string       `json:"id"`
	TenantID             string       `json:"tenantId"`
	ListingID            string       `json:"listingId"`
	SubmissionID         string       `json:"submissionId"`
	VersionRevisionID    string       `json:"versionRevisionId"`
	ListingRevisionID    string       `json:"listingRevisionId"`
	State                ReleaseState `json:"state"`
	ReplacementReleaseID string       `json:"replacementReleaseId,omitempty"`
	DispositionReason    string       `json:"dispositionReason,omitempty"`
	SunsetAt             *time.Time   `json:"sunsetAt,omitempty"`
	ActivatedBy          string       `json:"activatedBy"`
	Revision             int64        `json:"revision"`
	ActivatedAt          time.Time    `json:"activatedAt"`
	UpdatedAt            time.Time    `json:"updatedAt"`
}

type CatalogRecord struct {
	Display         *CatalogPresentation `json:"display,omitempty"`
	Resource        Resource             `json:"resource"`
	Version         ResourceVersion      `json:"version"`
	VersionRevision VersionRevision      `json:"versionRevision"`
	Listing         CatalogListing       `json:"listing"`
	ListingRevision ListingRevision      `json:"listingRevision"`
	Release         ListingRelease       `json:"release"`
	Evidence        EvidenceSet          `json:"evidence"`
	RelevanceScore  int                  `json:"relevanceScore,omitempty"`
	Source          string               `json:"source"`
}

type AuditEvent = audit.Record

type AuditExport = audit.Export

type OutboxEvent struct {
	CausalTrace    TraceContext    `json:"-"`
	ID             string          `json:"id"`
	TenantID       string          `json:"tenantId"`
	Type           string          `json:"type"`
	Subject        string          `json:"subject"`
	Data           json.RawMessage `json:"data"`
	Internal       bool            `json:"-"`
	CreatedAt      time.Time       `json:"createdAt"`
	PublishedAt    *time.Time      `json:"publishedAt,omitempty"`
	DispatchAfter  *time.Time      `json:"dispatchAfter,omitempty"`
	LeaseOwner     string          `json:"-"`
	LeaseExpiresAt *time.Time      `json:"-"`
}

type Page[T any] struct {
	Tier           string `json:"tier,omitempty"`
	Profile        string `json:"profile,omitempty"`
	FallbackReason string `json:"fallbackReason,omitempty"`
	PartialIndex   bool   `json:"partialIndex,omitempty"`
	Items          []T    `json:"items"`
	NextCursor     string `json:"nextCursor,omitempty"`
	Total          int    `json:"total"`
}

// SearchOrder selects deterministic column ordering instead of relevance.
type SearchOrder string

const (
	SearchOrderNameAsc       SearchOrder = "name_asc"
	SearchOrderNameDesc      SearchOrder = "name_desc"
	SearchOrderKindAsc       SearchOrder = "kind_asc"
	SearchOrderKindDesc      SearchOrder = "kind_desc"
	SearchOrderPublisherAsc  SearchOrder = "publisher_asc"
	SearchOrderPublisherDesc SearchOrder = "publisher_desc"
)

type SearchQuery struct {
	Order       SearchOrder        `json:"order,omitempty"`
	Origins     []SearchResultType `json:"origins,omitempty"`
	NamespaceID string             `json:"namespaceId,omitempty"`
	Text        string             `json:"text"`
	Kinds       []Kind             `json:"kinds,omitempty"`
	Tags        []string           `json:"tags,omitempty"`
	Categories  []string           `json:"categories,omitempty"`
	License     string             `json:"license,omitempty"`
	Publisher   string             `json:"publisher,omitempty"`
	Visibility  Visibility         `json:"visibility,omitempty"`
	Limit       int                `json:"limit,omitempty"`
	Cursor      string             `json:"cursor,omitempty"`
}

type CatalogSearchPosition struct {
	SortValue   string `json:"o,omitempty"`
	Score       int    `json:"s"`
	DisplayName string `json:"n"`
	ResultID    string `json:"i"`
}

type CatalogSearchRepositoryRequest struct {
	// IdentityProbes requests only exact matching identities, before paging.
	IdentityProbes     []IndexedIdentity
	Query              SearchQuery
	Limit              int
	After              *CatalogSearchPosition
	ExpectedGeneration string
}

type CatalogSearchRepositoryPage struct {
	Matches    []IndexedIdentity
	Items      []CatalogSummary
	Total      int
	Generation string
	HasMore    bool
}

type SearchResultType string

const (
	SearchResultLocal   SearchResultType = "local"
	SearchResultIndexed SearchResultType = "indexed"
	SearchResultLive    SearchResultType = "live"
)

type IndexedSearchEvidence struct {
	PresentationSourceRevision int64    `json:"presentationSourceRevision"`
	PresentationSourceID       string   `json:"presentationSourceId"`
	PresentationGeneration     int64    `json:"presentationGeneration"`
	ObservationGroupID         string   `json:"observationGroupId"`
	NormalizedEntryDigest      string   `json:"normalizedEntryDigest"`
	SourceIDs                  []string `json:"sourceIds"`
	SourceCount                int      `json:"sourceCount"`
	SourcesTruncated           bool     `json:"sourcesTruncated"`
	Conflict                   bool     `json:"conflict"`
	SameSourceConflict         bool     `json:"sameSourceConflict"`
}

type LiveSearchEvidence struct {
	ArtworkOverrideDigest  string          `json:"-"`
	ArtworkAdmitted        bool            `json:"-"`
	SourceRevision         int64           `json:"sourceRevision"`
	PresentationGeneration int64           `json:"presentationGeneration"`
	PolicyDigest           string          `json:"-"`
	CredentialRevision     string          `json:"-"`
	SourceID               string          `json:"sourceId"`
	Source                 string          `json:"source"`
	Entry                  json.RawMessage `json:"entry"`
	UpstreamScore          int             `json:"-"`
}

type RatingSummary struct {
	Count   int64    `json:"count"`
	Average *float64 `json:"average,omitempty"`
	Receipt string   `json:"receipt,omitempty"`
}

type PeerRatingSummary struct {
	Reporter   string    `json:"reporter"`
	Count      int64     `json:"count"`
	Average    float64   `json:"average"`
	MeasuredAt time.Time `json:"measuredAt"`
}
type ImportedResource struct {
	ResourceID   string `json:"resourceId"`
	ChangedEntry bool   `json:"changedEntry"`
}
type CatalogSearchResult struct {
	PeerRatings    *PeerRatingSummary     `json:"peerRatings,omitempty"`
	ImportedHere   *ImportedResource      `json:"importedHere,omitempty"`
	Ratings        *RatingSummary         `json:"ratings,omitempty"`
	Artwork        *artwork.Presentation  `json:"artwork,omitempty"`
	ID             string                 `json:"id"`
	Type           SearchResultType       `json:"type"`
	Identifier     string                 `json:"identifier"`
	Version        string                 `json:"version,omitempty"`
	VersionPresent bool                   `json:"versionPresent"`
	Kind           Kind                   `json:"kind"`
	DisplayName    string                 `json:"displayName"`
	Description    string                 `json:"description,omitempty"`
	Tags           []string               `json:"tags"`
	Publisher      string                 `json:"publisher,omitempty"`
	Score          int                    `json:"score"`
	Local          *CatalogSearchLocal    `json:"local,omitempty"`
	Indexed        *IndexedSearchEvidence `json:"indexed,omitempty"`
	Live           *LiveSearchEvidence    `json:"live,omitempty"`
}

type CatalogSearchDiagnostic struct {
	SourceID string `json:"sourceId,omitempty"`
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
}

type CatalogSearchResponse struct {
	semantic       *semanticPageState
	FallbackReason string                    `json:"fallbackReason,omitempty"`
	PartialIndex   bool                      `json:"partialIndex,omitempty"`
	Items          []CatalogSearchResult     `json:"items"`
	NextCursor     string                    `json:"nextCursor,omitempty"`
	DurableTotal   int                       `json:"durableTotal"`
	LiveCount      int                       `json:"liveCount"`
	Profile        string                    `json:"profile"`
	Generation     string                    `json:"generation"`
	Tier           string                    `json:"tier"`
	Partial        bool                      `json:"partial"`
	Diagnostics    []CatalogSearchDiagnostic `json:"diagnostics,omitempty"`
}

type FederatedCatalogSearchRequest struct {
	IdentityProbes     []IndexedIdentity
	LiveLimit          int
	Query              SearchQuery
	Limit              int
	After              *CatalogSearchPosition
	ExpectedGeneration string
	IncludeLive        bool
	ExactIdentity      *IndexedIdentity
}

// IndexedIdentity selects the current reader-visible projection of one exact
// external identity. It never selects an upstream or requests raw evidence.
type IndexedIdentity struct {
	Identifier     string
	Version        string
	VersionPresent bool
}

type FederatedCatalogSearchPage struct {
	Matches           []IndexedIdentity
	ReplayFingerprint string
	Indexed           []CatalogSearchResult
	IndexedTotal      int
	Generation        string
	HasMore           bool
	Live              []CatalogSearchResult
	Partial           bool
	Diagnostics       []CatalogSearchDiagnostic
}

type FederatedCatalogSearcher interface {
	SearchFederatedCatalog(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error)
}

// CatalogSearchSnapshotReader reads both durable Catalog projections from one
// tenant-scoped snapshot. It performs no live/outbound discovery.
type CatalogSearchSnapshotReader interface {
	SearchCatalogSnapshot(context.Context, string, CatalogSearchRepositoryRequest, FederatedCatalogSearchRequest) (CatalogSearchSnapshot, error)
}

type CatalogSearchSnapshot struct {
	Local   CatalogSearchRepositoryPage
	Indexed FederatedCatalogSearchPage
}

// FederatedCatalogLiveSearcher supplies only transient discovery after the
// durable snapshot has closed; it must not repeat indexed database selection.
type FederatedCatalogLiveSearcher interface {
	SearchLiveCatalog(context.Context, FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error)
}

type FacetRequest struct {
	Field    string `json:"field"`
	Limit    int    `json:"limit,omitempty"`
	MinCount int    `json:"minCount,omitempty"`
}

type ExploreRequest struct {
	Query  SearchQuery    `json:"query"`
	Facets []FacetRequest `json:"facets"`
}

type FacetBucket struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type FacetResult struct {
	Buckets    []FacetBucket `json:"buckets"`
	OtherCount int           `json:"otherCount,omitempty"`
}

type ExploreResponse struct {
	Tier           string                 `json:"tier,omitempty"`
	Profile        string                 `json:"profile,omitempty"`
	FallbackReason string                 `json:"fallbackReason,omitempty"`
	PartialIndex   bool                   `json:"partialIndex,omitempty"`
	Matched        int                    `json:"matched"`
	Facets         map[string]FacetResult `json:"facets"`
}

// IncludesOrigin applies the selected provenance set without granting upstream authority.
// A namespace selects local releases; declaration filters apply to every selected origin.
func (q SearchQuery) IncludesOrigin(origin SearchResultType) bool {
	if q.NamespaceID != "" && origin != SearchResultLocal {
		return false
	}
	if q.Origins == nil {
		return true
	}
	for _, selected := range q.Origins {
		if selected == origin {
			return true
		}
	}
	return false
}
