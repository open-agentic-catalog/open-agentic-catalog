package federation

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

// ObservationControlFilters contains only bounded, literal ARD declarations.
// Nil means unavailable evidence; a nonnil empty Categories is known-empty.
// It does not inspect native data or consuming-Catalog presentation overrides.
type ObservationControlFilters struct {
	License    *string
	Categories []string
}

// ProjectObservationControlFilters derives optional filters from the retained
// normalized entry. Each dimension fails independently without rejecting the
// observation. Ingestion has already expanded subject aliases to their IRI.
func ProjectObservationControlFilters(raw []byte) ObservationControlFilters {
	var entry map[string]json.RawMessage
	if json.Unmarshal(raw, &entry) != nil {
		return ObservationControlFilters{}
	}
	var metadata, unknown map[string]json.RawMessage
	_ = json.Unmarshal(entry["metadata"], &metadata)
	_ = json.Unmarshal(entry["unknownProperties"], &unknown)
	return projectControlDeclarations(metadata, unknown[standards.ARDSubjectIRI])
}

func projectControlDeclarations(metadata map[string]json.RawMessage, subject json.RawMessage) ObservationControlFilters {
	var result ObservationControlFilters
	var license string
	if json.Unmarshal(metadata["license"], &license) == nil && validControlDeclaration(license, catalog.MaxCompactLicenseCharacters) {
		result.License = &license
	}
	labels, ok := controlCategoryLabels(subject)
	if !ok {
		return result
	}
	// Validate before normalizing: duplicates and surrounding spaces still count
	// toward the raw declaration limits. Never silently truncate or skip labels.
	result.Categories = make([]string, 0, len(labels))
	for _, label := range labels {
		result.Categories = append(result.Categories, strings.ToLower(strings.TrimSpace(label)))
	}
	slices.Sort(result.Categories)
	result.Categories = slices.Compact(result.Categories)
	return result
}

func controlCategoryLabels(subject json.RawMessage) ([]string, bool) {
	var scalar string
	var labels []string
	if json.Unmarshal(subject, &scalar) == nil && len(subject) > 0 && subject[0] == '"' {
		labels = []string{scalar}
	} else if json.Unmarshal(subject, &labels) != nil || labels == nil {
		return nil, false
	}
	if len(labels) > 16 {
		return nil, false
	}
	for _, label := range labels {
		if !validControlDeclaration(label, 80) {
			return nil, false
		}
	}
	return labels, true
}

// Live declarations use only the supplied entry and local JSON-LD contexts.
// No artifact or remote context is fetched within the live work budget. Original
// labels are checked before JSON-LD can deduplicate or unwrap their values.
func projectLiveControlFilters(ctx context.Context, raw []byte, categories bool) ObservationControlFilters {
	var entry map[string]json.RawMessage
	if json.Unmarshal(raw, &entry) != nil {
		return ObservationControlFilters{}
	}
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal(entry["metadata"], &metadata)
	result := projectControlDeclarations(metadata, nil)
	if !categories || ctx.Err() != nil {
		return result
	}
	count := 0
	_, present, err := standards.ARDSubjectValuesWithValidation(entry, func(value json.RawMessage) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		labels, valid := controlCategoryLabels(value)
		if !valid || count+len(labels) > 16 {
			return errors.New("control subjects exceed bounded literal declarations")
		}
		count += len(labels)
		return nil
	})
	if err != nil || !present || ctx.Err() != nil {
		return result
	}
	normalized, err := standards.NormalizeARDEntry(raw, nil)
	if err != nil || ctx.Err() != nil {
		return result
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(normalized, &fields) != nil {
		return result
	}
	if count == 0 {
		result.Categories = []string{}
	} else {
		result.Categories = projectControlDeclarations(nil, fields[standards.ARDSubjectIRI]).Categories
	}
	return result
}

func validControlDeclaration(value string, maximum int) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') && utf8.RuneCountInString(value) <= maximum && strings.TrimSpace(value) != ""
}

func (p ObservationControlFilters) matches(query catalog.SearchQuery) bool {
	return (query.License == "" || p.License != nil && *p.License == query.License) &&
		(len(query.Categories) == 0 || p.Categories != nil && containsAllStrings(p.Categories, query.Categories))
}

func (p ObservationControlFilters) insufficient(query catalog.SearchQuery) bool {
	// A known mismatch excludes the entry without needing the other field.
	// Missing evidence is partial only while the entry could still match.
	if query.License != "" && p.License != nil && *p.License != query.License ||
		len(query.Categories) > 0 && p.Categories != nil && !containsAllStrings(p.Categories, query.Categories) {
		return false
	}
	return query.License != "" && p.License == nil || len(query.Categories) > 0 && p.Categories == nil
}
