package jobs

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type metadataOnlyStore struct {
	Store
	views *MemoryStore
}

// Leaving full reads unimplemented makes accidental execution-data hydration fail.
func (s metadataOnlyStore) GetView(ctx context.Context, tenantID, jobID string) (View, error) {
	return s.views.GetView(ctx, tenantID, jobID)
}

func (s metadataOnlyStore) ListViews(ctx context.Context, tenantID string, request ListRequest) ([]View, error) {
	return s.views.ListViews(ctx, tenantID, request)
}

func TestJobServiceUsesIndependentMetadataReads(t *testing.T) {
	store := NewMemoryStore()
	at := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	const firstID = "00000000-0000-7000-8000-000000000009"
	const secondID = "00000000-0000-7000-8000-000000000008"
	first := Job{ID: firstID, TenantID: "tenant", Kind: "source.sync", State: StateSucceeded, CreatedAt: at, Result: json.RawMessage(`{"id":"` + firstID + `","sourceId":"` + secondID + `","state":"complete"}`)}
	second := first
	second.ID = secondID
	store.jobs[firstID], store.jobs[secondID] = first, second
	service := &Service{store: metadataOnlyStore{views: store}}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "tenant", Subject: "fixture", Roles: []identity.Role{identity.RoleTenantAdmin}})
	page, err := service.List(ctx, ListRequest{Limit: 1})
	if err != nil || len(page) != 2 || page[0].ID != firstID || page[1].ID != secondID {
		t.Fatalf("page lost order or continuation sentinel: %+v, %v", page, err)
	}
	page[0].Result.State = "failed"
	got, err := service.Get(ctx, firstID)
	want := View{ID: firstID, TenantID: "tenant", Kind: "source.sync", State: StateSucceeded, CreatedAt: at, Result: &ResultSummary{Type: "source_snapshot", SnapshotID: firstID, SourceID: secondID, State: "complete"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("read result mutated stored metadata: %+v, %v", got, err)
	}
	page, err = service.List(ctx, ListRequest{Limit: 1, AfterCreatedAt: &at, AfterID: firstID})
	if err != nil || len(page) != 1 || page[0].ID != secondID {
		t.Fatalf("continuation returned %+v, %v", page, err)
	}
	page, err = service.List(ctx, ListRequest{Limit: 1, AfterCreatedAt: &at, AfterID: secondID})
	if err != nil || page == nil || len(page) != 0 {
		t.Fatalf("empty page returned %+v, %v", page, err)
	}
	for _, jobID := range []string{firstID, "missing"} {
		other := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "other", Subject: "fixture", Roles: []identity.Role{identity.RoleTenantAdmin}})
		if _, err := service.Get(other, jobID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("nonexistent or other-tenant Job exposed: %v", err)
		}
	}
}

func TestMemoryJobReadsOwnExecutionData(t *testing.T) {
	for _, operation := range []string{"get", "list"} {
		for _, field := range []string{"payload", "result", "lease expiry"} {
			t.Run(operation+"/"+field, func(t *testing.T) {
				at := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
				store := NewMemoryStore()
				originals := []Job{
					{ID: "first", TenantID: "tenant", Kind: "test", State: StateSucceeded, Progress: 100, CreatedAt: at.Add(time.Second), Payload: json.RawMessage(`{"input":1}`), Result: json.RawMessage(`{"result":2}`)},
					{ID: "second", TenantID: "tenant", Kind: "test", State: StateSucceeded, Progress: 100, CreatedAt: at, Payload: json.RawMessage(`{"input":3}`), Result: json.RawMessage(`{"result":4}`)},
				}
				if field == "lease expiry" {
					expires := at.Add(time.Minute)
					originals[0].State, originals[0].Progress = StateRunning, 0
					originals[0].LeaseOwner, originals[0].LeaseExpiresAt = "worker", &expires
					originals[0].Result = nil
				}
				for _, job := range originals {
					job.DeduplicationKey = job.ID
					if _, existing, err := store.Enqueue(t.Context(), job); err != nil || existing {
						t.Fatalf("seed Job: existing=%t, err=%v", existing, err)
					}
				}
				var reads []Job
				if operation == "get" {
					job, err := store.Get(t.Context(), "tenant", "first")
					if err != nil {
						t.Fatal(err)
					}
					reads = []Job{job}
				} else {
					var err error
					reads, err = store.List(t.Context(), "tenant", ListRequest{Limit: 1})
					if err != nil || len(reads) != 1 || reads[0].ID != "first" {
						t.Fatalf("selected page: %+v, %v", reads, err)
					}
					// Every accessible slot must be independent, including spare capacity.
					reads = reads[:cap(reads)]
				}
				for index := range reads {
					job := &reads[index]
					switch field {
					case "payload":
						if len(job.Payload) > 0 {
							job.Payload[0] = '!'
						}
					case "result":
						if len(job.Result) > 0 {
							job.Result[0] = '!'
						}
					case "lease expiry":
						if job.LeaseExpiresAt != nil {
							*job.LeaseExpiresAt = job.LeaseExpiresAt.Add(time.Hour)
						}
					}
				}
				for _, want := range originals {
					got, err := store.Get(t.Context(), "tenant", want.ID)
					if err != nil || string(got.Payload) != string(want.Payload) || string(got.Result) != string(want.Result) {
						t.Fatalf("read changed retained %s for %s", field, want.ID)
					}
					if field == "lease expiry" && want.ID == "first" && (got.LeaseExpiresAt == nil || !got.LeaseExpiresAt.Equal(at.Add(time.Minute))) {
						t.Fatal("read changed retained lease expiry")
					}
				}
			})
		}
	}
}
