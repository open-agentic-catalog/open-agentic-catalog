import type { Locator } from "@playwright/test";
import type { Kind } from "../../src/api/client";
import { expect } from "./fixtures";

/** Check the painted badge against the existing semantic palette, including CSS order. */
export async function expectKindPalette(
  browse: Locator,
  kinds: readonly Kind[] = ["agent", "mcp_server", "skill"],
) {
  const palette = {
    agent: "--accent-ink",
    mcp_server: "--purple",
    skill: "--teal",
    plugin: "--amber",
  };
  for (const kind of kinds) {
    const token = palette[kind];
    const badge = browse.locator(`.catalog-kind-badge.${kind}`).first();
    const expected = await browse.page().evaluate((palette) => {
      const sample = document.createElement("span");
      sample.hidden = true;
      sample.style.color = `var(${palette})`;
      document.body.append(sample);
      const color = getComputedStyle(sample).color;
      sample.remove();
      return color;
    }, token);
    // Result replacement can detach a sampled node. Assert against the current badge.
    await expect(badge, `${kind} badge uses ${token}`).toHaveCSS(
      "color",
      expected,
    );
    await expect
      .poll(
        () =>
          badge.evaluate((node) =>
            Number.parseFloat(getComputedStyle(node).columnGap),
          ),
        { message: `${kind} badge keeps at least 4px between its contents` },
      )
      .toBeGreaterThanOrEqual(4);
    await expect(badge).toHaveCSS("align-items", "center");
  }
}
