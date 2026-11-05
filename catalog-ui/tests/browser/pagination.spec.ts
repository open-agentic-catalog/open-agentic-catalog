import { test, expect, login, confirmCommand } from "./fixtures";

test("real publisher pages retain an off-page listing selection without submitting the form", async ({
  page,
  catalog,
  network,
}) => {
  // The seed has one Publisher. Add only enough to cross the shared 25-item
  // list/picker boundary; never enlarge the dataset for unrelated journeys.
  const ids = [catalog.receipt.publisherId];
  for (let index = 0; index < 25; index++) {
    const reply = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/publishers",
      {
        displayName: `Paged publisher ${index}`,
        domains: [`page-${index}.example.com`],
      },
    );
    ids.push(reply.data.id);
  }
  await login(page, catalog, "local-admin", "/publishers");
  const rows = page.locator("tbody tr");
  await expect(rows).toHaveCount(25);
  const rowIds = () =>
    rows
      .locator("a")
      .evaluateAll((links) =>
        links.map((link) =>
          new URL((link as HTMLAnchorElement).href).pathname.split("/").pop()!,
        ),
      );
  const first = await rowIds();
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(rows).toHaveCount(1);
  const last = (await rows.locator("a").getAttribute("href"))!
    .split("/")
    .pop()!;
  const lastName = await rows.locator("a").innerText();
  expect(lastName).toMatch(/Paged publisher/);
  expect(first).not.toContain(last);
  expect([...first, last].sort()).toEqual(ids.sort());
  await expect(
    page.getByRole("button", { name: "Next page", exact: true }),
  ).toHaveCount(0);
  const currentRefresh = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith("/api/v1/publishers"),
  );
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  const refreshed = await currentRefresh;
  expect(refreshed.status()).toBe(200);
  expect(new URL(refreshed.url()).searchParams.get("cursor")).toBeTruthy();
  await expect(rows).toHaveCount(1);
  expect(await rowIds()).toEqual([last]);
  await page.getByRole("button", { name: "First page", exact: true }).click();
  await expect(rows).toHaveCount(25);
  expect(await rowIds()).toEqual(first);

  const writes: string[] = [];
  page.on("request", (request) => {
    if (request.method() !== "GET" && request.url().includes("/_catalog/"))
      writes.push(request.url());
  });
  const pager = page.getByRole("group", {
    name: "Publisher inventory pagination",
    exact: true,
  });
  for (const recovery of ["First page", "Reload", "Refresh"]) {
    // Corrupt the real cursor on the wire; the actual API rejects it. The
    // following read must remove it instead of repeating the failed request.
    network.expectFailure("GET", "/_catalog/api/v1/publishers", 400);
    await page.route("**/_catalog/api/v1/publishers?*", async (route) => {
      const url = new URL(route.request().url());
      expect(url.searchParams.get("cursor")).toBeTruthy();
      url.searchParams.set(
        "cursor",
        url.searchParams.get("cursor") + "invalid",
      );
      await route.continue({ url: url.href });
      await page.unroute("**/_catalog/api/v1/publishers?*");
    });
    await page.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(rows).toHaveCount(0);
    await expect(pager).toBeFocused();
    await expect(
      pager.getByRole("button", { name: "First page" }),
    ).toBeEnabled();
    await expect(pager.getByRole("button", { name: "Next page" })).toHaveCount(
      0,
    );
    const recovered = page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith("/api/v1/publishers"),
    );
    const button = page.getByRole("button", { name: recovery, exact: true });
    await button.focus();
    await page.keyboard.press("Enter");
    const response = await recovered;
    expect(response.status()).toBe(200);
    expect(new URL(response.url()).searchParams.get("cursor")).toBeNull();
    expect(new URL(response.url()).searchParams.get("limit")).toBe("25");
    await expect(rows).toHaveCount(25);
    expect(await rowIds()).toEqual(first);
    await expect(page.getByRole("alert")).toHaveCount(0);
    if (recovery === "First page") await expect(pager).toBeFocused();
    expect(writes).toEqual([]);
  }

  const item = catalog.receipt.items.find(
    (item) => item.key === "draft-agent",
  )!;
  await page.goto(`${catalog.origin}/listings/${item.listingId}`);
  await page
    .getByRole("button", { name: "New listing revision", exact: true })
    .click();
  const picker = page.getByRole("combobox", { name: "Publisher", exact: true });
  const field = page
    .locator("div")
    .filter({ has: picker })
    .filter({
      has: page.locator(".pagination"),
    })
    .last();
  await field.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(picker.locator(`option[value="${last}"]`)).toHaveCount(1);
  await picker.selectOption(last);
  await field.getByRole("button", { name: "First page", exact: true }).click();
  await expect(picker.locator(`option[value="${first[0]}"]`)).toHaveCount(1);
  await expect(picker).toHaveValue(last);
  await expect(picker.locator(`option[value="${last}"]`)).toHaveText(
    `${lastName} / ${last} (selected)`,
  );
  await page
    .getByLabel("Display name", { exact: true })
    .fill("Refreshed selector draft");
  const added = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/publishers",
    {
      displayName: "ZZ refreshed publisher",
      domains: ["refreshed.example.com"],
    },
  );
  const refreshedChoices = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith("/api/v1/publishers"),
  );
  await field
    .getByRole("button", { name: "Refresh publisher choices", exact: true })
    .click();
  expect(
    new URL((await refreshedChoices).url()).searchParams.get("cursor"),
  ).toBeNull();
  await expect(picker).toHaveValue(last);
  await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
    "Refreshed selector draft",
  );
  await field.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(picker.locator(`option[value="${added.data.id}"]`)).toHaveText(
    `ZZ refreshed publisher / ${added.data.id}`,
  );
  await expect(picker).toHaveValue(last);
  await expect(page.getByRole("dialog")).not.toBeVisible();
  expect(writes).toEqual([]);
  // A successful save remounts revision history. Keep that real read pending
  // to prove its loading status cannot obscure the editor's command receipt.
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route(
    `**/_catalog/api/v1/catalog-listings/${item.listingId}/revisions?*`,
    async (route) => {
      await held;
      await route.continue();
    },
    { times: 1 },
  );
  const editor = page.locator("section").filter({
    has: page.getByRole("heading", {
      name: "Create listing revision",
      exact: true,
    }),
  });
  const history = page.locator("section").filter({
    has: page.getByRole("heading", {
      name: "Listing revision history",
      exact: true,
    }),
  });
  try {
    await page
      .getByRole("button", { name: "Create listing revision", exact: true })
      .click();
    await confirmCommand(page);
    await expect(history.getByRole("status")).toHaveText("Loading...");
    const receipt = editor.getByRole("status");
    await expect(receipt).toHaveText("Listing revision saved.");
    await expect(receipt).toBeFocused();
  } finally {
    release();
  }
  await expect(history.getByRole("status")).toHaveCount(0);
  await page.reload();
  await page
    .getByRole("link", { name: "Inspect candidate revision", exact: true })
    .click();
  await expect(
    page
      .locator(".listing-metadata dt")
      .filter({ hasText: /^Publisher ID$/ })
      .locator("+ dd"),
  ).toHaveText(last);
  const revisionId = page.url().split("/").pop()!;
  const saved = await catalog.admin<{ publisherId: string }>(
    "GET",
    `/api/v1/listing-revisions/${revisionId}`,
  );
  expect(saved.data.publisherId).toBe(last);
});
