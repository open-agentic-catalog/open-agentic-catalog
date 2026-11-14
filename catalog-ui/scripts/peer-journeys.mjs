// Real two-Catalog pagination with the normal UI page size and a peer outage.
import { chromium, expect } from "@playwright/test";
import { randomBytes } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { request } from "node:https";
import { networkObserver } from "./network-observer.mjs";
import AxeBuilder from "@axe-core/playwright";

const [directory, apiOrigin, fixtureOrigin, caFile] = process.argv.slice(2);
if (
  !directory ||
  !/^http:\/\/127\.0\.0\.1:\d+$/.test(apiOrigin) ||
  !/^https:\/\/127\.0\.0\.1:\d+$/.test(fixtureOrigin) ||
  !caFile
)
  throw new Error("Owned loopback Catalog and fixture required");
const scenario = JSON.parse(
  await readFile(join(directory, "scenario.json"), "utf8"),
);
if (
  !["core", "hybrid"].includes(scenario.mode) ||
  scenario.live.length !== 25 ||
  scenario.expected.length <= 24 ||
  scenario.expected.length > 48
)
  throw new Error("Invalid peer browser inventory");
const ca = await readFile(caFile);
async function fault(enabled) {
  await new Promise((resolve, reject) => {
    const body = JSON.stringify({ enabled, initial: enabled });
    const call = request(
      fixtureOrigin + "/peer-fault",
      {
        method: "POST",
        ca,
        headers: { "Content-Type": "application/json" },
        timeout: 2000,
      },
      (response) => {
        response.resume();
        response.once("error", reject);
        response.once("end", () =>
          response.statusCode === 200
            ? resolve()
            : reject(new Error("Peer fault control failed")),
        );
      },
    );
    call.once("error", reject);
    call.once("timeout", () =>
      call.destroy(new Error("Peer control deadline")),
    );
    call.end(body);
  });
}
/** Exercise the real saved-membership API through the host while its peer is unavailable. */
async function qualifySavedMembership(page) {
  const saved = scenario.comparison;
  if (!saved || !Array.isArray(saved.identifiers) || !saved.identifiers.length)
    throw new Error("Owned retained comparison prerequisite missing");
  const writes = [];
  const observe = (call) => {
    if (
      new URL(call.url()).pathname.includes("/_catalog/api/") &&
      call.method() !== "GET"
    )
      writes.push(call.method());
    if (call.url().includes("/raw")) writes.push("raw evidence read");
  };
  page.on("request", observe);
  try {
    await fault(true);
    const comparisonReply = (view) =>
      page.waitForResponse((reply) => {
        const url = new URL(reply.url());
        return (
          url.pathname.endsWith(`/snapshots/${saved.snapshotId}/comparison`) &&
          url.searchParams.get("view") === view
        );
      });
    const first = comparisonReply("changes");
    await page.goto(
      `${origin}/source-snapshots/${saved.snapshotId}?compare=${saved.snapshotId}`,
    );
    const reply = await first;
    expect(reply.status()).toBe(200);
    expect((await reply.json()).items).toEqual([]);
    await expect(
      page.getByText("No entry differences between these snapshots.", {
        exact: true,
      }),
    ).toBeVisible();
    const all = comparisonReply("all");
    await page.getByLabel("Include unchanged entries", { exact: true }).check();
    let response = await all;
    const identifiers = [];
    for (let budget = 0; budget < 10; budget++) {
      expect(response.status()).toBe(200);
      const current = await response.json();
      expect(current.sourceId).toBe(saved.sourceId);
      expect(current.sourceRevision).toBe(saved.sourceRevision);
      expect(current.before.id).toBe(saved.snapshotId);
      expect(current.after.id).toBe(saved.snapshotId);
      for (const entry of current.items) {
        expect(entry.change).toBe("unchanged");
        expect(entry.before.observationId).toBe(entry.after.observationId);
        expect(entry.before.occurrence.snapshotId).toBe(saved.snapshotId);
        expect(entry.after.occurrence.snapshotId).toBe(saved.snapshotId);
        identifiers.push(entry.after.identifier);
        await expect(
          page.getByRole("heading", {
            name: `Unchanged: ${entry.after.identifier}`,
            exact: true,
          }),
        ).toBeVisible();
      }
      if (!current.nextCursor) break;
      const next = comparisonReply("all");
      await page
        .getByRole("group", { name: "Source comparison pages" })
        .getByRole("button", { name: "Next page", exact: true })
        .click();
      response = await next;
    }
    expect(identifiers.sort()).toEqual(saved.identifiers);
    // This pair comes from the earlier real B/C correction, independently
    // checked against exact retained bytes before later imports and policies.
    const changed = saved.changed;
    expect(changed.beforeId).not.toBe(changed.afterId);
    expect(changed.beforeDigest).not.toBe(changed.afterDigest);
    await page.goto(`${origin}/sources/${saved.sourceId}`);
    await page
      .getByRole("button", { name: "Sync history", exact: true })
      .click();
    const history = page.locator("section.workspace-panel").filter({
      has: page.getByRole("heading", {
        name: "Synchronization snapshots",
        exact: true,
      }),
    });
    await history
      .getByRole("radio", { name: `Before ${changed.beforeId}`, exact: true })
      .check();
    await history
      .getByRole("radio", { name: `After ${changed.afterId}`, exact: true })
      .check();
    const selected = history.getByRole("link", {
      name: "Compare selected snapshots",
      exact: true,
    });
    await expect(selected).toHaveAttribute(
      "href",
      `/source-snapshots/${changed.beforeId}?compare=${changed.afterId}#snapshot-comparison`,
    );
    const changedReply = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return (
        url.pathname.endsWith(`/snapshots/${changed.beforeId}/comparison`) &&
        url.searchParams.get("otherSnapshotId") === changed.afterId
      );
    });
    await selected.click();
    const actual = await changedReply;
    expect(actual.status()).toBe(200);
    const difference = await actual.json();
    expect(difference.before.id).toBe(changed.beforeId);
    expect(difference.after.id).toBe(changed.afterId);
    expect(difference.sourceId).toBe(saved.sourceId);
    expect(difference.sourceRevision).toBe(changed.sourceRevision);
    expect(difference.nextCursor).toBeUndefined();
    expect(difference.items).toHaveLength(1);
    const row = difference.items[0];
    expect(row.change).toBe("changed");
    expect(row.before.identifier).toBe(changed.identifier);
    expect(row.after.identifier).toBe(changed.identifier);
    expect(row.before.observationId).toBe(changed.beforeObservationId);
    expect(row.after.observationId).toBe(changed.afterObservationId);
    expect(row.before.occurrence.snapshotId).toBe(changed.beforeId);
    expect(row.after.occurrence.snapshotId).toBe(changed.afterId);
    expect(row.before.occurrence.exactEntryDigest).toBe(changed.beforeDigest);
    expect(row.after.occurrence.exactEntryDigest).toBe(changed.afterDigest);
    const panel = page.locator("#snapshot-comparison");
    await expect(panel).toBeFocused();
    await expect(
      panel.getByRole("heading", {
        name: "Compare saved entries",
        exact: true,
      }),
    ).toBeInViewport();
    await expect(
      panel.getByRole("heading", {
        name: `Changed: ${changed.identifier}`,
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      panel.getByRole("link", {
        name: "Inspect before observation and occurrences",
      }),
    ).toHaveAttribute("href", `/observations/${changed.beforeObservationId}`);
    await expect(
      panel.getByRole("link", {
        name: "Inspect after observation and occurrences",
      }),
    ).toHaveAttribute("href", `/observations/${changed.afterObservationId}`);
    await panel.screenshot({
      path: join(directory, `source-comparison-changed-${scenario.mode}.png`),
    });
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
      await new AxeBuilder({ page }).include("#snapshot-comparison").analyze(),
    ).toMatchObject({ violations: [], incomplete: [] });
    await panel.screenshot({
      path: join(
        directory,
        `source-comparison-changed-${scenario.mode}-narrow.png`,
      ),
    });
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: "System", exact: true }).check();
    await page.keyboard.press("Escape");
    expect(writes).toEqual([]);
    await expect(page.getByRole("alert")).toHaveCount(0);
    await json(`source-comparison-${scenario.mode}.json`, {
      result: "passed",
      snapshotId: saved.snapshotId,
      sourceId: saved.sourceId,
      sourceRevision: saved.sourceRevision,
      rows: identifiers.length,
      peerUnavailable: true,
      noMutations: true,
      noRawEvidenceReads: true,
      distinctSnapshotChange: true,
      historySelection: true,
    });
  } finally {
    page.off("request", observe);
    await fault(false);
  }
}

const listener = createServer();
await new Promise((resolve, reject) => {
  listener.once("error", reject);
  listener.listen(0, "127.0.0.1", resolve);
});
const port = listener.address().port;
await new Promise((resolve) => listener.close(resolve));
const origin = `http://127.0.0.1:${port}`;
async function json(name, value) {
  const path = join(directory, name);
  await writeFile(path, JSON.stringify(value), { mode: 0o600 });
  return path;
}
const keysFile = await json("keys.json", [
  {
    hash: randomBytes(64).toString("hex"),
    block: randomBytes(32).toString("hex"),
  },
]);
const developmentUsersFile = await json("users.json", [
  { alias: "local-admin", label: "local-admin", token: "local-admin" },
]);
const config = await json("ui.json", {
  listen: `127.0.0.1:${port}`,
  environment: "development",
  origin,
  upstream: apiOrigin,
  assets: join(directory, "assets"),
  cookieName: `peer-ui-${port}`,
  keysFile,
  sessionSeconds: 3600,
  requestBytes: 2097152,
  responseBytes: 16777216,
  timeoutSeconds: 30,
  authMode: "development",
  developmentUsersFile,
});
const host = spawn(join(directory, "catalog-ui"), ["--config", config], {
  stdio: "ignore",
});
const exited = new Promise((resolve, reject) => {
  host.once("error", reject);
  host.once("exit", (code, signal) => resolve({ code, signal }));
});
let browser;
const stop = () => {
  host.kill("SIGTERM");
  void browser?.close();
};
process.on("SIGINT", stop);
process.on("SIGTERM", stop);
try {
  await expect
    .poll(
      async () => {
        try {
          return (
            await fetch(origin + "/livez", {
              signal: AbortSignal.timeout(1000),
            })
          ).status;
        } catch {
          return 0;
        }
      },
      { timeout: 10000 },
    )
    .toBe(200);
  browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 1280, height: 900 },
  });
  const page = await context.newPage();
  const network = networkObserver(origin, "/_catalog");
  const errors = [];
  page.on("pageerror", () => errors.push("browser exception"));
  page.on("response", (reply) =>
    network.observe(reply.request().method(), reply.url(), reply.status()),
  );
  page.on("requestfailed", (call) =>
    network.failure(call.method(), call.url(), call.failure()?.errorText),
  );
  await page.goto(origin + "/catalog");
  await page.getByRole("button", { name: "local-admin", exact: true }).click();
  const cards = page.locator("article.resource-card");
  await expect(cards.first()).toBeVisible();
  await qualifySavedMembership(page);
  await page.goto(origin + "/catalog");
  await expect(cards.first()).toBeVisible();
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page
    .getByRole("checkbox", { name: "Local Catalog", exact: true })
    .uncheck();
  await page
    .getByRole("checkbox", { name: "Indexed external", exact: true })
    .uncheck();
  await page
    .getByRole("checkbox", { name: "Live external", exact: true })
    .check();
  const reply = (text) =>
    page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/v1/catalog/search") &&
        r.request().method() === "POST" &&
        (!text || r.request().postDataJSON().text === text),
    );
  const applied = reply();
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await applied;
  const search = page.getByRole("searchbox", {
    name: "Search Catalog",
    exact: true,
  });
  const firstRead = reply("browserpeer");
  await search.fill("browserpeer");
  await search.press("Enter");
  const firstResponse = await firstRead;
  const first = await firstResponse.json();
  expect(firstResponse.status()).toBe(200);
  expect(first.tier).toBe("core");
  expect(first.durableTotal).toBe(0);
  expect(first.liveCount).toBe(24);
  expect(first.nextCursor).toMatch(/^lc1\./);
  await expect(cards).toHaveCount(24);
  const names = await cards.locator("h2").allTextContents();
  const identifiers = first.items.map((row) => row.identifier);
  expect(new Set(identifiers).size).toBe(24);
  expect(firstResponse.request().postDataJSON().limit).toBe(24);
  network.expectFailure("POST", "/api/v1/catalog/search", 503);
  await fault(true);
  const failedRead = reply();
  await page.getByRole("button", { name: /Next page/ }).click();
  const failed = await failedRead;
  expect(failed.status()).toBe(503);
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(cards).toHaveCount(24);
  expect(await cards.locator("h2").allTextContents()).toEqual(names);
  await expect(page.getByRole("button", { name: /Next page/ })).toBeDisabled();
  await fault(false);
  const recoveredRead = reply();
  await page
    .getByRole("button", { name: "Retry this page", exact: true })
    .click();
  const recovered = await recoveredRead;
  expect(recovered.status()).toBe(200);
  expect(recovered.request().postDataJSON()).toEqual(
    failed.request().postDataJSON(),
  );
  const last = await recovered.json();
  expect(last.liveCount).toBe(1);
  expect(last.nextCursor).toBeUndefined();
  expect(
    [...identifiers, ...last.items.map((row) => row.identifier)].sort(),
  ).toEqual(scenario.live);
  await expect(cards).toHaveCount(1);
  await expect(page.getByRole("alert")).toHaveCount(0);
  await page.getByRole("button", { name: "First page", exact: true }).click();
  await expect(cards).toHaveCount(24);
  expect(await cards.locator("h2").allTextContents()).toEqual(names);
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page
    .getByRole("checkbox", { name: "Local Catalog", exact: true })
    .check();
  await page
    .getByRole("checkbox", { name: "Indexed external", exact: true })
    .check();
  const combinedApplied = reply();
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await combinedApplied;
  const combinedRead = reply("peer");
  await search.fill("peer");
  await search.press("Enter");
  const combined = await (await combinedRead).json();
  expect(combined.tier).toBe(scenario.mode);
  expect(combined.items).toHaveLength(24);
  const all = combined.items.map((row) => row.identifier);
  await expect(cards).toHaveCount(24);
  const finalRead = reply();
  await page.getByRole("button", { name: /Next page/ }).click();
  const final = await (await finalRead).json();
  expect(final.tier).toBe(scenario.mode);
  expect(final.nextCursor).toBeUndefined();
  all.push(...final.items.map((row) => row.identifier));
  expect(all.sort()).toEqual(scenario.expected);
  await expect(cards).toHaveCount(scenario.expected.length - 24);
  expect(network.results()).toEqual([]);
  expect(errors).toEqual([]);
  await context.close();
} finally {
  try {
    await fault(false);
  } finally {
    await browser?.close();
    host.kill("SIGTERM");
    const timer = setTimeout(() => host.kill("SIGKILL"), 5000);
    try {
      expect(await exited).toEqual({ code: 0, signal: null });
    } finally {
      clearTimeout(timer);
    }
    process.off("SIGINT", stop);
    process.off("SIGTERM", stop);
  }
}
process.stdout.write(
  JSON.stringify({
    mode: scenario.mode,
    livePages: 2,
    combinedPages: 2,
    sameTokenRetry: true,
    sourceComparison: true,
    cleanup: "passed",
  }),
);
