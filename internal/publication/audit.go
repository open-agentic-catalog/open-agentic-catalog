// Publication evidence binds candidate contents and active-pointer transitions
// to immutable audit facts and independently classified public notifications.

package publication

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type candidateFacts struct {
	Host             string   `json:"host"`
	Audience         Audience `json:"audience"`
	Generation       int64    `json:"generation"`
	ARDProfile       string   `json:"ardProfile"`
	AICatalogProfile string   `json:"aiCatalogProfile"`
	ARDDigest        string   `json:"ardDigest"`
	AICatalogDigest  string   `json:"aiCatalogDigest"`
	EntryCount       int      `json:"entryCount"`
	ExclusionCount   int      `json:"exclusionCount"`
	Trigger          string   `json:"trigger"`
	JobID            string   `json:"jobId,omitempty"`
	JobKind          string   `json:"jobKind,omitempty"`
	JobAttempt       int      `json:"jobAttempt,omitempty"`
	Initiator        string   `json:"initiator,omitempty"`
}

type transitionFacts struct {
	Host               string `json:"host"`
	Generation         int64  `json:"generation"`
	ReplacedSnapshotID string `json:"replacedSnapshotId,omitempty"`
	ARDProfile         string `json:"ardProfile"`
	AICatalogProfile   string `json:"aiCatalogProfile"`
	ARDDigest          string `json:"ardDigest"`
	AICatalogDigest    string `json:"aiCatalogDigest"`
}

type withdrawalFacts struct {
	Host                string `json:"host"`
	Generation          int64  `json:"generation"`
	Cause               string `json:"cause"`
	OriginatingAction   string `json:"originatingAction"`
	OriginatingTargetID string `json:"originatingTargetId"`
	ARDDigest           string `json:"ardDigest,omitempty"`
	AICatalogDigest     string `json:"aiCatalogDigest,omitempty"`
}

// candidateEvidence binds generated discovery document digests, counts and
// execution attribution to candidate creation.
func (m *Manager) candidateEvidence(ctx context.Context, snapshot Snapshot) (MutationEvidence, error) {
	if m.evidence == nil {
		return MutationEvidence{}, nil
	}
	trigger := "direct"
	jobID, jobKind, initiator := "", "", ""
	jobAttempt := 0
	if job, ok := audit.JobExecutionFrom(ctx); ok {
		if !identity.ValidTenantID(job.ID) || job.Kind != "publication.generate" || job.Attempt < 1 || job.Trigger != "manual" {
			return MutationEvidence{}, catalog.E(catalog.CodeInternal, "publication candidate audit requires bounded Job execution attribution")
		}
		trigger = job.Trigger
		jobID, jobKind, jobAttempt = job.ID, job.Kind, job.Attempt
		if identity.ValidTenantID(job.Initiator) {
			initiator = job.Initiator
		}
	}
	facts := candidateFacts{Host: snapshot.Host, Audience: snapshot.Audience, Generation: snapshot.Generation, ARDProfile: snapshot.ARDProfile, AICatalogProfile: snapshot.AICatalogProfile, ARDDigest: snapshotARDDigest(snapshot), AICatalogDigest: snapshotAICatalogDigest(snapshot), EntryCount: len(snapshot.AICatalog.Entries), ExclusionCount: len(snapshot.Exclusions), Trigger: trigger, JobID: jobID, JobKind: jobKind, JobAttempt: jobAttempt, Initiator: initiator}
	record, err := m.auditRecord(ctx, snapshot.TenantID, audit.ActionPublicationCandidateCreated, snapshot.ID, "", facts, snapshot.CreatedAt)
	if err != nil {
		return MutationEvidence{}, err
	}
	record.AfterDigest, err = snapshotStateDigest(snapshot)
	if err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "digest publication candidate state", err)
	}
	if err := audit.ValidateNewRecord(record); err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "validate publication candidate evidence", err)
	}
	return MutationEvidence{Audit: record}, nil
}

// transitionEvidence records the activated snapshot and prior active identity
// for activation or rollback.
func (m *Manager) transitionEvidence(ctx context.Context, snapshot Snapshot, action, reason, replacedID string) (MutationEvidence, error) {
	if m.evidence == nil {
		return MutationEvidence{}, nil
	}
	if snapshot.ActivatedAt == nil || snapshot.State != SnapshotActive || action != audit.ActionPublicationActivated && action != audit.ActionPublicationRolledBack {
		return MutationEvidence{}, catalog.E(catalog.CodeInternal, "publication transition audit does not match an activated snapshot")
	}
	facts := transitionFacts{Host: snapshot.Host, Generation: snapshot.Generation, ReplacedSnapshotID: replacedID, ARDProfile: snapshot.ARDProfile, AICatalogProfile: snapshot.AICatalogProfile, ARDDigest: snapshotARDDigest(snapshot), AICatalogDigest: snapshotAICatalogDigest(snapshot)}
	record, err := m.auditRecord(ctx, snapshot.TenantID, action, snapshot.ID, reason, facts, *snapshot.ActivatedAt)
	if err != nil {
		return MutationEvidence{}, err
	}
	before := snapshot
	if action == audit.ActionPublicationActivated {
		before.State = SnapshotCandidate
		before.ActivatedAt = nil
	} else {
		before.State = SnapshotRetired
	}
	record.BeforeDigest, err = snapshotStateDigest(before)
	if err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "digest publication state before transition", err)
	}
	record.AfterDigest, err = snapshotStateDigest(snapshot)
	if err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "digest publication state after transition", err)
	}
	return m.publicEvidence(ctx, record, "publication.activated", snapshot, record.AfterDigest)
}

// withdrawalEvidence binds the originating lifecycle cause to snapshot
// retirement, optionally retaining document digests.
func (m *Manager) withdrawalEvidence(ctx context.Context, tenantID string, snapshot Snapshot, reason string, cause WithdrawalCause, includeDigests bool) (MutationEvidence, error) {
	if cause.Cause == "" || cause.OriginatingAction == "" || cause.OriginatingTargetID == "" {
		return MutationEvidence{}, catalog.E(catalog.CodeInternal, "publication withdrawal cause is incomplete")
	}
	if m.evidence == nil {
		return MutationEvidence{}, nil
	}
	// A safety withdrawal must remain possible when the retained snapshot is
	// structurally damaged. Route its evidence through the manager's trusted
	// tenant and host boundary, while the state digests below still describe
	// the exact snapshot that was retired.
	facts := withdrawalFacts{Host: m.host, Generation: snapshot.Generation, Cause: cause.Cause, OriginatingAction: cause.OriginatingAction, OriginatingTargetID: cause.OriginatingTargetID}
	if includeDigests {
		facts.ARDDigest = snapshotARDDigest(snapshot)
		facts.AICatalogDigest = snapshotAICatalogDigest(snapshot)
	}
	record, err := m.auditRecord(ctx, tenantID, audit.ActionPublicationUnpublished, snapshot.ID, reason, facts, m.clock.Now())
	if err != nil {
		return MutationEvidence{}, err
	}
	before := snapshot
	before.State = SnapshotActive
	record.BeforeDigest, err = snapshotStateDigest(before)
	if err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "digest publication state before withdrawal", err)
	}
	record.AfterDigest, err = snapshotStateDigest(snapshot)
	if err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "digest publication state after withdrawal", err)
	}
	return m.publicEvidence(ctx, record, "publication.unpublished", snapshot, record.AfterDigest)
}

// auditRecord constructs a publication audit record with the appropriate
// current membership or Job execution attribution.
func (m *Manager) auditRecord(ctx context.Context, tenantID, action, targetID, reason string, value any, at time.Time) (audit.Record, error) {
	facts, err := audit.Facts(value)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "construct publication audit facts", err)
	}
	idValue, err := m.ids.New(at)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "create publication audit identifier", err)
	}
	actor, hasActor := identity.FromContext(ctx)
	var principal audit.Principal
	execution := audit.ExecutionFrom(ctx)
	if _, worker := audit.JobExecutionFrom(ctx); worker {
		principal = audit.Principal{ID: audit.PrincipalIDCatalogWorker, Type: audit.PrincipalServiceExecutor}
		execution.AuthorizationDigest = ""
	} else {
		if !hasActor || actor.TenantID != tenantID {
			return audit.Record{}, catalog.E(catalog.CodeForbidden, "publication audit requires an authenticated tenant actor")
		}
		principal = audit.Principal{ID: actor.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(actor.ActorType), RealmID: actor.RealmID}
		if execution.AuthorizationDigest == "" {
			execution.AuthorizationDigest = actor.AuthorizationDigest()
		}
	}
	record, err := audit.NewRecord(idValue, tenantID, principal, action, "publication_snapshot", targetID, reason, execution, facts, at)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "construct publication audit record", err)
	}
	return record, nil
}

// publicEvidence pairs validated audit evidence with a host-bound public
// publication reference.
func (m *Manager) publicEvidence(ctx context.Context, record audit.Record, action string, snapshot Snapshot, digest string) (MutationEvidence, error) {
	if err := audit.ValidateNewRecord(record); err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "validate publication audit record", err)
	}
	outboxID, err := m.ids.New(record.At.Add(time.Nanosecond))
	if err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "create publication outbox identifier", err)
	}
	data, err := events.MarshalReferenceWithHost(action, "publication_snapshot", snapshot.ID, digest, m.host)
	if err != nil {
		return MutationEvidence{}, catalog.Wrap(catalog.CodeInternal, "construct publication notification", err)
	}
	outbox := catalog.OutboxEvent{CausalTrace: catalog.CausalTrace(ctx), ID: outboxID, TenantID: record.TenantID, Type: events.CanonicalType(action), Subject: snapshot.ID, Data: data, CreatedAt: record.At}
	if m.acknowledgeOutboxAtCreation {
		publishedAt := record.At
		outbox.PublishedAt = &publishedAt
	}
	return MutationEvidence{Audit: record, Outbox: &outbox}, nil
}

// snapshotStateDigest fingerprints the retained snapshot state used by
// lifecycle audit evidence.
func snapshotStateDigest(snapshot Snapshot) (string, error) {
	raw, err := json.Marshal(struct {
		ID               string        `json:"id"`
		Host             string        `json:"host"`
		Audience         Audience      `json:"audience"`
		Generation       int64         `json:"generation"`
		State            SnapshotState `json:"state"`
		ARDProfile       string        `json:"ardProfile"`
		AICatalogProfile string        `json:"aiCatalogProfile"`
		ARDDigest        string        `json:"ardDigest"`
		AICatalogDigest  string        `json:"aiCatalogDigest"`
	}{snapshot.ID, snapshot.Host, snapshot.Audience, snapshot.Generation, snapshot.State, snapshot.ARDProfile, snapshot.AICatalogProfile, snapshotARDDigest(snapshot), snapshotAICatalogDigest(snapshot)})
	if err != nil {
		return "", err
	}
	return audit.DigestBytes(raw), nil
}

// snapshotARDDigest returns the ARD document digest represented by its retained
// ETag.
func snapshotARDDigest(snapshot Snapshot) string {
	return etagDigest(snapshot.ETag)
}

// snapshotAICatalogDigest returns the AI Catalog document digest represented by
// its retained ETag.
func snapshotAICatalogDigest(snapshot Snapshot) string {
	return etagDigest(snapshot.AICatalogETag)
}

// etagDigest converts the retained ETag spelling into the audit digest
// representation.
func etagDigest(value string) string {
	value = strings.Trim(value, `"`)
	if len(value) == 64 {
		return "sha256:" + value
	}
	return ""
}
