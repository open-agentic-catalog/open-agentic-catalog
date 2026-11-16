import { test, expect, login, type Catalog } from "./fixtures";
import { agentDocument, publishFixture } from "./published-fixture";
import type { components } from "../../src/api/schema";
import AxeBuilder from "@axe-core/playwright";
import { doubleText } from "./text-layout";
import { tabTo } from "./keyboard";
import { object } from "./json";

type Snapshot = components["schemas"]["PublicationSnapshot"];
async function publish(catalog: Catalog) {
  const snapshot = await catalog.admin<Snapshot>(
    "POST",
    "/api/v1/publication/snapshots",
    { host: "127.0.0.1" },
  );
  await catalog.admin(
    "POST",
    `/api/v1/publication/snapshots/${snapshot.data.id}:activate`,
    { host: "127.0.0.1", reason: "Owned public browser publication" },
    snapshot.etag,
  );
}

test.describe("public discovery", () => {
  test.use({ publicDiscovery: true, curated: true });
  for (const base of ["root", "subpath"]) {
    test(`${base}: anonymous browsing, explicit Search, pagination and inert metadata`, async ({
      page,
      catalog,
    }, info) => {
      for (let i = 0; i < 6; i++)
        await publishFixture(catalog, i, "", {
          displayName: `Publicdiscoverybatch ${i}`,
          description: "Published browser metadata",
          representativeQueries: [
            "Find public discovery",
            "Browse public discovery metadata",
          ],
        });
      await publishFixture(catalog, 98, "", {
        kind: "skill",
        displayName: "Public inline Skill",
        nativeContent:
          "---\nname: public-inline-skill\ndescription: Reviewed inline Skill metadata for public browsing.\nlicense: Apache-2.0\n---\n\n# Instructions\n\nRead metadata without executing a runtime.\n",
      });
      // An unpublished private listing must never enter public metadata.
      const resource = await catalog.admin<{ id: string }>(
        "POST",
        "/api/v1/resources",
        {
          namespaceId: catalog.receipt.namespaceId,
          kind: "agent",
          canonicalIdentifier:
            "urn:air:demo.catalog.example.com:demo:private-draft",
        },
      );
      const version = await catalog.admin<{ revision: { id: string } }>(
        "POST",
        `/api/v1/resources/${resource.data.id}/versions`,
        {
          version: "1.0.0",
          representations: [
            {
              role: "primary",
              mediaType: "application/a2a-agent-card+json",
              data: agentDocument,
            },
          ],
        },
      );
      const listing = await catalog.admin<{ id: string }>(
        "POST",
        "/api/v1/catalog-listings",
        {
          namespaceId: catalog.receipt.namespaceId,
          resourceId: resource.data.id,
        },
      );
      await catalog.admin(
        "POST",
        `/api/v1/catalog-listings/${listing.data.id}/revisions`,
        {
          versionRevisionId: version.data.revision.id,
          publisherId: catalog.receipt.publisherId,
          displayName: "Private sentinel",
          description: "Unpublished private metadata",
          visibility: "private",
        },
        listing.etag,
      );
      await publish(catalog);
      const requests: {
        path: string;
        credentials: string | undefined;
        cookie: string | undefined;
        body: string | null;
      }[] = [];
      page.on("request", (request) =>
        requests.push({
          path: request.url(),
          credentials: request.headers()["authorization"],
          cookie: request.headers()["cookie"],
          body: request.postData(),
        }),
      );
      await page.goto(`${catalog.origin}${catalog.basePath}/discover`);
      await expect(
        page.getByRole("heading", { name: "Public discovery", exact: true }),
      ).toBeVisible();
      await expect(page.locator("article.public-entry")).not.toHaveCount(0);
      await expect(
        page.getByText("Private sentinel", { exact: true }),
      ).toHaveCount(0);
      await page
        .getByRole("combobox", { name: "Entries per page", exact: true })
        .selectOption("100");
      await page
        .getByRole("button", { name: "Browse directory", exact: true })
        .click();
      await expect(
        page.getByRole("button", { name: "Browse directory", exact: true }),
      ).toBeEnabled();
      const kinds = page.locator("article.public-entry > p:first-of-type");
      expect(await kinds.allTextContents()).toEqual(
        expect.arrayContaining([
          "Agent",
          "MCP Server",
          "Skill",
          "Plugin",
          "Registry metadata",
        ]),
      );
      await expect(page.getByText(/manifest.*entries/i)).toBeVisible();
      const first = page.locator("article.public-entry").first();
      await first
        .getByText("Inspect returned metadata", { exact: true })
        .click();
      await expect(
        first.locator("dt", { hasText: "Identifier" }),
      ).toBeVisible();
      await expect(first.locator("img,iframe,script")).toHaveCount(0);
      await page
        .getByRole("combobox", { name: "Entries per page", exact: true })
        .selectOption("5");
      await page
        .getByRole("button", { name: "Browse directory", exact: true })
        .click();
      await expect(page.locator("article.public-entry")).toHaveCount(5);
      const firstPage = await page
        .locator("article.public-entry h2")
        .allTextContents();
      await page
        .getByRole("button", { name: "Next page", exact: true })
        .click();
      await expect(
        page.getByRole("button", { name: "Browse directory", exact: true }),
      ).toBeEnabled();
      const secondPage = await page
        .locator("article.public-entry h2")
        .allTextContents();
      expect(secondPage.length).toBeGreaterThan(0);
      expect(secondPage.filter((name) => firstPage.includes(name))).toEqual([]);
      const before = requests.length;
      await page.getByRole("searchbox").fill("Publicdiscoverybatch");
      await expect(
        page.getByRole("button", { name: "Search", exact: true }),
      ).toBeDisabled();
      expect(requests.length).toBe(before);
      await page.getByRole("checkbox", { name: /embedding provider/ }).check();
      const searched = page.waitForResponse((response) =>
        response.url().endsWith("/_discovery/search"),
      );
      await page.getByRole("button", { name: "Search", exact: true }).click();
      const response = await searched;
      expect(response.status()).toBe(200);
      expect(response.request().postDataJSON()).toEqual({
        text: "Publicdiscoverybatch",
        pageSize: 5,
      });
      await expect(page.locator("article.public-entry")).toHaveCount(5);
      const searchedEntry = page.locator("article.public-entry").first();
      await searchedEntry
        .getByText("Inspect returned metadata", { exact: true })
        .click();
      await expect(
        searchedEntry.locator("dt", { hasText: "Reported source" }),
      ).toBeVisible();
      await expect(
        page.getByRole("button", { name: "Next page", exact: true }),
      ).toBeVisible();
      await page
        .getByRole("button", { name: "Next page", exact: true })
        .click();
      await expect(page.locator("article.public-entry")).toHaveCount(1);
      await expect(
        page.getByRole("button", { name: "Next page", exact: true }),
      ).toHaveCount(0);
      for (const request of requests) {
        const url = new URL(request.path);
        expect(url.origin).toBe(catalog.origin);
        expect(url.pathname).not.toMatch(/\/auth\/|\/_catalog\/|\/api\/v1\//);
        if (url.pathname.includes("/_discovery/")) {
          expect(request.credentials).toBeUndefined();
          expect(request.cookie).toBeUndefined();
        }
      }
      await page
        .getByRole("button", { name: "Browse directory", exact: true })
        .click();
      await expect(page.locator("article.public-entry")).toHaveCount(5);
      await expect(page.locator(".public-discovery")).toBeVisible();
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
      await tabTo(page, page.getByRole("searchbox"));
      await expect(page.getByRole("searchbox")).toBeFocused();
      const consentAlignment = () =>
        page.getByRole("checkbox").evaluate((input) => {
          const caption = input.parentElement!.querySelector("span")!;
          const control = input.getBoundingClientRect();
          return Math.abs(
            control.y +
              control.height / 2 -
              caption.getBoundingClientRect().y -
              parseFloat(getComputedStyle(caption).lineHeight) / 2,
          );
        });
      expect(await consentAlignment()).toBeLessThan(4);
      await page.screenshot({
        path: info.outputPath("public-discovery-desktop.png"),
        fullPage: true,
      });
      await page.emulateMedia({ colorScheme: "dark" });
      await page.setViewportSize({ width: 320, height: 900 });
      await page
        .locator("article.public-entry")
        .first()
        .getByText("Inspect returned metadata", { exact: true })
        .click();
      await doubleText(page);
      expect(await consentAlignment()).toBeLessThan(4);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      await page.screenshot({
        path: info.outputPath("public-discovery-mobile-large-text.png"),
        fullPage: true,
      });
    });
    test(`${base}: an empty displayed page keeps real continuation without a false no-match claim`, async ({
      page,
      catalog,
    }) => {
      for (let i = 0; i < 6; i++) await publishFixture(catalog, i);
      await publish(catalog);
      await page.goto(`${catalog.origin}${catalog.basePath}/discover`);
      await expect(page.locator("article.public-entry")).not.toHaveCount(0);
      await page
        .getByRole("combobox", { name: "Entries per page", exact: true })
        .selectOption("5");
      let next: string | undefined;
      // ARD permits an empty page with continuation. Change only presentation
      // input; the token and next-page response come from this real publication.
      const emptyPage = async (route: import("@playwright/test").Route) => {
        const response = await route.fetch();
        expect(response.status()).toBe(200);
        const value = object(await response.json());
        expect(value.items).toHaveLength(5);
        expect(typeof value.pageToken).toBe("string");
        next = value.pageToken as string;
        expect(next.length).toBeGreaterThan(0);
        await route.fulfill({ response, json: { ...value, items: [] } });
      };
      await page.route("**/_discovery/agents?*", emptyPage);
      await page
        .getByRole("button", { name: "Browse directory", exact: true })
        .click();
      await expect(
        page.getByText("No entries were returned on this page.", {
          exact: true,
        }),
      ).toBeVisible();
      await expect(
        page.getByText("No entries match this selection.", { exact: true }),
      ).toHaveCount(0);
      await expect(page.getByRole("status")).toContainText(
        "More pages are available.",
      );
      await page.unroute("**/_discovery/agents?*", emptyPage);
      const continued = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          url.pathname.endsWith("/_discovery/agents") &&
          url.searchParams.get("pageToken") === next
        );
      });
      await page
        .getByRole("button", { name: "Next page", exact: true })
        .click();
      expect((await continued).status()).toBe(200);
      await expect(page.locator("article.public-entry")).toHaveCount(5);
    });
    test(`${base}: replacement and withdrawal refuse continuation without automatic or private fallback`, async ({
      page,
      catalog,
      network,
    }) => {
      for (let i = 0; i < 6; i++) await publishFixture(catalog, i);
      await publish(catalog);
      await page.goto(`${catalog.origin}${catalog.basePath}/discover`);
      await expect(page.locator("article.public-entry")).not.toHaveCount(0);
      await page
        .getByRole("combobox", { name: "Entries per page", exact: true })
        .selectOption("5");
      await page
        .getByRole("button", { name: "Browse directory", exact: true })
        .click();
      await expect(page.locator("article.public-entry")).toHaveCount(5);
      const publicReads: string[] = [];
      page.on("request", (request) => {
        if (request.url().includes("/_discovery/"))
          publicReads.push(request.url());
      });
      await publishFixture(catalog, 10, "", {
        displayName: "Replacement entry",
      });
      await publish(catalog);
      network.expectFailure("GET", "/_discovery/agents", 400);
      await page
        .getByRole("button", { name: "Next page", exact: true })
        .click();
      await expect(page.getByRole("alert")).toContainText(
        "Refresh from the first page",
      );
      await expect(page.locator("article.public-entry")).toHaveCount(0);
      expect(publicReads).toHaveLength(1);
      await page
        .getByRole("button", { name: "Refresh publication", exact: true })
        .click();
      await expect(page.locator("article.public-entry")).toHaveCount(5);
      expect(publicReads).toHaveLength(3);
      await catalog.admin("POST", "/api/v1/publication:unpublish", {
        host: "127.0.0.1",
        reason: "Owned public withdrawal",
      });
      network.expectFailure("GET", "/_discovery/agents", 404);
      await page
        .getByRole("button", { name: "Next page", exact: true })
        .click();
      await expect(page.getByRole("alert")).toContainText(
        "Public discovery is unavailable",
      );
      await expect(page.locator("article.public-entry")).toHaveCount(0);
      expect(publicReads).toHaveLength(4);
    });
    test(`${base}: a private session neither bootstraps nor broadens the public view`, async ({
      page,
      catalog,
    }) => {
      await publish(catalog);
      await login(page, catalog, "local-admin");
      const reads: string[] = [];
      page.on("request", (request) =>
        reads.push(new URL(request.url()).pathname),
      );
      await page.goto(`${catalog.origin}${catalog.basePath}/discover`);
      await expect(page.locator("article.public-entry")).not.toHaveCount(0);
      expect(reads.some((path) => /\/auth\/|\/_catalog\//.test(path))).toBe(
        false,
      );
      // Returning to the private workspace still uses its original session.
      await page.getByRole("link", { name: "Sign in", exact: true }).click();
      await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
    });
  }
});
for (const base of ["root", "subpath"]) {
  test(`${base}: public discovery stays off by default and private routes require sign-in`, async ({
    page,
    catalog,
    network,
  }) => {
    network.expectFailure("GET", "/discover", 404);
    const response = await page.goto(
      `${catalog.origin}${catalog.basePath}/discover`,
    );
    expect(response?.status()).toBe(404);
    await page.goto(`${catalog.origin}${catalog.basePath}/catalog`);
    await expect(
      page.locator('[data-login-alias="local-reader"]'),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Browse public discovery", exact: true }),
    ).toHaveCount(0);
  });
}
