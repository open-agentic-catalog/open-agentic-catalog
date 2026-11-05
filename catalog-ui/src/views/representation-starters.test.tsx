// @vitest-environment jsdom
// Actual editor integration uses controlled drafts and browser-shaped files;
// every byte/promise is local and no Catalog or resource endpoint is contacted.
import { useState } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { deferred } from "../../tests/unit/fixtures";
import { screenLifecycle } from "../../tests/unit/screens";
import {
  newRepresentation,
  replaceRepresentation,
  representationInput,
  RepresentationEditor,
  type RepresentationDraft,
} from "./representations";
import { nativeStarters } from "./native-starters";

screenLifecycle("/unit/versions/new");
const create = vi.fn((_blob: Blob) => "blob:unused");
const revoke = vi.fn();
const fetch = vi.fn();
beforeEach(() => {
  create
    .mockReset()
    .mockReturnValueOnce("blob:original")
    .mockReturnValueOnce("blob:example");
  revoke.mockClear();
  fetch.mockClear();
  vi.stubGlobal("fetch", fetch);
  vi.stubGlobal(
    "URL",
    Object.assign(class extends URL {}, {
      createObjectURL: create,
      revokeObjectURL: revoke,
    }),
  );
});
afterEach(() => {
  expect(fetch).not.toHaveBeenCalled();
  vi.unstubAllGlobals();
});

const sample = nativeStarters("agent")[0]!;
function firstDraft(): RepresentationDraft {
  return {
    ...newRepresentation("agent"),
    editorID: "first-editor",
    role: "metadata",
    profileId: "chosen-profile",
    content: sample.content.replace(
      "Order exception Agent",
      "Existing private Agent",
    ),
  };
}
function otherDraft(): RepresentationDraft {
  return {
    ...newRepresentation("agent"),
    editorID: "second-editor",
    content: sample.content.replace(
      '"capabilities":{}',
      '"capabilities":{"extensions":[{"uri":"https://example.com/extra","params":{"count":1e2,"ratio":0.25,"maximum":9007199254740991}}]}',
    ),
  };
}

function Editors({
  initial,
  changed,
}: {
  initial: RepresentationDraft[];
  changed: (draft: RepresentationDraft) => void;
}) {
  const [drafts, setDrafts] = useState(initial);
  return (
    <>
      {drafts.map((value, index) => (
        <RepresentationEditor
          key={value.editorID}
          kind="agent"
          index={index}
          value={value}
          onChange={(next) => {
            changed(next);
            setDrafts((current) =>
              replaceRepresentation(current, value.editorID, next),
            );
          }}
        />
      ))}
      <output aria-label="Current controlled drafts">
        {JSON.stringify(drafts)}
      </output>
    </>
  );
}

function drafts(): RepresentationDraft[] {
  return JSON.parse(
    screen.getByLabelText("Current controlled drafts").textContent!,
  ) as RepresentationDraft[];
}
function file(text: string, name: string): File {
  const bytes = new TextEncoder().encode(text);
  return {
    name,
    size: bytes.length,
    arrayBuffer: async () => bytes.buffer,
  } as File;
}
function firstRow() {
  return within(screen.getByRole("group", { name: /content 1$/ }));
}
function selectFile(value: File) {
  fireEvent.change(firstRow().getByLabelText("Document file"), {
    target: { files: [value] },
  });
}
async function blobText(blob: Blob) {
  return new Promise((resolve) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.readAsText(blob);
  });
}

it("cancels then accepts a staged-file replacement without changing another row or editor identity", async () => {
  const initial = firstDraft();
  const other = otherDraft();
  const changed = vi.fn();
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  const view = render(<Editors initial={[initial, other]} changed={changed} />);
  fireEvent.click(
    firstRow().getByRole("button", { name: "Preview and download document" }),
  );
  expect(
    firstRow().getByRole("group", { name: "JSON document" }).textContent,
  ).toBe(initial.content);
  expect(
    firstRow()
      .getByRole("link", { name: "Download agent-card.json" })
      .getAttribute("href"),
  ).toBe("blob:original");
  selectFile(
    file(
      '{"name":"Selected file","version":"2.0.0","count":1e2}',
      "selected.json",
    ),
  );
  await firstRow().findByRole("button", { name: "Use loaded document" });
  const staged = drafts();
  expect(staged[0]).toEqual({ ...initial, documentStaged: true });
  expect(() => representationInput(staged[0]!)).toThrow(
    "Use or discard the selected document before saving.",
  );
  changed.mockClear();
  fireEvent.click(firstRow().getByRole("button", { name: "Use example" }));
  expect(confirm).toHaveBeenCalledExactlyOnceWith(
    "Replace this representation's current content and selected document with the example? Other representation rows stay unchanged.",
  );
  expect(drafts()).toEqual(staged);
  expect(firstRow().getByText("selected.json")).toBeTruthy();
  expect(
    firstRow().getByRole("textbox", { name: "Standards document (JSON)" }),
  ).toHaveProperty("value", initial.content);
  expect(changed).not.toHaveBeenCalled();
  expect(revoke).not.toHaveBeenCalled();

  confirm.mockReturnValue(true);
  fireEvent.click(firstRow().getByRole("button", { name: "Use example" }));
  expect(drafts()[0]).toEqual({ ...initial, content: sample.content });
  expect(drafts()[1]).toEqual(other);
  expect(drafts()[1]!.content).toContain(
    '"count":1e2,"ratio":0.25,"maximum":9007199254740991',
  );
  expect(screen.getByRole("group", { name: /content 1$/ }).id).toBe(
    initial.editorID,
  );
  expect(firstRow().queryByText("selected.json")).toBeNull();
  expect(
    firstRow().queryByRole("button", { name: "Use loaded document" }),
  ).toBeNull();
  expect(
    firstRow().getByRole("textbox", { name: "Standards document (JSON)" }),
  ).toHaveProperty("value", sample.content);
  expect(changed).toHaveBeenCalledExactlyOnceWith({
    ...initial,
    content: sample.content,
  });
  const input = representationInput(drafts()[0]!);
  expect(input).toMatchObject({
    role: "metadata",
    mediaType: "application/a2a-agent-card+json",
    profileId: "chosen-profile",
    data: { name: "Order exception Agent", version: "1.0.0" },
  });
  expect(input).not.toHaveProperty("documentStaged");

  expect(
    firstRow().getByRole("group", { name: "JSON document" }).textContent,
  ).toBe(sample.content);
  expect(
    firstRow()
      .getByRole("link", { name: "Download agent-card.json" })
      .getAttribute("href"),
  ).toBe("blob:example");
  expect(revoke).toHaveBeenCalledExactlyOnceWith("blob:original");
  expect(await blobText(create.mock.calls[1]![0])).toBe(sample.content);
  fireEvent.click(
    firstRow().getByRole("button", { name: "Hide document preview" }),
  );
  expect(
    firstRow().queryByRole("region", { name: "Current document preview" }),
  ).toBeNull();
  expect(revoke).toHaveBeenLastCalledWith("blob:example");
  view.unmount();
  expect(revoke).toHaveBeenCalledTimes(2);
});

it("invalidates a pending local file read when the real editor accepts an example", async () => {
  const pending = deferred<ArrayBuffer>();
  const initial = firstDraft();
  const other = otherDraft();
  const changed = vi.fn();
  vi.spyOn(window, "confirm").mockReturnValue(true);
  render(<Editors initial={[initial, other]} changed={changed} />);
  await firstRow().findByLabelText("Agent name");
  selectFile({
    ...file('{"name":"Late file"}', "late.json"),
    arrayBuffer: () => pending.promise,
  });
  expect(firstRow().getByRole("status").textContent).toBe(
    "Reading document...",
  );
  expect(drafts()[0]!.documentStaged).toBe(true);
  fireEvent.click(firstRow().getByRole("button", { name: "Use example" }));
  expect(drafts()[0]).toEqual({ ...initial, content: sample.content });
  expect(firstRow().queryByRole("status")).toBeNull();
  await act(async () =>
    pending.resolve(new TextEncoder().encode('{"name":"Late file"}').buffer),
  );
  expect(firstRow().queryByText("late.json")).toBeNull();
  expect(
    firstRow().queryByRole("button", { name: "Use loaded document" }),
  ).toBeNull();
  expect(drafts()).toEqual([{ ...initial, content: sample.content }, other]);
  expect(() => representationInput(drafts()[0]!)).not.toThrow();
});

it("replaces an HTTPS source only after confirmation and preserves role, profile and other row data", () => {
  const initial = {
    ...firstDraft(),
    mode: "url" as const,
    content: "",
    url: "https://example.com/original-card.json",
  };
  const other = otherDraft();
  const changed = vi.fn();
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  render(<Editors initial={[initial, other]} changed={changed} />);
  fireEvent.click(firstRow().getByRole("button", { name: "Use example" }));
  expect(drafts()).toEqual([initial, other]);
  expect(
    firstRow().getByRole("textbox", { name: "Document URL" }),
  ).toHaveProperty("value", initial.url);
  expect(changed).not.toHaveBeenCalled();
  confirm.mockReturnValue(true);
  fireEvent.click(firstRow().getByRole("button", { name: "Use example" }));
  expect(drafts()).toEqual([
    { ...initial, mode: "data", url: "", content: sample.content },
    other,
  ]);
  const input = representationInput(drafts()[0]!);
  expect(input).toMatchObject({
    role: "metadata",
    mediaType: initial.mediaType,
    profileId: initial.profileId,
    data: { version: "1.0.0" },
  });
  expect(input).not.toHaveProperty("url");
  expect(input).not.toHaveProperty("packageData");
  expect(input).not.toHaveProperty("handoff");
  expect(
    firstRow().getByRole("combobox", { name: "Content source" }),
  ).toHaveProperty("value", "data");
  expect(screen.getByRole("group", { name: /content 1$/ }).id).toBe(
    initial.editorID,
  );
});
