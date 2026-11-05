package identity

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestMemoryIdentitySeedsRejectInvalidIdentityAndRoleScope(t *testing.T) {
	t.Parallel()
	const tenant = "00000000-0000-7000-8000-000000000001"
	const namespace = "00000000-0000-7000-8000-000000000010"
	for _, test := range []struct {
		name   string
		change func(*Seed)
	}{
		{"invalid tenant", func(s *Seed) { s.TenantID = "not-a-uuid" }},
		{"empty issuer", func(s *Seed) { s.Issuer = "" }},
		{"oversized issuer", func(s *Seed) { s.Issuer = strings.Repeat("a", 2049) }},
		{"invalid subject", func(s *Seed) { s.Subject = "member\n" }},
		{"invalid organization", func(s *Seed) { s.ExternalOrganization = " organization" }},
		{"invalid actor type", func(s *Seed) { s.ActorType = "unknown" }},
		{"unknown tenant role", func(s *Seed) { s.Roles = []Role{"unknown"} }},
		{"invalid namespace", func(s *Seed) { s.Namespaces = map[string][]Role{"not-a-uuid": {RoleReader}} }},
		{"tenant role in namespace", func(s *Seed) { s.Namespaces = map[string][]Role{namespace: {RoleTenantAdmin}} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			seed := Seed{TenantID: tenant, Issuer: "https://identity.example.com", Subject: "member", ActorType: ActorHuman, Roles: []Role{RoleReader}}
			test.change(&seed)
			store, err := NewMemoryStore([]Seed{seed})
			if store != nil || ErrorCodeOf(err) != CodeInvalidArgument {
				t.Fatalf("invalid seed returned store=%#v, error=%v", store, err)
			}
		})
	}
}

func TestMemoryIdentityResolutionRejectsAmbiguousPrincipals(t *testing.T) {
	t.Parallel()
	const tenant = "00000000-0000-7000-8000-000000000001"
	const issuer = "https://identity.example.com"
	store, err := NewMemoryStore([]Seed{{TenantID: tenant, Issuer: issuer, Subject: "member", ActorType: ActorHuman, Roles: []Role{RoleReader}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		principal Principal
	}{
		{"missing issuer", Principal{TenantID: tenant, Subject: "member"}},
		{"invalid subject", Principal{Issuer: issuer, TenantID: tenant, Subject: "member\n"}},
		{"no tenant selector", Principal{Issuer: issuer, Subject: "member"}},
		{"two tenant selectors", Principal{Issuer: issuer, TenantID: tenant, ExternalOrganization: "organization", Subject: "member"}},
		{"invalid tenant selector", Principal{Issuer: issuer, TenantID: "not-a-uuid", Subject: "member"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actor, err := store.Resolve(t.Context(), test.principal)
			if ErrorCodeOf(err) != CodeUnauthenticated || !reflect.DeepEqual(actor, Actor{}) {
				t.Fatalf("invalid principal returned actor=%#v, error=%v", actor, err)
			}
		})
	}
}

func TestExactDevelopmentTemplateSeedsNeverExpandBundles(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	const issuer = "https://identity.example.com"
	keys := []string{"reader", "contributor", "reviewer", "publisher", "tenant_admin", "discovery_reader", "publisher_creator", "publication_admin", "source_admin", "security_reviewer", "webhook_admin", "feedback_member", "library_member", "service_client"}
	if len(keys) != MaxDevelopmentRoleTemplates || len(builtinPermissions) != MaxDevelopmentRoleTemplates {
		t.Fatal("development bound must cover the exact built-in roster")
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			typ := ActorHuman
			if key == "service_client" {
				typ = ActorService
			}
			seed := Seed{TenantID: tenant, Issuer: issuer, Subject: key, ActorType: typ, RoleTemplates: []string{key}}
			store, err := NewMemoryStore([]Seed{seed})
			if err != nil {
				t.Fatal(err)
			}
			actor, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenant, Subject: key})
			if err != nil {
				t.Fatal(err)
			}
			got, err := CurrentSelfAccess(WithActor(t.Context(), actor), SelfAccessRequest{Limit: 25})
			if err != nil {
				t.Fatal(err)
			}
			want, _ := BuiltinPermissions(key)
			slices.Sort(want)
			if !slices.Equal(got.Permissions, want) || !slices.Equal(got.AvailablePermissions, want) || actor.ActorType != typ {
				t.Fatalf("exact %s access = %#v, actor=%#v", key, got, actor)
			}
			assignments, err := store.ListEffectiveAssignments(t.Context(), actor, AuthorizationPageRequest{Limit: 25, AuthorizationRevision: got.AuthorizationRevision})
			if err != nil || len(assignments) != 1 || assignments[0].Role.Key != key {
				t.Fatalf("assignments = %#v, %v", assignments, err)
			}
			other, err := NewMemoryStore([]Seed{seed})
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := other.ListEffectiveAssignments(t.Context(), actor, AuthorizationPageRequest{Limit: 25, AuthorizationRevision: got.AuthorizationRevision})
			if err != nil || !reflect.DeepEqual(assignments, repeated) {
				t.Fatal("exact seeds are not deterministic", err)
			}
		})
	}
	t.Run("complete roster", func(t *testing.T) {
		store, err := NewMemoryStore([]Seed{{TenantID: tenant, Issuer: issuer, Subject: "all-templates", ActorType: ActorService, RoleTemplates: keys}})
		if err != nil {
			t.Fatal("complete current roster rejected", err)
		}
		actor, err := store.Resolve(t.Context(), Principal{Issuer: issuer, TenantID: tenant, Subject: "all-templates"})
		if err != nil {
			t.Fatal(err)
		}
		access, err := CurrentSelfAccess(WithActor(t.Context(), actor), SelfAccessRequest{Limit: 25})
		if err != nil {
			t.Fatal(err)
		}
		rows, err := store.ListEffectiveAssignments(t.Context(), actor, AuthorizationPageRequest{Limit: 25, AuthorizationRevision: access.AuthorizationRevision})
		if err != nil || len(rows) != 14 {
			t.Fatal("exact roster did not retain fourteen assignments", rows, err)
		}
		got := make([]string, 0, len(rows))
		for _, row := range rows {
			got = append(got, row.Role.Key)
		}
		want := slices.Clone(keys)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatal("exact roster assignments changed", got)
		}
	})
	for _, seed := range []Seed{
		{RoleTemplates: []string{"unknown"}}, {RoleTemplates: []string{"reader", "reader"}}, {RoleTemplates: []string{"reader"}, Roles: []Role{RoleReader}}, {RoleTemplates: make([]string, 15)},
	} {
		seed.TenantID = tenant
		seed.Issuer = issuer
		seed.Subject = "invalid"
		seed.ActorType = ActorHuman
		if store, err := NewMemoryStore([]Seed{seed}); store != nil || ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid exact seed accepted: %#v, %v", seed, err)
		}
	}
}
