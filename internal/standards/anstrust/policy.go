// Package anstrust verifies the fixed, offline ANS registration profile.
// It grants no network authority, publication approval or execution permission.
package anstrust

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const Profile = "ans-registration-v1"

// Policy contains operator-supplied authority, never keys taken from evidence.
type Policy struct {
	Profile               string
	RegistrationAuthority string
	LogOrigin             string
	EvidenceOrigin        string
	PublisherDomains      []string
	RegistrationKeys      map[string][]byte
	LogKeys               [][]byte
	MaxStatusAge          time.Duration
	FutureSkew            time.Duration
}

type publicKey struct {
	key         *ecdsa.PublicKey
	fingerprint string
	hint        [4]byte
}

// Verifier retains an immutable, validated authority policy.
type Verifier struct {
	policy       Policy
	registration map[string]publicKey
	log          []publicKey
	digest       string
}

var ErrPolicy = errors.New("invalid ANS verification policy")

// New validates bounded, public-only P-256 keys and exact authority inputs.
// A zero status age selects one hour. FutureSkew is explicit; zero permits no skew.
func New(p Policy) (*Verifier, error) {
	if p.MaxStatusAge == 0 {
		p.MaxStatusAge = time.Hour
	}
	if ValidateAuthority(p) != nil || len(p.RegistrationKeys) == 0 || len(p.RegistrationKeys) > 8 || len(p.LogKeys) == 0 || len(p.LogKeys) > 8 {
		return nil, ErrPolicy
	}
	p.PublisherDomains = slices.Clone(p.PublisherDomains)
	slices.Sort(p.PublisherDomains)
	v := &Verifier{policy: p, registration: make(map[string]publicKey)}
	seen := make(map[string]bool)
	ids := make([]string, 0, len(p.RegistrationKeys))
	for id, data := range p.RegistrationKeys {
		k, err := parseKey(data)
		if err != nil || !safeName(id) || seen[k.fingerprint] {
			return nil, ErrPolicy
		}
		seen[k.fingerprint] = true
		v.registration[id] = k
		ids = append(ids, id+":"+k.fingerprint)
	}
	seen = make(map[string]bool)
	hints := make(map[[4]byte]bool)
	logs := make([]string, 0, len(p.LogKeys))
	for _, data := range p.LogKeys {
		k, err := parseKey(data)
		if err != nil || seen[k.fingerprint] || hints[k.hint] {
			return nil, ErrPolicy
		}
		seen[k.fingerprint], hints[k.hint] = true, true
		v.log = append(v.log, k)
		logs = append(logs, k.fingerprint)
	}
	slices.Sort(ids)
	slices.Sort(logs)
	// Fingerprints, not PEM whitespace or untrusted short key IDs, bind policy.
	encoded, err := json.Marshal([]any{Profile, p.RegistrationAuthority, p.LogOrigin, p.EvidenceOrigin, p.PublisherDomains, ids, logs, p.MaxStatusAge.String(), p.FutureSkew.String()})
	if err != nil {
		return nil, ErrPolicy
	}
	v.digest = digest(encoded)
	v.policy.RegistrationKeys, v.policy.LogKeys = nil, nil
	return v, nil
}

// ValidateAuthority checks profile, authority and freshness settings without
// reading keys. Disabled deployments can reject malformed policy metadata safely.
func ValidateAuthority(p Policy) error {
	if p.Profile != Profile || !safeName(p.RegistrationAuthority) || !safeName(p.LogOrigin) ||
		p.MaxStatusAge < time.Second || p.MaxStatusAge > 24*time.Hour || p.FutureSkew < 0 || p.FutureSkew > time.Minute ||
		len(p.PublisherDomains) == 0 || len(p.PublisherDomains) > 64 {
		return ErrPolicy
	}
	u, err := url.Parse(p.EvidenceOrigin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.String() != p.EvidenceOrigin {
		return ErrPolicy
	}
	p.PublisherDomains = slices.Clone(p.PublisherDomains)
	slices.Sort(p.PublisherDomains)
	for i, d := range p.PublisherDomains {
		if d != strings.ToLower(d) || strings.TrimSpace(d) != d || (i > 0 && d == p.PublisherDomains[i-1]) {
			return ErrPolicy
		}
		if _, err := catalog.ParseAIRIdentifier("urn:air:" + d + ":agents:sample"); err != nil {
			return ErrPolicy
		}
	}
	return nil
}

// PolicyDigest identifies the normalized authority, complete key fingerprints
// and freshness rules. It contains no public-key bytes or local file paths.
func (v *Verifier) PolicyDigest() string { return v.digest }

func parseKey(data []byte) (publicKey, error) {
	if len(data) > 4096 {
		return publicKey{}, ErrPolicy
	}
	b, rest := pem.Decode(data)
	if b == nil || b.Type != "PUBLIC KEY" || len(b.Headers) != 0 || strings.TrimSpace(string(rest)) != "" {
		return publicKey{}, ErrPolicy
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return publicKey{}, ErrPolicy
	}
	e, ok := k.(*ecdsa.PublicKey)
	if !ok || e.Curve != elliptic.P256() {
		return publicKey{}, ErrPolicy
	}
	h := sha256.Sum256(b.Bytes)
	return publicKey{key: e, fingerprint: hex.EncodeToString(h[:]), hint: [4]byte(h[:4])}, nil
}

func safeName(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
			return false
		}
	}
	return true
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
