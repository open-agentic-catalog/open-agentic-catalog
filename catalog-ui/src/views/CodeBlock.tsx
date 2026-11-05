// Code presentation retains original text and keeps formatting/highlighting inside explicit work limits.
import { useMemo, useState } from "react";
import { jsonDocument } from "./code-document";
import { CopyText } from "./CopyReference";
import "./code-block.css";

/** Display only: original text stays available; no execution or HTML injection. */
export function CodeBlock({
  text,
  original = false,
  rawText = false,
}: {
  text: string;
  original?: boolean;
  rawText?: boolean;
}) {
  const document = useMemo(() => jsonDocument(text), [text]);
  const [formatted, setFormatted] = useState(!original);
  const [wrap, setWrap] = useState(true);
  const tokens =
    document && (formatted ? document.formatted : document.original);
  return (
    <div className="code-block">
      <div className="code-toolbar">
        <span className="code-language">
          {document ? "JSON" : "Plain text"}
        </span>
        <div className="code-controls">
          {rawText && (
            <CopyText
              label="Copy document text"
              text={text}
              disabled={new TextEncoder().encode(text).length > 2 * 1024 * 1024}
            />
          )}
          {document && (
            <button
              type="button"
              aria-pressed={formatted}
              onClick={() => setFormatted(!formatted)}
              title="Indent JSON for reading, or show its original whitespace"
            >
              Format JSON
            </button>
          )}
          <button
            type="button"
            aria-pressed={wrap}
            onClick={() => setWrap(!wrap)}
            title="Wrap long lines to fit this panel"
          >
            Wrap lines
          </button>
        </div>
      </div>
      <pre
        className="code-content"
        data-wrap={wrap}
        // Name the scrollable content without adding a landmark for each block.
        role="group"
        tabIndex={0}
        aria-label={document ? "JSON document" : "Text document"}
      >
        <code>
          {tokens
            ? tokens.map((token, index) =>
                token.kind === "space" ? (
                  token.text
                ) : (
                  <span key={index} className={`syntax-${token.kind}`}>
                    {token.text}
                  </span>
                ),
              )
            : text}
        </code>
      </pre>
    </div>
  );
}

/** Render a decoded value as JSON; use CodeBlock directly for exact retained text. */
export function JSONBlock({ value }: { value: unknown }) {
  const text = useMemo(() => JSON.stringify(value) ?? "", [value]);
  return <CodeBlock text={text} />;
}
