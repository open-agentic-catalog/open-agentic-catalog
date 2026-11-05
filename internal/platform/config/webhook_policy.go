// Webhook policy loading reuses normalized egress rules while retaining an
// independent enabled-capability and credential boundary.

package config

import (
	"fmt"
	"strings"
)

// Webhooks reuse the reviewed bounded policy document format, but never inherit
// federation's permissions, credentials, private roots, or default topology.
func loadWebhookPolicy(cfg *Config) error {
	cfg.Webhooks.Policy = FederationPolicy{}
	cfg.Webhooks.PolicyDigest = ""
	if !cfg.Webhooks.Enabled || cfg.Webhooks.PolicyFile == "" {
		return nil
	}
	isolated := *cfg
	isolated.Federation.PolicyFile = cfg.Webhooks.PolicyFile
	isolated.Federation.Policy = FederationPolicy{}
	if err := loadFederationPolicy(&isolated); err != nil {
		return fmt.Errorf("config: webhook egress policy: %w", err)
	}
	for _, profile := range isolated.Federation.Policy.PrivateProfiles {
		for _, origin := range profile.Origins {
			if !strings.HasPrefix(origin, "https://") {
				return fmt.Errorf("config: webhook private origins must use HTTPS")
			}
		}
	}
	cfg.Webhooks.Policy = isolated.Federation.Policy
	cfg.Webhooks.PolicyDigest = isolated.Federation.PolicyDigest
	return nil
}
