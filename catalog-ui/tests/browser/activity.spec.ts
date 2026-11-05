import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, signOut } from "./fixtures";
import type { components } from "../../src/api/schema";
import { doubleText } from "./text-layout";
const enabled = test.extend({ serviceActivity: true });
for (const base of ["root", "subpath"]) {
  enabled(
    `${base}: Activity counts real requests, keeps refresh manual and fits narrow enlarged layouts`,
    async ({ page, catalog }) => {
      const reads: string[] = [];
      page.on("request", (request) => {
        const path = new URL(request.url()).pathname;
        if (path.includes("/_catalog/api/v1/"))
          reads.push(path.split("/api/v1/")[1]!);
      });
      const initial = page.waitForResponse(
        (r) =>
          r.url().endsWith("/api/v1/service-activity") && r.status() === 200,
      );
      await login(page, catalog, "local-admin", "/overview?view=activity");
      const before = (await (
        await initial
      ).json()) as components["schemas"]["ServiceActivity"];
      const total = (data: typeof before) =>
        data.http.families
          .slice(0, 11)
          .reduce((n, row) => n + BigInt(row.completed), 0n);
      const panel = page.getByRole("region", {
        name: "Catalog requests",
        exact: true,
      });
      await expect(panel.locator(".overview-number")).toHaveText(
        total(before).toLocaleString(),
      );
      expect(reads.filter((path) => path.startsWith("overview/"))).toEqual([]);
      expect(reads.filter((path) => path === "service-activity")).toHaveLength(
        1,
      );
      const headingBox = await panel.getByRole("heading").boundingBox();
      const numberBox = await panel.locator(".overview-number").boundingBox();
      expect(headingBox!.y + headingBox!.height).toBeLessThanOrEqual(
        numberBox!.y,
      );
      expect(
        (await page.getByRole("checkbox").boundingBox())!.width,
      ).toBeLessThan(30);
      await expect(
        page.getByRole("checkbox", { name: "Live refresh every 10 seconds" }),
      ).not.toBeChecked();
      await catalog.admin("GET", "/api/v1/namespaces");
      const refreshed = page.waitForResponse(
        (r) =>
          r.url().endsWith("/api/v1/service-activity") && r.status() === 200,
      );
      await page.getByRole("button", { name: "Refresh", exact: true }).click();
      const after = (await (await refreshed).json()) as typeof before;
      expect(after.instanceId).toBe(before.instanceId);
      expect(total(after) - total(before)).toBe(1n);
      await expect(panel.locator(".overview-number")).toHaveText(
        total(after).toLocaleString(),
      );
      for (const group of ["method", "status", "family"])
        await page
          .getByRole("combobox", { name: "Break down requests by" })
          .selectOption(group);
      const scan = await new AxeBuilder({ page }).analyze();
      expect(scan.violations).toEqual([]);
      expect(scan.incomplete).toEqual([]);
      await page.screenshot({
        path: test.info().outputPath(`activity-${base}.png`),
        fullPage: true,
      });
      await page.setViewportSize({ width: 390, height: 1000 });
      await doubleText(page);
      await expect
        .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
        .toBeLessThanOrEqual(390);
      await page.screenshot({
        path: test.info().outputPath(`activity-${base}-narrow.png`),
        fullPage: true,
      });
      if (base === "root") {
        // Use real browser and server time: observer polling must not create Catalog work.
        const polled = page.waitForResponse(
          (response) =>
            response.url().endsWith("/api/v1/service-activity") &&
            response.status() === 200,
        );
        const live = page.getByRole("checkbox", {
          name: "Live refresh every 10 seconds",
        });
        await live.check();
        const observation = (await (await polled).json()) as typeof before;
        expect(total(observation)).toBe(total(after));
        await expect(
          page
            .getByRole("region", { name: "Requests per second", exact: true })
            .locator(".overview-number"),
        ).toHaveText("0");
        await expect(
          page
            .getByRole("region", {
              name: "Mean handler time in interval",
              exact: true,
            })
            .locator(".overview-number"),
        ).toHaveText("Not available");
        await live.uncheck();
      }
      await page
        .getByRole("link", { name: "Administration", exact: true })
        .click();
      await expect(
        page.getByRole("region", { name: "Catalog access", exact: true }),
      ).toBeVisible();
      expect(reads.filter((path) => path === "service-activity")).toHaveLength(
        base === "root" ? 3 : 2,
      );
    },
  );
}
test("disabled Activity and readers make no activity requests", async ({
  page,
  catalog,
}) => {
  const requests: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes("/api/v1/service-activity")) requests.push(r.method());
  });
  await login(page, catalog, "local-admin", "/overview?view=activity");
  await expect(page.getByRole("alert")).toContainText("unavailable");
  expect(requests).toEqual([]);
  await signOut(page);
  await login(page, catalog, "local-reader", "/overview?view=activity");
  await expect(page.getByRole("alert")).toContainText("unavailable");
  expect(requests).toEqual([]);
});

enabled(
  "injected samples: exact large counters, one pending poll, stale values and replica reset",
  async ({ page, catalog, network }) => {
    const { activityFixture } = await import("../unit/activity-fixture");
    await page.clock.install();
    let reads = 0;
    const large = 9007199254740993n;
    let sample = activityFixture(large);
    let release = () => {};
    let hold = false;
    let denied = false;
    await page.route("**/_catalog/api/v1/service-activity", async (route) => {
      reads++;
      if (denied) {
        await route.fulfill({
          status: 403,
          contentType: "application/problem+json",
          json: {
            type: "about:blank",
            title: "Forbidden",
            status: 403,
            code: "FORBIDDEN",
            detail: "Activity access was removed.",
          },
        });
        return;
      }
      const captured = structuredClone(sample);
      if (hold)
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      await route.fulfill({ json: captured });
    });
    await login(page, catalog, "local-admin", "/overview?view=activity");
    const number = (name: string) =>
      page
        .getByRole("region", { name, exact: true })
        .locator(".overview-number");
    await expect(number("Catalog requests")).toHaveText(large.toLocaleString());
    const refresh = page.getByRole("button", { name: "Refresh", exact: true });
    const live = page.getByRole("checkbox", {
      name: "Live refresh every 10 seconds",
    });
    await refresh.focus();
    await page.keyboard.press("Tab");
    await expect(live).toBeFocused();
    await page.emulateMedia({ forcedColors: "active" });
    await expect(number("Catalog requests")).toBeVisible();
    await page.emulateMedia({ forcedColors: "none" });
    sample = activityFixture(large + 2n, 20);
    sample.http.families[0]!.durationSeconds = 0.06;
    hold = true;
    await live.check();
    await page.clock.runFor(10000);
    await expect.poll(() => reads).toBe(2);
    await expect(refresh).toBeDisabled();
    await page.clock.runFor(30000);
    expect(reads).toBe(2);
    hold = false;
    release();
    await expect(number("Catalog requests")).toHaveText(
      (large + 2n).toLocaleString(),
    );
    // The previous observation is stale; a delayed response cannot create a rate.
    await expect(number("Requests per second")).toHaveText("Not available");
    await live.uncheck();
    sample = activityFixture(large + 4n, 30);
    sample.instanceId = "00000000-0000-7000-8000-000000000002";
    sample.http.families[0]!.durationSeconds = 0.08;
    await refresh.click();
    await expect(number("Catalog requests")).toHaveText(
      (large + 4n).toLocaleString(),
    );
    // Counts, duration and elapsed time increase. Only the execution changes.
    await expect(number("Requests per second")).toHaveText("Not available");
    sample = activityFixture(2n, 40);
    sample.instanceId = "00000000-0000-7000-8000-000000000002";
    await refresh.click();
    await expect(number("Catalog requests")).toHaveText("2");
    await expect(number("Requests per second")).toHaveText("Not available");
    await page.clock.runFor(31000);
    await expect(page.getByText(/Out of date/)).toBeVisible();
    expect(reads).toBe(4);
    network.expectFailure("GET", "/_catalog/api/v1/service-activity", 403);
    denied = true;
    await refresh.click();
    await expect(
      page.getByRole("button", { name: "Reload", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("region", { name: "Catalog requests", exact: true }),
    ).toHaveCount(0);
    await page.clock.runFor(30000);
    expect(reads).toBe(5);
    denied = false;
    await page.getByRole("button", { name: "Reload", exact: true }).click();
    await expect(number("Catalog requests")).toHaveText("2");
    expect(reads).toBe(6);
  },
);

enabled(
  "injected samples: hidden tabs, late replies and sign-out clear private activity",
  async ({ page, catalog }) => {
    const { activityFixture } = await import("../unit/activity-fixture");
    await page.clock.install();
    let reads = 0;
    let release = () => {};
    let hold = false;
    await page.route("**/_catalog/api/v1/service-activity", async (route) => {
      reads++;
      if (hold)
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      await route.fulfill({ json: activityFixture(123456789n) });
    });
    await login(page, catalog, "local-admin", "/overview?view=activity");
    const panel = page.getByRole("region", {
      name: "Catalog requests",
      exact: true,
    });
    await expect(panel).toBeVisible();
    const live = page.getByRole("checkbox", {
      name: "Live refresh every 10 seconds",
    });
    await live.check();
    await page.evaluate(() => {
      Object.defineProperty(document, "visibilityState", {
        configurable: true,
        value: "hidden",
      });
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await expect(live).not.toBeChecked();
    await page.clock.runFor(30000);
    expect(reads).toBe(1);
    await page.evaluate(() => {
      Object.defineProperty(document, "visibilityState", {
        configurable: true,
        value: "visible",
      });
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await page.clock.runFor(10000);
    expect(reads).toBe(1);
    hold = true;
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect.poll(() => reads).toBe(2);
    await signOut(page);
    release();
    await expect(panel).toHaveCount(0);
    await expect(
      page.getByText(123456789n.toLocaleString(), { exact: true }),
    ).toHaveCount(0);
  },
);
