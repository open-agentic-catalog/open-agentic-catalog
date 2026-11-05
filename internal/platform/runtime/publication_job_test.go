// Publication job tests cover host selection and progress/error translation
// using existing publication ports and isolated in-memory candidate stores.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// publicationJobRecords observes only the candidate builder's existing read port.
type publicationJobRecords func(context.Context, string) error

func (read publicationJobRecords) PublicationRecords(ctx context.Context, tenant string, _ bool, _ int) ([]catalog.CatalogRecord, bool, error) {
	return nil, false, read(ctx, tenant)
}

type publicationJobTimeIDs struct{}

func (publicationJobTimeIDs) Now() time.Time                { return time.Unix(123, 0) }
func (publicationJobTimeIDs) New(time.Time) (string, error) { return "candidate-id", nil }

func newPublicationJobManager(t *testing.T, host string, records publicationJobRecords) (*publication.Manager, *publication.MemoryStore) {
	t.Helper()
	store := publication.NewMemoryStore()
	manager, err := publication.NewManager(publication.Options{
		Records: records, Store: store, Clock: publicationJobTimeIDs{}, IDs: publicationJobTimeIDs{},
		BaseURL: "https://" + host, PublisherDomain: host, Host: host,
		ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: make([]byte, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, store
}

func TestPublicationJobRejectsInputBeforeProgress(t *testing.T) {
	manager := &publication.Manager{}
	single := map[string]*publication.Manager{"catalog.example.com": manager}
	multiple := map[string]*publication.Manager{"catalog.example.com": manager, "other.example.com": manager}
	for _, test := range []struct {
		name     string
		payload  string
		managers map[string]*publication.Manager
		safe     string
	}{
		{"malformed", `{`, single, "Publication generation job payload is invalid."},
		{"array", `[]`, single, "Publication generation job payload is invalid."},
		{"wrong field type", `{"host":3}`, single, "Publication generation job payload is invalid."},
		{"unknown field", `{"host":"catalog.example.com","extra":true}`, single, "Publication generation job payload is invalid."},
		{"duplicate host", `{"host":"catalog.example.com","host":"other.example.com"}`, multiple, "Publication generation job payload is invalid."},
		{"trailing object", `{} {}`, single, "Publication generation job payload is invalid."},
		{"no configured host", `{}`, nil, "Publication generation requires one exact configured discovery host."},
		{"ambiguous host", `{}`, multiple, "Publication generation requires one exact configured discovery host."},
		{"unknown explicit host", `{"host":"unknown.example.com"}`, single, "Publication generation requires one exact configured discovery host."},
		{"host must be exact", `{"host":"CATALOG.example.com"}`, single, "Publication generation requires one exact configured discovery host."},
		{"nil host binding", `{"host":"catalog.example.com"}`, map[string]*publication.Manager{"catalog.example.com": nil}, "Publication generation requires one exact configured discovery host."},
	} {
		t.Run(test.name, func(t *testing.T) {
			metrics := observability.New(time.Now)
			handler := publicationJobHandler(test.managers, metrics)
			result, err := claimedHandler(t, "publication.generate", handler)(t.Context(), jobs.Job{TenantID: "00000000-0000-7000-8000-000000000001", Payload: json.RawMessage(test.payload)}, func(int) error {
				t.Fatal("rejected input reported progress")
				return nil
			})
			var failure *jobs.HandlerError
			if result != nil || !errors.As(err, &failure) || failure.Class != jobs.FailureInvalidInput || failure.Safe != test.safe || failure.RetryAfter != 0 {
				t.Fatalf("rejection = %#v, %v", result, err)
			}
			assertJobOperationOutcome(t, metrics, "publication_build", "")
		})
	}
}

func TestPublicationJobSelectsHostAndPreservesAttribution(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		multiple      bool
	}{
		{"empty payload single host", "", false},
		{"empty object single host", `{}`, false},
		{"decoded empty object single host", `{ }`, false},
		{"explicit host among multiple", `{"host":"other.example.com"}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := jobs.Job{ID: "00000000-0000-7000-8000-000000000011", TenantID: "00000000-0000-7000-8000-000000000001", Payload: json.RawMessage(test.payload)}
			execution := audit.JobExecution{ID: job.ID, Kind: "publication.generate", Attempt: 1, Initiator: "00000000-0000-7000-8000-000000000021"}
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := audit.WithJobExecution(identity.WithActor(parent, identity.Actor{TenantID: "other-tenant", Subject: "caller"}), execution)
			metrics := observability.New(time.Now)
			wantHost := "catalog.example.com"
			if test.multiple {
				wantHost = "other.example.com"
			}
			var steps []int
			reads := 0
			manager, store := newPublicationJobManager(t, wantHost, func(readCtx context.Context, tenant string) error {
				reads++
				actor, ok := identity.FromContext(readCtx)
				if !ok || actor.TenantID != job.TenantID || actor.Subject != audit.PrincipalIDCatalogWorker || len(actor.Roles) != 0 || tenant != job.TenantID {
					t.Fatalf("candidate tenant = %q, actor = %#v", tenant, actor)
				}
				wantExecution := execution
				wantExecution.Trigger = "manual"
				if got, ok := audit.JobExecutionFrom(readCtx); !ok || got != wantExecution {
					t.Fatalf("candidate attribution = %#v, present %t", got, ok)
				}
				if !slices.Equal(steps, []int{20}) || readCtx.Done() == nil || parent.Err() != nil {
					t.Fatalf("progress %v, cancellation preserved %t", steps, readCtx.Done() == parent.Done())
				}
				return nil
			})
			managers := map[string]*publication.Manager{wantHost: manager}
			if test.multiple {
				other, _ := newPublicationJobManager(t, "catalog.example.com", func(context.Context, string) error {
					t.Fatal("unselected host read candidate records")
					return nil
				})
				managers["catalog.example.com"] = other
			}
			result, err := claimedHandler(t, "publication.generate", publicationJobHandler(managers, metrics))(ctx, job, func(value int) error { steps = append(steps, value); return nil })
			snapshot, ok := result.(jobs.SnapshotReference)
			if err != nil || !ok || snapshot.Host != wantHost || snapshot.State != string(publication.SnapshotCandidate) || reads != 1 || !slices.Equal(steps, []int{20, 100}) {
				t.Fatalf("candidate result = %#v, %v; reads %d, progress %v", result, err, reads, steps)
			}
			stored, err := store.Get(t.Context(), job.TenantID, snapshot.ID)
			if err != nil || stored.TenantID != job.TenantID || stored.ID != snapshot.ID || stored.Host != snapshot.Host || string(stored.State) != snapshot.State || stored.Generation != snapshot.Generation || stored.ETag == "" || stored.ARDProfile != "ard-test" {
				t.Fatalf("retained candidate = %#v, %v", stored, err)
			}
			if err := publication.ValidateSnapshotBounds(stored); err != nil {
				t.Fatalf("retained full snapshot is invalid: %v", err)
			}
			raw, err := json.Marshal(result)
			if err != nil || string(raw) != fmt.Sprintf(`{"id":"candidate-id","host":%q,"generation":%d,"state":"candidate"}`, wantHost, stored.Generation) {
				t.Fatalf("Job result is not an exact snapshot reference: %s, %v", raw, err)
			}
			if _, err := store.Active(t.Context(), job.TenantID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("candidate generation activated a publication: %v", err)
			}
			original, _ := audit.JobExecutionFrom(ctx)
			actor, _ := identity.FromContext(ctx)
			if original != execution || actor.TenantID != "other-tenant" {
				t.Fatal("handler mutated parent attribution")
			}
			assertJobOperationOutcome(t, metrics, "publication_build", "succeeded")
		})
	}
}

func TestPublicationJobProgressFailurePreservesCandidateBoundary(t *testing.T) {
	for _, failAt := range []int{20, 100} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			failure := errors.New("lease progress rejected")
			reads := 0
			manager, store := newPublicationJobManager(t, "catalog.example.com", func(context.Context, string) error { reads++; return nil })
			metrics := observability.New(time.Now)
			handler := publicationJobHandler(map[string]*publication.Manager{"catalog.example.com": manager}, metrics)
			var steps []int
			result, err := claimedHandler(t, "publication.generate", handler)(t.Context(), jobs.Job{TenantID: "00000000-0000-7000-8000-000000000001"}, func(value int) error {
				steps = append(steps, value)
				if value == failAt {
					return failure
				}
				return nil
			})
			if result != nil || !errors.Is(err, failure) {
				t.Fatalf("progress result = %#v, %v", result, err)
			}
			wantReads, outcome, wantSteps := 0, "", []int{20}
			if failAt == 100 {
				wantReads, outcome, wantSteps = 1, "succeeded", []int{20, 100}
			}
			if reads != wantReads || !slices.Equal(steps, wantSteps) {
				t.Fatalf("reads %d, progress %v", reads, steps)
			}
			stored, err := store.Get(t.Context(), "00000000-0000-7000-8000-000000000001", "candidate-id")
			if failAt == 20 && catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("initial progress failure stored a candidate: %v", err)
			}
			if failAt == 100 && (err != nil || stored.State != publication.SnapshotCandidate) {
				t.Fatalf("final progress failure lost candidate: %#v, %v", stored, err)
			}
			assertJobOperationOutcome(t, metrics, "publication_build", outcome)
		})
	}
}

func TestPublicationJobRedactsGenerationFailure(t *testing.T) {
	cause := errors.New("private record source diagnostic")
	reads := 0
	manager, store := newPublicationJobManager(t, "catalog.example.com", func(context.Context, string) error { reads++; return cause })
	metrics := observability.New(time.Now)
	var steps []int
	result, err := claimedHandler(t, "publication.generate", publicationJobHandler(map[string]*publication.Manager{"catalog.example.com": manager}, metrics))(t.Context(), jobs.Job{TenantID: "00000000-0000-7000-8000-000000000001"}, func(value int) error { steps = append(steps, value); return nil })
	var failure *jobs.HandlerError
	if result != nil || !errors.As(err, &failure) || failure.Class != jobs.FailureInternal || failure.Safe != "Publication candidate generation failed." || failure.RetryAfter != 0 || !errors.Is(failure.Cause, cause) {
		t.Fatalf("generation failure = %#v, %v", result, err)
	}
	if reads != 1 || !slices.Equal(steps, []int{20}) {
		t.Fatalf("reads %d, progress %v", reads, steps)
	}
	if _, err := store.Get(t.Context(), "00000000-0000-7000-8000-000000000001", "candidate-id"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("failed generation stored a candidate: %v", err)
	}
	assertJobOperationOutcome(t, metrics, "publication_build", "failed")
}
