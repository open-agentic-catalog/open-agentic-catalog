package config

import "testing"

func TestOwnedLoopbackPolicyRequiresExactTestHostAndExistingAuthority(t *testing.T) {
	base := FederationPolicy{SchemaVersion: 1, PolicyID: "owned-fixtures", Revision: 1, Public: FederationPublicPolicy{Mode: "disabled"}, PrivateProfiles: []FederationPrivateProfile{{Name: "owned-fixture", Origins: []string{"https://127.0.0.1:19443"}, AddressRanges: []string{"127.0.0.1/32"}, AssignedTenantIDs: []string{policyTestTenant}, TransportRef: "direct"}}}
	for _, environment := range []string{"test", "development", "production"} {
		for _, prefix := range []string{"127.0.0.1/32", "127.0.0.2/32", "::1/128", "127.0.0.0/8", "127.0.0.0/24", "::/127", "169.254.169.254/32", "0.0.0.0/0", "::ffff:127.0.0.1/128"} {
			t.Run(environment+"/"+prefix, func(t *testing.T) {
				policy := base
				policy.PrivateProfiles = append([]FederationPrivateProfile(nil), base.PrivateProfiles...)
				policy.PrivateProfiles[0].AddressRanges = []string{prefix}
				got, err := normalizeFederationPolicy(policy, environment)
				want := environment == "test" && (prefix == "127.0.0.1/32" || prefix == "127.0.0.2/32" || prefix == "::1/128")
				if (err == nil) != want {
					t.Fatalf("admission=%v want=%v: %v", err == nil, want, err)
				}
				if want && (len(got.PrivateProfiles[0].AssignedTenantIDs) != 1 || got.PrivateProfiles[0].AssignedTenantIDs[0] != policyTestTenant || len(got.PrivateProfiles[0].Origins) != 1) {
					t.Fatal("test route lost explicit authority")
				}
			})
		}
	}
	policy := base
	policy.PrivateProfiles = append([]FederationPrivateProfile(nil), base.PrivateProfiles...)
	policy.PrivateProfiles[0].AssignedTenantIDs = nil
	if _, err := normalizeFederationPolicy(policy, "test"); err == nil {
		t.Fatal("test route omitted tenant admission")
	}
	policy = base
	policy.PrivateProfiles = append([]FederationPrivateProfile(nil), base.PrivateProfiles...)
	policy.PrivateProfiles[0].Origins = nil
	if _, err := normalizeFederationPolicy(policy, "test"); err == nil {
		t.Fatal("test route omitted exact origin admission")
	}
}
