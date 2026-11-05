package identity

import (
	"fmt"
	"strings"
)

// ValidateMembershipFilters returns one exact filter, or refuses ambiguous input.
func ValidateMembershipFilters(filters []MembershipFilter) (MembershipFilter, error) {
	if len(filters) > 1 {
		return MembershipFilter{}, E(CodeInvalidArgument, "one membership filter is permitted")
	}
	f := MembershipFilter{}
	if len(filters) == 1 {
		f = filters[0]
	}
	if f.Subject != "" && !ValidSubject(f.Subject) || f.ActorType != "" && !ValidActorType(f.ActorType) || f.RealmID != "" && !ValidTenantID(f.RealmID) || f.State != "" && f.State != MembershipActive && f.State != MembershipSuspended && f.State != MembershipOffboarded {
		return MembershipFilter{}, E(CodeInvalidArgument, "membership directory filter is invalid")
	}
	f.RealmID = strings.ToLower(f.RealmID)
	return f, nil
}

func (f MembershipFilter) binding() string {
	return fmt.Sprintf("/%q/%q/%q/%q", f.Subject, f.State, f.ActorType, f.RealmID)
}

func (f MembershipFilter) matches(m Membership) bool {
	return (f.Subject == "" || m.Subject == f.Subject) && (f.State == "" || m.State == f.State) && (f.ActorType == "" || m.ActorType == f.ActorType) && (f.RealmID == "" || m.RealmID == f.RealmID)
}

// DirectoryFilter selects literal display spelling and current retained state.
type DirectoryFilter struct{ Name, State, Key string }

func directoryRequest(filters []DirectoryFilter, group bool) (AuthorizationPageRequest, error) {
	r := AuthorizationPageRequest{}
	if len(filters) > 1 {
		return r, E(CodeInvalidArgument, "one directory filter is permitted")
	}
	if len(filters) == 1 {
		r.Name, r.State, r.BuiltinKey = filters[0].Name, filters[0].State, filters[0].Key
	}
	if r.Name != "" {
		if err := validateDisplay(r.Name, ""); err != nil {
			return r, err
		}
	}
	if r.State != "" && r.State != "active" && r.State != "archived" && !(group && r.State == "suspended") {
		return r, E(CodeInvalidArgument, "directory state is invalid")
	}
	if r.BuiltinKey != "" {
		if _, ok := BuiltinPermissions(r.BuiltinKey); group || !ok {
			return r, E(CodeInvalidArgument, "built-in role filter is invalid")
		}
	}
	return r, nil
}
