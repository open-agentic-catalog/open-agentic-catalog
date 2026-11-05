package config

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Copies include nested slices: normalization may reorder private profiles.
func propertyPolicyCopy(policy FederationPolicy) FederationPolicy {
	policy.Public.Origins = slices.Clone(policy.Public.Origins)
	policy.AdditionalDeniedOrigins = slices.Clone(policy.AdditionalDeniedOrigins)
	policy.AdditionalDeniedAddressRanges = slices.Clone(policy.AdditionalDeniedAddressRanges)
	policy.PrivateProfiles = slices.Clone(policy.PrivateProfiles)
	for i := range policy.PrivateProfiles {
		p := &policy.PrivateProfiles[i]
		p.Origins = slices.Clone(p.Origins)
		p.AddressRanges = slices.Clone(p.AddressRanges)
		p.AssignedTenantIDs = slices.Clone(p.AssignedTenantIDs)
	}
	return policy
}

func propertyPolicyOrigin(t *rapid.T, origin string) string {
	// All generated origins use their scheme's default port. Casing, an
	// explicit/implicit default port and a root slash are
	// documented equivalent spellings, not additional route authority.
	if rapid.Bool().Draw(t, "implicit port") {
		origin = strings.TrimSuffix(strings.TrimSuffix(origin, ":443"), ":80")
	}
	if rapid.Bool().Draw(t, "origin casing") {
		origin = strings.ToUpper(origin)
	}
	if rapid.Bool().Draw(t, "root slash") {
		origin += "/"
	}
	return origin
}

func propertyPolicyOrigins(t *rapid.T, origins []string) []string {
	values := slices.Clone(origins)
	for i := range values {
		values[i] = propertyPolicyOrigin(t, values[i])
	}
	return rapid.Permutation(values).Draw(t, "origin order")
}

func TestPropertyFederationPolicyNormalization(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		environment := rapid.SampledFrom([]string{"production", "development"}).Draw(t, "environment")
		// Construct the canonical model directly in lexical order, without using
		// the implementation's sorting, URL or CIDR normalization helpers.
		want := FederationPolicy{
			SchemaVersion: 1, PolicyID: "generated-policy", Revision: int64(rapid.IntRange(1, 1000).Draw(t, "revision")),
			Public:                  FederationPublicPolicy{Mode: "allowlist", Origins: []string{}},
			AdditionalDeniedOrigins: []string{}, AdditionalDeniedAddressRanges: []string{},
		}
		for i := range rapid.IntRange(0, 4).Draw(t, "public count") {
			want.Public.Origins = append(want.Public.Origins, fmt.Sprintf("https://public-%02d.example:443", i))
		}
		for i := range rapid.IntRange(0, 3).Draw(t, "denied origin count") {
			want.AdditionalDeniedOrigins = append(want.AdditionalDeniedOrigins, fmt.Sprintf("https://denied-%02d.example:443", i))
		}
		for i := range rapid.IntRange(0, 3).Draw(t, "denied range count") {
			want.AdditionalDeniedAddressRanges = append(want.AdditionalDeniedAddressRanges, fmt.Sprintf("10.%d.0.0/16", i))
		}
		for i := range rapid.IntRange(0, 3).Draw(t, "private profile count") {
			p := FederationPrivateProfile{Name: fmt.Sprintf("private-%02d", i), TransportRef: "direct"}
			scheme, port := "https", 443
			if environment == "development" && rapid.Bool().Draw(t, "private HTTP") {
				scheme, port = "http", 80
			}
			for j := range rapid.IntRange(1, 3).Draw(t, "private origin count") {
				p.Origins = append(p.Origins, fmt.Sprintf("%s://private-%02d-%02d.example:%d", scheme, i, j, port))
			}
			for j := range rapid.IntRange(1, 3).Draw(t, "private range count") {
				p.AddressRanges = append(p.AddressRanges, fmt.Sprintf("172.16.%d.0/24", i*3+j))
			}
			// Include IPv6 unique-local space as well as RFC 1918 subnets.
			if rapid.Bool().Draw(t, "private IPv6") {
				p.AddressRanges = append(p.AddressRanges, fmt.Sprintf("fd00:%x::/48", i+1))
			}
			for j := range rapid.IntRange(1, 3).Draw(t, "tenant count") {
				p.AssignedTenantIDs = append(p.AssignedTenantIDs, fmt.Sprintf("abcdefab-0000-7000-8000-%012x", j+1))
			}
			want.PrivateProfiles = append(want.PrivateProfiles, p)
		}
		input := propertyPolicyCopy(want)
		input.Public.Origins = propertyPolicyOrigins(t, input.Public.Origins)
		input.AdditionalDeniedOrigins = propertyPolicyOrigins(t, input.AdditionalDeniedOrigins)
		input.AdditionalDeniedAddressRanges = rapid.Permutation(input.AdditionalDeniedAddressRanges).Draw(t, "denied range order")
		for i := range input.PrivateProfiles {
			p := &input.PrivateProfiles[i]
			p.Origins = propertyPolicyOrigins(t, p.Origins)
			p.AddressRanges = rapid.Permutation(p.AddressRanges).Draw(t, "private range order")
			p.AssignedTenantIDs = rapid.Permutation(p.AssignedTenantIDs).Draw(t, "tenant order")
		}
		input.PrivateProfiles = rapid.Permutation(input.PrivateProfiles).Draw(t, "profile order")
		got, err := normalizeFederationPolicy(input, environment)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("normalization: got %+v, error %v; want %+v", got, err, want)
		}
		again, err := normalizeFederationPolicy(propertyPolicyCopy(got), environment)
		if err != nil || !reflect.DeepEqual(again, want) {
			t.Fatalf("normalization is not idempotent: %+v, %v", again, err)
		}
		reject := func(label string, bad FederationPolicy) {
			if _, err := normalizeFederationPolicy(bad, environment); err == nil {
				t.Fatalf("accepted duplicate %s", label)
			}
		}
		if len(want.Public.Origins) > 0 {
			bad := propertyPolicyCopy(want)
			bad.Public.Origins = append(bad.Public.Origins, propertyPolicyOrigin(t, want.Public.Origins[0]))
			reject("public origin", bad)
		}
		if len(want.AdditionalDeniedOrigins) > 0 {
			bad := propertyPolicyCopy(want)
			bad.AdditionalDeniedOrigins = append(bad.AdditionalDeniedOrigins, propertyPolicyOrigin(t, want.AdditionalDeniedOrigins[0]))
			reject("denied origin", bad)
		}
		if len(want.AdditionalDeniedAddressRanges) > 0 {
			bad := propertyPolicyCopy(want)
			bad.AdditionalDeniedAddressRanges = append(bad.AdditionalDeniedAddressRanges, " "+want.AdditionalDeniedAddressRanges[0]+" ")
			reject("denied range", bad)
		}
		if len(want.PrivateProfiles) > 0 {
			for _, field := range []string{"profile", "origin", "range", "tenant"} {
				bad := propertyPolicyCopy(want)
				p := &bad.PrivateProfiles[0]
				switch field {
				case "profile":
					bad.PrivateProfiles = append(bad.PrivateProfiles, *p)
				case "origin":
					p.Origins = append(p.Origins, propertyPolicyOrigin(t, p.Origins[0]))
				case "range":
					p.AddressRanges = append(p.AddressRanges, " "+p.AddressRanges[0]+" ")
				case "tenant":
					p.AssignedTenantIDs = append(p.AssignedTenantIDs, p.AssignedTenantIDs[0])
				}
				reject("private "+field, bad)
			}
		}
	})
}
