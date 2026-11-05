import { test, expect, login } from "./fixtures";
import { object } from "./json";
import { publishFixture } from "./published-fixture";

for (const variant of [
  {
    title: "root absent version and opinion",
    present: false,
    artwork: false,
    saved: false,
  },
  {
    title: "subpath empty version and artwork",
    present: true,
    artwork: true,
    saved: false,
  },
  {
    title: "root saved absent version and opinion",
    present: false,
    artwork: false,
    saved: true,
  },
  {
    title: "subpath saved empty version and artwork",
    present: true,
    artwork: true,
    saved: true,
  },
]) {
  test(`${variant.title}: indexed detail retains search and guards drafts through Back/Forward`, async ({
    page,
    catalog,
    network,
  }) => {
    const sourceId = "00000000-0000-7000-8000-000000000188";
    const external = {
      id: "indexed-context",
      type: "indexed",
      identifier: "urn:air:context.example:agent:one",
      kind: "agent",
      displayName: "External context fixture",
      versionPresent: variant.present,
      ...(variant.present ? { version: "" } : {}),
      tags: [],
      indexed: {
        observationGroupId: "observation-group-v1:sha256:" + "a".repeat(64),
        normalizedEntryDigest: "sha256:" + "b".repeat(64),
        sourceIds: [sourceId],
        sourceCount: 1,
        sourcesTruncated: false,
        conflict: false,
        sameSourceConflict: false,
        presentationSourceId: sourceId,
        presentationGeneration: 1,
      },
    };
    let available = true;
    const queries: Record<string, string>[] = [];
    let artworkWrites = 0;
    page.on("request", (request) => {
      if (
        request.method() === "POST" &&
        new URL(request.url()).pathname.endsWith("/artwork")
      )
        artworkWrites++;
    });
    await page.route("**/_catalog/api/v1/catalog/search", async (route) => {
      const response = await route.fetch();
      const data = object(await response.json());
      delete data.nextCursor;
      await route.fulfill({
        response,
        json: {
          ...data,
          items: [
            external,
            ...Array.from({ length: 11 }, (_, index) => ({
              ...external,
              id: `indexed-context-${index}`,
              identifier: `${external.identifier}-${index}`,
              displayName: `${external.displayName} ${index}`,
            })),
          ],
          durableTotal: 12,
          liveCount: 0,
          partial: false,
        },
      });
    });
    await page.route(
      "**/_catalog/api/v1/catalog/indexed-record?*",
      async (route) => {
        queries.push(
          Object.fromEntries(new URL(route.request().url()).searchParams),
        );
        const { version: _empty, ...encoded } = external;
        await route.fulfill(
          available
            ? { json: encoded }
            : {
                status: 404,
                contentType: "application/problem+json",
                json: {
                  type: "about:blank",
                  title: "Not found",
                  status: 404,
                  code: "NOT_FOUND",
                },
              },
        );
      },
    );
    await page.route("**/_catalog/api/v1/ratings/read", async (route) => {
      await route.fulfill({
        json: {
          target: object(route.request().postDataJSON()).target,
          count: 0,
          changedEntry: false,
        },
      });
    });
    await page.route(
      `**/_catalog/api/v1/federation/sources/${sourceId}/artwork?*`,
      async (route) => {
        const target = Object.fromEntries(
          new URL(route.request().url()).searchParams,
        );
        await route.fulfill({
          headers: { ETag: '"rev-1"' },
          json: {
            id: "00000000-0000-7000-8000-000000000189",
            revision: 1,
            target: {
              ...target,
              sourceId,
              presentationGeneration: 1,
              versionPresent: variant.present,
            },
            icon: { mode: "inherit" },
            screenshots: { mode: "inherit" },
            reason: "Synthetic prior choice",
            createdAt: "2026-10-01T12:00:00Z",
          },
        });
      },
    );
    await page.setViewportSize({ width: 1280, height: 700 });
    if (variant.saved) {
      const saved = await catalog.admin<{ id: string }>(
        "POST",
        "/api/v1/personal/items",
        {
          type: "search",
          label: "Indexed context",
          search: { text: "", origins: ["indexed"] },
        },
      );
      await login(
        page,
        catalog,
        "local-admin",
        "/saved-items/" + saved.data.id,
      );
      await page
        .getByRole("button", { name: "Open search editor", exact: true })
        .click();
      await page
        .getByRole("checkbox", { name: /configured embedding provider/ })
        .check();
    } else await login(page, catalog, "local-admin");
    await page
      .getByRole("searchbox", { name: "Search Catalog", exact: true })
      .fill("submitted text");
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(page.locator("article.resource-card")).toHaveCount(12);
    await page
      .getByRole("searchbox", { name: "Search Catalog", exact: true })
      .fill("unsubmitted text");
    await page.getByRole("button", { name: "Table", exact: true }).click();
    await page.evaluate(() => window.scrollTo(0, 80));
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(80);
    await page
      .getByRole("button", { name: external.displayName, exact: true })
      .click();
    const originalScroll = await page.evaluate(() => window.scrollY);
    expect(originalScroll).toBeGreaterThan(0);
    await page
      .getByRole("link", { name: "Open current indexed record", exact: true })
      .click();
    await expect(
      page.getByRole("heading", {
        name: "External catalog record",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: external.displayName, exact: true }),
    ).toBeVisible();
    expect(queries).toEqual([
      {
        identifier: external.identifier,
        versionPresent: String(variant.present),
        ...(variant.present ? { version: "" } : {}),
      },
    ]);
    if (variant.artwork) {
      await page
        .getByRole("button", { name: "Manage local artwork", exact: true })
        .click();
      await page
        .getByRole("textbox", { name: "Artwork override reason", exact: true })
        .fill("Keep this artwork draft");
    } else {
      await page
        .getByRole("button", { name: "Ratings for this version", exact: true })
        .click();
      await page.getByRole("radio", { name: "4 stars", exact: true }).check();
    }
    page.once("dialog", (dialog) => dialog.dismiss());
    await page
      .getByRole("link", {
        name: variant.saved ? "Back to saved search" : "Back to Catalog",
        exact: true,
      })
      .click();
    await expect(page).toHaveURL(new RegExp("/indexed-record\\?"));
    if (variant.artwork)
      await expect(
        page.getByRole("textbox", {
          name: "Artwork override reason",
          exact: true,
        }),
      ).toHaveValue("Keep this artwork draft");
    else
      await expect(
        page.getByRole("radio", { name: "4 stars", exact: true }),
      ).toBeChecked();
    page.once("dialog", (dialog) => dialog.accept());
    await page.goBack();
    await expect(
      page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
    ).toHaveValue("unsubmitted text");
    await expect(
      page.getByRole("button", { name: "Table", exact: true }),
    ).toHaveAttribute("aria-pressed", "true");
    expect(
      await page.evaluate(() => JSON.stringify(history.state)),
    ).not.toMatch(/submitted/);
    await expect
      .poll(() => page.evaluate(() => window.scrollY))
      .toBe(originalScroll);
    const updatedScroll = await page.evaluate(() => {
      const next = Math.min(
        window.scrollY + 80,
        document.documentElement.scrollHeight - innerHeight,
      );
      window.scrollTo(0, next);
      return next;
    });
    expect(updatedScroll).toBeGreaterThan(originalScroll);
    await expect
      .poll(() => page.evaluate(() => window.scrollY))
      .toBe(updatedScroll);
    await page.goForward();
    await expect(
      page.getByRole("heading", { name: external.displayName, exact: true }),
    ).toBeVisible();
    expect(queries).toHaveLength(2);
    await page.goBack();
    await expect(
      page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
    ).toBeVisible();
    await expect
      .poll(() => page.evaluate(() => window.scrollY))
      .toBe(updatedScroll);
    available = false;
    network.expectFailure(
      "GET",
      "/_catalog/api/v1/catalog/indexed-record",
      404,
    );
    await page.goForward();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(
      page.getByRole("heading", { name: external.displayName, exact: true }),
    ).toHaveCount(0);
    expect(queries).toHaveLength(3);
    expect(artworkWrites).toBe(0);
    available = true;
    await page.reload();
    await page
      .getByRole("link", { name: "Back to Catalog", exact: true })
      .click();
    await expect(
      page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
    ).toHaveValue("");
    await expect(
      page.getByRole("button", { name: "Cards", exact: true }),
    ).toHaveAttribute("aria-pressed", "true");
  });
}

test("subpath: Explore retains both mode contexts and transfers applied local conjunctions to Catalog", async ({
  page,
  catalog,
}) => {
  await publishFixture(catalog, 188);
  await login(page, catalog, "local-reader", "/explore");
  await page
    .getByRole("combobox", { name: "Kind", exact: true })
    .selectOption("agent");
  await page
    .getByRole("button", { name: "Load namespaces", exact: true })
    .click();
  await page
    .getByRole("combobox", { name: "Local namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await expect(page.locator("tbody tr")).toHaveCount(2);
  await page
    .getByRole("link", { name: "Paged agent 188", exact: true })
    .click();
  await page.getByRole("link", { name: /Back to Explore/ }).click();
  await expect(
    page.getByRole("combobox", { name: "Kind", exact: true }),
  ).toHaveValue("agent");
  await expect(
    page.getByRole("combobox", { name: "Local namespace", exact: true }),
  ).toHaveValue(catalog.receipt.namespaceId);
  await page
    .getByRole("button", { name: "Use these filters for counts", exact: true })
    .click();
  await expect(
    page.getByRole("status", { name: "Matched releases", exact: true }),
  ).toHaveText("2 matching local releases");
  const tag = "paged";
  const category = "testing";
  await page
    .getByRole("button", { name: `Filter by tag ${tag}`, exact: true })
    .click();
  await page
    .getByRole("button", {
      name: `Filter by category ${category}`,
      exact: true,
    })
    .click();
  await page
    .getByRole("textbox", { name: "Search local metadata", exact: true })
    .fill("Unsubmitted counts draft");
  await page
    .getByRole("button", { name: "Browse releases", exact: true })
    .click();
  await expect(
    page.getByRole("combobox", { name: "Kind", exact: true }),
  ).toHaveValue("agent");
  await page.getByRole("button", { name: "View counts", exact: true }).click();
  await expect(
    page.getByRole("textbox", { name: "Search local metadata", exact: true }),
  ).toHaveValue("Unsubmitted counts draft");
  await expect(
    page.getByRole("button", {
      name: "Use these filters for browse",
      exact: true,
    }),
  ).toBeDisabled();
  const search = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/catalog/search"),
  );
  await page
    .getByRole("button", { name: "Search local Catalog", exact: true })
    .click();
  expect((await search).request().postDataJSON()).toEqual({
    kinds: ["agent"],
    namespaceId: catalog.receipt.namespaceId,
    tags: [tag],
    categories: [category],
    origins: ["local"],
    limit: 24,
  });
  await expect(page.locator("article.resource-card")).toHaveCount(1);
  expect(new URL(page.url()).search).toBe("");
  expect(
    await page.evaluate(() => JSON.stringify(history.state)),
  ).not.toContain("Unsubmitted");
  await page.goBack();
  await expect(
    page.getByRole("textbox", { name: "Search local metadata", exact: true }),
  ).toHaveValue("Unsubmitted counts draft");
  await page.goForward();
  await expect(page.locator("article.resource-card")).toHaveCount(1);
  await page.reload();
  await expect(page.locator("article.resource-card")).toHaveCount(4);
});
