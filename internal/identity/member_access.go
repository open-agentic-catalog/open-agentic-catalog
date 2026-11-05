package identity

import "context"

// MemberContribution describes a retained path, not an OAuth or action decision.
type MemberContribution struct {
	Assignment AccessAssignment `json:"assignment"`
	Role       RoleDefinition   `json:"role"`
	Group      *AccessGroup     `json:"group,omitempty"`
	Relation   *GroupMember     `json:"relation,omitempty"`
	Effective  bool             `json:"effective"`
}

type MemberAccessPage struct {
	AuthorizationPage[MemberContribution]
	Membership    Membership `json:"membership"`
	GroupCoverage string     `json:"groupCoverage"`
}

// MemberContributionSelection keeps membership lifecycle and paths in the same
// policy-bound store snapshot, including when there are no assignments.
type MemberContributionSelection struct {
	Membership Membership
	Items      []MemberContribution
}

// MemberAccess inspects the target with administrator authority. It never
// impersonates the recipient or discloses another realm's group directory.
func (s *Service) MemberAccess(ctx context.Context, memberID string, limit int, cursor string) (MemberAccessPage, error) {
	if !ValidTenantID(memberID) {
		return MemberAccessPage{}, E(CodeInvalidArgument, "membershipId must be a UUID")
	}
	var selection MemberContributionSelection
	page, err := authorizationPage(ctx, s, "member-access", AuthorizationPageRequest{MembershipID: memberID}, limit, cursor, false, func(store AdministrationStore, actor Actor, request AuthorizationPageRequest) ([]MemberContribution, error) {
		var err error
		selection, err = store.ListMemberContributions(ctx, actor, request)
		return selection.Items, err
	}, func(v MemberContribution) string { return v.Assignment.ID })
	if err != nil {
		return MemberAccessPage{}, err
	}
	actor, _ := FromContext(ctx)
	coverage := "current_realm"
	if selection.Membership.RealmID != actor.RealmID {
		coverage = "unavailable"
	}
	return MemberAccessPage{AuthorizationPage: page, Membership: selection.Membership, GroupCoverage: coverage}, nil
}

func (m *MemoryStore) ListMemberContributions(ctx context.Context, actor Actor, request AuthorizationPageRequest) (MemberContributionSelection, error) {
	return memoryAuthorizationRead(ctx, m, actor, &request, false, func() (MemberContributionSelection, error) {
		member, ok := m.memberships[request.MembershipID]
		if !ok || member.TenantID != actor.TenantID {
			return MemberContributionSelection{}, E(CodeNotFound, "membership was not found")
		}
		items := []MemberContribution{}
		for _, assignment := range m.allAccessAssignmentsLocked() {
			if assignment.TenantID != actor.TenantID || assignment.ID <= request.AfterID {
				continue
			}
			item := MemberContribution{Assignment: cloneAccessAssignment(assignment), Role: cloneDefinition(m.definitions[assignment.RoleDefinitionID])}
			if assignment.MembershipID != member.ID {
				if assignment.GroupID == "" || member.RealmID != actor.RealmID {
					continue
				}
				group := m.groups[assignment.GroupID]
				if group.TenantID != actor.TenantID || group.RealmID != actor.RealmID {
					continue
				}
				for _, relation := range m.groupMembers {
					if relation.TenantID != actor.TenantID || relation.GroupID != group.ID || relation.MembershipID != member.ID || relation.RealmID != actor.RealmID {
						continue
					}
					if item.Relation == nil || relation.State == AssignmentActive && item.Relation.State != AssignmentActive || relation.State == item.Relation.State && (relation.AddedAt.After(item.Relation.AddedAt) || relation.AddedAt.Equal(item.Relation.AddedAt) && relation.ID > item.Relation.ID) {
						retained := cloneGroupMember(relation)
						item.Relation = &retained
					}
				}
				if item.Relation == nil {
					continue
				}
				item.Group = &group
			}
			item.Effective = member.State == MembershipActive && m.realms[member.RealmID].State == RealmActive && assignment.State == AssignmentActive && item.Role.State == DefinitionActive && (item.Group == nil || item.Group.State == GroupActive && item.Relation.State == AssignmentActive)
			items = append(items, item)
		}
		return MemberContributionSelection{Membership: member, Items: memoryPage(items, request.Limit, func(v MemberContribution) string { return v.Assignment.ID })}, nil
	})
}
