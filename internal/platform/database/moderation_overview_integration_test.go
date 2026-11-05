//go:build integration

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"testing"
	"time"
)

func TestPostgresModerationOverviewVisibilityOldestAndBounds(t *testing.T) {
	db, repo, _, seed := newSearchCostFixture(t)
	seed(0)
	adminContext := semanticDisclosureActor(t, db, housekeepingTenantA)
	admin, _ := identity.FromContext(adminContext)
	moderator, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: admin.TenantID, TenantDisplayName: "Housekeeping tenant A", Issuer: admin.Issuer, TenancyMode: "dedicated", Subject: "summary-moderator", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleServiceClient}, Operator: "overview", Reason: "Qualify a moderator without Resource reads"})
	if err != nil {
		t.Fatal(err)
	}
	access, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewFake(time.Now().UTC().Truncate(time.Microsecond))
	identities, err := identity.NewService(identity.ServiceOptions{Store: access, Clock: c, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	role, err := identities.SaveRoleDefinition(adminContext, "", 0, identity.RoleDefinitionInput{Name: "Summary moderator", Permissions: []identity.Permission{"feedback.moderate", "ratings.moderate"}, Reason: "Qualify exact moderation summaries"}, false, "summary-role")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := identities.GrantAuthorizationAssignment(adminContext, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: moderator.MembershipID, Reason: "Qualify exact summary access"}, "summary-grant")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := access.ResolvePrincipal(t.Context(), identity.Principal{TenantID: admin.TenantID, Issuer: admin.Issuer, Subject: "summary-moderator"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), actor)
	decision, err := access.CheckPermissions(ctx, actor, []identity.PermissionCheck{{Permission: "resource.read"}})
	if err != nil || decision.Allowed[0] {
		t.Fatal(decision, err)
	}
	resources, err := catalog.NewService(catalog.Options{Repository: repo, Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte("01234567890123456789012345678901"), Policy: "search-cost-test"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := repo.SearchCatalog(adminContext, admin.TenantID, catalog.CatalogSearchRepositoryRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	resource := page.Items[0].Resource
	fs, err := NewPostgresFeedbackStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	feedbackService, err := feedback.NewService(fs, repo, c, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := resources.CreateResource(adminContext, catalog.CreateResourceInput{NamespaceID: resource.NamespaceID, CanonicalIdentifier: "urn:air:catalog.example.com:summary:deleted", Kind: catalog.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = feedbackService.Create(adminContext, feedback.CreateInput{ResourceID: draft.ID, Category: "problem", Comment: "Deleted Resource feedback"}); err != nil {
		t.Fatal(err)
	}
	if err = resources.DeleteResource(adminContext, draft.ID, "Qualify deleted Resource selection", draft.Revision); err != nil {
		t.Fatal(err)
	}
	c.Advance(time.Hour)
	oldest := c.Now()
	var first feedback.Record
	for _, category := range []string{"problem", "documentation", "suggestion", "positive", "other"} {
		row, err := feedbackService.Create(adminContext, feedback.CreateInput{ResourceID: resource.ID, Category: category, Comment: "Private summary fixture"})
		if err != nil {
			t.Fatal(err)
		}
		if category == "problem" {
			first = row
		}
	}
	c.Advance(time.Hour)
	first, err = feedbackService.Change(adminContext, first.ID, first.Revision, "address", feedback.EditInput{}, "Reviewed")
	if err != nil {
		t.Fatal(err)
	}
	first, err = feedbackService.Change(adminContext, first.ID, first.Revision, "reopen", feedback.EditInput{}, "")
	if err != nil {
		t.Fatal(err)
	}
	fo, err := feedbackService.Overview(ctx)
	if err != nil || fo.Open.Value != 5 || fo.OldestOpenAt == nil || !fo.OldestOpenAt.Equal(oldest) || first.CreatedAt != oldest {
		t.Fatal(fo, err)
	}
	for _, category := range fo.Categories {
		if category.Count != 1 {
			t.Fatal(fo)
		}
	}
	// Seed retained foreign opinions, then remove their Source. Metadata counts must not re-admit their targets.
	source, err := db.federation.CreateSource(t.Context(), housekeepingSource("00000000-0000-7000-8000-000000009400", admin.TenantID, c.Now()))
	if err != nil {
		t.Fatal(err)
	}
	target := ratings.Target{Origin: "foreign", SourceID: source.ID, PresentationGeneration: federation.EffectivePresentationGeneration(source), Identifier: "urn:air:catalog.example.com:summary:foreign", VersionPresent: true, Version: "1", Kind: catalog.KindAgent}
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	key := ratings.Key(target)
	if _, err = db.operator.Exec(t.Context(), `INSERT INTO rating_targets(tenant_id,target_key,target,source_id) VALUES($1,$2,$3,$4)`, admin.TenantID, key, raw, source.ID); err != nil {
		t.Fatal(err)
	}
	for i, state := range []string{"active", "withdrawn", "removed"} {
		principal, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: admin.TenantID, TenantDisplayName: "Housekeeping tenant A", Issuer: admin.Issuer, TenancyMode: "dedicated", Subject: fmt.Sprint("rating-summary-", i), ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}, Operator: "overview", Reason: "Qualify retained rating states"})
		if err != nil {
			t.Fatal(err)
		}
		principalKey, err := ratings.PrincipalKey(identity.Actor{TenantID: admin.TenantID, Issuer: admin.Issuer, Subject: principal.Subject, ActorType: identity.ActorHuman})
		if err != nil {
			t.Fatal(err)
		}
		voteID := fmt.Sprintf("00000000-0000-7000-9000-%012d", i+1)
		if _, err = db.operator.Exec(t.Context(), `INSERT INTO item_ratings(tenant_id,id,target_key,principal_key,submitted_by,score,consent,state,revision,created_at,updated_at) VALUES($1,$2,$3,$4,$5,4,false,'active',1,$6,$6)`, admin.TenantID, voteID, key, principalKey, principal.MembershipID, c.Now()); err != nil {
			t.Fatal(err)
		}
		if state != "active" {
			if _, err = db.operator.Exec(t.Context(), `UPDATE item_ratings SET state=$3,score=0,consent=false,revision=2 WHERE tenant_id=$1 AND id=$2`, admin.TenantID, voteID, state); err != nil {
				t.Fatal(err)
			}
		}
	}
	source.Status = federation.SourceRemoved
	source.Revision++
	if _, err = db.federation.UpdateSource(t.Context(), source, source.Revision-1); err != nil {
		t.Fatal(err)
	}
	rs, err := NewPostgresRatingsStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	ratingService, err := ratings.New(rs, postgresRatingAuthority(func(context.Context, ratings.Admission) (ratings.Admission, error) {
		t.Error("summary attempted target admission")
		return ratings.Admission{}, fmt.Errorf("unavailable target")
	}), c, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ro, err := ratingService.Overview(ctx)
	if err != nil || ro.Total.Value != 3 || ro.States[0].Count != 1 || ro.States[1].Count != 1 || ro.States[2].Count != 1 {
		t.Fatal(ro, err)
	}
	other := semanticDisclosureActor(t, db, housekeepingTenantB)
	fo, err = feedbackService.Overview(other)
	if err != nil || fo.Open.Value != 0 || fo.OldestOpenAt != nil {
		t.Fatal(fo, err)
	}
	ro, err = ratingService.Overview(other)
	if err != nil || ro.Total.Value != 0 {
		t.Fatal(ro, err)
	}
	if _, err = db.operator.Exec(t.Context(), `INSERT INTO item_feedback(id,tenant_id,membership_id,resource_id,category,comment,response,state,revision,created_at,updated_at) SELECT uuidv7(),$1,$2,$3,'problem','Owned bounded summary fixture','','open',1,$4,$4 FROM generate_series(1,10001)`, admin.TenantID, admin.MembershipID, resource.ID, c.Now()); err != nil {
		t.Fatal(err)
	}
	fo, err = feedbackService.Overview(ctx)
	if err != nil || !fo.Open.Limited || fo.Open.Value != 10000 || fo.Categories != nil || fo.OldestOpenAt == nil || !fo.OldestOpenAt.Equal(oldest) {
		t.Fatal(fo, err)
	}
	if _, err = db.operator.Exec(t.Context(), `WITH members AS (INSERT INTO tenant_memberships(id,tenant_id,realm_id,subject,actor_type,state,revision,created_at,updated_at) SELECT uuidv7(),$1,$2,'overview-scale-rating-'||n,'human','active',1,$4,$4 FROM generate_series(1,10001) n RETURNING id,subject) INSERT INTO item_ratings(tenant_id,id,target_key,principal_key,submitted_by,score,consent,state,revision,created_at,updated_at) SELECT $1,uuidv7(),$3,'sha256:'||encode(digest(convert_to('["'||$1::uuid::text||'","'||$5||'","'||subject||'"]','UTF8'),'sha256'),'hex'),id,4,false,'active',1,$4,$4 FROM members`, admin.TenantID, admin.RealmID, key, c.Now(), admin.Issuer); err != nil {
		t.Fatal(err)
	}
	ro, err = ratingService.Overview(ctx)
	if err != nil || !ro.Total.Limited || ro.Total.Value != 10000 || ro.States != nil {
		t.Fatal(ro, err)
	}
	if _, err = identities.RevokeAuthorizationAssignment(adminContext, grant.ID, grant.Revision, "Qualify current summary revocation", "summary-revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err = feedbackService.Overview(ctx); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal(err)
	}
	if _, err = ratingService.Overview(ctx); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal(err)
	}
}
