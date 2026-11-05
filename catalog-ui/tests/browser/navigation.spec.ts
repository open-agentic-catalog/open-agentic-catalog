import {
  test,
  expect,
  login,
  confirmCommand,
  dismissNativeReload,
  signOut,
  openAccount,
} from "./fixtures";

for (const deployment of ["root", "subpath"]) {
  test(`unregistered prototype names in browser history use My access at ${deployment}`, async ({
    page,
    catalog,
  }) => {
    await login(page, catalog, "local-reader", "/catalog");
    let directoryReads = 0;
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (
        /\/_catalog\/api\/v1\/(memberships|access\/roles|access\/groups|tenant-settings)(\/|$)/.test(
          path,
        )
      )
        directoryReads++;
    });
    for (const name of [
      "constructor",
      "toString",
      "__proto__",
      "hasOwnProperty",
    ]) {
      const path = `${catalog.basePath}/${name}`;
      await page.evaluate((next) => {
        history.pushState(null, "", next);
        window.dispatchEvent(new PopStateEvent("popstate"));
      }, path);
      await expect(
        page.getByRole("heading", { name: "My access", exact: true }),
      ).toBeVisible();
      await expect(
        page.getByRole("region", { name: "Current administration context" }),
      ).toHaveCount(0);
      expect((await page.request.get(catalog.origin + path)).status()).toBe(
        404,
      );
    }
    expect(directoryReads).toBe(0);
  });
}

test("native navigation preserves dismissed input, discards explicitly and leaves saved work without a prompt", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/namespaces/new");
  let writes = 0;
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/_catalog/api/v1/namespaces"
    )
      writes++;
  });
  await page
    .getByLabel("Display name", { exact: true })
    .fill("Discarded native draft");
  await page.getByLabel("Slug", { exact: true }).fill("native-draft");
  const editorURL = page.url();
  await expect(page).toHaveTitle("Create namespace | Open Agentic Catalog");
  await dismissNativeReload(page);
  await expect(page).toHaveURL(editorURL);
  await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
    "Discarded native draft",
  );
  expect(writes).toBe(0);
  await openAccount(page);
  const cancelAccountLink = page.waitForEvent("dialog").then(async (dialog) => {
    expect(dialog.type()).toBe("beforeunload");
    await dialog.dismiss();
  });
  await page
    .getByRole("navigation", { name: "Account pages", exact: true })
    .getByRole("link", { name: "My access", exact: true })
    .click();
  await cancelAccountLink;
  await expect(page).toHaveURL(editorURL);
  const cancelSignOut = page.waitForEvent("dialog").then(async (dialog) => {
    expect(dialog.type()).toBe("confirm");
    expect(dialog.message()).toContain(
      "unsaved input or an unresolved command",
    );
    await dialog.dismiss();
  });
  await signOut(page);
  await cancelSignOut;
  await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
    "Discarded native draft",
  );
  await expect(page.getByLabel("Account", { exact: true })).toBeVisible();
  expect(writes).toBe(0);
  await page.keyboard.press("Escape");
  const leave = page.waitForEvent("dialog").then(async (dialog) => {
    expect(dialog.type()).toBe("beforeunload");
    await dialog.accept();
  });
  await page
    .getByRole("navigation", { name: "Breadcrumb" })
    .getByRole("link", { name: "Namespaces", exact: true })
    .click();
  await leave;
  await expect(page).toHaveURL(catalog.origin + "/namespaces");
  expect(writes).toBe(0);
  const before = await catalog.admin<{ items: { slug: string }[] }>(
    "GET",
    "/api/v1/namespaces?limit=100",
  );
  expect(before.data.items.some((item) => item.slug === "native-draft")).toBe(
    false,
  );
  await page
    .getByRole("link", { name: "Create namespace", exact: true })
    .click();
  await page
    .getByLabel("Display name", { exact: true })
    .fill("Saved native draft");
  await page.getByLabel("Slug", { exact: true }).fill("native-saved");
  await page
    .getByLabel("Description", { exact: true })
    .fill("Owned browser namespace description");
  await page
    .getByRole("button", { name: "Create namespace", exact: true })
    .click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText(
    "Created namespace native-saved",
  );
  let prompts = 0;
  page.on("dialog", async (dialog) => {
    prompts++;
    await dialog.dismiss();
  });
  await page
    .getByRole("link", { name: "Back to namespaces", exact: true })
    .click();
  await expect(
    page.getByRole("link", { name: "Saved native draft", exact: true }),
  ).toBeVisible();
  expect(prompts).toBe(0);
  expect(writes).toBe(1);
  const after = await catalog.admin<{
    items: { slug: string; description: string }[];
  }>("GET", "/api/v1/namespaces?limit=100");
  expect(
    after.data.items.filter((item) => item.slug === "native-saved"),
  ).toHaveLength(1);
  expect(
    after.data.items.find((item) => item.slug === "native-saved")?.description,
  ).toBe("Owned browser namespace description");
});

test("history under a subpath reauthorizes a private editor after switching tenants", async ({
  page,
  catalog,
  network,
}) => {
  const saved = await catalog.admin<{ id: string; displayName: string }>(
    "POST",
    "/api/v1/namespaces",
    {
      slug: "private-history",
      displayName: "Private namespace retained only in the original tenant",
    },
  );
  const route = `/namespaces/${saved.data.id}`;
  await login(page, catalog, "local-admin", route);
  await expect(page).toHaveTitle("Namespace | Open Agentic Catalog");
  await expect(
    page
      .getByRole("navigation", { name: "Breadcrumb" })
      .getByRole("link", { name: "Namespaces", exact: true }),
  ).toHaveAttribute("href", `${catalog.basePath}/namespaces`);
  await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
    saved.data.displayName,
  );
  await openAccount(page);
  await page.getByRole("link", { name: "My access", exact: true }).click();
  await expect(
    page.getByRole("definition").filter({ hasText: "local-admin" }),
  ).toBeVisible();
  await signOut(page);
  await page.locator('[data-login-alias="other-tenant-admin"]').click();
  await expect(
    page.getByRole("definition").filter({ hasText: "other-admin" }),
  ).toBeVisible();

  const apiPath = `${catalog.basePath}/_catalog/api/v1/namespaces/${saved.data.id}`;
  network.expectFailure(
    "GET",
    `/_catalog/api/v1/namespaces/${saved.data.id}`,
    404,
  );
  const denied = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname === apiPath,
  );
  await page.goBack();
  await expect(page).toHaveURL(catalog.origin + catalog.basePath + route);
  expect((await denied).status()).toBe(404);
  await expect(page.getByRole("alert")).toContainText(
    "This record is unavailable with your current access. Check the reference or return to its list.",
  );
  await expect(page.getByLabel("Display name", { exact: true })).toHaveCount(0);
  await expect(
    page.getByText(saved.data.displayName, { exact: true }),
  ).toHaveCount(0);
  await page.goForward();
  await expect(
    page.getByRole("definition").filter({ hasText: "other-admin" }),
  ).toBeVisible();
  const retained = await catalog.admin(
    "GET",
    `/api/v1/namespaces/${saved.data.id}`,
  );
  expect(retained.data).toEqual(saved.data);
  expect(retained.etag).toBe(saved.etag);
});
