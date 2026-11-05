package anstrust

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const (
	MaxBundleBytes     = 1 << 20
	MaxEntryBytes      = 256 << 10
	MaxReceiptBytes    = 256 << 10
	MaxCheckpointBytes = 32 << 10
	MaxHistoryBytes    = 256 << 10
	MaxStatusBytes     = 64 << 10
)

// Bundle preserves exact evidence bytes as JSON base64 strings.
type Bundle struct {
	Profile           string `json:"profile"`
	Entry             []byte `json:"entry"`
	Receipt           []byte `json:"receipt"`
	Checkpoint        []byte `json:"checkpoint"`
	CheckpointHistory []byte `json:"checkpointHistory,omitempty"`
	StatusToken       []byte `json:"statusToken"`
}

var ErrBundle = errors.New("invalid ANS verification bundle")

func DecodeBundle(data []byte) (Bundle, error) {
	var b Bundle
	if len(data) > MaxBundleBytes || jsondoc.Decode(data, &b) != nil || !b.bounded() {
		return b, ErrBundle
	}
	return b, nil
}

func (b Bundle) bounded() bool {
	return len(b.Entry) <= MaxEntryBytes && len(b.Receipt) <= MaxReceiptBytes && len(b.Checkpoint) <= MaxCheckpointBytes &&
		len(b.CheckpointHistory) <= MaxHistoryBytes && len(b.StatusToken) <= MaxStatusBytes &&
		len(b.Entry)+len(b.Receipt)+len(b.Checkpoint)+len(b.CheckpointHistory)+len(b.StatusToken) <= MaxBundleBytes
}

// Check uses fixed codes and never exposes parser errors or untrusted text.
type Check struct {
	State string `json:"state"`
	Code  string `json:"code"`
}

type Result struct {
	Profile                    string     `json:"profile"`
	PolicyDigest               string     `json:"policyDigest"`
	EntryDigest                string     `json:"entryDigest"`
	CheckedAt                  time.Time  `json:"checkedAt"`
	ValidUntil                 *time.Time `json:"validUntil,omitempty"`
	Receipt                    Check      `json:"receipt"`
	Producer                   Check      `json:"producer"`
	Log                        Check      `json:"log"`
	Checkpoint                 Check      `json:"checkpoint"`
	Inclusion                  Check      `json:"inclusion"`
	Target                     Check      `json:"target"`
	Status                     Check      `json:"status"`
	CurrentRegistration        bool       `json:"currentRegistration"`
	SchemaVersion              string     `json:"schemaVersion,omitempty"`
	EventType                  string     `json:"eventType,omitempty"`
	Subject                    string     `json:"subject,omitempty"`
	LogID                      string     `json:"logId,omitempty"`
	RegistrationKeyFingerprint string     `json:"registrationKeyFingerprint,omitempty"`
	LogKeyFingerprint          string     `json:"logKeyFingerprint,omitempty"`
	EnvelopeLogKeyFingerprint  string     `json:"envelopeLogKeyFingerprint,omitempty"`
}

func check(state, code string) Check { return Check{State: state, Code: code} }
func valid() Check                   { return check("valid", "verified") }

// Verify performs no I/O. A negative assessment is a Result, not a parser error.
// Checkpoint inclusion proves one tree, not global log consistency.
func (v *Verifier) Verify(ctx context.Context, b Bundle, now time.Time) (Result, error) {
	na := check("not_assessed", "prerequisite_failed")
	r := Result{Profile: Profile, PolicyDigest: v.digest, EntryDigest: digest(b.Entry), CheckedAt: now.UTC(), Receipt: na, Producer: na, Log: na, Checkpoint: na, Inclusion: na, Target: na, Status: na}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if !b.bounded() {
		return r, ErrBundle
	}
	if b.Profile != Profile {
		r.Receipt = check("unsupported", "profile_unsupported")
		return r, nil
	}
	if len(b.Receipt) == 0 {
		r.Receipt = check("unavailable", "receipt_missing")
		// Acquisition refuses explicit unknown frameworks without contacting them.
		// Preserve that classification even though no receipt was acquired.
		if _, _, err := v.Reference(b.Entry); errors.Is(err, ErrFramework) {
			r.Target = check("unsupported", "trust_framework_unsupported")
		}
		return r, nil
	}
	m, k, err := v.verifyCOSE(b.Receipt, false)
	if err != nil {
		r.Receipt = check("invalid", "receipt_signature_or_encoding")
		return r, nil //nolint:nilerr // Invalid evidence is a classified assessment, not an execution failure.
	}
	r.Receipt, r.LogKeyFingerprint = valid(), k.fingerprint
	env, e, code := v.verifyEvent(m.payload)
	if code != "" {
		state := "invalid"
		if strings.HasSuffix(code, "unsupported") {
			state = "unsupported"
		}
		if code == "log_signature" {
			r.Producer = valid()
			r.Log = check(state, code)
		} else {
			r.Producer = check(state, code)
		}
		return r, nil
	}
	r.Producer, r.Log = valid(), valid()
	r.EnvelopeLogKeyFingerprint = env.verifiedLogKey
	r.RegistrationKeyFingerprint = v.registration[env.Payload.Producer.KeyID].fingerprint
	r.SchemaVersion, r.EventType, r.Subject, r.LogID = env.SchemaVersion, e.Type, e.ANSID, env.Payload.LogID
	if err := ctx.Err(); err != nil {
		return r, err
	}
	p, err := decodeProof(m)
	if err != nil {
		r.Inclusion = check("invalid", "proof_encoding")
		return r, nil //nolint:nilerr // Invalid proof is a classified assessment, not an execution failure.
	}
	if len(b.Checkpoint) == 0 {
		r.Checkpoint = check("unavailable", "checkpoint_missing")
		return r, nil
	}
	if !v.verifyCheckpoint(b, p) {
		if size, _, err := v.rawCheckpoint(b.Checkpoint); err == nil && size > p.Size && missingCheckpointHistory(b.CheckpointHistory) {
			r.Checkpoint = check("unavailable", "checkpoint_history_missing")
			return r, nil
		}
		r.Checkpoint = check("invalid", "checkpoint_binding")
		return r, nil
	}
	r.Checkpoint = valid()
	if !verifyInclusion(m.payload, p) {
		r.Inclusion = check("invalid", "inclusion_proof")
		return r, nil
	}
	r.Inclusion = valid()
	r.Target = v.matchTarget(b.Entry, env, e)
	if err := ctx.Err(); err != nil {
		return r, err
	}
	r.Status, r.ValidUntil = v.verifyStatus(b.StatusToken, env, e, now)
	r.CurrentRegistration = r.Target.State == "valid" && r.Status.State == "valid"
	return r, nil
}

func sanitized(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

type targetEntry struct {
	Identifier  string `json:"identifier"`
	DisplayName string `json:"displayName"`
	Version     string `json:"version"`
	Metadata    struct {
		LogID   string `json:"logId"`
		ANSName string `json:"ansName"`
	} `json:"metadata"`
	Trust struct {
		Identity     string          `json:"identity"`
		IdentityType string          `json:"identityType"`
		TrustSchema  json.RawMessage `json:"trustSchema"`
		Attestations []struct {
			Type      string `json:"type"`
			URI       string `json:"uri"`
			MediaType string `json:"mediaType"`
		} `json:"attestations"`
	} `json:"trustManifest"`
	TrustAlias json.RawMessage `json:"TrustManifest"`
}

// decodeTarget keeps ARD fields case-sensitive and unknown extensions inert.
// TrustManifest is the one explicit upstream alias, never a case-folding rule.
func decodeTarget(data []byte) (targetEntry, error) {
	var entry targetEntry
	if len(data) == 0 || len(data) > MaxEntryBytes || jsondoc.Validate(data) != nil || jsonv2.Unmarshal(data, &entry, jsonv2.MatchCaseInsensitiveNames(false)) != nil {
		return entry, errEvidence
	}
	if len(entry.TrustAlias) != 0 {
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			return entry, errEvidence
		}
		if _, both := fields["trustManifest"]; both {
			return entry, errEvidence
		}
		if jsonv2.Unmarshal(entry.TrustAlias, &entry.Trust, jsonv2.MatchCaseInsensitiveNames(false)) != nil {
			return entry, errEvidence
		}
	}
	return entry, nil
}

func (v *Verifier) matchTarget(data []byte, env envelope, e event) Check {
	entry, err := decodeTarget(data)
	if err != nil {
		return check("invalid", "entry_encoding")
	}
	if entry.Identifier == "" || entry.DisplayName == "" || entry.Version == "" || entry.Metadata.LogID == "" || entry.Metadata.ANSName == "" || entry.Trust.Identity == "" || len(entry.Trust.Attestations) == 0 {
		return check("unavailable", "target_evidence_incomplete")
	}
	if _, err := catalog.ParseAIRIdentifier(entry.Identifier); err != nil {
		return check("invalid", "entry_identifier")
	}
	if parsed, err := uuid.Parse(e.ANSID); err != nil || parsed.String() != e.ANSID {
		return check("invalid", "receipt_subject")
	}
	if len(entry.Trust.TrustSchema) != 0 {
		return check("unsupported", "trust_schema_unsupported")
	}
	host := strings.ToLower(e.Agent.Host)
	if !slices.Contains(v.policy.PublisherDomains, host) {
		return check("invalid", "publisher_not_permitted")
	}
	name := sanitized(e.Agent.Name)
	identifier := "urn:air:" + host + ":agents:" + strings.Join(strings.Fields(strings.TrimSpace(name)), "-")
	if entry.Identifier != identifier || entry.DisplayName != name || entry.Version != sanitized(e.Agent.Version) || entry.Metadata.LogID != env.Payload.LogID || entry.Metadata.ANSName != e.ANSName {
		return check("invalid", "target_mismatch")
	}
	if entry.Trust.IdentityType != "https" || entry.Trust.Identity != "https://"+host {
		return check("invalid", "identity_mismatch")
	}
	if len(entry.Trust.Attestations) > 32 {
		return check("invalid", "attestation_limit")
	}
	matches := 0
	for _, a := range entry.Trust.Attestations {
		if a.Type != "ANS-Registration" {
			continue
		}
		u, err := url.Parse(a.URI)
		if err == nil && u.String() == v.policy.EvidenceOrigin+"/v1/agents/"+e.ANSID+"/receipt" && a.MediaType == "application/scitt-receipt+cose" {
			matches++
		} else {
			return check("invalid", "receipt_reference_mismatch")
		}
	}
	if matches == 1 {
		return valid()
	}
	if matches > 1 {
		return check("invalid", "receipt_reference_ambiguous")
	}
	return check("unsupported", "registration_reference_missing")
}

type statusCertificate struct {
	Fingerprint string `cbor:"1,keyasint"`
	Type        string `cbor:"2,keyasint"`
}
type statusClaims struct {
	ID       string              `cbor:"1,keyasint"`
	State    string              `cbor:"2,keyasint"`
	Issued   int64               `cbor:"3,keyasint"`
	Expires  int64               `cbor:"4,keyasint"`
	Name     string              `cbor:"5,keyasint"`
	Identity []statusCertificate `cbor:"6,keyasint"`
	Server   []statusCertificate `cbor:"7,keyasint"`
	Hashes   map[string]string   `cbor:"8,keyasint"`
}

func (v *Verifier) verifyStatus(data []byte, env envelope, e event, now time.Time) (Check, *time.Time) {
	if len(data) == 0 {
		return check("unavailable", "status_missing"), nil
	}
	m, _, err := v.verifyCOSE(data, true)
	if err != nil {
		return check("invalid", "status_signature_or_encoding"), nil
	}
	var s statusClaims
	var fields map[int64]cbor.RawMessage
	if decodeCBOR(m.payload, &fields) != nil {
		return check("invalid", "status_encoding"), nil
	}
	for key := range fields {
		if key < 1 || key > 8 {
			return check("unsupported", "status_claim_unsupported"), nil
		}
	}
	if decodeCBOR(m.payload, &s) != nil || s.ID != e.ANSID || s.Name != e.ANSName {
		return check("invalid", "status_subject"), nil
	}
	issued, expires := time.Unix(s.Issued, 0).UTC(), time.Unix(s.Expires, 0).UTC()
	if s.Issued <= 0 || !expires.After(issued) || issued.After(now.Add(v.policy.FutureSkew)) || now.Sub(issued) >= v.policy.MaxStatusAge || !expires.After(now) {
		return check("invalid", "status_freshness"), nil
	}
	if s.State != "ACTIVE" && s.State != "DEPRECATED" && s.State != "REVOKED" && s.State != "EXPIRED" {
		return check("unsupported", "status_state_unsupported"), nil
	}
	if s.State != "ACTIVE" || e.Type == "AGENT_REVOKED" || e.Type == "AGENT_DEPRECATED" {
		return check("invalid", "registration_not_active"), nil
	}
	bound, ok := eventValidity(e, env.SchemaVersion, now)
	if !ok {
		return check("unavailable", "registration_validity_missing_or_expired"), nil
	}
	identities, servers := e.Attestations.ValidIdentityCerts, e.Attestations.ValidServerCerts
	if env.SchemaVersion == "V2" {
		identities, servers = e.Attestations.IdentityCerts, e.Attestations.ServerCerts
	}
	identityBound, identityOK := matchingCertificates(s.Identity, identities, now)
	serverBound, serverOK := matchingCertificates(s.Server, servers, now)
	if !identityOK || !serverOK {
		return check("invalid", "status_certificate_mismatch"), nil
	}
	// These authenticate matching producer claims, not fetched artifact bytes.
	if !maps.Equal(s.Hashes, e.Attestations.MetadataHashes) {
		return check("invalid", "status_metadata_mismatch"), nil
	}
	if bound.Before(expires) {
		expires = bound
	}
	for _, certBound := range []time.Time{identityBound, serverBound} {
		if !certBound.IsZero() && certBound.Before(expires) {
			expires = certBound
		}
	}
	if age := issued.Add(v.policy.MaxStatusAge); age.Before(expires) {
		expires = age
	}
	expires = expires.UTC()
	return valid(), &expires
}

func matchingCertificates(claims []statusCertificate, certs []certificate, now time.Time) (time.Time, bool) {
	var latest time.Time
	for _, claim := range claims {
		found := false
		for _, cert := range certs {
			if cert.Fingerprint != claim.Fingerprint || cert.Type != claim.Type {
				continue
			}
			found = true
			expires, err := time.Parse(time.RFC3339, cert.NotAfter)
			if err != nil {
				return time.Time{}, false
			}
			if expires.After(latest) {
				latest = expires
			}
		}
		if !found {
			return time.Time{}, false
		}
	}
	return latest, len(claims) == 0 || latest.After(now)
}
