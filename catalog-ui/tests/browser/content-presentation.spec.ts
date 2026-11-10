// Actual local records qualify shared kind markers and labelled actions on direct routes.
import { test, expect, login } from "./fixtures";
import { required } from "../required";
import { doubleText } from "./text-layout";
import { expectKindPalette } from "./kind-palette";

for (const base of ["root", "subpath"]) {
  test(`${base}: content markers preserve native labels, counts and narrow layout`, async ({
    page,
    catalog,
  }) => {
    await login(page, catalog, "local-admin", "/explore");
    const browse = page.getByRole("region", {
      name: "Local browse results",
      exact: true,
    });
    await expect(
      browse.locator(".catalog-kind-badge.agent").first(),
    ).toHaveText("Agent");
    await expect(
      browse.locator(".catalog-kind-badge.mcp_server").first(),
    ).toHaveText("MCP Server");
    await expect(
      browse.locator(".catalog-kind-badge.skill").first(),
    ).toHaveText("Skill");
    await expectKindPalette(browse);
    await expect(
      browse.locator('.catalog-kind-badge svg[aria-hidden="true"]').first(),
    ).toBeVisible();
    await expect(
      page
        .getByRole("combobox", { name: "Kind", exact: true })
        .getByRole("option", { name: "MCP Servers", exact: true }),
    ).toHaveCount(1);
    await page
      .getByRole("button", { name: "View counts", exact: true })
      .click();
    const agent = page.getByRole("button", {
      name: /^Filter by resource kind Agent: [0-9]+$/u,
      exact: true,
    });
    await expect(agent.locator(".catalog-kind-badge.agent")).toHaveText(
      "Agent",
    );
    const count = Number(await agent.locator("strong").innerText());
    expect(count).toBeGreaterThan(0);
    await expect(agent).toHaveAccessibleName(
      `Filter by resource kind Agent: ${count}`,
    );
    await agent.click();
    await expect(
      page.getByRole("status", { name: "Matched releases" }),
    ).toContainText(String(count));
    const item = required(
      catalog.receipt.items.find((entry) => entry.key === "order-agent"),
    );
    await page.goto(
      catalog.origin + catalog.basePath + `/resources/${item.resourceId}`,
    );
    await expect(page.locator(".catalog-kind-badge.agent").first()).toHaveText(
      "Agent",
    );
    await expect(
      page
        .getByRole("link", { name: "Add version", exact: true })
        .locator('svg[aria-hidden="true"]'),
    ).toHaveCount(1);
    await page
      .getByRole("link", { name: "Version 1.0.0", exact: true })
      .click();
    await expect(
      page.locator('.state-badge[data-tone="neutral"]', { hasText: "Sealed" }),
    ).toBeVisible();
    await page.goto(catalog.origin + catalog.basePath + "/namespaces");
    const refresh = page.getByRole("button", { name: "Refresh", exact: true });
    await expect(refresh.locator('svg[aria-hidden="true"]')).toHaveCount(1);
    await expect(
      page
        .getByRole("link", { name: "Create namespace", exact: true })
        .locator('svg[aria-hidden="true"]'),
    ).toHaveCount(1);
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: "Dark", exact: true }).check();
    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 320, height: 900 });
    await doubleText(page);
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
      .toBeLessThanOrEqual(320);
    await refresh.focus();
    await expect(refresh).toBeFocused();
    const response = page.waitForResponse(
      (reply) =>
        reply.request().method() === "GET" &&
        new URL(reply.url()).pathname.endsWith("/api/v1/namespaces"),
    );
    await page.keyboard.press("Enter");
    expect((await response).ok()).toBe(true);
    await expect(page.getByRole("alert")).toHaveCount(0);
  });
}
