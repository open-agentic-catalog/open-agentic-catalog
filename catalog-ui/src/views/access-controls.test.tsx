import { revealEditor } from "../../tests/unit/reveal-editor";
// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import {
  AccessReference,
  DirectoryFilters,
  GrantPreview,
  MemberContributions,
  BuiltinRoleHelp,
} from "./AccessControls";
import { ChoiceField } from "./authoring";
import { memberAccess } from "./access-responses";
import Authorization from "./Authorization";
import { OperationProvider } from "./operation-context";
import {
  role,
  group,
  assignment,
  member as relation,
  descriptors,
} from "../../tests/unit/policy-fixtures";
import {
  administrator,
  change as baseChange,
  click as baseClick,
  confirmCommand as baseConfirmCommand,
  etagReply,
  namespace,
  readReplies,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";
function change(name: string, value: string) {
  revealEditor(screen.getByLabelText(name));
  baseChange(name, value);
}
function click(name: string | RegExp) {
  const target = screen.getAllByText(name, { selector: "button" })[0];
  if (target) revealEditor(target);
  baseClick(name);
}
async function confirmCommand(name: string) {
  const target = screen.getAllByText(name, { selector: "button" })[0];
  if (target) revealEditor(target);
  await baseConfirmCommand(name);
}
import { deferred } from "../../tests/unit/fixtures";

screenLifecycle("/unit/access-roles");

it("shows help only for a verified builtin and preserves its actual scope eligibility", () => {
  const view = render(
    <BuiltinRoleHelp role={{ ...role, builtin: true, key: "reader" }} />,
  );
  expect(
    screen.getByText(/metadata, history and retained content/),
  ).toBeTruthy();
  expect(
    screen.getByText(
      /Allowed assignment scopes: tenant-wide or exact namespace/,
    ),
  ).toBeTruthy();
  view.rerender(
    <BuiltinRoleHelp
      role={{
        ...role,
        builtin: true,
        key: "source_admin",
        namespaceCapable: false,
      }}
    />,
  );
  expect(
    screen.getByText(/Allowed assignment scope: tenant-wide only/),
  ).toBeTruthy();
  view.rerender(
    <BuiltinRoleHelp
      role={{
        ...role,
        builtin: true,
        key: "service_client",
        namespaceCapable: true,
      }}
      namespaceEligible={false}
    />,
  );
  expect(
    screen.getByText(/Allowed assignment scope: tenant-wide only/),
  ).toBeTruthy();
  view.rerender(<BuiltinRoleHelp role={{ ...role, key: "reader" }} />);
  expect(
    screen.queryByRole("region", { name: "Built-in template help" }),
  ).toBeNull();
});
const human = {
  id: "member-a",
  tenantId: "tenant-a",
  realmId: "realm-a",
  subject: "Exact Human",
  actorType: "human" as const,
  state: "active" as const,
  revision: 1,
  createdAt: role.createdAt,
  updatedAt: role.updatedAt,
};
const direct = {
  assignment: { ...assignment, namespaceId: namespace.id },
  role,
  effective: true,
};
const groupPath = {
  assignment: {
    ...assignment,
    id: "group-assignment",
    membershipId: undefined,
    groupId: group.id,
    namespaceId: undefined,
    state: "revoked" as const,
  },
  role,
  group: { ...group, state: "suspended" as const },
  relation,
  effective: false,
};
const contributionPage = {
  items: [direct, groupPath],
  membership: human,
  authorizationRevision: 3,
  groupCoverage: "current_realm" as const,
};

function setup() {
  const w = screenWorkspace();
  w.props.access = administrator;
  readReplies(w.get, {
    "/api/v1/access/roles": { items: [role], authorizationRevision: 3 },
    "/api/v1/access/roles/{roleId}": role,
    "/api/v1/access/permissions": descriptors,
    "/api/v1/access/assignments": { items: [], authorizationRevision: 3 },
    "/api/v1/access/groups": { items: [group], authorizationRevision: 3 },
    "/api/v1/access/groups/{groupId}": group,
    "/api/v1/access/groups/{groupId}/members": {
      items: [],
      authorizationRevision: 3,
    },
    "/api/v1/memberships": { items: [human] },
    "/api/v1/memberships/{membershipId}": human,
    "/api/v1/memberships/{membershipId}/access": contributionPage,
    "/api/v1/namespaces": { items: [namespace] },
    "/api/v1/namespaces/{namespaceId}": namespace,
  });
  return w;
}

it.each(["members", "roles", "groups"] as const)(
  "submits and clears exact %s filters without changing spelling",
  (kind) => {
    const apply = vi.fn();
    render(
      <DirectoryFilters
        members={kind === "members"}
        groups={kind === "groups"}
        onApply={apply}
      />,
    );
    change(kind === "members" ? "Exact subject" : "Exact name", "Exact Value");
    change("State", "active");
    if (kind === "members") {
      change("Identity type filter", "service");
      change("Exact realm ID", "realm-a");
    }
    click("Apply filters");
    expect(apply).toHaveBeenLastCalledWith(
      kind === "members"
        ? {
            subject: "Exact Value",
            state: "active",
            actorType: "service",
            realmId: "realm-a",
          }
        : { name: "Exact Value", state: "active" },
    );
    click("Clear filters");
    expect(apply).toHaveBeenLastCalledWith({});
    expect(screen.getByLabelText("State")).toHaveProperty("value", "");
    click("Apply filters");
    expect(apply).toHaveBeenLastCalledWith({});
  },
);

it("preserves directory drafts when discarding another editor is refused", () => {
  const apply = vi.fn();
  render(<DirectoryFilters onApply={apply} />);
  change("Exact name", "Keep me");
  const block = (event: Event) => event.preventDefault();
  window.addEventListener("catalog-ui:leave", block);
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  try {
    click("Apply filters");
    click("Clear filters");
    expect(apply).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Exact name")).toHaveProperty(
      "value",
      "Keep me",
    );
    expect(confirm).toHaveBeenCalledTimes(2);
  } finally {
    window.removeEventListener("catalog-ui:leave", block);
  }
});

it.each(["role", "member", "group", "namespace"] as const)(
  "resolves only the selected %s and refreshes cosmetic metadata without a policy revision",
  async (kind) => {
    const w = setup();
    const record = { role, member: human, group, namespace }[kind];
    const token = {};
    const view = render(
      <AccessReference
        workspace={w.props}
        kind={kind}
        id={record.id}
        refreshToken={token}
      />,
    );
    await waitFor(() =>
      expect(
        screen.queryByText(new RegExp(`${kind} name unavailable`)),
      ).toBeNull(),
    );
    expect(w.get).toHaveBeenCalledTimes(1);
    const updated =
      kind === "member"
        ? { ...human, subject: "Renamed human" }
        : kind === "namespace"
          ? { ...namespace, displayName: "Renamed namespace" }
          : { ...record, name: "Renamed metadata" };
    w.get.mockResolvedValue(etagReply(updated));
    view.rerender(
      <AccessReference
        workspace={w.props}
        kind={kind}
        id={record.id}
        refreshToken={{}}
      />,
    );
    await screen.findByText(/Renamed/);
    expect(w.get).toHaveBeenCalledTimes(2);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["role", "group", "namespace"] as const)(
  "keeps an honest ID fallback for an unavailable %s directory",
  async (kind) => {
    const w = screenWorkspace();
    const access = {
      ...administrator,
      permissions: [],
      availablePermissions: [],
      namespaceGrants: [],
    };
    render(
      <AccessReference
        workspace={{ ...w.props, access }}
        kind={kind}
        id="known-id"
      />,
    );
    await screen.findByText(new RegExp(`${kind} name unavailable`));
    expect(w.get).not.toHaveBeenCalled();
  },
);

it("does not request a known foreign-realm group caption", () => {
  const w = screenWorkspace();
  render(
    <AccessReference
      workspace={{ ...w.props, access: administrator }}
      kind="group"
      id={group.id}
      realmId="other-realm"
    />,
  );
  expect(w.get).not.toHaveBeenCalled();
});

it.each(["id", "tenant", "realm"] as const)(
  "refuses a mismatched reference %s before displaying its name",
  async (mismatch) => {
    const w = setup();
    w.get.mockResolvedValue(
      etagReply({
        ...group,
        ...(mismatch === "id"
          ? { id: "wrong" }
          : mismatch === "tenant"
            ? { tenantId: "foreign" }
            : { realmId: "foreign" }),
      }),
    );
    render(<AccessReference workspace={w.props} kind="group" id={group.id} />);
    await waitFor(() => expect(w.props.onFailure).toHaveBeenCalled());
    expect(screen.queryByText(group.name)).toBeNull();
    expect(screen.getByText(group.id)).toBeTruthy();
  },
);

it("clears a private caption synchronously when directory permission is lost", async () => {
  const w = setup();
  const view = render(
    <AccessReference workspace={w.props} kind="role" id={role.id} />,
  );
  await screen.findByText(new RegExp(role.name));
  view.rerender(
    <AccessReference
      workspace={{
        ...w.props,
        access: { ...administrator, permissions: [], availablePermissions: [] },
      }}
      kind="role"
      id={role.id}
    />,
  );
  expect(screen.queryByText(new RegExp(role.name))).toBeNull();
  await act(async () => {});
  expect(w.get).toHaveBeenCalledTimes(1);
});

it("checks exact namespace permission and its OAuth ceiling before requesting a caption", async () => {
  const w = setup();
  const scoped = {
    ...administrator,
    permissions: [],
    availablePermissions: ["namespace.read" as const],
    namespaceGrants: [
      { namespaceId: namespace.id, permissions: ["namespace.read" as const] },
    ],
    scopeEnforcement: true,
    scopes: ["catalog.read" as const],
  };
  const view = render(
    <AccessReference
      workspace={{ ...w.props, access: scoped }}
      kind="namespace"
      id={namespace.id}
    />,
  );
  await screen.findByText(new RegExp(namespace.displayName));
  expect(w.get).toHaveBeenCalledTimes(1);
  view.rerender(
    <AccessReference
      workspace={{ ...w.props, access: { ...scoped, scopes: [] } }}
      kind="namespace"
      id={namespace.id}
    />,
  );
  expect(screen.queryByText(new RegExp(namespace.displayName))).toBeNull();
  await act(async () => {});
  expect(w.get).toHaveBeenCalledTimes(1);
});

it("explains route-specific empty-template eligibility and unverified references", () => {
  const view = render(
    <GrantPreview
      role={{
        ...role,
        key: "service_client",
        permissions: [],
        namespaceCapable: true,
      }}
      namespaceEligible={false}
      scope="tenant"
      namespaceId=""
    />,
  );
  expect(screen.getByText(/no product permissions/)).toBeTruthy();
  expect(screen.getByText("Tenant-wide only.")).toBeTruthy();
  expect(screen.getByText(/baseline human/)).toBeTruthy();
  view.rerender(<GrantPreview role={role} scope="namespace" namespaceId="" />);
  expect(screen.getByText(/choose an existing namespace/)).toBeTruthy();
  expect(screen.getByText("Namespace or tenant eligible.")).toBeTruthy();
  view.rerender(
    <GrantPreview role={role} scope="namespace" namespaceId={namespace.id} />,
  );
  expect(screen.getByText(`Exact namespace: ${namespace.id}.`)).toBeTruthy();
  view.rerender(<GrantPreview scope="" namespaceId="" />);
  expect(screen.getByText(/Role details are unverified/)).toBeTruthy();
  expect(screen.getByText(/Choose Tenant-wide/)).toBeTruthy();
});

it("filters a bounded picker on button or Enter while retaining the selected ID", async () => {
  const load = vi.fn(
    async (cursor: string, _signal: AbortSignal, search: string) => ({
      items: cursor
        ? [{ id: "later", label: "Later" }]
        : search
          ? []
          : [{ id: "selected", label: "Selected" }],
      ...(!cursor ? { nextCursor: "next" } : {}),
    }),
  );
  const select = vi.fn();
  render(
    <ChoiceField
      label="Test choice"
      searchLabel="Exact choice"
      value="selected"
      onChange={select}
      load={load}
      onFailure={vi.fn()}
    />,
  );
  await screen.findByRole("option", { name: "Selected" });
  change("Exact choice", "Exact typed case");
  click("Apply choice filter");
  await waitFor(() =>
    expect(load.mock.calls.at(-1)?.[2]).toBe("Exact typed case"),
  );
  expect(screen.getByLabelText("Test choice")).toHaveProperty(
    "value",
    "selected",
  );
  fireEvent.keyDown(screen.getByLabelText("Exact choice"), { key: "a" });
  change("Exact choice", "Other exact case");
  fireEvent.keyDown(screen.getByLabelText("Exact choice"), { key: "Enter" });
  await waitFor(() =>
    expect(load.mock.calls.at(-1)?.[2]).toBe("Other exact case"),
  );
  click("Next page");
  await screen.findByRole("option", { name: "Later" });
  expect(load.mock.calls.at(-1)?.[0]).toBe("next");
  expect(load.mock.calls.at(-1)?.[2]).toBe("Other exact case");
  expect(select).not.toHaveBeenCalled();
});

it("pages direct/group contributions, distinguishes lifecycle eligibility and refreshes names", async () => {
  const w = setup();
  const view = render(<MemberContributions {...w.props} memberId={human.id} />);
  await screen.findByText("Eligible assignment path", { exact: false });
  expect(
    screen.getByText(/Currently ineffective assignment path/),
  ).toBeTruthy();
  expect(screen.getByText(/An active relation alone/)).toBeTruthy();
  expect(screen.getByText(/Current policy revision: 3/)).toBeTruthy();
  expect(
    screen.getAllByRole("link", { name: "Inspect assignment path" }),
  ).toHaveLength(2);
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation(async (path, options) =>
    path === "/api/v1/memberships/{membershipId}/access"
      ? etagReply({
          ...contributionPage,
          items: [{ ...direct, role: { ...role, name: "Fresh role caption" } }],
        })
      : original(path, options),
  );
  click("Refresh member contributions");
  await screen.findByText("Fresh role caption");
  expect(w.post).not.toHaveBeenCalled();
  view.unmount();
});

it("restarts failed contribution pagination and renders empty foreign-realm coverage honestly", async () => {
  const w = setup();
  let fail = false;
  w.get.mockImplementation(async (_path, options) => {
    const cursor = (options as { params: { query: { cursor?: string } } })
      .params.query.cursor;
    if (cursor && fail) throw new Error("owned page failure");
    return etagReply({
      ...contributionPage,
      items: [],
      membership: { ...human, realmId: "foreign" },
      groupCoverage: "unavailable",
      nextCursor: "next",
    });
  });
  render(<MemberContributions {...w.props} memberId={human.id} />);
  await screen.findByText(/No retained contributions/);
  expect(
    screen.getByText(/unavailable in another identity realm/),
  ).toBeTruthy();
  fail = true;
  click("Next page");
  await screen.findByRole("alert");
  click("Reload");
  await screen.findByText(/No retained contributions/);
  fail = false;
  click("Next page");
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Refresh member contributions" }),
    ).toHaveProperty("disabled", false),
  );
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { cursor: "next" } },
  });
  click("Refresh member contributions");
  await waitFor(() =>
    expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { limit: 25 } },
    }),
  );
});

it("refuses a contribution response for another target or realm coverage", async () => {
  const w = setup();
  w.get.mockResolvedValue(
    etagReply({
      ...contributionPage,
      items: [],
      membership: { ...human, id: "other-member" },
    }),
  );
  render(<MemberContributions {...w.props} memberId={human.id} />);
  await screen.findByRole("alert");
  expect(screen.queryByText(role.name)).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("validates nested contribution references and state before they drive display", () => {
  expect(memberAccess(contributionPage)).toBe(true);
  const invalid = [
    null,
    {},
    { ...contributionPage, membership: { ...human, tenantId: undefined } },
    { ...contributionPage, groupCoverage: "invented" },
    { ...contributionPage, items: [{ ...direct, effective: "true" }] },
    {
      ...contributionPage,
      items: [
        { ...direct, assignment: { ...direct.assignment, tenantId: "other" } },
      ],
    },
    {
      ...contributionPage,
      items: [{ ...direct, role: { ...role, id: "wrong" } }],
    },
    {
      ...contributionPage,
      items: [
        {
          ...direct,
          assignment: { ...direct.assignment, membershipId: "wrong" },
        },
      ],
    },
    {
      ...contributionPage,
      items: [
        {
          ...direct,
          effective: true,
          assignment: { ...direct.assignment, state: "revoked" },
        },
      ],
    },
    {
      ...contributionPage,
      items: [
        { ...direct, assignment: { ...direct.assignment, realmId: "foreign" } },
      ],
    },
    { ...contributionPage, items: [{ ...groupPath, relation: undefined }] },
    {
      ...contributionPage,
      items: [
        { ...groupPath, relation: { ...relation, membershipId: "wrong" } },
      ],
    },
    { ...contributionPage, items: [{ ...groupPath, effective: true }] },
    { ...contributionPage, groupCoverage: "unavailable", items: [groupPath] },
  ];
  for (const value of invalid) expect(memberAccess(value)).toBe(false);
});

it.each([false, true])(
  "copies a currently inspected role to a new custom role without changing its source (protected=%s)",
  async (protectedSource) => {
    const w = setup();
    const source = {
      ...role,
      builtin: protectedSource,
      permissions: protectedSource
        ? (["resource.read", "access.manage"] as typeof role.permissions)
        : role.permissions,
    };
    const original = w.get.getMockImplementation()!;
    w.get.mockImplementation(async (path, options) =>
      path === "/api/v1/access/roles/{roleId}"
        ? etagReply(source)
        : original(path, options),
    );
    history.replaceState(null, "", "/unit/access-roles/role-a");
    w.post.mockResolvedValue(
      etagReply({
        ...role,
        id: "new-custom",
        name: "New inspected copy",
        permissions: source.permissions.filter((p) => p !== "access.manage"),
      }),
    );
    render(<Authorization {...w.props} id={role.id} />);
    await screen.findByRole("button", { name: "Copy to custom role" });
    click("Copy to custom role");
    await screen.findByRole("heading", { name: "Create role" });
    if (protectedSource) {
      expect(
        screen.getByRole("button", { name: "Create role" }),
      ).toHaveProperty("disabled", true);
      fireEvent.click(screen.getByLabelText(/I acknowledge/));
    }
    change("Role name", "New inspected copy");
    change("Metadata change reason", "Separate role, no assignments");
    await confirmCommand("Create role");
    await screen.findByRole("link", { name: "Open saved role" });
    expect(w.post).toHaveBeenCalledExactlyOnceWith(
      "/api/v1/access/roles",
      expect.objectContaining({
        body: {
          name: "New inspected copy",
          description: source.description,
          permissions: source.permissions.filter((p) => p !== "access.manage"),
          reason: "Separate role, no assignments",
        },
      }),
    );
    expect(w.patch).not.toHaveBeenCalled();
    expect(w.remove).not.toHaveBeenCalled();
    expect(source.permissions.includes("access.manage")).toBe(protectedSource);
  },
);

it("retains explicit companion validation in a role copy and lets the user cancel the private draft", async () => {
  const w = setup();
  const source = {
    ...role,
    permissions: [
      "namespace.read",
      "resource.create",
    ] as typeof role.permissions,
  };
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation(async (path, options) =>
    path === "/api/v1/access/roles/{roleId}"
      ? etagReply(source)
      : original(path, options),
  );
  history.replaceState(null, "", "/unit/access-roles/role-a");
  render(<Authorization {...w.props} id={role.id} />);
  await screen.findByRole("button", { name: "Copy to custom role" });
  click("Copy to custom role");
  await screen.findByRole("heading", { name: "Create role" });
  fireEvent.click(screen.getByLabelText("namespace.read"));
  expect(screen.getByText(/Required companions missing/)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Create role" })).toHaveProperty(
    "disabled",
    true,
  );
  vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Cancel role copy");
  expect(screen.getByRole("heading", { name: "Create role" })).toBeTruthy();
  vi.spyOn(window, "confirm").mockReturnValue(true);
  click("Cancel role copy");
  await screen.findByRole("heading", { name: "Update role" });
  expect(w.post).not.toHaveBeenCalled();
  expect(w.patch).not.toHaveBeenCalled();
});

it("does not render another identity's late contribution response", async () => {
  const w = setup();
  const late = deferred<ReturnType<typeof etagReply>>();
  w.get.mockReturnValueOnce(late.promise).mockResolvedValue(
    etagReply({
      ...contributionPage,
      items: [],
      membership: { ...human, id: "different-member" },
    }),
  );
  const view = render(<MemberContributions {...w.props} memberId={human.id} />);
  view.rerender(
    <MemberContributions {...w.props} memberId="different-member" />,
  );
  await screen.findByText(/No retained contributions/);
  await act(async () => late.resolve(etagReply(contributionPage)));
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.queryByText(role.name)).toBeNull();
});

it("captures exact permission additions, removals and scope changes before a role update", async () => {
  const w = setup();
  history.replaceState(null, "", "/unit/access-roles/role-a");
  render(<Authorization {...w.props} id={role.id} />);
  await screen.findByLabelText("Role name");
  fireEvent.click(await screen.findByLabelText("resource.read"));
  fireEvent.click(screen.getByLabelText("library.own"));
  change("Metadata change reason", "Review the full replacement set");
  click("Update role");
  const dialog = await screen.findByRole("dialog");
  const summary = within(dialog).getByLabelText("Command summary");
  expect(summary.textContent).toContain("Added permissionslibrary.own");
  expect(summary.textContent).toContain("Removed permissionsresource.read");
  expect(summary.textContent).toContain("Namespace or tenant → Tenant only");
  expect(summary.textContent).toContain(role.id);
  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
  expect(w.patch).not.toHaveBeenCalled();
  expect(screen.getByLabelText("library.own")).toHaveProperty("checked", true);
  expect(
    screen
      .getByRole("link", {
        name: "Inspect and explicitly clean up role assignments",
      })
      .getAttribute("href"),
  ).toBe(`/unit/access-assignments?roleDefinitionId=${role.id}`);
});

it.each(["roles", "groups"] as const)(
  "filters the %s directory at the API and resets its cursor",
  async (kind) => {
    const w = setup();
    const path =
      kind === "roles" ? "/api/v1/access/roles" : "/api/v1/access/groups";
    const configured = w.get.getMockImplementation()!;
    w.get.mockImplementation(async (p, o) =>
      p === path
        ? etagReply({
            items: kind === "roles" ? [role] : [group],
            authorizationRevision: 3,
            nextCursor: "next",
          })
        : configured(p, o),
    );
    history.replaceState(null, "", `/unit/access-${kind}`);
    render(<Authorization {...w.props} />);
    await screen.findByRole("link", {
      name: kind === "roles" ? role.name : group.name,
    });
    change("Exact name", "Exact Display Name");
    change("State", "archived");
    click("Apply filters");
    await waitFor(() =>
      expect(
        w.get.mock.calls.filter(([p]) => p === path).at(-1)?.[1],
      ).toMatchObject({
        params: {
          query: { limit: 25, name: "Exact Display Name", state: "archived" },
        },
      }),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Next page" })).toHaveProperty(
        "disabled",
        false,
      ),
    );
    click("Next page");
    await waitFor(() =>
      expect(
        w.get.mock.calls.filter(([p]) => p === path).at(-1)?.[1],
      ).toMatchObject({
        params: {
          query: {
            limit: 25,
            name: "Exact Display Name",
            state: "archived",
            cursor: "next",
          },
        },
      }),
    );
    click("Clear filters");
    await waitFor(() =>
      expect(
        w.get.mock.calls.filter(([p]) => p === path).at(-1)?.[1],
      ).toMatchObject({ params: { query: { limit: 25 } } }),
    );
  },
);

it("keeps a selected role through filtered choice pages and searches member and group pickers", async () => {
  const w = setup();
  history.replaceState(null, "", "/unit/access-assignments");
  render(<Authorization {...w.props} />);
  await screen.findByRole("option", { name: new RegExp(role.name) });
  change("Role", role.id);
  change("Exact role name", "Other role");
  const roleField = screen
    .getByLabelText("Exact role name")
    .closest<HTMLElement>(".workspace-toolbar")!;
  const different = { ...role, id: "other-role", name: "Other role" };
  const configured = w.get.getMockImplementation()!;
  w.get.mockImplementation(async (p, o) => {
    if (p === "/api/v1/access/roles")
      return etagReply({ items: [different], authorizationRevision: 3 });
    return configured(p, o);
  });
  fireEvent.click(
    within(roleField).getByRole("button", { name: "Apply choice filter" }),
  );
  await screen.findByRole("option", { name: new RegExp("Other role") });
  expect(screen.getByLabelText("Role definition ID")).toHaveProperty(
    "value",
    role.id,
  );
  expect(
    screen.getByRole("option", { name: new RegExp(`${role.name}.*selected`) }),
  ).toBeTruthy();
  change("Exact member subject", "Exact Human");
  fireEvent.click(
    within(
      screen
        .getByLabelText("Exact member subject")
        .closest<HTMLElement>(".workspace-toolbar")!,
    ).getByRole("button", { name: "Apply choice filter" }),
  );
  await waitFor(() =>
    expect(w.get).toHaveBeenCalledWith(
      "/api/v1/memberships",
      expect.objectContaining({
        params: {
          query: {
            limit: 25,
            state: "active",
            realmId: "realm-a",
            subject: "Exact Human",
          },
        },
      }),
    ),
  );
  change("Assignment target", "groupId");
  change("Exact group name", "Exact team");
  fireEvent.click(
    within(
      screen
        .getByLabelText("Exact group name")
        .closest<HTMLElement>(".workspace-toolbar")!,
    ).getByRole("button", { name: "Apply choice filter" }),
  );
  await waitFor(() =>
    expect(w.get).toHaveBeenCalledWith(
      "/api/v1/access/groups",
      expect.objectContaining({
        params: { query: { limit: 25, state: "active", name: "Exact team" } },
      }),
    ),
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("filters Add-member choices without granting the selected person anything", async () => {
  const w = setup();
  history.replaceState(null, "", `/unit/access-groups/${group.id}`);
  render(<Authorization {...w.props} id={group.id} />);
  await screen.findByLabelText("Exact member subject");
  change("Exact member subject", human.subject);
  fireEvent.click(
    within(
      screen
        .getByLabelText("Exact member subject")
        .closest<HTMLElement>(".workspace-toolbar")!,
    ).getByRole("button", { name: "Apply choice filter" }),
  );
  await waitFor(() =>
    expect(w.get).toHaveBeenCalledWith(
      "/api/v1/memberships",
      expect.objectContaining({
        params: {
          query: {
            limit: 25,
            state: "active",
            realmId: group.realmId,
            subject: human.subject,
          },
        },
      }),
    ),
  );
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["tenant", "coverage"])(
  "rejects %s mismatches after validating a member contribution response",
  async (scenario) => {
    const w = setup();
    w.get.mockResolvedValue(
      etagReply({
        ...contributionPage,
        items: [],
        ...(scenario === "tenant"
          ? { membership: { ...human, tenantId: "another-tenant" } }
          : { groupCoverage: "unavailable" }),
      }),
    );
    render(<MemberContributions {...w.props} memberId={human.id} />);
    await screen.findByRole("alert");
    expect(screen.queryByText(role.name)).toBeNull();
  },
);

it("renders retained empty-template paths and retries a failed first contribution page", async () => {
  const w = setup();
  w.get
    .mockRejectedValueOnce(new Error("owned unavailable page"))
    .mockResolvedValue(
      etagReply({
        ...contributionPage,
        items: [{ ...direct, role: { ...role, permissions: [] } }],
      }),
    );
  render(<MemberContributions {...w.props} memberId={human.id} />);
  await screen.findByRole("alert");
  click("Reload");
  await screen.findByText("Role permissions: None.");
  expect(w.post).not.toHaveBeenCalled();
});

it("captures a verified empty role as no product permissions before a general grant", async () => {
  const w = setup();
  const empty = {
    ...role,
    id: "role-empty",
    key: "service_client",
    builtin: true,
    name: "Membership only",
    permissions: [],
  };
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation(async (p, o) =>
    p === "/api/v1/access/roles/{roleId}" ? etagReply(empty) : original(p, o),
  );
  history.replaceState(
    null,
    "",
    "/unit/access-assignments?roleDefinitionId=role-empty",
  );
  render(<Authorization {...w.props} />);
  await screen.findByText(
    "This template supplies no product permissions. Identity type controls Human or Service classification; baseline human self and ownership rights remain separate.",
  );
  change("Member or group ID", human.id);
  change("Assignment scope", "tenant");
  change("Reason", "Explicit empty assignment");
  click("Grant access assignment");
  const dialog = await screen.findByRole("dialog");
  expect(dialog.textContent).toContain("No product permissions");
  expect(dialog.textContent).not.toContain(
    "Unverified; API validates the exact role ID",
  );
  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
  expect(w.post).not.toHaveBeenCalled();
});

it("immediately locks archived group commands while retaining its acknowledgement and inventories", async () => {
  const w = setup();
  history.replaceState(null, "", `/unit/access-groups/${group.id}`);
  w.post.mockResolvedValue(
    etagReply({ ...group, state: "archived", revision: 3 }),
  );
  render(<Authorization {...w.props} id={group.id} />);
  await screen.findByLabelText("Group action");
  change("Group action", "archive");
  change("Lifecycle change reason", "Archive empty owned group");
  await confirmCommand("Change group state");
  await screen.findByText(/Group state changed/);
  expect(screen.queryByLabelText("Group action")).toBeNull();
  for (const name of [
    "Update group",
    "Change group state",
    "Grant access assignment",
    "Add group member",
  ])
    expect(screen.queryByRole("button", { name })).toBeNull();
  expect(
    await screen.findByRole("heading", { name: "Direct group members" }),
  ).toBeTruthy();
  expect(
    await screen.findByRole("heading", { name: "Assignments" }),
  ).toBeTruthy();
  expect(
    screen.getByText(/These retained relations are historical and ineffective/),
  ).toBeTruthy();
  expect(screen.getByText(/Group state changed/)).toBeTruthy();
  expect(w.post).toHaveBeenCalledTimes(1);
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/access/groups/{groupId}:archive",
    {
      body: { reason: "Archive empty owned group" },
      params: {
        path: { groupId: group.id },
        header: { "If-Match": '"revision-1"' },
      },
    },
  ]);
  expect(w.patch).not.toHaveBeenCalled();
  expect(w.remove).not.toHaveBeenCalled();
  expect(
    screen.getByRole("link", { name: "Reload group" }).getAttribute("href"),
  ).toContain(group.id);
});

it("retains independent member and assignment revocation drafts through authority loss and group archival", async () => {
  const w = setup();
  history.replaceState(null, "", `/unit/access-groups/${group.id}`);
  const previousGet = w.get.getMockImplementation()!;
  let archived = false;
  w.get.mockImplementation(async (path, ...args) => {
    if (path === "/api/v1/access/groups/{groupId}")
      return etagReply(
        archived ? { ...group, state: "archived", revision: 3 } : group,
      );
    if (path === "/api/v1/access/groups/{groupId}/members")
      return etagReply({ items: [relation], authorizationRevision: 3 });
    if (path === "/api/v1/access/assignments")
      return etagReply({
        items: [{ ...assignment, membershipId: undefined, groupId: group.id }],
        authorizationRevision: 3,
      });
    return previousGet(path, ...args);
  });
  w.post.mockImplementation(async () => {
    archived = true;
    return etagReply({ ...group, state: "archived", revision: 3 });
  });
  const renderGroup = (access: typeof administrator) => (
    <OperationProvider
      access={access}
      runtime={w.props.runtime}
      view="access-groups"
    >
      <Authorization {...w.props} id={group.id} access={access} />
    </OperationProvider>
  );
  const ui = render(renderGroup(administrator));
  await screen.findByRole("button", {
    name: `Manage membership ${relation.membershipId}`,
  });
  click(`Manage membership ${relation.membershipId}`);
  const membershipEditor = screen.getByLabelText(
    "Selected group membership editor",
  );
  const memberReason = within(membershipEditor).getByLabelText("Reason");
  revealEditor(memberReason);
  fireEvent.change(memberReason, {
    target: { value: "Preserve this member relation reason" },
  });
  vi.spyOn(window, "confirm").mockReturnValue(true);
  fireEvent.click(
    await screen.findByRole("button", {
      name: `Manage assignment ${assignment.id}`,
    }),
  );
  const assignmentEditor = screen.getByLabelText(
    "Selected access assignment editor",
  );
  const assignmentReason = within(assignmentEditor).getByLabelText("Reason");
  fireEvent.change(assignmentReason, {
    target: { value: "Preserve this separate assignment reason" },
  });
  for (const access of [
    { ...administrator, permissions: [] },
    { ...administrator, scopeEnforcement: true, scopes: [] },
  ]) {
    ui.rerender(renderGroup(access));
    expect(memberReason.matches(":disabled")).toBe(true);
    expect(assignmentReason.matches(":disabled")).toBe(true);
    fireEvent.submit(membershipEditor.querySelector("form")!);
    fireEvent.submit(assignmentEditor.querySelector("form")!);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  }
  ui.rerender(renderGroup(administrator));
  change("Group action", "archive");
  change("Lifecycle change reason", "Archive only this group");
  await confirmCommand("Change group state");
  await screen.findByText(/Group state changed/);
  expect(
    within(
      screen.getByLabelText("Selected group membership editor"),
    ).getByLabelText("Reason"),
  ).toBe(memberReason);
  expect(
    within(
      screen.getByLabelText("Selected access assignment editor"),
    ).getByLabelText("Reason"),
  ).toBe(assignmentReason);
  expect(memberReason).toHaveProperty(
    "value",
    "Preserve this member relation reason",
  );
  expect(assignmentReason).toHaveProperty(
    "value",
    "Preserve this separate assignment reason",
  );
  for (const reason of [memberReason, assignmentReason])
    expect(reason.matches(":disabled")).toBe(true);
  fireEvent.submit(membershipEditor.querySelector("form")!);
  fireEvent.submit(assignmentEditor.querySelector("form")!);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(w.post).toHaveBeenCalledTimes(1);
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/access/groups/{groupId}:archive",
    {
      body: { reason: "Archive only this group" },
      params: {
        path: { groupId: group.id },
        header: { "If-Match": '"revision-1"' },
      },
    },
  ]);
  vi.mocked(window.confirm).mockReturnValue(false);
  click("Close membership editor");
  click("Close assignment editor");
  expect(screen.getByLabelText("Selected group membership editor")).toBe(
    membershipEditor,
  );
  expect(screen.getByLabelText("Selected access assignment editor")).toBe(
    assignmentEditor,
  );
  ui.rerender(
    renderGroup({ ...administrator, subject: "replacement-identity" }),
  );
  await screen.findByRole("heading", { name: group.name, level: 1 });
  expect(
    screen.queryByLabelText("Selected group membership editor"),
  ).toBeNull();
  expect(
    screen.queryByLabelText("Selected access assignment editor"),
  ).toBeNull();
  expect(screen.queryByLabelText("Reason")).toBeNull();
  expect(w.post).toHaveBeenCalledTimes(1);
});

it.each(["id", "tenantId", "realmId"] as const)(
  "refuses a group lifecycle result with a different %s",
  async (field) => {
    const w = setup();
    history.replaceState(null, "", `/unit/access-groups/${group.id}`);
    w.post.mockResolvedValue(
      etagReply({
        ...group,
        [field]: "different-target",
        state: "archived",
        revision: 3,
      }),
    );
    render(<Authorization {...w.props} id={group.id} />);
    await screen.findByLabelText("Group action");
    change("Group action", "archive");
    change("Lifecycle change reason", "Archive the exact group");
    await confirmCommand("Change group state");
    await screen.findByRole("alert");
    expect(screen.queryByText(/Group state changed/)).toBeNull();
    expect(
      screen.getByRole("button", { name: "Add group member" }),
    ).toBeTruthy();
    expect(w.post).toHaveBeenCalledTimes(1);
  },
);
