// @vitest-environment jsdom
import { render, screen, within } from "@testing-library/react";
import { expect, it } from "vitest";
import Resources from "./Resources";
import Members from "./Members";
import {
  administrator,
  click,
  readReplies,
  resource,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";

screenLifecycle("/unit");
const rowLinks = () =>
  within(screen.getAllByRole("rowgroup")[1]!)
    .getAllByRole("row")
    .map((row) => within(row).getAllByRole("link")[0]!.textContent);

it("sorts Resources by displayed kind, natural identifier and actual timestamp, without reads or writes", async () => {
  const { props, get, post } = screenWorkspace();
  const items = Object.freeze([
    {
      ...resource,
      id: "r3",
      canonicalIdentifier: "tool-10",
      kind: "agent",
      updatedAt: "2026-10-01T00:00:00Z",
    },
    {
      ...resource,
      id: "r2",
      canonicalIdentifier: "tool-2",
      kind: "skill",
      updatedAt: "2026-09-30T23:30:00-02:00",
    },
    {
      ...resource,
      id: "r1",
      canonicalIdentifier: "tool-1",
      kind: "mcp_server",
      updatedAt: "2026-09-30T23:00:00Z",
    },
  ]);
  readReplies(get, {
    "/api/v1/resources": { items, nextCursor: "resource-page-2" },
  });
  render(<Resources {...props} />);
  await screen.findByRole("link", { name: "tool-10" });
  expect(rowLinks()).toEqual(["tool-10", "tool-2", "tool-1"]);
  expect(
    screen.getByText(
      "Select a column heading to sort the results on this page.",
    ),
  ).toBeTruthy();
  for (const [name, ascending] of [
    ["identifier", ["tool-1", "tool-2", "tool-10"]],
    ["kind", ["tool-10", "tool-1", "tool-2"]],
    ["updated", ["tool-1", "tool-10", "tool-2"]],
  ] as const) {
    click(`Sort by ${name}, ascending`);
    expect(rowLinks()).toEqual(ascending);
    expect(
      screen
        .getAllByRole("columnheader")
        .filter((header) => header.getAttribute("aria-sort") === "ascending"),
    ).toHaveLength(1);
    click(`Sort by ${name}, descending`);
    expect(rowLinks()).toEqual([...ascending].reverse());
  }
  expect(get).toHaveBeenCalledTimes(1);
  expect(post).not.toHaveBeenCalled();
  click("Next page");
  await screen.findByRole("button", { name: "First page" });
  expect(get).toHaveBeenLastCalledWith(
    "/api/v1/resources",
    expect.objectContaining({
      params: { query: { limit: 25, cursor: "resource-page-2" } },
    }),
  );
  expect(
    screen
      .getByRole("columnheader", { name: "Updated" })
      .getAttribute("aria-sort"),
  ).toBe("descending");
  expect(rowLinks()).toEqual(["tool-2", "tool-10", "tool-1"]);
});

it("sorts Members by subject, identity type, state and the displayed realm, leaving Access as an action", async () => {
  const { props, get, post } = screenWorkspace();
  const member = {
    tenantId: "tenant-a",
    realmId: administrator.realmId,
    actorType: "human",
    state: "active",
    revision: 1,
  };
  const items = Object.freeze([
    {
      ...member,
      id: "m3",
      subject: "member-10",
      actorType: "service",
      state: "suspended",
    },
    {
      ...member,
      id: "m2",
      subject: "member-2",
      state: "offboarded",
      realmId: "aaa-realm",
    },
    { ...member, id: "m1", subject: "member-1", realmId: "zzz-realm" },
  ]);
  readReplies(get, {
    "/api/v1/memberships": { items, nextCursor: "member-page-2" },
    "/api/v1/access/roles": { items: [], authorizationRevision: 1 },
  });
  render(<Members {...props} access={administrator} />);
  await screen.findByRole("link", { name: "member-10" });
  expect(screen.getByLabelText("Identity type").tagName).toBe("SELECT");
  expect(screen.getByLabelText("State").tagName).toBe("SELECT");
  expect(rowLinks()).toEqual(["member-10", "member-2", "member-1"]);
  for (const [name, ascending, descending] of [
    [
      "login identifier (subject)",
      ["member-1", "member-2", "member-10"],
      ["member-10", "member-2", "member-1"],
    ],
    [
      "identity type",
      ["member-2", "member-1", "member-10"],
      ["member-10", "member-2", "member-1"],
    ],
    [
      "state",
      ["member-1", "member-2", "member-10"],
      ["member-10", "member-2", "member-1"],
    ],
    [
      "realm",
      ["member-2", "member-10", "member-1"],
      ["member-1", "member-10", "member-2"],
    ],
  ] as const) {
    click(`Sort by ${name}, ascending`);
    expect(rowLinks()).toEqual(ascending);
    click(`Sort by ${name}, descending`);
    expect(rowLinks()).toEqual(descending);
  }
  expect(
    within(screen.getByRole("columnheader", { name: "Access" })).queryByRole(
      "button",
    ),
  ).toBeNull();
  expect(
    screen.getByRole("link", { name: "member-2" }).getAttribute("href"),
  ).toBe("/unit/members/m2");
  expect(get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/memberships",
    "/api/v1/access/roles",
  ]);
  expect(post).not.toHaveBeenCalled();
  click("Next page");
  await screen.findByRole("button", { name: "First page" });
  expect(get).toHaveBeenLastCalledWith(
    "/api/v1/memberships",
    expect.objectContaining({
      params: { query: { limit: 25, cursor: "member-page-2" } },
    }),
  );
  expect(
    screen
      .getByRole("columnheader", { name: "Realm" })
      .getAttribute("aria-sort"),
  ).toBe("descending");
});
