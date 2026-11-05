import { Fragment, type ReactNode } from "react";

/** Keep read-only facts in a flat definition list with the caller's existing styles. */
export function Facts({
  rows,
  className = "workspace-kv",
}: {
  className?: string;
  rows: readonly (readonly [
    label: string,
    value: ReactNode,
    valueClass?: string,
  ])[];
}) {
  return (
    <dl className={className}>
      {rows.map(([label, value, valueClass]) => (
        <Fragment key={label}>
          <dt>{label}</dt>
          <dd className={valueClass}>{value}</dd>
        </Fragment>
      ))}
    </dl>
  );
}
