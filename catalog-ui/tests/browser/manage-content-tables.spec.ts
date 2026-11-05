// Real inventories retain all columns; native keyboard scrolling complements the isolated geometry controls.
import { test, expect, login } from "./fixtures";
import { tabTo } from "./keyboard";
import { doubleText } from "./text-layout";
import { required } from "../required";
import type { components } from "../../src/api/schema";
import type { Locator } from "@playwright/test";

async function box(value: Locator) {
  const result = await value.boundingBox();
  if (!result) throw new Error("Expected a visible table or history link");
  return result;
}

for (const [location, theme] of [
  ["root", "Light"],
  ["subpath", "Dark"],
] as const) {
  test(`${location}: Manage content tables remain reachable at 320px with enlarged text in ${theme}`, async ({
    page,
    catalog,
  }) => {
    const snapshot = await catalog.admin<
      components["schemas"]["PublicationSnapshot"]
    >("POST", "/api/v1/publication/snapshots", { host: "127.0.0.1" });
    await login(page, catalog, "local-admin", "/resources");
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: theme, exact: true }).check();
    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 320, height: 844 });
    const writes: string[] = [];
    page.on("request", (request) => {
      if (
        request.method() !== "GET" &&
        new URL(request.url()).pathname.includes("/_catalog/api/v1/")
      )
        writes.push(request.method());
    });
    const cases = [
      ["/resources", "Resource inventory"],
      ["/namespaces", "Namespaces"],
      ["/publishers", "Publishers"],
      [`/publishers/${catalog.receipt.publisherId}`, "Publisher domains"],
      ["/reviews", "Review submissions"],
      [
        `/publication/${snapshot.data.id}?host=127.0.0.1`,
        "ARD included entries",
      ],
    ] as const;
    for (const [path, label] of cases) {
      await page.goto(catalog.origin + catalog.basePath + path);
      if (label === "ARD included entries")
        await page.getByText(/^Inspect ARD entries/).click();
      const region = page.getByRole("region", { name: label, exact: true });
      await expect(region.locator("table")).toBeVisible();
      await doubleText(page);
      await expect(region).toHaveAttribute("tabindex", "0");
      await expect(
        page.getByText(/More columns are available/).first(),
      ).toBeVisible();
      await tabTo(page, region, "Tab");
      await page.keyboard.press("ArrowRight");
      await expect
        .poll(() => region.evaluate((node) => node.scrollLeft))
        .toBeGreaterThan(0);
      const atRightEdge = () =>
        region.evaluate(
          (node) => node.scrollLeft >= node.scrollWidth - node.clientWidth - 1,
        );
      // Enlarged sortable labels can need more than twenty native key presses.
      // Stop at the real edge and retain a finite bound and every geometry assertion.
      for (let index = 0; index < 80 && !(await atRightEdge()); index++)
        await page.keyboard.press("ArrowRight");
      await expect.poll(atRightEdge).toBe(true);
      const last = region.locator("thead th").last();
      const bounds = await box(region),
        column = await box(last);
      expect(column.x + column.width).toBeLessThanOrEqual(
        bounds.x + bounds.width + 1,
      );
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(320);
    }
    const item = required(
      catalog.receipt.items.find((item) => item.key === "order-agent"),
    );
    const versions = await catalog.admin<{ items: { id: string }[] }>(
      "GET",
      `/api/v1/resources/${item.resourceId}/versions?limit=1`,
    );
    const versionId = required(versions.data.items[0]).id;
    for (const [path, selector] of [
      [`/resources/${item.resourceId}`, ".resource-history a"],
      [
        `/listings/${item.listingId}`,
        `a[href^="${catalog.basePath}/listing-revisions/"]`,
      ],
      [
        `/versions/${versionId}`,
        `a[href^="${catalog.basePath}/version-revisions/"]`,
      ],
    ] as const) {
      await page.goto(catalog.origin + catalog.basePath + path);
      await expect(page.locator(selector).first()).toBeVisible();
      await doubleText(page);
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(320);
      const links = page.locator(selector);
      expect(await links.count()).toBeGreaterThan(0);
      for (const link of await links.all()) {
        const bounds = await box(link);
        expect(bounds.x).toBeGreaterThanOrEqual(0);
        expect(bounds.x + bounds.width).toBeLessThanOrEqual(320);
      }
    }
    expect(writes).toEqual([]);
  });
}
