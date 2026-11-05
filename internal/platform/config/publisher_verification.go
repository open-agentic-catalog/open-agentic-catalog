package config

import (
	"encoding/json"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

type PublisherDomainVerification struct {
	Mode          string   `json:"mode"`
	ChallengeTTL  Duration `json:"challengeTTL"`
	ProofMaxAge   Duration `json:"proofMaxAge"`
	LookupTimeout Duration `json:"lookupTimeout"`
}

func PublisherDomainVerificationDefaults() PublisherDomainVerification {
	p := catalog.DefaultPublisherDomainPolicy()
	return PublisherDomainVerification{Mode: p.Mode, ChallengeTTL: Duration{p.ChallengeTTL}, ProofMaxAge: Duration{p.ProofMaxAge}, LookupTimeout: Duration{p.LookupTimeout}}
}

func (p PublisherDomainVerification) Policy() catalog.PublisherDomainPolicy {
	return catalog.PublisherDomainPolicy{Mode: p.Mode, ChallengeTTL: p.ChallengeTTL.Duration, ProofMaxAge: p.ProofMaxAge.Duration, LookupTimeout: p.LookupTimeout.Duration}
}

func applyPublisherDomainVerification(c *Config, raw string) error {
	if len(raw) > 4096 || !strings.HasPrefix(strings.TrimSpace(raw), "{") || jsondoc.Validate([]byte(raw)) != nil {
		return safeerr.New("publisher domain verification override must be bounded duplicate-free JSON")
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil || containsNull(value) {
		return safeerr.New("publisher domain verification override must not contain null values")
	}
	if jsondoc.Decode([]byte(raw), &c.PublisherDomainVerification) != nil {
		return safeerr.New("publisher domain verification override contains invalid or unknown settings")
	}
	return nil
}
