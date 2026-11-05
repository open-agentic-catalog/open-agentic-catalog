// ARD JSON-LD normalization uses the embedded base context and an explicitly
// bounded caller-controlled loader for additional remote contexts.

package standards

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/piprate/json-gold/ld"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/schemas"
)

const (
	ARDBaseContext    = "https://agenticresourcediscovery.org/context/v1"
	ARDSubjectIRI     = "http://purl.org/dc/terms/subject"
	ardNamespace      = "https://agenticresourcediscovery.org/ns#"
	ardContextPath    = "upstream/ard/ard-0.91-aa3e598/spec/schemas/ard.context.jsonld"
	maxRemoteContexts = 8
)

// ContextFetcher retrieves a remote JSON-LD context through the caller's
// policy-controlled fetch path. It must enforce transport, size, redirect,
// address, and timeout limits.
type ContextFetcher func(string) ([]byte, error)

// NormalizeARDEntry expands an ARD entry with the mandatory base context,
// applies the entry's own context, and compacts recognized terms back to the
// canonical ARD vocabulary. The original document should still be retained as
// source evidence by callers.
func NormalizeARDEntry(raw []byte, fetch ContextFetcher) ([]byte, error) {
	if err := jsondoc.Validate(raw); err != nil {
		return nil, errors.New("ARD entry is not unambiguous JSON")
	}
	var input any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return nil, errors.New("ARD entry is not valid JSON")
	}
	object, ok := input.(map[string]any)
	if !ok {
		return nil, errors.New("ARD entry must be a JSON object")
	}
	if contextValue, exists := object["@context"]; exists {
		object["@context"] = processorContext(contextValue)
	}
	lowerTrust, hasLowerTrust := object["trustManifest"]
	upperTrust, hasUpperTrust := object["TrustManifest"]
	if hasLowerTrust && hasUpperTrust {
		return nil, errors.New("ARD entry cannot contain both trustManifest and TrustManifest")
	}
	if hasUpperTrust {
		object["trustManifest"] = upperTrust
		delete(object, "TrustManifest")
	} else if hasLowerTrust {
		object["trustManifest"] = lowerTrust
	}

	baseContext, err := loadBaseContext()
	if err != nil {
		return nil, err
	}
	options := ld.NewJsonLdOptions("")
	options.ExpandContext = baseContext
	options.DocumentLoader = &boundedContextLoader{baseContext: baseContext, fetch: fetch}
	options.SafeMode = true
	compacted, err := compactJSONLDSafely(input, baseContext, options)
	if err != nil {
		return nil, fmt.Errorf("expand ARD JSON-LD entry: %w", err)
	}
	delete(compacted, "@context")
	if identifier, ok := compacted["id"]; ok {
		compacted["@id"] = identifier
		delete(compacted, "id")
	}
	normalized, err := json.Marshal(compacted)
	if err != nil {
		return nil, fmt.Errorf("encode normalized ARD entry: %w", err)
	}
	return normalized, nil
}

// ResolveARDTerm resolves the leading term in a query filter or facet through
// the ARD base context and an optional inline query context. Only terms that
// this Registry indexes are returned; unknown IRIs remain unsupported.
func ResolveARDTerm(term string, queryContext any) (string, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return "", errors.New("ARD term is empty")
	}
	if term == "publisher" {
		return term, nil
	}
	// Resolve a complete IRI or alias first. Periods in its namespace or
	// prefix are not literal path delimiters.
	iri, err := expandARDQueryTerm(term, queryContext)
	if canonical, ok := canonicalARDTerms[iri]; err == nil && ok {
		return canonical, nil
	}
	leading, remainder, path := strings.Cut(term, ".")
	if strings.HasPrefix(term, ardNamespace) {
		name, tail, found := strings.Cut(strings.TrimPrefix(term, ardNamespace), ".")
		leading, remainder, path = ardNamespace+name, tail, found
	} else if colon := strings.IndexByte(term, ':'); colon >= 0 {
		// A compact IRI can have periods in its prefix. Dots after the
		// colon delimit paths; ordinary metadata keys retain literal colons.
		if _, known := canonicalARDTerms[ardNamespace+leading]; !known {
			name, tail, found := strings.Cut(term[colon+1:], ".")
			leading, remainder, path = term[:colon+1]+name, tail, found
		}
	}
	if !path {
		if err != nil {
			return "", err
		}
		return "", errors.New("ARD query term is not supported by this Registry")
	}
	iri, err = expandARDQueryTerm(leading, queryContext)
	canonical, ok := canonicalARDTerms[iri]
	if err == nil && ok && (canonical == "metadata" || canonical == "trustManifest" || canonical == "data") {
		if remainder == "" {
			return "", errors.New("ARD query term path is empty")
		}
		return canonical + "." + remainder, nil
	}
	// A plain context alias can itself contain periods. Try only the one-
	// and two-member suffixes used by the reviewed opaque paths. Do not
	// scan every separator or resolve native member names as JSON-LD terms.
	root, suffix := term, ""
	for depth := 0; depth < 2; depth++ {
		separator := strings.LastIndexByte(root, '.')
		if separator < 0 {
			break
		}
		member := root[separator+1:]
		if member == "" {
			break
		}
		if suffix != "" {
			member += "." + suffix
		}
		root, suffix = root[:separator], member
		iri, resolveErr := expandARDQueryTerm(root, queryContext)
		canonical, ok := canonicalARDTerms[iri]
		if resolveErr == nil && ok && (canonical == "metadata" || canonical == "trustManifest" || canonical == "data") {
			return canonical + "." + suffix, nil
		}
	}
	if err != nil {
		return "", err
	}
	return "", errors.New("ARD query term path is not supported by this Registry")
}

// expandARDQueryTerm uses the existing processor and local loader, including
// actual protected definitions and keyword/reverse-property admission.
func expandARDQueryTerm(leading string, queryContext any) (string, error) {
	input := map[string]any{leading: "value"}
	if queryContext != nil {
		input["@context"] = processorContext(queryContext)
	}
	baseContext, err := loadBaseContext()
	if err != nil {
		return "", err
	}
	options := ld.NewJsonLdOptions("")
	options.ExpandContext = baseContext
	options.DocumentLoader = &boundedContextLoader{baseContext: baseContext}
	options.SafeMode = true
	expanded, err := expandJSONLDSafely(input, options)
	if err != nil {
		return "", fmt.Errorf("resolve ARD query context: %w", err)
	}
	if len(expanded) != 1 {
		return "", errors.New("ARD query term did not resolve to one node")
	}
	node, ok := expanded[0].(map[string]any)
	if !ok || len(node) != 1 {
		return "", errors.New("ARD query term is not supported by this Registry")
	}
	var iri string
	for key := range node {
		iri = key
	}
	return iri, nil
}

var canonicalARDTerms = map[string]string{
	ARDSubjectIRI:                          ARDSubjectIRI,
	ardNamespace + "identifier":            "identifier",
	ardNamespace + "displayName":           "displayName",
	ardNamespace + "mediaType":             "type",
	ardNamespace + "url":                   "url",
	ardNamespace + "data":                  "data",
	ardNamespace + "representativeQueries": "representativeQueries",
	ardNamespace + "capabilities":          "capabilities",
	ardNamespace + "tags":                  "tags",
	ardNamespace + "description":           "description",
	ardNamespace + "version":               "version",
	ardNamespace + "updatedAt":             "updatedAt",
	ardNamespace + "metadata":              "metadata",
	ardNamespace + "trustManifest":         "trustManifest",
}

// compactJSONLDSafely converts processor panics into a bounded parse error so
// untrusted JSON-LD cannot panic the service.
func compactJSONLDSafely(input, contextValue any, options *ld.JsonLdOptions) (compacted map[string]any, err error) {
	defer func() {
		if recover() != nil {
			compacted = nil
			err = errors.New("JSON-LD processor rejected the document")
		}
	}()
	return ld.NewJsonLdProcessor().Compact(input, contextValue, options)
}

// expandJSONLDSafely applies JSON-LD expansion while containing processor
// panics at the untrusted-document boundary.
func expandJSONLDSafely(input any, options *ld.JsonLdOptions) (expanded []any, err error) {
	defer func() {
		if recover() != nil {
			expanded = nil
			err = errors.New("JSON-LD processor rejected the document")
		}
	}()
	return ld.NewJsonLdProcessor().Expand(input, options)
}

type boundedContextLoader struct {
	baseContext any
	fetch       ContextFetcher
	loaded      int
}

// LoadDocument serves the pinned base context and retrieves only bounded
// additional contexts through the configured policy-controlled fetcher.
func (loader *boundedContextLoader) LoadDocument(uri string) (*ld.RemoteDocument, error) {
	if uri == ARDBaseContext {
		return &ld.RemoteDocument{DocumentURL: uri, Document: loader.baseContext}, nil
	}
	if loader.fetch == nil {
		return nil, fmt.Errorf("remote JSON-LD context %q is not allowed in this operation", uri)
	}
	loader.loaded++
	if loader.loaded > maxRemoteContexts {
		return nil, errors.New("remote JSON-LD context budget was exhausted")
	}
	raw, err := loader.fetch(uri)
	if err != nil {
		return nil, fmt.Errorf("fetch remote JSON-LD context: %w", err)
	}
	if err := jsondoc.Validate(raw); err != nil {
		return nil, errors.New("remote JSON-LD context is not unambiguous JSON")
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("remote JSON-LD context is not valid JSON")
	}
	if object, ok := document.(map[string]any); ok {
		if contextValue, exists := object["@context"]; exists {
			object["@context"] = processorContext(contextValue)
		}
	}
	return &ld.RemoteDocument{DocumentURL: uri, Document: document}, nil
}

// processorContext adapts numeric context controls to json-gold's float64
// contract. It copies only contexts and scoped term contexts. Opaque documents,
// JSON literal values and caller-owned query maps keep their exact numbers.
func processorContext(value any) any {
	switch contextValue := value.(type) {
	case []any:
		result := make([]any, len(contextValue))
		for index, item := range contextValue {
			result[index] = processorContext(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(contextValue))
		for key, item := range contextValue {
			if key == "@version" {
				if number, ok := item.(json.Number); ok {
					if parsed, err := number.Float64(); err == nil {
						item = parsed
					}
				}
			} else if definition, ok := item.(map[string]any); ok {
				if scoped, exists := definition["@context"]; exists {
					copyDefinition := make(map[string]any, len(definition))
					for name, field := range definition {
						copyDefinition[name] = field
					}
					copyDefinition["@context"] = processorContext(scoped)
					item = copyDefinition
				}
			}
			result[key] = item
		}
		return result
	default:
		return value
	}
}

// loadBaseContext decodes the immutable embedded ARD context without network
// retrieval.
func loadBaseContext() (any, error) {
	raw, err := schemas.Files.ReadFile(ardContextPath)
	if err != nil {
		return nil, fmt.Errorf("read embedded ARD base context: %w", err)
	}
	var contextDocument any
	if err := json.Unmarshal(raw, &contextDocument); err != nil {
		return nil, fmt.Errorf("decode embedded ARD base context: %w", err)
	}
	return contextDocument, nil
}
