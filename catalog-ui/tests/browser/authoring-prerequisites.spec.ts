import { test, expect, login, confirmCommand } from "./fixtures";
import { array, object, text } from "./json";

// The seed belongs to the primary tenant; the other administrator starts with
// empty directories. Recovery must preserve the draft and the seeded tenant.
for (const base of ["root", "subpath"] as const) {
  test(`${base}: empty namespace recovery opens an owned editor and preserves the unsent resource`, async ({
    page,
    context,
    catalog,
    sourceCoverage,
  }) => {
    const other = "other-tenant-admin" as const;
    const readOther = async (path: string) =>
      (await catalog.admin<unknown>("GET", path, undefined, undefined, other))
        .data;
    const directories = [
      "/api/v1/namespaces?limit=100",
      "/api/v1/publishers?limit=100",
      "/api/v1/resources?limit=100",
    ];
    const primaryBefore = await Promise.all(
      directories.map(async (path) => (await catalog.admin("GET", path)).data),
    );
    expect(array(object(primaryBefore[0]).items).length).toBeGreaterThan(0);
    expect(array(object(primaryBefore[1]).items).length).toBeGreaterThan(0);
    for (const path of directories)
      expect(array(object(await readOther(path)).items)).toEqual([]);

    const writes: {
      method: string;
      path: string;
      key: string | undefined;
      body: unknown;
    }[] = [];
    context.on("request", (request) => {
      const url = new URL(request.url());
      if (
        url.origin === catalog.origin &&
        url.pathname.includes("/_catalog/api/v1/") &&
        !["GET", "HEAD", "OPTIONS"].includes(request.method())
      )
        writes.push({
          method: request.method(),
          path: url.pathname,
          key: request.headers()["idempotency-key"],
          body: request.postDataJSON(),
        });
    });
    const at = catalog.origin + catalog.basePath;
    const identifier = `urn:air:catalog.example.com:prerequisites:${base}-draft`;
    await login(page, catalog, other, "/resources/new");
    const identity = page.getByRole("textbox", {
      name: "Canonical resource identifier",
      exact: true,
    });
    const kind = page.getByRole("combobox", { name: "Kind", exact: true });
    const namespace = page.getByRole("combobox", {
      name: "Namespace",
      exact: true,
    });
    await identity.fill(identifier);
    await kind.selectOption("skill");
    await expect(namespace.locator("option")).toHaveCount(1);
    await expect(
      page.getByText("A permitted namespace is required.", { exact: false }),
    ).toBeVisible();
    const manage = page.getByRole("link", {
      name: "Manage namespaces (opens in a new tab)",
      exact: true,
    });
    await expect(manage).toHaveAttribute(
      "href",
      catalog.basePath + "/namespaces",
    );
    await expect(manage).toHaveAttribute("target", "_blank");
    await expect(manage).toHaveAttribute("rel", "noopener noreferrer");
    expect(writes).toEqual([]);

    const opened = context.waitForEvent("page");
    await manage.click();
    const editor = await opened;
    await sourceCoverage.ready(editor);
    let createdNamespace: Record<string, unknown>;
    try {
      await expect(editor).toHaveURL(at + "/namespaces");
      expect(await editor.evaluate(() => window.opener === null)).toBe(true);
      await expect(identity).toHaveValue(identifier);
      await expect(kind).toHaveValue("skill");
      await editor
        .getByRole("link", { name: "Create namespace", exact: true })
        .click();
      await editor
        .getByLabel("Display name", { exact: true })
        .fill("Owned prerequisites");
      await editor
        .getByLabel("Slug", { exact: true })
        .fill("owned-prerequisites");
      await editor
        .getByLabel("Description", { exact: true })
        .fill("Created only after explicit confirmation.");
      await editor
        .getByRole("button", { name: "Create namespace", exact: true })
        .click();
      expect(writes).toEqual([]);
      const [reply] = await Promise.all([
        editor.waitForResponse(
          (response) =>
            response.request().method() === "POST" &&
            new URL(response.url()).pathname ===
              catalog.basePath + "/_catalog/api/v1/namespaces",
        ),
        confirmCommand(editor),
      ]);
      expect(reply.status()).toBe(201);
      createdNamespace = object(await reply.json());
      expect(createdNamespace).toMatchObject({
        tenantId: "00000000-0000-7000-8000-000000000002",
        slug: "owned-prerequisites",
        displayName: "Owned prerequisites",
        description: "Created only after explicit confirmation.",
        revision: 1,
      });
      await expect(editor.getByRole("status")).toContainText(
        "Created namespace owned-prerequisites.",
      );
      expect(
        await readOther("/api/v1/namespaces/" + text(createdNamespace.id)),
      ).toEqual(createdNamespace);
    } finally {
      await sourceCoverage.flush(editor);
      await editor.close();
    }
    await expect(page).toHaveURL(at + "/resources/new");
    await expect(identity).toHaveValue(identifier);
    await expect(kind).toHaveValue("skill");
    await page
      .getByRole("button", { name: "Refresh namespace choices", exact: true })
      .click();
    await expect(namespace.locator("option")).toHaveCount(2);
    await expect(namespace).toHaveValue("");
    await namespace.selectOption(text(createdNamespace.id));
    await expect(identity).toHaveValue(identifier);
    await expect(kind).toHaveValue("skill");
    expect(writes).toHaveLength(1);
    await page
      .getByRole("button", { name: "Create resource", exact: true })
      .click();
    expect(writes).toHaveLength(1);
    const [reply] = await Promise.all([
      page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          new URL(response.url()).pathname ===
            catalog.basePath + "/_catalog/api/v1/resources",
      ),
      confirmCommand(page),
    ]);
    expect(reply.status()).toBe(201);
    const resource = object(await reply.json());
    expect(resource).toMatchObject({
      tenantId: "00000000-0000-7000-8000-000000000002",
      namespaceId: text(createdNamespace.id),
      canonicalIdentifier: identifier,
      kind: "skill",
    });
    expect(await readOther("/api/v1/resources/" + text(resource.id))).toEqual(
      resource,
    );
    expect(writes).toEqual([
      {
        method: "POST",
        path: catalog.basePath + "/_catalog/api/v1/namespaces",
        key: expect.any(String),
        body: {
          displayName: "Owned prerequisites",
          description: "Created only after explicit confirmation.",
          slug: "owned-prerequisites",
        },
      },
      {
        method: "POST",
        path: catalog.basePath + "/_catalog/api/v1/resources",
        key: expect.any(String),
        body: {
          namespaceId: text(createdNamespace.id),
          canonicalIdentifier: identifier,
          kind: "skill",
        },
      },
    ]);
    expect(writes[0]!.key).not.toBe(writes[1]!.key);
    await page
      .getByRole("link", { name: "Open created resource", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: identifier, exact: true }),
    ).toBeVisible();
    expect(
      await Promise.all(
        directories.map(
          async (path) => (await catalog.admin("GET", path)).data,
        ),
      ),
    ).toEqual(primaryBefore);
    expect(array(object(await readOther(directories[1]!)).items)).toEqual([]);
  });
}
