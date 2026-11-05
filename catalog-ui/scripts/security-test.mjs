// Feed selected authenticated browser exchanges through the owned ZAP proxy.
import { chromium, expect } from "@playwright/test";
import { networkObserver } from "./network-observer.mjs";
import { waitForStableNetwork } from "./network-readiness.mjs";

const proxy = process.argv[2];
if (!/^http:\/\/(?:[0-9]{1,3}\.){3}[0-9]{1,3}:8090$/.test(proxy ?? ""))
  throw new Error("An owned container proxy is required");
const origin = "http://localhost:8081";
await waitForStableNetwork();
const browser = await chromium.launch({
  proxy: { server: proxy, bypass: "<-loopback>" },
});
try {
  const context = await browser.newContext({ serviceWorkers: "block" });
  await context.route("**/*", (route) =>
    new URL(route.request().url()).origin === origin
      ? route.continue()
      : route.abort(),
  );
  const ledger = networkObserver(origin, "");
  context.on("response", (reply) =>
    ledger.observe(reply.request().method(), reply.url(), reply.status()),
  );
  context.on("requestfailed", (request) =>
    ledger.failure(
      request.method(),
      request.url(),
      request.failure()?.errorText,
    ),
  );
  const page = await context.newPage();
  page.setDefaultTimeout(10000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto(origin + "/catalog");
  await page.locator('[data-login-alias="local-reader"]').click();
  await expect(page.getByLabel("Account", { exact: true })).toBeVisible();
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  await page
    .getByRole("searchbox", { name: "Search Catalog", exact: true })
    .fill("agent");
  await page
    .getByRole("searchbox", { name: "Search Catalog", exact: true })
    .press("Enter");
  await expect(page.locator("article.resource-card")).toHaveCount(1);
  await page.goto(origin + "/explore");
  await expect(page.locator("tbody tr")).toHaveCount(3);
  await page.getByLabel("Account", { exact: true }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.locator('[data-login-alias="local-reader"]')).toBeVisible();
  expect(errors).toEqual([]);
  expect(ledger.results()).toEqual([]);
  console.log("Owned passive browser controls passed");
} finally {
  await browser.close();
}
