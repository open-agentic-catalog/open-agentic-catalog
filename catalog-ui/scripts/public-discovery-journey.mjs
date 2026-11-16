// Deployed-image public reads use actual PostgreSQL publications. Setup runs separately.
import { expect } from "@playwright/test";
export async function publicDiscoveryJourney({
  page,
  origin,
  network,
  command,
}) {
  const activate = async () => {
    const candidate = await command("POST", "/api/v1/publication/snapshots", {
      host: "api",
    });
    await command(
      "POST",
      `/api/v1/publication/snapshots/${candidate.data.id}:activate`,
      { host: "api", reason: "Owned public browser qualification" },
      candidate.etag,
    );
  };
  await activate();
  const context = page.context();
  const cookies = await context.cookies();
  const requests = [];
  const observe = (request) =>
    requests.push({
      path: new URL(request.url()).pathname,
      headers: request.headers(),
    });
  try {
    await context.clearCookies();
    page.on("request", observe);
    await page.goto(origin + "/discover");
    await expect(
      page.getByRole("heading", { name: "Public discovery", exact: true }),
    ).toBeVisible();
    await expect(page.locator("article.public-entry")).toHaveCount(4);
    await expect(page.locator("article.public-entry")).toContainText([
      "Registry metadata",
    ]);
    const first = page.locator("article.public-entry").first();
    await first.getByText("Inspect returned metadata", { exact: true }).click();
    await expect(first.locator("dt", { hasText: "Identifier" })).toBeVisible();
    await page.getByRole("searchbox").fill("order");
    await expect(
      page.getByRole("button", { name: "Search", exact: true }),
    ).toBeDisabled();
    await page.getByRole("checkbox", { name: /embedding provider/ }).check();
    const searched = page.waitForResponse((response) =>
      response.url().endsWith("/_discovery/search"),
    );
    await page.getByRole("button", { name: "Search", exact: true }).click();
    expect((await searched).status()).toBe(200);
    // The minimal dataset publishes one Agent, MCP Server and inline Skill.
    // All three match "order"; the Registry self-entry does not.
    await expect(page.locator("article.public-entry")).toHaveCount(3);

    await expect(
      page.getByRole("button", { name: "Browse directory", exact: true }),
    ).toBeEnabled();
    await page
      .getByRole("button", { name: "Browse directory", exact: true })
      .click();
    await expect(page.locator("article.public-entry")).toHaveCount(4);
    await command("POST", "/api/v1/publication:unpublish", {
      host: "api",
      reason: "Owned public browser cleanup",
    });
    network.expectFailure("GET", "/_discovery/manifest", 404);
    await page
      .getByRole("button", { name: "Refresh publication", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText(
      "Public discovery is unavailable",
    );
    await expect(page.locator("article.public-entry")).toHaveCount(0);
    expect(
      requests.some((request) => /\/auth\/|\/_catalog\//.test(request.path)),
    ).toBe(false);
    for (const request of requests.filter((request) =>
      request.path.includes("/_discovery/"),
    )) {
      expect(request.headers.authorization).toBeUndefined();
      expect(request.headers.cookie).toBeUndefined();
    }
  } finally {
    page.off("request", observe);
    await context.addCookies(cookies);
    await page.goto(origin + "/catalog");
  }
}
