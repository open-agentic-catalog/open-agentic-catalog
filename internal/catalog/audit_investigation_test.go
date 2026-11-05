package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const auditInvestigationTenantID = "00000000-0000-7000-8000-000000000081"

func TestPublisherDomainRevocationAuditRetainsBoundedConsequences(t *testing.T) {
	at := time.Date(2026, time.September, 4, 9, 0, 0, 0, time.UTC)
	record, err := audit.NewRecord("00000000-0000-7000-8000-000000000101", auditInvestigationTenantID, audit.Principal{ID: "catalog-api", Type: audit.PrincipalServiceExecutor}, "publisher.publication_domain_revoked", "publisher", "00000000-0000-7000-8000-000000000201", "authority removed", audit.Execution{}, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	affected := make([]string, 40)
	for index := range affected {
		affected[index] = fmt.Sprintf("00000000-0000-7000-8000-%012d", len(affected)-index)
	}
	completed, err := CompletePublisherDomainRevocationAudit(record, "catalog.example.com", affected)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.ValidateNewRecord(completed); err != nil {
		t.Fatal(err)
	}
	facts, ok := completed.Details["facts"].(map[string]any)
	if !ok || facts["domain"] != "catalog.example.com" || facts["affectedReleaseCount"] != json.Number("40") {
		t.Fatalf("bounded revocation facts = %#v", completed.Details)
	}
	sorted := append([]string(nil), affected...)
	slices.Sort(sorted)
	raw, err := json.Marshal(sorted)
	if err != nil {
		t.Fatal(err)
	}
	if facts["affectedReleaseDigest"] != audit.DigestBytes(raw) {
		t.Fatalf("affected release digest = %v, want %s", facts["affectedReleaseDigest"], audit.DigestBytes(raw))
	}
	if _, retained := facts["affectedReleaseIds"]; retained {
		t.Fatal("version-1 revocation retained an unbounded release identifier list")
	}
	empty, err := CompletePublisherDomainRevocationAudit(record, "catalog.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyFacts, ok := empty.Details["facts"].(map[string]any)
	if !ok || emptyFacts["affectedReleaseCount"] != json.Number("0") || emptyFacts["affectedReleaseDigest"] != audit.DigestBytes([]byte("[]")) {
		t.Fatalf("empty revocation facts = %#v", empty.Details)
	}

	if _, err := CompletePublisherDomainRevocationAudit(AuditEvent{}, "catalog.example.com", affected[:2]); err == nil {
		t.Fatal("unversioned revocation record was accepted")
	}

}

func TestMemoryAuditInvestigationFiltersBeforeLimitAndExportsAtomically(t *testing.T) {
	repository := NewMemoryRepository()
	base := time.Date(2026, time.September, 3, 9, 0, 0, 0, time.UTC)
	records := []AuditEvent{
		newAuditInvestigationRecord(t, "00000000-0000-7000-8000-000000000101", "reviewer-one", "namespace.created", "namespace", "00000000-0000-7000-8000-000000000201", "request-one", base),
		newAuditInvestigationRecord(t, "00000000-0000-7000-8000-000000000102", "reviewer-one", "namespace.created", "namespace", "00000000-0000-7000-8000-000000000202", "request-two", base.Add(time.Minute)),
		newAuditInvestigationRecord(t, "00000000-0000-7000-8000-000000000103", "reviewer-two", "resource.created", "resource", "00000000-0000-7000-8000-000000000203", "request-three", base.Add(2*time.Minute)),
	}
	for _, record := range records {
		if err := repository.AppendAuditEvent(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}

	page, err := repository.AuditEvents(context.Background(), auditInvestigationTenantID, AuditEventListRequest{Filters: audit.Query{Actor: "reviewer-one", Action: "namespace.created"}, Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != records[1].ID {
		t.Fatalf("filtered newest page = %#v, %v", page, err)
	}
	position := page[0].At
	page, err = repository.AuditEvents(context.Background(), auditInvestigationTenantID, AuditEventListRequest{AfterOccurredAt: &position, AfterID: page[0].ID, Filters: audit.Query{Actor: "reviewer-one", Action: "namespace.created"}, Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != records[0].ID {
		t.Fatalf("filtered resumed page = %#v, %v", page, err)
	}
	from, before := base, base.Add(2*time.Minute)
	targetPage, err := repository.AuditEvents(context.Background(), auditInvestigationTenantID, AuditEventListRequest{Filters: audit.Query{RequestID: "request-two", TargetType: "namespace", TargetID: records[1].TargetID, OccurredFrom: &from, OccurredBefore: &before}, Limit: 10})
	if err != nil || len(targetPage) != 1 || targetPage[0].ID != records[1].ID {
		t.Fatalf("request, target, and time filters = %#v, %v", targetPage, err)
	}

	cutoff := before.Add(time.Minute)
	filters := audit.Query{Actor: "reviewer-one", Action: "namespace.created", OccurredFrom: &from, OccurredBefore: &before}
	access := newAuditInvestigationRecord(t, "00000000-0000-7000-8000-000000000104", "reviewer-one", audit.ActionAuditExported, "audit_range", auditInvestigationTenantID, "audit-export", cutoff)
	access.Reason = "qualify restore evidence"
	if err := audit.ValidateNewRecord(access); err != nil {
		t.Fatal(err)
	}
	exported, err := repository.ExportAuditEvents(context.Background(), auditInvestigationTenantID, AuditExportRequest{Filters: filters, Cutoff: cutoff, GeneratedAt: cutoff, Access: access})
	if err != nil || exported.Manifest.RowCount != 2 || exported.Manifest.First == nil || exported.Manifest.First.ID != records[0].ID || exported.Manifest.Last == nil || exported.Manifest.Last.ID != records[1].ID {
		t.Fatalf("memory audit export = %#v, %v", exported.Manifest, err)
	}
	accessRows, err := repository.AuditEvents(context.Background(), auditInvestigationTenantID, AuditEventListRequest{Filters: audit.Query{Action: audit.ActionAuditExported}, Limit: 10})
	if err != nil || len(accessRows) != 1 || accessRows[0].At.Before(cutoff) || accessRows[0].AfterDigest != exported.Manifest.EventsDigest {
		t.Fatalf("atomic export access evidence = %#v, %v", accessRows, err)
	}
	beforeFailure := len(repository.audit)
	if _, err := repository.ExportAuditEvents(context.Background(), auditInvestigationTenantID, AuditExportRequest{Filters: filters, Cutoff: from, GeneratedAt: from, Access: access}); ErrorCodeOf(err) != CodeInternal {
		t.Fatalf("invalid export cutoff returned %v", err)
	}
	if len(repository.audit) != beforeFailure {
		t.Fatalf("failed export appended audit evidence: %d -> %d", beforeFailure, len(repository.audit))
	}
}

func TestAuditServiceAttributesReadsAndFailsClosed(t *testing.T) {
	base := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	seed := newAuditInvestigationRecord(t, "00000000-0000-7000-8000-000000000111", "service-writer", "resource.created", "resource", "00000000-0000-7000-8000-000000000211", "seed-request", base.Add(-time.Minute))
	if err := repository.AppendAuditEvent(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	validator := guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult { return ValidationResult{} })
	service, err := NewService(Options{Repository: repository, Clock: &guardrailClock{now: base}, IDs: &guardrailIDs{}, Validator: validator, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{
		TenantID: auditInvestigationTenantID, Subject: "external-subject-must-not-be-indexed", Roles: []identity.Role{identity.RoleSecurityReviewer},
		RealmID: "00000000-0000-7000-8000-000000000301", MembershipID: "00000000-0000-7000-8000-000000000302", ActorType: identity.ActorHuman,
	}
	ctx := identityfixture.WithActor(context.Background(), actor)
	ctx = audit.WithExecution(ctx, audit.Execution{RequestID: "investigation-request", AuthorizationDigest: actor.AuthorizationDigest()})
	items, err := service.AuditEvents(ctx, AuditEventListRequest{Filters: audit.Query{Action: seed.Action}, Limit: 10})
	if err != nil || len(items) != 1 || items[0].ID != seed.ID {
		t.Fatalf("audited investigation = %#v, %v", items, err)
	}
	if len(repository.audit) != 2 {
		t.Fatalf("audit row count = %d, want 2", len(repository.audit))
	}
	access := repository.audit[1]
	if access.Action != audit.ActionAuditEventsRead || access.Actor != actor.MembershipID || access.RequestID != "investigation-request" || access.AfterDigest == "" {
		t.Fatalf("investigation access attribution = %#v", access)
	}
	if access.Actor == actor.Subject {
		t.Fatal("external identity subject was retained as the v1 actor")
	}

	failing := &auditAppendFailureRepository{Repository: repository, err: errors.New("audit unavailable")}
	failingService, err := NewService(Options{Repository: failing, Clock: &guardrailClock{now: base}, IDs: &guardrailIDs{}, Validator: validator, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := failingService.AuditEvents(ctx, AuditEventListRequest{Limit: 10}); err == nil || result != nil {
		t.Fatalf("audit-write failure disclosed a page: %#v, %v", result, err)
	}
	if len(repository.audit) != 2 {
		t.Fatalf("failed access changed retained evidence: %#v", repository.audit)
	}
	reader := actor
	reader.Roles = []identity.Role{identity.RoleReader}
	if _, err := service.AuditEvents(identityfixture.WithActor(context.Background(), reader), AuditEventListRequest{Limit: 10}); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("reader audit investigation returned %v", err)
	}
}

func TestAuditServiceRejectsCorruptPageWithoutRecordingAccess(t *testing.T) {
	base := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	legacy := AuditEvent{ID: "00000000-0000-7000-8000-000000000119", TenantID: auditInvestigationTenantID, Actor: "legacy-writer", Action: "namespace.created", TargetType: "namespace", TargetID: "oversized-legacy", Details: map[string]any{"value": strings.Repeat("x", audit.MaxEncodedRecordBytes)}, At: base.Add(-time.Minute)}
	if err := repository.AppendAuditEvent(context.Background(), legacy); err == nil {
		t.Fatal("invalid audit record was persisted")
	}
	// Simulate corruption below the public repository boundary; reads still fail closed.
	repository.audit = append(repository.audit, legacy)
	validator := guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult { return ValidationResult{} })
	service, err := NewService(Options{Repository: repository, Clock: &guardrailClock{now: base}, IDs: &guardrailIDs{}, Validator: validator, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{TenantID: auditInvestigationTenantID, Subject: "reviewer", Roles: []identity.Role{identity.RoleSecurityReviewer}, RealmID: "00000000-0000-7000-8000-000000000301", MembershipID: "00000000-0000-7000-8000-000000000302", ActorType: identity.ActorHuman}
	before := len(repository.audit)
	result, err := service.AuditEvents(identityfixture.WithActor(context.Background(), actor), AuditEventListRequest{Filters: audit.Query{TargetType: "namespace", TargetID: legacy.TargetID}, Limit: 10})
	if ErrorCodeOf(err) != CodeInternal || result != nil {
		t.Fatalf("oversized legacy investigation = %#v, %v", result, err)
	}
	if len(repository.audit) != before {
		t.Fatalf("failed investigation appended access evidence: %d -> %d", before, len(repository.audit))
	}
}

type auditAppendFailureRepository struct {
	Repository
	err error
}

func (r *auditAppendFailureRepository) AppendAuditEvent(context.Context, AuditEvent) error {
	return r.err
}

func newAuditInvestigationRecord(t *testing.T, id, actor, action, targetType, targetID, requestID string, at time.Time) AuditEvent {
	t.Helper()
	reason := ""
	var facts map[string]any
	if action == audit.ActionAuditExported {
		reason = "temporary reason"
		facts = map[string]any{"rowCount": 0, "eventsDigest": audit.DigestBytes([]byte("[]")), "occurredFrom": at.Add(-time.Minute).Format(time.RFC3339Nano), "occurredBefore": at.Format(time.RFC3339Nano)}
	}
	record, err := audit.NewRecord(id, auditInvestigationTenantID, audit.Principal{ID: actor, Type: audit.PrincipalServiceExecutor}, action, targetType, targetID, reason, audit.Execution{RequestID: requestID}, facts, at)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
