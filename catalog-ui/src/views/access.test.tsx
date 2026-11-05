import { testAccess, testPermissions } from "../../tests/unit/access-fixtures";
import { required } from "../../tests/required";
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  renderHook,
  screen,
  within,
} from "@testing-library/react";
import Access from "./Access";
import { namespace } from "../../tests/unit/screens";
import { useNamespaceAccess } from "./namespace-access";
import { APIError } from "../api/client";
import {
  deferred,
  sessionAccess as reader,
  reply,
  workspace,
} from "../../tests/unit/fixtures";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("My access", () => {
  it("shows the authenticated identity, pages grants, and resets on identity change", async () => {
    const { props, get } = workspace();
    const access = {
      ...reader,
      nextCursor: "next",
      namespaceGrants: [
        {
          namespaceId: "00000000-0000-0000-0000-000000000010",
          permissions: testPermissions(["contributor" as const]),
        },
      ],
    };
    get.mockImplementation(async (path, options) =>
      path === "/api/v1/access/self"
        ? reply({ items: [], authorizationRevision: 1 })
        : path === "/api/v1/namespaces/{namespaceId}"
          ? reply({
              ...namespace,
              tenantId: reader.tenantId,
              id: (options as { params: { path: { namespaceId: string } } })
                .params.path.namespaceId,
              displayName: "Readable namespace",
            })
          : reply({
              ...reader,
              namespaceGrants: [
                {
                  namespaceId: "00000000-0000-0000-0000-000000000020",
                  permissions: testPermissions(["reviewer"]),
                },
              ],
            }),
    );
    const view = render(<Access {...props} access={access} />);
    const headings = screen
      .getAllByRole("heading", { level: 2 })
      .map((heading) => heading.textContent);
    expect(headings).toEqual(
      expect.arrayContaining(["Assignment sources", "Namespace grants"]),
    );
    expect(headings.indexOf("Assignment sources")).toBeLessThan(
      headings.indexOf("Namespace grants"),
    );
    expect(screen.getByText(reader.subject)).toBeTruthy();
    expect(screen.getByText(reader.tenantId)).toBeTruthy();
    expect(screen.getByText("resource.read")).toBeTruthy();
    expect(
      screen.getByText("Additional scope checks are disabled"),
    ).toBeTruthy();
    expect(
      get.mock.calls.filter(
        ([path]) =>
          path !== "/api/v1/access/self" &&
          path !== "/api/v1/namespaces/{namespaceId}",
      ),
    ).toEqual([]);
    expect(get).toHaveBeenCalledWith(
      "/api/v1/namespaces/{namespaceId}",
      expect.objectContaining({
        params: {
          path: { namespaceId: "00000000-0000-0000-0000-000000000010" },
        },
      }),
    );
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: "Next page" })),
    );
    expect(get).toHaveBeenCalledWith(
      "/api/v1/me",
      expect.objectContaining({ params: { query: { cursor: "next" } } }),
    );
    expect(
      screen.getByText("00000000-0000-0000-0000-000000000020"),
    ).toBeTruthy();
    expect(
      screen.queryByText("00000000-0000-0000-0000-000000000010"),
    ).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "First page" }));
    expect(
      screen.getByText("00000000-0000-0000-0000-000000000010"),
    ).toBeTruthy();
    view.rerender(
      <Access
        {...props}
        access={{
          ...reader,
          subject: "new-member",
          ...testAccess([]),
          scopeEnforcement: true,
        }}
      />,
    );
    expect(screen.getByText("No tenant-wide permissions")).toBeTruthy();
    expect(screen.getByText("None")).toBeTruthy();
    expect(screen.getByText("No namespace grants.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Next page" })).toBeNull();
    view.rerender(
      <Access
        {...props}
        access={{ ...reader, scopeEnforcement: true, scopes: ["catalog.read"] }}
      />,
    );
    expect(screen.getByText("catalog.read")).toBeTruthy();
  });

  it.each([new APIError(403, "denied"), new Error("private failure")])(
    "hides failed grant pages and allows return to the first page: %s",
    async (error) => {
      const { props, get } = workspace();
      const pending = deferred<unknown>();
      get.mockImplementation((path) =>
        path === "/api/v1/access/self"
          ? Promise.resolve(reply({ items: [], authorizationRevision: 1 }))
          : pending.promise,
      );
      render(<Access {...props} access={{ ...reader, nextCursor: "next" }} />);
      fireEvent.click(screen.getByRole("button", { name: "Next page" }));
      expect(screen.getByText("Loading grants...")).toBeTruthy();
      await act(async () => pending.reject(error));
      expect(screen.getByRole("alert").textContent).toBe(
        error instanceof APIError
          ? error.message
          : "Access could not be loaded.",
      );
      expect(props.onFailure).toHaveBeenCalledWith(error);
      expect(screen.queryByRole("button", { name: "Next page" })).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: "First page" }));
      expect(screen.queryByRole("alert")).toBeNull();
      expect(screen.getByRole("button", { name: "Next page" })).toBeTruthy();
    },
  );

  it.each(["resolve", "reject"] as const)(
    "discards stale grant-page %s after changing identity",
    async (outcome) => {
      const { props, get } = workspace();
      const pending = deferred<unknown>();
      get.mockImplementation((path) =>
        path === "/api/v1/access/self"
          ? Promise.resolve(reply({ items: [], authorizationRevision: 1 }))
          : pending.promise,
      );
      const view = render(
        <Access {...props} access={{ ...reader, nextCursor: "next" }} />,
      );
      fireEvent.click(screen.getByRole("button", { name: "Next page" }));
      const options = required(
        get.mock.calls.find(([path]) => path === "/api/v1/me"),
      )[1] as { signal: AbortSignal };
      view.rerender(
        <Access {...props} access={{ ...reader, subject: "new-identity" }} />,
      );
      expect(options.signal.aborted).toBe(true);
      await act(async () => {
        if (outcome === "resolve")
          pending.resolve(
            reply({
              ...reader,
              namespaceGrants: [
                {
                  namespaceId: "private-old",
                  permissions: testPermissions(["reviewer"]),
                },
              ],
            }),
          );
        else pending.reject(new Error("late"));
      });
      expect(screen.queryByText("private-old")).toBeNull();
      expect(screen.getByText("new-identity")).toBeTruthy();
      expect(props.onFailure).not.toHaveBeenCalled();
    },
  );
});

describe("exact namespace context", () => {
  it.each(["", "known", "no-more-pages"])(
    "uses existing context without scanning for %s",
    async (id) => {
      const { props, get } = workspace();
      const access = {
        ...reader,
        ...(id === "no-more-pages" ? {} : { nextCursor: "more" }),
        namespaceGrants: [
          {
            namespaceId: "known",
            permissions: testPermissions(["contributor" as const]),
          },
        ],
      };
      const hook = renderHook(() =>
        useNamespaceAccess({ ...props, access }, id),
      );
      await act(async () => {});
      expect(hook.result.current).toBe(access);
      expect(get).not.toHaveBeenCalled();
    },
  );
  it("requests only the selected missing namespace and never exposes an old selection", async () => {
    const { props, get } = workspace();
    const initial = { ...reader, nextCursor: "more" };
    const pending = deferred<unknown>();
    const resolved = {
      ...reader,
      namespaceGrants: [
        {
          namespaceId: "00000000-0000-0000-0000-000000000020",
          permissions: testPermissions(["contributor" as const]),
        },
      ],
    };
    get
      .mockReturnValueOnce(pending.promise)
      .mockResolvedValueOnce(reply(resolved));
    const hook = renderHook(
      ({ id }) => useNamespaceAccess({ ...props, access: initial }, id),
      { initialProps: { id: "00000000-0000-0000-0000-000000000010" } },
    );
    expect(hook.result.current).toBe(initial);
    hook.rerender({ id: "00000000-0000-0000-0000-000000000020" });
    await act(async () => {});
    expect(hook.result.current).toBe(resolved);
    expect(get.mock.calls.map((call) => call[1])).toEqual([
      expect.objectContaining({
        params: {
          query: { namespaceId: "00000000-0000-0000-0000-000000000010" },
        },
      }),
      expect.objectContaining({
        params: {
          query: { namespaceId: "00000000-0000-0000-0000-000000000020" },
        },
      }),
    ]);
    await act(async () =>
      pending.resolve(reply({ ...reader, ...testAccess(["tenant_admin"]) })),
    );
    expect(hook.result.current).toBe(resolved);
  });
});

it("pages direct/group assignment sources and explicitly refreshes current session hints", async () => {
  const { role, assignment } = await import("../../tests/unit/policy-fixtures");
  const { props, get } = workspace();
  get.mockImplementation(async (_path, options) =>
    reply({
      items: [
        {
          role: { ...role, tenantId: reader.tenantId },
          ...(options &&
          (options as { params: { query: { cursor?: string } } }).params.query
            .cursor
            ? { groupName: "Research group" }
            : {}),
          assignment:
            options &&
            (options as { params: { query: { cursor?: string } } }).params.query
              .cursor
              ? {
                  ...assignment,
                  tenantId: reader.tenantId,
                  realmId: reader.realmId,
                  membershipId: undefined,
                  groupId: "group-a",
                  namespaceId: "namespace-a",
                }
              : {
                  ...assignment,
                  tenantId: reader.tenantId,
                  realmId: reader.realmId,
                  membershipId: reader.membershipId,
                  namespaceId: undefined,
                },
        },
      ],
      authorizationRevision: 3,
      nextCursor:
        options &&
        (options as { params: { query: { cursor?: string } } }).params.query
          .cursor
          ? undefined
          : "sources-next",
    }),
  );
  const reload = vi.fn();
  vi.stubGlobal("location", { reload });
  render(<Access {...props} access={{ ...reader, ...testAccess([]) }} />);
  const sources = screen
    .getByRole("heading", { name: "Assignment sources" })
    .closest("section")!;
  await screen.findByText(/Direct member grant/);
  expect(within(sources).getByText("Permission meanings")).toBeTruthy();
  expect(within(sources).getByText(/Read resource metadata:/)).toBeTruthy();
  expect(
    within(sources).getByText(
      /Role permissions: namespace.read, resource.read/,
    ),
  ).toBeTruthy();
  expect(
    within(sources).getByText("Assignment attribution").closest("details")!
      .textContent,
  ).toContain(assignment.grantReason);
  expect(get.mock.calls.every(([path]) => path === "/api/v1/access/self")).toBe(
    true,
  );
  fireEvent.click(within(sources).getByRole("button", { name: "Next page" }));
  await screen.findByText(/Group Research group/);
  expect(within(sources).getByText("group-a")).toBeTruthy();
  expect(
    within(sources).getByText("Assignment attribution").closest("details")!
      .textContent,
  ).toContain("namespace-a");
  fireEvent.click(
    within(sources).getByRole("button", { name: "Refresh assignment sources" }),
  );
  await screen.findByText(/Direct member grant/);
  fireEvent.click(within(sources).getByRole("button", { name: "Next page" }));
  await screen.findByText(/Group Research group/);
  fireEvent.click(within(sources).getByRole("button", { name: "First page" }));
  await screen.findByText(/Direct member grant/);
  fireEvent.click(screen.getByRole("button", { name: "Refresh My access" }));
  expect(reload).toHaveBeenCalledTimes(1);
  vi.unstubAllGlobals();
});

it("refreshes cosmetic self captions without protected directory reads", async () => {
  const { role, assignment } = await import("../../tests/unit/policy-fixtures");
  const { props, get } = workspace();
  let caption = "Original visible team";
  get.mockImplementation(async () =>
    reply({
      items: [
        {
          role: { ...role, tenantId: reader.tenantId },
          assignment: {
            ...assignment,
            tenantId: reader.tenantId,
            realmId: reader.realmId,
            membershipId: undefined,
            groupId: "group-a",
          },
          groupName: caption,
        },
      ],
      authorizationRevision: 3,
    }),
  );
  render(<Access {...props} access={{ ...reader, ...testAccess([]) }} />);
  await screen.findByText(/Group Original visible team/);
  caption = "Renamed visible team";
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh assignment sources" }),
  );
  await screen.findByText(/Group Renamed visible team/);
  expect(screen.queryByText(/Group Original visible team/)).toBeNull();
  expect(get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/access/self",
    "/api/v1/access/self",
  ]);
});

it.each(["tenantId", "realmId", "membershipId"] as const)(
  "refuses self assignment evidence for a different %s",
  async (field) => {
    const { role, assignment } =
      await import("../../tests/unit/policy-fixtures");
    const { props, get } = workspace();
    const bound = {
      ...assignment,
      tenantId: reader.tenantId,
      realmId: reader.realmId,
      membershipId: reader.membershipId,
      [field]: "other-identity",
    };
    get.mockResolvedValue(
      reply({
        items: [
          { role: { ...role, tenantId: bound.tenantId }, assignment: bound },
        ],
        authorizationRevision: 3,
      }),
    );
    render(<Access {...props} access={{ ...reader, ...testAccess([]) }} />);
    const alert = await screen.findByRole("alert");
    expect(
      within(alert).getByText("This page could not be loaded."),
    ).toBeTruthy();
    expect(within(alert).getByRole("button", { name: "Reload" })).toBeTruthy();
    expect(screen.queryByText(/Direct member grant/)).toBeNull();
    expect(props.onFailure).toHaveBeenCalledWith(expect.any(Error));
  },
);

it("clears caption pages synchronously when identity changes and discards the old reply", async () => {
  const { props, get } = workspace();
  const pending = deferred<ReturnType<typeof reply>>();
  get
    .mockImplementationOnce(() => pending.promise)
    .mockResolvedValue(reply({ items: [], authorizationRevision: 3 }));
  const view = render(
    <Access {...props} access={{ ...reader, ...testAccess([]) }} />,
  );
  view.rerender(
    <Access
      {...props}
      access={{
        ...reader,
        ...testAccess([]),
        membershipId: "00000000-0000-0000-0000-000000000099",
      }}
    />,
  );
  const { role, assignment } = await import("../../tests/unit/policy-fixtures");
  await act(async () =>
    pending.resolve(
      reply({
        items: [
          {
            role: { ...role, tenantId: reader.tenantId },
            assignment: {
              ...assignment,
              tenantId: reader.tenantId,
              realmId: reader.realmId,
              membershipId: undefined,
              groupId: "group-a",
            },
            groupName: "Previous identity team",
          },
        ],
        authorizationRevision: 3,
      }),
    ),
  );
  expect(screen.queryByText(/Previous identity team/)).toBeNull();
  expect(get).toHaveBeenCalledTimes(2);
});

it("keeps an exact group reference when an optional self caption is unavailable", async () => {
  const { role, assignment } = await import("../../tests/unit/policy-fixtures");
  const { props, get } = workspace();
  get.mockResolvedValue(
    reply({
      items: [
        {
          role: { ...role, tenantId: reader.tenantId },
          assignment: {
            ...assignment,
            tenantId: reader.tenantId,
            realmId: reader.realmId,
            membershipId: undefined,
            groupId: "group-a",
            namespaceId: undefined,
          },
        },
      ],
      authorizationRevision: 3,
    }),
  );
  render(<Access {...props} access={{ ...reader, ...testAccess([]) }} />);
  await screen.findByText(/Group name unavailable/);
  expect(screen.getByText("group-a")).toBeTruthy();
  expect(get.mock.calls.map(([path]) => path)).toEqual(["/api/v1/access/self"]);
});

describe("assignment evidence after an access change", () => {
  const original: typeof reader = {
    ...reader,
    ...testAccess(["reader"]),
    authorizationRevision: 3,
    scopeEnforcement: true,
    scopes: ["catalog.read", "ratings.write"],
  };
  const changes: [string, typeof reader][] = [
    ["policy revision", { ...original, authorizationRevision: 4 }],
    ["OAuth ceiling", { ...original, scopes: ["catalog.read"] }],
    [
      "permissions",
      { ...original, permissions: ["namespace.read", "resource.read"] },
    ],
  ];

  it.each(
    changes.flatMap(([change, access]) =>
      (["loaded", "late success", "late failure"] as const).map((outcome) => ({
        change,
        access,
        outcome,
      })),
    ),
  )(
    "clears $outcome evidence after changing $change",
    async ({ access, outcome }) => {
      const { role, assignment } =
        await import("../../tests/unit/policy-fixtures");
      const { props, get } = workspace();
      const oldEvidence = {
        items: [
          {
            role: { ...role, tenantId: original.tenantId },
            assignment: {
              ...assignment,
              tenantId: original.tenantId,
              realmId: original.realmId,
              membershipId: undefined,
              namespaceId: undefined,
              groupId: "group-a",
            },
            groupName: "Previous policy team",
          },
        ],
        authorizationRevision: 3,
        nextCursor: "previous-policy-page",
      };
      const stale = deferred<ReturnType<typeof reply>>();
      const current = deferred<ReturnType<typeof reply>>();
      get.mockResolvedValueOnce(reply(oldEvidence));
      if (outcome !== "loaded") get.mockImplementationOnce(() => stale.promise);
      get.mockImplementationOnce(() => current.promise);
      const view = render(<Access {...props} access={original} />);
      await screen.findByText(/Group Previous policy team/);
      const sources = screen
        .getByRole("heading", { name: "Assignment sources" })
        .closest("section")!;
      if (outcome !== "loaded") {
        fireEvent.click(
          within(sources).getByRole("button", { name: "Next page" }),
        );
        expect(get).toHaveBeenLastCalledWith(
          "/api/v1/access/self",
          expect.objectContaining({
            params: { query: { limit: 25, cursor: "previous-policy-page" } },
          }),
        );
      }
      const previousOptions = required(get.mock.calls.at(-1))[1] as {
        signal: AbortSignal;
      };
      view.rerender(<Access {...props} access={access} />);
      expect(previousOptions.signal.aborted).toBe(true);
      // This assertion runs while the replacement read is still pending.
      expect(screen.queryByText(/Previous policy team/)).toBeNull();
      expect(get).toHaveBeenLastCalledWith(
        "/api/v1/access/self",
        expect.objectContaining({ params: { query: { limit: 25 } } }),
      );
      await act(async () => {
        current.resolve(
          reply({
            items: oldEvidence.items.map((item) => ({
              ...item,
              assignment: {
                ...item.assignment,
                id: "assignment-current",
                groupId: "group-current",
              },
              groupName: "Current policy team",
            })),
            authorizationRevision: access.authorizationRevision,
          }),
        );
      });
      await screen.findByText(/Group Current policy team/);
      await act(async () => {
        if (outcome === "late success") stale.resolve(reply(oldEvidence));
        if (outcome === "late failure")
          stale.reject(new Error("Obsolete policy read"));
      });
      expect(screen.getByText(/Group Current policy team/)).toBeTruthy();
      expect(screen.queryByText(/Previous policy team/)).toBeNull();
      expect(screen.queryByRole("button", { name: "Next page" })).toBeNull();
      expect(screen.queryByRole("button", { name: "First page" })).toBeNull();
      expect(screen.queryByRole("alert")).toBeNull();
      expect(props.onFailure).not.toHaveBeenCalled();
      expect(get.mock.calls.map(([path]) => path)).toEqual(
        Array(outcome === "loaded" ? 2 : 3).fill("/api/v1/access/self"),
      );
    },
  );
});

it.each(["tenantId", "realmId", "membershipId"] as const)(
  "does not adopt a namespace permission page from another %s",
  async (field) => {
    const { props, get } = workspace();
    const foreignId = "00000000-0000-0000-0000-000000000099";
    get.mockImplementation(async (path) =>
      path === "/api/v1/access/self"
        ? reply({ items: [], authorizationRevision: 1 })
        : reply({ ...reader, [field]: foreignId }),
    );
    render(<Access {...props} access={{ ...reader, nextCursor: "next" }} />);
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    expect((await screen.findByRole("alert")).textContent).toBe(
      "Access could not be loaded.",
    );
    expect(screen.queryByText(foreignId)).toBeNull();
    expect(props.onFailure).toHaveBeenCalledWith(expect.any(Error));
  },
);

it.each([
  [
    "reader",
    [
      "resource.read",
      "resource.read_history",
      "resource.read_content",
      "namespace.read",
    ],
    "Explore local catalog",
    "Discovery publication",
  ],
  [
    "contributor",
    [
      "resource.read",
      "resource.read_history",
      "resource.read_content",
      "namespace.read",
      "resource.create",
      "resource.edit",
      "resource.validate",
      "resource.delete_draft",
      "review.submit",
    ],
    "Authoring",
    "Publishers",
  ],
  [
    "reviewer",
    [
      "resource.read",
      "resource.read_history",
      "resource.read_content",
      "namespace.read",
      "review.decide",
    ],
    "Reviews",
    "Discovery publication",
  ],
  [
    "publisher",
    [
      "resource.read",
      "resource.read_history",
      "resource.read_content",
      "namespace.read",
      "release.publish",
      "release.transition",
    ],
    "Reviews",
    "Discovery publication",
  ],
  ["tenant_admin", testPermissions(["tenant_admin"]), "Roles", ""],
  [
    "discovery_reader",
    testPermissions(["discovery_reader"]),
    "Catalog",
    "Explore local catalog",
  ],
  [
    "publisher_creator",
    testPermissions(["publisher_creator"]),
    "Publishers",
    "Authoring",
  ],
  [
    "publication_admin",
    testPermissions(["publication_admin"]),
    "Discovery publication",
    "Authoring",
  ],
  [
    "source_admin",
    testPermissions(["source_admin"]),
    "Federation sources",
    "Authoring",
  ],
  [
    "security_reviewer",
    testPermissions(["security_reviewer"]),
    "Audit",
    "Catalog",
  ],
  ["webhook_admin", testPermissions(["webhook_admin"]), "Webhooks", "Catalog"],
  [
    "feedback_member",
    testPermissions(["feedback_member"]),
    "My feedback",
    "Catalog",
  ],
  [
    "library_member",
    testPermissions(["library_member"]),
    "Saved items and searches",
    "Catalog",
  ],
  ["service_client", [], "API reference", "My feedback"],
] as const)(
  "shows useful permission-based task links for the exact %s template without companions",
  async (template, permissions, allowed, denied) => {
    const { props, get } = workspace();
    get.mockResolvedValue(reply({ items: [], authorizationRevision: 1 }));
    render(
      <Access
        {...props}
        access={{
          ...reader,
          subject: template,
          actorType: template === "service_client" ? "service" : "human",
          permissions: [...permissions],
          availablePermissions: [...permissions],
        }}
      />,
    );
    const tasks = screen.getByRole("navigation", { name: "Available tasks" });
    expect(within(tasks).getByRole("link", { name: allowed })).toBeTruthy();
    if (denied)
      expect(within(tasks).queryByRole("link", { name: denied })).toBeNull();
    expect(
      screen.getByText(
        template === "service_client" ? "Service (nonhuman)" : "Human",
      ),
    ).toBeTruthy();
    await act(async () => {});
    expect(get).toHaveBeenCalledTimes(1);
    expect(get).toHaveBeenCalledWith(
      "/api/v1/access/self",
      expect.objectContaining({ params: { query: { limit: 25 } } }),
    );
  },
);

it("marks readable authoring and reviews without claiming write authority", async () => {
  const { props, get } = workspace();
  get.mockResolvedValue(reply({ items: [], authorizationRevision: 1 }));
  render(<Access {...props} access={reader} />);
  const tasks = screen.getByRole("navigation", { name: "Available tasks" });
  expect(
    within(tasks).getByRole("link", {
      name: "Authoring Read-only",
    }),
  ).toBeTruthy();
  expect(
    within(tasks).getByRole("link", { name: "Reviews Read-only" }),
  ).toBeTruthy();
});

it.each([
  ["human", false, [], true],
  ["human", true, ["catalog.read"], true],
  ["human", true, ["ratings.write"], false],
  ["service", false, [], false],
] as const)(
  "keeps baseline human task eligibility separate from grants and scopes: %s %s",
  (actorType, scopeEnforcement, scopes, personal) => {
    const { props, get } = workspace();
    get.mockResolvedValue(reply({ items: [], authorizationRevision: 1 }));
    render(
      <Access
        {...props}
        access={{
          ...reader,
          ...testAccess([]),
          actorType,
          scopeEnforcement,
          scopes: [...scopes],
        }}
      />,
    );
    const tasks = screen.getByRole("navigation", { name: "Available tasks" });
    for (const name of ["My work", "My ratings"])
      expect(within(tasks).queryByRole("link", { name }) !== null).toBe(
        personal,
      );
    for (const name of ["My feedback", "Saved items and searches"])
      expect(within(tasks).queryByRole("link", { name })).toBeNull();
    expect(
      within(tasks).getByRole("link", {
        name: "Service information",
      }),
    ).toBeTruthy();
  },
);

it("explains the empty template while preserving baseline human tasks", async () => {
  const { role, assignment } = await import("../../tests/unit/policy-fixtures");
  const { props, get } = workspace();
  get.mockResolvedValue(
    reply({
      items: [
        {
          role: {
            ...role,
            tenantId: reader.tenantId,
            key: "service_client",
            builtin: true,
            permissions: [],
          },
          assignment: {
            ...assignment,
            tenantId: reader.tenantId,
            realmId: reader.realmId,
            membershipId: reader.membershipId,
            namespaceId: undefined,
          },
        },
      ],
      authorizationRevision: 3,
    }),
  );
  render(<Access {...props} access={{ ...reader, ...testAccess([]) }} />);
  await screen.findByText(/The empty template supplies no product permissions/);
  expect(screen.queryByText("Permission meanings")).toBeNull();
  expect(
    screen.getByRole("navigation", { name: "Available tasks" }).textContent,
  ).toContain("My ratings");
});
