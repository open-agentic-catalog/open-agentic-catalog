package publication

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

type overviewSummaryProbe struct {
	calls int
	hosts []string
	rows  []SnapshotSummary
	fail  bool
}

func (p *overviewSummaryProbe) ReadActiveSummaries(_ context.Context, _ string, hosts []string) ([]SnapshotSummary, error) {
	p.calls++
	p.hosts = append([]string(nil), hosts...)
	if p.fail {
		return nil, catalog.E(catalog.CodeDependencyUnavailable, "controlled metadata failure")
	}
	return p.rows, nil
}

func TestPublicationOverviewSeparatesConfigurationAndStoredMetadata(t *testing.T) {
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant-1", Subject: "publisher", Roles: []identity.Role{identity.RolePublisher}})
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	reader := &overviewSummaryProbe{rows: []SnapshotSummary{{ID: "active", TenantID: "tenant-1", Host: "one.example", Audience: AudienceAuthenticated, State: SnapshotActive, Generation: 3, ARDProfile: "old", AICatalogProfile: "ai", ActivatedAt: &at}, {TenantID: "other", Host: "two.example", State: SnapshotActive}}}
	configs := []HostConfiguration{{Host: "disabled.example", Mode: "disabled"}, {Host: "one.example", Mode: "anonymous", ARDProfile: "ard", AICatalogProfile: "ai"}, {Host: "two.example", Mode: "authenticated", ARDProfile: "ard", AICatalogProfile: "ai"}}
	out, err := ReadOverview(ctx, configs, reader, at)
	if err != nil || reader.calls != 1 || !reflect.DeepEqual(reader.hosts, []string{"one.example", "two.example"}) || len(out.Hosts) != 3 || out.Hosts[0].Status != "disabled" || out.Hosts[0].Active != nil || out.Hosts[1].Status != "recorded" || !reflect.DeepEqual(out.Hosts[1].Mismatches, []string{"audience", "ard_profile"}) || out.Hosts[2].Status != "unpublished" {
		t.Fatal(out, reader, err)
	}
	reader.fail = true
	if _, err = ReadOverview(ctx, configs, reader, at); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatal(err)
	}
	denied := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant-1", Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	before := reader.calls
	if _, err = ReadOverview(denied, configs, reader, at); catalog.ErrorCodeOf(err) != catalog.CodeForbidden || reader.calls != before {
		t.Fatal("denied metadata read reached the store", err)
	}
	if out, err = ReadOverview(ctx, configs[:1], nil, at); err != nil || len(out.Hosts) != 1 {
		t.Fatal(out, err)
	}
	if _, err = ReadOverview(ctx, make([]HostConfiguration, 65), reader, at); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal(err)
	}
}

func TestMemoryPublicationOverviewReadsOnlyActiveMetadata(t *testing.T) {
	store := NewMemoryStore()
	stamp := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	store.snapshots[memorySnapshotKey("tenant", "id")] = Snapshot{ID: "id", TenantID: "tenant", Host: "one.example", State: SnapshotActive, Generation: 7, ActivatedAt: &stamp}
	store.active["tenant"] = "id"
	reader := MemoryHostSummaries{"one.example": store, "two.example": store}
	tenantContext := func(tenant string) context.Context {
		bound, _, err := identity.WithCollectionContext(identityfixture.WithActor(t.Context(), identity.Actor{TenantID: tenant, Subject: "metadata-reader", Roles: []identity.Role{identity.RoleTenantAdmin}}))
		if err != nil {
			t.Fatal(err)
		}
		return bound
	}
	ctx := tenantContext("tenant")
	rows, err := reader.ReadActiveSummaries(ctx, "tenant", []string{"one.example", "two.example", "missing.example"})
	if err != nil || len(rows) != 1 || rows[0].Generation != 7 {
		t.Fatal(rows, err)
	}
	rows, err = reader.ReadActiveSummaries(tenantContext("other"), "other", []string{"one.example"})
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	delete(store.active, "tenant")
	rows, err = reader.ReadActiveSummaries(ctx, "tenant", []string{"one.example"})
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}

func TestPublicationOverviewMaximumHostSetUsesOneMetadataRead(t *testing.T) {
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "publisher", Roles: []identity.Role{identity.RolePublisher}})
	reader := &overviewSummaryProbe{}
	configs := make([]HostConfiguration, 64)
	for i := range configs {
		configs[i] = HostConfiguration{Host: fmt.Sprintf("host-%02d.example", i), Mode: "anonymous", ARDProfile: "ard", AICatalogProfile: "ai"}
	}
	result, err := ReadOverview(ctx, configs, reader, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	if err != nil || reader.calls != 1 || len(reader.hosts) != 64 || len(result.Hosts) != 64 {
		t.Fatal(result, reader, err)
	}
	for _, host := range result.Hosts {
		if host.Status != "unpublished" || host.Active != nil {
			t.Fatal(host)
		}
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > 32768 {
		t.Fatal("unbounded host summary", len(raw), err)
	}
}
