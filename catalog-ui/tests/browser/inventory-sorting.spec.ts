// Header sorting rearranges a bounded page; API continuation and exact actions stay unchanged.
import { test, expect, login } from "./fixtures";
import { tabTo } from "./keyboard";
import { doubleText } from "./text-layout";
import { array, object, text } from "./json";
import AxeBuilder from "@axe-core/playwright";
import { required } from "../required";

const kindNames = new Map([
  ["agent", "Agent"],
  ["mcp_server", "MCP Server"],
  ["skill", "Skill"],
  ["plugin", "Plugin"],
]);

function pageItems(value: unknown, resource: boolean, realmId: string) {
  return array(object(value).items).map((value) => {
    const item = object(value);
    const label = text(item[resource ? "canonicalIdentifier" : "subject"]);
    return {
      id: text(item.id),
      label,
      values: resource
        ? [
            label,
            required(kindNames.get(text(item.kind))),
            Date.parse(text(item.updatedAt)),
          ]
        : [
            label,
            text(item.actorType),
            text(item.state),
            item.realmId === realmId ? "Your realm" : text(item.realmId),
          ],
    };
  });
}

/** Compare the API's values independently of the application's sort helper. */
function ordered(
  items: ReturnType<typeof pageItems>,
  column: number,
  descending = false,
) {
  return [...items]
    .sort((a, b) => {
      const left = a.values[column],
        right = b.values[column];
      const comparison =
        typeof left === "number" && typeof right === "number"
          ? left - right
          : text(left).localeCompare(text(right), undefined, {
              numeric: true,
              sensitivity: "base",
            });
      return descending ? -comparison : comparison;
    })
    .map((item) => item.label);
}

for (const location of ["root", "subpath"] as const) {
  test(`${location}: resource and member header sorting preserves bounded pages and exact links`, async ({
    page,
    catalog,
  }) => {
    for (let index = 1; index <= 27; index++) {
      await catalog.admin("POST", "/api/v1/resources", {
        namespaceId: catalog.receipt.namespaceId,
        kind: ["agent", "mcp_server", "skill", "plugin"][index % 4],
        canonicalIdentifier: `urn:air:example.com:sort:tool-${index}`,
      });
      await catalog.admin("POST", "/api/v1/role-assignments", {
        subject: `sort-member-${index}`,
        actorType: index % 2 ? "human" : "service",
        role: "service_client",
        reason: "Owned table sorting population",
      });
    }
    const identity = await catalog.admin<{ realmId: string }>(
      "GET",
      "/api/v1/me",
    );
    await login(page, catalog, "local-admin", "/resources");
    const reads: string[] = [],
      writes: string[] = [];
    page.on("request", (request) => {
      if (!new URL(request.url()).pathname.includes("/_catalog/api/v1/"))
        return;
      (request.method() === "GET" ? reads : writes).push(request.url());
    });
    for (const [path, label, heading] of [
      ["resources", "Resource inventory", "Identifier"],
      ["members", "Members", "Login identifier (subject)"],
    ] as const) {
      const apiPath = path === "resources" ? "resources" : "memberships";
      const firstReadResponse = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.endsWith(`/api/v1/${apiPath}`) &&
          !new URL(response.url()).searchParams.has("cursor"),
      );
      await page.goto(`${catalog.origin}${catalog.basePath}/${path}`);
      const first = object(await (await firstReadResponse).json());
      const cursor = text(first.nextCursor);
      const items = pageItems(
        first,
        path === "resources",
        identity.data.realmId,
      );
      expect(new Set(items.map((item) => item.values[1])).size).toBeGreaterThan(
        1,
      );
      const region = page.getByRole("region", { name: label, exact: true });
      const links = region.locator("tbody tr td:first-child a");
      await expect(links).toHaveCount(25);
      await expect(region.locator("table")).toHaveAccessibleDescription(
        "Select a column heading to sort the results on this page.",
      );
      const original = items.map((item) => item.label);
      await expect(links).toHaveText(original);
      const before = reads.length;
      const ascending = region.getByRole("button", {
        name: `Sort by ${heading.toLowerCase()}, ascending`,
        exact: true,
      });
      await tabTo(page, ascending);
      await page.keyboard.press("Enter");
      const expected = [...original].sort((a, b) =>
        a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" }),
      );
      await expect(links).toHaveText(expected);
      const header = region.getByRole("columnheader").first();
      await expect(header).toContainText(heading);
      await expect(header).toHaveAttribute("aria-sort", "ascending");
      await page.keyboard.press("Space");
      await expect(links).toHaveText([...expected].reverse());
      await expect(header).toHaveAttribute("aria-sort", "descending");
      const headings =
        path === "resources"
          ? [heading, "Kind", "Updated"]
          : [heading, "Identity type", "State", "Realm"];
      for (let column = 1; column < headings.length; column++) {
        const name = required(headings[column]);
        const selectedHeader = region.getByRole("columnheader").nth(column);
        for (const direction of ["ascending", "descending"] as const) {
          await region
            .getByRole("button", {
              name: `Sort by ${name.toLowerCase()}, ${direction}`,
              exact: true,
            })
            .click();
          await expect(selectedHeader).toHaveAttribute("aria-sort", direction);
          await expect(links).toHaveText(
            ordered(items, column, direction === "descending"),
          );
          await expect(
            region.locator(
              'th[aria-sort="ascending"], th[aria-sort="descending"]',
            ),
          ).toHaveCount(1);
        }
      }
      for (const direction of ["ascending", "descending"] as const)
        await region
          .getByRole("button", {
            name: `Sort by ${heading.toLowerCase()}, ${direction}`,
            exact: true,
          })
          .click();
      await expect(links).toHaveText([...expected].reverse());
      expect(reads).toHaveLength(before);
      const pager = page.getByRole("group", {
        name:
          path === "resources"
            ? "Resource inventory pagination"
            : "Member inventory pagination",
      });
      const nextRead = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.endsWith(`/api/v1/${apiPath}`) &&
          new URL(response.url()).searchParams.get("cursor") === cursor,
      );
      await pager
        .getByRole("button", { name: "Next page", exact: true })
        .click();
      const nextResponse = await nextRead;
      expect(nextResponse.status()).toBe(200);
      const nextItems = pageItems(
        await nextResponse.json(),
        path === "resources",
        identity.data.realmId,
      );
      expect(nextItems.length).toBeGreaterThan(0);
      await expect(header).toHaveAttribute("aria-sort", "descending");
      await expect(links).toHaveText(ordered(nextItems, 0, true));
      expect(
        nextItems.some((item) => items.some((first) => first.id === item.id)),
      ).toBe(false);
      const refreshedNext = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.endsWith(`/api/v1/${apiPath}`) &&
          new URL(response.url()).searchParams.get("cursor") === cursor,
      );
      await page.getByRole("button", { name: "Refresh", exact: true }).click();
      expect((await refreshedNext).status()).toBe(200);
      await expect(links).toHaveText(ordered(nextItems, 0, true));
      const firstRead = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.endsWith(`/api/v1/${apiPath}`) &&
          !new URL(response.url()).searchParams.has("cursor"),
      );
      await pager
        .getByRole("button", { name: "First page", exact: true })
        .click();
      expect((await firstRead).status()).toBe(200);
      await expect(links).toHaveText([...expected].reverse());
      const refreshed = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.endsWith(`/api/v1/${apiPath}`) &&
          !new URL(response.url()).searchParams.has("cursor"),
      );
      await page.getByRole("button", { name: "Refresh", exact: true }).click();
      expect((await refreshed).status()).toBe(200);
      await expect(links).toHaveText([...expected].reverse());
      const target = items.find((item) => item.label === expected[0])!;
      await expect(
        region.getByRole("link", { name: expected[0]!, exact: true }),
      ).toHaveAttribute("href", `${catalog.basePath}/${path}/${target.id}`);
      expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
        violations: [],
        incomplete: [],
      });
      await page.getByLabel("Appearance", { exact: true }).click();
      await page.getByRole("radio", { name: "Dark", exact: true }).check();
      await page.getByRole("radio", { name: "Compact", exact: true }).check();
      await page.keyboard.press("Escape");
      await page.setViewportSize({ width: 320, height: 844 });
      await doubleText(page);
      await expect(region).toHaveAttribute("tabindex", "0");
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(320);
      await region
        .getByRole("button", {
          name: `Sort by ${heading.toLowerCase()}, ascending`,
          exact: true,
        })
        .click();
      await expect(links).toHaveText(expected);
      expect(writes).toEqual([]);
      await page.setViewportSize({ width: 1440, height: 1000 });
    }
  });
}
