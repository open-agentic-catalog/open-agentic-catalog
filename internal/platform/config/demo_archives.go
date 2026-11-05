// Development archive configuration admits one literal local TLS fixture origin.
package config

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"fmt"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demoarchive"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// DemoArchives is disabled when Origin is empty. It is never a production fetch route.
type DemoArchives struct {
	Origin          string                `json:"origin,omitempty"`
	AllowlistFile   string                `json:"allowlistFile,omitempty"`
	Allowlist       demoarchive.Allowlist `json:"-"`
	TrustBundleFile string                `json:"trustBundleFile,omitempty"`
	TrustBundlePEM  []byte                `json:"-"`
	TrustDigest     string                `json:"-"`
}

func applyDemoArchives(c *Config, raw string) error {
	var value any
	if len(raw) > 4096 || jsondoc.Validate([]byte(raw)) != nil || json.Unmarshal([]byte(raw), &value) != nil || containsNull(value) {
		return safeerr.New("config: invalid development demo archive override")
	}
	var settings DemoArchives
	if jsondoc.Decode([]byte(raw), &settings) != nil {
		return safeerr.New("config: invalid development demo archive fields")
	}
	c.DemoArchives = settings
	return nil
}

// validateDemoArchives rejects an enabled local fixture before loading any file outside development.
func (c Config) validateDemoArchives() error {
	d := c.DemoArchives
	if d.Origin == "" {
		if d.AllowlistFile != "" || d.Allowlist.Format != 0 || d.TrustBundleFile != "" || len(d.TrustBundlePEM) != 0 || d.TrustDigest != "" {
			return safeerr.New("config: disabled demo archives cannot configure trust")
		}
		return nil
	}
	if c.Environment != "development" || c.Auth.Mode != "development" {
		return safeerr.New("config: demo archives require development environment and authentication")
	}
	if demoarchive.ValidateOrigin(d.Origin) != nil || d.TrustBundleFile == "" || d.AllowlistFile == "" {
		return safeerr.New("config: demo archives require a literal local HTTPS origin, trust bundle and prepared allowlist")
	}

	return nil
}

func loadDemoArchives(c *Config) error {
	if c.DemoArchives.Origin == "" {
		return nil
	}
	raw, err := readBoundedPolicy(c.DemoArchives.TrustBundleFile)
	if err != nil {
		return safeerr.New("config: cannot read demo archive trust bundle")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return safeerr.New("config: invalid demo archive trust bundle")
	}
	allowlistRaw, err := readBoundedFederationFile(c.DemoArchives.AllowlistFile, demoarchive.MaxAllowlistBytes)
	if err != nil {
		return safeerr.New("config: cannot read demo archive allowlist")
	}
	allowlist, err := demoarchive.Decode(allowlistRaw)
	if err != nil {
		return safeerr.New("config: invalid demo archive allowlist")
	}
	c.DemoArchives.Allowlist = allowlist
	c.DemoArchives.TrustBundlePEM = raw
	c.DemoArchives.TrustDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	return nil
}
