// Collection units qualify exact filters before admission and page bounds without a server or database.
package jobs

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestJobExactFiltersBeforeVisibilityAndLimit(t *testing.T) {
	store := NewMemoryStore()
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	const parent = "00000000-0000-7000-8000-00000000abcd"
	for index := range 32 {
		key := fmt.Sprintf("00000000-0000-7000-8000-%012d", index+1)
		job := Job{ID: key, TenantID: "tenant", Kind: "source.sync", State: StateFailed, CreatedAt: at.Add(time.Duration(index) * time.Second)}
		if index < 3 {
			job.ParentJobID, job.State = parent, StateQueued
		}
		if index == 29 {
			job.State = StateQueued // Same family/state, but not a retry child.
		}
		if index == 30 {
			job.ParentJobID, job.State = "00000000-0000-7000-8000-00000000dcba", StateQueued
		}
		if index == 31 {
			job.ParentJobID, job.State, job.Kind = parent, StateQueued, "publication.generate"
		}
		store.jobs[key] = job
	}
	foreign := store.jobs["00000000-0000-7000-8000-000000000001"]
	foreign.ID, foreign.TenantID, foreign.CreatedAt = "00000000-0000-7000-8000-000000000999", "other-tenant", at.Add(time.Hour)
	store.jobs[foreign.ID] = foreign
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "source-reader", Roles: []identity.Role{identity.RoleSourceAdmin}})
	checked := []string{}
	service := &Service{store: metadataOnlyStore{views: store}}
	service.targetValidation = func(_ context.Context, job Job) error {
		checked = append(checked, job.ID)
		if job.ID == "00000000-0000-7000-8000-000000000003" {
			return catalog.E(catalog.CodeNotFound, "unavailable target")
		}
		return nil
	}
	q := ListRequest{Limit: 1, Kind: "source.sync", State: StateQueued, ParentJobID: strings.ToUpper(parent)}
	page, err := service.List(ctx, q)
	if err != nil || len(page) != 2 || page[0].ID != "00000000-0000-7000-8000-000000000002" || page[1].ID != "00000000-0000-7000-8000-000000000001" || len(checked) != 3 {
		t.Fatalf("filters/admission lost off-page matches: %+v, checked=%v, err=%v", page, checked, err)
	}
	q.AfterCreatedAt, q.AfterID = &page[0].CreatedAt, page[0].ID
	page, err = service.List(ctx, q)
	if err != nil || len(page) != 1 || page[0].ID != "00000000-0000-7000-8000-000000000001" {
		t.Fatalf("continuation: %+v, %v", page, err)
	}
	for _, kind := range []string{"Source.sync", "unknown.job", strings.Repeat("界", 100)} {
		page, err = service.List(ctx, ListRequest{Limit: 1, Kind: kind})
		if err != nil || len(page) != 0 {
			t.Fatalf("literal kind %q: %+v, %v", kind, page, err)
		}
	}
}

func TestJobStateFiltersSelectEachSupportedState(t *testing.T) {
	// These literals come from the public Job-state enum, not the validator.
	states := []string{"queued", "running", "waiting", "succeeded", "failed", "cancelled", "dead_letter"}
	store := NewMemoryStore()
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for index, state := range states {
		key := fmt.Sprintf("00000000-0000-7000-8000-%012d", index+1)
		store.jobs[key] = Job{ID: key, TenantID: "tenant", Kind: "source.sync", State: State(state), CreatedAt: at.Add(time.Duration(index) * time.Second)}
	}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "source-reader", Roles: []identity.Role{identity.RoleSourceAdmin}})
	service := &Service{store: metadataOnlyStore{views: store}}
	for index, state := range states {
		t.Run(state, func(t *testing.T) {
			page, err := service.List(ctx, ListRequest{Limit: 1, State: State(state)})
			want := fmt.Sprintf("00000000-0000-7000-8000-%012d", index+1)
			if err != nil || len(page) != 1 || page[0].ID != want || string(page[0].State) != state {
				t.Fatalf("state %q must select only %s: %+v, %v", state, want, page, err)
			}
		})
	}
}

func TestJobListRejectsMalformedFiltersBeforeStore(t *testing.T) {
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	service := &Service{store: metadataOnlyStore{}}
	for _, q := range []ListRequest{{Kind: strings.Repeat("x", 101)}, {Kind: string([]byte{0xff})}, {Kind: "a\x00b"}, {State: "Queued"}, {State: "cancel_requested"}, {ParentJobID: "not-a-uuid"}} {
		q.Limit = 1
		if page, err := service.List(ctx, q); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || page != nil {
			t.Fatalf("invalid filter admitted: %+v, %+v, %v", q, page, err)
		}
	}
}
