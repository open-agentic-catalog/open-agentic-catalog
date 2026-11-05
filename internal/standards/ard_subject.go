package standards

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/piprate/json-gold/ld"
)

// ARDSubjectValues projects only the selected literal subject vocabulary from
// already decoded entry fields. It preserves sparse admission and never loads
// a remote context or parses opaque native documents. The caller enforces
// document byte limits.
func ARDSubjectValues(fields map[string]json.RawMessage) (values []string, present bool, err error) {
	return ARDSubjectValuesWithValidation(fields, nil)
}

// ARDSubjectValuesWithValidation applies caller bounds to each original subject
// value before decoding and duplicate removal. Context resolution and literal
// admission follow ARDSubjectValues; remote contexts are never fetched.
func ARDSubjectValuesWithValidation(fields map[string]json.RawMessage, validate func(json.RawMessage) error) (values []string, present bool, err error) {
	defer func() {
		if recover() != nil {
			values, err = nil, errors.New("ARD subject context was rejected")
		}
	}()
	_, present = fields[ARDSubjectIRI]
	var localContext any
	if raw, ok := fields["@context"]; ok {
		if err := json.Unmarshal(raw, &localContext); err != nil {
			return nil, false, errors.New("ARD subject context is invalid")
		}
	}
	needsContext := false
	for key := range fields {
		if !strings.HasPrefix(key, "@") && key != ARDSubjectIRI {
			if _, core := canonicalARDTerms[ardNamespace+key]; !core && key != "type" && key != "score" && key != "source" {
				needsContext = true
			}
		}
	}
	// Inline contexts can also alias an ordinary term. An uninterpreted remote
	// context on a sparse core-only projection does not disclose a subject.
	switch localContext.(type) {
	case map[string]any, []any:
		needsContext = true
	}
	var active *ld.Context
	if needsContext {
		base, err := loadBaseContext()
		if err != nil {
			return nil, false, err
		}
		options := ld.NewJsonLdOptions("")
		options.DocumentLoader = &boundedContextLoader{baseContext: base}
		baseDocument, ok := base.(map[string]any)
		if !ok {
			return nil, present, errors.New("ARD base context is not an object")
		}
		active, err = ld.NewContext(nil, options).Parse(baseDocument["@context"])
		if err == nil && localContext != nil {
			active, err = active.Parse(localContext)
		}
		if err != nil {
			return nil, present, errors.New("ARD subject context cannot be resolved locally")
		}
	}
	seen := make(map[string]struct{})
	for key, raw := range fields {
		iri := key
		if active != nil && !strings.HasPrefix(key, "@") {
			iri, err = active.ExpandIri(key, false, true, nil, nil)
			if err != nil {
				return nil, false, errors.New("ARD subject term cannot be resolved")
			}
		}
		if iri != ARDSubjectIRI {
			continue
		}
		present = true
		if active != nil {
			if definition := active.GetTermDefinition(key); definition != nil && (definition.Reverse || definition.Type == "@id" || definition.Type == "@vocab") {
				return nil, true, errors.New("ARD subject declarations must be forward literal topics")
			}
		}
		if validate != nil {
			if err := validate(raw); err != nil {
				return nil, true, err
			}
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, true, errors.New("ARD subjects are not literal strings")
		}
		var subjects []string
		switch typed := value.(type) {
		case nil:
		case string:
			subjects = []string{typed}
		case []any:
			for _, item := range typed {
				text, ok := item.(string)
				if !ok {
					return nil, true, errors.New("ARD subjects are not literal strings")
				}
				subjects = append(subjects, text)
			}
		default:
			return nil, true, errors.New("ARD subjects are not literal strings")
		}
		for _, subject := range subjects {
			if _, duplicate := seen[subject]; !duplicate {
				seen[subject] = struct{}{}
				values = append(values, subject)
			}
		}
	}
	sort.Strings(values)
	return values, present, nil
}
