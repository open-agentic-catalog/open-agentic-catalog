package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type auditIDFunc func(time.Time) (string, error)

func (f auditIDFunc) New(at time.Time) (string, error) { return f(at) }

func TestCommandEvidenceConstructionRejectsIdentifierAndEncodingFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("identifier provider failed")
	for _, test := range []struct {
		name      string
		failAt    int
		value     any
		action    string
		message   string
		wantCalls int
	}{
		{"audit identifier", 1, "value", "namespace.created", "create audit identifier", 1},
		{"outbox identifier", 2, "value", "namespace.created", "create outbox identifier", 2},
		{"unencodable subject", 0, make(chan int), "namespace.created", "digest event subject", 2},
		{"unknown notification", 0, "value", "not.a.public.action", "marshal outbox event", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			service := &Service{ids: auditIDFunc(func(time.Time) (string, error) {
				calls++
				if calls == test.failAt {
					return "", failure
				}
				return fmt.Sprintf("00000000-0000-7000-8000-%012d", calls), nil
			})}
			actor := identity.Actor{TenantID: auditInvestigationTenantID, Subject: "author", MembershipID: "00000000-0000-7000-8000-000000000010", RealmID: "00000000-0000-7000-8000-000000000011", ActorType: identity.ActorHuman}
			record, event, err := service.evidenceAt(t.Context(), actor, test.action, "namespace", "00000000-0000-7000-8000-000000000012", "", test.value, time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
			var domain *DomainError
			if !errors.As(err, &domain) || domain.Code != CodeInternal || domain.Message != test.message || calls != test.wantCalls {
				t.Fatalf("failure = %v, identifier calls=%d, want %s and %d calls", err, calls, test.message, test.wantCalls)
			}
			if !reflect.DeepEqual(record, AuditEvent{}) || !reflect.DeepEqual(event, OutboxEvent{}) {
				t.Fatal("failed construction returned partial audit or notification evidence")
			}
			if test.failAt != 0 && !errors.Is(err, failure) {
				t.Fatalf("identifier cause was lost: %v", err)
			}
		})
	}
}

func TestAuditReadsRejectIdentifierFailureBeforeDisclosingResults(t *testing.T) {
	t.Parallel()
	before := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	from := before.Add(-time.Hour)
	failure := errors.New("identifier provider failed")
	for _, test := range []struct {
		name string
		read func(*Service, context.Context) (any, error)
		zero any
	}{
		{"page", func(s *Service, ctx context.Context) (any, error) {
			return s.AuditEvents(ctx, AuditEventListRequest{Limit: 2})
		}, []AuditEvent(nil)},
		{"export", func(s *Service, ctx context.Context) (any, error) {
			return s.ExportAuditEvents(ctx, AuditExportInput{Filters: audit.Query{OccurredFrom: &from, OccurredBefore: &before}, Reason: "Investigate catalog changes"})
		}, AuditExport{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository := NewMemoryRepository()
			seed := newAuditInvestigationRecord(t, "00000000-0000-7000-8000-000000000020", "writer", "namespace.created", "namespace", "00000000-0000-7000-8000-000000000021", "seed-request", from)
			repository.audit = []AuditEvent{seed}
			service := &Service{repo: repository, clock: &guardrailClock{now: before}, ids: auditIDFunc(func(time.Time) (string, error) { return "", failure })}
			actor := identity.Actor{TenantID: auditInvestigationTenantID, Subject: "reviewer", Roles: []identity.Role{identity.RoleSecurityReviewer}, MembershipID: "00000000-0000-7000-8000-000000000010", RealmID: "00000000-0000-7000-8000-000000000011", ActorType: identity.ActorHuman}
			got, err := test.read(service, identityfixture.WithActor(t.Context(), actor))
			if ErrorCodeOf(err) != CodeInternal || !errors.Is(err, failure) || !reflect.DeepEqual(got, test.zero) {
				t.Fatalf("identifier failure disclosed results or lost cause: %#v, %v", got, err)
			}
			if !reflect.DeepEqual(repository.audit, []AuditEvent{seed}) || len(repository.outbox) != 0 {
				t.Fatal("failed read changed retained evidence")
			}
		})
	}
}

func TestAuditExportErrorTranslationPreservesCauseWithoutExposingIt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		code ErrorCode
	}{
		{"bounded range", fmt.Errorf("encode: %w", &audit.RangeTooLargeError{Reason: "private-cause"}), CodeAuditExportRangeTooLarge},
		{"unexpected encoder failure", errors.New("private-cause"), CodeInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := TranslateAuditExportError(test.err)
			var domain *DomainError
			if !errors.As(got, &domain) || domain.Code != test.code || !errors.Is(got, test.err) || strings.Contains(domain.Message, "private-cause") {
				t.Fatalf("translation lost safe code or internal cause: %#v", got)
			}
		})
	}
}

func TestCompleteAuditExportAccessBindsCanonicalManifestFacts(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 9, 1, 10, 0, 0, 123, time.FixedZone("offset", 3600))
	manifest := audit.Manifest{EventsDigest: "sha256:" + strings.Repeat("a", 64), RowCount: 2, Filters: audit.Filters{OccurredFrom: from, OccurredBefore: from.Add(time.Hour)}}
	for _, details := range []map[string]any{nil, {"facts": "invalid shape", "retained": true}, {"facts": map[string]any{"eventsDigest": "old", "rowCount": 99}, "retained": true}} {
		got := CompleteAuditExportAccess(AuditEvent{ID: "access", Details: details}, manifest)
		facts, ok := got.Details["facts"].(map[string]any)
		if !ok || got.ID != "access" || got.AfterDigest != manifest.EventsDigest || facts["eventsDigest"] != manifest.EventsDigest || facts["rowCount"] != json.Number("2") || facts["occurredFrom"] != "2026-09-01T09:00:00.000000123Z" || facts["occurredBefore"] != "2026-09-01T10:00:00.000000123Z" {
			t.Fatalf("completed access evidence = %#v", got)
		}
		if details != nil && got.Details["retained"] != true {
			t.Fatal("completion discarded unrelated details")
		}
	}
}
