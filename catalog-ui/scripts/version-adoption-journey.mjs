// Explicit new-Version adoption against the owned two-Catalog PostgreSQL fixture.
import { chromium, expect } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { randomBytes } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import {
  localPorts,
  startProcess,
  stopProcess,
  ready,
} from "./browser-fixture.mjs";
import { networkObserver } from "./network-observer.mjs";
const [directory] = process.argv.slice(2);
const input = JSON.parse(
  await readFile(join(directory, "version-adoption.json"), "utf8"),
);
if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(input.api))
  throw new Error("Owned Catalog origin required");
const [port] = await localPorts();
const origin = `http://127.0.0.1:${port}`;
async function json(name, value) {
  const path = join(directory, name);
  await writeFile(path, JSON.stringify(value), { mode: 0o600 });
  return path;
}
const keysFile = await json("version-adoption-keys.json", [
  {
    hash: randomBytes(64).toString("hex"),
    block: randomBytes(32).toString("hex"),
  },
]);
const developmentUsersFile = await json("version-adoption-users.json", [
  { alias: "local-admin", label: "local-admin", token: "local-admin" },
]);
const config = await json("version-adoption-ui.json", {
  listen: `127.0.0.1:${port}`,
  environment: "development",
  origin,
  upstream: input.api,
  assets: join(directory, "assets"),
  cookieName: `adopt-version-${port}`,
  keysFile,
  sessionSeconds: 3600,
  requestBytes: 2097152,
  responseBytes: 16777216,
  timeoutSeconds: 30,
  authMode: "development",
  developmentUsersFile,
});
const host = startProcess(join(directory, "catalog-ui"), ["--config", config]);
let browser;
try {
  await ready(origin, host);
  browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 1280, height: 900 },
  });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on("console", (message) => {
    if (message.type() === "error") errors.push("browser console error");
  });
  const network = networkObserver(origin);
  page.on("pageerror", () => errors.push("browser exception"));
  page.on("response", (reply) =>
    network.observe(reply.request().method(), reply.url(), reply.status()),
  );
  page.on("requestfailed", (request) =>
    network.failure(
      request.method(),
      request.url(),
      request.failure()?.errorText,
    ),
  );
  await page.goto(origin + "/upstream-changes");
  await page.locator('[data-login-alias="local-admin"]').click();
  const inventory = page.locator("section.workspace-panel").filter({
    has: page.locator('a[href*="/observations/' + input.observationId + '?"]'),
  });
  await expect(inventory).toContainText("Additional upstream version");
  await inventory
    .getByRole("link", { name: "Inspect selected occurrence" })
    .click();
  const panel = page
    .locator("section.workspace-panel")
    .filter({ hasText: input.body.snapshotId });
  const capturePanel = async (filename) => {
    // Crop the document with the sticky header at its normal top position.
    const clip = await panel.evaluate((element) => {
      globalThis.scrollTo({ top: 0, behavior: "instant" });
      const { x, y, width, height } = element.getBoundingClientRect();
      return { x, y, width, height };
    });
    await page.screenshot({
      path: join(input.reportDirectory, filename),
      fullPage: true,
      clip,
    });
  };
  await expect(panel).toContainText(input.body.entryLocation);
  await expect(panel).toContainText(input.body.retrievalDigest);
  const selectionURL = page.url();
  await expect(
    panel.getByRole("button", { name: "Find existing local Resource" }),
  ).toHaveCount(0);
  await expect(
    panel.getByRole("link", { name: input.resourceId, exact: true }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(
    panel.getByRole("button", { name: "Confirm new Version adoption" }),
  ).toBeEnabled();
  expect(
    await page.evaluate(
      () =>
        globalThis.document.documentElement.scrollWidth <=
        globalThis.innerWidth,
    ),
  ).toBe(true);
  await capturePanel("adopt-version-narrow.png");
  await page.setViewportSize({ width: 1280, height: 900 });
  await panel
    .getByRole("button", { name: "Confirm new Version adoption" })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText(input.resourceId);
  await expect(dialog).toContainText(input.body.priorVersionRevisionId);
  const response = page.waitForResponse(
    (reply) =>
      reply.request().method() === "POST" &&
      new URL(reply.url()).pathname.endsWith(
        `/resources/${input.resourceId}/versions:adopt`,
      ),
  );
  await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
  const reply = await response;
  expect(reply.status()).toBe(201);
  expect(reply.request().postDataJSON()).toEqual(input.body);
  const result = await reply.json();
  expect(result.version.version).toBe("2.0.0");
  expect(result.versionRevision.adoption.observationId).toBe(
    input.observationId,
  );
  expect(
    result.versionRevision.representations.every(
      (row) => row.validationState === "valid",
    ),
  ).toBe(true);
  const inspect = panel.getByRole("link", { name: "Inspect adopted revision" });
  await expect(inspect).toBeVisible();
  expect(
    (await new AxeBuilder({ page }).include("main").analyze()).violations,
  ).toEqual([]);
  await capturePanel("adopt-version-desktop.png");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  expect(
    await page.evaluate(
      () =>
        globalThis.document.documentElement.scrollWidth <=
        globalThis.innerWidth,
    ),
  ).toBe(true);
  expect(
    (await new AxeBuilder({ page }).include("main").analyze()).violations,
  ).toEqual([]);
  await capturePanel("adopt-version-narrow-dark.png");
  await inspect.click();
  await expect(page).toHaveURL(
    new RegExp(`/version-revisions/${result.versionRevision.id}$`),
  );
  // Reopening a saved link after the append must recheck current eligibility.
  await page.goto(selectionURL);
  await expect(page.getByRole("alert")).toContainText(
    "This selection is no longer available",
  );
  await expect(
    page.getByRole("button", { name: "Confirm new Version adoption" }),
  ).toHaveCount(0);
  await page.getByRole("link", { name: "Return to upstream changes" }).click();
  // The other kinds still have pending versions, so wait for actual results.
  await expect(
    page.getByRole("link", { name: "Inspect selected occurrence" }).first(),
  ).toBeVisible();
  await expect(inventory).toHaveCount(0);
  expect(network.results()).toEqual([]);
  expect(errors).toEqual([]);
} catch (error) {
  const line = Number(
    /version-adoption-journey\.mjs:(\d+):\d+/.exec(error.stack ?? "")?.[1],
  );
  process.stdout.write(
    JSON.stringify({
      stage: "version-adoption",
      ...(Number.isInteger(line) && line > 0 && line < 10000 ? { line } : {}),
    }),
  );
  process.exitCode = 1;
} finally {
  await browser?.close();
  await stopProcess(host);
}
if (!process.exitCode)
  process.stdout.write(JSON.stringify({ result: "passed", cleanup: "passed" }));
