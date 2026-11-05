import { testPermissions } from "../../tests/unit/access-fixtures";
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
import { APIError } from "../api/client";
import Publication from "./Publication";
import { deferred } from "../../tests/unit/fixtures";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";
screenLifecycle("/unit/publication");
const snapshot = {
  tenantId: "tenant-a",
  etag: '"snapshot"',
  createdAt: "2026-10-20T00:00:00Z",
  id: "snapshot-1",
  host: "catalog.example.com",
  generation: 1,
  state: "candidate",
  audience: "anonymous",
  exclusions: [],
  ardProfile: "ard-0.91-aa3e598",
  aiCatalogProfile: "ai-catalog-1.0-b062278",
  ard: {
    entries: [
      {
        identifier: "urn:air:catalog.example.com:registry:open-agentic-catalog",
        type: "application/ai-registry+json",
        displayName: "Catalog Registry",
        metadata: { catalogReleaseId: "registry:self" },
      },
    ],
  },
  aiCatalog: {
    specVersion: "1.0",
    host: { displayName: "Catalog" },
    entries: [],
  },
};

it.each(["id", "host"])(
  "rejects a snapshot bound to a different %s",
  async (field) => {
    history.replaceState(
      null,
      "",
      "/unit/publication/snapshot-1?host=catalog.example.com",
    );
    const { props, get } = screenWorkspace();
    get.mockImplementation(async (path) =>
      path === "/api/v1/publication/hosts"
        ? etagReply({ items: [] })
        : etagReply({ ...snapshot, [field]: "different" }),
    );
    render(<Publication {...props} id={snapshot.id} access={administrator} />);
    await screen.findByRole("alert");
    expect(screen.queryByRole("heading", { name: /Generation/ })).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Activate snapshot" }),
    ).toBeNull();
  },
);

it("trims the explicit host like the HTTP contract before checking returned binding", async () => {
  history.replaceState(
    null,
    "",
    "/unit/publication/snapshot-1?host=%20catalog.example.com%20",
  );
  const { props, get } = screenWorkspace();
  get.mockImplementation(async (path) =>
    path === "/api/v1/publication/hosts"
      ? etagReply({ items: [] })
      : etagReply(snapshot),
  );
  render(<Publication {...props} id={snapshot.id} access={administrator} />);
  await screen.findByRole("heading", { name: "Generation 1: candidate" });
  expect(get).toHaveBeenCalledWith(
    "/api/v1/publication/snapshots/{snapshotId}",
    expect.objectContaining({
      params: {
        path: { snapshotId: snapshot.id },
        query: { host: snapshot.host },
      },
    }),
  );
});

it.each(["reader", "namespace-publisher", "missing-scope"])(
  "does not load host evidence for %s",
  (admission) => {
    const { props, get } = screenWorkspace();
    const access =
      admission === "missing-scope"
        ? { ...administrator, scopeEnforcement: true, scopes: [] }
        : admission === "namespace-publisher"
          ? {
              ...props.access,
              namespaceGrants: [
                {
                  namespaceId: "ns-1",
                  permissions: testPermissions(["publisher" as const]),
                },
              ],
            }
          : props.access;
    render(<Publication {...props} access={access} />);
    expect(screen.getByRole("alert").textContent).toContain("publication.read");
    expect(screen.getByRole("alert").textContent).toContain(
      "When scopes are enforced",
    );
    expect(get).not.toHaveBeenCalled();
  },
);

it.each(["/docs/", "", "javascript:alert(1)"])(
  "validates operator documentation destination %s",
  (documentationUrl) => {
    const { props, get } = screenWorkspace();
    get.mockResolvedValue(etagReply({ items: [] }));
    render(
      <Publication
        {...props}
        access={administrator}
        runtime={{ ...props.runtime, documentationUrl }}
      />,
    );
    const link = screen.queryByRole("link", {
      name: "Operator documentation",
    });
    if (documentationUrl !== "/docs/") expect(link).toBeNull();
    else {
      expect(link?.getAttribute("href")).toBe("/docs/guides/publishing/");
      expect(link?.getAttribute("target")).toBe("_blank");
      expect(link?.getAttribute("rel")).toBe("noopener noreferrer");
      expect(link?.getAttribute("aria-describedby")).toBe(
        "operator-docs-new-tab",
      );
    }
  },
);

it("selects hosts explicitly, pages snapshots and builds an inspectable candidate", async () => {
  const { props, get, post } = screenWorkspace();
  get
    .mockResolvedValueOnce(
      etagReply({ items: [{ host: snapshot.host, mode: "anonymous" }] }),
    )
    .mockResolvedValueOnce(etagReply({ items: [snapshot], nextCursor: "next" }))
    .mockResolvedValue(etagReply({ items: [] }));
  post.mockResolvedValue(etagReply({ ...snapshot, id: "candidate-2" }));
  render(
    <Publication
      {...props}
      runtime={{
        ...props.runtime,
        documentationUrl: "https://docs.example.test/manual/",
      }}
      access={administrator}
    />,
  );
  expect(
    screen.getByRole("link", { name: "Documentation" }).getAttribute("href"),
  ).toBe("https://docs.example.test/manual/");
  await screen.findByRole("combobox", { name: "Configured publication hosts" });
  const snapshotGuidance = screen.getByText(
    /A snapshot fixes the released listings/,
  );
  expect(snapshotGuidance.textContent).toContain("activate it separately");
  expect(snapshotGuidance.closest("details")).toBeNull();
  expect(get).toHaveBeenCalledOnce();
  change("Configured publication hosts", "catalog.example.com");
  expect(post).not.toHaveBeenCalled();
  expect(get).toHaveBeenCalledOnce();
  click("Open host");
  expect(
    (await screen.findByRole("link", { name: "Generation 1" })).getAttribute(
      "href",
    ),
  ).toBe("/unit/publication/snapshot-1?host=catalog.example.com");
  click("Next page");
  await screen.findByText("No snapshots on this page.");
  expect(get.mock.calls[2]?.[1]).toMatchObject({
    params: { query: { host: snapshot.host, cursor: "next" } },
  });
  await confirmCommand("Build candidate snapshot");
  await screen.findByRole("link", { name: "Inspect candidate" });
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/publication/snapshots",
    { body: { host: snapshot.host } },
  ]);
});

it.each(["candidate", "retired"])(
  "binds %s transition to the chosen host and retained revision",
  async (state) => {
    const { props, get, post } = screenWorkspace();
    history.replaceState(
      null,
      "",
      "/unit/publication/snapshot-1?host=catalog.example.com",
    );
    get.mockImplementation(async (path) =>
      path === "/api/v1/publication/hosts"
        ? etagReply({ items: [] })
        : etagReply({
            ...snapshot,
            state,
            exclusions: [
              {
                resourceId: "excluded",
                identifier: "urn:excluded",
                profiles: ["ard-profile"],
                reasons: ["not eligible"],
              },
            ],
          }),
    );
    post.mockResolvedValue(etagReply({ ...snapshot, state: "active" }));
    render(<Publication {...props} id={snapshot.id} access={administrator} />);
    await screen.findByText("urn:excluded");
    expect(
      screen.getByText(
        state === "candidate"
          ? /This candidate is not served/
          : /This retained snapshot is not active/,
      ),
    ).toBeTruthy();
    change("Reason", "  Approved snapshot  ");
    await confirmCommand(
      state === "candidate"
        ? "Activate candidate"
        : "Restore retained snapshot",
    );
    await screen.findByText(/Discovery snapshot activated/);
    expect(screen.getByText(/displayed snapshot predates/)).toBeTruthy();
    expect(
      get.mock.calls.filter(
        ([path]) => path === "/api/v1/publication/snapshots/{snapshotId}",
      ),
    ).toHaveLength(1);
    get.mockImplementation(async (path) =>
      etagReply(
        path === "/api/v1/publication/hosts"
          ? { items: [] }
          : { ...snapshot, state: "active" },
      ),
    );
    click("Reload snapshot state");
    await screen.findByRole("heading", { name: "Generation 1: active" });
    expect(get).toHaveBeenLastCalledWith(
      "/api/v1/publication/snapshots/{snapshotId}",
      expect.objectContaining({
        params: {
          path: { snapshotId: snapshot.id },
          query: { host: snapshot.host },
        },
      }),
    );
    expect(post.mock.calls[0]).toMatchObject([
      `/api/v1/publication/snapshots/{snapshotId}:${state === "candidate" ? "activate" : "rollback"}`,
      {
        params: {
          path: { snapshotId: snapshot.id },
          header: { "If-Match": '"revision-1"' },
        },
        body: { host: snapshot.host, reason: "Approved snapshot" },
      },
    ]);
  },
);

it("withdraws only the selected host and does not grant activation for an active snapshot", async () => {
  const { props, get, post } = screenWorkspace();
  history.replaceState(null, "", "/unit/publication?host=catalog.example.com");
  get
    .mockResolvedValueOnce(etagReply({ items: [] }))
    .mockResolvedValue(etagReply({ items: [] }));
  post.mockResolvedValue(etagReply({ ...snapshot, state: "retired" }));
  const view = render(<Publication {...props} access={administrator} />);
  await screen.findByText("No snapshots on this page.");
  screen.getByText("Withdraw public discovery").click();
  change("Reason", "Withdraw");
  await confirmCommand("Unpublish host");
  await screen.findByText(/Host unpublished/);
  expect(screen.getByText(/inventory predates/)).toBeTruthy();
  click("Refresh snapshots");
  await waitFor(() =>
    expect(screen.queryByText(/inventory predates/)).toBeNull(),
  );
  expect(get).toHaveBeenLastCalledWith(
    "/api/v1/publication/snapshots",
    expect.objectContaining({
      params: { query: { host: snapshot.host, limit: 25 } },
    }),
  );
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/publication:unpublish",
    { body: { host: snapshot.host, reason: "Withdraw" } },
  ]);
  get.mockResolvedValue(etagReply({ ...snapshot, state: "active" }));
  view.rerender(
    <Publication {...props} id={snapshot.id} access={administrator} />,
  );
  await screen.findByText("No exclusions reported.");
  expect(
    screen.getByText(/Later listing releases are not added automatically/),
  ).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Activate candidate" }),
  ).toBeNull();
});

it("keeps disabled choices visible, requires explicit opening, and preserves an open host when inventory changes", async () => {
  const { props, get, post } = screenWorkspace();
  let inventories = 0;
  get.mockImplementation(async (path) => {
    if (path === "/api/v1/publication/hosts") {
      inventories++;
      return etagReply({
        items:
          inventories === 1
            ? [
                { host: "disabled.example", mode: "disabled" },
                { host: snapshot.host, mode: "anonymous" },
                { host: "private.example", mode: "authenticated" },
              ]
            : [],
      });
    }
    expect(path).toBe("/api/v1/publication/snapshots");
    return etagReply({ items: [snapshot] });
  });
  render(<Publication {...props} access={administrator} />);
  const selector = await screen.findByRole("combobox", {
    name: "Configured publication hosts",
  });
  expect((selector as HTMLSelectElement).value).toBe("");
  expect(
    (
      screen.getByRole("option", {
        name: "disabled.example — disabled",
      }) as HTMLOptionElement
    ).disabled,
  ).toBe(true);
  expect(get).toHaveBeenCalledOnce();
  change("Configured discovery host", "disabled.example");
  expect(
    (screen.getByRole("button", { name: "Open host" }) as HTMLButtonElement)
      .disabled,
  ).toBe(true);
  fireEvent.submit(
    screen.getByRole("button", { name: "Open host" }).closest("form")!,
  );
  expect(get).toHaveBeenCalledOnce();
  change("Configured publication hosts", snapshot.host);
  expect(
    (
      screen.getByRole("textbox", {
        name: "Configured discovery host",
      }) as HTMLInputElement
    ).value,
  ).toBe(snapshot.host);
  expect(get).toHaveBeenCalledOnce();
  click("Open host");
  await screen.findByRole("link", { name: "Generation 1" });
  click("Refresh host choices");
  await screen.findByText("No publication hosts configured for your tenant.");
  expect(screen.getByRole("link", { name: "Generation 1" })).toBeTruthy();
  expect(
    (
      screen.getByRole("textbox", {
        name: "Configured discovery host",
      }) as HTMLInputElement
    ).value,
  ).toBe(snapshot.host);
  expect(get).toHaveBeenCalledTimes(3);
  expect(post).not.toHaveBeenCalled();
});

it("recovers failed and malformed inventories without blocking exact manual host commands", async () => {
  const { props, get, post } = screenWorkspace();
  let attempts = 0;
  get.mockImplementation(async (path) => {
    if (path === "/api/v1/publication/hosts") {
      attempts++;
      if (attempts === 1) throw new APIError(503, "UNAVAILABLE");
      return etagReply(
        attempts === 2
          ? { items: [{ host: snapshot.host, mode: "unsafe" }] }
          : { items: [] },
      );
    }
    expect(path).toBe("/api/v1/publication/snapshots");
    return etagReply({ items: [] });
  });
  post.mockResolvedValue(etagReply(snapshot));
  render(<Publication {...props} access={administrator} />);
  await screen.findByRole("alert");
  change("Configured discovery host", " catalog.example.com ");
  click("Open host");
  await screen.findByText("No snapshots on this page.");
  click("Reload");
  await waitFor(() => expect(props.onFailure).toHaveBeenCalledTimes(2));
  expect(
    screen.queryByRole("combobox", { name: "Configured publication hosts" }),
  ).toBeNull();
  expect(screen.getByText("No snapshots on this page.")).toBeTruthy();
  click("Reload");
  await screen.findByText("No publication hosts configured for your tenant.");
  await confirmCommand("Build candidate snapshot");
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/publication/snapshots",
    { body: { host: snapshot.host } },
  ]);
  expect(get).toHaveBeenCalledTimes(4);
});

it("retains bookmarked snapshot input and its prepared activation through an inventory failure", async () => {
  const { props, get, post } = screenWorkspace();
  history.replaceState(
    null,
    "",
    "/unit/publication/snapshot-1?host=catalog.example.com",
  );
  get.mockImplementation(async (path) => {
    if (path === "/api/v1/publication/hosts")
      throw new APIError(503, "UNAVAILABLE");
    expect(path).toBe("/api/v1/publication/snapshots/{snapshotId}");
    return etagReply(snapshot);
  });
  post.mockResolvedValue(etagReply({ ...snapshot, state: "active" }));
  render(<Publication {...props} id={snapshot.id} access={administrator} />);
  await screen.findByText("No exclusions reported.");
  change("Reason", "Confirmed bookmarked host");
  click("Activate candidate");
  await screen.findByRole("dialog");
  click("Refresh host choices");
  await waitFor(() => expect(props.onFailure).toHaveBeenCalledTimes(2));
  change("Configured discovery host", "another.example");
  click("Confirm");
  await screen.findByText(/Discovery snapshot activated/);
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/publication/snapshots/{snapshotId}:activate",
    {
      params: {
        path: { snapshotId: snapshot.id },
        header: {
          "If-Match": '"revision-1"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
      body: { host: snapshot.host, reason: "Confirmed bookmarked host" },
    },
  ]);
  expect(
    get.mock.calls.filter(([path]) => path !== "/api/v1/publication/hosts"),
  ).toHaveLength(1);
});

it("clears host selection and fences an outstanding inventory when the authorized identity changes", async () => {
  const { props, get } = screenWorkspace();
  let finish: (value: ReturnType<typeof etagReply>) => void = () => {
    throw new Error("Read not started");
  };
  get
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    )
    .mockResolvedValue(etagReply({ items: [] }));
  const view = render(<Publication {...props} access={administrator} />);
  await waitFor(() => expect(get).toHaveBeenCalledOnce());
  change("Configured discovery host", snapshot.host);
  const oldOptions = get.mock.calls[0]?.[1];
  view.rerender(
    <Publication
      {...props}
      access={{
        ...administrator,
        tenantId: "tenant-b",
        subject: "another-subject",
      }}
    />,
  );
  await screen.findByText("No publication hosts configured for your tenant.");
  expect(oldOptions).toMatchObject({ signal: { aborted: true } });
  await act(async () =>
    finish(
      etagReply({
        items: [{ host: "old-private.example", mode: "anonymous" }],
      }),
    ),
  );
  expect(screen.queryByRole("option", { name: /old-private/ })).toBeNull();
  expect(
    (
      screen.getByRole("textbox", {
        name: "Configured discovery host",
      }) as HTMLInputElement
    ).value,
  ).toBe("");
  view.rerender(<Publication {...props} />);
  expect(
    screen.queryByRole("region", { name: "Publication host choices" }),
  ).toBeNull();
  expect(get).toHaveBeenCalledTimes(2);
});

it.each(["publication.generate", "publication.unpublish"] as const)(
  "separates build and withdrawal authority (%s)",
  async (permission) => {
    const { props, get } = screenWorkspace();
    history.replaceState(null, "", `/unit/publication?host=${snapshot.host}`);
    get.mockImplementation(async (path) =>
      etagReply({
        items:
          path === "/api/v1/publication/hosts"
            ? [{ host: snapshot.host, mode: "anonymous" }]
            : [snapshot],
      }),
    );
    render(
      <Publication
        {...props}
        access={{
          ...administrator,
          permissions: ["publication.read", permission],
          availablePermissions: ["publication.read", permission],
        }}
      />,
    );
    await screen.findByRole("heading", { name: snapshot.host });
    // The host heading and recovery pager also remain visible while loading.
    await screen.findByRole("link", { name: "Generation 1" });
    const permitted =
      permission === "publication.generate"
        ? "Build candidate snapshot"
        : "Unpublish host";
    const unavailable =
      permission === "publication.generate"
        ? "Unpublish host"
        : "Build candidate snapshot";
    expect(screen.getByRole("button", { name: permitted })).toHaveProperty(
      "disabled",
      false,
    );
    expect(screen.queryByRole("button", { name: unavailable })).toBeNull();
  },
);

it.each(["human", "service"] as const)(
  "opens sharing-only bookmarks for a %s without publication reads",
  async (actorType) => {
    history.replaceState(
      {},
      "",
      "/unit/publication?host=private.example&audience=authenticated",
    );
    const w = screenWorkspace();
    w.get.mockResolvedValue(
      etagReply({
        host: "private.example",
        audience: "authenticated",
        enabled: false,
        revision: 0,
      }),
    );
    w.post.mockResolvedValue(
      etagReply({
        host: "private.example",
        audience: "authenticated",
        enabled: true,
        revision: 1,
      }),
    );
    const access = {
      ...w.props.access,
      actorType,
      permissions: ["ratings.sharing.manage" as const],
      availablePermissions: ["ratings.sharing.manage" as const],
      namespaceGrants: [],
      scopeEnforcement: true,
      scopes: ["tenant.admin" as const],
    };
    const view = render(
      <Publication {...w.props} access={access} id="snapshot-bookmark" />,
    );
    expect(
      screen.queryByRole("button", { name: "Refresh host choices" }),
    ).toBeNull();
    click("Rating summary sharing");
    await screen.findByLabelText("Sharing reason");
    change("Sharing reason", "Share only consented aggregate opinions");
    await confirmCommand("Enable sharing");
    expect(w.post).toHaveBeenCalledWith(
      "/api/v1/ratings/sharing",
      expect.objectContaining({
        body: {
          host: "private.example",
          audience: "authenticated",
          enabled: true,
          reason: "Share only consented aggregate opinions",
        },
      }),
    );
    expect(
      w.get.mock.calls.every(([path]) => path === "/api/v1/ratings/sharing"),
    ).toBe(true);
    view.rerender(
      <Publication {...w.props} access={{ ...access, scopes: [] }} />,
    );
    expect(
      screen.queryByRole("button", { name: "Rating summary sharing" }),
    ).toBeNull();
  },
);

it("keeps sharing bound to the applied host and asks before discarding dirty context", async () => {
  const w = screenWorkspace();
  const hosts = [
    { host: "public.example", mode: "anonymous" },
    { host: "private.example", mode: "authenticated" },
  ];
  let inventoryFailed = false;
  w.get.mockImplementation(async (path, options) => {
    if (path === "/api/v1/publication/hosts") {
      if (inventoryFailed) throw new Error("Controlled inventory outage");
      return etagReply({ items: hosts });
    }
    if (path === "/api/v1/ratings/sharing")
      return etagReply({
        ...(
          options as { params: { query: { host: string; audience: string } } }
        ).params.query,
        enabled: false,
        revision: 0,
      });
    return etagReply({ items: [] });
  });
  render(<Publication {...w.props} access={administrator} />);
  await screen.findByLabelText("Configured publication hosts");
  change("Configured publication hosts", "public.example");
  change("Configured publication hosts", "");
  expect(screen.getByLabelText("Configured discovery host")).toHaveProperty(
    "value",
    "",
  );
  change("Configured publication hosts", "public.example");
  click("Open host");
  click("Rating summary sharing");
  await screen.findByLabelText("Sharing reason");
  change("Sharing reason", "Preserve this draft");
  change("Configured publication hosts", "private.example");
  expect(screen.getByLabelText("Sharing reason")).toHaveProperty(
    "value",
    "Preserve this draft",
  );
  expect(
    w.get.mock.calls.filter(([path]) => path === "/api/v1/ratings/sharing"),
  ).toHaveLength(1);
  inventoryFailed = true;
  click("Refresh host choices");
  await screen.findByRole("alert");
  expect(screen.getByLabelText("Sharing reason")).toHaveProperty(
    "value",
    "Preserve this draft",
  );
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Rating summary sharing");
  expect(screen.getByLabelText("Sharing reason")).toHaveProperty(
    "value",
    "Preserve this draft",
  );
  click("Open host");
  expect(screen.getByLabelText("Sharing reason")).toHaveProperty(
    "value",
    "Preserve this draft",
  );
  w.post.mockResolvedValue(
    etagReply({
      host: "public.example",
      audience: "anonymous",
      enabled: true,
      revision: 1,
    }),
  );
  await confirmCommand("Enable sharing");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/ratings/sharing",
    expect.objectContaining({
      body: {
        host: "public.example",
        audience: "anonymous",
        enabled: true,
        reason: "Preserve this draft",
      },
    }),
  );
  await waitFor(() =>
    expect(screen.getByLabelText("Sharing reason")).toHaveProperty("value", ""),
  );
  change("Sharing reason", "A second unsaved draft");
  discard.mockReturnValue(true);
  click("Open host");
  click("Rating summary sharing");
  await screen.findByLabelText("Sharing reason");
  expect(screen.getByLabelText("Sharing reason")).toHaveProperty("value", "");
  expect(
    w.get.mock.calls
      .filter(([path]) => path === "/api/v1/ratings/sharing")
      .at(-1)?.[1],
  ).toMatchObject({
    params: { query: { host: "private.example", audience: "authenticated" } },
  });
});

it.each([false, true])(
  "keeps independently authorized sharing when publication-read access is removed (uncertain: %s)",
  async (uncertain) => {
    history.replaceState(
      null,
      "",
      `/unit/publication?host=${snapshot.host}&audience=anonymous`,
    );
    const w = screenWorkspace();
    let hostReads = 0;
    w.get.mockImplementation(async (path) => {
      if (path === "/api/v1/publication/hosts") {
        hostReads++;
        return etagReply({
          items: [
            { host: snapshot.host, mode: "anonymous" },
            ...(hostReads === 1
              ? [{ host: "private-inventory.example", mode: "anonymous" }]
              : []),
          ],
        });
      }
      if (path === "/api/v1/ratings/sharing")
        return etagReply({
          host: snapshot.host,
          audience: "anonymous",
          enabled: false,
          revision: 0,
        });
      return etagReply({ items: [] });
    });
    const saved = {
      host: snapshot.host,
      audience: "anonymous",
      enabled: true,
      revision: 1,
    };
    if (uncertain)
      w.post.mockRejectedValueOnce(new APIError(503, "DEPENDENCY_UNAVAILABLE"));
    w.post.mockResolvedValue(etagReply(saved));
    const view = render(<Publication {...w.props} access={administrator} />);
    await screen.findByRole("option", {
      name: "private-inventory.example — anonymous",
    });
    click("Rating summary sharing");
    await screen.findByLabelText("Sharing reason");
    change("Sharing reason", "Keep this independently permitted draft");
    let dialog: HTMLElement | undefined;
    if (uncertain) {
      await confirmCommand("Enable sharing");
      dialog = screen.getByRole("dialog");
      await within(dialog).findByRole("button", { name: "Retry same command" });
    }
    view.rerender(
      <Publication
        {...w.props}
        access={{
          ...administrator,
          permissions: administrator.permissions.filter(
            (permission) => permission !== "publication.read",
          ),
        }}
      />,
    );
    expect(screen.getByLabelText("Sharing reason")).toHaveProperty(
      "value",
      "Keep this independently permitted draft",
    );
    expect(screen.queryByLabelText("Configured publication hosts")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Refresh snapshots" }),
    ).toBeNull();
    expect(hostReads).toBe(1);
    if (uncertain) {
      expect(screen.getByRole("dialog")).toBe(dialog);
      click("Retry same command");
    } else {
      expect(w.post).not.toHaveBeenCalled();
      await confirmCommand("Enable sharing");
    }
    await screen.findByText("Rating sharing choice saved.");
    expect(w.post).toHaveBeenCalledTimes(uncertain ? 2 : 1);
    if (uncertain) expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
    expect(w.post.mock.calls[0]).toEqual([
      "/api/v1/ratings/sharing",
      {
        body: {
          host: snapshot.host,
          audience: "anonymous",
          enabled: true,
          reason: "Keep this independently permitted draft",
        },
        params: {
          header: { "Idempotency-Key": expect.any(String) as unknown },
        },
      },
    ]);
    view.rerender(<Publication {...w.props} access={administrator} />);
    await waitFor(() => expect(hostReads).toBe(2));
    await screen.findByLabelText("Configured publication hosts");
    expect(
      screen.queryByRole("option", {
        name: "private-inventory.example — anonymous",
      }),
    ).toBeNull();
  },
);

it("ignores a late sharing reply after an explicit host replacement", async () => {
  const w = screenWorkspace();
  const old = deferred<ReturnType<typeof etagReply>>();
  let oldSignal: AbortSignal | undefined;
  w.get.mockImplementation((path, options) => {
    if (path !== "/api/v1/ratings/sharing") throw new Error("Unexpected read");
    const request = options as {
      params: { query: { host: string; audience: string } };
      signal: AbortSignal;
    };
    if (request.params.query.host === "first.example") {
      oldSignal = request.signal;
      return old.promise;
    }
    return Promise.resolve(
      etagReply({ ...request.params.query, enabled: true, revision: 2 }),
    );
  });
  const access = {
    ...administrator,
    permissions: ["ratings.sharing.manage" as const],
    availablePermissions: ["ratings.sharing.manage" as const],
  };
  render(<Publication {...w.props} access={access} />);
  change("Configured discovery host", "first.example");
  click("Open host");
  click("Rating summary sharing");
  await waitFor(() => expect(oldSignal).toBeDefined());
  change("Configured discovery host", "second.example");
  change("Discovery audience", "authenticated");
  click("Open host");
  click("Rating summary sharing");
  const sharingState = await screen.findByText("Enabled", {
    selector: ".workspace-status",
  });
  expect(sharingState.getAttribute("data-tone")).toBe("neutral");
  expect(sharingState.parentElement?.textContent).toContain(
    "Audience opt-in: Enabled",
  );
  expect(oldSignal?.aborted).toBe(true);
  await act(async () =>
    old.resolve(
      etagReply({
        host: "first.example",
        audience: "anonymous",
        enabled: false,
        revision: 0,
      }),
    ),
  );
  expect(
    screen.queryByText("Disabled", { selector: ".workspace-status" }),
  ).toBeNull();
  expect(screen.getByText(/Rating sharing for/).textContent).toContain(
    "second.example",
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("refreshes the selected snapshot page, deduplicates pending reads and finds newer candidates on First page", async () => {
  const w = screenWorkspace();
  let newest = false;
  w.get.mockImplementation(async (path) =>
    etagReply(
      path === "/api/v1/publication/hosts"
        ? { items: [] }
        : {
            items: [
              {
                ...snapshot,
                id: newest ? "new-candidate" : snapshot.id,
                generation: newest ? 2 : 1,
              },
            ],
            nextCursor: "next",
          },
    ),
  );
  render(<Publication {...w.props} access={administrator} />);
  await screen.findByLabelText("Configured discovery host");
  change("Configured discovery host", snapshot.host);
  click("Open host");
  await screen.findByRole("link", { name: "Generation 1" });
  newest = true;
  click("Refresh snapshots");
  await screen.findByRole("link", { name: "Generation 2" });
  click("Next page");
  await screen.findByRole("button", { name: "First page" });
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.get.mockReturnValueOnce(pending.promise);
  click("Refresh snapshots");
  expect(
    screen.getByRole("button", { name: "Refresh snapshots" }),
  ).toHaveProperty("disabled", true);
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { host: snapshot.host, cursor: "next" } },
  });
  await act(async () => pending.resolve(etagReply({ items: [snapshot] })));
  click("First page");
  await screen.findByRole("link", { name: "Generation 2" });
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { host: snapshot.host } },
  });
  expect(
    (
      w.get.mock.calls.at(-1)?.[1] as {
        params: { query: Record<string, string> };
      }
    ).params.query.cursor,
  ).toBeUndefined();
  expect(w.post).not.toHaveBeenCalled();
});
