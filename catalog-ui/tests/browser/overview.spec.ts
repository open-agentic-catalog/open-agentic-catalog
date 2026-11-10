import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, signOut } from "./fixtures";
import { doubleText } from "./text-layout";
import { object } from "./json";
import { tabTo } from "./keyboard";
import type { components } from "../../src/api/schema";
import { sourcesFixture, jobsFixture } from "../unit/operations-fixtures";

for (const base of ["root", "subpath"]) {
  test(`${base}: all Overview tabs keep bounded reads, keyboard controls and readable high-contrast layouts`, async ({
    page,
    catalog,
  }) => {
    const reads: string[] = [];
    const mutations: string[] = [];
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (
        path.includes("/_catalog/api/") &&
        !["GET", "HEAD"].includes(request.method())
      )
        mutations.push(path);
      if (path.includes("/api/v1/overview/"))
        reads.push(path.split("/").at(-1)!);
    });
    const sections = {
      content: [
        "indexed",
        "namespaces",
        "publication",
        "publishers",
        "releases",
        "resources",
        "reviews",
      ],
      operations: ["jobs", "sources"],
      administration: ["administration", "feedback", "ratings"],
    };
    await login(page, catalog, "local-admin", "/overview");
    for (const [view, expected] of Object.entries(sections)) {
      if (view !== "content") {
        reads.length = 0;
        await page
          .getByRole("link", {
            name: view === "operations" ? "Operations" : "Administration",
            exact: true,
          })
          .click();
      }
      await expect.poll(() => [...reads].sort()).toEqual(expected);
      await doubleText(page);
      for (const scheme of ["light", "dark"] as const) {
        await page.emulateMedia({
          colorScheme: scheme,
          reducedMotion: "reduce",
        });
        await page.setViewportSize({ width: 320, height: 900 });
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth + 1,
          ),
        ).toBe(true);
        const heading = page.locator(".page-heading");
        const headingBounds = await heading.boundingBox();
        const copyBounds = await heading.locator(":scope > div").boundingBox();
        const refreshBounds = await heading
          .getByRole("button", { name: "Refresh", exact: true })
          .boundingBox();
        expect(headingBounds).not.toBeNull();
        expect(copyBounds).not.toBeNull();
        expect(refreshBounds).not.toBeNull();
        expect(copyBounds!.width).toBeGreaterThanOrEqual(
          headingBounds!.width - 2,
        );
        expect(refreshBounds!.y).toBeGreaterThanOrEqual(
          copyBounds!.y + copyBounds!.height,
        );
        expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
          violations: [],
          incomplete: [],
        });
      }
      await page.emulateMedia({
        forcedColors: "active",
        reducedMotion: "reduce",
      });
      await expect(page.locator(".overview-grid")).toBeVisible();
      await expect(
        page.locator("summary").filter({ hasText: "as a table" }),
      ).toHaveCount(0);
      if (view === "content") {
        const values = page
          .getByRole("region", { name: "Local resources", exact: true })
          .locator(".overview-kind dd");
        await expect(values).toHaveCount(4);
        await expect(values.first()).toBeVisible();
      }
      if (view === "operations")
        await expect(page.locator(".job-overview-bars")).toHaveCount(0);
      const refresh = page.getByRole("button", {
        name: "Refresh",
        exact: true,
      });
      await tabTo(page, refresh);
      const before = reads.length;
      await page.keyboard.press("Enter");
      await expect.poll(() => reads.length).toBe(before + expected.length);
      expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
        violations: [],
        incomplete: [],
      });
      await page.emulateMedia({ forcedColors: "none" });
      await page.evaluate(() => {
        for (const node of document.querySelectorAll<HTMLElement>("body *"))
          node.style.fontSize = "";
      });
    }
    expect(mutations).toEqual([]);
  });
}

test("Source administrators use Operations without Content reads", async ({
  page,
  catalog,
  network,
}) => {
  const reads: string[] = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (path.includes("/_catalog/api/v1/overview/"))
      reads.push(path.split("/").at(-1)!);
  });
  await login(
    page,
    catalog,
    "local-template-source-admin",
    "/overview?view=operations",
  );
  await expect(
    page.getByRole("region", { name: "Federation sources", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Operations", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  await expect(
    page.getByRole("region", { name: "Job queues", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "External records", exact: true }),
  ).toHaveCount(0);
  await expect.poll(() => reads.length).toBe(2);
  expect([...reads].sort()).toEqual(["jobs", "sources"]);
  let denied = true;
  let sessions = 0;
  let recoveryDenial = false;
  let release = () => {};
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  for (const summary of ["sources", "jobs"]) {
    network.expectFailure("GET", `/_catalog/api/v1/overview/${summary}`, 403);
    await page.route(
      `**/_catalog/api/v1/overview/${summary}`,
      async (route) => {
        if (!denied && !(summary === "jobs" && recoveryDenial))
          return route.continue();
        if (!denied) recoveryDenial = false;
        await route.fulfill({
          status: 403,
          contentType: "application/problem+json",
          json: {
            type: "about:blank",
            title: "Forbidden",
            status: 403,
            code: "FORBIDDEN",
          },
        });
      },
    );
  }
  network.expectFailure("GET", "/auth/session", 503);
  await page.route("**/auth/session", async (route) => {
    sessions++;
    if (!denied) return route.continue();
    await pending;
    await route.fulfill({
      status: 503,
      contentType: "application/problem+json",
      json: {
        type: "about:blank",
        title: "Temporarily unavailable",
        status: 503,
      },
    });
  });
  const refresh = page.getByRole("button", { name: "Refresh", exact: true });
  try {
    await refresh.click();
    await expect.poll(() => reads.length).toBe(4);
    await expect.poll(() => sessions).toBe(1);
    await expect(
      page.getByText("Your current access does not permit this action.", {
        exact: true,
      }),
    ).toHaveCount(2);
    release();
    await expect.poll(() => network.results()).toEqual([]);
    await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Resources", exact: true }),
    ).toHaveCount(0);
    expect(reads.length).toBe(4);
    expect(sessions).toBe(1);
    denied = false;
    // One new refusal initiates a real current-access refresh; summaries then recover.
    network.expectFailure("GET", "/_catalog/api/v1/overview/jobs", 403);
    recoveryDenial = true;
    await refresh.click();
    await expect.poll(() => sessions).toBe(2);
    await refresh.click();
    await expect.poll(() => reads.length).toBe(8);
    await expect(
      page.getByRole("region", { name: "Federation sources", exact: true }),
    ).not.toContainText("Your current access does not permit this action.");
    await expect(
      page.getByRole("region", { name: "Job queues", exact: true }),
    ).not.toContainText("Your current access does not permit this action.");
    await expect(
      page
        .getByRole("region", { name: "Federation sources", exact: true })
        .getByText("No sources configured."),
    ).toBeVisible();
    await expect(
      page
        .getByRole("region", { name: "Job queues", exact: true })
        .locator("dd"),
    ).toHaveText(["0", "0"]);
    expect(sessions).toBe(2);
    expect(reads.length).toBe(8);
  } finally {
    release();
  }
});

for (const base of ["root", "subpath"]) {
  test(`${base}: Operations summaries match their responses and apply exact directory filters`, async ({
    page,
    catalog,
  }) => {
    const mutations: string[] = [];
    const summaryReads = { sources: 0, jobs: 0 };
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (path.endsWith("/api/v1/overview/sources")) summaryReads.sources++;
      if (path.endsWith("/api/v1/overview/jobs")) summaryReads.jobs++;
      if (
        request.url().includes("/_catalog/api/") &&
        !["GET", "HEAD"].includes(request.method())
      )
        mutations.push(request.url());
    });
    const sourceRead = page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith("/api/v1/overview/sources"),
    );
    const jobRead = page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith("/api/v1/overview/jobs"),
    );
    await login(page, catalog, "local-admin", "/overview?view=operations");
    const source = (await (
      await sourceRead
    ).json()) as components["schemas"]["SourceOverview"];
    const jobs = (await (
      await jobRead
    ).json()) as components["schemas"]["JobOverview"];
    const sources = page.getByRole("region", {
      name: "Federation sources",
      exact: true,
    });
    if (source.total.value === 0)
      await expect(sources.getByText("No sources configured.")).toBeVisible();
    else
      await expect(sources.locator(".overview-number")).toHaveText(
        String(source.total.value),
      );
    const queues = page.getByRole("region", {
      name: "Job queues",
      exact: true,
    });
    await expect(queues.locator("dd")).toHaveText([
      String(jobs.total.value),
      String(jobs.due.value),
    ]);
    const rows = queues.getByRole("table").locator("tbody tr");
    if (jobs.total.value === 0) {
      expect(
        jobs.kinds?.every((kind) =>
          kind.states.every((row) => row.count === 0),
        ),
      ).toBe(true);
      await expect(queues.getByRole("table")).toHaveCount(0);
      await expect(queues.getByText("No jobs to show.")).toBeVisible();
    } else {
      await expect(rows).toHaveCount(jobs.kinds?.length ?? 0);
      for (const [index, kind] of (jobs.kinds ?? []).entries())
        await expect(rows.nth(index).getByRole("cell")).toHaveText(
          kind.states.map((row) => String(row.count)),
        );
    }
    await expect(
      page.getByRole("region", { name: "Search indexing", exact: true }),
    ).toBeVisible();
    expect(mutations).toEqual([]);
    // Reject contradictory counts even when a successful reply omits its rows.
    const invalidJobs = {
      ...jobs,
      total: { value: 0, limited: false },
      due: { value: 1, limited: false },
      oldestDueAt: jobs.observedAt,
    };
    delete invalidJobs.kinds;
    let corrupt = true;
    await page.route("**/_catalog/api/v1/overview/jobs", (route) =>
      route.fulfill({ json: corrupt ? invalidJobs : jobs }),
    );
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(queues.getByRole("alert")).toBeVisible();
    await expect(queues.locator(".overview-facts dd")).toHaveCount(0);
    await expect(
      queues.getByRole("table", { name: "Jobs by kind and state" }),
    ).toHaveCount(0);
    if (source.total.value === 0)
      await expect(sources.getByText("No sources configured.")).toBeVisible();
    else
      await expect(sources.locator(".overview-number")).toHaveText(
        String(source.total.value),
      );
    const readsBeforeReload = { ...summaryReads };
    corrupt = false;
    await queues.getByRole("button", { name: "Reload", exact: true }).click();
    await expect(queues.locator("dd")).toHaveText([
      String(jobs.total.value),
      String(jobs.due.value),
    ]);
    expect(summaryReads).toEqual({
      sources: readsBeforeReload.sources,
      jobs: readsBeforeReload.jobs + 1,
    });
    await expect(queues.getByRole("alert")).toHaveCount(0);
    // Populated controls qualify exact links after the actual empty/count reads.
    // They do not qualify federation ingestion or Job persistence.
    for (const [name, value] of Object.entries({
      sources: { ...sourcesFixture(), tenantId: source.tenantId },
      jobs: { ...jobsFixture(), tenantId: jobs.tenantId },
    }))
      await page.route(`**/_catalog/api/v1/overview/${name}`, (route) =>
        route.fulfill({ json: value }),
      );
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(sources.locator(".overview-number")).toHaveText("3");
    await expect(queues.locator("dd")).toHaveText(["2", "1"]);
    const statusRead = page.waitForRequest((request) =>
      new URL(request.url()).pathname.endsWith("/api/v1/federation/sources"),
    );
    await sources
      .getByRole("link", { name: "Error", exact: true })
      .first()
      .click();
    expect(new URL((await statusRead).url()).searchParams.get("status")).toBe(
      "error",
    );
    await expect(
      page.getByRole("combobox", { name: "Source status" }),
    ).toHaveValue("error");
    await page.reload();
    await expect(
      page.getByRole("combobox", { name: "Source status" }),
    ).toHaveValue("error");
    await page.goBack();
    await expect(
      page.getByRole("link", { name: "Operations", exact: true }),
    ).toHaveAttribute("aria-current", "page");
    await page
      .getByRole("region", { name: "Job queues", exact: true })
      .locator('a[href*="kind=source.sync&state=failed"]')
      .click();
    await expect(
      page.getByRole("combobox", { name: "Job state", exact: true }),
    ).toHaveValue("failed");
    expect(mutations).toEqual([]);
  });
}

for (const base of ["root", "subpath"]) {
  test(`${base}: review and publication summaries match real reads and open exact queues`, async ({
    page,
    catalog,
  }) => {
    const reviewResponse = page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith("/api/v1/overview/reviews"),
    );
    const publicationResponse = page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith("/api/v1/overview/publication"),
    );
    const mutations: string[] = [];
    let reviewReads = 0;
    page.on("request", (request) => {
      if (new URL(request.url()).pathname.endsWith("/api/v1/overview/reviews"))
        reviewReads++;
      if (
        request.url().includes("/_catalog/api/") &&
        !["GET", "HEAD"].includes(request.method())
      )
        mutations.push(request.url());
    });
    await login(page, catalog, "local-admin", "/overview");
    const review = (await (
      await reviewResponse
    ).json()) as components["schemas"]["ReviewOverview"];
    const publication = (await (
      await publicationResponse
    ).json()) as components["schemas"]["PublicationOverview"];
    const reviews = page.getByRole("region", {
      name: "Reviews",
      exact: true,
    });
    await expect(reviews.locator("dd").nth(0)).toHaveText(
      String(review.pending.value),
    );
    await expect(reviews.locator("dd").nth(1)).toHaveText(
      String(review.approvedUnreleased.value),
    );
    const daily = reviews.getByRole("button", {
      name: "Daily breakdown",
      exact: true,
    });
    await tabTo(page, daily);
    await page.keyboard.press("Space");
    await expect(daily).toHaveAttribute("aria-pressed", "true");
    await expect(reviews.getByRole("img")).toHaveCount(0);
    const rows = reviews
      .getByRole("table", {
        name: "Daily submissions and decision outcomes in UTC",
      })
      .getByRole("row");
    await expect(rows).toHaveCount(31);
    for (let index = 0; index < 30; index++) {
      const day = review.days[index]!;
      await expect(rows.nth(index + 1).getByRole("rowheader")).toHaveText(
        day.date + (day.partial ? " (partial)" : ""),
      );
      await expect(rows.nth(index + 1).getByRole("cell")).toHaveText(
        [day.submitted, day.approved, day.changesRequired, day.rejected].map(
          (value) => (value === undefined ? "Unavailable" : String(value)),
        ),
      );
    }
    const chart = reviews.getByRole("button", { name: "Chart", exact: true });
    await tabTo(page, chart);
    await page.keyboard.press("Enter");
    await expect(chart).toHaveAttribute("aria-pressed", "true");
    await expect(reviews.getByRole("table")).toHaveCount(0);
    expect(reviewReads).toBe(1);
    const hosts = page.getByRole("region", {
      name: "Discovery publication",
      exact: true,
    });
    if (publication.hosts.length)
      await expect(hosts.getByRole("row")).toHaveCount(
        publication.hosts.length + 1,
      );
    else
      await expect(hosts).toContainText("No publication hosts are configured");
    const queueRead = page.waitForRequest((request) =>
      new URL(request.url()).pathname.endsWith(
        "/_catalog/api/v1/review-submissions",
      ),
    );
    await reviews
      .getByRole("link", {
        name: "Approved, unpublished",
        exact: true,
      })
      .click();
    const criteria = new URL((await queueRead).url()).searchParams;
    expect(criteria.get("state")).toBe("approved");
    expect(criteria.get("unreleased")).toBe("true");
    expect(criteria.has("cursor")).toBe(false);
    await expect(
      page.getByRole("checkbox", {
        name: "Approved submissions without a release",
      }),
    ).toBeChecked();
    await page.reload();
    await expect(
      page.getByRole("checkbox", {
        name: "Approved submissions without a release",
      }),
    ).toBeChecked();
    await page
      .getByRole("checkbox", { name: "Approved submissions without a release" })
      .uncheck();
    await page.getByRole("button", { name: "Apply review filters" }).click();
    await expect(page).toHaveURL(/state=approved$/);
    await page.goBack();
    await expect(
      page.getByRole("checkbox", {
        name: "Approved submissions without a release",
      }),
    ).toBeChecked();
    await page.goForward();
    await expect(
      page.getByRole("checkbox", {
        name: "Approved submissions without a release",
      }),
    ).not.toBeChecked();
    expect(mutations).toEqual([]);
  });
}

for (const base of ["root", "subpath"]) {
  test(`${base}: overview shows authorized API counts and applies Resource and Catalog links`, async ({
    page,
    catalog,
  }) => {
    const inventory = await catalog.admin<{
      total: { value: number };
      kinds: { kind: string; count: number }[];
    }>("GET", "/api/v1/overview/resources");
    const releases = await catalog.admin<{
      published: { value: number };
      deprecated: { value: number };
    }>("GET", "/api/v1/overview/releases");
    const reads: string[] = [];
    page.on("request", (request) => {
      if (
        new URL(request.url()).pathname.includes("/_catalog/api/v1/overview/")
      )
        reads.push(request.url());
    });
    await login(page, catalog, "local-admin", "/overview");
    const resources = page.getByRole("region", {
      name: "Local resources",
      exact: true,
    });
    await expect(resources.locator(".overview-number")).toHaveText(
      String(inventory.data.total.value),
    );
    await expect.poll(() => reads.length).toBe(7);
    const rows = resources.locator(".overview-kind dd");
    await expect(rows).toHaveCount(4);
    for (let i = 0; i < 4; i++)
      await expect(rows.nth(i)).toHaveText(
        inventory.data.kinds[i]!.count.toLocaleString("en-US"),
      );
    await expect(resources.getByRole("table")).toHaveCount(0);
    const current = page.getByRole("region", {
      name: "Current releases",
      exact: true,
    });
    await expect(current.locator("dd").nth(0)).toHaveText(
      String(releases.data.published.value),
    );
    await expect(current.locator("dd").nth(1)).toHaveText(
      String(releases.data.deprecated.value),
    );
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect.poll(() => reads.length).toBe(14);
    await expect(
      resources.getByRole("link", { name: "Agent", exact: true }),
    ).toBeVisible();
    await expect(page).toHaveTitle("Overview | Open Agentic Catalog");
    expect(
      await page
        .locator(".overview-grid")
        .evaluate(
          (node) =>
            getComputedStyle(node).gridTemplateColumns.split(" ").length,
        ),
    ).toBe(4);
    const geometry = await page.locator(".overview-grid").evaluate((grid) => {
      const cards = [...grid.querySelectorAll(".overview-stat")];
      const bounds = cards.map((card) => card.getBoundingClientRect());
      const resource = grid.firstElementChild!.getBoundingClientRect();
      return {
        heights: bounds.map((rect) => rect.height),
        tops: bounds.map((rect) => rect.top),
        gap: bounds[0]!.top - resource.bottom,
        expectedGap: parseFloat(getComputedStyle(grid).rowGap),
      };
    });
    expect(geometry.heights).toHaveLength(4);
    expect(
      Math.max(...geometry.heights) - Math.min(...geometry.heights),
    ).toBeLessThan(1);
    expect(
      Math.max(...geometry.tops) - Math.min(...geometry.tops),
    ).toBeLessThan(1);
    expect(Math.abs(geometry.gap - geometry.expectedGap)).toBeLessThan(1);
    const resourceRequest = page.waitForRequest((request) =>
      new URL(request.url()).pathname.endsWith("/_catalog/api/v1/resources"),
    );
    await resources.getByRole("link", { name: "Agent", exact: true }).click();
    expect(
      new URL((await resourceRequest).url()).searchParams.get("kind"),
    ).toBe("agent");
    await expect(
      page.getByRole("combobox", { name: "Resource kind", exact: true }),
    ).toHaveValue("agent");
    await page.reload();
    await expect(
      page.getByRole("combobox", { name: "Resource kind", exact: true }),
    ).toHaveValue("agent");
    await page
      .getByRole("combobox", { name: "Resource kind", exact: true })
      .selectOption("plugin");
    await expect(page).toHaveURL(/\/resources\?kind=plugin$/);
    await page.goBack();
    await expect(
      page.getByRole("combobox", { name: "Resource kind", exact: true }),
    ).toHaveValue("agent");
    await page.goForward();
    await expect(
      page.getByRole("combobox", { name: "Resource kind", exact: true }),
    ).toHaveValue("plugin");
    await page.goto(
      catalog.origin + catalog.basePath + "/overview?view=content",
    );
    await expect(
      resources.getByRole("link", { name: "Agent", exact: true }),
    ).toBeVisible();
    const search = page.waitForRequest((request) =>
      new URL(request.url()).pathname.endsWith(
        "/_catalog/api/v1/catalog/search",
      ),
    );
    await page
      .getByRole("link", { name: "Browse indexed records", exact: true })
      .click();
    expect(object((await search).postDataJSON())).toMatchObject({
      text: "",
      origins: ["indexed"],
    });
    await page.reload();
    await expect(page).toHaveURL(/\/catalog\?origin=indexed$/);
    await page.goto(
      catalog.origin + catalog.basePath + "/catalog?origin=local",
    );
    await expect(
      page.getByRole("heading", { name: "Catalog", exact: true }),
    ).toBeVisible();
    await page.goBack();
    await expect(page).toHaveURL(/\/catalog\?origin=indexed$/);
    await page.goForward();
    await expect(page).toHaveURL(/\/catalog\?origin=local$/);
  });

  test(`${base}: overview has no hidden reads for unknown views and keeps independent failure and limited states`, async ({
    page,
    catalog,
    network,
  }) => {
    const reads: string[] = [];
    page.on("request", (request) => {
      if (request.url().includes("/_catalog/api/v1/overview/"))
        reads.push(request.url());
    });
    await login(page, catalog, "local-admin", "/overview?view=unknown");
    await expect(page.getByRole("alert")).toContainText("unavailable");
    expect(reads).toEqual([]);
    await page.route("**/_catalog/api/v1/overview/resources", async (route) => {
      const reply = await route.fetch();
      const data = object(await reply.json());
      const { kinds: _kinds, ...rest } = data;
      await route.fulfill({
        response: reply,
        json: { ...rest, total: { value: 10000, limited: true } },
      });
    });
    await page.route("**/_catalog/api/v1/overview/indexed", async (route) => {
      const reply = await route.fetch();
      const data = object(await reply.json());
      const { count: _count, ...rest } = data;
      await route.fulfill({
        response: reply,
        json: { ...rest, limited: true },
      });
    });
    network.expectFailure("GET", "/_catalog/api/v1/overview/releases", 503);
    await page.route("**/_catalog/api/v1/overview/releases", async (route) =>
      route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        json: {
          type: "about:blank",
          title: "Summary unavailable",
          status: 503,
          detail: "Current releases cannot be read.",
          code: "DEPENDENCY_UNAVAILABLE",
        },
      }),
    );
    await page
      .getByRole("link", { name: "Open Overview", exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "Local resources", exact: true }),
    ).toContainText("kind breakdown is unavailable");
    await expect(
      page.getByRole("region", {
        name: "External records",
        exact: true,
      }),
    ).toContainText("Count unavailable");
    await expect(
      page
        .getByRole("region", { name: "Current releases", exact: true })
        .getByRole("alert"),
    ).toContainText("The request could not be completed. Please try again.");
    await expect(
      page.getByRole("region", { name: "Namespaces", exact: true }),
    ).toContainText("Manage namespaces");
    await page.unroute("**/_catalog/api/v1/overview/releases");
    const beforeRecovery = reads.length;
    const releases = page.getByRole("region", {
      name: "Current releases",
      exact: true,
    });
    await releases.getByRole("button", { name: "Reload", exact: true }).click();
    await expect(releases.locator("dd")).toHaveCount(2);
    expect(reads).toHaveLength(beforeRecovery + 1);
    await page.goBack();
    await expect(page.getByRole("alert")).toContainText("unavailable");
    expect(reads).toHaveLength(beforeRecovery + 1);
    await page.goForward();
    await expect(
      page.getByRole("region", { name: "Local resources", exact: true }),
    ).toContainText("kind breakdown is unavailable");
    await page.setViewportSize({ width: 320, height: 850 });
    expect(
      await page
        .locator(".overview-grid")
        .evaluate(
          (node) =>
            getComputedStyle(node).gridTemplateColumns.split(" ").length,
        ),
    ).toBe(1);
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: "Dark", exact: true }).check();
    await page.getByLabel("Appearance", { exact: true }).click();
    await doubleText(page);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth + 1,
      ),
    ).toBe(true);
    const accessibility = await new AxeBuilder({ page }).analyze();
    expect(accessibility.violations).toEqual([]);
    expect(accessibility.incomplete).toEqual([]);
    await signOut(page);
    await expect(
      page.getByRole("region", { name: "Local resources", exact: true }),
    ).toHaveCount(0);
  });
}

const maintainedTest = test.extend({ maintainedIdentities: true });
maintainedTest(
  "exact templates only fetch their permitted overview panels",
  async ({ page, catalog }) => {
    const reads: string[] = [];
    page.on("request", (request) => {
      if (request.url().includes("/_catalog/api/v1/overview/"))
        reads.push(new URL(request.url()).pathname.split("/").at(-1)!);
    });
    await login(page, catalog, "local-template-reader", "/overview");
    await expect(
      page.getByRole("region", { name: "Current releases", exact: true }),
    ).toBeVisible();
    await expect.poll(() => reads.length).toBe(4);
    expect([...reads].sort()).toEqual([
      "namespaces",
      "releases",
      "resources",
      "reviews",
    ]);
    await signOut(page);
    reads.length = 0;
    await page
      .locator('[data-login-alias="local-template-service-client"]')
      .click();
    await expect(page.getByRole("alert")).toContainText(
      "does not include Overview",
    );
    expect(reads).toEqual([]);
    await page.goto(
      catalog.origin + catalog.basePath + "/catalog?origin=indexed",
    );
    await expect(
      page.getByRole("heading", { name: "My access", exact: true }),
    ).toBeVisible();
    expect(reads).toEqual([]);
  },
);
