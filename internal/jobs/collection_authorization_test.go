package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestJobCollectionFiltersFamiliesAndTargetsBeforePaging(t *testing.T) {
	store := NewMemoryStore()
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for index, kind := range []string{"publication.generate", "unknown", "source.sync", "semantic.reindex", "source.sync", "source.sync"} {
		key := fmt.Sprintf("00000000-0000-7000-8000-%012d", 700+index)
		store.jobs[key] = Job{ID: key, TenantID: "tenant", Kind: kind, CreatedAt: at.Add(-time.Duration(index) * time.Minute)}
	}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "source-reader", Roles: []identity.Role{identity.RoleSourceAdmin}})
	checked := []string{}
	service := &Service{store: store, targetValidation: func(ctx context.Context, job Job) error {
		checked = append(checked, job.ID)
		// Looking up the Job here proves validation does not retain its mutex.
		if _, err := store.Get(ctx, job.TenantID, job.ID); err != nil {
			return err
		}
		if job.ID == "00000000-0000-7000-8000-000000000702" {
			return catalog.E(catalog.CodeNotFound, "removed source")
		}
		return nil
	}}
	page, err := service.List(ctx, ListRequest{Limit: 1})
	if err != nil || len(page) != 2 || page[0].ID != "00000000-0000-7000-8000-000000000704" || page[1].ID != "00000000-0000-7000-8000-000000000705" {
		t.Fatal("filtered page lost sentinel", page, err)
	}
	if len(checked) != 3 {
		t.Fatal("unreadable family inspected", checked)
	}
	page, err = service.List(ctx, ListRequest{Limit: 1, AfterCreatedAt: &page[0].CreatedAt, AfterID: page[0].ID})
	if err != nil || len(page) != 1 || page[0].ID != "00000000-0000-7000-8000-000000000705" {
		t.Fatal("filtered continuation", page, err)
	}
	noFamily := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	page, err = service.List(noFamily, ListRequest{Limit: 10})
	if err != nil || len(page) != 0 {
		t.Fatal("generic Job read disclosed families", page, err)
	}
	failure := catalog.E(catalog.CodeDependencyUnavailable, "target store unavailable")
	service.targetValidation = func(context.Context, Job) error { return failure }
	if page, err := service.List(ctx, ListRequest{Limit: 1}); !errors.Is(err, failure) || page != nil {
		t.Fatal("dependency failure silently omitted targets", page, err)
	}
}

func TestListTargetOptionIsExplicit(t *testing.T) {
	service := &Service{}
	if err := WithListTargets(nil)(service); err == nil {
		t.Fatal("nil target selector accepted")
	}
	policy := ListTargets{Enforced: true, PublicationHosts: []string{"catalog.example.com"}, SemanticProfile: "profile"}
	if err := WithListTargets(func(string) ListTargets { return policy })(service); err != nil {
		t.Fatal(err)
	}
	if !service.listTargets("tenant").Enforced {
		t.Fatal("deployment target enforcement missing")
	}
}

type collectionRequestStore struct {
	Store
	request ListRequest
}

func (s *collectionRequestStore) ListViews(ctx context.Context, _ string, request ListRequest) ([]View, error) {
	if _, selected := identity.CollectionRevision(ctx); !selected {
		return nil, errors.New("selection was not bound to current policy")
	}
	s.request = request
	return []View{}, nil
}

func TestJobListUsesCompositionTargetsInsteadOfCallerTargets(t *testing.T) {
	store := &collectionRequestStore{}
	service := &Service{store: store}
	configured := ListTargets{Enforced: true, PublicationHosts: []string{"configured.example"}}
	if err := WithListTargets(func(tenantID string) ListTargets {
		if tenantID != "tenant" {
			t.Fatal("configuration selected another tenant")
		}
		return configured
	})(service); err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	page, err := service.List(ctx, ListRequest{Limit: 1, Targets: ListTargets{Enforced: false, PublicationHosts: []string{"caller.example"}}})
	if err != nil || len(page) != 0 || !store.request.Targets.Enforced || len(store.request.Targets.PublicationHosts) != 1 || store.request.Targets.PublicationHosts[0] != "configured.example" || store.request.Limit != 2 {
		t.Fatal("caller bypassed deployment selection", store.request, page, err)
	}
}
