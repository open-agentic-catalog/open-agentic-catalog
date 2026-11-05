// @vitest-environment jsdom
// Presentation stays bound to prepared commands through confirmation and uncertain recovery.
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { deferred, installDialog } from "../../tests/unit/fixtures";
import { APIError } from "../api/client";
import type { IconName } from "../shell/Icon";
import { ActionForm } from "./authoring";
import { CommandDialog } from "./workspace";
import type { ActionIntent } from "./workspace";

let restoreDialog: () => void;
beforeEach(() => {
  restoreDialog = installDialog();
});
afterEach(() => {
  cleanup();
  restoreDialog();
  vi.restoreAllMocks();
});

const geometry = {
  remove: "M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7m4-7v7",
  refresh: "M20 7v5h-5m5 0a8 8 0 1 0-2 6",
};

/** Decorative geometry must not replace or change the visible action label. */
function expectIcon(button: HTMLElement, icon: keyof typeof geometry) {
  const svg = button.querySelector("svg")!;
  expect(svg.getAttribute("aria-hidden")).toBe("true");
  expect(svg.getAttribute("focusable")).toBe("false");
  expect(svg.querySelector("path")?.getAttribute("d")).toBe(geometry[icon]);
}

it.each<{
  intent: ActionIntent | undefined;
  expected: ActionIntent;
  icon: Extract<IconName, "remove" | "refresh"> | undefined;
}>([
  { intent: undefined, expected: "primary", icon: undefined },
  { intent: "secondary", expected: "secondary", icon: undefined },
  { intent: "danger", expected: "danger", icon: "remove" },
  { intent: "warning", expected: "warning", icon: "refresh" },
])(
  "uses explicit $expected presentation through preparation and cancellation",
  ({ intent, expected, icon }) => {
    const run = vi.fn(async () => "Saved");
    render(
      <ActionForm
        title="Delete staged item"
        description="Review the captured action."
        {...(intent ? { intent } : {})}
        {...(icon ? { icon } : {})}
        allowed
        prepare={() => run}
        onFailure={vi.fn()}
      />,
    );
    const opener = screen.getByRole("button", {
      name: "Delete staged item",
    });
    expect(opener.classList.contains(expected)).toBe(true);
    if (icon) expectIcon(opener, icon);
    else expect(opener.querySelector("svg")).toBeNull();
    opener.focus();
    fireEvent.click(opener);
    const dialog = screen.getByRole("dialog");
    const confirm = within(dialog).getByRole("button", {
      name: "Confirm",
    });
    expect(confirm.classList.contains(expected)).toBe(true);
    if (icon) expectIcon(confirm, icon);
    else expect(confirm.querySelector("svg")).toBeNull();
    expect(
      within(dialog)
        .getByRole("button", { name: "Cancel" })
        .classList.contains(expected),
    ).toBe(false);
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(run).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(opener);
  },
);

it("retains prepared presentation and exact inputs through busy and uncertain recovery after access loss", async () => {
  const pending = deferred<string>();
  const send = vi
    .fn<
      (
        key: string,
        target: string,
        body: { reason: string },
        etag: string,
      ) => Promise<string>
    >()
    .mockImplementationOnce(() => pending.promise)
    .mockResolvedValueOnce("Deleted original item.");
  const replacement = vi.fn(async () => "Replacement must not run.");
  let input = {
    target: "original-item",
    body: { reason: "Original reason" },
    etag: '"original-revision"',
  };
  const failure = vi.fn();
  const original = {
    title: "Delete original item",
    description: "Delete only the selected original item.",
    intent: "danger" as const,
    icon: "remove" as const,
    allowed: true,
    onFailure: failure,
    prepare: () => {
      const { target, etag } = input;
      const body = { ...input.body };
      return {
        summary: [
          { label: "Target", value: target },
          { label: "Reason", value: body.reason },
          { label: "ETag", value: etag },
        ],
        run: (key: string) => send(key, target, body, etag),
      };
    },
  };
  const view = render(<ActionForm {...original} />);
  fireEvent.click(screen.getByRole("button", { name: "Delete original item" }));
  input = {
    target: "replacement-item",
    body: { reason: "Replacement reason" },
    etag: '"replacement-revision"',
  };
  const changed = {
    ...original,
    title: "Refresh replacement item",
    description: "Replacement description.",
    intent: "warning" as const,
    icon: "refresh" as const,
    prepare: () => replacement,
  };
  view.rerender(<ActionForm {...changed} />);
  const dialog = screen.getByRole("dialog");
  expect(
    within(dialog).getByRole("heading", { name: "Delete original item" }),
  ).toBeTruthy();
  expect(dialog.textContent).toContain("Original reason");
  expect(dialog.textContent).toContain('"original-revision"');
  expect(dialog.textContent).not.toContain("Replacement");
  const confirm = within(dialog).getByRole("button", {
    name: "Confirm",
  });
  expect(confirm.classList.contains("danger")).toBe(true);
  expectIcon(confirm, "remove");
  fireEvent.click(confirm);
  const busy = within(dialog).getByRole("button", {
    name: "Submitting...",
  });
  expect(busy).toHaveProperty("disabled", true);
  expect(busy.classList.contains("danger")).toBe(true);
  expectIcon(busy, "remove");
  view.rerender(<ActionForm {...changed} allowed={false} editable={false} />);
  expect(fireEvent.keyDown(document.body, { key: "Escape" })).toBe(false);
  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
  expect(screen.getByRole("dialog")).toBe(dialog);
  expect(send).toHaveBeenCalledOnce();
  const unavailable = new APIError(503, "DEPENDENCY_UNAVAILABLE");
  await act(async () => pending.reject(unavailable));
  const retry = within(dialog).getByRole("button", {
    name: "Retry same command",
  });
  expect(retry).toHaveProperty("disabled", false);
  expect(retry.classList.contains("danger")).toBe(true);
  expectIcon(retry, "remove");
  expect(
    within(dialog).getByRole("button", { name: "Back to form" }),
  ).toHaveProperty("disabled", true);
  expect(fireEvent.keyDown(document.body, { key: "Escape" })).toBe(false);
  expect(failure).toHaveBeenCalledExactlyOnceWith(unavailable);
  fireEvent.click(retry);
  await screen.findByText("Deleted original item.");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(send).toHaveBeenCalledTimes(2);
  expect(send.mock.calls[0]?.[0]).toBeTruthy();
  expect(send.mock.calls[0]?.slice(1)).toEqual([
    "original-item",
    { reason: "Original reason" },
    '"original-revision"',
  ]);
  expect(send.mock.calls[1]).toEqual(send.mock.calls[0]);
  expect(replacement).not.toHaveBeenCalled();
});

it("keeps direct confirmation presentation bound to its original command", async () => {
  const original = vi.fn(async () => "Original result");
  const replacement = vi.fn(async () => "Replacement result");
  const done = vi.fn();
  const callbacks = {
    onClose: vi.fn(),
    onDone: done,
    onFailure: vi.fn(),
  };
  const view = render(
    <CommandDialog
      {...callbacks}
      command={{
        title: "Original command",
        description: "Captured caution.",
        intent: "warning",
        icon: "refresh",
        run: original,
      }}
    />,
  );
  view.rerender(
    <CommandDialog
      {...callbacks}
      command={{
        title: "Replacement command",
        description: "Different operation.",
        intent: "danger",
        icon: "remove",
        run: replacement,
      }}
    />,
  );
  const confirm = screen.getByRole("button", { name: "Confirm" });
  expect(confirm.classList.contains("warning")).toBe(true);
  expectIcon(confirm, "refresh");
  fireEvent.click(confirm);
  await act(async () => {});
  expect(done).toHaveBeenCalledExactlyOnceWith("Original result");
  expect(original).toHaveBeenCalledOnce();
  expect(replacement).not.toHaveBeenCalled();
});
