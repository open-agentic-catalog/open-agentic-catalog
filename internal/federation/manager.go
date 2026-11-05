// Federation management applies administrator-controlled topology to
// inspection, onboarding, synchronization and indexed/live discovery.

package federation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

type Fetcher interface {
	Fetch(context.Context, fetcher.Request) (fetcher.Result, error)
	PolicyDigest() string
}

type Clock interface{ Now() time.Time }
type IDGenerator interface {
	New(time.Time) (string, error)
}

type ObservationAdopter interface {
	AdoptExternalObservation(context.Context, catalog.ExternalObservationAdoptionInput) (catalog.ExternalObservationAdoption, error)
}

const (
	ardProfileID                 = "ard-0.91-aa3e598"
	ardDocumentMediaType         = "application/ard+json"
	aiCatalogDocumentMediaType   = "application/ai-catalog+json"
	MaxLiveSearchPageSize        = 50
	MaxLivePagesPerSource        = 5
	MaxLiveDiagnostics           = 32
	MaxDirectLiveSources         = 8
	MaxFederatedSources          = 12
	MaxSynchronizationEntries    = 10000
	MaxReferralTopologySources   = 256
	MaxReferralHops              = 2
	MaxLiveResponseBytes         = 8 << 20
	MaxLiveSearchTimeout         = 5 * time.Second
	DefaultLiveSearchLimit       = 2 * time.Second
	MaxCatalogSearchAttributions = 16
)

// Manager applies administrator-owned federation topology and safety limits to
// source inspection, synchronization, indexed observations, and live search.
type Manager struct {
	trust                       TrustVerifier
	store                       Store
	fetcher                     Fetcher
	clock                       Clock
	ids                         IDGenerator
	maxDepth                    int
	maxFetches                  int
	maxEntries                  int
	maxFanout                   int
	maxHops                     int
	maxLivePagesPerSource       int
	liveTimeout                 time.Duration
	targets                     map[string]DiscoveryTarget
	adopter                     ObservationAdopter
	evidence                    EvidenceSink
	acknowledgeOutboxAtCreation bool
}

type DiscoveryTarget struct {
	Host     string
	TenantID string
	Audience DiscoveryAudience
}

// ManagerOption configures a bounded federation capability at construction.
type ManagerOption func(*Manager) error

type sourceAccess struct {
	tenantID             string
	networkProfile       string
	credentialBinding    string
	credentialOrigin     string
	expectedPolicyDigest string
	credentialRevision   *string
	evidenceError        *error
}

// request binds tenant and network policy to outbound work and attaches
// credentials only at the approved credential origin.
func (a sourceAccess) request(endpoint string, request fetcher.Request) fetcher.Request {
	request.URL = endpoint
	request.TenantID = a.tenantID
	request.NetworkProfile = a.networkProfile
	request.ExpectedPolicyDigest = a.expectedPolicyDigest
	if a.credentialBinding != "" && normalizedOrigin(endpoint) == a.credentialOrigin {
		request.CredentialBinding = a.credentialBinding
	}
	return request
}

// accessForSource derives operation access from the source and can require its
// exact approved policy digest.
func accessForSource(source Source, requireApprovedPolicy bool) sourceAccess {
	access := newSourceAccess(source.TenantID, source.NetworkProfile, source.CredentialBinding, source.Endpoint)
	if requireApprovedPolicy {
		access.expectedPolicyDigest = source.PolicyDigest
		if access.expectedPolicyDigest == "" {
			access.expectedPolicyDigest = "unapproved-source-policy"
		}
	}
	return access
}

// newSourceAccess initializes shared credential-revision and first-error
// tracking for one source operation.
func newSourceAccess(tenantID, networkProfile, credentialBinding, credentialEndpoint string) sourceAccess {
	revision := ""
	var evidenceError error
	return sourceAccess{tenantID: tenantID, networkProfile: networkProfile, credentialBinding: credentialBinding, credentialOrigin: normalizedOrigin(credentialEndpoint), credentialRevision: &revision, evidenceError: &evidenceError}
}

// operationError returns the first retained safety-evidence error from the
// access operation.
func (a sourceAccess) operationError() error {
	if a.evidenceError == nil {
		return nil
	}
	return *a.evidenceError
}

// recordOperationError preserves the first nonnil safety-evidence error across
// related fetches.
func (a sourceAccess) recordOperationError(err error) {
	if err != nil && a.evidenceError != nil && *a.evidenceError == nil {
		*a.evidenceError = err
	}
}

// normalizedOrigin uses the shared fetch boundary's origin normalization and
// returns empty for invalid input.
func normalizedOrigin(endpoint string) string {
	origin, err := fetcher.NormalizeOrigin(endpoint, true)
	if err != nil {
		return ""
	}
	return origin
}

// validateAccessReferences validates optional opaque network-profile and
// credential-binding names without resolving secrets.
func validateAccessReferences(networkProfile, credentialBinding string) error {
	if networkProfile != "" && !validProfileReference(networkProfile) {
		return catalog.E(catalog.CodeInvalidArgument, "source network profile reference is invalid")
	}
	if credentialBinding != "" && !validCredentialReference(credentialBinding) {
		return catalog.E(catalog.CodeInvalidArgument, "source credential binding reference is invalid")
	}
	return nil
}

// validProfileReference accepts a bounded lowercase profile label with interior
// hyphens.
func validProfileReference(value string) bool {
	if value == "" || len(value) > 63 || !lowerAlphaNumeric(rune(value[0])) {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' && index > 0 && index < len(value)-1 {
			continue
		}
		return false
	}
	return true
}

// lowerAlphaNumeric reports whether a rune is a lowercase ASCII letter or
// digit.
func lowerAlphaNumeric(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

// validCredentialReference accepts a bounded opaque credential label without
// path separators or dot traversal names.
func validCredentialReference(value string) bool {
	if value == "" || len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// WithExecutionLimits sets validated upper bounds for approved live fan-out and
// referral hops.
func WithExecutionLimits(maxFanout, maxHops int) ManagerOption {
	return func(manager *Manager) error {
		if maxFanout < 1 || maxFanout > MaxFederatedSources || maxHops < 0 || maxHops > MaxReferralHops {
			return catalog.E(catalog.CodeInvalidArgument, "federation execution limits are invalid")
		}
		manager.maxFanout = maxFanout
		manager.maxHops = maxHops
		return nil
	}
}

// WithLiveSearchTimeout bounds the complete live-federation traversal rather
// than giving each sequential source a fresh request-sized budget.
func WithLiveSearchTimeout(timeout time.Duration) ManagerOption {
	return func(manager *Manager) error {
		if timeout <= 0 || timeout > MaxLiveSearchTimeout {
			return catalog.E(catalog.CodeInvalidArgument, "live federation timeout must be positive")
		}
		manager.liveTimeout = timeout
		return nil
	}
}

// WithLiveSearchPages bounds page fetches per peer, including the initial page.
// The default is one; tokens and candidates remain local to one live request.
func WithLiveSearchPages(pages int) ManagerOption {
	return func(manager *Manager) error {
		if pages < 1 || pages > MaxLivePagesPerSource {
			return catalog.E(catalog.CodeInvalidArgument, "live federation pages per source must be between 1 and 5")
		}
		manager.maxLivePagesPerSource = pages
		return nil
	}
}

// WithDiscoveryTargets configures the operator-selected host/audience targets
// available for source assignment.
func WithDiscoveryTargets(targets []DiscoveryTarget) ManagerOption {
	return func(manager *Manager) error {
		for _, target := range targets {
			host := strings.ToLower(strings.TrimSpace(target.Host))
			if host == "" || target.TenantID == "" || target.Audience != AudienceAnonymous && target.Audience != AudienceAuthenticated {
				return catalog.E(catalog.CodeInvalidArgument, "federation discovery target is invalid")
			}
			if _, exists := manager.targets[host]; exists {
				return catalog.E(catalog.CodeInvalidArgument, "federation discovery target hosts must be unique")
			}
			target.Host = host
			manager.targets[host] = target
		}
		return nil
	}
}

// WithObservationAdopter attaches the explicit local-adoption port without
// granting discovered observations local governance.
func WithObservationAdopter(adopter ObservationAdopter) ManagerOption {
	return func(manager *Manager) error {
		if adopter == nil {
			return catalog.E(catalog.CodeInvalidArgument, "observation adopter is required")
		}
		manager.adopter = adopter
		return nil
	}
}

// WithEvidenceSink enables the reviewed durable audit and public-notification
// inventory. The boolean makes public rows terminal at creation when webhook
// delivery is deliberately disabled.
func WithEvidenceSink(sink EvidenceSink, acknowledgeOutboxAtCreation bool) ManagerOption {
	return func(manager *Manager) error {
		if sink == nil {
			return catalog.E(catalog.CodeInvalidArgument, "federation evidence sink is required")
		}
		manager.evidence = sink
		manager.acknowledgeOutboxAtCreation = acknowledgeOutboxAtCreation
		return nil
	}
}

// NewManager validates source persistence, hardened fetching, clock and ID
// dependencies and applies explicit federation options.
func NewManager(store Store, outbound Fetcher, clock Clock, ids IDGenerator, options ...ManagerOption) (*Manager, error) {
	if store == nil || outbound == nil || clock == nil || ids == nil {
		return nil, catalog.E(catalog.CodeInvalidArgument, "federation manager dependencies are required")
	}
	if !validPolicyDigest(outbound.PolicyDigest()) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "federation fetcher must expose a valid active policy digest")
	}
	manager := &Manager{store: store, fetcher: outbound, clock: clock, ids: ids, maxDepth: 4, maxFetches: maxSyncRequests, maxEntries: MaxSynchronizationEntries, maxFanout: 3, maxHops: MaxReferralHops, maxLivePagesPerSource: 1, liveTimeout: DefaultLiveSearchLimit, targets: make(map[string]DiscoveryTarget)}
	for _, option := range options {
		if option == nil {
			return nil, catalog.E(catalog.CodeInvalidArgument, "federation manager option is required")
		}
		if err := option(manager); err != nil {
			return nil, err
		}
	}
	return manager, nil
}

// Inspect starts an administrator-authorized source inspection with default
// access references.
func (m *Manager) Inspect(ctx context.Context, endpoint string) (response Inspection, resultErr error) {
	ctx, endTrace := catalog.StartTrace(ctx, "federation inspect", catalog.TraceInternal, "")
	defer func() {
		outcome := "ok"
		if resultErr != nil {
			outcome = "error"
		}
		endTrace(outcome)
	}()
	return m.InspectWithAccess(ctx, InspectionInput{URL: endpoint})
}

// InspectWithAccess validates explicit access references and retains the
// resolved endpoint/profile evidence or an attributed inspection failure.
func (m *Manager) InspectWithAccess(ctx context.Context, input InspectionInput) (Inspection, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.inspect")
	if err != nil {
		return Inspection{}, err
	}
	input.URL = normalizeInspectionURL(input.URL)
	input.NetworkProfile = strings.TrimSpace(input.NetworkProfile)
	input.CredentialBinding = strings.TrimSpace(input.CredentialBinding)
	if err := validateAccessReferences(input.NetworkProfile, input.CredentialBinding); err != nil {
		return Inspection{}, err
	}
	configurationDigest, err := inspectionConfigurationDigest(input)
	if err != nil {
		return Inspection{}, catalog.Wrap(catalog.CodeInternal, "digest source inspection configuration", err)
	}
	now := m.clock.Now()
	inspectionID, err := m.ids.New(now)
	if err != nil {
		return Inspection{}, catalog.Wrap(catalog.CodeInternal, "create source inspection identifier", err)
	}
	access := newSourceAccess(actor.TenantID, input.NetworkProfile, input.CredentialBinding, input.URL)
	discovered, err := m.discover(ctx, input.URL, access, "")
	if err != nil {
		outcome := classifyOutboundOutcome(err)
		if outcome == outboundOutcomeInvalidInput {
			return Inspection{}, catalog.Wrap(catalog.CodeInvalidArgument, "source inspection target is invalid", err)
		}
		return Inspection{}, m.retainInspectionFailure(ctx, actor, inspectionID, input.NetworkProfile, configurationDigest, outcome, err, now)
	}
	result := discovered.result
	if input.CredentialBinding != "" && normalizedOrigin(result.FinalURL) != access.credentialOrigin {
		failure := catalog.E(catalog.CodeSourcePolicyDenied, "credentialed discovery resolved another origin; inspect that endpoint and its credential binding independently")
		return Inspection{}, m.retainInspectionFailure(ctx, actor, inspectionID, input.NetworkProfile, configurationDigest, outboundOutcomePolicyDenied, failure, now)
	}
	summary := summarizeDiscovery(discovered)
	if candidate := registryListCandidate(discovered); summary.sourceType == SourceRegistry && candidate != "" {
		listURL, probeFinding := m.probeRegistryList(ctx, access, candidate)
		if listURL != "" {
			summary.registryListURL = listURL
			summary.capabilities["enumerable"] = true
			summary.capabilities["list"] = true
		} else if probeFinding.Code != "" {
			summary.warnings = append(summary.warnings, probeFinding)
		}
	}
	if err := access.operationError(); err != nil {
		failure := catalog.Wrap(catalog.CodeDependencyUnavailable, "verify source inspection evidence", err)
		return Inspection{}, m.retainInspectionFailure(ctx, actor, inspectionID, input.NetworkProfile, configurationDigest, classifyOutboundOutcome(err), failure, now)
	}
	inspection := Inspection{ID: inspectionID, TenantID: actor.TenantID, RequestedURL: input.URL, ResolvedURL: result.FinalURL, SourceType: summary.sourceType, ProfileID: summary.profileID, MediaType: result.MediaType, BodyDigest: result.BodyDigest, EntryCount: summary.entryCount, KindCounts: summary.kindCounts, Publishers: summary.publishers, Tags: summary.tags, Capabilities: summary.capabilities, RegistrySearchURL: summary.registrySearchURL, RegistryListURL: summary.registryListURL, NetworkProfile: input.NetworkProfile, CredentialBinding: input.CredentialBinding, PolicyID: result.PolicyID, PolicyRevision: result.PolicyRevision, PolicyDigest: result.PolicyDigest, CredentialRevision: result.CredentialRevision, Warnings: summary.warnings, ExpiresAt: now.Add(15 * time.Minute), CreatedAt: now}
	digest := inspectionDigest(inspection)
	evidence, err := m.mutationEvidence(ctx, actor, audit.ActionSourceInspected, "source_inspection", inspection.ID, "", sourceInspectionFacts{Outcome: outboundOutcomeSucceeded, OriginClass: originClass(input.NetworkProfile), PolicyDigest: result.PolicyDigest, ConfigurationDigest: configurationDigest, InspectionDigest: digest, ExpiresAt: inspection.ExpiresAt.UTC().Format(time.RFC3339Nano)}, "", digest, "", "", now)
	if err != nil {
		return Inspection{}, err
	}
	return m.store.SaveInspection(ctx, inspection, evidence)
}

// retainInspectionFailure records bounded attribution and configuration
// evidence for an unsuccessful administrator inspection.
func (m *Manager) retainInspectionFailure(ctx context.Context, actor identity.Actor, inspectionID, networkProfile, configurationDigest, outcome string, operationErr error, at time.Time) error {
	record, err := m.auditRecord(ctx, actor, audit.ActionSourceInspected, "source_inspection", inspectionID, "", sourceInspectionFacts{Outcome: outcome, OriginClass: originClass(networkProfile), PolicyDigest: m.fetcher.PolicyDigest(), ConfigurationDigest: configurationDigest}, at)
	if err != nil {
		return err
	}
	if record.ID != "" {
		if err := m.evidence.AppendAuditEvent(ctx, record); err != nil {
			return catalog.Wrap(catalog.CodeDependencyUnavailable, "retain source inspection audit evidence", err)
		}
		return catalog.PreserveEvidence(operationErr)
	}
	return operationErr
}

type discovery struct {
	result            fetcher.Result
	document          parsedDocument
	registrySearchURL string
	warnings          []catalog.Finding
}

type discoverySummary struct {
	sourceType        SourceType
	profileID         string
	registrySearchURL string
	registryListURL   string
	entryCount        int
	kindCounts        map[string]int
	publishers        []string
	tags              []string
	capabilities      map[string]bool
	warnings          []catalog.Finding
}

type discoveryFailure struct {
	outcome  string
	cause    error
	evidence outboundEvidence
	rank     int
}

// record considers an ordinary discovery failure without retained HTTP
// evidence.
func (failure *discoveryFailure) record(outcome string, cause error) {
	failure.recordWithEvidence(outcome, cause, outboundEvidence{}, false)
}

// recordResult considers an ordinary discovery failure with a bounded
// fetch-result projection.
func (failure *discoveryFailure) recordResult(outcome string, cause error, result fetcher.Result) {
	failure.recordWithEvidence(outcome, cause, boundedOutboundEvidence(result), false)
}

// recordWithEvidence retains the highest-ranked failure, prioritizing explicit
// primary-interface failures over fallback attempts.
func (failure *discoveryFailure) recordWithEvidence(outcome string, cause error, evidence outboundEvidence, primary bool) {
	rank := map[string]int{
		outboundOutcomeParseError:     1,
		outboundOutcomeNegative:       2,
		outboundOutcomeHTTPError:      3,
		outboundOutcomeTransportError: 4,
		outboundOutcomeInvalidInput:   5,
		outboundOutcomePolicyDenied:   6,
	}[outcome]
	if primary {
		rank += 100
	}
	if cause != nil && rank > failure.rank {
		failure.outcome, failure.cause, failure.evidence, failure.rank = outcome, cause, evidence, rank
	}
}

// recordPrimary gives the selected interface's failure precedence over fallback
// discovery errors.
func (failure *discoveryFailure) recordPrimary(outcome string, cause error) {
	failure.recordWithEvidence(outcome, cause, outboundEvidence{}, true)

}

// recordPrimaryResult preserves bounded response evidence for a
// selected-interface failure.
func (failure *discoveryFailure) recordPrimaryResult(outcome string, cause error, result fetcher.Result) {
	failure.recordWithEvidence(outcome, cause, boundedOutboundEvidence(result), true)
}

// err maps the retained failure to a safe domain classification while
// preserving its diagnostic cause and bounded evidence.
func (failure *discoveryFailure) err() error {
	if failure.cause == nil {
		return nil
	}
	code := catalog.CodeInvalidArgument
	switch failure.outcome {
	case outboundOutcomePolicyDenied:
		code = catalog.CodeSourcePolicyDenied
	case outboundOutcomeTransportError:
		code = catalog.CodeDependencyUnavailable
	}
	wrapped := catalog.Wrap(code, "source did not expose a compatible discovery interface", failure.cause)
	if failure.evidence != (outboundEvidence{}) {
		return &classifiedOutboundError{outcome: failure.outcome, evidence: failure.evidence, err: wrapped}
	}
	return withOutboundOutcome(wrapped, failure.outcome)
}

// registryListCandidate returns the sole unambiguous Registry List endpoint
// advertised by the discovered entries.
func registryListCandidate(discovered discovery) string {
	candidates := make([]string, 0, 2)
	for _, entry := range discovered.document.entries {
		if entry.Type != "application/ai-registry+json" && entry.Type != "application/ai-registry" {
			continue
		}
		candidate := registryListEndpoint(entry.URL)
		if candidate != "" && !slices.Contains(candidates, candidate) {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return ""
}

// summarizeDiscovery projects resolved profile and Registry interface
// capabilities from the inspected document.
func summarizeDiscovery(discovered discovery) discoverySummary {
	document := discovered.document
	summary := discoverySummary{
		sourceType:   document.sourceType,
		profileID:    document.profileID,
		entryCount:   len(document.entries),
		kindCounts:   make(map[string]int),
		capabilities: map[string]bool{"enumerable": document.sourceType != SourceRegistry},
	}
	addWarning := func(finding catalog.Finding) {
		if len(summary.warnings) < 99 {
			summary.warnings = append(summary.warnings, finding)
			return
		}
		if len(summary.warnings) == 99 {
			summary.warnings = append(summary.warnings, catalog.Finding{Code: "SOURCE_WARNINGS_TRUNCATED", Severity: catalog.SeverityWarning, Message: "Additional source inspection warnings were omitted from this bounded report."})
		}
	}
	for _, finding := range discovered.warnings {
		addWarning(finding)
	}
	publishers := make(map[string]struct{})
	tags := make(map[string]struct{})
	registryEndpoints := make([]string, 0, 2)
	if discovered.registrySearchURL != "" {
		registryEndpoints = append(registryEndpoints, discovered.registrySearchURL)
	}
	for entryIndex, entry := range document.entries {
		for _, finding := range entry.StructuralFindings {
			finding.Path = fmt.Sprintf("/entries/%d%s", entryIndex, finding.Path)
			addWarning(finding)
		}
		if entry.Type == "application/ai-registry+json" || entry.Type == "application/ai-registry" {
			if entry.Type == "application/ai-registry" {
				addWarning(catalog.Finding{Code: "ARD_REGISTRY_MEDIA_TYPE_ALIAS", Severity: catalog.SeverityWarning, Message: "The Registry descriptor uses the legacy unsuffixed media type alias."})
			}
			registryEndpoint := registrySearchEndpoint(entry.URL)
			if registryEndpoint == "" {
				addWarning(catalog.Finding{Code: "ARD_REGISTRY_BASE_INVALID", Severity: catalog.SeverityWarning, Message: "A Registry descriptor does not expose a usable HTTPS operational base URL."})
			} else if !slices.Contains(registryEndpoints, registryEndpoint) {
				registryEndpoints = append(registryEndpoints, registryEndpoint)
			}
			if len(document.entries) == 1 {
				summary.sourceType = SourceRegistry
				summary.capabilities["enumerable"] = false
			} else {
				summary.sourceType = SourceHybrid
			}
			continue
		}
		summary.kindCounts[string(kindForMediaType(entry.Type))]++
		if parsed, parseErr := catalog.ParseAIRIdentifier(entry.Identifier); parseErr == nil {
			publishers[parsed.Publisher] = struct{}{}
		}
		for _, tag := range entry.Tags {
			tags[tag] = struct{}{}
		}
	}
	if len(registryEndpoints) == 1 {
		summary.registrySearchURL = registryEndpoints[0]
		summary.capabilities["search"] = true
	} else if len(registryEndpoints) > 1 {
		addWarning(catalog.Finding{Code: "ARD_MULTIPLE_REGISTRY_ENDPOINTS", Severity: catalog.SeverityWarning, Message: "The source advertises multiple Registry endpoints; choose and inspect one independently before enabling live federation."})
	}
	summary.publishers = sortedKeys(publishers)
	summary.tags = sortedKeys(tags)
	return summary
}

// sourceInterfaceChanged detects disagreement between retained source
// connectivity and the newly resolved interface.
func sourceInterfaceChanged(source Source, result fetcher.Result, summary discoverySummary) bool {
	return result.FinalURL != source.Endpoint || sourceDiscoveryContractChanged(source, summary)
}

// sourceDiscoveryContractChanged compares retained discovery profile/interface
// bindings against the inspected summary.
func sourceDiscoveryContractChanged(source Source, summary discoverySummary) bool {
	return summary.sourceType != source.Type || summary.profileID != source.ProfileID || summary.registrySearchURL != source.RegistrySearchURL || summary.registryListURL != source.RegistryListURL
}

// discover resolves one configured source through the normative well-known
// location and explicitly advertised fallbacks. Every candidate still crosses
// the same approved outbound boundary before it is parsed.
func (m *Manager) discover(ctx context.Context, endpoint string, access sourceAccess, expectedProfile string) (discovery, error) {
	var failures discoveryFailure
	profileID := ardProfileID
	if run := syncRunFromContext(ctx); run != nil && run.source.ProfileID != "" {
		profileID = run.source.ProfileID
	} else if expectedProfile != "" {
		profileID = expectedProfile
	}
	domainResolution := isDomainResolutionTarget(endpoint)
	normativeWellKnown := ""
	if domainResolution {
		base, baseErr := urlBase(endpoint)
		if baseErr == nil {
			normativeWellKnown = base + "/.well-known/ard.json"
			result, accept, fetchErr := m.fetchForParsing(ctx, access, normativeWellKnown, profileID, fetcher.Request{Accept: []string{ardDocumentMediaType, aiCatalogDocumentMediaType, "application/json"}}, true, false)
			switch {
			case fetchErr != nil:
				failures.recordPrimary(classifyOutboundOutcome(fetchErr), fetchErr)
			case result.Status != http.StatusOK:
				failures.recordPrimaryResult(outboundOutcomeHTTPError, fmt.Errorf("normative discovery returned HTTP %d", result.Status), result)
				accept(false)
			default:
				mediaType, classificationErr := effectiveDiscoveryMediaType(result.MediaType, ardDocumentMediaType, expectedProfile)
				if classificationErr != nil {
					failures.recordPrimaryResult(outboundOutcomeParseError, classificationErr, result)
					accept(false)
					break
				}
				document, parseErr := m.parseDocumentWithAccess(ctx, result.Body, mediaType, access)
				if parseErr != nil {
					failures.recordPrimaryResult(outboundOutcomeParseError, parseErr, result)
					accept(false)
					break
				}
				accept(true)
				return completeDiscovery(access, discovery{result: result, document: document, warnings: []catalog.Finding{{Code: "ARD_DISCOVERY_RESOLVED", Severity: catalog.SeverityInfo, Message: "The source Catalog was resolved through its normative ARD well-known location."}}})
			}
		}
	}
	initial, acceptInitial, err := m.fetchForParsing(ctx, access, endpoint, profileID, fetcher.Request{Accept: []string{ardDocumentMediaType, aiCatalogDocumentMediaType, "application/json", "text/html;q=0.5"}}, true, false)
	if err != nil {
		outcome := classifyOutboundOutcome(err)
		code := catalog.CodeDependencyUnavailable
		if outcome == outboundOutcomePolicyDenied {
			code = catalog.CodeSourcePolicyDenied
		} else if outcome == outboundOutcomeInvalidInput {
			code = catalog.CodeInvalidArgument
		}
		return discovery{}, withOutboundOutcome(catalog.Wrap(code, "inspect source", err), outcome)
	}
	var initialDocument *parsedDocument
	var embedded embeddedDiscovery
	var htmlLinks []discoveryLocation
	if initial.Status == http.StatusOK {
		mediaType, classificationErr := effectiveDiscoveryMediaType(initial.MediaType, discoveryURLMediaType(endpoint), expectedProfile)
		if classificationErr != nil {
			failures.recordPrimaryResult(outboundOutcomeParseError, classificationErr, initial)
		} else {
			if document, parseErr := m.parseDocumentWithAccess(ctx, initial.Body, mediaType, access); parseErr == nil {
				if !domainResolution {
					acceptInitial(true)
					return completeDiscovery(access, discovery{result: initial, document: document})
				}
				initialDocument = &document
			} else {
				failures.recordPrimaryResult(outboundOutcomeParseError, parseErr, initial)
			}
		}
		embedded, htmlLinks = htmlDiscovery(initial.Body, initial.FinalURL)
	} else {
		failures.recordPrimaryResult(outboundOutcomeHTTPError, fmt.Errorf("source discovery returned HTTP %d", initial.Status), initial)
	}
	// A selected Search route may reject GET while a same-origin well-known
	// manifest advertises several registries. Probe that choice before fallback
	// discovery; valid directly selected documents and domain discovery above
	// retain precedence. Failed probes must not become successful inspections.
	selected, parseErr := url.Parse(endpoint)
	if parseErr == nil && !domainResolution && initial.Status != http.StatusOK && strings.HasSuffix(strings.TrimSuffix(selected.Path, "/"), "/search") {
		acceptInitial(false)
		return m.probeRegistrySearch(ctx, endpoint, access)
	}
	candidates := make([]discoveryLocation, 0)
	discoveryBaseURL := initial.FinalURL
	if domainResolution {
		discoveryBaseURL = endpoint
	}
	if base, parseErr := urlBase(discoveryBaseURL); parseErr == nil && !domainResolution {
		candidates = append(candidates, discoveryLocation{URL: base + "/.well-known/ard.json", MediaType: ardDocumentMediaType})
	}
	candidates = append(candidates, discoveryLinks(initial.Links, initial.FinalURL)...)
	candidates = append(candidates, htmlLinks...)
	if base, parseErr := urlBase(discoveryBaseURL); parseErr == nil {
		candidates = append(candidates, discoveryLocation{URL: base + "/.well-known/ai-catalog.json", MediaType: "application/json"})
	}
	candidates = uniqueDiscoveryLocations(candidates, endpoint, normativeWellKnown)
	for _, candidate := range candidates {
		result, acceptCandidate, fetchErr := m.fetchForParsing(ctx, access, candidate.URL, profileID, fetcher.Request{Accept: []string{ardDocumentMediaType, aiCatalogDocumentMediaType, "application/json"}}, true, false)
		if fetchErr != nil {
			failures.record(classifyOutboundOutcome(fetchErr), fetchErr)
			continue
		}
		if result.Status != http.StatusOK {
			failures.recordResult(outboundOutcomeHTTPError, fmt.Errorf("advertised discovery returned HTTP %d", result.Status), result)
			acceptCandidate(false)
			continue
		}
		mediaType, classificationErr := effectiveDiscoveryMediaType(result.MediaType, candidate.MediaType, expectedProfile)
		if classificationErr != nil {
			failures.recordResult(outboundOutcomeParseError, classificationErr, result)
			acceptCandidate(false)
			continue
		}
		document, parseErr := m.parseDocumentWithAccess(ctx, result.Body, mediaType, access)
		if parseErr != nil {
			failures.recordResult(outboundOutcomeParseError, parseErr, result)
			acceptCandidate(false)
			continue
		}
		acceptInitial(false)
		acceptCandidate(true)
		return completeDiscovery(access, discovery{result: result, document: document, warnings: []catalog.Finding{{Code: "ARD_DISCOVERY_RESOLVED", Severity: catalog.SeverityInfo, Message: "The source Catalog was resolved through its advertised discovery location."}}})
	}
	if len(embedded.Body) > 0 {
		if document, parseErr := m.parseDocumentWithAccess(ctx, embedded.Body, embedded.MediaType, access); parseErr == nil {
			acceptInitial(true)
			embeddedResult := initial
			embeddedResult.Body = embedded.Body
			sum := sha256.Sum256(embedded.Body)
			embeddedResult.BodyDigest = "sha256:" + hex.EncodeToString(sum[:])
			embeddedResult.MediaType = embedded.MediaType
			return completeDiscovery(access, discovery{result: embeddedResult, document: document, warnings: []catalog.Finding{{Code: "ARD_DISCOVERED_IN_HTML", Severity: catalog.SeverityInfo, Message: "ARD metadata was discovered in the source page."}}})
		} else {
			failures.record(outboundOutcomeParseError, parseErr)
		}
	}
	if initialDocument != nil {
		acceptInitial(true)
		return completeDiscovery(access, discovery{result: initial, document: *initialDocument})
	}
	acceptInitial(false)
	if strings.HasSuffix(strings.TrimSuffix(endpoint, "/"), "/search") || initial.Status == http.StatusMethodNotAllowed {
		return m.probeRegistrySearch(ctx, endpoint, access)
	}
	if syncRunFromContext(ctx) != nil && initial.Status != http.StatusOK {
		return discovery{}, syncHTTPError(initial)
	}
	if initial.Status != http.StatusOK {
		if failureErr := failures.err(); failureErr != nil {
			return discovery{}, failureErr
		}
		return discovery{}, withOutboundResult(catalog.E(catalog.CodeInvalidArgument, fmt.Sprintf("source inspection returned HTTP %d and no ARD discovery location resolved", initial.Status)), outboundOutcomeHTTPError, initial)
	}
	if failureErr := failures.err(); failureErr != nil {
		return discovery{}, failureErr
	}
	return discovery{}, withOutboundOutcome(catalog.E(catalog.CodeInvalidArgument, "source did not expose a compatible ARD manifest, AI Catalog, or Registry Search endpoint"), outboundOutcomeParseError)
}

// probeRegistrySearch verifies the exact selected interface without replacing
// its outcome with an unrelated same-origin manifest.
func (m *Manager) probeRegistrySearch(ctx context.Context, endpoint string, access sourceAccess) (discovery, error) {
	probeBody := []byte(`{"query":{"text":"catalog capability probe"},"federation":"none","pageSize":1}`)
	result, probeErr := m.fetchWithAccess(ctx, access, endpoint, fetcher.Request{Method: http.MethodPost, Body: probeBody, ContentType: "application/json", Accept: []string{"application/json"}})
	switch {
	case probeErr != nil:
		var probeFailure discoveryFailure
		probeFailure.recordPrimary(classifyOutboundOutcome(probeErr), probeErr)
		return discovery{}, probeFailure.err()
	case result.Status != http.StatusOK:
		return discovery{}, withOutboundResult(catalog.E(catalog.CodeInvalidArgument, fmt.Sprintf("Registry Search probe returned HTTP %d", result.Status)), outboundOutcomeHTTPError, result)
	default:
		if _, decodeErr := decodeLiveResponse(result.Body); decodeErr == nil {
			return completeDiscovery(access, discovery{result: result, document: parsedDocument{sourceType: SourceRegistry, profileID: ardProfileID}, registrySearchURL: endpoint})
		} else {
			return discovery{}, withOutboundResult(catalog.Wrap(catalog.CodeInvalidArgument, "Registry Search probe returned incompatible JSON", decodeErr), outboundOutcomeParseError, result)
		}
	}
}

// completeDiscovery rejects accumulated access-evidence failures before
// returning successful discovery.
func completeDiscovery(access sourceAccess, discovered discovery) (discovery, error) {
	if err := access.operationError(); err != nil {
		return discovery{}, err
	}
	return discovered, nil
}

// normalizeInspectionURL canonicalizes the administrator's initial discovery
// URL spelling.
func normalizeInspectionURL(value string) string {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		return "https://" + value
	}
	return value
}

// urlBase derives the base URL used for well-known discovery from an inspection
// target.
func urlBase(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid discovery URL")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// registrySearchEndpoint appends /search only to a safe HTTPS Registry base
// without an existing query.
func registrySearchEndpoint(value string) string {
	value = strings.TrimSpace(value)
	if !catalog.ValidExternalHTTPSURL(value) {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.RawQuery != "" {
		return ""
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	path += "/search"
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String()
}

type discoveryLocation struct {
	URL       string
	MediaType string
}

type embeddedDiscovery struct {
	Body      []byte
	MediaType string
}

// discoveryLinks resolves recognized HTTP Link discovery relations against the
// fetched document URL.
func discoveryLinks(headers []string, baseURL string) []discoveryLocation {
	result := make([]discoveryLocation, 0)
	base, err := url.Parse(baseURL)
	if err != nil {
		return result
	}
	for _, header := range headers {
		for _, part := range splitHTTPList(header) {
			start := strings.Index(part, "<")
			end := strings.Index(part, ">")
			if start < 0 || end <= start {
				continue
			}
			mediaType := discoveryRelationMediaType(part[end+1:])
			if mediaType == "" {
				continue
			}
			reference, parseErr := url.Parse(part[start+1 : end])
			if parseErr == nil {
				result = append(result, discoveryLocation{URL: base.ResolveReference(reference).String(), MediaType: mediaType})
			}
		}
	}
	return result
}

// discoveryRelationMediaType recognizes discovery relations. The predecessor
// alias locates entries but does not establish the legacy payload profile (§5.1).
func discoveryRelationMediaType(parameters string) string {
	hasARD := linkHasRelation(parameters, "ard")
	hasAICatalog := linkHasRelation(parameters, "ai-catalog")
	if !hasARD && !hasAICatalog {
		return ""
	}
	if hasARD {
		return ardDocumentMediaType
	}
	if hasAICatalog {
		for _, parameter := range splitHTTPParameters(parameters) {
			name, value, ok := strings.Cut(parameter, "=")
			if ok && strings.EqualFold(strings.TrimSpace(name), "type") {
				mediaType := canonicalMediaType(strings.Trim(strings.TrimSpace(value), `"`))
				if mediaType == aiCatalogDocumentMediaType || mediaType == ardDocumentMediaType {
					return mediaType
				}
			}
		}
		return "application/json"
	}
	return ""
}

// linkHasRelation checks case-insensitive rel tokens without confusing
// semicolons inside quoted parameters.
func linkHasRelation(parameters string, wanted ...string) bool {
	for _, parameter := range splitHTTPParameters(parameters) {
		name, value, ok := strings.Cut(parameter, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "rel") {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		for _, relation := range strings.Fields(value) {
			for _, candidate := range wanted {
				if strings.EqualFold(relation, candidate) {
					return true
				}
			}
		}
	}
	return false
}

// splitHTTPParameters separates semicolon-delimited parameters outside quoted
// values.
func splitHTTPParameters(value string) []string {
	parts := make([]string, 0)
	start := 0
	quoted := false
	for index, character := range value {
		switch character {
		case '"':
			quoted = !quoted
		case ';':
			if !quoted {
				parts = append(parts, strings.TrimSpace(value[start:index]))
				start = index + 1
			}
		}
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}

// splitHTTPList separates comma-delimited Link values outside quotes and
// angle-bracket URL targets.
func splitHTTPList(value string) []string {
	parts := make([]string, 0)
	start := 0
	quoted := false
	angled := false
	for index, character := range value {
		switch character {
		case '"':
			if !angled {
				quoted = !quoted
			}
		case '<':
			if !quoted {
				angled = true
			}
		case '>':
			if !quoted {
				angled = false
			}
		case ',':
			if !quoted && !angled {
				parts = append(parts, strings.TrimSpace(value[start:index]))
				start = index + 1
			}
		}
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}

// htmlDiscovery extracts supported embedded discovery and link metadata from
// HTML without executing scripts or following links.
func htmlDiscovery(body []byte, baseURL string) (embeddedDiscovery, []discoveryLocation) {
	document, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return embeddedDiscovery{}, nil
	}
	base, _ := url.Parse(baseURL)
	links := make([]discoveryLocation, 0)
	var embedded embeddedDiscovery
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "link" {
			attributes := htmlAttributes(node)
			relation := strings.ToLower(attributes["rel"])
			mediaType := ""
			hasARD := hasRelation(relation, "ard")
			hasAICatalog := hasRelation(relation, "ai-catalog")
			if hasARD {
				mediaType = ardDocumentMediaType
			} else if hasAICatalog {
				mediaType = canonicalMediaType(attributes["type"])
				if mediaType != aiCatalogDocumentMediaType && mediaType != ardDocumentMediaType {
					mediaType = "application/json"
				}
			}
			if mediaType != "" {
				if reference, parseErr := url.Parse(attributes["href"]); parseErr == nil && reference.String() != "" && base != nil {
					links = append(links, discoveryLocation{URL: base.ResolveReference(reference).String(), MediaType: mediaType})
				}
			}
		}
		if len(embedded.Body) == 0 && node.Type == html.ElementNode && node.Data == "script" {
			attributes := htmlAttributes(node)
			scriptMediaType := canonicalMediaType(attributes["type"])
			if scriptMediaType == "application/ld+json" || scriptMediaType == ardDocumentMediaType || scriptMediaType == aiCatalogDocumentMediaType {
				var value map[string]json.RawMessage
				content := strings.TrimSpace(nodeText(node))
				if json.Unmarshal([]byte(content), &value) == nil {
					mediaType := scriptMediaType
					if mediaType == "application/ld+json" {
						mediaType = ardDocumentMediaType
					}
					if _, ok := value["entries"]; ok {
						embedded = embeddedDiscovery{Body: []byte(content), MediaType: mediaType}
					} else if _, hasIdentifier := value["identifier"]; hasIdentifier {
						wrapped, marshalErr := json.Marshal(map[string]any{"entries": []json.RawMessage{json.RawMessage(content)}})
						if marshalErr == nil {
							embedded = embeddedDiscovery{Body: wrapped, MediaType: mediaType}
						}
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return embedded, links
}

// hasRelation checks a whitespace-separated relation list for any supported
// token.
func hasRelation(value string, wanted ...string) bool {
	for _, relation := range strings.Fields(strings.ToLower(value)) {
		for _, candidate := range wanted {
			if relation == strings.ToLower(candidate) {
				return true
			}
		}
	}
	return false
}

// htmlAttributes projects element attributes for discovery link interpretation.
func htmlAttributes(node *html.Node) map[string]string {
	result := make(map[string]string, len(node.Attr))
	for _, attribute := range node.Attr {
		result[strings.ToLower(attribute.Key)] = attribute.Val
	}
	return result
}

// nodeText extracts text content from an embedded discovery element.
func nodeText(node *html.Node) string {
	var builder strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.TextNode {
			builder.WriteString(child.Data)
		}
	}
	return builder.String()
}

// uniqueDiscoveryLocations deduplicates candidate URLs while preserving
// explicit profile evidence and excluding already visited targets.
func uniqueDiscoveryLocations(values []discoveryLocation, excluded ...string) []discoveryLocation {
	seen := make(map[string]discoveryLocation, len(values))
	conflicting := make(map[string]struct{})
	blocked := make(map[string]struct{}, len(excluded))
	order := make([]string, 0, len(values))
	for _, value := range excluded {
		if value != "" {
			blocked[value] = struct{}{}
		}
	}
	for _, value := range values {
		if value.URL == "" {
			continue
		}
		if _, skip := blocked[value.URL]; skip {
			continue
		}
		previous, ok := seen[value.URL]
		if !ok {
			seen[value.URL] = value
			order = append(order, value.URL)
			continue
		}
		if (previous.MediaType == "" || previous.MediaType == "application/json") && value.MediaType != "" {
			seen[value.URL] = value
			continue
		}
		if value.MediaType == "" || value.MediaType == "application/json" || previous.MediaType == value.MediaType {
			continue
		}
		conflicting[value.URL] = struct{}{}
	}
	result := make([]discoveryLocation, 0, min(len(order), 8))
	for _, candidate := range order {
		if _, conflict := conflicting[candidate]; conflict {
			continue
		}
		result = append(result, seen[candidate])
		if len(result) == 8 {
			break
		}
	}
	return result
}

// isDomainResolutionTarget reports whether the input qualifies for origin-level
// discovery.
func isDomainResolutionTarget(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != "" && (parsed.Path == "" || parsed.Path == "/") && parsed.RawQuery == "" && parsed.Fragment == ""
}

// discoveryURLMediaType recognizes the current well-known profile. ARD §5.1
// treats the predecessor path as an equivalent entry source, not a legacy format
// declaration. Its response media type or retained profile can still select AI Catalog.
func discoveryURLMediaType(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	switch parsed.Path {
	case "/.well-known/ard.json":
		return ardDocumentMediaType
	case "/.well-known/ai-catalog.json":
		return "application/json"
	default:
		return ""
	}
}

// effectiveDiscoveryMediaType reconciles explicit response/location signals,
// uses the retained profile as fallback and otherwise selects ARD.
func effectiveDiscoveryMediaType(reported, discovered, expectedProfile string) (string, error) {
	reported = canonicalMediaType(reported)
	discovered = canonicalMediaType(discovered)
	expected := ""
	if strings.HasPrefix(expectedProfile, "ard-") {
		expected = ardDocumentMediaType
	} else if strings.HasPrefix(expectedProfile, "ai-catalog-") {
		expected = aiCatalogDocumentMediaType
	}
	selected := ""
	for _, candidate := range []string{discovered, reported} {
		if candidate != ardDocumentMediaType && candidate != aiCatalogDocumentMediaType {
			continue
		}
		if selected != "" && selected != candidate {
			return "", errors.New("discovery location, response media type, and retained source profile disagree")
		}
		selected = candidate
	}
	if selected == "" {
		selected = expected
	}
	if selected == "" {
		selected = ardDocumentMediaType
	}
	return selected, nil
}

// canonicalMediaType lowercases the parsed base media type, retaining a
// normalized spelling when parsing fails.
func canonicalMediaType(value string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(value))
	}
	return strings.ToLower(mediaType)
}

// Connect creates a source only from retained inspection evidence and validated
// administrator-selected participation policy.
func (m *Manager) Connect(ctx context.Context, input ConnectInput) (Source, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.manage")
	if err != nil {
		return Source{}, err
	}
	reason, err := audit.NormalizeReason(audit.ActionSourceConnected, input.Reason)
	if err != nil {
		return Source{}, catalog.Wrap(catalog.CodeInvalidArgument, "source connection reason is invalid", err)
	}
	inspection, err := m.store.GetInspection(ctx, actor.TenantID, input.InspectionID)
	if err != nil {
		return Source{}, err
	}
	if !inspection.ExpiresAt.After(m.clock.Now()) {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source inspection has expired; inspect the source again")
	}
	if m.fetcher.PolicyDigest() != inspection.PolicyDigest {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "federation policy changed after inspection; inspect the source again")
	}
	input.NetworkProfile = strings.TrimSpace(input.NetworkProfile)
	input.CredentialBinding = strings.TrimSpace(input.CredentialBinding)
	if input.NetworkProfile == "" {
		input.NetworkProfile = inspection.NetworkProfile
	}
	if input.CredentialBinding == "" {
		input.CredentialBinding = inspection.CredentialBinding
	}
	if input.NetworkProfile != inspection.NetworkProfile || input.CredentialBinding != inspection.CredentialBinding {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source connectivity must match the inspected network profile and credential binding")
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.DisplayName == "" || !utf8.ValidString(input.DisplayName) || strings.ContainsRune(input.DisplayName, 0) || utf8.RuneCountInString(input.DisplayName) > 160 {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source display name is required")
	}
	if input.UpdateMode == "" {
		input.UpdateMode = UpdateManual
	}
	if input.UpdateMode != UpdateManual && input.UpdateMode != UpdateAutomatic {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source update mode is invalid")
	}
	if err := validateScope(input.Scope); err != nil {
		return Source{}, err
	}
	input.Scope = normalizeScope(input.Scope)
	if len(input.Scope.Identifiers) == 0 && len(input.Scope.Versions) == 0 && len(input.Scope.Kinds) == 0 && len(input.Scope.Publishers) == 0 && len(input.Scope.Tags) == 0 {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source scope must select explicit identifiers or at least one constrained rule")
	}
	now := m.clock.Now()
	idValue, err := m.ids.New(now)
	if err != nil {
		return Source{}, catalog.Wrap(catalog.CodeInternal, "create source identifier", err)
	}
	if input.Live.Enabled && inspection.RegistrySearchURL == "" {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "the inspected source does not advertise an ARD Registry search endpoint")
	}
	input.Nesting, err = normalizeNestingPolicy(input.Nesting)
	if err != nil {
		return Source{}, err
	}
	source := Source{TrustVerificationPolicy: input.TrustVerificationPolicy, ArtworkEnabled: input.ArtworkEnabled, PresentationGeneration: 1, ID: idValue, TenantID: actor.TenantID, DisplayName: input.DisplayName, Type: inspection.SourceType, Endpoint: inspection.ResolvedURL, RegistrySearchURL: inspection.RegistrySearchURL, RegistryListURL: inspection.RegistryListURL, Status: SourceActive, ProfileID: inspection.ProfileID, RetrievalMode: input.RetrievalMode, Scope: input.Scope, UpdateMode: input.UpdateMode, Live: input.Live, Referrals: input.Referrals, DiscoveryAssignments: input.DiscoveryAssignments, Nesting: input.Nesting, CredentialBinding: input.CredentialBinding, NetworkProfile: input.NetworkProfile, PolicyID: inspection.PolicyID, PolicyRevision: inspection.PolicyRevision, PolicyDigest: inspection.PolicyDigest, CredentialRevision: inspection.CredentialRevision, Revision: 1, CreatedBy: actor.Subject, CreatedAt: now, UpdatedAt: now, InspectionID: inspection.ID}
	source, err = m.normalizeAndValidateSource(ctx, source)
	if err != nil {
		return Source{}, err
	}
	configurationDigest, err := sourceConfigurationDigest(source)
	if err != nil {
		return Source{}, catalog.Wrap(catalog.CodeInternal, "digest source configuration", err)
	}
	afterDigest, err := sourceStateDigest(source)
	if err != nil {
		return Source{}, catalog.Wrap(catalog.CodeInternal, "digest source state", err)
	}
	evidence, err := m.mutationEvidence(ctx, actor, audit.ActionSourceConnected, "federation_source", source.ID, reason, sourceConnectionFacts{InspectionID: inspection.ID, SourceRevision: source.Revision, ConfigurationDigest: configurationDigest, RetrievalMode: source.RetrievalMode, UpdateMode: source.UpdateMode, PolicyDigest: source.PolicyDigest}, "", afterDigest, "source.connected", "federation_source", now)
	if err != nil {
		return Source{}, err
	}
	return m.store.CreateSource(ctx, source, evidence)
}

// GetInspection returns one unexpired or historical inspection to an authorized source administrator.
func (m *Manager) GetInspection(ctx context.Context, inspectionID string) (Inspection, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return Inspection{}, err
	}
	inspection, err := m.store.GetInspection(ctx, actor.TenantID, inspectionID)
	if err != nil {
		return Inspection{}, err
	}
	if m.evidence == nil {
		return Inspection{}, catalog.E(catalog.CodeDependencyUnavailable, "source-inspection audit evidence is unavailable")
	}
	record, err := m.auditRecord(ctx, actor, audit.ActionSourceInspectionRead, "source_inspection", inspection.ID, "", sensitiveReadFacts{ContentDigest: inspectionDigest(inspection)}, m.clock.Now())
	if err != nil {
		return Inspection{}, err
	}
	if record.ID != "" {
		if err := m.evidence.AppendAuditEvent(ctx, record); err != nil {
			return Inspection{}, catalog.Wrap(catalog.CodeDependencyUnavailable, "retain source-inspection read evidence", err)
		}
	}
	return inspection, nil
}

// GetSnapshot returns immutable evidence for one federation run.
func (m *Manager) GetSnapshot(ctx context.Context, snapshotID string) (Snapshot, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return Snapshot{}, err
	}
	return m.store.GetSnapshot(ctx, actor.TenantID, snapshotID)
}

// ListSourcePage validates source-administrator pagination before reading the
// tenant source list.
func (m *Manager) ListSourcePage(ctx context.Context, request SourceListRequest) ([]Source, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return nil, err
	}
	if request.Limit < 1 || request.Limit > 100 || request.AfterDisplayName == "" && request.AfterID != "" || request.AfterDisplayName != "" && !identity.ValidTenantID(request.AfterID) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "source page position or limit is invalid")
	}
	if err := validateSourceListFilters(request); err != nil {
		return nil, err
	}
	request.Limit++
	return m.store.ListSourcePage(ctx, actor.TenantID, request)
}

// Referrals returns bounded approved referral metadata for the requested
// host/audience and reports topology truncation.
func (m *Manager) Referrals(ctx context.Context, host string, audience DiscoveryAudience) ([]Referral, bool, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "federation.discover")
	if err != nil {
		return nil, false, err
	}
	if err := identity.RequireInternalTarget(ctx, "federation.discover", actor.TenantID, host); err != nil {
		return nil, false, catalog.PermissionError("discovery host binding is required", err)
	}
	sources, partial, err := m.store.ListDiscoveryReferralSources(ctx, actor.TenantID, host, audience, m.fetcher.PolicyDigest(), m.maxFanout)
	if err != nil {
		return nil, false, err
	}
	referrals := make([]Referral, 0, min(len(sources), m.maxFanout))
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if source.Status != SourceActive || !source.Referrals.Enabled || source.RegistrySearchURL == "" || !m.sourceUsesCurrentPolicy(source) || !sourceEligibleForDiscovery(source, host, audience) {
			continue
		}
		referrals = append(referrals, Referral{SourceID: source.ID, DisplayName: source.DisplayName, URL: source.RegistrySearchURL})
	}
	return referrals, partial, nil
}

// sourceUsesCurrentPolicy reports whether the source's approved digest matches
// the active outbound policy.
func (m *Manager) sourceUsesCurrentPolicy(source Source) bool {
	return validProfileReference(source.PolicyID) && source.PolicyRevision > 0 && validPolicyDigest(source.PolicyDigest) && source.PolicyDigest == m.fetcher.PolicyDigest()
}

// sourceEligibleForDiscovery checks source status and administrator-selected
// host/audience participation.
func sourceEligibleForDiscovery(source Source, host string, audience DiscoveryAudience) bool {
	if audience == AudienceAnonymous {
		if source.NetworkProfile != "" || source.Live.Enabled && !source.Live.AnonymousDisclosureAcknowledged || source.Referrals.Enabled && !source.Referrals.AnonymousDisclosureAcknowledged {
			return false
		}
	}
	host = strings.ToLower(strings.TrimSpace(host))
	for _, assignment := range source.DiscoveryAssignments {
		if assignment.Host == host && assignment.Audience == audience {
			return true
		}
	}
	return false
}

// GetSource requires source-administrator authority and resolves a source in
// the actor's tenant.
func (m *Manager) GetSource(ctx context.Context, sourceID string) (Source, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return Source{}, err
	}
	return m.store.GetSource(ctx, actor.TenantID, sourceID)
}

// PreviewChange validates a proposed source revision and retains a digest-bound
// preview of its observation impact.
func (m *Manager) PreviewChange(ctx context.Context, sourceID string, input PatchInput) (ChangePreview, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.manage")
	if err != nil {
		return ChangePreview{}, err
	}
	source, err := m.store.GetSource(ctx, actor.TenantID, sourceID)
	if err != nil {
		return ChangePreview{}, err
	}
	intent := input.Intent
	if intent != ChangeUpdate && intent != ChangeRemove {
		return ChangePreview{}, catalog.E(catalog.CodeInvalidArgument, "source change-preview intent must be update or remove")
	}
	proposed := source
	if intent == ChangeRemove {
		if hasSourcePatch(input) {
			return ChangePreview{}, catalog.E(catalog.CodeInvalidArgument, "a source removal preview cannot also propose configuration changes")
		}
	} else {
		if !hasSourcePatch(input) {
			return ChangePreview{}, catalog.E(catalog.CodeInvalidArgument, "a source update preview requires at least one configuration change")
		}
		proposed, err = applySourcePatch(source, input)
		if err != nil {
			return ChangePreview{}, err
		}
		proposed, err = m.applyConnectivityInspection(ctx, actor.TenantID, source, proposed, input)
		if err != nil {
			return ChangePreview{}, err
		}
		proposed.PresentationGeneration = NextPresentationGeneration(source, proposed)
		proposed, err = m.normalizeAndValidateSource(ctx, proposed)
		if err != nil {
			return ChangePreview{}, err
		}
	}
	observationSet, overflow, err := m.store.CurrentObservationSet(ctx, actor.TenantID, source.ID, m.maxEntries)
	if err != nil {
		return ChangePreview{}, err
	}
	if overflow {
		return ChangePreview{}, catalog.E(catalog.CodeWorkLimitExceeded, "current source observations exceed the supported change-preview limit")
	}
	observations, overflow, err := m.store.ListCurrentObservations(ctx, actor.TenantID, source.ID, m.maxEntries)
	if err != nil {
		return ChangePreview{}, err
	}
	if overflow {
		return ChangePreview{}, catalog.E(catalog.CodeWorkLimitExceeded, "current source observations exceed the supported change-preview limit")
	}
	currentMatched := observationSet.Count
	proposedMatched := 0
	if intent == ChangeUpdate {
		for _, observation := range observations {
			var entry sourceEntry
			if err := json.Unmarshal(observation.RawEntry, &entry); err != nil {
				continue
			}
			if scopeMatches(proposed.Scope, entry) {
				proposedMatched++
			}
		}
	}
	configurationDigest, err := sourceConfigurationDigest(proposed)
	if err != nil {
		return ChangePreview{}, catalog.Wrap(catalog.CodeInternal, "digest proposed source configuration", err)
	}
	if intent == ChangeUpdate {
		currentConfigurationDigest, digestErr := sourceConfigurationDigest(source)
		if digestErr != nil {
			return ChangePreview{}, catalog.Wrap(catalog.CodeInternal, "digest current source configuration", digestErr)
		}
		if configurationDigest == currentConfigurationDigest {
			return ChangePreview{}, catalog.E(catalog.CodeInvalidArgument, "source update does not change the reviewed configuration")
		}
	}
	now := m.clock.Now()
	idValue, err := m.ids.New(now)
	if err != nil {
		return ChangePreview{}, catalog.Wrap(catalog.CodeInternal, "create source change preview identifier", err)
	}
	artworkAffected := 0
	if intent == ChangeRemove || EffectivePresentationGeneration(proposed) != EffectivePresentationGeneration(source) {
		if store, ok := m.store.(ArtworkConsequenceStore); ok {
			var overflow bool
			artworkAffected, overflow, err = store.ArtworkOverrideCount(ctx, actor.TenantID, source.ID, EffectivePresentationGeneration(source), MaxSynchronizationEntries)
			if err != nil {
				return ChangePreview{}, err
			}
			if overflow {
				return ChangePreview{}, catalog.E(catalog.CodeWorkLimitExceeded, "source artwork exceeds the supported preview bound")
			}
		}
	}
	expiresAt := now.Add(10 * time.Minute)
	digestFields := map[string]any{"id": idValue, "tenantId": actor.TenantID, "sourceId": source.ID, "revision": source.Revision, "intent": intent, "configurationDigest": configurationDigest, "observationSetDigest": observationSet.Digest, "currentMatched": currentMatched, "proposedMatched": proposedMatched, "expiresAt": expiresAt}
	if artworkAffected != 0 {
		digestFields["artworkOverridesAffected"] = artworkAffected
	}
	digestInput, err := json.Marshal(digestFields)
	if err != nil {
		return ChangePreview{}, catalog.Wrap(catalog.CodeInternal, "encode source change preview", err)
	}
	digestValue := sha256.Sum256(digestInput)
	preview := ChangePreview{ArtworkOverridesAffected: artworkAffected, ID: idValue, TenantID: actor.TenantID, SourceID: source.ID, SourceRevision: source.Revision, Intent: intent, CurrentMatched: currentMatched, ProposedMatched: proposedMatched, ExcludedObservations: max(0, currentMatched-proposedMatched), SendsQueriesUpstream: intent == ChangeUpdate && (proposed.Live.Enabled || proposed.Nesting.Enabled), ConfigurationDigest: configurationDigest, ObservationSetDigest: observationSet.Digest, Digest: "sha256:" + hex.EncodeToString(digestValue[:]), CreatedAt: now, ExpiresAt: expiresAt}
	evidence, err := m.mutationEvidence(ctx, actor, audit.ActionSourceChangePreviewCreated, "source_change_preview", preview.ID, "", sourcePreviewFacts{SourceID: source.ID, SourceRevision: source.Revision, Intent: intent, ConfigurationDigest: configurationDigest, ObservationSetDigest: observationSet.Digest, PreviewDigest: preview.Digest, CurrentMatched: currentMatched, ProposedMatched: proposedMatched, ExcludedObservations: preview.ExcludedObservations, SendsQueriesUpstream: preview.SendsQueriesUpstream, ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano)}, "", preview.Digest, "", "", now)
	if err != nil {
		return ChangePreview{}, err
	}
	return m.store.SaveChangePreview(ctx, preview, evidence)
}

// UpdateSource validates the expected revision and required preview/inspection
// bindings before activating a source configuration change.
func (m *Manager) UpdateSource(ctx context.Context, sourceID string, input PatchInput, expectedRevision int64) (Source, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.manage")
	if err != nil {
		return Source{}, err
	}
	if input.ChangePreviewDigest == "" {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "a current source change preview digest is required")
	}
	if !hasSourcePatch(input) {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "a source update requires at least one configuration change")
	}
	reason, err := audit.NormalizeReason(audit.ActionSourceRevisionActivated, input.Reason)
	if err != nil {
		return Source{}, catalog.Wrap(catalog.CodeInvalidArgument, "source revision reason is invalid", err)
	}
	source, err := m.store.GetSource(ctx, actor.TenantID, sourceID)
	if err != nil {
		return Source{}, err
	}
	if source.Revision != expectedRevision {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source revision does not match")
	}
	proposed, err := applySourcePatch(source, input)
	if err != nil {
		return Source{}, err
	}
	proposed, err = m.applyConnectivityInspection(ctx, actor.TenantID, source, proposed, input)
	if err != nil {
		return Source{}, err
	}
	proposed.PresentationGeneration = NextPresentationGeneration(source, proposed)
	proposed, err = m.normalizeAndValidateSource(ctx, proposed)
	if err != nil {
		return Source{}, err
	}
	configurationDigest, err := sourceConfigurationDigest(proposed)
	if err != nil {
		return Source{}, catalog.Wrap(catalog.CodeInternal, "digest proposed source configuration", err)
	}
	currentConfigurationDigest, err := sourceConfigurationDigest(source)
	if err != nil {
		return Source{}, catalog.Wrap(catalog.CodeInternal, "digest current source configuration", err)
	}
	if configurationDigest == currentConfigurationDigest {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source update does not change the reviewed configuration")
	}
	preview, err := m.store.GetChangePreview(ctx, actor.TenantID, input.ChangePreviewDigest)
	if err != nil {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source change preview is missing or stale")
	}
	if preview.SourceID != source.ID || preview.SourceRevision != source.Revision || preview.Intent != ChangeUpdate || preview.ConfigurationDigest != configurationDigest || !preview.ExpiresAt.After(m.clock.Now()) {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source change preview is missing or stale")
	}
	currentSet, overflow, err := m.store.CurrentObservationSet(ctx, actor.TenantID, source.ID, m.maxEntries)
	if err != nil {
		return Source{}, err
	}
	if overflow {
		return Source{}, catalog.E(catalog.CodeWorkLimitExceeded, "current source observations exceed the supported source-update limit")
	}
	if currentSet.Count != preview.CurrentMatched || currentSet.Digest != preview.ObservationSetDigest {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source observations changed after the consequence preview")
	}
	proposed.Revision = source.Revision + 1
	proposed.UpdatedAt = catalog.TransitionTime(m.clock.Now(), source.UpdatedAt)
	beforeDigest, err := sourceStateDigest(source)
	if err != nil {
		return Source{}, catalog.Wrap(catalog.CodeInternal, "digest current source state", err)
	}
	afterDigest, err := sourceStateDigest(proposed)
	if err != nil {
		return Source{}, catalog.Wrap(catalog.CodeInternal, "digest proposed source state", err)
	}
	evidence, err := m.mutationEvidence(ctx, actor, audit.ActionSourceRevisionActivated, "federation_source", source.ID, reason, sourceRevisionFacts{PreviewID: preview.ID, SourceRevision: proposed.Revision, BeforeConfigurationDigest: currentConfigurationDigest, AfterConfigurationDigest: configurationDigest, RetrievalMode: proposed.RetrievalMode, UpdateMode: proposed.UpdateMode, PolicyDigest: proposed.PolicyDigest, Trigger: "reviewed_update"}, beforeDigest, afterDigest, "source.revision.activated", "federation_source", proposed.UpdatedAt)
	if err != nil {
		return Source{}, err
	}
	evidence.ExpectedObservationSet = &currentSet
	return m.store.UpdateSource(ctx, proposed, expectedRevision, evidence)
}

// hasSourcePatch reports whether at least one supported source configuration
// field was supplied.
func hasSourcePatch(input PatchInput) bool {
	return input.TrustVerificationPolicy != nil || input.InspectionID != "" || input.DisplayName != nil || input.RetrievalMode != nil || input.Scope != nil || input.UpdateMode != nil || input.Live != nil || input.Referrals != nil || input.DiscoveryAssignments != nil || input.Nesting != nil || input.CredentialBinding != nil || input.NetworkProfile != nil || input.ArtworkEnabled != nil
}

// TestSource checks the current source configuration through the administrator
// test workflow.
func (m *Manager) TestSource(ctx context.Context, sourceID string) (TestResult, error) {
	actor, err := permissionActor(ctx, "source.inspect")
	if err != nil {
		return TestResult{}, err
	}
	source, err := m.store.GetSource(ctx, actor.TenantID, sourceID)
	if err != nil {
		return TestResult{}, err
	}
	return m.TestSourceAtRevision(ctx, sourceID, source.Revision)
}

// TestSourceAtRevision tests the exact expected source revision and retains
// attributed connectivity/profile evidence.
func (m *Manager) TestSourceAtRevision(ctx context.Context, sourceID string, expectedRevision int64) (TestResult, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.inspect")
	if err != nil {
		return TestResult{}, err
	}
	source, err := m.store.GetSource(ctx, actor.TenantID, sourceID)
	if err != nil {
		return TestResult{}, err
	}
	if expectedRevision <= 0 || source.Revision != expectedRevision {
		return TestResult{}, catalog.E(catalog.CodePreconditionFailed, "source revision does not match")
	}
	currentConfigurationDigest, err := sourceConfigurationDigest(source)
	if err != nil {
		return TestResult{}, catalog.Wrap(catalog.CodeInternal, "digest current source configuration", err)
	}
	validated, err := m.normalizeAndValidateSource(ctx, source)
	if err != nil {
		return TestResult{}, err
	}
	validatedConfigurationDigest, err := sourceConfigurationDigest(validated)
	if err != nil {
		return TestResult{}, catalog.Wrap(catalog.CodeInternal, "digest validated source configuration", err)
	}
	if currentConfigurationDigest != validatedConfigurationDigest {
		return TestResult{}, catalog.E(catalog.CodePreconditionFailed, "source configuration must be reviewed and saved under the current validation rules before it can be tested")
	}
	source = validated
	access := accessForSource(source, false)
	discovered, err := m.discover(ctx, source.Endpoint, access, source.ProfileID)
	if err != nil {
		failure := catalog.Wrap(catalog.CodeDependencyUnavailable, "test source", err)
		failureEvidence := outboundEvidenceFromError(err)
		result := TestResult{SourceID: source.ID, SourceRevision: source.Revision, Status: "failed", HTTPStatus: failureEvidence.HTTPStatus, BodyDigest: failureEvidence.BodyDigest, PolicyDigest: failureEvidence.PolicyDigest, CredentialRevision: failureEvidence.CredentialRevision, Capabilities: map[string]bool{}, TestedAt: m.clock.Now()}
		if auditErr := m.retainSourceTest(ctx, actor, source, result, classifyOutboundOutcome(err)); auditErr != nil {
			return TestResult{}, auditErr
		}
		return TestResult{}, catalog.PreserveEvidence(failure)
	}
	result := discovered.result
	test := TestResult{SourceID: source.ID, SourceRevision: source.Revision, Status: "failed", HTTPStatus: result.Status, MediaType: result.MediaType, BodyDigest: result.BodyDigest, Capabilities: map[string]bool{}, PolicyID: result.PolicyID, PolicyRevision: result.PolicyRevision, PolicyDigest: result.PolicyDigest, CredentialRevision: result.CredentialRevision, TestedAt: m.clock.Now()}
	if result.Status < 200 || result.Status >= 300 {
		test.SafeDiagnostic = fmt.Sprintf("Source returned HTTP %d.", result.Status)
		if err := m.retainSourceTest(ctx, actor, source, test, outboundOutcomeHTTPError); err != nil {
			return TestResult{}, err
		}
		return test, nil
	}
	summary := summarizeDiscovery(discovered)
	if candidate := registryListCandidate(discovered); summary.sourceType == SourceRegistry && candidate != "" {
		if listURL, _ := m.probeRegistryList(ctx, access, candidate); listURL != "" {
			summary.registryListURL = listURL
			summary.capabilities["enumerable"] = true
			summary.capabilities["list"] = true
		}
	}
	if err := access.operationError(); err != nil {
		failure := catalog.Wrap(catalog.CodeDependencyUnavailable, "verify source test evidence", err)
		if auditErr := m.retainSourceTest(ctx, actor, source, test, classifyOutboundOutcome(err)); auditErr != nil {
			return TestResult{}, auditErr
		}
		return TestResult{}, catalog.PreserveEvidence(failure)
	}
	test.EntryCount = summary.entryCount
	test.ProfileID = summary.profileID
	test.Capabilities = summary.capabilities
	if sourceInterfaceChanged(source, result, summary) {
		test.SafeDiagnostic = "Source interface changed. Inspect and review it again before updating policy evidence."
		if err := m.retainSourceTest(ctx, actor, source, test, outboundOutcomeNegative); err != nil {
			return TestResult{}, err
		}
		return test, nil
	}
	test.Status = "succeeded"
	test.TestedAt = catalog.TransitionTime(test.TestedAt, source.UpdatedAt)
	source.PolicyID = result.PolicyID
	source.PolicyRevision = result.PolicyRevision
	source.PolicyDigest = result.PolicyDigest
	source.CredentialRevision = result.CredentialRevision
	source.Revision++
	source.UpdatedAt = test.TestedAt
	before := source
	before.Revision--
	before.PolicyID = validated.PolicyID
	before.PolicyRevision = validated.PolicyRevision
	before.PolicyDigest = validated.PolicyDigest
	before.CredentialRevision = validated.CredentialRevision
	before.UpdatedAt = validated.UpdatedAt
	beforeDigest, err := sourceStateDigest(before)
	if err != nil {
		return TestResult{}, catalog.Wrap(catalog.CodeInternal, "digest source state before test", err)
	}
	afterDigest, err := sourceStateDigest(source)
	if err != nil {
		return TestResult{}, catalog.Wrap(catalog.CodeInternal, "digest source state after test", err)
	}
	evidence, err := m.mutationEvidence(ctx, actor, audit.ActionSourceTested, "federation_source", source.ID, "", sourceTestFacts{BeforeRevision: before.Revision, AfterRevision: source.Revision, Outcome: "succeeded", StateChanged: true, HTTPClass: httpClass(test.HTTPStatus), PolicyDigest: test.PolicyDigest, BodyDigest: test.BodyDigest, Capabilities: sortedCapabilities(test.Capabilities), CredentialRevision: test.CredentialRevision}, beforeDigest, afterDigest, "source.revision.activated", "federation_source", test.TestedAt)
	if err != nil {
		return TestResult{}, err
	}
	if _, err := m.store.UpdateSource(ctx, source, expectedRevision, evidence); err != nil {
		if catalog.ErrorCodeOf(err) == catalog.CodePreconditionFailed {
			if auditErr := m.retainSourceTest(ctx, actor, before, test, outboundOutcomeNegative); auditErr != nil {
				return TestResult{}, auditErr
			}
			return TestResult{}, catalog.PreserveEvidence(err)
		}
		return TestResult{}, err
	}
	test.SourceRevision = source.Revision
	return test, nil
}

// retainSourceTest binds a bounded test result to the tested source revision
// and actor.
func (m *Manager) retainSourceTest(ctx context.Context, actor identity.Actor, source Source, result TestResult, outcome string) error {
	policyDigest := result.PolicyDigest
	if policyDigest == "" {
		policyDigest = source.PolicyDigest
	}
	stateDigest, err := sourceStateDigest(source)
	if err != nil {
		return catalog.Wrap(catalog.CodeInternal, "digest source state for test evidence", err)
	}
	record, err := m.auditRecord(ctx, actor, audit.ActionSourceTested, "federation_source", source.ID, "", sourceTestFacts{BeforeRevision: source.Revision, AfterRevision: source.Revision, Outcome: outcome, StateChanged: false, HTTPClass: httpClass(result.HTTPStatus), PolicyDigest: policyDigest, BodyDigest: result.BodyDigest, Capabilities: sortedCapabilities(result.Capabilities), CredentialRevision: result.CredentialRevision}, m.clock.Now())
	if err != nil {
		return err
	}
	if record.ID == "" {
		return nil
	}
	record.BeforeDigest = stateDigest
	record.AfterDigest = stateDigest
	if err := audit.ValidateNewRecord(record); err != nil {
		return catalog.Wrap(catalog.CodeInternal, "validate source-test audit evidence", err)
	}
	if err := m.evidence.AppendAuditEvent(ctx, record); err != nil {
		return catalog.Wrap(catalog.CodeDependencyUnavailable, "retain source-test audit evidence", err)
	}
	return nil
}

// ListSnapshots validates a source-administrator history request and returns a
// bounded tenant source sample.
func (m *Manager) ListSnapshots(ctx context.Context, sourceID string, limit int) ([]Snapshot, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return nil, err
	}
	if _, err := m.store.GetSource(ctx, actor.TenantID, sourceID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	return m.store.ListSnapshots(ctx, actor.TenantID, sourceID, limit)
}

// ListSnapshotPage validates source history continuation and selects one extra
// row for pagination.
func (m *Manager) ListSnapshotPage(ctx context.Context, request SnapshotListRequest) ([]Snapshot, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return nil, err
	}
	if !identity.ValidTenantID(request.SourceID) || request.Limit < 1 || request.Limit > 100 || request.AfterStartedAt == nil && request.AfterID != "" || request.AfterStartedAt != nil && !identity.ValidTenantID(request.AfterID) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "source-snapshot page position or limit is invalid")
	}
	if _, err := m.store.GetSource(ctx, actor.TenantID, request.SourceID); err != nil {
		return nil, err
	}
	request.Limit++
	return m.store.ListSnapshotPage(ctx, actor.TenantID, request)
}

// GetObservation returns an observation within the source administrator's tenant.
func (m *Manager) GetObservation(ctx context.Context, observationID string) (Observation, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return Observation{}, err
	}
	return m.store.GetObservation(ctx, actor.TenantID, observationID)
}

// GetRawObservation prepares one exact retained source entry and appends its
// access evidence before the transport is allowed to write response bytes.
func (m *Manager) GetRawObservation(ctx context.Context, observationID string) (Observation, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read_evidence")
	if err != nil {
		return Observation{}, err
	}
	observation, err := m.store.GetObservation(ctx, actor.TenantID, observationID)
	if err != nil {
		return Observation{}, err
	}
	if !observation.RawEntryAvailable {
		return Observation{}, catalog.E(catalog.CodePreconditionFailed, "exact raw observation bytes are unavailable")
	}
	if m.evidence == nil {
		return Observation{}, catalog.E(catalog.CodeDependencyUnavailable, "raw-observation audit evidence is unavailable")
	}
	record, err := m.auditRecord(ctx, actor, audit.ActionRawObservationRead, "observation", observation.ID, "", sensitiveReadFacts{ContentDigest: observation.ExactEntryDigest, SizeBytes: len(observation.RawEntry)}, m.clock.Now())
	if err != nil {
		return Observation{}, err
	}
	if record.ID != "" {
		if err := m.evidence.AppendAuditEvent(ctx, record); err != nil {
			return Observation{}, catalog.Wrap(catalog.CodeDependencyUnavailable, "retain raw-observation read evidence", err)
		}
	}
	return observation, nil
}

type StatusChangeInput struct {
	Reason              string
	ChangePreviewDigest string
}

// SetStatus applies an administrator source lifecycle transition under the
// expected revision and required reason/confirmation inputs.
func (m *Manager) SetStatus(ctx context.Context, sourceID string, status SourceStatus, expectedRevision int64, inputs ...StatusChangeInput) (Source, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.manage")
	if err != nil {
		return Source{}, err
	}
	if status != SourceActive && status != SourcePaused && status != SourceRemoved {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source status is invalid")
	}
	if len(inputs) > 1 {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source status command input is ambiguous")
	}
	var input StatusChangeInput
	if len(inputs) == 1 {
		input = inputs[0]
	}
	action := audit.ActionSourcePaused
	publicAction := "source.paused"
	if status == SourceActive {
		action, publicAction = audit.ActionSourceResumed, "source.resumed"
	} else if status == SourceRemoved {
		action, publicAction = audit.ActionSourceRemoved, "source.removed"
	}
	reason, err := audit.NormalizeReason(action, input.Reason)
	if err != nil {
		return Source{}, catalog.Wrap(catalog.CodeInvalidArgument, "source lifecycle reason is invalid", err)
	}
	source, err := m.store.GetSource(ctx, actor.TenantID, sourceID)
	if err != nil {
		return Source{}, err
	}
	if status == SourceActive && !m.sourceUsesCurrentPolicy(source) {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source must pass a test under the active federation policy before it can resume")
	}
	if source.Status == status {
		return Source{}, catalog.E(catalog.CodeInvalidTransition, "source already has the requested status")
	}
	var preview ChangePreview
	var observationSet *ObservationSet
	if status == SourceRemoved {
		if input.ChangePreviewDigest == "" {
			return Source{}, catalog.E(catalog.CodePreconditionFailed, "a current source removal preview digest is required")
		}
		preview, err = m.store.GetChangePreview(ctx, actor.TenantID, input.ChangePreviewDigest)
		if err != nil || preview.SourceID != source.ID || preview.SourceRevision != source.Revision || preview.Intent != ChangeRemove || !preview.ExpiresAt.After(m.clock.Now()) {
			return Source{}, catalog.E(catalog.CodePreconditionFailed, "source removal preview is missing or stale")
		}
		currentSet, overflow, setErr := m.store.CurrentObservationSet(ctx, actor.TenantID, source.ID, m.maxEntries)
		if setErr != nil {
			return Source{}, setErr
		}
		if overflow {
			return Source{}, catalog.E(catalog.CodeWorkLimitExceeded, "current source observations exceed the supported source-removal limit")
		}
		if currentSet.Count != preview.CurrentMatched || currentSet.Digest != preview.ObservationSetDigest {
			return Source{}, catalog.E(catalog.CodePreconditionFailed, "source observations changed after the removal preview")
		}
		observationSet = &currentSet
	}
	before := source
	source.Status = status
	source.Revision++
	source.UpdatedAt = catalog.TransitionTime(m.clock.Now(), before.UpdatedAt)
	beforeDigest, err := sourceStateDigest(before)
	if err != nil {
		return Source{}, catalog.Wrap(catalog.CodeInternal, "digest source state before lifecycle transition", err)
	}
	afterDigest, err := sourceStateDigest(source)
	if err != nil {
		return Source{}, catalog.Wrap(catalog.CodeInternal, "digest source state after lifecycle transition", err)
	}
	facts := sourceLifecycleFacts{BeforeState: before.Status, AfterState: source.Status, BeforeRevision: before.Revision, AfterRevision: source.Revision}
	if status == SourceRemoved {
		facts.PreviewID = preview.ID
		facts.PreviewDigest = preview.Digest
		facts.ObservationSetDigest = preview.ObservationSetDigest
	}
	evidence, err := m.mutationEvidence(ctx, actor, action, "federation_source", source.ID, reason, facts, beforeDigest, afterDigest, publicAction, "federation_source", source.UpdatedAt)
	if err != nil {
		return Source{}, err
	}
	evidence.ExpectedObservationSet = observationSet
	return m.store.UpdateSource(ctx, source, expectedRevision, evidence)
}

// normalizeAndValidateSource closes the cross-field source contract after an
// inspection: retrieval mode, scope, topology, credentials, and policy evidence
// must describe one coherent administrator-approved source.
func (m *Manager) normalizeAndValidateSource(ctx context.Context, source Source) (Source, error) {
	if source.TrustVerificationPolicy != "" {
		if !validProfileReference(source.TrustVerificationPolicy) {
			return Source{}, catalog.E(catalog.CodeInvalidArgument, "source trust policy reference is invalid")
		}
		if m.trust != nil && m.trust.Enabled() {
			if _, _, ok := m.trust.Policy(source.TrustVerificationPolicy); !ok {
				return Source{}, catalog.E(catalog.CodeInvalidArgument, "source trust policy is unavailable")
			}
		}
	}
	source.DisplayName = strings.TrimSpace(source.DisplayName)
	if source.DisplayName == "" || !utf8.ValidString(source.DisplayName) || strings.ContainsRune(source.DisplayName, 0) || utf8.RuneCountInString(source.DisplayName) > 160 {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source display name is required")
	}
	switch source.Type {
	case SourceARDManifest, SourceAICatalog, SourceRegistry, SourceHybrid:
	default:
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source type is invalid")
	}
	if strings.TrimSpace(source.Endpoint) == "" || strings.TrimSpace(source.ProfileID) == "" {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source endpoint and profile are required")
	}
	if source.UpdateMode != UpdateManual && source.UpdateMode != UpdateAutomatic {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source update mode is invalid")
	}
	if err := validateScope(source.Scope); err != nil {
		return Source{}, err
	}
	source.Scope = normalizeScope(source.Scope)
	if len(source.Scope.Identifiers) == 0 && len(source.Scope.Versions) == 0 && len(source.Scope.Kinds) == 0 && len(source.Scope.Publishers) == 0 && len(source.Scope.Tags) == 0 {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source scope must remain constrained")
	}
	nesting, err := normalizeNestingPolicy(source.Nesting)
	if err != nil {
		return Source{}, err
	}
	source.Nesting = nesting
	if err := validateAccessReferences(source.NetworkProfile, source.CredentialBinding); err != nil {
		return Source{}, err
	}
	if source.RetrievalMode == "" {
		switch {
		case source.Type == SourceRegistry && source.RegistryListURL != "" && source.Live.Enabled:
			source.RetrievalMode = RetrievalHybrid
		case source.Type == SourceRegistry && source.RegistryListURL != "":
			source.RetrievalMode = RetrievalIndexed
		case source.Type == SourceRegistry && source.Live.Enabled:
			source.RetrievalMode = RetrievalLive
		case source.Live.Enabled:
			source.RetrievalMode = RetrievalHybrid
		default:
			source.RetrievalMode = RetrievalIndexed
		}
	}
	switch source.RetrievalMode {
	case RetrievalIndexed:
		if source.Live.Enabled {
			return Source{}, catalog.E(catalog.CodeInvalidArgument, "indexed retrieval cannot enable live federation")
		}
		if source.Type == SourceRegistry && source.RegistryListURL == "" {
			return Source{}, catalog.E(catalog.CodePreconditionFailed, "indexed Registry retrieval requires an inspected List endpoint")
		}
	case RetrievalLive, RetrievalHybrid:
		if !source.Live.Enabled || source.RegistrySearchURL == "" {
			return Source{}, catalog.E(catalog.CodePreconditionFailed, "live or hybrid retrieval requires an acknowledged Registry search endpoint")
		}
		if source.RetrievalMode == RetrievalHybrid && source.Type == SourceRegistry && source.RegistryListURL == "" {
			return Source{}, catalog.E(catalog.CodePreconditionFailed, "hybrid Registry retrieval requires an inspected List endpoint")
		}
	default:
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source retrieval mode is invalid")
	}
	if source.UpdateMode == UpdateAutomatic && (source.RetrievalMode == RetrievalLive || source.Type == SourceRegistry && source.RegistryListURL == "") {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "automatic maintenance requires an enumerable indexed or hybrid source")
	}
	if source.Live.Enabled && !source.Live.QueryPrivacyAcknowledged {
		return Source{}, catalog.E(catalog.CodeSourcePolicyDenied, "live federation requires an explicit query privacy acknowledgement")
	}
	if source.Live.ReferralOnly && !source.Live.Enabled {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "referral-only participation requires live federation")
	}
	if source.Live.Enabled {
		if _, valid := canonicalEndpoint(source.RegistrySearchURL); !valid {
			return Source{}, catalog.E(catalog.CodeInvalidArgument, "live federation requires a valid Registry search endpoint")
		}
		connected, listErr := m.store.SourceEndpointConnected(ctx, source.TenantID, source.ID, source.RegistrySearchURL)
		if listErr != nil {
			return Source{}, listErr
		}
		if connected {
			return Source{}, catalog.E(catalog.CodeConflict, "Registry search endpoint is already connected")
		}
	}
	if source.RegistryListURL != "" {
		if endpoint := registryListEndpoint(source.RegistryListURL); endpoint == "" || endpoint != source.RegistryListURL {
			return Source{}, catalog.E(catalog.CodeInvalidArgument, "Registry List endpoint is invalid or not normalized")
		}
	}
	if source.Live.FederationMode != "" && source.Live.FederationMode != "none" && source.Live.FederationMode != "referrals" {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "upstream live requests must use none or bounded referrals")
	}
	if source.Live.FederationMode == "referrals" {
		source.Referrals.Enabled = true
	}
	if source.Referrals.Enabled {
		if source.RegistrySearchURL == "" {
			return Source{}, catalog.E(catalog.CodePreconditionFailed, "referral participation requires a Registry search endpoint")
		}
		if source.Referrals.MaxHops == 0 {
			source.Referrals.MaxHops = 1
		}
		if source.Referrals.MaxHops < 1 || source.Referrals.MaxHops > m.maxHops || len(source.Referrals.ApprovedSourceIDs) > m.maxFanout {
			return Source{}, catalog.E(catalog.CodeInvalidArgument, "source referral limits exceed the deployment bounds")
		}
		source.Live.FederationMode = "referrals"
	} else {
		source.Referrals = ReferralPolicy{}
		source.Live.FederationMode = "none"
	}
	assignments, hasAnonymous, err := m.normalizeDiscoveryAssignments(source)
	if err != nil {
		return Source{}, err
	}
	source.DiscoveryAssignments = assignments
	if (source.Live.Enabled || source.Referrals.Enabled) && len(assignments) == 0 {
		return Source{}, catalog.E(catalog.CodeSourcePolicyDenied, "live or referral participation requires an exact discovery host and audience assignment")
	}
	if hasAnonymous && source.NetworkProfile != "" {
		return Source{}, catalog.E(catalog.CodeSourcePolicyDenied, "a private-network source cannot participate in anonymous discovery")
	}
	if hasAnonymous && source.Live.Enabled && !source.Live.AnonymousDisclosureAcknowledged {
		return Source{}, catalog.E(catalog.CodeSourcePolicyDenied, "anonymous live federation requires explicit source-origin disclosure acknowledgement")
	}
	if hasAnonymous && source.Referrals.Enabled && !source.Referrals.AnonymousDisclosureAcknowledged {
		return Source{}, catalog.E(catalog.CodeSourcePolicyDenied, "anonymous referrals require explicit disclosure acknowledgement")
	}
	approvedSourceIDs := normalizeStrings(source.Referrals.ApprovedSourceIDs)
	if len(approvedSourceIDs) != len(source.Referrals.ApprovedSourceIDs) {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "approved referral source identifiers must be non-empty and unique")
	}
	source.Referrals.ApprovedSourceIDs = approvedSourceIDs
	if len(source.Referrals.ApprovedSourceIDs) > m.maxFanout {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source referral topology exceeds the deployment fan-out")
	}
	if len(source.Referrals.ApprovedSourceIDs) > 0 {
		byID, listErr := m.referralTopology(ctx, source)
		if listErr != nil {
			return Source{}, listErr
		}
		for _, approvedID := range source.Referrals.ApprovedSourceIDs {
			candidate, exists := byID[approvedID]
			if !exists || approvedID == source.ID || candidate.Status == SourceRemoved || candidate.RegistrySearchURL == "" || !assignmentsOverlap(source.DiscoveryAssignments, candidate.DiscoveryAssignments) {
				return Source{}, catalog.E(catalog.CodeSourcePolicyDenied, "approved referral source is unavailable in the same discovery audience")
			}
		}
		byID[source.ID] = source
		if referralPathReachesSource(source.ID, source.Referrals.ApprovedSourceIDs, byID) {
			return Source{}, catalog.E(catalog.CodeSourcePolicyDenied, "approved referral topology contains a cycle")
		}
	}
	return source, nil
}

// referralTopology loads the bounded approved source graph needed to validate
// referral relationships.
func (m *Manager) referralTopology(ctx context.Context, source Source) (map[string]Source, error) {
	byID := map[string]Source{source.ID: source}
	pending := append([]string(nil), source.Referrals.ApprovedSourceIDs...)
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		currentID := pending[0]
		pending = pending[1:]
		if _, exists := byID[currentID]; exists {
			continue
		}
		if len(byID) == MaxReferralTopologySources {
			return nil, catalog.E(catalog.CodeWorkLimitExceeded, "approved referral topology exceeds the supported inspection limit")
		}
		current, err := m.store.GetSource(ctx, source.TenantID, currentID)
		if err != nil {
			if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
				continue
			}
			return nil, err
		}
		byID[currentID] = current
		pending = append(pending, current.Referrals.ApprovedSourceIDs...)
	}
	return byID, nil
}

// referralPathReachesSource detects whether proposed referral edges would reach
// the originating source.
func referralPathReachesSource(sourceID string, starts []string, sources map[string]Source) bool {
	pending := append([]string(nil), starts...)
	visited := make(map[string]struct{}, len(pending))
	for len(pending) > 0 {
		currentID := pending[0]
		pending = pending[1:]
		if currentID == sourceID {
			return true
		}
		if _, seen := visited[currentID]; seen {
			continue
		}
		visited[currentID] = struct{}{}
		current, exists := sources[currentID]
		if !exists || !current.Referrals.Enabled {
			continue
		}
		pending = append(pending, current.Referrals.ApprovedSourceIDs...)
	}
	return false
}

// normalizeDiscoveryAssignments validates and canonicalizes source
// host/audience assignments against configured targets.
func (m *Manager) normalizeDiscoveryAssignments(source Source) ([]DiscoveryAssignment, bool, error) {
	if len(source.DiscoveryAssignments) > 16 {
		return nil, false, catalog.E(catalog.CodeInvalidArgument, "source has too many discovery assignments")
	}
	assignments := append([]DiscoveryAssignment(nil), source.DiscoveryAssignments...)
	hasAnonymous := false
	seen := make(map[string]struct{}, len(assignments))
	for index := range assignments {
		assignment := &assignments[index]
		assignment.Host = strings.ToLower(strings.TrimSpace(assignment.Host))
		if assignment.Audience != AudienceAnonymous && assignment.Audience != AudienceAuthenticated {
			return nil, false, catalog.E(catalog.CodeInvalidArgument, "source discovery audience is invalid")
		}
		target, exists := m.targets[assignment.Host]
		if !exists || target.TenantID != source.TenantID || target.Audience != assignment.Audience {
			return nil, false, catalog.E(catalog.CodeSourcePolicyDenied, "source discovery assignment is not configured for this tenant and audience")
		}
		key := assignment.Host + "\x00" + string(assignment.Audience)
		if _, exists := seen[key]; exists {
			return nil, false, catalog.E(catalog.CodeInvalidArgument, "source discovery assignments must be unique")
		}
		seen[key] = struct{}{}
		hasAnonymous = hasAnonymous || assignment.Audience == AudienceAnonymous
	}
	sort.Slice(assignments, func(i, j int) bool {
		if assignments[i].Host != assignments[j].Host {
			return assignments[i].Host < assignments[j].Host
		}
		return assignments[i].Audience < assignments[j].Audience
	})
	return assignments, hasAnonymous, nil
}

// assignmentsOverlap reports whether two source assignment sets share a
// host/audience target.
func assignmentsOverlap(left, right []DiscoveryAssignment) bool {
	for _, leftAssignment := range left {
		for _, rightAssignment := range right {
			if leftAssignment == rightAssignment {
				return true
			}
		}
	}
	return false
}

// applyConnectivityInspection requires retained inspection evidence before
// accepting changed endpoint, profile or Registry-interface bindings.
func (m *Manager) applyConnectivityInspection(ctx context.Context, tenantID string, current, proposed Source, input PatchInput) (Source, error) {
	connectivityChanged := proposed.NetworkProfile != current.NetworkProfile || proposed.CredentialBinding != current.CredentialBinding
	if input.InspectionID == "" {
		if connectivityChanged {
			return Source{}, catalog.E(catalog.CodePreconditionFailed, "connectivity changes require a current source inspection")
		}
		return proposed, nil
	}
	inspection, err := m.store.GetInspection(ctx, tenantID, input.InspectionID)
	if err != nil {
		return Source{}, err
	}
	if !inspection.ExpiresAt.After(m.clock.Now()) {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source inspection has expired; inspect the source again")
	}
	if m.fetcher.PolicyDigest() != inspection.PolicyDigest {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "federation policy changed after inspection; inspect the source again")
	}
	if inspection.ResolvedURL != current.Endpoint || inspection.SourceType != current.Type || inspection.ProfileID != current.ProfileID || inspection.RegistrySearchURL != current.RegistrySearchURL || inspection.RegistryListURL != current.RegistryListURL {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source inspection does not describe the current source interface")
	}
	if input.NetworkProfile == nil && inspection.NetworkProfile != current.NetworkProfile {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "a network profile change must be explicit in the source patch")
	}
	if input.CredentialBinding == nil && inspection.CredentialBinding != current.CredentialBinding {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "a credential binding change must be explicit in the source patch")
	}
	if input.NetworkProfile != nil && strings.TrimSpace(*input.NetworkProfile) != inspection.NetworkProfile || input.CredentialBinding != nil && strings.TrimSpace(*input.CredentialBinding) != inspection.CredentialBinding {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "source connectivity must match the selected inspection")
	}
	proposed.NetworkProfile = inspection.NetworkProfile
	proposed.CredentialBinding = inspection.CredentialBinding
	proposed.PolicyID = inspection.PolicyID
	proposed.PolicyRevision = inspection.PolicyRevision
	proposed.PolicyDigest = inspection.PolicyDigest
	proposed.CredentialRevision = inspection.CredentialRevision
	proposed.InspectionID = inspection.ID
	return proposed, nil
}

// applySourcePatch constructs a candidate revision without granting it new
// connectivity authority; inspection binding is checked separately.
func applySourcePatch(source Source, input PatchInput) (Source, error) {
	if input.TrustVerificationPolicy == nil && input.InspectionID == "" && input.DisplayName == nil && input.RetrievalMode == nil && input.Scope == nil && input.UpdateMode == nil && input.Live == nil && input.Referrals == nil && input.DiscoveryAssignments == nil && input.Nesting == nil && input.CredentialBinding == nil && input.NetworkProfile == nil && input.ArtworkEnabled == nil {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source patch contains no changes")
	}
	if input.TrustVerificationPolicy != nil {
		source.TrustVerificationPolicy = *input.TrustVerificationPolicy
	}
	if input.ArtworkEnabled != nil {
		source.ArtworkEnabled = *input.ArtworkEnabled
	}
	if input.DisplayName != nil {
		source.DisplayName = strings.TrimSpace(*input.DisplayName)
	}
	if input.RetrievalMode != nil {
		source.RetrievalMode = *input.RetrievalMode
	}
	if input.Scope != nil {
		if err := validateScope(*input.Scope); err != nil {
			return Source{}, err
		}
		source.Scope = normalizeScope(*input.Scope)
	}
	if input.UpdateMode != nil {
		source.UpdateMode = *input.UpdateMode
	}
	if input.Live != nil {
		source.Live = *input.Live
	}
	if input.Referrals != nil {
		source.Referrals = *input.Referrals
	}
	if input.DiscoveryAssignments != nil {
		source.DiscoveryAssignments = append([]DiscoveryAssignment(nil), (*input.DiscoveryAssignments)...)
	}
	if input.Nesting != nil {
		source.Nesting = *input.Nesting
	}
	if input.CredentialBinding != nil {
		source.CredentialBinding = strings.TrimSpace(*input.CredentialBinding)
	}
	if input.NetworkProfile != nil {
		source.NetworkProfile = strings.TrimSpace(*input.NetworkProfile)
	}
	if source.DisplayName == "" || !utf8.ValidString(source.DisplayName) || strings.ContainsRune(source.DisplayName, 0) || utf8.RuneCountInString(source.DisplayName) > 160 {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source display name is required")
	}
	if len(source.Scope.Identifiers) == 0 && len(source.Scope.Versions) == 0 && len(source.Scope.Kinds) == 0 && len(source.Scope.Publishers) == 0 && len(source.Scope.Tags) == 0 {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source scope must remain constrained")
	}
	if source.UpdateMode != UpdateManual && source.UpdateMode != UpdateAutomatic {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "source update mode is invalid")
	}
	if source.Live.Enabled && !source.Live.QueryPrivacyAcknowledged {
		return Source{}, catalog.E(catalog.CodeSourcePolicyDenied, "live federation requires an explicit query privacy acknowledgement")
	}
	if source.Live.FederationMode != "" && source.Live.FederationMode != "none" && source.Live.FederationMode != "referrals" {
		return Source{}, catalog.E(catalog.CodeInvalidArgument, "upstream live requests must use none or bounded referrals")
	}
	if source.Live.Enabled && source.RegistrySearchURL == "" {
		return Source{}, catalog.E(catalog.CodePreconditionFailed, "this source does not advertise an ARD Registry search endpoint")
	}
	nesting, err := normalizeNestingPolicy(source.Nesting)
	if err != nil {
		return Source{}, err
	}
	source.Nesting = nesting
	if err := validateAccessReferences(source.NetworkProfile, source.CredentialBinding); err != nil {
		return Source{}, err
	}
	return source, nil
}

// sourceConfigurationDigest fingerprints administrator-managed source
// configuration independently of mutable sync progress.
func sourceConfigurationDigest(source Source) (string, error) {
	configuration := struct {
		TrustVerificationPolicy string                `json:"trustVerificationPolicy,omitempty"`
		ArtworkEnabled          bool                  `json:"artworkEnabled"`
		PresentationGeneration  int64                 `json:"presentationGeneration"`
		DisplayName             string                `json:"displayName"`
		RegistrySearchURL       string                `json:"registrySearchUrl"`
		RegistryListURL         string                `json:"registryListUrl"`
		RetrievalMode           RetrievalMode         `json:"retrievalMode"`
		Scope                   Scope                 `json:"scope"`
		UpdateMode              UpdateMode            `json:"updateMode"`
		Live                    LivePolicy            `json:"live"`
		Referrals               ReferralPolicy        `json:"referrals"`
		DiscoveryAssignments    []DiscoveryAssignment `json:"discoveryAssignments"`
		Nesting                 NestingPolicy         `json:"nesting"`
		CredentialBinding       string                `json:"credentialBinding"`
		NetworkProfile          string                `json:"networkProfile"`
	}{
		TrustVerificationPolicy: source.TrustVerificationPolicy,
		ArtworkEnabled:          source.ArtworkEnabled,
		PresentationGeneration:  EffectivePresentationGeneration(source),
		DisplayName:             source.DisplayName,
		RegistrySearchURL:       source.RegistrySearchURL,
		RegistryListURL:         source.RegistryListURL,
		RetrievalMode:           source.RetrievalMode,
		Scope:                   source.Scope,
		UpdateMode:              source.UpdateMode,
		Live:                    source.Live,
		Referrals:               source.Referrals,
		DiscoveryAssignments:    source.DiscoveryAssignments,
		Nesting:                 source.Nesting,
		CredentialBinding:       source.CredentialBinding,
		NetworkProfile:          source.NetworkProfile,
	}
	raw, err := json.Marshal(configuration)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// ValidateRetainedInspectionIntegrity verifies immutable inspection evidence
// without consulting today's outbound policy. Retained policy identity is required.
func ValidateRetainedInspectionIntegrity(inspection Inspection) error {
	if inspection.ID == "" || inspection.TenantID == "" || !catalog.ValidExternalHTTPSURL(inspection.RequestedURL) || !catalog.ValidExternalHTTPSURL(inspection.ResolvedURL) || !safeRetrievalText(inspection.ProfileID, 128, false) || !safeRetrievalText(inspection.MediaType, 255, false) || !validPolicyDigest(inspection.BodyDigest) || inspection.EntryCount < 0 || inspection.EntryCount > MaxSynchronizationEntries || inspection.CreatedAt.IsZero() || !inspection.ExpiresAt.After(inspection.CreatedAt) {
		return catalog.E(catalog.CodeInvalidArgument, "source inspection evidence is invalid")
	}
	switch inspection.SourceType {
	case SourceARDManifest, SourceAICatalog, SourceRegistry, SourceHybrid:
	default:
		return catalog.E(catalog.CodeInvalidArgument, "source inspection type is invalid")
	}
	if inspection.RegistrySearchURL != "" && !catalog.ValidExternalHTTPSURL(inspection.RegistrySearchURL) || inspection.RegistryListURL != "" && !catalog.ValidExternalHTTPSURL(inspection.RegistryListURL) || validateAccessReferences(inspection.NetworkProfile, inspection.CredentialBinding) != nil || !safeRetrievalText(inspection.CredentialRevision, 128, true) {
		return catalog.E(catalog.CodeInvalidArgument, "source inspection access evidence is invalid")
	}
	if len(inspection.KindCounts) > 16 || len(inspection.Publishers) > inspection.EntryCount || len(inspection.Tags) > MaxSynchronizationEntries || len(inspection.Capabilities) > 16 || len(inspection.Warnings) > 100 {
		return catalog.E(catalog.CodeInvalidArgument, "source inspection aggregate evidence exceeds its bounds")
	}
	totalKinds := 0
	for kind, count := range inspection.KindCounts {
		if !safeRetrievalText(kind, 64, false) || count < 0 || count > inspection.EntryCount {
			return catalog.E(catalog.CodeInvalidArgument, "source inspection kind evidence is invalid")
		}
		totalKinds += count
	}
	if totalKinds > inspection.EntryCount || !sortedUniqueSafeStrings(inspection.Publishers, 512) || !sortedUniqueSafeStrings(inspection.Tags, 256) {
		return catalog.E(catalog.CodeInvalidArgument, "source inspection summary evidence is invalid")
	}
	if inspection.PolicyID == "" && inspection.PolicyRevision == 0 && inspection.PolicyDigest == "" {
		return catalog.E(catalog.CodeDependencyUnavailable, "source inspection requires current policy evidence")
	}
	if !validProfileReference(inspection.PolicyID) || inspection.PolicyRevision < 1 || !validPolicyDigest(inspection.PolicyDigest) {
		return catalog.E(catalog.CodeInvalidArgument, "source inspection policy evidence is invalid")
	}
	return nil
}

// sortedUniqueSafeStrings checks bounded, strictly ordered, unique string
// evidence without rewriting it.
func sortedUniqueSafeStrings(values []string, limit int) bool {
	previous := ""
	for _, value := range values {
		if !safeRetrievalText(value, limit, false) || previous != "" && value <= previous {
			return false
		}
		previous = value
	}
	return true
}

// ValidateChangePreviewIntegrity checks the immutable preview digest and its
// row-independent aggregate relationships without applying current policy.
func ValidateChangePreviewIntegrity(preview ChangePreview) error {
	if preview.ID == "" || preview.TenantID == "" || preview.SourceID == "" || preview.SourceRevision < 1 || preview.Intent != ChangeUpdate && preview.Intent != ChangeRemove || preview.CreatedAt.IsZero() || !preview.ExpiresAt.After(preview.CreatedAt) || preview.CurrentMatched < 0 || preview.ProposedMatched < 0 || preview.ArtworkOverridesAffected < 0 || preview.ArtworkOverridesAffected > MaxSynchronizationEntries || preview.ExcludedObservations != max(0, preview.CurrentMatched-preview.ProposedMatched) || !validPolicyDigest(preview.ConfigurationDigest) || !validPolicyDigest(preview.ObservationSetDigest) || !validPolicyDigest(preview.Digest) || preview.Intent == ChangeRemove && (preview.ProposedMatched != 0 || preview.SendsQueriesUpstream) {
		return catalog.E(catalog.CodeInvalidArgument, "source change preview evidence is invalid")
	}
	// Zero is the neutral additive consequence: preserve the established digest
	// of immutable previews that did not retain this optional count.
	digestFields := map[string]any{"id": preview.ID, "tenantId": preview.TenantID, "sourceId": preview.SourceID, "revision": preview.SourceRevision, "intent": preview.Intent, "configurationDigest": preview.ConfigurationDigest, "observationSetDigest": preview.ObservationSetDigest, "currentMatched": preview.CurrentMatched, "proposedMatched": preview.ProposedMatched, "expiresAt": preview.ExpiresAt}
	if preview.ArtworkOverridesAffected != 0 {
		digestFields["artworkOverridesAffected"] = preview.ArtworkOverridesAffected
	}
	digestInput, err := json.Marshal(digestFields)
	if err != nil {
		return catalog.Wrap(catalog.CodeInternal, "encode source change preview", err)
	}
	digestValue := sha256.Sum256(digestInput)
	if preview.Digest != "sha256:"+hex.EncodeToString(digestValue[:]) {
		return catalog.E(catalog.CodeDependencyUnavailable, "source change preview digest is inconsistent")
	}
	return nil
}

// normalizeNestingPolicy validates bounded nesting depth and allowed origins
// before canonicalizing policy.
func normalizeNestingPolicy(policy NestingPolicy) (NestingPolicy, error) {
	if !policy.Enabled {
		return NestingPolicy{}, nil
	}
	if policy.MaxDepth == 0 {
		policy.MaxDepth = 4
	}
	if policy.MaxDepth < 1 || policy.MaxDepth > 4 || len(policy.AllowedOrigins) > 16 {
		return NestingPolicy{}, catalog.E(catalog.CodeInvalidArgument, "nested Catalog depth or origin policy is invalid")
	}
	origins := make([]string, 0, len(policy.AllowedOrigins))
	for _, value := range policy.AllowedOrigins {
		value = strings.TrimSpace(value)
		if !catalog.ValidExternalHTTPSURL(value) {
			return NestingPolicy{}, catalog.E(catalog.CodeInvalidArgument, "nested Catalog origins must be safe HTTPS origins")
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return NestingPolicy{}, catalog.E(catalog.CodeInvalidArgument, "nested Catalog origins cannot contain a path, query, or fragment")
		}
		origin, err := fetcher.NormalizeOrigin(value, false)
		if err != nil {
			return NestingPolicy{}, catalog.E(catalog.CodeInvalidArgument, "nested Catalog origins must be normalized HTTPS origins")
		}
		if !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
	}
	sort.Strings(origins)
	policy.AllowedOrigins = origins
	return policy, nil
}

// Sync runs source synchronization without an explicit caller-supplied revision
// fence.
func (m *Manager) Sync(ctx context.Context, sourceID string) (Snapshot, error) {
	return m.sync(ctx, sourceID, 0)
}

// SyncAtRevision runs a source sync only when the source still has the revision
// that was authorized and enqueued by the caller.
func (m *Manager) SyncAtRevision(ctx context.Context, sourceID string, expectedRevision int64) (Snapshot, error) {
	if expectedRevision <= 0 {
		return Snapshot{}, catalog.E(catalog.CodeInvalidArgument, "a positive source revision is required")
	}
	return m.sync(ctx, sourceID, expectedRevision)
}

// sync performs network enumeration outside a store transaction, then commits
// the immutable snapshot, observations, source state, and evidence together.
// An incomplete enumeration never receives withdrawal authority.
func (m *Manager) sync(ctx context.Context, sourceID string, expectedRevision int64) (response Snapshot, resultErr error) {
	ctx = identity.WithPermissionContext(ctx)
	ctx, endTrace := catalog.StartTrace(ctx, "federation sync", catalog.TraceInternal, "")
	defer func() {
		outcome := "ok"
		if resultErr != nil {
			outcome = "error"
		} else if !response.EnumerationComplete {
			outcome = "partial"
		}
		endTrace(outcome)
	}()
	actor, err := permissionActor(ctx, "source.sync")
	if err != nil {
		return Snapshot{}, err
	}
	if err := identity.RequireInternalTarget(ctx, "source.sync", actor.TenantID, sourceID); err != nil {
		return Snapshot{}, catalog.Wrap(catalog.CodeForbidden, "claimed source target does not match", err)
	}
	source, err := m.store.GetSource(ctx, actor.TenantID, sourceID)
	if err != nil {
		return Snapshot{}, err
	}
	if source.Status != SourceActive && source.Status != SourceError {
		return Snapshot{}, catalog.E(catalog.CodePreconditionFailed, "only an active or error source can sync")
	}
	if expectedRevision > 0 && source.Revision != expectedRevision {
		return Snapshot{}, catalog.E(catalog.CodePreconditionFailed, "source changed after the sync was requested")
	}
	if !m.sourceUsesCurrentPolicy(source) {
		return Snapshot{}, catalog.E(catalog.CodePreconditionFailed, "source must pass a test under the active federation policy before it can synchronize")
	}
	now := catalog.TransitionTime(m.clock.Now(), source.UpdatedAt)
	snapshotID, err := m.ids.New(now)
	if err != nil {
		return Snapshot{}, catalog.Wrap(catalog.CodeInternal, "create source snapshot identifier", err)
	}
	snapshot := Snapshot{ID: snapshotID, TenantID: actor.TenantID, SourceID: source.ID, SourceRevision: source.Revision, State: SnapshotRunning, PolicyID: source.PolicyID, PolicyRevision: source.PolicyRevision, PolicyDigest: source.PolicyDigest, CredentialRevision: source.CredentialRevision, StartedAt: now}
	run := newSyncRun(source)
	syncCtx, cancel := context.WithTimeout(ctx, maxSyncDuration)
	defer cancel()
	syncCtx = context.WithValue(syncCtx, syncRunContextKey{}, run)
	entries, bodyDigest, credentialRevision, complete, findings, err := m.enumerate(syncCtx, source)
	complete = complete && !run.partial
	snapshot.Retrievals, snapshot.RetrievalStates, snapshot.RetrievalInvalidations = run.snapshotEvidence()
	// New retrieval evidence must lie within this run's ordered interval.
	// Normalize it before it is retained, without changing prior snapshots.
	completionFloor := snapshot.StartedAt
	for index := range snapshot.RetrievalStates {
		state := &snapshot.RetrievalStates[index]
		state.UpdatedAt = catalog.TransitionTime(state.UpdatedAt, snapshot.StartedAt)
		completionFloor = catalog.TransitionTime(completionFloor, state.UpdatedAt)
	}
	for index := range snapshot.Retrievals {
		attempt := &snapshot.Retrievals[index]
		attempt.FetchedAt = catalog.TransitionTime(attempt.FetchedAt, snapshot.StartedAt)
		completionFloor = catalog.TransitionTime(completionFloor, attempt.FetchedAt)
	}
	if credentialRevision != "" {
		snapshot.CredentialRevision = credentialRevision
	}
	if err != nil {
		completed := catalog.TransitionTime(m.clock.Now(), completionFloor)
		snapshot.State = SnapshotFailed
		snapshot.CompletedAt = &completed
		snapshot.Findings = append(snapshot.Findings, findings...)
		snapshot.Findings = append(snapshot.Findings, catalog.Finding{Code: "SOURCE_SYNC_FAILED", Severity: catalog.SeverityError, Message: safeSyncFailureMessage(err)})
		snapshot.RetrievalStates = nil
		if class, _ := ClassifySyncError(err); class != SyncFailureCancelled && class != SyncFailureInvalidInput && class != SyncFailurePolicy {
			source.Status = SourceError
			source.LastError = "Source sync failed. Inspect the latest snapshot for safe diagnostics."
		}
		source.LastSyncAt = &completed
		source.UpdatedAt = completed
		commitCtx, commitCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer commitCancel()
		commitCtx = audit.WithJobExecution(commitCtx, mustJobExecution(ctx))
		evidence, evidenceErr := m.syncEvidence(commitCtx, actor, source, snapshot, string(func() SyncFailureClass { class, _ := ClassifySyncError(err); return class }()))
		if evidenceErr != nil {
			return snapshot, errors.Join(err, evidenceErr)
		}
		if _, _, commitErr := m.store.CommitSync(commitCtx, snapshot, nil, source, source.Revision, evidence); commitErr != nil {
			return snapshot, errors.Join(err, commitErr)
		}
		return snapshot, err
	}
	observations := make([]Observation, 0, len(entries))
	for index, entry := range entries {
		if !scopeMatches(source.Scope, entry) {
			continue
		}
		observation, observationErr := m.observation(actor.TenantID, source, snapshot.ID, entry, index, now)
		if observationErr != nil {
			findings = appendBoundedFinding(findings, catalog.Finding{Code: "ENTRY_RETAINED_WITH_ERROR", Severity: catalog.SeverityWarning, Path: fmt.Sprintf("/entries/%d", index), Message: observationErr.Error()})
			continue
		}
		if !complete {
			observation.Freshness = FreshStale
		}
		observations = append(observations, observation)
	}
	completed := catalog.TransitionTime(m.clock.Now(), completionFloor)
	snapshot.State = SnapshotComplete
	if !complete {
		snapshot.State = SnapshotIncomplete
	}
	snapshot.EnumerationComplete = complete
	snapshot.WithdrawalAuthority = complete && (source.Type == SourceARDManifest || source.Type == SourceAICatalog || source.Type == SourceHybrid)
	snapshot.ReconcileCurrentView = complete && source.Type == SourceRegistry
	snapshot.BodyDigest = bodyDigest
	snapshot.MemberCount = len(observations)
	snapshot.CompletedAt = &completed
	snapshot.Findings = findings
	previous, overflow, err := m.store.ListCurrentObservations(ctx, actor.TenantID, source.ID, m.maxEntries)
	if err != nil {
		return Snapshot{}, err
	}
	if overflow {
		return Snapshot{}, catalog.E(catalog.CodeWorkLimitExceeded, "current source observations exceed the supported synchronization limit")
	}
	previousDigests := make(map[string]map[string]struct{})
	for _, observation := range previous {
		if previousDigests[observation.SourceKey] == nil {
			previousDigests[observation.SourceKey] = make(map[string]struct{})
		}
		previousDigests[observation.SourceKey][observation.IdentityDigest] = struct{}{}
	}
	for _, observation := range observations {
		if digests, ok := previousDigests[observation.SourceKey]; !ok {
			snapshot.Added++
		} else if _, unchanged := digests[observation.IdentityDigest]; !unchanged {
			snapshot.Updated++
		}
	}
	if complete {
		source.LastCompleteSnapshotID = snapshot.ID
	}
	if complete {
		source.Status = SourceActive
		source.LastError = ""
	} else {
		snapshot.RetrievalStates = nil
		source.Status = SourceError
		source.LastError = "Source sync was incomplete. The last complete membership remains active."
	}
	source.LastSyncAt = &completed
	source.UpdatedAt = completed
	failureClass := ""
	if !complete {
		failureClass = string(SyncFailureDependency)
	}
	evidence, err := m.syncEvidence(ctx, actor, source, snapshot, failureClass)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, _, err = m.store.CommitSync(ctx, snapshot, observations, source, source.Revision, evidence)
	if err != nil {
		return Snapshot{}, err
	}
	if !complete {
		return snapshot, catalog.E(catalog.CodeDependencyUnavailable, "source sync was incomplete within its configured nesting bounds")
	}
	return snapshot, nil
}

// mustJobExecution retrieves Job attribution, returning its zero value when the
// context has none. It does not validate worker authority.
func mustJobExecution(ctx context.Context) audit.JobExecution {
	execution, _ := audit.JobExecutionFrom(ctx)
	return execution
}

// safeSyncFailureMessage maps retry classifications to fixed client-safe text
// without exposing remote errors.
func safeSyncFailureMessage(err error) string {
	class, _ := ClassifySyncError(err)
	switch class {
	case SyncFailureAuthentication:
		return "Source authentication failed."
	case SyncFailureRateLimited:
		return "Source synchronization was rate limited."
	case SyncFailurePolicy:
		return "Source synchronization is not permitted by the current policy."
	case SyncFailurePermanent, SyncFailureInvalidInput:
		return "Source returned an unsupported or invalid representation."
	case SyncFailureCancelled:
		return "Source synchronization was cancelled or exceeded its deadline."
	case SyncFailureTransient, SyncFailureDependency, SyncFailureInternal:
		return "Source synchronization did not complete within its safe bounds."
	default:
		return "Source synchronization did not complete within its safe bounds."
	}
}

// ObservationPage validates source-administrator filters and returns a bounded
// tenant observation page.
func (m *Manager) ObservationPage(ctx context.Context, request ObservationListRequest) ([]Observation, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return nil, err
	}
	if request.SourceID != "" && !identity.ValidTenantID(request.SourceID) || request.Limit < 1 || request.Limit > 100 || request.AfterID == "" && (request.AfterIdentifier != "" || request.AfterVersion != "" || request.AfterVersionPresent) || request.AfterID != "" && !identity.ValidTenantID(request.AfterID) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "observation page position or limit is invalid")
	}
	if request.SourceID != "" {
		if _, err := m.store.GetSource(ctx, actor.TenantID, request.SourceID); err != nil {
			return nil, err
		}
	}
	request.Limit++
	return m.store.ListObservationPage(ctx, actor.TenantID, request)
}

// ObservationOccurrences returns an authorized bounded provenance sample and
// its total retained count.
func (m *Manager) ObservationOccurrences(ctx context.Context, observationID string, limit int) ([]ObservationOccurrence, int, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return nil, 0, err
	}
	if _, err := m.store.GetObservationMetadata(ctx, actor.TenantID, observationID); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	return m.store.ListObservationOccurrences(ctx, actor.TenantID, observationID, limit)
}

// ObservationOccurrencePage validates source-administrator continuation over
// retained observation provenance.
func (m *Manager) ObservationOccurrencePage(ctx context.Context, request OccurrenceListRequest) ([]ObservationOccurrence, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return nil, err
	}
	positionEmpty := request.AfterObservedAt == nil && request.AfterSnapshotID == "" && request.AfterRetrievalDigest == "" && request.AfterEntryLocation == ""
	positionValid := request.AfterObservedAt != nil && identity.ValidTenantID(request.AfterSnapshotID) && strings.HasPrefix(request.AfterRetrievalDigest, "sha256:") && request.AfterEntryLocation != ""
	if !identity.ValidTenantID(request.ObservationID) || request.Limit < 1 || request.Limit > 100 || !positionEmpty && !positionValid {
		return nil, catalog.E(catalog.CodeInvalidArgument, "observation-occurrence page position or limit is invalid")
	}
	if _, err := m.store.GetObservationMetadata(ctx, actor.TenantID, request.ObservationID); err != nil {
		return nil, err
	}
	request.Limit++
	return m.store.ListObservationOccurrencePage(ctx, actor.TenantID, request)
}

// ObservationGroup groups current external observations by identity and version
// presence while retaining source conflict diagnostics.
func (m *Manager) ObservationGroup(ctx context.Context, identifier, version string, versionPresent bool) (ObservationGroup, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read")
	if err != nil {
		return ObservationGroup{}, err
	}
	parsed, err := catalog.ParseAIRIdentifier(strings.TrimSpace(identifier))
	if err != nil {
		return ObservationGroup{}, catalog.E(catalog.CodeInvalidArgument, "an exact normalized urn:air identifier is required")
	}
	identifier = parsed.String()
	if (versionPresent && version == "") || (!versionPresent && version != "") || !validDiscoveryVersion(version) {
		return ObservationGroup{}, catalog.E(catalog.CodeInvalidArgument, "the typed observation version key is invalid")
	}
	items, err := m.store.ListObservationGroupMembers(ctx, actor.TenantID, identifier, version, versionPresent, MaxObservationGroupContributions+1)
	if err != nil {
		return ObservationGroup{}, err
	}
	if len(items) == 0 {
		return ObservationGroup{}, catalog.E(catalog.CodeNotFound, "observation group was not found")
	}
	truncated := len(items) > MaxObservationGroupContributions
	if truncated {
		items = items[:MaxObservationGroupContributions]
	}
	sourceIDs := make([]string, 0, len(items))
	observationIDs := make([]string, 0, len(items))
	for _, observation := range items {
		observationIDs = append(observationIDs, observation.ID)
		if !slices.Contains(sourceIDs, observation.SourceID) {
			sourceIDs = append(sourceIDs, observation.SourceID)
		}
	}
	conflict, sourceConflicts, err := m.store.ObservationGroupConflicts(ctx, actor.TenantID, identifier, version, versionPresent, sourceIDs)
	if err != nil {
		return ObservationGroup{}, err
	}
	summaries, err := m.store.ObservationOccurrenceSummaries(ctx, actor.TenantID, observationIDs)
	if err != nil {
		return ObservationGroup{}, err
	}
	group := ObservationGroup{Identifier: identifier, Version: version, VersionPresent: versionPresent, Kind: items[0].Kind, Conflict: conflict, ContributionsTruncated: truncated, Members: make([]ObservationGroupMember, 0, len(items))}
	memberIndex := make(map[string]int, len(items))
	for _, observation := range items {
		summary, ok := summaries[observation.ID]
		if !ok || summary.Total < 1 || summary.Latest.ObservationID != observation.ID {
			return ObservationGroup{}, catalog.E(catalog.CodeDependencyUnavailable, "observation occurrence evidence is missing")
		}
		index, exists := memberIndex[observation.NormalizedEntryDigest]
		if !exists {
			index = len(group.Members)
			memberIndex[observation.NormalizedEntryDigest] = index
			group.Members = append(group.Members, ObservationGroupMember{NormalizedEntryDigest: observation.NormalizedEntryDigest, Contributions: make([]ObservationGroupContribution, 0, 1)})
		}
		group.Members[index].Contributions = append(group.Members[index].Contributions, ObservationGroupContribution{Observation: observation, OccurrenceCount: summary.Total, LatestOccurrence: summary.Latest, SourceConflict: sourceConflicts[observation.SourceID]})
	}
	sort.Slice(group.Members, func(i, j int) bool {
		return group.Members[i].NormalizedEntryDigest < group.Members[j].NormalizedEntryDigest
	})
	group.ID, err = ObservationGroupID(identifier, version, versionPresent)
	if err != nil {
		return ObservationGroup{}, err
	}
	return group, nil
}

// ObservationGroupID returns the stable identifier shared by federation
// services and persistence adapters for one exact typed observation identity.
func ObservationGroupID(identifier, version string, versionPresent bool) (string, error) {
	key, err := json.Marshal(struct {
		Profile        string `json:"profile"`
		Identifier     string `json:"identifier"`
		VersionPresent bool   `json:"versionPresent"`
		Version        string `json:"version"`
	}{"catalog-observation-group-v1", identifier, versionPresent, version})
	if err != nil {
		return "", catalog.Wrap(catalog.CodeInternal, "encode observation group identifier", err)
	}
	digest := sha256.Sum256(key)
	return "observation-group-v1:sha256:" + hex.EncodeToString(digest[:]), nil
}

// AdoptObservation verifies retained source occurrence and integrity bindings
// before delegating creation of an explicit local draft.
func (m *Manager) AdoptObservation(ctx context.Context, observationID string, input AdoptInput) (catalog.ExternalObservationAdoption, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "source.read_evidence")
	if err != nil {
		return catalog.ExternalObservationAdoption{}, err
	}
	if m.adopter == nil {
		return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodeDependencyUnavailable, "observation adoption is unavailable")
	}
	observation, err := m.store.GetObservation(ctx, actor.TenantID, observationID)
	if err != nil {
		return catalog.ExternalObservationAdoption{}, err
	}
	if observation.Freshness != FreshCurrent || observation.Completeness != CompleteEntry || observation.Compatibility.Structural != "valid" || observation.Compatibility.Support != "supported" || !observation.Kind.Valid() || observation.NormalizedEntryDigest == "" || len(observation.NormalizedEntry) == 0 {
		return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodePreconditionFailed, "only a current complete supported observation can be adopted")
	}
	source, err := m.store.GetSource(ctx, actor.TenantID, observation.SourceID)
	if err != nil {
		return catalog.ExternalObservationAdoption{}, err
	}
	if !scopeMatches(source.Scope, sourceEntry{Identifier: observation.Identifier, Version: observation.Version, Type: observation.MediaType, Tags: observation.SearchTags}) {
		return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodePreconditionFailed, "source scope excludes this observation")
	}
	occurrence, err := m.store.GetObservationOccurrence(ctx, actor.TenantID, observation.ID, input.SnapshotID, input.RetrievalDigest, input.EntryLocation)
	if err != nil {
		return catalog.ExternalObservationAdoption{}, err
	}
	if !occurrence.EvidenceComplete || occurrence.SourceID != observation.SourceID || occurrence.ExactEntryDigest != observation.ExactEntryDigest || occurrence.NormalizedEntryDigest != observation.NormalizedEntryDigest {
		return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodePreconditionFailed, "selected occurrence does not contain complete matching provenance")
	}
	snapshot, err := m.store.GetSnapshot(ctx, actor.TenantID, occurrence.SnapshotID)
	if err != nil {
		return catalog.ExternalObservationAdoption{}, err
	}
	if snapshot.State != SnapshotComplete || !snapshot.EnumerationComplete || snapshot.SourceRevision != occurrence.SourceRevision {
		return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodePreconditionFailed, "selected occurrence is not from a complete source snapshot")
	}
	_, sourceConflicts, err := m.store.ObservationGroupConflicts(ctx, actor.TenantID, observation.Identifier, observation.Version, observation.VersionPresent, []string{observation.SourceID})
	if err != nil {
		return catalog.ExternalObservationAdoption{}, err
	}
	if sourceConflicts[observation.SourceID] {
		return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodeConflict, "selected source has conflicting current observations for this exact identity and version")
	}
	localVersion := strings.TrimSpace(input.Version)
	if observation.VersionPresent {
		if localVersion != "" && localVersion != observation.Version {
			return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodeInvalidArgument, "local version must match the selected versioned observation")
		}
		localVersion = observation.Version
	} else if localVersion == "" {
		return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodeInvalidArgument, "an explicit local version is required for an unversioned observation")
	}
	var entry normalizedObservationEntry
	if err := json.Unmarshal(observation.NormalizedEntry, &entry); err != nil {
		return catalog.ExternalObservationAdoption{}, catalog.E(catalog.CodeDependencyUnavailable, "normalized observation evidence is invalid")
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = entry.DisplayName
	}
	description := strings.TrimSpace(input.Description)
	if description == "" {
		description = entry.Description
	}
	representation := catalog.RepresentationInput{Role: catalog.RepresentationPrimary, MediaType: entry.MediaType, ProfileID: observation.Compatibility.ProfileID, URL: entry.URL}
	if observation.Kind == catalog.KindPlugin {
		representation.ProfileID = catalog.PluginProfile
	}
	if observation.Kind == catalog.KindPlugin && len(entry.Metadata) > 0 {
		digest, err := catalog.PluginExpectedDigest(entry.Metadata)
		if err != nil {
			return catalog.ExternalObservationAdoption{}, err
		}
		representation.ExpectedDigest = digest
	}
	if len(entry.Data) > 0 && string(entry.Data) != "null" {
		representation.Data = append(json.RawMessage(nil), entry.Data...)
	}
	provenance := catalog.AdoptionProvenance{ObservationID: observation.ID, SourceID: observation.SourceID, SourceRevision: occurrence.SourceRevision, SnapshotID: occurrence.SnapshotID, RetrievalDigest: occurrence.RetrievalDigest, EntryLocation: occurrence.EntryLocation, ExactEntryDigest: occurrence.ExactEntryDigest, NormalizedEntryDigest: occurrence.NormalizedEntryDigest, NormalizationProfile: observation.NormalizationProfile}
	return m.adopter.AdoptExternalObservation(ctx, catalog.ExternalObservationAdoptionInput{SourceAdmissionRevision: source.Revision, AdmitCommit: func(ctx context.Context, commit func(context.Context) error) error {
		return identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			if memory, ok := m.store.(*MemoryStore); ok {
				return memory.withSourceRevision(ctx, actor.TenantID, source.ID, source.Revision, commit)
			}
			current, err := m.store.GetSource(ctx, actor.TenantID, source.ID)
			if err != nil {
				return err
			}
			if current.Revision != source.Revision {
				return catalog.E(catalog.CodePreconditionFailed, "source policy changed during adoption")
			}
			return commit(ctx)
		})
	}, NamespaceID: input.NamespaceID, CanonicalIdentifier: observation.Identifier, Kind: observation.Kind, Version: localVersion, Representation: representation, DisplayName: displayName, Description: description, Tags: entry.Tags, Categories: input.Categories, RepresentativeQueries: entry.RepresentativeQueries, PublisherID: input.PublisherID, DocumentationURL: input.DocumentationURL, SupportURL: input.SupportURL, License: input.License, TermsURL: input.TermsURL, Visibility: input.Visibility, Provenance: provenance})
}

// Search evaluates a bounded indexed observation query for the source
// administrator's tenant.
func (m *Manager) Search(ctx context.Context, query SearchQuery) (response []SearchResult, resultErr error) {
	ctx = identity.WithPermissionContext(ctx)
	ctx, endTrace := catalog.StartTrace(ctx, "federation search", catalog.TraceInternal, "")
	defer func() {
		outcome := "ok"
		if resultErr != nil {
			outcome = "error"
		}
		endTrace(outcome)
	}()
	items, err := m.SearchObservationPage(ctx, ObservationSearchRequest{Query: query, Limit: 100})
	if len(items) > 100 {
		items = items[:100]
	}
	return items, err
}

// SearchObservationPage validates indexed search filters and continuation
// before returning safe external observation results.
func (m *Manager) SearchObservationPage(ctx context.Context, request ObservationSearchRequest) ([]SearchResult, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "federation.discover")
	if err != nil {
		return nil, err
	}
	request.Query.Text = strings.Join(strings.Fields(request.Query.Text), " ")
	if len(request.Query.Text) > 500 || len(request.Query.Kinds) > 4 || len(request.Query.Tags) > 100 || request.Limit < 1 || request.Limit > 100 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "observation search is outside the supported limits")
	}
	seenKinds := make(map[catalog.Kind]struct{}, len(request.Query.Kinds))
	for _, kind := range request.Query.Kinds {
		if !kind.Valid() {
			return nil, catalog.E(catalog.CodeInvalidArgument, "observation search contains an invalid resource kind")
		}
		if _, duplicate := seenKinds[kind]; duplicate {
			return nil, catalog.E(catalog.CodeInvalidArgument, "observation search resource kinds must be unique")
		}
		seenKinds[kind] = struct{}{}
	}
	seenTags := make(map[string]struct{}, len(request.Query.Tags))
	for index, tag := range request.Query.Tags {
		if !utf8.ValidString(tag) || strings.ContainsRune(tag, '\x00') {
			return nil, catalog.E(catalog.CodeInvalidArgument, "observation search tags must be valid text without NUL")
		}
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || utf8.RuneCountInString(tag) > 80 {
			return nil, catalog.E(catalog.CodeInvalidArgument, "observation search tags cannot exceed 80 characters")
		}
		if _, duplicate := seenTags[tag]; duplicate {
			return nil, catalog.E(catalog.CodeInvalidArgument, "observation search tags must be unique")
		}
		seenTags[tag] = struct{}{}
		request.Query.Tags[index] = tag
	}
	positionEmpty := request.AfterIdentifier == "" && request.AfterID == "" && request.AfterScore == 0
	positionValid := identity.ValidTenantID(request.AfterID) && request.AfterScore >= 0 && request.AfterScore <= 100
	if !positionEmpty && !positionValid {
		return nil, catalog.E(catalog.CodeInvalidArgument, "observation-search page position is invalid")
	}
	request.Limit++
	return m.store.SearchObservationPage(ctx, actor.TenantID, request)
}

// SearchFederatedCatalog supplies the bounded indexed and transient portions
// of ordinary authenticated Catalog Search. Callers cannot select sources.
func (m *Manager) SearchFederatedCatalog(ctx context.Context, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "federation.discover")
	if err != nil {
		return catalog.FederatedCatalogSearchPage{}, err
	}
	page, err := m.store.SearchIndexedCatalog(ctx, actor.TenantID, request)
	if err != nil {
		return catalog.FederatedCatalogSearchPage{}, err
	}
	return m.searchLiveCatalog(ctx, request, page)
}

// SearchLiveCatalog augments an already completed durable search without
// repeating indexed selection or holding a database transaction during fetches.
func (m *Manager) SearchLiveCatalog(ctx context.Context, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
	ctx = identity.WithPermissionContext(ctx)
	if _, err := permissionActor(ctx, "federation.discover"); err != nil {
		return catalog.FederatedCatalogSearchPage{}, err
	}
	return m.searchLiveCatalog(ctx, request, catalog.FederatedCatalogSearchPage{})
}

// searchLiveCatalog composes approved live source results into the shared
// Catalog page with bounded attribution and diagnostics.
func (m *Manager) searchLiveCatalog(ctx context.Context, request catalog.FederatedCatalogSearchRequest, page catalog.FederatedCatalogSearchPage) (catalog.FederatedCatalogSearchPage, error) {
	page.Live = []catalog.CatalogSearchResult{}
	if !request.IncludeLive || !request.Query.IncludesOrigin(catalog.SearchResultLive) || strings.TrimSpace(request.Query.Text) == "" || request.Query.Visibility != "" && request.Query.Visibility != catalog.VisibilityTenant {
		return page, nil
	}
	filter := make(map[string]any)
	if len(request.Query.Kinds) > 0 {
		mediaTypes := make([]string, 0, len(request.Query.Kinds)*2)
		for _, kind := range request.Query.Kinds {
			mediaTypes = append(mediaTypes, catalogSearchMediaTypes(kind)...)
		}
		filter["type"] = mediaTypes
	}
	if len(request.Query.Tags) > 0 {
		filter["tags"] = append([]string(nil), request.Query.Tags...)
	}
	if request.Query.Publisher != "" {
		filter["publisher"] = request.Query.Publisher
	}
	if request.Query.License != "" {
		filter["metadata.license"] = request.Query.License
	}
	// ARD subjects use exact OR; Control categories use normalized AND.
	// Fetch a bounded superset and check declarations before candidate admission.
	liveLimit := min(request.Limit, MaxLiveSearchPageSize)
	if request.LiveLimit > 0 {
		liveLimit = min(request.LiveLimit, MaxLiveSearchPageSize)
	}
	if liveLimit <= 0 {
		liveLimit = 20
	}
	live, err := m.LiveSearch(ctx, LiveSearchRequest{Text: request.Query.Text, Filter: filter, PageSize: liveLimit, Replay: request.LiveLimit > 0, CatalogTenant: true, catalogQuery: request.Query})
	if err != nil {
		if request.LiveLimit > 0 && ctx.Err() == nil && (catalog.ErrorCodeOf(err) == catalog.CodeDependencyUnavailable || errors.Is(err, context.DeadlineExceeded)) {
			page.Partial = true
			appendCatalogSearchDiagnostic(&page, catalog.CatalogSearchDiagnostic{Status: "failed", Message: "Live federation was unavailable within the request deadline."})
			return page, nil
		}
		return catalog.FederatedCatalogSearchPage{}, err
	}
	page.Partial = live.Partial
	page.ReplayFingerprint = live.ReplayFingerprint
	for _, diagnostic := range live.Diagnostics {
		if diagnostic.Status == "succeeded" {
			continue
		}
		appendCatalogSearchDiagnostic(&page, catalog.CatalogSearchDiagnostic{SourceID: diagnostic.SourceID, Status: diagnostic.Status, Message: diagnostic.Message})
	}
	for _, liveResult := range live.Results {
		item, ok := catalogLiveResult(liveResult, request.Query)
		if !ok {
			page.ReplayFingerprint = ""
			page.Partial = true
			appendCatalogSearchDiagnostic(&page, catalog.CatalogSearchDiagnostic{SourceID: liveResult.SourceID, Status: "invalid_response", Message: "An upstream result was omitted because it did not satisfy the Catalog search contract."})
			continue
		}
		page.Live = append(page.Live, item)
	}
	sortCatalogSearchResults(page.Live)
	return page, nil
}

// appendCatalogSearchDiagnostic retains a diagnostic only within the Catalog
// search evidence bound.
func appendCatalogSearchDiagnostic(page *catalog.FederatedCatalogSearchPage, diagnostic catalog.CatalogSearchDiagnostic) {
	for _, existing := range page.Diagnostics {
		if existing.SourceID == diagnostic.SourceID && existing.Status == diagnostic.Status {
			return
		}
	}
	if len(page.Diagnostics) < MaxLiveDiagnostics {
		page.Diagnostics = append(page.Diagnostics, diagnostic)
	}
}

// catalogSearchMediaTypes maps governed kinds to the discovery media types
// accepted for live results.
func catalogSearchMediaTypes(kind catalog.Kind) []string {
	switch kind {
	case catalog.KindPlugin:
		return []string{catalog.PluginMediaZIP, catalog.PluginMediaGZIP}
	case catalog.KindAgent:
		return []string{adapter.MediaA2AAgentCard, adapter.MediaPrivateAgent}
	case catalog.KindMCPServer:
		return []string{adapter.MediaMCPServerCard, adapter.MediaMCPRegistry, adapter.MediaPrivateMCP}
	case catalog.KindSkill:
		// Include Markdown aliases accepted by kindForMediaType, so a peer's
		// exact ARD type filter does not exclude readable Skill metadata.
		return []string{adapter.MediaSkillJSON, adapter.MediaSkillZIP, adapter.MediaSkillGZIP, adapter.MediaSkillMarkdown, adapter.MediaSkillMarkdownARD}
	default:
		return nil
	}
}

// catalogLiveResult validates and projects a live entry into the shared search
// result shape under the requested filters.
func catalogLiveResult(result LiveResult, query catalog.SearchQuery) (catalog.CatalogSearchResult, bool) {
	var entry struct {
		Identifier  string   `json:"identifier"`
		Version     string   `json:"version"`
		Type        string   `json:"type"`
		DisplayName string   `json:"displayName"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Queries     []string `json:"representativeQueries"`
	}
	if jsonv2.Unmarshal(result.Entry, &entry, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false)) != nil || entry.Identifier != result.Identifier || entry.Version != result.Version {
		return catalog.CatalogSearchResult{}, false
	}
	identifier, err := catalog.ParseAIRIdentifier(entry.Identifier)
	if err != nil {
		return catalog.CatalogSearchResult{}, false
	}
	kind := kindForMediaType(entry.Type)
	if !kind.Valid() || len(query.Kinds) > 0 && !slices.Contains(query.Kinds, kind) {
		return catalog.CatalogSearchResult{}, false
	}
	tags := normalizedSearchTags(entry.Tags)
	queries := normalizedSearchQueries(entry.Queries)
	if len(query.Tags) > 0 && !hasAll(tags, query.Tags) || query.Publisher != "" && query.Publisher != identifier.Publisher {
		return catalog.CatalogSearchResult{}, false
	}
	displayName := strings.Join(strings.Fields(entry.DisplayName), " ")
	if displayName == "" {
		displayName = identifier.String()
	}
	description := strings.TrimSpace(entry.Description)
	if !validCatalogSearchProjection(displayName, description, tags, queries, identifier.Publisher) {
		return catalog.CatalogSearchResult{}, false
	}
	score := catalog.CoreLexicalScore(catalog.SearchDocument{Identifier: identifier.String(), DisplayName: displayName, Description: description, Tags: tags, RepresentativeQueries: queries}, query.Text)
	if query.Text != "" && score == 0 {
		return catalog.CatalogSearchResult{}, false
	}
	digest := sha256.Sum256(result.Entry)
	entryDigest := "sha256:" + hex.EncodeToString(digest[:])
	return catalog.CatalogSearchResult{
		ID: catalog.LiveSearchResultID(result.Source, identifier.String(), result.Version, result.VersionPresent, entryDigest), Type: catalog.SearchResultLive,
		Identifier: identifier.String(), Version: result.Version, VersionPresent: result.VersionPresent, Kind: kind, DisplayName: displayName, Description: description, Tags: tags, Publisher: identifier.Publisher, Score: score,
		Live: &catalog.LiveSearchEvidence{SourceRevision: result.SourceRevision, PresentationGeneration: result.PresentationGeneration, PolicyDigest: result.PolicyDigest, CredentialRevision: result.CredentialRevision, SourceID: result.SourceID, Source: result.Source, Entry: append(json.RawMessage(nil), result.Entry...), UpstreamScore: result.Score},
	}, true
}

// LiveSearch queries only active administrator-owned topology. Referrals can
// enqueue an independently connected source, never an arbitrary observed URL.
func (m *Manager) LiveSearch(ctx context.Context, request LiveSearchRequest) (LiveSearchResponse, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "federation.discover")
	if err != nil {
		return LiveSearchResponse{}, err
	}
	if err := identity.RequireInternalTarget(ctx, "federation.discover", actor.TenantID, request.DiscoveryHost); err != nil {
		return LiveSearchResponse{}, catalog.PermissionError("discovery host binding is required", err)
	}
	if request.Administrative {
		if _, err := permissionActor(ctx, "source.read"); err != nil {
			return LiveSearchResponse{}, err
		}
		reason, reasonErr := audit.NormalizeReason(audit.ActionFederationLiveSearchDiagnostic, request.Reason)
		if reasonErr != nil {
			return LiveSearchResponse{}, catalog.Wrap(catalog.CodeInvalidArgument, "live-search diagnostic reason is invalid", reasonErr)
		}
		request.Reason = reason
	} else if request.Reason != "" {
		return LiveSearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "reason is accepted only by administrator diagnostic live search")
	}
	request.Text = strings.Join(strings.Fields(request.Text), " ")
	if request.Text == "" || utf8.RuneCountInString(request.Text) > 500 {
		return LiveSearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "live search text is required")
	}
	if request.PageSize < 0 {
		return LiveSearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "live search page size must be positive")
	}
	if request.PageSize == 0 {
		request.PageSize = 10
	}
	if request.PageSize > MaxLiveSearchPageSize {
		return LiveSearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "live search page size cannot exceed 50")
	}
	if err := validateLiveSearchQuery(request.Context, request.Filter); err != nil {
		return LiveSearchResponse{}, err
	}
	evidenceCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, m.liveTimeout)
	defer cancel()
	switch {
	case request.CatalogTenant:
		if request.Administrative || len(request.SourceIDs) != 0 || request.DiscoveryHost != "" || request.Audience != "" {
			return LiveSearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "Catalog federation derives its source topology and accepts no source override")
		}
	case !request.Administrative:
		if len(request.SourceIDs) != 0 || request.DiscoveryHost == "" || request.Audience != AudienceAnonymous && request.Audience != AudienceAuthenticated {
			return LiveSearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "discovery federation requires an exact host and audience and accepts no source override")
		}
	default:
		for _, sourceID := range request.SourceIDs {
			if sourceID == "" || sourceID != strings.TrimSpace(sourceID) {
				return LiveSearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "administrator source selection must contain unique non-empty identifiers")
			}
		}
		normalizedSourceIDs := normalizeStrings(request.SourceIDs)
		if len(normalizedSourceIDs) != len(request.SourceIDs) {
			return LiveSearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "administrator source selection must contain unique non-empty identifiers")
		}
		request.SourceIDs = normalizedSourceIDs
	}
	directLimit := min(m.maxFanout, MaxDirectLiveSources)
	if len(request.SourceIDs) > directLimit {
		return LiveSearchResponse{}, catalog.E(catalog.CodeInvalidArgument, "administrator source selection exceeds the deployment fan-out")
	}
	var sources []Source
	var sourcesTruncated bool
	switch {
	case request.CatalogTenant:
		sources, sourcesTruncated, err = m.store.ListCatalogLiveSources(ctx, actor.TenantID, m.fetcher.PolicyDigest(), directLimit)
	case request.Administrative && len(request.SourceIDs) > 0:
		sources = make([]Source, 0, len(request.SourceIDs))
		for _, sourceID := range request.SourceIDs {
			source, sourceErr := m.store.GetSource(ctx, actor.TenantID, sourceID)
			if sourceErr != nil {
				if catalog.ErrorCodeOf(sourceErr) == catalog.CodeNotFound {
					return LiveSearchResponse{}, catalog.E(catalog.CodeSourcePolicyDenied, "one or more selected sources are not active live sources")
				}
				return LiveSearchResponse{}, sourceErr
			}
			sources = append(sources, source)
		}
	case request.Administrative:
		sources, sourcesTruncated, err = m.store.ListCatalogLiveSources(ctx, actor.TenantID, m.fetcher.PolicyDigest(), directLimit)
	default:
		sources, sourcesTruncated, err = m.store.ListDiscoveryLiveSources(ctx, actor.TenantID, request.DiscoveryHost, request.Audience, m.fetcher.PolicyDigest(), directLimit)
	}
	if err != nil {
		return LiveSearchResponse{}, err
	}
	byID := make(map[string]Source, len(sources))
	for _, source := range sources {
		byID[source.ID] = source
	}
	if request.Administrative && len(request.SourceIDs) > 0 {
		for _, sourceID := range request.SourceIDs {
			if _, exists := byID[sourceID]; !exists {
				// A missing object is rejected before the outbound boundary and
				// does not create attacker-amplifiable durable evidence.
				return LiveSearchResponse{}, catalog.E(catalog.CodeSourcePolicyDenied, "one or more selected sources are not active live sources")
			}
		}
	}
	selected := make([]Source, 0, len(sources))
	response := LiveSearchResponse{Results: make([]LiveResult, 0)}
	if sourcesTruncated {
		response.Partial = true
		appendLiveDiagnostic(&response, LiveDiagnostic{Status: "fanout_limited", Message: "Eligible live sources exceeded the deployment fan-out."})
	}
	for _, source := range sources {
		if request.Administrative && len(request.SourceIDs) > 0 && !slices.Contains(request.SourceIDs, source.ID) {
			continue
		}
		if source.Status != SourceActive || !source.Live.Enabled || source.RegistrySearchURL == "" || source.Live.ReferralOnly && (!request.Administrative || len(request.SourceIDs) == 0) {
			continue
		}
		if !request.Administrative && !request.CatalogTenant && !sourceEligibleForDiscovery(source, request.DiscoveryHost, request.Audience) {
			continue
		}
		if !m.sourceUsesCurrentPolicy(source) {
			response.Partial = true
			appendLiveDiagnostic(&response, LiveDiagnostic{SourceID: source.ID, Status: "policy_stale", Message: "Source policy evidence is not current."})
			continue
		}
		selected = append(selected, source)
	}
	if request.Administrative && len(request.SourceIDs) > 0 && len(selected) != len(request.SourceIDs) {
		return LiveSearchResponse{}, catalog.E(catalog.CodeSourcePolicyDenied, "one or more selected sources are not active live sources")
	}
	if len(selected) > directLimit {
		selected = selected[:directLimit]
		response.Partial = true
		appendLiveDiagnostic(&response, LiveDiagnostic{Status: "fanout_limited", Message: "Eligible sources exceeded the deployment fan-out."})
	}
	auditSources, liveOutcomes, err := m.traverseLivePeers(ctx, actor.TenantID, request, selected, &response)
	if err != nil {
		return LiveSearchResponse{}, err
	}
	if request.Administrative && len(auditSources) > 0 {
		if err := m.appendLiveSearchAudit(evidenceCtx, actor, request, auditSources, response, aggregateLiveOutcome(liveOutcomes, response.Partial)); err != nil {
			return LiveSearchResponse{}, err
		}
	}
	return response, nil
}

// appendLiveDiagnostic bounds per-response live discovery diagnostics.
func appendLiveDiagnostic(response *LiveSearchResponse, diagnostic LiveDiagnostic) {
	if len(response.Diagnostics) < MaxLiveDiagnostics-1 {
		response.Diagnostics = append(response.Diagnostics, diagnostic)
		return
	}
	if len(response.Diagnostics) == MaxLiveDiagnostics-1 {
		response.Diagnostics = append(response.Diagnostics, LiveDiagnostic{Status: "diagnostics_truncated", Message: "Additional source diagnostics were omitted."})
	}
}

// validateFetchEvidence requires valid outbound policy evidence and credential
// revision attribution when credentials were used.
func (m *Manager) validateFetchEvidence(result fetcher.Result, credentialed bool) error {
	if result.PolicyDigest != m.fetcher.PolicyDigest() || !validProfileReference(result.PolicyID) || result.PolicyRevision <= 0 {
		return catalog.E(catalog.CodeDependencyUnavailable, "federation fetch did not return current policy evidence")
	}
	if credentialed && (result.CredentialRevision == "" || result.CredentialRevision != strings.TrimSpace(result.CredentialRevision) || len(result.CredentialRevision) > 128 || strings.IndexFunc(result.CredentialRevision, unicode.IsControl) >= 0) {
		return catalog.E(catalog.CodeDependencyUnavailable, "credentialed federation fetch did not return valid revision evidence")
	}
	return nil
}

// fetchWithAccess applies source access, aggregate sync budgets and
// returned-policy/credential checks to each hardened fetch.
func (m *Manager) fetchWithAccess(ctx context.Context, access sourceAccess, endpoint string, request fetcher.Request) (fetcher.Result, error) {
	if err := access.operationError(); err != nil {
		return fetcher.Result{}, err
	}
	if run := syncRunFromContext(ctx); run != nil {
		if err := run.beforeFetch(); err != nil {
			return fetcher.Result{}, err
		}
	}
	outbound := access.request(endpoint, request)
	result, err := m.fetcher.Fetch(ctx, outbound)
	if err != nil {
		if errors.Is(err, fetcher.ErrPolicyChanged) {
			access.recordOperationError(err)
		}
		return fetcher.Result{}, err
	}
	credentialed := outbound.CredentialBinding != ""
	if err := m.validateFetchEvidence(result, credentialed); err != nil {
		access.recordOperationError(err)
		return fetcher.Result{}, err
	}
	if credentialed && access.credentialRevision != nil {
		if *access.credentialRevision == "" {
			*access.credentialRevision = result.CredentialRevision
		} else if *access.credentialRevision != result.CredentialRevision {
			err := errors.New("source credential changed during one federation operation")
			access.recordOperationError(err)
			return fetcher.Result{}, err
		}
	}
	if run := syncRunFromContext(ctx); run != nil {
		if err := run.accountBytes(len(result.Body)); err != nil {
			return fetcher.Result{}, err
		}
	}
	return result, nil
}

// validPolicyDigest reports whether a policy or provenance digest has the
// canonical SHA-256 spelling.
func validPolicyDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

// parseLiveScore accepts mathematical integers in [0,100], including decimal
// and exponent spellings. Decimal normalization avoids float rounding and
// unbounded big-number allocation for hostile exponents.
func parseLiveScore(raw []byte) (int, bool) {
	text := strings.TrimSpace(string(raw))
	var number json.Number
	if text == "" || text[0] == '"' || json.Unmarshal(raw, &number) != nil || number == "" {
		return 0, false
	}
	mantissa, exponentText, hasExponent := strings.Cut(strings.ToLower(string(number)), "e")
	negative := strings.HasPrefix(mantissa, "-")
	mantissa = strings.TrimPrefix(mantissa, "-")
	whole, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return 0, true
	}
	if negative {
		return 0, false
	}
	exponent := 0
	if hasExponent {
		var err error
		exponent, err = strconv.Atoi(exponentText)
		if err != nil || exponent > len(text)+3 || exponent < -len(text) {
			return 0, false
		}
	}
	significant := strings.TrimRight(digits, "0")
	power := exponent - len(fraction) + len(digits) - len(significant)
	if power < 0 || len(significant)+power > 3 {
		return 0, false
	}
	score, err := strconv.Atoi(significant)
	if err != nil {
		return 0, false
	}
	for range power {
		score *= 10
	}
	return score, score <= 100
}

type liveWireResponse struct {
	Results   []json.RawMessage `json:"results"`
	Referrals []liveReferral    `json:"referrals,omitempty"`
	PageToken string            `json:"pageToken,omitempty"`
}

type liveReferral struct {
	Identifier  string `json:"identifier"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
	URL         string `json:"url"`
}

// decodeLiveResponse rejects ambiguous or unknown envelope fields and bounds
// results, referrals and continuation text.
func decodeLiveResponse(body []byte) (liveWireResponse, error) {
	// Raw optional members distinguish omission from null, which Go's string
	// and slice decoders otherwise silently treat as an empty value.
	var wire struct {
		Results   []json.RawMessage `json:"results"`
		Referrals json.RawMessage   `json:"referrals"`
		PageToken json.RawMessage   `json:"pageToken"`
	}
	if err := jsondoc.Decode(body, &wire); err != nil || wire.Results == nil {
		return liveWireResponse{}, errors.New("invalid ARD Search response")
	}
	response := liveWireResponse{Results: wire.Results}
	if len(wire.Referrals) > 0 {
		// RegistryReferral is extensible under pinned ARD OpenAPI (D.3).
		// Only its typed core can authorize an already approved referral below.
		if err := jsonv2.Unmarshal(wire.Referrals, &response.Referrals, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false)); err != nil || response.Referrals == nil {
			return liveWireResponse{}, errors.New("invalid ARD Search referrals")
		}
	}
	if len(wire.PageToken) > 0 {
		if string(wire.PageToken) == "null" || json.Unmarshal(wire.PageToken, &response.PageToken) != nil {
			return liveWireResponse{}, errors.New("invalid ARD Search page token")
		}
	}
	if len(response.Results) > MaxLiveSearchPageSize || len(response.Referrals) > 64 {
		return liveWireResponse{}, errors.New("ARD Search response exceeds the bounded result or referral limit")
	}
	if len(response.PageToken) > catalog.MaxCatalogCursorBytes || strings.IndexFunc(response.PageToken, unicode.IsControl) >= 0 {
		return liveWireResponse{}, errors.New("ARD Search response contains an invalid page token")
	}
	return response, nil
}

// observedReferralIdentifier returns a valid AIR identity only from a bounded
// matching Registry referral.
func observedReferralIdentifier(referrals []liveReferral, endpoint string) (string, bool) {
	for _, referral := range referrals {
		identifier := strings.TrimSpace(referral.Identifier)
		displayName := strings.TrimSpace(referral.DisplayName)
		if _, err := catalog.ParseAIRIdentifier(identifier); err == nil && len(identifier) <= 512 && displayName != "" && len(displayName) <= 160 && strings.IndexFunc(identifier, unicode.IsControl) < 0 && strings.IndexFunc(displayName, unicode.IsControl) < 0 && (referral.Type == "application/ai-registry+json" || referral.Type == "application/ai-registry") && catalog.ValidExternalHTTPSURL(referral.URL) && sameEndpoint(referral.URL, endpoint) {
			return identifier, true
		}
	}
	return "", false
}

// sameEndpoint compares normalized origins, escaped paths and exact queries
// while rejecting credentials and fragments.
func sameEndpoint(left, right string) bool {
	leftKey, leftOK := canonicalEndpoint(left)
	rightKey, rightOK := canonicalEndpoint(right)
	return leftOK && rightOK && leftKey == rightKey
}

// canonicalEndpoint returns the origin/path/query identity used to match
// approved source endpoints.
func canonicalEndpoint(value string) (string, bool) {
	leftURL, leftErr := url.Parse(value)
	if leftErr != nil || leftURL.User != nil || leftURL.Fragment != "" {
		return "", false
	}
	leftOrigin, leftErr := fetcher.NormalizeOrigin(value, true)
	if leftErr != nil {
		return "", false
	}
	leftPath := leftURL.EscapedPath()
	if leftPath == "" {
		leftPath = "/"
	}
	key := leftOrigin + leftPath
	if leftURL.RawQuery != "" {
		key += "?" + leftURL.RawQuery
	}
	return key, true
}

// validateLiveSearchQuery enforces supported context and filter shapes before
// any administrator-approved query disclosure.
func validateLiveSearchQuery(queryContext any, filter map[string]any) error {
	if queryContext != nil {
		raw, err := json.Marshal(queryContext)
		if err != nil {
			return catalog.E(catalog.CodeInvalidArgument, "live search @context is not valid JSON")
		}
		var normalized any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err := decoder.Decode(&normalized); err != nil {
			return catalog.E(catalog.CodeInvalidArgument, "live search @context is not valid JSON")
		}
		switch value := normalized.(type) {
		case string:
			parsed, parseErr := url.Parse(value)
			if parseErr != nil || parsed.Scheme == "" || strings.ContainsAny(value, "\r\n\x00") {
				return catalog.E(catalog.CodeInvalidArgument, "live search @context string must be an absolute URI")
			}
		case map[string]any, []any:
		default:
			return catalog.E(catalog.CodeInvalidArgument, "live search @context must be a URI, object, or array")
		}
	}
	if len(filter) > 20 {
		return catalog.E(catalog.CodeInvalidArgument, "live search filter cannot contain more than 20 fields")
	}
	totalValues := 0
	for field, raw := range filter {
		if strings.TrimSpace(field) == "" || utf8.RuneCountInString(field) > 256 {
			return catalog.E(catalog.CodeInvalidArgument, "live search filter fields must be non-empty and at most 256 characters")
		}
		values := make([]string, 0)
		switch value := raw.(type) {
		case string:
			values = append(values, value)
		case []string:
			values = append(values, value...)
		case []any:
			for _, item := range value {
				text, ok := item.(string)
				if !ok {
					return catalog.E(catalog.CodeInvalidArgument, "live search filter values must be strings")
				}
				values = append(values, text)
			}
		default:
			return catalog.E(catalog.CodeInvalidArgument, "live search filter values must be a string or string array")
		}
		if len(values) == 0 || len(values) > 100 {
			return catalog.E(catalog.CodeInvalidArgument, "live search filter values must be a non-empty string or string array")
		}
		totalValues += len(values)
		if totalValues > 200 {
			return catalog.E(catalog.CodeInvalidArgument, "live search filter contains too many values")
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 512 {
				return catalog.E(catalog.CodeInvalidArgument, "live search filter values must be non-empty and at most 512 characters")
			}
		}
	}
	return nil
}

// validARDResultData requires an object without interpreting its native members.
// Raw values preserve numbers beyond float64's range and precision.
func validARDResultData(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var object map[string]json.RawMessage
	return jsonv2.Unmarshal(raw, &object) == nil && object != nil
}

type parsedDocument struct {
	sourceType SourceType
	profileID  string
	entries    []sourceEntry
}

type sourceEntry struct {
	Context                  any               `json:"@context,omitempty"`
	Identifier               string            `json:"identifier"`
	DisplayName              string            `json:"displayName,omitempty"`
	Type                     string            `json:"type"`
	URL                      string            `json:"url,omitempty"`
	Data                     json.RawMessage   `json:"data,omitempty"`
	RepresentativeQueries    []string          `json:"representativeQueries,omitempty"`
	Description              string            `json:"description,omitempty"`
	Tags                     []string          `json:"tags,omitempty"`
	Version                  string            `json:"version,omitempty"`
	Metadata                 json.RawMessage   `json:"metadata,omitempty"`
	VersionPresent           bool              `json:"-"`
	UnknownProperties        map[string]any    `json:"-"`
	Raw                      json.RawMessage   `json:"-"`
	ProfileID                string            `json:"-"`
	StructuralFindings       []catalog.Finding `json:"-"`
	RetrievalDigest          string            `json:"-"`
	EntryLocation            string            `json:"-"`
	NormalizationInputDigest string            `json:"-"`
}

// parseDocumentWithAccess carries the selected source access policy into
// remote-context normalization.
func (m *Manager) parseDocumentWithAccess(ctx context.Context, body []byte, mediaType string, access sourceAccess) (parsedDocument, error) {
	contextFetcher := func(uri string) ([]byte, string, error) {
		return m.fetchContext(ctx, access, uri)
	}
	return parseDocumentWithContextInputs(body, mediaType, contextFetcher)
}

// parseDocumentWithContextInputs selects the evidenced discovery profile,
// preserves extensions and binds context-dependent normalization inputs.
func parseDocumentWithContextInputs(body []byte, mediaType string, contextFetcher func(string) ([]byte, string, error)) (parsedDocument, error) {
	if err := jsondoc.Validate(body); err != nil {
		return parsedDocument{}, catalog.E(catalog.CodeInvalidArgument, "source is not an unambiguous ARD or AI Catalog JSON document")
	}
	var envelope struct {
		Entries []json.RawMessage `json:"entries"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		// Top-level standards documents are open. Retry without rejecting extensions.
		if err := json.Unmarshal(body, &envelope); err != nil {
			return parsedDocument{}, catalog.E(catalog.CodeInvalidArgument, "source is not a valid ARD or AI Catalog JSON document")
		}
	}
	if envelope.Entries == nil {
		return parsedDocument{}, catalog.E(catalog.CodeInvalidArgument, "source document has no entries array")
	}
	document := parsedDocument{sourceType: SourceARDManifest, profileID: ardProfileID, entries: make([]sourceEntry, 0, len(envelope.Entries))}
	mediaType = canonicalMediaType(mediaType)
	if mediaType == aiCatalogDocumentMediaType {
		if err := standards.ValidateAICatalog(body); err != nil {
			if strings.Contains(err.Error(), "major version") {
				return parsedDocument{}, catalog.E(catalog.CodeUnsupportedProfile, err.Error())
			}
			return parsedDocument{}, catalog.E(catalog.CodeInvalidArgument, err.Error())
		}
		document.sourceType = SourceAICatalog
		document.profileID = "ai-catalog-1.0-b062278"
	}
	for _, raw := range envelope.Entries {
		parseRaw := raw
		structuralFindings := make([]catalog.Finding, 0)
		contextInputs := make(map[string]string)
		if document.sourceType == SourceARDManifest {
			var entryContextFetcher standards.ContextFetcher
			if contextFetcher != nil {
				entryContextFetcher = func(uri string) ([]byte, error) {
					body, digest, err := contextFetcher(uri)
					if err == nil && digest != "" {
						contextInputs[uri] = digest
					}
					return body, err
				}
			}
			normalized, err := standards.NormalizeARDEntry(raw, entryContextFetcher)
			if err != nil {
				structuralFindings = append(structuralFindings, catalog.Finding{Code: "ARD_JSONLD_EXPANSION_FAILED", Severity: catalog.SeverityError, Message: "The ARD entry JSON-LD context could not be expanded safely."})
			} else {
				parseRaw = normalized
				if err := standards.ValidateARDEntry(normalized); err != nil {
					structuralFindings = append(structuralFindings, catalog.Finding{Code: "ARD_ENTRY_SCHEMA_INVALID", Severity: catalog.SeverityError, Message: err.Error()})
				}
				var discoverySignals struct {
					Identifier            string   `json:"identifier"`
					RepresentativeQueries []string `json:"representativeQueries"`
				}
				if json.Unmarshal(normalized, &discoverySignals) == nil {
					if len(discoverySignals.RepresentativeQueries) < 2 || len(discoverySignals.RepresentativeQueries) > 5 {
						structuralFindings = append(structuralFindings, catalog.Finding{Code: "ARD_REPRESENTATIVE_QUERIES_RECOMMENDED", Severity: catalog.SeverityWarning, Path: "/representativeQueries", Message: "ARD recommends two to five representative queries so the resource can be found semantically."})
					}
					if identifier, identifierErr := catalog.ParseAIRIdentifier(discoverySignals.Identifier); identifierErr == nil && len(identifier.Namespace) == 0 {
						structuralFindings = append(structuralFindings, catalog.Finding{Code: "ARD_IDENTIFIER_NAMESPACE_RECOMMENDED", Severity: catalog.SeverityWarning, Path: "/identifier", Message: "The identifier uses the schema-compatible terminal-only form; service-generated identifiers include an explicit namespace."})
					}
				}
			}
		}
		var entry sourceEntry
		if err := json.Unmarshal(parseRaw, &entry); err != nil {
			return parsedDocument{}, catalog.E(catalog.CodeInvalidArgument, "source contains an invalid entry")
		}
		var properties map[string]json.RawMessage
		if err := json.Unmarshal(parseRaw, &properties); err != nil {
			return parsedDocument{}, catalog.E(catalog.CodeInvalidArgument, "source contains an invalid entry")
		}
		_, entry.VersionPresent = properties["version"]
		for _, name := range []string{"@context", "identifier", "displayName", "type", "url", "data", "representativeQueries", "description", "tags", "version", "metadata"} {
			delete(properties, name)
		}
		entry.UnknownProperties = normalizedUnknownProperties(properties)
		entry.Raw = append(json.RawMessage(nil), raw...)
		entry.ProfileID = document.profileID
		entry.StructuralFindings = structuralFindings
		entry.NormalizationInputDigest = normalizationInputDigest(contextInputs)
		document.entries = append(document.entries, entry)
	}
	return document, nil
}

// enumerate walks an approved manifest graph within the configured fetch,
// depth, and entry budgets. Repeated URLs and document digests cannot expand
// the traversal indefinitely.
func (m *Manager) enumerate(ctx context.Context, source Source) ([]sourceEntry, string, string, bool, []catalog.Finding, error) {
	if source.Type == SourceRegistry {
		return m.enumerateRegistryList(ctx, source)
	}
	visitedURLs := make(map[string]struct{})
	visitedDigests := make(map[string]struct{})
	entries := make([]sourceEntry, 0)
	findings := make([]catalog.Finding, 0)
	rootDigest := ""
	fetches := 0
	complete := true
	access := accessForSource(source, true)
	addFinding := func(finding catalog.Finding) { findings = appendBoundedFinding(findings, finding) }
	maxDepth := m.maxDepth
	if source.Nesting.Enabled && source.Nesting.MaxDepth > 0 {
		maxDepth = min(maxDepth, source.Nesting.MaxDepth)
	}
	var processDocument func(string, string, parsedDocument, int)
	walk := func(endpoint string, depth int) error {
		if depth > maxDepth {
			return catalog.E(catalog.CodeInvalidArgument, "nested Catalog depth exceeds the configured maximum")
		}
		if fetches >= m.maxFetches {
			return catalog.E(catalog.CodeInvalidArgument, "nested Catalog fetch budget was exhausted")
		}
		if _, seen := visitedURLs[endpoint]; seen {
			addFinding(catalog.Finding{Code: "NESTED_CATALOG_CYCLE", Severity: catalog.SeverityWarning, Message: "A repeated nested Catalog URL was skipped."})
			return nil
		}
		visitedURLs[endpoint] = struct{}{}
		fetches++
		var result fetcher.Result
		var document parsedDocument
		if depth == 0 {
			discovered, discoverErr := m.discover(ctx, endpoint, access, source.ProfileID)
			if discoverErr != nil {
				return discoverErr
			}
			if source.ProfileID != "" && sourceInterfaceChanged(source, discovered.result, summarizeDiscovery(discovered)) {
				return catalog.E(catalog.CodePreconditionFailed, "source interface changed; inspect and review it again before synchronizing")
			}
			result = discovered.result
			document = discovered.document
		} else {
			fetched, acceptFetched, fetchErr := m.fetchForParsing(ctx, access, endpoint, source.ProfileID, fetcher.Request{Accept: []string{ardDocumentMediaType, aiCatalogDocumentMediaType, "application/json"}}, true, false)
			if fetchErr != nil {
				return fetchErr
			}
			if fetched.Status != http.StatusOK {
				return syncHTTPError(fetched)
			}
			mediaType, classificationErr := effectiveDiscoveryMediaType(fetched.MediaType, aiCatalogDocumentMediaType, "")
			if classificationErr != nil {
				acceptFetched(false)
				return classificationErr
			}
			parsed, parseErr := m.parseDocumentWithAccess(ctx, fetched.Body, mediaType, access)
			if parseErr != nil {
				acceptFetched(false)
				return parseErr
			}
			acceptFetched(true)
			result = fetched
			document = parsed
		}
		finalURL := result.FinalURL
		if finalURL == "" {
			finalURL = endpoint
		}
		if finalURL != endpoint {
			if _, seen := visitedURLs[finalURL]; seen {
				addFinding(catalog.Finding{Code: "NESTED_CATALOG_CYCLE", Severity: catalog.SeverityWarning, Message: "A nested Catalog redirect reached an already visited URL and was skipped."})
				return nil
			}
			visitedURLs[finalURL] = struct{}{}
		}
		digest := result.BodyDigest
		if digest == "" {
			sum := sha256.Sum256(result.Body)
			digest = "sha256:" + hex.EncodeToString(sum[:])
		}
		if depth == 0 {
			rootDigest = digest
		}
		if _, seen := visitedDigests[digest]; seen {
			addFinding(catalog.Finding{Code: "NESTED_CATALOG_CYCLE", Severity: catalog.SeverityWarning, Message: "A repeated nested Catalog document was skipped."})
			return nil
		}
		visitedDigests[digest] = struct{}{}
		processDocument(finalURL, digest, document, depth)
		return nil
	}
	processDocument = func(baseURL, retrievalDigest string, document parsedDocument, depth int) {
		for index, entry := range document.entries {
			if len(entries) >= m.maxEntries {
				complete = false
				addFinding(catalog.Finding{Code: "SOURCE_ENTRY_BUDGET_EXHAUSTED", Severity: catalog.SeverityWarning, Message: "The source entry budget was exhausted before enumeration completed."})
				return
			}
			entry.RetrievalDigest = retrievalDigest
			entry.EntryLocation = fmt.Sprintf("/entries/%d", index)
			entries = append(entries, entry)
			if entry.Type != aiCatalogDocumentMediaType {
				continue
			}
			if !source.Nesting.Enabled {
				addFinding(catalog.Finding{Code: "NESTED_CATALOG_NOT_ENABLED", Severity: catalog.SeverityInfo, Message: "A nested Catalog entry was retained but not traversed because nesting is not enabled for this source."})
				continue
			}
			if depth+1 > maxDepth {
				complete = false
				addFinding(catalog.Finding{Code: "NESTED_CATALOG_DEPTH_EXCEEDED", Severity: catalog.SeverityWarning, Message: "A nested Catalog branch exceeded the configured depth and was not traversed."})
				continue
			}
			hasURL := strings.TrimSpace(entry.URL) != ""
			hasData := len(entry.Data) > 0
			if hasURL == hasData {
				complete = false
				addFinding(catalog.Finding{Code: "NESTED_CATALOG_CONTENT_INVALID", Severity: catalog.SeverityWarning, Message: "A nested Catalog entry must contain exactly one URL or inline document."})
				continue
			}
			if hasData {
				digestBytes := sha256.Sum256(entry.Data)
				digest := "sha256:" + hex.EncodeToString(digestBytes[:])
				if _, seen := visitedDigests[digest]; seen {
					addFinding(catalog.Finding{Code: "NESTED_CATALOG_CYCLE", Severity: catalog.SeverityWarning, Message: "A repeated inline nested Catalog document was skipped."})
					continue
				}
				nestedDocument, parseErr := m.parseDocumentWithAccess(ctx, entry.Data, aiCatalogDocumentMediaType, access)
				if parseErr != nil {
					complete = false
					addFinding(catalog.Finding{Code: "NESTED_CATALOG_CONTENT_INVALID", Severity: catalog.SeverityWarning, Message: "An inline nested Catalog document could not be parsed."})
					continue
				}
				visitedDigests[digest] = struct{}{}
				processDocument(baseURL, digest, nestedDocument, depth+1)
				continue
			}
			nestedURL, resolveErr := resolveNestedURL(baseURL, entry.URL)
			if resolveErr != nil {
				complete = false
				addFinding(catalog.Finding{Code: "NESTED_CATALOG_URL_INVALID", Severity: catalog.SeverityWarning, Message: "A nested Catalog reference could not be resolved safely."})
				continue
			}
			if !nestedOriginAllowed(baseURL, nestedURL, source.Nesting.AllowedOrigins) {
				addFinding(catalog.Finding{Code: "NESTED_CATALOG_ORIGIN_DENIED", Severity: catalog.SeverityWarning, Message: "A cross-origin nested Catalog reference was not traversed because its origin is not allowlisted."})
				continue
			}
			if err := walk(nestedURL, depth+1); err != nil {
				complete = false
				addFinding(catalog.Finding{Code: "NESTED_CATALOG_INCOMPLETE", Severity: catalog.SeverityWarning, Message: "A permitted nested Catalog branch did not complete within the source bounds."})
			}
		}
	}
	credentialRevision := ""
	err := walk(source.Endpoint, 0)
	if evidenceErr := access.operationError(); evidenceErr != nil {
		err = errors.Join(err, evidenceErr)
	}
	if access.credentialRevision != nil {
		credentialRevision = *access.credentialRevision
	}
	if err != nil {
		return nil, rootDigest, credentialRevision, false, findings, err
	}
	return entries, rootDigest, credentialRevision, complete, findings, nil
}

// resolveNestedURL resolves a nested discovery reference against its parent and
// rejects invalid external-reference syntax.
func resolveNestedURL(baseURL, reference string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", errors.New("nested Catalog base URL is invalid")
	}
	ref, err := url.Parse(strings.TrimSpace(reference))
	if err != nil {
		return "", errors.New("nested Catalog URL is invalid")
	}
	resolved := base.ResolveReference(ref)
	if !catalog.ValidExternalHTTPSURL(resolved.String()) {
		return "", errors.New("nested Catalog URL is not a safe HTTPS reference")
	}
	return resolved.String(), nil
}

// nestedOriginAllowed permits same-origin nesting or an explicitly approved
// additional origin.
func nestedOriginAllowed(parentURL, nestedURL string, allowedOrigins []string) bool {
	parentOrigin, parentErr := fetcher.NormalizeOrigin(parentURL, false)
	nestedOrigin, nestedErr := fetcher.NormalizeOrigin(nestedURL, false)
	if parentErr != nil || nestedErr != nil {
		return false
	}
	if parentOrigin == nestedOrigin {
		return true
	}
	for _, allowed := range allowedOrigins {
		allowedOrigin, err := fetcher.NormalizeOrigin(allowed, false)
		if err == nil && allowedOrigin == nestedOrigin {
			return true
		}
	}
	return false
}

// assessSourceEntryStructure derives structural state and profile findings
// without equating validity with trust.
func assessSourceEntryStructure(entry sourceEntry, fallbackProfileID string) (string, string, []catalog.Finding) {
	findings := append([]catalog.Finding(nil), entry.StructuralFindings...)
	structural := "valid"
	for _, finding := range entry.StructuralFindings {
		if finding.Severity == catalog.SeverityError {
			structural = "invalid"
			break
		}
	}
	profileID := entry.ProfileID
	if profileID == "" {
		profileID = fallbackProfileID
	}
	requiresDisplayName := profileID != "ai-catalog-1.0-b062278"
	invalidDelivery := (entry.URL == "") == (len(entry.Data) == 0)
	if profileID == ardProfileID && len(entry.Data) > 0 {
		var object map[string]json.RawMessage
		if json.Unmarshal(entry.Data, &object) != nil || object == nil {
			invalidDelivery = true
		}
	}
	if entry.Identifier == "" || (requiresDisplayName && entry.DisplayName == "") || entry.Type == "" || invalidDelivery {
		structural = "invalid"
		message := "AI Catalog entries require identifier, type, and exactly one of URL or data."
		if requiresDisplayName {
			message = "ARD entries require identifier, displayName, type, and exactly one of URL or object data."
		}
		findings = append(findings, catalog.Finding{Code: "DISCOVERY_ENTRY_REQUIRED_FIELDS", Severity: catalog.SeverityError, Message: message})
	}
	if entry.VersionPresent && (entry.Version == "" || !validDiscoveryVersion(entry.Version)) {
		structural = "invalid"
		findings = append(findings, catalog.Finding{Code: "DISCOVERY_ENTRY_VERSION_INVALID", Severity: catalog.SeverityError, Path: "/version", Message: "A present version must be valid text without NUL and contain one to 128 Unicode characters."})
	}
	return profileID, structural, findings
}

// normalizedSourceEntryDigest fingerprints the normalized entry under its
// selected or fallback profile.
func normalizedSourceEntryDigest(entry sourceEntry, fallbackProfileID string) string {
	_, structural, _ := assessSourceEntryStructure(entry, fallbackProfileID)
	identifier, err := catalog.ParseAIRIdentifier(entry.Identifier)
	kind := kindForMediaType(entry.Type)
	if structural != "valid" || err != nil || !kind.Valid() {
		return ""
	}
	_, digest := normalizeObservationEntry(entry, identifier.String(), kind)
	return digest
}

// observation creates a source-owned external record with immutable occurrence
// and normalized search evidence.
func (m *Manager) observation(tenantID string, source Source, snapshotID string, entry sourceEntry, index int, now time.Time) (Observation, error) {
	profileID, structural, findings := assessSourceEntryStructure(entry, source.ProfileID)
	normalizedIdentifier := ""
	searchPublisher := ""
	if identifier, identifierErr := catalog.ParseAIRIdentifier(entry.Identifier); identifierErr != nil {
		findings = append(findings, catalog.Finding{Code: "ARD_IDENTIFIER_COMPATIBILITY", Severity: catalog.SeverityWarning, Message: "The identifier is not a normalized urn:air and remains source-scoped evidence."})
	} else {
		normalizedIdentifier = identifier.String()
		searchPublisher = identifier.Publisher
	}
	kind := kindForMediaType(entry.Type)
	support := "supported"
	if kind == "" {
		support = "unknown"
	}
	raw := entry.Raw
	if len(raw) == 0 {
		var err error
		raw, err = json.Marshal(entry)
		if err != nil {
			return Observation{}, err
		}
	}
	sum := sha256.Sum256(raw)
	exactDigest := "sha256:" + hex.EncodeToString(sum[:])
	identityDigest := observationIdentityDigest(exactDigest, entry.NormalizationInputDigest)
	normalizedDigest := ""
	var normalizedEntry []byte
	if structural == "valid" && normalizedIdentifier != "" && kind.Valid() {
		normalizedEntry, normalizedDigest = normalizeObservationEntry(entry, normalizedIdentifier, kind)
	}
	sourceKey := sourceScopedObservationKey(normalizedIdentifier, entry.Version, entry.VersionPresent, entry.RetrievalDigest, entry.EntryLocation, exactDigest)
	idValue, err := m.ids.New(now.Add(time.Duration(index+1) * time.Nanosecond))
	if err != nil {
		return Observation{}, err
	}
	observation := Observation{ID: idValue, TenantID: tenantID, SourceID: source.ID, SnapshotID: snapshotID, SourceKey: sourceKey, Identifier: normalizedIdentifier, Version: entry.Version, VersionPresent: entry.VersionPresent, Kind: kind, MediaType: entry.Type, Completeness: CompleteEntry, Compatibility: Compatibility{Structural: structural, ProfileID: profileID, Support: support, Findings: findings}, RawEntry: raw, NormalizedEntry: normalizedEntry, RawEntryAvailable: true, ExactEntryDigest: exactDigest, IdentityDigest: identityDigest, NormalizedEntryDigest: normalizedDigest, Freshness: FreshCurrent, FirstSeenAt: now, LastSeenAt: now}
	observation.ArtworkMetadata = ExtractArtworkCandidates(entry.UnknownProperties, entry.Data)
	observation.RatingsMetadata = ExtractRatingSummary(entry.UnknownProperties, source, normalizedIdentifier, entry.VersionPresent, entry.Version)
	if normalizedDigest != "" {
		observation.NormalizationProfile = observationNormalizationProfile
		var projection normalizedObservationEntry
		if err := json.Unmarshal(normalizedEntry, &projection); err != nil {
			return Observation{}, catalog.Wrap(catalog.CodeInternal, "decode normalized observation search projection", err)
		}
		observation.SearchDisplayName = projection.DisplayName
		observation.SearchDescription = projection.Description
		observation.SearchTags = normalizedSearchTags(projection.Tags)
		observation.SearchQueries = projection.RepresentativeQueries
		observation.SearchPublisher = searchPublisher
	}
	observation.Occurrence = ObservationOccurrence{ObservationID: observation.ID, SourceID: source.ID, SnapshotID: snapshotID, SourceRevision: source.Revision, RetrievalDigest: entry.RetrievalDigest, EntryLocation: entry.EntryLocation, ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, NormalizationInputDigest: entry.NormalizationInputDigest, EvidenceComplete: validPolicyDigest(entry.RetrievalDigest) && validEntryLocation(entry.EntryLocation), ObservedAt: now}
	return observation, nil
}

// kindForMediaType maps supported discovery representation types to Catalog
// kinds and leaves unknown types ungoverned.
func kindForMediaType(mediaType string) catalog.Kind {
	if adapter.IsSkillMarkdownMediaType(mediaType) {
		return catalog.KindSkill
	}
	switch strings.ToLower(mediaType) {
	case adapter.MediaA2AAgentCard, adapter.MediaPrivateAgent:
		return catalog.KindAgent
	case adapter.MediaMCPServerCard, adapter.MediaMCPRegistry, adapter.MediaPrivateMCP:
		return catalog.KindMCPServer
	case adapter.MediaSkillJSON, adapter.MediaSkillZIP, adapter.MediaSkillGZIP:
		return catalog.KindSkill
	case catalog.PluginMediaZIP, catalog.PluginMediaGZIP:
		return catalog.KindPlugin
	default:
		return ""
	}
}

// normalizeScope canonicalizes administrator-selected kind and metadata
// restrictions.
func normalizeScope(scope Scope) Scope {
	scope.ExcludedIdentifiers = normalizeExcludedIdentifiers(scope.ExcludedIdentifiers)
	scope.Identifiers = normalizeStrings(scope.Identifiers)
	scope.Versions = normalizeStrings(scope.Versions)
	scope.Publishers = normalizeStrings(scope.Publishers)
	scope.Tags = normalizeStrings(scope.Tags)
	var kinds []catalog.Kind
	if scope.Kinds != nil {
		kinds = make([]catalog.Kind, 0, len(scope.Kinds))
	}
	for _, kind := range scope.Kinds {
		if kind.Valid() && !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	scope.Kinds = kinds
	return scope
}

// validateScope checks supported kinds and bounded unique metadata restrictions
// before source activation.
func validateScope(scope Scope) error {
	if err := validateExcludedIdentifiers(scope.ExcludedIdentifiers); err != nil {
		return err
	}
	if len(scope.Identifiers) > 1000 || len(scope.Versions) > 100 || len(scope.Kinds) > 4 || len(scope.Publishers) > 100 || len(scope.Tags) > 100 {
		return catalog.E(catalog.CodeInvalidArgument, "source scope exceeds its supported limits")
	}
	if len(scope.Identifiers)+len(scope.Versions)+len(scope.Kinds)+len(scope.Publishers)+len(scope.Tags) == 0 {
		return catalog.E(catalog.CodeInvalidArgument, "source scope must select at least one identifier, version, resource kind, publisher, or tag")
	}
	if scope.Identifiers != nil && len(scope.Identifiers) == 0 || scope.Versions != nil && len(scope.Versions) == 0 || scope.Kinds != nil && len(scope.Kinds) == 0 || scope.Publishers != nil && len(scope.Publishers) == 0 || scope.Tags != nil && len(scope.Tags) == 0 {
		return catalog.E(catalog.CodeInvalidArgument, "source scope arrays cannot be empty")
	}
	seenKinds := make(map[catalog.Kind]struct{}, len(scope.Kinds))
	for _, kind := range scope.Kinds {
		if !kind.Valid() {
			return catalog.E(catalog.CodeInvalidArgument, "source scope contains an invalid resource kind")
		}
		if _, duplicate := seenKinds[kind]; duplicate {
			return catalog.E(catalog.CodeInvalidArgument, "source scope contains a duplicate resource kind")
		}
		seenKinds[kind] = struct{}{}
	}
	if err := validateScopeStrings("identifiers", scope.Identifiers, 512); err != nil {
		return err
	}
	if err := validateScopeStrings("versions", scope.Versions, 128); err != nil {
		return err
	}
	if err := validateScopeStrings("publishers", scope.Publishers, 255); err != nil {
		return err
	}
	return validateScopeStrings("tags", scope.Tags, 80)
}

// validateScopeStrings requires nonempty, already trimmed and unique values
// within the field's Unicode character bound, as declared in Control OpenAPI.
func validateScopeStrings(name string, values []string, maximumLength int) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return catalog.E(catalog.CodeInvalidArgument, "source scope "+name+" must be non-empty and cannot have surrounding whitespace")
		}
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return catalog.E(catalog.CodeInvalidArgument, "source scope "+name+" must be valid text without NUL")
		}
		if utf8.RuneCountInString(value) > maximumLength {
			return catalog.E(catalog.CodeInvalidArgument, "source scope "+name+" exceed their supported length")
		}
		if _, duplicate := seen[value]; duplicate {
			return catalog.E(catalog.CodeInvalidArgument, "source scope "+name+" contain a duplicate value")
		}
		seen[value] = struct{}{}
	}
	return nil
}

// normalizeStrings trims, removes empty/duplicate values and sorts source
// policy strings while preserving nil.
func normalizeStrings(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, 0)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

// scopeMatches requires every configured source restriction to match the
// candidate entry.
func scopeMatches(scope Scope, entry sourceEntry) bool {
	if scopeExcludesIdentifier(scope, entry.Identifier) {
		return false
	}
	if len(scope.Identifiers) > 0 && !slices.Contains(scope.Identifiers, entry.Identifier) {
		return false
	}
	if len(scope.Versions) > 0 && !slices.Contains(scope.Versions, entry.Version) {
		return false
	}
	if len(scope.Kinds) > 0 && !slices.Contains(scope.Kinds, kindForMediaType(entry.Type)) {
		return false
	}
	if len(scope.Publishers) > 0 {
		identifier, err := catalog.ParseAIRIdentifier(entry.Identifier)
		if err != nil || !slices.Contains(scope.Publishers, identifier.Publisher) {
			return false
		}
	}
	return len(scope.Tags) == 0 || hasAll(entry.Tags, scope.Tags)
}

// hasAll checks whether every requested exact value appears in the entry
// metadata.
func hasAll(values, wanted []string) bool {
	for _, item := range wanted {
		if !slices.Contains(values, item) {
			return false
		}
	}
	return true
}

// sortedKeys returns deterministic lexical ordering for set-valued source
// evidence.
func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func permissionActor(ctx context.Context, action identity.Permission) (identity.Actor, error) {
	a, err := identity.RequirePermission(ctx, action, "")
	if err != nil {
		return identity.Actor{}, catalog.PermissionError("current federation permission is required", err)
	}
	return a, nil
}
