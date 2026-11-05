import { writeFile } from "node:fs/promises";
import { expectKindPalette } from "./kind-palette";
import { array, object, text } from "./json";
import { required } from "../required";
import {
  dismissNativeReload,
  test,
  expect,
  login,
  confirmCommand,
  signOut,
} from "./fixtures";

test.describe("curated Catalog layout", () => {
  test.use({ curated: true });
  test("comfortable Catalog keeps search wide and first-row Preview within the desktop viewport", async ({
    page,
    catalog,
  }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await login(page, catalog);
    await page.mouse.move(0, 0);
    const input = page.getByRole("searchbox", {
      name: "Search Catalog",
      exact: true,
    });
    const searchWidth = (await input.boundingBox())!.width;
    expect(searchWidth).toBeGreaterThanOrEqual(400);
    const card = page.locator("article.resource-card").first();
    await expect(card).toBeVisible();
    const defaultCard = await card.boundingBox();
    const defaultPreview = await card
      .getByText("Preview", { exact: true })
      .boundingBox();
    await expect(
      page
        .locator("article.resource-card")
        .first()
        .getByText("Preview", { exact: true }),
    ).toBeInViewport({ ratio: 1 });
    await page.screenshot({
      path: test.info().outputPath("catalog-browse.png"),
    });
    await page
      .getByRole("button", { name: "Exclude live results", exact: true })
      .click();
    await expect(page.locator("article.resource-card")).toHaveCount(12);
    const allKinds = ["agent", "mcp_server", "skill", "plugin"] as const;
    await expectKindPalette(page.locator("main"), allKinds);
    await expect(
      page
        .locator("article.resource-card")
        .first()
        .getByText("Preview", { exact: true }),
    ).toBeInViewport({ ratio: 1 });
    const measurements = test.info().outputPath("layout-measurements.json");
    await writeFile(
      measurements,
      JSON.stringify(
        {
          viewport: { width: 1280, height: 900 },
          density: "Comfortable",
          searchWidth,
          defaultCard,
          defaultPreview,
          filteredCard: await card.boundingBox(),
          filteredPreview: await card
            .getByText("Preview", { exact: true })
            .boundingBox(),
        },
        null,
        2,
      ) + "\n",
    );
    await test.info().attach("layout-measurements", {
      path: measurements,
      contentType: "application/json",
    });
    await page.getByRole("button", { name: "Categories", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Apply filters", exact: true }),
    ).toBeVisible();
    await expect(page.locator(".category-count").first()).toHaveText(
      /^[1-9][0-9]*$/,
    );
    await page
      .getByRole("region", { name: "Filters", exact: true })
      .screenshot({
        path: test.info().outputPath("catalog-filters.png"),
      });
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: "Dark", exact: true }).check();
    await page.keyboard.press("Escape");
    await expectKindPalette(page.locator("main"), allKinds);
  });
});

test("imported-resource card links remain independent pointer and keyboard targets", async ({
  page,
  catalog,
}) => {
  const item = required(catalog.receipt.items.find((entry) => entry.releaseId));
  await page.route("**/_catalog/api/v1/catalog/search", async (route) => {
    const response = await route.fetch();
    const data = object(await response.json());
    delete data.nextCursor;
    await route.fulfill({
      response,
      json: {
        ...data,
        durableTotal: 1,
        liveCount: 0,
        partial: false,
        items: [
          {
            id: "indexed-link-fixture",
            type: "indexed",
            identifier: "urn:air:review.example:tools:one",
            displayName: "Imported link fixture",
            description: "Local display-only pointer fixture",
            kind: "mcp_server",
            versionPresent: true,
            version: "1.0.0",
            tags: [],
            indexed: {
              observationGroupId:
                "observation-group-v1:sha256:" + "a".repeat(64),
              sourceIds: ["00000000-0000-7000-8000-000000000010"],
              sourceCount: 1,
              sourcesTruncated: false,
              conflict: false,
              sameSourceConflict: false,
              normalizedEntryDigest: "sha256:" + "b".repeat(64),
            },
            importedHere: { resourceId: item.resourceId, changedEntry: false },
          },
        ],
      },
    });
  });
  await login(page, catalog, "local-admin");
  const link = page.getByRole("link", { name: "Imported here", exact: true });
  await expect(link).toHaveAttribute("href", `/resources/${item.resourceId}`);
  await link.scrollIntoViewIfNeeded();
  expect(
    await link.evaluate((element) => {
      const box = element.getBoundingClientRect();
      return (
        document
          .elementFromPoint(box.x + box.width / 2, box.y + box.height / 2)
          ?.closest("a") === element
      );
    }),
  ).toBe(true);
  await link.click();
  await expect(page).toHaveURL(
    catalog.origin + `/resources/${item.resourceId}`,
  );
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.goBack();
  await link.focus();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(
    catalog.origin + `/resources/${item.resourceId}`,
  );
  await page.goBack();
  await page
    .getByRole("button", { name: "Imported link fixture", exact: true })
    .click();
  await expect(
    page.getByRole("dialog", { name: "Imported link fixture", exact: true }),
  ).toBeVisible();
});

test("compact Catalog filters preserve one draft across desktop and mobile layouts", async ({
  page,
  catalog,
}) => {
  await login(page, catalog);
  const cards = page.locator("article.resource-card");
  await expect(cards).toHaveCount(3);
  await expect(cards.first()).toBeInViewport();
  const trigger = page.getByRole("button", { name: "Filters", exact: true });
  await trigger.click();
  const panel = page.getByRole("region", { name: "Filters", exact: true });
  await expect(panel).toBeVisible();
  expect(await panel.evaluate((el) => getComputedStyle(el).position)).toBe(
    "static",
  );
  await page.getByRole("checkbox", { name: "Agents", exact: true }).check();
  const tags = page.getByRole("textbox", { name: "Add tag", exact: true });
  await tags.fill("orders");
  await expect(cards).toHaveCount(3);
  await page.setViewportSize({ width: 390, height: 740 });
  const drawer = page.getByRole("dialog", { name: "Filters", exact: true });
  await expect(drawer).toBeVisible();
  await expect(drawer).toHaveAttribute("aria-modal", "true");
  await expect(tags).toHaveValue("orders");
  await expect(tags).toBeFocused();
  // Background controls cannot take focus while the native drawer is modal.
  const close = page.getByRole("button", {
    name: "Close filters",
    exact: true,
  });
  await close.focus();
  await page
    .locator(".search-input input")
    .evaluate((el) => (el as HTMLInputElement).focus());
  await expect(close).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(
    page.getByRole("button", { name: "Refresh applied results", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(
    page.getByRole("button", { name: "Add category", exact: true }),
  ).toBeFocused();
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(panel).toBeVisible();
  await expect(tags).toHaveValue("orders");
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await expect(cards).toHaveCount(1);
  await expect(trigger).toBeFocused();
  await expect(
    page.getByRole("button", { name: "Remove tag orders", exact: true }),
  ).toBeVisible();
  await trigger.click();
  await tags.fill("unapplied");
  await page.keyboard.press("Escape");
  await expect(panel).toHaveCount(0);
  await expect(trigger).toBeFocused();
  await trigger.click();
  await expect(tags).toHaveValue("");
  await expect(
    page.getByRole("checkbox", { name: "orders", exact: true }),
  ).toBeChecked();
  await page.keyboard.press("Escape");
});

test("multi-kind filters and exact result tags preserve submitted search on a narrow screen", async ({
  page,
  catalog,
}) => {
  await page.setViewportSize({ width: 390, height: 740 });
  await login(page, catalog);
  const cards = page.locator("article.resource-card");
  await expect(cards).toHaveCount(3);
  const retained = catalog.receipt.items.find((item) => item.releaseId);
  expect(retained).toBeTruthy();
  const read = await catalog.admin(
    "GET",
    "/api/v1/catalog/resources/" + required(retained).releaseId,
  );
  const presentation = object(read.data.display);
  const card = cards.filter({
    has: page.getByRole("button", {
      name: String(object(read.data.listingRevision).displayName),
      exact: true,
    }),
  });
  await expect(
    card.getByText(String(presentation.publisherName), { exact: true }),
  ).toBeVisible();
  await expect(
    card.getByText(String(presentation.namespaceName), { exact: true }),
  ).toBeVisible();
  await expect(card.getByText("Local Catalog", { exact: true })).toBeVisible();
  await expect(card.locator(".state-badge")).toHaveText("published");
  await expect(card.locator(".kind-icon svg")).toHaveAttribute(
    "aria-hidden",
    "true",
  );
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);

  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page.getByRole("checkbox", { name: "Agents", exact: true }).focus();
  await page.keyboard.press("Space");
  await page
    .getByRole("checkbox", { name: "MCP Servers", exact: true })
    .check();
  const suggestions = page.getByRole("searchbox", {
    name: "Search tag suggestions",
    exact: true,
  });
  await suggestions.fill("ORDERS");
  await page.getByRole("checkbox", { name: "orders", exact: true }).check();
  await suggestions.fill("no-such-suggestion");
  await expect(
    page.getByRole("checkbox", { name: "orders", exact: true }),
  ).toBeChecked();
  await expect(
    page.getByText(
      "No matching tag suggestions. Selected tags remain available.",
    ),
  ).toBeVisible();
  await expect(cards).toHaveCount(3);
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await expect(cards).toHaveCount(2);
  await page
    .getByRole("button", { name: "Remove tag orders", exact: true })
    .click();
  await expect(cards).toHaveCount(2);
  await page
    .getByRole("searchbox", { name: "Search Catalog", exact: true })
    .fill("unsubmitted-no-results-draft");
  const requestPromise = page.waitForRequest(
    (request) =>
      request.method() === "POST" &&
      request.url().endsWith("/api/v1/catalog/search"),
  );
  await cards
    .getByRole("button", { name: "Filter by tag orders", exact: true })
    .first()
    .focus();
  await page.keyboard.press("Enter");
  const request = await requestPromise;
  expect(request.postDataJSON()).toMatchObject({
    text: "",
    kinds: ["agent", "mcp_server"],
    tags: ["orders"],
  });
  await expect(cards).toHaveCount(2);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
  ).toHaveValue("unsubmitted-no-results-draft");
  await expect(
    page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
  ).toBeFocused();
  // A stretched title hit target must not intercept real pointer tag clicks.
  await page
    .getByRole("button", { name: "Remove tag orders", exact: true })
    .click();
  await expect(cards).toHaveCount(2);
  const pointerRequest = page.waitForRequest(
    (r) => r.method() === "POST" && r.url().endsWith("/api/v1/catalog/search"),
  );
  await cards
    .getByRole("button", { name: "Filter by tag orders", exact: true })
    .first()
    .click();
  expect((await pointerRequest).postDataJSON()).toMatchObject({
    text: "",
    kinds: ["agent", "mcp_server"],
    tags: ["orders"],
  });
  await expect(cards).toHaveCount(2);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
  ).toHaveValue("unsubmitted-no-results-draft");
  await page
    .getByRole("button", { name: "Remove kind Agent", exact: true })
    .click();
  await expect(cards).toHaveCount(1);
  await page.getByRole("button", { name: "Clear all", exact: true }).click();
  await expect(cards).toHaveCount(3);
  // HTML input limits must allow 80 code points even when each uses two
  // UTF-16 units. The real API accepts the filter; no fixture has this tag.
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  const unicodeTag = "\u{1f642}".repeat(80);
  await page.getByLabel("Add tag", { exact: true }).fill(unicodeTag);
  const unicodeResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/catalog/search"),
  );
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  const response = await unicodeResponse;
  expect(response.status()).toBe(200);
  expect(object(response.request().postDataJSON()).tags).toEqual([unicodeTag]);
  await expect(
    page.getByRole("heading", { name: "No resources found" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Clear all", exact: true }).click();
  await expect(cards).toHaveCount(3);
});

test("reader filters, previews, bookmarks and subpath hosting", async ({
  page,
  catalog,
}) => {
  await login(page, catalog);
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  await page.getByRole("button", { name: "Agents", exact: true }).click();
  await expect(page.locator("article.resource-card")).toHaveCount(1);
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page.getByRole("checkbox", { name: "orders", exact: true }).check();
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Remove tag orders" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Table", exact: true }).click();
  await page.locator(".table-scroll tbody tr button").first().click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).not.toBeVisible();
  await expect(
    page.locator(".table-scroll tbody tr button").first(),
  ).toBeFocused();
  await page.locator(".table-scroll tbody tr button").first().click();
  await page.getByRole("link", { name: "Open full record" }).click();
  await page.reload();
  await expect(page).toHaveTitle("Catalog record | Open Agentic Catalog");
  await expect(
    page
      .getByRole("navigation", { name: "Breadcrumb" })
      .getByRole("link", { name: "Catalog", exact: true }),
  ).toHaveAttribute("href", `${catalog.basePath}/catalog`);
  await expect(
    page.getByText("Order exception Agent", { exact: true }).first(),
  ).toBeVisible();
});

test("responsive keyboard filters and appearance persist without private storage", async ({
  page,
  catalog,
}) => {
  await login(page, catalog);
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.getByRole("radio", { name: "Compact", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.locator("html")).toHaveAttribute("data-density", "compact");
  expect(await page.evaluate(() => Object.keys(localStorage).sort())).toEqual([
    "catalog-ui:density",
    "catalog-ui:theme",
  ]);
  for (const width of [320, 390]) {
    await page.setViewportSize({ width, height: 740 });
    await page.getByLabel("Appearance", { exact: true }).click();
    await expect(page.locator(".appearance-panel")).toBeInViewport();
    await page.getByRole("radio", { name: "Dark", exact: true }).focus();
    await page.keyboard.press("ArrowLeft");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
    await page.keyboard.press("ArrowRight");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    await page.keyboard.press("Escape");
    await expect(page.getByLabel("Appearance", { exact: true })).toBeFocused();
    const menu = page.getByRole("button", {
      name: "Workspace menu",
      exact: true,
    });
    await expect(menu).toBeVisible();
    await expect(
      page.getByRole("navigation", { name: "Workspace", exact: true }),
    ).toBeHidden();
    await menu.focus();
    await page.keyboard.press("Enter");
    await expect(menu).toHaveAttribute("aria-expanded", "true");
    const navigation = page.getByRole("navigation", {
      name: "Workspace",
      exact: true,
    });
    for (const name of ["Discover", "Manage content", "Operations", "Help"]) {
      await expect(
        navigation.getByRole("group", { name, exact: true }),
      ).toBeVisible();
    }
    await expect(
      navigation.getByRole("group", { name: "Administration", exact: true }),
    ).toHaveCount(0);
    await expect(
      navigation
        .getByRole("group", { name: "Discover", exact: true })
        .getByRole("link", { name: "Authoring", exact: true }),
    ).toHaveCount(0);
    await expect(
      navigation
        .getByRole("group", { name: "Manage content", exact: true })
        .getByRole("link", { name: "Authoring", exact: true }),
    ).toBeVisible();
    // The expanded menu scrolls within its height bound. Keyboard navigation
    // must reveal the final Help destination after Documentation.
    await navigation
      .getByRole("link", { name: "Documentation", exact: true })
      .focus();
    for (const name of [
      "Getting started",
      "About",
      "Troubleshooting",
      "API reference",
    ]) {
      await page.keyboard.press("Tab");
      const destination = navigation.getByRole("link", { name, exact: true });
      await expect(destination).toBeFocused();
      await expect(destination).toBeInViewport();
    }
    await expect(
      page.getByRole("link", { name: "Members", exact: true }),
    ).toHaveCount(0);
    await page.keyboard.press("Escape");
    await expect(menu).toHaveAttribute("aria-expanded", "false");
    await expect(menu).toBeFocused();
    await page.getByRole("button", { name: "Filters", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Apply filters" }),
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
  }
});

test("reader cannot enter admin commands and cross-tab sign-out clears private views", async ({
  page,
  context,
  sourceCoverage,
  catalog,
}) => {
  await login(page, catalog, "local-reader", "/namespaces/new");
  await expect(page.getByRole("alert")).toContainText(
    "does not permit namespace creation",
  );
  const second = await context.newPage();
  await sourceCoverage.ready(second);
  await second.goto(catalog.origin + "/catalog");
  await expect(second.locator("article.resource-card")).toHaveCount(3);
  await second
    .getByRole("button", { name: "Order exception Agent", exact: true })
    .click();
  await expect(
    second
      .getByRole("dialog")
      .getByRole("region", { name: "Resource handoff" }),
  ).toBeVisible();
  await signOut(page);
  await expect(
    second.getByText(
      "Your session changed in another tab. Sign in to continue.",
    ),
  ).toBeVisible();
  await expect(second.locator("article.resource-card")).toHaveCount(0);
  await expect(second.getByRole("dialog")).toHaveCount(0);
  await expect(
    second.getByRole("region", { name: "Resource handoff" }),
  ).toHaveCount(0);
  await login(page, catalog, "other-tenant-admin");
  await expect(
    page.getByRole("heading", { name: "No resources found" }),
  ).toBeVisible();
  await expect(page.locator("article.resource-card")).toHaveCount(0);
});

for (const outcome of ["lost", "malformed JSON", "malformed shape"] as const) {
  test(`namespace creation retries the exact command after a ${outcome} successful response`, async ({
    page,
    catalog,
    network,
  }) => {
    if (outcome === "lost")
      network.expectFailure("POST", "/_catalog/api/v1/namespaces", 0);
    await login(page, catalog, "local-admin", "/namespaces/new");
    await page
      .getByLabel("Display name", { exact: true })
      .fill("Browser namespace");
    await page.getByLabel("Slug", { exact: true }).fill("browser-namespace");
    const requests: { key: string | undefined; body: string | null }[] = [];
    await page.route("**/_catalog/api/v1/namespaces", async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      requests.push({
        key: route.request().headers()["idempotency-key"],
        body: route.request().postData(),
      });
      const response = await route.fetch();
      expect(response.status()).toBe(201);
      if (requests.length === 1) {
        if (outcome === "lost") await route.abort("failed");
        else
          await route.fulfill({
            response,
            body:
              outcome === "malformed JSON"
                ? '{"private":"parse sentinel"'
                : '{"id":null}',
          });
      } else {
        expect(response.headers()["idempotency-replayed"]).toBe("true");
        await route.fulfill({ response });
      }
    });
    const create = page.getByRole("button", {
      name: "Create namespace",
      exact: true,
    });
    const slug = page.getByLabel("Slug", { exact: true });
    await slug.fill("Invalid Slug");
    await create.click();
    await expect(slug).toBeFocused();
    expect(
      await slug.evaluate(
        (input: HTMLInputElement) => input.validity.patternMismatch,
      ),
    ).toBe(true);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(requests).toHaveLength(0);
    await slug.fill("browser-namespace");
    // Preparing and cancelling a command must not send it or discard its inputs.
    await create.click();
    await expect(
      page.getByLabel("Display name", { exact: true }),
    ).toBeDisabled();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
      "Browser namespace",
    );
    await create.click();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(requests).toHaveLength(0);
    const stay = page.waitForEvent("dialog").then(async (dialog) => {
      const message = dialog.message();
      await dialog.dismiss();
      expect(message).toContain("discard unsaved input");
    });
    await signOut(page);
    await stay;
    await expect(page.getByLabel("Slug", { exact: true })).toHaveValue(
      "browser-namespace",
    );
    await page
      .getByRole("button", { name: "Create namespace", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
      "outcome is unknown",
    );
    await expect(page.getByRole("dialog")).not.toContainText("parse sentinel");
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toBeVisible();
    await expect(
      page.getByRole("dialog").getByRole("button", { name: "Back to form" }),
    ).toBeDisabled();
    expect(requests).toHaveLength(1);
    const pendingURL = page.url();
    await dismissNativeReload(page);
    await expect(page).toHaveURL(pendingURL);
    await expect(page.getByRole("dialog")).toBeVisible();
    await expect(page.getByLabel("Slug", { exact: true })).toHaveValue(
      "browser-namespace",
    );
    expect(requests).toHaveLength(1);
    await page
      .getByRole("button", { name: "Retry same command", exact: true })
      .click();
    await expect(page.getByRole("status")).toContainText(
      "Created namespace browser-namespace",
    );
    expect(requests).toHaveLength(2);
    expect(required(requests[0]).key).toBeTruthy();
    expect(requests[1]).toEqual(requests[0]);
    await page.getByRole("link", { name: "Back to namespaces" }).click();
    await expect(
      page.getByRole("link", { name: "Browser namespace", exact: true }),
    ).toHaveCount(1);
  });
}

test("failed namespace reads hide stale results and recover on explicit reload", async ({
  page,
  catalog,
  network,
}) => {
  network.expectFailure("GET", "/_catalog/api/v1/namespaces", 429);
  network.expectFailure("GET", "/_catalog/api/v1/namespaces", 503);
  await page.clock.install();
  await login(page, catalog, "local-reader", "/namespaces");
  const namespace = page.locator(
    `a[href="/namespaces/${catalog.receipt.namespaceId}"]`,
  );
  await expect(namespace).toBeVisible();
  let reads = 0;
  let retryAt = 0;
  await page.route("**/_catalog/api/v1/namespaces?*", async (route) => {
    expect(route.request().method()).toBe("GET");
    reads++;
    if (reads <= 2) {
      // Hinted and excessive-advice faults precede a real-service recovery.
      retryAt = Math.floor(Date.now() / 1000) * 1000 + 30000;
      await route.fulfill({
        status: reads === 1 ? 429 : 503,
        headers: {
          "Retry-After":
            reads === 1 ? new Date(retryAt).toUTCString() : "86401",
        },
        contentType: "application/problem+json",
        body: JSON.stringify({
          code: "UNAVAILABLE",
          detail: "private-error-sentinel",
        }),
      });
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  const error = page.getByRole("alert");
  await expect(error).toContainText("The request could not be completed");
  await expect(error).toContainText("The server suggests retrying after");
  await expect(error).toContainText("Retry manually when ready");
  await expect(
    page.getByText("private-error-sentinel", { exact: false }),
  ).toHaveCount(0);
  await expect(namespace).toHaveCount(0);
  const advice = await page.evaluate(
    (at) => new Date(at).toLocaleString(),
    retryAt,
  );
  await expect(error).toContainText(advice);
  // Advancing beyond the advice must neither rebase it nor send another read.
  await page.clock.runFor(60000);
  await expect(error).toContainText(advice);
  expect(reads).toBe(1);
  await error.getByRole("button", { name: "Reload", exact: true }).click();
  await expect(error).toContainText("The request could not be completed");
  await expect(error).not.toContainText("server suggests");
  await expect(error).not.toContainText("private-error-sentinel");
  await expect(namespace).toHaveCount(0);
  await page.clock.runFor(60000);
  expect(reads).toBe(2);
  await error.getByRole("button", { name: "Reload", exact: true }).click();
  await expect(namespace).toBeVisible();
  await expect(error).toHaveCount(0);
  expect(reads).toBe(3);
  await page.clock.resume();
});

test("listing forms reveal invalid relationships, retain versions and save metadata", async ({
  page,
  catalog,
}) => {
  const item = catalog.receipt.items.find(
    (item) => item.key === "draft-agent",
  )!;
  const target = catalog.receipt.items.find(
    (item) => item.key === "order-mcp",
  )!;
  await login(
    page,
    catalog,
    "local-contributor",
    `/listings/${item.listingId}`,
  );
  await page
    .getByRole("button", { name: "New listing revision", exact: true })
    .click();
  await expect(
    page.getByLabel("Keep the current version revision"),
  ).toBeChecked();
  await page
    .getByLabel("Display name", { exact: true })
    .fill("Browser reviewed Agent");
  const removeTags = page.getByRole("button", { name: /^Remove tag / });
  while (await removeTags.count()) await removeTags.first().click();
  const removeCategories = page.getByRole("button", {
    name: /^Remove category /,
  });
  while (await removeCategories.count()) await removeCategories.first().click();
  for (const [index, tag] of [
    "browser",
    "evidence, browser",
    "browser",
  ].entries()) {
    await page.getByRole("button", { name: "Add tag", exact: true }).click();
    await page
      .getByRole("textbox", { name: `Tag ${index + 1}`, exact: true })
      .fill(tag);
  }
  for (const [index, category] of ["testing", "governance"].entries()) {
    await page
      .getByRole("button", { name: "Add category", exact: true })
      .click();
    await page
      .getByRole("textbox", {
        name: `Category ${index + 1}`,
        exact: true,
      })
      .fill(category);
  }
  await page
    .getByRole("combobox", { name: "Visibility", exact: true })
    .selectOption("public");
  await page
    .getByRole("textbox", {
      name: "Representative queries (one per line)",
      exact: true,
    })
    .fill("Only one query");
  await page
    .getByRole("button", { name: "Create listing revision", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "Public listings need two to five representative queries.",
  );
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page
    .getByRole("combobox", { name: "Visibility", exact: true })
    .selectOption("private");
  const advanced = page.locator("details").filter({
    has: page.getByText("Links, license and relationships", { exact: true }),
  });
  await advanced.locator("summary").click();
  await page.getByRole("button", { name: "Add relationship" }).click();
  await expect(page.getByLabel("Relationship type")).toBeFocused();
  await advanced.locator("summary").click();
  await page
    .getByRole("button", { name: "Create listing revision", exact: true })
    .click();
  await expect(advanced).toHaveAttribute("open", "");
  await expect(
    page.getByLabel("Target identifier", { exact: true }),
  ).toBeFocused();
  await page
    .getByLabel("Target identifier", { exact: true })
    .fill(target.identifier);
  await page.getByLabel("Relationship type").selectOption("supersedes");
  await page
    .getByRole("button", { name: "Create listing revision", exact: true })
    .click();
  await expect(
    page.getByLabel("Target version (required)", { exact: true }),
  ).toBeFocused();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page
    .getByLabel("Target version (required)", { exact: true })
    .fill("1.0.0");
  await page.getByLabel("Relationship type").selectOption("depends_on");
  await page.getByLabel("Target version (optional)", { exact: true }).fill("");
  await page.getByRole("button", { name: "Add relationship" }).click();
  await expect(page.getByLabel("Relationship type").nth(1)).toBeFocused();
  await page
    .getByLabel("Target identifier", { exact: true })
    .nth(1)
    .fill(target.identifier);
  await page
    .getByRole("button", { name: "Create listing revision", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText("duplicate relationship");
  await expect(page.getByRole("dialog")).toHaveCount(0);

  await page.getByRole("button", { name: "Remove relationship 2" }).click();
  await expect(page.getByLabel("Relationship type")).toBeFocused();
  // Exercise the native collection ceiling without manufacturing a 65th row.
  for (let count = 1; count < 64; count++)
    await page
      .getByRole("button", { name: "Add relationship", exact: true })
      .click();
  await expect(
    page.getByRole("button", { name: "Add relationship", exact: true }),
  ).toBeDisabled();
  await expect(page.getByLabel("Relationship type")).toHaveCount(64);
  for (let count = 64; count > 1; count--)
    await page
      .getByRole("button", {
        name: `Remove relationship ${count}`,
        exact: true,
      })
      .click();
  await expect(
    page.getByLabel("Target identifier", { exact: true }),
  ).toHaveValue(target.identifier);
  let revisionWrites = 0;
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      request.url().endsWith(`/catalog-listings/${item.listingId}/revisions`)
    )
      revisionWrites++;
  });
  const extensions = page.getByRole("textbox", {
    name: "Extensions (JSON object)",
    exact: true,
  });
  for (const invalid of [
    '{"count":9007199254740992}',
    '{"count":-9007199254740992}',
    '{"count":1e400}',
  ]) {
    await extensions.fill(invalid);
    await page
      .getByRole("button", { name: "Create listing revision", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText(
      "Whole numbers must be between -9007199254740991 and 9007199254740991.",
    );
    await expect(page.getByRole("alert")).not.toContainText("URL");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(extensions).toHaveValue(invalid);
    expect(revisionWrites).toBe(0);
  }
  const boundaryExtensions = {
    minimum: -9007199254740991,
    maximum: 9007199254740991,
  };
  await extensions.fill(JSON.stringify(boundaryExtensions));
  const response = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith(`/catalog-listings/${item.listingId}/revisions`),
  );
  await page
    .getByRole("button", { name: "Create listing revision", exact: true })
    .click();
  await confirmCommand(page);
  const saved = await response;
  expect(saved.status()).toBe(201);
  const revision = object(object(await saved.json()).revision);
  expect(revisionWrites).toBe(1);
  expect(object(saved.request().postDataJSON()).extensions).toEqual(
    boundaryExtensions,
  );
  expect(revision.extensions).toEqual(boundaryExtensions);
  expect(revision.versionRevisionId).toBe(item.versionRevisionId);
  expect(revision.tags).toEqual(["browser", "evidence, browser"]);
  expect(revision.categories).toEqual(["governance", "testing"]);
  expect(revision.relationships).toEqual([
    {
      type: "depends_on",
      targetIdentifier: target.identifier,
    },
  ]);
  await page.reload();
  const retained = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      response
        .url()
        .endsWith(`/_catalog/api/v1/listing-revisions/${text(revision.id)}`),
  );
  await page.getByRole("link", { name: "Inspect candidate revision" }).click();
  expect(object(await (await retained).json()).extensions).toEqual(
    boundaryExtensions,
  );
  await expect(
    page.getByText("Browser reviewed Agent", { exact: true }).first(),
  ).toBeVisible();
  await expect(page.getByRole("main")).toContainText("evidence, browser");
});

test("independent review preserves evidence and supports edit resubmission and rejection history", async ({
  page,
  catalog,
}) => {
  const item = catalog.receipt.items.find(
    (item) => item.key === "pending-mcp",
  )!;
  let evidenceReads = 0;
  page.on("request", (request) => {
    if (
      request
        .url()
        .endsWith(`/review-submissions/${item.submissionId}/evidence`)
    )
      evidenceReads++;
  });
  await login(page, catalog, "local-reviewer", `/reviews/${item.submissionId}`);
  await expect(
    page.getByRole("heading", { name: "Awaiting independent review" }),
  ).toBeVisible();
  expect(evidenceReads).toBe(0);
  await page
    .getByRole("button", { name: "Inspect submitted evidence" })
    .click();
  await expect(
    page.getByText("MCP Server awaiting review", { exact: true }).first(),
  ).toBeVisible();
  expect(evidenceReads).toBe(1);
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Please clarify the metadata");
  const dismissed = page
    .waitForEvent("dialog")
    .then((dialog) => dialog.dismiss());
  await page
    .getByRole("button", { name: "Inspect submitted evidence" })
    .click();
  await dismissed;
  await expect(
    page.getByRole("textbox", { name: "Reason", exact: true }),
  ).toHaveValue("Please clarify the metadata");
  await page
    .getByRole("combobox", { name: "Decision", exact: true })
    .selectOption("request-changes");
  await page.getByRole("button", { name: "Record review decision" }).click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText(
    "Review changes required.",
  );
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Review changes required" }),
  ).toBeVisible();
  await signOut(page);
  await login(
    page,
    catalog,
    "local-contributor",
    `/reviews/${item.submissionId}`,
  );
  await expect(
    page.getByRole("region", { name: "Reviewer decisions", exact: true }),
  ).toContainText("Please clarify the metadata");
  await page.reload();
  await expect(
    page.getByRole("region", { name: "Reviewer decisions", exact: true }),
  ).toContainText("Changes requested");
  await page.goto(
    `${catalog.origin}${catalog.basePath}/listings/${item.listingId}`,
  );
  await page
    .getByRole("button", { name: "New listing revision", exact: true })
    .click();
  await page
    .getByLabel("Display name", { exact: true })
    .fill("Clarified MCP listing");
  await page
    .getByRole("button", { name: "Create listing revision", exact: true })
    .click();
  await confirmCommand(page);
  // The acknowledged write can coexist with its follow-up read's loading status.
  await expect(
    page.getByRole("status").filter({ hasText: /^Listing revision saved\.$/ }),
  ).toHaveText("Listing revision saved.");
  await page.reload();
  await page
    .getByRole("button", { name: "Submit for review", exact: true })
    .click();
  await confirmCommand(page);
  await page
    .getByRole("link", { name: "Open review submission", exact: true })
    .click();
  const resubmittedURL = page.url();
  const resubmittedId = resubmittedURL.split("/").pop()!;
  expect(resubmittedId).not.toBe(item.submissionId);
  await signOut(page);
  await login(page, catalog, "local-reviewer", "/reviews");
  await expect(
    page.getByRole("combobox", { name: "Review state", exact: true }),
  ).toHaveValue("submitted");
  await expect(
    page.getByRole("link", { name: item.submissionId!, exact: true }),
  ).toHaveCount(0);
  await page.getByRole("link", { name: resubmittedId, exact: true }).click();
  await expect(page).toHaveURL(resubmittedURL);
  await page
    .getByRole("button", { name: "Inspect submitted evidence" })
    .click();
  await expect(
    page.getByText("Clarified MCP listing", { exact: true }).first(),
  ).toBeVisible();
  await page
    .getByRole("combobox", { name: "Decision", exact: true })
    .selectOption("reject");
  await page
    .getByLabel("Reason", { exact: true })
    .fill("The revised metadata still needs correction");
  await page.getByRole("button", { name: "Record review decision" }).click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText("Review rejected.");
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Review rejected", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Inspect submitted evidence" })
    .click();
  await expect(
    page.getByRole("heading", {
      name: "Submitted evidence",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Record review decision" }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("combobox", { name: "Decision", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("textbox", { name: "Reason", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByText("Only a pending submission accepts a new review decision.", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Reviewer decisions", exact: true }),
  ).toContainText("The revised metadata still needs correction");
  await page.getByRole("link", { name: "Listing", exact: true }).click();
  await page
    .getByRole("button", { name: "Release history", exact: true })
    .click();
  await expect(
    page.getByRole("heading", {
      name: "Listing revision history",
      exact: true,
    }),
  ).toHaveCount(0);
  await page
    .getByRole("button", { name: "Revision history", exact: true })
    .click();
  const revisions = page.locator("section").filter({
    has: page.getByRole("heading", {
      name: "Listing revision history",
      exact: true,
    }),
  });
  await expect(revisions.getByRole("link")).toHaveCount(2);
  await revisions.getByRole("link", { name: /^Revision 1 by / }).click();
  await expect(
    page.getByText("MCP Server awaiting review", { exact: true }).first(),
  ).toBeVisible();
  await page
    .getByRole("link", { name: "Back to listing", exact: true })
    .click();
  await page
    .getByRole("link", { name: "Review submissions", exact: true })
    .click();
  await expect(page).toHaveURL(
    `${catalog.origin}/reviews?listing=${item.listingId}`,
  );
  await expect(
    page.getByRole("combobox", { name: "Review state", exact: true }),
  ).toHaveValue("");
  await expect(page.locator("tbody tr")).toHaveCount(2);
  await page
    .getByRole("combobox", { name: "Review state", exact: true })
    .selectOption("changes_required");
  await page
    .getByRole("button", { name: "Apply review filters", exact: true })
    .click();
  await expect(page.locator("tbody tr")).toHaveCount(1);
  await page
    .getByRole("link", { name: item.submissionId!, exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Review changes required", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Inspect submitted evidence" })
    .click();
  await expect(
    page.getByText("MCP Server awaiting review", { exact: true }).first(),
  ).toBeVisible();
  await expect(
    page.getByText("Clarified MCP listing", { exact: true }),
  ).toHaveCount(0);
  await signOut(page);
  await login(
    page,
    catalog,
    "local-contributor",
    `/reviews/${item.submissionId}`,
  );
  await expect(
    page.getByRole("region", { name: "Reviewer decisions", exact: true }),
  ).toContainText("Please clarify the metadata");
  const latestListing = await catalog.admin<{ candidateRevisionId: string }>(
    "GET",
    `/api/v1/catalog-listings/${item.listingId}`,
  );
  const originalSubmission = await catalog.admin<{ listingRevisionId: string }>(
    "GET",
    `/api/v1/review-submissions/${item.submissionId}`,
  );
  await page
    .getByRole("button", { name: "Choose revisions to compare", exact: true })
    .click();
  await page
    .getByRole("combobox", { name: "Before revision", exact: true })
    .selectOption(latestListing.data.candidateRevisionId);
  await expect(
    page.getByRole("combobox", { name: "After revision", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByText(/After is fixed to the submitted revision/),
  ).toContainText(originalSubmission.data.listingRevisionId);
  await page
    .getByRole("button", { name: "Compare selected revisions", exact: true })
    .click();
  const comparison = page.getByLabel("Compared revisions", { exact: true });
  await expect(comparison).toContainText(
    originalSubmission.data.listingRevisionId,
  );
  await page
    .getByRole("button", {
      name: "Compare linked Version revisions",
      exact: true,
    })
    .click();
  await expect(
    comparison.getByRole("heading", {
      name: "Before representations",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    comparison.getByRole("region", {
      name: "Linked Version comparison",
      exact: true,
    }),
  ).toContainText(item.versionRevisionId);
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const author = members.data.items.find(
    (member) => member.subject === "local-contributor",
  )!;
  const grants = await catalog.admin<{
    items: { id: string; revision: number; state: string }[];
    nextCursor?: string;
  }>("GET", `/api/v1/access/assignments?membershipId=${author.id}&limit=100`);
  expect(grants.data.nextCursor).toBeUndefined();
  const active = grants.data.items.filter((grant) => grant.state === "active");
  expect(active.length).toBeGreaterThan(0);
  for (const grant of active)
    await catalog.admin(
      "POST",
      `/api/v1/access/assignments/${grant.id}:revoke`,
      { reason: "Qualify reviewer-reason clearing after author access loss" },
      `"rev-${grant.revision}"`,
    );
  await page.reload();
  await expect(
    page.getByRole("region", { name: "Reviewer decisions", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByText("Please clarify the metadata", { exact: true }),
  ).toHaveCount(0);
});

test("stale namespace edits cannot overwrite a newer saved revision", async ({
  page,
  context,
  sourceCoverage,
  catalog,
  network,
}) => {
  network.expectFailure(
    "PATCH",
    `/_catalog/api/v1/namespaces/${catalog.receipt.namespaceId}`,
    412,
  );
  network.expectFailure(
    "PATCH",
    `/_catalog/api/v1/namespaces/${catalog.receipt.namespaceId}`,
    503,
  );
  const path = `/namespaces/${catalog.receipt.namespaceId}`;
  await login(page, catalog, "local-admin", path);
  const stale = await context.newPage();
  await sourceCoverage.ready(stale);
  await stale.goto(catalog.origin + path);
  await expect(
    stale.getByRole("textbox", { name: "Display name", exact: true }),
  ).toBeVisible();
  await stale
    .getByRole("textbox", { name: "Display name", exact: true })
    .fill("Stale edit");
  await stale
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Testing a stale form");
  await page
    .getByRole("textbox", { name: "Display name", exact: true })
    .fill("Current saved name");
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Save the current revision");
  const commands: {
    method: string;
    url: string;
    key: string | undefined;
    etag: string | undefined;
    body: string | null;
  }[] = [];
  let committedETag: string | undefined;
  await page.route(`**/_catalog/api/v1${path}`, async (route) => {
    const request = route.request();
    if (request.method() !== "PATCH") {
      await route.continue();
      return;
    }
    const headers = request.headers();
    commands.push({
      method: request.method(),
      url: request.url(),
      key: headers["idempotency-key"],
      etag: headers["if-match"],
      body: request.postData(),
    });
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    if (commands.length === 1) {
      committedETag = response.headers()["etag"];
      expect(committedETag).toBeTruthy();
      expect(committedETag).not.toBe(required(commands[0]).etag);
      // The service committed; only the browser's first response is replaced.
      await route.fulfill({
        status: 503,
        headers: { "Retry-After": "30" },
        contentType: "application/problem+json",
        body: JSON.stringify({
          code: "UNAVAILABLE",
          detail: "private-retry-sentinel",
        }),
      });
    } else {
      expect(response.headers()["idempotency-replayed"]).toBe("true");
      expect(response.headers()["etag"]).toBe(committedETag);
      await route.fulfill({ response });
    }
  });
  await page.getByRole("button", { name: "Review changes" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText(
    "The server suggests retrying after",
  );
  await expect(dialog.getByRole("alert")).toContainText("outcome is unknown");
  await expect(dialog).not.toContainText("private-retry-sentinel");
  expect(commands).toHaveLength(1);
  expect(required(commands[0]).key).toBeTruthy();
  expect(required(commands[0]).etag).toBeTruthy();
  await dialog.getByRole("button", { name: "Retry same command" }).click();
  await expect(dialog).toHaveCount(0);
  expect(commands).toHaveLength(2);
  expect(commands[1]).toEqual(commands[0]);
  const rejected = stale.waitForResponse(
    (response) =>
      response.request().method() === "PATCH" &&
      response
        .url()
        .endsWith(path.replace("/namespaces/", "/api/v1/namespaces/")),
  );
  await stale.getByRole("button", { name: "Review changes" }).click();
  await stale
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  expect((await rejected).status()).toBe(412);
  await expect(stale.getByRole("alert")).toContainText(
    "The record changed. Your input is retained.",
  );
  await expect(
    stale.getByRole("button", { name: "Retry same command" }),
  ).toHaveCount(0);
  await expect(
    stale
      .getByRole("dialog")
      .getByRole("button", { name: "Retry same command" }),
  ).toHaveCount(0);
  await expect(
    stale.getByRole("dialog").getByRole("button", { name: "Back to form" }),
  ).toBeVisible();
  const readback = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      response.url().endsWith(`/_catalog/api/v1${path}`),
  );
  await page.reload();
  expect((await readback).headers()["etag"]).toBe(committedETag);
  await expect(
    page.getByRole("textbox", { name: "Display name", exact: true }),
  ).toHaveValue("Current saved name");
});

test("origin and local namespace filters preserve same-tab record return and fresh reload", async ({
  page,
  context,
  sourceCoverage,
  catalog,
}) => {
  await login(page, catalog);
  const first = page.locator("article.resource-card").first();
  const name = await first.locator("h2").innerText();
  const namespace = first.locator(".catalog-presentation button").first();
  await namespace.click();
  await expect(
    page.getByRole("button", { name: /^Remove local namespace / }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await expect(
    page.getByRole("checkbox", { name: "Indexed external", exact: true }),
  ).toBeDisabled();
  await page
    .getByRole("button", { name: "Load namespaces", exact: true })
    .click();
  await expect(
    page.getByRole("combobox", { name: "Local namespace" }),
  ).not.toHaveValue("");
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await page.getByRole("button", { name, exact: true }).click();
  const opened = context.waitForEvent("page");
  await page
    .getByRole("link", { name: /Open full record/ })
    .click({ modifiers: ["Control"] });
  const separate = await opened;
  await sourceCoverage.ready(separate);
  await expect(
    separate.getByRole("link", { name: /Back to Catalog/ }),
  ).toBeVisible();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(
    page.getByRole("button", { name: /^Remove local namespace / }),
  ).toBeVisible();
  await sourceCoverage.flush(separate);
  await separate.close();
  await page.getByRole("link", { name: /Open full record/ }).click();
  await expect(
    page.getByRole("link", { name: /Back to Catalog/ }),
  ).toBeVisible();
  await page.getByRole("link", { name: /Back to Catalog/ }).click();
  await expect(
    page.getByRole("button", { name: /^Remove local namespace / }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("button", { name: /^Remove local namespace / }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page
    .getByRole("checkbox", { name: "Local Catalog", exact: true })
    .uncheck();
  await page
    .getByRole("checkbox", { name: "Indexed external", exact: true })
    .uncheck();
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await expect(
    page.getByText(
      "Enter search text to request live external results. Live results may change. Start a new search if a page can no longer be loaded.",
    ),
  ).toBeVisible();
  await expect(page.locator("article.resource-card")).toHaveCount(0);
});

// Response fixture qualifies rendering only; the ordinary journeys retain real API data.
test("cards wrap long metadata and keep all four kinds accessible at narrow widths", async ({
  page,
  catalog,
}) => {
  const name = "LongResource".repeat(15);
  const description = "UnbrokenDescription".repeat(30);
  const longVersion = "v".repeat(128);
  await page.route(
    "**/_catalog/api/v1/catalog/search",
    async (route) => {
      const response = await route.fetch();
      const payload = object(await response.json());
      const base = object(required(array(payload.items)[0]));
      const local = object(base.local);
      await route.fulfill({
        response,
        json: {
          ...payload,
          items: ["agent", "mcp_server", "skill", "plugin"].map((kind) => ({
            ...base,
            id: kind,
            kind,
            displayName: `${kind}-${name}`,
            description,
            version: longVersion,
            versionPresent: true,
            local: {
              ...local,
              version: { ...object(local.version), version: longVersion },
              resource: { ...object(local.resource), kind },
              display: {
                publisherName: "Publisher".repeat(17),
                namespaceName: "Namespace".repeat(17),
                license: "License".repeat(30),
              },
            },
            tags: ["longtag".repeat(11)],
          })),
          durableTotal: 4,
          liveCount: 0,
        },
      });
    },
    { times: 1 },
  );
  await page.setViewportSize({ width: 320, height: 740 });
  await login(page, catalog);
  const cards = page.locator("article.resource-card");
  await expect(cards).toHaveCount(4);
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
  for (const kind of ["agent", "mcp_server", "skill", "plugin"]) {
    const card = page.locator(`article[data-kind="${kind}"]`);
    await expect(card.locator(".kind-icon svg")).toHaveCount(1);
    const title = card.getByRole("button", {
      name: `${kind}-${name}`,
      exact: true,
    });
    await title.focus();
    await expect(title).toBeFocused();
    await expect(title).toBeInViewport();
    await expect(card.locator(".description")).toHaveText(description);
    await expect(card.locator(".version-label")).toHaveText(longVersion);
    // Clamped descriptions must stay within the card, including unbroken input.
    expect(
      await card.evaluate(
        (element) => element.scrollWidth <= element.clientWidth,
      ),
    ).toBe(true);
  }
  // The stretched preview target must not intercept independent filter buttons.
  const tag = cards
    .first()
    .getByRole("button", { name: `Filter by tag ${"longtag".repeat(11)}` });
  await tag.click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: `Remove tag ${"longtag".repeat(11)}` }),
  ).toBeVisible();
});

test("namespace loading keeps Escape dismissal usable after native focus loss", async ({
  page,
  catalog,
}) => {
  await login(page, catalog);
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  const panel = page.getByRole("region", { name: "Filters", exact: true });
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/_catalog/api/v1/namespaces?*", async (route) => {
    const response = await route.fetch();
    await held;
    await route.fulfill({ response });
  });
  try {
    await page
      .getByRole("button", { name: "Load namespaces", exact: true })
      .click();
    await expect(
      page.getByText("Loading namespaces...", { exact: true }),
    ).toBeVisible();
    // Chromium drops focus when the activated button becomes disabled.
    await expect
      .poll(() => page.evaluate(() => document.activeElement === document.body))
      .toBe(true);
    release();
    await expect(
      page.getByRole("button", { name: "First namespace page", exact: true }),
    ).toBeEnabled();
    await page.keyboard.press("Escape");
    await expect(panel).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Filters", exact: true }),
    ).toBeFocused();
    await page
      .locator("article.resource-card")
      .first()
      .locator("button.title-button")
      .click();
    await expect(
      page
        .getByRole("dialog")
        .getByRole("region", { name: "Resource handoff" }),
    ).toBeVisible();
  } finally {
    release();
  }
});

test("subpath: source diagnostics preserve results, copy attribution and retry only on request", async ({
  page,
  context,
  catalog,
}) => {
  const sourceId = "00000000-0000-7000-8000-000000000010";
  const statuses = [
    "failed",
    "invalid_response",
    "policy_stale",
    "fanout_limited",
    "diagnostics_truncated",
    "live_results_limited",
    "continuation_unavailable",
  ];
  let searches = 0;
  let malformed = false;
  let omitDiagnostics = false;
  const searchBodies: unknown[] = [];
  // Rendering controls use genuine local results; injected diagnostics do not
  // claim a live peer was called or that this synthetic source exists.
  await page.route("**/_catalog/api/v1/catalog/search", async (route) => {
    searches++;
    searchBodies.push(object(route.request().postDataJSON()));
    const response = await route.fetch();
    const payload = object(await response.json());
    delete payload.diagnostics;
    await route.fulfill({
      response,
      json: {
        ...payload,
        partial: true,
        ...(omitDiagnostics
          ? {}
          : {
              diagnostics: statuses.map((status, i) => ({
                status,
                ...(i === 0
                  ? { sourceId: malformed ? "../private?query" : sourceId }
                  : {}),
              })),
            }),
      },
    });
  });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: catalog.origin,
  });
  await login(page, catalog);
  const cards = page.locator("article.resource-card");
  await expect(cards).toHaveCount(3);
  const details = page
    .locator("details")
    .filter({ has: page.getByText("Search diagnostics", { exact: true }) });
  await details.locator("summary").focus();
  await page.keyboard.press("Enter");
  await expect(details).toHaveAttribute("open", "");
  await expect(details.locator("li")).toHaveCount(7);
  for (const status of statuses)
    await expect(details.getByText(status, { exact: true })).toBeVisible();
  await expect(
    details.getByText("No source ID supplied.", { exact: true }),
  ).toHaveCount(6);
  await expect(
    details.getByRole("link", { name: "Open source", exact: true }),
  ).toHaveCount(0);
  await details
    .getByRole("button", { name: "Copy source ID", exact: true })
    .click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    sourceId,
  );
  expect(searches).toBe(1);
  const retried = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/catalog/search"),
  );
  await page.getByRole("button", { name: "Retry search", exact: true }).click();
  expect((await retried).status()).toBe(200);
  await expect(cards).toHaveCount(3);
  expect(searches).toBe(2);
  await signOut(page);
  await login(page, catalog, "local-admin");
  await expect(cards).toHaveCount(3);
  const source = details.getByRole("link", {
    name: "Open source",
    exact: true,
  });
  await details.locator("summary").click();
  await expect(source).toHaveAttribute(
    "href",
    `${catalog.basePath}/sources/${sourceId}`,
  );
  expect(searches).toBe(3);
  await page.setViewportSize({ width: 320, height: 740 });
  await expect(source).toBeVisible();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
  malformed = true;
  await page.getByRole("button", { name: "Retry search", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText(
    "The request could not be completed. Please try again.",
  );
  await expect(
    page.getByRole("link", { name: "Open source", exact: true }),
  ).toHaveCount(0);
  await expect(cards).toHaveCount(0);
  expect(searches).toBe(4);
  malformed = false;
  omitDiagnostics = true;
  await page
    .getByRole("button", { name: "Start a new search", exact: true })
    .click();
  await expect(cards).toHaveCount(3);
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(
    page.getByText(/Some source queries or results are incomplete/),
  ).toBeVisible();
  await expect(details).toHaveCount(0);
  await expect(source).toHaveCount(0);
  expect(searches).toBe(5);
  const retryWithoutDiagnostics = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/catalog/search"),
  );
  await page.getByRole("button", { name: "Retry search", exact: true }).click();
  expect((await retryWithoutDiagnostics).status()).toBe(200);
  await expect(cards).toHaveCount(3);
  await expect(details).toHaveCount(0);
  expect(searches).toBe(6);
  expect(searchBodies[5]).toEqual(searchBodies[4]);
  expect(searchBodies[5]).not.toHaveProperty("cursor");
});

test("subpath: indexed provenance keeps bounded attribution, permissions and exact group identity before handoff", async ({
  page,
  context,
  catalog,
}) => {
  const sourceIds = Array.from(
    { length: 16 },
    (_, i) => `00000000-0000-7000-8000-${String(i).padStart(12, "0")}`,
  );
  const groupId = "observation-group-v1:sha256:" + "a".repeat(64);
  const hostile = '<img src=x onerror="alert(1)">';
  const indexed = {
    id: "indexed:rendering-control",
    type: "indexed",
    identifier: "urn:air:external.test:default:tools",
    versionPresent: true,
    version: "",
    kind: "mcp_server",
    displayName: "External rendering control",
    description: hostile,
    tags: [],
    score: 1,
    indexed: {
      observationGroupId: groupId,
      normalizedEntryDigest: "sha256:" + "b".repeat(64),
      sourceIds,
      sourceCount: 20,
      sourcesTruncated: true,
      conflict: true,
      sameSourceConflict: true,
    },
  };
  let indexedReads = 0;
  const sourceReads: string[] = [];
  // These static projections qualify rendering, not federation, source health or a stored peer.
  await page.route("**/_catalog/api/v1/catalog/search", async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      json: {
        ...object(await response.json()),
        items: [indexed],
        durableTotal: 1,
        liveCount: 0,
        nextCursor: undefined,
      },
    });
  });
  await page.route(
    "**/_catalog/api/v1/catalog/indexed-record?**",
    async (route) => {
      indexedReads++;
      expect(
        Object.fromEntries(new URL(route.request().url()).searchParams),
      ).toEqual({
        identifier: indexed.identifier,
        versionPresent: "true",
        version: indexed.version,
      });
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json: indexed,
      });
    },
  );
  page.on("request", (request) => {
    if (
      /\/_catalog\/api\/v1\/(sources|observation-groups)(\/|\?|$)/.test(
        request.url(),
      )
    )
      sourceReads.push(request.url());
  });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: catalog.origin,
  });
  await login(page, catalog);
  await page
    .getByRole("button", { name: "External rendering control", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  const provenance = dialog.getByRole("region", {
    name: "Indexed source provenance",
  });
  await expect(
    provenance.getByRole("button", { name: "Copy Source ID", exact: true }),
  ).toHaveCount(16);
  await expect(provenance).toContainText(
    "Reported sources: 20. Source IDs shown: 16.",
  );
  await expect(provenance).toContainText("Additional source IDs were omitted");
  await expect(provenance).toContainText("Conflicting current variants");
  await expect(provenance).toContainText(
    "A source reports multiple current variants",
  );
  await expect(dialog.getByText(hostile, { exact: true })).toBeVisible();
  await expect(dialog.locator("img")).toHaveCount(0);
  await expect(provenance.getByRole("link")).toHaveCount(0);
  await provenance
    .getByRole("button", { name: "Copy Source ID", exact: true })
    .first()
    .click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    sourceIds[0],
  );
  await provenance
    .getByRole("button", { name: "Copy Observation group ID", exact: true })
    .click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    groupId,
  );
  await expect(
    dialog.getByText(/Upstream declarations are not local assessments/),
  ).toBeVisible();
  await page.setViewportSize({ width: 320, height: 740 });
  expect(
    await dialog.evaluate((element) => element.scrollWidth),
  ).toBeLessThanOrEqual(320);
  await dialog
    .getByRole("link", { name: /Open current indexed record/ })
    .click();
  await expect(
    page.getByRole("heading", { name: indexed.displayName, exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: indexed.displayName }),
  ).toBeVisible();
  await expect(
    page
      .getByRole("region", { name: "Indexed source provenance" })
      .getByRole("link"),
  ).toHaveCount(0);
  expect(indexedReads).toBe(1);
  await signOut(page);
  await login(page, catalog, "local-admin");
  await page
    .getByRole("button", { name: indexed.displayName, exact: true })
    .click();
  await expect(
    dialog.getByRole("link", {
      name: "Compare source contributions",
      exact: true,
    }),
  ).toHaveCount(0);
  await expect(
    dialog.getByRole("link", {
      name: `Open source ${sourceIds[0]}`,
      exact: true,
    }),
  ).toHaveAttribute("href", `${catalog.basePath}/sources/${sourceIds[0]}`);
  await dialog
    .getByRole("link", { name: /Open current indexed record/ })
    .click();
  await expect(
    page.getByRole("heading", { name: indexed.displayName, exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", {
      name: "Compare source contributions",
      exact: true,
    }),
  ).toHaveCount(0);
  expect(indexedReads).toBe(2);
  // Empty-version metadata remains inspectable. A nonempty version keeps its
  // exact comparison URL, including punctuation, in both preview and detail.
  indexed.version = "1.0+build";
  await page
    .getByRole("link", { name: "Back to Catalog", exact: true })
    .click();
  await page
    .getByRole("button", { name: indexed.displayName, exact: true })
    .click();
  const expectedGroup = `${catalog.basePath}/observation-group?${new URLSearchParams({ identifier: indexed.identifier, versionPresent: "true", version: indexed.version })}`;
  await expect(
    dialog.getByRole("link", {
      name: "Compare source contributions",
      exact: true,
    }),
  ).toHaveAttribute("href", expectedGroup);
  await dialog
    .getByRole("link", { name: /Open current indexed record/ })
    .click();
  await expect(
    page.getByRole("heading", { name: indexed.displayName, exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", {
      name: "Compare source contributions",
      exact: true,
    }),
  ).toHaveAttribute("href", expectedGroup);
  expect(indexedReads).toBe(3);
  expect(sourceReads).toEqual([]);
});

test("malformed local assessment withholds handoff and an explicit retry restores the genuine record", async ({
  page,
  catalog,
}) => {
  const skill = required(
    catalog.receipt.items.find((item) => item.key === "order-skill"),
  );
  let malformed = true;
  let reads = 0;
  await page.route("**/_catalog/api/v1/catalog/resources/*", async (route) => {
    reads++;
    const response = await route.fetch();
    const payload = object(await response.json());
    await route.fulfill({
      response,
      json: malformed
        ? {
            ...payload,
            evidence: {
              ...object(payload.evidence),
              legalRights: { state: "cleared" },
            },
          }
        : payload,
    });
  });
  await login(page, catalog, "local-reader", `/catalog/${skill.releaseId}`);
  await expect(page.getByRole("alert")).toContainText(
    "The request could not be completed",
  );
  await expect(
    page.getByRole("region", { name: "Resource assessment" }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("region", { name: "Resource handoff" }),
  ).toHaveCount(0);
  expect(reads).toBe(1);
  malformed = false;
  await page.getByRole("button", { name: "Retry record", exact: true }).click();
  const assessment = page.getByRole("region", { name: "Resource assessment" });
  await expect(assessment).toContainText("Completed");
  await expect(
    assessment.getByText("Not assessed", { exact: true }),
  ).toHaveCount(2);
  await expect(
    page.getByRole("region", { name: "Resource handoff" }),
  ).toBeVisible();
  expect(reads).toBe(2);
});

test.describe("Catalog column order and badge layout", () => {
  test.use({ curated: true });
  test("sorts actual results across pages and keeps compact, aligned controls", async ({
    page,
    catalog,
  }) => {
    const requests: Record<string, unknown>[] = [];
    await page.route("**/_catalog/api/v1/catalog/search", async (route) => {
      const query = object(route.request().postDataJSON());
      requests.push(query);
      // Reduce only page size; the real owned API still selects and orders every result.
      await route.continue({
        postData: JSON.stringify({ ...query, limit: 2 }),
      });
    });
    await page.setViewportSize({ width: 1440, height: 1080 });
    await login(page, catalog);
    const firstCard = page.locator("article.resource-card").first();
    const namespace = firstCard.locator(".namespace-filter");
    await expect(namespace).toBeVisible();
    expect((await namespace.boundingBox())!.height).toBeLessThanOrEqual(30);
    const badges = firstCard.locator(".card-badges > span");
    const heights = await badges.evaluateAll((elements) =>
      elements.map((element) => element.getBoundingClientRect().height),
    );
    expect(Math.max(...heights) - Math.min(...heights)).toBeLessThan(2);
    await page.getByRole("button", { name: "Table", exact: true }).click();
    await page
      .getByRole("button", { name: "Sort by name, descending", exact: true })
      .focus();
    await page.keyboard.press("Enter");
    await expect(
      page.getByRole("columnheader", { name: /Name/ }),
    ).toHaveAttribute("aria-sort", "descending");
    const names: string[] = [];
    for (let index = 0; index < 10; index++) {
      const rows = page.locator(".catalog-table tbody tr");
      await expect(rows).toHaveCount(2);
      names.push(...(await rows.locator(".title-button").allTextContents()));
      const next = page.getByRole("button", { name: /Next page/ });
      if (!(await next.count())) break;
      const response = page.waitForResponse(
        (r) =>
          r.url().includes("/_catalog/api/v1/catalog/search") &&
          r.request().method() === "POST",
      );
      await next.click();
      await response;
      await expect(
        page.getByText("The requested page is loading."),
      ).toHaveCount(0);
    }
    expect(names).toHaveLength(12);
    expect(new Set(names).size).toBe(12);
    expect(names).toEqual(
      [...names].sort((a, b) =>
        a.toLowerCase() < b.toLowerCase()
          ? 1
          : a.toLowerCase() > b.toLowerCase()
            ? -1
            : 0,
      ),
    );
    await page
      .getByRole("button", { name: "Sort by kind, ascending", exact: true })
      .click();
    await expect(
      page.getByRole("columnheader", { name: /Kind/ }),
    ).toHaveAttribute("aria-sort", "ascending");
    expect(requests.at(-1)).toHaveProperty("order", "kind_asc");
    expect(requests.at(-1)).not.toHaveProperty("cursor");
    await expect(
      page.getByRole("columnheader", { name: "Status", exact: true }),
    ).toBeVisible();
    const row = page.locator(".catalog-table tbody tr").first();
    const kind = await row.locator(".catalog-kind-badge").boundingBox();
    const state = await row.locator(".state-badge").boundingBox();
    expect(Math.abs(kind!.y - state!.y)).toBeLessThan(2);
    expect(Math.abs(kind!.height - state!.height)).toBeLessThan(2);
    await page.screenshot({
      path: test.info().outputPath("catalog-table-sorted.png"),
    });
    await page.getByText("Save this named search", { exact: true }).click();
    await page
      .getByRole("textbox", { name: "Saved label", exact: true })
      .fill("Catalog by kind");
    await page.getByText("More saved filters", { exact: true }).click();
    await expect(
      page.getByRole("combobox", { name: "Saved sort order", exact: true }),
    ).toHaveValue("kind_asc");
    const savedReply = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname.endsWith(
          "/_catalog/api/v1/personal/items",
        ),
    );
    await page
      .getByRole("button", { name: "Save named search", exact: true })
      .click();
    await expect(
      page.getByRole("dialog").getByText("Sort by", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("dialog").getByText("Kind (A–Z)", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const saved = await savedReply;
    expect(saved.status()).toBe(201);
    const savedId = text(object(await saved.json()).id);
    const loaded = await page.request.get(
      `${catalog.origin}${catalog.basePath}/_catalog/api/v1/personal/items/${encodeURIComponent(savedId)}`,
      { headers: { "X-Catalog-Request": "1" } },
    );
    expect(loaded.status()).toBe(200);
    const savedCriteria = object(object(await loaded.json()).search);
    expect(savedCriteria.order).toBe("kind_asc");
    expect(savedCriteria).not.toHaveProperty("cursor");
    expect(savedCriteria).not.toHaveProperty("limit");
    page.once("dialog", async (dialog) => {
      expect(dialog.type()).toBe("confirm");
      await dialog.accept();
    });
    await page.getByText("Save this named search", { exact: true }).click();
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(390);
    await page.screenshot({
      path: test.info().outputPath("catalog-table-mobile.png"),
    });
  });
});
