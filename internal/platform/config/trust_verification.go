package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
)

// TrustVerification contains deployment-owned authority and acquisition bounds.
// It never supplies credentials or overrides the federation egress policy.
type TrustVerification struct {
	Enabled      bool           `json:"enabled"`
	Policies     []TrustPolicy  `json:"policies"`
	Timeout      Duration       `json:"timeout"`
	MaxRequests  int            `json:"maxRequests"`
	MaxStatusAge Duration       `json:"maxStatusAge"`
	FutureSkew   Duration       `json:"futureSkew"`
	Limits       EvidenceLimits `json:"limits"`
}

type EvidenceLimits struct {
	ReceiptBytes    int64 `json:"receiptBytes"`
	CheckpointBytes int64 `json:"checkpointBytes"`
	HistoryBytes    int64 `json:"historyBytes"`
	StatusBytes     int64 `json:"statusBytes"`
	BundleBytes     int64 `json:"bundleBytes"`
}

type TrustPolicy struct {
	Name                  string     `json:"name"`
	Profile               string     `json:"profile"`
	RegistrationAuthority string     `json:"registrationAuthority"`
	LogOrigin             string     `json:"logOrigin"`
	EvidenceOrigin        string     `json:"evidenceOrigin"`
	NetworkProfile        string     `json:"networkProfile,omitempty"`
	PublisherDomains      []string   `json:"publisherDomains"`
	RegistrationKeys      []TrustKey `json:"registrationKeys"`
	LogKeyFiles           []string   `json:"logKeyFiles"`
	verifier              *anstrust.Verifier
}

type TrustKey struct {
	ID   string `json:"id"`
	File string `json:"file"`
}

func TrustVerificationDefaults() TrustVerification {
	return TrustVerification{Policies: []TrustPolicy{}, Timeout: Duration{15 * time.Second}, MaxRequests: 4,
		MaxStatusAge: Duration{time.Hour}, FutureSkew: Duration{time.Minute},
		Limits: EvidenceLimits{anstrust.MaxReceiptBytes, anstrust.MaxCheckpointBytes, anstrust.MaxHistoryBytes, anstrust.MaxStatusBytes, anstrust.MaxBundleBytes}}
}

func (c TrustVerification) authority(p TrustPolicy) anstrust.Policy {
	return anstrust.Policy{Profile: p.Profile, RegistrationAuthority: p.RegistrationAuthority, LogOrigin: p.LogOrigin,
		EvidenceOrigin: p.EvidenceOrigin, PublisherDomains: p.PublisherDomains, MaxStatusAge: c.MaxStatusAge.Duration, FutureSkew: c.FutureSkew.Duration}
}

// Validate performs no I/O, including when configured policies are disabled.
// Migration owners do not consume this capability or its key files.
func (c TrustVerification) Validate(egress FederationPolicy, role Role, lease time.Duration) error {
	if c.Timeout.Duration < time.Second || c.Timeout.Duration > time.Minute || c.MaxRequests < 1 || c.MaxRequests > 4 ||
		c.MaxStatusAge.Duration < time.Second || c.MaxStatusAge.Duration > 24*time.Hour || c.FutureSkew.Duration < 0 || c.FutureSkew.Duration > time.Minute || len(c.Policies) > 16 {
		return safeerr.New("config: trustVerification timing, request or policy bounds are invalid")
	}
	for _, limit := range []struct{ value, maximum int64 }{
		{c.Limits.ReceiptBytes, anstrust.MaxReceiptBytes}, {c.Limits.CheckpointBytes, anstrust.MaxCheckpointBytes},
		{c.Limits.HistoryBytes, anstrust.MaxHistoryBytes}, {c.Limits.StatusBytes, anstrust.MaxStatusBytes}, {c.Limits.BundleBytes, anstrust.MaxBundleBytes},
	} {
		if limit.value < 1024 || limit.value > limit.maximum {
			return safeerr.New("config: trustVerification byte limits exceed supported bounds")
		}
	}
	if c.Enabled && (len(c.Policies) == 0 || ((role == RoleWorker || role == RoleAll) && c.Timeout.Duration+5*time.Second > lease)) {
		return safeerr.New("config: enabled trustVerification requires policies and five seconds of worker lease margin")
	}
	seen := make(map[string]bool)
	for _, p := range c.Policies {
		if !validFederationName(p.Name) || seen[p.Name] || anstrust.ValidateAuthority(c.authority(p)) != nil || len(p.RegistrationKeys) == 0 || len(p.RegistrationKeys) > 8 || len(p.LogKeyFiles) == 0 || len(p.LogKeyFiles) > 8 {
			return safeerr.New("config: trustVerification policy names, profile or authority inputs are invalid")
		}
		seen[p.Name] = true
		keys := make(map[string]bool)
		paths := append([]string(nil), p.LogKeyFiles...)
		for _, k := range p.RegistrationKeys {
			if !validClaimName(k.ID) || strings.Contains(k.ID, ":") || keys[k.ID] {
				return safeerr.New("config: trustVerification registration key IDs must be bounded and unique")
			}
			keys[k.ID] = true
			paths = append(paths, k.File)
		}
		for _, path := range paths {
			if len(path) > 4096 || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexFunc(path, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
				return safeerr.New("config: trustVerification key files require clean absolute paths")
			}
		}
		if c.Enabled {
			// Reuse the same exact-origin/private-route validation as artwork.
			binding := ListingMediaDefaults()
			binding.AllowedOrigins = []MediaOrigin{{Origin: p.EvidenceOrigin, NetworkProfile: p.NetworkProfile}}
			if binding.Validate(egress) != nil {
				return safeerr.New("config: trustVerification evidence origin is not admitted by the selected egress route")
			}
		}
	}
	return nil
}

// loadTrustVerification loads only bounded public keys, never network evidence.
// ConfigMap projection symlinks are allowed; the opened target must be regular.
func loadTrustVerification(c *Config) error {
	for i := range c.TrustVerification.Policies {
		c.TrustVerification.Policies[i].verifier = nil
	}
	if !c.TrustVerification.Enabled {
		return nil
	}
	for i, p := range c.TrustVerification.Policies {
		authority := c.TrustVerification.authority(p)
		authority.RegistrationKeys = make(map[string][]byte)
		for _, k := range p.RegistrationKeys {
			raw, err := readVerificationKey(k.File)
			if err != nil {
				return err
			}
			authority.RegistrationKeys[k.ID] = raw
		}
		for _, path := range p.LogKeyFiles {
			raw, err := readVerificationKey(path)
			if err != nil {
				return err
			}
			authority.LogKeys = append(authority.LogKeys, raw)
		}
		v, err := anstrust.New(authority)
		if err != nil {
			return safeerr.New("config: invalid trustVerification public key or authority policy")
		}
		c.TrustVerification.Policies[i].verifier = v
	}
	return nil
}

func readVerificationKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, safeerr.New("config: cannot read bounded regular trustVerification public key")
	}
	f, err := os.Open(path) // #nosec G304 -- operator-selected public key; regular target and byte bounds are checked.
	if err != nil {
		return nil, safeerr.New("config: cannot read bounded regular trustVerification public key")
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, safeerr.New("config: cannot read bounded regular trustVerification public key")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return nil, safeerr.New("config: cannot read bounded regular trustVerification public key")
	}
	return raw, nil
}

// Policy resolves a startup-loaded authority. Disabled and unknown names fail
// closed without reading files or contacting an authority.
func (c TrustVerification) Policy(name string) (TrustPolicy, *anstrust.Verifier, bool) {
	if c.Enabled {
		for _, p := range c.Policies {
			if p.Name == name && p.verifier != nil {
				return p, p.verifier, true
			}
		}
	}
	return TrustPolicy{}, nil, false
}

func applyTrustVerification(c *Config, raw string) error {
	var value any
	if len(raw) > 64<<10 || !strings.HasPrefix(strings.TrimSpace(raw), "{") || jsondoc.Validate([]byte(raw)) != nil || json.Unmarshal([]byte(raw), &value) != nil || containsNull(value) || jsondoc.Decode([]byte(raw), &c.TrustVerification) != nil {
		return safeerr.New("config: trustVerification override must be bounded closed JSON without null or duplicate fields")
	}
	return nil
}

// TrustVerificationReport omits policy names, domains, origins, files and keys.
type TrustVerificationReport struct {
	Enabled      bool           `json:"enabled"`
	PolicyCount  int            `json:"policyCount"`
	Timeout      Duration       `json:"timeout"`
	MaxRequests  int            `json:"maxRequests"`
	Limits       EvidenceLimits `json:"limits"`
	MaxStatusAge Duration       `json:"maxStatusAge"`
	FutureSkew   Duration       `json:"futureSkew"`
	PolicyDigest string         `json:"policyDigest,omitempty"`
}

func (c TrustVerification) Report() TrustVerificationReport {
	return TrustVerificationReport{Enabled: c.Enabled, PolicyCount: len(c.Policies), Timeout: c.Timeout, MaxRequests: c.MaxRequests, Limits: c.Limits, MaxStatusAge: c.MaxStatusAge, FutureSkew: c.FutureSkew}
}

// PolicyDigest binds loaded keys, named selections, routes and operational limits
// to the underlying egress generation. Disabled settings have no active digest.
func (c TrustVerification) PolicyDigest(egressDigest string) string {
	if !c.Enabled {
		return ""
	}
	var policies []string
	for _, p := range c.Policies {
		if p.verifier == nil {
			return ""
		}
		policies = append(policies, p.Name+":"+p.NetworkProfile+":"+p.verifier.PolicyDigest())
	}
	slices.Sort(policies)
	raw, err := json.Marshal([]any{1, egressDigest, policies, c.Report()})
	if err != nil {
		return ""
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}
