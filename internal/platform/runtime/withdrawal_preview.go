package runtime

import (
	"context"
	"sort"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// PreviewPublicationRelease follows exactly the tenant-owned configured hosts
// used for withdrawal, with bounded selection before reading any bundle.
func (p *publicationWithdrawalCoordinator) PreviewPublicationRelease(ctx context.Context, tenantID, releaseID string) ([]catalog.ReleaseWithdrawalSnapshot, error) {
	hosts := make([]string, 0, min(len(p.managers), publication.MaxWithdrawalPreviewHosts))
	for host, binding := range p.managers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if binding.tenantID != tenantID {
			continue
		}
		if len(hosts) == publication.MaxWithdrawalPreviewHosts {
			return nil, catalog.E(catalog.CodeWorkLimitExceeded, "withdrawal preview exceeds its configured-host limit")
		}
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	budget := &publication.WithdrawalPreviewBudget{}
	affected := []catalog.ReleaseWithdrawalSnapshot{}
	for _, host := range hosts {
		snapshot, matched, err := p.managers[host].manager.PreviewReleaseWithdrawal(ctx, tenantID, releaseID, budget)
		if err != nil {
			return nil, err
		}
		if matched {
			affected = append(affected, snapshot)
		}
	}
	return affected, nil
}
