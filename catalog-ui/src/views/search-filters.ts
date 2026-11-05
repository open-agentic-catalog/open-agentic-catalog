// Pure filter updates preserve unrelated search criteria and invalidate obsolete cursors.
import type { components } from "../api/schema";
import type { SearchResult } from "../api/client";

export type PublisherSuggestion = {
  value: string;
  label: string;
  origin: "local" | "external";
};

/** Current-page labels are hints; exact publisher identifiers remain filter authority. */
export function publisherSuggestions(
  items: SearchResult[],
): PublisherSuggestion[] {
  const suggestions = new Map<string, PublisherSuggestion>();
  for (const item of items) {
    if (!item.publisher || suggestions.has(item.publisher)) continue;
    suggestions.set(item.publisher, {
      value: item.publisher,
      label:
        item.type === "local"
          ? (item.local?.display?.publisherName ?? item.publisher)
          : item.publisher,
      origin: item.type === "local" ? "local" : "external",
    });
    if (suggestions.size === 8) break;
  }
  return [...suggestions.values()];
}

export type SearchQuery = components["schemas"]["CatalogSearch"];
/** Explore accepts the same local criteria without Search continuation or origins. */
export function categoryFacetQuery(
  query: SearchQuery,
): components["schemas"]["CatalogExploreQuery"] {
  const {
    origins: _origins,
    cursor: _cursor,
    limit: _limit,
    order: _order,
    ...local
  } = query;
  return local;
}
export type FilterValues = {
  [
    K in
      | "kinds"
      | "tags"
      | "categories"
      | "license"
      | "publisher"
      | "visibility"
      | "origins"
      | "namespaceId"
  ]?: SearchQuery[K] | undefined;
};

// Editing filters replaces submitted criteria without carrying an obsolete cursor or changing text.
export function applyFilters(
  query: SearchQuery,
  filters: FilterValues,
): SearchQuery {
  const {
    cursor: _cursor,
    origins: _origins,
    namespaceId: _namespaceId,
    tags: _tags,
    categories: _categories,
    license: _license,
    publisher: _publisher,
    visibility: _visibility,
    kinds: _kinds,
    ...rest
  } = query;
  return {
    ...rest,
    ...(filters.namespaceId
      ? {
          namespaceId: filters.namespaceId,
          origins: ["local"] as const,
        }
      : filters.origins?.length
        ? { origins: [...filters.origins] }
        : {}),
    ...(filters.kinds?.length ? { kinds: [...filters.kinds] } : {}),
    ...(filters.tags?.length ? { tags: [...filters.tags] } : {}),
    ...(filters.categories?.length
      ? { categories: [...filters.categories] }
      : {}),
    ...(filters.license?.trim() ? { license: filters.license.trim() } : {}),
    ...(filters.publisher?.trim()
      ? { publisher: filters.publisher.trim() }
      : {}),
    ...(filters.visibility ? { visibility: filters.visibility } : {}),
  };
}

/** Add one exact tag in insertion order; commas remain part of the value. */
export function mergeTags(selected: string[], input: string): string[] {
  const tags = [
    ...new Set([...selected, ...(input.trim() ? [input.trim()] : [])]),
  ];
  if (
    tags.length > 32 ||
    tags.some((tag) => [...tag].length > 80 || tag.includes("\0"))
  ) {
    throw new Error("Use up to 32 tags, with no more than 80 characters each.");
  }
  return tags;
}
