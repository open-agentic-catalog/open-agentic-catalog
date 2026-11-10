import { test, expect, login } from "./fixtures";

test("filter drafts dismiss without requests and every clear operation preserves the exact applied query", async ({
  page,
  catalog,
}) => {
  await login(page, catalog);
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  const requests: unknown[] = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().endsWith("/api/v1/catalog/search"))
      requests.push(r.postDataJSON());
  });
  const filters = page.getByRole("button", { name: /^Filters/ });
  await expect(filters).toHaveAccessibleName("Filters");
  await expect(filters).toHaveAccessibleDescription("0 applied filters");
  for (const close of ["close", "trigger", "outside"]) {
    await filters.click();
    await page.getByRole("checkbox", { name: "Skills", exact: true }).check();
    await expect(filters).toHaveAccessibleDescription("0 applied filters");
    if (close === "close")
      await page
        .getByRole("button", { name: "Close filters", exact: true })
        .click();
    else if (close === "trigger") await filters.click();
    else
      await page
        .getByRole("searchbox", { name: "Search Catalog", exact: true })
        .click();
    await expect(
      page.getByRole("region", { name: "Filters", exact: true }),
    ).toHaveCount(0);
    expect(requests).toEqual([]);
  }
  await filters.click();
  await expect(
    page.getByRole("checkbox", { name: "Skills", exact: true }),
  ).not.toBeChecked();
  for (const invalid of [
    "x".repeat(81),
    Array.from({ length: 33 }, (_, i) => `tag${i}`).join(","),
  ]) {
    await page.getByLabel("Add tag", { exact: true }).fill(invalid);
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText("Use up to 32 tags");
    expect(requests).toEqual([]);
  }
  await page
    .getByRole("button", { name: "Clear filters", exact: true })
    .click();
  await page.getByRole("button", { name: "Add category", exact: true }).click();
  const category = page.getByLabel("Category 1", { exact: true });
  await category.fill("   ");
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "categories needs 1 to 80 characters",
  );
  await expect(category).toBeFocused();
  expect(requests).toEqual([]);
  await page
    .getByRole("button", { name: "Clear filters", exact: true })
    .click();
  await page.getByRole("checkbox", { name: "Skills", exact: true }).check();
  await page.getByRole("checkbox", { name: "Skills", exact: true }).uncheck();
  await page.getByRole("checkbox", { name: "orders", exact: true }).check();
  await page.getByRole("checkbox", { name: "orders", exact: true }).uncheck();
  await page.getByRole("checkbox", { name: "orders", exact: true }).check();
  await page
    .getByRole("button", { name: "Clear tag filters", exact: true })
    .click();
  await page
    .getByLabel("Publisher identifier", { exact: true })
    .fill(catalog.receipt.publisherId);
  await page
    .getByRole("button", { name: "Clear publisher filter", exact: true })
    .click();
  const publisher = page
    .getByRole("region", { name: "Filters", exact: true })
    .locator("button.publisher-choice")
    .filter({ hasText: catalog.receipt.publisherId });
  await publisher.click();
  await publisher.click();
  await page.getByRole("radio", { name: "Private", exact: true }).check();
  await page
    .getByRole("radio", { name: "Any visibility", exact: true })
    .check();
  await page
    .getByLabel("Publisher identifier", { exact: true })
    .fill(catalog.receipt.publisherId);
  await page.getByRole("radio", { name: "Public", exact: true }).check();
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  expect(requests).toHaveLength(1);
  expect(requests[0]).toMatchObject({
    publisher: catalog.receipt.publisherId,
    visibility: "public",
  });
  expect(requests[0]).not.toHaveProperty("tags");
  expect(requests[0]).not.toHaveProperty("kinds");
  await expect(filters).toHaveAccessibleDescription("2 applied filters");
  await page
    .getByRole("button", {
      name: `Remove publisher: ${catalog.receipt.publisherId}`,
      exact: true,
    })
    .click();
  await expect(filters).toHaveAccessibleDescription("1 applied filters");
  await page
    .getByRole("searchbox", { name: "Search Catalog", exact: true })
    .fill("Order");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await page
    .getByRole("button", { name: "Remove search text Order", exact: true })
    .click();
  await expect(
    page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
  ).toHaveValue("");
  await expect(
    page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
  ).toBeFocused();
});

test("resource list pages retain distinct real identities and changing kind resets its cursor", async ({
  page,
  catalog,
}) => {
  const initial = await catalog.admin<{ items: { id: string }[] }>(
    "GET",
    "/api/v1/resources?limit=100",
  );
  for (let i = initial.data.items.length; i < 26; i++) {
    await catalog.admin("POST", "/api/v1/resources", {
      namespaceId: catalog.receipt.namespaceId,
      kind: "agent",
      canonicalIdentifier: `urn:air:demo.catalog.example.com:demo:list-${i}`,
    });
  }
  await login(page, catalog, "local-reader", "/resources");
  const links = page.locator("tbody a");
  await expect(links).toHaveCount(25);
  const first = await links.evaluateAll((items) =>
    items.map((item) => item.getAttribute("href")),
  );
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(links).toHaveCount(1);
  expect(first).not.toContain(await links.getAttribute("href"));
  const response = page.waitForResponse(
    (r) =>
      r.url().includes("/api/v1/resources?") &&
      new URL(r.url()).searchParams.get("kind") === "skill",
  );
  await page
    .getByRole("combobox", { name: "Resource kind", exact: true })
    .selectOption("skill");
  expect(new URL((await response).url()).searchParams.has("cursor")).toBe(
    false,
  );
  await expect(links).toHaveCount(1);
  await expect(links).toHaveAttribute(
    "href",
    `/resources/${catalog.receipt.items.find((item) => item.key === "order-skill")!.resourceId}`,
  );
  await expect(
    page.getByRole("button", { name: "First page", exact: true }),
  ).toHaveCount(0);
});

test("subpath: mobile publisher labels retain exact criteria and live exclusion is explicit", async ({
  page,
  catalog,
}) => {
  await page.setViewportSize({ width: 390, height: 740 });
  await login(page, catalog);
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  const search = page.getByRole("searchbox", {
    name: "Search Catalog",
    exact: true,
  });
  await expect(search).toHaveAccessibleDescription(
    /Live search may send your search text, tags and filters/,
  );
  await search.fill("Order");
  const submitted = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/catalog/search"),
  );
  await search.press("Enter");
  expect((await submitted).status()).toBe(200);
  expect((await submitted).request().postDataJSON()).toEqual({
    text: "Order",
    limit: 24,
  });
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  await search.fill("unsubmitted draft");
  const filter = page.getByRole("button", { name: "Filters", exact: true });
  await filter.click();
  const drawer = page.getByRole("dialog", { name: "Filters", exact: true });
  const publisher = drawer
    .locator("button.publisher-choice")
    .filter({ hasText: catalog.receipt.publisherId });
  await expect(publisher).toContainText("Demo Publisher");
  await expect(publisher).toContainText("Local publisher");
  await publisher.focus();
  await page.keyboard.press("Enter");
  await expect(publisher).toHaveAttribute("aria-pressed", "true");
  await page
    .getByRole("checkbox", { name: "Live external", exact: true })
    .uncheck();
  const applied = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/catalog/search"),
  );
  await drawer
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  expect((await applied).status()).toBe(200);
  expect((await applied).request().postDataJSON()).toEqual({
    text: "Order",
    limit: 24,
    publisher: catalog.receipt.publisherId,
    origins: ["local", "indexed"],
  });
  await expect(drawer).toHaveCount(0);
  await expect(filter).toBeFocused();
  await expect(search).toHaveValue("unsubmitted draft");
  await expect(search).toHaveAccessibleDescription(/Live sources are excluded/);
  await expect(search).toHaveAccessibleDescription(
    /configured semantic search provider/,
  );
  await expect(
    page.getByText("How results are counted", { exact: true }),
  ).toHaveCount(0);
  await expect(page.locator("article.resource-card")).toHaveCount(3);
});
