import { test, expect, login, signOut } from "./fixtures";

for (const deployment of ["root", "subpath"]) {
  test(`Troubleshooting guidance and deliberate private diagnostic copy at ${deployment}`, async ({
    page,
    catalog,
    context,
    network,
  }, info) => {
    let apiReads = 0;
    let uiReads = 0;
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (path.endsWith("/_catalog/api/v1/service-information")) apiReads++;
      if (path.endsWith("/_build")) uiReads++;
    });
    await page.goto(`${catalog.origin}${catalog.basePath}/troubleshooting`);
    await expect(
      page.getByRole("heading", { name: "Troubleshooting", exact: true }),
    ).toBeVisible();
    await expect(page).toHaveTitle("Troubleshooting | Open Agentic Catalog");
    await expect(
      page.getByRole("button", { name: "Preview diagnostic details" }),
    ).toHaveCount(0);
    expect([apiReads, uiReads]).toEqual([0, 0]);
    await login(page, catalog);
    await page
      .getByRole("link", { name: "Troubleshooting", exact: true })
      .click();
    expect([apiReads, uiReads]).toEqual([0, 0]);
    await expect(
      page.getByRole("link", { name: "My access", exact: true }).last(),
    ).toHaveAttribute("href", `${catalog.basePath}/access`);
    await page
      .getByRole("button", { name: "Preview diagnostic details" })
      .focus();
    await page.keyboard.press("Enter");
    const copy = page.getByRole("button", {
      name: "Copy diagnostic details",
      exact: true,
    });
    await expect(copy).toBeVisible();
    expect([apiReads, uiReads]).toEqual([1, 1]);
    await context.grantPermissions(["clipboard-read", "clipboard-write"], {
      origin: catalog.origin,
    });
    await copy.click();
    await expect(page.getByText("Copied.", { exact: true })).toBeVisible();
    const copied = await page.evaluate(() => navigator.clipboard.readText());
    const report = JSON.parse(copied) as {
      formatVersion: number;
      apiBuild: object;
      uiBuild: object;
    };
    expect(Object.keys(report).sort()).toEqual([
      "apiBuild",
      "formatVersion",
      "uiBuild",
    ]);
    expect(report.formatVersion).toBe(1);
    for (const build of [report.apiBuild, report.uiBuild])
      expect(Object.keys(build).sort()).toEqual([
        "commit",
        "sourceState",
        "version",
      ]);
    expect(copied).not.toMatch(
      /token|cookie|tenant|subject|instanceId|startedAt|uptime|local-reader/i,
    );
    expect([apiReads, uiReads]).toEqual([1, 1]);
    await page.evaluate(() =>
      Object.defineProperty(navigator.clipboard, "writeText", {
        configurable: true,
        value: async () => {
          throw new Error("PRIVATE_CLIPBOARD_FAILURE");
        },
      }),
    );
    await copy.click();
    await expect(page.getByText(/Copy unavailable/)).toBeVisible();
    await expect(page.locator("body")).not.toContainText(
      "PRIVATE_CLIPBOARD_FAILURE",
    );
    expect([apiReads, uiReads]).toEqual([1, 1]);
    await page
      .getByRole("button", { name: "Close diagnostic preview" })
      .click();
    await page.evaluate(() => {
      if (document.activeElement instanceof HTMLElement)
        document.activeElement.blur();
      window.scrollTo(0, 0);
    });
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
    await page.reload();
    await expect(
      page.getByRole("heading", { name: "Troubleshooting", exact: true }),
    ).toBeInViewport();
    await expect(
      page.getByRole("button", { name: "Preview diagnostic details" }),
    ).toBeVisible();
    await page.screenshot({
      path: info.outputPath("troubleshooting.png"),
      fullPage: true,
    });
    network.expectFailure("GET", "/_catalog/api/v1/service-information", 503);
    await page.route(
      "**/_catalog/api/v1/service-information",
      (route) =>
        route.fulfill({
          status: 503,
          headers: { "X-Request-ID": "owned-response-1" },
          json: {
            code: "DEPENDENCY_UNAVAILABLE",
            status: 503,
            title: "Unavailable",
            detail: "PRIVATE_DETAIL",
          },
        }),
      { times: 1 },
    );
    await page
      .getByRole("button", { name: "Preview diagnostic details" })
      .click();
    await expect(copy).toBeVisible();
    await expect(
      page.getByRole("group", { name: "JSON document" }),
    ).toContainText("owned-response-1");
    await expect(
      page.getByRole("group", { name: "JSON document" }),
    ).not.toContainText("PRIVATE_DETAIL");
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await signOut(page);
    await expect(
      page.getByRole("heading", { name: "Troubleshooting", exact: true }),
    ).toBeVisible();
    await expect(copy).toHaveCount(0);
    await expect(page.locator("body")).not.toContainText("owned-response-1");
    await page.reload();
    await expect(
      page.getByRole("heading", { name: "Troubleshooting", exact: true }),
    ).toBeVisible();
  });
}
