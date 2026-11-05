package identity

import (
	"context"
	"errors"
	"testing"
)

func TestCollectionAdmissionUsesCurrentScopedPolicy(t *testing.T) {
	_, service, admin, alice, _ := administrationFixture(t)
	owner := WithActor(t.Context(), admin)
	role := administrationRole(t, service, owner, "resource.read")
	grant, err := service.GrantAuthorizationAssignment(owner, AccessAssignmentInput{MembershipID: alice.MembershipID, RoleDefinitionID: role.ID, NamespaceID: administrationNamespace, Reason: "Read selected namespace"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	ctx, _, err := WithCollectionContext(WithActor(t.Context(), alice))
	if err != nil || !NeedsCollectionAdmission(ctx) {
		t.Fatal(err)
	}
	escaped := make(chan context.Context, 1)
	_, err = WithCollectionRead(ctx, func(bound context.Context) (bool, error) {
		escaped <- bound
		if NeedsCollectionAdmission(bound) {
			t.Fatal("nested read did not join the snapshot")
		}
		for scope, want := range map[string]bool{"": false, administrationNamespace: true, administrationOtherNamespace: false} {
			got, err := CollectionAllows(bound, "resource.read", scope)
			if err != nil || got != want {
				t.Fatalf("scope=%s allowed=%v want=%v error=%v", scope, got, want, err)
			}
		}
		if len(RecordedPermissionChecks(bound)) != 0 {
			t.Fatal("row decisions grew command evidence")
		}
		return WithCollectionRead(bound, func(context.Context) (bool, error) { return true, nil })
	})
	if err != nil || !NeedsCollectionAdmission(<-escaped) {
		t.Fatal("escaped admission stayed active", err)
	}
	if _, err := service.RevokeAuthorizationAssignment(owner, grant.ID, grant.Revision, "Revoke collection access", "revoke"); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = WithCollectionRead(ctx, func(context.Context) (bool, error) { called = true; return true, nil })
	if ErrorCodeOf(err) != CodeInvalidArgument || called {
		t.Fatal("stale collection admitted", err)
	}
	fresh, _, err := WithCollectionContext(WithActor(t.Context(), alice))
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := CollectionAllows(fresh, "resource.read", administrationNamespace); err != nil || allowed {
		t.Fatal("revoked actor retained access", err)
	}
}

func TestCollectionAdmissionRejectsMissingAndInactivePrincipals(t *testing.T) {
	store, _, _, alice, _ := administrationFixture(t)
	for _, ctx := range []context.Context{t.Context(), WithActor(t.Context(), Actor{TenantID: administrationTenant, Subject: "forged", Roles: []Role{RoleTenantAdmin}})} {
		if _, _, err := WithCollectionContext(ctx); ErrorCodeOf(err) != CodeForbidden {
			t.Fatal("unbound authority admitted", err)
		}
	}
	ctx, _, err := WithCollectionContext(WithActor(t.Context(), alice))
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	member := store.memberships[alice.MembershipID]
	member.State = MembershipOffboarded
	store.memberships[member.ID] = member
	store.mu.Unlock()
	called := false
	_, err = WithCollectionRead(ctx, func(context.Context) (bool, error) { called = true; return true, nil })
	if ErrorCodeOf(err) != CodeForbidden || called {
		t.Fatal("inactive member got an empty success instead of denial", err)
	}
}

func TestCollectionReadCleanupAndValidation(t *testing.T) {
	store, _, _, alice, _ := administrationFixture(t)
	ctx, _, err := WithCollectionContext(WithActor(t.Context(), alice))
	if err != nil {
		t.Fatal(err)
	}
	escaped := make(chan context.Context, 1)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic swallowed")
			}
		}()
		_, _ = WithCollectionRead(ctx, func(bound context.Context) (bool, error) { escaped <- bound; panic("owned test panic") })
	}()
	if !NeedsCollectionAdmission(<-escaped) {
		t.Fatal("panic leaked snapshot")
	}
	if _, err := WithCollectionRead(ctx, func(context.Context) (bool, error) { return true, nil }); err != nil {
		t.Fatal("panic leaked lock", err)
	}
	if err := store.WithCollectionAdmission(ctx, alice, 1, nil); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
	forged := alice
	forged.MembershipID = ""
	if err := store.WithCollectionAdmission(ctx, forged, 1, func(context.Context) error { return nil }); err == nil {
		t.Fatal("incomplete actor admitted")
	}
	other := alice
	other.Subject = "different"
	if err := store.WithCollectionAdmission(ctx, other, 1, func(context.Context) error { return nil }); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("mismatched context admitted", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := WithCollectionRead(canceled, func(context.Context) (bool, error) { t.Fatal("canceled read ran"); return false, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if allowed, err := CollectionAllows(t.Context(), "resource.read", administrationNamespace); err != nil || !allowed {
		t.Fatal("owner repository contract changed", err)
	}
}

func TestCollectionDecisionsFailClosedOnMalformedOrChangedAuthority(t *testing.T) {
	_, _, _, alice, _ := administrationFixture(t)
	bound := BindPermissionStore(alice, malformedDecisionStore{decision: PermissionDecision{Revision: 1, Allowed: []bool{false}}})
	ctx, _, err := WithCollectionContext(WithActor(t.Context(), bound))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := WithCollectionContext(WithActor(t.Context(), BindPermissionStore(alice, malformedDecisionStore{failure: E(CodeDependency, "current policy unavailable")}))); ErrorCodeOf(err) != CodeDependency {
		t.Fatal("collection binding ignored failed policy lookup", err)
	}
	for _, test := range []struct {
		name     string
		decision PermissionDecision
		failure  error
		code     ErrorCode
	}{
		{name: "wrong decision count", decision: PermissionDecision{Revision: 1}, code: CodeDependency},
		{name: "changed revision", decision: PermissionDecision{Revision: 2, Allowed: []bool{true}}, code: CodeInvalidArgument},
		{name: "dependency failure", failure: E(CodeDependency, "permission store unavailable"), code: CodeDependency},
	} {
		t.Run(test.name, func(t *testing.T) {
			actor := BindPermissionStore(alice, malformedDecisionStore{decision: test.decision, failure: test.failure})
			allowed, err := CollectionAllows(WithActor(ctx, actor), "resource.read", administrationNamespace)
			if allowed || ErrorCodeOf(err) != test.code {
				t.Fatal("invalid authority became a row grant", allowed, err)
			}
		})
	}
	unbound := WithActor(ctx, Actor{})
	if allowed, err := CollectionAllows(unbound, "resource.read", administrationNamespace); allowed || ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("missing principal selected rows", err)
	}
	if NeedsCollectionAdmission(unbound) {
		t.Fatal("missing principal claimed a memory admission")
	}
	if _, err := WithCollectionRead(unbound, func(context.Context) (bool, error) { t.Fatal("missing principal ran selection"); return false, nil }); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal(err)
	}
	if err := RequireCollectionTenant(unbound, administrationTenant); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("missing principal crossed tenant boundary", err)
	}
	// Non-memory policy stores leave admission to their repository transaction.
	if NeedsCollectionAdmission(ctx) {
		t.Fatal("SQL-style policy store acquired a memory fence")
	}
	if _, err := WithCollectionRead(ctx, func(context.Context) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestExactReadAdmissionRechecksRecordedPermission(t *testing.T) {
	_, service, admin, alice, _ := administrationFixture(t)
	owner := WithActor(t.Context(), admin)
	role := administrationRole(t, service, owner, "resource.read")
	grant, err := service.GrantAuthorizationAssignment(owner, AccessAssignmentInput{MembershipID: alice.MembershipID, RoleDefinitionID: role.ID, NamespaceID: administrationNamespace, Reason: "Exact read fixture"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPermissionContext(WithActor(t.Context(), alice))
	if _, err := RequirePermission(ctx, "resource.read", administrationNamespace); err != nil {
		t.Fatal(err)
	}
	if !NeedsReadAdmission(ctx) {
		t.Fatal("recorded exact read has no admission")
	}
	if _, err := WithReadAdmission(ctx, func(bound context.Context) (bool, error) {
		if NeedsReadAdmission(bound) {
			t.Fatal("admitted exact read did not join policy fence")
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RevokeAuthorizationAssignment(owner, grant.ID, grant.Revision, "Withdraw before repository lock", "revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err := WithReadAdmission(ctx, func(context.Context) (bool, error) { t.Fatal("revoked exact read ran"); return false, nil }); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("recorded read bypassed current policy", err)
	}
}
