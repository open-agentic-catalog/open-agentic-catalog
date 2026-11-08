// Selected only by the disposable monitoring supervisor, after timed work.
import {
  chromium,
  expect,
} from "../../catalog-ui/node_modules/@playwright/test/index.mjs";
import { readFile } from "node:fs/promises";
import { hasPositiveHeapQuery } from "../../catalog-ui/scripts/monitoring-query.mjs";

const [inputFile] = process.argv.slice(2);
const input = JSON.parse(await readFile(inputFile, "utf8"));
if (
  !/^http:\/\/127\.0\.0\.1:\d+$/.test(input.origin) ||
  !/^catalog-browser-[a-f0-9]{24}$/.test(input.uid) ||
  !Array.isArray(input.window) ||
  input.window.length !== 2 ||
  input.window.some((value) => !Number.isSafeInteger(value) || value <= 0) ||
  input.window[1] <= input.window[0]
)
  throw new Error("Owned monitoring browser input required");

const browser = await chromium.launch({ headless: true });
const failures = [];
const pending = [];
const queries = [];
let context;
let owned = false;
let inspectQueries = true;
let expectedAvailabilityChecks = 0;
const availabilityPath = `/apis/dashboard.grafana.app/v2/namespaces/default/dashboards/${input.uid}/dto`;
try {
  context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
  });
  const page = await context.newPage();
  page.on("pageerror", () => failures.push("browser-exception"));
  page.on("console", (message) => {
    if (
      message.type() === "error" &&
      !(
        message.location().url === input.origin + availabilityPath &&
        !owned &&
        message.text().includes("404 (Not Found)")
      )
    )
      failures.push("console-error");
  });
  page.on("requestfailed", (request) => {
    if (request.failure()?.errorText !== "net::ERR_ABORTED")
      failures.push("failed-request");
  });
  page.on("response", (response) => {
    if (
      response.status() === 404 &&
      !owned &&
      new URL(response.url()).pathname === availabilityPath
    )
      expectedAvailabilityChecks++;
    else if (response.status() >= 400) failures.push("http-error");
    if (
      inspectQueries &&
      new URL(response.url()).pathname === "/api/ds/query"
    ) {
      pending.push(
        (async () => {
          const request = response.request().postDataJSON();
          const body = await response.json();
          queries.push({ request, body });
        })().catch(() => failures.push("invalid-query-response")),
      );
    }
  });
  await page.goto(input.origin + "/login");
  await page
    .getByRole("textbox", { name: "Email or username" })
    .fill("catalog");
  await page
    .getByRole("textbox", { name: "Password", exact: true })
    .fill((await readFile(input.passwordFile, "utf8")).trim());
  await page.getByRole("button", { name: "Log in", exact: true }).click();
  await page.waitForURL((url) => url.pathname !== "/login");
  const provisioned = await context.request.get(
    input.origin + "/api/dashboards/uid/catalog-overview",
  );
  expect(provisioned.status()).toBe(200);
  const original = await provisioned.json();
  expect(original.dashboard.editable).toBe(false);
  const parameters = new URLSearchParams({
    from: String(input.window[0]),
    to: String(input.window[1]),
    "var-datasource": "catalog-prometheus",
    "var-deployment": "local",
    "var-role": "$__all",
    refresh: "",
  });
  await page.goto(input.origin + "/d/catalog-overview?" + parameters);
  await expect(
    page.getByRole("heading", { name: "Scrape health", exact: true }),
  ).toBeVisible();
  await page.getByRole("combobox", { name: "Deployment", exact: true }).click();
  await page.getByRole("option", { name: "local", exact: true }).click();
  const selected = [];
  for (const role of ["api", "worker", "All"]) {
    const first = queries.length;
    await page.getByRole("combobox", { name: "Role", exact: true }).click();
    await page.getByRole("option", { name: role, exact: true }).click();
    const heap = page.getByRole("region", {
      name: "Heap bytes (not RSS)",
      exact: true,
    });
    await heap.scrollIntoViewIfNeeded();
    await expect
      .poll(
        async () => {
          await Promise.all(pending);
          return queries
            .slice(first)
            .some((exchange) =>
              hasPositiveHeapQuery(exchange, role, input.window),
            );
        },
        { timeout: 20000 },
      )
      .toBe(true);
    await expect(
      heap.getByText("Unavailable / no samples", { exact: true }),
    ).toHaveCount(0);
    selected.push(role);
  }
  inspectQueries = false;
  await Promise.all(pending);
  // Never change the provisioned dashboard. Import/update a unique owned UID
  // through the native workflow, with controlled zero and unavailable panels.
  const copy = structuredClone(original.dashboard);
  copy.id = null;
  copy.uid = input.uid;
  copy.title = "Catalog browser fixture " + input.uid.slice(-8);
  copy.editable = true;
  copy.refresh = "";
  copy.panels.forEach((panel) => {
    panel.gridPos.y += 4;
  });
  const panel = (id, title, expression, x) => ({
    id,
    title,
    type: "stat",
    gridPos: { x, y: 0, w: 12, h: 4 },
    datasource: { type: "prometheus", uid: "catalog-prometheus" },
    targets: [{ refId: "A", expr: expression, instant: true, range: false }],
    fieldConfig: {
      defaults: { noValue: "Unavailable / no samples", decimals: 0 },
      overrides: [],
    },
    options: {
      reduceOptions: { calcs: ["lastNotNull"], fields: "", values: false },
      textMode: "value",
      graphMode: "none",
      colorMode: "none",
    },
  });
  copy.panels.unshift(
    panel(1001, "Controlled zero", "vector(0)", 0),
    panel(
      1002,
      "Controlled unavailable",
      `open_agentic_catalog_browser_absent{invocation="${input.uid}"}`,
      12,
    ),
  );
  for (const update of [false, true]) {
    if (update) copy.title += " updated";
    await page.goto(input.origin + "/dashboard/import");
    await expect(
      page.getByRole("heading", { name: "Import dashboard", exact: true }),
    ).toBeVisible();
    await page.locator('input[type="file"]').setInputFiles({
      name: "catalog-browser.json",
      mimeType: "application/json",
      buffer: Buffer.from(JSON.stringify(copy)),
    });
    await expect(
      page.getByRole("textbox", { name: "Name", exact: true }),
    ).toHaveValue(copy.title);
    await page
      .getByRole("button", {
        name: update ? "Import (Overwrite)" : "Import",
        exact: true,
      })
      .click();
    await page.waitForURL((url) =>
      url.pathname.startsWith("/d/" + input.uid + "/"),
    );
    owned = true;
    const saved = await context.request.get(
      input.origin + "/api/dashboards/uid/" + input.uid,
    );
    expect(saved.status()).toBe(200);
    expect((await saved.json()).dashboard.title).toBe(copy.title);
    await expect(
      page
        .getByRole("region", { name: "Controlled zero", exact: true })
        .getByText("0", { exact: true }),
    ).toBeVisible();
    await expect(
      page
        .getByRole("region", { name: "Controlled unavailable", exact: true })
        .getByText("Unavailable / no samples", { exact: true }),
    ).toBeVisible();
  }
  const unchanged = await context.request.get(
    input.origin + "/api/dashboards/uid/catalog-overview",
  );
  expect((await unchanged.json()).dashboard).toEqual(original.dashboard);
  await Promise.all(pending);
  expect(failures).toEqual([]);
  const controls = [];
  for (const fault of ["selector", "dataSource", "asset"]) {
    // Faults stay inside a separate page of this owned fixture. Require the
    // intended native failure as well as the healthy dashboard above.
    const probe = await context.newPage();
    let intercepted = 0;
    try {
      if (fault === "asset") {
        await probe.route("**/public/build/**", async (route) => {
          intercepted++;
          await route.abort();
        });
      } else if (fault === "dataSource") {
        await probe.route("**/api/ds/query*", async (route) => {
          intercepted++;
          await route.abort();
        });
      }
      await probe.goto(input.origin + "/d/" + input.uid + "?" + parameters);
      let expected;
      if (fault === "selector") {
        const role = probe.getByRole("combobox", { name: "Role", exact: true });
        await expect(role).toBeVisible();
        await role.evaluate((element) =>
          element.setAttribute("aria-label", "Broken owned selector"),
        );
        await expect(role).toHaveCount(0);
        expected = role;
      } else if (fault === "dataSource") {
        const region = probe.getByRole("region", {
          name: "Controlled zero",
          exact: true,
        });
        await expect(region).toBeVisible();
        expected = region.getByText("0", { exact: true });
      } else
        expected = probe.getByRole("combobox", { name: "Role", exact: true });
      let refused = false;
      try {
        await expect(expected).toBeVisible({ timeout: 2000 });
      } catch (error) {
        if (
          error.matcherResult?.name !== "toBeVisible" ||
          error.matcherResult.pass !== false
        )
          throw error;
        refused = true;
      }
      expect(refused).toBe(true);
      if (fault !== "selector") expect(intercepted).toBeGreaterThan(0);
      controls.push({ fault, refused: true });
    } finally {
      await probe.close();
    }
  }
  console.log(
    JSON.stringify({
      status: "passed",
      browser: browser.version(),
      window: input.window,
      roles: selected,
      deployment: "local",
      dataSource: "catalog-prometheus",
      nativeImportAndUpdate: true,
      controlledZero: true,
      controlledUnavailable: true,
      provisionedUnchanged: true,
      controls,
      expectedAvailabilityChecks,
      diagnostics: "passed",
    }),
  );
} finally {
  try {
    if (context && owned) {
      const removed = await context.request.delete(
        input.origin + "/api/dashboards/uid/" + input.uid,
      );
      expect(removed.status()).toBe(200);
    }
  } finally {
    await context?.close();
    await browser.close();
  }
}
