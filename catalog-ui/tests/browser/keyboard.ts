import type { Locator, Page } from "@playwright/test";
import { expect } from "./fixtures";

/** Reach a control through the actual tab order, with a bounded failure. */
export async function tabTo(
  page: Page,
  target: Locator,
  direction: "Tab" | "Shift+Tab" = "Tab",
  within?: Locator,
) {
  await expect(target).toBeVisible();
  for (let step = 0; step < 80; step++) {
    if (await target.evaluate((node) => node === document.activeElement)) break;
    await page.keyboard.press(direction);
    if (within) await noBackgroundFocus(within);
  }
  await expect(
    target,
    "Control is reachable through keyboard navigation",
  ).toBeFocused();
  await visibleFocus(target);
}

/** Native modal navigation may visit browser chrome, never background controls. */
export async function noBackgroundFocus(modal: Locator) {
  expect(
    await modal.evaluate(
      (node) =>
        node.contains(document.activeElement) ||
        (!document.hasFocus() && document.activeElement === document.body),
    ),
    "Modal keyboard navigation excludes background controls",
  ).toBe(true);
}

/** Verify a rendered focus ring rather than only document.activeElement. */
export async function visibleFocus(target: Locator) {
  await expect(target).toBeFocused();
  await expect(target).toBeInViewport();
  expect(
    await target.evaluate((node) => {
      // Native radios paint their label; stretched card links paint the card.
      const painted =
        node.closest(".appearance-choice") ??
        (node.matches(".title-button")
          ? node.closest(".resource-card")
          : null) ??
        node;
      const style = getComputedStyle(painted);
      return (
        node.matches(":focus-visible") &&
        style.outlineStyle !== "none" &&
        parseFloat(style.outlineWidth) > 0 &&
        style.outlineColor !== "rgba(0, 0, 0, 0)"
      );
    }),
    "Keyboard focus has a visible outline",
  ).toBe(true);
}
