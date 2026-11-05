// Development owner plans are validated without opening a database or process.
package main

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// TestShippedDevelopmentProfilesKeepExactAssignmentsAndPermissionUnions uses
// literal old bundle roles as the permission baseline and independent new keys.
func TestShippedDevelopmentProfilesKeepExactAssignmentsAndPermissionUnions(t *testing.T) {
	bundles := map[string][]string{
		"local-admin": {"tenant_admin"}, "other-tenant-admin": {"tenant_admin"},
		"local-reader":      {"reader", "discovery_reader", "feedback_member"},
		"local-contributor": {"contributor", "discovery_reader", "feedback_member", "publisher_creator"},
		"local-reviewer":    {"reviewer", "discovery_reader", "feedback_member"},
		"local-publisher":   {"publisher", "discovery_reader", "feedback_member", "publication_admin"},
	}
	old := map[string][]identity.Role{
		"local-admin":        {identity.RoleTenantAdmin, identity.RoleContributor, identity.RoleReviewer, identity.RolePublisher, identity.RoleSourceAdmin, identity.RoleSecurityReviewer},
		"other-tenant-admin": {identity.RoleTenantAdmin, identity.RoleContributor, identity.RoleReviewer, identity.RolePublisher},
		"local-reader":       {identity.RoleReader}, "local-contributor": {identity.RoleContributor, identity.RoleReader},
		"local-reviewer": {identity.RoleReviewer, identity.RoleReader}, "local-publisher": {identity.RolePublisher, identity.RoleReader},
	}
	for _, profile := range []string{"local", "postgres"} {
		cfg := config.Defaults()
		raw, err := os.ReadFile("../../config/" + profile + ".json") // #nosec G304 -- Fixed two-profile test table, with no external path input.
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		if len(cfg.Auth.DevelopmentTokens) != 20 {
			t.Fatal("persona roster changed")
		}
		cfg.Database.Driver, cfg.Database.DSN = "postgres", "postgres://must-not-connect.invalid/catalog"
		plan, err := planIdentityProvisioning([]string{"provision-development", "--config", writeCatalogctlTestConfig(t, cfg)})
		if err != nil || len(plan.inputs) != 20 {
			t.Fatal("shipped profile plan", err)
		}
		for alias, user := range cfg.Auth.DevelopmentTokens {
			expectedSubject, expectedTenant := alias, "00000000-0000-7000-8000-000000000001"
			if alias == "other-tenant-admin" {
				expectedSubject, expectedTenant = "other-admin", "00000000-0000-7000-8000-000000000002"
			}
			if user.Subject != expectedSubject || user.TenantID != expectedTenant {
				t.Fatal("shipped principal binding changed", profile, alias, user.Subject, user.TenantID)
			}
			want, exists := bundles[alias]
			if !exists {
				want = []string{strings.ReplaceAll(strings.TrimPrefix(alias, "local-template-"), "-", "_")}
			}
			if len(user.Roles) != 0 || !slices.Equal(user.RoleTemplates, want) {
				t.Fatal("unexpected exact grants", profile, alias, user.RoleTemplates)
			}
			input := identity.Seed{TenantID: user.TenantID, Issuer: "profile-test", Subject: user.Subject, ActorType: identity.ActorHuman, RoleTemplates: user.RoleTemplates}
			if alias == "local-template-service-client" {
				input.ActorType = identity.ActorService
			}
			current, err := identity.NewMemoryStore([]identity.Seed{input})
			if err != nil {
				t.Fatal(err)
			}
			baselineInput := input
			if roles, exists := old[alias]; exists {
				baselineInput.Roles, baselineInput.RoleTemplates = roles, nil
			}
			baseline, err := identity.NewMemoryStore([]identity.Seed{baselineInput})
			if err != nil {
				t.Fatal(err)
			}
			principal := identity.Principal{TenantID: user.TenantID, Issuer: input.Issuer, Subject: user.Subject}
			actor, err := current.ResolvePrincipal(t.Context(), principal)
			if err != nil {
				t.Fatal(err)
			}
			prior, err := baseline.ResolvePrincipal(t.Context(), principal)
			if err != nil {
				t.Fatal(err)
			}
			permissions := identity.Permissions()
			checks := make([]identity.PermissionCheck, 0, len(permissions))
			for _, p := range permissions {
				checks = append(checks, identity.PermissionCheck{Permission: p.Key})
			}
			got, err := current.CheckPermissions(t.Context(), actor, checks)
			if err != nil {
				t.Fatal(err)
			}
			before, err := baseline.CheckPermissions(t.Context(), prior, checks)
			if err != nil || !slices.Equal(got.Allowed, before.Allowed) {
				t.Fatal("profile permission union changed", profile, alias, err)
			}
			rows, err := current.ListEffectiveAssignments(t.Context(), actor, identity.AuthorizationPageRequest{Limit: 100, AuthorizationRevision: got.Revision})
			if err != nil || len(rows) != len(want) {
				t.Fatal("extra or missing profile assignments", profile, alias, rows, err)
			}
		}
	}
}

func TestDevelopmentPlanRetainsScopedOnlyAndMixedGrants(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		cfg := config.Defaults()
		cfg.Database.Driver, cfg.Database.DSN = "postgres", "postgres://must-not-connect.invalid/catalog"
		u := config.DevelopmentUser{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "scoped", Namespaces: map[string][]identity.Role{"aaaaaaaa-0000-7000-8000-000000000010": {identity.RoleReader}}}
		if mixed {
			u.RoleTemplates = []string{"library_member"}
		}
		cfg.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{"synthetic-alias": u}
		plan, err := planIdentityProvisioning([]string{"provision-development", "--config", writeCatalogctlTestConfig(t, cfg)})
		if err != nil || len(plan.inputs) != 1 {
			t.Fatal(plan.inputs, err)
		}
		input := plan.inputs[0]
		if !reflect.DeepEqual(input.Namespaces, u.Namespaces) || !slices.Equal(input.RoleTemplates, u.RoleTemplates) || len(input.Roles) != 0 {
			t.Fatal("scope was dropped or widened", input)
		}
		input.Namespaces["aaaaaaaa-0000-7000-8000-000000000010"][0] = identity.RoleContributor
		if u.Namespaces["aaaaaaaa-0000-7000-8000-000000000010"][0] != identity.RoleReader || plan.configuration.Auth.DevelopmentTokens["synthetic-alias"].Namespaces["aaaaaaaa-0000-7000-8000-000000000010"][0] != identity.RoleReader {
			t.Fatal("plan aliases configuration")
		}
	}
}

func TestDevelopmentPlanCombinesAliasesAndRejectsConflictsBeforeExecution(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.Driver, cfg.Database.DSN = "postgres", "postgres://must-not-connect.invalid/catalog"
	a := config.DevelopmentUser{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "same", Roles: []identity.Role{identity.RoleReader}}
	b := a
	b.Roles, b.RoleTemplates = nil, []string{"library_member", "reader"}
	for _, conflict := range []bool{false, true} {
		if conflict {
			b.RoleTemplates = []string{"service_client"}
		}
		for _, reverse := range []bool{false, true} {
			left, right := a, b
			if reverse {
				left, right = right, left
			}
			cfg.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{"synthetic-secret-left": left, "synthetic-secret-right": right}
			plan, err := planIdentityProvisioning([]string{"provision-development", "--config", writeCatalogctlTestConfig(t, cfg)})
			if conflict {
				if err == nil || len(plan.inputs) != 0 || !strings.Contains(err.Error(), "conflicting actor types") || strings.Contains(err.Error(), "synthetic-secret") {
					t.Fatal("conflict returned a usable plan or unsafe error", err)
				}
			} else if err != nil || len(plan.inputs) != 1 || len(plan.inputs[0].Roles) != 0 || !slices.Equal(plan.inputs[0].RoleTemplates, []string{"discovery_reader", "feedback_member", "library_member", "reader"}) {
				t.Fatal("aliases were not normalized once", plan.inputs, err)
			}
		}
	}
}

func TestDevelopmentMembershipOnlyPlansKeepTypeSeparateFromTheEmptyTemplate(t *testing.T) {
	for _, test := range []struct {
		user config.DevelopmentUser
		want identity.ActorType
	}{
		{config.DevelopmentUser{}, identity.ActorHuman},
		{config.DevelopmentUser{ActorType: identity.ActorHuman, RoleTemplates: []string{"service_client"}}, identity.ActorHuman},
		{config.DevelopmentUser{ActorType: identity.ActorService}, identity.ActorService},
		{config.DevelopmentUser{RoleTemplates: []string{"service_client"}}, identity.ActorService},
	} {
		cfg := config.Defaults()
		cfg.Database.Driver, cfg.Database.DSN = "postgres", "postgres://must-not-connect.invalid/catalog"
		u := test.user
		u.TenantID, u.Subject = "00000000-0000-7000-8000-000000000001", "membership-only"
		cfg.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{"synthetic-only": u}
		plan, err := planIdentityProvisioning([]string{"provision-development", "--config", writeCatalogctlTestConfig(t, cfg)})
		if err != nil || len(plan.inputs) != 1 {
			t.Fatal("membership-only plan refused before owner execution", err)
		}
		input := plan.inputs[0]
		if input.ActorType != test.want || !slices.Equal(input.RoleTemplates, []string{"service_client"}) || len(input.Roles) != 0 || len(input.Namespaces) != 0 {
			t.Fatal("membership-only plan changed type or widened grants", input)
		}
	}
}
