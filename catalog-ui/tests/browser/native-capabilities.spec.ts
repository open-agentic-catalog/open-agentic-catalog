import { test, expect, login } from "./fixtures";
import { agentDocument, publishFixture } from "./published-fixture";
import { object } from "./json";

for (const base of ["root", "subpath"]) {
  test(`${base}: private declared capabilities retain false through filters, facets and saved restore`, async ({
    page,
    catalog,
  }, testInfo) => {
    for (const [index, streaming] of [false, true, undefined].entries())
      await publishFixture(catalog, 180 + index, "", {
        nativeDocument: {
          ...agentDocument,
          capabilities: {
            ...object(agentDocument.capabilities),
            ...(streaming === undefined ? {} : { streaming }),
          },
        },
      });
    await login(page, catalog, "local-admin");
    await expect(page.locator("article.resource-card")).toHaveCount(6);
    const searches: Record<string, unknown>[] = [];
    page.on("request", (request) => {
      if (
        request.method() === "POST" &&
        request.url().endsWith("/api/v1/catalog/search")
      )
        searches.push(object(request.postDataJSON()));
    });
    await page.getByRole("button", { name: /^Filters/ }).click();
    await page.getByLabel("Add tag", { exact: true }).fill("paged");
    await page.getByText("Agent declarations", { exact: true }).click();
    await page
      .getByLabel("Agent streaming declaration", { exact: true })
      .selectOption("false");
    await expect(
      page.getByRole("checkbox", { name: "Live external", exact: true }),
    ).toBeDisabled();
    expect(searches).toEqual([]);
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    await expect(page.locator("article.resource-card")).toHaveCount(1);
    expect(searches.at(-1)).toMatchObject({
      nativeCapabilities: { agentStreaming: false },
      origins: ["local"],
      tags: ["paged"],
    });
    await expect(
      page.getByRole("button", {
        name: "Remove declared capability Agent streaming",
        exact: true,
      }),
    ).toContainText("false");
    const facets = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().endsWith("/api/v1/catalog/explore"),
    );
    await page.getByRole("button", { name: "Categories", exact: true }).click();
    const reply = await facets;
    expect(reply.status()).toBe(200);
    expect(object(await reply.json()).matched).toBe(1);
    await page
      .getByRole("button", { name: "Close filters", exact: true })
      .click();
    await page.getByText("Save this named search", { exact: true }).click();
    await page
      .getByRole("textbox", { name: "Saved label", exact: true })
      .fill("Nonstreaming Agents");
    const saved = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().endsWith("/api/v1/personal/items"),
    );
    await page
      .getByRole("button", { name: "Save named search", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const stored = await saved;
    expect(stored.status()).toBe(201);
    expect(object(stored.request().postDataJSON()).search).toMatchObject({
      nativeCapabilities: { agentStreaming: false },
      origins: ["local"],
    });
    await page
      .getByRole("link", { name: "Open saved item", exact: true })
      .click();
    await page.reload();
    await page.getByText("More saved filters", { exact: true }).click();
    await page.getByText("Agent declarations", { exact: true }).click();
    await expect(
      page.getByLabel("Agent streaming declaration", { exact: true }),
    ).toHaveValue("false");
    const count = searches.length;
    await page
      .getByRole("button", { name: "Open search editor", exact: true })
      .click();
    expect(searches).toHaveLength(count);
    await page
      .getByRole("checkbox", { name: /configured embedding provider/ })
      .check();
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(page.locator("article.resource-card")).toHaveCount(1);
    await page.setViewportSize({ width: 375, height: 900 });
    await page.getByRole("button", { name: /^Filters/ }).click();
    await page.getByText("Agent declarations", { exact: true }).click();
    const streaming = page.getByLabel("Agent streaming declaration", {
      exact: true,
    });
    await streaming.scrollIntoViewIfNeeded();
    const box = await streaming.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(375);
    await page.screenshot({
      path: testInfo.outputPath("native-capabilities-mobile.png"),
      fullPage: true,
    });
    await page
      .getByRole("button", { name: "Clear declared capabilities", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    await expect(page.locator("article.resource-card")).toHaveCount(3);
    expect(searches.at(-1)).not.toHaveProperty("nativeCapabilities");
  });
}
