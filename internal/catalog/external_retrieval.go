// External retrieval evidence binds fetched representations to their URL,
// content digest and immutable validation result.

package catalog

import (
	"errors"
	"fmt"
	"mime"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ValidateExternalRetrievals validates the bounded immutable JSON aggregate
// stored with one EvidenceSet. Whether it is complete and current for a
// specific VersionRevision is checked separately by the service.
func ValidateExternalRetrievals(retrievals []ExternalRetrieval) error {
	if len(retrievals) > 16 {
		return E(CodeInvalidArgument, "external retrieval evidence exceeds the representation limit")
	}
	seen := make(map[string]struct{}, len(retrievals))
	for _, retrieval := range retrievals {
		if retrieval.RepresentationID == "" || len(retrieval.RepresentationID) > 128 {
			return E(CodeInvalidArgument, "external retrieval representation identity is invalid")
		}
		if _, duplicate := seen[retrieval.RepresentationID]; duplicate {
			return E(CodeInvalidArgument, "external retrieval evidence contains a duplicate representation")
		}
		seen[retrieval.RepresentationID] = struct{}{}
		if !ValidExternalHTTPSURL(retrieval.RequestedURL) || !ValidExternalHTTPSURL(retrieval.FinalURL) || !sameHTTPSOrigin(retrieval.RequestedURL, retrieval.FinalURL) {
			return E(CodeInvalidArgument, "external retrieval URL evidence is invalid")
		}
		if retrieval.Status < 200 || retrieval.Status > 599 || !validFetchedMediaType(retrieval.MediaType) || !sha256DigestPattern.MatchString(retrieval.ContentDigest) || retrieval.Size < 0 || retrieval.Size > MaxCatalogDocumentBytes {
			return E(CodeInvalidArgument, "external retrieval response evidence is invalid")
		}
		if !validExternalPolicyID(retrieval.PolicyID) || retrieval.PolicyRevision <= 0 || !sha256DigestPattern.MatchString(retrieval.PolicyDigest) {
			return E(CodeInvalidArgument, "external retrieval policy evidence is invalid")
		}
		if retrieval.RetrievedAt.IsZero() || retrieval.ExpiresAt.IsZero() || !retrieval.ExpiresAt.Equal(retrieval.RetrievedAt.Add(ExternalRepresentationFreshness)) {
			return E(CodeInvalidArgument, "external retrieval freshness evidence is invalid")
		}
	}
	return nil
}

// ExternalRepresentationBinding contains only the retained delivery identity
// required for collection reads. Complete detail readers separately verify
// Plugin documents and inventory; collections never claim to revalidate bytes.
type ExternalRepresentationBinding struct {
	ID            string
	URL           string
	MediaType     string
	ContentDigest string
	Size          int64
	Retained      bool
}

// ValidateExternalRetrievalEvidence also binds each retrieval aggregate to a
// URL representation in the immutable revision. A blocked run may omit a URL
// whose fetch failed, but it may never introduce evidence for another URL or
// representation.
func ValidateExternalRetrievalEvidence(revision VersionRevision, evidence EvidenceSet) error {
	bindings := make([]ExternalRepresentationBinding, 0, len(revision.Representations))
	for _, r := range revision.Representations {
		if evidence.State == EvidenceComplete && r.URL != "" && r.Plugin != nil && ValidatePluginInspection(r.Plugin) != nil {
			return E(CodeInvalidArgument, "complete external retrieval evidence does not match its retained validation bytes")
		}
		bindings = append(bindings, ExternalRepresentationBinding{ID: r.ID, URL: r.URL, MediaType: r.MediaType, ContentDigest: r.ContentDigest, Size: r.Size, Retained: r.ArtifactID != "" || r.Plugin != nil})
	}
	return ValidateExternalRetrievalBindings(bindings, evidence)
}

// ValidateExternalRetrievalBindings verifies URL, representation, digest, size,
// media type, profile and expiry metadata without loading retained documents.
func ValidateExternalRetrievalBindings(bindings []ExternalRepresentationBinding, evidence EvidenceSet) error {
	if err := ValidateExternalRetrievals(evidence.ExternalRetrievals); err != nil {
		return err
	}
	urlRepresentations := make(map[string]ExternalRepresentationBinding)
	for _, representation := range bindings {
		if representation.URL != "" {
			urlRepresentations[representation.ID] = representation
		}
	}
	for _, retrieval := range evidence.ExternalRetrievals {
		representation, ok := urlRepresentations[retrieval.RepresentationID]
		if !ok || representation.URL != retrieval.RequestedURL {
			return E(CodeInvalidArgument, "external retrieval evidence does not match its version revision")
		}
	}
	if evidence.ValidationProfile != currentValidationProfile {
		return E(CodeInvalidArgument, "unsupported validation evidence profile")
	}

	expiresAt := earliestExternalRetrievalExpiry(evidence.ExternalRetrievals)
	if !evidence.ExpiresAt.Equal(expiresAt) {
		return E(CodeInvalidArgument, "external retrieval expiry does not match its evidence set")
	}
	if evidence.State == EvidenceComplete {
		if len(evidence.ExternalRetrievals) != len(urlRepresentations) {
			return E(CodeInvalidArgument, "complete external retrieval evidence does not cover its version revision")
		}
		for _, retrieval := range evidence.ExternalRetrievals {
			representation := urlRepresentations[retrieval.RepresentationID]
			mediaType, err := representationBaseMediaType(representation.MediaType)
			if err != nil || !representation.Retained || retrieval.Status != 200 || retrieval.MediaType != mediaType && !PluginResponseMediaType(mediaType, retrieval.MediaType) || retrieval.ContentDigest != representation.ContentDigest || retrieval.Size != representation.Size {
				return E(CodeInvalidArgument, "complete external retrieval evidence does not match its retained validation bytes")
			}
		}
	}

	return nil
}

// validFetchedMediaType requires a bounded canonical type/subtype without
// parameters or control characters.
func validFetchedMediaType(value string) bool {
	if value == "" || len(value) > 255 || value != strings.TrimSpace(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(value)
	return err == nil && strings.Contains(mediaType, "/") && len(parameters) == 0 && value == mediaType
}

// validExternalPolicyID reports whether a retained policy identifier uses
// bounded lowercase label syntax.
func validExternalPolicyID(value string) bool {
	if value == "" || len(value) > 63 || value != strings.TrimSpace(value) {
		return false
	}
	for index, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' && index > 0 && index < len(value)-1 {
			continue
		}
		return false
	}
	return true
}

// representationBaseMediaType parses and lowercases a representation
// type/subtype while discarding parameters.
func representationBaseMediaType(value string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || !strings.Contains(mediaType, "/") {
		if err == nil {
			err = errors.New("media type must contain a type and subtype")
		}
		return "", fmt.Errorf("parse representation media type: %w", err)
	}
	return strings.ToLower(mediaType), nil
}

// earliestExternalRetrievalExpiry returns the earliest retained expiry, or zero
// when there are no retrievals.
func earliestExternalRetrievalExpiry(retrievals []ExternalRetrieval) time.Time {
	var earliest time.Time
	for _, retrieval := range retrievals {
		if earliest.IsZero() || retrieval.ExpiresAt.Before(earliest) {
			earliest = retrieval.ExpiresAt
		}
	}
	return earliest
}

// sameHTTPSOrigin compares lowercase HTTPS hosts and normalized ports,
// including the implicit port 443.
func sameHTTPSOrigin(left, right string) bool {
	leftURL, leftErr := url.Parse(left)
	rightURL, rightErr := url.Parse(right)
	if leftErr != nil || rightErr != nil || !strings.EqualFold(leftURL.Scheme, "https") || !strings.EqualFold(rightURL.Scheme, "https") {
		return false
	}
	normalize := func(value *url.URL) string {
		port := value.Port()
		if port == "" {
			port = "443"
		}
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil || portNumber == 0 {
			return ""
		}
		return strings.ToLower(value.Hostname()) + ":" + strconv.FormatUint(portNumber, 10)
	}
	leftOrigin := normalize(leftURL)
	return leftOrigin != "" && leftOrigin == normalize(rightURL)
}
