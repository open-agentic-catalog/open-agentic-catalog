// @vitest-environment jsdom
// Bundled glyphs remain decorative, labelled by their controls and independent of remote assets.
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { Icon, controlIcons, type IconName } from "./Icon";

afterEach(cleanup);

const names: IconName[] = [
  "star",
  "catalog",
  "agent",
  "mcp_server",
  "skill",
  "plugin",
  "search",
  "filters",
  "list",
  "arrow",
  "first",
  "chevron",
  "close",
  "access",
  "user",
  "sun",
  "moon",
  "monitor",
  "webhook",
  "history",
  "check",
  "warning",
  "publish",
  "logout",
  "globe",
  "book",
  "external",
  "add",
  "edit",
  "save",
  "refresh",
  "copy",
  "download",
  "compare",
  "remove",
  "bookmark",
  "message",
  "settings",
  "group",
  "pause",
  "play",
  "key",
  "eye",
];

it.each(names)("renders %s as decorative local geometry", (name) => {
  const { container } = render(<Icon name={name} />);
  const svg = container.querySelector("svg")!;
  expect(svg.getAttribute("aria-hidden")).toBe("true");
  expect(svg.getAttribute("focusable")).toBe("false");
  expect(svg.getAttribute("stroke")).toBe("currentColor");
  expect(svg.getAttribute("fill")).toBe("none");
  expect(svg.getAttribute("viewBox")).toBe("0 0 24 24");
  expect(svg.children.length).toBeGreaterThan(0);
  expect(
    Array.from(svg.children).every((child) =>
      ["path", "rect", "circle", "ellipse"].includes(child.tagName),
    ),
  ).toBe(true);
  expect(screen.queryByRole("img")).toBeNull();
});

it("keeps the visible label as the button name and supports compact sizing", () => {
  const view = render(
    <button>
      <Icon name="save" /> Save changes
    </button>,
  );
  const button = screen.getByRole("button", { name: "Save changes" });
  const svg = button.querySelector("svg")!;
  expect(svg.getAttribute("width")).toBe("18");
  expect(svg.getAttribute("height")).toBe("18");
  view.rerender(
    <button>
      <Icon name="save" size={14} /> Save changes
    </button>,
  );
  expect(svg.getAttribute("width")).toBe("14");
  expect(svg.getAttribute("height")).toBe("14");
  expect(screen.getByRole("button", { name: "Save changes" })).toBe(button);
});

it.each(names)("reuses the %s control glyph at its fixed size", (name) => {
  const { container, rerender } = render(
    <button>{controlIcons[name]} Action</button>,
  );
  const before = container.querySelector("svg")!.outerHTML;
  expect(screen.getByRole("button", { name: "Action" })).toBeTruthy();
  expect(container.querySelector("svg")!.getAttribute("width")).toBe("16");
  expect(container.querySelector("svg")!.getAttribute("aria-hidden")).toBe(
    "true",
  );
  rerender(
    <button>
      <Icon name={name} size={16} /> Action
    </button>,
  );
  expect(container.querySelector("svg")!.outerHTML).toBe(before);
});
