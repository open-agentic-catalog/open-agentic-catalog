import { test, expect, login } from "./fixtures";
import { agentDocument, publishFixture } from "./published-fixture";
import { object } from "./json";

for (const base of ["root", "subpath"]) {
  test(`${base}: declared Agent protocols survive Apply, categories and saved searches`, async ({
    page,
    catalog,
  }, testInfo) => {
    const bindings = ["HTTP+JSON", "JSONRPC", "HTTP+JSON", "Private+V1 "];
    for (const [index, binding] of bindings.entries())
      await publishFixture(catalog, 80 + index, "", {
        nativeDocument: {
          ...agentDocument,
          supportedInterfaces: [
            {
              url: "https://inert.example/agent",
              protocolBinding: binding,
              protocolVersion: "1.0",
            },
          ],
        },
      });
    await login(page, catalog, "local-admin");
    await expect(page.locator("article.resource-card")).toHaveCount(7);
    const searches: Record<string, unknown>[] = [];
    page.on("request", (request) => {
      if (
        request.method() === "POST" &&
        request.url().endsWith("/api/v1/catalog/search")
      )
        searches.push(object(request.postDataJSON()));
    });
    const filters = page.getByRole("button", { name: /^Filters/ });
    await filters.click();
    await page.getByLabel("Add tag", { exact: true }).fill("paged");
    const protocol = page.getByLabel("Agent protocol binding", { exact: true });
    await protocol.fill("HTTP+JSON");
    await expect(
      page.getByRole("checkbox", { name: "Local Catalog", exact: true }),
    ).toBeChecked();
    await expect(
      page.getByRole("checkbox", { name: "Live external", exact: true }),
    ).toBeDisabled();
    expect(searches).toEqual([]);
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    await expect(page.locator("article.resource-card")).toHaveCount(2);
    expect(searches.at(-1)).toMatchObject({
      agentProtocol: "HTTP+JSON",
      origins: ["local"],
      tags: ["paged"],
    });
    const chip = page.getByRole("button", {
      name: "Remove declared A2A protocol: HTTP+JSON",
      exact: true,
    });
    await expect(chip).toBeVisible();
    const facets = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().endsWith("/api/v1/catalog/explore"),
    );
    await page.getByRole("button", { name: "Categories", exact: true }).click();
    const counted = await facets;
    expect(counted.status()).toBe(200);
    expect(object(counted.request().postDataJSON()).query).toMatchObject({
      agentProtocol: "HTTP+JSON",
      tags: ["paged"],
    });
    expect(object(await counted.json()).matched).toBe(2);
    await page
      .getByRole("button", { name: "Close filters", exact: true })
      .click();
    await page.getByText("Save this named search", { exact: true }).click();
    await page
      .getByRole("textbox", { name: "Saved label", exact: true })
      .fill("Declared HTTP Agents");
    const savedReply = page.waitForResponse(
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
    const saved = await savedReply;
    expect(saved.status()).toBe(201);
    expect(object(saved.request().postDataJSON()).search).toMatchObject({
      agentProtocol: "HTTP+JSON",
      origins: ["local"],
    });
    await page
      .getByRole("link", { name: "Open saved item", exact: true })
      .click();
    await page.reload();
    await page.getByText("More saved filters", { exact: true }).click();
    await expect(
      page.getByRole("textbox", {
        name: "Saved Agent protocol binding",
        exact: true,
      }),
    ).toHaveValue("HTTP+JSON");
    const before = searches.length;
    await page
      .getByRole("button", { name: "Open search editor", exact: true })
      .click();
    expect(searches).toHaveLength(before);
    await page
      .getByRole("checkbox", { name: /configured embedding provider/ })
      .check();
    expect(searches).toHaveLength(before);
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(page.locator("article.resource-card")).toHaveCount(2);
    expect(searches.at(-1)).toMatchObject({
      agentProtocol: "HTTP+JSON",
      origins: ["local"],
    });
    await filters.click();
    await page.setViewportSize({ width: 375, height: 900 });
    await protocol.fill("Private+V1 ");
    await protocol.scrollIntoViewIfNeeded();
    const box = await protocol.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(375);
    const namespaceBox = await page
      .getByLabel("Exact namespace ID", { exact: true })
      .boundingBox();
    expect(namespaceBox).not.toBeNull();
    expect(namespaceBox!.x).toBeCloseTo(box!.x, 0);
    expect(namespaceBox!.width).toBeCloseTo(box!.width, 0);
    await page.screenshot({
      path: testInfo.outputPath("agent-protocol-mobile.png"),
    });
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    await expect(page.locator("article.resource-card")).toHaveCount(1);
    expect(searches.at(-1)).toMatchObject({
      agentProtocol: "Private+V1 ",
      origins: ["local"],
    });
    await page
      .getByRole("button", {
        name: "Remove declared A2A protocol: Private+V1 ",
        exact: true,
      })
      .click();
    await expect(page.locator("article.resource-card")).toHaveCount(4);
    expect(searches.at(-1)).not.toHaveProperty("agentProtocol");
    await filters.click();
    await page
      .getByRole("button", { name: "Clear filters", exact: true })
      .click();
    // Clearing all filters restores the default origins. Select local again
    // before executing; this journey gives no new live-disclosure consent.
    await page
      .getByRole("checkbox", { name: "Indexed external", exact: true })
      .uncheck();
    await page
      .getByRole("checkbox", { name: "Live external", exact: true })
      .uncheck();
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    await expect(page.locator("article.resource-card")).toHaveCount(7);
    expect(searches.at(-1)).not.toHaveProperty("tags");
    expect(searches.at(-1)).not.toHaveProperty("agentProtocol");
  });
}
