// Job evidence records enqueue and worker transitions while retaining the
// initiating actor and bounded execution attribution.

package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

type sourceSyncRequestFacts struct {
	SourceRevision int64  `json:"sourceRevision"`
	JobID          string `json:"jobId"`
	Trigger        string `json:"trigger"`
	Deduplicated   bool   `json:"deduplicated"`
}

type semanticReindexFacts struct {
	Profile      string `json:"profile"`
	Scope        string `json:"scope"`
	JobID        string `json:"jobId"`
	Deduplicated bool   `json:"deduplicated"`
}

type jobCancellationFacts struct {
	Kind                  string       `json:"kind"`
	BeforeState           State        `json:"beforeState"`
	AfterState            State        `json:"afterState"`
	CancellationRequested bool         `json:"cancellationRequested"`
	FailureClass          FailureClass `json:"failureClass,omitempty"`
}

type jobRetryFacts struct {
	Kind             string       `json:"kind"`
	BeforeState      State        `json:"beforeState"`
	AfterState       State        `json:"afterState"`
	ParentJobID      string       `json:"parentJobId"`
	ReplacementJobID string       `json:"replacementJobId"`
	FailureClass     FailureClass `json:"failureClass,omitempty"`
}

type sourceSyncJobPayload struct {
	SourceID       string `json:"sourceId"`
	SourceRevision int64  `json:"sourceRevision"`
	Trigger        string `json:"trigger"`
}

// newAuditRecord binds reviewed Job facts to the initiating membership and
// current execution attribution.
func (s *Service) newAuditRecord(ctx context.Context, actor identity.Actor, action, targetType, targetID, reason string, value any, at time.Time) (audit.Record, error) {
	facts, err := audit.Facts(value)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "construct Job audit facts", err)
	}
	idValue, err := s.ids.New(at)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "create Job audit identifier", err)
	}
	execution := audit.ExecutionFrom(ctx)
	if execution.AuthorizationDigest == "" || len(identity.RecordedPermissionChecks(ctx)) > 0 {
		execution.AuthorizationDigest = identity.DecisionDigest(ctx)
	}
	record, err := audit.NewRecord(idValue, actor.TenantID, audit.Principal{ID: actor.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(actor.ActorType), RealmID: actor.RealmID}, action, targetType, targetID, reason, execution, facts, at)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "construct Job audit record", err)
	}
	return record, nil
}

// cancellationFacts describes the before/after Job state and cancellation flag
// for reasoned operator evidence.
func cancellationFacts(before, after Job) jobCancellationFacts {
	return jobCancellationFacts{Kind: before.Kind, BeforeState: before.State, AfterState: after.State, CancellationRequested: after.CancellationRequested, FailureClass: before.FailureClass}
}

// FinalizeEnqueueEvidence binds a prevalidated request record to the Job that
// the atomic enqueue actually returned, including deduplication reuse.
func FinalizeEnqueueEvidence(record audit.Record, job Job, existing bool) (audit.Record, error) {
	if record.ID == "" {
		return audit.Record{}, catalog.E(catalog.CodeInternal, "Job enqueue evidence is empty")
	}
	if record.TenantID != job.TenantID {
		return audit.Record{}, catalog.E(catalog.CodeInternal, "Job enqueue evidence does not match the Job tenant")
	}
	switch record.Action {
	case audit.ActionSourceTrustRequested:
		p, err := DecodeTrustPayload(job)
		if err != nil || record.TargetType != "job" || factString(record, "sourceId") != p.SourceID || factInt64(record, "sourceRevision") != p.SourceRevision || factString(record, "entryDigest") != p.ExactEntryDigest || factString(record, "policyDigest") != p.PolicyDigest || !existing && (record.TargetID != job.ID || record.Actor != job.Actor || job.State != StateQueued || factString(record, "jobId") != job.ID) || existing && job.State != StateQueued && job.State != StateRunning && job.State != StateWaiting {
			return audit.Record{}, catalog.E(catalog.CodeInternal, "trust enqueue evidence does not match Job")
		}
		facts, err := audit.Facts(map[string]any{"sourceId": p.SourceID, "sourceRevision": p.SourceRevision, "entryDigest": p.ExactEntryDigest, "policyDigest": p.PolicyDigest, "jobId": job.ID, "deduplicated": existing})
		if err != nil {
			return audit.Record{}, err
		}
		record.TargetID = job.ID
		record.Details["facts"] = facts
	case audit.ActionSemanticReindexRequested:
		payload, payloadErr := catalog.DecodeSemanticReindex(job.Payload, job.PayloadVersion)
		if job.Kind != "semantic.reindex" || payloadErr != nil || job.DeduplicationKey != payload.Profile+":"+payload.Scope || record.TargetType != "job" || record.Details == nil || factString(record, "profile") != payload.Profile || factString(record, "scope") != payload.Scope || !existing && (record.TargetID != job.ID || record.Actor != job.Actor || job.State != StateQueued) || existing && job.State != StateQueued && job.State != StateRunning && job.State != StateWaiting {
			return audit.Record{}, catalog.E(catalog.CodeInternal, "semantic reindex evidence does not match the Job")
		}
		facts, err := audit.Facts(semanticReindexFacts{Profile: payload.Profile, Scope: payload.Scope, JobID: job.ID, Deduplicated: existing})
		if err != nil {
			return audit.Record{}, err
		}
		record.TargetID = job.ID
		record.Details["facts"] = facts
		record.AfterDigest, err = jobStateDigest(job)
		if err != nil {
			return audit.Record{}, err
		}
	case audit.ActionSourceSyncRequested:
		payload, err := decodeSourceSyncJobPayload(job)
		requestTrigger := factString(record, "trigger")
		if err != nil || record.Details == nil || record.TargetType != "federation_source" || record.TargetID != payload.SourceID || factInt64(record, "sourceRevision") != payload.SourceRevision || requestTrigger != "manual" || job.DeduplicationKey != sourceSyncDeduplicationKey(payload) {
			return audit.Record{}, catalog.E(catalog.CodeInternal, "source synchronization request evidence does not match the Job")
		}
		if (payload.Trigger == "automatic" && job.Actor != audit.InitiatorSourceMaintainer) || (payload.Trigger == "manual" && !identity.ValidTenantID(job.Actor)) {
			return audit.Record{}, catalog.E(catalog.CodeInternal, "source synchronization request evidence does not match the Job")
		}
		if !existing && (payload.Trigger != requestTrigger || factString(record, "jobId") != job.ID || job.Actor != record.Actor || job.State != StateQueued) {
			return audit.Record{}, catalog.E(catalog.CodeInternal, "source synchronization request evidence does not match the Job")
		}
		if existing && job.State != StateQueued && job.State != StateRunning && job.State != StateWaiting {
			return audit.Record{}, catalog.E(catalog.CodeInternal, "source synchronization request evidence does not match the Job")
		}
		facts, err := audit.Facts(sourceSyncRequestFacts{SourceRevision: payload.SourceRevision, JobID: job.ID, Trigger: requestTrigger, Deduplicated: existing})
		if err != nil {
			return audit.Record{}, err
		}
		record.Details["facts"] = facts
		record.AfterDigest, err = jobStateDigest(job)
		if err != nil {
			return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "digest enqueued Job state", err)
		}
	case audit.ActionJobRetried:
		if existing || job.ParentJobID == "" || job.State != StateQueued || job.Actor != record.Actor || record.TargetType != "job" || record.TargetID != job.ParentJobID || factString(record, "parentJobId") != job.ParentJobID || factString(record, "replacementJobId") != job.ID || factString(record, "kind") != job.Kind || factString(record, "afterState") != string(job.State) {
			return audit.Record{}, catalog.E(catalog.CodeInternal, "Job retry evidence does not match the replacement Job")
		}
		var err error
		record.AfterDigest, err = jobStateDigest(job)
		if err != nil {
			return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "digest replacement Job state", err)
		}
	default:
		return audit.Record{}, catalog.E(catalog.CodeInternal, "Job enqueue evidence has an unexpected action")
	}
	if err := audit.ValidateNewRecord(record); err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "validate Job enqueue evidence", err)
	}
	return record, nil
}

// decodeSourceSyncJobPayload accepts only the current closed source.sync
// payload version and validated source revision binding.
func decodeSourceSyncJobPayload(job Job) (sourceSyncJobPayload, error) {
	if job.Kind != "source.sync" || job.PayloadVersion != 2 || jsondoc.Validate(job.Payload) != nil {
		return sourceSyncJobPayload{}, catalog.E(catalog.CodeInternal, "source synchronization Job payload is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(job.Payload))
	decoder.DisallowUnknownFields()
	var payload sourceSyncJobPayload
	if err := decoder.Decode(&payload); err != nil || !identity.ValidTenantID(payload.SourceID) || payload.SourceRevision < 1 || payload.Trigger != "manual" && payload.Trigger != "automatic" {
		return sourceSyncJobPayload{}, catalog.E(catalog.CodeInternal, "source synchronization Job payload is invalid")
	}
	return payload, nil
}

// ValidateActionablePayload checks only payloads a current worker could still
// claim. Terminal Job history is intentionally not forced through newer
// executable contracts during restore qualification.
func ValidateActionablePayload(job Job) error {
	switch job.Kind {
	case TrustKind:
		_, err := DecodeTrustPayload(job)
		return err
	case "semantic.reindex":
		_, err := catalog.DecodeSemanticReindex(job.Payload, job.PayloadVersion)
		return err
	case "source.sync":
		var payload struct {
			SourceID       string `json:"sourceId"`
			SourceRevision int64  `json:"sourceRevision"`
			Trigger        string `json:"trigger"`
		}
		if jsondoc.Validate(job.Payload) != nil {
			return catalog.E(catalog.CodeInvalidArgument, "source synchronization Job payload is invalid")
		}
		decoder := json.NewDecoder(bytes.NewReader(job.Payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil || !identity.ValidTenantID(payload.SourceID) || payload.SourceRevision < 1 || job.PayloadVersion != 2 {
			return catalog.E(catalog.CodeInvalidArgument, "source synchronization Job payload is invalid")
		}
		if payload.Trigger != "manual" && payload.Trigger != "automatic" {
			return catalog.E(catalog.CodeInvalidArgument, "source synchronization Job payload is invalid")
		}
	case "publication.generate":
		if job.PayloadVersion != 1 || jsondoc.Validate(job.Payload) != nil {
			return catalog.E(catalog.CodeInvalidArgument, "publication generation Job payload is invalid")
		}
		decoder := json.NewDecoder(bytes.NewReader(job.Payload))
		decoder.DisallowUnknownFields()
		var payload struct {
			Host string `json:"host"`
		}
		if err := decoder.Decode(&payload); err != nil || len(payload.Host) > 253 || strings.ContainsRune(payload.Host, '\x00') {
			return catalog.E(catalog.CodeInvalidArgument, "publication generation Job payload is invalid")
		}
	default:
		return catalog.E(catalog.CodeInvalidArgument, "actionable Job kind has no current worker handler")
	}
	return nil
}

// sourceSyncDeduplicationKey identifies equivalent source synchronization work
// across the allowed trigger paths.
func sourceSyncDeduplicationKey(payload sourceSyncJobPayload) string {
	return "source.sync:" + payload.SourceID + ":" + strconv.FormatInt(payload.SourceRevision, 10)
}

// FinalizeCancellationEvidence binds a cancellation record to the state
// observed while the Job row is locked.
func FinalizeCancellationEvidence(record audit.Record, before, after Job) (audit.Record, error) {
	if record.ID == "" {
		return audit.Record{}, catalog.E(catalog.CodeInternal, "Job cancellation evidence is empty")
	}
	if record.Action != audit.ActionJobCancellationRequested || record.Details == nil || before.ID == "" || before.ID != after.ID || before.TenantID == "" || before.TenantID != after.TenantID || record.TenantID != before.TenantID || record.TargetType != "job" || record.TargetID != before.ID {
		return audit.Record{}, catalog.E(catalog.CodeInternal, "Job cancellation evidence does not match the Job")
	}
	facts, err := audit.Facts(cancellationFacts(before, after))
	if err != nil {
		return audit.Record{}, err
	}
	record.Details["facts"] = facts
	record.BeforeDigest, err = jobStateDigest(before)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "digest Job state before cancellation", err)
	}
	record.AfterDigest, err = jobStateDigest(after)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "digest Job state after cancellation", err)
	}
	if err := audit.ValidateNewRecord(record); err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "validate Job cancellation evidence", err)
	}
	return record, nil
}

// jobStateDigest fingerprints the Job state used to bind enqueue, cancellation
// and retry evidence.
func jobStateDigest(job Job) (string, error) {
	raw, err := json.Marshal(struct {
		ID                    string       `json:"id"`
		Kind                  string       `json:"kind"`
		ParentJobID           string       `json:"parentJobId,omitempty"`
		State                 State        `json:"state"`
		Progress              int          `json:"progress"`
		Attempt               int          `json:"attempt"`
		AttemptLimit          int          `json:"attemptLimit"`
		CancellationRequested bool         `json:"cancellationRequested"`
		FailureClass          FailureClass `json:"failureClass,omitempty"`
	}{job.ID, job.Kind, job.ParentJobID, job.State, job.Progress, job.Attempt, job.AttemptLimit, job.CancellationRequested, job.FailureClass})
	if err != nil {
		return "", err
	}
	return audit.DigestBytes(raw), nil
}

// factString reads a string from validated audit facts, returning empty for
// absent or differently typed values.
func factString(record audit.Record, name string) string {
	facts, _ := record.Details["facts"].(map[string]any)
	value, _ := facts[name].(string)
	return value
}

// factInt64 reads supported exact integer fact encodings and returns zero for
// other values.
func factInt64(record audit.Record, name string) int64 {
	facts, _ := record.Details["facts"].(map[string]any)
	switch value := facts[name].(type) {
	case json.Number:
		result, _ := value.Int64()
		return result
	case int64:
		return value
	case int:
		return int64(value)
	default:
		return 0
	}
}
