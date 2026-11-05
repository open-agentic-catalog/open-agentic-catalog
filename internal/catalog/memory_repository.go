// The in-memory repository models lifecycle guards and atomic evidence for
// development and tests; production persistence is implemented by PostgreSQL.

package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
)

// MemoryRepository mirrors Repository lifecycle and atomic-evidence semantics
// for deterministic unit tests and the combined development role.
type MemoryRepository struct {
	mu               sync.RWMutex
	namespaces       map[string]Namespace
	publishers       map[string]Publisher
	publisherDomains map[string]PublisherDomainVerification
	resources        map[string]Resource
	versions         map[string]ResourceVersion
	versionRevisions map[string]VersionRevision
	documents        map[string]DocumentBlob
	evidenceSets     map[string]EvidenceSet
	validationRuns   map[string][]string
	listings         map[string]CatalogListing
	listingRevisions map[string]ListingRevision
	submissions      map[string]ReviewSubmission
	releases         map[string]ListingRelease
	workOwners       map[string]workOwner
	origins          map[originKey]originEntry
	audit            []AuditEvent
	outbox           []OutboxEvent
	notification     map[string]NotificationState
}

// NewMemoryRepository creates an empty, concurrency-safe development repository
// with isolated lifecycle and evidence indexes.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		notification:     make(map[string]NotificationState),
		namespaces:       make(map[string]Namespace),
		publishers:       make(map[string]Publisher),
		publisherDomains: make(map[string]PublisherDomainVerification),
		resources:        make(map[string]Resource),
		versions:         make(map[string]ResourceVersion),
		versionRevisions: make(map[string]VersionRevision),
		documents:        make(map[string]DocumentBlob),
		evidenceSets:     make(map[string]EvidenceSet),
		validationRuns:   make(map[string][]string),
		listings:         make(map[string]CatalogListing),
		listingRevisions: make(map[string]ListingRevision),
		submissions:      make(map[string]ReviewSubmission),
		releases:         make(map[string]ListingRelease),
	}
}

// Ping succeeds without external dependencies because all state belongs to this
// process.
func (m *MemoryRepository) Ping(context.Context) error { return nil }

// CreateNamespace enforces unique tenant slugs and commits a copied namespace
// with audit/outbox evidence.
func (m *MemoryRepository) CreateNamespace(ctx context.Context, namespace Namespace, audit AuditEvent, outbox OutboxEvent) (Namespace, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Namespace
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.CreateNamespace(ctx, namespace, audit, outbox)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.namespaces {
		if existing.TenantID == namespace.TenantID && existing.Slug == namespace.Slug {
			return Namespace{}, E(CodeConflict, "namespace slug already exists")
		}
	}
	m.namespaces[namespace.ID] = clone(namespace)
	m.appendEvidence(ctx, audit, outbox)
	return clone(namespace), nil
}

// GetNamespace returns a copy only when the requested namespace belongs to
// tenantID.
func (m *MemoryRepository) GetNamespace(ctx context.Context, tenantID, namespaceID string) (Namespace, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (Namespace, error) { return m.GetNamespace(ctx, tenantID, namespaceID) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	namespace, ok := m.namespaces[namespaceID]
	if !ok || namespace.TenantID != tenantID {
		return Namespace{}, E(CodeNotFound, "namespace was not found")
	}
	return clone(namespace), nil
}

// ListNamespaces returns a bounded tenant page ordered by slug and ID.
func (m *MemoryRepository) ListNamespaces(ctx context.Context, tenantID string, request NamespaceListRequest) ([]Namespace, error) {
	if err := identity.RequireCollectionTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]Namespace, error) { return m.ListNamespaces(ctx, tenantID, request) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Namespace, 0)
	for _, namespace := range m.namespaces {
		if namespace.TenantID != tenantID || request.Slug != "" && namespace.Slug != request.Slug || request.DisplayName != "" && namespace.DisplayName != request.DisplayName {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "namespace.read", namespace.ID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		if namespace.TenantID == tenantID && (request.AfterSlug == "" || namespace.Slug > request.AfterSlug || namespace.Slug == request.AfterSlug && namespace.ID > request.AfterID) {
			items = append(items, clone(namespace))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Slug != items[j].Slug {
			return items[i].Slug < items[j].Slug
		}
		return items[i].ID < items[j].ID
	})
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return items, nil
}

// UpdateNamespace enforces a single revision increment while preserving
// immutable slug and creation time.
func (m *MemoryRepository) UpdateNamespace(ctx context.Context, params UpdateNamespaceParams) (Namespace, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Namespace
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.UpdateNamespace(ctx, params)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.namespaces[params.Namespace.ID]
	if !ok || current.TenantID != params.Namespace.TenantID {
		return Namespace{}, E(CodeNotFound, "namespace was not found")
	}
	if current.Revision != params.ExpectedRevision || params.Namespace.Revision != current.Revision+1 {
		return Namespace{}, E(CodePreconditionFailed, "namespace revision does not match")
	}
	params.Namespace.Slug = current.Slug
	params.Namespace.CreatedAt = current.CreatedAt
	m.namespaces[current.ID] = clone(params.Namespace)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(params.Namespace), nil
}

// CreatePublisher rejects domains already held by another publisher in the
// tenant before retaining evidence.
func (m *MemoryRepository) CreatePublisher(ctx context.Context, publisher Publisher, audit AuditEvent, outbox OutboxEvent) (Publisher, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Publisher
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.CreatePublisher(ctx, publisher, audit, outbox)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.publishers {
		if existing.TenantID != publisher.TenantID {
			continue
		}
		for _, domain := range publisher.Domains {
			if contains(existing.Domains, domain) {
				return Publisher{}, E(CodeConflict, "publisher domain already belongs to another publisher")
			}
		}
	}
	m.publishers[publisher.ID] = clone(publisher)
	m.appendEvidence(ctx, audit, outbox)
	return clone(publisher), nil
}

// ListPublishers returns all in-memory tenant publishers ordered by display
// name and ID; paginated callers use ListPublisherPage.
func (m *MemoryRepository) ListPublishers(ctx context.Context, tenantID string) ([]Publisher, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]Publisher, error) { return m.ListPublishers(ctx, tenantID) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Publisher, 0)
	for _, publisher := range m.publishers {
		if publisher.TenantID == tenantID {
			items = append(items, clone(publisher))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].DisplayName == items[j].DisplayName {
			return items[i].ID < items[j].ID
		}
		return items[i].DisplayName < items[j].DisplayName
	})
	return items, nil
}

// GetPublisher returns a copied publisher from the explicit tenant.
func (m *MemoryRepository) GetPublisher(ctx context.Context, tenantID, publisherID string) (Publisher, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (Publisher, error) { return m.GetPublisher(ctx, tenantID, publisherID) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	publisher, ok := m.publishers[publisherID]
	if !ok || publisher.TenantID != tenantID {
		return Publisher{}, E(CodeNotFound, "publisher was not found")
	}
	return clone(publisher), nil
}

// ListPublisherPage selects tenant publishers after the display-name/ID
// position and applies the requested limit.
func (m *MemoryRepository) ListPublisherPage(ctx context.Context, tenantID string, request PublisherListRequest) ([]Publisher, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]Publisher, error) { return m.ListPublisherPage(ctx, tenantID, request) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Publisher, 0)
	for _, publisher := range m.publishers {
		if publisher.TenantID == tenantID && (request.DisplayName == "" || publisher.DisplayName == request.DisplayName) && (request.DeclaredDomain == "" || slices.Contains(publisher.Domains, request.DeclaredDomain)) && (request.AfterDisplayName == "" || publisher.DisplayName > request.AfterDisplayName || publisher.DisplayName == request.AfterDisplayName && publisher.ID > request.AfterID) {
			items = append(items, clone(publisher))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].DisplayName != items[j].DisplayName {
			return items[i].DisplayName < items[j].DisplayName
		}
		return items[i].ID < items[j].ID
	})
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return items, nil
}

// UpdatePublisher preserves domain approvals and rejects removal of approved or
// actively published domains under the expected revision.
func (m *MemoryRepository) UpdatePublisher(ctx context.Context, params UpdatePublisherParams) (Publisher, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Publisher
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.UpdatePublisher(ctx, params)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.publishers[params.Publisher.ID]
	if !ok || current.TenantID != params.Publisher.TenantID {
		return Publisher{}, E(CodeNotFound, "publisher was not found")
	}
	if current.Revision != params.ExpectedRevision || params.Publisher.Revision != current.Revision+1 {
		return Publisher{}, E(CodePreconditionFailed, "publisher revision does not match")
	}
	for _, approved := range current.ApprovedPublicationDomains {
		if !contains(params.Publisher.Domains, approved) {
			return Publisher{}, E(CodeConflict, "an approved publication domain must be revoked before it is removed")
		}
	}
	for _, domain := range current.Domains {
		if contains(params.Publisher.Domains, domain) {
			continue
		}
		for _, listing := range m.listings {
			if listing.TenantID != current.TenantID || listing.CurrentReleaseID == "" {
				continue
			}
			release, exists := m.releases[listing.CurrentReleaseID]
			if !exists || release.State != ReleasePublished && release.State != ReleaseDeprecated {
				continue
			}
			revision, exists := m.listingRevisions[release.ListingRevisionID]
			resource, resourceExists := m.resources[listing.ResourceID]
			if !exists || !resourceExists || revision.PublisherID != current.ID {
				continue
			}
			identifier, parseErr := ParseAIRIdentifier(resource.CanonicalIdentifier)
			if parseErr == nil && identifier.Publisher == domain {
				return Publisher{}, E(CodeConflict, "a declared domain with an active publication cannot be removed")
			}
		}
	}
	for _, existing := range m.publishers {
		if existing.TenantID == current.TenantID && existing.ID != current.ID {
			for _, domain := range params.Publisher.Domains {
				if contains(existing.Domains, domain) {
					return Publisher{}, E(CodeConflict, "publisher domain already belongs to another publisher")
				}
			}
		}
	}
	params.Publisher.ApprovedPublicationDomains = append([]string{}, current.ApprovedPublicationDomains...)
	params.Publisher.CreatedAt = current.CreatedAt
	for key, verification := range m.publisherDomains {
		if verification.TenantID == current.TenantID && verification.PublisherID == current.ID && !contains(params.Publisher.Domains, verification.Domain) {
			delete(m.publisherDomains, key)
		}
	}
	m.publishers[current.ID] = clone(params.Publisher)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(params.Publisher), nil
}

// ApprovePublisherPublicationDomain adds approval only to a declared publisher
// domain under the expected revision.
func (m *MemoryRepository) ApprovePublisherPublicationDomain(ctx context.Context, params PublisherDomainApproval) (Publisher, error) {
	tenantID, publisherID, domain, expectedRevision, outbox := params.TenantID, params.PublisherID, params.Domain, params.ExpectedRevision, params.Outbox
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Publisher
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.ApprovePublisherPublicationDomain(ctx, params)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	publisher, ok := m.publishers[publisherID]
	if !ok || publisher.TenantID != tenantID {
		return Publisher{}, E(CodeNotFound, "publisher was not found")
	}
	if expectedRevision > 0 && publisher.Revision != expectedRevision {
		return Publisher{}, E(CodePreconditionFailed, "publisher revision does not match")
	}
	if !contains(publisher.Domains, domain) {
		return Publisher{}, E(CodeInvalidArgument, "domain is not declared by this publisher")
	}
	audit, err := PublisherApprovalAudit(params, m.publisherDomains[tenantID+"/"+publisherID+"/"+domain])
	if err != nil {
		return Publisher{}, err
	}
	if !contains(publisher.ApprovedPublicationDomains, domain) {
		publisher.ApprovedPublicationDomains = append(publisher.ApprovedPublicationDomains, domain)
		sort.Strings(publisher.ApprovedPublicationDomains)
		publisher.Revision++
		publisher.UpdatedAt = audit.At
	}
	m.publishers[publisherID] = clone(publisher)
	m.appendEvidence(ctx, audit, outbox)
	return clone(publisher), nil
}

// RevokePublisherPublicationDomain removes approval and returns affected
// release IDs while retaining the same transition's evidence.
func (m *MemoryRepository) RevokePublisherPublicationDomain(ctx context.Context, tenantID, publisherID, domain string, expectedRevision int64, audit AuditEvent, outbox OutboxEvent) (Publisher, []string, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Publisher
		var admitted1 []string
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, admitted1, operationErr = m.RevokePublisherPublicationDomain(ctx, tenantID, publisherID, domain, expectedRevision, audit, outbox)
			return operationErr
		})
		return admitted0, admitted1, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	publisher, ok := m.publishers[publisherID]
	if !ok || publisher.TenantID != tenantID {
		return Publisher{}, nil, E(CodeNotFound, "publisher was not found")
	}
	if expectedRevision > 0 && publisher.Revision != expectedRevision {
		return Publisher{}, nil, E(CodePreconditionFailed, "publisher revision does not match")
	}
	if !contains(publisher.ApprovedPublicationDomains, domain) {
		return Publisher{}, nil, E(CodeInvalidTransition, "publication domain is not approved")
	}
	affected := make([]string, 0)
	for _, listing := range m.listings {
		if listing.TenantID != tenantID || listing.CurrentReleaseID == "" {
			continue
		}
		release, exists := m.releases[listing.CurrentReleaseID]
		if !exists || release.State != ReleasePublished && release.State != ReleaseDeprecated {
			continue
		}
		revision, exists := m.listingRevisions[release.ListingRevisionID]
		if !exists || revision.PublisherID != publisherID {
			continue
		}
		resource, exists := m.resources[listing.ResourceID]
		if !exists {
			continue
		}
		identifier, err := ParseAIRIdentifier(resource.CanonicalIdentifier)
		if err == nil && identifier.Publisher == domain {
			affected = append(affected, release.ID)
		}
	}
	sort.Strings(affected)
	audit, err := CompletePublisherDomainRevocationAudit(audit, domain, affected)
	if err != nil {
		return Publisher{}, nil, err
	}
	approved := make([]string, 0, len(publisher.ApprovedPublicationDomains)-1)
	for _, candidate := range publisher.ApprovedPublicationDomains {
		if candidate != domain {
			approved = append(approved, candidate)
		}
	}
	publisher.ApprovedPublicationDomains = approved
	publisher.Revision++
	publisher.UpdatedAt = audit.At
	m.publishers[publisherID] = clone(publisher)
	m.appendEvidence(ctx, audit, outbox)
	return clone(publisher), affected, nil
}

// CreateResource enforces a same-tenant namespace and unique canonical resource
// identity before inserting evidence.
func (m *MemoryRepository) CreateResource(ctx context.Context, params CreateResourceParams) (Resource, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Resource
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.CreateResource(ctx, params)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if namespace, ok := m.namespaces[params.Resource.NamespaceID]; !ok || namespace.TenantID != params.Resource.TenantID {
		return Resource{}, E(CodeInvalidArgument, "resource namespace does not exist in the tenant")
	}
	for _, existing := range m.resources {
		if existing.TenantID == params.Resource.TenantID && existing.CanonicalIdentifier == params.Resource.CanonicalIdentifier {
			return Resource{}, E(CodeConflict, "canonical resource identifier already exists")
		}
	}
	m.resources[params.Resource.ID] = clone(params.Resource)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(params.Resource), nil
}

// GetResource hides absent, cross-tenant and soft-deleted resources behind the
// same not-found result.
func (m *MemoryRepository) GetResource(ctx context.Context, tenantID, idValue string) (Resource, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (Resource, error) { return m.GetResource(ctx, tenantID, idValue) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	resource, ok := m.resources[idValue]
	if !ok || resource.TenantID != tenantID || resource.DeletedAt != nil {
		return Resource{}, E(CodeNotFound, "resource was not found")
	}
	return clone(resource), nil
}

// GetResourceByIdentifier resolves an active tenant resource by its exact
// canonical identifier.
func (m *MemoryRepository) GetResourceByIdentifier(ctx context.Context, tenantID, identifier string) (Resource, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (Resource, error) {
			return m.GetResourceByIdentifier(ctx, tenantID, identifier)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, resource := range m.resources {
		if resource.TenantID == tenantID && resource.DeletedAt == nil && resource.CanonicalIdentifier == identifier {
			return clone(resource), nil
		}
	}
	return Resource{}, E(CodeNotFound, "resource was not found")
}

// ListResources filters active tenant resources and orders the bounded page by
// descending update time and ID.
func (m *MemoryRepository) ListResources(ctx context.Context, tenantID string, request ResourceListRequest) ([]Resource, error) {
	if err := identity.RequireCollectionTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]Resource, error) { return m.ListResources(ctx, tenantID, request) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Resource, 0)
	for _, resource := range m.resources {
		if resource.TenantID != tenantID {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "resource.read", resource.NamespaceID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		beforeCursor := request.AfterUpdatedAt == nil || resource.UpdatedAt.Before(*request.AfterUpdatedAt) || resource.UpdatedAt.Equal(*request.AfterUpdatedAt) && resource.ID < request.AfterID
		if resource.TenantID == tenantID && resource.DeletedAt == nil && (request.Kind == "" || resource.Kind == request.Kind) && (request.CanonicalIdentifier == "" || resource.CanonicalIdentifier == request.CanonicalIdentifier) && beforeCursor {
			items = append(items, clone(resource))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return items, nil
}

// DeleteResource soft-deletes under the expected revision and rejects resources
// with adoption, review or release history.
func (m *MemoryRepository) DeleteResource(ctx context.Context, params DeleteResourceParams) (Resource, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Resource
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.DeleteResource(ctx, params)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	resource, ok := m.resources[params.ResourceID]
	if !ok || resource.TenantID != params.TenantID || resource.DeletedAt != nil {
		return Resource{}, E(CodeNotFound, "resource was not found")
	}
	if resource.Revision != params.ExpectedRevision {
		return Resource{}, E(CodePreconditionFailed, "resource revision does not match")
	}
	for _, version := range m.versions {
		if version.TenantID != params.TenantID || version.ResourceID != resource.ID {
			continue
		}
		for _, revision := range m.versionRevisions {
			if revision.ResourceVersionID == version.ID && revision.Adoption != nil {
				return Resource{}, E(CodeConflict, "an adopted resource retains provenance and cannot be deleted")
			}
		}
	}
	for _, listing := range m.listings {
		if listing.TenantID != params.TenantID || listing.ResourceID != resource.ID {
			continue
		}
		for _, submission := range m.submissions {
			if submission.ListingID == listing.ID {
				return Resource{}, E(CodeConflict, "a reviewed resource cannot be deleted")
			}
		}
		for _, release := range m.releases {
			if release.ListingID == listing.ID {
				return Resource{}, E(CodeConflict, "a resource with release history cannot be deleted")
			}
		}
	}
	resource.Revision++
	resource.UpdatedAt = params.At
	resource.DeletedBy = params.Actor
	resource.DeleteReason = params.Reason
	resource.DeletedAt = &params.At
	m.resources[resource.ID] = clone(resource)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(resource), nil
}

// CreateVersion validates same-tenant parentage and retained representation
// evidence before committing the version, initial revision and documents.
func (m *MemoryRepository) CreateVersion(ctx context.Context, params CreateVersionParams) (ResourceVersion, VersionRevision, EvidenceSet, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 ResourceVersion
		var admitted1 VersionRevision
		var admitted2 EvidenceSet
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, admitted1, admitted2, operationErr = m.CreateVersion(ctx, params)
			return operationErr
		})
		return admitted0, admitted1, admitted2, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	resource, ok := m.resources[params.Version.ResourceID]
	if !ok || resource.TenantID != params.Version.TenantID || resource.DeletedAt != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, E(CodeNotFound, "resource was not found")
	}
	for _, existing := range m.versions {
		if existing.TenantID == params.Version.TenantID && existing.ResourceID == params.Version.ResourceID && existing.Version == params.Version.Version {
			return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, E(CodeConflict, "resource version already exists")
		}
	}
	documents, err := m.validatedDocuments(params.Version.TenantID, params.VersionRevision, params.Documents)
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	if err := ValidateExternalRetrievalEvidence(params.VersionRevision, params.Evidence); err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	for key, document := range documents {
		m.documents[key] = cloneDocument(document)
	}
	m.versions[params.Version.ID] = clone(params.Version)
	m.versionRevisions[params.VersionRevision.ID] = clone(params.VersionRevision)
	m.evidenceSets[params.Evidence.ID] = clone(params.Evidence)
	m.validationRuns[params.VersionRevision.ID] = append(m.validationRuns[params.VersionRevision.ID], params.Evidence.ID)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(params.Version), clone(params.VersionRevision), clone(params.Evidence), nil
}

// CreateVersionRevision appends distinct content only to an unsealed version at
// the expected revision, committing documents and evidence together.
func (m *MemoryRepository) CreateVersionRevision(ctx context.Context, params CreateVersionParams) (ResourceVersion, VersionRevision, EvidenceSet, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 ResourceVersion
		var admitted1 VersionRevision
		var admitted2 EvidenceSet
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, admitted1, admitted2, operationErr = m.CreateVersionRevision(ctx, params)
			return operationErr
		})
		return admitted0, admitted1, admitted2, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.versions[params.Version.ID]
	if !ok || current.TenantID != params.Version.TenantID {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, E(CodeNotFound, "resource version was not found")
	}
	if resource, exists := m.resources[current.ResourceID]; !exists || resource.DeletedAt != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, E(CodeNotFound, "resource version was not found")
	}
	if current.SealedRevisionID != "" {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, E(CodeVersionSealed, "the version label is sealed")
	}
	if current.Revision != params.ExpectedRevision {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, E(CodePreconditionFailed, "resource version revision does not match")
	}
	if current.Revision+1 != params.Version.Revision {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, E(CodePreconditionFailed, "resource version revision does not match")
	}
	for _, revision := range m.versionRevisions {
		if revision.TenantID == params.Version.TenantID && revision.ResourceVersionID == params.Version.ID && revision.ContentSetDigest == params.VersionRevision.ContentSetDigest {
			return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, E(CodeConflict, "an identical version revision already exists")
		}
	}
	documents, err := m.validatedDocuments(params.Version.TenantID, params.VersionRevision, params.Documents)
	if err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	if err := ValidateExternalRetrievalEvidence(params.VersionRevision, params.Evidence); err != nil {
		return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, err
	}
	for key, document := range documents {
		m.documents[key] = cloneDocument(document)
	}
	m.versions[params.Version.ID] = clone(params.Version)
	m.versionRevisions[params.VersionRevision.ID] = clone(params.VersionRevision)
	m.evidenceSets[params.Evidence.ID] = clone(params.Evidence)
	m.validationRuns[params.VersionRevision.ID] = append(m.validationRuns[params.VersionRevision.ID], params.Evidence.ID)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(params.Version), clone(params.VersionRevision), clone(params.Evidence), nil
}

// GetVersion returns a copied tenant version only while its parent resource
// remains active.
func (m *MemoryRepository) GetVersion(ctx context.Context, tenantID, idValue string) (ResourceVersion, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (ResourceVersion, error) { return m.GetVersion(ctx, tenantID, idValue) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	version, ok := m.versions[idValue]
	resource, resourceExists := m.resources[version.ResourceID]
	if !ok || version.TenantID != tenantID || !resourceExists || resource.DeletedAt != nil {
		return ResourceVersion{}, E(CodeNotFound, "resource version was not found")
	}
	return clone(version), nil
}

// GetVersionByLabel resolves the exact tenant/resource/version-label tuple
// under an active resource.
func (m *MemoryRepository) GetVersionByLabel(ctx context.Context, tenantID, resourceID, versionLabel string) (ResourceVersion, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (ResourceVersion, error) {
			return m.GetVersionByLabel(ctx, tenantID, resourceID, versionLabel)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, version := range m.versions {
		resource, resourceExists := m.resources[version.ResourceID]
		if version.TenantID == tenantID && version.ResourceID == resourceID && version.Version == versionLabel && resourceExists && resource.DeletedAt == nil {
			return clone(version), nil
		}
	}
	return ResourceVersion{}, E(CodeNotFound, "resource version was not found")
}

// GetVersionRevision returns retained revision state with its selected
// validation evidence and verifies active tenant parentage.
func (m *MemoryRepository) GetVersionRevision(ctx context.Context, tenantID, idValue string) (VersionRevision, EvidenceSet, error) {
	if identity.NeedsReadAdmission(ctx) {
		type selected struct {
			revision VersionRevision
			evidence EvidenceSet
		}
		value, err := identity.WithReadAdmission(ctx, func(ctx context.Context) (selected, error) {
			revision, evidence, err := m.GetVersionRevision(ctx, tenantID, idValue)
			return selected{revision, evidence}, err
		})
		return value.revision, value.evidence, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	revision, ok := m.versionRevisions[idValue]
	if !ok || revision.TenantID != tenantID {
		return VersionRevision{}, EvidenceSet{}, E(CodeNotFound, "version revision was not found")
	}
	if version, exists := m.versions[revision.ResourceVersionID]; exists {
		if resource, resourceExists := m.resources[version.ResourceID]; resourceExists && resource.DeletedAt != nil {
			return VersionRevision{}, EvidenceSet{}, E(CodeNotFound, "version revision was not found")
		}
	}
	evidenceID := revision.EvidenceSetID
	if runs := m.validationRuns[revision.ID]; len(runs) > 0 {
		evidenceID = runs[len(runs)-1]
	}
	evidence, ok := m.evidenceSets[evidenceID]
	if !ok || evidence.TenantID != tenantID {
		return VersionRevision{}, EvidenceSet{}, E(CodeInternal, "version revision evidence is missing")
	}
	revision.EvidenceSetID = evidence.ID
	return clone(revision), clone(evidence), nil
}

// GetValidationEvidence resolves an exact evidence run only when it belongs to
// the requested tenant revision.
func (m *MemoryRepository) GetValidationEvidence(ctx context.Context, tenantID, versionRevisionID, evidenceID string) (EvidenceSet, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (EvidenceSet, error) {
			return m.GetValidationEvidence(ctx, tenantID, versionRevisionID, evidenceID)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	revision, ok := m.versionRevisions[versionRevisionID]
	version, versionExists := m.versions[revision.ResourceVersionID]
	resource, resourceExists := m.resources[version.ResourceID]
	if !ok || revision.TenantID != tenantID || !versionExists || !resourceExists || resource.DeletedAt != nil {
		return EvidenceSet{}, E(CodeNotFound, "version validation evidence was not found")
	}
	associated := revision.EvidenceSetID == evidenceID
	for _, candidate := range m.validationRuns[versionRevisionID] {
		associated = associated || candidate == evidenceID
	}
	evidence, ok := m.evidenceSets[evidenceID]
	if !associated || !ok || evidence.TenantID != tenantID {
		return EvidenceSet{}, E(CodeNotFound, "version validation evidence was not found")
	}
	return clone(evidence), nil
}

// CreateValidationRun appends evidence for a retained revision without
// rewriting its representations.
func (m *MemoryRepository) CreateValidationRun(ctx context.Context, params CreateValidationRunParams) (EvidenceSet, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 EvidenceSet
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.CreateValidationRun(ctx, params)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	revision, ok := m.versionRevisions[params.VersionRevisionID]
	version, versionExists := m.versions[revision.ResourceVersionID]
	resource, resourceExists := m.resources[version.ResourceID]
	if !ok || revision.TenantID != params.TenantID || !versionExists || !resourceExists || resource.DeletedAt != nil {
		return EvidenceSet{}, E(CodeNotFound, "version revision was not found")
	}
	if params.Evidence.TenantID != params.TenantID || params.Evidence.SubjectDigest != revision.ContentSetDigest || params.Evidence.ID == "" || params.Evidence.Digest == "" {
		return EvidenceSet{}, E(CodeInvalidArgument, "version validation evidence does not match its revision")
	}
	if err := ValidateExternalRetrievalEvidence(revision, params.Evidence); err != nil {
		return EvidenceSet{}, err
	}
	if _, exists := m.evidenceSets[params.Evidence.ID]; exists {
		return EvidenceSet{}, E(CodeConflict, "version validation evidence already exists")
	}
	for _, evidence := range m.evidenceSets {
		if evidence.TenantID == params.TenantID && evidence.Digest == params.Evidence.Digest {
			return EvidenceSet{}, E(CodeConflict, "version validation evidence digest already exists")
		}
	}
	m.evidenceSets[params.Evidence.ID] = clone(params.Evidence)
	m.validationRuns[params.VersionRevisionID] = append(m.validationRuns[params.VersionRevisionID], params.Evidence.ID)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(params.Evidence), nil
}

// GetDocument returns retained bytes only while an active tenant resource
// references them; inactive-only references return ErrInactiveDocument.
func (m *MemoryRepository) GetDocument(ctx context.Context, tenantID, idValue string) (DocumentBlob, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (DocumentBlob, error) { return m.GetDocument(ctx, tenantID, idValue) })
	}
	if err := ctx.Err(); err != nil {
		return DocumentBlob{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	document, ok := m.documents[tenantID+"/"+idValue]
	active := false
	referenced := false
	for _, revision := range m.versionRevisions {
		for _, representation := range revision.Representations {
			if representation.ArtifactID != idValue {
				continue
			}
			version, versionExists := m.versions[revision.ResourceVersionID]
			resource, resourceExists := m.resources[version.ResourceID]
			if versionExists && resourceExists && resource.TenantID == tenantID {
				referenced = true
				if resource.DeletedAt == nil {
					active = true
					break
				}
			}
		}
		if active {
			break
		}
	}
	if !ok {
		if referenced && !active {
			return DocumentBlob{}, Wrap(CodeNotFound, "catalog document was not found", ErrInactiveDocument)
		}
		return DocumentBlob{}, E(CodeNotFound, "catalog document was not found")
	}
	if !active {
		return DocumentBlob{}, Wrap(CodeNotFound, "catalog document was not found", ErrInactiveDocument)
	}
	return cloneDocument(document), nil
}

// validatedDocuments checks exact tenant document bindings before any revision
// write becomes visible.
func (m *MemoryRepository) validatedDocuments(tenantID string, revision VersionRevision, documents []DocumentBlob) (map[string]DocumentBlob, error) {
	for _, representation := range revision.Representations {
		if err := ValidatePackageRepresentation(representation); err != nil {
			return nil, err
		}
	}
	if err := ValidatePackageRevisionDigest(revision); err != nil {
		return nil, err
	}
	prepared := make(map[string]DocumentBlob, len(documents))
	var documentBytes int64
	for _, document := range documents {
		if err := ValidateDocumentBlob(tenantID, document); err != nil {
			return nil, err
		}
		if int64(len(document.Content)) > MaxCatalogDocumentBytes-documentBytes {
			return nil, E(CodeInvalidArgument, "bounded representation documents exceed the 16 MiB revision limit")
		}
		documentBytes += int64(len(document.Content))
		key := tenantID + "/" + document.ID
		if existing, ok := prepared[key]; ok {
			if !sameDocument(existing, document) {
				return nil, E(CodeConflict, "catalog document bytes already use another media type")
			}
			continue
		}
		if existing, ok := m.documents[key]; ok {
			if !sameDocument(existing, document) {
				return nil, E(CodeConflict, "catalog document bytes already use another media type")
			}
			prepared[key] = existing
			continue
		}
		prepared[key] = document
	}
	referenced := make(map[string]bool, len(prepared))
	for _, representation := range revision.Representations {
		if representation.ArtifactID == "" {
			continue
		}
		key := tenantID + "/" + representation.ArtifactID
		document, ok := prepared[key]
		if !ok {
			return nil, E(CodeInvalidArgument, "catalog document is missing for its representation")
		}
		if representation.ContentDigest != document.Digest || representation.MediaType != document.MediaType || representation.Size != document.Size {
			return nil, E(CodeInvalidArgument, "catalog document metadata does not match its representation")
		}
		referenced[key] = true
	}
	for key := range prepared {
		if !referenced[key] {
			return nil, E(CodeInvalidArgument, "catalog document is not referenced by the version revision")
		}
	}
	return prepared, nil
}

// ValidateDocumentBlob enforces the domain identity and storage bounds shared
// by repository implementations.
func ValidateDocumentBlob(tenantID string, document DocumentBlob) error {
	digest := digestBytes(document.Content)
	if document.TenantID != tenantID || document.ID != strings.TrimPrefix(digest, "sha256:") || document.Digest != digest || document.Size != int64(len(document.Content)) || document.Size <= 0 || document.Size > MaxCatalogDocumentBytes || !utf8.Valid(document.Content) || bytes.IndexByte(document.Content, 0) >= 0 || !validRepresentationMediaType(document.MediaType) || document.CreatedAt.IsZero() {
		return E(CodeInvalidArgument, "catalog document metadata or content is invalid")
	}
	return nil
}

// sameDocument compares immutable document metadata and exact content bytes.
func sameDocument(left, right DocumentBlob) bool {
	return left.ID == right.ID && left.TenantID == right.TenantID && left.Digest == right.Digest && left.MediaType == right.MediaType && left.Size == right.Size && bytes.Equal(left.Content, right.Content)
}

// cloneDocument copies retained bytes so callers cannot mutate repository
// document storage.
func cloneDocument(document DocumentBlob) DocumentBlob {
	document.Content = append([]byte(nil), document.Content...)
	return document
}

// CreateListing checks same-tenant resource and namespace ownership before
// inserting the listing with evidence.
func (m *MemoryRepository) CreateListing(ctx context.Context, params CreateListingParams) (CatalogListing, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 CatalogListing
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.CreateListing(ctx, params)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	resource, ok := m.resources[params.Listing.ResourceID]
	if !ok || resource.TenantID != params.Listing.TenantID || resource.DeletedAt != nil {
		return CatalogListing{}, E(CodeNotFound, "resource was not found")
	}
	if resource.NamespaceID != params.Listing.NamespaceID {
		return CatalogListing{}, E(CodeInvalidArgument, "a listing must remain in its resource namespace")
	}
	if namespace, exists := m.namespaces[params.Listing.NamespaceID]; !exists || namespace.TenantID != params.Listing.TenantID {
		return CatalogListing{}, E(CodeInvalidArgument, "listing namespace does not exist in the tenant")
	}
	for _, listing := range m.listings {
		if listing.TenantID == params.Listing.TenantID && listing.NamespaceID == params.Listing.NamespaceID && listing.ResourceID == params.Listing.ResourceID {
			return CatalogListing{}, E(CodeConflict, "a listing already exists for this resource and namespace")
		}
	}
	m.listings[params.Listing.ID] = clone(params.Listing)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(params.Listing), nil
}

// GetListing returns a tenant listing only while its parent resource remains
// active.
func (m *MemoryRepository) GetListing(ctx context.Context, tenantID, idValue string) (CatalogListing, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (CatalogListing, error) { return m.GetListing(ctx, tenantID, idValue) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	listing, ok := m.listings[idValue]
	resource, resourceExists := m.resources[listing.ResourceID]
	if !ok || listing.TenantID != tenantID || !resourceExists || resource.DeletedAt != nil {
		return CatalogListing{}, E(CodeNotFound, "catalog listing was not found")
	}
	return clone(listing), nil
}

// CreateListingRevision appends a revision under the listing's expected current
// revision and preserves prior immutable revisions.
func (m *MemoryRepository) CreateListingRevision(ctx context.Context, params CreateListingRevisionParams) (CatalogListing, ListingRevision, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 CatalogListing
		var admitted1 ListingRevision
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, admitted1, operationErr = m.CreateListingRevision(ctx, params)
			return operationErr
		})
		return admitted0, admitted1, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	listing, ok := m.listings[params.Revision.ListingID]
	resource, resourceExists := m.resources[listing.ResourceID]
	if !ok || listing.TenantID != params.Revision.TenantID || !resourceExists || resource.DeletedAt != nil {
		return CatalogListing{}, ListingRevision{}, E(CodeNotFound, "catalog listing was not found")
	}
	if listing.Revision != params.ExpectedRevision {
		return CatalogListing{}, ListingRevision{}, E(CodePreconditionFailed, "catalog listing revision does not match")
	}
	if _, ok := m.versionRevisions[params.Revision.VersionRevisionID]; !ok {
		return CatalogListing{}, ListingRevision{}, E(CodeNotFound, "version revision was not found")
	}
	for _, revision := range m.listingRevisions {
		if revision.TenantID == params.Revision.TenantID && revision.ListingID == params.Revision.ListingID && revision.Digest == params.Revision.Digest {
			return CatalogListing{}, ListingRevision{}, E(CodeConflict, "an identical listing revision already exists")
		}
	}
	listing.CandidateRevisionID = params.Revision.ID
	listing.Revision++
	listing.UpdatedAt = params.Revision.CreatedAt
	m.listings[listing.ID] = clone(listing)
	m.listingRevisions[params.Revision.ID] = clone(params.Revision)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(listing), clone(params.Revision), nil
}

// AdoptExternalObservation commits the complete local draft and provenance
// together after validating retained document and parent bindings.
func (m *MemoryRepository) AdoptExternalObservation(ctx context.Context, params AdoptExternalObservationParams) (ExternalObservationAdoption, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 ExternalObservationAdoption
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.AdoptExternalObservation(ctx, params)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if namespace, ok := m.namespaces[params.Resource.NamespaceID]; !ok || namespace.TenantID != params.Resource.TenantID {
		return ExternalObservationAdoption{}, E(CodeInvalidArgument, "adoption namespace does not exist in the tenant")
	}
	if publisher, ok := m.publishers[params.ListingRevision.PublisherID]; !ok || publisher.TenantID != params.Resource.TenantID {
		return ExternalObservationAdoption{}, E(CodeInvalidArgument, "publisher does not exist in the tenant")
	}
	for _, existing := range m.resources {
		if existing.TenantID == params.Resource.TenantID && existing.CanonicalIdentifier == params.Resource.CanonicalIdentifier {
			return ExternalObservationAdoption{}, E(CodeConflict, "canonical resource identifier already exists")
		}
	}
	if params.Version.TenantID != params.Resource.TenantID || params.Version.ResourceID != params.Resource.ID || params.VersionRevision.TenantID != params.Resource.TenantID || params.VersionRevision.ResourceVersionID != params.Version.ID || params.Version.CandidateRevisionID != params.VersionRevision.ID || params.VersionRevision.Adoption == nil {
		return ExternalObservationAdoption{}, E(CodeInvalidArgument, "adopted version does not belong to its resource or provenance")
	}
	if params.Listing.TenantID != params.Resource.TenantID || params.Listing.NamespaceID != params.Resource.NamespaceID || params.Listing.ResourceID != params.Resource.ID || params.Listing.CandidateRevisionID != params.ListingRevision.ID || params.ListingRevision.TenantID != params.Resource.TenantID || params.ListingRevision.ListingID != params.Listing.ID || params.ListingRevision.VersionRevisionID != params.VersionRevision.ID {
		return ExternalObservationAdoption{}, E(CodeInvalidArgument, "adopted listing does not belong to its resource revision")
	}
	documents, err := m.validatedDocuments(params.Resource.TenantID, params.VersionRevision, params.Documents)
	if err != nil {
		return ExternalObservationAdoption{}, err
	}
	if err := ValidateExternalRetrievalEvidence(params.VersionRevision, params.Evidence); err != nil {
		return ExternalObservationAdoption{}, err
	}
	for key, document := range documents {
		m.documents[key] = cloneDocument(document)
	}
	m.resources[params.Resource.ID] = clone(params.Resource)
	m.versions[params.Version.ID] = clone(params.Version)
	m.versionRevisions[params.VersionRevision.ID] = clone(params.VersionRevision)
	m.evidenceSets[params.Evidence.ID] = clone(params.Evidence)
	m.validationRuns[params.VersionRevision.ID] = append(m.validationRuns[params.VersionRevision.ID], params.Evidence.ID)
	m.listings[params.Listing.ID] = clone(params.Listing)
	m.listingRevisions[params.ListingRevision.ID] = clone(params.ListingRevision)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return ExternalObservationAdoption{Resource: clone(params.Resource), Version: clone(params.Version), VersionRevision: clone(params.VersionRevision), Evidence: clone(params.Evidence), Listing: clone(params.Listing), ListingRevision: clone(params.ListingRevision)}, nil
}

// GetListingRevision resolves retained listing metadata through its active
// tenant-owned parent.
func (m *MemoryRepository) GetListingRevision(ctx context.Context, tenantID, idValue string) (ListingRevision, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (ListingRevision, error) {
			return m.GetListingRevision(ctx, tenantID, idValue)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	revision, ok := m.listingRevisions[idValue]
	listing, listingExists := m.listings[revision.ListingID]
	resource, resourceExists := m.resources[listing.ResourceID]
	if !ok || revision.TenantID != tenantID || !listingExists || !resourceExists || resource.DeletedAt != nil {
		return ListingRevision{}, E(CodeNotFound, "listing revision was not found")
	}
	return clone(revision), nil
}

// Submit binds current version, listing and evidence digests to a new review
// submission under repository preconditions.
func (m *MemoryRepository) Submit(ctx context.Context, params SubmitParams) (ReviewSubmission, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 ReviewSubmission
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.Submit(ctx, params)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	listing, ok := m.listings[params.Submission.ListingID]
	resource, resourceExists := m.resources[listing.ResourceID]
	if !ok || listing.TenantID != params.Submission.TenantID || !resourceExists || resource.DeletedAt != nil {
		return ReviewSubmission{}, E(CodeNotFound, "catalog listing was not found")
	}
	if listing.CandidateRevisionID != params.Submission.ListingRevisionID {
		return ReviewSubmission{}, E(CodePreconditionFailed, "listing candidate changed before submission")
	}
	for _, submission := range m.submissions {
		if submission.TenantID == params.Submission.TenantID && submission.ListingID == params.Submission.ListingID && submission.State == SubmissionSubmitted {
			return ReviewSubmission{}, E(CodeConflict, "the listing already has an active review submission")
		}
	}
	revision, ok := m.versionRevisions[params.Submission.VersionRevisionID]
	if !ok || revision.TenantID != params.Submission.TenantID {
		return ReviewSubmission{}, E(CodeNotFound, "version revision was not found")
	}
	if currentValidationEvidenceID(revision, m.validationRuns[revision.ID]) != params.Submission.EvidenceSetID {
		return ReviewSubmission{}, E(CodePreconditionFailed, "validation evidence changed before submission")
	}
	m.submissions[params.Submission.ID] = clone(params.Submission)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(params.Submission), nil
}

// GetSubmission returns retained review state only within the explicit tenant.
func (m *MemoryRepository) GetSubmission(ctx context.Context, tenantID, idValue string) (ReviewSubmission, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (ReviewSubmission, error) { return m.GetSubmission(ctx, tenantID, idValue) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	submission, ok := m.submissions[idValue]
	listing, listingExists := m.listings[submission.ListingID]
	resource, resourceExists := m.resources[listing.ResourceID]
	if !ok || submission.TenantID != tenantID || !listingExists || !resourceExists || resource.DeletedAt != nil {
		return ReviewSubmission{}, E(CodeNotFound, "review submission was not found")
	}
	return clone(submission), nil
}

// DecideSubmission enforces expected submission state and seals the approved
// version revision while retaining decision evidence.
func (m *MemoryRepository) DecideSubmission(ctx context.Context, params DecideSubmissionParams) (ReviewSubmission, ResourceVersion, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 ReviewSubmission
		var admitted1 ResourceVersion
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, admitted1, operationErr = m.DecideSubmission(ctx, params)
			return operationErr
		})
		return admitted0, admitted1, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	submission, ok := m.submissions[params.SubmissionID]
	if !ok || submission.TenantID != params.TenantID {
		return ReviewSubmission{}, ResourceVersion{}, E(CodeNotFound, "review submission was not found")
	}
	if params.ExpectedRevision > 0 && submission.Revision != params.ExpectedRevision {
		return ReviewSubmission{}, ResourceVersion{}, E(CodePreconditionFailed, "review submission revision does not match")
	}
	if submission.State != SubmissionSubmitted {
		return ReviewSubmission{}, ResourceVersion{}, E(CodeInvalidTransition, "review submission is already terminal")
	}
	origin, err := m.originatingPrincipalLocked(params.TenantID, "review_submission", params.SubmissionID)
	if err != nil {
		return ReviewSubmission{}, ResourceVersion{}, err
	}
	if !identity.ValidTenantID(params.ActorMembershipID) || !identity.ValidTenantID(params.ActorRealmID) {
		return ReviewSubmission{}, ResourceVersion{}, E(CodeForbidden, "reviewer principal cannot be established")
	}
	if origin.SamePrincipal(identity.Actor{TenantID: params.TenantID, MembershipID: params.ActorMembershipID, RealmID: params.ActorRealmID, Subject: params.Actor}) {
		return ReviewSubmission{}, ResourceVersion{}, E(CodeSeparationOfDuties, "the submitter cannot decide this submission")
	}
	revision, ok := m.versionRevisions[submission.VersionRevisionID]
	if !ok {
		return ReviewSubmission{}, ResourceVersion{}, E(CodeInternal, "submitted version revision is missing")
	}
	version, ok := m.versions[revision.ResourceVersionID]
	if !ok {
		return ReviewSubmission{}, ResourceVersion{}, E(CodeInternal, "submitted resource version is missing")
	}
	switch params.Action {
	case "approve":
		if params.ExpectedValidationEvidenceID == "" || currentValidationEvidenceID(revision, m.validationRuns[revision.ID]) != params.ExpectedValidationEvidenceID {
			return ReviewSubmission{}, ResourceVersion{}, E(CodePreconditionFailed, "review submission validation evidence is no longer current")
		}
		if version.SealedRevisionID != "" && version.SealedRevisionID != revision.ID {
			return ReviewSubmission{}, ResourceVersion{}, E(CodeConflict, "version is sealed to different content")
		}
		// A later listing review can reuse the same sealed content. Its new
		// decision must not mutate that immutable resource version again.
		if version.SealedRevisionID == "" {
			version.SealedRevisionID = revision.ID
			version.CandidateRevisionID = revision.ID
			version.Revision++
			version.UpdatedAt = params.At
		}
		submission.State = SubmissionApproved
	case "request_changes":
		submission.State = SubmissionChangesRequired
	case "reject":
		submission.State = SubmissionRejected
	default:
		return ReviewSubmission{}, ResourceVersion{}, E(CodeInvalidArgument, "review action is invalid")
	}
	submission.Decisions = append(submission.Decisions, ReviewDecision{Actor: params.Actor, Action: params.Action, Reason: params.Reason, At: params.At})
	submission.DecidedAt = &params.At
	submission.Revision++
	m.submissions[submission.ID] = clone(submission)
	m.versions[version.ID] = clone(version)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(submission), clone(version), nil
}

// Publish activates the reviewed release and supersedes the prior current
// release in the same evidence-bearing mutation.
func (m *MemoryRepository) Publish(ctx context.Context, params PublishParams) (CatalogListing, ListingRelease, *ListingRelease, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 CatalogListing
		var admitted1 ListingRelease
		var admitted2 *ListingRelease
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, admitted1, admitted2, operationErr = m.Publish(ctx, params)
			return operationErr
		})
		return admitted0, admitted1, admitted2, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	listing, ok := m.listings[params.ListingID]
	if !ok || listing.TenantID != params.TenantID {
		return CatalogListing{}, ListingRelease{}, nil, E(CodeNotFound, "catalog listing was not found")
	}
	if params.ExpectedRevision > 0 && listing.Revision != params.ExpectedRevision {
		return CatalogListing{}, ListingRelease{}, nil, E(CodePreconditionFailed, "catalog listing revision does not match")
	}
	submission, ok := m.submissions[params.SubmissionID]
	if !ok || submission.TenantID != params.TenantID || submission.ListingID != listing.ID || submission.State != SubmissionApproved {
		return CatalogListing{}, ListingRelease{}, nil, E(CodePreconditionFailed, "approved submission does not belong to this listing")
	}
	revision, ok := m.versionRevisions[submission.VersionRevisionID]
	if !ok {
		return CatalogListing{}, ListingRelease{}, nil, E(CodeInternal, "submitted version revision is missing")
	}
	if params.ExpectedValidationEvidenceID == "" || currentValidationEvidenceID(revision, m.validationRuns[revision.ID]) != params.ExpectedValidationEvidenceID {
		return CatalogListing{}, ListingRelease{}, nil, E(CodePublicationNotReady, "validation evidence changed before publication")
	}
	var prior *ListingRelease
	if listing.CurrentReleaseID != "" {
		current, exists := m.releases[listing.CurrentReleaseID]
		if !exists {
			return CatalogListing{}, ListingRelease{}, nil, E(CodeInternal, "current release pointer is invalid")
		}
		current.State = params.PriorDisposition
		current.DispositionReason = params.PriorReason
		current.UpdatedAt = params.Release.ActivatedAt
		current.Revision++
		if params.PriorDisposition == ReleaseSuperseded {
			current.ReplacementReleaseID = params.Release.ID
		}
		m.releases[current.ID] = clone(current)
		copyValue := clone(current)
		prior = &copyValue
	}
	m.releases[params.Release.ID] = clone(params.Release)
	listing.CurrentReleaseID = params.Release.ID
	listing.Revision++
	listing.UpdatedAt = params.Release.ActivatedAt
	m.listings[listing.ID] = clone(listing)
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(listing), clone(params.Release), prior, nil
}

// currentValidationEvidenceID selects the newest retained run, falling back to
// the revision's initial evidence binding.
func currentValidationEvidenceID(revision VersionRevision, runs []string) string {
	if len(runs) > 0 {
		return runs[len(runs)-1]
	}
	return revision.EvidenceSetID
}

// GetRelease returns a copied release only when its tenant matches.
func (m *MemoryRepository) GetRelease(ctx context.Context, tenantID, idValue string) (ListingRelease, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (ListingRelease, error) { return m.GetRelease(ctx, tenantID, idValue) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	release, ok := m.releases[idValue]
	if !ok || release.TenantID != tenantID {
		return ListingRelease{}, E(CodeNotFound, "listing release was not found")
	}
	return clone(release), nil
}

// TransitionRelease enforces the legal release-state transition and optimistic
// revision before retaining evidence.
func (m *MemoryRepository) TransitionRelease(ctx context.Context, params TransitionReleaseParams) (ListingRelease, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 ListingRelease
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.TransitionRelease(ctx, params)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	release, ok := m.releases[params.ReleaseID]
	if !ok || release.TenantID != params.TenantID {
		return ListingRelease{}, E(CodeNotFound, "listing release was not found")
	}
	if params.ExpectedRevision > 0 && release.Revision != params.ExpectedRevision {
		return ListingRelease{}, E(CodePreconditionFailed, "listing release revision does not match")
	}
	if !validReleaseTransition(release.State, params.TargetState) {
		return ListingRelease{}, E(CodeInvalidTransition, "release transition is not permitted")
	}
	release.State = params.TargetState
	release.DispositionReason = params.Reason
	release.ReplacementReleaseID = params.ReplacementID
	release.SunsetAt = params.SunsetAt
	release.UpdatedAt = params.At
	release.Revision++
	m.releases[release.ID] = clone(release)
	listing := m.listings[release.ListingID]
	if listing.CurrentReleaseID == release.ID && params.TargetState != ReleaseDeprecated {
		listing.CurrentReleaseID = ""
		listing.Revision++
		listing.UpdatedAt = params.At
		m.listings[listing.ID] = clone(listing)
	}
	m.appendEvidence(ctx, params.Audit, params.Outbox)
	return clone(release), nil
}

// validReleaseTransition reports whether a release-state edge belongs to the
// maintained lifecycle.
func validReleaseTransition(from, to ReleaseState) bool {
	switch from {
	case ReleasePublished:
		return to == ReleaseDeprecated || to == ReleaseRetired || to == ReleaseSuperseded || to == ReleaseRevoked
	case ReleaseDeprecated:
		return to == ReleaseRetired || to == ReleaseSuperseded || to == ReleaseRevoked
	case ReleaseRetired, ReleaseSuperseded:
		return to == ReleaseRevoked
	case ReleaseRevoked:
		return false
	default:
		return false
	}
}

// PublicationRecords selects eligible approved-domain releases under count and
// serialized-byte ceilings, reporting overflow without a partial publication.
func (m *MemoryRepository) PublicationRecords(ctx context.Context, tenantID string, request PublicationRecordRequest) (PublicationRecordPage, error) {
	if request.Limit < 1 || request.Limit > MaxPublicationRecords {
		return PublicationRecordPage{}, E(CodeInvalidArgument, "publication record limit must be positive")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]CatalogRecord, 0, request.Limit+1)
	projectionBytes := 0
	for _, listing := range m.listings {
		if err := ctx.Err(); err != nil {
			return PublicationRecordPage{}, err
		}
		record, ok := m.catalogRecordLocked(tenantID, listing, !request.IncludeTenant)
		if !ok || request.IncludeTenant && record.ListingRevision.Visibility != VisibilityPublic && record.ListingRevision.Visibility != VisibilityTenant {
			continue
		}
		identifier, err := ParseAIRIdentifier(record.Resource.CanonicalIdentifier)
		if err != nil {
			continue
		}
		publisher, ok := m.publishers[record.ListingRevision.PublisherID]
		if !ok || publisher.TenantID != tenantID || !contains(publisher.ApprovedPublicationDomains, identifier.Publisher) {
			continue
		}
		record.Display = nil // Collection labels do not change publication construction or its byte budget.
		raw, err := json.Marshal(record)
		if err != nil {
			return PublicationRecordPage{}, Wrap(CodeInternal, "measure publication record", err)
		}
		projectionBytes += len(raw)
		if projectionBytes > MaxPublicationProjectionBytes {
			return PublicationRecordPage{}, E(CodeWorkLimitExceeded, "publication record projection exceeds the supported byte limit")
		}
		items = append(items, clone(record))
		if len(items) > request.Limit {
			return PublicationRecordPage{Overflow: true}, nil
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Resource.CanonicalIdentifier != items[j].Resource.CanonicalIdentifier {
			return items[i].Resource.CanonicalIdentifier < items[j].Resource.CanonicalIdentifier
		}
		if items[i].Version.Version != items[j].Version.Version {
			return items[i].Version.Version < items[j].Version.Version
		}
		return items[i].Release.ID < items[j].Release.ID
	})
	return PublicationRecordPage{Records: items}, nil
}

// PublishedReleaseIDsByVersionRevision returns bounded active release
// references for withdrawal and reports overflow instead of truncating them.
func (m *MemoryRepository) PublishedReleaseIDsByVersionRevision(ctx context.Context, tenantID, revisionID string, limit int) ([]string, bool, error) {
	if limit < 1 {
		return nil, false, E(CodeInvalidArgument, "release lookup limit must be positive")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, limit+1)
	for _, listing := range m.listings {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if listing.TenantID != tenantID || listing.CurrentReleaseID == "" {
			continue
		}
		release, ok := m.releases[listing.CurrentReleaseID]
		if !ok || release.TenantID != tenantID || release.VersionRevisionID != revisionID || release.State != ReleasePublished && release.State != ReleaseDeprecated {
			continue
		}
		ids = append(ids, release.ID)
		if len(ids) > limit {
			return nil, true, nil
		}
	}
	sort.Strings(ids)
	return ids, false, nil
}

// catalogRecordsLocked projects matching current releases while the caller
// holds the repository lock.
func (m *MemoryRepository) catalogRecordsLocked(tenantID string, publicOnly bool) []CatalogSummary {
	items := make([]CatalogSummary, 0)
	for _, listing := range m.listings {
		if record, ok := m.catalogRecordLocked(tenantID, listing, publicOnly); ok {
			items = append(items, record.Summary())
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ListingRevision.DisplayName == items[j].ListingRevision.DisplayName {
			return items[i].Release.ID < items[j].Release.ID
		}
		return strings.ToLower(items[i].ListingRevision.DisplayName) < strings.ToLower(items[j].ListingRevision.DisplayName)
	})
	return items
}

// catalogRecordLocked requires a consistent active tenant
// resource/listing/release chain and applies the requested visibility boundary.
func (m *MemoryRepository) catalogRecordLocked(tenantID string, listing CatalogListing, publicOnly bool) (CatalogRecord, bool) {
	if listing.TenantID != tenantID || listing.CurrentReleaseID == "" {
		return CatalogRecord{}, false
	}
	release, ok := m.releases[listing.CurrentReleaseID]
	if !ok || release.State != ReleasePublished && release.State != ReleaseDeprecated {
		return CatalogRecord{}, false
	}
	listingRevision, ok := m.listingRevisions[release.ListingRevisionID]
	if !ok || publicOnly && listingRevision.Visibility != VisibilityPublic {
		return CatalogRecord{}, false
	}
	versionRevision, ok := m.versionRevisions[release.VersionRevisionID]
	if !ok {
		return CatalogRecord{}, false
	}
	version, ok := m.versions[versionRevision.ResourceVersionID]
	if !ok {
		return CatalogRecord{}, false
	}
	resource, ok := m.resources[listing.ResourceID]
	if !ok || resource.DeletedAt != nil {
		return CatalogRecord{}, false
	}
	evidenceID := versionRevision.EvidenceSetID
	if runs := m.validationRuns[versionRevision.ID]; len(runs) > 0 {
		evidenceID = runs[len(runs)-1]
	}
	evidence, ok := m.evidenceSets[evidenceID]
	if !ok {
		return CatalogRecord{}, false
	}
	versionRevision.EvidenceSetID = evidence.ID
	publisherName, namespaceName := "", ""
	if publisher := m.publishers[listingRevision.PublisherID]; publisher.TenantID == tenantID {
		publisherName = publisher.DisplayName
	}
	if namespace := m.namespaces[listing.NamespaceID]; namespace.TenantID == tenantID {
		namespaceName = namespace.DisplayName
	}
	return CatalogRecord{Display: NewCatalogPresentation(publisherName, namespaceName, listingRevision.License), Resource: resource, Version: version, VersionRevision: versionRevision, Listing: listing, ListingRevision: listingRevision, Release: release, Evidence: evidence, Source: "local"}, true
}

// SearchCatalog mirrors the PostgreSQL page contract over the bounded
// development data set; production search remains database-bounded.
func (m *MemoryRepository) SearchCatalog(ctx context.Context, tenantID string, request CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
	if err := identity.RequireCollectionTenant(ctx, tenantID); err != nil {
		return CatalogSearchRepositoryPage{}, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (CatalogSearchRepositoryPage, error) {
			return m.SearchCatalog(ctx, tenantID, request)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	records, err := m.authorizedCatalogRecordsLocked(ctx, tenantID)
	if err != nil {
		return CatalogSearchRepositoryPage{}, err
	}
	generation := catalogSearchGeneration(records)
	if request.ExpectedGeneration != "" && request.ExpectedGeneration != generation {
		return CatalogSearchRepositoryPage{}, Wrap(CodeInvalidArgument, "catalog cursor is stale", ErrSearchGenerationChanged)
	}
	matched := filterAndScoreRecords(records, request.Query)
	if request.IdentityProbes != nil {
		if len(request.IdentityProbes) > LiveSearchWindow {
			return CatalogSearchRepositoryPage{}, E(CodeInvalidArgument, "identity probe exceeds its bound")
		}
		matches := []IndexedIdentity{}
		for _, probe := range request.IdentityProbes {
			for _, record := range matched {
				if probe.VersionPresent && probe.Identifier == record.Resource.CanonicalIdentifier && probe.Version == record.Version.Version {
					matches = append(matches, probe)
					break
				}
			}
		}
		return CatalogSearchRepositoryPage{Generation: generation, Matches: matches}, nil
	}
	total := len(matched)
	if request.After != nil {
		matched = recordsAfterPosition(matched, *request.After, request.Query.Order)
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 20
	}
	hasMore := len(matched) > limit
	if hasMore {
		matched = matched[:limit]
	}
	return CatalogSearchRepositoryPage{Items: matched, Total: total, Generation: generation, HasMore: hasMore}, nil
}

// ExploreCatalog computes facets from the tenant's current in-memory Catalog
// projection.
func (m *MemoryRepository) ExploreCatalog(ctx context.Context, tenantID string, query SearchQuery, facets []FacetRequest) (ExploreResponse, error) {
	if err := identity.RequireCollectionTenant(ctx, tenantID); err != nil {
		return ExploreResponse{}, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (ExploreResponse, error) {
			return m.ExploreCatalog(ctx, tenantID, query, facets)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	records, err := m.authorizedCatalogRecordsLocked(ctx, tenantID)
	if err != nil {
		return ExploreResponse{}, err
	}
	matched := filterAndScoreRecords(records, query)
	return buildCatalogFacets(matched, facets), nil
}

// GetCatalogRecord resolves the current release-backed Catalog projection for
// the explicit tenant.
func (m *MemoryRepository) GetCatalogRecord(ctx context.Context, tenantID, releaseID string) (CatalogRecord, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (CatalogRecord, error) { return m.GetCatalogRecord(ctx, tenantID, releaseID) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, listing := range m.listings {
		if listing.TenantID != tenantID || listing.CurrentReleaseID != releaseID {
			continue
		}
		if record, ok := m.catalogRecordLocked(tenantID, listing, false); ok {
			return clone(record), nil
		}
	}
	return CatalogRecord{}, E(CodeNotFound, "catalog record was not found")
}

// AuditEvents returns the requested tenant's filtered, ordered audit page.
func (m *MemoryRepository) AuditEvents(_ context.Context, tenantID string, request AuditEventListRequest) ([]AuditEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]AuditEvent, 0, min(request.Limit, len(m.audit)))
	for _, event := range m.audit {
		beforeCursor := request.AfterOccurredAt == nil || event.At.Before(*request.AfterOccurredAt) || event.At.Equal(*request.AfterOccurredAt) && event.ID < request.AfterID
		if event.TenantID == tenantID && beforeCursor && auditEventMatches(event, request.Filters) {
			items = append(items, cloneAuditEvent(event))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].At.Equal(items[j].At) {
			return items[i].At.After(items[j].At)
		}
		return items[i].ID > items[j].ID
	})
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return items, nil
}

// AppendAuditEvent adds an audit-only record without creating an outbox
// notification.
func (m *MemoryRepository) AppendAuditEvent(ctx context.Context, event AuditEvent) error {
	if identity.NeedsPermissionAdmission(ctx) {
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			operationErr := m.AppendAuditEvent(ctx, event)
			return operationErr
		})
		return err
	}

	if err := audit.ValidateNewRecord(event); err != nil {
		return Wrap(CodeInternal, "validate audit event", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, currentMemoryAudit(ctx, event))
	return nil
}

// AppendEvidence adds one audit row and an independently classified optional
// notification to the shared in-memory evidence inventory under one lock.
func (m *MemoryRepository) AppendEvidence(ctx context.Context, event AuditEvent, outbox *OutboxEvent) error {
	if identity.NeedsPermissionAdmission(ctx) {
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			operationErr := m.AppendEvidence(ctx, event, outbox)
			return operationErr
		})
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := audit.ValidateNewRecord(event); err != nil {
		return Wrap(CodeInternal, "validate audit event", err)
	}
	m.audit = append(m.audit, currentMemoryAudit(ctx, event))
	if outbox != nil {
		m.appendNotification(*outbox)
	}
	return nil
}

// AppendClaimedOutboxAudit is the atomic in-memory worker evidence boundary.
// Callers retain their delivery-state lock until their infallible assignment.
func (m *MemoryRepository) AppendClaimedOutboxAudit(ctx context.Context, tenantID, eventID, owner string, clock Clock, record AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.TenantID != tenantID || owner == "" || clock == nil {
		return E(CodeConflict, "outbox claim evidence is invalid")
	}
	if err := audit.ValidateNewRecord(record); err != nil {
		return Wrap(CodeInternal, "validate claimed audit event", err)
	}
	now := clock.Now()
	for _, event := range m.outbox {
		if event.TenantID == tenantID && event.ID == eventID && event.PublishedAt == nil && event.LeaseOwner == owner && event.LeaseExpiresAt != nil && event.LeaseExpiresAt.After(now) {
			m.audit = append(m.audit, cloneAuditEvent(record))
			return nil
		}
	}
	return E(CodeConflict, "outbox event lease is not owned or has expired")
}

// ExportAuditEvents selects the bounded tenant query and delegates
// deterministic manifest construction to the audit package.
func (m *MemoryRepository) ExportAuditEvents(ctx context.Context, tenantID string, request AuditExportRequest) (AuditExport, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 AuditExport
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.ExportAuditEvents(ctx, tenantID, request)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]AuditEvent, 0, min(audit.MaxExportRows+1, len(m.audit)))
	for _, event := range m.audit {
		if event.TenantID == tenantID && auditEventMatches(event, request.Filters) {
			items = append(items, cloneAuditEvent(event))
			if len(items) > audit.MaxExportRows {
				break
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].At.Equal(items[j].At) {
			return items[i].At.Before(items[j].At)
		}
		return items[i].ID < items[j].ID
	})
	export, _, err := audit.BuildExport(tenantID, request.Filters, request.Cutoff, request.GeneratedAt, items)
	if err != nil {
		return AuditExport{}, TranslateAuditExportError(err)
	}
	request.Access = CompleteAuditExportAccess(request.Access, export.Manifest)
	if err := audit.ValidateNewRecord(request.Access); err != nil {
		return AuditExport{}, Wrap(CodeInternal, "validate audit-export access event", err)
	}
	m.audit = append(m.audit, cloneAuditEvent(request.Access))
	return export, nil
}

// auditEventMatches applies exact tenant-independent audit query predicates to
// an already selected record.
func auditEventMatches(event AuditEvent, query audit.Query) bool {
	return (query.Actor == "" || event.Actor == query.Actor) &&
		(query.Action == "" || event.Action == query.Action) &&
		(query.RequestID == "" || event.RequestID == query.RequestID) &&
		(query.TargetType == "" || event.TargetType == query.TargetType && event.TargetID == query.TargetID) &&
		(query.OccurredFrom == nil || !event.At.Before(*query.OccurredFrom)) &&
		(query.OccurredBefore == nil || event.At.Before(*query.OccurredBefore))
}

// ClaimOutbox selects due unacknowledged records across tenants and assigns
// owner-bound leases up to the requested limit.
func (m *MemoryRepository) ClaimOutbox(ctx context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]OutboxEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]OutboxEvent, 0, min(limit, len(m.outbox)))
	indexes := make([]int, len(m.outbox))
	for i := range indexes {
		indexes[i] = i
	}
	due := func(event OutboxEvent) time.Time {
		if event.DispatchAfter != nil {
			return *event.DispatchAfter
		}
		return event.CreatedAt
	}
	sort.Slice(indexes, func(i, j int) bool {
		left, right := m.outbox[indexes[i]], m.outbox[indexes[j]]
		if a, b := due(left), due(right); !a.Equal(b) {
			return a.Before(b)
		}
		return left.ID < right.ID
	})
	for _, index := range indexes {
		event := &m.outbox[index]
		if event.PublishedAt == nil && (event.DispatchAfter == nil || !event.DispatchAfter.After(now)) && (event.LeaseExpiresAt == nil || !event.LeaseExpiresAt.After(now)) {
			expires := now.Add(lease)
			event.LeaseOwner = owner
			event.LeaseExpiresAt = &expires
			items = append(items, cloneOutboxEvent(*event))
			if len(items) == limit {
				break
			}
		}
	}
	return items, nil
}

// MarkOutboxPublished acknowledges only the matching tenant/event claim while
// its lease remains valid.
func (m *MemoryRepository) MarkOutboxPublished(ctx context.Context, tenantID, eventID, owner string, publishedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range m.outbox {
		if m.outbox[index].TenantID == tenantID && m.outbox[index].ID == eventID && m.outbox[index].PublishedAt == nil && m.outbox[index].LeaseOwner == owner && m.outbox[index].LeaseExpiresAt != nil && m.outbox[index].LeaseExpiresAt.After(publishedAt) {
			m.outbox[index].PublishedAt = &publishedAt
			m.outbox[index].LeaseOwner = ""
			m.outbox[index].LeaseExpiresAt = nil
			return nil
		}
	}
	return E(CodeConflict, "outbox event lease is not owned or has expired")
}

// RenewOutbox extends only a still-valid matching tenant/event owner lease.
func (m *MemoryRepository) RenewOutbox(ctx context.Context, tenantID, eventID, owner string, now time.Time, lease time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range m.outbox {
		if m.outbox[index].TenantID == tenantID && m.outbox[index].ID == eventID && m.outbox[index].PublishedAt == nil && m.outbox[index].LeaseOwner == owner && m.outbox[index].LeaseExpiresAt != nil && m.outbox[index].LeaseExpiresAt.After(now) {
			expires := now.Add(lease)
			m.outbox[index].LeaseExpiresAt = &expires
			return nil
		}
	}
	return E(CodeConflict, "outbox event lease is not owned or has expired")
}

// ReleaseOutbox clears claim ownership only for the matching tenant/event
// owner.
func (m *MemoryRepository) ReleaseOutbox(ctx context.Context, tenantID, eventID, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range m.outbox {
		if m.outbox[index].TenantID == tenantID && m.outbox[index].ID == eventID && m.outbox[index].PublishedAt == nil && m.outbox[index].LeaseOwner == owner {
			m.outbox[index].LeaseOwner = ""
			m.outbox[index].LeaseExpiresAt = nil
			return nil
		}
	}
	return E(CodeConflict, "outbox event lease is not owned")
}

// DeferOutbox schedules the matching leased event for later work and releases
// its claim under the repository delay bound.
func (m *MemoryRepository) DeferOutbox(ctx context.Context, tenantID, eventID, owner string, now, notBefore time.Time) error {
	if !notBefore.After(now) || notBefore.After(now.Add(time.Hour)) {
		return E(CodeInvalidArgument, "outbox deferral must be within one hour")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range m.outbox {
		event := &m.outbox[index]
		if event.TenantID == tenantID && event.ID == eventID && event.PublishedAt == nil && event.LeaseOwner == owner && event.LeaseExpiresAt != nil && event.LeaseExpiresAt.After(now) {
			event.DispatchAfter = &notBefore
			event.LeaseOwner = ""
			event.LeaseExpiresAt = nil
			return nil
		}
	}
	return E(CodeConflict, "outbox event lease is not owned or has expired")
}

// appendEvidence appends the transition audit and classified notification while
// the caller holds the repository lock.
func (m *MemoryRepository) appendEvidence(ctx context.Context, audit AuditEvent, outbox OutboxEvent) {
	m.indexOriginatingPrincipal(audit)
	m.indexWorkAudit(audit)
	m.audit = append(m.audit, currentMemoryAudit(ctx, audit))
	m.appendNotification(outbox)
}

// contains reports exact string membership without normalizing stored
// identities.
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// clone copies JSON-shaped domain values so repository callers cannot retain
// mutable aliases.
func clone[T any](value T) T {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var copyValue T
	if err := json.Unmarshal(raw, &copyValue); err != nil {
		panic(err)
	}
	return copyValue
}

// cloneAuditEvent copies audit details while preserving the typed record
// envelope.
func cloneAuditEvent(value AuditEvent) AuditEvent {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var copyValue AuditEvent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&copyValue); err != nil {
		panic(err)
	}
	return copyValue
}

// currentMemoryAudit stamps the revision selected under the actual admission
// fence, rather than a preparatory decision made before a concurrent policy edit.
func currentMemoryAudit(ctx context.Context, record AuditEvent) AuditEvent {
	record = cloneAuditEvent(record)
	if len(identity.RecordedPermissionChecks(ctx)) > 0 {
		if record.Details == nil {
			record.Details = map[string]any{}
		}
		record.Details["authorizationDigest"] = identity.DecisionDigest(ctx)
	}
	return record
}

// authorizedCatalogRecordsLocked excludes inaccessible local evidence before
// constructing search projections, generation, matches and aggregate populations.
func (m *MemoryRepository) authorizedCatalogRecordsLocked(ctx context.Context, tenantID string) ([]CatalogSummary, error) {
	if _, selected := identity.CollectionRevision(ctx); !selected {
		return m.catalogRecordsLocked(tenantID, false), nil
	}
	items := make([]CatalogSummary, 0)
	for _, listing := range m.listings {
		if listing.TenantID != tenantID {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "resource.read", listing.NamespaceID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		if record, ok := m.catalogRecordLocked(tenantID, listing, false); ok {
			items = append(items, record.Summary())
		}
	}
	// The existing filter/scoring phase sorts rows; generation is deterministic
	// over the eligible set independently of map iteration.
	sort.Slice(items, func(i, j int) bool { return items[i].Release.ID < items[j].Release.ID })
	return items, nil
}
