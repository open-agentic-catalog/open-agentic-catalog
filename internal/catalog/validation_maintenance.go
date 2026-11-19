// Validation maintenance projects current evidence without fetching or renewing it.
package catalog

import (
	"context"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const DefaultValidationHorizonHours = 24
const MaxValidationHorizonHours = 168

// ValidationMaintenancePolicy describes the configured validator, not a grant.
type ValidationMaintenancePolicy struct {
	Profile, Version, ExternalDigest string
}

// ValidationMaintenanceRequest selects a moving, ID-ordered current population.
// Policy is supplied by Service, never by the API caller.
type ValidationMaintenanceRequest struct {
	State, AfterID      string
	HorizonHours, Limit int
	ObservedAt          time.Time
	Policy              ValidationMaintenancePolicy
}

// ValidationMaintenanceItem discloses no document, finding, URL or publication scope.
type ValidationMaintenanceItem struct {
	RevisionID          string        `json:"revisionId"`
	ResourceID          string        `json:"resourceId"`
	NamespaceID         string        `json:"namespaceId"`
	Kind                Kind          `json:"kind"`
	CanonicalIdentifier string        `json:"canonicalIdentifier"`
	VersionID           string        `json:"versionId"`
	Version             string        `json:"version"`
	RevisionNumber      int           `json:"revisionNumber"`
	EvidenceID          string        `json:"evidenceId"`
	EvidenceState       EvidenceState `json:"evidenceState"`
	ValidationProfile   string        `json:"validationProfile"`
	PolicyVersion       string        `json:"policyVersion"`
	ValidatedAt         time.Time     `json:"validatedAt"`
	ExpiresAt           time.Time     `json:"expiresAt,omitzero"`
	Applicable          bool          `json:"applicable"`
	Conditions          []string      `json:"conditions"`
}

// ValidationMaintenanceReader applies permissions, current pointers and filters before its limit.
type ValidationMaintenanceReader interface {
	ListValidationMaintenance(context.Context, string, ValidationMaintenanceRequest) ([]ValidationMaintenanceItem, error)
}

// ValidationMaintenancePolicy returns local configuration without remote discovery.
func (s *Service) ValidationMaintenancePolicy() ValidationMaintenancePolicy {
	p := ValidationMaintenancePolicy{Profile: currentValidationProfile, Version: s.policy}
	if s.externalFetcher != nil {
		p.ExternalDigest = s.externalFetcher.PolicyDigest()
	}
	return p
}

// ListValidationMaintenance requires the intersection of three current scoped
// grants in its repository selection; renewal retains its separate admission.
func (s *Service) ListValidationMaintenance(ctx context.Context, q ValidationMaintenanceRequest) ([]ValidationMaintenanceItem, error) {
	ctx, a, err := collectionContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := ValidateValidationMaintenanceRequest(q); err != nil {
		return nil, err
	}
	q.Policy = s.ValidationMaintenancePolicy()
	q.Limit++
	reader, ok := s.repo.(ValidationMaintenanceReader)
	if !ok {
		return nil, E(CodeDependencyUnavailable, "validation maintenance is unavailable")
	}
	return reader.ListValidationMaintenance(ctx, a.TenantID, q)
}

// ValidateValidationMaintenanceRequest bounds both selection and the fixed time window.
func ValidateValidationMaintenanceRequest(q ValidationMaintenanceRequest) error {
	if q.Limit < 1 || q.Limit > 100 || q.HorizonHours < 1 || q.HorizonHours > MaxValidationHorizonHours || q.ObservedAt.IsZero() || q.AfterID != "" && !identity.ValidTenantID(q.AfterID) {
		return E(CodeInvalidArgument, "validation maintenance requires a 1-100 page size and a 1-168 hour horizon")
	}
	switch q.State {
	case "attention", "expiring", "expired", "blocked", "stale":
		return nil
	}
	return E(CodeInvalidArgument, "validation maintenance state is invalid")
}

// ValidationMaintenanceConditions uses the validator's policy/expiry predicates.
// Conditions overlap: expired evidence may also be blocked or policy-stale.
// Integrity failures are errors, never another kind of stale evidence.
func ValidationMaintenanceConditions(e EvidenceSet, subjectDigest string, q ValidationMaintenanceRequest) ([]string, error) {
	if err := ValidateEvidenceSetIntegrity(e); err != nil {
		return nil, err
	}
	if e.SubjectDigest != subjectDigest {
		return nil, E(CodeDependencyUnavailable, "validation evidence subject does not match its revision")
	}
	return validationMaintenanceConditions(e, q), nil
}

// validationMaintenanceConditions is the metadata preselection shared with the
// SQL predicate; selected rows still require integrity and delivery checks.
func validationMaintenanceConditions(e EvidenceSet, q ValidationMaintenanceRequest) []string {
	conditions := []string{}
	if e.State == EvidenceBlocked {
		conditions = append(conditions, "blocked")
	}
	if !validationPolicyApplicable(e, q.Policy) {
		conditions = append(conditions, "stale")
	}
	if !e.ExpiresAt.IsZero() {
		if !e.ExpiresAt.After(q.ObservedAt) {
			conditions = append(conditions, "expired")
		} else if !e.ExpiresAt.After(q.ObservedAt.Add(time.Duration(q.HorizonHours) * time.Hour)) {
			conditions = append(conditions, "expiring")
		}
	}
	return conditions
}

// validationPolicyApplicable shares the current validator's policy comparisons.
func validationPolicyApplicable(e EvidenceSet, p ValidationMaintenancePolicy) bool {
	if !validationProfilePolicyCurrent(e, p.Profile, p.Version) {
		return false
	}
	for _, r := range e.ExternalRetrievals {
		if !sha256DigestPattern.MatchString(p.ExternalDigest) || r.PolicyDigest != p.ExternalDigest {
			return false
		}
	}
	return true
}

// validationProfilePolicyCurrent is also the review/publication policy predicate.
func validationProfilePolicyCurrent(e EvidenceSet, profile, policy string) bool {
	return e.ValidationProfile == profile && e.PolicyVersion == policy
}

// ProjectValidationMaintenance verifies evidence and returns a minimal projection.
// The owning repository must admit the current subject before calling it.
func ProjectValidationMaintenance(resource Resource, version ResourceVersion, revision VersionRevision, e EvidenceSet, q ValidationMaintenanceRequest) (ValidationMaintenanceItem, error) {
	conditions, err := ValidationMaintenanceConditions(e, revision.ContentSetDigest, q)
	if err != nil {
		return ValidationMaintenanceItem{}, err
	}
	return ValidationMaintenanceItem{RevisionID: revision.ID, ResourceID: resource.ID, NamespaceID: resource.NamespaceID, Kind: resource.Kind, CanonicalIdentifier: resource.CanonicalIdentifier, VersionID: version.ID, Version: version.Version, RevisionNumber: revision.RevisionNumber, EvidenceID: e.ID, EvidenceState: e.State, ValidationProfile: e.ValidationProfile, PolicyVersion: e.PolicyVersion, ValidatedAt: e.CreatedAt, ExpiresAt: e.ExpiresAt, Applicable: e.State == EvidenceComplete && validationPolicyApplicable(e, q.Policy) && (e.ExpiresAt.IsZero() || e.ExpiresAt.After(q.ObservedAt)), Conditions: conditions}, nil
}
