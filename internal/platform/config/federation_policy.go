// Federation policy loading bounds deployment files, normalizes tenant/origin
// routes and binds private trust to an exact policy digest.

package config

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const (
	maxFederationPolicyBytes              = 1 << 20
	maxFederationTrustBundleBytes         = 4 << 20
	maxFederationCombinedTrustBundleBytes = 16 << 20
)

var privateAddressRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

var errFederationMountedFileTooLarge = safeerr.New("mounted federation file exceeds its size limit")

// loadFederationPolicy resolves mounted policy and trust inputs into normalized
// in-memory bytes, then binds the complete effective policy to one digest.
func loadFederationPolicy(cfg *Config) error {
	policy := cfg.Federation.Policy
	if cfg.Federation.PolicyFile != "" {
		if cfg.Environment == "production" && (!filepath.IsAbs(cfg.Federation.PolicyFile) || filepath.Clean(cfg.Federation.PolicyFile) != cfg.Federation.PolicyFile) {
			return safeerr.New("config: production federation.policyFile must be an absolute clean path")
		}
		raw, err := readBoundedPolicy(cfg.Federation.PolicyFile)
		if err != nil {
			return fmt.Errorf("config: read federation policy: %w", err)
		}
		if err := jsondoc.Validate(raw); err != nil {
			return fmt.Errorf("config: decode federation policy: %w", err)
		}
		var document any
		if err := json.Unmarshal(raw, &document); err != nil || containsNull(document) {
			return safeerr.New("config: federation policy contains invalid or null values")
		}
		if err := jsondoc.Decode(raw, &policy); err != nil {
			return fmt.Errorf("config: decode federation policy: %w", err)
		}
	}
	normalized, err := normalizeFederationPolicy(policy, cfg.Environment)
	if err != nil {
		return err
	}
	trustDigests, err := loadFederationTrustBundles(&normalized)
	if err != nil {
		return err
	}
	canonical, err := json.Marshal(struct {
		Policy       FederationPolicy    `json:"policy"`
		TrustBundles []trustBundleDigest `json:"trustBundles,omitempty"`
	}{Policy: normalized, TrustBundles: trustDigests})
	if err != nil {
		return fmt.Errorf("config: encode normalized federation policy: %w", err)
	}
	digest := sha256.Sum256(canonical)
	cfg.Federation.Policy = normalized
	cfg.Federation.PolicyDigest = "sha256:" + hex.EncodeToString(digest[:])
	return nil
}

// readBoundedPolicy enforces the policy-file ceiling and gives size failures a
// policy-specific diagnostic.
func readBoundedPolicy(filename string) ([]byte, error) {
	raw, err := readBoundedFederationFile(filename, maxFederationPolicyBytes)
	if err != nil {
		if errors.Is(err, errFederationMountedFileTooLarge) {
			return nil, safeerr.New("federation policy exceeds 1 MiB")
		}
		return nil, err
	}
	return raw, nil
}

// readBoundedFederationFile reads one byte beyond the configured ceiling to detect
// overflow and closes the selected deployment file.
func readBoundedFederationFile(filename string, maximum int64) ([]byte, error) {
	file, err := os.Open(filename) // #nosec G304 -- the operator explicitly selects the policy path.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximum {
		return nil, errFederationMountedFileTooLarge
	}
	return raw, nil
}

type trustBundleDigest struct {
	Profile string `json:"profile"`
	Digest  string `json:"digest"`
}

// loadFederationTrustBundles copies certificate-bearing PEM into private profiles
// and returns exact byte digests, enforcing per-file and combined limits.
func loadFederationTrustBundles(policy *FederationPolicy) ([]trustBundleDigest, error) {
	digests := make([]trustBundleDigest, 0)
	total := 0
	for index := range policy.PrivateProfiles {
		profile := &policy.PrivateProfiles[index]
		profile.TrustBundlePEM = nil
		if profile.TrustBundleFile == "" {
			continue
		}
		raw, err := readBoundedFederationFile(profile.TrustBundleFile, maxFederationTrustBundleBytes)
		if err != nil {
			if errors.Is(err, errFederationMountedFileTooLarge) {
				return nil, fmt.Errorf("config: federation trust bundle for profile %q exceeds 4 MiB", profile.Name)
			}
			return nil, fmt.Errorf("config: read federation trust bundle for profile %q: %w", profile.Name, err)
		}
		total += len(raw)
		if total > maxFederationCombinedTrustBundleBytes {
			return nil, safeerr.New("config: federation trust bundles exceed the 16 MiB combined limit")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("config: federation trust bundle for profile %q contains no certificates", profile.Name)
		}
		profile.TrustBundlePEM = append([]byte(nil), raw...)
		digest := sha256.Sum256(raw)
		digests = append(digests, trustBundleDigest{Profile: profile.Name, Digest: "sha256:" + hex.EncodeToString(digest[:])})
	}
	return digests, nil
}

// normalizeFederationPolicy closes and sorts the egress topology so equivalent
// operator input produces the same policy digest and routing behavior.
func normalizeFederationPolicy(policy FederationPolicy, environment string) (FederationPolicy, error) {
	if policy.SchemaVersion != 1 || !validFederationName(policy.PolicyID) || policy.Revision <= 0 {
		return FederationPolicy{}, safeerr.New("config: federation policy identity is invalid")
	}
	switch policy.Public.Mode {
	case fetcher.PublicDisabled:
		if len(policy.Public.Origins) != 0 {
			return FederationPolicy{}, safeerr.New("config: disabled federation public policy cannot contain origins")
		}
	case fetcher.PublicAllowlist:
		origins, err := normalizePolicyOrigins(policy.Public.Origins, false, 256)
		if err != nil {
			return FederationPolicy{}, fmt.Errorf("config: federation public origins: %w", err)
		}
		policy.Public.Origins = origins
	case fetcher.PublicAnyPublicHTTPS:
		if len(policy.Public.Origins) != 0 {
			return FederationPolicy{}, safeerr.New("config: any-public federation policy cannot contain an origin allowlist")
		}
	default:
		return FederationPolicy{}, safeerr.New("config: federation public mode must be disabled, allowlist, or any_public_https")
	}
	allowHTTP := environment != "production"
	deniedOrigins, err := normalizePolicyOrigins(policy.AdditionalDeniedOrigins, allowHTTP, 256)
	if err != nil {
		return FederationPolicy{}, fmt.Errorf("config: federation denied origins: %w", err)
	}
	policy.AdditionalDeniedOrigins = deniedOrigins
	deniedRanges, err := normalizeAddressRanges(policy.AdditionalDeniedAddressRanges, false, false, 256)
	if err != nil {
		return FederationPolicy{}, fmt.Errorf("config: federation denied address ranges: %w", err)
	}
	policy.AdditionalDeniedAddressRanges = deniedRanges
	if len(policy.PrivateProfiles) > 32 {
		return FederationPolicy{}, safeerr.New("config: federation policy cannot contain more than 32 private profiles")
	}
	seenNames := make(map[string]struct{}, len(policy.PrivateProfiles))
	for index := range policy.PrivateProfiles {
		profile := &policy.PrivateProfiles[index]
		if !validFederationName(profile.Name) {
			return FederationPolicy{}, safeerr.New("config: federation private profile name must be a lower-case slug")
		}
		if _, exists := seenNames[profile.Name]; exists {
			return FederationPolicy{}, safeerr.New("config: federation private profile names must be unique")
		}
		seenNames[profile.Name] = struct{}{}
		profile.Origins, err = normalizePolicyOrigins(profile.Origins, allowHTTP, 64)
		if err != nil || len(profile.Origins) == 0 {
			return FederationPolicy{}, safeerr.New("config: each federation private profile requires valid exact origins")
		}
		profile.AddressRanges, err = normalizeAddressRanges(profile.AddressRanges, true, environment == "test", 64)
		if err != nil || len(profile.AddressRanges) == 0 {
			return FederationPolicy{}, safeerr.New("config: each federation private profile requires bounded private address ranges")
		}
		if len(profile.AssignedTenantIDs) == 0 || len(profile.AssignedTenantIDs) > 128 {
			return FederationPolicy{}, safeerr.New("config: each federation private profile requires assigned tenants")
		}
		assignedTenantIDs := uniqueSorted(profile.AssignedTenantIDs)
		if len(assignedTenantIDs) != len(profile.AssignedTenantIDs) {
			return FederationPolicy{}, safeerr.New("config: federation private profile tenant assignments must be unique")
		}
		profile.AssignedTenantIDs = assignedTenantIDs
		for _, tenantID := range profile.AssignedTenantIDs {
			if !identity.ValidTenantID(tenantID) {
				return FederationPolicy{}, safeerr.New("config: federation private profile contains an invalid tenant ID")
			}
		}
		if profile.TransportRef != "direct" {
			return FederationPolicy{}, safeerr.New("config: the initial federation private transportRef must be direct")
		}
		if profile.TrustBundleFile != "" && (!filepath.IsAbs(profile.TrustBundleFile) || filepath.Clean(profile.TrustBundleFile) != profile.TrustBundleFile) {
			return FederationPolicy{}, safeerr.New("config: federation trustBundleFile must be an absolute clean path")
		}
	}
	sort.Slice(policy.PrivateProfiles, func(i, j int) bool { return policy.PrivateProfiles[i].Name < policy.PrivateProfiles[j].Name })
	return policy, nil
}

// normalizePolicyOrigins bounds and sorts canonical origins, rejecting
// path/query/fragment components and normalized duplicates.
func normalizePolicyOrigins(values []string, allowHTTP bool, maximum int) ([]string, error) {
	if len(values) > maximum {
		return nil, safeerr.New("too many origins")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, safeerr.New("origins cannot contain a path, query, or fragment")
		}
		origin, err := fetcher.NormalizeOrigin(value, allowHTTP)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[origin]; exists {
			return nil, safeerr.New("origins must be unique after normalization")
		}
		seen[origin] = struct{}{}
		result = append(result, origin)
	}
	sort.Strings(result)
	return result, nil
}

// normalizeAddressRanges accepts bounded unique canonical non-default CIDRs and
// optionally requires complete private-range containment. Owned test profiles
// can additionally admit exact loopback host prefixes, never a loopback subnet.
func normalizeAddressRanges(values []string, requirePrivate, allowTestLoopback bool, maximum int) ([]string, error) {
	if len(values) > maximum {
		return nil, safeerr.New("too many address ranges")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil || prefix != prefix.Masked() || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return nil, safeerr.New("address ranges must be canonical non-default CIDR prefixes")
		}
		testLoopback := allowTestLoopback && prefix.Bits() == prefix.Addr().BitLen() && prefix.Addr().IsLoopback()
		if requirePrivate && !privatePrefixContained(prefix) && !testLoopback {
			return nil, safeerr.New("private profile address ranges must use private address space")
		}
		normalized := prefix.String()
		if _, exists := seen[normalized]; exists {
			return nil, safeerr.New("address ranges must be unique")
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result, nil
}

// validFederationName restricts policy and profile identifiers to the namespace
// grammar with a 63-byte ceiling.
func validFederationName(value string) bool {
	return len(value) <= 63 && validNamespace(value)
}

// privatePrefixContained checks the whole CIDR against reviewed private-address
// ranges, excluding wider prefixes that merely start inside them.
func privatePrefixContained(prefix netip.Prefix) bool {
	for _, privateRange := range privateAddressRanges {
		if prefix.Addr().BitLen() == privateRange.Addr().BitLen() && prefix.Bits() >= privateRange.Bits() && privateRange.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

// uniqueSorted copies and deduplicates assignments in lexical order without
// changing the caller's slice.
func uniqueSorted(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	write := 0
	for _, value := range result {
		if write > 0 && result[write-1] == value {
			continue
		}
		result[write] = value
		write++
	}
	return result[:write]
}
