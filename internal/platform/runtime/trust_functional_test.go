//go:build functional

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type trustSourceAvailability struct {
	federation.Store
	available *bool
}

func (s trustSourceAvailability) GetSource(ctx context.Context, tenant, id string) (federation.Source, error) {
	if !*s.available {
		return federation.Source{}, catalog.E(catalog.CodeDependencyUnavailable, "injected source-store outage")
	}
	return s.Store.GetSource(ctx, tenant, id)
}

func TestTrustWorkerRetainsPrivateEvidenceAndAudit(t *testing.T) {
	for _, scenario := range []string{"valid", "signature", "unsupported framework", "credential current", "credential rotated", "credential rotated during evidence", "outage recovery", "outage exhausted", "source dependency", "source changed", "cancelled", "cancel during evidence", "lease lost"} {
		t.Run(scenario, func(t *testing.T) {
			c, b, now := trustRuntimeFixture(t)
			if scenario == "unsupported framework" {
				var entry map[string]any
				if err := json.Unmarshal(b.Entry, &entry); err != nil {
					t.Fatal(err)
				}
				manifest, ok := entry["trustManifest"].(map[string]any)
				if !ok {
					t.Fatal("native fixture lacks Trust Manifest")
				}
				manifest["trustSchema"] = map[string]any{"identifier": "https://framework.example/unsupported", "version": "1"}
				var err error
				b.Entry, err = json.Marshal(entry)
				if err != nil {
					t.Fatal(err)
				}
			}
			script := &trustScript{bundle: b, digest: c.Federation.PolicyDigest}
			if scenario == "signature" {
				script.bundle.Receipt = bytes.Clone(b.Receipt)
				script.bundle.Receipt[len(b.Receipt)-1] ^= 1
			}
			if scenario == "outage recovery" || scenario == "outage exhausted" {
				script.status = 503
			}
			credentialGeneration := "generation-one"
			v, err := newTrustVerifier(c, script, trustCredentialResolver(func(_ context.Context, r fetcher.CredentialRequest) (fetcher.ResolvedCredential, error) {
				return fetcher.ResolvedCredential{Revision: credentialGeneration, Origin: r.Origin}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			repository := catalog.NewMemoryRepository()
			sources := federation.NewMemoryStore()
			available := scenario != "source dependency"
			var sourceStore federation.Store = sources
			if !available {
				sourceStore = trustSourceAvailability{sources, &available}
			}
			clk := clock.NewFake(now)
			manager, err := federation.NewManager(sourceStore, script, clk, id.Generator{}, federation.WithTrustVerifier(v))
			if err != nil {
				t.Fatal(err)
			}
			source := federation.Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: "00000000-0000-7000-8000-000000000001", Status: federation.SourceActive, Revision: 1, TrustVerificationPolicy: "owned", PolicyID: "owned", PolicyRevision: 1, PolicyDigest: c.Federation.PolicyDigest}
			if scenario == "credential current" || scenario == "credential rotated" || scenario == "credential rotated during evidence" {
				source.Endpoint = "https://peer.example/search"
				source.CredentialBinding, source.CredentialRevision = "owned-peer", credentialGeneration
			}
			if _, err = sources.CreateSource(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			digest, _, _ := v.Policy("owned")
			p := jobs.TrustPayload{SourceID: source.ID, SourceRevision: 1, SourcePolicyDigest: source.PolicyDigest, PolicyName: "owned", PolicyDigest: digest, Identifier: "urn:air:trust-v2.ans.test:agents:trust-v2", Version: "1.0.0", VersionPresent: true, MediaType: "application/mcp-server-card+json", Delivery: "url", ExactEntryDigest: audit.DigestBytes(b.Entry), Entry: b.Entry}
			p.CredentialRevision = source.CredentialRevision
			payload, marshalErr := json.Marshal(p)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			job := jobs.Job{ID: "00000000-0000-7000-8000-000000000011", TenantID: source.TenantID, Kind: jobs.TrustKind, PayloadVersion: 1, Payload: payload, Actor: "00000000-0000-7000-8000-000000000012", State: jobs.StateQueued, AttemptLimit: 2, DeduplicationKey: "owned", NotBefore: now, CreatedAt: now, UpdatedAt: now}
			store := memoryTrustJobs{Store: jobs.NewMemoryStore(repository), sources: sources}
			if _, _, err = store.Enqueue(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			worker, err := jobs.NewWorker(store, clk, "owned-worker", time.Minute, time.Millisecond, time.Minute, map[string]jobs.Handler{jobs.TrustKind: trustJobHandler(manager, slog.New(slog.DiscardHandler))})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "source changed" {
				script.fault = func(ctx context.Context, _ fetcher.Request) error {
					source.Revision++
					_, e := sources.UpdateSource(ctx, source, source.Revision-1)
					return e
				}
			}
			if scenario == "credential rotated" {
				credentialGeneration = "generation-two"
			}
			if scenario == "credential rotated during evidence" {
				script.fault = func(context.Context, fetcher.Request) error {
					credentialGeneration = "generation-two"
					return nil
				}
			}
			if scenario == "cancel during evidence" {
				script.fault = func(ctx context.Context, _ fetcher.Request) error {
					_, e := store.RequestCancellation(ctx, job.TenantID, job.ID, now)
					return e
				}
			}
			if scenario == "lease lost" {
				script.fault = func(ctx context.Context, _ fetcher.Request) error {
					clk.Advance(2 * time.Minute)
					_, e := store.Claim(ctx, "replacement-worker", clk.Now(), time.Minute)
					return e
				}
			}
			if scenario == "cancelled" {
				if _, err = store.RequestCancellation(t.Context(), job.TenantID, job.ID, now); err != nil {
					t.Fatal(err)
				}
			}
			if err = worker.RunOnce(t.Context()); err != nil && scenario != "lease lost" {
				t.Fatal(err)
			}
			retained, err := store.Get(t.Context(), source.TenantID, job.ID)
			if err != nil {
				t.Fatalf("worker=%+v err=%v", retained, err)
			}
			if scenario == "outage recovery" || scenario == "outage exhausted" || scenario == "source dependency" {
				if retained.State != jobs.StateWaiting || len(retained.Result) != 0 || retained.Attempt != 1 {
					t.Fatalf("transient outage did not retain same Job: %+v", retained)
				}
				if scenario == "outage recovery" {
					script.status = 0
				}
				if scenario == "source dependency" {
					if retained.FailureClass != jobs.FailureDependency || script.calls != 0 {
						t.Fatal("source-store outage was not retryable or contacted evidence")
					}
					available = true
				}
				clk.Advance(retained.NotBefore.Sub(clk.Now()) + time.Millisecond)
				if err = worker.RunOnce(t.Context()); err != nil {
					t.Fatal(err)
				}
				retained, err = store.Get(t.Context(), source.TenantID, job.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "lease lost" {
				if retained.State != jobs.StateRunning || retained.LeaseOwner != "replacement-worker" || len(retained.Result) > 0 || script.calls != 1 {
					t.Fatal("lost lease completed or fetched further evidence")
				}
				return
			}
			if scenario == "source changed" || scenario == "credential rotated" || scenario == "credential rotated during evidence" || scenario == "cancelled" || scenario == "cancel during evidence" || scenario == "outage exhausted" {
				wantState, wantClass := jobs.StateFailed, jobs.FailurePolicyDenied
				switch scenario {
				case "cancelled", "cancel during evidence":
					wantState, wantClass = jobs.StateCancelled, jobs.FailureCancelled
				case "outage exhausted":
					wantState, wantClass = jobs.StateDeadLetter, jobs.FailureTransientUpstream
				}
				if retained.State != wantState || scenario != "cancelled" && retained.FailureClass != wantClass {
					t.Fatalf("wrong terminal outcome: state=%s class=%s want state=%s class=%s", retained.State, retained.FailureClass, wantState, wantClass)
				}
				if len(retained.Result) != 0 {
					t.Fatal("failed execution manufactured evidence")
				}
				wantCalls := 1
				if scenario == "cancelled" || scenario == "credential rotated" {
					wantCalls = 0
				} else if scenario == "outage exhausted" {
					wantCalls = 2
				}
				if script.calls != wantCalls {
					t.Fatalf("terminal work made %d evidence requests, want %d", script.calls, wantCalls)
				}
				return
			}
			if retained.State != jobs.StateSucceeded {
				t.Fatalf("assessment failed: %+v", retained)
			}
			var result jobs.TrustAssessment
			if json.Unmarshal(retained.Result, &result) != nil || result.Assessment.CurrentRegistration != (scenario != "signature" && scenario != "unsupported framework") || !bytes.Equal(result.Evidence.Entry, b.Entry) {
				t.Fatal("retained exact evidence or outcome changed")
			}
			if scenario == "signature" && (retained.Attempt != 1 || script.calls != 1) {
				t.Fatal("invalid signature retried or fetched further evidence")
			}
			if scenario == "unsupported framework" && (script.calls != 0 || retained.Attempt != 1 || result.Assessment.Target.State != "unsupported" || len(result.Evidence.Receipt) != 0) {
				t.Fatal("unsupported framework lost its classification or contacted evidence")
			}
			if scenario != "signature" && scenario != "unsupported framework" && !bytes.Equal(result.Evidence.Receipt, b.Receipt) {
				t.Fatal("receipt bytes changed")
			}
			records, e := repository.AuditEvents(t.Context(), job.TenantID, catalog.AuditEventListRequest{Filters: audit.Query{Action: audit.ActionSourceTrustCompleted}, Limit: 10})
			if e != nil || len(records) != 1 {
				t.Fatalf("completion audit count=%d err=%v", len(records), e)
			}
			public, marshalErr := json.Marshal(jobs.PublicView(retained))
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if bytes.Contains(public, []byte("evidence")) || bytes.Contains(public, []byte("policyName")) || jobs.PublicView(retained).Result != nil {
				t.Fatalf("generic response exposed evidence: %s", public)
			}

		})
	}
}
