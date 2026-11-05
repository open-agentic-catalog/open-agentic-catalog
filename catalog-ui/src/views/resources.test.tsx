import { testAccess, testPermissions } from "../../tests/unit/access-fixtures";
// @vitest-environment jsdom
import { expect, it } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import Resources from "./Resources";
import { APIError } from "../api/client";
import { deferred, reply, sessionAccess } from "../../tests/unit/fixtures";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  listing,
  namespace,
  readReplies,
  resource,
  screenLifecycle,
  screenWorkspace,
  version,
} from "../../tests/unit/screens";
screenLifecycle("/unit/resources");

it("applies public kind presets and history changes without serializing private identifier drafts", async () => {
  history.replaceState(null, "", "/unit/resources?kind=agent");
  const { props, get } = screenWorkspace();
  readReplies(get, { "/api/v1/resources": { items: [resource] } });
  render(<Resources {...props} />);
  await screen.findByRole("link", { name: resource.canonicalIdentifier });
  expect(get.mock.calls.at(-1)?.[1]).toHaveProperty(
    "params.query.kind",
    "agent",
  );
  change("Exact canonical identifier", "private unsubmitted input");
  await act(async () => {
    history.pushState(null, "", "/unit/resources?kind=plugin");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(get.mock.calls.at(-1)?.[1]).toHaveProperty(
    "params.query.kind",
    "plugin",
  );
  expect(screen.getByLabelText("Exact canonical identifier")).toHaveProperty(
    "value",
    "private unsubmitted input",
  );
  change("Resource kind", "");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty("params.query.kind"),
  );
  expect(location.search).toBe("");
  expect(history.state).toBeNull();
});

it.each(["x", "🙂"])(
  "rejects an oversized identifier (%s) before preparing a command",
  async (character) => {
    // Validate the form, not cold module loading; browser tests cover chunks.
    await import("./AuthoringWorkspace");
    const { props, get, post } = screenWorkspace();
    readReplies(get, { "/api/v1/namespaces": { items: [namespace] } });
    render(<Resources {...props} id="new" access={administrator} />);
    const namespaces = await screen.findByRole("combobox", {
      name: "Namespace",
    });
    await within(namespaces).findByRole("option", {
      name: `${namespace.displayName} / ${namespace.slug} / ${namespace.id}`,
    });
    change("Namespace", namespace.id);
    change("Canonical resource identifier", character.repeat(513));
    click("Create resource");
    await screen.findByText(
      "Canonical identifier needs 1 to 512 characters without NUL.",
    );
    expect(post).not.toHaveBeenCalled();
  },
);

it("keeps Reader history accessible without offering creation and respects an administrator's scope ceiling", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, {
    "/api/v1/resources": { items: [resource] },
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resources/{resourceId}/versions": { items: [version] },
    "/api/v1/namespaces": { items: [namespace] },
  });
  const view = render(<Resources {...props} />);
  await screen.findByRole("link", { name: resource.canonicalIdentifier });
  expect(screen.queryByRole("link", { name: "Create resource" })).toBeNull();
  expect(
    screen.getByText(
      "The resource.create permission is required for this action.",
    ),
  ).toBeTruthy();
  view.rerender(<Resources {...props} id={resource.id} />);
  await screen.findByRole("link", { name: "Version 1.0.0" });
  expect(screen.queryByRole("link", { name: "Add version" })).toBeNull();
  expect(
    screen.getByText(
      "The resource.edit permission is required in this namespace.",
    ),
  ).toBeTruthy();
  view.rerender(
    <Resources
      {...props}
      access={{
        ...administrator,
        scopeEnforcement: true,
        scopes: ["catalog.read"],
      }}
    />,
  );
  await screen.findByRole("link", { name: resource.canonicalIdentifier });
  expect(screen.queryByRole("link", { name: "Create resource" })).toBeNull();
  expect(screen.getByText(/catalog.write scope is required/)).toBeTruthy();
  view.rerender(<Resources {...props} access={administrator} />);
  expect(
    screen.getByRole("link", { name: "Create resource" }).getAttribute("href"),
  ).toBe("/unit/resources/new");
  view.rerender(<Resources {...props} id="new" />);
  await screen.findByRole("heading", { name: "Create resource" });
  expect(screen.queryByLabelText("Namespace")).toBeNull();
  expect(screen.queryByLabelText("Canonical resource identifier")).toBeNull();
  expect(screen.queryByRole("button", { name: "Create resource" })).toBeNull();
  expect(
    screen.getByText(
      "The resource.create permission is required for this action.",
    ),
  ).toBeTruthy();
  expect(post).not.toHaveBeenCalled();
});

it.each([true, false])(
  "resolves an incomplete grant page only for the chosen namespace (allowed: %s)",
  async (allowed) => {
    const { props, get, post } = screenWorkspace();
    const target = "00000000-0000-0000-0000-000000000020";
    const access = {
      ...sessionAccess,
      availablePermissions: [
        ...sessionAccess.availablePermissions,
        "resource.create" as const,
      ],
      nextCursor: "more-grants",
    };
    const pending = deferred<unknown>();
    get.mockImplementation(async (path) => {
      if (path === "/api/v1/resources") return reply({ items: [] });
      if (path === "/api/v1/namespaces")
        return reply({ items: [{ ...namespace, id: target }] });
      if (path === "/api/v1/me") return pending.promise;
      throw new Error("Unexpected read");
    });
    const view = render(<Resources {...props} access={access} />);
    await screen.findByText("No resources match these filters.");
    expect(
      screen
        .getByRole("link", { name: "Choose namespace to create" })
        .getAttribute("href"),
    ).toBe("/unit/resources/new");
    expect(get.mock.calls.map(([path]) => path)).toEqual(["/api/v1/resources"]);
    view.rerender(<Resources {...props} access={access} id="new" />);
    await screen.findByRole("option", {
      name: `${namespace.displayName} / ${namespace.slug} / ${target}`,
    });
    expect(
      screen.getByRole("button", { name: "Create resource" }),
    ).toHaveProperty("disabled", true);
    change("Namespace", target);
    change(
      "Canonical resource identifier",
      "urn:air:example.com:agents:scoped",
    );
    await screen.findByText(/has not been confirmed/);
    expect(
      screen.getByRole("button", { name: "Create resource" }),
    ).toHaveProperty("disabled", true);
    await act(async () =>
      pending.resolve(
        reply({
          ...sessionAccess,
          namespaceGrants: allowed
            ? [
                {
                  namespaceId: target,
                  permissions: testPermissions(["contributor"]),
                },
              ]
            : [],
        }),
      ),
    );
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Create resource" }),
      ).toHaveProperty("disabled", !allowed),
    );
    expect(get.mock.calls.filter(([path]) => path === "/api/v1/me")).toEqual([
      [
        "/api/v1/me",
        expect.objectContaining({ params: { query: { namespaceId: target } } }),
      ],
    ]);
    expect(post).not.toHaveBeenCalled();
    if (allowed) {
      post.mockResolvedValue(
        reply({
          ...resource,
          tenantId: access.tenantId,
          namespaceId: target,
          canonicalIdentifier: "urn:air:example.com:agents:scoped",
        }),
      );
      await confirmCommand("Create resource");
      await screen.findByRole("link", { name: "Open created resource" });
      expect(post.mock.calls[0]).toMatchObject([
        "/api/v1/resources",
        { body: { namespaceId: target } },
      ]);
    } else {
      expect(
        screen.getByText(
          /resource.create permission is required in this namespace/,
        ),
      ).toBeTruthy();
      expect(
        screen.getByLabelText("Canonical resource identifier"),
      ).toHaveProperty("value", "urn:air:example.com:agents:scoped");
    }
  },
);

it("filters and pages resources without carrying a cursor into a new kind", async () => {
  const { props, get } = screenWorkspace();
  get
    .mockResolvedValueOnce(reply({ items: [resource], nextCursor: "next" }))
    .mockResolvedValueOnce(reply({ items: [] }))
    .mockResolvedValue(reply({ items: [resource] }));
  render(<Resources {...props} />);
  await screen.findByRole("link", { name: resource.canonicalIdentifier });
  expect(screen.getByLabelText("Resource kind")).toHaveProperty(
    "required",
    false,
  );
  expect(screen.getByRole("cell", { name: "Agent" })).toBeTruthy();
  expect(screen.getByRole("option", { name: "MCP Servers" })).toHaveProperty(
    "value",
    "mcp_server",
  );
  expect(screen.getByRole("option", { name: "Plugins" })).toHaveProperty(
    "value",
    "plugin",
  );
  click("Next page");
  await screen.findByText("No resources match these filters.");
  change("Resource kind", "skill");
  await screen.findByRole("link", { name: resource.canonicalIdentifier });
  expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { kind: "skill" } },
  });
  expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty("params.query.cursor");
  click("Refresh");
  await waitFor(() => expect(get).toHaveBeenCalledTimes(4));
  change("Resource kind", "");
  await waitFor(() => expect(get).toHaveBeenCalledTimes(5));
  expect(screen.getByRole("option", { name: "All kinds" })).toHaveProperty(
    "selected",
    true,
  );
  expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty("params.query.kind");
  expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty("params.query.cursor");
});

it.each([
  { name: "ASCII", identifier: "urn:air:example.com:skills:test" },
  { name: "Unicode boundary", identifier: "🙂".repeat(512) },
])(
  "creates a selected resource kind with a $name identifier after confirmation",
  async ({ identifier }) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      "/api/v1/namespaces": { items: [namespace], nextCursor: "ns-next" },
    });
    post.mockResolvedValue(
      reply({
        ...resource,
        kind: "skill",
        canonicalIdentifier: identifier,
      }),
    );
    render(<Resources {...props} id="new" access={administrator} />);
    await screen.findByRole("option", {
      name: `${namespace.displayName} / ${namespace.slug} / ${namespace.id}`,
    });
    for (const [label, value] of [
      ["Agent", "agent"],
      ["MCP Server", "mcp_server"],
      ["Skill", "skill"],
      ["Plugin", "plugin"],
    ] as const) {
      expect(screen.getByRole("option", { name: label })).toHaveProperty(
        "value",
        value,
      );
    }
    change("Namespace", namespace.id);
    click("Next page");
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
    change("Canonical resource identifier", identifier);
    expect(
      screen.getByLabelText("Canonical resource identifier"),
    ).toHaveProperty("maxLength", 1024);
    change("Kind", "skill");
    await confirmCommand("Create resource");
    expect(
      (
        await screen.findByRole("link", { name: "Open created resource" })
      ).getAttribute("href"),
    ).toBe("/unit/resources/resource-1");
    expect(post).toHaveBeenCalledExactlyOnceWith("/api/v1/resources", {
      params: {
        header: { "Idempotency-Key": expect.stringMatching(/\S+/) as unknown },
      },
      body: {
        namespaceId: namespace.id,
        canonicalIdentifier: identifier,
        kind: "skill",
      },
    });
  },
);

it("refreshes each resource history and creates a listing with the exact parent", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, {
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resources/{resourceId}/versions": {
      nextCursor: "versions-next",
      items: [
        version,
        {
          ...version,
          id: "version-2",
          version: "2.0",
          sealedRevisionId: "rev-2",
        },
      ],
    },
    "/api/v1/resources/{resourceId}/listings": {
      nextCursor: "listings-next",
      items: [
        listing,
        { ...listing, id: "listing-2", currentReleaseId: "release" },
      ],
    },
  });
  post.mockResolvedValue(reply({ ...listing, id: "listing-new" }));
  render(<Resources {...props} id={resource.id} access={administrator} />);
  await screen.findByRole("link", { name: "Version 1.0.0" });
  expect(
    screen.getByRole("group", { name: "Resource versions pagination" }),
  ).toBeTruthy();
  expect(
    screen.queryByRole("group", { name: "Resource listings pagination" }),
  ).toBeNull();
  click("Versions");
  await waitFor(() =>
    expect(
      get.mock.calls.filter(([path]) => String(path).endsWith("/versions")),
    ).toHaveLength(2),
  );
  click("Next page");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)).toMatchObject([
      "/api/v1/resources/{resourceId}/versions",
      {
        params: {
          path: { resourceId: resource.id },
          query: { cursor: "versions-next" },
        },
      },
    ]),
  );
  click("Listings");
  await screen.findByRole("link", { name: "Listing listing-1" });
  expect(
    screen.getByRole("group", { name: "Resource listings pagination" }),
  ).toBeTruthy();
  expect(
    screen.queryByRole("group", { name: "Resource versions pagination" }),
  ).toBeNull();
  expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty("params.query.cursor");
  click("Next page");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)).toMatchObject([
      "/api/v1/resources/{resourceId}/listings",
      {
        params: {
          path: { resourceId: resource.id },
          query: { cursor: "listings-next" },
        },
      },
    ]),
  );
  expect(
    (
      await screen.findByRole("link", { name: "Open existing listing" })
    ).getAttribute("href"),
  ).toBe("/unit/listings/listing-1");
  expect(screen.queryByRole("button", { name: "Create listing" })).toBeNull();
  expect(post).not.toHaveBeenCalled();
});

it.each([204, 403, 404, 409, 412])(
  "handles resource deletion HTTP %s with exact revision and reason",
  async (status) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      "/api/v1/resources/{resourceId}": resource,
      "/api/v1/resources/{resourceId}/versions": { items: [] },
    });
    post.mockResolvedValue({
      response: new Response(null, { status }),
      ...(status !== 204 ? { error: {} } : {}),
    });
    render(
      <Resources
        {...props}
        id={resource.id}
        access={{ ...props.access, ...testAccess(["reader", "contributor"]) }}
      />,
    );
    await screen.findByText("No versions yet.");
    screen.getByText("Discard an unused resource").click();
    change("Reason", "  Remove draft  ");
    await confirmCommand("Delete unused resource");
    if (status === 204) await screen.findByText(/Resource deleted/);
    else {
      await screen.findByRole("alert");
      expect(props.onFailure).toHaveBeenCalledTimes(1);
    }
    expect(post.mock.calls[0]).toMatchObject([
      "/api/v1/resources/{resourceId}:delete",
      {
        params: {
          path: { resourceId: resource.id },
          header: { "If-Match": '"revision-1"' },
        },
        body: { reason: "Remove draft" },
      },
    ]);
  },
);

it("denies browsing without permissions and leaves exact creator admission to the Catalog", async () => {
  const { props, get, post } = screenWorkspace();
  const view = render(
    <Resources {...props} access={{ ...props.access, ...testAccess([]) }} />,
  );
  expect(screen.getByRole("alert").textContent).toContain("resource.read");
  expect(screen.getByRole("alert").textContent).toContain(
    "Creating a resource requires resource.create in its destination namespace.",
  );
  expect(screen.getByRole("alert").textContent).toContain(
    "When scopes are enforced",
  );
  expect(get).not.toHaveBeenCalled();
  get.mockImplementation(async (path) =>
    etagReply(
      String(path).endsWith("/versions")
        ? { items: [] }
        : { ...resource, createdBy: "other" },
    ),
  );
  view.rerender(
    <Resources
      {...props}
      id={resource.id}
      access={{ ...props.access, ...testAccess(["reader", "contributor"]) }}
    />,
  );
  await screen.findByText("No versions yet.");
  screen.getByText("Discard an unused resource").click();
  expect(
    screen.getByText(/The Catalog verifies exact creator identity/),
  ).toBeTruthy();
  post.mockRejectedValue(new APIError(403, "FORBIDDEN"));
  change("Reason", "Check exact creator identity");
  await confirmCommand("Delete unused resource");
  await screen.findByRole("alert");
  expect(props.onFailure).toHaveBeenCalledWith(
    expect.objectContaining({ code: "FORBIDDEN" }),
  );
  expect(screen.queryByText(/Resource deleted/)).toBeNull();
  expect(post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/resources/{resourceId}:delete",
    expect.objectContaining({
      body: { reason: "Check exact creator identity" },
    }),
  );
});

it("reads metadata without automatically fetching private resource history", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, { "/api/v1/resources/{resourceId}": resource });
  render(
    <Resources
      {...props}
      id={resource.id}
      access={{
        ...props.access,
        permissions: [],
        availablePermissions: ["resource.read"],
        namespaceGrants: [
          { namespaceId: resource.namespaceId, permissions: ["resource.read"] },
        ],
      }}
    />,
  );
  await screen.findByText(
    "Resource history requires resource.read_history for this namespace.",
  );
  expect(get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/resources/{resourceId}",
  ]);
  expect(post).not.toHaveBeenCalled();
});

it.each(["tenant", "namespace"])(
  "creates with %s authority and only the write scope, without directory or resource reads",
  async (scope) => {
    const w = screenWorkspace();
    const permissions = ["resource.create", "namespace.read"] as const;
    const access = {
      ...administrator,
      permissions: scope === "tenant" ? [...permissions] : [],
      availablePermissions: [...permissions],
      namespaceGrants:
        scope === "namespace"
          ? [{ namespaceId: namespace.id, permissions: [...permissions] }]
          : [],
      scopeEnforcement: true,
      scopes: ["catalog.write" as const],
    };
    w.post.mockResolvedValue(etagReply(resource));
    const view = render(<Resources {...w.props} access={access} />);
    expect(
      screen
        .getByRole("link", { name: "Create resource" })
        .getAttribute("href"),
    ).toBe("/unit/resources/new");
    expect(w.get).not.toHaveBeenCalled();
    view.rerender(<Resources {...w.props} access={access} id="new" />);
    change("Namespace ID", namespace.id);
    change("Canonical resource identifier", resource.canonicalIdentifier);
    await confirmCommand("Create resource");
    await screen.findByText("Created resource references");
    expect(w.post).toHaveBeenCalledExactlyOnceWith("/api/v1/resources", {
      body: {
        namespaceId: namespace.id,
        canonicalIdentifier: resource.canonicalIdentifier,
        kind: "agent",
      },
      params: { header: { "Idempotency-Key": expect.any(String) as unknown } },
    });
    expect(w.get).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("link", { name: "Open created resource" }),
    ).toBeNull();
    expect(
      screen.getByText("Created resource references").closest("details")
        ?.textContent,
    ).toContain(resource.id);
    view.rerender(<Resources {...w.props} access={access} id={resource.id} />);
    expect(screen.getByRole("alert").textContent).toContain("resource.read");
    expect(w.get).not.toHaveBeenCalled();
  },
);

it("checks the exact manually entered namespace instead of using another namespace's creation grant", async () => {
  const w = screenWorkspace();
  render(
    <Resources
      {...w.props}
      id="new"
      access={{
        ...administrator,
        permissions: [],
        availablePermissions: ["resource.create", "namespace.read"],
        namespaceGrants: [
          {
            namespaceId: namespace.id,
            permissions: ["resource.create", "namespace.read"],
          },
        ],
        scopeEnforcement: true,
        scopes: ["catalog.write"],
      }}
    />,
  );
  change("Namespace ID", "other-namespace");
  change("Canonical resource identifier", resource.canonicalIdentifier);
  expect(
    screen.getByRole("button", { name: "Create resource" }),
  ).toHaveProperty("disabled", true);
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
});

it("creates the one Listing when history is empty and keeps the command receipt", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resources/{resourceId}/versions": { items: [] },
    "/api/v1/resources/{resourceId}/listings": { items: [] },
  });
  const pending = deferred<ReturnType<typeof reply>>();
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation((path, options) =>
    path === "/api/v1/resources/{resourceId}/listings"
      ? pending.promise
      : original(path, options),
  );
  w.post.mockResolvedValue(reply(listing));
  render(<Resources {...w.props} id={resource.id} access={administrator} />);
  await screen.findByText("No versions yet.");
  click("Listings");
  expect(
    screen.getByRole("button", { name: "Create listing" }).matches(":disabled"),
  ).toBe(true);
  await act(async () => pending.resolve(reply({ items: [] })));
  await screen.findByText("No listings yet.");
  await waitFor(() =>
    expect(
      screen
        .getByRole("button", { name: "Create listing" })
        .matches(":disabled"),
    ).toBe(false),
  );
  await confirmCommand("Create listing");
  await screen.findByText("Listing created.");
  expect(
    (
      await screen.findByRole("link", { name: "Open existing listing" })
    ).getAttribute("href"),
  ).toBe("/unit/listings/listing-1");
  expect(w.post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/catalog-listings",
    expect.objectContaining({
      body: { resourceId: resource.id, namespaceId: resource.namespaceId },
    }),
  );
  expect(
    screen.getByRole("button", { name: "Create listing" }).matches(":disabled"),
  ).toBe(true);
});

it.each(["history", "metadata-only", "late-hash"] as const)(
  "opens the listing-preparation hash with independent history access: %s",
  async (mode) => {
    const w = screenWorkspace();
    const pending = deferred<ReturnType<typeof reply>>();
    window.history.replaceState(
      {},
      "",
      `/unit/resources/${resource.id}${mode === "late-hash" ? "" : "#listings"}`,
    );
    readReplies(w.get, {
      "/api/v1/resources/{resourceId}": resource,
      "/api/v1/resources/{resourceId}/listings": { items: [] },
      "/api/v1/resources/{resourceId}/versions": { items: [] },
    });
    if (mode === "late-hash") {
      const original = w.get.getMockImplementation()!;
      w.get.mockImplementation((p, o) =>
        p === "/api/v1/resources/{resourceId}"
          ? pending.promise
          : original(p, o),
      );
    }
    const access =
      mode === "metadata-only"
        ? {
            ...administrator,
            permissions: administrator.permissions.filter(
              (p) => p !== "resource.read_history",
            ),
            availablePermissions: administrator.availablePermissions.filter(
              (p) => p !== "resource.read_history",
            ),
          }
        : administrator;
    render(<Resources {...w.props} id={resource.id} access={access} />);
    if (mode === "late-hash") {
      window.history.replaceState(
        {},
        "",
        `/unit/resources/${resource.id}#listings`,
      );
      await act(async () => pending.resolve(reply(resource)));
    }
    await screen.findByRole("heading", { name: resource.canonicalIdentifier });
    if (mode === "metadata-only")
      await waitFor(() =>
        expect(screen.getByLabelText("Listing preparation")).toBe(
          document.activeElement,
        ),
      );
    else if (mode === "history")
      await waitFor(() =>
        expect(
          screen.getByRole("heading", { name: "Listings" }).closest("section"),
        ).toBe(document.activeElement),
      );
    else expect(screen.queryByLabelText("Listing preparation")).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  },
);
it("refuses foreign resource bindings in Listing history", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resources/{resourceId}/versions": { items: [] },
    "/api/v1/resources/{resourceId}/listings": {
      items: [{ ...listing, resourceId: "another" }],
    },
  });
  render(<Resources {...w.props} id={resource.id} access={administrator} />);
  await screen.findByText("No versions yet.");
  click("Listings");
  await screen.findByRole("alert");
  expect(
    screen.queryByRole("link", { name: "Open existing listing" }),
  ).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("finds an exact identity beyond the loaded page and clears continuation on apply, kind change and reset", async () => {
  const { props, get } = screenWorkspace();
  get.mockResolvedValue(reply({ items: [resource], nextCursor: "next" }));
  render(<Resources {...props} />);
  await screen.findByRole("link", { name: resource.canonicalIdentifier });
  click("Next page");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toHaveProperty(
      "params.query.cursor",
      "next",
    ),
  );
  change(
    "Exact canonical identifier",
    "  URN:AIR:EXAMPLE.COM:Finance:MyAgent  ",
  );
  expect(get).toHaveBeenCalledTimes(2);
  click("Find resource");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: {
        query: {
          canonicalIdentifier: "URN:AIR:EXAMPLE.COM:Finance:MyAgent",
          limit: 25,
        },
      },
    }),
  );
  expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty("params.query.cursor");
  click("Next page");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toHaveProperty(
      "params.query.cursor",
      "next",
    ),
  );
  change("Resource kind", "skill");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: {
        query: {
          kind: "skill",
          canonicalIdentifier: "URN:AIR:EXAMPLE.COM:Finance:MyAgent",
        },
      },
    }),
  );
  expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty("params.query.cursor");
  click("Clear identifier");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
      "params.query.canonicalIdentifier",
    ),
  );
  expect(screen.getByLabelText("Exact canonical identifier")).toHaveProperty(
    "value",
    "",
  );
  expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty("params.query.cursor");
  change("Exact canonical identifier", "   ");
  click("Find resource");
  expect(screen.queryByRole("alert")).toBeNull();
  const count = get.mock.calls.length;
  change("Exact canonical identifier", "🙂".repeat(513));
  click("Find resource");
  await screen.findByRole("alert");
  expect(get).toHaveBeenCalledTimes(count);
  click("Clear identifier");
  expect(screen.queryByRole("alert")).toBeNull();
});

it.each(["\ufeffOpaque:e\u0301:🙂\ufeff", "Café:e\u0301:🙂"])(
  "preserves opaque Unicode spelling in exact lookup: %s",
  async (identifier) => {
    const { props, get } = screenWorkspace();
    get.mockResolvedValue(reply({ items: [] }));
    render(<Resources {...props} />);
    await screen.findByText("No resources match these filters.");
    change("Exact canonical identifier", ` \t${identifier}\n `);
    click("Find resource");
    await waitFor(() =>
      expect(get.mock.calls.at(-1)?.[1]).toHaveProperty(
        "params.query.canonicalIdentifier",
        identifier,
      ),
    );
  },
);

it.each([
  ["boundary BOM", "\ufeffOpaque:e\u0301:🙂\ufeff", " \t"],
  ["Unicode whitespace", "Café:e\u0301:🙂", "\u0085"],
])(
  "preserves exact resource identity through creation with %s",
  async (_case, identifier, padding) => {
    const w = screenWorkspace();
    readReplies(w.get, { "/api/v1/namespaces": { items: [namespace] } });
    w.post.mockResolvedValue(
      etagReply({ ...resource, canonicalIdentifier: identifier }),
    );
    render(<Resources {...w.props} id="new" access={administrator} />);
    await screen.findByRole("option", {
      name: `${namespace.displayName} / ${namespace.slug} / ${namespace.id}`,
    });
    change("Namespace", namespace.id);
    change(
      "Canonical resource identifier",
      `${padding}${identifier}${padding}`,
    );
    click("Create resource");
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByLabelText("Command summary").textContent,
    ).toContain(identifier);
    expect(w.post).not.toHaveBeenCalled();
    click("Confirm");
    await screen.findByText("Created resource references");
    expect(w.post).toHaveBeenCalledExactlyOnceWith("/api/v1/resources", {
      body: {
        namespaceId: namespace.id,
        canonicalIdentifier: identifier,
        kind: "agent",
      },
      params: { header: { "Idempotency-Key": expect.any(String) as unknown } },
    });
  },
);

it.each([false, true])(
  "retains a create-only Resource draft and captured command after its final creation hint is removed (uncertain: %s)",
  async (uncertain) => {
    const w = screenWorkspace();
    const permissions = ["resource.create", "namespace.read"] as const;
    const access = {
      ...administrator,
      permissions: [...permissions],
      availablePermissions: [...permissions],
      namespaceGrants: [],
      scopeEnforcement: true,
      scopes: ["catalog.write" as const],
    };
    w.post
      .mockRejectedValueOnce(new APIError(503, "DEPENDENCY_UNAVAILABLE"))
      .mockResolvedValue(etagReply(resource));
    const view = render(<Resources {...w.props} id="new" access={access} />);
    change("Namespace ID", namespace.id);
    change("Canonical resource identifier", resource.canonicalIdentifier);
    click("Create resource");
    const dialog = await screen.findByRole("dialog");
    if (uncertain) {
      click("Confirm");
      await within(dialog).findByRole("button", { name: "Retry same command" });
    }
    view.rerender(
      <Resources
        {...w.props}
        id="new"
        access={{
          ...access,
          permissions: [],
          availablePermissions: [],
        }}
      />,
    );
    expect(screen.getByRole("dialog")).toBe(dialog);
    expect(
      screen.getByLabelText("Canonical resource identifier"),
    ).toHaveProperty("value", resource.canonicalIdentifier);
    expect(
      screen
        .getByLabelText("Canonical resource identifier")
        .closest("fieldset")
        ?.matches(":disabled"),
    ).toBe(true);
    expect(w.get).not.toHaveBeenCalled();
    if (uncertain) {
      click("Retry same command");
      await screen.findByText("Created resource references");
      expect(w.post).toHaveBeenCalledTimes(2);
      expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
    } else {
      expect(
        within(dialog).getByRole("button", { name: "Confirm" }),
      ).toHaveProperty("disabled", true);
      expect(w.post).not.toHaveBeenCalled();
      click("Cancel");
      expect(
        screen.getByLabelText("Canonical resource identifier"),
      ).toHaveProperty("value", resource.canonicalIdentifier);
    }
  },
);

it("keeps initially denied Resource creation free of empty mutation fields and reads", () => {
  const w = screenWorkspace();
  render(
    <Resources
      {...w.props}
      id="new"
      access={{
        ...administrator,
        permissions: [],
        availablePermissions: [],
        namespaceGrants: [],
        scopeEnforcement: true,
        scopes: [],
      }}
    />,
  );
  expect(screen.queryByLabelText("Canonical resource identifier")).toBeNull();
  expect(screen.queryByLabelText("Namespace ID")).toBeNull();
  expect(screen.queryByRole("button", { name: "Create resource" })).toBeNull();
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
});
