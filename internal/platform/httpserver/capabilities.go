// Capability responses project configured availability and the caller's
// authorization without promising unavailable providers.

package httpserver

import (
	"context"
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// CapabilityState distinguishes enabled, disabled and unavailable optional behavior.
type CapabilityState string

type publicationAuthorizationState string

type searchTier string

const (
	CapabilityEnabled     CapabilityState = "enabled"
	CapabilityDisabled    CapabilityState = "disabled"
	CapabilityUnavailable CapabilityState = "unavailable"

	publicationAuthorizationAdministratorApproved publicationAuthorizationState = "administrator_approved"

	searchTierCore searchTier = "core"
)

// CapabilitySnapshot carries composition-owned provider availability into the API projection.
type CapabilitySnapshot struct {
	ServiceActivity      CapabilityState
	SemanticSearch       CapabilityState
	CredentialResolution CapabilityState
	ManagedMedia         CapabilityState
	SoftwareAssessment   CapabilityState
	Webhooks             CapabilityState
}

type capabilitiesResponse struct {
	ResourceKinds        []catalog.Kind            `json:"resourceKinds"`
	Profiles             capabilityProfiles        `json:"profiles"`
	Operations           []string                  `json:"operations"`
	Governance           governanceCapabilities    `json:"governance"`
	Search               searchCapabilities        `json:"search"`
	OptionalCapabilities optionalCapabilities      `json:"optionalCapabilities"`
	Limits               capabilityLimits          `json:"limits"`
	ProfileBundle        profileBundleCapabilities `json:"profileBundle"`
	Degradations         []capabilityDegradation   `json:"degradations"`
}

type capabilityProfiles struct {
	Plugin    []string `json:"plugin"`
	ARD       []string `json:"ard"`
	AICatalog []string `json:"aiCatalog"`
	Agent     []string `json:"agent"`
	MCP       []string `json:"mcp"`
	Skill     []string `json:"skill"`
}

type governanceCapabilities struct {
	PublisherDomainVerification string                        `json:"publisherDomainVerification"`
	PublicationAuthorization    publicationAuthorizationState `json:"publicationAuthorization"`
	PublisherAuthority          catalog.TrustState            `json:"publisherAuthority"`
	LegalRights                 catalog.LegalRightsState      `json:"legalRights"`
}

type searchCapabilities struct {
	ProviderState string                       `json:"providerState,omitempty"`
	Indexing      *catalog.SemanticIndexStatus `json:"indexing,omitempty"`
	Tier          searchTier                   `json:"tier"`
	Profile       string                       `json:"profile"`
	Semantic      CapabilityState              `json:"semantic"`
	Degraded      bool                         `json:"degraded"`
}

type optionalCapabilities struct {
	ServiceActivity      CapabilityState `json:"serviceActivity"`
	CredentialResolution CapabilityState `json:"credentialResolution"`
	ManagedMedia         CapabilityState `json:"managedMedia"`
	SoftwareAssessment   CapabilityState `json:"softwareAssessment"`
	Webhooks             CapabilityState `json:"webhooks"`
}

type capabilityLimits struct {
	ValidationMaxFindings         int   `json:"validationMaxFindings"`
	ValidationMaxDiagnosticBytes  int   `json:"validationMaxDiagnosticBytes"`
	RequestBodyBytes              int64 `json:"requestBodyBytes"`
	CatalogDocumentBytes          int64 `json:"catalogDocumentBytes"`
	PluginDocumentBytes           int   `json:"pluginDocumentBytes"`
	PluginComponents              int   `json:"pluginComponents"`
	SkillPackageBytes             int64 `json:"skillPackageBytes"`
	SkillPackageExpandedBytes     int64 `json:"skillPackageExpandedBytes"`
	SkillPackageFileBytes         int64 `json:"skillPackageFileBytes"`
	SkillPackageFiles             int   `json:"skillPackageFiles"`
	SkillPackageDepth             int   `json:"skillPackageDepth"`
	SkillPackageExpansionRatio    int   `json:"skillPackageExpansionRatio"`
	CatalogPageSize               int   `json:"catalogPageSize"`
	ARDPageSize                   int   `json:"ardPageSize"`
	LiveFederationPageSize        int   `json:"liveFederationPageSize"`
	PublicationRecords            int   `json:"publicationRecords"`
	PublicationProjectionBytes    int   `json:"publicationProjectionBytes"`
	PublicationARDEntries         int   `json:"publicationARDEntries"`
	PublicationAICatalogEntries   int   `json:"publicationAICatalogEntries"`
	PublicationExclusions         int   `json:"publicationExclusions"`
	PublicationArtifactReferences int   `json:"publicationArtifactReferences"`
	PublicationDocumentBytes      int   `json:"publicationDocumentBytes"`
	PublicationSnapshotBytes      int   `json:"publicationSnapshotBytes"`
	PublicationWorkUnits          int   `json:"publicationWorkUnits"`
	PublicationConstructions      int   `json:"publicationConcurrentConstructions"`
}

type profileBundleCapabilities struct {
	Digest    string `json:"digest"`
	Artifacts int    `json:"artifacts"`
}

type capabilityDegradation struct {
	Component string `json:"component"`
	Reason    string `json:"reason"`
}

// handleCapabilities requires an authenticated actor and reports configured availability, profiles and hard limits.
func (s *Server) handleCapabilities(writer http.ResponseWriter, request *http.Request) {
	reply, err := s.ReadCapabilities(request.Context())
	s.respondRead(writer, request, reply, err)
}

func (s *Server) capabilitiesRead(ctx context.Context) (capabilitiesResponse, error) {
	operations := []string{"catalog.lifecycle", "catalog.search", "catalog.explore", "skill.package.inspect", "plugin.package.inspect", "skill.handoff", "publication.ard", "publication.ai_catalog", "publication.rollback", "registry.search", "registry.explore", "registry.list", "source.inspect", "source.configure", "source.test", "source.sync", "source.snapshot_history", "federation.live"}
	if s.capabilities.Webhooks == CapabilityEnabled {
		operations = append(operations, "webhooks.signed")
	}
	if s.mcp != nil {
		operations = append(operations, "catalog.mcp.http")
	}
	ardProfiles := make([]string, 0, 1)
	if s.ardProfile != "" {
		ardProfiles = append(ardProfiles, s.ardProfile)
	}
	aiCatalogProfiles := make([]string, 0, 1)
	if s.aiCatalogProfile != "" {
		aiCatalogProfiles = append(aiCatalogProfiles, s.aiCatalogProfile)
	}
	search := searchCapabilities{Tier: searchTierCore, Profile: catalog.CoreSearchProfile, Semantic: s.capabilities.SemanticSearch}
	if enabled, profile := s.catalog.SemanticConfiguration(ctx); enabled {
		search.Tier = searchTier("hybrid")
		search.Profile = profile
		search.Semantic = CapabilityEnabled
		search.ProviderState = s.catalog.SemanticProviderState(ctx)
		search.Degraded = search.ProviderState == "degraded"
	} else if search.Semantic == CapabilityEnabled {
		search.Semantic = CapabilityDisabled
	}
	actor, _ := identity.FromContext(ctx)
	_, reindexDenied := identity.RequirePermission(ctx, "operations.reindex", "")
	if search.Semantic == CapabilityEnabled && reindexDenied == nil && (!s.enforceOAuthScopes || actor.Claims["scope:tenant.admin"] == "true") {
		var err error
		search.Indexing, err = s.catalog.SemanticStatus(ctx)
		if err != nil {
			return capabilitiesResponse{}, err
		}
	}
	return capabilitiesResponse{
		ResourceKinds: []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin},
		Profiles: capabilityProfiles{
			Plugin:    []string{catalog.PluginProfile},
			ARD:       ardProfiles,
			AICatalog: aiCatalogProfiles,
			Agent:     []string{"a2a-agent-card-1.0.1-3303592", "open-agentic-catalog-agent-descriptor-1"},
			MCP:       []string{"mcp-registry-server-1.8.1-f52dc85", "mcp-server-card-v1-experimental-526201b", "open-agentic-catalog-mcp-registration-1"},
			Skill:     []string{"agent-skills-69ef37e"},
		},
		Operations: operations,
		Governance: governanceCapabilities{
			PublicationAuthorization:    publicationAuthorizationAdministratorApproved,
			PublisherAuthority:          catalog.TrustNotAssessed,
			PublisherDomainVerification: s.catalog.PublisherDomainVerificationPolicy().Mode,
			LegalRights:                 catalog.LegalRightsNotAssessed,
		},
		Search: search,
		OptionalCapabilities: optionalCapabilities{
			ServiceActivity:      s.capabilities.ServiceActivity,
			CredentialResolution: s.capabilities.CredentialResolution,
			ManagedMedia:         s.capabilities.ManagedMedia,
			SoftwareAssessment:   s.capabilities.SoftwareAssessment,
			Webhooks:             s.capabilities.Webhooks,
		},
		Limits: capabilityLimits{
			RequestBodyBytes:              s.maxBodyBytes,
			CatalogDocumentBytes:          catalog.MaxCatalogDocumentBytes,
			ValidationMaxFindings:         s.catalog.ValidationLimits().MaxFindings,
			ValidationMaxDiagnosticBytes:  s.catalog.ValidationLimits().MaxDiagnosticBytes,
			PluginDocumentBytes:           catalog.MaxPluginDocumentBytes,
			PluginComponents:              catalog.MaxPluginComponents,
			SkillPackageBytes:             catalog.MaxSkillPackageBytes,
			SkillPackageExpandedBytes:     catalog.MaxSkillPackageExpandedBytes,
			SkillPackageFileBytes:         catalog.MaxSkillPackageFileBytes,
			SkillPackageFiles:             catalog.MaxSkillPackageFiles,
			SkillPackageDepth:             catalog.MaxSkillPackageDepth,
			SkillPackageExpansionRatio:    catalog.MaxSkillPackageExpansionRatio,
			CatalogPageSize:               catalog.MaxCatalogPageSize,
			ARDPageSize:                   publication.MaxARDPageSize,
			LiveFederationPageSize:        federation.MaxLiveSearchPageSize,
			PublicationRecords:            catalog.MaxPublicationRecords,
			PublicationProjectionBytes:    catalog.MaxPublicationProjectionBytes,
			PublicationARDEntries:         publication.MaxPublicationARDEntries,
			PublicationAICatalogEntries:   publication.MaxPublicationAICatalogEntries,
			PublicationExclusions:         publication.MaxPublicationExclusions,
			PublicationArtifactReferences: publication.MaxPublicationArtifactReferences,
			PublicationDocumentBytes:      publication.MaxPublicationDocumentBytes,
			PublicationSnapshotBytes:      publication.MaxPublicationSnapshotBytes,
			PublicationWorkUnits:          publication.MaxPublicationWorkUnits,
			PublicationConstructions:      publication.MaxConcurrentConstructions,
		},
		ProfileBundle: profileBundleCapabilities{Digest: s.profileBundleDigest, Artifacts: s.profileArtifactCount},
		Degradations:  []capabilityDegradation{},
	}, nil
}
