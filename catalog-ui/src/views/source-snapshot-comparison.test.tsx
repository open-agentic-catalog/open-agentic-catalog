// @vitest-environment jsdom
// Isolated comparison controls substitute only transport, not the view or its response checks.
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import type { components } from "../api/schema";
import { APIError } from "../api/client";
import { SourceSnapshotComparison } from "./SourceSnapshotComparison";
import { SourceSnapshot, SourceSnapshots } from "./SourceSnapshots";
import { testAccess } from "../../tests/unit/access-fixtures";
import { FederationSnapshotComparison } from "./responses";
import { deferred, reply, workspace } from "../../tests/unit/fixtures";

type Schema = components["schemas"];
const beforeID = "00000000-0000-7000-8000-000000000101";
const afterID = "00000000-0000-7000-8000-000000000102";
const thirdID = "00000000-0000-7000-8000-000000000103";
const sourceID = "00000000-0000-7000-8000-000000000104";
const observationID = "00000000-0000-7000-8000-000000000105";
const at = "2026-09-01T12:00:00Z";
const digest = "sha256:" + "a".repeat(64);
const snapshot: Schema["FederationSnapshot"] = {
  id: beforeID,
  tenantId: sourceID,
  sourceId: sourceID,
  sourceRevision: 2,
  state: "complete",
  enumerationComplete: true,
  withdrawalAuthority: false,
  bodyDigest: digest,
  policyId: "unit",
  policyRevision: 1,
  policyDigest: digest,
  memberCount: 1,
  added: 0,
  updated: 0,
  withdrawn: 0,
  startedAt: at,
  completedAt: at,
};
const member = (
  snapshotId: string,
  identifier = "urn:air:peer.example:agent",
): Schema["FederationSnapshotComparisonMember"] => ({
  observationId: observationID,
  identifier,
  version: "",
  versionPresent: false,
  kind: "agent",
  identityDigest: digest,
  occurrence: {
    observationId: observationID,
    sourceId: sourceID,
    snapshotId,
    sourceRevision: 2,
    retrievalDigest: digest,
    entryLocation: "/entries/0",
    exactEntryDigest: digest,
    evidenceComplete: true,
    observedAt: at,
  },
});
const page = (
  change: "changed" | "unchanged" = "changed",
): Schema["FederationSnapshotComparisonPage"] => {
  const after = member(afterID);
  if (change === "changed") {
    after.observationId = "00000000-0000-7000-8000-000000000106";
    after.identityDigest = "sha256:" + "b".repeat(64);
    after.occurrence.observationId = after.observationId;
    after.occurrence.exactEntryDigest = after.identityDigest;
  }
  return {
    sourceId: sourceID,
    sourceRevision: 2,
    before: {
      id: beforeID,
      memberCount: 1,
      startedAt: at,
      withdrawalAuthority: false,
    },
    after: {
      id: afterID,
      memberCount: 1,
      startedAt: at,
      withdrawalAuthority: false,
    },
    items: [
      {
        position: 1,
        sourceKey: "key",
        change,
        beforeCount: 1,
        afterCount: 1,
        before: member(beforeID),
        after,
      },
    ],
  };
};
afterEach(() => {
  cleanup();
  history.replaceState(null, "", "/");
});
/** Keep selected-header reads separate from explicitly controlled comparison replies. */
function comparisonWorkspace() {
  const fixture = workspace();
  const comparison = vi.fn(async (..._args: unknown[]): Promise<unknown> => {
    throw new Error("Unexpected comparison read");
  });
  const snapshotRead = vi.fn(async (...args: unknown[]): Promise<unknown> => {
    const input = args[1] as { params: { path: { snapshotId: string } } };
    return reply({ ...snapshot, id: input.params.path.snapshotId });
  });
  fixture.get.mockImplementation(async (...args: unknown[]) => {
    if (args[0] === "/api/v1/federation/snapshots/{snapshotId}/comparison")
      return comparison(...args);
    if (args[0] === "/api/v1/federation/snapshots/{snapshotId}")
      return snapshotRead(...args);
    throw new Error("Unexpected source comparison transport read");
  });
  return { ...fixture, transport: fixture.get, get: comparison, snapshotRead };
}

function select(after = afterID) {
  fireEvent.change(screen.getByLabelText("After snapshot ID"), {
    target: { value: after },
  });
  fireEvent.submit(
    screen.getByRole("form", { name: "Select source snapshots" }),
  );
}

it("refuses an unselected before snapshot and recovers only through an exact reread", async () => {
  const { props, get } = workspace();
  const requested = "00000000-0000-7000-8000-00000000ABCD";
  const canonical = requested.toLowerCase();
  get
    .mockResolvedValueOnce(reply({ ...snapshot, id: thirdID }))
    .mockResolvedValue(reply({ ...snapshot, id: canonical }));
  render(
    <SourceSnapshot
      {...props}
      id={requested}
      access={{ ...props.access, ...testAccess(["source_admin"]) }}
    />,
  );
  expect((await screen.findByRole("alert")).textContent).toContain(
    "This page could not be loaded",
  );
  expect(screen.queryByText("Compare saved entries")).toBeNull();
  expect(screen.queryByRole("link", { name: "Source" })).toBeNull();
  expect(get).toHaveBeenCalledTimes(1);
  expect(props.onFailure).toHaveBeenCalledWith(
    new Error("Snapshot selection does not match."),
  );
  fireEvent.click(screen.getByRole("button", { name: "Reload snapshot" }));
  await screen.findByRole("heading", { name: "Compare saved entries" });
  expect(get).toHaveBeenCalledTimes(2);
  for (const [, options] of get.mock.calls)
    expect(options).toMatchObject({
      params: { path: { snapshotId: canonical } },
    });
});

it("clears a loaded comparison on permission loss and aborts its pending refresh", async () => {
  const { props, get, snapshotRead } = comparisonWorkspace();
  history.replaceState(
    null,
    "",
    `/unit/source-snapshots/${beforeID}?compare=${afterID}`,
  );
  get.mockResolvedValue(reply(page()));
  const access = { ...props.access, ...testAccess(["source_admin"]) };
  const { rerender } = render(
    <SourceSnapshot {...props} id={beforeID} access={access} />,
  );
  await screen.findByRole("heading", { name: /Changed:/ });
  const pending = deferred<ReturnType<typeof reply>>();
  get.mockReturnValueOnce(pending.promise);
  fireEvent.click(screen.getByRole("button", { name: "Reload comparison" }));
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  const signal = (get.mock.calls[1]![1] as { signal: AbortSignal }).signal;
  const headersBeforeRevocation = snapshotRead.mock.calls.length;
  expect(signal.aborted).toBe(false);
  rerender(<SourceSnapshot {...props} id={beforeID} />);
  expect(signal.aborted).toBe(true);
  expect(screen.getByRole("alert").textContent).toContain("source.read");
  expect(screen.queryByRole("heading", { name: /Changed:/ })).toBeNull();
  await act(async () => pending.resolve(reply(page())));
  expect(screen.queryByText("Compare saved entries")).toBeNull();
  expect(get).toHaveBeenCalledTimes(2);
  expect(snapshotRead).toHaveBeenCalledTimes(headersBeforeRevocation);
  expect(props.onFailure).not.toHaveBeenCalled();
});

it("reads only the explicit immutable pair and renders occurrence links without fetching raw evidence", async () => {
  const { props, get, transport, snapshotRead } = comparisonWorkspace();
  get
    .mockResolvedValueOnce(reply({ ...page("unchanged"), items: [] }))
    .mockResolvedValue(reply(page("unchanged")));
  render(<SourceSnapshotComparison {...props} snapshot={snapshot} />);
  expect(get).not.toHaveBeenCalled();
  select();
  await screen.findByText("No entry differences between these snapshots.");
  expect(get).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByLabelText("Include unchanged entries"));
  await screen.findByRole("heading", {
    name: "Unchanged: urn:air:peer.example:agent",
  });
  expect(get).toHaveBeenCalledTimes(2);
  expect(transport).toHaveBeenCalledTimes(4);
  expect(snapshotRead).toHaveBeenCalledTimes(2);
  expect(get.mock.calls[0]).toEqual([
    "/api/v1/federation/snapshots/{snapshotId}/comparison",
    expect.objectContaining({
      params: {
        path: { snapshotId: beforeID },
        query: { otherSnapshotId: afterID, view: "changes", limit: 25 },
      },
    }),
  ]);
  expect(get.mock.calls[1]![1]).toMatchObject({
    params: { query: { otherSnapshotId: afterID, view: "all", limit: 25 } },
  });
  expect(
    screen
      .getByRole("link", { name: "Inspect before observation and occurrences" })
      .getAttribute("href"),
  ).toBe(`/unit/observations/${observationID}`);
  expect(screen.getByText("Reused")).toBeTruthy();
  expect(
    screen.getByText(/Absence alone does not prove upstream deletion/),
  ).toBeTruthy();
  fireEvent.click(screen.getByText("Exact after occurrence"));
  expect(get).toHaveBeenCalledTimes(2);
  expect(screen.getAllByText("Not supplied")).toHaveLength(2);
});

it.each([
  ["processing inputs", "Unchanged", "Changed", "Changed"],
  ["raw formatting", "Changed", "Unchanged", "Unchanged"],
] as const)(
  "distinguishes changes to %s without conflating exact and normalized evidence",
  async (mode, exact, inputs, normalized) => {
    const { props, get } = comparisonWorkspace();
    const response = page();
    const before = response.items[0]!.before!;
    const after = response.items[0]!.after!;
    before.occurrence.normalizationInputDigest = digest;
    before.occurrence.normalizedEntryDigest = digest;
    after.occurrence.normalizationInputDigest = digest;
    after.occurrence.normalizedEntryDigest = digest;
    if (mode === "processing inputs") {
      after.occurrence.exactEntryDigest = before.occurrence.exactEntryDigest;
      after.occurrence.normalizationInputDigest = after.identityDigest;
      after.occurrence.normalizedEntryDigest = after.identityDigest;
    }
    get.mockResolvedValue(reply(response));
    render(<SourceSnapshotComparison {...props} snapshot={snapshot} />);
    select();
    await screen.findByRole("heading", { name: /Changed:/ });
    for (const [label, value] of [
      ["Exact entry", exact],
      ["Normalization inputs", inputs],
      ["Normalized entry", normalized],
      ["Observation", "Different retained observation"],
    ])
      expect(screen.getByText(label!).nextElementSibling?.textContent).toBe(
        value,
      );
    expect(get).toHaveBeenCalledTimes(1);
  },
);

it("keeps added unidentified entries and absent identified entries on their own sides", async () => {
  const { props, get } = comparisonWorkspace();
  const response = page();
  const added = response.items[0]!.after!;
  added.identifier = "";
  added.kind = "";
  added.versionPresent = true;
  added.version = "1.0.0";
  response.items = [
    {
      position: 1,
      sourceKey: "a-opaque-occurrence-key",
      change: "added",
      beforeCount: 0,
      afterCount: 1,
      after: added,
    },
    {
      position: 2,
      sourceKey: "b-identified-key",
      change: "absent",
      beforeCount: 1,
      afterCount: 0,
      before: member(beforeID),
    },
  ];
  get.mockResolvedValue(reply(response));
  render(<SourceSnapshotComparison {...props} snapshot={snapshot} />);
  select();
  const addedHeading = await screen.findByRole("heading", {
    name: "Added: a-opaque-occurrence-key",
  });
  const absentHeading = screen.getByRole("heading", {
    name: "Absent: urn:air:peer.example:agent",
  });
  const additions = within(addedHeading.closest("article")!);
  const absences = within(absentHeading.closest("article")!);
  expect(additions.getByText("Not available")).toBeTruthy();
  expect(additions.getByText("1.0.0")).toBeTruthy();
  expect(additions.queryByRole("link", { name: /Inspect before/ })).toBeNull();
  expect(absences.queryByRole("link", { name: /Inspect after/ })).toBeNull();
  expect(absences.getByText("Not supplied")).toBeTruthy();
  expect(screen.queryByText("Exact entry")).toBeNull();
});

it("distinguishes no differences from two complete empty memberships", async () => {
  const { props, get, snapshotRead } = comparisonWorkspace();
  const response = page();
  response.items = [];
  response.before.memberCount = response.after.memberCount = 0;
  snapshotRead.mockResolvedValue(
    reply({ ...snapshot, id: afterID, memberCount: 0 }),
  );
  get.mockResolvedValue(reply(response));
  render(
    <SourceSnapshotComparison
      {...props}
      snapshot={{ ...snapshot, memberCount: 0 }}
    />,
  );
  select();
  await screen.findByText("No entry differences between these snapshots.");
  fireEvent.click(screen.getByLabelText("Include unchanged entries"));
  await screen.findByText("Both selected memberships are empty.");
  expect(screen.queryByRole("heading", { level: 3 })).toBeNull();
  expect(get).toHaveBeenCalledTimes(2);
});

it("retains selections across source history pages and refuses different-revision choices locally", async () => {
  const { props, get } = workspace();
  get
    .mockResolvedValueOnce(reply({ items: [snapshot], nextCursor: "next" }))
    .mockResolvedValue(
      reply({ items: [{ ...snapshot, id: afterID, sourceRevision: 3 }] }),
    );
  render(<SourceSnapshots {...props} id={sourceID} />);
  fireEvent.click(
    await screen.findByRole("radio", { name: `Before ${beforeID}` }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  fireEvent.click(
    await screen.findByRole("radio", { name: `After ${afterID}` }),
  );
  expect(screen.getByRole("status").textContent).toContain(
    "same source revision",
  );
  expect(
    screen.queryByRole("link", { name: "Compare selected snapshots" }),
  ).toBeNull();
  expect(get).toHaveBeenCalledTimes(2);
});

it("links exact same-revision selections and prevents carrying a pair into another source", async () => {
  const { props, get } = workspace();
  get.mockResolvedValue(
    reply({ items: [snapshot, { ...snapshot, id: afterID }] }),
  );
  const { rerender } = render(<SourceSnapshots {...props} id={sourceID} />);
  fireEvent.click(
    await screen.findByRole("radio", { name: `Before ${beforeID}` }),
  );
  fireEvent.click(screen.getByRole("radio", { name: `After ${afterID}` }));
  expect(
    screen
      .getByRole("link", { name: "Compare selected snapshots" })
      .getAttribute("href"),
  ).toBe(
    `/unit/source-snapshots/${beforeID}?compare=${afterID}#snapshot-comparison`,
  );
  get.mockResolvedValue(
    reply({
      items: [
        { ...snapshot, id: thirdID, sourceId: thirdID },
        { ...snapshot, id: observationID, sourceId: thirdID },
      ],
    }),
  );
  rerender(<SourceSnapshots {...props} id={thirdID} />);
  fireEvent.click(
    await screen.findByRole("radio", { name: `Before ${thirdID}` }),
  );
  expect(
    screen.queryByRole("link", { name: "Compare selected snapshots" }),
  ).toBeNull();
  fireEvent.click(
    screen.getByRole("radio", { name: `After ${observationID}` }),
  );
  expect(
    screen
      .getByRole("link", { name: "Compare selected snapshots" })
      .getAttribute("href"),
  ).toBe(
    `/unit/source-snapshots/${thirdID}?compare=${observationID}#snapshot-comparison`,
  );
});

it("does not offer selection when a complete-state summary lacks enumeration completeness", async () => {
  const { props, get } = workspace();
  get.mockResolvedValue(
    reply({ items: [{ ...snapshot, enumerationComplete: false }] }),
  );
  render(<SourceSnapshots {...props} id={sourceID} />);
  await screen.findByRole("link", { name: /2026/ });
  expect(screen.queryByRole("radio")).toBeNull();
});

it("clears rejected pages and restarts the same pair explicitly without replaying the stale cursor", async () => {
  const { props, get } = comparisonWorkspace();
  get
    .mockResolvedValueOnce(reply({ ...page(), nextCursor: "next" }))
    .mockRejectedValueOnce(new Error("cursor refused"))
    .mockResolvedValue(reply({ ...page(), nextCursor: "next" }));
  render(<SourceSnapshotComparison {...props} snapshot={snapshot} />);
  select();
  await screen.findByRole("heading", { name: /Changed:/ });
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("alert");
  expect(screen.queryByRole("heading", { name: /Changed:/ })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Reload comparison" }));
  await screen.findByRole("heading", { name: /Changed:/ });
  expect(get.mock.calls[2]?.[1]).toMatchObject({
    params: { query: { otherSnapshotId: afterID, view: "changes", limit: 25 } },
  });
  expect(
    (get.mock.calls[2]?.[1] as { params: { query: object } }).params.query,
  ).not.toHaveProperty("cursor");
  fireEvent.click(screen.getByLabelText("Include unchanged entries"));
  await waitFor(() =>
    expect(get.mock.calls[3]?.[1]).toMatchObject({
      params: { query: { view: "all" } },
    }),
  );
});

it("fences a late result when another after snapshot is selected", async () => {
  const { props, get } = comparisonWorkspace();
  const pending = deferred<ReturnType<typeof reply>>();
  get
    .mockReturnValueOnce(pending.promise)
    .mockResolvedValue(
      reply({ ...page(), after: { ...page().after, id: thirdID }, items: [] }),
    );
  render(<SourceSnapshotComparison {...props} snapshot={snapshot} />);
  select();
  await waitFor(() => expect(get).toHaveBeenCalledTimes(1));
  const signal = (get.mock.calls[0]?.[1] as { signal: AbortSignal }).signal;
  select(thirdID);
  await screen.findByText("No entry differences between these snapshots.");
  await act(async () => {
    pending.resolve(reply(page()));
  });
  expect(signal.aborted).toBe(true);
  expect(screen.queryByRole("heading", { name: /Changed:/ })).toBeNull();
  expect(props.onFailure).not.toHaveBeenCalled();
});

it("rejects a valid-shaped response bound to a different selection without displaying its members", async () => {
  const { props, get } = comparisonWorkspace();
  get.mockResolvedValue(
    reply({ ...page(), before: { ...page().before, id: thirdID }, items: [] }),
  );
  render(<SourceSnapshotComparison {...props} snapshot={snapshot} />);
  select();
  await screen.findByRole("alert");
  expect(
    screen.queryByText("No entry differences between these snapshots."),
  ).toBeNull();
  expect(props.onFailure).toHaveBeenCalledTimes(1);
});

it.each(["incomplete", "failed", "running"] as const)(
  "does not request comparison of a %s attempt",
  (state) => {
    const { props, get } = workspace();
    history.replaceState(null, "", `/?compare=${afterID}`);
    render(
      <SourceSnapshotComparison
        {...props}
        snapshot={{ ...snapshot, state, enumerationComplete: false }}
      />,
    );
    expect(screen.getByText(/Failed or incomplete attempts/)).toBeTruthy();
    expect(get).not.toHaveBeenCalled();
  },
);

it("renders multiple occurrences without claiming a unique correspondence", async () => {
  const { props, get } = comparisonWorkspace();
  get.mockResolvedValue(
    reply({
      ...page(),
      items: [
        {
          position: 1,
          sourceKey: "duplicate",
          change: "multiple",
          beforeCount: 2,
          afterCount: 1,
          before: { ...member(beforeID), versionPresent: true },
        },
      ],
    }),
  );
  render(<SourceSnapshotComparison {...props} snapshot={snapshot} />);
  select();
  await screen.findByRole("heading", { name: /Multiple occurrences:/ });
  expect(
    screen.getByText(/no match or preferred entry is inferred/),
  ).toBeTruthy();
  expect(screen.getByText("(explicitly empty)")).toBeTruthy();
  expect(screen.queryByText("Reused")).toBeNull();
});

it.each([
  [
    { state: "failed", enumerationComplete: false, memberCount: 0 },
    "Comparable membership is unavailable",
  ],
  [
    { state: "incomplete", enumerationComplete: false },
    "Comparable membership is unavailable",
  ],
  [{ sourceRevision: 3 }, "different source revisions"],
  [{ sourceId: thirdID }, "different sources"],
] as const)(
  "explains the selected after snapshot before attempting an ineligible comparison: %j",
  async (patch, message) => {
    const { props, get, snapshotRead } = comparisonWorkspace();
    snapshotRead.mockResolvedValue(
      reply({ ...snapshot, id: afterID, ...patch }),
    );
    render(<SourceSnapshotComparison {...props} snapshot={snapshot} />);
    select();
    expect((await screen.findByRole("alert")).textContent).toContain(message);
    expect(screen.getByText("Before source revision")).toBeTruthy();
    expect(screen.getByText("After source revision")).toBeTruthy();
    expect(get).not.toHaveBeenCalled();
    expect(
      screen.queryByText("No entry differences between these snapshots."),
    ).toBeNull();
  },
);

it.each([
  [400, "same source and source revision"],
  [412, "Failed, incomplete or over-budget membership"],
  [503, "Saved membership is unavailable"],
] as const)(
  "uses a safe comparison-specific explanation for HTTP %s without losing the error classification",
  async (status, message) => {
    const { props, get } = comparisonWorkspace();
    const failure = new APIError(status, "COMPARISON_REFUSED");
    failure.message = "private diagnostic sentinel";
    get.mockRejectedValue(failure);
    render(<SourceSnapshotComparison {...props} snapshot={snapshot} />);
    select();
    expect((await screen.findByRole("alert")).textContent).toContain(message);
    expect(screen.queryByText(/private diagnostic sentinel/)).toBeNull();
    expect(props.onFailure).toHaveBeenCalledWith(failure);
    expect(failure.status).toBe(status);
    expect(failure.code).toBe("COMPARISON_REFUSED");
  },
);

it("accepts uppercase UUID selections using canonical returned identities", async () => {
  const { props, get } = comparisonWorkspace();
  const after = "00000000-0000-7000-8000-00000000abcd";
  get.mockResolvedValue(
    reply({ ...page(), after: { ...page().after, id: after }, items: [] }),
  );
  render(<SourceSnapshotComparison {...props} snapshot={snapshot} />);
  select(after.toUpperCase());
  await screen.findByText("No entry differences between these snapshots.");
  expect(get.mock.calls[0]?.[1]).toMatchObject({
    params: { query: { otherSnapshotId: after } },
  });
});

it("rejects a mismatched after snapshot header before requesting differences", async () => {
  const { props, get, snapshotRead } = comparisonWorkspace();
  snapshotRead.mockResolvedValue(reply({ ...snapshot, id: thirdID }));
  render(<SourceSnapshotComparison {...props} snapshot={snapshot} />);
  select();
  await screen.findByRole("alert");
  expect(get).not.toHaveBeenCalled();
});

it("accepts empty or complete compact pages and rejects malformed consumed evidence", () => {
  expect(FederationSnapshotComparison(page())).toBe(true);
  expect(FederationSnapshotComparison({ ...page(), items: [] })).toBe(true);
  for (const patch of [
    { sourceRevision: 0 },
    { before: null },
    { nextCursor: null },
    { items: [{}] },
    { items: [{ ...page().items[0], change: "withdrawn" }] },
    { items: [{ ...page().items[0], after: member(beforeID) }] },
    {
      items: [
        {
          ...page().items[0],
          after: {
            ...member(afterID),
            occurrence: {
              ...member(afterID).occurrence,
              evidenceComplete: false,
            },
          },
        },
      ],
    },
    { items: [{ ...page().items[0], change: "multiple" }] },
    { items: [page().items[0], page().items[0]] },
  ])
    expect(FederationSnapshotComparison({ ...page(), ...patch })).toBe(false);
});
