package catalog

import (
	"strings"
	"testing"
	"time"
)

func TestValidateExternalRetrievalsRejectsMalformedOrUnboundedEvidence(t *testing.T) {
	t.Parallel()

	retrievedAt := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	valid := ExternalRetrieval{
		RepresentationID: "representation",
		RequestedURL:     "https://content.example.com/card.json",
		FinalURL:         "https://CONTENT.example.com:443/cards/current.json",
		Status:           200,
		MediaType:        "application/json",
		ContentDigest:    "sha256:" + strings.Repeat("a", 64),
		Size:             128,
		PolicyID:         "production-policy",
		PolicyRevision:   2,
		PolicyDigest:     "sha256:" + strings.Repeat("b", 64),
		RetrievedAt:      retrievedAt,
		ExpiresAt:        retrievedAt.Add(ExternalRepresentationFreshness),
	}
	if err := ValidateExternalRetrievals(nil); err != nil {
		t.Fatalf("empty retrieval evidence returned %v", err)
	}
	if err := ValidateExternalRetrievals([]ExternalRetrieval{valid}); err != nil {
		t.Fatalf("valid retrieval evidence returned %v", err)
	}

	tests := map[string]func(*ExternalRetrieval){
		"representation":  func(value *ExternalRetrieval) { value.RepresentationID = "" },
		"requested URL":   func(value *ExternalRetrieval) { value.RequestedURL = "http://content.example.com/card.json" },
		"final origin":    func(value *ExternalRetrieval) { value.FinalURL = "https://other.example.com/card.json" },
		"status":          func(value *ExternalRetrieval) { value.Status = 199 },
		"media type":      func(value *ExternalRetrieval) { value.MediaType = "application/json; charset=utf-8" },
		"content digest":  func(value *ExternalRetrieval) { value.ContentDigest = "sha256:invalid" },
		"negative size":   func(value *ExternalRetrieval) { value.Size = -1 },
		"oversized":       func(value *ExternalRetrieval) { value.Size = MaxCatalogDocumentBytes + 1 },
		"policy ID":       func(value *ExternalRetrieval) { value.PolicyID = "Production" },
		"policy revision": func(value *ExternalRetrieval) { value.PolicyRevision = 0 },
		"policy digest":   func(value *ExternalRetrieval) { value.PolicyDigest = "" },
		"retrieved at":    func(value *ExternalRetrieval) { value.RetrievedAt = time.Time{} },
		"expiry":          func(value *ExternalRetrieval) { value.ExpiresAt = value.ExpiresAt.Add(time.Second) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := valid
			mutate(&candidate)
			if ErrorCodeOf(ValidateExternalRetrievals([]ExternalRetrieval{candidate})) != CodeInvalidArgument {
				t.Fatalf("invalid %s evidence was accepted", name)
			}
		})
	}
	duplicate := valid
	if ErrorCodeOf(ValidateExternalRetrievals([]ExternalRetrieval{valid, duplicate})) != CodeInvalidArgument {
		t.Fatal("duplicate representation retrieval was accepted")
	}
	overLimit := make([]ExternalRetrieval, 17)
	for index := range overLimit {
		overLimit[index] = valid
		overLimit[index].RepresentationID = string(rune('a' + index))
	}
	if ErrorCodeOf(ValidateExternalRetrievals(overLimit)) != CodeInvalidArgument {
		t.Fatal("over-limit retrieval evidence was accepted")
	}
}

func TestSameHTTPSOriginNormalizesDefaultPortOnly(t *testing.T) {
	t.Parallel()

	if !sameHTTPSOrigin("https://EXAMPLE.com/path", "https://example.com:443/other") {
		t.Fatal("equivalent HTTPS origins did not match")
	}
	for _, candidate := range []string{"http://example.com/other", "https://example.com:444/other", "https://other.example/other", "invalid"} {
		if sameHTTPSOrigin("https://example.com/path", candidate) {
			t.Fatalf("different origin %q matched", candidate)
		}
	}
}

func TestExternalRetrievalMetadataHelpersRejectAmbiguousValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"application/json", "application/vnd.example+json"} {
		if !validFetchedMediaType(value) {
			t.Fatalf("valid fetched media type %q was rejected", value)
		}
	}
	for _, value := range []string{"", " application/json", "Application/JSON", "application/json; charset=utf-8", "invalid", "application/\x00json", strings.Repeat("a", 256)} {
		if validFetchedMediaType(value) {
			t.Fatalf("ambiguous fetched media type %q was accepted", value)
		}
	}
	if mediaType, err := representationBaseMediaType("Application/JSON; charset=utf-8"); err != nil || mediaType != "application/json" {
		t.Fatalf("representation base media type = %q, %v", mediaType, err)
	}
	if _, err := representationBaseMediaType("invalid"); err == nil {
		t.Fatal("malformed representation media type was accepted")
	}

	base := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	if expiry := earliestExternalRetrievalExpiry(nil); !expiry.IsZero() {
		t.Fatalf("empty retrieval expiry = %s", expiry)
	}
	if expiry := earliestExternalRetrievalExpiry([]ExternalRetrieval{{ExpiresAt: base.Add(time.Hour)}, {ExpiresAt: base}, {ExpiresAt: base.Add(2 * time.Hour)}}); !expiry.Equal(base) {
		t.Fatalf("earliest retrieval expiry = %s, want %s", expiry, base)
	}
}

func TestValidateExternalRetrievalEvidenceBindsRevisionAndFreshness(t *testing.T) {
	t.Parallel()

	retrievedAt := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	url := "https://content.example.com/card.json"
	digest := "sha256:" + strings.Repeat("a", 64)
	revision := VersionRevision{Representations: []Representation{{ID: "representation", URL: url, MediaType: "application/json", ContentDigest: digest, Size: 128, ArtifactID: "artifact"}}}
	retrieval := ExternalRetrieval{
		RepresentationID: "representation",
		RequestedURL:     url,
		FinalURL:         url,
		Status:           200,
		MediaType:        "application/json",
		ContentDigest:    digest,
		Size:             128,
		PolicyID:         "production-policy",
		PolicyRevision:   2,
		PolicyDigest:     "sha256:" + strings.Repeat("b", 64),
		RetrievedAt:      retrievedAt,
		ExpiresAt:        retrievedAt.Add(ExternalRepresentationFreshness),
	}
	evidence := EvidenceSet{State: EvidenceComplete, ValidationProfile: currentValidationProfile, ExternalRetrievals: []ExternalRetrieval{retrieval}, ExpiresAt: retrieval.ExpiresAt}
	if err := ValidateExternalRetrievalEvidence(revision, evidence); err != nil {
		t.Fatalf("valid revision-bound evidence returned %v", err)
	}

	wrongRepresentation := evidence
	wrongRepresentation.ExternalRetrievals = append([]ExternalRetrieval(nil), evidence.ExternalRetrievals...)
	wrongRepresentation.ExternalRetrievals[0].RepresentationID = "other"
	if ErrorCodeOf(ValidateExternalRetrievalEvidence(revision, wrongRepresentation)) != CodeInvalidArgument {
		t.Fatal("retrieval evidence for another representation was accepted")
	}
	wrongURL := evidence
	wrongURL.ExternalRetrievals = append([]ExternalRetrieval(nil), evidence.ExternalRetrievals...)
	wrongURL.ExternalRetrievals[0].RequestedURL = "https://content.example.com/other.json"
	wrongURL.ExternalRetrievals[0].FinalURL = wrongURL.ExternalRetrievals[0].RequestedURL
	if ErrorCodeOf(ValidateExternalRetrievalEvidence(revision, wrongURL)) != CodeInvalidArgument {
		t.Fatal("retrieval evidence for another URL was accepted")
	}
	wrongExpiry := evidence
	wrongExpiry.ExpiresAt = wrongExpiry.ExpiresAt.Add(time.Second)
	if ErrorCodeOf(ValidateExternalRetrievalEvidence(revision, wrongExpiry)) != CodeInvalidArgument {
		t.Fatal("evidence-set expiry outside its retrieval aggregate was accepted")
	}
	for name, mutate := range map[string]func(*ExternalRetrieval){
		"status":     func(value *ExternalRetrieval) { value.Status = 404 },
		"media type": func(value *ExternalRetrieval) { value.MediaType = "text/plain" },
		"digest":     func(value *ExternalRetrieval) { value.ContentDigest = "sha256:" + strings.Repeat("c", 64) },
		"size":       func(value *ExternalRetrieval) { value.Size++ },
	} {
		t.Run("complete "+name, func(t *testing.T) {
			t.Parallel()
			candidate := evidence
			candidate.ExternalRetrievals = append([]ExternalRetrieval(nil), evidence.ExternalRetrievals...)
			mutate(&candidate.ExternalRetrievals[0])
			if ErrorCodeOf(ValidateExternalRetrievalEvidence(revision, candidate)) != CodeInvalidArgument {
				t.Fatalf("complete evidence with mismatched %s was accepted", name)
			}
		})
	}
	missing := evidence
	missing.ExternalRetrievals = nil
	missing.ExpiresAt = time.Time{}
	if ErrorCodeOf(ValidateExternalRetrievalEvidence(revision, missing)) != CodeInvalidArgument {
		t.Fatal("complete evidence missing its URL retrieval was accepted")
	}
	blockedDrift := evidence
	blockedDrift.State = EvidenceBlocked
	blockedDrift.ExternalRetrievals = append([]ExternalRetrieval(nil), evidence.ExternalRetrievals...)
	blockedDrift.ExternalRetrievals[0].ContentDigest = "sha256:" + strings.Repeat("c", 64)
	if err := ValidateExternalRetrievalEvidence(revision, blockedDrift); err != nil {
		t.Fatalf("blocked drift evidence returned %v", err)
	}
	if err := ValidateExternalRetrievalEvidence(revision, EvidenceSet{State: EvidenceBlocked, ValidationProfile: currentValidationProfile}); err != nil {
		t.Fatalf("blocked fetch with no retrieval aggregate returned %v", err)
	}
	if err := ValidateExternalRetrievalEvidence(VersionRevision{}, EvidenceSet{ValidationProfile: "catalog-validation-v5", ExpiresAt: retrievedAt}); err == nil {
		t.Fatal("obsolete validation profile was accepted")
	}
}
