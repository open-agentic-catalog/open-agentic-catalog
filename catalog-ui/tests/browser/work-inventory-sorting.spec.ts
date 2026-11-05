// Real service inventories; controlled Job arithmetic stays in jobs.spec.ts and isolated units.
import { test, expect, login } from "./fixtures";
import type { Locator, Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { array, object, text } from "./json";
import { publishFixture } from "./published-fixture";
import { doubleText } from "./text-layout";
import { tabTo } from "./keyboard";

type Row = { label: string; values: (string | number)[] };
function order(rows: Row[], column: number, descending: boolean) {
  return [...rows]
    .sort((a, b) => {
      const left = a.values[column]!,
        right = b.values[column]!;
      const comparison =
        typeof left === "number" && typeof right === "number"
          ? left - right
          : String(left).localeCompare(String(right), undefined, {
              numeric: true,
              sensitivity: "base",
            });
      return descending ? -comparison : comparison;
    })
    .map((row) => row.label);
}
async function checkColumns(
  region: Locator,
  rows: Row[],
  columns: string[],
  reads: string[],
) {
  const before = reads.length;
  await expect(region.locator("tbody tr td:first-child")).toHaveText(
    rows.map((row) => row.label),
  );
  for (const [index, column] of columns.entries()) {
    for (const descending of [false, true]) {
      const direction = descending ? "descending" : "ascending";
      const button = region.getByRole("button", {
        name: `Sort by ${column.toLowerCase()}, ${direction}`,
        exact: true,
      });
      await button.focus();
      await button.press(descending ? "Space" : "Enter");
      await expect(region.locator("tbody tr td:first-child")).toHaveText(
        order(rows, index, descending),
      );
      await expect(
        region.locator(`th[aria-sort="${direction}"]`),
      ).toHaveAttribute("aria-sort", direction);
      await expect(
        region.locator(`th[aria-sort="${direction}"]`),
      ).toContainText(column);
    }
  }
  expect(reads).toHaveLength(before);
}
async function narrow(page: Page, region: Locator) {
  await page.setViewportSize({ width: 320, height: 900 });
  await doubleText(page);
  await expect(region).toHaveAttribute("tabindex", "0");
  await region.focus();
  await tabTo(page, region.getByRole("button").last());
  await expect(region.getByRole("button").last()).toBeInViewport();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
  // Scan from the reading origin after the keyboard and reflow checks.
  await page.evaluate(() => window.scrollTo(0, 0));
  expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
    violations: [],
    incomplete: [],
  });
}

for (const location of ["root", "subpath"]) {
  test(`${location}: controlled Job values sort numerically and preserve applied paging and manual refresh`, async ({
    page,
    catalog,
  }) => {
    const base = {
      tenantId: "00000000-0000-7000-8000-000000000001",
      priority: 0,
      attemptLimit: 12,
      cancellationRequested: false,
      notBefore: "2026-10-01T00:00:00Z",
      createdAt: "2026-10-01T00:00:00Z",
    };
    const items = [
      {
        ...base,
        id: "00000000-0000-7000-8000-000000000102",
        kind: "semantic.reindex",
        state: "failed",
        progress: 20,
        attempt: 2,
        updatedAt: "2026-10-01T01:00:00Z",
      },
      {
        ...base,
        id: "00000000-0000-7000-8000-000000000110",
        kind: "source.sync",
        state: "queued",
        progress: 100,
        attempt: 10,
        updatedAt: "2026-10-01T00:00:00Z",
      },
      {
        ...base,
        id: "00000000-0000-7000-8000-000000000101",
        kind: "publication.generate",
        state: "running",
        progress: 3,
        attempt: 1,
        updatedAt: "2026-09-30T23:00:00Z",
      },
    ];
    const reads: string[] = [],
      writes: string[] = [];
    await page.route("**/_catalog/api/v1/jobs?**", async (route) => {
      reads.push(route.request().url());
      const later = new URL(route.request().url()).searchParams.has("cursor");
      await route.fulfill({
        json: {
          items: later ? [items[2]] : items,
          ...(later ? {} : { nextCursor: "owned-job-page" }),
        },
      });
    });
    await login(page, catalog, "local-admin", "/jobs");
    page.on("request", (request) => {
      if (
        request.method() !== "GET" &&
        new URL(request.url()).pathname.includes("/_catalog/api/v1/")
      )
        writes.push(request.url());
    });
    const region = page.getByRole("region", {
      name: "Job inventory",
      exact: true,
    });
    await expect(region).toBeVisible();
    await expect(
      page.getByText(/Newest first\. Refresh is manual/),
    ).toBeVisible();
    await page
      .getByRole("combobox", { name: "Job state", exact: true })
      .selectOption("cancelled");
    const names: Record<string, string> = {
      "semantic.reindex": "Search indexing",
      "source.sync": "Source synchronization",
      "publication.generate": "Discovery snapshot generation",
    };
    await checkColumns(
      region,
      items.map((row) => ({
        label: names[row.kind]! + row.id,
        values: [
          names[row.kind]!,
          row.state,
          row.progress,
          row.attempt,
          Date.parse(row.updatedAt),
        ],
      })),
      ["Job type / ID", "State", "Progress", "Attempts", "Updated"],
      reads,
    );
    await expect(
      page.getByText(/Sorted on this page\. Refresh is manual/),
    ).toBeVisible();
    await expect(region.getByText("10 / 12", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(region.locator("tbody tr")).toHaveCount(1);
    await expect(region.getByRole("columnheader").last()).toHaveAttribute(
      "aria-sort",
      "descending",
    );
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect.poll(() => reads.length).toBe(3);
    expect(Object.fromEntries(new URL(reads.at(-1)!).searchParams)).toEqual({
      limit: "25",
      cursor: "owned-job-page",
    });
    await expect(
      page.getByRole("combobox", { name: "Job state", exact: true }),
    ).toHaveValue("cancelled");
    await expect(region.getByRole("link")).toHaveAttribute(
      "href",
      `${catalog.basePath}/jobs/${items[2]!.id}`,
    );
    expect(writes).toEqual([]);
    await narrow(page, region);
  });

  test(`${location}: review and personal tables sort actual records without applying drafts or changing links`, async ({
    page,
    catalog,
  }) => {
    for (const index of [10, 2])
      await publishFixture(catalog, 700 + index, "", {
        displayName: `Tool ${index}`,
      });
    const reviews = await catalog.admin<{ items: unknown[] }>(
      "GET",
      "/api/v1/review-submissions?limit=25",
    );
    await login(page, catalog, "local-admin", "/reviews?state=");
    const reads: string[] = [],
      writes: string[] = [];
    page.on("request", (request) => {
      if (new URL(request.url()).pathname.includes("/_catalog/api/v1/"))
        (request.method() === "GET" ? reads : writes).push(request.url());
    });
    const reviewRegion = page.getByRole("region", {
      name: "Review submissions",
      exact: true,
    });
    await expect(reviewRegion).toBeVisible();
    await page
      .getByLabel("Exact Listing ID", { exact: true })
      .fill(catalog.receipt.items[0]!.listingId);
    await checkColumns(
      reviewRegion,
      reviews.data.items.map((value) => {
        const row = object(value);
        return {
          label: text(row.id),
          values: [
            row.submittedDisplayName === undefined
              ? "Name unavailable"
              : text(row.submittedDisplayName),
            row.canonicalIdentifier === undefined
              ? "Identifier unavailable"
              : text(row.canonicalIdentifier),
            text(row.state).replaceAll("_", " "),
            text(row.submittedBy),
            Date.parse(text(row.submittedAt)),
          ],
        };
      }),
      [
        "Submitted listing",
        "Resource identifier",
        "State",
        "Submitted by",
        "Submitted",
      ],
      reads,
    );
    await expect(
      page.getByLabel("Exact Listing ID", { exact: true }),
    ).toHaveValue(catalog.receipt.items[0]!.listingId);
    for (const value of reviews.data.items) {
      const id = text(object(value).id);
      await expect(
        reviewRegion.getByRole("link", { name: id, exact: true }),
      ).toHaveAttribute("href", `${catalog.basePath}/reviews/${id}`);
    }
    const draftIds: string[] = [];
    for (const index of [10, 2, 1]) {
      const resource = await catalog.admin<{ id: string }>(
        "POST",
        "/api/v1/resources",
        {
          namespaceId: catalog.receipt.namespaceId,
          kind: "agent",
          canonicalIdentifier: `urn:air:demo.catalog.example.com:demo:work-${index}`,
        },
      );
      draftIds.push(resource.data.id);
    }
    const work = await catalog.admin<{ items: unknown[] }>(
      "GET",
      "/api/v1/me/work?queue=drafts&limit=25",
    );
    await page.goto(catalog.origin + catalog.basePath + "/my-work");
    const workRegion = page.getByRole("region", {
      name: "My work results",
      exact: true,
    });
    await expect(workRegion).toBeVisible();
    const workNames: Record<string, string> = {
      resource: "Resource without versions",
      version: "Version candidate",
      candidate: "Listing candidate",
      submission: "Review submission",
    };
    const states: Record<string, string> = {
      empty_resource: "No versions yet",
      unsealed_version: "Unsealed version",
      unsubmitted_candidate: "Not submitted",
      submitted: "Awaiting review",
      changes_required: "Changes required",
      approved: "Approved",
    };
    await checkColumns(
      workRegion,
      work.data.items.map((value) => {
        const row = object(value),
          label =
            row.displayName === undefined
              ? workNames[text(row.type)]!
              : text(row.displayName);
        return {
          label,
          values: [
            label,
            row.canonicalIdentifier === undefined
              ? "Identifier unavailable"
              : text(row.canonicalIdentifier),
            states[text(row.state)]!,
          ],
        };
      }),
      ["Work", "Resource", "State"],
      reads,
    );
    for (const id of draftIds)
      await expect(
        workRegion.locator(`a[href="${catalog.basePath}/resources/${id}"]`),
      ).toHaveText("Open resource");
    const item = catalog.receipt.items.find((item) => item.releaseId)!;
    for (const index of [10, 2, 1])
      await catalog.admin(
        "POST",
        "/api/v1/personal/items",
        index === 2
          ? {
              type: "resource",
              label: `Tool ${index}`,
              reference: {
                origin: "local",
                releaseId: item.releaseId,
                versionRevisionId: item.versionRevisionId,
              },
            }
          : {
              type: "search",
              label: `Tool ${index}`,
              search: { text: `tool ${index}` },
            },
      );
    const saved = await catalog.admin<{ items: unknown[] }>(
      "GET",
      "/api/v1/personal/items?limit=25",
    );
    await page.goto(catalog.origin + catalog.basePath + "/saved-items");
    const savedRegion = page.getByRole("region", {
      name: "Saved library results",
      exact: true,
    });
    await expect(savedRegion).toBeVisible();
    await page
      .getByLabel("Exact saved label", { exact: true })
      .fill("Pending filter");
    await checkColumns(
      savedRegion,
      saved.data.items.map((value) => {
        const row = object(value);
        return {
          label: text(row.label),
          values: [
            text(row.label),
            row.type === "search" ? "Named search" : "Resource reference",
            Date.parse(text(row.updatedAt)),
          ],
        };
      }),
      ["Label", "Type", "Last updated"],
      reads,
    );
    await expect(
      page.getByLabel("Exact saved label", { exact: true }),
    ).toHaveValue("Pending filter");
    for (const value of saved.data.items) {
      const row = object(value);
      await expect(
        savedRegion.getByRole("link", { name: text(row.label), exact: true }),
      ).toHaveAttribute(
        "href",
        `${catalog.basePath}/saved-items/${text(row.id)}`,
      );
    }
    expect(writes).toEqual([]);
    await narrow(page, savedRegion);
  });

  test(`${location}: publication headers sort complete real documents before independent local pagination`, async ({
    page,
    catalog,
  }) => {
    for (let index = 0; index < 23; index++)
      await publishFixture(catalog, 800 + index);
    const snapshot = await catalog.admin(
      "POST",
      "/api/v1/publication/snapshots",
      { host: "127.0.0.1" },
    );
    const data = object(snapshot.data);
    await login(
      page,
      catalog,
      "local-admin",
      `/publication/${text(data.id)}?host=127.0.0.1`,
    );
    await expect(
      page.getByRole("heading", {
        name: "Included discovery entries",
        exact: true,
      }),
    ).toBeVisible();
    const reads: string[] = [],
      writes: string[] = [];
    page.on("request", (request) => {
      if (new URL(request.url()).pathname.includes("/_catalog/api/v1/"))
        (request.method() === "GET" ? reads : writes).push(request.url());
    });
    const initialReads = reads.length;
    for (const [name, document] of [
      ["ARD", data.ard],
      ["AI Catalog", data.aiCatalog],
    ] as const) {
      const entries = array(object(document).entries).map(object);
      expect(entries.length).toBeGreaterThan(25);
      await page
        .getByText(`Inspect ${name} entries (${entries.length})`, {
          exact: true,
        })
        .click();
      const region = page.getByRole("region", {
        name: `${name} included entries`,
        exact: true,
      });
      const pager = page.getByRole("group", {
        name: `${name} included entries pagination`,
        exact: true,
      });
      const labels = region.locator("tbody tr td:first-child");
      await expect(labels).toHaveText(
        entries.slice(0, 25).map((entry) => text(entry.identifier)),
      );
      for (const [column, field] of [
        ["Identifier", "identifier"],
        ["Version", "version"],
        ["Media type", "type"],
      ] as const) {
        for (const descending of [false, true]) {
          await pager
            .getByRole("button", { name: "Next page", exact: true })
            .click();
          await expect(labels).toHaveCount(entries.length - 25);
          const expected = [...entries].sort((a, b) => {
            const left = a[field!],
              right = b[field!];
            const comparison =
              left === undefined
                ? right === undefined
                  ? 0
                  : 1
                : right === undefined
                  ? -1
                  : text(left).localeCompare(text(right), undefined, {
                      numeric: true,
                      sensitivity: "base",
                    });
            return descending ? -comparison : comparison;
          });
          const direction = descending ? "descending" : "ascending";
          const button = region.getByRole("button", {
            name: `Sort by ${column!.toLowerCase()}, ${direction}`,
            exact: true,
          });
          await button.focus();
          await button.press(descending ? "Space" : "Enter");
          await expect(labels).toHaveText(
            expected.slice(0, 25).map((entry) => text(entry.identifier)),
          );
          await expect(
            pager.getByRole("button", { name: "First page", exact: true }),
          ).toHaveCount(0);
          await expect(
            region.locator(`th[aria-sort="${direction}"]`),
          ).toHaveAttribute("aria-sort", direction);
          await expect(
            region.locator(`th[aria-sort="${direction}"]`),
          ).toContainText(column);
        }
      }
      if (name === "ARD") {
        await pager
          .getByRole("button", { name: "Next page", exact: true })
          .click();
        await expect(labels).toHaveCount(entries.length - 25);
      } else {
        const ardEntries = array(object(data.ard).entries);
        const ard = page.getByRole("region", {
          name: "ARD included entries",
          exact: true,
        });
        await expect(ard.locator("tbody tr")).toHaveCount(
          ardEntries.length - 25,
        );
        const previous = await ard
          .locator("tbody tr td:first-child")
          .allTextContents();
        const summary = page.getByText(
          `Inspect ARD entries (${ardEntries.length})`,
          { exact: true },
        );
        await summary.click();
        await expect(ard).toHaveCount(0);
        await expect(labels).toHaveCount(25);
        await summary.click();
        await expect(ard.locator("tbody tr td:first-child")).toHaveText(
          previous,
        );
        await expect(ard.locator('th[aria-sort="descending"]')).toContainText(
          "Media type",
        );
        await expect(labels).toHaveCount(25);
      }
    }
    expect(reads).toHaveLength(initialReads);
    expect(writes).toEqual([]);
    await narrow(
      page,
      page.getByRole("region", {
        name: "AI Catalog included entries",
        exact: true,
      }),
    );
  });
}
