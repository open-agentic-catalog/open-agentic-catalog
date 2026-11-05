// Real browser layout and local editing against an invocation-owned Catalog.
import AxeBuilder from "@axe-core/playwright";
import type { Locator } from "@playwright/test";
import { test, expect, login } from "./fixtures";
import { openDisclosure } from "./editor-disclosure";
import { doubleText } from "./text-layout";
import { fillAgentFields } from "./authoring-fields";
import { visibleFocus } from "./keyboard";

async function box(control: Locator) {
  await expect(control).toBeVisible();
  const result = await control.boundingBox();
  if (!result) throw new Error("Expected a visible control");
  return result;
}
for (const base of ["root", "subpath"] as const) {
  test(`${base}: authoring detail metadata and feedback have usable spacing`, async ({
    page,
    catalog,
  }) => {
    await page.setViewportSize({ width: 1440, height: 1200 });
    await login(
      page,
      catalog,
      "local-admin",
      `/resources/${catalog.receipt.items[0]!.resourceId}`,
    );
    for (const item of catalog.receipt.items) {
      await page.goto(
        catalog.origin + catalog.basePath + `/resources/${item.resourceId}`,
      );
      const back = page.getByRole("link", {
        name: "Back to authoring",
        exact: true,
      });
      const panel = page.locator(".resource-identity");
      const b = await box(back);
      const section = await box(page.locator(".workspace-panel").first());
      expect(section.y - b.y - b.height).toBeGreaterThanOrEqual(15);
      const badge = await box(panel.locator(".catalog-kind-badge"));
      const namespace = await box(panel.getByRole("link"));
      expect(
        Math.abs(
          badge.y + badge.height / 2 - namespace.y - namespace.height / 2,
        ),
      ).toBeLessThanOrEqual(1);
    }
    await openDisclosure(page, "Item feedback");
    const submit = page.getByRole("button", {
      name: "Submit feedback",
      exact: true,
    });
    await expect(submit).toBeVisible();
    const filters = page.locator(".feedback-filters");
    expect(
      (await box(filters)).y -
        (await box(submit)).y -
        (await box(submit)).height,
    ).toBeGreaterThanOrEqual(15);
    const category = page.getByRole("combobox", {
      name: "Feedback category",
      exact: true,
    });
    const refresh = page.getByRole("button", {
      name: "Refresh feedback",
      exact: true,
    });
    const c = await box(category),
      r = await box(refresh);
    expect(Math.abs(c.y - r.y)).toBeLessThanOrEqual(1);
    for (const width of [1024, 375, 320]) {
      await page.setViewportSize({ width, height: 1000 });
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(width);
    }
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: "Dark", exact: true }).check();
    await page.getByRole("radio", { name: "Compact", exact: true }).check();
    await page.keyboard.press("Escape");
    await doubleText(page);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    await page.emulateMedia({ forcedColors: "active" });
    expect(
      await page.evaluate(() => matchMedia("(forced-colors: active)").matches),
    ).toBe(true);
    await category.focus();
    await visibleFocus(category);
    expect(
      await category.evaluate(
        (node) => getComputedStyle(node).forcedColorAdjust,
      ),
    ).toBe("auto");
  });

  test(`${base}: guided choices, file stages and narrow preview keep their own values and focus`, async ({
    page,
    catalog,
  }, testInfo) => {
    await page.setViewportSize({ width: 1440, height: 1200 });
    await login(page, catalog, "local-contributor", "/resources/new");
    await openDisclosure(page, "Build an AIR identifier");
    for (const width of [1440, 390, 320]) {
      await page.setViewportSize({ width, height: 1200 });
      const refresh = await box(
        page.getByRole("button", {
          name: "Refresh namespace choices",
          exact: true,
        }),
      );
      const nextField = await box(
        page.locator(".workspace-field").filter({
          has: page.getByLabel("Canonical resource identifier", {
            exact: true,
          }),
        }),
      );
      expect(nextField.y - refresh.y - refresh.height).toBeGreaterThanOrEqual(
        15,
      );
      const use = await box(
        page.getByRole("button", { name: "Use AIR identifier", exact: true }),
      );
      const clear = await box(
        page.getByRole("button", { name: "Clear AIR builder", exact: true }),
      );
      if (Math.abs(clear.y - use.y) < 1)
        expect(clear.x - use.x - use.width).toBeGreaterThanOrEqual(8);
      else expect(clear.y - use.y - use.height).toBeGreaterThanOrEqual(8);
      const kind = await box(
        page.locator(".workspace-field").filter({
          has: page.getByRole("combobox", { name: "Kind", exact: true }),
        }),
      );
      expect(
        kind.y - Math.max(use.y + use.height, clear.y + clear.height),
      ).toBeGreaterThanOrEqual(15);
    }
    await page.getByText("Build an AIR identifier", { exact: true }).click();
    await page.setViewportSize({ width: 1440, height: 1200 });
    await page
      .getByLabel("Namespace", { exact: true })
      .selectOption(catalog.receipt.namespaceId);
    await page
      .getByLabel("Canonical resource identifier", { exact: true })
      .fill(`urn:air:catalog.example.com:layout:${base}`);
    await page
      .getByRole("button", { name: "Next: Content", exact: true })
      .click();
    const exampleSummary = page.getByText("Use a local example", {
      exact: true,
    });
    const beforeBlur = await box(exampleSummary);
    await exampleSummary.click();
    await expect(
      page.getByLabel("Version label", { exact: true }),
    ).toHaveAttribute("aria-invalid", "true");
    await expect(exampleSummary.locator("..")).toHaveAttribute("open", "");
    expect(
      Math.abs((await box(exampleSummary)).y - beforeBlur.y),
    ).toBeLessThanOrEqual(1);
    await exampleSummary.click();
    await fillAgentFields(page);
    await expect(
      page.getByLabel("Agent name", { exact: true }),
    ).toHaveAttribute("required", "");
    await expect(page.locator(".required-legend")).toHaveCount(1);
    await openDisclosure(page, "Interface 1 protocol settings");
    await page
      .getByLabel("Interface 1 protocol binding", { exact: true })
      .selectOption("__custom__");
    const custom = page.getByLabel("Interface 1 custom protocol binding", {
      exact: true,
    });
    await expect(custom).toBeFocused();
    await custom.fill("vendor-binding");
    await page
      .getByLabel("Interface 1 A2A protocol version", { exact: true })
      .selectOption("0.3");
    await page
      .getByRole("button", { name: "Add interface", exact: true })
      .click();
    await page
      .getByLabel("Interface 2 HTTPS URL", { exact: true })
      .fill("https://agents.example.test/second");
    await page
      .getByRole("button", { name: "Move interface 2 up", exact: true })
      .click();
    await expect(
      page.getByLabel("Interface 1 HTTPS URL", { exact: true }),
    ).toBeFocused();
    await expect(
      page.getByLabel("Interface 2 custom protocol binding", { exact: true }),
    ).toHaveValue("vendor-binding");
    await page
      .getByRole("button", { name: "Move interface 2 up", exact: true })
      .click();
    await expect(custom).toHaveValue("vendor-binding");
    await page.setViewportSize({ width: 1440, height: 1800 });
    await page
      .getByRole("group", { name: "Agent interfaces", exact: true })
      .evaluate((node) => node.scrollIntoView({ block: "center" }));
    await page
      .getByRole("group", { name: "Agent interfaces", exact: true })
      .screenshot({
        path: testInfo.outputPath("authoring-protocol-choices.png"),
      });
    await openDisclosure(page, "Use a local example");
    page.once("dialog", (dialog) => dialog.accept());
    await page
      .getByRole("button", { name: "Use example", exact: true })
      .click();
    const preview = page.getByRole("button", {
      name: "Preview and download document",
      exact: true,
    });
    const example = page.getByRole("button", {
      name: "Use example",
      exact: true,
    });
    const e = await box(example),
      p = await box(preview);
    expect(p.y - e.y - e.height).toBeGreaterThanOrEqual(8);
    const importToggle = page.getByText("Import an existing Agent document", {
      exact: true,
    });
    await importToggle.click();
    const file = page.getByLabel("Document file", { exact: true });
    const bytes = Buffer.from(
      '{"name":"Local import","supportedInterfaces":[],"version":"1.0.0"}',
    );
    const selected = {
      name: "local-agent.json",
      mimeType: "application/json",
      buffer: bytes,
    };
    await file.setInputFiles(selected);
    const apply = page.getByRole("button", {
      name: "Use loaded document",
      exact: true,
    });
    await expect(apply).toBeVisible();
    expect(
      await file.evaluate((node: HTMLInputElement) => node.files?.[0]?.name),
    ).toBe("local-agent.json");
    const reselect = page.waitForEvent("filechooser");
    await file.click();
    const chooser = await reselect;
    expect(
      await file.evaluate((node: HTMLInputElement) => node.files?.length),
    ).toBe(0);
    await chooser.setFiles(selected);
    await expect(apply).toBeVisible();
    const cancelled = page.waitForEvent("filechooser");
    await file.click();
    await cancelled;
    await file.dispatchEvent("cancel");
    expect(
      await file.evaluate((node: HTMLInputElement) => node.files?.[0]?.name),
    ).toBe("local-agent.json");
    const discard = page.getByRole("button", {
      name: "Discard selected document",
      exact: true,
    });
    const a = await box(apply),
      d = await box(discard);
    expect(d.x - a.x - a.width).toBeGreaterThanOrEqual(8);
    await page
      .locator(".document-import")
      .screenshot({ path: testInfo.outputPath("authoring-file-stages.png") });
    await discard.click();
    expect(
      await file.evaluate((node: HTMLInputElement) => node.files?.length),
    ).toBe(0);
    await file.setInputFiles(selected);
    await expect(apply).toBeVisible();
    await discard.click();
    await page.setViewportSize({ width: 375, height: 900 });
    const url = page.url();
    await preview.click();
    const panel = page.getByRole("region", {
      name: "Current document preview",
      exact: true,
    });
    await expect(panel).toBeFocused();
    const panelBox = await box(panel);
    expect(panelBox.y).toBeLessThan(300);
    await expect(
      panel.getByRole("heading", { name: "Document preview" }),
    ).toBeVisible();
    await panel.screenshot({
      path: testInfo.outputPath("authoring-mobile-preview.png"),
    });
    await panel
      .getByRole("button", { name: "Back to editor", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Hide document preview", exact: true }),
    ).toBeFocused();
    expect(page.url()).toBe(url);
    await page.setViewportSize({ width: 320, height: 900 });
    await doubleText(page);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
    // Inspect the full page from its reading origin after the focus checks.
    await page.evaluate(() => window.scrollTo(0, 0));
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    await openDisclosure(page, "Advanced representation settings");
    const media = page.getByRole("textbox", {
      name: "Media type",
      exact: true,
    });
    for (const value of ["constructor", "__proto__", "toString"]) {
      await media.fill(value);
      await expect(
        page.getByText(`Format: ${value} (JSON).`, { exact: true }),
      ).toBeVisible();
    }
    await media.fill("application/a2a-agent-card+json");
    await expect(page.getByLabel("Agent name", { exact: true })).toHaveValue(
      "Order exception Agent",
    );
    await openDisclosure(page, "Advanced Agent Card JSON");
    const raw = page.getByRole("textbox", {
      name: "Standards document (JSON)",
      exact: true,
    });
    const card = JSON.parse(await raw.inputValue()) as Record<string, unknown>;
    const interfaces = card.supportedInterfaces as Record<string, unknown>[];
    card.supportedInterfaces = [interfaces[0], { ...interfaces[0] }];
    const unchanged = JSON.stringify(card, null, 2) + "\n";
    await raw.fill(unchanged);
    await page.getByText("Advanced Agent Card JSON", { exact: true }).click();
    await page
      .getByRole("button", { name: "Move interface 1 down", exact: true })
      .click();
    await expect(
      page.getByLabel("Interface 2 HTTPS URL", { exact: true }),
    ).toBeFocused();
    await openDisclosure(page, "Advanced Agent Card JSON");
    await expect(raw).toHaveValue(unchanged);
  });
}
