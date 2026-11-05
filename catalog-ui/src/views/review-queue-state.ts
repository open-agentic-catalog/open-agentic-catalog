/** Queue URLs retain only applied exact references and the selected state. */
export const reviewLabels = {
  submitted: "submitted",
  approved: "approved",
  changes_required: "changes required",
  rejected: "rejected",
};

export type ReviewFilters = {
  listingId: string;
  state: string;
  unreleased?: string;
};

export function reviewFilters(search: string): ReviewFilters {
  const query = new URLSearchParams(search);
  const listingId = query.get("listing") ?? "";
  return {
    listingId,
    ...(query.has("unreleased")
      ? {
          unreleased:
            query.getAll("unreleased").length === 1
              ? query.get("unreleased")!
              : "unsupported",
        }
      : {}),
    // An explicit empty state means all states, including without a Listing.
    // Unknown URL states remain subject to the API's existing validation.
    state: query.get("state") ?? (listingId ? "" : "submitted"),
  };
}

export function reviewQueueURL(basePath: string, filters: ReviewFilters) {
  const query = new URLSearchParams({ state: filters.state });
  if (filters.unreleased !== undefined)
    query.set("unreleased", filters.unreleased);
  if (filters.listingId) query.set("listing", filters.listingId);
  return `${basePath}/reviews?${query}`;
}
