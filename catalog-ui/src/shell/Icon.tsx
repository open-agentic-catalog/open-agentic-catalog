// Shared icons remain bundled SVG geometry without resource-controlled asset requests.
import type { ReactNode } from "react";

// Share repeated geometry to keep the bundled vocabulary small.
const catalogTile = (x: string, y: string) => (
  <rect x={x} y={y} width="7" height="7" rx="1.5" />
);
const circle = (cx: string, cy: string, r: string) => (
  <circle cx={cx} cy={cy} r={r} />
);
const path = (d: string) => <path d={d} />;

// Small, code-owned line icons. No external assets or per-record image fetches.
const shapes = {
  star: "m12 3 2.8 5.7 6.2.9-4.5 4.4 1.1 6.2L12 17.3l-5.6 2.9 1.1-6.2L3 9.6l6.2-.9Z",
  catalog: (
    <>
      {catalogTile("3", "3")}
      {catalogTile("14", "3")}
      {catalogTile("3", "14")}
      {catalogTile("14", "14")}
    </>
  ),
  agent: (
    <>
      <rect x="4" y="7" width="16" height="13" rx="4" />
      {path("M12 3v4M2 12v4m20-4v4M8 16h8")}
      {circle("8.5", "12", ".7")}
      {circle("15.5", "12", ".7")}
    </>
  ),
  mcp_server: "M8 3v5m8-5v5M6 8h12v3a6 6 0 0 1-12 0Zm6 9v4m-4 0h8",
  skill: "m13 2-9 12h7l-1 8 10-13h-7Z",
  plugin:
    "M9 3h6v4a3 3 0 1 1 4 4h2v10h-8v-3a3 3 0 1 0-6 0v3H3v-8h3a3 3 0 1 0 0-6H3V3h6Z",
  search: (
    <>
      {circle("10.5", "10.5", "6.5")}
      {path("m16 16 5 5")}
    </>
  ),
  filters: (
    <>
      {path("M4 6h16M4 12h16M4 18h16")}
      {circle("8", "6", "2")}
      {circle("16", "12", "2")}
      {circle("10", "18", "2")}
    </>
  ),
  list: "M8 5h13M8 12h13M8 19h13M3 5h.01M3 12h.01M3 19h.01",
  arrow: "M4 12h16m-6-6 6 6-6 6",
  first: "M5 4v16m13-16-8 8 8 8",
  chevron: "m9 5 7 7-7 7",
  close: "m6 6 12 12M6 18 18 6",
  pause: "M7 4v16M17 4v16",
  play: "m7 3 14 9-14 9Z",
  key: "M14 10a5 5 0 1 0-4 4l-7 7m3-3 3 3m0-6 3 3",
  eye: "M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12Zm13 0a3 3 0 1 0-6 0 3 3 0 0 0 6 0",
  add: "M12 5v14M5 12h14",
  edit: "m15 4 5 5M4 20l1-6L16 3l5 5L10 19Z",
  save: "M3 3h14l4 4v14H3Zm4 0v6h10V3M7 21v-8h10v8",
  refresh: "M20 7v5h-5m5 0a8 8 0 1 0-2 6",
  copy: "M9 9h12v12H9ZM15 5V3H3v12h2",
  download: "M12 3v12m-5-5 5 5 5-5M4 17v4h16v-4",
  compare: "M3 4h7v16H3Zm11 0h7v16h-7Z",
  remove: "M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7m4-7v7",
  bookmark: "M6 3h12v18l-6-4-6 4Z",
  message: "M3 3h18v14H9l-6 4Zm4 5h10M7 12h7",
  settings: "M4 5h16M4 12h16M4 19h16M8 3v4m8 3v4m-8 3v4",
  group:
    "M16 21v-2a4 4 0 0 0-8 0v2m13 0v-2a4 4 0 0 0-3-4M3 21v-2a4 4 0 0 1 3-4M12 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8m7 1a4 4 0 0 1 0 7M5 4a4 4 0 0 0 0 7",
  access: (
    <>
      {path("m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6Z")}
      {path("m8 12 3 3 5-6")}
    </>
  ),
  user: (
    <>
      {circle("12", "8", "4")}
      {path("M4 21v-2a8 8 0 0 1 16 0v2")}
    </>
  ),
  sun: (
    <>
      {circle("12", "12", "4")}
      {path("M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1 1m12 12 1 1M5 19l1-1M18 6l1-1")}
    </>
  ),
  moon: "M20 14a9 9 0 0 1-10-10 9 9 0 1 0 10 10Z",
  monitor: (
    <>
      <rect x="3" y="4" width="18" height="13" rx="2" />
      {path("M12 17v4m-4 0h8")}
    </>
  ),
  webhook: (
    <>
      {circle("12", "5", "3")}
      {circle("5", "18", "3")}
      {circle("19", "18", "3")}
      {path("m10.5 7.5-4 8m7-8 4 8M8 18h8")}
    </>
  ),
  history: (
    <>
      {path("M3 11a9 9 0 1 1 2.6 7.4M3 4v7h7")}
      {path("M12 7v5l3 2")}
    </>
  ),
  check: "m5 12 4 4 10-10",
  warning: (
    <>
      {path("m12 3 10 18H2Z")}
      {path("M12 9v5m0 3h.01")}
    </>
  ),
  publish: "M12 16V3m-5 5 5-5 5 5M4 15v6h16v-6",
  logout: "M9 3H4v18h5M10 12h11m-4-4 4 4-4 4",
  globe: (
    <>
      {circle("12", "12", "9")}
      <ellipse cx="12" cy="12" rx="4" ry="9" />
      {path("M3 12h18")}
    </>
  ),
  book: "M12 5v16M3 3c4-1 6 0 9 2 3-2 5-3 9-2v16c-4-1-6 0-9 2-3-2-5-3-9-2Z",
  external: "M14 3h7v7m0-7L10 14M10 3H3v18h18v-7",
} satisfies Record<string, ReactNode>;

export type IconName = keyof typeof shapes;
/** Render a decorative local SVG; surrounding text or the control supplies its accessible name. */
export function Icon({ name, size = 18 }: { name: IconName; size?: number }) {
  const shape = shapes[name];
  return (
    <svg
      className="icon"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.65"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {typeof shape === "string" ? path(shape) : shape}
    </svg>
  );
}

/** Reuse fixed-size decorative glyphs in labelled controls without repeated JSX. */
export const controlIcons = Object.fromEntries(
  (Object.keys(shapes) as IconName[]).map((name) => [
    name,
    <Icon name={name} size={16} />,
  ]),
) as Record<IconName, ReactNode>;
