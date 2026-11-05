// @vitest-environment jsdom
// File loading tests own their bytes and promises; no service or filesystem is required.
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import {
  documentFileLimit,
  documentText,
  LocalDocument,
} from "./LocalDocument";
import { parseObject } from "./representations";
import { deferred } from "../../tests/unit/fixtures";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

/** Supply browser-shaped files with controlled exact bytes to jsdom. */
function file(text: string, name = "document.json") {
  const bytes = new TextEncoder().encode(text);
  return {
    name,
    size: bytes.length,
    arrayBuffer: async () => bytes.buffer,
  } as File;
}

/** Select a file through the real input without changing the document editor. */
function select(value?: File) {
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: { files: value ? [value] : [] },
  });
}

it("bounds file bytes and decodes UTF-8 without accepting NUL, empty text or malformed encoding", async () => {
  expect(await documentText(file("\ufeff# café\r\n"))).toBe("# café\r\n");
  for (const value of [
    file(""),
    file(" "),
    file("a\0b"),
    { ...file("x"), size: documentFileLimit + 1 },
    {
      ...file("x"),
      arrayBuffer: async () => new Uint8Array(documentFileLimit + 1).buffer,
    },
    { ...file("x"), arrayBuffer: async () => new Uint8Array([255]).buffer },
  ])
    await expect(documentText(value)).rejects.toThrow();
  const exact = file("x".repeat(documentFileLimit));
  expect((await documentText(exact)).length).toBe(documentFileLimit);
});

it("stages JSON without changing text, protects replacement and leaves applying and saving explicit", async () => {
  const onLoad = vi.fn();
  const confirm = vi
    .spyOn(window, "confirm")
    .mockReturnValueOnce(false)
    .mockReturnValue(true);
  const props = {
    content: "unsaved text",
    format: "application/json",
    json: true,
    validate: parseObject,
    onLoad,
  };
  const view = render(<LocalDocument {...props} />);
  select();
  expect(screen.queryByRole("status")).toBeNull();
  select(file('{"name":"café"}', "<script>name.json"));
  await screen.findByText("<script>name.json");
  expect(view.container.querySelector("script")).toBeNull();
  expect(onLoad).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Use loaded document" }));
  expect(onLoad).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Use loaded document" }));
  expect(onLoad).toHaveBeenCalledExactlyOnceWith('{"name":"café"}');
  expect(screen.getByText(/Review the text before saving/)).toBeTruthy();
  expect(screen.queryByRole("button")).toBeNull();
  view.rerender(<LocalDocument {...props} content='{"name":"café"}' />);
  select(file('{"name":"café"}'));
  await screen.findByRole("button", { name: "Use loaded document" });
  fireEvent.click(screen.getByRole("button", { name: "Use loaded document" }));
  expect(confirm).toHaveBeenCalledTimes(2);
});

it("rejects invalid JSON and read failures safely, then permits plain Markdown without JSON parsing", async () => {
  const onLoad = vi.fn();
  const props = {
    content: "",
    format: "application/json",
    json: true,
    validate: parseObject,
    onLoad,
  };
  const view = render(<LocalDocument {...props} />);
  select(file('{"duplicate":1,"duplicate":2}'));
  await screen.findByRole("alert");
  expect(screen.queryByRole("button")).toBeNull();
  select({
    ...file("x"),
    arrayBuffer: async () => {
      throw new Error("private local path");
    },
  });
  await screen.findByRole("alert");
  expect(screen.queryByText(/private local path/)).toBeNull();
  view.rerender(
    <LocalDocument
      {...props}
      json={false}
      validate={undefined}
      format="text/markdown"
    />,
  );
  select(file("# Literal <script>text</script>\n", "SKILL.md"));
  await screen.findByRole("button", { name: "Use loaded document" });
  expect(screen.queryByRole("alert")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Use loaded document" }));
  expect(onLoad).toHaveBeenCalledExactlyOnceWith(
    "# Literal <script>text</script>\n",
  );
});

it("protects text edited while the selected file is still loading", async () => {
  const pending = deferred<ArrayBuffer>();
  const onLoad = vi.fn();
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  const props = { content: "", format: "text/markdown", json: false, onLoad };
  const view = render(<LocalDocument {...props} />);
  select({ ...file("# From file"), arrayBuffer: () => pending.promise });
  view.rerender(<LocalDocument {...props} content="Typed during loading" />);
  await act(async () => {
    pending.resolve(new TextEncoder().encode("# From file").buffer);
  });
  fireEvent.click(screen.getByRole("button", { name: "Use loaded document" }));
  expect(confirm).toHaveBeenCalledOnce();
  expect(onLoad).not.toHaveBeenCalled();
  // Clearing the editor after previewing also uses the latest content, not a captured value.
  view.rerender(<LocalDocument {...props} />);
  fireEvent.click(screen.getByRole("button", { name: "Use loaded document" }));
  expect(confirm).toHaveBeenCalledOnce();
  expect(onLoad).toHaveBeenCalledExactlyOnceWith("# From file");
});

it.each([
  ['{"key":1,"key":2}', "Duplicate JSON object keys are not allowed."],
  [
    '{"number":9007199254740992}',
    "Whole numbers must be between -9007199254740991 and 9007199254740991.",
  ],
  [
    '{"number":0.123456789012345678901}',
    "The browser cannot preserve every number in this JSON. Use values it can represent exactly.",
  ],
  [
    '{"number":1e-9999}',
    "The browser cannot preserve every number in this JSON. Use values it can represent exactly.",
  ],
  ["[]", "Enter a JSON object."],
  ["{", "Enter valid JSON."],
])(
  "explains a known file validation failure without replacing editor input: %s",
  async (text, message) => {
    const onLoad = vi.fn();
    render(
      <LocalDocument
        content="Existing draft"
        format="application/json"
        json
        validate={parseObject}
        onLoad={onLoad}
      />,
    );
    select(file(text));
    expect((await screen.findByRole("alert")).textContent).toContain(message);
    expect(
      screen.queryByRole("button", { name: "Use loaded document" }),
    ).toBeNull();
    expect(onLoad).not.toHaveBeenCalled();
  },
);

it("reports pending and staged file input to its owning row until it is applied", async () => {
  const pending = deferred<ArrayBuffer>();
  const staged = vi.fn();
  const load = vi.fn();
  render(
    <LocalDocument
      content=""
      format="text/markdown"
      json={false}
      onLoad={load}
      onStagedChange={staged}
    />,
  );
  expect(staged).toHaveBeenLastCalledWith(false);
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(true);
  select({ ...file("# Skill"), arrayBuffer: () => pending.promise });
  expect(staged).toHaveBeenLastCalledWith(true);
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(false);
  await act(async () =>
    pending.resolve(new TextEncoder().encode("# Skill").buffer),
  );
  await screen.findByRole("button", { name: "Use loaded document" });
  expect(staged).toHaveBeenLastCalledWith(true);
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "Use loaded document" }));
  expect(load).toHaveBeenCalledExactlyOnceWith("# Skill");
  expect(staged).toHaveBeenLastCalledWith(false);
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(true);
});

it.each(["read", "validation"])(
  "clears the pending-file guard after a %s failure without changing the draft",
  async (failure) => {
    const pending = deferred<ArrayBuffer>();
    const staged = vi.fn();
    const load = vi.fn();
    render(
      <LocalDocument
        content="Existing private draft"
        format="application/json"
        json
        validate={parseObject}
        onLoad={load}
        onStagedChange={staged}
      />,
    );
    select({ ...file("{}"), arrayBuffer: () => pending.promise });
    expect(staged).toHaveBeenLastCalledWith(true);
    expect(
      window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    ).toBe(false);
    await act(async () => {
      if (failure === "read") pending.reject(new Error("private file path"));
      else pending.resolve(new TextEncoder().encode("{").buffer);
    });
    expect((await screen.findByRole("alert")).textContent).toBe(
      failure === "read"
        ? "Could not read this document. Choose another file and try again."
        : "Enter valid JSON.",
    );
    expect(staged).toHaveBeenLastCalledWith(false);
    expect(
      screen.queryByRole("button", { name: "Use loaded document" }),
    ).toBeNull();
    expect(load).not.toHaveBeenCalled();
    expect(
      window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    ).toBe(true);
  },
);

it.each(["resolve", "reject"])(
  "ignores a stale %s after another selection and after unmount",
  async (outcome) => {
    const pending = deferred<ArrayBuffer>();
    const onLoad = vi.fn();
    const view = render(
      <LocalDocument
        content=""
        format="text/markdown"
        json={false}
        onLoad={onLoad}
      />,
    );
    select({ ...file("old"), arrayBuffer: () => pending.promise });
    expect(screen.getByRole("status").textContent).toBe("Reading document...");
    select(file("new", "new.md"));
    await screen.findByText("new.md");
    await act(async () => {
      if (outcome === "resolve")
        pending.resolve(new TextEncoder().encode("old").buffer);
      else pending.reject(new Error("late failure"));
    });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByText("new.md")).toBeTruthy();
    const removed = deferred<ArrayBuffer>();
    select({ ...file("old"), arrayBuffer: () => removed.promise });
    view.unmount();
    expect(
      window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    ).toBe(true);
    await act(async () => {
      if (outcome === "resolve")
        removed.resolve(new TextEncoder().encode("old").buffer);
      else removed.reject(new Error("removed"));
    });
    expect(onLoad).not.toHaveBeenCalled();
  },
);

it("permits same-file reselection and restores a staged chooser after cancellation without applying its text", async () => {
  const onLoad = vi.fn();
  render(
    <LocalDocument
      content="unchanged editor"
      format="application/json"
      json
      validate={parseObject}
      onLoad={onLoad}
    />,
  );
  const input = screen.getByLabelText<HTMLInputElement>("Document file");
  fireEvent(input, new Event("cancel", { bubbles: true }));
  const selected = file('{"name":"Selected"}', "same.json");
  select(selected);
  await screen.findByRole("button", { name: "Use loaded document" });
  fireEvent.click(input);
  expect(input.value).toBe("");
  const files = [selected] as unknown as FileList;
  const add = vi.fn();
  Object.defineProperty(input, "files", {
    configurable: true,
    writable: true,
    value: [],
  });
  vi.stubGlobal(
    "DataTransfer",
    class {
      items = { add };
      files = files;
    },
  );
  fireEvent(input, new Event("cancel", { bubbles: true }));
  expect(input.files).toBe(files);
  expect(add).toHaveBeenCalledExactlyOnceWith(selected);
  expect(screen.getByText("same.json")).toBeTruthy();
  expect(onLoad).not.toHaveBeenCalled();
  vi.stubGlobal("DataTransfer", undefined);
  fireEvent(input, new Event("cancel", { bubbles: true }));
  expect(screen.getByText("same.json")).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Discard selected document" }),
  );
  fireEvent(input, new Event("cancel", { bubbles: true }));
  expect(onLoad).not.toHaveBeenCalled();
  vi.unstubAllGlobals();
});
