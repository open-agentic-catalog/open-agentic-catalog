package anstrust

import (
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

var (
	ErrReference = errors.New("invalid ANS registration reference")
	ErrFramework = errors.New("unsupported ANS verification framework")
)

// Reference validates a bounded entry before acquisition. Only this policy's
// exact receipt path can select a subject; other entry URLs are never followed.
func (v *Verifier) Reference(data []byte) (subject, receiptURL string, err error) {
	entry, decodeErr := decodeTarget(data)
	if decodeErr != nil {
		return "", "", ErrReference
	}
	if len(entry.Trust.TrustSchema) != 0 {
		return "", "", ErrFramework
	}
	id, parseErr := catalog.ParseAIRIdentifier(entry.Identifier)
	if parseErr != nil || len(id.Namespace) != 1 || id.Namespace[0] != "agents" || !slices.Contains(v.policy.PublisherDomains, id.Publisher) ||
		entry.Trust.IdentityType != "https" || entry.Trust.Identity != "https://"+id.Publisher || entry.DisplayName == "" || entry.Version == "" || entry.Metadata.LogID == "" || entry.Metadata.ANSName == "" || len(entry.Trust.Attestations) > 32 {
		return "", "", ErrReference
	}
	for _, a := range entry.Trust.Attestations {
		if a.Type != "ANS-Registration" {
			continue
		}
		if receiptURL != "" || a.MediaType != "application/scitt-receipt+cose" {
			return "", "", ErrReference
		}
		prefix := v.policy.EvidenceOrigin + "/v1/agents/"
		if !strings.HasPrefix(a.URI, prefix) || !strings.HasSuffix(a.URI, "/receipt") {
			return "", "", ErrReference
		}
		subject = strings.TrimSuffix(strings.TrimPrefix(a.URI, prefix), "/receipt")
		parsed, e := uuid.Parse(subject)
		if e != nil || parsed.String() != subject || a.URI != prefix+subject+"/receipt" {
			return "", "", ErrReference
		}
		receiptURL = a.URI
	}
	if receiptURL == "" {
		return "", "", ErrFramework
	}
	return subject, receiptURL, nil
}

// RequiredHistory authenticates the receipt, event and latest checkpoint before
// choosing at most one exact-size history request. Zero means none is needed.
// A failed prerequisite must stop acquisition, not trigger a history scan.
func (v *Verifier) RequiredHistory(entry, receipt, checkpoint []byte) (uint64, error) {
	if len(entry) > MaxEntryBytes || len(receipt) > MaxReceiptBytes || len(checkpoint) > MaxCheckpointBytes {
		return 0, ErrBundle
	}
	m, _, err := v.verifyCOSE(receipt, false)
	if err != nil {
		return 0, errEvidence
	}
	env, e, code := v.verifyEvent(m.payload)
	if code != "" || v.matchTarget(entry, env, e).State != "valid" {
		return 0, errEvidence
	}
	p, err := decodeProof(m)
	if err != nil || !verifyInclusion(m.payload, p) {
		return 0, errEvidence
	}
	size, _, err := v.rawCheckpoint(checkpoint)
	if err != nil || size < p.Size {
		return 0, errEvidence
	}
	if size > p.Size {
		return p.Size, nil
	}
	if !v.verifyCheckpoint(Bundle{Checkpoint: checkpoint}, p) {
		return 0, errEvidence
	}
	return 0, nil
}
