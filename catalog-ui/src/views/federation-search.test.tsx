import { deferred } from "../../tests/unit/fixtures";
import { editValues } from "../../tests/unit/screens";
// @vitest-environment jsdom
import { expect, it } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { FederationSearch } from "./FederationSearch";
import { APIError } from "../api/client";
import type { components } from "../api/schema";
import {
  administrator,
  confirmCommand,
  change,
  click,
  etagReply,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";

screenLifecycle("/unit/federation-search");

it("filters source choices before paging without changing selected IDs or sending discovery", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [] }));
  render(<FederationSearch {...w.props} access={administrator} />);
  change("Source IDs (optional)", "00000000-0000-7000-8000-000000000010");
  click("Choose configured sources");
  await screen.findByText(/No configured sources on this page/);
  change("Source name (exact match)", " Exact Name ");
  expect(w.get).toHaveBeenCalledOnce();
  click("Apply source choice filter");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { limit: 25, displayName: " Exact Name " } },
  });
  click("Clear source choice filter");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
  expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
    "params.query.displayName",
  );
  expect(screen.getByLabelText("Source IDs (optional)")).toHaveProperty(
    "value",
    "00000000-0000-7000-8000-000000000010",
  );
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["Refresh source choices", "Reload"])(
  "recovers source-picker continuation through %s without changing selected IDs or sending a query",
  async (recovery) => {
    const w = screenWorkspace();
    w.get
      .mockResolvedValueOnce(
        etagReply({ items: [], nextCursor: "source-next" }),
      )
      .mockRejectedValueOnce(new Error("cursor refused"))
      .mockResolvedValue(etagReply({ items: [] }));
    render(<FederationSearch {...w.props} access={administrator} />);
    const selected = "00000000-0000-7000-8000-000000000010";
    change("Source IDs (optional)", selected);
    change("Query", "Preserve unsubmitted text");
    click("Choose configured sources");
    fireEvent.click(await screen.findByRole("button", { name: "Next page" }));
    await screen.findByRole("alert");
    click(recovery);
    await screen.findByText(/No configured sources on this page/);
    expect(w.get.mock.calls[2]?.[1]).toMatchObject({
      params: { query: { limit: 25 } },
    });
    expect(screen.getByLabelText("Source IDs (optional)")).toHaveProperty(
      "value",
      selected,
    );
    expect(screen.getByLabelText("Query")).toHaveProperty(
      "value",
      "Preserve unsubmitted text",
    );
    expect(screen.queryByRole("button", { name: "First page" })).toBeNull();
    expect(w.get).toHaveBeenCalledTimes(3);
    expect(w.post).not.toHaveBeenCalled();
  },
);

const observation: components["schemas"]["Observation"] = {
  id: "observation-1",
  tenantId: administrator.tenantId,
  sourceId: "source-1",
  sourceKey: "helper",
  identifier: "urn:air:example.com:agents:helper",
  versionPresent: false,
  completeness: "complete",
  freshness: "current",
  rawEntryAvailable: true,
  exactEntryDigest: "entry-digest",
  identityDigest: "identity-digest",
  compatibility: {
    structural: "valid",
    profileId: "ard",
    support: "supported",
  },
  firstSeenAt: stamp,
  lastSeenAt: stamp,
};

function indexedResult(): unknown {
  return JSON.parse(
    screen
      .getByText("Indexed results")
      .closest("details")!
      .querySelector("pre")!.textContent!,
  );
}

it("keeps indexed pagination on the submitted filters and resets it only on a new search", async () => {
  const { props, post } = screenWorkspace();
  const first = {
    items: [
      {
        observation,
        score: 91,
        source: "00000000-0000-7000-8000-000000000001",
      },
    ],
    nextCursor: "next-observations",
  };
  const second = {
    items: [
      {
        observation: {
          ...observation,
          id: "observation-2",
          sourceKey: "other",
        },
        score: 82,
        source: "00000000-0000-7000-8000-000000000002",
      },
    ],
  };
  post
    .mockResolvedValueOnce(etagReply(first))
    .mockResolvedValueOnce(etagReply(second))
    .mockResolvedValueOnce(etagReply({ items: [] }));
  render(<FederationSearch {...props} access={administrator} />);
  change("Query", " saved search ");
  fireEvent.click(screen.getByRole("checkbox", { name: "Skills" }));
  click("Search indexed observations");
  await screen.findByText("Indexed results");
  expect(indexedResult()).toEqual(first);
  change("Query", "   ");
  fireEvent.click(screen.getByRole("checkbox", { name: "Skills" }));
  expect(post).toHaveBeenCalledTimes(1);
  click("Next page");
  await waitFor(() => expect(indexedResult()).toEqual(second));
  expect(post.mock.calls[1]).toEqual([
    "/api/v1/observations:search",
    {
      body: {
        text: "saved search",
        tags: [],
        kinds: ["skill"],
        limit: 25,
        cursor: "next-observations",
      },
      signal: expect.any(AbortSignal) as unknown,
    },
  ]);
  click("Search indexed observations");
  await waitFor(() => expect(indexedResult()).toEqual({ items: [] }));
  expect(post.mock.calls[2]).toEqual([
    "/api/v1/observations:search",
    {
      body: {
        text: undefined,
        tags: [],
        kinds: undefined,
        limit: 25,
        cursor: undefined,
      },
      signal: expect.any(AbortSignal) as unknown,
    },
  ]);
  expect(screen.queryByRole("button", { name: "First page" })).toBeNull();
  expect(props.onFailure).not.toHaveBeenCalled();
});

it("rejects live-query overrides, then sends only allowed advanced fields after correction and confirmation", async () => {
  const { props, post } = screenWorkspace();
  const complete: components["schemas"]["LiveSearchResponse"] = {
    results: [],
    partial: false,
    diagnostics: [],
  };
  post.mockResolvedValue(etagReply(complete));
  render(<FederationSearch {...props} access={administrator} />);
  change("Query", " chosen query ");
  editValues("Observation tags", ["indexed-only"]);
  fireEvent.click(screen.getByRole("checkbox", { name: "Agents" }));
  expect(
    screen.getByText(/Kind and tag filters apply only to indexed search/),
  ).toBeTruthy();
  change("Reason", " Review configured sources ");
  const advanced = "Advanced live options (JSON)";
  change(advanced, '{"sourceIds":["unselected-source"]}');
  click("Query live sources");
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Live options accept filter, @context and pageSize only.",
  );
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(post).not.toHaveBeenCalled();
  const options = {
    filter: { type: "https://example.com/Agent" },
    "@context": { type: "@type" },
    pageSize: 5,
  };
  change(advanced, JSON.stringify(options));
  click("Query live sources");
  const confirmation = await screen.findByRole("dialog");
  expect(
    within(confirmation).getByText(/Send this query to approved sources/),
  ).toBeTruthy();
  expect(post).not.toHaveBeenCalled();
  click("Confirm");
  await screen.findByText("Live diagnostic complete.");
  expect(post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/federation/sources:live-search",
    {
      params: { header: { "Idempotency-Key": expect.any(String) as unknown } },
      body: {
        ...options,
        text: "chosen query",
        reason: "Review configured sources",
        sourceIds: undefined,
      },
    },
  );
  expect(
    screen.queryByText(/Some source queries or results are incomplete/),
  ).toBeNull();
  expect(screen.queryByRole("alert")).toBeNull();
  expect(props.onFailure).not.toHaveBeenCalled();
});

it("recovers an indexed cursor failure without replaying a live query or losing the selected page", async () => {
  const { props, post } = screenWorkspace();
  const error = new APIError(503, "UNAVAILABLE");
  post
    .mockResolvedValueOnce(
      etagReply({
        items: [
          {
            observation,
            score: 91,
            source: "00000000-0000-7000-8000-000000000001",
          },
        ],
        nextCursor: "next-observations",
      }),
    )
    .mockRejectedValueOnce(error)
    .mockResolvedValueOnce(etagReply({ items: [] }));
  render(<FederationSearch {...props} access={administrator} />);
  change("Query", "helper");
  click("Search indexed observations");
  await screen.findByText("Indexed results");
  click("Next page");
  expect((await screen.findByRole("alert")).textContent).toContain(
    error.message,
  );
  expect(props.onFailure).toHaveBeenCalledExactlyOnceWith(error);
  click("Reload");
  await waitFor(() => expect(indexedResult()).toEqual({ items: [] }));
  expect(screen.queryByRole("alert")).toBeNull();
  expect(post.mock.calls.map(([path]) => path)).toEqual(
    Array(3).fill("/api/v1/observations:search"),
  );
  expect(post.mock.calls[2]?.[1]).toMatchObject({
    body: { text: "helper" },
  });
  expect(post.mock.calls[2]?.[1]).not.toHaveProperty("body.cursor");
});

it("shows useful bounded live evidence and its continuation warning without following another page", async () => {
  const { props, post } = screenWorkspace();
  const partial: components["schemas"]["LiveSearchResponse"] = {
    results: [
      {
        identifier: "urn:air:peer.example:agents:helper",
        sourceRevision: 1,
        exactEntryDigest: `sha256:${"a".repeat(64)}`,
        versionPresent: false,
        score: 91,
        sourceId: "00000000-0000-7000-8000-000000000001",
        source: "https://peer.example/ard/",
        entry: { displayName: "Peer helper" },
      },
    ],
    partial: true,
    diagnostics: [
      {
        sourceId: "00000000-0000-7000-8000-000000000001",
        status: "continuation_unavailable",
        message:
          "The upstream Registry has more results; this bounded live query does not continue them.",
      },
    ],
  };
  post.mockResolvedValue(etagReply(partial));
  render(<FederationSearch {...props} access={administrator} />);
  change("Query", "peer helper");
  change("Reason", "Review configured sources");
  click("Query live sources");
  await screen.findByRole("dialog");
  click("Confirm");
  await screen.findByText(/Some source queries or results are incomplete/);
  const evidence = screen
    .getByText("Live results and diagnostics")
    .closest("details")!;
  expect(JSON.parse(evidence.querySelector("pre")!.textContent!)).toEqual(
    partial,
  );
  expect(screen.queryByText("Live diagnostic complete.")).toBeNull();
  const rows = within(
    screen.getByRole("region", { name: "Live diagnostic results" }),
  );
  expect(
    rows.getByRole("heading", { name: partial.results[0]!.identifier }),
  ).toBeTruthy();
  expect(rows.getByText("No version supplied")).toBeTruthy();
  expect(
    rows.getByText(
      "Not supplied in this live projection; inspect the raw entry.",
    ),
  ).toBeTruthy();
  expect(
    rows.getByText(/The remaining upstream page was not followed/),
  ).toBeTruthy();
  expect(rows.queryByRole("link", { name: /record|adopt/i })).toBeNull();
  expect(screen.queryByRole("alert")).toBeNull();
  expect(props.onFailure).not.toHaveBeenCalled();
  expect(post).toHaveBeenCalledOnce();
});

const sourceID = "00000000-0000-7000-8000-000000000001";
const configuredSource = {
  id: sourceID,
  displayName: "Configured peer",
  endpoint: "https://peer.example/ard/",
  status: "active",
  profileId: "ard",
  retrievalMode: "hybrid",
  updateMode: "automatic",
  scope: {},
  live: { enabled: true },
  referrals: {},
  nesting: {},
  revision: 1,
};

it("renders exact indexed identities, optional native kinds and separate compatibility without extra reads", async () => {
  const { props, post, get } = screenWorkspace();
  const hostile = '<img src=x onerror="alert(1)">';
  post.mockResolvedValue(
    etagReply({
      items: [
        {
          observation: {
            ...observation,
            sourceKey: hostile,
            identifier: undefined,
            kind: "plugin",
            versionPresent: true,
            version: "",
            compatibility: {
              structural: "invalid",
              support: "unknown",
              profileId: "future",
            },
          },
          score: 0,
          source: sourceID,
        },
        {
          observation: {
            ...observation,
            id: "observation-2",
            compatibility: { ...observation.compatibility, profileId: "" },
          },
          score: 100,
          source: sourceID,
        },
      ],
    }),
  );
  const { container } = render(
    <FederationSearch {...props} access={administrator} />,
  );
  click("Search indexed observations");
  const rows = within(
    await screen.findByRole("region", { name: "Indexed diagnostic results" }),
  );
  expect(rows.getByRole("link", { name: hostile }).getAttribute("href")).toBe(
    "/unit/observations/observation-1",
  );
  expect(rows.getByText("Plugin")).toBeTruthy();
  expect(rows.getByText("Version: (empty)")).toBeTruthy();
  expect(rows.getByText("Native kind not supplied")).toBeTruthy();
  expect(rows.getByText("No version supplied")).toBeTruthy();
  expect(rows.getByText("Invalid").getAttribute("data-tone")).toBe("danger");
  expect(rows.getByText("Unknown").getAttribute("data-tone")).toBe("neutral");
  expect(rows.getByText("Not supplied")).toBeTruthy();
  expect(
    rows
      .getAllByRole("link", { name: sourceID })
      .map((a) => a.getAttribute("href")),
  ).toEqual([`/unit/sources/${sourceID}`, `/unit/sources/${sourceID}`]);
  expect(container.querySelector("img")).toBeNull();
  expect(get).not.toHaveBeenCalled();
  expect(post).toHaveBeenCalledOnce();
});

it("opens one source page explicitly, preserves cross-page and manual selections, and rechecks no eligibility in the browser", async () => {
  const { props, get, post } = screenWorkspace();
  const otherID = "00000000-0000-7000-8000-000000000002";
  get
    .mockResolvedValueOnce(
      etagReply({ items: [configuredSource], nextCursor: "next-sources" }),
    )
    .mockResolvedValue(
      etagReply({
        items: [
          { ...configuredSource, status: "paused", live: { enabled: false } },
          { ...configuredSource, id: otherID, displayName: "Second peer" },
        ],
      }),
    );
  post.mockResolvedValue(
    etagReply({
      results: [],
      partial: true,
      diagnostics: [{ sourceId: sourceID, status: "policy_stale" }],
    }),
  );
  render(<FederationSearch {...props} access={administrator} />);
  expect(get).not.toHaveBeenCalled();
  click("Choose configured sources");
  const first = await screen.findByRole("checkbox", {
    name: /Configured peer/,
  });
  fireEvent.click(first);
  click("Next page");
  const second = await screen.findByRole("checkbox", { name: /Second peer/ });
  expect(
    (
      screen.getByRole("checkbox", {
        name: /Configured peer.*paused.*disabled/,
      }) as HTMLInputElement
    ).checked,
  ).toBe(true);
  fireEvent.click(second);
  expect(
    (
      screen.getByRole("textbox", {
        name: "Source IDs (optional)",
      }) as HTMLInputElement
    ).value,
  ).toBe(`${sourceID}, ${otherID}`);
  expect(get.mock.calls[1]?.[1]).toMatchObject({
    params: { query: { limit: 25, cursor: "next-sources" } },
  });
  expect(get).toHaveBeenCalledTimes(2);
  expect(post).not.toHaveBeenCalled();
  fireEvent.click(second);
  click("Close source picker");
  change("Source IDs (optional)", `${sourceID},${sourceID},${otherID}`);
  change("Query", "retained query");
  change("Reason", "Investigate changed eligibility");
  click("Query live sources");
  await screen.findByRole("dialog");
  expect(post).not.toHaveBeenCalled();
  click("Confirm");
  await screen.findByText(/Some source queries or results are incomplete/);
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: { sourceIds: [sourceID, otherID] },
  });
  expect(
    within(
      screen.getByRole("region", { name: "Live diagnostic results" }),
    ).getByText(/policy_stale/),
  ).toBeTruthy();
  expect(get).toHaveBeenCalledTimes(2);
});

it("limits the picker to eight unique IDs while keeping failed and empty picker reads recoverable without changing a live command", async () => {
  const { props, get, post } = screenWorkspace();
  const ids = Array.from(
    { length: 9 },
    (_, i) => `00000000-0000-7000-8000-${String(i + 1).padStart(12, "0")}`,
  );
  get
    .mockRejectedValueOnce(new APIError(503, "UNAVAILABLE"))
    .mockResolvedValueOnce(etagReply({ items: [] }))
    .mockResolvedValue(
      etagReply({ items: [{ ...configuredSource, id: ids[8] }] }),
    );
  post.mockResolvedValue(etagReply({ results: [], partial: false }));
  render(<FederationSearch {...props} access={administrator} />);
  change("Source IDs (optional)", ids.slice(0, 8).join(","));
  click("Choose configured sources");
  await screen.findByRole("alert");
  expect(post).not.toHaveBeenCalled();
  click("Reload");
  await screen.findByText(/No configured sources on this page/);
  click("Refresh source choices");
  const box = await within(
    screen.getByRole("region", { name: "Configured source picker" }),
  ).findByRole("checkbox");
  expect((box as HTMLInputElement).disabled).toBe(true);
  change("Source IDs (optional)", ids.slice(0, 7).join(","));
  expect((box as HTMLInputElement).disabled).toBe(false);
  fireEvent.click(box);
  change("Query", "query");
  change("Reason", "Bounded manual IDs");
  change("Source IDs (optional)", ids.join(","));
  click("Query live sources");
  expect((await screen.findByRole("alert")).textContent).toContain(
    "at most eight",
  );
  expect(screen.queryByRole("dialog")).toBeNull();
  change("Source IDs (optional)", ids.slice(0, 8).join(","));
  click("Query live sources");
  await screen.findByRole("dialog");
  click("Confirm");
  await screen.findByText("Live diagnostic complete.");
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: { sourceIds: ids.slice(0, 8) },
  });
  expect(props.onFailure).toHaveBeenCalledOnce();
  expect(get).toHaveBeenCalledTimes(3);
});

it("keeps the exact live body and key after a malformed successful response despite changed query inputs", async () => {
  const { props, post } = screenWorkspace();
  post
    .mockResolvedValueOnce(etagReply({ results: [{}], partial: false }))
    .mockResolvedValue(etagReply({ results: [], partial: false }));
  render(<FederationSearch {...props} access={administrator} />);
  change("Query", "original query");
  change("Reason", "Original reason");
  change("Source IDs (optional)", sourceID);
  click("Query live sources");
  await screen.findByRole("dialog");
  change("Query", "changed outside confirmation");
  click("Confirm");
  await screen.findByRole("alert");
  expect(
    screen.queryByRole("region", { name: "Live diagnostic results" }),
  ).toBeNull();
  expect(post).toHaveBeenCalledOnce();
  click("Retry same command");
  await screen.findByText("Live diagnostic complete.");
  expect(post.mock.calls[1]).toEqual(post.mock.calls[0]);
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      text: "original query",
      reason: "Original reason",
      sourceIds: [sourceID],
    },
  });
  expect(props.onFailure).toHaveBeenCalledOnce();
});

it("keeps a prepared live command intact when an outstanding source-picker read fails", async () => {
  const { props, get, post } = screenWorkspace();
  let failRead: (error: Error) => void = () => {
    throw new Error("Read not started");
  };
  get.mockImplementation(
    () =>
      new Promise((_, reject) => {
        failRead = reject;
      }),
  );
  post.mockResolvedValue(etagReply({ results: [], partial: false }));
  render(<FederationSearch {...props} access={administrator} />);
  change("Source IDs (optional)", sourceID);
  change("Query", "prepared query");
  change("Reason", "Prepared reason");
  click("Choose configured sources");
  await waitFor(() => expect(get).toHaveBeenCalledOnce());
  click("Query live sources");
  await screen.findByRole("dialog");
  await act(async () => failRead(new APIError(503, "UNAVAILABLE")));
  expect(
    within(
      screen.getByRole("region", { name: "Configured source picker" }),
    ).getByRole("alert"),
  ).toBeTruthy();
  expect(post).not.toHaveBeenCalled();
  click("Confirm");
  await screen.findByText("Live diagnostic complete.");
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      text: "prepared query",
      reason: "Prepared reason",
      sourceIds: [sourceID],
    },
  });
  expect(get).toHaveBeenCalledOnce();
  expect(props.onFailure).toHaveBeenCalledOnce();
});

it("shows sparse live types and unattributed diagnostics without inventing native support or following URLs", async () => {
  const { props, post, get } = screenWorkspace();
  const hostile = '<img src=x onerror="alert(1)">';
  const liveRow = {
    identifier: "urn:air:peer.example:tools:helper",
    sourceRevision: 1,
    exactEntryDigest: `sha256:${"a".repeat(64)}`,
    versionPresent: false,
    score: 91,
    sourceId: sourceID,
    source: "https://peer.example/ard/",
    entry: { type: hostile },
  };
  post.mockResolvedValue(
    etagReply({
      results: [
        liveRow,
        {
          ...liveRow,
          identifier: "urn:air:peer.example:tools:empty",
          versionPresent: true,
          entry: { type: { future: true } },
        },
      ],
      partial: false,
      diagnostics: [{ status: "succeeded" }],
    }),
  );
  const { container } = render(
    <FederationSearch {...props} access={administrator} />,
  );
  change("Query", "query");
  change("Reason", "Inspect sparse declarations");
  click("Query live sources");
  await screen.findByRole("dialog");
  click("Confirm");
  await screen.findByText("Live diagnostic complete.");
  const rows = within(
    screen.getByRole("region", { name: "Live diagnostic results" }),
  );
  expect(rows.getByText(hostile)).toBeTruthy();
  expect(
    rows.getByText("Not supplied as text; inspect the raw entry."),
  ).toBeTruthy();
  expect(rows.getByText("Version: (empty)")).toBeTruthy();
  expect(rows.getByText(/No source attribution/)).toBeTruthy();
  expect(rows.getByText(/This bounded source query succeeded/)).toBeTruthy();
  expect(container.querySelector("img")).toBeNull();
  expect(
    rows
      .getAllByRole("link")
      .every((a) => a.getAttribute("href") === `/unit/sources/${sourceID}`),
  ).toBe(true);
  expect(post).toHaveBeenCalledOnce();
  expect(get).not.toHaveBeenCalled();
});

it.each(["permission", "scope"])(
  "makes no diagnostic requests without the %s ceiling",
  async (missing) => {
    const w = screenWorkspace();
    render(
      <FederationSearch
        {...w.props}
        access={{
          ...administrator,
          permissions:
            missing === "permission"
              ? ["source.read"]
              : ["source.read", "federation.discover"],
          availablePermissions: ["source.read", "federation.discover"],
          scopeEnforcement: true,
          scopes: missing === "scope" ? ["catalog.read"] : ["sources.read"],
        }}
      />,
    );
    expect(screen.getByRole("alert").textContent).toContain(
      "federation.discover",
    );
    expect(
      screen.queryByRole("button", { name: "Search indexed observations" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Query live sources" }),
    ).toBeNull();
    expect(w.get).not.toHaveBeenCalled();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("reruns identical indexed criteria from page one with exact comma tags and multiple kinds, ignoring an old page reply", async () => {
  const w = screenWorkspace();
  const pending = deferred<unknown>();
  w.post
    .mockResolvedValueOnce(etagReply({ items: [], nextCursor: "second-page" }))
    .mockImplementationOnce(() => pending.promise)
    .mockResolvedValue(
      etagReply({ items: [{ observation, source: sourceID, score: 80 }] }),
    );
  render(
    <FederationSearch
      {...w.props}
      access={{
        ...administrator,
        permissions: ["source.read", "federation.discover"],
        availablePermissions: ["source.read", "federation.discover"],
        scopeEnforcement: true,
        scopes: ["sources.read"],
      }}
    />,
  );
  change("Query", " retained ");
  editValues("Observation tags", ["design, review", "工具"]);
  fireEvent.click(screen.getByRole("checkbox", { name: "Agents" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Plugins" }));
  click("Search indexed observations");
  await screen.findByRole("button", { name: "Next page" });
  click("Next page");
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(2));
  const old = w.post.mock.calls[1]?.[1] as { signal: AbortSignal };
  click("Search indexed observations");
  await screen.findByRole("link", { name: observation.identifier! });
  const body = {
    text: "retained",
    tags: ["design, review", "工具"],
    kinds: ["agent", "plugin"],
    limit: 25,
  };
  expect(w.post.mock.calls[0]?.[1]).toMatchObject({ body });
  expect(w.post.mock.calls[1]?.[1]).toMatchObject({
    body: { ...body, cursor: "second-page" },
  });
  expect(w.post.mock.calls[2]?.[1]).toMatchObject({ body });
  expect(w.post.mock.calls[2]?.[1]).not.toHaveProperty("body.cursor");
  expect(old.signal.aborted).toBe(true);
  await act(async () => pending.resolve(etagReply({ items: [] })));
  expect(
    screen.getByRole("link", { name: observation.identifier! }),
  ).toBeTruthy();
  click("Search indexed observations");
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(4));
  expect(w.post.mock.calls[3]?.[1]).toMatchObject({ body });
});

it.each([
  ["text", "🙂".repeat(126), "500 UTF-8 bytes"],
  ["tag", "🙂".repeat(81), "80 characters"],
] as const)(
  "refuses an oversized indexed %s without sending a request and permits correction",
  async (field, value, message) => {
    const { props, post } = screenWorkspace();
    post.mockResolvedValue(etagReply({ items: [] }));
    render(<FederationSearch {...props} access={administrator} />);
    const label = field === "text" ? "Query" : "Observation tag 1";
    if (field === "tag") editValues("Observation tags", [value]);
    else change(label, value);
    click("Search indexed observations");
    expect(screen.getByRole("alert").textContent).toContain(message);
    expect(screen.getByRole("textbox", { name: label })).toBe(
      document.activeElement,
    );
    expect(post).not.toHaveBeenCalled();
    change(label, "design, review");
    click("Search indexed observations");
    await screen.findByText("Indexed results");
    expect(post).toHaveBeenCalledExactlyOnceWith(
      "/api/v1/observations:search",
      {
        body: {
          ...(field === "text" ? { text: "design, review" } : {}),
          tags: field === "tag" ? ["design, review"] : [],
          limit: 25,
        },
        signal: expect.any(AbortSignal) as unknown,
      },
    );
    expect(screen.queryByRole("alert")).toBeNull();
  },
);

it("uses code points for live text while preserving the indexed byte limit", async () => {
  const { props, post } = screenWorkspace();
  render(<FederationSearch {...props} access={administrator} />);
  const text = screen.getByLabelText("Query");
  expect(text.getAttribute("maxlength")).toBe("1000");
  change("Query", "🙂".repeat(500) + "a");
  change("Reason", "Explicit query disclosure");
  click("Query live sources");
  expect(screen.getByRole("alert").textContent).toContain("500 characters");
  expect(screen.queryByRole("dialog")).toBeNull();
  change("Query", "🙂".repeat(500));
  click("Query live sources");
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(post).not.toHaveBeenCalled();
  click("Cancel");
  click("Search indexed observations");
  expect(screen.getByRole("alert").textContent).toContain("500 UTF-8 bytes");
  expect(post).not.toHaveBeenCalled();
});

it.each([
  ["failed", "danger"],
  ["invalid_response", "danger"],
  ["continuation_unavailable", "warning"],
] as const)(
  "keeps the %s live diagnostic separate from accepted results",
  async (status, tone) => {
    const { props, post, get } = screenWorkspace();
    post.mockResolvedValue(
      etagReply({
        results: [],
        partial: true,
        diagnostics: [{ sourceId: sourceID, status }],
      }),
    );
    const view = render(<FederationSearch {...props} access={administrator} />);
    change("Query", "Inspect partial results");
    change("Reason", "Investigate this source outcome");
    await confirmCommand("Query live sources");
    await screen.findByRole("heading", { name: "Transient live results" });
    expect(
      view.container
        .querySelector(`[data-state="${status}"]`)
        ?.getAttribute("data-tone"),
    ).toBe(tone);
    expect(
      screen.getByText(
        "Some source queries or results are incomplete. Available results are shown.",
      ),
    ).toBeTruthy();
    expect(post).toHaveBeenCalledOnce();
    expect(get).not.toHaveBeenCalled();
  },
);
