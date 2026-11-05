//go:build integration

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
)

func TestPostgresTrustJobAtomicityFencesAndRetention(t *testing.T) {
	db := newHousekeepingDatabase(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var bundle anstrust.Bundle
	fixture, err := os.ReadFile("../../standards/anstrust/testdata/native/v2.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(fixture, &bundle); err != nil {
		t.Fatal(err)
	}
	ra, e := os.ReadFile("../../standards/anstrust/testdata/native/ra.pem")
	if e != nil {
		t.Fatal(e)
	}
	tl, e := os.ReadFile("../../standards/anstrust/testdata/native/tl.pem")
	if e != nil {
		t.Fatal(e)
	}
	verifier, e := anstrust.New(anstrust.Policy{Profile: anstrust.Profile, RegistrationAuthority: "ans-ra-local", LogOrigin: "owned-ans", EvidenceOrigin: "https://tl.ans-qualification.svc.cluster.local", PublisherDomains: []string{"trust-v1.ans.test", "trust-v2.ans.test"}, RegistrationKeys: map[string][]byte{"ans-ra-signer": ra}, LogKeys: [][]byte{tl}})
	if e != nil {
		t.Fatal(e)
	}
	assessed, e := verifier.Verify(t.Context(), bundle, now)
	if e != nil {
		t.Fatal(e)
	}
	foreign := housekeepingSource("00000000-0000-7000-8000-000000000999", housekeepingTenantB, now)
	foreign.TrustVerificationPolicy = "owned"
	if _, err = db.federation.CreateSource(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	var priorAuditID string
	for index, scenario := range []string{"complete", "audit rollback", "wrong lease", "cancelled", "source changed", "cross tenant"} {
		t.Run(scenario, func(t *testing.T) {
			source := housekeepingSource(fmt.Sprintf("00000000-0000-7000-8000-%012d", 930+index), housekeepingTenantA, now)
			source.TrustVerificationPolicy = "owned"
			if _, err = db.federation.CreateSource(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			p := jobs.TrustPayload{SourceID: source.ID, SourceRevision: 1, SourcePolicyDigest: source.PolicyDigest, PolicyName: "owned", PolicyDigest: "sha256:" + strings.Repeat("b", 64), Identifier: "urn:air:trust-v2.ans.test:agents:trust-v2", Version: "1.0.0", VersionPresent: true, MediaType: "application/mcp-server-card+json", Delivery: "url", ExactEntryDigest: audit.DigestBytes(bundle.Entry), Entry: bundle.Entry}
			payload, marshalErr := json.Marshal(p)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			job := housekeepingJob(fmt.Sprintf("00000000-0000-7000-8000-%012d", 950+index), "", now)
			job.Kind = jobs.TrustKind
			job.PayloadVersion = 1
			job.Payload = payload
			job.Actor = "00000000-0000-7000-8000-000000000012"
			job.State = jobs.StateQueued
			job.DeduplicationKey = job.ID
			job.NotBefore = now
			foreignPayload := p
			foreignPayload.SourceID = foreign.ID
			foreignRaw, e := json.Marshal(foreignPayload)
			if e != nil {
				t.Fatal(e)
			}
			cross := job
			cross.Payload = foreignRaw
			if _, _, e = db.apiJobs.Enqueue(t.Context(), cross); catalog.ErrorCodeOf(e) != catalog.CodeNotFound {
				t.Fatalf("cross-tenant source binding=%v", e)
			}
			if _, _, err = db.apiJobs.Enqueue(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			if jobs.ValidateActionablePayload(job) != nil {
				t.Fatal("restore rejected current payload")
			}
			if _, err = db.apiJobs.Get(t.Context(), housekeepingTenantB, job.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("cross-tenant read=%v", err)
			}
			claimed, err := db.workerJobs.Claim(t.Context(), "trust-owner", now, time.Minute)
			if err != nil || claimed.ID != job.ID {
				t.Fatalf("claim=%s err=%v", claimed.ID, err)
			}
			result := jobs.TrustAssessment{SourceID: source.ID, SourceRevision: 1, PolicyName: p.PolicyName, PolicyDigest: p.PolicyDigest, Authority: "owned", ExactEntryDigest: p.ExactEntryDigest, MediaType: p.MediaType, Delivery: p.Delivery, Evidence: bundle, SoftwareTrust: "not_assessed", ArtifactIntegrity: "not_assessed", Rights: "not_assessed", Assessment: assessed}
			raw, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			facts, _ := audit.Facts(map[string]any{"sourceId": source.ID, "sourceRevision": int64(1), "entryDigest": p.ExactEntryDigest, "policyDigest": p.PolicyDigest, "resultDigest": audit.DigestBytes(raw), "currentRegistration": assessed.CurrentRegistration, "jobId": job.ID, "jobKind": job.Kind, "jobAttempt": claimed.Attempt, "initiator": job.Actor})
			auditID := fmt.Sprintf("00000000-0000-7000-8000-%012d", 970+index)
			if scenario == "audit rollback" {
				auditID = priorAuditID
			}
			record, err := audit.NewRecord(auditID, job.TenantID, audit.Principal{ID: audit.PrincipalIDCatalogWorker, Type: audit.PrincipalServiceExecutor}, audit.ActionSourceTrustCompleted, "job", job.ID, "", audit.Execution{}, facts, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.workerJobs.Complete(t.Context(), job.TenantID, job.ID, "trust-owner", now, raw); err == nil {
				t.Fatal("unattributed completion accepted")
			}
			owner, tenant := "trust-owner", job.TenantID
			switch scenario {
			case "wrong lease":
				owner = "other-owner"
			case "cross tenant":
				tenant = housekeepingTenantB
			case "cancelled":
				if _, err = db.apiJobs.RequestCancellation(t.Context(), job.TenantID, job.ID, now); err != nil {
					t.Fatal(err)
				}
			case "source changed":
				source.Revision++
				source.UpdatedAt = now.Add(time.Microsecond)
				if _, err = db.federation.UpdateSource(t.Context(), source, 1); err != nil {
					t.Fatal(err)
				}
			}
			_, err = db.workerJobs.Complete(t.Context(), tenant, job.ID, owner, now, raw, record)
			if scenario == "complete" {
				if err != nil {
					t.Fatal(err)
				}
				priorAuditID = record.ID
				stored, e := db.apiJobs.Get(t.Context(), job.TenantID, job.ID)
				if e != nil || stored.State != jobs.StateSucceeded {
					t.Fatal("completion not durable", e)
				}
				value, e := jobs.DecodeTrustAssessment(p, stored.Result)
				if e != nil {
					t.Fatal("stored assessment could not be decoded", e)
				}
				// Exercise the actual private read and disclosure audit with retained
				// PostgreSQL authority, not only a repository read as a fixture owner.
				peerCalls := 0
				peer := trustSelectionTransport(func(context.Context, fetcher.Request) (fetcher.Result, error) {
					peerCalls++
					return fetcher.Result{}, fmt.Errorf("private reads must not contact evidence endpoints")
				})
				manager, e := federation.NewManager(db.federation, peer, clock.NewFake(now), id.Generator{}, federation.WithTrustVerifier(trustSelectionPolicy{}), federation.WithEvidenceSink(db.apiJobs.repository, true))
				if e != nil {
					t.Fatal(e)
				}
				service, e := jobs.NewService(db.apiJobs, clock.NewFake(now), id.Generator{}, jobs.WithTargetValidation(manager.ValidateTrustJob))
				if e != nil {
					t.Fatal(e)
				}
				adminContext := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: job.TenantID, Subject: "trust-result-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
				private, e := service.PrivateTrustJob(adminContext, job.ID)
				if e != nil {
					t.Fatal("authorized private Job read failed", e)
				}
				disclosed, e := manager.ReadTrustAssessment(adminContext, source.ID, private)
				if e != nil || peerCalls != 0 {
					t.Fatal("authorized result read failed or contacted a peer", e)
				}
				for name, evidence := range map[string]anstrust.Bundle{"stored": value.Evidence, "disclosed": disclosed.Result.Evidence} {
					if evidence.Profile != bundle.Profile ||
						!bytes.Equal(evidence.Entry, bundle.Entry) ||
						!bytes.Equal(evidence.Receipt, bundle.Receipt) ||
						!bytes.Equal(evidence.Checkpoint, bundle.Checkpoint) ||
						!bytes.Equal(evidence.CheckpointHistory, bundle.CheckpointHistory) ||
						!bytes.Equal(evidence.StatusToken, bundle.StatusToken) {
						t.Fatalf("%s evidence differs from the original native fixture", name)
					}
				}
				readerContext := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: job.TenantID, Subject: "trust-result-reader", Roles: []identity.Role{identity.RoleReader}})
				if _, e = service.PrivateTrustJob(readerContext, job.ID); catalog.ErrorCodeOf(e) != catalog.CodeForbidden {
					t.Fatal("Reader disclosed a private assessment", e)
				}
				var readAudits int
				if e = db.operator.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2 AND target_id=$3`, job.TenantID, audit.ActionSourceTrustRead, job.ID).Scan(&readAudits); e != nil || readAudits != 1 {
					t.Fatalf("private-read audit count=%d err=%v", readAudits, e)
				}
				view, e := db.apiJobs.GetView(t.Context(), job.TenantID, job.ID)
				if e != nil || view.Result != nil {
					t.Fatal("generic Job exposes trust evidence", e)
				}
				marked, e := db.housekeeper.ReclaimTerminalJobs(t.Context(), 10*time.Millisecond, 10)
				if e != nil || marked.Marked != 1 {
					t.Fatalf("evidence retention mark=%+v err=%v", marked, e)
				}
				deadline := time.Now().Add(2 * time.Second)
				for {
					cleaned, e := db.housekeeper.ReclaimTerminalJobs(t.Context(), 10*time.Millisecond, 10)
					if e != nil {
						t.Fatal(e)
					}
					if cleaned.Reclaimed == 1 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("evidence retention deadline did not elapse")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if _, e = db.apiJobs.Get(t.Context(), job.TenantID, job.ID); catalog.ErrorCodeOf(e) != catalog.CodeNotFound {
					t.Fatal("terminal evidence was not deleted", e)
				}
				var count int
				if e = db.operator.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE id=$1`, record.ID).Scan(&count); e != nil || count != 1 {
					t.Fatal("cleanup deleted audit", e)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s completion accepted", scenario)
			}
			stored, e := db.apiJobs.Get(t.Context(), job.TenantID, job.ID)
			if e != nil || stored.State != jobs.StateRunning || len(stored.Result) > 0 {
				t.Fatalf("failed completion committed: %+v err=%v", stored, e)
			}
			if scenario == "source changed" {
				_, _, err = db.apiJobs.Enqueue(t.Context(), job)
				if err == nil {
					t.Fatal("stale enqueue reused work")
				}
			}
			if _, err = db.workerJobs.Fail(t.Context(), job.TenantID, job.ID, "trust-owner", now, jobs.FailureCancelled, "Cancelled by test", time.Time{}); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Verify API RLS independently of repository predicates.
	_, err = withTenantRead(t.Context(), db.api, housekeepingTenantB, func(tx pgx.Tx) (int, error) {
		var n int
		e := tx.QueryRow(t.Context(), `SELECT count(*) FROM jobs`).Scan(&n)
		if e == nil && n != 0 {
			t.Error("RLS exposed another tenant")
		}
		return n, e
	})
	if err != nil {
		t.Fatal(err)
	}
}

type trustSelectionTransport func(context.Context, fetcher.Request) (fetcher.Result, error)

func (trustSelectionTransport) PolicyDigest() string {
	return "sha256:" + strings.Repeat("a", 64)
}

func (f trustSelectionTransport) Fetch(ctx context.Context, r fetcher.Request) (fetcher.Result, error) {
	return f(ctx, r)
}

type trustSelectionPolicy struct{}

func (trustSelectionPolicy) Enabled() bool { return true }
func (trustSelectionPolicy) Policy(string) (string, string, bool) {
	return "sha256:" + strings.Repeat("b", 64), "owned", true
}
func (trustSelectionPolicy) ValidateSource(context.Context, federation.Source) error { return nil }
func (trustSelectionPolicy) Assess(context.Context, jobs.TrustPayload, func(context.Context) error, func() time.Time) (anstrust.Bundle, anstrust.Result, error) {
	panic("enqueue must not retrieve signed evidence")
}

func TestPostgresTrustSelectionOutsideTransactionAndReplay(t *testing.T) {
	db := newHousekeepingDatabase(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	source := housekeepingSource("00000000-0000-7000-8000-000000000991", housekeepingTenantA, now)
	source.Type = federation.SourceRegistry
	source.RetrievalMode = federation.RetrievalLive
	source.RegistrySearchURL = source.Endpoint
	source.Live = federation.LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true, FederationMode: "none"}
	source.TrustVerificationPolicy = "owned"
	if _, err := db.federation.CreateSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{ "identifier":"urn:air:peer.example:agents:one", "type":"application/a2a-agent-card+json", "url":"https://peer.example/card", "score":100, "source":"https://peer.example/search" }`)
	var calls int
	peer := trustSelectionTransport(func(ctx context.Context, _ fetcher.Request) (fetcher.Result, error) {
		calls++
		command := commandTransactionFrom(ctx)
		if command == nil {
			t.Fatal("command coordinator missing")
		}
		command.mu.Lock()
		active := command.tx != nil
		command.mu.Unlock()
		if active {
			t.Fatal("live selection held interactive SQL transaction")
		}
		return fetcher.Result{Status: 200, MediaType: "application/json", FinalURL: source.Endpoint, Body: append(append([]byte(`{"results":[`), raw...), []byte("]}")...), PolicyID: source.PolicyID, PolicyRevision: source.PolicyRevision, PolicyDigest: source.PolicyDigest}, nil
	})
	clk := clock.NewFake(now)
	manager, err := federation.NewManager(db.federation, peer, clk, id.Generator{}, federation.WithTrustVerifier(trustSelectionPolicy{}), federation.WithEvidenceSink(db.apiJobs.repository, true))
	if err != nil {
		t.Fatal(err)
	}
	service, err := jobs.NewService(db.apiJobs, clk, id.Generator{}, jobs.WithAuditEvidence(), jobs.WithTargetValidation(manager.ValidateTrustJob), jobs.WithEnqueueValidation(manager.ValidateTrustEnqueue))
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{TenantID: housekeepingTenantA, Subject: "trust-admin", Roles: []identity.Role{identity.RoleTenantAdmin}}
	ctx := postgresFixtureContext(t, db, t.Context(), actor)
	resolved, _ := identity.FromContext(ctx)
	coordinator, err := NewPostgresIdempotencyStore(db.apiJobs.repository)
	if err != nil {
		t.Fatal(err)
	}
	scope := idempotency.Scope{TenantID: housekeepingTenantA, Actor: resolved.MembershipID, Route: "POST /api/v1/federation/sources/" + source.ID + "/trust-assessments", Key: "trust-sql-request", AuthorizationDigest: audit.DigestBytes([]byte("fixture authorization"))}
	input := federation.TrustRequest{SourceRevision: 1, Identifier: "urn:air:peer.example:agents:one", MediaType: "application/a2a-agent-card+json", Delivery: "url", ExactEntryDigest: audit.DigestBytes(raw), Text: "one", Reason: "Assess exact registration"}
	execute := func(ctx context.Context) idempotency.Response {
		p, e := manager.PrepareTrustAssessment(ctx, source.ID, input)
		if e != nil {
			t.Fatal(e)
		}
		payload, e := json.Marshal(p)
		if e != nil {
			t.Fatal(e)
		}
		job, _, e := service.Enqueue(ctx, jobs.EnqueueInput{Kind: jobs.TrustKind, PayloadVersion: 1, Payload: payload, DeduplicationKey: "owned-trust", TrustReason: input.Reason})
		if e != nil {
			t.Fatal(e)
		}
		body, e := json.Marshal(jobs.PublicView(job))
		if e != nil {
			t.Fatal(e)
		}
		return idempotency.Response{Digest: "same-request", Status: 202, Body: body}
	}
	first, err := coordinator.Execute(ctx, scope, "same-request", now.Add(time.Hour), execute)
	if err != nil || first.Replayed || first.Response.Status != 202 {
		t.Fatalf("first command=%+v err=%v", first, err)
	}
	replay, err := coordinator.Execute(ctx, scope, "same-request", now.Add(time.Hour), func(context.Context) idempotency.Response {
		t.Fatal("replay repeated selection")
		return idempotency.Response{}
	})
	if err != nil || !replay.Replayed || !bytes.Equal(first.Response.Body, replay.Response.Body) || calls != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, calls, err)
	}
	var count int
	if err = db.operator.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE tenant_id=$1 AND kind=$2`, housekeepingTenantA, jobs.TrustKind).Scan(&count); err != nil || count != 1 {
		t.Fatal("replay duplicated work", err)
	}
	// Use retained PostgreSQL authority, including the collection decision map.
	page, err := service.List(ctx, jobs.ListRequest{Limit: 1, Targets: jobs.ListTargets{Enforced: true}})
	if err != nil || len(page) != 1 || page[0].Kind != jobs.TrustKind || page[0].Result != nil {
		t.Fatalf("authorized private-family page=%+v err=%v", page, err)
	}
	reader := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "trust-reader", Roles: []identity.Role{identity.RoleReader}})
	if _, err = service.PrivateTrustJob(reader, page[0].ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("reader accessed private Job: %v", err)
	}
	readPage, err := service.List(reader, jobs.ListRequest{Limit: 1, Targets: jobs.ListTargets{Enforced: true}})
	if err != nil || len(readPage) != 0 {
		t.Fatalf("reader private-family page=%+v err=%v", readPage, err)
	}
	claimed, err := db.workerJobs.Claim(t.Context(), "trust-retry-owner", now, time.Minute)
	if err != nil || claimed.ID != page[0].ID {
		t.Fatalf("retry fixture claim=%+v err=%v", claimed, err)
	}
	if _, err = db.workerJobs.Fail(t.Context(), housekeepingTenantA, claimed.ID, claimed.LeaseOwner, now, jobs.FailurePermanentUpstream, "Owned permanent failure", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Retry(reader, claimed.ID, "Retry private assessment"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("reader retried private work: %v", err)
	}
	child, err := service.Retry(ctx, claimed.ID, "Retry private assessment")
	if err != nil || child.ParentJobID != claimed.ID || calls != 1 {
		t.Fatalf("retry repeated selection or lost parent: %+v calls=%d err=%v", child, calls, err)
	}
	source.Status, source.Revision, source.UpdatedAt = federation.SourceRemoved, 2, now.Add(time.Microsecond)
	if _, err = db.federation.UpdateSource(ctx, source, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Retry(ctx, claimed.ID, "Retry stale assessment"); err == nil || calls != 1 {
		t.Fatal("removed source was eligible for retry", err)
	}
	if _, err = service.Cancel(reader, child.ID, "Cancel private assessment"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("reader cancelled private work: %v", err)
	}
	cancelled, err := service.Cancel(ctx, child.ID, "Cancel removed-source assessment")
	if err != nil || cancelled.State != jobs.StateCancelled {
		t.Fatalf("removed-source work could not be cancelled: %+v err=%v", cancelled, err)
	}
}
