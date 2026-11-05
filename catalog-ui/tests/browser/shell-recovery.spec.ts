import { test, expect, login } from "./fixtures";
import { tabTo } from "./keyboard";

test("unavailable login options and failed sign-out recover without pretending the cookie was revoked", async ({
  page,
  catalog,
  network,
  context,
}) => {
  network.expectFailure("GET", "/auth/options", 503);
  await page.route(
    "**/auth/options",
    (route) =>
      route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: '{"code":"UNAVAILABLE","status":503,"title":"Unavailable"}',
      }),
    { times: 1 },
  );
  await page.goto(catalog.origin + "/catalog");
  await expect(page.getByRole("alert")).toHaveText(
    "Sign-in options could not be loaded. Reload sign-in options to try again.",
  );
  await expect(page.locator('[data-login-alias="local-reader"]')).toHaveCount(
    0,
  );
  const reloadOptions = page.getByRole("button", {
    name: "Reload sign-in options",
    exact: true,
  });
  await expect(reloadOptions.locator("svg")).toHaveAttribute(
    "aria-hidden",
    "true",
  );
  await expect(reloadOptions.locator("svg")).toHaveAttribute(
    "focusable",
    "false",
  );
  await reloadOptions.click();
  await page.locator('[data-login-alias="local-reader"]').click();
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  const before = await context.cookies(catalog.origin);
  network.expectFailure("POST", "/auth/logout", 503);
  await page.route(
    "**/auth/logout",
    (route) =>
      route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: '{"code":"UNAVAILABLE","status":503,"title":"Unavailable"}',
      }),
    { times: 1 },
  );
  await tabTo(page, page.getByLabel("Account", { exact: true }));
  await page.keyboard.press("Enter");
  await tabTo(
    page,
    page.getByRole("button", { name: "Sign out", exact: true }),
  );
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alert")).toContainText(
    "session cookie may still be active",
  );
  await expect(page.locator("article.resource-card")).toHaveCount(0);
  expect((await context.cookies(catalog.origin)).length).toBe(before.length);
  const retry = page.getByRole("button", {
    name: "Retry sign-out",
    exact: true,
  });
  await expect(retry).toHaveAccessibleName("Retry sign-out");
  await tabTo(page, retry);
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("button", { name: "Retry sign-out", exact: true }),
  ).toHaveCount(0);
  expect(await context.cookies(catalog.origin)).toEqual([]);
  await page.locator('[data-login-alias="local-reader"]').click();
  await expect(page.locator("article.resource-card")).toHaveCount(3);
});

test("a missing lazy screen offers reload and recovers the actual built module", async ({
  page,
  catalog,
  network,
  browserErrors,
}) => {
  await login(page, catalog, "local-reader");
  // Block the asset for both module preload and the actual dynamic import.
  // A single-use route permits a later request to recover before Reload.
  let blockedRequests = 0;
  await page.route("**/assets/Service-*.js", (route) => {
    blockedRequests++;
    network.expectFailure("GET", new URL(route.request().url()).pathname, 404);
    return route.fulfill({
      status: 404,
      contentType: "text/plain",
      body: "missing test-owned asset",
    });
  });
  await page
    .getByRole("link", { name: "Service information", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Reload Catalog", exact: true }),
  ).toBeVisible();
  const reload = page.getByRole("button", {
    name: "Reload Catalog",
    exact: true,
  });
  await expect(reload.locator("svg")).toHaveAttribute("aria-hidden", "true");
  await expect(reload.locator("svg")).toHaveAttribute("focusable", "false");
  expect(
    network
      .diagnostics()
      .assets.filter(
        (event) =>
          event.asset === "service" &&
          event.event === "response" &&
          event.status === 404,
      ),
  ).toHaveLength(blockedRequests);
  expect(blockedRequests).toBeGreaterThan(0);
  // The expected module exception is accounted for explicitly, not globally
  // ignored. New errors during reload still fail the automatic fixture.
  await expect.poll(() => browserErrors.length).toBe(1);
  expect(browserErrors[0]).toContain(
    "Failed to fetch dynamically imported module",
  );
  browserErrors.splice(0, 1);
  await page.unroute("**/assets/Service-*.js");
  await page
    .getByRole("button", { name: "Reload Catalog", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Service information", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Reload Catalog", exact: true }),
  ).toHaveCount(0);
  await expect
    .poll(
      () =>
        network
          .diagnostics()
          .assets.filter(
            (event) => event.asset === "service" && event.event === "finished",
          ).length,
    )
    .toBe(1);
  expect(
    network
      .diagnostics()
      .assets.filter(
        (event) => event.asset === "service" && event.event === "request",
      ),
  ).toHaveLength(blockedRequests + 1);
  expect(
    network
      .diagnostics()
      .assets.filter(
        (event) =>
          event.asset === "service" &&
          event.event === "response" &&
          event.status === 200,
      ),
  ).toHaveLength(1);
});

test("a namespace form and confirmation can be completed by keyboard on a narrow screen", async ({
  page,
  catalog,
}) => {
  await page.setViewportSize({ width: 320, height: 740 });
  await login(page, catalog, "local-admin", "/namespaces/new");
  const name =
    "A long descriptive namespace label for keyboard-only administration";
  await tabTo(
    page,
    page.getByRole("textbox", { name: "Display name", exact: true }),
  );
  await page.keyboard.type(name);
  await tabTo(page, page.getByRole("textbox", { name: "Slug", exact: true }));
  await page.keyboard.type("keyboard-namespace");
  await tabTo(
    page,
    page.getByRole("button", { name: "Create namespace", exact: true }),
  );
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Tab");
  expect(
    await dialog.evaluate((node) => node.contains(document.activeElement)),
  ).toBe(true);
  await page.keyboard.press("Shift+Tab");
  expect(
    await dialog.evaluate((node) => node.contains(document.activeElement)),
  ).toBe(true);
  await tabTo(
    page,
    dialog.getByRole("button", { name: "Confirm", exact: true }),
    "Tab",
    dialog,
  );
  await page.keyboard.press("Enter");
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("status")).toContainText(
    "Created namespace keyboard-namespace",
  );
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
});

for (const deployment of ["root", "subpath"]) {
  for (const fault of ["unavailable", "invalid JSON", "invalid settings"]) {
    test(`runtime configuration ${fault} fails safely and explicit Reload recovers at ${deployment}`, async ({
      page,
      catalog,
      network,
    }) => {
      const requests: string[] = [];
      page.on("request", (request) => {
        if (/\/(auth|_catalog)\//.test(new URL(request.url()).pathname))
          requests.push(request.url());
      });
      if (fault === "unavailable")
        network.expectFailure("GET", "/runtime.json", 503);
      await page.route(
        "**/runtime.json",
        (route) =>
          route.fulfill({
            status: fault === "unavailable" ? 503 : 200,
            contentType: "application/json",
            body:
              fault === "invalid settings"
                ? '{"basePath":1,"authMode":"invalid"}'
                : "private configuration sentinel",
          }),
        { times: 1 },
      );
      await page.goto(catalog.origin + catalog.basePath + "/catalog");
      await expect(
        page.getByRole("heading", { name: "Catalog unavailable", exact: true }),
      ).toBeVisible();
      await expect(
        page.getByText(
          "The UI settings could not be loaded. Reload to try again.",
          { exact: true },
        ),
      ).toBeVisible();
      await expect(page.locator("body")).not.toContainText(
        "private configuration sentinel",
      );
      expect(requests).toEqual([]);
      const reload = page.getByRole("button", { name: "Reload", exact: true });
      await expect(reload.locator("svg")).toHaveAttribute(
        "aria-hidden",
        "true",
      );
      await expect(reload.locator("svg")).toHaveAttribute("focusable", "false");
      await tabTo(
        page,
        page.getByRole("button", { name: "Reload", exact: true }),
      );
      await page.keyboard.press("Enter");
      await page.locator('[data-login-alias="local-reader"]').click();
      await expect(page.locator("article.resource-card")).toHaveCount(3);
    });
  }
}
