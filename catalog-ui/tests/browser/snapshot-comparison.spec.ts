// Real publication prerequisites qualify bounded lookup and explicitly selected retained comparisons.
import type { components } from "../../src/api/schema";
import { test, expect, login, type Catalog } from "./fixtures";
import { required } from "../required";
import { array, object, text } from "./json";
import AxeBuilder from "@axe-core/playwright";
import { doubleText } from "./text-layout";
import { tabTo } from "./keyboard";

type Snapshot = components["schemas"]["PublicationSnapshot"];
async function build(catalog: Catalog) {
  return catalog.admin<Snapshot>("POST", "/api/v1/publication/snapshots", {
    host: "127.0.0.1",
  });
}
async function activate(
  catalog: Catalog,
  snapshot: { data: Snapshot; etag: string | null },
) {
  await catalog.admin(
    "POST",
    `/api/v1/publication/snapshots/${snapshot.data.id}:activate`,
    { host: "127.0.0.1", reason: "Owned snapshot comparison prerequisite" },
    snapshot.etag,
  );
}
async function grantRead(catalog: Catalog) {
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const member = required(
    members.data.items.find((item) => item.subject === "local-no-role"),
  );
  const role = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "Snapshot reader",
      permissions: ["publication.read"],
      reason: "Owned read-only comparison prerequisite",
    },
  );
  await catalog.admin("POST", "/api/v1/access/assignments", {
    membershipId: member.id,
    roleDefinitionId: role.data.id,
    reason: "Owned exact publication read prerequisite",
  });
}
for (const location of ["root", "subpath"]) {
  test(`${location}: active lookup finds an off-page generation and comparison preserves exact retained references`, async ({
    page,
    catalog,
    network,
  }) => {
    const original = await build(catalog);
    await activate(catalog, original);
    const item = required(
      catalog.receipt.items.find((item) => item.key === "order-agent"),
    );
    const listing = await catalog.admin(
      "GET",
      `/api/v1/catalog-listings/${item.listingId}`,
    );
    const revision = await catalog.admin(
      "POST",
      `/api/v1/catalog-listings/${item.listingId}/revisions`,
      {
        versionRevisionId: item.versionRevisionId,
        publisherId: catalog.receipt.publisherId,
        displayName: "Updated snapshot comparison",
        description: "Updated description for the retained comparison",
        visibility: "public",
        representativeQueries: [
          "Find comparison agents",
          "Browse retained metadata",
        ],
        license: "Apache-2.0",
      },
      listing.etag,
    );
    const submission = await catalog.admin<{ id: string }>(
      "POST",
      `/api/v1/catalog-listings/${item.listingId}/submissions`,
      {},
    );
    await catalog.admin(
      "POST",
      `/api/v1/review-submissions/${submission.data.id}:approve`,
      { reason: "Independent snapshot fixture review" },
      submission.etag,
      "local-reviewer",
    );
    await catalog.admin(
      "POST",
      `/api/v1/catalog-listings/${item.listingId}:publish`,
      {
        submissionId: submission.data.id,
        priorDisposition: "superseded",
        priorReason: "Owned replacement for comparison",
      },
      revision.etag,
    );
    const active = await build(catalog);
    await activate(catalog, active);
    const retired = await catalog.admin<Snapshot>(
      "GET",
      `/api/v1/publication/snapshots/${original.data.id}?host=127.0.0.1`,
    );
    expect(retired.data.state).toBe("retired");
    const candidates: Awaited<ReturnType<typeof build>>[] = [];
    for (let index = 0; index < 26; index++)
      candidates.push(await build(catalog));
    const selected = required(candidates.at(-1));
    await grantRead(catalog);
    const reads: URL[] = [],
      writes: string[] = [],
      outside: string[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.origin !== catalog.origin) outside.push(url.origin);
      if (url.pathname.includes("/_catalog/api/v1/")) {
        if (request.method() === "GET") reads.push(url);
        else writes.push(request.method());
      }
    });
    await login(page, catalog, "local-no-role", "/publication?host=127.0.0.1");
    await expect(
      page.getByRole("link", {
        name: `Generation ${selected.data.generation}`,
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", {
        name: `Generation ${active.data.generation}`,
        exact: true,
      }),
    ).toHaveCount(0);
    const listRequests = () =>
      reads.filter((url) => url.pathname.endsWith("/publication/snapshots"));
    const fullRequests = () =>
      reads.filter((url) =>
        /\/publication\/snapshots\/[0-9a-f-]+$/.test(url.pathname),
      );
    expect(listRequests().some((url) => url.searchParams.has("state"))).toBe(
      false,
    );
    await page
      .getByRole("button", { name: "Read active snapshot", exact: true })
      .click();
    const lookup = page.getByRole("region", {
      name: "Active snapshot lookup",
      exact: true,
    });
    await expect(lookup).toContainText(active.data.id);
    const activeQuery = required(listRequests().at(-1));
    expect(Object.fromEntries(activeQuery.searchParams)).toEqual({
      host: "127.0.0.1",
      state: "active",
      limit: "1",
    });
    expect(fullRequests()).toHaveLength(0);
    await page
      .getByRole("combobox", { name: "Snapshot state", exact: true })
      .selectOption("active");
    await expect(
      page.getByRole("link", {
        name: `Generation ${active.data.generation}`,
        exact: true,
      }),
    ).toBeVisible();
    const inventoryPath = "/_catalog/api/v1/publication/snapshots";
    const candidateFirstPage = () =>
      page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.request().method() === "GET" &&
          url.pathname.endsWith(inventoryPath) &&
          url.searchParams.get("state") === "candidate" &&
          !url.searchParams.has("cursor")
        );
      });
    const filteredPage = candidateFirstPage();
    await page
      .getByRole("combobox", { name: "Snapshot state", exact: true })
      .selectOption("candidate");
    const filteredResponse = await filteredPage;
    expect(filteredResponse.status()).toBe(200);
    const filteredBody = object(await filteredResponse.json());
    const firstCursor = text(filteredBody.nextCursor);
    expect(array(filteredBody.items).map((item) => object(item).id)).toEqual(
      candidates
        .slice(1)
        .reverse()
        .map((item) => item.data.id),
    );
    const newest = page.getByRole("link", {
      name: `Generation ${selected.data.generation}`,
      exact: true,
    });
    await expect(newest).toBeVisible();
    const oldestCandidate = required(candidates[0]);
    const oldest = page.getByRole("link", {
      name: `Generation ${oldestCandidate.data.generation}`,
      exact: true,
    });
    await expect(oldest).toHaveCount(0);
    const inventory = page.getByRole("group", {
      name: "Publication snapshots pagination",
      exact: true,
    });
    let failContinuation = true;
    await page.route(`**${inventoryPath}?*`, async (route) => {
      const query = new URL(route.request().url()).searchParams;
      if (
        failContinuation &&
        query.get("state") === "candidate" &&
        query.has("cursor")
      ) {
        failContinuation = false;
        await route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({
            type: "about:blank",
            title: "Unavailable",
            status: 503,
            code: "DEPENDENCY_UNAVAILABLE",
          }),
        });
      } else await route.continue();
    });
    network.expectFailure("GET", inventoryPath, 503);
    const beforePage = listRequests().length;
    await inventory
      .getByRole("button", { name: "Next page", exact: true })
      .click();
    await expect(page.getByRole("alert")).toBeVisible();
    expect(listRequests()).toHaveLength(beforePage + 1);
    const failedQuery = required(listRequests().at(-1)).searchParams;
    expect(failedQuery.get("state")).toBe("candidate");
    expect(failedQuery.get("cursor")).toBe(firstCursor);
    await expect(oldest).toHaveCount(0);
    await expect(newest).toHaveCount(0);
    await expect(lookup).toContainText(active.data.id);
    const recoveryPage = candidateFirstPage();
    await page.getByRole("button", { name: "Reload", exact: true }).click();
    const recoveryResponse = await recoveryPage;
    expect(recoveryResponse.status()).toBe(200);
    const recoveredBody = object(await recoveryResponse.json());
    const recoveredCursor = text(recoveredBody.nextCursor);
    expect(array(recoveredBody.items).map((item) => object(item).id)).toEqual(
      candidates
        .slice(1)
        .reverse()
        .map((item) => item.data.id),
    );
    await expect(newest).toBeVisible();
    expect(
      Object.fromEntries(required(listRequests().at(-1)).searchParams),
    ).toEqual({
      host: "127.0.0.1",
      state: "candidate",
      limit: "25",
    });
    await inventory
      .getByRole("button", { name: "Next page", exact: true })
      .click();
    await expect(oldest).toBeVisible();
    await expect(newest).toHaveCount(0);
    expect(
      Object.fromEntries(required(listRequests().at(-1)).searchParams),
    ).toEqual({
      host: "127.0.0.1",
      state: "candidate",
      limit: "25",
      cursor: recoveredCursor,
    });
    await expect(
      page.getByRole("link", { name: /^Generation \d+$/ }),
    ).toHaveCount(1);
    await expect(
      inventory.getByRole("button", { name: "Next page", exact: true }),
    ).toHaveCount(0);
    await expect(lookup).toContainText(active.data.id);
    expect(listRequests()).toHaveLength(beforePage + 3);
    expect(fullRequests()).toHaveLength(0);
    const opener = page.getByRole("textbox", {
      name: "Exact Snapshot ID",
      exact: true,
    });
    let wrongTenant = true;
    await page.route(
      `**/_catalog/api/v1/publication/snapshots/${selected.data.id}?host=*`,
      async (route) => {
        if (!wrongTenant) return route.continue();
        wrongTenant = false;
        const response = await route.fetch();
        expect(response.status()).toBe(200);
        const body = object(await response.json());
        expect(body.tenantId).toBe(selected.data.tenantId);
        const foreignTenant = "00000000-0000-7000-8000-00000000ffff";
        expect(body.tenantId).not.toBe(foreignTenant);
        await route.fulfill({
          response,
          json: { ...body, tenantId: foreignTenant },
        });
      },
    );
    await opener.fill(selected.data.id.toUpperCase());
    await opener.press("Enter");
    await expect(page).toHaveURL(
      `${catalog.origin}${catalog.basePath}/publication/${selected.data.id}?host=127.0.0.1`,
    );
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(
      page.getByRole("region", {
        name: "Included discovery entries",
        exact: true,
      }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("region", { name: "Snapshot comparison", exact: true }),
    ).toHaveCount(0);
    await page.getByRole("button", { name: "Reload", exact: true }).click();
    await expect(
      page.getByRole("heading", {
        name: `Generation ${selected.data.generation}: candidate`,
        exact: true,
      }),
    ).toBeVisible();
    const comparison = page.getByRole("region", {
      name: "Snapshot comparison",
      exact: true,
    });
    await comparison
      .getByRole("button", { name: "Choose snapshot baseline", exact: true })
      .click();
    await comparison
      .getByRole("button", { name: "Read active snapshot", exact: true })
      .click();
    await expect(
      comparison.getByRole("region", { name: "Active snapshot lookup" }),
    ).toContainText(active.data.id);
    await comparison
      .getByRole("button", {
        name: "Use observed snapshot as baseline",
        exact: true,
      })
      .click();
    await expect(
      comparison.getByRole("textbox", {
        name: "Baseline Snapshot ID",
        exact: true,
      }),
    ).toHaveValue(active.data.id);
    await comparison
      .getByRole("textbox", { name: "Baseline Snapshot ID", exact: true })
      .fill(original.data.id.toUpperCase());
    const beforeCompare = fullRequests().length;
    await comparison
      .getByRole("button", { name: "Compare selected snapshots", exact: true })
      .click();
    const captured = page.getByRole("region", {
      name: "Captured snapshot comparison",
      exact: true,
    });
    await expect(captured).toContainText(
      "Observed baseline state: retired; selected state: candidate",
    );
    expect(fullRequests().length - beforeCompare).toBe(2);
    expect(
      fullRequests()
        .slice(-2)
        .map((url) => url.pathname.split("/").at(-1))
        .sort(),
    ).toEqual([original.data.id, selected.data.id].sort());
    const entries = captured.getByRole("region", {
      name: "ARD resource entries",
      exact: true,
    });
    await entries.locator("summary").first().click();
    await expect(
      entries
        .locator("pre")
        .filter({ hasText: "Updated description for the retained comparison" })
        .first(),
    ).toBeVisible();
    if (location === "root") {
      await page.setViewportSize({ width: 1440, height: 1200 });
      await captured.evaluate((node) =>
        window.scrollBy(0, node.getBoundingClientRect().top - 100),
      );
      const top = await captured.boundingBox();
      const field = await captured
        .getByRole("region", { name: "Changed description", exact: true })
        .boundingBox();
      if (!top || !field)
        throw new Error("The retained comparison is not visible");
      await page.screenshot({
        path: test.info().outputPath("publication-snapshot-comparison.png"),
        clip: {
          x: top.x,
          y: top.y,
          width: top.width,
          height: field.y + field.height - top.y,
        },
      });
      await page.setViewportSize({ width: 1440, height: 900 });
    }
    const refreshTarget = required(candidates[0]);
    await activate(catalog, refreshTarget);
    await comparison
      .getByRole("button", { name: "Refresh active snapshot", exact: true })
      .click();
    await expect(
      comparison.getByRole("region", { name: "Active snapshot lookup" }),
    ).toContainText(refreshTarget.data.id);
    await expect(
      comparison.getByRole("textbox", {
        name: "Baseline Snapshot ID",
        exact: true,
      }),
    ).toHaveValue(original.data.id.toUpperCase());
    await expect(captured).toContainText(original.data.id);
    await expect(captured).not.toContainText(refreshTarget.data.id);
    await expect(captured).toContainText("selected state: candidate");
    const count = fullRequests().length;
    let fail = true;
    const baselinePath = `/_catalog/api/v1/publication/snapshots/${original.data.id}`;
    await page.route(`**${baselinePath}?host=*`, async (route) => {
      if (fail)
        await route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({
            type: "about:blank",
            title: "Unavailable",
            status: 503,
            code: "DEPENDENCY_UNAVAILABLE",
          }),
        });
      else await route.continue();
    });
    network.expectFailure("GET", baselinePath, 503);
    await comparison
      .getByRole("button", { name: "Compare selected snapshots", exact: true })
      .click();
    await expect(captured.getByRole("alert")).toBeVisible();
    await expect(
      captured.getByRole("region", { name: "ARD resource entries" }),
    ).toHaveCount(0);
    fail = false;
    await captured.getByRole("button", { name: "Reload", exact: true }).click();
    await expect(captured).toContainText(
      "Observed baseline state: retired; selected state: candidate",
    );
    expect(fullRequests().length - count).toBe(4);
    const first = await page.request.get(
      `${catalog.origin}${catalog.basePath}/_catalog/api/v1/publication/snapshots?host=127.0.0.1&limit=1`,
      { headers: { "X-Catalog-Request": "1" } },
    );
    expect(first.status()).toBe(200);
    const cursor = text(object(await first.json()).nextCursor);
    const changedState = await page.request.get(
      `${catalog.origin}${catalog.basePath}/_catalog/api/v1/publication/snapshots?host=127.0.0.1&limit=1&state=active&cursor=${encodeURIComponent(cursor)}`,
      { headers: { "X-Catalog-Request": "1" } },
    );
    expect(changedState.status()).toBe(400);
    expect(object(await changedState.json()).code).toBe("INVALID_ARGUMENT");
    if (location === "subpath") {
      await page.getByLabel("Appearance", { exact: true }).click();
      await page.getByRole("radio", { name: "Dark", exact: true }).check();
      await page.keyboard.press("Escape");
    }
    await page.setViewportSize({ width: 320, height: 844 });
    await doubleText(page);
    await tabTo(
      page,
      comparison.getByRole("button", {
        name: "Compare selected snapshots",
        exact: true,
      }),
      "Tab",
    );
    const beforeKeyboard = fullRequests().length;
    await page.keyboard.press("Enter");
    await expect(captured).toContainText(
      "Observed baseline state: retired; selected state: candidate",
    );
    await expect.poll(() => fullRequests().length).toBe(beforeKeyboard + 2);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
    expect(
      reads.filter((url) =>
        /\/(catalog-listings|listing-revisions|version-revisions|catalog\/resources)\//.test(
          url.pathname,
        ),
      ),
    ).toEqual([]);
    expect(writes).toEqual([]);
    expect(outside).toEqual([]);
    // Scan from the reading origin after the keyboard and reflow checks.
    await page.evaluate(() => window.scrollTo(0, 0));
    const accessibility = await new AxeBuilder({ page }).analyze();
    expect(accessibility.violations).toEqual([]);
    expect(accessibility.incomplete).toEqual([]);
  });
}
