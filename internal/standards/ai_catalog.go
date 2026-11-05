// AI Catalog validation enforces envelope and entry semantics beyond structural
// JSON validity while separating consumer and producer-level requirements.

package standards

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

var aiCatalogVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// ValidateAICatalog validates the normative Level 1 document and entry rules
// and accepts forward-compatible minor versions within major version 1.
func ValidateAICatalog(raw []byte) error {
	return validateAICatalog(raw, false)
}

// ValidateAICatalogLevel2 validates a document produced by this service. It
// additionally requires the Level 2 host identity and exact 1.0 producer
// version implemented by the pinned profile.
func ValidateAICatalogLevel2(raw []byte) error {
	return validateAICatalog(raw, true)
}

// validateAICatalog applies consumer envelope/entry rules and enables exact
// producer-level host requirements when producer is true.
func validateAICatalog(raw []byte, producer bool) error {
	if err := jsondoc.Validate(raw); err != nil {
		return errors.New("AI Catalog is not unambiguous JSON")
	}
	var document map[string]json.RawMessage
	if err := decodeJSON(raw, &document); err != nil || document == nil {
		return errors.New("AI Catalog must be a JSON object")
	}
	var specVersion string
	if err := requiredJSONField(document, "specVersion", &specVersion); err != nil {
		return err
	}
	versionParts := aiCatalogVersionPattern.FindStringSubmatch(specVersion)
	if versionParts == nil {
		return errors.New("AI Catalog specVersion must use Major.Minor format")
	}
	major, _ := strconv.Atoi(versionParts[1])
	if major != 1 {
		return fmt.Errorf("AI Catalog major version %d is not supported", major)
	}
	if producer && specVersion != "1.0" {
		return errors.New("AI Catalog producer profile must emit specVersion 1.0")
	}

	if hostRaw, ok := document["host"]; ok {
		if err := validateAICatalogHost(hostRaw); err != nil {
			return err
		}
	} else if producer {
		return errors.New("AI Catalog Level 2 publication requires host")
	}
	if extensions, ok := document["extensions"]; ok {
		if err := validateAICatalogExtensions(extensions); err != nil {
			return err
		}
	}
	if signature, ok := document["signature"]; ok {
		if err := validateDetachedJWS(signature); err != nil {
			return fmt.Errorf("AI Catalog signature: %w", err)
		}
	}

	var entries []json.RawMessage
	if err := requiredJSONField(document, "entries", &entries); err != nil {
		return err
	}
	if entries == nil {
		return errors.New("entries must be an array")
	}
	seenVersions := make(map[string]struct{}, len(entries))
	seenUnversioned := make(map[string]struct{}, len(entries))
	seenIdentifiers := make(map[string]struct{}, len(entries))
	for index, rawEntry := range entries {
		identifier, version, err := validateAICatalogEntry(rawEntry)
		if err != nil {
			return fmt.Errorf("AI Catalog entry %d: %w", index, err)
		}
		if version == "" {
			if _, exists := seenIdentifiers[identifier]; exists {
				return fmt.Errorf("AI Catalog entry %d duplicates unversioned identifier %q", index, identifier)
			}
			seenUnversioned[identifier] = struct{}{}
			seenIdentifiers[identifier] = struct{}{}
			continue
		}
		if _, exists := seenUnversioned[identifier]; exists {
			return fmt.Errorf("AI Catalog entry %d conflicts with unversioned identifier %q", index, identifier)
		}
		key := identifier + "\x00" + version
		if _, exists := seenVersions[key]; exists {
			return fmt.Errorf("AI Catalog entry %d duplicates identifier and version", index)
		}
		seenVersions[key] = struct{}{}
		seenIdentifiers[identifier] = struct{}{}
	}
	return nil
}

// validateAICatalogHost checks the required host identity and declared
// discovery-interface metadata.
func validateAICatalogHost(raw json.RawMessage) error {
	var host map[string]json.RawMessage
	if err := decodeJSON(raw, &host); err != nil || host == nil {
		return errors.New("AI Catalog host must be an object")
	}
	var displayName string
	if err := requiredJSONField(host, "displayName", &displayName); err != nil || strings.TrimSpace(displayName) == "" {
		return errors.New("AI Catalog host.displayName must be a non-empty string")
	}
	for _, field := range []string{"identifier", "documentationUrl", "logoUrl"} {
		if value, ok := host[field]; ok {
			var text string
			if err := decodeJSON(value, &text); err != nil || strings.TrimSpace(text) == "" {
				return fmt.Errorf("AI Catalog host.%s must be a non-empty string", field)
			}
			if (field == "documentationUrl" || field == "logoUrl") && !absoluteURI(text) {
				return fmt.Errorf("AI Catalog host.%s must be an absolute URI", field)
			}
		}
	}
	if trustManifest, ok := host["trustManifest"]; ok {
		if err := validateAICatalogTrustManifest(trustManifest, "", "", ""); err != nil {
			return fmt.Errorf("AI Catalog host.trustManifest: %w", err)
		}
	}
	return nil
}

// validateAICatalogEntry validates one entry and returns its identity/type
// binding for envelope-level checks.
func validateAICatalogEntry(raw json.RawMessage) (string, string, error) {
	var entry map[string]json.RawMessage
	if err := decodeJSON(raw, &entry); err != nil || entry == nil {
		return "", "", errors.New("entry must be an object")
	}
	var identifier string
	if err := requiredJSONField(entry, "identifier", &identifier); err != nil || strings.TrimSpace(identifier) == "" {
		return "", "", errors.New("identifier must be a non-empty string")
	}
	var mediaType string
	if err := requiredJSONField(entry, "type", &mediaType); err != nil || strings.TrimSpace(mediaType) == "" {
		return "", "", errors.New("type must be a non-empty string")
	}
	_, hasURL := entry["url"]
	_, hasData := entry["data"]
	if hasURL == hasData {
		return "", "", errors.New("exactly one of url or data is required")
	}
	if hasURL {
		var artifactURL string
		if err := decodeJSON(entry["url"], &artifactURL); err != nil || !absoluteURI(artifactURL) {
			return "", "", errors.New("url must be an absolute URI")
		}
	}
	for _, field := range []string{"displayName", "description"} {
		if value, ok := entry[field]; ok {
			if err := validateOptionalString(value, false); err != nil {
				return "", "", fmt.Errorf("%s must be a string", field)
			}
		}
	}
	var version string
	if value, ok := entry["version"]; ok {
		if err := decodeJSON(value, &version); err != nil || strings.TrimSpace(version) == "" {
			return "", "", errors.New("version must be a non-empty string when present")
		}
	}
	if value, ok := entry["updatedAt"]; ok {
		var updatedAt string
		if err := decodeJSON(value, &updatedAt); err != nil {
			return "", "", errors.New("updatedAt must be an RFC 3339 string")
		}
		if _, err := time.Parse(time.RFC3339Nano, updatedAt); err != nil {
			return "", "", errors.New("updatedAt must be an RFC 3339 string")
		}
	}
	if value, ok := entry["tags"]; ok {
		if err := validateStringArray(value); err != nil {
			return "", "", errors.New("tags must be an array of strings")
		}
	}
	if value, ok := entry["publisher"]; ok {
		if err := validateAICatalogPublisher(value); err != nil {
			return "", "", err
		}
	}
	if value, ok := entry["extensions"]; ok {
		if err := validateAICatalogExtensions(value); err != nil {
			return "", "", err
		}
	}
	if value, ok := entry["trustManifest"]; ok {
		artifactURL := ""
		if hasURL {
			_ = decodeJSON(entry["url"], &artifactURL)
		}
		if err := validateAICatalogTrustManifest(value, identifier, mediaType, artifactURL); err != nil {
			return "", "", fmt.Errorf("trustManifest: %w", err)
		}
	}
	return identifier, version, nil
}

// validateAICatalogPublisher checks the optional publisher object's required
// identity fields and reference shapes.
func validateAICatalogPublisher(raw json.RawMessage) error {
	var publisher map[string]json.RawMessage
	if err := decodeJSON(raw, &publisher); err != nil || publisher == nil {
		return errors.New("publisher must be an object")
	}
	for _, field := range []string{"identifier", "displayName"} {
		var value string
		if err := requiredJSONField(publisher, field, &value); err != nil || strings.TrimSpace(value) == "" {
			return fmt.Errorf("publisher.%s must be a non-empty string", field)
		}
	}
	if rawIdentityType, ok := publisher["identityType"]; ok {
		if err := validateOptionalString(rawIdentityType, false); err != nil {
			return errors.New("publisher.identityType must be a string")
		}
	}
	return nil
}

// validateAICatalogExtensions requires a namespaced extension map without
// treating extension content as service control fields.
func validateAICatalogExtensions(raw json.RawMessage) error {
	var extensions map[string]json.RawMessage
	if err := decodeJSON(raw, &extensions); err != nil || extensions == nil {
		return errors.New("extensions must be an object")
	}
	for key := range extensions {
		if !validExtensionKey(key) {
			return fmt.Errorf("AI Catalog extension key %q is not a URL or reverse-DNS name", key)
		}
	}
	return nil
}

// validateAICatalogTrustManifest validates declared trust metadata and its
// binding to the containing entry without establishing cryptographic trust.
func validateAICatalogTrustManifest(raw json.RawMessage, entryIdentifier, entryType, entryURL string) error {
	var manifest map[string]json.RawMessage
	if err := decodeJSON(raw, &manifest); err != nil || manifest == nil {
		return errors.New("must be an object")
	}
	var identity string
	if err := requiredJSONField(manifest, "identity", &identity); err != nil || !absoluteURI(identity) {
		return errors.New("identity must be a globally unique absolute URI")
	}
	if entryIdentifier != "" && !aiCatalogTrustDomainAligned(entryIdentifier, identity) {
		return errors.New("identity trust domain does not align with the entry publisher domain")
	}
	if value, ok := manifest["identityType"]; ok {
		if err := validateOptionalString(value, false); err != nil {
			return fmt.Errorf("identityType: %w", err)
		}
	}
	substantive := false
	if value, ok := manifest["trustSchema"]; ok {
		if err := validateAICatalogTrustSchema(value); err != nil {
			return err
		}
		substantive = true
	}
	for _, collection := range []struct {
		name string
		fn   func(json.RawMessage) error
	}{{"attestations", validateAICatalogAttestations}, {"provenance", validateAICatalogProvenance}} {
		if value, ok := manifest[collection.name]; ok {
			var items []json.RawMessage
			if err := decodeJSON(value, &items); err != nil {
				return fmt.Errorf("%s must be an array", collection.name)
			}
			if len(items) > 0 {
				substantive = true
			}
			if err := collection.fn(value); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"privacyPolicyUrl", "termsOfServiceUrl"} {
		if value, ok := manifest[field]; ok {
			var uri string
			if err := decodeJSON(value, &uri); err != nil || !absoluteURI(uri) {
				return fmt.Errorf("%s must be an absolute URI", field)
			}
		}
	}
	for _, field := range []string{"issuedAt", "expiresAt"} {
		if value, ok := manifest[field]; ok {
			if err := validateRFC3339(value); err != nil {
				return fmt.Errorf("%s must be an RFC 3339 timestamp", field)
			}
		}
	}
	if value, ok := manifest["subject"]; ok {
		if err := validateAICatalogSubject(value, entryType, entryURL, entryIdentifier != ""); err != nil {
			return err
		}
	}
	if value, ok := manifest["extensions"]; ok {
		if err := validateAICatalogExtensions(value); err != nil {
			return err
		}
	}
	if signature, ok := manifest["signature"]; ok {
		if _, hasSubject := manifest["subject"]; !hasSubject {
			return errors.New("a signed Trust Manifest requires subject")
		}
		if _, hasIssuedAt := manifest["issuedAt"]; !hasIssuedAt {
			return errors.New("a signed Trust Manifest requires issuedAt")
		}
		if err := validateDetachedJWS(signature); err != nil {
			return fmt.Errorf("signature: %w", err)
		}
		substantive = true
	}
	if !substantive {
		return errors.New("must contain substantive trust evidence")
	}
	return nil
}

// validateAICatalogTrustSchema checks the declared trust-schema reference and
// supported metadata types.
func validateAICatalogTrustSchema(raw json.RawMessage) error {
	var schema map[string]json.RawMessage
	if err := decodeJSON(raw, &schema); err != nil || schema == nil {
		return errors.New("trustSchema must be an object")
	}
	for _, field := range []string{"identifier", "version"} {
		var value string
		if err := requiredJSONField(schema, field, &value); err != nil || strings.TrimSpace(value) == "" {
			return fmt.Errorf("trustSchema.%s must be a non-empty string", field)
		}
	}
	if value, ok := schema["governanceUri"]; ok {
		var uri string
		if err := decodeJSON(value, &uri); err != nil || !absoluteURI(uri) {
			return errors.New("trustSchema.governanceUri must be an absolute URI")
		}
	}
	if value, ok := schema["verificationMethods"]; ok {
		if err := validateStringArray(value); err != nil {
			return errors.New("trustSchema.verificationMethods must be an array of strings")
		}
	}
	return nil
}

// validateAICatalogAttestations checks reference URI, digest and size fields
// without retrieving or verifying attestations.
func validateAICatalogAttestations(raw json.RawMessage) error {
	var attestations []map[string]json.RawMessage
	if err := decodeJSON(raw, &attestations); err != nil || attestations == nil {
		return errors.New("attestations must be an array of objects")
	}
	for index, attestation := range attestations {
		for _, field := range []string{"type", "uri"} {
			var value string
			if err := requiredJSONField(attestation, field, &value); err != nil || strings.TrimSpace(value) == "" {
				return fmt.Errorf("attestations[%d].%s must be a non-empty string", index, field)
			}
			if field == "uri" && !absoluteURI(value) {
				return fmt.Errorf("attestations[%d].uri must be an absolute URI", index)
			}
		}
		if value, ok := attestation["digest"]; ok {
			if err := validateDigest(value); err != nil {
				return fmt.Errorf("attestations[%d].digest: %w", index, err)
			}
		}
		if value, ok := attestation["size"]; ok {
			var size *uint64
			if err := decodeJSON(value, &size); err != nil || size == nil {
				return fmt.Errorf("attestations[%d].size must be an unsigned integer", index)
			}
		}
		if value, ok := attestation["description"]; ok {
			if err := validateOptionalString(value, false); err != nil {
				return fmt.Errorf("attestations[%d].description must be a string", index)
			}
		}
	}
	return nil
}

// validateAICatalogProvenance checks relation/source identifiers and optional
// digest and reference fields.
func validateAICatalogProvenance(raw json.RawMessage) error {
	var links []map[string]json.RawMessage
	if err := decodeJSON(raw, &links); err != nil || links == nil {
		return errors.New("provenance must be an array of objects")
	}
	for index, link := range links {
		for _, field := range []string{"relation", "sourceId"} {
			var value string
			if err := requiredJSONField(link, field, &value); err != nil || strings.TrimSpace(value) == "" {
				return fmt.Errorf("provenance[%d].%s must be a non-empty string", index, field)
			}
		}
		for _, field := range []string{"sourceDigest", "signatureRef"} {
			if value, ok := link[field]; ok {
				if err := validateOptionalString(value, true); err != nil {
					return fmt.Errorf("provenance[%d].%s must be a non-empty string", index, field)
				}
			}
		}
		if value, ok := link["sourceDigest"]; ok {
			if err := validateDigest(value); err != nil {
				return fmt.Errorf("provenance[%d].sourceDigest: %w", index, err)
			}
		}
		for _, field := range []string{"registryUri", "statementUri"} {
			if value, ok := link[field]; ok {
				var uri string
				if err := decodeJSON(value, &uri); err != nil || !absoluteURI(uri) {
					return fmt.Errorf("provenance[%d].%s must be an absolute URI", index, field)
				}
			}
		}
	}
	return nil
}

// validateAICatalogSubject requires a typed digest subject and optionally binds
// its type and URL to the enclosing entry.
func validateAICatalogSubject(raw json.RawMessage, entryType, entryURL string, bindToEntry bool) error {
	var subject map[string]json.RawMessage
	if err := decodeJSON(raw, &subject); err != nil || subject == nil {
		return errors.New("subject must be an object")
	}
	var mediaType string
	if err := requiredJSONField(subject, "type", &mediaType); err != nil || strings.TrimSpace(mediaType) == "" {
		return errors.New("subject.type must be a non-empty string")
	}
	if bindToEntry && mediaType != entryType {
		return errors.New("subject.type must equal the containing entry type")
	}
	if digest, ok := subject["digest"]; !ok {
		return errors.New("subject.digest is required")
	} else if err := validateDigest(digest); err != nil {
		return fmt.Errorf("subject.digest: %w", err)
	}
	if value, ok := subject["url"]; ok {
		var uri string
		if err := decodeJSON(value, &uri); err != nil || !absoluteURI(uri) {
			return errors.New("subject.url must be an absolute URI")
		}
		if bindToEntry && (entryURL == "" || uri != entryURL) {
			return errors.New("subject.url must equal the containing entry URL")
		}
	}
	return nil
}

// validateDetachedJWS checks detached compact serialization, protected-header
// ambiguity and the supported algorithm vocabulary without verifying a
// signature.
func validateDetachedJWS(raw json.RawMessage) error {
	var value string
	if err := decodeJSON(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return errors.New("must be a non-empty detached JWS string")
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "" || parts[2] == "" {
		return errors.New("must use detached compact JWS serialization")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return errors.New("has an invalid protected header")
	}
	var header map[string]json.RawMessage
	if jsondoc.Validate(headerBytes) != nil || json.Unmarshal(headerBytes, &header) != nil || header == nil {
		return errors.New("protected header must be an unambiguous JSON object")
	}
	var algorithm string
	if err := requiredJSONField(header, "alg", &algorithm); err != nil {
		return errors.New("protected header requires alg")
	}
	switch algorithm {
	case "ES256", "ES384", "EdDSA", "PS256", "PS384", "RS256":
	default:
		return errors.New("protected header uses an unsupported signature algorithm")
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[2]); err != nil {
		return errors.New("has an invalid signature value")
	}
	return nil
}

// validateDigest accepts supported SHA-256-or-stronger algorithm names with
// exact lowercase hexadecimal lengths.
func validateDigest(raw json.RawMessage) error {
	var value string
	if err := decodeJSON(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return errors.New("must be a non-empty digest string")
	}
	algorithm, encoded, found := strings.Cut(value, ":")
	if !found || encoded == "" || strings.ToLower(encoded) != encoded {
		return errors.New("must use algorithm:lowercase-hex format")
	}
	bits := map[string]int{"sha256": 256, "sha384": 384, "sha512": 512, "sha3-256": 256, "sha3-384": 384, "sha3-512": 512}[strings.ToLower(algorithm)]
	if bits < 256 || len(encoded) != bits/4 {
		return errors.New("must use SHA-256 or a stronger supported digest")
	}
	for _, character := range encoded {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return errors.New("must use algorithm:lowercase-hex format")
		}
	}
	return nil
}

// validateRFC3339 decodes a JSON timestamp string and accepts RFC 3339
// fractional-second precision.
func validateRFC3339(raw json.RawMessage) error {
	var value string
	if err := decodeJSON(raw, &value); err != nil {
		return err
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err
}

// validateOptionalString checks a supplied JSON string and optionally rejects
// whitespace-only values.
func validateOptionalString(raw json.RawMessage, nonEmpty bool) error {
	var value *string
	if err := decodeJSON(raw, &value); err != nil || value == nil {
		return errors.New("must be a string")
	}
	if nonEmpty && strings.TrimSpace(*value) == "" {
		return errors.New("string is empty")
	}
	return nil
}

// validateStringArray preserves empty arrays and strings while rejecting null
// collections and elements, which decoding directly into []string would accept.
func validateStringArray(raw json.RawMessage) error {
	var values []*string
	if err := decodeJSON(raw, &values); err != nil || values == nil {
		return errors.New("must be an array of strings")
	}
	for _, value := range values {
		if value == nil {
			return errors.New("must be an array of strings")
		}
	}
	return nil
}

// aiCatalogTrustDomainAligned reports whether the declared trust identity
// matches the AIR publisher domain or its subdomain.
func aiCatalogTrustDomainAligned(identifier, identity string) bool {
	parts := strings.Split(identifier, ":")
	if len(parts) < 5 || !strings.EqualFold(parts[0], "urn") || !strings.EqualFold(parts[1], "air") {
		return false
	}
	publisher := strings.ToLower(strings.TrimSuffix(parts[2], "."))
	domain := trustIdentityDomain(identity)
	return domain != "" && (domain == publisher || strings.HasSuffix(domain, "."+publisher))
}

// validExtensionKey accepts an absolute URI or a reverse-domain extension
// namespace with at least two valid labels, without rewriting its spelling.
func validExtensionKey(value string) bool {
	if strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return false
	}
	if absoluteURI(value) {
		return true
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for index, character := range part {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' && index > 0 && index < len(part)-1 {
				continue
			}
			return false
		}
	}
	return true
}

// absoluteURI requires a parsed absolute scheme and rejects CR, LF and NUL in
// the retained spelling.
func absoluteURI(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.IsAbs() && parsed.Scheme != "" && !strings.ContainsAny(value, "\r\n\x00")
}

// requiredJSONField requires a named member and decodes its expected type with
// a field-specific error.
func requiredJSONField[T any](object map[string]json.RawMessage, name string, target *T) error {
	raw, ok := object[name]
	if !ok {
		return fmt.Errorf("%s is required", name)
	}
	if err := decodeJSON(raw, target); err != nil {
		return fmt.Errorf("%s has the wrong type", name)
	}
	return nil
}

// decodeJSON preserves numeric spelling and rejects a second trailing JSON
// value.
func decodeJSON[T any](raw []byte, target *T) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}
