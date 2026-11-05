// Coordinator tests check bounded scheduling and drain/error behavior without
// PostgreSQL or inference. Repository tests own eligibility and lease fencing.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// semanticMaintenanceFixture records orchestration, not database semantics.
type semanticMaintenanceFixture struct {
	t             *testing.T
	configuration config.Semantic
	steps         []string
	fail          string
	err           error
	cancel        context.CancelFunc
}

func (f *semanticMaintenanceFixture) step(ctx context.Context, name string) error {
	f.steps = append(f.steps, name)
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == f.fail {
		return f.err
	}
	return nil
}

func (f *semanticMaintenanceFixture) check(profile string, excluded []string) {
	f.t.Helper()
	if profile != f.configuration.EmbeddingProfile() || !reflect.DeepEqual(excluded, f.configuration.ExcludedTenants) {
		f.t.Fatal("maintenance lost configured profile or tenant exclusions")
	}
}

func (f *semanticMaintenanceFixture) Cleanup(ctx context.Context, profile string, excluded []string, limit int) (int64, error) {
	f.check(profile, excluded)
	if limit != 128 {
		f.t.Fatal("cleanup work bound changed")
	}
	return 0, f.step(ctx, "cleanup")
}

func (f *semanticMaintenanceFixture) Due(ctx context.Context, profile string, dimensions int, excluded []string, limit int) ([]catalog.SemanticGeneration, error) {
	f.check(profile, excluded)
	if limit != 16 || dimensions != f.configuration.Dimensions {
		f.t.Fatal("admission selection bounds changed")
	}
	return []catalog.SemanticGeneration{
		{TenantID: strings.ToLower(excluded[0])},
		{TenantID: "invalid"},
		{TenantID: "00000000-0000-7000-8000-000000000001", Profile: profile, Scope: "control", SourceGeneration: "7", Dimensions: dimensions},
	}, f.step(ctx, "due")
}

func (f *semanticMaintenanceFixture) Admit(ctx context.Context, g catalog.SemanticGeneration, excluded []string, maxAttempts int) (bool, error) {
	if maxAttempts != 7 {
		f.t.Fatalf("lost configured attempt limit: %d", maxAttempts)
	}
	f.check(g.Profile, excluded)
	if g.ID != "00000000-0000-7000-8000-000000000002" || g.TenantID != "00000000-0000-7000-8000-000000000001" || g.Scope != "control" || g.SourceGeneration != "7" || g.Dimensions != f.configuration.Dimensions {
		f.t.Fatal("admission changed scope identity or admitted a forbidden tenant")
	}
	err := f.step(ctx, "admit")
	if f.cancel != nil {
		f.cancel()
	}
	// A concurrent replica may already have admitted the same scope. That is
	// ordinary no-work, not a reason to fail or retry admission in this pass.
	return false, err
}

func (f *semanticMaintenanceFixture) Now() time.Time { return time.Unix(123, 0) }

func (f *semanticMaintenanceFixture) New(now time.Time) (string, error) {
	if !now.Equal(f.Now()) {
		f.t.Fatal("identifier did not use the injected clock")
	}
	return "00000000-0000-7000-8000-000000000002", f.step(context.Background(), "id")
}

// TestSemanticMaintenance stops at the first failure, forwards policy to every
// store boundary, and drains after an immediate bounded pass without a sleep.
func TestSemanticMaintenance(t *testing.T) {
	for _, failure := range []string{"", "cleanup", "due", "id", "admit", "drain"} {
		t.Run(failure, func(t *testing.T) {
			configuration := config.SemanticDefaults()
			configuration.Enabled, configuration.Dimensions = true, 384
			configuration.Model, configuration.ProfileRevision = "fixture", "v1"
			configuration.ExcludedTenants = []string{"AAAAAAAA-0000-7000-8000-000000000001"}
			f := &semanticMaintenanceFixture{t: t, configuration: configuration, fail: failure, err: errors.New("fixture failure")}
			s := &semanticMaintainer{store: f, configuration: configuration, maxAttempts: 7, clock: f, ids: f}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var err error
			if failure == "drain" {
				f.cancel = cancel
				err = s.Run(ctx)
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("drain: %v", err)
				}
			} else {
				if failure == "" {
					err = s.reconcile(ctx)
				} else {
					err = s.Run(ctx)
				}
				if failure == "" && err != nil || failure != "" && !errors.Is(err, f.err) {
					t.Fatalf("failure propagation: %v", err)
				}
			}
			want := []string{"cleanup", "due", "id", "admit"}
			for i, step := range want {
				if step == failure {
					want = want[:i+1]
					break
				}
			}
			if !reflect.DeepEqual(f.steps, want) {
				t.Fatalf("steps: got %v, want %v", f.steps, want)
			}
		})
	}
}

// semanticJobStore exercises coordinator policy with an empty population or one
// failed provider batch. Unexpected writes hit the unimplemented port and fail.
type semanticJobStore struct {
	catalog.SemanticIndexStore
	catalog.SemanticProvider
	t             *testing.T
	want          catalog.SemanticGeneration
	err           error
	began         bool
	completed     bool
	providerError error
	providerCalls int
	inputs        []catalog.SemanticInput
}

func (f *semanticJobStore) Begin(_ context.Context, g catalog.SemanticGeneration, owner string) error {
	f.t.Helper()
	if g != f.want || owner != "worker-lease" {
		f.t.Fatalf("lost Job identity or lease: %+v %q", g, owner)
	}
	f.began = true
	return f.err
}

func (f *semanticJobStore) Inputs(context.Context, catalog.SemanticGeneration, string, int) ([]catalog.SemanticInput, int64, error) {
	return f.inputs, int64(len(f.inputs)), nil
}

func (f *semanticJobStore) KnownInputs(context.Context, catalog.SemanticGeneration, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (f *semanticJobStore) Documents(context.Context, []string) ([][]float32, error) {
	f.providerCalls++
	return nil, f.providerError
}

func (f *semanticJobStore) Complete(_ context.Context, g catalog.SemanticGeneration, owner string, total int64) error {
	f.t.Helper()
	if g != f.want || owner != "worker-lease" || total != 0 {
		f.t.Fatal("completion lost identity, lease or empty population count")
	}
	f.completed = true
	return nil
}

// TestSemanticJobHandler checks policy, failure classification and progress at
// the runtime boundary. Indexer tests separately exercise actual metadata pages.
func TestSemanticJobHandler(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cause    error
		class    jobs.FailureClass
		provider bool
		delay    time.Duration
	}{
		{name: "success"},
		{name: "payload", class: jobs.FailureInvalidInput},
		{name: "profile", class: jobs.FailureInvalidInput},
		{name: "excluded", class: jobs.FailureInvalidInput},
		{name: "progress-start"},
		{name: "progress-complete"},
		{name: "dependency", cause: catalog.ErrEmbeddingUnavailable, class: jobs.FailureDependency},
		{name: "provider minimum", cause: fmt.Errorf("provider: %w", &catalog.EmbeddingRetryError{RetryAfter: 2 * time.Minute}), class: jobs.FailureDependency, provider: true, delay: 2 * time.Minute},
		{name: "hint cannot override authentication", cause: errors.Join(catalog.ErrEmbeddingAuthentication, &catalog.EmbeddingRetryError{RetryAfter: 2 * time.Minute}), class: jobs.FailureAuthentication, provider: true},
		{name: "authentication", cause: catalog.ErrEmbeddingAuthentication, class: jobs.FailureAuthentication},
		{name: "response", cause: catalog.ErrEmbeddingInvalidResponse, class: jobs.FailurePermanentUpstream},
		{name: "generation", cause: catalog.ErrSearchGenerationChanged, class: jobs.FailureInvalidInput},
		{name: "invalid", cause: catalog.E(catalog.CodeInvalidArgument, "fixture"), class: jobs.FailureInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configuration := config.SemanticDefaults()
			configuration.Enabled, configuration.Dimensions = true, 384
			configuration.Model, configuration.ProfileRevision = "fixture", "v1"
			input := catalog.SemanticReindexInput{Profile: configuration.EmbeddingProfile(), Scope: "control", SourceGeneration: "7"}
			job := jobs.Job{ID: "00000000-0000-7000-8000-000000000001", TenantID: "aaaaaaaa-0000-7000-8000-000000000001", LeaseOwner: "worker-lease", PayloadVersion: 1}
			f := &semanticJobStore{t: t, err: tc.cause, want: catalog.SemanticGeneration{ID: job.ID, TenantID: job.TenantID, Profile: input.Profile, Scope: input.Scope, SourceGeneration: input.SourceGeneration, Dimensions: 384}}
			if tc.provider {
				f.err, f.providerError = nil, tc.cause
				f.inputs = []catalog.SemanticInput{{Key: "a", Document: catalog.SemanticDocument{DisplayName: "Fixture"}}}
			}
			switch tc.name {
			case "payload":
				job.PayloadVersion = 0
			case "profile":
				input.Profile = "sha256:" + strings.Repeat("0", 64)
			case "excluded":
				configuration.ExcludedTenants = []string{strings.ToUpper(job.TenantID)}
			}
			var err error
			job.Payload, err = json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			indexer, err := catalog.NewSemanticIndexer(f, f, 8, 8192)
			if err != nil {
				t.Fatal(err)
			}
			progressErr := errors.New("lease lost while reporting progress")
			var progress []int
			result, err := semanticJobHandler(configuration, indexer)(t.Context(), job, func(value int) error {
				progress = append(progress, value)
				if tc.name == "progress-start" && value == 1 || tc.name == "progress-complete" && value == 100 {
					return progressErr
				}
				return nil
			})
			wantProgress := []int{1}
			switch tc.name {
			case "success", "progress-complete":
				wantProgress = []int{1, 100}
			case "payload", "profile", "excluded":
				wantProgress = nil
			}
			if !reflect.DeepEqual(progress, wantProgress) || f.began != (len(wantProgress) > 0 && tc.name != "progress-start") || f.completed != (len(wantProgress) == 2) {
				t.Fatalf("unexpected execution: progress=%v began=%v completed=%v", progress, f.began, f.completed)
			}
			wantCalls := 0
			if tc.provider {
				wantCalls = 1
			}
			if f.providerCalls != wantCalls {
				t.Fatalf("provider calls=%d, want %d", f.providerCalls, wantCalls)
			}
			switch {
			case tc.class != "":
				var failure *jobs.HandlerError
				if !errors.As(err, &failure) || failure.Class != tc.class || failure.RetryAfter != tc.delay || tc.cause != nil && !errors.Is(failure.Cause, tc.cause) {
					t.Fatalf("classification: %v", err)
				}
			case tc.name != "success":
				if !errors.Is(err, progressErr) {
					t.Fatalf("progress error lost: %v", err)
				}
			default:
				encoded, marshalErr := json.Marshal(result)
				if err != nil || marshalErr != nil || string(encoded) != `{"generation":"`+job.ID+`","members":0}` {
					t.Fatalf("completion: %s %v %v", encoded, err, marshalErr)
				}
			}
			if err != nil && result != nil {
				t.Fatal("failed Job reported success output")
			}
		})
	}
}
