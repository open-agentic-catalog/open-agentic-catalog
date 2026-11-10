import { test, expect, login } from "./fixtures";

test("submitted text, visibility and history keep queries private and reset predictably", async ({
  page,
  catalog,
}) => {
  await login(page, catalog);
  const cards = page.locator("article.resource-card");
  await expect(cards).toHaveCount(3);
  const search = page.getByRole("searchbox", {
    name: "Search Catalog",
    exact: true,
  });
  await search.fill("Order exception");
  await expect(
    page.getByText(
      "Text changed. Choose Search to apply it. Filters and refresh use the applied search text.",
      { exact: true },
    ),
  ).toBeVisible();
  // Editing alone must not replace the submitted results.
  await expect(cards).toHaveCount(3);
  await search.press("Enter");
  await expect(cards).toHaveCount(2);
  await expect(page.locator("#pending-search-text")).toHaveCount(0);
  await expect(cards.filter({ hasText: "Order exception Agent" })).toHaveCount(
    1,
  );
  expect(new URL(page.url()).search).toBe("");
  await page.getByRole("button", { name: "Plugins", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "No resources found" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "All kinds", exact: true }).click();
  await expect(cards).toHaveCount(2);
  await expect(search).toHaveValue("Order exception");
  await expect(
    page.getByRole("button", { name: "All kinds", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await page
    .getByRole("button", { name: "Visibility: Private", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "No resources found" }),
  ).toBeVisible();
  await page
    .getByRole("button", {
      name: "Remove visibility filter: private",
      exact: true,
    })
    .click();
  await expect(cards).toHaveCount(2);
  await page.getByRole("button", { name: "Clear all", exact: true }).click();
  await expect(search).toHaveValue("");
  await expect(search).toBeFocused();
  await expect(cards).toHaveCount(3);
  await cards.first().getByRole("button").first().click();
  await page
    .getByRole("link", { name: "Open full record", exact: true })
    .click();
  const bookmark = page.url();
  await expect(
    page.getByRole("navigation", { name: "Record workflows" }),
  ).toBeVisible();
  await page.goBack();
  await expect(cards).toHaveCount(3);
  await page.goForward();
  await expect(page).toHaveURL(bookmark);
  await expect(
    page.getByRole("navigation", { name: "Record workflows" }),
  ).toBeVisible();
});

test("missing Catalog bookmark shows a safe error and allows ordinary navigation", async ({
  page,
  catalog,
  network,
}) => {
  const missing = "00000000-0000-7000-8000-ffffffffffff";
  network.expectFailure(
    "GET",
    `/_catalog/api/v1/catalog/resources/${missing}`,
    404,
  );
  await login(page, catalog, "local-reader", `/catalog/${missing}`);
  await expect(page.getByRole("alert")).toContainText(
    "This record is unavailable with your current access. Check the reference or return to its list.",
  );
  await expect(
    page.getByRole("navigation", { name: "Record workflows" }),
  ).toHaveCount(0);
  await page
    .getByRole("link", { name: "Catalog", exact: true })
    .first()
    .click();
  await expect(page.locator("article.resource-card")).toHaveCount(3);
});

test("system appearance follows preference changes with reduced motion and narrow reflow", async ({
  page,
  catalog,
}) => {
  await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
  await login(page, catalog);
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  await expect(page.locator("article.resource-card").first()).toHaveCSS(
    "transition-duration",
    "0s",
  );
  await expect(page.locator("article.resource-card").first()).toHaveCSS(
    "animation-name",
    "none",
  );
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "System", exact: true }).check();
  await page.keyboard.press("Escape");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "system");
  await expect(page.locator("html")).toHaveCSS("color-scheme", "dark");
  await page.emulateMedia({ colorScheme: "light", reducedMotion: "reduce" });
  await expect(page.locator("html")).toHaveCSS("color-scheme", "light");
  await page.setViewportSize({ width: 320, height: 740 });
  await page.getByRole("button", { name: "Filters", exact: true }).focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("button", { name: "Apply filters", exact: true }),
  ).toBeInViewport();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("button", { name: "Filters", exact: true }),
  ).toBeFocused();
  await page.reload();
  await expect(page.locator("html")).toHaveCSS("color-scheme", "light");
  expect(
    await page.evaluate(() => localStorage.getItem("catalog-ui:theme")),
  ).toBe("system");
});
