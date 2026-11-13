import { useId, useMemo, useRef, useState } from "react";
import { documentChanges, visibleChangeText } from "./document-changes";

/** Local presentation of already verified, explicitly selected retained documents. */
export function DocumentChanges({
  before,
  after,
}: {
  before: string;
  after: string;
}) {
  const diff = useMemo(() => documentChanges(before, after), [before, after]);
  const [current, setCurrent] = useState<number>();
  const regions = useRef<(HTMLElement | null)[]>([]);
  const id = useId();
  if (!diff.available)
    return (
      <p role="status">
        Detailed change navigation is unavailable: these documents exceed the
        comparison work limits. Inspect the original documents below.
      </p>
    );
  const count = diff.changes.length;
  function move(index: number) {
    setCurrent(index);
    regions.current[index]!.focus();
  }
  return (
    <section aria-label="Document changes">
      <h4>Document changes</h4>
      <p role="status" aria-live="polite">
        {count === 0
          ? "No document changes."
          : current === undefined
            ? `${count} changed ${count === 1 ? "region" : "regions"}.`
            : `Change ${current + 1} of ${count}.`}
      </p>
      {count > 0 && (
        <>
          <div className="workspace-toolbar">
            <button
              type="button"
              disabled={current === undefined || current === 0}
              onClick={() => move(current! - 1)}
            >
              Previous change
            </button>
            <button
              type="button"
              disabled={current === count - 1}
              onClick={() => move(current === undefined ? 0 : current + 1)}
            >
              Next change
            </button>
          </div>
          <p>
            While a change has focus, Alt+Down moves to the next change and
            Alt+Up moves to the previous change.
          </p>
          <p>
            Removed text is crossed out; added text is underlined. In these
            excerpts, a middle dot marks a space, <code>{"\\t"}</code> a tab,{" "}
            <code>{"\\r"}</code> a carriage return, <code>{"\\n"}</code> a line
            feed, and [BOM] a byte order mark. Original text remains below.
          </p>
          <ol className="document-changes">
            {diff.changes.map((change, index) => (
              <li key={index}>
                <section
                  tabIndex={-1}
                  aria-labelledby={`${id}-${index}`}
                  aria-keyshortcuts="Alt+ArrowDown Alt+ArrowUp"
                  onKeyDown={(event) => {
                    if (
                      event.target !== event.currentTarget ||
                      !event.altKey ||
                      event.ctrlKey ||
                      event.metaKey ||
                      !["ArrowDown", "ArrowUp"].includes(event.key)
                    )
                      return;
                    event.preventDefault();
                    const next = index + (event.key === "ArrowDown" ? 1 : -1);
                    if (next >= 0 && next < count) move(next);
                  }}
                  ref={(element) => {
                    regions.current[index] = element;
                  }}
                >
                  <h5 id={`${id}-${index}`}>
                    Change {index + 1}:{" "}
                    {change.before.value
                      ? change.after.value
                        ? "Replaced text"
                        : "Removed text"
                      : "Added text"}
                  </h5>
                  <div className="revision-comparison-grid">
                    {(["before", "after"] as const).map((side) => {
                      const part = change[side];
                      const value = visibleChangeText(part.value);
                      return (
                        <div key={side}>
                          <p>
                            {side === "before" ? "Before" : "After"}: line{" "}
                            {part.line}, column {part.column}
                          </p>
                          <pre
                            role="group"
                            tabIndex={0}
                            aria-label={`${side === "before" ? "Before" : "After"} change ${index + 1}`}
                          >
                            <code>
                              {visibleChangeText(part.prefix)}
                              {value ? (
                                side === "before" ? (
                                  <del>{value}</del>
                                ) : (
                                  <ins>{value}</ins>
                                )
                              ) : (
                                <span className="muted">[no text]</span>
                              )}
                              {visibleChangeText(part.suffix)}
                            </code>
                          </pre>
                        </div>
                      );
                    })}
                  </div>
                </section>
              </li>
            ))}
          </ol>
        </>
      )}
    </section>
  );
}
