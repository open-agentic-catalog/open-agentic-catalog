// @vitest-environment jsdom
// These isolated labels describe specific outcomes, never permission, health or trust summaries.
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import {
  WorkState,
  AccessState,
  SourceState,
  SourceSnapshotState,
  CompletenessState,
  StructureState,
} from "./operational-labels";
import { commandSummary } from "./workspace";

afterEach(cleanup);

const groups = [
  [
    AccessState,
    [
      ["active", "Active", "info"],
      ["suspended", "Suspended", "warning"],
      ["offboarded", "Offboarded", "neutral"],
      ["archived", "Archived", "neutral"],
      ["revoked", "Revoked", "neutral"],
    ],
  ],
  [
    WorkState,
    [
      ["queued", "Queued", "neutral"],
      ["pending", "Pending", "neutral"],
      ["running", "Running", "info"],
      ["waiting", "Waiting", "warning"],
      ["succeeded", "Succeeded", "success"],
      ["failed", "Failed", "danger"],
      ["cancelled", "Cancelled", "neutral"],
      ["dead_letter", "Automatic retries stopped", "danger"],
    ],
  ],
  [
    SourceState,
    [
      ["active", "Active", "info"],
      ["paused", "Paused", "warning"],
      ["error", "Error", "danger"],
      ["removed", "Removed", "neutral"],
    ],
  ],
  [
    SourceSnapshotState,
    [
      ["complete", "Complete", "info"],
      ["incomplete", "Incomplete", "warning"],
      ["failed", "Failed", "danger"],
    ],
  ],
  [
    CompletenessState,
    [
      ["complete", "Complete", "info"],
      ["projection", "Projection", "warning"],
      ["unresolved", "Unresolved", "warning"],
    ],
  ],
  [
    StructureState,
    [
      ["valid", "Valid", "success"],
      ["invalid", "Invalid", "danger"],
    ],
  ],
] as const;
for (const [Component, cases] of groups) {
  it.each([...cases, ["future_code", "future_code", "neutral"] as const])(
    `${Component.name} keeps %s labelled with %s and %s tone`,
    (state, label, tone) => {
      render(<Component state={state} />);
      const badge = screen.getByText(label);
      expect(badge.getAttribute("data-tone")).toBe(tone);
      expect(badge.getAttribute("data-state")).toBe(state);
      expect(screen.queryByRole("img")).toBeNull();
      expect(screen.queryByRole("button")).toBeNull();
      expect(screen.queryByText(/trusted|approved|installed/i)).toBeNull();
    },
  );
}

it("preserves exact confirmation strings and order without binding or sending commands", () => {
  const original = [
    ["Action", "Remove source"],
    ["Reason", "  first\nsecond  "],
    ["Literal", "<script>text</script>"],
    ["Empty", ""],
  ] as const;
  expect(commandSummary(original)).toEqual([
    { label: "Action", value: "Remove source" },
    { label: "Reason", value: "  first\nsecond  " },
    { label: "Literal", value: "<script>text</script>" },
    { label: "Empty", value: "" },
  ]);
  expect(original[1][1]).toBe("  first\nsecond  ");
  expect(commandSummary([])).toEqual([]);
});
