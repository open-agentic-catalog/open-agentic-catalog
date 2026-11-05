// @vitest-environment jsdom
import { Profiler, useState } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { ValueList } from "./ValueList";
import {
  AdoptionMetadata,
  adoptionInput,
  adoptionShape,
} from "./AdoptionMetadata";
import { SkillHandoffEditor, handoffInput, handoffShape } from "./SkillHandoff";
import { sourceScope } from "./federation-inputs";
import { parseObject } from "./representations";
const git = {
  repository: "https://git.example.com/skills",
  commit: "a".repeat(40),
  directory: "skills/example",
};
const oci = {
  reference: "registry.example.com/skills@sha256:" + "b".repeat(64),
};
afterEach(cleanup);
const change = (label: string, value: string) =>
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
const click = (name: string) =>
  fireEvent.click(screen.getByRole("button", { name }));

it("focuses a value row added before the initial passive effects", async () => {
  function Rows() {
    const [value, setValue] = useState<string[]>([]);
    return (
      <ValueList
        id="tags"
        label="Tags"
        itemLabel="Tag"
        value={value}
        maximum={2}
        length={80}
        onChange={setValue}
      />
    );
  }
  let activated = false;
  // Exercise the same early interaction ordering as the representation editor.
  render(
    <Profiler
      id="early-value-action"
      onRender={() => {
        const add = screen.queryByRole("button", { name: "Add tag" });
        if (!activated && add) {
          activated = true;
          add.click();
        }
      }}
    >
      <Rows />
    </Profiler>,
  );
  const row = await screen.findByLabelText("Tag 1");
  expect(document.activeElement).toBe(row);
  expect(row).toHaveProperty("value", "");
});

it("uses the explicit item label without changing a group's wording or literal values", () => {
  const update = vi.fn();
  render(
    <ValueList
      id="policies"
      label="Companies and policies"
      itemLabel="Entry"
      value={["ACME, policy"]}
      maximum={2}
      length={80}
      onChange={update}
    />,
  );
  expect(
    screen.getByRole("group", { name: "Companies and policies" }),
  ).toBeTruthy();
  expect(screen.getByLabelText("Entry 1")).toHaveProperty(
    "value",
    "ACME, policy",
  );
  click("Add entry");
  expect(update).toHaveBeenLastCalledWith(["ACME, policy", ""]);
  click("Remove entry 1");
  expect(update).toHaveBeenLastCalledWith([]);
});
it("edits one literal value per row, bounds additions and removes explicitly without parsing commas or newlines", () => {
  const observe = vi.fn();
  function Rows() {
    const [value, setValue] = useState(["tools, data", "two\nlines"]);
    return (
      <ValueList
        id="tags"
        label="Tags"
        itemLabel="Tag"
        value={value}
        maximum={2}
        length={80}
        onChange={(next) => {
          setValue(next);
          observe(next);
        }}
      />
    );
  }
  render(<Rows />);
  expect(screen.getByLabelText("Tag 1")).toHaveProperty("value", "tools, data");
  expect(screen.getByLabelText("Tag 2")).toHaveProperty("value", "two\nlines");
  expect(screen.getByRole("button", { name: "Add tag" })).toHaveProperty(
    "disabled",
    true,
  );
  change("Tag 1", "literal,comma");
  expect(observe).toHaveBeenLastCalledWith(["literal,comma", "two\nlines"]);
  click("Remove tag 1");
  expect(document.activeElement).toBe(screen.getByLabelText("Tag 1"));
  click("Remove tag 1");
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Add tag" }),
  );
  expect(observe).toHaveBeenLastCalledWith([]);
  click("Add tag");
  expect(document.activeElement).toBe(screen.getByLabelText("Tag 1"));
  change("Tag 1", "new,value");
  expect(observe).toHaveBeenLastCalledWith(["new,value"]);
});
it("guided adoption and advanced JSON edit one value and preserve omissions versus explicit empty categories", () => {
  const observe = vi.fn();
  function Editor() {
    const [value, setValue] = useState('{"visibility":"private"}');
    return (
      <AdoptionMetadata
        value={value}
        onChange={(next) => {
          setValue(next);
          observe(adoptionShape(parseObject(next)));
        }}
      />
    );
  }
  render(<Editor />);
  fireEvent.click(screen.getByLabelText("Override local categories"));
  expect(observe).toHaveBeenLastCalledWith({
    visibility: "private",
    categories: [],
  });
  click("Add local category");
  change("Local category 1", "one,category");
  expect(observe).toHaveBeenLastCalledWith({
    visibility: "private",
    categories: ["one,category"],
  });
  click("Remove local category 1");
  fireEvent.click(screen.getByLabelText("Override local categories"));
  expect(observe).toHaveBeenLastCalledWith({ visibility: "private" });
  for (const [label, value] of [
    ["version", "1.0"],
    ["display name", "🙂".repeat(160)],
    ["description", "Description"],
    ["documentation URL", "https://example.com/docs"],
    ["support URL", "https://example.com/support"],
    ["license", "Apache-2.0"],
    ["terms URL", "https://example.com/terms"],
  ]) {
    fireEvent.click(
      screen.getByLabelText(`Override local ${label!.toLowerCase()}`),
    );
    change(`Local ${label!}`, value!);
  }
  fireEvent.click(screen.getByLabelText("Override local license"));
  expect(screen.queryByLabelText("Local license")).toBeNull();
  change("Local visibility", "tenant");
  click("Edit local metadata overrides as JSON");
  expect(screen.getByLabelText("Local metadata overrides JSON")).toHaveProperty(
    "value",
    JSON.stringify(observe.mock.calls.at(-1)?.[0]),
  );
  change(
    "Local metadata overrides JSON",
    '{"categories":[],"visibility":"public"}',
  );
  click("Use guided local metadata overrides");
  expect(screen.getByLabelText("Override local categories")).toHaveProperty(
    "checked",
    true,
  );
  expect(screen.getByLabelText("Override local display name")).toHaveProperty(
    "checked",
    false,
  );
  change("Local visibility", "");
  expect(observe).toHaveBeenLastCalledWith({ categories: [] });
});
it("rejects oversized guided updates without losing the previous draft and recovers on correction", () => {
  const observe = vi.fn();
  function Editor() {
    const [value, setValue] = useState('{"license":"Apache-2.0"}');
    return (
      <AdoptionMetadata
        value={value}
        onChange={(next) => {
          setValue(next);
          observe(next);
        }}
      />
    );
  }
  render(<Editor />);
  change("Local license", "A".repeat(2 * 1024 * 1024));
  expect(screen.getByRole("alert").textContent).toContain(
    "browser editor limit",
  );
  expect(observe).not.toHaveBeenCalled();
  expect(screen.getByLabelText("Local license")).toHaveProperty(
    "value",
    "Apache-2.0",
  );
  change("Local license", "MIT");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(observe).toHaveBeenLastCalledWith('{"license":"MIT"}');
});
it.each([
  ['{"snapshotId":"forged"}', "documented local metadata"],
  [
    '{"displayName":9007199254740992}',
    "Whole numbers must be between -9007199254740991 and 9007199254740991.",
  ],
])(
  "invalid advanced adoption (%s) remains editable and cannot silently replace the guided value",
  (text, message) => {
    function Editor() {
      const [value, setValue] = useState("{}");
      return <AdoptionMetadata value={value} onChange={setValue} />;
    }
    render(<Editor />);
    click("Edit local metadata overrides as JSON");
    change("Local metadata overrides JSON", text);
    click("Use guided local metadata overrides");
    expect(screen.getByRole("alert").textContent).toContain(message);
    expect(screen.getByRole("alert").textContent).not.toContain("URL");
    expect(
      screen.getByLabelText("Local metadata overrides JSON"),
    ).toHaveProperty("value", text);
    change("Local metadata overrides JSON", "{}");
    click("Use guided local metadata overrides");
    expect(screen.queryByRole("alert")).toBeNull();
  },
);
it.each([
  { version: "" },
  { displayName: "🙂".repeat(161) },
  { description: "\0" },
  { license: "NUL\0license" },
  { supportUrl: "http://example.com" },
  { termsUrl: "https://example.com?token=secret" },
  { categories: Array.from({ length: 17 }, (_, i) => String(i)) },
  { categories: ["x", "x"] },
  { categories: ["🙂".repeat(81)] },
  { visibility: "unknown" },
  { visibility: null },
  { version: 1 },
  { categories: [1] },
  { categories: null },
  { snapshotId: "forged" },
])("rejects an invalid supported adoption contract (%j)", (data) => {
  expect(() => adoptionInput(data)).toThrow();
});
it("admits omitted and explicit values without inserting overrides", () => {
  expect(adoptionInput({})).toEqual({});
  expect(
    adoptionInput({ license: "Full license declaration " + "a".repeat(1000) }),
  ).toEqual({ license: "Full license declaration " + "a".repeat(1000) });
  expect(
    adoptionInput({
      categories: [],
      license: "",
      displayName: "🙂".repeat(160),
      description: "🙂".repeat(4000),
    }),
  ).toEqual({
    categories: [],
    license: "",
    displayName: "🙂".repeat(160),
    description: "🙂".repeat(4000),
  });
});
it("guides both immutable references without losing either when changing editor, then removes each explicitly", () => {
  const observe = vi.fn();
  function Editor() {
    const [value, setValue] = useState("");
    return (
      <SkillHandoffEditor
        value={value}
        onChange={(next) => {
          setValue(next);
          observe(next);
        }}
      />
    );
  }
  render(<Editor />);
  const prerequisite = screen.getByText(
    /Supply at least one immutable Git or OCI reference/,
  );
  expect(prerequisite.textContent).toContain("both are allowed");
  expect(prerequisite.textContent).toContain(
    "does not verify remote availability",
  );
  fireEvent.click(screen.getByLabelText("Include Git handoff"));
  change("Git repository", git.repository);
  change("Git commit", git.commit);
  change("Git directory", git.directory);
  fireEvent.click(screen.getByLabelText("Include OCI handoff"));
  change("OCI digest reference", oci.reference);
  expect(handoffInput(observe.mock.calls.at(-1)?.[0] as string)).toEqual({
    git,
    oci,
  });
  click("Edit immutable handoff as JSON");
  expect(
    screen.getByText(/Supply at least one immutable Git or OCI reference/),
  ).toBe(prerequisite);
  expect(screen.getByLabelText("Immutable handoff JSON")).toHaveProperty(
    "value",
    JSON.stringify({ git, oci }),
  );
  change("Immutable handoff JSON", "not JSON");
  click("Use guided immutable handoff");
  expect(screen.getByRole("alert").textContent).toContain("valid JSON");
  change("Immutable handoff JSON", JSON.stringify({ git, oci }));
  click("Use guided immutable handoff");
  fireEvent.click(screen.getByLabelText("Include Git handoff"));
  expect(
    screen.getByText(/Supply at least one immutable Git or OCI reference/),
  ).toBe(prerequisite);
  expect(handoffInput(observe.mock.calls.at(-1)?.[0] as string)).toEqual({
    oci,
  });
  fireEvent.click(screen.getByLabelText("Include OCI handoff"));
  expect(() => handoffInput(observe.mock.calls.at(-1)?.[0] as string)).toThrow(
    /at least one/,
  );
});
it("explains missing references for untouched guided input without accepting malformed JSON", () => {
  expect(() => handoffInput("")).toThrow(
    "Supply at least one immutable Git or OCI reference.",
  );
  expect(() => handoffInput("{}")).toThrow(
    "Supply at least one immutable Git or OCI reference.",
  );
  expect(() => handoffInput("{")).toThrow("Enter valid JSON.");
});
it.each([
  {},
  { type: "git" },
  { git: null },
  { git: [] },
  { git: { ...git, token: "secret" } },
  { git: { ...git, commit: 42 } },
  { git: { ...git, commit: "main" } },
  { git: { ...git, repository: "https://user:pass@example.com/repo" } },
  { git: { ...git, repository: "https://example.com/" } },
  { git: { ...git, repository: "https://example.com/repo?x=y" } },
  { git: { ...git, repository: "https://example.com/team/./repo" } },
  { git: { ...git, repository: "https://example.com/team/../repo" } },
  { git: { ...git, repository: "https://example.com/team//repo" } },
  { git: { ...git, repository: "https://example.com/team\\repo" } },
  { git: { ...git, repository: "https://example.com/team repo" } },
  { git: { ...git, repository: "https://example.com/%72epo" } },
  { git: { ...git, repository: "https://example.com:0/repo" } },
  { git: { ...git, repository: "https://🙂.example.com/repo" } },
  { git: { ...git, directory: "../skill" } },
  { git: { ...git, directory: "/skill" } },
  { git: { ...git, directory: "C:skill" } },
  { git: { ...git, directory: "skills//one" } },
  { git: { ...git, directory: "skills./one" } },
  { git: { ...git, directory: "skills /one" } },
  { git: { ...git, directory: "🙂".repeat(64) } },
  { git: { ...git, directory: Array(18).fill("skill").join("/") } },
  {
    git: {
      ...git,
      directory: ("a".repeat(250) + "/").repeat(4) + "🙂".repeat(10),
    },
  },
  { oci: { reference: "registry.example.com/skills:latest" } },
  {
    oci: { reference: "registry.example.com/skills@sha256:" + "A".repeat(64) },
  },
  {
    oci: {
      reference:
        "🙂".repeat(500) + ".example.com/skills@sha256:" + "a".repeat(64),
    },
  },
  {
    oci: { reference: "🙂.example.com/skills@sha256:" + "a".repeat(64) },
  },
])("rejects invalid immutable handoff (%j)", (value) => {
  expect(() => handoffInput(JSON.stringify(value))).toThrow();
});
it("admits Git/OCI combinations and full SHA256 Git commits, including a trailing repository slash", () => {
  expect(handoffInput(JSON.stringify({ git, oci }))).toEqual({ git, oci });
  const value = {
    git: {
      ...git,
      repository: git.repository + "/",
      commit: "A".repeat(64),
      directory: ".metadata/skill",
    },
  };
  expect(handoffInput(JSON.stringify(value))).toEqual({
    git: { ...value.git, commit: "a".repeat(64) },
  });
  expect(handoffShape({})).toEqual({});
  const directory = ("🙂".repeat(63) + "/").repeat(4) + "skill";
  expect(handoffInput(JSON.stringify({ git: { ...git, directory } }))).toEqual({
    git: { ...git, directory },
  });
});
it.each([
  { tags: ["one,tag"] },
  { identifiers: ["urn:air:example.com:agents:one"] },
  { versions: ["1.0"] },
  { versions: ["🙂".repeat(128)] },
  { identifiers: ["🙂".repeat(512)] },
  { excludedIdentifiers: ["🙂".repeat(512)], kinds: ["agent"] },
  { publishers: ["Example, Inc."] },
  { kinds: ["plugin"] },
])("preserves one exact scope value (%j)", (value) => {
  expect(sourceScope(value)).toEqual(value);
});
it.each([
  { kinds: ["tool"] },
  { kinds: ["agent", "mcp_server", "skill", "plugin", "tool"] },
  { tags: ["a", "a"] },
  { tags: ["a "] },
  { versions: ["🙂".repeat(129)] },
  { tags: Array.from({ length: 101 }, (_, i) => String(i)) },
  { identifiers: ["a".repeat(513)] },
  { publishers: ["p".repeat(256)] },
])("rejects over-limit or ambiguous scopes (%j)", (value) => {
  expect(() => sourceScope(value)).toThrow();
});

it("keeps exact source exclusions separate from positive selection", () => {
  expect(sourceScope({ kinds: ["skill"], excludedIdentifiers: [] })).toEqual({
    kinds: ["skill"],
    excludedIdentifiers: [],
  });
  const value = {
    kinds: ["agent"],
    excludedIdentifiers: ["urn:air:EXAMPLE.COM:Team:Agent", "legacy:item"],
  };
  expect(sourceScope(value)).toEqual(value);
  for (const scope of [
    { excludedIdentifiers: [] },
    { excludedIdentifiers: ["legacy:item"] },
    { kinds: ["agent"], excludedIdentifiers: null },
    { kinds: ["agent"], excludedIdentifiers: ["x", "x"] },
    { kinds: ["agent"], excludedIdentifiers: [" x"] },
    { kinds: ["agent"], excludedIdentifiers: ["🙂".repeat(513)] },
    {
      kinds: ["agent"],
      excludedIdentifiers: Array.from({ length: 1001 }, (_, i) => String(i)),
    },
  ])
    expect(() => sourceScope(scope)).toThrow();
});
