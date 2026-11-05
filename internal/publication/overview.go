package publication

import (
	"context"
	"sort"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type HostConfiguration struct{ Host, Mode, ARDProfile, AICatalogProfile string }
type HostOverview struct {
	Host       string           `json:"host"`
	Mode       string           `json:"mode"`
	Status     string           `json:"status"`
	Active     *SnapshotSummary `json:"active,omitempty"`
	Mismatches []string         `json:"mismatches"`
}
type Overview struct {
	catalog.OverviewScope
	Hosts []HostOverview `json:"hosts"`
}
type ActiveSummaryReader interface {
	ReadActiveSummaries(context.Context, string, []string) ([]SnapshotSummary, error)
}

// ReadOverview reports retained metadata, never public-serving validation.
func ReadOverview(ctx context.Context, configs []HostConfiguration, reader ActiveSummaryReader, at time.Time) (Overview, error) {
	ctx = identity.WithPermissionContext(ctx)
	if _, err := identity.RequirePermission(ctx, "publication.read", ""); err != nil {
		return Overview{}, catalog.PermissionError("current publication permission is required", err)
	}
	ctx, actor, err := identity.WithCollectionContext(ctx)
	if err != nil {
		return Overview{}, catalog.PermissionError("publication summary admission failed", err)
	}
	if len(configs) > 64 {
		return Overview{}, catalog.E(catalog.CodeInvalidArgument, "publication host summary exceeds its bound")
	}
	out := Overview{OverviewScope: catalog.NewOverviewScope(actor.TenantID, at), Hosts: []HostOverview{}}
	out.SelectionLimit = 64
	hosts := []string{}
	for _, config := range configs {
		if config.Mode != "disabled" {
			hosts = append(hosts, config.Host)
		}
	}
	var active []SnapshotSummary
	if len(hosts) > 0 {
		if reader == nil {
			return Overview{}, catalog.E(catalog.CodeDependencyUnavailable, "publication summary is unavailable")
		}
		active, err = reader.ReadActiveSummaries(ctx, actor.TenantID, hosts)
		if err != nil {
			return Overview{}, err
		}
	}
	byHost := map[string]SnapshotSummary{}
	for _, row := range active {
		if row.TenantID == actor.TenantID && row.State == SnapshotActive {
			byHost[row.Host] = row
		}
	}
	for _, config := range configs {
		item := HostOverview{Host: config.Host, Mode: config.Mode, Status: "unpublished", Mismatches: []string{}}
		if config.Mode == "disabled" {
			item.Status = "disabled"
		} else if row, ok := byHost[config.Host]; ok {
			item.Status = "recorded"
			item.Active = &row
			if string(row.Audience) != config.Mode {
				item.Mismatches = append(item.Mismatches, "audience")
			}
			if row.ARDProfile != config.ARDProfile {
				item.Mismatches = append(item.Mismatches, "ard_profile")
			}
			if row.AICatalogProfile != config.AICatalogProfile {
				item.Mismatches = append(item.Mismatches, "ai_catalog_profile")
			}
		}
		out.Hosts = append(out.Hosts, item)
	}
	sort.Slice(out.Hosts, func(i, j int) bool { return out.Hosts[i].Host < out.Hosts[j].Host })
	return out, nil
}

// MemoryHostSummaries reads only configured stores and projects metadata under
// their locks. It does not load or validate snapshot documents.
type MemoryHostSummaries map[string]*MemoryStore

func (stores MemoryHostSummaries) ReadActiveSummaries(ctx context.Context, tenant string, hosts []string) ([]SnapshotSummary, error) {
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return nil, err
	}
	if identity.NeedsCollectionAdmission(ctx) {
		return identity.WithCollectionRead(ctx, func(bound context.Context) ([]SnapshotSummary, error) {
			return stores.ReadActiveSummaries(bound, tenant, hosts)
		})
	}
	allowed, err := identity.CollectionAllows(ctx, "publication.read", "")
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, catalog.E(catalog.CodeForbidden, "publication permission is required")
	}
	out := []SnapshotSummary{}
	for _, host := range hosts {
		store := stores[host]
		if store == nil {
			continue
		}
		store.mu.RLock()
		if id := store.active[tenant]; id != "" {
			if snapshot, ok := store.snapshots[memorySnapshotKey(tenant, id)]; ok && snapshot.TenantID == tenant && snapshot.Host == host {
				out = append(out, SummarizeSnapshot(snapshot))
			}
		}
		store.mu.RUnlock()
	}
	return out, nil
}
