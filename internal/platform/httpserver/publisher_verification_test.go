package httpserver

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPublisherDomainTagBindings(t *testing.T) {
	t.Parallel()
	policy := catalog.DefaultPublisherDomainPolicy()
	policy.Mode = catalog.DomainVerificationOptional
	status := catalog.PublisherDomainStatus{Domain: "example.test", Mode: policy.Mode, State: "not_verified", ChallengeState: "not_issued", Version: catalog.PublisherDomainVersion{PublisherRevision: 2}}
	tag, err := publisherDomainTag("tenant", "publisher", status, policy)
	if err != nil || !strings.HasPrefix(tag, `"publisher-domain-`) || strings.HasPrefix(tag, `W/`) {
		t.Fatal(tag, err)
	}
	cases := map[string]func(*catalog.PublisherDomainStatus, *catalog.PublisherDomainPolicy){
		"domain": func(s *catalog.PublisherDomainStatus, _ *catalog.PublisherDomainPolicy) { s.Domain = "other.test" },
		"parent": func(s *catalog.PublisherDomainStatus, _ *catalog.PublisherDomainPolicy) {
			s.Version.PublisherRevision++
		},
		"state identity":      func(s *catalog.PublisherDomainStatus, _ *catalog.PublisherDomainPolicy) { s.Version.ID = "new-state" },
		"revision":            func(s *catalog.PublisherDomainStatus, _ *catalog.PublisherDomainPolicy) { s.Version.Revision++ },
		"challenge freshness": func(s *catalog.PublisherDomainStatus, _ *catalog.PublisherDomainPolicy) { s.ChallengeState = "expired" },
		"proof freshness":     func(s *catalog.PublisherDomainStatus, _ *catalog.PublisherDomainPolicy) { s.State = "expired" },
		"policy":              func(_ *catalog.PublisherDomainStatus, p *catalog.PublisherDomainPolicy) { p.ProofMaxAge += time.Hour },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, p := status, policy
			change(&s, &p)
			got, e := publisherDomainTag("tenant", "publisher", s, p)
			if e != nil || got == tag {
				t.Fatal("tag did not bind changed input", got, e)
			}
		})
	}
	for _, binding := range [][2]string{{"other-tenant", "publisher"}, {"tenant", "other-publisher"}} {
		got, e := publisherDomainTag(binding[0], binding[1], status, policy)
		if e != nil || got == tag {
			t.Fatal("tag lost resource binding", e)
		}
	}
	status.Version.ID = "retained-state"
	tag, err = publisherDomainTag("tenant", "publisher", status, policy)
	if err != nil {
		t.Fatal(err)
	}
	status.Version.PublisherRevision++
	same, err := publisherDomainTag("tenant", "publisher", status, policy)
	if err != nil || tag != same {
		t.Fatal("unrelated publisher metadata changed an existing verification tag", err)
	}
	raw, err := json.Marshal(status)
	if err != nil || strings.Contains(string(raw), "version") || strings.Contains(string(raw), "nonce") {
		t.Fatal("wire status exposed internal state", string(raw), err)
	}
}

func TestPublisherDomainTagRefusesUnencodableEvidence(t *testing.T) {
	t.Parallel()
	status := catalog.PublisherDomainStatus{Domain: "example.test", ExpiresAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}
	if _, err := publisherDomainTag("tenant", "publisher", status, catalog.DefaultPublisherDomainPolicy()); err == nil {
		t.Fatal("invalid evidence produced a revision token")
	}
}
