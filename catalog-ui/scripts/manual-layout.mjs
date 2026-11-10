import { expect } from "@playwright/test";

async function expectKeyboardScroll(page, diagram) {
  await diagram.focus();
  await expect(diagram).toBeFocused();
  expect(await diagram.evaluate((region) => region.scrollLeft)).toBe(0);
  await page.keyboard.press("ArrowRight");
  await expect
    .poll(() => diagram.evaluate((region) => region.scrollLeft))
    .toBeGreaterThan(0);
  expect(
    await diagram.evaluate(
      (region) => region.ownerDocument.defaultView.scrollX,
    ),
  ).toBe(0);
}

// Exercise the real manual. Keep horizontal scrolling inside its components.
export async function manualLayout(page, baseURL) {
  const original = {
    url: page.url(),
    viewport: page.viewportSize(),
    theme: await page
      .getByRole("combobox", { name: "Select theme" })
      .inputValue(),
    fontSize: await page
      .locator("html")
      .evaluate((root) => root.style.fontSize),
  };
  const results = [];
  const diagramURL = new URL("architecture/overview/", baseURL).href;
  const configurationURL = new URL("reference/configuration/", baseURL).href;
  try {
    await page.goto(diagramURL);
    const diagram = page.getByRole("region", {
      name: "People and systems around the Catalog",
      exact: true,
    });
    await expect(diagram).toHaveAttribute("tabindex", "0");
    for (const theme of ["light", "dark"]) {
      await page
        .getByRole("combobox", { name: "Select theme" })
        .selectOption(theme);
      for (const width of [799, 800, 801, 820, 1024, 1440]) {
        await page.setViewportSize({ width, height: 900 });
        // Start each case in a fresh document so the previous key's scroll
        // animation cannot carry over to this viewport.
        await page.goto(diagramURL);
        await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
        const metrics = await diagram.evaluate((region) => {
          const style =
            region.ownerDocument.defaultView.getComputedStyle(region);
          return {
            available:
              region.clientWidth -
              parseFloat(style.paddingLeft) -
              parseFloat(style.paddingRight),
            svgWidth: region.querySelector("svg").getBoundingClientRect().width,
            documentWidth: region.ownerDocument.documentElement.scrollWidth,
            scrolls: region.scrollWidth > region.clientWidth,
          };
        });
        expect(metrics.svgWidth).toBeGreaterThanOrEqual(560);
        expect(
          Math.abs(metrics.svgWidth - Math.max(560, metrics.available)),
        ).toBeLessThan(1);
        expect(metrics.documentWidth).toBeLessThanOrEqual(width);
        expect(metrics.scrolls).toBe(metrics.available < 560);
        if (metrics.scrolls) {
          await expectKeyboardScroll(page, diagram);
        }
        results.push({ page: "diagram", width, theme, ...metrics });
      }
    }

    const note = page.locator(".starlight-aside").filter({
      has: page.locator(".starlight-aside__title", {
        hasText: "Current implementation",
      }),
    });
    const title = note.locator(".starlight-aside__title");
    for (const theme of ["light", "dark"]) {
      await page.goto(configurationURL);
      await expect(title).toHaveText("Current implementation");
      await page.locator("html").evaluate((root) => {
        root.style.fontSize = "";
      });
      await page.setViewportSize({ width: 1440, height: 900 });
      await page
        .getByRole("combobox", { name: "Select theme" })
        .selectOption(theme);
      await page.setViewportSize({ width: 375, height: 900 });
      await page.locator("html").evaluate((root) => {
        root.style.fontSize = "200%";
      });
      await expect(title).toBeVisible();
      await expect(note.locator(".starlight-aside__icon")).toBeVisible();
      await expect(note).toContainText("No policy is selected by default.");
      const metrics = await title.evaluate((element) => {
        const aside = element
          .closest(".starlight-aside")
          .getBoundingClientRect();
        const title = element.getBoundingClientRect();
        const icon = element
          .querySelector(".starlight-aside__icon")
          .getBoundingClientRect();
        return {
          documentWidth: element.ownerDocument.documentElement.scrollWidth,
          titleFits:
            element.scrollWidth <= element.clientWidth &&
            title.left >= aside.left &&
            title.right <= aside.right,
          iconFits: icon.left >= aside.left && icon.right <= aside.right,
          overflow:
            element.ownerDocument.defaultView.getComputedStyle(element)
              .overflowX,
        };
      });
      expect(metrics.documentWidth).toBeLessThanOrEqual(375);
      expect(metrics.titleFits).toBe(true);
      expect(metrics.iconFits).toBe(true);
      expect(metrics.overflow).toBe("visible");
      // The configuration reference owns the callout; the overview owns the figure.
      await page.goto(diagramURL);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      await page.locator("html").evaluate((root) => {
        root.style.fontSize = "200%";
      });
      await expect(diagram).toHaveAttribute("tabindex", "0");
      await expectKeyboardScroll(page, diagram);
      results.push({ page: "callout", width: 375, theme, ...metrics });
    }
  } finally {
    await page.setViewportSize(original.viewport);
    await page.goto(original.url);
    await page
      .getByRole("combobox", { name: "Select theme" })
      .selectOption(original.theme);
    await page.locator("html").evaluate((root, size) => {
      root.style.fontSize = size;
    }, original.fontSize);
  }
  return results;
}
