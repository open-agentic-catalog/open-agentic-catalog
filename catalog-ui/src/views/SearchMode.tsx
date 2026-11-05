/** Explains the retrieval used for displayed results, not merely deployment capability. */
export function SearchMode({
  result,
  browsing = false,
}: {
  browsing?: boolean;
  result: { tier?: string; fallbackReason?: string; partialIndex?: boolean };
}) {
  const reasons: Record<string, string> = {
    indexing:
      "The semantic index is being prepared. These results use keyword search.",
    provider_unavailable:
      "Semantic search is temporarily unavailable. These results use keyword search.",
    index_changed:
      "The catalog changed during this request. These results use keyword search.",
    work_limit:
      "The semantic matching limit was reached. These results use keyword search; add filters to narrow your search.",
  };
  return (
    <div className="muted" role="status" aria-label="Search method">
      {!browsing && (
        <span>
          {result.tier === "hybrid"
            ? "Keyword and meaning-based search"
            : "Keyword search"}
        </span>
      )}
      <details>
        <summary>How search works</summary>
        <p>
          {browsing
            ? "With no search text, results are listed by name unless you choose another order."
            : result.tier === "hybrid"
              ? "Search matches your words and their meaning. The most relevant local and synced matches appear first, followed by live results."
              : "Search matches words in resource names, descriptions, tags and other searchable metadata."}
        </p>
      </details>
      {result.fallbackReason &&
        Object.hasOwn(reasons, result.fallbackReason) && (
          <p>{reasons[result.fallbackReason]}</p>
        )}
      {result.partialIndex && (
        <p>Some items can only be matched by keywords.</p>
      )}
    </div>
  );
}
