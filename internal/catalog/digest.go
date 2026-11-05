// Revision integrity binds representations, package inspections and validation
// evidence using deterministic service-owned digests.

package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// digestJSON hashes the service's deterministic JSON encoding with a sha256:
// prefix.
func digestJSON(value any) (string, error) {
	canonical, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// digestBytes hashes exact input bytes without normalizing their
// representation.
func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// canonicalJSON uses encoding/json's deterministic map ordering after a
// UseNumber round trip. This helper is for service-owned semantic and
// idempotency digests. A future signature profile that requires JCS must use a
// separate pinned adapter.
func canonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical input: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("decode canonical input: %w", err)
	}
	return json.Marshal(normalized)
}

type contentManifestItem struct {
	Role          RepresentationRole `json:"role"`
	MediaType     string             `json:"mediaType"`
	ProfileID     string             `json:"profileId"`
	URL           string             `json:"url,omitempty"`
	ContentDigest string             `json:"contentDigest"`
	Size          int64              `json:"size"`
	HandoffDigest string             `json:"handoffDigest,omitempty"`
	PackageDigest string             `json:"packageDigest,omitempty"`
}

// contentSetDigest binds representation metadata, content, handoff and package
// evidence independently of input slice ordering.
func contentSetDigest(representations []Representation) (string, error) {
	items := make([]contentManifestItem, 0, len(representations))
	for _, representation := range representations {
		handoffDigest := ""
		if representation.Handoff != nil {
			var err error
			handoffDigest, err = digestJSON(representation.Handoff)
			if err != nil {
				return "", fmt.Errorf("digest representation handoff: %w", err)
			}
		}
		packageDigest := ""
		if representation.Plugin != nil {
			var err error
			packageDigest, err = digestJSON(representation.Plugin)
			if err != nil {
				return "", err
			}
		}
		if representation.Package != nil {
			var err error
			packageDigest, err = digestJSON(representation.Package)
			if err != nil {
				return "", fmt.Errorf("digest representation package inspection: %w", err)
			}
		}
		items = append(items, contentManifestItem{
			Role:          representation.Role,
			MediaType:     representation.MediaType,
			ProfileID:     representation.ProfileID,
			URL:           representation.URL,
			ContentDigest: representation.ContentDigest,
			Size:          representation.Size,
			HandoffDigest: handoffDigest,
			PackageDigest: packageDigest,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Role != items[j].Role {
			return items[i].Role < items[j].Role
		}
		if items[i].MediaType != items[j].MediaType {
			return items[i].MediaType < items[j].MediaType
		}
		if items[i].ProfileID != items[j].ProfileID {
			return items[i].ProfileID < items[j].ProfileID
		}
		if items[i].ContentDigest != items[j].ContentDigest {
			return items[i].ContentDigest < items[j].ContentDigest
		}
		if items[i].URL != items[j].URL {
			return items[i].URL < items[j].URL
		}
		if items[i].HandoffDigest != items[j].HandoffDigest {
			return items[i].HandoffDigest < items[j].HandoffDigest
		}
		if items[i].PackageDigest != items[j].PackageDigest {
			return items[i].PackageDigest < items[j].PackageDigest
		}
		return items[i].Size < items[j].Size
	})
	return digestJSON(items)
}

// ValidateVersionRevisionIntegrity checks the immutable representation-set
// digest and the package and external-retrieval evidence bound to a retained
// revision. It deliberately does not re-run current adapters or fetch remote
// content, because those operations answer current-policy questions rather
// than whether the retained history is internally consistent.
func ValidateVersionRevisionIntegrity(revision VersionRevision, evidence EvidenceSet) error {
	digest, err := contentSetDigest(revision.Representations)
	if err != nil || digest != revision.ContentSetDigest {
		return E(CodeDependencyUnavailable, "version revision failed its content-set integrity check")
	}
	for _, representation := range revision.Representations {
		if err := ValidatePackageRepresentation(representation); err != nil {
			return E(CodeDependencyUnavailable, "version revision package evidence failed its integrity check")
		}
	}
	if evidence.SubjectDigest != revision.ContentSetDigest {
		return E(CodeDependencyUnavailable, "version revision validation subject digest is inconsistent")
	}
	if err := ValidateExternalRetrievalEvidence(revision, evidence); err != nil {
		return E(CodeDependencyUnavailable, "version revision external retrieval evidence is inconsistent")
	}
	return nil
}

// ValidateEvidenceSetIntegrity verifies the current immutable evidence-set
// digest. Unreleased predecessor profiles are not supported.
func ValidateEvidenceSetIntegrity(evidence EvidenceSet) error {
	if !sha256DigestPattern.MatchString(evidence.SubjectDigest) || !sha256DigestPattern.MatchString(evidence.Digest) || evidence.State != EvidenceComplete && evidence.State != EvidenceBlocked || evidence.ValidationProfile == "" || evidence.PolicyVersion == "" || evidence.CreatedAt.IsZero() || !evidence.ExpiresAt.IsZero() && evidence.ExpiresAt.Before(evidence.CreatedAt) {
		return E(CodeDependencyUnavailable, "validation evidence envelope is invalid")
	}
	if err := ValidateExternalRetrievals(evidence.ExternalRetrievals); err != nil {
		return E(CodeDependencyUnavailable, "validation retrieval evidence is invalid")
	}
	if evidence.ValidationProfile != currentValidationProfile {
		return E(CodeDependencyUnavailable, "unsupported validation evidence profile")
	}
	var expiresAt any
	if !evidence.ExpiresAt.IsZero() {
		expiresAt = evidence.ExpiresAt
	}
	findings := evidence.Findings
	if findings == nil {
		findings = []Finding{}
	}
	retrievals := evidence.ExternalRetrievals
	if retrievals == nil {
		retrievals = []ExternalRetrieval{}
	}
	digest, digestErr := digestJSON(map[string]any{
		"runId":              evidence.ID,
		"subjectDigest":      evidence.SubjectDigest,
		"state":              evidence.State,
		"findings":           findings,
		"validationProfile":  evidence.ValidationProfile,
		"policyVersion":      evidence.PolicyVersion,
		"legalRights":        evidence.LegalRights,
		"trust":              evidence.Trust,
		"externalRetrievals": retrievals,
		"createdAt":          evidence.CreatedAt,
		"expiresAt":          expiresAt,
	})
	if digestErr != nil || digest != evidence.Digest {
		return E(CodeDependencyUnavailable, "validation evidence failed its digest integrity check")
	}
	return nil
}
