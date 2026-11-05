//go:build integration

package database

import (
	"reflect"
	"slices"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
)

func TestPostgresDevelopmentExactTemplateRoster(t *testing.T) {
	db := newHousekeepingDatabase(t)
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh tenant also exercises the existing tenant-initialization trigger.
	const tenant = "00000000-0000-7000-8000-000000009101"
	keys := []string{"reader", "contributor", "reviewer", "publisher", "tenant_admin", "discovery_reader", "publisher_creator", "publication_admin", "source_admin", "security_reviewer", "webhook_admin", "feedback_member", "library_member", "service_client"}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			typ := identity.ActorHuman
			if key == "service_client" {
				typ = identity.ActorService
			}
			input := IdentityProvisioningInput{TenantID: tenant, TenantDisplayName: "Exact development templates", Issuer: authn.DevelopmentIssuer, TenancyMode: "dedicated", Subject: key, ActorType: typ, RoleTemplates: []string{key}, Operator: "catalogctl-development-bootstrap", Reason: "Qualify exact development template"}
			got, err := ProvisionIdentity(t.Context(), db.operator, input)
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := ProvisionIdentity(t.Context(), db.operator, input)
			if err != nil || !reflect.DeepEqual(got, repeated) || len(got.TemplateAssignments) != 1 || len(got.Assignments) != 0 {
				t.Fatalf("retained assignments %#v repeated %#v err %v", got, repeated, err)
			}
			actor, err := store.Resolve(t.Context(), identity.Principal{Issuer: authn.DevelopmentIssuer, TenantID: tenant, Subject: key})
			if err != nil {
				t.Fatal(err)
			}
			descriptors := identity.Permissions()
			checks := make([]identity.PermissionCheck, 0, len(descriptors))
			want, _ := identity.BuiltinPermissions(key)
			for _, desc := range descriptors {
				checks = append(checks, identity.PermissionCheck{Permission: desc.Key})
			}
			decision, err := store.CheckPermissions(t.Context(), actor, checks)
			if err != nil {
				t.Fatal(err)
			}
			for i, check := range checks {
				if decision.Allowed[i] != slices.Contains(want, check.Permission) {
					t.Fatalf("%s unexpected %s admission=%v", key, check.Permission, decision.Allowed[i])
				}
			}
			rows, err := store.ListEffectiveAssignments(t.Context(), actor, identity.AuthorizationPageRequest{Limit: 25, AuthorizationRevision: decision.Revision})
			if err != nil || len(rows) != 1 || rows[0].Role.Key != key || actor.ActorType != typ {
				t.Fatalf("exact actor/assignments %#v %#v %v", actor, rows, err)
			}
			if _, err := store.Resolve(t.Context(), identity.Principal{Issuer: authn.DevelopmentIssuer, TenantID: housekeepingTenantB, Subject: key}); err == nil {
				t.Fatal("template identity escaped tenant")
			}
		})
	}
	t.Run("complete roster", func(t *testing.T) {
		input := IdentityProvisioningInput{TenantID: tenant, TenantDisplayName: "Exact development templates", Issuer: authn.DevelopmentIssuer, TenancyMode: "dedicated", Subject: "all-templates", ActorType: identity.ActorService, RoleTemplates: keys, Operator: "catalogctl-development-bootstrap", Reason: "Qualify complete exact development roster"}
		got, err := ProvisionIdentity(t.Context(), db.operator, input)
		if err != nil || len(got.TemplateAssignments) != 14 || len(got.Assignments) != 0 {
			t.Fatal("complete exact roster rejected or expanded", got, err)
		}
		repeated, err := ProvisionIdentity(t.Context(), db.operator, input)
		if err != nil || !reflect.DeepEqual(got, repeated) {
			t.Fatal("complete exact roster was not retained", repeated, err)
		}
		actor, err := store.Resolve(t.Context(), identity.Principal{Issuer: authn.DevelopmentIssuer, TenantID: tenant, Subject: input.Subject})
		if err != nil || actor.ActorType != identity.ActorService {
			t.Fatal("complete roster principal", actor, err)
		}
		decision, err := store.CheckPermissions(t.Context(), actor, []identity.PermissionCheck{{Permission: "library.own"}})
		if err != nil || !slices.Equal(decision.Allowed, []bool{true}) {
			t.Fatal("new template missing from provisioned roster", decision, err)
		}
		rows, err := store.ListEffectiveAssignments(t.Context(), actor, identity.AuthorizationPageRequest{Limit: 25, AuthorizationRevision: decision.Revision})
		if err != nil || len(rows) != 14 {
			t.Fatal("complete roster assignment projection", rows, err)
		}
		actual := make([]string, 0, len(rows))
		for _, row := range rows {
			actual = append(actual, row.Role.Key)
		}
		want := slices.Clone(keys)
		slices.Sort(actual)
		slices.Sort(want)
		if !slices.Equal(actual, want) {
			t.Fatal("provisioned template identities changed", actual)
		}
	})
}
