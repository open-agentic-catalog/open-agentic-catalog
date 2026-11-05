package identity

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// Alter evidence after service preparation to exercise the store's atomic rejection.
type offboardingEvidenceStore struct {
	Store
	mutate func(*ChangeEvidence)
}

func (s *offboardingEvidenceStore) ChangeMembership(ctx context.Context, params ChangeMembershipParams) (MembershipLifecycleResult, error) {
	if s.mutate != nil {
		s.mutate(&params.Evidence)
	}
	return s.Store.ChangeMembership(ctx, params)
}

func TestOffboardingEvidenceFailurePreservesMembershipAndAllRoles(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000001"
	const namespaceID = "00000000-0000-7000-8000-000000000010"
	const issuer = "https://identity.example.com"
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	sentinel := errors.New("evidence store unavailable")
	for _, test := range []struct {
		name    string
		mutate  func(*ChangeEvidence)
		sinkErr error
		code    ErrorCode
	}{
		{name: "sink unavailable", sinkErr: sentinel, code: CodeDependency},
		{name: "missing audit format", mutate: func(e *ChangeEvidence) { e.FormatVersion = nil }, code: CodeInternal},
		{name: "invalid outbox identifier", mutate: func(e *ChangeEvidence) { e.OutboxID = "invalid" }, code: CodeInternal},
		{name: "wrong notification type", mutate: func(e *ChangeEvidence) { e.OutboxType = "com.open.agentic.catalog.resource.created.v1" }, code: CodeInternal},
		{name: "wrong notification subject", mutate: func(e *ChangeEvidence) { e.OutboxSubject = namespaceID }, code: CodeInternal},
		{name: "wrong acknowledgement time", mutate: func(e *ChangeEvidence) { at := e.At.Add(time.Second); e.OutboxPublished = &at }, code: CodeInternal},
		{name: "invalid notification data", mutate: func(e *ChangeEvidence) { e.OutboxData = []byte(`{"private":true}`) }, code: CodeInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := &recordingIdentityEvidenceSink{err: test.sinkErr}
			store, err := NewMemoryStore([]Seed{
				{TenantID: tenantID, Issuer: issuer, Subject: "admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}},
				{TenantID: tenantID, Issuer: issuer, Subject: "member", ActorType: ActorHuman, Roles: []Role{RoleReader}, Namespaces: map[string][]Role{namespaceID: {RoleContributor, RoleReviewer}}},
			}, sink)
			if err != nil {
				t.Fatal(err)
			}
			boundary := &offboardingEvidenceStore{Store: store, mutate: test.mutate}
			service, err := NewService(ServiceOptions{Store: boundary, Clock: identityTestClock{at: now}, IDs: &identityTestIDs{}, CursorKey: identityTestCursorKey})
			if err != nil {
				t.Fatal(err)
			}
			admin, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "admin"})
			if err != nil {
				t.Fatal(err)
			}
			principal := Principal{Issuer: issuer, TenantID: tenantID, Subject: "member"}
			member, err := store.Resolve(t.Context(), principal)
			if err != nil {
				t.Fatal(err)
			}
			before, err := store.GetMembership(t.Context(), tenantID, member.MembershipID)
			if err != nil {
				t.Fatal(err)
			}
			rolesBefore, err := store.ListRoleAssignments(t.Context(), tenantID, RoleAssignmentListRequest{Limit: 100})
			if err != nil || len(rolesBefore) != 4 {
				t.Fatalf("initial role assignments: %#v, %v", rolesBefore, err)
			}
			ctx := WithActor(t.Context(), admin)
			result, err := service.OffboardMembership(ctx, member.MembershipID, "Membership retired", "request-failure", before.Revision)
			if ErrorCodeOf(err) != test.code || !reflect.DeepEqual(result, MembershipLifecycleResult{}) {
				t.Fatalf("failed offboarding returned %#v, %v", result, err)
			}
			if test.sinkErr != nil && !errors.Is(err, test.sinkErr) {
				t.Fatalf("dependency failure lost its internal cause: %v", err)
			}
			after, err := store.GetMembership(t.Context(), tenantID, member.MembershipID)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("failed offboarding changed membership: %#v, %v", after, err)
			}
			rolesAfter, err := store.ListRoleAssignments(t.Context(), tenantID, RoleAssignmentListRequest{Limit: 100})
			if err != nil || !reflect.DeepEqual(rolesAfter, rolesBefore) {
				t.Fatalf("failed offboarding changed retained roles: %#v, %v", rolesAfter, err)
			}
			resolved, err := store.Resolve(t.Context(), principal)
			if err != nil || !reflect.DeepEqual(resolved, member) {
				t.Fatalf("failed offboarding changed effective access: %#v, %v", resolved, err)
			}
			if len(store.audit) != 0 || len(sink.records) != 0 {
				t.Fatal("failed offboarding retained partial evidence")
			}
			// One recovery case is enough; the malformed-evidence cases exercise
			// independent rejection rules, not different successful transitions.
			if test.sinkErr == nil {
				return
			}

			// The original revision remains usable after the evidence failure is repaired.
			boundary.mutate, sink.err = nil, nil
			result, err = service.OffboardMembership(ctx, member.MembershipID, "Membership retired", "request-retry", before.Revision)
			if err != nil || result.Membership.State != MembershipOffboarded || result.Membership.Revision != before.Revision+1 || result.RevokedRoleAssignments != 5 || result.RevokedNamespaceMemberships != 1 {
				t.Fatalf("offboarding retry returned %#v, %v", result, err)
			}
			if _, err := store.Resolve(t.Context(), principal); ErrorCodeOf(err) != CodeUnauthenticated {
				t.Fatalf("offboarded member retained access: %v", err)
			}
			rolesAfter, err = store.ListRoleAssignments(t.Context(), tenantID, RoleAssignmentListRequest{Limit: 100})
			if err != nil || len(rolesAfter) != len(rolesBefore) {
				t.Fatalf("offboarding lost retained assignment history: %#v, %v", rolesAfter, err)
			}
			for index, assignment := range rolesAfter {
				expected := rolesBefore[index]
				if assignment.MembershipID == member.MembershipID {
					expected.State, expected.Revision = AssignmentRevoked, expected.Revision+1
					expected.RevokedBy, expected.RevokeReason, expected.RevokedAt = admin.Subject, "Membership retired", &now
				}
				if !reflect.DeepEqual(assignment, expected) {
					t.Fatalf("offboarding changed unrelated fields or roles: %#v, want %#v", assignment, expected)
				}
			}
			if len(store.audit) != 1 || len(sink.records) != 1 || sink.records[0].TargetID != member.MembershipID {
				t.Fatal("offboarding retry did not commit exactly one evidence record")
			}
		})
	}
}
