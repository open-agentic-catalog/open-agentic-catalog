//go:build integration

package database

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresNamespaceUnicodeBoundsAndRejectionEvidence(t *testing.T) {
	db := newHousekeepingDatabase(t)
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.NewFake(time.Now().UTC().Truncate(time.Microsecond)), IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://text.example", TenancyMode: "dedicated", Subject: "text-admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}, Operator: "text-test", Reason: "synthetic text qualification", AcknowledgeOutbox: true})
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{TenantID: housekeepingTenantA, Subject: "text-admin", MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}}
	ctx := postgresFixtureContext(t, db, t.Context(), actor)
	actor, _ = identity.FromContext(ctx)
	counts := func() [3]int {
		t.Helper()
		var got [3]int
		err := db.operator.QueryRow(ctx, `SELECT
   (SELECT count(*) FROM namespaces WHERE tenant_id=$1),
   (SELECT count(*) FROM audit_events WHERE tenant_id=$1),
   (SELECT count(*) FROM outbox_events WHERE tenant_id=$1)`, actor.TenantID).Scan(&got[0], &got[1], &got[2])
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := counts()
	input := catalog.CreateNamespaceInput{Slug: "unicode", DisplayName: strings.Repeat("🙂", 160), Description: strings.Repeat("🙂", 2000)}
	ns, err := service.CreateNamespace(ctx, input)
	if err != nil || ns.DisplayName != input.DisplayName || ns.Description != input.Description {
		t.Fatalf("valid Unicode creation: %+v %v", ns, err)
	}
	after := counts()
	if after != [3]int{before[0] + 1, before[1] + 1, before[2] + 1} {
		t.Fatalf("creation evidence counts: %v -> %v", before, after)
	}
	for _, field := range []string{"name", "description"} {
		for _, suffix := range []string{"x", "\x00"} {
			invalid := input
			if field == "name" {
				invalid.DisplayName += suffix
			} else {
				invalid.Description += suffix
			}
			// NUL itself, rather than the length guard, must cause this rejection.
			if suffix == "\x00" {
				if field == "name" {
					invalid.DisplayName = "bad\x00name"
				} else {
					invalid.Description = "bad\x00description"
				}
			}
			invalid.Slug = "rejected"
			if _, err := service.CreateNamespace(ctx, invalid); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("create %s %q: %v", field, suffix, err)
			}
			patch := catalog.PatchNamespaceInput{Reason: "Boundary qualification"}
			if field == "name" {
				patch.DisplayName = &invalid.DisplayName
			} else {
				patch.Description = &invalid.Description
			}
			if _, err := service.UpdateNamespace(ctx, ns.ID, patch, ns.Revision); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("patch %s %q: %v", field, suffix, err)
			}
			if got := counts(); got != after {
				t.Fatalf("rejection changed state/evidence: %v -> %v", after, got)
			}
		}
	}
	stored, err := service.GetNamespace(ctx, ns.ID)
	if err != nil || stored.Revision != ns.Revision || stored.DisplayName != input.DisplayName || stored.Description != input.Description {
		t.Fatalf("rejection changed persisted text: %+v %v", stored, err)
	}
	other := actor
	other.TenantID = housekeepingTenantB
	if _, err := service.GetNamespace(identity.WithActor(t.Context(), other), ns.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("cross-tenant read: %v", err)
	}
	next := strings.Repeat("é", 2000)
	updated, err := service.UpdateNamespace(ctx, ns.ID, catalog.PatchNamespaceInput{Description: &next, Reason: "Valid after rejection"}, ns.Revision)
	if err != nil || updated.Description != next || updated.Revision != ns.Revision+1 {
		t.Fatalf("valid recovery: %+v %v", updated, err)
	}
	if got := counts(); got != [3]int{after[0], after[1] + 1, after[2] + 1} {
		t.Fatalf("recovery evidence: %v -> %v", after, got)
	}
}
