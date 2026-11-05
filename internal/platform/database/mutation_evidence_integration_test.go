//go:build integration

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// Snapshot the small, owned fixture as values. Omitting the expected target and
// evidence IDs on success detects any additional mutation in either tenant.
func readMutationEvidenceState(t *testing.T, ctx context.Context, owner *pgxpool.Pool, targetID, auditID, outboxID string) string {
	t.Helper()
	var state string
	if err := owner.QueryRow(ctx, `
SELECT jsonb_build_object(
  'namespaces',COALESCE((SELECT jsonb_agg(to_jsonb(n) ORDER BY n.id) FROM namespaces n WHERE n.id::text<>$1),'[]'::jsonb),
  'resources',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM resources r WHERE r.id::text<>$1),'[]'::jsonb),
  'publishers',COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM publishers p WHERE p.id::text<>$1),'[]'::jsonb),
  'audit',COALESCE((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM audit_events a WHERE a.id::text<>$2),'[]'::jsonb),
  'outbox',COALESCE((SELECT jsonb_agg(to_jsonb(o) ORDER BY o.id) FROM outbox_events o WHERE o.id::text<>$3),'[]'::jsonb)
)::text`, targetID, auditID, outboxID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// Only generated UUIDs and fixed fixture SQL enter this owner-created trigger.
// Its checks run as the actual API role inside the repository transaction.
func rejectMutationEvidenceInsert(t *testing.T, ctx context.Context, owner *pgxpool.Pool, record catalog.AuditEvent, outbox catalog.OutboxEvent, rejectOutbox bool, changedPredicate string) func() {
	t.Helper()
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, err := owner.Exec(cleanupCtx, `
DROP TRIGGER IF EXISTS reject_mutation_evidence ON audit_events;
DROP TRIGGER IF EXISTS reject_mutation_evidence ON outbox_events;
DROP FUNCTION IF EXISTS reject_mutation_evidence()`); err != nil {
			t.Errorf("remove mutation evidence rejection: %v", err)
		}
	}
	t.Cleanup(cleanup)
	targetTable := "publishers"
	if record.TargetType == "resource" {
		targetTable = "resources"
	} else if record.TargetType == "namespace" {
		targetTable = "namespaces"
	}
	table, evidenceID := "audit_events", record.ID
	condition := fmt.Sprintf("NEW.action='%s' AND NEW.target_id='%s'", record.Action, record.TargetID)
	priorAuditCheck := ""
	if rejectOutbox {
		table, evidenceID = "outbox_events", outbox.ID
		condition = fmt.Sprintf("NEW.event_type='%s' AND NEW.subject='%s'", outbox.Type, outbox.Subject)
		priorAuditCheck = fmt.Sprintf(`
IF NOT EXISTS(SELECT 1 FROM audit_events WHERE tenant_id='%s' AND id='%s' AND action='%s' AND target_id='%s') THEN
  RAISE EXCEPTION 'expected audit insert did not precede outbox rejection';
END IF;`, record.TenantID, record.ID, record.Action, record.TargetID)
	}
	if _, err := owner.Exec(ctx, fmt.Sprintf(`
CREATE FUNCTION reject_mutation_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS(SELECT 1 FROM %s WHERE tenant_id='%s' AND id='%s' AND revision=2 AND (%s)) THEN
    RAISE EXCEPTION 'expected mutation did not precede evidence rejection';
  END IF;
  %s
  RAISE EXCEPTION 'injected mutation evidence failure';
END $$;
REVOKE ALL ON FUNCTION reject_mutation_evidence() FROM PUBLIC;
CREATE TRIGGER reject_mutation_evidence BEFORE INSERT ON %s FOR EACH ROW
WHEN (NEW.id='%s' AND NEW.tenant_id='%s' AND %s)
EXECUTE FUNCTION reject_mutation_evidence()`, targetTable, record.TenantID, record.TargetID, changedPredicate, priorAuditCheck, table, evidenceID, record.TenantID, condition)); err != nil {
		t.Fatal(err)
	}
	return cleanup
}

func TestPostgresMutationEvidenceRollback(t *testing.T) {
	db := newHousekeepingDatabase(t)
	repository, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	newID := func() string {
		t.Helper()
		value, err := (id.Generator{}).New(now)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	namespaceA, namespaceB := newID(), newID()
	namespaceAudit, namespaceOutbox := newID(), newID()
	draftID, otherDraftID := newID(), newID()
	approveID, revokeID, otherPublisherID := newID(), newID(), newID()
	if _, err := db.operator.Exec(ctx, `
INSERT INTO namespaces(id,tenant_id,slug,display_name,created_at,updated_at) VALUES
  ($1,$3,'atomicity','Atomicity A',$5,$5),($2,$4,'atomicity','Atomicity B',$5,$5)`, namespaceA, namespaceB, housekeepingTenantA, housekeepingTenantB, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(ctx, `
INSERT INTO resources(id,tenant_id,namespace_id,canonical_identifier,kind,created_by,revision,created_at,updated_at) VALUES
  ($1,$3,$5,'urn:air:atomicity.example:atomicity:draft','agent','fixture',1,$7,$7),
  ($2,$4,$6,'urn:air:atomicity.example:atomicity:draft','agent','fixture',1,$7,$7)`, draftID, otherDraftID, housekeepingTenantA, housekeepingTenantB, namespaceA, namespaceB, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(ctx, `
INSERT INTO namespaces(id,tenant_id,slug,display_name,created_at,updated_at) VALUES
  ($1,$3,'audit-update','Original audit namespace',$4,$4),
  ($2,$3,'outbox-update','Original outbox namespace',$4,$4)`, namespaceAudit, namespaceOutbox, housekeepingTenantA, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(ctx, `
INSERT INTO publishers(id,tenant_id,display_name,domains,approved_publication_domains,revision,created_at,updated_at) VALUES
  ($1,$4,'Approval fixture',ARRAY['approve.atomicity.example'],'{}',1,$6,$6),
  ($2,$4,'Revocation fixture',ARRAY['revoke.atomicity.example'],ARRAY['revoke.atomicity.example'],1,$6,$6),
  ($3,$5,'Other tenant',ARRAY['approve.atomicity.example','revoke.atomicity.example'],ARRAY['revoke.atomicity.example'],1,$6,$6)`, approveID, revokeID, otherPublisherID, housekeepingTenantA, housekeepingTenantB, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, action, targetType, targetID, domain, changedPredicate string
		rejectOutbox                                                 bool
	}{
		{"namespace update audit failure", "namespace.updated", "namespace", namespaceAudit, "", "display_name='Updated namespace' AND description='Updated description'", false},
		{"namespace update outbox failure", "namespace.updated", "namespace", namespaceOutbox, "", "display_name='Updated namespace' AND description='Updated description'", true},
		{"draft deletion outbox failure", "resource.deleted", "resource", draftID, "", "deleted_at IS NOT NULL", true},
		{"domain approval audit failure", "publisher.publication_domain_approved", "publisher", approveID, "approve.atomicity.example", "approved_publication_domains=ARRAY['approve.atomicity.example']", false},
		{"domain revocation outbox failure", "publisher.publication_domain_revoked", "publisher", revokeID, "revoke.atomicity.example", "approved_publication_domains='{}'::text[]", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := now.Add(time.Second)
			record := integrationAudit(t, newID(), housekeepingTenantA, "mutation-test", tc.action, tc.targetType, tc.targetID, "verify mutation evidence rollback", at)
			if tc.domain != "" {
				record.Details["facts"] = map[string]any{"domain": tc.domain}
			}
			outbox := integrationOutbox(t, newID(), record)
			var resource catalog.Resource
			var publisher catalog.Publisher
			var namespace, expectedNamespace catalog.Namespace
			if tc.targetType == "namespace" {
				expectedNamespace, err = repository.GetNamespace(ctx, record.TenantID, tc.targetID)
				if err != nil {
					t.Fatal("namespace update control", err)
				}
				expectedNamespace.DisplayName, expectedNamespace.Description = "Updated namespace", "Updated description"
				expectedNamespace.Revision, expectedNamespace.UpdatedAt = 2, at
			}
			var affected []string
			mutate := func() error {
				var err error
				switch tc.action {
				case "namespace.updated":
					input := expectedNamespace
					// Repository updates preserve identity and creation metadata.
					input.Slug, input.CreatedAt = "ignored-replacement", at
					namespace, err = repository.UpdateNamespace(ctx, catalog.UpdateNamespaceParams{Namespace: input, ExpectedRevision: 1, Audit: record, Outbox: outbox})
				case "resource.deleted":
					resource, err = repository.DeleteResource(ctx, catalog.DeleteResourceParams{TenantID: record.TenantID, ResourceID: tc.targetID, ExpectedRevision: 1, Actor: record.Actor, Reason: record.Reason, At: at, Audit: record, Outbox: outbox})
				case "publisher.publication_domain_approved":
					publisher, err = repository.ApprovePublisherPublicationDomain(ctx, catalog.PublisherDomainApproval{TenantID: record.TenantID, PublisherID: tc.targetID, Domain: tc.domain, ExpectedRevision: 1, Audit: record, Outbox: outbox})
				case "publisher.publication_domain_revoked":
					publisher, affected, err = repository.RevokePublisherPublicationDomain(ctx, record.TenantID, tc.targetID, tc.domain, 1, record, outbox)
				}
				return err
			}
			before := readMutationEvidenceState(t, ctx, db.operator, "", "", "")
			unaffected := readMutationEvidenceState(t, ctx, db.operator, tc.targetID, record.ID, outbox.ID)
			removeRejection := rejectMutationEvidenceInsert(t, ctx, db.operator, record, outbox, tc.rejectOutbox, tc.changedPredicate)
			err := mutate()
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "injected mutation evidence failure" {
				t.Fatalf("mutation evidence failure returned %v", err)
			}
			if resource != (catalog.Resource{}) || !reflect.DeepEqual(publisher, catalog.Publisher{}) || !reflect.DeepEqual(namespace, catalog.Namespace{}) || len(affected) != 0 {
				t.Fatal("failed mutation returned a successful object or affected releases")
			}
			if readMutationEvidenceState(t, ctx, db.operator, "", "", "") != before {
				t.Fatal("evidence rejection retained a mutation or evidence in the fixture")
			}
			removeRejection()
			// Reusing these exact IDs proves the rolled-back inserts did not
			// reserve audit/outbox uniqueness and the repository can retry.
			if err := mutate(); err != nil {
				t.Fatalf("retry with identical evidence failed: %v", err)
			}
			if readMutationEvidenceState(t, ctx, db.operator, tc.targetID, record.ID, outbox.ID) != unaffected {
				t.Fatal("retry changed unrelated objects or added unexpected evidence")
			}
			switch tc.targetType {
			case "namespace":
				retained, readErr := repository.GetNamespace(ctx, record.TenantID, tc.targetID)
				if readErr != nil || !reflect.DeepEqual(namespace, expectedNamespace) || !reflect.DeepEqual(retained, expectedNamespace) {
					t.Fatalf("retried namespace lost descriptive changes or immutable metadata: result=%#v retained=%#v expected=%#v error=%v", namespace, retained, expectedNamespace, readErr)
				}
			case "resource":
				if resource.ID != tc.targetID || resource.Revision != 2 || resource.DeletedAt == nil || !resource.DeletedAt.Equal(at) || resource.DeletedBy != record.Actor || resource.DeleteReason != record.Reason {
					t.Fatalf("retried deletion result = %#v", resource)
				}
				if _, err := repository.GetResource(ctx, record.TenantID, tc.targetID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
					t.Fatalf("deleted draft remained visible: %v", err)
				}
				var tombstoneMatches bool
				if err := db.operator.QueryRow(ctx, `SELECT revision=2 AND deleted_at=$3 AND updated_at=$3 AND deleted_by=$4 AND delete_reason=$5 AND created_at=$6 AND namespace_id=$7 FROM resources WHERE tenant_id=$1 AND id=$2`, record.TenantID, tc.targetID, at, record.Actor, record.Reason, now, namespaceA).Scan(&tombstoneMatches); err != nil || !tombstoneMatches {
					t.Fatalf("retried tombstone = %t, err=%v", tombstoneMatches, err)
				}
			default:
				wantApproved := []string{}
				if tc.action == "publisher.publication_domain_approved" {
					wantApproved = []string{tc.domain}
				}
				if publisher.ID != tc.targetID || publisher.Revision != 2 || !publisher.UpdatedAt.Equal(at) || !slices.Equal(publisher.ApprovedPublicationDomains, wantApproved) || len(affected) != 0 {
					t.Fatalf("retried publisher = %#v, affected=%v", publisher, affected)
				}
				retained, err := repository.GetPublisher(ctx, record.TenantID, tc.targetID)
				if err != nil || retained.Revision != 2 || !slices.Equal(retained.ApprovedPublicationDomains, wantApproved) || !slices.Equal(retained.Domains, []string{tc.domain}) || !retained.UpdatedAt.Equal(at) || !retained.CreatedAt.Equal(now) {
					t.Fatalf("retained publisher = %#v, err=%v", retained, err)
				}
				var approvalsMatch bool
				if err := db.operator.QueryRow(ctx, `SELECT approved_publication_domains=$3 FROM publishers WHERE tenant_id=$1 AND id=$2`, record.TenantID, tc.targetID, wantApproved).Scan(&approvalsMatch); err != nil || !approvalsMatch {
					t.Fatalf("retained approval set = %t, err=%v", approvalsMatch, err)
				}
			}
			var auditCount, outboxCount int
			if err := db.operator.QueryRow(ctx, `
SELECT (SELECT count(*) FROM audit_events WHERE id=$1 AND tenant_id=$3 AND action=$4 AND target_id=$5 AND occurred_at=$6),
       (SELECT count(*) FROM outbox_events WHERE id=$2 AND tenant_id=$3 AND event_type=$7 AND subject=$5 AND created_at=$6 AND data=$8::jsonb)`, record.ID, outbox.ID, record.TenantID, record.Action, tc.targetID, at, outbox.Type, outbox.Data).Scan(&auditCount, &outboxCount); err != nil || auditCount != 1 || outboxCount != 1 {
				t.Fatalf("retry evidence count = audit %d, outbox %d, err=%v", auditCount, outboxCount, err)
			}
			if tc.action == "publisher.publication_domain_revoked" {
				var factsJSON []byte
				if err := db.operator.QueryRow(ctx, `SELECT details->'facts' FROM audit_events WHERE tenant_id=$1 AND id=$2`, record.TenantID, record.ID).Scan(&factsJSON); err != nil {
					t.Fatal(err)
				}
				var facts map[string]any
				if err := json.Unmarshal(factsJSON, &facts); err != nil {
					t.Fatal(err)
				}
				if len(facts) != 3 || facts["domain"] != tc.domain || facts["affectedReleaseCount"] != float64(0) || facts["affectedReleaseDigest"] != audit.DigestBytes([]byte("[]")) {
					t.Fatalf("retried revocation facts = %s", factsJSON)
				}
			}
		})
	}
}
