//go:build integration

// Real owner provisioning tests preserve scoped policy and retained history.
package database

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func developmentProvisionInput(seed identity.Seed) IdentityProvisioningInput {
	name := "Tenant " + seed.TenantID
	if seed.TenantID == housekeepingTenantA {
		name = "Housekeeping tenant A"
	}
	if seed.TenantID == housekeepingTenantB {
		name = "Housekeeping tenant B"
	}
	return IdentityProvisioningInput{TenantID: seed.TenantID, TenantDisplayName: name, Issuer: authn.DevelopmentIssuer, TenancyMode: "dedicated", Subject: seed.Subject, ActorType: seed.ActorType, Roles: seed.Roles, RoleTemplates: seed.RoleTemplates, Namespaces: seed.Namespaces, Operator: "catalogctl-development-bootstrap", Reason: "Qualify owned development policy", AcknowledgeOutbox: true}
}

func TestPostgresDevelopmentScopedProvisioningAndRollback(t *testing.T) {
	db, store, _, _, _, _ := authorizationCoreFixture(t)
	const foreign = "00000000-0000-7000-8000-000000000999"
	if _, err := db.operator.Exec(t.Context(), `INSERT INTO namespaces(id,tenant_id,slug,display_name,created_at,updated_at,revision) VALUES($1,$2,'foreign','Foreign',clock_timestamp(),clock_timestamp(),1)`, foreign, housekeepingTenantB); err != nil {
		t.Fatal(err)
	}
	for _, mixed := range []bool{false, true} {
		subject := "scoped-only"
		if mixed {
			subject = "scoped-library"
		}
		seed := identity.Seed{TenantID: housekeepingTenantA, Issuer: authn.DevelopmentIssuer, Subject: subject, ActorType: identity.ActorHuman, Namespaces: map[string][]identity.Role{authorizationCoreNamespace: {identity.RoleReader}}}
		if mixed {
			seed.RoleTemplates = []string{"library_member"}
		}
		input := developmentProvisionInput(seed)
		result, err := ProvisionIdentity(t.Context(), db.operator, input)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Assignments) != 1 || result.Assignments[0].NamespaceID != authorizationCoreNamespace || result.Assignments[0].Role != identity.RoleReader || len(result.TemplateAssignments) != len(seed.RoleTemplates) {
			t.Fatal("scoped result lost or widened grants", result)
		}
		repeated, err := ProvisionIdentity(t.Context(), db.operator, input)
		if err != nil || !reflect.DeepEqual(result, repeated) {
			t.Fatal("active reprovisioning changed retained references", result, repeated, err)
		}
		memory, err := identity.NewMemoryStore([]identity.Seed{seed})
		if err != nil {
			t.Fatal(err)
		}
		principal := identity.Principal{TenantID: seed.TenantID, Issuer: seed.Issuer, Subject: seed.Subject}
		actor, err := store.ResolvePrincipal(t.Context(), principal)
		if err != nil {
			t.Fatal(err)
		}
		local, err := memory.ResolvePrincipal(t.Context(), principal)
		if err != nil {
			t.Fatal(err)
		}
		checks := []identity.PermissionCheck{{Permission: "resource.read", NamespaceID: authorizationCoreNamespace}, {Permission: "resource.read", NamespaceID: foreign}, {Permission: "resource.read"}, {Permission: "library.own"}, {Permission: "federation.discover"}, {Permission: "publisher.read"}}
		got, err := store.CheckPermissions(t.Context(), actor, checks)
		if err != nil {
			t.Fatal(err)
		}
		mem, err := memory.CheckPermissions(t.Context(), local, checks)
		want := []bool{true, false, false, mixed, false, false}
		if err != nil || !slices.Equal(got.Allowed, want) || !slices.Equal(mem.Allowed, want) {
			t.Fatal("memory/PostgreSQL scoped authority differs", got, mem, err)
		}
		var details audit.Details
		if err := db.operator.QueryRow(t.Context(), `SELECT details FROM audit_events WHERE tenant_id=$1 AND target_id=$2 AND action='identity.membership_provisioned' ORDER BY occurred_at DESC,id DESC LIMIT 1`, seed.TenantID, result.MembershipID).Scan(&details); err != nil {
			t.Fatal(err)
		}
		facts := details.Facts
		if facts["namespaceGrantCount"] != float64(1) || facts["namespaceGrantsDigest"] != audit.DigestBytes([]byte(`["`+authorizationCoreNamespace+`:reader"]`)) {
			t.Fatal("audit lost exact scoped grant binding", facts)
		}
	}
	counts := func() []int64 {
		t.Helper()
		var values []int64
		if err := db.operator.QueryRow(t.Context(), `SELECT ARRAY[(SELECT count(*) FROM tenant_memberships),(SELECT count(*) FROM role_assignments),(SELECT count(*) FROM namespace_memberships),(SELECT count(*) FROM audit_events),(SELECT count(*) FROM outbox_events),(SELECT count(*) FROM tenant_identity_realms)]`).Scan(&values); err != nil {
			t.Fatal(err)
		}
		return values
	}
	for _, bad := range []string{foreign, "00000000-0000-7000-8000-000000000998"} {
		before := counts()
		seed := identity.Seed{TenantID: housekeepingTenantA, Issuer: authn.DevelopmentIssuer, Subject: "invalid-" + bad, ActorType: identity.ActorHuman, RoleTemplates: []string{"library_member"}, Namespaces: map[string][]identity.Role{authorizationCoreNamespace: {identity.RoleReader}, bad: {identity.RoleReader}}}
		result, err := ProvisionIdentity(t.Context(), db.operator, developmentProvisionInput(seed))
		if err == nil || !reflect.DeepEqual(result, IdentityProvisioningResult{}) || !slices.Equal(before, counts()) {
			t.Fatal("missing/foreign namespace left partial principal or success evidence", result, err)
		}
	}
}

func TestPostgresDevelopmentProvisioningKeepsEachRoleInItsOwnNamespace(t *testing.T) {
	db, store, _, _, _, _ := authorizationCoreFixture(t)
	scopes := []struct {
		namespace string
		role      identity.Role
		allowed   []bool
	}{
		{authorizationCoreNamespace, identity.RoleReader, []bool{true, false, false, false, false}},
		{"00000000-0000-7000-8000-000000000820", identity.RoleContributor, []bool{true, true, false, false, false}},
		{"00000000-0000-7000-8000-000000000821", identity.RoleReviewer, []bool{true, false, true, false, false}},
		{"00000000-0000-7000-8000-000000000822", identity.RolePublisher, []bool{true, false, false, true, true}},
	}
	seed := identity.Seed{TenantID: housekeepingTenantA, Issuer: authn.DevelopmentIssuer, Subject: "four-scoped-roles", ActorType: identity.ActorHuman, Namespaces: map[string][]identity.Role{}}
	checks := make([]identity.PermissionCheck, 0, 25)
	want := make([]bool, 0, 25)
	for i, scope := range scopes {
		if i > 0 {
			if _, err := db.operator.Exec(t.Context(), `INSERT INTO namespaces(id,tenant_id,slug,display_name,created_at,updated_at,revision) VALUES($1,$2,$3,'Scoped qualification',clock_timestamp(),clock_timestamp(),1)`, scope.namespace, seed.TenantID, "scope-"+string(scope.role)); err != nil {
				t.Fatal(err)
			}
		}
		// Repeated input must not duplicate returned references or audit facts.
		seed.Namespaces[scope.namespace] = []identity.Role{scope.role, scope.role}
		for _, permission := range []identity.Permission{"resource.read", "resource.create", "review.decide", "release.publish", "release.transition"} {
			checks = append(checks, identity.PermissionCheck{Permission: permission, NamespaceID: scope.namespace})
		}
		want = append(want, scope.allowed...)
	}
	for _, permission := range []identity.Permission{"resource.read", "federation.discover", "publisher.read", "library.own", "publication.generate"} {
		checks = append(checks, identity.PermissionCheck{Permission: permission})
		want = append(want, false)
	}
	input := developmentProvisionInput(seed)
	first, err := ProvisionIdentity(t.Context(), db.operator, input)
	if err != nil || len(first.Assignments) != 4 || len(first.TemplateAssignments) != 0 {
		t.Fatal("owner provisioned extra or missing role references", first, err)
	}
	for i, assignment := range first.Assignments {
		if assignment.NamespaceID != scopes[i].namespace || assignment.Role != scopes[i].role || assignment.ID == "" || assignment.MembershipID != first.MembershipID || assignment.TenantID != seed.TenantID || assignment.State != identity.AssignmentActive || assignment.Revision != 1 {
			t.Fatal("owner returned a different role, scope or principal", assignment)
		}
	}
	repeated, err := ProvisionIdentity(t.Context(), db.operator, input)
	if err != nil || !reflect.DeepEqual(first, repeated) {
		t.Fatal("repeated multi-namespace provisioning changed active references", repeated, err)
	}
	memory, err := identity.NewMemoryStore([]identity.Seed{seed})
	if err != nil {
		t.Fatal(err)
	}
	principal := identity.Principal{TenantID: seed.TenantID, Issuer: seed.Issuer, Subject: seed.Subject}
	actor, err := store.ResolvePrincipal(t.Context(), principal)
	if err != nil {
		t.Fatal(err)
	}
	local, err := memory.ResolvePrincipal(t.Context(), principal)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := store.CheckPermissions(t.Context(), actor, checks)
	if err != nil || !slices.Equal(actual.Allowed, want) {
		t.Fatal("PostgreSQL role authority crossed its namespace or expanded companions", actual, err)
	}
	mem, err := memory.CheckPermissions(t.Context(), local, checks)
	if err != nil || !slices.Equal(mem.Allowed, want) {
		t.Fatal("memory role authority differs from the independent permission table", mem, err)
	}
	rows, err := store.ListEffectiveAssignments(t.Context(), actor, identity.AuthorizationPageRequest{Limit: 100, AuthorizationRevision: actual.Revision})
	if err != nil || len(rows) != 4 {
		t.Fatal("owner retained unexpected assignments", rows, err)
	}
	var details audit.Details
	if err := db.operator.QueryRow(t.Context(), `SELECT details FROM audit_events WHERE tenant_id=$1 AND target_id=$2 AND action='identity.membership_provisioned' ORDER BY occurred_at DESC,id DESC LIMIT 1`, seed.TenantID, first.MembershipID).Scan(&details); err != nil {
		t.Fatal(err)
	}
	grants := []byte(`["` + authorizationCoreNamespace + `:reader","00000000-0000-7000-8000-000000000820:contributor","00000000-0000-7000-8000-000000000821:reviewer","00000000-0000-7000-8000-000000000822:publisher"]`)
	if details.Facts["namespaceGrantCount"] != float64(4) || details.Facts["namespaceGrantsDigest"] != audit.DigestBytes(grants) {
		t.Fatal("multi-namespace audit count or ordered digest lost exact grants", details.Facts)
	}
}

func TestPostgresDevelopmentProfilesMatchMemoryForAllTwentyPersonas(t *testing.T) {
	db := newHousekeepingDatabase(t)
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"local", "postgres"} {
		cfg := config.Defaults()
		raw, err := os.ReadFile("../../../config/" + profile + ".json") // #nosec G304 -- Fixed two-profile test table, with no external path input.
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		if len(cfg.Auth.DevelopmentTokens) != 20 {
			t.Fatal("persona roster changed")
		}
		seeds := make([]identity.Seed, 0, len(cfg.Auth.DevelopmentTokens))
		for _, u := range cfg.Auth.DevelopmentTokens {
			typ := identity.ActorHuman
			if slices.Contains(u.RoleTemplates, "service_client") {
				typ = identity.ActorService
			}
			seeds = append(seeds, identity.Seed{TenantID: u.TenantID, Issuer: authn.DevelopmentIssuer, Subject: u.Subject, ActorType: typ, RoleTemplates: u.RoleTemplates})
		}
		seeds, err = identity.NormalizeDevelopmentSeeds(seeds)
		if err != nil {
			t.Fatal(err)
		}
		memory, err := identity.NewMemoryStore(seeds)
		if err != nil {
			t.Fatal(err)
		}
		for _, seed := range seeds {
			first, err := ProvisionIdentity(t.Context(), db.operator, developmentProvisionInput(seed))
			if err != nil {
				t.Fatal(profile, seed.Subject, err)
			}
			again, err := ProvisionIdentity(t.Context(), db.operator, developmentProvisionInput(seed))
			if err != nil || !reflect.DeepEqual(first, again) || len(first.TemplateAssignments) != len(seed.RoleTemplates) || len(first.Assignments) != 0 {
				t.Fatal("fresh/repeated exact persona grants differ", profile, seed.Subject, err)
			}
			principal := identity.Principal{TenantID: seed.TenantID, Issuer: seed.Issuer, Subject: seed.Subject}
			a, err := store.ResolvePrincipal(t.Context(), principal)
			if err != nil {
				t.Fatal(err)
			}
			m, err := memory.ResolvePrincipal(t.Context(), principal)
			if err != nil {
				t.Fatal(err)
			}
			permissions := identity.Permissions()
			checks := make([]identity.PermissionCheck, 0, len(permissions))
			for _, p := range permissions {
				checks = append(checks, identity.PermissionCheck{Permission: p.Key})
			}
			actual, err := store.CheckPermissions(t.Context(), a, checks)
			if err != nil {
				t.Fatal(err)
			}
			want, err := memory.CheckPermissions(t.Context(), m, checks)
			if err != nil || !slices.Equal(actual.Allowed, want.Allowed) || a.ActorType != seed.ActorType {
				t.Fatal("persona permission union/type changed", profile, seed.Subject, actual, want, err)
			}
			rows, err := store.ListEffectiveAssignments(t.Context(), a, identity.AuthorizationPageRequest{Limit: 100, AuthorizationRevision: actual.Revision})
			if err != nil {
				t.Fatal(err)
			}
			var keys []string
			for _, row := range rows {
				if row.Assignment.NamespaceID != "" || row.Assignment.MembershipID != a.MembershipID {
					t.Fatal("unexpected persona scope or target", row)
				}
				keys = append(keys, row.Role.Key)
			}
			slices.Sort(keys)
			expected := slices.Clone(seed.RoleTemplates)
			slices.Sort(expected)
			if !slices.Equal(keys, expected) {
				t.Fatal("persona has redundant or missing grants", profile, seed.Subject, keys, expected)
			}
		}
	}
}

func TestPostgresDevelopmentProvisioningIsAdditiveAndRegrantsExplicitly(t *testing.T) {
	db, store, _, _, _, _ := authorizationCoreFixture(t)
	seed := identity.Seed{TenantID: housekeepingTenantA, Issuer: authn.DevelopmentIssuer, Subject: "retained", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleContributor, identity.RoleReader}}
	old, err := ProvisionIdentity(t.Context(), db.operator, developmentProvisionInput(seed))
	if err != nil {
		t.Fatal(err)
	}
	adminSeed := seed
	adminSeed.Subject, adminSeed.Roles = "development-admin", []identity.Role{identity.RoleTenantAdmin}
	if _, err = ProvisionIdentity(t.Context(), db.operator, developmentProvisionInput(adminSeed)); err != nil {
		t.Fatal(err)
	}
	admin, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: seed.TenantID, Issuer: seed.Issuer, Subject: adminSeed.Subject})
	if err != nil {
		t.Fatal(err)
	}
	var now time.Time
	if err = db.operator.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	s, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.NewFake(now.Add(time.Second)), IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), admin)
	custom, err := s.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Retained audit reader", Permissions: []identity.Permission{"audit.read"}, Reason: "Retain independent policy"}, false, "custom")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: custom.ID, MembershipID: old.MembershipID, Reason: "Retain an unrelated custom grant"}, "grant-custom")
	if err != nil {
		t.Fatal(err)
	}
	seed.Roles, seed.RoleTemplates = nil, []string{"contributor", "discovery_reader", "feedback_member", "publisher_creator"}
	current, err := ProvisionIdentity(t.Context(), db.operator, developmentProvisionInput(seed))
	if err != nil {
		t.Fatal(err)
	}
	var active int
	if err = db.operator.QueryRow(t.Context(), `SELECT count(*) FROM role_assignments WHERE tenant_id=$1 AND membership_id=$2 AND state='active'`, seed.TenantID, old.MembershipID).Scan(&active); err != nil || active != 6 {
		t.Fatal("new profile removed retained assignments", active, err)
	}
	for _, a := range old.Assignments {
		if a.Role == identity.RoleReader {
			if _, err = s.RevokeRole(ctx, a.ID, "Remove only redundant Reader path", "reader", a.Revision); err != nil {
				t.Fatal(err)
			}
		}
	}
	principal := identity.Principal{TenantID: seed.TenantID, Issuer: seed.Issuer, Subject: seed.Subject}
	actor, err := store.ResolvePrincipal(t.Context(), principal)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := store.CheckPermissions(t.Context(), actor, []identity.PermissionCheck{{Permission: "resource.read"}, {Permission: "audit.read"}})
	if err != nil || !slices.Equal(decision.Allowed, []bool{true, true}) {
		t.Fatal("revoking overlap removed remaining authority", decision, err)
	}
	var revoked identity.AccessAssignment
	for _, a := range current.TemplateAssignments {
		if a.RoleDefinitionID == "" {
			t.Fatal("missing template reference")
		}
		var key string
		if err = db.operator.QueryRow(t.Context(), `SELECT key FROM role_definitions WHERE tenant_id=$1 AND id=$2`, seed.TenantID, a.RoleDefinitionID).Scan(&key); err != nil {
			t.Fatal(err)
		}
		if key == "publisher_creator" {
			revoked, err = s.RevokeAuthorizationAssignment(ctx, a.ID, a.Revision, "Test explicit configured regrant", "revoke")
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	decision, err = store.CheckPermissions(t.Context(), actor, []identity.PermissionCheck{{Permission: "publisher.create"}})
	if err != nil || decision.Allowed[0] {
		t.Fatal("revocation did not remove configured authority", decision, err)
	}
	restored, err := ProvisionIdentity(t.Context(), db.operator, developmentProvisionInput(seed))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range restored.TemplateAssignments {
		if a.ID == revoked.ID {
			t.Fatal("provisioning rewrote revoked history")
		}
	}
	decision, err = store.CheckPermissions(t.Context(), actor, []identity.PermissionCheck{{Permission: "publisher.create"}, {Permission: "audit.read"}})
	if err != nil || !slices.Equal(decision.Allowed, []bool{true, true}) {
		t.Fatal("explicit regrant lost configured or unrelated authority", decision, err)
	}
	var kept bool
	if err = db.operator.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM role_assignments WHERE tenant_id=$1 AND id=$2 AND state='active' AND revision=$3) AND EXISTS(SELECT 1 FROM role_assignments WHERE tenant_id=$1 AND id=$4 AND state='revoked' AND revision=2)`, seed.TenantID, grant.ID, grant.Revision, revoked.ID).Scan(&kept); err != nil || !kept {
		t.Fatal("provisioning rewrote custom policy or revoked history", kept, err)
	}
	// Namespace reprovisioning also restores only the explicitly configured
	// path, even when the retained revocation time is ahead of the owner clock.
	seed.Subject, seed.RoleTemplates, seed.Namespaces = "revoked-scoped", nil, map[string][]identity.Role{authorizationCoreNamespace: {identity.RoleReader}}
	scoped, err := ProvisionIdentity(t.Context(), db.operator, developmentProvisionInput(seed))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RevokeRole(ctx, scoped.Assignments[0].ID, "Explicit scoped revocation", "scoped-revoke", scoped.Assignments[0].Revision); err != nil {
		t.Fatal(err)
	}
	regranted, err := ProvisionIdentity(t.Context(), db.operator, developmentProvisionInput(seed))
	if err != nil || len(regranted.Assignments) != 1 || regranted.Assignments[0].ID == scoped.Assignments[0].ID || regranted.Assignments[0].NamespaceID != authorizationCoreNamespace || len(regranted.TemplateAssignments) != 0 {
		t.Fatal("scoped reprovisioning rewrote history or widened access", regranted, err)
	}
	if err = db.operator.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM namespace_memberships WHERE tenant_id=$1 AND membership_id=$2 AND namespace_id=$3 AND state='active' AND revision=3) AND EXISTS(SELECT 1 FROM role_assignments WHERE tenant_id=$1 AND id=$4 AND state='revoked' AND revision=2)`, seed.TenantID, scoped.MembershipID, authorizationCoreNamespace, scoped.Assignments[0].ID).Scan(&kept); err != nil || !kept {
		t.Fatal("scoped membership/history restoration is inconsistent", kept, err)
	}
}
