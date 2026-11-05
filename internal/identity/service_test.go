package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type identityTestClock struct{ at time.Time }

func (c identityTestClock) Now() time.Time { return c.at }

var identityTestCursorKey = []byte("01234567890123456789012345678901")

type identityTestIDs struct{ next int }

func (g *identityTestIDs) New(time.Time) (string, error) {
	g.next++
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", g.next), nil
}

type failingIdentityIDs struct {
	next   int
	failAt int
}

type recordingIdentityEvidenceSink struct {
	tenantIDs []string
	records   []ChangeEvidence
	err       error
}

func (s *recordingIdentityEvidenceSink) AppendIdentityEvidence(_ context.Context, tenantID string, evidence ChangeEvidence) error {
	if s.err != nil {
		return s.err
	}
	s.tenantIDs = append(s.tenantIDs, tenantID)
	s.records = append(s.records, evidence)
	return nil
}

func (g *failingIdentityIDs) New(time.Time) (string, error) {
	g.next++
	if g.next == g.failAt {
		return "", errors.New("identifier unavailable")
	}
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", g.next), nil
}

func TestIdentityServiceOwnsRoleLifecycleAndFinalAdministratorInvariant(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const namespaceID = "00000000-0000-7000-8000-000000000010"
	const issuer = "https://identity.example.com"
	evidence := &recordingIdentityEvidenceSink{}
	store, err := NewMemoryStore([]Seed{{TenantID: tenantID, Issuer: issuer, Subject: "admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}}}, evidence)
	if err != nil {
		t.Fatal(err)
	}
	ids := &identityTestIDs{}
	service, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{at: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)}, IDs: ids, CursorKey: identityTestCursorKey, AcknowledgeOutboxAtCreation: true})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Resolve(context.Background(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithActor(context.Background(), admin)
	if _, err := service.RevokeRole(ctx, assignmentIDFor(t, store, tenantID, "admin", RoleTenantAdmin), "cannot remove final administrator", "request-1", 1); ErrorCodeOf(err) != CodeSeparationOfDuties {
		t.Fatalf("final administrator revocation returned %v", err)
	}

	reader, err := service.GrantRole(ctx, GrantRoleInput{Subject: "service-reader", ActorType: ActorService, Role: RoleReader, Reason: "Allow service discovery"}, "request-2")
	if err != nil {
		t.Fatal(err)
	}
	if reader.State != AssignmentActive || reader.ActorType != ActorService || reader.Revision != 1 {
		t.Fatalf("granted assignment = %#v", reader)
	}
	resolved, err := store.Resolve(context.Background(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "service-reader"})
	if err != nil || resolved.ActorType != ActorService || !resolved.HasRole(RoleReader, "") {
		t.Fatalf("resolved service actor = %#v, %v", resolved, err)
	}
	if _, err := service.GrantRole(ctx, GrantRoleInput{Subject: "service-reader", ActorType: ActorService, Role: RoleReader, Reason: "Duplicate"}, "request-3"); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("duplicate role grant returned %v", err)
	}
	if _, err := service.GrantRole(ctx, GrantRoleInput{Subject: "service-reader", ActorType: ActorService, Role: RoleTenantAdmin, NamespaceID: namespaceID, Reason: "Invalid scope"}, "request-4"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("namespace tenant administrator grant returned %v", err)
	}

	contributor, err := service.GrantRole(ctx, GrantRoleInput{Subject: "service-reader", ActorType: ActorService, Role: RoleContributor, NamespaceID: namespaceID, Reason: "Allow namespace authoring"}, "request-5")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = store.Resolve(context.Background(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "service-reader"})
	if err != nil || !resolved.HasRole(RoleContributor, namespaceID) {
		t.Fatalf("resolved namespace role = %#v, %v", resolved, err)
	}
	if _, err := service.RevokeRole(ctx, contributor.ID, "stale request", "request-6", 2); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("stale role revocation returned %v", err)
	}
	revoked, err := service.RevokeRole(ctx, contributor.ID, "Namespace access ended", "request-7", contributor.Revision)
	if err != nil || revoked.State != AssignmentRevoked || revoked.Revision != 2 || revoked.RevokedAt == nil {
		t.Fatalf("revoked role = %#v, %v", revoked, err)
	}

	secondAdmin, err := service.GrantRole(ctx, GrantRoleInput{Subject: "second-admin", ActorType: ActorHuman, Role: RoleTenantAdmin, Reason: "Maintain administrator quorum"}, "request-8")
	if err != nil {
		t.Fatal(err)
	}
	firstAdminID := assignmentIDFor(t, store, tenantID, "admin", RoleTenantAdmin)
	if _, err := service.RevokeRole(ctx, firstAdminID, "Administrator offboarding", "request-9", 1); err != nil {
		t.Fatal(err)
	}
	secondActor, err := store.Resolve(context.Background(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "second-admin"})
	if err != nil || !secondActor.HasRole(RoleTenantAdmin, "") {
		t.Fatalf("second administrator = %#v, %v", secondActor, err)
	}
	if _, err := service.RevokeRole(WithActor(context.Background(), secondActor), secondAdmin.ID, "cannot remove final administrator", "request-10", secondAdmin.Revision); ErrorCodeOf(err) != CodeSeparationOfDuties {
		t.Fatalf("new final administrator revocation returned %v", err)
	}
	if len(store.audit) != 5 {
		t.Fatalf("successful identity audit events = %d, want 5", len(store.audit))
	}
	if len(evidence.records) != len(store.audit) || len(evidence.tenantIDs) != len(store.audit) {
		t.Fatalf("shared identity evidence = records %d tenants %d, want %d", len(evidence.records), len(evidence.tenantIDs), len(store.audit))
	}
	for index, record := range evidence.records {
		if evidence.tenantIDs[index] != tenantID || record.FormatVersion == nil || record.OutboxID == "" || record.OutboxType == "" || record.OutboxSubject != record.TargetID || record.OutboxPublished == nil || !record.OutboxPublished.Equal(record.At) {
			t.Fatalf("shared identity evidence %d = tenant %q record %#v", index, evidence.tenantIDs[index], record)
		}
	}
}

func TestMemoryIdentityEvidenceFailureRollsBackTransitions(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const issuer = "https://identity.example.com"
	sentinel := errors.New("shared evidence unavailable")
	sink := &recordingIdentityEvidenceSink{}
	store, err := NewMemoryStore([]Seed{
		{TenantID: tenantID, Issuer: issuer, Subject: "admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}},
		{TenantID: tenantID, Issuer: issuer, Subject: "second-admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{at: time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)}, IDs: &identityTestIDs{}, CursorKey: identityTestCursorKey})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithActor(t.Context(), admin)

	sink.err = sentinel
	if _, err := service.GrantRole(ctx, GrantRoleInput{Subject: "reader", ActorType: ActorHuman, Role: RoleReader, Reason: "Grant governed access"}, "request-grant"); ErrorCodeOf(err) != CodeDependency || !errors.Is(err, sentinel) {
		t.Fatalf("failed shared grant evidence returned %v", err)
	}
	if _, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "reader"}); ErrorCodeOf(err) != CodeUnauthenticated {
		t.Fatalf("failed grant retained a membership: %v", err)
	}

	second, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "second-admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SuspendMembership(ctx, second.MembershipID, "Pause access", "request-suspend", 1); ErrorCodeOf(err) != CodeDependency || !errors.Is(err, sentinel) {
		t.Fatalf("failed shared suspension evidence returned %v", err)
	}
	retained, err := store.GetMembership(t.Context(), tenantID, second.MembershipID)
	if err != nil || retained.State != MembershipActive || retained.Revision != 1 {
		t.Fatalf("membership after failed evidence = %#v, %v", retained, err)
	}

	sink.err = nil
	reader, err := service.GrantRole(ctx, GrantRoleInput{Subject: "reader", ActorType: ActorHuman, Role: RoleReader, Reason: "Grant governed access"}, "request-grant-success")
	if err != nil {
		t.Fatal(err)
	}
	sink.err = sentinel
	if _, err := service.RevokeRole(ctx, reader.ID, "Remove governed access", "request-revoke", reader.Revision); ErrorCodeOf(err) != CodeDependency || !errors.Is(err, sentinel) {
		t.Fatalf("failed shared revocation evidence returned %v", err)
	}
	assignments, err := store.ListRoleAssignments(t.Context(), tenantID, RoleAssignmentListRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, assignment := range assignments {
		if assignment.ID == reader.ID && (assignment.State != AssignmentActive || assignment.Revision != 1) {
			t.Fatalf("assignment after failed evidence = %#v", assignment)
		}
	}
	if len(store.audit) != 1 || len(sink.records) != 1 {
		t.Fatalf("failed changes retained evidence: local %d shared %d", len(store.audit), len(sink.records))
	}

	if _, err := NewMemoryStore(nil, nil); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("nil evidence sink returned %v", err)
	}
	if _, err := NewMemoryStore(nil, sink, sink); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("multiple evidence sinks returned %v", err)
	}
}

func TestIdentityResolutionIsExactAndTenantBound(t *testing.T) {
	const tenantOne = "00000000-0000-7000-8000-000000000001"
	const tenantTwo = "00000000-0000-7000-8000-000000000002"
	const issuer = "https://identity.example.com"
	store, err := NewMemoryStore([]Seed{
		{TenantID: tenantOne, Issuer: issuer, ExternalOrganization: "organization-one", Subject: "member", ActorType: ActorHuman, Roles: []Role{RoleReader}},
		{TenantID: tenantTwo, Issuer: issuer, ExternalOrganization: "organization-two", Subject: "member", ActorType: ActorHuman, Roles: []Role{RoleContributor}},
	})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := store.Resolve(context.Background(), Principal{Issuer: issuer, ExternalOrganization: "organization-two", Subject: "member", Claims: map[string]string{"scope:catalog.read": "true"}})
	if err != nil || actor.TenantID != tenantTwo || !actor.HasRole(RoleContributor, "") || actor.HasRole(RoleReader, "") {
		t.Fatalf("shared identity resolution = %#v, %v", actor, err)
	}
	actor.Claims["scope:catalog.read"] = "changed"
	again, err := store.Resolve(context.Background(), Principal{Issuer: issuer, ExternalOrganization: "organization-two", Subject: "member", Claims: map[string]string{"scope:catalog.read": "true"}})
	if err != nil || again.Claims["scope:catalog.read"] != "true" {
		t.Fatalf("identity claims were not isolated: %#v, %v", again, err)
	}
	for name, principal := range map[string]Principal{
		"unknown organization": {Issuer: issuer, ExternalOrganization: "unknown", Subject: "member"},
		"wrong issuer":         {Issuer: "https://other.example.com", ExternalOrganization: "organization-two", Subject: "member"},
		"unknown subject":      {Issuer: issuer, ExternalOrganization: "organization-two", Subject: "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Resolve(context.Background(), principal); ErrorCodeOf(err) != CodeUnauthenticated {
				t.Fatalf("unprovisioned identity returned %v", err)
			}
		})
	}
}

func TestMembershipAdministrationIsBoundedAndLifecycleSafe(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const otherTenantID = "00000000-0000-7000-8000-000000000002"
	const namespaceID = "00000000-0000-7000-8000-000000000010"
	const issuer = "https://identity.example.com"
	store, err := NewMemoryStore([]Seed{
		{TenantID: tenantID, Issuer: issuer, Subject: "admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}},
		{TenantID: tenantID, Issuer: issuer, Subject: "member", ActorType: ActorHuman, Roles: []Role{RoleReader}, Namespaces: map[string][]Role{namespaceID: {RoleContributor}}},
		{TenantID: tenantID, Issuer: issuer, Subject: "z-admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}},
		{TenantID: otherTenantID, Issuer: issuer, Subject: "other-admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	service, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{at: now}, IDs: &identityTestIDs{}, CursorKey: identityTestCursorKey, AcknowledgeOutboxAtCreation: true})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	adminContext := WithActor(t.Context(), admin)
	first, err := service.ListMemberships(adminContext, 2, "")
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" || first.Items[0].Subject != "admin" || first.Items[1].Subject != "member" {
		t.Fatalf("first membership page = %#v, %v", first, err)
	}
	second, err := service.ListMemberships(adminContext, 2, first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.Items[0].Subject != "z-admin" || second.NextCursor != "" {
		t.Fatalf("second membership page = %#v, %v", second, err)
	}
	if _, err := service.ListMemberships(adminContext, 1, first.NextCursor); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("cursor reused with another limit returned %v", err)
	}
	tamperedSuffix := "0"
	if strings.HasSuffix(first.NextCursor, tamperedSuffix) {
		tamperedSuffix = "1"
	}
	tampered := first.NextCursor[:len(first.NextCursor)-1] + tamperedSuffix
	if _, err := service.ListMemberships(adminContext, 2, tampered); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("tampered cursor returned %v", err)
	}
	memberID := first.Items[1].ID
	membership, err := service.GetMembership(adminContext, memberID)
	if err != nil || membership.Subject != "member" {
		t.Fatalf("membership read = %#v, %v", membership, err)
	}
	otherMemberships, err := store.ListMemberships(t.Context(), otherTenantID, "", "", 10)
	if err != nil || len(otherMemberships) != 1 {
		t.Fatalf("other memberships = %#v, %v", otherMemberships, err)
	}
	if _, err := service.GetMembership(adminContext, otherMemberships[0].ID); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("cross-tenant membership read returned %v", err)
	}

	// Model a retained in-memory timestamp with finer precision than PostgreSQL.
	// The next transition must round its floor up, never back into history.
	membership.UpdatedAt = now.Add(999 * time.Nanosecond)
	store.memberships[memberID] = membership
	orderedAt := now.Add(time.Microsecond)
	suspended, err := service.SuspendMembership(adminContext, memberID, "Temporary access hold", "request-suspend", membership.Revision)
	if err != nil || suspended.Membership.State != MembershipSuspended || suspended.Membership.Revision != 2 {
		t.Fatalf("suspended membership = %#v, %v", suspended, err)
	}
	if !suspended.Membership.UpdatedAt.Equal(orderedAt) {
		t.Fatal("suspension did not preserve the retained timestamp floor")
	}
	if _, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "member"}); ErrorCodeOf(err) != CodeUnauthenticated {
		t.Fatalf("suspended membership resolved: %v", err)
	}
	service.clock = identityTestClock{at: now.Add(-2 * time.Second)}
	if _, err := service.ReactivateMembership(adminContext, memberID, "Stale revision", "request-stale", 1); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("stale reactivation returned %v", err)
	}
	reactivated, err := service.ReactivateMembership(adminContext, memberID, "Access restored", "request-reactivate", suspended.Membership.Revision)
	if err != nil || reactivated.Membership.State != MembershipActive {
		t.Fatalf("reactivated membership = %#v, %v", reactivated, err)
	}
	if reactivated.Membership.UpdatedAt.Before(suspended.Membership.UpdatedAt) {
		t.Fatal("clock correction moved membership history backwards")
	}
	offboarded, err := service.OffboardMembership(adminContext, memberID, "Employment ended", "request-offboard", reactivated.Membership.Revision)
	if err != nil || offboarded.Membership.State != MembershipOffboarded || offboarded.RevokedRoleAssignments != 4 || offboarded.RevokedNamespaceMemberships != 1 {
		t.Fatalf("offboarded membership = %#v, %v", offboarded, err)
	}
	if !offboarded.Membership.UpdatedAt.Equal(reactivated.Membership.UpdatedAt) {
		t.Fatal("offboarding did not retain the previous transition time floor")
	}
	for _, evidence := range store.audit {
		if evidence.TargetID == memberID && (!evidence.At.Equal(orderedAt) || evidence.OutboxPublished == nil || !evidence.OutboxPublished.Equal(orderedAt)) {
			t.Fatal("membership audit/outbox timestamp differs from the ordered transition")
		}
	}
	if _, err := service.ReactivateMembership(adminContext, memberID, "Must remain terminal", "request-terminal", offboarded.Membership.Revision); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("offboarded membership reactivation returned %v", err)
	}

	adminMembership, err := service.GetMembership(adminContext, admin.MembershipID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SuspendMembership(adminContext, admin.MembershipID, "Second administrator remains", "request-admin-suspend", adminMembership.Revision); err != nil {
		t.Fatal(err)
	}
	zAdmin, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "z-admin"})
	if err != nil {
		t.Fatal(err)
	}
	zAdminMembership, err := service.GetMembership(WithActor(t.Context(), zAdmin), zAdmin.MembershipID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.OffboardMembership(WithActor(t.Context(), zAdmin), zAdmin.MembershipID, "Cannot remove final administrator", "request-final", zAdminMembership.Revision); ErrorCodeOf(err) != CodeSeparationOfDuties {
		t.Fatalf("final administrator offboarding returned %v", err)
	}
}

func TestMembershipAdministrationRejectsUnauthorizedUnsafeAndStaleRequests(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const issuer = "https://identity.example.com"
	store, err := NewMemoryStore([]Seed{
		{TenantID: tenantID, Issuer: issuer, Subject: "admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}},
		{TenantID: tenantID, Issuer: issuer, Subject: "other-admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}},
		{TenantID: tenantID, Issuer: issuer, Subject: "member", ActorType: ActorHuman, Roles: []Role{RoleReader}},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	service, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{at: now}, IDs: &identityTestIDs{}, CursorKey: identityTestCursorKey, AcknowledgeOutboxAtCreation: true})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	otherAdmin, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "other-admin"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "member"})
	if err != nil {
		t.Fatal(err)
	}
	adminContext := WithActor(t.Context(), admin)
	memberContext := WithActor(t.Context(), member)

	if _, err := service.ListMemberships(t.Context(), 10, ""); ErrorCodeOf(err) != CodeUnauthenticated {
		t.Fatalf("anonymous membership list returned %v", err)
	}
	if _, err := service.ListMemberships(memberContext, 10, ""); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("reader membership list returned %v", err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := service.ListMemberships(adminContext, limit, ""); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("membership limit %d returned %v", limit, err)
		}
	}
	if _, err := service.GetMembership(adminContext, "not-a-uuid"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid membership identifier returned %v", err)
	}
	if _, err := service.GetMembership(memberContext, member.MembershipID); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("reader membership detail returned %v", err)
	}

	first, err := service.ListMemberships(adminContext, 1, "")
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first secured membership page = %#v, %v", first, err)
	}
	if _, err := service.ListMemberships(WithActor(t.Context(), otherAdmin), 1, first.NextCursor); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("cursor reused by another administrator returned %v", err)
	}
	expiredService, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{at: now.Add(membershipCursorTTL)}, IDs: &identityTestIDs{}, CursorKey: identityTestCursorKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expiredService.ListMemberships(adminContext, 1, first.NextCursor); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("expired membership cursor returned %v", err)
	}

	for name, request := range map[string]struct {
		membershipID string
		reason       string
		revision     int64
	}{
		"invalid identifier": {membershipID: "not-a-uuid", reason: "reason", revision: 1},
		"missing reason":     {membershipID: member.MembershipID, revision: 1},
		"missing revision":   {membershipID: member.MembershipID, reason: "reason"},
		"oversized reason":   {membershipID: member.MembershipID, reason: strings.Repeat("x", 2001), revision: 1},
		"too many runes":     {membershipID: member.MembershipID, reason: strings.Repeat("\u00e9", 2001), revision: 1},
		"control character":  {membershipID: member.MembershipID, reason: "line\nbreak", revision: 1},
		"invalid utf-8":      {membershipID: member.MembershipID, reason: string([]byte{0xff}), revision: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.SuspendMembership(adminContext, request.membershipID, request.reason, "request-invalid", request.revision); ErrorCodeOf(err) != CodeInvalidArgument {
				t.Fatalf("unsafe membership transition returned %v", err)
			}
		})
	}
	if _, err := service.SuspendMembership(memberContext, member.MembershipID, "Unauthorized", "request-forbidden", 1); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("reader membership transition returned %v", err)
	}

	suspended, err := service.SuspendMembership(adminContext, member.MembershipID, "Temporary hold", "request-suspend", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SuspendMembership(adminContext, member.MembershipID, "Duplicate hold", "request-suspend-again", suspended.Membership.Revision); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("duplicate membership suspension returned %v", err)
	}
	store.mu.Lock()
	realm := store.realms[member.RealmID]
	realm.State = RealmSuspended
	store.realms[member.RealmID] = realm
	store.mu.Unlock()
	if _, err := service.ReactivateMembership(adminContext, member.MembershipID, "Realm remains unavailable", "request-realm-suspended", suspended.Membership.Revision); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("suspended-realm membership reactivation returned %v", err)
	}
	store.mu.Lock()
	realm.State = RealmActive
	store.realms[member.RealmID] = realm
	store.mu.Unlock()
	reactivated, err := service.ReactivateMembership(adminContext, member.MembershipID, "Realm restored", "request-reactivate", suspended.Membership.Revision)
	if err != nil {
		t.Fatal(err)
	}
	offboarded, err := service.OffboardMembership(adminContext, member.MembershipID, "Membership retired", "request-offboard", reactivated.Membership.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.OffboardMembership(adminContext, member.MembershipID, "Cannot repeat terminal transition", "request-offboard-again", offboarded.Membership.Revision); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("duplicate membership offboarding returned %v", err)
	}
}

func FuzzMembershipCursor(f *testing.F) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	service := &Service{clock: identityTestClock{at: now}, cursorKey: identityTestCursorKey}
	const subject = "member"
	const membershipID = "00000000-0000-7000-8000-000000000001"
	const authorizationDigest = "sha256:membership-authorization"
	const limit = 50
	valid, err := service.encodeMembershipCursor(subject, membershipID, authorizationDigest, limit)
	if err != nil {
		f.Fatal(err)
	}
	parts := strings.Split(valid, ".")
	if len(parts) != 2 {
		f.Fatal("valid membership cursor has no MAC separator")
	}
	for _, seed := range []string{
		valid,
		parts[0] + "." + strings.ToUpper(parts[1]),
		"%." + strings.Repeat("0", 64),
		parts[0] + ".not-hex",
		parts[0] + "." + strings.Repeat("0", 64),
		"",
		"offset:7",
		strings.Repeat("x", maxMembershipCursorBytes+1),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, token string) {
		cursor, present, decodeErr := service.decodeMembershipCursor(token, authorizationDigest, limit)
		if token == "" {
			if decodeErr != nil || present {
				t.Fatalf("empty cursor returned %#v, present=%t, %v", cursor, present, decodeErr)
			}
			return
		}
		if decodeErr != nil {
			return
		}
		if !present || cursor.Subject != subject || cursor.ID != membershipID {
			t.Fatalf("accepted membership cursor = %#v, present=%t", cursor, present)
		}
		canonical, encodeErr := service.encodeMembershipCursor(cursor.Subject, cursor.ID, authorizationDigest, limit)
		if encodeErr != nil || canonical != token {
			t.Fatalf("accepted non-canonical membership cursor %q", token)
		}
	})
}

func TestIdentityServiceRejectsMissingDependenciesAndAuthority(t *testing.T) {
	ids := &identityTestIDs{}
	if _, err := NewService(ServiceOptions{}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("missing service dependencies returned %v", err)
	}
	store, err := NewMemoryStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{}, IDs: ids, CursorKey: identityTestCursorKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListRoleAssignments(context.Background(), RoleAssignmentListRequest{Limit: 20}); ErrorCodeOf(err) != CodeUnauthenticated {
		t.Fatalf("anonymous role listing returned %v", err)
	}
	reader := Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "reader", Roles: []Role{RoleReader}}
	if _, err := service.GrantRole(WithActor(context.Background(), reader), GrantRoleInput{Subject: "member", ActorType: ActorHuman, Role: RoleReader, Reason: "reason"}, "request"); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("reader role grant returned %v", err)
	}
	if _, err := service.RevokeRole(WithActor(t.Context(), reader), "00000000-0000-7000-8000-000000000010", "Revoke access", "request", 1); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("reader role revocation returned %v", err)
	}
}

func TestIdentityServiceRoleAssignmentPagesValidateAndFetchOneExtra(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000001"
	const issuer = "https://identity.example.com"
	store, err := NewMemoryStore([]Seed{{TenantID: tenantID, Issuer: issuer, Subject: "admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{at: time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)}, IDs: &identityTestIDs{}, CursorKey: identityTestCursorKey})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithActor(t.Context(), admin)
	for _, input := range []GrantRoleInput{
		{Subject: "bravo", ActorType: ActorHuman, Role: RoleReader, Reason: "Reader access"},
		{Subject: "charlie", ActorType: ActorService, Role: RoleServiceClient, Reason: "Service access"},
	} {
		if _, err := service.GrantRole(ctx, input, "request"); err != nil {
			t.Fatal(err)
		}
	}

	page, err := service.ListRoleAssignments(ctx, RoleAssignmentListRequest{Limit: 1})
	if err != nil || len(page) != 2 {
		t.Fatalf("role-assignment lookahead page = %#v, %v", page, err)
	}
	position := page[len(page)-1]
	memberPage, err := service.ListRoleAssignments(ctx, RoleAssignmentListRequest{MembershipID: position.MembershipID, Limit: 1})
	if err != nil || len(memberPage) != 1 || memberPage[0].ID != position.ID {
		t.Fatalf("member-scoped roles = %#v, %v", memberPage, err)
	}
	if _, err := service.ListRoleAssignments(ctx, RoleAssignmentListRequest{MembershipID: "00000000-0000-7000-8000-000000000099", Limit: 1}); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("missing member roles = %v", err)
	}
	page, err = service.ListRoleAssignments(ctx, RoleAssignmentListRequest{AfterSubject: position.Subject, AfterNamespaceID: position.NamespaceID, AfterRole: position.Role, AfterID: position.ID, Limit: 1})
	if err != nil || len(page) != 1 || page[0].Subject != "charlie" {
		t.Fatalf("positioned role-assignment page = %#v, %v", page, err)
	}
	for _, request := range []RoleAssignmentListRequest{
		{MembershipID: "invalid", Limit: 1},
		{},
		{Limit: 101},
		{AfterSubject: "member", Limit: 1},
		{AfterSubject: "member", AfterRole: RoleReader, AfterID: "not-a-uuid", Limit: 1},
		{AfterSubject: "member", AfterNamespaceID: "not-a-uuid", AfterRole: RoleReader, AfterID: admin.MembershipID, Limit: 1},
		{AfterSubject: "member", AfterRole: Role("owner"), AfterID: admin.MembershipID, Limit: 1},
	} {
		if _, err := service.ListRoleAssignments(ctx, request); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid role-assignment page %#v returned %v", request, err)
		}
	}
}

func TestIdentityServiceRejectsUnsafeInputsAndIdentifierFailuresBeforeMutation(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const issuer = "https://identity.example.com"
	store, err := NewMemoryStore([]Seed{{TenantID: tenantID, Issuer: issuer, Subject: "admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}}})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	adminContext := WithActor(t.Context(), admin)
	validInput := GrantRoleInput{Subject: "reader", ActorType: ActorHuman, Role: RoleReader, Reason: "Governed access"}
	t.Run("membership and revocation preparation", func(t *testing.T) {
		service, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{}, IDs: &failingIdentityIDs{failAt: 1}, CursorKey: identityTestCursorKey})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.changeMembership(adminContext, admin.MembershipID, "Reviewed transition", "request", 1, MembershipTransition("unsupported")); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("unknown transition returned %v", err)
		}
		if _, err := service.changeMembership(adminContext, "00000000-0000-7000-8000-000000000099", "Reviewed transition", "request", 1, MembershipSuspend); ErrorCodeOf(err) != CodeNotFound {
			t.Fatalf("missing membership returned %v", err)
		}
		if _, err := service.changeMembership(adminContext, admin.MembershipID, "Reviewed transition", "request", 1, MembershipSuspend); ErrorCodeOf(err) != CodeInternal {
			t.Fatalf("membership evidence identifier failure returned %v", err)
		}
		service.ids = &failingIdentityIDs{failAt: 1}
		if _, err := service.RevokeRole(adminContext, "00000000-0000-7000-8000-000000000010", "Revoke access", "request", 1); ErrorCodeOf(err) != CodeInternal {
			t.Fatalf("revocation evidence identifier failure returned %v", err)
		}
		if _, err := store.Resolve(adminContext, Principal{Issuer: issuer, TenantID: tenantID, Subject: "admin"}); err != nil {
			t.Fatalf("preparation failure changed administrator access: %v", err)
		}
	})

	for name, input := range map[string]GrantRoleInput{
		"blank subject": {ActorType: ActorHuman, Role: RoleReader, Reason: "reason"},
		"unknown type":  {Subject: "reader", ActorType: "robot", Role: RoleReader, Reason: "reason"},
		"blank reason":  {Subject: "reader", ActorType: ActorHuman, Role: RoleReader},
		"oversized reason": {Subject: "reader", ActorType: ActorHuman, Role: RoleReader,
			Reason: strings.Repeat("x", 2001)},
		"control reason": {Subject: "reader", ActorType: ActorHuman, Role: RoleReader,
			Reason: "line\nbreak"},
		"unknown role": {Subject: "reader", ActorType: ActorHuman, Role: "owner", Reason: "reason"},
		"invalid namespace": {Subject: "reader", ActorType: ActorHuman, Role: RoleReader,
			NamespaceID: "not-a-uuid", Reason: "reason"},
		"global-only namespace role": {Subject: "reader", ActorType: ActorHuman, Role: RoleTenantAdmin,
			NamespaceID: "00000000-0000-7000-8000-000000000010", Reason: "reason"},
	} {
		t.Run(name, func(t *testing.T) {
			service, serviceErr := NewService(ServiceOptions{Store: store, Clock: identityTestClock{}, IDs: &identityTestIDs{}, CursorKey: identityTestCursorKey})
			if serviceErr != nil {
				t.Fatal(serviceErr)
			}
			if _, grantErr := service.GrantRole(adminContext, input, "request"); ErrorCodeOf(grantErr) != CodeInvalidArgument {
				t.Fatalf("unsafe grant returned %v", grantErr)
			}
		})
	}

	realmFreeAdmin := admin
	realmFreeAdmin.RealmID = ""
	service, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{}, IDs: &identityTestIDs{}, CursorKey: identityTestCursorKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GrantRole(WithActor(t.Context(), realmFreeAdmin), validInput, "request"); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("realm-free administrator grant returned %v", err)
	}

	before, err := store.ListRoleAssignments(t.Context(), tenantID, RoleAssignmentListRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for failAt := 1; failAt <= 4; failAt++ {
		ids := &failingIdentityIDs{failAt: failAt}
		service, serviceErr := NewService(ServiceOptions{Store: store, Clock: identityTestClock{}, IDs: ids, CursorKey: identityTestCursorKey})
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		if _, grantErr := service.GrantRole(adminContext, validInput, "request"); ErrorCodeOf(grantErr) != CodeInternal {
			t.Fatalf("identifier failure %d returned %v", failAt, grantErr)
		}
	}
	namespaceInput := validInput
	namespaceInput.Subject = "namespace-reader"
	namespaceInput.NamespaceID = "00000000-0000-7000-8000-000000000010"
	for failAt := 1; failAt <= 5; failAt++ {
		ids := &failingIdentityIDs{failAt: failAt}
		service, serviceErr := NewService(ServiceOptions{Store: store, Clock: identityTestClock{}, IDs: ids, CursorKey: identityTestCursorKey})
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		if _, grantErr := service.GrantRole(adminContext, namespaceInput, "request"); ErrorCodeOf(grantErr) != CodeInternal {
			t.Fatalf("namespace identifier failure %d returned %v", failAt, grantErr)
		}
	}
	after, err := store.ListRoleAssignments(t.Context(), tenantID, RoleAssignmentListRequest{Limit: 100})
	if err != nil || len(after) != len(before) {
		t.Fatalf("identifier failure mutated assignments: before=%d after=%d error=%v", len(before), len(after), err)
	}

	for _, invalid := range []struct {
		assignment string
		reason     string
		revision   int64
	}{
		{assignment: "not-a-uuid", reason: "reason", revision: 1},
		{assignment: admin.MembershipID, reason: "", revision: 1},
		{assignment: admin.MembershipID, reason: "reason", revision: 0},
		{assignment: admin.MembershipID, reason: strings.Repeat("x", 2001), revision: 1},
		{assignment: admin.MembershipID, reason: "line\nbreak", revision: 1},
	} {
		if _, revokeErr := service.RevokeRole(adminContext, invalid.assignment, invalid.reason, "request", invalid.revision); ErrorCodeOf(revokeErr) != CodeInvalidArgument {
			t.Fatalf("unsafe revocation %#v returned %v", invalid, revokeErr)
		}
	}

	for _, value := range []string{" leading", "trailing ", "control\x00", string([]byte{0xff}), strings.Repeat("x", 513)} {
		if ValidSubject(value) {
			t.Fatalf("unsafe subject %q was accepted", value)
		}
	}
	for _, value := range []string{"bounded-subject", strings.Repeat("x", 512), strings.Repeat("\u00e9", 256), "inside space"} {
		if !ValidSubject(value) {
			t.Fatalf("valid subject %q was rejected", value)
		}
	}
	var nilError *Error
	if nilError.Error() != "" {
		t.Fatal("nil typed identity error returned text")
	}
	wrappedCause := errors.New("cause")
	if !errors.Is(Wrap(CodeInternal, "wrapped", wrappedCause), wrappedCause) {
		t.Fatal("identity error did not preserve its cause")
	}
}

func TestIdentityServiceNormalizesUnicodeAuditReasons(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const issuer = "https://identity.example.com"
	store, err := NewMemoryStore([]Seed{{TenantID: tenantID, Issuer: issuer, Subject: "admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{at: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)}, IDs: &identityTestIDs{}, CursorKey: identityTestCursorKey})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenantID, Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	reason := strings.Repeat("\u00e9", 2000)
	assignment, err := service.GrantRole(WithActor(t.Context(), admin), GrantRoleInput{Subject: "reader", ActorType: ActorHuman, Role: RoleReader, Reason: "\u2003" + reason + "\u3000"}, "request-unicode")
	if err != nil {
		t.Fatal(err)
	}
	if assignment.GrantReason != reason || len(store.audit) != 1 || store.audit[0].Reason != reason {
		t.Fatalf("normalized Unicode reason = assignment %q audit %#v", assignment.GrantReason, store.audit)
	}
}

func assignmentIDFor(t *testing.T, store *MemoryStore, tenantID, subject string, role Role) string {
	t.Helper()
	assignments, err := store.ListRoleAssignments(context.Background(), tenantID, RoleAssignmentListRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, assignment := range assignments {
		if assignment.Subject == subject && assignment.Role == role && assignment.State == AssignmentActive {
			return assignment.ID
		}
	}
	t.Fatalf("active %s assignment for %s was not found", role, subject)
	return ""
}
