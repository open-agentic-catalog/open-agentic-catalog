// @vitest-environment jsdom
import { expect, it } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { deferred } from "../../tests/unit/fixtures";
import Namespaces from "./Namespaces";
import Publishers from "./Publishers";
import {
  administrator,
  change,
  click,
  etagReply,
  namespace,
  publisher,
  readReplies,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";

screenLifecycle("/unit/namespaces");

it.each([
  ["namespace", Namespaces, namespace, "namespace.manage", "Reason"],
  [
    "publisher",
    Publishers,
    publisher,
    "publisher.manage",
    "Metadata change reason",
  ],
] as const)(
  "blocks a prepared %s metadata send when its exact editing permission is removed",
  async (kind, View, item, permission, reason) => {
    const w = screenWorkspace();
    readReplies(w.get, { [`/api/v1/${kind}s/{${kind}Id}`]: item });
    const view = render(
      <View {...w.props} id={item.id} access={administrator} />,
    );
    await screen.findByLabelText(reason);
    change("Display name", "Captured metadata draft");
    change(reason, "Captured metadata reason");
    click("Review changes");
    const dialog = await screen.findByRole("dialog");
    const captured =
      within(dialog).getByLabelText("Command summary").textContent;
    view.rerender(
      <View
        {...w.props}
        id={item.id}
        access={{
          ...administrator,
          permissions: administrator.permissions.filter(
            (value) => value !== permission,
          ),
        }}
      />,
    );
    expect(screen.getByRole("dialog")).toBe(dialog);
    expect(within(dialog).getByLabelText("Command summary").textContent).toBe(
      captured,
    );
    expect(
      within(dialog).getByRole("button", { name: "Confirm" }),
    ).toHaveProperty("disabled", true);
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    expect(w.patch).not.toHaveBeenCalled();
    expect(w.post).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "value",
      "Captured metadata draft",
    );
    expect(screen.getByLabelText(reason)).toHaveProperty(
      "value",
      "Captured metadata reason",
    );
  },
);

it.each([
  ["namespace", Namespaces, "namespace.create"],
  ["publisher", Publishers, "publisher.create"],
] as const)(
  "retains the prepared %s creation and blocks a new send after access loss",
  async (kind, View, permission) => {
    const w = screenWorkspace();
    const view = render(<View {...w.props} id="new" access={administrator} />);
    change("Display name", "Captured creation draft");
    if (kind === "namespace") change("Slug", "captured-creation");
    else change("Declared domains", "example.com");
    click(`Create ${kind}`);
    const dialog = await screen.findByRole("dialog");
    const captured =
      within(dialog).getByLabelText("Command summary").textContent;
    view.rerender(
      <View
        {...w.props}
        id="new"
        access={{
          ...administrator,
          permissions: administrator.permissions.filter(
            (value) => value !== permission,
          ),
        }}
      />,
    );
    expect(screen.getByRole("dialog")).toBe(dialog);
    expect(within(dialog).getByLabelText("Command summary").textContent).toBe(
      captured,
    );
    expect(
      within(dialog).getByRole("button", { name: "Confirm" }),
    ).toHaveProperty("disabled", true);
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    expect(w.post).not.toHaveBeenCalled();
    expect(w.patch).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "value",
      "Captured creation draft",
    );
  },
);

it.each([false, true])(
  "blocks a prepared domain command independently of retained publisher metadata authority (revocation: %s)",
  async (revoked) => {
    const w = screenWorkspace();
    const item = {
      ...publisher,
      approvedPublicationDomains: revoked ? ["example.com"] : [],
    };
    readReplies(w.get, { "/api/v1/publishers/{publisherId}": item });
    const view = render(
      <Publishers {...w.props} id={item.id} access={administrator} />,
    );
    await screen.findByLabelText("Publication approval reason");
    change("Publication approval reason", "Captured domain reason");
    click(revoked ? "Revoke approval" : "Approve domain");
    const dialog = await screen.findByRole("dialog");
    const captured =
      within(dialog).getByLabelText("Command summary").textContent;
    view.rerender(
      <Publishers
        {...w.props}
        id={item.id}
        access={{
          ...administrator,
          permissions: administrator.permissions.filter(
            (value) => value !== "publisher.approve_domain",
          ),
        }}
      />,
    );
    expect(screen.getByRole("dialog")).toBe(dialog);
    expect(within(dialog).getByLabelText("Command summary").textContent).toBe(
      captured,
    );
    expect(
      within(dialog).getByRole("button", { name: "Confirm" }),
    ).toHaveProperty("disabled", true);
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    expect(w.post).not.toHaveBeenCalled();
    expect(w.patch).not.toHaveBeenCalled();
  },
);

it("retains exact-key recovery of an uncertain domain command after approval permission is removed", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/publishers/{publisherId}": publisher });
  w.post
    .mockResolvedValueOnce({
      error: { code: "DEPENDENCY_UNAVAILABLE" },
      response: new Response(null, { status: 503 }),
    })
    .mockResolvedValueOnce(
      etagReply({
        ...publisher,
        approvedPublicationDomains: ["example.com"],
        revision: 2,
      }),
    );
  const view = render(
    <Publishers {...w.props} id={publisher.id} access={administrator} />,
  );
  await screen.findByLabelText("Publication approval reason");
  change("Publication approval reason", "Captured uncertain domain reason");
  click("Approve domain");
  const dialog = await screen.findByRole("dialog");
  fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
  await within(dialog).findByRole("button", { name: "Retry same command" });
  view.rerender(
    <Publishers
      {...w.props}
      id={publisher.id}
      access={{
        ...administrator,
        permissions: administrator.permissions.filter(
          (value) => value !== "publisher.approve_domain",
        ),
      }}
    />,
  );
  const retry = within(dialog).getByRole("button", {
    name: "Retry same command",
  });
  expect(retry).toHaveProperty("disabled", false);
  fireEvent.click(retry);
  await screen.findByText(/Publisher domain command acknowledged/);
  expect(screen.queryByText(/Domain approved for publication/)).toBeNull();
  expect(w.post).toHaveBeenCalledTimes(2);
  expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
});

it.each([false, true])(
  "does not disclose a late domain outcome when approval access is removed and restored before its reply (revocation: %s)",
  async (revoked) => {
    const w = screenWorkspace();
    const item = {
      ...publisher,
      approvedPublicationDomains: revoked ? ["example.com"] : [],
    };
    readReplies(w.get, { "/api/v1/publishers/{publisherId}": item });
    const pending = deferred<ReturnType<typeof etagReply>>();
    w.post.mockReturnValue(pending.promise);
    const view = render(
      <Publishers {...w.props} id={item.id} access={administrator} />,
    );
    await screen.findByLabelText("Publication approval reason");
    change("Publication approval reason", "Captured pending domain reason");
    click(revoked ? "Revoke approval" : "Approve domain");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    expect(w.post).toHaveBeenCalledOnce();
    view.rerender(
      <Publishers
        {...w.props}
        id={item.id}
        access={{
          ...administrator,
          permissions: administrator.permissions.filter(
            (value) => value !== "publisher.approve_domain",
          ),
        }}
      />,
    );
    view.rerender(
      <Publishers {...w.props} id={item.id} access={administrator} />,
    );
    await act(async () =>
      pending.resolve(
        etagReply(
          revoked
            ? {
                publisher: { ...publisher, revision: 2 },
                affectedReleaseIds: ["00000000-0000-7000-8000-000000000001"],
                withdrawnHosts: ["private-pending.example.com"],
              }
            : {
                ...publisher,
                revision: 2,
                approvedPublicationDomains: ["example.com"],
              },
        ),
      ),
    );
    await screen.findByText(/Publisher domain command acknowledged/);
    expect(
      screen.queryByRole("region", { name: "Publication revocation receipt" }),
    ).toBeNull();
    expect(screen.queryByText("private-pending.example.com")).toBeNull();
    expect(
      screen.queryByText(/Approval revoked\.|Domain approved for publication/),
    ).toBeNull();
    expect(w.post).toHaveBeenCalledExactlyOnceWith(
      `/api/v1/publishers/{publisherId}/publication-domains/{domain}:${revoked ? "revoke" : "approve"}`,
      expect.objectContaining({
        body: { reason: "Captured pending domain reason" },
        params: {
          path: { publisherId: item.id, domain: "example.com" },
          header: {
            "If-Match": '"revision-1"',
            "Idempotency-Key": expect.any(String) as unknown,
          },
        },
      }),
    );
    expect(w.props.onFailure).not.toHaveBeenCalled();
  },
);
