package catalog

import (
	"context"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func (m *MemoryRepository) ReadPublisherDomainVerification(ctx context.Context, tenant, publisher, domain string) (Publisher, PublisherDomainVerification, error) {
	if identity.NeedsReadAdmission(ctx) {
		type pair struct {
			p Publisher
			v PublisherDomainVerification
		}
		result, err := identity.WithReadAdmission(ctx, func(call context.Context) (pair, error) {
			p, v, err := m.ReadPublisherDomainVerification(call, tenant, publisher, domain)
			return pair{p, v}, err
		})
		return result.p, result.v, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.publishers[publisher]
	if !ok || p.TenantID != tenant {
		return Publisher{}, PublisherDomainVerification{}, E(CodeNotFound, "publisher was not found")
	}
	if !contains(p.Domains, domain) {
		return Publisher{}, PublisherDomainVerification{}, E(CodeNotFound, "publisher domain was not found")
	}
	v, ok := m.publisherDomains[tenant+"/"+publisher+"/"+domain]
	if !ok {
		v = PublisherDomainVerification{TenantID: tenant, PublisherID: publisher, Domain: domain}
	}
	return clone(p), v, nil
}

func (m *MemoryRepository) SavePublisherDomainVerification(ctx context.Context, params PublisherDomainMutation) (PublisherDomainVerification, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var result PublisherDomainVerification
		err := identity.WithCurrentPermissionAdmission(ctx, func(call context.Context) error {
			var err error
			result, err = m.SavePublisherDomainVerification(call, params)
			return err
		})
		return result, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v := params.State
	p, ok := m.publishers[v.PublisherID]
	if !ok || p.TenantID != v.TenantID || !contains(p.Domains, v.Domain) {
		return PublisherDomainVerification{}, E(CodeNotFound, "publisher domain was not found")
	}
	current := m.publisherDomains[v.TenantID+"/"+v.PublisherID+"/"+v.Domain]
	if err := CheckPublisherDomainVersion(p, current, params.Expected); err != nil {
		return PublisherDomainVerification{}, err
	}
	if err := CheckPublisherDomainCompletion(params, current); err != nil {
		return PublisherDomainVerification{}, err
	}
	if err := CheckPublisherDomainTransition(current, v); err != nil {
		return PublisherDomainVerification{}, err
	}
	if err := audit.ValidateNewRecord(params.Audit); err != nil {
		return PublisherDomainVerification{}, Wrap(CodeInternal, "validate publisher verification audit", err)
	}
	if params.Audit.TenantID != v.TenantID || params.Audit.TargetType != "publisher" || params.Audit.TargetID != v.PublisherID || !PublisherVerificationAction(params.Audit.Action) {
		return PublisherDomainVerification{}, E(CodeInternal, "publisher verification audit binding is invalid")
	}
	m.publisherDomains[v.TenantID+"/"+v.PublisherID+"/"+v.Domain] = v
	m.audit = append(m.audit, currentMemoryAudit(ctx, params.Audit))
	return v, nil
}
