package federation

import (
	"context"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"strings"
	"testing"
	"time"
)

type failedOverviewStore struct{ *MemoryStore }

func (failedOverviewStore) CountIndexedInventory(context.Context, string) (*int, bool, error) {
	return nil, false, catalog.E(catalog.CodeDependencyUnavailable, "controlled indexed failure")
}

func TestIndexedOverviewPreservesReadFailures(t *testing.T) {
	manager, store, _, _, _, _ := newChangePreviewTest(t)
	manager.store = failedOverviewStore{store}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: auditEvidenceTenantID, Subject: "overview-admin", Roles: []identity.Role{identity.RoleTenantAdmin}})
	value, err := manager.IndexedOverview(ctx)
	if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || value.Count != nil {
		t.Fatal(value, err)
	}
}

func TestIndexedOverviewDeduplicatesEligibleIdentitiesWithoutOutboundWork(t *testing.T) {
	store := NewMemoryStore()
	outbound := &routingFetcher{results: nil}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewMemoryStore([]identity.Seed{{TenantID: catalogSearchTestTenantID, Issuer: "overview-unit", Subject: "discovery-reader", ActorType: identity.ActorHuman, RoleTemplates: []string{"discovery_reader"}}, {TenantID: catalogSearchTestTenantID, Issuer: "overview-unit", Subject: "reader", ActorType: identity.ActorHuman, RoleTemplates: []string{"reader"}}})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := authority.ResolvePrincipal(t.Context(), identity.Principal{TenantID: catalogSearchTestTenantID, Issuer: "overview-unit", Subject: "discovery-reader"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), actor)
	read := func(want int) {
		t.Helper()
		value, err := manager.IndexedOverview(ctx)
		if err != nil || value.Limited || value.Count == nil || *value.Count != want || !value.ObservedAt.Equal(now) || len(outbound.requests) != 0 {
			t.Fatal(value, err, outbound.requests)
		}
	}
	read(0)
	identifier := "urn:air:remote.example:default:alpha"
	for i := 0; i < 2; i++ {
		key := fmt.Sprintf("source-%d", i)
		store.sources[key] = Source{ID: key, TenantID: catalogSearchTestTenantID, Status: SourceActive, RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}}
		observation := indexedSearchObservation(key, identifier, "Alpha", "sha256:"+strings.Repeat(fmt.Sprint(i), 64))
		store.observations[key] = observation
	}
	read(1) // Conflicting digest and another source still identify one record.
	absent := store.observations["source-0"]
	absent.Version = ""
	absent.VersionPresent = false
	store.observations["absent"] = absent
	explicit := absent
	explicit.VersionPresent = true
	store.observations["empty"] = explicit
	read(3)
	source := store.sources["source-0"]
	source.Status = SourcePaused
	store.sources[source.ID] = source
	read(1)
	source.Status = SourceError
	store.sources[source.ID] = source
	read(3)
	source.Scope = Scope{Kinds: []catalog.Kind{catalog.KindSkill}}
	store.sources[source.ID] = source
	read(1)
	delete(store.sources, "source-1")
	read(0)
	source.Scope = Scope{Kinds: []catalog.Kind{catalog.KindAgent}}
	store.sources[source.ID] = source
	read(3)
	source.TenantID = "another-tenant"
	store.sources[source.ID] = source
	read(0)
	reader, err := authority.ResolvePrincipal(t.Context(), identity.Principal{TenantID: catalogSearchTestTenantID, Issuer: "overview-unit", Subject: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	denied := identity.WithActor(t.Context(), reader)
	if _, err = manager.IndexedOverview(denied); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("exact Reader acquired federation permission", err)
	}
	if _, err = manager.IndexedOverview(context.Background()); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal(err)
	}
}

func TestIndexedOverviewContributionCeilingDoesNotInventIdentityLowerBound(t *testing.T) {
	store := NewMemoryStore()
	store.sources["source"] = Source{ID: "source", TenantID: catalogSearchTestTenantID, Status: SourceActive, RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}}
	for i := 0; i < catalog.OverviewSelectionLimit; i++ {
		store.observations[fmt.Sprint(i)] = indexedSearchObservation("source", "urn:air:remote.example:default:alpha", "Alpha", "digest")
	}
	count, limited, err := store.CountIndexedInventory(t.Context(), catalogSearchTestTenantID)
	if err != nil || limited || count == nil || *count != 1 {
		t.Fatal(count, limited, err)
	}
	store.observations["overflow"] = store.observations["0"]
	count, limited, err = store.CountIndexedInventory(t.Context(), catalogSearchTestTenantID)
	if err != nil || !limited || count != nil {
		t.Fatal("contribution ceiling became an identity lower bound", count, limited, err)
	}
}
