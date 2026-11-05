// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import Audit from "./Audit";
import { FederationSearch } from "./FederationSearch";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";

screenLifecycle("/unit/audit");
afterEach(() => vi.unstubAllGlobals());

it.each([false, true])(
  "shows applied audit bounds with unchanged paging/export after edits; export-only %s",
  async (exportOnly) => {
    const w = screenWorkspace();
    vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
    vi.stubGlobal(
      "URL",
      Object.assign(class extends URL {}, {
        createObjectURL: vi.fn(() => "blob:private-export"),
        revokeObjectURL: vi.fn(),
      }),
    );
    w.get.mockResolvedValue(etagReply({ items: [], nextCursor: "next" }));
    w.post.mockResolvedValue(etagReply(new Blob(['{"exact":true}'])));
    const access = exportOnly
      ? {
          ...administrator,
          permissions: ["audit.export" as const],
          availablePermissions: ["audit.export" as const],
        }
      : administrator;
    render(<Audit {...w.props} access={access} />);
    change("Time range", "hour");
    click(exportOnly ? "Select export range" : "Search audit events");
    const applied = screen.getByRole("region", {
      name: "Applied audit criteria",
    });
    expect(applied.textContent).toContain("2026-09-01T11:00:00.000Z");
    expect(applied.textContent).toContain("2026-09-01T12:00:00.000Z");
    if (!exportOnly)
      await screen.findByRole("heading", { name: "Audit events" });
    change("Time range", "week");
    expect(within(applied).getByRole("status").textContent).toContain(
      "Unsubmitted filter changes",
    );
    if (!exportOnly) {
      click("Next page");
      await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
      expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
        params: {
          query: { cursor: "next", occurredFrom: "2026-09-01T11:00:00.000Z" },
        },
      });
    } else expect(w.get).not.toHaveBeenCalled();
    change("Reason", "Capture this exact investigation");
    await confirmCommand("Prepare audit export");
    await screen.findByRole("link", { name: "Download exact audit export" });
    expect(w.post.mock.calls[0]?.[1]).toMatchObject({
      body: {
        occurredFrom: "2026-09-01T11:00:00.000Z",
        occurredBefore: "2026-09-01T12:00:00.000Z",
      },
    });
    click(exportOnly ? "Select export range" : "Search audit events");
    expect(screen.queryByText(/Unsubmitted filter changes/)).toBeNull();
    expect(
      screen.getByRole("region", { name: "Applied audit criteria" })
        .textContent,
    ).toContain("2026-08-25T12:00:00.000Z");
    expect(
      screen.queryByRole("link", { name: "Download exact audit export" }),
    ).toBeNull();
  },
);

it("keeps indexed and live submitted contexts separate when shared text changes", async () => {
  const w = screenWorkspace();
  w.post.mockImplementation(async (path) =>
    path === "/api/v1/observations:search"
      ? etagReply({ items: [], nextCursor: "next" })
      : etagReply({ results: [], diagnostics: [], partial: false }),
  );
  render(<FederationSearch {...w.props} access={administrator} />);
  change("Query", "indexed original");
  click("Search indexed observations");
  await screen.findByText("No matching indexed observations.");
  const indexed = screen.getByRole("region", {
    name: "Applied indexed criteria",
  });
  expect(indexed.textContent).toContain("indexed original");
  change("Query", "live original");
  expect(within(indexed).getByRole("status").textContent).toContain(
    "Filters changed. Search again to update these results.",
  );
  change("Source IDs (optional)", "00000000-0000-7000-8000-000000000001");
  change("Reason", "Explicit local live diagnostic");
  await confirmCommand("Query live sources");
  const live = await screen.findByRole("region", {
    name: "Applied live query",
  });
  expect(live.textContent).toContain("live original");
  expect(live.textContent).toContain("00000000-0000-7000-8000-000000000001");
  const sends = w.post.mock.calls.length;
  change("Query", "unsubmitted sensitive draft");
  expect(within(live).getByRole("status").textContent).toContain(
    "Unsubmitted live changes",
  );
  expect(w.post).toHaveBeenCalledTimes(sends);
  expect(location.href).not.toContain("sensitive");
  click("Next page");
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(sends + 1));
  expect(w.post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { text: "indexed original", cursor: "next" },
  });
  click("Start a new live query");
  expect(
    screen.queryByRole("region", { name: "Applied live query" }),
  ).toBeNull();
  expect(
    screen.getByRole("region", { name: "Applied indexed criteria" })
      .textContent,
  ).toContain("indexed original");
});
