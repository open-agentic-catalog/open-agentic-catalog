// Development seed regressions keep aliases consistent without token-specific grants.
package identity

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestDevelopmentAliasesRejectConflictingActorTypes(t *testing.T) {
	a := Seed{TenantID: administrationTenant, Issuer: "issuer", Subject: "same", ActorType: ActorHuman, Roles: []Role{RoleReader}}
	b := a
	b.ActorType, b.Roles = ActorService, []Role{RoleServiceClient}
	for _, seeds := range [][]Seed{{a, b}, {b, a}} {
		store, err := NewMemoryStore(seeds)
		if store != nil || ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal("conflicting aliases constructed a usable store", err)
		}
	}
}

func TestDevelopmentSeedNormalizationOwnsInputsAndPreservesRealmMappings(t *testing.T) {
	a := Seed{TenantID: administrationTenant, Issuer: "https://issuer.example", Subject: "same", ActorType: ActorHuman, Roles: []Role{RoleReader}, Namespaces: map[string][]Role{administrationNamespace: {RoleReviewer}}}
	for _, change := range []string{"tenant", "issuer", "organization", "subject"} {
		b := a
		b.ActorType = ActorService
		switch change {
		case "tenant":
			b.TenantID = administrationOtherNamespace
		case "issuer":
			b.Issuer += "/other"
		case "organization":
			b.ExternalOrganization = "organization"
		case "subject":
			b.Subject = "Same"
		}
		normalized, err := NormalizeDevelopmentSeeds([]Seed{a, b})
		if err != nil || len(normalized) != 2 {
			t.Fatal("distinct principals were merged", change, normalized, err)
		}
	}
	result, err := NormalizeDevelopmentSeeds([]Seed{a})
	if err != nil {
		t.Fatal(err)
	}
	result[0].Roles[0], result[0].Namespaces[administrationNamespace][0] = RolePublisher, RoleContributor
	if a.Roles[0] != RoleReader || a.Namespaces[administrationNamespace][0] != RoleReviewer {
		t.Fatal("normalized inputs alias caller-owned grants")
	}
	upper := a
	upper.TenantID = "AAAAAAAA-0000-7000-8000-000000000001"
	lower := upper
	lower.TenantID = strings.ToLower(upper.TenantID)
	lower.ActorType = ActorService
	if got, err := NormalizeDevelopmentSeeds([]Seed{upper, lower}); got != nil || ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("UUID spelling bypassed principal conflict", err)
	}
	shared := a
	shared.ExternalOrganization = "organization"
	foreign := shared
	foreign.TenantID = administrationOtherNamespace
	if got, err := NormalizeDevelopmentSeeds([]Seed{shared, foreign}); got != nil || ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("ambiguous shared realm was accepted", err)
	}
	invalid := a
	invalid.Issuer = " https://issuer.example"
	if got, err := NormalizeDevelopmentSeeds([]Seed{a, invalid}); got != nil || ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("invalid later declaration returned partial seeds", err)
	}
}

func TestDevelopmentAliasesUnionExactAndBootstrapAssignments(t *testing.T) {
	a := Seed{TenantID: administrationTenant, Issuer: "issuer", Subject: "same", ActorType: ActorHuman, Roles: []Role{RoleReader}, Namespaces: map[string][]Role{administrationNamespace: {RoleReviewer}}}
	b := a
	b.Roles, b.RoleTemplates = nil, []string{"reader", "library_member"}
	b.Namespaces = map[string][]Role{administrationNamespace: {RoleContributor, RoleReviewer}}
	var prior []string
	for _, seeds := range [][]Seed{{a, b}, {b, a}} {
		store, err := NewMemoryStore(seeds)
		if err != nil {
			t.Fatal(err)
		}
		actor, err := store.ResolvePrincipal(t.Context(), Principal{TenantID: a.TenantID, Issuer: a.Issuer, Subject: a.Subject})
		if err != nil {
			t.Fatal(err)
		}
		rows, err := store.ListEffectiveAssignments(t.Context(), actor, AuthorizationPageRequest{Limit: 100, AuthorizationRevision: 1})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, row := range rows {
			got = append(got, row.Assignment.NamespaceID+":"+row.Role.Key)
		}
		slices.Sort(got)
		want := []string{":discovery_reader", ":feedback_member", ":library_member", ":reader", administrationNamespace + ":contributor", administrationNamespace + ":reviewer"}
		slices.Sort(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("alias grant union=%v want=%v", got, want)
		}
		if prior != nil && !reflect.DeepEqual(got, prior) {
			t.Fatal("alias input order changed grants")
		}
		prior = got
	}
}

func TestDevelopmentAliasesCanonicalizeUUIDsWithoutWideningScopes(t *testing.T) {
	const tenant = "aaaaaaaa-0000-7000-8000-000000000001"
	const namespace = "bbbbbbbb-0000-7000-8000-000000000010"
	const other = "cccccccc-0000-7000-8000-000000000020"
	a := Seed{TenantID: strings.ToUpper(tenant), Issuer: "issuer", Subject: "same", ActorType: ActorHuman, RoleTemplates: []string{"library_member"}, Namespaces: map[string][]Role{strings.ToUpper(namespace): {RoleReviewer, RoleReader, RoleReader}}}
	b := Seed{TenantID: tenant, Issuer: a.Issuer, Subject: a.Subject, ActorType: ActorHuman, Namespaces: map[string][]Role{namespace: {RolePublisher, RoleReviewer}, other: {RoleContributor}}}
	want := Seed{TenantID: tenant, Issuer: a.Issuer, Subject: a.Subject, ActorType: ActorHuman, RoleTemplates: []string{"library_member"}, Namespaces: map[string][]Role{namespace: {RolePublisher, RoleReader, RoleReviewer}, other: {RoleContributor}}}
	var member, realm string
	for _, seeds := range [][]Seed{{a, b}, {b, a}} {
		got, err := NormalizeDevelopmentSeeds(seeds)
		if err != nil || !reflect.DeepEqual(got, []Seed{want}) {
			t.Fatal("UUID aliases lost or duplicated exact grants", got, err)
		}
		store, err := NewMemoryStore(seeds)
		if err != nil {
			t.Fatal(err)
		}
		for _, spelling := range []string{tenant, strings.ToUpper(tenant)} {
			actor, err := store.ResolvePrincipal(t.Context(), Principal{TenantID: spelling, Issuer: a.Issuer, Subject: a.Subject})
			if err != nil || actor.TenantID != tenant || actor.MembershipID == "" || actor.RealmID == "" {
				t.Fatal("canonical principal did not resolve", actor, err)
			}
			if member != "" && (actor.MembershipID != member || actor.RealmID != realm) {
				t.Fatal("UUID spelling or seed order created another principal")
			}
			member, realm = actor.MembershipID, actor.RealmID
			checks := []PermissionCheck{{Permission: "resource.read", NamespaceID: strings.ToUpper(namespace)}, {Permission: "release.publish", NamespaceID: namespace}, {Permission: "review.decide", NamespaceID: other}, {Permission: "release.publish", NamespaceID: other}, {Permission: "resource.create", NamespaceID: other}, {Permission: "resource.read"}, {Permission: "library.own"}, {Permission: "federation.discover"}}
			decision, err := store.CheckPermissions(t.Context(), actor, checks)
			if err != nil || !slices.Equal(decision.Allowed, []bool{true, true, false, false, true, false, true, false}) {
				t.Fatal("canonical aliases widened or lost permission scopes", decision, err)
			}
		}
		got[0].Namespaces[namespace][0] = RoleContributor
		if !slices.Equal(a.Namespaces[strings.ToUpper(namespace)], []Role{RoleReviewer, RoleReader, RoleReader}) || !slices.Equal(b.Namespaces[namespace], []Role{RolePublisher, RoleReviewer}) {
			t.Fatal("merged normalization mutated caller-owned grants")
		}
	}
}
