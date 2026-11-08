// Actual Chromium controls run only inside the explicitly selected packaged
// browser gate. Blank calibration contexts contain no application/source counters.
import assert from "node:assert/strict";
import { browserErrors } from "./browser-errors.mjs";
import { networkObserver } from "./network-observer.mjs";

export async function calibrateBrowserErrors(browser) {
  const observer = browserErrors();
  const contexts = [];
  const origin = "https://browser-calibration.invalid";
  try {
    const context = await browser.newContext();
    contexts.push(context);
    context.setDefaultTimeout(10000);
    // Also qualify observing a page that predates observer installation.
    const primary = await context.newPage();
    observer.observe(context, [origin]);
    await primary.evaluate(() => console.info("synthetic-private-info"));
    assert.deepEqual(observer.results(), []);
    const additional = await context.newPage();
    await Promise.all([
      additional.waitForEvent("console", {
        timeout: 10000,
        predicate: (message) => message.type() === "error",
      }),
      additional.evaluate(() => console.error("synthetic-private-console")),
    ]);
    assert.deepEqual(observer.results(), [{ category: "console", count: 1 }]);
    assert.throws(
      () => assert.deepEqual(observer.results(), []),
      assert.AssertionError,
    );
    // Application console.error must still fail even when its text copies a
    // native resource notice; native notices have no JavaScript arguments.
    await Promise.all([
      primary.waitForEvent("console", {
        timeout: 10000,
        predicate: (message) => message.type() === "error",
      }),
      primary.evaluate(() =>
        console.error(
          "Failed to load resource: the server responded with a status of 503 (Service Unavailable)",
        ),
      ),
    ]);
    assert.deepEqual(observer.results(), [{ category: "console", count: 2 }]);
    await Promise.all([
      primary.waitForEvent("pageerror", { timeout: 10000 }),
      primary.evaluate(() => {
        setTimeout(() => {
          throw new Error("synthetic-private-exception");
        }, 0);
      }),
    ]);
    assert.deepEqual(observer.results(), [
      { category: "console", count: 2 },
      { category: "page", count: 1 },
    ]);
    assert.throws(
      () => assert.deepEqual(observer.results(), []),
      assert.AssertionError,
    );
    await context.close();
    const reopened = await browser.newContext();
    contexts.push(reopened);
    reopened.setDefaultTimeout(10000);
    observer.observe(reopened, [origin]);
    const page = await reopened.newPage();
    await Promise.all([
      page.waitForEvent("console", {
        timeout: 10000,
        predicate: (message) => message.type() === "error",
      }),
      page.evaluate(() => console.error("synthetic-private-reopened")),
    ]);
    assert.deepEqual(observer.results(), [
      { category: "console", count: 3 },
      { category: "page", count: 1 },
    ]);
    // Route every byte locally: native failed-resource console notices must
    // leave deliberate and unexpected HTTP outcomes to the existing ledger.
    const url = origin + "/synthetic-private-path";
    const network = networkObserver(origin);
    reopened.on("response", (reply) =>
      network.observe(reply.request().method(), reply.url(), reply.status()),
    );
    await page.route(url, (route) =>
      route.fulfill({
        status: 503,
        headers: {
          "Cache-Control": "no-store",
          "Access-Control-Allow-Origin": "*",
        },
        body: "synthetic-private-body",
      }),
    );
    network.expectFailure("GET", "/synthetic-private-path", 503);
    for (const declared of [true, false]) {
      await Promise.all([
        page.waitForEvent("console", {
          timeout: 10000,
          predicate: (message) => message.type() === "error",
        }),
        page.evaluate(async (target) => {
          await fetch(target, {
            mode: "no-cors",
            signal: AbortSignal.timeout(10000),
          });
        }, url),
      ]);
      assert.deepEqual(
        network.results(),
        declared ? [] : ["Unexpected UI HTTP 503 (GET)"],
      );
      assert.deepEqual(observer.results(), [
        { category: "console", count: 3 },
        { category: "page", count: 1 },
      ]);
    }
    const foreign =
      "https://untracked-calibration.invalid/synthetic-private-path";
    await page.route(foreign, (route) =>
      route.fulfill({
        status: 503,
        headers: { "Access-Control-Allow-Origin": "*" },
        body: "synthetic-private-body",
      }),
    );
    await Promise.all([
      page.waitForEvent("console", {
        timeout: 10000,
        predicate: (message) => message.type() === "error",
      }),
      page.evaluate(async (target) => {
        await fetch(target, {
          mode: "no-cors",
          signal: AbortSignal.timeout(10000),
        });
      }, foreign),
    ]);
    assert.deepEqual(network.results(), ["Unexpected UI HTTP 503 (GET)"]);
    assert.deepEqual(observer.results(), [
      { category: "console", count: 4 },
      { category: "page", count: 1 },
    ]);
    assert.doesNotMatch(
      JSON.stringify(observer.results()),
      /synthetic-private/,
    );
  } finally {
    await Promise.all(contexts.map((context) => context.close()));
  }
}
