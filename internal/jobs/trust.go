package jobs

import (
	"context"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
)

const TrustKind = "source.trust_verify"
const MaxTrustRecordBytes = anstrust.MaxBundleBytes + 32<<10

// TrustPayload retains exact selected bytes as base64, never re-encoded JSON.
// PolicyDigest binds startup authority and acquisition settings, not source keys.
type TrustPayload struct {
	SourceID           string `json:"sourceId"`
	SourceRevision     int64  `json:"sourceRevision"`
	SourcePolicyDigest string `json:"sourcePolicyDigest"`
	CredentialRevision string `json:"credentialRevision"`
	PolicyName         string `json:"policyName"`
	PolicyDigest       string `json:"policyDigest"`
	Identifier         string `json:"identifier"`
	Version            string `json:"version"`
	VersionPresent     bool   `json:"versionPresent"`
	MediaType          string `json:"mediaType"`
	Delivery           string `json:"delivery"`
	ExactEntryDigest   string `json:"exactEntryDigest"`
	Entry              []byte `json:"entry"`
}

func DecodeTrustPayload(job Job) (TrustPayload, error) {
	var p TrustPayload
	if job.Kind != TrustKind || job.PayloadVersion != 1 || len(job.Payload) > MaxTrustRecordBytes || jsondoc.Decode(job.Payload, &p) != nil || !identity.ValidTenantID(p.SourceID) || p.SourceRevision < 1 || len(p.PolicyName) == 0 || len(p.PolicyName) > 63 || !trustDigest(p.PolicyDigest) || !trustDigest(p.SourcePolicyDigest) || len(p.CredentialRevision) > 512 || len(p.Entry) == 0 || len(p.Entry) > anstrust.MaxEntryBytes || p.ExactEntryDigest != audit.DigestBytes(p.Entry) || p.Identifier == "" || len(p.Identifier) > 4096 || utf8.RuneCountInString(p.Version) > 128 || !p.VersionPresent && p.Version != "" || len(p.MediaType) == 0 || utf8.RuneCountInString(p.MediaType) > 255 || p.Delivery != "url" && p.Delivery != "data" {
		return TrustPayload{}, catalog.E(catalog.CodeInvalidArgument, "trust assessment Job payload is invalid")
	}
	if _, err := catalog.ParseAIRIdentifier(p.Identifier); err != nil || !TrustEntryMatches(p.Entry, p) {
		return TrustPayload{}, catalog.E(catalog.CodeInvalidArgument, "trust assessment native selection is invalid")
	}
	return p, nil
}

func trustDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

// TrustEntryMatches compares the exact native selection with retained raw bytes.
func TrustEntryMatches(raw []byte, p TrustPayload) bool {
	var e struct {
		Identifier string `json:"identifier"`
		Version    string `json:"version"`
		Type       string `json:"type"`
		URL        string `json:"url"`
	}
	var fields map[string]json.RawMessage
	if standards.ValidateARDSearchEntry(raw) != nil || jsonv2.Unmarshal(raw, &e, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false)) != nil || json.Unmarshal(raw, &fields) != nil {
		return false
	}
	_, present := fields["version"]
	delivery := "data"
	if e.URL == "" {
		if _, ok := fields["data"]; !ok {
			return false
		}
	}
	if e.URL != "" {
		delivery = "url"
	}
	return e.Identifier == p.Identifier && e.Version == p.Version && present == p.VersionPresent && e.Type == p.MediaType && delivery == p.Delivery && audit.DigestBytes(raw) == p.ExactEntryDigest
}

// CompletionEvidence is implemented only by attributed handler results.
// Stores persist this evidence atomically with the owned terminal transition.
type CompletionEvidence interface{ JobCompletionEvidence() audit.Record }

// TrustAssessment is private retained evidence. Generic Views never project it.
type TrustAssessment struct {
	SourceID          string          `json:"sourceId"`
	SourceRevision    int64           `json:"sourceRevision"`
	PolicyName        string          `json:"policyName"`
	PolicyDigest      string          `json:"policyDigest"`
	Authority         string          `json:"authority"`
	ExactEntryDigest  string          `json:"exactEntryDigest"`
	MediaType         string          `json:"mediaType"`
	Delivery          string          `json:"delivery"`
	Assessment        anstrust.Result `json:"assessment"`
	Evidence          anstrust.Bundle `json:"evidence"`
	SoftwareTrust     string          `json:"softwareTrust"`
	ArtifactIntegrity string          `json:"artifactIntegrity"`
	Rights            string          `json:"rights"`
	EvidenceRecord    audit.Record    `json:"-"`
}

func (r TrustAssessment) JobCompletionEvidence() audit.Record { return r.EvidenceRecord }
func ValidateTrustCompletion(job Job, raw json.RawMessage, record audit.Record) error {
	p, err := DecodeTrustPayload(job)
	if err != nil {
		return err
	}
	r, err := DecodeTrustAssessment(p, raw)
	if err != nil {
		return err
	}
	if record.Action != audit.ActionSourceTrustCompleted || record.TenantID != job.TenantID || record.TargetID != job.ID || factString(record, "sourceId") != p.SourceID || factInt64(record, "sourceRevision") != p.SourceRevision || factString(record, "entryDigest") != p.ExactEntryDigest || factString(record, "policyDigest") != p.PolicyDigest || factString(record, "resultDigest") != audit.DigestBytes(raw) || factString(record, "jobId") != job.ID || factString(record, "jobKind") != job.Kind || factInt64(record, "jobAttempt") != int64(job.Attempt) || factString(record, "initiator") != job.Actor {
		return catalog.E(catalog.CodeInternal, "trust completion audit does not match the claimed Job")
	}
	facts, _ := record.Details["facts"].(map[string]any)
	if current, ok := facts["currentRegistration"].(bool); !ok || current != r.Assessment.CurrentRegistration {
		return catalog.E(catalog.CodeInternal, "trust completion audit outcome is invalid")
	}
	return audit.ValidateNewRecord(record)
}

// DecodeTrustAssessment checks retained target binding on completion and reads.
func DecodeTrustAssessment(p TrustPayload, raw json.RawMessage) (TrustAssessment, error) {
	var r TrustAssessment
	if len(raw) > MaxTrustRecordBytes || jsondoc.Decode(raw, &r) != nil || r.SourceID != p.SourceID || r.SourceRevision != p.SourceRevision || r.PolicyName != p.PolicyName || r.PolicyDigest != p.PolicyDigest || r.ExactEntryDigest != p.ExactEntryDigest || r.MediaType != p.MediaType || r.Delivery != p.Delivery || audit.DigestBytes(r.Evidence.Entry) != p.ExactEntryDigest || "sha256:"+r.Assessment.EntryDigest != p.ExactEntryDigest || r.Assessment.CheckedAt.IsZero() || r.SoftwareTrust != "not_assessed" || r.ArtifactIntegrity != "not_assessed" || r.Rights != "not_assessed" {
		return TrustAssessment{}, catalog.E(catalog.CodeInternal, "retained trust assessment binding is invalid")
	}
	bundle, err := json.Marshal(r.Evidence)
	if err != nil {
		return TrustAssessment{}, err
	}
	if _, err = anstrust.DecodeBundle(bundle); err != nil {
		return TrustAssessment{}, catalog.E(catalog.CodeInternal, "trust result evidence exceeds its bounds")
	}
	if r.Authority == "" || len(r.Authority) > 253 || r.Assessment.Profile != anstrust.Profile || !trustDigest("sha256:"+r.Assessment.PolicyDigest) {
		return TrustAssessment{}, catalog.E(catalog.CodeInternal, "trust result verification profile is invalid")
	}
	for _, check := range []anstrust.Check{r.Assessment.Receipt, r.Assessment.Producer, r.Assessment.Log, r.Assessment.Checkpoint, r.Assessment.Inclusion, r.Assessment.Target, r.Assessment.Status} {
		if len(check.Code) == 0 || len(check.Code) > 100 {
			return TrustAssessment{}, catalog.E(catalog.CodeInternal, "trust result check code is invalid")
		}
		switch check.State {
		case "valid", "invalid", "unavailable", "unsupported", "not_assessed":
		default:
			return TrustAssessment{}, catalog.E(catalog.CodeInternal, "trust result check state is invalid")
		}
		if r.Assessment.CurrentRegistration && check.State != "valid" {
			return TrustAssessment{}, catalog.E(catalog.CodeInternal, "current registration requires all checks to pass")
		}
	}
	if r.Assessment.CurrentRegistration && (r.Assessment.ValidUntil == nil || !r.Assessment.ValidUntil.After(r.Assessment.CheckedAt)) {
		return TrustAssessment{}, catalog.E(catalog.CodeInternal, "current registration validity is invalid")
	}
	return r, nil
}

// PrivateTrustJob requires both normal Job and evidence disclosure authority.
func (s *Service) PrivateTrustJob(ctx context.Context, id string) (Job, error) {
	ctx = identity.WithPermissionContext(ctx)
	if _, err := s.Get(ctx, id); err != nil {
		return Job{}, err
	}
	a, _ := identity.FromContext(ctx)
	job, err := s.store.Get(ctx, a.TenantID, id)
	if err != nil {
		return Job{}, err
	}
	if job.Kind != TrustKind {
		return Job{}, catalog.E(catalog.CodeNotFound, "trust assessment was not found")
	}
	return job, nil
}
