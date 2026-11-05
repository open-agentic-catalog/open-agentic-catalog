// @vitest-environment jsdom
import { afterEach, beforeEach, it, expect, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { DraftFileActions } from "./DraftFileActions";
import { readDraftFile, writeDraftFile, type DraftFile } from "./draft-file";
import { editorDraft } from "./draft-editor";
import { newRepresentation } from "./representations";
import {
  screenLifecycle,
  click as press,
  change,
} from "../../tests/unit/screens";
import { deferred } from "../../tests/unit/fixtures";

screenLifecycle("/unit/resources/new");
const click = (name: string) => {
  if (
    ["Import draft JSON", "Download draft JSON"].includes(name) &&
    !screen.queryByRole("button", { name })
  )
    fireEvent.click(screen.getByText("Import or download draft JSON"));
  press(name);
};
const create = vi.fn((_value: Blob) => "blob:draft");
const revoke = vi.fn();
beforeEach(() => {
  create.mockClear();
  revoke.mockClear();
  vi.stubGlobal(
    "URL",
    class extends URL {
      static createObjectURL = create;
      static revokeObjectURL = revoke;
    },
  );
});
afterEach(() => vi.unstubAllGlobals());
const draft = (): DraftFile =>
  editorDraft("agent", "urn:example:agent", "1", [newRepresentation("agent")]);
const file = (value: DraftFile = draft()) => ({
  size: 100,
  arrayBuffer: async () =>
    new TextEncoder().encode(writeDraftFile(value)).buffer,
});
async function choose(value: unknown = file()) {
  await act(async () => {
    fireEvent.change(screen.getByLabelText("Choose draft JSON file"), {
      target: { files: value ? [value] : [] },
    });
  });
}
it("links the transfer tutorial without sending a command or rewriting an external manual", () => {
  const props = {
    locked: false,
    dirty: false,
    read: vi.fn(draft),
    apply: vi.fn(),
  };
  const view = render(
    <DraftFileActions {...props} documentationUrl="/docs/" />,
  );
  fireEvent.click(screen.getByText("Import or download draft JSON"));
  fireEvent.click(screen.getByText("About draft files"));
  expect(
    screen
      .getByRole("link", {
        name: "Move drafts between Catalogs (opens in a new tab)",
      })
      .getAttribute("href"),
  ).toBe("/docs/guides/draft-transfer/");
  expect(screen.getByRole("link").getAttribute("target")).toBe("_blank");
  expect(screen.getByRole("link").getAttribute("rel")).toBe(
    "noopener noreferrer",
  );
  view.rerender(
    <DraftFileActions
      {...props}
      documentationUrl="https://manual.example.test/operator/"
    />,
  );
  expect(
    screen
      .getByRole("link", { name: "Documentation (opens in a new tab)" })
      .getAttribute("href"),
  ).toBe("https://manual.example.test/operator/");
  view.rerender(<DraftFileActions {...props} documentationUrl="" />);
  expect(screen.queryByRole("link")).toBeNull();
  expect(props.read).not.toHaveBeenCalled();
  expect(props.apply).not.toHaveBeenCalled();
});
it("previews, cancels, confirms replacement and downloads exact raw text", async () => {
  const value = draft();
  value.listing = {
    displayName: "",
    description: "",
    visibility: "private",
    extensions: '{"n":9007199254740993',
  };
  const read = vi.fn(() => value);
  const apply = vi.fn();
  const view = render(
    <DraftFileActions locked={false} dirty read={read} apply={apply} />,
  );
  click("Import draft JSON");
  await choose();
  expect(document.activeElement).toBe(
    screen.getByRole("region", { name: "Draft import preview" }),
  );
  click("Cancel import");
  expect(apply).not.toHaveBeenCalled();
  await choose(file(value));
  expect(
    (screen.getByLabelText("Listing metadata to use") as HTMLSelectElement)
      .value,
  ).toBe("imported");
  vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValue(true);
  click("Use this draft");
  expect(apply).not.toHaveBeenCalled();
  change("Listing metadata to use", "none");
  click("Use this draft");
  expect(apply).toHaveBeenCalledWith(value, "none");
  click("Download draft JSON");
  expect(
    screen
      .getByRole("link", { name: "Save catalog-draft.json" })
      .getAttribute("href"),
  ).toBe("blob:draft");
  expect(create.mock.calls).toHaveLength(1);
  const blob = create.mock.calls[0]![0] as unknown as Blob;
  // Native bytes remain text inside the reusable file, including unfinished JSON.
  const source = await new Promise<string>((resolve) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.readAsText(blob);
  });
  expect(readDraftFile(source)).toEqual(value);
  click("Download draft JSON");
  expect(revoke).toHaveBeenCalledWith("blob:draft");
  view.unmount();
  expect(revoke).toHaveBeenCalledTimes(2);
});
it("preserves input after cancellation, invalid or oversized files and download/apply errors", async () => {
  const apply = vi.fn(() => {
    throw new Error("Apply refused");
  });
  const view = render(
    <DraftFileActions
      locked={false}
      dirty={false}
      read={() => {
        throw new Error("Apply helper fields");
      }}
      apply={apply}
    />,
  );
  await choose(null);
  expect(apply).not.toHaveBeenCalled();
  for (const value of [
    { size: 64 * 1024 * 1024 + 1 },
    { size: 2, arrayBuffer: async () => new TextEncoder().encode("{}").buffer },
    {
      size: 1,
      arrayBuffer: async () => {
        throw new Error("Read refused");
      },
    },
  ]) {
    await choose(value);
    expect(document.activeElement).toBe(screen.getByRole("alert"));
    expect(apply).not.toHaveBeenCalled();
  }
  click("Download draft JSON");
  expect(screen.getByRole("alert").textContent).toContain("Apply helper");
  await choose();
  click("Use this draft");
  expect(screen.getByRole("alert").textContent).toContain("Apply refused");
  view.unmount();
});
it("fences newer selections, access loss and unmounted reads", async () => {
  const first = deferred<ArrayBuffer>();
  const second = deferred<ArrayBuffer>();
  const apply = vi.fn<(file: DraftFile, choice: string) => void>();
  const props = { locked: false, dirty: false, read: draft, apply };
  const view = render(<DraftFileActions {...props} />);
  await choose({ size: 1, arrayBuffer: () => first.promise });
  await choose(file({ ...draft(), version: "newest" }));
  await act(async () =>
    first.resolve(
      new TextEncoder().encode(
        writeDraftFile({ ...draft(), version: "obsolete" }),
      ).buffer,
    ),
  );
  click("Use this draft");
  expect(apply.mock.calls[0]![0].version).toBe("newest");
  click("Download draft JSON");
  await choose({ size: 1, arrayBuffer: () => second.promise });
  view.rerender(<DraftFileActions {...props} locked />);
  await act(async () => second.reject(new Error("obsolete error")));
  expect(screen.queryByRole("alert")).toBeNull();
  expect(
    (
      screen.getByRole("button", {
        name: "Import draft JSON",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  const last = deferred<ArrayBuffer>();
  view.rerender(<DraftFileActions {...props} />);
  await choose({ size: 1, arrayBuffer: () => last.promise });
  view.unmount();
  await act(async () =>
    last.resolve(new TextEncoder().encode(writeDraftFile(draft())).buffer),
  );
  expect(apply).toHaveBeenCalledTimes(1);
});
it("keeps a fixed target, offers existing metadata and explains separate packages", async () => {
  const apply = vi.fn();
  const value = draft();
  const view = render(
    <DraftFileActions
      locked={false}
      dirty={false}
      read={draft}
      apply={apply}
      target={{ kind: "agent", canonicalIdentifier: "other" }}
    />,
  );
  await choose();
  expect(screen.getByRole("alert").textContent).toContain("different Resource");
  view.rerender(
    <DraftFileActions
      locked={false}
      dirty={false}
      read={draft}
      apply={apply}
      target={value}
    />,
  );
  await choose();
  change("Listing metadata to use", "existing");
  click("Use this draft");
  expect(apply).toHaveBeenCalledWith(value, "existing");
  value.representations[0]!.input = { mode: "skill-package", handoff: "" };
  await choose(file(value));
  expect(screen.getByText(/Supply the Skill package separately/)).toBeTruthy();
  value.representations[0]!.input = {
    mode: "plugin-preparation",
    manifest: "",
    mcp: "",
    skills: [],
  };
  await choose(file(value));
  expect(screen.getByText(/Plugin preparation stays editable/)).toBeTruthy();
  await choose(
    file({
      ...draft(),
      representations: [
        {
          ...draft().representations[0]!,
          input: { mode: "data", text: "x".repeat(2 * 1024 * 1024 + 1) },
        },
      ],
    }),
  );
  expect(screen.getByRole("alert").textContent).toMatch(/browser|editor/i);
  await waitFor(() => expect(apply).toHaveBeenCalledTimes(1));
});

it("keeps local rescue downloads available after import is locked, and revokes them during commands", async () => {
  const read = vi.fn(draft);
  const apply = vi.fn();
  const view = render(
    <DraftFileActions
      locked
      downloadLocked={false}
      dirty={false}
      read={read}
      apply={apply}
    />,
  );
  click("Download draft JSON");
  expect(
    (
      screen.getByRole("button", {
        name: "Import draft JSON",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  expect(
    screen.getByRole("link", { name: "Save catalog-draft.json" }),
  ).toBeTruthy();
  await choose();
  expect(apply).not.toHaveBeenCalled();
  view.rerender(
    <DraftFileActions
      locked
      downloadLocked
      dirty={false}
      read={read}
      apply={apply}
    />,
  );
  expect(revoke).toHaveBeenCalledWith("blob:draft");
  expect(screen.queryByRole("link")).toBeNull();
});
