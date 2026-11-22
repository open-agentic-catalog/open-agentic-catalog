// @vitest-environment jsdom
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { expect, it } from "vitest";
import { APIError } from "../api/client";
import { deferred } from "../../tests/unit/fixtures";
import {
  administrator,
  etagReply,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";
import ListingMetadataGaps from "./ListingMetadataGaps";
import { listingMetadataGapPage } from "./responses";

screenLifecycle("/unit/listing-metadata-gaps");
const id = (n: number) =>
  "00000000-0000-4000-8000-" + String(n).padStart(12, "0");
const row = {
  listingId: id(1),
  namespaceId: id(2),
  resourceId: id(3),
  releaseId: id(4),
  listingRevisionId: id(5),
  kind: "agent",
  displayName: "Release with no support URL",
  version: "1.0",
  state: "deprecated",
  missing: ["support"],
};

it("clears stale results on a refused continuation and keeps pending criteria separate from refresh", async () => {
  const w = screenWorkspace();
  w.props.access = {
    ...administrator,
    scopeEnforcement: true,
    scopes: ["catalog.read"],
  };
  const queries: Record<string, unknown>[] = [];
  w.get.mockImplementation(async (_path, options) => {
    const { query } = (
      options as { params: { query: Record<string, unknown> } }
    ).params;
    queries.push(query);
    if (query.cursor) throw new APIError(400, "INVALID_ARGUMENT");
    if (query.kind === "skill") return etagReply({ items: [] });
    return etagReply({ items: [row], nextCursor: "page-two" });
  });
  render(<ListingMetadataGaps {...w.props} />);
  await screen.findByRole("link", { name: row.displayName });
  expect(
    screen.queryByRole("link", { name: "Edit current candidate" }),
  ).toBeNull();
  fireEvent.change(screen.getByRole("combobox", { name: "Resource kind" }), {
    target: { value: "skill" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("alert");
  expect(screen.queryByRole("link", { name: row.displayName })).toBeNull();
  expect(queries.at(-1)).toEqual({
    limit: 20,
    checks: "documentation,support,license",
    cursor: "page-two",
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh from first page" }),
  );
  await screen.findByRole("link", { name: row.displayName });
  expect(queries.at(-1)).toEqual({
    limit: 20,
    checks: "documentation,support,license",
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await screen.findByText(
    "No current releases are missing the selected fields.",
  );
  expect(queries.at(-1)).toEqual({
    limit: 20,
    checks: "documentation,support,license",
    kind: "skill",
  });
  for (const label of [
    "Documentation URL",
    "Support URL",
    "License declaration",
  ])
    fireEvent.click(screen.getByRole("checkbox", { name: label }));
  expect(screen.getByRole("button", { name: "Apply filters" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("retains filter edits during a failed refresh and retries only the applied selection", async () => {
  const w = screenWorkspace();
  const pending = deferred<unknown>();
  w.get
    .mockResolvedValueOnce(etagReply({ items: [row] }))
    .mockReturnValueOnce(pending.promise)
    .mockResolvedValueOnce(etagReply({ items: [] }))
    .mockResolvedValueOnce(etagReply({ items: [row] }));
  render(<ListingMetadataGaps {...w.props} />);
  await screen.findByRole("link", { name: row.displayName });
  const apply = screen.getByRole("button", { name: "Apply filters" });
  fireEvent.click(apply);
  expect(screen.getByRole("status").textContent).toContain("Loading");
  expect(apply).toHaveProperty("disabled", true);
  expect(
    screen.getByRole("button", { name: "Refresh from first page" }),
  ).toHaveProperty("disabled", true);
  expect(screen.queryByRole("link", { name: row.displayName })).toBeNull();
  fireEvent.change(screen.getByRole("textbox", { name: "Namespace ID" }), {
    target: { value: row.namespaceId },
  });
  const failure = new APIError(503, "DEPENDENCY_UNAVAILABLE", "report-read");
  await act(async () => pending.reject(failure));
  const alert = screen.getByRole("alert");
  expect(alert.textContent).toContain(
    "The request could not be completed. Please try again.",
  );
  expect(w.props.onFailure).toHaveBeenCalledExactlyOnceWith(failure);
  expect(screen.queryByText(/No current releases are missing/)).toBeNull();
  expect(w.get).toHaveBeenCalledTimes(2);
  fireEvent.click(within(alert).getByRole("button", { name: "Reload" }));
  await screen.findByText(
    "No current releases are missing the selected fields.",
  );
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.getByRole("textbox", { name: "Namespace ID" })).toHaveProperty(
    "value",
    row.namespaceId,
  );
  fireEvent.click(apply);
  await screen.findByRole("link", { name: row.displayName });
  const initial = { checks: "documentation,support,license", limit: 20 };
  expect(
    w.get.mock.calls.map(([path, options]) => [
      path,
      (options as { params: { query: unknown } }).params.query,
    ]),
  ).toEqual([
    ["/api/v1/listing-metadata-gaps", initial],
    ["/api/v1/listing-metadata-gaps", initial],
    ["/api/v1/listing-metadata-gaps", initial],
    [
      "/api/v1/listing-metadata-gaps",
      { ...initial, namespaceId: row.namespaceId },
    ],
  ]);
});

it("refuses a malformed row or page instead of hiding a missing declaration", () => {
  expect(listingMetadataGapPage({ items: [row] })).toBe(true);
  for (const fields of [
    { state: "retired" },
    { listingRevisionId: ".." },
    { releaseId: null },
    { kind: "unknown" },
    { missing: [] },
    { missing: ["support", "support"] },
    { missing: ["tags"] },
    { displayName: "" },
    { version: "" },
  ])
    expect(listingMetadataGapPage({ items: [{ ...row, ...fields }] })).toBe(
      false,
    );
  expect(listingMetadataGapPage({ items: null })).toBe(false);
  expect(listingMetadataGapPage({ items: Array(101).fill(row) })).toBe(false);
});

it("resolves a later-page edit grant only when the reader inspects the references", async () => {
  const w = screenWorkspace();
  const access = {
    ...administrator,
    membershipId: id(6),
    realmId: id(7),
    tenantId: id(8),
    permissions: [],
    namespaceGrants: [],
    nextCursor: "more-namespaces",
  };
  w.props.access = access;
  const queries: Record<string, unknown>[] = [];
  w.get.mockImplementation(async (path, options) => {
    const { query } = (
      options as { params: { query: Record<string, unknown> } }
    ).params;
    queries.push(query);
    if (path === "/api/v1/me")
      return etagReply({
        ...access,
        namespaceGrants: [
          {
            namespaceId: row.namespaceId,
            permissions: [
              "resource.read",
              "resource.read_history",
              "resource.read_content",
              "resource.edit",
            ],
          },
        ],
      });
    return etagReply({ items: [row] });
  });
  const view = render(<ListingMetadataGaps {...w.props} />);
  await screen.findByRole("link", { name: row.displayName });
  fireEvent.change(screen.getByRole("textbox", { name: "Namespace ID" }), {
    target: { value: row.namespaceId },
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await waitFor(() =>
    expect(queries).toEqual([
      { checks: "documentation,support,license", limit: 20 },
      {
        checks: "documentation,support,license",
        namespaceId: row.namespaceId,
        limit: 20,
      },
    ]),
  );
  fireEvent.click(screen.getByText("Release references"));
  expect(
    (
      await screen.findByRole("link", { name: "Edit current candidate" })
    ).getAttribute("href"),
  ).toBe("/unit/listings/" + row.listingId);
  expect(queries.at(-1)).toEqual({ namespaceId: row.namespaceId });
  view.rerender(
    <ListingMetadataGaps
      {...w.props}
      access={{ ...access, availablePermissions: [], authorizationRevision: 2 }}
    />,
  );
  await screen.findByRole("alert");
  expect(screen.queryByText(row.displayName)).toBeNull();
});
