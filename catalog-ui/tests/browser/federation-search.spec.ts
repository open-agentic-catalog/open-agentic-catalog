import { test, expect, login, signOut } from "./fixtures";

// Fixed owned transport controls qualify UI behavior, not peer ingestion or availability.
test("subpath: federation investigation preserves paged source choices and exact live recovery", async ({
  page,
  catalog,
}, testInfo) => {
  const firstID = "00000000-0000-7000-8000-000000000001";
  const secondID = "00000000-0000-7000-8000-000000000002";
  const source = {
    id: firstID,
    displayName: "Configured rendering peer",
    endpoint: "https://peer.invalid/ard/",
    status: "active",
    profileId: "ard",
    retrievalMode: "hybrid",
    updateMode: "automatic",
    scope: {},
    live: { enabled: true },
    nesting: {},
    referrals: {},
    revision: 1,
  };
  let sourceReads = 0;
  const indexedBodies: unknown[] = [];
  const liveCommands: { body: unknown; key: string | undefined }[] = [];
  const hostile = '<img src=x onerror="alert(1)">';
  await page.route(
    "**/_catalog/api/v1/federation/sources?**",
    async (route) => {
      sourceReads++;
      const query = new URL(route.request().url()).searchParams;
      expect(query.get("limit")).toBe("25");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json:
          sourceReads === 1
            ? { items: [{}] }
            : query.has("cursor")
              ? {
                  items: [
                    {
                      ...source,
                      id: secondID,
                      displayName: "Second peer",
                      status: "paused",
                      live: { enabled: false },
                    },
                  ],
                }
              : { items: [source], nextCursor: "source-page-two" },
      });
    },
  );
  await page.route("**/_catalog/api/v1/observations:search", async (route) => {
    indexedBodies.push(route.request().postDataJSON());
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      json: {
        items: [
          {
            observation: {
              id: firstID,
              tenantId: firstID,
              sourceId: firstID,
              sourceKey: "tools",
              identifier: "urn:air:peer.invalid:tools:helper",
              versionPresent: true,
              version: "",
              kind: "plugin",
              completeness: "projection",
              freshness: "stale",
              compatibility: {
                structural: "valid",
                support: "unknown",
                profileId: "future",
              },
              rawEntryAvailable: false,
              exactEntryDigest: "sha256:" + "a".repeat(64),
              identityDigest: "sha256:" + "b".repeat(64),
              firstSeenAt: "2026-09-01T12:00:00Z",
              lastSeenAt: "2026-09-01T12:00:00Z",
            },
            score: 73,
            source: firstID,
          },
        ],
      },
    });
  });
  await page.route(
    "**/_catalog/api/v1/federation/sources:live-search",
    async (route) => {
      liveCommands.push({
        body: route.request().postDataJSON(),
        key: route.request().headers()["idempotency-key"],
      });
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json:
          liveCommands.length === 1
            ? { results: [{}], partial: false }
            : {
                results: [
                  {
                    identifier: "urn:air:peer.invalid:tools:live",
                    sourceRevision: 1,
                    exactEntryDigest: `sha256:${"a".repeat(64)}`,
                    versionPresent: true,
                    score: 91,
                    sourceId: firstID,
                    source: "https://peer.invalid/ard/",
                    entry: { type: hostile, future: { untouched: true } },
                  },
                ],
                partial: true,
                diagnostics: [
                  { sourceId: secondID, status: "policy_stale" },
                  { status: "continuation_unavailable" },
                ],
              },
      });
    },
  );
  await login(page, catalog, "local-reader", "/federation-search");
  await expect(page.getByRole("alert")).toHaveText(
    "The source.read and federation.discover permissions are required. When scopes are enforced, sources.read is also required.",
  );
  await expect(
    page.getByRole("textbox", { name: "Query", exact: true }),
  ).toHaveCount(0);
  expect(sourceReads).toBe(0);
  expect(indexedBodies).toEqual([]);
  expect(liveCommands).toEqual([]);
  await signOut(page);
  await login(page, catalog, "local-source", "/federation-search");
  await page.setViewportSize({ width: 390, height: 844 });
  expect(sourceReads).toBe(0);
  expect(indexedBodies).toEqual([]);
  expect(liveCommands).toEqual([]);
  await expect(
    page.getByText("Enter a query to run a live diagnostic query.", {
      exact: true,
    }),
  ).toBeVisible();
  expect(liveCommands).toEqual([]);
  await page.getByRole("textbox", { name: "Query", exact: true }).fill("tools");
  await page
    .getByRole("button", { name: "Search indexed observations", exact: true })
    .click();
  const indexed = page.getByRole("region", {
    name: "Indexed diagnostic results",
  });
  await expect(
    indexed.getByRole("link", {
      name: "urn:air:peer.invalid:tools:helper",
      exact: true,
    }),
  ).toHaveAttribute("href", `${catalog.basePath}/observations/${firstID}`);
  await expect(indexed).toContainText("Version: (empty)");
  await expect(indexed).toContainText("Plugin");
  await expect(indexed).toContainText("stale");
  await expect(indexed.getByText("Unknown", { exact: true })).toHaveAttribute(
    "data-tone",
    "neutral",
  );
  await expect(
    indexed.getByText("Projection", { exact: true }),
  ).toHaveAttribute("data-tone", "warning");
  await expect(
    indexed.getByRole("link", { name: firstID, exact: true }),
  ).toHaveAttribute("href", `${catalog.basePath}/sources/${firstID}`);
  await page
    .getByRole("button", { name: "Choose configured sources", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText("could not be completed");
  expect(liveCommands).toEqual([]);
  await page.getByRole("button", { name: "Reload", exact: true }).click();
  await page
    .getByRole("checkbox", { name: /Configured rendering peer/ })
    .press("Space");
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await page
    .getByRole("checkbox", { name: /Second peer.*paused.*disabled/ })
    .press("Space");
  const ids = page.getByRole("textbox", {
    name: "Source IDs (optional)",
    exact: true,
  });
  await expect(ids).toHaveValue(`${firstID}, ${secondID}`);
  expect(sourceReads).toBe(3);
  expect(indexedBodies).toHaveLength(1);
  expect(liveCommands).toEqual([]);
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Review declared metadata");
  await page
    .getByRole("button", { name: "Query live sources", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText(
    "could not be completed",
  );
  await expect(
    page.getByRole("region", { name: "Live diagnostic results" }),
  ).toHaveCount(0);
  expect(liveCommands).toHaveLength(1);
  await dialog
    .getByRole("button", { name: "Retry same command", exact: true })
    .click();
  await expect(dialog).toHaveCount(0);
  const live = page.getByRole("region", { name: "Live diagnostic results" });
  await expect(live).toContainText("Version: (empty)");
  await expect(live.getByText(hostile, { exact: true })).toBeVisible();
  await expect(live.locator("img")).toHaveCount(0);
  await expect(live).toContainText(
    "Source policy evidence is no longer current.",
  );
  await expect(live).toContainText(
    "The remaining upstream page was not followed",
  );
  await expect(live).toContainText("No source attribution");
  await expect(live.getByRole("link", { name: /record|adopt/i })).toHaveCount(
    0,
  );
  expect(liveCommands[0]?.key).toBeTruthy();
  expect(liveCommands[1]).toEqual(liveCommands[0]);
  expect(liveCommands[0]?.body).toEqual({
    text: "tools",
    reason: "Review declared metadata",
    sourceIds: [firstID, secondID],
  });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  expect(sourceReads).toBe(3);
  expect(indexedBodies).toHaveLength(1);
  await page.setViewportSize({ width: 390, height: 1200 });
  await live.evaluate((element) => element.scrollIntoView({ block: "center" }));
  await live.screenshot({
    path: testInfo.outputPath("live-diagnostics-narrow.png"),
  });
  await page
    .getByRole("region", { name: "Configured source picker" })
    .screenshot({ path: testInfo.outputPath("source-picker-narrow.png") });
});
