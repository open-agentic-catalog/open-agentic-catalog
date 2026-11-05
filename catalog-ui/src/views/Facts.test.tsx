// @vitest-environment jsdom
import { useState } from "react";
import { afterEach, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Facts } from "./Facts";

afterEach(cleanup);

it("keeps fact labels and rich values as flat ordered definition-list children", () => {
  const rows = [
    ["Entries", 0],
    ["Finished", <time dateTime="2026-10-21T12:00:00Z">Today at noon</time>],
    ["Source", <a href="/sources/exact-source">Exact source</a>],
    ["Digest", <code>sha256:exact</code>],
    ["Optional outcome", undefined],
  ] as const;
  const { container } = render(<Facts rows={rows} />);
  const list = container.querySelector("dl")!;
  expect(list.className).toBe("workspace-kv");
  expect(
    [...list.children].map((node) => [node.tagName, node.textContent]),
  ).toEqual([
    ["DT", "Entries"],
    ["DD", "0"],
    ["DT", "Finished"],
    ["DD", "Today at noon"],
    ["DT", "Source"],
    ["DD", "Exact source"],
    ["DT", "Digest"],
    ["DD", "sha256:exact"],
    ["DT", "Optional outcome"],
    ["DD", ""],
  ]);
  expect(
    screen.getByRole("link", { name: "Exact source" }).getAttribute("href"),
  ).toBe("/sources/exact-source");
  expect(list.querySelector("time")?.getAttribute("datetime")).toBe(
    "2026-10-21T12:00:00Z",
  );
  expect(list.querySelector("code")?.textContent).toBe("sha256:exact");
});

it("retains a rich value's DOM and local state when current facts are refreshed", () => {
  function EvidenceToggle() {
    const [shown, setShown] = useState(false);
    return (
      <button onClick={() => setShown(!shown)} aria-expanded={shown}>
        {shown ? "Hide saved evidence" : "Show saved evidence"}
      </button>
    );
  }
  const { rerender } = render(
    <Facts
      rows={[
        ["Entries", 1],
        ["Evidence", <EvidenceToggle />],
      ]}
    />,
  );
  const button = screen.getByRole("button", { name: "Show saved evidence" });
  fireEvent.click(button);
  rerender(
    <Facts
      rows={[
        ["Entries", 2],
        ["Evidence", <EvidenceToggle />],
      ]}
    />,
  );
  expect(screen.getByRole("button", { name: "Hide saved evidence" })).toBe(
    button,
  );
  expect(button.getAttribute("aria-expanded")).toBe("true");
  expect(screen.getByText("Entries").nextElementSibling?.textContent).toBe("2");
  fireEvent.click(button);
  expect(screen.getByRole("button", { name: "Show saved evidence" })).toBe(
    button,
  );
});

it("preserves existing list and value classes without wrapping fact children", () => {
  const { container } = render(
    <Facts
      className="metadata-grid"
      rows={[
        ["Reason", "Exact\n retained reason", "release-disposition-reason"],
        ["Reference", <code>literal-id</code>],
      ]}
    />,
  );
  const list = container.querySelector("dl")!;
  expect(list.className).toBe("metadata-grid");
  expect(list.children.length).toBe(4);
  expect(list.children[1]!.className).toBe("release-disposition-reason");
  expect(list.children[1]!.textContent).toBe("Exact\n retained reason");
  expect(list.children[3]!.hasAttribute("class")).toBe(false);
  expect(list.children[3]!.firstElementChild?.tagName).toBe("CODE");
});
