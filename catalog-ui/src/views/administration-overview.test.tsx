// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  render,
  screen,
  within,
  waitFor,
  fireEvent,
} from "@testing-library/react";
import { workspace, reply, deferred } from "../../tests/unit/fixtures";
import {
  administrationFixture,
  feedbackFixture,
  ratingFixture,
} from "../../tests/unit/administration-fixtures";
import {
  administrationOverview,
  feedbackOverview,
  ratingOverview,
} from "./administration-overview-values";
import {
  administrationDirectory,
  administrationParameter,
  directorySearch,
  moderationSearch,
} from "./administration-filter-state";
import Overview from "./Overview";
import { capabilities } from "../../tests/unit/responses";

afterEach(() => {
  cleanup();
  history.replaceState(null, "", "/");
});
type Permissions = ReturnType<
  typeof workspace
>["props"]["access"]["permissions"];
function administration(
  permissions: Permissions = [
    "access.manage",
    "feedback.moderate",
    "ratings.moderate",
  ],
) {
  const f = workspace();
  const selected = permissions as typeof f.props.access.permissions;
  f.props.access = {
    ...f.props.access,
    permissions: selected,
    availablePermissions: selected,
    namespaceGrants: [],
    scopeEnforcement: true,
    scopes: ["tenant.admin"],
  };
  f.get.mockImplementation(async (path: unknown) => {
    if (path === "/api/v1/capabilities") return reply(capabilities);
    if (path === "/api/v1/overview/administration")
      return reply(administrationFixture());
    if (path === "/api/v1/overview/feedback") return reply(feedbackFixture());
    if (path === "/api/v1/overview/ratings") return reply(ratingFixture());
    throw new Error("Unexpected private read");
  });
  const mutate = vi.fn();
  Object.assign(f.props.client.api, {
    POST: mutate,
    PATCH: mutate,
    PUT: mutate,
    DELETE: mutate,
  });
  history.replaceState(null, "", "/unit/overview");
  return { ...f, mutate };
}

it("defaults specialist access to Administration and links exact counted populations", async () => {
  const f = administration();
  render(<Overview {...f.props} />);
  await screen.findByText(/14 built-in; 4 custom/);
  expect(
    screen
      .getByRole("link", { name: "Administration" })
      .getAttribute("aria-current"),
  ).toBe("page");
  expect(screen.queryByRole("link", { name: "Content" })).toBeNull();
  expect(screen.queryByRole("link", { name: "Operations" })).toBeNull();
  const human = screen.getByRole("row", {
    name: /^People/,
  });
  expect(within(human).getByText("5")).toBeTruthy();
  expect(
    within(human)
      .getAllByRole("link", { name: "People: Suspended: 1" })[0]!
      .getAttribute("href"),
  ).toBe("/unit/members?state=suspended&actorType=human");
  const service = screen.getByRole("row", {
    name: /^Service identities/,
  });
  expect(within(service).getByText("2")).toBeTruthy();
  expect(
    screen
      .getByRole("link", { name: "Manage assignments" })
      .getAttribute("href"),
  ).toBe("/unit/access-assignments");
  const feedback = screen.getByRole("region", {
    name: "Open feedback",
  });
  expect(
    within(feedback)
      .getAllByRole("link", { name: "Problem" })[0]!
      .getAttribute("href"),
  ).toBe("/unit/feedback?state=open&category=problem");
  const ratings = screen.getByRole("region", {
    name: "Ratings",
  });
  expect(
    within(ratings)
      .getAllByRole("link", { name: "Removed" })[0]!
      .getAttribute("href"),
  ).toBe("/unit/ratings?state=removed");
  await waitFor(() => expect(f.get).toHaveBeenCalledTimes(4));
  expect(f.mutate).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() => expect(f.get).toHaveBeenCalledTimes(8));
});
it.each([
  ["access.manage", "Catalog access", "administration"],
  ["feedback.moderate", "Open feedback", "feedback"],
  ["ratings.moderate", "Ratings", "ratings"],
])(
  "%s reads only its permitted summary without catalog.read",
  async (permission, title, path) => {
    const f = administration([permission as Permissions[number]]);
    render(<Overview {...f.props} />);
    const panel = await screen.findByRole("region", {
      name: title,
    });
    await waitFor(() => expect(within(panel).queryByRole("status")).toBeNull());
    expect(f.get.mock.calls.map(([path]) => path)).toEqual([
      `/api/v1/overview/${path}`,
      ...(permission === "access.manage" ? ["/api/v1/capabilities"] : []),
    ]);
  },
);
it("refuses personal permissions, missing scopes and navigation-only hints", async () => {
  const f = administration(["feedback.own", "ratings.own"]);
  const view = render(<Overview {...f.props} />);
  expect(screen.getByRole("alert").textContent).toContain(
    "does not include Overview",
  );
  expect(f.get).not.toHaveBeenCalled();
  for (const access of [
    {
      ...f.props.access,
      permissions: ["feedback.moderate" as const],
      scopes: ["catalog.read" as const],
    },
    {
      ...f.props.access,
      permissions: [],
      availablePermissions: ["feedback.moderate" as const],
    },
  ])
    view.rerender(<Overview {...f.props} access={access} />);
  expect(f.get).not.toHaveBeenCalled();
});
it("keeps other panels on failure and retries only the failed summary", async () => {
  const f = administration(),
    load = f.get.getMockImplementation()!;
  let failures = 0;
  f.get.mockImplementation((path, ...args) =>
    path === "/api/v1/overview/feedback" && failures++ === 0
      ? Promise.reject(new Error("unavailable"))
      : load(path, ...args),
  );
  render(<Overview {...f.props} />);
  const panel = screen.getByRole("region", {
    name: "Open feedback",
  });
  await within(panel).findByRole("alert");
  expect(await screen.findByText(/14 built-in/)).toBeTruthy();
  fireEvent.click(within(panel).getByRole("button", { name: "Reload" }));
  await within(panel).findByText("6", { exact: true });
  expect(within(panel).queryByRole("alert")).toBeNull();
  expect(f.get).toHaveBeenCalledTimes(5);
  expect(f.mutate).not.toHaveBeenCalled();
});
it("discards late private summaries after permission loss", async () => {
  const f = administration(),
    pending = deferred<ReturnType<typeof reply>>();
  f.get.mockReturnValue(pending.promise);
  const view = render(<Overview {...f.props} />);
  await waitFor(() => expect(f.get).toHaveBeenCalledTimes(4));
  view.rerender(
    <Overview
      {...f.props}
      access={{ ...f.props.access, permissions: [], availablePermissions: [] }}
    />,
  );
  pending.resolve(reply(administrationFixture()));
  await waitFor(() =>
    expect(screen.queryByRole("region", { name: "Catalog access" })).toBeNull(),
  );
  expect(f.props.onFailure).not.toHaveBeenCalled();
});
it("keeps independently complete membership counts and the oldest time beside limited measures", async () => {
  const f = administration(),
    a = administrationFixture(),
    feedback = feedbackFixture(),
    r = ratingFixture();
  a.human = { total: { value: 10000, limited: true } };
  a.activeRoles = { total: { value: 10000, limited: true } };
  a.activeGroups = { value: 10000, limited: true };
  a.activeAssignments = { value: 10000, limited: true };
  feedback.open = { value: 10000, limited: true };
  delete feedback.categories;
  r.total = { value: 10000, limited: true };
  delete r.states;
  f.get.mockImplementation(async (path) =>
    reply(
      path === "/api/v1/overview/administration"
        ? a
        : path === "/api/v1/overview/feedback"
          ? feedback
          : r,
    ),
  );
  render(<Overview {...f.props} />);
  await screen.findByText(
    /Breakdown unavailable: more than 10,000 memberships/,
  );
  expect(screen.getByText(/More than 10,000 roles/)).toBeTruthy();
  expect(screen.getByText(/More than 10,000 active groups/)).toBeTruthy();
  expect(
    screen.getByText(/More than 10,000 active role assignments/),
  ).toBeTruthy();
  expect(screen.getByText(/Open feedback exceeds/)).toBeTruthy();
  expect(screen.getByText(/Ratings exceed/)).toBeTruthy();
  expect(screen.getByRole("row", { name: /^Service identities/ })).toBeTruthy();
  expect(screen.getByText(/Oldest submission/)).toBeTruthy();
});
it("shows empty sets with no invented oldest time", async () => {
  const f = administration(),
    a = administrationFixture(),
    feedback = feedbackFixture(),
    r = ratingFixture();
  for (const type of ["human", "service"] as const) {
    a[type].total.value = 0;
    a[type].states!.forEach((row) => (row.count = 0));
  }
  a.activeRoles = {
    total: { value: 0, limited: false },
    builtin: 0,
    custom: 0,
  };
  a.activeGroups.value = 0;
  a.activeAssignments.value = 0;
  feedback.open.value = 0;
  feedback.categories!.forEach((row) => (row.count = 0));
  delete feedback.oldestOpenAt;
  r.total.value = 0;
  r.states!.forEach((row) => (row.count = 0));
  f.get.mockImplementation(async (path) =>
    reply(
      path === "/api/v1/overview/administration"
        ? a
        : path === "/api/v1/overview/feedback"
          ? feedback
          : r,
    ),
  );
  render(<Overview {...f.props} />);
  const table = await screen.findByRole("table", { name: "Catalog members" });
  for (const row of within(table).getAllByRole("row").slice(1))
    expect(
      within(row)
        .getAllByRole("cell")
        .map((cell) => cell.textContent),
    ).toEqual(["0", "0", "0", "0"]);
  expect(screen.getByText("No open feedback.")).toBeTruthy();
  expect(screen.getByText("No ratings to show.")).toBeTruthy();
  expect(screen.queryByText(/Oldest submission/)).toBeNull();
});
it("labels single memberships, submissions and ratings", async () => {
  const f = administration(),
    a = administrationFixture(),
    feedback = feedbackFixture(),
    rating = ratingFixture();
  for (const type of ["human", "service"] as const) {
    a[type].total.value = 1;
    a[type].states!.forEach(
      (row) => (row.count = row.state === "active" ? 1 : 0),
    );
  }
  feedback.open.value = 1;
  feedback.categories!.forEach(
    (row) => (row.count = row.category === "problem" ? 1 : 0),
  );
  rating.total.value = 1;
  rating.states!.forEach((row) => (row.count = row.state === "active" ? 1 : 0));
  f.get.mockImplementation(async (path) =>
    reply(
      path === "/api/v1/overview/administration"
        ? a
        : path === "/api/v1/overview/feedback"
          ? feedback
          : rating,
    ),
  );
  render(<Overview {...f.props} />);
  await screen.findByText("open submission");
  expect(screen.getByRole("row", { name: /^People/ }).textContent).toContain(
    "1",
  );
  expect(
    screen.getByRole("row", { name: /^Service identities/ }).textContent,
  ).toContain("1");
  expect(screen.getByText("rating")).toBeTruthy();
});

it("validates consumed counts, complete distributions and independent limits", () => {
  const a = administrationFixture(),
    f = feedbackFixture(),
    r = ratingFixture();
  expect(administrationOverview(a, "tenant-a")).toBe(true);
  expect(feedbackOverview(f, "tenant-a")).toBe(true);
  expect(ratingOverview(r, "tenant-a")).toBe(true);
  for (const value of [
    null,
    { ...a, tenantId: "other" },
    { ...a, human: null },
    { ...a, service: { total: { value: -1, limited: false } } },
    {
      ...a,
      human: { total: { value: 10000, limited: true }, states: a.human.states },
    },
    { ...a, human: { ...a.human, states: [{ state: "active", count: 5 }] } },
    {
      ...a,
      activeGroups: { value: Number.MAX_SAFE_INTEGER + 1, limited: false },
    },
    { ...a, activeAssignments: null },
    { ...a, activeRoles: null },
    {
      ...a,
      activeRoles: { total: { value: 10000, limited: true }, builtin: 14 },
    },
    { ...a, activeRoles: { ...a.activeRoles, custom: 5 } },
    { ...a, activeRoles: { ...a.activeRoles, builtin: -1 } },
  ])
    expect(administrationOverview(value, "tenant-a")).toBe(false);
  const limited = { value: 10000, limited: true };
  expect(
    administrationOverview(
      { ...a, human: { total: limited }, activeRoles: { total: limited } },
      "tenant-a",
    ),
  ).toBe(true);
  for (const value of [
    null,
    { ...f, open: { value: -1, limited: false } },
    { ...f, oldestOpenAt: undefined },
    { ...f, oldestOpenAt: "invalid" },
    { ...f, open: { value: 0, limited: false } },
    { ...f, open: limited },
    { ...f, categories: null },
    { ...f, categories: [{}] },
    {
      ...f,
      categories: f.categories!.map((row) => ({ ...row, category: "problem" })),
    },
  ])
    expect(feedbackOverview(value, "tenant-a")).toBe(false);
  expect(
    feedbackOverview(
      { ...f, open: limited, categories: undefined },
      "tenant-a",
    ),
  ).toBe(true);
  for (const value of [
    null,
    { ...r, total: null },
    { ...r, total: limited },
    { ...r, states: r.states!.map((row) => ({ ...row, state: "active" })) },
    { ...r, states: r.states!.map((row) => ({ ...row, count: 0 })) },
  ])
    expect(ratingOverview(value, "tenant-a")).toBe(false);
  expect(
    ratingOverview({ ...r, total: limited, states: undefined }, "tenant-a"),
  ).toBe(true);
});
it("keeps URL filters exact without serializing private directory fields", () => {
  expect(administrationParameter("", "state")).toBe("");
  expect(administrationParameter("?state=", "state")).toBe("unsupported");
  expect(administrationParameter("?state=active&state=active", "state")).toBe(
    "unsupported",
  );
  expect(
    administrationDirectory("?state=suspended&actorType=service", true),
  ).toEqual({ state: "suspended", actorType: "service" });
  expect(administrationDirectory("?state=ACTIVE&actorType=service")).toEqual({
    state: "ACTIVE",
  });
  expect(
    directorySearch({
      state: "active",
      actorType: "human",
      subject: "private",
      realmId: "private",
      name: "private",
    }),
  ).toBe("?state=active&actorType=human");
  expect(directorySearch({})).toBe("");
  expect(moderationSearch({ state: "open", category: "problem" })).toBe(
    "?state=open&category=problem",
  );
  expect(moderationSearch({ state: "" })).toBe("");
});

it("keeps membership states in their labeled columns when the response reorders them", async () => {
  const f = administration(),
    value = administrationFixture();
  value.human.states!.reverse();
  const load = f.get.getMockImplementation()!;
  f.get.mockImplementation((path, ...args) =>
    path === "/api/v1/overview/administration"
      ? Promise.resolve(reply(value))
      : load(path, ...args),
  );
  render(<Overview {...f.props} />);
  const table = await screen.findByRole("table", { name: "Catalog members" });
  const row = within(table).getByRole("row", { name: /^People/ });
  expect(
    within(row)
      .getAllByRole("cell")
      .map((cell) => cell.textContent),
  ).toEqual(["5", "3", "1", "1"]);
  expect(
    within(row)
      .getByRole("link", { name: "People: Active: 3" })
      .getAttribute("href"),
  ).toBe("/unit/members?state=active&actorType=human");
});
