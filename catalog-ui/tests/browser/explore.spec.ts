import { object } from "./json";
import { test, expect, login } from "./fixtures";

test("local browse and combined facets use the published releases and link to exact records", async ({
  page,
  catalog,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await login(page, catalog, "local-reader", "/explore");
  let facetReads = 0;
  page.on("request", (request) => {
    if (
      request.url().endsWith("/api/v1/catalog/explore") &&
      request.method() === "POST"
    )
      facetReads++;
  });
  await expect(page.locator("tbody tr")).toHaveCount(3);
  const expectRefreshSpacing = async () => {
    const action = page.getByRole("button", {
      name: "Refresh browse results",
      exact: true,
    });
    const row = page.locator(".workspace-toolbar").filter({ has: action });
    await expect(row).toHaveCount(1);
    const spacing = await row.evaluate((toolbar) => {
      const fields = Array.from(
        toolbar.querySelectorAll(
          ":scope > .workspace-field, :scope > .filter-group",
        ),
      );
      const fieldsBottom = Math.max(
        ...fields.map((field) => field.getBoundingClientRect().bottom),
      );
      const button = toolbar
        .querySelector(".workspace-actions > button")!
        .getBoundingClientRect();
      const table = document.querySelector(
        '[aria-label="Local browse results"]',
      )!;
      // Sorting guidance is the first result content, before any scrolling hint.
      const firstResult = document.getElementById(
        table.querySelector("table")!.getAttribute("aria-describedby")!,
      )!;
      return {
        above: button.top - fieldsBottom,
        below: firstResult.getBoundingClientRect().top - button.bottom,
      };
    });
    expect(spacing.above).toBeGreaterThanOrEqual(8);
    expect(spacing.above).toBeLessThanOrEqual(20);
    expect(spacing.below).toBeGreaterThanOrEqual(16);
    expect(spacing.below).toBeLessThanOrEqual(24);
  };
  await expectRefreshSpacing();
  // Namespace help and its explicit loading action must not push adjacent
  // filter selects down to the action's baseline.
  const alignedFilters = async () => {
    const tops = await Promise.all(
      ["Local namespace", "Kind", "Visibility"].map((name) =>
        page
          .getByRole("combobox", { name, exact: true })
          .evaluate((control) => control.getBoundingClientRect().top),
      ),
    );
    expect(Math.max(...tops) - Math.min(...tops)).toBeLessThanOrEqual(2);
  };
  await alignedFilters();
  await page
    .getByRole("button", { name: "Load namespaces", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "First namespace page", exact: true }),
  ).toBeVisible();
  await alignedFilters();
  for (const width of [390, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    await expectRefreshSpacing();
  }
  await page
    .getByRole("combobox", { name: "Kind", exact: true })
    .selectOption("skill");
  await expect(page.locator("tbody tr")).toHaveCount(1);
  const refreshedBrowse = page.waitForResponse(
    (response) =>
      response.url().includes("/api/v1/catalog/resources") &&
      response.request().method() === "GET",
  );
  await page
    .getByRole("button", { name: "Refresh browse results", exact: true })
    .click();
  expect((await refreshedBrowse).status()).toBe(200);
  await expect(
    page.getByRole("combobox", { name: "Kind", exact: true }),
  ).toHaveValue("skill");
  const skill = catalog.receipt.items.find(
    (item) => item.key === "order-skill",
  )!;
  // The selected row is checked against its real API relationship, not a
  // display-only label. Browser Back must return to a usable browse screen.
  const link = page.locator("tbody a");
  await expect(link).toHaveAttribute("href", `/catalog/${skill.releaseId}`);
  await link.click();
  await expect(page).toHaveURL(catalog.origin + `/catalog/${skill.releaseId}`);
  await page.goBack();
  await page
    .getByRole("combobox", { name: "Visibility", exact: true })
    .selectOption("private");
  await expect(
    page.getByText("No local releases on this page.", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "View counts", exact: true }).click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("3 matching local releases");
  const termsSummary = page.getByText("Tags and categories", { exact: true });
  const metadataSummary = page.getByText("Publisher, license and visibility", {
    exact: true,
  });
  const beforeDisclosureReads = facetReads;
  for (const summary of [termsSummary, metadataSummary]) {
    const disclosure = summary.locator("..");
    await expect(disclosure).toHaveJSProperty("open", false);
    await summary.click();
    await expect(disclosure).toHaveJSProperty("open", true);
    await summary.click();
    await expect(disclosure).toHaveJSProperty("open", false);
  }
  expect(facetReads).toBe(beforeDisclosureReads);
  await expect(
    page.getByRole("heading", { name: "Trust assessment", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Not assessed", { exact: true })).toBeVisible();
  // Standalone namespace controls align with the search panel and leave a
  // visible gap. The same component in the Browse toolbar keeps its alignment.
  const namespace = page.getByRole("combobox", {
    name: "Local namespace",
    exact: true,
  });
  const namespaceSection = page.locator(".explore-namespace");
  const form = page.locator("form.workspace-form");
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    const control = await namespace.boundingBox();
    const section = await namespaceSection.boundingBox();
    const search = await form.boundingBox();
    expect(Math.abs(control!.x - search!.x)).toBeLessThanOrEqual(2);
    expect(search!.y - section!.y - section!.height).toBeGreaterThanOrEqual(16);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(width);
  }
  await page
    .getByRole("button", { name: "Load namespaces", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "First namespace page", exact: true }),
  ).toBeVisible();
  const loadedSection = await namespaceSection.boundingBox();
  const loadedForm = await form.boundingBox();
  expect(
    loadedForm!.y - loadedSection!.y - loadedSection!.height,
  ).toBeGreaterThanOrEqual(16);
  await page.setViewportSize({ width: 1440, height: 900 });
  const kinds = page.locator(".facet-grid section").filter({
    has: page.getByRole("heading", { name: "Resource kind", exact: true }),
  });
  for (const kind of ["Agent", "MCP Server", "Skill"])
    await expect(kinds.getByText(kind, { exact: true })).toBeVisible();
  // Refine returned values using the actual local query, not page-local data.
  await page
    .getByRole("button", {
      name: "Filter by resource kind Skill: 1",
      exact: true,
    })
    .focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("1 matching local release");
  await expect(
    page.getByLabel("Search local metadata", { exact: true }),
  ).toBeFocused();
  await page
    .getByLabel("Search local metadata", { exact: true })
    .fill("unsubmitted-draft");
  const manualRefresh = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/catalog/explore") &&
      response.request().method() === "POST",
  );
  await page
    .getByRole("button", { name: "Refresh counts", exact: true })
    .click();
  const refreshed = await manualRefresh;
  expect(refreshed.status()).toBe(200);
  expect(object(refreshed.request().postDataJSON()).query).toEqual({
    kinds: ["skill"],
  });
  await expect(
    page.getByLabel("Search local metadata", { exact: true }),
  ).toHaveValue("unsubmitted-draft");
  const ranking = page.getByText("How search works", { exact: true });
  await ranking.focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByText(/Search matches words in resource names/),
  ).toBeVisible();
  await page.keyboard.press("Enter");
  await expect(
    page.getByText(/Search matches words in resource names/),
  ).not.toBeVisible();
  await page
    .getByRole("button", { name: "Filter by tag orders: 1", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Remove tag orders", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", {
      name: `Filter by publisher ${catalog.receipt.publisherId}: 1`,
      exact: true,
    })
    .click();
  await expect(
    page.getByRole("button", {
      name: `Remove publisher ${catalog.receipt.publisherId}`,
      exact: true,
    }),
  ).toBeVisible();
  const refinement = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/catalog/explore") &&
      response.request().method() === "POST" &&
      object(object(response.request().postDataJSON()).query).visibility ===
        "public",
  );
  await page
    .getByRole("button", {
      name: "Filter by visibility Public: 1",
      exact: true,
    })
    .click();
  const response = await refinement;
  expect(response.status()).toBe(200);
  expect(object(response.request().postDataJSON()).query).toEqual({
    kinds: ["skill"],
    tags: ["orders"],
    publisher: catalog.receipt.publisherId,
    visibility: "public",
  });
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("1 matching local release");
  await expect(
    page.getByLabel("Search local metadata", { exact: true }),
  ).toHaveValue("unsubmitted-draft");
  for (const field of ["category", "trustState", "source"])
    await expect(
      page.getByRole("button", { name: new RegExp(`Filter by ${field}`) }),
    ).toHaveCount(0);
  const namespaceRead = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/catalog/explore") &&
      object(object(response.request().postDataJSON()).query).namespaceId ===
        catalog.receipt.namespaceId,
  );
  await expect(
    namespace.locator("option").filter({ hasText: /^Catalog demo$/ }),
  ).toHaveAttribute("value", catalog.receipt.namespaceId);
  await page
    .getByRole("button", {
      name: "Filter by local namespace Catalog demo: 1",
      exact: true,
    })
    .click();
  expect((await namespaceRead).status()).toBe(200);
  await expect(
    page.getByRole("button", {
      name: "Remove local namespace Catalog demo",
      exact: true,
    }),
  ).toBeVisible();
  await page
    .getByRole("button", {
      name: "Remove local namespace Catalog demo",
      exact: true,
    })
    .click();
  await expect(
    page.getByRole("button", {
      name: "Remove local namespace Catalog demo",
      exact: true,
    }),
  ).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 840 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: test.info().outputPath("local-facets-narrow.png"),
    fullPage: true,
  });
  await page
    .getByRole("button", { name: "Remove resource kind Skill", exact: true })
    .click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("3 matching local releases");
  for (const name of [
    "Remove tag orders",
    `Remove publisher ${catalog.receipt.publisherId}`,
    "Remove visibility Public",
  ]) {
    await page.getByRole("button", { name, exact: true }).click();
    await expect(page.getByRole("button", { name, exact: true })).toHaveCount(
      0,
    );
    await expect(
      page.getByRole("status", { name: "Matched releases" }),
    ).toHaveText("3 matching local releases");
  }
  await page
    .getByRole("button", { name: "Clear filters", exact: true })
    .click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("3 matching local releases");
  // Native required-field validation must reveal a hidden empty row before
  // focusing it. Preparation and disclosure changes do not issue a read.
  await termsSummary.click();
  await page.getByRole("button", { name: "Add tag", exact: true }).click();
  await termsSummary.click();
  const beforeInvalidReads = facetReads;
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await expect(termsSummary.locator("..")).toHaveJSProperty("open", true);
  await expect(page.getByLabel("Tag 1", { exact: true })).toBeFocused();
  expect(facetReads).toBe(beforeInvalidReads);
  await page.getByLabel("Tag 1", { exact: true }).fill("orders");
  await page.getByRole("checkbox", { name: "Skills", exact: true }).check();
  await page.getByRole("checkbox", { name: "Agents", exact: true }).check();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("3 matching local releases");
  await metadataSummary.click();
  await page
    .getByRole("combobox", { name: "Visibility", exact: true })
    .selectOption("public");
  await page
    .getByRole("textbox", { name: "Publisher ID (optional)", exact: true })
    .fill(catalog.receipt.publisherId);
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("2 matching local releases");
  await page
    .getByRole("button", { name: "Remove resource kind Agent", exact: true })
    .click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("1 matching local release");
  await expect(kinds.getByText("Agent", { exact: true })).toHaveCount(0);
  await page
    .getByRole("textbox", { name: "Search local metadata", exact: true })
    .fill("absent-browser-fixture-term");
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("0 matching local releases");
  await expect(
    kinds.getByText("No matching values.", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", {
      name: "Remove text absent-browser-fixture-term",
      exact: true,
    })
    .click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("1 matching local release");
  await expect(
    page.getByRole("textbox", { name: "Search local metadata", exact: true }),
  ).toHaveValue("");
  await page
    .getByRole("button", { name: "Clear filters", exact: true })
    .click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("3 matching local releases");
  await expect(
    page.getByRole("checkbox", { name: "Skills", exact: true }),
  ).not.toBeChecked();
  await page
    .getByRole("button", { name: "Browse releases", exact: true })
    .click();
  await expect(
    page.getByRole("combobox", { name: "Kind", exact: true }),
  ).toHaveValue("skill");
  await expect(
    page.getByRole("combobox", { name: "Visibility", exact: true }),
  ).toHaveValue("private");
  await expect(
    page.getByText("No local releases on this page.", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("combobox", { name: "Kind", exact: true })
    .selectOption("");
  await page
    .getByRole("combobox", { name: "Visibility", exact: true })
    .selectOption("");
  await expect(page.locator("tbody tr")).toHaveCount(3);
});
