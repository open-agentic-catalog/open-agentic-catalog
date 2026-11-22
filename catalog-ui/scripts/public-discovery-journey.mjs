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
  const observe = (request) => requests.push(request);
  try {
    for (const anonymous of [false, true]) {
      if (anonymous) {
        // Leave the private page before observing requests from the anonymous visit.
        await page.goto("about:blank");
        await context.clearCookies();
      }
      requests.length = 0;
      page.on("request", observe);
      await browseAndSearch(page, origin);
      if (anonymous) {
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
      }
      page.off("request", observe);
      expect(
        requests.some((request) =>
          /\/auth\/|\/_catalog\//.test(new URL(request.url()).pathname),
        ),
      ).toBe(false);
      const discovery = origin + "/_discovery/";
      const operations = new Set();
      for (const request of requests) {
        if (!request.url().startsWith(discovery)) continue;
        const operation = request.url().slice(discovery.length).split("?")[0];
        operations.add(request.method() + " " + operation);
        const headers = await request.allHeaders();
        expect(Object.hasOwn(headers, "cookie")).toBe(false);
        expect(Object.hasOwn(headers, "authorization")).toBe(false);
      }
      expect(operations).toEqual(
        new Set(["GET manifest", "GET agents", "POST search"]),
      );
      if (!anonymous) {
        // The same browser must still send its session on a real private read.
        const [session] = await Promise.all([
          page.waitForResponse(origin + "/auth/session"),
          page.getByRole("link", { name: "Sign in", exact: true }).click(),
        ]);
        expect(session.status()).toBe(200);
        expect(Boolean(await session.request().headerValue("cookie"))).toBe(
          true,
        );
        await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
      }
    }
  } finally {
    page.off("request", observe);
    await context.addCookies(cookies);
    await page.goto(origin + "/catalog");
  }
}

async function browseAndSearch(page, origin) {
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
}
