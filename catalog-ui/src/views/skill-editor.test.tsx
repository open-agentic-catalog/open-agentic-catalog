// @vitest-environment jsdom
// Controlled drafts and files stay local. No binary, database or network is used.
import { useState } from "react";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { change, click, screenLifecycle } from "../../tests/unit/screens";
import {
  newRepresentation,
  RepresentationEditor,
  RepresentationList,
  representationInput,
  type RepresentationDraft,
} from "./representations";
import { readSkillDocument, writeSkillDocument } from "./skill-markdown";
import { nativeStarters } from "./native-starters";
screenLifecycle("/unit/resources/new");
const sample = nativeStarters("skill")[0]!.content;
async function editor(content = "", media = "application/agent-skills+md") {
  const changed = vi.fn();
  let current: RepresentationDraft;
  function Harness() {
    const [value, setValue] = useState({
      ...newRepresentation("skill"),
      editorID: "skill",
      content,
      mediaType: media,
      role: "metadata" as const,
      profileId: "retained-profile",
    });
    current = value;
    return (
      <RepresentationEditor
        kind="skill"
        index={0}
        value={value}
        onChange={(next) => {
          changed(next);
          setValue(next as typeof value);
        }}
      />
    );
  }
  render(<Harness />);
  await screen.findByText("Advanced SKILL.md");
  if (readSkillDocument(content).document)
    await screen.findByLabelText("Skill name");
  else await screen.findByText(/Your input is unchanged/);
  return {
    changed,
    value: () => current!,
    document: () => readSkillDocument(current!.content).document!,
  };
}
function open(label: string, expanded = true) {
  const details = screen.getByText(label, { exact: true }).closest("details")!;
  act(() => {
    details.open = expanded;
    fireEvent(details, new Event("toggle"));
  });
}
it("generates the real document from ordinary fields without changing a draft on mount", async () => {
  const test = await editor();
  expect(test.changed).not.toHaveBeenCalled();
  await screen.findByLabelText("Skill name");
  change("Skill name", "review-notes");
  change("Skill description", 'Summarize a change: "before" and "after".');
  change("Skill instructions (Markdown)", "# Review\n\nRead the evidence.\n");
  expect(representationInput(test.value())).toEqual({
    role: "metadata",
    profileId: "retained-profile",
    mediaType: "application/agent-skills+md",
    content:
      '---\nname: "review-notes"\ndescription: "Summarize a change: \\"before\\" and \\"after\\"."\n---\n# Review\n\nRead the evidence.\n'.replaceAll(
        '\\\\"',
        '\\"',
      ),
  });
  expect(test.document()).not.toHaveProperty("version");
});
it("edits every optional field and string metadata, retains order and restores focus on additions and removals", async () => {
  const test = await editor(sample);
  open("Optional Skill fields");
  change("Skill license (optional)", "Apache-2.0");
  change("Skill compatibility (optional)", "Requires git.");
  change("Skill allowed tools (optional)", "Read Bash(git:*)");
  click("Add Skill metadata");
  expect(document.activeElement).toBe(
    screen.getByLabelText("Skill metadata 1 key"),
  );
  change("Skill metadata 1 key", "version");
  change("Skill metadata 1 value", "1.0");
  click("Add Skill metadata");
  change("Skill metadata 2 key", "author");
  change("Skill metadata 2 value", "Example team");
  expect(test.document()).toMatchObject({
    license: "Apache-2.0",
    compatibility: "Requires git.",
    "allowed-tools": "Read Bash(git:*)",
    metadata: [
      ["version", "1.0"],
      ["author", "Example team"],
    ],
  });
  change("Skill metadata 2 key", "version");
  expect(screen.getByRole("alert").textContent).toContain("different key");
  expect(test.document().metadata![1]![0]).toBe("author");
  click("Remove Skill metadata 1");
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Add Skill metadata" }),
  );
  expect(test.document().metadata).toEqual([["author", "Example team"]]);
  click("Remove Skill metadata 1");
  for (const field of [
    "Skill license (optional)",
    "Skill compatibility (optional)",
    "Skill allowed tools (optional)",
  ])
    change(field, "");
  expect(test.document()).toEqual(readSkillDocument(sample).document);
  expect(test.value().content).not.toContain("compatibility:");
});
it("leaves untouched advanced values and Markdown intact, including the profiled Markdown media type", async () => {
  const original = writeSkillDocument({
    name: "review-notes",
    description: "Keep original.\nAnother line.",
    body: "\r\n# Original\r\n<script>neverExecute()</script>\r\n",
    license: " LICENSE.txt ",
    metadata: [
      ["version", "2.0"],
      ["author", "日本語 😀"],
    ],
    "allowed-tools": "Read Bash(git:*)",
  });
  const test = await editor(
    original,
    'text/markdown; profile="urn:air:agent-skills"',
  );
  await screen.findByLabelText("Skill name");
  change("Skill name", "renamed-notes");
  expect(test.document()).toEqual({
    ...readSkillDocument(original).document!,
    name: "renamed-notes",
  });
  expect(test.value().mediaType).toBe(
    'text/markdown; profile="urn:air:agent-skills"',
  );
  open("Advanced SKILL.md");
  expect(screen.queryByLabelText("Skill name")).toBeNull();
  expect(screen.getByLabelText("Text content")).toHaveProperty(
    "value",
    test.value().content.replaceAll("\r\n", "\n"),
  );
  const before = test.value().content;
  open("Advanced SKILL.md", false);
  await screen.findByLabelText("Skill name");
  expect(test.value().content).toBe(before);
});
it.each([
  "invalid YAML",
  '---\nname: old\ndescription: "Keep this."\ncompatibility: ""\n---\nbody',
  "---\nname: old\ndescription: |\n  Keep this block.\n---\n\nbody\n",
  "---\nname: old\ndescription: text\nextra: keep\n---\n",
])(
  "keeps unsupported imports unchanged and provides the raw correction path: %s",
  async (content) => {
    const test = await editor(content);
    await screen.findByText(/Your input is unchanged/);
    expect(test.changed).not.toHaveBeenCalled();
    click("Open Advanced SKILL.md");
    expect(screen.getByLabelText("Text content")).toHaveProperty(
      "value",
      content,
    );
    change("Text content", sample);
    open("Advanced SKILL.md", false);
    await screen.findByLabelText("Skill name");
    expect(test.value().content).toBe(sample);
    open("Advanced SKILL.md");
    change("Text content", "unfinished YAML:");
    open("Advanced SKILL.md", false);
    expect(test.value().content).toBe("unfinished YAML:");
    expect(await screen.findByText(/Your input is unchanged/)).not.toBeNull();
  },
);
it("validates decoded fields, shows accessible errors and keeps oversized changes from replacing the draft", async () => {
  const test = await editor(sample);
  await screen.findByLabelText("Skill name");
  change("Skill name", "Bad--Name");
  fireEvent.blur(screen.getByLabelText("Skill name"));
  expect(screen.getByLabelText("Skill name").getAttribute("aria-invalid")).toBe(
    "true",
  );
  expect(screen.getByText(/single hyphens between words/)).not.toBeNull();
  change("Skill name", "review-notes");
  expect(
    (screen.getByLabelText("Skill name") as HTMLInputElement).checkValidity(),
  ).toBe(true);
  change("Skill description", "😀".repeat(1024));
  expect(
    (
      screen.getByLabelText("Skill description") as HTMLInputElement
    ).checkValidity(),
  ).toBe(true);
  change("Skill description", "😀".repeat(1025));
  expect(
    (
      screen.getByLabelText("Skill description") as HTMLInputElement
    ).checkValidity(),
  ).toBe(false);
  open("Optional Skill fields");
  change("Skill compatibility (optional)", " ");
  expect(
    (
      screen.getByLabelText(
        "Skill compatibility (optional)",
      ) as HTMLInputElement
    ).checkValidity(),
  ).toBe(false);
  change("Skill compatibility (optional)", "");
  expect(
    (
      screen.getByLabelText(
        "Skill compatibility (optional)",
      ) as HTMLInputElement
    ).checkValidity(),
  ).toBe(true);
  const before = test.value().content;
  change("Skill instructions (Markdown)", "😀".repeat(125000));
  expect(screen.getByRole("alert").textContent).toContain(
    "500,000 UTF-8 bytes",
  );
  expect(test.value().content).toBe(before);
});
it("stages Markdown and imports it only after an explicit action, then focuses the raw content", async () => {
  const test = await editor(sample);
  vi.spyOn(window, "confirm").mockReturnValue(true);
  open("Import an existing Skill document");
  const content = sample.replace("review-notes", "imported-notes");
  const bytes = new TextEncoder().encode(content);
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: {
      files: [
        {
          name: "SKILL.md",
          size: bytes.length,
          arrayBuffer: async () => bytes.buffer,
        },
      ],
    },
  });
  await screen.findByRole("button", { name: "Use loaded document" });
  expect(test.value().content).toBe(sample);
  click("Use loaded document");
  await waitFor(() =>
    expect(document.activeElement).toBe(screen.getByLabelText("Text content")),
  );
  expect(test.value().content).toBe(content);
  open("Advanced SKILL.md", false);
  expect(await screen.findByLabelText("Skill name")).toHaveProperty(
    "value",
    "imported-notes",
  );
});
it.each(["ordinary", "metadata"])(
  "imports a long malformed %s field unchanged and retains the raw correction path",
  async (kind) => {
    const test = await editor(sample);
    vi.spyOn(window, "confirm").mockReturnValue(true);
    open("Import an existing Skill document");
    const field =
      kind === "ordinary"
        ? `description:${" ".repeat(40_000)}\rtext`
        : `description: valid\nmetadata:\n  key:${" ".repeat(40_000)}\rtext`;
    const content = `---\nname: imported-notes\n${field}\n---\nBody\r\n\u0085`;
    const bytes = new TextEncoder().encode(content);
    fireEvent.change(screen.getByLabelText("Document file"), {
      target: {
        files: [
          {
            name: "SKILL.md",
            size: bytes.length,
            arrayBuffer: async () => bytes.buffer,
          },
        ],
      },
    });
    await screen.findByRole("button", { name: "Use loaded document" });
    expect(test.changed).toHaveBeenLastCalledWith(
      expect.objectContaining({ content: sample, documentStaged: true }),
    );
    expect(test.value().content).toBe(sample);
    click("Use loaded document");
    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByLabelText("Text content"),
      ),
    );
    expect(test.value().content).toBe(content);
    open("Advanced SKILL.md", false);
    expect(await screen.findByText(/Your input is unchanged/)).toBeTruthy();
    expect(test.value().content).toBe(content);
    expect(screen.queryByLabelText("Skill name")).toBeNull();
  },
);
it("preserves an uploaded archive and handoff while adding a primary document for discovery", async () => {
  let current: RepresentationDraft[];
  const packageRow = {
    ...newRepresentation("skill"),
    editorID: "archive",
    mode: "package" as const,
    packageData: "exact bytes",
    mediaType: "application/agent-skills+gzip",
    handoff:
      '{"git":{"repository":"https://example.com/skills","commit":"' +
      "a".repeat(40) +
      '","directory":"review-notes"}}',
  };
  function Harness() {
    const [rows, setRows] = useState<RepresentationDraft[]>([
      packageRow,
      {
        ...newRepresentation("skill"),
        editorID: "unchanged",
        role: "documentation",
        mode: "url",
        url: "https://example.test/SKILL.md",
      },
    ]);
    current = rows;
    return <RepresentationList kind="skill" value={rows} onChange={setRows} />;
  }
  render(<Harness />);
  click("Add primary SKILL.md for discovery");
  await screen.findByLabelText("Skill name");
  expect(current!).toHaveLength(3);
  expect(current![0]).toEqual({ ...packageRow, role: "distribution" });
  expect(current!.filter((row) => row.role === "primary")).toHaveLength(1);
  expect(current![1]!.url).toBe("https://example.test/SKILL.md");
  expect(current![1]!.role).toBe("documentation");
  expect(current![2]!.mode).toBe("content");
  expect(
    screen.queryByRole("button", {
      name: "Add primary SKILL.md for discovery",
    }),
  ).toBeNull();
  const row = screen.getByRole("group", {
    name: /content 1$/,
  });
  expect(within(row).getByLabelText("Immutable handoff JSON")).toHaveProperty(
    "value",
    packageRow.handoff,
  );
});

it("preserves the existing Skill JSON import path without forcing a Markdown editor", async () => {
  let current: RepresentationDraft;
  function Harness() {
    const [value, setValue] = useState({
      ...newRepresentation("skill"),
      editorID: "json",
      mode: "data" as const,
      mediaType: "application/agent-skills+json",
    });
    current = value;
    return (
      <RepresentationEditor
        kind="skill"
        index={0}
        value={value}
        onChange={(next) => setValue(next as typeof value)}
      />
    );
  }
  render(<Harness />);
  const content =
    '{"name":"json-skill","description":"Keep metadata.","extension":{"version":"independent"}}';
  const bytes = new TextEncoder().encode(content);
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: {
      files: [
        {
          name: "skill.json",
          size: bytes.length,
          arrayBuffer: async () => bytes.buffer,
        },
      ],
    },
  });
  await screen.findByRole("button", { name: "Use loaded document" });
  click("Use loaded document");
  await waitFor(() => expect(current!.content).toBe(content));
  expect(screen.queryByText("Advanced SKILL.md")).toBeNull();
  expect(representationInput(current!).data).toEqual(JSON.parse(content));
});

it("clears a rejected field edit after the author explicitly reloads the same document", async () => {
  const test = await editor(sample);
  change("Skill instructions (Markdown)", "😀".repeat(125000));
  expect(screen.getByRole("alert").textContent).toContain("500,000");
  expect(test.value().content).toBe(sample);
  open("Import an existing Skill document");
  const bytes = new TextEncoder().encode(sample);
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: {
      files: [
        {
          name: "SKILL.md",
          size: bytes.length,
          arrayBuffer: async () => bytes.buffer,
        },
      ],
    },
  });
  await screen.findByRole("button", { name: "Use loaded document" });
  click("Use loaded document");
  open("Advanced SKILL.md", false);
  await screen.findByLabelText("Skill name");
  expect(test.value().content).toBe(sample);
  expect(screen.queryByRole("alert")).toBeNull();
});
