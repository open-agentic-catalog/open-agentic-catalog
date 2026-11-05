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
import { SourceTopology } from "./SourceTopology";
import { topologyInput } from "./federation-inputs";
import { ChoiceField, ActionForm } from "./authoring";
import { ArtworkEditor } from "./ArtworkEditor";
import { artworkInput, type ArtworkDescriptor } from "./artwork";
import { InputError } from "./input-validation";
import { SkillHandoffEditor, handoffInput } from "./SkillHandoff";
import { LocalDocument } from "./LocalDocument";
import { normalizedRatingIdentifier, RatingEntry } from "./Ratings";
import {
  workspace,
  reply,
  deferred,
  installDialog,
} from "../../tests/unit/fixtures";
import { administrator } from "../../tests/unit/screens";
import { testAccess } from "../../tests/unit/access-fixtures";
const closeDialog = installDialog();
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
// Restore modal primitives after this file's tests without changing other workers.
import { afterAll } from "vitest";
afterAll(closeDialog);
function click(name: string) {
  fireEvent.click(screen.getByRole("button", { name }));
}
function change(name: string, value: string) {
  fireEvent.change(screen.getByLabelText(name), { target: { value } });
}
function asset(alt: string) {
  return {
    url: "https://images.example/duplicate.png",
    alt,
    rights: { basis: "owned" as const, attribution: "Example" },
  };
}
it("moves focus with duplicate-URL artwork through repeated movement and deliberate removals", () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  function Editor() {
    const [value, onChange] = useState<ArtworkDescriptor>({
      schemaVersion: 1,
      screenshots: [asset("A"), asset("B"), asset("C")],
    });
    return <ArtworkEditor value={value} onChange={onChange} />;
  }
  render(<Editor />);
  fireEvent.click(screen.getByText("Icon and screenshots"));
  click("Add icon");
  expect(screen.getByLabelText("Icon HTTPS URL")).toBe(document.activeElement);
  click("Remove icon");
  expect(screen.getByRole("button", { name: "Add icon" })).toBe(
    document.activeElement,
  );
  click("Move screenshot 3 earlier");
  expect(screen.getByLabelText("Screenshot 2 alternative text")).toHaveProperty(
    "value",
    "C",
  );
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Move screenshot 2 earlier" }),
  );
  fireEvent.click(document.activeElement!);
  expect(screen.getByLabelText("Screenshot 1 alternative text")).toHaveProperty(
    "value",
    "C",
  );
  click("Remove screenshot 1");
  expect(screen.getByLabelText("Screenshot 1 alternative text")).toHaveProperty(
    "value",
    "C",
  );
  confirm.mockReturnValue(true);
  click("Remove screenshot 1");
  expect(screen.getByLabelText("Screenshot 1 HTTPS URL")).toBe(
    document.activeElement,
  );
  click("Remove screenshot 2");
  click("Remove screenshot 1");
  expect(screen.getByRole("button", { name: "Add screenshot" })).toBe(
    document.activeElement,
  );
  click("Add screenshot");
  expect(screen.getByLabelText("Screenshot 1 HTTPS URL")).toBe(
    document.activeElement,
  );
});
it.each([
  ["url", "http://images.example/x", "url"],
  ["alt", "", "alt"],
  ["attribution", "", "attribution"],
  ["reference", "http://bad.example", "reference"],
  ["basis", "invalid", "basis"],
])("maps artwork %s errors to the exact second row", (field, value, suffix) => {
  const item = asset("Second");
  if (field === "url" || field === "alt") item[field] = value!;
  else Object.assign(item.rights, { [field!]: value });
  try {
    artworkInput({ schemaVersion: 1, screenshots: [asset("First"), item] });
    throw Error("Expected rejection");
  } catch (error) {
    expect(error).toBeInstanceOf(InputError);
    expect((error as InputError).fieldId).toBe(
      `listing-artwork-screenshot-1-${suffix}`,
    );
  }
});
it("reveals a collapsed artwork field on rejection, preserving draft text and issuing no command", async () => {
  const run = vi.fn();
  function Editor() {
    const [value, onChange] = useState<ArtworkDescriptor>({
      schemaVersion: 1,
      icon: { ...asset(""), url: "https://images.example/icon.png" },
    });
    return (
      <ActionForm
        description="Prepare only"
        title="Save test listing"
        allowed
        onFailure={vi.fn()}
        prepare={() => {
          artworkInput(value);
          return { summary: [], run };
        }}
      >
        <ArtworkEditor value={value} onChange={onChange} />
      </ActionForm>
    );
  }
  render(<Editor />);
  click("Save test listing");
  const link = await screen.findByRole("link", {
    name: /Artwork alternative text|explicit OCI registry/,
  });
  fireEvent.click(link);
  expect(screen.getByLabelText("Icon alternative text")).toBe(
    document.activeElement,
  );
  expect(screen.getByLabelText("Icon HTTPS URL")).toHaveProperty(
    "value",
    "https://images.example/icon.png",
  );
  expect(run).not.toHaveBeenCalled();
});
it("maps guided Skill errors precisely and advanced JSON errors to JSON", () => {
  const handoff = {
    git: {
      repository: "https://git.example/team/repo",
      commit: "a".repeat(40),
      directory: "skills/demo",
    },
    oci: { reference: `registry.example/team/pkg@sha256:${"a".repeat(64)}` },
  };
  for (const [field, value] of [
    ["repository", "http://git.example/repo"],
    ["commit", "short"],
    ["directory", "../bad"],
  ]) {
    try {
      handoffInput(
        JSON.stringify({
          ...handoff,
          git: { ...handoff.git, [field!]: value },
        }),
        "row-handoff",
      );
      throw Error("Expected error");
    } catch (e) {
      expect(e).toMatchObject({
        fieldId: `row-handoff-git-${field}`,
        fallbackId: "row-handoff",
      });
    }
  }
  try {
    handoffInput('{"oci":{"reference":"bad"}}', "row-handoff");
    throw Error("Expected error");
  } catch (e) {
    expect(e).toMatchObject({ fieldId: "row-handoff-oci-reference" });
  }
  expect(() => handoffInput("{", "row-handoff")).toThrow(InputError);
  const run = vi.fn();
  render(
    <ActionForm
      description="Prepare only"
      title="Save handoff"
      allowed
      onFailure={vi.fn()}
      prepare={() => {
        handoffInput('{"oci":{"reference":"bad"}}', "row-handoff");
        return { summary: [], run };
      }}
    >
      <SkillHandoffEditor
        id="row-handoff"
        value='{"oci":{"reference":"bad"}}'
        onChange={vi.fn()}
      />
    </ActionForm>,
  );
  click("Save handoff");
  fireEvent.click(
    screen.getByRole("link", {
      name: /Artwork alternative text|explicit OCI registry/,
    }),
  );
  expect(screen.getByLabelText("Immutable handoff JSON")).toBe(
    document.activeElement,
  );
  click("Use guided immutable handoff");
  click("Save handoff");
  fireEvent.click(screen.getByRole("link", { name: /explicit OCI registry/ }));
  expect(screen.getByLabelText("OCI digest reference")).toBe(
    document.activeElement,
  );
  expect(run).not.toHaveBeenCalled();
});
it("discards a pending or ready local file and fences late reads without replacing text", async () => {
  const pending = deferred<ArrayBuffer>();
  const onLoad = vi.fn();
  const staged = vi.fn();
  render(
    <>
      <textarea id="document-editor" defaultValue="old valid text" />
      <LocalDocument
        editorId="document-editor"
        content="old valid text"
        format="text/markdown"
        json={false}
        onLoad={onLoad}
        onStagedChange={staged}
      />
    </>,
  );
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: {
      files: [
        { name: "pending.md", size: 10, arrayBuffer: () => pending.promise },
      ],
    },
  });
  click("Discard selected document");
  expect(document.activeElement).toBe(screen.getByRole("textbox"));
  await act(async () =>
    pending.resolve(new TextEncoder().encode("late result").buffer),
  );
  expect(
    screen.queryByRole("button", { name: "Use loaded document" }),
  ).toBeNull();
  expect(onLoad).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: {
      files: [
        {
          name: "ready.md",
          size: 3,
          arrayBuffer: async () => new TextEncoder().encode("new").buffer,
        },
      ],
    },
  });
  await screen.findByRole("button", { name: "Use loaded document" });
  click("Discard selected document");
  expect(staged).toHaveBeenLastCalledWith(false);
  expect(screen.getByRole("textbox")).toHaveProperty("value", "old valid text");
});
it("refreshes successful paged choices, retains selected labels and fences old pages", async () => {
  const pending = deferred<{ items: { id: string; label: string }[] }>();
  const load = vi.fn(async (cursor: string) =>
    cursor
      ? pending.promise
      : { items: [{ id: "same", label: "Name / one" }], nextCursor: "next" },
  );
  function Editor() {
    const [value, onChange] = useState("");
    return (
      <ChoiceField
        label="Publisher"
        value={value}
        onChange={onChange}
        load={load}
        onFailure={vi.fn()}
        manualLabel="Publisher ID"
      />
    );
  }
  render(<Editor />);
  await screen.findByRole("option", { name: "Name / one" });
  change("Publisher", "same");
  click("Next page");
  await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
  click("Refresh publisher choices"); // Disabled during a read; settle that page before the explicit refresh.
  await act(async () =>
    pending.resolve({ items: [{ id: "later", label: "Name / two" }] }),
  );
  change("Publisher", "later");
  click("Refresh publisher choices");
  await screen.findByRole("option", { name: "Name / one" });
  expect(screen.getByLabelText("Publisher")).toHaveProperty("value", "later");
  expect(
    screen.getByRole("option", { name: "Name / two (selected)" }),
  ).toBeTruthy();
  expect(load.mock.calls.at(-1)?.[0]).toBe("");
});
it("supports bounded audience assignments with semantic JSON preservation and independent manual entry", async () => {
  const f = workspace();
  const original = {
    referrals: { enabled: true, allowedHosts: ["peer.example"] },
    nesting: { enabled: false },
    discoveryAssignments: [],
  };
  function Editor({ admin = false }: { admin?: boolean }) {
    const [value, onChange] = useState(JSON.stringify(original));
    return (
      <SourceTopology
        {...f.props}
        access={
          admin
            ? administrator
            : { ...f.props.access, ...testAccess(["source_admin"]) }
        }
        value={value}
        onChange={onChange}
      />
    );
  }
  const view = render(<Editor />);
  fireEvent.click(screen.getByText("Advanced source participation"));
  expect(
    screen.queryByRole("button", { name: "Load configured host choices" }),
  ).toBeNull();
  click("Add discovery assignment");
  change("Discovery host 1", "catalog.example");
  change("Discovery audience 1", "authenticated");
  click("Edit topology as JSON");
  expect(
    JSON.parse(
      (screen.getByLabelText("Topology JSON") as HTMLTextAreaElement).value,
    ),
  ).toEqual({
    ...original,
    discoveryAssignments: [
      { host: "catalog.example", audience: "authenticated" },
    ],
  });
  click("Use guided topology");
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Remove discovery assignment 1");
  expect(screen.getByLabelText("Discovery host 1")).toBeTruthy();
  confirm.mockReturnValue(true);
  click("Remove discovery assignment 1");
  expect(screen.getByText("No discovery audience assignments.")).toBeTruthy();
  const addAssignment = screen.getByRole("button", {
    name: "Add discovery assignment",
  });
  for (let i = 0; i < 16; i++) fireEvent.click(addAssignment);
  expect(
    screen.getByRole("button", { name: "Add discovery assignment" }),
  ).toHaveProperty("disabled", true);
  expect(f.get).not.toHaveBeenCalled();
  f.get.mockResolvedValue(
    reply({
      items: [
        { host: "catalog.example", mode: "anonymous" },
        { host: "disabled.example", mode: "disabled" },
      ],
    }),
  );
  view.rerender(<Editor admin />);
  const disclosure = screen.getByText("Advanced source participation");
  expect(disclosure.closest("details")).toHaveProperty("open", false);
  fireEvent.click(disclosure);
  await act(async () => click("Load configured host choices"));
  await screen.findAllByRole("option", { name: "catalog.example (anonymous)" });
  change("Configured discovery host 1", "catalog.example");
  expect(screen.getByLabelText("Discovery host 1")).toHaveProperty(
    "value",
    "catalog.example",
  );
  expect(f.get).toHaveBeenCalledOnce();
  click("Hide configured host choices");
});
it("rejects normalized duplicate source audiences without altering the prepared supported policies", () => {
  const value = {
    referrals: { enabled: true },
    nesting: { enabled: false },
    discoveryAssignments: [
      { host: "Catalog.Example", audience: "anonymous" },
      { host: "catalog.example", audience: "authenticated" },
    ],
  };
  expect(topologyInput(JSON.stringify(value))).toEqual(value);
  expect(() =>
    topologyInput(
      JSON.stringify({
        ...value,
        discoveryAssignments: [
          value.discoveryAssignments[0],
          { host: "catalog.example", audience: "anonymous" },
        ],
      }),
    ),
  ).toThrow("unique");
  for (const assignments of [
    [{ host: "", audience: "anonymous" }],
    Array.from({ length: 17 }, () => ({
      host: "example.com",
      audience: "anonymous",
    })),
    [{ host: "example.com", audience: "other" }],
    [{ host: "x", audience: "anonymous", extra: true }],
  ])
    expect(() =>
      topologyInput(JSON.stringify({ discoveryAssignments: assignments })),
    ).toThrow();
});
it("sends no rating reads for opaque or non-normalized identifiers", () => {
  const f = workspace();
  for (const value of [
    "private-resource",
    "urn:air:EXAMPLE.COM:agents:demo",
    "urn:air:127.0.0.1:demo",
    "urn:air:example.com:..",
    "urn:air:example.com:",
    "urn:air:single:demo",
    "urn:air:example.com:é",
    "x".repeat(513),
  ])
    expect(normalizedRatingIdentifier(value)).toBe(false);
  for (const value of [
    "urn:air:example.com:demo",
    "urn:air:example.com:agents:Helper-1",
    "urn:air:example.com:_.demo",
  ])
    expect(normalizedRatingIdentifier(value)).toBe(true);
  render(
    <RatingEntry
      client={f.props.client}
      access={administrator}
      onFailure={vi.fn()}
      request={{
        target: {
          origin: "local",
          kind: "agent",
          identifier: "private-resource",
          versionPresent: true,
          version: "1",
          resourceId: "resource-1",
          versionRevisionId: "rev-1",
        },
      }}
    />,
  );
  expect(screen.queryByRole("button")).toBeNull();
  expect(f.get).not.toHaveBeenCalled();
});

it("refreshes the first page in place and recovers from a failed refresh without discarding other input", async () => {
  const load = vi.fn(async () => ({
    items: [
      { id: "one", label: "Same name / one" },
      { id: "two", label: "Same name / two" },
    ],
  }));
  function Editor() {
    const [value, onChange] = useState("one");
    return (
      <>
        <input aria-label="Unrelated draft" defaultValue="Keep me" />
        <ChoiceField
          label="Namespace"
          value={value}
          onChange={onChange}
          load={load}
          onFailure={vi.fn()}
        />
      </>
    );
  }
  render(<Editor />);
  await screen.findByRole("option", { name: "Same name / one" });
  load.mockRejectedValueOnce(new Error("Unavailable"));
  click("Refresh namespace choices");
  await screen.findByRole("alert");
  expect(screen.getByLabelText("Namespace")).toHaveProperty("value", "one");
  click("Reload");
  await screen.findByRole("option", { name: "Same name / two" });
  expect(screen.getByLabelText("Unrelated draft")).toHaveProperty(
    "value",
    "Keep me",
  );
  expect(load).toHaveBeenCalledTimes(3);
});
it("preserves multiple source rows and accepts a topology without optional assignments", async () => {
  const f = workspace();
  function Editor() {
    const [value, onChange] = useState("{}");
    return (
      <SourceTopology
        {...f.props}
        access={administrator}
        value={value}
        onChange={onChange}
      />
    );
  }
  render(<Editor />);
  fireEvent.click(screen.getByText("Advanced source participation"));
  click("Add discovery assignment");
  click("Add discovery assignment");
  change("Discovery host 1", "catalog.example");
  change("Discovery audience 1", "authenticated");
  click("Remove discovery assignment 2");
  click("Load configured host choices");
  await screen.findByRole("alert");
  change("Configured discovery host 1", "");
  expect(screen.getByLabelText("Discovery host 1")).toHaveProperty(
    "value",
    "catalog.example",
  );
  expect(() => topologyInput('{"referrals":[]}')).toThrow(
    "policies must be objects",
  );
  expect(() => topologyInput('{"nesting":false}')).toThrow();
});
