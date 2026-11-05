package config

import (
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mediaConfiguration() Config {
	c := Defaults()
	c.Federation.Policy.Public.Origins = []string{"https://images.example:443"}
	c.Federation.Policy.PrivateProfiles = []FederationPrivateProfile{{Name: "internal-media", Origins: []string{"https://images.internal.example:443"}, AssignedTenantIDs: []string{"00000000-0000-7000-8000-000000000001"}, AddressRanges: []string{"10.0.0.0/8"}, TransportRef: "direct"}}
	c.ListingMedia.AllowedOrigins = []MediaOrigin{{Origin: "https://images.example"}, {Origin: "https://images.internal.example", NetworkProfile: "internal-media"}}
	return c
}

func TestListingMediaDefaultAndOriginAdmission(t *testing.T) {
	defaults := Defaults()
	if len(defaults.ListingMedia.AllowedOrigins) != 0 || defaults.ListingMedia.Limits.FetchTimeout.Duration != 3*time.Second || defaults.ListingMedia.Limits.MaxConcurrent != 4 {
		t.Fatalf("unexpected default: %#v", defaults.ListingMedia)
	}
	if _, ok := defaults.ListingMedia.OriginBinding("https://images.example/icon.png"); ok {
		t.Fatal("empty media policy granted authority")
	}
	c := mediaConfiguration()
	if err := c.ListingMedia.Validate(c.Federation.Policy); err != nil {
		t.Fatal(err)
	}
	binding, ok := c.ListingMedia.OriginBinding("https://IMAGES.INTERNAL.EXAMPLE/icon.png")
	if !ok || binding.NetworkProfile != "internal-media" || binding.Origin != "https://images.internal.example:443" {
		t.Fatalf("binding normalization: %#v %v", binding, ok)
	}
	for _, url := range []string{"https://images.internal.example.attacker.test/i", "https://images.internal.example:8443/i", "http://images.internal.example/i"} {
		if _, ok := c.ListingMedia.OriginBinding(url); ok {
			t.Fatalf("inexact origin admitted: %s", url)
		}
	}
	for name, change := range map[string]func(*Config){
		"duplicate normalized origin": func(c *Config) {
			c.ListingMedia.AllowedOrigins = append(c.ListingMedia.AllowedOrigins, MediaOrigin{Origin: "https://IMAGES.EXAMPLE:443/"})
		},
		"unapproved public":    func(c *Config) { c.ListingMedia.AllowedOrigins[0].Origin = "https://other.example" },
		"disabled public":      func(c *Config) { c.Federation.Policy.Public = FederationPublicPolicy{Mode: "disabled"} },
		"unknown profile":      func(c *Config) { c.ListingMedia.AllowedOrigins[1].NetworkProfile = "other" },
		"wrong private origin": func(c *Config) { c.ListingMedia.AllowedOrigins[1].Origin = "https://another.internal.example" },
		"global denied origin": func(c *Config) {
			c.Federation.Policy.AdditionalDeniedOrigins = []string{"https://images.internal.example"}
		},
		"path supplied":        func(c *Config) { c.ListingMedia.AllowedOrigins[0].Origin = "https://images.example/icon" },
		"query supplied":       func(c *Config) { c.ListingMedia.AllowedOrigins[0].Origin = "https://images.example?width=2" },
		"credentials supplied": func(c *Config) { c.ListingMedia.AllowedOrigins[0].Origin = "https://user@images.example" },
		"plaintext supplied":   func(c *Config) { c.ListingMedia.AllowedOrigins[0].Origin = "http://images.example" },
		"over origin bound":    func(c *Config) { c.ListingMedia.AllowedOrigins = make([]MediaOrigin, 129) },
		"over work bound":      func(c *Config) { c.ListingMedia.Limits.MaxConcurrent = 9 },
		"zero timeout":         func(c *Config) { c.ListingMedia.Limits.FetchTimeout.Duration = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := mediaConfiguration()
			change(&candidate)
			if err := candidate.ListingMedia.Validate(candidate.Federation.Policy); err == nil {
				t.Fatal("invalid media policy accepted")
			}
		})
	}
	c = Defaults()
	c.Federation.Policy.Public = FederationPublicPolicy{Mode: "any_public_https"}
	if _, ok := c.ListingMedia.OriginBinding("https://unconfigured.example/i"); ok {
		t.Fatal("any-public federation bypassed empty media policy")
	}
	c.ListingMedia.AllowedOrigins = []MediaOrigin{{Origin: "https://explicit.example"}}
	if err := c.ListingMedia.Validate(c.Federation.Policy); err != nil {
		t.Fatalf("explicit narrowing of any-public policy rejected: %v", err)
	}
}

func TestListingMediaOverrideStrictnessAndPartialDefaults(t *testing.T) {
	c := Defaults()
	if err := applyListingMedia(&c, `{"limits":{"maxScreenshots":2,"fetchTimeout":"1500ms"}}`); err != nil {
		t.Fatal(err)
	}
	if c.ListingMedia.Limits.MaxScreenshots != 2 || c.ListingMedia.Limits.FetchTimeout.Duration != 1500*time.Millisecond || c.ListingMedia.Limits.MaxIconBytes != 256<<10 {
		t.Fatal("partial overlay lost defaults")
	}
	for _, raw := range []string{
		`null`, `[]`, `{"allowedOrigins":null}`, `{"limits":null}`, `{"limits":{"fetchTimeout":3}}`,
		`{"allowedOrigins":[],"allowedOrigins":[]}`, `{"Limits":{}}`, `{"limits":{"maxConcurrent":4,"MaxConcurrent":5}}`,
		`{"allowedOrigins":[{"origin":"https://images.example","NetworkProfile":"private"}]}`, `{"extra":true}`, `{"allowedOrigins":[]} {}`,
	} {
		candidate := Defaults()
		if err := applyListingMedia(&candidate, raw); err == nil {
			t.Fatalf("ambiguous override accepted: %s", raw)
		}
	}
	t.Setenv("CATALOG_LISTING_MEDIA", `{"limits":{"maxScreenshots":3}}`)
	loaded, err := LoadFor("../../../config/local.json", RoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ListingMedia.Limits.MaxScreenshots != 3 || loaded.ListingMedia.Limits.MaxIconBytes != 256<<10 {
		t.Fatal("runtime environment override not applied")
	}
}

func TestListingMediaPolicyFenceAndPrivateReport(t *testing.T) {
	c := mediaConfiguration()
	digest := c.ListingMedia.PolicyDigest(c.Federation.PolicyDigest)
	copyConfig := mediaConfiguration()
	copyConfig.ListingMedia.AllowedOrigins[0], copyConfig.ListingMedia.AllowedOrigins[1] = copyConfig.ListingMedia.AllowedOrigins[1], copyConfig.ListingMedia.AllowedOrigins[0]
	copyConfig.ListingMedia.AllowedOrigins[1].Origin = "https://IMAGES.EXAMPLE:443/"
	if got := copyConfig.ListingMedia.PolicyDigest(copyConfig.Federation.PolicyDigest); got != digest {
		t.Fatal("equivalent origins/order changed policy fence")
	}
	copyConfig = mediaConfiguration()
	copyConfig.ListingMedia.AllowedOrigins[1].NetworkProfile = "replacement"
	if got := copyConfig.ListingMedia.PolicyDigest(copyConfig.Federation.PolicyDigest); got == digest {
		t.Fatal("network-profile replacement did not fence receipts")
	}
	if got := c.ListingMedia.PolicyDigest("replacement-policy"); got == digest {
		t.Fatal("underlying route/trust policy did not fence receipts")
	}
	report := c.Report()
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if report.ListingMedia.OriginCount != 2 || report.ListingMedia.PrivateOriginCount != 1 || report.ListingMedia.PolicyDigest != digest {
		t.Fatalf("wrong media report: %#v", report.ListingMedia)
	}
	for _, private := range []string{"images.example", "images.internal.example", "internal-media"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("diagnostics disclosed origin/profile: %s", private)
		}
	}
	// Migration is independent of artwork work policy and egress inputs.
	c = Defaults()
	c.Database.Driver = "postgres"
	c.Database.DSN = "postgres://owner@database.example/catalog"
	c.ListingMedia.Limits.MaxConcurrent = 0
	if err := c.ValidateFor(RoleMigration); err != nil {
		t.Fatalf("migration acquired media dependency: %v", err)
	}
	if err := c.ValidateFor(RoleAPI); err == nil {
		t.Fatal("API ignored invalid media limits")
	}
}

func TestListingMediaDefaultsStrictSerialization(t *testing.T) {
	want := ListingMediaDefaults()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"allowedOrigins":[]`) {
		t.Fatal("default origins must serialize as an empty array")
	}
	var got ListingMedia
	if err := jsondoc.Decode(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("strict default serialization changed limits or origins")
	}
	if err := jsondoc.Decode([]byte(`{"allowedOrigins":null}`), &got); err == nil {
		t.Fatal("explicit null origins accepted")
	}
}
