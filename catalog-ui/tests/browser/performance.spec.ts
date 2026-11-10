// Production-only measurement: real host/API, in-memory authentication state, no routing or tracing.
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import type { Page, BrowserContext, Response } from "@playwright/test";
import { test, expect, login } from "./fixtures";
import { publishFixture } from "./published-fixture";
import { object, array } from "./json";
import { required } from "../required";
import { networkObserver } from "../../scripts/network-observer.mjs";

type Requests = {
  search: number;
  detail: number;
  assets: number;
  other: number;
};
type Sample = {
  journey: string;
  iteration: number;
  durationMs: number;
  requests: Requests;
  searchBytes: number;
  resources: {
    entries: number;
    transferBytes: number;
    encodedBytes: number;
    decodedBytes: number;
    zeroTransferEntries: number;
    assetTransferBytes: number;
    cachedJavaScriptEntries: number;
  };
};

test("production UI performance journeys", async ({
  browser,
  page: setupPage,
  catalog,
}) => {
  const output = required(process.env.CATALOG_UI_PERFORMANCE);
  const padding = "large-retained-card:".padEnd(1 << 20, "x");
  const releases = catalog.receipt.items.flatMap((item) =>
    item.releaseId ? [item.releaseId] : [],
  );
  for (let i = 0; i < 22; i++)
    releases.push(await publishFixture(catalog, i, i === 0 ? padding : ""));
  expect(releases).toHaveLength(25);
  await login(setupPage, catalog);
  await expect(setupPage.locator("article.resource-card")).toHaveCount(24);
  // No storageState file is written. Each context has fresh HTTP caches.
  const state = await setupPage.context().storageState();
  const samples: Sample[] = [];
  const save = () =>
    writeFile(
      join(output, "samples.json"),
      JSON.stringify({ browser: browser.version(), samples }, null, 2),
    );

  for (let iteration = 0; iteration < 6; iteration++) {
    const anonymous = await browser.newContext({
      viewport: { width: 1440, height: 900 },
    });
    try {
      const tab = await anonymous.newPage();
      const measure = observe(anonymous, tab);
      await measure.run("bootstrap", iteration, true, async () => {
        await tab.goto(catalog.origin + "/catalog");
        await expect(
          tab.locator('[data-login-alias="local-reader"]'),
        ).toBeVisible();
      });
      measure.check();
    } finally {
      await anonymous.close();
    }

    const context = await browser.newContext({
      storageState: state,
      viewport: { width: 1440, height: 900 },
    });
    try {
      const tab = await context.newPage();
      const measure = observe(context, tab);
      const cards = tab.locator("article.resource-card");
      await measure.run("catalog-cold", iteration, true, async () => {
        await tab.goto(catalog.origin + "/catalog");
        await expect(cards).toHaveCount(24);
        await expect(tab.getByTitle("Account", { exact: true })).toBeVisible();
      });
      await measure.run("catalog-warm", iteration, true, async () => {
        await tab.locator('a[href="/catalog"]').first().click();
        await expect(cards).toHaveCount(24);
      });
      const firstNames = await cards.locator("h2").allTextContents();
      await measure.run("page", iteration, false, async () => {
        const reply = tab.waitForResponse(
          (r) =>
            r.request().method() === "POST" &&
            r.url().endsWith("/api/v1/catalog/search"),
        );
        await tab.getByRole("button", { name: /Next page/ }).click();
        const response = await reply;
        if (response.status() !== 200) {
          const problem: unknown = await response.json();
          await writeFile(
            join(output, "continuation-failure.json"),
            JSON.stringify(
              {
                status: response.status(),
                problem,
                serverDate: response.headers()["date"],
                observedAt: new Date().toISOString(),
                request: object(response.request().postDataJSON()),
              },
              null,
              2,
            ),
          );
        }
        expect(response.status(), "Search continuation status").toBe(200);
        await expect(cards).toHaveCount(1);
        expect(
          new Set([
            ...firstNames,
            ...(await cards.locator("h2").allTextContents()),
          ]).size,
        ).toBe(25);
      });
      await measure.run("search", iteration, false, async () => {
        await tab
          .getByRole("searchbox", { name: "Search Catalog", exact: true })
          .fill("Paged agent");
        const reply = tab.waitForResponse(
          (r) =>
            r.request().method() === "POST" &&
            r.url().endsWith("/api/v1/catalog/search"),
        );
        await tab.getByRole("button", { name: "Search", exact: true }).click();
        expect(
          object((await reply).request().postDataJSON()).cursor,
        ).toBeUndefined();
        await expect(cards).toHaveCount(22);
      });
      await measure.run("filter", iteration, false, async () => {
        await tab.getByRole("button", { name: "Filters", exact: true }).click();
        await tab.getByRole("checkbox", { name: "paged", exact: true }).check();
        const reply = tab.waitForResponse(
          (r) =>
            r.request().method() === "POST" &&
            r.url().endsWith("/api/v1/catalog/search"),
        );
        await tab
          .getByRole("button", { name: "Apply filters", exact: true })
          .click();
        expect(object((await reply).request().postDataJSON()).tags).toEqual([
          "paged",
        ]);
        await expect(cards).toHaveCount(22);
      });
      await measure.run("detail", iteration, false, async () => {
        const reply = tab.waitForResponse((r) =>
          r.url().endsWith("/api/v1/catalog/resources/" + releases[3]),
        );
        await tab
          .getByRole("button", { name: "Paged agent 00", exact: true })
          .click();
        const response = await reply;
        expect(response.status()).toBe(200);
        const revision = object(object(await response.json()).versionRevision);
        const representation = object(array(revision.representations)[0]);
        expect(
          object(array(object(representation.data).skills)[0]).description ===
            padding,
        ).toBe(true);
        await expect(
          tab.getByRole("dialog").getByText("Apache-2.0", { exact: true }),
        ).toBeVisible();
      });
      measure.check();
    } finally {
      await context.close();
    }
  }

  function observe(context: BrowserContext, tab: Page) {
    const ledger = networkObserver(catalog.origin);
    let errors = 0;
    let active: { requests: Requests; searchBytes: number } | undefined;
    const pending: Promise<void>[] = [];
    tab.on("pageerror", () => {
      errors++;
    });
    tab.on("console", (message) => {
      if (
        message.type() === "error" &&
        !message.text().startsWith("Failed to load resource:")
      )
        errors++;
    });
    context.on("requestfailed", (r) =>
      ledger.failure(r.method(), r.url(), r.failure()?.errorText),
    );
    context.on("request", (request) => {
      const url = new URL(request.url());
      if (url.origin !== catalog.origin) {
        errors++;
        return;
      }
      if (!active) return;
      const key = url.pathname.endsWith("/api/v1/catalog/search")
        ? "search"
        : /\/api\/v1\/catalog\/resources\/[^/]+$/.test(url.pathname)
          ? "detail"
          : url.pathname.startsWith("/assets/")
            ? "assets"
            : "other";
      active.requests[key]++;
    });
    context.on("response", (response: Response) => {
      ledger.observe(
        response.request().method(),
        response.url(),
        response.status(),
      );
      const sample = active;
      if (sample && response.url().endsWith("/api/v1/catalog/search")) {
        pending.push(
          response
            .body()
            .then((body) => {
              sample.searchBytes = Math.max(sample.searchBytes, body.length);
              if (body.includes("large-retained-card:")) errors++;
            })
            .catch(() => {
              errors++;
            }),
        );
      }
    });
    function check() {
      expect(errors, "Unexpected browser error or eager document content").toBe(
        0,
      );
      expect(ledger.results()).toEqual([]);
    }
    return {
      check,
      async run(
        journey: string,
        iteration: number,
        navigation: boolean,
        action: () => Promise<void>,
      ) {
        const sample = {
          requests: { search: 0, detail: 0, assets: 0, other: 0 },
          searchBytes: 0,
        };
        active = sample;
        const before = await tab.evaluate(() => {
          performance.clearResourceTimings();
          return { time: performance.now(), origin: performance.timeOrigin };
        });
        await action();
        const timing = await tab.evaluate(() => ({
          time: performance.now(),
          origin: performance.timeOrigin,
          resources: performance.getEntriesByType("resource").map((entry) => {
            const r = entry as PerformanceResourceTiming;
            return {
              asset: new URL(r.name).pathname.startsWith("/assets/"),
              javaScript: new URL(r.name).pathname.endsWith(".js"),
              transfer: r.transferSize,
              encoded: r.encodedBodySize,
              decoded: r.decodedBodySize,
            };
          }),
        }));
        await Promise.all(pending.splice(0));
        active = undefined;
        check();
        if (navigation) expect(timing.origin).not.toBe(before.origin);
        else expect(timing.origin).toBe(before.origin);
        samples.push({
          journey,
          iteration,
          durationMs: timing.time - (navigation ? 0 : before.time),
          ...sample,
          resources: {
            entries: timing.resources.length,
            transferBytes: timing.resources.reduce((n, r) => n + r.transfer, 0),
            encodedBytes: timing.resources.reduce((n, r) => n + r.encoded, 0),
            decodedBytes: timing.resources.reduce((n, r) => n + r.decoded, 0),
            assetTransferBytes: timing.resources
              .filter((r) => r.asset)
              .reduce((n, r) => n + r.transfer, 0),
            cachedJavaScriptEntries: timing.resources.filter(
              (r) =>
                r.asset && r.javaScript && r.transfer === 0 && r.encoded > 0,
            ).length,
            zeroTransferEntries: timing.resources.filter(
              (r) => r.transfer === 0 && r.encoded > 0,
            ).length,
          },
        });
        await save();
      },
    };
  }
});
