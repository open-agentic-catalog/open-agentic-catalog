// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { workspace, reply, deferred } from "../../tests/unit/fixtures";
import { activityFixture } from "../../tests/unit/activity-fixture";
import { capabilities } from "../../tests/unit/responses";
import { APIError } from "../api/client";
import ActivityOverview from "./ActivityOverview";
import Overview from "./Overview";

async function settle() {
  await act(async () => {
    await Promise.resolve();
  });
}
function clock() {
  vi.useFakeTimers();
  vi.spyOn(performance, "now").mockImplementation(() => Date.now());
}
async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}
function visible(hidden: boolean) {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    value: hidden ? "hidden" : "visible",
  });
  fireEvent(document, new Event("visibilitychange"));
}
afterEach(() => {
  cleanup();
  visible(false);
  vi.useRealTimers();
  vi.restoreAllMocks();
  history.replaceState(null, "", "/");
});

it("shows exact counters, all three breakdowns, process identity and separate HTTP/Search/MCP populations", async () => {
  const f = workspace();
  const data = activityFixture(9007199254740993n);
  data.mcp.enabled = true;
  f.get.mockResolvedValue(reply(data));
  render(<ActivityOverview {...f.props} />);
  await settle();
  expect(
    within(screen.getByRole("region", { name: "Catalog requests" })).getByText(
      9007199254740993n.toLocaleString(),
    ),
  ).toBeTruthy();
  expect(
    screen
      .getByRole("link", { name: "Service information" })
      .getAttribute("href"),
  ).toBe("/unit/service");
  const select = screen.getByRole("combobox", {
    name: "Break down requests by",
  });
  fireEvent.change(select, { target: { value: "method" } });
  expect(screen.getByRole("rowheader", { name: "GET" })).toBeTruthy();
  fireEvent.change(select, { target: { value: "status" } });
  expect(screen.getByRole("rowheader", { name: "4xx" })).toBeTruthy();
  fireEvent.change(select, { target: { value: "family" } });
  expect(
    screen.getByRole("rowheader", { name: "Catalog search" }),
  ).toBeTruthy();
  expect(
    screen.getByRole("region", { name: "MCP operation outcomes" }),
  ).toBeTruthy();
  fireEvent.click(screen.getByText("Responding process"));
  expect(screen.getByText(data.instanceId)).toBeTruthy();
  expect(f.get).toHaveBeenCalledTimes(1);
});
it("uses matching samples for rates and means, ages observations monotonically and clears stale pairing", async () => {
  clock();
  const f = workspace();
  const next = activityFixture(6n, 20);
  next.http.families[0]!.durationSeconds = 0.06;
  f.get
    .mockResolvedValueOnce(reply(activityFixture()))
    .mockResolvedValueOnce(reply(next))
    .mockResolvedValue(reply(activityFixture(8n, 30)));
  render(<ActivityOverview {...f.props} />);
  await settle();
  await advance(10000);
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await settle();
  expect(
    within(
      screen.getByRole("region", { name: "Requests per second" }),
    ).getByText("0.2"),
  ).toBeTruthy();
  expect(screen.getByText("Measured over 10 seconds.")).toBeTruthy();
  await advance(30000);
  expect(screen.getByText(/Out of date/)).toBeTruthy();
  expect(
    within(
      screen.getByRole("region", { name: "Requests per second" }),
    ).getByText("Not available"),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await settle();
  expect(screen.queryByText(/Measured over/)).toBeNull();
});
it("defaults to manual reads, schedules after completion, prevents overlap and resets the timer after manual refresh", async () => {
  clock();
  const f = workspace();
  const pending = deferred<ReturnType<typeof reply>>();
  f.get
    .mockResolvedValueOnce(reply(activityFixture()))
    .mockImplementationOnce(() => pending.promise)
    .mockResolvedValue(reply(activityFixture(8n, 30)));
  render(<ActivityOverview {...f.props} />);
  await settle();
  await advance(20000);
  expect(f.get).toHaveBeenCalledTimes(1);
  const toggle = screen.getByRole("checkbox");
  fireEvent.click(toggle);
  await advance(10000);
  expect(f.get).toHaveBeenCalledTimes(2);
  expect(
    (screen.getByRole("button", { name: "Refresh" }) as HTMLButtonElement)
      .disabled,
  ).toBe(true);
  await advance(20000);
  expect(f.get).toHaveBeenCalledTimes(2);
  await act(async () => pending.resolve(reply(activityFixture(6n, 20))));
  await advance(5000);
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await settle();
  expect(f.get).toHaveBeenCalledTimes(3);
  await advance(5000);
  expect(f.get).toHaveBeenCalledTimes(3);
  await advance(5000);
  expect(f.get).toHaveBeenCalledTimes(4);
  fireEvent.click(toggle);
  await advance(20000);
  expect(f.get).toHaveBeenCalledTimes(4);
});
it.each([
  new APIError(403, "FORBIDDEN"),
  new APIError(503, "DEPENDENCY_UNAVAILABLE"),
  new Error("private upstream text"),
])(
  "clears samples and pauses after failure until explicit reload",
  async (failure) => {
    clock();
    const f = workspace();
    const onPause = vi.fn();
    f.get
      .mockResolvedValueOnce(reply(activityFixture()))
      .mockRejectedValueOnce(failure)
      .mockResolvedValue(reply(activityFixture()));
    render(<ActivityOverview {...f.props} onPause={onPause} />);
    await settle();
    fireEvent.click(screen.getByRole("checkbox"));
    await advance(10000);
    expect(screen.getByRole("alert").textContent).not.toContain(
      "private upstream text",
    );
    expect(
      screen.queryByRole("region", { name: "Catalog requests" }),
    ).toBeNull();
    expect(f.props.onFailure).toHaveBeenCalledWith(failure);
    expect(onPause).toHaveBeenCalledWith(true);
    await advance(30000);
    expect(f.get).toHaveBeenCalledTimes(2);
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    await settle();
    expect(screen.getByText("MCP is turned off.")).toBeTruthy();
    expect(onPause).toHaveBeenLastCalledWith(false);
  },
);
it("does not render malformed successful samples", async () => {
  const f = workspace();
  f.get.mockResolvedValue(reply({ ...activityFixture(), http: {} }));
  render(<ActivityOverview {...f.props} />);
  await settle();
  expect(screen.getByRole("alert")).toBeTruthy();
  expect(screen.queryByRole("table")).toBeNull();
});
it("aborts pending work on hide, ignores late replies, resumes manually and clears the rate pair", async () => {
  clock();
  const f = workspace();
  const pending = deferred<ReturnType<typeof reply>>();
  const initial = activityFixture();
  f.get
    .mockResolvedValueOnce(reply(initial))
    .mockImplementationOnce(() => pending.promise)
    .mockResolvedValue(reply(activityFixture(6n, 20)));
  render(<ActivityOverview {...f.props} />);
  await settle();
  fireEvent.click(screen.getByRole("checkbox"));
  await advance(10000);
  const options = f.get.mock.calls[1]![1] as { signal: AbortSignal };
  visible(true);
  expect(options.signal.aborted).toBe(true);
  await act(async () => pending.resolve(reply(activityFixture(999n, 25))));
  expect(screen.queryByText("999")).toBeNull();
  await advance(20000);
  visible(false);
  await advance(10000);
  expect(f.get).toHaveBeenCalledTimes(2);
  expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(
    false,
  );
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await settle();
  expect(f.get).toHaveBeenCalledTimes(3);
  expect(screen.queryByText(/Measured over/)).toBeNull();
});
it("waits for explicit refresh if mounted hidden and aborts without accepting work after unmount", async () => {
  clock();
  visible(true);
  const f = workspace();
  const pending = deferred<ReturnType<typeof reply>>();
  f.get.mockImplementation(() => pending.promise);
  const view = render(<ActivityOverview {...f.props} />);
  await settle();
  expect(f.get).not.toHaveBeenCalled();
  visible(false);
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  const signal = (f.get.mock.calls[0]![1] as { signal: AbortSignal }).signal;
  view.unmount();
  expect(signal.aborted).toBe(true);
  await act(async () => pending.reject(new Error("late")));
  expect(f.props.onFailure).not.toHaveBeenCalled();
});
it("fences equal-looking access epochs, A-to-B-to-A transitions and source changes", async () => {
  const f = workspace();
  const old = deferred<ReturnType<typeof reply>>();
  f.get
    .mockImplementationOnce(() => old.promise)
    .mockResolvedValue(reply(activityFixture(8n, 20)));
  const original = f.props.access;
  const view = render(<ActivityOverview {...f.props} />);
  view.rerender(<ActivityOverview {...f.props} access={{ ...original }} />);
  await settle();
  expect(screen.getByRole("button", { name: "Reload" })).toBeTruthy();
  view.rerender(<ActivityOverview {...f.props} access={original} />);
  await act(async () => old.resolve(reply(activityFixture(999n))));
  expect(screen.queryByText("999")).toBeNull();
  expect(f.get).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await settle();
  expect(screen.getByRole("region", { name: "Catalog requests" })).toBeTruthy();
  view.rerender(
    <ActivityOverview
      {...f.props}
      runtime={{ ...f.props.runtime, basePath: "/other" }}
    />,
  );
  expect(screen.queryByRole("region", { name: "Catalog requests" })).toBeNull();
});
it("clears immediately when the client's identity lifetime aborts", async () => {
  const f = workspace();
  const identity = new AbortController();
  Object.defineProperty(f.props.client, "signal", {
    get: () => identity.signal,
  });
  f.get.mockResolvedValue(reply(activityFixture()));
  render(<ActivityOverview {...f.props} />);
  await settle();
  act(() => identity.abort());
  expect(screen.queryByRole("region", { name: "Catalog requests" })).toBeNull();
});
function overview(enabled: "enabled" | "disabled" = "enabled") {
  const f = workspace();
  f.props.access = {
    ...f.props.access,
    permissions: ["access.manage"],
    availablePermissions: ["access.manage"],
  };
  f.get.mockImplementation(async (path) =>
    reply(
      path === "/api/v1/capabilities"
        ? {
            ...capabilities,
            optionalCapabilities: {
              ...capabilities.optionalCapabilities,
              serviceActivity: enabled,
            },
          }
        : activityFixture(),
    ),
  );
  history.replaceState(null, "", "/overview?view=activity");
  return f;
}
it("gates Activity by configured availability and current tenant permission before reading it", async () => {
  const f = overview();
  const view = render(<Overview {...f.props} />);
  await settle();
  expect(
    screen.getByRole("link", { name: "Activity" }).getAttribute("aria-current"),
  ).toBe("page");
  expect(f.get.mock.calls.map((v) => v[0])).toEqual([
    "/api/v1/capabilities",
    "/api/v1/service-activity",
  ]);
  // A denied read must not restart when session reconciliation returns equal hints.
  f.get.mockRejectedValueOnce(new APIError(403, "FORBIDDEN"));
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await settle();
  view.rerender(<Overview {...f.props} access={{ ...f.props.access }} />);
  await settle();
  expect(screen.queryByRole("region", { name: "Catalog requests" })).toBeNull();
  expect(
    f.get.mock.calls.filter((v) => v[0] === "/api/v1/service-activity"),
  ).toHaveLength(2);
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await settle();
  expect(screen.getByRole("region", { name: "Catalog requests" })).toBeTruthy();
  expect(
    f.get.mock.calls.filter((v) => v[0] === "/api/v1/service-activity"),
  ).toHaveLength(3);
});
it.each(["?view=unknown", "?view=activity&view=activity"])(
  "does not request Activity for invalid selections %s",
  async (search) => {
    const f = overview();
    history.replaceState(null, "", "/overview" + search);
    render(<Overview {...f.props} />);
    await settle();
    expect(screen.getByRole("alert")).toBeTruthy();
    expect(f.get.mock.calls.map((v) => v[0])).toEqual(["/api/v1/capabilities"]);
  },
);
it.each(["disabled", "reader", "grantless", "missing-scope"])(
  "does not request unavailable Activity for %s",
  async (condition) => {
    const f = overview(condition === "disabled" ? "disabled" : "enabled");
    if (condition === "reader") f.props.access = workspace().props.access;
    if (condition === "grantless")
      f.props.access = {
        ...f.props.access,
        permissions: [],
        availablePermissions: [],
        actorType: "service",
      };
    if (condition === "missing-scope")
      f.props.access = {
        ...f.props.access,
        scopeEnforcement: true,
        scopes: ["catalog.read"],
      };
    render(<Overview {...f.props} />);
    await settle();
    expect(screen.getByRole("alert")).toBeTruthy();
    expect(
      f.get.mock.calls.some((v) => v[0] === "/api/v1/service-activity"),
    ).toBe(false);
  },
);
it("shares the administrator capability read with Operations without fetching Activity", async () => {
  const f = overview();
  f.props.access = {
    ...f.props.access,
    permissions: ["access.manage", "operations.reindex"],
    availablePermissions: ["access.manage", "operations.reindex"],
  };
  history.replaceState(null, "", "/overview?view=operations");
  render(<Overview {...f.props} />);
  await settle();
  expect(f.get.mock.calls.map((v) => v[0])).toEqual(["/api/v1/capabilities"]);
  expect(screen.getByRole("link", { name: "Activity" })).toBeTruthy();
});
it("retries failed or malformed availability without disclosing activity and preserves default Administration", async () => {
  const f = overview();
  f.get.mockResolvedValueOnce(
    reply({
      ...capabilities,
      optionalCapabilities: {
        ...capabilities.optionalCapabilities,
        serviceActivity: "unavailable",
      },
    }),
  );
  render(<Overview {...f.props} />);
  await settle();
  expect(screen.getByRole("button", { name: "Reload" })).toBeTruthy();
  expect(
    f.get.mock.calls.some((v) => v[0] === "/api/v1/service-activity"),
  ).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await settle();
  expect(screen.getByRole("region", { name: "Catalog requests" })).toBeTruthy();
});
it("rejects hidden or invalidated refreshes and discards stale pairs before the next age tick", async () => {
  clock();
  const f = workspace();
  const identity = new AbortController();
  Object.defineProperty(f.props.client, "signal", {
    get: () => identity.signal,
  });
  f.get.mockResolvedValue(reply(activityFixture()));
  render(<ActivityOverview {...f.props} />);
  await settle();
  vi.setSystemTime(Date.now() + 30000);
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await settle();
  expect(screen.queryByText(/Measured over/)).toBeNull();
  visible(true);
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  expect(f.get).toHaveBeenCalledTimes(2);
  visible(false);
  act(() => identity.abort());
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  expect(f.get).toHaveBeenCalledTimes(2);
});
it("refreshes shared indexing observations with the administrator Operations page", async () => {
  const f = overview();
  f.props.access = {
    ...f.props.access,
    permissions: ["access.manage", "operations.reindex"],
    availablePermissions: ["access.manage", "operations.reindex"],
  };
  history.replaceState(null, "", "/overview?view=operations");
  const data = {
    ...capabilities,
    search: {
      ...capabilities.search,
      semantic: "enabled",
      indexing: { state: "pending", indexed: 0, failed: 0 },
    },
  };
  f.get.mockResolvedValueOnce(reply(data)).mockResolvedValueOnce(
    reply({
      ...data,
      search: {
        ...data.search,
        indexing: { state: "ready", indexed: 12, failed: 0 },
      },
    }),
  );
  render(<Overview {...f.props} />);
  await settle();
  const panel = screen.getByRole("region", { name: "Search indexing" });
  expect(within(panel).getByText("Pending")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await settle();
  expect(
    within(screen.getByRole("region", { name: "Search indexing" })).getByText(
      "ready",
    ),
  ).toBeTruthy();
  expect(f.get.mock.calls.map((v) => v[0])).toEqual([
    "/api/v1/capabilities",
    "/api/v1/capabilities",
  ]);
});

it("keeps observation age monotonic when the wall clock moves backward", async () => {
  vi.useFakeTimers();
  let monotonic = 0;
  vi.spyOn(performance, "now").mockImplementation(() => monotonic);
  const f = workspace();
  f.get.mockResolvedValue(reply(activityFixture()));
  render(<ActivityOverview {...f.props} />);
  await settle();
  vi.setSystemTime(new Date("2000-01-01T00:00:00Z"));
  monotonic = 31000;
  await advance(1000);
  expect(screen.getByText(/Out of date/)).toBeTruthy();
  expect(f.get).toHaveBeenCalledTimes(1);
});
