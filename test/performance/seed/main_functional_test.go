//go:build functional

// Functional tests compose Catalog, governance and synchronization workflows.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	fixture "github.com/open-agentic-catalog/open-agentic-catalog/test/performance/scalefixture"
)

func TestIndexedFixtureIsSearchable(t *testing.T) {
	repo := catalog.NewMemoryRepository()
	store := federation.NewMemoryStore(repo)
	entries := make([]map[string]any, 97)
	for i := range entries {
		entries[i] = fixture.Entry(i)
	}
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	m, err := federation.NewManager(store, retrieval{body: body}, clock.Real{}, id.Generator{}, federation.WithEvidenceSink(repo, true))
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: fixture.Tenant, Subject: "author", ActorType: identity.ActorHuman, RealmID: "00000000-0000-7000-8000-000000000010", MembershipID: "00000000-0000-7000-8000-000000000011", Roles: []identity.Role{identity.RoleSourceAdmin, identity.RoleReader}})
	inspection, err := m.Inspect(ctx, "https://scale.example/.well-known/ard.json")
	if err != nil {
		t.Fatal(err)
	}
	source, err := m.Connect(ctx, federation.ConnectInput{InspectionID: inspection.ID, DisplayName: "Scale bulk source", Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin}}, RetrievalMode: federation.RetrievalIndexed, UpdateMode: federation.UpdateManual, Reason: "Synthetic test"})
	if err != nil {
		t.Fatal(err)
	}
	jobStore := jobs.NewMemoryStore(repo)
	if _, err := syncFixture(ctx, jobStore, jobStore, source, m.SyncAtRevision); err != nil {
		t.Fatal(err)
	}
	page, err := m.SearchFederatedCatalog(ctx, catalog.FederatedCatalogSearchRequest{Limit: 100})
	if err != nil || page.IndexedTotal != len(entries) {
		t.Fatalf("indexed=%d want=%d error=%v", page.IndexedTotal, len(entries), err)
	}
}

func TestSeedSynchronizationUsesRealAuditedJob(t *testing.T) {
	for _, fail := range []bool{false, true} {
		repo := catalog.NewMemoryRepository()
		store := jobs.NewMemoryStore(repo)
		ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "author", ActorType: identity.ActorHuman, RealmID: "00000000-0000-7000-8000-000000000010", MembershipID: "00000000-0000-7000-8000-000000000011", Roles: []identity.Role{identity.RoleSourceAdmin, identity.RoleReader}})
		source := federation.Source{ID: "00000000-0000-7000-8000-000000000015", Revision: 1}
		called := false
		_, err := syncFixture(ctx, store, store, source, func(call context.Context, sourceID string, revision int64) (federation.Snapshot, error) {
			called = true
			execution, ok := audit.JobExecutionFrom(call)
			if !ok || execution.Trigger != "manual" || execution.Attempt != 1 || sourceID != source.ID || revision != 1 {
				t.Fatalf("invalid execution: %+v", execution)
			}
			job, getErr := store.Get(call, "00000000-0000-7000-8000-000000000001", execution.ID)
			if getErr != nil || job.State != jobs.StateRunning || job.PayloadVersion != 2 {
				t.Fatalf("not a running real Job: %+v, %v", job, getErr)
			}
			if fail {
				return federation.Snapshot{}, errors.New("synthetic sync failure")
			}
			return federation.Snapshot{Added: 97}, nil
		})
		if !called || (err != nil) != fail {
			t.Fatalf("called=%v error=%v", called, err)
		}
	}
}

func TestGovernedSeedRecords(t *testing.T) {
	key := sha256.Sum256([]byte(t.Name()))
	repo := catalog.NewMemoryRepository()
	s, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), ExternalFetcher: retrieval{}, CursorKey: key[:], Policy: "default-v1", AcknowledgeOutboxAtCreation: true})
	if err != nil {
		t.Fatal(err)
	}
	author := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "author", ActorType: identity.ActorHuman, RealmID: "00000000-0000-7000-8000-000000000010", MembershipID: "00000000-0000-7000-8000-000000000011", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RolePublisher, identity.RoleReader}})
	reviewer := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "reviewer", ActorType: identity.ActorHuman, RealmID: "00000000-0000-7000-8000-000000000010", MembershipID: "00000000-0000-7000-8000-000000000012", Roles: []identity.Role{identity.RoleReviewer}})
	ns, err := s.CreateNamespace(author, catalog.CreateNamespaceInput{Slug: "scale", DisplayName: "Scale"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := s.CreatePublisher(author, catalog.CreatePublisherInput{DisplayName: "Scale publisher", Domains: []string{"scale.example"}})
	if err != nil {
		t.Fatal(err)
	}
	indices := []int{0, 1, 2, 3, 7, 11, 50, 100}
	progress := seedProgress{}
	err = seedPairs(t.Context(), len(indices), &progress, func(_ context.Context, index int, p *seedProgress) (preparedRecord, error) {
		return prepareSeedRecord(author, reviewer, s, ns.ID, publisher.ID, indices[index], p, catalog.VisibilityTenant)
	}, func(record preparedRecord, p *seedProgress) error {
		return publishSeedRecord(author, s, record, p)
	})
	if err != nil || progress.CompletedLocalRecords != len(indices) {
		t.Fatalf("paired governed records=%d error=%v", progress.CompletedLocalRecords, err)
	}
	page, err := s.Search(author, catalog.SearchQuery{Text: "Scale", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 8 {
		t.Fatalf("searchable records: %d", page.Total)
	}
	publisher, err = s.ApprovePublisherPublicationDomain(author, publisher.ID, "scale.example", "Approve owned fixture publication", publisher.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedRecord(author, reviewer, s, ns.ID, publisher.ID, 4001, &seedProgress{}, catalog.VisibilityPublic); err != nil {
		t.Fatal(err)
	}
	public, err := s.Search(author, catalog.SearchQuery{Text: "Scale", Visibility: catalog.VisibilityPublic, Limit: 20})
	if err != nil || public.Total != 1 {
		t.Fatalf("public seed must retain the ordinary governance boundary: %+v %v", public, err)
	}
	manager, err := publication.NewManager(publication.Options{Records: s, Store: publication.NewMemoryStore(), Clock: clock.Real{}, IDs: id.Generator{}, BaseURL: "https://scale.example", PublisherDomain: "scale.example", ARDProfile: "ard-test", AICatalogProfile: "ai-catalog-test", CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.BuildCandidate(author, fixture.Tenant)
	if err != nil || len(snapshot.ARD.Entries) != 2 || len(snapshot.Exclusions) != 0 {
		t.Fatalf("the public MCP seed must reach the manifest without exclusions: %+v %v", snapshot.Exclusions, err)
	}
}

// seedRecord preserves the complete single-record workflow for focused tests.
func seedRecord(author, reviewer context.Context, s *catalog.Service, namespace, publisher string, i int, progress *seedProgress, visibility catalog.Visibility) error {
	record, err := prepareSeedRecord(author, reviewer, s, namespace, publisher, i, progress, visibility)
	if err != nil {
		return err
	}
	return publishSeedRecord(author, s, record, progress)
}
