// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { DocumentChanges } from "./DocumentChanges";
afterEach(cleanup);

it("navigates deterministic regions with focus, announcements and endpoint controls", () => {
  render(
    <DocumentChanges
      before={'{"url":"old","n":1.0}'}
      after={'{"url":"new","n":1e0}'}
    />,
  );
  const previous = screen.getByRole("button", { name: "Previous change" });
  const next = screen.getByRole("button", { name: "Next change" });
  // jsdom has no scrolling geometry; the real browser regression checks that
  // keyboard navigation exposes the heading below the sticky header.
  for (const region of screen.getAllByRole("region", { name: /^Change / }))
    region.scrollIntoView = vi.fn();
  expect(screen.getByRole("status").textContent).toBe("2 changed regions.");
  expect(previous).toHaveProperty("disabled", true);
  fireEvent.click(next);
  expect(screen.getByRole("status").textContent).toBe("Change 1 of 2.");
  expect(document.activeElement).toBe(
    screen.getByRole("region", { name: "Change 1: Replaced text" }),
  );
  fireEvent.click(next);
  expect(document.activeElement).toBe(
    screen.getByRole("region", { name: "Change 2: Replaced text" }),
  );
  expect(next).toHaveProperty("disabled", true);
  fireEvent.click(previous);
  expect(document.activeElement).toBe(
    screen.getByRole("region", { name: "Change 1: Replaced text" }),
  );
  expect(previous).toHaveProperty("disabled", true);
  const first = screen.getByRole("region", { name: "Change 1: Replaced text" });
  const second = screen.getByRole("region", {
    name: "Change 2: Replaced text",
  });
  fireEvent.keyDown(first, { key: "ArrowDown", altKey: true });
  expect(document.activeElement).toBe(second);
  fireEvent.keyDown(second, { key: "ArrowDown", altKey: true });
  expect(document.activeElement).toBe(second);
  fireEvent.keyDown(second, { key: "ArrowUp", altKey: true });
  expect(document.activeElement).toBe(first);
  fireEvent.keyDown(first, { key: "ArrowUp", altKey: true });
  expect(document.activeElement).toBe(first);
  for (const event of [
    { key: "ArrowDown" },
    { key: "Enter", altKey: true },
    { key: "ArrowDown", altKey: true, ctrlKey: true },
    { key: "ArrowDown", altKey: true, metaKey: true },
  ])
    fireEvent.keyDown(first, event);
  expect(document.activeElement).toBe(first);
  fireEvent.keyDown(
    within(first).getByRole("group", { name: "Before change 1" }),
    { key: "ArrowDown", altKey: true },
  );
  expect(document.activeElement).toBe(first);
});

it.each([
  ["", "<script>\t\uFEFF\r\n", "Added text"],
  ["<script>\t\uFEFF\r\n", "", "Removed text"],
])(
  "labels one-sided edits and safely renders original characters: %s",
  (before, after, label) => {
    render(<DocumentChanges before={before} after={after} />);
    const region = screen.getByRole("region", { name: `Change 1: ${label}` });
    expect(region.textContent).toContain("[no text]");
    expect(region.textContent).toContain("<script>\\t[BOM]\\r\\n");
    expect(document.querySelector("script")).toBeNull();
    expect(within(region).getByRole("heading").textContent).toContain(label);
  },
);

it("keeps unchanged and unavailable states honest", () => {
  const view = render(<DocumentChanges before="same" after="same" />);
  expect(screen.getByRole("status").textContent).toBe("No document changes.");
  expect(screen.queryByRole("button")).toBeNull();
  view.rerender(<DocumentChanges before={"a".repeat(70000)} after="other" />);
  expect(screen.getByRole("status").textContent).toContain(
    "Detailed change navigation is unavailable",
  );
  expect(screen.queryByRole("button")).toBeNull();
  expect(screen.queryByRole("region")).toBeNull();
});
