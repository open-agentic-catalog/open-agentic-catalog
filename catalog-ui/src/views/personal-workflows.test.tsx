// @vitest-environment jsdom
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { PersonalLibrary, SavePersonal, SaveResource } from "./Personal";
import { deferred } from "../../tests/unit/fixtures";
import {
  change,
  confirmCommand,
  etagReply,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";
import type { components } from "../api/schema";

screenLifecycle("/unit/saved-items");
const criteria: components["schemas"]["PersonalCriteria"] = {
  text: "private text",
  kinds: ["agent", "skill"],
  tags: ["a,b", "", " exact ", "a,b"],
  categories: [" First ", "First"],
  namespaceId: "00000000-0000-7000-8000-000000000001",
  origins: ["local"],
  publisher: "publisher-id",
  license: " Apache-2.0 ",
  visibility: "private",
};
const item = {
  id: "saved-1",
  type: "search" as const,
  label: "Research",
  search: criteria,
  revision: 1,
  createdAt: stamp,
  updatedAt: stamp,
};

it("summarizes local and external saved references without resolving their targets", async () => {
  const w = screenWorkspace();
  const references: components["schemas"]["PersonalReference"][] = [
    {
      origin: "local",
      releaseId: "release-1",
      versionRevisionId: "revision-1",
    },
    {
      origin: "foreign",
      kind: "skill",
      sourceId: "source-1",
      presentationGeneration: 3,
      identifier: "urn:example:skill",
      versionPresent: false,
    },
  ];
  w.get.mockResolvedValue(
    etagReply({
      items: references.map((reference, index) => ({
        ...item,
        id: `reference-${index}`,
        type: "resource",
        label: `Saved reference ${index}`,
        search: undefined,
        reference,
      })),
    }),
  );
  render(<PersonalLibrary {...w.props} />);
  await screen.findByRole("link", { name: "Saved reference 0" });
  expect(screen.getByText("Local release reference")).toBeTruthy();
  expect(screen.getByText("urn:example:skill")).toBeTruthy();
  expect(screen.getAllByText("Resource reference")).toHaveLength(2);
  expect(screen.queryByText("revision-1")).toBeNull();
  expect(w.get).toHaveBeenCalledOnce();
  expect(w.get.mock.calls[0]?.[0]).toBe("/api/v1/personal/items");
  expect(w.post).not.toHaveBeenCalled();
});

it("shows an unfiltered saved search summary without inventing criteria or resolving it", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [{ ...item, search: {} }] }));
  render(<PersonalLibrary {...w.props} />);
  await screen.findByRole("link", { name: item.label });
  expect(screen.getByText(/Any search text/).textContent).toContain(
    "Open for all saved filters",
  );
  expect(w.get).toHaveBeenCalledOnce();
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["   ", "bad\u007flabel"])(
  "rejects the invalid exact saved label %j before another read",
  async (label) => {
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply({ items: [] }));
    render(<PersonalLibrary {...w.props} />);
    await screen.findByText(
      "Your library is empty. Save a resource from Catalog or create a named search.",
    );
    change("Exact saved label", label);
    fireEvent.click(
      screen.getByRole("button", { name: "Apply saved item filters" }),
    );
    expect(screen.getByRole("alert").textContent).toContain(
      "cannot be whitespace only",
    );
    expect(document.activeElement).toBe(
      screen.getByLabelText("Exact saved label"),
    );
    expect(screen.getByLabelText("Exact saved label")).toHaveProperty(
      "value",
      label,
    );
    expect(w.get).toHaveBeenCalledOnce();
    fireEvent.click(
      screen.getByRole("button", { name: "Clear saved item filters" }),
    );
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByLabelText("Exact saved label")).toHaveProperty(
      "value",
      "",
    );
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("refuses unsupported resource reference replacement before confirmation or contact", async () => {
  const w = screenWorkspace();
  render(
    <SavePersonal
      {...w.props}
      basePath="/unit"
      initial={{
        type: "resource",
        label: "Saved release",
        reference: {
          origin: "local",
          releaseId: "release-1",
          versionRevisionId: "revision-1",
        },
      }}
    />,
  );
  fireEvent.click(screen.getByText("Edit exact resource reference"));
  change(
    "Exact resource reference JSON",
    '{"origin":"unknown","releaseId":"release-1"}',
  );
  fireEvent.click(screen.getByRole("button", { name: "Save resource" }));
  expect(screen.getByRole("alert").textContent).toContain(
    "Enter valid supported criteria or an exact resource reference",
  );
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
});

it("rejects controls in a saved label and an oversized otherwise valid criteria document locally", async () => {
  const w = screenWorkspace();
  render(
    <SavePersonal
      {...w.props}
      basePath="/unit"
      initial={{ type: "search", label: "Saved query", search: {} }}
    />,
  );
  change("Saved label", "bad\u007flabel");
  fireEvent.click(screen.getByRole("button", { name: "Save named search" }));
  expect(screen.getByRole("alert").textContent).toContain("without controls");
  change("Saved label", "Saved query");
  fireEvent.click(
    screen.getByRole("button", { name: "Edit private saved criteria as JSON" }),
  );
  const search = {
    text: "a".repeat(500),
    publisher: "b".repeat(256),
    license: "😀".repeat(256),
    tags: Array.from({ length: 32 }, () => "😀".repeat(80)),
    categories: Array.from({ length: 16 }, () => "😀".repeat(80)),
  };
  const payload = JSON.stringify(search);
  change("Private saved criteria JSON", payload);
  fireEvent.click(screen.getByRole("button", { name: "Save named search" }));
  expect(screen.getByRole("alert").textContent).toContain(
    "16 KiB request budget",
  );
  expect(screen.getByLabelText("Private saved criteria JSON")).toHaveProperty(
    "value",
    payload,
  );
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
});

it("uses one guided/advanced draft, preserves every supported value and captures a save without discovery", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(etagReply({ id: "new-item", revision: 1 }));
  render(
    <SavePersonal
      {...w.props}
      basePath="/unit"
      initial={{ type: "search", label: "Private research", search: criteria }}
    />,
  );
  expect(screen.getByLabelText("Saved search text")).toHaveProperty(
    "value",
    criteria.text,
  );
  change("Saved search text", "changed private text");
  fireEvent.click(
    screen.getByRole("button", { name: "Edit private saved criteria as JSON" }),
  );
  expect(
    JSON.parse(
      (
        screen.getByLabelText(
          "Private saved criteria JSON",
        ) as HTMLTextAreaElement
      ).value,
    ),
  ).toEqual({ ...criteria, text: "changed private text" });
  change(
    "Private saved criteria JSON",
    JSON.stringify({ ...criteria, text: "advanced private text" }),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Use guided private saved criteria" }),
  );
  expect(screen.getByLabelText("Saved search text")).toHaveProperty(
    "value",
    "advanced private text",
  );
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Save named search" }));
  const summary = within(screen.getByRole("dialog")).getByLabelText(
    "Command summary",
  );
  expect(summary.textContent).toContain("advanced private text");
  expect(summary.textContent).toContain(criteria.namespaceId);
  expect(summary.textContent).toContain('"a,b"');
  expect(w.post).not.toHaveBeenCalled();
  await act(async () =>
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Confirm",
      }),
    ),
  );
  await screen.findByText("Private item saved.");
  expect(w.post).toHaveBeenCalledTimes(1);
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/personal/items",
    expect.objectContaining({
      body: {
        type: "search",
        label: "Private research",
        search: { ...criteria, text: "advanced private text" },
      },
    }),
  );
});

it("keeps invalid advanced input visible, links prepare errors and sends no request", async () => {
  const w = screenWorkspace();
  render(
    <SavePersonal
      {...w.props}
      basePath="/unit"
      initial={{ type: "search", label: "Private", search: {} }}
    />,
  );
  expect(screen.getByText(/Origins are not specified/)).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Edit private saved criteria as JSON" }),
  );
  change("Private saved criteria JSON", '{"text":"one","text":"two"}');
  fireEvent.click(
    screen.getByRole("button", { name: "Use guided private saved criteria" }),
  );
  expect(screen.getByRole("alert").textContent).toContain("Duplicate JSON");
  expect(screen.getByLabelText("Private saved criteria JSON")).toHaveProperty(
    "value",
    '{"text":"one","text":"two"}',
  );
  change(
    "Private saved criteria JSON",
    JSON.stringify({ text: "é".repeat(251) }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Save named search" }));
  const error = await screen.findByRole("alert");
  fireEvent.click(within(error).getByRole("link"));
  expect(document.activeElement).toBe(
    screen.getByLabelText("Private saved criteria JSON"),
  );
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
});

it("stages literal library filters, pages only their applied values and never resolves rows", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [item], nextCursor: "next" }));
  render(<PersonalLibrary {...w.props} />);
  await screen.findByRole("link", { name: item.label });
  expect(screen.getByText(/"private text"/).textContent).toContain(
    "Agent, Skill",
  );
  expect(
    screen.getByText(new Date(stamp).toLocaleString()).getAttribute("datetime"),
  ).toBe(stamp);
  change("Exact saved label", " Case ");
  change("Saved item type", "search");
  expect(w.get).toHaveBeenCalledTimes(1);
  fireEvent.click(
    screen.getByRole("button", { name: "Apply saved item filters" }),
  );
  await waitFor(() =>
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/personal/items",
      expect.objectContaining({
        params: { query: { type: "search", label: " Case ", limit: 25 } },
      }),
    ),
  );
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await waitFor(() =>
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/personal/items",
      expect.objectContaining({
        params: {
          query: { type: "search", label: " Case ", limit: 25, cursor: "next" },
        },
      }),
    ),
  );
  change("Exact saved label", "new pending label");
  fireEvent.click(screen.getByRole("button", { name: "Refresh saved items" }));
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(4));
  expect(w.get).toHaveBeenLastCalledWith(
    "/api/v1/personal/items",
    expect.objectContaining({
      params: {
        query: { type: "search", label: " Case ", limit: 25, cursor: "next" },
      },
    }),
  );
  change("Saved item type", "resource");
  w.get.mockRejectedValueOnce(new Error("invalid filtered continuation"));
  fireEvent.click(screen.getByRole("button", { name: "Refresh saved items" }));
  await screen.findByRole("alert");
  expect(w.get).toHaveBeenCalledTimes(5);
  expect(w.get).toHaveBeenLastCalledWith(
    "/api/v1/personal/items",
    expect.objectContaining({
      params: {
        query: { type: "search", label: " Case ", limit: 25, cursor: "next" },
      },
    }),
  );
  expect(screen.getByRole("link", { name: item.label })).toBeTruthy();
  expect(screen.getByText("Last loaded saved library page")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Refresh saved items" }));
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(6));
  expect(w.get).toHaveBeenLastCalledWith(
    "/api/v1/personal/items",
    expect.objectContaining({
      params: { query: { type: "search", label: " Case ", limit: 25 } },
    }),
  );
  expect(screen.getByLabelText("Exact saved label")).toHaveProperty(
    "value",
    "new pending label",
  );
  expect(screen.getByLabelText("Saved item type")).toHaveProperty(
    "value",
    "resource",
  );
  expect(screen.getByText(/Applied: search/).textContent).toContain('" Case "');
  fireEvent.click(
    screen.getByRole("button", { name: "Clear saved item filters" }),
  );
  await waitFor(() =>
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/personal/items",
      expect.objectContaining({ params: { query: { limit: 25 } } }),
    ),
  );
  expect(screen.getByLabelText("Exact saved label")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.getByLabelText("Saved item type")).toHaveProperty("value", "");
  expect(w.post).not.toHaveBeenCalled();
});

it("retains an edited draft during pending/failed current reads and blocks new commands", async () => {
  const w = screenWorkspace();
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.get
    .mockResolvedValueOnce(etagReply(item))
    .mockReturnValueOnce(pending.promise);
  const discard = vi.spyOn(window, "confirm").mockReturnValue(true);
  render(<PersonalLibrary {...w.props} id={item.id} />);
  await screen.findByRole("heading", { name: item.label });
  change("Saved label", "Retained draft");
  fireEvent.click(
    screen.getByRole("button", { name: "Read current saved item" }),
  );
  expect(discard).toHaveBeenCalled();
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Update saved item" }),
    ).toHaveProperty("disabled", true),
  );
  expect(screen.getByLabelText("Saved label").matches(":disabled")).toBe(false);
  await act(async () => pending.reject(new Error("read failed")));
  await screen.findByRole("alert");
  expect(screen.getByLabelText("Saved label")).toHaveProperty(
    "value",
    "Retained draft",
  );
  expect(
    screen.getByRole("button", { name: "Update saved item" }),
  ).toHaveProperty("disabled", true);
  expect(screen.getByText("Last loaded saved item")).toBeTruthy();
  expect(w.patch).not.toHaveBeenCalled();
});

it("quick save exposes a captured read-only target and a structured confirmation", async () => {
  const w = screenWorkspace();
  const reference = {
    origin: "local" as const,
    releaseId: "release-1",
    versionRevisionId: "revision-1",
  };
  w.post.mockResolvedValue(etagReply({ id: "saved", revision: 1 }));
  render(
    <SaveResource
      {...w.props}
      basePath="/unit"
      label="Local release"
      reference={reference}
    />,
  );
  fireEvent.click(screen.getByText("Save resource"));
  await screen.findByRole("heading", { name: "Captured resource reference" });
  expect(screen.queryByLabelText("Exact resource reference JSON")).toBeNull();
  expect(screen.getByText("release-1")).toBeTruthy();
  change("Saved label", "My release");
  await confirmCommand("Save resource");
  await screen.findByText("Private item saved.");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/personal/items",
    expect.objectContaining({
      body: { type: "resource", label: "My release", reference },
    }),
  );
});

it("rejects a malformed creation receipt and replays only its captured body and key", async () => {
  const w = screenWorkspace();
  w.post
    .mockResolvedValueOnce(etagReply({ id: "new-item", revision: 2 }))
    .mockResolvedValue(etagReply({ id: "new-item", revision: 1 }));
  render(
    <SavePersonal
      {...w.props}
      basePath="/unit"
      initial={{
        type: "search",
        label: "Private",
        search: { text: "captured" },
      }}
    />,
  );
  await confirmCommand("Save named search");
  expect(screen.getByRole("dialog").textContent).toContain(
    "The outcome is unknown.",
  );
  expect(screen.queryByText("Private item saved.")).toBeNull();
  const captured = w.post.mock.calls[0]![1];
  fireEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: /Retry/ }),
  );
  await screen.findByText("Private item saved.");
  expect(w.post).toHaveBeenCalledTimes(2);
  expect(w.post.mock.calls[1]![1]).toEqual(captured);
});

it("a deliberate current read aborts an obsolete saved-reference resolution", async () => {
  const w = screenWorkspace();
  const reference = {
    origin: "local" as const,
    releaseId: "release-1",
    versionRevisionId: "revision-1",
  };
  const saved = {
    ...item,
    type: "resource" as const,
    search: undefined,
    reference,
  };
  w.get.mockResolvedValue(etagReply(saved));
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.post.mockReturnValue(pending.promise);
  render(<PersonalLibrary {...w.props} id={item.id} />);
  fireEvent.click(
    await screen.findByRole("button", { name: "Check saved reference" }),
  );
  const options = w.post.mock.calls[0]![1] as { signal: AbortSignal };
  fireEvent.click(
    screen.getByRole("button", { name: "Read current saved item" }),
  );
  await waitFor(() => expect(options.signal.aborted).toBe(true));
  await act(async () =>
    pending.resolve(etagReply({ status: "available", reference })),
  );
  expect(
    screen.queryByRole("link", { name: "Open exact saved release" }),
  ).toBeNull();
  expect(w.props.onFailure).not.toHaveBeenCalled();
});

it("shows access guidance instead of an empty read-only library editor and links invalid labels to their input", async () => {
  const w = screenWorkspace();
  const v = render(
    <SavePersonal
      {...w.props}
      basePath="/unit"
      access={{
        ...w.props.access,
        scopeEnforcement: true,
        scopes: ["catalog.read"],
      }}
      initial={{ type: "search", label: "Private", search: {} }}
    />,
  );
  expect(screen.queryByLabelText("Saved label")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Save named search" }),
  ).toBeNull();
  expect(
    screen.getByText(/Saving requires a human account/).textContent,
  ).toContain("library.own and personal.write");
  expect(screen.queryByText(/current item validator/)).toBeNull();
  v.rerender(
    <SavePersonal
      {...w.props}
      basePath="/unit"
      initial={{ type: "search", label: "Private", search: {} }}
    />,
  );
  change("Saved label", " ");
  fireEvent.click(screen.getByRole("button", { name: "Save named search" }));
  const error = await screen.findByRole("alert");
  fireEvent.click(within(error).getByRole("link"));
  expect(document.activeElement).toBe(screen.getByLabelText("Saved label"));
  expect(w.post).not.toHaveBeenCalled();
});
it("edits every guided criterion with exact native fields and preserves an omitted origin", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(etagReply({ id: "guided", revision: 1 }));
  render(
    <SavePersonal
      {...w.props}
      basePath="/unit"
      initial={{ type: "search", label: "Guided", search: {} }}
    />,
  );
  change("Saved search text", "guided text");
  fireEvent.click(screen.getByText("More saved filters"));
  fireEvent.click(screen.getByRole("checkbox", { name: "Agents" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Agents" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Skills" }));
  fireEvent.click(screen.getByRole("button", { name: "Add tag" }));
  change("Tag 1", " Exact, Tag ");
  fireEvent.click(screen.getByRole("button", { name: "Add category" }));
  change("Category 1", " Tools ");
  fireEvent.click(screen.getByRole("checkbox", { name: "Specify origins" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Indexed external" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Local Catalog" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Specify origins" }));
  change("Saved Namespace ID", "00000000-0000-7000-8000-000000000001");
  change("Saved Namespace ID", "");
  change("Saved Namespace ID", "00000000-0000-7000-8000-000000000001");
  change("Saved Publisher", "publisher");
  change("Saved Publisher", "");
  change("Saved license declaration", "Apache-2.0");
  change("Saved visibility", "tenant");
  change("Saved visibility", "");
  change("Saved visibility", "private");
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
  await confirmCommand("Save named search");
  await screen.findByText("Private item saved.");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/personal/items",
    expect.objectContaining({
      body: {
        type: "search",
        label: "Guided",
        search: {
          text: "guided text",
          kinds: ["skill"],
          tags: [" Exact, Tag "],
          categories: [" Tools "],
          namespaceId: "00000000-0000-7000-8000-000000000001",
          license: "Apache-2.0",
          visibility: "private",
        },
      },
    }),
  );
});
