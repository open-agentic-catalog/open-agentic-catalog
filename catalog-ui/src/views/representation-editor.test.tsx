// @vitest-environment jsdom
import { useState } from "react";
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { newRepresentation, RepresentationEditor } from "./representations";
import type { RepresentationDraft } from "./representations";
import { deferred } from "../../tests/unit/fixtures";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it("does not protect a checksum that was typed then completely cleared", async () => {
  const removed = vi.fn();
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  function Editor() {
    const [value, setValue] = useState(newRepresentation("plugin"));
    return (
      <RepresentationEditor
        kind="plugin"
        index={1}
        value={value}
        onChange={setValue}
        onRemove={removed}
      />
    );
  }
  render(<Editor />);
  await screen.findByLabelText("Expected archive checksum (optional)");
  fireEvent.change(
    screen.getByLabelText("Expected archive checksum (optional)"),
    { target: { value: "sha256:unfinished" } },
  );
  fireEvent.change(
    screen.getByLabelText("Expected archive checksum (optional)"),
    { target: { value: "" } },
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Remove representation 2" }),
  );
  expect(confirm).not.toHaveBeenCalled();
  expect(removed).toHaveBeenCalledOnce();
});

it("protects a supplied archive checksum even before a URL is entered", async () => {
  const removed = vi.fn();
  const value = {
    ...newRepresentation("plugin"),
    expectedDigest: "sha256:" + "a".repeat(64),
  };
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  render(
    <RepresentationEditor
      kind="plugin"
      index={1}
      value={value}
      onChange={vi.fn()}
      onRemove={removed}
    />,
  );
  await screen.findByLabelText("Expected archive checksum (optional)");
  fireEvent.click(
    screen.getByRole("button", { name: "Remove representation 2" }),
  );
  expect(confirm).toHaveBeenCalledOnce();
  expect(
    screen.getByLabelText("Expected archive checksum (optional)"),
  ).toHaveProperty("value", value.expectedDigest);
  expect(removed).not.toHaveBeenCalled();
  confirm.mockReturnValue(true);
  fireEvent.click(
    screen.getByRole("button", { name: "Remove representation 2" }),
  );
  expect(removed).toHaveBeenCalledOnce();
});

it.each(["content", "data", "url"] as const)(
  "preserves Skill archive input when a switch to %s is declined, and reconciles media on acceptance",
  (mode) => {
    const original: RepresentationDraft = {
      ...newRepresentation("skill"),
      mode: "package",
      mediaType: "application/zip",
      profileId: "archive-profile",
      expectedDigest: "sha256:archive",
      packageData: "archive-bytes",
      handoff: '{"git":{"commit":"exact"}}',
    };
    const changed = vi.fn();
    function Editor() {
      const [value, setValue] = useState(original);
      return (
        <RepresentationEditor
          kind="skill"
          index={0}
          value={value}
          onChange={(next) => {
            changed(next);
            setValue(next);
          }}
        />
      );
    }
    const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
    render(<Editor />);
    fireEvent.change(screen.getByLabelText("Content source"), {
      target: { value: mode },
    });
    expect(discard).toHaveBeenCalledOnce();
    expect(screen.getByLabelText("Content source")).toHaveProperty(
      "value",
      "package",
    );
    expect(screen.getByLabelText("Media type")).toHaveProperty(
      "value",
      "application/zip",
    );
    expect(changed).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Immutable handoff JSON")).toHaveProperty(
      "value",
      original.handoff,
    );
    discard.mockReturnValue(true);
    fireEvent.change(screen.getByLabelText("Content source"), {
      target: { value: mode },
    });
    expect(screen.getByLabelText("Content source")).toHaveProperty(
      "value",
      mode,
    );
    expect(screen.getByLabelText("Media type")).toHaveProperty(
      "value",
      mode === "data" ? "application/json" : "application/agent-skills+md",
    );
    expect(screen.getByLabelText("Profile ID (optional)")).toHaveProperty(
      "value",
      "",
    );
    expect(changed.mock.calls[0]?.[0]).not.toHaveProperty("expectedDigest");
    expect(screen.queryByLabelText("Immutable handoff JSON")).toBeNull();
  },
);

it("does not prompt for a clean source switch or an unchanged source", () => {
  const onChange = vi.fn();
  const discard = vi.spyOn(window, "confirm");
  render(
    <RepresentationEditor
      kind="agent"
      index={0}
      value={newRepresentation("agent")}
      onChange={onChange}
    />,
  );
  fireEvent.change(screen.getByLabelText("Content source"), {
    target: { value: "data" },
  });
  expect(onChange).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Content source"), {
    target: { value: "url" },
  });
  expect(discard).not.toHaveBeenCalled();
  expect(onChange).toHaveBeenCalledOnce();
});

it.each(["agent", "skill"] as const)(
  "loads a local document into the %s editor without changing its format or identity",
  async (kind) => {
    const draft = newRepresentation(kind);
    const onChange = vi.fn<(draft: RepresentationDraft) => void>();
    const text = kind === "agent" ? '{"name":"Example"}' : "# Example\n";
    function Editor() {
      const [value, setValue] = useState(draft);
      return (
        <RepresentationEditor
          kind={kind}
          index={0}
          value={value}
          onChange={(next) => {
            onChange(next);
            setValue(next);
          }}
        />
      );
    }
    render(<Editor />);
    fireEvent.change(screen.getByLabelText("Document file"), {
      target: {
        files: [
          {
            name: kind === "agent" ? "card.json" : "SKILL.md",
            size: text.length,
            arrayBuffer: async () => new TextEncoder().encode(text).buffer,
          },
        ],
      },
    });
    await screen.findByRole("button", { name: "Use loaded document" });
    expect(
      onChange.mock.calls.every(
        ([value]) => value.documentStaged && value.content === "",
      ),
    ).toBe(true);
    fireEvent.click(
      screen.getByRole("button", { name: "Use loaded document" }),
    );
    await waitFor(() => {
      expect(screen.queryByText("Reading document...")).toBeNull();
      expect(onChange).toHaveBeenLastCalledWith({
        ...draft,
        content: text,
      });
      expect(
        screen.getByLabelText(
          kind === "agent" ? "Standards document (JSON)" : "Text content",
        ),
      ).toHaveProperty("value", text);
    });
  },
);

it("edits representation metadata and clears content only after confirming a source change", () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
  const removed = vi.fn();
  function Editor() {
    const [value, setValue] = useState(newRepresentation("skill"));
    return (
      <RepresentationEditor
        kind="skill"
        index={1}
        value={value}
        onChange={setValue}
        onRemove={removed}
      />
    );
  }
  render(<Editor />);
  expect(
    screen.getByRole("group", { name: /content 2$/ }).textContent,
  ).toContain("content 2");
  expect(
    document.getElementById(
      screen.getByLabelText("Media type").getAttribute("aria-describedby")!,
    )?.textContent,
  ).toContain("supported media type for this resource kind");
  expect(
    document.getElementById(
      screen
        .getByLabelText("Profile ID (optional)")
        .getAttribute("aria-describedby")!,
    )?.textContent,
  ).toContain("does not choose another validator");
  for (const [name, value] of [
    ["Representation role", "documentation"],
    ["Media type", "text/markdown"],
    ["Profile ID (optional)", "profile-1"],
    ["Text content", "# Evidence"],
  ] as const) {
    fireEvent.change(screen.getByLabelText(name), { target: { value } });
    expect((screen.getByLabelText(name) as HTMLInputElement).value).toBe(value);
  }
  fireEvent.change(screen.getByLabelText("Content source"), {
    target: { value: "url" },
  });
  expect(confirm).toHaveBeenCalledTimes(1);
  expect(screen.queryByLabelText("Text content")).toBeNull();
  fireEvent.change(screen.getByLabelText("Document URL"), {
    target: { value: "https://example.com/SKILL.md" },
  });
  expect(
    (screen.getByLabelText("Document URL") as HTMLInputElement).value,
  ).toBe("https://example.com/SKILL.md");
  fireEvent.change(screen.getByLabelText("Content source"), {
    target: { value: "data" },
  });
  expect(
    document.getElementById(
      screen
        .getByLabelText("Standards document (JSON)")
        .getAttribute("aria-describedby")!,
    )?.textContent,
  ).toContain("choose Anonymous HTTPS document");
  expect(
    (screen.getByLabelText("Standards document (JSON)") as HTMLTextAreaElement)
      .value,
  ).toBe("");
  fireEvent.change(screen.getByLabelText("Standards document (JSON)"), {
    target: { value: '{"name":"skill"}' },
  });
  fireEvent.change(screen.getByLabelText("Content source"), {
    target: { value: "url" },
  });
  expect(
    (screen.getByLabelText("Document URL") as HTMLInputElement).value,
  ).toBe("");
  fireEvent.change(screen.getByLabelText("Content source"), {
    target: { value: "package" },
  });
  expect(
    screen.getByText(/Choose a non-empty archive up to 16 MiB/).textContent,
  ).toContain("deployment request limit may be smaller");
  expect(screen.getByText(/A discovery snapshot also needs/)).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Edit immutable handoff as JSON" }),
  );
  fireEvent.change(screen.getByLabelText("Immutable handoff JSON"), {
    target: { value: '{"type":"git"}' },
  });
  expect(
    (screen.getByLabelText("Immutable handoff JSON") as HTMLTextAreaElement)
      .value,
  ).toBe('{"type":"git"}');
  fireEvent.click(
    screen.getByRole("button", { name: "Remove representation 2" }),
  );
  expect(removed).toHaveBeenCalledOnce();
});

it("offers archives only for Skills and cannot remove the required representation", () => {
  render(
    <RepresentationEditor
      kind="agent"
      index={0}
      value={newRepresentation("agent")}
      onChange={vi.fn()}
    />,
  );
  expect(
    screen.queryByRole("option", {
      name: "Upload a complete Skill package",
    }),
  ).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Remove representation" }),
  ).toBeNull();
  expect(screen.getByLabelText("Standards document (JSON)")).toBeTruthy();
});

const archiveDraft = (): RepresentationDraft => ({
  ...newRepresentation("skill"),
  mode: "package",
});

it("clears old archive bytes, disables input while reading and returns the exact encoded bytes", async () => {
  const pending = deferred<ArrayBuffer>();
  const onChange = vi.fn();
  const draft = { ...archiveDraft(), packageData: "AA==" };
  render(
    <RepresentationEditor
      kind="skill"
      index={0}
      value={draft}
      onChange={onChange}
    />,
  );
  fireEvent.change(screen.getByLabelText("Skill archive"), {
    target: {
      files: [
        { name: "skill.tgz", size: 3, arrayBuffer: () => pending.promise },
      ],
    },
  });
  expect(onChange).toHaveBeenCalledWith({ ...draft, packageData: "" });
  expect(
    (screen.getByLabelText("Skill archive") as HTMLInputElement).disabled,
  ).toBe(true);
  expect(screen.getByRole("status").textContent).toBe("Reading package...");
  await act(async () => pending.resolve(new Uint8Array([0, 128, 255]).buffer));
  expect(onChange).toHaveBeenLastCalledWith({
    ...draft,
    packageData: "AID/",
    mediaType: "application/agent-skills+gzip",
  });
  expect(screen.queryByRole("status")).toBeNull();
  expect(
    (screen.getByLabelText("Skill archive") as HTMLInputElement).disabled,
  ).toBe(false);
  onChange.mockClear();
  fireEvent.change(screen.getByLabelText("Skill archive"), {
    target: { files: [] },
  });
  expect(onChange).toHaveBeenCalledExactlyOnceWith({
    ...draft,
    packageData: "",
  });
});

it("shows a safe read failure and allows another file selection", async () => {
  const onChange = vi.fn();
  render(
    <RepresentationEditor
      kind="skill"
      index={0}
      value={archiveDraft()}
      onChange={onChange}
    />,
  );
  fireEvent.change(screen.getByLabelText("Skill archive"), {
    target: {
      files: [
        {
          name: "private.zip",
          size: 1,
          arrayBuffer: async () => {
            throw new Error("private filesystem information");
          },
        },
      ],
    },
  });
  expect((await screen.findByRole("alert")).textContent).toBe(
    "Could not read this package. Choose a non-empty file up to 16 MiB.",
  );
  expect(document.body.textContent).not.toContain("private filesystem");
  fireEvent.change(screen.getByLabelText("Skill archive"), {
    target: {
      files: [
        {
          name: "skill.zip",
          size: 1,
          arrayBuffer: async () => new Uint8Array([1]).buffer,
        },
      ],
    },
  });
  await waitFor(() =>
    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({
        packageData: "AQ==",
        mediaType: "application/agent-skills+zip",
      }),
    ),
  );
  expect(screen.queryByRole("alert")).toBeNull();
});

for (const outcome of ["success", "failure"]) {
  it(`ignores a late archive ${outcome} after removing its editor`, async () => {
    const pending = deferred<ArrayBuffer>();
    const onChange = vi.fn();
    const { unmount } = render(
      <RepresentationEditor
        kind="skill"
        index={0}
        value={archiveDraft()}
        onChange={onChange}
      />,
    );
    fireEvent.change(screen.getByLabelText("Skill archive"), {
      target: {
        files: [
          { name: "skill.zip", size: 1, arrayBuffer: () => pending.promise },
        ],
      },
    });
    onChange.mockClear();
    unmount();
    await act(async () => {
      if (outcome === "success") pending.resolve(new Uint8Array([1]).buffer);
      else pending.reject(new Error("read failed"));
    });
    expect(onChange).not.toHaveBeenCalled();
  });
}

it("clears the local staged guard after discard and explains both MCP delivery boundaries", async () => {
  function Editor() {
    const [value, onChange] = useState(newRepresentation("mcp_server"));
    return (
      <>
        <output>
          {value.documentStaged ? "selection unresolved" : "selection resolved"}
        </output>
        <RepresentationEditor
          kind="mcp_server"
          index={0}
          value={value}
          onChange={onChange}
        />
      </>
    );
  }
  render(<Editor />);
  expect(
    screen.getByText(/MCP Registry metadata can validate and release locally/),
  ).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: {
      files: [
        {
          name: "mcp.json",
          size: 2,
          arrayBuffer: async () => new TextEncoder().encode("{}").buffer,
        },
      ],
    },
  });
  await screen.findByRole("button", { name: "Use loaded document" });
  expect(screen.getByText("selection unresolved")).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Discard selected document" }),
  );
  expect(screen.getByText("selection resolved")).toBeTruthy();
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    "",
  );
});
