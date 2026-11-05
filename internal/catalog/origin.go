package catalog

import (
	"context"
	"slices"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// OriginatingPrincipal is private authorization context from immutable creation
// evidence. It is never a resource or review response projection.
type OriginatingPrincipal struct {
	TenantID, MembershipID, RealmID, Subject string
}

// OriginatingActions restricts origin lookups to immutable creation transitions.
func OriginatingActions(kind string) []string {
	switch kind {
	case "resource":
		return []string{"resource.created", "observation.adopted"}
	case "review_submission":
		return []string{"review_submission.submitted"}
	default:
		return nil
	}
}

// SamePrincipal preserves membership attribution and issuer-realm subject
// independence; a different membership alone does not establish independence.
func (p OriginatingPrincipal) SamePrincipal(a identity.Actor) bool {
	return p.TenantID == a.TenantID && (p.MembershipID == a.MembershipID || p.RealmID == a.RealmID && p.Subject == a.Subject)
}

func (m *MemoryRepository) GetOriginatingPrincipal(ctx context.Context, tenant, kind, target string) (OriginatingPrincipal, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) (OriginatingPrincipal, error) {
			return m.GetOriginatingPrincipal(ctx, tenant, kind, target)
		})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.originatingPrincipalLocked(tenant, kind, target)
}

type originKey struct{ tenant, kind, target string }
type originEntry struct {
	principal OriginatingPrincipal
	ambiguous bool
}

func (m *MemoryRepository) originatingPrincipalLocked(tenant, kind, target string) (OriginatingPrincipal, error) {
	entry := m.origins[originKey{tenant, kind, target}]
	if entry.ambiguous || entry.principal.MembershipID == "" || entry.principal.Subject == "" {
		return OriginatingPrincipal{}, E(CodeForbidden, "originating principal cannot be established")
	}
	return entry.principal, nil
}

// Index exact immutable transitions as evidence commits; lookup never walks the
// retained audit collection. Duplicate origins remain ambiguous and fail closed.
func (m *MemoryRepository) indexOriginatingPrincipal(record AuditEvent) {
	if !slices.Contains(OriginatingActions(record.TargetType), record.Action) {
		return
	}
	if m.origins == nil {
		m.origins = make(map[originKey]originEntry)
	}
	key := originKey{record.TenantID, record.TargetType, record.TargetID}
	if entry, exists := m.origins[key]; exists {
		entry.ambiguous = true
		m.origins[key] = entry
		return
	}
	entry := originEntry{}
	realm, _ := record.Details["realmId"].(string)
	actorType, _ := record.Details["actorType"].(string)
	if record.FormatVersion != nil && *record.FormatVersion == audit.RecordFormatV1 && record.Details["principalType"] == audit.PrincipalCatalogMembership && identity.ValidTenantID(record.Actor) && identity.ValidTenantID(realm) && identity.ValidActorType(identity.ActorType(actorType)) {
		subject := ""
		if r := m.resources[record.TargetID]; record.TargetType == "resource" && r.TenantID == record.TenantID {
			subject = r.CreatedBy
		} else if r := m.submissions[record.TargetID]; record.TargetType == "review_submission" && r.TenantID == record.TenantID {
			subject = r.SubmittedBy
		}
		entry.principal = OriginatingPrincipal{TenantID: record.TenantID, MembershipID: record.Actor, RealmID: realm, Subject: subject}
	}
	m.origins[key] = entry
}
