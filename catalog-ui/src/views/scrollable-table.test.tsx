// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { ScrollableTable } from "./ScrollableTable";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
const contents = (
  <table>
    <thead>
      <tr>
        <th>Name</th>
        <th>Publication approval</th>
      </tr>
    </thead>
    <tbody>
      <tr>
        <td>Full name</td>
        <td>Approved</td>
      </tr>
    </tbody>
  </table>
);
it("adds and removes the keyboard stop and visible guidance on measured overflow", () => {
  let width = 800;
  let notify = () => {};
  const disconnect = vi.fn();
  vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockImplementation(
    () => width,
  );
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
  class Observer {
    constructor(callback: () => void) {
      notify = callback;
    }
    observe() {}
    disconnect = disconnect;
  }
  vi.stubGlobal("ResizeObserver", Observer);
  const view = render(
    <ScrollableTable label="Publishers">{contents}</ScrollableTable>,
  );
  const region = screen.getByRole("region", { name: "Publishers" });
  expect(region.getAttribute("tabindex")).toBeNull();
  act(() => {
    width = 1100;
    notify();
  });
  expect(region.getAttribute("tabindex")).toBe("0");
  const hint = screen.getByText(/More columns are available/);
  expect(region.getAttribute("aria-describedby")).toBe(hint.id);
  expect(
    screen.getByRole("columnheader", { name: "Publication approval" }),
  ).toBeTruthy();
  act(() => {
    width = 800;
    notify();
  });
  expect(region.getAttribute("tabindex")).toBeNull();
  expect(region.getAttribute("aria-describedby")).toBeNull();
  view.unmount();
  expect(disconnect).toHaveBeenCalledOnce();
});
it("remeasures changed content and supports resize without ResizeObserver", () => {
  vi.stubGlobal("ResizeObserver", undefined);
  let width = 600;
  const measured = vi
    .spyOn(HTMLElement.prototype, "scrollWidth", "get")
    .mockImplementation(() => width);
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
  const view = render(
    <ScrollableTable label="Rows">{contents}</ScrollableTable>,
  );
  expect(screen.getByRole("region").tabIndex).toBe(0);
  width = 300;
  view.rerender(
    <ScrollableTable label="Rows">
      <table />
    </ScrollableTable>,
  );
  expect(screen.getByRole("region").getAttribute("tabindex")).toBeNull();
  act(() => {
    width = 600;
    window.dispatchEvent(new Event("resize"));
  });
  expect(screen.getByRole("region").tabIndex).toBe(0);
  view.unmount();
  const calls = measured.mock.calls.length;
  window.dispatchEvent(new Event("resize"));
  expect(measured).toHaveBeenCalledTimes(calls);
});
