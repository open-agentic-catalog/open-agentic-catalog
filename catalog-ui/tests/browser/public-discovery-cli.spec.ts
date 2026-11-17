// Both native clients read one invocation-owned publication; no API reply is mocked.
import { execFile } from "node:child_process";
import { join } from "node:path";
import { promisify } from "node:util";
import { test, expect, type Catalog } from "./fixtures";
import { required } from "../required";
import { array, object, text } from "./json";
import { publishFixture } from "./published-fixture";
import type { components } from "../../src/api/schema";

const execute = promisify(execFile);

async function cli(
  catalog: Catalog,
  action: "manifest" | "list",
  extra: string[] = [],
  refusedStatus?: number,
): Promise<Record<string, unknown>> {
  let output;
  try {
    output = await execute(
      join(required(process.env.CATALOG_UI_TEST_BUILD), "catalogctl"),
      [
        "discovery",
        action,
        "--base-url",
        catalog.apiOrigin,
        "--timeout",
        "5s",
        ...extra,
      ],
      {
        encoding: "utf8",
        timeout: 15000,
        maxBuffer: 1 << 20,
        env: {
          ...process.env,
          CATALOG_REMOTE_TOKEN: "ignored-public-reader-canary",
          CATALOG_REMOTE_TOKEN_FILE: "/unreadable/public-reader-token",
          CATALOG_CONFIG: "/unreadable/public-reader-config",
          CATALOG_DATABASE_DSN_FILE: "/unreadable/public-reader-dsn",
        },
      },
    );
  } catch (error) {
    if (refusedStatus === undefined) throw error;
    const failure = object(error);
    expect(failure.code).toBe(1);
    expect(failure.killed).toBe(false);
    expect(failure.signal).toBeNull();
    expect(failure.stdout).toBe("");
    const diagnostic = object(JSON.parse(text(failure.stderr)));
    expect(diagnostic.status).toBe(refusedStatus);
    expect(diagnostic.code).toBe(
      refusedStatus === 400 ? "INVALID_ARGUMENT" : "NOT_FOUND",
    );
    expect(text(diagnostic.requestId).length).toBeGreaterThan(0);
    return diagnostic;
  }
  expect(refusedStatus).toBeUndefined();
  expect(output.stderr).toBe("");
  const envelope = object(JSON.parse(output.stdout));
  expect(text(object(envelope.meta).requestId).length).toBeGreaterThan(0);
  return object(envelope.data);
}

async function activate(catalog: Catalog) {
  const candidate = await catalog.admin<
    components["schemas"]["PublicationSnapshot"]
  >("POST", "/api/v1/publication/snapshots", { host: "127.0.0.1" });
  await catalog.admin(
    "POST",
    `/api/v1/publication/snapshots/${candidate.data.id}:activate`,
    { host: "127.0.0.1", reason: "Owned browser and CLI qualification" },
    candidate.etag,
  );
}

test.describe("public UI and native CLI together", () => {
  test.use({ publicDiscovery: true });
  for (const base of ["root", "subpath"]) {
    test(`${base}: share exact continuation, refuse old generations and withdraw together`, async ({
      page,
      catalog,
      network,
    }) => {
      for (let index = 0; index < 9; index++)
        await publishFixture(catalog, index);
      await activate(catalog);
      const origin = catalog.origin + catalog.basePath;
      const browserRequests: import("@playwright/test").Request[] = [];
      page.on("request", (request) => browserRequests.push(request));
      await page.goto(origin + "/discover");
      await expect(page.locator("article.public-entry")).not.toHaveCount(0);

      const manifest = await cli(catalog, "manifest");
      expect(array(manifest.entries).length).toBeGreaterThan(10);
      await page
        .getByRole("combobox", { name: "Entries per page", exact: true })
        .selectOption("5");
      const firstResponse = page.waitForResponse((response) =>
        response.url().includes("/_discovery/agents?pageSize=5"),
      );
      await page
        .getByRole("button", { name: "Browse directory", exact: true })
        .click();
      const first = object(await (await firstResponse).json());
      await expect(page.locator("article.public-entry")).toHaveCount(5);
      const nativeFirst = await cli(catalog, "list", ["--page-size", "5"]);
      expect(nativeFirst.items).toEqual(first.items);
      expect(nativeFirst.total).toBe(first.total);

      const secondResponse = page.waitForResponse(
        (response) =>
          response.url().includes("/_discovery/agents?") &&
          new URL(response.url()).searchParams.has("pageToken"),
      );
      await page
        .getByRole("button", { name: "Next page", exact: true })
        .click();
      const second = object(await (await secondResponse).json());
      const nativeSecond = await cli(catalog, "list", [
        "--page-size",
        "5",
        "--page-token",
        text(first.pageToken),
      ]);
      expect(nativeSecond.items).toEqual(second.items);
      expect(nativeSecond.total).toBe(second.total);
      expect(array(second.items)).toHaveLength(5);
      expect(text(second.pageToken).length).toBeGreaterThan(0);

      await activate(catalog);
      await cli(
        catalog,
        "list",
        ["--page-size", "5", "--page-token", text(second.pageToken)],
        400,
      );
      network.expectFailure("GET", "/_discovery/agents", 400);
      await page
        .getByRole("button", { name: "Next page", exact: true })
        .click();
      await expect(page.getByRole("alert")).toContainText(
        "Refresh from the first page",
      );
      await expect(page.locator("article.public-entry")).toHaveCount(0);
      await page
        .getByRole("button", { name: "Refresh publication", exact: true })
        .click();
      await expect(page.locator("article.public-entry")).toHaveCount(5);
      const refreshed = await cli(catalog, "list", ["--page-size", "5"]);
      expect(
        array(refreshed.items).map((entry) => text(object(entry).displayName)),
      ).toEqual(
        await page.locator("article.public-entry h2").allTextContents(),
      );

      await catalog.admin("POST", "/api/v1/publication:unpublish", {
        host: "127.0.0.1",
        reason: "Owned browser and CLI withdrawal",
      });
      await cli(catalog, "manifest", [], 404);
      await cli(catalog, "list", ["--page-size", "5"], 404);
      network.expectFailure("GET", "/_discovery/manifest", 404);
      await page
        .getByRole("button", { name: "Refresh publication", exact: true })
        .click();
      await expect(page.getByRole("alert")).toContainText(
        "Public discovery is unavailable",
      );
      await expect(page.locator("article.public-entry")).toHaveCount(0);
      for (const request of browserRequests) {
        expect(new URL(request.url()).origin).toBe(catalog.origin);
        expect(new URL(request.url()).pathname).not.toMatch(
          /\/auth\/|\/_catalog\//,
        );
        if (request.url().includes("/_discovery/")) {
          expect(request.headers().authorization).toBeUndefined();
          expect(request.headers().cookie).toBeUndefined();
        }
      }
    });
  }
});

test.describe("public UI is disabled by default", () => {
  for (const base of ["root", "subpath"]) {
    test(`${base}: disabling UI does not withdraw API publication or admit private reads`, async ({
      page,
      catalog,
      network,
    }) => {
      await activate(catalog);
      const origin = catalog.origin + catalog.basePath;
      network.expectFailure("GET", "/discover", 404);
      expect((await page.goto(origin + "/discover"))?.status()).toBe(404);
      const headers = { "X-Catalog-Request": "1", Origin: catalog.origin };
      for (const path of ["manifest", "agents"]) {
        const response = await page.request.get(
          origin + "/_discovery/" + path,
          { headers },
        );
        expect(response.status()).toBe(404);
      }
      expect(
        (
          await page.request.post(origin + "/_discovery/search", {
            headers,
            data: { text: "order", pageSize: 5 },
          })
        ).status(),
      ).toBe(404);
      const runtime = object(
        await (await page.request.get(origin + "/runtime.json")).json(),
      );
      expect(runtime.publicDiscoveryEnabled).not.toBe(true);
      expect(
        array((await cli(catalog, "manifest")).entries).length,
      ).toBeGreaterThan(0);
      expect(array((await cli(catalog, "list")).items).length).toBeGreaterThan(
        0,
      );
      expect(
        (
          await page.request.get(
            catalog.apiOrigin + "/api/v1/catalog/resources",
          )
        ).status(),
      ).toBe(401);
    });
  }
});
