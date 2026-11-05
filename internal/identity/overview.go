package identity

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/overview"
)

type MembershipStateCount struct {
	State MembershipState `json:"state"`
	Count int             `json:"count"`
}
type MembershipOverview struct {
	Total  overview.BoundedCount  `json:"total"`
	States []MembershipStateCount `json:"states,omitempty"`
}
type RoleOverview struct {
	Total   overview.BoundedCount `json:"total"`
	Builtin *int                  `json:"builtin,omitempty"`
	Custom  *int                  `json:"custom,omitempty"`
}
type AdministrationOverview struct {
	overview.Scope
	Human             MembershipOverview    `json:"human"`
	Service           MembershipOverview    `json:"service"`
	ActiveGroups      overview.BoundedCount `json:"activeGroups"`
	ActiveRoles       RoleOverview          `json:"activeRoles"`
	ActiveAssignments overview.BoundedCount `json:"activeAssignments"`
}

// AdministrationOverviewRows contains bounded scalar metadata only.
type AdministrationOverviewRows struct {
	Human, Service      []MembershipState
	Groups, Assignments int
	BuiltinRoles        []bool
}

func membershipOverview(rows []MembershipState) MembershipOverview {
	out := MembershipOverview{Total: overview.Count(len(rows))}
	if out.Total.Limited {
		return out
	}
	out.States = []MembershipStateCount{{State: MembershipActive}, {State: MembershipSuspended}, {State: MembershipOffboarded}}
	for _, state := range rows {
		for i := range out.States {
			if out.States[i].State == state {
				out.States[i].Count++
			}
		}
	}
	return out
}

func (s *Service) AdministrationOverview(ctx context.Context) (AdministrationOverview, error) {
	actor, _, err := s.currentAuthorization(ctx, true)
	if err != nil {
		return AdministrationOverview{}, err
	}
	store, err := s.administrationStore()
	if err != nil {
		return AdministrationOverview{}, err
	}
	at := s.clock.Now()
	rows, err := store.ReadAdministrationOverview(ctx, actor)
	if err != nil {
		return AdministrationOverview{}, err
	}
	roles := RoleOverview{Total: overview.Count(len(rows.BuiltinRoles))}
	if !roles.Total.Limited {
		builtin, custom := 0, 0
		for _, value := range rows.BuiltinRoles {
			if value {
				builtin++
			} else {
				custom++
			}
		}
		roles.Builtin, roles.Custom = &builtin, &custom
	}
	return AdministrationOverview{Scope: overview.NewScope(actor.TenantID, at), Human: membershipOverview(rows.Human), Service: membershipOverview(rows.Service), ActiveGroups: overview.Count(rows.Groups), ActiveRoles: roles, ActiveAssignments: overview.Count(rows.Assignments)}, nil
}

func (m *MemoryStore) ReadAdministrationOverview(ctx context.Context, actor Actor) (AdministrationOverviewRows, error) {
	return memoryAuthorizationRead(ctx, m, actor, nil, false, func() (AdministrationOverviewRows, error) {
		out := AdministrationOverviewRows{}
		for _, member := range m.memberships {
			if member.TenantID != actor.TenantID {
				continue
			}
			if member.ActorType == ActorHuman && len(out.Human) <= overview.SelectionLimit {
				out.Human = append(out.Human, member.State)
			}
			if member.ActorType == ActorService && len(out.Service) <= overview.SelectionLimit {
				out.Service = append(out.Service, member.State)
			}
		}
		for _, group := range m.groups {
			if group.TenantID == actor.TenantID && group.RealmID == actor.RealmID && group.State == GroupActive && out.Groups <= overview.SelectionLimit {
				out.Groups++
			}
		}
		for _, role := range m.definitions {
			if role.TenantID == actor.TenantID && role.State == DefinitionActive && len(out.BuiltinRoles) <= overview.SelectionLimit {
				out.BuiltinRoles = append(out.BuiltinRoles, role.Builtin)
			}
		}
		for key, assignment := range m.assignments {
			if _, exists := m.accessAssignments[key]; exists {
				continue
			}
			if assignment.TenantID == actor.TenantID && assignment.State == AssignmentActive && out.Assignments <= overview.SelectionLimit {
				out.Assignments++
			}
		}
		for _, assignment := range m.accessAssignments {
			if assignment.TenantID == actor.TenantID && assignment.State == AssignmentActive && out.Assignments <= overview.SelectionLimit {
				out.Assignments++
			}
		}
		return out, nil
	})
}
