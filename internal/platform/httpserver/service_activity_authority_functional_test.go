//go:build functional

package httpserver

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestServiceActivityRealCurrentMembershipAndAssignmentRevocation(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	store, err := identity.NewMemoryStore([]identity.Seed{
		{TenantID: tenant, Issuer: "activity-test", Subject: "owner", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}},
		{TenantID: tenant, Issuer: "activity-test", Subject: "target", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleReader}},
		{TenantID: tenant, Issuer: "activity-test", Subject: "service", ActorType: identity.ActorService},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewFake(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: c, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	auth := accessLifecycleAuthenticator{store: store, principals: map[string]identity.Principal{}}
	for _, subject := range []string{"owner", "target", "service", "absent"} {
		auth.principals[subject] = identity.Principal{TenantID: tenant, Issuer: "activity-test", Subject: subject, Claims: map[string]string{"scope:tenant.admin": "true"}}
	}
	server := newConfiguredHTTPServer(t, func(d *Dependencies) {
		d.Identity, d.Authenticator, d.Now = service, auth, c.Now
		d.EnforceOAuthScopes = true
		d.Capabilities.ServiceActivity = CapabilityEnabled
	})
	defer server.Close()
	check := func(token string, want int) {
		t.Helper()
		for _, method := range []string{"GET", "HEAD"} {
			response := requestJSON(t, server.URL, method, "/api/v1/service-activity", token, "", "", nil)
			requireStatus(t, response, want)
			if response.header.Get("Cache-Control") != "no-store" || want != 200 && strings.Contains(string(response.body), `"families"`) {
				t.Fatal("unsafe refusal")
			}
		}
	}

	check("target", 200)
	check("service", 403)
	check("absent", 401)
	actor, err := auth.Authenticate(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(context.Background(), actor)
	grants, err := service.ListRoleAssignments(ctx, identity.RoleAssignmentListRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var readerGrant, adminGrant identity.RoleAssignment
	for _, grant := range grants {
		if grant.Subject != "target" {
			continue
		}
		if grant.Role == identity.RoleReader {
			readerGrant = grant
		}
		if grant.Role == identity.RoleTenantAdmin {
			adminGrant = grant
		}
	}
	if readerGrant.ID == "" || adminGrant.ID == "" {
		t.Fatal("missing positive grant controls")
	}
	// access.manage is protected. Only the administrator template supplies it.
	// Removing a separate Reader assignment must not remove that administrator grant.
	if _, err := service.RevokeRole(ctx, readerGrant.ID, "Remove separate Reader assignment", "activity-reader-revoke", readerGrant.Revision); err != nil {
		t.Fatal(err)
	}
	check("target", 200)
	if _, err := service.RevokeRole(ctx, adminGrant.ID, "Remove activity administration", "activity-admin-revoke", adminGrant.Revision); err != nil {
		t.Fatal(err)
	}
	check("target", 403)
	member, err := service.GetMembership(ctx, readerGrant.MembershipID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SuspendMembership(ctx, member.ID, "Suspend activity member", "activity-member-suspend", member.Revision); err != nil {
		t.Fatal(err)
	}
	check("target", 401)
	check("owner", 200)
}

type activityIdentityEvidence struct{ calls atomic.Uint64 }

func (e *activityIdentityEvidence) AppendIdentityEvidence(context.Context, string, identity.ChangeEvidence) error {
	e.calls.Add(1)
	return nil
}

func TestServiceActivityReadCreatesNoRetainedAuditOrOutbox(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	repository := catalog.NewMemoryRepository()
	evidence := &activityIdentityEvidence{}
	var at time.Time
	actual := newConfiguredHTTPServer(t, func(d *Dependencies) {
		at = d.Now()
		c := clock.NewFake(at)
		service, err := catalog.NewService(catalog.Options{Repository: repository, Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: d.CursorKey})
		if err != nil {
			t.Fatal(err)
		}
		d.Catalog = service
		store, err := identity.NewMemoryStore([]identity.Seed{{TenantID: tenant, Issuer: "activity-evidence", Subject: "admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}}}, evidence)
		if err != nil {
			t.Fatal(err)
		}
		d.Identity, err = identity.NewService(identity.ServiceOptions{Store: store, Clock: c, IDs: id.Generator{}, CursorKey: d.CursorKey})
		if err != nil {
			t.Fatal(err)
		}
		auth := accessLifecycleAuthenticator{store: store, principals: map[string]identity.Principal{"admin": {TenantID: tenant, Issuer: "activity-evidence", Subject: "admin"}}}
		d.Authenticator = auth
		// Prove that the attached identity evidence sink observes actual writes.
		actor, err := auth.Authenticate(t.Context(), "admin")
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.Identity.SaveRoleDefinition(identity.WithActor(t.Context(), actor), "", 0, identity.RoleDefinitionInput{Name: "Evidence control", Permissions: []identity.Permission{"resource.read"}, Reason: "Verify identity evidence attachment"}, false, "activity-evidence-control")
		if err != nil || evidence.calls.Load() != 1 {
			t.Fatal("identity evidence control failed", err, evidence.calls.Load())
		}
		d.Capabilities.ServiceActivity = CapabilityEnabled
	})
	defer actual.Close()
	// Prove Catalog writes reach the repository being inspected.
	requireStatus(t, requestJSON(t, actual.URL, "POST", "/api/v1/namespaces", "admin", "activity-evidence-namespace", "", map[string]any{"slug": "evidence-control", "displayName": "Evidence control"}), 201)
	before, err := repository.AuditEvents(t.Context(), tenant, catalog.AuditEventListRequest{Limit: 2})
	if err != nil || len(before) != 1 {
		t.Fatal("Catalog evidence control failed", before, err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		requireStatus(t, requestJSON(t, actual.URL, method, "/api/v1/service-activity", "admin", "", "", nil), 200)
	}
	after, err := repository.AuditEvents(t.Context(), tenant, catalog.AuditEventListRequest{Limit: 2})
	if err != nil || len(after) != 1 || after[0].ID != before[0].ID || evidence.calls.Load() != 1 {
		t.Fatal("Activity read retained new audit evidence", after, err, evidence.calls.Load())
	}
	// Claiming is confined to the disposable repository. Only the control command emitted an event.
	outbox, err := repository.ClaimOutbox(t.Context(), "activity-qualification", at, time.Minute, 2)
	if err != nil || len(outbox) != 1 {
		t.Fatal("Activity read changed the control outbox population", outbox, err)
	}
}
