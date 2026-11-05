package anstrust

import (
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"strings"
	"time"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

type certificate struct {
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	NotAfter    string `json:"notAfter"`
}

type event struct {
	ANSID     string `json:"ansId"`
	ANSName   string `json:"ansName"`
	RAID      string `json:"raId"`
	Type      string `json:"eventType"`
	IssuedAt  string `json:"issuedAt"`
	ExpiresAt string `json:"expiresAt"`
	Agent     struct {
		Host    string `json:"host"`
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"agent"`
	Attestations struct {
		MetadataHashes     map[string]string `json:"metadataHashes"`
		ValidIdentityCerts []certificate     `json:"validIdentityCerts"`
		ValidServerCerts   []certificate     `json:"validServerCerts"`
		IdentityCerts      []certificate     `json:"identityCerts"`
		ServerCerts        []certificate     `json:"serverCerts"`
	} `json:"attestations"`
}

type envelope struct {
	verifiedLogKey string
	SchemaVersion  string `json:"schemaVersion"`
	Signature      string `json:"signature"`
	Payload        struct {
		LogID    string `json:"logId"`
		Producer struct {
			Event     json.RawMessage `json:"event"`
			KeyID     string          `json:"keyId"`
			Signature string          `json:"signature"`
		} `json:"producer"`
	} `json:"payload"`
}

func canonical(data []byte) ([]byte, error) {
	if jsondoc.Validate(data) != nil {
		return nil, errEvidence
	}
	b, err := jsoncanonicalizer.Transform(data)
	if err != nil {
		return nil, errEvidence
	}
	return b, nil
}

func (v *Verifier) verifyEvent(payload []byte) (envelope, event, string) {
	var env envelope
	var e event
	// Unknown signed extensions remain in the canonical bytes, but cannot
	// replace protocol fields through encoding/json's case-insensitive lookup.
	if jsondoc.Validate(payload) != nil || jsonv2.Unmarshal(payload, &env, jsonv2.MatchCaseInsensitiveNames(false)) != nil {
		return env, e, "event_encoding"
	}
	if env.SchemaVersion != "V1" && env.SchemaVersion != "V2" {
		return env, e, "event_schema_unsupported"
	}
	if jsonv2.Unmarshal(env.Payload.Producer.Event, &e, jsonv2.MatchCaseInsensitiveNames(false)) != nil || e.RAID != v.policy.RegistrationAuthority || !safeName(e.ANSID) || !safeName(env.Payload.LogID) || len(e.ANSName) > 512 {
		return env, e, "event_subject"
	}
	switch e.Type {
	case "AGENT_REGISTERED", "AGENT_RENEWED", "AGENT_REVOKED", "AGENT_DEPRECATED":
	default:
		return env, e, "event_type_unsupported"
	}
	k, ok := v.registration[env.Payload.Producer.KeyID]
	if !ok || verifyJWS(env.Payload.Producer.Signature, env.Payload.Producer.Event, k, env.Payload.Producer.KeyID, v.policy.RegistrationAuthority) != nil {
		return env, e, "producer_signature"
	}
	var outer map[string]json.RawMessage
	if json.Unmarshal(payload, &outer) != nil {
		return env, e, "event_encoding"
	}
	delete(outer, "signature")
	unsigned, err := json.Marshal(outer)
	if err != nil {
		return env, e, "log_signature"
	}
	for _, key := range v.log {
		if verifyJWS(env.Signature, unsigned, key, "", v.policy.LogOrigin) == nil {
			env.verifiedLogKey = key.fingerprint
			return env, e, ""
		}
	}
	return env, e, "log_signature"
}

func verifyJWS(s string, payload []byte, k publicKey, expectedID, authority string) error {
	if len(s) > 4096 {
		return errEvidence
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[1] != "" {
		return errEvidence
	}
	header, err := strictBase64(parts[0], true)
	if err != nil || len(header) > 2048 || jsondoc.Validate(header) != nil {
		return errEvidence
	}
	sig, err := strictBase64(parts[2], true)
	if err != nil || len(sig) != 64 {
		return errEvidence
	}
	var h struct {
		Alg       string `json:"alg"`
		Kid       string `json:"kid"`
		Typ       string `json:"typ"`
		Timestamp int64  `json:"timestamp"`
		RAID      string `json:"raid"`
	}
	if jsondoc.Decode(header, &h) != nil || h.Alg != "ES256" || !safeName(h.Kid) || h.Typ != "JWT" || h.Timestamp <= 0 || h.RAID != authority || expectedID != "" && h.Kid != expectedID {
		return errEvidence
	}
	// TL key IDs are only hints. The caller selects the already accepted full key.
	p, err := canonical(payload)
	if err != nil {
		return errEvidence
	}
	j, err := jose.ParseDetached(s, p, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return errEvidence
	}
	if _, err := j.Verify(k.key); err != nil {
		return errEvidence
	}
	return nil
}

func strictBase64(s string, rawURL bool) ([]byte, error) {
	e := base64.StdEncoding
	if rawURL {
		e = base64.RawURLEncoding
	}
	b, err := e.Strict().DecodeString(s)
	if err != nil || e.EncodeToString(b) != s {
		return nil, errEvidence
	}
	return b, nil
}

func eventValidity(e event, schema string, now time.Time) (time.Time, bool) {
	issued, err := time.Parse(time.RFC3339, e.IssuedAt)
	if err != nil || issued.After(now) {
		return time.Time{}, false
	}
	expires, err := time.Parse(time.RFC3339, e.ExpiresAt)
	if err != nil || !expires.After(now) {
		return time.Time{}, false
	}
	groups := [][]certificate{e.Attestations.ValidIdentityCerts, e.Attestations.ValidServerCerts}
	if schema == "V2" {
		groups = [][]certificate{e.Attestations.IdentityCerts, e.Attestations.ServerCerts}
	}
	for _, group := range groups {
		if len(group) == 0 {
			return time.Time{}, false
		}
		// Any still-valid certificate can establish this role's bound. Do not let
		// an expired overlap certificate invalidate a valid rotation successor.
		var latest time.Time
		for _, cert := range group {
			t, err := time.Parse(time.RFC3339, cert.NotAfter)
			if err != nil || cert.Fingerprint == "" || cert.Type == "" {
				return time.Time{}, false
			}
			if t.After(latest) {
				latest = t
			}
		}
		if !latest.After(now) {
			return time.Time{}, false
		}
		if latest.Before(expires) {
			expires = latest
		}
	}
	return expires, true
}
