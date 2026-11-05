import type { ReactNode } from "react";
export function Observation({ data }: { data: { observedAt: string } }) {
  return (
    <p className="muted overview-observation">
      Updated{" "}
      <time
        dateTime={data.observedAt}
        title={new Date(data.observedAt).toLocaleString()}
      >
        {new Date(data.observedAt).toLocaleTimeString(undefined, {
          hour: "2-digit",
          minute: "2-digit",
        })}
      </time>
    </p>
  );
}

export function Panel({
  title,
  children,
  wide = false,
  compact = false,
  value,
}: {
  title: string;
  children: ReactNode;
  wide?: boolean;
  compact?: boolean;
  value?: ReactNode;
}) {
  return (
    <section
      className={`workspace-panel overview-panel${wide ? " overview-inventory" : ""}${compact ? " overview-stat" : ""}`}
      aria-label={title}
    >
      <header className="overview-panel-heading">
        <h2>{title}</h2>
        {value !== undefined && (
          <strong className="overview-number">{value}</strong>
        )}
      </header>
      {children}
    </section>
  );
}

export function CategoryBars({
  title,
  rows,
}: {
  title: string;
  rows: { label: ReactNode; count: number; tone?: string }[];
}) {
  const max = Math.max(1, ...rows.map((row) => row.count));
  return (
    <section aria-label={title}>
      <h3>{title}</h3>
      <ul className="operation-bars">
        {rows.map((row, index) => (
          <li key={index} data-tone={row.tone}>
            <span>{row.label}</span>
            <strong>{row.count.toLocaleString()}</strong>
            <div className="overview-track" aria-hidden="true">
              <span style={{ width: `${(100 * row.count) / max}%` }} />
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}
