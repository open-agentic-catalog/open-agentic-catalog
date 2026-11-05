// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { RecordNavigation, localRecordSections } from "./RecordNavigation";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  history.replaceState(null, "", "/");
});

it("focuses only known anchors after lazy arrival, repeat selection and history changes", () => {
  history.replaceState(null, "", "/unit/catalog/example#record-handoff");
  const scroll = vi.fn();
  render(
    <>
      <RecordNavigation sections={localRecordSections} />
      <h2
        id="record-overview"
        tabIndex={-1}
        ref={(node) => {
          if (node) node.scrollIntoView = scroll;
        }}
      >
        Overview
      </h2>
      <h2
        id="record-handoff"
        tabIndex={-1}
        ref={(node) => {
          if (node) node.scrollIntoView = scroll;
        }}
      >
        Use this resource
      </h2>
    </>,
  );
  expect(document.activeElement).toBe(
    screen.getByRole("heading", { name: "Use this resource" }),
  );
  expect(scroll).toHaveBeenLastCalledWith({ block: "start" });
  fireEvent.click(screen.getByRole("link", { name: "Use this resource" }));
  expect(scroll).toHaveBeenCalledTimes(2);
  history.replaceState(null, "", "#record-overview");
  fireEvent(window, new HashChangeEvent("hashchange"));
  expect(document.activeElement).toBe(
    screen.getByRole("heading", { name: "Overview" }),
  );
  const count = scroll.mock.calls.length;
  history.replaceState(null, "", "#arbitrary-unrecognized-target");
  fireEvent(window, new HashChangeEvent("hashchange"));
  expect(scroll).toHaveBeenCalledTimes(count);
});

it("does not focus an absent target and removes its history listener on unmount", () => {
  history.replaceState(null, "", "/unit/catalog/example#record-evidence");
  const view = render(<RecordNavigation sections={localRecordSections} />);
  const listener = vi.spyOn(window, "removeEventListener");
  view.unmount();
  expect(listener).toHaveBeenCalledWith("hashchange", expect.any(Function));
});
