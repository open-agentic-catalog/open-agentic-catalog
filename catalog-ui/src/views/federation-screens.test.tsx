import { editValues } from "../../tests/unit/screens";
import { testAccess } from "../../tests/unit/access-fixtures";
// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { SourceSnapshot, SourceSnapshots } from "./SourceSnapshots";
import { Observations, ObservationGroup } from "./Observations";
import { FederationSearch } from "./FederationSearch";
import Federation, { Inspection, ScopeInput } from "./Federation";
import { sourceValues } from "./federation-inputs";
import {
  installDialog,
  reader,
  reply,
  workspace,
} from "../../tests/unit/fixtures";

const sourceAdmin = { ...reader, ...testAccess(["source_admin" as const]) };
let restoreDialog: () => void;
beforeEach(() => {
  history.replaceState(null, "", "/unit/observations");
  restoreDialog = installDialog();
});
afterEach(() => {
  cleanup();
  restoreDialog();
  vi.restoreAllMocks();
});

it.each([
  SourceSnapshot,
  Observations,
  ObservationGroup,
  FederationSearch,
  Federation,
  Inspection,
])("%s denies readers without making private requests", (Screen) => {
  const { props, get } = workspace();
  render(<Screen {...props} />);
  expect(screen.getByRole("alert").textContent).toContain("source.read");
  expect(get).not.toHaveBeenCalled();
});

it("discards only a rejected snapshot cursor and disables busy history paging", async () => {
  const { props, get } = workspace();
  let finish!: (value: ReturnType<typeof reply>) => void;
  get
    .mockResolvedValueOnce(reply({ items: [], nextCursor: "opaque-next" }))
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    )
    .mockRejectedValueOnce(new Error("cursor rejected"))
    .mockResolvedValue(reply({ items: [] }));
  render(<SourceSnapshots {...props} access={sourceAdmin} id="source-a" />);
  fireEvent.click(await screen.findByRole("button", { name: "Next page" }));
  expect(
    screen.getByRole("group", { name: "Synchronization snapshot pages" }),
  ).toBeTruthy();
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "First page" })).toHaveProperty(
      "disabled",
      true,
    ),
  );
  expect(
    screen.getByRole("button", { name: "Refresh snapshots" }),
  ).toHaveProperty("disabled", true);
  await act(async () =>
    finish(reply({ items: [], nextCursor: "another-next" })),
  );
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("alert");
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await screen.findByText("No snapshots.");
  expect(get.mock.calls[3]?.[1]).toMatchObject({
    params: { path: { sourceId: "source-a" }, query: { limit: 25 } },
  });
  expect(screen.queryByRole("button", { name: "First page" })).toBeNull();
  expect(get).toHaveBeenCalledTimes(4);
});

it("pages snapshot summaries without fetching their evidence", async () => {
  const { props, get } = workspace();
  get
    .mockResolvedValueOnce(
      reply({
        items: [
          {
            id: "snapshot-a",
            sourceId: "source-a",
            sourceRevision: 1,
            startedAt: "2026-01-01T00:00:00Z",
            state: "complete",
            memberCount: 2,
            withdrawalAuthority: true,
            enumerationComplete: true,
            added: 0,
            updated: 0,
            withdrawn: 0,
          },
          {
            id: "snapshot-partial",
            sourceId: "source-a",
            sourceRevision: 1,
            startedAt: "2026-01-02T00:00:00Z",
            state: "incomplete",
            memberCount: 1,
            withdrawalAuthority: false,
            enumerationComplete: false,
            added: 0,
            updated: 0,
            withdrawn: 0,
          },
        ],
        nextCursor: "next",
      }),
    )
    .mockResolvedValueOnce(reply({ items: [] }));
  render(<SourceSnapshots {...props} access={sourceAdmin} id="source-a" />);
  const link = (await screen.findAllByRole("link")).find(
    (link) => link.getAttribute("href") === "/unit/source-snapshots/snapshot-a",
  )!;
  expect(link.querySelector("time")?.dateTime).toBe("2026-01-01T00:00:00Z");
  expect(link.getAttribute("href")).toBe("/unit/source-snapshots/snapshot-a");
  const items = screen.getAllByRole("article");
  expect(
    within(items[0]!).getByText("All entries enumerated").nextElementSibling
      ?.textContent,
  ).toBe("Yes");
  expect(
    within(items[0]!).getByText("Withdrawal authority").nextElementSibling
      ?.textContent,
  ).toBe("Yes");
  expect(
    within(items[1]!).getByText("Snapshot state").nextElementSibling
      ?.textContent,
  ).toBe("Incomplete");
  expect(
    within(items[1]!).getByText("Entries").nextElementSibling?.textContent,
  ).toBe("1");
  expect(
    within(items[1]!).getByText("Withdrawal authority").nextElementSibling
      ?.textContent,
  ).toBe("No");
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "Next page" })),
  );
  expect(screen.getByText("No snapshots.")).toBeTruthy();
  expect(
    get.mock.calls.map(([path, options]) => [
      path,
      (options as { params: unknown }).params,
    ]),
  ).toEqual([
    [
      "/api/v1/federation/sources/{sourceId}/snapshots",
      {
        path: { sourceId: "source-a" },
        query: { limit: 25, cursor: undefined },
      },
    ],
    [
      "/api/v1/federation/sources/{sourceId}/snapshots",
      { path: { sourceId: "source-a" }, query: { limit: 25, cursor: "next" } },
    ],
  ]);
});

it("retains an exact snapshot across failed refresh and recovers without fetching its source", async () => {
  const { props, get } = workspace();
  const snapshot = {
    id: "snapshot-a",
    sourceId: "source-a",
    sourceRevision: 1,
    startedAt: "2026-01-01T00:00:00Z",
    state: "complete",
    memberCount: 0,
    withdrawalAuthority: true,
    enumerationComplete: true,
    added: 0,
    updated: 0,
    withdrawn: 0,
  };
  get
    .mockRejectedValueOnce(new Error("private upstream detail"))
    .mockResolvedValueOnce(reply(snapshot));
  render(<SourceSnapshot {...props} access={sourceAdmin} id="snapshot-a" />);
  expect((await screen.findByRole("alert")).textContent).not.toContain(
    "private upstream",
  );
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "Reload" })),
  );
  expect(
    screen.getByRole("link", { name: "Source" }).getAttribute("href"),
  ).toBe("/unit/sources/source-a");
  expect(get).toHaveBeenCalledTimes(2);
  const withdrawalHelp = screen
    .getByText("How snapshot withdrawal works")
    .closest("details")!;
  expect(withdrawalHelp.open).toBe(false);
  fireEvent.click(screen.getByText("How snapshot withdrawal works"));
  expect(withdrawalHelp.open).toBe(true);
  expect(withdrawalHelp.textContent).toContain(
    "Failed or incomplete synchronization preserves the last complete source view.",
  );
  expect(get).toHaveBeenCalledTimes(2);
  expect(get).toHaveBeenLastCalledWith(
    "/api/v1/federation/snapshots/{snapshotId}",
    expect.objectContaining({ params: { path: { snapshotId: "snapshot-a" } } }),
  );
  expect(props.onFailure).toHaveBeenCalledOnce();
  get
    .mockRejectedValueOnce(new Error("private refresh detail"))
    .mockResolvedValueOnce(reply(snapshot));
  fireEvent.click(screen.getByRole("button", { name: "Reload snapshot" }));
  expect((await screen.findByRole("alert")).textContent).not.toContain(
    "private refresh",
  );
  expect(
    screen.getByRole("heading", { name: "Snapshot: Complete" }),
  ).toBeTruthy();
  expect(
    screen.getByRole("link", { name: "Source" }).getAttribute("href"),
  ).toBe("/unit/sources/source-a");
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  expect(
    screen.getByRole("heading", { name: "Snapshot: Complete" }),
  ).toBeTruthy();
  expect(get).toHaveBeenCalledTimes(4);
  for (const call of get.mock.calls)
    expect(call).toEqual([
      "/api/v1/federation/snapshots/{snapshotId}",
      expect.objectContaining({
        params: { path: { snapshotId: "snapshot-a" } },
      }),
    ]);
  expect(props.onFailure).toHaveBeenCalledTimes(2);
});

it("keeps observation paging source-bound and resets the cursor on freshness changes", async () => {
  history.replaceState(
    null,
    "",
    "/unit/observations?source=00000000-0000-7000-8000-000000000099",
  );
  const { props, get } = workspace();
  get
    .mockResolvedValueOnce(reply({ items: [], nextCursor: "next" }))
    .mockResolvedValue(reply({ items: [] }));
  render(<Observations {...props} access={sourceAdmin} />);
  fireEvent.click(await screen.findByRole("button", { name: "Next page" }));
  await screen.findByRole("button", { name: "First page" });
  await act(async () =>
    fireEvent.change(screen.getByRole("combobox", { name: "Show entries" }), {
      target: { value: "all" },
    }),
  );
  expect(get).toHaveBeenCalledTimes(2);
  await act(async () =>
    fireEvent.click(
      screen.getByRole("button", { name: "Apply observation filters" }),
    ),
  );
  expect(get.mock.calls.map(([path]) => path)).toEqual(
    Array(3).fill("/api/v1/observations"),
  );
  expect(
    get.mock.calls.map(
      ([, options]) => (options as { params: { query: unknown } }).params.query,
    ),
  ).toEqual([
    {
      sourceId: "00000000-0000-7000-8000-000000000099",
      freshness: "current",
      limit: 25,
      cursor: undefined,
    },
    {
      sourceId: "00000000-0000-7000-8000-000000000099",
      freshness: "current",
      limit: 25,
      cursor: "next",
    },
    {
      sourceId: "00000000-0000-7000-8000-000000000099",
      freshness: "all",
      limit: 25,
      cursor: undefined,
    },
  ]);
});

it.each([
  {
    state: "incomplete",
    reconcileCurrentView: false,
    summary: "cannot replace the last complete source view",
  },
  {
    state: "complete",
    reconcileCurrentView: true,
    summary: "without becoming authoritative upstream withdrawals",
  },
])(
  "explains retained snapshot outcome independently of withdrawal authority: %j",
  async ({ state, reconcileCurrentView, summary }) => {
    const { props, get } = workspace();
    get.mockResolvedValue(
      reply({
        id: "snapshot-a",
        sourceId: "source-a",
        sourceRevision: 1,
        state,
        enumerationComplete: state === "complete",
        withdrawalAuthority: false,
        reconcileCurrentView,
        memberCount: 7,
        added: 2,
        updated: 5,
        withdrawn: 0,
        startedAt: "2026-01-01T00:00:00Z",
        completedAt: "2026-01-01T00:01:00Z",
      }),
    );
    render(<SourceSnapshot {...props} id="snapshot-a" access={sourceAdmin} />);
    await screen.findByRole("heading", {
      name: `Snapshot: ${state === "complete" ? "Complete" : "Incomplete"}`,
    });
    for (const [label, value] of [
      ["Entry count", "7"],
      ["Added", "2"],
      ["Updated", "5"],
      ["Marked withdrawn", "0"],
      ["Withdrawal authority", "No"],
    ] as const)
      expect(screen.getByText(label).nextElementSibling?.textContent).toBe(
        value,
      );
    expect(screen.getByText(new RegExp(summary))).toBeTruthy();
    expect(
      screen
        .getByRole("link", { name: "Browse source observations" })
        .getAttribute("href"),
    ).toBe("/unit/observations?source=source-a");
    expect(
      screen.getByText("Snapshot and retrievals").closest("details"),
    ).toHaveProperty("open", false);
    expect(get).toHaveBeenCalledTimes(1);
  },
);

it("loads raw evidence only when requested and keeps it inert", async () => {
  const { props, get } = workspace();
  const raw = "<script>privateEvidence()</script>";
  get.mockResolvedValueOnce(reply({ items: [] })).mockResolvedValueOnce({
    data: raw,
    response: new Response(raw, {
      headers: { "Content-Digest": "sha-256=:fixture:" },
    }),
  });
  const view = render(
    <Observations {...props} access={sourceAdmin} id="observation-a" />,
  );
  await screen.findByText("No complete retained occurrences.");
  expect(
    screen.getByText(
      /Each occurrence records where and when the source supplied this entry/,
    ),
  ).toBeTruthy();
  const adoptionGuidance = screen.getByText(
    /Adoption creates a local draft, never approval or publication/,
  );
  expect(adoptionGuidance.closest("details")).toBeNull();
  expect(get).toHaveBeenCalledTimes(1);
  await act(async () =>
    fireEvent.click(
      screen.getByRole("button", { name: "Read exact raw entry" }),
    ),
  );
  expect(screen.getByText(raw)).toBeTruthy();
  expect(screen.getByText("sha-256=:fixture:")).toBeTruthy();
  expect(view.container.querySelector("script")).toBeNull();
  expect(get).toHaveBeenLastCalledWith(
    "/api/v1/observations/{observationId}/raw",
    expect.objectContaining({
      params: { path: { observationId: "observation-a" } },
      parseAs: "text",
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Hide raw entry" }));
  expect(screen.queryByText(raw)).toBeNull();
  expect(get).toHaveBeenCalledTimes(2);
});

it("keeps an empty-version observation inspectable without requesting an invalid group", async () => {
  history.replaceState(
    null,
    "",
    "/unit/observation-group?identifier=urn:example:agent&versionPresent=true&version=",
  );
  const { props, get } = workspace();
  render(<ObservationGroup {...props} access={sourceAdmin} />);
  await screen.findByText(/A present-empty version cannot be compared/);
  expect(get).not.toHaveBeenCalled();
  expect(screen.queryByText("Group evidence")).toBeNull();
});

it("sends diagnostic queries only on explicit search or live confirmation", async () => {
  const { props, get } = workspace();
  const post = vi
    .fn()
    .mockResolvedValueOnce(reply({ items: [] }))
    .mockResolvedValueOnce(reply({ results: [], partial: true }));
  const client = { ...props.client, api: { ...props.client.api, POST: post } };
  render(<FederationSearch {...props} client={client} access={sourceAdmin} />);
  expect(post).not.toHaveBeenCalled();
  expect(get).not.toHaveBeenCalled();
  fireEvent.change(screen.getByRole("textbox", { name: "Query" }), {
    target: { value: " example " },
  });
  editValues("Observation tags", ["support", "support", "demo"]);
  fireEvent.click(screen.getByRole("checkbox", { name: "Agents" }));
  await act(async () =>
    fireEvent.click(
      screen.getByRole("button", { name: "Search indexed observations" }),
    ),
  );
  expect(post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/observations:search",
    expect.objectContaining({
      body: {
        text: "example",
        tags: ["support", "demo"],
        kinds: ["agent"],
        limit: 25,
        cursor: undefined,
      },
    }),
  );
  fireEvent.change(
    screen.getByRole("textbox", {
      name: "Source IDs (optional)",
    }),
    { target: { value: "source-a,source-a" } },
  );
  fireEvent.change(screen.getByRole("textbox", { name: "Reason" }), {
    target: { value: "Investigate partial response" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Query live sources" }));
  expect(post).toHaveBeenCalledTimes(1);
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "Confirm" })),
  );
  expect(post).toHaveBeenLastCalledWith(
    "/api/v1/federation/sources:live-search",
    {
      params: { header: { "Idempotency-Key": expect.any(String) as unknown } },
      body: {
        text: "example",
        reason: "Investigate partial response",
        sourceIds: ["source-a"],
      },
    },
  );
  expect(
    screen.getByText(/Some source queries or results are incomplete/),
  ).toBeTruthy();
  expect(props.onFailure).not.toHaveBeenCalled();
});

it("normalizes source lists without folding case or splitting spaces within values", () => {
  expect(sourceValues(" A, A\nB B, b b,, ")).toEqual(["A", "B B", "b b"]);
  expect(sourceValues("")).toEqual([]);
});

it("reports a truncated comparison separately from an empty or complete contribution list", async () => {
  history.replaceState(
    null,
    "",
    "/unit/observation-group?identifier=urn:example:agent&versionPresent=false",
  );
  const { props, get } = workspace();
  get.mockResolvedValue(
    reply({
      identifier: "urn:example:agent",
      versionPresent: false,
      conflict: true,
      contributionsTruncated: true,
      members: [],
    }),
  );
  render(<ObservationGroup {...props} access={sourceAdmin} />);
  await screen.findByText(
    "Different entry contents; contribution list truncated",
  );
  expect(get).toHaveBeenCalledExactlyOnceWith("/api/v1/observation-groups", {
    params: {
      query: { identifier: "urn:example:agent", versionPresent: false },
    },
    signal: expect.any(AbortSignal) as unknown,
  });
});

it("clears exact excluded identifiers without reinterpreting their literal values", () => {
  const onChange = vi.fn();
  const props = {
    label: "Excluded identifiers",
    hint: "Exclude these exact identities.",
    onChange,
  };
  const ui = render(
    <ScopeInput {...props} value={["urn:example:literal,identifier"]} />,
  );
  const input = screen.getByLabelText("Excluded identifier 1");
  expect(input).toHaveProperty("id", "scope-excludedIdentifiers");
  expect(input).toHaveProperty("value", "urn:example:literal,identifier");
  fireEvent.click(
    screen.getByRole("button", { name: "Clear excluded identifiers" }),
  );
  expect(onChange).toHaveBeenCalledExactlyOnceWith([]);
  ui.rerender(<ScopeInput {...props} value={[]} />);
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(
    screen.getByRole("button", { name: "Add excluded identifier" }),
  ).toHaveProperty("id", "scope-excludedIdentifiers");
});
