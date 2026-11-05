package identity

import (
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// propertyRoleOrder changes only multiplicity and ordering, not the grant set.
func propertyRoleOrder(t *rapid.T, roles []Role) []Role {
	repeated := make([]Role, 0, 3*len(roles))
	for _, role := range roles {
		for range rapid.IntRange(1, 3).Draw(t, "copies") {
			repeated = append(repeated, role)
		}
	}
	return rapid.Permutation(repeated).Draw(t, "grant order")
}

func TestPropertyAuthorizationEquivalence(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		roles := []Role{RoleReader, RoleContributor, RoleReviewer, RolePublisher, RoleSourceAdmin, RoleSecurityReviewer, RoleTenantAdmin, RoleServiceClient}
		// The first four roles are the documented namespace-scoped vocabulary.
		const unrelated = "00000000-0000-7000-8000-000000000099"
		const empty = "00000000-0000-7000-8000-000000000098"
		scopes := make([]string, 0, 6)
		scopes = append(scopes, "", unrelated, empty)
		for i := range rapid.IntRange(0, 3).Draw(t, "namespace count") {
			scopes = append(scopes, fmt.Sprintf("00000000-0000-7000-8000-%012x", i+1))
		}
		// Construct allowed (scope, role) pairs from the generated grants before
		// asking Actor to resolve them. No production authorization helper is
		// used to compute the expectation.
		allowed := make(map[string]map[Role]bool)
		for _, scope := range scopes {
			allowed[scope] = make(map[Role]bool)
		}
		actor := Actor{
			TenantID:     fmt.Sprintf("abcdefab-0000-7000-8000-%012x", rapid.Uint64Range(0, 65535).Draw(t, "tenant")),
			Subject:      rapid.StringMatching(`[a-z0-9]{1,16}`).Draw(t, "subject"),
			RealmID:      "00000000-0000-7000-8000-000000000090",
			MembershipID: "00000000-0000-7000-8000-000000000091",
			ActorType:    rapid.SampledFrom([]ActorType{ActorHuman, ActorService}).Draw(t, "actor type"),
			Namespaces:   make(map[string][]Role),
			Claims:       make(map[string]string),
		}
		global := rapid.IntRange(0, 255).Draw(t, "global grant mask")
		for i, role := range roles {
			if global&(1<<i) == 0 {
				continue
			}
			actor.Roles = append(actor.Roles, role)
			for _, scope := range scopes {
				allowed[scope][role] = true
				if role == RoleTenantAdmin {
					for _, inherited := range roles {
						allowed[scope][inherited] = true
					}
				}
			}
		}
		for _, scope := range scopes[3:] {
			mask := rapid.IntRange(0, 15).Draw(t, "namespace grant mask")
			for i, role := range roles[:4] {
				if mask&(1<<i) != 0 {
					actor.Namespaces[scope] = append(actor.Namespaces[scope], role)
					allowed[scope][role] = true
				}
			}
		}
		claimNames := make([]string, 0, 3)
		for i := range rapid.IntRange(0, 3).Draw(t, "claim count") {
			name := fmt.Sprintf("claim-%d", i)
			claimNames = append(claimNames, name)
			actor.Claims[name] = rapid.StringMatching(`[a-z0-9]{0,16}`).Draw(t, "claim value")
		}
		equivalent := actor
		equivalent.TenantID = strings.ToUpper(actor.TenantID) // Always changes alphabetic UUID digits.
		equivalent.Roles = propertyRoleOrder(t, actor.Roles)
		equivalent.Namespaces = map[string][]Role{empty: {}}
		for _, scope := range rapid.Permutation(scopes[3:]).Draw(t, "namespace insertion order") {
			equivalent.Namespaces[scope] = propertyRoleOrder(t, actor.Namespaces[scope])
		}
		equivalent.Claims = make(map[string]string)
		for _, name := range rapid.Permutation(claimNames).Draw(t, "claim insertion order") {
			equivalent.Claims[name] = actor.Claims[name]
		}
		if actor.AuthorizationDigest() != equivalent.AuthorizationDigest() {
			t.Fatal("equivalent grants, maps or tenant casing changed the digest")
		}
		for _, scope := range scopes {
			for _, role := range roles {
				want := allowed[scope][role]
				if actor.HasRole(role, scope) != want || equivalent.HasRole(role, scope) != want {
					t.Fatalf("role %s in scope %q: want %v for generated grants %+v", role, scope, want, actor)
				}
			}
		}
		// A bound identity change must invalidate this concrete projection;
		// this does not assert mathematical hash injectivity.
		changed := actor
		changed.Subject += "-other"
		if changed.AuthorizationDigest() == actor.AuthorizationDigest() {
			t.Fatal("changed subject retained the authorization digest")
		}
	})
}
