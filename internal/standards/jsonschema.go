// Package standards validates protocol documents against the exact schemas
// pinned in the service's standards bundle. It combines embedded JSON Schema
// checks with ARD JSON-LD and AI Catalog semantic rules. Additional JSON-LD
// contexts require a caller-controlled [ContextFetcher]; schemas are compiled
// from pinned embedded inputs. Structural validity does not establish publisher
// trust or authorize resource invocation.
package standards

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/schemas"
)

const (
	ardSchemaPath           = "upstream/ard/ard-0.91-aa3e598/spec/schemas/ard-entry.schema.json"
	ardSchemaURL            = "https://raw.githubusercontent.com/ards-project/ard-spec/main/spec/schemas/ard-entry.schema.json"
	mcpServerCardSchemaPath = "upstream/mcp/mcp-server-card-v1-experimental-526201b/schema.json"
	mcpServerCardSchemaURL  = "https://catalog.invalid/schemas/mcp-server-card-v1.json"
	mcpRegistrySchemaPath   = "upstream/mcp/mcp-registry-server-1.8.1-f52dc85/server.schema.json"
)

type compiledSchemas struct {
	ardEntry    *jsonschema.Schema
	ardFields   *jsonschema.Schema
	ardManifest *jsonschema.Schema
	mcpCard     *jsonschema.Schema
	mcpRegistry *jsonschema.Schema
}

var (
	compileOnce sync.Once
	compiled    compiledSchemas
	errCompile  error
)

// ValidateARDEntryFields validates present discovery terms without requiring a
// complete entry. Search projections may omit delivery and descriptive fields.
// The reviewed trust-member alias is applied only to this validation copy.
func ValidateARDEntryFields(raw []byte) error {
	if err := jsondoc.Validate(raw); err != nil {
		return errors.New("ARD fields are not unambiguous JSON")
	}
	validationCopy, err := ardValidationCopy(raw)
	if err != nil {
		return err
	}
	schemaSet, err := loadSchemas()
	if err != nil {
		return err
	}
	return validate(schemaSet.ardFields, validationCopy)
}

// ardValidationCopy reconciles the schema/context trust-member spelling on a
// validation copy. Retained evidence and JSON-LD input keep their exact bytes.
func ardValidationCopy(raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("ARD fields must be an object")
	}
	if trust, present := fields["trustManifest"]; present {
		if _, duplicate := fields["TrustManifest"]; duplicate {
			return nil, errors.New("ARD trust manifest has conflicting spellings")
		}
		fields["TrustManifest"] = trust
		delete(fields, "trustManifest")
		var err error
		raw, err = json.Marshal(fields)
		if err != nil {
			return nil, fmt.Errorf("encode ARD validation projection: %w", err)
		}
	}
	return raw, nil
}

// ValidateARDSearchEntry validates present entry terms, media type, node identity
// and declared trust-domain alignment without requiring complete-entry fields.
// The caller enforces Search's identifier, score and source requirements.
// This check neither resolves contexts nor verifies cryptographic evidence.
func ValidateARDSearchEntry(raw []byte) error {
	if err := ValidateARDEntryFields(raw); err != nil {
		return err
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entry); err != nil {
		return err
	}
	if err := validateARDPresentSemantics(entry); err != nil {
		return err
	}
	return validateARDTrustBinding(entry)
}

// ValidateARDQueryContext checks the URI/object/array shape shared by the pinned
// QueryModel and EntryFields definitions. Nil represents an omitted context;
// wire decoders must reject explicit null before that distinction is lost.
// This structural check never resolves or fetches a context.
func ValidateARDQueryContext(queryContext any) error {
	if queryContext == nil {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"@context": queryContext})
	if err != nil {
		return fmt.Errorf("encode ARD query context: %w", err)
	}
	return ValidateARDEntryFields(raw)
}

// ValidateARDEntry validates one ARD 0.91 entry against the authoritative
// schema from the pinned ARD profile.
func ValidateARDEntry(raw []byte) error {
	if err := jsondoc.Validate(raw); err != nil {
		return errors.New("ARD entry is not unambiguous JSON")
	}
	validationCopy, err := ardValidationCopy(raw)
	if err != nil {
		return err
	}
	schemaSet, err := loadSchemas()
	if err != nil {
		return err
	}
	if err := validate(schemaSet.ardEntry, validationCopy); err != nil {
		return err
	}
	return validateARDSemantics(raw)
}

// ValidateARDManifest validates a hosted ARD 0.91 manifest.
func ValidateARDManifest(raw []byte) error {
	if err := jsondoc.Validate(raw); err != nil {
		return errors.New("ARD manifest is not unambiguous JSON")
	}
	schemaSet, err := loadSchemas()
	if err != nil {
		return err
	}
	return validate(schemaSet.ardManifest, raw)
}

// ValidateMCPServerCard validates the experimental v1 MCP Server Card profile.
func ValidateMCPServerCard(raw []byte) error {
	schemaSet, err := loadSchemas()
	if err != nil {
		return err
	}
	return validate(schemaSet.mcpCard, raw)
}

// ValidateMCPRegistryServer validates MCP Registry server.json 1.8.1 metadata.
func ValidateMCPRegistryServer(raw []byte) error {
	schemaSet, err := loadSchemas()
	if err != nil {
		return err
	}
	return validate(schemaSet.mcpRegistry, raw)
}

// loadSchemas compiles the pinned ARD and distinct MCP schemas once and retains
// any compilation error.
func loadSchemas() (compiledSchemas, error) {
	compileOnce.Do(func() {
		compiler := jsonschema.NewCompiler()
		compiler.AssertFormat()

		ardDocument, err := readSchema(ardSchemaPath)
		if err != nil {
			errCompile = err
			return
		}
		if err := compiler.AddResource(ardSchemaURL, ardDocument); err != nil {
			errCompile = fmt.Errorf("register ARD schema: %w", err)
			return
		}

		mcpCardDocument, err := readSchema(mcpServerCardSchemaPath)
		if err != nil {
			errCompile = err
			return
		}
		if err := compiler.AddResource(mcpServerCardSchemaURL, mcpCardDocument); err != nil {
			errCompile = fmt.Errorf("register MCP Server Card schema: %w", err)
			return
		}

		mcpRegistryDocument, err := readSchema(mcpRegistrySchemaPath)
		if err != nil {
			errCompile = err
			return
		}
		mcpRegistryURL := schemaIdentifier(mcpRegistryDocument)
		if err := compiler.AddResource(mcpRegistryURL, mcpRegistryDocument); err != nil {
			errCompile = fmt.Errorf("register MCP Registry schema: %w", err)
			return
		}

		compiled.ardEntry, err = compiler.Compile(ardSchemaURL)
		if err != nil {
			errCompile = fmt.Errorf("compile ARD entry schema: %w", err)
			return
		}
		compiled.ardManifest, err = compiler.Compile(ardSchemaURL + "#/$defs/ArdManifest")
		if err != nil {
			errCompile = fmt.Errorf("compile ARD manifest schema: %w", err)
			return
		}
		compiled.ardFields, err = compiler.Compile(ardSchemaURL + "#/$defs/EntryFields")
		if err != nil {
			errCompile = fmt.Errorf("compile ARD fields schema: %w", err)
			return
		}
		compiled.mcpCard, err = compiler.Compile(mcpServerCardSchemaURL + "#/$defs/ServerCard")
		if err != nil {
			errCompile = fmt.Errorf("compile MCP Server Card schema: %w", err)
			return
		}
		compiled.mcpRegistry, err = compiler.Compile(mcpRegistryURL)
		if err != nil {
			errCompile = fmt.Errorf("compile MCP Registry schema: %w", err)
		}
	})
	return compiled, errCompile
}

// readSchema loads and decodes an exact embedded profile path.
func readSchema(path string) (any, error) {
	raw, err := schemas.Files.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read embedded schema %s: %w", path, err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode embedded schema %s: %w", path, err)
	}
	return document, nil
}

// schemaIdentifier extracts a schema's declared identifier for local compiler
// registration.
func schemaIdentifier(document any) string {
	object, _ := document.(map[string]any)
	identifier, _ := object["$id"].(string)
	return identifier
}

// validate rejects ambiguous JSON before applying the selected compiled schema.
func validate(schema *jsonschema.Schema, raw []byte) error {
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("decode JSON document: %w", err)
	}
	if err := schema.Validate(instance); err != nil {
		var validationError *jsonschema.ValidationError
		if errors.As(err, &validationError) {
			path := jsonPointer(deepestInstanceLocation(validationError))
			if path == "" {
				return errors.New("document does not conform to the pinned schema")
			}
			return fmt.Errorf("document does not conform to the pinned schema at %s", path)
		}
		return fmt.Errorf("validate document against pinned schema: %w", err)
	}
	return nil
}

// deepestInstanceLocation chooses a specific nested validation location for a
// bounded source-path finding.
func deepestInstanceLocation(validationError *jsonschema.ValidationError) []string {
	best := validationError.InstanceLocation
	for _, cause := range validationError.Causes {
		candidate := deepestInstanceLocation(cause)
		if len(candidate) > len(best) {
			best = candidate
		}
	}
	return best
}

// jsonPointer escapes instance-location segments into an RFC 6901 pointer.
func jsonPointer(segments []string) string {
	if len(segments) == 0 {
		return ""
	}
	// An oversized location falls back to the existing location-free error.
	// Count escapes before allocating; instance member names are untrusted.
	size := 0
	for _, segment := range segments {
		size += 1 + len(segment) + strings.Count(segment, "~") + strings.Count(segment, "/")
		if size > 1024 {
			return ""
		}
	}
	escaped := make([]string, len(segments))
	for index, segment := range segments {
		escaped[index] = strings.NewReplacer("~", "~0", "/", "~1").Replace(segment)
	}
	return "/" + strings.Join(escaped, "/")
}

// MarshalAndValidateARDManifest is used by publication to make schema
// validation part of the candidate build, not only a test-time assertion.
func MarshalAndValidateARDManifest(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := ValidateARDManifest(raw); err != nil {
		return nil, err
	}
	var manifest struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	for index, entry := range manifest.Entries {
		if err := ValidateARDEntry(entry); err != nil {
			return nil, fmt.Errorf("ARD entry %d: %w", index, err)
		}
	}
	return raw, nil
}

// validateARDSemantics checks AIR identity, media type and trust-domain
// alignment beyond the embedded schema.
func validateARDSemantics(raw []byte) error {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entry); err != nil {
		return err
	}
	var identifier string
	if err := json.Unmarshal(entry["identifier"], &identifier); err != nil {
		return errors.New("ARD identifier is invalid")
	}
	if _, err := catalog.ParseAIRIdentifier(identifier); err != nil {
		return errors.New("ARD identifier must use a domain-anchored urn:air publisher")
	}
	var displayName string
	if err := json.Unmarshal(entry["displayName"], &displayName); err != nil || strings.TrimSpace(displayName) == "" {
		return errors.New("ARD displayName must be a non-empty string")
	}
	if err := validateARDPresentSemantics(entry); err != nil {
		return err
	}
	return validateARDTrustBinding(entry)
}

// validateARDPresentSemantics shares the complete-entry checks without adding
// required fields to sparse Search results or changing their retained bytes.
// Schema validation remains responsible for field shapes and full-entry terms.
func validateARDPresentSemantics(entry map[string]json.RawMessage) error {
	if rawType, present := entry["type"]; present {
		var mediaType string
		if err := json.Unmarshal(rawType, &mediaType); err != nil {
			return errors.New("ARD type is invalid")
		}
		parsedMediaType, _, err := mime.ParseMediaType(strings.TrimSpace(mediaType))
		if err != nil || !strings.Contains(parsedMediaType, "/") {
			return errors.New("ARD type must be a valid media type")
		}
	}
	if rawID, ok := entry["@id"]; ok {
		var nodeID string
		if err := json.Unmarshal(rawID, &nodeID); err != nil || strings.TrimSpace(nodeID) == "" {
			return errors.New("ARD @id must be a non-empty IRI")
		}
		var identifier string
		if err := json.Unmarshal(entry["identifier"], &identifier); err != nil || nodeID != identifier {
			return errors.New("ARD @id differs from identifier and no approved identity mapping proves equivalence")
		}
	}
	return nil
}

// validateARDTrustBinding checks the declaration, not ownership or signatures.
// Complete entries and sparse Search entries share this publisher boundary.
func validateARDTrustBinding(entry map[string]json.RawMessage) error {
	rawTrust, hasLowerTrust := entry["trustManifest"]
	upperTrust, hasUpperTrust := entry["TrustManifest"]
	if hasLowerTrust && hasUpperTrust {
		return errors.New("ARD entry cannot contain both trustManifest and TrustManifest")
	}
	if hasUpperTrust {
		rawTrust = upperTrust
	}
	if !hasLowerTrust && !hasUpperTrust {
		return nil
	}
	var trust map[string]json.RawMessage
	if err := json.Unmarshal(rawTrust, &trust); err != nil || trust == nil {
		return errors.New("ARD trustManifest must be an object")
	}
	var identity string
	if err := json.Unmarshal(trust["identity"], &identity); err != nil || strings.TrimSpace(identity) == "" {
		return errors.New("ARD trustManifest.identity is required")
	}
	var identifier string
	if err := json.Unmarshal(entry["identifier"], &identifier); err != nil {
		return errors.New("ARD identifier is invalid")
	}
	publisher := ardPublisher(identifier)
	identityDomain := trustIdentityDomain(identity)
	if publisher == "" || identityDomain == "" || identityDomain != publisher && !strings.HasSuffix(identityDomain, "."+publisher) {
		return errors.New("ARD trustManifest.identity does not align with the identifier publisher")
	}
	return nil
}

// ardPublisher extracts the normalized AIR publisher or returns empty for an
// invalid identifier.
func ardPublisher(identifier string) string {
	parsed, err := catalog.ParseAIRIdentifier(identifier)
	if err != nil {
		return ""
	}
	return parsed.Publisher
}

// trustIdentityDomain derives the declared domain from a did:web or URL
// identity without resolving or authenticating it.
func trustIdentityDomain(identity string) string {
	identity = strings.TrimSpace(identity)
	if strings.HasPrefix(strings.ToLower(identity), "did:web:") {
		value := strings.TrimPrefix(identity, "did:web:")
		domain, _, _ := strings.Cut(value, ":")
		decoded, err := url.PathUnescape(domain)
		if err == nil {
			domain = decoded
		}
		if parsed, err := url.Parse("https://" + domain); err == nil {
			return strings.ToLower(parsed.Hostname())
		}
		return ""
	}
	parsed, err := url.Parse(identity)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
}
