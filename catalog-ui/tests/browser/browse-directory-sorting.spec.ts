// Real service pages retain their cursors and exact targets when the browser sorts loaded rows.
import { test, expect, login } from "./fixtures";
import type { Locator, Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { array, object, text } from "./json";
import { tabTo } from "./keyboard";
import { doubleText } from "./text-layout";
import { publishFixture } from "./published-fixture";

type Row = { id: string; label: string; values: (string | number)[] };
const kinds = new Map([
  ["agent", "Agent"],
  ["mcp_server", "MCP Server"],
  ["skill", "Skill"],
  ["plugin", "Plugin"],
]);
function ordered(rows: Row[], column: number, descending = false) {
  return [...rows]
    .sort((a, b) => {
      const left = a.values[column],
        right = b.values[column];
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
const labels = (region: Locator) => region.locator("tbody tr td:first-child");
async function checkColumns(
  page: Page,
  region: Locator,
  rows: Row[],
  headings: string[],
  reads: string[],
) {
  await expect(labels(region)).toHaveText(rows.map((row) => row.label));
  const before = reads.length;
  for (const [column, name] of headings.entries()) {
    const button = region.getByRole("button", {
      name: `Sort by ${name.toLowerCase()}, ascending`,
      exact: true,
    });
    if (column === 0) {
      await tabTo(page, button);
      await page.keyboard.press("Enter");
    } else await button.click();
    await expect(labels(region)).toHaveText(ordered(rows, column));
    await expect(region.getByRole("columnheader").nth(column)).toHaveAttribute(
      "aria-sort",
      "ascending",
    );
    if (column === 0) await page.keyboard.press("Space");
    else
      await region
        .getByRole("button", {
          name: `Sort by ${name.toLowerCase()}, descending`,
          exact: true,
        })
        .click();
    await expect(labels(region)).toHaveText(ordered(rows, column, true));
    await expect(region.getByRole("columnheader").nth(column)).toHaveAttribute(
      "aria-sort",
      "descending",
    );
    await expect(
      region.locator('th[aria-sort="ascending"],th[aria-sort="descending"]'),
    ).toHaveCount(1);
  }
  expect(reads).toHaveLength(before);
}
function directoryRows(value: unknown, kind: "namespace" | "publisher"): Row[] {
  return array(object(value).items).map((value) => {
    const item = object(value),
      name = text(item.displayName);
    return {
      id: text(item.id),
      label: name,
      values:
        kind === "namespace"
          ? [name, text(item.slug)]
          : [
              name,
              array(item.domains).length,
              array(item.approvedPublicationDomains).length,
            ],
    };
  });
}
function browseRows(value: unknown): Row[] {
  return array(object(value).items).map((value) => {
    const item = object(value),
      release = object(item.release),
      name = text(object(item.listingRevision).displayName);
    return {
      id: text(release.id),
      label: name,
      values: [
        name,
        kinds.get(text(object(item.resource).kind))!,
        text(object(item.version).version),
        text(release.state),
      ],
    };
  });
}

for (const location of ["root", "subpath"] as const) {
  test(`${location}: directory sorting preserves counts, cursors, filters and exact links`, async ({
    page,
    catalog,
  }) => {
    for (const kind of ["namespace", "publisher"] as const) {
      for (let index = 1; index <= 27; index++) {
        const domains = Array.from(
          { length: index % 3 === 0 ? 10 : index % 3 === 1 ? 2 : 1 },
          (_, i) => `sort-${index}-${i}.example`,
        );
        const created = await catalog.admin<{ id: string }>(
          "POST",
          `/api/v1/${kind}s`,
          {
            displayName: `Directory ${index}`,
            ...(kind === "namespace" ? { slug: `sort-${index}` } : { domains }),
          },
        );
        if (kind === "publisher") {
          let etag = created.etag;
          for (const domain of domains.slice(0, index % 3)) {
            const approved = await catalog.admin(
              "POST",
              `/api/v1/publishers/${created.data.id}/publication-domains/${domain}:approve`,
              { reason: "Owned numeric sorting fixture" },
              etag,
            );
            etag = approved.etag;
          }
        }
      }
    }
    await login(page, catalog, "local-admin", "/namespaces");
    const reads: string[] = [],
      writes: string[] = [];
    page.on("request", (request) => {
      if (new URL(request.url()).pathname.includes("/_catalog/api/v1/"))
        (request.method() === "GET" ? reads : writes).push(request.url());
    });
    for (const kind of ["namespace", "publisher"] as const) {
      const path = `/api/v1/${kind}s`,
        label = kind === "namespace" ? "Namespaces" : "Publishers";
      const collection = (url: string) => new URL(url).pathname.endsWith(path);
      const firstResponse = page.waitForResponse(
        (response) =>
          collection(response.url()) &&
          !new URL(response.url()).searchParams.has("cursor"),
      );
      await page.goto(`${catalog.origin}${catalog.basePath}/${kind}s`);
      const first = object(await (await firstResponse).json()),
        rows = directoryRows(first, kind),
        cursor = text(first.nextCursor);
      const region = page.getByRole("region", { name: label, exact: true });
      await expect(labels(region)).toHaveCount(25);
      await expect(region.locator("table")).toHaveAccessibleDescription(
        "Select a column heading to sort the results on this page.",
      );
      await page
        .getByRole("textbox", { name: "Exact display name", exact: true })
        .fill("Unsent directory filter");
      await checkColumns(
        page,
        region,
        rows,
        kind === "namespace"
          ? ["Name", "Slug"]
          : ["Name", "Declared domains", "Approved domains"],
        reads,
      );
      if (kind === "publisher") {
        expect(rows.some((row) => row.values[1] === 10)).toBe(true);
        expect(rows.some((row) => row.values[1] === 2)).toBe(true);
        await expect(
          region.getByText("10 domains", { exact: true }).first(),
        ).toBeVisible();
      }
      await region
        .getByRole("button", { name: "Sort by name, ascending", exact: true })
        .click();
      await region
        .getByRole("button", { name: "Sort by name, descending", exact: true })
        .click();
      const pager = page.getByRole("group", {
        name: `${kind === "namespace" ? "Namespace" : "Publisher"} inventory pagination`,
        exact: true,
      });
      const nextResponse = page.waitForResponse(
        (response) =>
          collection(response.url()) &&
          new URL(response.url()).searchParams.get("cursor") === cursor,
      );
      await pager
        .getByRole("button", { name: "Next page", exact: true })
        .click();
      const nextReply = await nextResponse;
      expect(nextReply.status()).toBe(200);
      const next = directoryRows(await nextReply.json(), kind);
      expect(next.length).toBeGreaterThan(0);
      expect(
        next.some((row) => rows.some((first) => first.id === row.id)),
      ).toBe(false);
      await expect(labels(region)).toHaveText(ordered(next, 0, true));
      await expect(region.getByRole("columnheader").first()).toHaveAttribute(
        "aria-sort",
        "descending",
      );
      const refreshed = page.waitForResponse(
        (response) =>
          collection(response.url()) &&
          new URL(response.url()).searchParams.get("cursor") === cursor,
      );
      await page.getByRole("button", { name: "Refresh", exact: true }).click();
      expect((await refreshed).status()).toBe(200);
      await expect(labels(region)).toHaveText(ordered(next, 0, true));
      await expect(
        page.getByRole("textbox", { name: "Exact display name", exact: true }),
      ).toHaveValue("Unsent directory filter");
      const back = page.waitForResponse(
        (response) =>
          collection(response.url()) &&
          !new URL(response.url()).searchParams.has("cursor"),
      );
      await pager
        .getByRole("button", { name: "First page", exact: true })
        .click();
      expect((await back).status()).toBe(200);
      await expect(labels(region)).toHaveText(ordered(rows, 0, true));
      const target = object(array(first.items)[0]),
        key =
          kind === "namespace"
            ? text(target.slug)
            : text(array(target.domains)[0]);
      await page
        .getByRole("textbox", { name: "Exact display name", exact: true })
        .fill(text(target.displayName));
      await page
        .getByRole("textbox", {
          name: kind === "namespace" ? "Exact slug" : "Exact declared domain",
          exact: true,
        })
        .fill(key);
      const filtered = page.waitForResponse(
        (response) =>
          collection(response.url()) &&
          new URL(response.url()).searchParams.get("displayName") ===
            target.displayName,
      );
      await page
        .getByRole("button", { name: "Apply filters", exact: true })
        .click();
      const filterReply = await filtered;
      expect(filterReply.status()).toBe(200);
      const query = new URL(filterReply.url()).searchParams;
      expect(query.get(kind === "namespace" ? "slug" : "declaredDomain")).toBe(
        key,
      );
      expect(query.has("cursor")).toBe(false);
      await expect(labels(region)).toHaveText([text(target.displayName)]);
      const before = reads.length;
      await region
        .getByRole("button", { name: "Sort by name, ascending", exact: true })
        .click();
      expect(reads).toHaveLength(before);
      await expect(
        region.getByRole("link", {
          name: text(target.displayName),
          exact: true,
        }),
      ).toHaveAttribute(
        "href",
        `${catalog.basePath}/${kind}s/${text(target.id)}`,
      );
      await page
        .getByRole("button", { name: "Clear filters", exact: true })
        .click();
      await expect(labels(region)).toHaveText(ordered(rows, 0));
      expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
        violations: [],
        incomplete: [],
      });
      await page.getByLabel("Appearance", { exact: true }).click();
      await page.getByRole("radio", { name: "Dark", exact: true }).check();
      await page.getByRole("radio", { name: "Compact", exact: true }).check();
      await page.keyboard.press("Escape");
      await page.setViewportSize({ width: 320, height: 900 });
      await doubleText(page);
      await expect(region).toHaveAttribute("tabindex", "0");
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(320);
      const last = region.getByRole("button", {
        name:
          kind === "namespace"
            ? "Sort by slug, ascending"
            : "Sort by approved domains, ascending",
        exact: true,
      });
      await tabTo(page, last);
      await expect(last).toBeInViewport();
      expect(writes).toEqual([]);
      await page.setViewportSize({ width: 1440, height: 1000 });
    }
  });

  test(`${location}: Browse sorting keeps real release pages and applied filters`, async ({
    page,
    catalog,
  }) => {
    for (let index = 1; index <= 23; index++)
      await publishFixture(catalog, index, "", {
        displayName: `Sorted agent ${index}`,
      });
    await login(page, catalog, "local-reader", "/explore");
    const reads: string[] = [],
      writes: string[] = [];
    page.on("request", (request) => {
      if (new URL(request.url()).pathname.includes("/_catalog/api/v1/"))
        (request.method() === "GET" ? reads : writes).push(request.url());
    });
    const collection = (url: string) =>
      new URL(url).pathname.endsWith("/api/v1/catalog/resources");
    const firstResponse = page.waitForResponse(
      (response) =>
        collection(response.url()) &&
        !new URL(response.url()).searchParams.has("cursor"),
    );
    await page.goto(`${catalog.origin}${catalog.basePath}/explore`);
    const first = object(await (await firstResponse).json()),
      rows = browseRows(first),
      cursor = text(first.nextCursor);
    expect(new Set(rows.map((row) => row.values[1])).size).toBeGreaterThan(1);
    const region = page.getByRole("region", {
      name: "Local browse results",
      exact: true,
    });
    await expect(labels(region)).toHaveCount(25);
    await checkColumns(
      page,
      region,
      rows,
      ["Listing", "Kind", "Version", "State"],
      reads,
    );
    const pager = page.getByRole("group", { name: "Pagination", exact: true });
    const nextResponse = page.waitForResponse(
      (response) =>
        collection(response.url()) &&
        new URL(response.url()).searchParams.get("cursor") === cursor,
    );
    await pager.getByRole("button", { name: "Next page", exact: true }).click();
    const nextReply = await nextResponse;
    expect(nextReply.status()).toBe(200);
    const next = browseRows(await nextReply.json());
    expect(next.length).toBeGreaterThan(0);
    expect(next.some((row) => rows.some((first) => first.id === row.id))).toBe(
      false,
    );
    await expect(labels(region)).toHaveText(ordered(next, 3, true));
    const refresh = page.waitForResponse(
      (response) =>
        collection(response.url()) &&
        new URL(response.url()).searchParams.get("cursor") === cursor,
    );
    await page
      .getByRole("button", { name: "Refresh browse results", exact: true })
      .click();
    expect((await refresh).status()).toBe(200);
    await expect(labels(region)).toHaveText(ordered(next, 3, true));
    const filtered = page.waitForResponse(
      (response) =>
        collection(response.url()) &&
        new URL(response.url()).searchParams.get("kind") === "skill",
    );
    await page
      .getByRole("combobox", { name: "Kind", exact: true })
      .selectOption("skill");
    const filteredReply = await filtered;
    expect(filteredReply.status()).toBe(200);
    expect(new URL(filteredReply.url()).searchParams.has("cursor")).toBe(false);
    const skills = browseRows(await filteredReply.json());
    await expect(labels(region)).toHaveText(ordered(skills, 3, true));
    const before = reads.length;
    await region
      .getByRole("button", { name: "Sort by listing, ascending", exact: true })
      .click();
    await expect(labels(region)).toHaveText(ordered(skills, 0));
    expect(reads).toHaveLength(before);
    expect(writes).toEqual([]);
    for (const skill of skills)
      await expect(
        region.getByRole("link", { name: skill.label, exact: true }),
      ).toHaveAttribute("href", `${catalog.basePath}/catalog/${skill.id}`);
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    await page.setViewportSize({ width: 320, height: 900 });
    await doubleText(page);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
    await expect(region).toHaveAttribute("tabindex", "0");
    await tabTo(
      page,
      region.getByRole("button", {
        name: "Sort by state, ascending",
        exact: true,
      }),
    );
  });

  test(`${location}: sorted Publisher domains keep drafts and exact approval commands`, async ({
    page,
    catalog,
  }) => {
    const domains = ["sort-10.example", "sort-2.example", "sort-1.example"];
    const created = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/publishers",
      { displayName: "Sorted domain owner", domains },
    );
    const approved = await catalog.admin(
      "POST",
      `/api/v1/publishers/${created.data.id}/publication-domains/sort-1.example:approve`,
      { reason: "Owned domain sorting setup" },
      created.etag,
    );
    const initial = object(approved.data),
      declared = array(initial.domains).map(text),
      current = array(initial.approvedPublicationDomains).map(text);
    await login(page, catalog, "local-admin", `/publishers/${created.data.id}`);
    const reads: string[] = [],
      writes: string[] = [];
    page.on("request", (request) => {
      if (new URL(request.url()).pathname.includes("/_catalog/api/v1/"))
        (request.method() === "GET" ? reads : writes).push(request.url());
    });
    await page
      .getByRole("textbox", {
        name: "Publication approval reason",
        exact: true,
      })
      .fill("Approve only the selected sorted row");
    await page
      .getByRole("textbox", { name: "Display name", exact: true })
      .fill("Unsent publisher name");
    const region = page.getByRole("region", {
      name: "Publisher domains",
      exact: true,
    });
    const rows = declared.map((domain) => ({
      id: domain,
      label: domain,
      values: [domain, current.includes(domain) ? "Approved" : "Not approved"],
    }));
    await checkColumns(
      page,
      region,
      rows,
      ["Domain", "Publication approval"],
      reads,
    );
    await expect(
      region
        .getByRole("button", { name: "Approve domain", exact: true })
        .first(),
    ).toBeDisabled();
    await expect(
      page.getByRole("textbox", { name: "Display name", exact: true }),
    ).toHaveValue("Unsent publisher name");
    await expect(
      page.getByRole("textbox", {
        name: "Publication approval reason",
        exact: true,
      }),
    ).toHaveValue("Approve only the selected sorted row");
    expect(writes).toEqual([]);
    await page
      .getByRole("textbox", { name: "Display name", exact: true })
      .fill("Sorted domain owner");
    await region
      .getByRole("row")
      .filter({
        has: page.getByRole("cell", { name: "sort-2.example", exact: true }),
      })
      .getByRole("button", { name: "Approve domain", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByText("sort-2.example", { exact: true }),
    ).toBeVisible();
    await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
    await expect(page.getByRole("status")).toContainText(
      "Domain approved for publication",
    );
    expect(writes).toHaveLength(1);
    expect(new URL(writes[0]!).pathname).toBe(
      `${catalog.basePath}/_catalog/api/v1/publishers/${created.data.id}/publication-domains/sort-2.example:approve`,
    );
    const saved = await catalog.admin<{ approvedPublicationDomains: string[] }>(
      "GET",
      `/api/v1/publishers/${created.data.id}`,
    );
    expect(saved.data.approvedPublicationDomains).toContain("sort-2.example");
    expect(saved.data.approvedPublicationDomains).toContain("sort-1.example");
    expect(saved.data.approvedPublicationDomains).not.toContain(
      "sort-10.example",
    );
    const reloaded = page.waitForResponse(
      (response) =>
        response.request().method() === "GET" &&
        new URL(response.url()).pathname.endsWith(
          `/api/v1/publishers/${created.data.id}`,
        ),
    );
    await page
      .getByRole("button", { name: "Reload publisher", exact: true })
      .click();
    const reply = await reloaded;
    expect(reply.status()).toBe(200);
    const refreshed = object(await reply.json());
    const refreshedRows = array(refreshed.domains)
      .map(text)
      .map((domain) => ({
        id: domain,
        label: domain,
        values: [
          domain,
          array(refreshed.approvedPublicationDomains).includes(domain)
            ? "Approved"
            : "Not approved",
        ],
      }));
    await expect(region.getByRole("columnheader").nth(1)).toHaveAttribute(
      "aria-sort",
      "descending",
    );
    await expect(labels(region)).toHaveText(ordered(refreshedRows, 1, true));
    await expect(
      page.getByRole("textbox", {
        name: "Publication approval reason",
        exact: true,
      }),
    ).toHaveValue("");
    expect(writes).toHaveLength(1);
  });
}
