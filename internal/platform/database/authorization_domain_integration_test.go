//go:build integration

package database

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"testing"
	"time"
)

// Revocation occurs after successful domain preflight, immediately before the
// actual repository write. PostgreSQL admission must deny the write and evidence.
type revokingResourceRepository struct {
	catalog.Repository
	before func()
}

func (r *revokingResourceRepository) CreateResource(ctx context.Context, p catalog.CreateResourceParams) (catalog.Resource, error) {
	if r.before != nil {
		r.before()
	}
	return r.Repository.CreateResource(ctx, p)
}
func TestPostgresDomainWriteRechecksAfterPreparatoryPermission(t *testing.T) {
	db, _, identities, admin, alice, _ := authorizationCoreFixture(t)
	adminContext := identity.WithActor(t.Context(), admin)
	grant, err := identities.GrantRole(adminContext, identity.GrantRoleInput{Subject: alice.Subject, ActorType: alice.ActorType, Role: identity.RoleContributor, NamespaceID: authorizationCoreNamespace, Reason: "Explicit scoped author qualification"}, "grant-author")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &revokingResourceRepository{Repository: repo}
	service, err := catalog.NewService(catalog.Options{Repository: wrapped, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	user := identity.WithActor(t.Context(), alice)
	if _, err := service.CreateResource(user, catalog.CreateResourceInput{NamespaceID: authorizationCoreNamespace, Kind: catalog.KindAgent, CanonicalIdentifier: "urn:air:catalog.example.com:core:control"}); err != nil {
		t.Fatal(err)
	}
	counts := func() [3]int {
		t.Helper()
		var c [3]int
		if err := db.operator.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM resources), (SELECT count(*) FROM audit_events WHERE action='resource.created'), (SELECT count(*) FROM outbox_events WHERE event_type='catalog.resource.created')`).Scan(&c[0], &c[1], &c[2]); err != nil {
			t.Fatal(err)
		}
		return c
	}
	before := counts()
	reached := false
	wrapped.before = func() {
		reached = true
		if _, err := identities.RevokeRole(adminContext, grant.ID, "Withdraw author between preflight and commit", "revoke-author", grant.Revision); err != nil {
			t.Fatal(err)
		}
	}
	_, err = service.CreateResource(user, catalog.CreateResourceInput{NamespaceID: authorizationCoreNamespace, Kind: catalog.KindAgent, CanonicalIdentifier: "urn:air:catalog.example.com:core:revoked"})
	if !reached || catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("write did not reject current revocation: reached=%t err=%v", reached, err)
	}
	if after := counts(); after != before {
		t.Fatalf("denied write retained state/evidence: %v -> %v", before, after)
	}
}

func TestPostgresScopedContentReferenceAndMetadataSeparation(t *testing.T) {
	db, _, identities, admin, alice, readerGrant := authorizationCoreFixture(t)
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	owner := identity.WithActor(t.Context(), admin)
	resource, err := service.CreateResource(owner, catalog.CreateResourceInput{NamespaceID: authorizationCoreNamespace, Kind: catalog.KindSkill, CanonicalIdentifier: "urn:air:catalog.example.com:core:scoped-skill"})
	if err != nil {
		t.Fatal(err)
	}
	content := "---\nname: scoped-skill\ndescription: Retained content qualification.\n---\n\nExact instructions.\n"
	_, revision, _, err := service.CreateVersion(owner, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillMarkdown, Content: content}}})
	if err != nil {
		t.Fatal(err)
	}
	document := revision.Representations[0].ArtifactID
	if document == "" {
		t.Fatal("fixture did not retain a document")
	}
	user := identity.WithActor(t.Context(), alice)
	if _, bytes, err := service.GetArtifact(user, document); err != nil || string(bytes) != content {
		t.Fatalf("scoped content refused: %v", err)
	}
	if _, err := identities.RevokeRole(owner, readerGrant.ID, "Replace complete reader with metadata only", "revoke-reader", readerGrant.Revision); err != nil {
		t.Fatal(err)
	}
	role, err := identities.SaveRoleDefinition(owner, "", 0, identity.RoleDefinitionInput{Name: "Metadata only", Permissions: []identity.Permission{"resource.read"}, Reason: "Separate content disclosure"}, false, "metadata-role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identities.GrantAuthorizationAssignment(owner, identity.AccessAssignmentInput{MembershipID: alice.MembershipID, RoleDefinitionID: role.ID, NamespaceID: authorizationCoreNamespace, Reason: "Permit metadata only"}, "metadata-grant"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetResource(user, resource.ID); err != nil {
		t.Fatal("metadata read refused", err)
	}
	if _, _, _, err := service.GetVersionRevision(user, revision.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("metadata permission exposed exact content", err)
	}
	if _, _, err := service.GetArtifact(user, document); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("metadata permission exposed raw document", err)
	}
}

func TestPostgresInternalAdmissionRejectsEndedClaim(t *testing.T) {
	db := newHousekeepingDatabase(t)
	repo, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresJobStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	jobID, err := (id.Generator{}).New(now)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: jobID, TenantID: housekeepingTenantA, Kind: "publication.generate", Actor: "00000000-0000-7000-8000-000000000010", State: jobs.StateQueued, PayloadVersion: 1, Payload: json.RawMessage(`{}`), AttemptLimit: 1, CreatedAt: now, UpdatedAt: now, NotBefore: now}
	if _, _, err := store.Enqueue(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(t.Context(), "lease-qualification", now, time.Minute)
	if err != nil || claimed.ID != jobID {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	ctx, err := identity.WithInternalJobAuthority(t.Context(), claimed.Kind, claimed.TenantID, "catalog.example.com", claimed.ID, claimed.LeaseOwner, claimed.Attempt, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx = identity.WithActor(ctx, identity.Actor{TenantID: claimed.TenantID, Subject: "worker", ActorType: identity.ActorService})
	admitted := func() error {
		_, err := withTenantTx(ctx, db.worker, claimed.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (int, error) { var v int; err := tx.QueryRow(ctx, "SELECT 1").Scan(&v); return v, err })
		return err
	}
	if err := admitted(); err != nil {
		t.Fatal("live lease refused", err)
	}
	if _, err := store.Complete(t.Context(), claimed.TenantID, claimed.ID, claimed.LeaseOwner, now.Add(time.Second), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := admitted(); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("ended claim admitted SQL work", err)
	}
}

func TestPostgresAdministrationAuditUsesAdmittedPolicyRevision(t *testing.T) {
	db, store, service, admin, _, _ := authorizationCoreFixture(t)
	ctx := identity.WithPermissionContext(identity.WithActor(t.Context(), admin))
	decision, err := store.CheckPermissions(ctx, admin, []identity.PermissionCheck{{Permission: "access.manage"}})
	if err != nil {
		t.Fatal(err)
	}
	identity.SetPermissionRevision(ctx, decision.Revision)
	expected := identity.DecisionDigest(ctx)
	role, err := service.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Audit revision control", Permissions: []identity.Permission{"resource.read"}, Reason: "Verify admitted policy evidence"}, false, "audit-revision")
	if err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := db.operator.QueryRow(t.Context(), `SELECT details->>'authorizationDigest' FROM audit_events WHERE tenant_id=$1 AND target_id=$2 AND action='authorization.role.created'`, admin.TenantID, role.ID).Scan(&digest); err != nil || digest != expected {
		t.Fatalf("audit digest=%s want=%s err=%v", digest, expected, err)
	}
	current, err := store.CheckPermissions(ctx, admin, []identity.PermissionCheck{{Permission: "access.manage"}})
	if err != nil || current.Revision <= decision.Revision {
		t.Fatal("mutation did not advance policy", current, err)
	}
	identity.SetPermissionRevision(ctx, current.Revision)
	if digest == identity.DecisionDigest(ctx) {
		t.Fatal("audit used post-mutation revision")
	}
}
