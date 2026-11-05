// Federation evidence binds source administration and synchronization outcomes
// to attributed actions and bounded observation-change facts.

package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

const (
	outboundOutcomeSucceeded      = "succeeded"
	outboundOutcomePolicyDenied   = "policy_denied"
	outboundOutcomeTransportError = "transport_error"
	outboundOutcomeHTTPError      = "http_error"
	outboundOutcomeParseError     = "parse_error"
	outboundOutcomeNegative       = "negative_result"
	outboundOutcomePartial        = "partial"
	outboundOutcomeInvalidInput   = "invalid_input"
)

type classifiedOutboundError struct {
	outcome  string
	evidence outboundEvidence
	err      error
}

// outboundEvidence carries only the bounded response metadata needed to
// explain a source test failure. It deliberately excludes URLs, headers, and
// response bytes from the error chain and therefore from audit construction.
type outboundEvidence struct {
	HTTPStatus         int
	BodyDigest         string
	PolicyDigest       string
	CredentialRevision string
}

// Error preserves the underlying diagnostic text while carrying a separate
// bounded outbound classification.
func (e *classifiedOutboundError) Error() string { return e.err.Error() }

// Unwrap exposes the underlying outbound error for ordinary error inspection.
func (e *classifiedOutboundError) Unwrap() error { return e.err }

// withOutboundOutcome attaches a fixed disclosure-safe classification to a
// nonnil diagnostic error.
func withOutboundOutcome(err error, outcome string) error {
	if err == nil {
		return nil
	}
	return &classifiedOutboundError{outcome: outcome, err: err}
}

// withOutboundResult retains bounded fetch evidence alongside the
// classification of a nonnil error.
func withOutboundResult(err error, outcome string, result fetcher.Result) error {
	if err == nil {
		return nil
	}
	return &classifiedOutboundError{outcome: outcome, evidence: boundedOutboundEvidence(result), err: err}
}

// boundedOutboundEvidence selects only valid HTTP status, digests and bounded
// credential revision metadata from a fetch result.
func boundedOutboundEvidence(result fetcher.Result) outboundEvidence {
	evidence := outboundEvidence{}
	if result.Status >= 100 && result.Status <= 599 {
		evidence.HTTPStatus = result.Status
	}
	if validPolicyDigest(result.BodyDigest) {
		evidence.BodyDigest = result.BodyDigest
	}
	if validPolicyDigest(result.PolicyDigest) {
		evidence.PolicyDigest = result.PolicyDigest
	}
	if len(result.CredentialRevision) <= 512 {
		evidence.CredentialRevision = result.CredentialRevision
	}
	return evidence
}

// outboundEvidenceFromError retrieves the retained bounded projection without
// inspecting unrestricted response data.
func outboundEvidenceFromError(err error) outboundEvidence {
	var classified *classifiedOutboundError
	if errors.As(err, &classified) {
		return classified.evidence
	}
	return outboundEvidence{}
}

// classifyOutboundOutcome preserves explicit classifications and otherwise
// distinguishes invalid target, policy denial and transport failure.
func classifyOutboundOutcome(err error) string {
	var classified *classifiedOutboundError
	if errors.As(err, &classified) {
		return classified.outcome
	}
	switch {
	case errors.Is(err, fetcher.ErrInvalidTarget):
		return outboundOutcomeInvalidInput
	case errors.Is(err, fetcher.ErrPolicyDenied), errors.Is(err, fetcher.ErrPolicyChanged), errors.Is(err, fetcher.ErrCredentialChanged), catalog.ErrorCodeOf(err) == catalog.CodeSourcePolicyDenied:
		return outboundOutcomePolicyDenied
	default:
		return outboundOutcomeTransportError
	}
}

type sourceInspectionFacts struct {
	Outcome             string `json:"outcome"`
	OriginClass         string `json:"originClass"`
	PolicyDigest        string `json:"policyDigest"`
	ConfigurationDigest string `json:"configurationDigest"`
	InspectionDigest    string `json:"inspectionDigest,omitempty"`
	ExpiresAt           string `json:"expiresAt,omitempty"`
}

type sourcePreviewFacts struct {
	SourceID             string       `json:"sourceId"`
	SourceRevision       int64        `json:"sourceRevision"`
	Intent               ChangeIntent `json:"intent"`
	ConfigurationDigest  string       `json:"configurationDigest"`
	ObservationSetDigest string       `json:"observationSetDigest"`
	PreviewDigest        string       `json:"previewDigest"`
	CurrentMatched       int          `json:"currentMatched"`
	ProposedMatched      int          `json:"proposedMatched"`
	ExcludedObservations int          `json:"excludedObservations"`
	SendsQueriesUpstream bool         `json:"sendsQueriesUpstream"`
	ExpiresAt            string       `json:"expiresAt"`
}

type sourceConnectionFacts struct {
	InspectionID        string        `json:"inspectionId"`
	SourceRevision      int64         `json:"sourceRevision"`
	ConfigurationDigest string        `json:"configurationDigest"`
	RetrievalMode       RetrievalMode `json:"retrievalMode"`
	UpdateMode          UpdateMode    `json:"updateMode"`
	PolicyDigest        string        `json:"policyDigest"`
}

type sourceRevisionFacts struct {
	PreviewID                 string        `json:"previewId,omitempty"`
	SourceRevision            int64         `json:"sourceRevision"`
	BeforeConfigurationDigest string        `json:"beforeConfigurationDigest"`
	AfterConfigurationDigest  string        `json:"afterConfigurationDigest"`
	RetrievalMode             RetrievalMode `json:"retrievalMode"`
	UpdateMode                UpdateMode    `json:"updateMode"`
	PolicyDigest              string        `json:"policyDigest"`
	Trigger                   string        `json:"trigger"`
}

type sourceTestFacts struct {
	BeforeRevision     int64    `json:"beforeRevision"`
	AfterRevision      int64    `json:"afterRevision"`
	Outcome            string   `json:"outcome"`
	StateChanged       bool     `json:"stateChanged"`
	HTTPClass          string   `json:"httpClass,omitempty"`
	PolicyDigest       string   `json:"policyDigest"`
	BodyDigest         string   `json:"bodyDigest,omitempty"`
	Capabilities       []string `json:"capabilities,omitempty"`
	CredentialRevision string   `json:"credentialRevision,omitempty"`
}

type sourceLifecycleFacts struct {
	BeforeState          SourceStatus `json:"beforeState"`
	AfterState           SourceStatus `json:"afterState"`
	BeforeRevision       int64        `json:"beforeRevision"`
	AfterRevision        int64        `json:"afterRevision"`
	PreviewID            string       `json:"previewId,omitempty"`
	PreviewDigest        string       `json:"previewDigest,omitempty"`
	ObservationSetDigest string       `json:"observationSetDigest,omitempty"`
}

type sourceSyncFacts struct {
	SourceID       string        `json:"sourceId"`
	SourceRevision int64         `json:"sourceRevision"`
	State          SnapshotState `json:"state"`
	PolicyDigest   string        `json:"policyDigest"`
	MemberCount    int           `json:"memberCount"`
	Added          int           `json:"added"`
	Updated        int           `json:"updated"`
	Withdrawn      int           `json:"withdrawn"`
	FailureClass   string        `json:"failureClass,omitempty"`
	JobID          string        `json:"jobId"`
	JobKind        string        `json:"jobKind"`
	JobAttempt     int           `json:"jobAttempt"`
	Initiator      string        `json:"initiator,omitempty"`
	Trigger        string        `json:"trigger"`
}

// syncEvidence binds source revision, completion counts and worker attribution
// to the retained sync outcome.
func (m *Manager) syncEvidence(ctx context.Context, actor identity.Actor, source Source, snapshot Snapshot, failureClass string) (MutationEvidence, error) {
	if m.evidence == nil {
		return MutationEvidence{}, nil
	}
	if snapshot.CompletedAt == nil || source.ID == "" || source.TenantID != actor.TenantID || snapshot.TenantID != actor.TenantID || snapshot.SourceID != source.ID || snapshot.SourceRevision != source.Revision || snapshot.PolicyDigest != source.PolicyDigest {
		return MutationEvidence{}, catalog.E(catalog.CodeInternal, "source synchronization audit does not match its completed snapshot")
	}
	job, ok := audit.JobExecutionFrom(ctx)
	if !ok || !identity.ValidTenantID(job.ID) || job.Kind != "source.sync" || job.Attempt < 1 || job.Trigger != "manual" && job.Trigger != "automatic" {
		return MutationEvidence{}, catalog.E(catalog.CodeInternal, "source synchronization audit requires bounded Job execution attribution")
	}
	initiator := ""
	if identity.ValidTenantID(job.Initiator) || job.Initiator == audit.InitiatorSourceMaintainer {
		initiator = job.Initiator
	}
	facts := sourceSyncFacts{SourceID: source.ID, SourceRevision: source.Revision, State: snapshot.State, PolicyDigest: snapshot.PolicyDigest, MemberCount: snapshot.MemberCount, Added: snapshot.Added, Updated: snapshot.Updated, Withdrawn: snapshot.Withdrawn, FailureClass: failureClass, JobID: job.ID, JobKind: job.Kind, JobAttempt: job.Attempt, Initiator: initiator, Trigger: job.Trigger}
	action, publicAction := audit.ActionSourceSyncCompleted, "source.sync.completed"
	if snapshot.State != SnapshotComplete {
		action, publicAction = audit.ActionSourceSyncFailed, "source.sync.failed"
	}
	afterDigest, err := snapshotStateDigest(snapshot)
	if err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "digest source synchronization state", err)
	}
	return m.mutationEvidence(ctx, actor, action, "source_snapshot", snapshot.ID, "", facts, "", afterDigest, publicAction, "source_snapshot", *snapshot.CompletedAt)
}

type sensitiveReadFacts struct {
	ContentDigest string `json:"contentDigest"`
	SizeBytes     int    `json:"sizeBytes,omitempty"`
}

type liveSearchFacts struct {
	ContactedSourceIDs []string `json:"contactedSourceIds"`
	Outcome            string   `json:"outcome"`
	ResultCount        int      `json:"resultCount"`
	DiagnosticCount    int      `json:"diagnosticCount"`
	Partial            bool     `json:"partial"`
	PolicyDigests      []string `json:"policyDigests"`
}

// aggregateLiveOutcome combines per-source outcomes and partial-result state
// into one reviewed audit classification.
func aggregateLiveOutcome(outcomes []string, partial bool) string {
	if len(outcomes) == 0 {
		if partial {
			return outboundOutcomePartial
		}
		return outboundOutcomeSucceeded
	}
	if !partial {
		// A valid empty result from one source and accepted results from
		// another still form one complete aggregate response. Prefer success
		// when any source contributed results; otherwise retain the common
		// negative result (or the first defensive classification).
		if slices.Contains(outcomes, outboundOutcomeSucceeded) {
			return outboundOutcomeSucceeded
		}
		return outcomes[0]
	}
	first := outcomes[0]
	for _, outcome := range outcomes[1:] {
		if outcome != first {
			return outboundOutcomePartial
		}
	}
	if first == outboundOutcomeSucceeded {
		return outboundOutcomePartial
	}
	return first
}

// appendLiveSearchAudit retains bounded source/policy/outcome facts without
// recording query text or response bodies.
func (m *Manager) appendLiveSearchAudit(ctx context.Context, actor identity.Actor, request LiveSearchRequest, sources []Source, response LiveSearchResponse, outcome string) error {
	if m.evidence == nil {
		return nil
	}
	at := m.clock.Now()
	operationID, err := m.ids.New(at)
	if err != nil {
		return catalog.Wrap(catalog.CodeInternal, "create live-search diagnostic identifier", err)
	}
	selectedIDs := make([]string, 0, len(sources))
	policyDigests := make([]string, 0, len(sources))
	for _, source := range sources {
		selectedIDs = append(selectedIDs, source.ID)
		if source.PolicyDigest != "" {
			policyDigests = append(policyDigests, source.PolicyDigest)
		}
	}
	sort.Strings(selectedIDs)
	selectedIDs = slices.Compact(selectedIDs)
	sort.Strings(policyDigests)
	policyDigests = slices.Compact(policyDigests)
	record, err := m.auditRecord(ctx, actor, audit.ActionFederationLiveSearchDiagnostic, "federation_live_search", operationID, request.Reason, liveSearchFacts{ContactedSourceIDs: selectedIDs, Outcome: outcome, ResultCount: len(response.Results), DiagnosticCount: len(response.Diagnostics), Partial: response.Partial, PolicyDigests: policyDigests}, at)
	if err != nil {
		return err
	}
	if record.ID == "" {
		return nil
	}
	if err := m.evidence.AppendAuditEvent(ctx, record); err != nil {
		return catalog.Wrap(catalog.CodeDependencyUnavailable, "retain live-search diagnostic evidence", err)
	}
	return nil
}

// mutationEvidence pairs an attributed audit transition with its independently
// selected public notification, when required.
func (m *Manager) mutationEvidence(ctx context.Context, actor identity.Actor, action, targetType, targetID, reason string, factsValue any, beforeDigest, afterDigest, publicAction, publicTargetType string, at time.Time) (MutationEvidence, error) {
	record, err := m.auditRecord(ctx, actor, action, targetType, targetID, reason, factsValue, at)
	if err != nil || record.ID == "" {
		return MutationEvidence{}, err
	}
	record.BeforeDigest = beforeDigest
	record.AfterDigest = afterDigest
	if err := audit.ValidateNewRecord(record); err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "validate federation audit evidence", err)
	}
	evidence := MutationEvidence{Audit: record}
	if publicAction == "" {
		return evidence, nil
	}
	outboxID, err := m.ids.New(at.Add(time.Nanosecond))
	if err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "create federation outbox identifier", err)
	}
	data, err := events.MarshalReference(publicAction, publicTargetType, targetID, afterDigest)
	if err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "construct federation notification", err)
	}
	outbox := catalog.OutboxEvent{CausalTrace: catalog.CausalTrace(ctx), ID: outboxID, TenantID: record.TenantID, Type: events.CanonicalType(publicAction), Subject: targetID, Data: data, CreatedAt: record.At}
	if m.acknowledgeOutboxAtCreation {
		publishedAt := record.At
		outbox.PublishedAt = &publishedAt
	}
	evidence.Outbox = &outbox
	return evidence, nil
}

// FinalizeSyncEvidence binds synchronization evidence to the exact snapshot
// shape computed while the store holds its mutation transaction. In
// particular, withdrawal counts cannot be known safely before current
// observations are locked and reconciled.
func FinalizeSyncEvidence(evidence MutationEvidence, snapshot Snapshot) (MutationEvidence, error) {
	if evidence.Audit.ID == "" {
		return evidence, nil
	}
	if evidence.Audit.Action != audit.ActionSourceSyncCompleted && evidence.Audit.Action != audit.ActionSourceSyncFailed {
		return MutationEvidence{}, catalog.E(catalog.CodeInternal, "source synchronization evidence has an unexpected action")
	}
	facts, ok := evidence.Audit.Details["facts"].(map[string]any)
	if !ok {
		return MutationEvidence{}, catalog.E(catalog.CodeInternal, "source synchronization evidence facts are unavailable")
	}
	boundFacts := make(map[string]any, len(facts))
	for name, value := range facts {
		boundFacts[name] = value
	}
	boundFacts["sourceId"] = snapshot.SourceID
	boundFacts["sourceRevision"] = snapshot.SourceRevision
	boundFacts["state"] = string(snapshot.State)
	boundFacts["policyDigest"] = snapshot.PolicyDigest
	boundFacts["memberCount"] = snapshot.MemberCount
	boundFacts["added"] = snapshot.Added
	boundFacts["updated"] = snapshot.Updated
	boundFacts["withdrawn"] = snapshot.Withdrawn
	evidence.Audit.Details["facts"] = boundFacts
	afterDigest, err := snapshotStateDigest(snapshot)
	if err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "digest final source synchronization state", err)
	}
	evidence.Audit.AfterDigest = afterDigest
	if err := audit.ValidateNewRecord(evidence.Audit); err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "validate final source synchronization evidence", err)
	}
	if evidence.Outbox == nil {
		return evidence, nil
	}
	publicAction := "source.sync.completed"
	if evidence.Audit.Action == audit.ActionSourceSyncFailed {
		publicAction = "source.sync.failed"
	}
	data, err := events.MarshalReference(publicAction, "source_snapshot", snapshot.ID, evidence.Audit.AfterDigest)
	if err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "construct final source synchronization notification", err)
	}
	outbox := *evidence.Outbox
	outbox.TenantID = snapshot.TenantID
	outbox.Type = events.CanonicalType(publicAction)
	outbox.Subject = snapshot.ID
	outbox.Data = data
	evidence.Outbox = &outbox
	return evidence, nil
}

// auditRecord validates action facts and binds current membership or worker
// execution attribution.
func (m *Manager) auditRecord(ctx context.Context, actor identity.Actor, action, targetType, targetID, reason string, factsValue any, at time.Time) (audit.Record, error) {
	if m.evidence == nil {
		return audit.Record{}, nil
	}
	facts, err := audit.Facts(factsValue)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "construct federation audit facts", err)
	}
	idValue, err := m.ids.New(at)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "create federation audit identifier", err)
	}
	principal := audit.Principal{ID: actor.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(actor.ActorType), RealmID: actor.RealmID}
	execution := audit.ExecutionFrom(ctx)
	if job, ok := audit.JobExecutionFrom(ctx); ok {
		principal = audit.Principal{ID: audit.PrincipalIDCatalogWorker, Type: audit.PrincipalServiceExecutor}
		execution.AuthorizationDigest = ""
		_ = job
	} else if execution.AuthorizationDigest == "" || len(identity.RecordedPermissionChecks(ctx)) > 0 {
		execution.AuthorizationDigest = identity.DecisionDigest(ctx)
	}
	record, err := audit.NewRecord(idValue, actor.TenantID, principal, action, targetType, targetID, reason, execution, facts, at)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "construct federation audit event", err)
	}
	return record, nil
}

// sourceStateDigest fingerprints the retained source state for before/after
// audit binding.
func sourceStateDigest(source Source) (string, error) {
	configurationDigest, err := sourceConfigurationDigest(source)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Revision            int64        `json:"revision"`
		Status              SourceStatus `json:"status"`
		ConfigurationDigest string       `json:"configurationDigest"`
		PolicyDigest        string       `json:"policyDigest"`
	}{source.Revision, source.Status, configurationDigest, source.PolicyDigest})
	if err != nil {
		return "", err
	}
	return audit.DigestBytes(raw), nil
}

// inspectionConfigurationDigest binds the requested inspection endpoint and
// opaque access references.
func inspectionConfigurationDigest(input InspectionInput) (string, error) {
	raw, err := json.Marshal(struct {
		URL               string `json:"url"`
		NetworkProfile    string `json:"networkProfile,omitempty"`
		CredentialBinding string `json:"credentialBinding,omitempty"`
	}{input.URL, input.NetworkProfile, input.CredentialBinding})
	if err != nil {
		return "", fmt.Errorf("encode source inspection configuration: %w", err)
	}
	return audit.DigestBytes(raw), nil
}

// inspectionDigest fingerprints the retained inspection evidence without
// fetching its endpoint again.
func inspectionDigest(inspection Inspection) string {
	return audit.DigestBytes([]byte(inspection.ID + ":" + inspection.BodyDigest + ":" + inspection.PolicyDigest))
}

// snapshotStateDigest fingerprints the retained synchronization outcome for
// audit consistency.
func snapshotStateDigest(snapshot Snapshot) (string, error) {
	raw, err := json.Marshal(struct {
		ID                  string        `json:"id"`
		SourceID            string        `json:"sourceId"`
		SourceRevision      int64         `json:"sourceRevision"`
		State               SnapshotState `json:"state"`
		EnumerationComplete bool          `json:"enumerationComplete"`
		MemberCount         int           `json:"memberCount"`
		Added               int           `json:"added"`
		Updated             int           `json:"updated"`
		Withdrawn           int           `json:"withdrawn"`
		BodyDigest          string        `json:"bodyDigest"`
		PolicyDigest        string        `json:"policyDigest"`
	}{snapshot.ID, snapshot.SourceID, snapshot.SourceRevision, snapshot.State, snapshot.EnumerationComplete, snapshot.MemberCount, snapshot.Added, snapshot.Updated, snapshot.Withdrawn, snapshot.BodyDigest, snapshot.PolicyDigest})
	if err != nil {
		return "", fmt.Errorf("encode source snapshot state: %w", err)
	}
	return audit.DigestBytes(raw), nil
}

// originClass distinguishes the default public profile from an explicitly named
// private network profile.
func originClass(networkProfile string) string {
	if strings.TrimSpace(networkProfile) == "" {
		return "public"
	}
	return "named_private:" + strings.TrimSpace(networkProfile)
}

// sortedCapabilities returns a deterministic projection of enabled capability
// names.
func sortedCapabilities(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for name, enabled := range values {
		if enabled {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	if len(result) > 32 {
		result = result[:32]
	}
	return result
}

// httpClass returns a valid status class such as 2xx, or empty for a non-HTTP
// status.
func httpClass(status int) string {
	if status < 100 || status > 599 {
		return ""
	}
	return string(rune('0'+status/100)) + "xx"
}
