// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  render,
  screen,
  fireEvent,
  waitFor,
  within,
} from "@testing-library/react";
import { workspace, reply, deferred } from "../../tests/unit/fixtures";
import { capabilities } from "../../tests/unit/responses";
import {
  endpointOverviewFixture,
  deliveryOverviewFixture,
  notificationsFixture,
} from "../../tests/unit/webhook-overview-fixtures";
import {
  webhookEndpointOverview,
  webhookDeliveryOverview,
} from "./webhook-overview-values";
import Overview from "./Overview";

afterEach(() => {
  cleanup();
  history.replaceState(null, "", "/");
});
function webhookAccess(evidence = true) {
  const f = workspace();
  const permissions: typeof f.props.access.permissions = [
    "webhook.read",
    ...(evidence ? ["webhook.read_evidence" as const] : []),
  ];
  f.props.access = {
    ...f.props.access,
    permissions,
    availablePermissions: permissions,
    namespaceGrants: [],
    scopeEnforcement: true,
    scopes: ["webhooks.manage"],
  };
  f.get.mockImplementation(async (path: unknown) =>
    reply(
      path === "/api/v1/capabilities"
        ? {
            ...capabilities,
            optionalCapabilities: {
              ...capabilities.optionalCapabilities,
              webhooks: "enabled",
            },
          }
        : path === "/api/v1/overview/webhook-endpoints"
          ? endpointOverviewFixture()
          : path === "/api/v1/overview/webhook-deliveries"
            ? deliveryOverviewFixture()
            : notificationsFixture(),
    ),
  );
  const mutate = vi.fn();
  Object.assign(f.props.client.api, {
    POST: mutate,
    PUT: mutate,
    PATCH: mutate,
    DELETE: mutate,
  });
  history.replaceState(null, "", "/overview");
  return { ...f, mutate };
}

it("defaults webhook-only access to Operations and keeps event and delivery units separate", async () => {
  const f = webhookAccess();
  render(<Overview {...f.props} />);
  expect(await screen.findByText("Publication receiver")).toBeTruthy();
  expect(
    screen
      .getByRole("link", { name: "Operations" })
      .getAttribute("aria-current"),
  ).toBe("page");
  expect(
    screen
      .getByRole("link", { name: "Publication receiver" })
      .getAttribute("href"),
  ).toBe("/unit/webhooks/00000000-0000-7000-8000-000000000010");
  const deliveries = await screen.findByRole("region", {
    name: "Delivery history",
  });
  expect(within(deliveries).getByText("10")).toBeTruthy();
  const counters = screen.getByRole("region", {
    name: "Notification totals",
  });
  expect(within(counters).getByText("At least 1,000")).toBeTruthy();
  expect(within(counters).getByText("5")).toBeTruthy();
  expect(within(counters).getByText("2")).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Content" })).toBeNull();
  expect(
    screen.queryByRole("region", { name: "Federation sources" }),
  ).toBeNull();
  await waitFor(() => expect(f.get).toHaveBeenCalledTimes(4));
  expect(
    screen.getAllByRole("link", { name: "Active" })[0]!.getAttribute("href"),
  ).toBe("/unit/webhooks?status=active");
  expect(
    screen.getByRole("link", { name: "View delivery" }).getAttribute("href"),
  ).toBe(
    "/unit/webhooks/00000000-0000-7000-8000-000000000010?deliveryId=00000000-0000-7000-9000-000000000010&state=waiting",
  );
  expect(f.mutate).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() => expect(f.get).toHaveBeenCalledTimes(8));
});

it("reads only endpoint states without evidence permission", async () => {
  const f = webhookAccess(false);
  render(<Overview {...f.props} />);
  await screen.findByText(/endpoints/);
  expect(f.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/capabilities",
    "/api/v1/overview/webhook-endpoints",
  ]);
  expect(screen.queryByRole("region", { name: "Delivery history" })).toBeNull();
});

function withIndexing(f: ReturnType<typeof webhookAccess>) {
  const permissions = [
    ...f.props.access.permissions,
    "operations.reindex" as const,
  ];
  f.props.access = {
    ...f.props.access,
    permissions,
    availablePermissions: permissions,
    scopes: [...f.props.access.scopes, "tenant.admin"],
  };
}

it("shares one capability response between webhooks and search indexing", async () => {
  const f = webhookAccess();
  withIndexing(f);
  render(<Overview {...f.props} />);
  await screen.findByText("Publication receiver");
  expect(screen.getByRole("region", { name: "Search indexing" })).toBeTruthy();
  expect(
    f.get.mock.calls.filter(([path]) => path === "/api/v1/capabilities"),
  ).toHaveLength(1);
  expect(f.get).toHaveBeenCalledTimes(4);
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() => expect(f.get).toHaveBeenCalledTimes(8));
  expect(
    f.get.mock.calls.filter(([path]) => path === "/api/v1/capabilities"),
  ).toHaveLength(2);
});

it("retries a shared capability failure from search indexing", async () => {
  const f = webhookAccess();
  withIndexing(f);
  f.get.mockRejectedValueOnce(new Error("capabilities unavailable"));
  render(<Overview {...f.props} />);
  const indexing = screen.getByRole("region", { name: "Search indexing" });
  await within(indexing).findByRole("alert");
  expect(
    within(screen.getByRole("region", { name: "Webhooks" })).getByRole("alert"),
  ).toBeTruthy();
  expect(
    screen.queryByRole("region", { name: "Webhook endpoints" }),
  ).toBeNull();
  expect(f.get).toHaveBeenCalledTimes(1);
  expect(f.props.onFailure).toHaveBeenCalledTimes(1);
  fireEvent.click(within(indexing).getByRole("button", { name: "Reload" }));
  await screen.findByText("Publication receiver");
  expect(within(indexing).queryByRole("alert")).toBeNull();
  expect(f.get).toHaveBeenCalledTimes(5);
  expect(f.mutate).not.toHaveBeenCalled();
});

it.each(["disabled", "unavailable"])(
  "does not read private webhook summaries when %s",
  async (status) => {
    const f = webhookAccess();
    f.get.mockResolvedValue(
      reply({
        ...capabilities,
        optionalCapabilities: {
          ...capabilities.optionalCapabilities,
          webhooks: status,
        },
      }),
    );
    render(<Overview {...f.props} />);
    await screen.findByText(status === "disabled" ? "Off" : "Unavailable");
    expect(f.get).toHaveBeenCalledTimes(1);
  },
);

it("denies missing scope and clears summaries on access loss", async () => {
  const f = webhookAccess();
  const view = render(<Overview {...f.props} />);
  await screen.findByText("Publication receiver");
  view.rerender(
    <Overview {...f.props} access={{ ...f.props.access, scopes: [] }} />,
  );
  expect(screen.queryByText("Publication receiver")).toBeNull();
  expect(screen.getByRole("alert").textContent).toContain(
    "does not include Overview",
  );
});

it("fences a late capability response after permission loss", async () => {
  const f = webhookAccess();
  const pending = deferred<ReturnType<typeof reply>>();
  f.get.mockReturnValue(pending.promise);
  const view = render(<Overview {...f.props} />);
  view.rerender(
    <Overview
      {...f.props}
      access={{ ...f.props.access, permissions: [], availablePermissions: [] }}
    />,
  );
  pending.resolve(reply(capabilities));
  await waitFor(() =>
    expect(
      screen.queryByRole("region", { name: "Webhook endpoints" }),
    ).toBeNull(),
  );
  expect(f.get).toHaveBeenCalledTimes(1);
});

it("keeps successful delivery and notification sections when endpoint reads fail", async () => {
  const f = webhookAccess(),
    load = f.get.getMockImplementation()!;
  f.get.mockImplementation((path, ...args) =>
    path === "/api/v1/overview/webhook-endpoints"
      ? Promise.reject(new Error("endpoint unavailable"))
      : load(path, ...args),
  );
  render(<Overview {...f.props} />);
  await screen.findByText("Publication receiver");
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.getByText("At least 1,000")).toBeTruthy();
  expect(f.props.onFailure).toHaveBeenCalled();
});

it("discards late delivery evidence and counters after evidence access is revoked", async () => {
  const f = webhookAccess(),
    load = f.get.getMockImplementation()!;
  const deliveries = deferred<ReturnType<typeof reply>>();
  const notifications = deferred<ReturnType<typeof reply>>();
  const signals: AbortSignal[] = [];
  f.get.mockImplementation((path, options, ...args) => {
    if (
      path === "/api/v1/overview/webhook-deliveries" ||
      path === "/api/v1/webhooks/reconciliation"
    ) {
      signals.push((options as { signal: AbortSignal }).signal);
      return path === "/api/v1/overview/webhook-deliveries"
        ? deliveries.promise
        : notifications.promise;
    }
    return load(path, options, ...args);
  });
  const view = render(<Overview {...f.props} />);
  await waitFor(() => expect(signals).toHaveLength(2));
  const permissions = ["webhook.read" as const];
  view.rerender(
    <Overview
      {...f.props}
      access={{
        ...f.props.access,
        permissions,
        availablePermissions: permissions,
      }}
    />,
  );
  expect(signals.every((signal) => signal.aborted)).toBe(true);
  deliveries.resolve(reply(deliveryOverviewFixture()));
  notifications.resolve(reply(notificationsFixture()));
  await screen.findByText(/endpoints/);
  expect(screen.queryByText("Publication receiver")).toBeNull();
  expect(screen.queryByRole("region", { name: "Delivery history" })).toBeNull();
  expect(
    screen.queryByRole("region", { name: "Notification totals" }),
  ).toBeNull();
  expect(f.props.onFailure).not.toHaveBeenCalled();
  expect(
    f.get.mock.calls.filter(
      ([path]) =>
        path === "/api/v1/overview/webhook-deliveries" ||
        path === "/api/v1/webhooks/reconciliation",
    ),
  ).toHaveLength(2);
});

it("retries a failed delivery read without refreshing successful panels", async () => {
  const f = webhookAccess(),
    load = f.get.getMockImplementation()!;
  let deliveryReads = 0;
  f.get.mockImplementation((path, ...args) => {
    if (path === "/api/v1/overview/webhook-deliveries" && ++deliveryReads === 1)
      return Promise.reject(new Error("delivery summary unavailable"));
    return load(path, ...args);
  });
  render(<Overview {...f.props} />);
  const deliveries = await screen.findByRole("region", {
    name: "Delivery history",
  });
  await within(deliveries).findByRole("alert");
  await screen.findByText("At least 1,000");
  fireEvent.click(within(deliveries).getByRole("button", { name: "Reload" }));
  await within(deliveries).findByText("Publication receiver");
  expect(within(deliveries).queryByRole("alert")).toBeNull();
  expect(screen.getByText("At least 1,000")).toBeTruthy();
  expect(f.get).toHaveBeenCalledTimes(5);
  expect(deliveryReads).toBe(2);
  expect(f.mutate).not.toHaveBeenCalled();
});

it.each(["endpoints", "deliveries"])(
  "shows %s limits without drawing incomplete bars",
  async (kind) => {
    const f = webhookAccess(),
      load = f.get.getMockImplementation()!;
    f.get.mockImplementation((path, ...args) =>
      path === `/api/v1/overview/webhook-${kind}`
        ? Promise.resolve(
            reply({
              ...(kind === "endpoints"
                ? endpointOverviewFixture()
                : deliveryOverviewFixture()),
              total: { value: 10000, limited: true },
              states: undefined,
            }),
          )
        : load(path, ...args),
    );
    render(<Overview {...f.props} />);
    await screen.findByText("10,000+");
    expect(
      screen.getByText(
        kind === "endpoints"
          ? /Status breakdown unavailable/
          : /State breakdown unavailable/,
      ),
    ).toBeTruthy();
  },
);

it("shows empty populations and missing historical observations without proportions", async () => {
  const f = webhookAccess();
  f.get.mockImplementation(async (path) =>
    reply(
      path === "/api/v1/capabilities"
        ? {
            ...capabilities,
            optionalCapabilities: {
              ...capabilities.optionalCapabilities,
              webhooks: "enabled",
            },
          }
        : path === "/api/v1/overview/webhook-endpoints"
          ? {
              ...endpointOverviewFixture(),
              total: { value: 0, limited: false },
              states: [
                { state: "active", count: 0 },
                { state: "disabled", count: 0 },
              ],
            }
          : path === "/api/v1/overview/webhook-deliveries"
            ? {
                ...deliveryOverviewFixture(),
                total: { value: 0, limited: false },
                states: ["pending", "waiting", "succeeded", "dead_letter"].map(
                  (state) => ({ state, count: 0 }),
                ),
                failedEndpoints: [],
              }
            : {
                pendingEvents: 0,
                pendingEventLimit: 1000,
                capacityLossCount: 0,
                deliveryLossCount: 0,
              },
    ),
  );
  render(<Overview {...f.props} />);
  await screen.findByText("No deliveries to show.");
  expect(screen.getByText("No endpoints configured.")).toBeTruthy();
  expect(screen.queryByRole("table", { name: "Delivery failures" })).toBeNull();
  expect(
    screen.getByRole("region", { name: "Notification totals" }).textContent,
  ).not.toContain("None recorded");
});

it("labels singular populations and rejects malformed reads", async () => {
  const f = webhookAccess();
  f.get.mockImplementation(async (path) =>
    reply(
      path === "/api/v1/capabilities"
        ? {
            ...capabilities,
            optionalCapabilities: {
              ...capabilities.optionalCapabilities,
              webhooks: "enabled",
            },
          }
        : path === "/api/v1/overview/webhook-endpoints"
          ? {
              ...endpointOverviewFixture(),
              total: { value: 1, limited: false },
              states: [
                { state: "active", count: 1 },
                { state: "disabled", count: 0 },
              ],
            }
          : path === "/api/v1/overview/webhook-deliveries"
            ? {
                ...deliveryOverviewFixture(),
                total: { value: 1, limited: false },
                states: [
                  { state: "pending", count: 0 },
                  { state: "waiting", count: 1 },
                  { state: "succeeded", count: 0 },
                  { state: "dead_letter", count: 0 },
                ],
              }
            : { pendingEvents: -1 },
    ),
  );
  render(<Overview {...f.props} />);
  await screen.findByText("delivery", { exact: true });
  expect(screen.getByText(/endpoint$/)).toBeTruthy();
  expect(await screen.findByRole("alert")).toBeTruthy();
});

it("validates consumed summary values and independent limited failures", () => {
  const ep = endpointOverviewFixture(),
    delivery = deliveryOverviewFixture();
  expect(webhookEndpointOverview(ep, "tenant-a")).toBe(true);
  expect(webhookDeliveryOverview(delivery, "tenant-a")).toBe(true);
  expect(webhookEndpointOverview(ep, "other")).toBe(false);
  for (const change of [
    { total: { value: -1, limited: false } },
    {
      states: [
        { state: "active", count: 3 },
        { state: "active", count: 0 },
      ],
    },
    {
      states: [
        { state: "active", count: 2 },
        { state: "disabled", count: 0 },
      ],
    },
  ])
    expect(webhookEndpointOverview({ ...ep, ...change }, "tenant-a")).toBe(
      false,
    );
  expect(
    webhookDeliveryOverview(
      {
        ...delivery,
        total: { value: 0, limited: false },
        states: delivery.states!.map((row) => ({ ...row, count: 0 })),
      },
      "tenant-a",
    ),
  ).toBe(false);
  const limited = {
    ...delivery,
    total: { value: 10000, limited: true },
    states: undefined,
  };
  expect(webhookDeliveryOverview(limited, "tenant-a")).toBe(true);
  expect(
    webhookDeliveryOverview(
      { ...limited, states: delivery.states },
      "tenant-a",
    ),
  ).toBe(false);
  for (const failedEndpoints of [
    null,
    Array(6).fill(delivery.failedEndpoints[0]),
    [...delivery.failedEndpoints, ...delivery.failedEndpoints],
    [{}],
    [{ ...delivery.failedEndpoints[0], endpointId: "../escape" }],
    [{ ...delivery.failedEndpoints[0], deliveryId: "../escape" }],
    [{ ...delivery.failedEndpoints[0], state: "succeeded" }],
    [{ ...delivery.failedEndpoints[0], name: "" }],
    [{ ...delivery.failedEndpoints[0], name: "x".repeat(161) }],
    [{ ...delivery.failedEndpoints[0], attemptedAt: "invalid" }],
  ])
    expect(
      webhookDeliveryOverview({ ...delivery, failedEndpoints }, "tenant-a"),
    ).toBe(false);
});

it("keeps delivery history and loss counters when no endpoints remain", async () => {
  const f = webhookAccess(),
    endpoints = endpointOverviewFixture(),
    deliveries = deliveryOverviewFixture();
  endpoints.total.value = 0;
  endpoints.states!.forEach((row) => {
    row.count = 0;
  });
  deliveries.failedEndpoints = [];
  const load = f.get.getMockImplementation()!;
  f.get.mockImplementation((path, ...args) =>
    path === "/api/v1/overview/webhook-endpoints"
      ? Promise.resolve(reply(endpoints))
      : path === "/api/v1/overview/webhook-deliveries"
        ? Promise.resolve(reply(deliveries))
        : load(path, ...args),
  );
  render(<Overview {...f.props} />);
  await screen.findByText("No endpoints configured.");
  const history = await screen.findByRole("region", {
    name: "Delivery history",
  });
  expect(within(history).getByText("10")).toBeTruthy();
  expect(
    within(
      screen.getByRole("region", { name: "Notification totals" }),
    ).getByText("5"),
  ).toBeTruthy();
  expect(history.querySelector("details")).toBeNull();
  expect(f.get).toHaveBeenCalledTimes(4);
  expect(f.mutate).not.toHaveBeenCalled();
});
