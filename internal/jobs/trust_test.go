package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func trustJobFixture(t *testing.T) (Job, TrustPayload, TrustAssessment, audit.Record) {
	t.Helper()
	entry := []byte(`{ "identifier":"urn:air:peer.example:agents:one", "type":"application/a2a-agent-card+json", "url":"https://peer.example/card", "score":100, "source":"https://peer.example/search" }`)
	p := TrustPayload{SourceID: "00000000-0000-7000-8000-000000000010", SourceRevision: 1, SourcePolicyDigest: "sha256:" + strings.Repeat("a", 64), PolicyName: "owned", PolicyDigest: "sha256:" + strings.Repeat("b", 64), Identifier: "urn:air:peer.example:agents:one", MediaType: "application/a2a-agent-card+json", Delivery: "url", ExactEntryDigest: audit.DigestBytes(entry), Entry: entry}
	raw, marshalErr := json.Marshal(p)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	job := Job{ID: "00000000-0000-7000-8000-000000000011", TenantID: "00000000-0000-7000-8000-000000000001", Actor: "00000000-0000-7000-8000-000000000012", Kind: TrustKind, PayloadVersion: 1, Payload: raw, Attempt: 1}
	na := anstrust.Check{State: "not_assessed", Code: "prerequisite_failed"}
	result := TrustAssessment{SourceID: p.SourceID, SourceRevision: 1, PolicyName: p.PolicyName, PolicyDigest: p.PolicyDigest, Authority: "owned", ExactEntryDigest: p.ExactEntryDigest, MediaType: p.MediaType, Delivery: p.Delivery, SoftwareTrust: "not_assessed", ArtifactIntegrity: "not_assessed", Rights: "not_assessed", Evidence: anstrust.Bundle{Profile: anstrust.Profile, Entry: entry, Receipt: []byte{}, Checkpoint: []byte{}, StatusToken: []byte{}}, Assessment: anstrust.Result{Profile: anstrust.Profile, PolicyDigest: strings.Repeat("b", 64), EntryDigest: strings.TrimPrefix(p.ExactEntryDigest, "sha256:"), CheckedAt: time.Date(2026, 10, 9, 14, 31, 24, 0, time.UTC), Receipt: anstrust.Check{State: "unavailable", Code: "receipt_missing"}, Producer: na, Log: na, Checkpoint: na, Inclusion: na, Target: na, Status: na}}
	raw, marshalErr = json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	facts, _ := audit.Facts(map[string]any{"sourceId": p.SourceID, "sourceRevision": 1, "entryDigest": p.ExactEntryDigest, "policyDigest": p.PolicyDigest, "resultDigest": audit.DigestBytes(raw), "currentRegistration": false, "jobId": job.ID, "jobKind": job.Kind, "jobAttempt": 1, "initiator": job.Actor})
	record, err := audit.NewRecord("00000000-0000-7000-8000-000000000031", job.TenantID, audit.Principal{ID: audit.PrincipalIDCatalogWorker, Type: audit.PrincipalServiceExecutor}, audit.ActionSourceTrustCompleted, "job", job.ID, "", audit.Execution{}, facts, result.Assessment.CheckedAt)
	if err != nil {
		t.Fatal(err)
	}
	return job, p, result, record
}

func TestTrustPayloadAdmission(t *testing.T) {
	for _, name := range []string{"valid", "kind", "schema", "unknown", "null", "payload limit", "oversized", "source", "revision", "policy", "policy digest", "policy hex", "source digest", "credential", "entry absent", "entry at limit", "entry limit", "entry digest", "identifier", "identifier mismatch", "media mismatch", "delivery mismatch", "version absent", "version empty", "version limit", "media limit", "delivery", "entry schema"} {
		t.Run(name, func(t *testing.T) {
			job, p, _, _ := trustJobFixture(t)
			switch name {
			case "kind":
				job.Kind = "source.sync"
			case "schema":
				job.PayloadVersion = 2
			case "source":
				p.SourceID = "bad"
			case "revision":
				p.SourceRevision = 0
			case "policy":
				p.PolicyName = ""
			case "policy digest":
				p.PolicyDigest = "bad"
			case "policy hex":
				p.PolicyDigest = "sha256:" + strings.Repeat("z", 64)
			case "source digest":
				p.SourcePolicyDigest = "SHA256:" + strings.Repeat("a", 64)
			case "credential":
				p.CredentialRevision = strings.Repeat("a", 513)
			case "entry absent":
				p.Entry = nil
			case "entry at limit", "entry limit":
				length := anstrust.MaxEntryBytes
				if name == "entry limit" {
					length++
				}
				p.Entry = append(bytes.Repeat([]byte(" "), length-len(p.Entry)), p.Entry...)
				p.ExactEntryDigest = audit.DigestBytes(p.Entry)
			case "entry digest":
				p.ExactEntryDigest = p.PolicyDigest
			case "identifier":
				p.Identifier = "not-an-air"
			case "identifier mismatch":
				p.Identifier = "urn:air:peer.example:agents:other"
			case "media mismatch":
				p.MediaType = "application/mcp-server-card+json"
			case "delivery mismatch":
				p.Delivery = "data"
			case "version absent":
				p.Version = "1"
			case "version empty":
				p.VersionPresent = true
			case "version limit":
				p.Version = strings.Repeat("x", 129)
			case "media limit":
				p.MediaType = strings.Repeat("x", 256)
			case "delivery":
				p.Delivery = "archive"
			case "entry schema":
				p.Entry = []byte(`{"identifier":"bad"}`)
				p.ExactEntryDigest = audit.DigestBytes(p.Entry)
			}
			raw, marshalErr := json.Marshal(p)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			job.Payload = raw
			switch name {
			case "unknown":
				job.Payload = append([]byte(nil), raw[:len(raw)-1]...)
				job.Payload = append(job.Payload, []byte(`,"extra":true}`)...)
			case "null":
				job.Payload = bytes.Replace(raw, []byte(`"credentialRevision":""`), []byte(`"credentialRevision":null`), 1)
			case "payload limit", "oversized":
				length := MaxTrustRecordBytes
				if name == "oversized" {
					length++
				}
				job.Payload = append(bytes.Repeat([]byte(" "), length-len(raw)), raw...)
			}
			got, err := DecodeTrustPayload(job)
			if name == "valid" || name == "payload limit" || name == "entry at limit" {
				if err != nil || string(got.Entry) != string(p.Entry) {
					t.Fatal(err)
				}
				if ValidateActionablePayload(job) != nil {
					t.Fatal("restore payload rejected")
				}
			} else if err == nil {
				t.Fatal("invalid payload accepted")
			}
		})
	}
}

func TestTrustPayloadNativeVersionValues(t *testing.T) {
	for _, length := range []int{0, 128, 129} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			job, p, _, _ := trustJobFixture(t)
			p.VersionPresent = true
			p.Version = strings.Repeat("\u00e9", length)
			version, err := json.Marshal(p.Version)
			if err != nil {
				t.Fatal(err)
			}
			p.Entry = bytes.Replace(p.Entry, []byte(`"identifier":`), append(append([]byte(`"version":`), version...), []byte(`,"identifier":`)...), 1)
			p.ExactEntryDigest = audit.DigestBytes(p.Entry)
			job.Payload, err = json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := DecodeTrustPayload(job)
			if length <= 128 {
				if err != nil || !selected.VersionPresent || selected.Version != p.Version {
					t.Fatal("valid native version changed or was rejected", err)
				}
			} else if err == nil {
				t.Fatal("over-limit version accepted")
			}
		})
	}
}

func TestTrustCompletionBinding(t *testing.T) {
	for _, name := range []string{"valid", "payload", "unknown", "result limit", "oversized", "source", "revision", "policy", "entry", "digest", "media", "delivery", "assessment digest", "checked time", "software trust", "artifact integrity", "rights", "bundle", "audit action", "audit tenant", "audit target", "audit facts", "audit digest", "audit outcome", "authority", "profile", "verifier policy", "check code", "check state", "false current", "missing validity"} {
		t.Run(name, func(t *testing.T) {
			job, p, r, record := trustJobFixture(t)
			switch name {
			case "authority":
				r.Authority = ""
			case "profile":
				r.Assessment.Profile = "other"
			case "verifier policy":
				r.Assessment.PolicyDigest = "bad"
			case "check code":
				r.Assessment.Receipt.Code = ""
			case "check state":
				r.Assessment.Receipt.State = "trusted"
			case "false current":
				r.Assessment.CurrentRegistration = true
			case "missing validity":
				r.Assessment.CurrentRegistration = true
				valid := anstrust.Check{State: "valid", Code: "valid"}
				r.Assessment.Receipt, r.Assessment.Producer, r.Assessment.Log, r.Assessment.Checkpoint, r.Assessment.Inclusion, r.Assessment.Target, r.Assessment.Status = valid, valid, valid, valid, valid, valid, valid
			case "payload":
				job.Payload = []byte(`{}`)
			case "source":
				r.SourceID = "changed"
			case "revision":
				r.SourceRevision++
			case "policy":
				r.PolicyDigest = "changed"
			case "entry":
				r.Evidence.Entry = []byte(`{}`)
			case "digest":
				r.ExactEntryDigest = "changed"
			case "media":
				r.MediaType = "changed"
			case "delivery":
				r.Delivery = "changed"
			case "assessment digest":
				r.Assessment.EntryDigest = "changed"
			case "checked time":
				r.Assessment.CheckedAt = time.Time{}
			case "software trust":
				r.SoftwareTrust = "trusted"
			case "artifact integrity":
				r.ArtifactIntegrity = "valid"
			case "rights":
				r.Rights = "approved"
			case "bundle":
				r.Evidence.Receipt = make([]byte, anstrust.MaxReceiptBytes+1)
			case "audit action":
				record.Action = audit.ActionSourceTrustRequested
			case "audit tenant":
				record.TenantID = p.SourceID
			case "audit target":
				record.TargetID = p.SourceID
			case "audit facts":
				facts, ok := record.Details["facts"].(map[string]any)
				if !ok {
					t.Fatal("missing audit facts")
				}
				facts["policyDigest"] = "changed"
			case "audit digest":
				facts, ok := record.Details["facts"].(map[string]any)
				if !ok {
					t.Fatal("missing audit facts")
				}
				facts["resultDigest"] = "sha256:" + strings.Repeat("f", 64)
			case "audit outcome":
				facts, ok := record.Details["facts"].(map[string]any)
				if !ok {
					t.Fatal("missing audit facts")
				}
				facts["currentRegistration"] = true
			}
			raw, marshalErr := json.Marshal(r)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			switch name {
			case "unknown":
				raw = append(raw[:len(raw)-1], []byte(`,"extra":true}`)...)
			case "result limit", "oversized":
				length := MaxTrustRecordBytes
				if name == "oversized" {
					length++
				}
				raw = append(bytes.Repeat([]byte(" "), length-len(raw)), raw...)
			}
			// Keep audit binding valid for result cases so an unrelated digest or
			// outcome mismatch cannot conceal a missing result-validation guard.
			if !strings.HasPrefix(name, "audit ") {
				facts, ok := record.Details["facts"].(map[string]any)
				if !ok {
					t.Fatal("missing audit facts")
				}
				facts["resultDigest"] = audit.DigestBytes(raw)
				facts["currentRegistration"] = r.Assessment.CurrentRegistration
			}
			_, decodeErr := DecodeTrustAssessment(p, raw)
			wantReadable := name == "valid" || name == "result limit" || name == "payload" || strings.HasPrefix(name, "audit ")
			if (decodeErr == nil) != wantReadable {
				t.Fatalf("private result validation: %v", decodeErr)
			}
			err := ValidateTrustCompletion(job, raw, record)
			if name == "valid" || name == "result limit" {
				if err != nil {
					t.Fatal(err)
				}
				if r.JobCompletionEvidence().ID != "" {
					t.Fatal("unexpected serialized audit")
				}
			} else if err == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
}

func TestTrustMemoryCompletionRequiresAtomicEvidence(t *testing.T) {
	for _, scenario := range []string{"complete", "cancelled", "missing evidence", "missing sink", "audit failure", "wrong result"} {
		t.Run(scenario, func(t *testing.T) {
			job, _, assessment, record := trustJobFixture(t)
			now := assessment.Assessment.CheckedAt
			until := now.Add(time.Minute)
			job.State, job.LeaseOwner, job.LeaseExpiresAt = StateRunning, "owned", &until
			sink := &recordingJobEvidence{}
			store := NewMemoryStore(sink)
			store.jobs[job.ID] = cloneJob(job)
			evidence := []audit.Record{record}
			switch scenario {
			case "cancelled":
				job.CancellationRequested = true
				store.jobs[job.ID] = cloneJob(job)
			case "missing evidence":
				evidence = nil
			case "missing sink":
				store.evidence = nil
			case "audit failure":
				store.evidence = failingJobEvidence{errors.New("injected audit failure")}
			case "wrong result":
				assessment.SourceRevision++
			}
			raw, err := json.Marshal(assessment)
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.Complete(t.Context(), job.TenantID, job.ID, "owned", now, raw, evidence...)
			retained := store.jobs[job.ID]
			if scenario == "complete" {
				if err != nil || got.State != StateSucceeded || !bytes.Equal(got.Result, raw) || len(sink.records) != 1 {
					t.Fatalf("completion=%+v audit=%d err=%v", got, len(sink.records), err)
				}
				got.Result[0] = 'x'
				if !bytes.Equal(store.jobs[job.ID].Result, raw) {
					t.Fatal("returned result aliases retained evidence")
				}
			} else if err == nil || retained.State != StateRunning || len(retained.Result) != 0 || len(sink.records) != 0 {
				t.Fatalf("rejected completion changed state or audit: %+v err=%v", retained, err)
			}
		})
	}
}

func TestTrustServicePrivateReadsAndExecutionAdmission(t *testing.T) {
	job, _, _, _ := trustJobFixture(t)
	actor := identity.Actor{TenantID: job.TenantID, Subject: "admin", MembershipID: job.Actor, RealmID: "00000000-0000-7000-8000-000000000013", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	sink := &recordingJobEvidence{}
	store := NewMemoryStore(sink)
	eligible, disclose, validations := true, true, 0
	service, err := NewService(store, clock.NewFake(time.Date(2026, 10, 9, 14, 31, 24, 0, time.UTC)), id.Generator{}, WithAuditEvidence(), WithTargetValidation(func(context.Context, Job) error {
		if !disclose {
			return catalog.E(catalog.CodeForbidden, "private evidence is unavailable")
		}
		return nil
	}), WithEnqueueValidation(func(context.Context, Job) error {
		validations++
		if !eligible {
			return catalog.E(catalog.CodePreconditionFailed, "source changed")
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	input := EnqueueInput{Kind: TrustKind, PayloadVersion: 1, Payload: job.Payload, DeduplicationKey: "owned", TrustReason: "Assess selected registration"}
	first, reused, err := service.Enqueue(ctx, input)
	if err != nil || reused || len(sink.records) != 1 || sink.records[0].Action != audit.ActionSourceTrustRequested {
		t.Fatalf("request audit: job=%+v reused=%v err=%v", first, reused, err)
	}
	second, reused, err := service.Enqueue(ctx, input)
	if err != nil || !reused || second.ID != first.ID || len(sink.records) != 2 {
		t.Fatalf("active deduplication lost audit binding: reused=%v err=%v", reused, err)
	}
	facts, ok := sink.records[1].Details["facts"].(map[string]any)
	if !ok || facts["deduplicated"] != true || facts["jobId"] != first.ID {
		t.Fatal("deduplicated request did not identify its retained Job")
	}
	if _, err = service.PrivateTrustJob(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	reader := actor
	reader.Roles = []identity.Role{identity.RoleReader}
	if _, _, err = service.Enqueue(identityfixture.WithActor(t.Context(), reader), input); catalog.ErrorCodeOf(err) != catalog.CodeForbidden || validations != 2 {
		t.Fatal("Reader crossed execution admission", err)
	}
	if _, err = service.PrivateTrustJob(identityfixture.WithActor(t.Context(), reader), first.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("Reader disclosed private Job", err)
	}
	failed := store.jobs[first.ID]
	failed.State = StateFailed
	store.jobs[first.ID] = failed
	eligible = false
	if _, err = service.Retry(ctx, first.ID, "Retry selected registration"); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || len(store.jobs) != 1 {
		t.Fatal("stale work was retried", err)
	}
	if _, err = service.PrivateTrustJob(ctx, first.ID); err != nil {
		t.Fatal("stale eligibility prevented historical access", err)
	}
	eligible = true
	retry, err := service.Retry(ctx, first.ID, "Retry selected registration")
	if err != nil || retry.ParentJobID != first.ID || validations != 4 {
		t.Fatalf("retry eligibility was not checked: job=%+v validations=%d err=%v", retry, validations, err)
	}
	disclose = false
	if _, err = service.PrivateTrustJob(ctx, first.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("private read bypassed current target permission", err)
	}
	if _, err = service.Retry(ctx, first.ID, "Retry selected registration"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden || validations != 4 {
		t.Fatal("retry bypassed disclosure admission", err)
	}
	if _, err = service.PrivateTrustJob(ctx, "00000000-0000-7000-8000-000000000099"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("missing private Job did not return NotFound", err)
	}
	if _, err = NewService(store, service.clock, service.ids, WithEnqueueValidation(nil)); err == nil {
		t.Fatal("nil execution validator was accepted")
	}
	service.targetValidation = func(_ context.Context, replacement Job) error {
		if replacement.ParentJobID != "" {
			return catalog.E(catalog.CodeForbidden, "replacement target denied")
		}
		return nil
	}
	if _, err = service.Retry(ctx, first.ID, "Retry selected registration"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden || len(store.jobs) != 2 {
		t.Fatal("retry did not validate the replacement target", err)
	}
	ordinary := failed
	ordinary.Kind = "source.sync"
	store.jobs[first.ID] = ordinary
	service.targetValidation = nil
	if _, err = service.PrivateTrustJob(ctx, first.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("ordinary Job was treated as private trust evidence", err)
	}
}

func TestTrustRequestsRequirePayloadEligibilityAndAuditReason(t *testing.T) {
	for _, scenario := range []string{"malformed payload", "missing reason", "changed source"} {
		t.Run(scenario, func(t *testing.T) {
			job, _, _, _ := trustJobFixture(t)
			ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: job.TenantID, Subject: "admin", MembershipID: job.Actor, RealmID: "00000000-0000-7000-8000-000000000013", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
			sink := &recordingJobEvidence{}
			store := NewMemoryStore(sink)
			service, err := NewService(store, clock.NewFake(time.Date(2026, 10, 9, 14, 31, 24, 0, time.UTC)), id.Generator{}, WithAuditEvidence(), WithEnqueueValidation(func(context.Context, Job) error {
				if scenario == "changed source" {
					return catalog.E(catalog.CodePreconditionFailed, "source changed")
				}
				return nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			input := EnqueueInput{Kind: TrustKind, PayloadVersion: 1, Payload: job.Payload, DeduplicationKey: "owned", TrustReason: "Assess selected registration"}
			if scenario == "malformed payload" {
				input.Payload = []byte(`{}`)
			} else if scenario == "missing reason" {
				input.TrustReason = ""
			}
			if _, _, err = service.Enqueue(ctx, input); err == nil || len(store.jobs) != 0 || len(sink.records) != 0 {
				t.Fatalf("invalid request retained work or evidence: jobs=%d audit=%d err=%v", len(store.jobs), len(sink.records), err)
			}
		})
	}
}

func TestTrustInlineNativeSelection(t *testing.T) {
	_, p, _, _ := trustJobFixture(t)
	for _, scenario := range []string{"inline", "sparse", "wrong case type"} {
		t.Run(scenario, func(t *testing.T) {
			raw := []byte(`{"identifier":"urn:air:peer.example:agents:one","type":"application/a2a-agent-card+json","data":{"name":"Agent"}}`)
			if scenario == "sparse" {
				raw = []byte(`{"identifier":"urn:air:peer.example:agents:one","type":"application/a2a-agent-card+json"}`)
			} else if scenario == "wrong case type" {
				raw = bytes.Replace(raw, []byte(`"type"`), []byte(`"Type"`), 1)
			}
			selected := p
			selected.Delivery, selected.Entry, selected.ExactEntryDigest = "data", raw, audit.DigestBytes(raw)
			if matches := TrustEntryMatches(raw, selected); matches != (scenario == "inline") {
				t.Fatalf("native selection matched=%v", matches)
			}
		})
	}
}

func TestTrustRequestEvidenceRejectsChangedBindings(t *testing.T) {
	for _, scenario := range []string{"source revision", "entry digest", "terminal reuse", "actor", "payload"} {
		t.Run(scenario, func(t *testing.T) {
			job, p, _, record := trustJobFixture(t)
			job.State = StateQueued
			record.Action, record.TargetType, record.TargetID, record.Actor = audit.ActionSourceTrustRequested, "job", job.ID, job.Actor
			record.Details["principalType"], record.Details["actorType"] = audit.PrincipalCatalogMembership, "human"
			record.Details["realmId"], record.Reason = "00000000-0000-7000-8000-000000000013", "Assess selected registration"
			facts, err := audit.Facts(map[string]any{"sourceId": p.SourceID, "sourceRevision": p.SourceRevision, "entryDigest": p.ExactEntryDigest, "policyDigest": p.PolicyDigest, "jobId": job.ID, "deduplicated": false})
			if err != nil {
				t.Fatal(err)
			}
			record.Details["facts"] = facts
			if _, err = FinalizeEnqueueEvidence(record, job, false); err != nil {
				t.Fatal("valid control was rejected", err)
			}
			existing := false
			switch scenario {
			case "source revision":
				facts["sourceRevision"] = 2
			case "entry digest":
				facts["entryDigest"] = p.PolicyDigest
			case "terminal reuse":
				existing, job.State = true, StateSucceeded
			case "actor":
				job.Actor = "00000000-0000-7000-8000-000000000099"
			case "payload":
				job.Payload = []byte(`{}`)
			}
			if _, err = FinalizeEnqueueEvidence(record, job, existing); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatal("changed enqueue evidence was accepted", err)
			}
		})
	}
}
