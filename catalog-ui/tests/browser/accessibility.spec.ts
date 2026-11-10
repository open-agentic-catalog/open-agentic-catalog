import { openDisclosure } from "./editor-disclosure";
// Rendered accessibility complements existing keyboard journeys, not WCAG certification.
import AxeBuilder from "@axe-core/playwright";
import type { Page } from "@playwright/test";
import { test, expect, login, openAccount } from "./fixtures";
import { noBackgroundFocus, tabTo, visibleFocus } from "./keyboard";
import { doubleText, expectNaturalText } from "./text-layout";
import { paintedContrast } from "./painted-contrast";

/** Scan the settled page or selected exposed surface without disabling rules. */
async function accessible(
  page: Page,
  scope?: string,
  previewOverlapTarget?: ".preview-actions > .muted",
) {
  // Theme changes animate button colors; scan the final state, not a transition.
  await page.evaluate(async () => {
    await Promise.all(
      document
        .getAnimations()
        .filter(
          (animation) =>
            animation.effect?.getComputedTiming().iterations !== Infinity,
        )
        .map((animation) => animation.finished.catch(() => {})),
    );
  });
  // Keyboard travel can leave partially clipped rows or text beneath the
  // sticky header. Scan a known viewport after the real focus/scroll assertions.
  // A modal hides the backing page. Preserve its scroll position so closing
  // the modal returns to an opener that remains visible.
  // Keep all navigation and contrast rules in the scan.
  await page.evaluate(() => {
    if (!document.querySelector("dialog:modal")) window.scrollTo(0, 0);
  });
  await page.locator("#workspace-navigation").evaluateAll((menus) => {
    for (const menu of menus) menu.scrollTop = 0;
  });
  const builder = new AxeBuilder({ page });
  if (scope) builder.include(scope);
  const report = await builder
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  await reviewAccessibility(page, report, previewOverlapTarget);
  return report;
}

async function reviewAccessibility(
  page: Page,
  report: Awaited<ReturnType<AxeBuilder["analyze"]>>,
  previewOverlapTarget?: ".preview-actions > .muted",
) {
  // Keep failures actionable without retaining a raw DOM/accessibility report.
  const summarize = (items: typeof report.violations) =>
    items.map((item) => ({
      rule: item.id,
      nodes: item.nodes.map((node) => ({
        target: node.target,
        reason: node.failureSummary,
      })),
    }));
  expect(summarize(report.violations), "Accessibility violations").toEqual([]);
  // axe 4.13 asks for review whenever aria-controls accompanies aria-haspopup,
  // even when the target exists. Verify this exact relationship ourselves.
  const filterPopup = report.incomplete.some(
    (item) =>
      item.id === "aria-valid-attr-value" &&
      item.nodes.some(
        (node) =>
          node.target.length === 1 &&
          node.target[0] === ".filter-trigger" &&
          node.failureSummary?.includes('aria-controls="search-filters"'),
      ),
  );
  if (filterPopup) {
    const trigger = page.getByRole("button", { name: "Filters", exact: true });
    await expect(trigger).toHaveAttribute("aria-controls", "search-filters");
    await expect(trigger).toHaveAttribute("aria-haspopup", "dialog");
    await expect(trigger).toHaveAttribute("aria-expanded", "true");
    await expect(page.locator("#search-filters")).toHaveCount(1);
    await expect(
      page.getByRole("dialog", { name: "Filters", exact: true }),
    ).toHaveAttribute("id", "search-filters");
    await expect(
      page.getByRole("dialog", { name: "Filters", exact: true }),
    ).toBeVisible();
  }
  const unresolved = [];
  for (const item of report.incomplete) {
    for (const node of item.nodes) {
      if (
        filterPopup &&
        item.id === "aria-valid-attr-value" &&
        node.target.length === 1 &&
        node.target[0] === ".filter-trigger" &&
        node.failureSummary?.includes('aria-controls="search-filters"')
      )
        continue;
      // Resolve only this reproduced native-modal overlap. The painted check
      // rejects covered/clipped text, low contrast and unsupported composition.
      if (
        previewOverlapTarget &&
        item.id === "color-contrast" &&
        node.target.length === 1 &&
        node.target[0] === previewOverlapTarget &&
        node.failureSummary?.includes("overlapped by another element")
      ) {
        const target = page
          .locator("dialog.detail-dialog:modal")
          .locator(previewOverlapTarget);
        await expect(target).toHaveCount(1);
        await expect(target).toBeVisible();
        const measured = await paintedContrast(target);
        await test.info().attach("preview-painted-contrast", {
          body: JSON.stringify({ target: previewOverlapTarget, ...measured }),
          contentType: "application/json",
        });
        continue;
      }
      unresolved.push({ item, node });
    }
  }
  expect(
    unresolved.map(({ item, node }) => ({
      rule: item.id,
      target: node.target,
      reason: node.failureSummary,
      relatedTargets: [...node.any, ...node.all, ...node.none].flatMap(
        (check) => (check.relatedNodes ?? []).map((related) => related.target),
      ),
    })),
    "Accessibility checks needing manual review",
  ).toEqual([]);
}

test("accessibility review rejects unreviewed results and failed independent checks", async ({
  page,
  catalog,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page, catalog);
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  // The native modal makes the real background triggers inert. Add one owned
  // visible control to reach Axe's popup-review branch without changing the UI.
  const originals = await page.locator(".filter-trigger").elementHandles();
  const classes = await Promise.all(
    originals.map((element) => element.getAttribute("class")),
  );
  const originalTrigger = page.getByRole("button", {
    name: "Filters",
    exact: true,
  });
  await originalTrigger.evaluate((element) =>
    element.setAttribute("aria-label", "Original filter control"),
  );
  try {
    for (const element of originals)
      await element.evaluate((node) => {
        if (!(node instanceof Element))
          throw new Error("Expected a filter element");
        node.classList.remove("filter-trigger");
      });
    await page.locator("#search-filters").evaluate((dialog) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "filter-trigger";
      button.dataset.axeCalibration = "filters";
      button.textContent = "Filters";
      button.setAttribute("aria-controls", "search-filters");
      button.setAttribute("aria-haspopup", "dialog");
      button.setAttribute("aria-expanded", "true");
      dialog.append(button);
    });
    const report = await accessible(page);
    // Reach the real pinned-Axe review branch before injecting report controls.
    const item = report.incomplete.find(
      (item) => item.id === "aria-valid-attr-value",
    );
    expect(item).toBeDefined();
    const node = item!.nodes.find(
      (node) =>
        node.target.length === 1 && node.target[0] === ".filter-trigger",
    );
    expect(node).toBeDefined();
    expect(node!.failureSummary).toContain('aria-controls="search-filters"');
    const control = { ...item!, nodes: [node!] };
    // These reports deliberately alter native evidence to test review rejection.
    await expect(
      reviewAccessibility(page, {
        ...report,
        incomplete: [{ ...control, id: "unreviewed-rule" }],
      }),
    ).rejects.toThrow(/Accessibility checks needing manual review/);
    await expect(
      reviewAccessibility(page, {
        ...report,
        incomplete: [
          { ...control, nodes: [{ ...node!, target: [".unreviewed-target"] }] },
        ],
      }),
    ).rejects.toThrow(/Accessibility checks needing manual review/);
    await expect(
      reviewAccessibility(page, {
        ...report,
        violations: [control],
      }),
    ).rejects.toThrow(/Accessibility violations/);
    const trigger = page.getByRole("button", { name: "Filters", exact: true });
    await trigger.evaluate((element) =>
      element.setAttribute("aria-expanded", "false"),
    );
    try {
      await expect(reviewAccessibility(page, report)).rejects.toThrow(
        /aria-expanded/,
      );
    } finally {
      await trigger.evaluate((element) =>
        element.setAttribute("aria-expanded", "true"),
      );
    }
    await reviewAccessibility(page, report);
  } finally {
    await page
      .locator('[data-axe-calibration="filters"]')
      .evaluateAll((nodes) => nodes.forEach((node) => node.remove()));
    for (const [index, element] of originals.entries())
      await element.evaluate((node, value) => {
        if (!(node instanceof Element))
          throw new Error("Expected a filter element");
        node.setAttribute("class", value!);
      }, classes[index]!);
    await page
      .getByRole("button", { name: "Original filter control", exact: true })
      .evaluate((element) => element.setAttribute("aria-label", "Filters"));
  }
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
});

test("page descriptions use available desktop space and wrap at narrow enlarged sizes", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/explore");
  const descriptions = [
    [
      "/explore",
      "Explore local catalog",
      "Browse locally governed releases or see counts by kind, tag, category and other details. External federation results are not included in these counts.",
    ],
    [
      "/members",
      "Members and access",
      "Manage Catalog membership and access for an existing issuer identity. This does not create an identity-provider account or issue tokens.",
    ],
    [
      "/feedback",
      "Feedback inbox",
      "Only the submitter and authorized tenant moderators can read feedback. Do not include credentials, personal information or sensitive logs.",
    ],
    [
      "/audit",
      "Audit investigation",
      "Find out what changed, when it happened and who was responsible. Choose a time range and search; all other filters are optional. Viewing or exporting this history is also recorded.",
    ],
  ] as const;
  for (const [path, title, description] of descriptions) {
    await page.goto(catalog.origin + path);
    await expect(
      page.getByRole("heading", { name: title, exact: true }),
    ).toBeVisible();
    const heading = page.locator(".page-heading");
    await expect(heading.locator("p")).toHaveText(description);
    await expectNaturalText(heading.locator("p"), heading, 1);
  }
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.getByRole("radio", { name: "Compact", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 768, height: 900 });
  await expectNaturalText(
    page.locator(".page-heading p"),
    page.locator(".page-heading"),
    "wrapped",
  );
  await page.setViewportSize({ width: 320, height: 900 });
  await doubleText(page);
  await expectNaturalText(
    page.locator(".page-heading p"),
    page.locator(".page-heading"),
    "wrapped",
  );
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
  const search = page.getByRole("button", {
    name: "Search audit events",
    exact: true,
  });
  await search.focus();
  await expect(search).toBeFocused();
  await expect(search).toBeInViewport();
  const hiddenButtons = page.locator(
    "details:not([open]) > :not(summary) button",
  );
  expect(await hiddenButtons.count()).toBeGreaterThan(0);
  expect(
    await hiddenButtons.evaluateAll((buttons) =>
      buttons.every(
        (button) =>
          getComputedStyle(button).transitionDuration === "0s" &&
          button.getAnimations().length === 0,
      ),
    ),
  ).toBe(true);
  await accessible(page);
});

for (const deployment of ["root", "subpath"]) {
  test(`accessibility: public Help and quick navigation at ${deployment}`, async ({
    page,
    catalog,
  }) => {
    await page.goto(catalog.origin + catalog.basePath + "/about");
    await expect(
      page.getByRole("heading", { name: "About", exact: true }),
    ).toBeVisible();
    await accessible(page);
    await page.emulateMedia({ colorScheme: "dark" });
    await page.setViewportSize({ width: 320, height: 740 });
    await doubleText(page);
    await accessible(page);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
    await page
      .getByRole("link", { name: "Getting started", exact: true })
      .first()
      .click();
    await expect(
      page.getByRole("heading", { name: "Getting started", exact: true }),
    ).toBeVisible();
    await doubleText(page);
    await accessible(page);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
    await page
      .getByRole("link", { name: "Troubleshooting", exact: true })
      .first()
      .click();
    await expect(
      page.getByRole("heading", { name: "Troubleshooting", exact: true }),
    ).toBeVisible();
    await doubleText(page);
    await accessible(page);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
    await login(page, catalog, "local-admin");
    await page.goto(catalog.origin + catalog.basePath + "/about");
    await expect(
      page.getByText("UI host version", { exact: true }),
    ).toBeVisible();
    await doubleText(page);
    await accessible(page);
    await page.keyboard.press("Control+k");
    const chooser = page.getByRole("dialog", { name: "Quick navigation" });
    const input = chooser.getByRole("textbox", { name: "Find a page" });
    await expect(input).toBeFocused();
    expect(
      await chooser.evaluate((node) => node.scrollWidth - node.clientWidth),
      "Quick navigation fits its available width",
    ).toBeLessThanOrEqual(0);
    await accessible(page);
    await tabTo(
      page,
      chooser.getByRole("button", { name: "Close quick navigation" }),
      "Tab",
      chooser,
    );
    await page.keyboard.press("Tab");
    await noBackgroundFocus(chooser);
    await tabTo(page, input, "Tab", chooser);
    await input.fill("no matching page");
    await expect(chooser.getByRole("status")).toHaveText("No matching pages.");
    // Scan the top of the scrolling surface after actual keyboard traversal.
    await chooser.evaluate((node) => {
      node.scrollTop = 0;
    });
    await accessible(page);
    await page.keyboard.press("Escape");
    await visibleFocus(
      page.getByRole("button", { name: "Quick navigation", exact: true }),
    );
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
    await page.goto(catalog.origin + catalog.basePath + "/troubleshooting");
    await page
      .getByRole("button", { name: "Preview diagnostic details", exact: true })
      .click();
    await expect(
      page.getByRole("button", {
        name: "Copy diagnostic details",
        exact: true,
      }),
    ).toBeVisible();
    await doubleText(page);
    await accessible(page);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
  });
}

test("accessibility: login, results, preview and dark narrow filters", async ({
  page,
  catalog,
}) => {
  await page.goto(catalog.origin);
  await expect(page.locator('[data-login-alias="local-reader"]')).toBeVisible();
  await accessible(page);
  await login(page, catalog);
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  await accessible(page);
  const search = page.getByRole("searchbox", {
    name: "Search Catalog",
    exact: true,
  });
  await tabTo(page, search);
  await page.keyboard.type("orders");
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("button", {
      name: "Remove search text orders",
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  const trigger = page.getByRole("button", { name: "Filters", exact: true });
  await tabTo(page, trigger);
  await page.keyboard.press("Enter");
  const panel = page.getByRole("region", { name: "Filters", exact: true });
  await expect(panel).toBeVisible();
  await expect(panel).not.toHaveAttribute("aria-modal", "true");
  await accessible(page);
  await tabTo(
    page,
    page.getByRole("button", { name: "Apply filters", exact: true }),
  );
  await page.keyboard.press("Tab");
  await expect(panel).toHaveCount(0);
  expect(
    await trigger.evaluate((node) => node === document.activeElement),
  ).toBe(false);
  await tabTo(page, trigger, "Shift+Tab");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Escape");
  await visibleFocus(trigger);
  const table = page.getByRole("button", { name: "Table", exact: true });
  await tabTo(page, table);
  await page.keyboard.press("Enter");
  const previewOpener = page.locator(".table-scroll tbody tr button").first();
  await tabTo(page, previewOpener);
  await page.keyboard.press("Enter");
  const preview = page.getByRole("dialog");
  await expect(preview).toBeVisible();
  expect(await preview.evaluate((node) => node.matches(":modal"))).toBe(true);
  const closePreview = page.getByRole("button", {
    name: "Close preview",
    exact: true,
  });
  await tabTo(page, closePreview, "Tab", preview);
  await page.keyboard.press("Tab");
  expect(
    await preview.evaluate((node) => node.contains(document.activeElement)),
  ).toBe(true);
  await page.keyboard.press("Shift+Tab");
  await visibleFocus(closePreview);
  await accessible(page);
  // Exercise the preview edge; Chromium may visit its own chrome during wrap.
  await page.keyboard.press("Shift+Tab");
  await noBackgroundFocus(preview);
  await tabTo(page, closePreview, "Tab", preview);
  await page.keyboard.press("Escape");
  await visibleFocus(previewOpener);
  await tabTo(
    page,
    page.getByRole("button", { name: "Cards", exact: true }),
    "Shift+Tab",
  );
  await page.keyboard.press("Enter");
  await tabTo(page, page.getByLabel("Appearance", { exact: true }));
  await page.keyboard.press("Enter");
  await tabTo(page, page.getByRole("radio", { name: "System", exact: true }));
  await page.keyboard.press("ArrowRight");
  await page.keyboard.press("ArrowRight");
  await expect(
    page.getByRole("radio", { name: "Dark", exact: true }),
  ).toBeChecked();
  await visibleFocus(page.getByRole("radio", { name: "Dark", exact: true }));
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 844 });
  await tabTo(page, trigger);
  await page.keyboard.press("Enter");
  const drawer = page.getByRole("dialog", { name: "Filters", exact: true });
  await expect(drawer).toHaveAttribute("aria-modal", "true");
  const closeFilters = page.getByRole("button", {
    name: "Close filters",
    exact: true,
  });
  await tabTo(page, closeFilters, "Tab", drawer);
  await page.keyboard.press("Tab");
  await expect(
    page.getByRole("button", { name: "Refresh applied results", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(
    page.getByRole("button", { name: "Add category", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(
    page.getByRole("button", { name: "Refresh applied results", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await visibleFocus(closeFilters);
  await expect(
    page.getByRole("button", { name: "Apply filters", exact: true }),
  ).toBeVisible();
  await accessible(page);
  await tabTo(
    page,
    page.getByRole("checkbox", { name: "Agents", exact: true }),
    "Tab",
    drawer,
  );
  await page.keyboard.press("Space");
  await tabTo(
    page,
    page.getByRole("button", { name: "Apply filters", exact: true }),
    "Tab",
    drawer,
  );
  await page.keyboard.press("Tab");
  await noBackgroundFocus(drawer);
  await tabTo(page, closeFilters, "Tab", drawer);
  await page.keyboard.press("Shift+Tab");
  await noBackgroundFocus(drawer);
  await tabTo(
    page,
    page.getByRole("button", { name: "Apply filters", exact: true }),
    "Shift+Tab",
    drawer,
  );
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("dialog", { name: "Filters", exact: true }),
  ).not.toBeVisible();
  await visibleFocus(trigger);
  await expect(page.locator("article.resource-card")).toHaveCount(1);
  await expect(
    page.getByRole("group", { name: "Applied search", exact: true }),
  ).toBeVisible();
  await accessible(page);
  const darkOpener = page
    .locator("article.resource-card .title-button")
    .first();
  await tabTo(page, darkOpener);
  await page.keyboard.press("Enter");
  await expect(preview).toBeVisible();
  await tabTo(page, closePreview, "Tab", preview);
  await accessible(page, undefined, ".preview-actions > .muted");
  await page.screenshot({
    path: test.info().outputPath("keyboard-preview-dark-narrow.png"),
    fullPage: true,
  });
  await page.keyboard.press("Escape");
  await visibleFocus(darkOpener);
  await page.goto(catalog.origin + "/explore");
  await page.getByRole("button", { name: "View counts", exact: true }).click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("3 matching local releases");
  await page
    .getByRole("button", { name: "Filter by tag orders: 3", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Remove tag orders", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("3 matching local releases");
  await accessible(page);
  await page.screenshot({
    path: test.info().outputPath("local-facets-dark.png"),
    fullPage: true,
  });
});

test("accessibility: authoring, native validation and keyboard confirmation", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/resources/new");
  await expect(
    page.getByRole("textbox", { name: "Canonical resource identifier" }),
  ).toBeVisible();
  await expect(
    page
      .getByRole("combobox", { name: "Namespace", exact: true })
      .getByRole("option", {
        name: new RegExp(
          `^Catalog demo / .+ / ${catalog.receipt.namespaceId}$`,
        ),
      }),
  ).toHaveCount(1);
  const menu = page.getByRole("navigation", { name: "Workspace", exact: true });
  expect(
    await menu.evaluate(
      (element) => element.scrollHeight <= element.clientHeight,
    ),
  ).toBe(true);
  const last = menu.getByRole("link", { name: "API reference", exact: true });
  expect(
    await last.evaluate((element) => {
      const bounds = element.getBoundingClientRect();
      const menu = element.closest("nav")!.getBoundingClientRect();
      return bounds.top >= menu.top && bounds.bottom <= menu.bottom;
    }),
  ).toBe(true);
  await page.setViewportSize({ width: 1440, height: 600 });
  await menu.getByRole("link", { name: "Catalog", exact: true }).focus();
  await tabTo(page, last);
  expect(await menu.evaluate((element) => element.scrollTop)).toBeGreaterThan(
    0,
  );
  expect(
    await last.evaluate(
      (element) => element.getBoundingClientRect().bottom <= innerHeight,
    ),
  ).toBe(true);
  await page.setViewportSize({ width: 1440, height: 900 });
  await accessible(page);
  await page.goto(catalog.origin + "/namespaces/new");
  const name = page.getByRole("textbox", { name: "Display name", exact: true });
  const submit = page.getByRole("button", {
    name: "Create namespace",
    exact: true,
  });
  await tabTo(page, submit);
  await page.keyboard.press("Enter");
  await expect(name).toBeFocused();
  expect(
    await name.evaluate((el: HTMLInputElement) => el.validity.valueMissing),
  ).toBe(true);
  await expect(name).toHaveAccessibleName("Display name");
  await accessible(page);
  await page.keyboard.type("Keyboard accessible namespace");
  await tabTo(page, page.getByRole("textbox", { name: "Slug", exact: true }));
  await page.keyboard.type("keyboard-accessible");
  await tabTo(page, submit);
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", {
    name: "Create namespace",
    exact: true,
  });
  await expect(dialog).toBeVisible();
  await expect(
    dialog.getByRole("button", { name: "Cancel", exact: true }),
  ).toBeFocused();
  const overlaps = await dialog
    .getByLabel("Command summary")
    .evaluate((summary) => {
      const rows = [...summary.querySelectorAll("dt, dd")].map((item) => ({
        text: item.textContent,
        box: item.getBoundingClientRect(),
      }));
      return rows.flatMap((row, index) =>
        rows
          .slice(index + 1)
          .filter(
            (other) =>
              Math.min(row.box.right, other.box.right) >
                Math.max(row.box.left, other.box.left) + 1 &&
              Math.min(row.box.bottom, other.box.bottom) >
                Math.max(row.box.top, other.box.top) + 1,
          )
          .map((other) => [row.text, other.text]),
      );
    });
  expect(overlaps).toEqual([]);
  await accessible(page);
  await page.keyboard.press("Tab");
  await visibleFocus(
    dialog.getByRole("button", { name: "Confirm", exact: true }),
  );
  await page.keyboard.press("Shift+Tab");
  await visibleFocus(
    dialog.getByRole("button", { name: "Cancel", exact: true }),
  );
  await page.keyboard.press("Enter");
  await expect(dialog).not.toBeVisible();
  await visibleFocus(submit);
  await expect(name).toHaveValue("Keyboard accessible namespace");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Escape");
  await expect(submit).toBeFocused();
  await page.keyboard.press("Enter");
  await tabTo(
    page,
    dialog.getByRole("button", { name: "Confirm", exact: true }),
    "Tab",
    dialog,
  );
  await page.keyboard.press("Enter");
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole("status")).toContainText(
    "Created namespace keyboard-accessible",
  );
  await page.goto(catalog.origin + "/namespaces");
  await expect(
    page.getByText("Keyboard accessible namespace", { exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByText("Keyboard accessible namespace", { exact: true }),
  ).toBeVisible();
});

test("accessibility: review, members and audit controls", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/reviews");
  await expect(page.locator(".workspace-table tbody tr")).toHaveCount(1);
  await page.setViewportSize({ width: 390, height: 844 });
  const submitted = page.locator(".workspace-table .state-badge");
  await expect(submitted).toHaveText("submitted");
  expect(
    await submitted.evaluate((node) => {
      const range = document.createRange();
      range.selectNodeContents(node);
      return range.getClientRects().length;
    }),
  ).toBe(1);
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await accessible(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(catalog.origin + "/members");
  await openDisclosure(page, "Add Catalog member");
  await expect(
    page.getByRole("heading", { name: "Add Catalog member", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "local-admin", exact: true }),
  ).toBeVisible();
  await accessible(page);
  await page.goto(catalog.origin + "/audit");
  await expect(
    page.getByRole("button", { name: "Search audit events", exact: true }),
  ).toBeVisible();
  await accessible(page);
});

test("accessibility: search indexing remains usable with doubled text at 320px", async ({
  page,
  catalog,
}) => {
  await page.setViewportSize({ width: 320, height: 740 });
  await login(page, catalog, "local-admin", "/service#search-indexing");
  await expect(
    page.getByText(
      /Semantic indexing is turned off. Keyword search is available./,
    ),
  ).toBeVisible();
  await doubleText(page);
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
  expect(
    await page.locator(".topbar").evaluate((header) => {
      const bounds = header.getBoundingClientRect();
      return Array.from(header.children).every((child) => {
        const box = child.getBoundingClientRect();
        return (
          box.width === 0 ||
          (box.left >= bounds.left &&
            box.right <= bounds.right &&
            box.top >= bounds.top &&
            box.bottom <= bounds.bottom)
        );
      });
    }),
    "Enlarged header contents remain inside their background",
  ).toBe(true);
  const refresh = page.getByRole("button", { name: "Refresh", exact: true });
  await expect(refresh).toBeEnabled();
  await refresh.focus();
  await expect(refresh).toBeFocused();
  await expect(refresh).toBeInViewport();
  const reads = Promise.all(
    ["/api/v1/service-information", "/api/v1/capabilities"].map((path) =>
      page.waitForResponse(
        (response) =>
          response.request().method() === "GET" &&
          response.url().endsWith(path) &&
          response.status() === 200,
      ),
    ),
  );
  await page.keyboard.press("Enter");
  await reads;
  await expect(refresh).toBeEnabled();
  await expect(
    page.getByRole("heading", { name: "Search indexing", exact: true }),
  ).toBeVisible();
  await accessible(page);
});

test("accessibility: Catalog controls wrap with doubled text at 320px", async ({
  page,
  catalog,
}) => {
  await page.setViewportSize({ width: 320, height: 740 });
  await login(page, catalog);
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  await doubleText(page);
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
  const searches: string[] = [];
  page.on("request", (request) => {
    if (request.url().endsWith("/api/v1/catalog/search"))
      searches.push(request.method());
  });
  const table = page.getByRole("button", { name: "Table", exact: true });
  await table.focus();
  await expect(table).toBeInViewport();
  await expect(table).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(table).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator("tbody tr")).toHaveCount(3);
  const cards = page.getByRole("button", { name: "Cards", exact: true });
  await cards.focus();
  await expect(cards).toBeInViewport();
  await page.keyboard.press("Enter");
  await expect(cards).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  expect(searches).toEqual([]);
  // Focus scrolls to the layout controls. Scan from the page top so a filter
  // clipped at the sticky header's scroll edge is not measured as a small target.
  await page.evaluate(() => window.scrollTo(0, 0));
  await accessible(page);
});

test("theme changes preserve control contrast throughout animation", async ({
  page,
  catalog,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({
    colorScheme: "light",
    reducedMotion: "no-preference",
  });
  await login(page, catalog, "local-admin", "/namespaces");
  await page.mouse.move(0, 0);
  const control = page.locator(".mobile-nav-toggle");
  await expect(control).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "system");
  // Lengthen existing transitions so intermediate frames can be sampled
  // deterministically. This does not introduce any transition property.
  await control.evaluate((node) => {
    if (!(node instanceof HTMLElement))
      throw new Error("Expected an HTML control");
    node.style.transitionDuration = "10s";
  });
  const contrast = () =>
    control.evaluate((node) => {
      function luminance(color: string) {
        const channels = color
          .match(/\d+(?:\.\d+)?/g)
          ?.slice(0, 3)
          .map((value) => {
            const channel = Number(value) / 255;
            return channel <= 0.04045
              ? channel / 12.92
              : ((channel + 0.055) / 1.055) ** 2.4;
          });
        if (!channels || channels.length !== 3)
          throw new Error("Expected an opaque rendered color");
        return channels.reduce(
          (sum, channel, index) =>
            sum + channel * ([0.2126, 0.7152, 0.0722][index] ?? 0),
          0,
        );
      }
      // Force style resolution before inspecting the real CSS transitions.
      node.getBoundingClientRect();
      const animations = node
        .getAnimations()
        .filter(
          (animation) =>
            animation.effect?.getComputedTiming().iterations !== Infinity,
        );
      for (const animation of animations) animation.pause();
      try {
        return [0, 0.5, 1].map((progress) => {
          for (const animation of animations) {
            const duration = animation.effect?.getComputedTiming().duration;
            if (typeof duration !== "number")
              throw new Error("Expected a finite control transition");
            animation.currentTime = duration * progress;
          }
          const style = getComputedStyle(node);
          const foreground = luminance(style.color);
          const background = luminance(style.backgroundColor);
          return {
            progress,
            ratio:
              (Math.max(foreground, background) + 0.05) /
              (Math.min(foreground, background) + 0.05),
          };
        });
      } finally {
        for (const animation of animations) animation.finish();
      }
    });
  for (const sample of await contrast())
    expect(
      sample.ratio,
      "Initial light control contrast",
    ).toBeGreaterThanOrEqual(4.5);
  for (const colorScheme of ["dark", "light"] as const) {
    await page.emulateMedia({ colorScheme });
    for (const sample of await contrast())
      expect(
        sample.ratio,
        `${colorScheme} control contrast at ${sample.progress}`,
      ).toBeGreaterThanOrEqual(4.5);
  }
});

test("System follows the device theme and compact density preserves table text", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/namespaces");
  const cell = page.locator(".workspace-table tbody td").first();
  await expect(cell).toBeVisible();
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "System", exact: true }).check();
  await page.keyboard.press("Escape");
  const colors: string[] = [];
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    await expect(page.locator("html")).toHaveAttribute("data-theme", "system");
    colors.push(
      await page
        .locator("html")
        .evaluate((node) =>
          getComputedStyle(node).getPropertyValue("--canvas").trim(),
        ),
    );
    await accessible(page);
  }
  expect(colors[0]).not.toBe(colors[1]);
  const before = await cell.evaluate((node) => ({
    text: node.textContent,
    font: getComputedStyle(node).fontSize,
    padding: parseFloat(getComputedStyle(node).paddingTop),
  }));
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Compact", exact: true }).check();
  await page.keyboard.press("Escape");
  const after = await cell.evaluate((node) => ({
    text: node.textContent,
    font: getComputedStyle(node).fontSize,
    padding: parseFloat(getComputedStyle(node).paddingTop),
  }));
  expect(after.text).toBe(before.text);
  expect(after.font).toBe(before.font);
  expect(after.padding).toBeLessThan(before.padding);
  await page.setViewportSize({ width: 768, height: 600 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  // The bounded short-window menu deliberately scrolls. Focus must reveal its
  // last link; axe cannot resolve contrast for partially clipped offscreen rows.
  const lastLink = page
    .getByRole("navigation", { name: "Workspace", exact: true })
    .getByRole("link", { name: "API reference", exact: true });
  await lastLink.focus();
  await expect(lastLink).toBeFocused();
  await expect(lastLink).toBeInViewport();
  await openAccount(page);
  const account = page
    .getByRole("navigation", { name: "Account pages", exact: true })
    .getByRole("link", { name: "My access", exact: true });
  await account.focus();
  await expect(account).toBeFocused();
  await expect(account).toBeInViewport();
  await account.click();
  await page.setViewportSize({ width: 390, height: 844 });
  const heading = page.getByRole("heading", { name: "My access", exact: true });
  const title = await heading.boundingBox();
  const icon = await heading.locator("svg").boundingBox();
  expect(icon!.y).toBeGreaterThanOrEqual(title!.y);
  expect(icon!.y + icon!.height).toBeLessThanOrEqual(title!.y + title!.height);
  const identity = page.getByRole("region", {
    name: "Your identity",
    exact: true,
  });
  expect(
    await identity.evaluate((node) =>
      parseFloat(getComputedStyle(node).paddingTop),
    ),
  ).toBe(16);
  const values = await identity
    .locator(".metadata-grid dd")
    .evaluateAll((nodes) =>
      nodes.map((node) => ({
        value: node.getBoundingClientRect(),
        label: node.previousElementSibling!.getBoundingClientRect(),
        text: node.textContent,
        clipped: node.scrollWidth > node.clientWidth,
      })),
    );
  for (const { value, label, text, clipped } of values) {
    expect(value.y - label.y - label.height).toBeGreaterThanOrEqual(0);
    expect(value.y - label.y - label.height).toBeLessThanOrEqual(6);
    expect(text).not.toBe("");
    expect(clipped).toBe(false);
  }
  await page.goto(catalog.origin + "/members");
  const panel = page.locator(".workspace-table-wrap");
  await expect(panel).toBeVisible();
  const states = page.locator(".workspace-table .workspace-status");
  await expect(states.first()).toBeVisible();
  expect(
    await states.evaluateAll((nodes) =>
      nodes.every((node) => getComputedStyle(node).whiteSpace === "nowrap"),
    ),
  ).toBe(true);
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
});

test.describe("coarse-pointer control sizing", () => {
  test.use({ hasTouch: true, viewport: { width: 390, height: 740 } });
  test("filter clear and preview controls remain usable in a short touch layout", async ({
    page,
    catalog,
  }) => {
    await login(page, catalog);
    expect(
      await page.evaluate(() => matchMedia("(pointer: coarse)").matches),
    ).toBe(true);
    await page.getByRole("button", { name: "Filters", exact: true }).tap();
    const dialog = page.getByRole("dialog", { name: "Filters", exact: true });
    await dialog.getByRole("checkbox", { name: "Agents", exact: true }).check();
    await dialog.getByRole("checkbox", { name: "orders", exact: true }).check();
    const clear = dialog.getByRole("button", {
      name: "Clear tag filters",
      exact: true,
    });
    await expect(clear).toBeVisible();
    const bounds = await clear.boundingBox();
    expect(bounds!.width).toBeGreaterThanOrEqual(44);
    expect(bounds!.height).toBeGreaterThanOrEqual(44);
    await clear.tap();
    await expect(
      dialog.getByRole("checkbox", { name: "Agents", exact: true }),
    ).toBeChecked();
    await expect(
      dialog.getByRole("checkbox", { name: "orders", exact: true }),
    ).not.toBeChecked();
    const apply = dialog.getByRole("button", {
      name: "Apply filters",
      exact: true,
    });
    await expect(apply).toBeInViewport();
    await accessible(page);
    await apply.tap();
    await page
      .locator("article.resource-card")
      .first()
      .locator("button.title-button")
      .tap();
    const close = page.getByRole("button", {
      name: "Close preview",
      exact: true,
    });
    const closeBounds = await close.boundingBox();
    expect(closeBounds!.width).toBeGreaterThanOrEqual(44);
    expect(closeBounds!.height).toBeGreaterThanOrEqual(44);
    const record = page
      .getByRole("dialog")
      .getByRole("link", { name: "Open full record", exact: true });
    await expect(record).toBeInViewport();
    expect((await record.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    await expect(
      page
        .getByRole("dialog")
        .getByRole("heading", { name: "Use this resource", exact: true }),
    ).toBeVisible();
    await accessible(page);
    await close.tap();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  });
});

test("Account uses native keyboard navigation, exclusive header panels and safe outside dismissal under a subpath", async ({
  page,
  catalog,
}) => {
  await login(page, catalog);
  const trigger = page.getByTitle("Account", { exact: true });
  const details = page.locator("details.account");
  const account = page.getByRole("navigation", {
    name: "Account pages",
    exact: true,
  });
  const access = account.getByRole("link", { name: "My access", exact: true });
  const appearance = page.getByLabel("Appearance", { exact: true });
  await tabTo(page, trigger);
  await page.keyboard.press("Enter");
  await expect(details).toHaveAttribute("open", "");
  await page.keyboard.press("Tab");
  await expect(access).toBeFocused();
  for (const name of ["My work", "Saved items and searches", "My ratings"]) {
    await page.keyboard.press("Tab");
    await expect(
      account.getByRole("link", { name, exact: true }),
    ).toBeFocused();
  }
  for (const name of ["Saved items and searches", "My work", "My access"]) {
    await page.keyboard.press("Shift+Tab");
    await expect(
      account.getByRole("link", { name, exact: true }),
    ).toBeFocused();
  }
  await page.keyboard.press("Escape");
  await expect(trigger).toBeFocused();
  await expect(details).not.toHaveAttribute("open");
  await page.keyboard.press("Space");
  await expect(access).toBeVisible();
  for (const theme of ["Light", "Dark"] as const) {
    await appearance.click();
    await expect(details).not.toHaveAttribute("open");
    await page.getByRole("radio", { name: theme, exact: true }).check();
    await trigger.click();
    await expect(
      page.getByRole("radio", { name: theme, exact: true }),
    ).toBeHidden();
    // The disclosure intentionally overlaps the workspace. Scan the exposed
    // header, then the whole page after closure; do not suppress contrast rules.
    await accessible(page, ".topbar");
    await page.keyboard.press("Escape");
    await accessible(page);
  }
  await openAccount(page);
  await page.getByRole("button", { name: "Sign out", exact: true }).focus();
  await page.keyboard.press("Tab");
  await expect(
    page
      .getByRole("navigation", { name: "Workspace", exact: true })
      .getByRole("link", { name: "Catalog", exact: true }),
  ).toBeFocused();
  await expect(details).not.toHaveAttribute("open");
  await trigger.click();
  await page
    .getByRole("searchbox", { name: "Search Catalog", exact: true })
    .click();
  await expect(details).not.toHaveAttribute("open");
  await expect(
    page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
  ).toBeFocused();
  await trigger.click();
  await access.click();
  await expect(page).toHaveURL(catalog.origin + catalog.basePath + "/access");
  await expect(page.getByRole("navigation", { name: "Breadcrumb" })).toHaveText(
    "AccountMy access",
  );
  await openAccount(page);
  await expect(access).toHaveAttribute("aria-current", "page");
  await expect(
    page
      .getByRole("navigation", { name: "Workspace", exact: true })
      .getByRole("link", { name: "My access", exact: true }),
  ).toHaveCount(0);
});

test("Account fits long identities, short touch screens and 320px doubled text without horizontal overflow", async ({
  page,
  catalog,
  context,
}) => {
  const subject = "synthetic-long-identity-".repeat(12);
  // Substitute the display subject only. Permissions, cookies and API requests
  // retain the actual owned service identity; this is a layout boundary fixture.
  await page.route("**/auth/session", async (route) => {
    const response = await route.fetch();
    const session = (await response.json()) as { access?: { subject: string } };
    if (session.access) session.access.subject = subject;
    await route.fulfill({ response, json: session });
  });
  await login(page, catalog);
  const touch = await context.newCDPSession(page);
  await touch.send("Emulation.setTouchEmulationEnabled", { enabled: true });
  for (const [width, height] of [
    [1440, 480],
    [1101, 480],
    [1100, 480],
    [901, 480],
    [900, 480],
    [768, 400],
    [701, 480],
    [700, 480],
    [320, 480],
  ] as const) {
    await page.setViewportSize({ width, height });
    await openAccount(page);
    const panel = page.locator(".account-panel");
    await expect(panel.getByText(subject, { exact: true })).toBeVisible();
    const box = (await panel.boundingBox())!;
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    expect(box.y + box.height).toBeLessThanOrEqual(height);
    const logout = page.getByRole("button", { name: "Sign out", exact: true });
    await logout.focus();
    await expect(logout).toBeInViewport();
    expect((await logout.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    await page.keyboard.press("Escape");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
      `The page fits the ${width}px viewport`,
    ).toBe(true);
  }
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Compact", exact: true }).check();
  await page.keyboard.press("Escape");
  await doubleText(page);
  await openAccount(page);
  const panel = page.locator(".account-panel");
  const box = (await panel.boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(320);
  expect(box.y + box.height).toBeLessThanOrEqual(480);
  await page.getByRole("button", { name: "Sign out", exact: true }).focus();
  await expect(
    page.getByRole("button", { name: "Sign out", exact: true }),
  ).toBeInViewport();
  await page.keyboard.press("Escape");
  await expect(page.getByTitle("Account", { exact: true })).toBeFocused();
  await expect(
    page.getByRole("button", { name: "Workspace menu", exact: true }),
  ).toBeInViewport();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});
