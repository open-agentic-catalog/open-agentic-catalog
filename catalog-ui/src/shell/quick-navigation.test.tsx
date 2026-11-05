// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { installDialog, sessionAccess } from "../../tests/unit/fixtures";
import { testAccess } from "../../tests/unit/access-fixtures";
import { QuickNavigation } from "./QuickNavigation";

let restore: () => void;
beforeEach(() => {
  restore = installDialog();
});
afterEach(() => {
  cleanup();
  restore();
});

it("filters fixed permitted pages, supports modal Escape and removes stale policy choices", () => {
  const admin = { ...sessionAccess, ...testAccess(["tenant_admin"]) };
  const view = render(<QuickNavigation access={admin} basePath="/unit" />);
  const opener = screen.getByRole("button", { name: "Quick navigation" });
  fireEvent.click(opener);
  const dialog = screen.getByRole("dialog");
  const input = within(dialog).getByRole("textbox", { name: "Find a page" });
  fireEvent.change(input, { target: { value: " aBoUt " } });
  input.focus();
  view.rerender(<QuickNavigation access={{ ...admin }} basePath="/unit" />);
  expect(document.activeElement).toBe(input);
  expect((input as HTMLInputElement).value).toBe(" aBoUt ");
  expect(
    within(dialog).getByRole("link", { name: /About/ }).getAttribute("href"),
  ).toBe("/unit/about");
  fireEvent.change(input, { target: { value: "private resource name" } });
  expect(within(dialog).getByRole("status").textContent).toBe(
    "No matching pages.",
  );
  fireEvent(dialog, new Event("cancel", { cancelable: true }));
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(document.activeElement).toBe(opener);
  fireEvent.click(opener);
  expect(
    within(screen.getByRole("dialog")).getByRole("link", { name: /Settings/ }),
  ).toBeTruthy();
  view.rerender(<QuickNavigation access={sessionAccess} basePath="/unit" />);
  expect(screen.queryByRole("dialog")).toBeNull();
  fireEvent.click(opener);
  expect(
    within(screen.getByRole("dialog")).queryByRole("link", {
      name: /Settings/,
    }),
  ).toBeNull();
  fireEvent.click(
    screen.getByRole("button", { name: "Close quick navigation" }),
  );
  expect(document.activeElement).toBe(opener);
});

it("uses Ctrl/Command+K outside editors and other dialogs only", () => {
  render(
    <>
      <div className="header-actions">
        <details open>
          <summary>Header</summary>
        </details>
      </div>
      <input aria-label="Draft" />
      <div contentEditable suppressContentEditableWarning>
        Private draft
      </div>
      <dialog aria-label="Other" />
      <QuickNavigation access={sessionAccess} basePath="" />
    </>,
  );
  for (const modifiers of [{ ctrlKey: true }, { metaKey: true }]) {
    fireEvent.keyDown(document, { key: "k", ...modifiers });
    expect(
      screen.getByRole("dialog", { name: "Quick navigation" }),
    ).toBeTruthy();
    fireEvent.click(
      screen.getByRole("button", { name: "Close quick navigation" }),
    );
  }
  for (const target of [
    screen.getByRole("textbox", { name: "Draft" }),
    screen.getByText("Private draft"),
  ]) {
    fireEvent.keyDown(target, { key: "k", ctrlKey: true });
    expect(
      screen.queryByRole("dialog", { name: "Quick navigation" }),
    ).toBeNull();
  }
  for (const key of [
    { key: "k" },
    { key: "x", ctrlKey: true },
    { key: "k", ctrlKey: true, altKey: true },
    { key: "k", metaKey: true, shiftKey: true },
  ])
    fireEvent.keyDown(document, key);
  const header = screen.getByText("Header").closest("details")!;
  header.open = true;
  fireEvent.click(screen.getByRole("button", { name: "Quick navigation" }));
  expect(header.open).toBe(false);
  fireEvent.click(
    screen.getByRole("button", { name: "Close quick navigation" }),
  );
  const other = document.querySelector<HTMLDialogElement>(
    'dialog[aria-label="Other"]',
  )!;
  other.showModal();
  fireEvent.keyDown(document, { key: "k", ctrlKey: true });
  expect(screen.queryByRole("dialog", { name: "Quick navigation" })).toBeNull();
  other.close();
  const prevented = new KeyboardEvent("keydown", {
    key: "k",
    ctrlKey: true,
    cancelable: true,
  });
  prevented.preventDefault();
  fireEvent(document, prevented);
  expect(screen.queryByRole("dialog", { name: "Quick navigation" })).toBeNull();
});

it("closes after selecting a page section and retains ordinary link activation", () => {
  const admin = { ...sessionAccess, ...testAccess(["tenant_admin"]) };
  render(<QuickNavigation access={admin} basePath="/unit" />);
  fireEvent.click(screen.getByRole("button", { name: "Quick navigation" }));
  const link = within(screen.getByRole("dialog")).getByRole("link", {
    name: /Search indexing/,
  });
  expect(link.getAttribute("href")).toBe("/unit/service#search-indexing");
  let canceledByUI = true;
  const nativeNavigation = (event: MouseEvent) => {
    canceledByUI = event.defaultPrevented;
    event.preventDefault();
  };
  document.addEventListener("click", nativeNavigation, { once: true });
  fireEvent.click(link);
  expect(canceledByUI).toBe(false);
  expect(screen.queryByRole("dialog")).toBeNull();
});
