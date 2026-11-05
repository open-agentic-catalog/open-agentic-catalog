package identity

import (
	"context"
	"maps"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
)

// AdministrationRecord validates private audit-only attribution and exact
// mutation binding. Permission/group changes have no selected public consumer.
func AdministrationRecord(change AdministrationChange, action, target string, facts map[string]any) (audit.Record, error) {
	e := change.Evidence
	if e.Action != action || e.TargetType != target || e.TargetID != change.ID || e.Actor != change.Actor.MembershipID || !e.At.Equal(change.At) || e.OutboxID != "" || e.OutboxType != "" || e.OutboxSubject != "" || e.OutboxPublished != nil || len(e.OutboxData) != 0 || e.Details["realmId"] != change.Actor.RealmID || e.Details["actorType"] != string(change.Actor.ActorType) {
		return audit.Record{}, E(CodeInternal, "authorization audit does not match the admitted mutation")
	}
	details := maps.Clone(e.Details)
	if details == nil {
		details = map[string]any{}
	}
	details["facts"] = facts
	record := audit.Record{FormatVersion: e.FormatVersion, ID: e.AuditID, TenantID: change.Actor.TenantID, Actor: e.Actor, Action: action, TargetType: target, TargetID: change.ID, Reason: e.Reason, RequestID: e.RequestID, Details: details, At: e.At}
	if err := audit.ValidateNewRecord(record); err != nil {
		return audit.Record{}, Wrap(CodeInternal, "authorization audit is invalid", err)
	}
	return record, nil
}

func (s *Service) administrationChange(ctx context.Context, id string, revision int64, reason, action, target, requestID string) (AdministrationChange, error) {
	if id != "" && (!ValidTenantID(id) || revision < 1) || id == "" && revision != 0 {
		return AdministrationChange{}, E(CodeInvalidArgument, "existing objects require a UUID and current revision")
	}
	actor, _, err := s.currentAuthorization(ctx, true)
	if err != nil {
		return AdministrationChange{}, err
	}
	reason, err = normalizeAuditReason(action, reason, "a bounded non-secret administration reason is required")
	if err != nil {
		return AdministrationChange{}, err
	}
	at := s.clock.Now().UTC().Truncate(time.Microsecond)
	if id == "" {
		id, err = s.ids.New(at)
		if err != nil {
			return AdministrationChange{}, Wrap(CodeInternal, "create authorization identifier", err)
		}
	}
	auditID, err := s.ids.New(at)
	if err != nil {
		return AdministrationChange{}, Wrap(CodeInternal, "create authorization audit identifier", err)
	}
	execution := audit.ExecutionFrom(ctx)
	if execution.RequestID == "" {
		execution.RequestID = requestID
	}
	if execution.AuthorizationDigest == "" {
		execution.AuthorizationDigest = DecisionDigest(ctx)
	}
	record, err := audit.NewRecord(auditID, actor.TenantID, audit.Principal{ID: actor.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(actor.ActorType), RealmID: actor.RealmID}, action, target, id, reason, execution, map[string]any{}, at)
	if err != nil {
		return AdministrationChange{}, Wrap(CodeInternal, "construct authorization audit", err)
	}
	return AdministrationChange{Actor: actor, ID: id, ExpectedRevision: revision, At: record.At, Evidence: ChangeEvidence{FormatVersion: record.FormatVersion, AuditID: record.ID, Actor: record.Actor, Action: record.Action, TargetType: record.TargetType, TargetID: record.TargetID, Reason: record.Reason, RequestID: record.RequestID, Details: record.Details, At: record.At}}, nil
}
