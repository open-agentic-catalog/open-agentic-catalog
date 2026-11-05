package config

import (
	"slices"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestDevelopmentIdentityTypeIsExplicitAndIndependentFromEmptyTemplate(t *testing.T) {
	for _, test := range []struct {
		user DevelopmentUser
		want identity.ActorType
	}{
		{DevelopmentUser{}, identity.ActorHuman},
		{DevelopmentUser{Roles: []identity.Role{identity.RoleServiceClient}}, identity.ActorService},
		{DevelopmentUser{RoleTemplates: []string{"service_client"}}, identity.ActorService},
		{DevelopmentUser{ActorType: identity.ActorHuman, RoleTemplates: []string{"service_client"}}, identity.ActorHuman},
		{DevelopmentUser{ActorType: identity.ActorService, RoleTemplates: []string{"reader"}}, identity.ActorService},
	} {
		if got := test.user.IdentityType(); got != test.want {
			t.Fatal(got, test.want)
		}
	}
	cfg := validTestConfig()
	cfg.Auth.DevelopmentTokens = map[string]DevelopmentUser{"synthetic-human": {TenantID: "00000000-0000-7000-8000-000000000001", Subject: "human", ActorType: identity.ActorHuman, RoleTemplates: []string{"service_client"}}}
	if err := cfg.validateAuth(); err != nil {
		t.Fatal(err)
	}
	u := cfg.Auth.DevelopmentTokens["synthetic-human"]
	u.ActorType = "unknown"
	cfg.Auth.DevelopmentTokens["synthetic-human"] = u
	if err := cfg.validateAuth(); err == nil {
		t.Fatal("unknown explicit type accepted")
	}
	empty := DevelopmentUser{}
	if templates := empty.SeedRoleTemplates(); len(templates) != 1 || templates[0] != "service_client" || empty.IdentityType() != identity.ActorHuman {
		t.Fatal("empty development declaration lost Human membership-only semantics", templates)
	}
	scoped := DevelopmentUser{Namespaces: map[string][]identity.Role{"00000000-0000-7000-8000-000000000010": {identity.RoleReader}}}
	if len(scoped.SeedRoleTemplates()) != 0 {
		t.Fatal("scoped-only declaration gained a global assignment")
	}
	configured := DevelopmentUser{RoleTemplates: []string{"library_member"}}
	templates := configured.SeedRoleTemplates()
	templates[0] = "reader"
	if configured.RoleTemplates[0] != "library_member" {
		t.Fatal("seed templates alias configuration")
	}
}

func TestDevelopmentPresentationNeverChangesIdentityOrGrants(t *testing.T) {
	for _, group := range []string{"", "bundle", "template", "administrator", "<script>"} {
		t.Run(group, func(t *testing.T) {
			cfg := validTestConfig()
			u := DevelopmentUser{Subject: "membership-only", TenantID: "00000000-0000-7000-8000-000000000001", ActorType: identity.ActorHuman, ProfileGroup: group, Elevated: true, SeparateTenant: true}
			cfg.Auth.DevelopmentTokens = map[string]DevelopmentUser{"synthetic-token": u}
			err := cfg.validateAuth()
			valid := group == "" || group == "bundle" || group == "template"
			if valid && err != nil || !valid && err == nil {
				t.Fatal("profile group validation", err)
			}
			if !valid && err.Error() != "config: development identity profile group is invalid" {
				t.Fatal("presentation refusal disclosed an untrusted value", err)
			}
			if u.IdentityType() != identity.ActorHuman || !slices.Equal(u.SeedRoleTemplates(), []string{"service_client"}) || len(u.Roles) != 0 {
				t.Fatal("presentation metadata manufactured authority or changed identity")
			}
		})
	}
}
