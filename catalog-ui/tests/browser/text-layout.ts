import type { Locator, Page } from "@playwright/test";
import { expect } from "./fixtures";

/** Enlarge existing text once without multiplying inherited ancestor sizes. */
export async function doubleText(page: Page) {
  await page.evaluate(() => {
    const sizes = Array.from(document.querySelectorAll<HTMLElement>("body *"))
      .filter((element) => element instanceof HTMLElement)
      .map((element) => ({
        element,
        size: parseFloat(getComputedStyle(element).fontSize),
      }));
    for (const { element, size } of sizes)
      element.style.fontSize = `${size * 2}px`;
  });
}

/** Measure real line boxes against the available panel, not a capped text parent. */
export async function expectNaturalText(
  text: Locator,
  container: Locator,
  lines: number | "wrapped",
) {
  await expect(text).toBeVisible();
  const available = await container.evaluate((node) => {
    const style = getComputedStyle(node);
    return (
      node.clientWidth -
      parseFloat(style.paddingLeft) -
      parseFloat(style.paddingRight)
    );
  });
  const rendered = await text.evaluate((node) => {
    const range = document.createRange();
    range.selectNodeContents(node);
    return {
      width: node.getBoundingClientRect().width,
      lines: new Set(
        Array.from(range.getClientRects(), (rect) => Math.round(rect.top)),
      ).size,
    };
  });
  expect(Math.abs(rendered.width - available)).toBeLessThanOrEqual(2);
  if (lines === "wrapped") expect(rendered.lines).toBeGreaterThan(1);
  else expect(rendered.lines).toBe(lines);
  await expect(text.locator("br")).toHaveCount(0);
}
