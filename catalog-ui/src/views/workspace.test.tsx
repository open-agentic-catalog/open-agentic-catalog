import { required } from "../../tests/required";
import { useLayoutEffect, type FormEvent } from "react";
// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  renderHook,
  screen,
} from "@testing-library/react";
import { APIError } from "../api/client";
import { deferred, installDialog } from "../../tests/unit/fixtures";
import {
  CommandDialog,
  Field,
  FieldValidationContext,
  Heading,
  Pager,
  ReadState,
  Reason,
  confirmDiscard,
  useRead,
  useUnsaved,
} from "./workspace";
import { reportInvalidFields } from "./authoring";

it("associates inline errors once, preserves existing help and clears its invalid state", () => {
  const view = render(
    <Field label="Native name" hint="Existing hint" error="Enter a name.">
      <input id="native-name" aria-describedby="native-name-error extra-help" />
    </Field>,
  );
  const input = screen.getByLabelText("Native name");
  expect(input.getAttribute("aria-invalid")).toBe("true");
  const descriptions = input.getAttribute("aria-describedby")!.split(" ");
  expect(descriptions.filter((id) => id === "native-name-error")).toHaveLength(
    1,
  );
  expect(descriptions).toContain("extra-help");
  expect(document.getElementById("native-name-error")?.textContent).toBe(
    "Enter a name.",
  );
  view.rerender(
    <Field label="Native name" hint="Existing hint">
      <input id="native-name" aria-describedby="extra-help" />
    </Field>,
  );
  expect(input.hasAttribute("aria-invalid")).toBe(false);
  expect(document.getElementById("native-name-error")).toBeNull();
  expect(input.getAttribute("aria-describedby")).toContain("extra-help");
});

it("marks only the command error target and gives an explicit field error priority", () => {
  const fields = (own = "") => (
    <>
      <Field label="Target" error={own}>
        <input id="target" />
      </Field>
      <Field label="Other">
        <input id="other" />
      </Field>
      <Field label="Unidentified">
        <input />
      </Field>
    </>
  );
  const failure = { fieldId: "target", message: "Use HTTPS." };
  const view = render(
    <FieldValidationContext value={failure}>{fields()}</FieldValidationContext>,
  );
  expect(screen.getByLabelText("Target").getAttribute("aria-invalid")).toBe(
    "true",
  );
  expect(screen.getByLabelText("Other").hasAttribute("aria-invalid")).toBe(
    false,
  );
  expect(
    screen.getByLabelText("Unidentified").hasAttribute("aria-invalid"),
  ).toBe(false);
  expect(document.getElementById("target-error")?.textContent).toBe(
    "Use HTTPS.",
  );
  view.rerender(
    <FieldValidationContext value={failure}>
      {fields("Enter a target.")}
    </FieldValidationContext>,
  );
  expect(document.getElementById("target-error")?.textContent).toBe(
    "Enter a target.",
  );
  view.rerender(
    <FieldValidationContext value={undefined}>
      {fields()}
    </FieldValidationContext>,
  );
  expect(screen.getByLabelText("Target").hasAttribute("aria-invalid")).toBe(
    false,
  );
});

it("validates a step, reveals missing fields and ignores disabled controls without submitting", () => {
  const submitted = vi.fn((event: FormEvent<HTMLFormElement>) =>
    event.preventDefault(),
  );
  render(
    <form onSubmit={submitted}>
      <fieldset aria-label="Step">
        <input aria-label="First" required />
        <details>
          <summary>Extra fields</summary>
          <input aria-label="Second" required />
        </details>
        <input aria-label="Disabled" required disabled />
      </fieldset>
    </form>,
  );
  const step = screen.getByRole("group", { name: "Step" });
  const first = screen.getByLabelText("First") as HTMLInputElement;
  const second = screen.getByLabelText("Second") as HTMLInputElement;
  expect(reportInvalidFields(step)).toBe(false);
  expect(document.activeElement).toBe(first);
  expect(second.closest("details")?.open).toBe(true);
  first.value = "One";
  second.value = "Two";
  expect(reportInvalidFields(step)).toBe(true);
  expect(submitted).not.toHaveBeenCalled();
});

it("keeps navigation and unload protection aligned with each committed dirty state", () => {
  const observed: boolean[][] = [];
  function Editor({ dirty }: { dirty: boolean }) {
    useUnsaved(dirty);
    useLayoutEffect(() => {
      observed.push([
        window.dispatchEvent(
          new Event("catalog-ui:leave", { cancelable: true }),
        ),
        window.dispatchEvent(new Event("beforeunload", { cancelable: true })),
      ]);
    });
    return null;
  }
  const view = render(<Editor dirty={false} />);
  view.rerender(<Editor dirty />);
  view.rerender(<Editor dirty={false} />);
  expect(observed).toEqual([
    [true, true],
    [false, false],
    [true, true],
  ]);
});

it("removes unmounted editor protection before another committed control can navigate", () => {
  const observed = vi.fn();
  function Editor() {
    useUnsaved(true);
    return null;
  }
  function NextControl() {
    useLayoutEffect(() => {
      observed([
        window.dispatchEvent(
          new Event("catalog-ui:leave", { cancelable: true }),
        ),
        window.dispatchEvent(new Event("beforeunload", { cancelable: true })),
      ]);
    });
    return null;
  }
  const view = render(
    <>
      <Editor key="editor" />
      <NextControl key="next" />
    </>,
  );
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(false);
  observed.mockClear();
  view.rerender(
    <>
      <NextControl key="next" />
    </>,
  );
  expect(observed).toHaveBeenCalledExactlyOnceWith([true, true]);
});

let restoreDialog: () => void;
beforeEach(() => {
  restoreDialog = installDialog();
});
afterEach(() => {
  cleanup();
  restoreDialog();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

it("keeps confirmation facts and execution bound to one preparation after a parent rerender", async () => {
  const original = vi.fn(async () => "Original saved");
  const replacement = vi.fn(async () => "Replacement saved");
  const done = vi.fn();
  const callbacks = { onClose: vi.fn(), onDone: done, onFailure: vi.fn() };
  const view = render(
    <CommandDialog
      command={{
        title: "Original action",
        description: "Original description",
        summary: [
          { label: "Target", value: "<script>literal original</script>" },
        ],
        run: original,
      }}
      {...callbacks}
    />,
  );
  view.rerender(
    <CommandDialog
      command={{
        title: "Replacement action",
        description: "Replacement description",
        summary: [{ label: "Target", value: "Replacement target" }],
        run: replacement,
      }}
      {...callbacks}
    />,
  );
  expect(screen.getByLabelText("Command summary").textContent).toContain(
    "<script>literal original</script>",
  );
  expect(screen.getByRole("heading", { name: "Original action" })).toBeTruthy();
  expect(screen.queryByText("Replacement description")).toBeNull();
  expect(view.container.querySelector("script")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await act(async () => {});
  expect(original).toHaveBeenCalledOnce();
  expect(replacement).not.toHaveBeenCalled();
  expect(done).toHaveBeenCalledWith("Original saved");
});

describe("bounded asynchronous reads", () => {
  it("retains a same-key page during reread and failure, but clears it when scope changes", async () => {
    const pending = deferred<string>();
    const failure = vi.fn();
    const load = vi
      .fn()
      .mockResolvedValueOnce("loaded")
      .mockImplementationOnce(() => pending.promise)
      .mockResolvedValueOnce("other scope");
    const hook = renderHook(({ key }) => useRead(key, load, failure, true), {
      initialProps: { key: "scope-a" },
    });
    await act(async () => {});
    expect(hook.result.current.data).toBe("loaded");
    act(() => hook.result.current.refresh());
    expect(hook.result.current.busy).toBe(true);
    expect(hook.result.current.data).toBe("loaded");
    await act(async () => pending.reject(new Error("transport unavailable")));
    expect(hook.result.current.data).toBe("loaded");
    expect(hook.result.current.error).toBe("This page could not be loaded.");
    expect(failure).toHaveBeenCalledOnce();
    hook.rerender({ key: "scope-b" });
    expect(hook.result.current.data).toBeUndefined();
    await act(async () => {});
    expect(hook.result.current.data).toBe("other scope");
    expect(hook.result.current.error).toBe("");
  });
  it("refreshes with current callbacks and discards obsolete success", async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    const oldLoad = vi.fn((_signal: AbortSignal) => first.promise);
    const newLoad = vi.fn((_signal: AbortSignal) => second.promise);
    const failure = vi.fn();
    const hook = renderHook(({ key, load }) => useRead(key, load, failure), {
      initialProps: { key: "first", load: oldLoad },
    });
    expect(hook.result.current.busy).toBe(true);
    hook.rerender({ key: "second", load: newLoad });
    expect(required(oldLoad.mock.calls[0])[0].aborted).toBe(true);
    await act(async () => first.resolve("obsolete"));
    expect(hook.result.current.data).toBeUndefined();
    expect(hook.result.current.busy).toBe(true);
    await act(async () => second.resolve("current"));
    expect(hook.result.current.data).toBe("current");
    expect(hook.result.current.busy).toBe(false);
    await act(async () => hook.result.current.refresh());
    expect(newLoad).toHaveBeenCalledTimes(2);
    expect(failure).not.toHaveBeenCalled();
    hook.unmount();
    expect(required(newLoad.mock.calls[1])[0].aborted).toBe(true);
  });

  it.each([
    new APIError(403, "denied"),
    new Error("private transport detail"),
    new APIError(429, "RATE_LIMITED", "", Date.UTC(2026, 8, 26, 12, 0, 30)),
  ])(
    "reports a current failure safely and permits recovery: %s",
    async (error) => {
      vi.useFakeTimers();
      vi.setSystemTime(Date.UTC(2026, 8, 26, 12));
      const failure = vi.fn();
      const load = vi
        .fn()
        .mockRejectedValueOnce(error)
        .mockResolvedValueOnce("recovered");
      const hook = renderHook(() => useRead("key", load, failure));
      await act(async () => {});
      expect(hook.result.current.error).toBe(
        error instanceof APIError
          ? error.message
          : "This page could not be loaded.",
      );
      expect(hook.result.current.busy).toBe(false);
      expect(failure).toHaveBeenCalledWith(error);
      const message = hook.result.current.error;
      hook.rerender();
      await act(async () => vi.advanceTimersByTimeAsync(120000));
      expect(hook.result.current.error).toBe(message);
      expect(load).toHaveBeenCalledTimes(1);
      await act(async () => hook.result.current.refresh());
      expect(hook.result.current).toMatchObject({
        data: "recovered",
        error: "",
        busy: false,
      });
    },
  );

  it("ignores a late failure after unmount", async () => {
    const pending = deferred<string>();
    const failure = vi.fn();
    const hook = renderHook(() =>
      useRead("key", () => pending.promise, failure),
    );
    hook.unmount();
    await act(async () => pending.reject(new Error("late")));
    expect(failure).not.toHaveBeenCalled();
  });
});

describe("deliberate commands", () => {
  function mount(run: (key: string) => Promise<string>) {
    const callbacks = { onClose: vi.fn(), onDone: vi.fn(), onFailure: vi.fn() };
    const view = render(
      <CommandDialog
        command={{ title: "Save", description: "Save exact revision", run }}
        {...callbacks}
      />,
    );
    return { ...view, ...callbacks };
  }
  it("does not submit before confirmation and restores the trigger on dismissal", () => {
    render(<button>Open editor</button>);
    const button = screen.getByRole("button", { name: "Open editor" });
    button.focus();
    const run = vi.fn();
    const view = mount(run);
    fireEvent(
      screen.getByRole("dialog"),
      new Event("cancel", { cancelable: true }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Enter" });
    expect(view.onClose).toHaveBeenCalledTimes(2);
    expect(
      fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" }),
    ).toBe(false);
    expect(view.onClose).toHaveBeenCalledTimes(3);
    expect(fireEvent.keyDown(document.body, { key: "Escape" })).toBe(false);
    expect(view.onClose).toHaveBeenCalledTimes(4);
    expect(run).not.toHaveBeenCalled();
    view.unmount();
    expect(document.activeElement).toBe(button);
    expect(fireEvent.keyDown(document.body, { key: "Escape" })).toBe(true);
    expect(view.onClose).toHaveBeenCalledTimes(4);
  });
  it("captures the opener before commit-time focus changes", () => {
    render(<button>Open editor</button>);
    const opener = screen.getByRole("button", { name: "Open editor" });
    opener.focus();
    const view = render(
      <>
        <button
          ref={(element) => {
            element?.focus();
          }}
        >
          Commit focus target
        </button>
        <CommandDialog
          command={{
            title: "Save",
            description: "Save exact revision",
            run: vi.fn(),
          }}
          onClose={vi.fn()}
          onDone={vi.fn()}
          onFailure={vi.fn()}
        />
      </>,
    );
    view.unmount();
    expect(document.activeElement).toBe(opener);
  });
  it("leaves current focus alone when the command opener has been removed", () => {
    const origin = render(<button>Open editor</button>);
    const opener = screen.getByRole("button", { name: "Open editor" });
    opener.focus();
    const run = vi.fn();
    const view = mount(run);
    const restore = vi.spyOn(opener, "focus");
    origin.unmount();
    render(<button>Current page</button>);
    const current = screen.getByRole("button", { name: "Current page" });
    current.focus();
    view.unmount();
    expect(restore).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(current);
    expect(run).not.toHaveBeenCalled();
    expect(view.onDone).not.toHaveBeenCalled();
    expect(view.onFailure).not.toHaveBeenCalled();
  });
  it("guards an in-flight command and reports successful completion once", async () => {
    const pending = deferred<string>();
    const run = vi.fn(() => pending.promise);
    const view = mount(run);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    fireEvent.click(screen.getByRole("button", { name: "Submitting..." }));
    fireEvent(
      screen.getByRole("dialog"),
      new Event("cancel", { cancelable: true }),
    );
    for (let attempt = 0; attempt < 2; attempt++)
      expect(
        fireEvent.keyDown(
          attempt ? document.body : screen.getByRole("dialog"),
          {
            key: "Escape",
          },
        ),
      ).toBe(false);
    expect(run).toHaveBeenCalledTimes(1);
    expect(view.onClose).not.toHaveBeenCalled();
    expect(
      (screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    await act(async () => pending.resolve("saved"));
    expect(view.onDone).toHaveBeenCalledExactlyOnceWith("saved");
  });
  it.each([
    new TypeError("private upstream"),
    new APIError(503, "UNAVAILABLE", "", Date.UTC(2026, 8, 26, 12, 0, 30)),
  ])(
    "keeps unknown outcomes on the same command and key: %s",
    async (error) => {
      vi.useFakeTimers();
      vi.setSystemTime(Date.UTC(2026, 8, 26, 12));
      const run = vi
        .fn<(key: string) => Promise<string>>()
        .mockRejectedValueOnce(error)
        .mockResolvedValueOnce("replayed");
      const view = mount(run);
      await act(async () =>
        fireEvent.click(screen.getByRole("button", { name: "Confirm" })),
      );
      expect(screen.getByRole("alert").textContent).toContain(
        "outcome is unknown",
      );
      expect(screen.getByRole("alert").textContent).not.toContain(
        "private upstream",
      );
      fireEvent(
        screen.getByRole("dialog"),
        new Event("cancel", { cancelable: true }),
      );
      for (let attempt = 0; attempt < 2; attempt++)
        expect(
          fireEvent.keyDown(
            attempt ? document.body : screen.getByRole("dialog"),
            {
              key: "Escape",
            },
          ),
        ).toBe(false);
      expect(view.onClose).not.toHaveBeenCalled();
      if (error instanceof APIError)
        expect(screen.getByRole("alert").textContent).toContain(error.message);
      const message = screen.getByRole("alert").textContent;
      await act(async () => vi.advanceTimersByTimeAsync(120000));
      expect(screen.getByRole("alert").textContent).toBe(message);
      expect(run).toHaveBeenCalledTimes(1);
      expect(
        (
          screen.getByRole("button", {
            name: "Retry same command",
          }) as HTMLButtonElement
        ).disabled,
      ).toBe(false);
      await act(async () =>
        fireEvent.click(
          screen.getByRole("button", { name: "Retry same command" }),
        ),
      );
      expect(required(run.mock.calls[0])[0]).toBe(
        required(run.mock.calls[1])[0],
      );
      expect(view.onDone).toHaveBeenCalledWith("replayed");
    },
  );
  it.each([
    new APIError(412, "PRECONDITION_FAILED"),
    new APIError(422, "INVALID_ARGUMENT", "request-a"),
    new APIError(422, ""),
  ])(
    "returns a definite rejection to the form without offering a retry: %s",
    async (error) => {
      const view = mount(vi.fn().mockRejectedValue(error));
      await act(async () =>
        fireEvent.click(screen.getByRole("button", { name: "Confirm" })),
      );
      const text = screen.getByRole("alert").textContent!;
      expect(text).toContain(error.message);
      if (error.requestId) expect(text).toContain("Request: request-a");
      expect(text.includes("The record changed. Your input is retained.")).toBe(
        error.status === 412,
      );
      expect(text).not.toContain("The outcome is unknown.");
      expect(
        screen.queryByRole("button", { name: "Retry same command" }),
      ).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: "Back to form" }));
      expect(view.onClose).toHaveBeenCalledOnce();
      expect(view.onFailure).toHaveBeenCalledWith(error);
    },
  );
  it.each(["resolve", "reject"] as const)(
    "ignores late %s after unmount",
    async (outcome) => {
      const pending = deferred<string>();
      const view = mount(() => pending.promise);
      fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
      view.unmount();
      await act(async () => {
        if (outcome === "resolve") pending.resolve("late");
        else pending.reject(new Error("late"));
      });
      expect(view.onDone).not.toHaveBeenCalled();
      expect(view.onFailure).not.toHaveBeenCalled();
    },
  );
});

it("protects dirty navigation, allows clean navigation and removes listeners", () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  const hook = renderHook(({ dirty }) => useUnsaved(dirty), {
    initialProps: { dirty: false },
  });
  expect(confirmDiscard()).toBe(true);
  expect(confirm).not.toHaveBeenCalled();
  hook.rerender({ dirty: true });
  expect(
    window.dispatchEvent(new Event("beforeunload", { cancelable: true })),
  ).toBe(false);
  expect(confirmDiscard()).toBe(false);
  confirm.mockReturnValue(true);
  expect(confirmDiscard()).toBe(true);
  hook.unmount();
  expect(
    window.dispatchEvent(new Event("beforeunload", { cancelable: true })),
  ).toBe(true);
});

it("keeps each dirty editor protected when another editor becomes clean or unmounts", () => {
  function Editor({ dirty }: { dirty: boolean }) {
    useUnsaved(dirty);
    return null;
  }
  const protectedEvents = () => [
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    window.dispatchEvent(new Event("beforeunload", { cancelable: true })),
  ];
  const view = render(
    <>
      <Editor key="first" dirty />
      <Editor key="second" dirty />
    </>,
  );
  expect(protectedEvents()).toEqual([false, false]);
  view.rerender(
    <>
      <Editor key="first" dirty={false} />
      <Editor key="second" dirty />
    </>,
  );
  expect(protectedEvents()).toEqual([false, false]);
  view.rerender(
    <>
      <Editor key="first" dirty />
      <Editor key="second" dirty />
    </>,
  );
  view.rerender(
    <>
      <Editor key="second" dirty />
    </>,
  );
  expect(protectedEvents()).toEqual([false, false]);
  view.rerender(
    <>
      <Editor key="second" dirty={false} />
    </>,
  );
  expect(protectedEvents()).toEqual([true, true]);
  view.unmount();
  expect(protectedEvents()).toEqual([true, true]);
});

it("wires shared loading/error, paging, field help and reason controls without hidden submits", () => {
  const refresh = vi.fn(),
    page = vi.fn(),
    reason = vi.fn();
  render(
    <>
      <Heading title="Panel" description="Description" icon="book">
        <span>Actions</span>
      </Heading>
      <ReadState busy error="Unavailable" refresh={refresh} />
      <Pager cursor="second" nextCursor="third" onPage={page} />
      <Field label="Subject" hint="Visible hint" help="More information">
        <input aria-describedby="existing" />
      </Field>
      <Reason value="" onChange={reason} />
    </>,
  );
  expect(
    screen.queryByRole("heading", { name: "Panel", level: 1 }),
  ).not.toBeNull();
  expect(screen.queryByText("Description")).not.toBeNull();
  expect(screen.queryByText("Actions")).not.toBeNull();
  expect(screen.getByRole("status").textContent).toBe("Loading...");
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  expect(refresh).toHaveBeenCalledOnce();
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  fireEvent.click(screen.getByRole("button", { name: "First page" }));
  expect(page.mock.calls).toEqual([["third"], [""]]);
  const input = screen.getByRole("textbox", { name: "Subject" });
  const help = screen.getByRole("button", { name: "About subject" });
  const before = input.getAttribute("aria-describedby")!;
  expect(before).toContain("existing");
  fireEvent.click(help);
  expect(help.getAttribute("aria-expanded")).toBe("true");
  expect(input.getAttribute("aria-describedby")).not.toBe(before);
  fireEvent.click(help);
  expect(input.getAttribute("aria-describedby")).toBe(before);
  fireEvent.change(screen.getByRole("textbox", { name: "Reason" }), {
    target: { value: "purpose" },
  });
  expect(reason).toHaveBeenCalledWith("purpose");
});

it("discloses only validated read diagnostics and supports code-only, ID-only and empty errors", () => {
  const view = render(
    <ReadState
      busy={false}
      error="Safe failure"
      refresh={vi.fn()}
      problem={new APIError(503, "DEPENDENCY_UNAVAILABLE", "read.id")}
    />,
  );
  fireEvent.click(screen.getByText("Request details"));
  expect(screen.getByText("DEPENDENCY_UNAVAILABLE")).toBeTruthy();
  expect(screen.getByText("read.id")).toBeTruthy();
  view.rerender(
    <ReadState
      busy={false}
      error="Safe failure"
      refresh={vi.fn()}
      problem={new APIError(503, "DENIED")}
    />,
  );
  expect(screen.queryByText("Request ID")).toBeNull();
  view.rerender(
    <ReadState
      busy={false}
      error="Safe failure"
      refresh={vi.fn()}
      problem={new APIError(503, "", "read.id")}
    />,
  );
  expect(screen.queryByText("Service code")).toBeNull();
  view.rerender(
    <ReadState
      busy={false}
      error="Safe failure"
      refresh={vi.fn()}
      problem={new APIError(503, "secret=private", "secret=private")}
    />,
  );
  expect(screen.queryByText("Request details")).toBeNull();
  expect(view.container.textContent).not.toContain("secret=private");
});
it("allows 2000 supplementary reason characters and rejects 2001 code points", () => {
  const view = render(<Reason value={"🙂".repeat(2000)} onChange={vi.fn()} />);
  const control = screen.getByRole("textbox", {
    name: "Reason",
  }) as HTMLTextAreaElement;
  expect(control.validity.valid).toBe(true);
  expect(control.maxLength).toBe(4000);
  view.rerender(<Reason value={"🙂".repeat(2000) + "a"} onChange={vi.fn()} />);
  expect(control.validity.customError).toBe(true);
  view.rerender(<Reason value="Reviewed" onChange={vi.fn()} />);
  expect(control.validity.valid).toBe(true);
});

it("keeps an optional named reason optional while enforcing its Unicode budget", () => {
  const view = render(
    <Reason
      label="Reason (optional)"
      required={false}
      value=""
      onChange={vi.fn()}
    />,
  );
  const control = screen.getByLabelText(
    "Reason (optional)",
  ) as HTMLTextAreaElement;
  expect(control.required).toBe(false);
  expect(control.validity.valid).toBe(true);
  view.rerender(
    <Reason
      label="Reason (optional)"
      required={false}
      value={"🙂".repeat(2000)}
      onChange={vi.fn()}
    />,
  );
  expect(control.validity.valid).toBe(true);
  view.rerender(
    <Reason
      label="Reason (optional)"
      required={false}
      value={"🙂".repeat(2000) + "a"}
      onChange={vi.fn()}
    />,
  );
  expect(control.validity.customError).toBe(true);
});
