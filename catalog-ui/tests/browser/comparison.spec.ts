import AxeBuilder from "@axe-core/playwright";
import { test, expect, login } from "./fixtures";
import type { Page } from "@playwright/test";
import { tabTo, noBackgroundFocus } from "./keyboard";
import { doubleText } from "./text-layout";
import { paintedContrast } from "./painted-contrast";

async function expectResultSpacing(page: Page) {
  const actions = page.locator(".discovery-actions");
  await expect(actions).toBeVisible();
  for (const layout of ["Table", "Cards"]) {
    await page.getByRole("button", { name: layout, exact: true }).click();
    const results = page.locator(
      layout === "Table" ? ".catalog-table" : ".card-grid",
    );
    await expect(results).toBeVisible();
    const row = (await actions.boundingBox())!;
    const content = (await results.boundingBox())!;
    expect(content.y - row.y - row.height).toBeGreaterThanOrEqual(16);
    expect(content.y - row.y - row.height).toBeLessThanOrEqual(24);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  }
}

for (const base of ["root", "subpath"]) {
  test(`${base}: comparison is bounded, keyboard accessible and makes no detail reads`, async ({
    page,
    catalog,
  }) => {
    let detailReads = 0;
    let searches = 0;
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (/\/api\/v1\/catalog\/resources\//.test(path)) detailReads++;
      if (path.endsWith("/api/v1/catalog/search")) searches++;
    });
    // Select the first result as soon as it enters the document.
    await page.addInitScript(() => {
      const observer = new MutationObserver(() => {
        const control = document.querySelector<HTMLInputElement>(
          ".comparison-choice input",
        );
        if (!control) return;
        observer.disconnect();
        control.click();
      });
      observer.observe(document, { childList: true, subtree: true });
    });
    await login(page, catalog);
    await expect(page.locator("article.resource-card")).toHaveCount(3);
    const selections = page.getByRole("checkbox", { name: /^Compare / });
    await expect(selections.nth(0)).toBeChecked();
    await selections.nth(0).uncheck();
    await expectResultSpacing(page);
    await tabTo(page, selections.nth(0));
    await page.keyboard.press("Space");
    await expectResultSpacing(page);
    await selections.nth(1).check();
    await expectResultSpacing(page);
    const counts = page.getByText("How results are counted", { exact: true });
    await counts.click();
    await expectResultSpacing(page);
    await counts.click();
    await selections.nth(2).click();
    await expect(
      page.getByText("Choose at most two results. Remove a selection first.", {
        exact: true,
      }),
    ).toBeVisible();
    await expect(selections.nth(2)).not.toBeChecked();
    await tabTo(
      page,
      page.getByRole("button", { name: "Compare selected (2/2)", exact: true }),
    );
    await page.keyboard.press("Enter");
    const dialog = page.getByRole("dialog", {
      name: "Compare selected resources",
      exact: true,
    });
    await page.setViewportSize({ width: 1440, height: 1200 });
    await expect(dialog.getByRole("heading", { level: 3 })).toHaveCount(2);
    await page.screenshot({
      path: test.info().outputPath("comparison-light.png"),
    });
    await expect(
      dialog.getByText("Local release: published", { exact: true }),
    ).toHaveCount(2);
    await tabTo(
      page,
      dialog.getByRole("button", { name: "Close comparison", exact: true }),
      "Shift+Tab",
      dialog,
    );
    await page.keyboard.press("Shift+Tab");
    await noBackgroundFocus(dialog);
    await dialog.evaluate((element) => {
      element.scrollTop = 0;
    });
    expect(
      await dialog.evaluate(
        (element) => element.scrollHeight <= element.clientHeight,
      ),
    ).toBe(true);
    // Calibrate the independent painted check before resolving scanner uncertainty.
    const copy = dialog
      .getByRole("button", { name: "Copy Release ID", exact: true })
      .first();
    await paintedContrast(copy);
    const originalStyle = await copy.getAttribute("style");
    try {
      await copy.evaluate((element) => {
        element.style.color = "rgb(255, 255, 255)";
        element.style.backgroundColor = "rgb(255, 255, 255)";
      });
      await expect(paintedContrast(copy)).rejects.toThrow();
    } finally {
      await copy.evaluate((element, style) => {
        if (style === null) element.removeAttribute("style");
        else element.setAttribute("style", style);
      }, originalStyle);
    }
    await paintedContrast(copy);
    await copy.evaluate((element) => {
      const cover = document.createElement("span");
      const rect = element.getBoundingClientRect();
      cover.dataset.contrastControl = "occlusion";
      Object.assign(cover.style, {
        position: "fixed",
        left: `${rect.left}px`,
        top: `${rect.top}px`,
        width: `${rect.width}px`,
        height: `${rect.height}px`,
        background: "white",
        zIndex: "2147483647",
      });
      element.closest("dialog")!.append(cover);
    });
    try {
      await expect(paintedContrast(copy)).rejects.toThrow();
    } finally {
      await dialog
        .locator('[data-contrast-control="occlusion"]')
        .evaluate((element) => element.remove());
    }
    await paintedContrast(copy);
    const report = await new AxeBuilder({ page })
      .include("dialog.result-comparison")
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
      .analyze();
    expect(
      report.violations.map((item) => ({
        id: item.id,
        nodes: item.nodes.map((node) => node.target),
      })),
    ).toEqual([]);
    for (const item of report.incomplete) {
      expect(item.id).toBe("color-contrast");
      for (const node of item.nodes) {
        expect(node.target).toHaveLength(1);
        const selector = node.target[0];
        expect(typeof selector).toBe("string");
        expect(node.failureSummary).toMatch(/overlap/);
        const target = dialog.locator(selector as string);
        await expect(target).toHaveCount(1);
        await expect(target).toBeVisible();
        await paintedContrast(target);
      }
    }
    await page.keyboard.press("Escape");
    await expect(selections.nth(0)).toBeFocused();
    await expect(selections.nth(0)).toBeInViewport();
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: "Dark", exact: true }).check();
    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 390, height: 844 });
    await page
      .getByRole("button", { name: "Compare selected (2/2)", exact: true })
      .click();
    await expect(dialog).toBeVisible();
    expect(
      await dialog.evaluate(
        (element) => element.scrollWidth <= element.clientWidth,
      ),
    ).toBe(true);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: test.info().outputPath("comparison-dark-narrow.png"),
    });
    await dialog.evaluate((element) => {
      element.scrollTop = element.scrollHeight;
    });
    await expect(
      dialog.getByRole("button", { name: "Close comparison", exact: true }),
    ).toBeInViewport();
    await expect(dialog.locator(".rating-summary").last()).toBeInViewport();
    await page.keyboard.press("Escape");
    await expect(selections.nth(0)).toBeFocused();
    await expect(selections.nth(0)).toBeInViewport();
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: "Compact", exact: true }).check();
    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 320, height: 844 });
    await doubleText(page);
    await expectResultSpacing(page);
    expect(detailReads).toBe(0);
    expect(searches).toBe(1);
    await page
      .getByRole("button", { name: "Refresh results", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: /Compare selected/ }),
    ).toHaveCount(0);
    await expect.poll(() => searches).toBe(2);
    await expect(selections.nth(0)).not.toBeChecked();
    await page
      .getByRole("button", { name: /^Filter by namespace / })
      .first()
      .click();
    await expect.poll(() => searches).toBe(3);
    await expect(
      page.getByRole("button", { name: "Refresh results", exact: true }),
    ).toBeEnabled();
    const actions = page.locator(".discovery-actions");
    await expect(actions).toBeEmpty();
    expect(
      await actions.evaluate((element) => ({
        height: element.getBoundingClientRect().height,
        margin: getComputedStyle(element).marginBottom,
      })),
    ).toEqual({ height: 0, margin: "0px" });
    expect(detailReads).toBe(0);
  });
}
