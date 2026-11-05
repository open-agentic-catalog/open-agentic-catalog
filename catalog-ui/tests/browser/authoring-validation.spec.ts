import type { Locator, Page } from "@playwright/test";
import { test, expect, login, type Catalog } from "./fixtures";
import {
  fillAgentFields,
  fillHostedMCPFields,
  fillSkillFields,
} from "./authoring-fields";
import { required } from "../required";
import { doubleText } from "./text-layout";
import AxeBuilder from "@axe-core/playwright";

async function content(page: Page, catalog: Catalog, kind: string) {
  await login(page, catalog, "local-contributor", "/resources/new");
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByRole("combobox", { name: "Kind", exact: true })
    .selectOption(kind);
  await page
    .getByLabel("Canonical resource identifier", { exact: true })
    .fill(`urn:air:demo.catalog.example.com:validation:${kind}`);
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
}

async function highlighted(field: Locator) {
  await expect(field).toHaveAttribute("aria-invalid", "true");
  await expect(field).toHaveAccessibleDescription(/Enter|Use/);
  const colors = await field.evaluate((node) => {
    const sample = document.createElement("span");
    sample.style.color = "var(--danger)";
    document.body.append(sample);
    const danger = getComputedStyle(sample).color;
    sample.remove();
    return { border: getComputedStyle(node).borderTopColor, danger };
  });
  expect(colors.border).toBe(colors.danger);
}

for (const base of ["root", "subpath"] as const) {
  test(`${base}: Basics identifies and focuses the missing canonical identifier`, async ({
    page,
    catalog,
  }) => {
    await login(page, catalog, "local-contributor", "/resources/new");
    await page
      .getByRole("combobox", { name: "Namespace", exact: true })
      .selectOption(catalog.receipt.namespaceId);
    await page
      .getByRole("button", { name: "Next: Content", exact: true })
      .click();
    const identifier = page.getByLabel("Canonical resource identifier", {
      exact: true,
    });
    await expect(identifier).toBeFocused();
    await highlighted(identifier);
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  for (const [kind, name, fill] of [
    ["agent", "Agent name", fillAgentFields],
    ["mcp_server", "MCP server name", fillHostedMCPFields],
    ["skill", "Skill name", fillSkillFields],
  ] as const) {
    test(`${base}: ${kind} Listing cannot hide missing required content`, async ({
      page,
      catalog,
    }, testInfo) => {
      const writes: string[] = [];
      page.on("request", (request) => {
        if (
          request.method() === "POST" &&
          request.url().includes("/_catalog/api/v1/")
        )
          writes.push(request.url());
      });
      await content(page, catalog, kind);
      await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
      await page.getByLabel("Include Listing details", { exact: true }).check();
      await page
        .getByRole("button", { name: "Next: Listing details", exact: true })
        .click();
      const missing = page.getByLabel(name, { exact: true });
      await expect(missing).toBeVisible();
      await expect(missing).toBeFocused();
      await highlighted(missing);
      expect(
        await page.locator('[aria-invalid="true"]').count(),
      ).toBeGreaterThanOrEqual(2);
      expect(writes).toEqual([]);
      await expect(page.getByRole("dialog")).toHaveCount(0);
      if (base === "root" && kind === "agent") {
        await missing.press("Tab");
        await page.keyboard.press("Escape");
        await page.screenshot({
          path: testInfo.outputPath("authoring-required-fields.png"),
        });
      }
      await fill(page);
      await expect(missing).not.toHaveAttribute("aria-invalid");
      await expect(page.locator('[aria-invalid="true"]')).toHaveCount(0);
      await page
        .getByRole("button", { name: "Next: Listing details", exact: true })
        .click();
      await expect(
        page.getByRole("button", { name: "Back: Content", exact: true }),
      ).toBeVisible();
      await page
        .getByRole("button", { name: "Back: Content", exact: true })
        .click();
      await expect(missing).not.toHaveValue("");
      await page
        .getByLabel("Include Listing details", { exact: true })
        .uncheck();
      await page
        .getByRole("button", { name: "Save content draft", exact: true })
        .click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Cancel", exact: true })
        .click();
      expect(writes).toEqual([]);
    });
  }

  test(`${base}: Plugin preparation highlights its missing name before offering files`, async ({
    page,
    catalog,
  }) => {
    await content(page, catalog, "plugin");
    await page
      .getByRole("combobox", { name: "Plugin input", exact: true })
      .selectOption("prepare");
    await page
      .getByRole("button", {
        name: "Preview and download Plugin files",
        exact: true,
      })
      .click();
    const name = page.getByLabel("Plugin name", { exact: true });
    await expect(name).toBeFocused();
    await highlighted(name);
    await expect(page.locator('a[download="plugin.json"]')).toHaveCount(0);
    await name.fill("Bad Name");
    await name.press("Tab");
    await highlighted(name);
    await name.fill("review-tools");
    await expect(name).not.toHaveAttribute("aria-invalid");
    await page
      .getByRole("button", { name: "Hide prepared files", exact: true })
      .click();
    await page
      .getByRole("button", {
        name: "Preview and download Plugin files",
        exact: true,
      })
      .click();
    await expect(
      page
        .getByRole("region", { name: "Prepared Plugin files", exact: true })
        .locator("summary"),
    ).toHaveText(["plugin.json"]);
    await page
      .getByRole("button", { name: "Hide prepared files", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Add MCP server", exact: true })
      .click();
    await page
      .getByRole("button", {
        name: "Preview and download Plugin files",
        exact: true,
      })
      .click();
    const command = page.getByLabel("Command", { exact: true });
    await expect(command).toBeFocused();
    await highlighted(command);
    await command.fill("python -m wrong");
    await command.press("Tab");
    await highlighted(command);
    await command.fill("python");
    await expect(command).not.toHaveAttribute("aria-invalid");
    page.once("dialog", (dialog) => dialog.accept());
    await page
      .getByRole("combobox", { name: "Server 1 transport", exact: true })
      .selectOption("streamable-http");
    const endpoint = page.getByLabel("MCP endpoint URL", { exact: true });
    await endpoint.fill("http://remote.example.test/mcp");
    await endpoint.press("Tab");
    await highlighted(endpoint);
    await endpoint.fill("http://localhost:9876/mcp");
    await expect(endpoint).not.toHaveAttribute("aria-invalid");
    await page
      .getByRole("button", { name: "Hide prepared files", exact: true })
      .click();
    await page
      .getByRole("button", {
        name: "Preview and download Plugin files",
        exact: true,
      })
      .click();
    await expect(
      page
        .getByRole("region", { name: "Prepared Plugin files", exact: true })
        .locator("summary"),
    ).toHaveText(["plugin.json", "mcp.json"]);
  });

  test(`${base}: archive URL and checksum errors mark their fields and recover without a write`, async ({
    page,
    catalog,
  }) => {
    const writes: string[] = [];
    page.on("request", (request) => {
      if (
        request.method() === "POST" &&
        request.url().includes("/_catalog/api/v1/")
      )
        writes.push(request.url());
    });
    await content(page, catalog, "plugin");
    await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
    const url = page.getByLabel("Plugin archive URL", { exact: true });
    await url.fill("http://example.test/plugin.zip");
    await page.getByLabel("Include Listing details", { exact: true }).check();
    const next = page.getByRole("button", {
      name: "Next: Listing details",
      exact: true,
    });
    const save = page.getByRole("button", {
      name: "Save content draft",
      exact: true,
    });
    await next.click();
    await highlighted(url);
    const alert = page.getByRole("alert");
    await alert.getByRole("link").click();
    await expect(url).toBeFocused();
    const input = page.getByRole("combobox", {
      name: "Plugin input",
      exact: true,
    });
    await input.selectOption("prepare");
    await expect(page.getByLabel("Plugin name", { exact: true })).toBeVisible();
    await expect(alert).toHaveCount(0);
    await input.selectOption("archive");
    await expect(url).toHaveValue("http://example.test/plugin.zip");
    await next.click();
    await highlighted(url);
    await url.fill("https://example.test/plugin.zip");
    await expect(url).not.toHaveAttribute("aria-invalid");
    const checksum = page.getByLabel("Expected archive checksum (optional)", {
      exact: true,
    });
    await checksum.fill("sha256:ABC");
    await next.click();
    await highlighted(checksum);
    await checksum.fill(`sha256:${"a".repeat(64)}`);
    await expect(checksum).not.toHaveAttribute("aria-invalid");
    await next.click();
    await page
      .getByRole("button", { name: "Back: Content", exact: true })
      .click();
    await page.getByLabel("Include Listing details", { exact: true }).uncheck();
    await save.click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    expect(writes).toEqual([]);
  });

  test(`${base}: a new Version focuses its missing label and preserves corrected fields at narrow enlarged sizes`, async ({
    page,
    catalog,
  }) => {
    const writes: string[] = [];
    page.on("request", (request) => {
      if (
        request.method() === "POST" &&
        request.url().includes("/_catalog/api/v1/")
      )
        writes.push(request.url());
    });
    const existing = required(
      catalog.receipt.items.find((item) => item.key === "draft-agent"),
    );
    await login(
      page,
      catalog,
      "local-contributor",
      `/resources/${existing.resourceId}`,
    );
    await page.getByRole("link", { name: "Add version", exact: true }).click();
    await fillAgentFields(page);
    const label = page.getByLabel("Version label", { exact: true });
    await label.fill(" ");
    const save = page.getByRole("button", {
      name: "Create version",
      exact: true,
    });
    await save.click();
    await expect(label).toBeFocused();
    await highlighted(label);
    await label.fill("x".repeat(129));
    await save.click();
    await expect(label).toHaveAttribute("aria-invalid", "true");
    await label.fill("2.0.0");
    await expect(label).not.toHaveAttribute("aria-invalid");
    const endpoint = page.getByLabel("Interface 1 HTTPS URL", { exact: true });
    await endpoint.fill("http://example.test/agent");
    await save.click();
    await expect(endpoint).toBeFocused();
    await highlighted(endpoint);
    await page.setViewportSize({ width: 320, height: 1000 });
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: "Dark", exact: true }).check();
    await page.keyboard.press("Escape");
    await doubleText(page);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
    await highlighted(endpoint);
    await page.evaluate(() => window.scrollTo(0, 0));
    const accessibility = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
      .analyze();
    expect(accessibility.violations).toEqual([]);
    expect(accessibility.incomplete).toEqual([]);
    await endpoint.fill("https://example.test/agent");
    await expect(endpoint).not.toHaveAttribute("aria-invalid");
    await save.click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    expect(writes).toEqual([]);
  });
}
