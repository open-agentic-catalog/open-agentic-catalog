import type { Locator } from "@playwright/test";
import { expect } from "./fixtures";

/** Measure the control rectangles, independently of labels and explanatory text. */
export async function expectAlignedOperationControls(controls: Locator[]) {
  const boxes = await Promise.all(
    controls.map(async (control) => {
      await expect(control).toBeVisible();
      const box = await control.boundingBox();
      expect(box).not.toBeNull();
      return box!;
    }),
  );
  const first = boxes[0]!;
  for (const box of boxes.slice(1)) {
    expect(Math.abs(box.y - first.y)).toBeLessThanOrEqual(2);
    expect(Math.abs(box.height - first.height)).toBeLessThanOrEqual(2);
  }
}
