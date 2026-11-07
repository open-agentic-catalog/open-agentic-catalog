import { revealEditor } from "../../tests/unit/reveal-editor";
import { required } from "../../tests/required";
// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import Members from "./Members";
import { deferred } from "../../tests/unit/fixtures";
import {
  administrator,
  change as baseChange,
  click as baseClick,
  confirmCommand as baseConfirmCommand,
  etagReply,
  namespace,
  readReplies as baseReadReplies,
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
function grantTitle() {
  return screen.queryByText("Add Catalog member", { selector: "h2" })
    ? "Add Catalog member"
    : "Grant a built-in role";
}
screenLifecycle("/unit/members");
const member = {
  id: "member-a",
  tenantId: "tenant-a",
  subject: "unit-reader",
  realmId: "realm-a",
  actorType: "human",
  state: "active",
  revision: 1,
};
const grant = {
  id: "grant-1",
  membershipId: member.id,
  subject: member.subject,
  role: "reader",
  state: "active",
  revision: 3,
};

const readerDefinition = {
  id: "role-reader",
  tenantId: "tenant-a",
  key: "reader",
  builtin: true,
  name: "Reader",
  description: "Read resource metadata, history and content",
  state: "active",
  revision: 1,
  permissions: [
    "resource.read",
    "resource.read_history",
    "resource.read_content",
    "ratings.own",
  ],
  namespaceCapable: true,
  createdAt: "2026-10-05T10:00:00Z",
  updatedAt: "2026-10-05T10:00:00Z",
};
function generalGrant(value: typeof grant & { namespaceId?: string }) {
  const { subject: _subject, role: _role, ...rest } = value;
  return {
    ...rest,
    tenantId: "tenant-a",
    realmId: "realm-a",
    roleDefinitionId: readerDefinition.id,
    grantedBy: "admin",
    grantReason: "Reviewed",
    grantedAt: readerDefinition.createdAt,
  };
}
function readReplies(
  get: Parameters<typeof baseReadReplies>[0],
  routes: Record<string, unknown>,
) {
  const current = (routes["/api/v1/memberships/{membershipId}"] ??
    member) as typeof member;
  const inventory = routes["/api/v1/access/assignments"] as
    { items?: (typeof grant)[] } | undefined;
  baseReadReplies(get, {
    "/api/v1/access/roles": { items: [], authorizationRevision: 3 },
    "/api/v1/access/roles/{roleId}": readerDefinition,
    "/api/v1/memberships/{membershipId}/access": {
      items: [],
      authorizationRevision: 3,
      membership: current,
      groupCoverage:
        current.realmId === "realm-a" ? "current_realm" : "unavailable",
    },
    ...routes,
    ...(inventory
      ? {
          "/api/v1/access/assignments": {
            ...inventory,
            items: inventory.items?.map(generalGrant),
            authorizationRevision: 3,
          },
        }
      : {}),
  });
  const configured = get.getMockImplementation()!;
  get.mockImplementation(async (path, options) => {
    if (path === "/api/v1/access/roles") {
      const key =
        (options as { params?: { query?: { key?: string } } })?.params?.query
          ?.key ?? "reader";
      return etagReply({
        items: [
          {
            ...readerDefinition,
            key,
            id: `role-${key}`,
            name: key,
            permissions:
              key === "service_client" ? [] : readerDefinition.permissions,
          },
        ],
        authorizationRevision: 3,
      });
    }
    return configured(path, options);
  });
}

it("restricts administration and creates scoped grants with explicit identity and role", async () => {
  const { props, get, post } = screenWorkspace();
  const view = render(<Members {...props} />);
  expect(screen.getByRole("alert")).toBeTruthy();
  expect(
    screen.getByRole("link", { name: "Review My access" }).parentElement
      ?.textContent,
  ).toContain("required. Review My access");
  expect(get).not.toHaveBeenCalled();
  readReplies(get, {
    "/api/v1/memberships": {
      items: [
        member,
        {
          ...member,
          id: "other",
          subject: "service-account",
          realmId: "other-realm",
          actorType: "service",
        },
      ],
      nextCursor: "next",
    },
    "/api/v1/namespaces": { items: [namespace], nextCursor: "ns-next" },
  });
  post.mockResolvedValue(etagReply({ ...grant, membershipId: "new-member" }));
  view.rerender(<Members {...props} access={administrator} />);
  await screen.findByRole("link", { name: "unit-reader" });
  expect(screen.getByRole("columnheader", { name: "Realm" })).toBeTruthy();
  expect(
    screen.getByText(/Realm identifies the identity-provider boundary/),
  ).toBeTruthy();
  expect(screen.getByText("Your realm")).toBeTruthy();
  expect(screen.getByText("other-realm")).toBeTruthy();
  click("Next page");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { cursor: "next" } },
    }),
  );
  change("Subject", "robot");
  change("Identity type", "service");
  change("Catalog role", "tenant_admin");
  change("Assignment scope", "namespace");
  expect(
    (screen.getByLabelText("Catalog role") as HTMLSelectElement).value,
  ).toBe("tenant_admin");
  expect(screen.getByRole("button", { name: grantTitle() })).toHaveProperty(
    "disabled",
    true,
  );
  await screen.findByRole("option", {
    name: `${namespace.displayName} / ${namespace.slug} / ${namespace.id}`,
  });
  change("Namespace", namespace.id);
  const choices = within(
    required(
      screen.getByLabelText("Namespace").closest(".workspace-field")
        ?.parentElement ?? undefined,
    ),
  );
  fireEvent.click(choices.getByRole("button", { name: "Next page" }));
  await waitFor(() =>
    expect(get.mock.calls.at(-1)).toMatchObject([
      "/api/v1/namespaces",
      { params: { query: { cursor: "ns-next" } } },
    ]),
  );
  expect(screen.getByLabelText("Namespace")).toHaveProperty(
    "value",
    namespace.id,
  );
  change("Catalog role", "contributor");
  change("Reason", "Namespace author");
  await waitFor(() =>
    expect(screen.getByRole("button", { name: grantTitle() })).toHaveProperty(
      "disabled",
      false,
    ),
  );
  await confirmCommand(grantTitle());
  await screen.findByRole("link", { name: "Open updated membership" });
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/role-assignments",
    {
      body: {
        subject: "robot",
        actorType: "service",
        role: "contributor",
        namespaceId: namespace.id,
        reason: "Namespace author",
      },
    },
  ]);
});

it.each([
  ["active", "suspend", "suspended"],
  ["suspended", "reactivate", "active"],
  ["active", "offboard", "offboarded"],
])(
  "changes %s membership through %s with a captured revision",
  async (state, action, finalState) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      "/api/v1/memberships/{membershipId}": { ...member, state },
      "/api/v1/access/assignments": { items: [] },
    });
    post.mockResolvedValue(
      etagReply({
        membership: { ...member, state: finalState, revision: 2 },
        revokedRoleAssignments: 2,
        revokedNamespaceMemberships: 1,
        ...(action === "suspend" ? {} : { revokedGroupMemberships: 1 }),
      }),
    );
    render(<Members {...props} id={member.id} access={administrator} />);
    await screen.findByText("No assignments on this page.");
    expect(screen.getByText(/This is your membership/)).toBeTruthy();
    change("Membership action", action);
    fireEvent.change(required(screen.getAllByLabelText("Reason")[0]), {
      target: { value: "  Lifecycle review  " },
    });
    const prepared = screen.getByRole("button", {
      name: "Change membership state",
    });
    expect(
      prepared.classList.contains(
        action === "offboard"
          ? "danger"
          : action === "suspend"
            ? "warning"
            : "primary",
      ),
    ).toBe(true);
    expect(prepared.querySelector("svg")?.getAttribute("aria-hidden")).toBe(
      "true",
    );
    await confirmCommand("Change membership state");
    await screen.findByText(new RegExp(`Membership ${finalState}`));
    expect(screen.getByLabelText("Membership action")).toHaveProperty(
      "disabled",
      true,
    );
    expect(
      screen.getAllByRole("region", { name: "Member access contributions" }),
    ).toHaveLength(1);
    if (finalState === "offboarded") {
      expect(screen.queryByRole("button", { name: grantTitle() })).toBeNull();
      expect(
        screen.queryByRole("button", { name: /^Revoke assignment/ }),
      ).toBeNull();
      expect(
        screen.getByText(
          "Offboarding is terminal. This retained membership cannot accept new grants or lifecycle changes.",
        ),
      ).toBeTruthy();
    }
    expect(post.mock.calls[0]).toMatchObject([
      `/api/v1/memberships/{membershipId}:${action}`,
      {
        params: {
          path: { membershipId: member.id },
          header: { "If-Match": '"revision-1"' },
        },
        body: { reason: "Lifecycle review" },
      },
    ]);
  },
);

it.each([true, false])(
  "revokes the selected grant using its list revision (namespace=%s)",
  async (scoped) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      "/api/v1/memberships/{membershipId}": member,
      "/api/v1/access/assignments": {
        items: [
          { ...grant, ...(scoped ? { namespaceId: namespace.id } : {}) },
          {
            ...grant,
            id: "revoked",
            state: "revoked",
            namespaceId: "ns-2",
            revokedBy: "admin",
            revokeReason: "No longer needed",
            revokedAt: "2026-10-05T10:00:00Z",
          },
        ],
        nextCursor: "next",
      },
    });
    post.mockResolvedValue(etagReply(generalGrant(grant)));
    render(<Members {...props} id={member.id} access={administrator} />);
    await screen.findByRole("button", { name: "Revoke assignment grant-1" });
    const row = screen
      .getByRole("button", { name: "Revoke assignment grant-1" })
      .closest(".workspace-toolbar")!;
    expect(row.textContent).toContain(
      scoped ? `Namespace ${namespace.id}` : "Tenant-wide",
    );
    expect(row.textContent).toContain("active");
    const evidence = screen.getAllByText("Assignment attribution");
    expect(evidence).toHaveLength(2);
    expect(evidence[1]!.closest("details")!.textContent).toContain(
      "No longer needed",
    );
    click("Revoke assignment grant-1");
    const section = screen
      .getByRole("heading", { name: "Revoke assignment grant-1" })
      .closest("section")!;
    fireEvent.change(within(section).getByLabelText("Reason"), {
      target: { value: "Role no longer needed" },
    });
    const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
    click("Next page");
    fireEvent.click(
      required(
        screen.getAllByRole("button", { name: "Revoke assignment grant-1" })[0],
      ),
    );
    expect(discard).toHaveBeenCalledTimes(2);
    expect(within(section).getByLabelText("Reason")).toHaveProperty(
      "value",
      "Role no longer needed",
    );
    expect(
      get.mock.calls.filter(([path]) => path === "/api/v1/access/assignments"),
    ).toHaveLength(1);
    expect(post).not.toHaveBeenCalled();
    discard.mockReturnValue(true);
    fireEvent.click(
      within(section).getByRole("button", {
        name: "Revoke assignment grant-1",
      }),
    );
    click("Confirm");
    await screen.findByText(/Role revoked/);
    expect(post.mock.calls[0]).toMatchObject([
      "/api/v1/access/assignments/{assignmentId}:revoke",
      {
        params: {
          path: { assignmentId: grant.id },
          header: { "If-Match": '"rev-3"' },
        },
      },
    ]);
    click("Next page");
    await waitFor(() =>
      expect(
        get.mock.calls
          .filter(([path]) => path === "/api/v1/access/assignments")
          .at(-1)?.[1],
      ).toMatchObject({
        params: { query: { membershipId: member.id, cursor: "next" } },
      }),
    );
  },
);

it.each([
  ["active", "suspend", "suspended"],
  ["suspended", "reactivate", "active"],
  ["active", "offboard", "offboarded"],
] as const)(
  "retains independent grant and revocation drafts when %s membership becomes %s",
  async (state, action, finalState) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      "/api/v1/memberships/{membershipId}": { ...member, state },
      "/api/v1/access/assignments": { items: [grant] },
      "/api/v1/access/roles": {
        items: [readerDefinition],
        authorizationRevision: 3,
      },
    });
    post.mockImplementation(async () => {
      readReplies(get, {
        "/api/v1/memberships/{membershipId}": {
          ...member,
          state: finalState,
          revision: 2,
        },
        "/api/v1/access/assignments": {
          items: [{ ...grant, state: "revoked", revision: 4 }],
        },
        "/api/v1/access/roles": {
          items: [readerDefinition],
          authorizationRevision: 4,
        },
      });
      return etagReply({
        membership: { ...member, state: finalState, revision: 2 },
        revokedRoleAssignments: 1,
        revokedNamespaceMemberships: 0,
        revokedGroupMemberships: 0,
      });
    });
    render(<Members {...props} id={member.id} access={administrator} />);
    await screen.findByRole("button", {
      name: `Revoke assignment ${grant.id}`,
    });
    click(`Revoke assignment ${grant.id}`);
    const revocation = screen.getByLabelText("Selected role revocation editor");
    const revokeReason = within(revocation).getByLabelText("Reason");
    revealEditor(revokeReason);
    fireEvent.change(revokeReason, {
      target: { value: "Retain this exact revocation" },
    });
    const grantSection = screen
      .getByRole("heading", { name: "Grant a built-in role" })
      .closest("section")!;
    const grantReason = within(grantSection).getByLabelText("Reason");
    revealEditor(grantReason);
    fireEvent.change(within(grantSection).getByLabelText("Catalog role"), {
      target: { value: "reader" },
    });
    fireEvent.change(within(grantSection).getByLabelText("Assignment scope"), {
      target: { value: "tenant" },
    });
    fireEvent.change(grantReason, {
      target: { value: "Retain this other grant" },
    });
    const lifecycle = screen
      .getByLabelText("Membership action")
      .closest("section")!;
    revealEditor(lifecycle);
    fireEvent.change(within(lifecycle).getByLabelText("Membership action"), {
      target: { value: action },
    });
    fireEvent.change(within(lifecycle).getByLabelText("Reason"), {
      target: { value: "Lifecycle only" },
    });
    await confirmCommand("Change membership state");
    await screen.findByText(new RegExp(`Membership ${finalState}`));
    await waitFor(() =>
      expect(
        get.mock.calls.filter(
          ([path]) => path === "/api/v1/access/assignments",
        ),
      ).toHaveLength(2),
    );
    expect(
      within(
        screen.getByLabelText("Selected role revocation editor"),
      ).getByLabelText("Reason"),
    ).toBe(revokeReason);
    expect(revokeReason).toHaveProperty(
      "value",
      "Retain this exact revocation",
    );
    expect(grantReason).toHaveProperty("value", "Retain this other grant");
    expect(within(grantSection).getByLabelText("Catalog role")).toHaveProperty(
      "value",
      "reader",
    );
    expect(
      within(grantSection).getByLabelText("Assignment scope"),
    ).toHaveProperty("value", "tenant");
    expect(revokeReason.matches(":disabled")).toBe(finalState === "offboarded");
    expect(grantReason.matches(":disabled")).toBe(finalState === "offboarded");
    expect(
      within(revocation).getByRole("button", {
        name: `Revoke assignment ${grant.id}`,
      }),
    ).toHaveProperty("disabled", true);
    expect(post).toHaveBeenCalledTimes(1);
    expect(post.mock.calls[0]).toMatchObject([
      `/api/v1/memberships/{membershipId}:${action}`,
      {
        params: {
          path: { membershipId: member.id },
          header: { "If-Match": '"revision-1"' },
        },
        body: { reason: "Lifecycle only" },
      },
    ]);
    expect(
      get.mock.calls.filter(
        ([path]) => path === "/api/v1/access/assignments",
      )[1]?.[1],
    ).toMatchObject({
      params: { query: { membershipId: member.id, limit: 25 } },
    });
    const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
    click("Close role revocation editor");
    expect(discard).toHaveBeenCalledOnce();
    expect(screen.getByLabelText("Selected role revocation editor")).toBe(
      revocation,
    );
    if (finalState === "offboarded") {
      fireEvent.submit(grantSection.querySelector("form")!);
      fireEvent.submit(revocation.querySelector("form")!);
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(post).toHaveBeenCalledTimes(1);
    }
  },
);

it("grants tenant-wide access without retaining a previously selected namespace", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, {
    "/api/v1/memberships": { items: [] },
    "/api/v1/namespaces": { items: [namespace] },
  });
  post.mockResolvedValue(etagReply({ ...grant, membershipId: "new-member" }));
  render(<Members {...props} access={administrator} />);
  await screen.findByText("No memberships on this page.");
  change("Subject", " New colleague ");
  change("Catalog role", "reader");
  change("Assignment scope", "namespace");
  await screen.findByRole("option", {
    name: `${namespace.displayName} / ${namespace.slug} / ${namespace.id}`,
  });
  change("Namespace", namespace.id);
  change("Assignment scope", "tenant");
  expect(screen.queryByLabelText("Namespace")).toBeNull();
  change("Reason", " Tenant reader ");
  await waitFor(() =>
    expect(screen.getByRole("button", { name: grantTitle() })).toHaveProperty(
      "disabled",
      false,
    ),
  );
  await confirmCommand(grantTitle());
  await screen.findByRole("link", { name: "Open updated membership" });
  expect(required(post.mock.calls[0])[1]).toMatchObject({
    body: {
      subject: "New colleague",
      actorType: "human",
      role: "reader",
      reason: "Tenant reader",
    },
  });
  expect(
    (required(post.mock.calls[0])[1] as { body: object }).body,
  ).not.toHaveProperty("namespaceId");
});

it.each(["offboarded", "foreign-realm"])(
  "keeps grants unavailable for %s members",
  async (scenario) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      "/api/v1/memberships/{membershipId}": {
        ...member,
        state: scenario === "offboarded" ? scenario : "active",
        realmId: scenario === "foreign-realm" ? "other-realm" : member.realmId,
      },
      "/api/v1/access/assignments": { items: [] },
    });
    render(<Members {...props} id={member.id} access={administrator} />);
    await screen.findByText("No assignments on this page.");
    if (scenario === "offboarded") {
      expect(screen.queryByLabelText("Subject")).toBeNull();
      expect(screen.queryByRole("button", { name: grantTitle() })).toBeNull();
      expect(screen.queryByLabelText("Membership action")).toBeNull();
      expect(
        screen.getByText(/This retained membership cannot accept new grants/),
      ).toBeTruthy();
    } else {
      expect(screen.getByRole("button", { name: grantTitle() })).toHaveProperty(
        "disabled",
        true,
      );
    }
    expect(post).not.toHaveBeenCalled();
  },
);

it("grants an exact namespace role with tenant.admin scope without loading namespace names", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/memberships": { items: [] },
    "/api/v1/namespaces": { items: [] },
  });
  w.post.mockResolvedValue(etagReply(grant));
  render(
    <Members
      {...w.props}
      access={{
        ...administrator,
        scopeEnforcement: true,
        scopes: ["tenant.admin"],
      }}
    />,
  );
  await screen.findByText(/No memberships/);
  change("Subject", member.subject);
  change("Catalog role", "reader");
  change("Assignment scope", "namespace");
  change("Namespace ID", namespace.id);
  change("Reason", "Grant the exact namespace only");
  await waitFor(() =>
    expect(screen.getByRole("button", { name: grantTitle() })).toHaveProperty(
      "disabled",
      false,
    ),
  );
  await confirmCommand(grantTitle());
  await screen.findByRole("link", { name: "Open updated membership" });
  expect(w.get.mock.calls.map(([path]) => path)).not.toContain(
    "/api/v1/namespaces",
  );
  expect(
    w.get.mock.calls.filter(([path]) => path === "/api/v1/memberships"),
  ).toHaveLength(1);
  expect(w.post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      subject: member.subject,
      role: "reader",
      namespaceId: namespace.id,
    },
  });
});

it("focuses member revocation, protects its reason and returns to the invoking assignment", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/memberships/{membershipId}": member,
    "/api/v1/access/assignments": { items: [grant] },
    "/api/v1/namespaces": { items: [] },
  });
  const view = render(
    <Members {...w.props} id={member.id} access={administrator} />,
  );
  const opener = await screen.findByRole("button", {
    name: "Revoke assignment grant-1",
  });
  opener.focus();
  fireEvent.click(opener);
  const editor = screen.getByRole("region", {
    name: "Selected role revocation editor",
  });
  expect(document.activeElement).toBe(editor);
  const reason = within(editor).getByLabelText("Reason");
  reason.focus();
  view.rerender(<Members {...w.props} id={member.id} access={administrator} />);
  expect(document.activeElement).toBe(reason);
  fireEvent.click(opener);
  expect(document.activeElement).toBe(editor);
  fireEvent.change(reason, {
    target: { value: "Keep this revocation reason" },
  });
  const close = within(editor).getByRole("button", {
    name: "Close role revocation editor",
  });
  close.focus();
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  fireEvent.click(close);
  expect(document.activeElement).toBe(close);
  expect(reason).toHaveProperty("value", "Keep this revocation reason");
  discard.mockReturnValue(true);
  fireEvent.click(close);
  expect(document.activeElement).toBe(opener);
  expect(
    screen.queryByRole("region", { name: "Selected role revocation editor" }),
  ).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("explains a missing membership revision separately from an invalid state and never prepares a write", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/memberships/{membershipId}": member,
    "/api/v1/access/assignments": { items: [] },
  });
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation(async (path, options) =>
    path === "/api/v1/memberships/{membershipId}"
      ? { data: member, response: new Response() }
      : original(path, options),
  );
  render(<Members {...w.props} id={member.id} access={administrator} />);
  await screen.findByText(
    "Reload the membership to obtain its current revision.",
  );
  const stateButton = screen.getByRole("button", {
    name: "Change membership state",
  });
  expect(stateButton).toHaveProperty("disabled", true);
  fireEvent.click(stateButton);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("gives state-specific advice for an unsupported membership action without changing access", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/memberships/{membershipId}": member,
    "/api/v1/access/assignments": { items: [] },
  });
  render(<Members {...w.props} id={member.id} access={administrator} />);
  await screen.findByLabelText("Membership action");
  change("Membership action", "reactivate");
  expect(
    screen.getByText(
      "Choose an action supported by this membership's current state.",
    ),
  ).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "Change membership state" }),
  ).toHaveProperty("disabled", true);
  expect(w.post).not.toHaveBeenCalled();
});

it("applies exact member directory filters and recovers a stale filtered cursor", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/memberships": { items: [member], nextCursor: "next" },
  });
  const configured = w.get.getMockImplementation()!;
  w.get.mockImplementation(async (path, options) => {
    if (
      path === "/api/v1/memberships" &&
      (options as { params?: { query?: { cursor?: string } } })?.params?.query
        ?.cursor
    )
      throw new Error("owned stale cursor");
    return configured(path, options);
  });
  render(<Members {...w.props} access={administrator} />);
  await screen.findByText(member.subject);
  change("Exact subject", "Exact Subject");
  change("State", "suspended");
  change("Identity type filter", "service");
  change("Exact realm ID", "realm-a");
  click("Apply filters");
  await waitFor(() =>
    expect(w.get).toHaveBeenCalledWith(
      "/api/v1/memberships",
      expect.objectContaining({
        params: {
          query: {
            limit: 25,
            subject: "Exact Subject",
            state: "suspended",
            actorType: "service",
            realmId: "realm-a",
          },
        },
      }),
    ),
  );
  click("Next page");
  await screen.findByRole("alert");
  click("Refresh");
  await screen.findByText(member.subject);
  expect(
    w.get.mock.calls
      .filter(([path]) => path === "/api/v1/memberships")
      .at(-1)?.[1],
  ).toMatchObject({
    params: {
      query: {
        limit: 25,
        subject: "Exact Subject",
        state: "suspended",
        actorType: "service",
        realmId: "realm-a",
      },
    },
  });
  click("Clear filters");
  await waitFor(() =>
    expect(
      w.get.mock.calls
        .filter(([path]) => path === "/api/v1/memberships")
        .at(-1)?.[1],
    ).toMatchObject({ params: { query: { limit: 25 } } }),
  );
});

it.each(["human", "service"])(
  "creates %s membership with no product grant as a separate command",
  async (actorType) => {
    const w = screenWorkspace();
    readReplies(w.get, {
      "/api/v1/memberships": { items: [] },
      "/api/v1/namespaces": { items: [] },
    });
    w.post.mockResolvedValue(etagReply({ ...grant, role: "service_client" }));
    render(<Members {...w.props} access={administrator} />);
    await screen.findByText("No memberships on this page.");
    change("Subject", "Specialist only");
    change("Identity type", actorType);
    expect(screen.getByLabelText("Assignment scope")).toHaveProperty(
      "value",
      "",
    );
    change("Assignment scope", "namespace");
    expect(screen.getByLabelText("Catalog role")).toHaveProperty(
      "value",
      "service_client",
    );
    expect(screen.getByRole("button", { name: grantTitle() })).toHaveProperty(
      "disabled",
      true,
    );
    change("Assignment scope", "tenant");
    change("Reason", "Establish membership only");
    await waitFor(() =>
      expect(screen.getByRole("button", { name: grantTitle() })).toHaveProperty(
        "disabled",
        false,
      ),
    );
    click(grantTitle());
    const summary = screen.getByRole("dialog");
    expect(summary.textContent).toContain("service_client");
    expect(summary.textContent).toContain("No product permissions");
    expect(summary.textContent).toContain("Tenant-wide");
    click("Confirm");
    await screen.findByText(/Membership retained with the empty/);
    expect(w.post).toHaveBeenCalledTimes(1);
    expect(w.post.mock.calls[0]?.[1]).toMatchObject({
      body: {
        subject: "Specialist only",
        actorType,
        role: "service_client",
        reason: "Establish membership only",
      },
    });
    expect(w.post.mock.calls[0]?.[1]).not.toHaveProperty("body.namespaceId");
  },
);

it("blocks role preparation until exact current metadata loads and supports explicit refresh", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/memberships": { items: [] },
    "/api/v1/namespaces": { items: [] },
  });
  const configured = w.get.getMockImplementation()!;
  const late = deferred<ReturnType<typeof etagReply>>();
  let pending = true;
  w.get.mockImplementation(async (path, options) =>
    path === "/api/v1/access/roles" && pending
      ? late.promise
      : configured(path, options),
  );
  render(<Members {...w.props} access={administrator} />);
  await screen.findByText("No memberships on this page.");
  change("Subject", "New member");
  change("Assignment scope", "tenant");
  change("Reason", "Membership first");
  expect(screen.getByRole("button", { name: grantTitle() })).toHaveProperty(
    "disabled",
    true,
  );
  late.resolve(etagReply({ items: [], authorizationRevision: 3 }));
  await screen.findByText(
    "Load the selected built-in role before preparing this grant.",
  );
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Refresh proposed role details" }),
    ).toHaveProperty("disabled", false),
  );
  pending = false;
  click("Refresh proposed role details");
  await waitFor(() =>
    expect(screen.getByRole("button", { name: grantTitle() })).toHaveProperty(
      "disabled",
      false,
    ),
  );
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["active", "suspended", "foreign"])(
  "checks current membership eligibility before granting to %s targets",
  async (state) => {
    const w = screenWorkspace();
    readReplies(w.get, {
      "/api/v1/memberships/{membershipId}": {
        ...member,
        state: state === "suspended" ? state : "active",
        realmId: state === "foreign" ? "another-realm" : member.realmId,
      },
      "/api/v1/access/assignments": { items: [] },
    });
    render(<Members {...w.props} access={administrator} id={member.id} />);
    await screen.findByLabelText("Assignment scope");
    change("Assignment scope", "tenant");
    change("Subject", member.subject);
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Refresh proposed role details" }),
      ).toHaveProperty("disabled", false),
    );
    expect(screen.getByRole("button", { name: grantTitle() })).toHaveProperty(
      "disabled",
      state !== "active",
    );
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["id", "tenantId", "realmId"] as const)(
  "rejects a lifecycle result with a different %s without changing member controls",
  async (field) => {
    const w = screenWorkspace();
    readReplies(w.get, {
      "/api/v1/memberships/{membershipId}": member,
      "/api/v1/access/assignments": { items: [] },
    });
    w.post.mockResolvedValue(
      etagReply({
        membership: {
          ...member,
          [field]: "different-target",
          state: "offboarded",
          revision: 2,
        },
        revokedRoleAssignments: 1,
        revokedNamespaceMemberships: 0,
        revokedGroupMemberships: 0,
      }),
    );
    render(<Members {...w.props} access={administrator} id={member.id} />);
    await screen.findByLabelText("Membership action");
    change("Membership action", "offboard");
    fireEvent.change(screen.getAllByLabelText("Reason")[0]!, {
      target: { value: "Explicit lifecycle command" },
    });
    await confirmCommand("Change membership state");
    await screen.findByRole("alert");
    expect(screen.getByRole("button", { name: grantTitle() })).toBeTruthy();
    expect(screen.queryByText(/Membership offboarded/)).toBeNull();
    expect(w.post).toHaveBeenCalledTimes(1);
  },
);
