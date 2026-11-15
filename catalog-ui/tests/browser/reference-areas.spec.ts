import AxeBuilder from "@axe-core/playwright";
import { test, expect, login } from "./fixtures";
import { doubleText } from "./text-layout";
import { visibleFocus } from "./keyboard";

for (const base of ["root", "subpath"]) {
  test(`${base}: API sections, combined filters and focus remain local at wide and narrow sizes`, async ({
    page,
    catalog,
  }) => {
    await login(page, catalog, "local-reader", "/api-reference");
    await expect(
      page.getByText("199 of 199 operations", { exact: true }),
    ).toBeVisible();
    if (base === "root") {
      await page.setViewportSize({ width: 1440, height: 1400 });
      await page.screenshot({
        path: test.info().outputPath("api-areas-desktop.png"),
        fullPage: false,
      });
      await page.setViewportSize({ width: 1440, height: 900 });
    }
    const calls: string[] = [];
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (path.includes("/_catalog/") || path.endsWith("/_reference"))
        calls.push(path);
    });
    const search = page.getByRole("searchbox", {
      name: "Search API operations",
    });
    const method = page.getByRole("combobox", {
      name: "HTTP method",
      exact: true,
    });
    const filter = page.getByRole("combobox", {
      name: "API area",
      exact: true,
    });
    const index = page.getByRole("navigation", {
      name: "API areas",
      exact: true,
    });
    const identity = page.getByRole("heading", {
      name: "Identity",
      exact: true,
    });
    const section = page.getByRole("region", { name: "Identity", exact: true });
    const disclosure = section.locator("details.api-area-disclosure");
    const summary = disclosure.locator(".api-area-summary");
    await expect(page.locator(".api-overview > details")).toHaveJSProperty(
      "open",
      false,
    );
    await expect(
      page.getByText(/This read-only reference requires UI sign-in/),
    ).toBeVisible();
    const field = await search.boundingBox();
    expect(field!.y).toBeLessThan(650);
    const jump = index.getByRole("link", {
      name: "Identity: 26 operations",
      exact: true,
    });
    await jump.focus();
    await page.keyboard.press("Enter");
    await expect(identity).toBeFocused();
    await expect(jump).toHaveAttribute("aria-current", "location");
    await expect(filter).toHaveValue("");
    expect((await identity.boundingBox())!.y).toBeGreaterThanOrEqual(
      (await page.getByRole("banner").boundingBox())!.height,
    );
    await expect(section.locator(".api-operation")).toHaveCount(26);
    await expect(
      section.getByRole("group", { name: "JSON document" }),
    ).toHaveCount(0);
    const operation = section.locator(".api-operation").first();
    const opener = operation.locator(":scope > summary");
    await opener.focus();
    await page.keyboard.press("Enter");
    await expect(operation).toHaveJSProperty("open", true);
    const hash = await operation
      .getByRole("link", { name: "Link to operation", exact: true })
      .getAttribute("href");
    await section
      .getByRole("button", { name: "Collapse Identity", exact: true })
      .click();
    await expect(disclosure).toHaveJSProperty("open", false);
    await expect(summary).toBeFocused();
    await page.evaluate((value) => {
      location.hash = value!;
    }, hash);
    await expect(disclosure).toHaveJSProperty("open", true);
    await expect(opener).toBeFocused();
    await expect(operation).toHaveJSProperty("open", true);
    await expect(page.locator(".api-operation[open]")).toHaveCount(1);

    await filter.selectOption("Identity");
    await expect(
      page.getByText("49 of 199 operations", { exact: true }),
    ).toBeVisible();
    await expect(section.locator(".api-operation")).toHaveCount(26);
    await method.selectOption("POST");
    await search.fill("private filter stays in this tab");
    await expect(
      page.getByText("0 of 199 operations", { exact: true }),
    ).toBeVisible();
    await expect(page.locator(".api-filter-summary")).toContainText(
      'method POST; API area "Identity"',
    );
    await expect(
      page.getByText(
        "No operations match. Change or clear search, HTTP method or API area.",
      ),
    ).toBeVisible();
    expect(page.url()).not.toContain("private");
    expect(
      await page.evaluate(() => JSON.stringify(history.state)),
    ).not.toContain("private");
    await page
      .getByRole("button", { name: "Clear filters", exact: true })
      .click();
    await expect(search).toBeFocused();
    await expect(method).toHaveValue("");
    await expect(filter).toHaveValue("");
    await expect(search).toHaveValue("");
    await expect(operation).toHaveJSProperty("open", true);
    await expect(
      page.getByText("199 of 199 operations", { exact: true }),
    ).toBeVisible();

    await page.getByLabel("Appearance", { exact: true }).click();
    await page
      .getByRole("radio", {
        name: base === "root" ? "Light" : "Dark",
        exact: true,
      })
      .check();
    await page.getByRole("radio", { name: "Compact", exact: true }).check();
    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 390, height: 844 });
    await doubleText(page);
    const narrowJump = page.getByRole("combobox", {
      name: "Jump to API section",
      exact: true,
    });
    await expect(narrowJump).toBeVisible();
    await expect(jump).toBeHidden();
    const lifecycle = page.getByRole("heading", {
      name: "Catalog lifecycle",
      exact: true,
    });
    const target = await lifecycle.getAttribute("id");
    await narrowJump.selectOption(target!);
    await expect(lifecycle).toBeFocused();
    await page.evaluate(
      () =>
        new Promise<void>((resolve) =>
          requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
        ),
    );
    await expect(narrowJump).toHaveValue(target!);
    expect((await lifecycle.boundingBox())!.y).toBeGreaterThanOrEqual(
      (await page.getByRole("banner").boundingBox())!.height,
    );
    const lifecycleSection = page.getByRole("region", {
      name: "Catalog lifecycle",
      exact: true,
    });
    const last = lifecycleSection.locator(".api-operation > summary").last();
    await last.scrollIntoViewIfNeeded();
    const back = lifecycleSection.getByRole("link", {
      name: "Back to API areas",
      exact: true,
    });
    await expect(back).toBeInViewport();
    expect(
      await back.evaluate(
        (node) => getComputedStyle(node.parentElement!).backgroundColor,
      ),
    ).not.toBe("rgba(0, 0, 0, 0)");
    await back.focus();
    await visibleFocus(back);
    await page.keyboard.press("Enter");
    await expect(index).toBeFocused();
    await expect(index).toBeInViewport();
    await expect(filter).toHaveValue("");
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    const accessibility = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
      .analyze();
    expect(accessibility.violations).toEqual([]);
    expect(accessibility.incomplete).toEqual([]);
    await page.screenshot({
      path: test.info().outputPath(`api-areas-${base}.png`),
      fullPage: false,
    });
    expect(calls).toEqual([]);
  });
}
