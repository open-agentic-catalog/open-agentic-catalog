package database

import (
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func currentTestEvidence(t *testing.T, action, targetType, targetID string, at time.Time) (catalog.AuditEvent, catalog.OutboxEvent) {
	t.Helper()
	const tenantID = "00000000-0000-7000-8000-000000000001"
	record, err := audit.NewRecord("00000000-0000-7000-8000-000000000098", tenantID, audit.Principal{ID: "catalog-api", Type: audit.PrincipalServiceExecutor}, action, targetType, targetID, "reviewed test transition", audit.Execution{}, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	data, err := events.MarshalReference(action, targetType, targetID, "")
	if err != nil {
		t.Fatal(err)
	}
	return record, catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000099", TenantID: tenantID, Type: events.CanonicalType(action), Subject: targetID, Data: data, CreatedAt: at}
}

func currentTestIdentityEvidence(t *testing.T, action, targetType, targetID string, at time.Time) identity.ChangeEvidence {
	t.Helper()
	record, outbox := currentTestEvidence(t, action, targetType, targetID, at)
	return identity.ChangeEvidence{FormatVersion: record.FormatVersion, AuditID: record.ID, OutboxID: outbox.ID, Actor: record.Actor, Action: record.Action, TargetType: record.TargetType, TargetID: record.TargetID, Reason: record.Reason, Details: record.Details, At: at, OutboxType: outbox.Type, OutboxSubject: outbox.Subject, OutboxData: outbox.Data}
}

func currentTestAuditFacts(t *testing.T, action, targetType, targetID string, at time.Time, facts map[string]any) audit.Record {
	t.Helper()
	record, err := audit.NewRecord("00000000-0000-7000-8000-000000000090", restoreTestTenant, audit.Principal{ID: "catalog-worker", Type: audit.PrincipalServiceExecutor}, action, targetType, targetID, "", audit.Execution{}, facts, at)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
