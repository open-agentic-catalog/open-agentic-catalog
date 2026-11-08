// Native navigation of a fresh privacy-checked exported Job handoff in Jaeger.
import {
  chromium,
  expect,
} from "../../catalog-ui/node_modules/@playwright/test/index.mjs";
import { readFile } from "node:fs/promises";

const [filename] = process.argv.slice(2);
const input = JSON.parse(await readFile(filename, "utf8"));
if (
  !/^http:\/\/127\.0\.0\.1:\d+$/.test(input.origin) ||
  !Array.isArray(input.window) ||
  input.window.length !== 2 ||
  input.window.some((value) => !Number.isSafeInteger(value) || value <= 0) ||
  input.window[1] <= input.window[0]
) {
  throw new Error("Owned viewer and captured observation window required");
}
for (const name of ["request", "enqueue", "attempt", "sync", "replay"]) {
  if (
    !/^[a-f0-9]{32}$/.test(input.spans[name]?.traceId) ||
    !/^[a-f0-9]{16}$/.test(input.spans[name]?.spanId)
  ) {
    throw new Error("Exact decoded trace and span identities required");
  }
}
const { attempt, enqueue, request, sync, replay } = input.spans;
expect(enqueue.parentId).toBe(request.spanId);
expect(attempt.parentId).toBe("");
expect(attempt.links).toEqual([
  { traceId: enqueue.traceId, spanId: enqueue.spanId },
]);
expect(sync.parentId).toBe(attempt.spanId);
expect(enqueue.traceId).not.toBe(replay.traceId);
// The native custom-time controls retain whole minutes. Expand only to the
// enclosing minutes of the actual stored-span window, never a moving lookback.
const window = [
  Math.floor(input.window[0] / 60000000) * 60000000,
  Math.ceil(input.window[1] / 60000000) * 60000000,
];
const browser = await chromium.launch({ headless: true });
const diagnostics = [];
try {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
  });
  const observe = (page) => {
    page.on("pageerror", () => diagnostics.push("browser-exception"));
    page.on("console", (message) => {
      if (message.type() === "error") diagnostics.push("console-error");
    });
    page.on("response", (response) => {
      if (response.status() >= 400) diagnostics.push("http-error");
    });
    page.on("requestfailed", (request) => {
      if (request.failure()?.errorText !== "net::ERR_ABORTED")
        diagnostics.push("failed-request");
    });
  };
  context.on("page", observe);
  const page = await context.newPage();
  const parameters = new URLSearchParams({
    service: attempt.service,
    start: String(window[0]),
    end: String(window[1]),
    lookback: "custom",
    limit: "20",
    tags: JSON.stringify({
      "service.namespace": attempt.installation + "-unselected",
      "service.instance.id": attempt.instance,
    }),
  });
  await page.goto(input.origin + "/search?" + parameters);
  const controls = [];
  for (const selection of ["wrongInstallation", "wrongService", "correct"]) {
    const service =
      selection === "wrongService" ? request.service : attempt.service;
    const installation =
      attempt.installation +
      (selection === "wrongInstallation" ? "-absent" : "");
    const attributes = {
      "service.namespace": installation,
      "service.instance.id": attempt.instance,
    };
    await page.getByRole("combobox").first().click();
    // Ant Select keeps a hidden accessibility option beside its visible
    // virtualized menu item; click the rendered native item, never force it.
    await page
      .locator(".ant-select-item-option-content")
      .getByText(service, { exact: true })
      .click();
    await page
      .getByRole("textbox", {
        name: "http.status_code=200 error=true",
        exact: true,
      })
      .fill(
        `service.namespace=${installation} service.instance.id=${attempt.instance}`,
      );
    const result = page.waitForResponse((response) => {
      const url = new URL(response.url());
      if (
        url.pathname !== "/api/v3/trace-summaries" ||
        url.searchParams.get("query.serviceName") !== service
      )
        return false;
      const observed = JSON.parse(
        url.searchParams.get("query.attributes") ?? "{}",
      );
      return (
        observed["service.namespace"] === attributes["service.namespace"] &&
        observed["service.instance.id"] === attributes["service.instance.id"]
      );
    });
    await page
      .getByRole("button", { name: "Find Traces", exact: true })
      .click();
    const response = await result;
    expect(response.status()).toBe(200);
    const query = new URL(response.url()).searchParams;
    expect(Date.parse(query.get("query.startTimeMin")) * 1000).toBe(window[0]);
    expect(Date.parse(query.get("query.startTimeMax")) * 1000).toBe(window[1]);
    const body = await response.json();
    expect(body.error).toBeUndefined();
    expect(Array.isArray(body.summaries)).toBe(true);
    if (selection === "correct") {
      expect(body.summaries.map((trace) => trace.traceId)).toContain(
        attempt.traceId,
      );
      await expect(
        page.locator(`main a[href="/trace/${attempt.traceId}"]`),
      ).toBeVisible();
    } else {
      expect(body.summaries).toEqual([]);
      await expect(page.locator('main a[href^="/trace/"]')).toHaveCount(0);
    }
    controls.push({ selection, count: body.summaries.length });
  }
  await page.locator(`main a[href="/trace/${attempt.traceId}"]`).click();
  const attemptRow = page.getByRole("switch", {
    name: attempt.service + " " + attempt.name,
    exact: true,
  });
  await expect(attemptRow).toBeVisible();
  await expect(
    page.getByRole("switch", {
      name: sync.service + " " + sync.name,
      exact: true,
    }),
  ).toBeVisible();
  await attemptRow.click();
  await expect(
    page.locator(".SpanDetail--debugInfo").filter({ hasText: attempt.spanId }),
  ).toBeVisible();
  const linkedPath = `/trace/${enqueue.traceId}?uiFind=${enqueue.spanId}`;
  const link = attemptRow
    .locator("..")
    .locator("a.ReferencesButton-MultiParent");
  const requireLink = () =>
    expect(link).toHaveAttribute("href", linkedPath, { timeout: 1000 });
  await requireLink();
  // Calibrate the exact native link assertion on the otherwise healthy viewer.
  // Change only this owned page's link, then restore it before real navigation.
  await link.evaluate(
    (element, href) => element.setAttribute("href", href),
    `/trace/${replay.traceId}?uiFind=${enqueue.spanId}`,
  );
  let wrongLinkRefused = false;
  try {
    await requireLink();
  } catch (error) {
    if (
      error.matcherResult?.name !== "toHaveAttribute" ||
      error.matcherResult.pass !== false
    )
      throw error;
    wrongLinkRefused = true;
  }
  expect(wrongLinkRefused).toBe(true);
  await link.evaluate(
    (element, href) => element.setAttribute("href", href),
    linkedPath,
  );
  await requireLink();
  const popup = context.waitForEvent("page");
  await link.click();
  const cause = await popup;
  await expect(cause).toHaveURL(input.origin + linkedPath);
  await expect(
    cause.getByRole("textbox", { name: "Find...", exact: true }),
  ).toHaveValue(enqueue.spanId);
  await expect(
    cause.locator(".SpanDetail--debugInfo").filter({ hasText: enqueue.spanId }),
  ).toBeVisible();
  await cause
    .getByRole("switch", {
      name: request.service + " " + request.name,
      exact: true,
    })
    .click();
  await expect(
    cause.locator(".SpanDetail--debugInfo").filter({ hasText: request.spanId }),
  ).toBeVisible();
  expect(diagnostics).toEqual([]);
  console.log(
    JSON.stringify({
      status: "passed",
      browser: browser.version(),
      searchWindowMicros: window,
      controls,
      attemptTraceId: attempt.traceId,
      linkedTraceId: enqueue.traceId,
      highlightedSpanId: enqueue.spanId,
      parentSpanId: request.spanId,
      wrongLinkRefused,
      diagnostics: "passed",
    }),
  );
  await context.close();
} finally {
  await browser.close();
}
