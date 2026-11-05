// Run inside the selected Explorer namespace, never against a retained Catalog.
import { readFile, writeFile } from "node:fs/promises";
import { chromium, expect } from "@playwright/test";
import { browse } from "./ard-explorer-journeys.mjs";

const { origin, fixture } = JSON.parse(
  await readFile("/state/origin.json", "utf8"),
);
const browser = await chromium.launch({
  executablePath: "/chromium/chrome-linux64/chrome",
  args: ["--disable-background-networking", "--disable-component-update"],
});
const errors = [];
const refused = [];
let proxyOperations = 0;
let externalControl = false;
const exchanges = [];
const captures = [];
try {
  const context = await browser.newContext({ serviceWorkers: "block" });
  expect(await context.cookies()).toEqual([]);
  await context.route("**/*", async (route) => {
    const request = route.request();
    if (!request.url().startsWith(origin)) {
      if (externalControl && request.url() === "http://198.51.100.1/") {
        externalControl = false;
      } else {
        refused.push(request.url());
      }
      await route.abort();
      return;
    }
    if (request.url() === `${origin}__ard/proxy`) {
      proxyOperations += 1;
      const body = request.postData() ?? "";
      if (proxyOperations > 50 || Buffer.byteLength(body) > 1048576) {
        refused.push("proxy operation or body limit");
        await route.abort();
        return;
      }
    }
    await route.continue();
  });
  const page = await context.newPage();
  page.on("response", (response) => {
    if (response.url() !== `${origin}__ard/proxy`) return;
    captures.push(
      (async () => {
        const request = response.request();
        const spec = request.postDataJSON();
        exchanges.push({
          url: spec.url,
          method: spec.method,
          body: spec.body ?? null,
          authorization: Object.keys(spec.headers ?? {}).some(
            (key) => key.toLowerCase() === "authorization",
          ),
          outerStatus: response.status(),
          native: await response.json(),
        });
      })(),
    );
  });
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (
      message.type() === "error" &&
      message.location().url !== "http://198.51.100.1/"
    )
      errors.push(message.text());
  });
  await page.goto(origin);
  await expect(
    page.getByRole("heading", { name: "ARD Explorer" }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Connect", exact: true }),
  ).toBeEnabled();
  await expect(page.getByRole("combobox")).toHaveValue(
    "http://127.0.0.1:8080/.well-known/ard.json",
  );
  const retained = await page.evaluate(() => Object.keys(localStorage));
  expect(retained).toEqual([]);
  const results = await page.evaluate(async () => {
    const proxy = async (url) => {
      const response = await fetch("/__ard/proxy", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ url, method: "GET", timeoutMs: 1000 }),
      });
      return { status: response.status, document: await response.json() };
    };
    return {
      local: await proxy("http://127.0.0.1:8080/version"),
      external: await proxy("http://198.51.100.1/"),
    };
  });
  expect(results.local.status).toBe(200);
  expect(results.local.document.ok).toBe(true);
  expect(results.local.document.status).toBe(200);
  expect(JSON.parse(results.local.document.body).service).toBe(
    "open-agentic-catalog",
  );
  expect(results.external.status).toBe(200);
  expect(results.external.document.ok).toBe(false);
  externalControl = true;
  expect(
    await page.evaluate(async () => {
      try {
        await fetch("http://198.51.100.1/");
        return false;
      } catch {
        return true;
      }
    }),
  ).toBe(true);
  expect(externalControl).toBe(false);
  expect(refused).toEqual([]);
  expect(errors).toEqual([]);
  await Promise.all(captures);
  await page.screenshot({ path: "/state/startup.png", fullPage: true });
  await writeFile(
    "/state/browser.json",
    JSON.stringify({
      passed: true,
      proxyOperations,
      externalProxyRefused: true,
      externalBrowserRefused: true,
      browserVersion: browser.version(),
      errors,
      refused,
    }) + "\n",
  );
  if (fixture) {
    try {
      const result = await browse(
        page,
        origin,
        fixture,
        exchanges,
        () => proxyOperations,
      );
      await Promise.all(captures);
      expect(errors).toEqual([]);
      expect(refused).toEqual([]);
      await writeFile("/state/browsing.json", JSON.stringify(result) + "\n");
    } catch (error) {
      await Promise.allSettled(captures);
      await writeFile(
        "/state/browsing-failure.json",
        JSON.stringify({ exchanges, errors, refused }) + "\n",
      );
      await page.screenshot({
        path: "/state/browsing-failure.png",
        fullPage: true,
      });
      throw error;
    }
  }
} finally {
  await browser.close();
}
