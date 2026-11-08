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
  sourcesFixture,
  jobsFixture,
} from "../../tests/unit/operations-fixtures";
import { sourceOverview, jobOverview } from "./operations-overview-values";
import { sourceStatus, sourceDirectoryURL } from "./source-directory-state";
import Overview from "./Overview";

afterEach(() => {
  cleanup();
  history.replaceState(null, "", "/");
});
function operations() {
  const f = workspace();
  f.props.access = {
    ...f.props.access,
    permissions: ["source.read", "job.read", "operations.reindex"],
    availablePermissions: ["source.read", "job.read", "operations.reindex"],
    namespaceGrants: [],
    scopeEnforcement: true,
    scopes: ["sources.read", "catalog.read", "tenant.admin"],
  };
  f.get.mockImplementation(async (path: unknown) =>
    reply(
      path === "/api/v1/overview/sources"
        ? sourcesFixture()
        : path === "/api/v1/overview/jobs"
          ? jobsFixture()
          : capabilities,
    ),
  );
  history.replaceState(null, "", "/overview?view=operations");
  const mutate = vi.fn();
  Object.assign(f.props.client.api, {
    POST: mutate,
    PATCH: mutate,
    DELETE: mutate,
    PUT: mutate,
  });
  return { ...f, mutate };
}
it("renders authorized Operations reads, matching status/queue links and historical evidence", async () => {
  const f = operations();
  render(<Overview {...f.props} />);
  expect(await screen.findByText("Example source")).toBeTruthy();
  expect(
    screen.getAllByRole("link", { name: "Active" })[0]!.getAttribute("href"),
  ).toBe("/unit/sources?status=active");
  expect(screen.getByText(/Previous configuration: 1/)).toBeTruthy();
  const table = await screen.findByRole("table", {
    name: "Jobs by kind and state",
  });
  expect(
    within(table).getAllByRole("link", { name: "1" })[0]!.getAttribute("href"),
  ).toContain("kind=source.sync&state=queued");
  expect(
    screen
      .getByRole("link", { name: "Manage search indexing" })
      .getAttribute("href"),
  ).toBe("/unit/service#search-indexing");
  await waitFor(() => expect(f.get).toHaveBeenCalledTimes(3));
  expect(f.mutate).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() => expect(f.get).toHaveBeenCalledTimes(6));
});
it("defaults a Source-only reader to Operations without requiring catalog.read", async () => {
  const f = operations();
  f.props.access = {
    ...f.props.access,
    permissions: ["source.read"],
    availablePermissions: ["source.read"],
    scopes: ["sources.read"],
  };
  history.replaceState(null, "", "/overview");
  render(<Overview {...f.props} />);
  await screen.findByText("Example source");
  expect(f.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/overview/sources",
  ]);
  expect(screen.queryByRole("link", { name: "Content" })).toBeNull();
});
it("clears Operations data on permission loss and fences a late read", async () => {
  const f = operations(),
    pending = deferred<unknown>();
  f.get.mockReturnValue(pending.promise);
  const view = render(<Overview {...f.props} />);
  view.rerender(
    <Overview
      {...f.props}
      access={{ ...f.props.access, permissions: [], availablePermissions: [] }}
    />,
  );
  pending.resolve(reply(sourcesFixture()));
  await waitFor(() => expect(screen.queryByText("Example source")).toBeNull());
  expect(
    screen.queryByRole("region", { name: "Federation sources" }),
  ).toBeNull();
});
it("keeps Jobs usable after a failed source read and reloads only that panel", async () => {
  const f = operations();
  f.get.mockImplementation(async (path: unknown) => {
    if (path === "/api/v1/overview/sources")
      throw new Error("controlled failure");
    return reply(
      path === "/api/v1/overview/jobs" ? jobsFixture() : capabilities,
    );
  });
  render(<Overview {...f.props} />);
  await screen.findByRole("table", { name: "Jobs by kind and state" });
  const panel = screen.getByRole("region", { name: "Federation sources" });
  fireEvent.click(within(panel).getByRole("button", { name: "Reload" }));
  await waitFor(() => expect(f.get).toHaveBeenCalledTimes(4));
});
it.each(["disabled", "unavailable", "enabled"])(
  "preserves %s semantic state without invented counts",
  async (semantic) => {
    const f = operations();
    f.props.access = {
      ...f.props.access,
      permissions: ["operations.reindex"],
      availablePermissions: ["operations.reindex"],
    };
    f.get.mockResolvedValue(
      reply({
        ...capabilities,
        search: { ...capabilities.search, semantic, indexing: undefined },
      }),
    );
    render(<Overview {...f.props} />);
    if (semantic === "disabled")
      await screen.findByText(
        "Semantic search is off. Keyword search is available.",
      );
    else await screen.findByText(/Provider status \(last reported\)/);
    expect(
      screen.queryByRole("region", { name: "Indexed metadata" }),
    ).toBeNull();
    expect(f.get).toHaveBeenCalledTimes(1);
  },
);
it.each(["pending", "building", "ready", "failed"])(
  "renders %s processed counts on one scale",
  async (state) => {
    const f = operations();
    f.props.access = {
      ...f.props.access,
      permissions: ["operations.reindex"],
      availablePermissions: ["operations.reindex"],
    };
    f.get.mockResolvedValue(
      reply({
        ...capabilities,
        search: {
          ...capabilities.search,
          semantic: "enabled",
          providerState: "degraded",
          indexing: { state, indexed: 8, failed: 2 },
        },
      }),
    );
    render(<Overview {...f.props} />);
    const region = await screen.findByRole("region", {
      name: "Indexed metadata",
    });
    expect(
      [...region.querySelectorAll<HTMLElement>(".overview-track span")].map(
        (n) => n.style.width,
      ),
    ).toEqual(["100%", "25%"]);
    expect(region.textContent).toContain("8");
  },
);
it("keeps capped distributions unavailable while preserving problems and oldest due time", async () => {
  const f = operations(),
    sources = sourcesFixture(),
    jobs = jobsFixture();
  sources.total = { value: 10000, limited: true };
  delete sources.states;
  delete sources.sync;
  delete sources.historicalComplete;
  jobs.total = { value: 10000, limited: true };
  delete jobs.kinds;
  f.get.mockImplementation(async (path: unknown) =>
    reply(
      path === "/api/v1/overview/sources"
        ? sources
        : path === "/api/v1/overview/jobs"
          ? jobs
          : capabilities,
    ),
  );
  render(<Overview {...f.props} />);
  await screen.findByText("Example source");
  expect(screen.getByText(/Oldest due/)).toBeTruthy();
  expect(
    screen.queryByRole("table", { name: "Jobs by kind and state" }),
  ).toBeNull();
});
it("validates exact populations, consumed metadata and malformed status URLs", () => {
  expect(sourceOverview(sourcesFixture(), "tenant-a")).toBe(true);
  expect(jobOverview(jobsFixture(), "tenant-a")).toBe(true);
  for (const key of ["states", "sync"] as const) {
    const value = sourcesFixture();
    value[key]![0]!.count++;
    expect(sourceOverview(value, "tenant-a")).toBe(false);
  }
  const s = sourcesFixture();
  s.problems.push(s.problems[0]!);
  expect(sourceOverview(s, "tenant-a")).toBe(false);
  const j = jobsFixture();
  j.oldestDueAt = "2026-10-26T00:00:00Z";
  expect(jobOverview(j, "tenant-a")).toBe(false);
  expect(sourceOverview(sourcesFixture(), "other")).toBe(false);
  expect(jobOverview(jobsFixture(), "other")).toBe(false);
  for (const query of ["?status=", "?status=active&status=paused"]) {
    expect(sourceStatus(query)).toBe("unsupported");
  }
  expect(sourceStatus("?status=paused")).toBe("paused");
  expect(sourceStatus("")).toBe("");
  expect(sourceDirectoryURL("/unit", "error")).toBe(
    "/unit/sources?status=error",
  );
  expect(sourceDirectoryURL("", "")).toBe("/sources");
});

it.each(["omitted", "empty", "zero states"])(
  "rejects due Jobs without any retained Jobs when the distribution is %s",
  (distribution) => {
    const value = jobsFixture();
    value.total.value = 0;
    if (distribution === "omitted") delete value.kinds;
    else if (distribution === "empty") value.kinds = [];
    else
      value.kinds!.forEach((kind) =>
        kind.states.forEach((row) => (row.count = 0)),
      );
    expect(jobOverview(value, "tenant-a")).toBe(false);
    value.due.value = 0;
    delete value.oldestDueAt;
    expect(jobOverview(value, "tenant-a")).toBe(true);
  },
);

it("keeps state columns correct when a valid response orders states differently", async () => {
  const f = operations(),
    value = jobsFixture();
  value.kinds![0]!.states.reverse();
  f.get.mockImplementation(async (path: unknown) =>
    reply(
      path === "/api/v1/overview/jobs"
        ? value
        : path === "/api/v1/overview/sources"
          ? sourcesFixture()
          : capabilities,
    ),
  );
  render(<Overview {...f.props} />);
  const table = await screen.findByRole("table", {
    name: "Jobs by kind and state",
  });
  expect(
    within(table)
      .getAllByRole("cell")
      .map((cell) => cell.textContent),
  ).toEqual(["1", "0", "0", "0", "1", "0", "0"]);
});

it("shows complete empty sets as zero without a due timestamp or invented sync evidence", async () => {
  const f = operations(),
    sources = sourcesFixture(),
    jobs = jobsFixture();
  sources.total.value = 0;
  sources.states!.forEach((row) => (row.count = 0));
  sources.sync!.forEach((row) => (row.count = 0));
  sources.historicalComplete = 0;
  sources.problems = [];
  jobs.total.value = 0;
  jobs.due.value = 0;
  delete jobs.oldestDueAt;
  delete jobs.kinds;
  f.get.mockImplementation(async (path: unknown) =>
    reply(
      path === "/api/v1/overview/sources"
        ? sources
        : path === "/api/v1/overview/jobs"
          ? jobs
          : capabilities,
    ),
  );
  render(<Overview {...f.props} />);
  await screen.findByText("No sources configured.");
  expect(screen.getByText("No jobs to show.")).toBeTruthy();
  expect(screen.queryByText(/Oldest due/)).toBeNull();
  expect(screen.queryByRole("region", { name: "Source status" })).toBeNull();
  expect(
    screen.queryByRole("table", { name: "Jobs by kind and state" }),
  ).toBeNull();
});
it.each([true, false])(
  "separates same-revision complete evidence from no retained observation, present=%s",
  async (present) => {
    const f = operations(),
      value = sourcesFixture();
    value.problems[0]!.latest.state = "incomplete";
    if (present) value.problems[0]!.lastComplete!.revision = 2;
    else delete value.problems[0]!.lastComplete;
    f.get.mockImplementation(async (path: unknown) =>
      reply(
        path === "/api/v1/overview/sources"
          ? value
          : path === "/api/v1/overview/jobs"
            ? jobsFixture()
            : capabilities,
      ),
    );
    render(<Overview {...f.props} />);
    await screen.findByText("Example source");
    expect(screen.queryByText(/Historical:/)).toBeNull();
    if (!present) expect(screen.getByText("Not available")).toBeTruthy();
  },
);
it("ignores a valid late capability response after the indexing permission is removed", async () => {
  const f = operations(),
    pending = deferred<unknown>();
  f.props.access = {
    ...f.props.access,
    permissions: ["operations.reindex"],
    availablePermissions: ["operations.reindex"],
  };
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
    expect(screen.queryByText(/This browser received/)).toBeNull(),
  );
  expect(f.props.onFailure).not.toHaveBeenCalled();
});

it("uses singular Source labels and keeps count definitions available in a disclosure", async () => {
  const f = operations(),
    value = sourcesFixture();
  value.total.value = 1;
  value.states!.forEach((row) => (row.count = row.state === "error" ? 1 : 0));
  value.sync!.forEach((row) => (row.count = row.state === "failed" ? 1 : 0));
  f.get.mockImplementation(async (path: unknown) =>
    reply(
      path === "/api/v1/overview/sources"
        ? value
        : path === "/api/v1/overview/jobs"
          ? jobsFixture()
          : capabilities,
    ),
  );
  render(<Overview {...f.props} />);
  await screen.findByText("Example source");
  const panel = screen.getByRole("region", { name: "Federation sources" });
  expect(
    within(panel).getByText("1", { selector: ".overview-number" }),
  ).toBeTruthy();
  expect(panel.textContent).toContain("1 source still uses an earlier sync");
  expect(panel.querySelector("details")).toBeNull();
});

it("keeps zero historical evidence distinct from a plural current count after explicit refresh", async () => {
  const f = operations();
  f.props.access = {
    ...f.props.access,
    permissions: [...f.props.access.permissions, "resource.read"],
    availablePermissions: [
      ...f.props.access.availablePermissions,
      "resource.read",
    ],
  };
  let sources = sourcesFixture();
  sources.historicalComplete = 0;
  delete sources.problems[0]!.lastComplete;
  f.get.mockImplementation(async (path: unknown) => {
    if (path === "/api/v1/overview/sources") return reply(sources);
    if (path === "/api/v1/overview/jobs") return reply(jobsFixture());
    if (path === "/api/v1/capabilities") return reply(capabilities);
    throw new Error("Unexpected Operations Overview read");
  });
  render(<Overview {...f.props} />);
  await screen.findByText("Example source");
  const panel = screen.getByRole("region", { name: "Federation sources" });
  expect(within(panel).queryByText(/still uses? an earlier sync/)).toBeNull();
  expect(within(panel).getByText("Not available")).toBeTruthy();
  expect(
    screen.getByRole("link", { name: "Content" }).getAttribute("aria-current"),
  ).toBeNull();
  expect(
    screen
      .getByRole("link", { name: "Operations" })
      .getAttribute("aria-current"),
  ).toBe("page");
  expect(f.get.mock.calls.map(([path]) => path).sort()).toEqual([
    "/api/v1/capabilities",
    "/api/v1/overview/jobs",
    "/api/v1/overview/sources",
  ]);
  sources = { ...sourcesFixture(), historicalComplete: 2 };
  expect(f.get).toHaveBeenCalledTimes(3);
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  expect(
    await screen.findByText("2 sources still use an earlier sync."),
  ).toBeTruthy();
  expect(f.get).toHaveBeenCalledTimes(6);
  expect(
    f.get.mock.calls.filter(([path]) => path === "/api/v1/overview/resources"),
  ).toEqual([]);
  expect(f.props.onFailure).not.toHaveBeenCalled();
  expect(f.mutate).not.toHaveBeenCalled();
});
