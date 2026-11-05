// Explicit browser qualification against the invocation-owned curated Catalog.
import { chromium, expect } from "@playwright/test";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { networkObserver } from "./network-observer.mjs";

const [origin, receiptFile, output] = process.argv.slice(2);
const target = new URL(origin);
if (
  target.origin !== origin ||
  target.protocol !== "http:" ||
  target.hostname !== "127.0.0.1" ||
  !target.port
)
  throw new Error("Select the owned loopback UI");
const receipt = JSON.parse(await readFile(receiptFile, "utf8"));
const directory = resolve(output);
await mkdir(directory, { recursive: true });
const report = { scope: "curated-owned-browser", success: false, phases: [] };
const browser = await chromium.launch({ headless: true });
let page;
try {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
  });
  page = await context.newPage();
  const errors = [];
  const network = networkObserver(origin);
  page.on("response", (response) =>
    network.observe(
      response.request().method(),
      response.url(),
      response.status(),
    ),
  );
  page.on("requestfailed", (request) =>
    network.failure(
      request.method(),
      request.url(),
      request.failure()?.errorText,
    ),
  );
  page.on("pageerror", () => errors.push("browser-error"));
  page.on("request", (request) => {
    if (new URL(request.url()).origin !== origin)
      errors.push("unexpected-browser-destination");
  });
  await page.goto(origin + "/catalog");
  await page.locator('[data-login-alias="local-reader"]').click();
  await expect(
    page.getByRole("heading", { name: "Catalog", exact: true }),
  ).toBeVisible();
  await expect(page.locator("article.resource-card")).toHaveCount(12);
  await page.screenshot({ path: resolve(directory, "catalog-browse.png") });
  report.phases.push("twelve-real-published-results");
  for (const name of ["Agents", "MCP Servers", "Skills", "Plugins"]) {
    await page.getByRole("button", { name, exact: true }).click();
    await expect(page.locator("article.resource-card")).toHaveCount(3);
  }
  await page.getByRole("button", { name: "All kinds", exact: true }).click();
  await expect(page.locator("article.resource-card")).toHaveCount(12);
  report.phases.push("four-equal-kind-filters");
  const sort = page.getByRole("combobox", {
    name: "Sort results",
    exact: true,
  });
  await expect(sort).toHaveValue("");
  const titles = page.locator("article.resource-card .card-heading h2 button");
  const names = await titles.allTextContents();
  expect(names).toHaveLength(12);
  expect(names).toEqual([...names].sort((a, b) => a.localeCompare(b)));
  await sort.selectOption("name_desc");
  await expect(titles).toHaveText([...names].reverse());
  await sort.selectOption("");
  await expect(titles).toHaveText(names);
  report.phases.push("default-and-descending-name-order");
  const categoryRequests = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname.endsWith("/catalog/explore"))
      categoryRequests.push(request.postDataJSON());
  });
  const categories = page.getByRole("button", {
    name: "Categories",
    exact: true,
  });
  await categories.click();
  await expect(
    page.getByRole("button", {
      name: "Add category",
      exact: true,
    }),
  ).toBeFocused();
  await expect(
    page.getByRole("checkbox", { name: "development", exact: true }),
  ).toBeVisible();
  await page.getByRole("checkbox", { name: "testing", exact: true }).check();
  await page
    .getByRole("checkbox", { name: "development", exact: true })
    .check();
  await expect(page.locator("article.resource-card")).toHaveCount(12);
  expect(categoryRequests).toEqual([
    {
      query: { text: "" },
      facets: [{ field: "category", limit: 24, minCount: 1 }],
    },
  ]);
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  for (const kind of ["mcp_server", "skill", "plugin"])
    await expect(
      page.locator(`article.resource-card[data-kind="${kind}"]`),
    ).toHaveCount(1);
  await page.getByRole("button", { name: "Clear all", exact: true }).click();
  await expect(page.locator("article.resource-card")).toHaveCount(12);
  await page.setViewportSize({ width: 390, height: 844 });
  await categories.click();
  await expect(
    page.getByRole("dialog", { name: "Filters", exact: true }),
  ).toBeVisible();
  await page.getByRole("checkbox", { name: "design", exact: true }).check();
  await page.keyboard.press("Escape");
  await expect(categories).toBeFocused();
  await expect(page.locator("article.resource-card")).toHaveCount(12);
  await page.setViewportSize({ width: 1440, height: 1000 });
  report.phases.push(
    "bounded-local-category-counts-conjunction-and-mobile-dismissal",
  );
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await expect(
    page.getByRole("checkbox", { name: "development", exact: true }),
  ).toBeVisible();
  await page.screenshot({ path: resolve(directory, "catalog-filters.png") });
  await page.keyboard.press("Escape");
  for (const key of [
    "hello-world",
    "everything",
    "frontend-design",
    "frontend-toolkit",
  ]) {
    report.phase = "handoff-" + key;
    const item = receipt.items.find((value) => value.key === key);
    if (!item?.releaseId) throw new Error("Missing curated browser input");
    report.step = "record-response";
    const response = await context.request.get(
      origin + "/_catalog/api/v1/catalog/resources/" + item.releaseId,
      { headers: { Origin: origin, "X-Catalog-Request": "1" } },
    );
    report.responseStatus = response.status();
    report.responseType = response.headers()["content-type"];
    expect(response.status()).toBe(200);
    const record = await response.json();
    report.step = "preview-open";
    await page
      .getByRole("button", {
        name: record.listingRevision.displayName,
        exact: true,
      })
      .click();
    const dialog = page.getByRole("dialog");
    report.step = "preview-region";
    await expect(
      dialog.getByRole("region", { name: "Resource handoff" }),
    ).toBeVisible();
    await expect(
      dialog.getByText(record.listingRevision.description, { exact: true }),
    ).toBeVisible();
    report.step = "preview-identifier";
    report.identifierMatches = await dialog
      .getByText(item.identifier, { exact: true })
      .count();
    const reference = dialog.getByRole("group", {
      name: "Exact resource reference",
      exact: true,
    });
    await expect(
      reference.getByText(item.identifier, { exact: true }),
    ).toBeVisible();
    if (key === "frontend-design") {
      await expect(
        dialog.getByText(
          "The archive is not stored for download. Retained inspection documents are evidence, not the complete package. Use the declared external archive or immutable Git/OCI reference; no archive is reconstructed here.",
          { exact: true },
        ),
      ).toBeVisible();
      await expect(
        dialog.getByText("8a1541c4a3ffa5a20a5a91de0dcf3f0bab1d1ef4", {
          exact: true,
        }),
      ).toBeVisible();
      await page.screenshot({
        path: resolve(directory, "catalog-preview.png"),
      });
    }
    await page
      .getByRole("button", { name: "Close preview", exact: true })
      .click();
    report.step = "full-record";
    await page.goto(origin + "/catalog/" + item.releaseId);
    await expect(
      page.getByRole("heading", {
        name: record.listingRevision.displayName,
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page
        .getByRole("region", { name: "Resource handoff", exact: true })
        .getByRole("link", { name: "Documentation", exact: true }),
    ).toHaveAttribute("href", record.listingRevision.documentationUrl);
    report.step = "resource-namespace";
    await page.goto(origin + "/resources/" + item.resourceId);
    const namespace =
      key === "frontend-toolkit" ? "IDE plugins" : "Catalog demo";
    await page.getByRole("link", { name: "Namespace", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: namespace, exact: true }),
    ).toBeVisible();
    await page.goto(origin + "/catalog");
    await expect(page.locator("article.resource-card")).toHaveCount(12);
  }
  report.phases.push("actual-four-kind-preview-detail-and-namespace-handoff");
  await page.goto(origin + "/namespaces");
  await expect(
    page.getByRole("link", { name: "Catalog demo", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "IDE plugins", exact: true }),
  ).toBeVisible();
  expect(errors).toEqual([]);
  expect(network.results()).toEqual([]);
  report.phases.push("two-namespaces-no-external-browser-request");
  report.success = true;
} catch (error) {
  // This launcher accepts only the owned curated fixture. Keep visual failure evidence.
  await page
    ?.screenshot({ path: resolve(directory, "failure.png"), timeout: 5000 })
    .catch(() => {
      // Preserve the original assertion if the failed page cannot be captured.
    });
  throw error;
} finally {
  await browser.close();
  await writeFile(
    resolve(directory, "report.json"),
    JSON.stringify(report, null, 2) + "\n",
  );
}
