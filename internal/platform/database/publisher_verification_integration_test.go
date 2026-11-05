//go:build integration

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

type verificationTXTFunc func(context.Context, string) ([]string, error)

func (f verificationTXTFunc) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return f(ctx, name)
}

func TestPostgresPublisherVerificationProofAndAtomicity(t *testing.T) {
	db := newHousekeepingDatabase(t)
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	provisioned, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "urn:publisher-verification-test", TenancyMode: "dedicated", Subject: "domain-admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}, Operator: "publisher-test", Reason: "Qualify domain control", AcknowledgeOutbox: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "domain-admin", MembershipID: provisioned.MembershipID, RealmID: provisioned.RealmID, ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
	c := clock.NewFake(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	policy := catalog.DefaultPublisherDomainPolicy()
	policy.Mode = catalog.DomainVerificationRequired
	var apiRole string
	if err := db.api.QueryRow(t.Context(), `SELECT current_user`).Scan(&apiRole); err != nil {
		t.Fatal(err)
	}
	var value string
	var lookups atomic.Int32
	s, err := catalog.NewService(catalog.Options{Repository: repo, Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte("01234567890123456789012345678901"), PublisherDomainPolicy: policy, PublisherTXT: verificationTXTFunc(func(call context.Context, _ string) ([]string, error) {
		lookups.Add(1)
		var open int
		if err := db.operator.QueryRow(call, `SELECT count(*) FROM pg_stat_activity WHERE usename=$1 AND state='idle in transaction'`, apiRole).Scan(&open); err != nil {
			t.Error(err)
		}
		if open != 0 {
			t.Errorf("DNS lookup retained %d API transactions", open)
		}
		return []string{value}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Verified Publisher", Domains: []string{"catalog.example.com", "other.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApprovePublisherPublicationDomain(ctx, p.ID, "catalog.example.com", "No proof yet", p.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("approval ignored required proof", err)
	}
	status, err := s.GetPublisherDomainVerification(ctx, p.ID, "catalog.example.com")
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := s.IssuePublisherDomainChallenge(ctx, p.ID, "catalog.example.com", "Issue exact challenge", status.Version)
	if err != nil {
		t.Fatal(err)
	}
	value = challenge.Value
	assertPublisherVerificationSQLRefusal(t, db, p.TenantID, challenge.Status.Version.ID, `UPDATE publisher_domain_verifications SET revision=revision+1,nonce=CASE WHEN nonce=repeat('A',43) THEN repeat('A',42)||'E' ELSE repeat('A',43) END WHERE tenant_id=$1 AND id=$2`, "publisher challenge is immutable")
	assertPublisherVerificationSQLRefusal(t, db, p.TenantID, challenge.Status.Version.ID, `UPDATE publisher_domain_verifications SET revision=revision+1,challenge_expires_at=challenge_expires_at+interval '1 hour' WHERE tenant_id=$1 AND id=$2`, "publisher challenge is immutable")
	assertPublisherVerificationSQLRefusal(t, db, p.TenantID, challenge.Status.Version.ID, `UPDATE publisher_domain_verifications SET revision=revision+2 WHERE tenant_id=$1 AND id=$2`, "invalid publisher verification transition")
	// A late audit failure must roll back the successful DNS observation and proof.
	_, err = db.operator.Exec(t.Context(), `CREATE FUNCTION reject_domain_check_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='publisher.domain_verification_checked' THEN RAISE EXCEPTION 'owned audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_domain_check_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_domain_check_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CheckPublisherDomain(ctx, p.ID, "catalog.example.com", "Audit failure must roll back", challenge.Status.Version); err == nil {
		t.Fatal("late audit failure accepted")
	}
	after, err := s.GetPublisherDomainVerification(ctx, p.ID, "catalog.example.com")
	if err != nil || after.State != "not_verified" || after.Version.Revision != challenge.Status.Version.Revision {
		t.Fatal("failed audit retained proof", err)
	}
	if _, err = db.operator.Exec(t.Context(), `DROP TRIGGER reject_domain_check_audit ON audit_events; DROP FUNCTION reject_domain_check_audit()`); err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdempotencyStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	scope := idempotency.Scope{TenantID: p.TenantID, Actor: "domain-admin", Route: "publisher.domain_verification_checked", Key: "verified-check", AuthorizationDigest: digestText("domain-admin")}
	var commandErr error
	// A completed mismatch is replayable data, even after DNS contains the value.
	negativeScope := scope
	negativeScope.Key = "negative-check"
	value = "different TXT value"
	var negativeStatus catalog.PublisherDomainStatus
	negative, err := store.Execute(ctx, negativeScope, "negative-check", time.Now().Add(time.Hour), func(call context.Context) idempotency.Response {
		negativeStatus, commandErr = s.CheckPublisherDomain(call, p.ID, "catalog.example.com", "Commit unmatched check", challenge.Status.Version)
		body, encodeErr := json.Marshal(negativeStatus)
		if encodeErr != nil {
			t.Error(encodeErr)
		}
		return idempotency.Response{Digest: "negative-check", Status: 200, Body: body}
	})
	if err != nil || commandErr != nil || negative.Replayed || negative.Response.Status != 200 || negativeStatus.State != "not_verified" || negativeStatus.LastCheckOutcome != "mismatch" || !negativeStatus.VerifiedAt.IsZero() {
		t.Fatalf("negative check did not complete safely: %+v %v %v", negativeStatus, err, commandErr)
	}
	negativeLookups := lookups.Load()
	value = challenge.Value
	replayNegative, err := store.Execute(ctx, negativeScope, "negative-check", time.Now().Add(time.Hour), func(context.Context) idempotency.Response {
		t.Error("negative replay queried DNS")
		return idempotency.Response{}
	})
	if err != nil || !replayNegative.Replayed || !bytes.Equal(replayNegative.Response.Body, negative.Response.Body) || lookups.Load() != negativeLookups {
		t.Fatal("negative replay changed its observation", err)
	}
	challenge.Status.Version = negativeStatus.Version
	execution, err := store.Execute(ctx, scope, "exact-check", time.Now().Add(time.Hour), func(call context.Context) idempotency.Response {
		status, commandErr = s.CheckPublisherDomain(call, p.ID, "catalog.example.com", "Commit verified proof", challenge.Status.Version)
		body, encodeErr := json.Marshal(status)
		if encodeErr != nil {
			t.Error(encodeErr)
		}
		return idempotency.Response{Digest: "exact-check", Status: 200, Body: body}
	})
	if err != nil || commandErr != nil || status.State != "verified" || execution.Replayed {
		t.Fatalf("atomic verify: %+v %v %v", status, err, commandErr)
	}
	assertPublisherVerificationSQLRefusal(t, db, p.TenantID, status.Version.ID, `UPDATE publisher_domain_verifications SET revision=revision+1,proof_expires_at=proof_expires_at+interval '1 hour' WHERE tenant_id=$1 AND id=$2`, "publisher proof is immutable")
	assertPublisherVerificationSQLRefusal(t, db, p.TenantID, status.Version.ID, `UPDATE publisher_domain_verifications SET revision=revision+1,challenge_consumed=false WHERE tenant_id=$1 AND id=$2`, "publisher challenge is already consumed")
	beforeLookups := lookups.Load()
	p, err = s.ApprovePublisherPublicationDomain(ctx, p.ID, "catalog.example.com", "Approve recorded proof", p.Revision)
	if err != nil || lookups.Load() != beforeLookups {
		t.Fatal("approval queried DNS", err)
	}
	var auditCount, outboxCount int
	if err = db.operator.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND target_id=$2 AND action LIKE 'publisher.domain_%'),(SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND subject=$2)`, p.TenantID, p.ID).Scan(&auditCount, &outboxCount); err != nil || auditCount != 3 || outboxCount != 2 {
		t.Fatalf("private evidence: audit=%d outbox=%d err=%v", auditCount, outboxCount, err)
	}
	// The worker cannot read even a known verification record.
	_, err = db.worker.Exec(t.Context(), `SELECT id FROM publisher_domain_verifications WHERE tenant_id=$1`, p.TenantID)
	if err == nil {
		t.Fatal("worker gained verification access")
	}
	var foreignCount int
	_, err = withTenantRead(t.Context(), db.api, housekeepingTenantB, func(tx pgx.Tx) (bool, error) {
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM publisher_domain_verifications WHERE publisher_id=$1`, p.ID).Scan(&foreignCount)
		return true, err
	})
	if err != nil || foreignCount != 0 {
		t.Fatal("verification RLS failed", err)
	}
	// Raw deletion cannot bypass the retained-state identity while declared.
	_, err = withTenantTx(t.Context(), db.api, p.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (bool, error) {
		_, err := tx.Exec(t.Context(), `DELETE FROM publisher_domain_verifications WHERE tenant_id=$1 AND publisher_id=$2`, p.TenantID, p.ID)
		return true, err
	})
	if err == nil {
		t.Fatal("direct deletion bypassed domain lifecycle")
	}
	cleared, err := s.ClearPublisherDomainVerification(ctx, p.ID, "catalog.example.com", "Clear only DNS evidence", status.Version)
	if err != nil || cleared.State != "not_verified" {
		t.Fatal(err)
	}
	current, err := s.GetPublisher(ctx, p.ID)
	if err != nil || len(current.ApprovedPublicationDomains) != 1 {
		t.Fatal("proof clearing revoked approval", err)
	}
	if _, err = s.ApprovePublisherPublicationDomain(ctx, p.ID, "catalog.example.com", "New commands still require proof", current.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("cleared proof approved", err)
	}
	replayLookups := lookups.Load()
	replay, err := store.Execute(ctx, scope, "exact-check", time.Now().Add(time.Hour), func(context.Context) idempotency.Response {
		t.Error("replay executed DNS command")
		return idempotency.Response{}
	})
	if err != nil || !replay.Replayed || !bytes.Equal(replay.Response.Body, execution.Response.Body) || lookups.Load() != replayLookups {
		t.Fatalf("historical replay changed proof or queried DNS: %v", err)
	}
	changed := scope
	changed.AuthorizationDigest = digestText("revoked domain-admin")
	if _, err = store.Execute(ctx, changed, "exact-check", time.Now().Add(time.Hour), func(context.Context) idempotency.Response {
		t.Error("changed authority executed command")
		return idempotency.Response{}
	}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("changed authority replay accepted", err)
	}
	// An unapproved declaration's state disappears atomically with that domain.
	other, err := s.GetPublisherDomainVerification(ctx, p.ID, "other.example.com")
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.IssuePublisherDomainChallenge(ctx, p.ID, "other.example.com", "Create removable state", other.Version)
	if err != nil {
		t.Fatal(err)
	}
	// Domain cleanup and Publisher audit share the same transaction.
	if _, err = db.operator.Exec(t.Context(), `CREATE FUNCTION reject_publisher_update_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='publisher.updated' THEN RAISE EXCEPTION 'owned removal audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_publisher_update_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_publisher_update_audit()`); err != nil {
		t.Fatal(err)
	}
	removeDomains := []string{"catalog.example.com"}
	if _, err = s.UpdatePublisher(ctx, p.ID, catalog.PatchPublisherInput{Domains: &removeDomains, Reason: "Reject incomplete domain removal"}, p.Revision); err == nil {
		t.Fatal("removal committed without required audit")
	}
	retained, err := s.GetPublisherDomainVerification(ctx, p.ID, "other.example.com")
	if err != nil || retained.Version.ID != old.Status.Version.ID || retained.Version.Revision != old.Status.Version.Revision {
		t.Fatal("failed domain removal deleted verification state", err)
	}
	if _, err = db.operator.Exec(t.Context(), `DROP TRIGGER reject_publisher_update_audit ON audit_events; DROP FUNCTION reject_publisher_update_audit()`); err != nil {
		t.Fatal(err)
	}
	domains := make([]string, 1, 2)
	domains[0] = "catalog.example.com"
	p, err = s.UpdatePublisher(ctx, p.ID, catalog.PatchPublisherInput{Domains: &domains, Reason: "Remove unapproved declaration"}, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	domains = append(domains, "other.example.com")
	p, err = s.UpdatePublisher(ctx, p.ID, catalog.PatchPublisherInput{Domains: &domains, Reason: "Readd declaration"}, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	other, err = s.GetPublisherDomainVerification(ctx, p.ID, "other.example.com")
	if err != nil || other.Version.ID != "" {
		t.Fatal("removal retained challenge identity", err)
	}
	if _, err = s.CheckPublisherDomain(ctx, p.ID, "other.example.com", "Old challenge must remain invalid", old.Status.Version); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("old state returned", err)
	}

	// The new restore stage detects binding corruption without exposing TXT values.
	corrupt, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(t.Context(), corrupt)()
	if _, err = corrupt.Exec(t.Context(), `ALTER TABLE publisher_domain_verifications DISABLE TRIGGER publisher_domain_verification_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err = corrupt.Exec(t.Context(), `UPDATE publisher_domain_verifications SET domain='undeclared.example' WHERE tenant_id=$1 AND id=$2`, p.TenantID, cleared.Version.ID); err != nil {
		t.Fatal(err)
	}
	records, more, err := scanRestorePublisherVerification(t.Context(), corrupt, p.TenantID, "", 20)
	invalid := false
	for _, record := range records {
		if record.finding != nil && record.finding.Code == "RESTORE_PUBLISHER_VERIFICATION_INVALID" {
			invalid = true
		}
	}
	if err != nil || more || !invalid {
		t.Fatalf("restore missed corrupt domain binding: %+v %v", records, err)
	}
	if err = corrupt.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var schemaName string
	if err = db.operator.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&schemaName); err != nil {
		t.Fatal(err)
	}
	if _, err = withTenantRead(t.Context(), db.operator, p.TenantID, func(tx pgx.Tx) (bool, error) { return true, verifyRestoreConstraints(t.Context(), tx, schemaName) }); err != nil {
		t.Error(err)
	}
	if _, err = withTenantRead(t.Context(), db.operator, p.TenantID, func(tx pgx.Tx) (bool, error) { return true, verifyRestoreTriggers(t.Context(), tx, schemaName) }); err != nil {
		t.Error(err)
	}
	// Proof expiry is evaluated after a real Publisher lock wait, not at preparation.
	fresh, err := s.IssuePublisherDomainChallenge(ctx, p.ID, "other.example.com", "Check lock-time expiry", other.Version)
	if err != nil {
		t.Fatal(err)
	}
	value = fresh.Value
	proof, err := s.CheckPublisherDomain(ctx, p.ID, "other.example.com", "Record expiring proof", fresh.Status.Version)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(t.Context(), lock)()
	if _, err = lock.Exec(t.Context(), `SELECT id FROM publishers WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, p.TenantID, p.ID); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, e := s.ApprovePublisherPublicationDomain(ctx, p.ID, "other.example.com", "Expiry after row lock", p.Revision)
		finished <- e
	}()
	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if err = db.operator.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE usename=$1 AND wait_event_type='Lock' AND query LIKE '%FROM publishers%FOR UPDATE%')`, apiRole).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.Advance(proof.ExpiresAt.Sub(c.Now()))
	if err = lock.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-finished:
		if !waiting || catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
			t.Fatalf("approval did not check expiry after lock: waiting=%v err=%v", waiting, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval did not finish after lock release")
	}
}

func TestPostgresPublisherVerificationMigrationPreservesApprovals(t *testing.T) {
	db := newHousekeepingDatabase(t)
	// Execute immutable predecessor inputs in another invocation-owned schema.
	schema := fmt.Sprintf("catalog_domain_legacy_%d", time.Now().UnixNano())
	tx, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(t.Context(), tx)()
	var extensionSchema string
	if err = tx.QueryRow(t.Context(), `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pgcrypto'`).Scan(&extensionSchema); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()+`; SET LOCAL search_path TO `+pgx.Identifier{schema}.Sanitize()+`,`+pgx.Identifier{extensionSchema}.Sanitize()+`,public`); err != nil {
		t.Fatal(err)
	}
	items, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if !strings.HasSuffix(item.Name(), ".sql") || item.Name() >= "000047" {
			continue
		}
		raw, err := fs.ReadFile(migrations.Files, item.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(t.Context(), string(raw)); err != nil {
			t.Fatalf("legacy migration %s: %v", item.Name(), err)
		}
	}
	const publisher = "00000000-0000-7000-8000-000000000111"
	if _, err = tx.Exec(t.Context(), `INSERT INTO tenants(id,display_name,created_at) VALUES($1,'Legacy',now())`, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO publishers(id,tenant_id,display_name,domains,verified_domains,approved_publication_domains,revision,created_at,updated_at) VALUES($1,$2,'Legacy',ARRAY['a.example','b.example'],ARRAY['b.example','undeclared.example'],ARRAY['a.example'],1,now(),now())`, publisher, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	migration, err := fs.ReadFile(migrations.Files, "000047_publisher_domain_verification.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(migration) == 0 {
		t.Fatal("verification migration absent")
	}
	if _, err = tx.Exec(t.Context(), `SAVEPOINT before_cleanup`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), string(migration)); err == nil {
		t.Fatal("inconsistent legacy domains accepted")
	}
	if _, err = tx.Exec(t.Context(), `ROLLBACK TO SAVEPOINT before_cleanup`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE publishers SET verified_domains=ARRAY['b.example'],revision=revision+1 WHERE id=$1`, publisher); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), string(migration)); err != nil {
		t.Fatal(err)
	}
	var approvals []string
	var revision int
	var proofs int
	if err = tx.QueryRow(t.Context(), `SELECT approved_publication_domains,revision,(SELECT count(*) FROM publisher_domain_verifications) FROM publishers WHERE id=$1`, publisher).Scan(&approvals, &revision, &proofs); err != nil || strings.Join(approvals, ",") != "a.example,b.example" || revision != 2 || proofs != 0 {
		t.Fatalf("approval cleanup: %v rev=%d proof=%d %v", approvals, revision, proofs, err)
	}
}

// Each mutation uses the exact API login and valid columns. Check the trigger's
// fixed refusal, so a missing grant or unrelated constraint cannot mask a gap.
func assertPublisherVerificationSQLRefusal(t *testing.T, db *housekeepingDatabase, tenant, stateID, statement, message string) {
	t.Helper()
	_, err := withTenantTx(t.Context(), db.api, tenant, pgx.TxOptions{}, func(tx pgx.Tx) (bool, error) {
		_, err := tx.Exec(t.Context(), statement, tenant, stateID)
		return true, err
	})
	var detail *pgconn.PgError
	if !errors.As(err, &detail) || detail.Code != "P0001" || detail.Message != message {
		t.Fatalf("verification guard returned the wrong refusal: %v", err)
	}
}
