import { expect, type Locator } from "@playwright/test";

// Resolve only the observed native-modal overlap indeterminates using actual
// painted text rectangles and independently calculated opaque-color contrast.
// Every other incomplete rule/failure remains a failure; no axe rule is disabled.
export async function paintedContrast(target: Locator) {
  const measured = await target.evaluate((element) => {
    function luminance(color: string) {
      const match = /^rgb\((\d+), (\d+), (\d+)\)$/.exec(color);
      if (!match) throw new Error("Expected an opaque RGB color");
      const channels = match.slice(1).map((channel) => {
        const value = Number(channel) / 255;
        return value <= 0.04045
          ? value / 12.92
          : ((value + 0.055) / 1.055) ** 2.4;
      });
      return (
        channels[0]! * 0.2126 + channels[1]! * 0.7152 + channels[2]! * 0.0722
      );
    }
    let parent: Element | null = element;
    let background = "";
    while (parent) {
      const style = getComputedStyle(parent);
      if (style.opacity !== "1" || style.backgroundImage !== "none")
        throw new Error("Unsupported contrast composition");
      if (style.backgroundColor !== "rgba(0, 0, 0, 0)") {
        background = style.backgroundColor;
        break;
      }
      parent = parent.parentElement;
    }
    const foreground = luminance(getComputedStyle(element).color);
    const backdrop = luminance(background);
    const ratio =
      (Math.max(foreground, backdrop) + 0.05) /
      (Math.min(foreground, backdrop) + 0.05);
    const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
    let count = 0;
    let unobscured = true;
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      if (!node.textContent?.trim()) continue;
      const range = document.createRange();
      range.selectNodeContents(node);
      for (const rect of range.getClientRects()) {
        if (!rect.width || !rect.height) continue;
        count++;
        if (
          rect.left < 0 ||
          rect.top < 0 ||
          rect.right > innerWidth ||
          rect.bottom > innerHeight
        )
          unobscured = false;
        for (const x of [
          rect.left + 0.5,
          (rect.left + rect.right) / 2,
          rect.right - 0.5,
        ]) {
          const painted = document.elementFromPoint(
            x,
            (rect.top + rect.bottom) / 2,
          );
          if (!painted || !element.contains(painted)) unobscured = false;
        }
      }
    }
    return {
      ratio,
      count,
      unobscured,
      color: getComputedStyle(element).color,
      background,
    };
  });
  expect(measured.count).toBeGreaterThan(0);
  expect(measured.unobscured).toBe(true);
  expect(measured.ratio).toBeGreaterThanOrEqual(4.5);
  return measured;
}
