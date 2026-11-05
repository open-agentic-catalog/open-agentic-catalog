// Fixed owned-fixture classifications exclude query text, tokens and private causes.
export function searchContinuationRefusal(status, problem) {
  if (status !== 400 || problem?.code !== "INVALID_ARGUMENT")
    return "unclassified";
  switch (problem.detail) {
    case "catalog cursor is invalid for this request":
      return "catalog_binding";
    case "catalog cursor has expired":
      return "catalog_expired";
    case "catalog cursor is stale":
      return "catalog_generation";
    case "live search cursor is invalid or expired; restart the search":
      return "live_cursor_validation";
    default:
      return "unclassified";
  }
}
