//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// Reuse the published-listing fixture so rejected mutations must also preserve
// a real release. Direct repository calls reach SQL revision fencing.
func testPublisherRevisionAndAuditFailures(t *testing.T, repository *PostgresRepository, owner *pgxpool.Pool, current catalog.Publisher) catalog.Publisher {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	type publisherState struct {
		publisher, releases string
		audits, outbox      int
	}
	readState := func() publisherState {
		t.Helper()
		var state publisherState
		if err := owner.QueryRow(ctx, `
SELECT (SELECT to_jsonb(publisher)::text FROM publishers publisher WHERE tenant_id=$1 AND id=$2::uuid),
       (SELECT jsonb_agg(to_jsonb(release) ORDER BY release.id)::text FROM listing_releases release
          JOIN listing_revisions revision ON revision.tenant_id=release.tenant_id AND revision.id=release.listing_revision_id
         WHERE release.tenant_id=$1 AND revision.publisher_id=$2::uuid),
       (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND target_id=$2::text),
       (SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND subject=$2::text)`, current.TenantID, current.ID).
			Scan(&state.publisher, &state.releases, &state.audits, &state.outbox); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := readState()
	updated := current
	updated.DisplayName += " Verified"
	updated.Revision++
	updated.UpdatedAt = current.UpdatedAt.Add(time.Microsecond)
	evidence := func(action string) (catalog.AuditEvent, catalog.OutboxEvent) {
		t.Helper()
		generator := id.Generator{}
		auditID, err := generator.New(updated.UpdatedAt)
		if err != nil {
			t.Fatal(err)
		}
		outboxID, err := generator.New(updated.UpdatedAt)
		if err != nil {
			t.Fatal(err)
		}
		record := integrationAudit(t, auditID, current.TenantID, "publisher-test", action, "publisher", current.ID, "verify publisher mutation atomicity", updated.UpdatedAt)
		return record, integrationOutbox(t, outboxID, record)
	}
	updateAudit, updateOutbox := evidence("publisher.updated")
	params := catalog.UpdatePublisherParams{Publisher: updated, ExpectedRevision: current.Revision - 1, Audit: updateAudit, Outbox: updateOutbox}
	if _, err := repository.UpdatePublisher(ctx, params); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale repository publisher update returned %v", err)
	}
	if readState() != before {
		t.Fatal("stale publisher update changed authority, releases or evidence")
	}
	revokeAudit, revokeOutbox := evidence("publisher.publication_domain_revoked")
	if _, affected, err := repository.RevokePublisherPublicationDomain(ctx, current.TenantID, current.ID, "catalog.example.com", current.Revision-1, revokeAudit, revokeOutbox); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || len(affected) != 0 {
		t.Fatalf("stale repository domain revocation returned affected=%v err=%v", affected, err)
	}
	if readState() != before {
		t.Fatal("stale domain revocation changed authority, releases or evidence")
	}
	params.ExpectedRevision = current.Revision
	func() {
		// IDs are generated UUIDs from this fixture; only this publisher's update
		// audit is rejected in its private schema.
		if _, err := owner.Exec(ctx, fmt.Sprintf(`
CREATE FUNCTION reject_publisher_update_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected publisher audit failure'; END $$;
CREATE TRIGGER reject_publisher_update_audit BEFORE INSERT ON audit_events FOR EACH ROW
WHEN (NEW.action='publisher.updated' AND NEW.tenant_id='%s' AND NEW.target_id='%s')
EXECUTE FUNCTION reject_publisher_update_audit()`, current.TenantID, current.ID)); err != nil {
			t.Fatal(err)
		}
		defer func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cleanupCancel()
			if _, err := owner.Exec(cleanupCtx, `DROP TRIGGER reject_publisher_update_audit ON audit_events; DROP FUNCTION reject_publisher_update_audit()`); err != nil {
				t.Errorf("remove publisher audit failure: %v", err)
			}
		}()
		_, err := repository.UpdatePublisher(ctx, params)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "injected publisher audit failure" {
			t.Fatalf("publisher audit failure returned %v", err)
		}
		if readState() != before {
			t.Fatal("failed publisher audit retained a mutation, release change or evidence")
		}
	}()
	result, err := repository.UpdatePublisher(ctx, params)
	if err != nil || result.Revision != updated.Revision || result.DisplayName != updated.DisplayName || !slices.Equal(result.ApprovedPublicationDomains, current.ApprovedPublicationDomains) {
		t.Fatalf("publisher retry = %#v, %v", result, err)
	}
	after := readState()
	if after.publisher == before.publisher || after.releases != before.releases || after.audits != before.audits+1 || after.outbox != before.outbox+1 {
		t.Fatal("publisher retry did not commit exactly one mutation and its evidence while retaining releases")
	}
	return result
}
