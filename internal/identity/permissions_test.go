package identity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPermissionRegistryMatchesAcceptedOracleAndReturnsDetachedData(t *testing.T) {
	raw, err := os.ReadFile("../../test/authorization/inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Permissions map[string]struct {
			Scope     string   `json:"scope"`
			Delegable bool     `json:"delegable"`
			Requires  []string `json:"requires"`
		} `json:"permissions"`
		Builtins map[string][]string `json:"builtins"`
	}
	if err := json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	descriptors := Permissions()
	if len(descriptors) != 50 || len(descriptors) != len(oracle.Permissions) {
		t.Fatal("registry changed without reviewed oracle")
	}
	seen := map[Permission]bool{}
	for _, d := range descriptors {
		want, ok := oracle.Permissions[string(d.Key)]
		if !ok || seen[d.Key] || d.NamespaceScoped != (want.Scope == "namespace") || d.Delegable != want.Delegable {
			t.Fatalf("descriptor %v", d)
		}
		seen[d.Key] = true
		values := make([]string, len(d.Requires))
		for i, v := range d.Requires {
			values[i] = string(v)
		}
		if !slices.Equal(values, want.Requires) {
			t.Fatalf("requirements for %s", d.Key)
		}
	}
	for key, want := range oracle.Builtins {
		got, ok := BuiltinPermissions(key)
		if !ok || len(got) != len(want) {
			t.Fatalf("template %s", key)
		}
		for i, v := range got {
			if string(v) != want[i] || !seen[v] {
				t.Fatalf("template %s", key)
			}
		}
		if len(got) > 0 {
			got[0] = "bad"
			again, _ := BuiltinPermissions(key)
			if again[0] == "bad" {
				t.Fatal("mutable template")
			}
		}
	}
	descriptors[0].Key = "bad"
	if Permissions()[0].Key == "bad" {
		t.Fatal("mutable registry")
	}
	for i, d := range descriptors {
		if len(d.Requires) > 0 {
			descriptors[i].Requires[0] = "bad"
			if Permissions()[i].Requires[0] == "bad" {
				t.Fatal("mutable requirements")
			}
			break
		}
	}
	if _, ok := BuiltinPermissions("invented"); ok {
		t.Fatal("unknown builtin")
	}
}
func TestPermissionCheckBoundaries(t *testing.T) {
	for _, checks := range [][]PermissionCheck{nil, make([]PermissionCheck, 65), {{Permission: "invented"}}, {{Permission: "access.manage", NamespaceID: "00000000-0000-7000-8000-000000000010"}}, {{Permission: "resource.read", NamespaceID: "bad"}}} {
		if ErrorCodeOf(ValidatePermissionChecks(checks)) != CodeInvalidArgument {
			t.Fatalf("accepted %v", checks)
		}
	}
	for _, checks := range [][]PermissionCheck{{{Permission: "access.manage"}}, {{Permission: "resource.read", NamespaceID: "00000000-0000-7000-8000-000000000010"}}, {{Permission: "resource.read"}}} {
		if err := ValidatePermissionChecks(checks); err != nil {
			t.Fatal(err)
		}
	}
}
func TestMemoryCurrentPermissionsAndAdministrationRevocation(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	const ns = "aaaaaaaa-0000-7000-8000-000000000010"
	store, err := NewMemoryStore([]Seed{{TenantID: tenant, Issuer: "issuer", Subject: "admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}}, {TenantID: tenant, Issuer: "issuer", Subject: "alice", ActorType: ActorHuman, Namespaces: map[string][]Role{ns: {RoleReader}}}})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := store.ResolvePrincipal(t.Context(), Principal{Issuer: "issuer", TenantID: tenant, Subject: "alice"})
	if err != nil || len(actor.Roles) != 0 || actor.Namespaces != nil {
		t.Fatalf("compact actor %v %v", actor, err)
	}
	checks := []PermissionCheck{{Permission: "resource.read", NamespaceID: ns}, {Permission: "resource.read"}, {Permission: "resource.edit", NamespaceID: ns}, {Permission: "access.manage"}}
	decision, err := store.CheckPermissions(t.Context(), actor, checks)
	if err != nil || !reflect.DeepEqual(decision.Allowed, []bool{true, false, false, false}) {
		t.Fatalf("%v %v", decision, err)
	}
	caseDecision, err := store.CheckPermissions(t.Context(), actor, []PermissionCheck{{Permission: "resource.read", NamespaceID: strings.ToUpper(ns)}})
	if err != nil || !caseDecision.Allowed[0] {
		t.Fatal("memory UUID scope differs from PostgreSQL", err)
	}
	called := false
	if err := store.WithPermissionAdmission(t.Context(), actor, checks, func(context.Context) error { called = true; return nil }); ErrorCodeOf(err) != CodeForbidden || called {
		t.Fatal("denied callback ran")
	}
	err = store.WithPermissionAdmission(t.Context(), actor, checks[:1], func(ctx context.Context) error {
		_, e := store.CheckPermissions(ctx, actor, checks[:1])
		if e != nil {
			return e
		}
		_, e = store.RevokeRole(ctx, RevokeRoleParams{})
		if ErrorCodeOf(e) != CodeConflict {
			t.Fatal("shared fence upgraded")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	forged := actor
	forged.Roles = []Role{RoleTenantAdmin}
	projection, _ := store.CheckPermissions(t.Context(), forged, checks)
	if !reflect.DeepEqual(projection.Allowed, decision.Allowed) {
		t.Fatal("Actor role projection granted authority")
	}
	forged.Subject = "other"
	d, err := store.CheckPermissions(t.Context(), forged, checks)
	if err != nil || slices.Contains(d.Allowed, true) {
		t.Fatalf("forged actor admitted %v %v", d, err)
	}
	admin, _ := store.Resolve(t.Context(), Principal{Issuer: "issuer", TenantID: tenant, Subject: "admin"})
	service, err := NewService(ServiceOptions{Store: store, Clock: identityTestClock{at: time.Now()}, IDs: &identityTestIDs{}, CursorKey: identityTestCursorKey, AcknowledgeOutboxAtCreation: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithActor(t.Context(), admin)
	suspended, err := service.SuspendMembership(ctx, actor.MembershipID, "Temporary hold", "suspend", 1)
	if err != nil {
		t.Fatal(err)
	}
	d, err = store.CheckPermissions(t.Context(), actor, checks)
	if err != nil || slices.Contains(d.Allowed, true) || d.Revision <= decision.Revision {
		t.Fatalf("stale authority %v %v", d, err)
	}
	sink := &recordingIdentityEvidenceSink{err: errors.New("evidence unavailable")}
	store.evidence = sink
	_, err = service.ReactivateMembership(ctx, actor.MembershipID, "Resume access", "resume", suspended.Membership.Revision)
	if err == nil {
		t.Fatal("missing evidence accepted")
	}
	after, _ := store.CheckPermissions(t.Context(), actor, checks)
	if !reflect.DeepEqual(after, d) {
		t.Fatal("failed evidence changed revision or authority")
	}
}

func TestMemoryPermissionAdmissionCancellationAndPanicRelease(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	store, err := NewMemoryStore([]Seed{{TenantID: tenant, Issuer: "issuer", Subject: "admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}}})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := store.ResolvePrincipal(t.Context(), Principal{Issuer: "issuer", TenantID: tenant, Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	checks := []PermissionCheck{{Permission: "resource.read"}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.CheckPermissions(ctx, actor, checks); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	called := false
	if err := store.WithPermissionAdmission(ctx, actor, checks, func(context.Context) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatal("canceled callback admitted", err)
	}
	var escaped context.Context
	if err := store.WithPermissionAdmission(t.Context(), actor, checks, func(ctx context.Context) error {
		escaped = ctx //nolint:fatcontext // Deliberately retain callback input to prove admission expires after return.
		if _, e := store.ResolvePrincipal(ctx, Principal{Issuer: "issuer", TenantID: tenant, Subject: "admin"}); e != nil {
			return e
		}
		_, e := store.GetMembership(ctx, tenant, actor.MembershipID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if memoryAdmissionFrom(escaped, store) {
		t.Fatal("escaped context retained admission")
	}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = store.WithPermissionAdmission(t.Context(), actor, checks, func(context.Context) error { panic("owned panic") })
	}()
	if recovered == nil {
		t.Fatal("panic swallowed")
	}
	store.mu.Lock()
	revision := store.revisions[tenant]
	store.mu.Unlock() // Recovery must release the shared admission.
	if revision != 1 {
		t.Fatal("panicking read admission changed authority")
	}
}

func TestMemoryPermissionBoundaryFailuresAndNestedAdmission(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	store, err := NewMemoryStore([]Seed{{TenantID: tenant, Issuer: "issuer", Subject: "admin", ActorType: ActorHuman, Roles: []Role{RoleTenantAdmin}}})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := store.ResolvePrincipal(t.Context(), Principal{Issuer: "issuer", TenantID: tenant, Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	checks := []PermissionCheck{{Permission: "access.manage"}}
	if _, err := store.CheckPermissions(t.Context(), actor, nil); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
	if err := store.WithPermissionAdmission(t.Context(), actor, nil, func(context.Context) error { return nil }); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
	if err := store.WithPermissionAdmission(t.Context(), actor, checks, nil); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
	different := actor
	different.Subject = "other"
	if err := store.WithPermissionAdmission(WithActor(t.Context(), actor), different, checks, func(context.Context) error { t.Fatal("mismatched admission callback ran"); return nil }); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal(err)
	}
	if _, err := store.CheckPermissions(t.Context(), Actor{}, checks); ErrorCodeOf(err) != CodeUnauthenticated {
		t.Fatal(err)
	}
	if err := store.WithPermissionAdmission(t.Context(), actor, checks, func(ctx context.Context) error {
		return store.WithPermissionAdmission(ctx, actor, checks, func(inner context.Context) error {
			_, e := store.GetMembership(inner, tenant, actor.MembershipID)
			return e
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantRole(WithActor(t.Context(), actor), GrantRoleParams{TenantID: "00000000-0000-7000-8000-000000000002"}); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("cross-tenant administration admitted", err)
	}
	store.mu.Lock()
	delete(store.revisions, tenant)
	store.mu.Unlock()
	if err := store.WithPermissionAdmission(t.Context(), actor, checks, func(context.Context) error { t.Fatal("missing-state callback ran"); return nil }); ErrorCodeOf(err) != CodeDependency {
		t.Fatal(err)
	}
	if _, err := store.GrantRole(WithActor(t.Context(), actor), GrantRoleParams{TenantID: tenant}); ErrorCodeOf(err) != CodeDependency {
		t.Fatal("missing-state administration admitted", err)
	}
}

func TestPermissionAdmissionPrincipalBinding(t *testing.T) {
	actor := Actor{TenantID: "00000000-0000-7000-8000-000000000001", RealmID: "00000000-0000-7000-8000-000000000010", MembershipID: "00000000-0000-7000-8000-000000000020", Subject: "admin", ActorType: ActorHuman}
	if _, err := BindPermissionActor(t.Context(), Actor{}); ErrorCodeOf(err) != CodeUnauthenticated {
		t.Fatal(err)
	}
	ctx, err := BindPermissionActor(t.Context(), actor)
	if err != nil {
		t.Fatal(err)
	}
	if bound, ok := FromContext(ctx); !ok || bound.Subject != actor.Subject {
		t.Fatal("admitted work lost attribution")
	}
	current := actor
	current.Claims = map[string]string{"scope": "original"}
	ctx = WithActor(t.Context(), current)
	projected := actor
	projected.Claims = map[string]string{"scope": "replacement"}
	bound, err := BindPermissionActor(ctx, projected)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := FromContext(bound); got.Claims["scope"] != "original" {
		t.Fatal("authenticated annotations replaced")
	}
	for _, field := range []string{"tenant", "realm", "membership", "subject", "type"} {
		different := actor
		switch field {
		case "tenant":
			different.TenantID = "00000000-0000-7000-8000-000000000002"
		case "realm":
			different.RealmID = "00000000-0000-7000-8000-000000000011"
		case "membership":
			different.MembershipID = "00000000-0000-7000-8000-000000000021"
		case "subject":
			different.Subject = "other"
		case "type":
			different.ActorType = ActorService
		}
		if _, err := BindPermissionActor(ctx, different); ErrorCodeOf(err) != CodeForbidden {
			t.Fatalf("%s mismatch admitted %v", field, err)
		}
	}
}
