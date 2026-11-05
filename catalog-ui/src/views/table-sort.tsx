import { useState } from "react";
import { Icon } from "../shell/Icon";

const collator = new Intl.Collator(undefined, {
  numeric: true,
  sensitivity: "base",
});

/** Compare displayed text naturally, retaining the original order for ties. */
export function compareTableText(left: string, right: string): number {
  return collator.compare(left, right);
}

/** Sort only the loaded page without mutating its response or changing cursors. */
export function usePageSort<T, K extends string>(
  items: readonly T[] | undefined,
  comparators: Record<K, (left: T, right: T) => number>,
  onSort?: () => void,
) {
  const [selection, setSelection] = useState<{
    key: K;
    descending: boolean;
  }>();
  const rows = [...(items ?? [])];
  if (selection) {
    const { key, descending } = selection;
    rows.sort((left, right) =>
      descending
        ? comparators[key](right, left)
        : comparators[key](left, right),
    );
  }
  return {
    rows,
    selection,
    heading: (label: string, key: K) => {
      const active = selection?.key === key;
      const descending = active && selection.descending;
      return (
        <th
          scope="col"
          aria-sort={
            active ? (descending ? "descending" : "ascending") : "none"
          }
        >
          <button
            className="table-sort-heading"
            type="button"
            aria-label={`Sort by ${label.toLowerCase()}, ${active && !descending ? "descending" : "ascending"}`}
            onClick={() => {
              setSelection({ key, descending: active && !descending });
              onSort?.();
            }}
          >
            {label}
            <span
              aria-hidden="true"
              style={
                active
                  ? {
                      display: "inline-flex",
                      transform: `rotate(${descending ? 90 : -90}deg)`,
                    }
                  : undefined
              }
            >
              {active ? <Icon name="arrow" size={14} /> : "↕"}
            </span>
          </button>
        </th>
      );
    },
  };
}
