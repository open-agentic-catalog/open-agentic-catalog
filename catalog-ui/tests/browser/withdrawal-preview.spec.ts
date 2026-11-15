// Controlled read replies qualify rendering/refresh and layout. The packaged
// journey and PostgreSQL tests qualify actual two-host membership and withdrawal.
import { test, expect, login } from "./fixtures";
import AxeBuilder from "@axe-core/playwright";
import { doubleText } from "./text-layout";
import { paintedContrast } from "./painted-contrast";
import type { components } from "../../src/api/schema";

for (const location of ["root", "subpath"]) {
  test(`${location}: withdrawal preview preserves an explicit observation through confirmation and narrow layout`, async ({
    page,
    catalog,
  }, info) => {
    const releaseId = catalog.receipt.items.find(
      (item) => item.releaseId,
    )!.releaseId!;
    const retained = await catalog.admin<
      components["schemas"]["ListingRelease"]
    >("GET", `/api/v1/listing-releases/${releaseId}`);
    const key = (n: number) =>
      `00000000-0000-7000-8000-${String(n).padStart(12, "0")}`;
    let snapshots = [
      {
        snapshotId: key(8001),
        host: "public.example.com",
        audience: "anonymous",
        generation: 3,
        etag: `"${"a".repeat(64)}"`,
      },
      {
        snapshotId: key(8002),
        host: "private.example.com",
        audience: "authenticated",
        generation: 9,
        etag: `"${"b".repeat(64)}"`,
      },
    ];
    const reads: string[] = [];
    const writes: string[] = [];
    await page.route(
      "**/_catalog/api/v1/listing-releases/*/withdrawal-preview?**",
      async (route) => {
        reads.push(
          new URL(route.request().url()).searchParams.get("transition")!,
        );
        await route.fulfill({
          json: {
            releaseId,
            releaseRevision: retained.data.revision,
            transition: "retired",
            observedAt: "2026-11-15T12:00:00Z",
            snapshots,
          },
          headers: { "Cache-Control": "no-store" },
        });
      },
    );
    await login(page, catalog, "local-publisher", `/releases/${releaseId}`);
    page.on("request", (request) => {
      if (
        new URL(request.url()).pathname.includes("/_catalog/api/") &&
        !["GET", "HEAD", "OPTIONS"].includes(request.method())
      )
        writes.push(new URL(request.url()).pathname);
    });
    await expect(
      page.getByRole("combobox", { name: "Disposition", exact: true }),
    ).toHaveValue("deprecate");
    expect(reads).toEqual([]);
    await page
      .getByRole("combobox", { name: "Disposition", exact: true })
      .selectOption("retire");
    const refresh = page.getByRole("button", {
      name: "Preview or refresh affected feeds",
      exact: true,
    });
    const region = page.getByRole("region", {
      name: "Discovery withdrawal preview",
      exact: true,
    });
    await expect(refresh).toBeEnabled();
    expect(reads).toEqual([]);
    await refresh.focus();
    await page.keyboard.press("Enter");
    await expect(
      region.getByRole("link", { name: key(8001), exact: true }),
    ).toHaveAttribute(
      "href",
      `${catalog.basePath}/publication/${key(8001)}?host=public.example.com`,
    );
    await expect(region).toContainText("private.example.com");
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Owned advisory preview");
    await page
      .getByRole("button", { name: "Change release disposition", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByRole("region", {
        name: "Observed discovery feeds",
        exact: true,
      }),
    ).toContainText(key(8001));
    await expect(
      dialog.getByRole("region", {
        name: "Observed discovery feeds",
        exact: true,
      }),
    ).toContainText(key(8002));
    await expect(refresh).toBeDisabled();
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    snapshots = [
      {
        ...snapshots[0]!,
        snapshotId: key(8003),
        generation: 4,
        etag: `"${"c".repeat(64)}"`,
      },
      snapshots[1]!,
    ];
    await refresh.click();
    await expect(region).toContainText("Affected feeds changed.");
    await expect(
      region.getByRole("link", { name: key(8003), exact: true }),
    ).toBeVisible();
    await expect(
      region.getByRole("link", { name: key(8001), exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveValue("Owned advisory preview");
    await page.screenshot({
      path: info.outputPath("withdrawal-preview-desktop.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 320, height: 900 });
    await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
    await doubleText(page);
    // Verify painted header text at the scrolled position. Then use the
    // canonical top-of-page viewport for the complete Axe scan: sticky
    // headers otherwise produce overlap indeterminates against hidden copy.
    for (const selector of [
      ".brand > span:last-child",
      ".brand strong",
      ".environment-label",
    ])
      await paintedContrast(page.locator(selector));
    await page.evaluate(() => scrollTo(0, 0));
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth + 1,
      ),
    ).toBe(true);
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    await page.screenshot({
      path: info.outputPath("withdrawal-preview-narrow.png"),
      fullPage: true,
    });
    expect(reads).toEqual(["retired", "retired"]);
    expect(writes).toEqual([]);
  });
}
