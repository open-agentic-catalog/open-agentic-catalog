// Synthetic controls exercise bundled CSS; real command coverage stays in authoring-preparation.
import type { Locator } from "@playwright/test";
import { test, expect, login } from "./fixtures";
import { doubleText } from "./text-layout";
import { visibleFocus } from "./keyboard";

/** Measure painted text against an opaque surface, independently of CSS tokens. */
async function contrast(control: Locator) {
  return control.evaluate((node) => {
    const luminance = (color: string) => {
      const channels = color
        .match(/[\d.]+/g)!
        .slice(0, 3)
        .map(Number);
      return channels.reduce((sum, value, index) => {
        const channel = value / 255;
        return (
          sum +
          (channel <= 0.04045
            ? channel / 12.92
            : ((channel + 0.055) / 1.055) ** 2.4) *
            [0.2126, 0.7152, 0.0722][index]!
        );
      }, 0);
    };
    const style = getComputedStyle(node);
    const foreground = luminance(style.color);
    const background = luminance(style.backgroundColor);
    return (
      (Math.max(foreground, background) + 0.05) /
      (Math.min(foreground, background) + 0.05)
    );
  });
}

for (const base of ["root", "subpath"]) {
  test(`${base}: shared action and status tones stay readable and contextual`, async ({
    page,
    catalog,
  }) => {
    await login(page, catalog, "local-admin", "/namespaces");
    // This owned display-only fixture cannot send commands or interpret domain state.
    await page.locator("main").evaluate((main) => {
      const section = document.createElement("section");
      section.id = "presentation-fixture";
      section.className = "workspace-panel";
      section.innerHTML = `
        <h2>Shared presentation fixture</h2>
        <div class="workspace-toolbar">
          <button type="button" class="primary">Primary action</button>
          <button type="button" class="secondary">Secondary action</button>
          <button type="button" class="warning">Caution action</button>
          <button type="button" class="danger">Destructive action</button>
          <button type="button" class="danger" disabled>Unavailable action</button>
        </div>
        <div class="workspace-toolbar" id="explicit-tones"></div>
        <p class="state-badge published" data-tone="neutral" id="neutral-published">Publication unknown</p>
        <p class="workspace-status" data-state="succeeded" data-tone="neutral" id="neutral-succeeded">Result unavailable</p>
        <div data-tone="neutral">
          <p>Historical assessment</p>
          <span class="workspace-status" data-tone="danger" id="invalid-check">Signature: invalid</span>
        </div>`;
      for (const className of ["state-badge", "workspace-status"]) {
        for (const tone of [
          "neutral",
          "info",
          "success",
          "warning",
          "danger",
        ]) {
          const badge = document.createElement("span");
          badge.className = className;
          badge.dataset.tone = tone;
          badge.textContent = `${className}: ${tone}`;
          section.querySelector("#explicit-tones")!.append(badge);
        }
      }
      main.prepend(section);
    });
    const fixture = page.locator("#presentation-fixture");
    const buttons = fixture.locator("button:not(:disabled)");
    for (const scheme of ["light", "dark", "light"] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await expect(page.locator("html")).toHaveAttribute(
        "data-theme",
        "system",
      );
      for (const button of await buttons.all()) {
        await page.mouse.move(0, 0);
        expect(await contrast(button)).toBeGreaterThanOrEqual(4.5);
        await button.hover();
        expect(await contrast(button)).toBeGreaterThanOrEqual(4.5);
        await button.focus();
        await page.keyboard.press("Tab");
        await page.keyboard.press("Shift+Tab");
        await visibleFocus(button);
        expect(await contrast(button)).toBeGreaterThanOrEqual(4.5);
      }
      for (const badge of await fixture.locator("#explicit-tones > span").all())
        expect(await contrast(badge)).toBeGreaterThanOrEqual(4.5);
      const neutral = fixture
        .locator('.workspace-status[data-tone="neutral"]')
        .first();
      const color = await neutral.evaluate(
        (node) => getComputedStyle(node).color,
      );
      for (const overridden of ["#neutral-published", "#neutral-succeeded"])
        await expect(fixture.locator(overridden)).toHaveCSS("color", color);
      const negative = fixture
        .locator('.workspace-status[data-tone="danger"]')
        .first();
      const failureColor = await negative.evaluate(
        (node) => getComputedStyle(node).color,
      );
      await expect(fixture.locator("#invalid-check")).toHaveCSS(
        "color",
        failureColor,
      );
      expect(failureColor).not.toBe(color);
    }
    await expect(
      fixture.getByRole("button", { name: "Unavailable action" }),
    ).toBeDisabled();
    await page.setViewportSize({ width: 320, height: 900 });
    await doubleText(page);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
    await page.emulateMedia({
      forcedColors: "active",
      reducedMotion: "reduce",
    });
    const danger = fixture.getByRole("button", { name: "Destructive action" });
    await danger.focus();
    await page.keyboard.press("Tab");
    await page.keyboard.press("Shift+Tab");
    await visibleFocus(danger);
    await expect(danger).toHaveText("Destructive action");
    const focus = await danger.evaluate((node) => {
      const style = getComputedStyle(node);
      return {
        width: parseFloat(style.outlineWidth),
        style: style.outlineStyle,
      };
    });
    expect(focus.width).toBeGreaterThanOrEqual(2);
    expect(focus.style).not.toBe("none");
  });
}
