// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { ErrorBoundary } from "./ErrorBoundary";
import { Icon } from "./Icon";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("renders children normally and recovers a failed view with a full reload", () => {
  const error = new Error("synthetic missing chunk");
  const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
  const reload = vi.fn();
  vi.stubGlobal("location", { reload });
  const view = render(
    <ErrorBoundary>
      <h1>Working view</h1>
      <Icon name="book" size={24} />
    </ErrorBoundary>,
  );
  expect(screen.getByRole("heading").textContent).toBe("Working view");
  expect(view.container.querySelector("svg")?.getAttribute("width")).toBe("24");
  function Broken(): never {
    throw error;
  }
  view.rerender(
    <ErrorBoundary>
      <Broken />
    </ErrorBoundary>,
  );
  expect(screen.queryByText("Working view")).toBeNull();
  expect(screen.getByRole("heading").textContent).toBe(
    "The client could not load this view",
  );
  expect(view.container.textContent).not.toContain(error.message);
  expect(consoleError).toHaveBeenCalled();
  const reloadButton = screen.getByRole("button", { name: "Reload Catalog" });
  expect(reloadButton.querySelector("svg")?.getAttribute("aria-hidden")).toBe(
    "true",
  );
  expect(reloadButton.querySelector("svg")?.getAttribute("focusable")).toBe(
    "false",
  );
  fireEvent.click(reloadButton);
  expect(reload).toHaveBeenCalledOnce();
});
