// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { atField, InputError } from "./input-validation";
import { ActionForm, ChoiceField, EvidenceDetails } from "./authoring";
import { deferred } from "../../tests/unit/fixtures";
import { installDialog } from "../../tests/unit/fixtures";
import { within, waitFor } from "@testing-library/react";
import { useState } from "react";
import { Field } from "./workspace";

it("connects a preparation error to its field and clears it when the author corrects the input", () => {
  const send = vi.fn();
  function Harness() {
    const [value, setValue] = useState("http://example.test/package");
    return (
      <ActionForm
        title="Save draft"
        description="Prepare content"
        allowed
        onFailure={vi.fn()}
        prepare={() => {
          if (!value.startsWith("https://"))
            throw new InputError("Use HTTPS.", "archive");
          return send;
        }}
      >
        <Field label="Archive">
          <input
            id="archive"
            value={value}
            onChange={(event) => setValue(event.target.value)}
          />
        </Field>
      </ActionForm>
    );
  }
  render(<Harness />);
  fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
  const input = screen.getByLabelText("Archive");
  expect(input.getAttribute("aria-invalid")).toBe("true");
  expect(document.getElementById("archive-error")?.textContent).toBe(
    "Use HTTPS.",
  );
  expect(screen.getByRole("alert")).toBe(document.activeElement);
  fireEvent.click(within(screen.getByRole("alert")).getByRole("link"));
  expect(input).toBe(document.activeElement);
  fireEvent.change(input, {
    target: { value: "https://example.test/package" },
  });
  expect(input.hasAttribute("aria-invalid")).toBe(false);
  expect(screen.queryByRole("alert")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(send).not.toHaveBeenCalled();
});

let restoreDialog: () => void;
beforeEach(() => {
  restoreDialog = installDialog();
});
afterEach(() => {
  cleanup();
  restoreDialog();
  vi.restoreAllMocks();
});

it("keeps retained structured data inert inside an explicitly opened disclosure", () => {
  const view = render(
    <EvidenceDetails
      title="Evidence"
      value={{ content: "<script>unsafe()</script>" }}
    />,
  );
  const panel = view.container.querySelector("details")!;
  expect(panel.open).toBe(false);
  panel.open = true;
  expect(screen.getByLabelText("JSON document").textContent).toContain(
    "<script>unsafe()</script>",
  );
  expect(view.container.querySelector("script")).toBeNull();
});

it("shows captured facts, cancels without sending and retains the same inputs and key after an uncertain result", async () => {
  let draft = "Original target";
  const send = vi
    .fn()
    .mockRejectedValueOnce(new Error("lost response"))
    .mockResolvedValueOnce("Saved");
  const prepare = () => {
    const captured = draft;
    return {
      summary: [{ label: "Target", value: captured }],
      run: (key: string) => send(key, captured) as Promise<string>,
    };
  };
  render(
    <ActionForm
      title="Save target"
      description="Explicit operation"
      allowed
      prepare={prepare}
      onFailure={vi.fn()}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Save target" }));
  const first = await screen.findByRole("dialog");
  expect(within(first).getByLabelText("Command summary").textContent).toContain(
    "Original target",
  );
  fireEvent.click(within(first).getByRole("button", { name: "Cancel" }));
  expect(send).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Save target" }));
  const dialog = await screen.findByRole("dialog");
  draft = "Changed elsewhere after preparation";
  fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
  await screen.findByText("The response was not received.");
  expect(
    within(dialog).getByLabelText("Command summary").textContent,
  ).toContain("Original target");
  fireEvent.click(
    within(dialog).getByRole("button", { name: "Retry same command" }),
  );
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(send).toHaveBeenCalledTimes(2);
  expect(send.mock.calls[1]).toEqual(send.mock.calls[0]);
  expect(send.mock.calls[0]?.[1]).toBe("Original target");
});

it("keeps denied forms inert even when submit is dispatched directly", () => {
  const prepare = vi.fn();
  const view = render(
    <ActionForm
      title="Create"
      description="Action"
      allowed={false}
      prepare={prepare}
      onFailure={vi.fn()}
    />,
  );
  fireEvent.submit(view.container.querySelector("form")!);
  expect(prepare).not.toHaveBeenCalled();
  expect(
    (screen.getByRole("button", { name: "Create" }) as HTMLButtonElement)
      .disabled,
  ).toBe(true);
});

it("separates editing authority from submit readiness so prerequisite selection remains usable", () => {
  const prepare = vi.fn();
  const view = render(
    <ActionForm
      title="Create scoped item"
      description="Choose a namespace first"
      allowed={false}
      editable
      prepare={prepare}
      onFailure={vi.fn()}
    >
      <select aria-label="Namespace">
        <option value="">Choose namespace</option>
        <option value="tools">Tools</option>
      </select>
    </ActionForm>,
  );
  const namespace = screen.getByLabelText("Namespace");
  expect(namespace.matches(":disabled")).toBe(false);
  fireEvent.change(namespace, { target: { value: "tools" } });
  expect(namespace).toHaveProperty("value", "tools");
  expect(
    screen.getByRole("button", { name: "Create scoped item" }),
  ).toHaveProperty("disabled", true);
  view.rerender(
    <ActionForm
      title="Create scoped item"
      description="Read only"
      allowed
      editable={false}
      dirty
      prepare={prepare}
      onFailure={vi.fn()}
    >
      <input aria-label="Mutation field" defaultValue="Saved value" />
    </ActionForm>,
  );
  expect(screen.queryByLabelText("Mutation field")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Create scoped item" }),
  ).toBeNull();
  expect(screen.getByText("Read only")).toBeTruthy();
  fireEvent.submit(view.container.querySelector("form")!);
  expect(prepare).not.toHaveBeenCalled();
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(true);
});

it("preserves exact-key recovery and gates rearming after editing authority ends", async () => {
  const response = deferred<string>();
  const run = vi
    .fn()
    .mockImplementationOnce(() => response.promise)
    .mockResolvedValue("Saved original command");
  const prepare = vi.fn(() => run);
  const reset = vi.fn();
  const props = {
    title: "Save exact item",
    description: "Captured input",
    allowed: true,
    prepare,
    another: { label: "Prepare another exact item", reset },
    onFailure: vi.fn(),
  };
  const view = render(<ActionForm {...props} />);
  fireEvent.click(screen.getByRole("button", { name: "Save exact item" }));
  fireEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Confirm" }),
  );
  view.rerender(<ActionForm {...props} allowed={false} editable={false} />);
  await act(async () => response.reject(new Error("Lost reply")));
  fireEvent.click(screen.getByRole("button", { name: "Retry same command" }));
  await screen.findByText("Saved original command");
  expect(run).toHaveBeenCalledTimes(2);
  expect(run.mock.calls[1]).toEqual(run.mock.calls[0]);
  view.rerender(<ActionForm {...props} editable={false} />);
  const rearm = screen.getByRole("button", {
    name: "Prepare another exact item",
  });
  expect(rearm).toHaveProperty("disabled", true);
  fireEvent.click(rearm);
  fireEvent.submit(view.container.querySelector("form")!);
  expect(reset).not.toHaveBeenCalled();
  expect(prepare).toHaveBeenCalledTimes(1);
  expect(run).toHaveBeenCalledTimes(2);
  view.rerender(<ActionForm {...props} />);
  expect(rearm).toHaveProperty("disabled", false);
  fireEvent.click(rearm);
  expect(reset).toHaveBeenCalledTimes(1);
  expect(
    screen.getByRole("button", { name: "Save exact item" }),
  ).toHaveProperty("disabled", false);
});

it("checks explicit rearming permission independently of the next command's readiness", async () => {
  const reset = vi.fn();
  const send = vi.fn().mockResolvedValue("Saved item");
  const props = {
    title: "Save item",
    description: "Save an independently permitted item",
    allowed: true,
    prepare: () => send,
    another: { label: "Prepare another item", reset },
    onFailure: vi.fn(),
  };
  const view = render(<ActionForm {...props} />);
  fireEvent.click(screen.getByRole("button", { name: "Save item" }));
  fireEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Confirm" }),
  );
  await screen.findByText("Saved item");
  view.rerender(
    <ActionForm {...props} another={{ ...props.another, allowed: false }} />,
  );
  const rearm = screen.getByRole("button", { name: "Prepare another item" });
  expect(rearm).toHaveProperty("disabled", true);
  fireEvent.click(rearm);
  expect(reset).not.toHaveBeenCalled();
  view.rerender(
    <ActionForm
      {...props}
      allowed={false}
      another={{ ...props.another, allowed: true }}
    />,
  );
  expect(rearm).toHaveProperty("disabled", false);
  fireEvent.click(rearm);
  expect(reset).toHaveBeenCalledOnce();
  expect(screen.getByRole("button", { name: "Save item" })).toHaveProperty(
    "disabled",
    true,
  );
  fireEvent.submit(view.container.querySelector("form")!);
  expect(send).toHaveBeenCalledOnce();
  expect(screen.queryByRole("dialog")).toBeNull();
});

it.each([new Error("Required metadata missing"), "invalid"])(
  "preserves editable input after preparation fails: %s",
  (error) => {
    const view = render(
      <ActionForm
        title="Create"
        description="Action"
        allowed
        prepare={() => {
          throw error;
        }}
        onFailure={vi.fn()}
      >
        <input aria-label="Draft" defaultValue="retained" />
      </ActionForm>,
    );
    fireEvent.submit(view.container.querySelector("form")!);
    expect(screen.getByRole("alert").textContent).toBe(
      error instanceof Error ? error.message : "Check the input.",
    );
    expect(
      (screen.getByRole("textbox", { name: "Draft" }) as HTMLInputElement)
        .value,
    ).toBe("retained");
    expect(screen.queryByRole("dialog")).toBeNull();
  },
);

it("confirms once, handles cancellation and leaves a successful form sealed", async () => {
  const run = vi.fn().mockResolvedValue("Saved revision");
  const view = render(
    <ActionForm
      title="Create"
      description="Action"
      allowed
      dirty
      prepare={() => run}
      onFailure={vi.fn()}
    >
      <details>
        <summary>More</summary>
        <input required aria-label="Draft" />
      </details>
    </ActionForm>,
  );
  fireEvent.invalid(
    screen.getByRole("textbox", { name: "Draft", hidden: true }),
  );
  expect(view.container.querySelector("details")?.open).toBe(true);
  fireEvent.change(screen.getByRole("textbox", { name: "Draft" }), {
    target: { value: "retained" },
  });
  fireEvent.submit(view.container.querySelector("form")!);
  expect(run).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(screen.queryByRole("dialog")).toBeNull();
  fireEvent.submit(view.container.querySelector("form")!);
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "Confirm" })),
  );
  expect(run).toHaveBeenCalledOnce();
  expect(screen.getByRole("status").textContent).toContain("Saved revision");
  expect(view.container.querySelector("fieldset")?.disabled).toBe(true);
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(true);
});

it("retains off-page selections while paging choices without submitting the surrounding form", async () => {
  const load = vi.fn(async (cursor: string, _signal: AbortSignal) =>
    cursor
      ? { items: [{ id: "second", label: "Second" }] }
      : { items: [{ id: "first", label: "First" }], nextCursor: "next" },
  );
  const submit = vi.fn((event: React.FormEvent) => event.preventDefault());
  const change = vi.fn();
  const view = render(
    <form onSubmit={submit}>
      <ChoiceField
        label="Publisher"
        value="chosen"
        onChange={change}
        load={load}
        onFailure={vi.fn()}
      />
    </form>,
  );
  await act(async () => {});
  expect(
    (screen.getByRole("combobox", { name: "Publisher" }) as HTMLSelectElement)
      .value,
  ).toBe("chosen");
  expect(
    screen.getByRole("option", { name: "chosen (selected)" }),
  ).toBeTruthy();
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "Next page" })),
  );
  expect(screen.getByRole("option", { name: "Second" })).toBeTruthy();
  fireEvent.change(screen.getByRole("combobox", { name: "Publisher" }), {
    target: { value: "second" },
  });
  expect(change).toHaveBeenCalledWith("second");
  view.rerender(
    <form onSubmit={submit}>
      <ChoiceField
        label="Publisher"
        value="second"
        onChange={change}
        load={load}
        onFailure={vi.fn()}
      />
    </form>,
  );
  expect(
    screen.queryByRole("option", { name: "Second (selected)" }),
  ).toBeNull();
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "First page" })),
  );
  expect(
    screen.getByRole("option", { name: "Second (selected)" }),
  ).toBeTruthy();
  expect(submit).not.toHaveBeenCalled();
  expect(load.mock.calls.map((call) => call[0])).toEqual(["", "next", ""]);
});

it.each([true, false])(
  "focuses a local validation summary and links only an existing control (exists=%s)",
  (exists) => {
    const validate = vi
      .fn()
      .mockImplementationOnce(() => {
        throw new Error("Fix this document.");
      })
      .mockReturnValue("valid");
    const view = render(
      <ActionForm
        title="Save"
        description="Validate locally"
        allowed
        onFailure={vi.fn()}
        prepare={() => {
          atField("document-input", validate);
          return vi.fn(async () => "Saved");
        }}
      >
        <details>
          <summary>Document</summary>
          <input
            id={exists ? "document-input" : "another-input"}
            aria-label="Document"
            defaultValue="retained"
          />
        </details>
      </ActionForm>,
    );
    fireEvent.submit(view.container.querySelector("form")!);
    expect(screen.getByRole("alert")).toBe(document.activeElement);
    expect(screen.queryByRole("dialog")).toBeNull();
    if (exists) {
      fireEvent.click(screen.getByRole("link", { name: "Fix this document." }));
      expect(view.container.querySelector("details")?.open).toBe(true);
      expect(screen.getByLabelText("Document")).toBe(document.activeElement);
    } else expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getByLabelText("Document")).toHaveProperty(
      "value",
      "retained",
    );
    fireEvent.submit(view.container.querySelector("form")!);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByRole("dialog")).toBeTruthy();
  },
);

it("keeps an unrecognized local validation failure safe and links it explicitly", () => {
  expect.assertions(3);
  expect(() =>
    atField("local-input", () => {
      throw "unsafe detail";
    }),
  ).toThrow("Check the input.");
  try {
    atField("local-input", () => {
      throw new Error("Check the value.");
    });
  } catch (error) {
    expect(error).toBeInstanceOf(InputError);
    expect(error).toHaveProperty("fieldId", "local-input");
  }
});

it("discards a known choice label and cursor when its query context changes", async () => {
  const load = vi.fn(async () => ({
    items: [{ id: "same-id", label: "Private old name" }],
    nextCursor: "next",
  }));
  const props = {
    label: "Namespace",
    value: "same-id",
    onChange: vi.fn(),
    load,
    onFailure: vi.fn(),
  };
  const view = render(<ChoiceField {...props} queryKey="old" />);
  await screen.findByRole("option", { name: "Private old name" });
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "Next page" })),
  );
  const fresh = vi.fn(async () => ({ items: [] }));
  view.rerender(<ChoiceField {...props} queryKey="new" load={fresh} />);
  await act(async () => {});
  expect(screen.queryByText(/Private old name/)).toBeNull();
  expect(
    screen.getByRole("option", { name: "same-id (selected)" }),
  ).toHaveProperty("selected", true);
  expect(fresh).toHaveBeenCalledWith("", expect.any(AbortSignal), "");
});

it("retains known labels and manual exact IDs through failed choice pagination and first-page recovery", async () => {
  const load = vi.fn(async (cursor: string) => {
    if (cursor) throw new Error("private upstream");
    return {
      items: [{ id: "known", label: "Known role" }],
      nextCursor: "next",
    };
  });
  const change = vi.fn();
  const fail = vi.fn();
  const props = {
    label: "Role",
    manualLabel: "Role ID",
    value: "known",
    onChange: change,
    load,
    onFailure: fail,
  };
  const view = render(<ChoiceField {...props} />);
  await screen.findByRole("option", { name: "Known role" });
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("alert");
  expect(
    screen.getByRole("option", { name: "Known role (selected)" }),
  ).toBeTruthy();
  expect(screen.getByLabelText("Role ID")).toHaveProperty("value", "known");
  fireEvent.change(screen.getByLabelText("Role ID"), {
    target: { value: "manual-exact" },
  });
  expect(change).toHaveBeenCalledWith("manual-exact");
  view.rerender(<ChoiceField {...props} value="manual-exact" />);
  expect(
    screen.getByRole("option", { name: "manual-exact (unverified ID)" }),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await screen.findByRole("option", { name: "Known role" });
  expect(screen.getByLabelText("Role ID")).toHaveProperty(
    "value",
    "manual-exact",
  );
  expect(load).toHaveBeenLastCalledWith("", expect.any(AbortSignal), "");
});

it("retries a failed first choice page without changing a manual reference", async () => {
  const load = vi
    .fn()
    .mockRejectedValueOnce(new Error("private failure"))
    .mockResolvedValue({ items: [] });
  const change = vi.fn();
  render(
    <ChoiceField
      label="Role"
      manualLabel="Role ID"
      value="exact-manual"
      onChange={change}
      load={load}
      onFailure={vi.fn()}
    />,
  );
  await screen.findByRole("alert");
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await act(async () => {});
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.getByLabelText("Role ID")).toHaveProperty(
    "value",
    "exact-manual",
  );
  expect(change).not.toHaveBeenCalled();
});

it("focuses each acknowledgement and rearms only an explicit next command with a fresh key", async () => {
  const sent = vi.fn().mockResolvedValue("Saved");
  const reset = vi.fn();
  const prepare = vi.fn(() => sent as (key: string) => Promise<string>);
  render(
    <ActionForm
      title="Grant"
      description="One exact grant"
      allowed
      dirty
      prepare={prepare}
      onFailure={vi.fn()}
      another={{ label: "Grant another", reset }}
    />,
  );
  const start = screen.getByRole("button", { name: "Grant" });
  start.focus();
  fireEvent.click(start);
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(document.activeElement).toBe(start);
  expect(sent).not.toHaveBeenCalled();
  for (let index = 0; index < 2; index++) {
    fireEvent.click(start);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(sent).toHaveBeenCalledTimes(index + 1));
    const status = await screen.findByRole("status");
    expect(status.textContent).toContain("Saved");
    expect(status.textContent).not.toContain("Follow the result link");
    await waitFor(() => expect(document.activeElement).toBe(status));
    expect(start.closest("fieldset")).toHaveProperty("disabled", true);
    fireEvent.submit(start.closest("form")!);
    expect(sent).toHaveBeenCalledTimes(index + 1);
    if (!index) {
      fireEvent.click(screen.getByRole("button", { name: "Grant another" }));
      expect(reset).toHaveBeenCalledTimes(1);
      expect(status.textContent).toContain("Saved");
      expect(start.closest("fieldset")).toHaveProperty("disabled", false);
      expect(prepare).toHaveBeenCalledTimes(2);
    }
  }
  expect(sent.mock.calls[0]?.[0]).not.toBe(sent.mock.calls[1]?.[0]);
});
