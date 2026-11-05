// Local facet refinement uses only filters supported by Catalog Explore.
import type { components } from "../api/schema";
import { kindChoices } from "../resource-kinds";

export type FacetQuery = components["schemas"]["CatalogExploreQuery"];

/** Add a returned value to submitted criteria; undefined means no valid action.
 * An already selected value returns the same query and must not trigger a read. */
export function refineFacet(
  query: FacetQuery,
  field: string,
  value: string,
): FacetQuery | undefined {
  if (
    field === "namespace" &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
      value,
    )
  )
    return query.namespaceId === value
      ? query
      : { ...query, namespaceId: value };
  if (field === "kind") {
    const kind = kindChoices.find((choice) => choice.value === value)?.value;
    if (!kind) return;
    return query.kinds?.includes(kind)
      ? query
      : { ...query, kinds: [...(query.kinds ?? []), kind] };
  }
  if (field === "tag") {
    if (query.tags?.includes(value)) return query;
    if (!value || [...value].length > 80 || (query.tags?.length ?? 0) >= 32)
      return;
    return { ...query, tags: [...(query.tags ?? []), value] };
  }
  if (field === "category") {
    if (query.categories?.includes(value)) return query;
    if (
      !value.trim() ||
      [...value].length > 80 ||
      value.includes("\0") ||
      (query.categories?.length ?? 0) >= 16
    )
      return;
    return { ...query, categories: [...(query.categories ?? []), value] };
  }
  if (field === "publisher" && value)
    return query.publisher === value ? query : { ...query, publisher: value };
  if (
    field === "visibility" &&
    (value === "private" || value === "tenant" || value === "public")
  )
    return query.visibility === value ? query : { ...query, visibility: value };
}
