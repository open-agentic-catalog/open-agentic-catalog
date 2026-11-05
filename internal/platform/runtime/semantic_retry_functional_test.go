//go:build functional

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/embeddings"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

// The real indexer owns page completion; the existing fixture retains identity
// and lease assertions. PostgreSQL separately qualifies durable storage fences.
type semanticRetryStore struct {
	*semanticJobStore
	members []catalog.SemanticMember
}

func (f *semanticRetryStore) Inputs(context.Context, catalog.SemanticGeneration, string, int) ([]catalog.SemanticInput, int64, error) {
	if len(f.members) == 0 {
		return []catalog.SemanticInput{{Key: "a", Document: catalog.SemanticDocument{DisplayName: "Fixture"}}}, 1, nil
	}
	return nil, 1, nil
}

func (f *semanticRetryStore) SaveBatch(_ context.Context, g catalog.SemanticGeneration, owner string, members []catalog.SemanticMember) error {
	f.t.Helper()
	if g != f.want || owner != "worker-lease" || len(members) != 1 || members[0].Key != "a" || members[0].Failure != "" || members[0].Reuse || !slices.Equal(members[0].Vector, []float32{0.6, 0.8}) {
		f.t.Fatal("saved incomplete metadata or lost generation/lease identity")
	}
	f.members = slices.Clone(members)
	return nil
}

func (f *semanticRetryStore) Complete(_ context.Context, g catalog.SemanticGeneration, owner string, total int64) error {
	f.t.Helper()
	if g != f.want || owner != "worker-lease" || total != 1 || len(f.members) != 1 {
		f.t.Fatal("completed an incomplete or different generation")
	}
	f.completed = true
	return nil
}

type semanticRetryFixture struct {
	logs       *bytes.Buffer
	metrics    *observability.Registry
	client     *embeddings.Client
	store      *jobs.MemoryStore
	indexStore *semanticRetryStore
	worker     *jobs.Worker
	job        jobs.Job
}

func newSemanticRetryFixture(t *testing.T, endpoint string, fake *clock.Fake, attempts int) semanticRetryFixture {
	t.Helper()
	configuration := config.SemanticDefaults()
	configuration.Enabled, configuration.AllowHTTP = true, true
	configuration.Endpoint = endpoint + "/v1/embeddings"
	configuration.AllowedAddressPrefixes = []string{"127.0.0.0/8"}
	configuration.Model, configuration.ProfileRevision, configuration.Dimensions = "fixture", "one", 2
	client, err := embeddings.New(configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	input := catalog.SemanticReindexInput{Profile: configuration.EmbeddingProfile(), Scope: "control", SourceGeneration: "7"}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{
		ID: "00000000-0000-7000-8000-000000000001", TenantID: "aaaaaaaa-0000-7000-8000-000000000001",
		Kind: "semantic.reindex", State: jobs.StateQueued, Payload: payload, PayloadVersion: 1,
		AttemptLimit: attempts, NotBefore: fake.Now(), CreatedAt: fake.Now(), UpdatedAt: fake.Now(),
	}
	indexStore := &semanticRetryStore{semanticJobStore: &semanticJobStore{t: t, want: catalog.SemanticGeneration{
		ID: job.ID, TenantID: job.TenantID, Profile: input.Profile, Scope: input.Scope, SourceGeneration: input.SourceGeneration, Dimensions: 2,
	}}}
	indexer, err := catalog.NewSemanticIndexer(indexStore, client, configuration.BatchSize, configuration.MaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	store := jobs.NewMemoryStore()
	if _, _, err := store.Enqueue(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	metrics := observability.New(fake.Now)
	logs := new(bytes.Buffer)
	worker, err := jobs.NewWorker(store, fake, "worker-lease", time.Minute, time.Second, 5500*time.Millisecond, map[string]jobs.Handler{
		"semantic.reindex": semanticJobHandler(configuration, indexer),
	}, jobObserver{metrics, diagnostics.NewLogger(logs, "debug", "json")})
	if err != nil {
		t.Fatal(err)
	}

	return semanticRetryFixture{logs: logs, metrics: metrics, client: client, store: store, indexStore: indexStore, worker: worker, job: job}
}

// HTTP 429 then 503 must schedule the same indexing Job beyond the minimum,
// including a capped later attempt, before a successful provider response.
func TestSemanticProviderHintsScheduleAndRecoverSameJob(t *testing.T) {
	fake := clock.NewFake(time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC))
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" {
			t.Error("unexpected provider request")
		}
		// Simulate elapsed handler work without sleeping: scheduling must use
		// completion time, not the earlier claim time.
		fake.Advance(7 * time.Second)
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if call <= 2 {
			w.Header().Set("Retry-After", "120")
			status := http.StatusTooManyRequests
			if call == 2 {
				status = http.StatusServiceUnavailable
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"private provider response"}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"fixture","data":[{"index":0,"embedding":[3,4]}]}`))
	}))
	t.Cleanup(server.Close)
	f := newSemanticRetryFixture(t, server.URL, fake, 3)
	var totalDelay float64
	for attempt := 1; attempt <= 2; attempt++ {
		if err := f.worker.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		waiting, err := f.store.Get(t.Context(), f.job.TenantID, f.job.ID)
		window := 5 * time.Second
		if attempt == 2 {
			window = 5500 * time.Millisecond
		}
		if err != nil || waiting.State != jobs.StateWaiting || waiting.Attempt != attempt || waiting.AttemptLimit != 3 || waiting.FailureClass != jobs.FailureDependency || waiting.SafeError != "Semantic indexing did not complete; lexical search remains available." || !waiting.UpdatedAt.Equal(fake.Now()) || waiting.NotBefore.Before(fake.Now().Add(2*time.Minute)) || waiting.NotBefore.After(fake.Now().Add(2*time.Minute+window)) || waiting.LeaseOwner != "" || waiting.LeaseExpiresAt != nil {
			t.Fatalf("attempt %d lost retry policy: %#v, %v", attempt, waiting, err)
		}
		if calls.Load() != int32(attempt) || len(f.indexStore.members) != 0 || f.indexStore.completed || f.client.State() != "degraded" {
			t.Fatal("transient failure retried inference, saved a page or completed indexing")
		}
		totalDelay += waiting.NotBefore.Sub(fake.Now()).Seconds()
		fake.Advance(waiting.NotBefore.Sub(fake.Now()) - time.Nanosecond)
		if err := f.worker.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		unchanged, err := f.store.Get(t.Context(), f.job.TenantID, f.job.ID)
		if err != nil || !reflect.DeepEqual(unchanged, waiting) || calls.Load() != int32(attempt) {
			t.Fatal("Job ran before its due time")
		}
		fake.Advance(time.Nanosecond)
	}
	if err := f.worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	completed, err := f.store.Get(t.Context(), f.job.TenantID, f.job.ID)
	if err != nil || completed.State != jobs.StateSucceeded || completed.Attempt != 3 || completed.AttemptLimit != 3 || completed.Progress != 100 || completed.FailureClass != "" || completed.SafeError != "" || calls.Load() != 3 || !f.indexStore.completed || len(f.indexStore.members) != 1 || f.client.State() != "healthy" {
		t.Fatalf("same Job did not recover: %#v, requests=%d, %v", completed, calls.Load(), err)
	}
	if strings.Count(f.logs.String(), "job.attempt_completed") != 3 || strings.Contains(f.logs.String(), `"level":"WARN"`) || strings.Contains(f.logs.String(), `"level":"ERROR"`) {
		t.Fatalf("ordinary retries/recovery diagnostics: %s", f.logs.String())
	}
	metrics := string(f.metrics.Render(t.Context()))
	for _, want := range []string{
		`open_agentic_catalog_job_retry_delay_seconds_count{kind="semantic_reindex"} 2`,
		fmt.Sprintf(`open_agentic_catalog_job_retry_delay_seconds_sum{kind="semantic_reindex"} %g`, totalDelay),
	} {
		if !strings.Contains(metrics, want+"\n") {
			t.Fatalf("real provider retry metric missing %s", want)
		}
	}
	for _, private := range []string{f.job.TenantID, f.job.ID, server.URL, "private provider response"} {
		if strings.Contains(metrics+f.logs.String(), private) {
			t.Fatal("retry metrics disclose private provider or Job data")
		}
	}
}

// Exercise response handling through the worker without cloning the scheduler's
// unit matrix. These cases bind real wire hints to retry permission and bounds.
func TestSemanticProviderRetryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		header   string
		attempts int
		state    jobs.State
		class    jobs.FailureClass
	}{
		{"invalid hint", 503, "private invalid timing", 3, jobs.StateWaiting, jobs.FailureDependency},
		{"HTTP date", 503, "date", 3, jobs.StateWaiting, jobs.FailureDependency},
		{"maximum hint", 429, "86400", 3, jobs.StateWaiting, jobs.FailureDependency},
		{"authentication", 401, "120", 3, jobs.StateFailed, jobs.FailureAuthentication},
		{"invalid response", 200, "120", 3, jobs.StateFailed, jobs.FailurePermanentUpstream},
		{"initial only", 503, "120", 1, jobs.StateDeadLetter, jobs.FailureDependency},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Deliberately distinct from the transport's real receipt clock.
			fake := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			var calls atomic.Int32
			until := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Second)
			header := tc.header
			if tc.name == "HTTP date" {
				header = until.Format(http.TimeFormat)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				fake.Advance(7 * time.Second)
				w.Header().Set("Retry-After", header)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"private":"provider response"}`))
			}))
			t.Cleanup(server.Close)
			f := newSemanticRetryFixture(t, server.URL, fake, tc.attempts)
			before := time.Now()
			if err := f.worker.RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			after := time.Now()
			got, err := f.store.Get(t.Context(), f.job.TenantID, f.job.ID)
			if err != nil || got.State != tc.state || got.FailureClass != tc.class || got.Attempt != 1 || got.AttemptLimit != tc.attempts || got.LeaseOwner != "" || got.LeaseExpiresAt != nil || !got.UpdatedAt.Equal(fake.Now()) || got.SafeError != "Semantic indexing did not complete; lexical search remains available." {
				t.Fatalf("provider policy lost: %#v %v", got, err)
			}
			if calls.Load() != 1 || len(f.indexStore.members) != 0 || f.indexStore.completed {
				t.Fatal("failed inference retried internally or saved incomplete metadata")
			}
			if tc.state == jobs.StateWaiting {
				minimum, maximum := time.Duration(0), 5*time.Second
				switch tc.name {
				case "HTTP date":
					minimum, maximum = until.Sub(after), until.Sub(before)+5*time.Second
					if minimum <= 0 {
						t.Fatal("date fixture expired before it could verify a positive hint")
					}
				case "maximum hint":
					minimum, maximum = 24*time.Hour, 24*time.Hour
				}
				if delay := got.NotBefore.Sub(fake.Now()); delay < minimum || delay > maximum {
					t.Fatalf("scheduled delay %s outside [%s,%s]", delay, minimum, maximum)
				}
				fake.Advance(got.NotBefore.Sub(fake.Now()) - time.Nanosecond)
			} else {
				if !got.NotBefore.Equal(f.job.NotBefore) {
					t.Fatal("terminal failure gained a retry time")
				}
				fake.Advance(25 * time.Hour)
			}
			if err := f.worker.RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			unchanged, err := f.store.Get(t.Context(), f.job.TenantID, f.job.ID)
			if err != nil || calls.Load() != 1 || !reflect.DeepEqual(unchanged, got) {
				t.Fatal("waiting or terminal Job was incorrectly retried")
			}
			var event map[string]any
			if err := json.Unmarshal(f.logs.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			level := "WARN"
			if tc.state == jobs.StateWaiting {
				level = "DEBUG"
			}
			if event["level"] != level || event["committed"] != true || event["outcome"] != string(tc.state) || event["failure_class"] != string(tc.class) || event["event"] != "job.attempt_completed" {
				t.Fatalf("committed diagnostic: %s", f.logs.String())
			}
			metrics := string(f.metrics.Render(t.Context()))
			if tc.state == jobs.StateWaiting {
				for _, want := range []string{
					`open_agentic_catalog_job_retry_delay_seconds_count{kind="semantic_reindex"} 1`,
					fmt.Sprintf(`open_agentic_catalog_job_retry_delay_seconds_sum{kind="semantic_reindex"} %g`, got.NotBefore.Sub(got.UpdatedAt).Seconds()),
				} {
					if !strings.Contains(metrics, want+"\n") {
						t.Fatalf("persisted provider hint or idle poll changed retry metric: missing %s", want)
					}
				}
			} else if strings.Contains(metrics, "open_agentic_catalog_job_retry_delay_seconds") {
				t.Fatal("terminal provider failure created a retry metric")
			}
			for _, private := range []string{f.job.TenantID, f.job.ID, server.URL, "private invalid timing", "provider response"} {
				if strings.Contains(metrics+f.logs.String(), private) {
					t.Fatal("retry metrics disclose private provider or Job data")
				}
			}
		})
	}
}
