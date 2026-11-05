package federation

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
)

// TrustVerifier is the configured offline verifier and bounded acquisition port.
// Guard is checked before every external request; no SQL transaction spans I/O.
type TrustVerifier interface {
	Enabled() bool
	ValidateSource(context.Context, Source) error
	Policy(string) (digest, authority string, available bool)
	Assess(context.Context, jobs.TrustPayload, func(context.Context) error, func() time.Time) (anstrust.Bundle, anstrust.Result, error)
}

func WithTrustVerifier(verifier TrustVerifier) ManagerOption {
	return func(m *Manager) error { m.trust = verifier; return nil }
}

// TrustRequest selects a previously observed entry, never client-supplied evidence.
// ObservationID selects indexed evidence. Otherwise Text selects one direct peer.
type TrustRequest struct {
	SourceRevision   int64  `json:"sourceRevision"`
	Identifier       string `json:"identifier"`
	Version          string `json:"version,omitempty"`
	VersionPresent   bool   `json:"versionPresent"`
	MediaType        string `json:"mediaType"`
	Delivery         string `json:"delivery"`
	ExactEntryDigest string `json:"exactEntryDigest"`
	ObservationID    string `json:"observationId,omitempty"`
	Text             string `json:"text,omitempty"`
	Reason           string `json:"reason"`
}

// UnmarshalJSON preserves required false-valued version presence explicitly.
func (r *TrustRequest) UnmarshalJSON(raw []byte) error {
	type wire TrustRequest
	var value wire
	if err := jsondoc.Decode(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, name := range []string{"sourceRevision", "identifier", "versionPresent", "mediaType", "delivery", "exactEntryDigest", "reason"} {
		if _, ok := fields[name]; !ok {
			return catalog.E(catalog.CodeInvalidArgument, "trust assessment selection is incomplete")
		}
	}
	_, hasVersion := fields["version"]
	if hasVersion != value.VersionPresent {
		return catalog.E(catalog.CodeInvalidArgument, "version presence does not match the selected version")
	}
	_, hasObservation := fields["observationId"]
	_, hasText := fields["text"]
	if hasObservation == hasText {
		return catalog.E(catalog.CodeInvalidArgument, "select exactly one indexed observation or live query")
	}
	*r = TrustRequest(value)
	return nil
}

func (m *Manager) trustSource(ctx context.Context, sourceID string, manage bool) (Source, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, err := permissionActor(ctx, "source.read_evidence")
	if err != nil {
		return Source{}, err
	}
	if manage {
		for _, permission := range []identity.Permission{"source.manage", "federation.discover"} {
			if _, err = permissionActor(ctx, permission); err != nil {
				return Source{}, err
			}
		}
	}
	return m.store.GetSource(ctx, a.TenantID, sourceID)
}
func TrustSourceMatches(source Source, p jobs.TrustPayload) bool {
	return source.ID == p.SourceID && source.Revision == p.SourceRevision && source.Status == SourceActive && source.TrustVerificationPolicy == p.PolicyName && source.PolicyDigest == p.SourcePolicyDigest && source.CredentialRevision == p.CredentialRevision
}
func (m *Manager) checkTrustTarget(ctx context.Context, p jobs.TrustPayload) error {
	a, ok := identity.FromContext(ctx)
	if !ok {
		return catalog.E(catalog.CodeForbidden, "trust execution principal is required")
	}
	source, err := m.store.GetSource(ctx, a.TenantID, p.SourceID)
	if err != nil {
		return err
	}
	if m.trust == nil {
		return catalog.E(catalog.CodeDependencyUnavailable, "trust verification is disabled")
	}
	digest, _, ok := m.trust.Policy(p.PolicyName)
	if !ok || digest != p.PolicyDigest || !TrustSourceMatches(source, p) || !m.sourceUsesCurrentPolicy(source) {
		return catalog.E(catalog.CodePreconditionFailed, "trust assessment target or policy changed; select the entry again")
	}
	err = m.trust.ValidateSource(ctx, source)
	if errors.Is(err, fetcher.ErrCredentialChanged) || errors.Is(err, fetcher.ErrPolicyChanged) || errors.Is(err, fetcher.ErrPolicyDenied) {
		return catalog.Wrap(catalog.CodePreconditionFailed, "trust assessment source credentials or egress policy changed; inspect the source again", err)
	}
	return err
}
func (m *Manager) PrepareTrustAssessment(ctx context.Context, sourceID string, input TrustRequest) (jobs.TrustPayload, error) {
	ctx = identity.WithPermissionContext(ctx)
	source, err := m.trustSource(ctx, sourceID, true)
	if err != nil {
		return jobs.TrustPayload{}, err
	}
	if _, err = audit.NormalizeReason(audit.ActionSourceTrustRequested, input.Reason); err != nil {
		return jobs.TrustPayload{}, catalog.E(catalog.CodeInvalidArgument, "trust assessment reason is required")
	}
	if m.trust == nil {
		return jobs.TrustPayload{}, catalog.E(catalog.CodeDependencyUnavailable, "trust verification is disabled")
	}
	digest, _, ok := m.trust.Policy(source.TrustVerificationPolicy)
	if !ok {
		return jobs.TrustPayload{}, catalog.E(catalog.CodeDependencyUnavailable, "source trust policy is unavailable")
	}
	p := jobs.TrustPayload{SourceID: source.ID, SourceRevision: input.SourceRevision, SourcePolicyDigest: source.PolicyDigest, CredentialRevision: source.CredentialRevision, PolicyName: source.TrustVerificationPolicy, PolicyDigest: digest, Identifier: input.Identifier, Version: input.Version, VersionPresent: input.VersionPresent, MediaType: input.MediaType, Delivery: input.Delivery, ExactEntryDigest: input.ExactEntryDigest}
	if _, err = catalog.ParseAIRIdentifier(input.Identifier); err != nil || input.SourceRevision < 1 || !validPolicyDigest(input.ExactEntryDigest) || input.Delivery != "url" && input.Delivery != "data" || len(input.MediaType) == 0 || utf8.RuneCountInString(input.MediaType) > 255 || utf8.RuneCountInString(input.Version) > 128 || !input.VersionPresent && input.Version != "" {
		return jobs.TrustPayload{}, catalog.E(catalog.CodeInvalidArgument, "trust assessment selection is invalid")
	}
	if err = m.checkTrustTarget(ctx, p); err != nil {
		return jobs.TrustPayload{}, err
	}
	if input.ObservationID != "" {
		if !identity.ValidTenantID(input.ObservationID) || input.Text != "" {
			return jobs.TrustPayload{}, catalog.E(catalog.CodeInvalidArgument, "indexed selection requires only an observation ID")
		}
		observation, err := m.store.GetObservation(ctx, source.TenantID, input.ObservationID)
		if err != nil {
			return jobs.TrustPayload{}, err
		}
		if observation.SourceID != source.ID || observation.Freshness != FreshCurrent || !observation.RawEntryAvailable || source.LastCompleteSnapshotID == "" || !scopeMatches(source.Scope, sourceEntry{Identifier: observation.Identifier, Version: observation.Version, VersionPresent: observation.VersionPresent, Type: observation.MediaType, Tags: observation.SearchTags}) {
			return jobs.TrustPayload{}, catalog.E(catalog.CodePreconditionFailed, "indexed entry is no longer current; select it again")
		}
		current, err := m.store.ObservationHasSnapshotEvidence(ctx, source.TenantID, observation.ID, source.ID, source.LastCompleteSnapshotID, source.Revision, observation.ExactEntryDigest)
		if err != nil {
			return jobs.TrustPayload{}, err
		}
		if !current {
			return jobs.TrustPayload{}, catalog.E(catalog.CodePreconditionFailed, "indexed entry lacks current snapshot evidence; select it again")
		}
		p.Entry = append([]byte(nil), observation.RawEntry...)
	} else {
		if input.Text == "" {
			return jobs.TrustPayload{}, catalog.E(catalog.CodeInvalidArgument, "live selection requires search text")
		}
		response, err := m.LiveSearch(ctx, LiveSearchRequest{Text: input.Text, PageSize: MaxLiveSearchPageSize, SourceIDs: []string{sourceID}, Administrative: true, DirectOnly: true, Reason: input.Reason})
		if err != nil {
			return jobs.TrustPayload{}, err
		}
		for _, result := range response.Results {
			if jobs.TrustEntryMatches(result.Entry, p) {
				if p.Entry != nil {
					return jobs.TrustPayload{}, catalog.E(catalog.CodeConflict, "live target is ambiguous")
				}
				p.Entry = append([]byte(nil), result.Entry...)
			}
		}
	}
	if !jobs.TrustEntryMatches(p.Entry, p) || len(p.Entry) > anstrust.MaxEntryBytes {
		return jobs.TrustPayload{}, catalog.E(catalog.CodePreconditionFailed, "entry changed or is unavailable; select it again")
	}
	if err = m.checkTrustTarget(ctx, p); err != nil {
		return jobs.TrustPayload{}, err
	}
	return p, nil
}

// ValidateTrustJob protects metadata, controls and retries without external I/O.
func (m *Manager) ValidateTrustJob(ctx context.Context, job jobs.Job) error {
	p, err := jobs.DecodeTrustPayload(job)
	if err != nil {
		return err
	}
	a, err := permissionActor(ctx, "source.read_evidence")
	if err != nil {
		return err
	}
	if a.TenantID != job.TenantID || !identity.ValidTenantID(p.SourceID) {
		return catalog.E(catalog.CodeNotFound, "trust assessment was not found")
	}
	return nil
}

func (m *Manager) ExecuteTrustAssessment(ctx context.Context, job jobs.Job) (jobs.TrustAssessment, error) {
	p, err := jobs.DecodeTrustPayload(job)
	if err != nil {
		return jobs.TrustAssessment{}, err
	}
	if err = identity.RequireInternalTarget(ctx, jobs.TrustKind, job.TenantID, p.SourceID); err != nil {
		return jobs.TrustAssessment{}, err
	}
	if err = jobs.CheckClaimedAttempt(ctx, job); err != nil {
		return jobs.TrustAssessment{}, err
	}
	guard := func(ctx context.Context) error {
		if err := jobs.CheckClaimedAttempt(ctx, job); err != nil {
			return err
		}
		return m.checkTrustTarget(ctx, p)
	}
	if err = guard(ctx); err != nil {
		return jobs.TrustAssessment{}, err
	}
	bundle, result, err := m.trust.Assess(ctx, p, guard, m.clock.Now)
	if err != nil {
		return jobs.TrustAssessment{}, err
	}
	if err = guard(ctx); err != nil {
		return jobs.TrustAssessment{}, err
	}
	return m.trustAssessmentResult(ctx, job, p, bundle, result)
}

// trustAssessmentResult assembles bounded evidence and completion attribution.
// ExecuteTrustAssessment owns acquisition and the current claim/source fences.
func (m *Manager) trustAssessmentResult(ctx context.Context, job jobs.Job, p jobs.TrustPayload, bundle anstrust.Bundle, result anstrust.Result) (jobs.TrustAssessment, error) {
	_, authority, _ := m.trust.Policy(p.PolicyName)
	r := jobs.TrustAssessment{SourceID: p.SourceID, SourceRevision: p.SourceRevision, PolicyName: p.PolicyName, PolicyDigest: p.PolicyDigest, Authority: authority, ExactEntryDigest: p.ExactEntryDigest, MediaType: p.MediaType, Delivery: p.Delivery, Assessment: result, Evidence: bundle, SoftwareTrust: "not_assessed", ArtifactIntegrity: "not_assessed", Rights: "not_assessed"}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > jobs.MaxTrustRecordBytes {
		return jobs.TrustAssessment{}, catalog.E(catalog.CodeInternal, "trust assessment result exceeds its limit")
	}
	facts, err := audit.Facts(map[string]any{"sourceId": p.SourceID, "sourceRevision": p.SourceRevision, "entryDigest": p.ExactEntryDigest, "policyDigest": p.PolicyDigest, "resultDigest": audit.DigestBytes(raw), "currentRegistration": result.CurrentRegistration, "jobId": job.ID, "jobKind": job.Kind, "jobAttempt": job.Attempt, "initiator": job.Actor})
	if err != nil {
		return jobs.TrustAssessment{}, err
	}
	at := m.clock.Now()
	id, err := m.ids.New(at)
	if err != nil {
		return jobs.TrustAssessment{}, err
	}
	r.EvidenceRecord, err = audit.NewRecord(id, job.TenantID, audit.Principal{ID: audit.PrincipalIDCatalogWorker, Type: audit.PrincipalServiceExecutor}, audit.ActionSourceTrustCompleted, "job", job.ID, "", audit.ExecutionFrom(ctx), facts, at)
	return r, err
}

type TrustResult struct {
	Result        jobs.TrustAssessment `json:"result"`
	Current       bool                 `json:"current"`
	Applicability string               `json:"applicability"`
}

func (m *Manager) ReadTrustAssessment(ctx context.Context, sourceID string, job jobs.Job) (TrustResult, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, err := permissionActor(ctx, "source.read_evidence")
	if err != nil {
		return TrustResult{}, err
	}
	p, err := jobs.DecodeTrustPayload(job)
	if err != nil || p.SourceID != sourceID || job.TenantID != a.TenantID {
		return TrustResult{}, catalog.E(catalog.CodeNotFound, "trust assessment was not found")
	}

	if job.State != jobs.StateSucceeded {
		return TrustResult{}, catalog.E(catalog.CodeInvalidTransition, "trust assessment has not completed successfully")
	}
	r, err := jobs.DecodeTrustAssessment(p, job.Result)
	if err != nil {
		return TrustResult{}, err
	}
	// Retain disclosure evidence before any private signed bytes can be returned.
	if m.evidence == nil {
		return TrustResult{}, catalog.E(catalog.CodeDependencyUnavailable, "trust-assessment read evidence is unavailable")
	}
	record, err := m.auditRecord(ctx, a, audit.ActionSourceTrustRead, "job", job.ID, "", sensitiveReadFacts{ContentDigest: audit.DigestBytes(job.Result), SizeBytes: len(job.Result)}, m.clock.Now())
	if err != nil {
		return TrustResult{}, err
	}
	if record.ID != "" {
		if err = m.evidence.AppendAuditEvent(ctx, record); err != nil {
			return TrustResult{}, catalog.Wrap(catalog.CodeDependencyUnavailable, "retain trust-assessment read evidence", err)
		}
	}
	value := TrustResult{Result: r, Applicability: "historical"}
	if err = m.checkTrustTarget(ctx, p); err != nil {
		return value, nil //nolint:nilerr // Stale source/policy preserves historical evidence under current read authorization.
	}
	if !r.Assessment.CurrentRegistration {
		value.Applicability = "negative_or_insufficient"
		return value, nil
	}
	now := m.clock.Now()
	if r.Assessment.ValidUntil == nil || !now.Before(*r.Assessment.ValidUntil) || now.Before(r.Assessment.CheckedAt) {
		value.Applicability = "expired"
		return value, nil
	}
	value.Current = true
	value.Applicability = "current"
	return value, nil
}

// WithTrustSource holds source eligibility through one local Job transition.
func (m *MemoryStore) WithTrustSource(ctx context.Context, tenant string, p jobs.TrustPayload, work func() error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	source, ok := m.sources[p.SourceID]
	if !ok || source.TenantID != tenant || !TrustSourceMatches(source, p) {
		return catalog.E(catalog.CodePreconditionFailed, "trust assessment source changed")
	}
	return work()
}

// ValidateTrustEnqueue rechecks eligibility without repeating live selection.
func (m *Manager) ValidateTrustEnqueue(ctx context.Context, job jobs.Job) error {
	p, err := jobs.DecodeTrustPayload(job)
	if err != nil {
		return err
	}
	if _, err = m.trustSource(ctx, p.SourceID, true); err != nil {
		return err
	}
	return m.checkTrustTarget(ctx, p)
}
