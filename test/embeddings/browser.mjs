// Real hybrid-search browser assertions against the semantic harness's owned API.
// The temporary UI host has no database/model credentials and is always reaped.
import {
  chromium,
  expect,
} from "../../catalog-ui/node_modules/@playwright/test/index.mjs";
import { randomBytes } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { collectBrowserCoverage } from "../../catalog-ui/scripts/e2e-coverage.mjs";

const [directory, apiOrigin, mode, snapshotId] = process.argv.slice(2);
if (
  !directory ||
  !/^http:\/\/127\.0\.0\.1:\d+$/.test(apiOrigin) ||
  !["hybrid", "core"].includes(mode) ||
  !/^[0-9a-f-]{36}$/.test(snapshotId ?? "")
)
  throw new Error(
    "Owned loopback fixture and expected retrieval mode required",
  );
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
const developmentUsersFile = await json(
  "users.json",
  ["local-reader", "local-admin"].map((alias) => ({
    alias,
    label: alias,
    token: alias,
  })),
);
const config = await json("ui.json", {
  listen: `127.0.0.1:${port}`,
  environment: "development",
  origin,
  upstream: apiOrigin,
  assets: join(directory, "assets"),
  cookieName: `semantic-ui-${port}`,
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
  host.once("exit", resolve);
});
let browser;
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
    viewport: { width: 390, height: 844 },
  });
  const coverage = await collectBrowserCoverage(
    context,
    process.env.CATALOG_UI_E2E_COVERAGE,
  );
  const page = await context.newPage();
  await coverage.ready(page);
  const jobs = [];
  const failures = [];
  const searches = [];
  let explores = 0;
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/api/v1/catalog/search"))
      searches.push(request.postDataJSON());
    if (path.endsWith("/api/v1/catalog/explore")) explores++;
  });
  page.on("pageerror", () => failures.push("browser exception"));
  page.on("response", (response) => {
    if (response.status() >= 400)
      failures.push(
        `${response.request().method()} ${new URL(response.url()).pathname}: ${response.status()}`,
      );
  });
  for (const alias of ["local-reader", "local-admin"]) {
    await page.goto(origin + "/catalog");
    await page.getByRole("button", { name: alias, exact: true }).click();
    const search = page.getByRole("searchbox", {
      name: "Search Catalog",
      exact: true,
    });
    await search.fill(mode === "hybrid" ? "purchase assistance" : "order");
    await search.press("Enter");
    await expect(
      page.getByText(
        mode === "hybrid"
          ? "Keyword and meaning-based search"
          : "Keyword search",
        {
          exact: true,
        },
      ),
    ).toBeVisible();
    await expect(page.locator("article.resource-card").first()).toBeVisible();
    if (mode === "core")
      await expect(
        page.getByText(
          "Semantic search is temporarily unavailable. These results use keyword search.",
          { exact: true },
        ),
      ).toBeVisible();
    const appliedText = mode === "hybrid" ? "purchase assistance" : "order";
    const beforeDraft = searches.length;
    const beforeExplore = explores;
    await search.fill("unsubmitted semantic reader draft");
    await expect(page.locator("#pending-search-text")).toContainText(
      "Text changed. Choose Search to apply it.",
    );
    expect(searches).toHaveLength(beforeDraft);
    expect(explores).toBe(beforeExplore);
    const refresh = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname.endsWith("/api/v1/catalog/search") &&
        response.request().method() === "POST",
    );
    await page
      .getByRole("button", { name: "Refresh results", exact: true })
      .click();
    const refreshed = await refresh;
    expect(refreshed.status()).toBe(200);
    expect(refreshed.request().postDataJSON()).toMatchObject({
      text: appliedText,
    });
    expect(await refreshed.json()).toMatchObject({
      tier: mode,
      partial: false,
    });
    await expect(page.locator("article.resource-card").first()).toBeVisible();
    await expect(search).toHaveValue("unsubmitted semantic reader draft");
    expect(searches).toHaveLength(beforeDraft + 1);
    expect(explores).toBe(beforeExplore);

    // Persist only applied criteria in the owned SQL library, then exercise
    // saved-search consent against the real enabled model (or its outage).
    await page.getByText("Save this named search", { exact: true }).click();
    await page
      .getByRole("textbox", { name: "Saved label", exact: true })
      .fill("Semantic reader workflow");
    await expect(
      page.getByRole("textbox", { name: "Saved search text", exact: true }),
    ).toHaveValue(appliedText);
    await page
      .getByRole("button", { name: "Save named search", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    await page
      .getByRole("link", { name: "Open saved item", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Open search editor", exact: true })
      .click();
    const consent = page.getByRole("checkbox", {
      name: /configured embedding provider/,
    });
    await expect(consent).not.toBeChecked();
    await expect(
      page.getByRole("heading", { name: "Saved search", exact: true }),
    ).toBeVisible();
    expect(searches).toHaveLength(beforeDraft + 1);
    expect(explores).toBe(beforeExplore);
    await consent.check();
    expect(searches).toHaveLength(beforeDraft + 1);
    expect(explores).toBe(beforeExplore);
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText(
      "Review query disclosure and provider consent before searching.",
    );
    expect(searches).toHaveLength(beforeDraft + 1);
    expect(explores).toBe(beforeExplore);
    const liveConsent = page.getByRole("checkbox", {
      name: /Allow disclosure of this query to current administrator-managed live sources/,
    });
    await expect(liveConsent).not.toBeChecked();
    await liveConsent.check();
    expect(searches).toHaveLength(beforeDraft + 1);
    expect(explores).toBe(beforeExplore);
    const submitted = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname.endsWith("/api/v1/catalog/search") &&
        response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Search", exact: true }).click();
    const submittedResponse = await submitted;
    expect(submittedResponse.status()).toBe(200);
    expect(submittedResponse.request().postDataJSON()).toMatchObject({
      text: appliedText,
    });
    expect(await submittedResponse.json()).toMatchObject({
      tier: mode,
      partial: false,
    });
    await expect(page.locator("article.resource-card").first()).toBeVisible();
    expect(searches).toHaveLength(beforeDraft + 2);
    expect(explores).toBe(beforeExplore);
    // Visit the real admin surface with both identities. No disabled/read-only
    // browser control is treated as a replacement for backend authorization.
    await page
      .getByRole("button", { name: "Workspace menu", exact: true })
      .click();
    const indexingLink = page.getByRole("link", {
      name: "Search indexing",
      exact: true,
    });
    if (alias === "local-reader") {
      await expect(indexingLink).toHaveCount(0);
      await page.goto(origin + "/service");
    } else {
      await indexingLink.click();
      await expect(page).toHaveURL(origin + "/service#search-indexing");
      await expect(
        page.getByRole("heading", { name: "Search indexing", exact: true }),
      ).toBeInViewport();
    }
    if (mode === "core")
      await expect(
        page
          .locator("dt")
          .filter({ hasText: /^Search using a fallback$/ })
          .locator("+ dd"),
      ).toHaveText("Yes");
    const rebuild = page.getByRole("heading", {
      name: "Rebuild search index",
      exact: true,
    });
    if (alias === "local-reader") {
      await expect(rebuild).toHaveCount(0);
    } else {
      await expect(rebuild).toBeVisible();
      if (mode === "hybrid") {
        for (const scope of ["control", "public"]) {
          await page
            .getByRole("combobox", {
              name: "Search index",
              exact: true,
            })
            .selectOption(scope);
          const reason = `Browser qualification ${scope} rebuild`;
          await page.getByLabel("Reason", { exact: true }).fill(reason);
          if (scope === "public") {
            let writes = 0;
            const count = (request) => {
              if (request.method() === "POST") writes++;
            };
            page.on("request", count);
            await page
              .getByLabel("Active snapshot ID", { exact: true })
              .fill("invalid-snapshot");
            await page
              .getByRole("button", {
                name: "Rebuild search index",
                exact: true,
              })
              .click();
            await expect(page.getByRole("dialog")).toHaveCount(0);
            expect(writes).toBe(0);
            page.off("request", count);
            await page
              .getByRole("button", {
                name: "Choose active snapshot",
                exact: true,
              })
              .click();
            const hostChoices = page.getByRole("combobox", {
              name: "Publication host",
              exact: true,
            });
            await expect(hostChoices.locator("option")).not.toHaveCount(1);
            await hostChoices.selectOption("127.0.0.1");
            await page
              .getByRole("button", {
                name: "Read active snapshot",
                exact: true,
              })
              .click();
            await expect(
              page.getByRole("region", { name: "Active snapshot lookup" }),
            ).toContainText(snapshotId);
            await page
              .getByRole("button", {
                name: "Use observed snapshot for reindexing",
                exact: true,
              })
              .click();
            await expect(
              page.getByLabel("Active snapshot ID", { exact: true }),
            ).toHaveValue(snapshotId);
          }
          const accepted = page.waitForResponse(
            (response) =>
              response.request().method() === "POST" &&
              response.url().endsWith("/api/v1/catalog:reindex"),
          );
          await page
            .getByRole("button", { name: "Rebuild search index", exact: true })
            .click();
          await page
            .getByRole("dialog")
            .getByRole("button", { name: "Confirm", exact: true })
            .click();
          const response = await accepted;
          expect(response.status()).toBe(202);
          expect(response.request().postDataJSON()).toEqual({
            reason,
            ...(scope === "public" ? { snapshotId } : {}),
          });
          const job = await response.json();
          expect(job.kind).toBe("semantic.reindex");
          const jobLink = page.getByRole("link", {
            name: "View indexing Job",
            exact: true,
          });
          await expect(jobLink).toHaveAttribute("href", `/jobs/${job.id}`);
          await expect(
            page.getByRole("region", { name: "Accepted indexing request" }),
          ).toContainText(scope === "public" ? snapshotId : "Catalog search");
          jobs.push({ id: job.id, scope });
          await jobLink.click();
          await expect(
            page.getByRole("heading", {
              name: "Search indexing",
              exact: true,
            }),
          ).toBeVisible();
          await page.goto(origin + "/service");
        }
      }
    }
    await page.getByTitle("Account", { exact: true }).click();
    await page.getByRole("button", { name: "Sign out", exact: true }).click();
    await expect(page.getByTitle("Account", { exact: true })).toHaveCount(0);
  }
  expect(failures).toEqual([]);
  await coverage.finish();
  await context.close();
  console.log(
    JSON.stringify({
      browser: mode,
      version: browser.version(),
      jobs,
      reader: "passed",
      administrator: "passed",
    }),
  );
} finally {
  await browser?.close();
  host.kill("SIGTERM");
  const force = setTimeout(() => host.kill("SIGKILL"), 5000);
  await exited;
  clearTimeout(force);
}
