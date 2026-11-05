// @vitest-environment jsdom
// Presentation names the exact loaded kind, lifecycle or difference without changing evidence.
import { afterEach, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { KindBadge } from "./KindBadge";
import { ReleaseState } from "./ReleaseState";
import { SnapshotState } from "./ActiveSnapshot";
import {
  ChangeLabel,
  MetadataChanges,
  metadataChanges,
} from "./RevisionComparison";

afterEach(cleanup);

it.each([
  ["agent", "Agent"],
  ["mcp_server", "MCP Server"],
  ["skill", "Skill"],
  ["plugin", "Plugin"],
] as const)(
  "labels %s with its existing color and decorative glyph",
  (kind, label) => {
    render(<KindBadge kind={kind} />);
    const badge = screen.getByText(label);
    expect(badge.classList.contains("catalog-kind-badge")).toBe(true);
    expect(badge.classList.contains(kind)).toBe(true);
    expect(
      badge.querySelector('svg[aria-hidden="true"][focusable="false"]'),
    ).toBeTruthy();
    expect(badge.querySelector("svg")?.getAttribute("stroke")).toBe(
      "currentColor",
    );
    expect(screen.queryByRole("img")).toBeNull();
    expect(screen.queryByRole("link")).toBeNull();
  },
);

it.each([
  ["published", "success"],
  ["deprecated", "warning"],
  ["retired", "neutral"],
  ["superseded", "neutral"],
  ["revoked", "danger"],
] as const)(
  "describes the %s release disposition with %s tone",
  (state, tone) => {
    render(<ReleaseState state={state} />);
    expect(screen.getByText(state).getAttribute("data-tone")).toBe(tone);
  },
);

it.each([
  ["candidate", "info"],
  ["active", "info"],
  ["retired", "neutral"],
] as const)("does not imply host health from a %s snapshot", (state, tone) => {
  render(<SnapshotState state={state} />);
  expect(screen.getByText(state).getAttribute("data-tone")).toBe(tone);
  expect(screen.queryByText(/healthy|trusted|verified/i)).toBeNull();
});

it.each([
  ["Added", "success"],
  ["Removed", "warning"],
  ["Changed", "info"],
] as const)("retains the %s difference label with %s tone", (change, tone) => {
  render(<ChangeLabel change={change} />);
  expect(screen.getByText(`${change}:`).getAttribute("data-tone")).toBe(tone);
});

it("keeps exact before/after differences and inert hostile text beside the cues", () => {
  const before = { removed: "retained old value", changed: "before" };
  const after = { added: "<script>literal</script>", changed: "after" };
  render(<MetadataChanges changes={metadataChanges(before, after)} />);
  for (const label of ["Added added", "Removed removed", "Changed changed"])
    expect(screen.getByRole("region", { name: label })).toBeTruthy();
  for (const label of ["Added: added", "Removed: removed", "Changed: changed"])
    expect(screen.getByRole("heading", { name: label })).toBeTruthy();
  for (const value of [
    "retained old value",
    "before",
    "after",
    "<script>literal</script>",
  ])
    expect(
      screen.getAllByText((text) => text.includes(value)).length,
    ).toBeGreaterThan(0);
  expect(document.querySelector("script")).toBeNull();
  expect(screen.queryByRole("link")).toBeNull();
});
