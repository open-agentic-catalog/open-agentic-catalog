import type { SearchResult } from "../api/client";
import "./declared-tools.css";

/** Declarations are untrusted plain text and refer to the exact selected release. */
export function DeclaredToolMatch({
  item,
  onInspect,
}: {
  item: SearchResult;
  onInspect: () => void;
}) {
  const match = item.toolMatch;
  if (!match || !item.local) return null;
  return (
    <section
      className="declared-tool-match"
      aria-label="Matching declared tools"
    >
      <strong>
        {match.matchedToolCount} matching declared{" "}
        {match.matchedToolCount === 1 ? "tool" : "tools"}
      </strong>
      <ul>
        {match.tools.map((tool) => (
          <li key={tool.name}>
            <code>{tool.name}</code>
            {tool.description && (
              <p>
                {tool.description}
                {tool.descriptionTruncated && "..."}
              </p>
            )}
          </li>
        ))}
      </ul>
      {match.truncated && (
        <p>Showing the first {match.tools.length} matches.</p>
      )}
      <p className="muted">
        Author-declared metadata. Tools have not been called or independently
        verified.
      </p>
      <button type="button" onClick={onInspect}>
        Inspect released declaration
      </button>
    </section>
  );
}
