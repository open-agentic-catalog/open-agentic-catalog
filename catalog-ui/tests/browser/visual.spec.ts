// Opt-in reference-environment pixel checks. No random IDs/timestamps are masked.
import { test, expect, login, openAccount } from "./fixtures";
import { expectKindPalette } from "./kind-palette";
import { doubleText } from "./text-layout";
import { reviewFixture } from "../unit/overview-fixtures";
import AxeBuilder from "@axe-core/playwright";
import { sourcesFixture, jobsFixture } from "../unit/operations-fixtures";
import {
  endpointOverviewFixture,
  deliveryOverviewFixture,
  notificationsFixture,
} from "../unit/webhook-overview-fixtures";
import {
  administrationFixture,
  feedbackFixture,
  ratingFixture,
} from "../unit/administration-fixtures";

test("visual: Administration counts use exact tables and readable narrow layouts", async ({
  page,
  catalog,
}) => {
  const tenant = (
    await catalog.admin<{ tenantId: string }>("GET", "/api/v1/me")
  ).data.tenantId;
  const access = administrationFixture(),
    feedback = feedbackFixture(),
    ratings = ratingFixture();
  for (const [name, data] of [
    ["administration", access],
    ["feedback", feedback],
    ["ratings", ratings],
  ] as const) {
    data.tenantId = tenant;
    await page.route(`**/_catalog/api/v1/overview/${name}`, (route) =>
      route.fulfill({ json: data }),
    );
  }
  await page.setViewportSize({ width: 1440, height: 3200 });
  await login(page, catalog, "local-admin", "/overview?view=administration");
  const grid = page.locator(".overview-grid");
  await expect(grid.getByText(/14 built-in; 4 custom/)).toBeVisible();
  await expect(grid.getByText(/Oldest submission/)).toBeVisible();
  expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
    violations: [],
    incomplete: [],
  });
  await expect(grid).toHaveScreenshot("overview-administration-light.png");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 4200 });
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
    .toBeLessThanOrEqual(390);
  expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
    violations: [],
    incomplete: [],
  });
  await expect(grid).toHaveScreenshot(
    "overview-administration-dark-narrow.png",
  );
});

test("visual: Operations summaries use readable bars and exact tables", async ({
  page,
  catalog,
}) => {
  await page.clock.setFixedTime(new Date("2026-10-25T12:00:00Z"));
  const tenant = (
    await catalog.admin<{ tenantId: string }>("GET", "/api/v1/overview/sources")
  ).data.tenantId;
  const sources = sourcesFixture(),
    jobs = jobsFixture();
  sources.tenantId = tenant;
  jobs.tenantId = tenant;
  sources.total.value = 5;
  sources.states!.forEach(
    (row) => (row.count = row.state === "active" ? 3 : 1),
  );
  sources.sync!.forEach(
    (row) =>
      (row.count =
        row.state === "not_applicable"
          ? 2
          : row.state === "incomplete"
            ? 0
            : 1),
  );
  jobs.total.value = 5;
  jobs.kinds![0]!.states.forEach(
    (row) =>
      (row.count =
        row.state === "queued"
          ? 2
          : ["waiting", "failed"].includes(row.state)
            ? 1
            : 0),
  );
  jobs.kinds!.push({
    kind: "publication.generate",
    states: jobs.kinds![0]!.states.map((row) => ({
      ...row,
      count: row.state === "succeeded" ? 1 : 0,
    })),
  });
  await page.route("**/_catalog/api/v1/overview/sources", (route) =>
    route.fulfill({ json: sources }),
  );
  await page.route("**/_catalog/api/v1/overview/jobs", (route) =>
    route.fulfill({ json: jobs }),
  );
  await page.setViewportSize({ width: 1440, height: 3000 });
  await login(page, catalog, "local-admin", "/overview?view=operations");
  const grid = page.locator(".overview-grid");
  await expect(grid.getByText("Example source")).toBeVisible();
  await expect(grid.getByText(/^Loaded/)).toBeVisible();
  expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
    violations: [],
    incomplete: [],
  });
  await expect(grid).toHaveScreenshot("overview-operations-light.png");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 4000 });
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
    .toBeLessThanOrEqual(390);
  expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
    violations: [],
    incomplete: [],
  });
  await expect(grid).toHaveScreenshot("overview-operations-dark-narrow.png");
});

test("visual: review activity and age charts remain readable in light and narrow dark layouts", async ({
  page,
  catalog,
}, info) => {
  const response = await catalog.admin<{ tenantId: string }>(
    "GET",
    "/api/v1/overview/reviews",
  );
  await page.setViewportSize({ width: 1440, height: 2000 });
  await page.route("**/_catalog/api/v1/overview/reviews", async (route) =>
    route.fulfill({ json: reviewFixture(response.data.tenantId) }),
  );
  const scope = {
    tenantId: response.data.tenantId,
    scope: "current_access",
    observedAt: "2026-10-25T12:00:00Z",
    selectionLimit: 10000,
  };
  const responses = {
    resources: {
      ...scope,
      total: { value: 12, limited: false },
      kinds: [
        { kind: "agent", count: 6 },
        { kind: "mcp_server", count: 3 },
        { kind: "skill", count: 2 },
        { kind: "plugin", count: 1 },
      ],
    },
    releases: {
      ...scope,
      published: { value: 8, limited: false },
      deprecated: { value: 2, limited: false },
    },
    namespaces: { ...scope, count: { value: 3, limited: false } },
    publishers: { ...scope, count: { value: 2, limited: false } },
    indexed: { ...scope, count: 14, limited: false },
    publication: {
      ...scope,
      selectionLimit: 64,
      hosts: [
        {
          host: "catalog.example",
          mode: "anonymous",
          status: "unpublished",
          mismatches: [],
        },
      ],
    },
  };
  await login(page, catalog, "local-admin", "/overview");
  const panel = page.getByRole("region", {
    name: "Reviews",
    exact: true,
  });
  await expect(panel.getByRole("img")).toBeVisible();
  await expect
    .poll(() =>
      panel
        .locator(".operation-bars .overview-track span")
        .first()
        .evaluate((node) => getComputedStyle(node).backgroundColor),
    )
    .not.toBe("rgba(0, 0, 0, 0)");
  expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
    violations: [],
    incomplete: [],
  });
  const tickOffsets = await panel
    .locator(".review-date-axis time")
    .evaluateAll((ticks) =>
      ticks.map((tick, ordinal) => {
        const index = [0, 7, 14, 21, 29][ordinal]!;
        const submitted = document
          .querySelectorAll(".review-submitted-bar")
          .item(index)
          .getBoundingClientRect();
        const decided = document
          .querySelectorAll(".review-decided-bar")
          .item(index)
          .getBoundingClientRect();
        const label = tick.getBoundingClientRect();
        const anchor =
          ordinal === 0
            ? label.left
            : ordinal === ticks.length - 1
              ? label.right
              : (label.left + label.right) / 2;
        return Math.abs(anchor - (submitted.left + decided.right) / 2);
      }),
    );
  expect(tickOffsets).toHaveLength(5);
  for (const offset of tickOffsets) expect(offset).toBeLessThan(1);
  const desktopDateLines = await panel
    .locator(".review-date-axis time")
    .evaluateAll((ticks) =>
      ticks.map((tick) => {
        const text = document.createRange();
        text.selectNodeContents(tick);
        return text.getClientRects().length;
      }),
    );
  expect(desktopDateLines).toEqual([1, 1, 1, 1, 1]);
  await expect(panel).toHaveScreenshot("overview-reviews-light.png");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 1800 });
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
    .toBeLessThanOrEqual(390);
  await expect(panel).toHaveScreenshot("overview-reviews-dark-narrow.png");
  await expect
    .poll(() =>
      panel
        .locator(".review-date-axis time")
        .first()
        .evaluate((node) => node.getBoundingClientRect().height),
    )
    .toBeGreaterThanOrEqual(9);
  expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
    violations: [],
    incomplete: [],
  });
  await page.setViewportSize({ width: 1440, height: 4200 });
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Light", exact: true }).check();
  await page.keyboard.press("Escape");
  for (const [name, value] of Object.entries(responses))
    await page.route(`**/_catalog/api/v1/overview/${name}`, (route) =>
      route.fulfill({ json: value }),
    );
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(page.locator(".overview-kind dd")).toHaveText([
    "6",
    "3",
    "2",
    "1",
  ]);
  await page.locator(".overview-grid").screenshot({
    path: info.outputPath("overview-content-documentation.png"),
  });
  await page.emulateMedia({ forcedColors: "active", reducedMotion: "reduce" });
  const activity = await page
    .locator(".review-submitted-bar")
    .first()
    .evaluate((node) => {
      const style = getComputedStyle(node);
      return {
        fill: style.fill,
        foreground: getComputedStyle(node.closest(".overview-panel")!).color,
      };
    });
  expect(activity.fill).toBe(activity.foreground);
  await expect(page.locator(".review-decided-bar").first()).toHaveCSS(
    "fill",
    "none",
  );
  await page.emulateMedia({ forcedColors: "none" });
  await page.setViewportSize({ width: 320, height: 1800 });
  await doubleText(page);
  await expect(panel.locator(".review-date-axis time:visible")).toHaveCount(3);
  const narrowDateLines = await panel
    .locator(".review-date-axis time:visible")
    .evaluateAll((ticks) =>
      ticks.map((tick) => {
        const text = document.createRange();
        text.selectNodeContents(tick);
        return text.getClientRects().length;
      }),
    );
  for (const lines of narrowDateLines) {
    expect(lines).toBeGreaterThanOrEqual(1);
    expect(lines).toBeLessThanOrEqual(2);
  }
  const tickBounds = await panel
    .locator(".review-date-axis time:visible")
    .evaluateAll((ticks) =>
      ticks.map((tick) => {
        const bounds = tick.getBoundingClientRect();
        return { left: bounds.left, right: bounds.right };
      }),
    );
  for (let index = 1; index < tickBounds.length; index++)
    expect(tickBounds[index]!.left).toBeGreaterThanOrEqual(
      tickBounds[index - 1]!.right,
    );
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
  expect(
    await panel.locator(".review-date-axis").evaluate((axis) => {
      const bounds = axis.getBoundingClientRect();
      return [...axis.querySelectorAll("time")].every((time) => {
        const text = document.createRange();
        text.selectNodeContents(time);
        return [...text.getClientRects()].every(
          (rect) =>
            rect.left >= bounds.left - 1 && rect.right <= bounds.right + 1,
        );
      });
    }),
  ).toBe(true);
  expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
    violations: [],
    incomplete: [],
  });
});

test("visual: Overview kind cards share the existing palette", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/overview");
  const cards = page
    .getByRole("region", { name: "Local resources", exact: true })
    .locator(".overview-kinds");
  await expect(cards.locator(".overview-kind")).toHaveCount(4);
  await expectKindPalette(cards);
  await expect(cards).toHaveScreenshot("overview-kinds-light.png");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 320, height: 1000 });
  await expectKindPalette(cards);
  await expect(cards).toHaveScreenshot("overview-kinds-dark-narrow.png");
});

test("visual: Explore kind markers use the Catalog palette", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-reader", "/explore");
  const browse = page.getByRole("region", {
    name: "Local browse results",
    exact: true,
  });
  await expect(browse.locator(".catalog-kind-badge.agent")).toBeVisible();
  await expectKindPalette(browse);
  expect(
    await page
      .getByRole("heading", { name: "Explore local catalog", exact: true })
      .locator("svg")
      .innerHTML(),
  ).toBe(
    await page
      .locator('.sidebar a[aria-current="page"]')
      .locator("svg")
      .innerHTML(),
  );
  await page.mouse.move(0, 0);
  await expect(browse).toHaveScreenshot("explore-kinds-light.png");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 1000 });
  await expectKindPalette(browse);
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
    .toBeLessThanOrEqual(390);
  await expect(browse).toHaveScreenshot("explore-kinds-dark-narrow.png");
});

test("visual: API endpoint sections and readable authentication", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-reader", "/api-reference");
  await page
    .getByRole("combobox", { name: "API area", exact: true })
    .selectOption("Identity");
  await page
    .getByRole("searchbox", { name: "Search API operations" })
    .fill("suspendMembership");
  const operation = page.locator(
    'details[data-operation-id="suspendMembership"]',
  );
  await expect(operation).toHaveCount(1);
  await page.mouse.move(0, 0);
  await expect(page).toHaveScreenshot("api-endpoints-light.png", {
    fullPage: true,
  });
  await operation.locator(":scope > summary").click();
  const authentication = operation.getByRole("region", {
    name: "Operation authentication",
    exact: true,
  });
  await expect(authentication).toContainText("HTTP Bearer token");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 1000 });
  await authentication.evaluate((element) =>
    element.scrollIntoView({ block: "center" }),
  );
  await expect
    .poll(() =>
      authentication.evaluate((element) => {
        const box = element.getBoundingClientRect();
        return (
          box.top >=
            document.querySelector(".topbar")!.getBoundingClientRect().bottom &&
          box.bottom <= innerHeight
        );
      }),
    )
    .toBe(true);
  await expect(authentication).toHaveScreenshot(
    "api-authentication-dark-narrow.png",
  );
});

test("visual: API definition index and declared schema facts", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-reader", "/api-reference");
  await page.getByRole("button", { name: /^Browse definitions/ }).click();
  await page
    .getByRole("searchbox", { name: "Search definitions" })
    .fill("ResourceKind");
  await page
    .getByRole("combobox", { name: "Component kind", exact: true })
    .selectOption("schemas");
  await page
    .getByRole("button", { name: "Open schemas/ResourceKind", exact: true })
    .click();
  const summary = page.getByRole("region", {
    name: "Readable schema summary",
    exact: true,
  });
  await expect(summary).toContainText("agent");
  await expect(summary).toContainText("mcp_server");
  // Include the actual index row, paging controls and complete small schema.
  await page.setViewportSize({ width: 1440, height: 1400 });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.mouse.move(0, 0);
  await expect(page).toHaveScreenshot("api-definitions-light.png");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 1000 });
  await summary.evaluate((element) =>
    element.scrollIntoView({ block: "center" }),
  );
  await expect
    .poll(() =>
      summary.evaluate((element) => {
        const box = element.getBoundingClientRect();
        return (
          box.top >=
            document.querySelector(".topbar")!.getBoundingClientRect().bottom &&
          box.bottom <= innerHeight
        );
      }),
    )
    .toBe(true);
  await expect(summary).toHaveScreenshot("api-schema-dark-narrow.png");
});

test("visual: exact review navigation", async ({ page, catalog }) => {
  await login(page, catalog, "local-template-reviewer", "/reviews");
  await expect(
    page.getByRole("button", { name: "Apply review filters", exact: true }),
  ).toBeEnabled();
  await expect(
    page.getByRole("form", { name: "Review queue filters", exact: true }),
  ).toHaveScreenshot("review-filters-light.png");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(
    page.getByRole("form", {
      name: "Open exact review submission",
      exact: true,
    }),
  ).toHaveScreenshot("review-opener-dark-narrow.png");
});

test("visual: native preparation and captured Listing metadata", async ({
  page,
  catalog,
}) => {
  const item = catalog.receipt.items.find(
    (entry) => entry.key === "draft-agent",
  )!;
  await login(
    page,
    catalog,
    "local-admin",
    `/versions/new?resource=${item.resourceId}`,
  );
  const help = page.getByRole("region", {
    name: "Native input guidance",
    exact: true,
  });
  await expect(help).toContainText("A2A Agent Card");
  await expect(help).toHaveScreenshot("native-preparation-light.png");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 844 });
  await help.locator("summary").focus();
  await page.keyboard.press("Enter");
  await expect(
    help.getByText(/Where native metadata declares a version/),
  ).toBeVisible();
  await expect(help).toHaveScreenshot("native-preparation-dark-narrow.png");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Light", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(
    `${catalog.origin}${catalog.basePath}/listings/${item.listingId}`,
  );
  await page
    .getByRole("button", { name: "New listing revision", exact: true })
    .click();
  await page
    .getByLabel("Display name", { exact: true })
    .fill("Travel planning assistant");
  await page
    .getByRole("textbox", { name: "Description", exact: true })
    .fill("Helps prepare a trip itinerary.");
  await page
    .getByRole("button", { name: "Create listing revision", exact: true })
    .click();
  const review = page.getByRole("region", {
    name: "Prepared listing changes",
    exact: true,
  });
  await expect(review).toHaveScreenshot("listing-prepared-light.png");
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 844 });
  await page
    .getByRole("button", { name: "Create listing revision", exact: true })
    .click();
  // A full review is taller than the scrollable dialog. Capture one complete
  // field, avoiding a clipped element image that includes the background page.
  await expect(
    review.getByRole("region", { name: "Changed description", exact: true }),
  ).toHaveScreenshot("listing-prepared-dark-narrow.png");
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
});

test("visual: light search shell and results", async ({ page, catalog }) => {
  await login(page, catalog);
  await expect(page).toHaveTitle("Catalog | Open Agentic Catalog");
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  await page.mouse.move(0, 0);
  await expect(page).toHaveScreenshot("search-light.png", { fullPage: true });
  await openAccount(page);
  await expect(page.locator(".account-panel")).toHaveScreenshot(
    "account-light.png",
  );
  await page.keyboard.press("Escape");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.getByRole("radio", { name: "Compact", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 844 });
  const kinds = page.getByRole("group", { name: "Resource kinds" });
  for (const name of ["Agents", "MCP Servers", "Skills", "Plugins"]) {
    await expect(
      kinds.getByRole("button", { name, exact: true }),
    ).toBeInViewport();
  }
  const widths = await kinds
    .locator("button[data-kind]")
    .evaluateAll((buttons) =>
      buttons.map((button) => button.getBoundingClientRect().width),
    );
  expect(widths).toHaveLength(4);
  expect(new Set(widths).size).toBe(1);
  await expect(page).toHaveScreenshot("search-dark-compact-narrow.png", {
    fullPage: true,
  });
  await openAccount(page);
  await expect(page.locator(".account-panel")).toHaveScreenshot(
    "account-dark-compact-narrow.png",
  );
});

test("visual: dark management table", async ({ page, catalog }) => {
  await login(page, catalog, "local-admin", "/namespaces");
  await expect(page.locator(".workspace-table tbody tr")).toHaveCount(1);
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await expect(page).toHaveScreenshot("namespaces-dark.png", {
    fullPage: true,
  });
});

test("visual: current policy administration", async ({ page, catalog }) => {
  await catalog.admin("POST", "/api/v1/access/roles", {
    name: "Scoped metadata reader",
    permissions: ["namespace.read", "resource.read"],
    reason: "Disposable documentation screenshot",
  });
  await login(page, catalog, "local-admin", "/access-roles");
  await expect(
    page.getByRole("link", { name: "Scoped metadata reader", exact: true }),
  ).toBeVisible();
  await expect(page).toHaveScreenshot("access-roles-light.png", {
    fullPage: true,
  });
});

test("visual: multi-kind and tag filters at desktop and narrow widths", async ({
  page,
  catalog,
}) => {
  await login(page, catalog);
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page.getByRole("checkbox", { name: "Agents", exact: true }).check();
  await page
    .getByRole("checkbox", { name: "MCP Servers", exact: true })
    .check();
  await page.getByRole("checkbox", { name: "orders", exact: true }).check();
  await page
    .getByRole("button", { name: "Close filters", exact: true })
    .focus();
  // Checking an offscreen tag scrolls the panel. Capture its ordinary top view,
  // not the lower publisher suggestions containing generated fixture UUIDs.
  await page.locator(".filter-panel-body").evaluate((element) => {
    element.scrollTop = 0;
  });
  await expect(
    page.getByRole("region", { name: "Filters", exact: true }),
  ).toHaveScreenshot("filters-desktop.png");
  await page.setViewportSize({ width: 390, height: 740 });
  await expect(
    page.getByRole("button", { name: "Apply filters", exact: true }),
  ).toBeInViewport();
  await expect(page.getByRole("dialog")).toHaveScreenshot("filters-narrow.png");
});

test("visual: narrow authoring and confirmation", async ({ page, catalog }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  // This display-only confirmation is never sent. A fixed read fixture keeps
  // its exact destination visible without masking a generated identity.
  const namespace = await catalog.admin<Record<string, unknown>>(
    "GET",
    `/api/v1/namespaces/${catalog.receipt.namespaceId}`,
  );
  const namespaceId = "00000000-0000-7000-8000-000000000188";
  await page.route("**/_catalog/api/v1/namespaces?*", (route) =>
    route.fulfill({
      json: { items: [{ ...namespace.data, id: namespaceId }] },
    }),
  );
  await login(page, catalog, "local-admin", "/resources/new");
  await page
    .getByRole("textbox", { name: "Canonical resource identifier" })
    .fill("urn:air:catalog.example.com:demo:visual-agent");
  // The sign-in click must not leave an unrelated form button hovered.
  await page.mouse.move(0, 0);
  // Filling a below-fold control can scroll the sticky header. Capture the
  // complete page from its top while preserving the input's focus ring.
  await page.evaluate(() => window.scrollTo(0, 0));
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
  expect(
    await page
      .getByRole("banner")
      .evaluate((header) => header.getBoundingClientRect().top),
  ).toBe(0);
  await expect(page).toHaveScreenshot("authoring-narrow.png", {
    fullPage: true,
  });
  // Keep the initial pending selection and the captured target separately.
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(namespaceId);
  await page
    .getByRole("button", { name: "Create resource", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog).toHaveScreenshot("confirmation-narrow.png");
});

test("visual: consumer handoff in light and dark narrow layouts", async ({
  page,
  catalog,
}, info) => {
  const skill = catalog.receipt.items.find(
    (item) => item.key === "order-skill",
  );
  expect(skill?.releaseId).toBeTruthy();
  await login(page, catalog, "local-reader", `/catalog/${skill!.releaseId}`);
  const handoff = page.getByRole("region", {
    name: "Resource handoff",
    exact: true,
  });
  const assessment = page.getByRole("region", {
    name: "Resource assessment",
    exact: true,
  });
  // Stable reported states only; this capture contains no fixture IDs or dates.
  await assessment.evaluate((element) =>
    element.scrollIntoView({ block: "center" }),
  );
  await expect(assessment).toHaveScreenshot("resource-assessment-light.png");
  await expect(
    handoff.getByRole("heading", { name: "Use this resource", exact: true }),
  ).toBeVisible();
  // The consumer section contains stable references. Dynamic release dates/IDs
  // remain outside this capture; no masking or fixture-clock override is used.
  await expect(handoff).toHaveScreenshot("resource-handoff-light.png");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  // Fit the complete long section below the sticky header in this element capture.
  // Ordinary narrow/320px journeys retain their shorter viewport and scroll checks.
  await page.setViewportSize({ width: 390, height: 1800 });
  await assessment.evaluate((element) =>
    element.scrollIntoView({ block: "center" }),
  );
  await expect(assessment).toHaveScreenshot(
    "resource-assessment-dark-narrow.png",
  );
  await handoff.evaluate((element) =>
    element.scrollIntoView({ block: "center" }),
  );
  await expect
    .poll(() =>
      handoff.evaluate((element) => {
        const box = element.getBoundingClientRect();
        return (
          box.top >=
            document.querySelector(".topbar")!.getBoundingClientRect().bottom &&
          box.bottom <= innerHeight
        );
      }),
    )
    .toBe(true);
  await expect(handoff).toHaveScreenshot("resource-handoff-dark-narrow.png");
  // Retain an ordinary synthetic preview capture for explicit documentation review.
  await page.getByRole("link", { name: /Back to Catalog/ }).click();
  await page.setViewportSize({ width: 1440, height: 1100 });
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Light", exact: true }).check();
  await page.keyboard.press("Escape");
  await page
    .getByRole("button", { name: "Order exception Agent", exact: true })
    .click();
  await expect(
    page
      .getByRole("dialog")
      .getByRole("heading", { name: "Use this resource", exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: info.outputPath("catalog-preview.png"),
    fullPage: true,
  });
});

test("visual: Operational Job outcomes keep contextual colors", async ({
  page,
  catalog,
}) => {
  const stamp = "2026-10-20T00:00:00Z";
  const states = [
    "queued",
    "running",
    "waiting",
    "succeeded",
    "failed",
    "cancelled",
    "dead_letter",
  ];
  await page.route("**/_catalog/api/v1/jobs?**", async (route) => {
    await route.fulfill({
      json: {
        items: states.map((state, index) => ({
          id: `00000000-0000-7000-8000-${String(4000 + index).padStart(12, "0")}`,
          tenantId: "00000000-0000-7000-8000-000000000001",
          kind: "source.sync",
          state,
          progress: state === "running" ? 25 : 0,
          priority: 0,
          attempt: 1,
          attemptLimit: 3,
          notBefore: stamp,
          createdAt: stamp,
          updatedAt: stamp,
          cancellationRequested: false,
        })),
      },
    });
  });
  await login(page, catalog, "local-admin", "/jobs");
  const table = page.getByRole("region", {
    name: "Job inventory",
    exact: true,
  });
  await expect(table.getByRole("row")).toHaveCount(8);
  for (const [state, tone] of [
    ["queued", "neutral"],
    ["running", "info"],
    ["waiting", "warning"],
    ["succeeded", "success"],
    ["failed", "danger"],
    ["cancelled", "neutral"],
    ["dead_letter", "danger"],
  ]) {
    await expect(table.locator(`[data-state="${state}"]`)).toHaveAttribute(
      "data-tone",
      tone!,
    );
  }
  await page.mouse.move(0, 0);
  await expect(table).toHaveScreenshot("operations-jobs-light.png");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 1000 });
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
    .toBeLessThanOrEqual(390);
  // Keep the state column visible inside the existing horizontal table wrapper.
  await table.evaluate((node) => {
    node.scrollLeft = (
      node.querySelector("th:nth-child(2)") as HTMLElement
    ).offsetLeft;
  });
  await page.evaluate(() => window.scrollTo(0, 0));
  await expect(page).toHaveScreenshot("operations-jobs-dark-narrow.png", {
    fullPage: true,
  });
});

test("visual: Settings actions distinguish save from confirmed reset", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/settings");
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Review deployment defaults");
  const save = page.getByRole("button", { name: "Save settings", exact: true });
  const reset = page.getByRole("button", {
    name: "Use deployment defaults for all settings",
    exact: true,
  });
  await expect(save).toHaveClass("primary");
  await expect(reset).toHaveClass("warning");
  await expect(
    page
      .getByRole("group", { name: "Tenant settings values", exact: true })
      .locator('[data-tone="neutral"]'),
  ).toHaveCount(3);
  await page.mouse.move(0, 0);
  await expect(save.locator("..")).toHaveScreenshot(
    "settings-actions-light.png",
  );
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 1000 });
  const before = await catalog.admin("GET", "/api/v1/tenant-settings");
  await reset.click();
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByRole("button", { name: "Confirm", exact: true }),
  ).toHaveClass("warning");
  await page.mouse.move(0, 0);
  await expect(dialog).toHaveScreenshot("settings-reset-dark-narrow.png");
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  const after = await catalog.admin("GET", "/api/v1/tenant-settings");
  expect(after.data).toEqual(before.data);
  expect(after.etag).toBe(before.etag);
});

test("visual: public About and Getting started use small familiar symbols", async ({
  page,
  catalog,
}) => {
  // Select the maintained local-manual link policy for this display fixture.
  await page.route("**/runtime.json", async (route) => {
    const response = await route.fetch();
    const runtime = (await response.json()) as object;
    await route.fulfill({
      response,
      json: { ...runtime, documentationUrl: "/docs/" },
    });
  });
  await page.goto(`${catalog.origin}${catalog.basePath}/about`);
  const about = page.locator("main .workspace-panel").first();
  await expect(about.locator("li > svg")).toHaveCount(4);
  await page.mouse.move(0, 0);
  await expect(about).toHaveScreenshot("about-kinds-light.png");
  await login(page, catalog, "local-reader");
  await page.goto(`${catalog.origin}${catalog.basePath}/getting-started`);
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.getByRole("radio", { name: "Compact", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 1000 });
  const workflow = page.locator(".help-workflows > section").first();
  await expect(
    workflow
      .getByRole("link", { name: "Open Catalog", exact: true })
      .locator("svg"),
  ).toHaveAttribute("aria-hidden", "true");
  await expect(
    workflow.getByRole("link", { name: /Read this workflow/ }).locator("svg"),
  ).toHaveAttribute("aria-hidden", "true");
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
    .toBeLessThanOrEqual(390);
  await expect(workflow).toHaveScreenshot("getting-started-dark-narrow.png");
});

test.describe("enabled webhook overview", () => {
  test.use({ webhooks: true });
  test("visual: webhook counts and recent failures stay readable", async ({
    page,
    catalog,
  }) => {
    await page.clock.setFixedTime(new Date("2026-10-25T12:00:00Z"));
    const tenant = (
      await catalog.admin<{ tenantId: string }>("GET", "/api/v1/me")
    ).data.tenantId;
    const endpoints = endpointOverviewFixture(),
      deliveries = deliveryOverviewFixture();
    endpoints.tenantId = tenant;
    deliveries.tenantId = tenant;
    await page.route("**/_catalog/api/v1/overview/webhook-endpoints", (route) =>
      route.fulfill({ json: endpoints }),
    );
    await page.route(
      "**/_catalog/api/v1/overview/webhook-deliveries",
      (route) => route.fulfill({ json: deliveries }),
    );
    await page.route("**/_catalog/api/v1/webhooks/reconciliation", (route) =>
      route.fulfill({ json: notificationsFixture() }),
    );
    await page.setViewportSize({ width: 1440, height: 2200 });
    await login(page, catalog, "local-template-webhook-admin", "/overview");
    const grid = page.locator(".overview-grid");
    await expect(
      grid.getByText("Publication receiver", { exact: true }),
    ).toBeVisible();
    await expect(
      grid.getByText("At least 1,000", { exact: true }),
    ).toBeVisible();
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    await expect(grid).toHaveScreenshot("overview-webhooks-light.png");
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: "Dark", exact: true }).check();
    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 390, height: 5000 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    await expect(grid).toHaveScreenshot("overview-webhooks-dark-narrow.png");
  });
});

test("visual: MCP client configuration controls in light and narrow dark layouts", async ({
  page,
  catalog,
}) => {
  const item = catalog.receipt.items.find((item) => item.key === "order-mcp")!;
  await login(page, catalog, "local-reader", `/catalog/${item.releaseId}`);
  const panel = page.getByRole("region", {
    name: "MCP client configuration",
    exact: true,
  });
  await expect(panel).toBeVisible();
  await expect(panel).toHaveScreenshot("mcp-client-controls-light.png");
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  // Keep the complete doubled-text panel below the sticky header.
  // Ordinary MCP journeys separately verify the shorter 320px viewport.
  await page.setViewportSize({ width: 390, height: 1800 });
  await doubleText(page);
  await panel.evaluate((element) =>
    element.scrollIntoView({ block: "center" }),
  );
  await expect
    .poll(() =>
      panel.evaluate((element) => {
        const box = element.getBoundingClientRect();
        return (
          box.top >=
            document.querySelector(".topbar")!.getBoundingClientRect().bottom &&
          box.bottom <= innerHeight
        );
      }),
    )
    .toBe(true);
  await expect(panel).toHaveScreenshot("mcp-client-controls-dark-narrow.png");
});
