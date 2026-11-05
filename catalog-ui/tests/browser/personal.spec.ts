import { test, expect, login } from "./fixtures";

for (const base of ["root", "subpath"]) {
  test(`${base}: personal work and saved searches preserve ownership and explicit execution`, async ({
    page,
    catalog,
  }, testInfo) => {
    const draft = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/resources",
      {
        namespaceId: catalog.receipt.namespaceId,
        canonicalIdentifier:
          "urn:air:demo.catalog.example.com:demo:draft-helper",
        kind: "agent",
      },
      undefined,
      "local-contributor",
    );
    await login(page, catalog, "local-contributor", "/my-work");
    const basePath = catalog.basePath;
    const origin = catalog.origin;
    // The login helper selects its base from test title. Account routes survive reload.
    await expect(
      page.getByRole("heading", { name: "My work", exact: true }),
    ).toBeVisible();
    await page.reload();
    await expect(
      page.getByRole("heading", { name: "My work", exact: true }),
    ).toBeVisible();
    await expect(
      page
        .getByRole("row")
        .filter({
          hasText: "urn:air:demo.catalog.example.com:demo:draft-helper",
        })
        .getByRole("link", { name: "Open resource", exact: true }),
    ).toBeVisible();
    await expect(
      page
        .getByRole("row")
        .filter({
          hasText: "urn:air:demo.catalog.example.com:demo:draft-helper",
        })
        .getByRole("link", { name: "Open resource", exact: true }),
    ).toHaveAttribute("href", basePath + "/resources/" + draft.data.id);
    await page.screenshot({
      path: testInfo.outputPath("my-work.png"),
      fullPage: true,
    });
    await page
      .getByRole("combobox", { name: "Work queue", exact: true })
      .selectOption("approved");
    await expect(
      page.getByText("No approved work without a release.", { exact: true }),
    ).toBeVisible();
    const searches: string[] = [];
    const suggestions: string[] = [];
    page.on("request", (r) => {
      if (new URL(r.url()).pathname.endsWith("/api/v1/catalog/search"))
        searches.push(r.postData() ?? "");
      if (new URL(r.url()).pathname.endsWith("/api/v1/catalog/explore"))
        suggestions.push(r.postData() ?? "");
    });
    await page.goto(origin + basePath + "/saved-items/new");
    await page
      .getByRole("textbox", { name: "Saved label", exact: true })
      .fill("Research helpers");
    await page
      .getByRole("textbox", { name: "Saved search text", exact: true })
      .fill("support helper");
    await page.getByText("More saved filters", { exact: true }).click();
    await page
      .getByRole("checkbox", { name: "Specify origins", exact: true })
      .check();
    const savedReply = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        new URL(r.url()).pathname.endsWith("/_catalog/api/v1/personal/items"),
    );
    await page
      .getByRole("button", { name: "Save named search", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const response = await savedReply;
    expect(response.status()).toBe(201);
    const saved = (await response.json()) as { id: string; revision: number };
    expect(saved.revision).toBe(1);
    await page
      .getByRole("link", { name: "Open saved item", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Research helpers", exact: true }),
    ).toBeVisible();
    await page.reload();
    await expect(
      page.getByRole("textbox", { name: "Saved label", exact: true }),
    ).toHaveValue("Research helpers");
    await page.screenshot({
      path: testInfo.outputPath("saved-search.png"),
      fullPage: true,
    });
    expect(searches).toEqual([]);
    expect(suggestions).toEqual([]);
    await page
      .getByRole("button", { name: "Open search editor", exact: true })
      .click();
    await expect(page.getByRole("searchbox")).toHaveValue("support helper");
    await expect(page.locator("h1")).toHaveCount(1);
    await page.getByRole("button", { name: "Categories", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Add category", exact: true }),
    ).toBeVisible();
    expect(suggestions).toEqual([]);
    await page
      .getByRole("button", { name: "Close filters", exact: true })
      .click();
    expect(searches).toEqual([]);
    expect(new URL(page.url()).search).toBe("");
    expect(
      await page.evaluate(() => JSON.stringify(history.state)),
    ).not.toContain("support helper");
    await page
      .getByRole("checkbox", { name: /configured embedding provider/ })
      .check();
    expect(suggestions).toEqual([]);
    expect(searches).toEqual([]);
    await page.getByRole("button", { name: "Categories", exact: true }).click();
    await expect(
      page.getByText(
        "Local Catalog matches for the applied search. External results are not counted.",
        { exact: true },
      ),
    ).toBeVisible();
    await expect.poll(() => suggestions.length).toBe(1);
    expect(JSON.parse(suggestions[0]!)).toMatchObject({
      query: { text: "support helper" },
    });
    await page
      .getByRole("button", { name: "Close filters", exact: true })
      .click();
    // Fit the native input's code-unit limit while exceeding the UTF-8 byte budget.
    await page.getByRole("searchbox").fill("🙂".repeat(126));
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("500");
    expect(searches).toEqual([]);
    await page.getByRole("searchbox").fill("support helper");
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect.poll(() => searches.length).toBe(1);
    await page.getByText("Save this named search", { exact: true }).click();
    await page.setViewportSize({ width: 320, height: 900 });
    const toolbar = page.locator("details[open]").filter({
      has: page.getByText("Save this named search", { exact: true }),
    });
    const toolbarLabel = toolbar.getByRole("textbox", {
      name: "Saved label",
      exact: true,
    });
    await toolbarLabel.fill("Search toolbar copy");
    const labelBounds = await toolbarLabel.boundingBox();
    expect(labelBounds).not.toBeNull();
    expect(labelBounds!.x).toBeGreaterThanOrEqual(0);
    expect(labelBounds!.x + labelBounds!.width).toBeLessThanOrEqual(320);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
    const toolbarSave = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        new URL(r.url()).pathname.endsWith("/_catalog/api/v1/personal/items"),
    );
    await toolbar
      .getByRole("button", { name: "Save named search", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const toolbarResponse = await toolbarSave;
    expect(toolbarResponse.status()).toBe(201);
    expect(JSON.parse(toolbarResponse.request().postData()!)).toMatchObject({
      type: "search",
      label: "Search toolbar copy",
      search: { text: "support helper", origins: ["local"] },
    });
    await expect(
      toolbar.getByText("Private item saved.", { exact: true }),
    ).toBeVisible();
    expect(searches).toHaveLength(1);
    await page.setViewportSize({ width: 1440, height: 900 });
    // A new identity gets its own library. The route never reuses a previous result.
    await page.getByLabel("Account", { exact: true }).click();
    await page.getByRole("button", { name: "Sign out", exact: true }).click();
    await page.locator('[data-login-alias="local-admin"]').click();
    await page.goto(origin + basePath + "/saved-items");
    await expect(
      page.getByText(
        "Your library is empty. Save a resource from Catalog or create a named search.",
        { exact: true },
      ),
    ).toBeVisible();
    await page.getByLabel("Account", { exact: true }).click();
    await page.getByRole("button", { name: "Sign out", exact: true }).click();
    await page.locator('[data-login-alias="local-contributor"]').click();
    await page.goto(origin + basePath + "/saved-items/" + saved.id);
    await expect(
      page.getByRole("heading", { name: "Research helpers", exact: true }),
    ).toBeVisible();
  });
}
