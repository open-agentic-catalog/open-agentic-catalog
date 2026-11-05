package runtime

import (
	"reflect"
	"testing"
)

func TestConfiguredJobCollectionTargetsPreserveTenantAndHostSelection(t *testing.T) {
	for _, test := range []struct {
		name        string
		bindings    map[string]publicationBinding
		profile     string
		hosts       []string
		defaultHost string
	}{
		{name: "disabled optional targets"},
		{name: "single own host", bindings: map[string]publicationBinding{"one.example": {tenantID: "tenant"}}, hosts: []string{"one.example"}, defaultHost: "one.example"},
		{name: "foreign host", bindings: map[string]publicationBinding{"foreign.example": {tenantID: "other"}}},
		{name: "multi tenant explicit host", bindings: map[string]publicationBinding{"foreign.example": {tenantID: "other"}, "one.example": {tenantID: "tenant"}}, hosts: []string{"one.example"}},
		{name: "multiple own hosts and semantic", bindings: map[string]publicationBinding{"z.example": {tenantID: "tenant"}, "a.example": {tenantID: "tenant"}}, profile: "selected-profile", hosts: []string{"a.example", "z.example"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := configuredJobCollectionTargets("tenant", test.bindings, test.profile)
			if !got.Enforced || !reflect.DeepEqual(got.PublicationHosts, test.hosts) || got.DefaultPublicationHost != test.defaultHost || got.SemanticProfile != test.profile {
				t.Fatal("deployment inputs expanded targets", got)
			}
			if len(got.PublicationHosts) > 0 {
				got.PublicationHosts[0] = "mutated"
				again := configuredJobCollectionTargets("tenant", test.bindings, test.profile)
				if !reflect.DeepEqual(again.PublicationHosts, test.hosts) {
					t.Fatal("mutable selection altered configuration")
				}
			}
		})
	}
}
