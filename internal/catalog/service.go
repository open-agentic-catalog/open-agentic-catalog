// Catalog application services authorize lifecycle commands and construct the
// immutable records, validation results and atomic evidence required by
// repositories.

package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	auditpkg "github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const currentValidationProfile = "catalog-validation-v7"

const MaxCatalogPageSize = 100

// MaxCatalogDocumentBytes is the hard per-revision budget for exact textual
// documents retained in PostgreSQL. The HTTP request limit may be lower.
const MaxCatalogDocumentBytes = 16 << 20

// ExternalRepresentationFreshness is the deliberately simple v1 validity
// window for evidence about a changeable anonymous HTTPS representation.
const ExternalRepresentationFreshness = 24 * time.Hour

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	New(time.Time) (string, error)
}

type Artifact struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	Digest    string    `json:"digest"`
	MediaType string    `json:"mediaType"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"createdAt"`
}

type ValidationResult struct {
	Plugin          *PluginInspection
	ProfileID       string
	NormalizedFacts map[string]any
	Unknown         map[string]any
	Findings        []Finding
	Package         *SkillPackageInspection
	Handoff         *SkillHandoff
}

// RepresentationValidator checks supplied metadata or package bytes without
// executing content. Findings are structural evidence, not publisher trust,
// rights assessment or permission to invoke the resource.
type RepresentationValidator interface {
	Validate(context.Context, Kind, RepresentationInput) ValidationResult
}

type ExternalRepresentationFetchRequest struct {
	TenantID         string
	URL              string
	MediaType        string
	MaxResponseBytes int64
}

type ExternalRepresentationFetchResult struct {
	RequestedURL   string
	FinalURL       string
	Status         int
	MediaType      string
	Body           []byte
	ContentDigest  string
	PolicyID       string
	PolicyRevision int64
	PolicyDigest   string
	RetrievedAt    time.Time
}

// ExternalRepresentationFetcher is the narrow domain port for anonymous URL
// representations. Runtime adapters must route it through the hardened shared
// fetch boundary without a tenant-selected network or credential profile.
type ExternalRepresentationFetcher interface {
	FetchExternalRepresentation(context.Context, ExternalRepresentationFetchRequest) (ExternalRepresentationFetchResult, error)
	PolicyDigest() string
}

// Service enforces Catalog authorization and lifecycle rules before delegating
// atomic persistence to Repository.
type Service struct {
	semantic                    *SemanticSearch
	repo                        Repository
	clock                       Clock
	ids                         IDGenerator
	validator                   RepresentationValidator
	diagnosticLimits            DiagnosticLimits
	externalFetcher             ExternalRepresentationFetcher
	artwork                     *artwork.Service
	cursorKey                   []byte
	policy                      string
	acknowledgeOutboxAtCreation bool
	publicationWithdrawer       PublicationWithdrawer
	publisherDomainPolicy       PublisherDomainPolicy
	publisherTXT                PublisherTXTResolver
	publisherDomainNonce        io.Reader
	publisherDomainLookups      chan struct{}
}

// PublicationWithdrawer removes public snapshots after a governed change makes
// their released content ineligible for discovery.
type PublicationWithdrawer interface {
	WithdrawPublicationDomain(context.Context, string, string, string, string, string) ([]string, error)
	WithdrawPublicationRelease(context.Context, string, string, string, string, string) ([]string, error)
}

// Options supplies the Catalog's persistence, validation, identity-generation,
// and external retrieval boundaries.
type Options struct {
	SemanticSearch   *SemanticSearch
	Repository       Repository
	Clock            Clock
	IDs              IDGenerator
	Validator        RepresentationValidator
	DiagnosticLimits DiagnosticLimits
	ExternalFetcher  ExternalRepresentationFetcher
	Artwork          *artwork.Service
	CursorKey        []byte
	Policy           string
	// AcknowledgeOutboxAtCreation retains atomic outbox evidence but makes it
	// terminal when no external delivery capability is configured.
	AcknowledgeOutboxAtCreation bool
	PublicationWithdrawer       PublicationWithdrawer
	PublisherDomainPolicy       PublisherDomainPolicy
	PublisherTXT                PublisherTXTResolver
	PublisherDomainNonce        io.Reader
}

// NewService validates required domain dependencies and copies the cursor key.
// An empty policy selects the default validation policy.
func NewService(options Options) (*Service, error) {
	if options.Repository == nil || options.Clock == nil || options.IDs == nil || options.Validator == nil {
		return nil, E(CodeInvalidArgument, "catalog service dependencies are required")
	}
	if len(options.CursorKey) < 32 {
		return nil, E(CodeInvalidArgument, "catalog cursor key must contain at least 32 bytes")
	}
	if options.Policy == "" {
		options.Policy = "default-v1"
	}
	if options.DiagnosticLimits == (DiagnosticLimits{}) {
		options.DiagnosticLimits = DefaultDiagnosticLimits()
	}
	if err := options.DiagnosticLimits.Validate(); err != nil {
		return nil, err
	}
	if options.PublisherDomainPolicy == (PublisherDomainPolicy{}) {
		options.PublisherDomainPolicy = DefaultPublisherDomainPolicy()
	}
	if err := options.PublisherDomainPolicy.Validate(); err != nil {
		return nil, err
	}
	if options.PublisherDomainPolicy.Mode != DomainVerificationDisabled && options.PublisherTXT == nil {
		return nil, E(CodeInvalidArgument, "publisher DNS resolver is required when verification is enabled")
	}
	return &Service{
		semantic:                    options.SemanticSearch,
		repo:                        options.Repository,
		clock:                       options.Clock,
		ids:                         options.IDs,
		validator:                   options.Validator,
		diagnosticLimits:            options.DiagnosticLimits,
		externalFetcher:             options.ExternalFetcher,
		artwork:                     options.Artwork,
		cursorKey:                   append([]byte(nil), options.CursorKey...),
		policy:                      options.Policy,
		acknowledgeOutboxAtCreation: options.AcknowledgeOutboxAtCreation,
		publicationWithdrawer:       options.PublicationWithdrawer,
		publisherDomainPolicy:       options.PublisherDomainPolicy,
		publisherTXT:                options.PublisherTXT,
		publisherDomainNonce:        publisherDomainNonce(options.PublisherDomainNonce),
		publisherDomainLookups:      make(chan struct{}, 4),
	}, nil
}

type CreateNamespaceInput struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
}

type PatchNamespaceInput struct {
	DisplayName *string `json:"displayName,omitempty"`
	Description *string `json:"description,omitempty"`
	Reason      string  `json:"reason"`
}

// CreateNamespace requires tenant-administrator authority and persists a
// normalized namespace with its transition evidence.
func (s *Service) CreateNamespace(ctx context.Context, input CreateNamespaceInput) (Namespace, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "namespace.create", "")
	if err != nil {
		return Namespace{}, err
	}
	input.Slug = strings.ToLower(strings.TrimSpace(input.Slug))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Description = strings.TrimSpace(input.Description)
	if !validSlug(input.Slug) || input.DisplayName == "" || !validTextLength(input.DisplayName, 160) || !validTextLength(input.Description, 2000) {
		return Namespace{}, E(CodeInvalidArgument, "a valid namespace slug and display name are required")
	}
	now := s.clock.Now()
	idValue, err := s.ids.New(now)
	if err != nil {
		return Namespace{}, Wrap(CodeInternal, "create namespace identifier", err)
	}
	namespace := Namespace{ID: idValue, TenantID: actor.TenantID, Slug: input.Slug, DisplayName: input.DisplayName, Description: input.Description, Revision: 1, CreatedAt: now, UpdatedAt: now}
	audit, outbox, err := s.evidence(ctx, actor, "namespace.created", "namespace", namespace.ID, namespace)
	if err != nil {
		return Namespace{}, err
	}
	return s.repo.CreateNamespace(ctx, namespace, audit, outbox)
}

// ListNamespaces validates the tenant-reader page request and returns up to
// limit plus one rows for continuation detection.
func (s *Service) ListNamespaces(ctx context.Context, request NamespaceListRequest) ([]Namespace, error) {
	ctx, actor, err := collectionContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Limit < 1 || request.Limit > MaxCatalogPageSize || request.AfterSlug == "" && request.AfterID != "" || request.AfterSlug != "" && !identity.ValidTenantID(request.AfterID) {
		return nil, E(CodeInvalidArgument, "namespace page position or limit is invalid")
	}
	request, err = request.NormalizeFilters()
	if err != nil {
		return nil, err
	}
	request.Limit++
	return s.repo.ListNamespaces(ctx, actor.TenantID, request)
}

// GetNamespace validates the UUID and returns a namespace visible to the tenant
// reader.
func (s *Service) GetNamespace(ctx context.Context, namespaceID string) (Namespace, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "namespace.read", namespaceID)
	if err != nil {
		return Namespace{}, err
	}
	if !identity.ValidTenantID(namespaceID) {
		return Namespace{}, E(CodeInvalidArgument, "namespaceId must be a UUID")
	}
	return s.repo.GetNamespace(ctx, actor.TenantID, namespaceID)
}

// UpdateNamespace changes administrator-owned display fields under the expected
// revision and a required audit reason.
func (s *Service) UpdateNamespace(ctx context.Context, namespaceID string, input PatchNamespaceInput, expectedRevision int64) (Namespace, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "namespace.manage", namespaceID)
	if err != nil {
		return Namespace{}, err
	}
	if !identity.ValidTenantID(namespaceID) || expectedRevision <= 0 || input.DisplayName == nil && input.Description == nil {
		return Namespace{}, E(CodeInvalidArgument, "a namespace UUID, current revision, bounded reason, and at least one changed field are required")
	}
	input.Reason, err = normalizeAuditReason("namespace.updated", input.Reason, "namespace update reason is invalid")
	if err != nil {
		return Namespace{}, err
	}
	current, err := s.GetNamespace(ctx, namespaceID)
	if err != nil {
		return Namespace{}, err
	}
	if current.Revision != expectedRevision {
		return Namespace{}, E(CodePreconditionFailed, "namespace revision does not match")
	}
	updated := current
	if input.DisplayName != nil {
		updated.DisplayName = strings.TrimSpace(*input.DisplayName)
		if updated.DisplayName == "" || !validTextLength(updated.DisplayName, 160) {
			return Namespace{}, E(CodeInvalidArgument, "namespace displayName must contain at most 160 characters")
		}
	}
	if input.Description != nil {
		updated.Description = strings.TrimSpace(*input.Description)
		if !validTextLength(updated.Description, 2000) {
			return Namespace{}, E(CodeInvalidArgument, "namespace description must contain at most 2000 characters")
		}
	}
	if updated.DisplayName == current.DisplayName && updated.Description == current.Description {
		return Namespace{}, E(CodeInvalidArgument, "namespace patch contains no changes")
	}
	updated.Revision = expectedRevision + 1
	updated.UpdatedAt = mutationTime(s.clock.Now(), current.UpdatedAt)
	audit, outbox, err := s.evidenceAt(ctx, actor, "namespace.updated", "namespace", namespaceID, input.Reason, updated, updated.UpdatedAt)
	if err != nil {
		return Namespace{}, err
	}
	return s.repo.UpdateNamespace(ctx, UpdateNamespaceParams{Namespace: updated, ExpectedRevision: expectedRevision, Audit: audit, Outbox: outbox})
}

type CreatePublisherInput struct {
	DisplayName string   `json:"displayName"`
	Domains     []string `json:"domains"`
}

type PatchPublisherInput struct {
	DisplayName *string   `json:"displayName,omitempty"`
	Domains     *[]string `json:"domains,omitempty"`
	Reason      string    `json:"reason"`
}

// CreatePublisher validates publisher metadata and creates an attributed
// tenant-owned publisher.
func (s *Service) CreatePublisher(ctx context.Context, input CreatePublisherInput) (Publisher, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "publisher.create", "")
	if err != nil {
		return Publisher{}, err
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.DisplayName == "" || !validTextLength(input.DisplayName, 160) || len(input.Domains) == 0 || len(input.Domains) > 64 {
		return Publisher{}, E(CodeInvalidArgument, "publisher display name and at least one domain are required")
	}
	domains, err := normalizePublisherDomains(input.Domains)
	if err != nil {
		return Publisher{}, err
	}
	now := s.clock.Now()
	idValue, err := s.ids.New(now)
	if err != nil {
		return Publisher{}, Wrap(CodeInternal, "create publisher identifier", err)
	}
	publisher := Publisher{ID: idValue, TenantID: actor.TenantID, DisplayName: input.DisplayName, Domains: domains, ApprovedPublicationDomains: []string{}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	audit, outbox, err := s.evidence(ctx, actor, "publisher.created", "publisher", publisher.ID, publisher)
	if err != nil {
		return Publisher{}, err
	}
	return s.repo.CreatePublisher(ctx, publisher, audit, outbox)
}

// ApprovePublisherPublicationDomain authorizes an administrator to bind a
// normalized publication domain under the current publisher revision.
func (s *Service) ApprovePublisherPublicationDomain(ctx context.Context, publisherID, domain, reason string, expectedRevision int64) (Publisher, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "publisher.approve_domain", "")
	if err != nil {
		return Publisher{}, err
	}
	reason, err = normalizeAuditReason("publisher.publication_domain_approved", reason, "publication-domain approval reason is invalid")
	if err != nil {
		return Publisher{}, err
	}
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if !validDomain(domain) {
		return Publisher{}, E(CodeInvalidArgument, "publication domain is invalid")
	}
	current, err := s.repo.GetPublisher(ctx, actor.TenantID, publisherID)
	if err != nil {
		return Publisher{}, err
	}
	at := mutationTime(s.clock.Now(), current.UpdatedAt)
	audit, outbox, err := s.evidenceAt(ctx, actor, "publisher.publication_domain_approved", "publisher", publisherID, reason, map[string]string{"domain": domain}, at)
	if err != nil {
		return Publisher{}, err
	}
	return s.repo.ApprovePublisherPublicationDomain(ctx, PublisherDomainApproval{TenantID: actor.TenantID, PublisherID: publisherID, Domain: domain, ExpectedRevision: expectedRevision, Audit: audit, Outbox: outbox, Policy: s.publisherDomainPolicy, Now: s.clock.Now})
}

// RevokePublisherPublicationDomain commits the authority change first, then
// removes every active host snapshot that could still expose affected releases.
func (s *Service) RevokePublisherPublicationDomain(ctx context.Context, publisherID, domain, reason string, expectedRevision int64) (PublicationDomainRevocation, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "publisher.approve_domain", "")
	if err != nil {
		return PublicationDomainRevocation{}, err
	}
	reason, err = normalizeAuditReason("publisher.publication_domain_revoked", reason, "publication-domain revocation reason is invalid")
	if err != nil {
		return PublicationDomainRevocation{}, err
	}
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if !validDomain(domain) {
		return PublicationDomainRevocation{}, E(CodeInvalidArgument, "publication domain is invalid")
	}
	current, err := s.repo.GetPublisher(ctx, actor.TenantID, publisherID)
	if err != nil {
		return PublicationDomainRevocation{}, err
	}
	at := mutationTime(s.clock.Now(), current.UpdatedAt)
	audit, outbox, err := s.evidenceAt(ctx, actor, "publisher.publication_domain_revoked", "publisher", publisherID, reason, map[string]any{"domain": domain}, at)
	if err != nil {
		return PublicationDomainRevocation{}, err
	}
	publisher, affected, err := s.repo.RevokePublisherPublicationDomain(ctx, actor.TenantID, publisherID, domain, expectedRevision, audit, outbox)
	if err != nil {
		return PublicationDomainRevocation{}, err
	}
	withdrawn := []string{}
	if s.publicationWithdrawer != nil {
		withdrawn, err = s.publicationWithdrawer.WithdrawPublicationDomain(ctx, actor.TenantID, domain, "publisher.publication_domain_revoked", publisherID, "publisher_domain_revoked")
		if err != nil {
			return PublicationDomainRevocation{}, err
		}
	}
	sort.Strings(affected)
	sort.Strings(withdrawn)
	return PublicationDomainRevocation{Publisher: publisher, AffectedReleaseIDs: affected, WithdrawnHosts: withdrawn}, nil
}

// ListPublishers returns a tenant-reader page with one possible continuation
// sentinel.
func (s *Service) ListPublishers(ctx context.Context, request PublisherListRequest) ([]Publisher, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "publisher.read", "")
	if err != nil {
		return nil, err
	}
	if request.Limit < 1 || request.Limit > MaxCatalogPageSize || request.AfterDisplayName == "" && request.AfterID != "" || request.AfterDisplayName != "" && !identity.ValidTenantID(request.AfterID) {
		return nil, E(CodeInvalidArgument, "publisher page position or limit is invalid")
	}
	request, err = request.NormalizeFilters()
	if err != nil {
		return nil, err
	}
	request.Limit++
	return s.repo.ListPublisherPage(ctx, actor.TenantID, request)
}

// GetPublisher returns a publisher only within the authenticated reader's
// tenant.
func (s *Service) GetPublisher(ctx context.Context, publisherID string) (Publisher, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "publisher.read", "")
	if err != nil {
		return Publisher{}, err
	}
	if !identity.ValidTenantID(publisherID) {
		return Publisher{}, E(CodeInvalidArgument, "publisherId must be a UUID")
	}
	return s.repo.GetPublisher(ctx, actor.TenantID, publisherID)
}

// UpdatePublisher applies a reasoned administrator patch under the expected
// publisher revision.
func (s *Service) UpdatePublisher(ctx context.Context, publisherID string, input PatchPublisherInput, expectedRevision int64) (Publisher, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "publisher.manage", "")
	if err != nil {
		return Publisher{}, err
	}
	if !identity.ValidTenantID(publisherID) || expectedRevision <= 0 || input.DisplayName == nil && input.Domains == nil {
		return Publisher{}, E(CodeInvalidArgument, "a publisher UUID, current revision, bounded reason, and at least one changed field are required")
	}
	input.Reason, err = normalizeAuditReason("publisher.updated", input.Reason, "publisher update reason is invalid")
	if err != nil {
		return Publisher{}, err
	}
	current, err := s.GetPublisher(ctx, publisherID)
	if err != nil {
		return Publisher{}, err
	}
	if current.Revision != expectedRevision {
		return Publisher{}, E(CodePreconditionFailed, "publisher revision does not match")
	}
	updated := current
	if input.DisplayName != nil {
		updated.DisplayName = strings.TrimSpace(*input.DisplayName)
		if updated.DisplayName == "" || !validTextLength(updated.DisplayName, 160) {
			return Publisher{}, E(CodeInvalidArgument, "publisher displayName must contain at most 160 characters")
		}
	}
	if input.Domains != nil {
		updated.Domains, err = normalizePublisherDomains(*input.Domains)
		if err != nil {
			return Publisher{}, err
		}
	}
	if updated.DisplayName == current.DisplayName && slices.Equal(updated.Domains, current.Domains) {
		return Publisher{}, E(CodeInvalidArgument, "publisher patch contains no changes")
	}
	updated.Revision = expectedRevision + 1
	updated.UpdatedAt = mutationTime(s.clock.Now(), current.UpdatedAt)
	audit, outbox, err := s.evidenceAt(ctx, actor, "publisher.updated", "publisher", publisherID, input.Reason, updated, updated.UpdatedAt)
	if err != nil {
		return Publisher{}, err
	}
	return s.repo.UpdatePublisher(ctx, UpdatePublisherParams{Publisher: updated, ExpectedRevision: expectedRevision, Audit: audit, Outbox: outbox})
}

// normalizePublisherDomains validates and deduplicates declared publisher DNS
// names into deterministic order.
func normalizePublisherDomains(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > 64 {
		return nil, E(CodeInvalidArgument, "publisher must declare between 1 and 64 domains")
	}
	domains := make([]string, 0, len(values))
	for _, domain := range values {
		domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		if !validDomain(domain) {
			return nil, E(CodeInvalidArgument, "publisher contains an invalid domain")
		}
		if !slices.Contains(domains, domain) {
			domains = append(domains, domain)
		}
	}
	sort.Strings(domains)
	return domains, nil
}

type CreateResourceInput struct {
	NamespaceID         string `json:"namespaceId"`
	CanonicalIdentifier string `json:"canonicalIdentifier"`
	Kind                Kind   `json:"kind"`
}

// CreateResource requires resource.create permission in the namespace and retains
// a bounded opaque identifier or normalized AIR identifier.
func (s *Service) CreateResource(ctx context.Context, input CreateResourceInput) (Resource, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "resource.create", input.NamespaceID)
	if err != nil {
		return Resource{}, err
	}
	input.CanonicalIdentifier, err = CanonicalResourceIdentifier(input.CanonicalIdentifier)
	if err != nil || input.NamespaceID == "" || !input.Kind.Valid() {
		return Resource{}, E(CodeInvalidArgument, "namespace, canonical identifier, and supported kind are required")
	}

	now := s.clock.Now()
	idValue, err := s.ids.New(now)
	if err != nil {
		return Resource{}, Wrap(CodeInternal, "create resource identifier", err)
	}
	resource := Resource{ID: idValue, TenantID: actor.TenantID, NamespaceID: input.NamespaceID, CanonicalIdentifier: input.CanonicalIdentifier, Kind: input.Kind, CreatedBy: actor.Subject, Revision: 1, CreatedAt: now, UpdatedAt: now}
	audit, outbox, err := s.evidence(ctx, actor, "resource.created", "resource", resource.ID, resource)
	if err != nil {
		return Resource{}, err
	}
	return s.repo.CreateResource(ctx, CreateResourceParams{Resource: resource, Audit: audit, Outbox: outbox})
}

// GetResource returns an active resource within the authenticated reader's
// tenant.
func (s *Service) GetResource(ctx context.Context, idValue string) (Resource, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return Resource{}, err
	}
	value, err := s.repo.GetResource(ctx, actor.TenantID, idValue)
	if err != nil {
		return Resource{}, err
	}
	if _, err := requirePermission(ctx, "resource.read", value.NamespaceID); err != nil {
		return Resource{}, err
	}
	return s.repo.GetResource(ctx, actor.TenantID, idValue)
}

// CanonicalResourceIdentifier applies creation's bounded exact identity rules.
// Opaque identities retain their case and Unicode spelling; AIR normalizes only
// its scheme and publisher, preserving its namespace and resource name.
func CanonicalResourceIdentifier(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !validTextLength(value, 512) {
		return "", E(CodeInvalidArgument, "canonical identifier is invalid")
	}
	if strings.HasPrefix(strings.ToLower(value), "urn:air:") {
		parsed, err := ParseAIRIdentifier(value)
		if err != nil {
			return "", err
		}
		value = parsed.String()
	}
	return value, nil
}

// ListResources validates kind and continuation filters before selecting a
// bounded tenant page.
func (s *Service) ListResources(ctx context.Context, request ResourceListRequest) ([]Resource, error) {
	ctx, actor, err := collectionContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.CanonicalIdentifier != "" {
		request.CanonicalIdentifier, err = CanonicalResourceIdentifier(request.CanonicalIdentifier)
		if err != nil {
			return nil, err
		}
	}
	if request.Kind != "" && !request.Kind.Valid() {
		return nil, E(CodeInvalidArgument, "resource kind is invalid")
	}
	if request.Limit < 1 || request.Limit > MaxCatalogPageSize || request.AfterUpdatedAt == nil && request.AfterID != "" || request.AfterUpdatedAt != nil && !identity.ValidTenantID(request.AfterID) {
		return nil, E(CodeInvalidArgument, "resource page position or limit is invalid")
	}
	request.Limit++
	return s.repo.ListResources(ctx, actor.TenantID, request)
}

// DeleteResource requires draft-delete authority and exact retained creator
// identity or protected administration, then retains reasoned deletion evidence.
func (s *Service) DeleteResource(ctx context.Context, resourceID, reason string, expectedRevision int64) error {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return err
	}
	if !identity.ValidTenantID(resourceID) || expectedRevision <= 0 {
		return E(CodeInvalidArgument, "a resource UUID, current revision, and bounded deletion reason are required")
	}
	reason, err = normalizeAuditReason("resource.deleted", reason, "resource deletion reason is invalid")
	if err != nil {
		return err
	}
	resource, err := s.repo.GetResource(ctx, actor.TenantID, resourceID)
	if err != nil {
		return err
	}
	if _, err := requirePermission(ctx, "resource.delete_draft", resource.NamespaceID); err != nil {
		return err
	}
	origin, originErr := s.repo.GetOriginatingPrincipal(ctx, actor.TenantID, "resource", resource.ID)
	if originErr != nil && ErrorCodeOf(originErr) != CodeForbidden {
		return originErr
	}
	if originErr != nil || origin.MembershipID != actor.MembershipID || origin.RealmID != actor.RealmID || origin.Subject != actor.Subject {
		if _, err := requirePermission(ctx, "access.manage", ""); err != nil {
			return E(CodeForbidden, "resource creator or protected administrator is required")
		}
	}
	at := mutationTime(s.clock.Now(), resource.UpdatedAt)
	audit, outbox, err := s.evidenceAt(ctx, actor, "resource.deleted", "resource", resourceID, reason, map[string]any{"resourceId": resourceID, "softDelete": true}, at)
	if err != nil {
		return err
	}
	_, err = s.repo.DeleteResource(ctx, DeleteResourceParams{TenantID: actor.TenantID, ResourceID: resourceID, ExpectedRevision: expectedRevision, Actor: actor.Subject, Reason: reason, At: audit.At, Audit: audit, Outbox: outbox})
	return err
}

// GetVersion returns a tenant-readable version without exposing another
// tenant's identity.
func (s *Service) GetVersion(ctx context.Context, versionID string) (ResourceVersion, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return ResourceVersion{}, err
	}
	value, err := s.repo.GetVersion(ctx, actor.TenantID, versionID)
	if err != nil {
		return ResourceVersion{}, err
	}
	resource, err := s.repo.GetResource(ctx, actor.TenantID, value.ResourceID)
	if err != nil {
		return ResourceVersion{}, err
	}
	if _, err := requirePermission(ctx, "resource.read", resource.NamespaceID); err != nil {
		return ResourceVersion{}, err
	}
	return s.repo.GetVersion(ctx, actor.TenantID, versionID)
}

// GetVersionRevision returns revision, evidence and its canonical namespace
// only after authorization and the private-read audit append succeed.
func (s *Service) GetVersionRevision(ctx context.Context, revisionID string) (VersionRevision, EvidenceSet, string, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	revision, evidence, err := s.repo.GetVersionRevision(ctx, actor.TenantID, revisionID)
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	version, err := s.repo.GetVersion(ctx, actor.TenantID, revision.ResourceVersionID)
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	resource, err := s.repo.GetResource(ctx, actor.TenantID, version.ResourceID)
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	if _, err := requirePermission(ctx, "resource.read_content", resource.NamespaceID); err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	record, err := s.newActorAuditRecord(ctx, actor, auditpkg.ActionVersionRevisionRead, "version_revision", revision.ID, "", map[string]any{"contentDigest": revision.ContentSetDigest}, s.clock.Now())
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	record.AfterDigest = revision.ContentSetDigest
	if err := auditpkg.ValidateNewRecord(record); err != nil {
		return VersionRevision{}, EvidenceSet{}, "", Wrap(CodeInternal, "validate version-revision read evidence", err)
	}
	if err := s.repo.AppendAuditEvent(ctx, record); err != nil {
		return VersionRevision{}, EvidenceSet{}, "", Wrap(CodeDependencyUnavailable, "retain version-revision read evidence", err)
	}
	return revision, evidence, resource.NamespaceID, nil
}

// RevalidateVersionRevision returns a new validation run and canonical namespace
// without rewriting the retained revision. Blocked external evidence can withdraw affected
// publication.
func (s *Service) RevalidateVersionRevision(ctx context.Context, revisionID string) (resultRevision VersionRevision, resultEvidence EvidenceSet, resultNamespaceID string, resultErr error) {
	ctx = identity.WithPermissionContext(ctx)
	ctx, endTrace := StartTrace(ctx, "version revalidate", TraceInternal, "")
	defer func() {
		if resultErr != nil {
			endTrace("error")
		} else {
			endTrace("ok")
		}
	}()
	actor, err := requireActor(ctx)
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	revision, _, err := s.repo.GetVersionRevision(ctx, actor.TenantID, revisionID)
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	version, err := s.repo.GetVersion(ctx, actor.TenantID, revision.ResourceVersionID)
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	resource, err := s.repo.GetResource(ctx, actor.TenantID, version.ResourceID)
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	if _, err = requirePermission(ctx, "resource.validate", resource.NamespaceID); err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	findings, retrievals, err := s.revalidateStoredRevision(ctx, actor.TenantID, resource.Kind, version.Version, revision)
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	evidence, err := s.newValidationEvidence(actor.TenantID, revision.ContentSetDigest, findings, retrievals, s.clock.Now())
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	audit, outbox, err := s.evidence(ctx, actor, "version_revision.validated", "version_revision", revision.ID, map[string]any{"revisionId": revision.ID, "evidence": evidence})
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	evidence, err = s.repo.CreateValidationRun(ctx, CreateValidationRunParams{TenantID: actor.TenantID, VersionRevisionID: revision.ID, Evidence: evidence, Audit: audit, Outbox: outbox})
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, "", err
	}
	if evidence.State == EvidenceBlocked && revisionHasExternalRepresentation(revision) {
		if err := s.withdrawPublishedVersionRevision(ctx, actor.TenantID, revision.ID); err != nil {
			return VersionRevision{}, EvidenceSet{}, "", err
		}
	}
	revision.EvidenceSetID = evidence.ID
	return revision, evidence, resource.NamespaceID, nil
}

// revisionHasExternalRepresentation reports whether any retained representation
// declares an external URL.
func revisionHasExternalRepresentation(revision VersionRevision) bool {
	return slices.ContainsFunc(revision.Representations, func(representation Representation) bool {
		return representation.URL != ""
	})
}

// withdrawPublishedVersionRevision finds bounded active release references and
// withdraws affected snapshots through the configured publication port.
func (s *Service) withdrawPublishedVersionRevision(ctx context.Context, tenantID, revisionID string) error {
	if s.publicationWithdrawer == nil {
		return nil
	}
	releaseIDs, overflow, err := s.repo.PublishedReleaseIDsByVersionRevision(ctx, tenantID, revisionID, MaxPublicationAffectedReleases)
	if err != nil {
		return err
	}
	if overflow {
		return E(CodeWorkLimitExceeded, "published releases affected by validation exceed the supported safety-withdrawal limit")
	}
	for _, releaseID := range releaseIDs {
		if _, err := s.publicationWithdrawer.WithdrawPublicationRelease(ctx, tenantID, releaseID, "version_revision.validated", revisionID, "external_representation_revalidation_blocked"); err != nil {
			return err
		}
	}
	return nil
}

type RepresentationInput struct {
	ExpectedDigest *string            `json:"expectedDigest,omitempty"`
	Role           RepresentationRole `json:"role"`
	MediaType      string             `json:"mediaType"`
	ProfileID      string             `json:"profileId,omitempty"`
	URL            string             `json:"url,omitempty"`
	Data           json.RawMessage    `json:"data,omitempty"`
	Content        string             `json:"content,omitempty"`
	PackageData    []byte             `json:"packageData,omitempty"`
	Handoff        *SkillHandoff      `json:"handoff,omitempty"`
}

// UnmarshalJSON preserves the difference between an omitted checksum and an
// explicitly invalid null. Keep strict decoding for the surrounding control
// object while leaving standards data as its original JSON bytes.
func (r *RepresentationInput) UnmarshalJSON(data []byte) error {
	if err := jsondoc.Validate(data); err != nil {
		return err
	}
	if bytes.TrimSpace(data)[0] != '{' {
		return E(CodeInvalidArgument, "representation must be a JSON object")
	}
	type plain RepresentationInput
	var decoded plain
	input := struct {
		*plain
		ExpectedDigest json.RawMessage `json:"expectedDigest"`
	}{plain: &decoded}
	if err := jsondoc.Decode(data, &input); err != nil {
		return err
	}
	if len(input.ExpectedDigest) > 0 {
		var digest string
		if bytes.Equal(bytes.TrimSpace(input.ExpectedDigest), []byte("null")) || json.Unmarshal(input.ExpectedDigest, &digest) != nil {
			return E(CodeInvalidArgument, "expectedDigest must be a SHA-256 string when present")
		}
		decoded.ExpectedDigest = &digest
	}
	*r = RepresentationInput(decoded)
	return nil
}

type CreateVersionInput struct {
	Version         string                `json:"version"`
	Representations []RepresentationInput `json:"representations"`
}

// CreateVersion validates representations and creates the initial immutable
// revision and evidence for a namespace-authorized resource.
func (s *Service) CreateVersion(ctx context.Context, resourceID string, input CreateVersionInput) (resultVersion ResourceVersion, resultRevision VersionRevision, resultEvidence EvidenceSet, resultErr error) {
	ctx = identity.WithPermissionContext(ctx)
	ctx, endTrace := StartTrace(ctx, "version create", TraceInternal, "")
	defer func() {
		if resultErr != nil {
			endTrace("error")
		} else {
			endTrace("ok")
		}
	}()
	actor, err := requireActor(ctx)
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	resource, err := s.repo.GetResource(ctx, actor.TenantID, resourceID)
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	if _, err = requirePermission(ctx, "resource.edit", resource.NamespaceID); err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	input.Version = strings.TrimSpace(input.Version)
	if input.Version == "" || !validTextLength(input.Version, 128) {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, E(CodeInvalidArgument, "a non-empty version label is required")
	}
	now := s.clock.Now()
	versionID, err := s.ids.New(now)
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, Wrap(CodeInternal, "create version identifier", err)
	}
	version := ResourceVersion{ID: versionID, TenantID: actor.TenantID, ResourceID: resource.ID, Version: input.Version, Revision: 1, CreatedAt: now, UpdatedAt: now}
	revision, evidence, documents, err := s.buildVersionRevision(ctx, actor, resource.Kind, version.ID, input.Version, 1, now, input.Representations)
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	version.CandidateRevisionID = revision.ID
	audit, outbox, err := s.evidence(ctx, actor, "resource_version.created", "resource_version", version.ID, map[string]any{"version": version, "revision": revision, "evidence": evidence})
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	return s.repo.CreateVersion(ctx, CreateVersionParams{Version: version, VersionRevision: revision, Documents: documents, Evidence: evidence, Audit: audit, Outbox: outbox})
}

// CreateVersionRevision validates replacement representations and appends an
// immutable revision under the expected unsealed version revision.
func (s *Service) CreateVersionRevision(ctx context.Context, versionID string, representations []RepresentationInput, expectedRevision int64) (resultVersion ResourceVersion, resultRevision VersionRevision, resultEvidence EvidenceSet, resultErr error) {
	ctx = identity.WithPermissionContext(ctx)
	ctx, endTrace := StartTrace(ctx, "version revise", TraceInternal, "")
	defer func() {
		if resultErr != nil {
			endTrace("error")
		} else {
			endTrace("ok")
		}
	}()
	actor, err := requireActor(ctx)
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	version, err := s.repo.GetVersion(ctx, actor.TenantID, versionID)
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	if version.Revision != expectedRevision {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, E(CodePreconditionFailed, "resource version revision does not match")
	}
	if version.SealedRevisionID != "" {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, E(CodeVersionSealed, "the version label is sealed; create a new version")
	}
	resource, err := s.repo.GetResource(ctx, actor.TenantID, version.ResourceID)
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	if _, err = requirePermission(ctx, "resource.edit", resource.NamespaceID); err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	previous, _, err := s.repo.GetVersionRevision(ctx, actor.TenantID, version.CandidateRevisionID)
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	now := mutationTime(s.clock.Now(), version.UpdatedAt)
	revision, evidence, documents, err := s.buildVersionRevision(ctx, actor, resource.Kind, version.ID, version.Version, previous.RevisionNumber+1, now, representations)
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	version.CandidateRevisionID = revision.ID
	version.Revision++
	version.UpdatedAt = now
	audit, outbox, err := s.evidenceAt(ctx, actor, "resource_version.revised", "resource_version", version.ID, "", map[string]any{"version": version, "revision": revision, "evidence": evidence}, now)
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	return s.repo.CreateVersionRevision(ctx, CreateVersionParams{Version: version, VersionRevision: revision, Documents: documents, Evidence: evidence, ExpectedRevision: expectedRevision, Audit: audit, Outbox: outbox})
}

// buildVersionRevision prepares one immutable revision and its validation
// evidence before the repository transaction. Exact bounded text documents are
// retained; transient Skill package bytes are inspected but never persisted.
func (s *Service) buildVersionRevision(ctx context.Context, actor identity.Actor, kind Kind, versionID, expectedVersion string, number int, now time.Time, inputs []RepresentationInput) (VersionRevision, EvidenceSet, []DocumentBlob, error) {
	if len(inputs) == 0 || len(inputs) > 16 {
		return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "between one and sixteen representations are required")
	}
	revisionID, err := s.ids.New(now)
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, nil, Wrap(CodeInternal, "create revision identifier", err)
	}
	representations := make([]Representation, 0, len(inputs))
	documents := make([]DocumentBlob, 0, len(inputs))
	retrievals := make([]ExternalRetrieval, 0, len(inputs))
	var documentBytes int64
	var retainedPluginBytes int64
	revisionFindings := NewFindingCollector(s.diagnosticLimits)
	evidenceFindings := NewFindingCollector(s.diagnosticLimits)
	primaryCount := 0
	for index, input := range inputs {
		if input.Role == "" {
			input.Role = RepresentationMetadata
		}
		if !input.Role.Valid() {
			return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "representation role is invalid")
		}
		if input.Role == RepresentationPrimary {
			primaryCount++
		}
		mediaType := input.MediaType
		if !validRepresentationMediaType(mediaType) || len(input.ProfileID) > 160 || (len(input.Data) == 0 && input.Content == "" && input.URL == "" && len(input.PackageData) == 0) {
			return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "each representation needs a valid media type and one delivery form")
		}
		input.MediaType = mediaType
		plugin := kind == KindPlugin && IsPluginMediaType(mediaType)
		if kind == KindPlugin && (!plugin || input.URL == "" || len(input.PackageData) != 0) {
			return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "Plugins require an HTTPS archive URL")
		}
		if plugin && input.ProfileID != "" && input.ProfileID != PluginProfile {
			return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "unsupported Plugin validation profile")
		}
		if input.ExpectedDigest != nil && (!plugin || !sha256DigestPattern.MatchString(*input.ExpectedDigest)) {
			return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "expectedDigest requires a Plugin archive and SHA-256 digest")
		}
		if input.URL != "" && !ValidExternalHTTPSURL(input.URL) {
			return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "external representation URL is not a safe HTTPS reference")
		}
		deliveryForms := 0
		for _, present := range []bool{len(input.Data) > 0, input.Content != "", input.URL != "", len(input.PackageData) > 0} {
			if present {
				deliveryForms++
			}
		}
		if deliveryForms != 1 {
			return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "a representation must use exactly one delivery form")
		}
		if len(input.PackageData) > 0 {
			if input.Handoff == nil {
				return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "inspected Skill packages require an immutable Git or OCI handoff")
			}
			handoff, handoffErr := NormalizeSkillHandoff(*input.Handoff, "")
			if handoffErr != nil {
				return VersionRevision{}, EvidenceSet{}, nil, Wrap(CodeInvalidArgument, "validate Skill package handoff", handoffErr)
			}
			input.Handoff = &handoff
		} else if input.Handoff != nil {
			return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "a Skill handoff is valid only with transient packageData")
		}
		representationID, idErr := s.ids.New(now.Add(time.Duration(index+1) * time.Nanosecond))
		if idErr != nil {
			return VersionRevision{}, EvidenceSet{}, nil, Wrap(CodeInternal, "create representation identifier", idErr)
		}
		validationInput := input
		var content []byte
		var retrievalFindings []Finding
		var retrieval *ExternalRetrieval
		switch {
		case input.Content != "":
			if !utf8.ValidString(input.Content) || strings.ContainsRune(input.Content, '\x00') {
				return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "bounded representation documents must contain valid UTF-8 text without NUL bytes")
			}
			content = []byte(input.Content)
		case input.URL != "":
			remaining := MaxCatalogDocumentBytes - documentBytes
			if remaining <= 0 {
				return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "bounded representation documents exceed the 16 MiB revision limit")
			}
			result, fetchErr := s.fetchExternalRepresentation(ctx, actor.TenantID, input.URL, mediaType, max(remaining, int64(1024)))
			if fetchErr != nil {
				return VersionRevision{}, EvidenceSet{}, nil, fetchErr
			}
			content = result.Body
			if len(content) == 0 || !plugin && (!utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0) {
				return VersionRevision{}, EvidenceSet{}, nil, E(CodeValidationFailed, "external representation did not return bounded UTF-8 validation bytes")
			}
			if plugin {
				validationInput.PackageData = content
				if result.Status != http.StatusOK || !PluginResponseMediaType(mediaType, result.MediaType) || input.ExpectedDigest != nil && *input.ExpectedDigest != result.ContentDigest {
					return VersionRevision{}, EvidenceSet{}, nil, E(CodeValidationFailed, "Plugin archive response or expected digest does not match")
				}
			} else {
				validationInput, fetchErr = representationInputWithExternalBytes(input, content)
			}
			if fetchErr != nil {
				return VersionRevision{}, EvidenceSet{}, nil, fetchErr
			}
			retrievalValue := externalRetrievalFromResult(representationID, result)
			retrieval = &retrievalValue
			retrievalFindings = externalRetrievalFindings(mediaType, result)
		}
		if len(content) > 0 {
			documentBytes += int64(len(content))
			if documentBytes > MaxCatalogDocumentBytes {
				return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "bounded representation documents exceed the 16 MiB revision limit")
			}
		}
		validation := s.validateRepresentation(ctx, kind, validationInput)
		if plugin && validation.Plugin == nil {
			return VersionRevision{}, EvidenceSet{}, nil, E(CodeValidationFailed, "Plugin archive is unsafe, malformed or exceeds inspection limits")
		}
		if err := ctx.Err(); err != nil {
			return VersionRevision{}, EvidenceSet{}, nil, err
		}
		if nativeVersion, ok := validation.NormalizedFacts["version"].(string); ok && nativeVersion != "" && nativeVersion != expectedVersion {
			validation.Findings = append(validation.Findings, Finding{Code: "NATIVE_VERSION_MISMATCH", Severity: SeverityError, Path: "/version", Message: "The representation version must match the Catalog version label."})
		}
		validation.Findings = s.boundFindings(validation.Findings)
		profile := validation.ProfileID
		if profile == "" {
			profile = input.ProfileID
		}
		representation := Representation{ID: representationID, Role: input.Role, MediaType: mediaType, ProfileID: profile, URL: input.URL, ValidationState: "valid", Findings: validation.Findings, NormalizedFacts: validation.NormalizedFacts, UnknownProperties: validation.Unknown}
		switch {
		case plugin:
			for _, document := range validation.Plugin.Documents {
				retainedPluginBytes += int64(len(document.Content))
			}
			if retainedPluginBytes > MaxCatalogDocumentBytes {
				return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "Plugin validation documents exceed the revision byte limit")
			}
			representation.Plugin = validation.Plugin
			representation.ContentDigest = digestBytes(content)
			representation.Size = int64(len(content))
			if retrieval != nil {
				retrievals = append(retrievals, *retrieval)
			}
		case len(input.Data) > 0:
			var object map[string]any
			decoder := json.NewDecoder(strings.NewReader(string(input.Data)))
			decoder.UseNumber()
			if err := decoder.Decode(&object); err != nil || object == nil {
				return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "inline representation data must be a JSON object")
			}
			canonical, canonicalErr := canonicalJSON(object)
			if canonicalErr != nil {
				return VersionRevision{}, EvidenceSet{}, nil, Wrap(CodeInvalidArgument, "canonicalize inline representation", canonicalErr)
			}
			representation.Data = canonical
			representation.ContentDigest = digestBytes(canonical)
			representation.Size = int64(len(canonical))
		case input.Content != "" || input.URL != "":
			digest := digestBytes(content)
			document := DocumentBlob{ID: strings.TrimPrefix(digest, "sha256:"), TenantID: actor.TenantID, Digest: digest, MediaType: representation.MediaType, Size: int64(len(content)), Content: append([]byte(nil), content...), CreatedAt: now}
			representation.ArtifactID = document.ID
			representation.ContentDigest = document.Digest
			representation.Size = document.Size
			documents = append(documents, document)
			if retrieval != nil {
				retrievals = append(retrievals, *retrieval)
			}
		case len(input.PackageData) > 0:
			if validation.Package == nil || validation.Handoff == nil {
				return VersionRevision{}, EvidenceSet{}, nil, E(CodeInvalidArgument, "packageData is supported only for inspected Agent Skill ZIP or GZIP representations")
			}
			representation.ContentDigest = digestBytes(input.PackageData)
			representation.Size = int64(len(input.PackageData))
			representation.Package = validation.Package
			representation.Handoff = validation.Handoff
		}
		for _, finding := range retrievalFindings {
			finding.Path = fmt.Sprintf("/representations/%d%s", index, finding.Path)
			evidenceFindings.Add(finding)
		}
		for _, finding := range validation.Findings {
			finding.Path = fmt.Sprintf("/representations/%d%s", index, finding.Path)
			revisionFindings.Add(finding)
			evidenceFindings.Add(finding)
			if finding.Severity == SeverityError {
				representation.ValidationState = "invalid"
			}
		}
		representations = append(representations, representation)
	}
	if primaryCount != 1 {
		finding := Finding{Code: "PRIMARY_REPRESENTATION_COUNT", Severity: SeverityError, Path: "/representations", Message: "Exactly one primary representation is required."}
		revisionFindings.Add(finding)
		evidenceFindings.Add(finding)
	}
	setDigest, err := contentSetDigest(representations)
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, nil, Wrap(CodeInternal, "digest representation set", err)
	}
	evidence, err := s.newValidationEvidence(actor.TenantID, setDigest, evidenceFindings.Findings(), retrievals, now)
	if err != nil {
		return VersionRevision{}, EvidenceSet{}, nil, err
	}
	revision := VersionRevision{ID: revisionID, TenantID: actor.TenantID, ResourceVersionID: versionID, RevisionNumber: number, Representations: representations, ContentSetDigest: setDigest, Findings: revisionFindings.Findings(), EvidenceSetID: evidence.ID, CreatedBy: actor.Subject, CreatedAt: now}
	return revision, evidence, documents, nil
}

// newValidationEvidence binds findings, policy and external retrieval freshness
// into a new immutable evidence-set digest.
func (s *Service) newValidationEvidence(tenantID, subjectDigest string, findings []Finding, retrievals []ExternalRetrieval, now time.Time) (EvidenceSet, error) {
	// PostgreSQL timestamptz stores microseconds. Normalize before hashing so a
	// later read can reproduce the immutable evidence digest exactly.
	now = now.UTC().Truncate(time.Microsecond)
	findings = s.boundFindings(findings)
	evidenceID, err := s.ids.New(now.Add(100 * time.Nanosecond))
	if err != nil {
		return EvidenceSet{}, Wrap(CodeInternal, "create evidence identifier", err)
	}
	state := EvidenceComplete
	for _, finding := range findings {
		if finding.Severity == SeverityError {
			state = EvidenceBlocked
			break
		}
	}
	legalRights := LegalRightsSummary{State: LegalRightsNotAssessed}
	trust := TrustSummary{State: TrustNotAssessed}
	retrievals = append([]ExternalRetrieval(nil), retrievals...)
	if retrievals == nil {
		retrievals = []ExternalRetrieval{}
	}
	if err := ValidateExternalRetrievals(retrievals); err != nil {
		return EvidenceSet{}, err
	}
	expiresAt := earliestExternalRetrievalExpiry(retrievals)
	var expiresAtDigest any
	if !expiresAt.IsZero() {
		expiresAtDigest = expiresAt
	}
	evidenceDigest, err := digestJSON(map[string]any{
		"runId":              evidenceID,
		"subjectDigest":      subjectDigest,
		"state":              state,
		"findings":           findings,
		"validationProfile":  currentValidationProfile,
		"policyVersion":      s.policy,
		"legalRights":        legalRights,
		"trust":              trust,
		"externalRetrievals": retrievals,
		"createdAt":          now,
		"expiresAt":          expiresAtDigest,
	})
	if err != nil {
		return EvidenceSet{}, Wrap(CodeInternal, "digest evidence", err)
	}
	return EvidenceSet{ID: evidenceID, TenantID: tenantID, SubjectDigest: subjectDigest, Digest: evidenceDigest, State: state, Findings: findings, ValidationProfile: currentValidationProfile, PolicyVersion: s.policy, LegalRights: legalRights, Trust: trust, ExternalRetrievals: retrievals, ExpiresAt: expiresAt, CreatedAt: now}, nil
}

// revalidateStoredRevision verifies retained bytes and immutable digests before
// producing fresh findings. It never repairs or rewrites the sealed revision.
func (s *Service) revalidateStoredRevision(ctx context.Context, tenantID string, kind Kind, versionLabel string, revision VersionRevision) ([]Finding, []ExternalRetrieval, error) {
	digest, err := contentSetDigest(revision.Representations)
	if err != nil || digest != revision.ContentSetDigest {
		return nil, nil, E(CodeDependencyUnavailable, "version revision failed its content-set integrity check")
	}
	if err := ValidatePackageRevisionDigest(revision); err != nil {
		return nil, nil, E(CodeDependencyUnavailable, "version revision package evidence failed its integrity check")
	}
	findings := NewFindingCollector(s.diagnosticLimits)
	retrievals := make([]ExternalRetrieval, 0)
	var retrievalBytes int64
	primaryCount := 0
	for index, representation := range revision.Representations {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if representation.Role == RepresentationPrimary {
			primaryCount++
		}
		input := RepresentationInput{Role: representation.Role, MediaType: representation.MediaType, ProfileID: representation.ProfileID, URL: representation.URL, Data: append(json.RawMessage(nil), representation.Data...)}
		var validation ValidationResult
		retrievalFindings := make([]Finding, 0)
		switch {
		case representation.Plugin != nil:
			if err := ValidatePackageRepresentation(representation); err != nil {
				return nil, nil, err
			}
			remaining := MaxCatalogDocumentBytes - retrievalBytes
			if remaining <= 0 {
				return nil, nil, E(CodeDependencyUnavailable, "Plugin retrieval exceeds revision budget")
			}
			result, err := s.fetchExternalRepresentation(ctx, tenantID, representation.URL, representation.MediaType, max(remaining, int64(1024)))
			if err != nil {
				findings.Add(Finding{Code: "EXTERNAL_RETRIEVAL_FAILED", Severity: SeverityError, Path: "/url", Message: "The Plugin archive could not be retrieved through approved policy."})
				continue
			}
			retrievalBytes += int64(len(result.Body))
			if retrievalBytes > MaxCatalogDocumentBytes {
				return nil, nil, E(CodeDependencyUnavailable, "Plugin retrieval exceeds revision budget")
			}
			retrievals = append(retrievals, externalRetrievalFromResult(representation.ID, result))
			retrievalFindings = append(retrievalFindings, externalRetrievalFindings(representation.MediaType, result)...)
			if result.ContentDigest != representation.ContentDigest || int64(len(result.Body)) != representation.Size {
				retrievalFindings = append(retrievalFindings, Finding{Code: "EXTERNAL_CONTENT_CHANGED", Severity: SeverityError, Path: "/url", Message: "Plugin archive bytes changed; create a new revision."})
			}
			input.PackageData = result.Body
			validation = s.validateRepresentation(ctx, kind, input)
		case representation.Package != nil:
			if err := ValidatePackageRepresentation(representation); err != nil {
				return nil, nil, E(CodeDependencyUnavailable, "version revision package evidence failed its integrity check")
			}
			validation = ValidationResult{ProfileID: representation.ProfileID, NormalizedFacts: representation.NormalizedFacts, Unknown: representation.UnknownProperties, Findings: s.boundFindings(representation.Findings), Package: representation.Package, Handoff: representation.Handoff}
		default:
			switch {
			case representation.URL != "":
				if representation.ArtifactID == "" || !ValidExternalHTTPSURL(representation.URL) {
					findings.Add(Finding{Code: "EXTERNAL_CONTENT_NOT_BOUND", Severity: SeverityError, Path: fmt.Sprintf("/representations/%d/url", index), Message: "This external reference has no exact retained validation bytes; create a new VersionRevision."})
					continue
				}
				artifact, content, contentErr := s.getArtifact(ctx, tenantID, representation.ArtifactID)
				if contentErr != nil {
					return nil, nil, contentErr
				}
				if artifact.Digest != representation.ContentDigest || artifact.MediaType != representation.MediaType || artifact.Size != representation.Size || !utf8.Valid(content) || bytes.ContainsRune(content, '\x00') {
					return nil, nil, E(CodeDependencyUnavailable, "version revision document failed its integrity check")
				}
				remaining := MaxCatalogDocumentBytes - retrievalBytes
				if remaining <= 0 {
					return nil, nil, E(CodeDependencyUnavailable, "version revision external retrieval budget is invalid")
				}
				result, fetchErr := s.fetchExternalRepresentation(ctx, tenantID, representation.URL, representation.MediaType, max(remaining, int64(1024)))
				if fetchErr != nil {
					findings.Add(Finding{Code: "EXTERNAL_RETRIEVAL_FAILED", Severity: SeverityError, Path: fmt.Sprintf("/representations/%d/url", index), Message: "The external representation could not be retrieved through the current approved outbound policy."})
					continue
				}
				retrievalBytes += int64(len(result.Body))
				if retrievalBytes > MaxCatalogDocumentBytes {
					return nil, nil, E(CodeDependencyUnavailable, "version revision external retrieval budget was exceeded")
				}
				retrievals = append(retrievals, externalRetrievalFromResult(representation.ID, result))
				retrievalFindings = append(retrievalFindings, externalRetrievalFindings(representation.MediaType, result)...)
				if result.ContentDigest != representation.ContentDigest || int64(len(result.Body)) != representation.Size {
					retrievalFindings = append(retrievalFindings, Finding{Code: "EXTERNAL_CONTENT_CHANGED", Severity: SeverityError, Path: "/url", Message: "The external representation bytes changed; create a new VersionRevision."})
				}
				input, fetchErr = representationInputWithExternalBytes(input, result.Body)
				if fetchErr != nil {
					retrievalFindings = append(retrievalFindings, Finding{Code: "EXTERNAL_CONTENT_INVALID", Severity: SeverityError, Path: "/url", Message: "The external representation no longer returns bounded UTF-8 validation bytes; create a new VersionRevision."})
					validation = ValidationResult{ProfileID: representation.ProfileID, NormalizedFacts: representation.NormalizedFacts, Unknown: representation.UnknownProperties, Findings: s.boundFindings(representation.Findings)}
					break
				}
				validationResult := s.validateRepresentation(ctx, kind, input)
				validation.ProfileID = validationResult.ProfileID
				validation.NormalizedFacts = validationResult.NormalizedFacts
				validation.Unknown = validationResult.Unknown
				validation.Findings = append(validation.Findings, validationResult.Findings...)
			case representation.ArtifactID != "":
				artifact, content, contentErr := s.getArtifact(ctx, tenantID, representation.ArtifactID)
				if contentErr != nil {
					return nil, nil, contentErr
				}
				if artifact.Digest != representation.ContentDigest || artifact.MediaType != representation.MediaType || artifact.Size != representation.Size || !utf8.Valid(content) || bytes.ContainsRune(content, '\x00') {
					return nil, nil, E(CodeDependencyUnavailable, "version revision document failed its integrity check")
				}
				input.Content = string(content)
			case len(representation.Data) > 0:
				if validationErr := jsondoc.Validate(representation.Data); validationErr != nil {
					return nil, nil, E(CodeDependencyUnavailable, "version revision inline document failed its integrity check")
				}
				var object map[string]any
				decoder := json.NewDecoder(bytes.NewReader(representation.Data))
				decoder.UseNumber()
				if decodeErr := decoder.Decode(&object); decodeErr != nil || object == nil || decoder.Decode(&struct{}{}) != io.EOF {
					return nil, nil, E(CodeDependencyUnavailable, "version revision inline document failed its integrity check")
				}
				canonical, canonicalErr := canonicalJSON(object)
				if canonicalErr != nil || digestBytes(canonical) != representation.ContentDigest || int64(len(canonical)) != representation.Size {
					return nil, nil, E(CodeDependencyUnavailable, "version revision inline document failed its integrity check")
				}
				input.Data = canonical
			default:
				return nil, nil, E(CodeDependencyUnavailable, "version revision representation delivery form is missing")
			}
			if representation.URL == "" {
				validation = s.validateRepresentation(ctx, kind, input)
			}
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			profileID := validation.ProfileID
			if profileID == "" {
				profileID = representation.ProfileID
			}
			if profileID != representation.ProfileID {
				validation.Findings = append(validation.Findings, Finding{Code: "REPRESENTATION_PROFILE_CHANGED", Severity: SeverityError, Path: "/profileId", Message: "Current validation interprets the representation under a different profile; create a new VersionRevision."})
			}
			if !equivalentProjection(validation.NormalizedFacts, representation.NormalizedFacts) {
				validation.Findings = append(validation.Findings, Finding{Code: "NORMALIZED_FACTS_CHANGED", Severity: SeverityError, Path: "/normalizedFacts", Message: "Current validation derives different normalized facts; create a new VersionRevision."})
			}
			if !equivalentProjection(validation.Unknown, representation.UnknownProperties) {
				validation.Findings = append(validation.Findings, Finding{Code: "UNKNOWN_PROPERTIES_CHANGED", Severity: SeverityError, Path: "/unknownProperties", Message: "Current validation preserves a different unknown-property projection; create a new VersionRevision."})
			}
		}
		if nativeVersion, ok := validation.NormalizedFacts["version"].(string); ok && nativeVersion != "" && nativeVersion != versionLabel {
			validation.Findings = append(validation.Findings, Finding{Code: "NATIVE_VERSION_MISMATCH", Severity: SeverityError, Path: "/version", Message: "The representation version must match the Catalog version label."})
		}
		currentValidationState := "valid"
		for _, finding := range validation.Findings {
			if finding.Severity == SeverityError {
				currentValidationState = "invalid"
				break
			}
		}
		if currentValidationState != representation.ValidationState {
			validation.Findings = append(validation.Findings, Finding{Code: "REPRESENTATION_VALIDATION_STATE_CHANGED", Severity: SeverityError, Path: "/validationState", Message: "Current validation derives a different immutable representation state; create a new VersionRevision."})
		}
		for _, finding := range retrievalFindings {
			finding.Path = fmt.Sprintf("/representations/%d%s", index, finding.Path)
			findings.Add(finding)
		}
		for _, finding := range validation.Findings {
			finding.Path = fmt.Sprintf("/representations/%d%s", index, finding.Path)
			findings.Add(finding)
		}
	}
	if primaryCount != 1 {
		findings.Add(Finding{Code: "PRIMARY_REPRESENTATION_COUNT", Severity: SeverityError, Path: "/representations", Message: "Exactly one primary representation is required."})
	}
	return findings.Findings(), retrievals, nil
}

// fetchExternalRepresentation uses the injected outbound port and verifies
// returned URL, byte, digest, policy and timestamp safety evidence.
func (s *Service) fetchExternalRepresentation(ctx context.Context, tenantID, rawURL, mediaType string, maxResponseBytes int64) (ExternalRepresentationFetchResult, error) {
	if s.externalFetcher == nil {
		return ExternalRepresentationFetchResult{}, E(CodeDependencyUnavailable, "external representation retrieval is unavailable")
	}
	policyDigest := s.externalFetcher.PolicyDigest()
	if !sha256DigestPattern.MatchString(policyDigest) {
		return ExternalRepresentationFetchResult{}, E(CodeDependencyUnavailable, "external representation routing policy identity is unavailable")
	}
	result, err := s.externalFetcher.FetchExternalRepresentation(ctx, ExternalRepresentationFetchRequest{TenantID: tenantID, URL: rawURL, MediaType: mediaType, MaxResponseBytes: maxResponseBytes})
	if err != nil {
		if IsPluginMediaType(mediaType) && ErrorCodeOf(err) == CodeValidationFailed {
			return ExternalRepresentationFetchResult{}, E(CodeValidationFailed, "Plugin archive URL is invalid or denied by outbound policy")
		}
		return ExternalRepresentationFetchResult{}, Wrap(CodeDependencyUnavailable, "retrieve external representation through approved outbound policy", err)
	}
	if err := ctx.Err(); err != nil {
		return ExternalRepresentationFetchResult{}, err
	}
	result.RetrievedAt = result.RetrievedAt.UTC().Truncate(time.Microsecond)
	if result.RequestedURL != rawURL || !ValidExternalHTTPSURL(result.FinalURL) || !sameHTTPSOrigin(rawURL, result.FinalURL) || result.Status < 200 || result.Status > 599 || !validFetchedMediaType(result.MediaType) || result.ContentDigest != digestBytes(result.Body) || int64(len(result.Body)) > maxResponseBytes || int64(len(result.Body)) > MaxCatalogDocumentBytes || !validExternalPolicyID(result.PolicyID) || result.PolicyRevision <= 0 || result.PolicyDigest != policyDigest || result.RetrievedAt.IsZero() {
		return ExternalRepresentationFetchResult{}, E(CodeDependencyUnavailable, "external representation retrieval returned invalid safety evidence")
	}
	result.Body = append([]byte(nil), result.Body...)
	return result, nil
}

// representationInputWithExternalBytes requires nonempty UTF-8 bytes and
// selects JSON or text validation input from the declared media type.
func representationInputWithExternalBytes(input RepresentationInput, content []byte) (RepresentationInput, error) {
	if len(content) == 0 || !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return RepresentationInput{}, E(CodeValidationFailed, "external representation did not return bounded UTF-8 validation bytes")
	}
	mediaType, err := representationBaseMediaType(input.MediaType)
	if err != nil {
		return RepresentationInput{}, E(CodeInvalidArgument, "external representation media type is invalid")
	}
	input.Data = nil
	input.Content = ""
	if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") {
		input.Data = append(json.RawMessage(nil), content...)
	} else {
		input.Content = string(content)
	}
	return input, nil
}

// externalRetrievalFromResult projects bounded fetch metadata and derives the
// fixed evidence expiry without retaining response bytes.
func externalRetrievalFromResult(representationID string, result ExternalRepresentationFetchResult) ExternalRetrieval {
	return ExternalRetrieval{
		RepresentationID: representationID,
		RequestedURL:     result.RequestedURL,
		FinalURL:         result.FinalURL,
		Status:           result.Status,
		MediaType:        result.MediaType,
		ContentDigest:    result.ContentDigest,
		Size:             int64(len(result.Body)),
		PolicyID:         result.PolicyID,
		PolicyRevision:   result.PolicyRevision,
		PolicyDigest:     result.PolicyDigest,
		RetrievedAt:      result.RetrievedAt,
		ExpiresAt:        result.RetrievedAt.Add(ExternalRepresentationFreshness),
	}
}

// externalRetrievalFindings reports non-200 responses and declared/received
// media-type disagreement.
func externalRetrievalFindings(declaredMediaType string, result ExternalRepresentationFetchResult) []Finding {
	findings := make([]Finding, 0, 2)
	if result.Status != http.StatusOK {
		findings = append(findings, Finding{Code: "EXTERNAL_HTTP_STATUS", Severity: SeverityError, Path: "/url", Message: "The external representation must be anonymously available with HTTP status 200."})
	}
	declaredBase, err := representationBaseMediaType(declaredMediaType)
	if err != nil || declaredBase != result.MediaType && !PluginResponseMediaType(declaredBase, result.MediaType) {
		findings = append(findings, Finding{Code: "EXTERNAL_MEDIA_TYPE_MISMATCH", Severity: SeverityError, Path: "/url", Message: "The external response media type does not match the declared representation media type."})
	}
	return findings
}

// equivalentProjection compares deterministic JSON digests, treating nil and
// empty maps as equivalent.
func equivalentProjection(left, right map[string]any) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	leftDigest, leftErr := digestJSON(left)
	rightDigest, rightErr := digestJSON(right)
	return leftErr == nil && rightErr == nil && leftDigest == rightDigest
}

// validRepresentationMediaType checks the bounded declared type/subtype and
// accepted parameter syntax.
func validRepresentationMediaType(value string) bool {
	if value == "" || len(value) > 255 || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.Contains(mediaType, "/")
}

type CreateListingInput struct {
	NamespaceID string `json:"namespaceId"`
	ResourceID  string `json:"resourceId"`
}

// CreateListing creates a namespace-authorized governance listing for the
// selected resource and publisher.
func (s *Service) CreateListing(ctx context.Context, input CreateListingInput) (CatalogListing, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "resource.edit", input.NamespaceID)
	if err != nil {
		return CatalogListing{}, err
	}
	resource, err := s.repo.GetResource(ctx, actor.TenantID, input.ResourceID)
	if err != nil {
		return CatalogListing{}, err
	}
	if input.NamespaceID == "" || resource.TenantID != actor.TenantID || resource.NamespaceID != input.NamespaceID {
		return CatalogListing{}, E(CodeInvalidArgument, "a listing must remain in its resource namespace")
	}
	now := s.clock.Now()
	idValue, err := s.ids.New(now)
	if err != nil {
		return CatalogListing{}, Wrap(CodeInternal, "create listing identifier", err)
	}
	listing := CatalogListing{ID: idValue, TenantID: actor.TenantID, NamespaceID: input.NamespaceID, ResourceID: resource.ID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	audit, outbox, err := s.evidence(ctx, actor, "catalog_listing.created", "catalog_listing", listing.ID, listing)
	if err != nil {
		return CatalogListing{}, err
	}
	return s.repo.CreateListing(ctx, CreateListingParams{Listing: listing, Audit: audit, Outbox: outbox})
}

// GetListing returns a listing within the authenticated reader's tenant.
func (s *Service) GetListing(ctx context.Context, listingID string) (CatalogListing, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return CatalogListing{}, err
	}
	value, err := s.repo.GetListing(ctx, actor.TenantID, listingID)
	if err != nil {
		return CatalogListing{}, err
	}
	if _, err := requirePermission(ctx, "resource.read", value.NamespaceID); err != nil {
		return CatalogListing{}, err
	}
	return s.repo.GetListing(ctx, actor.TenantID, listingID)
}

// GetListingRevision returns immutable listing content after current namespace
// authorization. This metadata read does not append a private-read audit event.
func (s *Service) GetListingRevision(ctx context.Context, revisionID string) (ListingRevision, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return ListingRevision{}, err
	}
	value, err := s.repo.GetListingRevision(ctx, actor.TenantID, revisionID)
	if err != nil {
		return ListingRevision{}, err
	}
	listing, err := s.repo.GetListing(ctx, actor.TenantID, value.ListingID)
	if err != nil {
		return ListingRevision{}, err
	}
	if _, err := requirePermission(ctx, "resource.read_content", listing.NamespaceID); err != nil {
		return ListingRevision{}, err
	}
	return s.repo.GetListingRevision(ctx, actor.TenantID, revisionID)
}

type CreateListingRevisionInput struct {
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
}

// CreateListingRevision validates display metadata and appends a revision under
// the listing's optimistic concurrency precondition.
func (s *Service) CreateListingRevision(ctx context.Context, listingID string, input CreateListingRevisionInput, expectedRevision int64) (CatalogListing, ListingRevision, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return CatalogListing{}, ListingRevision{}, err
	}
	listing, err := s.repo.GetListing(ctx, actor.TenantID, listingID)
	if err != nil {
		return CatalogListing{}, ListingRevision{}, err
	}
	if listing.Revision != expectedRevision {
		return CatalogListing{}, ListingRevision{}, E(CodePreconditionFailed, "catalog listing revision does not match")
	}
	if _, err = requirePermission(ctx, "resource.edit", listing.NamespaceID); err != nil {
		return CatalogListing{}, ListingRevision{}, err
	}
	versionRevision, _, err := s.repo.GetVersionRevision(ctx, actor.TenantID, input.VersionRevisionID)
	if err != nil {
		return CatalogListing{}, ListingRevision{}, err
	}
	version, err := s.repo.GetVersion(ctx, actor.TenantID, versionRevision.ResourceVersionID)
	if err != nil {
		return CatalogListing{}, ListingRevision{}, err
	}
	if version.ResourceID != listing.ResourceID {
		return CatalogListing{}, ListingRevision{}, E(CodeInvalidArgument, "listing revision must select a version of the listed resource")
	}
	resource, err := s.repo.GetResource(ctx, actor.TenantID, listing.ResourceID)
	if err != nil {
		return CatalogListing{}, ListingRevision{}, err
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Description = strings.TrimSpace(input.Description)
	if input.DisplayName == "" || input.Description == "" || !validTextLength(input.DisplayName, 160) || !validTextLength(input.Description, 4000) || !input.Visibility.Valid() {
		return CatalogListing{}, ListingRevision{}, E(CodeInvalidArgument, "display name, description, and visibility are required")
	}
	if err := validateListingDiscoveryMetadata(input.Tags, input.Categories, input.RepresentativeQueries); err != nil {
		return CatalogListing{}, ListingRevision{}, err
	}
	for _, candidate := range []string{input.DocumentationURL, input.SupportURL, input.TermsURL} {
		if candidate == "" {
			continue
		}
		if !ValidDocumentationHTTPSURL(candidate) {
			return CatalogListing{}, ListingRevision{}, E(CodeInvalidArgument, "listing URL is not a safe HTTPS reference")
		}
	}
	input.Tags = normalizeTerms(input.Tags, 32)
	input.Categories = normalizeTerms(input.Categories, 16)
	input.RepresentativeQueries = normalizeQueries(input.RepresentativeQueries)
	input.Relationships, err = s.normalizeRelationships(ctx, actor.TenantID, resource, version, input.Relationships)
	if err != nil {
		return CatalogListing{}, ListingRevision{}, err
	}
	if _, err := s.repo.GetPublisher(ctx, actor.TenantID, input.PublisherID); err != nil {
		if ErrorCodeOf(err) != CodeNotFound {
			return CatalogListing{}, ListingRevision{}, err
		}
		return CatalogListing{}, ListingRevision{}, E(CodeInvalidArgument, "publisher does not exist in the tenant")
	}
	input.Extensions, err = s.sealListingArtwork(ctx, actor.TenantID, input.Extensions)
	if err != nil {
		return CatalogListing{}, ListingRevision{}, err
	}
	previousNumber := 0
	if listing.CandidateRevisionID != "" {
		previous, getErr := s.repo.GetListingRevision(ctx, actor.TenantID, listing.CandidateRevisionID)
		if getErr != nil {
			return CatalogListing{}, ListingRevision{}, getErr
		}
		previousNumber = previous.RevisionNumber
	}
	now := mutationTime(s.clock.Now(), listing.UpdatedAt)
	idValue, err := s.ids.New(now)
	if err != nil {
		return CatalogListing{}, ListingRevision{}, Wrap(CodeInternal, "create listing revision identifier", err)
	}
	revision := ListingRevision{ID: idValue, TenantID: actor.TenantID, ListingID: listing.ID, RevisionNumber: previousNumber + 1, VersionRevisionID: input.VersionRevisionID, DisplayName: input.DisplayName, Description: input.Description, Tags: input.Tags, Categories: input.Categories, RepresentativeQueries: input.RepresentativeQueries, PublisherID: input.PublisherID, DocumentationURL: input.DocumentationURL, SupportURL: input.SupportURL, License: strings.TrimSpace(input.License), TermsURL: input.TermsURL, Visibility: input.Visibility, Relationships: input.Relationships, Extensions: input.Extensions, CreatedBy: actor.Subject, CreatedAt: now}
	revision.Digest, err = digestJSON(revisionDigestInput(revision))
	if err != nil {
		// Extensions contain caller-supplied JSON values. Unsupported numeric
		// exponents and unencodable values are input errors, not service failures.
		return CatalogListing{}, ListingRevision{}, Wrap(CodeInvalidArgument, "listing extensions contain unsupported JSON values", err)
	}
	audit, outbox, err := s.evidenceAt(ctx, actor, "catalog_listing.revised", "catalog_listing", listing.ID, "", revision, now)
	if err != nil {
		return CatalogListing{}, ListingRevision{}, err
	}
	return s.repo.CreateListingRevision(ctx, CreateListingRevisionParams{Revision: revision, ExpectedRevision: expectedRevision, Audit: audit, Outbox: outbox})
}

// revisionDigestInput selects the immutable listing fields that define its
// semantic digest.
func revisionDigestInput(revision ListingRevision) any {
	return struct {
		VersionRevisionID     string                  `json:"versionRevisionId"`
		DisplayName           string                  `json:"displayName"`
		Description           string                  `json:"description"`
		Tags                  []string                `json:"tags"`
		Categories            []string                `json:"categories"`
		RepresentativeQueries []string                `json:"representativeQueries"`
		PublisherID           string                  `json:"publisherId"`
		DocumentationURL      string                  `json:"documentationUrl"`
		SupportURL            string                  `json:"supportUrl"`
		License               string                  `json:"license"`
		TermsURL              string                  `json:"termsUrl"`
		Visibility            Visibility              `json:"visibility"`
		Relationships         []Relationship          `json:"relationships"`
		Extensions            listingDigestExtensions `json:"extensions"`
	}{revision.VersionRevisionID, revision.DisplayName, revision.Description, revision.Tags, revision.Categories, revision.RepresentativeQueries, revision.PublisherID, revision.DocumentationURL, revision.SupportURL, revision.License, revision.TermsURL, revision.Visibility, revision.Relationships, revision.Extensions}
}

// ValidateListingRevisionDigest verifies the service-owned immutable digest
// without applying current publication policy or resolving relationships.
func ValidateListingRevisionDigest(revision ListingRevision) error {
	digest, err := digestJSON(revisionDigestInput(revision))
	if err != nil || digest != revision.Digest {
		return E(CodeDependencyUnavailable, "listing revision failed its digest integrity check")
	}
	return nil
}

// normalizeRelationships validates relationship types and local target bindings
// within the source tenant before canonicalizing references.
func (s *Service) normalizeRelationships(ctx context.Context, tenantID string, sourceResource Resource, sourceVersion ResourceVersion, relationships []Relationship) ([]Relationship, error) {
	if len(relationships) > 64 {
		return nil, E(CodeInvalidArgument, "a listing revision cannot contain more than 64 relationships")
	}
	normalized := make([]Relationship, 0, len(relationships))
	seen := make(map[string]struct{}, len(relationships))
	for _, relationship := range relationships {
		relationship.Type = strings.ToLower(strings.TrimSpace(relationship.Type))
		relationship.TargetIdentifier = strings.TrimSpace(relationship.TargetIdentifier)
		relationship.TargetVersion = strings.TrimSpace(relationship.TargetVersion)
		if !validRelationshipType(relationship.Type) || relationship.TargetIdentifier == "" || !validTextLength(relationship.TargetIdentifier, 512) || !validTextLength(relationship.TargetVersion, 128) {
			return nil, E(CodeInvalidArgument, "relationship type, target identifier, or target version is invalid")
		}
		if strings.HasPrefix(strings.ToLower(relationship.TargetIdentifier), "urn:air:") {
			identifier, err := ParseAIRIdentifier(relationship.TargetIdentifier)
			if err != nil {
				return nil, err
			}
			relationship.TargetIdentifier = identifier.String()
		}
		if relationship.Type == "supersedes" && relationship.TargetVersion == "" {
			return nil, E(CodeInvalidArgument, "a supersedes relationship requires an exact target version")
		}
		targetResource, err := s.repo.GetResourceByIdentifier(ctx, tenantID, relationship.TargetIdentifier)
		if err != nil {
			if ErrorCodeOf(err) == CodeNotFound {
				return nil, E(CodeInvalidArgument, "relationship target does not exist in the tenant Catalog")
			}
			return nil, err
		}
		var targetVersion ResourceVersion
		if relationship.TargetVersion != "" {
			targetVersion, err = s.repo.GetVersionByLabel(ctx, tenantID, targetResource.ID, relationship.TargetVersion)
			if err != nil {
				if ErrorCodeOf(err) == CodeNotFound {
					return nil, E(CodeInvalidArgument, "relationship target version does not exist in the tenant Catalog")
				}
				return nil, err
			}
		}
		if targetResource.ID == sourceResource.ID && (relationship.TargetVersion == "" || targetVersion.ID == sourceVersion.ID) {
			return nil, E(CodeInvalidArgument, "a listing revision cannot relate a version to itself")
		}
		key := relationship.Type + "\x00" + relationship.TargetIdentifier + "\x00" + relationship.TargetVersion
		if _, exists := seen[key]; exists {
			return nil, E(CodeInvalidArgument, "a listing revision cannot contain duplicate relationships")
		}
		seen[key] = struct{}{}
		normalized = append(normalized, relationship)
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Type != normalized[j].Type {
			return normalized[i].Type < normalized[j].Type
		}
		if normalized[i].TargetIdentifier != normalized[j].TargetIdentifier {
			return normalized[i].TargetIdentifier < normalized[j].TargetIdentifier
		}
		return normalized[i].TargetVersion < normalized[j].TargetVersion
	})
	return normalized, nil
}

// validRelationshipType reports whether a relationship name belongs to the
// supported Catalog vocabulary.
func validRelationshipType(value string) bool {
	switch value {
	case "depends_on", "recommended_with", "supersedes", "bundled_with":
		return true
	default:
		return false
	}
}

// validateListingDiscoveryMetadata enforces per-field collection and text
// limits before retaining tags, categories and representative queries.
func validateListingDiscoveryMetadata(tags, categories, queries []string) error {
	if len(tags) > 32 || len(categories) > 16 || len(queries) > 5 {
		return E(CodeInvalidArgument, "listing tags, categories, or representative queries exceed their supported limits")
	}
	for _, values := range [][]string{tags, categories} {
		for _, value := range values {
			if !utf8.ValidString(value) || utf8.RuneCountInString(strings.TrimSpace(value)) > 80 || strings.ContainsRune(value, '\x00') {
				return E(CodeInvalidArgument, "listing tags and categories cannot exceed 80 characters")
			}
		}
	}
	for _, query := range queries {
		query = strings.Join(strings.Fields(query), " ")
		if !validTextLength(query, 240) || utf8.RuneCountInString(query) < 3 {
			return E(CodeInvalidArgument, "representative queries must contain between 3 and 240 characters")
		}
	}
	return nil
}

// Submit binds a listing's current revisions and validation evidence into a
// review submission.
func (s *Service) Submit(ctx context.Context, listingID string) (ReviewSubmission, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return ReviewSubmission{}, err
	}
	listing, err := s.repo.GetListing(ctx, actor.TenantID, listingID)
	if err != nil {
		return ReviewSubmission{}, err
	}
	if _, err = requirePermission(ctx, "review.submit", listing.NamespaceID); err != nil {
		return ReviewSubmission{}, err
	}
	if listing.CandidateRevisionID == "" {
		return ReviewSubmission{}, E(CodePreconditionFailed, "listing has no candidate revision")
	}
	listingRevision, err := s.repo.GetListingRevision(ctx, actor.TenantID, listing.CandidateRevisionID)
	if err != nil {
		return ReviewSubmission{}, err
	}
	versionRevision, evidence, err := s.repo.GetVersionRevision(ctx, actor.TenantID, listingRevision.VersionRevisionID)
	if err != nil {
		return ReviewSubmission{}, err
	}
	if evidence.State != EvidenceComplete {
		return ReviewSubmission{}, &DomainError{Code: CodeValidationFailed, Message: "blocking validation findings must be resolved before submission"}
	}
	now := s.clock.Now()
	if !s.validationEvidenceCurrentForRevision(versionRevision, evidence, now) {
		return ReviewSubmission{}, E(CodePreconditionFailed, "validation evidence is not current")
	}
	idValue, err := s.ids.New(now)
	if err != nil {
		return ReviewSubmission{}, Wrap(CodeInternal, "create submission identifier", err)
	}
	submission := ReviewSubmission{ID: idValue, TenantID: actor.TenantID, ListingID: listing.ID, VersionRevisionID: versionRevision.ID, ListingRevisionID: listingRevision.ID, EvidenceSetID: evidence.ID, EvidenceDigest: evidence.Digest, PolicyVersion: evidence.PolicyVersion, State: SubmissionSubmitted, SubmittedBy: actor.Subject, Revision: 1, SubmittedAt: now}
	audit, outbox, err := s.evidence(ctx, actor, "review_submission.submitted", "review_submission", submission.ID, submission)
	if err != nil {
		return ReviewSubmission{}, err
	}
	return s.repo.Submit(ctx, SubmitParams{Submission: submission, Audit: audit, Outbox: outbox})
}

// GetSubmission returns a review submission within the authenticated reader's
// tenant.
func (s *Service) GetSubmission(ctx context.Context, submissionID string) (ReviewSubmission, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return ReviewSubmission{}, err
	}
	value, err := s.repo.GetSubmission(ctx, actor.TenantID, submissionID)
	if err != nil {
		return ReviewSubmission{}, err
	}
	listing, err := s.repo.GetListing(ctx, actor.TenantID, value.ListingID)
	if err != nil {
		return ReviewSubmission{}, err
	}
	if _, err := requirePermission(ctx, "resource.read_history", listing.NamespaceID); err != nil {
		return ReviewSubmission{}, err
	}
	value, err = s.repo.GetSubmission(ctx, actor.TenantID, submissionID)
	if err == nil {
		value.NamespaceID = listing.NamespaceID
		value.ListingConcurrencyRevision = listing.Revision
	}
	return value, err
}

// DecideSubmission applies an authorized review decision with reason,
// separation-of-duties checks and optimistic concurrency.
func (s *Service) DecideSubmission(ctx context.Context, submissionID, action, reason string, expectedRevision int64) (ReviewSubmission, ResourceVersion, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return ReviewSubmission{}, ResourceVersion{}, err
	}
	if action != "approve" && action != "request_changes" && action != "reject" {
		return ReviewSubmission{}, ResourceVersion{}, E(CodeInvalidArgument, "review action is invalid")
	}
	reason, err = normalizeAuditReason("review_submission."+action, reason, "review decision reason is invalid")
	if err != nil {
		return ReviewSubmission{}, ResourceVersion{}, err
	}
	submission, err := s.repo.GetSubmission(ctx, actor.TenantID, submissionID)
	if err != nil {
		return ReviewSubmission{}, ResourceVersion{}, err
	}
	listing, err := s.repo.GetListing(ctx, actor.TenantID, submission.ListingID)
	if err != nil {
		return ReviewSubmission{}, ResourceVersion{}, err
	}
	if _, err = requirePermission(ctx, "review.decide", listing.NamespaceID); err != nil {
		return ReviewSubmission{}, ResourceVersion{}, err
	}
	origin, err := s.repo.GetOriginatingPrincipal(ctx, actor.TenantID, "review_submission", submission.ID)
	if err != nil {
		return ReviewSubmission{}, ResourceVersion{}, err
	}
	if origin.SamePrincipal(actor) {
		return ReviewSubmission{}, ResourceVersion{}, E(CodeSeparationOfDuties, "a submitter cannot review the same submission")
	}
	expectedValidationEvidenceID := ""
	mutationPriors := []time.Time{submission.SubmittedAt}
	if action == "approve" {
		revision, currentEvidence, validationErr := s.repo.GetVersionRevision(ctx, actor.TenantID, submission.VersionRevisionID)
		if validationErr != nil {
			return ReviewSubmission{}, ResourceVersion{}, validationErr
		}
		boundEvidence, validationErr := s.repo.GetValidationEvidence(ctx, actor.TenantID, revision.ID, submission.EvidenceSetID)
		if validationErr != nil {
			if ErrorCodeOf(validationErr) == CodeNotFound {
				return ReviewSubmission{}, ResourceVersion{}, E(CodePreconditionFailed, "review submission validation evidence is no longer available")
			}
			return ReviewSubmission{}, ResourceVersion{}, validationErr
		}
		if boundEvidence.Digest != submission.EvidenceDigest || boundEvidence.SubjectDigest != revision.ContentSetDigest || boundEvidence.PolicyVersion != submission.PolicyVersion || currentEvidence.ID != boundEvidence.ID || !s.validationEvidenceCurrentForRevision(revision, currentEvidence, s.clock.Now()) {
			return ReviewSubmission{}, ResourceVersion{}, E(CodePreconditionFailed, "review submission validation evidence is no longer current")
		}
		version, validationErr := s.repo.GetVersion(ctx, actor.TenantID, revision.ResourceVersionID)
		if validationErr != nil {
			return ReviewSubmission{}, ResourceVersion{}, validationErr
		}
		mutationPriors = append(mutationPriors, version.UpdatedAt)
		expectedValidationEvidenceID = currentEvidence.ID
	}
	at := mutationTime(s.clock.Now(), mutationPriors...)
	audit, outbox, err := s.evidenceAt(ctx, actor, "review_submission."+action, "review_submission", submissionID, reason, map[string]string{"action": action}, at)
	if err != nil {
		return ReviewSubmission{}, ResourceVersion{}, err
	}
	return s.repo.DecideSubmission(ctx, DecideSubmissionParams{ActorMembershipID: actor.MembershipID, ActorRealmID: actor.RealmID, TenantID: actor.TenantID, SubmissionID: submissionID, ExpectedRevision: expectedRevision, ExpectedValidationEvidenceID: expectedValidationEvidenceID, Actor: actor.Subject, Action: action, Reason: reason, At: at, Audit: audit, Outbox: outbox})
}

type PublishInput struct {
	SubmissionID     string       `json:"submissionId"`
	PriorDisposition ReleaseState `json:"priorDisposition,omitempty"`
	PriorReason      string       `json:"priorReason,omitempty"`
}

// Publish binds an approved submission to its exact current evidence and creates
// a release atomically; it does not publish mutable draft state.
func (s *Service) Publish(ctx context.Context, listingID string, input PublishInput, expectedRevision int64) (CatalogListing, ListingRelease, *ListingRelease, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return CatalogListing{}, ListingRelease{}, nil, err
	}
	disposition := input.PriorDisposition
	if disposition == "" {
		disposition = ReleaseRetired
	}
	if disposition != ReleaseRetired && disposition != ReleaseSuperseded {
		return CatalogListing{}, ListingRelease{}, nil, E(CodeInvalidArgument, "prior release disposition must be retired or superseded")
	}
	priorReason, err := normalizeAuditReason("listing_release.published", input.PriorReason, "prior release reason is invalid")
	if err != nil {
		return CatalogListing{}, ListingRelease{}, nil, err
	}
	if disposition == ReleaseSuperseded && priorReason == "" {
		return CatalogListing{}, ListingRelease{}, nil, E(CodeInvalidArgument, "explicit supersession requires a reason")
	}
	listing, err := s.repo.GetListing(ctx, actor.TenantID, listingID)
	if err != nil {
		return CatalogListing{}, ListingRelease{}, nil, err
	}
	if _, err = requirePermission(ctx, "release.publish", listing.NamespaceID); err != nil {
		return CatalogListing{}, ListingRelease{}, nil, err
	}
	submission, err := s.repo.GetSubmission(ctx, actor.TenantID, input.SubmissionID)
	if err != nil {
		return CatalogListing{}, ListingRelease{}, nil, err
	}
	if submission.ListingID != listing.ID || submission.State != SubmissionApproved {
		return CatalogListing{}, ListingRelease{}, nil, E(CodePreconditionFailed, "the exact approved listing submission is required")
	}
	versionRevision, currentEvidence, err := s.repo.GetVersionRevision(ctx, actor.TenantID, submission.VersionRevisionID)
	if err != nil {
		return CatalogListing{}, ListingRelease{}, nil, err
	}
	approvedEvidence, err := s.repo.GetValidationEvidence(ctx, actor.TenantID, versionRevision.ID, submission.EvidenceSetID)
	if err != nil {
		if ErrorCodeOf(err) == CodeNotFound {
			return CatalogListing{}, ListingRelease{}, nil, E(CodePublicationNotReady, "approved evidence is missing, stale, or blocked")
		}
		return CatalogListing{}, ListingRelease{}, nil, err
	}
	if approvedEvidence.Digest != submission.EvidenceDigest || approvedEvidence.SubjectDigest != versionRevision.ContentSetDigest || approvedEvidence.PolicyVersion != submission.PolicyVersion || !s.validationEvidenceCurrentForRevision(versionRevision, currentEvidence, s.clock.Now()) {
		return CatalogListing{}, ListingRelease{}, nil, E(CodePublicationNotReady, "approved evidence is missing, stale, or blocked")
	}
	externalVerification, err := s.validateExternalPublication(ctx, actor.TenantID, versionRevision)
	if err != nil {
		return CatalogListing{}, ListingRelease{}, nil, err
	}
	listingRevision, err := s.repo.GetListingRevision(ctx, actor.TenantID, submission.ListingRevisionID)
	if err != nil {
		return CatalogListing{}, ListingRelease{}, nil, err
	}
	if listingRevision.Visibility == VisibilityPublic {
		if len(listingRevision.RepresentativeQueries) < 2 || len(listingRevision.RepresentativeQueries) > 5 {
			return CatalogListing{}, ListingRelease{}, nil, E(CodePublicationNotReady, "public ARD publication requires two to five representative queries")
		}
		resource, resourceErr := s.repo.GetResource(ctx, actor.TenantID, listing.ResourceID)
		if resourceErr != nil {
			return CatalogListing{}, ListingRelease{}, nil, resourceErr
		}
		identifier, parseErr := ParseAIRIdentifier(resource.CanonicalIdentifier)
		if parseErr != nil {
			return CatalogListing{}, ListingRelease{}, nil, E(CodePublicationNotReady, "public resources require a normalized urn:air identifier")
		}
		publisher, publisherErr := s.repo.GetPublisher(ctx, actor.TenantID, listingRevision.PublisherID)
		if publisherErr != nil {
			if ErrorCodeOf(publisherErr) != CodeNotFound {
				return CatalogListing{}, ListingRelease{}, nil, publisherErr
			}
			return CatalogListing{}, ListingRelease{}, nil, E(CodePublicationNotReady, "publisher domain is not approved for publication")
		}
		if !slices.Contains(publisher.ApprovedPublicationDomains, identifier.Publisher) {
			return CatalogListing{}, ListingRelease{}, nil, E(CodePublicationNotReady, "publisher domain is not approved for publication")
		}
	}
	mutationPriors := []time.Time{listing.UpdatedAt}
	if listing.CurrentReleaseID != "" {
		currentRelease, getErr := s.repo.GetRelease(ctx, actor.TenantID, listing.CurrentReleaseID)
		if getErr != nil {
			return CatalogListing{}, ListingRelease{}, nil, getErr
		}
		mutationPriors = append(mutationPriors, currentRelease.UpdatedAt)
	}
	now := mutationTime(s.clock.Now(), mutationPriors...)
	releaseID, err := s.ids.New(now)
	if err != nil {
		return CatalogListing{}, ListingRelease{}, nil, Wrap(CodeInternal, "create release identifier", err)
	}
	release := ListingRelease{ID: releaseID, TenantID: actor.TenantID, ListingID: listing.ID, SubmissionID: submission.ID, VersionRevisionID: submission.VersionRevisionID, ListingRevisionID: submission.ListingRevisionID, State: ReleasePublished, ActivatedBy: actor.Subject, Revision: 1, ActivatedAt: now, UpdatedAt: now}
	var eventSubject any = release
	if len(externalVerification) > 0 {
		eventSubject = map[string]any{"release": release, "externalVerification": externalVerification}
	}
	audit, outbox, err := s.evidenceAt(ctx, actor, "listing_release.published", "listing_release", release.ID, priorReason, eventSubject, now)
	if err != nil {
		return CatalogListing{}, ListingRelease{}, nil, err
	}
	if len(externalVerification) > 0 {

		externalDigest, digestErr := digestJSON(externalVerification)
		if digestErr != nil {
			return CatalogListing{}, ListingRelease{}, nil, Wrap(CodeInternal, "digest external publication verification", digestErr)
		}
		audit.Details["facts"] = map[string]any{"externalVerificationCount": len(externalVerification), "externalVerificationDigest": externalDigest}
		if err := auditpkg.ValidateNewRecord(audit); err != nil {
			return CatalogListing{}, ListingRelease{}, nil, Wrap(CodeInternal, "validate publication audit event", err)
		}

	}
	return s.repo.Publish(ctx, PublishParams{TenantID: actor.TenantID, ListingID: listing.ID, SubmissionID: submission.ID, ExpectedRevision: expectedRevision, ExpectedValidationEvidenceID: currentEvidence.ID, PriorDisposition: disposition, PriorReason: priorReason, Actor: actor.Subject, Release: release, Audit: audit, Outbox: outbox})
}

// validationEvidenceCurrent checks completed evidence against the active
// validation policy and expiry.
func (s *Service) validationEvidenceCurrent(evidence EvidenceSet, now time.Time) bool {
	return evidence.State == EvidenceComplete && evidence.ValidationProfile == currentValidationProfile && evidence.PolicyVersion == s.policy && (evidence.ExpiresAt.IsZero() || evidence.ExpiresAt.After(now))
}

// validationEvidenceCurrentForRevision also verifies the retained revision's
// exact evidence and retrieval bindings.
func (s *Service) validationEvidenceCurrentForRevision(revision VersionRevision, evidence EvidenceSet, now time.Time) bool {
	if evidence.SubjectDigest != revision.ContentSetDigest || !s.validationEvidenceCurrent(evidence, now) || ValidateExternalRetrievals(evidence.ExternalRetrievals) != nil {
		return false
	}
	expected := make(map[string]Representation)
	for _, representation := range revision.Representations {
		if representation.URL != "" {
			expected[representation.ID] = representation
		}
	}
	if len(expected) == 0 {
		return len(evidence.ExternalRetrievals) == 0 && evidence.ExpiresAt.IsZero()
	}
	if s.externalFetcher == nil || !sha256DigestPattern.MatchString(s.externalFetcher.PolicyDigest()) || len(expected) != len(evidence.ExternalRetrievals) || !evidence.ExpiresAt.Equal(earliestExternalRetrievalExpiry(evidence.ExternalRetrievals)) {
		return false
	}
	for _, retrieval := range evidence.ExternalRetrievals {
		representation, ok := expected[retrieval.RepresentationID]
		if !ok || representation.ArtifactID == "" && representation.Plugin == nil || representation.Plugin != nil && ValidatePluginInspection(representation.Plugin) != nil || retrieval.RequestedURL != representation.URL || retrieval.Status != http.StatusOK || retrieval.ContentDigest != representation.ContentDigest || retrieval.Size != representation.Size || retrieval.PolicyDigest != s.externalFetcher.PolicyDigest() || !retrieval.ExpiresAt.After(now) || !sameHTTPSOrigin(retrieval.RequestedURL, retrieval.FinalURL) {
			return false
		}
		mediaType, err := representationBaseMediaType(representation.MediaType)
		if err != nil || mediaType != retrieval.MediaType && !PluginResponseMediaType(mediaType, retrieval.MediaType) {
			return false
		}
	}
	return true
}

// validateExternalPublication refreshes external representation evidence before
// publication and rejects changed or unavailable content.
func (s *Service) validateExternalPublication(ctx context.Context, tenantID string, revision VersionRevision) ([]ExternalRetrieval, error) {
	retrievals := make([]ExternalRetrieval, 0)
	var fetchedBytes int64
	for _, representation := range revision.Representations {
		if representation.URL == "" {
			continue
		}
		if representation.Plugin != nil {
			if err := ValidatePluginInspection(representation.Plugin); err != nil {
				return nil, err
			}
			remaining := MaxCatalogDocumentBytes - fetchedBytes
			if remaining <= 0 {
				return nil, E(CodePublicationNotReady, "Plugin verification exceeds the publication byte limit")
			}
			result, err := s.fetchExternalRepresentation(ctx, tenantID, representation.URL, representation.MediaType, max(remaining, int64(1024)))
			if err != nil {
				return nil, err
			}
			fetchedBytes += int64(len(result.Body))
			if fetchedBytes > MaxCatalogDocumentBytes || result.Status != http.StatusOK || !PluginResponseMediaType(representation.MediaType, result.MediaType) || result.ContentDigest != representation.ContentDigest || int64(len(result.Body)) != representation.Size {
				return nil, E(CodePublicationNotReady, "Plugin archive no longer matches the reviewed bytes")
			}
			retrievals = append(retrievals, externalRetrievalFromResult(representation.ID, result))
			continue
		}
		document, err := s.repo.GetDocument(ctx, tenantID, representation.ArtifactID)
		if err != nil {
			if ErrorCodeOf(err) == CodeNotFound {
				return nil, E(CodePublicationNotReady, "external representation validation bytes are unavailable")
			}
			return nil, err
		}
		if document.ID != representation.ArtifactID || document.Digest != representation.ContentDigest || document.MediaType != representation.MediaType || document.Size != representation.Size || digestBytes(document.Content) != representation.ContentDigest {
			return nil, E(CodePublicationNotReady, "external representation validation bytes failed their digest check")
		}
		remaining := MaxCatalogDocumentBytes - fetchedBytes
		if remaining <= 0 {
			return nil, E(CodePublicationNotReady, "external representation verification exceeds the publication byte limit")
		}
		result, err := s.fetchExternalRepresentation(ctx, tenantID, representation.URL, representation.MediaType, max(remaining, int64(1024)))
		if err != nil {
			return nil, err
		}
		fetchedBytes += int64(len(result.Body))
		baseMediaType, mediaTypeErr := representationBaseMediaType(representation.MediaType)
		if fetchedBytes > MaxCatalogDocumentBytes || result.Status != http.StatusOK || mediaTypeErr != nil || result.MediaType != baseMediaType || result.ContentDigest != representation.ContentDigest || int64(len(result.Body)) != representation.Size || !bytes.Equal(result.Body, document.Content) {
			return nil, E(CodePublicationNotReady, "external representation no longer matches the reviewed bytes")
		}
		retrievals = append(retrievals, externalRetrievalFromResult(representation.ID, result))
	}
	return retrievals, nil
}

// TransitionRelease applies a one-way release lifecycle change and withdraws
// affected public snapshots once the governed transition has committed.
func (s *Service) TransitionRelease(ctx context.Context, releaseID string, target ReleaseState, reason, replacementID string, sunsetAt *time.Time, expectedRevision int64) (ListingRelease, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return ListingRelease{}, err
	}
	if target != ReleaseDeprecated && target != ReleaseRetired && target != ReleaseSuperseded && target != ReleaseRevoked {
		return ListingRelease{}, E(CodeInvalidArgument, "release transition is invalid")
	}
	reason, err = normalizeAuditReason("listing_release."+string(target), reason, "release transition reason is invalid")
	if err != nil {
		return ListingRelease{}, err
	}
	release, err := s.repo.GetRelease(ctx, actor.TenantID, releaseID)
	if err != nil {
		return ListingRelease{}, err
	}
	listing, err := s.repo.GetListing(ctx, actor.TenantID, release.ListingID)
	if err != nil {
		return ListingRelease{}, err
	}
	if _, err = requirePermission(ctx, "release.transition", listing.NamespaceID); err != nil {
		return ListingRelease{}, err
	}
	if target == ReleaseSuperseded && replacementID == "" {
		return ListingRelease{}, E(CodeInvalidArgument, "supersession requires another replacement release")
	}
	if target != ReleaseDeprecated && target != ReleaseSuperseded && replacementID != "" {
		return ListingRelease{}, E(CodeInvalidArgument, "only deprecation or supersession accepts replacement guidance")
	}
	if target != ReleaseDeprecated && sunsetAt != nil {
		return ListingRelease{}, E(CodeInvalidArgument, "only deprecation accepts sunset guidance")
	}
	if replacementID != "" && !identity.ValidTenantID(replacementID) {
		return ListingRelease{}, E(CodeInvalidArgument, "replacementId must be a UUID")
	}
	transitionAt := mutationTime(s.clock.Now(), release.UpdatedAt, listing.UpdatedAt)
	if sunsetAt != nil {
		normalizedSunset := sunsetAt.UTC().Truncate(time.Microsecond)
		if !normalizedSunset.After(transitionAt) {
			return ListingRelease{}, E(CodeInvalidArgument, "deprecation sunset guidance must be in the future")
		}
		sunsetAt = &normalizedSunset
	}
	if replacementID != "" {
		if replacementID == releaseID {
			return ListingRelease{}, E(CodeInvalidArgument, "replacement guidance must identify another release")
		}
		replacement, replacementErr := s.repo.GetRelease(ctx, actor.TenantID, replacementID)
		if replacementErr != nil {
			return ListingRelease{}, replacementErr
		}
		if replacement.State != ReleasePublished && replacement.State != ReleaseDeprecated {
			return ListingRelease{}, E(CodeInvalidArgument, "replacement release must remain discoverable")
		}
	}
	audit, outbox, err := s.evidenceAt(ctx, actor, "listing_release."+string(target), "listing_release", release.ID, reason, map[string]any{"state": target, "replacementId": replacementID, "sunsetAt": sunsetAt}, transitionAt)
	if err != nil {
		return ListingRelease{}, err
	}
	transitioned, err := s.repo.TransitionRelease(ctx, TransitionReleaseParams{TenantID: actor.TenantID, ReleaseID: releaseID, ExpectedRevision: expectedRevision, TargetState: target, Reason: reason, ReplacementID: replacementID, SunsetAt: sunsetAt, Actor: actor.Subject, At: transitionAt, Audit: audit, Outbox: outbox})
	if err != nil {
		return ListingRelease{}, err
	}
	if target != ReleaseDeprecated && s.publicationWithdrawer != nil {
		if _, err := s.publicationWithdrawer.WithdrawPublicationRelease(ctx, actor.TenantID, releaseID, "listing_release."+string(target), releaseID, "release_"+string(target)); err != nil {
			return ListingRelease{}, err
		}
	}
	return transitioned, nil
}

// GetRelease returns retained release metadata within the authenticated
// reader's tenant.
func (s *Service) GetRelease(ctx context.Context, releaseID string) (ListingRelease, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return ListingRelease{}, err
	}
	value, err := s.repo.GetRelease(ctx, actor.TenantID, releaseID)
	if err != nil {
		return ListingRelease{}, err
	}
	listing, err := s.repo.GetListing(ctx, actor.TenantID, value.ListingID)
	if err != nil {
		return ListingRelease{}, err
	}
	if _, err := requirePermission(ctx, "resource.read_history", listing.NamespaceID); err != nil {
		return ListingRelease{}, err
	}
	return s.repo.GetRelease(ctx, actor.TenantID, releaseID)
}

// GetArtifact validates retained document integrity and appends private-read
// evidence before returning its bytes.
func (s *Service) GetArtifact(ctx context.Context, artifactID string) (Artifact, []byte, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return Artifact{}, nil, err
	}
	if _, err := requirePermission(ctx, "resource.read_content", ""); err != nil {
		if ErrorCodeOf(err) != CodeForbidden {
			return Artifact{}, nil, err
		}
		reader, ok := s.repo.(DocumentAccessReader)
		if !ok {
			return Artifact{}, nil, err
		}
		scope, err := reader.DocumentAccessNamespace(ctx, actor.TenantID, artifactID)
		if err != nil {
			return Artifact{}, nil, err
		}
		if _, err := requirePermission(ctx, "resource.read_content", scope); err != nil {
			return Artifact{}, nil, err
		}
	}
	artifact, content, err := s.getArtifact(ctx, actor.TenantID, artifactID)
	if err != nil {
		return Artifact{}, nil, err
	}
	record, err := s.newActorAuditRecord(ctx, actor, auditpkg.ActionCatalogDocumentRead, "catalog_document", artifact.ID, "", map[string]any{"contentDigest": artifact.Digest, "sizeBytes": len(content)}, s.clock.Now())
	if err != nil {
		return Artifact{}, nil, err
	}
	record.AfterDigest = artifact.Digest
	if err := auditpkg.ValidateNewRecord(record); err != nil {
		return Artifact{}, nil, Wrap(CodeInternal, "validate Catalog-document read evidence", err)
	}
	if err := s.repo.AppendAuditEvent(ctx, record); err != nil {
		return Artifact{}, nil, Wrap(CodeDependencyUnavailable, "retain Catalog-document read evidence", err)
	}
	return artifact, content, nil
}

// GetPublicArtifact returns integrity-checked document bytes for an explicit
// tenant. The caller must first verify membership in the active public
// snapshot.
func (s *Service) GetPublicArtifact(ctx context.Context, tenantID, artifactID string) (Artifact, []byte, error) {
	return s.getArtifact(ctx, tenantID, artifactID)
}

// getArtifact checks the document ID, tenant reference and retained integrity
// before returning copied bytes.
func (s *Service) getArtifact(ctx context.Context, tenantID, artifactID string) (Artifact, []byte, error) {
	if !validDocumentID(artifactID) {
		return Artifact{}, nil, E(CodeInvalidArgument, "catalog document identifier is invalid")
	}
	document, err := s.repo.GetDocument(ctx, tenantID, artifactID)
	if err == nil {
		if validationErr := ValidateDocumentBlob(tenantID, document); validationErr != nil {
			return Artifact{}, nil, E(CodeDependencyUnavailable, "catalog document failed its integrity check")
		}
		return Artifact{ID: document.ID, TenantID: document.TenantID, Digest: document.Digest, MediaType: document.MediaType, Size: document.Size, CreatedAt: document.CreatedAt}, append([]byte(nil), document.Content...), nil
	}
	return Artifact{}, nil, err
}

// validDocumentID reports whether the document key is exactly a lowercase
// SHA-256 hexadecimal digest.
func validDocumentID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// Search returns tenant-readable local Catalog results with a signed query- and
// authorization-bound continuation.
func (s *Service) Search(ctx context.Context, query SearchQuery) (Page[CatalogSummary], error) {
	ctx, actor, err := collectionContext(ctx)
	if err != nil {
		return Page[CatalogSummary]{}, err
	}
	if err := validateCatalogSearchQuery(query); err != nil {
		return Page[CatalogSummary]{}, err
	}
	query = normalizeCatalogSearchQuery(query)
	if query.Limit <= 0 {
		query.Limit = 20
	}
	hybrid, used, fallback, err := s.semanticPage(ctx, actor, query, true)
	if err != nil {
		return Page[CatalogSummary]{}, err
	}
	if used {
		page := Page[CatalogSummary]{Items: []CatalogSummary{}, NextCursor: hybrid.NextCursor, Total: hybrid.DurableTotal, Tier: hybrid.Tier, Profile: hybrid.Profile, PartialIndex: hybrid.PartialIndex}
		for _, item := range hybrid.Items {
			if item.Local == nil {
				return Page[CatalogSummary]{}, E(CodeInternal, "hybrid local result missing")
			}
			page.Items = append(page.Items, item.Local.browseSummary(item))
		}
		return page, nil
	}
	binding, err := s.searchCursorBinding(ctx, actor, query)
	if err != nil {
		return Page[CatalogSummary]{}, err
	}
	cursor, hasCursor, err := s.decodeSearchCursor(query.Cursor, binding)
	if err != nil {
		return Page[CatalogSummary]{}, err
	}
	request := CatalogSearchRepositoryRequest{Query: query, Limit: query.Limit}
	if hasCursor {
		request.ExpectedGeneration = cursor.Generation
		request.After = &CatalogSearchPosition{Score: cursor.Score, DisplayName: cursor.DisplayName, ResultID: cursor.ResultID, SortValue: cursor.SortValue}
	}
	page, err := s.repo.SearchCatalog(ctx, actor.TenantID, request)
	if err != nil {
		return Page[CatalogSummary]{}, err
	}
	next := ""
	if page.HasMore && len(page.Items) > 0 {
		next, err = s.encodeSearchCursor(page.Generation, catalogRecordPosition(page.Items[len(page.Items)-1], query.Order), binding)
		if err != nil {
			return Page[CatalogSummary]{}, err
		}
	}
	return Page[CatalogSummary]{Items: page.Items, NextCursor: next, Total: page.Total, FallbackReason: fallback}, nil
}

// Explore validates requested facets and aggregates matching local Catalog
// records for an authenticated reader.
func (s *Service) Explore(ctx context.Context, request ExploreRequest) (ExploreResponse, error) {
	ctx, actor, err := collectionContext(ctx)
	if err != nil {
		return ExploreResponse{}, err
	}
	if len(request.Facets) == 0 || len(request.Facets) > 10 {
		return ExploreResponse{}, E(CodeInvalidArgument, "between one and ten facets are required")
	}
	if request.Query.Limit != 0 || request.Query.Cursor != "" {
		return ExploreResponse{}, E(CodeInvalidArgument, "catalog explore does not accept pagination controls")
	}
	if err := validateCatalogSearchQuery(request.Query); err != nil {
		return ExploreResponse{}, err
	}
	request.Query = normalizeCatalogSearchQuery(request.Query)
	seen := make(map[string]struct{}, len(request.Facets))
	for index := range request.Facets {
		facet := &request.Facets[index]
		if !validCatalogFacet(facet.Field) {
			return ExploreResponse{}, E(CodeInvalidArgument, "facet field is not supported")
		}
		if _, duplicate := seen[facet.Field]; duplicate {
			return ExploreResponse{}, E(CodeInvalidArgument, "catalog facet fields must be unique")
		}
		seen[facet.Field] = struct{}{}
		if facet.Limit < 0 || facet.MinCount < 0 {
			return ExploreResponse{}, E(CodeInvalidArgument, "facet limits are invalid")
		}
		if facet.Limit == 0 {
			facet.Limit = 20
		}
		if facet.Limit > MaxCatalogPageSize {
			return ExploreResponse{}, E(CodeInvalidArgument, "facet limits are invalid")
		}
	}
	response, used, fallback, err := s.semanticExplore(ctx, actor, request)
	if err != nil || used {
		return response, err
	}
	response, err = s.repo.ExploreCatalog(ctx, actor.TenantID, request.Query, request.Facets)
	response.FallbackReason = fallback
	return response, err
}

// GetCatalogRecord returns the release-backed Catalog projection within the
// authenticated reader's tenant.
func (s *Service) GetCatalogRecord(ctx context.Context, releaseID string) (CatalogRecord, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return CatalogRecord{}, err
	}
	value, err := s.repo.GetCatalogRecord(ctx, actor.TenantID, releaseID)
	if err != nil {
		return CatalogRecord{}, err
	}
	if _, err := requirePermission(ctx, "resource.read", value.Resource.NamespaceID); err != nil {
		return CatalogRecord{}, err
	}
	return s.repo.GetCatalogRecord(ctx, actor.TenantID, releaseID)
}

// PublicRecords returns the bounded public-only publication projection for an
// explicit tenant.
func (s *Service) PublicRecords(ctx context.Context, tenantID string) ([]CatalogRecord, error) {
	records, overflow, err := s.PublicationRecords(ctx, tenantID, false, MaxPublicationRecords)
	if err != nil {
		return nil, err
	}
	if overflow {
		return nil, E(CodeWorkLimitExceeded, "eligible publication records exceed the supported limit")
	}
	return records, nil
}

// PublicationRecords returns one complete bounded eligibility set. Overflow is
// explicit because publishing a truncated set would silently withdraw entries.
func (s *Service) PublicationRecords(ctx context.Context, tenantID string, includeTenant bool, limit int) ([]CatalogRecord, bool, error) {
	page, err := s.repo.PublicationRecords(ctx, tenantID, PublicationRecordRequest{IncludeTenant: includeTenant, Limit: limit})
	if err != nil {
		return nil, false, err
	}
	if page.Overflow {
		return nil, true, nil
	}
	result := make([]CatalogRecord, 0, len(page.Records))
	for _, record := range page.Records {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if includeTenant && record.ListingRevision.Visibility != VisibilityTenant && record.ListingRevision.Visibility != VisibilityPublic {
			continue
		}
		if _, err := ParseAIRIdentifier(record.Resource.CanonicalIdentifier); err != nil {
			continue
		}
		if !s.validationEvidenceCurrentForRevision(record.VersionRevision, record.Evidence, s.clock.Now()) {
			record.Evidence.State = EvidenceBlocked
			record.Evidence.Findings = append(record.Evidence.Findings, Finding{Code: "PUBLICATION_VALIDATION_NOT_CURRENT", Severity: SeverityError, Message: "Current validation evidence is stale, incomplete, or bound to another outbound policy."})
		}
		result = append(result, record)
	}
	return result, false, nil
}

// AuditEvents authorizes and validates a bounded tenant audit query before
// requesting a continuation sentinel.
func (s *Service) AuditEvents(ctx context.Context, request AuditEventListRequest) ([]AuditEvent, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "audit.read", "")
	if err != nil {
		return nil, err
	}
	if request.Limit < 1 || request.Limit > MaxCatalogPageSize || request.AfterOccurredAt == nil && request.AfterID != "" || request.AfterOccurredAt != nil && !identity.ValidTenantID(request.AfterID) {
		return nil, E(CodeInvalidArgument, "audit-event page position or limit is invalid")
	}
	request.Filters, err = auditpkg.NormalizeQuery(request.Filters, false)
	if err != nil {
		return nil, Wrap(CodeInvalidArgument, "audit-event filters are invalid", err)
	}
	pageLimit := request.Limit
	request.Limit++
	items, err := s.repo.AuditEvents(ctx, actor.TenantID, request)
	if err != nil {
		return nil, err
	}
	prepared := items
	if len(prepared) > pageLimit {
		prepared = prepared[:pageLimit]
	}
	digest, _, err := auditpkg.DigestRecords(prepared)
	if err != nil {
		return nil, TranslateAuditExportError(err)
	}
	access, err := s.newActorAuditRecord(ctx, actor, auditpkg.ActionAuditEventsRead, "audit_event_page", actor.TenantID, "", map[string]any{"eventCount": len(prepared), "eventsDigest": digest}, s.clock.Now())
	if err != nil {
		return nil, err
	}
	access.AfterDigest = digest
	if err := auditpkg.ValidateNewRecord(access); err != nil {
		return nil, Wrap(CodeInternal, "validate audit-page access event", err)
	}
	if err := s.repo.AppendAuditEvent(ctx, access); err != nil {
		return nil, err
	}
	return items, nil
}

// ExportAuditEvents constructs a bounded tenant audit export and retains
// evidence of access to the completed manifest.
func (s *Service) ExportAuditEvents(ctx context.Context, input AuditExportInput) (AuditExport, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "audit.export", "")
	if err != nil {
		return AuditExport{}, err
	}
	filters, err := auditpkg.NormalizeQuery(input.Filters, true)
	if err != nil {
		return AuditExport{}, Wrap(CodeInvalidArgument, "audit-export filters are invalid", err)
	}
	reason, err := auditpkg.NormalizeReason(auditpkg.ActionAuditExported, input.Reason)
	if err != nil {
		return AuditExport{}, Wrap(CodeInvalidArgument, "audit-export reason is invalid", err)
	}
	cutoff := s.clock.Now().UTC()
	if filters.OccurredBefore.After(cutoff) {
		return AuditExport{}, E(CodeInvalidArgument, "audit-export occurredBefore must not be after the server cutoff")
	}
	access, err := s.newActorAuditRecord(ctx, actor, auditpkg.ActionAuditExported, "audit_range", actor.TenantID, reason, map[string]any{
		"rowCount":       0,
		"eventsDigest":   auditpkg.DigestBytes([]byte("[]")),
		"occurredFrom":   filters.OccurredFrom.UTC().Format(time.RFC3339Nano),
		"occurredBefore": filters.OccurredBefore.UTC().Format(time.RFC3339Nano),
	}, cutoff)
	if err != nil {
		return AuditExport{}, err
	}
	return s.repo.ExportAuditEvents(ctx, actor.TenantID, AuditExportRequest{Filters: filters, Cutoff: cutoff, GeneratedAt: cutoff, Access: access})
}

// evidence prepares attributed audit and outbox records at the service clock's
// current time.
func (s *Service) evidence(ctx context.Context, actor identity.Actor, action, targetType, targetID string, value any) (AuditEvent, OutboxEvent, error) {
	return s.evidenceAt(ctx, actor, action, targetType, targetID, "", value, s.clock.Now())
}

// evidenceAt constructs transition evidence at the supplied ordered timestamp
// with separately classified public/internal notification data.
func (s *Service) evidenceAt(ctx context.Context, actor identity.Actor, action, targetType, targetID, reason string, value any, at time.Time) (AuditEvent, OutboxEvent, error) {
	now := at.UTC().Truncate(time.Microsecond)
	auditID, err := s.ids.New(now)
	if err != nil {
		return AuditEvent{}, OutboxEvent{}, Wrap(CodeInternal, "create audit identifier", err)
	}
	outboxID, err := s.ids.New(now.Add(time.Nanosecond))
	if err != nil {
		return AuditEvent{}, OutboxEvent{}, Wrap(CodeInternal, "create outbox identifier", err)
	}
	digest, err := digestJSON(value)
	if err != nil {
		return AuditEvent{}, OutboxEvent{}, Wrap(CodeInternal, "digest event subject", err)
	}
	payload, err := events.MarshalReference(action, targetType, targetID, digest)
	if err != nil {
		return AuditEvent{}, OutboxEvent{}, Wrap(CodeInternal, "marshal outbox event", err)
	}
	var facts map[string]any
	if action == "resource_version.created" || action == "resource_version.revised" {
		if values, ok := value.(map[string]any); ok {
			if revision, ok := values["revision"].(VersionRevision); ok {
				facts = map[string]any{"revisionId": revision.ID}
			}
		}
	}
	if action == "catalog_listing.revised" {
		if revision, ok := value.(ListingRevision); ok {
			facts = map[string]any{"revisionId": revision.ID}
		}
	}
	auditEvent, err := s.newActorAuditRecordWithID(ctx, auditID, actor, action, targetType, targetID, reason, facts, now)
	if err != nil {
		return AuditEvent{}, OutboxEvent{}, err
	}
	outbox := OutboxEvent{CausalTrace: CausalTrace(ctx), ID: outboxID, TenantID: actor.TenantID, Type: CanonicalEventType(action), Subject: targetID, Data: payload, CreatedAt: auditEvent.At}
	if s.acknowledgeOutboxAtCreation {
		publishedAt := auditEvent.At
		outbox.PublishedAt = &publishedAt
	}
	auditEvent.AfterDigest = digest
	if err := auditpkg.ValidateNewRecord(auditEvent); err != nil {
		return AuditEvent{}, OutboxEvent{}, Wrap(CodeInternal, "validate audit event", err)
	}
	return auditEvent, outbox, nil
}

// mutationTime prevents a valid optimistic update from moving a persisted
// timestamp backwards when another API replica's wall clock is slightly ahead.
// PostgreSQL stores microseconds, so ceiling prior values also preserves the
// invariant for deterministic in-memory repositories with finer timestamps.
func mutationTime(candidate time.Time, priors ...time.Time) time.Time {
	candidate = candidate.UTC().Truncate(time.Microsecond)
	for _, prior := range priors {
		prior = prior.UTC()
		if !candidate.Before(prior) {
			continue
		}
		candidate = prior.Truncate(time.Microsecond)
		if candidate.Before(prior) {
			candidate = candidate.Add(time.Microsecond)
		}
	}
	return candidate
}

// newActorAuditRecord allocates an audit ID and builds a validated
// membership-attributed record.
func (s *Service) newActorAuditRecord(ctx context.Context, actor identity.Actor, action, targetType, targetID, reason string, facts map[string]any, at time.Time) (AuditEvent, error) {
	auditID, err := s.ids.New(at)
	if err != nil {
		return AuditEvent{}, Wrap(CodeInternal, "create audit identifier", err)
	}
	return s.newActorAuditRecordWithID(ctx, auditID, actor, action, targetType, targetID, reason, facts, at)
}

// newActorAuditRecordWithID binds an existing audit ID to membership and
// execution attribution before closed-fact validation.
func (s *Service) newActorAuditRecordWithID(ctx context.Context, auditID string, actor identity.Actor, action, targetType, targetID, reason string, facts map[string]any, at time.Time) (AuditEvent, error) {
	reason, err := normalizeAuditReason(action, reason, "audit reason is invalid")
	if err != nil {
		return AuditEvent{}, err
	}

	execution := auditpkg.ExecutionFrom(ctx)
	if execution.AuthorizationDigest == "" || len(identity.RecordedPermissionChecks(ctx)) > 0 {
		execution.AuthorizationDigest = identity.DecisionDigest(ctx)
	}
	record, err := auditpkg.NewRecord(auditID, actor.TenantID, auditpkg.Principal{ID: actor.MembershipID, Type: auditpkg.PrincipalCatalogMembership, ActorType: string(actor.ActorType), RealmID: actor.RealmID}, action, targetType, targetID, reason, execution, facts, at)
	if err != nil {
		return AuditEvent{}, Wrap(CodeInternal, "construct audit event", err)
	}
	return record, nil
}

// normalizeAuditReason applies the action's reason policy and maps failures to
// a client-safe invalid-argument error.
func normalizeAuditReason(action, reason, message string) (string, error) {
	normalized, err := auditpkg.NormalizeReason(action, reason)
	if err != nil {
		return "", Wrap(CodeInvalidArgument, message, err)
	}
	return normalized, nil
}

// CanonicalEventType returns the stable, versioned CloudEvents type for one
// internal domain action.
func CanonicalEventType(action string) string {
	return events.CanonicalType(action)
}

// requireActor requires a resolved actor with a tenant boundary in the request
// context.
func requireActor(ctx context.Context) (identity.Actor, error) {
	actor, ok := identity.FromContext(ctx)
	if !ok || actor.TenantID == "" || actor.Subject == "" {
		return identity.Actor{}, E(CodeForbidden, "authenticated actor is required")
	}
	return actor, nil
}

// requirePermission uses current service-owned action/scope decisions.
func requirePermission(ctx context.Context, action identity.Permission, namespaceID string) (identity.Actor, error) {
	actor, err := identity.RequirePermission(ctx, action, namespaceID)
	if err != nil {
		code := CodeForbidden
		switch identity.ErrorCodeOf(err) {
		case identity.CodeDependency:
			code = CodeDependencyUnavailable
		case identity.CodeInvalidArgument:
			code = CodeInvalidArgument
		case identity.CodeUnauthenticated, identity.CodeForbidden, identity.CodeNotFound, identity.CodeConflict, identity.CodePreconditionFailed, identity.CodeSeparationOfDuties, identity.CodeInternal:
			// Admission failures remain undisclosed; dependency failures are distinct.
		}
		return identity.Actor{}, Wrap(code, "Catalog permission admission failed", err)
	}
	return actor, nil
}

// validSlug checks the service's bounded namespace slug syntax.
func validSlug(value string) bool {
	if value == "" || len(value) > 64 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

// normalizeTerms canonicalizes, deduplicates and bounds set-valued listing
// terms.
func normalizeTerms(values []string, maximum int) []string {
	result := make([]string, 0, min(len(values), maximum))
	for _, value := range values {
		if !utf8.ValidString(value) {
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || utf8.RuneCountInString(value) > 80 || strings.ContainsRune(value, '\x00') || slices.Contains(result, value) {
			continue
		}
		result = append(result, value)
		if len(result) == maximum {
			break
		}
	}
	sort.Strings(result)
	return result
}

// normalizeQueries canonicalizes representative query strings for retained
// discovery metadata.
func normalizeQueries(values []string) []string {
	result := make([]string, 0, min(len(values), 5))
	for _, value := range values {
		value = strings.Join(strings.Fields(value), " ")
		if !validTextLength(value, 240) || utf8.RuneCountInString(value) < 3 || slices.Contains(result, value) {
			continue
		}
		result = append(result, value)
		if len(result) == 5 {
			break
		}
	}
	return result
}

// validateCatalogSearchQuery enforces supported query fields, filter values and
// collection limits.
func validateCatalogSearchQuery(query SearchQuery) error {
	switch query.Order {
	case "", SearchOrderNameAsc, SearchOrderNameDesc, SearchOrderKindAsc, SearchOrderKindDesc, SearchOrderPublisherAsc, SearchOrderPublisherDesc:
	default:
		return E(CodeInvalidArgument, "catalog search order is invalid")
	}
	if query.NamespaceID != "" && !identity.ValidTenantID(query.NamespaceID) {
		return E(CodeInvalidArgument, "namespaceId must be a UUID")
	}
	if query.Origins != nil && (len(query.Origins) == 0 || len(query.Origins) > 3) {
		return E(CodeInvalidArgument, "origins must contain one to three unique supported values")
	}
	seenOrigins := make(map[SearchResultType]bool, len(query.Origins))
	for _, origin := range query.Origins {
		if origin != SearchResultLocal && origin != SearchResultIndexed && origin != SearchResultLive || seenOrigins[origin] {
			return E(CodeInvalidArgument, "origins must contain unique supported values")
		}
		seenOrigins[origin] = true
	}
	if query.NamespaceID != "" && query.Origins != nil && (len(query.Origins) != 1 || query.Origins[0] != SearchResultLocal) {
		return E(CodeInvalidArgument, "a local namespace requires local origin only")
	}
	if !validTextLength(query.License, MaxCompactLicenseCharacters) || query.License != "" && strings.TrimSpace(query.License) == "" || len(query.Categories) > 16 {
		return E(CodeInvalidArgument, "catalog license or categories exceed supported limits")
	}
	for _, category := range query.Categories {
		if !validTextLength(category, 80) || strings.TrimSpace(category) == "" {
			return E(CodeInvalidArgument, "catalog categories require one to 80 characters without NUL")
		}
	}
	if !query.IncludesOrigin(SearchResultLocal) && !query.IncludesOrigin(SearchResultIndexed) && query.Cursor != "" && !strings.HasPrefix(query.Cursor, LiveCursorPrefix) {
		return E(CodeInvalidArgument, "live-only search cursor is invalid")
	}
	if len(query.Text) > 500 || len(query.Publisher) > 256 || strings.ContainsRune(query.Text, '\x00') || strings.ContainsRune(query.Publisher, '\x00') || len(query.Cursor) > MaxCatalogCursorBytes || query.Limit < 0 || query.Limit > MaxCatalogPageSize || len(query.Tags) > 32 || query.Visibility != "" && !query.Visibility.Valid() {
		return E(CodeInvalidArgument, "catalog search query is outside the supported limits")
	}
	seenKinds := make(map[Kind]struct{}, len(query.Kinds))
	for _, kind := range query.Kinds {
		if !kind.Valid() {
			return E(CodeInvalidArgument, "catalog search contains an invalid resource kind")
		}
		if _, duplicate := seenKinds[kind]; duplicate {
			return E(CodeInvalidArgument, "catalog search resource kinds must be unique")
		}
		seenKinds[kind] = struct{}{}
	}
	for _, tag := range query.Tags {
		if !utf8.ValidString(tag) || utf8.RuneCountInString(strings.TrimSpace(tag)) > 80 || strings.ContainsRune(tag, '\x00') {
			return E(CodeInvalidArgument, "catalog search tags cannot exceed 80 characters")
		}
	}
	return nil
}

// filterAndScoreRecords applies Catalog filters, core lexical scoring and
// deterministic relevance/name/release ordering.
func filterAndScoreRecords(records []CatalogSummary, query SearchQuery) []CatalogSummary {
	query = normalizeCatalogSearchQuery(query)
	filtered := make([]CatalogSummary, 0, len(records))
	for _, record := range records {
		if !query.IncludesOrigin(SearchResultLocal) || query.NamespaceID != "" && record.Listing.NamespaceID != query.NamespaceID {
			continue
		}
		if len(query.Kinds) > 0 && !slices.Contains(query.Kinds, record.Resource.Kind) {
			continue
		}
		if query.Visibility != "" && record.ListingRevision.Visibility != query.Visibility {
			continue
		}
		if len(query.Tags) > 0 && !allTerms(record.ListingRevision.Tags, normalizeTerms(query.Tags, 32)) {
			continue
		}
		if query.License != "" && record.ListingRevision.License != query.License || len(query.Categories) > 0 && !allTerms(record.ListingRevision.Categories, query.Categories) {
			continue
		}
		if query.Publisher != "" && record.ListingRevision.PublisherID != query.Publisher {
			continue
		}
		record.RelevanceScore = CoreLexicalScore(SearchDocument{Identifier: record.Resource.CanonicalIdentifier, DisplayName: record.ListingRevision.DisplayName, Description: record.ListingRevision.Description, Tags: record.ListingRevision.Tags, RepresentativeQueries: record.ListingRevision.RepresentativeQueries}, query.Text)
		if query.Text != "" && record.RelevanceScore == 0 {
			continue
		}
		filtered = append(filtered, record)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return searchPositionAfter(catalogRecordPosition(filtered[j], query.Order), catalogRecordPosition(filtered[i], query.Order), query.Order)
	})
	return filtered
}

// validCatalogFacet reports whether a field belongs to the closed Catalog facet
// vocabulary.
func validCatalogFacet(field string) bool {
	switch field {
	case "kind", "visibility", "publisher", "tag", "category", "namespace", "trustState", "source":
		return true
	default:
		return false
	}
}

// catalogFacetValues projects the selected facet from one release-backed
// Catalog record.
func catalogFacetValues(record CatalogSummary, field string) []string {
	switch field {
	case "kind":
		return []string{string(record.Resource.Kind)}
	case "visibility":
		return []string{string(record.ListingRevision.Visibility)}
	case "publisher":
		return []string{record.ListingRevision.PublisherID}
	case "tag":
		return record.ListingRevision.Tags
	case "category":
		return record.ListingRevision.Categories
	case "namespace":
		return []string{record.Listing.NamespaceID}
	case "trustState":
		return []string{string(record.Evidence.Trust.State)}
	case "source":
		return []string{record.Source}
	default:
		return nil
	}
}

// allTerms reports whether every requested normalized term appears in the
// record's values.
func allTerms(haystack, needles []string) bool {
	for _, needle := range needles {
		if !slices.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

// validTextLength requires lossless, NUL-free text within a JSON Schema
// Unicode code-point bound. Transport and document byte budgets are separate.
func validTextLength(value string, maximum int) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= maximum
}

// boundFindings copies only a bounded report; retained evidence is never edited.
func (s *Service) boundFindings(findings []Finding) []Finding {
	report := NewFindingCollector(s.diagnosticLimits)
	report.AddAll("", findings)
	return report.Findings()
}

// validateRepresentation enforces the service budget even for another validator
// implementation. Native validators also bound diagnostics at their producers.
func (s *Service) validateRepresentation(ctx context.Context, kind Kind, input RepresentationInput) ValidationResult {
	result := s.validator.Validate(ctx, kind, input)
	result.Findings = s.boundFindings(result.Findings)
	return result
}

// ValidationLimits returns the effective new-diagnostic budgets, not read limits.
func (s *Service) ValidationLimits() DiagnosticLimits { return s.diagnosticLimits }

// collectionContext marks a private selection; actual repositories enforce rows.
func collectionContext(ctx context.Context) (context.Context, identity.Actor, error) {
	bound, actor, err := identity.WithCollectionContext(ctx)
	if err != nil {
		return bound, actor, PermissionError("Catalog collection admission failed", err)
	}
	return bound, actor, nil
}
