// Package httpserver maps the HTTP contract to authenticated domain services.
// Middleware establishes request identity, authorization, deadlines, and command
// replay before handlers invoke owning services. Public discovery selects active
// audience-scoped snapshots; worker-only operations use a separate package.
package httpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/draftfile"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/buildinfo"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/catalogmcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/tenantsettings"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

// Dependencies is the complete HTTP composition contract. New routes should use
// an owning domain service rather than reaching a store directly.
type Dependencies struct {
	MCP                  config.MCP
	PublicBaseURL        string
	AuthIssuer           string
	TenantSettings       *tenantsettings.Service
	PersonalStore        personal.Store
	RatingsStore         ratings.Store
	Artwork              *artwork.Service
	MediaPolicyDigest    string
	Process              *processinfo.Process
	Role                 string
	Catalog              *catalog.Service
	Identity             *identity.Service
	Federation           *federation.Manager
	Jobs                 *jobs.Service
	Feedback             *feedback.Service
	Webhooks             *webhook.Service
	Authenticator        authn.Authenticator
	Idempotency          idempotency.Store
	AtomicIdempotency    idempotency.AtomicStore
	Logger               *slog.Logger
	PublicationOverview  publication.ActiveSummaryReader
	DiscoveryHosts       []DiscoveryHost
	MaxBodyBytes         int64
	RequestTimeout       time.Duration
	ServiceVersion       string
	Commit               string
	SourceState          string
	ARDProfile           string
	AICatalogProfile     string
	ProfileBundleDigest  string
	ProfileArtifactCount int
	CORSOrigins          []string
	Readiness            func(context.Context) error
	EnforceOAuthScopes   bool
	Capabilities         CapabilitySnapshot
	CursorKey            []byte
	Now                  func() time.Time
	Observability        *observability.Registry
	Tracing              catalog.Tracer
}

// DiscoveryMode selects whether a configured host disables discovery or serves
// anonymous or authenticated audience snapshots.
type DiscoveryMode string

const (
	DiscoveryDisabled      DiscoveryMode = "disabled"
	DiscoveryAnonymous     DiscoveryMode = "anonymous"
	DiscoveryAuthenticated DiscoveryMode = "authenticated"
)

// DiscoveryHost binds an exact public host to one tenant, publisher domain and
// publication manager. Bindings are validated when the server is constructed.
type DiscoveryHost struct {
	Host            string
	TenantID        string
	Mode            DiscoveryMode
	PublisherDomain string
	Publication     *publication.Manager
}

var revisionETag = regexp.MustCompile(`^"rev-([1-9][0-9]*)"$`)

// Server owns routing, middleware, contract decoding, and health state; domain
// authorization and lifecycle behavior remain in the injected services.
type Server struct {
	mcp                  *mcpHTTP
	tenantSettings       *tenantsettings.Service
	ratings              *ratings.Service
	artwork              *artwork.Service
	mediaPolicyDigest    string
	process              *processinfo.Process
	role                 string
	catalog              *catalog.Service
	catalogSearch        *catalog.SearchService
	identity             *identity.Service
	federation           *federation.Manager
	jobs                 *jobs.Service
	personal             *personal.Service
	feedback             *feedback.Service
	webhooks             *webhook.Service
	capabilities         CapabilitySnapshot
	authenticator        authn.Authenticator
	publicationOverview  publication.ActiveSummaryReader
	discovery            map[string]DiscoveryHost
	maxBodyBytes         int64
	requestTimeout       time.Duration
	serviceVersion       string
	commit               string
	sourceState          string
	ardProfile           string
	aiCatalogProfile     string
	profileBundleDigest  string
	profileArtifactCount int
	enforceOAuthScopes   bool
	readiness            func(context.Context) error
	logger               *slog.Logger
	securityWarnings     [2]diagnostics.Warning
	readinessWarning     diagnostics.Warning
	readinessFailed      atomic.Bool
	dependencyWarning    diagnostics.Warning
	federationWarning    diagnostics.Warning
	ready                atomic.Bool
	observability        *observability.Registry
	handler              http.Handler
	cursorKey            []byte
	now                  func() time.Time
}

// New validates injected services, host bindings and limits, then composes the product middleware stack.
func New(dependencies Dependencies) (*Server, error) {
	if dependencies.Catalog == nil || dependencies.Identity == nil || dependencies.Federation == nil || dependencies.Jobs == nil || dependencies.Authenticator == nil || (dependencies.Idempotency == nil) == (dependencies.AtomicIdempotency == nil) || dependencies.MaxBodyBytes <= 0 || dependencies.RequestTimeout <= 0 || len(dependencies.CursorKey) < 32 {
		return nil, errors.New("HTTP server dependencies are incomplete")
	}
	// Draft export must not parse the embedded contract inside its first request deadline.
	draftfile.InitializeContract()
	if dependencies.Logger == nil {
		dependencies.Logger = slog.Default()
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.Role == "" {
		dependencies.Role = "api"
	}
	if dependencies.Role != "api" && dependencies.Role != "all" {
		return nil, errors.New("HTTP server role must be api or all")
	}
	if dependencies.Process == nil {
		process, err := processinfo.New()
		if err != nil {
			return nil, err
		}
		dependencies.Process = process
	}
	if dependencies.Observability == nil {
		dependencies.Observability = observability.New(dependencies.Now, dependencies.Process)
	}
	catalogSearch, err := catalog.NewSearchService(dependencies.Catalog, dependencies.Federation)
	if err != nil {
		return nil, err
	}
	capabilities, err := normalizeCapabilities(dependencies.Capabilities, dependencies.Webhooks != nil)
	if err != nil {
		return nil, err
	}
	if len(dependencies.DiscoveryHosts) > 64 {
		return nil, errors.New("HTTP server discovery-host dependencies exceed the 64-host bound")
	}
	discovery := make(map[string]DiscoveryHost, len(dependencies.DiscoveryHosts))
	for _, binding := range dependencies.DiscoveryHosts {
		host, ok := normalizeDiscoveryHost(binding.Host)
		if !ok || host != binding.Host || !identity.ValidTenantID(binding.TenantID) || binding.PublisherDomain == "" || binding.Mode != DiscoveryDisabled && binding.Mode != DiscoveryAnonymous && binding.Mode != DiscoveryAuthenticated || binding.Mode != DiscoveryDisabled && binding.Publication == nil {
			return nil, errors.New("HTTP server discovery-host dependency is invalid")
		}
		if _, exists := discovery[host]; exists {
			return nil, errors.New("HTTP server discovery-host dependencies contain a duplicate host")
		}
		discovery[host] = binding
	}
	server := &Server{tenantSettings: dependencies.TenantSettings, artwork: dependencies.Artwork, mediaPolicyDigest: dependencies.MediaPolicyDigest, process: dependencies.Process, role: dependencies.Role, feedback: dependencies.Feedback, catalog: dependencies.Catalog, catalogSearch: catalogSearch, identity: dependencies.Identity, federation: dependencies.Federation, jobs: dependencies.Jobs, webhooks: dependencies.Webhooks, capabilities: capabilities, authenticator: dependencies.Authenticator, publicationOverview: dependencies.PublicationOverview, discovery: discovery, maxBodyBytes: dependencies.MaxBodyBytes, requestTimeout: dependencies.RequestTimeout, serviceVersion: dependencies.ServiceVersion, commit: dependencies.Commit, sourceState: buildinfo.State(dependencies.SourceState), ardProfile: dependencies.ARDProfile, aiCatalogProfile: dependencies.AICatalogProfile, profileBundleDigest: dependencies.ProfileBundleDigest, profileArtifactCount: dependencies.ProfileArtifactCount, enforceOAuthScopes: dependencies.EnforceOAuthScopes, readiness: dependencies.Readiness, logger: dependencies.Logger, cursorKey: append([]byte(nil), dependencies.CursorKey...), now: dependencies.Now, observability: dependencies.Observability}
	if dependencies.PersonalStore != nil {
		server.personal, err = personal.New(dependencies.PersonalStore, personalAuthority{local: dependencies.Catalog, federation: dependencies.Federation}, ratingClock{now: dependencies.Now}, id.Generator{})
		if err != nil {
			return nil, err
		}
	}
	if dependencies.RatingsStore != nil {
		server.ratings, err = ratings.New(dependencies.RatingsStore, ratingAuthority{local: dependencies.Catalog, federation: dependencies.Federation}, ratingClock{now: dependencies.Now}, id.Generator{})
		if err != nil {
			return nil, err
		}
	}
	if dependencies.MCP.Enabled {
		if err := dependencies.MCP.Validate(false); err != nil {
			return nil, err
		}
		public := strings.TrimSuffix(dependencies.PublicBaseURL, "/")
		// Reuse the runtime public-origin validator for direct composition callers.
		if public == "" {
			return nil, errors.New("MCP public origin is required")
		}
		if err := config.ValidatePublicOrigin(public); err != nil {
			return nil, errors.New("MCP public origin is invalid")
		}
		bodyLimit := min(dependencies.MaxBodyBytes, int64(catalogmcp.MaxFrameBytes))
		server.mcp = &mcpHTTP{reader: server, authenticator: dependencies.Authenticator, settings: dependencies.MCP, publicOrigin: public, issuer: dependencies.AuthIssuer, version: dependencies.ServiceVersion, enforceScopes: dependencies.EnforceOAuthScopes, timeout: dependencies.RequestTimeout, maxBody: bodyLimit, slots: make(chan struct{}, dependencies.MCP.MaxConcurrency), logger: dependencies.Logger, metrics: server.observability}
	}
	router := http.NewServeMux()
	server.routes(router)
	handler := recoverPanics(dependencies.Logger, routerErrorHandler(router))
	cache := newIdempotencyCache(dependencies.Idempotency, dependencies.Logger)
	cache.atomic = dependencies.AtomicIdempotency
	cache.replayAdmission = server.accessReplayAdmission
	handler = cache.middleware(dependencies.MaxBodyBytes, handler)
	handler = authorizationScopes(dependencies.EnforceOAuthScopes, handler)
	handler = auditExecution(handler)
	handler = authentication(dependencies.Authenticator, handler)
	handler = controlHeaderAdmission(handler)
	handler = securityHeaders(handler)
	handler = productWorkDeadline(router, dependencies.RequestTimeout, server.observability, handler)
	handler = cors(dependencies.CORSOrigins, handler)
	handler = recoverPanics(dependencies.Logger, handler)
	handler = server.metricsMiddleware(router, handler)
	handler = traceHTTP(dependencies.Tracing, router, handler)
	handler = requestIDMiddleware(handler)
	server.handler = handler
	server.ready.Store(true)
	return server, nil
}

// normalizeCapabilities fills absent capability states and rejects webhook declarations that disagree with service wiring.
func normalizeCapabilities(snapshot CapabilitySnapshot, webhookService bool) (CapabilitySnapshot, error) {
	if snapshot.ServiceActivity == "" {
		snapshot.ServiceActivity = CapabilityDisabled
	}
	if snapshot.ServiceActivity != CapabilityEnabled && snapshot.ServiceActivity != CapabilityDisabled {
		return CapabilitySnapshot{}, errors.New("HTTP server service activity capability must be enabled or disabled")
	}
	if snapshot.SemanticSearch == "" {
		snapshot.SemanticSearch = CapabilityUnavailable
	}
	if snapshot.CredentialResolution == "" {
		snapshot.CredentialResolution = CapabilityUnavailable
	}
	if snapshot.ManagedMedia == "" {
		snapshot.ManagedMedia = CapabilityUnavailable
	}
	if snapshot.SoftwareAssessment == "" {
		snapshot.SoftwareAssessment = CapabilityUnavailable
	}
	if snapshot.Webhooks == "" {
		if webhookService {
			snapshot.Webhooks = CapabilityEnabled
		} else {
			snapshot.Webhooks = CapabilityDisabled
		}
	}
	for _, capability := range []struct {
		name  string
		state CapabilityState
	}{
		{name: "semantic search", state: snapshot.SemanticSearch},
		{name: "credential resolution", state: snapshot.CredentialResolution},
		{name: "managed media", state: snapshot.ManagedMedia},
		{name: "software assessment", state: snapshot.SoftwareAssessment},
		{name: "webhooks", state: snapshot.Webhooks},
	} {
		if capability.state != CapabilityEnabled && capability.state != CapabilityDisabled && capability.state != CapabilityUnavailable {
			return CapabilitySnapshot{}, fmt.Errorf("HTTP server %s capability state %q is invalid", capability.name, capability.state)
		}
	}
	if (snapshot.Webhooks == CapabilityEnabled) != webhookService {
		return CapabilitySnapshot{}, errors.New("HTTP server webhook capability state does not match its service wiring")
	}
	return snapshot, nil
}

// Handler returns the fully composed product handler with authentication, replay and transport protections.
func (s *Server) Handler() http.Handler { return s.handler }

// SetReady updates local readiness; readiness requests still run the configured dependency check.
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// routes registers implemented product/discovery paths with their closed query and identifier checks.
func (s *Server) routes(router *http.ServeMux) {
	if s.mcp != nil {
		router.Handle("/mcp", s.mcp)
		router.Handle(mcpMetadataPath, s.mcp)
	}
	s.personalRoutes(router)
	s.feedbackRoutes(router)
	s.ratingsRoutes(router)
	router.HandleFunc("GET /api/v1/tenant-settings", s.withQueryParameters(s.handleTenantSettings))
	router.HandleFunc("POST /api/v1/tenant-settings", s.withQueryParameters(s.handleTenantSettings))
	router.HandleFunc("GET /livez", s.withQueryParameters(s.handleLive))
	router.HandleFunc("GET /readyz", s.withQueryParameters(s.handleReady))
	router.HandleFunc("GET /version", s.withQueryParameters(s.handleVersion))
	router.HandleFunc("GET /metrics", s.withQueryParameters(s.handleMetrics))
	router.HandleFunc("GET /openapi/control-v1.yaml", s.withQueryParameters(s.handleOpenAPI))

	if s.capabilities.ServiceActivity == CapabilityEnabled {
		router.HandleFunc("GET /api/v1/service-activity", s.withQueryParameters(s.handleServiceActivity))
	}
	router.HandleFunc("GET /api/v1/service-information", s.withQueryParameters(s.handleServiceInformation))
	router.HandleFunc("GET /api/v1/capabilities", s.withQueryParameters(s.handleCapabilities))
	router.HandleFunc("GET /api/v1/me", s.withQueryParameters(s.handleSelf, "limit", "cursor", "namespaceId"))
	router.HandleFunc("GET /api/v1/memberships", s.withQueryParameters(s.handleMembershipsList, "limit", "cursor", "subject", "state", "actorType", "realmId"))
	router.HandleFunc("GET /api/v1/memberships/{membershipId}/access", s.withQueryParameters(s.withUUIDPathParameters(s.handleMemberAccess, "membershipId"), "limit", "cursor"))
	router.HandleFunc("GET /api/v1/memberships/{membershipId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleMembershipGet, "membershipId")))
	router.HandleFunc("POST /api/v1/memberships/{membershipCommand}", s.withQueryParameters(s.dispatchMembershipCommand))
	router.HandleFunc("GET /api/v1/role-assignments", s.withQueryParameters(s.handleRoleAssignmentsList, "limit", "cursor", "membershipId"))
	router.HandleFunc("POST /api/v1/role-assignments", s.withQueryParameters(s.handleRoleAssignmentCreate))
	router.HandleFunc("POST /api/v1/role-assignments/{assignmentCommand}", s.withQueryParameters(s.dispatchRoleAssignmentCommand))
	router.HandleFunc("GET /api/v1/access/permissions", s.withQueryParameters(s.handlePermissionCatalog))
	router.HandleFunc("POST /api/v1/access/capabilities", s.withQueryParameters(s.handlePermissionDecisions))
	router.HandleFunc("GET /api/v1/access/self", s.withQueryParameters(s.handleEffectiveAccess, "limit", "cursor"))
	router.HandleFunc("GET /api/v1/access/roles", s.withQueryParameters(s.handleDefinitionsList, "limit", "cursor", "name", "state", "key"))
	router.HandleFunc("POST /api/v1/access/roles", s.withQueryParameters(s.handleDefinitionCreate))
	router.HandleFunc("GET /api/v1/access/roles/{roleId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleDefinitionGet, "roleId")))
	router.HandleFunc("PATCH /api/v1/access/roles/{roleId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleDefinitionUpdate, "roleId")))
	router.HandleFunc("POST /api/v1/access/roles/{roleCommand}", s.withQueryParameters(s.dispatchDefinitionCommand))
	router.HandleFunc("GET /api/v1/access/roles/{roleId}/assignments", s.withQueryParameters(s.withUUIDPathParameters(s.handleDefinitionAssignments, "roleId"), "limit", "cursor"))
	router.HandleFunc("GET /api/v1/access/groups", s.withQueryParameters(s.handleAccessGroupsList, "limit", "cursor", "name", "state"))
	router.HandleFunc("POST /api/v1/access/groups", s.withQueryParameters(s.handleAccessGroupCreate))
	router.HandleFunc("GET /api/v1/access/groups/{groupId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleAccessGroupGet, "groupId")))
	router.HandleFunc("PATCH /api/v1/access/groups/{groupId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleAccessGroupUpdate, "groupId")))
	router.HandleFunc("POST /api/v1/access/groups/{groupCommand}", s.withQueryParameters(s.dispatchAccessGroupCommand))
	router.HandleFunc("GET /api/v1/access/groups/{groupId}/members", s.withQueryParameters(s.withUUIDPathParameters(s.handleGroupMembersList, "groupId"), "limit", "cursor"))
	router.HandleFunc("POST /api/v1/access/groups/{groupId}/members", s.withQueryParameters(s.withUUIDPathParameters(s.handleGroupMemberCreate, "groupId")))
	router.HandleFunc("POST /api/v1/access/groups/{groupId}/members/{memberCommand}", s.withQueryParameters(s.withUUIDPathParameters(s.dispatchGroupMemberCommand, "groupId")))
	router.HandleFunc("GET /api/v1/access/assignments", s.withQueryParameters(s.handleAccessAssignmentsList, "limit", "cursor", "roleDefinitionId", "membershipId", "groupId"))
	router.HandleFunc("POST /api/v1/access/assignments", s.withQueryParameters(s.handleAccessAssignmentCreate))
	router.HandleFunc("POST /api/v1/access/assignments/{accessAssignmentCommand}", s.withQueryParameters(s.dispatchAccessAssignmentCommand))
	router.HandleFunc("GET /api/v1/namespaces", s.withQueryParameters(s.handleNamespacesList, "limit", "cursor", "slug", "displayName"))
	router.HandleFunc("POST /api/v1/namespaces", s.withQueryParameters(s.handleNamespacesCreate))
	router.HandleFunc("GET /api/v1/namespaces/{namespaceId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleNamespaceGet, "namespaceId")))
	router.HandleFunc("PATCH /api/v1/namespaces/{namespaceId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleNamespacePatch, "namespaceId")))
	router.HandleFunc("GET /api/v1/publishers", s.withQueryParameters(s.handlePublishersList, "limit", "cursor", "displayName", "declaredDomain"))
	router.HandleFunc("POST /api/v1/publishers", s.withQueryParameters(s.handlePublishersCreate))
	router.HandleFunc("GET /api/v1/publishers/{publisherId}", s.withQueryParameters(s.withUUIDPathParameters(s.handlePublisherGet, "publisherId")))
	router.HandleFunc("GET /api/v1/publishers/{publisherId}/domains/{domain}/verification", s.withQueryParameters(s.withUUIDPathParameters(s.handlePublisherDomainStatus, "publisherId")))
	router.HandleFunc("GET /api/v1/publishers/{publisherId}/domains/{domain}/verification/challenge", s.withQueryParameters(s.withUUIDPathParameters(s.handlePublisherDomainChallenge, "publisherId")))
	router.HandleFunc("POST /api/v1/publishers/{publisherId}/domains/{domain}/verification:issue", s.withQueryParameters(s.withUUIDPathParameters(s.handlePublisherDomainIssue, "publisherId")))
	router.HandleFunc("POST /api/v1/publishers/{publisherId}/domains/{domain}/verification:check", s.withQueryParameters(s.withUUIDPathParameters(s.handlePublisherDomainCheck, "publisherId")))
	router.HandleFunc("POST /api/v1/publishers/{publisherId}/domains/{domain}/verification:clear", s.withQueryParameters(s.withUUIDPathParameters(s.handlePublisherDomainClear, "publisherId")))
	router.HandleFunc("PATCH /api/v1/publishers/{publisherId}", s.withQueryParameters(s.withUUIDPathParameters(s.handlePublisherPatch, "publisherId")))
	router.HandleFunc("POST /api/v1/publishers/{publisherId}/publication-domains/{domainCommand}", s.withQueryParameters(s.withUUIDPathParameters(s.dispatchPublisherPublicationDomainCommand, "publisherId")))

	router.HandleFunc("GET /api/v1/resources", s.withQueryParameters(s.handleResourcesList, "canonicalIdentifier", "kind", "limit", "cursor"))
	router.HandleFunc("GET /api/v1/resources/{resourceId}/versions", s.withQueryParameters(s.withUUIDPathParameters(s.handleVersionsList, "resourceId"), "limit", "cursor"))
	router.HandleFunc("GET /api/v1/resource-versions/{versionId}/revisions", s.withQueryParameters(s.withUUIDPathParameters(s.handleVersionRevisionsList, "versionId"), "limit", "cursor"))
	router.HandleFunc("GET /api/v1/resources/{resourceId}/listings", s.withQueryParameters(s.withUUIDPathParameters(s.handleListingsList, "resourceId"), "limit", "cursor"))
	router.HandleFunc("GET /api/v1/catalog-listings/{listingId}/revisions", s.withQueryParameters(s.withUUIDPathParameters(s.handleListingRevisionsList, "listingId"), "limit", "cursor"))
	router.HandleFunc("GET /api/v1/catalog-listings/{listingId}/releases", s.withQueryParameters(s.withUUIDPathParameters(s.handleReleasesList, "listingId"), "limit", "cursor"))
	router.HandleFunc("GET /api/v1/review-submissions", s.withQueryParameters(s.handleSubmissionsList, "limit", "cursor", "listingId", "state", "unreleased"))
	router.HandleFunc("GET /api/v1/review-submissions/{submissionId}/evidence", s.withQueryParameters(s.withUUIDPathParameters(s.handleSubmissionEvidenceGet, "submissionId")))
	router.HandleFunc("POST /api/v1/resources", s.withQueryParameters(s.handleResourcesCreate))
	router.HandleFunc("GET /api/v1/resources/{resourceId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleResourceGet, "resourceId")))
	router.HandleFunc("POST /api/v1/resources/{resourceCommand}", s.withQueryParameters(s.dispatchResourceCommand))
	router.HandleFunc("POST /api/v1/resources/{resourceId}/versions", s.withQueryParameters(s.withUUIDPathParameters(s.handleVersionCreate, "resourceId")))
	router.HandleFunc("GET /api/v1/resource-versions/{versionId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleVersionGet, "versionId")))
	router.HandleFunc("POST /api/v1/resource-versions/{versionId}/revisions", s.withQueryParameters(s.withUUIDPathParameters(s.handleVersionRevisionCreate, "versionId")))
	router.HandleFunc("GET /api/v1/version-revisions/{revisionId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleVersionRevisionGet, "revisionId")))
	router.HandleFunc("GET /api/v1/version-revisions/{revisionId}/export", s.withQueryParameters(s.withUUIDPathParameters(s.handleDraftExport, "revisionId"), "listingRevisionId"))
	router.HandleFunc("POST /api/v1/version-revisions/{revisionCommand}", s.withQueryParameters(s.dispatchVersionRevisionCommand))

	router.HandleFunc("POST /api/v1/catalog-listings", s.withQueryParameters(s.handleListingCreate))
	router.HandleFunc("GET /api/v1/catalog-listings/{listingId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleListingGet, "listingId")))
	router.HandleFunc("POST /api/v1/catalog-listings/{listingId}/revisions", s.withQueryParameters(s.withUUIDPathParameters(s.handleListingRevisionCreate, "listingId")))
	router.HandleFunc("GET /api/v1/listing-revisions/{revisionId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleListingRevisionGet, "revisionId")))
	router.HandleFunc("POST /api/v1/catalog-listings/{listingId}/submissions", s.withQueryParameters(s.withUUIDPathParameters(s.handleSubmissionCreate, "listingId")))
	router.HandleFunc("GET /api/v1/review-submissions/{submissionId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleSubmissionGet, "submissionId")))
	router.HandleFunc("POST /api/v1/review-submissions/{submissionCommand}", s.withQueryParameters(s.dispatchSubmissionCommand))
	router.HandleFunc("POST /api/v1/catalog-listings/{listingCommand}", s.withQueryParameters(s.dispatchListingCommand))
	router.HandleFunc("GET /api/v1/listing-releases/{releaseId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleReleaseGet, "releaseId")))
	router.HandleFunc("POST /api/v1/listing-releases/{releaseCommand}", s.withQueryParameters(s.dispatchReleaseCommand))
	router.HandleFunc("GET /api/v1/artifacts/{artifactId}/content", s.withQueryParameters(s.handleArtifactGet))

	router.HandleFunc("GET /api/v1/catalog/resources", s.withQueryParameters(s.handleCatalogBrowse, "limit", "cursor", "visibility", "kind", "namespaceId"))
	router.HandleFunc("POST /api/v1/artwork/read", s.withQueryParameters(s.handleArtworkRead))
	router.HandleFunc("GET /api/v1/federation/sources/{sourceId}/artwork", s.withQueryParameters(s.withUUIDPathParameters(s.handleArtworkOverrideGet, "sourceId"), "identifier", "versionPresent", "version", "kind", "presentationGeneration"))
	router.HandleFunc("GET /api/v1/federation/sources/{sourceId}/artwork/history", s.withQueryParameters(s.withUUIDPathParameters(s.handleArtworkOverrideHistory, "sourceId"), "identifier", "versionPresent", "version", "kind", "presentationGeneration", "limit", "cursor"))
	router.HandleFunc("POST /api/v1/federation/sources/{sourceId}/artwork", s.withQueryParameters(s.withUUIDPathParameters(s.handleArtworkOverrideSet, "sourceId")))
	router.HandleFunc("GET /api/v1/overview/reviews", s.withQueryParameters(s.handleOverviewReviews))
	router.HandleFunc("GET /api/v1/overview/administration", s.withQueryParameters(s.handleOverviewAdministration))
	router.HandleFunc("GET /api/v1/overview/sources", s.withQueryParameters(s.handleOverviewSources))
	router.HandleFunc("GET /api/v1/overview/jobs", s.withQueryParameters(s.handleOverviewJobs))
	router.HandleFunc("GET /api/v1/overview/publication", s.withQueryParameters(s.handleOverviewPublication))
	router.HandleFunc("GET /api/v1/overview/resources", s.withQueryParameters(s.handleOverviewResources))
	router.HandleFunc("GET /api/v1/overview/releases", s.withQueryParameters(s.handleOverviewReleases))
	router.HandleFunc("GET /api/v1/overview/namespaces", s.withQueryParameters(s.handleOverviewDirectory(catalog.InventoryNamespaces)))
	router.HandleFunc("GET /api/v1/overview/publishers", s.withQueryParameters(s.handleOverviewDirectory(catalog.InventoryPublishers)))
	router.HandleFunc("GET /api/v1/overview/indexed", s.withQueryParameters(s.handleOverviewIndexed))
	router.HandleFunc("POST /api/v1/catalog/search", s.withQueryParameters(s.handleCatalogSearch))
	router.HandleFunc("POST /api/v1/catalog:reindex", s.withQueryParameters(s.handleSemanticReindex))
	router.HandleFunc("GET /api/v1/catalog/indexed-record", s.withQueryParameters(s.handleIndexedCatalogRecord, "identifier", "versionPresent", "version"))
	router.HandleFunc("POST /api/v1/catalog/explore", s.withQueryParameters(s.handleCatalogExplore))
	router.HandleFunc("GET /api/v1/catalog/resources/{catalogRecordId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleCatalogRecordGet, "catalogRecordId")))
	router.HandleFunc("GET /api/v1/audit-events", s.withQueryParameters(s.handleAuditEvents, "actor", "action", "requestId", "targetType", "targetId", "occurredFrom", "occurredBefore", "limit", "cursor"))
	router.HandleFunc("POST /api/v1/audit-exports", s.withQueryParameters(s.handleAuditExport))

	router.HandleFunc("GET /api/v1/publication/hosts", s.withQueryParameters(s.handlePublicationHosts))
	router.HandleFunc("GET /api/v1/publication/snapshots", s.withQueryParameters(s.handlePublicationList, "host", "state", "limit", "cursor"))
	router.HandleFunc("POST /api/v1/publication/snapshots", s.withQueryParameters(s.handlePublicationBuild))
	router.HandleFunc("GET /api/v1/publication/snapshots/{snapshotId}", s.withQueryParameters(s.withUUIDPathParameters(s.handlePublicationGet, "snapshotId"), "host"))
	router.HandleFunc("POST /api/v1/publication/snapshots/{snapshotCommand}", s.withQueryParameters(s.dispatchPublicationCommand))
	router.HandleFunc("POST /api/v1/publication:unpublish", s.withQueryParameters(s.handlePublicationUnpublish))

	router.HandleFunc("POST /api/v1/federation/source-inspections", s.withQueryParameters(s.handleSourceInspect))
	router.HandleFunc("GET /api/v1/federation/source-inspections/{inspectionId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleSourceInspectionGet, "inspectionId")))
	router.HandleFunc("GET /api/v1/federation/sources", s.withQueryParameters(s.handleSourcesList, "limit", "cursor", "displayName", "status"))
	router.HandleFunc("POST /api/v1/federation/sources", s.withQueryParameters(s.handleSourceConnect))
	router.HandleFunc("GET /api/v1/federation/sources/{sourceId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleSourceGet, "sourceId")))
	router.HandleFunc("PATCH /api/v1/federation/sources/{sourceId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleSourcePatch, "sourceId")))
	router.HandleFunc("POST /api/v1/federation/sources/{sourceId}/change-previews", s.withQueryParameters(s.withUUIDPathParameters(s.handleSourceChangePreview, "sourceId")))
	router.HandleFunc("GET /api/v1/federation/sources/{sourceId}/snapshots", s.withQueryParameters(s.withUUIDPathParameters(s.handleSourceSnapshots, "sourceId"), "limit", "cursor"))
	router.HandleFunc("POST /api/v1/federation/sources/{sourceCommand}", s.withQueryParameters(s.dispatchSourceCommand))
	router.HandleFunc("GET /api/v1/federation/snapshots/{snapshotId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleSourceSnapshotGet, "snapshotId")))
	router.HandleFunc("GET /api/v1/federation/snapshots/{snapshotId}/comparison", s.withQueryParameters(s.withUUIDPathParameters(s.handleSourceSnapshotComparison, "snapshotId"), "otherSnapshotId", "view", "limit", "cursor"))
	router.HandleFunc("GET /api/v1/observations", s.withQueryParameters(s.handleObservations, "sourceId", "freshness", "limit", "cursor"))
	router.HandleFunc("GET /api/v1/observation-groups", s.withQueryParameters(s.handleObservationGroup, "identifier", "version", "versionPresent"))
	router.HandleFunc("GET /api/v1/observations/{observationId}/raw", s.withQueryParameters(s.withUUIDPathParameters(s.handleObservationRaw, "observationId")))
	router.HandleFunc("GET /api/v1/observations/{observationId}/occurrences", s.withQueryParameters(s.withUUIDPathParameters(s.handleObservationOccurrences, "observationId"), "limit", "cursor"))
	router.HandleFunc("POST /api/v1/observations/{observationCommand}", s.withQueryParameters(s.dispatchObservationCommand))
	router.HandleFunc("POST /api/v1/observations:search", s.withQueryParameters(s.handleObservationSearch))
	router.HandleFunc("POST /api/v1/federation/sources:live-search", s.withQueryParameters(s.handleLiveFederationSearch))

	router.HandleFunc("POST /api/v1/federation/sources/{sourceId}/trust-assessments", s.withQueryParameters(s.withUUIDPathParameters(s.handleTrustRequest, "sourceId")))
	router.HandleFunc("GET /api/v1/federation/sources/{sourceId}/trust-assessments/{jobId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleTrustRead, "sourceId", "jobId")))
	router.HandleFunc("GET /api/v1/jobs", s.withQueryParameters(s.handleJobsList, "limit", "cursor", "kind", "state", "parentJobId"))
	router.HandleFunc("GET /api/v1/jobs/{jobId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleJobGet, "jobId")))
	router.HandleFunc("POST /api/v1/jobs/{jobCommand}", s.withQueryParameters(s.dispatchJobCommand))
	if s.webhooks != nil {
		router.HandleFunc("GET /api/v1/overview/webhook-endpoints", s.withQueryParameters(s.handleOverviewWebhookEndpoints))
		router.HandleFunc("GET /api/v1/overview/webhook-deliveries", s.withQueryParameters(s.handleOverviewWebhookDeliveries))
		router.HandleFunc("GET /api/v1/webhooks/reconciliation", s.withQueryParameters(s.handleWebhookReconciliation))
		router.HandleFunc("GET /api/v1/webhooks", s.withQueryParameters(s.handleWebhooksList, "limit", "cursor", "name", "status"))
		router.HandleFunc("POST /api/v1/webhooks", s.withQueryParameters(s.handleWebhookCreate))
		router.HandleFunc("GET /api/v1/webhooks/{webhookId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleWebhookGet, "webhookId")))
		router.HandleFunc("PATCH /api/v1/webhooks/{webhookId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleWebhookPatch, "webhookId")))
		router.HandleFunc("DELETE /api/v1/webhooks/{webhookId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleWebhookDelete, "webhookId")))
		router.HandleFunc("GET /api/v1/webhooks/{webhookId}/deliveries", s.withQueryParameters(s.withUUIDPathParameters(s.handleWebhookDeliveries, "webhookId"), "limit", "cursor", "deliveryId", "eventId", "state"))
		router.HandleFunc("POST /api/v1/webhooks/{webhookCommand}", s.withQueryParameters(s.dispatchWebhookCommand))
	}

	router.HandleFunc("GET /.well-known/ard.json", s.withQueryParameters(s.handleARDManifest))
	router.HandleFunc("GET /.well-known/ai-catalog.json", s.withQueryParameters(s.handleAICatalog))
	router.HandleFunc("POST /ard/v1/search", s.withQueryParameters(s.handleARDSearch))
	router.HandleFunc("POST /ard/v1/explore", s.withQueryParameters(s.handleARDExplore))
	router.HandleFunc("GET /ard/v1/agents", s.withQueryParameters(s.handleARDList, "pageSize", "pageToken", "filter", "orderBy"))
	router.HandleFunc("GET /public/v1/artifacts/{artifactId}", s.withQueryParameters(s.handlePublicArtifact))
}

// dispatchVersionRevisionCommand accepts only :validate and validates its extracted revision UUID.
func (s *Server) dispatchVersionRevisionCommand(writer http.ResponseWriter, request *http.Request) {
	idValue, ok := strings.CutSuffix(request.PathValue("revisionCommand"), ":validate")
	if !ok || idValue == "" {
		writeProblem(writer, request, catalog.E(catalog.CodeNotFound, "version revision command was not found"))
		return
	}
	if !s.setUUIDPathValue(writer, request, "revisionId", idValue) {
		return
	}
	s.handleVersionRevisionValidate(writer, request)
}

// dispatchPublisherPublicationDomainCommand accepts only domain approval/revocation suffixes and exposes the exact domain to the handler.
func (s *Server) dispatchPublisherPublicationDomainCommand(writer http.ResponseWriter, request *http.Request) {
	value := request.PathValue("domainCommand")
	for suffix, handler := range map[string]http.HandlerFunc{
		":approve": s.handlePublisherPublicationDomainApprove,
		":revoke":  s.handlePublisherPublicationDomainRevoke,
	} {
		if domain, ok := strings.CutSuffix(value, suffix); ok && domain != "" {
			request.SetPathValue("domain", domain)
			handler(writer, request)
			return
		}
	}
	writeProblem(writer, request, catalog.E(catalog.CodeNotFound, "publisher publication-domain command was not found"))
}

// dispatchSubmissionCommand routes the supported review-decision suffix after validating the submission UUID.
func (s *Server) dispatchSubmissionCommand(writer http.ResponseWriter, request *http.Request) {
	value := request.PathValue("submissionCommand")
	for suffix, handler := range map[string]http.HandlerFunc{
		":approve":         s.handleSubmissionApprove,
		":request-changes": s.handleSubmissionRequestChanges,
		":reject":          s.handleSubmissionReject,
	} {
		if idValue, ok := strings.CutSuffix(value, suffix); ok && idValue != "" {
			if !s.setUUIDPathValue(writer, request, "submissionId", idValue) {
				return
			}
			handler(writer, request)
			return
		}
	}
	writeProblem(writer, request, catalog.E(catalog.CodeNotFound, "review submission command was not found"))
}

// dispatchListingCommand accepts only :publish and validates the target listing UUID.
func (s *Server) dispatchListingCommand(writer http.ResponseWriter, request *http.Request) {
	idValue, ok := strings.CutSuffix(request.PathValue("listingCommand"), ":publish")
	if !ok || idValue == "" {
		writeProblem(writer, request, catalog.E(catalog.CodeNotFound, "catalog listing command was not found"))
		return
	}
	if !s.setUUIDPathValue(writer, request, "listingId", idValue) {
		return
	}
	s.handleListingPublish(writer, request)
}

// dispatchReleaseCommand routes supported lifecycle suffixes after validating the release UUID.
func (s *Server) dispatchReleaseCommand(writer http.ResponseWriter, request *http.Request) {
	value := request.PathValue("releaseCommand")
	for suffix, handler := range map[string]http.HandlerFunc{
		":deprecate": s.handleReleaseDeprecate,
		":retire":    s.handleReleaseRetire,
		":supersede": s.handleReleaseSupersede,
		":revoke":    s.handleReleaseRevoke,
	} {
		if idValue, ok := strings.CutSuffix(value, suffix); ok && idValue != "" {
			if !s.setUUIDPathValue(writer, request, "releaseId", idValue) {
				return
			}
			handler(writer, request)
			return
		}
	}
	writeProblem(writer, request, catalog.E(catalog.CodeNotFound, "listing release command was not found"))
}

// dispatchPublicationCommand selects activation or rollback for a validated snapshot UUID.
func (s *Server) dispatchPublicationCommand(writer http.ResponseWriter, request *http.Request) {
	value := request.PathValue("snapshotCommand")
	for suffix, handler := range map[string]http.HandlerFunc{
		":activate": s.handlePublicationActivate,
		":rollback": s.handlePublicationRollback,
	} {
		if idValue, ok := strings.CutSuffix(value, suffix); ok && idValue != "" {
			if !s.setUUIDPathValue(writer, request, "snapshotId", idValue) {
				return
			}
			handler(writer, request)
			return
		}
	}
	writeProblem(writer, request, catalog.E(catalog.CodeNotFound, "publication snapshot command was not found"))
}

// dispatchRoleAssignmentCommand accepts only :revoke and validates the retained assignment UUID.
func (s *Server) dispatchRoleAssignmentCommand(writer http.ResponseWriter, request *http.Request) {
	idValue, ok := strings.CutSuffix(request.PathValue("assignmentCommand"), ":revoke")
	if !ok || idValue == "" {
		writeProblem(writer, request, catalog.E(catalog.CodeNotFound, "role assignment command was not found"))
		return
	}
	if s.setUUIDPathValue(writer, request, "assignmentId", idValue) {
		s.handleRoleAssignmentDelete(writer, request)
	}
}

// dispatchResourceCommand accepts only :delete and validates the target resource UUID.
func (s *Server) dispatchResourceCommand(writer http.ResponseWriter, request *http.Request) {
	idValue, ok := strings.CutSuffix(request.PathValue("resourceCommand"), ":delete")
	if !ok || idValue == "" {
		writeProblem(writer, request, catalog.E(catalog.CodeNotFound, "resource command was not found"))
		return
	}
	if s.setUUIDPathValue(writer, request, "resourceId", idValue) {
		s.handleResourceDelete(writer, request)
	}
}

// dispatchSourceCommand routes supported source actions after validating the source UUID.
func (s *Server) dispatchSourceCommand(writer http.ResponseWriter, request *http.Request) {
	value := request.PathValue("sourceCommand")
	for suffix, handler := range map[string]http.HandlerFunc{
		":sync":   s.handleSourceSync,
		":test":   s.handleSourceTest,
		":pause":  s.handleSourcePause,
		":resume": s.handleSourceResume,
		":remove": s.handleSourceRemove,
	} {
		if idValue, ok := strings.CutSuffix(value, suffix); ok && idValue != "" {
			if !s.setUUIDPathValue(writer, request, "sourceId", idValue) {
				return
			}
			handler(writer, request)
			return
		}
	}
	writeProblem(writer, request, catalog.E(catalog.CodeNotFound, "federation source command was not found"))
}

// dispatchJobCommand selects cancellation or retry for a validated job UUID.
func (s *Server) dispatchJobCommand(writer http.ResponseWriter, request *http.Request) {
	value := request.PathValue("jobCommand")
	for suffix, handler := range map[string]http.HandlerFunc{
		":cancel": s.handleJobCancel,
		":retry":  s.handleJobRetry,
	} {
		if idValue, ok := strings.CutSuffix(value, suffix); ok && idValue != "" {
			if !s.setUUIDPathValue(writer, request, "jobId", idValue) {
				return
			}
			handler(writer, request)
			return
		}
	}
	writeProblem(writer, request, catalog.E(catalog.CodeNotFound, "job command was not found"))
}

// dispatchWebhookCommand selects signing-key rotation or test delivery for a validated webhook UUID.
func (s *Server) dispatchWebhookCommand(writer http.ResponseWriter, request *http.Request) {
	value := request.PathValue("webhookCommand")
	for suffix, handler := range map[string]http.HandlerFunc{
		":rotate-secret": s.handleWebhookRotateSecret,
		":test":          s.handleWebhookTest,
	} {
		if idValue, ok := strings.CutSuffix(value, suffix); ok && idValue != "" {
			if !s.setUUIDPathValue(writer, request, "webhookId", idValue) {
				return
			}
			handler(writer, request)
			return
		}
	}
	writeProblem(writer, request, catalog.E(catalog.CodeNotFound, "webhook command was not found"))
}

type requestOperationObservation struct {
	operation observability.Operation
	outcome   observability.Outcome
}

type requestOperationKey struct{}

// observeRequestOperation records a domain-specific outcome for the request's fixed operation metric.
func observeRequestOperation(request *http.Request, operation observability.Operation, outcome observability.Outcome) {
	observeOperation(request.Context(), operation, outcome)
}

func observeOperation(ctx context.Context, operation observability.Operation, outcome observability.Outcome) {
	if observation, ok := ctx.Value(requestOperationKey{}).(*requestOperationObservation); ok {
		observation.operation = operation
		observation.outcome = outcome
	}
}

// metricsMiddleware records bounded route/status metrics and applies explicit successful domain outcomes.
func (s *Server) metricsMiddleware(router *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		startedAt := s.now()
		_, initialPattern := router.Handler(request)
		finish := s.observability.BeginHTTPFamily(activityFamily(request.Method, initialPattern))
		request.Pattern = initialPattern
		observation := &requestOperationObservation{}
		request = request.WithContext(context.WithValue(request.Context(), requestOperationKey{}, observation))
		problem := &problemObservation{}
		request = request.WithContext(context.WithValue(request.Context(), problemObservationKey{}, problem))
		capture := &statusWriter{ResponseWriter: writer}
		completed := false
		defer func() {
			status := capture.status
			if status == 0 {
				status = http.StatusOK
				if !completed {
					status = http.StatusInternalServerError
				}
			}
			pattern := request.Pattern
			if pattern == "" {
				pattern = initialPattern
			}
			finish(request.Method, pattern, status, capture.bytes, s.now().Sub(startedAt))
			s.logProblem(request, status, problem)
			if operation, ok := observedHTTPOperation(request, pattern); ok {
				outcome := httpOutcome(status)
				if !completed {
					outcome = observability.OutcomeFailed
				}
				if completed && status < 400 && observation.operation == operation && observation.outcome != "" {
					outcome = observation.outcome
				}
				s.observability.ObserveOperation(operation, outcome)
			}
		}()
		next.ServeHTTP(capture, request)
		completed = true
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

// Unwrap preserves access to optional interfaces through http.ResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// WriteHeader retains and forwards only the first response status.
func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

// resetResponse clears observed status/bytes only when the underlying response buffer can also reset.
func (w *statusWriter) resetResponse() bool {
	if resetter, ok := w.ResponseWriter.(interface{ resetResponse() bool }); ok && resetter.resetResponse() {
		w.status = 0
		w.bytes = 0
		return true
	}
	return false
}

// responseCommitted reports whether this wrapper has observed a response status or body write.
func (w *statusWriter) responseCommitted() bool { return w.status != 0 }

// Write records the implicit success status and the number of bytes actually accepted by the writer.
func (w *statusWriter) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	written, err := w.ResponseWriter.Write(value)
	w.bytes += int64(written)
	return written, err
}

// handleLive reports local process liveness without contacting dependencies.
func (s *Server) handleLive(writer http.ResponseWriter, request *http.Request) {
	s.respondJSON(writer, request, http.StatusOK, serviceStatusResponse{Status: "live"})
}

// handleReady checks local readiness and runs the injected dependency probe within two seconds.
func (s *Server) handleReady(writer http.ResponseWriter, request *http.Request) {
	if !s.ready.Load() {
		writeProblem(writer, request, catalog.E(catalog.CodeDependencyUnavailable, "service is not ready"))
		return
	}
	if s.readiness != nil {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if err := s.readiness(ctx); err != nil {
			s.readinessFailed.Store(true)
			if s.observability != nil {
				s.observability.UpdateDependency("postgresql", false)
				s.observability.ObserveOperation(observability.OperationReadiness, observability.OutcomeFailed)
			}
			diagnosticOwned(request.Context())
			s.readinessWarning.Warn(s.now(), s.logger, "readiness dependency check failed", requestLogAttributes(request, "event", "dependency.unavailable", "component", "readiness", "error_code", catalog.ErrorCodeOf(err), "error_type", fmt.Sprintf("%T", err))...)
			writeProblem(writer, request, catalog.E(catalog.CodeDependencyUnavailable, "service dependency is unavailable"))
			return
		}
		if s.readinessFailed.Swap(false) {
			s.logger.Info("readiness dependency recovered", "event", "dependency.recovered", "component", "readiness")
		}
	}
	if s.observability != nil {
		s.observability.UpdateDependency("postgresql", true)
		s.observability.ObserveOperation(observability.OperationReadiness, observability.OutcomeSucceeded)
	}
	s.respondJSON(writer, request, http.StatusOK, serviceStatusResponse{Status: "ready"})
}

// handleVersion returns build and standards-profile identifiers from process configuration.
func (s *Server) handleVersion(writer http.ResponseWriter, request *http.Request) {
	s.respondJSON(writer, request, http.StatusOK, serviceVersionResponse{Service: "open-agentic-catalog", Version: s.serviceVersion, Commit: s.commit, SourceState: s.sourceState, ARDProfile: s.ardProfile, AICatalogProfile: s.aiCatalogProfile})
}

// handleServiceInformation reads only this process's build and timing state.
// It must not call readiness or an aggregate metrics provider.
func (s *Server) handleServiceInformation(writer http.ResponseWriter, request *http.Request) {
	s.respondJSON(writer, request, http.StatusOK, serviceInformationResponse{
		serviceVersionResponse: serviceVersionResponse{Service: "open-agentic-catalog", Version: s.serviceVersion, Commit: s.commit, SourceState: s.sourceState, ARDProfile: s.ardProfile, AICatalogProfile: s.aiCatalogProfile},
		Snapshot:               s.process.Read(), Role: s.role,
	})
}

// handleMetrics renders provider-neutral metrics under a bounded context with caching disabled.
func (s *Server) handleMetrics(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	_, _ = writer.Write(s.observability.Render(ctx))
}

// observedHTTPOperation maps selected route patterns to the fixed operation vocabulary.
func observedHTTPOperation(request *http.Request, pattern string) (observability.Operation, bool) {
	switch {
	case request.Method == http.MethodPost && pattern == "POST /api/v1/catalog/search":
		return observability.OperationCatalogSearch, true
	case request.Method == http.MethodPost && pattern == "POST /api/v1/observations:search":
		return observability.OperationObservationSearch, true
	case request.Method == http.MethodPost && pattern == "POST /api/v1/federation/sources:live-search":
		return observability.OperationLiveSearch, true
	case request.Method == http.MethodPost && (pattern == "POST /api/v1/publication/snapshots/{snapshotCommand}" || pattern == "POST /api/v1/publication:unpublish"):
		return observability.OperationPublicationChange, true
	case request.Method == http.MethodPost && pattern == "POST /api/v1/version-revisions/{revisionCommand}" && strings.HasSuffix(request.PathValue("revisionCommand"), ":validate"):
		return observability.OperationValidation, true
	default:
		return "", false
	}
}

// httpOutcome classifies response status as success, rejection or failure for operation metrics.
func httpOutcome(status int) observability.Outcome {
	switch {
	case status >= 500:
		return observability.OutcomeFailed
	case status >= 400:
		return observability.OutcomeRejected
	default:
		return observability.OutcomeSucceeded
	}
}

// decodeJSON requires a bounded application/json object with exact control
// fields, non-null typed values and lossless Unicode.
func decodeJSON(writer http.ResponseWriter, request *http.Request, maximum int64, target any) error {
	return decodeJSONUsing(writer, request, maximum, target, jsondoc.Decode)
}

func decodeJSONUsing(writer http.ResponseWriter, request *http.Request, maximum int64, target any, decode func([]byte, any) error) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return catalog.E(catalog.CodeInvalidArgument, "request body requires Content-Type application/json")
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maximum)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return catalog.Wrap(catalog.CodeInvalidArgument, "request body exceeds the supported size", err)
	}
	if err := jsondoc.Validate(body); err != nil {
		return catalog.Wrap(catalog.CodeInvalidArgument, "request body is not unambiguous JSON", err)
	}
	// Every implemented JSON request has an object envelope. In particular,
	// encoding/json accepts null into a struct without reporting an error.
	if bytes.TrimSpace(body)[0] != '{' {
		return catalog.E(catalog.CodeInvalidArgument, "request body must be a JSON object")
	}
	if err := decode(body, target); err != nil {
		return catalog.Wrap(catalog.CodeInvalidArgument, "request body is not valid for this operation", err)
	}
	return nil
}

// expectedRevision requires the exact current revision ETag syntax in If-Match and parses its positive revision.
func expectedRevision(request *http.Request) (int64, error) {
	value := strings.TrimSpace(request.Header.Get("If-Match"))
	if value == "" {
		return 0, catalog.E(catalog.CodePreconditionFailed, "If-Match is required for this command")
	}
	match := revisionETag.FindStringSubmatch(value)
	if len(match) != 2 {
		return 0, catalog.E(catalog.CodePreconditionFailed, "If-Match must contain the current revision ETag")
	}
	revision, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, catalog.E(catalog.CodePreconditionFailed, "If-Match must contain the current revision ETag")
	}
	return revision, nil
}

// setETag encodes a domain revision in the control API's strong revision-tag format.
func setETag(writer http.ResponseWriter, revision int64) {
	writer.Header().Set("ETag", fmt.Sprintf(`"rev-%d"`, revision))
}

// parseLimit accepts one nonempty page limit in 1..100 or uses the caller's default when absent.
func parseLimit(request *http.Request, defaultValue int) (int, error) {
	query, err := parseQueryParameters(request)
	if err != nil {
		return 0, err
	}
	values, present := query["limit"]
	if !present {
		return defaultValue, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, catalog.E(catalog.CodeInvalidArgument, "limit must be provided once as an integer between 1 and 100")
	}
	value, err := strconv.Atoi(values[0])
	if err != nil || value <= 0 || value > 100 {
		return 0, catalog.E(catalog.CodeInvalidArgument, "limit must be an integer between 1 and 100")
	}
	return value, nil
}

// reasonBody decodes a closed reason object and trims its text before domain validation.
func reasonBody(writer http.ResponseWriter, request *http.Request, maximum int64) (string, error) {
	var input struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(writer, request, maximum, &input); err != nil {
		return "", err
	}
	return strings.TrimSpace(input.Reason), nil
}

// fail leaves final-response diagnostics to the owning middleware.
func (s *Server) fail(writer http.ResponseWriter, request *http.Request, err error) {
	writeProblem(writer, request, err)
}
