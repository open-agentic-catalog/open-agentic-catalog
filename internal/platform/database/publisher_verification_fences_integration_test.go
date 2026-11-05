//go:build integration

package database

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresPublisherVerificationTenantAndConcurrentFences(t *testing.T) {
	db, _, access, admin, alice, _ := authorizationCoreFixture(t)
	ctx := identity.WithActor(t.Context(), admin)
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "other-domain-admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
	var lookup verificationTXTFunc
	var calls atomic.Int32
	newService := func() *catalog.Service {
		t.Helper()
		repo, err := NewPostgresRepository(db.api)
		if err != nil {
			t.Fatal(err)
		}
		policy := catalog.DefaultPublisherDomainPolicy()
		policy.Mode = catalog.DomainVerificationRequired
		s, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte("01234567890123456789012345678901"), PublisherDomainPolicy: policy, PublisherTXT: verificationTXTFunc(func(call context.Context, name string) ([]string, error) {
			calls.Add(1)
			return lookup(call, name)
		})})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s, second := newService(), newService() // Independent repository instances share only PostgreSQL.
	create := func(tb testing.TB, call context.Context) catalog.Publisher {
		tb.Helper()
		p, err := s.CreatePublisher(call, catalog.CreatePublisherInput{DisplayName: "Tenant DNS fixture", Domains: []string{"catalog.example.com", "other.example.com"}})
		if err != nil {
			tb.Fatal(err)
		}
		return p
	}
	issue := func(tb testing.TB, call context.Context, p catalog.Publisher) catalog.PublisherDomainChallenge {
		tb.Helper()
		status, err := s.GetPublisherDomainVerification(call, p.ID, "catalog.example.com")
		if err != nil {
			tb.Fatal(err)
		}
		challenge, err := s.IssuePublisherDomainChallenge(call, p.ID, "catalog.example.com", "Issue isolated challenge", status.Version)
		if err != nil {
			tb.Fatal(err)
		}
		return challenge
	}
	a, b := create(t, ctx), create(t, other)
	first, foreign := issue(t, ctx, a), issue(t, other, b)
	if first.Value == foreign.Value || first.Status.Version.ID == foreign.Status.Version.ID {
		t.Fatal("tenants shared a challenge")
	}
	lookup = func(context.Context, string) ([]string, error) { return []string{first.Value}, nil }
	negative, err := s.CheckPublisherDomain(other, b.ID, "catalog.example.com", "Reject another tenant's value", foreign.Status.Version)
	if err != nil || negative.LastCheckOutcome != "mismatch" || negative.State != "not_verified" {
		t.Fatal("foreign nonce established proof", err)
	}
	if _, err := s.CheckPublisherDomain(ctx, a.ID, "catalog.example.com", "Verify the bound tenant", first.Status.Version); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if _, err := s.GetPublisherDomainVerification(other, a.ID, "catalog.example.com"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("cross-tenant status disclosed", err)
	}
	if _, err := s.GetPublisherDomainChallenge(other, a.ID, "catalog.example.com"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("cross-tenant nonce disclosed", err)
	}
	if _, err := s.CheckPublisherDomain(other, a.ID, "catalog.example.com", "Deny cross-tenant check", first.Status.Version); catalog.ErrorCodeOf(err) != catalog.CodeNotFound || calls.Load() != before {
		t.Fatal("cross-tenant check reached DNS", err)
	}
	if _, err := s.ClearPublisherDomainVerification(other, a.ID, "catalog.example.com", "Deny cross-tenant clear", first.Status.Version); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("cross-tenant clear accepted", err)
	}
	if _, err := s.IssuePublisherDomainChallenge(other, a.ID, "catalog.example.com", "Deny cross-tenant issue", first.Status.Version); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("cross-tenant issue accepted", err)
	}
	separate, err := s.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Separate Publisher", Domains: []string{"separate.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssuePublisherDomainChallenge(ctx, separate.ID, "separate.example.com", "Reject another Publisher's state", first.Status.Version); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("cross-Publisher challenge accepted", err)
	}
	untouched, err := s.GetPublisherDomainVerification(ctx, separate.ID, "separate.example.com")
	if err != nil || untouched.ChallengeState != "not_issued" || calls.Load() != before {
		t.Fatal("cross-Publisher refusal changed state or performed DNS", err)
	}
	role, err := access.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "DNS manager", Permissions: []identity.Permission{"publisher.read", "publisher.manage"}, Reason: "Delegate exact DNS management"}, false, "dns-role")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := access.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, Reason: "Grant DNS management"}, "dns-grant")
	if err != nil {
		t.Fatal(err)
	}
	manager := identity.WithActor(t.Context(), alice)
	if _, err := s.ApprovePublisherPublicationDomain(manager, a.ID, "catalog.example.com", "Management does not grant approval", a.Revision); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("manager approved a domain", err)
	}
	for _, scenario := range []string{"clear", "remove_readd", "revoke_grant", "suspend_membership"} {
		t.Run(scenario, func(t *testing.T) {
			p, err := s.GetPublisher(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			challenge := issue(t, manager, p)
			priorOutcome := challenge.Status.LastCheckOutcome
			started, release := make(chan struct{}), make(chan struct{})
			lookup = func(call context.Context, _ string) ([]string, error) {
				close(started)
				select {
				case <-release:
					return []string{challenge.Value}, nil
				case <-call.Done():
					return nil, call.Err()
				}
			}
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			call, cancel := context.WithTimeout(manager, 5*time.Second)
			defer cancel()
			result := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_, err := s.CheckPublisherDomain(call, p.ID, "catalog.example.com", "Fence blocked DNS", challenge.Status.Version)
				result <- err
			}()
			defer func() { cancel(); unblock(); <-finished }()
			select {
			case <-started:
			case <-call.Done():
				t.Fatal("DNS did not start")
			}
			change, stop := context.WithTimeout(ctx, 3*time.Second)
			defer stop()
			switch scenario {
			case "clear":
				if _, err := second.ClearPublisherDomainVerification(change, p.ID, "catalog.example.com", "Clear blocked challenge", challenge.Status.Version); err != nil {
					t.Fatal(err)
				}
			case "remove_readd":
				domains := make([]string, 1, 2)
				domains[0] = "other.example.com"
				removed, err := second.UpdatePublisher(change, p.ID, catalog.PatchPublisherInput{Domains: &domains, Reason: "Remove blocked domain"}, p.Revision)
				if err != nil {
					t.Fatal(err)
				}
				domains = append(domains, "catalog.example.com")
				if _, err := second.UpdatePublisher(change, p.ID, catalog.PatchPublisherInput{Domains: &domains, Reason: "Readd blocked domain"}, removed.Revision); err != nil {
					t.Fatal(err)
				}
				replacement := issue(t, ctx, p)
				if replacement.Status.Version.Revision != first.Status.Version.Revision || replacement.Status.Version.ID == first.Status.Version.ID {
					t.Fatal("replacement identity did not fence the same revision")
				}
				if _, err := second.IssuePublisherDomainChallenge(change, p.ID, "catalog.example.com", "Deny old issue", first.Status.Version); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
					t.Fatal("stale issue accepted", err)
				}
				if _, err := second.ClearPublisherDomainVerification(change, p.ID, "catalog.example.com", "Deny old clear", first.Status.Version); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
					t.Fatal("stale clear accepted", err)
				}
				priorOutcome = ""
			case "revoke_grant":
				if _, err := access.RevokeAuthorizationAssignment(change, grant.ID, grant.Revision, "Revoke during DNS", "dns-revoke"); err != nil {
					t.Fatal(err)
				}
			case "suspend_membership":
				membership, err := access.GetMembership(change, alice.MembershipID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := access.SuspendMembership(change, alice.MembershipID, "Suspend during DNS", "dns-suspend", membership.Revision); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			select {
			case err := <-result:
				expected := catalog.CodePreconditionFailed
				if scenario == "revoke_grant" || scenario == "suspend_membership" {
					expected = catalog.CodeForbidden
				}
				if catalog.ErrorCodeOf(err) != expected {
					t.Fatal("stale or revoked DNS completion accepted", err)
				}
			case <-call.Done():
				t.Fatal("DNS completion did not return")
			}
			current, err := second.GetPublisherDomainVerification(ctx, p.ID, "catalog.example.com")
			if err != nil || current.State != "not_verified" || current.LastCheckOutcome != priorOutcome {
				t.Fatal("blocked completion persisted proof", err)
			}
			if scenario == "revoke_grant" {
				grant, err = access.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, Reason: "Restore exact grant for next case"}, "dns-grant-again")
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
