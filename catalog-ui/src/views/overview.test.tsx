// @vitest-environment jsdom
import { afterEach, expect, it } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { workspace, reply, deferred } from "../../tests/unit/fixtures";
import type { Access } from "../api/client";
import Overview from "./Overview";
import { reviewFixture } from "../../tests/unit/overview-fixtures";

afterEach(() => {
  cleanup();
  history.replaceState(null, "", "/");
});

it.each([
  [0, 0, 0, 0],
  [7, 0, 0, 0],
  [4, 2, 1, 0],
])("shows each kind count once for %j", async (...counts) => {
  const f = workspace();
  f.props.access = {
    ...f.props.access,
    permissions: ["resource.read"],
    availablePermissions: ["resource.read"],
  };
  f.get.mockResolvedValue(
    reply({
      ...resource,
      total: { value: counts.reduce((a, b) => a + b, 0), limited: false },
      kinds: resource.kinds.map((kind, index) => ({
        ...kind,
        count: counts[index],
      })),
    }),
  );
  render(<Overview {...f.props} />);
  const panel = screen.getByRole("region", { name: "Local resources" });
  await within(panel).findByRole("link", { name: "Agent" });
  expect(
    [...panel.querySelectorAll(".overview-kind dd")].map(
      (node) => node.textContent,
    ),
  ).toEqual(counts.map(String));
  expect(panel.querySelectorAll(".overview-kind")).toHaveLength(4);
  expect(panel.querySelectorAll("details,table,.overview-track")).toHaveLength(
    0,
  );
  expect(f.get).toHaveBeenCalledTimes(1);
});

it("shows malformed successful counts as an error without a numeric summary", async () => {
  const f = workspace();
  f.props.access = {
    ...f.props.access,
    permissions: ["resource.read"],
    availablePermissions: ["resource.read"],
  };
  f.get.mockResolvedValue(
    reply({ ...resource, total: { value: -1, limited: false } }),
  );
  render(<Overview {...f.props} />);
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(document.querySelector(".overview-number")).toBeNull();
  expect(f.props.onFailure).toHaveBeenCalled();
});
const scope = {
  tenantId: "tenant-a",
  scope: "current_access",
  selectionLimit: 10000,
  observedAt: "2026-10-25T12:00:00Z",
};
const resource = {
  ...scope,
  total: { value: 4, limited: false },
  kinds: ["agent", "mcp_server", "skill", "plugin"].map((kind) => ({
    kind,
    count: 1,
  })),
};

it("uses singular labels for one resource, directory entry and indexed record", async () => {
  const f = workspace();
  f.get.mockImplementation(async (path: unknown) => {
    if (path === "/api/v1/overview/reviews") return reply(reviewFixture());
    if (path === "/api/v1/overview/resources")
      return reply({
        ...resource,
        total: { value: 1, limited: false },
        kinds: resource.kinds.map((kind, index) => ({
          ...kind,
          count: index === 0 ? 1 : 0,
        })),
      });
    if (path === "/api/v1/overview/indexed")
      return reply({ ...scope, count: 1, limited: false });
    if (path === "/api/v1/overview/releases")
      return reply({
        ...scope,
        published: { value: 0, limited: false },
        deprecated: { value: 0, limited: false },
      });
    return reply({ ...scope, count: { value: 1, limited: false } });
  });
  render(<Overview {...f.props} />);
  await screen.findByRole("link", { name: "Agent" });
  for (const [label, text] of [
    ["Namespaces", "1"],
    ["Publishers", "1"],
    ["External records", "1"],
  ] as const) {
    expect(screen.getByRole("region", { name: label }).textContent).toContain(
      text,
    );
  }
});

it("reads independent panels, uses matching links and refreshes explicitly", async () => {
  const f = workspace();
  f.get.mockImplementation(async (path: unknown) => {
    if (path === "/api/v1/overview/reviews") return reply(reviewFixture());
    if (path === "/api/v1/overview/resources") return reply(resource);
    if (path === "/api/v1/overview/releases")
      return reply({
        ...scope,
        published: { value: 2, limited: false },
        deprecated: { value: 1, limited: false },
      });
    if (path === "/api/v1/overview/indexed")
      return reply({ ...scope, count: 0, limited: false });
    return reply({ ...scope, count: { value: 1, limited: false } });
  });
  render(<Overview {...f.props} />);
  await screen.findByRole("link", { name: "Agent" });
  expect(f.get).toHaveBeenCalledTimes(6);
  expect(screen.getByRole("link", { name: "Agent" }).getAttribute("href")).toBe(
    "/unit/resources?kind=agent",
  );
  expect(
    screen
      .getByRole("link", { name: "Browse indexed records" })
      .getAttribute("href"),
  ).toBe("/unit/catalog?origin=indexed");
  expect(document.querySelectorAll(".overview-kind dd")).toHaveLength(4);
  expect(document.querySelectorAll(".overview-observation")).toHaveLength(6);
  expect(
    screen.queryByText(/Current access in tenant|Each panel counts|as a table/),
  ).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() => expect(f.get).toHaveBeenCalledTimes(12));
});

it("keeps failed reads separate and omits misleading limited charts", async () => {
  const f = workspace();
  f.get.mockImplementation(async (path: unknown) => {
    if (path === "/api/v1/overview/reviews") return reply(reviewFixture());
    if (path === "/api/v1/overview/resources")
      return reply({ ...scope, total: { value: 10000, limited: true } });
    if (path === "/api/v1/overview/indexed")
      return reply({ ...scope, limited: true });
    throw new Error("controlled read failure");
  });
  render(<Overview {...f.props} />);
  expect(await screen.findByText(/kind breakdown is unavailable/)).toBeTruthy();
  expect(
    within(screen.getByRole("region", { name: "Local resources" })).queryByRole(
      "table",
    ),
  ).toBeNull();
  expect(await screen.findByText(/Count unavailable/)).toBeTruthy();
  expect(
    within(screen.getByRole("region", { name: "Current releases" })).getByRole(
      "alert",
    ),
  ).toBeTruthy();
  expect(
    within(screen.getByRole("region", { name: "Local resources" })).queryByRole(
      "alert",
    ),
  ).toBeNull();
});

it("does not read denied or unknown views and follows Back/Forward view changes", async () => {
  const f = workspace();
  history.replaceState(null, "", "/overview?view=unknown");
  render(<Overview {...f.props} />);
  expect(screen.getByRole("alert").textContent).toContain("unavailable");
  expect(f.get).not.toHaveBeenCalled();
  history.pushState(null, "", "/overview?view=content");
  fireEvent(window, new PopStateEvent("popstate"));
  await waitFor(() => expect(f.get).toHaveBeenCalledTimes(6));
});

it("clears private data and ignores late reads after permission loss", async () => {
  const f = workspace(),
    pending = deferred<unknown>();
  f.get.mockReturnValue(pending.promise);
  const view = render(<Overview {...f.props} />);
  const denied = {
    ...f.props.access,
    permissions: [],
    availablePermissions: [],
    namespaceGrants: [],
  };
  view.rerender(<Overview {...f.props} access={denied} />);
  pending.resolve(reply(resource));
  await waitFor(() =>
    expect(screen.getByRole("alert").textContent).toContain("does not include"),
  );
  expect(document.querySelector(".overview-number")).toBeNull();
  expect(f.get).toHaveBeenCalledTimes(6);
});

it.each(["permission", "scope"])(
  "removes loaded counts after selective %s loss and retains permitted panels",
  async (lost) => {
    const f = workspace();
    const access: Access = {
      ...f.props.access,
      permissions: ["resource.read", "publisher.read"],
      availablePermissions: ["resource.read", "publisher.read"],
      namespaceGrants: [],
      scopeEnforcement: true,
      scopes: ["catalog.read", "publishers.read"],
    };
    f.get.mockImplementation(async (path: unknown) =>
      reply(
        path === "/api/v1/overview/resources"
          ? resource
          : { ...scope, count: { value: 1, limited: false } },
      ),
    );
    const view = render(<Overview {...f.props} access={access} />);
    await screen.findByRole("link", { name: "Agent" });
    expect(f.get).toHaveBeenCalledTimes(2);
    const reduced: Access =
      lost === "permission"
        ? {
            ...access,
            permissions: ["publisher.read"],
            availablePermissions: ["publisher.read"],
          }
        : { ...access, scopes: ["publishers.read"] };
    view.rerender(<Overview {...f.props} access={reduced} />);
    expect(
      screen.queryByRole("region", { name: "Local resources" }),
    ).toBeNull();
    expect(screen.queryByRole("link", { name: "Agent" })).toBeNull();
    await waitFor(() =>
      expect(
        screen.getByRole("region", { name: "Publishers" }).textContent,
      ).toContain("1"),
    );
    expect(f.get.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/overview/resources",
      "/api/v1/overview/publishers",
      "/api/v1/overview/publishers",
    ]);
    expect(f.props.onFailure).not.toHaveBeenCalled();
  },
);
