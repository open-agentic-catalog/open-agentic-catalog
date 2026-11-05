import { testAccess } from "../../tests/unit/access-fixtures";
import * as authoringCommands from "../api/commands";
import { required } from "../../tests/required";
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
import Namespaces from "./Namespaces";
import Publishers from "./Publishers";
import { deferred, reader } from "../../tests/unit/fixtures";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  namespace,
  publisher,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";
screenLifecycle("/unit/namespaces");

it.each([
  ["namespace", Namespaces, "Slug", "tools"],
  ["publisher", Publishers, "Declared domains", "one.example\ntwo.example"],
] as const)(
  "captures exact %s creation values in confirmation without sending on cancellation",
  (name, View, field, value) => {
    const { props, post, get } = screenWorkspace();
    render(<View {...props} id="new" access={administrator} />);
    change("Display name", "Exact proposed name");
    change(field, value);
    if (name === "namespace")
      change("Description", "Exact namespace description");
    click(`Create ${name}`);
    const summary = within(screen.getByRole("dialog")).getByLabelText(
      "Command summary",
    );
    expect(summary.textContent).toContain(
      "New object; ID is returned after creation",
    );
    expect(summary.textContent).toContain("Exact proposed name");
    expect(summary.textContent).toContain(value);
    if (name === "namespace")
      expect(summary.textContent).toContain("Exact namespace description");
    expect(post).not.toHaveBeenCalled();
    click("Cancel");
    expect(post).not.toHaveBeenCalled();
    expect(get).not.toHaveBeenCalled();
  },
);

it.each([
  ["namespace", Namespaces, namespace, "catalog.read", "tenant.admin"],
  ["publisher", Publishers, publisher, "publishers.read", "publishers.manage"],
] as const)(
  "updates the %s creation link when command scope is removed but browsing remains",
  async (name, View, item, readScope, createScope) => {
    const { props, get, post } = screenWorkspace();
    get.mockResolvedValue(etagReply({ items: [item] }));
    const access = {
      ...administrator,
      scopeEnforcement: true,
      scopes: [readScope, createScope],
    };
    const view = render(<View {...props} access={access} />);
    await screen.findByRole("link", { name: item.displayName });
    expect(
      screen.getByRole("link", { name: `Create ${name}` }).getAttribute("href"),
    ).toBe(`/unit/${name}s/new`);
    view.rerender(
      <View
        {...props}
        access={{
          ...access,
          scopes: [readScope],
        }}
      />,
    );
    expect(screen.queryByRole("link", { name: `Create ${name}` })).toBeNull();
    expect(screen.getByRole("link", { name: item.displayName })).toBeTruthy();
    expect(get).toHaveBeenCalledOnce();
    expect(get).toHaveBeenCalledWith(`/api/v1/${name}s`, {
      params: { query: { limit: 25, cursor: undefined } },
      signal: expect.any(AbortSignal) as unknown,
    });
    expect(post).not.toHaveBeenCalled();
  },
);

it.each([
  ["namespace", Namespaces, "Slug", "tools"],
  ["publisher", Publishers, "Declared domains", "example.com"],
] as const)(
  "rejects a whitespace-only %s name through the enabled creation control",
  (name, View, field, value) => {
    const { props, get, post } = screenWorkspace();
    render(<View {...props} id="new" access={administrator} />);
    change("Display name", "   ");
    change(field, value);
    const create = screen.getByRole("button", { name: `Create ${name}` });
    expect(create).toHaveProperty("disabled", false);
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "validity.valid",
      true,
    );
    fireEvent.click(create);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(post).not.toHaveBeenCalled();
    change("Display name", "Tools");
    fireEvent.click(create);
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(post).not.toHaveBeenCalled();
    expect(get).not.toHaveBeenCalled();
    click("Cancel");
  },
);

it.each([false, true])(
  "keeps namespace creation independent of browsing scope (creation=%s)",
  (create) => {
    const { props, get } = screenWorkspace();
    render(
      <Namespaces
        {...props}
        access={{
          ...administrator,
          scopeEnforcement: true,
          scopes: create ? ["tenant.admin"] : [],
        }}
      />,
    );
    expect(
      screen.getByText(/Namespace browsing requires namespace.read/),
    ).toBeTruthy();
    const link = screen.queryByRole("link", { name: "Create a namespace" });
    if (create) expect(link?.getAttribute("href")).toBe("/unit/namespaces/new");
    else expect(link).toBeNull();
    expect(get).not.toHaveBeenCalled();
  },
);

it.each([
  ["namespace", Namespaces, namespace, "namespaces"],
  ["publisher", Publishers, publisher, "publishers"],
] as const)(
  "pages %s lists, reloads and preserves read-only access",
  async (_name, View, item, path) => {
    const { props, get } = screenWorkspace();
    const stored =
      path === "publishers"
        ? { ...item, approvedPublicationDomains: [] }
        : item;
    get
      .mockResolvedValueOnce(etagReply({ items: [stored], nextCursor: "next" }))
      .mockResolvedValueOnce(etagReply({ items: [] }))
      .mockResolvedValue(etagReply({ items: [item] }));
    render(<View {...props} />);
    expect(
      (
        await screen.findByRole("link", { name: item.displayName })
      ).getAttribute("href"),
    ).toBe(`/unit/${path}/${item.id}`);
    click("Next page");
    await screen.findByText(`No ${path} on this page.`);
    click("First page");
    await screen.findByRole("link", { name: item.displayName });
    click("Refresh");
    await waitFor(() => expect(get).toHaveBeenCalledTimes(4));
    expect(get.mock.calls[1]?.[1]).toMatchObject({
      params: { query: { cursor: "next" } },
    });
  },
);

it.each([
  ["namespace", Namespaces],
  ["publisher", Publishers],
] as const)("denies %s creation without its role", (name, View) => {
  const { props, get } = screenWorkspace();
  render(<View {...props} id="new" />);
  expect(screen.getByRole("alert").textContent).toContain(
    `does not permit ${name} creation`,
  );
  expect(get).not.toHaveBeenCalled();
});

it("creates namespace metadata and seals the form after success", async () => {
  const { props, post } = screenWorkspace();
  post.mockResolvedValue(etagReply(namespace));
  render(<Namespaces {...props} id="new" access={administrator} />);
  change("Display name", " Tools ");
  change("Slug", "tools");
  change("Description", "Team resources");
  await confirmCommand("Create namespace");
  await screen.findByText(/Created namespace tools/);
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/namespaces",
    {
      body: {
        displayName: "Tools",
        slug: "tools",
        description: "Team resources",
      },
    },
  ]);
  expect(
    screen.getByLabelText("Display name").closest("fieldset")?.disabled,
  ).toBe(true);
});

it("requires a changed namespace and reason, preserves cancelled edits and reloads current revision", async () => {
  const { props, get, patch } = screenWorkspace();
  get.mockResolvedValue(etagReply(namespace));
  patch.mockResolvedValue(etagReply({ ...namespace, revision: 2 }));
  const confirm = vi
    .spyOn(window, "confirm")
    .mockReturnValueOnce(false)
    .mockReturnValue(true);
  render(<Namespaces {...props} id={namespace.id} access={administrator} />);
  await screen.findByLabelText("Display name");
  expect(
    (
      screen.getByRole("button", {
        name: "Review changes",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  change("Display name", "New name");
  change("Reason", "  Rename  ");
  click("Review changes");
  click("Cancel");
  click("Reload namespace");
  expect(get).toHaveBeenCalledTimes(1);
  await confirmCommand("Review changes");
  await screen.findByText(/Namespace updated/);
  expect(patch.mock.calls[0]).toMatchObject([
    "/api/v1/namespaces/{namespaceId}",
    {
      params: {
        path: { namespaceId: namespace.id },
        header: { "If-Match": '"revision-1"' },
      },
      body: { displayName: "New name", reason: "Rename" },
    },
  ]);
  click("Reload namespace");
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  expect(confirm).toHaveBeenCalledTimes(1);
});

it("creates a publisher only after validating its declared domains", async () => {
  const { props, post } = screenWorkspace();
  post.mockResolvedValue(etagReply(publisher));
  render(<Publishers {...props} id="new" access={administrator} />);
  change("Display name", "Example");
  change("Declared domains", " , ");
  click("Create publisher");
  await screen.findByRole("alert");
  expect(post).not.toHaveBeenCalled();
  change("Declared domains", "example.com,example.com");
  await confirmCommand("Create publisher");
  await screen.findByText(/Created publisher/);
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/publishers",
    { body: { displayName: "Example", domains: ["example.com"] } },
  ]);
});

it.each([
  { revoke: false, hosts: [] },
  { revoke: true, hosts: ["catalog.example.com"] },
  { revoke: true, hosts: [] },
])(
  "keeps publisher metadata independent from domain approval (revoke=$revoke, hosts=$hosts)",
  async ({ revoke, hosts }) => {
    const { props, get, post } = screenWorkspace();
    get.mockResolvedValue(
      etagReply({
        ...publisher,
        approvedPublicationDomains: revoke ? ["example.com"] : [],
      }),
    );
    post.mockResolvedValue(
      etagReply({
        ...publisher,
        publisher,
        affectedReleaseIds: ["release-1"],
        withdrawnHosts: hosts,
      }),
    );
    render(<Publishers {...props} id={publisher.id} access={administrator} />);
    await screen.findByLabelText("Declared domains");
    const reasons = screen.getAllByLabelText(
      /^(Metadata change reason|Publication approval reason)$/,
    );
    fireEvent.change(required(reasons[1]), {
      target: { value: "  Domain review  " },
    });
    const action = revoke ? "Revoke approval" : "Approve domain";
    change("Display name", "Unsaved");
    expect(
      (screen.getByRole("button", { name: action }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    change("Display name", publisher.displayName);
    await confirmCommand(action);
    if (revoke)
      await screen.findByText(
        new RegExp(`Withdrawn hosts: ${hosts.join(", ") || "none"}`),
      );
    else await screen.findByText(/Domain approved for publication/);
    if (revoke) {
      expect(
        screen.getByRole("region", { name: "Publication revocation receipt" })
          .textContent,
      ).toContain("release-1");
      click("Clear receipt");
      expect(
        screen.queryByRole("region", {
          name: "Publication revocation receipt",
        }),
      ).toBeNull();
    }
    expect(post.mock.calls[0]).toMatchObject([
      `/api/v1/publishers/{publisherId}/publication-domains/{domain}:${revoke ? "revoke" : "approve"}`,
      {
        params: {
          path: { publisherId: publisher.id, domain: "example.com" },
          header: { "If-Match": '"revision-1"' },
        },
        body: { reason: "Domain review" },
      },
    ]);
  },
);

it.each([
  ["identity", "success"],
  ["identity", "failure"],
  ["publisher", "success"],
  ["publisher", "failure"],
  ["permission", "success"],
  ["permission", "failure"],
  ["scope", "success"],
  ["scope", "failure"],
] as const)(
  "discards a pending domain revocation after %s changes and late %s",
  async (boundary, outcome) => {
    const { props, get, post } = screenWorkspace();
    const selected = {
      ...publisher,
      approvedPublicationDomains: ["example.com"],
    };
    const replacement =
      boundary === "publisher"
        ? { ...publisher, id: "publisher-2", displayName: "Another publisher" }
        : selected;
    get
      .mockResolvedValueOnce(etagReply(selected))
      .mockResolvedValue(etagReply(replacement));
    const pending = deferred<ReturnType<typeof etagReply>>();
    post.mockReturnValue(pending.promise);
    const access = {
      ...administrator,
      permissions: [
        "publisher.read",
        "publisher.approve_domain",
      ] as typeof administrator.permissions,
      availablePermissions: [
        "publisher.read",
        "publisher.approve_domain",
      ] as typeof administrator.permissions,
      scopeEnforcement: true,
      scopes: ["publishers.read", "publishers.manage"] as (
        "publishers.read" | "publishers.manage"
      )[],
    };
    const view = render(
      <Publishers {...props} id={selected.id} access={access} />,
    );
    await screen.findByRole("heading", { name: selected.displayName });
    expect(screen.queryByLabelText("Metadata change reason")).toBeNull();
    expect(
      screen.getByText("Declared domains").nextElementSibling?.textContent,
    ).toBe(selected.domains.join(", "));
    change(
      "Publication approval reason",
      "Withdraw the selected publisher domain",
    );
    await confirmCommand("Revoke approval");
    expect(post).toHaveBeenCalledOnce();
    expect(
      screen.getByRole("button", { name: "Submitting..." }),
    ).toHaveProperty("disabled", true);
    view.rerender(
      <Publishers
        {...props}
        id={replacement.id}
        access={
          boundary === "identity"
            ? { ...access, subject: "another-identity" }
            : boundary === "permission"
              ? {
                  ...access,
                  permissions: access.permissions.filter(
                    (permission) => permission !== "publisher.approve_domain",
                  ),
                  availablePermissions: access.availablePermissions.filter(
                    (permission) => permission !== "publisher.approve_domain",
                  ),
                }
              : boundary === "scope"
                ? { ...access, scopes: ["publishers.read"] }
                : access
        }
      />,
    );
    await screen.findByRole("heading", { name: replacement.displayName });
    await act(async () => {
      if (outcome === "success")
        pending.resolve(
          etagReply({
            publisher,
            affectedReleaseIds: ["00000000-0000-7000-8000-000000000001"],
            withdrawnHosts: ["private-old-publisher.example.com"],
          }),
        );
      else pending.reject(new Error("Controlled old-identity command failure"));
    });
    const policyChanged = boundary === "permission" || boundary === "scope";
    if (policyChanged && outcome === "failure") {
      const dialog = screen.getByRole("dialog");
      expect(
        within(dialog).getByRole("button", { name: "Back to form" }),
      ).toHaveProperty("disabled", true);
      expect(
        within(dialog).getByRole("button", { name: "Retry same command" }),
      ).toHaveProperty("disabled", false);
      expect(
        screen.queryByText("private-old-publisher.example.com"),
      ).toBeNull();
      const captured = structuredClone(required(post.mock.calls[0]));
      post.mockResolvedValueOnce(
        etagReply({
          publisher,
          affectedReleaseIds: ["00000000-0000-7000-8000-000000000001"],
          withdrawnHosts: ["private-old-publisher.example.com"],
        }),
      );
      click("Retry same command");
      await screen.findByText(/Publisher domain command acknowledged/);
      expect(post).toHaveBeenCalledTimes(2);
      expect(post.mock.calls[1]).toEqual(captured);
    } else {
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(post).toHaveBeenCalledOnce();
    }
    expect(
      screen.queryByRole("region", { name: "Publication revocation receipt" }),
    ).toBeNull();
    expect(screen.queryByText(/Approval revoked\./)).toBeNull();
    expect(screen.queryByText("private-old-publisher.example.com")).toBeNull();
    expect(screen.queryByLabelText("Display name")).toBeNull();
    expect(
      screen.getByText("Display name").nextElementSibling?.textContent,
    ).toBe(replacement.displayName);
    expect(get).toHaveBeenCalledTimes(policyChanged ? 1 : 2);
    expect(props.onFailure).not.toHaveBeenCalled();
    if (policyChanged) {
      expect(
        screen.getByLabelText("Publication approval reason"),
      ).toHaveProperty("value", "Withdraw the selected publisher domain");
      expect(
        screen.queryByRole("button", { name: "Revoke approval" }),
      ).toBeNull();
      view.rerender(
        <Publishers {...props} id={replacement.id} access={access} />,
      );
      expect(
        screen.queryByRole("region", {
          name: "Publication revocation receipt",
        }),
      ).toBeNull();
      expect(
        screen.queryByText("private-old-publisher.example.com"),
      ).toBeNull();
    }
  },
);

it.each(["publisher", "scope", "permission"] as const)(
  "clears a disclosed publisher receipt after its %s boundary changes",
  async (boundary) => {
    const { props, get, post } = screenWorkspace();
    const selected = {
      ...publisher,
      approvedPublicationDomains: ["example.com"],
    };
    const replacement =
      boundary === "publisher"
        ? { ...publisher, id: "publisher-2", displayName: "Another publisher" }
        : { ...publisher, revision: 2 };
    get
      .mockResolvedValueOnce(etagReply(selected))
      .mockResolvedValue(etagReply(replacement, '"revision-2"'));
    post.mockResolvedValue(
      etagReply({
        publisher,
        affectedReleaseIds: ["00000000-0000-7000-8000-000000000001"],
        withdrawnHosts: ["private-old-publisher.example.com"],
      }),
    );
    const access = {
      ...administrator,
      permissions: [
        "publisher.read",
        "publisher.approve_domain",
      ] as typeof administrator.permissions,
      availablePermissions: [
        "publisher.read",
        "publisher.approve_domain",
      ] as typeof administrator.permissions,
      scopeEnforcement: true,
      scopes: ["publishers.read", "publishers.manage"] as (
        "publishers.read" | "publishers.manage"
      )[],
    };
    const view = render(
      <Publishers {...props} id={selected.id} access={access} />,
    );
    await screen.findByRole("heading", { name: selected.displayName });
    expect(screen.queryByLabelText("Metadata change reason")).toBeNull();
    change(
      "Publication approval reason",
      "Withdraw the selected publisher domain",
    );
    await confirmCommand("Revoke approval");
    expect(
      (
        await screen.findByRole("region", {
          name: "Publication revocation receipt",
        })
      ).textContent,
    ).toContain("private-old-publisher.example.com");
    view.rerender(
      <Publishers
        {...props}
        id={replacement.id}
        access={
          boundary === "scope"
            ? { ...access, scopes: ["publishers.read"] }
            : boundary === "permission"
              ? {
                  ...access,
                  permissions: access.permissions.filter(
                    (permission) => permission !== "publisher.approve_domain",
                  ),
                  availablePermissions: access.availablePermissions.filter(
                    (permission) => permission !== "publisher.approve_domain",
                  ),
                }
              : access
        }
      />,
    );
    expect(
      screen.queryByRole("region", { name: "Publication revocation receipt" }),
    ).toBeNull();
    await screen.findByRole("heading", { name: replacement.displayName });
    expect(
      screen.queryByRole("region", { name: "Publication revocation receipt" }),
    ).toBeNull();
    expect(screen.queryByText("private-old-publisher.example.com")).toBeNull();
    if (boundary === "scope" || boundary === "permission") {
      expect(
        screen.queryByRole("button", { name: "Revoke approval" }),
      ).toBeNull();
      expect(
        screen.queryByRole("button", { name: "Approve domain" }),
      ).toBeNull();
    }
    expect(get).toHaveBeenCalledTimes(boundary === "publisher" ? 2 : 1);
    if (boundary !== "publisher") {
      view.rerender(
        <Publishers {...props} id={replacement.id} access={access} />,
      );
      expect(
        screen.queryByRole("region", {
          name: "Publication revocation receipt",
        }),
      ).toBeNull();
      expect(
        screen.queryByText("private-old-publisher.example.com"),
      ).toBeNull();
    }
    expect(post).toHaveBeenCalledOnce();
    expect(props.onFailure).not.toHaveBeenCalled();
  },
);

it("retains the exact revocation command after a malformed successful receipt", async () => {
  const { props, get, post } = screenWorkspace();
  const receipt = {
    publisher,
    affectedReleaseIds: ["00000000-0000-7000-8000-000000000001"],
    withdrawnHosts: ["catalog.example.com"],
  };
  get.mockResolvedValue(
    etagReply({ ...publisher, approvedPublicationDomains: ["example.com"] }),
  );
  post
    .mockResolvedValueOnce(etagReply({ ...receipt, withdrawnHosts: null }))
    .mockResolvedValue(etagReply(receipt));
  render(<Publishers {...props} id={publisher.id} access={administrator} />);
  await screen.findByLabelText("Declared domains");
  fireEvent.change(
    required(
      screen.getAllByLabelText(
        /^(Metadata change reason|Publication approval reason)$/,
      )[1],
    ),
    {
      target: { value: "Withdraw the selected publisher domain" },
    },
  );
  await confirmCommand("Revoke approval");
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByText(/The outcome is unknown/)).toBeTruthy();
  expect(
    within(dialog).getByRole("button", { name: "Back to form" }),
  ).toHaveProperty("disabled", true);
  expect(
    screen.queryByRole("region", { name: "Publication revocation receipt" }),
  ).toBeNull();
  expect(post).toHaveBeenCalledOnce();
  expect(get).toHaveBeenCalledOnce();
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/publishers/{publisherId}/publication-domains/{domain}:revoke",
    {
      params: {
        path: { publisherId: publisher.id, domain: "example.com" },
        header: {
          "If-Match": '"revision-1"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
      body: { reason: "Withdraw the selected publisher domain" },
    },
  ]);
  await act(async () => {
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Retry same command" }),
    );
  });
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(
    screen.getByRole("region", { name: "Publication revocation receipt" })
      .textContent,
  ).toContain("catalog.example.com");
  expect(post).toHaveBeenCalledTimes(2);
  expect(post.mock.calls[1]).toEqual(post.mock.calls[0]);
  expect(get).toHaveBeenCalledOnce();
  expect(props.onFailure).toHaveBeenCalledOnce();
});

it("saves publisher metadata with its ETag and keeps rejected changes editable", async () => {
  const { props, get, patch } = screenWorkspace();
  get.mockResolvedValue(etagReply(publisher));
  patch
    .mockResolvedValueOnce({
      error: {},
      response: new Response(null, { status: 412 }),
    })
    .mockResolvedValue(etagReply(publisher));
  render(<Publishers {...props} id={publisher.id} access={administrator} />);
  await screen.findByLabelText("Display name");
  change("Display name", "Renamed");
  change("Declared domains", "example.com\nother.example.com");
  fireEvent.change(
    required(
      screen.getAllByLabelText(
        /^(Metadata change reason|Publication approval reason)$/,
      )[0],
    ),
    {
      target: { value: "Metadata change" },
    },
  );
  await confirmCommand("Review changes");
  await screen.findByRole("alert");
  expect(props.onFailure).toHaveBeenCalledTimes(1);
  click("Back to form");
  expect(
    (screen.getByLabelText("Display name") as HTMLInputElement).value,
  ).toBe("Renamed");
  await confirmCommand("Review changes");
  await screen.findByText(/Publisher updated/);
  expect(patch.mock.calls[1]).toMatchObject([
    "/api/v1/publishers/{publisherId}",
    {
      params: { header: { "If-Match": '"revision-1"' } },
      body: {
        displayName: "Renamed",
        reason: "Metadata change",
        domains: ["example.com", "other.example.com"],
      },
    },
  ]);
});

it.each([false, true])(
  "keeps publisher creation separate from read access (create=%s)",
  (create) => {
    const { props, get, post } = screenWorkspace();
    render(
      <Publishers
        {...props}
        access={{
          ...reader,
          ...testAccess(create ? ["contributor"] : []),
          scopeEnforcement: true,
          scopes: create ? ["publishers.manage"] : [],
        }}
      />,
    );
    expect(
      screen.getByText(/Publisher browsing requires publisher.read/),
    ).toBeTruthy();
    const link = screen.queryByRole("link", { name: "Create a publisher" });
    if (create) expect(link?.getAttribute("href")).toBe("/unit/publishers/new");
    else expect(link).toBeNull();
    expect(get).not.toHaveBeenCalled();
    expect(post).not.toHaveBeenCalled();
  },
);

it("preserves declined publisher reloads, then replaces discarded edits with the latest revision", async () => {
  const { props, get, patch } = screenWorkspace();
  get
    .mockResolvedValueOnce(etagReply(publisher))
    .mockResolvedValueOnce(
      etagReply(
        { ...publisher, displayName: "Changed elsewhere", revision: 2 },
        '"revision-2"',
      ),
    )
    .mockResolvedValueOnce(
      etagReply(
        { ...publisher, displayName: "Final name", revision: 3 },
        '"revision-3"',
      ),
    );
  patch.mockResolvedValue(etagReply({ ...publisher, revision: 3 }));
  render(<Publishers {...props} id={publisher.id} access={administrator} />);
  await screen.findByLabelText("Display name");
  change("Display name", "Unsaved name");
  fireEvent.change(
    required(
      screen.getAllByLabelText(
        /^(Metadata change reason|Publication approval reason)$/,
      )[0],
    ),
    {
      target: { value: "Unsaved reason" },
    },
  );
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Reload publisher");
  expect(discard).toHaveBeenCalledOnce();
  expect(get).toHaveBeenCalledTimes(1);
  expect(
    (screen.getByLabelText("Display name") as HTMLInputElement).value,
  ).toBe("Unsaved name");
  discard.mockReturnValue(true);
  click("Reload publisher");
  await screen.findByRole("heading", { name: "Changed elsewhere" });
  expect(
    (screen.getByLabelText("Display name") as HTMLInputElement).value,
  ).toBe("Changed elsewhere");
  expect(
    (
      screen.getAllByLabelText(
        /^(Metadata change reason|Publication approval reason)$/,
      )[0] as HTMLTextAreaElement
    ).value,
  ).toBe("");
  change("Display name", "Final name");
  fireEvent.change(
    required(
      screen.getAllByLabelText(
        /^(Metadata change reason|Publication approval reason)$/,
      )[0],
    ),
    {
      target: { value: "Reviewed current revision" },
    },
  );
  await confirmCommand("Review changes");
  await screen.findByText(/Publisher updated/);
  expect(patch).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/publishers/{publisherId}",
    {
      params: {
        path: { publisherId: publisher.id },
        header: {
          "If-Match": '"revision-2"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
      body: {
        displayName: "Final name",
        domains: publisher.domains,
        reason: "Reviewed current revision",
      },
    },
  );
  click("Reload publisher");
  await screen.findByRole("heading", { name: "Final name" });
  expect(discard).toHaveBeenCalledTimes(2);
  expect(get).toHaveBeenCalledTimes(3);
  expect(props.onFailure).not.toHaveBeenCalled();
});

it("blocks publisher changes without an ETag and restores safe controls after a clean reload", async () => {
  const { props, get, patch, post } = screenWorkspace();
  get
    .mockResolvedValueOnce(etagReply(publisher, null))
    .mockResolvedValueOnce(etagReply(publisher));
  render(<Publishers {...props} id={publisher.id} access={administrator} />);
  await screen.findByLabelText("Display name");
  change("Display name", "Needs a revision");
  const reasons = screen.getAllByLabelText(
    /^(Metadata change reason|Publication approval reason)$/,
  );
  fireEvent.change(required(reasons[0]), {
    target: { value: "Update metadata" },
  });
  fireEvent.change(required(reasons[1]), {
    target: { value: "Approve domain" },
  });
  for (const name of ["Review changes", "Approve domain"]) {
    const button = screen.getByRole("button", { name }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    fireEvent.click(button);
  }
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(patch).not.toHaveBeenCalled();
  expect(post).not.toHaveBeenCalled();
  change("Display name", publisher.displayName);
  for (const reason of reasons)
    fireEvent.change(reason, { target: { value: "" } });
  const discard = vi.spyOn(window, "confirm");
  click("Reload publisher");
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  await screen.findByLabelText("Display name");
  fireEvent.change(
    required(
      screen.getAllByLabelText(
        /^(Metadata change reason|Publication approval reason)$/,
      )[1],
    ),
    {
      target: { value: "Reviewed domain" },
    },
  );
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "Approve domain",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false),
  );
  expect(discard).not.toHaveBeenCalled();
});

it.each([
  [Namespaces, namespace],
  [Publishers, publisher],
] as const)(
  "renders retained metadata read-only for a Reader",
  async (View, item) => {
    const { props, get } = screenWorkspace();
    get.mockResolvedValue(etagReply(item));
    render(<View {...props} id={item.id} />);
    await screen.findByRole("heading", { name: item.displayName });
    expect(screen.queryByLabelText("Display name")).toBeNull();
    expect(
      screen.getByText("Display name").nextElementSibling?.textContent,
    ).toBe(item.displayName);
    expect(screen.getByText("Revision").nextElementSibling?.textContent).toBe(
      String(item.revision),
    );
    expect(screen.queryByRole("button", { name: "Review changes" })).toBeNull();
  },
);

it("resolves only a later-page namespace, keeps writes disabled on failure and fences revoked grants", async () => {
  const { props, get, patch } = screenWorkspace();
  const id = "00000000-0000-0000-0000-000000000021";
  const initial = {
    ...administrator,
    tenantId: "00000000-0000-0000-0000-000000000001",
    membershipId: "00000000-0000-0000-0000-000000000002",
    realmId: "00000000-0000-0000-0000-000000000003",
    permissions: [],
    availablePermissions: ["namespace.read", "namespace.manage"] as const,
    namespaceGrants: [],
    nextCursor: "more-grants",
  };
  const access = {
    ...initial,
    availablePermissions: [...initial.availablePermissions],
  };
  let fail = true;
  get.mockImplementation(async (path, options) => {
    if (path === "/api/v1/namespaces/{namespaceId}")
      return etagReply({ ...namespace, id });
    expect(path).toBe("/api/v1/me");
    expect(options).toMatchObject({ params: { query: { namespaceId: id } } });
    expect(options).not.toHaveProperty("params.query.cursor");
    if (fail) throw new Error("Private dependency error");
    return etagReply({
      ...access,
      namespaceGrants: [
        {
          namespaceId: id,
          permissions: ["namespace.read", "namespace.manage"],
        },
      ],
    });
  });
  const view = render(<Namespaces {...props} id={id} access={access} />);
  await screen.findByRole("alert");
  expect(screen.queryByRole("button", { name: "Review changes" })).toBeNull();
  expect(screen.queryByLabelText("Display name")).toBeNull();
  expect(screen.getByText("Display name").nextElementSibling?.textContent).toBe(
    namespace.displayName,
  );
  expect(patch).not.toHaveBeenCalled();
  fail = false;
  click("Reload");
  await screen.findByRole("button", { name: "Review changes" });
  change("Display name", "Updated tools");
  change("Reason", "Reviewed metadata");
  expect(screen.getByRole("button", { name: "Review changes" })).toHaveProperty(
    "disabled",
    false,
  );
  view.rerender(
    <Namespaces
      {...props}
      id={id}
      access={{ ...access, nextCursor: "", authorizationRevision: 2 }}
    />,
  );
  expect(screen.queryByRole("button", { name: "Review changes" })).toBeNull();
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Updated tools",
  );
  expect(
    screen.getByLabelText("Display name").closest("fieldset"),
  ).toHaveProperty("disabled", true);
  expect(patch).not.toHaveBeenCalled();
});

it.each([false, true])(
  "checks namespace management separately from its OAuth ceiling (scope=%s)",
  async (scope) => {
    const { props, get, patch } = screenWorkspace();
    get.mockResolvedValue(etagReply(namespace));
    render(
      <Namespaces
        {...props}
        id={namespace.id}
        access={{
          ...administrator,
          permissions: [],
          availablePermissions: ["namespace.read", "namespace.manage"],
          namespaceGrants: [
            {
              namespaceId: namespace.id,
              permissions: scope
                ? ["namespace.read"]
                : ["namespace.read", "namespace.manage"],
            },
          ],
          scopeEnforcement: true,
          scopes: scope ? ["catalog.read", "tenant.admin"] : ["catalog.read"],
        }}
      />,
    );
    await screen.findByRole("heading", { name: namespace.displayName });
    expect(screen.queryByLabelText("Display name")).toBeNull();
    expect(
      screen.getByText("Display name").nextElementSibling?.textContent,
    ).toBe(namespace.displayName);
    expect(screen.queryByRole("button", { name: "Review changes" })).toBeNull();
    expect(patch).not.toHaveBeenCalled();
    expect(get).toHaveBeenCalledOnce();
  },
);

it.each([
  ["namespace", Namespaces, "Display name", 160],
  ["namespace", Namespaces, "Description", 2000],
  ["publisher", Publishers, "Display name", 160],
] as const)(
  "allows the full Unicode %s %s budget and refuses one extra code point",
  async (kind, View, field, limit) => {
    const { props, get, post } = screenWorkspace();
    render(<View {...props} access={administrator} id="new" />);
    change("Display name", "Unicode example");
    change(
      kind === "namespace" ? "Slug" : "Declared domains",
      kind === "namespace" ? "unicode" : "example.com",
    );
    const control = screen.getByLabelText(field);
    expect(control.getAttribute("maxlength")).toBe(String(limit * 2));
    change(field, "🙂".repeat(limit) + "a");
    click(`Create ${kind}`);
    expect(screen.getByRole("alert").textContent).toContain(
      `${limit} characters`,
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    change(field, "🙂".repeat(limit));
    click(`Create ${kind}`);
    expect(screen.getByRole("dialog").textContent).toContain("Create");
    expect(post).not.toHaveBeenCalled();
    expect(get).not.toHaveBeenCalled();
    click("Cancel");
  },
);

it("keeps exact revocation references for a minimal delegate without detail lookups and clears them on identity changes", async () => {
  const { props, get, post } = screenWorkspace();
  const released = "00000000-0000-7000-8000-000000000099";
  const access = {
    ...administrator,
    ...testAccess([]),
    permissions: [
      "publisher.read",
      "publisher.approve_domain",
    ] as typeof administrator.permissions,
    availablePermissions: [
      "publisher.read",
      "publisher.approve_domain",
    ] as typeof administrator.permissions,
    scopeEnforcement: true,
    scopes: [
      "publishers.read",
      "publishers.manage",
    ] as typeof administrator.scopes,
  };
  get.mockResolvedValue(
    etagReply({ ...publisher, approvedPublicationDomains: ["example.com"] }),
  );
  post.mockResolvedValue(
    etagReply({
      ...publisher,
      publisher,
      affectedReleaseIds: [released],
      withdrawnHosts: ["catalog.example.com"],
    }),
  );
  const view = render(
    <Publishers {...props} id={publisher.id} access={access} />,
  );
  await screen.findByRole("button", { name: "Revoke approval" });
  fireEvent.change(
    screen
      .getAllByLabelText(
        /^(Metadata change reason|Publication approval reason)$/,
      )
      .at(-1)!,
    {
      target: { value: "Withdraw approval" },
    },
  );
  await confirmCommand("Revoke approval");
  const receipt = await screen.findByRole("region", {
    name: "Publication revocation receipt",
  });
  expect(receipt.textContent).toContain(released);
  expect(receipt.textContent).toContain("catalog.example.com");
  expect(receipt.querySelectorAll("a")).toHaveLength(0);
  expect(screen.getByRole("button", { name: "Copy release ID" })).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "Copy withdrawn host" }),
  ).toBeTruthy();
  expect(get).toHaveBeenCalledOnce();
  view.rerender(
    <Publishers
      {...props}
      id={publisher.id}
      access={{ ...access, subject: "another-delegate" }}
    />,
  );
  expect(
    screen.queryByRole("region", { name: "Publication revocation receipt" }),
  ).toBeNull();
});

it("keeps known tenant-wide Namespace editing usable during a supplemental access read while blocking new sends until recovery", async () => {
  const w = screenWorkspace();
  const access = {
    ...administrator,
    tenantId: "00000000-0000-0000-0000-000000000001",
    membershipId: "00000000-0000-0000-0000-000000000002",
    realmId: "00000000-0000-0000-0000-000000000003",
    namespaceGrants: [],
    nextCursor: "later-grants",
  };
  const item = { ...namespace, tenantId: access.tenantId };
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.get.mockImplementation(async (path) => {
    if (path === "/api/v1/namespaces/{namespaceId}") return etagReply(item);
    expect(path).toBe("/api/v1/me");
    return pending.promise;
  });
  w.patch.mockResolvedValue(
    etagReply({
      ...item,
      displayName: "Staged during access recovery",
      revision: 2,
    }),
  );
  render(<Namespaces {...w.props} id={namespace.id} access={access} />);
  const name = await screen.findByLabelText("Display name");
  expect(name.matches(":disabled")).toBe(false);
  change("Display name", "Staged during access recovery");
  change("Reason", "Preserve current authority and recover readiness");
  const review = screen.getByRole("button", { name: "Review changes" });
  expect(review).toHaveProperty("disabled", true);
  fireEvent.submit(name.closest("form")!);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(w.patch).not.toHaveBeenCalled();
  await act(async () =>
    pending.reject(new Error("Controlled supplemental access read failure")),
  );
  await screen.findByRole("alert");
  expect(screen.getByLabelText("Display name").matches(":disabled")).toBe(
    false,
  );
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Staged during access recovery",
  );
  expect(review).toHaveProperty("disabled", true);
  fireEvent.submit(name.closest("form")!);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(w.patch).not.toHaveBeenCalled();
  w.get.mockResolvedValueOnce(etagReply(access));
  click("Reload");
  await waitFor(() => expect(review).toHaveProperty("disabled", false));
  await confirmCommand("Review changes");
  await screen.findByText(/Namespace updated/);
  expect(w.patch).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/namespaces/{namespaceId}",
    expect.objectContaining({
      params: {
        path: { namespaceId: namespace.id },
        header: {
          "If-Match": '"revision-1"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
      body: {
        displayName: "Staged during access recovery",
        description: "",
        reason: "Preserve current authority and recover readiness",
      },
    }),
  );
  expect(w.get).toHaveBeenCalledTimes(3);
  expect(w.props.onFailure).toHaveBeenCalledOnce();
});

it.each(["list", "detail"] as const)(
  "refuses a Publisher %s response with omitted approval facts instead of presenting no approval",
  async (mode) => {
    const w = screenWorkspace();
    const { approvedPublicationDomains: _approvals, ...incomplete } = publisher;
    w.get.mockResolvedValue(
      etagReply(mode === "list" ? { items: [incomplete] } : incomplete),
    );
    render(
      <Publishers
        {...w.props}
        id={mode === "detail" ? publisher.id : ""}
        access={administrator}
      />,
    );
    await screen.findByRole("alert");
    expect(
      screen.queryByRole("heading", { name: "Publication domains" }),
    ).toBeNull();
    expect(screen.queryByRole("button", { name: "Approve domain" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Review changes" })).toBeNull();
    expect(screen.queryByText("None", { exact: true })).toBeNull();
    expect(w.get).toHaveBeenCalledOnce();
    expect(w.post).not.toHaveBeenCalled();
    expect(w.patch).not.toHaveBeenCalled();
    expect(w.props.onFailure).toHaveBeenCalledOnce();
  },
);

it("keeps a Publisher creation draft and safe explanation after an unrecognized local preparation failure", async () => {
  const w = screenWorkspace();
  vi.spyOn(authoringCommands, "domainsFromInput").mockImplementationOnce(() => {
    throw "private local parser diagnostic";
  });
  render(<Publishers {...w.props} id="new" access={administrator} />);
  change("Display name", "Retained publisher draft");
  change("Declared domains", "example.com");
  click("Create publisher");
  expect((await screen.findByRole("alert")).textContent).toBe(
    "Check the publisher fields.",
  );
  expect(screen.queryByText("private local parser diagnostic")).toBeNull();
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Retained publisher draft",
  );
  expect(screen.getByLabelText("Declared domains")).toHaveProperty(
    "value",
    "example.com",
  );
  expect(w.post).not.toHaveBeenCalled();
  click("Create publisher");
  const dialog = await screen.findByRole("dialog");
  expect(
    within(dialog).getByLabelText("Command summary").textContent,
  ).toContain("Retained publisher draft");
  expect(
    within(dialog).getByLabelText("Command summary").textContent,
  ).toContain("example.com");
  click("Cancel");
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
});

it.each([
  ["namespace", Namespaces, namespace, "Reason"],
  ["publisher", Publishers, publisher, "Metadata change reason"],
] as const)(
  "prevents a first %s metadata send without an ETag even when submission is dispatched directly",
  async (kind, View, item, reason) => {
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply(item, null));
    render(<View {...w.props} id={item.id} access={administrator} />);
    await screen.findByLabelText("Display name");
    change("Display name", "Preserved without a revision marker");
    change(reason, "Obtain the current marker before any write");
    expect(
      screen.getByRole("button", { name: "Review changes" }),
    ).toHaveProperty("disabled", true);
    fireEvent.submit(screen.getByLabelText(reason).closest("form")!);
    if (kind === "publisher") {
      const dialog = await screen.findByRole("dialog");
      expect(
        within(dialog).getByLabelText("Command summary").textContent,
      ).toContain("Unavailable; reload before saving");
      expect(
        within(dialog).getByRole("button", { name: "Confirm" }),
      ).toHaveProperty("disabled", true);
      fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
      click("Cancel");
    } else expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "value",
      "Preserved without a revision marker",
    );
    expect(screen.getByLabelText(reason)).toHaveProperty(
      "value",
      "Obtain the current marker before any write",
    );
    expect(w.post).not.toHaveBeenCalled();
    expect(w.patch).not.toHaveBeenCalled();
  },
);
