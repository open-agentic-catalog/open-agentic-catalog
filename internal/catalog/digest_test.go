package catalog

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestCanonicalJSONIsStableAndRejectsUnsupportedValues(t *testing.T) {
	left, err := canonicalJSON(map[string]any{"z": 1, "a": jsonNumber("1.00")})
	if err != nil {
		t.Fatal(err)
	}
	right, err := canonicalJSON(map[string]any{"a": jsonNumber("1.00"), "z": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatalf("canonical JSON depends on map insertion order: %s != %s", left, right)
	}
	if _, err := canonicalJSON(make(chan struct{})); err == nil {
		t.Fatal("unsupported canonical value was accepted")
	}
	if _, err := digestJSON(make(chan struct{})); err == nil {
		t.Fatal("unsupported digest value was accepted")
	}
}

type jsonNumber string

func (n jsonNumber) MarshalJSON() ([]byte, error) { return []byte(n), nil }

func TestContentSetDigestIsIndependentOfRepresentationOrder(t *testing.T) {
	t.Parallel()

	representations := []Representation{
		{Role: RepresentationDistribution, MediaType: "application/json", ProfileID: "profile", URL: "https://artifacts.example.com/b", ContentDigest: "sha256:shared", Size: 20},
		{Role: RepresentationDistribution, MediaType: "application/json", ProfileID: "profile", URL: "https://artifacts.example.com/a", ContentDigest: "sha256:shared", Size: 10},
		{Role: RepresentationDistribution, MediaType: "application/json", ProfileID: "profile", URL: "https://artifacts.example.com/a", ContentDigest: "sha256:shared", Size: 20},
		{Role: RepresentationDistribution, MediaType: "application/yaml", ProfileID: "profile", ContentDigest: "sha256:shared"},
		{Role: RepresentationDistribution, MediaType: "application/json", ProfileID: "other", ContentDigest: "sha256:shared"},
		{Role: RepresentationDistribution, MediaType: "application/json", ProfileID: "profile", ContentDigest: "sha256:other"},
		{Role: RepresentationDistribution, MediaType: "application/json", ProfileID: "profile", URL: "https://artifacts.example.com/a", ContentDigest: "sha256:shared", Size: 20, Handoff: &SkillHandoff{OCI: &OCIHandoffReference{Reference: "registry.example.com/team/skill@sha256:" + strings.Repeat("a", 64)}}},
		{Role: RepresentationDistribution, MediaType: "application/json", ProfileID: "profile", URL: "https://artifacts.example.com/a", ContentDigest: "sha256:shared", Size: 20, Package: &SkillPackageInspection{RootDirectory: "skill"}},
		{Role: RepresentationPrimary, MediaType: "application/yaml", ProfileID: "other", ContentDigest: "sha256:other", Size: 30},
	}
	forward, err := contentSetDigest(representations)
	if err != nil {
		t.Fatal(err)
	}
	reversed := append([]Representation(nil), representations...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	backward, err := contentSetDigest(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if backward != forward {
		t.Fatalf("content-set digest depends on representation order: %s != %s", backward, forward)
	}
}

func TestContentSetDigestBindsEveryManifestField(t *testing.T) {
	t.Parallel()

	base := Representation{Role: RepresentationPrimary, MediaType: "application/json", ProfileID: "profile", URL: "https://artifacts.example.com/a", ContentDigest: "sha256:content", Size: 10}
	want, err := contentSetDigest([]Representation{base})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Representation){
		"role":           func(value *Representation) { value.Role = RepresentationMetadata },
		"media type":     func(value *Representation) { value.MediaType = "application/yaml" },
		"profile":        func(value *Representation) { value.ProfileID = "other" },
		"URL":            func(value *Representation) { value.URL = "https://artifacts.example.com/b" },
		"content digest": func(value *Representation) { value.ContentDigest = "sha256:other" },
		"size":           func(value *Representation) { value.Size++ },
		"handoff": func(value *Representation) {
			value.Handoff = &SkillHandoff{OCI: &OCIHandoffReference{Reference: "registry.example.com/team/skill@sha256:" + strings.Repeat("a", 64)}}
		},
		"package inspection": func(value *Representation) {
			value.Package = &SkillPackageInspection{RootDirectory: "safe-skill", Files: []SkillPackageFile{{Path: "safe-skill/SKILL.md", Digest: "sha256:" + strings.Repeat("b", 64)}}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			changed := base
			mutate(&changed)
			got, digestErr := contentSetDigest([]Representation{changed})
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			if got == want {
				t.Fatalf("changing %s did not change the content-set digest", name)
			}
		})
	}
}

func TestSkillPackageRevisionDigestBindsInventoryAndHandoff(t *testing.T) {
	t.Parallel()
	representation := validSkillPackageRepresentation()
	digest, err := contentSetDigest([]Representation{representation})
	if err != nil {
		t.Fatal(err)
	}
	revision := VersionRevision{Representations: []Representation{representation}, ContentSetDigest: digest}
	if err := ValidatePackageRevisionDigest(revision); err != nil {
		t.Fatal(err)
	}
	revision.Representations[0] = cloneSkillPackageRepresentation(representation)
	revision.Representations[0].Package.Files[0].Digest = "sha256:" + strings.Repeat("c", 64)
	if err := ValidatePackageRevisionDigest(revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("changed package inventory returned %v", err)
	}
}

func TestRetainedCatalogDigestValidators(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	representation := Representation{ID: "representation", Role: RepresentationPrimary, MediaType: "text/plain", ProfileID: "agent-skill-markdown", ContentDigest: "sha256:" + strings.Repeat("a", 64), Size: 7, ArtifactID: strings.Repeat("a", 64), ValidationState: "valid"}
	contentDigest, err := contentSetDigest([]Representation{representation})
	if err != nil {
		t.Fatal(err)
	}
	evidence := EvidenceSet{
		ID:                 "00000000-0000-7000-8000-000000000001",
		TenantID:           "00000000-0000-7000-8000-000000000002",
		SubjectDigest:      contentDigest,
		State:              EvidenceComplete,
		Findings:           []Finding{},
		ValidationProfile:  currentValidationProfile,
		PolicyVersion:      "catalog-policy-v1",
		LegalRights:        LegalRightsSummary{State: LegalRightsNotAssessed},
		Trust:              TrustSummary{State: TrustNotAssessed},
		ExternalRetrievals: []ExternalRetrieval{},
		CreatedAt:          now,
	}
	evidence.Digest, err = digestJSON(map[string]any{
		"runId":              evidence.ID,
		"subjectDigest":      evidence.SubjectDigest,
		"state":              evidence.State,
		"findings":           evidence.Findings,
		"validationProfile":  evidence.ValidationProfile,
		"policyVersion":      evidence.PolicyVersion,
		"legalRights":        evidence.LegalRights,
		"trust":              evidence.Trust,
		"externalRetrievals": evidence.ExternalRetrievals,
		"createdAt":          evidence.CreatedAt,
		"expiresAt":          nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateEvidenceSetIntegrity(evidence); err != nil {
		t.Fatalf("current validation evidence = %v", err)
	}
	revision := VersionRevision{Representations: []Representation{representation}, ContentSetDigest: contentDigest}
	if err := ValidateVersionRevisionIntegrity(revision, evidence); err != nil {
		t.Fatalf("valid retained revision returned %v", err)
	}

	invalidEvidence := evidence
	invalidEvidence.Digest = "sha256:" + strings.Repeat("b", 64)
	if err := ValidateEvidenceSetIntegrity(invalidEvidence); err == nil {
		t.Fatalf("tampered current evidence = %v", err)
	}
	legacyEvidence := evidence
	legacyEvidence.ValidationProfile = "catalog-validation-v6"
	if err := ValidateEvidenceSetIntegrity(legacyEvidence); err == nil {
		t.Fatalf("legacy validation evidence = %v", err)
	}
	for name, mutate := range map[string]func(*EvidenceSet){
		"legacy digest":  func(value *EvidenceSet) { value.Digest = "not-a-digest" },
		"subject digest": func(value *EvidenceSet) { value.SubjectDigest = "not-a-digest" },
		"state":          func(value *EvidenceSet) { value.State = "unknown" },
		"creation time":  func(value *EvidenceSet) { value.CreatedAt = time.Time{} },
		"expiry":         func(value *EvidenceSet) { value.ExpiresAt = value.CreatedAt.Add(-time.Second) },
	} {
		t.Run("invalid "+name, func(t *testing.T) {
			changed := legacyEvidence
			mutate(&changed)
			if err := ValidateEvidenceSetIntegrity(changed); err == nil {
				t.Fatalf("invalid legacy evidence = %v", err)
			}
		})
	}
	invalidRevision := revision
	invalidRevision.ContentSetDigest = "sha256:" + strings.Repeat("c", 64)
	if err := ValidateVersionRevisionIntegrity(invalidRevision, evidence); err == nil {
		t.Fatal("tampered retained revision was accepted")
	}
	invalidPackageRevision := revision
	invalidPackageRevision.Representations = append([]Representation(nil), revision.Representations...)
	invalidPackageRevision.Representations[0].Handoff = &SkillHandoff{}
	invalidPackageRevision.ContentSetDigest, err = contentSetDigest(invalidPackageRevision.Representations)
	if err != nil {
		t.Fatal(err)
	}
	invalidPackageEvidence := evidence
	invalidPackageEvidence.SubjectDigest = invalidPackageRevision.ContentSetDigest
	if err := ValidateVersionRevisionIntegrity(invalidPackageRevision, invalidPackageEvidence); err == nil {
		t.Fatal("invalid retained package evidence was accepted")
	}
	mismatchedSubject := evidence
	mismatchedSubject.SubjectDigest = "sha256:" + strings.Repeat("d", 64)
	if err := ValidateVersionRevisionIntegrity(revision, mismatchedSubject); err == nil {
		t.Fatal("mismatched retained validation subject was accepted")
	}
	invalidRetrieval := evidence
	invalidRetrieval.ExternalRetrievals = []ExternalRetrieval{{}}
	if err := ValidateVersionRevisionIntegrity(revision, invalidRetrieval); err == nil {
		t.Fatal("invalid retained external retrieval was accepted")
	}

	nilCollections := evidence
	nilCollections.Findings = nil
	nilCollections.ExternalRetrievals = nil
	nilCollections.ExpiresAt = now.Add(time.Hour)
	nilCollections.Digest, err = digestJSON(map[string]any{
		"runId":              nilCollections.ID,
		"subjectDigest":      nilCollections.SubjectDigest,
		"state":              nilCollections.State,
		"findings":           []Finding{},
		"validationProfile":  nilCollections.ValidationProfile,
		"policyVersion":      nilCollections.PolicyVersion,
		"legalRights":        nilCollections.LegalRights,
		"trust":              nilCollections.Trust,
		"externalRetrievals": []ExternalRetrieval{},
		"createdAt":          nilCollections.CreatedAt,
		"expiresAt":          nilCollections.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateEvidenceSetIntegrity(nilCollections); err != nil {
		t.Fatalf("current evidence with normalized empty collections = %v", err)
	}
	invalidEvidenceRetrieval := evidence
	invalidEvidenceRetrieval.ExternalRetrievals = []ExternalRetrieval{{}}
	if err := ValidateEvidenceSetIntegrity(invalidEvidenceRetrieval); err == nil {
		t.Fatalf("invalid evidence retrieval = %v", err)
	}

	listing := ListingRevision{VersionRevisionID: "revision", DisplayName: "Example", Description: "Retained metadata", Tags: []string{"one"}, Categories: []string{}, RepresentativeQueries: []string{"find example"}, PublisherID: "publisher", Visibility: VisibilityTenant, Relationships: []Relationship{}, Extensions: map[string]any{}}
	listing.Digest, err = digestJSON(revisionDigestInput(listing))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateListingRevisionDigest(listing); err != nil {
		t.Fatalf("valid retained listing returned %v", err)
	}
	listing.DisplayName = "Changed"
	if err := ValidateListingRevisionDigest(listing); err == nil {
		t.Fatal("tampered retained listing was accepted")
	}
}
