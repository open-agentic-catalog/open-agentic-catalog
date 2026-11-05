// @vitest-environment jsdom
import { render, screen, within } from "@testing-library/react";
import { expect, it } from "vitest";
import Reviews from "./Reviews";
import Jobs from "./Jobs";
import { MyWork, PersonalLibrary } from "./Personal";
import { submission, job } from "../../tests/unit/responses";
import {
  administrator,
  change,
  click,
  etagReply,
  readReplies,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";

screenLifecycle("/unit");
const rows = (name: string) =>
  within(screen.getByRole("region", { name })).getAllByRole("row").slice(1);
const cellLabels = (name: string, column = 0) =>
  rows(name).map((row) =>
    within(row).getAllByRole("cell")[column]!.textContent?.trim(),
  );
function checkColumn(
  region: string,
  column: string,
  expected: string[],
  descending = [...expected].reverse(),
  cell = 0,
) {
  click(`Sort by ${column.toLowerCase()}, ascending`);
  expect(cellLabels(region, cell)).toEqual(expected);
  expect(
    within(screen.getByRole("region", { name: region }))
      .getByRole("columnheader", { name: column })
      .getAttribute("aria-sort"),
  ).toBe("ascending");
  click(`Sort by ${column.toLowerCase()}, descending`);
  expect(cellLabels(region, cell)).toEqual(descending);
}

it("sorts review labels, missing context and actual submission times without applying filter drafts", async () => {
  const w = screenWorkspace();
  const items = Object.freeze([
    {
      ...submission,
      id: "s10",
      submittedDisplayName: "Tool 10",
      canonicalIdentifier: "urn:tool:10",
      submittedBy: "author10",
      submittedAt: "2026-10-01T00:00:00Z",
    },
    {
      ...submission,
      id: "s2",
      submittedDisplayName: "Tool 2",
      canonicalIdentifier: "urn:tool:2",
      state: "approved",
      submittedBy: "author2",
      submittedAt: "2026-09-30T23:30:00-02:00",
    },
    {
      ...submission,
      id: "s1",
      state: "changes_required",
      submittedBy: "author1",
      submittedAt: "2026-09-30T23:00:00Z",
    },
  ]);
  readReplies(w.get, {
    "/api/v1/review-submissions": { items, nextCursor: "review-next" },
  });
  render(<Reviews {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: "s10" });
  change("Exact Listing ID", "00000000-0000-7000-8000-000000000005");
  expect(cellLabels("Review submissions")).toEqual(["s10", "s2", "s1"]);
  for (const [column, order] of [
    ["Submitted listing", ["s1", "s2", "s10"]],
    ["Resource identifier", ["s1", "s2", "s10"]],
    ["State", ["s2", "s1", "s10"]],
    ["Submitted by", ["s1", "s2", "s10"]],
    ["Submitted", ["s1", "s10", "s2"]],
  ] as const)
    checkColumn("Review submissions", column, [...order]);
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.post).not.toHaveBeenCalled();
  expect(screen.getByLabelText("Exact Listing ID")).toHaveProperty(
    "value",
    "00000000-0000-7000-8000-000000000005",
  );
  w.get.mockResolvedValueOnce(
    etagReply({ items: [{ ...submission, id: "replacement" }] }),
  );
  click("Next page");
  await screen.findByRole("link", { name: "replacement" });
  expect(cellLabels("Review submissions")).toEqual(["replacement"]);
  expect(w.get).toHaveBeenLastCalledWith(
    "/api/v1/review-submissions",
    expect.objectContaining({
      params: {
        query: { state: "submitted", limit: 25, cursor: "review-next" },
      },
    }),
  );
  expect(
    screen
      .getByRole("columnheader", { name: "Submitted" })
      .getAttribute("aria-sort"),
  ).toBe("descending");
  expect(
    screen.getByRole("link", { name: "replacement" }).getAttribute("href"),
  ).toBe("/unit/reviews/replacement");
  expect(items.map((item) => item.id)).toEqual(["s10", "s2", "s1"]);
});

it("sorts Job types, states, progress, attempts and timestamps without changing the manual refresh or targets", async () => {
  const w = screenWorkspace();
  const items = [
    {
      ...job,
      id: "j10",
      kind: "source.sync",
      progress: 100,
      attempt: 10,
      attemptLimit: 12,
      state: "queued",
      updatedAt: "2026-10-01T00:00:00Z",
    },
    {
      ...job,
      id: "j2",
      kind: "semantic.reindex",
      progress: 20,
      attempt: 2,
      state: "failed",
      updatedAt: "2026-09-30T23:30:00-02:00",
    },
    {
      ...job,
      id: "j1",
      kind: "publication.generate",
      progress: 3,
      attempt: 1,
      state: "running",
      updatedAt: "2026-09-30T23:00:00Z",
    },
  ];
  readReplies(w.get, { "/api/v1/jobs": { items } });
  render(<Jobs {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: "Source synchronization" });
  expect(screen.getByText(/Newest first\. Refresh is manual/)).toBeTruthy();
  change("Job state", "cancelled");
  for (const [column, order] of [
    [
      "Job type / ID",
      [
        "Discovery snapshot generationj1",
        "Search indexingj2",
        "Source synchronizationj10",
      ],
    ],
    [
      "State",
      [
        "Search indexingj2",
        "Source synchronizationj10",
        "Discovery snapshot generationj1",
      ],
    ],
    [
      "Progress",
      [
        "Discovery snapshot generationj1",
        "Search indexingj2",
        "Source synchronizationj10",
      ],
    ],
    [
      "Attempts",
      [
        "Discovery snapshot generationj1",
        "Search indexingj2",
        "Source synchronizationj10",
      ],
    ],
    [
      "Updated",
      [
        "Discovery snapshot generationj1",
        "Source synchronizationj10",
        "Search indexingj2",
      ],
    ],
  ] as const)
    checkColumn("Job inventory", column, [...order]);
  expect(screen.queryByText(/Newest first/)).toBeNull();
  expect(
    screen.getByText(/Sorted on this page\. Refresh is manual/),
  ).toBeTruthy();
  expect(screen.getByText("10 / 12")).toBeTruthy();
  expect(screen.getByLabelText("Job state")).toHaveProperty(
    "value",
    "cancelled",
  );
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.post).not.toHaveBeenCalled();
  click("Refresh");
  await screen.findByRole("button", { name: "Refresh" });
  expect(w.get).toHaveBeenLastCalledWith(
    "/api/v1/jobs",
    expect.objectContaining({ params: { query: { limit: 25 } } }),
  );
  expect(
    screen
      .getByRole("link", { name: "Source synchronization" })
      .getAttribute("href"),
  ).toBe("/unit/jobs/j10");
});

it("sorts My work by displayed fallback names, identifiers and states while keeping exact Continue links", async () => {
  const w = screenWorkspace();
  const base = {
    type: "resource",
    state: "empty_resource",
    resourceId: "r",
    namespaceId: "ns-1",
    kind: "agent",
  };
  const items = [
    {
      ...base,
      id: "w10",
      resourceId: "r10",
      displayName: "Tool 10",
      canonicalIdentifier: "urn:tool:10",
    },
    {
      ...base,
      id: "w2",
      resourceId: "r2",
      displayName: "Tool 2",
      canonicalIdentifier: "urn:tool:2",
      state: "unsealed_version",
    },
    { ...base, id: "w1", resourceId: "r1", state: "unsubmitted_candidate" },
  ];
  readReplies(w.get, { "/api/v1/me/work": { items, nextCursor: "work-next" } });
  render(<MyWork {...w.props} access={administrator} />);
  await screen.findByText("Tool 10");
  checkColumn("My work results", "Work", [
    "Resource without versions",
    "Tool 2",
    "Tool 10",
  ]);
  checkColumn("My work results", "Resource", [
    "Resource without versions",
    "Tool 2",
    "Tool 10",
  ]);
  checkColumn("My work results", "State", [
    "Tool 10",
    "Resource without versions",
    "Tool 2",
  ]);
  const target = rows("My work results").find((row) =>
    row.textContent?.includes("Tool 2"),
  )!;
  expect(
    within(target)
      .getByRole("link", { name: "Open resource" })
      .getAttribute("href"),
  ).toBe("/unit/resources/r2");
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.post).not.toHaveBeenCalled();
  w.get.mockResolvedValueOnce(
    etagReply({
      items: [{ ...base, id: "new-work", displayName: "Replacement work" }],
    }),
  );
  click("Next page");
  await screen.findByText("Replacement work");
  expect(cellLabels("My work results")).toEqual(["Replacement work"]);
  expect(w.get).toHaveBeenLastCalledWith(
    "/api/v1/me/work",
    expect.objectContaining({
      params: { query: { queue: "drafts", limit: 25, cursor: "work-next" } },
    }),
  );
  expect(
    screen
      .getByRole("columnheader", { name: "State" })
      .getAttribute("aria-sort"),
  ).toBe("descending");
});

it("sorts Saved items with stable labels, displayed types and dates without applying pending filters or fetching targets", async () => {
  const w = screenWorkspace();
  const base = { type: "search", search: {}, revision: 1, createdAt: stamp };
  const items = [
    { ...base, id: "p10", label: "Tool 10", updatedAt: "2026-10-01T00:00:00Z" },
    {
      id: "p2",
      label: "Tool 2",
      type: "resource",
      reference: {
        origin: "local",
        releaseId: "release-2",
        versionRevisionId: "version-2",
      },
      revision: 1,
      createdAt: stamp,
      updatedAt: "2026-09-30T23:30:00-02:00",
    },
    { ...base, id: "p1", label: "Tool 2", updatedAt: "2026-09-30T23:00:00Z" },
  ];
  readReplies(w.get, {
    "/api/v1/personal/items": { items, nextCursor: "saved-next" },
  });
  render(<PersonalLibrary {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: "Tool 10" });
  change("Exact saved label", "Pending label");
  checkColumn(
    "Saved library results",
    "Label",
    ["Tool 2", "Tool 2", "Tool 10"],
    ["Tool 10", "Tool 2", "Tool 2"],
  );
  expect(
    rows("Saved library results").map((row) =>
      within(row).getByRole("link").getAttribute("href"),
    ),
  ).toEqual([
    "/unit/saved-items/p10",
    "/unit/saved-items/p2",
    "/unit/saved-items/p1",
  ]);
  checkColumn(
    "Saved library results",
    "Type",
    ["Named search", "Named search", "Resource reference"],
    ["Resource reference", "Named search", "Named search"],
    1,
  );
  checkColumn("Saved library results", "Last updated", [
    "Tool 2",
    "Tool 10",
    "Tool 2",
  ]);
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.post).not.toHaveBeenCalled();
  expect(screen.getByLabelText("Exact saved label")).toHaveProperty(
    "value",
    "Pending label",
  );
  w.get.mockResolvedValueOnce(
    etagReply({
      items: [{ ...base, id: "next", label: "New page", updatedAt: stamp }],
    }),
  );
  click("Next page");
  await screen.findByRole("link", { name: "New page" });
  expect(cellLabels("Saved library results")).toEqual(["New page"]);
  expect(w.get).toHaveBeenLastCalledWith(
    "/api/v1/personal/items",
    expect.objectContaining({
      params: { query: { limit: 25, cursor: "saved-next" } },
    }),
  );
  expect(
    screen
      .getByRole("columnheader", { name: "Last updated" })
      .getAttribute("aria-sort"),
  ).toBe("descending");
});
