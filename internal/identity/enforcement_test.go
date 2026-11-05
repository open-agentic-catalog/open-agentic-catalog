package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCurrentAdmissionIgnoresRoleProjectionAndRechecksRevocation(t *testing.T) {
	store, service, admin, alice, _ := administrationFixture(t)
	owner := WithActor(t.Context(), admin)
	role := administrationRole(t, service, owner, "resource.read")
	grant, err := service.GrantAuthorizationAssignment(owner, AccessAssignmentInput{MembershipID: alice.MembershipID, RoleDefinitionID: role.ID, NamespaceID: administrationNamespace, Reason: "Scoped exact metadata"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	// A stale authenticated actor is deliberately retained across policy changes.
	alice.Roles = []Role{RoleTenantAdmin}
	ctx := WithPermissionContext(WithActor(t.Context(), alice))
	if _, err := RequirePermission(ctx, "resource.read", administrationNamespace); err != nil {
		t.Fatal(err)
	}
	for _, check := range []PermissionCheck{{Permission: "resource.read", NamespaceID: administrationOtherNamespace}, {Permission: "resource.read_history", NamespaceID: administrationNamespace}, {Permission: "resource.read_content", NamespaceID: administrationNamespace}, {Permission: "access.manage"}} {
		if _, err := RequirePermission(ctx, check.Permission, check.NamespaceID); ErrorCodeOf(err) != CodeForbidden {
			t.Fatal("projection broadened authority", check, err)
		}
	}
	checks := RecordedPermissionChecks(ctx)
	if len(checks) != 1 {
		t.Fatal("denied checks recorded", checks)
	}
	checks[0].Permission = "access.manage"
	if RecordedPermissionChecks(ctx)[0].Permission != "resource.read" {
		t.Fatal("mutable decision evidence")
	}
	before := DecisionDigest(ctx)
	stable := alice.CommandDigest()
	emitted := 0
	if err := WithReplayDisclosure(ctx, RecordedPermissionChecks(ctx), func(context.Context) error { emitted++; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RevokeAuthorizationAssignment(owner, grant.ID, grant.Revision, "Withdraw scoped metadata", "revoke"); err != nil {
		t.Fatal(err)
	}
	if err := WithReplayDisclosure(ctx, RecordedPermissionChecks(ctx), func(context.Context) error { emitted++; return nil }); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("revoked reply admitted", err)
	}
	if _, err := RequirePermission(ctx, "resource.read", administrationNamespace); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("stale actor retained grant", err)
	}
	after, err := CurrentDecisionDigest(ctx)
	if err != nil || after == before || stable != alice.CommandDigest() || emitted != 1 {
		t.Fatal("decision/command binding", before, after, emitted, err)
	}
	forged := alice
	forged.permissionStore = nil
	if _, err := RequirePermission(WithActor(t.Context(), forged), "access.manage", ""); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("bare administrator admitted", err)
	}
	_ = store
}

func TestUnrelatedPolicyRevisionDoesNotChangeCommandIdentity(t *testing.T) {
	_, service, admin, alice, _ := administrationFixture(t)
	owner := WithActor(t.Context(), admin)
	role := administrationRole(t, service, owner, "resource.read")
	if _, err := service.GrantAuthorizationAssignment(owner, AccessAssignmentInput{MembershipID: alice.MembershipID, RoleDefinitionID: role.ID, NamespaceID: administrationNamespace, Reason: "Scoped read"}, "grant"); err != nil {
		t.Fatal(err)
	}
	ctx := WithPermissionContext(WithActor(t.Context(), alice))
	if _, err := RequirePermission(ctx, "resource.read", administrationNamespace); err != nil {
		t.Fatal(err)
	}
	before := DecisionDigest(ctx)
	command := alice.CommandDigest()
	administrationRole(t, service, owner, "namespace.read")
	called := false
	if err := WithReplayDisclosure(ctx, RecordedPermissionChecks(ctx), func(ctx context.Context) error {
		called = true
		if DecisionDigest(ctx) == before {
			t.Fatal("revision not refreshed")
		}
		return nil
	}); err != nil || !called || command != alice.CommandDigest() {
		t.Fatal("unrelated policy blocked replay", err)
	}
	for _, change := range []func(*Actor){func(a *Actor) { a.RealmID = administrationNamespace }, func(a *Actor) { a.MembershipID = administrationNamespace }, func(a *Actor) { a.TenantID = administrationNamespace }} {
		other := alice
		change(&other)
		if other.CommandDigest() == command {
			t.Fatal("principal collision")
		}
	}
}

func TestDiscoveryAuthoritySeparatesCallerPermissionEvidence(t *testing.T) {
	_, _, admin, _, _ := administrationFixture(t)
	caller := WithPermissionContext(WithActor(t.Context(), admin))
	if _, err := RequirePermission(caller, "discovery.read", ""); err != nil {
		t.Fatal(err)
	}
	before := DecisionDigest(caller)
	if checks := RecordedPermissionChecks(caller); len(checks) != 1 || checks[0].Permission != "discovery.read" {
		t.Fatal("caller admission evidence missing", checks)
	}
	service := Actor{TenantID: admin.TenantID, Subject: "discovery-registry", ActorType: ActorService}
	verified := 0
	bound, err := WithInternalAuthority(WithActor(caller, service), "federation.discover", admin.TenantID, "catalog.example", func(context.Context) error {
		verified++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checks := RecordedPermissionChecks(bound); len(checks) != 0 {
		t.Fatal("caller checks inherited by internal SQL admission", checks)
	}
	if _, err := RequirePermission(bound, "federation.discover", ""); err != nil || verified != 1 {
		t.Fatal("configured discovery authority rejected", verified, err)
	}
	for _, check := range []PermissionCheck{{Permission: "discovery.read"}, {Permission: "access.manage"}, {Permission: "federation.discover", NamespaceID: administrationNamespace}} {
		if _, err := RequirePermission(bound, check.Permission, check.NamespaceID); ErrorCodeOf(err) != CodeForbidden {
			t.Fatal("internal discovery authority broadened", check, err)
		}
	}
	if err := RequireInternalTarget(bound, "federation.discover", admin.TenantID, "other.example"); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("wrong discovery host admitted", err)
	}
	if err := RequireInternalTarget(bound, "federation.discover", administrationOtherNamespace, "catalog.example"); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("wrong discovery tenant admitted", err)
	}
	canceled, cancel := context.WithCancel(bound)
	cancel()
	if _, err := RequirePermission(canceled, "federation.discover", ""); !errors.Is(err, context.Canceled) {
		t.Fatal("discovery cancellation lost", err)
	}
	if checks := RecordedPermissionChecks(caller); len(checks) != 1 || checks[0].Permission != "discovery.read" || DecisionDigest(caller) != before {
		t.Fatal("caller binding changed through internal discovery", checks)
	}
}

func TestInternalExecutionRequiresExactBindingAndLiveVerifier(t *testing.T) {
	actor := Actor{TenantID: administrationTenant, Subject: "catalog-worker", ActorType: ActorService, Roles: []Role{RoleTenantAdmin}}
	base := WithActor(t.Context(), actor)
	if _, err := RequirePermission(base, "source.sync", ""); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("worker name/roles granted authority", err)
	}
	if _, err := WithInternalAuthority(base, "source.sync", actor.TenantID, administrationNamespace, func(context.Context) error { return nil }); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("unclaimed job admitted", err)
	}
	active := true
	verify := func(context.Context) error {
		if !active {
			return E(CodeForbidden, "lease ended")
		}
		return nil
	}
	ctx, err := WithInternalJobAuthority(base, "source.sync", actor.TenantID, administrationNamespace, administrationOtherNamespace, "worker", 1, verify)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RequirePermission(ctx, "source.sync", ""); err != nil {
		t.Fatal(err)
	}
	for _, check := range []PermissionCheck{{Permission: "access.manage"}, {Permission: "publication.generate"}, {Permission: "source.sync", NamespaceID: administrationNamespace}} {
		if _, err := RequirePermission(ctx, check.Permission, check.NamespaceID); ErrorCodeOf(err) != CodeForbidden {
			t.Fatal("internal scope escaped", check, err)
		}
	}
	if err := RequireInternalTarget(ctx, "source.sync", actor.TenantID, administrationOtherNamespace); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("target escaped", err)
	}
	if err := RequireInternalTarget(ctx, "source.sync", administrationOtherNamespace, administrationNamespace); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("tenant escaped", err)
	}
	active = false
	if _, err := RequirePermission(ctx, "source.sync", ""); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("expired binding admitted", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := RequirePermission(canceled, "source.sync", ""); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if lease, ok := InternalLeaseFrom(ctx); !ok || lease.Owner != "worker" || lease.Attempt != 1 || lease.JobID != administrationOtherNamespace {
		t.Fatal("lease binding", lease, ok)
	}
}

type admissionEvidenceSink struct {
	t     *testing.T
	calls int
}

func (s *admissionEvidenceSink) AppendIdentityEvidence(ctx context.Context, _ string, _ ChangeEvidence) error {
	s.calls++
	if NeedsPermissionAdmission(ctx) {
		s.t.Fatal("writer evidence recursively acquired policy admission")
	}
	return nil
}
func TestMemoryPolicyWriterEvidenceJoinsExclusiveAdmission(t *testing.T) {
	store, service, admin, alice, _ := administrationFixture(t)
	sink := &admissionEvidenceSink{t: t}
	store.evidence = sink
	if _, err := service.GrantRole(WithPermissionContext(WithActor(t.Context(), admin)), GrantRoleInput{Subject: alice.Subject, ActorType: alice.ActorType, Role: RoleReader, NamespaceID: administrationNamespace, Reason: "Evidence lock regression"}, "grant"); err != nil {
		t.Fatal(err)
	}
	administrationRole(t, service, WithPermissionContext(WithActor(t.Context(), admin)), "resource.read")
	if sink.calls != 2 {
		t.Fatal("evidence not retained")
	}
}

func TestBoundedAdmissionJoinsOnlyWhileCallbackIsActive(t *testing.T) {
	_, service, admin, alice, _ := administrationFixture(t)
	ctx := WithPermissionContext(WithActor(t.Context(), admin))
	if _, err := RequirePermission(ctx, "resource.read", administrationNamespace); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("owned callback failure")
	var escaped context.Context
	err := WithCurrentPermissionAdmission(ctx, func(admitted context.Context) error {
		escaped = admitted //nolint:fatcontext // Deliberately retain callback input to prove admission expires after return.
		if NeedsPermissionAdmission(admitted) {
			t.Fatal("nested local work attempted a second admission")
		}
		RecordJobDisclosure(admitted, administrationNamespace)
		return sentinel
	})
	if !errors.Is(err, sentinel) || escaped == nil || !NeedsPermissionAdmission(escaped) || !NeedsPermissionAdmission(ctx) {
		t.Fatal("admission leaked after return", err)
	}
	if RecordedJobDisclosure(ctx) != administrationNamespace || RecordedJobDisclosure(t.Context()) != "" {
		t.Fatal("Job disclosure crossed request contexts")
	}
	if IsAdministration(ctx) || !IsAdministration(WithAdministration(ctx)) {
		t.Fatal("ordinary admission became a policy writer")
	}
	// A writer must be able to proceed after the failed callback releases admission.
	if _, err := service.GrantRole(WithActor(t.Context(), admin), GrantRoleInput{Subject: alice.Subject, ActorType: alice.ActorType, Role: RoleReader, Reason: "Verify release after callback failure"}, "grant"); err != nil {
		t.Fatal(err)
	}
	if err := WithReplayDisclosure(t.Context(), []PermissionCheck{{Permission: "resource.read"}}, func(context.Context) error { t.Fatal("unbound replay emitted bytes"); return nil }); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal(err)
	}
}

type malformedDecisionStore struct {
	PermissionStore
	decision PermissionDecision
	failure  error
}

func (s malformedDecisionStore) CheckPermissions(context.Context, Actor, []PermissionCheck) (PermissionDecision, error) {
	return s.decision, s.failure
}

func TestPermissionDecisionFailuresAreNotAuthority(t *testing.T) {
	for _, trial := range []struct {
		name     string
		decision PermissionDecision
		failure  error
		require  ErrorCode
		digest   ErrorCode
	}{
		{"inactive", PermissionDecision{}, E(CodeUnauthenticated, "inactive"), CodeForbidden, CodeUnauthenticated},
		{"unavailable", PermissionDecision{}, E(CodeDependency, "unavailable"), CodeDependency, CodeDependency},
		{"missing result", PermissionDecision{Revision: 1}, nil, CodeForbidden, CodeDependency},
		{"multiple results", PermissionDecision{Revision: 1, Allowed: []bool{true, true}}, nil, CodeForbidden, CodeDependency},
		{"missing revision", PermissionDecision{Allowed: []bool{false}}, nil, CodeForbidden, CodeDependency},
	} {
		t.Run(trial.name, func(t *testing.T) {
			a := BindPermissionStore(Actor{TenantID: administrationTenant, RealmID: administrationNamespace, MembershipID: administrationOtherNamespace}, malformedDecisionStore{decision: trial.decision, failure: trial.failure})
			ctx := WithPermissionContext(WithActor(t.Context(), a))
			if _, err := RequirePermission(ctx, "resource.read", administrationNamespace); ErrorCodeOf(err) != trial.require {
				t.Fatal("bad decision admitted", err)
			}
			if _, err := CurrentDecisionDigest(ctx); ErrorCodeOf(err) != trial.digest {
				t.Fatal("invalid revision became a cursor", err)
			}
			if len(RecordedPermissionChecks(ctx)) != 0 {
				t.Fatal("failed checks retained as replay authority")
			}
		})
	}
	if _, err := CurrentDecisionDigest(t.Context()); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("unbound cursor admitted", err)
	}
}

func TestPermissionCheckBoundRetainsNoExtraAuthority(t *testing.T) {
	_, _, admin, _, _ := administrationFixture(t)
	ctx := WithPermissionContext(WithActor(t.Context(), admin))
	for n := 0; n < MaxPermissionChecks; n++ {
		namespace := fmt.Sprintf("aaaaaaaa-0000-7000-8000-%012x", n+1)
		if _, err := RequirePermission(ctx, "resource.read", namespace); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RequirePermission(ctx, "resource.read", "aaaaaaaa-0000-7000-8000-000000000999"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("unbounded replay checks accepted", err)
	}
	if len(RecordedPermissionChecks(ctx)) != MaxPermissionChecks {
		t.Fatal("check overflow retained")
	}
}

func TestInternalAuthorityRejectsMalformedAndCrossKindClaims(t *testing.T) {
	verify := func(context.Context) error { return nil }
	for _, trial := range []struct {
		kind, tenant, target, job, owner string
		attempt                          int
	}{
		{"unknown", administrationTenant, "host", administrationNamespace, "worker", 1},
		{"source.sync", administrationTenant, "host", "bad", "worker", 1},
		{"source.sync", administrationTenant, "host", administrationNamespace, "", 1},
		{"source.sync", administrationTenant, "host", administrationNamespace, strings.Repeat("x", 257), 1},
		{"source.sync", administrationTenant, "host", administrationNamespace, "worker", 0},
		{"source.sync", "bad", "host", administrationNamespace, "worker", 1},
		{"source.sync", administrationTenant, "", administrationNamespace, "worker", 1},
	} {
		if _, err := WithInternalJobAuthority(t.Context(), trial.kind, trial.tenant, trial.target, trial.job, trial.owner, trial.attempt, verify); ErrorCodeOf(err) != CodeForbidden {
			t.Fatal("malformed authority accepted", trial, err)
		}
	}
	if _, err := WithInternalAuthority(t.Context(), "source.sync", administrationTenant, "host", verify); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("Job admitted without claim", err)
	}
	if _, err := WithInternalAuthority(t.Context(), "federation.discover", administrationTenant, "host", nil); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("unverified authority admitted", err)
	}
	ctx, err := WithInternalAuthority(t.Context(), "federation.discover", administrationTenant, "host", verify)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := InternalLeaseFrom(ctx); ok {
		t.Fatal("public discovery acquired a Job lease")
	}
	if err := RequireInternalTarget(ctx, "federation.discover", administrationTenant, "host"); err != nil {
		t.Fatal(err)
	}
	if err := RequireInternalTarget(ctx, "source.sync", administrationTenant, "host"); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("cross-kind authority admitted", err)
	}
	if err := RequireInternalTarget(t.Context(), "source.sync", administrationTenant, "host"); err != nil {
		t.Fatal("ordinary caller fabricated internal authority", err)
	}
	if _, err := RequirePermission(ctx, "federation.discover", ""); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("missing service principal admitted", err)
	}
}
