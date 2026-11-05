// @vitest-environment jsdom
/** Retrieval labels follow the response and never guess from capability state. */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { SearchMode } from "./SearchMode";

afterEach(cleanup);

it("labels hybrid and partial indexing, then follows a replacement response", () => {
  const view = render(
    <SearchMode result={{ tier: "hybrid", partialIndex: true }} />,
  );
  expect(screen.getByText("Keyword and meaning-based search")).not.toBeNull();
  expect(
    screen.getByText("Some items can only be matched by keywords."),
  ).not.toBeNull();
  view.rerender(<SearchMode result={{ tier: "core" }} />);
  expect(screen.getByText("Keyword search")).not.toBeNull();
  expect(
    screen.queryByText("Some items can only be matched by keywords."),
  ).toBeNull();
});

it.each(["indexing", "provider_unavailable", "index_changed", "work_limit"])(
  "explains %s without exposing provider details",
  (reason) => {
    render(<SearchMode result={{ fallbackReason: reason }} />);
    expect(screen.getByText("Keyword search")).not.toBeNull();
    expect(screen.getByText(/These results use keyword search/)).not.toBeNull();
  },
);

it.each(["private server detail", "__proto__", "constructor", "toString"])(
  "ignores an unknown diagnostic: %s",
  (reason) => {
    render(<SearchMode result={{ fallbackReason: reason }} />);
    expect(
      screen.getByRole("status", { name: "Search method" }).textContent,
    ).toContain("Keyword search");
    expect(
      screen.getByText(/Search matches words in resource names/),
    ).toBeTruthy();
  },
);

it("explains browsing without a redundant search-method badge", () => {
  render(<SearchMode result={{ tier: "core" }} browsing />);
  expect(screen.queryByText("Keyword search")).toBeNull();
  expect(screen.getByText("How search works")).toBeTruthy();
  expect(
    screen.getByText(/With no search text, results are listed by name/),
  ).toBeTruthy();
});
