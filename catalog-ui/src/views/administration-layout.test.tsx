// @vitest-environment jsdom
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { expect, it, vi } from "vitest";
import Authorization from "./Authorization";
import Members from "./Members";
import Ratings from "./Ratings";
import {
  role,
  group,
  assignment,
  descriptors,
} from "../../tests/unit/policy-fixtures";
import {
  administrator,
  change,
  etagReply,
  readReplies,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";

screenLifecycle("/unit/access-assignments");
const membership = {
  id: "member-a",
  tenantId: "tenant-a",
  realmId: "realm-a",
  subject: "A colleague",
  actorType: "human",
  state: "active",
  revision: 1,
};
function policyWorkspace() {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/access/roles": { items: [role], authorizationRevision: 3 },
    "/api/v1/access/roles/{roleId}": role,
    "/api/v1/access/permissions": descriptors,
    "/api/v1/access/groups": { items: [group], authorizationRevision: 3 },
    "/api/v1/namespaces": { items: [] },
    "/api/v1/namespaces/{namespaceId}": {
      id: "namespace-a",
      tenantId: "tenant-a",
      slug: "tools",
      displayName: "Tools",
      revision: 1,
      createdAt: stamp,
      updatedAt: stamp,
    },
    "/api/v1/memberships": { items: [membership] },
    "/api/v1/memberships/{membershipId}": membership,
    "/api/v1/access/groups/{groupId}": group,
    "/api/v1/access/assignments": {
      items: [assignment],
      authorizationRevision: 3,
      nextCursor: "next-assignments",
    },
  });
  return { ...w, props: { ...w.props, access: administrator } };
}

it("stages exact assignment filters independently of a protected grant and retains them through paging", async () => {
  const w = policyWorkspace();
  render(<Authorization {...w.props} />);
  await screen.findByRole("heading", { name: "Assignments" });
  fireEvent.click(screen.getByText("Grant access", { selector: "summary" }));
  change("Role", role.id);
  change("Member", membership.id);
  change("Assignment scope", "tenant");
  change("Reason", "Keep this grant draft");
  const before = w.get.mock.calls.filter(
    ([path]) => path === "/api/v1/access/assignments",
  ).length;
  fireEvent.click(
    screen.getByText("Filter assignments", { selector: "summary" }),
  );
  change("Role ID filter", role.id);
  change("Member ID filter", membership.id);
  expect(
    w.get.mock.calls.filter(([path]) => path === "/api/v1/access/assignments"),
  ).toHaveLength(before);
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(
    w.get.mock.calls.filter(([path]) => path === "/api/v1/access/assignments"),
  ).toHaveLength(before);
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
  expect(screen.getByLabelText("Role ID filter")).toHaveProperty(
    "value",
    role.id,
  );
  expect(
    w.get.mock.calls.filter(([path]) => path === "/api/v1/access/assignments"),
  ).toHaveLength(before);
  discard.mockReturnValue(true);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await waitFor(() =>
    expect(w.get).toHaveBeenCalledWith(
      "/api/v1/access/assignments",
      expect.objectContaining({
        params: {
          query: {
            roleDefinitionId: role.id,
            membershipId: membership.id,
            limit: 25,
          },
        },
      }),
    ),
  );
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Keep this grant draft",
  );
  expect(screen.getByLabelText("Role")).toHaveProperty("value", role.id);
  expect(screen.getByLabelText("Assignment scope")).toHaveProperty(
    "value",
    "tenant",
  );
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await waitFor(() =>
    expect(w.get).toHaveBeenCalledWith(
      "/api/v1/access/assignments",
      expect.objectContaining({
        params: {
          query: {
            roleDefinitionId: role.id,
            membershipId: membership.id,
            limit: 25,
            cursor: "next-assignments",
          },
        },
      }),
    ),
  );
  change("Member ID filter", "");
  change("Group ID filter", group.id);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await waitFor(() =>
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/access/assignments",
      expect.objectContaining({
        params: {
          query: { roleDefinitionId: role.id, groupId: group.id, limit: 25 },
        },
        signal: expect.any(AbortSignal) as AbortSignal,
      }),
    ),
  );
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
  await waitFor(() =>
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/access/assignments",
      expect.objectContaining({
        params: { query: { limit: 25 } },
        signal: expect.any(AbortSignal) as AbortSignal,
      }),
    ),
  );
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Keep this grant draft",
  );
  expect(screen.getByLabelText("Role")).toHaveProperty("value", role.id);
  expect(screen.getByLabelText("Group ID filter")).toHaveProperty("value", "");
  expect(w.post).not.toHaveBeenCalled();
});

it("shows selected immutable permissions before the complete vocabulary and offers no mutation editor", async () => {
  history.replaceState(null, "", `/unit/access-roles/${role.id}`);
  const w = policyWorkspace();
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation((path, options) =>
    path === "/api/v1/access/roles/{roleId}"
      ? Promise.resolve(etagReply({ ...role, builtin: true }))
      : original(path, options),
  );
  render(<Authorization {...w.props} id={role.id} />);
  const permissions = (
    await screen.findByRole("heading", { name: "Role permissions" })
  ).closest("section")!;
  await waitFor(() =>
    expect(within(permissions).getAllByRole("checkbox")).toHaveLength(
      role.permissions.length + 1,
    ),
  );
  expect(within(permissions).queryByLabelText("Role name")).toBeNull();
  expect(
    within(permissions).queryByLabelText("Metadata change reason"),
  ).toBeNull();
  fireEvent.click(
    within(permissions).getByRole("checkbox", {
      name: "Show selected permissions only",
    }),
  );
  expect(within(permissions).getAllByRole("checkbox")).toHaveLength(
    descriptors.length + 1,
  );
  for (const permission of descriptors)
    expect(within(permissions).getByLabelText(permission.key)).toHaveProperty(
      "disabled",
      true,
    );
  expect(w.post).not.toHaveBeenCalled();
});

it("keeps an entered membership draft mounted and disabled through same-identity authority loss", async () => {
  history.replaceState(null, "", "/unit/members");
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/memberships": { items: [] },
    "/api/v1/access/roles": {
      items: [
        { ...role, builtin: true, key: "service_client", permissions: [] },
      ],
      authorizationRevision: 3,
    },
  });
  const view = render(<Members {...w.props} access={administrator} />);
  await screen.findByText("No memberships on this page.");
  const disclosure = screen
    .getByText("Add Catalog member", { selector: "summary" })
    .closest("details")!;
  expect(disclosure.open).toBe(false);
  fireEvent.click(
    within(disclosure).getByText("Add Catalog member", { selector: "summary" }),
  );
  change("Subject", "An existing identity");
  change("Reason", "Keep this private reason");
  view.rerender(
    <Members
      {...w.props}
      access={{ ...administrator, permissions: [], availablePermissions: [] }}
    />,
  );
  expect(screen.getByLabelText("Subject")).toHaveProperty(
    "value",
    "An existing identity",
  );
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Keep this private reason",
  );
  expect(screen.getByLabelText("Reason").closest("fieldset")).toHaveProperty(
    "disabled",
    true,
  );
  expect(
    window.dispatchEvent(new Event("beforeunload", { cancelable: true })),
  ).toBe(false);
  expect(w.post).not.toHaveBeenCalled();
});

it("binds a contextual moderation reason to its selected rating across filters and permission changes", async () => {
  history.replaceState(null, "", "/unit/ratings");
  const w = screenWorkspace();
  const target = {
    origin: "local",
    resourceId: "resource-1",
    versionRevisionId: "revision-1",
    identifier: "urn:air:example.com:agents:helper",
    kind: "agent",
    versionPresent: true,
    version: "1",
  };
  const first = {
    id: "rating-1",
    tenantId: "tenant-a",
    target,
    score: 4,
    consent: false,
    state: "active",
    revision: 2,
    createdAt: stamp,
    updatedAt: stamp,
  };
  const second = {
    ...first,
    id: "rating-2",
    revision: 6,
    target: { ...target, versionRevisionId: "revision-2" },
  };
  w.get.mockResolvedValue(etagReply({ items: [first, second] }));
  const view = render(<Ratings {...w.props} access={administrator} />);
  await screen.findAllByRole("button", { name: "Remove rating" });
  expect(screen.queryByLabelText("Moderation reason")).toBeNull();
  fireEvent.click(screen.getAllByRole("button", { name: "Remove rating" })[1]!);
  change("Moderation reason", "Reason for the second exact revision");
  change("Rating state", "removed");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  expect(screen.getByLabelText("Moderation reason")).toHaveProperty(
    "value",
    "Reason for the second exact revision",
  );
  view.rerender(
    <Ratings
      {...w.props}
      access={{ ...administrator, permissions: [], availablePermissions: [] }}
    />,
  );
  expect(
    screen.getByLabelText("Moderation reason").closest("fieldset"),
  ).toHaveProperty("disabled", true);
  view.rerender(<Ratings {...w.props} access={administrator} />);
  const panel = within(
    screen.getByRole("region", { name: "Selected rating moderation" }),
  );
  await waitFor(() =>
    expect(panel.getByRole("button", { name: "Remove rating" })).toHaveProperty(
      "disabled",
      false,
    ),
  );
  fireEvent.click(panel.getByRole("button", { name: "Remove rating" }));
  const dialog = await screen.findByRole("dialog");
  expect(dialog.textContent).toContain("rating-2");
  expect(dialog.textContent).toContain('"rev-6"');
  expect(dialog.textContent).toContain("revision-2");
  view.rerender(
    <Ratings
      {...w.props}
      access={{ ...administrator, permissions: [], availablePermissions: [] }}
    />,
  );
  const confirm = within(dialog).getByRole("button", {
    name: "Confirm",
  });
  expect(confirm).toHaveProperty("disabled", true);
  fireEvent.click(confirm);
  expect(w.post).not.toHaveBeenCalled();
  expect(screen.getByLabelText("Moderation reason")).toHaveProperty(
    "value",
    "Reason for the second exact revision",
  );
  view.rerender(
    <Ratings
      {...w.props}
      access={{ ...administrator, realmId: "another-realm" }}
    />,
  );
  expect(screen.queryByLabelText("Moderation reason")).toBeNull();
  expect(screen.queryByRole("dialog")).toBeNull();
});
