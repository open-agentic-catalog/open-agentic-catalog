// Webhook evidence binds endpoint and delivery facts to their exact tenant,
// target, transition time and attributed action.

package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// EndpointChangeAction classifies an endpoint mutation, prioritizing status
// changes over secret rotation and ordinary metadata updates.
func EndpointChangeAction(before, after Endpoint) string {
	if after.Status != before.Status {
		return audit.ActionWebhookDisabled
	}
	if after.SecretVersion != before.SecretVersion {
		return audit.ActionWebhookSecretRotated
	}
	return audit.ActionWebhookUpdated
}

// ValidateEndpointEvidence checks that optional fixture or required runtime
// evidence exactly matches endpoint revision, secret version and status.
func ValidateEndpointEvidence(records []audit.Record, endpoint Endpoint, action string) error {
	if err := ValidateEvidence(records, endpoint.TenantID, "webhook_endpoint", endpoint.ID, endpoint.UpdatedAt, action); err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	facts := map[string]any{"revision": endpoint.Revision, "secretVersion": endpoint.SecretVersion, "status": string(endpoint.Status)}
	if action == audit.ActionWebhookSecretRotated {
		actual, ok := records[0].Details["facts"].(map[string]any)
		if !ok {
			return catalog.E(catalog.CodeInternal, "webhook audit facts are unavailable")
		}
		facts["revokePrevious"] = actual["revokePrevious"]
	}
	return matchWebhookFacts(records[0], facts)
}

// ValidateDeliveryEvidence binds supplied audit evidence to the exact delivery
// attempt and retained HTTP status.
func ValidateDeliveryEvidence(records []audit.Record, delivery Delivery) error {
	if err := ValidateEvidence(records, delivery.TenantID, "webhook_delivery", delivery.ID, delivery.UpdatedAt, audit.ActionWebhookDeliveryAttempted); err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	return matchWebhookFacts(records[0], map[string]any{"endpointId": delivery.EndpointID, "eventId": delivery.EventID, "attempt": delivery.Attempt, "state": string(delivery.State), "httpStatus": delivery.ResponseStatus})
}

// matchWebhookFacts compares deterministic JSON facts against the state being
// committed.
func matchWebhookFacts(record audit.Record, expected map[string]any) error {
	actual, err := json.Marshal(record.Details["facts"])
	if err != nil {
		return catalog.Wrap(catalog.CodeInternal, "encode webhook audit facts", err)
	}
	want, err := json.Marshal(expected)
	if err != nil || !bytes.Equal(actual, want) {
		return catalog.E(catalog.CodeInternal, "webhook audit facts do not match committed state")
	}
	return nil
}

// PrepareDeliveryReadEvidence validates a single access-evidence builder and
// binds its digest to the returned page, excluding the continuation sentinel.
func PrepareDeliveryReadEvidence(tenantID, endpointID string, items []Delivery, selectionLimit int, builders []DeliveryReadEvidence) ([]audit.Record, error) {
	if len(builders) == 0 {
		return nil, nil
	}
	if len(builders) != 1 || builders[0] == nil || selectionLimit < 2 || selectionLimit > 101 || len(items) > selectionLimit {
		return nil, catalog.E(catalog.CodeInternal, "webhook delivery access evidence is ambiguous")
	}
	record, err := builders[0](items)
	if err != nil {
		return nil, err
	}
	records := []audit.Record{record}
	if err := ValidateEvidence(records, tenantID, "webhook_endpoint", endpointID, time.Time{}, audit.ActionWebhookDeliveriesRead); err != nil {
		return nil, err
	}
	prepared := items[:min(len(items), selectionLimit-1)]
	raw, err := json.Marshal(prepared)
	if err != nil {
		return nil, catalog.Wrap(catalog.CodeInternal, "encode webhook delivery access evidence", err)
	}
	if err := matchWebhookFacts(record, map[string]any{"deliveryCount": len(prepared), "deliveriesDigest": audit.DigestBytes(raw)}); err != nil {
		return nil, err
	}
	return records, nil
}

// appendAudit requires an evidence sink whenever a mutation supplies an audit
// record.
func (m *MemoryStore) appendAudit(ctx context.Context, records []audit.Record) error {
	if len(records) == 0 {
		return nil
	}
	if m.evidence == nil {
		return catalog.E(catalog.CodeInternal, "webhook audit evidence sink is unavailable")
	}
	return m.evidence.AppendAuditEvent(ctx, records[0])
}

type EvidenceSink interface {
	AppendAuditEvent(context.Context, audit.Record) error
}
type DeliveryReadEvidence func([]Delivery) (audit.Record, error)

// webhookAudit constructs validated attribution for a Catalog membership or
// worker executor without retaining receiver or secret data.
func webhookAudit(ctx context.Context, ids IDGenerator, tenantID, action, targetType, targetID string, facts map[string]any, at time.Time, worker bool) (audit.Record, error) {
	principal := audit.Principal{ID: audit.PrincipalIDCatalogWorker, Type: audit.PrincipalServiceExecutor}
	execution := audit.ExecutionFrom(ctx)
	if worker {
		execution.AuthorizationDigest = ""
	} else {
		actor, ok := identity.FromContext(ctx)
		if !ok || actor.TenantID != tenantID {
			return audit.Record{}, catalog.E(catalog.CodeInternal, "webhook audit attribution is unavailable")
		}
		principal = audit.Principal{ID: actor.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(actor.ActorType), RealmID: actor.RealmID}
		if execution.AuthorizationDigest == "" || len(identity.RecordedPermissionChecks(ctx)) > 0 {
			execution.AuthorizationDigest = identity.DecisionDigest(ctx)
		}
	}
	id, err := ids.New(at)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "create webhook audit identifier", err)
	}
	record, err := audit.NewRecord(id, tenantID, principal, action, targetType, targetID, "", execution, facts, at)
	if err != nil {
		return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "construct webhook audit evidence", err)
	}
	return record, nil
}

// endpointAudit records the endpoint revision and secret version, including
// predecessor revocation for rotation.
func (s *Service) endpointAudit(ctx context.Context, endpoint Endpoint, action string, revoke bool) (audit.Record, error) {
	facts := map[string]any{"revision": endpoint.Revision, "secretVersion": endpoint.SecretVersion, "status": string(endpoint.Status)}
	if action == audit.ActionWebhookSecretRotated {
		facts["revokePrevious"] = revoke
	}
	return webhookAudit(ctx, s.ids, endpoint.TenantID, action, "webhook_endpoint", endpoint.ID, facts, endpoint.UpdatedAt, false)
}

// deliveryAudit records fixed delivery attempt facts with either membership or
// worker attribution.
func deliveryAudit(ctx context.Context, ids IDGenerator, delivery Delivery, worker bool) (audit.Record, error) {
	return webhookAudit(ctx, ids, delivery.TenantID, audit.ActionWebhookDeliveryAttempted, "webhook_delivery", delivery.ID, map[string]any{"endpointId": delivery.EndpointID, "eventId": delivery.EventID, "attempt": delivery.Attempt, "state": string(delivery.State), "httpStatus": delivery.ResponseStatus}, delivery.UpdatedAt, worker)
}

// deliveryReadAudit builds access evidence over the visible page rather than
// its continuation sentinel.
func (s *Service) deliveryReadAudit(ctx context.Context, tenantID, endpointID string, limit int) DeliveryReadEvidence {
	return func(items []Delivery) (audit.Record, error) {
		prepared := items[:min(len(items), limit)]
		raw, err := json.Marshal(prepared)
		if err != nil {
			return audit.Record{}, catalog.Wrap(catalog.CodeInternal, "encode webhook delivery evidence", err)
		}
		return webhookAudit(ctx, s.ids, tenantID, audit.ActionWebhookDeliveriesRead, "webhook_endpoint", endpointID, map[string]any{"deliveryCount": len(prepared), "deliveriesDigest": audit.DigestBytes(raw)}, s.clock.Now(), false)
	}
}

// ValidateEvidence binds audit-only evidence to the exact committed operation.
// An omitted record is reserved for direct repository fixtures; runtime domain
// entry points always construct one, and cannot supply an outbox notification.
func ValidateEvidence(records []audit.Record, tenantID, targetType, targetID string, at time.Time, actions ...string) error {
	if len(records) == 0 {
		return nil
	}
	if len(records) != 1 {
		return catalog.E(catalog.CodeInternal, "webhook audit evidence is ambiguous")
	}
	record := records[0]
	if record.FormatVersion == nil || record.TenantID != tenantID || record.TargetType != targetType || record.TargetID != targetID || !slices.Contains(actions, record.Action) || (!at.IsZero() && !record.At.Equal(at.UTC().Truncate(time.Microsecond))) {
		return catalog.E(catalog.CodeInternal, "webhook audit evidence does not match its operation")
	}
	if err := audit.ValidateNewRecord(record); err != nil {
		return catalog.Wrap(catalog.CodeInternal, "webhook audit evidence is invalid", err)
	}
	return nil
}
