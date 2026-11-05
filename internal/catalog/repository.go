// Repository and dependency ports express tenant-scoped persistence, transition
// preconditions and bounded external work without infrastructure imports.

package catalog

import (
	"context"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
)

const (
	// MaxPublicationRecords bounds the complete set hydrated for one host and
	// audience. It is a safety ceiling, not a pagination size.
	MaxPublicationRecords = 1000
	// MaxPublicationAffectedReleases bounds synchronous safety withdrawal after
	// one external representation becomes invalid.
	MaxPublicationAffectedReleases = 1000
	// MaxPublicationProjectionBytes bounds the aggregate governed records
	// hydrated for one publication build or eligibility revalidation.
	MaxPublicationProjectionBytes = 32 << 20
)

// CreateResourceParams groups a new resource with its atomic audit and outbox evidence.
type CreateResourceParams struct {
	Resource Resource
	Audit    AuditEvent
	Outbox   OutboxEvent
}

// UpdateNamespaceParams carries a namespace replacement and the revision it must match.
type UpdateNamespaceParams struct {
	Namespace        Namespace
	ExpectedRevision int64
	Audit            AuditEvent
	Outbox           OutboxEvent
}

// UpdatePublisherParams carries a publisher replacement and the revision it must match.
type UpdatePublisherParams struct {
	Publisher        Publisher
	ExpectedRevision int64
	Audit            AuditEvent
	Outbox           OutboxEvent
}

// DeleteResourceParams identifies a tenant-scoped resource deletion, its revision
// precondition and the evidence retained for the transition.
type DeleteResourceParams struct {
	TenantID         string
	ResourceID       string
	ExpectedRevision int64
	Actor            string
	Reason           string
	At               time.Time
	Audit            AuditEvent
	Outbox           OutboxEvent
}

// CreateVersionParams groups a version and immutable revision with retained
// documents and validation evidence. When appending a draft revision,
// ExpectedRevision must match the existing version's revision counter.
type CreateVersionParams struct {
	Version          ResourceVersion
	VersionRevision  VersionRevision
	Documents        []DocumentBlob
	Evidence         EvidenceSet
	ExpectedRevision int64
	Audit            AuditEvent
	Outbox           OutboxEvent
}

// CreateValidationRunParams appends validation evidence to an existing immutable
// version revision without replacing the representation it evaluated.
type CreateValidationRunParams struct {
	TenantID          string
	VersionRevisionID string
	Evidence          EvidenceSet
	Audit             AuditEvent
	Outbox            OutboxEvent
}

// CreateListingParams groups a stable listing with its creation evidence.
type CreateListingParams struct {
	Listing CatalogListing
	Audit   AuditEvent
	Outbox  OutboxEvent
}

// CreateListingRevisionParams appends listing content only if the stable
// listing still has ExpectedRevision.
type CreateListingRevisionParams struct {
	Revision         ListingRevision
	ExpectedRevision int64
	Audit            AuditEvent
	Outbox           OutboxEvent
}

// AdoptExternalObservationParams groups the locally governed resource, version
// and listing created atomically from an external observation.
type AdoptExternalObservationParams struct {
	SourceAdmissionRevision int64
	Resource                Resource
	Version                 ResourceVersion
	VersionRevision         VersionRevision
	Documents               []DocumentBlob
	Evidence                EvidenceSet
	Listing                 CatalogListing
	ListingRevision         ListingRevision
	Audit                   AuditEvent
	Outbox                  OutboxEvent
}

// SubmitParams records a review submission and its transition evidence.
type SubmitParams struct {
	Submission ReviewSubmission
	Audit      AuditEvent
	Outbox     OutboxEvent
}

// DecideSubmissionParams binds a review decision to the expected submission
// revision and validation evidence so concurrent changes cannot be overlooked.
type DecideSubmissionParams struct {
	ActorMembershipID            string
	ActorRealmID                 string
	TenantID                     string
	SubmissionID                 string
	ExpectedRevision             int64
	ExpectedValidationEvidenceID string
	Actor                        string
	Action                       string
	Reason                       string
	At                           time.Time
	Audit                        AuditEvent
	Outbox                       OutboxEvent
}

// PublishParams binds publication to a reviewed submission, expected listing
// revision and validation evidence, including disposition of the prior release.
type PublishParams struct {
	TenantID                     string
	ListingID                    string
	SubmissionID                 string
	ExpectedRevision             int64
	ExpectedValidationEvidenceID string
	PriorDisposition             ReleaseState
	PriorReason                  string
	Actor                        string
	Release                      ListingRelease
	Audit                        AuditEvent
	Outbox                       OutboxEvent
}

// TransitionReleaseParams requests a revision-checked release state change
// with its reason, optional replacement or sunset, and transition evidence.
type TransitionReleaseParams struct {
	TenantID         string
	ReleaseID        string
	ExpectedRevision int64
	TargetState      ReleaseState
	Reason           string
	ReplacementID    string
	SunsetAt         *time.Time
	Actor            string
	At               time.Time
	Audit            AuditEvent
	Outbox           OutboxEvent
}

// NamespaceListRequest selects a page after a slug and ID cursor.
type NamespaceListRequest struct {
	Slug        string
	DisplayName string
	AfterSlug   string
	AfterID     string
	Limit       int
}

// PublisherListRequest selects a page after a display-name and ID cursor.
type PublisherListRequest struct {
	DisplayName      string
	DeclaredDomain   string
	AfterDisplayName string
	AfterID          string
	Limit            int
}

// ResourceListRequest selects a resource page, optionally restricted by kind,
// after an update-time and ID cursor.
type ResourceListRequest struct {
	CanonicalIdentifier string
	Kind                Kind
	AfterUpdatedAt      *time.Time
	AfterID             string
	Limit               int
}

// AuditEventListRequest filters an audit page after an occurrence-time and ID cursor.
type AuditEventListRequest struct {
	AfterOccurredAt *time.Time
	AfterID         string
	Filters         audit.Query
	Limit           int
}

// AuditExportRequest defines a bounded audit export and the access event to
// retain for it. Cutoff fixes the eligible event boundary for the export.
type AuditExportRequest struct {
	Filters     audit.Query
	Cutoff      time.Time
	GeneratedAt time.Time
	Access      AuditEvent
}

// AuditExportInput carries caller-selected filters and the reason for audit access.
type AuditExportInput struct {
	Filters audit.Query
	Reason  string
}

// PublicationRecordRequest bounds the complete eligible record set used to
// construct or revalidate one immutable publication snapshot. Repositories
// return at most Limit records and set Overflow when another eligible record
// exists; callers must fail closed rather than publish a partial Catalog.
type PublicationRecordRequest struct {
	IncludeTenant bool
	Limit         int
}

// PublicationRecordPage holds eligible records and signals whether the complete
// set exceeded the request limit. Overflow is a failure boundary, not pagination.
type PublicationRecordPage struct {
	Records  []CatalogRecord
	Overflow bool
}

// Repository is the Catalog system-of-record boundary. Mutation methods accept
// the audit and outbox evidence that must commit atomically with domain state.
type Repository interface {
	// GetOriginatingPrincipal binds an exact Resource or submission to retained
	// immutable creation evidence, failing closed for missing/ambiguous identity.
	GetOriginatingPrincipal(context.Context, string, string, string) (OriginatingPrincipal, error)
	// Ping reports whether the repository can safely serve runtime work.
	Ping(context.Context) error
	// CreateNamespace inserts a namespace with its transition evidence.
	CreateNamespace(context.Context, Namespace, AuditEvent, OutboxEvent) (Namespace, error)
	// GetNamespace looks up a namespace by tenant ID and namespace ID.
	GetNamespace(context.Context, string, string) (Namespace, error)
	// ListNamespaces returns a tenant's page in slug and ID order.
	ListNamespaces(context.Context, string, NamespaceListRequest) ([]Namespace, error)
	// UpdateNamespace replaces mutable namespace fields after checking the revision.
	UpdateNamespace(context.Context, UpdateNamespaceParams) (Namespace, error)
	// CreatePublisher inserts a publisher with its transition evidence.
	CreatePublisher(context.Context, Publisher, AuditEvent, OutboxEvent) (Publisher, error)
	// GetPublisher looks up a publisher by tenant ID and publisher ID.
	GetPublisher(context.Context, string, string) (Publisher, error)
	// ListPublishers returns the tenant's publisher inventory without a page cursor.
	ListPublishers(context.Context, string) ([]Publisher, error)
	// ListPublisherPage returns publishers in display-name and ID order.
	ListPublisherPage(context.Context, string, PublisherListRequest) ([]Publisher, error)
	// UpdatePublisher replaces mutable publisher fields after checking the revision.
	UpdatePublisher(context.Context, UpdatePublisherParams) (Publisher, error)
	// ApprovePublisherPublicationDomain grants a domain to the tenant's publisher
	// identified by the first two strings; the third is the domain.
	ApprovePublisherPublicationDomain(context.Context, PublisherDomainApproval) (Publisher, error)
	ReadPublisherDomainVerification(context.Context, string, string, string) (Publisher, PublisherDomainVerification, error)
	SavePublisherDomainVerification(context.Context, PublisherDomainMutation) (PublisherDomainVerification, error)
	// RevokePublisherPublicationDomain removes the domain approval and returns
	// affected release IDs for withdrawal from active publication snapshots.
	RevokePublisherPublicationDomain(context.Context, string, string, string, int64, AuditEvent, OutboxEvent) (Publisher, []string, error)

	// CreateResource inserts a stable resource and its transition evidence.
	CreateResource(context.Context, CreateResourceParams) (Resource, error)
	// GetResource looks up a resource by tenant ID and resource ID.
	GetResource(context.Context, string, string) (Resource, error)
	// GetResourceByIdentifier resolves a resource's canonical identifier within a tenant.
	GetResourceByIdentifier(context.Context, string, string) (Resource, error)
	// ListResources returns a tenant's filtered resource page.
	ListResources(context.Context, string, ResourceListRequest) ([]Resource, error)
	// DeleteResource applies lifecycle and revision checks before recording deletion.
	DeleteResource(context.Context, DeleteResourceParams) (Resource, error)
	// CreateVersion inserts a version, its initial immutable revision and evidence.
	CreateVersion(context.Context, CreateVersionParams) (ResourceVersion, VersionRevision, EvidenceSet, error)
	// CreateVersionRevision appends a draft revision and advances the version pointer.
	CreateVersionRevision(context.Context, CreateVersionParams) (ResourceVersion, VersionRevision, EvidenceSet, error)
	// GetVersion looks up a version by tenant ID and version ID.
	GetVersion(context.Context, string, string) (ResourceVersion, error)
	// ListVersions pages versions by tenant ID and parent resource ID.
	ListVersions(context.Context, string, string, HistoryPageRequest) ([]ResourceVersion, error)
	// ListVersionRevisions pages revision summaries by tenant ID and parent version ID.
	ListVersionRevisions(context.Context, string, string, HistoryPageRequest) ([]RevisionSummary, error)
	// ListListings pages listings by tenant ID and parent resource ID.
	ListListings(context.Context, string, string, HistoryPageRequest) ([]CatalogListing, error)
	// ListListingRevisions pages revision summaries by tenant ID and parent listing ID.
	ListListingRevisions(context.Context, string, string, HistoryPageRequest) ([]RevisionSummary, error)
	// ListSubmissions returns a tenant's filtered review-submission page.
	ListSubmissions(context.Context, string, SubmissionListRequest) ([]ReviewSubmission, error)
	// ListReleases pages release history by tenant ID and parent listing ID.
	ListReleases(context.Context, string, string, HistoryPageRequest) ([]ListingRelease, error)
	// GetVersionByLabel resolves a label under the specified tenant and resource.
	GetVersionByLabel(context.Context, string, string, string) (ResourceVersion, error)
	// GetVersionRevision returns the tenant's immutable revision and its current evidence.
	GetVersionRevision(context.Context, string, string) (VersionRevision, EvidenceSet, error)
	// GetValidationEvidence looks up retained evidence by tenant, revision and evidence ID.
	GetValidationEvidence(context.Context, string, string, string) (EvidenceSet, error)
	// CreateValidationRun retains a new validation result for an existing revision.
	CreateValidationRun(context.Context, CreateValidationRunParams) (EvidenceSet, error)
	// GetDocument resolves retained document bytes by tenant ID and document ID.
	GetDocument(context.Context, string, string) (DocumentBlob, error)
	// CreateListing inserts a stable listing with its transition evidence.
	CreateListing(context.Context, CreateListingParams) (CatalogListing, error)
	// GetListing looks up a listing by tenant ID and listing ID.
	GetListing(context.Context, string, string) (CatalogListing, error)
	// CreateListingRevision appends content and advances the listing's current revision.
	CreateListingRevision(context.Context, CreateListingRevisionParams) (CatalogListing, ListingRevision, error)
	// AdoptExternalObservation atomically creates local authoring records while
	// retaining the external observation's provenance; it does not publish them.
	AdoptExternalObservation(context.Context, AdoptExternalObservationParams) (ExternalObservationAdoption, error)
	// GetListingRevision looks up immutable content by tenant ID and revision ID.
	GetListingRevision(context.Context, string, string) (ListingRevision, error)
	// Submit retains a review submission against its referenced revisions.
	Submit(context.Context, SubmitParams) (ReviewSubmission, error)
	// GetSubmission looks up a submission by tenant ID and submission ID.
	GetSubmission(context.Context, string, string) (ReviewSubmission, error)
	// DecideSubmission applies a review decision and any resulting version transition.
	DecideSubmission(context.Context, DecideSubmissionParams) (ReviewSubmission, ResourceVersion, error)
	// Publish creates a release and returns the updated listing and optional prior release.
	Publish(context.Context, PublishParams) (CatalogListing, ListingRelease, *ListingRelease, error)
	// GetRelease looks up a release by tenant ID and release ID.
	GetRelease(context.Context, string, string) (ListingRelease, error)
	// TransitionRelease applies a permitted, revision-checked lifecycle change.
	TransitionRelease(context.Context, TransitionReleaseParams) (ListingRelease, error)

	// PublicationRecords selects a complete eligible set or reports overflow.
	PublicationRecords(context.Context, string, PublicationRecordRequest) (PublicationRecordPage, error)
	// PublishedReleaseIDsByVersionRevision selects release references by tenant
	// and revision ID. The boolean reports that the supplied limit was exceeded.
	PublishedReleaseIDsByVersionRevision(context.Context, string, string, int) ([]string, bool, error)
	// SearchCatalog executes the tenant's local discovery query and page selection.
	SearchCatalog(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error)
	// ExploreCatalog computes a tenant's discovery results and requested facets.
	ExploreCatalog(context.Context, string, SearchQuery, []FacetRequest) (ExploreResponse, error)
	// GetCatalogRecord resolves an eligible release-backed record by tenant and release ID.
	GetCatalogRecord(context.Context, string, string) (CatalogRecord, error)
	// HasPublishedIdentity checks local precedence for a tenant, identifier and
	// version label, including deprecated releases that remain discoverable.
	HasPublishedIdentity(context.Context, string, string, string) (bool, error)
	// AuditEvents returns a tenant's filtered audit page.
	AuditEvents(context.Context, string, AuditEventListRequest) ([]AuditEvent, error)
	// AppendAuditEvent retains audit-only evidence without manufacturing an outbox record.
	AppendAuditEvent(context.Context, AuditEvent) error
	// AppendEvidence atomically retains audit evidence and an optional outbox record.
	AppendEvidence(context.Context, AuditEvent, *OutboxEvent) error
	// ExportAuditEvents builds a bounded tenant export and records its access evidence.
	ExportAuditEvents(context.Context, string, AuditExportRequest) (AuditExport, error)
	// ClaimOutbox selects eligible records across tenants for the supplied owner,
	// current time, lease duration and batch limit.
	ClaimOutbox(context.Context, string, time.Time, time.Duration, int) ([]OutboxEvent, error)
	// RenewOutbox extends a lease identified by tenant, event and owner.
	RenewOutbox(context.Context, string, string, string, time.Time, time.Duration) error
	// MarkOutboxPublished completes delivery for the tenant, event and lease owner.
	MarkOutboxPublished(context.Context, string, string, string, time.Time) error
	// ReleaseOutbox releases a claim identified by tenant, event and owner.
	ReleaseOutbox(context.Context, string, string, string) error
	// DeferOutbox reschedules an owned tenant event using the supplied current
	// time and next-attempt time, without marking delivery complete.
	DeferOutbox(context.Context, string, string, string, time.Time, time.Time) error
}
