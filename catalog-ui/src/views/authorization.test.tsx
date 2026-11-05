import { revealEditor } from "../../tests/unit/reveal-editor";
// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
  waitFor,
} from "@testing-library/react";
import Authorization from "./Authorization";
import {
  role,
  group,
  assignment,
  member,
  descriptors,
} from "../../tests/unit/policy-fixtures";
import {
  administrator,
  click as baseClick,
  change as baseChange,
  etagReply,
  readReplies,
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
import {
  deferred,
  installDialog,
  reader,
  reply,
} from "../../tests/unit/fixtures";
import { APIError } from "../api/client";
let restore: () => void;
beforeEach(() => {
  restore = installDialog();
  history.replaceState(null, "", "/unit/access-roles");
});
afterEach(() => {
  cleanup();
  restore();
  vi.restoreAllMocks();
  history.replaceState(null, "", "/");
});
function setup(section = "access-roles", id = "") {
  history.replaceState(null, "", `/unit/${section}/${id}`);
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/access/roles": {
      items: [role],
      authorizationRevision: 3,
      nextCursor: "role-next",
    },
    "/api/v1/access/roles/{roleId}": role,
    "/api/v1/access/permissions": descriptors,
    "/api/v1/access/groups": {
      items: [group],
      authorizationRevision: 3,
      nextCursor: "group-next",
    },
    "/api/v1/access/groups/{groupId}": group,
    "/api/v1/access/groups/{groupId}/members": {
      items: [member],
      authorizationRevision: 3,
      nextCursor: "member-next",
    },
    "/api/v1/memberships": { items: [], nextCursor: undefined },
    "/api/v1/namespaces": { items: [], nextCursor: undefined },
    "/api/v1/access/assignments": {
      items: [assignment],
      authorizationRevision: 3,
      nextCursor: "assignment-next",
    },
  });
  return {
    ...w,
    props: {
      ...w.props,
      access: {
        ...administrator,
        tenantId: role.tenantId,
        realmId: group.realmId,
      },
      id,
    },
  };
}
function fillReason(title: string, value = "Reviewed policy change") {
  const heading = screen.getByText(title, { selector: "h2" });
  revealEditor(heading);
  const section = screen
    .getByRole("heading", { name: title })
    .closest("section")!;
  fireEvent.change(
    within(section).getByLabelText(
      /^(Reason|Metadata change reason|Lifecycle change reason)$/,
    ),
    {
      target: { value },
    },
  );
}

it("keeps foreign-realm assignments readable without offering a usable revoke draft", async () => {
  const w = setup("access-assignments");
  w.get.mockImplementation(async (...[path]) =>
    path === "/api/v1/access/assignments"
      ? reply({
          items: [{ ...assignment, realmId: "other-realm" }],
          authorizationRevision: 3,
        })
      : reply({ items: [] }),
  );
  render(<Authorization {...w.props} />);
  expect(await screen.findByText(/Read-only in realm/)).toBeTruthy();
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Manage assignment assignment-a",
    }),
  );
  expect(
    await screen.findByText(/Read-only assignment in realm other-realm/),
  ).toBeTruthy();
  const section = screen
    .getByRole("heading", { name: "Revoke access assignment" })
    .closest("section")!;
  expect(within(section).queryByLabelText("Reason")).toBeNull();
  expect(
    within(section).queryByRole("button", { name: "Revoke access assignment" }),
  ).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("shows active relations under archived groups as historical and ineffective", async () => {
  const w = setup("access-groups", group.id);
  w.get.mockImplementation(async (...[path]) =>
    path === "/api/v1/access/groups/{groupId}"
      ? etagReply({ ...group, state: "archived" })
      : reply({
          items:
            path === "/api/v1/access/groups/{groupId}/members" ? [member] : [],
          authorizationRevision: 3,
        }),
  );
  render(<Authorization {...w.props} />);
  expect(
    await screen.findByText(
      /These retained relations are historical and ineffective/,
    ),
  ).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Manage membership member-a" }),
  ).toBeNull();
  expect(
    screen.queryByRole("heading", { name: "Revoke group member" }),
  ).toBeNull();
  for (const title of ["Add group member", "Grant access assignment"]) {
    const section = screen
      .getByText(title, { selector: "h2" })
      .closest("section")!;
    expect(within(section).queryByLabelText("Reason")).toBeNull();
    expect(section.querySelector("input,select,textarea")).toBeNull();
  }
  expect(w.post).not.toHaveBeenCalled();
});

/** Confirm workflow facts through the real dialog before the existing request assertions. */
async function confirmCommand(title: string) {
  revealEditor(screen.getByText(title, { selector: "button" }));
  fireEvent.click(screen.getByRole("button", { name: title }));
  const dialog = await screen.findByRole("dialog");
  const facts = within(dialog).getByLabelText("Command summary");
  expect(facts.textContent).toContain("Action");
  expect(facts.textContent).toContain("Reason");
  if (title === "Change group state") {
    const action = (screen.getByLabelText("Group action") as HTMLSelectElement)
      .value;
    expect(facts.textContent).toContain(
      action === "suspend"
        ? "Suspend group"
        : action === "reactivate"
          ? "Reactivate group"
          : "Archive group",
    );
  } else
    expect(facts.textContent).toContain(
      title === "Grant access assignment" ? "Grant access" : title,
    );
  fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
}

it.each(["membership", "assignment"] as const)(
  "keeps independent %s reasons while switching and closing selected editors",
  async (kind) => {
    const isMember = kind === "membership";
    const w = setup(
      isMember ? "access-groups" : "access-assignments",
      isMember ? group.id : "",
    );
    const oldRead = w.get.getMockImplementation()!;
    const first = isMember ? member : assignment;
    const second = {
      ...first,
      id: "second-path",
      membershipId: "second-member",
    };
    w.get.mockImplementation((path, options) =>
      path ===
      (isMember
        ? "/api/v1/access/groups/{groupId}/members"
        : "/api/v1/access/assignments")
        ? Promise.resolve(
            reply({
              items: [first, second],
              authorizationRevision: 3,
              nextCursor: "next",
            }),
          )
        : oldRead(path, options),
    );
    const title = isMember ? "Revoke group member" : "Revoke access assignment";
    const manage = (row: typeof first) =>
      screen.getByRole("button", {
        name: isMember
          ? `Manage membership ${row.membershipId}`
          : `Manage assignment ${row.id}`,
      });
    const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
    await act(async () => {
      render(<Authorization {...w.props} />);
    });
    await screen.findByRole("button", {
      name: isMember
        ? `Manage membership ${first.membershipId}`
        : `Manage assignment ${first.id}`,
    });
    fireEvent.click(manage(first));
    fillReason(title, "First selected reason");
    if (isMember) {
      fillReason("Add group member", "Separate add reason");
      change("Membership ID", "new-member");
    }
    const reads = w.get.mock.calls.length;
    fireEvent.click(manage(second));
    fireEvent.click(
      screen.getByRole("button", {
        name: isMember ? "Close membership editor" : "Close assignment editor",
      }),
    );
    fireEvent.click(screen.getAllByRole("button", { name: "Next page" })[0]!);
    if (!isMember)
      fireEvent.click(
        screen.getByRole("button", { name: "Refresh assignments" }),
      );
    expect(
      screen
        .getByRole("heading", { name: title })
        .closest("section")
        ?.querySelector("textarea"),
    ).toHaveProperty("value", "First selected reason");
    expect(w.get).toHaveBeenCalledTimes(reads);
    expect(w.post).not.toHaveBeenCalled();
    expect(
      window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    ).toBe(false);
    discard.mockReturnValue(true);
    fireEvent.click(manage(second));
    expect(
      screen
        .getByRole("heading", { name: title })
        .closest("section")
        ?.querySelector("textarea"),
    ).toHaveProperty("value", "");
    fillReason(title, "Second selected reason");
    fireEvent.click(
      screen.getByRole("button", {
        name: isMember ? "Close membership editor" : "Close assignment editor",
      }),
    );
    expect(screen.queryByRole("heading", { name: title })).toBeNull();
    fireEvent.click(manage(first));
    expect(
      screen
        .getByRole("heading", { name: title })
        .closest("section")
        ?.querySelector("textarea"),
    ).toHaveProperty("value", "");
    if (isMember) {
      expect(
        screen
          .getByRole("heading", { name: "Add group member" })
          .closest("section")
          ?.querySelector("textarea"),
      ).toHaveProperty("value", "Separate add reason");
      expect(screen.getByLabelText("Membership ID")).toHaveProperty(
        "value",
        "new-member",
      );
    }
  },
);
it("denies administration without protected current access, independent of navigation", () => {
  const w = setup();
  render(<Authorization {...w.props} access={reader} />);
  expect(screen.getByRole("alert").textContent).toContain(
    "Direct tenant administrator",
  );
  expect(w.get).not.toHaveBeenCalled();
});
it.each([
  ["access-roles", "Metadata reader", "Refresh roles"],
  ["access-groups", "Metadata readers", "Refresh groups"],
] as const)(
  "pages and explicitly refreshes %s",
  async (section, label, refresh) => {
    const w = setup(section);
    render(<Authorization {...w.props} />);
    await screen.findByRole("link", { name: label });
    fireEvent.click(screen.getAllByRole("button", { name: "Next page" })[0]!);
    await act(async () => {});
    expect(w.get).toHaveBeenCalledWith(
      section === "access-roles"
        ? "/api/v1/access/roles"
        : "/api/v1/access/groups",
      expect.objectContaining({
        params: {
          query: {
            limit: 25,
            cursor: section === "access-roles" ? "role-next" : "group-next",
          },
        },
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "First page" }));
    fireEvent.click(screen.getByRole("button", { name: refresh }));
    await act(async () => {});
  },
);
it.each([false, true])(
  "creates/edits a custom role with captured permissions (existing: %s)",
  async (existing) => {
    const w = setup("access-roles", existing ? role.id : "new");
    w.post.mockResolvedValue(reply(role));
    w.patch.mockResolvedValue(reply(role));
    render(<Authorization {...w.props} />);
    await screen.findByLabelText("resource.read");
    change("Role name", "Focused metadata role");
    change("Role description", "Bounded readable content");
    if (!existing) fireEvent.click(screen.getByLabelText("resource.read"));
    fireEvent.click(screen.getByLabelText("resource.create"));
    fireEvent.click(screen.getByLabelText("resource.create"));
    fillReason(existing ? "Update role" : "Create role");
    await confirmCommand(existing ? "Update role" : "Create role");
    await screen.findByText(/Role saved/);
    const request = existing ? w.patch : w.post;
    expect(request).toHaveBeenCalledWith(
      existing ? "/api/v1/access/roles/{roleId}" : "/api/v1/access/roles",
      expect.objectContaining({
        body: {
          name: "Focused metadata role",
          description: "Bounded readable content",
          permissions: existing ? role.permissions : ["resource.read"],
          reason: "Reviewed policy change",
        },
      }),
    );
    expect(screen.getByLabelText("access.manage")).toHaveProperty(
      "disabled",
      true,
    );
  },
);
it("preserves role idempotency input after a malformed successful write and requires a manual retry", async () => {
  const w = setup("access-roles", "new");
  w.post
    .mockResolvedValueOnce(reply({ id: role.id }))
    .mockResolvedValueOnce(reply(role));
  render(<Authorization {...w.props} />);
  await screen.findByLabelText("resource.read");
  change("Role name", "Retry role");
  fireEvent.click(screen.getByLabelText("resource.read"));
  fillReason("Create role");
  await confirmCommand("Create role");
  await screen.findByText(/uncertain|could not be checked|invalid/i);
  const first = w.post.mock.calls[0];
  fireEvent.click(screen.getByRole("button", { name: /Retry/ }));
  await act(async () => {});
  expect(w.post.mock.calls[1]).toEqual(first);
  await screen.findByText(/Role saved/);
});
it("keeps a tenant-only built-in role immutable while showing assignment impact", async () => {
  const w = setup("access-roles", role.id);
  const vocabulary = deferred<ReturnType<typeof etagReply>>();
  w.get.mockImplementation(async (path) =>
    path === "/api/v1/access/permissions"
      ? vocabulary.promise
      : etagReply(
          path === "/api/v1/access/roles/{roleId}"
            ? {
                ...role,
                builtin: true,
                name: "Tenant administrator",
                permissions: ["access.manage"],
                namespaceCapable: false,
              }
            : { items: [], authorizationRevision: 3 },
        ),
  );
  render(<Authorization {...w.props} />);
  await screen.findByRole("heading", { name: "Role permissions" });
  expect(screen.getByText(/Built-in templates cannot be edited/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Update role" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Archive role" })).toBeNull();
  expect(screen.queryByLabelText("Role name")).toBeNull();
  expect(screen.queryByLabelText("Role description")).toBeNull();
  expect(screen.queryByLabelText("Metadata change reason")).toBeNull();
  expect(
    screen.getByRole("link", { name: "Back to roles" }).getAttribute("href"),
  ).toBe("/unit/access-roles");
  change("Find permissions", "tenant access");
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(true);
  await act(async () => vocabulary.resolve(etagReply(descriptors)));
  expect(await screen.findByLabelText("access.manage")).toHaveProperty(
    "disabled",
    true,
  );
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(true);
  expect(w.post).not.toHaveBeenCalled();
});
it("archives only a loaded custom role with its current ETag and reason", async () => {
  const w = setup("access-roles", role.id);
  w.post.mockResolvedValue(reply({ ...role, state: "archived" }));
  render(<Authorization {...w.props} />);
  await screen.findByRole("button", { name: "Archive role" });
  fillReason("Archive role");
  expect(
    screen
      .getByRole("button", { name: "Archive role" })
      .classList.contains("danger"),
  ).toBe(true);
  await confirmCommand("Archive role");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/access/roles/{roleId}:archive",
    expect.objectContaining({
      params: {
        path: { roleId: role.id },
        header: {
          "Idempotency-Key": expect.any(String) as unknown,
          "If-Match": '"revision-1"',
        },
      },
    }),
  );
});
it.each([false, true])(
  "creates/edits a same-realm group (existing: %s)",
  async (existing) => {
    const w = setup("access-groups", existing ? group.id : "new");
    w.post.mockResolvedValue(reply(group));
    w.patch.mockResolvedValue(reply(group));
    render(<Authorization {...w.props} />);
    await screen.findByLabelText("Group name");
    change("Group name", "Focused group");
    change("Group description", "Direct local members");
    fillReason(existing ? "Update group" : "Create group");
    await confirmCommand(existing ? "Update group" : "Create group");
    await screen.findByText(/Group saved\./);
    expect(existing ? w.patch : w.post).toHaveBeenCalledWith(
      existing ? "/api/v1/access/groups/{groupId}" : "/api/v1/access/groups",
      expect.objectContaining({
        body: {
          name: "Focused group",
          description: "Direct local members",
          reason: "Reviewed policy change",
        },
      }),
    );
  },
);
it.each(["suspend", "reactivate", "archive"] as const)(
  "captures the %s group transition and revision",
  async (action) => {
    const w = setup("access-groups", group.id);
    w.get.mockImplementation(async (path) =>
      etagReply(
        path === "/api/v1/access/groups/{groupId}"
          ? {
              ...group,
              state: action === "reactivate" ? "suspended" : "active",
            }
          : { items: [], authorizationRevision: 3 },
      ),
    );
    w.post.mockResolvedValue(
      reply({
        ...group,
        state:
          action === "suspend"
            ? "suspended"
            : action === "archive"
              ? "archived"
              : "active",
      }),
    );
    render(<Authorization {...w.props} />);
    await screen.findByLabelText("Group action");
    change("Group action", action);
    fillReason("Change group state");
    expect(
      screen
        .getByRole("button", { name: "Change group state" })
        .classList.contains(
          action === "archive"
            ? "danger"
            : action === "suspend"
              ? "warning"
              : "primary",
        ),
    ).toBe(true);
    await confirmCommand("Change group state");
    expect(w.post).toHaveBeenCalledWith(
      `/api/v1/access/groups/{groupId}:${action}`,
      expect.objectContaining({
        params: {
          path: { groupId: group.id },
          header: {
            "Idempotency-Key": expect.any(String) as unknown,
            "If-Match": '"revision-1"',
          },
        },
      }),
    );
  },
);
it("adds and revokes group membership with separate reasons and bounded pages", async () => {
  const w = setup("access-groups", group.id);
  w.post.mockResolvedValue(reply(member));
  render(<Authorization {...w.props} />);
  await screen.findByRole("heading", { name: "Direct group members" });
  change("Membership ID", "another-member");
  fillReason("Add group member");
  await confirmCommand("Add group member");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/access/groups/{groupId}/members",
    expect.objectContaining({
      body: {
        membershipId: "another-member",
        reason: "Reviewed policy change",
      },
    }),
  );
  vi.spyOn(window, "confirm").mockReturnValue(true);
  fireEvent.click(
    screen.getByRole("button", {
      name: `Manage membership ${member.membershipId}`,
    }),
  );
  fillReason("Revoke group member");
  await confirmCommand("Revoke group member");
  expect(w.post).toHaveBeenLastCalledWith(
    "/api/v1/access/groups/{groupId}/members/{memberId}:revoke",
    expect.objectContaining({
      params: {
        path: { groupId: group.id, memberId: member.id },
        header: {
          "Idempotency-Key": expect.any(String) as unknown,
          "If-Match": '"rev-2"',
        },
      },
    }),
  );
  vi.spyOn(window, "confirm").mockReturnValue(true);
  fireEvent.click(screen.getAllByRole("button", { name: "Next page" })[0]!);
  await act(async () => {});
  expect(w.get).toHaveBeenCalledWith(
    "/api/v1/access/groups/{groupId}/members",
    expect.objectContaining({
      params: {
        path: { groupId: group.id },
        query: { limit: 25, cursor: "member-next" },
      },
    }),
  );
});
it.each(["membershipId", "groupId"] as const)(
  "grants a %s assignment and revokes only its own path",
  async (target) => {
    const w = setup("access-assignments");
    w.post.mockResolvedValue(reply(assignment));
    render(<Authorization {...w.props} />);
    await screen.findByRole("heading", { name: "Grant access assignment" });
    change("Role definition ID", role.id);
    change("Assignment target", target);
    change(
      "Member or group ID",
      target === "membershipId" ? "member-b" : group.id,
    );
    if (target === "membershipId") {
      change("Assignment scope", "namespace");
      change("Namespace ID", "namespace-a");
    } else change("Assignment scope", "tenant");
    fillReason("Grant access assignment");
    await confirmCommand("Grant access assignment");
    expect(w.post).toHaveBeenCalledWith(
      "/api/v1/access/assignments",
      expect.objectContaining({
        body: {
          roleDefinitionId: role.id,
          [target]: target === "membershipId" ? "member-b" : group.id,
          ...(target === "membershipId" ? { namespaceId: "namespace-a" } : {}),
          reason: "Reviewed policy change",
        },
      }),
    );
    vi.spyOn(window, "confirm").mockReturnValue(true);
    fireEvent.click(
      screen.getByRole("button", {
        name: `Manage assignment ${assignment.id}`,
      }),
    );
    fillReason("Revoke access assignment");
    await confirmCommand("Revoke access assignment");
    expect(w.post).toHaveBeenLastCalledWith(
      "/api/v1/access/assignments/{assignmentId}:revoke",
      expect.objectContaining({
        params: {
          path: { assignmentId: assignment.id },
          header: {
            "Idempotency-Key": expect.any(String) as unknown,
            "If-Match": '"rev-3"',
          },
        },
      }),
    );
    fireEvent.click(screen.getAllByRole("button", { name: "Next page" })[0]!);
    await act(async () => {});
    fireEvent.click(screen.getByRole("button", { name: "First page" }));
    fireEvent.click(
      screen.getByRole("button", { name: "Refresh assignments" }),
    );
    await act(async () => {});
  },
);
it("does not render a malformed read as an empty successful policy inventory", async () => {
  const w = setup();
  w.get.mockResolvedValue(
    reply({
      items: [{ ...role, permissions: ["unknown"] }],
      authorizationRevision: 3,
    }),
  );
  render(<Authorization {...w.props} />);
  await screen.findByRole("alert");
  expect(w.props.onFailure).toHaveBeenCalled();
  expect(screen.queryByRole("link", { name: role.name })).toBeNull();
});
it("retains a revision conflict without automatically resubmitting a role edit", async () => {
  const w = setup("access-roles", role.id);
  const original = w.get.getMockImplementation()!;
  const permissions = deferred<ReturnType<typeof etagReply>>();
  w.get.mockImplementation((path, options) =>
    path === "/api/v1/access/permissions"
      ? permissions.promise
      : original(path, options),
  );
  w.patch.mockRejectedValue(new APIError(412, "PRECONDITION_FAILED"));
  render(<Authorization {...w.props} />);
  await screen.findByLabelText("Role name");
  fillReason("Update role");
  const update = screen.getByRole("button", { name: "Update role" });
  expect(update).toHaveProperty("disabled", true);
  fireEvent.click(update);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(w.patch).not.toHaveBeenCalled();
  await act(async () => permissions.resolve(etagReply(descriptors)));
  expect(update).toHaveProperty("disabled", false);
  await confirmCommand("Update role");
  expect((await screen.findByRole("alert")).textContent).toContain(
    "The record changed. Reload it before trying again.",
  );
  expect(w.patch).toHaveBeenCalledTimes(1);
});

it.each(["membershipId", "groupId", "roleDefinitionId"] as const)(
  "preserves the %s inventory filter across bounded pages",
  async (filter) => {
    const w = setup("access-assignments");
    history.replaceState(
      null,
      "",
      `/unit/access-assignments?${filter}=selected-id`,
    );
    render(<Authorization {...w.props} />);
    await screen.findByText(/Showing assignments for:/);
    expect(w.get).toHaveBeenCalledWith(
      "/api/v1/access/assignments",
      expect.objectContaining({
        params: { query: { limit: 25, [filter]: "selected-id" } },
      }),
    );
    fireEvent.click(screen.getAllByRole("button", { name: "Next page" })[0]!);
    await waitFor(() =>
      expect(w.get).toHaveBeenLastCalledWith(
        "/api/v1/access/assignments",
        expect.objectContaining({
          params: {
            query: {
              limit: 25,
              [filter]: "selected-id",
              cursor: "assignment-next",
            },
          },
        }),
      ),
    );
    expect(
      screen
        .getByRole("link", { name: "All assignments" })
        .getAttribute("href"),
    ).toBe("/unit/access-assignments");
  },
);

it("preserves a revoke draft and focus when continuation is cancelled, without replaying a mutation", async () => {
  const w = setup("access-assignments");
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  render(<Authorization {...w.props} />);
  fireEvent.click(
    await screen.findByRole("button", {
      name: `Manage assignment ${assignment.id}`,
    }),
  );
  await screen.findByText("Assignment attribution");
  fillReason("Revoke access assignment", "Keep this unsent reason");
  const next = screen.getAllByRole("button", { name: "Next page" })[0]!;
  next.focus();
  fireEvent.click(next);
  expect(discard).toHaveBeenCalledOnce();
  expect(document.activeElement).toBe(next);
  expect(
    w.get.mock.calls.filter(([path]) => path === "/api/v1/access/assignments"),
  ).toHaveLength(1);
  expect(
    screen
      .getByRole("heading", { name: "Revoke access assignment" })
      .closest("section")
      ?.querySelector("textarea"),
  ).toHaveProperty("value", "Keep this unsent reason");
  expect(w.post).not.toHaveBeenCalled();
});

it("shows every returned descriptor, flags missing companions and never selects them implicitly", async () => {
  const w = setup("access-roles", "new");
  render(<Authorization {...w.props} />);
  await screen.findByLabelText("resource.read");
  expect(
    screen
      .getAllByRole("checkbox")
      .filter((box) => box.closest(".permission-choice")),
  ).toHaveLength(descriptors.length);
  for (const descriptor of descriptors)
    expect(screen.getByLabelText(descriptor.key)).toHaveProperty(
      "disabled",
      !descriptor.delegable,
    );
  change("Role name", "Explicit authoring");
  fillReason("Create role");
  fireEvent.click(screen.getByLabelText("resource.create"));
  expect(screen.getByRole("button", { name: "Create role" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(screen.getByLabelText("namespace.read")).toHaveProperty(
    "checked",
    false,
  );
  expect(screen.getByText(/Required companions missing/).textContent).toContain(
    "namespace.read",
  );
  fireEvent.click(screen.getByLabelText("namespace.read"));
  expect(screen.getByRole("button", { name: "Create role" })).toHaveProperty(
    "disabled",
    false,
  );
  fireEvent.click(screen.getByLabelText("namespace.read"));
  expect(screen.getByRole("button", { name: "Create role" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("uses bounded named choices, excludes inactive/foreign values and submits only selected exact IDs", async () => {
  const w = setup("access-assignments");
  const original = w.get.getMockImplementation()!;
  const membership = {
    id: "known-member",
    tenantId: role.tenantId,
    realmId: group.realmId,
    subject: "Human reader",
    actorType: "human",
    state: "active",
    revision: 1,
    createdAt: role.createdAt,
    updatedAt: role.updatedAt,
  };
  w.get.mockImplementation(async (path, ...args) => {
    if (path === "/api/v1/access/roles")
      return reply({
        items: [
          role,
          { ...role, id: "inactive-role", state: "archived" },
          { ...role, id: "foreign-role", tenantId: "foreign" },
        ],
        authorizationRevision: 3,
      });
    if (path === "/api/v1/memberships")
      return reply({
        items: [
          membership,
          { ...membership, id: "inactive-member", state: "suspended" },
          { ...membership, id: "foreign-member", tenantId: "foreign" },
        ],
      });
    if (path === "/api/v1/namespaces")
      return reply({
        items: [
          {
            id: "known-namespace",
            tenantId: role.tenantId,
            slug: "tools",
            displayName: "Tools",
            revision: 1,
            createdAt: role.createdAt,
            updatedAt: role.updatedAt,
          },
        ],
      });
    return original(path, ...args);
  });
  w.post.mockResolvedValue(reply(assignment));
  render(<Authorization {...w.props} />);
  await screen.findByRole("option", { name: /Human reader/ });
  expect(
    screen.queryByRole("option", { name: /inactive-|foreign-/ }),
  ).toBeNull();
  change("Role", role.id);
  change("Member", "known-member");
  change("Assignment scope", "namespace");
  await screen.findByRole("option", { name: /Tools/ });
  change("Namespace", "known-namespace");
  fillReason("Grant access assignment");
  await confirmCommand("Grant access assignment");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/access/assignments",
    expect.objectContaining({
      body: {
        roleDefinitionId: role.id,
        membershipId: "known-member",
        namespaceId: "known-namespace",
        reason: "Reviewed policy change",
      },
    }),
  );
  expect(w.get).toHaveBeenCalledWith(
    "/api/v1/access/roles",
    expect.objectContaining({
      params: { query: { limit: 25, state: "active" } },
    }),
  );
});

it("explains known tenant-only and protected group eligibility without broadening the assignment", async () => {
  const w = setup("access-assignments");
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation(async (path, ...args) =>
    path === "/api/v1/access/roles/{roleId}"
      ? reply({
          ...role,
          builtin: true,
          key: "tenant_admin",
          namespaceCapable: false,
          permissions: ["access.manage"],
        })
      : path === "/api/v1/access/roles"
        ? reply({
            items: [
              {
                ...role,
                namespaceCapable: false,
                permissions: ["access.manage"],
              },
            ],
            authorizationRevision: 3,
          })
        : original(path, ...args),
  );
  render(<Authorization {...w.props} />);
  await screen.findByRole("option", { name: /Tenant only/ });
  change("Role", role.id);
  change("Assignment scope", "namespace");
  expect(
    screen.getByRole("button", { name: "Grant access assignment" }),
  ).toHaveProperty("disabled", true);
  change("Assignment scope", "tenant");
  change("Assignment target", "groupId");
  await screen.findByRole("option", { name: /Metadata readers/ });
  change("Group", group.id);
  expect(
    screen.getByRole("button", { name: "Grant access assignment" }),
  ).toHaveProperty("disabled", true);
  change("Assignment target", "membershipId");
  expect(
    screen.getByRole("button", { name: "Grant access assignment" }),
  ).toHaveProperty("disabled", false);
  expect(w.post).not.toHaveBeenCalled();
});

it("filters later-page group member choices to the group's active tenant and realm, retaining manual fallback", async () => {
  const w = setup("access-groups", group.id);
  const original = w.get.getMockImplementation()!;
  const membership = {
    id: "known-member",
    tenantId: role.tenantId,
    realmId: group.realmId,
    subject: "Same-realm human",
    actorType: "human",
    state: "active",
    revision: 1,
    createdAt: role.createdAt,
    updatedAt: role.updatedAt,
  };
  w.get.mockImplementation(async (path, options) =>
    path === "/api/v1/memberships"
      ? reply({
          ...((options as { params: { query: { cursor?: string } } }).params
            .query.cursor
            ? {
                items: [
                  membership,
                  { ...membership, id: "wrong-realm", realmId: "other" },
                  { ...membership, id: "wrong-tenant", tenantId: "other" },
                  { ...membership, id: "inactive", state: "suspended" },
                ],
              }
            : { items: [], nextCursor: "members-next" }),
        })
      : original(path, options),
  );
  w.post.mockResolvedValue(reply(member));
  render(<Authorization {...w.props} />);
  const picker = (await screen.findByLabelText("Member")).closest(
    ".workspace-field",
  )!.parentElement!;
  const next = await within(picker).findByRole("button", { name: "Next page" });
  await waitFor(() => expect(next).toHaveProperty("disabled", false));
  fireEvent.click(next);
  await screen.findByRole("option", { name: /Same-realm human/ });
  expect(screen.queryByRole("option", { name: /wrong-|inactive/ })).toBeNull();
  expect(w.get).toHaveBeenCalledWith(
    "/api/v1/memberships",
    expect.objectContaining({
      params: {
        query: {
          limit: 25,
          state: "active",
          realmId: group.realmId,
          cursor: "members-next",
        },
      },
    }),
  );
  change("Member", membership.id);
  fillReason("Add group member");
  await confirmCommand("Add group member");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/access/groups/{groupId}/members",
    expect.objectContaining({
      body: { membershipId: membership.id, reason: "Reviewed policy change" },
    }),
  );
});

it("keeps namespace ID fallback usable without namespace inventory permission", async () => {
  const w = setup("access-assignments");
  w.post.mockResolvedValue(
    reply({
      ...assignment,
      roleDefinitionId: "off-page-role",
      membershipId: "member-b",
      namespaceId: "manual-namespace",
    }),
  );
  render(
    <Authorization
      {...w.props}
      access={{
        ...w.props.access,
        permissions: ["access.manage"],
        availablePermissions: ["access.manage"],
      }}
    />,
  );
  await screen.findByRole("heading", { name: "Grant access assignment" });
  change("Assignment scope", "namespace");
  change("Namespace ID", "manual-namespace");
  change("Role definition ID", "off-page-role");
  change("Member or group ID", "member-b");
  fillReason("Grant access assignment");
  fireEvent.click(
    screen.getByRole("button", { name: "Grant access assignment" }),
  );
  const dialog = await screen.findByRole("dialog");
  const facts = within(dialog).getByLabelText("Command summary");
  expect(facts.textContent).toContain("Exact role ID supplied");
  expect(facts.textContent).toContain("Action");
  expect(facts.textContent).toContain("Grant access");
  expect(facts.textContent).toContain("Reason");
  expect(facts.textContent).toContain("Reviewed policy change");
  expect(facts.textContent).toContain("Exact namespace");
  expect(facts.textContent).toContain("manual-namespace");
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
  await screen.findByText(/Access assigned/);
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/access/assignments",
    expect.objectContaining({
      body: {
        roleDefinitionId: "off-page-role",
        membershipId: "member-b",
        namespaceId: "manual-namespace",
        reason: "Reviewed policy change",
      },
    }),
  );
  expect(w.get.mock.calls.some(([path]) => path === "/api/v1/namespaces")).toBe(
    false,
  );
});

it("distinguishes built-in tenant-only roles from custom namespace-capable roles in the inventory", async () => {
  const w = setup();
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation(async (path, options) =>
    path === "/api/v1/access/roles"
      ? reply({
          items: [
            role,
            {
              ...role,
              id: "administrator-role",
              builtin: true,
              name: "Tenant administrator",
              permissions: ["access.manage"],
              namespaceCapable: false,
            },
          ],
          authorizationRevision: 3,
        })
      : original(path, options),
  );
  render(<Authorization {...w.props} />);
  const builtin = await screen.findByRole("link", {
    name: "Tenant administrator",
  });
  expect(builtin.closest(".policy-record")?.textContent).toContain("Built-in");
  expect(builtin.closest(".policy-record")?.textContent).toContain(
    "Tenant only",
  );
  const custom = screen.getByRole("link", { name: role.name });
  expect(custom.closest(".policy-record")?.textContent).toContain("Custom");
  expect(custom.closest(".policy-record")?.textContent).toContain(
    "Namespace or tenant",
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("shows retained group and revoked assignment paths without a reason form until a live row is selected", async () => {
  const w = setup("access-assignments");
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation(async (path, ...args) =>
    path === "/api/v1/access/assignments"
      ? reply({
          items: [
            {
              ...assignment,
              membershipId: undefined,
              groupId: group.id,
              namespaceId: undefined,
            },
            {
              ...assignment,
              id: "revoked-path",
              state: "revoked",
              revokedBy: "revoker",
              revokeReason: "Retired project",
              revokedAt: role.updatedAt,
            },
          ],
          authorizationRevision: 3,
        })
      : original(path, ...args),
  );
  render(<Authorization {...w.props} />);
  await screen.findByRole("link", { name: /Metadata readers/ });
  const evidence = screen.getAllByText("Assignment attribution");
  expect(evidence).toHaveLength(2);
  expect(evidence[1]!.closest("details")!.textContent).toContain(
    "Retired project",
  );
  expect(evidence[0]!.closest("details")!.textContent).toContain(
    assignment.grantReason,
  );
  expect(w.post).not.toHaveBeenCalled();
  expect(
    screen.queryByRole("heading", { name: "Revoke access assignment" }),
  ).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Manage assignment revoked-path" }),
  ).toBeNull();
  fireEvent.click(
    screen.getByRole("button", { name: `Manage assignment ${assignment.id}` }),
  );
  expect(
    screen.getAllByRole("heading", { name: "Revoke access assignment" }),
  ).toHaveLength(1);
  fillReason("Revoke access assignment", "Remove only this group path");
  fireEvent.click(
    screen.getByRole("button", { name: "Revoke access assignment" }),
  );
  const dialog = await screen.findByRole("dialog");
  const facts = within(dialog).getByLabelText("Command summary");
  for (const text of [
    assignment.id,
    assignment.roleDefinitionId,
    group.id,
    "None — group assignment",
    "Tenant-wide",
    "Not namespace-scoped",
    '"rev-3"',
    "Remove only this group path",
  ])
    expect(facts.textContent).toContain(text);
  expect(w.post).not.toHaveBeenCalled();
  w.post.mockResolvedValue(
    reply({
      ...assignment,
      membershipId: undefined,
      groupId: group.id,
      namespaceId: undefined,
      state: "revoked",
      revision: 4,
    }),
  );
  fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
  await screen.findByText(/Assignment revoked/);
  expect(w.post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/access/assignments/{assignmentId}:revoke",
    expect.objectContaining({
      params: {
        path: { assignmentId: assignment.id },
        header: {
          "Idempotency-Key": expect.any(String) as unknown,
          "If-Match": '"rev-3"',
        },
      },
      body: { reason: "Remove only this group path" },
    }),
  );
});

it.each([
  ["Role", "/api/v1/access/roles"],
  ["Member", "/api/v1/memberships"],
  ["Group", "/api/v1/access/groups"],
  ["Namespace", "/api/v1/namespaces"],
] as const)(
  "uses the %s choice continuation without paging the assignment inventory or submitting a draft",
  async (label, endpoint) => {
    const w = setup("access-assignments");
    const original = w.get.getMockImplementation()!;
    const id = "second-page-choice";
    const choice =
      label === "Role"
        ? { ...role, id, name: "Later role" }
        : label === "Group"
          ? { ...group, id, name: "Later group" }
          : label === "Member"
            ? {
                id,
                tenantId: role.tenantId,
                realmId: group.realmId,
                subject: "Later member",
                actorType: "human",
                state: "active",
                revision: 1,
                createdAt: role.createdAt,
                updatedAt: role.updatedAt,
              }
            : {
                id,
                tenantId: role.tenantId,
                slug: "later",
                displayName: "Later namespace",
                revision: 1,
                createdAt: role.createdAt,
                updatedAt: role.updatedAt,
              };
    w.get.mockImplementation(async (path, options) => {
      if (path !== endpoint) return original(path, options);
      const query = (options as { params: { query: { cursor?: string } } })
        .params.query;
      return reply({
        items: query.cursor
          ? [
              choice,
              { ...choice, id: "foreign-choice", tenantId: "other" },
              ...(label === "Member" || label === "Group"
                ? [{ ...choice, id: "wrong-realm-choice", realmId: "other" }]
                : []),
              ...(label !== "Namespace"
                ? [
                    {
                      ...choice,
                      id: "inactive-choice",
                      state: label === "Role" ? "archived" : "suspended",
                    },
                  ]
                : []),
            ]
          : [],
        authorizationRevision: 3,
        ...(query.cursor ? {} : { nextCursor: "choices-next" }),
      });
    });
    w.post.mockResolvedValue(
      reply({
        ...assignment,
        roleDefinitionId: label === "Role" ? id : role.id,
        membershipId:
          label === "Group" ? undefined : label === "Member" ? id : "member-b",
        groupId: label === "Group" ? id : undefined,
        namespaceId: label === "Namespace" ? id : undefined,
      }),
    );
    render(<Authorization {...w.props} />);
    await screen.findByRole("button", {
      name: `Manage assignment ${assignment.id}`,
    });
    if (label === "Group") change("Assignment target", "groupId");
    if (label === "Namespace") change("Assignment scope", "namespace");
    const picker = screen
      .getByLabelText(label)
      .closest(".workspace-field")!.parentElement!;
    const next = await within(picker).findByRole("button", {
      name: "Next page",
    });
    await waitFor(() => expect(next).toHaveProperty("disabled", false));
    fillReason("Grant access assignment", "Keep this grant draft");
    fireEvent.click(next);
    await within(picker).findByRole("option", { name: /Later/ });
    expect(
      within(picker).queryByRole("option", {
        name: /foreign-choice|wrong-realm-choice|inactive-choice/,
      }),
    ).toBeNull();
    expect(w.get).toHaveBeenCalledWith(
      endpoint,
      expect.objectContaining({
        params: {
          query: {
            limit: 25,
            ...(label !== "Namespace" ? { state: "active" } : {}),
            ...(label === "Member" ? { realmId: group.realmId } : {}),
          },
        },
      }),
    );
    expect(w.get).toHaveBeenCalledWith(
      endpoint,
      expect.objectContaining({
        params: {
          query: {
            limit: 25,
            ...(label !== "Namespace" ? { state: "active" } : {}),
            ...(label === "Member" ? { realmId: group.realmId } : {}),
            cursor: "choices-next",
          },
        },
      }),
    );
    expect(w.get.mock.calls.filter(([path]) => path === endpoint)).toHaveLength(
      2,
    );
    expect(
      w.get.mock.calls.filter(
        ([path]) => path === "/api/v1/access/assignments",
      ),
    ).toHaveLength(1);
    expect(w.post).not.toHaveBeenCalled();
    change(label, id);
    if (label !== "Role") change("Role definition ID", role.id);
    if (label !== "Member" && label !== "Group")
      change("Member or group ID", "member-b");
    if (label !== "Namespace") change("Assignment scope", "tenant");
    await confirmCommand("Grant access assignment");
    await screen.findByText(/Access assigned/);
    expect(w.post).toHaveBeenCalledExactlyOnceWith(
      "/api/v1/access/assignments",
      expect.objectContaining({
        body: {
          roleDefinitionId: label === "Role" ? id : role.id,
          ...(label === "Group"
            ? { groupId: id }
            : { membershipId: label === "Member" ? id : "member-b" }),
          ...(label === "Namespace" ? { namespaceId: id } : {}),
          reason: "Keep this grant draft",
        },
      }),
    );
    expect(w.patch).not.toHaveBeenCalled();
    expect(w.remove).not.toHaveBeenCalled();
  },
);

it.each(["membership", "assignment"] as const)(
  "preserves a dirty %s add/grant draft when failed inventory Reload is cancelled",
  async (kind) => {
    const isMember = kind === "membership";
    const w = setup(
      isMember ? "access-groups" : "access-assignments",
      isMember ? group.id : "",
    );
    const endpoint = isMember
      ? "/api/v1/access/groups/{groupId}/members"
      : "/api/v1/access/assignments";
    const original = w.get.getMockImplementation()!;
    w.get.mockImplementation(async (path, options) => {
      if (path !== endpoint) return original(path, options);
      const query = (options as { params: { query: { cursor?: string } } })
        .params.query;
      if (query.cursor)
        throw new APIError(400, "invalid_cursor", "changed-policy");
      return reply({
        items: [isMember ? member : assignment],
        authorizationRevision: 3,
        nextCursor: "stale-page",
      });
    });
    render(<Authorization {...w.props} />);
    await screen.findByRole("button", {
      name: isMember
        ? `Manage membership ${member.membershipId}`
        : `Manage assignment ${assignment.id}`,
    });
    fireEvent.click(screen.getAllByRole("button", { name: "Next page" })[0]!);
    await screen.findByRole("alert");
    const title = isMember ? "Add group member" : "Grant access assignment";
    const idLabel = isMember ? "Membership ID" : "Member or group ID";
    fillReason(title, "Keep this unsent draft");
    change(idLabel, "draft-member");
    const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
    const reads = w.get.mock.calls.length;
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    expect(discard).toHaveBeenCalledOnce();
    expect(w.get.mock.calls).toHaveLength(reads);
    expect(screen.getByRole("alert").textContent).toContain("invalid_cursor");
    expect(screen.getByLabelText(idLabel)).toHaveProperty(
      "value",
      "draft-member",
    );
    expect(
      within(
        screen.getByRole("heading", { name: title }).closest("section")!,
      ).getByLabelText(
        /^(Reason|Metadata change reason|Lifecycle change reason)$/,
      ),
    ).toHaveProperty("value", "Keep this unsent draft");
    discard.mockReturnValue(true);
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(w.get).toHaveBeenLastCalledWith(
      endpoint,
      expect.objectContaining({
        params: isMember
          ? { path: { groupId: group.id }, query: { limit: 25 } }
          : { query: { limit: 25 } },
      }),
    );
    expect(w.post).not.toHaveBeenCalled();
    expect(w.patch).not.toHaveBeenCalled();
    expect(w.remove).not.toHaveBeenCalled();
  },
);

for (const [section, path, label, description] of [
  [
    "access-roles",
    "/api/v1/access/roles",
    "roles",
    "Choose permissions for custom roles and inspect built-in roles.",
  ],
  [
    "access-groups",
    "/api/v1/access/groups",
    "groups",
    "Group members so they can share role assignments.",
  ],
  [
    "access-assignments",
    "/api/v1/access/assignments",
    "assignments",
    "Assign roles to members or groups for a tenant or namespace.",
  ],
]) {
  it(`presents an empty ${label} page without commands or invented results`, async () => {
    const w = setup(section);
    const original = w.get.getMockImplementation()!;
    w.get.mockImplementation(async (request, options) =>
      request === path
        ? reply({ items: [], authorizationRevision: 3 })
        : original(request, options),
    );
    render(<Authorization {...w.props} />);
    await screen.findByText(`No ${label} on this page.`);
    expect(screen.getByText(description!)).toBeTruthy();
    expect(
      screen.getByText(/Changes take effect without restarting services/),
    ).toBeTruthy();
    expect(
      screen.queryByRole("button", { name: /^Manage assignment/ }),
    ).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  });
}

it.each(["roles", "groups"] as const)(
  "validates Unicode names/descriptions before confirming custom %s",
  async (kind) => {
    const w = setup(`access-${kind}`, "new");
    render(<Authorization {...w.props} />);
    const name = await screen.findByLabelText(
      kind === "roles" ? "Role name" : "Group name",
    );
    // Valid companions remain selected explicitly; test only the display metadata boundary.
    if (kind === "roles")
      fireEvent.click(await screen.findByLabelText(/resource.read$/));
    const reason = screen.getByLabelText(
      /^(Reason|Metadata change reason|Lifecycle change reason)$/,
    );
    fireEvent.change(reason, { target: { value: "Create reviewed metadata" } });
    for (const [control, limit] of [
      [name, 120],
      [
        screen.getByLabelText(
          kind === "roles" ? "Role description" : "Group description",
        ),
        2000,
      ],
    ] as const) {
      fireEvent.change(name, { target: { value: "Unicode role" } });
      fireEvent.change(control, {
        target: { value: "🙂".repeat(limit) + "a" },
      });
      fireEvent.click(
        screen.getByRole("button", {
          name: kind === "roles" ? "Create role" : "Create group",
        }),
      );
      expect(screen.getByRole("alert").textContent).toContain(
        `${limit} characters`,
      );
      expect(screen.queryByRole("dialog")).toBeNull();
      fireEvent.change(control, { target: { value: "🙂".repeat(limit) } });
      fireEvent.click(
        screen.getByRole("button", {
          name: kind === "roles" ? "Create role" : "Create group",
        }),
      );
      expect(screen.getByRole("dialog")).toBeTruthy();
      fireEvent.click(
        within(screen.getByRole("dialog")).getByRole("button", {
          name: "Cancel",
        }),
      );
    }
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("searches permission labels and keys without changing exact selections or required companions", async () => {
  const w = setup("access-roles", "new");
  w.post.mockResolvedValue(reply(role));
  render(<Authorization {...w.props} />);
  await screen.findByLabelText("ratings.sharing.manage");
  expect(screen.getByText("Select at least one permission.")).toBeTruthy();
  expect(screen.getByLabelText("Role name")).toHaveProperty("readOnly", false);
  change("Role name", "Useful explicit permissions");
  fillReason("Create role");
  change("Find permissions", "rating summary sharing");
  expect(
    fireEvent.keyDown(screen.getByLabelText("Find permissions"), {
      key: "Enter",
    }),
  ).toBe(false);
  expect(
    fireEvent.keyDown(screen.getByLabelText("Find permissions"), {
      key: "ArrowDown",
    }),
  ).toBe(true);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.getByText("Manage rating summary sharing")).toBeTruthy();
  fireEvent.click(screen.getByLabelText("ratings.sharing.manage"));
  fireEvent.click(screen.getByLabelText("Show selected permissions only"));
  change("Find permissions", "RESOURCE.CREATE");
  expect(screen.getByText("0 matching permissions.")).toBeTruthy();
  expect(screen.getByText(/No permissions match these filters/)).toBeTruthy();
  expect(screen.getByText(/1 permission selected/)).toBeTruthy();
  fireEvent.click(screen.getByLabelText("Show selected permissions only"));
  fireEvent.click(screen.getByLabelText("resource.create"));
  expect(screen.getByText(/Required companions missing/)).toHaveProperty(
    "textContent",
    expect.stringContaining("namespace.read"),
  );
  expect(screen.getByRole("button", { name: "Create role" })).toHaveProperty(
    "disabled",
    true,
  );
  change("Find permissions", "namespace.read");
  fireEvent.click(screen.getByLabelText("namespace.read"));
  fireEvent.click(screen.getByLabelText("Show selected permissions only"));
  change("Find permissions", "");
  for (const key of [
    "ratings.sharing.manage",
    "resource.create",
    "namespace.read",
  ])
    expect(screen.getByLabelText(key)).toHaveProperty("checked", true);
  expect(screen.getByText("3 matching permissions.")).toBeTruthy();
  fireEvent.click(screen.getByLabelText("Show selected permissions only"));
  change("Find permissions", "access.manage");
  expect(screen.getByLabelText("access.manage")).toHaveProperty(
    "disabled",
    true,
  );
  expect(screen.getByText(/Not delegable/)).toBeTruthy();
  change("Find permissions", "no matching action");
  expect(w.post).not.toHaveBeenCalled();
  await confirmCommand("Create role");
  await screen.findByText(/Role saved/);
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/access/roles",
    expect.objectContaining({
      body: {
        name: "Useful explicit permissions",
        description: "",
        reason: "Reviewed policy change",
        permissions: [
          "ratings.sharing.manage",
          "resource.create",
          "namespace.read",
        ],
      },
    }),
  );
});

it.each(["pending", "failed"] as const)(
  "explains %s permission loading without disabling editable prerequisites",
  async (mode) => {
    const w = setup("access-roles", "new");
    const original = w.get.getMockImplementation()!;
    const pending = deferred<ReturnType<typeof reply>>();
    w.get.mockImplementation((path, options) =>
      path === "/api/v1/access/permissions"
        ? pending.promise
        : original(path, options),
    );
    render(<Authorization {...w.props} />);
    expect(screen.getByText("Loading available permissions.")).toBeTruthy();
    change("Role name", "Preserve this draft");
    if (mode === "failed") {
      await act(async () =>
        pending.reject(new APIError(503, "DEPENDENCY_UNAVAILABLE")),
      );
      expect(
        screen.getByText(
          "Load the available permissions before preparing a role.",
        ),
      ).toBeTruthy();
    } else {
      await act(async () => pending.resolve(reply(descriptors)));
      expect(screen.getByText("Select at least one permission.")).toBeTruthy();
    }
    expect(screen.getByLabelText("Role name")).toHaveProperty(
      "value",
      "Preserve this draft",
    );
    expect(screen.getByRole("button", { name: "Create role" })).toHaveProperty(
      "disabled",
      true,
    );
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["role", "group"] as const)(
  "keeps archived %s metadata readable without an unsavable draft",
  async (kind) => {
    const w = setup(`access-${kind}s`, kind === "role" ? role.id : group.id);
    const original = w.get.getMockImplementation()!;
    w.get.mockImplementation(async (path, options) =>
      path === `/api/v1/access/${kind}s/{${kind}Id}`
        ? etagReply({ ...(kind === "role" ? role : group), state: "archived" })
        : original(path, options),
    );
    render(<Authorization {...w.props} />);
    await screen.findByRole("heading", {
      name: kind === "role" ? "Role permissions" : "Group overview",
    });
    expect(
      screen.queryByLabelText(kind === "role" ? "Role name" : "Group name"),
    ).toBeNull();
    expect(screen.queryByLabelText("Metadata change reason")).toBeNull();
    expect(
      screen.queryByRole("button", {
        name: kind === "role" ? "Update role" : "Update group",
      }),
    ).toBeNull();
    expect(
      window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    ).toBe(true);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("keeps suspended group metadata editable and chooses reactivation without implying it is active", async () => {
  const w = setup("access-groups", group.id);
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation(async (path, options) =>
    path === "/api/v1/access/groups/{groupId}"
      ? etagReply({ ...group, state: "suspended" })
      : original(path, options),
  );
  render(<Authorization {...w.props} />);
  expect(await screen.findByLabelText("Group name")).toHaveProperty(
    "readOnly",
    false,
  );
  expect(screen.getByRole("button", { name: "Update group" })).toHaveProperty(
    "disabled",
    false,
  );
  expect(screen.getByLabelText("Group action")).toHaveProperty(
    "value",
    "reactivate",
  );
  expect(
    screen.getByRole("button", { name: "Change group state" }),
  ).toHaveProperty("disabled", false);
  change("Group action", "suspend");
  expect(
    screen.getByText(
      "Choose a change supported by this group's current state.",
    ),
  ).toBeTruthy();
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["membership", "assignment"] as const)(
  "focuses the selected %s editor and restores its opener only after accepted close",
  async (kind) => {
    const isMember = kind === "membership";
    const w = setup(
      isMember ? "access-groups" : "access-assignments",
      isMember ? group.id : "",
    );
    const view = render(<Authorization {...w.props} />);
    const opener = await screen.findByRole("button", {
      name: isMember
        ? `Manage membership ${member.membershipId}`
        : `Manage assignment ${assignment.id}`,
    });
    opener.focus();
    fireEvent.click(opener);
    const editor = screen.getByRole("region", {
      name: isMember
        ? "Selected group membership editor"
        : "Selected access assignment editor",
    });
    expect(document.activeElement).toBe(editor);
    const reason = within(editor).getByLabelText(
      /^(Reason|Metadata change reason|Lifecycle change reason)$/,
    );
    reason.focus();
    view.rerender(<Authorization {...w.props} />);
    expect(document.activeElement).toBe(reason);
    fireEvent.click(opener);
    expect(document.activeElement).toBe(editor);
    fireEvent.change(reason, {
      target: { value: "Keep this exact unsent reason" },
    });
    const close = within(editor).getByRole("button", {
      name: isMember ? "Close membership editor" : "Close assignment editor",
    });
    const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
    close.focus();
    fireEvent.click(close);
    expect(document.activeElement).toBe(close);
    expect(reason).toHaveProperty("value", "Keep this exact unsent reason");
    discard.mockReturnValue(true);
    fireEvent.click(close);
    expect(document.activeElement).toBe(opener);
    expect(
      screen.queryByRole("region", {
        name: isMember
          ? "Selected group membership editor"
          : "Selected access assignment editor",
      }),
    ).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["role", "group"] as const)(
  "preserves an active custom %s draft when its response has no current revision",
  async (kind) => {
    const record = kind === "role" ? role : group;
    const w = setup(`access-${kind}s`, record.id);
    const original = w.get.getMockImplementation()!;
    w.get.mockImplementation(async (path, options) =>
      path === `/api/v1/access/${kind}s/{${kind}Id}`
        ? { data: record, response: new Response() }
        : original(path, options),
    );
    render(<Authorization {...w.props} />);
    const title = kind === "role" ? "Update role" : "Update group";
    const name = await screen.findByLabelText(
      kind === "role" ? "Role name" : "Group name",
    );
    const panel = screen
      .getByRole("heading", { name: title })
      .closest("section")!;
    const description = within(panel).getByLabelText(
      kind === "role" ? "Role description" : "Group description",
    );
    const reason = within(panel).getByLabelText(
      /^(Reason|Metadata change reason|Lifecycle change reason)$/,
    );
    for (const control of [name, description, reason])
      expect(control).toHaveProperty("readOnly", false);
    fireEvent.change(name, { target: { value: "Keep this unsaved name" } });
    fireEvent.change(description, {
      target: { value: "Keep this unsaved description" },
    });
    fireEvent.change(reason, { target: { value: "Keep this unsaved reason" } });
    expect(
      within(panel).getByText(
        `Reload the ${kind} to obtain its current revision.`,
      ),
    ).toBeTruthy();
    expect(within(panel).getByRole("button", { name: title })).toHaveProperty(
      "disabled",
      true,
    );
    fireEvent.submit(
      within(panel).getByRole("button", { name: title }).closest("form")!,
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(
      window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    ).toBe(false);
    const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
    const reads = w.get.mock.calls.length;
    fireEvent.click(
      screen.getByRole("button", { name: "Refresh assignments" }),
    );
    expect(discard).toHaveBeenCalled();
    expect(w.get).toHaveBeenCalledTimes(reads);
    expect(name).toHaveProperty("value", "Keep this unsaved name");
    expect(description).toHaveProperty(
      "value",
      "Keep this unsaved description",
    );
    expect(reason).toHaveProperty("value", "Keep this unsaved reason");
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["access-roles", "access-groups"])(
  "keeps metadata and lifecycle reasons independent for %s",
  async (section) => {
    const w = setup(section, section === "access-roles" ? role.id : group.id);
    w.post.mockResolvedValue(
      etagReply(
        section === "access-roles"
          ? { ...role, state: "archived" }
          : { ...group, state: "suspended" },
      ),
    );
    render(<Authorization {...w.props} />);
    await screen.findByLabelText("Metadata change reason");
    change("Lifecycle change reason", "Only this transition");
    expect(screen.getByLabelText("Metadata change reason")).toHaveProperty(
      "value",
      "",
    );
    await confirmCommand(
      section === "access-roles" ? "Archive role" : "Change group state",
    );
    await screen.findByText(
      section === "access-roles" ? /Role archived/ : /Group state changed/,
    );
    expect(screen.getByLabelText("Lifecycle change reason")).toHaveProperty(
      "value",
      "",
    );
    await waitFor(() => {
      const clean = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(clean);
      expect(clean.defaultPrevented).toBe(false);
    });
    change("Metadata change reason", "Keep this independent draft");
    const dirty = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(dirty);
    expect(dirty.defaultPrevented).toBe(true);
  },
);

it.each(["", "?membershipId=member-preset", "?groupId=group-preset"])(
  "resets assignment operand in both directions while preserving policy inputs (%s)",
  async (preset) => {
    const w = setup("access-assignments");
    history.replaceState(null, "", `/unit/access-assignments${preset}`);
    render(<Authorization {...w.props} />);
    await screen.findByRole("heading", { name: "Grant access assignment" });
    change("Role definition ID", role.id);
    fillReason("Grant access assignment", "Keep policy reason");
    for (const type of ["groupId", "membershipId", "groupId"]) {
      change("Member or group ID", "exact-previous-operand");
      const selector = screen.getByLabelText(
        "Assignment target",
      ) as HTMLSelectElement;
      if (selector.value === type)
        change(
          "Assignment target",
          type === "groupId" ? "membershipId" : "groupId",
        );
      change("Assignment target", type);
      expect(screen.getByLabelText("Member or group ID")).toHaveProperty(
        "value",
        "",
      );
      expect(screen.getByLabelText("Role definition ID")).toHaveProperty(
        "value",
        role.id,
      );
      expect(screen.getByLabelText("Reason")).toHaveProperty(
        "value",
        "Keep policy reason",
      );
    }
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["member", "assignment"])(
  "supports two deliberate consecutive %s grants with fresh keys and bounded refresh",
  async (kind) => {
    const isMember = kind === "member";
    const w = setup(
      isMember ? "access-groups" : "access-assignments",
      isMember ? group.id : "",
    );
    w.post.mockResolvedValue(etagReply(isMember ? member : assignment));
    render(<Authorization {...w.props} />);
    const title = isMember ? "Add group member" : "Grant access assignment";
    await screen.findByRole("heading", { name: title });
    if (!isMember) {
      change("Role definition ID", role.id);
      change("Assignment scope", "tenant");
    }
    const label = isMember ? "Membership ID" : "Member or group ID";
    for (let i = 0; i < 2; i++) {
      change(label, `member-${i}`);
      fillReason(title, `Reason ${i}`);
      await confirmCommand(title);
      await screen.findByText(
        isMember
          ? "Group member added."
          : "Access assigned. Refresh My access in the affected session.",
      );
      expect(w.post).toHaveBeenCalledTimes(i + 1);
      if (!i) {
        click(
          isMember
            ? "Add another group member"
            : "Grant another access assignment",
        );
        expect(screen.getByLabelText(label)).toHaveProperty("value", "");
        expect(
          within(
            screen.getByRole("heading", { name: title }).closest("section")!,
          ).getByLabelText("Reason"),
        ).toHaveProperty("value", "");
      }
    }
    expect(w.post.mock.calls[0]?.[1]).toMatchObject({
      body: { reason: "Reason 0" },
    });
    expect(w.post.mock.calls[1]?.[1]).toMatchObject({
      body: { reason: "Reason 1" },
    });
    const first = w.post.mock.calls[0]?.[1] as {
      params: { header: Record<string, string> };
    };
    const second = w.post.mock.calls[1]?.[1] as {
      params: { header: Record<string, string> };
    };
    expect(first.params.header["Idempotency-Key"]).not.toBe(
      second.params.header["Idempotency-Key"],
    );
    const path = isMember
      ? "/api/v1/access/groups/{groupId}/members"
      : "/api/v1/access/assignments";
    await waitFor(() =>
      expect(w.get.mock.calls.filter(([p]) => p === path)).toHaveLength(3),
    );
  },
);

it.each(["access-roles", "access-groups"])(
  "preserves a real sibling metadata reason after %s lifecycle success",
  async (section) => {
    const w = setup(section, section === "access-roles" ? role.id : group.id);
    w.post.mockResolvedValue(
      etagReply(
        section === "access-roles"
          ? { ...role, state: "archived" }
          : { ...group, state: "suspended" },
      ),
    );
    render(<Authorization {...w.props} />);
    await screen.findByLabelText("Metadata change reason");
    change("Metadata change reason", "Keep this metadata draft");
    change("Lifecycle change reason", "Only consume this transition reason");
    await confirmCommand(
      section === "access-roles" ? "Archive role" : "Change group state",
    );
    await screen.findByText(
      section === "access-roles" ? /Role archived/ : /Group state changed/,
    );
    expect(screen.getByLabelText("Metadata change reason")).toHaveProperty(
      "value",
      "Keep this metadata draft",
    );
    expect(screen.getByLabelText("Lifecycle change reason")).toHaveProperty(
      "value",
      "",
    );
    const leave = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(leave);
    expect(leave.defaultPrevented).toBe(true);
  },
);

it("keeps entered member and assignment drafts disabled when their group becomes terminal", async () => {
  const w = setup("access-groups", group.id);
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation((path, options) =>
    path === "/api/v1/access/groups/{groupId}/members" ||
    path === "/api/v1/access/assignments"
      ? Promise.resolve(reply({ items: [], authorizationRevision: 3 }))
      : original(path, options),
  );
  w.post.mockResolvedValue(
    reply({ ...group, state: "archived", revision: group.revision + 1 }),
  );
  render(<Authorization {...w.props} />);
  await screen.findByText(group.name, { selector: "h1" });
  fillReason("Add group member", "Keep member reason");
  change("Membership ID", "draft-member");
  fillReason("Grant access assignment", "Keep assignment reason");
  change("Role definition ID", role.id);
  change("Assignment scope", "tenant");
  change("Group action", "archive");
  fillReason("Change group state", "Archive empty saved group");
  await confirmCommand("Change group state");
  await screen.findByText(/Group state changed/);
  for (const [title, reason] of [
    ["Add group member", "Keep member reason"],
    ["Grant access assignment", "Keep assignment reason"],
  ]) {
    const region = screen
      .getByText(title!, { selector: "h2" })
      .closest("section")!;
    const control = within(region).getByLabelText("Reason");
    expect(control).toHaveProperty("value", reason);
    expect(control.closest("fieldset")).toHaveProperty("disabled", true);
  }
  expect(screen.getByLabelText("Membership ID")).toHaveProperty(
    "value",
    "draft-member",
  );
  expect(screen.getByLabelText("Role definition ID")).toHaveProperty(
    "value",
    role.id,
  );
  expect(
    window.dispatchEvent(new Event("beforeunload", { cancelable: true })),
  ).toBe(false);
  expect(w.post).toHaveBeenCalledTimes(1);
  expect(w.post.mock.calls[0]?.[0]).toBe(
    "/api/v1/access/groups/{groupId}:archive",
  );
});

it.each(["roles", "groups"] as const)(
  "orients denied %s details without disclosing names, then admits newly granted authority",
  async (kind) => {
    const w = setup(`access-${kind}`, kind === "roles" ? role.id : group.id);
    const denied = {
      ...administrator,
      permissions: [],
      availablePermissions: [],
    };
    const original = w.get.getMockImplementation()!;
    w.get.mockImplementation((path, options) =>
      path ===
      `/api/v1/access/${kind}/{${kind === "roles" ? "roleId" : "groupId"}}`
        ? Promise.resolve(
            etagReply({
              ...(kind === "roles" ? role : group),
              description: "",
            }),
          )
        : original(path, options),
    );
    const view = render(<Authorization {...w.props} access={denied} />);
    expect(
      screen.getByRole("heading", {
        name: kind === "roles" ? "Roles" : "Groups",
      }),
    ).toBeTruthy();
    expect(screen.getByRole("link", { name: "Review My access" })).toBeTruthy();
    expect(
      screen.queryByRole("heading", {
        name: kind === "roles" ? role.name : group.name,
      }),
    ).toBeNull();
    expect(w.get).not.toHaveBeenCalled();
    view.rerender(<Authorization {...w.props} />);
    await screen.findByRole("heading", {
      name: kind === "roles" ? role.name : group.name,
    });
    expect(
      screen.getByText(
        kind === "roles"
          ? "Inspect this role and its assignments."
          : "Inspect this group and its access paths.",
      ),
    ).toBeTruthy();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("shows tenant-only scope changes accurately without submitting the editable role", async () => {
  const w = setup("access-roles", role.id);
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation((path, options) =>
    path === "/api/v1/access/roles/{roleId}"
      ? Promise.resolve(
          etagReply({
            ...role,
            namespaceCapable: false,
            permissions: ["settings.read"],
          }),
        )
      : original(path, options),
  );
  render(<Authorization {...w.props} />);
  const setting = await screen.findByLabelText("settings.read");
  revealEditor(setting);
  fireEvent.click(setting);
  fireEvent.click(screen.getByLabelText("namespace.read"));
  fillReason("Update role", "Narrow this role to namespace actions");
  click("Update role");
  const dialog = await screen.findByRole("dialog");
  expect(
    within(dialog).getByLabelText("Command summary").textContent,
  ).toContain("Tenant only → Namespace or tenant");
  expect(w.patch).not.toHaveBeenCalled();
  click("Cancel");
  expect(screen.getByLabelText("Metadata change reason")).toHaveProperty(
    "value",
    "Narrow this role to namespace actions",
  );
});

it("keeps an inspected built-in role and grant draft when a custom copy is declined", async () => {
  const w = setup("access-roles", role.id);
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation((path, options) =>
    path === "/api/v1/access/roles/{roleId}"
      ? Promise.resolve(etagReply({ ...role, builtin: true }))
      : original(path, options),
  );
  render(<Authorization {...w.props} />);
  await screen.findByRole("heading", { name: "Role permissions" });
  fillReason("Grant access assignment", "Keep this independent grant reason");
  const decline = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Copy to custom role");
  expect(decline).toHaveBeenCalledOnce();
  expect(screen.queryByLabelText("Role name")).toBeNull();
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Keep this independent grant reason",
  );
  expect(w.post).not.toHaveBeenCalled();
});
