package catalog

import "strings"

// NormalizeFilters uses the same slug/domain normalization as authoring. Names
// remain exact, including case and spacing; an absent filter is the empty value.
func (r NamespaceListRequest) NormalizeFilters() (NamespaceListRequest, error) {
	if r.Slug != "" {
		if !validTextLength(r.Slug, 64) {
			return r, E(CodeInvalidArgument, "namespace slug filter is invalid")
		}
		r.Slug = strings.ToLower(strings.TrimSpace(r.Slug))
		if !validSlug(r.Slug) {
			return r, E(CodeInvalidArgument, "namespace slug filter is invalid")
		}
	}
	if r.DisplayName != "" && (strings.TrimSpace(r.DisplayName) == "" || !validTextLength(r.DisplayName, 160)) {
		return r, E(CodeInvalidArgument, "displayName filter must contain at most 160 characters")
	}
	return r, nil
}

// NormalizeFilters selects declared domains only; approval grants no match.
func (r PublisherListRequest) NormalizeFilters() (PublisherListRequest, error) {
	if r.DisplayName != "" && (strings.TrimSpace(r.DisplayName) == "" || !validTextLength(r.DisplayName, 160)) {
		return r, E(CodeInvalidArgument, "displayName filter must contain at most 160 characters")
	}
	if r.DeclaredDomain != "" {
		if !validTextLength(r.DeclaredDomain, 253) {
			return r, E(CodeInvalidArgument, "declaredDomain filter is invalid")
		}
		domains, err := normalizePublisherDomains([]string{r.DeclaredDomain})
		if err != nil {
			return r, err
		}
		r.DeclaredDomain = domains[0]
	}
	return r, nil
}
