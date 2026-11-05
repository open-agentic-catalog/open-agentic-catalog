package catalog

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// These doubles supply decisions and record handoffs only. No Catalog workflow,
// identity store, listener, database or application process is constructed.
type originDeleteRepository struct {
	Repository
	resource               Resource
	origin                 OriginatingPrincipal
	resourceErr, originErr error
	resourceReads          [][2]string
	originReads            [][3]string
	deleted                []DeleteResourceParams
}

func (r *originDeleteRepository) GetResource(_ context.Context, tenant, target string) (Resource, error) {
	r.resourceReads = append(r.resourceReads, [2]string{tenant, target})
	return r.resource, r.resourceErr
}

func (r *originDeleteRepository) GetOriginatingPrincipal(_ context.Context, tenant, kind, target string) (OriginatingPrincipal, error) {
	r.originReads = append(r.originReads, [3]string{tenant, kind, target})
	return r.origin, r.originErr
}

func (r *originDeleteRepository) DeleteResource(_ context.Context, p DeleteResourceParams) (Resource, error) {
	r.deleted = append(r.deleted, p)
	return r.resource, nil
}

type originPermissionDecisions struct {
	identity.PermissionStore
	allowed map[identity.PermissionCheck]bool
}

func (p originPermissionDecisions) CheckPermissions(_ context.Context, _ identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	d := identity.PermissionDecision{Revision: 1, Allowed: make([]bool, len(checks))}
	for i, check := range checks {
		d.Allowed[i] = p.allowed[check]
	}
	return d, nil
}

func TestDeleteResourceOriginAdmission(t *testing.T) {
	const resourceID = "00000000-0000-7000-8000-000000000080"
	const namespaceID = "00000000-0000-7000-8000-000000000081"
	lookupFailure := Wrap(CodeDependencyUnavailable, "owned lookup failure", errors.New("unavailable origin store"))
	identifierFailure := errors.New("owned identifier failure")
	for _, trial := range []struct {
		name             string
		change           string
		admin            bool
		denyDelete       bool
		resourceErr      error
		originErr        error
		failIdentifiers  bool
		want             ErrorCode
		wantOriginLookup bool
	}{
		{name: "exact creator", wantOriginLookup: true},
		{name: "equal subject different membership", change: "membership", want: CodeForbidden, wantOriginLookup: true},
		{name: "different realm", change: "realm", want: CodeForbidden, wantOriginLookup: true},
		{name: "different subject", change: "subject", want: CodeForbidden, wantOriginLookup: true},
		{name: "missing creator evidence", originErr: E(CodeForbidden, "origin missing"), want: CodeForbidden, wantOriginLookup: true},
		{name: "protected administrator override", change: "membership", admin: true, wantOriginLookup: true},
		{name: "protected administrator missing origin", admin: true, originErr: E(CodeForbidden, "origin missing"), wantOriginLookup: true},
		{name: "creator lookup dependency failure", originErr: lookupFailure, want: CodeDependencyUnavailable, wantOriginLookup: true},
		{name: "administrator lookup dependency failure", admin: true, originErr: lookupFailure, want: CodeDependencyUnavailable, wantOriginLookup: true},
		{name: "creator lost delete permission", denyDelete: true, want: CodeForbidden},
		{name: "administrator lacks delete permission", admin: true, denyDelete: true, want: CodeForbidden},
		{name: "resource read failure", resourceErr: lookupFailure, want: CodeDependencyUnavailable},
		{name: "evidence identifier failure", failIdentifiers: true, want: CodeInternal, wantOriginLookup: true},
	} {
		t.Run(trial.name, func(t *testing.T) {
			f := newGuardrailFixture(t)
			a := f.admin
			a.Roles = nil // Role projections must not supply authority to this unit.
			permissions := originPermissionDecisions{allowed: map[identity.PermissionCheck]bool{
				{Permission: "resource.delete_draft", NamespaceID: namespaceID}: !trial.denyDelete,
				{Permission: "access.manage"}:                                   trial.admin,
			}}
			ctx := identity.WithActor(t.Context(), identity.BindPermissionStore(a, permissions))
			r := &originDeleteRepository{
				resource:    Resource{ID: resourceID, TenantID: a.TenantID, NamespaceID: namespaceID, CreatedBy: a.Subject, Revision: 1, UpdatedAt: f.clock.Now()},
				origin:      OriginatingPrincipal{TenantID: a.TenantID, MembershipID: a.MembershipID, RealmID: a.RealmID, Subject: a.Subject},
				resourceErr: trial.resourceErr, originErr: trial.originErr,
			}
			switch trial.change {
			case "membership":
				r.origin.MembershipID = guardReviewerMember
			case "realm":
				r.origin.RealmID = guardRealm
			case "subject":
				r.origin.Subject = "another creator"
			}
			ids := &guardrailIDs{}
			f.service.repo, f.service.ids = r, ids
			if trial.failIdentifiers {
				f.service.ids = auditIDFunc(func(time.Time) (string, error) { ids.next++; return "", identifierFailure })
			}
			err := f.service.DeleteResource(ctx, resourceID, "Review exact creator admission", 1)
			if trial.want == "" && err != nil || trial.want != "" && (err == nil || ErrorCodeOf(err) != trial.want) {
				t.Fatalf("delete error=%v want=%s", err, trial.want)
			}
			if trial.want == CodeDependencyUnavailable && !errors.Is(err, lookupFailure) || trial.failIdentifiers && !errors.Is(err, identifierFailure) {
				t.Fatal("lost the original dependency failure", err)
			}
			if !reflect.DeepEqual(r.resourceReads, [][2]string{{a.TenantID, resourceID}}) {
				t.Fatal("resource read lost tenant or identity", r.resourceReads)
			}
			var wantOrigins [][3]string
			if trial.wantOriginLookup {
				wantOrigins = [][3]string{{a.TenantID, "resource", resourceID}}
			}
			if !reflect.DeepEqual(r.originReads, wantOrigins) {
				t.Fatal("unexpected or incorrectly bound origin lookup", r.originReads)
			}
			if trial.want != "" {
				wantIDs := 0
				if trial.failIdentifiers {
					wantIDs = 1
				}
				if len(r.deleted) != 0 || ids.next != wantIDs {
					t.Fatal("refusal reached mutation or allocated success evidence", r.deleted, ids.next)
				}
				return
			}
			if len(r.deleted) != 1 || ids.next != 2 {
				t.Fatal("valid deletion did not hand off once with audit/outbox identifiers", r.deleted, ids.next)
			}
			p := r.deleted[0]
			if p.TenantID != a.TenantID || p.ResourceID != resourceID || p.ExpectedRevision != 1 || p.Actor != a.Subject || p.Reason != "Review exact creator admission" || !p.At.Equal(f.clock.Now()) || p.Audit.Actor != a.MembershipID || p.Audit.Action != "resource.deleted" || p.Audit.TargetID != resourceID || p.Outbox.Subject != resourceID {
				t.Fatal("deletion handoff lost principal, target, revision or evidence", p)
			}
		})
	}
}
