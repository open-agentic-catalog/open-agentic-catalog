// Development seeds normalize aliases before either store constructs policy.
package identity

import (
	"cmp"
	"slices"
	"strings"
)

// BootstrapCompanions returns only explicit operator/development bundle grants.
// Ordinary assignment and permission evaluation never expand a role this way.
func BootstrapCompanions(role Role) []string {
	switch role {
	case RoleReader:
		return []string{"discovery_reader", "feedback_member"}
	case RoleContributor:
		return []string{"discovery_reader", "feedback_member", "publisher_creator"}
	case RolePublisher:
		return []string{"discovery_reader", "feedback_member", "publication_admin"}
	case RoleReviewer, RoleSourceAdmin, RoleSecurityReviewer, RoleTenantAdmin, RoleServiceClient:
		return nil
	default:
		return nil
	}
}

// NormalizeDevelopmentSeeds validates every declaration before returning any
// usable seeds. Aliases of one tenant/realm/subject must agree on actor type and
// share a deduplicated grant union, including explicit bootstrap companions.
// Inputs are not modified and returned slices/maps do not alias them.
func NormalizeDevelopmentSeeds(seeds []Seed) ([]Seed, error) {
	type principalKey struct{ tenant, issuer, organization, subject string }
	type combined struct {
		seed   Seed
		count  int
		global map[string]bool
		scoped map[string][]Role
	}
	groups := map[principalKey]*combined{}
	sharedRealms := map[[2]string]string{}
	for _, seed := range seeds {
		if !ValidTenantID(seed.TenantID) || seed.Issuer == "" || strings.TrimSpace(seed.Issuer) != seed.Issuer || len(seed.Issuer) > 2048 || !ValidSubject(seed.Subject) || seed.ExternalOrganization != "" && !ValidSubject(seed.ExternalOrganization) || !ValidActorType(seed.ActorType) || len(seed.RoleTemplates) > MaxDevelopmentRoleTemplates || len(seed.Roles) > 0 && len(seed.RoleTemplates) > 0 {
			return nil, E(CodeInvalidArgument, "development identity seed is invalid")
		}
		seed.TenantID = strings.ToLower(seed.TenantID)
		if seed.ExternalOrganization != "" {
			mapping := [2]string{seed.Issuer, seed.ExternalOrganization}
			if tenant, exists := sharedRealms[mapping]; exists && tenant != seed.TenantID {
				return nil, E(CodeInvalidArgument, "development issuer organization maps to multiple tenants")
			}
			sharedRealms[mapping] = seed.TenantID
		}
		key := principalKey{seed.TenantID, seed.Issuer, seed.ExternalOrganization, seed.Subject}
		group := groups[key]
		if group == nil {
			group = &combined{seed: seed, global: map[string]bool{}, scoped: map[string][]Role{}}
			groups[key] = group
		} else if group.seed.ActorType != seed.ActorType {
			return nil, E(CodeInvalidArgument, "development aliases for one principal have conflicting actor types")
		}
		group.count++
		for _, role := range seed.Roles {
			if err := validateRoleScope(role, ""); err != nil {
				return nil, err
			}
			group.global[string(role)] = true
			for _, key := range BootstrapCompanions(role) {
				group.global[key] = true
			}
		}
		seen := map[string]bool{}
		for _, key := range seed.RoleTemplates {
			if _, ok := BuiltinPermissions(key); !ok || seen[key] {
				return nil, E(CodeInvalidArgument, "development role template is invalid or repeated")
			}
			seen[key], group.global[key] = true, true
		}
		for namespace, roles := range seed.Namespaces {
			if !ValidTenantID(namespace) || len(roles) == 0 {
				return nil, E(CodeInvalidArgument, "development namespace assignment is invalid")
			}
			namespace = strings.ToLower(namespace)
			for _, role := range roles {
				if err := validateRoleScope(role, namespace); err != nil {
					return nil, err
				}
			}
			group.scoped[namespace] = append(group.scoped[namespace], roles...)
		}
	}
	var result []Seed
	for _, group := range groups {
		seed := group.seed
		seed.Roles, seed.RoleTemplates = slices.Clone(seed.Roles), slices.Clone(seed.RoleTemplates)
		if group.count > 1 {
			seed.Roles, seed.RoleTemplates = nil, nil
			for key := range group.global {
				seed.RoleTemplates = append(seed.RoleTemplates, key)
			}
			slices.Sort(seed.RoleTemplates)
		}
		seed.Namespaces = nil
		if len(group.scoped) > 0 {
			seed.Namespaces = map[string][]Role{}
		}
		for namespace, roles := range group.scoped {
			slices.Sort(roles)
			seed.Namespaces[namespace] = slices.Compact(roles)
		}
		result = append(result, seed)
	}
	slices.SortFunc(result, func(a, b Seed) int {
		return cmp.Or(cmp.Compare(a.TenantID, b.TenantID), cmp.Compare(a.Issuer, b.Issuer), cmp.Compare(a.ExternalOrganization, b.ExternalOrganization), cmp.Compare(a.Subject, b.Subject))
	})
	return result, nil
}
