package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestAdministrationOverviewRetainedRecordsAndCurrentAuthority(t *testing.T) {
	store, service, admin, alice, sink := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	base, err := service.AdministrationOverview(ctx)
	if err != nil || base.Human.Total.Value != 2 || base.Service.Total.Value != 0 || len(base.Service.States) != 3 || base.ActiveGroups.Value != 0 || *base.ActiveRoles.Custom != 0 {
		t.Fatal(base, err)
	}
	store.memberships["suspended"] = Membership{ID: "suspended", TenantID: admin.TenantID, ActorType: ActorHuman, State: MembershipSuspended, RealmID: "inactive-realm", Subject: "private-subject"}
	store.memberships["service"] = Membership{ID: "service", TenantID: admin.TenantID, ActorType: ActorService, State: MembershipActive, RealmID: "inactive-realm"}
	store.memberships["offboarded"] = Membership{ID: "offboarded", TenantID: admin.TenantID, ActorType: ActorService, State: MembershipOffboarded}
	store.memberships["other"] = Membership{ID: "other", TenantID: "other", ActorType: ActorHuman, State: MembershipActive}
	store.groups["active"] = AccessGroup{ID: "active", TenantID: admin.TenantID, State: GroupActive, RealmID: admin.RealmID}
	store.groups["other-realm"] = AccessGroup{ID: "other-realm", TenantID: admin.TenantID, State: GroupActive, RealmID: "inactive-realm"}
	store.groups["archived"] = AccessGroup{ID: "archived", TenantID: admin.TenantID, RealmID: admin.RealmID, State: GroupArchived}
	store.groups["suspended"] = AccessGroup{ID: "suspended", TenantID: admin.TenantID, RealmID: admin.RealmID, State: GroupSuspended}
	store.definitions["custom"] = RoleDefinition{ID: "custom", TenantID: admin.TenantID, State: DefinitionActive}
	store.definitions["archived"] = RoleDefinition{ID: "archived", TenantID: admin.TenantID, State: DefinitionArchived}
	store.accessAssignments["retained"] = AccessAssignment{ID: "retained", TenantID: admin.TenantID, State: AssignmentActive, MembershipID: "suspended", RoleDefinitionID: "custom"}
	store.accessAssignments["revoked"] = AccessAssignment{ID: "revoked", TenantID: admin.TenantID, State: AssignmentRevoked}
	// The legacy memory projection must not count a second copy of one assignment ID.
	for key, row := range store.assignments {
		store.accessAssignments[key] = AccessAssignment{ID: key, TenantID: row.TenantID, State: row.State, RoleDefinitionID: deterministicUUID("definition", row.TenantID, string(row.Role)), MembershipID: row.MembershipID}
	}
	before := len(sink.records)
	out, err := service.AdministrationOverview(ctx)
	if err != nil || out.Human.Total.Value != 3 || out.Human.States[1].Count != 1 || out.Service.Total.Value != 2 || out.Service.States[0].Count != 1 || out.Service.States[2].Count != 1 || out.ActiveGroups.Value != 1 || *out.ActiveRoles.Custom != 1 || *out.ActiveRoles.Builtin != *base.ActiveRoles.Builtin || out.ActiveAssignments.Value != base.ActiveAssignments.Value+1 {
		t.Fatal(out, err)
	}
	raw, err := json.Marshal(out)
	if err != nil || strings.Contains(string(raw), "private-subject") || len(sink.records) != before {
		t.Fatal(string(raw), err)
	}
	if _, err = service.AdministrationOverview(WithActor(t.Context(), alice)); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal(err)
	}
	if _, err = service.AdministrationOverview(t.Context()); ErrorCodeOf(err) != CodeUnauthenticated {
		t.Fatal(err)
	}
	for key, row := range store.accessAssignments {
		if row.MembershipID == admin.MembershipID {
			row.State = AssignmentRevoked
			store.accessAssignments[key] = row
		}
	}
	for key, row := range store.assignments {
		if row.MembershipID == admin.MembershipID {
			row.State = AssignmentRevoked
			store.assignments[key] = row
		}
	}
	if _, err = service.AdministrationOverview(ctx); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("revoked authority retained summary access", err)
	}
}

type failedAdministrationOverview struct{ *MemoryStore }

func (failedAdministrationOverview) ReadAdministrationOverview(context.Context, Actor) (AdministrationOverviewRows, error) {
	return AdministrationOverviewRows{}, errors.New("injected summary failure")
}

func TestAdministrationOverviewIndependentCeilingsAndFailure(t *testing.T) {
	store, service, admin, _, _ := administrationFixture(t)
	for i := 0; i < 10001; i++ {
		key := fmt.Sprintf("row-%05d", i)
		store.memberships[key] = Membership{ID: key, TenantID: admin.TenantID, ActorType: ActorHuman, State: MembershipSuspended}
		store.groups[key] = AccessGroup{ID: key, TenantID: admin.TenantID, RealmID: admin.RealmID, State: GroupActive}
		store.definitions[key] = RoleDefinition{ID: key, TenantID: admin.TenantID, State: DefinitionActive}
		store.accessAssignments[key] = AccessAssignment{ID: key, TenantID: admin.TenantID, State: AssignmentActive}
	}
	out, err := service.AdministrationOverview(WithActor(t.Context(), admin))
	if err != nil || !out.Human.Total.Limited || out.Human.Total.Value != 10000 || out.Human.States != nil || out.Service.Total.Limited || out.Service.Total.Value != 0 || len(out.Service.States) != 3 || !out.ActiveGroups.Limited || !out.ActiveRoles.Total.Limited || out.ActiveRoles.Builtin != nil || out.ActiveRoles.Custom != nil || !out.ActiveAssignments.Limited {
		t.Fatal(out, err)
	}
	service.store = failedAdministrationOverview{store}
	if _, err = service.AdministrationOverview(WithActor(t.Context(), admin)); err == nil {
		t.Fatal("read failure became a count")
	}
}

func TestAdministrationOverviewServiceCeilingDoesNotLimitHumans(t *testing.T) {
	store, service, admin, _, _ := administrationFixture(t)
	for i := 0; i < 10001; i++ {
		key := fmt.Sprintf("service-%05d", i)
		state := MembershipActive
		if i%2 == 1 {
			state = MembershipOffboarded
		}
		store.memberships[key] = Membership{ID: key, TenantID: admin.TenantID, RealmID: admin.RealmID, Subject: key, ActorType: ActorService, State: state}
	}
	ctx := WithActor(t.Context(), admin)
	out, err := service.AdministrationOverview(ctx)
	if err != nil || out.Service.Total.Value != 10000 || !out.Service.Total.Limited || out.Service.States != nil || out.Human.Total.Value != 2 || out.Human.Total.Limited || len(out.Human.States) != 3 || out.Human.States[0].Count != 2 {
		t.Fatal(out, err)
	}
	delete(store.memberships, "service-10000")
	out, err = service.AdministrationOverview(ctx)
	if err != nil || out.Service.Total.Value != 10000 || out.Service.Total.Limited || len(out.Service.States) != 3 || out.Service.States[0].Count != 5000 || out.Service.States[1].Count != 0 || out.Service.States[2].Count != 5000 || out.Human.Total.Value != 2 {
		t.Fatal(out, err)
	}
}
