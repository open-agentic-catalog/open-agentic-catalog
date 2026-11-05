package config

import (
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestDevelopmentTemplateAndPresentationValidation(t *testing.T) {
	valid := DevelopmentUser{Subject: "exact", TenantID: "00000000-0000-7000-8000-000000000001", Label: "Reader", Description: "Read local resources", RoleTemplates: []string{"reader"}}
	for name, mutate := range map[string]func(*DevelopmentUser){
		"valid":              func(*DevelopmentUser) {},
		"empty presentation": func(u *DevelopmentUser) { u.Label = ""; u.Description = "" },
		"all templates": func(u *DevelopmentUser) {
			u.RoleTemplates = []string{"reader", "contributor", "reviewer", "publisher", "discovery_reader", "publisher_creator", "publication_admin", "source_admin", "security_reviewer", "webhook_admin", "feedback_member", "library_member", "tenant_admin", "service_client"}
		},
		"both":                 func(u *DevelopmentUser) { u.Roles = []identity.Role{identity.RoleReader} },
		"unknown":              func(u *DevelopmentUser) { u.RoleTemplates = []string{"unknown"} },
		"repeat":               func(u *DevelopmentUser) { u.RoleTemplates = []string{"reader", "reader"} },
		"too many":             func(u *DevelopmentUser) { u.RoleTemplates = make([]string, 15) },
		"label too long":       func(u *DevelopmentUser) { u.Label = strings.Repeat("x", 101) },
		"description too long": func(u *DevelopmentUser) { u.Description = strings.Repeat("x", 301) },
		"maximum UTF8 presentation": func(u *DevelopmentUser) {
			u.Label = strings.Repeat("🙂", 25)
			u.Description = strings.Repeat("🙂", 75)
		},
		"UTF8 label byte overflow": func(u *DevelopmentUser) {
			u.Label = strings.Repeat("🙂", 25) + "a"
		},
		"UTF8 description byte overflow": func(u *DevelopmentUser) {
			u.Description = strings.Repeat("🙂", 75) + "a"
		},
		"label whitespace":       func(u *DevelopmentUser) { u.Label = " Reader" },
		"description whitespace": func(u *DevelopmentUser) { u.Description = "desc " },
		"label control":          func(u *DevelopmentUser) { u.Label = "reader\u0007" },
		"description control":    func(u *DevelopmentUser) { u.Description = "line\ncontrol" },
	} {
		t.Run(name, func(t *testing.T) {
			u := valid
			mutate(&u)
			cfg := validTestConfig()
			cfg.Auth.DevelopmentTokens = map[string]DevelopmentUser{"exact": u}
			err := cfg.Validate()
			wantValid := name == "valid" || name == "all templates" || name == "empty presentation" || name == "maximum UTF8 presentation"
			if (err == nil) != wantValid {
				t.Fatalf("validation %s=%v", name, err)
			}
		})
	}
}

func TestDevelopmentAliasesRejectActorTypeConflictsWithoutDisclosingTokens(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		a := DevelopmentUser{TenantID: "aaaaaaaa-0000-7000-8000-000000000001", Subject: "same", RoleTemplates: []string{"reader"}}
		b := a
		b.TenantID, b.RoleTemplates = strings.ToUpper(a.TenantID), []string{"service_client"}
		if reverse {
			a, b = b, a
		}
		cfg := validTestConfig()
		cfg.Auth.DevelopmentTokens = map[string]DevelopmentUser{"private-alias-a": a, "private-alias-b": b}
		if err := cfg.validateAuth(); err == nil || !strings.Contains(err.Error(), "conflicting actor types") || strings.Contains(err.Error(), "private-alias") {
			t.Fatal("invalid alias declarations accepted or disclosed", err)
		}
		b.RoleTemplates = []string{"library_member"}
		a.RoleTemplates = []string{"reader"}
		cfg.Auth.DevelopmentTokens = map[string]DevelopmentUser{"private-alias-a": a, "private-alias-b": b}
		if err := cfg.validateAuth(); err != nil {
			t.Fatal("consistent aliases refused", err)
		}
	}
}
