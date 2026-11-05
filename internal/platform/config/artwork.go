package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// MediaOrigin narrows existing egress authority. Only operator configuration
// selects a network profile; resource documents cannot request one.
type MediaOrigin struct {
	Origin         string `json:"origin"`
	NetworkProfile string `json:"networkProfile,omitempty"`
}

type ListingMedia struct {
	AllowedOrigins []MediaOrigin      `json:"allowedOrigins"`
	Limits         ListingMediaLimits `json:"limits"`
}

type ListingMediaLimits struct {
	MaxScreenshots      int      `json:"maxScreenshots"`
	MaxURLBytes         int      `json:"maxUrlBytes"`
	MaxAltRunes         int      `json:"maxAltRunes"`
	MaxCaptionRunes     int      `json:"maxCaptionRunes"`
	MaxAttributionRunes int      `json:"maxAttributionRunes"`
	MaxDescriptorBytes  int      `json:"maxDescriptorBytes"`
	MaxIconBytes        int64    `json:"maxIconBytes"`
	MaxScreenshotBytes  int64    `json:"maxScreenshotBytes"`
	MaxIconEdge         int      `json:"maxIconEdge"`
	MaxIconPixels       int64    `json:"maxIconPixels"`
	MaxScreenshotEdge   int      `json:"maxScreenshotEdge"`
	MaxScreenshotPixels int64    `json:"maxScreenshotPixels"`
	FetchTimeout        Duration `json:"fetchTimeout"`
	ValidationTimeout   Duration `json:"validationTimeout"`
	MaxConcurrent       int      `json:"maxConcurrent"`
}

func ListingMediaDefaults() ListingMedia {
	l := artwork.DefaultLimits()
	return ListingMedia{AllowedOrigins: []MediaOrigin{}, Limits: ListingMediaLimits{
		MaxScreenshots: l.MaxScreenshots, MaxURLBytes: l.MaxURLBytes, MaxAltRunes: l.MaxAltRunes, MaxCaptionRunes: l.MaxCaptionRunes, MaxAttributionRunes: l.MaxAttributionRunes, MaxDescriptorBytes: l.MaxDescriptorBytes,
		MaxIconBytes: l.MaxIconBytes, MaxScreenshotBytes: l.MaxScreenshotBytes, MaxIconEdge: l.MaxIconEdge, MaxIconPixels: l.MaxIconPixels, MaxScreenshotEdge: l.MaxScreenshotEdge, MaxScreenshotPixels: l.MaxScreenshotPixels,
		FetchTimeout: Duration{l.FetchTimeout}, ValidationTimeout: Duration{l.ValidationTimeout}, MaxConcurrent: l.MaxConcurrent,
	}}
}

func (m ListingMedia) ArtworkLimits() artwork.Limits {
	l := m.Limits
	return artwork.Limits{MaxScreenshots: l.MaxScreenshots, MaxURLBytes: l.MaxURLBytes, MaxAltRunes: l.MaxAltRunes, MaxCaptionRunes: l.MaxCaptionRunes, MaxAttributionRunes: l.MaxAttributionRunes, MaxDescriptorBytes: l.MaxDescriptorBytes,
		MaxIconBytes: l.MaxIconBytes, MaxScreenshotBytes: l.MaxScreenshotBytes, MaxIconEdge: l.MaxIconEdge, MaxIconPixels: l.MaxIconPixels, MaxScreenshotEdge: l.MaxScreenshotEdge, MaxScreenshotPixels: l.MaxScreenshotPixels,
		FetchTimeout: l.FetchTimeout.Duration, ValidationTimeout: l.ValidationTimeout.Duration, MaxConcurrent: l.MaxConcurrent}
}

// Validate checks both presentation limits and the complete exact origin/profile
// assignment without contacting DNS, loading credentials or broadening routes.
func (m ListingMedia) Validate(policy FederationPolicy) error {
	if err := m.ArtworkLimits().Validate(); err != nil {
		return safeerr.New("config: listingMedia limits exceed supported bounds")
	}
	if len(m.AllowedOrigins) > 128 {
		return safeerr.New("config: listingMedia permits at most 128 exact origin bindings")
	}
	seen := make(map[string]bool, len(m.AllowedOrigins))
	for _, binding := range m.AllowedOrigins {
		origins, err := normalizePolicyOrigins([]string{binding.Origin}, false, 1)
		if err != nil {
			return safeerr.New("config: listingMedia origin must be exact HTTPS without path, query or credentials")
		}
		origin := origins[0]
		if seen[origin] {
			return safeerr.New("config: listingMedia origins must be unique after normalization")
		}
		seen[origin] = true
		if containsMediaOrigin(policy.AdditionalDeniedOrigins, origin) {
			return safeerr.New("config: listingMedia origin is denied by federation policy")
		}
		if binding.NetworkProfile == "" {
			if policy.Public.Mode == fetcher.PublicAnyPublicHTTPS {
				continue
			}
			if policy.Public.Mode != fetcher.PublicAllowlist || !containsMediaOrigin(policy.Public.Origins, origin) {
				return safeerr.New("config: listingMedia origin is not admitted by public federation policy")
			}
			continue
		}
		found := false
		for _, profile := range policy.PrivateProfiles {
			if profile.Name == binding.NetworkProfile && containsMediaOrigin(profile.Origins, origin) {
				found = true
				break
			}
		}
		if !found {
			return safeerr.New("config: listingMedia networkProfile must name an existing private route containing its origin")
		}
	}
	return nil
}

func containsMediaOrigin(values []string, origin string) bool {
	for _, value := range values {
		normalized, err := fetcher.NormalizeOrigin(value, false)
		if err == nil && normalized == origin {
			return true
		}
	}
	return false
}

// OriginBinding resolves only an exact configured origin. Tenant assignment is
// checked by Router on each private-route request.
func (m ListingMedia) OriginBinding(value string) (MediaOrigin, bool) {
	origin, err := fetcher.NormalizeOrigin(value, false)
	if err != nil {
		return MediaOrigin{}, false
	}
	for _, binding := range m.AllowedOrigins {
		normalized, e := fetcher.NormalizeOrigin(binding.Origin, false)
		if e == nil && normalized == origin {
			binding.Origin = normalized
			return binding, true
		}
	}
	return MediaOrigin{}, false
}

// PolicyDigest binds receipts to origin/profile choices, work limits and the
// underlying policy/trust/tenant-assignment generation without disclosing them.
func (m ListingMedia) PolicyDigest(federationDigest string) string {
	bindings := append([]MediaOrigin(nil), m.AllowedOrigins...)
	for i := range bindings {
		if normalized, err := fetcher.NormalizeOrigin(bindings[i].Origin, false); err == nil {
			bindings[i].Origin = normalized
		}
	}
	slices.SortFunc(bindings, func(a, b MediaOrigin) int { return strings.Compare(a.Origin, b.Origin) })
	raw, err := json.Marshal(struct {
		Version    int                `json:"version"`
		Federation string             `json:"federation"`
		Origins    []MediaOrigin      `json:"origins"`
		Limits     ListingMediaLimits `json:"limits"`
	}{1, federationDigest, bindings, m.Limits})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func applyListingMedia(c *Config, raw string) error {
	if len(raw) > 32<<10 || !strings.HasPrefix(strings.TrimSpace(raw), "{") || jsondoc.Validate([]byte(raw)) != nil {
		return safeerr.New("listing media override must be bounded duplicate-free JSON")
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil || containsNull(value) {
		return safeerr.New("listing media override must not contain null values")
	}
	if err := jsondoc.Decode([]byte(raw), &c.ListingMedia); err != nil {
		return safeerr.New("listing media override contains invalid or unknown settings")
	}
	return nil
}

type ListingMediaReport struct {
	OriginCount        int                `json:"originCount"`
	PrivateOriginCount int                `json:"privateOriginCount"`
	Limits             ListingMediaLimits `json:"limits"`
	PolicyDigest       string             `json:"policyDigest"`
}

func (c Config) listingMediaReport() ListingMediaReport {
	private := 0
	for _, binding := range c.ListingMedia.AllowedOrigins {
		if binding.NetworkProfile != "" {
			private++
		}
	}
	return ListingMediaReport{OriginCount: len(c.ListingMedia.AllowedOrigins), PrivateOriginCount: private, Limits: c.ListingMedia.Limits, PolicyDigest: c.ListingMedia.PolicyDigest(c.Federation.PolicyDigest)}
}
