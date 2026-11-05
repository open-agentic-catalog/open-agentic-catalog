import { useEffect, useId, useRef, useState, type ReactNode } from "react";

/** Keep every column available, with an explicit keyboard stop only on overflow. */
export function ScrollableTable({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const hint = useId();
  const [overflow, setOverflow] = useState(false);
  useEffect(() => {
    const node = ref.current!;
    const measure = () => setOverflow(node.scrollWidth > node.clientWidth + 1);
    measure();
    const observer =
      typeof ResizeObserver === "undefined"
        ? undefined
        : new ResizeObserver(measure);
    observer?.observe(node);
    window.addEventListener("resize", measure);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, [children]);
  return (
    <>
      {overflow && (
        <p id={hint} className="muted workspace-table-hint">
          More columns are available. Focus the table and use Left/Right arrow
          keys, or scroll horizontally.
        </p>
      )}
      <div
        ref={ref}
        className="workspace-table-wrap"
        role="region"
        aria-label={label}
        aria-describedby={overflow ? hint : undefined}
        tabIndex={overflow ? 0 : undefined}
      >
        {children}
      </div>
    </>
  );
}
