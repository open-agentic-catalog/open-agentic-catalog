// @vitest-environment jsdom
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, it } from "vitest";
import ValidationMaintenance from "./ValidationMaintenance";
import {
  maintenanceItem,
  maintenancePage,
} from "./validation-maintenance-responses";
import { APIError } from "../api/client";
import {
  administrator,
  etagReply,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";

screenLifecycle("/unit/validation-maintenance");
const row = {
  revisionId: "revision-one",
  resourceId: "resource-one",
  namespaceId: "namespace-one",
  kind: "agent",
  canonicalIdentifier: "urn:air:example.com:agents:invoice",
  versionId: "version-one",
  version: "1.0.0",
  revisionNumber: 2,
  evidenceId: "evidence-one",
  evidenceState: "complete",
  validationProfile: "catalog-validation-v7",
  policyVersion: "catalog-policy-v1",
  validatedAt: stamp,
  expiresAt: stamp,
  applicable: false,
  conditions: ["expired"],
};
const page = {
  items: [row],
  observedAt: stamp,
  horizonHours: 24,
  nextCursor: "later",
};

it("stages filters, continues applied criteria and explicitly refreshes without issuing a command", async () => {
  const w = screenWorkspace();
  w.props.access = administrator;
  const queries: Record<string, unknown>[] = [];
  w.get.mockImplementation(async (_path, options) => {
    const q = (options as { params: { query: Record<string, unknown> } }).params
      .query;
    queries.push(q);
    if (q.cursor) throw new APIError(400, "INVALID_ARGUMENT");
    return etagReply({
      ...page,
      horizonHours: q.horizonHours,
      items: q.state === "stale" ? [] : [row],
    });
  });
  render(<ValidationMaintenance {...w.props} />);
  const link = await screen.findByRole("link", {
    name: "Inspect evidence and renewal",
  });
  expect(link.getAttribute("href")).toBe(
    "/unit/version-revisions/revision-one",
  );
  const initial = queries.length;
  fireEvent.change(
    screen.getByRole("combobox", { name: "Evidence condition" }),
    { target: { value: "expired" } },
  );
  fireEvent.change(
    screen.getByRole("spinbutton", { name: "Expiry horizon (hours)" }),
    { target: { value: "048" } },
  );
  expect(queries).toHaveLength(initial);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await waitFor(() =>
    expect(queries.at(-1)).toEqual({
      state: "expired",
      horizonHours: 48,
      limit: 20,
    }),
  );
  expect(
    screen.queryByText(
      "Filter edits are pending. Apply filters to update the list.",
    ),
  ).toBeNull();
  fireEvent.change(
    screen.getByRole("combobox", { name: "Evidence condition" }),
    { target: { value: "stale" } },
  );
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("alert");
  expect(queries.at(-1)).toEqual({
    state: "expired",
    horizonHours: 48,
    limit: 20,
    cursor: "later",
  });
  expect(
    screen.getByRole("combobox", { name: "Evidence condition" }),
  ).toHaveProperty("value", "stale");
  fireEvent.click(screen.getByRole("button", { name: "First page" }));
  await screen.findByRole("link", { name: "Inspect evidence and renewal" });
  expect(queries.at(-1)).toEqual({
    state: "expired",
    horizonHours: 48,
    limit: 20,
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh from first page" }),
  );
  await waitFor(() => expect(queries).toHaveLength(initial + 4));
  expect(
    screen.getByRole("combobox", { name: "Evidence condition" }),
  ).toHaveProperty("value", "stale");
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await screen.findByText("No current revisions match this selection.");
  expect(w.post).not.toHaveBeenCalled();
});

it("rejects an invalid horizon locally and associates the error with its input", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(page));
  render(<ValidationMaintenance {...w.props} />);
  await screen.findByRole("link", { name: "Inspect evidence and renewal" });
  const calls = w.get.mock.calls.length;
  const input = screen.getByRole("spinbutton", {
    name: "Expiry horizon (hours)",
  });
  fireEvent.change(input, { target: { value: "169" } });
  fireEvent.submit(
    screen.getByRole("form", { name: "Filter validation maintenance" }),
  );
  const alert = await screen.findByRole("alert");
  expect(input.getAttribute("aria-describedby")).toBe(alert.id);
  expect(w.get).toHaveBeenCalledTimes(calls);
  expect(w.post).not.toHaveBeenCalled();
});

it("pages the applied selection and refreshes from the first page without applying pending edits", async () => {
  const w = screenWorkspace();
  const queries: Record<string, unknown>[] = [];
  w.get.mockImplementation(async (_path, options) => {
    const query = (options as { params: { query: Record<string, unknown> } })
      .params.query;
    queries.push(query);
    return etagReply({
      ...page,
      nextCursor: query.cursor ? "" : "later",
      items: query.cursor
        ? [
            {
              ...row,
              revisionId: "revision-two",
              versionId: "version-two",
              version: "2.0.0",
              revisionNumber: 1,
              evidenceId: "evidence-two",
              expiresAt: "2026-09-02T12:00:00Z",
              conditions: ["expiring"],
              applicable: true,
            },
          ]
        : [
            {
              ...row,
              expiresAt: undefined,
              conditions: ["blocked", "stale"],
              evidenceState: "blocked",
              policyVersion: "previous-policy",
            },
          ],
    });
  });
  render(<ValidationMaintenance {...w.props} />);
  await screen.findByText("No time expiry");
  expect(
    screen.getByRole("cell", {
      name: /Blocked, Policy stale.*Not applicable at observation/,
    }),
  ).toBeTruthy();
  const calls = queries.length;
  // Applying the unchanged first-page criteria deliberately refreshes once.
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await waitFor(() => expect(queries).toHaveLength(calls + 1));
  await screen.findByText("No time expiry");
  fireEvent.change(
    screen.getByRole("combobox", { name: "Evidence condition" }),
    { target: { value: "blocked" } },
  );
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("cell", {
    name: /Expiring.*Applicable at observation/,
  });
  expect(queries.at(-1)).toEqual({
    state: "attention",
    horizonHours: 24,
    limit: 20,
    cursor: "later",
  });
  expect(screen.queryByText("evidence-one")).toBeNull();
  expect(
    screen
      .getByRole("link", { name: "Inspect evidence and renewal" })
      .getAttribute("href"),
  ).toBe("/unit/version-revisions/revision-two");
  expect(screen.queryByRole("button", { name: "Next page" })).toBeNull();
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh from first page" }),
  );
  await screen.findByText("No time expiry");
  expect(queries).toHaveLength(calls + 3);
  expect(queries.at(-1)).toEqual({
    state: "attention",
    horizonHours: 24,
    limit: 20,
  });
  expect(
    screen.getByRole("combobox", { name: "Evidence condition" }),
  ).toHaveProperty("value", "blocked");
  expect(screen.queryByText("evidence-two")).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("refuses a malformed successful page before rendering a link and recovers on explicit refresh", async () => {
  const w = screenWorkspace();
  w.get
    .mockResolvedValueOnce(
      etagReply({ ...page, items: [{ ...row, revisionId: "../private" }] }),
    )
    .mockResolvedValue(etagReply(page));
  render(<ValidationMaintenance {...w.props} />);
  await screen.findByRole("alert");
  expect(
    screen.queryByRole("link", { name: "Inspect evidence and renewal" }),
  ).toBeNull();
  expect(screen.queryByText("evidence-one")).toBeNull();
  expect(w.get).toHaveBeenCalledTimes(1);
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh from first page" }),
  );
  const link = await screen.findByRole("link", {
    name: "Inspect evidence and renewal",
  });
  expect(link.getAttribute("href")).toBe(
    "/unit/version-revisions/revision-one",
  );
  expect(screen.queryByRole("alert")).toBeNull();
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(w.post).not.toHaveBeenCalled();
});

it("refuses unsafe links, invalid conditions, observations and page bounds", () => {
  expect(maintenanceItem(row)).toBe(true);
  expect(maintenancePage(page)).toBe(true);
  for (const change of [
    { revisionId: "../private" },
    { conditions: [] },
    { conditions: ["expired", "expired"] },
    { conditions: ["healthy"] },
    { expiresAt: null },
    { evidenceState: "unknown" },
    { applicable: "yes" },
  ])
    expect(maintenanceItem({ ...row, ...change })).toBe(false);
  for (const change of [
    { observedAt: "bad" },
    { horizonHours: 0 },
    { horizonHours: 169 },
    { items: Array.from({ length: 101 }, () => row) },
    { nextCursor: null },
  ])
    expect(maintenancePage({ ...page, ...change })).toBe(false);
});
