//go:build integration

package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
)

func TestPostgresValidationDiagnosticsAndRetainedEvidence(t *testing.T) {
	db := newHousekeepingDatabase(t)
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	roles := []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RoleReader}
	principal, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.diagnostics.example", TenancyMode: "dedicated", Subject: "diagnostic-author", ActorType: identity.ActorHuman, Roles: roles, Operator: "diagnostic-test", Reason: "synthetic diagnostic fixture", AcknowledgeOutbox: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "diagnostic-author", Roles: roles, MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: principal.ActorType})
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := clock.NewFake(now)
	key := sha256.Sum256([]byte(t.Name()))
	newService := func(repository catalog.Repository, limits catalog.DiagnosticLimits) *catalog.Service {
		t.Helper()
		validator, e := adapter.NewRegistryWithLimits(limits)
		if e != nil {
			t.Fatal(e)
		}
		s, e := catalog.NewService(catalog.Options{Repository: repository, Clock: c, IDs: id.Generator{}, Validator: validator, DiagnosticLimits: limits, CursorKey: key[:], Policy: "diagnostic-test"})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	service := newService(repo, catalog.DefaultDiagnosticLimits())
	ns, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "diagnostics", DisplayName: "Diagnostics"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: ns.ID, CanonicalIdentifier: "urn:air:catalog.example.com:diagnostics:agent", Kind: catalog.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	card := `{"name":"Agent","description":"Fixture","version":"1.0.0","supportedInterfaces":[{"url":"https://agent.example.com/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[` + strings.Repeat(`{},`, 49999) + `{}]}`
	input := catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: json.RawMessage(card)}}}
	store, err := NewPostgresIdempotencyStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	scope := idempotency.Scope{TenantID: housekeepingTenantA, Actor: "diagnostic-author", Route: "POST /api/v1/resources/{resourceId}/versions", Key: "diagnostic-version", AuthorizationDigest: digestText("diagnostic-authority")}
	var version catalog.ResourceVersion
	var revision catalog.VersionRevision
	var evidence catalog.EvidenceSet
	var commandErr error
	execution, err := store.Execute(ctx, scope, "diagnostic-request", now.Add(time.Hour), func(commandCtx context.Context) idempotency.Response {
		version, revision, evidence, commandErr = service.CreateVersion(commandCtx, resource.ID, input)
		if commandErr != nil {
			idempotency.AbortCommand(commandCtx, commandErr)
			return idempotency.Response{Status: 500}
		}
		body, marshalErr := json.Marshal(map[string]any{"version": version, "revision": revision, "evidence": evidence})
		if marshalErr != nil {
			commandErr = marshalErr
			idempotency.AbortCommand(commandCtx, marshalErr)
		}
		return idempotency.Response{Digest: "diagnostic-request", Status: 201, Body: body}
	})
	if err != nil || commandErr != nil {
		t.Fatalf("create: %v / %v", err, commandErr)
	}
	if len(execution.Response.Body) > 512<<10 || evidence.State != catalog.EvidenceBlocked || len(evidence.Findings) != 128 {
		t.Fatalf("amplified/accepted invalid revision: bytes=%d state=%s count=%d", len(execution.Response.Body), evidence.State, len(evidence.Findings))
	}
	if err := catalog.ValidateEvidenceSetIntegrity(evidence); err != nil {
		t.Fatal(err)
	}
	counts := func() [5]int {
		t.Helper()
		var result [5]int
		if e := db.operator.QueryRow(ctx, `SELECT (SELECT count(*) FROM resource_version_revisions WHERE tenant_id=$1), (SELECT count(*) FROM audit_events WHERE tenant_id=$1), (SELECT count(*) FROM outbox_events WHERE tenant_id=$1), (SELECT count(*) FROM evidence_sets WHERE tenant_id=$1), (SELECT count(*) FROM version_validation_runs WHERE tenant_id=$1)`, housekeepingTenantA).Scan(&result[0], &result[1], &result[2], &result[3], &result[4]); e != nil {
			t.Fatal(e)
		}
		return result
	}
	before := counts()
	replay, err := store.Execute(ctx, scope, "diagnostic-request", now.Add(time.Hour), func(context.Context) idempotency.Response {
		t.Error("replay executed command")
		return idempotency.Response{}
	})
	if err != nil || !replay.Replayed || !bytes.Equal(replay.Response.Body, execution.Response.Body) || counts() != before {
		t.Fatalf("replay changed response/effects: %v", err)
	}
	low := newService(repo, catalog.DiagnosticLimits{MaxFindings: 2, MaxDiagnosticBytes: 1024})
	stored, retained, _, err := low.GetVersionRevision(ctx, revision.ID)
	if err != nil || !reflect.DeepEqual(stored.Findings, revision.Findings) || retained.Digest != evidence.Digest || !reflect.DeepEqual(retained.Findings, evidence.Findings) {
		t.Fatalf("lower budget altered retained evidence: %v", err)
	}
	if err := catalog.ValidateEvidenceSetIntegrity(retained); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.GetVersionRevision(t.Context(), housekeepingTenantB, revision.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant read: %v", err)
	}
	c.Advance(time.Second)
	_, fresh, _, err := low.RevalidateVersionRevision(ctx, revision.ID)
	if err != nil || fresh.ID == evidence.ID || fresh.State != catalog.EvidenceBlocked || len(fresh.Findings) != 2 || fresh.Findings[1].Code != catalog.DiagnosticBudgetExhausted {
		t.Fatalf("revalidate: state=%s count=%d err=%v", fresh.State, len(fresh.Findings), err)
	}
	if err := catalog.ValidateEvidenceSetIntegrity(fresh); err != nil {
		t.Fatal(err)
	}
	old, err := repo.GetValidationEvidence(ctx, housekeepingTenantA, revision.ID, evidence.ID)
	if err != nil || old.Digest != evidence.Digest || !reflect.DeepEqual(old.Findings, evidence.Findings) {
		t.Fatalf("revalidation rewrote old evidence: %v", err)
	}
	// Failure after a successful mutation must roll back version, evidence,
	// audit and outbox together, retaining the terminal failure for replay.
	before = counts()
	failure := errors.New("synthetic response failure")
	scope.Key = "diagnostic-rollback"
	rollbackInput := append([]catalog.RepresentationInput(nil), input.Representations...)
	rollbackInput[0].Data = json.RawMessage(strings.Replace(card, `"name":"Agent"`, `"name":"Changed"`, 1))
	aborted, err := store.Execute(ctx, scope, "rollback-request", now.Add(time.Hour), func(commandCtx context.Context) idempotency.Response {
		_, _, _, commandErr = low.CreateVersionRevision(commandCtx, version.ID, rollbackInput, version.Revision)
		idempotency.AbortCommand(commandCtx, failure)
		return idempotency.Response{Digest: "rollback-request", Status: 500}
	})
	if err != nil || commandErr != nil || aborted.Response.Status != 500 || counts() != before {
		t.Fatalf("rollback lost atomicity: %v / %v", err, commandErr)
	}
	failedReplay, err := store.Execute(ctx, scope, "rollback-request", now.Add(time.Hour), func(context.Context) idempotency.Response {
		t.Error("aborted command replay executed")
		return idempotency.Response{}
	})
	if err != nil || !failedReplay.Replayed || failedReplay.Response.Status != 500 || counts() != before {
		t.Fatalf("failed replay: %v", err)
	}

	// A valid large document under a tight diagnostic budget is still supported.
	valid := strings.Replace(card, `"skills":[`+strings.Repeat(`{},`, 49999)+`{}]`, `"skills":[{"id":"valid","name":"Valid","description":"`+strings.Repeat("x", 1<<20)+`","tags":["fixture"]}]`, 1)
	validInputs := []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: json.RawMessage(valid)}}
	_, goodRevision, good, err := low.CreateVersionRevision(ctx, version.ID, validInputs, version.Revision)
	if err != nil || good.State != catalog.EvidenceComplete || len(good.Findings) != 0 {
		t.Fatalf("valid large document rejected: %v", err)
	}
	if err := catalog.ValidateEvidenceSetIntegrity(good); err != nil {
		t.Fatal(err)
	}
	read, _, _, err := low.GetVersionRevision(ctx, goodRevision.ID)
	if err != nil || read.Representations[0].ContentDigest != goodRevision.Representations[0].ContentDigest {
		t.Fatalf("valid large detail: %v", err)
	}
	t.Logf("bounded PostgreSQL response=%d bytes; stored/replayed findings=%d; new lower-budget findings=%d", len(execution.Response.Body), len(evidence.Findings), len(fresh.Findings))
	t.Run("budget change does not invalidate completed evidence", func(t *testing.T) {
		mcp, e := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: ns.ID, CanonicalIdentifier: "urn:air:catalog.example.com:diagnostics:mcp", Kind: catalog.KindMCPServer})
		if e != nil {
			t.Fatal(e)
		}
		_, priorRevision, priorEvidence, e := service.CreateVersion(ctx, mcp.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaMCPRegistry, Data: json.RawMessage(`{"name":"io.example/server","description":"Example","version":"1.0.0"}`)}}})
		if e != nil || priorEvidence.State != catalog.EvidenceComplete || len(priorEvidence.Findings) != 2 {
			t.Fatalf("warning-only valid control: %v / %+v", e, priorEvidence.Findings)
		}
		one := newService(repo, catalog.DiagnosticLimits{MaxFindings: 1, MaxDiagnosticBytes: 1024})
		_, readEvidence, _, e := one.GetVersionRevision(ctx, priorRevision.ID)
		if e != nil || readEvidence.State != catalog.EvidenceComplete || readEvidence.Digest != priorEvidence.Digest || !reflect.DeepEqual(readEvidence.Findings, priorEvidence.Findings) {
			t.Fatalf("configuration change invalidated history: %v", e)
		}
		_, limited, _, e := one.RevalidateVersionRevision(ctx, priorRevision.ID)
		if e != nil || limited.State != catalog.EvidenceBlocked || len(limited.Findings) != 1 || limited.Findings[0].Code != catalog.DiagnosticBudgetExhausted {
			t.Fatalf("explicit new validation exceeded policy: %v / %+v", e, limited.Findings)
		}
		retained, e := repo.GetValidationEvidence(ctx, housekeepingTenantA, priorRevision.ID, priorEvidence.ID)
		if e != nil || retained.State != catalog.EvidenceComplete || retained.Digest != priorEvidence.Digest {
			t.Fatalf("new validation rewrote completed evidence: %v", e)
		}
	})
	// Simulate reports admitted before the producer budgets. Insert immutable
	// rows once through the real repository; never disable guards or rewrite them.
	legacy := &legacyDiagnosticRepository{Repository: repo}
	legacyService := newService(legacy, catalog.DefaultDiagnosticLimits())
	for i, target := range []string{"all", "revision", "representation", "evidence"} {
		t.Run("historical "+target, func(t *testing.T) {
			legacy.target = target
			legacy.findings = []catalog.Finding{{Code: "OLD_INVALID", Severity: catalog.SeverityError, Message: strings.Repeat("x", 32<<20)}}
			if target == "all" {
				legacy.findings = make([]catalog.Finding, 50000)
				for j := range legacy.findings {
					legacy.findings[j] = catalog.Finding{Code: "A2A_SKILL_INVALID", Severity: catalog.SeverityError, Path: fmt.Sprintf("/skills/%d", j), Message: "Each A2A Agent Skill requires id, name, description, and at least one tag."}
				}
			}
			legacyInput := catalog.CreateVersionInput{Version: fmt.Sprintf("2.0.%d", i), Representations: validInputs}
			var legacyRevision catalog.VersionRevision
			var legacyEvidence catalog.EvidenceSet
			var e error
			if target == "all" {
				scope.Key = "historical-command"
				legacyExecution, execErr := store.Execute(ctx, scope, "historical-request", now.Add(time.Hour), func(commandCtx context.Context) idempotency.Response {
					_, legacyRevision, legacyEvidence, e = legacyService.CreateVersion(commandCtx, resource.ID, legacyInput)
					if e != nil {
						idempotency.AbortCommand(commandCtx, e)
						return idempotency.Response{Status: 500}
					}
					body, marshalErr := json.Marshal(map[string]any{"revision": legacyRevision, "evidence": legacyEvidence})
					if marshalErr != nil {
						e = marshalErr
						idempotency.AbortCommand(commandCtx, e)
					}
					return idempotency.Response{Digest: "historical-request", Status: 201, Body: body}
				})
				if execErr != nil || e != nil {
					t.Fatalf("historical capture: %v / %v", execErr, e)
				}
				beforeReplay := counts()
				replay, replayErr := store.Execute(ctx, scope, "historical-request", now.Add(time.Hour), func(context.Context) idempotency.Response {
					t.Error("historical replay executed")
					return idempotency.Response{}
				})
				if replayErr != nil || !replay.Replayed || !bytes.Equal(replay.Response.Body, legacyExecution.Response.Body) || counts() != beforeReplay {
					t.Fatalf("historical replay changed: %v", replayErr)
				}
			} else {
				_, legacyRevision, legacyEvidence, e = legacyService.CreateVersion(ctx, resource.ID, legacyInput)
			}
			if e != nil {
				t.Fatal(e)
			}
			_, got, _, e := low.GetVersionRevision(ctx, legacyRevision.ID)
			if target == "all" {
				if e != nil || !reflect.DeepEqual(got.Findings, legacy.findings) || got.Digest != legacyEvidence.Digest {
					t.Fatalf("historical report lost: %v", e)
				}
				if e := catalog.ValidateEvidenceSetIntegrity(got); e != nil {
					t.Fatal(e)
				}
			} else if catalog.ErrorCodeOf(e) != catalog.CodeWorkLimitExceeded {
				t.Fatalf("historical guard: %v", e)
			}
			if target == "evidence" {
				if _, e := repo.GetValidationEvidence(ctx, housekeepingTenantA, legacyRevision.ID, legacyEvidence.ID); catalog.ErrorCodeOf(e) != catalog.CodeWorkLimitExceeded {
					t.Fatalf("exact evidence guard: %v", e)
				}
			}
		})
	}
}

type legacyDiagnosticRepository struct {
	catalog.Repository
	target   string
	findings []catalog.Finding
}

func (r *legacyDiagnosticRepository) CreateVersion(ctx context.Context, p catalog.CreateVersionParams) (catalog.ResourceVersion, catalog.VersionRevision, catalog.EvidenceSet, error) {
	if r.target == "all" || r.target == "revision" {
		p.VersionRevision.Findings = r.findings
	}
	if r.target == "all" || r.target == "representation" {
		p.VersionRevision.Representations[0].Findings = r.findings
		p.VersionRevision.Representations[0].ValidationState = "invalid"
	}
	if r.target == "all" || r.target == "evidence" {
		p.Evidence.Findings = r.findings
		p.Evidence.State = catalog.EvidenceBlocked
		// Independent reproduction of the retained v7 digest contract.
		e := p.Evidence
		raw, err := json.Marshal(map[string]any{"runId": e.ID, "subjectDigest": e.SubjectDigest, "state": e.State, "findings": e.Findings, "validationProfile": e.ValidationProfile, "policyVersion": e.PolicyVersion, "legalRights": e.LegalRights, "trust": e.Trust, "externalRetrievals": []catalog.ExternalRetrieval{}, "createdAt": e.CreatedAt, "expiresAt": nil})
		if err != nil {
			return catalog.ResourceVersion{}, catalog.VersionRevision{}, catalog.EvidenceSet{}, err
		}
		var canonical any
		if err := json.Unmarshal(raw, &canonical); err != nil {
			return catalog.ResourceVersion{}, catalog.VersionRevision{}, catalog.EvidenceSet{}, err
		}
		raw, err = json.Marshal(canonical)
		if err != nil {
			return catalog.ResourceVersion{}, catalog.VersionRevision{}, catalog.EvidenceSet{}, err
		}
		p.Evidence.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	}
	return r.Repository.CreateVersion(ctx, p)
}
